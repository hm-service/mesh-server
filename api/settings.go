package api

import (
	"errors"
	"log/slog"
	"mime"
	"net/http"
	"strings"

	"mesh-server/app"
	"mesh-server/httpx"
)

// settingsResponse is the admin view of the control-plane configuration.
// app_sha256 is the full digest of the published build; app_hash stays the
// 16-character value clients compare.
type settingsResponse struct {
	DefaultTOML string `json:"default_toml"`
	AppVersion  string `json:"app_version"`
	AppURL      string `json:"app_url"`
	AppHash     string `json:"app_hash"`
	AppSHA256   string `json:"app_sha256"`
	AppNotes    string `json:"app_notes"`
	AppSource   string `json:"app_source"`
	AppFileName string `json:"app_file_name"`
	AppSize     int64  `json:"app_size"`
}

// settingsInput updates only the fields present in a JSON body.
type settingsInput struct {
	DefaultTOML *string `json:"default_toml"`
	AppVersion  *string `json:"app_version"`
	AppURL      *string `json:"app_url"`
	AppNotes    *string `json:"app_notes"`
}

func (s *Server) settingsSnapshot() (settingsResponse, error) {
	defaultTOML, err := s.app.DefaultTOMLFile()
	if err != nil {
		return settingsResponse{}, err
	}
	stored, err := s.app.Settings()
	if err != nil {
		return settingsResponse{}, err
	}
	return settingsResponse{
		DefaultTOML: defaultTOML,
		AppVersion:  stored.AppVersion,
		AppURL:      stored.AppURL,
		AppHash:     stored.AppHash,
		AppSHA256:   stored.AppSHA256,
		AppNotes:    stored.AppNotes,
		AppSource:   stored.AppSource,
		AppFileName: stored.AppFileName,
		AppSize:     stored.AppSize,
	}, nil
}

func (s *Server) writeSettings(w http.ResponseWriter) {
	out, err := s.settingsSnapshot()
	if err != nil {
		s.logger.Error("read settings", "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	s.writeSettings(w)
}

// handleUpdateSettings dispatches the two release modes: multipart upload and
// external URL. Both may carry a new default template.
func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	if contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err == nil && contentType == "multipart/form-data" {
		s.updateSettingsFromUpload(w, r)
		return
	}
	s.updateSettingsFromJSON(w, r)
}

func (s *Server) updateSettingsFromJSON(w http.ResponseWriter, r *http.Request) {
	var req settingsInput
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.WriteErrorDetail(w, http.StatusBadRequest, "invalid_request", "请求内容不合法")
		return
	}
	if _, err := s.app.ApplySettingsPatch(app.SettingsPatch{
		AppVersion: req.AppVersion,
		AppURL:     req.AppURL,
		AppNotes:   req.AppNotes,
	}); err != nil {
		s.writeSettingsError(w, "update settings", err)
		return
	}
	if req.DefaultTOML != nil {
		if err := s.app.SaveDefaultTOML(*req.DefaultTOML); err != nil {
			s.logger.Error("write default template", "error", err)
			httpx.WriteError(w, http.StatusInternalServerError, "internal")
			return
		}
	}
	s.writeSettings(w)
}

func (s *Server) updateSettingsFromUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, app.MaxUploadBytes+app.UploadHeadroom)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			httpx.WriteErrorDetail(w, http.StatusBadRequest, "file_too_large", "客户端文件不能超过 200MB")
			return
		}
		httpx.WriteErrorDetail(w, http.StatusBadRequest, "invalid_request", "请求必须是 multipart/form-data")
		return
	}
	if r.MultipartForm != nil {
		defer func() { _ = r.MultipartForm.RemoveAll() }()
	}
	if mode := strings.TrimSpace(r.FormValue("mode")); mode != "" && mode != app.SettingSourceUpload {
		httpx.WriteErrorDetail(w, http.StatusBadRequest, "invalid_argument", "mode 必须是 upload")
		return
	}
	version := strings.TrimSpace(r.FormValue("app_version"))
	if version == "" {
		httpx.WriteErrorDetail(w, http.StatusBadRequest, "invalid_argument", "app_version 不能为空")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		httpx.WriteErrorDetail(w, http.StatusBadRequest, "invalid_argument", "请选择客户端 exe")
		return
	}
	defer file.Close()
	if header.Size > app.MaxUploadBytes {
		httpx.WriteErrorDetail(w, http.StatusBadRequest, "file_too_large", "客户端文件不能超过 200MB")
		return
	}
	if _, err := s.app.PublishUpload(version, r.FormValue("app_notes"), file); err != nil {
		s.writeSettingsError(w, "publish upload", err)
		return
	}
	if err := s.app.SaveDefaultTOML(r.FormValue("default_toml")); err != nil {
		s.logger.Error("write default template", "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal")
		return
	}
	s.writeSettings(w)
}

// writeSettingsError maps a rejected admin input to the documented 400 and
// anything else to an opaque 500.
func (s *Server) writeSettingsError(w http.ResponseWriter, operation string, err error) {
	if invalid, ok := errors.AsType[*app.ValidationError](err); ok {
		httpx.WriteErrorDetail(w, http.StatusBadRequest, "invalid_argument", invalid.Message)
		return
	}
	s.logger.Error(operation, slog.Any("error", err))
	httpx.WriteError(w, http.StatusInternalServerError, "internal")
}
