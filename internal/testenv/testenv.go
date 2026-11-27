// Package testenv builds a throwaway control plane: a temporary DATA_DIR, its
// own database and a ready-to-use HTTP server.
package testenv

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"

	"mesh-server/api"
	"mesh-server/app"
	"mesh-server/core"
	"mesh-server/internal/fakecli"
	"mesh-server/signing"
)

// Token is the AUTH_TOKEN every test environment uses.
const Token = "test_token_0123456789"

// sessionKey keeps issued credentials verifiable across one test.
var sessionKey = []byte("test_session_key_0123456789abcdef")

// Env is one isolated control plane.
type Env struct {
	T       *testing.T
	DataDir string
	App     *app.App
	Server  *httptest.Server
	Client  *http.Client
	Clock   *Clock
	// credential is the bearer token obtained by Login; empty before that.
	credential string
	// releasePrivate signs publish requests. Every environment gets its own key
	// pair and hands the public half to the app, because the compiled-in key
	// belongs to the offline release key nobody has here.
	releasePrivate ed25519.PrivateKey
}

// Clock is the injectable application clock.
type Clock struct {
	t   *testing.T
	now time.Time
}

// Now is the clock reading.
func (c *Clock) Now() time.Time { return c.now }

// Advance moves the clock forward and pins the stand-in easytier-cli to the same
// moment, so a credential issued after the move expires in the moved frame.
func (c *Clock) Advance(d time.Duration) {
	c.now = c.now.Add(d)
	c.t.Setenv(fakecli.NowEnv, strconv.FormatInt(c.now.Unix(), 10))
}

// Option adjusts an environment before the server is built.
type Option func(*config)

type config struct {
	login          bool
	logger         *slog.Logger
	readyzToken    string
	apiConcurrency int
	// dataDir reuses an existing DATA_DIR; empty means a fresh temporary one.
	dataDir string
	// credentialCLI points the app at the test binary as easytier-cli.
	credentialCLI bool
}

// WithReadyzToken protects /readyz the way a deployment does.
func WithReadyzToken(token string) Option {
	return func(c *config) { c.readyzToken = token }
}

// WithoutLogin leaves the client without a bearer credential.
func WithoutLogin() Option {
	return func(c *config) { c.login = false }
}

// WithAPIConcurrency shrinks the API gates, so a test can fill one of them.
func WithAPIConcurrency(n int) Option {
	return func(c *config) { c.apiConcurrency = n }
}

// WithDataDir reuses an existing state directory, so a test can open the same
// database twice (for example to drop the credential channel in between).
func WithDataDir(dir string) Option {
	return func(c *config) { c.dataDir = dir }
}

// WithCredentialCLI turns credential issuance on, using the test binary itself
// as the stand-in easytier-cli (see internal/fakecli).
func WithCredentialCLI() Option {
	return func(c *config) { c.credentialCLI = true }
}

// New builds the environment and logs the admin in.
func New(t *testing.T, options ...Option) *Env {
	t.Helper()
	cfg := config{login: true, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for _, option := range options {
		option(&cfg)
	}
	dir := cfg.dataDir
	if dir == "" {
		dir = t.TempDir()
	}
	clock := &Clock{t: t, now: time.Unix(1_732_400_000, 0)}
	// 每个环境自带一对发布密钥：编进二进制的公钥属于那把离线私钥，这里没有。
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate release key: %v", err)
	}
	appOptions := app.Options{
		Now:              clock.Now,
		ReleasePublicKey: public,
		// The publish tests serve packages from an httptest server on loopback,
		// which the hardened download client refuses by design.
		FetchClient: &http.Client{Timeout: 10 * time.Second},
	}
	if cfg.credentialCLI {
		self, err := os.Executable()
		if err != nil {
			t.Fatalf("locate test binary: %v", err)
		}
		appOptions.CredentialCLI = self
		appOptions.CredentialRPC = "127.0.0.1:15889"
	}
	// The stand-in CLI reads the same frame the app clock starts at.
	t.Setenv(fakecli.NowEnv, strconv.FormatInt(clock.now.Unix(), 10))
	instance, err := app.Open(dir, appOptions)
	if err != nil {
		t.Fatalf("open app: %v", err)
	}
	t.Cleanup(func() {
		if err := instance.Close(); err != nil {
			t.Logf("close app: %v", err)
		}
	})
	hash, err := core.NewTokenHash(Token)
	if err != nil {
		t.Fatalf("hash AUTH_TOKEN: %v", err)
	}
	handler := api.New(api.Options{
		App:         instance,
		Logger:      cfg.logger,
		ReadyzToken: cfg.readyzToken,
		TokenHash:   hash,
		SessionKey:  sessionKey,
		// The floor is part of the login path; only the wait is skipped here.
		Sleep:             func(time.Duration) {},
		MaxAPIConcurrency: cfg.apiConcurrency,
	}).Handler()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := &http.Client{Timeout: 5 * time.Second, Transport: server.Client().Transport}
	env := &Env{T: t, DataDir: dir, App: instance, Server: server, Client: client, Clock: clock, releasePrivate: private}
	if cfg.login {
		env.Login()
	}
	return env
}

// Login exchanges AUTH_TOKEN for a bearer credential.
func (e *Env) Login() {
	e.T.Helper()
	res, body := e.Do("POST", "/api/auth", map[string]string{"token": Token})
	if res.StatusCode != http.StatusOK {
		e.T.Fatalf("auth = %d, want 200: %s", res.StatusCode, body)
	}
	var out struct {
		AccessToken string `json:"accessToken"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.AccessToken == "" {
		e.T.Fatalf("decode auth response: %v (%s)", err, body)
	}
	e.credential = out.AccessToken
}

// Credential returns the current bearer token, empty when not logged in.
func (e *Env) Credential() string { return e.credential }

// Sign returns the release signature (hex) for one slot, as the operator's
// offline key would produce it.
func (e *Env) Sign(slot, platform, version, sha256 string) string {
	e.T.Helper()
	return hex.EncodeToString(ed25519.Sign(e.releasePrivate,
		[]byte(signing.String(slot, platform, version, sha256))))
}

// SignManifest returns the MESH4 signature (hex) over one manifest's file set,
// exactly as signpkg -manifest writes it into version.json before packaging.
func (e *Env) SignManifest(slot, platform, version string, files map[string]string) string {
	e.T.Helper()
	return hex.EncodeToString(ed25519.Sign(e.releasePrivate,
		[]byte(signing.ManifestString(slot, platform, version, signing.FilesDigest(files)))))
}

// Do sends a JSON request with the bearer credential.
func (e *Env) Do(method, path string, body any) (*http.Response, []byte) {
	e.T.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			e.T.Fatalf("marshal %s %s body: %v", method, path, err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, e.Server.URL+path, reader)
	if err != nil {
		e.T.Fatalf("build %s %s: %v", method, path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	e.authorize(req)
	return e.send(req, method, path)
}

// authorize attaches the bearer credential to a request built by Do.
func (e *Env) authorize(req *http.Request) {
	if e.credential != "" {
		req.Header.Set("Authorization", "Bearer "+e.credential)
	}
}

// Send performs one fully prepared request.
func (e *Env) Send(req *http.Request) (*http.Response, []byte) {
	e.T.Helper()
	return e.send(req, req.Method, req.URL.Path)
}

func (e *Env) send(req *http.Request, method, path string) (*http.Response, []byte) {
	res, err := e.Client.Do(req)
	if err != nil {
		e.T.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		e.T.Fatalf("read %s %s body: %v", method, path, err)
	}
	return res, data
}
