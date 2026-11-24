// Probe: PUT /api/admin/releases publishes the client slot before the launcher
// slot is validated, so a request that answers 400 can still change what is
// published.
//
// Run with:
//
//	go run ./code-reviews/2026-11-25T05-05-33+0800_ea44e6/scripts/probe_publish_atomicity
package main

import (
	"bytes"
	"encoding/json"
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

const probeToken = "probe_token_0123456789"

func main() {
	dir, err := os.MkdirTemp("", "probe-publish-")
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
		App:        instance,
		Logger:     logger,
		TokenHash:  hash,
		SessionKey: []byte("probe_session_key_0123456789abcdef"),
	}).Handler())
	defer srv.Close()

	client := srv.Client()
	client.Timeout = 30 * time.Second

	// The artifact server stands in for the downloaded client package.
	artifact := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("client-package-bytes"))
	}))
	defer artifact.Close()

	var login struct {
		AccessToken string `json:"access_token"`
	}
	mustCall(client, srv.URL, "POST", "/api/auth", map[string]string{"token": probeToken}, "", &login)

	// The client slot points at a reachable file; the launcher slot points at a
	// port nothing listens on, so its download is rejected.
	body := map[string]string{
		"platform":         "windows/amd64",
		"app_version":      "2026.11.25-1",
		"app_url":          artifact.URL + "/client.tar.zst",
		"launcher_version": "2026.11.25-2",
		"launcher_url":     "http://127.0.0.1:9/launcher.exe",
	}
	status, raw := rawCall(client, srv.URL, "PUT", "/api/admin/releases", body, login.AccessToken)
	fmt.Printf("PUBLISH status=%d body=%s\n", status, string(raw))

	var state struct {
		Releases []struct {
			Platform        string `json:"platform"`
			AppVersion      string `json:"app_version"`
			AppURL          string `json:"app_url"`
			AppHash         string `json:"app_hash"`
			LauncherVersion string `json:"launcher_version"`
		} `json:"releases"`
	}
	mustCall(client, srv.URL, "GET", "/api/admin/releases", nil, login.AccessToken, &state)
	for _, r := range state.Releases {
		fmt.Printf("AFTER platform=%s app_version=%q app_hash=%q launcher_version=%q\n",
			r.Platform, r.AppVersion, r.AppHash, r.LauncherVersion)
	}
	if status == http.StatusBadRequest && len(state.Releases) == 1 && state.Releases[0].AppVersion != "" {
		fmt.Println("REPRODUCED: answered 400 but the client slot is published")
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
