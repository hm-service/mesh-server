package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const (
	// DefaultListen matches the address CONTRACT.md has always documented.
	DefaultListen = ":8088"
	configName    = "config.json"
)

// Config is DATA_DIR/config.json. It only carries the listen address: the
// admin credential comes from AUTH_TOKEN at startup and is never persisted.
type Config struct {
	Listen string `json:"listen"`
}

func configPath(dataDir string) string {
	return filepath.Join(dataDir, configName)
}

// loadOrCreateConfig returns the persisted config, writing the default when
// config.json does not exist yet.
func loadOrCreateConfig(dataDir string) (Config, error) {
	path := configPath(dataDir)
	raw, err := os.ReadFile(path)
	if err == nil {
		var cfg Config
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return Config{}, fmt.Errorf("parse %s: %w", path, err)
		}
		if cfg.Listen == "" {
			cfg.Listen = DefaultListen
		}
		return cfg, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return Config{}, fmt.Errorf("read %s: %w", path, err)
	}
	cfg := Config{Listen: DefaultListen}
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return Config{}, err
	}
	if err := WriteFileAtomic(path, append(out, 0x0a)); err != nil {
		return Config{}, err
	}
	return cfg, nil
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
