package db

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
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

// TestObserveMatchesTheAuthenticatedKey pins the condition of the observation
// write: the row is only touched while it still holds the identity the request
// authenticated as. The same device_id bound to another key is another identity,
// because a rebind can reuse a device_id — the key is what cannot be reused.
func TestObserveMatchesTheAuthenticatedKey(t *testing.T) {
	d := openTemp(t)
	if err := d.Create(Device{PublicID: "d_1", DeviceID: "dev-1", Pubkey: "pk1"}, 10); err != nil {
		t.Fatal(err)
	}
	now := int64(1_800_000_000)
	got, err := d.Observe("d_1", "dev-1", "pk1", Observation{ConfigHash: "h1", AppVersion: "v1", IP: "10.0.0.9"}, now)
	if err != nil {
		t.Fatalf("Observe() error: %v", err)
	}
	if got.LastConfigHash != "h1" || got.LastAppVersion != "v1" || got.LastIP != "10.0.0.9" || got.LastSeenAt != now {
		t.Fatalf("observed row = %+v, want the reported observations", got)
	}

	// 同一个 device_id 换了一把钥匙：旧身份写下的观测值不再属于它。
	if _, err := d.Update("d_1", func(device *Device) error {
		device.Pubkey = "pk2"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before, err := d.Get("d_1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Observe("d_1", "dev-1", "pk1", Observation{ConfigHash: "h2", AppVersion: "v2"}, now+1); !errors.Is(err, ErrIdentityChanged) {
		t.Fatalf("Observe() with the previous key = %v, want ErrIdentityChanged", err)
	}
	after, err := d.Get("d_1")
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("the refused observation wrote the row: %+v -> %+v", before, after)
	}
	if _, err := d.Observe("d_1", "dev-2", "pk2", Observation{}, now+1); !errors.Is(err, ErrIdentityChanged) {
		t.Fatalf("Observe() with another device_id = %v, want ErrIdentityChanged", err)
	}
	if _, err := d.Observe("d_missing", "dev-1", "pk1", Observation{}, now+1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Observe() on a missing slot = %v, want ErrNotFound", err)
	}
	if _, err := d.Observe("d_1", "dev-1", "pk2", Observation{ConfigHash: "h2"}, now+2); err != nil {
		t.Fatalf("Observe() with the current key: %v", err)
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

// TestOpenRejectsDatabaseMissingAColumn pins the startup contract: Open compares
// the file with db/db.go and fails with the column named. It does not add the
// column, so a server started against an unmigrated file stops instead of
// running on a silently patched schema.
func TestOpenRejectsDatabaseMissingAColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mesh.db")
	current, err := Open(path)
	if err != nil {
		t.Fatalf("Open() on a fresh database: %v", err)
	}
	if err := current.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}

	conn, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open the file to age it: %v", err)
	}
	if _, err := conn.Exec("ALTER TABLE device DROP COLUMN last_platform"); err != nil {
		conn.Close()
		t.Fatalf("prepare a database without device.last_platform: %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("close the aged file: %v", err)
	}

	if _, err := Open(path); err == nil {
		t.Fatal("Open() accepted a database without device.last_platform")
	} else if !strings.Contains(err.Error(), "last_platform") {
		t.Fatalf("Open() error %q does not name the missing column", err)
	}

	// The failed start left the old table alone: nothing was added back.
	conn, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("reopen the file: %v", err)
	}
	defer conn.Close()
	columns, err := tableColumns(conn, "device")
	if err != nil {
		t.Fatalf("inspect device columns: %v", err)
	}
	for _, column := range columns {
		if column.Name == "last_platform" {
			t.Fatal("Open() added last_platform to a database that lacked it")
		}
	}
}

func TestReleasePerPlatform(t *testing.T) {
	d := openTemp(t)
	if _, found, err := d.Release("windows/amd64"); err != nil || found {
		t.Fatalf("Release() on empty database = found %v, %v", found, err)
	}
	first := ClientSlot{Version: "v1", URL: "u1", SHA256: "s1"}
	if err := d.PublishSlots("windows/amd64", &first, nil); err != nil {
		t.Fatalf("PublishSlots() error: %v", err)
	}
	second := ClientSlot{Version: "v2", URL: "u2", SHA256: "s2"}
	if err := d.PublishSlots("windows/amd64", &second, nil); err != nil {
		t.Fatalf("PublishSlots() second error: %v", err)
	}
	got, found, err := d.Release("windows/amd64")
	if err != nil || !found || got.Client == nil || got.Launcher != nil ||
		*got.Client != second {
		t.Fatalf("Release() = %+v, found %v, %v; want the second client slot", got, found, err)
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
// published artifacts: the client package and the launcher are separate rows,
// so each has its own write and delete path and one never disturbs the other.
func TestReleaseSlotsAreIndependent(t *testing.T) {
	d := openTemp(t)
	client := ClientSlot{Version: "v1", URL: "u1", SHA256: "s1", Notes: "n1", AutoUpdate: true}
	if err := d.PublishSlots("windows/amd64", &client, nil); err != nil {
		t.Fatalf("PublishSlots() client error: %v", err)
	}
	launcher := LauncherSlot{Version: "l1", URL: "lu1", SHA256: "ls1"}
	if err := d.PublishSlots("windows/amd64", nil, &launcher); err != nil {
		t.Fatalf("PublishSlots() launcher error: %v", err)
	}
	// Publishing the launcher leaves the package's switch where the admin put it.
	got, found, err := d.Release("windows/amd64")
	if err != nil || !found || got.Client == nil || got.Launcher == nil {
		t.Fatalf("Release() = %+v, found %v, %v; want both slots", got, found, err)
	}
	if *got.Client != client || *got.Launcher != launcher {
		t.Fatalf("Release() = %+v, want %+v and %+v", got, client, launcher)
	}

	// Clearing the launcher leaves the client package alone.
	if err := d.ClearLauncher("windows/amd64"); err != nil {
		t.Fatalf("ClearLauncher() error: %v", err)
	}
	got, found, err = d.Release("windows/amd64")
	if err != nil || !found || got.Launcher != nil || got.Client == nil || *got.Client != client {
		t.Fatalf("after ClearLauncher() = %+v, found %v, %v; want only the client slot", got, found, err)
	}

	// A launcher-only platform disappears once its row is cleared.
	if err := d.PublishSlots("linux/amd64", nil, &LauncherSlot{Version: "l9", URL: "lu9", SHA256: "ls9"}); err != nil {
		t.Fatalf("PublishSlots() launcher-only error: %v", err)
	}
	if err := d.ClearLauncher("linux/amd64"); err != nil {
		t.Fatalf("ClearLauncher() error: %v", err)
	}
	if _, found, err := d.Release("linux/amd64"); err != nil || found {
		t.Fatalf("launcher row left behind: found %v, %v", found, err)
	}
}

// TestPublishSlotsIsAtomic pins that the two slots of one publish land together:
// the writes share a transaction, so the second row can never exist alone.
func TestPublishSlotsIsAtomic(t *testing.T) {
	d := openTemp(t)
	if err := d.PublishSlots("windows/amd64",
		&ClientSlot{Version: "v1", URL: "u1", SHA256: "s1"},
		&LauncherSlot{Version: "l1", URL: "lu1", SHA256: "ls1"}); err != nil {
		t.Fatalf("PublishSlots() error: %v", err)
	}
	got, found, err := d.Release("windows/amd64")
	if err != nil || !found || got.Client == nil || got.Launcher == nil {
		t.Fatalf("Release() = %+v, found %v, %v; want both slots", got, found, err)
	}
	if got.Client.Version != "v1" || got.Launcher.Version != "l1" || got.Launcher.SHA256 != "ls1" {
		t.Fatalf("release = %+v, want both slots", got)
	}
}

// TestLauncherRowCannotCarryClientFields pins the table CHECK: notes and the
// auto-update switch belong to the client package row, so the database refuses
// a launcher row that declares them instead of asking every writer to remember.
func TestLauncherRowCannotCarryClientFields(t *testing.T) {
	d := openTemp(t)
	if _, err := d.conn.Exec("INSERT INTO release (platform, slot, version, notes, auto_update) " +
		"VALUES ('windows/amd64', 'client', 'v1', 'n1', 1)"); err != nil {
		t.Fatalf("a client row with notes and auto_update = %v, want it accepted", err)
	}
	for name, statement := range map[string]string{
		"notes":       "INSERT INTO release (platform, slot, version, notes) VALUES ('linux/amd64', 'launcher', 'l1', 'n1')",
		"auto_update": "INSERT INTO release (platform, slot, version, auto_update) VALUES ('linux/amd64', 'launcher', 'l1', 1)",
	} {
		if _, err := d.conn.Exec(statement); err == nil {
			t.Fatalf("a launcher row with %s was accepted", name)
		}
	}
}
