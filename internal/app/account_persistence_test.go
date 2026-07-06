package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAddAccountRecordsReturnsStoreError(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	if err := os.Mkdir(filepath.Join(dir, "accounts.json.tmp"), 0755); err != nil {
		t.Fatalf("block account writes: %v", err)
	}
	s := &Server{store: store}

	added, skipped, msg, err := s.addAccountRecords([]map[string]any{{"access_token": "tok-add"}})
	if err == nil {
		t.Fatal("addAccountRecords error = nil, want store write error")
	}
	if added != 0 || skipped != 0 || msg != "" {
		t.Fatalf("added=%d skipped=%d msg=%q, want zero values on write error", added, skipped, msg)
	}
	if got := store.LoadAccounts(); len(got) != 0 {
		t.Fatalf("accounts = %#v, want no persisted accounts after write error", got)
	}
}

func TestDeleteAccountTokensReturnsStoreError(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	if err := store.SaveAccounts([]Account{{AccessToken: "tok-delete", SourceType: "web", Status: accountStatusNormal, Quota: 1}}); err != nil {
		t.Fatalf("save account: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "accounts.json.tmp"), 0755); err != nil {
		t.Fatalf("block account writes: %v", err)
	}
	s := &Server{store: store}

	removed, err := s.deleteAccountTokens([]string{"tok-delete"})
	if err == nil {
		t.Fatal("deleteAccountTokens error = nil, want store write error")
	}
	if removed != 0 {
		t.Fatalf("removed = %d, want 0 on write error", removed)
	}
	got := store.LoadAccounts()
	if len(got) != 1 || got[0].AccessToken != "tok-delete" {
		t.Fatalf("accounts = %#v, want original account preserved after write error", got)
	}
}
