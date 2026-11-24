package db

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

var (
	// ErrNotFound reports a missing device row.
	ErrNotFound = errors.New("device not found")
	// ErrLimit reports that the device slots are exhausted.
	ErrLimit = errors.New("device limit reached")
)

// Device is one row of the device table. The field names follow the wire
// contract of CONTRACT.md 1.1.
type Device struct {
	PublicID       string
	LegacyID       string
	Name           string
	DeviceID       string
	Hostname       string
	Pubkey         string
	ConfigTOML     string
	ConfigHash     string
	Token          string
	TokenExpiresAt int64
	CreatedAt      int64
	LastSeenAt     int64
	LastIP         string
	LastConfigHash string
	LastAppHash    string
	LastPlatform   string
}

const deviceColumns = "public_id, legacy_id, name, device_id, hostname, pubkey, config_toml, config_hash, " +
	"token, token_expires_at, created_at, last_seen_at, last_ip, last_config_hash, last_app_hash, last_platform"

const insertDevice = "INSERT INTO device (" + deviceColumns + ") VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)"

type rowScanner interface {
	Scan(dest ...any) error
}

func scanDevice(row rowScanner) (Device, error) {
	var d Device
	err := row.Scan(&d.PublicID, &d.LegacyID, &d.Name, &d.DeviceID, &d.Hostname, &d.Pubkey,
		&d.ConfigTOML, &d.ConfigHash, &d.Token, &d.TokenExpiresAt, &d.CreatedAt, &d.LastSeenAt,
		&d.LastIP, &d.LastConfigHash, &d.LastAppHash, &d.LastPlatform)
	return d, err
}

func nowUnix() int64 {
	return time.Now().Unix()
}

// List returns every device in creation order.
func (d *DB) List() ([]Device, error) {
	rows, err := d.conn.Query("SELECT " + deviceColumns + " FROM device ORDER BY id")
	if err != nil {
		return nil, fmt.Errorf("list devices: %w", err)
	}
	defer rows.Close()
	out := make([]Device, 0, 8)
	for rows.Next() {
		device, err := scanDevice(rows)
		if err != nil {
			return nil, fmt.Errorf("scan device: %w", err)
		}
		out = append(out, device)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate devices: %w", err)
	}
	return out, nil
}

// Count returns the number of device slots in use.
func (d *DB) Count() (int, error) {
	var n int
	if err := d.conn.QueryRow("SELECT COUNT(*) FROM device").Scan(&n); err != nil {
		return 0, fmt.Errorf("count devices: %w", err)
	}
	return n, nil
}

// Get returns the device with public id.
func (d *DB) Get(publicID string) (Device, error) {
	device, err := scanDevice(d.conn.QueryRow("SELECT "+deviceColumns+" FROM device WHERE public_id = ?", publicID))
	if errors.Is(err, sql.ErrNoRows) {
		return Device{}, ErrNotFound
	}
	if err != nil {
		return Device{}, fmt.Errorf("get device: %w", err)
	}
	return device, nil
}

// GetByDeviceID returns the device bound to the client-generated device_id.
func (d *DB) GetByDeviceID(deviceID string) (Device, error) {
	if deviceID == "" {
		return Device{}, ErrNotFound
	}
	device, err := scanDevice(d.conn.QueryRow("SELECT "+deviceColumns+" FROM device WHERE device_id = ?", deviceID))
	if errors.Is(err, sql.ErrNoRows) {
		return Device{}, ErrNotFound
	}
	if err != nil {
		return Device{}, fmt.Errorf("get device by device id: %w", err)
	}
	return device, nil
}

// GetByToken returns the device holding token, ignoring empty tokens.
func (d *DB) GetByToken(token string) (Device, error) {
	if token == "" {
		return Device{}, ErrNotFound
	}
	device, err := scanDevice(d.conn.QueryRow("SELECT "+deviceColumns+" FROM device WHERE token = ?", token))
	if errors.Is(err, sql.ErrNoRows) {
		return Device{}, ErrNotFound
	}
	if err != nil {
		return Device{}, fmt.Errorf("get device by token: %w", err)
	}
	return device, nil
}

// Create inserts dev, refusing once max slots are in use. The check and the
// insert share one transaction, so two requests cannot both take the last slot.
func (d *DB) Create(dev Device, max int) error {
	tx, err := d.conn.Begin()
	if err != nil {
		return fmt.Errorf("begin create device: %w", err)
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRow("SELECT COUNT(*) FROM device").Scan(&n); err != nil {
		return fmt.Errorf("count devices: %w", err)
	}
	if n >= max {
		return ErrLimit
	}
	if _, err := tx.Exec(insertDevice, deviceArgs(dev)...); err != nil {
		return fmt.Errorf("insert device: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit create device: %w", err)
	}
	return nil
}

func deviceArgs(dev Device) []any {
	return []any{
		dev.PublicID, dev.LegacyID, dev.Name, dev.DeviceID, dev.Hostname, dev.Pubkey,
		dev.ConfigTOML, dev.ConfigHash, dev.Token, dev.TokenExpiresAt, dev.CreatedAt,
		dev.LastSeenAt, dev.LastIP, dev.LastConfigHash, dev.LastAppHash, dev.LastPlatform,
	}
}

// Update applies mutate to the device with public id inside one transaction and
// reports the stored result. A failing mutate leaves the row untouched.
func (d *DB) Update(publicID string, mutate func(*Device) error) (Device, error) {
	tx, err := d.conn.Begin()
	if err != nil {
		return Device{}, fmt.Errorf("begin update device: %w", err)
	}
	defer tx.Rollback()
	device, err := scanDevice(tx.QueryRow("SELECT "+deviceColumns+" FROM device WHERE public_id = ?", publicID))
	if errors.Is(err, sql.ErrNoRows) {
		return Device{}, ErrNotFound
	}
	if err != nil {
		return Device{}, fmt.Errorf("load device: %w", err)
	}
	if err := mutate(&device); err != nil {
		return Device{}, err
	}
	if _, err := tx.Exec("UPDATE device SET name=?, device_id=?, hostname=?, pubkey=?, config_toml=?, "+
		"config_hash=?, token=?, token_expires_at=?, created_at=?, last_seen_at=?, last_ip=?, "+
		"last_config_hash=?, last_app_hash=?, last_platform=? WHERE public_id=?",
		device.Name, device.DeviceID, device.Hostname, device.Pubkey, device.ConfigTOML,
		device.ConfigHash, device.Token, device.TokenExpiresAt, device.CreatedAt, device.LastSeenAt,
		device.LastIP, device.LastConfigHash, device.LastAppHash, device.LastPlatform, publicID,
	); err != nil {
		return Device{}, fmt.Errorf("update device: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Device{}, fmt.Errorf("commit update device: %w", err)
	}
	return device, nil
}

// Delete removes the device with public id and returns the removed row.
func (d *DB) Delete(publicID string) (Device, error) {
	tx, err := d.conn.Begin()
	if err != nil {
		return Device{}, fmt.Errorf("begin delete device: %w", err)
	}
	defer tx.Rollback()
	device, err := scanDevice(tx.QueryRow("SELECT "+deviceColumns+" FROM device WHERE public_id = ?", publicID))
	if errors.Is(err, sql.ErrNoRows) {
		return Device{}, ErrNotFound
	}
	if err != nil {
		return Device{}, fmt.Errorf("load device: %w", err)
	}
	if _, err := tx.Exec("DELETE FROM device WHERE public_id = ?", publicID); err != nil {
		return Device{}, fmt.Errorf("delete device: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Device{}, fmt.Errorf("commit delete device: %w", err)
	}
	return device, nil
}

// Touch records a successful device request.
func (d *DB) Touch(publicID string, now int64, ip string) error {
	var (
		res sql.Result
		err error
	)
	if ip == "" {
		res, err = d.conn.Exec("UPDATE device SET last_seen_at = ? WHERE public_id = ?", now, publicID)
	} else {
		res, err = d.conn.Exec("UPDATE device SET last_seen_at = ?, last_ip = ? WHERE public_id = ?", now, ip, publicID)
	}
	if err != nil {
		return fmt.Errorf("touch device: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("touch device: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// RecordHeartbeat stores the hashes a device reported together with its
// last_seen_at.
func (d *DB) RecordHeartbeat(publicID, configHash, appHash, platform string, now int64) error {
	res, err := d.conn.Exec("UPDATE device SET last_config_hash = ?, last_app_hash = ?, last_platform = ?, last_seen_at = ? WHERE public_id = ?",
		configHash, appHash, platform, now, publicID)
	if err != nil {
		return fmt.Errorf("record heartbeat: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("record heartbeat: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// PurgeExpiredTokens clears registration tokens whose validity has ended; the
// plaintext token is only kept until token_expires_at (CONTRACT.md 1.1).
func (d *DB) PurgeExpiredTokens(now int64) (int64, error) {
	res, err := d.conn.Exec("UPDATE device SET token = '', token_expires_at = 0 WHERE token <> '' AND token_expires_at <= ?", now)
	if err != nil {
		return 0, fmt.Errorf("purge expired tokens: %w", err)
	}
	return res.RowsAffected()
}

// ImportDevices inserts legacy rows, skipping every one that is already there.
// The legacy_id unique index is what makes repeated imports a no-op.
func (d *DB) ImportDevices(devices []Device) (int, error) {
	tx, err := d.conn.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin import devices: %w", err)
	}
	defer tx.Rollback()
	inserted := 0
	for _, dev := range devices {
		res, err := tx.Exec(insertDevice+" ON CONFLICT DO NOTHING", deviceArgs(dev)...)
		if err != nil {
			return 0, fmt.Errorf("import device %s: %w", dev.PublicID, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf("import device %s: %w", dev.PublicID, err)
		}
		inserted += int(n)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit import devices: %w", err)
	}
	return inserted, nil
}
