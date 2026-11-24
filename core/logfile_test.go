package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWritesAndTrimsAtHighWatermark(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.log")
	w, err := OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Repeat("x", 200) + "\n"
	for range 26000 {
		if _, err := w.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() >= highWatermark+4096 {
		t.Fatalf("size after rotation = %d, want under %d", st.Size(), highWatermark)
	}
	if st.Size() <= lowWatermark/2 {
		t.Fatalf("size after rotation = %d, file was emptied instead of trimmed", st.Size())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.HasSuffix(text, "\n") {
		t.Fatal("file must end on a record boundary")
	}
	for row := range strings.SplitSeq(strings.TrimSuffix(text, "\n"), "\n") {
		if len(row) != 200 {
			t.Fatalf("line was cut mid-record: %q", row)
		}
	}
}

func TestCompleteRecords(t *testing.T) {
	cases := []struct {
		name     string
		window   string
		sentinel bool
		want     string
	}{
		{"整文件无哨兵", "a\nb\n", false, "a\nb\n"},
		{"无结尾换行", "a\nb", false, "a\n"},
		{"哨兵为换行", "\na\nb\n", true, "a\nb\n"},
		{"哨兵在行中", "Xa\nb\nc\n", true, "b\nc\n"},
		{"窗口只有半行", "XXX", true, ""},
		{"空窗口", "", true, ""},
	}
	for _, tc := range cases {
		offset, count := completeRecords([]byte(tc.window), tc.sentinel)
		if got := tc.window[offset : offset+count]; got != tc.want {
			t.Fatalf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}
