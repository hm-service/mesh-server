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

// deviceAuth verifies the CONTRACT.md 1.3 headers for body and returns the
// device. On failure it has already written a 401 response and never reveals
// which check failed.
func (s *Server) deviceAuth(w http.ResponseWriter, r *http.Request, body []byte) (db.Device, bool) {
	deviceID := strings.TrimSpace(r.Header.Get("X-Device-Id"))
	timestamp := strings.TrimSpace(r.Header.Get("X-Timestamp"))
	nonce := strings.TrimSpace(r.Header.Get("X-Nonce"))
	signature := strings.TrimSpace(r.Header.Get("X-Signature"))
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
	signing := core.SigningString(r.Method, r.URL.Path, core.BodyHash(body), timestamp, nonce)
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
		if !s.registerProofValid(r, body, existing, req.Token, now) {
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
func (s *Server) registerProofValid(r *http.Request, body []byte, device db.Device, token string, now time.Time) bool {
	deviceID := strings.TrimSpace(r.Header.Get("X-Device-Id"))
	timestamp := strings.TrimSpace(r.Header.Get("X-Timestamp"))
	nonce := strings.TrimSpace(r.Header.Get("X-Nonce"))
	signature := strings.TrimSpace(r.Header.Get("X-Signature"))
	if deviceID != "" && deviceID == device.DeviceID && timestamp != "" && nonce != "" && signature != "" {
		if err := core.CheckTimestamp(timestamp, now); err != nil {
			return false
		}
		signing := core.SigningString(r.Method, r.URL.Path, core.BodyHash(body), timestamp, nonce)
		if err := core.VerifySignature(device.Pubkey, signature, []byte(signing)); err != nil {
			return false
		}
		return s.nonces.Claim(nonce, now)
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
	body, err := httpx.ReadBody(w, r)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "bad_request")
		return
	}
	device, ok := s.deviceAuth(w, r, body)
	if !ok {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, configResponse{
		ConfigTOML: device.ConfigTOML,
		ConfigHash: device.ConfigHash,
	})
}

type heartbeatRequest struct {
	ConfigHash string `json:"config_hash"`
	AppHash    string `json:"app_hash"`
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
	device, ok := s.deviceAuth(w, r, body)
	if !ok {
		return
	}
	var req heartbeatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "bad_request")
		return
	}
	if err := s.app.RecordHeartbeat(device.PublicID, req.ConfigHash, req.AppHash); err != nil {
		s.logger.Error("record heartbeat", "id", device.PublicID, "error", err)
	}
	settings, err := s.app.Settings()
	if err != nil {
		s.logger.Error("read settings", "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal")
		return
	}
	configChanged := req.ConfigHash != device.ConfigHash
	appChanged := settings.AppHash != "" && req.AppHash != settings.AppHash
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
			Version: settings.AppVersion,
			URL:     settings.AppURL,
			Notes:   settings.AppNotes,
			SHA256:  settings.AppSHA256,
		}
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}
