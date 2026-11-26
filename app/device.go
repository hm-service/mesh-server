package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"mesh-server/core"
	"mesh-server/db"
)

const (
	// MaxDevices caps the number of device slots (contract/device-api.md).
	MaxDevices = 200
	// TokenTTL is the lifetime of a one-time registration token.
	TokenTTL = 10 * time.Minute
	// OnlineWindow derives device status from last_seen_at.
	OnlineWindow = 90 * time.Second
	// MaxNameRunes bounds a device display name.
	MaxNameRunes = 64
	// MaxConfigBytes bounds one device configuration. 请求体上限是 1MiB，而设备
	// 列表要读全部配置来算派生字段：不在这里收口，200 台设备就能让一次列表请求
	// 同时持有 200MiB。配置只有几 KB，64KiB 已经宽到不必再想。
	MaxConfigBytes = 64 << 10
	// tokenPrefix marks registration tokens.
	tokenPrefix = "etk_"
)

var (
	// ErrNotFound reports a missing device.
	ErrNotFound = db.ErrNotFound
	// ErrLimit reports that the device slots are exhausted.
	ErrLimit = db.ErrLimit
	// ErrGroupNotFound reports a missing device group.
	ErrGroupNotFound = db.ErrGroupNotFound
	// ErrTokenInvalid reports a missing, used or expired registration token.
	ErrTokenInvalid = errors.New("registration token is not usable")
	// ErrInvalidInput reports a name or configuration that cannot be stored.
	ErrInvalidInput = errors.New("invalid device input")
)

// ListDevices returns every device in creation order.
func (a *App) ListDevices() ([]db.Device, error) {
	return a.database.List()
}

// ListGroups returns every device group in display order.
func (a *App) ListGroups() ([]db.Group, error) {
	return a.database.ListGroups()
}

// CreateGroup appends a named group at the end of the group order.
func (a *App) CreateGroup(name string) (db.Group, error) {
	name = trimName(name)
	if name == "" || len([]rune(name)) > MaxNameRunes {
		return db.Group{}, ErrInvalidInput
	}
	return a.database.CreateGroup(name)
}

// UpdateGroup renames and/or moves a group. Nil fields stay unchanged.
func (a *App) UpdateGroup(id int64, name *string, position *int64) (db.Group, error) {
	if name != nil {
		trimmed := trimName(*name)
		if trimmed == "" || len([]rune(trimmed)) > MaxNameRunes {
			return db.Group{}, ErrInvalidInput
		}
		name = &trimmed
	}
	if position != nil && *position < 0 {
		return db.Group{}, ErrInvalidInput
	}
	return a.database.UpdateGroup(id, name, position)
}

// DeleteGroup removes a group; its devices fall back to ungrouped.
func (a *App) DeleteGroup(id int64) error {
	return a.database.DeleteGroup(id)
}

// SetDeviceGroup moves one device to groupID, 0 meaning ungrouped.
func (a *App) SetDeviceGroup(publicID string, groupID int64) error {
	if publicID == "" || groupID < 0 {
		return ErrInvalidInput
	}
	return a.database.SetDeviceGroup(publicID, groupID)
}

// SetDeviceLayout saves the group and position of every dragged device at once.
func (a *App) SetDeviceLayout(items []db.DevicePlacement) error {
	if len(items) == 0 {
		return ErrInvalidInput
	}
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		if item.PublicID == "" || item.GroupID < 0 || item.Position < 0 {
			return ErrInvalidInput
		}
		if _, dup := seen[item.PublicID]; dup {
			return ErrInvalidInput
		}
		seen[item.PublicID] = struct{}{}
	}
	return a.database.SetDeviceLayout(items)
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
// registration token. 配置原样落库：控制面不解释、不填充、不校验它的内容。
func (a *App) CreateDevice(name, configTOML string) (db.Device, string, error) {
	name = trimName(name)
	if name == "" || len([]rune(name)) > MaxNameRunes {
		return db.Device{}, "", ErrInvalidInput
	}
	if isBlank(configTOML) || len(configTOML) > MaxConfigBytes {
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

// AutoUpdateChoice 是一次设备级自动更新写入。Set 为 false 表示这次请求没有带
// 这个字段（不改这一列）；Value 为 nil 表示恢复跟随发布包。用两个字段而不是
// *bool，是因为「没带」和「null」在请求里是两件事。
type AutoUpdateChoice struct {
	Set   bool
	Value *bool
}

// UpdateDevice replaces the display name and the assigned configuration.
//
// 配置原样落库：地址写在配置里，控制面不再替它保留或改写，也不判断它与别的
// 设备是否一样——那可能是多网卡、迁移期并存或临时排查时的有意为之。
func (a *App) UpdateDevice(publicID, name, configTOML string, autoUpdate AutoUpdateChoice) (db.Device, error) {
	name = trimName(name)
	if name == "" || len([]rune(name)) > MaxNameRunes {
		return db.Device{}, ErrInvalidInput
	}
	if isBlank(configTOML) || len(configTOML) > MaxConfigBytes {
		return db.Device{}, ErrInvalidInput
	}
	if _, err := a.database.Get(publicID); err != nil {
		return db.Device{}, err
	}
	hash := core.Hash(configTOML)
	return a.database.Update(publicID, func(d *db.Device) error {
		d.Name = name
		d.ConfigTOML = configTOML
		d.ConfigHash = hash
		if autoUpdate.Set {
			d.AutoUpdate = autoUpdate.Value
		}
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

// ConsumeToken clears a registration token that has just been used to prove
// ownership of an already bound slot. 绑定那一支在 BindDevice 里清，这一支要
// 单独清：令牌是一次性的，否则它在剩余有效期内还能反复取回配置。
func (a *App) ConsumeToken(publicID string) error {
	_, err := a.database.Update(publicID, func(d *db.Device) error {
		d.Token = ""
		d.TokenExpiresAt = 0
		return nil
	})
	return err
}

// DeleteDevice removes the slot and remembers its device_id so later signed
// requests are answered with code "revoked" (contract/client.md). The tombstone
// only lives in memory, exactly as before.
func (a *App) DeleteDevice(ctx context.Context, publicID string) (db.Device, error) {
	// 先撤销凭据再删记录：撤销失败就让删除失败，否则这台设备还能用旧凭据
	// 一直连到过期，而控制面已经没有它的记录了。撤销走调用方的 context：
	// 客户端断开后不该再占着种子逐张等下去。
	if err := a.RevokeCredential(ctx, publicID); err != nil {
		return db.Device{}, fmt.Errorf("revoke credentials: %w", err)
	}
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

// RecordHeartbeat stores the config hash and app version a device reported
// together with its platform.
func (a *App) RecordHeartbeat(publicID, configHash, appVersion, platform string) error {
	return a.database.RecordHeartbeat(publicID, configHash, appVersion, platform, a.now().Unix())
}

// migrateConfigHashes 按当前算法重算每台设备的 config_hash，只写不一致的行。
// 摘要算法变过而存量行没跟着变时，客户端现算的值永远对不上，设备会一直
// stale，每轮心跳都被重发一遍配置。幂等，每次启动都可以跑。
func (a *App) migrateConfigHashes() error {
	devices, err := a.database.List()
	if err != nil {
		return err
	}
	for _, device := range devices {
		want := core.Hash(device.ConfigTOML)
		if device.ConfigHash == want {
			continue
		}
		if _, err := a.database.Update(device.PublicID, func(d *db.Device) error {
			d.ConfigHash = want
			return nil
		}); err != nil {
			return fmt.Errorf("migrate config hash of %s: %w", device.PublicID, err)
		}
	}
	return nil
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

// AppState reports whether the device already runs a build at least as new as
// the one published for its platform (contract/updates.md). With nothing published
// there is nothing to update to, so it counts as current, even for a device that
// has never reported a platform. A device running a newer build than the
// published one counts as current as well: the control plane only ever asks a
// client to upgrade (NeedsUpdate).
func AppState(d db.Device, r db.Release) string {
	if NeedsUpdate(d.LastAppVersion, r.Version) {
		return "stale"
	}
	return "current"
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
