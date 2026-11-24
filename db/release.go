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

// SetRelease inserts or replaces both slots of its platform. Importing legacy
// data is its only caller: it must not drop a slot the admin already published.
func (d *DB) SetRelease(r Release) error {
	_, err := d.conn.Exec("INSERT INTO release ("+releaseColumns+") VALUES (?,?,?,?,?,?,?,?,?) "+
		"ON CONFLICT(platform) DO UPDATE SET version=excluded.version, url=excluded.url, "+
		"hash=excluded.hash, sha256=excluded.sha256, notes=excluded.notes, "+
		"launcher_version=excluded.launcher_version, launcher_url=excluded.launcher_url, "+
		"launcher_sha256=excluded.launcher_sha256",
		r.Platform, r.Version, r.URL, r.Hash, r.SHA256, r.Notes,
		r.LauncherVersion, r.LauncherURL, r.LauncherSHA256)
	if err != nil {
		return fmt.Errorf("set release: %w", err)
	}
	return nil
}

// SetClientRelease inserts or replaces only the client package slot of its
// platform; the launcher slot keeps whatever it already had.
func (d *DB) SetClientRelease(r Release) error {
	_, err := d.conn.Exec("INSERT INTO release (platform, version, url, hash, sha256, notes) VALUES (?,?,?,?,?,?) "+
		"ON CONFLICT(platform) DO UPDATE SET version=excluded.version, url=excluded.url, "+
		"hash=excluded.hash, sha256=excluded.sha256, notes=excluded.notes",
		r.Platform, r.Version, r.URL, r.Hash, r.SHA256, r.Notes)
	if err != nil {
		return fmt.Errorf("set client release: %w", err)
	}
	return nil
}

// SetLauncherRelease inserts or replaces only the launcher slot of platform; the
// client package slot keeps whatever it already had.
func (d *DB) SetLauncherRelease(platform, version, url, sha string) error {
	_, err := d.conn.Exec("INSERT INTO release (platform, launcher_version, launcher_url, launcher_sha256) VALUES (?,?,?,?) "+
		"ON CONFLICT(platform) DO UPDATE SET launcher_version=excluded.launcher_version, "+
		"launcher_url=excluded.launcher_url, launcher_sha256=excluded.launcher_sha256",
		platform, version, url, sha)
	if err != nil {
		return fmt.Errorf("set launcher release: %w", err)
	}
	return nil
}

// ImportRelease stores a release only when its platform has nothing published,
// so a repeated import never overwrites what the admin saved.
func (d *DB) ImportRelease(r Release) (bool, error) {
	if r.Version == "" {
		return false, nil
	}
	existing, found, err := d.Release(r.Platform)
	if err != nil {
		return false, err
	}
	if found && existing.Version != "" {
		return false, nil
	}
	if err := d.SetRelease(r); err != nil {
		return false, err
	}
	return true, nil
}

// DeleteRelease removes every slot of platform.
func (d *DB) DeleteRelease(platform string) error {
	if _, err := d.conn.Exec("DELETE FROM release WHERE platform = ?", platform); err != nil {
		return fmt.Errorf("delete release: %w", err)
	}
	return nil
}

// ClearLauncher blanks only the launcher slot of platform. A row whose client
// package is not published either is dropped: releases only lists what exists.
func (d *DB) ClearLauncher(platform string) error {
	if _, err := d.conn.Exec("UPDATE release SET launcher_version = '', launcher_url = '', launcher_sha256 = '' "+
		"WHERE platform = ?", platform); err != nil {
		return fmt.Errorf("clear launcher: %w", err)
	}
	if _, err := d.conn.Exec("DELETE FROM release WHERE platform = ? AND version = ''", platform); err != nil {
		return fmt.Errorf("clear launcher: %w", err)
	}
	return nil
}

// ImportLegacyRelease moves the pre-platform setting row into release for the
// given platform, once. A populated release table means this already ran.
func (d *DB) ImportLegacyRelease(platform string) (bool, error) {
	var count int
	if err := d.conn.QueryRow("SELECT COUNT(*) FROM release").Scan(&count); err != nil {
		return false, fmt.Errorf("count releases: %w", err)
	}
	if count > 0 {
		return false, nil
	}
	var r Release
	err := d.conn.QueryRow("SELECT app_version, app_url, app_hash, app_sha256, app_notes "+
		"FROM setting ORDER BY id LIMIT 1").
		Scan(&r.Version, &r.URL, &r.Hash, &r.SHA256, &r.Notes)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read legacy settings: %w", err)
	}
	if r.Version == "" {
		return false, nil
	}
	r.Platform = platform
	if err := d.SetRelease(r); err != nil {
		return false, err
	}
	return true, nil
}
