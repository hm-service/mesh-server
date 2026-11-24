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

// registerRequest is POST /api/register. timestamp、nonce、signature、platform
// 只有"槽位已经绑定"时才用得上：它们带着本机私钥的签名，证明调用方就是那个
// 设备。签名覆盖 MESH2\nPOST\n/api/register\n<device_id>\n<timestamp>\n<nonce>\n<platform>\n\n。
type registerRequest struct {
	Token     string `json:"token"`
	DeviceID  string `json:"device_id"`
	Hostname  string `json:"hostname"`
	Pubkey    string `json:"pubkey"`
	Timestamp string `json:"timestamp"`
	Nonce     string `json:"nonce"`
	Signature string `json:"signature"`
	Platform  string `json:"platform"`
}

type registerResponse struct {
	DeviceID   string `json:"device_id"`
	ConfigTOML string `json:"config_toml"`
	ConfigHash string `json:"config_hash"`
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
	AppHash    string `json:"appHash"`
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
	if err := core.CheckTimestamp(timestamp, now); err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, codeUnauthorized)
		return db.Device{}, false
	}
	signing := core.SigningString(r.Method, r.URL.Path, deviceID, timestamp, nonce, creds.Platform, creds.ConfigHash, creds.AppHash)
	if err := core.VerifySignature(device.Pubkey, signature, []byte(signing)); err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, codeUnauthorized)
		return db.Device{}, false
	}
	if !s.nonces.Claim(nonce, now) {
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
		if !s.registerProofValid(existing, req, now) {
			httpx.WriteError(w, http.StatusUnauthorized, codeUnauthorized)
			return
		}
		if existing.Pubkey != req.Pubkey {
			httpx.WriteError(w, http.StatusConflict, "device_conflict")
			return
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

// registerProofValid reports whether the caller proved it owns the bound slot.
// A signature with the bound key counts, and so does a registration token that
// was never used up. Knowing device_id and pubkey is not enough.
func (s *Server) registerProofValid(device db.Device, req registerRequest, now time.Time) bool {
	if req.Timestamp != "" && req.Nonce != "" && req.Signature != "" {
		if err := core.CheckTimestamp(req.Timestamp, now); err != nil {
			return false
		}
		// 注册请求没有配置与版本信息，签名里这两行留空。
		signing := core.SigningString(http.MethodPost, "/api/register", req.DeviceID, req.Timestamp, req.Nonce, req.Platform, "", "")
		if err := core.VerifySignature(device.Pubkey, req.Signature, []byte(signing)); err != nil {
			return false
		}
		return s.nonces.Claim(req.Nonce, now)
	}
	return s.slotTokenValid(device, req.Token, now)
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

// updateInfo points a client at the published build. Version only labels the
// update in the client UI; app_hash decides that one exists, and sha256 lets the
// client verify the download after the fact.
type updateInfo struct {
	Version string `json:"version"`
	URL     string `json:"url"`
	Notes   string `json:"notes,omitempty"`
	SHA256  string `json:"sha256,omitempty"`
}

// launcherInfo points a client at the launcher published for its platform. It
// rides along with every heartbeat that has a body, not only when it changed:
// the client hashes the launcher it already has itself, so the server has
// nothing to compare and cannot tell whether the local copy is current.
type launcherInfo struct {
	Version string `json:"version"`
	URL     string `json:"url"`
	SHA256  string `json:"sha256"`
}

// launcherOf is the launcher slot of release as a heartbeat field; nil when the
// platform has no launcher published.
func launcherOf(r *http.Request, release db.Release) *launcherInfo {
	if release.LauncherSHA256 == "" {
		return nil
	}
	return &launcherInfo{
		Version: release.LauncherVersion,
		URL:     absoluteURL(r, release.LauncherURL),
		SHA256:  release.LauncherSHA256,
	}
}

// heartbeatResponse carries only what changed; 204 means nothing did.
// ConfigB64 是整份 config.toml 的 base64：配置只有几 KB，随心跳一起回去
// 就不必让客户端再发一次请求。
type heartbeatResponse struct {
	ConfigHash string        `json:"config_hash,omitempty"`
	ConfigB64  string        `json:"config_b64,omitempty"`
	AppUpdate  *updateInfo   `json:"app_update,omitempty"`
	Launcher   *launcherInfo `json:"launcher,omitempty"`
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
	if err := s.app.RecordHeartbeat(device.PublicID, req.ConfigHash, req.AppHash, req.Platform); err != nil {
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
	appChanged := release.Hash != "" && req.AppHash != release.Hash
	// 启动器不参与签名原文，服务端也就无从比较客户端手里那份，所以只要该平台
	// 配了启动器就每次都带上；客户端自己算本地摘要去比。代价是这种情况没有
	// 204，三十秒多几十字节。
	launcher := launcherOf(r, release)
	if !configChanged && !appChanged && launcher == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	resp := heartbeatResponse{Launcher: launcher}
	if configChanged {
		resp.ConfigHash = device.ConfigHash
		resp.ConfigB64 = base64.StdEncoding.EncodeToString([]byte(device.ConfigTOML))
	}
	if appChanged {
		resp.AppUpdate = &updateInfo{
			Version: release.Version,
			URL:     absoluteURL(r, release.URL),
			Notes:   release.Notes,
			SHA256:  release.SHA256,
		}
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}