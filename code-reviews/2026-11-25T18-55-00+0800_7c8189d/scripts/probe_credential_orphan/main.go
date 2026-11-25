// Probe: what happens to credential rows when the issuance channel is off?
//
// RevokeCredential returns nil immediately when ET_CLI is unset, so both
// DELETE /api/devices/{id}/credential and DELETE /api/devices/{id} report
// success while the seed-side credential and its row stay behind.
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
	"mesh-server/db"
)

const probeToken = "probe_token_0123456789"

var probeNow = time.Unix(1_732_400_000, 0)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "-p" {
		fakeCLI(os.Args)
		return
	}
	logPath := os.Getenv("PROBE_CLI_LOG")
	dir, err := os.MkdirTemp("", "probe-orphan-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)

	// Phase 1: issuance channel on. One heartbeat issues one credential.
	withCLI := open(dir, true)
	d := withCLI.newDevice("orphan")
	cfg := core.Hash("dhcp = true\n")
	first := withCLI.heartbeat(d, "", cfg)
	if first.CredentialID == "" {
		fmt.Println("SETUP FAILED: no credential was issued")
		return
	}
	fmt.Printf("ISSUED credential_id=%s\n", first.CredentialID)
	if _, err := withCLI.app.LatestCredential(d.publicID); err != nil {
		fmt.Printf("SETUP FAILED: %v\n", err)
		return
	}
	withCLI.close()

	// Phase 2: the operator drops ET_CLI from the deployment. Rows survive.
	off := open(dir, false)
	defer off.close()
	status, raw := rawCall(off, "DELETE", "/api/devices/"+d.publicID+"/credential", nil, off.bearer)
	fmt.Printf("REVOKE-ENDPOINT status=%d body=%s\n", status, strings.TrimSpace(string(raw)))
	if _, err := off.app.LatestCredential(d.publicID); err == nil {
		fmt.Println("ROW REMAINS after DELETE /api/devices/{id}/credential (answered success)")
	}
	status, raw = rawCall(off, "DELETE", "/api/devices/"+d.publicID, nil, off.bearer)
	fmt.Printf("DELETE-DEVICE  status=%d body=%s\n", status, strings.TrimSpace(string(raw)))
	if _, err := off.app.LatestCredential(d.publicID); err == nil {
		fmt.Println("ROW REMAINS after DELETE /api/devices/{id}: the credential is now unreachable and unrevokable")
		fmt.Println("REPRODUCED: deleting a device with the issuance channel off leaves its credential rows behind, and revoke answers success without doing anything")
		return
	}
	fmt.Println("NOT REPRODUCED")
	_ = logPath
}

// fakeCLI mimics easytier-cli's JSON for generate/revoke.
func fakeCLI(args []string) {
	joined := strings.Join(args, " ")
	switch {
	case strings.Contains(joined, "credential generate"):
		id := valueOf(args, "--credential-id")
		ttl, _ := strconv.ParseInt(valueOf(args, "--ttl"), 10, 64)
		base, _ := strconv.ParseInt(os.Getenv("PROBE_NOW"), 10, 64)
		logPath := os.Getenv("PROBE_CLI_LOG")
		if f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			fmt.Fprintf(f, "%s generate\n", id)
			f.Close()
		}
		raw, _ := json.Marshal(map[string]string{
			"credential_id":     id,
			"credential_secret": base64.StdEncoding.EncodeToString([]byte("orphan-secret-0123456789012345")),
			"expiry_unix":       strconv.FormatInt(base+ttl, 10),
		})
		fmt.Println(string(raw))
	case strings.Contains(joined, "credential revoke"):
		logPath := os.Getenv("PROBE_CLI_LOG")
		if f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			fmt.Fprintf(f, "%s revoke\n", strings.Join(args[5:], " "))
			f.Close()
		}
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

type env struct {
	app    *app.App
	srv    *httptest.Server
	client *http.Client
	clock  *clock
	bearer string
}

func open(dir string, withCLI bool) *env {
	clk := &clock{t: probeNow}
	opts := app.Options{Now: clk.Now}
	if withCLI {
		self, err := os.Executable()
		if err != nil {
			panic(err)
		}
		opts.CredentialCLI = self
		opts.CredentialRPC = "127.0.0.1:1"
	}
	instance, err := app.Open(dir, opts)
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
	publicID string
	id       string
	priv     ed25519.PrivateKey
}

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
	return testDevice{publicID: created.ID, id: "probe-device-" + name, priv: priv}
}

type heartbeatAnswer struct {
	Credential   string `json:"credential"`
	CredentialID string `json:"credential_id"`
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
	status, raw := rawCall(e, "POST", "/api/heartbeat", map[string]string{
		"deviceId":     d.id,
		"timestamp":    ts,
		"nonce":        nonce,
		"signature":    sig,
		"platform":     platform,
		"configHash":   configHash,
		"appHash":      "",
		"credentialId": credentialID,
	}, "")
	answer := heartbeatAnswer{}
	if status == http.StatusOK {
		if err := json.Unmarshal(raw, &answer); err != nil {
			panic(fmt.Sprintf("decode heartbeat: %v (%s)", err, raw))
		}
	}
	return answer
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

var _ = db.ErrCredentialNotFound
