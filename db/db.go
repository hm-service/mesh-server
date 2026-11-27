// Package db owns DATA_DIR/mesh.db. SQL appears only in this package.
package db

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
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

// verifySchema compares every table declared in schema with the structure the
// database file actually has: columns (name, type, nullability, default, primary
// key) and indexes (name, uniqueness, partiality, columns). A difference is fatal
// and is not repaired here: deciding what an older file's data means is an
// operator's job, and repairing it silently would hide a deployment that has not
// been migrated.
//
// The reference is the same DDL, applied to an in-memory file: SQLite parses it,
// so there is no second hand-written list of columns in Go to keep in sync.
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
		if err := compareTable(reference, d.conn, table); err != nil {
			return fmt.Errorf("database schema does not match db/db.go: %w", err)
		}
	}
	return nil
}

// compareTable checks one declared table against the stored one.
func compareTable(want, got *sql.DB, table string) error {
	wantColumns, err := tableColumns(want, table)
	if err != nil {
		return err
	}
	gotColumns, err := tableColumns(got, table)
	if err != nil {
		return err
	}
	if len(gotColumns) == 0 {
		return fmt.Errorf("table %s is missing", table)
	}
	if err := compareColumns(table, wantColumns, gotColumns); err != nil {
		return err
	}
	wantIndexes, err := tableIndexes(want, table)
	if err != nil {
		return err
	}
	gotIndexes, err := tableIndexes(got, table)
	if err != nil {
		return err
	}
	return compareIndexes(table, wantIndexes, gotIndexes)
}

// compareColumns compares the two column lists by name and, for each name, by
// every attribute that decides what the column may hold.
func compareColumns(table string, want, got []columnShape) error {
	byName := make(map[string]columnShape, len(got))
	for _, column := range got {
		byName[column.Name] = column
	}
	wanted := make(map[string]struct{}, len(want))
	for _, column := range want {
		wanted[column.Name] = struct{}{}
		actual, ok := byName[column.Name]
		if !ok {
			return fmt.Errorf("table %s has no column %s", table, column.Name)
		}
		if diff := columnDifference(column, actual); diff != "" {
			return fmt.Errorf("table %s column %s: %s", table, column.Name, diff)
		}
	}
	for _, column := range got {
		if _, ok := wanted[column.Name]; !ok {
			return fmt.Errorf("table %s has unexpected column %s", table, column.Name)
		}
	}
	return nil
}

// columnDifference describes how one column departs from the declaration; an
// empty string means it matches.
func columnDifference(want, got columnShape) string {
	var diff []string
	if want.Type != got.Type {
		diff = append(diff, fmt.Sprintf("type %s, want %s", got.Type, want.Type))
	}
	if want.NotNull != got.NotNull {
		diff = append(diff, fmt.Sprintf("not null %t, want %t", got.NotNull, want.NotNull))
	}
	if want.Default != got.Default {
		diff = append(diff, fmt.Sprintf("default %q, want %q", got.Default, want.Default))
	}
	if want.PK != got.PK {
		diff = append(diff, fmt.Sprintf("primary key %d, want %d", got.PK, want.PK))
	}
	return strings.Join(diff, "; ")
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

// columnShape is one column as SQLite declares it. The type is upper-cased and
// the default expression is lower-cased and trimmed, so a hand migration that
// wrote the same thing in another case or with parentheses around the literal
// compares equal.
type columnShape struct {
	Name    string
	Type    string
	NotNull bool
	Default string
	PK      int
}

// tableColumns returns the columns of table in declaration order. A missing
// table yields no columns; callers compare that like any other mismatch.
func tableColumns(conn *sql.DB, table string) ([]columnShape, error) {
	rows, err := conn.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var columns []columnShape
	for rows.Next() {
		var (
			cid, notNull, pk int
			name, ctype      string
			dflt             any
		)
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			return nil, err
		}
		columns = append(columns, columnShape{
			Name:    name,
			Type:    strings.ToUpper(strings.TrimSpace(ctype)),
			NotNull: notNull != 0,
			Default: defaultValue(dflt),
			PK:      pk,
		})
	}
	return columns, rows.Err()
}

// defaultValue normalises what PRAGMA table_info reports for a default: SQLite
// keeps the literal as written, so DEFAULT 0, DEFAULT(0) and default 'x' style
// differences must not read as a schema mismatch.
func defaultValue(raw any) string {
	if raw == nil {
		return ""
	}
	value := strings.ToLower(strings.TrimSpace(fmt.Sprint(raw)))
	for len(value) >= 2 && value[0] == '(' && value[len(value)-1] == ')' {
		value = strings.TrimSpace(value[1 : len(value)-1])
	}
	return value
}

// indexShape is one index as SQLite declares it. Origin tells a named
// CREATE INDEX ("c") from the implicit index behind a UNIQUE constraint ("u");
// the latter gets a generated name, so it is compared by shape instead of name.
type indexShape struct {
	Name    string
	Unique  bool
	Partial bool
	Columns []string
	Origin  string
}

// tableIndexes returns every index of table except the rowid primary key. The
// index list is read to completion before the columns are looked up: this
// package opens the database with a single connection, so a nested query while
// a result set is still open would wait for a connection that cannot come.
func tableIndexes(conn *sql.DB, table string) ([]indexShape, error) {
	listed, err := indexList(conn, table)
	if err != nil {
		return nil, err
	}
	indexes := make([]indexShape, 0, len(listed))
	for _, index := range listed {
		columns, err := indexColumns(conn, index.Name)
		if err != nil {
			return nil, err
		}
		index.Columns = columns
		indexes = append(indexes, index)
	}
	return indexes, nil
}

// indexList reads PRAGMA index_list into memory, without its columns.
func indexList(conn *sql.DB, table string) ([]indexShape, error) {
	rows, err := conn.Query("PRAGMA index_list(" + table + ")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var indexes []indexShape
	for rows.Next() {
		var (
			seq, unique, partial int
			name, origin         string
		)
		if err := rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			return nil, err
		}
		if origin == "pk" {
			continue
		}
		indexes = append(indexes, indexShape{
			Name:    name,
			Unique:  unique != 0,
			Partial: partial != 0,
			Origin:  origin,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return indexes, nil
}

// indexColumns returns the indexed columns in index order. An expression index
// (which this schema never declares) reports NULL for its column name.
func indexColumns(conn *sql.DB, name string) ([]string, error) {
	rows, err := conn.Query("PRAGMA index_info(" + name + ")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var (
			seqno, cid int
			column     sql.NullString
		)
		if err := rows.Scan(&seqno, &cid, &column); err != nil {
			return nil, err
		}
		value := "<expression>"
		if column.Valid {
			value = column.String
		}
		columns = append(columns, value)
	}
	return columns, rows.Err()
}

// compareIndexes checks the indexes the declaration names, and the implicit
// UNIQUE indexes as a multiset of shapes: their generated names depend on the
// declaration order, which is not part of the contract.
//
// An extra named index is ignored: it changes no behaviour the code depends on,
// and an operator may have added one for a query. An extra UNIQUE constraint is
// not ignored, because it changes what the table accepts.
func compareIndexes(table string, want, got []indexShape) error {
	wantNamed, wantUnique := splitIndexes(want)
	gotNamed, gotUnique := splitIndexes(got)
	for name, wantIndex := range wantNamed {
		gotIndex, ok := gotNamed[name]
		if !ok {
			return fmt.Errorf("table %s has no index %s", table, name)
		}
		if diff := indexDifference(wantIndex, gotIndex); diff != "" {
			return fmt.Errorf("table %s index %s: %s", table, name, diff)
		}
	}
	for _, key := range sortedCountKeys(wantUnique, gotUnique) {
		switch {
		case wantUnique[key] > gotUnique[key]:
			return fmt.Errorf("table %s is missing a unique constraint: %s", table, key)
		case gotUnique[key] > wantUnique[key]:
			return fmt.Errorf("table %s has an unexpected unique constraint: %s", table, key)
		}
	}
	return nil
}

// splitIndexes separates the named indexes into a map and the implicit UNIQUE
// indexes into a count per shape.
func splitIndexes(indexes []indexShape) (map[string]indexShape, map[string]int) {
	named := make(map[string]indexShape)
	unique := make(map[string]int)
	for _, index := range indexes {
		if index.Origin == "u" {
			unique[uniqueKey(index)]++
			continue
		}
		named[index.Name] = index
	}
	return named, unique
}

// uniqueKey is the shape of an implicit UNIQUE index, without its generated name.
func uniqueKey(index indexShape) string {
	return fmt.Sprintf("unique %t, partial %t, columns %s", index.Unique, index.Partial, strings.Join(index.Columns, ","))
}

// sortedCountKeys returns every key of both counts in a stable order, so the
// reported mismatch does not change between runs.
func sortedCountKeys(a, b map[string]int) []string {
	keys := make([]string, 0, len(a)+len(b))
	for key := range a {
		keys = append(keys, key)
	}
	for key := range b {
		if _, ok := a[key]; !ok {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

// indexDifference describes how one named index departs from the declaration.
func indexDifference(want, got indexShape) string {
	var diff []string
	if want.Unique != got.Unique {
		diff = append(diff, fmt.Sprintf("unique %t, want %t", got.Unique, want.Unique))
	}
	if want.Partial != got.Partial {
		diff = append(diff, fmt.Sprintf("partial %t, want %t", got.Partial, want.Partial))
	}
	if strings.Join(want.Columns, ",") != strings.Join(got.Columns, ",") {
		diff = append(diff, fmt.Sprintf("columns %s, want %s",
			strings.Join(got.Columns, ","), strings.Join(want.Columns, ",")))
	}
	return strings.Join(diff, "; ")
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
