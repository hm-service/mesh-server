package app

import (
	"errors"
	"fmt"
	"time"

	"mesh-server/core"
	"mesh-server/db"
)

const (
	// MaxDevices caps the number of device slots (CONTRACT.md 1.4).
	MaxDevices = 200
	// TokenTTL is the lifetime of a one-time registration token.
	TokenTTL = 10 * time.Minute
	// OnlineWindow derives device status from last_seen_at.
	OnlineWindow = 90 * time.Second
	// MaxNameRunes bounds a device display name.
	MaxNameRunes = 64
	// tokenPrefix marks registration tokens.
	tokenPrefix = "etk_"
)

var (
	// ErrNotFound reports a missing device.
	ErrNotFound = db.ErrNotFound
	// ErrLimit reports that the device slots are exhausted.
	ErrLimit = db.ErrLimit
	// ErrTokenInvalid reports a missing, used or expired registration token.
	ErrTokenInvalid = errors.New("registration token is not usable")
	// ErrInvalidInput reports a name or configuration that cannot be stored.
	ErrInvalidInput = errors.New("invalid device input")
)

// ListDevices returns every device in creation order.
func (a *App) ListDevices() ([]db.Device, error) {
	return a.database.List()
}

// DeviceByID returns the device with the internal public id.
func (a *App) DeviceByID(publicID string) (db.Device, bool, error) {
	device, err := a.database.Get(publicID)
	if errors.Is(err, db.ErrNotFound) {
		return db.Device{}, false, nil
	}
	return device, err == nil, err
}

// DeviceByDeviceID returns the device bound to the client-generated device_id.
func (a *App) DeviceByDeviceID(deviceID string) (db.Device, bool, error) {
	device, err := a.database.GetByDeviceID(deviceID)
	if errors.Is(err, db.ErrNotFound) {
		return db.Device{}, false, nil
	}
	return device, err == nil, err
}

// DeviceByToken returns the device holding a registration token.
func (a *App) DeviceByToken(token string) (db.Device, bool, error) {
	device, err := a.database.GetByToken(token)
	if errors.Is(err, db.ErrNotFound) {
		return db.Device{}, false, nil
	}
	return device, err == nil, err
}

// CreateDevice stores a new slot and returns it together with its one-time
// registration token.
func (a *App) CreateDevice(name, configTOML string) (db.Device, string, error) {
	name = trimName(name)
	if name == "" || len([]rune(name)) > MaxNameRunes {
		return db.Device{}, "", ErrInvalidInput
	}
	if isBlank(configTOML) {
		return db.Device{}, "", ErrInvalidInput
	}
	id, err := a.newDeviceID()
	if err != nil {
		return db.Device{}, "", err
	}
	token, err := newToken()
	if err != nil {
		return db.Device{}, "", err
	}
	now := a.now()
	device := db.Device{
		PublicID:       id,
		Name:           name,
		ConfigTOML:     configTOML,
		ConfigHash:     core.Hash(configTOML),
		Token:          token,
		TokenExpiresAt: now.Add(TokenTTL).Unix(),
		CreatedAt:      now.Unix(),
	}
	if err := a.database.Create(device, MaxDevices); err != nil {
		return db.Device{}, "", err
	}
	return device, token, nil
}

// UpdateDevice replaces the display name and the assigned configuration.
func (a *App) UpdateDevice(publicID, name, configTOML string) (db.Device, error) {
	name = trimName(name)
	if name == "" || len([]rune(name)) > MaxNameRunes {
		return db.Device{}, ErrInvalidInput
	}
	if isBlank(configTOML) {
		return db.Device{}, ErrInvalidInput
	}
	hash := core.Hash(configTOML)
	return a.database.Update(publicID, func(d *db.Device) error {
		d.Name = name
		d.ConfigTOML = configTOML
		d.ConfigHash = hash
		return nil
	})
}

// IssueToken stores a fresh one-time registration token for the slot.
func (a *App) IssueToken(publicID string) (string, error) {
	token, err := newToken()
	if err != nil {
		return "", err
	}
	now := a.now()
	_, err = a.database.Update(publicID, func(d *db.Device) error {
		d.Token = token
		d.TokenExpiresAt = now.Add(TokenTTL).Unix()
		return nil
	})
	if err != nil {
		return "", err
	}
	return token, nil
}

// DeleteDevice removes the slot and remembers its device_id so later signed
// requests are answered with code "revoked" (CONTRACT.md 2.6). The tombstone
// only lives in memory, exactly as before.
func (a *App) DeleteDevice(publicID string) (db.Device, error) {
	device, err := a.database.Delete(publicID)
	if err != nil {
		return db.Device{}, err
	}
	a.revoke(device.DeviceID)
	return device, nil
}

// Revoked reports whether device_id belonged to a deleted device.
func (a *App) Revoked(deviceID string) bool {
	if deviceID == "" {
		return false
	}
	a.revokedMu.Lock()
	defer a.revokedMu.Unlock()
	_, ok := a.revoked[deviceID]
	return ok
}

func (a *App) revoke(deviceID string) {
	if deviceID == "" {
		return
	}
	a.revokedMu.Lock()
	defer a.revokedMu.Unlock()
	a.revoked[deviceID] = struct{}{}
}

// BindDevice consumes token and binds the slot to a client identity. The
// previous identity of the same slot, if any, is revoked.
func (a *App) BindDevice(token, deviceID, hostname, pubkey, ip string) (db.Device, error) {
	pending, ok, err := a.DeviceByToken(token)
	if err != nil {
		return db.Device{}, err
	}
	if !ok {
		return db.Device{}, ErrTokenInvalid
	}
	now := a.now()
	var rebound string
	device, err := a.database.Update(pending.PublicID, func(d *db.Device) error {
		if d.Token != token || d.TokenExpiresAt <= now.Unix() {
			return ErrTokenInvalid
		}
		rebound = d.DeviceID
		d.DeviceID = deviceID
		d.Hostname = hostname
		d.Pubkey = pubkey
		d.Token = ""
		d.TokenExpiresAt = 0
		d.LastSeenAt = now.Unix()
		if ip != "" {
			d.LastIP = ip
		}
		return nil
	})
	if err != nil {
		return db.Device{}, err
	}
	if rebound != "" && rebound != device.DeviceID {
		a.revoke(rebound)
	}
	return device, nil
}

// Touch records a successful device request.
func (a *App) Touch(publicID, ip string) error {
	return a.database.Touch(publicID, a.now().Unix(), ip)
}

// RecordHeartbeat stores the hashes a device reported together with its platform.
func (a *App) RecordHeartbeat(publicID, configHash, appHash, platform string) error {
	return a.database.RecordHeartbeat(publicID, configHash, appHash, platform, a.now().Unix())
}

// DeviceStatus is derived from last_seen_at; an unregistered slot is pending.
func DeviceStatus(d db.Device, now time.Time) string {
	switch {
	case d.DeviceID == "":
		return "pending"
	case now.Unix()-d.LastSeenAt < int64(OnlineWindow/time.Second):
		return "online"
	default:
		return "offline"
	}
}

// ConfigState reports whether the device already uses the assigned config.
func ConfigState(d db.Device) string {
	if d.LastConfigHash != "" && d.LastConfigHash == d.ConfigHash {
		return "current"
	}
	return "stale"
}

// AppState reports whether the device runs the build published for its platform.
// With nothing published for that platform there is nothing to update to, so it
// counts as current.
func AppState(d db.Device, r db.Release) string {
	// 平台未知说明这台设备还没上报过，它不可能已经在跑对应平台的包。
	if d.LastPlatform == "" {
		return "stale"
	}
	if r.Hash == "" || d.LastAppHash == r.Hash {
		return "current"
	}
	return "stale"
}

// ReleaseVersion labels the published build; empty when nothing is published.
func ReleaseVersion(r db.Release) string {
	if r.Hash == "" {
		return ""
	}
	return r.Version
}

// newDeviceID returns an unused short id. The space is small on purpose: the id
// is a display handle, while device_id is the client identity.
func (a *App) newDeviceID() (string, error) {
	for range 8 {
		raw, err := core.RandomHex(3)
		if err != nil {
			return "", err
		}
		id := "d_" + raw
		if _, found, err := a.DeviceByID(id); err != nil {
			return "", err
		} else if !found {
			return id, nil
		}
	}
	return "", fmt.Errorf("device id collision")
}

func newToken() (string, error) {
	raw, err := core.RandomHex(24)
	if err != nil {
		return "", err
	}
	return tokenPrefix + raw, nil
}
