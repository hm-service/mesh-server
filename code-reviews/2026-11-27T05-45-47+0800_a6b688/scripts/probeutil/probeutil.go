// Package probeutil builds a throwaway control plane for this round's probes.
// It mirrors internal/testenv, but as a normal package so a probe can run with
// `go run` instead of through `go test`.
package probeutil

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"time"

	"mesh-server/api"
	"mesh-server/app"
	"mesh-server/core"
)

// Token is the AUTH_TOKEN every probe environment uses.
const Token = "probe_token_0123456789"

var sessionKey = []byte("probe_session_key_0123456789abcdef")

// Env is one isolated control plane with an admin credential already issued.
type Env struct {
	DataDir string
	App     *app.App
	Server  *httptest.Server
	Client  *http.Client
	Bearer  string

	nonces int64
}

// New opens a control plane in dir and serves it. static, when not nil, is the
// embedded frontend build.
func New(dir string, static fs.FS) (*Env, error) {
	instance, err := app.Open(dir, app.Options{
		Now: time.Now,
		// Probe file servers listen on loopback, which the hardened fetch client
		// refuses by design; no probe publishes a package.
		FetchClient: &http.Client{Timeout: 10 * time.Second},
	})
	if err != nil {
		return nil, err
	}
	hash, err := core.NewTokenHash(Token)
	if err != nil {
		return nil, err
	}
	server := httptest.NewServer(api.New(api.Options{
		App:        instance,
		Static:     static,
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		TokenHash:  hash,
		SessionKey: sessionKey,
		Sleep:      func(time.Duration) {},
	}).Handler())
	nonce, err := core.RandomBytes(core.NonceBytes)
	if err != nil {
		return nil, err
	}
	return &Env{
		DataDir: dir,
		App:     instance,
		Server:  server,
		Client:  server.Client(),
		Bearer:  core.SignBearer(sessionKey, time.Now().Add(time.Hour), nonce),
	}, nil
}

// Close stops the server and closes the database.
func (e *Env) Close() {
	e.Server.Close()
	_ = e.App.Close()
}

// Do sends one JSON request with the admin credential.
func (e *Env) Do(method, path string, body any) (*http.Response, []byte) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			panic(err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, e.Server.URL+path, reader)
	if err != nil {
		panic(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+e.Bearer)
	return e.Send(req)
}

// Send performs one fully prepared request.
func (e *Env) Send(req *http.Request) (*http.Response, []byte) {
	res, err := e.Client.Do(req)
	if err != nil {
		panic(err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		panic(err)
	}
	return res, data
}

// Device is one bound device slot.
type Device struct {
	PublicID   string
	DeviceID   string
	Hostname   string
	Pub        ed25519.PublicKey
	Priv       ed25519.PrivateKey
	ConfigTOML string
}

// Create makes an unbound slot and returns its id and registration token.
func (e *Env) Create(name, configTOML string) (string, string, error) {
	res, data := e.Do("POST", "/api/admin/devices", map[string]string{"name": name, "configToml": configTOML})
	if res.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("create device = %d: %s", res.StatusCode, data)
	}
	var out struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", "", err
	}
	return out.ID, out.Token, nil
}

// Register binds a slot to a fresh keypair over HTTP.
func (e *Env) Register(name, configTOML string) (Device, error) {
	publicID, token, err := e.Create(name, configTOML)
	if err != nil {
		return Device{}, err
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return Device{}, err
	}
	deviceID := "probe-device-" + name
	hostname := "HOST-" + name
	res, data := e.Do("POST", "/api/v1/register", map[string]string{
		"token":    token,
		"deviceId": deviceID,
		"hostname": hostname,
		"pubkey":   base64.StdEncoding.EncodeToString(pub),
	})
	if res.StatusCode != http.StatusOK {
		return Device{}, fmt.Errorf("register = %d: %s", res.StatusCode, data)
	}
	return Device{PublicID: publicID, DeviceID: deviceID, Hostname: hostname, Pub: pub, Priv: priv, ConfigTOML: configTOML}, nil
}

// NextNonce returns a fresh probe nonce.
func (e *Env) NextNonce() string {
	e.nonces++
	return fmt.Sprintf("%032x", e.nonces)
}
