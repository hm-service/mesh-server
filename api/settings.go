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
// AutoUpdate 是跟着客户端包走的开关：true 表示客户端收到这个版本后可以自己装，
// false 表示只提示、等用户手动确认。
type releaseView struct {
	Platform        string `json:"platform"`
	AppVersion      string `json:"app_version"`
	AppURL          string `json:"app_url"`
	AppSHA256       string `json:"app_sha256"`
	AppNotes        string `json:"app_notes"`
	AutoUpdate      bool   `json:"auto_update"`
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
// on its own: a client package is addressed by app_url, a launcher by its
// version and URL together.
//
// AppVersion only exists to reject it: the published version is the one the
// package's version.json declares, read while the digest is computed. Silently
// ignoring the field would let a caller believe the label it sent was stored.
type releaseInput struct {
	Platform        *string `json:"platform"`
	AppVersion      *string `json:"app_version"`
	AppURL          *string `json:"app_url"`
	AppNotes        *string `json:"app_notes"`
	AutoUpdate      *bool   `json:"auto_update"`
	LauncherVersion *string `json:"launcher_version"`
	LauncherURL     *string `json:"launcher_url"`
	DefaultTOML     *string `json:"default_toml"`
}

func toView(r db.Release) releaseView {
	return releaseView{
		Platform:        r.Platform,
		AppVersion:      r.Version,
		AppURL:          r.URL,
		AppSHA256:       r.SHA256,
		AppNotes:        r.Notes,
		AutoUpdate:      r.AutoUpdate,
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
	if req.AppVersion != nil {
		httpx.WriteErrorDetail(w, http.StatusBadRequest, "invalid_argument",
			"app_version 由包内 version.json 读出，不再作为请求字段")
		return
	}
	platform := ""
	if req.Platform != nil {
		platform = strings.TrimSpace(*req.Platform)
	}
	client := req.AppURL != nil
	launcher := req.LauncherVersion != nil || req.LauncherURL != nil
	// 备注与自动安装开关都属于客户端包，没有包可挂时不接受：否则要么写进一行
	// 没有发布内容的记录，要么被悄悄丢掉，两种都不是调用方要的结果。
	if req.AppNotes != nil && !client {
		httpx.WriteErrorDetail(w, http.StatusBadRequest, "invalid_argument", "app_notes 必须与 app_url 一起给出")
		return
	}
	if req.AutoUpdate != nil && !client {
		httpx.WriteErrorDetail(w, http.StatusBadRequest, "invalid_argument", "auto_update 必须与 app_url 一起给出")
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
	publish := app.PublishInput{Platform: platform, DefaultTOML: req.DefaultTOML}
	if client {
		notes := ""
		if req.AppNotes != nil {
			notes = *req.AppNotes
		}
		// 省略 auto_update 等于"手动"：每次发布客户端包都会重写这个开关，
		// 新包默认不让客户端自己装。
		autoUpdate := false
		if req.AutoUpdate != nil {
			autoUpdate = *req.AutoUpdate
		}
		publish.Client = &app.ClientPackage{URL: *req.AppURL, Notes: notes, AutoUpdate: autoUpdate}
	}
	if launcher {
		publish.Launcher = &db.LauncherSlot{Version: *req.LauncherVersion, URL: *req.LauncherURL}
	}
	// One call for both slots and the template: a rejected request has written
	// nothing, so the admin never sees a 400 for a half-applied publish.
	if err := s.app.Publish(publish); err != nil {
		s.writeReleaseError(w, "publish release", err)
		return
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
