package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Store struct {
	dir string
	mu  sync.RWMutex
}

func NewStore(dir string) *Store {
	_ = os.MkdirAll(dir, 0755)
	return &Store{dir: dir}
}

func (s *Store) path(name string) string { return filepath.Join(s.dir, name) }

func readJSONFile[T any](path string, fallback T) T {
	b, err := os.ReadFile(path)
	if err != nil {
		return fallback
	}
	var out T
	if err := json.Unmarshal(b, &out); err != nil {
		return fallback
	}
	return out
}

func writeJSONFile(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	unlock, err := lockJSONFile(path)
	if err != nil {
		return err
	}
	defer unlock()
	return writeJSONFileUnlocked(path, value)
}

func writeJSONFileUnlocked(path string, value any) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func updateJSONFile(path string, fn func() error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	unlock, err := lockJSONFile(path)
	if err != nil {
		return err
	}
	defer unlock()
	return fn()
}

func lockJSONFile(path string) (func(), error) {
	lockPath := path + ".lock"
	deadline := time.Now().Add(10 * time.Second)
	for {
		f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
		if err == nil {
			return func() {
				_ = f.Close()
				_ = os.Remove(lockPath)
			}, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, err
		}
		if st, statErr := os.Stat(lockPath); statErr == nil && time.Since(st.ModTime()) > 30*time.Second {
			_ = os.Remove(lockPath)
			continue
		}
		if time.Now().After(deadline) {
			return nil, errors.New("timeout waiting for file lock: " + filepath.Base(path))
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func (s *Store) LoadAccounts() []Account {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := readJSONFile(s.path("accounts.json"), []Account{})
	out := make([]Account, 0, len(items))
	for _, a := range items {
		if a.AccessToken == "" {
			continue
		}
		if a.Type == "" {
			a.Type = "free"
		}
		if a.Status == "" {
			a.Status = accountStatusNormal
		}
		if a.SourceType == "" {
			a.SourceType = "web"
		}
		if a.InitialQuota < a.Quota {
			a.InitialQuota = a.Quota
		}
		out = append(out, a)
	}
	return out
}

func (s *Store) SaveAccounts(items []Account) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeJSONFile(s.path("accounts.json"), items)
}

func (s *Store) UpdateAccounts(fn func([]Account) []Account) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := s.path("accounts.json")
	return updateJSONFile(path, func() error {
		items := readJSONFile(path, []Account{})
		return writeJSONFileUnlocked(path, fn(items))
	})
}

type authKeysWrap struct {
	Items []UserKey `json:"items"`
}

func (s *Store) LoadAuthKeys() []UserKey {
	s.mu.RLock()
	defer s.mu.RUnlock()
	path := s.path("auth_keys.json")
	wrap := readJSONFile(path, authKeysWrap{})
	if len(wrap.Items) > 0 {
		return normalizeKeys(wrap.Items)
	}
	arr := readJSONFile(path, []UserKey{})
	return normalizeKeys(arr)
}

func (s *Store) SaveAuthKeys(items []UserKey) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeJSONFile(s.path("auth_keys.json"), authKeysWrap{Items: items})
}

func (s *Store) UpdateAuthKeys(fn func([]UserKey) []UserKey) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := s.path("auth_keys.json")
	return updateJSONFile(path, func() error {
		wrap := readJSONFile(path, authKeysWrap{})
		items := wrap.Items
		if len(items) == 0 {
			items = readJSONFile(path, []UserKey{})
		}
		return writeJSONFileUnlocked(path, authKeysWrap{Items: fn(normalizeKeys(items))})
	})
}

func (s *Store) LoadTasks() []ImageTask {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return readJSONFile(s.path("image_tasks.json"), []ImageTask{})
}
func (s *Store) SaveTasks(items []ImageTask) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeJSONFile(s.path("image_tasks.json"), items)
}
func (s *Store) UpdateTasks(fn func([]ImageTask) []ImageTask) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := s.path("image_tasks.json")
	return updateJSONFile(path, func() error {
		items := readJSONFile(path, []ImageTask{})
		return writeJSONFileUnlocked(path, fn(items))
	})
}
func (s *Store) LoadOwners() map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return readJSONFile(s.path("image_owners.json"), map[string]string{})
}
func (s *Store) SaveOwners(items map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeJSONFile(s.path("image_owners.json"), items)
}
func (s *Store) UpdateOwners(fn func(map[string]string) map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := s.path("image_owners.json")
	return updateJSONFile(path, func() error {
		items := readJSONFile(path, map[string]string{})
		return writeJSONFileUnlocked(path, fn(items))
	})
}
func (s *Store) LoadPrompts() map[string]map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return readJSONFile(s.path("image_prompts.json"), map[string]map[string]any{})
}
func (s *Store) SavePrompts(items map[string]map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeJSONFile(s.path("image_prompts.json"), items)
}
func (s *Store) UpdatePrompts(fn func(map[string]map[string]any) map[string]map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := s.path("image_prompts.json")
	return updateJSONFile(path, func() error {
		items := readJSONFile(path, map[string]map[string]any{})
		return writeJSONFileUnlocked(path, fn(items))
	})
}
func (s *Store) LoadTags() map[string][]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return readJSONFile(s.path("image_tags.json"), map[string][]string{})
}
func (s *Store) SaveTags(items map[string][]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeJSONFile(s.path("image_tags.json"), items)
}
func (s *Store) UpdateTags(fn func(map[string][]string) map[string][]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := s.path("image_tags.json")
	return updateJSONFile(path, func() error {
		items := readJSONFile(path, map[string][]string{})
		return writeJSONFileUnlocked(path, fn(items))
	})
}
func (s *Store) LoadList(name string) []map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return readJSONFile(s.path(name), []map[string]any{})
}
func (s *Store) SaveList(name string, v []map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeJSONFile(s.path(name), v)
}
func (s *Store) UpdateList(name string, fn func([]map[string]any) []map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := s.path(name)
	return updateJSONFile(path, func() error {
		items := readJSONFile(path, []map[string]any{})
		return writeJSONFileUnlocked(path, fn(items))
	})
}

func ensureNotDir(path string) error {
	st, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if st.IsDir() {
		return errors.New(path + " is a directory")
	}
	return nil
}

func hashKey(key string) string { h := sha256.Sum256([]byte(key)); return hex.EncodeToString(h[:]) }
