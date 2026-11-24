// Probe: an unauthenticated client that sends the headers of /api/heartbeat and
// then never sends the body keeps the request handler (and one api gate slot)
// forever, because http.Server has no ReadTimeout and ReadBody waits for the
// body. The gate is shared with every /api/* admin route, so a handful of such
// connections turn the whole console API into 503 "busy".
//
// The gate is set to one slot here so the reproduction needs a single
// connection; the deployment default is MAX_API_CONCURRENCY=128.
//
// The verdict reads the status observed while the connection is still open: the
// console answering 200 there means the device surface no longer shares its gate.
//
// Run with:
//
//	go run ./code-reviews/2026-11-25T05-05-33+0800_ea44e6/scripts/probe_slow_body
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"time"

	"mesh-server/api"
	"mesh-server/app"
	"mesh-server/core"
)

const probeToken = "probe_token_0123456789"

func main() {
	dir, err := os.MkdirTemp("", "probe-slow-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	instance, err := app.Open(dir, app.Options{Now: time.Now})
	if err != nil {
		panic(err)
	}
	defer instance.Close()
	hash, err := core.NewTokenHash(probeToken)
	if err != nil {
		panic(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(api.New(api.Options{
		App:               instance,
		Logger:            logger,
		TokenHash:         hash,
		SessionKey:        []byte("probe_session_key_0123456789abcdef"),
		MaxAPIConcurrency: 1,
	}).Handler())
	defer srv.Close()
	client := srv.Client()
	client.Timeout = 10 * time.Second

	var login struct {
		AccessToken string `json:"access_token"`
	}
	mustCall(client, srv.URL, "POST", "/api/auth", map[string]string{"token": probeToken}, "", &login)

	addr := srv.Listener.Addr().String()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		panic(err)
	}
	// Header only: the handler is dispatched and blocks reading the body.
	fmt.Fprintf(conn, "POST /api/heartbeat HTTP/1.1\r\nHost: probe\r\nContent-Type: application/json\r\nContent-Length: 200\r\n\r\n")
	time.Sleep(300 * time.Millisecond)

	blockedStatus, blockedBody := rawCall(client, srv.URL, "GET", "/api/devices", nil, login.AccessToken)
	fmt.Printf("WHILE-BLOCKED GET /api/devices status=%d body=%s\n", blockedStatus, string(blockedBody))

	_ = conn.Close()
	time.Sleep(200 * time.Millisecond)
	status, raw := rawCall(client, srv.URL, "GET", "/api/devices", nil, login.AccessToken)
	fmt.Printf("AFTER-CLOSE   GET /api/devices status=%d body=%s\n", status, string(raw))

	if blockedStatus == http.StatusServiceUnavailable {
		fmt.Println("REPRODUCED: one idle unauthenticated request made the console API answer 503 while it was open")
		return
	}
	fmt.Println("NOT REPRODUCED")
}

func rawCall(client *http.Client, base, method, path string, body any, bearer string) (int, []byte) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			panic(err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, base+path, reader)
	if err != nil {
		panic(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	res, err := client.Do(req)
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

func mustCall(client *http.Client, base, method, path string, body any, bearer string, out any) {
	status, raw := rawCall(client, base, method, path, body, bearer)
	if status != http.StatusOK {
		panic(fmt.Sprintf("%s %s = %d: %s", method, path, status, raw))
	}
	if err := json.Unmarshal(raw, out); err != nil {
		panic(fmt.Sprintf("%s %s decode: %v (%s)", method, path, err, raw))
	}
}
