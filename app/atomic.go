package app

import (
	"fmt"
	"os"
	"path/filepath"
)

// stagedFile is a file written beside its destination and made visible by a
// rename, so a reader sees either the old contents or the new ones.
type stagedFile struct {
	path string
	tmp  string
}

// stageFile writes raw to a temporary file next to path. Commit publishes it;
// Abort discards it and is a no-op after Commit.
func stageFile(path string, raw []byte) (*stagedFile, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create %s: %w", dir, err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return nil, fmt.Errorf("write %s: %w", tmp, err)
	}
	return &stagedFile{path: path, tmp: tmp}, nil
}

// Commit renames the temporary file over the destination.
func (s *stagedFile) Commit() error {
	if s == nil {
		return nil
	}
	if err := os.Rename(s.tmp, s.path); err != nil {
		_ = os.Remove(s.tmp)
		return fmt.Errorf("replace %s: %w", s.path, err)
	}
	return nil
}

// Abort removes the temporary file of a stage that was not committed.
func (s *stagedFile) Abort() {
	if s == nil {
		return
	}
	_ = os.Remove(s.tmp)
}
