package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
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

func TestGenerateImageWithPoolCanceledContextDoesNotMarkAccountFailure(t *testing.T) {
	s := newImageAccountGuardTestServer(t)
	if err := s.store.SaveAccounts([]Account{
		{AccessToken: "tok-a", SourceType: "web", Status: accountStatusNormal, Quota: 5},
		{AccessToken: "tok-b", SourceType: "web", Status: accountStatusNormal, Quota: 5},
	}); err != nil {
		t.Fatalf("save accounts: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	items, err := s.generateImageWithPool(ctx, "prompt", "gpt-image-2", "1:1", "", nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("generateImageWithPool err = %v, want context.Canceled", err)
	}
	if len(items) != 0 {
		t.Fatalf("items = %#v, want none", items)
	}
	for _, account := range s.store.LoadAccounts() {
		if account.Fail != 0 {
			t.Fatalf("account %s fail = %d, want 0", account.AccessToken, account.Fail)
		}
		if got := s.accountPool.activeCount(account.AccessToken); got != 0 {
			t.Fatalf("account %s active count = %d, want 0", account.AccessToken, got)
		}
	}
}

func TestCollectImageBatchResultsDoesNotSwallowCanceledOrPolicy(t *testing.T) {
	success := upstreamImageResult{Bytes: []byte("ok")}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	items, err := collectImageBatchResults(ctx, [][]upstreamImageResult{{success}}, []error{nil})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled context err = %v, want context.Canceled", err)
	}
	if len(items) != 0 {
		t.Fatalf("canceled context items = %#v, want none", items)
	}

	items, err = collectImageBatchResults(context.Background(), [][]upstreamImageResult{{success}, nil}, []error{nil, context.Canceled})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled worker err = %v, want context.Canceled", err)
	}
	if len(items) != 0 {
		t.Fatalf("canceled worker items = %#v, want none", items)
	}

	policyErr := errors.New("I can\u2019t generate this image because it violates our content policy.")
	items, err = collectImageBatchResults(context.Background(), [][]upstreamImageResult{{success}, nil}, []error{nil, policyErr})
	if err == nil || !strings.Contains(err.Error(), "content policy") {
		t.Fatalf("policy err = %v, want content policy", err)
	}
	if len(items) != 0 {
		t.Fatalf("policy items = %#v, want none", items)
	}
}

func TestCollectImageBatchResultsAllowsPartialForRetryableFailures(t *testing.T) {
	success := upstreamImageResult{Bytes: []byte("ok")}
	items, err := collectImageBatchResults(context.Background(), [][]upstreamImageResult{{success}, nil}, []error{nil, errors.New("image generation SSE timed out (120s)")})
	if err != nil {
		t.Fatalf("collect err = %v, want partial success", err)
	}
	if len(items) != 1 || string(items[0].Bytes) != "ok" {
		t.Fatalf("items = %#v, want one successful image", items)
	}
}

func TestCollectImageBatchResultsDoesNotSwallowUploadLimit(t *testing.T) {
	success := upstreamImageResult{Bytes: []byte("ok")}
	items, err := collectImageBatchResults(context.Background(), [][]upstreamImageResult{{success}, nil}, []error{nil, errors.New("POST /backend-api/files failed: status=429 body=file upload limit reached")})
	if err == nil || !strings.Contains(err.Error(), "file upload limit") {
		t.Fatalf("upload limit err = %v, want propagated upload limit", err)
	}
	if len(items) != 0 {
		t.Fatalf("upload limit items = %#v, want none", items)
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

	s.markAccountFailure("tok", errors.New(`oauth refresh failed: status=400 body={"error":"invalid_grant"}`), true)

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

func TestRateLimitedAutoRemoveMarksPendingDeleteWhenAccountIsActive(t *testing.T) {
	s := newImageAccountGuardTestServer(t)
	s.cfg.AutoRemoveRateLimitedAccounts = true
	if err := s.store.SaveAccounts([]Account{{AccessToken: "tok", Status: accountStatusNormal, Quota: 5}}); err != nil {
		t.Fatalf("save accounts: %v", err)
	}
	s.accountPool.inflight["tok"] = 1

	s.markAccountFailure("tok", errors.New("image generation failed: status=429 body=too many requests"), true)

	accounts := s.store.LoadAccounts()
	if len(accounts) != 1 {
		t.Fatalf("active rate-limited account should be kept pending, accounts=%#v", accounts)
	}
	if !accounts[0].PendingDelete || accounts[0].Quota != 0 || accounts[0].Status != accountStatusInvalid {
		t.Fatalf("rate-limited active account not marked pending delete: %#v", accounts[0])
	}
	s.accountPool.releaseToken("tok")
	s.cleanupPendingDeletedAccounts()
	if got := s.store.LoadAccounts(); len(got) != 0 {
		t.Fatalf("pending rate-limited account should be removed after release, got %#v", got)
	}
}

func TestImageFailureDeletesInactiveInvalidAccounts(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "oauth invalid grant", err: errors.New(`oauth refresh failed: status=400 body={"error":"invalid_grant"}`)},
		{name: "oauth missing refresh token", err: errors.New("refresh_token not found")},
		{name: "token invalidated", err: errors.New("authentication token has been invalidated")},
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

func TestRetryableNonCredentialImageFailuresKeepAccount(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "temporary", err: errors.New("image generation SSE timed out (600s)")},
		{name: "upstream 503", err: errors.New("GET /backend-api/conversation failed: status=503 body=busy")},
		{name: "turnstile", err: errors.New("turnstile required")},
		{name: "cloudflare", err: errors.New("GET failed: status=403 body=<html>something seems to have gone wrong</html>")},
		{name: "bootstrap", err: errors.New("bootstrap redirect: status=302 location=/auth/login")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newImageAccountGuardTestServer(t)
			if err := s.store.SaveAccounts([]Account{{AccessToken: "tok", Status: accountStatusNormal, Quota: 5}}); err != nil {
				t.Fatalf("save accounts: %v", err)
			}
			if !shouldRetryImageAccount(tc.err) {
				t.Fatalf("%s should stay retryable", tc.name)
			}
			s.markAccountFailure("tok", tc.err, true)
			got := s.store.LoadAccounts()
			if len(got) != 1 {
				t.Fatalf("retryable non-credential error should keep account, got %#v", got)
			}
			if got[0].PendingDelete || got[0].Status != accountStatusNormal || got[0].Quota != 5 || got[0].Fail != 1 {
				t.Fatalf("account state after %s = %#v, want kept normal with one failure", tc.name, got[0])
			}
		})
	}
}

func TestCodexSetupInvalidRefreshTokenRetriesAndDeletesAccounts(t *testing.T) {
	s := newImageAccountGuardTestServer(t)
	if err := s.store.SaveAccounts([]Account{
		{AccessToken: "bad-high", SourceType: "codex", Type: "plus", Status: accountStatusNormal, Quota: 50},
		{AccessToken: "bad-low", SourceType: "codex", Type: "plus", Status: accountStatusNormal, Quota: 40},
	}); err != nil {
		t.Fatalf("save accounts: %v", err)
	}

	_, err := s.generateImageWithPool(context.Background(), "prompt", "codex-gpt-image-2", "1:1", "", nil)
	if err == nil {
		t.Fatal("expected setup error")
	}
	if !strings.Contains(err.Error(), "refresh_token not found") {
		t.Fatalf("error = %q, want refresh token setup error", err.Error())
	}
	if got := s.store.LoadAccounts(); len(got) != 0 {
		t.Fatalf("codex setup should retry and delete invalid accounts, got %#v", got)
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

func TestCountAccountsForTokensUsesFinalPersistedAccounts(t *testing.T) {
	accounts := []Account{{AccessToken: "kept"}, {AccessToken: "other"}}
	got := countAccountsForTokens(accounts, []string{"kept", "removed"})
	if got != 1 {
		t.Fatalf("countAccountsForTokens = %d, want 1", got)
	}
}

func TestCountValidatedAccountsForTokensRequiresUsableAccounts(t *testing.T) {
	accounts := []Account{
		{AccessToken: "usable", Status: accountStatusNormal, Quota: 10},
		{AccessToken: "unknown", Status: accountStatusNormal, Quota: 10, ImageQuotaUnknown: true},
		{AccessToken: "zero", Status: accountStatusNormal, Quota: 0},
		{AccessToken: "pending", Status: accountStatusNormal, Quota: 10, PendingDelete: true},
		{AccessToken: "disabled", Status: accountStatusDisabled, Quota: 10},
	}
	got := countValidatedAccountsForTokens(accounts, []string{"usable", "unknown", "zero", "pending", "disabled"})
	if got != 1 {
		t.Fatalf("countValidatedAccountsForTokens = %d, want 1", got)
	}
}

func TestImportedAccountIsPendingRefreshValidation(t *testing.T) {
	s := newImageAccountGuardTestServer(t)
	records := map[string]map[string]any{
		"tok": {"access_token": "tok", "status": accountStatusNormal, "quota": 100},
	}
	added, skipped, err := s.upsertAccountRecordsForRefresh(records, "web")
	if err != nil {
		t.Fatalf("upsert account: %v", err)
	}
	if added != 1 || skipped != 0 {
		t.Fatalf("added=%d skipped=%d, want 1/0", added, skipped)
	}
	got := s.store.LoadAccounts()
	if len(got) != 1 {
		t.Fatalf("accounts = %#v, want one pending account", got)
	}
	if !got[0].ImageQuotaUnknown || !got[0].RefreshValidationPending || got[0].Quota != 0 {
		t.Fatalf("imported account should wait for refresh validation, got %#v", got[0])
	}
}

func TestPendingRefreshValidationSurvivesGlobalImageCleanup(t *testing.T) {
	s := newImageAccountGuardTestServer(t)
	if _, _, err := s.upsertAccountRecordsForRefresh(map[string]map[string]any{
		"tok": {"access_token": "tok", "status": accountStatusNormal, "quota": 100},
	}, "web"); err != nil {
		t.Fatalf("upsert account: %v", err)
	}

	if removed := s.cleanupUnusableImageAccounts(); removed != 0 {
		t.Fatalf("pending refresh validation account should not be globally cleaned, removed=%d", removed)
	}
	got := s.store.LoadAccounts()
	if len(got) != 1 || !got[0].RefreshValidationPending || !got[0].ImageQuotaUnknown || got[0].Quota != 0 {
		t.Fatalf("pending refresh validation account should survive global cleanup, got %#v", got)
	}

	if removed := s.removeRegisterUnusableAccounts([]string{"tok"}, []map[string]any{{"token": "tok", "error": "refresh failed"}}); removed != 1 {
		t.Fatalf("refresh cleanup should still remove pending invalid account, removed=%d", removed)
	}
	if got := s.store.LoadAccounts(); len(got) != 0 {
		t.Fatalf("pending account should be removed after refresh failure, got %#v", got)
	}
}

func TestStalePendingRefreshValidationIsGloballyCleaned(t *testing.T) {
	s := newImageAccountGuardTestServer(t)
	createdAt := time.Now().UTC().Add(-accountRefreshValidationGracePeriod - time.Minute).Format(time.RFC3339Nano)
	if err := s.store.SaveAccounts([]Account{{
		AccessToken:              "tok",
		Status:                   accountStatusNormal,
		ImageQuotaUnknown:        true,
		RefreshValidationPending: true,
		Quota:                    0,
		CreatedAt:                &createdAt,
	}}); err != nil {
		t.Fatalf("save accounts: %v", err)
	}

	if removed := s.cleanupUnusableImageAccounts(); removed != 1 {
		t.Fatalf("stale pending refresh validation account should be cleaned, removed=%d", removed)
	}
	if got := s.store.LoadAccounts(); len(got) != 0 {
		t.Fatalf("stale pending account should be removed, got %#v", got)
	}
}

func TestExistingValidatedAccountKeepsRuntimeStateWhenReimported(t *testing.T) {
	s := newImageAccountGuardTestServer(t)
	limitedAt := "2026-01-01T00:00:00Z"
	resetAt := "2026-01-01T01:00:00Z"
	if err := s.store.SaveAccounts([]Account{{
		AccessToken:       "tok",
		Status:            accountStatusNormal,
		Quota:             25,
		InitialQuota:      50,
		ImageQuotaUnknown: false,
		LimitsProgress:    []map[string]any{{"feature_name": "images", "remaining": 25}},
		RateLimitedAt:     &limitedAt,
		RateLimitResetAt:  &resetAt,
	}}); err != nil {
		t.Fatalf("save existing account: %v", err)
	}

	added, skipped, err := s.upsertAccountRecordsForRefresh(map[string]map[string]any{
		"tok": {"access_token": "tok", "status": accountStatusNormal, "quota": 100, "email": "updated@example.com"},
	}, "web")
	if err != nil {
		t.Fatalf("upsert account: %v", err)
	}
	if added != 0 || skipped != 1 {
		t.Fatalf("added=%d skipped=%d, want 0/1", added, skipped)
	}
	got := s.store.LoadAccounts()
	if len(got) != 1 {
		t.Fatalf("accounts = %#v, want one existing account", got)
	}
	if got[0].ImageQuotaUnknown || got[0].RefreshValidationPending || got[0].Quota != 25 || len(got[0].LimitsProgress) != 1 {
		t.Fatalf("existing validated runtime state should be preserved before refresh, got %#v", got[0])
	}
	if got[0].Email == nil || *got[0].Email != "updated@example.com" {
		t.Fatalf("account metadata should still be updated, got %#v", got[0].Email)
	}

	removed := s.removeRegisterUnusableAccounts([]string{"tok"}, []map[string]any{{"token": "tok", "error": "temporary refresh failed"}})
	if removed != 0 {
		t.Fatalf("existing validated account should not be removed on refresh error, removed=%d", removed)
	}
	if got := s.store.LoadAccounts(); len(got) != 1 || got[0].Quota != 25 || got[0].ImageQuotaUnknown || got[0].RefreshValidationPending {
		t.Fatalf("existing validated account should survive refresh failure, got %#v", got)
	}
}

func TestRefreshFailureRemovesImportedAccountWithStaleQuota(t *testing.T) {
	s := newImageAccountGuardTestServer(t)
	if _, _, err := s.upsertAccountRecordsForRefresh(map[string]map[string]any{
		"tok": {"access_token": "tok", "status": accountStatusNormal, "quota": 100},
	}, "web"); err != nil {
		t.Fatalf("upsert account: %v", err)
	}
	if got := s.store.LoadAccounts(); len(got) != 1 || !got[0].ImageQuotaUnknown || !got[0].RefreshValidationPending || got[0].Quota != 0 {
		t.Fatalf("account should be pending validation before refresh, got %#v", got)
	}

	s.removeRegisterUnusableAccounts([]string{"tok"}, []map[string]any{{"token": "tok", "error": "refresh failed"}})
	if got := s.store.LoadAccounts(); len(got) != 0 {
		t.Fatalf("stale imported account should be removed after refresh failure, got %#v", got)
	}
}

func TestRemoveRegisterUnusableAccountsReturnsActualRemovalCount(t *testing.T) {
	s := newImageAccountGuardTestServer(t)
	if err := s.store.SaveAccounts([]Account{{AccessToken: "tok", Status: accountStatusNormal, ImageQuotaUnknown: true, Quota: 0}}); err != nil {
		t.Fatalf("save accounts: %v", err)
	}

	removed := s.removeRegisterUnusableAccounts([]string{"tok"}, nil)
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	if got := s.store.LoadAccounts(); len(got) != 0 {
		t.Fatalf("unusable account should be removed, got %#v", got)
	}
}

func TestRefreshRemovalCountsSeparateTargetAndGlobalCleanup(t *testing.T) {
	s := newImageAccountGuardTestServer(t)
	if err := s.store.SaveAccounts([]Account{
		{AccessToken: "target", Status: accountStatusNormal, Quota: 10},
		{AccessToken: "stale", Status: accountStatusNormal, ImageQuotaUnknown: true, Quota: 0},
	}); err != nil {
		t.Fatalf("save accounts: %v", err)
	}

	removed, cleanupRemoved := s.cleanupRefreshedAccountState([]string{"target"}, nil, false)
	if removed != 0 {
		t.Fatalf("target removed = %d, want 0", removed)
	}
	if cleanupRemoved != 1 {
		t.Fatalf("cleanup removed = %d, want 1", cleanupRemoved)
	}
	got := s.store.LoadAccounts()
	if len(got) != 1 {
		t.Fatalf("accounts = %#v, want target account after stale cleanup", got)
	}
	if countAccountsForTokens(got, []string{"target"}) != 1 {
		t.Fatalf("target account should survive global cleanup, got %#v", got)
	}
}

func TestCodexAccountImportRequiresRefreshToken(t *testing.T) {
	records := map[string]map[string]any{
		"tok": {"access_token": "tok"},
	}
	if msg := validateAccountRecordsForSource(records, "codex", nil); msg == "" {
		t.Fatal("expected codex token-only import to be rejected")
	}

	records["tok"]["refreshToken"] = "refresh-token"
	if msg := validateAccountRecordsForSource(records, "codex", nil); msg != "" {
		t.Fatalf("codex import with refreshToken rejected: %s", msg)
	}

	refreshToken := "existing-refresh"
	if msg := validateAccountRecordsForSource(map[string]map[string]any{
		"tok": {"access_token": "tok"},
	}, "codex", []Account{{AccessToken: "tok", RefreshToken: &refreshToken}}); msg != "" {
		t.Fatalf("existing codex refresh token should satisfy update: %s", msg)
	}
}

func TestInternalRegisterAccountRecordsUseSharedCodexValidation(t *testing.T) {
	s := newImageAccountGuardTestServer(t)
	added, skipped, msg, err := s.addAccountRecords([]map[string]any{
		{"accessToken": "tok", "sourceType": "codex", "quota": 10},
	})
	if err != nil {
		t.Fatalf("addAccountRecords error: %v", err)
	}
	if msg == "" {
		t.Fatal("expected internal codex account without refresh token to be rejected")
	}
	if added != 0 || skipped != 0 {
		t.Fatalf("added=%d skipped=%d, want 0/0", added, skipped)
	}
	if got := s.store.LoadAccounts(); len(got) != 0 {
		t.Fatalf("rejected codex account should not be stored, got %#v", got)
	}

	added, skipped, msg, err = s.addAccountRecords([]map[string]any{
		{"accessToken": "tok", "sourceType": "codex", "refreshToken": "refresh-token", "quota": 10},
	})
	if err != nil {
		t.Fatalf("addAccountRecords error: %v", err)
	}
	if msg != "" {
		t.Fatalf("internal codex account with refreshToken rejected: %s", msg)
	}
	if added != 1 || skipped != 0 {
		t.Fatalf("added=%d skipped=%d, want 1/0", added, skipped)
	}
	accounts := s.store.LoadAccounts()
	if len(accounts) != 1 {
		t.Fatalf("accounts len=%d, want 1", len(accounts))
	}
	if accounts[0].SourceType != "codex" {
		t.Fatalf("source type = %q, want codex", accounts[0].SourceType)
	}
	if accounts[0].RefreshToken == nil || *accounts[0].RefreshToken != "refresh-token" {
		t.Fatalf("refresh token not preserved: %#v", accounts[0].RefreshToken)
	}
}

func TestMergeRefreshedAccountInfoPreservesQuotaWithoutImageSignal(t *testing.T) {
	account := Account{AccessToken: "tok", SourceType: "codex", Status: accountStatusNormal, Quota: 100, ImageQuotaUnknown: false, RefreshValidationPending: true}
	mergeRefreshedAccountInfo(&account, Account{
		AccessToken:       "new-token-ignored",
		SourceType:        "web",
		Status:            accountStatusNormal,
		Quota:             0,
		ImageQuotaUnknown: true,
	})
	if account.AccessToken != "tok" {
		t.Fatalf("refresh merge should keep stored token, got %q", account.AccessToken)
	}
	if account.SourceType != "codex" {
		t.Fatalf("refresh merge should keep codex source type, got %q", account.SourceType)
	}
	if account.ImageQuotaUnknown || account.RefreshValidationPending || account.Quota != 100 {
		t.Fatalf("refresh merge should preserve image quota without image quota signal: %#v", account)
	}

	s := newImageAccountGuardTestServer(t)
	if err := s.store.SaveAccounts([]Account{account}); err != nil {
		t.Fatalf("save accounts: %v", err)
	}
	s.cleanupUnusableImageAccounts()
	if got := s.store.LoadAccounts(); len(got) != 1 {
		t.Fatalf("refreshed account without image quota signal should remain, got %#v", got)
	}
}

func TestMergeRefreshedAccountInfoPreservesImageQuotaOnEmptyRefreshAccount(t *testing.T) {
	account := Account{
		AccessToken:              "tok",
		SourceType:               "codex",
		Status:                   accountStatusNormal,
		Quota:                    25,
		InitialQuota:             25,
		ImageQuotaUnknown:        false,
		LimitsProgress:           []map[string]any{{"feature_name": "image_gen", "remaining": 25}},
		RefreshValidationPending: true,
	}
	mergeRefreshedAccountInfo(&account, Account{AccessToken: "tok"})

	if account.Quota != 25 || account.InitialQuota != 25 || account.ImageQuotaUnknown || len(account.LimitsProgress) != 1 {
		t.Fatalf("empty refresh account should not overwrite image quota: %#v", account)
	}
	if account.RefreshValidationPending {
		t.Fatalf("refresh validation should be cleared: %#v", account)
	}
}

func TestMergeRefreshedAccountInfoOverridesExplicitEmptyImageQuota(t *testing.T) {
	account := Account{AccessToken: "tok", SourceType: "codex", Status: accountStatusNormal, Quota: 100, ImageQuotaUnknown: false, RefreshValidationPending: true}
	mergeRefreshedAccountInfo(&account, Account{
		AccessToken:       "new-token-ignored",
		SourceType:        "web",
		Status:            accountStatusLimited,
		Quota:             0,
		ImageQuotaUnknown: false,
		LimitsProgress:    []map[string]any{{"feature_name": "image_gen", "remaining": 0}},
	})
	if account.AccessToken != "tok" {
		t.Fatalf("refresh merge should keep stored token, got %q", account.AccessToken)
	}
	if account.SourceType != "codex" {
		t.Fatalf("refresh merge should keep codex source type, got %q", account.SourceType)
	}
	if account.ImageQuotaUnknown || account.RefreshValidationPending || account.Quota != 0 {
		t.Fatalf("refresh merge should overwrite stale quota with explicit empty image quota: %#v", account)
	}

	s := newImageAccountGuardTestServer(t)
	if err := s.store.SaveAccounts([]Account{account}); err != nil {
		t.Fatalf("save accounts: %v", err)
	}
	s.cleanupUnusableImageAccounts()
	if got := s.store.LoadAccounts(); len(got) != 0 {
		t.Fatalf("refreshed empty quota account should be removed, got %#v", got)
	}
}
