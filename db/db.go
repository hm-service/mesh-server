// Package db owns DATA_DIR/mesh.db. SQL appears only in this package.
package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	_ "modernc.org/sqlite"
)

// schema is the complete current structure: one CREATE statement per table and
// index, with every column the code reads. It is the only declaration of that
// structure. Open applies it to a new file and then checks that an existing
// file has exactly these columns; the schema is never altered in place.
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
    last_platform    TEXT    NOT NULL DEFAULT '',
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
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    platform           TEXT    NOT NULL UNIQUE,
    version            TEXT    NOT NULL DEFAULT '',
    url                TEXT    NOT NULL DEFAULT '',
    sha256             TEXT    NOT NULL DEFAULT '',
    signature          TEXT    NOT NULL DEFAULT '',
    notes              TEXT    NOT NULL DEFAULT '',
    auto_update        BOOLEAN NOT NULL DEFAULT 0,
    launcher_version   TEXT    NOT NULL DEFAULT '',
    launcher_url       TEXT    NOT NULL DEFAULT '',
    launcher_sha256    TEXT    NOT NULL DEFAULT '',
    launcher_signature TEXT    NOT NULL DEFAULT ''
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

// Open opens path, creates the tables when they are missing and verifies that
// the stored schema matches the one declared above.
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
	if err := createSchema(conn); err != nil {
		conn.Close()
		return nil, err
	}
	d := &DB{conn: conn}
	if err := d.verifySchema(); err != nil {
		conn.Close()
		return nil, err
	}
	return d, nil
}

// Close closes the database file.
func (d *DB) Close() error {
	return d.conn.Close()
}

// createSchema runs the declared statements, which only ever create missing
// tables and indexes. An existing table is left exactly as it is.
func createSchema(conn *sql.DB) error {
	for statement := range strings.SplitSeq(schema, ";") {
		statement = strings.TrimSpace(statement)
		if statement == "" {
			continue
		}
		if _, err := conn.Exec(statement); err != nil {
			return fmt.Errorf("create schema: %w", err)
		}
	}
	return nil
}

// verifySchema compares every table declared in schema with the columns the
// database file actually has. A difference is fatal and is not repaired here:
// deciding what an older file's data means is an operator's job, and repairing
// it silently would hide a deployment that has not been migrated.
func (d *DB) verifySchema() error {
	reference, err := schemaReference()
	if err != nil {
		return fmt.Errorf("verify schema: %w", err)
	}
	defer reference.Close()
	tables, err := schemaTables(reference)
	if err != nil {
		return fmt.Errorf("verify schema: %w", err)
	}
	for _, table := range tables {
		want, err := tableColumns(reference, table)
		if err != nil {
			return fmt.Errorf("verify schema: %w", err)
		}
		got, err := tableColumns(d.conn, table)
		if err != nil {
			return fmt.Errorf("verify schema: %w", err)
		}
		missing, unexpected := columnDifference(want, got)
		if len(missing) == 0 && len(unexpected) == 0 {
			continue
		}
		var diff []string
		if len(missing) > 0 {
			diff = append(diff, "missing "+strings.Join(missing, ", "))
		}
		if len(unexpected) > 0 {
			diff = append(diff, "unexpected "+strings.Join(unexpected, ", "))
		}
		return fmt.Errorf("database schema does not match db/db.go: table %s: %s", table, strings.Join(diff, "; "))
	}
	return nil
}

// schemaReference builds the declared schema in an in-memory database and
// returns it. SQLite itself parses the same DDL the real file is compared
// against, so there is no second column list in Go to keep in sync.
func schemaReference() (*sql.DB, error) {
	conn, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, err
	}
	conn.SetMaxOpenConns(1)
	if err := createSchema(conn); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

// schemaTables lists the tables the declared schema creates.
func schemaTables(conn *sql.DB) ([]string, error) {
	rows, err := conn.Query("SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		tables = append(tables, name)
	}
	return tables, rows.Err()
}

// tableColumns returns the column names of table in declaration order. A
// missing table yields no columns; callers compare it like any other mismatch.
func tableColumns(conn *sql.DB, table string) ([]string, error) {
	rows, err := conn.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var (
			cid, notNull, pk int
			name, ctype      string
			dflt             any
		)
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// columnDifference returns the columns in want but not in got, then the columns
// in got but not in want.
func columnDifference(want, got []string) (missing, unexpected []string) {
	wantSet := make(map[string]struct{}, len(want))
	for _, name := range want {
		wantSet[name] = struct{}{}
	}
	gotSet := make(map[string]struct{}, len(got))
	for _, name := range got {
		gotSet[name] = struct{}{}
	}
	for _, name := range want {
		if _, ok := gotSet[name]; !ok {
			missing = append(missing, name)
		}
	}
	for _, name := range got {
		if _, ok := wantSet[name]; !ok {
			unexpected = append(unexpected, name)
		}
	}
	return missing, unexpected
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
