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

// releaseView is one platform's published build in the admin payload.
type releaseView struct {
	Platform   string `json:"platform"`
	AppVersion string `json:"app_version"`
	AppURL     string `json:"app_url"`
	AppHash    string `json:"app_hash"`
	AppSHA256  string `json:"app_sha256"`
	AppNotes   string `json:"app_notes"`
}

// releasesResponse is the admin view of the release table plus the template.
type releasesResponse struct {
	DefaultTOML string        `json:"default_toml"`
	Releases    []releaseView `json:"releases"`
}

// releaseInput updates one platform through a JSON body.
type releaseInput struct {
	Platform    *string `json:"platform"`
	AppVersion  *string `json:"app_version"`
	AppURL      *string `json:"app_url"`
	AppNotes    *string `json:"app_notes"`
	DefaultTOML *string `json:"default_toml"`
}

func toView(r db.Release) releaseView {
	return releaseView{
		Platform:   r.Platform,
		AppVersion: r.Version,
		AppURL:     r.URL,
		AppHash:    r.Hash,
		AppSHA256:  r.SHA256,
		AppNotes:   r.Notes,
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

// handlePublishRelease points one platform at an external download URL and may
// carry a new default template.
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
	switch {
	case req.AppURL != nil && req.AppVersion != nil:
		if platform == "" {
			httpx.WriteErrorDetail(w, http.StatusBadRequest, "invalid_argument", "platform 不能为空")
			return
		}
		notes := ""
		if req.AppNotes != nil {
			notes = *req.AppNotes
		}
		if _, err := s.app.SetExternal(platform, *req.AppVersion, *req.AppURL, notes); err != nil {
			s.writeReleaseError(w, "publish external release", err)
			return
		}
	case req.AppVersion == nil && req.AppURL == nil:
		// Only the template is being edited; keep the releases as they are.
	default:
		httpx.WriteErrorDetail(w, http.StatusBadRequest, "invalid_argument", "app_version 与 app_url 必须同时给出")
		return
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

// handleDeleteRelease clears the build published for one platform.
func (s *Server) handleDeleteRelease(w http.ResponseWriter, r *http.Request) {
	platform := strings.TrimSpace(r.PathValue("platform"))
	if err := s.app.ClearRelease(platform); err != nil {
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
