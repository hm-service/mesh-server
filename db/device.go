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
	// ErrIdentityChanged reports a slot that no longer holds the identity a
	// request was authenticated as: a rebind happened in between.
	ErrIdentityChanged = errors.New("device identity changed")
)

// Device is one row of the device table. The field names follow the wire
// contract of contract/server.md.
type Device struct {
	PublicID       string
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
	LastAppVersion string
	LastPlatform   string
	GroupID        int64
	Position       int64
	// AutoUpdate 是设备级自动更新覆盖：nil 跟随发布包，true 强制自动，false
	// 强制手动（contract/admin-api.md）。
	AutoUpdate *bool
}

const deviceColumns = "public_id, name, device_id, hostname, pubkey, config_toml, config_hash, " +
	"token, token_expires_at, created_at, last_seen_at, last_ip, last_config_hash, last_app_version, " +
	"last_platform, group_id, position, auto_update"

const insertDevice = "INSERT INTO device (" + deviceColumns + ") VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)"

type rowScanner interface {
	Scan(dest ...any) error
}

func scanDevice(row rowScanner) (Device, error) {
	var d Device
	err := row.Scan(&d.PublicID, &d.Name, &d.DeviceID, &d.Hostname, &d.Pubkey,
		&d.ConfigTOML, &d.ConfigHash, &d.Token, &d.TokenExpiresAt, &d.CreatedAt, &d.LastSeenAt,
		&d.LastIP, &d.LastConfigHash, &d.LastAppVersion, &d.LastPlatform, &d.GroupID, &d.Position,
		&d.AutoUpdate)
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
	// 新设备排在未分组末尾；既有行的 position 都是 0，仍保持创建顺序。
	if err := tx.QueryRow("SELECT COALESCE(MAX(position), -1) + 1 FROM device WHERE group_id = 0").Scan(&dev.Position); err != nil {
		return fmt.Errorf("next device position: %w", err)
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
		dev.PublicID, dev.Name, dev.DeviceID, dev.Hostname, dev.Pubkey,
		dev.ConfigTOML, dev.ConfigHash, dev.Token, dev.TokenExpiresAt, dev.CreatedAt,
		dev.LastSeenAt, dev.LastIP, dev.LastConfigHash, dev.LastAppVersion, dev.LastPlatform,
		dev.GroupID, dev.Position, dev.AutoUpdate,
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
		"last_config_hash=?, last_app_version=?, last_platform=?, group_id=?, position=?, "+
		"auto_update=? WHERE public_id=?",
		device.Name, device.DeviceID, device.Hostname, device.Pubkey, device.ConfigTOML,
		device.ConfigHash, device.Token, device.TokenExpiresAt, device.CreatedAt, device.LastSeenAt,
		device.LastIP, device.LastConfigHash, device.LastAppVersion, device.LastPlatform, device.GroupID,
		device.Position, device.AutoUpdate, publicID,
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

// Observation is what one authenticated request observed about its own device.
type Observation struct {
	ConfigHash string
	AppVersion string
	Platform   string
	IP         string
}

// Observe folds one authenticated request into the device row in a single
// conditional write and returns the row that write produced: the snapshot the
// caller decides its answer from. The condition carries the identity that
// authenticated — deviceID and the pubkey the signature was verified with — so a
// slot rebound since authentication (even to the same device_id with another
// key) is left untouched and reports ErrIdentityChanged; a deleted one reports
// ErrNotFound. An empty IP leaves the stored one alone.
func (d *DB) Observe(publicID, deviceID, pubkey string, o Observation, now int64) (Device, error) {
	tx, err := d.conn.Begin()
	if err != nil {
		return Device{}, fmt.Errorf("begin observe device: %w", err)
	}
	defer tx.Rollback()
	res, err := tx.Exec("UPDATE device SET last_seen_at = ?, last_ip = COALESCE(NULLIF(?, ''), last_ip), "+
		"last_config_hash = ?, last_app_version = ?, last_platform = ? "+
		"WHERE public_id = ? AND device_id = ? AND pubkey = ?",
		now, o.IP, o.ConfigHash, o.AppVersion, o.Platform, publicID, deviceID, pubkey)
	if err != nil {
		return Device{}, fmt.Errorf("observe device: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return Device{}, fmt.Errorf("observe device: %w", err)
	}
	if affected == 0 {
		return Device{}, observeMiss(tx, publicID, deviceID, pubkey)
	}
	device, err := scanDevice(tx.QueryRow("SELECT "+deviceColumns+" FROM device WHERE public_id = ?", publicID))
	if err != nil {
		return Device{}, fmt.Errorf("observe device: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Device{}, fmt.Errorf("commit observe device: %w", err)
	}
	return device, nil
}

// observeMiss explains a conditional write that matched no row: the slot is
// gone, or it now belongs to another identity — another device_id, or the same
// device_id bound to another key.
func observeMiss(tx *sql.Tx, publicID, deviceID, pubkey string) error {
	var currentID, currentKey string
	err := tx.QueryRow("SELECT device_id, pubkey FROM device WHERE public_id = ?", publicID).Scan(&currentID, &currentKey)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("observe device: %w", err)
	}
	if currentID != deviceID || currentKey != pubkey {
		return ErrIdentityChanged
	}
	return ErrNotFound
}

// PurgeExpiredTokens clears registration tokens whose validity has ended; the
// plaintext token is only kept until token_expires_at (contract/server.md).
func (d *DB) PurgeExpiredTokens(now int64) (int64, error) {
	res, err := d.conn.Exec("UPDATE device SET token = '', token_expires_at = 0 WHERE token <> '' AND token_expires_at <= ?", now)
	if err != nil {
		return 0, fmt.Errorf("purge expired tokens: %w", err)
	}
	return res.RowsAffected()
}
