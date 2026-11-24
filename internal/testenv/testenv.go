// Package testenv builds a throwaway control plane: a temporary DATA_DIR, its
// own database and a ready-to-use HTTP server.
package testenv

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mesh-server/api"
	"mesh-server/app"
	"mesh-server/core"
)

// Password is the admin password every test environment uses.
const Password = "test-password-12"

// Env is one isolated control plane.
type Env struct {
	T       *testing.T
	DataDir string
	App     *app.App
	Server  *httptest.Server
	Client  *http.Client
	Clock   *Clock
}

// Clock is the injectable application clock.
type Clock struct {
	t time.Time
}

// Now is the clock reading.
func (c *Clock) Now() time.Time { return c.t }

// Advance moves the clock forward.
func (c *Clock) Advance(d time.Duration) { c.t = c.t.Add(d) }

// Option adjusts an environment before the server is built.
type Option func(*config)

type config struct {
	login       bool
	logger      *slog.Logger
	readyzToken string
}

// WithReadyzToken protects /readyz the way a deployment does.
func WithReadyzToken(token string) Option {
	return func(c *config) { c.readyzToken = token }
}

// WithoutLogin leaves the client without a session cookie.
func WithoutLogin() Option {
	return func(c *config) { c.login = false }
}

// New builds the environment and logs the admin in.
func New(t *testing.T, options ...Option) *Env {
	t.Helper()
	cfg := config{login: true, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for _, option := range options {
		option(&cfg)
	}
	dir := t.TempDir()
	clock := &Clock{t: time.Unix(1_732_400_000, 0)}
	if err := writeConfig(dir); err != nil {
		t.Fatalf("write config.json: %v", err)
	}
	instance, _, err := app.Open(dir, app.Options{Now: clock.Now})
	if err != nil {
		t.Fatalf("open app: %v", err)
	}
	t.Cleanup(func() {
		if err := instance.Close(); err != nil {
			t.Logf("close app: %v", err)
		}
	})
	handler := api.New(api.Options{
		App:          instance,
		Logger:       cfg.logger,
		ReadyzToken:  cfg.readyzToken,
		MinAuthDelay: 0,
	}).Handler()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookie jar: %v", err)
	}
	client := &http.Client{Jar: jar, Timeout: 5 * time.Second, Transport: server.Client().Transport}
	env := &Env{T: t, DataDir: dir, App: instance, Server: server, Client: client, Clock: clock}
	if cfg.login {
		env.Login()
	}
	return env
}

// writeConfig stores a config.json whose password is Password, so a test
// environment does not have to replay the first-start password from the log.
func writeConfig(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	salt, hash, err := core.HashPassword(Password)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(map[string]string{
		"listen":        ":8088",
		"password_salt": salt,
		"password_hash": hash,
	})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "config.json"), raw, 0o600)
}

// Login obtains the admin session.
func (e *Env) Login() {
	e.T.Helper()
	res, body := e.Do("POST", "/api/login", map[string]string{"password": Password})
	if res.StatusCode != http.StatusNoContent {
		e.T.Fatalf("login = %d, want 204: %s", res.StatusCode, body)
	}
}

// Do sends a JSON request with the session cookie.
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
	return e.send(req, method, path)
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
