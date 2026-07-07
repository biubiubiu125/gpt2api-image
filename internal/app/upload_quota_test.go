package app

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestUploadQuotaFromLimitsUsesMostConservativeUploadFeature(t *testing.T) {
	limits := []map[string]any{
		{"feature_name": "image_gen", "remaining": 100},
		{"feature_name": "file_uploads", "remaining": 3, "reset_after": "2026-07-08T00:00:00Z"},
		{"feature_name": "multimodal_uploads", "remaining": 2, "resets_at": "2026-07-08T01:00:00Z"},
		{"feature_name": "file_search", "remaining": 1},
	}

	got := uploadQuotaFromLimits(limits)
	if !got.Found {
		t.Fatal("expected upload quota feature to be found")
	}
	if got.Quota != 2 || got.FeatureName != "multimodal_uploads" || got.ResetAt != "2026-07-08T01:00:00Z" {
		t.Fatalf("upload quota = %#v, want multimodal_uploads remaining 2", got)
	}
}

func TestUploadQuotaFromLimitsAcceptsAlternateRemainingAndResetFields(t *testing.T) {
	limits := []map[string]any{
		{"feature_name": "file_uploads", "remaining_count": 5, "next_reset_at": "2026-07-08T02:00:00Z"},
		{"feature_name": "attachment_uploads", "limit": 10, "used": 7, "available_at": "2026-07-08T03:00:00Z"},
	}

	got := uploadQuotaFromLimits(limits)
	if !got.Found {
		t.Fatal("expected upload quota feature to be found")
	}
	if got.Quota != 3 || got.FeatureName != "attachment_uploads" || got.ResetAt != "2026-07-08T03:00:00Z" {
		t.Fatalf("upload quota = %#v, want attachment_uploads remaining 3", got)
	}
}

func TestUploadQuotaFromLimitsIgnoresNonUploadFileFeature(t *testing.T) {
	limits := []map[string]any{
		{"feature_name": "file_storage", "remaining": 0},
	}

	got := uploadQuotaFromLimits(limits)
	if got.Found {
		t.Fatalf("file_storage should not be treated as upload quota: %#v", got)
	}
}

func TestStoreLoadAndUpdateBackfillsUploadQuotaFromLimitsProgress(t *testing.T) {
	s := newImageAccountGuardTestServer(t)
	resetAt := "2026-07-08T00:00:00Z"
	legacy := []map[string]any{{
		"access_token": "legacy",
		"status":       accountStatusNormal,
		"quota":        25,
		"limits_progress": []map[string]any{
			{"feature_name": "file_uploads", "remaining": 2, "reset_after": resetAt},
		},
	}}
	if err := writeJSONFile(s.store.path("accounts.json"), legacy); err != nil {
		t.Fatalf("write legacy accounts: %v", err)
	}

	loaded := s.store.LoadAccounts()
	if len(loaded) != 1 || loaded[0].UploadQuota != 2 || loaded[0].UploadQuotaUnknown {
		t.Fatalf("loaded legacy upload quota = %#v, want known quota 2", loaded)
	}

	s.markAccountUploadSuccess("legacy", 1)

	got := s.store.LoadAccounts()
	if len(got) != 1 || got[0].UploadQuota != 1 || got[0].UploadQuotaUnknown {
		t.Fatalf("updated legacy upload quota = %#v, want known quota 1", got)
	}
}

func TestPickTokenForUploadsSkipsInsufficientAndPrefersKnownQuota(t *testing.T) {
	cfg := Config{ImageAccountConcurrency: 3}
	pool := newAccountPool(&cfg)
	accounts := []Account{
		{AccessToken: "insufficient", Status: accountStatusNormal, Quota: 100, UploadQuota: 1},
		{AccessToken: "unknown", Status: accountStatusNormal, Quota: 200, UploadQuotaUnknown: true},
		{AccessToken: "known", Status: accountStatusNormal, Quota: 20, UploadQuota: 3},
	}

	got, err := pool.pickTokenExcludingForUploads(accounts, false, "", nil, 2)
	if err != nil {
		t.Fatalf("pick token: %v", err)
	}
	if got != "known" {
		t.Fatalf("picked %q, want known", got)
	}
}

func TestUploadQuotaReservationBlocksConcurrentOverselect(t *testing.T) {
	cfg := Config{ImageAccountConcurrency: 3}
	pool := newAccountPool(&cfg)
	accounts := []Account{
		{AccessToken: "tok", Status: accountStatusNormal, Quota: 100, UploadQuota: 3},
	}

	got, err := pool.pickTokenExcludingForUploads(accounts, false, "", nil, 2)
	if err != nil {
		t.Fatalf("first pick token: %v", err)
	}
	if got != "tok" {
		t.Fatalf("first pick = %q, want tok", got)
	}
	if _, err := pool.pickTokenExcludingForUploads(accounts, false, "", nil, 2); err == nil || err.Error() != "no available image upload quota" {
		t.Fatalf("second pick err = %v, want upload quota error", err)
	}

	pool.releaseToken("tok")
	pool.releaseUploadReservation("tok", 2)
	if _, err := pool.pickTokenExcludingForUploads(accounts, false, "", nil, 2); err != nil {
		t.Fatalf("pick after release: %v", err)
	}
}

func TestUnknownUploadQuotaBlocksUploadTask(t *testing.T) {
	cfg := Config{ImageAccountConcurrency: 3}
	pool := newAccountPool(&cfg)
	accounts := []Account{
		{AccessToken: "unknown", Status: accountStatusNormal, Quota: 100, UploadQuotaUnknown: true},
	}

	if _, err := pool.pickTokenExcludingForUploads(accounts, false, "", nil, 1); err == nil || err.Error() != "no available image upload quota" {
		t.Fatalf("unknown pick err = %v, want upload quota error", err)
	}
}

func TestUnknownUploadQuotaBlocksTextUploadTask(t *testing.T) {
	cfg := Config{ImageAccountConcurrency: 3}
	pool := newAccountPool(&cfg)
	accounts := []Account{
		{AccessToken: "unknown", Status: accountStatusNormal, UploadQuotaUnknown: true},
	}

	if _, err := pool.pickTextTokenExcludingForUploads(accounts, "", nil, 1); err == nil || err.Error() != "no available image upload quota" {
		t.Fatalf("unknown text upload pick err = %v, want upload quota error", err)
	}
}

func TestUploadQuotaAccountSurvivesImageQuotaCleanupForTextUploads(t *testing.T) {
	s := newImageAccountGuardTestServer(t)
	resetAt := "2026-07-08T00:00:00Z"
	featureName := "file_uploads"
	account := Account{
		AccessToken:            "upload-only",
		Status:                 accountStatusNormal,
		Quota:                  0,
		ImageQuotaUnknown:      true,
		UploadQuota:            2,
		UploadQuotaUnknown:     false,
		UploadLimitResetAt:     &resetAt,
		UploadLimitFeatureName: &featureName,
	}
	if err := s.store.SaveAccounts([]Account{account}); err != nil {
		t.Fatalf("save accounts: %v", err)
	}

	if removed := s.cleanupUnusableImageAccounts(); removed != 0 {
		t.Fatalf("upload-capable account should survive image cleanup, removed=%d", removed)
	}
	got := s.store.LoadAccounts()
	if len(got) != 1 {
		t.Fatalf("accounts after cleanup = %#v, want retained upload account", got)
	}
	if _, err := s.accountPool.pickTokenExcludingForUploads(got, false, "", nil, 1); err == nil || err.Error() != "no available image quota" {
		t.Fatalf("image pick err = %v, want image quota rejection", err)
	}
	token, err := s.accountPool.pickTextTokenExcludingForUploads(got, "", nil, 1)
	if err != nil {
		t.Fatalf("text upload pick: %v", err)
	}
	if token != "upload-only" {
		t.Fatalf("text upload picked %q, want upload-only", token)
	}
}

func TestTextUploadSelectionSkipsActiveRateLimitedAccount(t *testing.T) {
	cfg := Config{ImageAccountConcurrency: 3}
	pool := newAccountPool(&cfg)
	restoreAt := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	accounts := []Account{
		{AccessToken: "limited", Status: accountStatusLimited, RestoreAt: &restoreAt, RateLimitResetAt: &restoreAt, UploadQuota: 5},
		{AccessToken: "ready", Status: accountStatusNormal, UploadQuota: 1},
	}

	got, err := pool.pickTextTokenExcludingForUploads(accounts, "", nil, 1)
	if err != nil {
		t.Fatalf("pick text upload token: %v", err)
	}
	if got != "ready" {
		t.Fatalf("picked %q, want ready", got)
	}
}

func TestTextUploadSelectionAllowsImageLimitedAccountWithImageReset(t *testing.T) {
	cfg := Config{ImageAccountConcurrency: 3}
	pool := newAccountPool(&cfg)
	imageResetAt := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	accounts := []Account{
		{AccessToken: "upload-only", Status: accountStatusLimited, Quota: 0, ImageLimitResetAt: &imageResetAt, UploadQuota: 2},
	}

	got, err := pool.pickTextTokenExcludingForUploads(accounts, "", nil, 1)
	if err != nil {
		t.Fatalf("pick text upload token: %v", err)
	}
	if got != "upload-only" {
		t.Fatalf("picked %q, want upload-only", got)
	}
}

func TestTextUploadSelectionAllowsImageExhaustedLimitedAccountWithoutReset(t *testing.T) {
	cfg := Config{ImageAccountConcurrency: 3}
	pool := newAccountPool(&cfg)
	accounts := []Account{
		{AccessToken: "upload-only", Status: accountStatusLimited, Quota: 0, UploadQuota: 2},
	}

	got, err := pool.pickTextTokenExcludingForUploads(accounts, "", nil, 1)
	if err != nil {
		t.Fatalf("pick text upload token: %v", err)
	}
	if got != "upload-only" {
		t.Fatalf("picked %q, want upload-only", got)
	}
}

func TestNormalizeAccountLimitStateMigratesLegacyImageReset(t *testing.T) {
	resetAt := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	account := Account{
		AccessToken:       "legacy",
		Status:            accountStatusLimited,
		Quota:             0,
		ImageQuotaUnknown: false,
		RestoreAt:         &resetAt,
		RateLimitResetAt:  &resetAt,
		UploadQuota:       2,
	}

	normalizeAccountLimitState(&account)

	if account.ImageLimitResetAt == nil || *account.ImageLimitResetAt != resetAt {
		t.Fatalf("legacy image reset was not migrated: %#v", account)
	}
	if account.RestoreAt != nil || account.RateLimitResetAt != nil {
		t.Fatalf("legacy image reset should be removed from generic limit fields: %#v", account)
	}
}

func TestNormalizeAccountLimitStatePreservesGenericRateLimit(t *testing.T) {
	resetAt := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	limitedAt := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	account := Account{
		AccessToken:       "limited",
		Status:            accountStatusLimited,
		Quota:             0,
		ImageQuotaUnknown: false,
		RestoreAt:         &resetAt,
		RateLimitedAt:     &limitedAt,
		RateLimitResetAt:  &resetAt,
		UploadQuota:       2,
	}

	normalizeAccountLimitState(&account)

	if account.ImageLimitResetAt != nil {
		t.Fatalf("generic rate limit should not become image reset: %#v", account)
	}
	if account.RestoreAt == nil || account.RateLimitResetAt == nil {
		t.Fatalf("generic rate limit fields should be preserved: %#v", account)
	}
}

func TestImageSuccessKeepsUploadCapableAccountWhenImageQuotaReachesZero(t *testing.T) {
	s := newImageAccountGuardTestServer(t)
	if err := s.store.SaveAccounts([]Account{{
		AccessToken:        "tok",
		Status:             accountStatusNormal,
		Quota:              1,
		UploadQuota:        2,
		UploadQuotaUnknown: false,
	}}); err != nil {
		t.Fatalf("save accounts: %v", err)
	}

	s.markAccountSuccess("tok", true)

	got := s.store.LoadAccounts()
	if len(got) != 1 {
		t.Fatalf("upload-capable exhausted image account should remain, got %#v", got)
	}
	if got[0].Quota != 0 || got[0].Status != accountStatusLimited {
		t.Fatalf("image quota state after success = %#v, want limited zero quota", got[0])
	}
	if got[0].UploadQuota != 2 || got[0].UploadQuotaUnknown {
		t.Fatalf("upload quota should be preserved: %#v", got[0])
	}
	if _, err := s.accountPool.pickTokenExcludingForUploads(got, false, "", nil, 1); err == nil || err.Error() != "no available image quota" {
		t.Fatalf("image pick err = %v, want image quota rejection", err)
	}
	token, err := s.accountPool.pickTextTokenExcludingForUploads(got, "", nil, 1)
	if err != nil {
		t.Fatalf("text upload pick: %v", err)
	}
	if token != "tok" {
		t.Fatalf("text upload picked %q, want tok", token)
	}
}

func TestTextUploadQuotaReservationBlocksConcurrentOverselect(t *testing.T) {
	cfg := Config{ImageAccountConcurrency: 3}
	pool := newAccountPool(&cfg)
	accounts := []Account{
		{AccessToken: "tok", Status: accountStatusNormal, UploadQuota: 1},
	}

	got, err := pool.pickTextTokenExcludingForUploads(accounts, "", nil, 1)
	if err != nil {
		t.Fatalf("first text pick: %v", err)
	}
	if got != "tok" {
		t.Fatalf("first text pick = %q, want tok", got)
	}
	if _, err := pool.pickTextTokenExcludingForUploads(accounts, "", nil, 1); err == nil || err.Error() != "no available image upload quota" {
		t.Fatalf("second text pick err = %v, want upload quota error", err)
	}
}

func TestPureTextUploadSelectionReleaseDoesNotTouchImageInflight(t *testing.T) {
	s := newImageAccountGuardTestServer(t)
	s.accountPool.inflight["tok"] = 1

	s.releaseTextUploadSelection(context.Background(), "tok", "", 0)

	if got := s.accountPool.activeCount("tok"); got != 1 {
		t.Fatalf("image inflight after pure text release = %d, want 1", got)
	}
}

func TestUploadQuotaZeroDoesNotBlockTextToImageSelection(t *testing.T) {
	cfg := Config{ImageAccountConcurrency: 3}
	pool := newAccountPool(&cfg)
	accounts := []Account{
		{AccessToken: "text-only", Status: accountStatusNormal, Quota: 10, UploadQuota: 0},
	}

	got, err := pool.pickTokenExcludingForUploads(accounts, false, "", nil, 0)
	if err != nil {
		t.Fatalf("pick token: %v", err)
	}
	if got != "text-only" {
		t.Fatalf("picked %q, want text-only", got)
	}
}

func TestAcquireUploadAccountLeaseRejectsUnknownQuota(t *testing.T) {
	s := &Server{
		cfg:       Config{ImageAccountConcurrency: 3},
		taskStore: &PGTaskStore{},
	}
	_, leased, err := s.acquireUploadAccountLease(context.Background(), "tok", Account{AccessToken: "tok", UploadQuotaUnknown: true}, 1, "test", time.Minute)
	if err == nil || !errors.Is(err, errImageUploadQuotaReserved) {
		t.Fatalf("lease err = %v, want upload quota reserved error", err)
	}
	if leased {
		t.Fatal("unknown upload quota should not acquire lease")
	}
}

func TestUploadLimitFailureDoesNotClearImageQuota(t *testing.T) {
	s := newImageAccountGuardTestServer(t)
	if err := s.store.SaveAccounts([]Account{{
		AccessToken:        "tok",
		Type:               "free",
		Status:             accountStatusNormal,
		Quota:              25,
		UploadQuota:        1,
		UploadQuotaUnknown: false,
	}}); err != nil {
		t.Fatalf("save account: %v", err)
	}

	s.markAccountFailure("tok", errors.New("POST /backend-api/files failed: status=429 body=You have reached your file upload limit"), true)

	got := s.store.LoadAccounts()
	if len(got) != 1 {
		t.Fatalf("accounts = %#v, want one account", got)
	}
	if got[0].Quota != 25 || got[0].Status != accountStatusNormal {
		t.Fatalf("image quota/status changed after upload limit: %#v", got[0])
	}
	if got[0].UploadQuota != 0 || got[0].UploadQuotaUnknown {
		t.Fatalf("upload quota not marked exhausted: %#v", got[0])
	}
	if got[0].UploadLimitResetAt == nil || got[0].UploadLimitFeatureName == nil {
		t.Fatalf("upload reset metadata missing: %#v", got[0])
	}
}

func TestGenericRateLimitClearsStaleImageReset(t *testing.T) {
	s := newImageAccountGuardTestServer(t)
	imageResetAt := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	if err := s.store.SaveAccounts([]Account{{
		AccessToken:        "tok",
		Type:               "free",
		Status:             accountStatusNormal,
		Quota:              25,
		ImageLimitResetAt:  &imageResetAt,
		UploadQuota:        2,
		UploadQuotaUnknown: false,
	}}); err != nil {
		t.Fatalf("save account: %v", err)
	}

	s.markAccountFailure("tok", errors.New("status=429 body=too many requests"), false)

	got := s.store.LoadAccounts()
	if len(got) != 1 {
		t.Fatalf("accounts = %#v, want one account", got)
	}
	if got[0].Status != accountStatusLimited {
		t.Fatalf("generic rate limit status = %q, want limited", got[0].Status)
	}
	if got[0].ImageLimitResetAt != nil {
		t.Fatalf("stale image reset should be cleared on generic rate limit: %#v", got[0])
	}
	if got[0].RestoreAt == nil || got[0].RateLimitResetAt == nil || got[0].RateLimitedAt == nil {
		t.Fatalf("generic rate limit metadata missing: %#v", got[0])
	}
	if got[0].UploadQuota != 2 || got[0].UploadQuotaUnknown {
		t.Fatalf("upload quota should be preserved on generic rate limit: %#v", got[0])
	}
}

func TestTextUploadLimitFailureMarksUploadQuota(t *testing.T) {
	s := newImageAccountGuardTestServer(t)
	if err := s.store.SaveAccounts([]Account{{
		AccessToken:        "tok",
		Type:               "free",
		Status:             accountStatusNormal,
		Quota:              25,
		UploadQuota:        1,
		UploadQuotaUnknown: false,
	}}); err != nil {
		t.Fatalf("save account: %v", err)
	}

	s.markAccountFailure("tok", errors.New("POST /backend-api/files failed: status=429 body=You have reached your file upload limit"), false)

	got := s.store.LoadAccounts()
	if len(got) != 1 {
		t.Fatalf("accounts = %#v, want one account", got)
	}
	if got[0].Quota != 25 || got[0].Status != accountStatusNormal {
		t.Fatalf("image quota/status changed after text upload limit: %#v", got[0])
	}
	if got[0].UploadQuota != 0 || got[0].UploadQuotaUnknown {
		t.Fatalf("upload quota not marked exhausted: %#v", got[0])
	}
}

func TestUploadSuccessExhaustsKnownQuotaWithResetMetadata(t *testing.T) {
	s := newImageAccountGuardTestServer(t)
	if err := s.store.SaveAccounts([]Account{{
		AccessToken:        "tok",
		Type:               "free",
		Status:             accountStatusNormal,
		Quota:              25,
		UploadQuota:        1,
		UploadQuotaUnknown: false,
	}}); err != nil {
		t.Fatalf("save account: %v", err)
	}

	s.markAccountUploadSuccess("tok", 1)

	got := s.store.LoadAccounts()
	if len(got) != 1 {
		t.Fatalf("accounts = %#v, want one account", got)
	}
	if got[0].UploadQuota != 0 || got[0].UploadQuotaUnknown {
		t.Fatalf("upload quota state after exhaustion = %#v, want known zero", got[0])
	}
	if got[0].UploadLimitResetAt == nil || got[0].UploadLimitFeatureName == nil {
		t.Fatalf("upload exhaustion metadata missing: %#v", got[0])
	}
}

func TestUploadLimitRestoreDelayPreservesParsedFiveMinutes(t *testing.T) {
	err := errors.New("POST /backend-api/files failed: status=429 body=try again in 5 minutes")
	if got := uploadLimitRestoreDelay(err, "free"); got != 5*time.Minute {
		t.Fatalf("upload restore delay = %s, want 5m", got)
	}
}

func TestUploadLimitRestoreDelayUsesPlanFallbacks(t *testing.T) {
	err := errors.New("POST /backend-api/files failed: status=429 body=file upload limit reached")
	if got := uploadLimitRestoreDelay(err, "free"); got != 24*time.Hour {
		t.Fatalf("free upload fallback restore delay = %s, want 24h", got)
	}
	if got := uploadLimitRestoreDelay(err, "Plus"); got != 3*time.Hour {
		t.Fatalf("paid upload fallback restore delay = %s, want 3h", got)
	}
	if got := uploadLimitRestoreDelay(err, "unknown"); got != 24*time.Hour {
		t.Fatalf("unknown upload fallback restore delay = %s, want conservative 24h", got)
	}
}

func TestUploadLimitFailureUsesPaidFallbackResetWindow(t *testing.T) {
	s := newImageAccountGuardTestServer(t)
	if err := s.store.SaveAccounts([]Account{{
		AccessToken:        "tok",
		Type:               "Plus",
		Status:             accountStatusNormal,
		Quota:              25,
		UploadQuota:        1,
		UploadQuotaUnknown: false,
	}}); err != nil {
		t.Fatalf("save account: %v", err)
	}

	before := time.Now().UTC()
	s.markAccountFailure("tok", errors.New("POST /backend-api/files failed: status=429 body=You have reached your file upload limit"), true)

	got := s.store.LoadAccounts()
	if len(got) != 1 {
		t.Fatalf("accounts = %#v, want one account", got)
	}
	assertUploadResetDelay(t, got[0].UploadLimitResetAt, before, 3*time.Hour)
}

func assertUploadResetDelay(t *testing.T, resetAt *string, before time.Time, want time.Duration) {
	t.Helper()
	if resetAt == nil {
		t.Fatal("upload reset is nil")
	}
	rt, err := parseAccountTime(*resetAt)
	if err != nil {
		t.Fatalf("parse upload reset %q: %v", *resetAt, err)
	}
	got := rt.Sub(before)
	if got < want-2*time.Second || got > want+2*time.Second {
		t.Fatalf("upload reset delay = %s, want about %s", got, want)
	}
}

func TestUploadLimitErrorTextRequiresUploadContext(t *testing.T) {
	if isUploadLimitErrorText(errors.New("status=429 body=rate limit reached")) {
		t.Fatal("plain rate limit without upload context should not be upload limit")
	}
	if !isUploadLimitErrorText(errors.New("POST /backend-api/files failed: status=429 body=file upload limit reached")) {
		t.Fatal("file upload limit should be detected")
	}
	if !isUploadLimitErrorText(errors.New("POST /backend-api/files/file-123/uploaded failed: status=400 body=file uploads exceeded")) {
		t.Fatal("uploaded endpoint file upload limit should be detected")
	}
	if isUploadLimitErrorText(errors.New("GET /backend-api/files/file-123/download failed: status=429 body=rate limit reached")) {
		t.Fatal("file download 429 should not be treated as account upload quota")
	}
	if isUploadLimitErrorText(errors.New("image upload failed: status=429 body=too many requests")) {
		t.Fatal("object storage 429 should not be treated as account upload quota")
	}
	if isUploadLimitErrorText(errors.New("image upload failed: status=500 body=storage unavailable")) {
		t.Fatal("plain storage upload failure should not be treated as upload quota")
	}
}

func TestMergeRefreshedAccountInfoPreservesImageQuotaWhenOnlyUploadQuotaKnown(t *testing.T) {
	restoreAt := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	rateLimitedAt := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	existing := Account{
		AccessToken:        "tok",
		Status:             accountStatusLimited,
		Quota:              25,
		InitialQuota:       25,
		ImageQuotaUnknown:  false,
		LimitsProgress:     []map[string]any{{"feature_name": "image_gen", "remaining": 25}},
		ImageLimitResetAt:  &restoreAt,
		RateLimitedAt:      &rateLimitedAt,
		UploadQuota:        0,
		UploadQuotaUnknown: true,
	}
	refreshed := Account{
		AccessToken:        "tok",
		Status:             accountStatusNormal,
		Quota:              0,
		ImageQuotaUnknown:  true,
		UploadQuota:        3,
		UploadQuotaUnknown: false,
		LimitsProgress:     []map[string]any{{"feature_name": "file_uploads", "remaining": 3}},
	}

	mergeRefreshedAccountInfo(&existing, refreshed)

	if existing.Quota != 25 || existing.ImageQuotaUnknown || len(existing.LimitsProgress) != 1 {
		t.Fatalf("image quota should be preserved on upload-only refresh: %#v", existing)
	}
	if existing.Status != accountStatusLimited || existing.ImageLimitResetAt == nil || *existing.ImageLimitResetAt != restoreAt || existing.RestoreAt != nil || existing.RateLimitResetAt != nil {
		t.Fatalf("image limit state should be preserved on upload-only refresh: %#v", existing)
	}
	if existing.UploadQuota != 3 || existing.UploadQuotaUnknown {
		t.Fatalf("upload quota should be refreshed: %#v", existing)
	}
}

func TestMergeRefreshedAccountInfoPreservesUploadQuotaWithoutUploadSignal(t *testing.T) {
	resetAt := "2026-07-08T00:00:00Z"
	featureName := "file_uploads"
	existing := Account{
		AccessToken:              "tok",
		Status:                   accountStatusNormal,
		Quota:                    25,
		ImageQuotaUnknown:        false,
		UploadQuota:              3,
		UploadQuotaUnknown:       false,
		UploadLimitResetAt:       &resetAt,
		UploadLimitFeatureName:   &featureName,
		RefreshValidationPending: true,
	}
	refreshed := Account{
		AccessToken:        "tok",
		Status:             accountStatusNormal,
		Quota:              25,
		ImageQuotaUnknown:  false,
		UploadQuota:        0,
		UploadQuotaUnknown: true,
		LimitsProgress:     []map[string]any{{"feature_name": "image_gen", "remaining": 25}},
	}

	mergeRefreshedAccountInfo(&existing, refreshed)

	if existing.UploadQuota != 3 || existing.UploadQuotaUnknown || existing.UploadLimitResetAt == nil || existing.UploadLimitFeatureName == nil {
		t.Fatalf("upload quota should be preserved without upload quota signal: %#v", existing)
	}
	if existing.RefreshValidationPending {
		t.Fatalf("refresh validation should be cleared: %#v", existing)
	}
}

func TestMergeRefreshedAccountInfoPreservesUploadQuotaOnEmptyRefreshAccount(t *testing.T) {
	existing := Account{
		AccessToken:        "tok",
		Status:             accountStatusNormal,
		Quota:              25,
		ImageQuotaUnknown:  false,
		UploadQuota:        2,
		UploadQuotaUnknown: false,
		LimitsProgress:     []map[string]any{{"feature_name": "image_gen", "remaining": 25}},
	}

	mergeRefreshedAccountInfo(&existing, Account{AccessToken: "tok"})

	if existing.UploadQuota != 2 || existing.UploadQuotaUnknown {
		t.Fatalf("empty refresh account should not overwrite upload quota: %#v", existing)
	}
}

func TestMergeRefreshedAccountInfoOverridesExplicitEmptyUploadQuotaFromLimits(t *testing.T) {
	existing := Account{
		AccessToken:        "tok",
		Status:             accountStatusNormal,
		Quota:              25,
		ImageQuotaUnknown:  false,
		UploadQuota:        2,
		UploadQuotaUnknown: false,
	}
	refreshed := Account{
		AccessToken:        "tok",
		Status:             accountStatusNormal,
		Quota:              25,
		ImageQuotaUnknown:  false,
		UploadQuota:        0,
		UploadQuotaUnknown: false,
		LimitsProgress:     []map[string]any{{"feature_name": "file_uploads", "remaining": 0}},
	}

	mergeRefreshedAccountInfo(&existing, refreshed)

	if existing.UploadQuota != 0 || existing.UploadQuotaUnknown {
		t.Fatalf("explicit empty upload quota should overwrite previous quota: %#v", existing)
	}
}

func TestReimportPreservesUploadQuotaWhenImageQuotaUnavailable(t *testing.T) {
	s := newImageAccountGuardTestServer(t)
	resetAt := "2026-07-08T00:00:00Z"
	featureName := "file_uploads"
	if err := s.store.SaveAccounts([]Account{{
		AccessToken:            "tok",
		Status:                 accountStatusNormal,
		Quota:                  0,
		ImageQuotaUnknown:      true,
		UploadQuota:            2,
		UploadQuotaUnknown:     false,
		UploadLimitResetAt:     &resetAt,
		UploadLimitFeatureName: &featureName,
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
	if got[0].UploadQuota != 2 || got[0].UploadQuotaUnknown || got[0].UploadLimitResetAt == nil || *got[0].UploadLimitResetAt != resetAt || got[0].UploadLimitFeatureName == nil || *got[0].UploadLimitFeatureName != featureName {
		t.Fatalf("upload quota runtime state should survive reimport independently from image quota: %#v", got[0])
	}
	if !got[0].ImageQuotaUnknown || !got[0].RefreshValidationPending || got[0].Quota != 0 {
		t.Fatalf("image quota should still wait for refresh validation: %#v", got[0])
	}
	if got[0].Email == nil || *got[0].Email != "updated@example.com" {
		t.Fatalf("account metadata should still be updated, got %#v", got[0].Email)
	}
}

func TestCountUploadImagesInMessagesMatchesConversationUploadRoles(t *testing.T) {
	img := "data:image/png;base64,aW1hZ2U="
	messages := []map[string]any{
		{"role": "user", "content": []any{map[string]any{"type": "image_url", "image_url": map[string]any{"url": img}}}},
		{"role": "assistant", "content": []any{map[string]any{"type": "image_url", "image_url": map[string]any{"url": img}}}},
		{"role": "system", "content": []any{map[string]any{"type": "image_url", "image_url": map[string]any{"url": img}}}},
		{"role": "tool", "content": []any{map[string]any{"type": "image_url", "image_url": map[string]any{"url": img}}}},
	}

	if got := countUploadImagesInMessages(messages); got != 2 {
		t.Fatalf("upload image count = %d, want 2", got)
	}
}

func TestTextUploadSelectionReleasesUploadInflight(t *testing.T) {
	cfg := Config{ImageAccountConcurrency: 3}
	pool := newAccountPool(&cfg)
	accounts := []Account{{AccessToken: "tok", Status: accountStatusNormal, UploadQuota: 2}}
	if _, err := pool.pickTextTokenExcludingForUploads(accounts, "", nil, 1); err != nil {
		t.Fatalf("pick text upload token: %v", err)
	}
	if got := pool.activeCount("tok"); got != 1 {
		t.Fatalf("active after text upload pick = %d, want 1", got)
	}
	pool.releaseToken("tok")
	pool.releaseUploadReservation("tok", 1)
	if got := pool.activeCount("tok"); got != 0 {
		t.Fatalf("active after text upload release = %d, want 0", got)
	}
	if _, err := pool.pickTextTokenExcludingForUploads(accounts, "", nil, 2); err != nil {
		t.Fatalf("pick after text upload release: %v", err)
	}
}
