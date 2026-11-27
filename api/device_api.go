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
	// CredentialID 是客户端正在用的凭据：不参与签名，只决定签发与确认的时机；
	// 它与其余字段一样受合法域约束（core.DeviceRequestFields）。
	CredentialID string `json:"credentialId"`
	// AppUpdateKnown 是客户端内存里那份 appUpdate 的 sha256（不是版本号：同一
	// 版本号重新发布时内容会变）。命中时只回精简形状，省掉每轮重复的 notes。
	// 不参与签名，也不影响鉴权：它只决定下发多少内容。
	AppUpdateKnown string `json:"appUpdateKnown"`
}

// deviceAuth verifies a signed request and returns the device. On failure it
// has already written a 401 response and never reveals which check failed.
func (s *Server) deviceAuth(w http.ResponseWriter, r *http.Request, creds heartbeatRequest) (db.Device, bool) {
	// 值域先于一切：签名原文按 \n 拼接，一个带换行的字段就能把同一段文本拆成
	// 另一组字段。形状不对与签名错误走同一条 401，响应不说明是哪一步失败的。
	fields := core.DeviceRequestFields{
		DeviceID:       creds.DeviceID,
		Timestamp:      creds.Timestamp,
		Nonce:          creds.Nonce,
		Platform:       creds.Platform,
		ConfigHash:     creds.ConfigHash,
		AppVersion:     creds.AppVersion,
		Signature:      creds.Signature,
		CredentialID:   creds.CredentialID,
		AppUpdateKnown: creds.AppUpdateKnown,
	}
	if err := fields.Validate(); err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, codeUnauthorized)
		return db.Device{}, false
	}
	deviceID := creds.DeviceID
	timestamp := creds.Timestamp
	nonce := creds.Nonce
	signature := creds.Signature
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
	// 与心跳同一份值域：注册请求里的签名三件套要么全空（只出示令牌），要么
	// 全部在合法域内。hostname 不参与签名，只受长度限制。
	fields := core.DeviceRequestFields{
		DeviceID:  req.DeviceID,
		Timestamp: req.Timestamp,
		Nonce:     req.Nonce,
		Platform:  req.Platform,
		Signature: req.Signature,
	}
	if err := fields.Validate(); err != nil || len(req.Hostname) > 255 {
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
		// 核对与清空在同一次事务里完成，令牌在这期间被轮换或消费时不再放行配置。
		if byToken {
			consumed, err := s.app.ConsumeTokenIfCurrent(existing.PublicID, req.Token)
			if err != nil {
				s.logger.Error("consume registration token", "id", existing.PublicID, "error", err)
				httpx.WriteError(w, http.StatusInternalServerError, "internal")
				return
			}
			if !consumed {
				httpx.WriteError(w, http.StatusUnauthorized, codeUnauthorized)
				return
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
	device, err := s.app.BindDevice(r.Context(), req.Token, req.DeviceID, req.Hostname, req.Pubkey, s.clientIP(r))
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
	// 凭据的确认、收敛与签发在 app 里一次做完：整段在设备锁内，删除、换绑和
	// 另一次心跳都排在它后面，看不到「读快照 → 决定 → 落地」的中间态。
	issued, err := s.app.CredentialStep(r.Context(), device, req.CredentialID)
	if err != nil {
		// 失败不上报给设备：心跳照常回 204，下一轮再试。ErrIdentityChanged 是
		// 换绑插进来了，这种请求本来就不该再拿到凭据。
		s.logger.Error("credential step", "id", device.PublicID, "error", err)
	}
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
