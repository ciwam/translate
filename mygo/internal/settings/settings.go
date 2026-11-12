// Package settings persists user configuration. Port of
// macos/Sources/Translate/TTSettingsStore.m, with mygo.Secrets (the macOS
// Keychain) replacing AES-CBC file encryption.
package settings

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"ciwang/translate/internal/provider"

	"github.com/egoist/mygo"
)

// Data is the persisted settings payload. API keys live in the system
// credential store (Keychain on macOS), never in this file.
type Data struct {
	ProviderIndex int    `json:"providerIndex"`
	TargetIndex   int    `json:"targetIndex"`
	Glossary      string `json:"glossary"`
}

// Store reads and writes settings.json plus per-provider keychain entries.
type Store struct {
	dir string
}

// Load opens the store, creating the directory when missing.
func Load() (*Store, error) {
	dir, err := userDataDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

func userDataDir() (string, error) {
	if dir := os.Getenv("TRANSLATE_SETTINGS_DIR"); dir != "" {
		return dir, nil
	}
	// mygo.PathUserData requires the app to have initialized its identity;
	// fall back to the conventional macOS location for the CLI and tests.
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "Application Support", "Translate-Go"), nil
}

func (s *Store) settingsPath() string { return filepath.Join(s.dir, "settings.json") }

func (s *Store) Load() Data {
	d := Data{ProviderIndex: 0, TargetIndex: provider.DefaultTargetIndex()}
	b, err := os.ReadFile(s.settingsPath())
	if err != nil {
		return d
	}
	_ = json.Unmarshal(b, &d)
	if d.TargetIndex < 0 || d.TargetIndex >= provider.TargetCount() {
		d.TargetIndex = provider.DefaultTargetIndex()
	}
	if d.ProviderIndex < 0 || d.ProviderIndex >= provider.ProviderCount() {
		d.ProviderIndex = 0
	}
	return d
}

func (s *Store) Save(d Data) error {
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.settingsPath(), b, 0o644)
}

// secretKey names the keychain entry for a provider.
func secretKey(idx int) string { return "api-key-" + itoa(idx) }

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var buf [8]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

// APIKey reads one provider's key from the system credential store.
func (s *Store) APIKey(providerIndex int) string {
	key, err := mygo.Secrets.Get("api-key-" + itoa(providerIndex))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(key))
}

// SetAPIKey stores one provider's key (empty deletes the entry).
func (s *Store) SetAPIKey(providerIndex int, key string) error {
	if strings.TrimSpace(key) == "" {
		err := mygo.Secrets.Delete("api-key-" + itoa(providerIndex))
		if err != nil && !errors.Is(err, mygo.ErrSecretNotFound) {
			return err
		}
		return nil
	}
	return mygo.Secrets.Set("api-key-"+itoa(providerIndex), []byte(key))
}
