// Package db owns DATA_DIR/mesh.db. SQL appears only in this package.
package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	_ "modernc.org/sqlite"
)

// schema keeps every table with an auto-increment id as the primary key; the
// business keys are separate columns on top of it.
const schema = `
CREATE TABLE IF NOT EXISTS device (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    public_id        TEXT    NOT NULL UNIQUE,
    name             TEXT    NOT NULL DEFAULT '',
    device_id        TEXT    NOT NULL DEFAULT '',
    hostname         TEXT    NOT NULL DEFAULT '',
    pubkey           TEXT    NOT NULL DEFAULT '',
    config_toml      TEXT    NOT NULL DEFAULT '',
    config_hash      TEXT    NOT NULL DEFAULT '',
    token            TEXT    NOT NULL DEFAULT '',
    token_expires_at INTEGER NOT NULL DEFAULT 0,
    created_at       INTEGER NOT NULL DEFAULT 0,
    last_seen_at     INTEGER NOT NULL DEFAULT 0,
    last_ip          TEXT    NOT NULL DEFAULT '',
    last_config_hash TEXT    NOT NULL DEFAULT '',
    last_app_version TEXT    NOT NULL DEFAULT '',
    group_id         INTEGER NOT NULL DEFAULT 0,
    position         INTEGER NOT NULL DEFAULT 0,
    auto_update      INTEGER
);

CREATE TABLE IF NOT EXISTS device_group (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    name     TEXT    NOT NULL DEFAULT '',
    position INTEGER NOT NULL DEFAULT 0
);

CREATE UNIQUE INDEX IF NOT EXISTS device_device_id ON device (device_id) WHERE device_id <> '';

CREATE TABLE IF NOT EXISTS release (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    platform         TEXT    NOT NULL UNIQUE,
    version          TEXT    NOT NULL DEFAULT '',
    url              TEXT    NOT NULL DEFAULT '',
    sha256           TEXT    NOT NULL DEFAULT '',
    notes            TEXT    NOT NULL DEFAULT '',
    auto_update      BOOLEAN NOT NULL DEFAULT 0,
    launcher_version TEXT    NOT NULL DEFAULT '',
    launcher_url     TEXT    NOT NULL DEFAULT '',
    launcher_sha256  TEXT    NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS credential (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    credential_id    TEXT    NOT NULL UNIQUE,  -- 种子上的标识（uuid4），撤销与续期用
    device_public_id TEXT    NOT NULL DEFAULT '',
    expires_at       INTEGER NOT NULL DEFAULT 0,
    created_at       INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS credential_device ON credential (device_public_id);
`

// DB is the single connection to the SQLite file.
type DB struct {
	conn *sql.DB
}

// Open opens path and creates the tables when they are missing.
func Open(path string) (*DB, error) {
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	// One file, one connection: WAL still lets /readyz read while a write runs.
	conn.SetMaxOpenConns(1)
	for _, pragma := range []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = FULL",
		"PRAGMA busy_timeout = 5000",
	} {
		if _, err := conn.Exec(pragma); err != nil {
			conn.Close()
			return nil, fmt.Errorf("apply %q: %w", pragma, err)
		}
	}
	d := &DB{conn: conn}
	if err := d.createSchema(); err != nil {
		conn.Close()
		return nil, err
	}
	return d, nil
}

// Close closes the database file.
func (d *DB) Close() error {
	return d.conn.Close()
}

func (d *DB) createSchema() error {
	for statement := range strings.SplitSeq(schema, ";") {
		statement = strings.TrimSpace(statement)
		if statement == "" {
			continue
		}
		if _, err := d.conn.Exec(statement); err != nil {
			return fmt.Errorf("create schema: %w", err)
		}
	}
	// Columns added after the table first shipped: SQLite has no
	// ADD COLUMN IF NOT EXISTS, so they are checked one by one.
	if err := d.ensureColumn("device", "last_platform", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := d.ensureColumn("device", "group_id", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := d.ensureColumn("device", "position", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	// 设备级自动更新覆盖：NULL 表示跟随发布包，0 强制手动，1 强制自动。
	if err := d.ensureColumn("device", "auto_update", "INTEGER"); err != nil {
		return err
	}
	if err := d.ensureColumn("release", "launcher_version", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := d.ensureColumn("release", "launcher_url", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := d.ensureColumn("release", "launcher_sha256", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	// A database written before the package-level switch existed keeps every
	// package it published as manual: the column arrives as 0.
	if err := d.ensureColumn("release", "auto_update", "BOOLEAN NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := d.renameAppHashColumn(); err != nil {
		return err
	}
	if err := d.dropReleaseHashColumn(); err != nil {
		return err
	}
	return d.dropLegacySchema()
}

// renameAppHashColumn turns device.last_app_hash into last_app_version, the
// field that now holds the version string clients report. A pre-migration row
// holds a 16-character package digest: it is cleared because as a version it is
// meaningless and would only be shown as one until the next heartbeat.
func (d *DB) renameAppHashColumn() error {
	found, err := d.hasColumn("device", "last_app_hash")
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	if _, err := d.conn.Exec("ALTER TABLE device RENAME COLUMN last_app_hash TO last_app_version"); err != nil {
		return fmt.Errorf("rename device.last_app_hash: %w", err)
	}
	if _, err := d.conn.Exec("UPDATE device SET last_app_version = '' WHERE last_app_version <> ''"); err != nil {
		return fmt.Errorf("clear device.last_app_version: %w", err)
	}
	return nil
}

// dropReleaseHashColumn removes release.hash, the 16-character package digest
// clients used to report. The version string in release.version is what the
// control plane compares now; release.sha256 stays for download verification.
func (d *DB) dropReleaseHashColumn() error {
	found, err := d.hasColumn("release", "hash")
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	if _, err := d.conn.Exec("ALTER TABLE release DROP COLUMN hash"); err != nil {
		return fmt.Errorf("drop release.hash: %w", err)
	}
	return nil
}

// dropLegacySchema removes what the pre-SQLite import needed. The import is
// gone, so the column, its unique index and the settings table would only keep
// their old rows alive without a reader.
func (d *DB) dropLegacySchema() error {
	if _, err := d.conn.Exec("DROP INDEX IF EXISTS device_legacy_id"); err != nil {
		return fmt.Errorf("drop device_legacy_id: %w", err)
	}
	found, err := d.hasColumn("device", "legacy_id")
	if err != nil {
		return err
	}
	if found {
		if _, err := d.conn.Exec("ALTER TABLE device DROP COLUMN legacy_id"); err != nil {
			return fmt.Errorf("drop device.legacy_id: %w", err)
		}
	}
	if _, err := d.conn.Exec("DROP TABLE IF EXISTS setting"); err != nil {
		return fmt.Errorf("drop setting: %w", err)
	}
	return nil
}

// ensureColumn adds column to table when the table does not have it yet.
func (d *DB) ensureColumn(table, column, decl string) error {
	found, err := d.hasColumn(table, column)
	if err != nil {
		return err
	}
	if found {
		return nil
	}
	if _, err := d.conn.Exec("ALTER TABLE " + table + " ADD COLUMN " + column + " " + decl); err != nil {
		return fmt.Errorf("add %s.%s: %w", table, column, err)
	}
	return nil
}

// hasColumn reports whether table has column. The connection pool holds a single
// connection, so the PRAGMA rows must be closed before any ALTER runs.
func (d *DB) hasColumn(table, column string) (bool, error) {
	rows, err := d.conn.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return false, fmt.Errorf("inspect %s: %w", table, err)
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var (
			cid, notNull, pk int
			name, ctype      string
			dflt             any
		)
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			return false, fmt.Errorf("inspect %s: %w", table, err)
		}
		if name == column {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("inspect %s: %w", table, err)
	}
	return found, nil
}

// Probe proves that the file is readable and writable by writing to and
// deleting from the __probe table (the /readyz check).
func (d *DB) Probe(ctx context.Context) error {
	if _, err := d.conn.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS __probe (id INTEGER PRIMARY KEY AUTOINCREMENT, at INTEGER NOT NULL)"); err != nil {
		return fmt.Errorf("create __probe: %w", err)
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin __probe: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO __probe (at) VALUES (?)", nowUnix()); err != nil {
		tx.Rollback()
		return fmt.Errorf("write __probe: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM __probe"); err != nil {
		tx.Rollback()
		return fmt.Errorf("delete __probe: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit __probe: %w", err)
	}
	return nil
}
