package api

import (
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

type registerRequest struct {
	Token    string `json:"token"`
	DeviceID string `json:"device_id"`
	Hostname string `json:"hostname"`
	Pubkey   string `json:"pubkey"`
}

type registerResponse struct {
	DeviceID   string `json:"device_id"`
	ConfigTOML string `json:"config_toml"`
	ConfigHash string `json:"config_hash"`
}

type configResponse struct {
	ConfigTOML string `json:"config_toml"`
	ConfigHash string `json:"config_hash"`
}

// deviceCredentials 是一次设备请求携带的身份凭据。GET 类请求放在请求头里，
// 心跳放在请求体里——那里还有业务字段，放一起比再塞四个 X- 头干净。
type deviceCredentials struct {
	DeviceID   string `json:"deviceId"`
	Timestamp  string `json:"timestamp"`
	Nonce      string `json:"nonce"`
	Signature  string `json:"signature"`
	Platform   string `json:"platform"`
	ConfigHash string `json:"configHash"`
	AppHash    string `json:"appHash"`
}

// credentialsFromHeader reads the header form used by body-less requests.
func credentialsFromHeader(r *http.Request) deviceCredentials {
	return deviceCredentials{
		DeviceID:  strings.TrimSpace(r.Header.Get("X-Device-Id")),
		Timestamp: strings.TrimSpace(r.Header.Get("X-Timestamp")),
		Nonce:     strings.TrimSpace(r.Header.Get("X-Nonce")),
		Signature: strings.TrimSpace(r.Header.Get("X-Signature")),
		Platform:  strings.TrimSpace(r.Header.Get("X-Platform")),
	}
}

// deviceAuth verifies a signed request and returns the device. On failure it
// has already written a 401 response and never reveals which check failed.
func (s *Server) deviceAuth(w http.ResponseWriter, r *http.Request, creds deviceCredentials) (db.Device, bool) {
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
// configuration again to the holder of the bound key or of a fresh registration
// token, because that configuration carries the network secret.
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
		if !s.registerProofValid(r, existing, req.Token, now) {
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
// A request signature with the bound key counts, and so does a valid
// registration token for the same slot. Knowing device_id and pubkey does not.
func (s *Server) registerProofValid(r *http.Request, device db.Device, token string, now time.Time) bool {
	creds := credentialsFromHeader(r)
	if creds.DeviceID != "" && creds.DeviceID == device.DeviceID && creds.Timestamp != "" && creds.Nonce != "" && creds.Signature != "" {
		if err := core.CheckTimestamp(creds.Timestamp, now); err != nil {
			return false
		}
		signing := core.SigningString(r.Method, r.URL.Path, creds.DeviceID, creds.Timestamp, creds.Nonce, creds.Platform, "", "")
		if err := core.VerifySignature(device.Pubkey, creds.Signature, []byte(signing)); err != nil {
			return false
		}
		return s.nonces.Claim(creds.Nonce, now)
	}
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

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	device, ok := s.deviceAuth(w, r, credentialsFromHeader(r))
	if !ok {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, configResponse{
		ConfigTOML: device.ConfigTOML,
		ConfigHash: device.ConfigHash,
	})
}

// heartbeatRequest 与 deviceCredentials 同构：心跳把认证字段和业务字段放在
// 同一个请求体里。
type heartbeatRequest = deviceCredentials

// updateInfo points a client at the published build. Version only labels the
// update in the client UI; app_hash decides that one exists, and sha256 lets the
// client verify the download after the fact.
type updateInfo struct {
	Version string `json:"version"`
	URL     string `json:"url"`
	Notes   string `json:"notes,omitempty"`
	SHA256  string `json:"sha256,omitempty"`
}

// heartbeatResponse carries only what changed; 204 means nothing did.
type heartbeatResponse struct {
	ConfigHash string      `json:"config_hash,omitempty"`
	AppUpdate  *updateInfo `json:"app_update,omitempty"`
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
	if !configChanged && !appChanged {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	resp := heartbeatResponse{}
	if configChanged {
		resp.ConfigHash = device.ConfigHash
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
