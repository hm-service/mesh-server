package db

import (
	"errors"
	"fmt"
	"path/filepath"
	"testing"
)

func openTemp(t *testing.T) *DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "mesh.db"))
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func TestDeviceRoundTrip(t *testing.T) {
	d := openTemp(t)
	want := Device{
		PublicID:   "d_1",
		Name:       "laptop",
		DeviceID:   "dev-1",
		Hostname:   "DESKTOP",
		Pubkey:     "pk",
		ConfigTOML: "a = 1\n",
		ConfigHash: "abc",
		CreatedAt:  7,
		LastIP:     "10.0.0.1",
	}
	if err := d.Create(want, 10); err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	got, err := d.Get("d_1")
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if got != want {
		t.Fatalf("device = %+v, want %+v", got, want)
	}
	if byDeviceID, err := d.GetByDeviceID("dev-1"); err != nil || byDeviceID.PublicID != "d_1" {
		t.Fatalf("GetByDeviceID() = %+v, %v", byDeviceID, err)
	}
	if _, err := d.GetByDeviceID(""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetByDeviceID(\"\") = %v, want ErrNotFound", err)
	}
}

func TestDeviceLimit(t *testing.T) {
	d := openTemp(t)
	for i := range 3 {
		if err := d.Create(Device{PublicID: fmt.Sprintf("d_%d", i)}, 3); err != nil {
			t.Fatalf("Create(%d) error: %v", i, err)
		}
	}
	if err := d.Create(Device{PublicID: "d_extra"}, 3); !errors.Is(err, ErrLimit) {
		t.Fatalf("Create() over limit = %v, want ErrLimit", err)
	}
}

func TestUpdateAndDelete(t *testing.T) {
	d := openTemp(t)
	if err := d.Create(Device{PublicID: "d_1", Name: "old", DeviceID: "dev-1"}, 10); err != nil {
		t.Fatal(err)
	}
	updated, err := d.Update("d_1", func(device *Device) error {
		device.Name = "new"
		device.ConfigHash = "hash"
		return nil
	})
	if err != nil {
		t.Fatalf("Update() error: %v", err)
	}
	if updated.Name != "new" || updated.ConfigHash != "hash" {
		t.Fatalf("updated = %+v", updated)
	}
	if _, err := d.Delete("d_1"); err != nil {
		t.Fatalf("Delete() error: %v", err)
	}
	if _, err := d.Get("d_1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get() after delete = %v, want ErrNotFound", err)
	}
	if _, err := d.Delete("d_missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete(unknown) = %v, want ErrNotFound", err)
	}
}

func TestPurgeExpiredTokens(t *testing.T) {
	d := openTemp(t)
	now := int64(1_800_000_000)
	if err := d.Create(Device{PublicID: "d_old", Token: "etk_old", TokenExpiresAt: now - 1}, 10); err != nil {
		t.Fatal(err)
	}
	if err := d.Create(Device{PublicID: "d_new", Token: "etk_new", TokenExpiresAt: now + 600}, 10); err != nil {
		t.Fatal(err)
	}
	purged, err := d.PurgeExpiredTokens(now)
	if err != nil {
		t.Fatalf("PurgeExpiredTokens() error: %v", err)
	}
	if purged != 1 {
		t.Fatalf("purged = %d, want 1", purged)
	}
	old, err := d.Get("d_old")
	if err != nil || old.Token != "" || old.TokenExpiresAt != 0 {
		t.Fatalf("expired token not purged: %+v, %v", old, err)
	}
	fresh, err := d.Get("d_new")
	if err != nil || fresh.Token != "etk_new" {
		t.Fatalf("fresh token purged: %+v, %v", fresh, err)
	}
}

func TestReleasePerPlatform(t *testing.T) {
	d := openTemp(t)
	if _, found, err := d.Release("windows/amd64"); err != nil || found {
		t.Fatalf("Release() on empty database = found %v, %v", found, err)
	}
	first := ClientSlot{Version: "v1", URL: "u1", Hash: "h1", SHA256: "s1"}
	if err := d.PublishSlots("windows/amd64", &first, nil); err != nil {
		t.Fatalf("PublishSlots() error: %v", err)
	}
	second := ClientSlot{Version: "v2", URL: "u2", Hash: "h2", SHA256: "s2"}
	if err := d.PublishSlots("windows/amd64", &second, nil); err != nil {
		t.Fatalf("PublishSlots() second error: %v", err)
	}
	got, found, err := d.Release("windows/amd64")
	if err != nil || !found || got != (Release{Platform: "windows/amd64", Version: "v2", URL: "u2", Hash: "h2", SHA256: "s2"}) {
		t.Fatalf("Release() = %+v, found %v, %v", got, found, err)
	}
	if err := d.PublishSlots("linux/amd64", &ClientSlot{Version: "v9"}, nil); err != nil {
		t.Fatalf("PublishSlots() linux error: %v", err)
	}
	all, err := d.Releases()
	if err != nil || len(all) != 2 {
		t.Fatalf("Releases() = %+v, %v; want 2 rows", all, err)
	}
	if err := d.DeleteRelease("linux/amd64"); err != nil {
		t.Fatalf("DeleteRelease() error: %v", err)
	}
	if _, found, _ := d.Release("linux/amd64"); found {
		t.Fatal("linux release survived DeleteRelease")
	}
}

// TestReleaseSlotsAreIndependent pins the storage contract behind the two
// published artifacts: the client package and the launcher share a row but each
// has its own write and clear path.
func TestReleaseSlotsAreIndependent(t *testing.T) {
	d := openTemp(t)
	client := ClientSlot{Version: "v1", URL: "u1", Hash: "h1", SHA256: "s1", Notes: "n1"}
	if err := d.PublishSlots("windows/amd64", &client, nil); err != nil {
		t.Fatalf("PublishSlots() client error: %v", err)
	}
	if err := d.PublishSlots("windows/amd64", nil, &LauncherSlot{Version: "l1", URL: "lu1", SHA256: "ls1"}); err != nil {
		t.Fatalf("PublishSlots() launcher error: %v", err)
	}
	want := Release{Platform: "windows/amd64", Version: "v1", URL: "u1", Hash: "h1", SHA256: "s1", Notes: "n1",
		LauncherVersion: "l1", LauncherURL: "lu1", LauncherSHA256: "ls1"}
	got, found, err := d.Release("windows/amd64")
	if err != nil || !found || got != want {
		t.Fatalf("Release() = %+v, found %v, %v; want %+v", got, found, err, want)
	}

	// Clearing the launcher leaves the client package alone.
	if err := d.ClearLauncher("windows/amd64"); err != nil {
		t.Fatalf("ClearLauncher() error: %v", err)
	}
	clientOnly := Release{Platform: "windows/amd64", Version: "v1", URL: "u1", Hash: "h1", SHA256: "s1", Notes: "n1"}
	if got, found, err = d.Release("windows/amd64"); err != nil || !found || got != clientOnly {
		t.Fatalf("after ClearLauncher() = %+v, found %v, %v; want %+v", got, found, err, clientOnly)
	}

	// A launcher-only row disappears once its slot is cleared.
	if err := d.PublishSlots("linux/amd64", nil, &LauncherSlot{Version: "l9", URL: "lu9", SHA256: "ls9"}); err != nil {
		t.Fatalf("PublishSlots() launcher-only error: %v", err)
	}
	if err := d.ClearLauncher("linux/amd64"); err != nil {
		t.Fatalf("ClearLauncher() error: %v", err)
	}
	if _, found, err := d.Release("linux/amd64"); err != nil || found {
		t.Fatalf("empty row left behind: found %v, %v", found, err)
	}
}

// TestPublishSlotsIsAtomic pins that the two slots of one publish land together:
// the writes share a transaction, so a row can never carry only the second slot.
func TestPublishSlotsIsAtomic(t *testing.T) {
	d := openTemp(t)
	if err := d.PublishSlots("windows/amd64",
		&ClientSlot{Version: "v1", URL: "u1", Hash: "h1", SHA256: "s1"},
		&LauncherSlot{Version: "l1", URL: "lu1", SHA256: "ls1"}); err != nil {
		t.Fatalf("PublishSlots() error: %v", err)
	}
	got, found, err := d.Release("windows/amd64")
	if err != nil || !found {
		t.Fatalf("Release() = found %v, %v", found, err)
	}
	if got.Version != "v1" || got.LauncherVersion != "l1" || got.LauncherSHA256 != "ls1" {
		t.Fatalf("release = %+v, want both slots", got)
	}
}
