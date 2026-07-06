package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestV1ImageResultSaveFailureCleansSavedImagesAndRefunds(t *testing.T) {
	s, id := newV1ImageCleanupTestServer(t)
	installTwoImageGenerator(t, s)
	saved := installSecondSaveFailure(t, s)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	rr := httptest.NewRecorder()
	s.imageResult(rr, req, id, "draw a cat", "gpt-image-2", "", "", "b64_json", 2, false, nil)

	assertSaveFailureCleaned(t, s, rr, saved, id.ID)
}

func TestV1ImageResultMetadataFailureCleansSavedImageAndRefunds(t *testing.T) {
	s, id := newV1ImageCleanupTestServer(t)
	s.imageGenerator = func(ctx context.Context, prompt, model, size, resolution string, refs [][]byte, n int) ([]upstreamImageResult, error) {
		return []upstreamImageResult{{Bytes: []byte("image data"), RevisedPrompt: prompt}}, nil
	}
	saved := []string{}
	s.imageSaver = func(r *http.Request, data []byte) (string, string, error) {
		rel, url, err := s.saveImageWithBaseURL(s.baseURL(r), data)
		if err == nil {
			saved = append(saved, rel)
		}
		return rel, url, err
	}
	if err := os.Mkdir(s.store.path("image_owners.json.tmp"), 0755); err != nil {
		t.Fatalf("block owner metadata writes: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	rr := httptest.NewRecorder()
	s.imageResult(rr, req, id, "draw a cat", "gpt-image-2", "", "", "b64_json", 1, false, nil)

	assertMetadataFailureCleaned(t, s, rr, saved, id.ID)
}

func TestV1ChatImageSaveFailureCleansSavedImagesAndRefunds(t *testing.T) {
	s, id := newV1ImageCleanupTestServer(t)
	installTwoImageGenerator(t, s)
	saved := installSecondSaveFailure(t, s)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	rr := httptest.NewRecorder()
	s.handleV1ChatImageCompletion(rr, req, id, map[string]any{
		"model":  "gpt-image-2",
		"prompt": "draw a cat",
		"n":      2,
	})

	assertSaveFailureCleaned(t, s, rr, saved, id.ID)
}

func TestV1ImageResultStreamSaveFailureCleansSavedImagesAndRefunds(t *testing.T) {
	s, id := newV1ImageCleanupTestServer(t)
	installTwoImageGenerator(t, s)
	saved := installSecondSaveFailure(t, s)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	rr := httptest.NewRecorder()
	s.imageResultStream(rr, req, id, "draw a cat", "gpt-image-2", "", "", 2, false, nil)

	assertStreamSaveFailureCleaned(t, s, rr, saved, id.ID)
}

func newV1ImageCleanupTestServer(t *testing.T) (*Server, *Identity) {
	t.Helper()
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
	id := &Identity{ID: "user-key", Role: "user"}
	if err := s.store.SaveAuthKeys([]UserKey{{ID: id.ID, Name: "user", Enabled: true}}); err != nil {
		t.Fatalf("save auth keys: %v", err)
	}
	return s, id
}

func installTwoImageGenerator(t *testing.T, s *Server) {
	t.Helper()
	s.imageGenerator = func(ctx context.Context, prompt, model, size, resolution string, refs [][]byte, n int) ([]upstreamImageResult, error) {
		if n != 2 {
			t.Fatalf("n = %d, want 2", n)
		}
		return []upstreamImageResult{
			{Bytes: []byte("first image"), RevisedPrompt: prompt},
			{Bytes: []byte("second image"), RevisedPrompt: prompt},
		}, nil
	}
}

func installSecondSaveFailure(t *testing.T, s *Server) *[]string {
	t.Helper()
	saved := []string{}
	call := 0
	s.imageSaver = func(r *http.Request, data []byte) (string, string, error) {
		call++
		if call == 2 {
			return "", "", errors.New("disk full")
		}
		rel, url, err := s.saveImageWithBaseURL(s.baseURL(r), data)
		if err == nil {
			saved = append(saved, rel)
		}
		return rel, url, err
	}
	return &saved
}

func assertSaveFailureCleaned(t *testing.T, s *Server, rr *httptest.ResponseRecorder, saved *[]string, userID string) {
	t.Helper()
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body=%s, want 500", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "disk full") {
		t.Fatalf("body = %s, want disk full error", rr.Body.String())
	}
	if len(*saved) != 1 {
		t.Fatalf("saved rels = %#v, want exactly one saved image before failure", *saved)
	}
	rel := (*saved)[0]
	path, err := s.imagePath(rel)
	if err != nil {
		t.Fatalf("image path: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("saved image still exists at %s, stat err=%v", path, err)
	}
	if _, ok := s.store.LoadOwners()[rel]; ok {
		t.Fatalf("owner metadata still exists for %s", rel)
	}
	if _, ok := s.store.LoadPrompts()[rel]; ok {
		t.Fatalf("prompt metadata still exists for %s", rel)
	}
	for _, key := range s.store.LoadAuthKeys() {
		if key.ID == userID {
			if key.ImageDailyUsed != 0 || key.ImageMonthlyUsed != 0 || key.ImageTotalUsed != 0 {
				t.Fatalf("image quota used = daily:%d monthly:%d total:%d, want all 0", key.ImageDailyUsed, key.ImageMonthlyUsed, key.ImageTotalUsed)
			}
			return
		}
	}
	t.Fatalf("auth key %q not found", userID)
}

func assertMetadataFailureCleaned(t *testing.T, s *Server, rr *httptest.ResponseRecorder, saved []string, userID string) {
	t.Helper()
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body=%s, want 500", rr.Code, rr.Body.String())
	}
	if len(saved) != 1 {
		t.Fatalf("saved rels = %#v, want exactly one saved image before metadata failure", saved)
	}
	rel := saved[0]
	path, err := s.imagePath(rel)
	if err != nil {
		t.Fatalf("image path: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("saved image still exists at %s, stat err=%v", path, err)
	}
	if _, ok := s.store.LoadOwners()[rel]; ok {
		t.Fatalf("owner metadata still exists for %s", rel)
	}
	if _, ok := s.store.LoadPrompts()[rel]; ok {
		t.Fatalf("prompt metadata still exists for %s", rel)
	}
	for _, key := range s.store.LoadAuthKeys() {
		if key.ID == userID {
			if key.ImageDailyUsed != 0 || key.ImageMonthlyUsed != 0 || key.ImageTotalUsed != 0 {
				t.Fatalf("image quota used = daily:%d monthly:%d total:%d, want all 0", key.ImageDailyUsed, key.ImageMonthlyUsed, key.ImageTotalUsed)
			}
			return
		}
	}
	t.Fatalf("auth key %q not found", userID)
}

func assertStreamSaveFailureCleaned(t *testing.T, s *Server, rr *httptest.ResponseRecorder, saved *[]string, userID string) {
	t.Helper()
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200 stream error", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "disk full") || !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("body = %s, want stream error and done marker", body)
	}
	if strings.Contains(body, "image.generation.result") {
		t.Fatalf("body = %s, want no result event after save failure", body)
	}
	if len(*saved) != 1 {
		t.Fatalf("saved rels = %#v, want exactly one saved image before failure", *saved)
	}
	rel := (*saved)[0]
	path, err := s.imagePath(rel)
	if err != nil {
		t.Fatalf("image path: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("saved image still exists at %s, stat err=%v", path, err)
	}
	if _, ok := s.store.LoadOwners()[rel]; ok {
		t.Fatalf("owner metadata still exists for %s", rel)
	}
	if _, ok := s.store.LoadPrompts()[rel]; ok {
		t.Fatalf("prompt metadata still exists for %s", rel)
	}
	for _, key := range s.store.LoadAuthKeys() {
		if key.ID == userID {
			if key.ImageDailyUsed != 0 || key.ImageMonthlyUsed != 0 || key.ImageTotalUsed != 0 {
				t.Fatalf("image quota used = daily:%d monthly:%d total:%d, want all 0", key.ImageDailyUsed, key.ImageMonthlyUsed, key.ImageTotalUsed)
			}
			return
		}
	}
	t.Fatalf("auth key %q not found", userID)
}
