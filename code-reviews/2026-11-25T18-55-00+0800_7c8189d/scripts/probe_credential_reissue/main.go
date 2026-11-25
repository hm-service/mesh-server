// Probe: how often does the server issue a credential?
//
// It runs the real app + HTTP handler with a stand-in easytier-cli (this same
// binary, re-executed with the argv the issuer builds). Every CLI call appends
// "<credential_id> <secret>" to PROBE_CLI_LOG, so each issued secret can be
// matched back to the id it was generated for.
//
// Part A (renewal churn): a device that keeps reporting a stale id after a
// renewal must not get a new credential on every single heartbeat. The first
// heartbeat reports "none" (a caller that has no credential), which is what a
// fresh ordinary node sends; the empty string belongs to management nodes and is
// never issued to.
// Part B (concurrent heartbeats): N heartbeats at once for a device that has no
// credential each issue one, and a response must not pair a secret with the id of
// *another* issuance (`issueCredentialIfNeeded` used to re-read the newest row
// instead of using the one it just created).
package main

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
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"mesh-server/api"
	"mesh-server/app"
	"mesh-server/core"
)

const probeToken = "probe_token_0123456789"

// probeNow is the fake clock every environment starts at (2024-11-23T22:13:20Z).
var probeNow = time.Unix(1_732_400_000, 0)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "-p" {
		fakeCLI(os.Args)
		return
	}
	churn()
	concurrent()
}

// fakeCLI answers the way easytier-cli does (verified against v2.7.1: see
// REVIEW.md), and records every issuance. expiry_unix is a JSON string, which
// is what pbjson emits for int64.
func fakeCLI(args []string) {
	joined := strings.Join(args, " ")
	switch {
	case strings.Contains(joined, "credential generate"):
		id := valueOf(args, "--credential-id")
		ttl, _ := strconv.ParseInt(valueOf(args, "--ttl"), 10, 64)
		logPath := os.Getenv("PROBE_CLI_LOG")
		f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		// A unique secret per call, without coordinating between children.
		secret := fmt.Sprintf("secret-%d-%d", os.Getpid(), time.Now().UnixNano())
		time.Sleep(200 * time.Millisecond)
		fmt.Fprintf(f, "%s %s\n", id, secret)
		f.Close()
		base, _ := strconv.ParseInt(os.Getenv("PROBE_NOW"), 10, 64)
		out := map[string]string{
			"credential_id":     id,
			"credential_secret": base64.StdEncoding.EncodeToString([]byte(secret + strings.Repeat("x", 20))),
			"expiry_unix":       strconv.FormatInt(base+ttl, 10),
		}
		raw, _ := json.Marshal(out)
		fmt.Println(string(raw))
	case strings.Contains(joined, "credential revoke"):
		fmt.Println(`{"success":true}`)
	default:
		fmt.Fprintf(os.Stderr, "unexpected args %q\n", args)
		os.Exit(2)
	}
}

func valueOf(args []string, name string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == name {
			return args[i+1]
		}
	}
	return ""
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
	// 假 CLI 用 PROBE_NOW 算 expiry_unix：不跟着推进的话，续期后签出的那张
	// 相对假时钟永远只剩 6 天，续期窗口关不上，每轮都会续签。
	os.Setenv("PROBE_NOW", strconv.FormatInt(c.t.Unix(), 10))
}

type env struct {
	app    *app.App
	srv    *httptest.Server
	client *http.Client
	clock  *clock
	bearer string
}

func newEnv(dir string) *env {
	clk := &clock{t: probeNow}
	self, err := os.Executable()
	if err != nil {
		panic(err)
	}
	instance, err := app.Open(dir, app.Options{
		Now:           clk.Now,
		CredentialCLI: self,
		CredentialRPC: "127.0.0.1:1",
	})
	if err != nil {
		panic(err)
	}
	hash, err := core.NewTokenHash(probeToken)
	if err != nil {
		panic(err)
	}
	srv := httptest.NewServer(api.New(api.Options{
		App:        instance,
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		TokenHash:  hash,
		SessionKey: []byte("probe_session_key_0123456789abcdef"),
		Sleep:      func(time.Duration) {},
	}).Handler())
	client := srv.Client()
	client.Timeout = 60 * time.Second
	e := &env{app: instance, srv: srv, client: client, clock: clk}
	var login struct {
		AccessToken string `json:"access_token"`
	}
	mustCall(e, "POST", "/api/auth", map[string]string{"token": probeToken}, &login)
	e.bearer = login.AccessToken
	return e
}

func (e *env) close() {
	e.srv.Close()
	_ = e.app.Close()
}

type testDevice struct {
	id   string
	priv ed25519.PrivateKey
}

// newDevice registers one device slot and returns the identity a client would hold.
func (e *env) newDevice(name string) testDevice {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	var created struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	mustCall(e, "POST", "/api/devices", map[string]string{
		"name":        name,
		"config_toml": "dhcp = true\n",
	}, &created)
	status, raw := rawCall(e, "POST", "/api/register", map[string]string{
		"token":     created.Token,
		"device_id": "probe-device-" + name,
		"hostname":  name,
		"pubkey":    base64.StdEncoding.EncodeToString(pub),
	}, "")
	if status != http.StatusOK {
		panic(fmt.Sprintf("register = %d: %s", status, raw))
	}
	return testDevice{id: "probe-device-" + name, priv: priv}
}

type heartbeatAnswer struct {
	Status        int
	Credential    string `json:"credential"`
	CredentialID  string `json:"credential_id"`
	CredentialExp int64  `json:"credential_expiry"`
}

func (e *env) heartbeat(d testDevice, credentialID, configHash string) heartbeatAnswer {
	ts := strconv.FormatInt(e.clock.Now().Unix(), 10)
	nonce, err := core.RandomHex(16)
	if err != nil {
		panic(err)
	}
	const platform = "windows/amd64"
	signed := core.SigningString("POST", "/api/heartbeat", d.id, ts, nonce, platform, configHash, "")
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(d.priv, []byte(signed)))
	body := map[string]string{
		"deviceId":     d.id,
		"timestamp":    ts,
		"nonce":        nonce,
		"signature":    sig,
		"platform":     platform,
		"configHash":   configHash,
		"appHash":      "",
		"credentialId": credentialID,
	}
	status, raw := rawCall(e, "POST", "/api/heartbeat", body, "")
	answer := heartbeatAnswer{Status: status}
	if status == http.StatusOK {
		if err := json.Unmarshal(raw, &answer); err != nil {
			panic(fmt.Sprintf("decode heartbeat: %v (%s)", err, raw))
		}
	}
	return answer
}

// issued reads the CLI log: every line is "<requested id> <secret>".
func issued(logPath string) map[string]string {
	out := map[string]string{}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		return out
	}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 {
			out[fields[1]] = fields[0]
		}
	}
	return out
}

func churn() {
	dir, err := os.MkdirTemp("", "probe-churn-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	e := newEnv(dir)
	defer e.close()
	d := e.newDevice("churn")
	cfg := core.Hash("dhcp = true\n")

	first := e.heartbeat(d, "none", cfg)
	fmt.Printf("FIRST  status=%d credential_id=%q issued=%t\n", first.Status, first.CredentialID, first.Credential != "")
	if first.CredentialID == "" {
		fmt.Println("SETUP FAILED: no credential on the first heartbeat")
		return
	}
	settled := e.heartbeat(d, first.CredentialID, cfg)
	fmt.Printf("AGAIN  reporting the issued id: status=%d issued=%t (want no new credential)\n", settled.Status, settled.Credential != "")

	// 30-day TTL, renewal inside the last 7 days.
	e.clock.Advance(24 * 24 * time.Hour)
	renewed := e.heartbeat(d, first.CredentialID, cfg)
	fmt.Printf("RENEW  status=%d new_id=%q issued=%t\n", renewed.Status, renewed.CredentialID, renewed.Credential != "")
	if renewed.CredentialID == "" {
		fmt.Println("SETUP FAILED: renewal did not issue a credential")
		return
	}
	// The device keeps using the credential it already has (the response was
	// lost, or it has not restarted core yet) and says so on every heartbeat.
	extra := 0
	for i := range 4 {
		a := e.heartbeat(d, first.CredentialID, cfg)
		if a.Credential != "" {
			extra++
		}
		fmt.Printf("OLD-ID round %d: status=%d issued=%t id=%q\n", i+1, a.Status, a.Credential != "", a.CredentialID)
	}
	total := len(issued(os.Getenv("PROBE_CLI_LOG")))
	fmt.Printf("TOTAL issued=%d (1 initial + 1 renewal + %d while the device kept reporting the old id)\n", total, extra)
	if extra == 4 {
		fmt.Println("REPRODUCED: every heartbeat that reports a stale credential id signs another credential")
	} else {
		fmt.Println("NOT REPRODUCED")
	}
}

func concurrent() {
	logPath := os.Getenv("PROBE_CLI_LOG")
	dir, err := os.MkdirTemp("", "probe-concurrent-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	e := newEnv(dir)
	defer e.close()
	d := e.newDevice("concurrent")
	cfg := core.Hash("dhcp = true\n")

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		answers []heartbeatAnswer
	)
	const rounds = 8
	for range rounds {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a := e.heartbeat(d, "none", cfg)
			mu.Lock()
			answers = append(answers, a)
			mu.Unlock()
		}()
	}
	wg.Wait()

	byIssued := issued(logPath)
	mismatch := 0
	withCredential := 0
	distinctIDs := map[string]struct{}{}
	for _, a := range answers {
		if a.Credential == "" {
			continue
		}
		withCredential++
		distinctIDs[a.CredentialID] = struct{}{}
		secret := secretOf(a.Credential)
		if owner := byIssued[secret]; owner != a.CredentialID {
			mismatch++
			fmt.Printf("MISMATCH secret=%s was generated for id=%s, response carries id=%s\n", secret, owner, a.CredentialID)
		}
	}
	fmt.Printf("CONCURRENT heartbeats=%d answered_with_credential=%d distinct_ids=%d cli_calls=%d mismatched_pairs=%d\n",
		rounds, withCredential, len(distinctIDs), len(byIssued), mismatch)
	// 这台设备一条凭据记录都没有：并发首轮各签一张是已知且接受的代价（没有可
	// 比较的创建时间，冷却挡不住），多出来的是没有被确认过的废凭据。这里判的是
	// F2 的串台，不是签发张数。
	if mismatch > 0 {
		fmt.Printf("REPRODUCED: %d responses paired a private key with another issuance's credential_id\n", mismatch)
		return
	}
	fmt.Println("NOT REPRODUCED: every response paired its secret with the id it was generated for")
}

// secretOf reverses the base64 the fake CLI produced.
func secretOf(encoded string) string {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return ""
	}
	return strings.TrimRight(string(raw), "x")
}

func rawCall(e *env, method, path string, body any, bearer string) (int, []byte) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			panic(err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, e.srv.URL+path, reader)
	if err != nil {
		panic(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	res, err := e.client.Do(req)
	if err != nil {
		panic(err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		panic(err)
	}
	return res.StatusCode, data
}

func mustCall(e *env, method, path string, body any, out any) {
	status, raw := rawCall(e, method, path, body, e.bearer)
	if status != http.StatusOK {
		panic(fmt.Sprintf("%s %s = %d: %s", method, path, status, raw))
	}
	if err := json.Unmarshal(raw, out); err != nil {
		panic(fmt.Sprintf("%s %s decode: %v (%s)", method, path, err, raw))
	}
}
