package app

import (
	"context"
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
	bound, err := a.BindDevice(context.Background(), token, "dev-1", "DESKTOP", "pubkey", "127.0.0.1")
	if err != nil {
		t.Fatalf("BindDevice() error: %v", err)
	}
	if bound.PublicID != device.PublicID || bound.DeviceID != "dev-1" || bound.Pubkey != "pubkey" {
		t.Fatalf("bound = %+v", bound)
	}
	if bound.Token != "" || bound.TokenExpiresAt != 0 {
		t.Fatalf("token not consumed: %+v", bound)
	}
	if _, err := a.BindDevice(context.Background(), token, "dev-2", "OTHER", "pubkey", ""); !errors.Is(err, ErrTokenInvalid) {
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
	if _, err := instance.BindDevice(context.Background(), token, "dev-1", "HOST", "pk", ""); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("expired token = %v, want ErrTokenInvalid", err)
	}
}

func TestReenrollRevokesPreviousIdentity(t *testing.T) {
	a := openTemp(t)
	device, token, err := a.CreateDevice("laptop", "a = 1\n")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.BindDevice(context.Background(), token, "dev-1", "HOST", "pk", ""); err != nil {
		t.Fatal(err)
	}
	reissued, err := a.IssueToken(device.PublicID)
	if err != nil {
		t.Fatalf("IssueToken() error: %v", err)
	}
	bound, err := a.BindDevice(context.Background(), reissued, "dev-2", "HOST", "pk2", "")
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

// TestRebindClearsPreviousObservations pins Q10: a rebind replaces the identity
// and the observations that identity reported, in the same update. Slot-owned
// attributes (name, group, configuration) survive; a rebind to the same
// device_id is not an identity change and keeps its observations.
func TestRebindClearsPreviousObservations(t *testing.T) {
	a := openTemp(t)
	device, token, err := a.CreateDevice("laptop", "a = 1\n")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.BindDevice(context.Background(), token, "dev-1", "OLD-HOST", "pk1", "10.0.0.1"); err != nil {
		t.Fatalf("first bind: %v", err)
	}
	if err := a.RecordHeartbeat(device.PublicID, "old-hash", "2026.11.26-1", "windows/amd64"); err != nil {
		t.Fatalf("RecordHeartbeat() error: %v", err)
	}
	if _, err := a.UpdateDevice(device.PublicID, "renamed", "a = 2\n", AutoUpdateChoice{}); err != nil {
		t.Fatalf("UpdateDevice() error: %v", err)
	}
	group, err := a.CreateGroup("办公")
	if err != nil {
		t.Fatalf("CreateGroup() error: %v", err)
	}
	if err := a.SetDeviceGroup(device.PublicID, group.ID); err != nil {
		t.Fatalf("SetDeviceGroup() error: %v", err)
	}

	reissued, err := a.IssueToken(device.PublicID)
	if err != nil {
		t.Fatalf("IssueToken() error: %v", err)
	}
	bound, err := a.BindDevice(context.Background(), reissued, "dev-2", "NEW-HOST", "pk2", "")
	if err != nil {
		t.Fatalf("rebind error: %v", err)
	}
	if bound.LastConfigHash != "" || bound.LastAppVersion != "" || bound.LastPlatform != "" {
		t.Fatalf("observations of the previous identity survived the rebind: %+v", bound)
	}
	if bound.Name != "renamed" || bound.ConfigTOML != "a = 2\n" || bound.ConfigHash != core.Hash("a = 2\n") || bound.GroupID != group.ID {
		t.Fatalf("slot attributes were lost by the rebind: %+v", bound)
	}
	if bound.DeviceID != "dev-2" || bound.Hostname != "NEW-HOST" || bound.Pubkey != "pk2" {
		t.Fatalf("identity not replaced: %+v", bound)
	}

	// 同一身份重新注册不是换绑：观测值仍属于它。
	if err := a.RecordHeartbeat(device.PublicID, "new-hash", "2026.11.27-1", "linux/amd64"); err != nil {
		t.Fatalf("RecordHeartbeat() error: %v", err)
	}
	again, err := a.IssueToken(device.PublicID)
	if err != nil {
		t.Fatalf("IssueToken() error: %v", err)
	}
	same, err := a.BindDevice(context.Background(), again, "dev-2", "NEW-HOST", "pk2", "")
	if err != nil {
		t.Fatalf("rebind to the same identity: %v", err)
	}
	if same.LastConfigHash != "new-hash" || same.LastAppVersion != "2026.11.27-1" || same.LastPlatform != "linux/amd64" {
		t.Fatalf("observations cleared although the identity did not change: %+v", same)
	}
}

func TestDeleteRevokesDeviceID(t *testing.T) {
	a := openTemp(t)
	device, token, err := a.CreateDevice("laptop", "a = 1\n")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.BindDevice(context.Background(), token, "dev-1", "HOST", "pk", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := a.DeleteDevice(context.Background(), device.PublicID); err != nil {
		t.Fatalf("DeleteDevice() error: %v", err)
	}
	if _, found, err := a.DeviceByID(device.PublicID); err != nil || found {
		t.Fatalf("deleted device still present: found %v, %v", found, err)
	}
	if !a.Revoked("dev-1") {
		t.Fatal("deleted device_id not marked revoked")
	}
	if _, err := a.DeleteDevice(context.Background(), device.PublicID); !errors.Is(err, ErrNotFound) {
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
	updated, err := a.UpdateDevice(device.PublicID, "renamed", next, AutoUpdateChoice{})
	if err != nil {
		t.Fatalf("UpdateDevice() error: %v", err)
	}
	if updated.Name != "renamed" || updated.ConfigHash != core.Hash(next) || updated.ConfigTOML != next {
		t.Fatalf("updated = %+v", updated)
	}
	if _, err := a.UpdateDevice("d_missing", "name", next, AutoUpdateChoice{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("UpdateDevice(unknown) = %v, want ErrNotFound", err)
	}
}

// TestUpdateDeviceAutoUpdate pins the three states on the stored row: the field
// missing leaves the column alone, null puts it back to following the package,
// and true/false force the device's own answer.
func TestUpdateDeviceAutoUpdate(t *testing.T) {
	a := openTemp(t)
	device, _, err := a.CreateDevice("laptop", "a = 1\n")
	if err != nil {
		t.Fatal(err)
	}
	if device.AutoUpdate != nil {
		t.Fatalf("new device autoUpdate = %v, want nil (follow the package)", *device.AutoUpdate)
	}

	forced := true
	stored, err := a.UpdateDevice(device.PublicID, "laptop", "a = 1\n", AutoUpdateChoice{Set: true, Value: &forced})
	if err != nil || stored.AutoUpdate == nil || !*stored.AutoUpdate {
		t.Fatalf("forced automatic = %+v (%v), want true", stored.AutoUpdate, err)
	}

	// 请求里没带这个字段：这一列保持原样。
	kept, err := a.UpdateDevice(device.PublicID, "laptop", "a = 2\n", AutoUpdateChoice{})
	if err != nil || kept.AutoUpdate == nil || !*kept.AutoUpdate {
		t.Fatalf("update without the field = %+v (%v), want the override kept", kept.AutoUpdate, err)
	}

	// null：恢复跟随发布包。
	back, err := a.UpdateDevice(device.PublicID, "laptop", "a = 2\n", AutoUpdateChoice{Set: true})
	if err != nil || back.AutoUpdate != nil {
		t.Fatalf("null override = %+v (%v), want follow (nil)", back.AutoUpdate, err)
	}

	manual := false
	if _, err := a.UpdateDevice(device.PublicID, "laptop", "a = 2\n", AutoUpdateChoice{Set: true, Value: &manual}); err != nil {
		t.Fatalf("forced manual error: %v", err)
	}
	reloaded, found, err := a.DeviceByID(device.PublicID)
	if err != nil || !found || reloaded.AutoUpdate == nil || *reloaded.AutoUpdate {
		t.Fatalf("forced manual = %+v (%v), want false", reloaded.AutoUpdate, err)
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
	settings := dbRelease("2026.11.24-4")
	device.LastPlatform = "windows/amd64"
	device.LastAppVersion = "2026.11.24-4"
	if got := AppState(device, settings); got != "current" {
		t.Fatalf("app state = %q, want current", got)
	}
	device.LastAppVersion = "2026.11.23-9"
	if got := AppState(device, settings); got != "stale" {
		t.Fatalf("app state = %q, want stale", got)
	}
	// 比发布新（回滚过发布、手工装过新包）也是 current：控制面只要求升级。
	device.LastAppVersion = "2026.11.25-1"
	if got := AppState(device, settings); got != "current" {
		t.Fatalf("app state of a newer device = %q, want current", got)
	}
	if got := AppState(device, dbRelease("")); got != "current" {
		t.Fatalf("app state without a release = %q, want current", got)
	}
	// 没有发布时，连平台都还没上报的设备也算 current：不存在可更新到的版本。
	unknown := device
	unknown.LastPlatform = ""
	unknown.LastAppVersion = ""
	if got := AppState(unknown, dbRelease("")); got != "current" {
		t.Fatalf("app state of a device without a platform and no release = %q, want current", got)
	}
	if got := AppState(unknown, dbRelease("2026.11.24-4")); got != "stale" {
		t.Fatalf("app state of a device without a platform = %q, want stale", got)
	}
}
func dbDevice(publicID, deviceID string, seenAt int64) db.Device {
	return db.Device{PublicID: publicID, DeviceID: deviceID, LastSeenAt: seenAt}
}

func dbRelease(version string) db.Release {
	return db.Release{Version: version}
}

// TestOpenLeavesStoredConfigHashAlone pins that startup no longer repairs data:
// the row keeps the digest it was written with. A changed hash algorithm is a
// manual migration, not a silent rewrite on boot.
func TestOpenLeavesStoredConfigHashAlone(t *testing.T) {
	dir := t.TempDir()
	const toml = "hostname = \"auto\""
	first, err := Open(dir, Options{})
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	device, _, err := first.CreateDevice("laptop", toml)
	if err != nil {
		t.Fatalf("CreateDevice() error: %v", err)
	}
	stored := core.Hash(toml + "\n")
	if stored == core.Hash(toml) {
		t.Fatalf("the two digests are equal, the test proves nothing")
	}
	if _, err := first.database.Update(device.PublicID, func(d *db.Device) error {
		d.ConfigHash = stored
		return nil
	}); err != nil {
		t.Fatalf("store a digest written by an older algorithm: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}

	second, err := Open(dir, Options{})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })
	reloaded, found, err := second.DeviceByID(device.PublicID)
	if err != nil || !found {
		t.Fatalf("DeviceByID() = found %v, %v", found, err)
	}
	if reloaded.ConfigHash != stored {
		t.Fatalf("config hash = %q, want the stored %q kept", reloaded.ConfigHash, stored)
	}
}
