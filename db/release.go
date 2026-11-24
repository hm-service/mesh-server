package db

import (
	"database/sql"
	"errors"
	"fmt"
)

// Release is the published build for one platform. Hash is the first 16 hex
// characters of SHA256 and is what clients report in their heartbeat; SHA256 is
// the full digest they verify the download against.
type Release struct {
	Platform string
	Version  string
	URL      string
	Hash     string
	SHA256   string
	Notes    string
	Source   string
	FileName string
	Size     int64
}

const releaseColumns = "platform, version, url, hash, sha256, notes, source, file_name, size"

// Release returns the published build for platform.
func (d *DB) Release(platform string) (Release, bool, error) {
	var r Release
	err := d.conn.QueryRow("SELECT "+releaseColumns+" FROM release WHERE platform = ?", platform).
		Scan(&r.Platform, &r.Version, &r.URL, &r.Hash, &r.SHA256, &r.Notes, &r.Source, &r.FileName, &r.Size)
	if errors.Is(err, sql.ErrNoRows) {
		return Release{}, false, nil
	}
	if err != nil {
		return Release{}, false, fmt.Errorf("get release: %w", err)
	}
	return r, true, nil
}

// Releases returns every published build ordered by platform.
func (d *DB) Releases() ([]Release, error) {
	rows, err := d.conn.Query("SELECT " + releaseColumns + " FROM release ORDER BY platform")
	if err != nil {
		return nil, fmt.Errorf("list releases: %w", err)
	}
	defer rows.Close()
	out := make([]Release, 0, 4)
	for rows.Next() {
		var r Release
		if err := rows.Scan(&r.Platform, &r.Version, &r.URL, &r.Hash, &r.SHA256, &r.Notes, &r.Source, &r.FileName, &r.Size); err != nil {
			return nil, fmt.Errorf("scan release: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetRelease inserts or replaces the build of its platform.
func (d *DB) SetRelease(r Release) error {
	_, err := d.conn.Exec("INSERT INTO release ("+releaseColumns+") VALUES (?,?,?,?,?,?,?,?,?) "+
		"ON CONFLICT(platform) DO UPDATE SET version=excluded.version, url=excluded.url, "+
		"hash=excluded.hash, sha256=excluded.sha256, notes=excluded.notes, source=excluded.source, "+
		"file_name=excluded.file_name, size=excluded.size",
		r.Platform, r.Version, r.URL, r.Hash, r.SHA256, r.Notes, r.Source, r.FileName, r.Size)
	if err != nil {
		return fmt.Errorf("set release: %w", err)
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

// DeleteRelease removes the build of platform.
func (d *DB) DeleteRelease(platform string) error {
	if _, err := d.conn.Exec("DELETE FROM release WHERE platform = ?", platform); err != nil {
		return fmt.Errorf("delete release: %w", err)
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
	err := d.conn.QueryRow("SELECT app_version, app_url, app_hash, app_sha256, app_notes, " +
		"app_source, app_file_name, app_size FROM setting ORDER BY id LIMIT 1").
		Scan(&r.Version, &r.URL, &r.Hash, &r.SHA256, &r.Notes, &r.Source, &r.FileName, &r.Size)
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
