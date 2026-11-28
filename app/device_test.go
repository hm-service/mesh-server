package app

import (
	"context"
	"encoding/base64"
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

// createTestDevice creates a slot bound to a fresh key pair. 槽位从创建起就认
// 一把钥匙，所以测试里的设备默认都是可认领、可签发的。
func createTestDevice(t *testing.T, a *App, name, toml string) db.Device {
	t.Helper()
	pubkey, _ := testKeypair(t)
	device, err := a.CreateDevice(name, toml, pubkey)
	if err != nil {
		t.Fatalf("CreateDevice() error: %v", err)
	}
	return device
}

// TestCreateDeviceStoresTheSlot pins what creation writes: the key is the slot's
// identity from the start, the configuration is stored verbatim with its digest,
// and the slot is unclaimed until a signed request proves the key.
func TestCreateDeviceStoresTheSlot(t *testing.T) {
	a := openTemp(t)
	const toml = "hostname = \"auto\"\n"
	pubkey, _ := testKeypair(t)
	device, err := a.CreateDevice("laptop", toml, pubkey)
	if err != nil {
		t.Fatalf("CreateDevice() error: %v", err)
	}
	if device.Pubkey != pubkey {
		t.Fatalf("stored pubkey = %q, want %q", device.Pubkey, pubkey)
	}
	if device.ConfigHash != core.Hash(toml) || device.ConfigTOML != toml {
		t.Fatalf("stored config = %q/%q", device.ConfigTOML, device.ConfigHash)
	}
	if device.DeviceID != "" || device.Hostname != "" || device.LastSeenAt != 0 {
		t.Fatalf("a new slot is already claimed: %+v", device)
	}
	if got, found, err := a.DeviceByPubkey(pubkey); err != nil || !found || got.PublicID != device.PublicID {
		t.Fatalf("DeviceByPubkey() = %+v, found %v, %v", got, found, err)
	}

	if _, err := a.CreateDevice("   ", toml, pubkey); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("blank name = %v, want ErrInvalidInput", err)
	}
	if _, err := a.CreateDevice("name", "   ", pubkey); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("blank config = %v, want ErrInvalidInput", err)
	}
}

// TestCreateDeviceRejectsUnusableKeys pins the creation-time key check: a value
// that is not base64 of a decodable Ed25519 point can never prove a slot, so it
// must not become one.
func TestCreateDeviceRejectsUnusableKeys(t *testing.T) {
	a := openTemp(t)
	const toml = "a = 1\n"
	identity := make([]byte, 32)
	identity[0] = 1 // y = 1：Edwards 单位元，没有 Montgomery 形式。
	cases := map[string]string{
		"empty":     "",
		"not b64":   "not-base64!",
		"too short": base64.StdEncoding.EncodeToString(make([]byte, 31)),
		"identity":  base64.StdEncoding.EncodeToString(identity),
	}
	for name, pubkey := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := a.CreateDevice("laptop", toml, pubkey); !errors.Is(err, ErrPublicKeyInvalid) {
				t.Fatalf("CreateDevice() with a %s key = %v, want ErrPublicKeyInvalid", name, err)
			}
		})
	}
}

// TestCreateDeviceRejectsAKeyAnotherSlotHolds pins the slot-per-key rule at the
// app boundary, so the admin API can answer it instead of surfacing a database
// constraint.
func TestCreateDeviceRejectsAKeyAnotherSlotHolds(t *testing.T) {
	a := openTemp(t)
	first := createTestDevice(t, a, "first", "a = 1\n")
	if _, err := a.CreateDevice("second", "a = 1\n", first.Pubkey); !errors.Is(err, ErrPubkeyTaken) {
		t.Fatalf("CreateDevice() with a key another slot holds = %v, want ErrPubkeyTaken", err)
	}
}

// TestClaimBindsTheSlot pins the first claim: the device_id, hostname and
// address the signed request carried are written together with last_seen_at.
func TestClaimBindsTheSlot(t *testing.T) {
	a := openTemp(t)
	device := createTestDevice(t, a, "laptop", "a = 1\n")
	claimed, err := a.ClaimDevice(context.Background(), Claim{
		PublicID:     device.PublicID,
		ProvedPubkey: device.Pubkey,
		DeviceID:     "dev-1",
		Hostname:     "DESKTOP",
		IP:           "10.0.0.5",
	})
	if err != nil {
		t.Fatalf("ClaimDevice() error: %v", err)
	}
	if claimed.DeviceID != "dev-1" || claimed.Hostname != "DESKTOP" || claimed.LastIP != "10.0.0.5" {
		t.Fatalf("claimed = %+v, want the identity from the request", claimed)
	}
	if got, found, err := a.DeviceByDeviceID("dev-1"); err != nil || !found || got.PublicID != device.PublicID {
		t.Fatalf("DeviceByDeviceID() = %+v, found %v, %v", got, found, err)
	}
	if DeviceStatus(claimed, a.now()) != "online" {
		t.Fatalf("status of a just-claimed slot = %q, want online", DeviceStatus(claimed, a.now()))
	}
}

// TestClaimIsIdempotent pins the repeated claim: the same device_id keeps the
// observations, refreshes the hostname and takes the address only when the
// request carries one.
func TestClaimIsIdempotent(t *testing.T) {
	a := openTemp(t)
	device := createTestDevice(t, a, "laptop", "a = 1\n")
	ctx := context.Background()
	claim := func(hostname, ip string) db.Device {
		t.Helper()
		claimed, err := a.ClaimDevice(ctx, Claim{
			PublicID:     device.PublicID,
			ProvedPubkey: device.Pubkey,
			DeviceID:     "dev-1",
			Hostname:     hostname,
			IP:           ip,
		})
		if err != nil {
			t.Fatalf("ClaimDevice() error: %v", err)
		}
		return claimed
	}
	claim("HOST-1", "10.0.0.5")
	if _, err := a.Heartbeat(ctx, DeviceAuth{PublicID: device.PublicID, DeviceID: "dev-1", Pubkey: device.Pubkey},
		HeartbeatObservation{ConfigHash: "hash-1", AppVersion: "v1", Platform: "windows/amd64"}); err != nil {
		t.Fatalf("Heartbeat() error: %v", err)
	}
	again := claim("HOST-2", "")
	if again.Hostname != "HOST-2" {
		t.Fatalf("hostname = %q, want the refreshed one", again.Hostname)
	}
	if again.LastIP != "10.0.0.5" {
		t.Fatalf("last ip = %q, want the stored address kept", again.LastIP)
	}
	if again.LastConfigHash != "hash-1" || again.LastAppVersion != "v1" || again.LastPlatform != "windows/amd64" {
		t.Fatalf("observations cleared by an idempotent claim: %+v", again)
	}
	if a.Revoked("dev-1") {
		t.Fatal("an idempotent claim revoked the current identity")
	}
	if moved := claim("HOST-2", "10.0.0.6"); moved.LastIP != "10.0.0.6" {
		t.Fatalf("last ip = %q, want the reported address", moved.LastIP)
	}
}

// TestClaimRebindingRevokesThePreviousIdentity pins the hand-over of a slot
// between two device_ids of the same key: the old identity is tombstoned so its
// in-flight requests cannot keep working, and the new one is not.
func TestClaimRebindingRevokesThePreviousIdentity(t *testing.T) {
	a := openTemp(t)
	device := createTestDevice(t, a, "laptop", "a = 1\n")
	ctx := context.Background()
	for _, id := range []string{"dev-1", "dev-2"} {
		if _, err := a.ClaimDevice(ctx, Claim{
			PublicID:     device.PublicID,
			ProvedPubkey: device.Pubkey,
			DeviceID:     id,
			Hostname:     "HOST",
		}); err != nil {
			t.Fatalf("ClaimDevice(%s) error: %v", id, err)
		}
	}
	if !a.Revoked("dev-1") {
		t.Fatal("previous identity was not revoked")
	}
	if a.Revoked("dev-2") {
		t.Fatal("current identity marked revoked")
	}
	stored, found, err := a.DeviceByID(device.PublicID)
	if err != nil || !found || stored.DeviceID != "dev-2" {
		t.Fatalf("device = %+v, found %v, %v; want dev-2", stored, found, err)
	}
}

// TestRebindClearsPreviousObservations pins Q10: a rebind replaces the identity
// and the observations that identity reported, in the same update. Slot-owned
// attributes (name, group, configuration) survive; a claim for the same
// device_id is not an identity change and keeps its observations.
func TestRebindClearsPreviousObservations(t *testing.T) {
	a := openTemp(t)
	ctx := context.Background()
	device := createTestDevice(t, a, "laptop", "a = 1\n")
	if _, err := a.ClaimDevice(ctx, Claim{
		PublicID: device.PublicID, ProvedPubkey: device.Pubkey,
		DeviceID: "dev-1", Hostname: "OLD-HOST", IP: "10.0.0.1",
	}); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if _, err := a.Heartbeat(ctx, DeviceAuth{PublicID: device.PublicID, DeviceID: "dev-1", Pubkey: device.Pubkey},
		HeartbeatObservation{ConfigHash: "old-hash", AppVersion: "2026.11.26-1", Platform: "windows/amd64"}); err != nil {
		t.Fatalf("Heartbeat() error: %v", err)
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

	bound, err := a.ClaimDevice(ctx, Claim{
		PublicID: device.PublicID, ProvedPubkey: device.Pubkey,
		DeviceID: "dev-2", Hostname: "NEW-HOST",
	})
	if err != nil {
		t.Fatalf("rebind error: %v", err)
	}
	if bound.LastConfigHash != "" || bound.LastAppVersion != "" || bound.LastPlatform != "" {
		t.Fatalf("observations of the previous identity survived the rebind: %+v", bound)
	}
	if bound.Name != "renamed" || bound.ConfigTOML != "a = 2\n" || bound.ConfigHash != core.Hash("a = 2\n") || bound.GroupID != group.ID {
		t.Fatalf("slot attributes were lost by the rebind: %+v", bound)
	}
	if bound.DeviceID != "dev-2" || bound.Hostname != "NEW-HOST" || bound.Pubkey != device.Pubkey {
		t.Fatalf("identity not replaced: %+v", bound)
	}

	// 同一身份重新注册不是换绑：观测值仍属于它。
	if _, err := a.Heartbeat(ctx, DeviceAuth{PublicID: device.PublicID, DeviceID: "dev-2", Pubkey: device.Pubkey},
		HeartbeatObservation{ConfigHash: "new-hash", AppVersion: "2026.11.27-1", Platform: "linux/amd64"}); err != nil {
		t.Fatalf("Heartbeat() error: %v", err)
	}
	same, err := a.ClaimDevice(ctx, Claim{
		PublicID: device.PublicID, ProvedPubkey: device.Pubkey,
		DeviceID: "dev-2", Hostname: "NEW-HOST",
	})
	if err != nil {
		t.Fatalf("claim for the same identity: %v", err)
	}
	if same.LastConfigHash != "new-hash" || same.LastAppVersion != "2026.11.27-1" || same.LastPlatform != "linux/amd64" {
		t.Fatalf("observations cleared although the identity did not change: %+v", same)
	}
}

// TestClaimRejectsAStaleIdentityProof pins the check under the lock: the
// signature was verified against the key the slot held when the request was
// authenticated. If the slot was rebound to another key before the lock was
// taken, that proof no longer says the caller holds the slot, so nothing is
// written and no configuration is handed back.
func TestClaimRejectsAStaleIdentityProof(t *testing.T) {
	a := openTemp(t)
	ctx := context.Background()
	device := createTestDevice(t, a, "laptop", "a = 1\n")
	if _, err := a.ClaimDevice(ctx, Claim{
		PublicID: device.PublicID, ProvedPubkey: device.Pubkey, DeviceID: "dev-1", Hostname: "HOST",
	}); err != nil {
		t.Fatalf("ClaimDevice() error: %v", err)
	}

	// 认证已经把这次请求的签名用旧钥匙验过了；之后槽位换成新钥匙。
	next, _ := testKeypair(t)
	if _, err := a.RebindDevice(ctx, device.PublicID, next); err != nil {
		t.Fatalf("RebindDevice() error: %v", err)
	}
	before, found, err := a.DeviceByID(device.PublicID)
	if err != nil || !found {
		t.Fatalf("DeviceByID() = found %v, %v", found, err)
	}
	_, err = a.ClaimDevice(ctx, Claim{
		PublicID: device.PublicID, ProvedPubkey: device.Pubkey,
		DeviceID: "dev-1", Hostname: "HOST", IP: "10.0.0.9",
	})
	if !errors.Is(err, ErrIdentityChanged) {
		t.Fatalf("ClaimDevice() with a proof made by the previous key = %v, want ErrIdentityChanged", err)
	}
	after, found, err := a.DeviceByID(device.PublicID)
	if err != nil || !found {
		t.Fatalf("DeviceByID() = found %v, %v", found, err)
	}
	if after != before {
		t.Fatalf("the refused registration wrote the row: %+v -> %+v", before, after)
	}

	// 用当前那把钥匙验过的证明照常通过。
	if _, err := a.ClaimDevice(ctx, Claim{
		PublicID: device.PublicID, ProvedPubkey: next,
		DeviceID: "dev-1", Hostname: "HOST", IP: "10.0.0.9",
	}); err != nil {
		t.Fatalf("ClaimDevice() with the current key: %v", err)
	}
}

// TestClaimRejectsADeviceIDAnotherSlotHolds pins the 409 of the claim table: a
// device_id already owned by another slot is refused, nothing is written, and
// the slot can still be claimed under a free id.
func TestClaimRejectsADeviceIDAnotherSlotHolds(t *testing.T) {
	a := openTemp(t)
	ctx := context.Background()
	first := createTestDevice(t, a, "first", "a = 1\n")
	second := createTestDevice(t, a, "second", "a = 1\n")
	if _, err := a.ClaimDevice(ctx, Claim{
		PublicID: first.PublicID, ProvedPubkey: first.Pubkey, DeviceID: "dev-1", Hostname: "HOST",
	}); err != nil {
		t.Fatalf("ClaimDevice() error: %v", err)
	}
	before, found, err := a.DeviceByID(second.PublicID)
	if err != nil || !found {
		t.Fatalf("DeviceByID() = found %v, %v", found, err)
	}
	_, err = a.ClaimDevice(ctx, Claim{
		PublicID: second.PublicID, ProvedPubkey: second.Pubkey,
		DeviceID: "dev-1", Hostname: "HOST", IP: "10.0.0.9",
	})
	if !errors.Is(err, ErrDeviceIDTaken) {
		t.Fatalf("ClaimDevice() with a taken device_id = %v, want ErrDeviceIDTaken", err)
	}
	after, found, err := a.DeviceByID(second.PublicID)
	if err != nil || !found {
		t.Fatalf("DeviceByID() = found %v, %v", found, err)
	}
	if after != before {
		t.Fatalf("the refused claim wrote the row: %+v -> %+v", before, after)
	}
	if stored, found, err := a.DeviceByID(first.PublicID); err != nil || !found || stored.DeviceID != "dev-1" {
		t.Fatalf("the first slot = %+v, found %v, %v; want it untouched", stored, found, err)
	}

	// 换一个没人占用的 device_id 照常认领。
	if _, err := a.ClaimDevice(ctx, Claim{
		PublicID: second.PublicID, ProvedPubkey: second.Pubkey, DeviceID: "dev-2", Hostname: "HOST",
	}); err != nil {
		t.Fatalf("ClaimDevice() with a free device_id: %v", err)
	}
}

// TestRebindPointsTheSlotAtAnotherKey pins the key rotation: the old key stops
// being the slot's identity, the slot goes back to unclaimed with the old
// observations cleared, and only the new key can claim it afterwards.
func TestRebindPointsTheSlotAtAnotherKey(t *testing.T) {
	a := openTemp(t)
	ctx := context.Background()
	device := createTestDevice(t, a, "laptop", "a = 1\n")
	if _, err := a.ClaimDevice(ctx, Claim{
		PublicID: device.PublicID, ProvedPubkey: device.Pubkey, DeviceID: "dev-1", Hostname: "HOST", IP: "10.0.0.9",
	}); err != nil {
		t.Fatalf("ClaimDevice() error: %v", err)
	}
	if _, err := a.Heartbeat(ctx, DeviceAuth{PublicID: device.PublicID, DeviceID: "dev-1", Pubkey: device.Pubkey},
		HeartbeatObservation{ConfigHash: "old-hash", AppVersion: "2026.11.26-1", Platform: "windows/amd64"}); err != nil {
		t.Fatalf("Heartbeat() error: %v", err)
	}

	// 写之前的拒绝：钥匙不能用、钥匙归别的槽位、槽位不存在。
	if _, err := a.RebindDevice(ctx, device.PublicID, "not-base64!"); !errors.Is(err, ErrPublicKeyInvalid) {
		t.Fatalf("RebindDevice() with an unusable key = %v, want ErrPublicKeyInvalid", err)
	}
	other := createTestDevice(t, a, "other", "a = 1\n")
	before, found, err := a.DeviceByID(device.PublicID)
	if err != nil || !found {
		t.Fatalf("DeviceByID() = found %v, %v", found, err)
	}
	if _, err := a.RebindDevice(ctx, device.PublicID, other.Pubkey); !errors.Is(err, ErrPubkeyTaken) {
		t.Fatalf("RebindDevice() to a key another slot holds = %v, want ErrPubkeyTaken", err)
	}
	if after, found, err := a.DeviceByID(device.PublicID); err != nil || !found || after != before {
		t.Fatalf("a rejected rebind wrote the row: %+v, found %v, %v", after, found, err)
	}
	next, _ := testKeypair(t)
	if _, err := a.RebindDevice(ctx, "d_missing", next); !errors.Is(err, ErrNotFound) {
		t.Fatalf("RebindDevice() of a missing slot = %v, want ErrNotFound", err)
	}

	rebound, err := a.RebindDevice(ctx, device.PublicID, next)
	if err != nil {
		t.Fatalf("RebindDevice() error: %v", err)
	}
	if rebound.Pubkey != next || rebound.DeviceID != "" || rebound.Hostname != "" ||
		rebound.LastConfigHash != "" || rebound.LastAppVersion != "" || rebound.LastPlatform != "" {
		t.Fatalf("rebound = %+v, want the new key and an unclaimed slot", rebound)
	}
	// 旧钥匙不再是这个槽位的身份。
	if _, err := a.ClaimDevice(ctx, Claim{
		PublicID: device.PublicID, ProvedPubkey: device.Pubkey, DeviceID: "dev-1", Hostname: "HOST",
	}); !errors.Is(err, ErrIdentityChanged) {
		t.Fatalf("ClaimDevice() with the replaced key = %v, want ErrIdentityChanged", err)
	}
	// 新钥匙认领。
	if _, err := a.ClaimDevice(ctx, Claim{
		PublicID: device.PublicID, ProvedPubkey: next, DeviceID: "dev-2", Hostname: "HOST",
	}); err != nil {
		t.Fatalf("ClaimDevice() with the new key: %v", err)
	}
}

func TestDeleteRevokesDeviceID(t *testing.T) {
	a := openTemp(t)
	device := createTestDevice(t, a, "laptop", "a = 1\n")
	if _, err := a.ClaimDevice(context.Background(), Claim{
		PublicID: device.PublicID, ProvedPubkey: device.Pubkey, DeviceID: "dev-1", Hostname: "HOST",
	}); err != nil {
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
	device := createTestDevice(t, a, "laptop", "a = 1\n")
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
	device := createTestDevice(t, a, "laptop", "a = 1\n")
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
		pubkey, _ := testKeypair(t)
		if _, err := a.CreateDevice(fmt.Sprintf("dev-%03d", i), "a = 1\n", pubkey); err != nil {
			t.Fatalf("CreateDevice(%d) error: %v", i, err)
		}
	}
	pubkey, _ := testKeypair(t)
	if _, err := a.CreateDevice("overflow", "a = 1\n", pubkey); !errors.Is(err, ErrLimit) {
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
	if got := AppState(device, db.Release{}); got != "current" {
		t.Fatalf("app state without a release = %q, want current", got)
	}
	// 没有发布时，连平台都还没上报的设备也算 current：不存在可更新到的版本。
	unknown := device
	unknown.LastPlatform = ""
	unknown.LastAppVersion = ""
	if got := AppState(unknown, db.Release{}); got != "current" {
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
	return db.Release{Platform: "windows/amd64", Client: &db.ClientSlot{Version: version}}
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
	device := createTestDevice(t, first, "laptop", toml)
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
