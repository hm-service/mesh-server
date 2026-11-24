package api

import (
	"io"
	"mime"
	"net/http"
	"path"
	"strings"

	"mesh-server/httpx"
)

// handleIndex serves the console entry document.
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	s.serveIndex(w, r)
}

// handleAsset serves the embedded build; an unknown path falls back to the
// entry document, so the console keeps working without a server-side router.
func (s *Server) handleAsset(w http.ResponseWriter, r *http.Request) {
	name := path.Clean(strings.TrimPrefix(r.URL.Path, "/admin/"))
	if s.static == nil || name == "." || name == ".." || strings.HasPrefix(name, "../") {
		s.serveIndex(w, r)
		return
	}
	file, err := s.static.Open(name)
	if err != nil {
		s.serveIndex(w, r)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.IsDir() {
		s.serveIndex(w, r)
		return
	}
	seeker, ok := file.(io.ReadSeeker)
	if !ok {
		s.serveIndex(w, r)
		return
	}
	w.Header().Set("Content-Type", contentTypeOf(name))
	// Vite fingerprints the asset names, so a URL never changes its bytes.
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeContent(w, r, name, info.ModTime(), seeker)
}

func (s *Server) serveIndex(w http.ResponseWriter, r *http.Request) {
	if len(s.index) == 0 {
		httpx.WriteError(w, http.StatusNotFound, "not_found")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(s.index)
}

func contentTypeOf(name string) string {
	if ct := mime.TypeByExtension(path.Ext(name)); ct != "" {
		return ct
	}
	return "application/octet-stream"
}
