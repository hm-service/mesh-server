package db

import (
	"fmt"
)

// The two slot names of the release table. Each published artifact is one row,
// so (platform, slot) identifies it and the table never has to say which
// columns belong to which artifact.
const (
	slotClient   = "client"
	slotLauncher = "launcher"
)

// Release is what one platform publishes: the client package slot and the
// launcher slot. A slot is present exactly when its field is non-nil — a row
// exists only while its slot is published — so "published" and "the row
// exists" are the same fact.
//
// Signature is the Ed25519 signature over the slot's signing string (app
// package). It is stored, not re-derived: the control plane cannot sign, and a
// signature it recomputed would prove nothing.
//
// Version is the string clients report in their heartbeat and the control plane
// compares; SHA256 is the full digest they verify the download against. The
// launcher slot stores only the full digest: clients hash the launcher they
// already have themselves, so there is no reported version to compare against.
//
// AutoUpdate is the package-level switch clients follow: it says whether this
// build may install itself, not whether this device is unattended. It belongs to
// the client package slot because one platform publishes one package at a time.
type Release struct {
	Platform string
	Client   *ClientSlot
	Launcher *LauncherSlot
}

// ClientSlot is the client package slot of one platform: the version the
// package's manifest declares, the address clients download from, the digest of
// the bytes behind it and the policy attached to this package.
type ClientSlot struct {
	Version    string
	URL        string
	SHA256     string
	Signature  string
	Notes      string
	AutoUpdate bool
}

// LauncherSlot is the launcher slot of one platform. Only the full digest is
// stored: the client hashes the launcher it already has itself.
type LauncherSlot struct {
	Version   string
	URL       string
	SHA256    string
	Signature string
}

// releaseColumns is one stored slot row. (platform, slot) identifies it.
const releaseColumns = "platform, slot, version, url, sha256, signature, notes, auto_update"

// slotRow is one row of releaseColumns before it becomes a slot of Release.
type slotRow struct {
	platform   string
	slot       string
	version    string
	url        string
	sha256     string
	signature  string
	notes      string
	autoUpdate bool
}

func scanSlotRow(row interface{ Scan(...any) error }) (slotRow, error) {
	var r slotRow
	err := row.Scan(&r.platform, &r.slot, &r.version, &r.url, &r.sha256, &r.signature, &r.notes, &r.autoUpdate)
	return r, err
}

// attach stores the row as the slot of release it names. Notes and the
// auto-update switch belong to the client package only; a launcher row is held
// to the empty values by the table's CHECK constraint, so they are not read.
func (r slotRow) attach(release *Release) error {
	release.Platform = r.platform
	switch r.slot {
	case slotClient:
		release.Client = &ClientSlot{Version: r.version, URL: r.url, SHA256: r.sha256,
			Signature: r.signature, Notes: r.notes, AutoUpdate: r.autoUpdate}
	case slotLauncher:
		release.Launcher = &LauncherSlot{Version: r.version, URL: r.url, SHA256: r.sha256,
			Signature: r.signature}
	default:
		return fmt.Errorf("release row of %s has unknown slot %q", r.platform, r.slot)
	}
	return nil
}

// Release returns the slots published for platform; found is false when the
// platform publishes nothing.
func (d *DB) Release(platform string) (Release, bool, error) {
	rows, err := d.conn.Query("SELECT "+releaseColumns+" FROM release WHERE platform = ? ORDER BY slot", platform)
	if err != nil {
		return Release{}, false, fmt.Errorf("get release: %w", err)
	}
	defer rows.Close()
	var release Release
	found := false
	for rows.Next() {
		row, err := scanSlotRow(rows)
		if err != nil {
			return Release{}, false, fmt.Errorf("scan release: %w", err)
		}
		if err := row.attach(&release); err != nil {
			return Release{}, false, err
		}
		found = true
	}
	if err := rows.Err(); err != nil {
		return Release{}, false, fmt.Errorf("read release: %w", err)
	}
	if !found {
		return Release{}, false, nil
	}
	return release, true, nil
}

// Releases returns every published platform ordered by platform.
func (d *DB) Releases() ([]Release, error) {
	rows, err := d.conn.Query("SELECT " + releaseColumns + " FROM release ORDER BY platform, slot")
	if err != nil {
		return nil, fmt.Errorf("list releases: %w", err)
	}
	defer rows.Close()
	out := make([]Release, 0, 4)
	for rows.Next() {
		row, err := scanSlotRow(rows)
		if err != nil {
			return nil, fmt.Errorf("scan release: %w", err)
		}
		// Rows of one platform arrive next to each other, so the aggregate is
		// the last element whenever its platform is already open.
		if len(out) == 0 || out[len(out)-1].Platform != row.platform {
			out = append(out, Release{Platform: row.platform})
		}
		if err := row.attach(&out[len(out)-1]); err != nil {
			return nil, err
		}
	}
	return out, rows.Err()
}

// PublishSlots writes the given slots of platform in one transaction, so a
// request that publishes both of them can never leave one behind: either both
// rows are stored or neither is. A nil slot is left as it is; a published slot
// creates its row or replaces the one it already had.
func (d *DB) PublishSlots(platform string, client *ClientSlot, launcher *LauncherSlot) error {
	if client == nil && launcher == nil {
		return nil
	}
	tx, err := d.conn.Begin()
	if err != nil {
		return fmt.Errorf("begin publish: %w", err)
	}
	defer tx.Rollback()
	if client != nil {
		if _, err := tx.Exec("INSERT INTO release (platform, slot, version, url, sha256, signature, notes, auto_update) "+
			"VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(platform, slot) DO UPDATE SET "+
			"version = excluded.version, url = excluded.url, sha256 = excluded.sha256, "+
			"signature = excluded.signature, notes = excluded.notes, auto_update = excluded.auto_update",
			platform, slotClient, client.Version, client.URL, client.SHA256, client.Signature,
			client.Notes, client.AutoUpdate); err != nil {
			return fmt.Errorf("publish client slot: %w", err)
		}
	}
	if launcher != nil {
		// notes and auto_update are not written: their defaults are the only
		// values the CHECK constraint accepts on a launcher row.
		if _, err := tx.Exec("INSERT INTO release (platform, slot, version, url, sha256, signature) "+
			"VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT(platform, slot) DO UPDATE SET "+
			"version = excluded.version, url = excluded.url, sha256 = excluded.sha256, "+
			"signature = excluded.signature",
			platform, slotLauncher, launcher.Version, launcher.URL, launcher.SHA256, launcher.Signature); err != nil {
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

// ClearLauncher removes the launcher slot of platform. The client package is a
// different row and is not touched.
func (d *DB) ClearLauncher(platform string) error {
	if _, err := d.conn.Exec("DELETE FROM release WHERE platform = ? AND slot = ?", platform, slotLauncher); err != nil {
		return fmt.Errorf("clear launcher: %w", err)
	}
	return nil
}
