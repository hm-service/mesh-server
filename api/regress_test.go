package api_test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"mesh-server/api"
	"mesh-server/app"
	"mesh-server/internal/testenv"
	"mesh-server/signing"
)

// TestRegisterRejectsAStranger pins what keeps the configuration out of reach:
// knowing deviceId and pubkey is not enough, because the only proof the control
// plane accepts is a signature from the slot's key. Neither an unsigned request
// nor one signed by another key gets a configToml.
func TestRegisterRejectsAStranger(t *testing.T) {
	env := testenv.New(t)
	dev := registerDevice(t, env, "laptop", testTOML)
	pub := publicKeyOf(t, env, dev.internalID)
	now := env.Clock.Now().Unix()

	// 三件套全空：注册没有别的凭据可出示，验签这一步拒绝。
	unsigned, err := json.Marshal(map[string]string{
		"deviceId": dev.deviceID,
		"hostname": "HOST-laptop",
		"pubkey":   base64.StdEncoding.EncodeToString(pub),
		"platform": testPlatform,
	})
	if err != nil {
		t.Fatal(err)
	}
	res, body := env.Do("POST", "/api/v1/register", json.RawMessage(unsigned))
	if res.StatusCode != http.StatusUnauthorized || !strings.Contains(string(body), "unauthorized") {
		t.Fatalf("unsigned register = %d: %s, want 401 unauthorized", res.StatusCode, body)
	}
	if strings.Contains(string(body), "configToml") || strings.Contains(string(body), "network_secret") {
		t.Fatalf("the response leaked the configuration: %s", body)
	}

	// 拿另一把钥匙给这个槽位的公钥签名：验签失败，同样是 401。
	var nonces int64
	_, strangerPriv := newKeypair(t)
	forged := signedRegisterBody(t, dev.deviceID, strangerPriv, "HOST-laptop", pub, now, newNonce(&nonces))
	res, body = env.Do("POST", "/api/v1/register", json.RawMessage(forged))
	if res.StatusCode != http.StatusUnauthorized || !strings.Contains(string(body), "unauthorized") {
		t.Fatalf("register signed by another key = %d: %s, want 401 unauthorized", res.StatusCode, body)
	}
	if strings.Contains(string(body), "configToml") || strings.Contains(string(body), "network_secret") {
		t.Fatalf("the response leaked the configuration: %s", body)
	}

	// The same request with a signature from the slot's key still works: the
	// published client keeps its idempotent re-registration.
	signedBody := signedRegisterBody(t, dev.deviceID, dev.priv, "HOST-laptop", pub, now, newNonce(&nonces))
	res, data := env.Do("POST", "/api/v1/register", json.RawMessage(signedBody))
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
		{"PUT", "/api/admin/template"},
		{"GET", "/api/admin/devices"},
		{"POST", "/api/admin/devices"},
		{"GET", "/api/admin/devices/d_000000"},
		{"PUT", "/api/admin/devices/d_000000"},
		{"DELETE", "/api/admin/devices/d_000000"},
		{"POST", "/api/admin/devices/d_000000/pubkey"},
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
	file := releasePkg(t, env, "2026.11.24-3", client)
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
	file := releasePkg(t, env, "2026.11.25-1", []byte("a whole mesh client binary"))
	// Both slots are signed: the launcher URL is the one that must fail, and a
	// missing signature would fail first and hide that.
	appURL := fileServer(t, file)
	res, body := env.Do("PUT", "/api/admin/releases", map[string]string{
		"platform":          testPlatform,
		"appUrl":            appURL,
		"appSignature":      env.Sign(signing.SlotClient, testPlatform, "2026.11.25-1", digestOf(file)),
		"launcherVersion":   "2026.11.25-9",
		"launcherUrl":       "http://127.0.0.1:1/mesh.exe",
		"launcherSignature": env.Sign(signing.SlotLauncher, testPlatform, "2026.11.25-9", strings.Repeat("0", 64)),
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

// TestRegisterGateDoesNotStarveHeartbeat pins that registration and heartbeats
// have separate gates. Registering is unauthenticated and holds its slot until
// the body-read deadline; a shared gate would let a handful of silent register
// requests turn every device's heartbeat into 503 busy.
func TestRegisterGateDoesNotStarveHeartbeat(t *testing.T) {
	env := testenv.New(t, testenv.WithAPIConcurrency(1))

	body, writer := io.Pipe()
	defer writer.Close()
	req, err := http.NewRequest(http.MethodPost, env.Server.URL+"/api/v1/register", body)
	if err != nil {
		t.Fatalf("build register request: %v", err)
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
	// Give the handler time to take its slot and block in ReadBody.
	time.Sleep(200 * time.Millisecond)

	// 心跳内容不必合法：只要求它不是 503——400/401 都说明请求进了闸门后面，
	// 没被那条卡住的注册挤掉。
	res, data := env.Do("POST", "/api/v1/heartbeat", []byte("{}"))
	if res.StatusCode == http.StatusServiceUnavailable {
		t.Fatalf("heartbeat got 503 while a register held its slot: %s", data)
	}

	writer.Close()
	select {
	case <-sent:
	case <-time.After(5 * time.Second):
		t.Fatal("the stalled register request did not end")
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

// TestCreateDeviceRejectsAutoUpdate pins F4: 新设备一律跟随发布包，请求里带这个
// 字段就 400，而不是收下 200 再悄悄丢掉。autoUpdate 不是这个请求声明的字段，
// 由统一的严格解码拒绝（400 invalid_request），不是这一处单独检查。
func TestCreateDeviceRejectsAutoUpdate(t *testing.T) {
	env := testenv.New(t)
	count := func() int {
		res, data := env.Do("GET", "/api/admin/devices", nil)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("list devices = %d: %s", res.StatusCode, data)
		}
		var list []json.RawMessage
		if err := json.Unmarshal(data, &list); err != nil {
			t.Fatal(err)
		}
		return len(list)
	}
	before := count()
	for _, value := range []any{true, false, nil} {
		res, data := env.Do("POST", "/api/admin/devices", map[string]any{
			"name": "unused", "configToml": testTOML, "autoUpdate": value,
		})
		if res.StatusCode != http.StatusBadRequest || !strings.Contains(string(data), "invalid_request") {
			t.Fatalf("create with autoUpdate=%v = %d, want 400 invalid_request: %s", value, res.StatusCode, data)
		}
	}
	if after := count(); after != before {
		t.Fatalf("a rejected create still added a device: %d -> %d", before, after)
	}
}

// TestDeviceConfigHasItsOwnLimit pins F9: 1MiB 请求体是唯一限制，而设备列表要把
// 全部配置读进内存来算派生字段，所以约束得落在写入端。
func TestDeviceConfigHasItsOwnLimit(t *testing.T) {
	env := testenv.New(t)
	pub, _ := newKeypair(t)
	pubkey := base64.StdEncoding.EncodeToString(pub)
	over := strings.Repeat("a", app.MaxConfigBytes+1)
	res, data := env.Do("POST", "/api/admin/devices", map[string]string{"name": "big", "configToml": over, "pubkey": pubkey})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("create with an oversized config = %d, want 400: %s", res.StatusCode, data)
	}

	// 上限之内照收。
	atLimit := strings.Repeat("a", app.MaxConfigBytes)
	res, data = env.Do("POST", "/api/admin/devices", map[string]string{"name": "big", "configToml": atLimit, "pubkey": pubkey})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("create at the limit = %d, want 200: %s", res.StatusCode, data)
	}

	dev := registerDevice(t, env, "laptop", testTOML)
	res, data = env.Do("PUT", "/api/admin/devices/"+dev.internalID, map[string]string{"name": "laptop", "configToml": over})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("update with an oversized config = %d, want 400: %s", res.StatusCode, data)
	}
}

// TestConsoleEntryIsNotCachedAndMissingAssetsAreNotFound pins F5: index.html 不是
// 指纹文件名，标一年不可变会让升级后仍用旧入口文档；缺失资源回落到入口文档
// 只会把 404 变成一个浏览器拒绝执行的 200 text/html。
func TestConsoleEntryIsNotCachedAndMissingAssetsAreNotFound(t *testing.T) {
	env := testenv.New(t)
	build := fstest.MapFS{
		"index.html":      {Data: []byte("<!doctype html>")},
		"assets/app-1.js": {Data: []byte("// app")},
	}
	server := httptest.NewServer(api.New(api.Options{App: env.App, Static: build}).Handler())
	defer server.Close()

	cases := []struct {
		path   string
		status int
		cache  string
	}{
		{"/admin", http.StatusOK, "no-store"},
		{"/admin/", http.StatusOK, "no-store"},
		{"/admin/index.html", http.StatusOK, "no-store"},
		{"/admin/assets/app-1.js", http.StatusOK, "public, max-age=31536000, immutable"},
		{"/admin/assets/missing-2.js", http.StatusNotFound, ""},
	}
	for _, tc := range cases {
		res, err := http.Get(server.URL + tc.path)
		if err != nil {
			t.Fatalf("GET %s: %v", tc.path, err)
		}
		_, _ = io.Copy(io.Discard, res.Body)
		res.Body.Close()
		if res.StatusCode != tc.status {
			t.Fatalf("GET %s = %d, want %d", tc.path, res.StatusCode, tc.status)
		}
		if got := res.Header.Get("Cache-Control"); got != tc.cache {
			t.Fatalf("GET %s Cache-Control = %q, want %q", tc.path, got, tc.cache)
		}
	}
}
