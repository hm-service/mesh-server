package api

import (
	"errors"
	"net/http"
	"time"

	"mesh-server/app"
	"mesh-server/db"
	"mesh-server/httpx"
)

// deviceSummary is one row of GET /api/devices (CONTRACT.md 1.4).
type deviceSummary struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Hostname       string `json:"hostname"`
	DeviceID       string `json:"device_id"`
	Status         string `json:"status"`
	ConfigHash     string `json:"config_hash"`
	LastConfigHash string `json:"last_config_hash"`
	LastAppHash    string `json:"last_app_hash"`
	ConfigState    string `json:"config_state"`
	AppState       string `json:"app_state"`
	AppVersion     string `json:"app_version"`
	Platform       string `json:"last_platform"`
	GroupID        int64  `json:"group_id"`
	Position       int64  `json:"position"`
	CreatedAt      int64  `json:"created_at"`
	LastSeenAt     int64  `json:"last_seen_at"`
}

// releaseFor returns the build published for the device's platform; an empty
// release means that platform has nothing published.
func releaseFor(releases []db.Release, platform string) db.Release {
	for _, r := range releases {
		if r.Platform == platform {
			return r
		}
	}
	return db.Release{}
}

func summarize(d db.Device, release db.Release, now time.Time) deviceSummary {
	return deviceSummary{
		ID:             d.PublicID,
		Name:           d.Name,
		Hostname:       d.Hostname,
		DeviceID:       d.DeviceID,
		Status:         app.DeviceStatus(d, now),
		ConfigHash:     d.ConfigHash,
		LastConfigHash: d.LastConfigHash,
		LastAppHash:    d.LastAppHash,
		ConfigState:    app.ConfigState(d),
		AppState:       app.AppState(d, release),
		AppVersion:     app.ReleaseVersion(release),
		Platform:       d.LastPlatform,
		GroupID:        d.GroupID,
		Position:       d.Position,
		CreatedAt:      d.CreatedAt,
		LastSeenAt:     d.LastSeenAt,
	}
}

// deviceDetail is the full device record GET /api/devices/{id} returns, plus
// the derived status fields the console shows.
type deviceDetail struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	DeviceID       string `json:"device_id"`
	Hostname       string `json:"hostname"`
	Pubkey         string `json:"pubkey"`
	ConfigTOML     string `json:"config_toml"`
	ConfigHash     string `json:"config_hash"`
	Token          string `json:"token"`
	TokenExpiresAt int64  `json:"token_expires_at"`
	CreatedAt      int64  `json:"created_at"`
	LastSeenAt     int64  `json:"last_seen_at"`
	LastIP         string `json:"last_ip"`
	LastConfigHash string `json:"last_config_hash"`
	LastAppHash    string `json:"last_app_hash"`
	Status         string `json:"status"`
	ConfigState    string `json:"config_state"`
	AppState       string `json:"app_state"`
	AppVersion     string `json:"app_version"`
	Platform       string `json:"last_platform"`
	GroupID        int64  `json:"group_id"`
	Position       int64  `json:"position"`
}

func detailOf(d db.Device, release db.Release, now time.Time) deviceDetail {
	return deviceDetail{
		ID:             d.PublicID,
		Name:           d.Name,
		DeviceID:       d.DeviceID,
		Hostname:       d.Hostname,
		Pubkey:         d.Pubkey,
		ConfigTOML:     d.ConfigTOML,
		ConfigHash:     d.ConfigHash,
		Token:          d.Token,
		TokenExpiresAt: d.TokenExpiresAt,
		CreatedAt:      d.CreatedAt,
		LastSeenAt:     d.LastSeenAt,
		LastIP:         d.LastIP,
		LastConfigHash: d.LastConfigHash,
		LastAppHash:    d.LastAppHash,
		Status:         app.DeviceStatus(d, now),
		ConfigState:    app.ConfigState(d),
		AppState:       app.AppState(d, release),
		AppVersion:     app.ReleaseVersion(release),
		Platform:       d.LastPlatform,
		GroupID:        d.GroupID,
		Position:       d.Position,
	}
}

func (s *Server) handleListDevices(w http.ResponseWriter, r *http.Request) {
	devices, err := s.app.ListDevices()
	if err != nil {
		s.logger.Error("list devices", "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal")
		return
	}
	releases, err := s.app.Releases()
	if err != nil {
		s.logger.Error("read releases", "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal")
		return
	}
	now := s.app.Now()
	out := make([]deviceSummary, 0, len(devices))
	for _, d := range devices {
		out = append(out, summarize(d, releaseFor(releases, d.LastPlatform), now))
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// handleTemplate returns the prefill of the new-device dialog.
func (s *Server) handleTemplate(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"toml": s.app.TemplateTOML()})
}

type deviceInput struct {
	Name       string `json:"name"`
	ConfigTOML string `json:"config_toml"`
}

type createDeviceResponse struct {
	ID    string `json:"id"`
	Token string `json:"token"`
}

func (s *Server) handleCreateDevice(w http.ResponseWriter, r *http.Request) {
	var req deviceInput
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "bad_request")
		return
	}
	device, token, err := s.app.CreateDevice(req.Name, req.ConfigTOML)
	switch {
	case errors.Is(err, app.ErrInvalidInput):
		httpx.WriteError(w, http.StatusBadRequest, "invalid_request")
		return
	case errors.Is(err, app.ErrLimit):
		httpx.WriteError(w, http.StatusConflict, "device_limit")
		return
	case err != nil:
		s.logger.Error("create device", "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, createDeviceResponse{ID: device.PublicID, Token: token})
}

func (s *Server) handleGetDevice(w http.ResponseWriter, r *http.Request) {
	device, found, err := s.app.DeviceByID(r.PathValue("id"))
	if err != nil {
		s.logger.Error("get device", "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal")
		return
	}
	if !found {
		httpx.WriteError(w, http.StatusNotFound, "not_found")
		return
	}
	release, _, err := s.app.Release(device.LastPlatform)
	if err != nil {
		s.logger.Error("read release", "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, detailOf(device, release, s.app.Now()))
}

func (s *Server) handleUpdateDevice(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req deviceInput
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "bad_request")
		return
	}
	updated, err := s.app.UpdateDevice(id, req.Name, req.ConfigTOML)
	switch {
	case errors.Is(err, app.ErrInvalidInput):
		httpx.WriteError(w, http.StatusBadRequest, "invalid_request")
		return
	case errors.Is(err, app.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "not_found")
		return
	case err != nil:
		s.logger.Error("update device", "id", id, "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"config_hash": updated.ConfigHash})
}

func (s *Server) handleDeleteDevice(w http.ResponseWriter, r *http.Request) {
	_, err := s.app.DeleteDevice(r.PathValue("id"))
	switch {
	case errors.Is(err, app.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "not_found")
		return
	case err != nil:
		s.logger.Error("delete device", "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleIssueToken(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	token, err := s.app.IssueToken(id)
	switch {
	case errors.Is(err, app.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "not_found")
		return
	case err != nil:
		s.logger.Error("issue token", "id", id, "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"token": token})
}
