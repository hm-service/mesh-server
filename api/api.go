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

// sessionTTL is how long one issued credential lives.
const sessionTTL = 24 * time.Hour

// Options wires a Server to the application and to its deployment settings.
type Options struct {
	App *app.App
	// Static is the embedded frontend build (frontend/dist).
	Static fs.FS
	// Logger defaults to slog.Default().
	Logger *slog.Logger
	// ReadyzToken protects /readyz when it is not empty.
	ReadyzToken string
	// TokenHash verifies the AUTH_TOKEN a client presents to /api/auth.
	TokenHash *core.TokenHash
	// SessionKey signs the issued credentials. An empty key is replaced by a
	// random one, which just means every restart invalidates old credentials.
	SessionKey []byte
	// TrustedProxies are the peers whose forwarding headers are believed. An
	// empty list trusts nobody, which is the default.
	TrustedProxies []netip.Prefix
	// Concurrency gates; zero means the documented default. The admin surface
	// and the device surface each get their own APIConcurrency-sized gate, so a
	// flood of device requests cannot starve the console.
	MaxAuthConcurrency   int
	MaxAPIConcurrency    int
	MaxStaticConcurrency int
	// Sleep is how a login waits out the minimum round trip; tests replace it
	// to keep the test suite fast. Nil sleeps for real.
	Sleep func(time.Duration)
}

// Server holds the HTTP state: the token hash, the signing key and the nonce
// replay cache.
type Server struct {
	app         *app.App
	logger      *slog.Logger
	static      fs.FS
	index       []byte
	readyzToken string
	proxies     []netip.Prefix
	authGate    *httpx.Gate
	apiGate     *httpx.Gate
	deviceGate  *httpx.Gate
	staticGate  *httpx.Gate
	readyzGate  *httpx.Gate
	tokenHash   *core.TokenHash
	sessionKey  []byte
	nonces      *core.NonceCache
	sleep       func(time.Duration)
	// fallbacks records the paths whose 405 fallback is already registered;
	// methods records the methods each path answers, for the Allow header.
	fallbacks map[string]struct{}
	methods   map[string][]string
}

// New returns a Server with fresh session and nonce state.
func New(opts Options) *Server {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	apiSlots := orDefault(opts.MaxAPIConcurrency, 128)
	s := &Server{
		app:         opts.App,
		logger:      logger,
		static:      opts.Static,
		readyzToken: opts.ReadyzToken,
		proxies:     opts.TrustedProxies,
		authGate:    httpx.NewGate(orDefault(opts.MaxAuthConcurrency, 32)),
		apiGate:     httpx.NewGate(apiSlots),
		deviceGate:  httpx.NewGate(apiSlots),
		staticGate:  httpx.NewGate(orDefault(opts.MaxStaticConcurrency, 64)),
		readyzGate:  httpx.NewGate(4),
		tokenHash:   opts.TokenHash,
		sessionKey:  opts.SessionKey,
		nonces:      core.NewNonceCache(core.ClockSkew),
		sleep:       opts.Sleep,
		fallbacks:   make(map[string]struct{}),
		methods:     make(map[string][]string),
	}
	if s.sleep == nil {
		s.sleep = time.Sleep
	}
	if len(s.sessionKey) == 0 {
		key, err := core.RandomBytes(core.SessionKeyBytes)
		if err != nil {
			// Without a signing key no credential can be trusted, so refuse to
			// build a server that would accept none of them silently.
			panic("api: generate session key: " + err.Error())
		}
		s.sessionKey = key
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
	s.route(mux, "POST", "/api/auth", s.withGate(s.authGate, s.handleAuth))
	s.route(mux, "GET", "/api/health", s.withGate(s.apiGate, s.handleHealth))
	s.route(mux, "GET", "/api/template", s.admin(s.handleTemplate))
	s.route(mux, "GET", "/api/devices", s.admin(s.handleListDevices))
	s.route(mux, "POST", "/api/devices", s.admin(s.handleCreateDevice))
	s.route(mux, "GET", "/api/devices/{id}", s.admin(s.handleGetDevice))
	s.route(mux, "PUT", "/api/devices/{id}", s.admin(s.handleUpdateDevice))
	s.route(mux, "DELETE", "/api/devices/{id}", s.admin(s.handleDeleteDevice))
	s.route(mux, "POST", "/api/devices/{id}/token", s.admin(s.handleIssueToken))
	s.route(mux, "PUT", "/api/devices/{id}/group", s.admin(s.handleSetDeviceGroup))
	s.route(mux, "PUT", "/api/layout", s.admin(s.handleDeviceLayout))
	// Device groups and the layout the console saves after a drag.
	s.route(mux, "GET", "/api/groups", s.admin(s.handleListGroups))
	s.route(mux, "POST", "/api/groups", s.admin(s.handleCreateGroup))
	s.route(mux, "PUT", "/api/groups/{id}", s.admin(s.handleUpdateGroup))
	s.route(mux, "DELETE", "/api/groups/{id}", s.admin(s.handleDeleteGroup))
	s.route(mux, "GET", "/api/admin/releases", s.admin(s.handleListReleases))
	s.route(mux, "PUT", "/api/admin/releases", s.admin(s.handlePublishRelease))
	s.route(mux, "DELETE", "/api/admin/releases/{platform...}", s.admin(s.handleDeleteRelease))
	// Device-facing API signed by the device key.
	s.route(mux, "POST", "/api/register", s.withGate(s.deviceGate, s.handleRegister))
	s.route(mux, "POST", "/api/heartbeat", s.withGate(s.deviceGate, s.handleHeartbeat))
	// Console pages and their assets.
	s.route(mux, "GET", "/admin", s.withGate(s.staticGate, s.handleIndex))
	s.route(mux, "GET", "/admin/{path...}", s.withGate(s.staticGate, s.handleAsset))
	// Anything else, with or without a method, is a miss.
	mux.HandleFunc("/", handleNotFound)
	return s.withObservability(mux)
}

// route registers one method and, on the same path, the fallback that answers
// 405 instead of letting the site-wide 404 shadow it. The methods registered for
// a path are collected here so the fallback can name them in Allow.
func (s *Server) route(mux *http.ServeMux, method, path string, h http.HandlerFunc) {
	mux.HandleFunc(method+" "+path, h)
	s.methods[path] = append(s.methods[path], method)
	// Several methods share one path, so the fallback may only be registered once.
	if _, done := s.fallbacks[path]; done {
		return
	}
	s.fallbacks[path] = struct{}{}
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		s.methodNotAllowed(w, path)
	})
}

// admin guards an endpoint with the bearer credential.
func (s *Server) admin(h http.HandlerFunc) http.HandlerFunc {
	return s.withGate(s.apiGate, s.withBearer(h))
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
