package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"mesh-server/app"
	"mesh-server/core"
)

// TestNonceCacheExhaustionIsRetryable pins the third answer of the replay cache:
// a full cache is the server's capacity, not proof that the device identity is
// invalid. It answers 503 with Retry-After, leaves every nonce already inside its
// window in place (a replay is still a 401 with the unchanged body), and does not
// block a request that carries another complete proof.
func TestNonceCacheExhaustionIsRetryable(t *testing.T) {
	instance, err := app.Open(t.TempDir(), app.Options{})
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	device, token, err := instance.CreateDevice("laptop", "a = 1\n")
	if err != nil {
		t.Fatalf("CreateDevice() error: %v", err)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	pubkey := base64.StdEncoding.EncodeToString(pub)
	if _, err := instance.BindDevice(context.Background(), token, "dev-1", "HOST", pubkey, ""); err != nil {
		t.Fatalf("BindDevice() error: %v", err)
	}

	server := New(Options{App: instance, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	// 直接填满重放缓存：容量上限是 8192 条，靠 8192 次真实请求把它占满只会让这条
	// 用例变成一次压测，测的还是同一段代码。
	now := instance.Now()
	full := core.NewNonceCache(core.ClockSkew)
	for i := range core.NonceCacheEntries {
		if got := full.Claim(fmt.Sprintf("registered-%05d", i), now, now); got != core.NonceAccepted {
			t.Fatalf("fill claim %d = %v, want NonceAccepted", i, got)
		}
	}
	server.nonces = full
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	post := func(path string, body []byte) (*http.Response, []byte) {
		t.Helper()
		res, err := httpServer.Client().Post(httpServer.URL+path, "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		defer res.Body.Close()
		data, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatalf("read %s body: %v", path, err)
		}
		return res, data
	}
	sign := func(path, nonce string, extra map[string]string) []byte {
		t.Helper()
		stamp := strconv.FormatInt(now.Unix(), 10)
		message := core.SigningString(http.MethodPost, path, "dev-1", stamp, nonce, "windows/amd64", "", "")
		payload := map[string]string{
			"deviceId":  "dev-1",
			"timestamp": stamp,
			"nonce":     nonce,
			"platform":  "windows/amd64",
			"signature": base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte(message))),
			"pubkey":    pubkey,
		}
		for key, value := range extra {
			payload[key] = value
		}
		body, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
		return body
	}

	// 容量耗尽：503 + Retry-After，响应体是公共的 busy，不是认证失败。
	res, data := post("/api/v1/heartbeat", sign("/api/v1/heartbeat", "fresh-nonce", nil))
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("heartbeat with a full replay cache = %d, want 503: %s", res.StatusCode, data)
	}
	if got := res.Header.Get("Retry-After"); got == "" {
		t.Fatal("503 without Retry-After: the client cannot tell when to come back")
	}
	if !strings.Contains(string(data), `"busy"`) {
		t.Fatalf("503 body = %s, want code busy", data)
	}

	// 饱和没有挤掉窗口内的条目：同一份签名再发一次仍是重放，仍是那条 401。
	res, data = post("/api/v1/heartbeat", sign("/api/v1/heartbeat", "registered-00000", nil))
	if res.StatusCode != http.StatusUnauthorized || !strings.Contains(string(data), `"unauthorized"`) {
		t.Fatalf("replay with a full cache = %d %s, want 401 unauthorized", res.StatusCode, data)
	}

	// 注册这条路径同理：签名这一支登记不下 nonce 时给出同一个可重试答复。
	res, data = post("/api/v1/register", sign("/api/v1/register", "fresh-register-nonce", nil))
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("signed register with a full replay cache = %d, want 503: %s", res.StatusCode, data)
	}
	if got := res.Header.Get("Retry-After"); got == "" {
		t.Fatal("register 503 without Retry-After")
	}

	// 令牌本身就是一份完整的证明，缓存满不影响它。
	issued, err := instance.IssueToken(device.PublicID)
	if err != nil {
		t.Fatalf("IssueToken() error: %v", err)
	}
	body, err := json.Marshal(map[string]string{
		"token":    issued,
		"deviceId": "dev-1",
		"hostname": "HOST",
		"pubkey":   pubkey,
	})
	if err != nil {
		t.Fatalf("marshal register: %v", err)
	}
	res, data = post("/api/v1/register", body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("register with a token and a full replay cache = %d, want 200: %s", res.StatusCode, data)
	}
}
