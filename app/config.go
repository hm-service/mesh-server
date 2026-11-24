package app

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"

	"mesh-server/core"
)

const (
	// DefaultListen matches the address CONTRACT.md has always documented.
	DefaultListen  = ":8088"
	passwordLength = 12
	configName     = "config.json"
)

// passwordAlphabet skips visually ambiguous characters, so the password printed
// once at first start can be copied by hand.
const passwordAlphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// Config is DATA_DIR/config.json. The admin password is stored as a scrypt
// salt and hash, never as plain text. Unknown fields of older versions are
// ignored on purpose: no_auth was removed and must not block a start.
type Config struct {
	Listen       string `json:"listen"`
	PasswordSalt string `json:"password_salt"`
	PasswordHash string `json:"password_hash"`
}

func configPath(dataDir string) string {
	return filepath.Join(dataDir, configName)
}

// loadOrCreateConfig returns the persisted config. When config.json is absent it
// generates a random admin password, stores only its hash and returns the plain
// password so the caller can print it exactly once.
func loadOrCreateConfig(dataDir string) (cfg Config, generated string, err error) {
	path := configPath(dataDir)
	raw, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return Config{}, "", fmt.Errorf("parse %s: %w", path, err)
		}
		if cfg.Listen == "" {
			cfg.Listen = DefaultListen
		}
		if cfg.PasswordSalt == "" || cfg.PasswordHash == "" {
			return Config{}, "", fmt.Errorf("%s is missing password_salt/password_hash", path)
		}
		return cfg, "", nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return Config{}, "", fmt.Errorf("read %s: %w", path, err)
	}
	password, err := randomPassword(passwordLength)
	if err != nil {
		return Config{}, "", err
	}
	salt, hash, err := core.HashPassword(password)
	if err != nil {
		return Config{}, "", err
	}
	cfg = Config{Listen: DefaultListen, PasswordSalt: salt, PasswordHash: hash}
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return Config{}, "", err
	}
	if err := WriteFileAtomic(path, append(out, 0x0a)); err != nil {
		return Config{}, "", err
	}
	return cfg, password, nil
}

// WriteFileAtomic replaces path through a temporary file plus rename. Files are
// created 0600: private keys, the database and the log all live in DATA_DIR.
func WriteFileAtomic(path string, raw []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

func randomPassword(length int) (string, error) {
	buf := make([]byte, length)
	limit := big.NewInt(int64(len(passwordAlphabet)))
	for i := range buf {
		idx, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return "", fmt.Errorf("read random: %w", err)
		}
		buf[i] = passwordAlphabet[idx.Int64()]
	}
	return string(buf), nil
}
