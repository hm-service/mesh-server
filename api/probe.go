package api

import (
	"context"
	"net/http"
	"time"

	"mesh-server/httpx"
)

// readyzTimeout bounds one readiness check, which touches the disk.
const readyzTimeout = 5 * time.Second

// handleLivez answers as long as the process is serving.
func (s *Server) handleLivez(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

// handleReadyz proves DATA_DIR and the database work. The reason for a failure
// goes to the log only, never to the caller.
func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	if s.readyzToken != "" && r.URL.Query().Get("token") != s.readyzToken {
		w.Header().Set("WWW-Authenticate", `Bearer realm="readyz"`)
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), readyzTimeout)
	defer cancel()
	if err := s.app.Probe(ctx); err != nil {
		s.logger.Error("readiness check failed", "error", err)
		httpx.WriteJSON(w, http.StatusServiceUnavailable, map[string]bool{"ok": false})
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
