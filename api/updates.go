package api

import (
	"errors"
	"net/http"

	"mesh-server/app"
	"mesh-server/httpx"
)

// handleDownloadUpdate serves DATA_DIR/updates/ publicly; the file name is
// whitelisted so no request can escape the directory.
func (s *Server) handleDownloadUpdate(w http.ResponseWriter, r *http.Request) {
	file, info, err := s.app.OpenRelease(r.PathValue("file"))
	switch {
	case errors.Is(err, app.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "not_found")
		return
	case err != nil:
		s.logger.Error("open release", "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal")
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, info.Name(), info.ModTime(), file)
}
