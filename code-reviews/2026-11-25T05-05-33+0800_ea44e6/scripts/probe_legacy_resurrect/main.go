// Probe: DATA_DIR/state.json is re-imported on every start, so anything the
// admin deleted comes back after a restart, and a launcher-only release row is
// overwritten by the legacy client package (which also blanks the launcher).
//
// Run with:
//
//	go run ./code-reviews/2026-11-25T05-05-33+0800_ea44e6/scripts/probe_legacy_resurrect
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"time"

	"mesh-server/app"
	"mesh-server/db"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	dir, err := os.MkdirTemp("", "probe-legacy-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)

	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	state := map[string]any{
		"devices": []map[string]any{{
			"id":           "d_abc123",
			"name":         "legacy-device",
			"device_id":    "legacy-device-id",
			"hostname":     "legacy-host",
			"pubkey":       base64.StdEncoding.EncodeToString(pub),
			"config_toml":  "dhcp = true\n",
			"config_hash":  "aaaabbbbccccdddd",
			"created_at":   1732400000,
			"last_seen_at": 1732400000,
		}},
		"settings": map[string]any{
			"app_version": "2026.11.1",
			"app_url":     "https://example.invalid/old-client.tar.zst",
			"app_hash":    "0123456789abcdef",
			"app_notes":   "legacy package",
		},
	}
	raw, err := json.Marshal(state)
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), raw, 0o600); err != nil {
		panic(err)
	}

	// First start: state.json is not read at all any more, so nothing is imported.
	a := open(dir)
	devices, err := a.ListDevices()
	if err != nil {
		panic(err)
	}
	rel, _, err := a.Release("windows/amd64")
	if err != nil {
		panic(err)
	}
	fmt.Printf("AFTER FIRST START  devices=%d release_version=%q\n", len(devices), rel.Version)

	// The admin revokes the device and deletes the published platform.
	if _, err := a.DeleteDevice(context.Background(), "d_abc123"); err != nil && !errors.Is(err, app.ErrNotFound) {
		panic(err)
	}
	if err := a.ClearRelease("windows/amd64"); err != nil {
		panic(err)
	}
	devices, _ = a.ListDevices()
	rel, found, _ := a.Release("windows/amd64")
	fmt.Printf("AFTER DELETE      devices=%d release_found=%v version=%q\n", len(devices), found, rel.Version)
	if err := a.Close(); err != nil {
		panic(err)
	}

	// Restart.
	a = open(dir)
	devices, _ = a.ListDevices()
	rel, found, _ = a.Release("windows/amd64")
	fmt.Printf("AFTER RESTART     devices=%d release_found=%v version=%q\n", len(devices), found, rel.Version)
	deviceResurrected := len(devices) == 1
	releaseResurrected := found

	// Publishing only a launcher, then restarting: the launcher slot is wiped
	// and the legacy client package comes back.
	artifact := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("launcher-bytes"))
	}))
	defer artifact.Close()
	if err := a.ClearRelease("windows/amd64"); err != nil {
		panic(err)
	}
	if err := a.Publish(app.PublishInput{
		Platform: "windows/amd64",
		Launcher: &db.LauncherSlot{Version: "2026.11.25-9", URL: artifact.URL + "/mesh.exe"},
	}); err != nil {
		panic(err)
	}
	rel, _, _ = a.Release("windows/amd64")
	fmt.Printf("LAUNCHER PUBLISHED version=%q launcher_version=%q launcher_url=%q\n", rel.Version, rel.LauncherVersion, rel.LauncherURL)
	if err := a.Close(); err != nil {
		panic(err)
	}

	a = open(dir)
	defer a.Close()
	rel, _, _ = a.Release("windows/amd64")
	fmt.Printf("AFTER RESTART     version=%q launcher_version=%q launcher_url=%q\n", rel.Version, rel.LauncherVersion, rel.LauncherURL)
	launcherWiped := rel.LauncherVersion == "" && rel.Version != ""

	if deviceResurrected && releaseResurrected && launcherWiped {
		fmt.Println("REPRODUCED: the deleted device and the deleted release came back, and the launcher-only release was overwritten")
		return
	}
	fmt.Printf("NOT FULLY REPRODUCED deviceResurrected=%v releaseResurrected=%v launcherWiped=%v\n",
		deviceResurrected, releaseResurrected, launcherWiped)
}

func open(dir string) *app.App {
	// The artifact is served from loopback, which the hardened download client
	// refuses by design, so this probe brings its own client.
	instance, err := app.Open(dir, app.Options{
		Now:         time.Now,
		FetchClient: &http.Client{Timeout: 10 * time.Second},
	})
	if err != nil {
		panic(err)
	}
	return instance
}
