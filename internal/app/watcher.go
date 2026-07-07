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
		s.cleanupUnusableImageAccounts()
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
