package db

import (
	"database/sql"
	"errors"
	"fmt"
)

// Release is what one platform publishes: the client package (Version, URL,
// Hash, SHA256, Notes) and the launcher (LauncherVersion, LauncherURL,
// LauncherSHA256). The two slots are written and cleared independently, so a row
// exists as long as at least one of them is published.
//
// Hash is the first 16 hex characters of the client package SHA256 and is what
// clients report in their heartbeat; SHA256 is the full digest they verify the
// download against. The launcher slot stores only the full digest: clients hash
// the launcher they already have themselves, so there is no reported hash to
// compare against.
type Release struct {
	Platform        string
	Version         string
	URL             string
	Hash            string
	SHA256          string
	Notes           string
	LauncherVersion string
	LauncherURL     string
	LauncherSHA256  string
}

// ClientSlot is the client package slot of one platform: the address clients
// download from and the digests of the bytes behind it.
type ClientSlot struct {
	Version string
	URL     string
	Hash    string
	SHA256  string
	Notes   string
}

// LauncherSlot is the launcher slot of one platform. Only the full digest is
// stored: the client hashes the launcher it already has itself.
type LauncherSlot struct {
	Version string
	URL     string
	SHA256  string
}

const releaseColumns = "platform, version, url, hash, sha256, notes, launcher_version, launcher_url, launcher_sha256"

// scanRelease reads one row of releaseColumns.
func scanRelease(row interface{ Scan(...any) error }) (Release, error) {
	var r Release
	err := row.Scan(&r.Platform, &r.Version, &r.URL, &r.Hash, &r.SHA256, &r.Notes,
		&r.LauncherVersion, &r.LauncherURL, &r.LauncherSHA256)
	return r, err
}

// Release returns both slots published for platform.
func (d *DB) Release(platform string) (Release, bool, error) {
	r, err := scanRelease(d.conn.QueryRow("SELECT "+releaseColumns+" FROM release WHERE platform = ?", platform))
	if errors.Is(err, sql.ErrNoRows) {
		return Release{}, false, nil
	}
	if err != nil {
		return Release{}, false, fmt.Errorf("get release: %w", err)
	}
	return r, true, nil
}

// Releases returns every published platform ordered by platform.
func (d *DB) Releases() ([]Release, error) {
	rows, err := d.conn.Query("SELECT " + releaseColumns + " FROM release ORDER BY platform")
	if err != nil {
		return nil, fmt.Errorf("list releases: %w", err)
	}
	defer rows.Close()
	out := make([]Release, 0, 4)
	for rows.Next() {
		r, err := scanRelease(rows)
		if err != nil {
			return nil, fmt.Errorf("scan release: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// PublishSlots writes the given slots of platform in one transaction, so a
// request that publishes both of them can never leave one behind: either both
// slots are stored or neither is. A nil slot keeps what the row already had, and
// the row is created when the platform is new.
func (d *DB) PublishSlots(platform string, client *ClientSlot, launcher *LauncherSlot) error {
	if client == nil && launcher == nil {
		return nil
	}
	tx, err := d.conn.Begin()
	if err != nil {
		return fmt.Errorf("begin publish: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec("INSERT INTO release (platform) VALUES (?) ON CONFLICT(platform) DO NOTHING", platform); err != nil {
		return fmt.Errorf("create release row: %w", err)
	}
	if client != nil {
		if _, err := tx.Exec("UPDATE release SET version = ?, url = ?, hash = ?, sha256 = ?, notes = ? WHERE platform = ?",
			client.Version, client.URL, client.Hash, client.SHA256, client.Notes, platform); err != nil {
			return fmt.Errorf("publish client slot: %w", err)
		}
	}
	if launcher != nil {
		if _, err := tx.Exec("UPDATE release SET launcher_version = ?, launcher_url = ?, launcher_sha256 = ? WHERE platform = ?",
			launcher.Version, launcher.URL, launcher.SHA256, platform); err != nil {
			return fmt.Errorf("publish launcher slot: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit publish: %w", err)
	}
	return nil
}

// DeleteRelease removes every slot of platform.
func (d *DB) DeleteRelease(platform string) error {
	if _, err := d.conn.Exec("DELETE FROM release WHERE platform = ?", platform); err != nil {
		return fmt.Errorf("delete release: %w", err)
	}
	return nil
}

// ClearLauncher blanks only the launcher slot of platform, in one transaction:
// a row whose client package is not published either is dropped, and a launcher
// published concurrently is never deleted by the drop.
func (d *DB) ClearLauncher(platform string) error {
	tx, err := d.conn.Begin()
	if err != nil {
		return fmt.Errorf("begin clear launcher: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec("UPDATE release SET launcher_version = '', launcher_url = '', launcher_sha256 = '' "+
		"WHERE platform = ?", platform); err != nil {
		return fmt.Errorf("clear launcher: %w", err)
	}
	if _, err := tx.Exec("DELETE FROM release WHERE platform = ? AND version = ''", platform); err != nil {
		return fmt.Errorf("clear launcher: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit clear launcher: %w", err)
	}
	return nil
}
