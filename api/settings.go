package api

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"mesh-server/app"
	"mesh-server/db"
	"mesh-server/httpx"
)

// releaseView is one platform's two published slots in the admin payload.
type releaseView struct {
	Platform        string `json:"platform"`
	AppVersion      string `json:"app_version"`
	AppURL          string `json:"app_url"`
	AppHash         string `json:"app_hash"`
	AppSHA256       string `json:"app_sha256"`
	AppNotes        string `json:"app_notes"`
	LauncherVersion string `json:"launcher_version"`
	LauncherURL     string `json:"launcher_url"`
	LauncherSHA256  string `json:"launcher_sha256"`
}

// releasesResponse is the admin view of the release table plus the template.
type releasesResponse struct {
	DefaultTOML string        `json:"default_toml"`
	Releases    []releaseView `json:"releases"`
}

// releaseInput updates one platform through a JSON body. Each slot is published
// on its own: the version and URL of a slot must arrive together.
type releaseInput struct {
	Platform        *string `json:"platform"`
	AppVersion      *string `json:"app_version"`
	AppURL          *string `json:"app_url"`
	AppNotes        *string `json:"app_notes"`
	LauncherVersion *string `json:"launcher_version"`
	LauncherURL     *string `json:"launcher_url"`
	DefaultTOML     *string `json:"default_toml"`
}

func toView(r db.Release) releaseView {
	return releaseView{
		Platform:        r.Platform,
		AppVersion:      r.Version,
		AppURL:          r.URL,
		AppHash:         r.Hash,
		AppSHA256:       r.SHA256,
		AppNotes:        r.Notes,
		LauncherVersion: r.LauncherVersion,
		LauncherURL:     r.LauncherURL,
		LauncherSHA256:  r.LauncherSHA256,
	}
}

func (s *Server) releasesSnapshot() (releasesResponse, error) {
	toml, err := s.app.DefaultTOMLFile()
	if err != nil {
		return releasesResponse{}, err
	}
	list, err := s.app.Releases()
	if err != nil {
		return releasesResponse{}, err
	}
	out := releasesResponse{DefaultTOML: toml, Releases: make([]releaseView, 0, len(list))}
	for _, r := range list {
		out.Releases = append(out.Releases, toView(r))
	}
	return out, nil
}

func (s *Server) writeReleases(w http.ResponseWriter) {
	out, err := s.releasesSnapshot()
	if err != nil {
		s.logger.Error("read releases", "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (s *Server) handleListReleases(w http.ResponseWriter, r *http.Request) {
	s.writeReleases(w)
}

// handlePublishRelease publishes the client package slot and/or the launcher
// slot of one platform and may carry a new default template.
func (s *Server) handlePublishRelease(w http.ResponseWriter, r *http.Request) {
	var req releaseInput
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.WriteErrorDetail(w, http.StatusBadRequest, "invalid_request", "请求内容不合法")
		return
	}
	platform := ""
	if req.Platform != nil {
		platform = strings.TrimSpace(*req.Platform)
	}
	client := req.AppVersion != nil || req.AppURL != nil
	launcher := req.LauncherVersion != nil || req.LauncherURL != nil
	if client && (req.AppVersion == nil || req.AppURL == nil) {
		httpx.WriteErrorDetail(w, http.StatusBadRequest, "invalid_argument", "app_version 与 app_url 必须同时给出")
		return
	}
	if launcher && (req.LauncherVersion == nil || req.LauncherURL == nil) {
		httpx.WriteErrorDetail(w, http.StatusBadRequest, "invalid_argument", "launcher_version 与 launcher_url 必须同时给出")
		return
	}
	if (client || launcher) && platform == "" {
		httpx.WriteErrorDetail(w, http.StatusBadRequest, "invalid_argument", "platform 不能为空")
		return
	}
	if client {
		notes := ""
		if req.AppNotes != nil {
			notes = *req.AppNotes
		}
		if err := s.app.SetClientExternal(platform, *req.AppVersion, *req.AppURL, notes); err != nil {
			s.writeReleaseError(w, "publish client release", err)
			return
		}
	}
	if launcher {
		if err := s.app.SetLauncherExternal(platform, *req.LauncherVersion, *req.LauncherURL); err != nil {
			s.writeReleaseError(w, "publish launcher release", err)
			return
		}
	}
	if req.DefaultTOML != nil {
		if err := s.app.SaveDefaultTOML(*req.DefaultTOML); err != nil {
			s.logger.Error("write default template", "error", err)
			httpx.WriteError(w, http.StatusInternalServerError, "internal")
			return
		}
	}
	s.writeReleases(w)
}

// handleDeleteRelease clears one platform: both slots by default, only the
// launcher slot with ?slot=launcher.
func (s *Server) handleDeleteRelease(w http.ResponseWriter, r *http.Request) {
	platform := strings.TrimSpace(r.PathValue("platform"))
	var err error
	switch slot := strings.TrimSpace(r.URL.Query().Get("slot")); slot {
	case "":
		err = s.app.ClearRelease(platform)
	case "launcher":
		err = s.app.ClearLauncher(platform)
	default:
		httpx.WriteErrorDetail(w, http.StatusBadRequest, "invalid_argument", "slot 只能是 launcher")
		return
	}
	if err != nil {
		s.writeReleaseError(w, "clear release", err)
		return
	}
	s.writeReleases(w)
}

// writeReleaseError maps a rejected admin input to 400 and anything else to 500.
func (s *Server) writeReleaseError(w http.ResponseWriter, operation string, err error) {
	if invalid, ok := errors.AsType[*app.ValidationError](err); ok {
		httpx.WriteErrorDetail(w, http.StatusBadRequest, "invalid_argument", invalid.Message)
		return
	}
	s.logger.Error(operation, slog.Any("error", err))
	httpx.WriteError(w, http.StatusInternalServerError, "internal")
}
