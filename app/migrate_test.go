package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The fixtures below mirror the shape the pre-SQLite releases wrote. They are
// marshalled here instead of being pasted as JSON text so the exact escape
// sequence of the stored file stays out of the test source while the field
// names remain an independent copy of the legacy format.
type legacyFixture struct {
	Devices  []legacyDeviceFixture  `json:"devices"`
	Settings *legacySettingsFixture `json:"settings"`
}

type legacyDeviceFixture struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	DeviceID       string `json:"device_id"`
	Hostname       string `json:"hostname"`
	Pubkey         string `json:"pubkey"`
	ConfigTOML     string `json:"config_toml"`
	ConfigHash     string `json:"config_hash"`
	Token          string `json:"token"`
	TokenExpiresAt int64  `json:"token_expires_at"`
	CreatedAt      int64  `json:"created_at"`
	LastSeenAt     int64  `json:"last_seen_at"`
	LastIP         string `json:"last_ip"`
	LastConfigHash string `json:"last_config_hash"`
	LastAppHash    string `json:"last_app_hash"`
}

type legacySettingsFixture struct {
	AppVersion  string `json:"app_version"`
	AppURL      string `json:"app_url"`
	AppHash     string `json:"app_hash"`
	AppNotes    string `json:"app_notes"`
	AppSource   string `json:"app_source"`
	AppFileName string `json:"app_file_name"`
	AppSize     int64  `json:"app_size"`
}

const (
	legacyFullHash = `e09238e331f0ab232d7dfd23a288098b81da3894bf0ee3e7a0d005bb85d4c7ae`
	legacyPubkey   = `KftCsfvGeFRrNNhvQgdcArYugmfCS0Fto1MrbnGz+FA=`
)

// legacyConfigJSON still carries no_auth: reading it must not fail.
const legacyConfigJSON = `{
  "listen": ":8088",
  "no_auth": true,
  "password_salt": "00000000000000000000000000000000",
  "password_hash": "0000000000000000000000000000000000000000000000000000000000000000"
}`

func legacyFixtureState() legacyFixture {
	return legacyFixture{
		Devices: []legacyDeviceFixture{
			{
				ID:         "d_3bf531",
				Name:       "test",
				DeviceID:   "0caff8dd605c27ab42395e84e35aa1e9",
				Hostname:   "DESKTOP-RQFD1KJ",
				Pubkey:     legacyPubkey,
				ConfigTOML: `hostname = "auto"`,
				ConfigHash: legacyFullHash,
				CreatedAt:  1795499549,
				LastSeenAt: 1795499874,
				LastIP:     "127.0.0.1",
			},
			{
				ID:             "d_1a2b3c",
				Name:           "spare",
				ConfigTOML:     `ipv4 = "10.126.126.9/24"`,
				ConfigHash:     "aaaaaaaabbbbbbbb",
				Token:          "etk_pending",
				TokenExpiresAt: 4102444800,
				CreatedAt:      1795400000,
				LastConfigHash: "ccccccccdddddddd",
				LastAppHash:    "eeeeeeeeffffffff",
			},
		},
		Settings: &legacySettingsFixture{
			AppVersion:  "2026.11.24-3",
			AppURL:      "/api/updates/2026.11.24-3-mesh-client.exe",
			AppHash:     "2495008aed91e3bf",
			AppNotes:    "修复窄屏布局",
			AppSource:   "upload",
			AppFileName: "2026.11.24-3-mesh-client.exe",
			AppSize:     6807712,
		},
	}
}

func writeLegacyData(t *testing.T, dir string) {
	t.Helper()
	encoded, err := json.MarshalIndent(legacyFixtureState(), "", "  ")
	if err != nil {
		t.Fatalf("marshal legacy state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(legacyConfigJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyStateIsImportedOnce(t *testing.T) {
	dir := t.TempDir()
	writeLegacyData(t, dir)

	instance, err := Open(dir, Options{})
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	devices, err := instance.ListDevices()
	if err != nil {
		t.Fatalf("ListDevices() error: %v", err)
	}
	if len(devices) != 2 {
		t.Fatalf("imported %d devices, want 2", len(devices))
	}
	first, found, err := instance.DeviceByID("d_3bf531")
	if err != nil || !found {
		t.Fatalf("DeviceByID(d_3bf531) = found %v, %v", found, err)
	}
	if first.DeviceID != "0caff8dd605c27ab42395e84e35aa1e9" {
		t.Fatalf("device_id = %q", first.DeviceID)
	}
	if first.Pubkey != legacyPubkey {
		t.Fatalf("pubkey = %q", first.Pubkey)
	}
	if first.ConfigHash != legacyFullHash {
		t.Fatalf("config_hash was rewritten: %q", first.ConfigHash)
	}
	if first.CreatedAt != 1795499549 || first.LastSeenAt != 1795499874 || first.LastIP != "127.0.0.1" {
		t.Fatalf("timestamps or ip lost: %+v", first)
	}
	second, found, err := instance.DeviceByID("d_1a2b3c")
	if err != nil || !found {
		t.Fatalf("DeviceByID(d_1a2b3c) = found %v, %v", found, err)
	}
	if second.Token != "etk_pending" || second.TokenExpiresAt != 4102444800 {
		t.Fatalf("pending token lost: %+v", second)
	}
	if second.LastConfigHash != "ccccccccdddddddd" || second.LastAppHash != "eeeeeeeeffffffff" {
		t.Fatalf("reported hashes lost: %+v", second)
	}
	release, found, err := instance.Release("windows/amd64")
	if err != nil || !found {
		t.Fatalf("Release(windows/amd64) = found %v, %v", found, err)
	}
	if release.Version != "2026.11.24-3" || release.Hash != "2495008aed91e3bf" || release.Size != 6807712 {
		t.Fatalf("release not imported: %+v", release)
	}
	if err := instance.Close(); err != nil {
		t.Fatal(err)
	}

	// A second start must neither duplicate the devices nor overwrite an edit.
	reopened, err := Open(dir, Options{})
	if err != nil {
		t.Fatalf("reopen error: %v", err)
	}
	if devices, err := reopened.ListDevices(); err != nil || len(devices) != 2 {
		t.Fatalf("devices after reopen = %d, %v; want 2", len(devices), err)
	}
	const editedTOML = `hostname = "edited"`
	if _, err := reopened.UpdateDevice("d_3bf531", "renamed", editedTOML); err != nil {
		t.Fatalf("UpdateDevice() error: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	third, err := Open(dir, Options{})
	if err != nil {
		t.Fatalf("third open error: %v", err)
	}
	defer third.Close()
	edited, found, err := third.DeviceByID("d_3bf531")
	if err != nil || !found {
		t.Fatalf("DeviceByID after third open = found %v, %v", found, err)
	}
	if edited.Name != "renamed" {
		t.Fatalf("admin edit was overwritten by the import: %+v", edited)
	}
	if devices, err := third.ListDevices(); err != nil || len(devices) != 2 {
		t.Fatalf("devices after third open = %d, %v; want 2", len(devices), err)
	}
}

func TestLegacyStateFileIsKept(t *testing.T) {
	dir := t.TempDir()
	writeLegacyData(t, dir)
	before, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	instance, err := Open(dir, Options{})
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer instance.Close()
	after, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatalf("state.json disappeared: %v", err)
	}
	if string(before) != string(after) {
		t.Fatal("state.json was modified by the migration")
	}
}

// TestLegacyConfigFileIsIgnored pins that DATA_DIR holds no configuration at
// all: an old config.json carrying no_auth and password fields is never read.
func TestLegacyConfigFileIsIgnored(t *testing.T) {
	dir := t.TempDir()
	writeLegacyData(t, dir)
	instance, err := Open(dir, Options{})
	if err != nil {
		t.Fatalf("Open() with a legacy config.json: %v", err)
	}
	defer instance.Close()
	if devices, err := instance.ListDevices(); err != nil || len(devices) != 2 {
		t.Fatalf("devices after migration = %d, %v; want 2", len(devices), err)
	}
}

func TestMissingStateFileIsNotAnError(t *testing.T) {
	dir := t.TempDir()
	instance, err := Open(dir, Options{})
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer instance.Close()
	if devices, err := instance.ListDevices(); err != nil || len(devices) != 0 {
		t.Fatalf("devices in a fresh directory = %d, %v", len(devices), err)
	}
}
