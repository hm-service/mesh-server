package app

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"mesh-server/core"
	"mesh-server/db"
)

func openTemp(t *testing.T) *App {
	t.Helper()
	instance, err := Open(t.TempDir(), Options{})
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	t.Cleanup(func() {
		if err := instance.Close(); err != nil {
			t.Logf("Close() error: %v", err)
		}
	})
	return instance
}

func TestCreateDeviceHashesConfig(t *testing.T) {
	a := openTemp(t)
	const toml = "hostname = \"auto\"\n"
	device, token, err := a.CreateDevice("laptop", toml)
	if err != nil {
		t.Fatalf("CreateDevice() error: %v", err)
	}
	if token == "" || device.Token != token {
		t.Fatalf("token = %q, stored = %q", token, device.Token)
	}
	if device.ConfigHash != core.Hash(toml) {
		t.Fatalf("config hash = %q, want %q", device.ConfigHash, core.Hash(toml))
	}
	if device.TokenExpiresAt != device.CreatedAt+int64(TokenTTL/time.Second) {
		t.Fatalf("token expiry = %d, created = %d", device.TokenExpiresAt, device.CreatedAt)
	}
	if _, _, err := a.CreateDevice("   ", toml); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("blank name = %v, want ErrInvalidInput", err)
	}
	if _, _, err := a.CreateDevice("name", "   "); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("blank config = %v, want ErrInvalidInput", err)
	}
}

func TestBindDeviceConsumesToken(t *testing.T) {
	a := openTemp(t)
	device, token, err := a.CreateDevice("laptop", "hostname = \"auto\"\n")
	if err != nil {
		t.Fatal(err)
	}
	bound, err := a.BindDevice(token, "dev-1", "DESKTOP", "pubkey", "127.0.0.1")
	if err != nil {
		t.Fatalf("BindDevice() error: %v", err)
	}
	if bound.PublicID != device.PublicID || bound.DeviceID != "dev-1" || bound.Pubkey != "pubkey" {
		t.Fatalf("bound = %+v", bound)
	}
	if bound.Token != "" || bound.TokenExpiresAt != 0 {
		t.Fatalf("token not consumed: %+v", bound)
	}
	if _, err := a.BindDevice(token, "dev-2", "OTHER", "pubkey", ""); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("second BindDevice() = %v, want ErrTokenInvalid", err)
	}
}

func TestBindDeviceRejectsExpiredToken(t *testing.T) {
	dir := t.TempDir()
	clock := struct{ now time.Time }{now: time.Unix(1_732_400_000, 0)}
	instance, err := Open(dir, Options{Now: func() time.Time { return clock.now }})
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	_, token, err := instance.CreateDevice("laptop", "a = 1\n")
	if err != nil {
		t.Fatal(err)
	}
	clock.now = clock.now.Add(TokenTTL + time.Second)
	if _, err := instance.BindDevice(token, "dev-1", "HOST", "pk", ""); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("expired token = %v, want ErrTokenInvalid", err)
	}
}

func TestReenrollRevokesPreviousIdentity(t *testing.T) {
	a := openTemp(t)
	device, token, err := a.CreateDevice("laptop", "a = 1\n")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.BindDevice(token, "dev-1", "HOST", "pk", ""); err != nil {
		t.Fatal(err)
	}
	reissued, err := a.IssueToken(device.PublicID)
	if err != nil {
		t.Fatalf("IssueToken() error: %v", err)
	}
	bound, err := a.BindDevice(reissued, "dev-2", "HOST", "pk2", "")
	if err != nil {
		t.Fatalf("re-enroll error: %v", err)
	}
	if bound.DeviceID != "dev-2" {
		t.Fatalf("device_id = %q, want dev-2", bound.DeviceID)
	}
	if !a.Revoked("dev-1") {
		t.Fatal("previous identity was not revoked")
	}
	if a.Revoked("dev-2") {
		t.Fatal("current identity marked revoked")
	}
}

func TestDeleteRevokesDeviceID(t *testing.T) {
	a := openTemp(t)
	device, token, err := a.CreateDevice("laptop", "a = 1\n")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.BindDevice(token, "dev-1", "HOST", "pk", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := a.DeleteDevice(device.PublicID); err != nil {
		t.Fatalf("DeleteDevice() error: %v", err)
	}
	if _, found, err := a.DeviceByID(device.PublicID); err != nil || found {
		t.Fatalf("deleted device still present: found %v, %v", found, err)
	}
	if !a.Revoked("dev-1") {
		t.Fatal("deleted device_id not marked revoked")
	}
	if _, err := a.DeleteDevice(device.PublicID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteDevice(unknown) = %v, want ErrNotFound", err)
	}
}

func TestUpdateDeviceConfig(t *testing.T) {
	a := openTemp(t)
	device, _, err := a.CreateDevice("laptop", "a = 1\n")
	if err != nil {
		t.Fatal(err)
	}
	const next = "a = 2\n"
	updated, err := a.UpdateDevice(device.PublicID, "renamed", next)
	if err != nil {
		t.Fatalf("UpdateDevice() error: %v", err)
	}
	if updated.Name != "renamed" || updated.ConfigHash != core.Hash(next) || updated.ConfigTOML != next {
		t.Fatalf("updated = %+v", updated)
	}
	if _, err := a.UpdateDevice("d_missing", "name", next); !errors.Is(err, ErrNotFound) {
		t.Fatalf("UpdateDevice(unknown) = %v, want ErrNotFound", err)
	}
}

func TestDeviceLimitApplies(t *testing.T) {
	a := openTemp(t)
	for i := range MaxDevices {
		if _, _, err := a.CreateDevice(fmt.Sprintf("dev-%03d", i), "a = 1\n"); err != nil {
			t.Fatalf("CreateDevice(%d) error: %v", i, err)
		}
	}
	if _, _, err := a.CreateDevice("overflow", "a = 1\n"); !errors.Is(err, ErrLimit) {
		t.Fatalf("CreateDevice() over limit = %v, want ErrLimit", err)
	}
}

func TestDerivedStates(t *testing.T) {
	now := time.Unix(1_732_400_000, 0)
	device := dbDevice("d_1", "dev-1", now.Unix())
	device.ConfigHash = "aaaa"
	device.LastConfigHash = "aaaa"
	if got := DeviceStatus(device, now); got != "online" {
		t.Fatalf("status = %q, want online", got)
	}
	if got := DeviceStatus(device, now.Add(OnlineWindow)); got != "offline" {
		t.Fatalf("status after window = %q, want offline", got)
	}
	pending := device
	pending.DeviceID = ""
	if got := DeviceStatus(pending, now); got != "pending" {
		t.Fatalf("status of unbound device = %q, want pending", got)
	}
	if got := ConfigState(device); got != "current" {
		t.Fatalf("config state = %q, want current", got)
	}
	device.LastConfigHash = "bbbb"
	if got := ConfigState(device); got != "stale" {
		t.Fatalf("config state = %q, want stale", got)
	}
	settings := dbSettings("v1", "aaaa")
	device.LastAppHash = "aaaa"
	if got := AppState(device, settings); got != "current" {
		t.Fatalf("app state = %q, want current", got)
	}
	device.LastAppHash = "cccc"
	if got := AppState(device, settings); got != "stale" {
		t.Fatalf("app state = %q, want stale", got)
	}
	if got := AppState(device, dbSettings("", "")); got != "current" {
		t.Fatalf("app state without a release = %q, want current", got)
	}
	if got := ReleaseVersion(dbSettings("", "")); got != "" {
		t.Fatalf("release version without a release = %q, want empty", got)
	}
}
func dbDevice(publicID, deviceID string, seenAt int64) db.Device {
	return db.Device{PublicID: publicID, DeviceID: deviceID, LastSeenAt: seenAt}
}

func dbSettings(version, hash string) db.Settings {
	return db.Settings{AppVersion: version, AppHash: hash}
}
