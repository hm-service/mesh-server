package db

import (
	"database/sql"
	"errors"
	"fmt"
)

// ErrGroupNotFound reports a missing device group row.
var ErrGroupNotFound = errors.New("group not found")

// Group is one row of the device_group table. position orders the groups in
// the console; devices order themselves through Device.Position.
type Group struct {
	ID       int64
	Name     string
	Position int64
}

// DevicePlacement is one device after a drag: the group it now belongs to and
// its index inside that group. GroupID 0 means ungrouped.
type DevicePlacement struct {
	PublicID string
	GroupID  int64
	Position int64
}

// ListGroups returns every group in display order.
func (d *DB) ListGroups() ([]Group, error) {
	rows, err := d.conn.Query("SELECT id, name, position FROM device_group ORDER BY position, id")
	if err != nil {
		return nil, fmt.Errorf("list groups: %w", err)
	}
	defer rows.Close()
	out := make([]Group, 0, 8)
	for rows.Next() {
		var g Group
		if err := rows.Scan(&g.ID, &g.Name, &g.Position); err != nil {
			return nil, fmt.Errorf("scan group: %w", err)
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate groups: %w", err)
	}
	return out, nil
}

// CreateGroup appends a group at the end of the group order.
func (d *DB) CreateGroup(name string) (Group, error) {
	tx, err := d.conn.Begin()
	if err != nil {
		return Group{}, fmt.Errorf("begin create group: %w", err)
	}
	defer tx.Rollback()
	var position int64
	if err := tx.QueryRow("SELECT COALESCE(MAX(position), -1) + 1 FROM device_group").Scan(&position); err != nil {
		return Group{}, fmt.Errorf("next group position: %w", err)
	}
	res, err := tx.Exec("INSERT INTO device_group (name, position) VALUES (?, ?)", name, position)
	if err != nil {
		return Group{}, fmt.Errorf("insert group: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Group{}, fmt.Errorf("group id: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Group{}, fmt.Errorf("commit create group: %w", err)
	}
	return Group{ID: id, Name: name, Position: position}, nil
}

// UpdateGroup renames and/or moves the group with id. Nil fields are left as
// they are.
func (d *DB) UpdateGroup(id int64, name *string, position *int64) (Group, error) {
	tx, err := d.conn.Begin()
	if err != nil {
		return Group{}, fmt.Errorf("begin update group: %w", err)
	}
	defer tx.Rollback()
	group, err := scanGroup(tx.QueryRow("SELECT id, name, position FROM device_group WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Group{}, ErrGroupNotFound
	}
	if err != nil {
		return Group{}, fmt.Errorf("load group: %w", err)
	}
	if name != nil {
		group.Name = *name
	}
	if position != nil {
		group.Position = *position
	}
	if _, err := tx.Exec("UPDATE device_group SET name = ?, position = ? WHERE id = ?", group.Name, group.Position, id); err != nil {
		return Group{}, fmt.Errorf("update group: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Group{}, fmt.Errorf("commit update group: %w", err)
	}
	return group, nil
}

// DeleteGroup removes the group and moves its devices back to ungrouped. The
// devices themselves stay, with their positions kept so their order survives.
func (d *DB) DeleteGroup(id int64) error {
	tx, err := d.conn.Begin()
	if err != nil {
		return fmt.Errorf("begin delete group: %w", err)
	}
	defer tx.Rollback()
	res, err := tx.Exec("DELETE FROM device_group WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete group: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete group: %w", err)
	}
	if affected == 0 {
		return ErrGroupNotFound
	}
	if _, err := tx.Exec("UPDATE device SET group_id = 0 WHERE group_id = ?", id); err != nil {
		return fmt.Errorf("ungroup devices: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit delete group: %w", err)
	}
	return nil
}

// SetDeviceGroup moves one device to groupID (0 for ungrouped) and puts it at
// the end of that group.
func (d *DB) SetDeviceGroup(publicID string, groupID int64) error {
	tx, err := d.conn.Begin()
	if err != nil {
		return fmt.Errorf("begin set device group: %w", err)
	}
	defer tx.Rollback()
	if err := requireGroup(tx, groupID); err != nil {
		return err
	}
	var position int64
	if err := tx.QueryRow("SELECT COALESCE(MAX(position), -1) + 1 FROM device WHERE group_id = ?", groupID).Scan(&position); err != nil {
		return fmt.Errorf("next device position: %w", err)
	}
	res, err := tx.Exec("UPDATE device SET group_id = ?, position = ? WHERE public_id = ?", groupID, position, publicID)
	if err != nil {
		return fmt.Errorf("set device group: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("set device group: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit set device group: %w", err)
	}
	return nil
}

// SetDeviceLayout stores the group and position of every dragged device in one
// transaction: either the whole new layout is saved or nothing is.
func (d *DB) SetDeviceLayout(items []DevicePlacement) error {
	tx, err := d.conn.Begin()
	if err != nil {
		return fmt.Errorf("begin device layout: %w", err)
	}
	defer tx.Rollback()
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		if item.PublicID == "" {
			return ErrNotFound
		}
		if _, dup := seen[item.PublicID]; dup {
			return fmt.Errorf("device layout: duplicate %s", item.PublicID)
		}
		seen[item.PublicID] = struct{}{}
		if err := requireGroup(tx, item.GroupID); err != nil {
			return err
		}
		res, err := tx.Exec("UPDATE device SET group_id = ?, position = ? WHERE public_id = ?", item.GroupID, item.Position, item.PublicID)
		if err != nil {
			return fmt.Errorf("place device %s: %w", item.PublicID, err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("place device %s: %w", item.PublicID, err)
		}
		if affected == 0 {
			return ErrNotFound
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit device layout: %w", err)
	}
	return nil
}

func scanGroup(row rowScanner) (Group, error) {
	var g Group
	err := row.Scan(&g.ID, &g.Name, &g.Position)
	return g, err
}

// requireGroup reports ErrGroupNotFound unless groupID exists; 0 always means
// ungrouped and needs no row.
func requireGroup(tx *sql.Tx, groupID int64) error {
	if groupID == 0 {
		return nil
	}
	var found int64
	err := tx.QueryRow("SELECT id FROM device_group WHERE id = ?", groupID).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrGroupNotFound
	}
	if err != nil {
		return fmt.Errorf("load group %d: %w", groupID, err)
	}
	return nil
}
