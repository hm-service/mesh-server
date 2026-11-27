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
}

// deviceAuth verifies a signed request and returns the identity it proved. On
// failure it has already written a 401 response and never reveals which check
// failed. It reads the device, never writes it: the observation belongs to the
// business entry, which records it once under the device lock.
func (s *Server) deviceAuth(w http.ResponseWriter, r *http.Request, creds heartbeatRequest) (app.DeviceAuth, bool) {
	// 值域先于一切：签名原文按 \n 拼接，一个带换行的字段就能把同一段文本拆成
	// 另一组字段。形状不对与签名错误走同一条 401，响应不说明是哪一步失败的。
	fields := core.DeviceRequestFields{
		DeviceID:     creds.DeviceID,
		Timestamp:    creds.Timestamp,
		Nonce:        creds.Nonce,
		Platform:     creds.Platform,
		ConfigHash:   creds.ConfigHash,
		AppVersion:   creds.AppVersion,
		Signature:    creds.Signature,
		CredentialID: creds.CredentialID,
	}
	if err := fields.Validate(); err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, codeUnauthorized)
		return app.DeviceAuth{}, false
	}
	deviceID := creds.DeviceID
	timestamp := creds.Timestamp
	nonce := creds.Nonce
	signature := creds.Signature
	device, found, err := s.app.DeviceByDeviceID(deviceID)
	if err != nil {
		s.logger.Error("load device", "device_id", deviceID, "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal")
		return app.DeviceAuth{}, false
	}
	if !found {
		s.writeUnauthorizedDevice(w, deviceID)
		return app.DeviceAuth{}, false
	}
	now := s.app.Now()
	signedAt, err := core.CheckTimestamp(timestamp, now)
	if err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, codeUnauthorized)
		return app.DeviceAuth{}, false
	}
	signing := core.SigningString(r.Method, r.URL.Path, deviceID, timestamp, nonce, creds.Platform, creds.ConfigHash, creds.AppVersion)
	if err := core.VerifySignature(device.Pubkey, signature, []byte(signing)); err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, codeUnauthorized)
		return app.DeviceAuth{}, false
	}
	if !s.nonces.Claim(nonce, now, signedAt) {
		httpx.WriteError(w, http.StatusUnauthorized, codeUnauthorized)
		return app.DeviceAuth{}, false
	}
	return app.DeviceAuth{PublicID: device.PublicID, DeviceID: device.DeviceID}, true
}

// writeUnauthorizedDevice answers a request whose device_id is unknown: a
// tombstone makes it "revoked", everything else stays "unauthorized".
func (s *Server) writeUnauthorizedDevice(w http.ResponseWriter, deviceID string) {
	code := codeUnauthorized
	if s.app.Revoked(deviceID) {
		code = codeRevoked
	}
	httpx.WriteError(w, http.StatusUnauthorized, code)
}

// handleRegister binds a device identity to a device slot. The idle branch is
// not credential free: a slot that is already bound only hands out its
// configuration again to the holder of a still valid registration token or of
// the slot's own key, because that configuration carries the network secret.
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
		// 签名这一支先验：验不过才看令牌，契约要求「出示下列任一凭据」，而调用
		// 方可能同时带着两者（管理端刚补签了令牌，本机又试了一次签名）。
		signed := s.registerSignatureValid(existing.Pubkey, req, now)
		if !signed && req.Token == "" {
			httpx.WriteError(w, http.StatusUnauthorized, codeUnauthorized)
			return
		}
		device, err := s.app.AcceptRegistration(existing.PublicID, app.BoundRegistration{
			DeviceID: req.DeviceID,
			Pubkey:   req.Pubkey,
			Token:    req.Token,
			Signed:   signed,
			IP:       s.clientIP(r),
		})
		switch {
		case errors.Is(err, app.ErrPubkeyMismatch):
			httpx.WriteError(w, http.StatusConflict, "device_conflict")
			return
		case errors.Is(err, app.ErrIdentityChanged), errors.Is(err, app.ErrTokenInvalid), errors.Is(err, app.ErrNotFound):
			// 与认证失败同一条 401：注册这条路径不区分「槽位被换绑」「令牌不对」
			// 「记录被删」。墓碑只给签名请求回 revoked，注册仍按未授权处理。
			httpx.WriteError(w, http.StatusUnauthorized, codeUnauthorized)
			return
		case err != nil:
			s.logger.Error("accept registration", "device_id", req.DeviceID, "error", err)
			httpx.WriteError(w, http.StatusInternalServerError, "internal")
			return
		}
		httpx.WriteJSON(w, http.StatusOK, registerResponse{
			DeviceID:   device.DeviceID,
			ConfigTOML: device.ConfigTOML,
			ConfigHash: device.ConfigHash,
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

// registerSignatureValid reports whether the request carries a valid signature
// of the bound slot's key; a valid signature also claims its nonce. 签名这一支
// 失败时由调用方退回令牌，而不是直接拒绝：契约要求「出示下列任一凭据」。
func (s *Server) registerSignatureValid(pubkey string, req registerRequest, now time.Time) bool {
	if req.Timestamp == "" || req.Nonce == "" || req.Signature == "" {
		return false
	}
	signedAt, err := core.CheckTimestamp(req.Timestamp, now)
	if err != nil {
		return false
	}
	// 注册请求没有配置与版本信息，签名里这两行留空。
	signing := core.SigningString(http.MethodPost, "/api/v1/register", req.DeviceID, req.Timestamp, req.Nonce, req.Platform, "", "")
	if err := core.VerifySignature(pubkey, req.Signature, []byte(signing)); err != nil {
		return false
	}
	return s.nonces.Claim(req.Nonce, now, signedAt)
}

// updateInfo points a client at the published build. Version is only sent to a
// device reporting an older one (app.NeedsUpdate); sha256 lets the client verify
// the download after the fact. AutoUpdate 是这个包自己带的选择——发布方决定它
// 能不能自己装，设备是不是无人值守不参与判断，所以它跟着 appUpdate 走，永远有值。
//
// 字段集合固定：只要这个 offer 出现，url、sha256、signature 就一定在里面（发布
// 要求 appUrl 非空，摘要与签名在发布时算出）。只有 notes 随发布记录为空而不出现。
// 「这份我手上已经有」由客户端按版本与摘要自己判，服务端不回第二种形状。
type updateInfo struct {
	Version string `json:"version"`
	URL     string `json:"url"`
	Notes   string `json:"notes,omitempty"`
	SHA256  string `json:"sha256"`
	// Signature 覆盖 {client, platform, version, sha256}（MESH3 行串）。客户端
	// 验不过就拒装——摘要来自控制面、内容来自任意地址，签名把这一对绑在一起。
	Signature string `json:"signature"`
	// AutoUpdate 是设备级覆盖后的结果，每轮心跳取当前值：管理员可能刚改过。
	AutoUpdate bool `json:"autoUpdate"`
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
	auth, ok := s.deviceAuth(w, r, req)
	if !ok {
		return
	}
	// 整个业务动作在 app 里一次完成：锁内复核绑定、一次条件写入、再按写出来的
	// 那一行规划响应与凭据步骤。api 不写设备，也不切换快照。
	result, err := s.app.Heartbeat(r.Context(), auth, app.HeartbeatObservation{
		ConfigHash:   req.ConfigHash,
		AppVersion:   req.AppVersion,
		Platform:     req.Platform,
		CredentialID: req.CredentialID,
		IP:           s.clientIP(r),
	})
	if err != nil {
		// 认证之后换绑或删除插进来：这次请求的身份已经不持有槽位，什么都不回。
		if errors.Is(err, app.ErrIdentityChanged) || errors.Is(err, app.ErrNotFound) {
			s.writeUnauthorizedDevice(w, auth.DeviceID)
			return
		}
		s.logger.Error("heartbeat", "id", auth.PublicID, "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal")
		return
	}
	if result.Config == nil && result.Update == nil && result.Launcher == nil && result.Credential == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	resp := heartbeatResponse{}
	if result.Config != nil {
		resp.ConfigHash = result.Config.Hash
		resp.ConfigB64 = base64.StdEncoding.EncodeToString([]byte(result.Config.TOML))
	}
	if result.Update != nil {
		resp.AppUpdate = &updateInfo{
			Version:    result.Update.Version,
			URL:        result.Update.URL,
			Notes:      result.Update.Notes,
			SHA256:     result.Update.SHA256,
			Signature:  result.Update.Signature,
			AutoUpdate: result.Update.AutoUpdate,
		}
	}
	if result.Launcher != nil {
		resp.Launcher = &launcherInfo{
			Version:   result.Launcher.Version,
			URL:       result.Launcher.URL,
			SHA256:    result.Launcher.SHA256,
			Signature: result.Launcher.Signature,
		}
	}
	if result.Credential != nil {
		resp.Credential = result.Credential.Secret
		resp.CredentialID = result.Credential.ID
		resp.CredentialExpiry = result.Credential.Expiry
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}
