package db

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

func TestGroupRoundTrip(t *testing.T) {
	d := openTemp(t)
	if groups, err := d.ListGroups(); err != nil || len(groups) != 0 {
		t.Fatalf("ListGroups() on empty database = %+v, %v", groups, err)
	}
	first, err := d.CreateGroup("办公")
	if err != nil {
		t.Fatalf("CreateGroup() error: %v", err)
	}
	second, err := d.CreateGroup("家里")
	if err != nil {
		t.Fatalf("CreateGroup() second error: %v", err)
	}
	if first.Position != 0 || second.Position != 1 || first.ID == second.ID {
		t.Fatalf("created groups = %+v, %+v", first, second)
	}
	groups, err := d.ListGroups()
	if err != nil || len(groups) != 2 || groups[0].Name != "办公" || groups[1].Name != "家里" {
		t.Fatalf("ListGroups() = %+v, %v", groups, err)
	}

	name := "办公室"
	position := int64(5)
	updated, err := d.UpdateGroup(first.ID, &name, &position)
	if err != nil {
		t.Fatalf("UpdateGroup() error: %v", err)
	}
	if updated.Name != name || updated.Position != position {
		t.Fatalf("updated group = %+v", updated)
	}
	groups, err = d.ListGroups()
	if err != nil || len(groups) != 2 || groups[0].ID != second.ID || groups[1].ID != first.ID {
		t.Fatalf("group order after move = %+v, %v", groups, err)
	}

	if _, err := d.UpdateGroup(999, nil, nil); !errors.Is(err, ErrGroupNotFound) {
		t.Fatalf("UpdateGroup(unknown) = %v, want ErrGroupNotFound", err)
	}
	if err := d.DeleteGroup(first.ID); err != nil {
		t.Fatalf("DeleteGroup() error: %v", err)
	}
	if err := d.DeleteGroup(first.ID); !errors.Is(err, ErrGroupNotFound) {
		t.Fatalf("DeleteGroup() twice = %v, want ErrGroupNotFound", err)
	}
	groups, err = d.ListGroups()
	if err != nil || len(groups) != 1 || groups[0].ID != second.ID {
		t.Fatalf("ListGroups() after delete = %+v, %v", groups, err)
	}
}

func TestDeviceGroupMembership(t *testing.T) {
	d := openTemp(t)
	for _, id := range []string{"d_1", "d_2"} {
		if err := d.Create(Device{PublicID: id}, 10); err != nil {
			t.Fatal(err)
		}
	}
	group, err := d.CreateGroup("办公")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetDeviceGroup("d_1", group.ID); err != nil {
		t.Fatalf("SetDeviceGroup(d_1) error: %v", err)
	}
	if err := d.SetDeviceGroup("d_2", group.ID); err != nil {
		t.Fatalf("SetDeviceGroup(d_2) error: %v", err)
	}
	one, err := d.Get("d_1")
	if err != nil || one.GroupID != group.ID || one.Position != 0 {
		t.Fatalf("d_1 after join = %+v, %v", one, err)
	}
	two, err := d.Get("d_2")
	if err != nil || two.GroupID != group.ID || two.Position != 1 {
		t.Fatalf("d_2 after join = %+v, %v", two, err)
	}
	if err := d.SetDeviceGroup("d_1", 0); err != nil {
		t.Fatalf("SetDeviceGroup(d_1, ungrouped) error: %v", err)
	}
	one, err = d.Get("d_1")
	if err != nil || one.GroupID != 0 {
		t.Fatalf("d_1 after leave = %+v, %v", one, err)
	}
	if err := d.SetDeviceGroup("d_missing", group.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetDeviceGroup(unknown device) = %v, want ErrNotFound", err)
	}
	if err := d.SetDeviceGroup("d_2", 999); !errors.Is(err, ErrGroupNotFound) {
		t.Fatalf("SetDeviceGroup(unknown group) = %v, want ErrGroupNotFound", err)
	}
	if two, err = d.Get("d_2"); err != nil || two.GroupID != group.ID {
		t.Fatalf("d_2 changed by a rejected move: %+v, %v", two, err)
	}
}

func TestDeleteGroupUngroupsDevices(t *testing.T) {
	d := openTemp(t)
	group, err := d.CreateGroup("临时")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"d_1", "d_2"} {
		if err := d.Create(Device{PublicID: id, GroupID: group.ID}, 10); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.DeleteGroup(group.ID); err != nil {
		t.Fatalf("DeleteGroup() error: %v", err)
	}
	if n, err := d.Count(); err != nil || n != 2 {
		t.Fatalf("Count() = %d, %v; want the devices kept", n, err)
	}
	for _, id := range []string{"d_1", "d_2"} {
		device, err := d.Get(id)
		if err != nil || device.GroupID != 0 {
			t.Fatalf("%s after group delete = %+v, %v", id, device, err)
		}
	}
}

func TestDeviceLayoutPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mesh.db")
	first, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	office, err := first.CreateGroup("办公")
	if err != nil {
		t.Fatal(err)
	}
	home, err := first.CreateGroup("家里")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"d_1", "d_2", "d_3"} {
		if err := first.Create(Device{PublicID: id}, 10); err != nil {
			t.Fatal(err)
		}
	}
	layout := []DevicePlacement{
		{PublicID: "d_1", GroupID: home.ID, Position: 1},
		{PublicID: "d_2", GroupID: office.ID, Position: 0},
		{PublicID: "d_3", GroupID: home.ID, Position: 0},
	}
	if err := first.SetDeviceLayout(layout); err != nil {
		t.Fatalf("SetDeviceLayout() error: %v", err)
	}
	if err := first.SetDeviceLayout([]DevicePlacement{{PublicID: "d_1", GroupID: 999, Position: 0}}); !errors.Is(err, ErrGroupNotFound) {
		t.Fatalf("SetDeviceLayout(unknown group) = %v, want ErrGroupNotFound", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}

	second, err := Open(path)
	if err != nil {
		t.Fatalf("reopen error: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })
	want := map[string]DevicePlacement{
		"d_1": {PublicID: "d_1", GroupID: home.ID, Position: 1},
		"d_2": {PublicID: "d_2", GroupID: office.ID, Position: 0},
		"d_3": {PublicID: "d_3", GroupID: home.ID, Position: 0},
	}
	for id, placement := range want {
		device, err := second.Get(id)
		if err != nil {
			t.Fatalf("Get(%s) error: %v", id, err)
		}
		if device.GroupID != placement.GroupID || device.Position != placement.Position {
			t.Fatalf("%s after reopen = group %d position %d, want %d/%d",
				id, device.GroupID, device.Position, placement.GroupID, placement.Position)
		}
	}
}

// TestOpenMigratesExistingDatabase proves that a database created before groups
// existed gains the columns and the table without losing its devices, and that
// the leftovers of the removed JSON import (device.legacy_id and the setting
// table) are dropped.
func TestOpenMigratesExistingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mesh.db")
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open old database: %v", err)
	}
	const oldSchema = `CREATE TABLE device (
	    id               INTEGER PRIMARY KEY AUTOINCREMENT,
	    public_id        TEXT    NOT NULL UNIQUE,
	    legacy_id        TEXT    NOT NULL DEFAULT '',
	    name             TEXT    NOT NULL DEFAULT '',
	    device_id        TEXT    NOT NULL DEFAULT '',
	    hostname         TEXT    NOT NULL DEFAULT '',
	    pubkey           TEXT    NOT NULL DEFAULT '',
	    config_toml      TEXT    NOT NULL DEFAULT '',
	    config_hash      TEXT    NOT NULL DEFAULT '',
	    token            TEXT    NOT NULL DEFAULT '',
	    token_expires_at INTEGER NOT NULL DEFAULT 0,
	    created_at       INTEGER NOT NULL DEFAULT 0,
	    last_seen_at     INTEGER NOT NULL DEFAULT 0,
	    last_ip          TEXT    NOT NULL DEFAULT '',
	    last_config_hash TEXT    NOT NULL DEFAULT '',
	    last_app_hash    TEXT    NOT NULL DEFAULT ''
	)`
	if _, err := conn.Exec(oldSchema); err != nil {
		conn.Close()
		t.Fatalf("create old schema: %v", err)
	}
	const oldSetting = `CREATE TABLE setting (
	    id            INTEGER PRIMARY KEY AUTOINCREMENT,
	    app_version   TEXT    NOT NULL DEFAULT '',
	    app_url       TEXT    NOT NULL DEFAULT '',
	    app_hash      TEXT    NOT NULL DEFAULT '',
	    app_sha256    TEXT    NOT NULL DEFAULT '',
	    app_notes     TEXT    NOT NULL DEFAULT ''
	)`
	if _, err := conn.Exec(oldSetting); err != nil {
		conn.Close()
		t.Fatalf("create old setting table: %v", err)
	}
	if _, err := conn.Exec("INSERT INTO device (public_id, name) VALUES ('d_old', '旧设备')"); err != nil {
		conn.Close()
		t.Fatalf("insert old device: %v", err)
	}
	if _, err := conn.Exec("INSERT INTO setting (app_version) VALUES ('0.1.0')"); err != nil {
		conn.Close()
		t.Fatalf("insert old setting: %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("close old database: %v", err)
	}

	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open() on old database error: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	device, err := d.Get("d_old")
	if err != nil || device.GroupID != 0 || device.Position != 0 || device.Name != "旧设备" {
		t.Fatalf("migrated device = %+v, %v", device, err)
	}
	group, err := d.CreateGroup("新分组")
	if err != nil {
		t.Fatalf("CreateGroup() on migrated database error: %v", err)
	}
	if err := d.SetDeviceGroup("d_old", group.ID); err != nil {
		t.Fatalf("SetDeviceGroup() on migrated database error: %v", err)
	}
	if found, err := d.hasColumn("device", "legacy_id"); err != nil || found {
		t.Fatalf("device.legacy_id survived the migration: found %v, %v", found, err)
	}
	var settings int
	if err := d.conn.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'setting'").Scan(&settings); err != nil {
		t.Fatalf("inspect old setting table: %v", err)
	}
	if settings != 0 {
		t.Fatal("the setting table survived the migration")
	}
}
