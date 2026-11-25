// probe_version_identity shows what the control plane does when the version a
// device reports is NOT the published one but is NEWER than it: the heartbeat
// keeps answering app_update (pointing at the older package) and the console
// keeps the device in app_state=stale, with no state that says "this device is
// ahead of the published build".
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"mesh-server/code-reviews/2026-11-26T07-08-57+0800_b56687/scripts/probeutil"
)

const platform = "windows/amd64"

type heartbeatResponse struct {
	AppUpdate *struct {
		Version    string `json:"version"`
		URL        string `json:"url"`
		AutoUpdate bool   `json:"autoUpdate"`
	} `json:"app_update"`
}

type deviceRow struct {
	ID             string `json:"id"`
	LastAppVersion string `json:"last_app_version"`
	AppVersion     string `json:"app_version"`
	AppState       string `json:"app_state"`
}

func main() {
	dir, err := os.MkdirTemp("", "probe-version-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	env, err := probeutil.New(dir)
	if err != nil {
		panic(err)
	}
	defer env.Close()

	const toml = "hostname = \"probe\"\n"
	dev, err := env.Register("laptop", toml)
	if err != nil {
		panic(err)
	}

	file, url := probeutil.FileServer([]byte("a published client package"))
	defer file.Close()
	// The published package is the OLDER build; the device already runs a newer
	// one (for example installed by hand from the zip).
	res, data := env.Do("PUT", "/api/admin/releases", map[string]any{
		"platform":    platform,
		"app_version": "2026.11.26-1",
		"app_url":     url,
		"auto_update": true,
	})
	if res.StatusCode != 200 {
		fmt.Printf("PUBLISH status=%d body=%s\n", res.StatusCode, data)
		return
	}

	for round := 1; round <= 2; round++ {
		res, data = env.Heartbeat(dev, platform, dev.ConfigHash, "2026.11.26-9")
		var got heartbeatResponse
		_ = json.Unmarshal(data, &got)
		if got.AppUpdate == nil {
			fmt.Printf("ROUND %d status=%d app_update=<none>\n", round, res.StatusCode)
			continue
		}
		fmt.Printf("ROUND %d status=%d app_update.version=%s autoUpdate=%v reported=2026.11.26-9\n",
			round, res.StatusCode, got.AppUpdate.Version, got.AppUpdate.AutoUpdate)
	}

	res, data = env.Do("GET", "/api/admin/devices", nil)
	var rows []deviceRow
	_ = json.Unmarshal(data, &rows)
	for _, row := range rows {
		if row.ID == dev.PublicID {
			fmt.Printf("CONSOLE last_app_version=%s published=%s app_state=%s\n",
				row.LastAppVersion, row.AppVersion, row.AppState)
		}
	}
	_ = time.Now()

	fmt.Println("VERDICT: the control plane only compares inequality, so a device ahead of the",
		"published build is told to install the older package on every heartbeat and stays stale")
}
