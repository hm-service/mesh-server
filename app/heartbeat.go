package app

import (
	"context"
	"log/slog"

	"mesh-server/db"
)

// HeartbeatObservation is what one heartbeat reports about the device it runs on:
// the signed business fields, the two fields that only choose how much to send,
// and the request's source address.
type HeartbeatObservation struct {
	ConfigHash     string
	AppVersion     string
	Platform       string
	CredentialID   string
	AppUpdateKnown string
	IP             string
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
// Known means the device reported the digest of exactly these bytes, so the
// content fields stay empty and the answer can be the short form.
type AppUpdateOffer struct {
	Version    string
	AutoUpdate bool
	Known      bool
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
	// 签名为空的行同样不下发：那是加签名之前发布的记录，客户端一律拒装。按
	// 未发布处理，管理端重新发布一次就恢复了。
	if release.Signature != "" && NeedsUpdate(beat.AppVersion, release.Version) {
		update := &AppUpdateOffer{
			Version:    release.Version,
			AutoUpdate: EffectiveAutoUpdate(device.AutoUpdate, release.AutoUpdate),
		}
		// 摘要一致才算同一份：同一版本号重新发布时内容会变，只有 sha256 能表达
		// "我手上是哪一份"。已知就只带版本与开关，notes 不必每 30 秒重复一次。
		if beat.AppUpdateKnown != "" && beat.AppUpdateKnown == release.SHA256 {
			update.Known = true
		} else {
			update.URL = release.URL
			update.Notes = release.Notes
			update.SHA256 = release.SHA256
			update.Signature = release.Signature
		}
		result.Update = update
	}
	// 启动器不参与签名原文，服务端也就无从比较客户端手里那份，所以只要该平台
	// 配了启动器就每次都带上；客户端自己算本地摘要去比。
	result.Launcher = launcherOffer(release)
	issued, err := a.credentialStepLocked(ctx, device, beat.CredentialID)
	if err != nil {
		slog.Error("credential step", "device_id", device.DeviceID, "error", err)
	} else {
		result.Credential = issued
	}
	return result, nil
}

// launcherOffer is the launcher slot of release as a heartbeat offer; nil when
// the platform has no launcher published. 有摘要没签名 = 这一行是加签名之前发布
// 的：客户端会验签并拒装，那就等于每 30 秒给全网发一个必然失败的槽位；按「没
// 发布」处理，等重新发布。
func launcherOffer(release db.Release) *LauncherOffer {
	if release.LauncherSHA256 == "" || release.LauncherSignature == "" {
		return nil
	}
	return &LauncherOffer{
		Version:   release.LauncherVersion,
		URL:       release.LauncherURL,
		SHA256:    release.LauncherSHA256,
		Signature: release.LauncherSignature,
	}
}
