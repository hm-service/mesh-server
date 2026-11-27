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
	// TokenLength is the exact shape of a registration token: 64 characters,
	// lowercase hex only, which is 32 bytes of entropy. The control plane
	// refuses anything else before it looks at the database, so a token-shaped
	// string pasted from somewhere else cannot reach the expiry branch and blur
	// "wrong shape" into "unknown token".
	TokenLength = 64
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
	// ErrIdentityChanged reports a request whose authenticated identity no longer
	// holds the slot: a rebind landed in between.
	ErrIdentityChanged = db.ErrIdentityChanged
	// ErrPubkeyMismatch reports a request that proved it owns the slot but
	// presented another key.
	ErrPubkeyMismatch = errors.New("device slot is bound to another pubkey")
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

// ValidToken reports whether s has the shape every registration token has: 64
// characters of 0-9a-f, i.e. 32 bytes of lowercase hex. The shape is part of the
// contract: admins copy tokens around, and a token that is one character off
// should fail at the door.
func ValidToken(s string) bool {
	if len(s) != TokenLength {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// DeviceByToken returns the device holding a registration token. A token of
// the wrong shape is answered as "no such device" without touching the
// database.
func (a *App) DeviceByToken(token string) (db.Device, bool, error) {
	if !ValidToken(token) {
		return db.Device{}, false, nil
	}
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
	// 生成端也自检一次：形状变了（改常量、改编码）要在这里炸，而不是让每个
	// 刚发出的令牌在注册时被自己拒掉。
	if !ValidToken(token) {
		return "", fmt.Errorf("generated token does not match the %d-character shape", TokenLength)
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
// requests are answered with code "revoked" (contract/client.md). The tombstone
// only lives in memory, exactly as before.
func (a *App) DeleteDevice(ctx context.Context, publicID string) (db.Device, error) {
	unlock := a.locks.lock(publicID)
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

// BindDevice consumes token and binds the slot to a client identity. The
// previous identity of the same slot, if any, is revoked, and the observations
// that identity reported are cleared. 槽位名下的凭据属于旧身份，也要在交接时
// 撤掉：否则旧机器还能拿着它连到过期，而控制面认为这个槽位已经换人了。
func (a *App) BindDevice(ctx context.Context, token, deviceID, hostname, pubkey, ip string) (db.Device, error) {
	pending, ok, err := a.DeviceByToken(token)
	if err != nil {
		return db.Device{}, err
	}
	if !ok {
		return db.Device{}, ErrTokenInvalid
	}
	unlock := a.locks.lock(pending.PublicID)
	defer unlock()
	// 撤销放在换绑之前：撤不掉就不换绑（令牌还在，客户端可以重试），旧身份也
	// 还能照常用到下一次。撤销成功而换绑失败（令牌在这两步之间被轮换或过期）
	// 的代价是一张凭据提前没了，下一次心跳会立刻补一张——比「换绑了但旧凭据
	// 还活着」便宜得多。
	if err := a.revokeDeviceLocked(ctx, pending.PublicID); err != nil {
		return db.Device{}, fmt.Errorf("revoke credentials: %w", err)
	}
	now := a.now()
	var rebound string
	device, err := a.database.Update(pending.PublicID, func(d *db.Device) error {
		if d.Token != token || d.TokenExpiresAt <= now.Unix() {
			return ErrTokenInvalid
		}
		// 槽位自身的属性（名称、分组、布局、配置）属于这一行记录，换绑不改。
		// last_config_hash、last_app_version、last_platform 是已绑定设备上报的
		// 观测值，只对原身份成立；身份一变就清空，与新身份写在同一次 Update 里。
		if d.DeviceID != deviceID {
			d.LastConfigHash = ""
			d.LastAppVersion = ""
			d.LastPlatform = ""
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

// DeviceAuth is one signed device request that authentication accepted: the slot
// the signature was checked against and the identity that signed it. The API
// layer produces it; every entry point below re-checks the binding under the
// device lock before it reads or writes anything.
type DeviceAuth struct {
	PublicID string
	DeviceID string
}

// BoundRegistration is one register request for a slot that is already bound,
// after the API layer established what proof it carries: a signature it verified
// (Signed), or the registration token to spend here.
type BoundRegistration struct {
	DeviceID string
	Pubkey   string
	Token    string
	Signed   bool
	IP       string
}

// AcceptRegistration answers an already bound slot's register request: it
// re-checks the binding, spends the token when that was the proof, and records
// the request — in one conditional write, returning the row it wrote. That row
// is the configuration the caller answers with.
//
// 令牌是一次性的：这一支靠它证明了归属就当场清掉，否则它在剩余的有效期（≤10
// 分钟）内还能反复取回配置，而正式节点的配置里有 network_secret。核对与清空在
// 同一次写入里完成，令牌在这期间被轮换或消费时不再放行配置。
//
// 换绑发生在认证与这里之间时什么都不写：旧身份不再持有这个槽位，调用方也不能
// 再用认证时读到的快照回答。
func (a *App) AcceptRegistration(publicID string, in BoundRegistration) (db.Device, error) {
	unlock := a.locks.lock(publicID)
	defer unlock()
	now := a.now()
	return a.database.Update(publicID, func(d *db.Device) error {
		if d.DeviceID != in.DeviceID {
			return ErrIdentityChanged
		}
		// 令牌先验、公钥后判：反过来的话，任何知道 device_id 的调用方都能用一次
		// 必然失败的注册（假令牌 + 猜的公钥）从 409 探出这个槽位是否已绑定。
		if !in.Signed && (d.Token != in.Token || d.TokenExpiresAt <= now.Unix()) {
			return ErrTokenInvalid
		}
		if d.Pubkey != in.Pubkey {
			return ErrPubkeyMismatch
		}
		if !in.Signed {
			d.Token = ""
			d.TokenExpiresAt = 0
		}
		d.LastSeenAt = now.Unix()
		if in.IP != "" {
			d.LastIP = in.IP
		}
		return nil
	})
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
	// 32 字节 → 64 个十六进制字符，正好是 TokenLength。
	return core.RandomHex(TokenLength / 2)
}
