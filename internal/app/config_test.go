package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfigMissingFileUsesEmptyObject(t *testing.T) {
	cfg, err := loadConfig(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatalf("load missing config: %v", err)
	}
	if cfg.Extra == nil || len(cfg.Extra) != 0 {
		t.Fatalf("Extra = %#v, want empty map", cfg.Extra)
	}
}

func TestLoadConfigRejectsInvalidJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"auth-key":`), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	_, err := loadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "invalid config file") {
		t.Fatalf("load invalid config err = %v, want invalid config file", err)
	}
}

func TestLoadConfigRejectsNonObjectJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`null`), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	_, err := loadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "expected JSON object") {
		t.Fatalf("load null config err = %v, want expected JSON object", err)
	}
}
