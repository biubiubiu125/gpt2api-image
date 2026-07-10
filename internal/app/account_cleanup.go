package app

import (
	"context"
	"log"
	"strings"
	"time"
)

const (
	accountStatusNormal   = "正常"
	accountStatusDisabled = "禁用"
	accountStatusInvalid  = "异常"
	accountStatusLimited  = "限流"

	accountRefreshValidationGracePeriod = 10 * time.Minute
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

func (s *Server) imageAccountRecordRemovalReason(a Account) (string, bool) {
	return s.imageAccountRemovalReason(a, false)
}

func (s *Server) registerImageAccountRemovalReason(a Account) (string, bool) {
	return s.imageAccountRemovalReason(a, true)
}

func (s *Server) imageAccountRemovalReason(a Account, includePendingRefreshValidation bool) (string, bool) {
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
		if !s.cfg.AutoRemoveInvalidAccounts {
			return "", false
		}
		return "account_status_invalid", true
	}
	if isAccountStatus(a.Status, accountStatusLimited) && s.cfg.AutoRemoveRateLimitedAccounts {
		return "image_account_rate_limited", true
	}
	if a.RefreshValidationPending && !includePendingRefreshValidation && accountRefreshValidationRecentlyStarted(a) {
		return "", false
	}
	if a.ImageQuotaUnknown {
		if accountHasUploadQuotaRuntimeValue(a) || !s.cfg.AutoDeleteQuotaZeroAccounts {
			return "", false
		}
		return "image_quota_unknown", true
	}
	if a.Quota <= 0 {
		if accountHasUploadQuotaRuntimeValue(a) || !s.cfg.AutoDeleteQuotaZeroAccounts {
			return "", false
		}
		return "image_quota_empty", true
	}
	return "", false
}

func (s *Server) imageAccountRuntimeRemovalReason(a Account) (string, bool) {
	if reason, ok := s.imageAccountRecordRemovalReason(a); ok {
		return reason, true
	}
	if a.RefreshValidationPending && accountRefreshValidationRecentlyStarted(a) {
		return "", false
	}
	if s.cfg.AutoDeleteQuotaZeroAccounts && a.Quota <= 0 {
		return "image_quota_empty", true
	}
	if s.cfg.AutoDeleteUploadQuotaZeroAccounts && !a.UploadQuotaUnknown && a.UploadQuota <= 0 {
		return "upload_quota_empty", true
	}
	if s.cfg.AutoRefreshDeleteFailedAccounts && strings.TrimSpace(ptrString(a.LastRefreshError)) != "" {
		return "refresh_failed", true
	}
	if a.Consecutive403 >= delete403ConsecutiveThreshold(s.cfg) {
		return "upstream_403", true
	}
	if a.ConsecutiveTimeout >= deleteTimeoutConsecutiveThreshold(s.cfg) {
		return "temporary_timeout", true
	}
	return "", false
}

func accountHasUploadQuotaRuntimeValue(a Account) bool {
	if a.UploadQuota > 0 {
		return true
	}
	if !a.UploadQuotaUnknown && (a.UploadLimitResetAt != nil || a.UploadLimitFeatureName != nil) {
		return true
	}
	for _, item := range a.LimitsProgress {
		if isUploadLimitFeatureName(strAny(item["feature_name"], "")) {
			return true
		}
	}
	return false
}

func accountRefreshValidationRecentlyStarted(a Account) bool {
	if !a.RefreshValidationPending || a.CreatedAt == nil {
		return false
	}
	createdAt := strings.TrimSpace(*a.CreatedAt)
	if createdAt == "" {
		return false
	}
	startedAt, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		startedAt, err = time.Parse(time.RFC3339, createdAt)
	}
	if err != nil {
		return false
	}
	return time.Since(startedAt) < accountRefreshValidationGracePeriod
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

func (s *Server) removeOrMarkImageAccount(token, reason string) (bool, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return false, nil
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "image_account_unusable"
	}
	now := nowISO()
	hasActiveWork := s.imageAccountHasActiveWork(token)
	before := s.store.LoadAccounts()
	changed := false
	removed := false
	if err := s.store.UpdateAccounts(func(accounts []Account) []Account {
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
	}); err != nil {
		log.Printf("[account-cleanup] failed to save account cleanup state: %v", err)
		return false, err
	}
	if changed && s.logSvc != nil {
		action := "清退生图账号"
		if !removed {
			action = "标记生图账号待清退"
		}
		s.logSvc.add("account", action, map[string]any{"reason": reason, "removed": removed, "emails": accountEmailsForTokens(before, []string{token})})
	}
	return changed, nil
}

func (s *Server) cleanupPendingDeletedAccounts() int {
	removed := 0
	for _, account := range s.store.LoadAccounts() {
		if !account.PendingDelete {
			continue
		}
		before := len(s.store.LoadAccounts())
		if _, err := s.removeOrMarkImageAccount(account.AccessToken, firstNonEmpty(ptrString(account.DeleteReason), "pending_delete")); err != nil {
			continue
		}
		after := len(s.store.LoadAccounts())
		if after < before {
			removed += before - after
		}
	}
	return removed
}

func (s *Server) cleanupUnusableImageAccounts() int {
	changed := 0
	for _, account := range s.store.LoadAccounts() {
		reason, ok := s.imageAccountRuntimeRemovalReason(account)
		if !ok {
			continue
		}
		if ok, err := s.removeOrMarkImageAccount(account.AccessToken, reason); err != nil || !ok {
			continue
		}
		changed++
	}
	return changed
}

func (s *Server) cleanupAccounts() int {
	changed := s.cleanupPendingDeletedAccounts()
	changed += s.cleanupUnusableImageAccounts()
	return changed
}

func (s *Server) cleanupAccountsAndMaybeRefill(reason string) int {
	changed := s.cleanupAccounts()
	if changed > 0 {
		s.triggerAutoRefillIfNeeded(reason)
	}
	return changed
}

func (s *Server) effectiveAvailableImageAccounts() int {
	count := 0
	now := time.Now().UTC()
	for _, account := range s.store.LoadAccounts() {
		if !s.isEffectiveAvailableImageAccount(account, now) {
			continue
		}
		count++
	}
	return count
}

func (s *Server) isEffectiveAvailableImageAccount(account Account, now time.Time) bool {
	if strings.TrimSpace(account.AccessToken) == "" || account.PendingDelete || isAccountDisabled(account.Status) || isAccountInvalidStatus(account.Status) {
		return false
	}
	if accountHasActiveLimitWindow(account, now, true) {
		return false
	}
	if account.ImageQuotaUnknown || account.Quota <= 0 {
		return false
	}
	if !account.UploadQuotaUnknown && account.UploadQuota <= 0 {
		return false
	}
	if account.Consecutive403 >= delete403ConsecutiveThreshold(s.cfg) {
		return false
	}
	if account.ConsecutiveTimeout >= deleteTimeoutConsecutiveThreshold(s.cfg) {
		return false
	}
	if strings.TrimSpace(ptrString(account.LastRefreshError)) != "" && s.cfg.AutoRefreshDeleteFailedAccounts {
		return false
	}
	return true
}

func (s *Server) triggerAutoRefillIfNeeded(reason string) {
	if !s.registerExecutorConfigured() || !s.cfg.AutoRefillUseEffectiveAvailable {
		return
	}
	cfg, err := s.fetchRegisterExecutorConfig()
	if err != nil {
		log.Printf("[account-refill] fetch register config failed: %v", err)
		return
	}
	refill, _ := cfg["auto_refill"].(map[string]any)
	if !boolAny(refill["enabled"], false) {
		return
	}
	minAvailable := intAny(refill["min_available"], 30)
	if minAvailable < 1 {
		minAvailable = 1
	}
	current := s.effectiveAvailableImageAccounts()
	if current >= minAvailable {
		if s.logSvc != nil {
			s.logSvc.add("account", "自动补号跳过", map[string]any{"reason": reason, "effective_available": current, "min_available": minAvailable})
		}
		return
	}
	batchTotal := intAny(refill["batch_total"], 100)
	if batchTotal < 1 {
		batchTotal = 1
	}
	if _, err := s.postRegisterExecutor("/api/register/start-auto-refill", map[string]any{"batch_total": batchTotal, "reason": reason, "effective_available": current, "min_available": minAvailable}); err != nil {
		log.Printf("[account-refill] trigger auto refill failed: %v", err)
		if s.logSvc != nil {
			s.logSvc.add("account", "自动补号触发失败", map[string]any{"reason": reason, "effective_available": current, "min_available": minAvailable, "error": err.Error()})
		}
		return
	}
	if s.logSvc != nil {
		s.logSvc.add("account", "自动补号触发", map[string]any{"reason": reason, "effective_available": current, "min_available": minAvailable, "batch_total": batchTotal})
	}
}
