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

func TestImportDevicesIsIdempotent(t *testing.T) {
	d := openTemp(t)
	legacy := []Device{
		{PublicID: "d_1", LegacyID: "d_1", Name: "one", DeviceID: "dev-1"},
		{PublicID: "d_2", LegacyID: "d_2", Name: "two"},
	}
	inserted, err := d.ImportDevices(legacy)
	if err != nil {
		t.Fatalf("ImportDevices() error: %v", err)
	}
	if inserted != 2 {
		t.Fatalf("first import inserted = %d, want 2", inserted)
	}
	// The second run must not duplicate anything, and must not overwrite a row
	// that has been edited in the meantime.
	if _, err := d.Update("d_1", func(device *Device) error {
		device.Name = "edited"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	inserted, err = d.ImportDevices(legacy)
	if err != nil {
		t.Fatalf("second ImportDevices() error: %v", err)
	}
	if inserted != 0 {
		t.Fatalf("second import inserted = %d, want 0", inserted)
	}
	if n, err := d.Count(); err != nil || n != 2 {
		t.Fatalf("Count() = %d, %v; want 2", n, err)
	}
	if got, err := d.Get("d_1"); err != nil || got.Name != "edited" {
		t.Fatalf("edited device = %+v, %v", got, err)
	}
}

func TestSettingsSingleRow(t *testing.T) {
	d := openTemp(t)
	if _, found, err := d.GetSettings(); err != nil || found {
		t.Fatalf("GetSettings() on empty database = found %v, %v", found, err)
	}
	first := Settings{AppVersion: "v1", AppHash: "h1", AppSHA256: "s1", AppSize: 3}
	if err := d.SetSettings(first); err != nil {
		t.Fatalf("SetSettings() error: %v", err)
	}
	second := Settings{AppVersion: "v2", AppHash: "h2", AppSHA256: "s2", AppSize: 4}
	if err := d.SetSettings(second); err != nil {
		t.Fatalf("SetSettings() second error: %v", err)
	}
	got, found, err := d.GetSettings()
	if err != nil || !found || got != second {
		t.Fatalf("GetSettings() = %+v, found %v, %v", got, found, err)
	}
	var rows int
	if err := d.conn.QueryRow("SELECT COUNT(*) FROM setting").Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("setting rows = %d, want 1", rows)
	}
	imported, err := d.ImportSettings(Settings{AppVersion: "legacy"})
	if err != nil || imported {
		t.Fatalf("ImportSettings() over existing settings = %v, %v", imported, err)
	}
	if got, _, _ := d.GetSettings(); got.AppVersion != "v2" {
		t.Fatalf("settings overwritten by import: %+v", got)
	}
}
