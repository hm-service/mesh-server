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
	dir, err := os.MkdirTemp("", "probe-")
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
	var login struct {
		AccessToken string `json:"access_token"`
	}
	mustCall(client, srv.URL, "POST", "/api/auth", map[string]string{"token": probeToken}, "", &login)

	// Nothing is published for any platform. A device that was just created has
	// never reported a platform.
	var created struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	mustCall(client, srv.URL, "POST", "/api/devices", map[string]string{
		"name":        "pending-device",
		"config_toml": "dhcp = true\n",
	}, login.AccessToken, &created)

	var list []struct {
		ID           string `json:"id"`
		Status       string `json:"status"`
		LastPlatform string `json:"last_platform"`
		AppState     string `json:"app_state"`
		AppVersion   string `json:"app_version"`
	}
	mustCall(client, srv.URL, "GET", "/api/devices", nil, login.AccessToken, &list)
	for _, d := range list {
		fmt.Printf("DEVICE id=%s status=%s last_platform=%q app_state=%s app_version=%q\n",
			d.ID, d.Status, d.LastPlatform, d.AppState, d.AppVersion)
	}
	if len(list) == 1 && list[0].AppState != "current" {
		fmt.Println("REPRODUCED: nothing is published, CONTRACT.md 1.4 says app_state is current, server says " + list[0].AppState)
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
