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
    legacy_id        TEXT    NOT NULL DEFAULT '',
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
    last_app_hash    TEXT    NOT NULL DEFAULT ''
);

CREATE UNIQUE INDEX IF NOT EXISTS device_legacy_id ON device (legacy_id) WHERE legacy_id <> '';
CREATE UNIQUE INDEX IF NOT EXISTS device_device_id ON device (device_id) WHERE device_id <> '';

CREATE TABLE IF NOT EXISTS release (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    platform  TEXT    NOT NULL UNIQUE,
    version   TEXT    NOT NULL DEFAULT '',
    url       TEXT    NOT NULL DEFAULT '',
    hash      TEXT    NOT NULL DEFAULT '',
    sha256    TEXT    NOT NULL DEFAULT '',
    notes     TEXT    NOT NULL DEFAULT '',
    source    TEXT    NOT NULL DEFAULT '',
    file_name TEXT    NOT NULL DEFAULT '',
    size      INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS setting (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    app_version   TEXT    NOT NULL DEFAULT '',
    app_url       TEXT    NOT NULL DEFAULT '',
    app_hash      TEXT    NOT NULL DEFAULT '',
    app_sha256    TEXT    NOT NULL DEFAULT '',
    app_notes     TEXT    NOT NULL DEFAULT '',
    app_source    TEXT    NOT NULL DEFAULT '',
    app_file_name TEXT    NOT NULL DEFAULT '',
    app_size      INTEGER NOT NULL DEFAULT 0
);
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
	return nil
}

// ensureColumn adds column to table when the table does not have it yet.
// The connection pool holds a single connection, so the PRAGMA rows must be
// closed before the ALTER runs or it would wait forever.
func (d *DB) ensureColumn(table, column, decl string) error {
	rows, err := d.conn.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return fmt.Errorf("inspect %s: %w", table, err)
	}
	found := false
	for rows.Next() {
		var (
			cid, notNull, pk int
			name, ctype      string
			dflt             any
		)
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			rows.Close()
			return fmt.Errorf("inspect %s: %w", table, err)
		}
		if name == column {
			found = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return fmt.Errorf("inspect %s: %w", table, err)
	}
	if found {
		return nil
	}
	if _, err := d.conn.Exec("ALTER TABLE " + table + " ADD COLUMN " + column + " " + decl); err != nil {
		return fmt.Errorf("add %s.%s: %w", table, column, err)
	}
	return nil
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
