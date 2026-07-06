package app

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSettingsSaveFailureReturnsErrorAndKeepsConfig(t *testing.T) {
	root := configRootBlockedByFile(t)
	s := &Server{
		root:  root,
		cfg:   Config{AuthKey: "test", Extra: map[string]any{}, Proxy: "http://old-proxy"},
		store: NewStore(t.TempDir()),
	}

	req := httptest.NewRequest(http.MethodPost, "/api/settings", strings.NewReader(`{"proxy":"http://new-proxy"}`))
	req.Header.Set("Authorization", "Bearer test")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	s.handleSettings(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("settings status = %d body=%s, want 500", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "failed to save config") {
		t.Fatalf("settings body = %s, want save failure", rr.Body.String())
	}
	if s.cfg.Proxy != "http://old-proxy" {
		t.Fatalf("proxy = %q, want old value after save failure", s.cfg.Proxy)
	}
	if _, ok := s.cfg.Extra["proxy"]; ok {
		t.Fatalf("extra proxy should not be written after save failure: %#v", s.cfg.Extra)
	}
}

func TestProxySaveFailureReturnsErrorAndKeepsConfig(t *testing.T) {
	root := configRootBlockedByFile(t)
	s := &Server{
		root:  root,
		cfg:   Config{AuthKey: "test", Extra: map[string]any{"proxy": "http://old-proxy"}, Proxy: "http://old-proxy"},
		store: NewStore(t.TempDir()),
	}

	req := httptest.NewRequest(http.MethodPost, "/api/proxy", strings.NewReader(`{"url":"http://new-proxy"}`))
	req.Header.Set("Authorization", "Bearer test")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	s.handleProxy(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("proxy status = %d body=%s, want 500", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "failed to save config") {
		t.Fatalf("proxy body = %s, want save failure", rr.Body.String())
	}
	if s.cfg.Proxy != "http://old-proxy" {
		t.Fatalf("proxy = %q, want old value after save failure", s.cfg.Proxy)
	}
	if got := s.cfg.Extra["proxy"]; got != "http://old-proxy" {
		t.Fatalf("extra proxy = %#v, want old value after save failure", got)
	}
}

func configRootBlockedByFile(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "config-root")
	if err := os.WriteFile(root, []byte("not a directory"), 0644); err != nil {
		t.Fatalf("write blocking file: %v", err)
	}
	return root
}
