// probe_admin_route_auth walks every management route with a forged bearer. It
// shows the code guards all of them (so the three routes the tests never try are
// a coverage gap, not an authentication hole).
package main

import (
	"fmt"
	"os"

	"mesh-server/code-reviews/2026-11-26T07-08-57+0800_b56687/scripts/probeutil"
)

func main() {
	dir, err := os.MkdirTemp("", "probe-auth-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	env, err := probeutil.New(dir)
	if err != nil {
		panic(err)
	}
	defer env.Close()
	env.Bearer = "not-a-valid-bearer"

	routes := []struct{ method, path string }{
		{"GET", "/api/admin/template"},
		{"GET", "/api/admin/devices"},
		{"POST", "/api/admin/devices"},
		{"GET", "/api/admin/devices/d_000000"},
		{"PUT", "/api/admin/devices/d_000000"},
		{"DELETE", "/api/admin/devices/d_000000"},
		{"POST", "/api/admin/devices/d_000000/token"},
		{"POST", "/api/admin/devices/d_000000/credential"},
		{"DELETE", "/api/admin/devices/d_000000/credential"},
		{"PUT", "/api/admin/devices/d_000000/group"},
		{"PUT", "/api/admin/layout"},
		{"GET", "/api/admin/groups"},
		{"POST", "/api/admin/groups"},
		{"PUT", "/api/admin/groups/1"},
		{"DELETE", "/api/admin/groups/1"},
		{"GET", "/api/admin/releases"},
		{"PUT", "/api/admin/releases"},
		{"DELETE", "/api/admin/releases/windows/amd64"},
	}
	for _, route := range routes {
		res, _ := env.Do(route.method, route.path, map[string]string{"name": "x", "config_toml": "a = 1\n"})
		fmt.Printf("%-6s %-38s forged bearer -> %d\n", route.method, route.path, res.StatusCode)
	}
}
