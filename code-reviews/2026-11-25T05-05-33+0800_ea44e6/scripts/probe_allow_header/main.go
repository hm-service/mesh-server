// Probe: the 405 fallback always claims "Allow: GET, HEAD", whatever methods the
// path really supports, so the header contradicts the routes (POST /api/auth,
// PUT /api/admin/releases, ...).
//
// Run with:
//
//	go run ./code-reviews/2026-11-25T05-05-33+0800_ea44e6/scripts/probe_allow_header
package main

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"time"

	"mesh-server/api"
	"mesh-server/app"
	"mesh-server/core"
)

func main() {
	dir, err := os.MkdirTemp("", "probe-allow-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	instance, err := app.Open(dir, app.Options{Now: time.Now})
	if err != nil {
		panic(err)
	}
	defer instance.Close()
	hash, err := core.NewTokenHash("probe_token_0123456789")
	if err != nil {
		panic(err)
	}
	srv := httptest.NewServer(api.New(api.Options{
		App:        instance,
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		TokenHash:  hash,
		SessionKey: []byte("probe_session_key_0123456789abcdef"),
	}).Handler())
	defer srv.Close()
	client := srv.Client()

	// path, real methods, method used by the probe
	cases := [][3]string{
		{"/api/auth", "POST only", "GET"},
		{"/api/admin/releases", "GET, PUT", "POST"},
		{"/api/devices", "GET, POST", "DELETE"},
		{"/api/layout", "PUT only", "GET"},
	}
	for _, c := range cases {
		req, err := http.NewRequest(c[2], srv.URL+c[0], nil)
		if err != nil {
			panic(err)
		}
		res, err := client.Do(req)
		if err != nil {
			panic(err)
		}
		res.Body.Close()
		fmt.Printf("%-24s real methods: %-10s %s -> %d Allow=%q\n", c[0], c[1], c[2], res.StatusCode, res.Header.Get("Allow"))
	}
}
