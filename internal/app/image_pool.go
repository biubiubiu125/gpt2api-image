package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

type imageGeneratorFunc func(ctx context.Context, prompt, model, size, resolution string, refs [][]byte, n int) ([]upstreamImageResult, error)

const maxImageAccountFallbackAttempts = 5

type imageAccountAttemptScope struct {
	mu        sync.Mutex
	max       int
	used      int
	attempted map[string]bool
}

func newImageAccountAttemptScope(max int) *imageAccountAttemptScope {
	if max < 1 {
		max = 1
	}
	return &imageAccountAttemptScope{max: max, attempted: map[string]bool{}}
}

func (s *imageAccountAttemptScope) exhausted() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.used >= s.max
}

func (s *imageAccountAttemptScope) usedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.used
}

func (s *imageAccountAttemptScope) excludedSnapshot() map[string]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]bool, len(s.attempted))
	for token, yes := range s.attempted {
		out[token] = yes
	}
	return out
}

func (s *imageAccountAttemptScope) reserve(primary string) bool {
	primary = strings.TrimSpace(primary)
	if primary == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.attempted[primary] || s.used >= s.max {
		return false
	}
	s.attempted[primary] = true
	s.used++
	return true
}

func (s *imageAccountAttemptScope) alias(tokens ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, token := range tokens {
		token = strings.TrimSpace(token)
		if token != "" {
			s.attempted[token] = true
		}
	}
}

func (s *Server) imageRequestTimeout() time.Duration {
	opts := s.imageGenerationOptions()
	return opts.Timeout + opts.PollInitialWait + 60*time.Second
}

func (s *Server) imageGenerationOptions() imageGenerationOptions {
	return normalizeImageGenerationOptions(imageGenerationOptions{
		Timeout:         time.Duration(s.cfg.ImagePollTimeoutSecs) * time.Second,
		PollInterval:    time.Duration(s.cfg.ImagePollIntervalSecs) * time.Second,
		PollInitialWait: time.Duration(s.cfg.ImagePollInitialWaitSecs) * time.Second,
		UploadTimeout:   time.Duration(s.cfg.ImagePollTimeoutSecs) * time.Second,
	})
}

func (s *Server) generateImageWithPool(ctx context.Context, prompt, model, size, resolution string, refs [][]byte) ([]upstreamImageResult, error) {
	return s.generateImageWithPoolScoped(ctx, prompt, model, size, resolution, refs, newImageAccountAttemptScope(maxImageAccountFallbackAttempts))
}

func (s *Server) generateImageWithPoolScoped(ctx context.Context, prompt, model, size, resolution string, refs [][]byte, scope *imageAccountAttemptScope) ([]upstreamImageResult, error) {
	model = normalizeImageModel(model)
	routes := s.imageRoutePlan()
	var lastErr error
	for idx, route := range routes {
		routeScope := scope
		if idx > 0 {
			routeScope = newImageAccountAttemptScope(maxImageAccountFallbackAttempts)
		}
		routeModel := internalImageModelForRoute(model, route)
		traceLogf(ctx, "├─ image route %s public_model=%s internal_model=%s", route, model, routeModel)
		items, err := s.generateImageWithPoolScopedRoute(ctx, prompt, routeModel, size, resolution, refs, routeScope)
		if err == nil {
			return items, nil
		}
		lastErr = err
		traceLogf(ctx, "│  └─ image route %s failed error=%v", route, err)
		if !shouldTryNextImageRoute(err, ctx.Err()) {
			break
		}
	}
	if lastErr == nil {
		lastErr = errors.New("no image route configured")
	}
	return nil, lastErr
}

func (s *Server) generateImageWithPoolScopedRoute(ctx context.Context, prompt, model, size, resolution string, refs [][]byte, scope *imageAccountAttemptScope) ([]upstreamImageResult, error) {
	if scope == nil {
		scope = newImageAccountAttemptScope(maxImageAccountFallbackAttempts)
	}
	s.cleanupPendingDeletedAccounts()
	s.cleanupUnusableImageAccounts()
	var lastErr error
	for !scope.exhausted() {
		excluded := scope.excludedSnapshot()
		traceLogf(ctx, "├─ image account attempt %d/%d model=%s resolution=%s excluded=%d", scope.usedCount()+1, maxImageAccountFallbackAttempts, model, resolution, len(excluded))
		account, err := s.pickAccountExcluding(model, resolution, excluded)
		if err != nil {
			if lastErr != nil {
				return nil, lastErr
			}
			return nil, err
		}
		poolToken := account.AccessToken
		if !scope.reserve(poolToken) {
			s.accountPool.releaseToken(poolToken)
			if scope.exhausted() {
				break
			}
			continue
		}
		client, actualAccount, err := s.upstreamClientForImageAccount(model, resolution, account)
		if err != nil {
			if !errors.Is(err, errImageAccountBusy) {
				s.markAccountFailure(poolToken, err, true)
			}
			s.accountPool.releaseToken(poolToken)
			s.cleanupPendingDeletedAccounts()
			if errors.Is(err, errImageAccountBusy) {
				lastErr = err
				continue
			}
			lastErr = err
			if shouldRetryImageAccount(err) {
				traceLogf(ctx, "│  ├─ retry with another image account after setup error")
				continue
			}
			return nil, err
		}
		token := actualAccount.AccessToken
		scope.alias(poolToken, token)
		hasTokenAlias := token != "" && token != poolToken
		if hasTokenAlias {
			s.accountPool.retainToken(token)
		}
		releaseSelectedToken := func() {
			s.accountPool.releaseToken(poolToken)
			if hasTokenAlias {
				s.accountPool.releaseToken(token)
			}
		}
		leaseID, leased, err := s.acquireImageAccountLease(ctx, token)
		if err != nil {
			releaseSelectedToken()
			lastErr = err
			continue
		}
		if !leased {
			releaseSelectedToken()
			lastErr = errImageAccountBusy
			continue
		}
		traceLogf(ctx, "│  ├─ selected image account %s", accountLabel(actualAccount))
		items, err := client.GenerateImage(ctx, prompt, model, size, resolution, refs, s.imageGenerationOptions())
		if err == nil {
			traceLogf(ctx, "└─ image account attempt success images=%d", len(items))
			s.markAccountSuccess(token, true)
			releaseSelectedToken()
			s.releaseImageAccountLease(ctx, leaseID)
			s.cleanupPendingDeletedAccounts()
			return items, nil
		}
		traceLogf(ctx, "│  └─ image account attempt failed error=%v", err)
		s.markAccountFailure(token, err, true)
		releaseSelectedToken()
		s.releaseImageAccountLease(ctx, leaseID)
		s.cleanupPendingDeletedAccounts()
		lastErr = err
		if !shouldRetryImageAccount(err) {
			return nil, err
		}
		traceLogf(ctx, "│  ├─ retry with another image account")
	}
	if lastErr == nil {
		lastErr = errors.New("no available image quota")
	}
	return nil, lastErr
}

func (s *Server) acquireImageAccountLease(ctx context.Context, token string) (string, bool, error) {
	if s.taskStore == nil {
		return "", true, nil
	}
	maxConc := s.cfg.ImageAccountConcurrency
	if maxConc < 1 {
		maxConc = 1
	}
	ttl := s.imageRequestTimeout() + 2*time.Minute
	return s.taskStore.AcquireAccountLease(ctx, token, maxConc, "image-"+randID(8), ttl)
}

func (s *Server) acquireOAuthRefreshLease(ctx context.Context, account Account) (string, bool, error) {
	if s.taskStore == nil {
		return "", true, nil
	}
	key := account.AccessToken
	if account.AccountID != nil && strings.TrimSpace(*account.AccountID) != "" {
		key = "account:" + strings.TrimSpace(*account.AccountID)
	} else if account.RefreshToken != nil && strings.TrimSpace(*account.RefreshToken) != "" {
		key = "refresh:" + strings.TrimSpace(*account.RefreshToken)
	}
	return s.taskStore.AcquireAccountLease(ctx, key, 1, "oauth-refresh-"+randID(8), 90*time.Second)
}

func (s *Server) releaseImageAccountLease(ctx context.Context, leaseID string) {
	if s.taskStore == nil || leaseID == "" {
		return
	}
	releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if ctx.Err() == nil {
		releaseCtx = ctx
	}
	if err := s.taskStore.ReleaseAccountLease(releaseCtx, leaseID); err != nil {
		traceLogf(ctx, "image account lease release failed: %v", err)
	}
}

func (s *Server) generateImagesWithPool(ctx context.Context, prompt, model, size, resolution string, refs [][]byte, n int) ([]upstreamImageResult, error) {
	if n <= 1 {
		return s.generateImageWithPool(ctx, prompt, model, size, resolution, refs)
	}
	scope := newImageAccountAttemptScope(maxImageAccountFallbackAttempts)
	limit := s.cfg.ImageAccountConcurrency
	if limit <= 0 {
		limit = 1
	}
	if limit > n {
		limit = n
	}
	sem := make(chan struct{}, limit)
	results := make([][]upstreamImageResult, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case <-ctx.Done():
				errs[i] = ctx.Err()
				return
			case sem <- struct{}{}:
			}
			defer func() { <-sem }()
			items, err := s.generateImageWithPoolScoped(ctx, prompt, model, size, resolution, refs, scope)
			if err != nil {
				errs[i] = err
				return
			}
			results[i] = items
		}()
	}
	wg.Wait()
	out := []upstreamImageResult{}
	var lastErr error
	for i := 0; i < n; i++ {
		if len(results[i]) > 0 {
			out = append(out, results[i][0])
		}
		if errs[i] != nil {
			lastErr = errs[i]
		}
	}
	if len(out) > 0 {
		return out, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("upstream returned no image")
}

func (s *Server) generateTaskImages(ctx context.Context, prompt, model, size, resolution string, refs [][]byte, n int) ([]upstreamImageResult, error) {
	if s.imageGenerator != nil {
		return s.imageGenerator(ctx, prompt, model, size, resolution, refs, n)
	}
	return s.generateImagesWithPool(ctx, prompt, model, size, resolution, refs, n)
}

func shouldRetryImageAccount(err error) bool {
	if err == nil {
		return false
	}
	return isRateLimitErrorText(err) || isInvalidTokenErrorText(err) || isUpstreamBlockErrorText(err) || isTurnstileRequirementErrorText(err) || isRetryableBootstrapError(err) || isTemporaryUpstreamErrorText(err)
}
