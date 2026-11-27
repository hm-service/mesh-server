package app

import (
	"context"
	"log/slog"

	"mesh-server/db"
)

// HeartbeatObservation is what one heartbeat reports about the device it runs on:
// the signed business fields, the credential the core has loaded, and the
// request's source address.
type HeartbeatObservation struct {
	ConfigHash   string
	AppVersion   string
	Platform     string
	CredentialID string
	IP           string
}

// HeartbeatResult is everything one heartbeat resolves to. Every field was
// decided against the same row — the snapshot the observation write produced —
// and nil means "nothing to send".
type HeartbeatResult struct {
	Config     *ConfigDelivery
	Update     *AppUpdateOffer
	Launcher   *LauncherOffer
	Credential *IssuedCredential
}

// ConfigDelivery is the assignment the device's reported hash does not match.
type ConfigDelivery struct {
	Hash string
	TOML string
}

// AppUpdateOffer is the client package published for the device's platform.
// Every field is filled whenever the offer exists: the client decides from the
// version and the digest whether it already holds these bytes.
type AppUpdateOffer struct {
	Version    string
	AutoUpdate bool
	URL        string
	Notes      string
	SHA256     string
	Signature  string
}

// LauncherOffer is the launcher published for the device's platform.
type LauncherOffer struct {
	Version   string
	URL       string
	SHA256    string
	Signature string
}

// Heartbeat is the complete business entry behind POST /api/v1/heartbeat. Under
// the device lock it re-checks that the slot still holds the authenticated
// identity, folds the observation into one conditional write, and plans the
// answer — configuration, app update, launcher and credential step — from the
// row that write produced.
//
// 锁与条件写入一起给出共同的一致性边界：换绑、删除或另一次心跳只能落在这次调用
// 之前或之后，看不到「观察已写、响应仍来自旧绑定」的中间态。凭据步骤失败只记
// 日志、不下发：心跳照常回答，下一轮再试。
func (a *App) Heartbeat(ctx context.Context, auth DeviceAuth, beat HeartbeatObservation) (HeartbeatResult, error) {
	unlock := a.locks.lock(auth.PublicID)
	defer unlock()
	device, err := a.database.Observe(auth.PublicID, auth.DeviceID, db.Observation{
		ConfigHash: beat.ConfigHash,
		AppVersion: beat.AppVersion,
		Platform:   beat.Platform,
		IP:         beat.IP,
	}, a.now().Unix())
	if err != nil {
		return HeartbeatResult{}, err
	}
	release, _, err := a.Release(beat.Platform)
	if err != nil {
		return HeartbeatResult{}, err
	}
	var result HeartbeatResult
	if beat.ConfigHash != device.ConfigHash {
		result.Config = &ConfigDelivery{Hash: device.ConfigHash, TOML: device.ConfigTOML}
	}
	// 只有设备上报的版本低于发布版本才算需要更新：设备版本更高（回滚过发布、
	// 手工装过新包）时按 current 处理，不能让它每 30 秒被要求装同一个旧包。
	//
	// 签名为空的槽位同样不下发：那是加签名之前发布的记录，客户端一律拒装。按
	// 未发布处理，管理端重新发布一次就恢复了。
	if client := release.Client; client != nil && client.Signature != "" && NeedsUpdate(beat.AppVersion, client.Version) {
		// 每次心跳都带完整的一份：「我手上是哪一份」由客户端按版本与摘要自己判，
		// 服务端不持有客户端的内存，也就没有可省的第二种形状。
		result.Update = &AppUpdateOffer{
			Version:    client.Version,
			AutoUpdate: EffectiveAutoUpdate(device.AutoUpdate, client.AutoUpdate),
			URL:        client.URL,
			Notes:      client.Notes,
			SHA256:     client.SHA256,
			Signature:  client.Signature,
		}
	}
	// 启动器不参与签名原文，服务端也就无从比较客户端手里那份，所以只要该平台
	// 配了启动器就每次都带上；客户端自己算本地摘要去比。
	result.Launcher = launcherOffer(release.Launcher)
	issued, err := a.credentialStepLocked(ctx, device, beat.CredentialID)
	if err != nil {
		slog.Error("credential step", "device_id", device.DeviceID, "error", err)
	} else {
		result.Credential = issued
	}
	return result, nil
}

// launcherOffer is the launcher slot as a heartbeat offer; nil when the
// platform has no launcher published. 有摘要没签名 = 这一行是加签名之前发布的：
// 客户端会验签并拒装，那就等于每 30 秒给全网发一个必然失败的槽位；按「没发布」
// 处理，等重新发布。
func launcherOffer(slot *db.LauncherSlot) *LauncherOffer {
	if slot == nil || slot.SHA256 == "" || slot.Signature == "" {
		return nil
	}
	return &LauncherOffer{
		Version:   slot.Version,
		URL:       slot.URL,
		SHA256:    slot.SHA256,
		Signature: slot.Signature,
	}
}
