package api

import (
	"math/rand/v2"
	"net/http"
	"time"

	"mesh-server/core"
	"mesh-server/httpx"
)

// authJitter is the extra random delay added on top of the login floor.
const authJitter = 200 * time.Millisecond

type loginRequest struct {
	Password string `json:"password"`
}

// handleLogin exchanges the admin password for the mesh_session cookie.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	var req loginRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "bad_request")
		return
	}
	cfg := s.app.Config()
	ok := req.Password != "" && core.VerifyPassword(req.Password, cfg.PasswordSalt, cfg.PasswordHash)
	s.holdAuthFloor(start)
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "invalid_password")
		return
	}
	id, err := s.sessions.Create()
	if err != nil {
		s.logger.Error("create session", "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    id,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(SessionTTL / time.Second),
		Expires:  s.app.Now().Add(SessionTTL),
	})
	w.WriteHeader(http.StatusNoContent)
}

// holdAuthFloor keeps a login from answering faster than the documented floor,
// so the response time does not tell which branch was taken.
func (s *Server) holdAuthFloor(start time.Time) {
	if s.minAuthDelay <= 0 {
		return
	}
	floor := s.minAuthDelay + rand.N(authJitter)
	if elapsed := time.Since(start); elapsed < floor {
		time.Sleep(floor - elapsed)
	}
}

// handleLogout drops the session and clears the cookie.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(SessionCookie); err == nil {
		s.sessions.Delete(cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
	})
	w.WriteHeader(http.StatusNoContent)
}

// handleHealth reports that the console is reachable. It carries no
// authentication state: the admin surface always requires a session.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
