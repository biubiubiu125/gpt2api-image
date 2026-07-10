package app

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"
	"time"
)

func (s *Server) requireRegisterInternal(w http.ResponseWriter, r *http.Request) bool {
	expected := strings.TrimSpace(s.cfg.RegisterInternalKey)
	headerKey := strings.TrimSpace(r.Header.Get("X-Register-Internal-Key"))
	bearer := s.bearer(r)
	if expected != "" {
		if subtle.ConstantTimeCompare([]byte(headerKey), []byte(expected)) == 1 ||
			subtle.ConstantTimeCompare([]byte(bearer), []byte(expected)) == 1 {
			return true
		}
		writeErr(w, 401, "register internal key is invalid")
		return false
	}
	writeErr(w, 401, "register internal key is required")
	return false
}

func (s *Server) handleInternalRegisterAccounts(w http.ResponseWriter, r *http.Request) {
	if !s.requireRegisterInternal(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, 200, map[string]any{"items": s.store.LoadAccounts()})
	case http.MethodPost:
		var body struct {
			AccountRecords []map[string]any `json:"account_records"`
		}
		if !readBody(w, r, &body) {
			return
		}
		added, skipped, msg, err := s.addAccountRecords(body.AccountRecords)
		if msg != "" {
			writeErr(w, 400, msg)
			return
		}
		if err != nil {
			writeErr(w, 500, "failed to save accounts: "+err.Error())
			return
		}
		if s.logSvc != nil {
			tokens := []string{}
			for _, rec := range body.AccountRecords {
				if token := strings.TrimSpace(strAny(rec["access_token"], strAny(rec["accessToken"], ""))); token != "" {
					tokens = append(tokens, token)
				}
			}
			s.logSvc.add("account", "注册机写入账号", map[string]any{"added": added, "skipped": skipped, "emails": accountEmailsForTokens(s.store.LoadAccounts(), tokens)})
		}
		writeJSON(w, 200, map[string]any{"added": added, "skipped": skipped, "items": s.store.LoadAccounts()})
	default:
		writeErr(w, 405, "method not allowed")
	}
}

func (s *Server) addAccountRecords(records []map[string]any) (int, int, string, error) {
	tokset := map[string]map[string]any{}
	for _, rec := range records {
		t := strings.TrimSpace(strAny(rec["access_token"], strAny(rec["accessToken"], "")))
		if t != "" {
			if _, ok := rec["refresh_validation_pending"]; !ok {
				rec["refresh_validation_pending"] = true
			}
			tokset[t] = rec
		}
	}
	if msg := validateAccountRecordsForSource(tokset, "web", s.store.LoadAccounts()); msg != "" {
		return 0, 0, msg, nil
	}
	added, skipped, err := s.upsertAccountRecordsForRefresh(tokset, "web")
	if err != nil {
		return 0, 0, "", err
	}
	return added, skipped, "", nil
}

func (s *Server) handleInternalRegisterAccountsRefresh(w http.ResponseWriter, r *http.Request) {
	if !s.requireRegisterInternal(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method not allowed")
		return
	}
	var body struct {
		AccessTokens        []string `json:"access_tokens"`
		DeferInvalidRemoval bool     `json:"defer_invalid_removal"`
	}
	if !readBody(w, r, &body) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	before := s.store.LoadAccounts()
	refreshed, errs, removed, cleanupRemoved := s.refreshAccountInfos(ctx, body.AccessTokens, body.DeferInvalidRemoval)
	accounts := s.store.LoadAccounts()
	if s.logSvc != nil {
		s.logSvc.add("account", "注册机刷新账号", map[string]any{"refreshed": refreshed, "errors": len(errs), "removed_unusable": removed, "cleanup_removed": cleanupRemoved, "emails": accountEmailsForRefreshLog(before, accounts, body.AccessTokens)})
	}
	writeJSON(w, 200, map[string]any{"refreshed": refreshed, "errors": errs, "removed_unusable": removed, "cleanup_removed": cleanupRemoved, "items": accounts})
}

func (s *Server) handleInternalRegisterAccountsDelete(w http.ResponseWriter, r *http.Request) {
	if !s.requireRegisterInternal(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method not allowed")
		return
	}
	var body struct {
		Tokens []string `json:"tokens"`
	}
	if !readBody(w, r, &body) {
		return
	}
	removed, err := s.deleteAccountTokens(body.Tokens)
	if err != nil {
		writeErr(w, 500, "failed to save accounts: "+err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"removed": removed, "items": s.store.LoadAccounts()})
}

func (s *Server) handleInternalRegisterSettings(w http.ResponseWriter, r *http.Request) {
	if !s.requireRegisterInternal(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		writeErr(w, 405, "method not allowed")
		return
	}
	writeJSON(w, 200, map[string]any{
		"config": map[string]any{
			"auto_remove_invalid_accounts":           s.cfg.AutoRemoveInvalidAccounts,
			"auto_remove_rate_limited_accounts":      s.cfg.AutoRemoveRateLimitedAccounts,
			"auto_delete_quota_zero_accounts":        s.cfg.AutoDeleteQuotaZeroAccounts,
			"auto_delete_upload_quota_zero_accounts": s.cfg.AutoDeleteUploadQuotaZeroAccounts,
			"delete_403_consecutive":                 delete403ConsecutiveThreshold(s.cfg),
			"delete_timeout_consecutive":             deleteTimeoutConsecutiveThreshold(s.cfg),
			"auto_refresh_delete_failed_accounts":    s.cfg.AutoRefreshDeleteFailedAccounts,
		},
	})
}

func (s *Server) deleteAccountTokens(tokens []string) (int, error) {
	targets := map[string]bool{}
	for _, token := range tokens {
		if t := strings.TrimSpace(token); t != "" {
			targets[t] = true
		}
	}
	if len(targets) == 0 {
		return 0, nil
	}
	before := s.store.LoadAccounts()
	removed := 0
	err := s.store.UpdateAccounts(func(accounts []Account) []Account {
		out := accounts[:0]
		for _, a := range accounts {
			if targets[a.AccessToken] {
				removed++
				continue
			}
			out = append(out, a)
		}
		return out
	})
	if err != nil {
		return 0, err
	}
	if removed > 0 && s.logSvc != nil {
		s.logSvc.add("account", "注册机删除不可用账号", map[string]any{"removed": removed, "emails": accountEmailsForTokenSet(before, targets)})
	}
	return removed, nil
}

func (s *Server) removeRegisterUnusableAccounts(tokens []string, errs []map[string]any) int {
	targets := map[string]bool{}
	errTargets := map[string]string{}
	for _, token := range tokens {
		if t := strings.TrimSpace(token); t != "" {
			targets[t] = true
		}
	}
	for _, item := range errs {
		for _, key := range []string{"access_token", "token"} {
			if t := strings.TrimSpace(strAny(item[key], "")); t != "" {
				targets[t] = true
				errTargets[t] = strings.TrimSpace(strAny(item["error"], "refresh failed"))
			}
		}
	}
	if len(targets) == 0 {
		return 0
	}
	changed := 0
	for _, account := range s.store.LoadAccounts() {
		if !targets[account.AccessToken] {
			continue
		}
		if errMsg, failed := errTargets[account.AccessToken]; failed && s.cfg.AutoRefreshDeleteFailedAccounts {
			reason := "refresh_failed"
			if isInvalidTokenErrorText(errors.New(errMsg)) {
				reason = "invalid_token"
			}
			if reason == "invalid_token" && !s.cfg.AutoRemoveInvalidAccounts {
				continue
			}
			if ok, err := s.removeOrMarkImageAccount(account.AccessToken, reason); err == nil && ok {
				changed++
			}
			continue
		}
		if reason, ok := s.registerImageAccountRemovalReason(account); ok {
			if ok, err := s.removeOrMarkImageAccount(account.AccessToken, reason); err == nil && ok {
				changed++
			}
		}
	}
	return changed
}
