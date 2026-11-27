// Package api is the HTTP interface described by contract/.
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

// Surface selects which routes a handler carries. The two surfaces can listen on
// different addresses, so the admin API never shares a socket with the device API.
type Surface int

const (
	// SurfaceAll is both surfaces on one handler, which is the single-listener setup.
	SurfaceAll Surface = iota
	// SurfaceDevice is the public device API plus the probes.
	SurfaceDevice
	// SurfaceAdmin is the console, its assets and the admin API.
	SurfaceAdmin
)

// Handler returns both surfaces on one handler.
func (s *Server) Handler() http.Handler {
	return s.HandlerFor(SurfaceAll)
}

// HandlerFor returns only the routes that belong to surface. Calls share the
// Server (and therefore the session key), so a handler built here and one built
// by Handler accept each other's credentials.
func (s *Server) HandlerFor(surface Surface) http.Handler {
	mux := http.NewServeMux()
	methods := make(map[string][]string)
	fallbacks := make(map[string]struct{})

	// register answers 405 on a known path instead of letting the site-wide 404
	// shadow it, and records the methods so Allow can name them.
	register := func(method, path string, h http.HandlerFunc) {
		mux.HandleFunc(method+" "+path, h)
		methods[path] = append(methods[path], method)
		if _, done := fallbacks[path]; done {
			return
		}
		fallbacks[path] = struct{}{}
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			s.methodNotAllowed(w, methods[path])
		})
	}

	onAdmin := surface == SurfaceAll || surface == SurfaceAdmin
	onDevice := surface == SurfaceAll || surface == SurfaceDevice
	admin := func(method, path string, h http.HandlerFunc) {
		if onAdmin {
			register(method, path, h)
		}
	}
	device := func(method, path string, h http.HandlerFunc) {
		if onDevice {
			register(method, path, h)
		}
	}

	// Console entry. On a split listener this lives on the admin surface only.
	if onAdmin {
		mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/admin", http.StatusFound)
		})
	}
	// Probes belong to the device surface: kubelet reaches them from the host.
	device("GET", "/livez", s.handleLivez)
	device("GET", "/readyz", s.withGate(s.readyzGate, s.handleReadyz))
	// 登录是唯一不带前缀的管理入口：它自己就是凭据的来源，放进 /api/admin 没有意义。
	admin("POST", "/api/auth", s.withGate(s.authGate, s.handleAuth))
	// Admin surface: everything under /api/admin is bearer-guarded.
	admin("GET", "/api/admin/template", s.admin(s.handleTemplate))
	admin("GET", "/api/admin/devices", s.admin(s.handleListDevices))
	admin("POST", "/api/admin/devices", s.admin(s.handleCreateDevice))
	admin("GET", "/api/admin/devices/{id}", s.admin(s.handleGetDevice))
	admin("PUT", "/api/admin/devices/{id}", s.admin(s.handleUpdateDevice))
	admin("DELETE", "/api/admin/devices/{id}", s.admin(s.handleDeleteDevice))
	admin("POST", "/api/admin/devices/{id}/token", s.admin(s.handleIssueToken))
	admin("POST", "/api/admin/devices/{id}/credential", s.admin(s.handleIssueCredential))
	admin("DELETE", "/api/admin/devices/{id}/credential", s.admin(s.handleRevokeCredential))
	admin("PUT", "/api/admin/devices/{id}/group", s.admin(s.handleSetDeviceGroup))
	admin("PUT", "/api/admin/layout", s.admin(s.handleDeviceLayout))
	// Device groups and the layout the console saves after a drag.
	admin("GET", "/api/admin/groups", s.admin(s.handleListGroups))
	admin("POST", "/api/admin/groups", s.admin(s.handleCreateGroup))
	admin("PUT", "/api/admin/groups/{id}", s.admin(s.handleUpdateGroup))
	admin("DELETE", "/api/admin/groups/{id}", s.admin(s.handleDeleteGroup))
	admin("GET", "/api/admin/releases", s.admin(s.handleListReleases))
	admin("PUT", "/api/admin/releases", s.admin(s.handlePublishRelease))
	admin("DELETE", "/api/admin/releases/{platform...}", s.admin(s.handleDeleteRelease))
	// Device-facing public API. It shares nothing with the admin surface: its own
	// gate, no bearer, and the device key in the request body.
	device("POST", "/api/v1/register", s.withGate(s.deviceGate, s.handleRegister))
	device("POST", "/api/v1/heartbeat", s.withGate(s.deviceGate, s.handleHeartbeat))
	// Console pages and their assets.
	admin("GET", "/admin", s.withGate(s.staticGate, s.handleIndex))
	admin("GET", "/admin/{path...}", s.withGate(s.staticGate, s.handleAsset))
	// Anything else, with or without a method, is a miss.
	mux.HandleFunc("/", handleNotFound)
	return s.withObservability(mux)
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
