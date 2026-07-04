package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

type imageGeneratorFunc func(ctx context.Context, prompt, model, size, resolution string, refs [][]byte, n int) ([]upstreamImageResult, error)

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
	accounts := s.store.LoadAccounts()
	maxAttempts := len(accounts)
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	excluded := map[string]bool{}
	var lastErr error
	for attempt := 0; attempt < maxAttempts; {
		traceLogf(ctx, "├─ image account attempt %d/%d model=%s resolution=%s excluded=%d", attempt+1, maxAttempts, model, resolution, len(excluded))
		account, err := s.pickAccountExcluding(model, resolution, excluded)
		if err != nil {
			if lastErr != nil {
				return nil, lastErr
			}
			return nil, err
		}
		poolToken := account.AccessToken
		client, actualAccount, err := s.upstreamClientForImageAccount(model, resolution, account)
		if err != nil {
			s.accountPool.releaseToken(poolToken)
			excluded[poolToken] = true
			if errors.Is(err, errImageAccountBusy) {
				lastErr = err
				attempt++
				continue
			}
			if lastErr != nil {
				return nil, lastErr
			}
			return nil, err
		}
		token := actualAccount.AccessToken
		leaseID, leased, err := s.acquireImageAccountLease(ctx, token)
		if err != nil {
			s.accountPool.releaseToken(poolToken)
			excluded[poolToken] = true
			excluded[token] = true
			lastErr = err
			attempt++
			continue
		}
		if !leased {
			s.accountPool.releaseToken(poolToken)
			excluded[poolToken] = true
			excluded[token] = true
			lastErr = errImageAccountBusy
			attempt++
			continue
		}
		attempt++
		traceLogf(ctx, "│  ├─ selected image account %s", accountLabel(actualAccount))
		excluded[poolToken] = true
		excluded[token] = true
		items, err := client.GenerateImage(ctx, prompt, model, size, resolution, refs, s.imageGenerationOptions())
		s.accountPool.releaseToken(poolToken)
		s.releaseImageAccountLease(ctx, leaseID)
		if err == nil {
			traceLogf(ctx, "└─ image account attempt %d success images=%d", attempt+1, len(items))
			s.markAccountSuccess(token, true)
			return items, nil
		}
		traceLogf(ctx, "│  └─ image account attempt %d failed error=%v", attempt+1, err)
		s.markAccountFailure(token, err, true)
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
			items, err := s.generateImageWithPool(ctx, prompt, model, size, resolution, refs)
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
