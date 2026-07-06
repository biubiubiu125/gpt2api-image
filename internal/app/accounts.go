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
		added, skipped := 0, 0
		if err := s.store.UpdateAccounts(func(accounts []Account) []Account {
			existing := map[string]int{}
			for i, a := range accounts {
				existing[a.AccessToken] = i
			}
			for token, rec := range tokset {
				a := accountFromRecord(token, accountRecordSource(source, rec), rec)
				if idx, ok := existing[token]; ok {
					cur := accounts[idx]
					mergeAccount(&cur, a)
					accounts[idx] = cur
					skipped++
				} else {
					accounts = append(accounts, a)
					added++
				}
			}
			return accounts
		}); err != nil {
			writeErr(w, 500, "failed to save accounts: "+err.Error())
			return
		}
		tokens := keysOf(tokset)
		refreshed, errs := s.refreshAccountInfos(r.Context(), tokens)
		accounts := s.store.LoadAccounts()
		s.logSvc.add("account", "新增账号", map[string]any{"added": added, "skipped": skipped, "refreshed": refreshed, "emails": accountEmailsForTokens(accounts, tokens)})
		writeJSON(w, 200, map[string]any{"added": added, "skipped": skipped, "refreshed": refreshed, "errors": errs, "items": accounts})
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
	a := Account{AccessToken: token, Type: typ, SourceType: source, Status: status, Quota: intAny(rec["quota"], 0), Success: intAny(rec["success"], 0), Fail: intAny(rec["fail"], 0), CreatedAt: &now, ImageQuotaUnknown: boolAny(rec["image_quota_unknown"], false)}
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
	if v := strings.TrimSpace(strAny(rec["restore_at"], "")); v != "" {
		a.RestoreAt = &v
	}
	if v := strings.TrimSpace(strAny(rec["rate_limited_at"], "")); v != "" {
		a.RateLimitedAt = &v
	}
	if v := strings.TrimSpace(strAny(rec["rate_limit_reset_at"], "")); v != "" {
		a.RateLimitResetAt = &v
	}
	if arr, ok := rec["limits_progress"].([]any); ok {
		for _, item := range arr {
			if m, ok := item.(map[string]any); ok {
				a.LimitsProgress = append(a.LimitsProgress, m)
			}
		}
	}
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
	if src.Quota > 0 || src.RestoreAt != nil || src.DefaultModelSlug != nil {
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
	mergeAccount(dst, src)
	dst.AccessToken = accessToken
	if strings.TrimSpace(sourceType) != "" {
		dst.SourceType = sourceType
	}
	dst.ImageQuotaUnknown = src.ImageQuotaUnknown
	dst.Quota = src.Quota
	dst.LimitsProgress = src.LimitsProgress
	if dst.InitialQuota < dst.Quota {
		dst.InitialQuota = dst.Quota
	}
}

func (s *Server) refreshAccountInfos(parent context.Context, tokens []string) (int, []map[string]any) {
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
		}
	}
	s.cleanupUnusableImageAccounts()
	return refreshed, errs
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
	refreshed, errs := s.refreshAccountInfos(r.Context(), tokens)
	accounts := s.store.LoadAccounts()
	if s.logSvc != nil {
		s.logSvc.add("account", "刷新账号", map[string]any{"refreshed": refreshed, "errors": len(errs), "emails": accountEmailsForRefreshLog(before, accounts, tokens)})
	}
	writeJSON(w, 200, map[string]any{"refreshed": refreshed, "errors": errs, "items": accounts})
}

func accountEmailsForRefreshLog(before, after []Account, tokens []string) []string {
	emails := accountEmailsForTokens(after, tokens)
	if len(emails) > 0 {
		return emails
	}
	return accountEmailsForTokens(before, tokens)
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
