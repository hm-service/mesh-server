package db

import (
	"database/sql"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// createFileDB writes path from ddl, statement by statement: the way an operator
// rebuilding a table by hand would leave it.
func createFileDB(t *testing.T, path, ddl string) {
	t.Helper()
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	for statement := range strings.SplitSeq(ddl, ";") {
		statement = strings.TrimSpace(statement)
		if statement == "" {
			continue
		}
		if _, err := conn.Exec(statement); err != nil {
			t.Fatalf("apply %q: %v", statement, err)
		}
	}
}

// agedSchema rewrites one fragment of the declared schema. It fails the test when
// the fragment is gone, so a reformatted declaration cannot silently turn the
// case into a no-op.
func agedSchema(t *testing.T, pattern, replacement string) string {
	t.Helper()
	compiled := regexp.MustCompile(pattern)
	if !compiled.MatchString(schema) {
		t.Fatalf("%q is no longer in the declared schema; update this test", pattern)
	}
	return compiled.ReplaceAllString(schema, replacement)
}

// TestOpenRejectsSchemaDrift pins the startup contract beyond column names: a
// file whose columns are named exactly like db/db.go but whose defaults or
// constraints differ cannot guarantee what the code assumes, so it must not
// start. Nothing is repaired on the way in.
func TestOpenRejectsSchemaDrift(t *testing.T) {
	cases := []struct {
		name string
		ddl  func(t *testing.T) string
		want string
	}{
		{
			name: "column default changed",
			ddl: func(t *testing.T) string {
				return agedSchema(t,
					`(?m)^\s*name\s+TEXT\s+NOT NULL DEFAULT ''`,
					"    name TEXT NOT NULL DEFAULT 'x'")
			},
			want: "default",
		},
		{
			name: "unique constraint dropped",
			ddl: func(t *testing.T) string {
				return agedSchema(t, `public_id\s+TEXT\s+NOT NULL UNIQUE`, "public_id TEXT NOT NULL")
			},
			want: "unique constraint",
		},
		{
			name: "release slot check dropped",
			ddl: func(t *testing.T) string {
				return agedSchema(t, `CHECK \(slot IN \('client', 'launcher'\)\)`, "")
			},
			want: "check constraint",
		},
		{
			name: "release launcher row check dropped",
			ddl: func(t *testing.T) string {
				return agedSchema(t, `,\s*\n\s*CHECK \(slot = 'client' OR \(notes = '' AND auto_update = 0\)\)`, "")
			},
			want: "check constraint",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "mesh.db")
			createFileDB(t, path, tc.ddl(t))
			if _, err := Open(path); err == nil {
				t.Fatalf("Open() accepted a database with %s", tc.name)
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Open() error %q does not mention %q", err, tc.want)
			}
		})
	}
}

// TestOpenRejectsAnIndexThatLostItsConstraint pins the index half: the same
// name and columns are not enough if the uniqueness or the predicate is gone,
// because CREATE INDEX IF NOT EXISTS would never fix that on the next start.
func TestOpenRejectsAnIndexThatLostItsConstraint(t *testing.T) {
	cases := []struct {
		name  string
		index string
		want  string
	}{
		{
			name:  "not unique",
			index: "CREATE INDEX device_device_id ON device (device_id)",
			want:  "unique",
		},
		{
			name:  "not partial",
			index: "CREATE UNIQUE INDEX device_device_id ON device (device_id)",
			want:  "partial",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "mesh.db")
			current, err := Open(path)
			if err != nil {
				t.Fatalf("Open() on a fresh database: %v", err)
			}
			if err := current.Close(); err != nil {
				t.Fatalf("Close() error: %v", err)
			}

			conn, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatalf("reopen the file: %v", err)
			}
			if _, err := conn.Exec("DROP INDEX device_device_id"); err != nil {
				conn.Close()
				t.Fatalf("drop the declared index: %v", err)
			}
			if _, err := conn.Exec(tc.index); err != nil {
				conn.Close()
				t.Fatalf("recreate the index without its %s: %v", tc.want, err)
			}
			if err := conn.Close(); err != nil {
				t.Fatalf("close the aged file: %v", err)
			}

			if _, err := Open(path); err == nil {
				t.Fatal("Open() accepted an index without its constraint")
			} else if !strings.Contains(err.Error(), "device_device_id") || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Open() error %q does not name the index and %q", err, tc.want)
			}
		})
	}
}

// TestOpenRejectsAnExtraUniqueIndex pins F3: an added index lies outside what the
// declaration describes only while it changes nothing the table accepts. A plain
// index only accelerates a query and is allowed; a UNIQUE one decides which rows
// the table will store, so it is a structural difference.
func TestOpenRejectsAnExtraUniqueIndex(t *testing.T) {
	aged := func(t *testing.T, statement string) error {
		t.Helper()
		path := filepath.Join(t.TempDir(), "mesh.db")
		current, err := Open(path)
		if err != nil {
			t.Fatalf("Open() on a fresh database: %v", err)
		}
		if err := current.Close(); err != nil {
			t.Fatalf("Close() error: %v", err)
		}
		conn, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatalf("reopen the file: %v", err)
		}
		if _, err := conn.Exec(statement); err != nil {
			conn.Close()
			t.Fatalf("apply %q: %v", statement, err)
		}
		if err := conn.Close(); err != nil {
			t.Fatalf("close the aged file: %v", err)
		}
		db, err := Open(path)
		if db != nil {
			if closeErr := db.Close(); closeErr != nil {
				t.Fatalf("Close() error: %v", closeErr)
			}
		}
		return err
	}

	err := aged(t, "CREATE UNIQUE INDEX device_name_unique ON device (name)")
	if err == nil {
		t.Fatal("Open() accepted an added UNIQUE index")
	}
	if !strings.Contains(err.Error(), "device_name_unique") || !strings.Contains(err.Error(), "unique index") {
		t.Fatalf("Open() error %q does not name the added unique index", err)
	}

	if err := aged(t, "CREATE INDEX device_name_lookup ON device (name)"); err != nil {
		t.Fatalf("Open() rejected a plain added index: %v", err)
	}
}

// TestOpenAcceptsTheSameChecksWrittenDifferently pins how the two sides are
// compared: the reference is the declared DDL, so keyword case and the whitespace
// around punctuation are not part of the contract, while a quoted literal keeps
// its bytes because those decide which rows the table accepts.
func TestOpenAcceptsTheSameChecksWrittenDifferently(t *testing.T) {
	reformat := func(t *testing.T, text, old, replacement string) string {
		t.Helper()
		if !strings.Contains(text, old) {
			t.Fatalf("%q is no longer in the declared schema; update this test", old)
		}
		return strings.Replace(text, old, replacement, 1)
	}
	ddl := reformat(t, schema,
		"CHECK (slot IN ('client', 'launcher'))",
		"check( slot in ('client','launcher') )")
	ddl = reformat(t, ddl,
		"CHECK (slot = 'client' OR (notes = '' AND auto_update = 0))",
		"CHECK ( slot='client' or ( notes='' and auto_update=0 ) )")
	// 注释不参与比较：一段提到 check 的说明既不是约束，也不该让库对不上。
	ddl = reformat(t, ddl, "CREATE TABLE IF NOT EXISTS release (",
		"CREATE TABLE IF NOT EXISTS release (\n    -- 这一行提到 check，但它只是注释")

	path := filepath.Join(t.TempDir(), "mesh.db")
	createFileDB(t, path, ddl)
	aged, err := Open(path)
	if err != nil {
		t.Fatalf("Open() rejected the same constraints written differently: %v", err)
	}
	if err := aged.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}
}
