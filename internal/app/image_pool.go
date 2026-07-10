package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

type imageGeneratorFunc func(ctx context.Context, prompt, model, size, resolution string, refs [][]byte, n int) ([]upstreamImageResult, error)

const defaultImageAccountFallbackAttempts = 3

func (s *Server) imageAccountFallbackAttempts() int {
	if s == nil || s.cfg.ImageAccountFallbackAttempts <= 0 {
		return defaultImageAccountFallbackAttempts
	}
	return s.cfg.ImageAccountFallbackAttempts
}

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
	return s.generateImageWithPoolScoped(ctx, prompt, model, size, resolution, refs, newImageAccountAttemptScope(s.imageAccountFallbackAttempts()))
}

func (s *Server) generateImageWithPoolForIdentity(ctx context.Context, id *Identity, prompt, model, size, resolution string, refs [][]byte) ([]upstreamImageResult, error) {
	routes, err := s.imageRoutePlanForIdentity(id)
	if err != nil {
		return nil, err
	}
	if s.imageGenerator != nil {
		return s.imageGenerator(ctx, prompt, model, size, resolution, refs, 1)
	}
	return s.generateImageWithPoolScopedRoutes(ctx, prompt, model, size, resolution, refs, newImageAccountAttemptScope(s.imageAccountFallbackAttempts()), routes)
}

func (s *Server) generateImageWithPoolScoped(ctx context.Context, prompt, model, size, resolution string, refs [][]byte, scope *imageAccountAttemptScope) ([]upstreamImageResult, error) {
	return s.generateImageWithPoolScopedRoutes(ctx, prompt, model, size, resolution, refs, scope, s.imageRoutePlan())
}

func (s *Server) generateImageWithPoolScopedRoutes(ctx context.Context, prompt, model, size, resolution string, refs [][]byte, scope *imageAccountAttemptScope, routes []string) ([]upstreamImageResult, error) {
	requestedCodex := isCodexImageRequest(model, resolution)
	model = normalizeImageModel(model)
	var lastErr error
	var previousRouteErr error
	for idx, route := range routes {
		routeScope := scope
		if idx > 0 && route == imageRouteCodex && previousRouteErr != nil && !requestedCodex && !s.hasPotentialCodexImageAccount() {
			traceLogf(ctx, "│  └─ image route %s skipped: no available codex Plus/Team/Pro account; keep previous web error=%v", route, previousRouteErr)
			break
		}
		routeModel := internalImageModelForRoute(model, route)
		traceLogf(ctx, "├─ image route %s public_model=%s internal_model=%s", route, model, routeModel)
		items, err := s.generateImageWithPoolScopedRoute(ctx, prompt, routeModel, size, resolution, refs, routeScope)
		if err == nil {
			return items, nil
		}
		lastErr = preferPreviousImageRouteError(previousRouteErr, err)
		traceLogf(ctx, "│  └─ image route %s failed error=%v", route, err)
		if previousRouteErr == nil {
			previousRouteErr = err
		}
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
		scope = newImageAccountAttemptScope(s.imageAccountFallbackAttempts())
	}
	s.cleanupPendingDeletedAccounts()
	s.cleanupAccountsAndMaybeRefill("image_request_start")
	var lastErr error
	for !scope.exhausted() {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		excluded := scope.excludedSnapshot()
		traceLogf(ctx, "├─ image account attempt %d/%d model=%s resolution=%s excluded=%d", scope.usedCount()+1, scope.max, model, resolution, len(excluded))
		requiredUploads := 0
		if !isCodexImageRequest(model, resolution) {
			requiredUploads = len(refs)
		}
		account, err := s.pickAccountExcludingForUploads(model, resolution, excluded, requiredUploads)
		if err != nil {
			if lastErr != nil {
				return nil, lastErr
			}
			return nil, err
		}
		poolToken := account.AccessToken
		if !scope.reserve(poolToken) {
			s.accountPool.releaseToken(poolToken)
			if requiredUploads > 0 {
				s.accountPool.releaseUploadReservation(poolToken, requiredUploads)
			}
			if scope.exhausted() {
				break
			}
			continue
		}
		client, actualAccount, err := s.upstreamClientForImageAccount(model, resolution, account)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				s.accountPool.releaseToken(poolToken)
				if requiredUploads > 0 {
					s.accountPool.releaseUploadReservation(poolToken, requiredUploads)
				}
				s.cleanupPendingDeletedAccounts()
				return nil, ctxErr
			}
			if errors.Is(err, context.Canceled) {
				s.accountPool.releaseToken(poolToken)
				if requiredUploads > 0 {
					s.accountPool.releaseUploadReservation(poolToken, requiredUploads)
				}
				s.cleanupPendingDeletedAccounts()
				return nil, err
			}
			if !errors.Is(err, errImageAccountBusy) {
				s.markAccountFailure(poolToken, err, true)
			}
			s.accountPool.releaseToken(poolToken)
			if requiredUploads > 0 {
				s.accountPool.releaseUploadReservation(poolToken, requiredUploads)
			}
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
			if requiredUploads > 0 {
				s.accountPool.releaseUploadReservation(poolToken, requiredUploads)
			}
			if hasTokenAlias {
				s.accountPool.releaseToken(token)
			}
		}
		leaseID, leased, err := s.acquireImageAccountLease(ctx, token, actualAccount, requiredUploads)
		if err != nil {
			releaseSelectedToken()
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			lastErr = err
			continue
		}
		if !leased {
			releaseSelectedToken()
			lastErr = errImageAccountBusy
			continue
		}
		traceLogf(ctx, "│  ├─ selected image account %s", accountLabel(actualAccount))
		opts := s.imageGenerationOptions()
		if requiredUploads > 0 {
			opts.OnReferenceUpload = func() {
				s.markAccountUploadSuccess(token, 1)
			}
		}
		items, err := client.GenerateImage(ctx, prompt, model, size, resolution, refs, opts)
		if err == nil {
			traceLogf(ctx, "└─ image account attempt success images=%d", len(items))
			s.markAccountSuccess(token, true)
			releaseSelectedToken()
			s.releaseImageAccountLease(ctx, leaseID)
			s.cleanupPendingDeletedAccounts()
			return items, nil
		}
		traceLogf(ctx, "│  └─ image account attempt failed error=%v", err)
		if ctxErr := ctx.Err(); ctxErr != nil {
			releaseSelectedToken()
			s.releaseImageAccountLease(ctx, leaseID)
			s.cleanupPendingDeletedAccounts()
			return nil, ctxErr
		}
		if errors.Is(err, context.Canceled) {
			releaseSelectedToken()
			s.releaseImageAccountLease(ctx, leaseID)
			s.cleanupPendingDeletedAccounts()
			return nil, err
		}
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

func (s *Server) acquireImageAccountLease(ctx context.Context, token string, account Account, requiredUploads int) (string, bool, error) {
	return s.acquireUploadAccountLease(ctx, token, account, requiredUploads, "image-"+randID(8), s.imageRequestTimeout()+2*time.Minute)
}

func (s *Server) acquireUploadAccountLease(ctx context.Context, token string, account Account, requiredUploads int, holder string, ttl time.Duration) (string, bool, error) {
	if s.taskStore == nil {
		return "", true, nil
	}
	maxConc := s.cfg.ImageAccountConcurrency
	if maxConc < 1 {
		maxConc = 1
	}
	maxUploadReservations := -1
	if requiredUploads > 0 {
		if account.UploadQuotaUnknown {
			return "", false, errImageUploadQuotaReserved
		} else {
			maxUploadReservations = account.UploadQuota
		}
	}
	if strings.TrimSpace(holder) == "" {
		holder = "upload-" + randID(8)
	}
	return s.taskStore.AcquireAccountLeaseWithUploadQuota(ctx, token, maxConc, maxUploadReservations, requiredUploads, holder, ttl)
}

func (s *Server) releaseTextUploadSelection(ctx context.Context, token, leaseID string, requiredUploads int) {
	if requiredUploads <= 0 {
		return
	}
	s.accountPool.releaseToken(token)
	s.accountPool.releaseUploadReservation(token, requiredUploads)
	s.releaseImageAccountLease(ctx, leaseID)
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
	if s.imageGenerator != nil {
		return s.imageGenerator(ctx, prompt, model, size, resolution, refs, n)
	}
	return s.generateImagesWithPoolRoutes(ctx, prompt, model, size, resolution, refs, n, s.imageRoutePlan())
}

func (s *Server) generateImagesWithPoolForIdentity(ctx context.Context, id *Identity, prompt, model, size, resolution string, refs [][]byte, n int) ([]upstreamImageResult, error) {
	routes, err := s.imageRoutePlanForIdentity(id)
	if err != nil {
		return nil, err
	}
	if s.imageGenerator != nil {
		return s.imageGenerator(ctx, prompt, model, size, resolution, refs, n)
	}
	return s.generateImagesWithPoolRoutes(ctx, prompt, model, size, resolution, refs, n, routes)
}

func (s *Server) generateImagesWithPoolRoutes(ctx context.Context, prompt, model, size, resolution string, refs [][]byte, n int, routes []string) ([]upstreamImageResult, error) {
	if n <= 1 {
		return s.generateImageWithPoolScopedRoutes(ctx, prompt, model, size, resolution, refs, newImageAccountAttemptScope(s.imageAccountFallbackAttempts()), routes)
	}
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
			scope := newImageAccountAttemptScope(s.imageAccountFallbackAttempts())
			items, err := s.generateImageWithPoolScopedRoutes(ctx, prompt, model, size, resolution, refs, scope, routes)
			if err != nil {
				errs[i] = err
				return
			}
			results[i] = items
		}()
	}
	wg.Wait()
	return collectImageBatchResults(ctx, results, errs)
}

func collectImageBatchResults(ctx context.Context, results [][]upstreamImageResult, errs []error) ([]upstreamImageResult, error) {
	out := []upstreamImageResult{}
	var lastErr error
	var blockingErr error
	if ctxErr := ctx.Err(); ctxErr != nil {
		blockingErr = ctxErr
	}
	for i := 0; i < len(results) || i < len(errs); i++ {
		if i < len(results) && len(results[i]) > 0 {
			out = append(out, results[i][0])
		}
		if i < len(errs) && errs[i] != nil {
			lastErr = errs[i]
			if blockingErr == nil && isNonSwallowableImageBatchError(errs[i]) {
				blockingErr = errs[i]
			}
		}
	}
	if blockingErr != nil {
		return nil, blockingErr
	}
	if len(out) > 0 {
		return out, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("upstream returned no image")
}

func isNonSwallowableImageBatchError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return true
	}
	return isImagePolicyMessage(err.Error()) || isUploadLimitErrorText(err)
}

func (s *Server) generateTaskImages(ctx context.Context, prompt, model, size, resolution string, refs [][]byte, n int) ([]upstreamImageResult, error) {
	return s.generateImagesWithPool(ctx, prompt, model, size, resolution, refs, n)
}

func (s *Server) generateTaskImagesForIdentity(ctx context.Context, id *Identity, prompt, model, size, resolution string, refs [][]byte, n int) ([]upstreamImageResult, error) {
	return s.generateImagesWithPoolForIdentity(ctx, id, prompt, model, size, resolution, refs, n)
}

func shouldRetryImageAccount(err error) bool {
	if err == nil {
		return false
	}
	return isUploadLimitErrorText(err) || isRateLimitErrorText(err) || isInvalidTokenErrorText(err) || isUpstreamBlockErrorText(err) || isTurnstileRequirementErrorText(err) || isRetryableBootstrapError(err) || isTemporaryUpstreamErrorText(err)
}
