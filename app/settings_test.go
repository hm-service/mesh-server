package app

import (
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mesh-server/db"
)

// TestStateJSONIsIgnored pins the removal of the JSON import: DATA_DIR/state.json
// is not read at all, so a device or a release the admin deleted can never come
// back after a restart.
func TestStateJSONIsIgnored(t *testing.T) {
	dir := t.TempDir()
	const legacy = `
	{
	  "devices": [
	    {
	      "id": "d_old",
	      "name": "旧设备",
	      "device_id": "dev-old",
	      "pubkey": "pk",
	      "config_toml": "a = 1",
	      "config_hash": "aaaabbbbccccdddd"
	    }
	  ],
	  "settings": {
	    "app_version": "0.1.0",
	    "app_url": "https://example.invalid/old.tar.zst",
	    "app_hash": "0123456789abcdef"
	  }
	}`
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(legacy), 0o600); err != nil {
		t.Fatalf("write state.json: %v", err)
	}
	instance, err := Open(dir, Options{})
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	t.Cleanup(func() { _ = instance.Close() })

	devices, err := instance.ListDevices()
	if err != nil || len(devices) != 0 {
		t.Fatalf("devices = %+v, %v; want none", devices, err)
	}
	releases, err := instance.Releases()
	if err != nil || len(releases) != 0 {
		t.Fatalf("releases = %+v, %v; want none", releases, err)
	}
}

// TestPublishRefusesInternalAddresses pins the download guard: a published URL
// must not turn the control plane into an HTTP client for its own network.
func TestPublishRefusesInternalAddresses(t *testing.T) {
	a := openTemp(t)
	err := a.Publish(PublishInput{
		Platform: "windows/amd64",
		Launcher: &db.LauncherSlot{Version: "2026.11.25-9", URL: "http://127.0.0.1:9/mesh.exe"},
	})
	var rejected *ValidationError
	if !errors.As(err, &rejected) {
		t.Fatalf("publish of a loopback URL = %v, want a validation error", err)
	}
	if !strings.Contains(rejected.Message, "无法读取 launcherUrl") {
		t.Fatalf("message = %q, want the launcher slot named", rejected.Message)
	}
	if _, found, err := a.Release("windows/amd64"); err != nil || found {
		t.Fatalf("a rejected publish stored a slot: found %v, %v", found, err)
	}
}

// TestDialGuard covers the addresses the download client must not reach, and
// the explicit prefixes it may reach anyway.
func TestDialGuard(t *testing.T) {
	guard := dialGuard(nil)
	for _, address := range []string{
		"127.0.0.1:80", "127.1.2.3:80", "[::1]:80",
		"10.1.2.3:80", "172.16.0.1:80", "192.168.1.1:80",
		"169.254.169.254:80", "0.0.0.0:80", "[ff02::1]:80",
	} {
		if err := guard("tcp", address, nil); err == nil {
			t.Fatalf("guard(%q) allowed an internal address", address)
		}
	}
	for _, address := range []string{"8.8.8.8:53", "1.1.1.1:443", "[2606:4700:4700::1111]:443"} {
		if err := guard("tcp", address, nil); err != nil {
			t.Fatalf("guard(%q) rejected a public address: %v", address, err)
		}
	}

	allowed := dialGuard([]netip.Prefix{netip.MustParsePrefix("10.126.126.1/32")})
	if err := allowed("tcp", "10.126.126.1:18080", nil); err != nil {
		t.Fatalf("guard rejected the configured prefix: %v", err)
	}
	if err := allowed("tcp", "10.126.126.2:18080", nil); err == nil {
		t.Fatal("guard allowed a host outside the configured prefix")
	}
}
