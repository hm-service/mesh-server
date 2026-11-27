package api_test

import (
	"archive/tar"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

	"mesh-server/app"
	"mesh-server/builtin"
	"mesh-server/core"
	"mesh-server/db"
	"mesh-server/internal/testenv"
	"mesh-server/signing"
)

const testTOML = "hostname = \"auto\"\n"

// The structs below mirror the wire contract instead of the server types, so a
// renamed JSON field fails here no matter how the internals are organised.
type deviceSummary struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Hostname       string `json:"hostname"`
	DeviceID       string `json:"deviceId"`
	Status         string `json:"status"`
	ConfigHash     string `json:"configHash"`
	LastConfigHash string `json:"lastConfigHash"`
	LastAppVersion string `json:"lastAppVersion"`
	ConfigState    string `json:"configState"`
	AppState       string `json:"appState"`
	AppVersion     string `json:"appVersion"`
	CreatedAt      int64  `json:"createdAt"`
	LastSeenAt     int64  `json:"lastSeenAt"`
	Identity       string `json:"identity"`
	IPv4           string `json:"ipv4"`
	DHCP           bool   `json:"dhcp"`
	// ConfigError 非空表示配置解析失败，此时 Identity 为空串。
	ConfigError string `json:"configError"`
	// AutoUpdate 是设备级覆盖：null 跟随发布包，true 自动，false 手动。
	AutoUpdate *bool `json:"autoUpdate"`
}

// credentialView mirrors the credential part of GET /api/admin/devices/{id}.
type credentialView struct {
	IssuedAt  int64 `json:"issuedAt"`
	ExpiresAt int64 `json:"expiresAt"`
}

type deviceDetail struct {
	ID             string          `json:"id"`
	Name           string          `json:"name"`
	DeviceID       string          `json:"deviceId"`
	Hostname       string          `json:"hostname"`
	Pubkey         string          `json:"pubkey"`
	ConfigTOML     string          `json:"configToml"`
	ConfigHash     string          `json:"configHash"`
	LastAppVersion string          `json:"lastAppVersion"`
	ConfigState    string          `json:"configState"`
	AppState       string          `json:"appState"`
	Status         string          `json:"status"`
	LastIP         string          `json:"lastIp"`
	CreatedAt      int64           `json:"createdAt"`
	LastSeenAt     int64           `json:"lastSeenAt"`
	Identity       string          `json:"identity"`
	IPv4           string          `json:"ipv4"`
	DHCP           bool            `json:"dhcp"`
	ConfigError    string          `json:"configError"`
	AutoUpdate     *bool           `json:"autoUpdate"`
	Credential     *credentialView `json:"credential"`
}

type createDeviceResponse struct {
	ID    string `json:"id"`
	Token string `json:"token"`
}

type configResponse struct {
	ConfigTOML string `json:"configToml"`
	ConfigHash string `json:"configHash"`
}

type updateInfo struct {
	Version    string `json:"version"`
	URL        string `json:"url"`
	Notes      string `json:"notes,omitempty"`
	SHA256     string `json:"sha256"`
	Signature  string `json:"signature"`
	AutoUpdate bool   `json:"autoUpdate"`
}

type launcherInfo struct {
	Version   string `json:"version"`
	URL       string `json:"url"`
	SHA256    string `json:"sha256"`
	Signature string `json:"signature"`
}

type heartbeatResponse struct {
	ConfigHash       string        `json:"configHash,omitempty"`
	ConfigB64        string        `json:"configB64,omitempty"`
	AppUpdate        *updateInfo   `json:"appUpdate,omitempty"`
	Launcher         *launcherInfo `json:"launcher,omitempty"`
	Credential       string        `json:"credential,omitempty"`
	CredentialID     string        `json:"credentialId,omitempty"`
	CredentialExpiry int64         `json:"credentialExpiry,omitempty"`
}

// testPlatform is the platform key the tests publish and heartbeat with.
const testPlatform = "windows/amd64"

// dummySignature has the shape of a release signature but no meaning: it lets a
// test reach the failure it is about (unreadable package, unreachable URL)
// instead of stopping at "the signature is malformed", which now fails first.
const dummySignature = "0000000000000000000000000000000000000000000000000000000000000000" +
	"0000000000000000000000000000000000000000000000000000000000000000"

// releaseView mirrors one entry of GET /api/admin/releases.
type releaseView struct {
	Platform          string `json:"platform"`
	AppVersion        string `json:"appVersion"`
	AppURL            string `json:"appUrl"`
	AppSHA256         string `json:"appSha256"`
	AppSignature      string `json:"appSignature"`
	AppNotes          string `json:"appNotes"`
	AutoUpdate        bool   `json:"autoUpdate"`
	LauncherVersion   string `json:"launcherVersion"`
	LauncherURL       string `json:"launcherUrl"`
	LauncherSHA256    string `json:"launcherSha256"`
	LauncherSignature string `json:"launcherSignature"`
}

// releasesResponse mirrors GET /api/admin/releases.
type releasesResponse struct {
	Releases []releaseView `json:"releases"`
}

// templateResponse mirrors GET /api/admin/template.
type templateResponse struct {
	Template string `json:"template"`
}

// releaseOf returns the entry of platform, or a zero view when absent.
func (r releasesResponse) releaseOf(platform string) releaseView {
	for _, v := range r.Releases {
		if v.Platform == platform {
			return v
		}
	}
	return releaseView{}
}

// signedRegisterBody builds POST /api/v1/register carrying a signature from the
// bound key: that is how an already bound slot proves it is the same device.
func signedRegisterBody(t *testing.T, deviceID string, priv ed25519.PrivateKey, hostname string, pubkey []byte, ts int64, nonce string) []byte {
	t.Helper()
	stamp := fmt.Sprintf("%d", ts)
	msg := core.SigningString("POST", "/api/v1/register", deviceID, stamp, nonce, testPlatform, "", "")
	body, err := json.Marshal(map[string]string{
		"deviceId":  deviceID,
		"hostname":  hostname,
		"pubkey":    base64.StdEncoding.EncodeToString(pubkey),
		"timestamp": stamp,
		"nonce":     nonce,
		"signature": base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte(msg))),
		"platform":  testPlatform,
	})
	if err != nil {
		t.Fatalf("marshal register: %v", err)
	}
	return body
}

// heartbeatBody builds the signed heartbeat payload for one device.
func heartbeatBody(t *testing.T, dev registeredDevice, ts int64, nonce, configHash, appVersion string) []byte {
	t.Helper()
	return signedHeartbeatBody(t, dev.deviceID, dev.priv, testPlatform, ts, nonce, configHash, appVersion)
}

// signedHeartbeatBody is heartbeatBody with every signed field spelled out, so
// the tampering tests can break exactly one of them.
func signedHeartbeatBody(t *testing.T, deviceID string, priv ed25519.PrivateKey, platform string, ts int64, nonce, configHash, appVersion string) []byte {
	t.Helper()
	stamp := fmt.Sprintf("%d", ts)
	msg := core.SigningString("POST", "/api/v1/heartbeat", deviceID, stamp, nonce, platform, configHash, appVersion)
	body, err := json.Marshal(map[string]string{
		"deviceId":   deviceID,
		"timestamp":  stamp,
		"nonce":      nonce,
		"signature":  base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte(msg))),
		"platform":   platform,
		"configHash": configHash,
		"appVersion": appVersion,
	})
	if err != nil {
		t.Fatalf("marshal heartbeat: %v", err)
	}
	return body
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

// signed sends one prepared request. The device signature travels in the body;
// headers only carries business headers.
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
	res, data := env.Do("POST", "/api/admin/devices", map[string]string{"name": name, "configToml": toml})
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
	res, data := env.Do("POST", "/api/v1/register", map[string]string{
		"token":    token,
		"deviceId": deviceID,
		"hostname": hostname,
		"pubkey":   base64.StdEncoding.EncodeToString(pub),
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

// tarZst packs entries into the tar.zst the launcher installs. The archive root
// is the content of bin/, exactly as build/release.ps1 writes it.
func tarZst(t *testing.T, entries map[string][]byte) []byte {
	t.Helper()
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	var buf bytes.Buffer
	zw, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatalf("zstd writer: %v", err)
	}
	tw := tar.NewWriter(zw)
	for _, name := range names {
		data := entries[name]
		hdr := &tar.Header{Name: name, Mode: 0o755, Size: int64(len(data)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("tar header %s: %v", name, err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatalf("tar write %s: %v", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zstd close: %v", err)
	}
	return buf.Bytes()
}

// releasePkg packs the client beside the kernel it needs and the version.json
// manifest that states the version the package was built as. The manifest is a
// complete windows/amd64 client manifest signed with the environment's release
// key, the way build/release.ps1 leaves it: every case that rejects a package
// changes one field of it, so the publish path is never exercised with a shape
// the release tool could not have produced.
func releasePkg(t *testing.T, env *testenv.Env, version string, client []byte) []byte {
	t.Helper()
	core := []byte("core")
	files := map[string]string{
		"mesh-client.exe":   digestOf(client),
		"easytier-core.exe": digestOf(core),
	}
	manifest, err := json.Marshal(map[string]any{
		"version":   version,
		"slot":      signing.SlotClient,
		"platform":  testPlatform,
		"files":     files,
		"signature": env.SignManifest(signing.SlotClient, testPlatform, version, files),
	})
	if err != nil {
		t.Fatalf("marshal version.json: %v", err)
	}
	return tarZst(t, map[string][]byte{
		"mesh-client.exe":   client,
		"easytier-core.exe": core,
		"version.json":      manifest,
	})
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
	res, err := anonymous.Get(env.Server.URL + "/api/admin/devices")
	if err != nil {
		t.Fatalf("anonymous GET /api/admin/devices: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous GET /api/admin/devices = %d, want 401", res.StatusCode)
	}
}

// TestCredentialExpires pins the stateless model: the issued credential carries
// its own expiry and the server keeps no table to look it up in.
func TestCredentialExpires(t *testing.T) {
	env := testenv.New(t)
	env.Clock.Advance(25 * time.Hour)

	res, _ := env.Do("GET", "/api/admin/devices", nil)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("list devices with an expired credential = %d, want 401", res.StatusCode)
	}
}

// TestTamperedCredentialIsRejected checks the signature before the expiry.
func TestTamperedCredentialIsRejected(t *testing.T) {
	env := testenv.New(t)
	good := env.Credential()
	// 最后一个 base64url 字符的低位是填充位：改它解码出来的签名可能一个字节都没变，
	// 所以先解开签名，翻一个真实比特再编码回去。
	dot := strings.IndexByte(good, '.')
	if dot < 0 {
		t.Fatalf("credential %q has no signature part", good)
	}
	signature, err := base64.RawURLEncoding.DecodeString(good[dot+1:])
	if err != nil || len(signature) == 0 {
		t.Fatalf("decode credential signature: %v", err)
	}
	signature[0] ^= 0x01
	tampered := good[:dot+1] + base64.RawURLEncoding.EncodeToString(signature)

	req, err := http.NewRequest("GET", env.Server.URL+"/api/admin/devices", nil)
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

	// The token is single use: another deviceId cannot register with it.
	if status, _ := register(t, env, token, "device-2", "OTHER", pub); status != http.StatusUnauthorized {
		t.Fatalf("reused token = %d, want 401", status)
	}

	// The same deviceId and pubkey are idempotent, but only with a valid
	// credential: the signature is made with the key the slot is bound to.
	body := signedRegisterBody(t, "device-1", priv, "DESKTOP-1", pub, env.Clock.Now().Unix(), newNonce(&nonces))
	res, data := env.Do("POST", "/api/v1/register", json.RawMessage(body))
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

	// The same deviceId with another pubkey conflicts, once the credential of
	// the bound key has been presented.
	otherPub, _ := newKeypair(t)
	status, _ = register(t, env, "", "device-1", "DESKTOP-1", otherPub)
	if status != http.StatusUnauthorized {
		t.Fatalf("unsigned conflict attempt = %d, want 401", status)
	}
	conflictBody := signedRegisterBody(t, "device-1", priv, "DESKTOP-1", otherPub, env.Clock.Now().Unix(), newNonce(&nonces))
	res, data = env.Do("POST", "/api/v1/register", json.RawMessage(conflictBody))
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

	// A correctly signed heartbeat passes.
	body := heartbeatBody(t, dev, now, newNonce(&nonces), dev.configHash, "")
	res, data := signed(t, env, "POST", "/api/v1/heartbeat", body, nil)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("valid heartbeat = %d, want 204: %s", res.StatusCode, data)
	}

	// Changing a signed field after signing breaks the signature.
	tampered := heartbeatBody(t, dev, now, newNonce(&nonces), dev.configHash, "")
	tampered = bytes.Replace(tampered, []byte(dev.configHash), []byte("tampered"), 1)
	res, _ = signed(t, env, "POST", "/api/v1/heartbeat", tampered, nil)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("tampered config hash = %d, want 401", res.StatusCode)
	}

	// The platform is part of what was signed.
	otherPlatform := heartbeatBody(t, dev, now, newNonce(&nonces), dev.configHash, "")
	otherPlatform = bytes.Replace(otherPlatform, []byte(testPlatform), []byte("linux/arm64"), 1)
	res, _ = signed(t, env, "POST", "/api/v1/heartbeat", otherPlatform, nil)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("tampered platform = %d, want 401", res.StatusCode)
	}

	// Stale and future timestamps are outside the 300s window.
	for _, ts := range []int64{now - 301, now + 301} {
		stale := heartbeatBody(t, dev, ts, newNonce(&nonces), dev.configHash, "")
		res, _ = signed(t, env, "POST", "/api/v1/heartbeat", stale, nil)
		if res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("timestamp %d = %d, want 401", ts, res.StatusCode)
		}
	}

	// An unknown device id cannot authenticate even with a valid key.
	unknown := signedHeartbeatBody(t, "dev-unknown", dev.priv, testPlatform, now, newNonce(&nonces), dev.configHash, "")
	res, _ = signed(t, env, "POST", "/api/v1/heartbeat", unknown, nil)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unknown device = %d, want 401", res.StatusCode)
	}
}

func TestNonceReplayIsRejected(t *testing.T) {
	env := testenv.New(t)
	var nonces int64
	dev := registerDevice(t, env, "laptop", testTOML)
	now := env.Clock.Now().Unix()
	body := heartbeatBody(t, dev, now, newNonce(&nonces), dev.configHash, "")

	res, data := signed(t, env, "POST", "/api/v1/heartbeat", body, nil)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("first request = %d, want 204: %s", res.StatusCode, data)
	}
	res, _ = signed(t, env, "POST", "/api/v1/heartbeat", body, nil)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("replayed nonce = %d, want 401", res.StatusCode)
	}

	// A fresh nonce for the same hashes is accepted.
	fresh := heartbeatBody(t, dev, now, newNonce(&nonces), dev.configHash, "")
	res, _ = signed(t, env, "POST", "/api/v1/heartbeat", fresh, nil)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("fresh nonce = %d, want 204", res.StatusCode)
	}
}

// TestDeviceFieldDomainIsRejected pins F3 at the door: a field outside the
// contract's domain gets the same 401 as a bad signature, even though the caller
// signed exactly what it sent. A newline would otherwise let the \n-joined
// signing text mean a different field list, and an unbounded field is an
// unbounded cache key or stored observation.
func TestDeviceFieldDomainIsRejected(t *testing.T) {
	env := testenv.New(t)
	var nonces int64
	dev := registerDevice(t, env, "laptop", testTOML)
	now := env.Clock.Now().Unix()

	cases := []struct {
		name       string
		nonce      string
		configHash string
		appVersion string
	}{
		{"nonce with a newline", "ab\nMESH2", dev.configHash, ""},
		{"nonce over the length limit", strings.Repeat("a", 65), dev.configHash, ""},
		{"config hash over the length limit", newNonce(&nonces), strings.Repeat("0", 65), ""},
		{"app version with a newline", newNonce(&nonces), dev.configHash, "1\n2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := signedHeartbeatBody(t, dev.deviceID, dev.priv, testPlatform, now, tc.nonce, tc.configHash, tc.appVersion)
			res, data := signed(t, env, "POST", "/api/v1/heartbeat", body, nil)
			if res.StatusCode != http.StatusUnauthorized {
				t.Fatalf("heartbeat = %d, want 401: %s", res.StatusCode, data)
			}
		})
	}

	// 值域检查没有误伤合法请求。
	body := heartbeatBody(t, dev, now, newNonce(&nonces), dev.configHash, "")
	if res, data := signed(t, env, "POST", "/api/v1/heartbeat", body, nil); res.StatusCode != http.StatusNoContent {
		t.Fatalf("valid heartbeat = %d, want 204: %s", res.StatusCode, data)
	}

	// 注册入口用同一份值域：带换行的 deviceId 在查库之前就被拒绝。
	pub, _ := newKeypair(t)
	res, data := env.Do("POST", "/api/v1/register", map[string]string{
		"token":    "",
		"deviceId": "dev\nMESH2",
		"hostname": "HOST",
		"pubkey":   base64.StdEncoding.EncodeToString(pub),
	})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("register with a newline in deviceId = %d, want 400: %s", res.StatusCode, data)
	}
}

// putReleases saves through the admin API and returns the response. Whatever
// the caller left unsigned is signed here, the way the operator's offline key
// would, so a test about slots does not have to know any digest.
func putReleases(t *testing.T, env *testenv.Env, fields map[string]any) releasesResponse {
	t.Helper()
	fillReleaseSignatures(t, env, fields)
	res, data := env.Do("PUT", "/api/admin/releases", fields)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("PUT releases = %d, want 200: %s", res.StatusCode, data)
	}
	var out releasesResponse
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("decode releases: %v", err)
	}
	return out
}

// fillReleaseSignatures signs every slot in fields that the caller left without
// a signature. It downloads exactly what the control plane will download, so
// the digest and the version in the signature are the ones being published.
func fillReleaseSignatures(t *testing.T, env *testenv.Env, fields map[string]any) {
	t.Helper()
	platform, _ := fields["platform"].(string)
	if platform == "" {
		return
	}
	if rawURL, ok := fields["appUrl"].(string); ok {
		if _, given := fields["appSignature"]; !given {
			pkg := fetchURL(t, rawURL)
			fields["appSignature"] = env.Sign(signing.SlotClient, platform,
				packageVersionOf(t, pkg), digestOf(pkg))
		}
	}
	if rawURL, ok := fields["launcherUrl"].(string); ok {
		if _, given := fields["launcherSignature"]; !given {
			version, _ := fields["launcherVersion"].(string)
			fields["launcherSignature"] = env.Sign(signing.SlotLauncher, platform,
				version, digestOf(fetchURL(t, rawURL)))
		}
	}
}

// digestOf is the full sha256 the control plane computes for a package.
func digestOf(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// packageVersionOf reads the version the package's manifest declares.
func packageVersionOf(t *testing.T, pkg []byte) string {
	t.Helper()
	zr, err := zstd.NewReader(bytes.NewReader(pkg))
	if err != nil {
		t.Fatalf("open package: %v", err)
	}
	defer zr.Close()
	tr := tar.NewReader(zr)
	for {
		hdr, err := tr.Next()
		if err != nil {
			t.Fatalf("find version.json: %v", err)
		}
		if hdr.Name != "version.json" {
			continue
		}
		var manifest struct {
			Version string `json:"version"`
		}
		if err := json.NewDecoder(tr).Decode(&manifest); err != nil {
			t.Fatalf("decode version.json: %v", err)
		}
		return manifest.Version
	}
}

// fetchURL reads what a release URL serves.
func fetchURL(t *testing.T, rawURL string) []byte {
	t.Helper()
	res, err := http.Get(rawURL)
	if err != nil {
		t.Fatalf("fetch %s: %v", rawURL, err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read %s: %v", rawURL, err)
	}
	return body
}

// publishExternal points the platform at a served package and returns its view.
// The published version is the one releasePkg wrote into the package.
func publishExternal(t *testing.T, env *testenv.Env, platform, notes string, pkg []byte) releaseView {
	t.Helper()
	return putReleases(t, env, map[string]any{
		"platform": platform,
		"appUrl":   fileServer(t, pkg),
		"appNotes": notes,
	}).releaseOf(platform)
}

// publishLauncher points the launcher slot of platform at a served file.
func publishLauncher(t *testing.T, env *testenv.Env, platform, version string, launcher []byte) releaseView {
	t.Helper()
	return putReleases(t, env, map[string]any{
		"platform":        platform,
		"launcherVersion": version,
		"launcherUrl":     fileServer(t, launcher),
	}).releaseOf(platform)
}

func heartbeat(t *testing.T, env *testenv.Env, dev registeredDevice, nonces *int64, configHash, appVersion string) (*http.Response, []byte) {
	t.Helper()
	body := heartbeatBody(t, dev, env.Clock.Now().Unix(), newNonce(nonces), configHash, appVersion)
	return signed(t, env, "POST", "/api/v1/heartbeat", body, nil)
}

func TestHeartbeatSemantics(t *testing.T) {
	env := testenv.New(t)
	var nonces int64
	dev := registerDevice(t, env, "laptop", testTOML)

	// Publish a build so appVersion has a value to compare against.
	build := releasePkg(t, env, "2026.11.24-4", []byte("client build one"))
	release := publishExternal(t, env, testPlatform, "修复窄屏布局", build)
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

	// Config hash and app version both match: 204 with no body at all.
	res, data := heartbeat(t, env, dev, &nonces, dev.configHash, release.AppVersion)
	if res.StatusCode != http.StatusNoContent || len(data) != 0 {
		t.Fatalf("heartbeat with current state = %d (%s), want 204 and no body", res.StatusCode, data)
	}

	// Only the app differs: appUpdate without configHash.
	res, data = heartbeat(t, env, dev, &nonces, dev.configHash, "2026.11.23-1")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("app-only heartbeat = %d, want 200: %s", res.StatusCode, data)
	}
	got := decode(data)
	wantUpdate := updateInfo{Version: "2026.11.24-4", URL: release.AppURL, Notes: "修复窄屏布局", SHA256: wantSHA, Signature: release.AppSignature}
	if got.ConfigHash != "" || got.AppUpdate == nil || *got.AppUpdate != wantUpdate {
		t.Fatalf("app-only heartbeat = %+v, want only appUpdate %+v", got, wantUpdate)
	}
	if strings.Contains(string(data), `"configHash"`) {
		t.Fatalf("app-only heartbeat body = %s, want no configHash", data)
	}

	// Only the config differs: configHash without update.
	res, data = heartbeat(t, env, dev, &nonces, "0000000000000000", release.AppVersion)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("config-only heartbeat = %d, want 200: %s", res.StatusCode, data)
	}
	got = decode(data)
	if got.AppUpdate != nil || got.ConfigHash != dev.configHash {
		t.Fatalf("config-only heartbeat = %+v, want configHash %q", got, dev.configHash)
	}
	// 整份 config.toml 随心跳一起回去，客户端不必再发一次请求。
	if raw, err := base64.StdEncoding.DecodeString(got.ConfigB64); err != nil || string(raw) != testTOML {
		t.Fatalf("config payload = %q (%v), want %q", got.ConfigB64, err, testTOML)
	}
	if strings.Contains(string(data), `"appUpdate"`) {
		t.Fatalf("config-only heartbeat body = %s, want no appUpdate", data)
	}

	// Both differ: both fields.
	res, data = heartbeat(t, env, dev, &nonces, "0000000000000000", "2026.11.23-1")
	got = decode(data)
	if res.StatusCode != http.StatusOK || got.ConfigHash != dev.configHash || got.AppUpdate == nil || *got.AppUpdate != wantUpdate {
		t.Fatalf("full heartbeat = %d %+v, want configHash and appUpdate", res.StatusCode, got)
	}

	// 改配置后，下一次心跳带回来的就是新的一份。
	const newTOML = `hostname = "laptop"`
	res, data = env.Do("PUT", "/api/admin/devices/"+dev.internalID, map[string]string{"name": "laptop", "configToml": newTOML})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("update device = %d, want 200: %s", res.StatusCode, data)
	}
	var updated map[string]string
	if err := json.Unmarshal(data, &updated); err != nil {
		t.Fatalf("decode update: %v", err)
	}
	newHash := core.Hash(newTOML)
	if updated["configHash"] != newHash || len(newHash) != 16 {
		t.Fatalf("update configHash = %q, want %q with 16 characters", updated["configHash"], newHash)
	}
	res, data = heartbeat(t, env, dev, &nonces, dev.configHash, release.AppVersion)
	got = decode(data)
	if res.StatusCode != http.StatusOK || got.ConfigHash != newHash {
		t.Fatalf("heartbeat after config update = %d %+v, want configHash %q", res.StatusCode, got, newHash)
	}
	if raw, err := base64.StdEncoding.DecodeString(got.ConfigB64); err != nil || string(raw) != newTOML {
		t.Fatalf("new config payload = %q (%v), want %q", got.ConfigB64, err, newTOML)
	}

	// With the new config and the published build the client is up to date.
	if res, data = heartbeat(t, env, dev, &nonces, newHash, release.AppVersion); res.StatusCode != http.StatusNoContent {
		t.Fatalf("heartbeat after config update = %d (%s), want 204", res.StatusCode, data)
	}

	// Publishing another build flips appVersion and the update comes back.
	next := publishExternal(t, env, testPlatform, "", releasePkg(t, env, "2026.11.25-1", []byte("client build two")))
	res, data = heartbeat(t, env, dev, &nonces, newHash, release.AppVersion)
	got = decode(data)
	if res.StatusCode != http.StatusOK || got.AppUpdate == nil || got.AppUpdate.URL != next.AppURL || got.ConfigHash != "" {
		t.Fatalf("heartbeat after release = %d %+v, want only appUpdate %q", res.StatusCode, got, next.AppURL)
	}
}

// TestHeartbeatAppUpdateIsAlwaysFull pins the shape of appUpdate: every
// heartbeat from a device reporting an older build carries the same complete
// offer, with url, sha256 and signature always present and no marker field for
// "the client already has this". Repeated heartbeats must not switch shapes.
func TestHeartbeatAppUpdateIsAlwaysFull(t *testing.T) {
	env := testenv.New(t)
	var nonces int64
	dev := registerDevice(t, env, "laptop", testTOML)
	pkg := releasePkg(t, env, "2026.11.26-18", []byte("client build one"))
	sum := sha256.Sum256(pkg)
	release := putReleases(t, env, map[string]any{
		"platform":   testPlatform,
		"appUrl":     fileServer(t, pkg),
		"appNotes":   "修复窄屏布局",
		"autoUpdate": true,
	}).releaseOf(testPlatform)
	wantSHA := hex.EncodeToString(sum[:])
	if release.AppSHA256 != wantSHA {
		t.Fatalf("published sha256 = %q, want %q", release.AppSHA256, wantSHA)
	}

	// decode returns the offer plus the raw field set, so a second shape or a
	// "present only sometimes" field is visible even when the struct matches.
	decode := func(data []byte) (heartbeatResponse, map[string]json.RawMessage) {
		t.Helper()
		var out heartbeatResponse
		if err := json.Unmarshal(data, &out); err != nil {
			t.Fatalf("decode heartbeat: %v", err)
		}
		var fields struct {
			AppUpdate map[string]json.RawMessage `json:"appUpdate"`
		}
		if err := json.Unmarshal(data, &fields); err != nil {
			t.Fatalf("decode heartbeat fields: %v", err)
		}
		return out, fields.AppUpdate
	}

	wantFull := updateInfo{Version: "2026.11.26-18", URL: release.AppURL, Notes: "修复窄屏布局", SHA256: wantSHA, Signature: release.AppSignature, AutoUpdate: true}
	// 两次心跳的输入完全相同（设备还报旧版本）：第三次只要形状变了就会露馅。
	for round := 0; round < 2; round++ {
		res, data := heartbeat(t, env, dev, &nonces, dev.configHash, "2026.11.25-1")
		if res.StatusCode != http.StatusOK {
			t.Fatalf("round %d heartbeat = %d, want 200: %s", round, res.StatusCode, data)
		}
		got, fields := decode(data)
		if got.AppUpdate == nil || *got.AppUpdate != wantFull {
			t.Fatalf("round %d heartbeat = %+v, want the full appUpdate %+v", round, got.AppUpdate, wantFull)
		}
		want := []string{"autoUpdate", "notes", "sha256", "signature", "url", "version"}
		var have []string
		for name := range fields {
			have = append(have, name)
		}
		sort.Strings(have)
		if strings.Join(have, ",") != strings.Join(want, ",") {
			t.Fatalf("round %d appUpdate fields = %v, want %v", round, have, want)
		}
	}

	// 设备级覆盖照样生效：autoUpdate 是这台设备的值，不是发布包上的值。
	putDevice(t, env, dev.internalID, map[string]any{"name": "laptop", "configToml": dev.configTOML, "autoUpdate": false})
	res, data := heartbeat(t, env, dev, &nonces, dev.configHash, "2026.11.25-1")
	got, fields := decode(data)
	wantOverride := wantFull
	wantOverride.AutoUpdate = false
	if res.StatusCode != http.StatusOK || got.AppUpdate == nil || *got.AppUpdate != wantOverride {
		t.Fatalf("heartbeat after a device override = %d %+v, want autoUpdate false", res.StatusCode, got.AppUpdate)
	}
	if len(fields) != 6 {
		t.Fatalf("appUpdate after a device override = %v, want the same six fields", fields)
	}
}

// TestReportedHashesShowInAdmin covers what the console uses to tell whether a
// device has already picked up the current config and build.
func TestReportedHashesShowInAdmin(t *testing.T) {
	env := testenv.New(t)
	var nonces int64
	dev := registerDevice(t, env, "laptop", testTOML)
	release := publishExternal(t, env, testPlatform, "", releasePkg(t, env, "2026.11.24-4", []byte("client build one")))

	summary := func() deviceSummary {
		t.Helper()
		res, data := env.Do("GET", "/api/admin/devices", nil)
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

	// Nothing reported yet: no config hash is recorded, and the platform is
	// unknown, so nothing is published for it either — contract/admin-api.md counts
	// that as current, not stale.
	if got := summary(); got.ConfigState != "stale" || got.AppState != "current" || got.LastConfigHash != "" || got.LastAppVersion != "" {
		t.Fatalf("before any heartbeat = %+v", got)
	}

	// A device running an old config and an old build.
	if res, data := heartbeat(t, env, dev, &nonces, "0000000000000000", "2026.11.23-9"); res.StatusCode >= http.StatusBadRequest {
		t.Fatalf("heartbeat = %d: %s", res.StatusCode, data)
	}
	got := summary()
	if got.LastConfigHash != "0000000000000000" || got.LastAppVersion != "2026.11.23-9" {
		t.Fatalf("recorded state = %+v", got)
	}
	if got.ConfigState != "stale" || got.AppState != "stale" || got.AppVersion != "2026.11.24-4" {
		t.Fatalf("stale states = %+v", got)
	}

	// After catching up both states flip without any further server change.
	if res, data := heartbeat(t, env, dev, &nonces, dev.configHash, release.AppVersion); res.StatusCode >= http.StatusBadRequest {
		t.Fatalf("heartbeat = %d: %s", res.StatusCode, data)
	}
	got = summary()
	if got.ConfigState != "current" || got.AppState != "current" {
		t.Fatalf("current states = %+v", got)
	}

	// The detail view reports the same states.
	res, data := env.Do("GET", "/api/admin/devices/"+dev.internalID, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("get device = %d: %s", res.StatusCode, data)
	}
	var detail deviceDetail
	if err := json.Unmarshal(data, &detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if detail.ConfigState != "current" || detail.AppState != "current" || detail.LastAppVersion != release.AppVersion {
		t.Fatalf("detail states = %+v", detail)
	}

	// Without a published build there is nothing to update to: still current.
	if res, data := env.Do("DELETE", "/api/admin/releases/"+testPlatform, nil); res.StatusCode != http.StatusOK {
		t.Fatalf("clear release = %d: %s", res.StatusCode, data)
	}
	if got := summary(); got.AppState != "current" || got.AppVersion != "" {
		t.Fatalf("no release = %+v", got)
	}
}

func TestDeletedDeviceGetsRevoked(t *testing.T) {
	env := testenv.New(t)
	var nonces int64
	dev := registerDevice(t, env, "laptop", testTOML)

	res, data := env.Do("DELETE", "/api/admin/devices/"+dev.internalID, nil)
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
		res, data := env.Do("GET", "/api/admin/devices", nil)
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
	res, data := env.Do("POST", "/api/admin/devices", map[string]string{"name": "overflow", "configToml": testTOML})
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

	res, data := env.Do("POST", "/api/admin/devices/"+dev.internalID+"/token", nil)
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

func TestReleaseRoundTrip(t *testing.T) {
	env := testenv.New(t)

	res, data := env.Do("GET", "/api/admin/releases", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET releases = %d, want 200: %s", res.StatusCode, data)
	}
	var empty releasesResponse
	if err := json.Unmarshal(data, &empty); err != nil {
		t.Fatalf("decode releases: %v", err)
	}
	if len(empty.Releases) != 0 {
		t.Fatalf("fresh releases = %+v, want empty", empty)
	}

	externalPkg := releasePkg(t, env, "2026.11.24-4", []byte("external mesh client binary"))
	rawURL := fileServer(t, externalPkg)
	saved := putReleases(t, env, map[string]any{
		"platform": testPlatform,
		"appUrl":   rawURL,
		"appNotes": "修复窄屏布局",
	})
	// The label is the one inside the package; appSha256 describes the bytes
	// clients download.
	got := saved.releaseOf(testPlatform)
	pkgSum := sha256.Sum256(externalPkg)
	wantPkg := hex.EncodeToString(pkgSum[:])
	if got.AppVersion != "2026.11.24-4" || got.AppURL != rawURL ||
		got.AppNotes != "修复窄屏布局" || got.AppSHA256 != wantPkg {
		t.Fatalf("save response = %+v, want sha256 %s", saved, wantPkg)
	}

	res, data = env.Do("GET", "/api/admin/releases", nil)
	var read releasesResponse
	if err := json.Unmarshal(data, &read); err != nil {
		t.Fatalf("decode releases: %v", err)
	}
	if read.releaseOf(testPlatform) != got {
		t.Fatalf("GET releases = %+v, want %+v", read, saved)
	}
	if stored, found, err := env.App.Release(testPlatform); err != nil || !found || stored.Client == nil ||
		stored.Client.Version != "2026.11.24-4" || stored.Client.URL != rawURL {
		t.Fatalf("stored release = %+v, found %v, %v", stored, found, err)
	}

	// Deleting clears only that platform.
	if res, data := env.Do("DELETE", "/api/admin/releases/"+testPlatform, nil); res.StatusCode != http.StatusOK {
		t.Fatalf("DELETE release = %d: %s", res.StatusCode, data)
	}
	if _, found, err := env.App.Release(testPlatform); err != nil || found {
		t.Fatalf("release survived delete: found %v, %v", found, err)
	}
}

// TestReleasePayloadKeepsOneShape pins the admin payload field by field: every
// platform entry carries the same eleven keys whether both slots are published
// or only one, so the console reads one shape and an unpublished slot is an
// empty value rather than an absent field.
func TestReleasePayloadKeepsOneShape(t *testing.T) {
	env := testenv.New(t)
	want := []string{
		"appNotes", "appSha256", "appSignature", "appUrl", "appVersion", "autoUpdate",
		"launcherSha256", "launcherSignature", "launcherUrl", "launcherVersion", "platform",
	}
	keysOf := func(t *testing.T) string {
		t.Helper()
		res, data := env.Do("GET", "/api/admin/releases", nil)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("GET releases = %d: %s", res.StatusCode, data)
		}
		var body struct {
			Releases []map[string]json.RawMessage `json:"releases"`
		}
		if err := json.Unmarshal(data, &body); err != nil {
			t.Fatalf("decode releases: %v", err)
		}
		if len(body.Releases) != 1 {
			t.Fatalf("releases = %d entries, want 1: %s", len(body.Releases), data)
		}
		keys := make([]string, 0, len(body.Releases[0]))
		for key := range body.Releases[0] {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		return strings.Join(keys, ",")
	}

	// Only the client package: the launcher keys are present and empty.
	publishExternal(t, env, testPlatform, "note", releasePkg(t, env, "2026.11.28-1", []byte("client build")))
	if got := keysOf(t); got != strings.Join(want, ",") {
		t.Fatalf("client-only payload keys = %s, want %s", got, strings.Join(want, ","))
	}

	// Both slots: the key set does not change when the second one appears.
	publishLauncher(t, env, testPlatform, "2026.11.28-1", []byte("launcher"))
	if got := keysOf(t); got != strings.Join(want, ",") {
		t.Fatalf("two-slot payload keys = %s, want %s", got, strings.Join(want, ","))
	}
}

// TestAutoUpdateFollowsThePackage pins the package-level switch: it is stored
// with the client package, travels to clients inside appUpdate, is rewritten by
// every client-package publish (omitted means manual), and cannot be set on a
// platform that has no package to attach it to.
func TestAutoUpdateFollowsThePackage(t *testing.T) {
	env := testenv.New(t)
	var nonces int64
	dev := registerDevice(t, env, "laptop", testTOML)

	build := releasePkg(t, env, "2026.11.26-1", []byte("client build one"))
	saved := putReleases(t, env, map[string]any{
		"platform":   testPlatform,
		"appUrl":     fileServer(t, build),
		"autoUpdate": true,
	}).releaseOf(testPlatform)
	if saved.AppVersion != "2026.11.26-1" {
		t.Fatalf("published version = %q, want the one in version.json", saved.AppVersion)
	}
	if !saved.AutoUpdate {
		t.Fatalf("published release = %+v, want autoUpdate", saved)
	}
	// The switch is data, not a request-time decoration.
	if stored, found, err := env.App.Release(testPlatform); err != nil || !found || stored.Client == nil || !stored.Client.AutoUpdate {
		t.Fatalf("stored release = %+v, found %v, %v; want autoUpdate", stored, found, err)
	}

	// The client learns the policy together with the update itself.
	res, data := heartbeat(t, env, dev, &nonces, dev.configHash, "2026.11.25-1")
	var got heartbeatResponse
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode heartbeat: %v", err)
	}
	if res.StatusCode != http.StatusOK || got.AppUpdate == nil || !got.AppUpdate.AutoUpdate {
		t.Fatalf("heartbeat = %d %+v, want appUpdate with autoUpdate", res.StatusCode, got)
	}

	// No appUpdate, no autoUpdate: the field does not ride on other responses.
	res, data = heartbeat(t, env, dev, &nonces, "0000000000000000", "2026.11.26-1")
	var onlyConfig heartbeatResponse
	if err := json.Unmarshal(data, &onlyConfig); err != nil {
		t.Fatalf("decode heartbeat: %v", err)
	}
	if res.StatusCode != http.StatusOK || onlyConfig.AppUpdate != nil || strings.Contains(string(data), "autoUpdate") {
		t.Fatalf("config-only heartbeat = %d %s, want no autoUpdate", res.StatusCode, data)
	}

	// Publishing the launcher leaves the client package's switch alone.
	launcher := publishLauncher(t, env, testPlatform, "2026.11.26-9", []byte("launcher binary"))
	if !launcher.AutoUpdate || launcher.AppVersion != "2026.11.26-1" {
		t.Fatalf("launcher publish = %+v, want the client slot kept", launcher)
	}

	// A new package starts manual unless the admin says otherwise.
	next := putReleases(t, env, map[string]any{
		"platform": testPlatform,
		"appUrl":   fileServer(t, releasePkg(t, env, "2026.11.26-2", []byte("client build two"))),
	}).releaseOf(testPlatform)
	if next.AutoUpdate || next.AppVersion != "2026.11.26-2" {
		t.Fatalf("republished package = %+v, want it back to manual", next)
	}

	// The switch cannot be flipped on its own: it has no package to belong to.
	res, data = env.Do("PUT", "/api/admin/releases", map[string]any{"platform": testPlatform, "autoUpdate": true})
	if res.StatusCode != http.StatusBadRequest || !strings.Contains(string(data), "invalid_argument") {
		t.Fatalf("lone autoUpdate = %d: %s, want 400 invalid_argument", res.StatusCode, data)
	}
	if stored, _, err := env.App.Release(testPlatform); err != nil || stored.Client == nil || stored.Client.AutoUpdate {
		t.Fatalf("rejected autoUpdate changed the row: %+v, %v", stored, err)
	}
}

// putDevice is one PUT /api/admin/devices/{id} that must be accepted.
func putDevice(t *testing.T, env *testenv.Env, id string, body map[string]any) {
	t.Helper()
	res, data := env.Do("PUT", "/api/admin/devices/"+id, body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("PUT device = %d: %s, want 200", res.StatusCode, data)
	}
}

// deviceAutoUpdate sends one heartbeat from an older build and returns the
// autoUpdate the device is told to use.
func deviceAutoUpdate(t *testing.T, env *testenv.Env, dev registeredDevice, nonces *int64) *bool {
	t.Helper()
	res, data := heartbeat(t, env, dev, nonces, dev.configHash, "2026.11.25-1")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("heartbeat = %d: %s, want 200", res.StatusCode, data)
	}
	var got heartbeatResponse
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode heartbeat: %v", err)
	}
	if got.AppUpdate == nil {
		t.Fatalf("heartbeat carried no appUpdate: %s", data)
	}
	return &got.AppUpdate.AutoUpdate
}

// TestDeviceAutoUpdateOverride pins the three-state device override: it wins over
// the package in both directions, a request without the field leaves it alone, and
// null puts the device back to following the package.
func TestDeviceAutoUpdateOverride(t *testing.T) {
	env := testenv.New(t)
	var nonces int64
	dev := registerDevice(t, env, "laptop", testTOML)
	putReleases(t, env, map[string]any{
		"platform":   testPlatform,
		"appUrl":     fileServer(t, releasePkg(t, env, "2026.11.26-1", []byte("client build one"))),
		"autoUpdate": true,
	})

	// 默认跟随：下发包上的 true。
	if got := deviceAutoUpdate(t, env, dev, &nonces); got == nil || !*got {
		t.Fatalf("following the package = %v, want true", got)
	}

	// 强制手动盖住包上的 true。
	putDevice(t, env, dev.internalID, map[string]any{"name": "laptop", "configToml": dev.configTOML, "autoUpdate": false})
	if got := deviceAutoUpdate(t, env, dev, &nonces); got == nil || *got {
		t.Fatalf("forced manual = %v, want false even though the package says automatic", got)
	}
	if row := deviceDetailOf(t, env, dev.internalID); row.AutoUpdate == nil || *row.AutoUpdate {
		t.Fatalf("detail autoUpdate = %v, want false", row.AutoUpdate)
	}
	if got := listAutoUpdate(t, env, dev.internalID); got == nil || *got {
		t.Fatalf("list autoUpdate = %v, want false", got)
	}

	// 请求里没有这个字段：这一列保持原样。
	putDevice(t, env, dev.internalID, map[string]any{"name": "laptop", "configToml": dev.configTOML})
	if row := deviceDetailOf(t, env, dev.internalID); row.AutoUpdate == nil || *row.AutoUpdate {
		t.Fatalf("update without the field = %v, want the override kept", row.AutoUpdate)
	}

	// null：恢复跟随发布包。
	putDevice(t, env, dev.internalID, map[string]any{"name": "laptop", "configToml": dev.configTOML, "autoUpdate": nil})
	if row := deviceDetailOf(t, env, dev.internalID); row.AutoUpdate != nil {
		t.Fatalf("null override = %v, want follow (null)", row.AutoUpdate)
	}
	if got := deviceAutoUpdate(t, env, dev, &nonces); got == nil || !*got {
		t.Fatalf("back to following = %v, want the package's true", got)
	}

	// 包改成手动之后，强制自动仍然生效：覆盖与包的方向无关。
	putReleases(t, env, map[string]any{
		"platform": testPlatform,
		"appUrl":   fileServer(t, releasePkg(t, env, "2026.11.26-2", []byte("client build two"))),
	})
	if got := deviceAutoUpdate(t, env, dev, &nonces); got == nil || *got {
		t.Fatalf("manual package with no override = %v, want false", got)
	}
	putDevice(t, env, dev.internalID, map[string]any{"name": "laptop", "configToml": dev.configTOML, "autoUpdate": true})
	if got := deviceAutoUpdate(t, env, dev, &nonces); got == nil || !*got {
		t.Fatalf("forced automatic over a manual package = %v, want true", got)
	}
}

// deviceDetailOf reads GET /api/admin/devices/{id}.
func deviceDetailOf(t *testing.T, env *testenv.Env, id string) deviceDetail {
	t.Helper()
	res, data := env.Do("GET", "/api/admin/devices/"+id, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("get device = %d: %s", res.StatusCode, data)
	}
	var device deviceDetail
	if err := json.Unmarshal(data, &device); err != nil {
		t.Fatalf("decode device: %v", err)
	}
	return device
}

// listAutoUpdate reads the autoUpdate of one row of GET /api/admin/devices.
func listAutoUpdate(t *testing.T, env *testenv.Env, id string) *bool {
	t.Helper()
	res, data := env.Do("GET", "/api/admin/devices", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list devices = %d: %s", res.StatusCode, data)
	}
	var rows []deviceSummary
	if err := json.Unmarshal(data, &rows); err != nil {
		t.Fatalf("decode devices: %v", err)
	}
	for _, row := range rows {
		if row.ID == id {
			return row.AutoUpdate
		}
	}
	t.Fatalf("device %s missing from the list", id)
	return nil
}

// TestReleaseSlotsAreIndependent covers the two artifacts one platform
// publishes: the launcher is a single file in the install root, the client
// package a tar.zst unpacked into bin/. Each slot is published and cleared on
// its own and neither touches the other.
func TestReleaseSlotsAreIndependent(t *testing.T) {
	env := testenv.New(t)

	client := publishExternal(t, env, testPlatform, "客户端更新", releasePkg(t, env, "2026.11.25-36", []byte("client build")))
	if client.LauncherVersion != "" || client.LauncherURL != "" || client.LauncherSHA256 != "" {
		t.Fatalf("publishing the client package also filled the launcher slot: %+v", client)
	}

	launcherBytes := []byte("launcher binary")
	launcherSum := sha256.Sum256(launcherBytes)
	launcherSHA := hex.EncodeToString(launcherSum[:])
	launcher := publishLauncher(t, env, testPlatform, "2026.11.25-36", launcherBytes)
	if launcher.LauncherVersion != "2026.11.25-36" || launcher.LauncherURL == "" || launcher.LauncherSHA256 != launcherSHA {
		t.Fatalf("launcher slot = %+v, want the served url and sha256 %s", launcher, launcherSHA)
	}
	// 启动器只有完整摘要，没有可比较的版本号。
	if len(launcher.LauncherSHA256) != 64 {
		t.Fatalf("launcherSha256 = %q, want 64 characters", launcher.LauncherSHA256)
	}
	// The client slot survived the launcher publish untouched.
	if launcher.AppVersion != client.AppVersion || launcher.AppURL != client.AppURL ||
		launcher.AppSHA256 != client.AppSHA256 || launcher.AppNotes != client.AppNotes {
		t.Fatalf("launcher publish changed the client slot: %+v, want %+v", launcher, client)
	}

	// A new client package leaves the launcher slot in place.
	next := publishExternal(t, env, testPlatform, "", releasePkg(t, env, "2026.11.26-1", []byte("client build two")))
	if next.AppVersion != "2026.11.26-1" || next.LauncherVersion != launcher.LauncherVersion ||
		next.LauncherURL != launcher.LauncherURL || next.LauncherSHA256 != launcher.LauncherSHA256 {
		t.Fatalf("client publish changed the launcher slot: %+v", next)
	}

	// Clearing only the launcher keeps the client package published.
	res, data := env.Do("DELETE", "/api/admin/releases/"+testPlatform+"?slot=launcher", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("clear launcher = %d, want 200: %s", res.StatusCode, data)
	}
	var afterClear releasesResponse
	if err := json.Unmarshal(data, &afterClear); err != nil {
		t.Fatalf("decode releases: %v", err)
	}
	cleared := afterClear.releaseOf(testPlatform)
	if cleared.AppVersion != next.AppVersion || cleared.AppURL != next.AppURL {
		t.Fatalf("clearing the launcher dropped the client slot: %+v", cleared)
	}
	if cleared.LauncherVersion != "" || cleared.LauncherURL != "" || cleared.LauncherSHA256 != "" {
		t.Fatalf("launcher slot survived ?slot=launcher: %+v", cleared)
	}
}

// TestReleaseSlotsKeepOnlyPublishedRows pins the shape of the admin payload: a
// platform is listed only while at least one of its slot rows exists, and the
// plain delete removes both rows at once.
func TestReleaseSlotsKeepOnlyPublishedRows(t *testing.T) {
	env := testenv.New(t)

	// A platform carrying only a launcher has no client slot.
	onlyLauncher := publishLauncher(t, env, "linux/amd64", "2026.11.25-36", []byte("launcher"))
	if onlyLauncher.AppVersion != "" {
		t.Fatalf("launcher-only platform = %+v, want an empty client slot", onlyLauncher)
	}
	if res, data := env.Do("DELETE", "/api/admin/releases/linux/amd64?slot=launcher", nil); res.StatusCode != http.StatusOK {
		t.Fatalf("clear launcher = %d: %s", res.StatusCode, data)
	}
	if _, found, err := env.App.Release("linux/amd64"); err != nil || found {
		t.Fatalf("platform with no slots left = found %v, %v; want it gone", found, err)
	}

	// The plain delete takes both slots of the platform.
	publishExternal(t, env, testPlatform, "", releasePkg(t, env, "2026.11.25-36", []byte("client build")))
	publishLauncher(t, env, testPlatform, "2026.11.25-36", []byte("launcher"))
	res, data := env.Do("DELETE", "/api/admin/releases/"+testPlatform, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("DELETE release = %d, want 200: %s", res.StatusCode, data)
	}
	var after releasesResponse
	if err := json.Unmarshal(data, &after); err != nil {
		t.Fatalf("decode releases: %v", err)
	}
	if got := after.releaseOf(testPlatform); got != (releaseView{}) {
		t.Fatalf("both slots should be gone: %+v", got)
	}
	if _, found, err := env.App.Release(testPlatform); err != nil || found {
		t.Fatalf("release row survived the delete: found %v, %v", found, err)
	}
}

// TestPublishRequiresASignatureForTheBytesItDownloaded pins the three ways a
// release signature can fail: nothing signed, a signature over another digest,
// and a signature made for the other slot. All of them must be refused before
// the row is written, so no device is ever offered a package it will reject.
func TestPublishRequiresASignatureForTheBytesItDownloaded(t *testing.T) {
	env := testenv.New(t)
	file := releasePkg(t, env, "2026.11.27-1", []byte("client build"))
	rawURL := fileServer(t, file)
	zero := strings.Repeat("0", 64)

	cases := []struct {
		name   string
		fields map[string]string
		want   string
	}{
		{
			name:   "unsigned",
			fields: map[string]string{"platform": testPlatform, "appUrl": rawURL},
			want:   "缺少发布签名",
		},
		{
			name: "signature over another digest",
			fields: map[string]string{
				"platform":     testPlatform,
				"appUrl":       rawURL,
				"appSignature": env.Sign(signing.SlotClient, testPlatform, "2026.11.27-1", zero),
			},
			want: "不符",
		},
		{
			name: "signature for the launcher slot",
			fields: map[string]string{
				"platform":     testPlatform,
				"appUrl":       rawURL,
				"appSignature": env.Sign(signing.SlotLauncher, testPlatform, "2026.11.27-1", digestOf(file)),
			},
			want: "不符",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, body := env.Do("PUT", "/api/admin/releases", tc.fields)
			if res.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), tc.want) {
				t.Fatalf("publish = %d: %s, want 400 containing %q", res.StatusCode, body, tc.want)
			}
		})
	}
	if _, found, err := env.App.Release(testPlatform); err != nil || found {
		t.Fatalf("a rejected publish stored a slot: found %v, %v", found, err)
	}
}

// TestPublishRequiresTheManifestSignature pins the inner signature check. The
// launcher refuses to install a package whose version.json is unsigned or signed
// for another file set, so the publish refuses it too instead of pushing a
// package every device will reject.
func TestPublishRequiresTheManifestSignature(t *testing.T) {
	env := testenv.New(t)
	client := []byte("client build")
	kernel := []byte("core")
	files := map[string]string{
		"mesh-client.exe":   digestOf(client),
		"easytier-core.exe": digestOf(kernel),
	}
	packageOf := func(version, signedVersion, signature string) []byte {
		if signature == "" {
			signature = env.SignManifest(signing.SlotClient, testPlatform, signedVersion, files)
		}
		manifest, err := json.Marshal(map[string]any{
			"version":   version,
			"slot":      signing.SlotClient,
			"platform":  testPlatform,
			"files":     files,
			"signature": signature,
		})
		if err != nil {
			t.Fatalf("marshal version.json: %v", err)
		}
		return tarZst(t, map[string][]byte{
			"mesh-client.exe":   client,
			"easytier-core.exe": kernel,
			"version.json":      manifest,
		})
	}
	unsigned, err := json.Marshal(map[string]any{
		"version":  "2026.11.28-1",
		"slot":     signing.SlotClient,
		"platform": testPlatform,
		"files":    files,
	})
	if err != nil {
		t.Fatalf("marshal version.json: %v", err)
	}
	noSignature := tarZst(t, map[string][]byte{
		"mesh-client.exe":   client,
		"easytier-core.exe": kernel,
		"version.json":      unsigned,
	})

	cases := []struct {
		name string
		pkg  []byte
	}{
		{"no signature", noSignature},
		{"signature over another version", packageOf("2026.11.28-1", "2026.11.28-0", "")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// 外层包签名是有效的：这一轮要拦的是内层清单签名。
			res, data := env.Do("PUT", "/api/admin/releases", map[string]string{
				"platform":     testPlatform,
				"appUrl":       fileServer(t, tc.pkg),
				"appSignature": env.Sign(signing.SlotClient, testPlatform, "2026.11.28-1", digestOf(tc.pkg)),
			})
			if res.StatusCode != http.StatusBadRequest || !strings.Contains(string(data), "缺少有效的发布签名") {
				t.Fatalf("publish = %d: %s, want 400 naming the manifest signature", res.StatusCode, data)
			}
		})
	}
	if _, found, err := env.App.Release(testPlatform); err != nil || found {
		t.Fatalf("a rejected publish stored a slot: found %v, %v", found, err)
	}

	// 正确签名的那一份照样发布成功。
	good := releasePkg(t, env, "2026.11.28-2", client)
	if got := publishExternal(t, env, testPlatform, "", good); got.AppVersion != "2026.11.28-2" {
		t.Fatalf("publish = %+v, want the version from the signed manifest", got)
	}
}

// TestUnsignedReleaseIsNotOffered pins the upgrade path: a row published before
// signatures existed keeps an empty signature, and every client would reject it
// every 30 seconds if it were sent. Such a slot is treated as unpublished until
// the admin publishes it again.
func TestUnsignedReleaseIsNotOffered(t *testing.T) {
	env := testenv.New(t)
	var nonces int64
	dev := registerDevice(t, env, "laptop", testTOML)
	seedUnsignedRelease(t, env, testPlatform)

	res, data := heartbeat(t, env, dev, &nonces, dev.configHash, "2026.11.25-1")
	if res.StatusCode != http.StatusNoContent || len(data) != 0 {
		t.Fatalf("heartbeat = %d (%s), want 204: an unsigned slot must look unpublished", res.StatusCode, data)
	}
}

// TestMalformedSignatureFailsBeforeDownloading pins the order: a signature that
// cannot possibly be valid is rejected without fetching the package first.
func TestMalformedSignatureFailsBeforeDownloading(t *testing.T) {
	env := testenv.New(t)
	res, data := env.Do("PUT", "/api/admin/releases", map[string]string{
		"platform":     testPlatform,
		"appUrl":       "http://127.0.0.1:1/mesh-client.tar.zst",
		"appSignature": "00",
	})
	if res.StatusCode != http.StatusBadRequest || !strings.Contains(string(data), "签名") {
		t.Fatalf("publish = %d: %s, want 400 naming the signature", res.StatusCode, data)
	}
	if strings.Contains(string(data), "无法读取") {
		t.Fatalf("publish = %s: the package was downloaded before the signature was checked", data)
	}
}

// seedUnsignedRelease writes a release row the way the schema migration leaves
// one: version and digest present, signature empty. The admin API cannot produce
// it (it demands a signature), so the test writes straight to the database.
func seedUnsignedRelease(t *testing.T, env *testenv.Env, platform string) {
	t.Helper()
	handle, err := db.Open(filepath.Join(env.DataDir, "mesh.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer handle.Close()
	client := &db.ClientSlot{
		Version: "2026.11.27-1",
		URL:     "http://127.0.0.1:1/mesh-client.tar.zst",
		SHA256:  strings.Repeat("a", 64),
	}
	launcher := &db.LauncherSlot{
		Version: "2026.11.27-1",
		URL:     "http://127.0.0.1:1/mesh.exe",
		SHA256:  strings.Repeat("b", 64),
	}
	if err := handle.PublishSlots(platform, client, launcher); err != nil {
		t.Fatalf("seed unsigned release: %v", err)
	}
}

func TestLauncherRejectsIncompletePublish(t *testing.T) {
	env := testenv.New(t)

	cases := []struct {
		name   string
		fields map[string]string
		want   string
	}{
		{
			name:   "version without url",
			fields: map[string]string{"platform": testPlatform, "launcherVersion": "2026.11.25-36"},
			want:   "同时给出",
		},
		{
			name:   "url without version",
			fields: map[string]string{"platform": testPlatform, "launcherUrl": "http://example.com/mesh.exe"},
			want:   "同时给出",
		},
		{
			name:   "non-http url",
			fields: map[string]string{"platform": testPlatform, "launcherVersion": "2026.11.25-36", "launcherUrl": "ftp://example.com/mesh.exe", "launcherSignature": dummySignature},
			want:   "http(s)",
		},
		{
			name:   "unreachable url",
			fields: map[string]string{"platform": testPlatform, "launcherVersion": "2026.11.25-36", "launcherUrl": "http://127.0.0.1:1/mesh.exe", "launcherSignature": dummySignature},
			want:   "无法读取 launcherUrl",
		},
		{
			name:   "empty signature",
			fields: map[string]string{"platform": testPlatform, "launcherVersion": "2026.11.25-36", "launcherUrl": fileServer(t, []byte("launcher one")), "launcherSignature": ""},
			want:   "缺少发布签名",
		},
		{
			name:   "missing platform",
			fields: map[string]string{"launcherVersion": "2026.11.25-36", "launcherUrl": "http://example.com/mesh.exe", "launcherSignature": dummySignature},
			want:   "platform",
		},
		{
			name:   "version outside the domain",
			fields: map[string]string{"platform": testPlatform, "launcherVersion": "2026.11.26-013", "launcherUrl": "http://example.com/mesh.exe", "launcherSignature": dummySignature},
			want:   "YYYY.MM.DD-N",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, data := env.Do("PUT", "/api/admin/releases", tc.fields)
			if res.StatusCode != http.StatusBadRequest || !strings.Contains(string(data), tc.want) {
				t.Fatalf("publish = %d: %s, want 400 containing %q", res.StatusCode, data, tc.want)
			}
		})
	}
	if _, found, err := env.App.Release(testPlatform); err != nil || found {
		t.Fatalf("rejected launcher publish was stored: found %v, %v", found, err)
	}

	// slot only names the launcher slot.
	res, data := env.Do("DELETE", "/api/admin/releases/"+testPlatform+"?slot=app", nil)
	if res.StatusCode != http.StatusBadRequest || !strings.Contains(string(data), "slot 只能是 launcher") {
		t.Fatalf("unknown slot = %d: %s", res.StatusCode, data)
	}
}

// TestHeartbeatCarriesLauncher pins what the client consumes: the launcher of
// the platform travels with every heartbeat that has a body, no matter which
// hash changed, because the server never learns the local launcher digest.
func TestHeartbeatCarriesLauncher(t *testing.T) {
	env := testenv.New(t)
	var nonces int64
	dev := registerDevice(t, env, "laptop", testTOML)
	release := publishExternal(t, env, testPlatform, "", releasePkg(t, env, "2026.11.25-36", []byte("client build")))
	launcher := publishLauncher(t, env, testPlatform, "2026.11.25-36", []byte("launcher one"))
	wantLauncher := launcherInfo{Version: "2026.11.25-36", URL: launcher.LauncherURL, SHA256: launcher.LauncherSHA256, Signature: launcher.LauncherSignature}

	decode := func(data []byte) heartbeatResponse {
		t.Helper()
		var out heartbeatResponse
		if err := json.Unmarshal(data, &out); err != nil {
			t.Fatalf("decode heartbeat: %v", err)
		}
		return out
	}

	// Only the config changed: no appUpdate, but the launcher still arrives.
	res, data := heartbeat(t, env, dev, &nonces, "0000000000000000", release.AppVersion)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("config-only heartbeat = %d, want 200: %s", res.StatusCode, data)
	}
	got := decode(data)
	if got.AppUpdate != nil {
		t.Fatalf("config-only heartbeat = %+v, want no appUpdate", got)
	}
	if got.Launcher == nil || *got.Launcher != wantLauncher {
		t.Fatalf("launcher = %+v, want %+v", got.Launcher, wantLauncher)
	}
	if !strings.Contains(string(data), `"launcher"`) {
		t.Fatalf("heartbeat body = %s, want a launcher field", data)
	}

	// A client package update carries both fields.
	res, data = heartbeat(t, env, dev, &nonces, dev.configHash, "2026.11.20-1")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("app-only heartbeat = %d, want 200: %s", res.StatusCode, data)
	}
	got = decode(data)
	if got.AppUpdate == nil || got.Launcher == nil || got.Launcher.SHA256 != wantLauncher.SHA256 {
		t.Fatalf("app-only heartbeat = %+v, want appUpdate and launcher", got)
	}

	// Nothing changed, but a launcher is published: 200 carrying only the
	// launcher, because the server cannot tell whether the client already has
	// that build. The client compares the digest itself.
	res, data = heartbeat(t, env, dev, &nonces, dev.configHash, release.AppVersion)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("current heartbeat = %d (%s), want 200", res.StatusCode, data)
	}
	if got = decode(data); got.ConfigHash != "" || got.AppUpdate != nil || got.Launcher == nil {
		t.Fatalf("current heartbeat = %+v, want only the launcher", got)
	}

	// A platform without a launcher omits the field instead of sending empties.
	if res, data := env.Do("DELETE", "/api/admin/releases/"+testPlatform+"?slot=launcher", nil); res.StatusCode != http.StatusOK {
		t.Fatalf("clear launcher = %d: %s", res.StatusCode, data)
	}
	res, data = heartbeat(t, env, dev, &nonces, dev.configHash, "2026.11.20-1")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("heartbeat after clearing the launcher = %d, want 200: %s", res.StatusCode, data)
	}
	if strings.Contains(string(data), `"launcher"`) {
		t.Fatalf("heartbeat body = %s, want no launcher field", data)
	}
	if got = decode(data); got.Launcher != nil {
		t.Fatalf("launcher = %+v, want nil", got.Launcher)
	}
}

func TestReleaseRejectsIncompletePublish(t *testing.T) {
	env := testenv.New(t)

	// appVersion is not an input any more: the version is read from the package,
	// and an undeclared field is rejected by the one decoding rule.
	res, data := env.Do("PUT", "/api/admin/releases", map[string]string{"platform": testPlatform, "appVersion": "2026.11.24-4"})
	if res.StatusCode != http.StatusBadRequest || !strings.Contains(string(data), "invalid_request") {
		t.Fatalf("appVersion as input = %d: %s, want 400 invalid_request", res.StatusCode, data)
	}
	if _, found, err := env.App.Release(testPlatform); err != nil || found {
		t.Fatalf("rejected publish was stored: found %v, %v", found, err)
	}

	// Notes and the auto-install switch are properties of a client package;
	// neither can be published on its own.
	for name, body := range map[string]map[string]any{
		"appNotes":   {"platform": testPlatform, "appNotes": "只写备注"},
		"autoUpdate": {"platform": testPlatform, "autoUpdate": true},
	} {
		res, data := env.Do("PUT", "/api/admin/releases", body)
		if res.StatusCode != http.StatusBadRequest || !strings.Contains(string(data), "appUrl") {
			t.Fatalf("%s without appUrl = %d: %s", name, res.StatusCode, data)
		}
	}

	res, data = env.Do("PUT", "/api/admin/releases", map[string]string{"platform": testPlatform, "appUrl": "ftp://example.com/a.tar.zst"})
	if res.StatusCode != http.StatusBadRequest || !strings.Contains(string(data), "http(s)") {
		t.Fatalf("non-http url = %d: %s", res.StatusCode, data)
	}

	// A URL the server cannot read is rejected.
	res, data = env.Do("PUT", "/api/admin/releases", map[string]string{
		"platform":     testPlatform,
		"appUrl":       "http://127.0.0.1:1/mesh-client.tar.zst",
		"appSignature": dummySignature,
	})
	if res.StatusCode != http.StatusBadRequest || !strings.Contains(string(data), "无法读取 appUrl") {
		t.Fatalf("unreachable url = %d: %s", res.StatusCode, data)
	}

	// The platform key is required.
	res, data = env.Do("PUT", "/api/admin/releases", map[string]string{"appUrl": "http://example.com/a.tar.zst"})
	if res.StatusCode != http.StatusBadRequest || !strings.Contains(string(data), "platform") {
		t.Fatalf("missing platform = %d: %s", res.StatusCode, data)
	}
}

// TestClientPackageVersionComesFromThePackage pins the new publish shape: the
// version is whatever version.json inside the package declares, and a package
// that has none, or names something the control plane cannot order, is rejected
// rather than stored under a guess.
func TestClientPackageVersionComesFromThePackage(t *testing.T) {
	env := testenv.New(t)

	good := releasePkg(t, env, "2026.11.26-11", []byte("client build"))
	saved := putReleases(t, env, map[string]any{
		"platform": testPlatform,
		"appUrl":   fileServer(t, good),
	}).releaseOf(testPlatform)
	if saved.AppVersion != "2026.11.26-11" {
		t.Fatalf("appVersion = %q, want the version inside the package", saved.AppVersion)
	}

	noManifest := tarZst(t, map[string][]byte{
		"mesh-client.exe":   []byte("client build"),
		"easytier-core.exe": []byte("core"),
	})
	unorderable, err := json.Marshal(map[string]any{
		"version":  "build-2026-11-26",
		"slot":     signing.SlotClient,
		"platform": testPlatform,
		"files": map[string]string{
			"mesh-client.exe":   digestOf([]byte("client build")),
			"easytier-core.exe": digestOf([]byte("core")),
		},
	})
	if err != nil {
		t.Fatalf("marshal version.json: %v", err)
	}
	badLabel := tarZst(t, map[string][]byte{
		"mesh-client.exe":   []byte("client build"),
		"easytier-core.exe": []byte("core"),
		"version.json":      unorderable,
	})
	wrongPlatform, err := json.Marshal(map[string]any{
		"version":  "2026.11.26-11",
		"slot":     signing.SlotClient,
		"platform": "linux/amd64",
		"files": map[string]string{
			"mesh-client":   digestOf([]byte("client build")),
			"easytier-core": digestOf([]byte("core")),
		},
	})
	if err != nil {
		t.Fatalf("marshal version.json: %v", err)
	}
	foreignPlatform := tarZst(t, map[string][]byte{
		"mesh-client":   []byte("client build"),
		"easytier-core": []byte("core"),
		"version.json":  wrongPlatform,
	})

	cases := []struct {
		name string
		pkg  []byte
		want string
	}{
		{name: "missing manifest", pkg: noManifest, want: "version.json"},
		{name: "unorderable version", pkg: badLabel, want: "YYYY.MM.DD-N"},
		{name: "manifest for another platform", pkg: foreignPlatform, want: "slot/platform"},
		{name: "not a tar.zst", pkg: []byte("not a package"), want: "更新包"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, data := env.Do("PUT", "/api/admin/releases", map[string]string{
				"platform":     testPlatform,
				"appUrl":       fileServer(t, tc.pkg),
				"appSignature": dummySignature,
			})
			if res.StatusCode != http.StatusBadRequest || !strings.Contains(string(data), tc.want) {
				t.Fatalf("publish = %d: %s, want 400 containing %q", res.StatusCode, data, tc.want)
			}
			stored, found, err := env.App.Release(testPlatform)
			if err != nil || !found || stored.Client == nil || stored.Client.Version != "2026.11.26-11" {
				t.Fatalf("rejected package changed the row: %+v, found %v, %v", stored, found, err)
			}
		})
	}
}

// TestHeartbeatOnlyUpgrades pins the version-order rule of the heartbeat
// (contract/updates.md): an update goes out only when the device reports a build
// older than the published one. -9 is older than -11 even though string order
// says the opposite, and a device ahead of the published build stays current so
// autoUpdate cannot loop on an old package.
func TestHeartbeatOnlyUpgrades(t *testing.T) {
	env := testenv.New(t)
	var nonces int64
	dev := registerDevice(t, env, "laptop", testTOML)
	release := publishExternal(t, env, testPlatform, "", releasePkg(t, env, "2026.11.26-11", []byte("client build")))

	detail := func() deviceDetail {
		t.Helper()
		res, data := env.Do("GET", "/api/admin/devices/"+dev.internalID, nil)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("get device = %d: %s", res.StatusCode, data)
		}
		var out deviceDetail
		if err := json.Unmarshal(data, &out); err != nil {
			t.Fatalf("decode device: %v", err)
		}
		return out
	}

	// 第 9 号构建比第 11 号旧，但字符串序把 "-9" 排在 "-11" 之后：只有按
	// (日期, 当日序号) 比较才会下发。
	res, data := heartbeat(t, env, dev, &nonces, dev.configHash, "2026.11.26-9")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("older heartbeat = %d, want 200: %s", res.StatusCode, data)
	}
	var got heartbeatResponse
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode heartbeat: %v", err)
	}
	if got.AppUpdate == nil || got.AppUpdate.Version != release.AppVersion {
		t.Fatalf("older heartbeat = %+v, want appUpdate %q", got, release.AppVersion)
	}
	if row := detail(); row.AppState != "stale" {
		t.Fatalf("appState of an older device = %q, want stale", row.AppState)
	}

	// 设备比发布新（回滚过发布、手工装过新包）：不下发，控制台算 current。
	res, data = heartbeat(t, env, dev, &nonces, dev.configHash, "2026.11.26-12")
	if res.StatusCode != http.StatusNoContent || len(data) != 0 {
		t.Fatalf("newer heartbeat = %d (%s), want 204 and no body", res.StatusCode, data)
	}
	if row := detail(); row.AppState != "current" || row.LastAppVersion != "2026.11.26-12" {
		t.Fatalf("newer device = %+v, want current", row)
	}

	// 日期先于当日序号：前一天的构建即使序号更大也更旧。
	res, data = heartbeat(t, env, dev, &nonces, dev.configHash, "2026.11.25-99")
	if res.StatusCode != http.StatusOK || !strings.Contains(string(data), `"appUpdate"`) {
		t.Fatalf("previous-day heartbeat = %d: %s, want appUpdate", res.StatusCode, data)
	}
}

// putTemplate saves the default template through its own route.
func putTemplate(t *testing.T, env *testenv.Env, template string) templateResponse {
	t.Helper()
	res, data := env.Do("PUT", "/api/admin/template", map[string]string{"template": template})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("PUT template = %d: %s", res.StatusCode, data)
	}
	var out templateResponse
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("decode template: %v", err)
	}
	return out
}

// TestTemplateSaveHasItsOwnOutcome pins the split: the template is its own
// resource with its own write, so a publish never touches it and a template save
// never touches a published slot. A blank value deletes the file and falls back
// to the embedded template, so "no custom template" has exactly one
// representation.
func TestTemplateSaveHasItsOwnOutcome(t *testing.T) {
	env := testenv.New(t)
	path := filepath.Join(env.DataDir, "peer.default.toml")
	const custom = `hostname = "custom"`

	// 先发布一个客户端包：保存模板不能改动它。
	file := releasePkg(t, env, "2026.11.26-2", []byte("a whole mesh client binary"))
	savedRelease := putReleases(t, env, map[string]any{"platform": testPlatform, "appUrl": fileServer(t, file)}).releaseOf(testPlatform)

	if saved := putTemplate(t, env, custom); saved.Template != custom {
		t.Fatalf("PUT template answered %q, want %q", saved.Template, custom)
	}
	if raw, err := os.ReadFile(path); err != nil || string(raw) != custom {
		t.Fatalf("peer.default.toml = %q, %v; want the saved template", raw, err)
	}
	stored, found, err := env.App.Release(testPlatform)
	if err != nil || !found || stored.Client == nil || stored.Client.Version != "2026.11.26-2" {
		t.Fatalf("template save changed the release: %+v, found %v, %v", stored, found, err)
	}
	if stored.Client.SHA256 != savedRelease.AppSHA256 {
		t.Fatalf("release digest = %q, want %q", stored.Client.SHA256, savedRelease.AppSHA256)
	}

	// 发布请求里带 defaultToml（或任何未声明字段）由统一的解码规则拒绝：模板
	// 只有 PUT /api/admin/template 一个写入口，发布请求里没有它的位置。
	res, data := env.Do("PUT", "/api/admin/releases", map[string]any{
		"platform":    testPlatform,
		"appUrl":      fileServer(t, file),
		"defaultToml": `hostname = "ignored-by-publish"`,
	})
	if res.StatusCode != http.StatusBadRequest || !strings.Contains(string(data), "invalid_request") {
		t.Fatalf("defaultToml in a publish = %d: %s, want 400 invalid_request", res.StatusCode, data)
	}
	if tmpl := getTemplate(t, env); tmpl.Template != custom {
		t.Fatalf("publish wrote the template: %q, want %q", tmpl.Template, custom)
	}

	for _, blank := range []string{"", "   \n\t\n"} {
		if blank != "" {
			// Put the custom template back, so the second round proves the
			// whitespace-only value acts the same way as the empty one.
			putTemplate(t, env, custom)
		}
		saved := putTemplate(t, env, blank)
		if saved.Template != builtin.DefaultTemplate() {
			t.Fatalf("template after a blank save = %q, want the embedded one", saved.Template)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("blank save left peer.default.toml behind: %v", err)
		}
		// GET falls back to the embedded template too.
		if tmpl := getTemplate(t, env); tmpl.Template != builtin.DefaultTemplate() {
			t.Fatalf("GET template after a blank save = %q, want the embedded one", tmpl.Template)
		}
	}
}

// getTemplate reads GET /api/admin/template.
func getTemplate(t *testing.T, env *testenv.Env) templateResponse {
	t.Helper()
	res, data := env.Do("GET", "/api/admin/template", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET template = %d: %s", res.StatusCode, data)
	}
	var out templateResponse
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("decode template: %v", err)
	}
	return out
}

// TestTemplatePayload pins the prefill shape: the template exactly as stored,
// placeholders and all. Everything else — which placeholders become form
// fields, what they default to — is the console's business.
func TestTemplatePayload(t *testing.T) {
	env := testenv.New(t)
	tmpl := getTemplate(t, env)
	if tmpl.Template != builtin.DefaultTemplate() {
		t.Fatalf("template = %q, want the embedded one", tmpl.Template)
	}
	if !strings.Contains(tmpl.Template, "{{ IP }}") {
		t.Fatalf("template lost its placeholder: %q", tmpl.Template)
	}
}

func TestSavedTemplateAppliesToNewDevices(t *testing.T) {
	env := testenv.New(t)
	const custom = `hostname = "from-settings"
ipv4 = "10.126.126.9/24"`
	putTemplate(t, env, custom)

	// The create dialog prefills from GET /api/admin/template.
	tmpl := getTemplate(t, env)
	if tmpl.Template != custom {
		t.Fatalf("template = %q, want %q", tmpl.Template, custom)
	}

	id, _ := createDevice(t, env, "fresh", tmpl.Template)
	res, data := env.Do("GET", "/api/admin/devices/"+id, nil)
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

// TestDeviceConfigIsStoredVerbatim pins the boundary the console relies on: the
// control plane stores whatever configuration it is handed. It does not know
// about placeholders, addresses or their uniqueness — filling the template is
// the console's job, and a repeated address may be intentional on the machines.
func TestDeviceConfigIsStoredVerbatim(t *testing.T) {
	env := testenv.New(t)
	// 连占位符都会被原样收下：控制面不认识它。
	const withPlaceholder = "ipv4 = \"{{ IP }}\"\n"
	id, _ := createDevice(t, env, "laptop", withPlaceholder)

	// 同一个地址建两台也不拦：多网卡、迁移期并存都可能是有意为之。
	createDevice(t, env, "ali-seed", "ipv4 = \"10.126.126.1/24\"\n")
	createDevice(t, env, "wsl", "ipv4 = \"10.126.126.1/24\"\n")

	const rewritten = "dhcp = false\nipv4 = \"10.126.126.4/24\"\n"
	res, data := env.Do("PUT", "/api/admin/devices/"+id, map[string]string{"name": "laptop", "configToml": rewritten})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("update = %d: %s, want 200", res.StatusCode, data)
	}
	res, data = env.Do("GET", "/api/admin/devices/"+id, nil)
	var device deviceDetail
	if err := json.Unmarshal(data, &device); err != nil {
		t.Fatalf("decode device: %v", err)
	}
	if device.ConfigTOML != rewritten || device.ConfigHash != core.Hash(rewritten) {
		t.Fatalf("stored config = %q/%q, want the submitted text verbatim", device.ConfigTOML, device.ConfigHash)
	}
}
