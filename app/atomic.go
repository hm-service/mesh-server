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
//
// 临时文件名由 os.CreateTemp 生成：两个发布请求同时写同一个目标时，固定名
// （<path>.tmp）会让后写的内容被先提交者的 rename 带走，后提交者拿到 ENOENT。
func stageFile(path string, raw []byte) (*stagedFile, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create %s: %w", dir, err)
	}
	file, err := os.CreateTemp(dir, filepath.Base(path)+".*")
	if err != nil {
		return nil, fmt.Errorf("create temporary file in %s: %w", dir, err)
	}
	tmp := file.Name()
	if _, err := file.Write(raw); err != nil {
		file.Close()
		_ = os.Remove(tmp)
		return nil, fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(tmp)
		return nil, fmt.Errorf("close %s: %w", tmp, err)
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
