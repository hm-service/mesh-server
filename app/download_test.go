package app

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"mesh-server/text"
)

// TestOpenDownloadRejectsADeclaredOversizeBody pins the declared-size check: a
// response that announces more than MaxReleaseBytes is refused before the body
// is read, so an oversized publish never pays for the transfer. The handler
// writes nothing, so a reader would block until the client timeout.
func TestOpenDownloadRejectsADeclaredOversizeBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(MaxReleaseBytes+1))
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	app := &App{fetch: &http.Client{Timeout: 2 * time.Second}}
	body, _, err := app.openDownload(srv.URL)
	if err == nil {
		if body != nil {
			body.Close()
		}
		t.Fatal("a body larger than MaxReleaseBytes was accepted")
	}

	if err.Error() != text.Get(text.AppFileTooLarge) {
		t.Fatalf("error = %q, want the size message", err)
	}
}

// TestOpenDownloadAcceptsAnUnstatedLength keeps the fallback: a chunked response
// has no Content-Length, and the read-time bound is what protects it.
func TestOpenDownloadAcceptsAnUnstatedLength(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	defer srv.Close()

	app := &App{fetch: &http.Client{Timeout: 2 * time.Second}}
	body, length, err := app.openDownload(srv.URL)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer body.Close()
	if length != -1 {
		t.Fatalf("content length = %d, want -1 for a streamed body", length)
	}
}
