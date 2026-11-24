// Package api is the HTTP interface described by CONTRACT.md 1.4.
package api

import (
	"io/fs"
	"log/slog"
	"net/http"
	"net/netip"
	"time"

	"mesh-server/app"
	"mesh-server/core"
	"mesh-server/httpx"
)

// SessionTTL is how long an admin cookie session lives.
const SessionTTL = 24 * time.Hour

// SessionCookie is the admin session cookie name; clients never see it.
const SessionCookie = "mesh_session"

// Options wires a Server to the application and to its deployment settings.
type Options struct {
	App *app.App
	// Static is the embedded frontend build (frontend/dist).
	Static fs.FS
	// Logger defaults to slog.Default().
	Logger *slog.Logger
	// ReadyzToken protects /readyz when it is not empty.
	ReadyzToken string
	// TrustedProxies are the peers whose forwarding headers are believed. An
	// empty list trusts nobody, which is the default.
	TrustedProxies []netip.Prefix
	// Concurrency gates; zero means the documented default.
	MaxAuthConcurrency   int
	MaxAPIConcurrency    int
	MaxStaticConcurrency int
	// MinAuthDelay is the floor of one login round trip. Zero disables it,
	// which is what the tests use.
	MinAuthDelay time.Duration
}

// Server holds the HTTP state: the session table and the nonce replay cache.
type Server struct {
	app          *app.App
	logger       *slog.Logger
	static       fs.FS
	index        []byte
	readyzToken  string
	proxies      []netip.Prefix
	authGate     *httpx.Gate
	apiGate      *httpx.Gate
	staticGate   *httpx.Gate
	readyzGate   *httpx.Gate
	sessions     *core.Sessions
	nonces       *core.NonceCache
	minAuthDelay time.Duration
	// fallbacks records the paths whose 405 fallback is already registered.
	fallbacks map[string]struct{}
}

// New returns a Server with fresh session and nonce state.
func New(opts Options) *Server {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	s := &Server{
		app:          opts.App,
		logger:       logger,
		static:       opts.Static,
		readyzToken:  opts.ReadyzToken,
		proxies:      opts.TrustedProxies,
		authGate:     httpx.NewGate(orDefault(opts.MaxAuthConcurrency, 32)),
		apiGate:      httpx.NewGate(orDefault(opts.MaxAPIConcurrency, 128)),
		staticGate:   httpx.NewGate(orDefault(opts.MaxStaticConcurrency, 64)),
		readyzGate:   httpx.NewGate(4),
		sessions:     core.NewSessions(SessionTTL, opts.App.Now),
		nonces:       core.NewNonceCache(core.ClockSkew),
		minAuthDelay: opts.MinAuthDelay,
		fallbacks:    make(map[string]struct{}),
	}
	if s.static != nil {
		if raw, err := fs.ReadFile(s.static, "index.html"); err == nil {
			s.index = raw
		} else {
			logger.Warn("frontend build has no index.html", "error", err)
		}
	}
	return s
}

func orDefault(value, fallback int) int {
	if value <= 0 {
		return fallback
	}
	return value
}

// Handler returns the routed HTTP handler with its middleware applied.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	// Static redirect from the site root to the console.
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin", http.StatusFound)
	})
	// Probes are the only routes without a session or device guard.
	s.route(mux, "GET", "/livez", s.handleLivez)
	s.route(mux, "GET", "/readyz", s.withGate(s.readyzGate, s.handleReadyz))
	// Admin surface.
	s.route(mux, "POST", "/api/login", s.withGate(s.authGate, s.handleLogin))
	s.route(mux, "POST", "/api/logout", s.handleLogout)
	s.route(mux, "GET", "/api/health", s.withGate(s.apiGate, s.handleHealth))
	s.route(mux, "GET", "/api/template", s.admin(s.handleTemplate))
	s.route(mux, "GET", "/api/devices", s.admin(s.handleListDevices))
	s.route(mux, "POST", "/api/devices", s.admin(s.handleCreateDevice))
	s.route(mux, "GET", "/api/devices/{id}", s.admin(s.handleGetDevice))
	s.route(mux, "PUT", "/api/devices/{id}", s.admin(s.handleUpdateDevice))
	s.route(mux, "DELETE", "/api/devices/{id}", s.admin(s.handleDeleteDevice))
	s.route(mux, "POST", "/api/devices/{id}/token", s.admin(s.handleIssueToken))
	s.route(mux, "GET", "/api/admin/settings", s.admin(s.handleGetSettings))
	s.route(mux, "PUT", "/api/admin/settings", s.admin(s.handleUpdateSettings))
	// Public download and the device-facing API signed by the device key.
	s.route(mux, "GET", "/api/updates/{file}", s.withGate(s.apiGate, s.handleDownloadUpdate))
	s.route(mux, "POST", "/api/register", s.withGate(s.apiGate, s.handleRegister))
	s.route(mux, "GET", "/api/config", s.withGate(s.apiGate, s.handleConfig))
	s.route(mux, "POST", "/api/heartbeat", s.withGate(s.apiGate, s.handleHeartbeat))
	// Console pages and their assets.
	s.route(mux, "GET", "/admin", s.withGate(s.staticGate, s.handleIndex))
	s.route(mux, "GET", "/admin/{path...}", s.withGate(s.staticGate, s.handleAsset))
	// Anything else, with or without a method, is a miss.
	mux.HandleFunc("/", handleNotFound)
	return s.withObservability(mux)
}

// route registers one method and, on the same path, the fallback that answers
// 405 instead of letting the site-wide 404 shadow it.
func (s *Server) route(mux *http.ServeMux, method, path string, h http.HandlerFunc) {
	mux.HandleFunc(method+" "+path, h)
	// Several methods share one path, so the fallback may only be registered once.
	if _, done := s.fallbacks[path]; done {
		return
	}
	s.fallbacks[path] = struct{}{}
	mux.HandleFunc(path, methodNotAllowed)
}

// admin guards an endpoint with the mesh_session cookie.
func (s *Server) admin(h http.HandlerFunc) http.HandlerFunc {
	return s.withGate(s.apiGate, s.withSession(h))
}

// withGate answers 503 right away when the route is already at its capacity.
func (s *Server) withGate(gate *httpx.Gate, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !gate.Acquire() {
			w.Header().Set("Retry-After", "1")
			httpx.WriteError(w, http.StatusServiceUnavailable, "busy")
			return
		}
		defer gate.Release()
		next(w, r)
	}
}

func (s *Server) clientIP(r *http.Request) string {
	return httpx.ClientIP(r, s.proxies)
}
