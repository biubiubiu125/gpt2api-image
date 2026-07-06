package app

import (
	"context"
	"log"
	"time"
)

func (s *Server) startLimitedAccountWatcher() {
	interval := time.Duration(s.cfg.RefreshAccountIntervalMinute) * time.Minute
	if interval <= 0 {
		interval = 60 * time.Minute
	}
	go func() {
		for {
			time.Sleep(interval)
			accounts := s.store.LoadAccounts()
			var limited []string
			for _, a := range accounts {
				if isAccountStatus(a.Status, accountStatusLimited) && a.AccessToken != "" {
					limited = append(limited, a.AccessToken)
				}
			}
			if len(limited) == 0 {
				continue
			}
			log.Printf("[account-limited-watcher] checking %d limited accounts", len(limited))
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
						mergeRefreshedAccountInfo(&updated[i], info)
						if isAccountStatus(info.Status, accountStatusNormal) && !info.ImageQuotaUnknown && info.Quota > 0 {
							updated[i].Status = accountStatusNormal
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
	}()
}
