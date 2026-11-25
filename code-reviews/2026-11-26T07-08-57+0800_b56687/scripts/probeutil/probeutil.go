// Package probeutil builds a throwaway control plane for the review probes. It
// mirrors internal/testenv but is a main-package helper, so probes can be run
// with `go run` instead of going through `go test`.
package probeutil

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"time"

	"mesh-server/api"
	"mesh-server/app"
	"mesh-server/core"
)

const Token = "probe_token_0123456789"

var sessionKey = []byte("probe_session_key_0123456789abcdef")

// Env is one isolated control plane with a logged-in admin client.
type Env struct {
	DataDir string
	App     *app.App
	Server  *httptest.Server
	Client  *http.Client
	Bearer  string

	nonces int64
}

// New opens a control plane in dir and serves it.
func New(dir string) (*Env, error) {
	instance, err := app.Open(dir, app.Options{
		Now: time.Now,
		// Probe file servers listen on loopback, which the hardened fetch client
		// refuses by design; the probes are not exercising that path.
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
	return e.send(req)
}

// DoRaw sends one request with caller-chosen headers, still carrying the admin
// credential.
func (e *Env) DoRaw(method, path string, body any, headers map[string]string) (*http.Response, []byte) {
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
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	req.Header.Set("Authorization", "Bearer "+e.Bearer)
	return e.send(req)
}

// Send performs one fully prepared request.
func (e *Env) Send(req *http.Request) (*http.Response, []byte) {
	return e.send(req)
}

func (e *Env) send(req *http.Request) (*http.Response, []byte) {
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

// Device is one registered device of a probe environment.
type Device struct {
	PublicID   string
	DeviceID   string
	Priv       ed25519.PrivateKey
	ConfigHash string
}

// Register creates a slot and binds it to a fresh keypair over HTTP.
func (e *Env) Register(name, configTOML string) (Device, error) {
	res, data := e.Do("POST", "/api/admin/devices", map[string]string{"name": name, "config_toml": configTOML})
	if res.StatusCode != http.StatusOK {
		return Device{}, fmt.Errorf("create device = %d: %s", res.StatusCode, data)
	}
	var created struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(data, &created); err != nil {
		return Device{}, err
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return Device{}, err
	}
	deviceID := "probe-device-" + name
	res, data = e.Do("POST", "/api/v1/register", map[string]string{
		"token":     created.Token,
		"device_id": deviceID,
		"hostname":  "HOST-" + name,
		"pubkey":    base64.StdEncoding.EncodeToString(pub),
	})
	if res.StatusCode != http.StatusOK {
		return Device{}, fmt.Errorf("register = %d: %s", res.StatusCode, data)
	}
	return Device{PublicID: created.ID, DeviceID: deviceID, Priv: priv, ConfigHash: core.Hash(configTOML)}, nil
}

// Heartbeat posts one signed heartbeat and returns the response.
func (e *Env) Heartbeat(dev Device, platform, configHash, appVersion string) (*http.Response, []byte) {
	e.nonces++
	stamp := fmt.Sprintf("%d", time.Now().Unix())
	nonce := fmt.Sprintf("%032x", e.nonces)
	msg := core.SigningString("POST", "/api/v1/heartbeat", dev.DeviceID, stamp, nonce, platform, configHash, appVersion)
	body := map[string]string{
		"deviceId":   dev.DeviceID,
		"timestamp":  stamp,
		"nonce":      nonce,
		"signature":  base64.StdEncoding.EncodeToString(ed25519.Sign(dev.Priv, []byte(msg))),
		"platform":   platform,
		"configHash": configHash,
		"appVersion": appVersion,
	}
	res, data := e.Do("POST", "/api/v1/heartbeat", body)
	return res, data
}

// FileServer serves body at every path, so a published URL can point at it.
func FileServer(body []byte) (*httptest.Server, string) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(body)
	}))
	return srv, srv.URL + "/mesh-client.tar.zst"
}
