// probe_device_create_autoupdate shows that POST /api/admin/devices accepts a
// device-level autoUpdate and silently drops it: the answer is 200 and the
// stored device follows the package. The release publish path rejects an input
// field it no longer takes (appVersion) instead of ignoring it.
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"mesh-server/code-reviews/2026-11-27T05-45-47+0800_a6b688/scripts/probeutil"
)

func main() {
	dir, err := os.MkdirTemp("", "probe-create-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	env, err := probeutil.New(dir, nil)
	if err != nil {
		panic(err)
	}
	defer env.Close()

	res, data := env.Do("POST", "/api/admin/devices", map[string]any{
		"name":       "laptop",
		"configToml": "hostname = \"auto\"\n",
		"autoUpdate": true,
	})
	fmt.Printf("CREATE status=%d body=%s\n", res.StatusCode, data)
	if res.StatusCode != http.StatusOK {
		return
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(data, &created); err != nil {
		panic(err)
	}
	res, data = env.Do("GET", "/api/admin/devices/"+created.ID, nil)
	var detail struct {
		AutoUpdate *bool `json:"autoUpdate"`
	}
	if err := json.Unmarshal(data, &detail); err != nil {
		panic(err)
	}
	fmt.Printf("DETAIL status=%d autoUpdate=%v\n", res.StatusCode, detail.AutoUpdate)

	// The same field on the release publish path is refused, not ignored.
	res, data = env.Do("PUT", "/api/admin/releases", map[string]any{"platform": "windows/amd64", "appVersion": "2026.11.26-1"})
	fmt.Printf("PUBLISH appVersion status=%d body=%s\n", res.StatusCode, data)
}
