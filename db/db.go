// Package db owns DATA_DIR/mesh.db. SQL appears only in this package.
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"

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
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    platform    TEXT    NOT NULL,
    slot        TEXT    NOT NULL CHECK (slot IN ('client', 'launcher')),
    version     TEXT    NOT NULL DEFAULT '',
    url         TEXT    NOT NULL DEFAULT '',
    sha256      TEXT    NOT NULL DEFAULT '',
    signature   TEXT    NOT NULL DEFAULT '',
    -- 只对 client 槽位有意义；launcher 行必须留空。
    notes       TEXT    NOT NULL DEFAULT '',
    auto_update BOOLEAN NOT NULL DEFAULT 0,
    CHECK (slot = 'client' OR (notes = '' AND auto_update = 0))
);

CREATE UNIQUE INDEX IF NOT EXISTS release_slot ON release (platform, slot);

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
// database file actually has. Columns, named indexes, the implicit UNIQUE
// constraints and the CHECK constraints behind a declaration all normalise to
// the same comparable entry, so one diff covers the whole structure instead of a
// pass per layer. A
// difference is fatal and is not repaired here: deciding what an older file's
// data means is an operator's job, and repairing it silently would hide a
// deployment that has not been migrated.
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
		want, err := tableEntries(reference, table, nil)
		if err != nil {
			return fmt.Errorf("verify schema: %w", err)
		}
		got, err := tableEntries(d.conn, table, declaredIndexes(want))
		if err != nil {
			return fmt.Errorf("verify schema: %w", err)
		}
		if err := diffTable(table, want, got); err != nil {
			return fmt.Errorf("database schema does not match db/db.go: %w", err)
		}
	}
	return nil
}

// schemaKind is what one entry describes. The kinds differ only in how a
// mismatch is reported; they are compared the same way.
type schemaKind string

const (
	schemaColumn     schemaKind = "column"
	schemaIndex      schemaKind = "index"
	schemaConstraint schemaKind = "unique constraint"
	schemaCheck      schemaKind = "check constraint"
)

// schemaEntry is one comparable schema fact: which object it describes and the
// attributes that decide what that object does. Columns, named indexes and
// implicit UNIQUE constraints all reduce to this, so one diff sees them alike.
type schemaEntry struct {
	Kind  schemaKind
	Name  string // column or index name; the shape, for a constraint
	Attrs []attribute
}

func (e schemaEntry) key() schemaEntryKey { return schemaEntryKey{Kind: e.Kind, Name: e.Name} }

// schemaEntryKey matches one declared entry with one stored entry.
type schemaEntryKey struct {
	Kind schemaKind
	Name string
}

// attribute is one named property of an entry. Entries build them in a fixed
// order, so a difference is always reported in the same order.
type attribute struct {
	Name  string
	Value string
}

// tableEntries returns the comparable structure of one table. Columns, the
// implicit UNIQUE constraints and the CHECK constraints are always kept. Of the
// named indexes, an undeclared one is kept only when it is UNIQUE: a plain index
// added for a query changes nothing the table accepts, while a UNIQUE index
// changes exactly what the table will store. declared is nil for the reference
// side, where every named index is declared by definition.
func tableEntries(conn *sql.DB, table string, declared map[string]struct{}) ([]schemaEntry, error) {
	columns, err := tableColumns(conn, table)
	if err != nil {
		return nil, err
	}
	entries := make([]schemaEntry, 0, len(columns)+4)
	for _, column := range columns {
		entries = append(entries, schemaEntry{Kind: schemaColumn, Name: column.Name, Attrs: []attribute{
			{"type", column.Type},
			{"not null", fmt.Sprintf("%t", column.NotNull)},
			{"default", fmt.Sprintf("%q", column.Default)},
			{"primary key", fmt.Sprintf("%d", column.PK)},
		}})
	}
	indexes, err := tableIndexes(conn, table)
	if err != nil {
		return nil, err
	}
	constraints := make(map[string]int)
	for _, index := range indexes {
		if index.Origin == "u" {
			// 隐式 UNIQUE 索引的名字由声明顺序生成，不是契约的一部分：形状才是。
			constraints[constraintShape(index)]++
			continue
		}
		if declared != nil && !index.Unique {
			if _, ok := declared[index.Name]; !ok {
				continue
			}
		}
		entries = append(entries, schemaEntry{Kind: schemaIndex, Name: index.Name, Attrs: []attribute{
			{"unique", fmt.Sprintf("%t", index.Unique)},
			{"partial", fmt.Sprintf("%t", index.Partial)},
			{"columns", strings.Join(index.Columns, ",")},
		}})
	}
	shapes := make([]string, 0, len(constraints))
	for shape := range constraints {
		shapes = append(shapes, shape)
	}
	sort.Strings(shapes)
	for _, shape := range shapes {
		entries = append(entries, schemaEntry{Kind: schemaConstraint, Name: shape, Attrs: []attribute{
			{"count", fmt.Sprintf("%d", constraints[shape])},
		}})
	}
	checks, err := tableChecks(conn, table)
	if err != nil {
		return nil, err
	}
	counted := make(map[string]int, len(checks))
	for _, check := range checks {
		counted[check]++
	}
	expressions := make([]string, 0, len(counted))
	for expression := range counted {
		expressions = append(expressions, expression)
	}
	sort.Strings(expressions)
	for _, expression := range expressions {
		entries = append(entries, schemaEntry{Kind: schemaCheck, Name: expression, Attrs: []attribute{
			{"count", fmt.Sprintf("%d", counted[expression])},
		}})
	}
	return entries, nil
}

// tableChecks returns the CHECK constraints of table in comparable form. SQLite
// keeps the CREATE TABLE text in sqlite_master and exposes no pragma for CHECK,
// so the comparison reads that text: the declaration in this file stays the only
// source of truth, and no SQL parser is involved. A table that is not there has
// no constraints; the missing columns report that case.
func tableChecks(conn *sql.DB, table string) ([]string, error) {
	var ddl sql.NullString
	err := conn.QueryRow("SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?", table).Scan(&ddl)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !ddl.Valid {
		return nil, nil
	}
	return checkExpressions(ddl.String), nil
}

// checkExpressions returns every CHECK ( ... ) expression in ddl, normalised for
// comparison. The parenthesis scan skips single-quoted literals, so a literal
// holding a parenthesis cannot cut an expression short.
func checkExpressions(ddl string) []string {
	ddl = stripComments(ddl)
	var checks []string
	for offset := 0; offset < len(ddl); {
		at := checkKeyword(ddl[offset:])
		if at < 0 {
			break
		}
		start := offset + at + len("check")
		open := start
		for open < len(ddl) && isSpaceByte(ddl[open]) {
			open++
		}
		if open >= len(ddl) || ddl[open] != '(' {
			offset = start
			continue
		}
		depth := 0
		end := -1
		quoted := false
		for i := open; i < len(ddl); i++ {
			switch c := ddl[i]; {
			case quoted:
				if c == singleQuote {
					quoted = false
				}
			case c == singleQuote:
				quoted = true
			case c == '(':
				depth++
			case c == ')':
				depth--
			}
			if depth == 0 && i > open {
				end = i + 1
				break
			}
		}
		if end < 0 {
			break
		}
		checks = append(checks, normaliseSQL(ddl[start:end]))
		offset = end
	}
	return checks
}

// stripComments removes -- line comments outside quoted literals, so the wording
// of a comment can never read as a constraint and a hand-migrated table that
// dropped a comment still compares equal.
func stripComments(ddl string) string {
	var out strings.Builder
	quoted := false
	for i := 0; i < len(ddl); i++ {
		c := ddl[i]
		switch {
		case quoted:
			out.WriteByte(c)
			if c == singleQuote {
				quoted = false
			}
		case c == singleQuote:
			quoted = true
			out.WriteByte(c)
		case c == '-' && i+1 < len(ddl) && ddl[i+1] == '-':
			for i < len(ddl) && ddl[i] != '\n' {
				i++
			}
			out.WriteByte('\n')
		default:
			out.WriteByte(c)
		}
	}
	return out.String()
}

// checkKeyword finds the next CHECK keyword at or after the start of text, or -1.
func checkKeyword(text string) int {
	for i := 0; i+len("check") <= len(text); i++ {
		if !strings.EqualFold(text[i:i+len("check")], "check") {
			continue
		}
		if i > 0 && isWordByte(text[i-1]) {
			continue
		}
		if i+len("check") < len(text) && isWordByte(text[i+len("check")]) {
			continue
		}
		return i
	}
	return -1
}

// normaliseSQL makes one DDL fragment comparable: keywords are case-insensitive
// and the whitespace around punctuation is not part of the contract, while a
// quoted literal keeps its case and its spacing, because there the bytes decide
// which rows the constraint accepts.
func normaliseSQL(text string) string {
	out := make([]rune, 0, len(text))
	space := false
	quoted := false
	for _, r := range text {
		if quoted {
			out = append(out, r)
			if r == singleQuote {
				quoted = false
			}
			continue
		}
		switch {
		case r == singleQuote:
			quoted = true
			space = false
			out = append(out, r)
		case unicode.IsSpace(r):
			space = true
		default:
			if space && len(out) > 0 && isWordRune(out[len(out)-1]) && isWordRune(r) {
				out = append(out, ' ')
			}
			space = false
			out = append(out, unicode.ToLower(r))
		}
	}
	return string(out)
}

// singleQuote is the SQL literal delimiter; a rune literal for it is unreadable.
const singleQuote = '\''

// isSpaceByte reports the ASCII whitespace SQLite accepts between tokens.
func isSpaceByte(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

func isWordByte(c byte) bool {
	return c == '_' || c == '$' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isWordRune(r rune) bool {
	return r == '_' || r == '$' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// constraintShape is the shape of an implicit UNIQUE index, without its
// generated name: SQLite numbers those by declaration order, which is not part
// of the contract.
func constraintShape(index indexShape) string {
	return fmt.Sprintf("unique %t, partial %t, columns %s", index.Unique, index.Partial, strings.Join(index.Columns, ","))
}

// declaredIndexes names the indexes the declaration requires.
func declaredIndexes(entries []schemaEntry) map[string]struct{} {
	names := make(map[string]struct{})
	for _, entry := range entries {
		if entry.Kind == schemaIndex {
			names[entry.Name] = struct{}{}
		}
	}
	return names
}

// diffTable reports the first difference between the declared structure and the
// stored one, naming the table, the object and the attribute that differs.
func diffTable(table string, want, got []schemaEntry) error {
	columns := 0
	for _, entry := range got {
		if entry.Kind == schemaColumn {
			columns++
		}
	}
	if columns == 0 {
		return fmt.Errorf("table %s is missing", table)
	}
	stored := make(map[schemaEntryKey]schemaEntry, len(got))
	for _, entry := range got {
		stored[entry.key()] = entry
	}
	declared := make(map[schemaEntryKey]struct{}, len(want))
	for _, entry := range want {
		declared[entry.key()] = struct{}{}
		actual, ok := stored[entry.key()]
		if !ok {
			switch entry.Kind {
			case schemaColumn:
				return fmt.Errorf("table %s has no column %s", table, entry.Name)
			case schemaIndex:
				return fmt.Errorf("table %s has no index %s", table, entry.Name)
			case schemaCheck:
				return fmt.Errorf("table %s is missing a check constraint: %s", table, entry.Name)
			default:
				return fmt.Errorf("table %s is missing a unique constraint: %s", table, entry.Name)
			}
		}
		if diff := attributeDifference(entry.Attrs, actual.Attrs); diff != "" {
			return fmt.Errorf("table %s %s %s: %s", table, entry.Kind, entry.Name, diff)
		}
	}
	for _, entry := range got {
		if _, ok := declared[entry.key()]; ok {
			continue
		}
		switch entry.Kind {
		case schemaColumn:
			return fmt.Errorf("table %s has an unexpected column %s", table, entry.Name)
		case schemaIndex:
			return fmt.Errorf("table %s has an undeclared unique index %s", table, entry.Name)
		case schemaCheck:
			return fmt.Errorf("table %s has an unexpected check constraint: %s", table, entry.Name)
		default:
			return fmt.Errorf("table %s has an unexpected unique constraint: %s", table, entry.Name)
		}
	}
	return nil
}

// attributeDifference describes how the stored attributes of one entry depart
// from the declaration; an empty string means they match.
func attributeDifference(want, got []attribute) string {
	actual := make(map[string]string, len(got))
	for _, attr := range got {
		actual[attr.Name] = attr.Value
	}
	var diff []string
	for _, attr := range want {
		if actual[attr.Name] != attr.Value {
			diff = append(diff, fmt.Sprintf("%s=%s, want %s=%s", attr.Name, actual[attr.Name], attr.Name, attr.Value))
		}
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
