package core

import (
	"bytes"
	"os"
	"sync"
)

const (
	highWatermark = 4 << 20
	lowWatermark  = highWatermark / 2
)

// LogFile is an append-only log file that trims itself back to the low
// watermark instead of rotating into extra files. It is safe for concurrent
// use and doubles as the io.Writer of the JSON logger.
type LogFile struct {
	mu   sync.Mutex
	path string
	f    *os.File
	size int64
}

// OpenLog opens path, creating it when missing.
func OpenLog(path string) (*LogFile, error) {
	w := &LogFile{path: path}
	if err := w.openLocked(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *LogFile) openLocked() error {
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	w.f = f
	w.size = st.Size()
	return nil
}

// Write appends p, trimming the file first once it reached the high watermark.
func (w *LogFile) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return 0, os.ErrClosed
	}
	if w.size >= highWatermark {
		w.rotateLocked()
		if w.f == nil {
			return 0, os.ErrClosed
		}
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

// Close closes the file, if open.
func (w *LogFile) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}

// rotateLocked rewrites the tail of the file through a temporary file, so a
// crash between trim and write leaves the original log intact.
func (w *LogFile) rotateLocked() {
	window, hasSentinel, err := readTail(w.path, lowWatermark)
	if err != nil {
		return
	}
	offset, count := completeRecords(window, hasSentinel)
	if count <= 0 {
		return
	}
	temp := w.path + ".rotating"
	if err := os.WriteFile(temp, window[offset:offset+count], 0o600); err != nil {
		_ = os.Remove(temp)
		return
	}
	// Windows cannot replace a file that is still open, so give the handle up first.
	_ = w.f.Close()
	w.f = nil
	if err := os.Rename(temp, w.path); err != nil {
		_ = os.Remove(temp)
	}
	// Reopen either way: if the rename failed we keep appending to the old file.
	_ = w.openLocked()
}

// readTail reads the last n bytes of path. hasSentinel reports that the window
// starts mid-file, so its first byte is the one before the window.
func readTail(path string, n int64) (window []byte, hasSentinel bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, false, err
	}
	start := max(st.Size()-n, 0)
	readFrom := start
	if start > 0 {
		hasSentinel = true
		readFrom = start - 1
	}
	window = make([]byte, st.Size()-readFrom)
	if _, err := f.ReadAt(window, readFrom); err != nil {
		return nil, false, err
	}
	return window, hasSentinel, nil
}

// completeRecords narrows the window down to whole lines; a line cut at either
// end is dropped.
func completeRecords(window []byte, hasSentinel bool) (offset, count int) {
	if hasSentinel {
		if len(window) == 0 {
			return 0, 0
		}
		if window[0] == '\n' {
			offset = 1
		} else {
			first := bytes.IndexByte(window[1:], '\n')
			if first < 0 {
				return 0, 0
			}
			offset = first + 2
		}
	}
	last := bytes.LastIndexByte(window, '\n')
	if last < offset {
		return 0, 0
	}
	return offset, last + 1 - offset
}
