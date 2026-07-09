package app

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeImageModelAliasesToPublicModel(t *testing.T) {
	for _, model := range []string{"", "auto", "gpt-image-2", "gpt-image-1", "dall-e-3", "dall-e-2", "codex-gpt-image-2", "plus-codex-gpt-image-2"} {
		if got := normalizeImageModel(model); got != publicImageModel {
			t.Fatalf("normalizeImageModel(%q) = %q, want %q", model, got, publicImageModel)
		}
	}
}

func TestImageRoutePlanDefaultsToWebFirst(t *testing.T) {
	cases := []struct {
		strategy string
		want     []string
	}{
		{"", []string{imageRouteWeb, imageRouteCodex}},
		{"auto", []string{imageRouteWeb, imageRouteCodex}},
		{"web_first", []string{imageRouteWeb, imageRouteCodex}},
		{"web_only", []string{imageRouteWeb}},
		{"codex_first", []string{imageRouteCodex, imageRouteWeb}},
		{"codex_only", []string{imageRouteCodex}},
	}
	for _, tc := range cases {
		s := &Server{cfg: Config{ImageRouteStrategy: tc.strategy}}
		got := s.imageRoutePlan()
		if len(got) != len(tc.want) {
			t.Fatalf("strategy %q routes = %#v, want %#v", tc.strategy, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("strategy %q routes = %#v, want %#v", tc.strategy, got, tc.want)
			}
		}
	}
}

func TestImageRoutePlanForIdentityRespectsPaidRoutePermission(t *testing.T) {
	freeIdentity := &Identity{ID: "free-user", Role: "user", AccountTier: "free"}
	premiumIdentity := &Identity{ID: "premium-user", Role: "user", AccountTier: "premium", CanUsePaidImageAccounts: true}

	webFirst := &Server{cfg: Config{ImageRouteStrategy: "web_first"}}
	freeRoutes, err := webFirst.imageRoutePlanForIdentity(freeIdentity)
	if err != nil {
		t.Fatalf("free web_first route plan returned error: %v", err)
	}
	if want := []string{imageRouteWeb}; !reflect.DeepEqual(freeRoutes, want) {
		t.Fatalf("free web_first route plan = %#v, want %#v", freeRoutes, want)
	}
	premiumRoutes, err := webFirst.imageRoutePlanForIdentity(premiumIdentity)
	if err != nil {
		t.Fatalf("premium web_first route plan returned error: %v", err)
	}
	if want := []string{imageRouteWeb, imageRouteCodex}; !reflect.DeepEqual(premiumRoutes, want) {
		t.Fatalf("premium web_first route plan = %#v, want %#v", premiumRoutes, want)
	}

	for _, strategy := range []string{"codex_first", "codex_only"} {
		s := &Server{cfg: Config{ImageRouteStrategy: strategy}}
		err := s.checkImageAccess(freeIdentity, publicImageModel, "", "")
		var se statusError
		if !errors.As(err, &se) || se.status != 403 {
			t.Fatalf("free %s access error = %#v, want 403 statusError", strategy, err)
		}
	}
}

func TestImageAccessRejectsHighResolutionDirectSizeForFreeIdentity(t *testing.T) {
	s := &Server{cfg: Config{ImageRouteStrategy: "web_first"}}
	freeIdentity := &Identity{ID: "free-user", Role: "user", AccountTier: "free"}
	for _, size := range []string{"2048x2048", "3840x2160", "2160×3840", "2k", "4k"} {
		err := s.checkImageAccess(freeIdentity, publicImageModel, size, "")
		var se statusError
		if !errors.As(err, &se) || se.status != 403 {
			t.Fatalf("size %q access error = %#v, want 403 statusError", size, err)
		}
	}
	if err := s.checkImageAccess(freeIdentity, publicImageModel, "", "2560x1440"); err == nil {
		t.Fatal("direct high-resolution resolution should be rejected for free identity")
	}
	if err := s.checkImageAccess(freeIdentity, publicImageModel, "1536x1024", ""); err != nil {
		t.Fatalf("1k-sized direct request should stay allowed, got %v", err)
	}
}

func TestGenerateImageWithPoolForIdentityUsesImageGenerator(t *testing.T) {
	s := &Server{}
	called := false
	s.imageGenerator = func(ctx context.Context, prompt, model, size, resolution string, refs [][]byte, n int) ([]upstreamImageResult, error) {
		called = true
		if prompt != "prompt" || model != publicImageModel || size != "1:1" || resolution != "" || n != 1 {
			t.Fatalf("imageGenerator args prompt=%q model=%q size=%q resolution=%q n=%d", prompt, model, size, resolution, n)
		}
		return []upstreamImageResult{{Bytes: []byte("ok")}}, nil
	}
	items, err := s.generateImageWithPoolForIdentity(context.Background(), &Identity{ID: "premium-user", CanUsePaidImageAccounts: true}, "prompt", publicImageModel, "1:1", "", nil)
	if err != nil {
		t.Fatalf("generate image with identity: %v", err)
	}
	if !called {
		t.Fatal("imageGenerator was not called")
	}
	if len(items) != 1 || string(items[0].Bytes) != "ok" {
		t.Fatalf("items = %#v, want generated image", items)
	}
}

func TestTaskOwnerIdentityLoadsCurrentPaidImagePermission(t *testing.T) {
	s := &Server{store: NewStore(t.TempDir())}
	if err := s.store.SaveAuthKeys([]UserKey{{
		ID:          "premium-user",
		Role:        "user",
		AccountTier: "premium",
		Enabled:     true,
	}}); err != nil {
		t.Fatalf("save auth keys: %v", err)
	}

	id := s.taskOwnerIdentity("premium-user", "user")
	if id.Role != "user" || id.AccountTier != "premium" || !id.CanUsePaidImageAccounts {
		t.Fatalf("task owner identity = %#v, want current premium user permissions", id)
	}

	missing := s.taskOwnerIdentity("missing-user", "")
	if missing.Role != "user" || missing.CanUsePaidImageAccounts {
		t.Fatalf("missing task owner identity = %#v, want safe free user fallback", missing)
	}

	if err := s.store.SaveAuthKeys([]UserKey{{
		ID:          "admin-key",
		Role:        "user",
		AccountTier: "free",
		Enabled:     true,
	}}); err != nil {
		t.Fatalf("save downgraded admin auth key: %v", err)
	}
	downgraded := s.taskOwnerIdentity("admin-key", "admin")
	if downgraded.Role != "user" || downgraded.AccountTier != "free" || downgraded.CanUsePaidImageAccounts {
		t.Fatalf("downgraded task owner identity = %#v, want current free user permissions", downgraded)
	}
	if err := s.store.SaveAuthKeys([]UserKey{{
		ID:          "disabled-premium",
		Role:        "admin",
		AccountTier: "premium",
		Enabled:     false,
	}}); err != nil {
		t.Fatalf("save disabled premium auth key: %v", err)
	}
	disabled := s.taskOwnerIdentity("disabled-premium", "admin")
	if disabled.Role != "user" || disabled.AccountTier != "free" || disabled.CanUsePaidImageAccounts || disabled.CanUseHighResolution {
		t.Fatalf("disabled task owner identity = %#v, want safe free user fallback", disabled)
	}
	missingAdmin := s.taskOwnerIdentity("missing-admin-key", "admin")
	if missingAdmin.Role != "user" || missingAdmin.CanUsePaidImageAccounts {
		t.Fatalf("missing admin-key task owner identity = %#v, want safe free user fallback", missingAdmin)
	}
	root := s.taskOwnerIdentity("admin", "admin")
	if !root.Root || !root.CanUsePaidImageAccounts || !root.CanUseHighResolution {
		t.Fatalf("root task owner identity = %#v, want root admin fallback only for owner admin", root)
	}
}

func TestCodexRouteNoLongerTriggeredByResolution(t *testing.T) {
	if isCodexImageRequest("gpt-image-2", "2k") {
		t.Fatal("gpt-image-2 2k should stay on the configured route instead of forcing Codex")
	}
	if !isCodexImageRequest("codex-gpt-image-2", "") {
		t.Fatal("internal codex route model should use Codex")
	}
}

func TestBuildEnhancedImagePromptAddsUltraClearAndReferenceGuidance(t *testing.T) {
	got := buildEnhancedImagePrompt("画一张产品主图", "1:1", "2k", true)
	for _, want := range []string{
		"画一张产品主图",
		"ultra high definition",
		"2048px",
		"Use every uploaded reference image",
		"not garbled",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("enhanced prompt missing %q:\n%s", want, got)
		}
	}
}

func TestResponseImageOptionsPreserveSizeAndResolution(t *testing.T) {
	topLevel := map[string]any{
		"size":       "16:9",
		"resolution": "4k",
		"tools": []any{map[string]any{
			"type":       "image_generation",
			"size":       "1:1",
			"resolution": "2k",
		}},
	}
	size, resolution := responseImageOptions(topLevel, false)
	if size != "16:9" || resolution != "4k" {
		t.Fatalf("top-level response image options = size %q resolution %q, want 16:9 4k", size, resolution)
	}

	toolLevel := map[string]any{
		"tools": []any{map[string]any{
			"type":       "image_generation",
			"size":       "9:16",
			"resolution": "2k",
		}},
	}
	size, resolution = responseImageOptions(toolLevel, true)
	if size != "9:16" || resolution != "2k" {
		t.Fatalf("tool-level response image options = size %q resolution %q, want 9:16 2k", size, resolution)
	}

	size, resolution = responseImageOptions(map[string]any{}, false)
	if size != "1:1" || resolution != "" {
		t.Fatalf("default generation response image options = size %q resolution %q, want 1:1 empty", size, resolution)
	}

	size, resolution = responseImageOptions(map[string]any{}, true)
	if size != "" || resolution != "" {
		t.Fatalf("default edit response image options = size %q resolution %q, want empty empty", size, resolution)
	}
}

func TestPublicImageModelListOnlyExposesGPTImage2(t *testing.T) {
	s := &Server{store: NewStore(t.TempDir())}
	if err := s.store.SaveAccounts([]Account{
		{AccessToken: "web", SourceType: "web", Type: "free", Status: accountStatusNormal, Quota: 10},
		{AccessToken: "codex", SourceType: "codex", Type: "plus", Status: accountStatusNormal, Quota: 10},
	}); err != nil {
		t.Fatalf("save accounts: %v", err)
	}
	got := s.imageModelIDs()
	if len(got) != 1 || got[0] != publicImageModel {
		t.Fatalf("imageModelIDs() = %#v, want only %q", got, publicImageModel)
	}
}

func TestWebRouteAccountSelectionExcludesCodexAccounts(t *testing.T) {
	s := &Server{cfg: Config{ImageAccountConcurrency: 3}, store: NewStore(t.TempDir()), accountPool: newAccountPool(&Config{ImageAccountConcurrency: 3})}
	if err := s.store.SaveAccounts([]Account{
		{AccessToken: "codex", SourceType: "codex", Type: "plus", Status: accountStatusNormal, Quota: 100},
		{AccessToken: "web", SourceType: "web", Type: "free", Status: accountStatusNormal, Quota: 1},
	}); err != nil {
		t.Fatalf("save accounts: %v", err)
	}
	account, err := s.pickAccountExcluding(publicImageModel, "", nil)
	if err != nil {
		t.Fatalf("pick web account: %v", err)
	}
	if account.AccessToken != "web" {
		t.Fatalf("picked account = %q, want web account", account.AccessToken)
	}
}

func TestPreferPreviousImageRouteErrorKeepsWebFailureWhenCodexHasNoAccount(t *testing.T) {
	webErr := routeErrString("POST /backend-api/sentinel/chat-requirements failed: status=401")
	codexErr := routeErrString("no available codex Plus/Team/Pro account")

	if got := preferPreviousImageRouteError(webErr, codexErr); got != webErr {
		t.Fatalf("preferred error = %v, want original web error", got)
	}
	if got := preferPreviousImageRouteError(nil, codexErr); got != codexErr {
		t.Fatalf("preferred error without previous = %v, want codex error", got)
	}
	nextErr := routeErrString("upstream status=503")
	if got := preferPreviousImageRouteError(codexErr, nextErr); got != nextErr {
		t.Fatalf("preferred non-codex error = %v, want current error", got)
	}
}

func TestPotentialCodexImageAccountRequiresUsablePremiumCodexAccount(t *testing.T) {
	s := &Server{store: NewStore(t.TempDir())}
	if err := s.store.SaveAccounts([]Account{
		{AccessToken: "web-plus", SourceType: "web", Type: "plus", Status: accountStatusNormal, Quota: 10},
		{AccessToken: "codex-free", SourceType: "codex", Type: "free", Status: accountStatusNormal, Quota: 10},
		{AccessToken: "codex-plus-no-refresh", SourceType: "codex", Type: "plus", Status: accountStatusNormal, Quota: 10},
	}); err != nil {
		t.Fatalf("save accounts: %v", err)
	}
	if s.hasPotentialCodexImageAccount() {
		t.Fatal("web, free codex, or codex without refresh_token accounts should not satisfy codex Plus/Team/Pro image fallback")
	}

	refreshToken := "refresh-token"
	if err := s.store.SaveAccounts([]Account{
		{AccessToken: "codex-plus", SourceType: "codex", Type: "plus", Status: accountStatusNormal, Quota: 10, RefreshToken: &refreshToken},
	}); err != nil {
		t.Fatalf("save accounts: %v", err)
	}
	if !s.hasPotentialCodexImageAccount() {
		t.Fatal("usable codex plus account should satisfy codex fallback")
	}
}

type routeErrString string

func (e routeErrString) Error() string { return string(e) }
