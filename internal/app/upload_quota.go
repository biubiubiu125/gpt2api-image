package app

import "strings"

type uploadQuotaInfo struct {
	Quota       int
	ResetAt     string
	FeatureName string
	Found       bool
}

func uploadQuotaFromLimits(limits []map[string]any) uploadQuotaInfo {
	info := uploadQuotaInfo{}
	for _, item := range limits {
		featureName := strings.TrimSpace(strAny(item["feature_name"], ""))
		if !isUploadLimitFeatureName(featureName) {
			continue
		}
		remaining := uploadLimitRemaining(item)
		resetAt := uploadLimitResetAt(item)
		if !info.Found || remaining < info.Quota {
			info = uploadQuotaInfo{
				Quota:       maxInt(0, remaining),
				ResetAt:     resetAt,
				FeatureName: featureName,
				Found:       true,
			}
		}
	}
	return info
}

func uploadLimitRemaining(item map[string]any) int {
	for _, key := range []string{"remaining", "remaining_count", "remaining_uses", "available", "available_count"} {
		if _, ok := item[key]; ok {
			return intAny(item[key], 0)
		}
	}
	if limit, ok := item["limit"]; ok {
		used := intAny(item["used"], 0)
		return intAny(limit, 0) - used
	}
	return 0
}

func uploadLimitResetAt(item map[string]any) string {
	for _, key := range []string{"reset_after", "resets_at", "reset_at", "reset_time", "available_at", "next_reset_at"} {
		if v := strings.TrimSpace(strAny(item[key], "")); v != "" {
			return v
		}
	}
	return ""
}

func isUploadLimitFeatureName(featureName string) bool {
	name := strings.ToLower(strings.TrimSpace(featureName))
	if name == "" {
		return false
	}
	if strings.Contains(name, "image_gen") || strings.Contains(name, "image-generation") || strings.Contains(name, "image generation") {
		return false
	}
	if strings.Contains(name, "upload") || strings.Contains(name, "attachment") || strings.Contains(name, "multimodal") {
		return true
	}
	return false
}

func applyUploadQuotaInfo(account *Account, info uploadQuotaInfo) {
	if account == nil {
		return
	}
	if !info.Found {
		account.UploadQuota = 0
		account.UploadQuotaUnknown = true
		account.UploadLimitResetAt = nil
		account.UploadLimitFeatureName = nil
		return
	}
	account.UploadQuota = maxInt(0, info.Quota)
	account.UploadQuotaUnknown = false
	if strings.TrimSpace(info.ResetAt) != "" {
		resetAt := strings.TrimSpace(info.ResetAt)
		account.UploadLimitResetAt = &resetAt
	} else {
		account.UploadLimitResetAt = nil
	}
	if strings.TrimSpace(info.FeatureName) != "" {
		featureName := strings.TrimSpace(info.FeatureName)
		account.UploadLimitFeatureName = &featureName
	} else {
		account.UploadLimitFeatureName = nil
	}
}

func normalizeAccountUploadQuotaState(account *Account) {
	if account == nil {
		return
	}
	if account.UploadQuota < 0 {
		account.UploadQuota = 0
	}
	if !account.UploadQuotaUnknown && account.UploadQuota == 0 && account.UploadLimitResetAt == nil && account.UploadLimitFeatureName == nil {
		account.UploadQuotaUnknown = true
	}
	if account.UploadQuotaUnknown && len(account.LimitsProgress) > 0 {
		applyUploadQuotaInfo(account, uploadQuotaFromLimits(account.LimitsProgress))
	}
	if account.UploadQuota < 0 {
		account.UploadQuota = 0
	}
}

func normalizeAccountLimitState(account *Account) {
	if account == nil {
		return
	}
	if account.ImageLimitResetAt == nil && account.RateLimitedAt == nil && (account.ImageQuotaUnknown || account.Quota <= 0) {
		resetAt := firstNonEmpty(ptrString(account.RateLimitResetAt), ptrString(account.RestoreAt))
		if resetAt != "" {
			v := resetAt
			account.ImageLimitResetAt = &v
			account.RestoreAt = nil
			account.RateLimitResetAt = nil
		}
	}
}

func accountHasEnoughUploadQuota(account Account, requiredUploads int) bool {
	return accountHasEnoughUploadQuotaAfterReservation(account, requiredUploads, 0)
}

func accountHasEnoughUploadQuotaAfterReservation(account Account, requiredUploads, reservedUploads int) bool {
	if requiredUploads <= 0 {
		return true
	}
	if account.UploadQuotaUnknown {
		return false
	}
	return availableUploadQuotaAfterReservation(account, reservedUploads) >= requiredUploads
}

func availableUploadQuotaAfterReservation(account Account, reservedUploads int) int {
	if account.UploadQuotaUnknown {
		return 0
	}
	return maxInt(0, account.UploadQuota-maxInt(0, reservedUploads))
}

func isUploadLimitErrorText(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	backendUploadContext := strings.Contains(text, "post /backend-api/files failed") ||
		(strings.Contains(text, "post /backend-api/files/") && strings.Contains(text, "/uploaded failed"))
	backendDownloadContext := strings.Contains(text, "/backend-api/files/") && strings.Contains(text, "/download")
	explicitUploadLimitContext := strings.Contains(text, "upload limit") ||
		strings.Contains(text, "upload quota") ||
		strings.Contains(text, "file uploads") ||
		strings.Contains(text, "file upload") ||
		strings.Contains(text, "attachment") ||
		strings.Contains(text, "multimodal")
	limitContext := strings.Contains(text, "status=429") ||
		strings.Contains(text, "http 429") ||
		strings.Contains(text, "too many") ||
		strings.Contains(text, "reached") ||
		strings.Contains(text, "exceeded") ||
		strings.Contains(text, "limit") ||
		strings.Contains(text, "quota") ||
		strings.Contains(text, "rate")
	return (backendUploadContext || (!backendDownloadContext && explicitUploadLimitContext)) && limitContext
}
