package api_test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"mesh-server/internal/testenv"
)

// TestRegisterIdempotentBranchRequiresCredential pins the fix for the worst hole
// in the old register flow: the idle branch ran before any token check, so
// knowing deviceId and pubkey was enough to walk away with a configToml that
// carries the network secret.
func TestRegisterIdempotentBranchRequiresCredential(t *testing.T) {
	env := testenv.New(t)
	dev := registerDevice(t, env, "laptop", testTOML)
	pub := publicKeyOf(t, env, dev.internalID)

	cases := []struct {
		name  string
		token string
	}{
		{name: "no token", token: ""},
		{name: "garbage token", token: "etk_deadbeef"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, body := register(t, env, tc.token, dev.deviceID, "HOST-laptop", pub)
			if status != http.StatusUnauthorized {
				t.Fatalf("register = %d, want 401: %s", status, body)
			}
			if strings.Contains(string(body), "configToml") || strings.Contains(string(body), "network_secret") {
				t.Fatalf("the response leaked the configuration: %s", body)
			}
		})
	}

	// The same request with a signature from the bound key still works: the
	// published client keeps its idempotent re-registration.
	var nonces int64
	body := signedRegisterBody(t, dev.deviceID, dev.priv, "HOST-laptop", pub, env.Clock.Now().Unix(), newNonce(&nonces))
	res, data := env.Do("POST", "/api/v1/register", json.RawMessage(body))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("signed re-register = %d, want 200: %s", res.StatusCode, data)
	}
	var out configResponse
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if out.ConfigTOML != dev.configTOML {
		t.Fatalf("signed re-register returned %q", out.ConfigTOML)
	}
}

func publicKeyOf(t *testing.T, env *testenv.Env, publicID string) []byte {
	t.Helper()
	device, found, err := env.App.DeviceByID(publicID)
	if err != nil || !found {
		t.Fatalf("DeviceByID(%s) = found %v, %v", publicID, found, err)
	}
	raw, err := base64.StdEncoding.DecodeString(device.Pubkey)
	if err != nil {
		t.Fatalf("decode pubkey: %v", err)
	}
	return raw
}

// TestManagementRoutesRequireSession walks the whole admin surface. The old code
// had a no_auth switch that skipped this check; the route table is now the only
// place a management endpoint can be registered, and every one of them must be
// guarded.
func TestManagementRoutesRequireSession(t *testing.T) {
	env := testenv.New(t, testenv.WithoutLogin())

	routes := []struct {
		method string
		path   string
	}{
		{"GET", "/api/admin/template"},
		{"GET", "/api/admin/devices"},
		{"POST", "/api/admin/devices"},
		{"GET", "/api/admin/devices/d_000000"},
		{"PUT", "/api/admin/devices/d_000000"},
		{"DELETE", "/api/admin/devices/d_000000"},
		{"POST", "/api/admin/devices/d_000000/token"},
		{"POST", "/api/admin/devices/d_000000/credential"},
		{"DELETE", "/api/admin/devices/d_000000/credential"},
		{"GET", "/api/admin/releases"},
		{"PUT", "/api/admin/releases"},
		{"DELETE", "/api/admin/releases/windows/amd64"},
	}
	for _, route := range routes {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			res, body := env.Do(route.method, route.path, map[string]string{"name": "x", "configToml": "a = 1\n"})
			if res.StatusCode != http.StatusUnauthorized {
				t.Fatalf("%s %s = %d, want 401: %s", route.method, route.path, res.StatusCode, body)
			}
			if !strings.Contains(string(body), "unauthorized") {
				t.Fatalf("body = %s, want code unauthorized", body)
			}
		})
	}
}

// TestReleaseExposesFullDigest pins that the full package sha256 travels to the
// client so it can verify what it downloaded.
func TestReleaseExposesFullDigest(t *testing.T) {
	env := testenv.New(t)
	var nonces int64
	dev := registerDevice(t, env, "laptop", testTOML)
	client := []byte("a whole mesh client binary")
	file := releasePkg(t, "2026.11.24-3", client)
	release := putReleases(t, env, map[string]any{
		"platform": testPlatform,
		"appUrl":   fileServer(t, file),
	}).releaseOf(testPlatform)

	pkgSum := sha256.Sum256(file)
	pkgFull := hex.EncodeToString(pkgSum[:])
	if release.AppSHA256 != pkgFull {
		t.Fatalf("appSha256 = %q, want %q", release.AppSHA256, pkgFull)
	}

	// The heartbeat carries the same digest to the client.
	res, body := heartbeat(t, env, dev, &nonces, dev.configHash, "2026.11.23-1")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("heartbeat = %d: %s", res.StatusCode, body)
	}
	var out heartbeatResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if out.AppUpdate == nil || out.AppUpdate.SHA256 != pkgFull {
		t.Fatalf("appUpdate = %+v, want sha256 %q", out.AppUpdate, pkgFull)
	}
}

// TestPublishRejectsBothSlotsTogether pins that a rejected publish writes
// nothing. The client slot of the same request was valid and downloaded already;
// before the two slots shared one transaction it was stored anyway and every
// device of that platform started receiving an update the admin never saw
// confirmed.
func TestPublishRejectsBothSlotsTogether(t *testing.T) {
	env := testenv.New(t)
	file := releasePkg(t, "2026.11.25-1", []byte("a whole mesh client binary"))
	res, body := env.Do("PUT", "/api/admin/releases", map[string]string{
		"platform":        testPlatform,
		"appUrl":          fileServer(t, file),
		"launcherVersion": "2026.11.25-9",
		"launcherUrl":     "http://127.0.0.1:1/mesh.exe",
	})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("publish = %d, want 400: %s", res.StatusCode, body)
	}
	if _, found, err := env.App.Release(testPlatform); err != nil || found {
		t.Fatalf("a rejected publish stored a slot: found %v, %v", found, err)
	}
}

// TestAllowNamesRegisteredMethods pins the Allow header of the 405 fallback: it
// used to be the constant "GET, HEAD" for every path, which told a client that
// GET /api/auth is supported when only POST is.
func TestAllowNamesRegisteredMethods(t *testing.T) {
	env := testenv.New(t)
	cases := []struct {
		method string
		path   string
		want   string
	}{
		{"GET", "/api/auth", "POST"},
		{"POST", "/api/admin/releases", "GET, HEAD, PUT"},
		{"DELETE", "/api/admin/devices", "GET, HEAD, POST"},
		{"GET", "/api/admin/layout", "PUT"},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			res, body := env.Do(tc.method, tc.path, nil)
			if res.StatusCode != http.StatusMethodNotAllowed {
				t.Fatalf("%s %s = %d, want 405: %s", tc.method, tc.path, res.StatusCode, body)
			}
			if allow := res.Header.Get("Allow"); allow != tc.want {
				t.Fatalf("Allow = %q, want %q", allow, tc.want)
			}
		})
	}
}

// TestDeviceGateDoesNotStarveAdmin pins that the device surface has its own
// concurrency gate: one unauthenticated request that never sends its body must
// not turn the console API into 503 busy.
func TestDeviceGateDoesNotStarveAdmin(t *testing.T) {
	env := testenv.New(t, testenv.WithAPIConcurrency(1))

	body, writer := io.Pipe()
	defer writer.Close()
	req, err := http.NewRequest(http.MethodPost, env.Server.URL+"/api/v1/heartbeat", body)
	if err != nil {
		t.Fatalf("build heartbeat request: %v", err)
	}
	req.ContentLength = 128
	sent := make(chan struct{})
	go func() {
		defer close(sent)
		res, err := env.Client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, res.Body)
			res.Body.Close()
		}
	}()
	// Give the handler time to take its gate slot and block in ReadBody.
	time.Sleep(200 * time.Millisecond)

	res, data := env.Do("GET", "/api/admin/devices", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/admin/devices while a device request is stuck = %d: %s", res.StatusCode, data)
	}

	writer.Close()
	select {
	case <-sent:
	case <-time.After(5 * time.Second):
		t.Fatal("the stalled device request did not end")
	}
}

func TestProbes(t *testing.T) {
	env := testenv.New(t, testenv.WithoutLogin(), testenv.WithReadyzToken("readyz_token_1234"))

	res, body := env.Do("GET", "/livez", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("/livez = %d, want 200: %s", res.StatusCode, body)
	}

	res, body = env.Do("GET", "/readyz", nil)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("/readyz without token = %d, want 401: %s", res.StatusCode, body)
	}
	res, body = env.Do("GET", "/readyz?token=wrong_readyz_token", nil)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("/readyz with wrong token = %d, want 401: %s", res.StatusCode, body)
	}
	res, body = env.Do("GET", "/readyz?token=readyz_token_1234", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("/readyz = %d, want 200: %s", res.StatusCode, body)
	}
	var payload map[string]bool
	if err := json.Unmarshal(body, &payload); err != nil || !payload["ok"] {
		t.Fatalf("/readyz body = %s, want ok", body)
	}
}

func TestMethodNotAllowedAndNotFound(t *testing.T) {
	env := testenv.New(t)

	res, body := env.Do("DELETE", "/api/admin/devices", nil)
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE /api/admin/devices = %d, want 405: %s", res.StatusCode, body)
	}
	if allow := res.Header.Get("Allow"); !strings.Contains(allow, "GET") {
		t.Fatalf("Allow = %q, want GET", allow)
	}
	res, body = env.Do("GET", "/api/unknown", nil)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("GET /api/unknown = %d, want 404: %s", res.StatusCode, body)
	}
	// The root redirects to the console; the session client would follow it,
	// so this one is built to stop at the first response.
	noFollow := &http.Client{
		Transport:     env.Server.Client().Transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	res, err := noFollow.Get(env.Server.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusFound || res.Header.Get("Location") != "/admin" {
		t.Fatalf("GET / = %d -> %q, want 302 /admin", res.StatusCode, res.Header.Get("Location"))
	}
}
