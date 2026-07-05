package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeImageResponseFormat(t *testing.T) {
	if got := normalizeImageResponseFormat(""); got != "b64_json" {
		t.Fatalf("empty response_format = %q, want b64_json", got)
	}
	if got := normalizeImageResponseFormat("url"); got != "url" {
		t.Fatalf("url response_format = %q, want url", got)
	}
	if got := normalizeImageResponseFormat("b64_json"); got != "b64_json" {
		t.Fatalf("b64_json response_format = %q, want b64_json", got)
	}
}

func TestImageTaskIDNamespacedByOwner(t *testing.T) {
	clientID := "client-task-1"
	a := imageTaskID("user-a", clientID)
	b := imageTaskID("user-b", clientID)
	if a == b {
		t.Fatalf("task ids should be owner-scoped: %q", a)
	}
	if imageTaskID("user-a", clientID) != a {
		t.Fatalf("task id should be stable for the same owner/client id")
	}
}

func TestPossibleImageTaskIDsIncludesClientAndDerivedID(t *testing.T) {
	ids := possibleImageTaskIDs("user-a", "client-task-1")
	if len(ids) != 2 {
		t.Fatalf("possible ids len = %d, want 2", len(ids))
	}
	if ids[0] != "client-task-1" {
		t.Fatalf("first id = %q, want original client id", ids[0])
	}
	if ids[1] != imageTaskID("user-a", "client-task-1") {
		t.Fatalf("derived id = %q, want %q", ids[1], imageTaskID("user-a", "client-task-1"))
	}
}

func TestImageTaskIDLengthBounded(t *testing.T) {
	clientID := strings.Repeat("client-task-", 40)
	id := imageTaskID("user-a", clientID)
	maxLen := len("usr_") + 12 + 1 + maxSafeFileNameLen
	if len(id) > maxLen {
		t.Fatalf("task id len = %d, want <= %d", len(id), maxLen)
	}
	if imageTaskID("user-a", clientID) != id {
		t.Fatalf("bounded task id should still be stable")
	}
}

func TestImageTaskIDStableForUnicodeClientID(t *testing.T) {
	clientID := "任务一号"
	first := imageTaskID("user-a", clientID)
	second := imageTaskID("user-a", clientID)
	if first != second {
		t.Fatalf("unicode client task id should be stable: %q != %q", first, second)
	}
	if !strings.Contains(first, "id_") {
		t.Fatalf("unicode client task id should use deterministic hash suffix: %q", first)
	}
	ids := possibleImageTaskIDs("user-a", clientID)
	if len(ids) != 2 || ids[1] != first {
		t.Fatalf("possible ids = %#v, want original plus stable derived %q", ids, first)
	}
}

func TestSaveTaskInputImagesUsesUniquePaths(t *testing.T) {
	dir := t.TempDir()
	s := &Server{dataDir: dir}
	first, err := s.saveTaskInputImages("same-task", [][]byte{[]byte("first")})
	if err != nil {
		t.Fatalf("save first inputs: %v", err)
	}
	second, err := s.saveTaskInputImages("same-task", [][]byte{[]byte("second")})
	if err != nil {
		t.Fatalf("save second inputs: %v", err)
	}
	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("input path counts = %d/%d, want 1/1", len(first), len(second))
	}
	if first[0] == second[0] {
		t.Fatalf("duplicate task input paths should not collide: %q", first[0])
	}
	gotFirst, _ := os.ReadFile(first[0])
	gotSecond, _ := os.ReadFile(second[0])
	if string(gotFirst) != "first" || string(gotSecond) != "second" {
		t.Fatalf("input files were overwritten: first=%q second=%q", gotFirst, gotSecond)
	}
}

func TestCleanupTaskInputPathsStaysUnderTaskInputs(t *testing.T) {
	root := t.TempDir()
	s := &Server{dataDir: filepath.Join(root, "data")}
	base := filepath.Join(s.dataDir, "task_inputs")
	insideDir := filepath.Join(s.dataDir, "task_inputs", "task-1")
	if err := os.MkdirAll(insideDir, 0755); err != nil {
		t.Fatalf("make task input dir: %v", err)
	}
	inside := filepath.Join(insideDir, "image.bin")
	outside := filepath.Join(root, "outside.bin")
	if err := os.WriteFile(inside, []byte("inside"), 0644); err != nil {
		t.Fatalf("write inside input: %v", err)
	}
	if err := os.WriteFile(outside, []byte("outside"), 0644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}

	s.cleanupTaskInputPaths([]string{inside, outside, base})

	if _, err := os.Stat(inside); !os.IsNotExist(err) {
		t.Fatalf("inside task input should be removed, stat err=%v", err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("outside file should not be touched: %v", err)
	}
	if _, err := os.Stat(base); err != nil {
		t.Fatalf("task input root should not be touched: %v", err)
	}
}

func TestAdminClientTaskFallbackRequiresOwner(t *testing.T) {
	store := &PGTaskStore{}
	admin := Identity{ID: "admin", Role: "admin"}
	if _, err := store.TasksByClientTaskID(context.Background(), "client-task-1", admin); !errors.Is(err, errAdminClientTaskOwnerRequired) {
		t.Fatalf("TasksByClientTaskID err = %v, want owner-required", err)
	}
	if _, err := store.CancelTasksByClientTaskID(context.Background(), "client-task-1", admin); !errors.Is(err, errAdminClientTaskOwnerRequired) {
		t.Fatalf("CancelTasksByClientTaskID err = %v, want owner-required", err)
	}
}

func TestScopedTaskIdentityUsesOwnerForAdmin(t *testing.T) {
	admin := &Identity{ID: "admin", Role: "admin"}
	got := scopedTaskIdentity(admin, "user-a")
	if got.ID != "user-a" || got.Role != "user" {
		t.Fatalf("scoped admin identity = %#v, want user-a/user", got)
	}
	user := &Identity{ID: "user-b", Role: "user"}
	got = scopedTaskIdentity(user, "user-a")
	if got.ID != "user-b" || got.Role != "user" {
		t.Fatalf("non-admin identity should not be overridden: %#v", got)
	}
}

func TestClientTaskIDFallbackScope(t *testing.T) {
	if canUseClientTaskIDFallback(Identity{ID: "admin", Role: "admin"}) {
		t.Fatalf("admin without owner scope should not use client_task_id fallback")
	}
	if !canUseClientTaskIDFallback(scopedTaskIdentity(&Identity{ID: "admin", Role: "admin"}, "user-a")) {
		t.Fatalf("admin scoped to owner should use client_task_id fallback")
	}
	if !canUseClientTaskIDFallback(Identity{ID: "user-a", Role: "user"}) {
		t.Fatalf("ordinary users should use client_task_id fallback")
	}
}

func TestEnqueueV1ImageTaskRequiresClientTaskID(t *testing.T) {
	s := &Server{taskStore: &PGTaskStore{}}
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{}`))
	rr := httptest.NewRecorder()
	ok := s.enqueueV1ImageTask(rr, req, &Identity{ID: "user-a", Role: "user"}, imageTaskCreateRequest{
		Mode:   "generate",
		Prompt: "sync prompt",
		Model:  "gpt-image-2",
		N:      1,
	})
	if ok {
		t.Fatalf("v1 image request without client_task_id should stay synchronous")
	}
}

func TestFreeUserHighResolutionRejectedBeforeDBTaskCreate(t *testing.T) {
	s := &Server{taskStore: &PGTaskStore{}}
	_, _, err := s.createDBImageTask(context.Background(), &Identity{
		ID:                      "user-a",
		Role:                    "user",
		AccountTier:             "free",
		CanUsePaidImageAccounts: false,
		CanUseHighResolution:    false,
	}, imageTaskCreateRequest{
		Mode:       "generate",
		Prompt:     "high resolution prompt",
		Model:      "gpt-image-2",
		Resolution: "2k",
		N:          1,
	})
	var se statusError
	if !errors.As(err, &se) || se.status != http.StatusForbidden {
		t.Fatalf("create high resolution task err = %v, want 403 statusError", err)
	}
}

func TestStoredAuthKeyBecomesServiceAdminKey(t *testing.T) {
	s := &Server{
		cfg:   Config{AuthKey: "root-key"},
		store: NewStore(t.TempDir()),
	}
	if err := s.store.SaveAuthKeys([]UserKey{{
		ID:                  "newapi",
		Name:                "newapi",
		Role:                "user",
		Key:                 "sk-newapi",
		KeyHash:             hashKey("sk-newapi"),
		AccountTier:         "free",
		Enabled:             true,
		ImageTotalQuota:     1,
		ImageTotalUnlimited: false,
	}}); err != nil {
		t.Fatalf("save auth keys: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/accounts", nil)
	req.Header.Set("Authorization", "Bearer sk-newapi")
	rr := httptest.NewRecorder()
	id, ok := s.requireAdmin(rr, req)
	if !ok {
		t.Fatalf("service key should pass admin auth, status=%d body=%s", rr.Code, rr.Body.String())
	}
	if id.ID != "newapi" || id.Role != "admin" || id.AccountTier != "premium" {
		t.Fatalf("identity = %#v, want newapi admin premium", id)
	}
	if !id.CanUseHighResolution || !id.CanUsePaidImageAccounts {
		t.Fatalf("service key should have full image access: %#v", id)
	}

	keys := s.store.LoadAuthKeys()
	if len(keys) != 1 {
		t.Fatalf("keys len = %d, want 1", len(keys))
	}
	if keys[0].Role != "admin" || keys[0].AccountTier != "premium" || !keys[0].ImageTotalUnlimited {
		t.Fatalf("stored key normalized = %#v, want admin premium unlimited", keys[0])
	}
}

func TestCreateAuthUserEndpointCreatesServiceKey(t *testing.T) {
	s := &Server{
		cfg:   Config{AuthKey: "root-key"},
		store: NewStore(t.TempDir()),
	}
	req := httptest.NewRequest(http.MethodPost, "/api/auth/users", strings.NewReader(`{"name":"newapi","key":"sk-service"}`))
	req.Header.Set("Authorization", "Bearer root-key")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	s.handleAuthUsers(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("create key status = %d body=%s, want 200", rr.Code, rr.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	req.Header.Set("Authorization", "Bearer sk-service")
	rr = httptest.NewRecorder()
	id, ok := s.requireAdmin(rr, req)
	if !ok {
		t.Fatalf("created service key should pass admin auth, status=%d body=%s", rr.Code, rr.Body.String())
	}
	if id.Role != "admin" || id.AccountTier != "premium" {
		t.Fatalf("created key identity = %#v, want admin premium", id)
	}
}

func TestSaveImageWithBaseURLUniqueForSameBytes(t *testing.T) {
	root := t.TempDir()
	s := &Server{
		dataDir:   filepath.Join(root, "data"),
		imagesDir: filepath.Join(root, "data", "images"),
		store:     NewStore(filepath.Join(root, "data")),
		cfg:       Config{ImageRetentionDays: 15},
	}
	relA, _, err := s.saveImageWithBaseURL("https://example.com", []byte("same-image"))
	if err != nil {
		t.Fatalf("save first image: %v", err)
	}
	relB, _, err := s.saveImageWithBaseURL("https://example.com", []byte("same-image"))
	if err != nil {
		t.Fatalf("save second image: %v", err)
	}
	if relA == relB {
		t.Fatalf("same bytes saved twice should get unique rel path: %q", relA)
	}
}

func TestHandleWebServesStaticExportRouteIndex(t *testing.T) {
	root := t.TempDir()
	webDist := filepath.Join(root, "web_dist")
	if err := os.MkdirAll(filepath.Join(webDist, "image"), 0755); err != nil {
		t.Fatalf("make image route dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(webDist, "_next", "static"), 0755); err != nil {
		t.Fatalf("make static dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(webDist, "index.html"), []byte("root index"), 0644); err != nil {
		t.Fatalf("write root index: %v", err)
	}
	if err := os.WriteFile(filepath.Join(webDist, "image", "index.html"), []byte("image route"), 0644); err != nil {
		t.Fatalf("write route index: %v", err)
	}
	if err := os.WriteFile(filepath.Join(webDist, "_next", "static", "app.js"), []byte("asset"), 0644); err != nil {
		t.Fatalf("write static asset: %v", err)
	}
	s := &Server{webDist: webDist}

	for _, path := range []string{"/image", "/image/"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rr := httptest.NewRecorder()
		s.handleWeb(rr, req)
		if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "image route") {
			t.Fatalf("%s response status=%d body=%q, want route index", path, rr.Code, rr.Body.String())
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/_next/static/app.js", nil)
	rr := httptest.NewRecorder()
	s.handleWeb(rr, req)
	if rr.Code != http.StatusOK || rr.Body.String() != "asset" {
		t.Fatalf("static asset response status=%d body=%q, want asset", rr.Code, rr.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/missing.js", nil)
	rr = httptest.NewRecorder()
	s.handleWeb(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("missing asset status = %d, want 404", rr.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/../config.json", nil)
	rr = httptest.NewRecorder()
	s.handleWeb(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("path traversal status = %d, want 404", rr.Code)
	}
}

func TestImageTaskCancelRejectsOversizedBody(t *testing.T) {
	t.Setenv("GPT2API_IMAGE_AUTH_KEY", "")
	t.Setenv("GPT2API_IMAGE_DATABASE_URL", "")
	t.Setenv("GPT2API_IMAGE_BASE_URL", "")
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "config.json"), []byte(`{"auth-key":"test","image_retention_days":15}`), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	s, err := newServer(root, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	body := `{"id":"` + strings.Repeat("x", int(maxJSONBodyBytes)+1) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/image-tasks/cancel", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer test")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("cancel status = %d body=%s, want 413", rr.Code, rr.Body.String())
	}
}

func TestImageTaskTimingSettingsHaveSafeMinimums(t *testing.T) {
	t.Setenv("GPT2API_IMAGE_AUTH_KEY", "")
	t.Setenv("GPT2API_IMAGE_DATABASE_URL", "")
	t.Setenv("GPT2API_IMAGE_BASE_URL", "")
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "config.json"), []byte(`{"auth-key":"test","image_task_timeout_secs":1,"image_task_claim_ttl_secs":1}`), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	s, err := newServer(root, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	if s.cfg.ImageTaskTimeoutSecs != minImageTaskTimeoutSecs {
		t.Fatalf("task timeout = %d, want %d", s.cfg.ImageTaskTimeoutSecs, minImageTaskTimeoutSecs)
	}
	if s.cfg.ImageTaskClaimTTLSecs != minImageTaskClaimTTLSecs {
		t.Fatalf("claim ttl = %d, want %d", s.cfg.ImageTaskClaimTTLSecs, minImageTaskClaimTTLSecs)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/settings", strings.NewReader(`{"image_task_timeout_secs":2,"image_task_claim_ttl_secs":2}`))
	req.Header.Set("Authorization", "Bearer test")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("settings status = %d body=%s, want 200", rr.Code, rr.Body.String())
	}
	if s.cfg.ImageTaskTimeoutSecs != minImageTaskTimeoutSecs {
		t.Fatalf("settings task timeout = %d, want %d", s.cfg.ImageTaskTimeoutSecs, minImageTaskTimeoutSecs)
	}
	if s.cfg.ImageTaskClaimTTLSecs != minImageTaskClaimTTLSecs {
		t.Fatalf("settings claim ttl = %d, want %d", s.cfg.ImageTaskClaimTTLSecs, minImageTaskClaimTTLSecs)
	}
}

func TestAdminManagementJSONEndpointsRejectOversizedBody(t *testing.T) {
	t.Setenv("GPT2API_IMAGE_AUTH_KEY", "")
	t.Setenv("GPT2API_IMAGE_DATABASE_URL", "")
	t.Setenv("GPT2API_IMAGE_BASE_URL", "")
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "config.json"), []byte(`{"auth-key":"test","image_retention_days":15}`), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	s, err := newServer(root, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	if err := s.store.SaveAuthKeys([]UserKey{{ID: "user-a", Name: "user-a", Role: "user", Enabled: true}}); err != nil {
		t.Fatalf("save auth keys: %v", err)
	}
	huge := strings.Repeat("x", int(maxJSONBodyBytes)+1)
	cases := []struct {
		name string
		path string
		body string
	}{
		{name: "accounts refresh", path: "/api/accounts/refresh", body: `{"access_tokens":["` + huge + `"]}`},
		{name: "auth regenerate", path: "/api/auth/users/user-a/regenerate", body: `{"key":"` + huge + `"}`},
		{name: "images download", path: "/api/images/download", body: `{"paths":["` + huge + `"]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer test")
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			s.Handler().ServeHTTP(rr, req)
			if rr.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("%s status = %d body=%s, want 413", tc.path, rr.Code, rr.Body.String())
			}
		})
	}
}

func TestTextV1RoutesDisabledInImageOnlyBuild(t *testing.T) {
	t.Setenv("GPT2API_IMAGE_AUTH_KEY", "")
	t.Setenv("GPT2API_IMAGE_DATABASE_URL", "")
	t.Setenv("GPT2API_IMAGE_BASE_URL", "")
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "config.json"), []byte(`{"auth-key":"test","image_retention_days":15}`), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	s, err := newServer(root, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	cases := []struct {
		name string
		path string
		body string
	}{
		{
			name: "chat completions text",
			path: "/v1/chat/completions",
			body: `{"model":"gpt-5","messages":[{"role":"user","content":"hello"}]}`,
		},
		{
			name: "responses text",
			path: "/v1/responses",
			body: `{"model":"gpt-5","input":"hello"}`,
		},
		{
			name: "messages",
			path: "/v1/messages",
			body: `{"model":"gpt-5","messages":[{"role":"user","content":"hello"}]}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer test")
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			s.Handler().ServeHTTP(rr, req)
			if rr.Code != http.StatusNotFound {
				t.Fatalf("%s status = %d body=%s, want 404", tc.path, rr.Code, rr.Body.String())
			}
		})
	}
}

func TestUnsafeDefaultAuthKeyRejected(t *testing.T) {
	cases := []string{
		"change-me",
		"replace-with-a-long-random-admin-key",
		"<openssl-rand-hex-32>",
	}
	for _, value := range cases {
		t.Run(value, func(t *testing.T) {
			t.Setenv("GPT2API_IMAGE_AUTH_KEY", "")
			t.Setenv("GPT2API_IMAGE_DATABASE_URL", "")
			root := t.TempDir()
			raw, _ := json.Marshal(map[string]any{"auth-key": value})
			if err := os.WriteFile(filepath.Join(root, "config.json"), raw, 0644); err != nil {
				t.Fatalf("write config: %v", err)
			}
			_, err := newServer(root, false)
			if err == nil || !strings.Contains(err.Error(), "默认占位值") {
				t.Fatalf("newServer err = %v, want unsafe default auth-key error", err)
			}
		})
	}
}

func TestV1ModelsImageOnly(t *testing.T) {
	t.Setenv("GPT2API_IMAGE_AUTH_KEY", "")
	t.Setenv("GPT2API_IMAGE_DATABASE_URL", "")
	t.Setenv("GPT2API_IMAGE_BASE_URL", "")
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "config.json"), []byte(`{"auth-key":"test","image_retention_days":15}`), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	s, err := newServer(root, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer test")
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("models status = %d body=%s, want 200", rr.Code, rr.Body.String())
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode models: %v", err)
	}
	if len(body.Data) == 0 {
		t.Fatalf("models list is empty")
	}
	for _, item := range body.Data {
		if !isSupportedImageModel(item.ID) {
			t.Fatalf("model %q is not an image model; full body=%s", item.ID, rr.Body.String())
		}
	}
}
