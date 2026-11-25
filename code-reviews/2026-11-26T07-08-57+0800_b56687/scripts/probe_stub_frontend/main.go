// probe_stub_frontend reproduces `just frontend-stub` + `just run`: the embedded
// build has no index.html, so /admin answers 404. The justfile comment also
// claims /readyz answers 503 in that state; it does not — readiness only touches
// DATA_DIR and the database, so it is 200.
package main

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing/fstest"
	"time"

	"mesh-server/api"
	"mesh-server/app"
	"mesh-server/core"
)

func main() {
	dir, err := os.MkdirTemp("", "probe-stub-")
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
	// frontend/dist holding only STUB, exactly what `just frontend-stub` creates.
	stub := fstest.MapFS{"STUB": &fstest.MapFile{Data: []byte{}}}
	server := httptest.NewServer(api.New(api.Options{
		App:        instance,
		Static:     stub,
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		TokenHash:  hash,
		SessionKey: []byte("probe_session_key_0123456789abcdef"),
	}).Handler())
	defer server.Close()

	for _, path := range []string{"/admin", "/readyz", "/livez"} {
		res, err := http.Get(server.URL + path)
		if err != nil {
			panic(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		fmt.Printf("GET %-8s status=%d body=%s\n", path, res.StatusCode, trim(body))
	}
	fmt.Println("VERDICT: /admin is 404 as documented, /readyz is 200 — the justfile comment",
		"claiming a stub build answers 503 on /readyz no longer describes the code")
}

func trim(data []byte) string {
	if len(data) > 100 {
		return string(data[:100]) + "…"
	}
	return string(data)
}
