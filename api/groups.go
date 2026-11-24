package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"mesh-server/app"
	"mesh-server/db"
	"mesh-server/httpx"
)

// groupView is one row of GET /api/groups.
type groupView struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Position int64  `json:"position"`
}

func groupOf(g db.Group) groupView {
	return groupView{ID: g.ID, Name: g.Name, Position: g.Position}
}

type groupInput struct {
	Name string `json:"name"`
}

// groupPatch leaves a field alone when it is absent from the body.
type groupPatch struct {
	Name     *string `json:"name"`
	Position *int64  `json:"position"`
}

// deviceGroupInput carries the group of one device; null and 0 both mean
// ungrouped.
type deviceGroupInput struct {
	GroupID *int64 `json:"group_id"`
}

// devicePlacementInput is one entry of PUT /api/layout, the layout the console
// sends after a drag.
type devicePlacementInput struct {
	ID       string `json:"id"`
	GroupID  *int64 `json:"group_id"`
	Position int64  `json:"position"`
}

type layoutInput struct {
	Devices []devicePlacementInput `json:"devices"`
}

// writeLayoutError maps the layout use cases to the API error codes.
func (s *Server) writeLayoutError(w http.ResponseWriter, err error, action string, attrs ...any) {
	switch {
	case errors.Is(err, app.ErrGroupNotFound):
		httpx.WriteError(w, http.StatusNotFound, "group_not_found")
	case errors.Is(err, app.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "not_found")
	case errors.Is(err, app.ErrInvalidInput):
		httpx.WriteError(w, http.StatusBadRequest, "invalid_request")
	default:
		s.logger.Error(action, append(attrs, "error", err)...)
		httpx.WriteError(w, http.StatusInternalServerError, "internal")
	}
}

func parseGroupID(raw string) (int64, bool) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

func (s *Server) handleListGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := s.app.ListGroups()
	if err != nil {
		s.logger.Error("list groups", "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal")
		return
	}
	out := make([]groupView, 0, len(groups))
	for _, g := range groups {
		out = append(out, groupOf(g))
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (s *Server) handleCreateGroup(w http.ResponseWriter, r *http.Request) {
	var req groupInput
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "bad_request")
		return
	}
	group, err := s.app.CreateGroup(req.Name)
	if err != nil {
		s.writeLayoutError(w, err, "create group")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, groupOf(group))
}

func (s *Server) handleUpdateGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := parseGroupID(r.PathValue("id"))
	if !ok {
		httpx.WriteError(w, http.StatusBadRequest, "bad_request")
		return
	}
	var req groupPatch
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "bad_request")
		return
	}
	group, err := s.app.UpdateGroup(id, req.Name, req.Position)
	if err != nil {
		s.writeLayoutError(w, err, "update group", "id", id)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, groupOf(group))
}

func (s *Server) handleDeleteGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := parseGroupID(r.PathValue("id"))
	if !ok {
		httpx.WriteError(w, http.StatusBadRequest, "bad_request")
		return
	}
	if err := s.app.DeleteGroup(id); err != nil {
		s.writeLayoutError(w, err, "delete group", "id", id)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSetDeviceGroup(w http.ResponseWriter, r *http.Request) {
	var req deviceGroupInput
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "bad_request")
		return
	}
	groupID := int64(0)
	if req.GroupID != nil {
		groupID = *req.GroupID
	}
	id := r.PathValue("id")
	if err := s.app.SetDeviceGroup(id, groupID); err != nil {
		s.writeLayoutError(w, err, "set device group", "id", id, "group_id", groupID)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeviceLayout(w http.ResponseWriter, r *http.Request) {
	var req layoutInput
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "bad_request")
		return
	}
	items := make([]db.DevicePlacement, 0, len(req.Devices))
	for _, entry := range req.Devices {
		groupID := int64(0)
		if entry.GroupID != nil {
			groupID = *entry.GroupID
		}
		items = append(items, db.DevicePlacement{
			PublicID: strings.TrimSpace(entry.ID),
			GroupID:  groupID,
			Position: entry.Position,
		})
	}
	if err := s.app.SetDeviceLayout(items); err != nil {
		s.writeLayoutError(w, err, "save device layout", "count", len(items))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
