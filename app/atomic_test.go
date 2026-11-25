package app

import (
	"os"
	"path/filepath"
	"testing"
)

// TestStageFileUsesUniqueTemporaryNames pins F7: two publishes of the same
// template may overlap, and a fixed "<path>.tmp" made one rename carry the other
// request's bytes away.
func TestStageFileUsesUniqueTemporaryNames(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "peer.default.toml")
	first, err := stageFile(path, []byte("first"))
	if err != nil {
		t.Fatalf("stageFile() error: %v", err)
	}
	second, err := stageFile(path, []byte("second"))
	if err != nil {
		t.Fatalf("second stageFile() error: %v", err)
	}
	if first.tmp == second.tmp {
		t.Fatalf("two stages share the temporary file %s", first.tmp)
	}
	if err := first.Commit(); err != nil {
		t.Fatalf("first Commit() error: %v", err)
	}
	if err := second.Commit(); err != nil {
		t.Fatalf("second Commit() error: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read template: %v", err)
	}
	if string(raw) != "second" {
		t.Fatalf("template = %q, want %q", raw, "second")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "peer.default.toml" {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("leftovers in %s: %v", dir, names)
	}
}
