package app

import (
	"context"
	"crypto/subtle"
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
	if bearer != "" && subtle.ConstantTimeCompare([]byte(bearer), []byte(s.cfg.AuthKey)) == 1 {
		return true
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
		added, skipped, msg := s.addAccountRecords(body.AccountRecords)
		if msg != "" {
			writeErr(w, 400, msg)
			return
		}
		if s.logSvc != nil {
			s.logSvc.add("account", "注册机写入账号", map[string]any{"added": added, "skipped": skipped})
		}
		writeJSON(w, 200, map[string]any{"added": added, "skipped": skipped, "items": s.store.LoadAccounts()})
	default:
		writeErr(w, 405, "method not allowed")
	}
}

func (s *Server) addAccountRecords(records []map[string]any) (int, int, string) {
	tokset := map[string]map[string]any{}
	for _, rec := range records {
		t := strings.TrimSpace(strAny(rec["access_token"], strAny(rec["accessToken"], "")))
		if t != "" {
			tokset[t] = rec
		}
	}
	if msg := validateAccountRecordsForSource(tokset, "web", s.store.LoadAccounts()); msg != "" {
		return 0, 0, msg
	}
	added, skipped := 0, 0
	_ = s.store.UpdateAccounts(func(accounts []Account) []Account {
		existing := map[string]int{}
		for i, a := range accounts {
			existing[a.AccessToken] = i
		}
		for token, rec := range tokset {
			source := accountRecordSource("web", rec)
			a := accountFromRecord(token, source, rec)
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
	})
	return added, skipped, ""
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
	refreshed, errs := s.refreshAccountInfos(ctx, body.AccessTokens)
	removed := 0
	if !body.DeferInvalidRemoval {
		removed = s.removeRegisterUnusableAccounts(body.AccessTokens, errs)
	}
	writeJSON(w, 200, map[string]any{"refreshed": refreshed, "errors": errs, "removed_unusable": removed, "items": s.store.LoadAccounts()})
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
	removed := s.deleteAccountTokens(body.Tokens)
	writeJSON(w, 200, map[string]any{"removed": removed, "items": s.store.LoadAccounts()})
}

func (s *Server) deleteAccountTokens(tokens []string) int {
	targets := map[string]bool{}
	for _, token := range tokens {
		if t := strings.TrimSpace(token); t != "" {
			targets[t] = true
		}
	}
	if len(targets) == 0 {
		return 0
	}
	removed := 0
	_ = s.store.UpdateAccounts(func(accounts []Account) []Account {
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
	if removed > 0 && s.logSvc != nil {
		s.logSvc.add("account", "注册机删除不可用账号", map[string]any{"removed": removed})
	}
	return removed
}

func (s *Server) removeRegisterUnusableAccounts(tokens []string, errs []map[string]any) int {
	targets := map[string]bool{}
	for _, token := range tokens {
		if t := strings.TrimSpace(token); t != "" {
			targets[t] = true
		}
	}
	for _, item := range errs {
		for _, key := range []string{"access_token", "token"} {
			if t := strings.TrimSpace(strAny(item[key], "")); t != "" {
				targets[t] = true
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
		if reason, ok := imageAccountRecordRemovalReason(account); ok {
			if s.removeOrMarkImageAccount(account.AccessToken, reason) {
				changed++
			}
		}
	}
	return changed
}
