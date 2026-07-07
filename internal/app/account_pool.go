package app

import (
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
)

type accountPool struct {
	mu             sync.Mutex
	index          int
	inflight       map[string]int
	uploadReserved map[string]int
	cfg            *Config
}

func newAccountPool(cfg *Config) *accountPool {
	return &accountPool{inflight: make(map[string]int), uploadReserved: make(map[string]int), cfg: cfg}
}

func ptrString(v *string) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(*v)
}

func parseAccountTime(value string) (time.Time, error) {
	text := strings.TrimSpace(value)
	if text == "" {
		return time.Time{}, errors.New("empty time")
	}
	if t, err := time.Parse(time.RFC3339, text); err == nil {
		return t, nil
	}
	if strings.HasSuffix(text, "Z") {
		return time.Parse(time.RFC3339Nano, text)
	}
	return time.Parse(time.RFC3339Nano, text)
}

func accountHasActiveLimitWindow(a Account, now time.Time, blockWithoutReset bool) bool {
	if !isAccountStatus(a.Status, accountStatusLimited) {
		return false
	}
	resetAt := firstNonEmpty(ptrString(a.RateLimitResetAt), ptrString(a.RestoreAt))
	if resetAt == "" {
		return blockWithoutReset
	}
	rt, err := parseAccountTime(resetAt)
	return err != nil || now.Before(rt)
}

func (p *accountPool) pickToken(accounts []Account, needCodex bool) (string, error) {
	return p.pickTokenExcluding(accounts, needCodex, "", nil)
}

func (p *accountPool) pickTokenExcluding(accounts []Account, needCodex bool, codexPlanType string, excluded map[string]bool) (string, error) {
	return p.pickTokenExcludingForUploads(accounts, needCodex, codexPlanType, excluded, 0)
}

func (p *accountPool) pickTokenExcludingForUploads(accounts []Account, needCodex bool, codexPlanType string, excluded map[string]bool, requiredUploads int) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	maxConc := p.cfg.ImageAccountConcurrency
	if maxConc < 1 {
		maxConc = 3
	}
	premium := map[string]bool{"plus": true, "pro": true, "team": true}
	now := time.Now().UTC()
	type candidate struct {
		account  Account
		idx      int
		offset   int
		inflight int
		used     int
	}
	candidates := []candidate{}
	uploadBlocked := false
	for offset := 0; offset < len(accounts); offset++ {
		idx := (p.index + offset) % len(accounts)
		a := accounts[idx]
		if a.AccessToken == "" || excluded[a.AccessToken] {
			continue
		}
		if a.PendingDelete || isAccountDisabled(a.Status) {
			continue
		}
		if isAccountInvalidStatus(a.Status) {
			continue
		}
		if accountHasActiveLimitWindow(a, now, true) {
			continue
		}
		if a.ImageQuotaUnknown || a.Quota <= 0 {
			continue
		}
		sourceType := strings.ToLower(strings.TrimSpace(a.SourceType))
		if needCodex {
			accountType := strings.ToLower(a.Type)
			if sourceType != "codex" {
				continue
			}
			if codexPlanType != "" {
				if accountType != codexPlanType {
					continue
				}
			} else if !premium[accountType] {
				continue
			}
		} else if sourceType == "codex" {
			continue
		}
		if requiredUploads > 0 && !needCodex && !accountHasEnoughUploadQuotaAfterReservation(a, requiredUploads, p.uploadReserved[a.AccessToken]) {
			uploadBlocked = true
			continue
		}
		// 检查并发槽
		if p.inflight[a.AccessToken] >= maxConc {
			continue
		}
		candidates = append(candidates, candidate{
			account:  a,
			idx:      idx,
			offset:   offset,
			inflight: p.inflight[a.AccessToken],
			used:     a.Success + a.Fail,
		})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		left, right := candidates[i], candidates[j]
		if requiredUploads > 0 && !needCodex {
			leftKnown := !left.account.UploadQuotaUnknown
			rightKnown := !right.account.UploadQuotaUnknown
			if leftKnown != rightKnown {
				return leftKnown
			}
			if leftKnown {
				leftAvailable := availableUploadQuotaAfterReservation(left.account, p.uploadReserved[left.account.AccessToken])
				rightAvailable := availableUploadQuotaAfterReservation(right.account, p.uploadReserved[right.account.AccessToken])
				if leftAvailable != rightAvailable {
					return leftAvailable > rightAvailable
				}
			}
		}
		if left.account.Quota != right.account.Quota {
			return left.account.Quota > right.account.Quota
		}
		if left.used != right.used {
			return left.used < right.used
		}
		if left.inflight != right.inflight {
			return left.inflight < right.inflight
		}
		return left.offset < right.offset
	})
	if len(candidates) > 0 {
		chosen := candidates[0]
		p.index = (chosen.idx + 1) % len(accounts)
		p.inflight[chosen.account.AccessToken]++
		if requiredUploads > 0 && !needCodex {
			p.uploadReserved[chosen.account.AccessToken] += requiredUploads
		}
		return chosen.account.AccessToken, nil
	}
	if needCodex {
		return "", errors.New("no available codex Plus/Team/Pro account")
	}
	if requiredUploads > 0 && uploadBlocked {
		return "", errors.New("no available image upload quota")
	}
	return "", errors.New("no available image quota")
}

func (p *accountPool) releaseToken(token string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if v := p.inflight[token]; v <= 1 {
		delete(p.inflight, token)
	} else {
		p.inflight[token] = v - 1
	}
}

func (p *accountPool) releaseUploadReservation(token string, n int) {
	token = strings.TrimSpace(token)
	if token == "" || n <= 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if v := p.uploadReserved[token]; v <= n {
		delete(p.uploadReserved, token)
	} else {
		p.uploadReserved[token] = v - n
	}
}

func (p *accountPool) retainToken(token string) {
	token = strings.TrimSpace(token)
	if token == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.inflight[token]++
}

func (p *accountPool) activeCount(token string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.inflight[token]
}

func (p *accountPool) pickTextToken(accounts []Account, planType string) (string, error) {
	return p.pickTextTokenExcluding(accounts, planType, nil)
}

func (p *accountPool) pickTextTokenExcluding(accounts []Account, planType string, excluded map[string]bool) (string, error) {
	return p.pickTextTokenExcludingForUploads(accounts, planType, excluded, 0)
}

func (p *accountPool) pickTextTokenExcludingForUploads(accounts []Account, planType string, excluded map[string]bool, requiredUploads int) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	maxConc := p.cfg.ImageAccountConcurrency
	if maxConc < 1 {
		maxConc = 3
	}
	premium := map[string]bool{"plus": true, "pro": true, "team": true}
	now := time.Now().UTC()
	type candidate struct {
		account Account
		idx     int
		offset  int
		used    int
	}
	candidates := []candidate{}
	uploadBlocked := false
	for offset := 0; offset < len(accounts); offset++ {
		idx := (p.index + offset) % len(accounts)
		a := accounts[idx]
		if a.AccessToken == "" || excluded[a.AccessToken] || a.PendingDelete || isAccountDisabled(a.Status) || isAccountInvalidStatus(a.Status) {
			continue
		}
		if accountHasActiveLimitWindow(a, now, false) {
			continue
		}
		lower := strings.ToLower(a.Type)
		if planType == "free" {
			if premium[lower] {
				continue
			}
		} else if planType != "" {
			if lower != planType && (!premium[lower] || lower != planType) {
				continue
			}
		}
		if requiredUploads > 0 && !accountHasEnoughUploadQuotaAfterReservation(a, requiredUploads, p.uploadReserved[a.AccessToken]) {
			uploadBlocked = true
			continue
		}
		if requiredUploads > 0 && p.inflight[a.AccessToken] >= maxConc {
			continue
		}
		candidates = append(candidates, candidate{account: a, idx: idx, offset: offset, used: a.Success + a.Fail})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		left, right := candidates[i], candidates[j]
		if requiredUploads > 0 {
			leftKnown := !left.account.UploadQuotaUnknown
			rightKnown := !right.account.UploadQuotaUnknown
			if leftKnown != rightKnown {
				return leftKnown
			}
			if leftKnown {
				leftAvailable := availableUploadQuotaAfterReservation(left.account, p.uploadReserved[left.account.AccessToken])
				rightAvailable := availableUploadQuotaAfterReservation(right.account, p.uploadReserved[right.account.AccessToken])
				if leftAvailable != rightAvailable {
					return leftAvailable > rightAvailable
				}
			}
		}
		if left.used != right.used {
			return left.used < right.used
		}
		return left.offset < right.offset
	})
	if len(candidates) > 0 {
		chosen := candidates[0]
		p.index = (chosen.idx + 1) % len(accounts)
		if requiredUploads > 0 {
			p.inflight[chosen.account.AccessToken]++
			p.uploadReserved[chosen.account.AccessToken] += requiredUploads
		}
		return chosen.account.AccessToken, nil
	}
	if requiredUploads > 0 && uploadBlocked {
		return "", errors.New("no available image upload quota")
	}
	return "", errors.New("no available text account")
}

func (s *Server) pickToken(model, resolution string) (string, error) {
	return s.pickTokenExcluding(model, resolution, nil)
}

func (s *Server) pickTokenExcluding(model, resolution string, excluded map[string]bool) (string, error) {
	account, err := s.pickAccountExcluding(model, resolution, excluded)
	return account.AccessToken, err
}

func (s *Server) pickAccountExcluding(model, resolution string, excluded map[string]bool) (Account, error) {
	return s.pickAccountExcludingForUploads(model, resolution, excluded, 0)
}

func (s *Server) pickAccountExcludingForUploads(model, resolution string, excluded map[string]bool, requiredUploads int) (Account, error) {
	accounts := s.store.LoadAccounts()
	needCodex := isCodexImageRequest(model, resolution)
	token, err := s.accountPool.pickTokenExcludingForUploads(accounts, needCodex, codexPlanTypeFromModel(model), excluded, requiredUploads)
	if err != nil {
		return Account{}, err
	}
	for _, account := range accounts {
		if account.AccessToken == token {
			return account, nil
		}
	}
	return Account{AccessToken: token}, nil
}

func (s *Server) pickTextToken() (string, error) {
	return s.pickTextTokenExcluding(nil, "")
}

func (s *Server) pickTextTokenExcluding(excluded map[string]bool, planType string) (string, error) {
	account, err := s.pickTextAccountExcluding(excluded, planType)
	return account.AccessToken, err
}

func (s *Server) pickTextAccountExcluding(excluded map[string]bool, planType string) (Account, error) {
	return s.pickTextAccountExcludingForUploads(excluded, planType, 0)
}

func (s *Server) pickTextAccountExcludingForUploads(excluded map[string]bool, planType string, requiredUploads int) (Account, error) {
	accounts := s.store.LoadAccounts()
	token, err := s.accountPool.pickTextTokenExcludingForUploads(accounts, planType, excluded, requiredUploads)
	if err != nil {
		return Account{}, err
	}
	for _, account := range accounts {
		if account.AccessToken == token {
			return account, nil
		}
	}
	return Account{AccessToken: token}, nil
}
