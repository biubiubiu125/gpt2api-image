package app

import (
	"context"
	"strings"
	"time"
)

const (
	accountStatusNormal   = "正常"
	accountStatusDisabled = "禁用"
	accountStatusInvalid  = "异常"
	accountStatusLimited  = "限流"
)

func isAccountStatus(status, want string) bool {
	return strings.TrimSpace(status) == want
}

func isAccountDisabled(status string) bool {
	return isAccountStatus(status, accountStatusDisabled)
}

func isAccountInvalidStatus(status string) bool {
	status = strings.TrimSpace(status)
	return status != "" && status != accountStatusNormal && status != accountStatusDisabled && status != accountStatusLimited
}

func imageAccountRecordRemovalReason(a Account) (string, bool) {
	if strings.TrimSpace(a.AccessToken) == "" {
		return "", false
	}
	if a.PendingDelete {
		if a.DeleteReason != nil && strings.TrimSpace(*a.DeleteReason) != "" {
			return strings.TrimSpace(*a.DeleteReason), true
		}
		return "pending_delete", true
	}
	if isAccountInvalidStatus(a.Status) {
		return "account_status_invalid", true
	}
	if a.ImageQuotaUnknown {
		return "image_quota_unknown", true
	}
	if a.Quota <= 0 {
		return "image_quota_empty", true
	}
	return "", false
}

func imageAccountErrorRemovalReason(err error) (string, bool) {
	switch {
	case err == nil:
		return "", false
	case isInvalidTokenErrorText(err):
		return "invalid_token", true
	default:
		return "", false
	}
}

func (s *Server) imageAccountHasActiveWork(token string) bool {
	token = strings.TrimSpace(token)
	if token == "" {
		return false
	}
	if s.accountPool != nil && s.accountPool.activeCount(token) > 0 {
		return true
	}
	if s.taskStore == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	active, err := s.taskStore.CountAccountLeases(ctx, token)
	if err != nil {
		return true
	}
	return active > 0
}

func (s *Server) removeOrMarkImageAccount(token, reason string) bool {
	token = strings.TrimSpace(token)
	if token == "" {
		return false
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "image_account_unusable"
	}
	now := nowISO()
	hasActiveWork := s.imageAccountHasActiveWork(token)
	changed := false
	removed := false
	_ = s.store.UpdateAccounts(func(accounts []Account) []Account {
		out := accounts[:0]
		for _, a := range accounts {
			if a.AccessToken != token {
				out = append(out, a)
				continue
			}
			changed = true
			if hasActiveWork {
				a.Status = accountStatusInvalid
				a.Quota = 0
				a.PendingDelete = true
				a.DeleteReason = &reason
				a.DeleteMarkedAt = &now
				out = append(out, a)
				continue
			}
			removed = true
		}
		return out
	})
	if changed && s.logSvc != nil {
		action := "清退生图账号"
		if !removed {
			action = "标记生图账号待清退"
		}
		s.logSvc.add("account", action, map[string]any{"reason": reason, "removed": removed})
	}
	return changed
}

func (s *Server) cleanupPendingDeletedAccounts() int {
	removed := 0
	for _, account := range s.store.LoadAccounts() {
		if !account.PendingDelete {
			continue
		}
		before := len(s.store.LoadAccounts())
		s.removeOrMarkImageAccount(account.AccessToken, firstNonEmpty(ptrString(account.DeleteReason), "pending_delete"))
		after := len(s.store.LoadAccounts())
		if after < before {
			removed += before - after
		}
	}
	return removed
}

func (s *Server) cleanupUnusableImageAccounts() int {
	removed := 0
	for _, account := range s.store.LoadAccounts() {
		reason, ok := imageAccountRecordRemovalReason(account)
		if !ok {
			continue
		}
		before := len(s.store.LoadAccounts())
		s.removeOrMarkImageAccount(account.AccessToken, reason)
		after := len(s.store.LoadAccounts())
		if after < before {
			removed += before - after
		}
	}
	return removed
}
