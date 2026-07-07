package app

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"
)

func (s *Server) handleAccounts(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, 200, map[string]any{"items": s.store.LoadAccounts()})
	case http.MethodPost:
		var body struct {
			Tokens         []string         `json:"tokens"`
			AccountRecords []map[string]any `json:"account_records"`
			SourceType     string           `json:"source_type"`
		}
		if !readBody(w, r, &body) {
			return
		}
		source := strings.ToLower(strings.TrimSpace(body.SourceType))
		if source == "" {
			source = "web"
		}
		if source != "web" && source != "codex" {
			writeErr(w, 400, "source_type must be web or codex")
			return
		}
		tokset := map[string]map[string]any{}
		for _, t := range body.Tokens {
			t = strings.TrimSpace(t)
			if t != "" {
				tokset[t] = map[string]any{"access_token": t}
			}
		}
		for _, rec := range body.AccountRecords {
			t := strings.TrimSpace(strAny(rec["access_token"], strAny(rec["accessToken"], "")))
			if t != "" {
				tokset[t] = rec
			}
		}
		if len(tokset) == 0 {
			writeErr(w, 400, "tokens or account_records is required")
			return
		}
		if msg := validateAccountRecordsForSource(tokset, source, s.store.LoadAccounts()); msg != "" {
			writeErr(w, 400, msg)
			return
		}
		added, skipped, err := s.upsertAccountRecordsForRefresh(tokset, source)
		if err != nil {
			writeErr(w, 500, "failed to save accounts: "+err.Error())
			return
		}
		tokens := keysOf(tokset)
		refreshed, errs, removedUnusable, cleanupRemoved := s.refreshAccountInfos(r.Context(), tokens, false)
		accounts := s.store.LoadAccounts()
		writeAttempted := added + skipped
		saved := countAccountsForTokens(accounts, tokens)
		validatedSaved := countValidatedAccountsForTokens(accounts, tokens)
		refreshFailed := len(errs)
		status := "ok"
		if refreshFailed > 0 || validatedSaved < writeAttempted {
			status = "partial"
		}
		s.logSvc.add("account", "新增账号", map[string]any{"added": added, "skipped": skipped, "write_attempted": writeAttempted, "saved": saved, "validated_saved": validatedSaved, "refreshed": refreshed, "refresh_failed": refreshFailed, "removed_unusable": removedUnusable, "cleanup_removed": cleanupRemoved, "emails": accountEmailsForTokens(accounts, tokens)})
		writeJSON(w, 200, map[string]any{"status": status, "added": added, "skipped": skipped, "write_attempted": writeAttempted, "saved": saved, "validated_saved": validatedSaved, "refreshed": refreshed, "refresh_failed": refreshFailed, "removed_unusable": removedUnusable, "cleanup_removed": cleanupRemoved, "errors": errs, "items": accounts})
	case http.MethodDelete:
		var body struct {
			Tokens          []string `json:"tokens"`
			DeleteMailboxes bool     `json:"delete_mailboxes"`
		}
		if !readBody(w, r, &body) {
			return
		}
		targets := map[string]bool{}
		for _, t := range body.Tokens {
			if strings.TrimSpace(t) != "" {
				targets[strings.TrimSpace(t)] = true
			}
		}
		before := s.store.LoadAccounts()
		removed := 0
		out := []Account{}
		if err := s.store.UpdateAccounts(func(accounts []Account) []Account {
			out = []Account{}
			for _, a := range accounts {
				if targets[a.AccessToken] {
					removed++
				} else {
					out = append(out, a)
				}
			}
			return out
		}); err != nil {
			writeErr(w, 500, "failed to save accounts: "+err.Error())
			return
		}
		if removed > 0 && s.logSvc != nil {
			s.logSvc.add("account", "删除账号", map[string]any{"removed": removed, "emails": accountEmailsForTokenSet(before, targets)})
		}
		writeJSON(w, 200, map[string]any{"removed": removed, "mailboxes_removed": 0, "mailbox_errors": []any{}, "items": out})
	default:
		writeErr(w, 405, "method not allowed")
	}
}

func accountRecordSource(defaultSource string, rec map[string]any) string {
	source := strings.ToLower(strings.TrimSpace(strAny(rec["source_type"], strAny(rec["sourceType"], defaultSource))))
	if source != "web" && source != "codex" {
		return "web"
	}
	return source
}

func accountRecordRefreshToken(rec map[string]any) string {
	return strings.TrimSpace(strAny(rec["refresh_token"], strAny(rec["refreshToken"], "")))
}

func validateAccountRecordsForSource(records map[string]map[string]any, defaultSource string, existing []Account) string {
	existingRefresh := map[string]bool{}
	for _, account := range existing {
		if account.RefreshToken != nil && strings.TrimSpace(*account.RefreshToken) != "" {
			existingRefresh[account.AccessToken] = true
		}
	}
	for token, rec := range records {
		if accountRecordSource(defaultSource, rec) != "codex" {
			continue
		}
		if accountRecordRefreshToken(rec) == "" && !existingRefresh[token] {
			return "codex account requires refresh_token"
		}
	}
	return ""
}

func accountFromRecord(token, source string, rec map[string]any) Account {
	typ := strings.TrimSpace(strAny(rec["type"], strAny(rec["plan_type"], "free")))
	if typ == "" {
		typ = "free"
	}
	status := strings.TrimSpace(strAny(rec["status"], accountStatusNormal))
	if status == "" {
		status = accountStatusNormal
	}
	now := nowISO()
	a := Account{AccessToken: token, Type: typ, SourceType: source, Status: status, Quota: intAny(rec["quota"], 0), Success: intAny(rec["success"], 0), Fail: intAny(rec["fail"], 0), CreatedAt: &now, ImageQuotaUnknown: boolAny(rec["image_quota_unknown"], false), UploadQuotaUnknown: true, RefreshValidationPending: boolAny(rec["refresh_validation_pending"], false)}
	a.PendingDelete = boolAny(rec["pending_delete"], false)
	if v := strings.TrimSpace(strAny(rec["delete_reason"], "")); v != "" {
		a.DeleteReason = &v
	}
	if v := strings.TrimSpace(strAny(rec["delete_marked_at"], "")); v != "" {
		a.DeleteMarkedAt = &v
	}
	if v := strings.TrimSpace(strAny(rec["email"], "")); v != "" {
		a.Email = &v
	}
	if v := strings.TrimSpace(strAny(rec["user_id"], "")); v != "" {
		a.UserID = &v
	}
	if v := accountRecordRefreshToken(rec); v != "" {
		a.RefreshToken = &v
	}
	if v := strings.TrimSpace(strAny(rec["id_token"], strAny(rec["idToken"], ""))); v != "" {
		a.IDToken = &v
	}
	if v := strings.TrimSpace(strAny(rec["password"], "")); v != "" {
		a.Password = &v
	}
	if v := strings.TrimSpace(strAny(rec["account_id"], strAny(rec["chatgpt_account_id"], ""))); v != "" {
		a.AccountID = &v
	}
	if v := strings.TrimSpace(strAny(rec["client_id"], strAny(rec["clientId"], ""))); v != "" {
		a.ClientID = &v
	}
	if v := strings.TrimSpace(strAny(rec["default_model_slug"], "")); v != "" {
		a.DefaultModelSlug = &v
	}
	if v := strings.TrimSpace(strAny(rec["image_limit_reset_at"], "")); v != "" {
		a.ImageLimitResetAt = &v
	}
	if v := strings.TrimSpace(strAny(rec["restore_at"], "")); v != "" {
		a.RestoreAt = &v
	}
	if v := strings.TrimSpace(strAny(rec["rate_limited_at"], "")); v != "" {
		a.RateLimitedAt = &v
	}
	if v := strings.TrimSpace(strAny(rec["rate_limit_reset_at"], "")); v != "" {
		a.RateLimitResetAt = &v
	}
	if _, ok := rec["upload_quota"]; ok {
		a.UploadQuota = intAny(rec["upload_quota"], 0)
		a.UploadQuotaUnknown = false
	}
	if _, ok := rec["upload_quota_unknown"]; ok {
		a.UploadQuotaUnknown = boolAny(rec["upload_quota_unknown"], a.UploadQuotaUnknown)
	}
	if v := strings.TrimSpace(strAny(rec["upload_limit_reset_at"], "")); v != "" {
		a.UploadLimitResetAt = &v
	}
	if v := strings.TrimSpace(strAny(rec["upload_limit_feature_name"], "")); v != "" {
		a.UploadLimitFeatureName = &v
	}
	if arr, ok := rec["limits_progress"].([]any); ok {
		for _, item := range arr {
			if m, ok := item.(map[string]any); ok {
				a.LimitsProgress = append(a.LimitsProgress, m)
			}
		}
	}
	if a.UploadQuotaUnknown && len(a.LimitsProgress) > 0 {
		applyUploadQuotaInfo(&a, uploadQuotaFromLimits(a.LimitsProgress))
	}
	normalizeAccountLimitState(&a)
	normalizeAccountUploadQuotaState(&a)
	a.FP = accountFPFromRecord(rec)
	if a.InitialQuota < a.Quota {
		a.InitialQuota = a.Quota
	}
	return a
}
func accountFPFromRecord(rec map[string]any) map[string]string {
	fp := map[string]string{}
	if raw, ok := rec["fp"].(map[string]any); ok {
		for k, v := range raw {
			if s := strings.TrimSpace(strAny(v, "")); s != "" {
				fp[strings.ToLower(strings.TrimSpace(k))] = s
			}
		}
	}
	for _, key := range []string{"user-agent", "impersonate", "oai-device-id", "oai-session-id", "sec-ch-ua", "sec-ch-ua-mobile", "sec-ch-ua-platform"} {
		if s := strings.TrimSpace(strAny(rec[key], "")); s != "" {
			fp[key] = s
		}
	}
	if len(fp) == 0 {
		return nil
	}
	return fp
}

func keysOf(m map[string]map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func accountEmailsForTokens(accounts []Account, tokens []string) []string {
	targets := map[string]bool{}
	for _, token := range tokens {
		if token = strings.TrimSpace(token); token != "" {
			targets[token] = true
		}
	}
	return accountEmailsForTokenSet(accounts, targets)
}

func accountEmailsForTokenSet(accounts []Account, targets map[string]bool) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, account := range accounts {
		if len(targets) > 0 && !targets[account.AccessToken] {
			continue
		}
		if account.Email == nil {
			continue
		}
		email := strings.TrimSpace(*account.Email)
		if email == "" || seen[strings.ToLower(email)] {
			continue
		}
		seen[strings.ToLower(email)] = true
		out = append(out, email)
	}
	sort.Strings(out)
	return out
}

func countAccountsForTokens(accounts []Account, tokens []string) int {
	targets := map[string]bool{}
	for _, token := range tokens {
		if token = strings.TrimSpace(token); token != "" {
			targets[token] = true
		}
	}
	if len(targets) == 0 {
		return 0
	}
	count := 0
	for _, account := range accounts {
		if targets[account.AccessToken] {
			count++
		}
	}
	return count
}

func countValidatedAccountsForTokens(accounts []Account, tokens []string) int {
	targets := map[string]bool{}
	for _, token := range tokens {
		if token = strings.TrimSpace(token); token != "" {
			targets[token] = true
		}
	}
	if len(targets) == 0 {
		return 0
	}
	count := 0
	for _, account := range accounts {
		if !targets[account.AccessToken] {
			continue
		}
		if !isAccountStatus(account.Status, accountStatusNormal) || account.PendingDelete || account.ImageQuotaUnknown || account.Quota <= 0 {
			continue
		}
		count++
	}
	return count
}

func markAccountPendingRefreshValidation(account *Account) {
	account.ImageQuotaUnknown = true
	account.UploadQuotaUnknown = true
	account.RefreshValidationPending = true
	account.Quota = 0
	account.UploadQuota = 0
	account.UploadLimitResetAt = nil
	account.UploadLimitFeatureName = nil
	account.LimitsProgress = nil
	if account.CreatedAt == nil || strings.TrimSpace(*account.CreatedAt) == "" {
		now := nowISO()
		account.CreatedAt = &now
	}
}

func preserveValidatedAccountRuntimeState(dst *Account, previous Account) {
	preserveAccountUploadQuotaRuntimeState(dst, previous)
	if previous.ImageQuotaUnknown || previous.Quota <= 0 {
		return
	}
	dst.Status = previous.Status
	dst.Quota = previous.Quota
	dst.InitialQuota = previous.InitialQuota
	dst.ImageQuotaUnknown = previous.ImageQuotaUnknown
	dst.RefreshValidationPending = previous.RefreshValidationPending
	dst.LimitsProgress = previous.LimitsProgress
	dst.ImageLimitResetAt = previous.ImageLimitResetAt
	dst.RestoreAt = previous.RestoreAt
	dst.RateLimitedAt = previous.RateLimitedAt
	dst.RateLimitResetAt = previous.RateLimitResetAt
	dst.PendingDelete = previous.PendingDelete
	dst.DeleteReason = previous.DeleteReason
	dst.DeleteMarkedAt = previous.DeleteMarkedAt
}

func preserveAccountUploadQuotaRuntimeState(dst *Account, previous Account) {
	if !hasUploadQuotaRuntimeState(previous) {
		return
	}
	dst.UploadQuota = previous.UploadQuota
	dst.UploadQuotaUnknown = previous.UploadQuotaUnknown
	dst.UploadLimitResetAt = previous.UploadLimitResetAt
	dst.UploadLimitFeatureName = previous.UploadLimitFeatureName
}

func hasUploadQuotaRuntimeState(account Account) bool {
	if !account.UploadQuotaUnknown || account.UploadQuota > 0 || account.UploadLimitResetAt != nil || account.UploadLimitFeatureName != nil {
		return true
	}
	for _, item := range account.LimitsProgress {
		if isUploadLimitFeatureName(strAny(item["feature_name"], "")) {
			return true
		}
	}
	return false
}

func (s *Server) upsertAccountRecordsForRefresh(tokset map[string]map[string]any, defaultSource string) (int, int, error) {
	added, skipped := 0, 0
	err := s.store.UpdateAccounts(func(accounts []Account) []Account {
		existing := map[string]int{}
		for i, a := range accounts {
			existing[a.AccessToken] = i
		}
		for token, rec := range tokset {
			a := accountFromRecord(token, accountRecordSource(defaultSource, rec), rec)
			markAccountPendingRefreshValidation(&a)
			if idx, ok := existing[token]; ok {
				cur := accounts[idx]
				previous := cur
				mergeAccount(&cur, a)
				preserveValidatedAccountRuntimeState(&cur, previous)
				accounts[idx] = cur
				skipped++
			} else {
				accounts = append(accounts, a)
				added++
			}
		}
		return accounts
	})
	return added, skipped, err
}

func mergeAccount(dst *Account, src Account) {
	if src.Type != "" {
		dst.Type = src.Type
	}
	if src.SourceType != "" {
		dst.SourceType = src.SourceType
	}
	if src.Status != "" {
		dst.Status = src.Status
	}
	if src.PendingDelete {
		dst.PendingDelete = true
	}
	if src.RefreshValidationPending {
		dst.RefreshValidationPending = true
	}
	if src.DeleteReason != nil {
		dst.DeleteReason = src.DeleteReason
	}
	if src.DeleteMarkedAt != nil {
		dst.DeleteMarkedAt = src.DeleteMarkedAt
	}
	if !src.ImageQuotaUnknown || src.Quota != 0 || len(src.LimitsProgress) > 0 {
		dst.Quota = src.Quota
	}
	if src.Email != nil {
		dst.Email = src.Email
	}
	if src.UserID != nil {
		dst.UserID = src.UserID
	}
	if src.RefreshToken != nil {
		dst.RefreshToken = src.RefreshToken
	}
	if src.IDToken != nil {
		dst.IDToken = src.IDToken
	}
	if src.AccountID != nil {
		dst.AccountID = src.AccountID
	}
	if src.ClientID != nil {
		dst.ClientID = src.ClientID
	}
	if src.DefaultModelSlug != nil {
		dst.DefaultModelSlug = src.DefaultModelSlug
	}
	if src.ImageLimitResetAt != nil {
		dst.ImageLimitResetAt = src.ImageLimitResetAt
	}
	if src.RestoreAt != nil {
		dst.RestoreAt = src.RestoreAt
	}
	if src.RateLimitedAt != nil {
		dst.RateLimitedAt = src.RateLimitedAt
	}
	if src.RateLimitResetAt != nil {
		dst.RateLimitResetAt = src.RateLimitResetAt
	}
	if len(src.LimitsProgress) > 0 {
		dst.LimitsProgress = src.LimitsProgress
		dst.ImageQuotaUnknown = src.ImageQuotaUnknown
	}
	if !src.UploadQuotaUnknown || src.UploadQuota > 0 || src.UploadLimitResetAt != nil || src.UploadLimitFeatureName != nil {
		dst.UploadQuota = src.UploadQuota
		dst.UploadQuotaUnknown = src.UploadQuotaUnknown
		dst.UploadLimitResetAt = src.UploadLimitResetAt
		dst.UploadLimitFeatureName = src.UploadLimitFeatureName
	}
	if src.Quota > 0 || src.ImageLimitResetAt != nil || src.RestoreAt != nil || src.DefaultModelSlug != nil {
		dst.ImageQuotaUnknown = src.ImageQuotaUnknown
	}
	if len(src.FP) > 0 {
		if dst.FP == nil {
			dst.FP = map[string]string{}
		}
		for k, v := range src.FP {
			if strings.TrimSpace(v) != "" {
				dst.FP[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
			}
		}
	}
	if dst.InitialQuota < dst.Quota {
		dst.InitialQuota = dst.Quota
	}
	if src.Password != nil {
		dst.Password = src.Password
	}
}

func mergeRefreshedAccountInfo(dst *Account, src Account) {
	accessToken := dst.AccessToken
	sourceType := dst.SourceType
	status := dst.Status
	quota := dst.Quota
	initialQuota := dst.InitialQuota
	imageQuotaUnknown := dst.ImageQuotaUnknown
	limitsProgress := dst.LimitsProgress
	imageLimitResetAt := dst.ImageLimitResetAt
	restoreAt := dst.RestoreAt
	rateLimitedAt := dst.RateLimitedAt
	rateLimitResetAt := dst.RateLimitResetAt
	uploadQuota := dst.UploadQuota
	uploadQuotaUnknown := dst.UploadQuotaUnknown
	uploadLimitResetAt := dst.UploadLimitResetAt
	uploadLimitFeatureName := dst.UploadLimitFeatureName
	mergeAccount(dst, src)
	dst.AccessToken = accessToken
	if strings.TrimSpace(sourceType) != "" {
		dst.SourceType = sourceType
	}
	dst.Status = status
	dst.Quota = quota
	dst.InitialQuota = initialQuota
	dst.ImageQuotaUnknown = imageQuotaUnknown
	dst.LimitsProgress = limitsProgress
	dst.ImageLimitResetAt = imageLimitResetAt
	dst.RestoreAt = restoreAt
	dst.RateLimitedAt = rateLimitedAt
	dst.RateLimitResetAt = rateLimitResetAt
	dst.UploadQuota = uploadQuota
	dst.UploadQuotaUnknown = uploadQuotaUnknown
	dst.UploadLimitResetAt = uploadLimitResetAt
	dst.UploadLimitFeatureName = uploadLimitFeatureName
	dst.RefreshValidationPending = false
	if refreshedHasImageQuotaSignal(src) {
		dst.Status = src.Status
		dst.ImageQuotaUnknown = src.ImageQuotaUnknown
		dst.Quota = src.Quota
		dst.LimitsProgress = src.LimitsProgress
		dst.ImageLimitResetAt = src.ImageLimitResetAt
		dst.RestoreAt = src.RestoreAt
		dst.RateLimitedAt = src.RateLimitedAt
		dst.RateLimitResetAt = src.RateLimitResetAt
		if dst.InitialQuota < dst.Quota {
			dst.InitialQuota = dst.Quota
		}
	}
	if refreshedHasUploadQuotaSignal(src) {
		dst.UploadQuota = src.UploadQuota
		dst.UploadQuotaUnknown = src.UploadQuotaUnknown
		dst.UploadLimitResetAt = src.UploadLimitResetAt
		dst.UploadLimitFeatureName = src.UploadLimitFeatureName
	}
}

func refreshedHasImageQuotaSignal(account Account) bool {
	if account.Quota > 0 || account.ImageLimitResetAt != nil || account.RestoreAt != nil || account.RateLimitResetAt != nil {
		return true
	}
	for _, item := range account.LimitsProgress {
		if strings.TrimSpace(strAny(item["feature_name"], "")) == "image_gen" {
			return true
		}
	}
	return false
}

func refreshedHasUploadQuotaSignal(account Account) bool {
	if account.UploadQuota > 0 || account.UploadLimitResetAt != nil || account.UploadLimitFeatureName != nil {
		return true
	}
	for _, item := range account.LimitsProgress {
		if isUploadLimitFeatureName(strAny(item["feature_name"], "")) {
			return true
		}
	}
	return false
}

func (s *Server) refreshAccountInfos(parent context.Context, tokens []string, deferInvalidRemoval ...bool) (int, []map[string]any, int, int) {
	want := map[string]bool{}
	for _, t := range tokens {
		if strings.TrimSpace(t) != "" {
			want[strings.TrimSpace(t)] = true
		}
	}
	accounts := s.store.LoadAccounts()
	refreshed := 0
	errs := []map[string]any{}
	updates := map[string]Account{}
	for _, a := range accounts {
		if len(want) > 0 && !want[a.AccessToken] {
			continue
		}
		ctx, cancel := context.WithTimeout(parent, 45*time.Second)
		account := a
		var err error
		if strings.EqualFold(account.SourceType, "codex") && account.RefreshToken != nil && strings.TrimSpace(*account.RefreshToken) != "" {
			account, err = s.refreshOAuthAccount(ctx, account.AccessToken)
		}
		var client *UpstreamClient
		if err == nil {
			client, err = NewUpstreamClientForAccount(account, s.cfg.Proxy, s.ensureCurlImpersonateBinary)
		}
		if err == nil {
			var info Account
			info, err = client.GetUserInfo(ctx)
			if err == nil {
				mergeRefreshedAccountInfo(&account, info)
				updates[account.AccessToken] = account
			}
		}
		cancel()
		if err != nil {
			errs = append(errs, map[string]any{"token": a.AccessToken, "error": err.Error()})
		}
	}
	if len(updates) > 0 {
		if err := s.store.UpdateAccounts(func(accounts []Account) []Account {
			for i, a := range accounts {
				if next, ok := updates[a.AccessToken]; ok {
					accounts[i] = next
				}
			}
			return accounts
		}); err != nil {
			errs = append(errs, map[string]any{"error": "failed to save refreshed accounts: " + err.Error()})
		} else {
			refreshed = len(updates)
			if s.logSvc != nil {
				s.logSvc.add("account", "刷新上传额度", map[string]any{"items": uploadQuotaRefreshLogItems(updates)})
			}
		}
	}
	deferRemoval := len(deferInvalidRemoval) > 0 && deferInvalidRemoval[0]
	removed, cleanupRemoved := s.cleanupRefreshedAccountState(tokens, errs, deferRemoval)
	return refreshed, errs, removed, cleanupRemoved
}

func (s *Server) cleanupRefreshedAccountState(tokens []string, errs []map[string]any, deferRemoval bool) (int, int) {
	if deferRemoval {
		return 0, 0
	}
	removed := s.removeRegisterUnusableAccounts(tokens, errs)
	cleanupRemoved := s.cleanupUnusableImageAccounts()
	return removed, cleanupRemoved
}

func (s *Server) handleAccountsRefresh(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method not allowed")
		return
	}
	var body struct {
		AccessTokens []string `json:"access_tokens"`
	}
	if !readBody(w, r, &body) {
		return
	}
	want := map[string]bool{}
	for _, t := range body.AccessTokens {
		if strings.TrimSpace(t) != "" {
			want[strings.TrimSpace(t)] = true
		}
	}
	var tokens []string
	if len(want) > 0 {
		for token := range want {
			tokens = append(tokens, token)
		}
	}
	before := s.store.LoadAccounts()
	refreshed, errs, removedUnusable, cleanupRemoved := s.refreshAccountInfos(r.Context(), tokens)
	accounts := s.store.LoadAccounts()
	if s.logSvc != nil {
		s.logSvc.add("account", "刷新账号", map[string]any{"refreshed": refreshed, "errors": len(errs), "removed_unusable": removedUnusable, "cleanup_removed": cleanupRemoved, "emails": accountEmailsForRefreshLog(before, accounts, tokens)})
	}
	writeJSON(w, 200, map[string]any{"refreshed": refreshed, "errors": errs, "removed_unusable": removedUnusable, "cleanup_removed": cleanupRemoved, "items": accounts})
}

func accountEmailsForRefreshLog(before, after []Account, tokens []string) []string {
	emails := accountEmailsForTokens(after, tokens)
	if len(emails) > 0 {
		return emails
	}
	return accountEmailsForTokens(before, tokens)
}

func uploadQuotaRefreshLogItems(updates map[string]Account) []map[string]any {
	tokens := make([]string, 0, len(updates))
	for token := range updates {
		tokens = append(tokens, token)
	}
	sort.Strings(tokens)
	out := make([]map[string]any, 0, len(tokens))
	for _, token := range tokens {
		account := updates[token]
		item := map[string]any{
			"email":                ptrString(account.Email),
			"upload_quota":         account.UploadQuota,
			"upload_quota_unknown": account.UploadQuotaUnknown,
			"feature_name":         ptrString(account.UploadLimitFeatureName),
			"reset_at":             ptrString(account.UploadLimitResetAt),
		}
		out = append(out, item)
	}
	return out
}

func (s *Server) handleAccountsUpdate(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	var body map[string]any
	if !readBody(w, r, &body) {
		return
	}
	token := strings.TrimSpace(strAny(body["access_token"], ""))
	if token == "" {
		writeErr(w, 400, "access_token is required")
		return
	}
	var item Account
	found := false
	if err := s.store.UpdateAccounts(func(accounts []Account) []Account {
		for i, a := range accounts {
			if a.AccessToken != token {
				continue
			}
			if v, ok := body["type"]; ok {
				a.Type = strAny(v, a.Type)
			}
			if v, ok := body["status"]; ok {
				a.Status = strAny(v, a.Status)
			}
			if v, ok := body["quota"]; ok {
				a.Quota = intAny(v, a.Quota)
			}
			accounts[i] = a
			item = a
			found = true
			break
		}
		return accounts
	}); err != nil {
		writeErr(w, 500, "failed to save accounts: "+err.Error())
		return
	}
	if found {
		writeJSON(w, 200, map[string]any{"item": item, "items": s.store.LoadAccounts()})
		return
	}
	writeErr(w, 404, "account not found")
}
