package app

import (
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	defaultRateLimitRestoreDelay    = 5 * time.Minute
	freeUploadLimitFallbackDelay    = 24 * time.Hour
	paidUploadLimitFallbackDelay    = 3 * time.Hour
	unknownUploadLimitFallbackDelay = freeUploadLimitFallbackDelay
	defaultUploadLimitFeature       = "upload"
	defaultDelete403Consecutive     = 2
	defaultDeleteTimeoutConsecutive = 2
)

func delete403ConsecutiveThreshold(cfg Config) int {
	if cfg.Delete403Consecutive <= 0 {
		return defaultDelete403Consecutive
	}
	return cfg.Delete403Consecutive
}

func deleteTimeoutConsecutiveThreshold(cfg Config) int {
	if cfg.DeleteTimeoutConsecutive <= 0 {
		return defaultDeleteTimeoutConsecutive
	}
	return cfg.DeleteTimeoutConsecutive
}

func isRateLimitErrorText(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "status=429") ||
		strings.Contains(text, "http 429") ||
		strings.Contains(text, "too many requests") ||
		strings.Contains(text, "rate_limit_exceeded") ||
		strings.Contains(text, "usage_limit_reached") ||
		strings.Contains(text, "free plan limit") ||
		strings.Contains(text, "limit for image generation") ||
		strings.Contains(text, "image generations requests") ||
		strings.Contains(text, "when the limit resets")
}

func isInvalidTokenErrorText(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "status=401") ||
		strings.Contains(text, "http 401") ||
		strings.Contains(text, "token_invalidated") ||
		strings.Contains(text, "token_revoked") ||
		strings.Contains(text, "invalid_grant") ||
		strings.Contains(text, "refresh_token not found") ||
		(strings.Contains(text, "refresh token") && (strings.Contains(text, "invalid") || strings.Contains(text, "expired") || strings.Contains(text, "revoked"))) ||
		strings.Contains(text, "invalidated oauth token") ||
		strings.Contains(text, "authentication token has been invalidated")
}

func isUpstreamBlockErrorText(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	if strings.Contains(text, "unusual activity has been detected") ||
		strings.Contains(text, "try again later") ||
		strings.Contains(text, "device blocked") ||
		strings.Contains(text, "suspicious activity") {
		return true
	}
	if !(strings.Contains(text, "status=403") || strings.Contains(text, "http 403")) {
		return false
	}
	return strings.Contains(text, "<html") ||
		strings.Contains(text, "<body") ||
		strings.Contains(text, "meta http-equiv") ||
		strings.Contains(text, "something seems to have gone wrong") ||
		strings.Contains(text, "cloudflare") ||
		strings.Contains(text, "just a moment") ||
		strings.Contains(text, "attention required") ||
		strings.Contains(text, "cf-chl") ||
		strings.Contains(text, "__cf_chl") ||
		strings.Contains(text, "cf-browser-verification") ||
		strings.Contains(text, "turnstile") ||
		strings.Contains(text, "captcha") ||
		strings.Contains(text, "blocked")
}

func isTurnstileRequirementErrorText(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "turnstile") && strings.Contains(text, "required")
}

func isTemporaryUpstreamErrorText(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	if strings.Contains(text, "timed out") || strings.Contains(text, "timeout") || strings.Contains(text, "deadline exceeded") {
		return true
	}
	if strings.Contains(text, "stream ended before image result") ||
		strings.Contains(text, "stream failed") ||
		strings.Contains(text, "unexpected eof") {
		return true
	}
	for _, marker := range []string{"status=500", "status=502", "status=503", "status=504", "http 500", "http 502", "http 503", "http 504"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func accountFailureCategory(err error) string {
	switch {
	case err == nil:
		return ""
	case isInvalidTokenErrorText(err):
		return "invalid_token"
	case isRateLimitErrorText(err):
		return "rate_limited"
	case isUploadLimitErrorText(err):
		return "upload_limited"
	case isUpstreamBlockErrorText(err) || isTurnstileRequirementErrorText(err):
		return "upstream_403"
	case isTemporaryUpstreamErrorText(err):
		return "temporary_timeout"
	default:
		return ""
	}
}

func rateLimitRestoreDelay(err error) time.Duration {
	if delay, ok := parseRateLimitRestoreDelay(err); ok {
		return delay
	}
	return defaultRateLimitRestoreDelay
}

func parseRateLimitRestoreDelay(err error) (time.Duration, bool) {
	if err == nil {
		return 0, false
	}
	text := strings.ToLower(err.Error())
	hours := 0
	minutes := 0
	if m := regexp.MustCompile(`(\d+)\s*hours?`).FindStringSubmatch(text); len(m) > 1 {
		hours, _ = strconv.Atoi(m[1])
	}
	if m := regexp.MustCompile(`(\d+)\s*minutes?`).FindStringSubmatch(text); len(m) > 1 {
		minutes, _ = strconv.Atoi(m[1])
	}
	if hours > 0 || minutes > 0 {
		return time.Duration(hours)*time.Hour + time.Duration(minutes)*time.Minute, true
	}
	return 0, false
}

func uploadLimitRestoreDelay(err error, accountType string) time.Duration {
	if delay, ok := parseRateLimitRestoreDelay(err); ok {
		return delay
	}
	return uploadLimitFallbackDelay(accountType)
}

func uploadLimitFallbackDelay(accountType string) time.Duration {
	switch strings.ToLower(strings.TrimSpace(normalizeAccountType(accountType))) {
	case "plus", "pro", "team", "enterprise", "prolite", "premium":
		return paidUploadLimitFallbackDelay
	case "free":
		return freeUploadLimitFallbackDelay
	default:
		return unknownUploadLimitFallbackDelay
	}
}

func defaultUploadLimitResetAt(account Account, err error) string {
	return time.Now().UTC().Add(uploadLimitRestoreDelay(err, account.Type)).Format(time.RFC3339)
}

func setDefaultUploadLimitMetadata(account *Account, err error) {
	if account.UploadLimitResetAt == nil {
		reset := defaultUploadLimitResetAt(*account, err)
		account.UploadLimitResetAt = &reset
	}
	if account.UploadLimitFeatureName == nil {
		featureName := defaultUploadLimitFeature
		account.UploadLimitFeatureName = &featureName
	}
}

func (s *Server) markAccountSuccess(token string, image bool) {
	if token == "" {
		return
	}
	now := nowISO()
	removeReason := ""
	if err := s.store.UpdateAccounts(func(accounts []Account) []Account {
		for i := range accounts {
			if accounts[i].AccessToken != token {
				continue
			}
			accounts[i].Success++
			accounts[i].LastUsedAt = &now
			accounts[i].LastError = nil
			accounts[i].LastErrorAt = nil
			accounts[i].Consecutive403 = 0
			accounts[i].ConsecutiveTimeout = 0
			if image && !accounts[i].ImageQuotaUnknown && accounts[i].Quota > 0 {
				accounts[i].Quota--
				if accounts[i].Quota <= 0 {
					accounts[i].Quota = 0
					accounts[i].Status = accountStatusLimited
					if accountHasUploadQuotaRuntimeValue(accounts[i]) && !s.cfg.AutoDeleteQuotaZeroAccounts {
						return accounts
					}
					removeReason = "image_quota_empty"
				}
			}
			if isAccountStatus(accounts[i].Status, accountStatusLimited) && !accounts[i].ImageQuotaUnknown && accounts[i].Quota > 0 {
				accounts[i].Status = accountStatusNormal
				accounts[i].ImageLimitResetAt = nil
				accounts[i].RestoreAt = nil
				accounts[i].RateLimitedAt = nil
				accounts[i].RateLimitResetAt = nil
			}
			return accounts
		}
		return accounts
	}); err != nil {
		log.Printf("[account-state] failed to save account success state: %v", err)
		return
	}
	if image && removeReason != "" {
		if _, err := s.removeOrMarkImageAccount(token, removeReason); err != nil {
			log.Printf("[account-state] failed to remove exhausted image account: %v", err)
		}
		s.triggerAutoRefillIfNeeded("image_quota_empty")
	}
}

func (s *Server) markAccountUploadSuccess(token string, n int) {
	if token == "" || n <= 0 {
		return
	}
	now := nowISO()
	if err := s.store.UpdateAccounts(func(accounts []Account) []Account {
		for i := range accounts {
			if accounts[i].AccessToken != token {
				continue
			}
			accounts[i].LastUsedAt = &now
			if !accounts[i].UploadQuotaUnknown && accounts[i].UploadQuota > 0 {
				accounts[i].UploadQuota = maxInt(0, accounts[i].UploadQuota-n)
				if accounts[i].UploadQuota == 0 {
					accounts[i].UploadQuotaUnknown = false
					setDefaultUploadLimitMetadata(&accounts[i], nil)
				}
			}
			return accounts
		}
		return accounts
	}); err != nil {
		log.Printf("[account-state] failed to save account upload quota state: %v", err)
	}
}

func (s *Server) markAccountFailure(token string, err error, image bool) {
	if token == "" {
		return
	}
	now := nowISO()
	removeReason := ""
	category := accountFailureCategory(err)
	if image {
		if reason, ok := imageAccountErrorRemovalReason(err); ok {
			removeReason = reason
		}
	}
	if err := s.store.UpdateAccounts(func(accounts []Account) []Account {
		for i := range accounts {
			if accounts[i].AccessToken != token {
				continue
			}
			accounts[i].Fail++
			accounts[i].LastUsedAt = &now
			if err != nil {
				msg := truncateText(err.Error(), 500)
				accounts[i].LastError = &msg
				accounts[i].LastErrorAt = &now
			}
			switch category {
			case "upstream_403":
				accounts[i].Consecutive403++
				accounts[i].ConsecutiveTimeout = 0
			case "temporary_timeout":
				accounts[i].ConsecutiveTimeout++
				accounts[i].Consecutive403 = 0
			default:
				if category != "" {
					accounts[i].Consecutive403 = 0
					accounts[i].ConsecutiveTimeout = 0
				}
			}
			if removeReason != "" {
				if removeReason == "invalid_token" && !s.cfg.AutoRemoveInvalidAccounts {
					removeReason = ""
				}
			}
			if removeReason != "" {
				accounts[i].Status = accountStatusInvalid
				accounts[i].Quota = 0
				return accounts
			}
			if isUploadLimitErrorText(err) {
				accounts[i].UploadQuota = 0
				accounts[i].UploadQuotaUnknown = false
				reset := defaultUploadLimitResetAt(accounts[i], err)
				accounts[i].UploadLimitResetAt = &reset
				featureName := defaultUploadLimitFeature
				accounts[i].UploadLimitFeatureName = &featureName
				if image && s.cfg.AutoDeleteUploadQuotaZeroAccounts {
					removeReason = "upload_quota_empty"
					accounts[i].Status = accountStatusInvalid
					accounts[i].Quota = 0
				}
				return accounts
			}
			if isRateLimitErrorText(err) {
				accounts[i].Status = accountStatusLimited
				reset := time.Now().UTC().Add(rateLimitRestoreDelay(err)).Format(time.RFC3339)
				accounts[i].ImageLimitResetAt = nil
				accounts[i].RestoreAt = &reset
				accounts[i].RateLimitResetAt = &reset
				accounts[i].RateLimitedAt = &now
				if s.cfg.AutoRemoveRateLimitedAccounts {
					removeReason = "image_account_rate_limited"
				}
			} else if isInvalidTokenErrorText(err) {
				accounts[i].Status = accountStatusInvalid
				accounts[i].Quota = 0
				if s.cfg.AutoRemoveInvalidAccounts {
					removeReason = "invalid_token"
				}
			} else if image && category == "upstream_403" && accounts[i].Consecutive403 >= delete403ConsecutiveThreshold(s.cfg) {
				accounts[i].Status = accountStatusInvalid
				accounts[i].Quota = 0
				removeReason = "upstream_403"
			} else if image && category == "temporary_timeout" && accounts[i].ConsecutiveTimeout >= deleteTimeoutConsecutiveThreshold(s.cfg) {
				accounts[i].Status = accountStatusInvalid
				accounts[i].Quota = 0
				removeReason = "temporary_timeout"
			}
			return accounts
		}
		return accounts
	}); err != nil {
		log.Printf("[account-state] failed to save account failure state: %v", err)
		return
	}
	if removeReason != "" {
		if _, err := s.removeOrMarkImageAccount(token, removeReason); err != nil {
			log.Printf("[account-state] failed to remove failed image account: %v", err)
		}
		s.triggerAutoRefillIfNeeded(removeReason)
	}
}
