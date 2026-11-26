// probe_static_index shows how the console is served: the entry document is
// cached for a year when it is requested by name, and an unknown asset answers
// 200 with the entry document instead of 404.
package main

import (
	"fmt"
	"net/http"
	"os"
	"testing/fstest"

	"mesh-server/code-reviews/2026-11-27T05-45-47+0800_a6b688/scripts/probeutil"
)

func main() {
	static := fstest.MapFS{
		"index.html":      &fstest.MapFile{Data: []byte("<!doctype html><div id=app></div>")},
		"assets/app-1.js": &fstest.MapFile{Data: []byte("console.log(1)")},
	}
	dir, err := os.MkdirTemp("", "probe-static-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	env, err := probeutil.New(dir, static)
	if err != nil {
		panic(err)
	}
	defer env.Close()

	for _, path := range []string{
		"/admin",
		"/admin/index.html",
		"/admin/assets/app-1.js",
		"/admin/assets/missing-2.js",
	} {
		res, body := env.Do(http.MethodGet, path, nil)
		fmt.Printf("%-30s status=%d cache=%q content-type=%q bytes=%d\n",
			path, res.StatusCode, res.Header.Get("Cache-Control"), res.Header.Get("Content-Type"), len(body))
	}
}
