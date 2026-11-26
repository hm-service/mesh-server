// probe_admin_route_auth walks every admin route registered in api/api.go with a
// forged bearer and reports the status, which is the evidence for the previous
// round's F6 (route coverage) on this commit.
package main

import (
	"bufio"
	"fmt"
	"net/http"
	"os"
	"strings"

	"mesh-server/code-reviews/2026-11-27T05-45-47+0800_a6b688/scripts/probeutil"
)

func main() {
	dir, err := os.MkdirTemp("", "probe-auth-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	env, err := probeutil.New(dir, nil)
	if err != nil {
		panic(err)
	}
	defer env.Close()

	routes := [][2]string{
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
	forged := "Bearer " + "forged.credential"
	bad := 0
	for _, route := range routes {
		req, err := http.NewRequest(route[0], env.Server.URL+route[1], strings.NewReader(`{"name":"x","configToml":"a = 1\n"}`))
		if err != nil {
			panic(err)
		}
		req.Header.Set("Authorization", forged)
		res, err := env.Client.Do(req)
		if err != nil {
			panic(err)
		}
		_, _ = bufio.NewReader(res.Body).ReadString('\n')
		res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized {
			bad++
			fmt.Printf("%-6s %-40s status=%d WANT 401\n", route[0], route[1], res.StatusCode)
		}
	}
	fmt.Printf("routes=%d not-401=%d\n", len(routes), bad)
}
