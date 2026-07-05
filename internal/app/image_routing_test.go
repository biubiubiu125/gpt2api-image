package app

import (
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
