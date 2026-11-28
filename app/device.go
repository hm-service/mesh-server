package app

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"mesh-server/core"
	"mesh-server/db"
)

const (
	// MaxDevices caps the number of device slots (contract/device-api.md).
	MaxDevices = 200
	// OnlineWindow derives device status from last_seen_at.
	OnlineWindow = 90 * time.Second
	// MaxNameRunes bounds a device display name.
	MaxNameRunes = 64
	// MaxConfigBytes bounds one device configuration. 请求体上限是 1MiB，而设备
	// 列表要读全部配置来算派生字段：不在这里收口，200 台设备就能让一次列表请求
	// 同时持有 200MiB。配置只有几 KB，64KiB 已经宽到不必再想。
	MaxConfigBytes = 64 << 10
)

var (
	// ErrNotFound reports a missing device.
	ErrNotFound = db.ErrNotFound
	// ErrLimit reports that the device slots are exhausted.
	ErrLimit = db.ErrLimit
	// ErrGroupNotFound reports a missing device group.
	ErrGroupNotFound = db.ErrGroupNotFound
	// ErrPubkeyTaken reports a key another slot already claims.
	ErrPubkeyTaken = db.ErrPubkeyTaken
	// ErrDeviceIDTaken reports a device_id another slot already holds.
	ErrDeviceIDTaken = db.ErrDeviceIDTaken
	// ErrPublicKeyInvalid reports a key that cannot be a device identity: not
	// base64 of 32 bytes, or not a point edwards25519 can decode.
	ErrPublicKeyInvalid = errors.New("device public key is not usable")
	// ErrInvalidInput reports a name or configuration that cannot be stored.
	ErrInvalidInput = errors.New("invalid device input")
	// ErrIdentityChanged reports a request whose authenticated identity no longer
	// holds the slot: a rebind landed in between.
	ErrIdentityChanged = db.ErrIdentityChanged
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

// DeviceByPubkey returns the slot whose key is pubkey. 公钥就是槽位的身份：注册
// 请求除了它没有别的句柄能让控制面找到「还没被认领的那个槽位」。
func (a *App) DeviceByPubkey(pubkey string) (db.Device, bool, error) {
	if err := CheckDevicePubkey(pubkey); err != nil {
		return db.Device{}, false, nil
	}
	device, err := a.database.GetByPubkey(pubkey)
	if errors.Is(err, db.ErrNotFound) {
		return db.Device{}, false, nil
	}
	return device, err == nil, err
}

// CheckDevicePubkey reports whether s can be a device identity: base64 of 32
// bytes that edwards25519 can decode. 形状对了但不在曲线上的公钥永远既签不出
// 有效签名也解不开信封，与其让它变成一个永远等不到认领的槽位，不如在写入时
// 就拒绝。
func CheckDevicePubkey(s string) error {
	if err := core.CheckPubkey(s); err != nil {
		return fmt.Errorf("%w: %v", ErrPublicKeyInvalid, err)
	}
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrPublicKeyInvalid, err)
	}
	if _, err := montgomeryPublicKey(raw); err != nil {
		return fmt.Errorf("%w: %v", ErrPublicKeyInvalid, err)
	}
	return nil
}

// CreateDevice stores a new slot bound to pubkey. 配置原样落库：控制面不解释、
// 不填充、不校验它的内容。槽位在设备首次注册之前就叫「待认领」，但它的钥匙从
// 这一刻起已经确定——没有可传递的注册令牌。
func (a *App) CreateDevice(name, configTOML, pubkey string) (db.Device, error) {
	name = trimName(name)
	if name == "" || len([]rune(name)) > MaxNameRunes {
		return db.Device{}, ErrInvalidInput
	}
	if isBlank(configTOML) || len(configTOML) > MaxConfigBytes {
		return db.Device{}, ErrInvalidInput
	}
	if err := CheckDevicePubkey(pubkey); err != nil {
		return db.Device{}, err
	}
	id, err := a.newDeviceID()
	if err != nil {
		return db.Device{}, err
	}
	now := a.now()
	device := db.Device{
		PublicID:   id,
		Name:       name,
		Pubkey:     pubkey,
		ConfigTOML: configTOML,
		ConfigHash: core.Hash(configTOML),
		CreatedAt:  now.Unix(),
	}
	if err := a.database.Create(device, MaxDevices); err != nil {
		return db.Device{}, err
	}
	return device, nil
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

// RebindDevice points the slot at another key and takes it back to unclaimed.
// 换钥匙是「这台机器没了、重装了、私钥文件丢了」的表达：旧钥匙的签名立刻失效，
// 旧身份名下的凭据在种子上一并撤销，槽位等新钥匙来认领。撤销失败就不换——否则
// 旧身份还能拿着旧凭据连到过期，而槽位已经属于别人。
func (a *App) RebindDevice(ctx context.Context, publicID, pubkey string) (db.Device, error) {
	if err := CheckDevicePubkey(pubkey); err != nil {
		return db.Device{}, err
	}
	unlock, err := a.locks.lock(ctx, publicID)
	if err != nil {
		return db.Device{}, err
	}
	defer unlock()
	previous, ok, err := a.DeviceByID(publicID)
	if err != nil {
		return db.Device{}, err
	}
	if !ok {
		return db.Device{}, ErrNotFound
	}
	if previous.Pubkey == pubkey && previous.DeviceID == "" {
		return previous, nil
	}
	if err := a.revokeDeviceLocked(ctx, publicID); err != nil {
		return db.Device{}, fmt.Errorf("revoke credentials: %w", err)
	}
	device, err := a.database.Update(publicID, func(d *db.Device) error {
		d.Pubkey = pubkey
		d.DeviceID = ""
		d.Hostname = ""
		d.LastConfigHash = ""
		d.LastAppVersion = ""
		d.LastPlatform = ""
		return nil
	})
	if err != nil {
		return db.Device{}, err
	}
	a.revoke(previous.DeviceID)
	return device, nil
}

// DeleteDevice removes the slot and remembers its device_id so later signed
// requests are answered with code "revoked" (contract/client.md). The tombstone
// only lives in memory, exactly as before.
func (a *App) DeleteDevice(ctx context.Context, publicID string) (db.Device, error) {
	unlock, err := a.locks.lock(ctx, publicID)
	if err != nil {
		return db.Device{}, err
	}
	defer unlock()
	// 先撤销凭据再删记录：撤销失败就让删除失败，否则这台设备还能用旧凭据
	// 一直连到过期，而控制面已经没有它的记录了。撤销走调用方的 context：
	// 客户端断开后不该再占着种子逐张等下去。
	if err := a.revokeDeviceLocked(ctx, publicID); err != nil {
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

// Claim is one register request after its signature was verified: the slot the
// key belongs to, the key that signed, and the identity asking to hold it.
type Claim struct {
	PublicID     string
	ProvedPubkey string
	DeviceID     string
	Hostname     string
	IP           string
}

// ClaimDevice binds the slot to the identity that proved it holds the key, and
// returns the row it wrote — that row is the configuration the caller answers
// with. 换绑（同一把钥匙、另一个 device_id）不需要额外证明：签名已经证明了钥匙，
// 而钥匙就是这个槽位。旧身份名下的凭据属于旧身份，先撤再换；撤销失败就不换，
// 客户端可以重试。
func (a *App) ClaimDevice(ctx context.Context, in Claim) (db.Device, error) {
	unlock, err := a.locks.lock(ctx, in.PublicID)
	if err != nil {
		return db.Device{}, err
	}
	defer unlock()
	current, ok, err := a.DeviceByID(in.PublicID)
	if err != nil {
		return db.Device{}, err
	}
	if !ok {
		return db.Device{}, ErrNotFound
	}
	// 认证与这里之间换过钥匙：这次签名不再说明「本机持有这个槽位」。
	if current.Pubkey != in.ProvedPubkey {
		return db.Device{}, ErrIdentityChanged
	}
	if current.DeviceID != in.DeviceID {
		if err := a.revokeDeviceLocked(ctx, in.PublicID); err != nil {
			return db.Device{}, fmt.Errorf("revoke credentials: %w", err)
		}
	}
	now := a.now()
	device, err := a.database.Update(in.PublicID, func(d *db.Device) error {
		if d.Pubkey != in.ProvedPubkey {
			return ErrIdentityChanged
		}
		// 槽位自身的属性（名称、分组、布局、配置）属于这一行记录，换绑不改。
		// 观测值只对原身份成立：身份一变就清空，与新身份写在同一次写入里。
		if d.DeviceID != in.DeviceID {
			d.LastConfigHash = ""
			d.LastAppVersion = ""
			d.LastPlatform = ""
		}
		d.DeviceID = in.DeviceID
		d.Hostname = in.Hostname
		d.LastSeenAt = now.Unix()
		if in.IP != "" {
			d.LastIP = in.IP
		}
		return nil
	})
	if err != nil {
		return db.Device{}, err
	}
	if current.DeviceID != device.DeviceID {
		a.revoke(current.DeviceID)
	}
	return device, nil
}

// DeviceAuth is one signed device request that authentication accepted: the slot
// the signature was checked against, the identity that signed it, and the key the
// signature was verified with. The API layer produces it; every entry point below
// re-checks the binding under the device lock before it reads or writes anything.
//
// Pubkey 是判据里不可省的一半：device_id 可以在两次换绑之后又回到原来的值，
// 只有「这把钥匙」能说明这次认证属于当前那一代绑定。
type DeviceAuth struct {
	PublicID string
	DeviceID string
	Pubkey   string
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
	if r.Client != nil && NeedsUpdate(d.LastAppVersion, r.Client.Version) {
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
