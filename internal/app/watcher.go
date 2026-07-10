package app

import (
	"context"
	"log"
	"strings"
	"time"
)

func (s *Server) startLimitedAccountWatcher() {
	interval := time.Duration(s.cfg.RefreshAccountIntervalMinute) * time.Minute
	if interval <= 0 {
		interval = 60 * time.Minute
	}
	go func() {
		for {
			s.refreshAccountsNeedingLimitRefresh()
			time.Sleep(interval)
		}
	}()
}

func (s *Server) startAutoAccountRefreshWatcher() {
	go func() {
		lastRun := time.Time{}
		for {
			if !s.cfg.AutoRefreshAccountsEnabled {
				lastRun = time.Now()
				time.Sleep(5 * time.Second)
				continue
			}
			interval := autoAccountRefreshInterval(s.cfg)
			if lastRun.IsZero() || time.Since(lastRun) >= interval {
				lastRun = time.Now()
				s.autoRefreshAccounts()
			}
			time.Sleep(5 * time.Second)
		}
	}()
}

func autoAccountRefreshInterval(cfg Config) time.Duration {
	interval := time.Duration(cfg.AutoRefreshAccountsIntervalMinutes) * time.Minute
	if interval <= 0 {
		interval = 60 * time.Minute
	}
	return interval
}

func (s *Server) startAccountCleanupWatcher() {
	go func() {
		lastRun := time.Time{}
		for {
			if !s.cfg.AutoCleanupAccountsEnabled {
				lastRun = time.Now()
				time.Sleep(5 * time.Second)
				continue
			}
			interval := accountCleanupInterval(s.cfg)
			if lastRun.IsZero() || time.Since(lastRun) >= interval {
				lastRun = time.Now()
				removed := s.cleanupAccountsAndMaybeRefill("account_cleanup_watcher")
				if removed > 0 && s.logSvc != nil {
					s.logSvc.add("account", "\u5b9a\u65f6\u6e05\u7406\u5f02\u5e38\u8d26\u53f7", map[string]any{"removed": removed, "effective_available": s.effectiveAvailableImageAccounts()})
				}
			}
			time.Sleep(5 * time.Second)
		}
	}()
}

func accountCleanupInterval(cfg Config) time.Duration {
	interval := time.Duration(cfg.AutoCleanupAccountsIntervalSeconds) * time.Second
	if interval < 10*time.Second {
		interval = 60 * time.Second
	}
	return interval
}

func (s *Server) autoRefreshAccounts() {
	accounts := s.store.LoadAccounts()
	tokens := s.nextAutoRefreshTokens(accounts, s.cfg.AutoRefreshAccountsBatchSize)
	if len(tokens) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(maxInt(60, len(tokens)*45+30))*time.Second)
	defer cancel()
	before := s.store.LoadAccounts()
	refreshed, errs, removed, cleanupRemoved := s.refreshAccountInfos(ctx, tokens)
	if s.logSvc != nil {
		s.logSvc.add("account", "定时刷新账号", map[string]any{"refreshed": refreshed, "errors": len(errs), "removed_unusable": removed, "cleanup_removed": cleanupRemoved, "emails": accountEmailsForRefreshLog(before, s.store.LoadAccounts(), tokens)})
	}
	if s.cfg.AutoRefreshTriggerRefill {
		s.triggerAutoRefillIfNeeded("auto_account_refresh")
	}
}

func (s *Server) nextAutoRefreshTokens(accounts []Account, batchSize int) []string {
	eligible := make([]string, 0, len(accounts))
	for _, account := range accounts {
		token := strings.TrimSpace(account.AccessToken)
		if token == "" || account.PendingDelete {
			continue
		}
		eligible = append(eligible, token)
	}
	if len(eligible) == 0 {
		return nil
	}
	if batchSize <= 0 || batchSize >= len(eligible) {
		s.autoRefreshMu.Lock()
		s.autoRefreshCursor = 0
		s.autoRefreshMu.Unlock()
		return eligible
	}
	s.autoRefreshMu.Lock()
	start := s.autoRefreshCursor % len(eligible)
	s.autoRefreshCursor = (start + batchSize) % len(eligible)
	s.autoRefreshMu.Unlock()
	out := make([]string, 0, batchSize)
	for i := 0; i < batchSize; i++ {
		out = append(out, eligible[(start+i)%len(eligible)])
	}
	return out
}

func (s *Server) refreshAccountsNeedingLimitRefresh() {
	limited := accountsNeedingLimitRefresh(s.store.LoadAccounts())
	if len(limited) == 0 {
		return
	}
	log.Printf("[account-limited-watcher] checking %d accounts needing limit refresh", len(limited))
	for _, token := range limited {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		client, err := NewUpstreamClientForAccount(s.accountByToken(token), s.cfg.Proxy, s.ensureCurlImpersonateBinary)
		if err != nil {
			cancel()
			continue
		}
		info, err := client.GetUserInfo(ctx)
		cancel()
		if err != nil {
			continue
		}
		if err := s.store.UpdateAccounts(func(updated []Account) []Account {
			for i := range updated {
				if updated[i].AccessToken != token {
					continue
				}
				if !accountNeedsLimitRefresh(updated[i]) {
					break
				}
				mergeRefreshedAccountInfo(&updated[i], info)
				if isAccountStatus(info.Status, accountStatusNormal) && !info.ImageQuotaUnknown && info.Quota > 0 {
					updated[i].Status = accountStatusNormal
					updated[i].ImageLimitResetAt = nil
					updated[i].RestoreAt = nil
					updated[i].RateLimitedAt = nil
					updated[i].RateLimitResetAt = nil
				}
				break
			}
			return updated
		}); err != nil {
			log.Printf("[account-limited-watcher] failed to save refreshed account: %v", err)
			continue
		}
		s.cleanupAccountsAndMaybeRefill("limited_account_refresh")
	}
}

func accountsNeedingLimitRefresh(accounts []Account) []string {
	tokens := []string{}
	for _, account := range accounts {
		if accountNeedsLimitRefresh(account) {
			tokens = append(tokens, strings.TrimSpace(account.AccessToken))
		}
	}
	return tokens
}

func accountNeedsLimitRefresh(a Account) bool {
	if strings.TrimSpace(a.AccessToken) == "" {
		return false
	}
	if a.PendingDelete || isAccountDisabled(a.Status) || isAccountInvalidStatus(a.Status) {
		return false
	}
	if isAccountStatus(a.Status, accountStatusLimited) {
		return true
	}
	if a.ImageQuotaUnknown || a.Quota <= 0 {
		return true
	}
	if a.UploadQuotaUnknown {
		return true
	}
	if a.UploadQuota > 0 {
		return false
	}
	resetAt := ptrString(a.UploadLimitResetAt)
	if resetAt == "" {
		return true
	}
	rt, err := parseAccountTime(resetAt)
	return err != nil || !time.Now().UTC().Before(rt)
}
