package api

import (
	"errors"
	"net/http"
	"time"

	"mesh-server/app"
	"mesh-server/db"
	"mesh-server/httpx"
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
	}
}

// deviceDetail is the full device record GET /api/admin/devices/{id} returns, plus
// the derived status fields the console shows.
type deviceDetail struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	DeviceID       string `json:"deviceId"`
	Hostname       string `json:"hostname"`
	Pubkey         string `json:"pubkey"`
	ConfigTOML     string `json:"configToml"`
	ConfigHash     string `json:"configHash"`
	Token          string `json:"token"`
	TokenExpiresAt int64  `json:"tokenExpiresAt"`
	CreatedAt      int64  `json:"createdAt"`
	LastSeenAt     int64  `json:"lastSeenAt"`
	LastIP         string `json:"lastIp"`
	LastConfigHash string `json:"lastConfigHash"`
	LastAppVersion string `json:"lastAppVersion"`
	Status         string `json:"status"`
	ConfigState    string `json:"configState"`
	AppState       string `json:"appState"`
	AppVersion     string `json:"appVersion"`
	Platform       string `json:"lastPlatform"`
	GroupID        int64  `json:"groupId"`
	Position       int64  `json:"position"`
	// Identity、IPv4、DHCP 是从 configToml 派生的展示字段（app.ConfigFactsOf）。
	Identity string `json:"identity"`
	IPv4     string `json:"ipv4"`
	DHCP     bool   `json:"dhcp"`
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
	facts := app.ConfigFactsOf(d.ConfigTOML)
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
		LastAppVersion: d.LastAppVersion,
		Status:         app.DeviceStatus(d, now),
		ConfigState:    app.ConfigState(d),
		AppState:       app.AppState(d, release),
		AppVersion:     release.Version,
		Platform:       d.LastPlatform,
		GroupID:        d.GroupID,
		Position:       d.Position,
		Identity:       facts.Identity,
		IPv4:           facts.IPv4,
		DHCP:           facts.DHCP,
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
	ConfigTOML string `json:"configToml"`
}

type createDeviceResponse struct {
	ID    string `json:"id"`
	Token string `json:"token"`
	// IPv4 是模板注入后落下来的地址，界面直接回显，省得再拉一次详情。
	IPv4 string `json:"ipv4,omitempty"`
}

// writeTemplateError 把模板注入的失败原因写给调用方。返回 true 表示响应已写。
// 这几条都出在管理员手写的模板上，光给一个 invalid_request 他改不动。
func writeTemplateError(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, app.ErrUnknownPlaceholder):
		httpx.WriteErrorDetail(w, http.StatusBadRequest, "invalid_request",
			"配置模板里有不认识的占位，目前只支持 "+app.TemplateIP)
		return true
	case errors.Is(err, app.ErrNoAddressPool):
		httpx.WriteErrorDetail(w, http.StatusBadRequest, "invalid_request",
			"还没有设备写过固定地址，推不出要分配哪个网段：先手工建一台带 ipv4 的设备")
		return true
	case errors.Is(err, app.ErrAddressExhausted):
		httpx.WriteErrorDetail(w, http.StatusBadRequest, "invalid_request",
			"参考网段里已经没有空闲地址了")
		return true
	}
	return false
}

func (s *Server) handleCreateDevice(w http.ResponseWriter, r *http.Request) {
	var req deviceInput
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "bad_request")
		return
	}
	device, token, err := s.app.CreateDevice(req.Name, req.ConfigTOML)
	if writeTemplateError(w, err) {
		return
	}
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
	var req deviceInput
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "bad_request")
		return
	}
	updated, err := s.app.UpdateDevice(id, req.Name, req.ConfigTOML)
	if writeTemplateError(w, err) {
		return
	}
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
		httpx.WriteErrorDetail(w, http.StatusConflict, "formal_node",
			"这台设备是正式节点（配置里有 network_secret），不使用凭据，轮换没有意义")
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
	httpx.WriteErrorDetail(w, http.StatusConflict, "credential_channel_unconfigured",
		"未配置凭据签发通道（ET_CLI），无法在种子上撤销；请在种子上手工撤销这些凭据")
}
