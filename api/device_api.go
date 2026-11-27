package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"mesh-server/app"
	"mesh-server/core"
	"mesh-server/db"
	"mesh-server/httpx"
)

const (
	codeUnauthorized = "unauthorized"
	codeRevoked      = "revoked"
)

// registerRequest is POST /api/v1/register. timestamp、nonce、signature、platform
// 只有"槽位已经绑定"时才用得上：它们带着本机私钥的签名，证明调用方就是那个
// 设备。签名覆盖 MESH2\nPOST\n/api/v1/register\n<deviceId>\n<timestamp>\n<nonce>\n<platform>\n\n。
type registerRequest struct {
	Token     string `json:"token"`
	DeviceID  string `json:"deviceId"`
	Hostname  string `json:"hostname"`
	Pubkey    string `json:"pubkey"`
	Timestamp string `json:"timestamp"`
	Nonce     string `json:"nonce"`
	Signature string `json:"signature"`
	Platform  string `json:"platform"`
}

type registerResponse struct {
	DeviceID   string `json:"deviceId"`
	ConfigTOML string `json:"configToml"`
	ConfigHash string `json:"configHash"`
}

// heartbeatRequest 是心跳请求体：认证字段与业务字段放在同一个 JSON 里，
// 请求头不承担认证。
type heartbeatRequest struct {
	DeviceID   string `json:"deviceId"`
	Timestamp  string `json:"timestamp"`
	Nonce      string `json:"nonce"`
	Signature  string `json:"signature"`
	Platform   string `json:"platform"`
	ConfigHash string `json:"configHash"`
	AppVersion string `json:"appVersion"`
	// CredentialID 是客户端正在用的凭据；不参与签名，也不影响鉴权。
	CredentialID string `json:"credentialId"`
	// AppUpdateKnown 是客户端内存里那份 appUpdate 的 sha256（不是版本号：同一
	// 版本号重新发布时内容会变）。命中时只回精简形状，省掉每轮重复的 notes。
	// 不参与签名，也不影响鉴权：它只决定下发多少内容。
	AppUpdateKnown string `json:"appUpdateKnown"`
}

// deviceAuth verifies a signed request and returns the device. On failure it
// has already written a 401 response and never reveals which check failed.
func (s *Server) deviceAuth(w http.ResponseWriter, r *http.Request, creds heartbeatRequest) (db.Device, bool) {
	deviceID := creds.DeviceID
	timestamp := creds.Timestamp
	nonce := creds.Nonce
	signature := creds.Signature
	if deviceID == "" || timestamp == "" || nonce == "" || signature == "" {
		httpx.WriteError(w, http.StatusUnauthorized, codeUnauthorized)
		return db.Device{}, false
	}
	device, found, err := s.app.DeviceByDeviceID(deviceID)
	if err != nil {
		s.logger.Error("load device", "device_id", deviceID, "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal")
		return db.Device{}, false
	}
	if !found {
		code := codeUnauthorized
		if s.app.Revoked(deviceID) {
			code = codeRevoked
		}
		httpx.WriteError(w, http.StatusUnauthorized, code)
		return db.Device{}, false
	}
	now := s.app.Now()
	signedAt, err := core.CheckTimestamp(timestamp, now)
	if err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, codeUnauthorized)
		return db.Device{}, false
	}
	signing := core.SigningString(r.Method, r.URL.Path, deviceID, timestamp, nonce, creds.Platform, creds.ConfigHash, creds.AppVersion)
	if err := core.VerifySignature(device.Pubkey, signature, []byte(signing)); err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, codeUnauthorized)
		return db.Device{}, false
	}
	if !s.nonces.Claim(nonce, now, signedAt) {
		httpx.WriteError(w, http.StatusUnauthorized, codeUnauthorized)
		return db.Device{}, false
	}
	if err := s.app.Touch(device.PublicID, s.clientIP(r)); err != nil {
		s.logger.Error("touch device", "id", device.PublicID, "error", err)
	}
	return device, true
}

// handleRegister binds a device identity to a device slot. The idle branch is
// not credential free: a slot that is already bound only hands out its
// configuration again to the holder of a still valid registration token,
// because that configuration carries the network secret.
func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	body, err := httpx.ReadBody(w, r)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "bad_request")
		return
	}
	var req registerRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "bad_request")
		return
	}
	req.Token = strings.TrimSpace(req.Token)
	req.DeviceID = strings.TrimSpace(req.DeviceID)
	req.Hostname = strings.TrimSpace(req.Hostname)
	req.Pubkey = strings.TrimSpace(req.Pubkey)
	if req.DeviceID == "" || len(req.DeviceID) > 128 || len(req.Hostname) > 255 {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if err := core.CheckPubkey(req.Pubkey); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_pubkey")
		return
	}
	now := s.app.Now()
	existing, bound, err := s.app.DeviceByDeviceID(req.DeviceID)
	if err != nil {
		s.logger.Error("load device", "device_id", req.DeviceID, "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal")
		return
	}
	if bound {
		proved, byToken := s.registerProofValid(existing, req, now)
		if !proved {
			httpx.WriteError(w, http.StatusUnauthorized, codeUnauthorized)
			return
		}
		if existing.Pubkey != req.Pubkey {
			httpx.WriteError(w, http.StatusConflict, "device_conflict")
			return
		}
		// 令牌是一次性的：这一支靠它证明了归属就当场清掉，否则它在剩余的有效期
		// （≤10 分钟）内还能反复取回配置，而正式节点的配置里有 network_secret。
		if byToken {
			if err := s.app.ConsumeToken(existing.PublicID); err != nil {
				s.logger.Error("consume registration token", "id", existing.PublicID, "error", err)
			}
		}
		if err := s.app.Touch(existing.PublicID, s.clientIP(r)); err != nil {
			s.logger.Error("touch device", "id", existing.PublicID, "error", err)
		}
		httpx.WriteJSON(w, http.StatusOK, registerResponse{
			DeviceID:   existing.DeviceID,
			ConfigTOML: existing.ConfigTOML,
			ConfigHash: existing.ConfigHash,
		})
		return
	}
	if req.Token == "" {
		httpx.WriteError(w, http.StatusUnauthorized, "invalid_token")
		return
	}
	device, err := s.app.BindDevice(req.Token, req.DeviceID, req.Hostname, req.Pubkey, s.clientIP(r))
	switch {
	case errors.Is(err, app.ErrTokenInvalid):
		httpx.WriteError(w, http.StatusUnauthorized, "invalid_token")
		return
	case errors.Is(err, app.ErrNotFound):
		httpx.WriteError(w, http.StatusUnauthorized, "invalid_token")
		return
	case err != nil:
		s.logger.Error("bind device", "device_id", req.DeviceID, "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, registerResponse{
		DeviceID:   device.DeviceID,
		ConfigTOML: device.ConfigTOML,
		ConfigHash: device.ConfigHash,
	})
}

// registerProofValid reports whether the caller proved it owns the bound slot,
// and whether it did so with a registration token. A signature with the bound
// key counts, and so does an unused token; knowing deviceId and pubkey is not
// enough.
//
// 签名这一支失败时继续看令牌，而不是直接拒绝：契约要求「出示下列任一凭据」，
// 而调用方可能同时带着两者（管理端刚补签了令牌，本机又试了一次签名，或这轮的
// 签名用了旧的 deviceId）。令牌成功时调用方要把它消费掉。
func (s *Server) registerProofValid(device db.Device, req registerRequest, now time.Time) (proved, byToken bool) {
	if req.Timestamp != "" && req.Nonce != "" && req.Signature != "" {
		if signedAt, err := core.CheckTimestamp(req.Timestamp, now); err == nil {
			// 注册请求没有配置与版本信息，签名里这两行留空。
			signing := core.SigningString(http.MethodPost, "/api/v1/register", req.DeviceID, req.Timestamp, req.Nonce, req.Platform, "", "")
			if err := core.VerifySignature(device.Pubkey, req.Signature, []byte(signing)); err == nil &&
				s.nonces.Claim(req.Nonce, now, signedAt) {
				return true, false
			}
		}
	}
	return s.slotTokenValid(device, req.Token, now), true
}

// slotTokenValid reports whether token still proves ownership of this slot. A
// registration token is single use per device: once bound it stays valid only
// for the slot it was issued to, until it expires.
func (s *Server) slotTokenValid(device db.Device, token string, now time.Time) bool {
	if token == "" {
		return false
	}
	pending, found, err := s.app.DeviceByToken(token)
	if err != nil {
		s.logger.Error("load registration token owner", "error", err)
		return false
	}
	return found && pending.PublicID == device.PublicID && pending.TokenExpiresAt > now.Unix()
}

// updateInfo points a client at the published build. Version is only sent to a
// device reporting an older one (app.NeedsUpdate); sha256 lets the client verify
// the download after the fact. AutoUpdate 是这个包自己带的选择——发布方决定它
// 能不能自己装，设备是不是无人值守不参与判断，所以它跟着 appUpdate 走，永远有值。
//
// 两种形状：设备报的 appUpdateKnown 与发布摘要一致时只回 version/autoUpdate/
// unchanged，内容字段整个不出现（发布要求 appUrl 非空，完整形状里 url 一定有
// 值）；否则回完整形状，且不带 unchanged。
type updateInfo struct {
	Version string `json:"version"`
	URL     string `json:"url,omitempty"`
	Notes   string `json:"notes,omitempty"`
	SHA256  string `json:"sha256,omitempty"`
	// Signature 覆盖 {client, platform, version, sha256}（MESH3 行串）。客户端
	// 验不过就拒装——摘要来自控制面、内容来自任意地址，签名把这一对绑在一起。
	Signature string `json:"signature,omitempty"`
	// AutoUpdate 在两种形状里都有：管理员可能刚改过设备级覆盖。
	AutoUpdate bool `json:"autoUpdate"`
	// Unchanged 表示设备手里已经是这一份：跳过下载与提示，只认 autoUpdate。
	Unchanged bool `json:"unchanged,omitempty"`
}

// launcherInfo points a client at the launcher published for its platform. It
// rides along with every heartbeat that has a body, not only when it changed:
// the client hashes the launcher it already has itself, so the server has
// nothing to compare and cannot tell whether the local copy is current.
type launcherInfo struct {
	Version   string `json:"version"`
	URL       string `json:"url"`
	SHA256    string `json:"sha256"`
	Signature string `json:"signature"`
}

// launcherOf is the launcher slot of release as a heartbeat field; nil when the
// platform has no launcher published. The URL is stored absolute (publishing
// requires one), so it travels to the client unchanged.
func launcherOf(release db.Release) *launcherInfo {
	// 有摘要没签名 = 这一行是加签名之前发布的。客户端会验签并拒装，那就等于
	// 每 30 秒给全网发一个必然失败的槽位；按「没发布」处理，等重新发布。
	if release.LauncherSHA256 == "" || release.LauncherSignature == "" {
		return nil
	}
	return &launcherInfo{
		Version:   release.LauncherVersion,
		URL:       release.LauncherURL,
		SHA256:    release.LauncherSHA256,
		Signature: release.LauncherSignature,
	}
}

// heartbeatResponse carries only what changed; 204 means nothing did.
// ConfigB64 是整份 config.toml 的 base64：配置只有几 KB，随心跳一起回去
// 就不必让客户端再发一次请求。
type heartbeatResponse struct {
	ConfigHash string        `json:"configHash,omitempty"`
	ConfigB64  string        `json:"configB64,omitempty"`
	AppUpdate  *updateInfo   `json:"appUpdate,omitempty"`
	Launcher   *launcherInfo `json:"launcher,omitempty"`
	// 凭据私钥只在签发的那一次响应里出现，之后服务端只剩 id。
	Credential       string `json:"credential,omitempty"`
	CredentialID     string `json:"credentialId,omitempty"`
	CredentialExpiry int64  `json:"credentialExpiry,omitempty"`
}

func (s *Server) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	body, err := httpx.ReadBody(w, r)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "bad_request")
		return
	}
	var req heartbeatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "bad_request")
		return
	}
	device, ok := s.deviceAuth(w, r, req)
	if !ok {
		return
	}
	if err := s.app.RecordHeartbeat(device.PublicID, req.ConfigHash, req.AppVersion, req.Platform); err != nil {
		s.logger.Error("record heartbeat", "id", device.PublicID, "error", err)
	}
	// 更新包按平台发布：Linux 与 Windows 不会互相覆盖。
	release, _, err := s.app.Release(req.Platform)
	if err != nil {
		s.logger.Error("read release", "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal")
		return
	}
	configChanged := req.ConfigHash != device.ConfigHash
	// 只有设备上报的版本低于发布版本才算需要更新：设备版本更高（回滚过发布、
	// 手工装过新包）时按 current 处理，不能让它每 30 秒被要求装同一个旧包。
	//
	// 签名为空的行同样不下发：那是加签名之前发布的记录，客户端一律拒装。按
	// 未发布处理，管理端重新发布一次就恢复了。
	appChanged := release.Signature != "" && app.NeedsUpdate(req.AppVersion, release.Version)
	// 启动器不参与签名原文，服务端也就无从比较客户端手里那份，所以只要该平台
	// 配了启动器就每次都带上；客户端自己算本地摘要去比。代价是这种情况没有
	// 204，三十秒多几十字节。
	launcher := launcherOf(release)
	// 设备上报的 id 与最新签发的一致，说明它已经换上新凭据并重启过 core，
	// 这时才吊销其余的。早一步吊销会让还没拿到新凭据的设备失联。
	// "none" 表示它手里没有凭据（那张新签的还没被用上），空串是旧客户端或
	// 正式节点，这两种都没有可确认的切换，不调 ConfirmCredential。
	if req.CredentialID != "" && req.CredentialID != "none" {
		if err := s.app.ConfirmCredential(r.Context(), device.PublicID, req.CredentialID); err != nil {
			s.logger.Error("confirm credential", "id", device.PublicID, "error", err)
		}
	}
	issued := s.issueCredentialIfNeeded(r, device.PublicID, req.CredentialID)
	if !configChanged && !appChanged && launcher == nil && issued == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	resp := heartbeatResponse{Launcher: launcher}
	if issued != nil {
		resp.Credential = issued.Secret
		resp.CredentialID = issued.ID
		resp.CredentialExpiry = issued.Expiry
	}
	if configChanged {
		resp.ConfigHash = device.ConfigHash
		resp.ConfigB64 = base64.StdEncoding.EncodeToString([]byte(device.ConfigTOML))
	}
	if appChanged {
		appUpdate := &updateInfo{
			Version:    release.Version,
			AutoUpdate: app.EffectiveAutoUpdate(device.AutoUpdate, release.AutoUpdate),
		}
		// 摘要一致才算同一份：同一版本号重新发布时内容会变，只有 sha256 能表达
		// "我手上是哪一份"。已知就只带版本与开关，notes 不必每 30 秒重复一次。
		if req.AppUpdateKnown != "" && req.AppUpdateKnown == release.SHA256 {
			appUpdate.Unchanged = true
		} else {
			appUpdate.URL = release.URL
			appUpdate.Notes = release.Notes
			appUpdate.SHA256 = release.SHA256
			appUpdate.Signature = release.Signature
		}
		resp.AppUpdate = appUpdate
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

// issueCredentialIfNeeded 判断这台设备要不要一张新凭据，要就当场签发，并用
// 这次签发自己的 id 与到期时间回答（不能回头再查一次库：并发心跳时那会把
// 一张凭据的私钥配到另一张的 id 上）。
//
// 上报的 credentialId 是设备 core 里正在用的那张，签发条件按它分档：
//
//   - 与最新签发的一致：切换已经完成，只有进入续期窗口才续签。
//   - 报着旧 id：core 还没重启完，或者管理端刚签发了一张而设备还不知道。
//     冷却外补一张（管理端那条通道靠这一步落到设备上），冷却内不签。
//   - none：客户端声明本地没有凭据，冷却外签一张。
//
// 冷却复用 CredentialNoneCooldown：报 none 与报旧 id 都可能每轮心跳都命中，
// 没有它就会一直签。
func (s *Server) issueCredentialIfNeeded(r *http.Request, publicID, reportedID string) *app.IssuedCredential {
	if !s.app.CredentialConfigured() {
		return nil
	}
	// 空串是正式节点（contract/device-api.md）或还不认识这个字段的旧客户端：既不
	// 确认也不签发，连凭据表都不必读。
	if reportedID == "" {
		return nil
	}
	now := s.app.Now().Unix()
	cooldown := int64(app.CredentialNoneCooldown.Seconds())
	latest, err := s.app.LatestCredential(publicID)
	switch {
	case errors.Is(err, db.ErrCredentialNotFound):
		// 这台设备从来就没有过凭据：签一张。
	case err != nil:
		s.logger.Error("load credential", "id", publicID, "error", err)
		return nil
	case reportedID == "none":
		// 客户端明确声明本地没有凭据：冷却外签一张。
		if now-latest.CreatedAt < cooldown {
			return nil
		}
	case reportedID == latest.CredentialID:
		// 设备已经用上最新那张：只在续期窗口内才签下一张。
		if latest.ExpiresAt-now >= int64(app.CredentialRenewBefore.Seconds()) {
			return nil
		}
	case now-latest.CreatedAt < cooldown:
		// 设备仍在用旧的那张，而且刚签过：它多半正在重启 core，等它报上
		// 最新的那张再收敛。
		return nil
	}
	// 走到这里还有一条：报着旧 id 且冷却已过——管理端签发的那张要下发，
	// 或者客户端卡在旧凭据上。补一张，下一次签发至少要再过一个冷却窗口。
	issued, err := s.app.IssueCredential(r.Context(), publicID)
	if err != nil {
		s.logger.Error("issue credential", "id", publicID, "error", err)
		return nil
	}
	return &issued
}
