package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"mesh-server/app"
	"mesh-server/db"
	"mesh-server/httpx"
	"mesh-server/text"
)

// deviceSummary is one row of GET /api/admin/devices (contract/admin-api.md).
type deviceSummary struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Hostname       string `json:"hostname"`
	DeviceID       string `json:"deviceId"`
	Status         string `json:"status"`
	ConfigHash     string `json:"configHash"`
	LastConfigHash string `json:"lastConfigHash"`
	LastAppVersion string `json:"lastAppVersion"`
	ConfigState    string `json:"configState"`
	AppState       string `json:"appState"`
	AppVersion     string `json:"appVersion"`
	Platform       string `json:"lastPlatform"`
	GroupID        int64  `json:"groupId"`
	Position       int64  `json:"position"`
	CreatedAt      int64  `json:"createdAt"`
	LastSeenAt     int64  `json:"lastSeenAt"`
	// Identity、IPv4、DHCP 是从 configToml 派生的展示字段（app.ConfigFactsOf）。
	Identity string `json:"identity"`
	IPv4     string `json:"ipv4"`
	DHCP     bool   `json:"dhcp"`
	// AutoUpdate 是设备级自动更新覆盖：null 跟随发布包，true 强制自动，false
	// 强制手动（contract/admin-api.md「设备级自动更新」）。
	AutoUpdate *bool `json:"autoUpdate"`
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
	facts := app.ConfigFactsOf(d.ConfigTOML)
	return deviceSummary{
		ID:             d.PublicID,
		Name:           d.Name,
		Hostname:       d.Hostname,
		DeviceID:       d.DeviceID,
		Status:         app.DeviceStatus(d, now),
		ConfigHash:     d.ConfigHash,
		LastConfigHash: d.LastConfigHash,
		LastAppVersion: d.LastAppVersion,
		ConfigState:    app.ConfigState(d),
		AppState:       app.AppState(d, release),
		AppVersion:     release.Version,
		Platform:       d.LastPlatform,
		GroupID:        d.GroupID,
		Position:       d.Position,
		CreatedAt:      d.CreatedAt,
		LastSeenAt:     d.LastSeenAt,
		Identity:       facts.Identity,
		IPv4:           facts.IPv4,
		DHCP:           facts.DHCP,
		AutoUpdate:     d.AutoUpdate,
	}
}

// deviceDetail is the full device record GET /api/admin/devices/{id} returns: the
// same projection as one list row (embedded, so the JSON stays flat) plus the
// fields only the detail view exposes.
type deviceDetail struct {
	deviceSummary
	Pubkey         string `json:"pubkey"`
	ConfigTOML     string `json:"configToml"`
	Token          string `json:"token"`
	TokenExpiresAt int64  `json:"tokenExpiresAt"`
	LastIP         string `json:"lastIp"`
	// Credential 是最新一张凭据的签发时间与到期时间；没有凭据时是 null。
	// 私钥从不出现在这里。
	Credential *credentialView `json:"credential"`
}

// credentialView 是设备凭据的可见部分：只有时间，没有私钥。
type credentialView struct {
	IssuedAt  int64 `json:"issuedAt"`
	ExpiresAt int64 `json:"expiresAt"`
}

func detailOf(d db.Device, release db.Release, now time.Time) deviceDetail {
	return deviceDetail{
		deviceSummary:  summarize(d, release, now),
		Pubkey:         d.Pubkey,
		ConfigTOML:     d.ConfigTOML,
		Token:          d.Token,
		TokenExpiresAt: d.TokenExpiresAt,
		LastIP:         d.LastIP,
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

// templateResponse is the default configuration exactly as stored: placeholders
// and all. 控制面不解释它的内容——占位符怎么变成表单、默认值怎么算都是控制台
// 的事（frontend/src/template.ts）。
type templateResponse struct {
	Template string `json:"template"`
}

func (s *Server) handleTemplate(w http.ResponseWriter, r *http.Request) {
	// 读不出来就报 500：回退内置模板会让控制台显示一份并非存储的内容，一保存
	// 就把原文件覆盖掉。
	toml, err := s.app.TemplateTOML()
	if err != nil {
		s.logger.Error("read default template", "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, templateResponse{Template: toml})
}

type deviceInput struct {
	Name       string `json:"name"`
	ConfigTOML string `json:"configToml"`
}

// createDeviceRequest is POST /api/admin/devices. AutoUpdate 只用来拒绝它：新设备
// 一律跟随发布包（contract/admin-api.md），而静默丢掉会让调用方以为自己设上了。
// 与发布接口对 appVersion 的处理一致。
type createDeviceRequest struct {
	deviceInput
	AutoUpdate optionalBool `json:"autoUpdate"`
}

// deviceUpdate is PUT /api/admin/devices/{id}: the fields every update rewrites
// plus the optional device-level auto-update override.
type deviceUpdate struct {
	deviceInput
	AutoUpdate optionalBool `json:"autoUpdate"`
}

// optionalBool 是一个三态字段：请求里没有它、它是 null、或者它是 true/false。
// *bool 分不清前两种（都是 nil），而「恢复跟随发布包」正是 null。
type optionalBool struct {
	Set   bool
	Value *bool
}

func (o *optionalBool) UnmarshalJSON(raw []byte) error {
	o.Set = true
	if string(raw) == "null" {
		o.Value = nil
		return nil
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	o.Value = &value
	return nil
}

type createDeviceResponse struct {
	ID    string `json:"id"`
	Token string `json:"token"`
	// IPv4 是从落库配置解析出来的地址，界面直接回显，省得再拉一次详情。
	IPv4 string `json:"ipv4,omitempty"`
}

func (s *Server) handleCreateDevice(w http.ResponseWriter, r *http.Request) {
	var req createDeviceRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "bad_request")
		return
	}
	if req.AutoUpdate.Set {
		httpx.WriteErrorDetail(w, http.StatusBadRequest, "invalid_argument", text.Get(text.APIAutoUpdatePerDevice))
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
	httpx.WriteJSON(w, http.StatusOK, createDeviceResponse{
		ID:    device.PublicID,
		Token: token,
		IPv4:  app.ConfigFactsOf(device.ConfigTOML).IPv4,
	})
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
	detail := detailOf(device, release, s.app.Now())
	credential, err := s.app.LatestCredential(device.PublicID)
	switch {
	case errors.Is(err, db.ErrCredentialNotFound):
		// 还没有凭据：保持 null，界面显示「无凭据」。
	case err != nil:
		s.logger.Error("read credential", "id", device.PublicID, "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal")
		return
	default:
		detail.Credential = &credentialView{IssuedAt: credential.CreatedAt, ExpiresAt: credential.ExpiresAt}
	}
	httpx.WriteJSON(w, http.StatusOK, detail)
}

func (s *Server) handleUpdateDevice(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req deviceUpdate
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "bad_request")
		return
	}
	updated, err := s.app.UpdateDevice(id, req.Name, req.ConfigTOML, app.AutoUpdateChoice{Set: req.AutoUpdate.Set, Value: req.AutoUpdate.Value})
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
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"configHash": updated.ConfigHash})
}

func (s *Server) handleDeleteDevice(w http.ResponseWriter, r *http.Request) {
	_, err := s.app.DeleteDevice(r.Context(), r.PathValue("id"))
	switch {
	case errors.Is(err, app.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "not_found")
		return
	case errors.Is(err, app.ErrCredentialChannelUnconfigured):
		writeCredentialChannelUnconfigured(w)
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

// handleIssueCredential 给设备签发一张凭据，私钥只在这一个响应里出现。
// 这张凭据不会立刻到设备上：设备下一次心跳仍报着旧 id，等冷却窗口过后才会
// 由心跳补签一张新的下发（报 none 时则直接拿到）。
func (s *Server) handleIssueCredential(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	issued, err := s.app.IssueCredential(r.Context(), id)
	switch {
	case errors.Is(err, app.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "not_found")
		return
	case errors.Is(err, app.ErrFormalNode):
		httpx.WriteErrorDetail(w, http.StatusConflict, "formal_node", text.Get(text.APIFormalNodeCredential))
		return
	case errors.Is(err, app.ErrCredentialChannelUnconfigured):
		writeCredentialChannelUnconfigured(w)
		return
	case err != nil:
		s.logger.Error("issue credential", "id", id, "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"credential":   issued.Secret,
		"credentialId": issued.ID,
		"expiresAt":    issued.Expiry,
	})
}

// handleRevokeCredential 撤销设备的凭据，全网几秒内断开用它接入的节点。
func (s *Server) handleRevokeCredential(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.app.RevokeCredential(r.Context(), id); err != nil {
		if errors.Is(err, app.ErrCredentialChannelUnconfigured) {
			writeCredentialChannelUnconfigured(w)
			return
		}
		s.logger.Error("revoke credential", "id", id, "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeCredentialChannelUnconfigured 回答「这台控制面没有签发通道，却要动种子
// 上的凭据」：记录留着，种子侧那张凭据仍然有效，只能由运维手工撤销。
func writeCredentialChannelUnconfigured(w http.ResponseWriter) {
	httpx.WriteErrorDetail(w, http.StatusConflict, "credential_channel_unconfigured", text.Get(text.APICredentialChannelUnconfigured))
}
