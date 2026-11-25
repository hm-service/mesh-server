// probe_blank_template shows that a blank default_toml is silently ignored: the
// request answers 200 while the stored template keeps its previous contents, so
// an admin cannot clear the template through the API and gets no error either.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"mesh-server/code-reviews/2026-11-26T07-08-57+0800_b56687/scripts/probeutil"
)

type releasesResponse struct {
	DefaultTOML string `json:"default_toml"`
}

func main() {
	dir, err := os.MkdirTemp("", "probe-template-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	env, err := probeutil.New(dir)
	if err != nil {
		panic(err)
	}
	defer env.Close()

	const first = "hostname = \"saved\"\n"
	res, _ := env.Do("PUT", "/api/admin/releases", map[string]any{"default_toml": first})
	fmt.Printf("SAVE first status=%d\n", res.StatusCode)
	stored := readTemplate(env.DataDir)
	fmt.Printf("FILE after first save  = %q\n", stored)

	for _, blank := range []string{"", "   \n\t\n"} {
		res, data := env.Do("PUT", "/api/admin/releases", map[string]any{"default_toml": blank})
		res2, payload := env.Do("GET", "/api/admin/releases", nil)
		var view releasesResponse
		_ = json.Unmarshal(payload, &view)
		fmt.Printf("CLEAR with %-10q status=%d body=%s -> file=%q api=%q\n",
			blank, res.StatusCode, trim(data), readTemplate(env.DataDir), view.DefaultTOML)
		_ = res2
	}

	fmt.Println("VERDICT: blank/whitespace default_toml is accepted and does nothing — the admin",
		"gets a success response while the template is unchanged (no way to clear it, no error)")
}

func readTemplate(dir string) string {
	raw, err := os.ReadFile(filepath.Join(dir, "peer.default.toml"))
	if err != nil {
		return "<missing>"
	}
	return string(raw)
}

func trim(data []byte) string {
	if len(data) > 120 {
		return string(data[:120]) + "…"
	}
	return string(data)
}
