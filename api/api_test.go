package api_test

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mesh-server/app"
	"mesh-server/core"
	"mesh-server/internal/testenv"
)

const testTOML = "hostname = \"auto\"\n"

// The structs below mirror the wire contract instead of the server types, so a
// renamed JSON field fails here no matter how the internals are organised.
type deviceSummary struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Hostname       string `json:"hostname"`
	DeviceID       string `json:"device_id"`
	Status         string `json:"status"`
	ConfigHash     string `json:"config_hash"`
	LastConfigHash string `json:"last_config_hash"`
	LastAppHash    string `json:"last_app_hash"`
	ConfigState    string `json:"config_state"`
	AppState       string `json:"app_state"`
	AppVersion     string `json:"app_version"`
	CreatedAt      int64  `json:"created_at"`
	LastSeenAt     int64  `json:"last_seen_at"`
}

type deviceDetail struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	DeviceID    string `json:"device_id"`
	Hostname    string `json:"hostname"`
	Pubkey      string `json:"pubkey"`
	ConfigTOML  string `json:"config_toml"`
	ConfigHash  string `json:"config_hash"`
	LastAppHash string `json:"last_app_hash"`
	ConfigState string `json:"config_state"`
	AppState    string `json:"app_state"`
	Status      string `json:"status"`
	LastIP      string `json:"last_ip"`
	CreatedAt   int64  `json:"created_at"`
	LastSeenAt  int64  `json:"last_seen_at"`
}

type createDeviceResponse struct {
	ID    string `json:"id"`
	Token string `json:"token"`
}

type configResponse struct {
	ConfigTOML string `json:"config_toml"`
	ConfigHash string `json:"config_hash"`
}

type updateInfo struct {
	Version string `json:"version"`
	URL     string `json:"url"`
	Notes   string `json:"notes,omitempty"`
	SHA256  string `json:"sha256,omitempty"`
}

type heartbeatResponse struct {
	ConfigHash string      `json:"config_hash,omitempty"`
	AppUpdate  *updateInfo `json:"app_update,omitempty"`
}

type settingsResponse struct {
	DefaultTOML string `json:"default_toml"`
	AppVersion  string `json:"app_version"`
	AppURL      string `json:"app_url"`
	AppHash     string `json:"app_hash"`
	AppSHA256   string `json:"app_sha256"`
	AppNotes    string `json:"app_notes"`
	AppSource   string `json:"app_source"`
	AppFileName string `json:"app_file_name"`
	AppSize     int64  `json:"app_size"`
}

// signedHeaders builds the CONTRACT.md 1.3 headers for one request.
func signedHeaders(t *testing.T, deviceID string, priv ed25519.PrivateKey, method, path string, body []byte, ts int64, nonce string) map[string]string {
	t.Helper()
	sum := sha256.Sum256(body)
	stamp := fmt.Sprintf("%d", ts)
	msg := core.SigningString(method, path, hex.EncodeToString(sum[:]), stamp, nonce)
	return map[string]string{
		"X-Device-Id": deviceID,
		"X-Timestamp": stamp,
		"X-Nonce":     nonce,
		"X-Signature": base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte(msg))),
	}
}

func newKeypair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}
	return pub, priv
}

func newNonce(counter *int64) string {
	*counter++
	return fmt.Sprintf("%032x", *counter)
}

// signed sends one request carrying device signature headers.
func signed(t *testing.T, env *testenv.Env, method, path string, body []byte, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, env.Server.URL+path, reader)
	if err != nil {
		t.Fatalf("build %s %s: %v", method, path, err)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	return env.Send(req)
}

func createDevice(t *testing.T, env *testenv.Env, name, toml string) (id, token string) {
	t.Helper()
	res, data := env.Do("POST", "/api/devices", map[string]string{"name": name, "config_toml": toml})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("create device = %d, want 200: %s", res.StatusCode, data)
	}
	var out createDeviceResponse
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if out.ID == "" || out.Token == "" {
		t.Fatalf("create response missing id/token: %s", data)
	}
	return out.ID, out.Token
}

func register(t *testing.T, env *testenv.Env, token, deviceID, hostname string, pub ed25519.PublicKey) (int, []byte) {
	t.Helper()
	res, data := env.Do("POST", "/api/register", map[string]string{
		"token":     token,
		"device_id": deviceID,
		"hostname":  hostname,
		"pubkey":    base64.StdEncoding.EncodeToString(pub),
	})
	return res.StatusCode, data
}

type registeredDevice struct {
	internalID string
	deviceID   string
	priv       ed25519.PrivateKey
	configTOML string
	configHash string
}

func registerDevice(t *testing.T, env *testenv.Env, name, toml string) registeredDevice {
	t.Helper()
	id, token := createDevice(t, env, name, toml)
	pub, priv := newKeypair(t)
	deviceID := "dev-" + name
	status, data := register(t, env, token, deviceID, "HOST-"+name, pub)
	if status != http.StatusOK {
		t.Fatalf("register = %d, want 200: %s", status, data)
	}
	var out configResponse
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("decode register response: %v", err)
	}
	wantHash := core.Hash(toml)
	if out.ConfigTOML != toml || out.ConfigHash != wantHash {
		t.Fatalf("register returned config %q/%q, want %q/%q", out.ConfigTOML, out.ConfigHash, toml, wantHash)
	}
	return registeredDevice{internalID: id, deviceID: deviceID, priv: priv, configTOML: toml, configHash: wantHash}
}

// releaseZip packs client bytes into a release package. The server only accepts
// this format, and the package must carry the kernel beside the client.
func releaseZip(t *testing.T, client []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	write := func(name string, data []byte) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}
	write("mesh-client.exe", client)
	write("bin/easytier-core.exe", []byte("core"))
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

// fileServer serves body at every path so external URL releases are testable.
func fileServer(t *testing.T, body []byte) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/mesh-client.zip"
}

func TestAuthRequiresCorrectToken(t *testing.T) {
	env := testenv.New(t, testenv.WithoutLogin())
	res, _ := env.Do("POST", "/api/auth", map[string]string{"token": "wrong_token_0123456789"})
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong token = %d, want 401", res.StatusCode)
	}
	anonymous := &http.Client{Transport: env.Server.Client().Transport}
	res, err := anonymous.Get(env.Server.URL + "/api/devices")
	if err != nil {
		t.Fatalf("anonymous GET /api/devices: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous GET /api/devices = %d, want 401", res.StatusCode)
	}
}

// TestCredentialExpires pins the stateless model: the issued credential carries
// its own expiry and the server keeps no table to look it up in.
func TestCredentialExpires(t *testing.T) {
	env := testenv.New(t)
	env.Clock.Advance(25 * time.Hour)

	res, _ := env.Do("GET", "/api/devices", nil)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("list devices with an expired credential = %d, want 401", res.StatusCode)
	}
}

// TestTamperedCredentialIsRejected checks the signature before the expiry.
func TestTamperedCredentialIsRejected(t *testing.T) {
	env := testenv.New(t)
	good := env.Credential()
	tampered := good[:len(good)-1] + string(good[len(good)-1]^1)

	req, err := http.NewRequest("GET", env.Server.URL+"/api/devices", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+tampered)
	res, _ := env.Send(req)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("tampered credential = %d, want 401", res.StatusCode)
	}
}

func TestRegisterIsIdempotent(t *testing.T) {
	env := testenv.New(t)
	var nonces int64
	id, token := createDevice(t, env, "laptop", testTOML)
	pub, priv := newKeypair(t)
	pubB64 := base64.StdEncoding.EncodeToString(pub)

	status, data := register(t, env, token, "device-1", "DESKTOP-1", pub)
	if status != http.StatusOK {
		t.Fatalf("register = %d: %s", status, data)
	}
	var first configResponse
	if err := json.Unmarshal(data, &first); err != nil {
		t.Fatal(err)
	}
	if first.ConfigTOML != testTOML || first.ConfigHash != core.Hash(testTOML) {
		t.Fatalf("register config = %+v", first)
	}

	// The token is single use: another device_id cannot register with it.
	if status, _ := register(t, env, token, "device-2", "OTHER", pub); status != http.StatusUnauthorized {
		t.Fatalf("reused token = %d, want 401", status)
	}

	// The same device_id and pubkey are idempotent, but only with a valid
	// credential: the signature is made with the key the slot is bound to.
	body, err := json.Marshal(map[string]string{
		"device_id": "device-1", "hostname": "DESKTOP-1", "pubkey": pubB64,
	})
	if err != nil {
		t.Fatal(err)
	}
	headers := signedHeaders(t, "device-1", priv, "POST", "/api/register", body, env.Clock.Now().Unix(), newNonce(&nonces))
	res, data := signed(t, env, "POST", "/api/register", body, headers)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("signed idempotent register = %d: %s", res.StatusCode, data)
	}
	var again configResponse
	if err := json.Unmarshal(data, &again); err != nil {
		t.Fatal(err)
	}
	if again.ConfigHash != first.ConfigHash {
		t.Fatalf("idempotent register = %+v, want %+v", again, first)
	}

	// The same device_id with another pubkey conflicts, once the credential of
	// the bound key has been presented.
	otherPub, _ := newKeypair(t)
	status, _ = register(t, env, "", "device-1", "DESKTOP-1", otherPub)
	if status != http.StatusUnauthorized {
		t.Fatalf("unsigned conflict attempt = %d, want 401", status)
	}
	conflictBody, err := json.Marshal(map[string]string{
		"device_id": "device-1", "hostname": "DESKTOP-1", "pubkey": base64.StdEncoding.EncodeToString(otherPub),
	})
	if err != nil {
		t.Fatal(err)
	}
	headers = signedHeaders(t, "device-1", priv, "POST", "/api/register", conflictBody, env.Clock.Now().Unix(), newNonce(&nonces))
	res, data = signed(t, env, "POST", "/api/register", conflictBody, headers)
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("pubkey conflict = %d, want 409: %s", res.StatusCode, data)
	}

	stored, found, err := env.App.DeviceByID(id)
	if err != nil || !found {
		t.Fatalf("device disappeared from the database: found %v, %v", found, err)
	}
	if stored.Token != "" || stored.TokenExpiresAt != 0 {
		t.Fatalf("token not cleared after register: %+v", stored)
	}
	if stored.DeviceID != "device-1" || stored.Hostname != "DESKTOP-1" {
		t.Fatalf("device not bound: %+v", stored)
	}
}

func TestRegisterRejectsExpiredToken(t *testing.T) {
	env := testenv.New(t)
	_, fresh := createDevice(t, env, "fresh", testTOML)
	env.Clock.Advance(9 * time.Minute)
	pub, _ := newKeypair(t)
	if status, data := register(t, env, fresh, "device-fresh", "HOST-FRESH", pub); status != http.StatusOK {
		t.Fatalf("fresh register = %d: %s", status, data)
	}

	_, stale := createDevice(t, env, "stale", testTOML)
	env.Clock.Advance(11 * time.Minute)
	if status, _ := register(t, env, stale, "device-stale", "HOST-STALE", pub); status != http.StatusUnauthorized {
		t.Fatalf("expired token = %d, want 401", status)
	}
}

func TestSignatureTampering(t *testing.T) {
	env := testenv.New(t)
	var nonces int64
	dev := registerDevice(t, env, "laptop", testTOML)
	now := env.Clock.Now().Unix()
	body := []byte(fmt.Sprintf(`{"config_hash":%q}`, dev.configHash))

	valid := signedHeaders(t, dev.deviceID, dev.priv, "POST", "/api/heartbeat", body, now, newNonce(&nonces))
	res, data := signed(t, env, "POST", "/api/heartbeat", body, valid)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("valid heartbeat = %d, want 204: %s", res.StatusCode, data)
	}

	// The signed body is replaced after signing.
	tampered := signedHeaders(t, dev.deviceID, dev.priv, "POST", "/api/heartbeat", body, now, newNonce(&nonces))
	res, _ = signed(t, env, "POST", "/api/heartbeat", []byte(`{"config_hash":"tampered"}`), tampered)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("tampered body = %d, want 401", res.StatusCode)
	}

	// The signature was made for another method and path.
	wrongPath := signedHeaders(t, dev.deviceID, dev.priv, "GET", "/api/config", nil, now, newNonce(&nonces))
	res, _ = signed(t, env, "POST", "/api/heartbeat", body, wrongPath)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong path signature = %d, want 401", res.StatusCode)
	}

	// Stale and future timestamps are outside the 300s window.
	for _, ts := range []int64{now - 301, now + 301} {
		headers := signedHeaders(t, dev.deviceID, dev.priv, "POST", "/api/heartbeat", body, ts, newNonce(&nonces))
		res, _ = signed(t, env, "POST", "/api/heartbeat", body, headers)
		if res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("timestamp %d = %d, want 401", ts, res.StatusCode)
		}
	}

	// An unknown device id cannot authenticate even with a valid key.
	unknown := signedHeaders(t, "dev-unknown", dev.priv, "POST", "/api/heartbeat", body, now, newNonce(&nonces))
	res, _ = signed(t, env, "POST", "/api/heartbeat", body, unknown)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unknown device = %d, want 401", res.StatusCode)
	}
}

func TestNonceReplayIsRejected(t *testing.T) {
	env := testenv.New(t)
	var nonces int64
	dev := registerDevice(t, env, "laptop", testTOML)
	body := []byte(fmt.Sprintf(`{"config_hash":%q}`, dev.configHash))
	now := env.Clock.Now().Unix()
	nonce := newNonce(&nonces)
	headers := signedHeaders(t, dev.deviceID, dev.priv, "POST", "/api/heartbeat", body, now, nonce)

	res, data := signed(t, env, "POST", "/api/heartbeat", body, headers)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("first request = %d, want 204: %s", res.StatusCode, data)
	}
	res, _ = signed(t, env, "POST", "/api/heartbeat", body, headers)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("replayed nonce = %d, want 401", res.StatusCode)
	}

	// A fresh nonce for the same body is accepted.
	headers = signedHeaders(t, dev.deviceID, dev.priv, "POST", "/api/heartbeat", body, now, newNonce(&nonces))
	res, _ = signed(t, env, "POST", "/api/heartbeat", body, headers)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("fresh nonce = %d, want 204", res.StatusCode)
	}
}

// publishUpload saves a release through the upload mode and returns the result.
func publishUpload(t *testing.T, env *testenv.Env, version, notes, defaultTOML string, file []byte) settingsResponse {
	t.Helper()
	res, data := uploadSettings(t, env, version, notes, defaultTOML, file)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("upload settings = %d, want 200: %s", res.StatusCode, data)
	}
	var out settingsResponse
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("decode settings: %v", err)
	}
	return out
}

// putSettings saves settings through the admin API and returns the response.
func putSettings(t *testing.T, env *testenv.Env, fields map[string]string) settingsResponse {
	t.Helper()
	res, data := env.Do("PUT", "/api/admin/settings", fields)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("PUT settings = %d, want 200: %s", res.StatusCode, data)
	}
	var out settingsResponse
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("decode settings: %v", err)
	}
	return out
}

// uploadSettings sends the multipart upload mode. A nil file omits the file part.
func uploadSettings(t *testing.T, env *testenv.Env, version, notes, defaultTOML string, file []byte) (*http.Response, []byte) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("mode", "upload"); err != nil {
		t.Fatalf("write mode: %v", err)
	}
	if err := mw.WriteField("app_version", version); err != nil {
		t.Fatalf("write app_version: %v", err)
	}
	if err := mw.WriteField("app_notes", notes); err != nil {
		t.Fatalf("write app_notes: %v", err)
	}
	if defaultTOML != "" {
		if err := mw.WriteField("default_toml", defaultTOML); err != nil {
			t.Fatalf("write default_toml: %v", err)
		}
	}
	if file != nil {
		part, err := mw.CreateFormFile("file", "mesh-client.zip")
		if err != nil {
			t.Fatalf("create file part: %v", err)
		}
		if _, err := part.Write(file); err != nil {
			t.Fatalf("write file part: %v", err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	req, err := http.NewRequest("PUT", env.Server.URL+"/api/admin/settings", &buf)
	if err != nil {
		t.Fatalf("build upload request: %v", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	env.Authorize(req)
	return env.Send(req)
}

func heartbeat(t *testing.T, env *testenv.Env, dev registeredDevice, nonces *int64, configHash, appHash string) (*http.Response, []byte) {
	t.Helper()
	body := []byte(fmt.Sprintf(`{"config_hash":%q,"app_hash":%q}`, configHash, appHash))
	headers := signedHeaders(t, dev.deviceID, dev.priv, "POST", "/api/heartbeat", body, env.Clock.Now().Unix(), newNonce(nonces))
	return signed(t, env, "POST", "/api/heartbeat", body, headers)
}

func TestHeartbeatSemantics(t *testing.T) {
	env := testenv.New(t)
	var nonces int64
	dev := registerDevice(t, env, "laptop", testTOML)

	// Publish a build so app_hash has a value to compare against.
	build := releaseZip(t, []byte("client build one"))
	release := publishUpload(t, env, "2026.11.24-4", "修复窄屏布局", "", build)
	sum := sha256.Sum256(build)
	wantSHA := hex.EncodeToString(sum[:])

	decode := func(data []byte) heartbeatResponse {
		t.Helper()
		var out heartbeatResponse
		if err := json.Unmarshal(data, &out); err != nil {
			t.Fatalf("decode heartbeat: %v", err)
		}
		return out
	}

	// Both hashes match: 204 with no body at all.
	res, data := heartbeat(t, env, dev, &nonces, dev.configHash, release.AppHash)
	if res.StatusCode != http.StatusNoContent || len(data) != 0 {
		t.Fatalf("heartbeat with current hashes = %d (%s), want 204 and no body", res.StatusCode, data)
	}

	// Only the app differs: app_update without config_hash.
	res, data = heartbeat(t, env, dev, &nonces, dev.configHash, "0000000000000000")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("app-only heartbeat = %d, want 200: %s", res.StatusCode, data)
	}
	got := decode(data)
	wantUpdate := updateInfo{Version: "2026.11.24-4", URL: release.AppURL, Notes: "修复窄屏布局", SHA256: wantSHA}
	if got.ConfigHash != "" || got.AppUpdate == nil || *got.AppUpdate != wantUpdate {
		t.Fatalf("app-only heartbeat = %+v, want only app_update %+v", got, wantUpdate)
	}
	if strings.Contains(string(data), `"config_hash"`) {
		t.Fatalf("app-only heartbeat body = %s, want no config_hash", data)
	}

	// Only the config differs: config_hash without update.
	res, data = heartbeat(t, env, dev, &nonces, "0000000000000000", release.AppHash)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("config-only heartbeat = %d, want 200: %s", res.StatusCode, data)
	}
	got = decode(data)
	if got.AppUpdate != nil || got.ConfigHash != dev.configHash {
		t.Fatalf("config-only heartbeat = %+v, want config_hash %q", got, dev.configHash)
	}
	if strings.Contains(string(data), `"app_update"`) {
		t.Fatalf("config-only heartbeat body = %s, want no app_update", data)
	}

	// Both differ: both fields.
	res, data = heartbeat(t, env, dev, &nonces, "0000000000000000", "0000000000000000")
	got = decode(data)
	if res.StatusCode != http.StatusOK || got.ConfigHash != dev.configHash || got.AppUpdate == nil || *got.AppUpdate != wantUpdate {
		t.Fatalf("full heartbeat = %d %+v, want config_hash and app_update", res.StatusCode, got)
	}

	// A new config is fetched through GET /api/config with its 16-char hash.
	const newTOML = `hostname = "laptop"`
	res, data = env.Do("PUT", "/api/devices/"+dev.internalID, map[string]string{"name": "laptop", "config_toml": newTOML})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("update device = %d, want 200: %s", res.StatusCode, data)
	}
	var updated map[string]string
	if err := json.Unmarshal(data, &updated); err != nil {
		t.Fatalf("decode update: %v", err)
	}
	newHash := core.Hash(newTOML)
	if updated["config_hash"] != newHash || len(newHash) != 16 {
		t.Fatalf("update config_hash = %q, want %q with 16 characters", updated["config_hash"], newHash)
	}
	headers := signedHeaders(t, dev.deviceID, dev.priv, "GET", "/api/config", nil, env.Clock.Now().Unix(), newNonce(&nonces))
	res, data = signed(t, env, "GET", "/api/config", nil, headers)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("get config = %d, want 200: %s", res.StatusCode, data)
	}
	var cfg configResponse
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	if cfg.ConfigTOML != newTOML || cfg.ConfigHash != newHash {
		t.Fatalf("config = %+v, want %q/%q", cfg, newTOML, newHash)
	}

	// With the new config and the published build the client is up to date.
	if res, data = heartbeat(t, env, dev, &nonces, newHash, release.AppHash); res.StatusCode != http.StatusNoContent {
		t.Fatalf("heartbeat after config update = %d (%s), want 204", res.StatusCode, data)
	}

	// Publishing another build flips app_hash and the update comes back.
	next := publishUpload(t, env, "2026.11.25-1", "", "", releaseZip(t, []byte("client build two")))
	res, data = heartbeat(t, env, dev, &nonces, newHash, release.AppHash)
	got = decode(data)
	if res.StatusCode != http.StatusOK || got.AppUpdate == nil || got.AppUpdate.URL != next.AppURL || got.ConfigHash != "" {
		t.Fatalf("heartbeat after release = %d %+v, want only app_update %q", res.StatusCode, got, next.AppURL)
	}
}

// TestReportedHashesShowInAdmin covers what the console uses to tell whether a
// device has already picked up the current config and build.
func TestReportedHashesShowInAdmin(t *testing.T) {
	env := testenv.New(t)
	var nonces int64
	dev := registerDevice(t, env, "laptop", testTOML)
	release := publishUpload(t, env, "2026.11.24-4", "", "", releaseZip(t, []byte("client build one")))

	summary := func() deviceSummary {
		t.Helper()
		res, data := env.Do("GET", "/api/devices", nil)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("list devices = %d: %s", res.StatusCode, data)
		}
		var out []deviceSummary
		if err := json.Unmarshal(data, &out); err != nil {
			t.Fatalf("decode devices: %v", err)
		}
		for _, d := range out {
			if d.ID == dev.internalID {
				return d
			}
		}
		t.Fatalf("device %s missing from list", dev.internalID)
		return deviceSummary{}
	}

	// Nothing reported yet: both states are stale and no hash was recorded.
	if got := summary(); got.ConfigState != "stale" || got.AppState != "stale" || got.LastConfigHash != "" || got.LastAppHash != "" {
		t.Fatalf("before any heartbeat = %+v", got)
	}

	// A device running an old config and an old build.
	if res, data := heartbeat(t, env, dev, &nonces, "0000000000000000", "1111111111111111"); res.StatusCode >= http.StatusBadRequest {
		t.Fatalf("heartbeat = %d: %s", res.StatusCode, data)
	}
	got := summary()
	if got.LastConfigHash != "0000000000000000" || got.LastAppHash != "1111111111111111" {
		t.Fatalf("recorded hashes = %+v", got)
	}
	if got.ConfigState != "stale" || got.AppState != "stale" || got.AppVersion != "2026.11.24-4" {
		t.Fatalf("stale states = %+v", got)
	}

	// After catching up both states flip without any further server change.
	if res, data := heartbeat(t, env, dev, &nonces, dev.configHash, release.AppHash); res.StatusCode >= http.StatusBadRequest {
		t.Fatalf("heartbeat = %d: %s", res.StatusCode, data)
	}
	got = summary()
	if got.ConfigState != "current" || got.AppState != "current" {
		t.Fatalf("current states = %+v", got)
	}

	// The detail view reports the same states.
	res, data := env.Do("GET", "/api/devices/"+dev.internalID, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("get device = %d: %s", res.StatusCode, data)
	}
	var detail deviceDetail
	if err := json.Unmarshal(data, &detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if detail.ConfigState != "current" || detail.AppState != "current" || detail.LastAppHash != release.AppHash {
		t.Fatalf("detail states = %+v", detail)
	}

	// Without a published build there is nothing to update to: still current.
	putSettings(t, env, map[string]string{"app_version": "", "app_url": ""})
	if got := summary(); got.AppState != "current" || got.AppVersion != "" {
		t.Fatalf("no release = %+v", got)
	}
}

func TestDeletedDeviceGetsRevoked(t *testing.T) {
	env := testenv.New(t)
	var nonces int64
	dev := registerDevice(t, env, "laptop", testTOML)

	res, data := env.Do("DELETE", "/api/devices/"+dev.internalID, nil)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("delete device = %d, want 204: %s", res.StatusCode, data)
	}
	res, data = heartbeat(t, env, dev, &nonces, dev.configHash, "")
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("deleted device heartbeat = %d, want 401", res.StatusCode)
	}
	if !strings.Contains(string(data), `"revoked"`) {
		t.Fatalf("deleted device body = %s, want code revoked", data)
	}
}

func TestDeviceStatusDerivation(t *testing.T) {
	env := testenv.New(t)
	var nonces int64
	createDevice(t, env, "waiting", testTOML)
	dev := registerDevice(t, env, "laptop", testTOML)

	statuses := func() map[string]string {
		t.Helper()
		res, data := env.Do("GET", "/api/devices", nil)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("list devices = %d: %s", res.StatusCode, data)
		}
		var list []deviceSummary
		if err := json.Unmarshal(data, &list); err != nil {
			t.Fatalf("decode list: %v", err)
		}
		out := make(map[string]string, len(list))
		for _, item := range list {
			out[item.Name] = item.Status
		}
		return out
	}

	got := statuses()
	if got["waiting"] != "pending" {
		t.Fatalf("unregistered status = %q, want pending", got["waiting"])
	}
	if got["laptop"] != "online" {
		t.Fatalf("fresh status = %q, want online", got["laptop"])
	}

	env.Clock.Advance(91 * time.Second)
	if got = statuses(); got["laptop"] != "offline" {
		t.Fatalf("status after 91s = %q, want offline", got["laptop"])
	}

	if res, data := heartbeat(t, env, dev, &nonces, dev.configHash, ""); res.StatusCode != http.StatusNoContent {
		t.Fatalf("heartbeat = %d, want 204: %s", res.StatusCode, data)
	}
	if got = statuses(); got["laptop"] != "online" {
		t.Fatalf("status after heartbeat = %q, want online", got["laptop"])
	}
}

func TestDeviceLimitReturnsConflict(t *testing.T) {
	env := testenv.New(t)
	for i := range app.MaxDevices {
		createDevice(t, env, fmt.Sprintf("dev-%03d", i), testTOML)
	}
	res, data := env.Do("POST", "/api/devices", map[string]string{"name": "overflow", "config_toml": testTOML})
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("device over limit = %d, want 409: %s", res.StatusCode, data)
	}
	if !strings.Contains(string(data), "device_limit") {
		t.Fatalf("body = %s, want device_limit", data)
	}
}

func TestReissuedTokenReenrollsDevice(t *testing.T) {
	env := testenv.New(t)
	var nonces int64
	dev := registerDevice(t, env, "laptop", testTOML)

	res, data := env.Do("POST", "/api/devices/"+dev.internalID+"/token", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("issue token = %d, want 200: %s", res.StatusCode, data)
	}
	var issued struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(data, &issued); err != nil || issued.Token == "" {
		t.Fatalf("issue token response = %s", data)
	}

	newPub, newPriv := newKeypair(t)
	if status, body := register(t, env, issued.Token, "device-2", "HOST-2", newPub); status != http.StatusOK {
		t.Fatalf("re-enroll = %d: %s", status, body)
	}

	// The previous identity of the slot is revoked.
	res, data = heartbeat(t, env, dev, &nonces, dev.configHash, "")
	if res.StatusCode != http.StatusUnauthorized || !strings.Contains(string(data), "revoked") {
		t.Fatalf("old identity heartbeat = %d: %s, want 401 revoked", res.StatusCode, data)
	}

	// The new identity of the same slot authenticates.
	updated := registeredDevice{deviceID: "device-2", priv: newPriv, configHash: dev.configHash}
	res, data = heartbeat(t, env, updated, &nonces, dev.configHash, "")
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("new identity heartbeat = %d, want 204: %s", res.StatusCode, data)
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	env := testenv.New(t)

	// No peer.default.toml yet: the template reads as empty, not as the built-in one.
	res, data := env.Do("GET", "/api/admin/settings", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET settings = %d, want 200: %s", res.StatusCode, data)
	}
	var empty settingsResponse
	if err := json.Unmarshal(data, &empty); err != nil {
		t.Fatalf("decode settings: %v", err)
	}
	if empty.DefaultTOML != "" || empty.AppVersion != "" || empty.AppURL != "" {
		t.Fatalf("fresh settings = %+v, want all empty", empty)
	}

	const toml = `hostname = "saved-by-settings"`
	external := []byte("external mesh client binary")
	externalPkg := releaseZip(t, external)
	url := fileServer(t, externalPkg)
	saved := putSettings(t, env, map[string]string{
		"default_toml": toml,
		"app_version":  "0.3.0",
		"app_url":      url,
		"app_notes":    "修复窄屏布局",
	})
	// app_hash describes the client inside the package; app_sha256 the package.
	externalSum := sha256.Sum256(external)
	wantFull := hex.EncodeToString(externalSum[:])
	pkgSum := sha256.Sum256(externalPkg)
	wantPkg := hex.EncodeToString(pkgSum[:])
	if saved.DefaultTOML != toml || saved.AppVersion != "0.3.0" || saved.AppURL != url ||
		saved.AppNotes != "修复窄屏布局" || saved.AppSource != "url" || saved.AppHash != wantFull[:16] ||
		saved.AppSHA256 != wantPkg {
		t.Fatalf("save response = %+v, want hash %s / %s", saved, wantFull[:16], wantFull)
	}

	res, data = env.Do("GET", "/api/admin/settings", nil)
	var got settingsResponse
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode settings: %v", err)
	}
	if got != saved {
		t.Fatalf("GET settings = %+v, want %+v", got, saved)
	}

	// The template is a file; the version and URL live in the database.
	raw, err := os.ReadFile(filepath.Join(env.DataDir, "peer.default.toml"))
	if err != nil {
		t.Fatalf("read peer.default.toml: %v", err)
	}
	if string(raw) != toml {
		t.Fatalf("peer.default.toml = %q, want %q", raw, toml)
	}
	if stored, err := env.App.Settings(); err != nil || stored.AppVersion != "0.3.0" || stored.AppURL != url {
		t.Fatalf("stored settings = %+v, %v", stored, err)
	}

	// Omitted fields keep their value.
	partial := putSettings(t, env, map[string]string{"app_version": "0.4.0"})
	if partial.AppVersion != "0.4.0" || partial.AppURL != url || partial.DefaultTOML != toml || partial.AppHash != wantFull[:16] || partial.AppNotes != "修复窄屏布局" {
		t.Fatalf("partial update = %+v", partial)
	}

	// Both empty is allowed and clears the update notification.
	cleared := putSettings(t, env, map[string]string{"app_version": "", "app_url": ""})
	if cleared.AppVersion != "" || cleared.AppURL != "" || cleared.AppNotes != "" || cleared.AppSource != "" ||
		cleared.AppFileName != "" || cleared.AppSize != 0 || cleared.AppHash != "" || cleared.AppSHA256 != "" {
		t.Fatalf("cleared settings = %+v", cleared)
	}
}

func TestSettingsRejectIncompleteAppUpdate(t *testing.T) {
	env := testenv.New(t)

	res, data := env.Do("PUT", "/api/admin/settings", map[string]string{"app_version": "0.3.0", "app_url": ""})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("version without url = %d, want 400: %s", res.StatusCode, data)
	}
	if !strings.Contains(string(data), "invalid_argument") || !strings.Contains(string(data), "app_url 不能为空") {
		t.Fatalf("version without url body = %s", data)
	}
	if stored, err := env.App.Settings(); err != nil || stored.AppVersion != "" || stored.AppURL != "" {
		t.Fatalf("rejected update was stored: %+v, %v", stored, err)
	}

	res, data = env.Do("PUT", "/api/admin/settings", map[string]string{"app_url": "http://example.com/a.exe"})
	if res.StatusCode != http.StatusBadRequest || !strings.Contains(string(data), "app_version 不能为空") {
		t.Fatalf("url without version = %d: %s", res.StatusCode, data)
	}

	res, data = env.Do("PUT", "/api/admin/settings", map[string]string{"app_version": "0.3.0", "app_url": "ftp://example.com/a.exe"})
	if res.StatusCode != http.StatusBadRequest || !strings.Contains(string(data), "http(s)") {
		t.Fatalf("non-http url = %d: %s", res.StatusCode, data)
	}

	// A URL the server cannot read is rejected: use the upload mode instead.
	res, data = env.Do("PUT", "/api/admin/settings", map[string]string{"app_version": "0.3.0", "app_url": "http://127.0.0.1:1/mesh-client.exe"})
	if res.StatusCode != http.StatusBadRequest || !strings.Contains(string(data), "上传") {
		t.Fatalf("unreachable url = %d: %s", res.StatusCode, data)
	}
}

func TestSavedTemplateAppliesToNewDevices(t *testing.T) {
	env := testenv.New(t)
	const custom = `hostname = "from-settings"
ipv4 = "10.126.126.9/24"`
	putSettings(t, env, map[string]string{"default_toml": custom})

	// The create dialog prefills from GET /api/template.
	res, data := env.Do("GET", "/api/template", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET template = %d: %s", res.StatusCode, data)
	}
	var tmpl struct {
		TOML string `json:"toml"`
	}
	if err := json.Unmarshal(data, &tmpl); err != nil {
		t.Fatalf("decode template: %v", err)
	}
	if tmpl.TOML != custom {
		t.Fatalf("template = %q, want %q", tmpl.TOML, custom)
	}

	id, _ := createDevice(t, env, "fresh", tmpl.TOML)
	res, data = env.Do("GET", "/api/devices/"+id, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("get device = %d: %s", res.StatusCode, data)
	}
	var device deviceDetail
	if err := json.Unmarshal(data, &device); err != nil {
		t.Fatalf("decode device: %v", err)
	}
	if device.ConfigTOML != custom || device.ConfigHash != core.Hash(custom) {
		t.Fatalf("new device config = %q/%q, want the saved template", device.ConfigTOML, device.ConfigHash)
	}
}

func TestUploadReleaseMode(t *testing.T) {
	env := testenv.New(t)
	const version = "2026.11.24-3"
	const toml = `hostname = "uploaded"`
	client := []byte{0x4d, 0x5a, 0x90, 0x00, 0xff, 0x7f, 0x0a, 0x01}
	file := releaseZip(t, client)

	res, data := uploadSettings(t, env, version, "首个上传版本", toml, file)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("upload = %d, want 200: %s", res.StatusCode, data)
	}
	var saved settingsResponse
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatalf("decode upload response: %v", err)
	}
	sum := sha256.Sum256(client)
	full := hex.EncodeToString(sum[:])
	pkgSum := sha256.Sum256(file)
	pkgFull := hex.EncodeToString(pkgSum[:])
	name := version + "-mesh-client.zip"
	want := settingsResponse{
		DefaultTOML: toml,
		AppVersion:  version,
		AppURL:      "/api/updates/" + name,
		AppNotes:    "首个上传版本",
		AppSource:   "upload",
		AppFileName: name,
		AppSize:     int64(len(file)),
		AppHash:     full[:16],
		AppSHA256:   pkgFull,
	}
	if saved != want {
		t.Fatalf("upload response = %+v, want %+v", saved, want)
	}

	// The stored configuration reads back the same way.
	res, data = env.Do("GET", "/api/admin/settings", nil)
	var got settingsResponse
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode settings: %v", err)
	}
	if got != want {
		t.Fatalf("GET settings = %+v, want %+v", got, want)
	}

	// The hosted file is downloadable without the admin cookie.
	download := func(path string) (string, []byte) {
		t.Helper()
		anonymous := &http.Client{Transport: env.Server.Client().Transport, Timeout: 5 * time.Second}
		res, err := anonymous.Get(env.Server.URL + path)
		if err != nil {
			t.Fatalf("download %s: %v", path, err)
		}
		defer res.Body.Close()
		body, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatalf("read download: %v", err)
		}
		if res.StatusCode != http.StatusOK {
			t.Fatalf("download %s = %d: %s", path, res.StatusCode, body)
		}
		if ct := res.Header.Get("Content-Type"); ct != "application/octet-stream" {
			t.Fatalf("download content-type = %q", ct)
		}
		return path, body
	}
	if _, body := download(want.AppURL); !bytes.Equal(body, file) {
		t.Fatalf("download body = %x, want %x", body, file)
	}

	// Re-uploading the same version replaces the hosted file.
	second := releaseZip(t, []byte("second build of the same version"))
	if res, data = uploadSettings(t, env, version, "", "", second); res.StatusCode != http.StatusOK {
		t.Fatalf("re-upload = %d: %s", res.StatusCode, data)
	}
	var replaced settingsResponse
	if err := json.Unmarshal(data, &replaced); err != nil {
		t.Fatalf("decode re-upload: %v", err)
	}
	if _, body := download(want.AppURL); !bytes.Equal(body, second) {
		t.Fatalf("re-uploaded download = %q, want %q", body, second)
	}
	secondSum := sha256.Sum256(second)
	if replaced.AppSHA256 != hex.EncodeToString(secondSum[:]) {
		t.Fatalf("re-uploaded sha256 = %q", replaced.AppSHA256)
	}

	// Names that try to leave DATA_DIR/updates/ are refused.
	for _, path := range []string{
		"/api/updates/..%2Fstate.json",
		"/api/updates/..%5Cstate.json",
		"/api/updates/missing.exe",
	} {
		res, data := env.Do("GET", path, nil)
		if res.StatusCode != http.StatusNotFound {
			t.Fatalf("GET %s = %d, want 404: %s", path, res.StatusCode, data)
		}
	}

	// Rejected uploads leave the release untouched.
	if res, data = uploadSettings(t, env, version, "", "", nil); res.StatusCode != http.StatusBadRequest || !strings.Contains(string(data), "请选择客户端 exe") {
		t.Fatalf("upload without file = %d: %s", res.StatusCode, data)
	}
	if res, data = uploadSettings(t, env, "2026/11/24", "", "", file); res.StatusCode != http.StatusBadRequest || !strings.Contains(string(data), "app_version") {
		t.Fatalf("upload with unsafe version = %d: %s", res.StatusCode, data)
	}
	if stored, err := env.App.Settings(); err != nil || stored.AppVersion != version || stored.AppSize != int64(len(second)) {
		t.Fatalf("settings changed after rejected uploads: %+v, %v", stored, err)
	}

	// Editing only the default template through JSON keeps the hosted release.
	revised := putSettings(t, env, map[string]string{"default_toml": `hostname = "only-template"`})
	if revised.AppSource != "upload" || revised.AppFileName != name || revised.AppHash != replaced.AppHash {
		t.Fatalf("template-only save = %+v, want the upload release kept", revised)
	}

	// Switching to an external URL clears the hosted-file fields and hashes
	// the remote package instead.
	externalClient := []byte("external build")
	externalPkg := releaseZip(t, externalClient)
	externalURL := fileServer(t, externalPkg)
	switched := putSettings(t, env, map[string]string{"app_version": version, "app_url": externalURL})
	externalSum := sha256.Sum256(externalClient)
	externalFull := hex.EncodeToString(externalSum[:])
	extPkgSum := sha256.Sum256(externalPkg)
	extPkgFull := hex.EncodeToString(extPkgSum[:])
	if switched.AppSource != "url" || switched.AppFileName != "" || switched.AppSize != 0 ||
		switched.AppHash != externalFull[:16] || switched.AppSHA256 != extPkgFull {
		t.Fatalf("switched settings = %+v", switched)
	}
}
