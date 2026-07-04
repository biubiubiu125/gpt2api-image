package app

import (
	"errors"
	"testing"
)

func newImageAccountGuardTestServer(t *testing.T) *Server {
	t.Helper()
	s := &Server{
		cfg:   Config{ImageAccountConcurrency: 3},
		store: NewStore(t.TempDir()),
	}
	s.accountPool = newAccountPool(&s.cfg)
	return s
}

func TestImageAccountAttemptScopeCapsAtFive(t *testing.T) {
	scope := newImageAccountAttemptScope(maxImageAccountFallbackAttempts)
	for i, token := range []string{"a", "b", "c", "d", "e"} {
		if !scope.reserve(token) {
			t.Fatalf("reserve %d should pass", i+1)
		}
	}
	if scope.reserve("f") {
		t.Fatalf("sixth image account attempt should be rejected")
	}
	if got := scope.usedCount(); got != maxImageAccountFallbackAttempts {
		t.Fatalf("used attempts = %d, want %d", got, maxImageAccountFallbackAttempts)
	}
}

func TestPickTokenPrefersHighQuotaLowUsageLowConcurrency(t *testing.T) {
	cfg := Config{ImageAccountConcurrency: 3}
	pool := newAccountPool(&cfg)
	accounts := []Account{
		{AccessToken: "low-quota", Status: accountStatusNormal, Quota: 10, Success: 0, Fail: 0},
		{AccessToken: "high-used", Status: accountStatusNormal, Quota: 50, Success: 8, Fail: 2},
		{AccessToken: "high-idle", Status: accountStatusNormal, Quota: 50, Success: 1, Fail: 0},
	}
	got, err := pool.pickTokenExcluding(accounts, false, "", nil)
	if err != nil {
		t.Fatalf("pick token: %v", err)
	}
	if got != "high-idle" {
		t.Fatalf("picked %q, want high-idle", got)
	}

	pool.releaseToken(got)
	pool.inflight["busy"] = 1
	accounts = []Account{
		{AccessToken: "busy", Status: accountStatusNormal, Quota: 50, Success: 1, Fail: 0},
		{AccessToken: "free", Status: accountStatusNormal, Quota: 50, Success: 1, Fail: 0},
	}
	got, err = pool.pickTokenExcluding(accounts, false, "", nil)
	if err != nil {
		t.Fatalf("pick token with inflight: %v", err)
	}
	if got != "free" {
		t.Fatalf("picked %q, want free", got)
	}
}

func TestPickTokenSkipsUnknownZeroAndPendingDeleteAccounts(t *testing.T) {
	cfg := Config{ImageAccountConcurrency: 3}
	pool := newAccountPool(&cfg)
	accounts := []Account{
		{AccessToken: "unknown", Status: accountStatusNormal, Quota: 100, ImageQuotaUnknown: true},
		{AccessToken: "zero", Status: accountStatusNormal, Quota: 0},
		{AccessToken: "pending", Status: accountStatusNormal, Quota: 100, PendingDelete: true},
		{AccessToken: "ok", Status: accountStatusNormal, Quota: 1},
	}
	got, err := pool.pickTokenExcluding(accounts, false, "", nil)
	if err != nil {
		t.Fatalf("pick token: %v", err)
	}
	if got != "ok" {
		t.Fatalf("picked %q, want ok", got)
	}
}

func TestImageFailureMarksPendingDeleteWhenAccountIsActive(t *testing.T) {
	s := newImageAccountGuardTestServer(t)
	if err := s.store.SaveAccounts([]Account{{AccessToken: "tok", Status: accountStatusNormal, Quota: 5}}); err != nil {
		t.Fatalf("save accounts: %v", err)
	}
	s.accountPool.inflight["tok"] = 1

	s.markAccountFailure("tok", errors.New("GET /backend-api/conversation failed: status=503 body=busy"), true)

	accounts := s.store.LoadAccounts()
	if len(accounts) != 1 {
		t.Fatalf("active account should be kept pending, accounts=%#v", accounts)
	}
	if !accounts[0].PendingDelete || accounts[0].Quota != 0 || accounts[0].Status != accountStatusInvalid {
		t.Fatalf("account not marked pending delete: %#v", accounts[0])
	}
	s.accountPool.releaseToken("tok")
	s.cleanupPendingDeletedAccounts()
	if got := s.store.LoadAccounts(); len(got) != 0 {
		t.Fatalf("pending account should be removed after release, got %#v", got)
	}
}

func TestImageFailureDeletesInactiveTemporaryAndTurnstileAccounts(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "temporary", err: errors.New("image generation SSE timed out (600s)")},
		{name: "turnstile", err: errors.New("turnstile required")},
		{name: "cloudflare", err: errors.New("GET failed: status=403 body=<html>something seems to have gone wrong</html>")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newImageAccountGuardTestServer(t)
			if err := s.store.SaveAccounts([]Account{{AccessToken: "tok", Status: accountStatusNormal, Quota: 5}}); err != nil {
				t.Fatalf("save accounts: %v", err)
			}
			s.markAccountFailure("tok", tc.err, true)
			if got := s.store.LoadAccounts(); len(got) != 0 {
				t.Fatalf("inactive abnormal account should be removed, got %#v", got)
			}
		})
	}
}

func TestImageSuccessDeletesAccountWhenQuotaReachesZero(t *testing.T) {
	s := newImageAccountGuardTestServer(t)
	if err := s.store.SaveAccounts([]Account{{AccessToken: "tok", Status: accountStatusNormal, Quota: 1}}); err != nil {
		t.Fatalf("save accounts: %v", err)
	}
	s.markAccountSuccess("tok", true)
	if got := s.store.LoadAccounts(); len(got) != 0 {
		t.Fatalf("quota zero account should be removed, got %#v", got)
	}
}

func TestRegisterRemovalDeletesUnknownQuotaAccounts(t *testing.T) {
	s := newImageAccountGuardTestServer(t)
	if err := s.store.SaveAccounts([]Account{{AccessToken: "tok", Status: accountStatusNormal, Quota: 100, ImageQuotaUnknown: true}}); err != nil {
		t.Fatalf("save accounts: %v", err)
	}
	if changed := s.removeRegisterUnusableAccounts([]string{"tok"}, nil); changed != 1 {
		t.Fatalf("changed = %d, want 1", changed)
	}
	if got := s.store.LoadAccounts(); len(got) != 0 {
		t.Fatalf("unknown quota account should be removed, got %#v", got)
	}
}

func TestMergeRefreshedAccountInfoOverridesStaleKnownQuota(t *testing.T) {
	account := Account{AccessToken: "tok", Status: accountStatusNormal, Quota: 100, ImageQuotaUnknown: false}
	mergeRefreshedAccountInfo(&account, Account{
		AccessToken:       "new-token-ignored",
		Status:            accountStatusNormal,
		Quota:             0,
		ImageQuotaUnknown: true,
	})
	if account.AccessToken != "tok" {
		t.Fatalf("refresh merge should keep stored token, got %q", account.AccessToken)
	}
	if !account.ImageQuotaUnknown || account.Quota != 0 {
		t.Fatalf("refresh merge should overwrite stale quota with unknown state: %#v", account)
	}

	s := newImageAccountGuardTestServer(t)
	if err := s.store.SaveAccounts([]Account{account}); err != nil {
		t.Fatalf("save accounts: %v", err)
	}
	s.cleanupUnusableImageAccounts()
	if got := s.store.LoadAccounts(); len(got) != 0 {
		t.Fatalf("refreshed unknown quota account should be removed, got %#v", got)
	}
}
