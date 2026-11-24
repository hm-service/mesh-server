package api

import (
	"net/http"
	"strings"
	"time"

	"mesh-server/core"
	"mesh-server/httpx"
)

// contentSecurityPolicy allows the Vue build and nothing else: no inline
// scripts, no external origins.
const contentSecurityPolicy = "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data:; font-src 'self'; connect-src 'self'; base-uri 'self'; form-action 'self'; " +
	"frame-ancestors 'none'"

// withObservability adds the security headers and the access log.
func (s *Server) withObservability(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := w.Header()
		header.Set("X-Content-Type-Options", "nosniff")
		header.Set("Referrer-Policy", "no-referrer")
		header.Set("X-Frame-Options", "DENY")
		header.Set("Content-Security-Policy", contentSecurityPolicy)
		start := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		// /livez is cheap and polled; logging it would only add noise.
		if r.URL.Path == "/livez" {
			return
		}
		s.logger.Info("request",
			"client_ip", s.clientIP(r),
			"method", r.Method,
			"path", r.URL.Path,
			"status", recorder.status,
			"bytes", recorder.bytes,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	})
}

// statusRecorder remembers what the handler answered, for the access log.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(p []byte) (int, error) {
	n, err := r.ResponseWriter.Write(p)
	r.bytes += n
	return n, err
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (r *statusRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

// withBearer guards admin endpoints with the Authorization: Bearer credential.
func (s *Server) withBearer(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearerToken(r)
		if !ok || !core.VerifyBearer(s.sessionKey, token, s.app.Now()) {
			httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next(w, r)
	}
}

// bearerToken extracts the credential from the Authorization header.
func bearerToken(r *http.Request) (string, bool) {
	const prefix = "Bearer "
	header := r.Header.Get("Authorization")
	if len(header) <= len(prefix) || !strings.HasPrefix(header, prefix) {
		return "", false
	}
	return strings.TrimSpace(header[len(prefix):]), true
}

// methodNotAllowed answers a known path with an unsupported method.
func methodNotAllowed(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Allow", "GET, HEAD")
	httpx.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed")
}

// handleNotFound is the site-wide fallback.
func handleNotFound(w http.ResponseWriter, r *http.Request) {
	httpx.WriteError(w, http.StatusNotFound, "not_found")
}
