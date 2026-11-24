package db

import (
	"database/sql"
	"errors"
	"fmt"
)

// Settings is the control-plane configuration edited from the admin page. The
// default EasyTier template is not part of it: DATA_DIR/peer.default.toml stays
// the single source of truth for that. AppSHA256 is the full digest behind the
// 16-character AppHash that clients compare.
type Settings struct {
	AppVersion  string
	AppURL      string
	AppHash     string
	AppSHA256   string
	AppNotes    string
	AppSource   string
	AppFileName string
	AppSize     int64
}

const settingColumns = "app_version, app_url, app_hash, app_sha256, app_notes, app_source, app_file_name, app_size"

// GetSettings returns the stored settings; found is false when nothing has been
// published yet.
func (d *DB) GetSettings() (Settings, bool, error) {
	var s Settings
	err := d.conn.QueryRow("SELECT "+settingColumns+" FROM setting ORDER BY id LIMIT 1").
		Scan(&s.AppVersion, &s.AppURL, &s.AppHash, &s.AppSHA256, &s.AppNotes, &s.AppSource, &s.AppFileName, &s.AppSize)
	if errors.Is(err, sql.ErrNoRows) {
		return Settings{}, false, nil
	}
	if err != nil {
		return Settings{}, false, fmt.Errorf("get settings: %w", err)
	}
	return s, true, nil
}

// SetSettings replaces the single settings row.
func (d *DB) SetSettings(s Settings) error {
	tx, err := d.conn.Begin()
	if err != nil {
		return fmt.Errorf("begin set settings: %w", err)
	}
	defer tx.Rollback()
	var id int64
	err = tx.QueryRow("SELECT id FROM setting ORDER BY id LIMIT 1").Scan(&id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := tx.Exec("INSERT INTO setting ("+settingColumns+") VALUES (?,?,?,?,?,?,?,?)",
			s.AppVersion, s.AppURL, s.AppHash, s.AppSHA256, s.AppNotes, s.AppSource, s.AppFileName, s.AppSize); err != nil {
			return fmt.Errorf("insert settings: %w", err)
		}
	case err != nil:
		return fmt.Errorf("find settings: %w", err)
	default:
		if _, err := tx.Exec("UPDATE setting SET app_version=?, app_url=?, app_hash=?, app_sha256=?, "+
			"app_notes=?, app_source=?, app_file_name=?, app_size=? WHERE id=?",
			s.AppVersion, s.AppURL, s.AppHash, s.AppSHA256, s.AppNotes, s.AppSource, s.AppFileName, s.AppSize, id); err != nil {
			return fmt.Errorf("update settings: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit set settings: %w", err)
	}
	return nil
}

// ImportSettings stores legacy settings only when nothing has been published
// yet, so a repeated import never overwrites what the admin saved.
func (d *DB) ImportSettings(s Settings) (bool, error) {
	tx, err := d.conn.Begin()
	if err != nil {
		return false, fmt.Errorf("begin import settings: %w", err)
	}
	defer tx.Rollback()
	var id int64
	err = tx.QueryRow("SELECT id FROM setting ORDER BY id LIMIT 1").Scan(&id)
	if err == nil {
		return false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("find settings: %w", err)
	}
	if _, err := tx.Exec("INSERT INTO setting ("+settingColumns+") VALUES (?,?,?,?,?,?,?,?)",
		s.AppVersion, s.AppURL, s.AppHash, s.AppSHA256, s.AppNotes, s.AppSource, s.AppFileName, s.AppSize); err != nil {
		return false, fmt.Errorf("import settings: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit import settings: %w", err)
	}
	return true, nil
}
