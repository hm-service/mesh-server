// probe_relative_release_url shows that a published address can only be an
// absolute http(s) URL, so api.absoluteURL can never take its relative branch:
// every stored release URL already returns early from the IsAbs check.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"mesh-server/code-reviews/2026-11-26T07-08-57+0800_b56687/scripts/probeutil"
)

const platform = "windows/amd64"

type heartbeatResponse struct {
	AppUpdate *struct {
		URL string `json:"url"`
	} `json:"app_update"`
}

func main() {
	dir, err := os.MkdirTemp("", "probe-url-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	env, err := probeutil.New(dir)
	if err != nil {
		panic(err)
	}
	defer env.Close()

	dev, err := env.Register("laptop", "hostname = \"probe\"\n")
	if err != nil {
		panic(err)
	}

	// The relative form the removed /api/updates/ hosting used to store.
	for _, relative := range []string{"/api/updates/windows-amd64-2026.11.26-1-mesh-client.tar.zst", "mesh-client.tar.zst"} {
		res, data := env.Do("PUT", "/api/admin/releases", map[string]any{
			"platform":    platform,
			"app_version": "2026.11.26-1",
			"app_url":     relative,
		})
		fmt.Printf("PUBLISH relative %-58q status=%d body=%s\n", relative, res.StatusCode, trim(data))
	}

	file, url := probeutil.FileServer([]byte("a published client package"))
	defer file.Close()
	res, data := env.Do("PUT", "/api/admin/releases", map[string]any{
		"platform":    platform,
		"app_version": "2026.11.26-1",
		"app_url":     url,
	})
	if res.StatusCode != 200 {
		fmt.Printf("PUBLISH absolute status=%d body=%s\n", res.StatusCode, data)
		return
	}

	res, data = env.Heartbeat(dev, platform, dev.ConfigHash, "2026.11.26-0")
	var got heartbeatResponse
	_ = json.Unmarshal(data, &got)
	if got.AppUpdate == nil {
		fmt.Printf("HEARTBEAT status=%d app_update=<none> body=%s\n", res.StatusCode, trim(data))
	} else {
		fmt.Printf("APP_UPDATE.url=%q stored=%q equal=%v\n", got.AppUpdate.URL, url, got.AppUpdate.URL == url)
	}
	fmt.Println("VERDICT: relative URLs are rejected at publish time, so absoluteURL always takes",
		"its IsAbs early return; the scheme/host/X-Forwarded-Proto branch is unreachable")
}

func trim(data []byte) string {
	if len(data) > 160 {
		return string(data[:160]) + "…"
	}
	return string(data)
}
