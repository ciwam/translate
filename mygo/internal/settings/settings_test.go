package settings

import (
	"os"
	"path/filepath"
	"testing"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("TRANSLATE_SETTINGS_DIR", dir)
	s, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAPIKeyRoundTrip(t *testing.T) {
	s := testStore(t)
	if got := s.APIKey(0); got != "" {
		t.Errorf("empty store returned %q", got)
	}
	if err := s.SetAPIKey(0, "sk-test-123"); err != nil {
		t.Fatal(err)
	}
	if got := s.APIKey(0); got != "sk-test-123" {
		t.Errorf("round trip got %q", got)
	}
	if got := s.APIKey(1); got != "" {
		t.Errorf("provider 1 should be empty, got %q", got)
	}
}

func TestAPIKeyDelete(t *testing.T) {
	s := testStore(t)
	_ = s.SetAPIKey(1, "abc")
	if err := s.SetAPIKey(1, ""); err != nil {
		t.Fatal(err)
	}
	if got := s.APIKey(1); got != "" {
		t.Errorf("deleted key returned %q", got)
	}
}

func TestEncryptionKeyFilePerms(t *testing.T) {
	s := testStore(t)
	_ = s.SetAPIKey(0, "trigger-keyfile-creation")
	info, err := os.Stat(filepath.Join(s.dir, "encryption.key"))
	if err != nil {
		t.Fatal("encryption.key not created")
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("encryption.key perm = %o, want 600", perm)
	}
	// Raw key must not appear in settings.json.
	b, _ := os.ReadFile(filepath.Join(s.dir, "settings.json"))
	if len(b) == 0 {
		t.Fatal("settings.json missing")
	}
	for _, marker := range []string{"trigger-keyfile-creation", "sk-"} {
		if contains(b, marker) {
			t.Errorf("settings.json contains plaintext marker %q", marker)
		}
	}
}

func TestKeySurvivesStoreReload(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TRANSLATE_SETTINGS_DIR", dir)
	s, _ := Load()
	_ = s.SetAPIKey(1, "persist-me")
	s2, _ := Load() // fresh store, same dir
	if got := s2.APIKey(1); got != "persist-me" {
		t.Errorf("reloaded key = %q", got)
	}
}

func contains(b []byte, s string) bool {
	return len(s) > 0 && len(b) >= len(s) && indexOf(b, []byte(s)) >= 0
}

func indexOf(b, sub []byte) int {
	for i := 0; i+len(sub) <= len(b); i++ {
		match := true
		for j := range sub {
			if b[i+j] != sub[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}
