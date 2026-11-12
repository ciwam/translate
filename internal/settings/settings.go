// Package settings persists user configuration. Port of
// macos/Sources/Translate/TTSettingsStore.m's file scheme: settings.json
// holds per-provider API keys encrypted with AES-256-GCM under a random key
// file (encryption.key, chmod 0600) — no Keychain involvement.
package settings

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"ciwang/translate/internal/provider"
)

// Data is the persisted settings payload.
type Data struct {
	ProviderIndex     int               `json:"providerIndex"`
	TargetIndex       int               `json:"targetIndex"`
	Glossary          string            `json:"glossary"`
	APIKeysByProvider map[string]string `json:"apiKeysByProvider,omitempty"` // provider index (decimal) -> base64(IV|ciphertext)
}

// Store reads and writes settings.json and encryption.key.
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
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "Application Support", "Translate-Go"), nil
}

func (s *Store) settingsPath() string { return filepath.Join(s.dir, "settings.json") }
func (s *Store) keyFilePath() string  { return filepath.Join(s.dir, "encryption.key") }

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
	if d.APIKeysByProvider == nil {
		d.APIKeysByProvider = map[string]string{}
	}
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.settingsPath(), b, 0o644)
}

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

// aesGCM loads or creates the 32-byte encryption key file (0600), matching
// the TTSettingsStore scheme.
func (s *Store) aesGCM() (cipher.AEAD, error) {
	key, err := os.ReadFile(s.keyFilePath())
	if err != nil || len(key) != 32 {
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		if err := os.WriteFile(s.keyFilePath(), key, 0o600); err != nil {
			return nil, err
		}
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// APIKey reads and decrypts one provider's key; "" when absent.
func (s *Store) APIKey(providerIndex int) string {
	d := s.Load()
	cipherB64 := d.APIKeysByProvider[itoa(providerIndex)]
	if cipherB64 == "" {
		return ""
	}
	gcm, err := s.aesGCM()
	if err != nil {
		return ""
	}
	raw, err := base64.StdEncoding.DecodeString(cipherB64)
	if err != nil || len(raw) < gcm.NonceSize() {
		return ""
	}
	plain, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], nil)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(plain))
}

// SetAPIKey encrypts and stores one provider's key (empty removes it).
func (s *Store) SetAPIKey(providerIndex int, key string) error {
	d := s.Load()
	if d.APIKeysByProvider == nil {
		d.APIKeysByProvider = map[string]string{}
	}
	slot := itoa(providerIndex)
	if strings.TrimSpace(key) == "" {
		delete(d.APIKeysByProvider, slot)
		return s.Save(d)
	}
	gcm, err := s.aesGCM()
	if err != nil {
		return fmt.Errorf("加密初始化失败：%w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	sealed := gcm.Seal(nonce, nonce, []byte(key), nil)
	d.APIKeysByProvider[slot] = base64.StdEncoding.EncodeToString(sealed)
	return s.Save(d)
}
