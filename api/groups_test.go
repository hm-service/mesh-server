package api_test

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"mesh-server/internal/testenv"
)

// groupAPIView mirrors one row of GET /api/admin/groups.
type groupAPIView struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Position int64  `json:"position"`
}

// groupedDevice is the part of GET /api/admin/devices the layout tests care about.
type groupedDevice struct {
	ID       string `json:"id"`
	GroupID  int64  `json:"groupId"`
	Position int64  `json:"position"`
}

func createGroup(t *testing.T, env *testenv.Env, name string) groupAPIView {
	t.Helper()
	res, data := env.Do("POST", "/api/admin/groups", map[string]string{"name": name})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/admin/groups = %d, want 200: %s", res.StatusCode, data)
	}
	var group groupAPIView
	if err := json.Unmarshal(data, &group); err != nil {
		t.Fatalf("decode group: %v", err)
	}
	if group.ID == 0 || group.Name != name {
		t.Fatalf("created group = %+v", group)
	}
	return group
}

func listGroups(t *testing.T, env *testenv.Env) []groupAPIView {
	t.Helper()
	res, data := env.Do("GET", "/api/admin/groups", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/admin/groups = %d: %s", res.StatusCode, data)
	}
	var groups []groupAPIView
	if err := json.Unmarshal(data, &groups); err != nil {
		t.Fatalf("decode groups: %v", err)
	}
	return groups
}

func listDeviceLayout(t *testing.T, env *testenv.Env) map[string]groupedDevice {
	t.Helper()
	res, data := env.Do("GET", "/api/admin/devices", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/admin/devices = %d: %s", res.StatusCode, data)
	}
	var devices []groupedDevice
	if err := json.Unmarshal(data, &devices); err != nil {
		t.Fatalf("decode devices: %v", err)
	}
	out := make(map[string]groupedDevice, len(devices))
	for _, device := range devices {
		out[device.ID] = device
	}
	return out
}

func TestGroupAPI(t *testing.T) {
	env := testenv.New(t)
	if groups := listGroups(t, env); len(groups) != 0 {
		t.Fatalf("fresh groups = %+v, want none", groups)
	}
	office := createGroup(t, env, "办公")
	home := createGroup(t, env, "家里")
	groups := listGroups(t, env)
	if len(groups) != 2 || groups[0].ID != office.ID || groups[1].ID != home.ID {
		t.Fatalf("groups after create = %+v", groups)
	}

	res, data := env.Do("PUT", "/api/admin/groups/"+itoa(office.ID), map[string]any{"name": "办公室", "position": 4})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("PUT group = %d, want 200: %s", res.StatusCode, data)
	}
	var renamed groupAPIView
	if err := json.Unmarshal(data, &renamed); err != nil {
		t.Fatalf("decode renamed group: %v", err)
	}
	if renamed.Name != "办公室" || renamed.Position != 4 {
		t.Fatalf("renamed group = %+v", renamed)
	}
	groups = listGroups(t, env)
	if len(groups) != 2 || groups[0].ID != home.ID || groups[1].ID != office.ID {
		t.Fatalf("group order after move = %+v", groups)
	}

	res, data = env.Do("PUT", "/api/admin/groups/999", map[string]string{"name": "x"})
	if res.StatusCode != http.StatusNotFound || !strings.Contains(string(data), "group_not_found") {
		t.Fatalf("PUT unknown group = %d: %s", res.StatusCode, data)
	}
	res, data = env.Do("POST", "/api/admin/groups", map[string]string{"name": "  "})
	if res.StatusCode != http.StatusBadRequest || !strings.Contains(string(data), "invalid_request") {
		t.Fatalf("POST blank group = %d: %s", res.StatusCode, data)
	}

	res, data = env.Do("DELETE", "/api/admin/groups/"+itoa(office.ID), nil)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE group = %d, want 204: %s", res.StatusCode, data)
	}
	if groups = listGroups(t, env); len(groups) != 1 || groups[0].ID != home.ID {
		t.Fatalf("groups after delete = %+v", groups)
	}
	res, data = env.Do("DELETE", "/api/admin/groups/"+itoa(office.ID), nil)
	if res.StatusCode != http.StatusNotFound || !strings.Contains(string(data), "group_not_found") {
		t.Fatalf("DELETE missing group = %d: %s", res.StatusCode, data)
	}
}

func TestDeviceGroupAPI(t *testing.T) {
	env := testenv.New(t)
	group := createGroup(t, env, "办公")
	first := createDevice(t, env, "one", testTOML)
	second := createDevice(t, env, "two", testTOML)

	res, data := env.Do("PUT", "/api/admin/devices/"+first+"/group", map[string]any{"groupId": group.ID})
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT device group = %d, want 204: %s", res.StatusCode, data)
	}
	layout := listDeviceLayout(t, env)
	if got := layout[first]; got.GroupID != group.ID {
		t.Fatalf("device group = %+v, want group %d", got, group.ID)
	}

	res, data = env.Do("PUT", "/api/admin/devices/"+second+"/group", map[string]any{"groupId": nil})
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT device group null = %d, want 204: %s", res.StatusCode, data)
	}
	layout = listDeviceLayout(t, env)
	if got := layout[second]; got.GroupID != 0 {
		t.Fatalf("null groupId left device in %+v", got)
	}

	res, data = env.Do("PUT", "/api/admin/devices/d_missing/group", map[string]any{"groupId": group.ID})
	if res.StatusCode != http.StatusNotFound || !strings.Contains(string(data), "not_found") {
		t.Fatalf("PUT missing device group = %d: %s", res.StatusCode, data)
	}
	res, data = env.Do("PUT", "/api/admin/devices/"+first+"/group", map[string]any{"groupId": 999})
	if res.StatusCode != http.StatusNotFound || !strings.Contains(string(data), "group_not_found") {
		t.Fatalf("PUT device into missing group = %d: %s", res.StatusCode, data)
	}
}

func TestDeleteGroupUngroupsDevicesAPI(t *testing.T) {
	env := testenv.New(t)
	group := createGroup(t, env, "临时")
	first := createDevice(t, env, "one", testTOML)
	second := createDevice(t, env, "two", testTOML)
	for _, id := range []string{first, second} {
		res, data := env.Do("PUT", "/api/admin/devices/"+id+"/group", map[string]any{"groupId": group.ID})
		if res.StatusCode != http.StatusNoContent {
			t.Fatalf("PUT %s group = %d: %s", id, res.StatusCode, data)
		}
	}
	if res, data := env.Do("DELETE", "/api/admin/groups/"+itoa(group.ID), nil); res.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE group = %d: %s", res.StatusCode, data)
	}
	layout := listDeviceLayout(t, env)
	if len(layout) != 2 {
		t.Fatalf("devices after group delete = %+v, want both kept", layout)
	}
	for _, id := range []string{first, second} {
		if got := layout[id]; got.GroupID != 0 {
			t.Fatalf("%s after group delete = %+v, want ungrouped", id, got)
		}
	}
}

func TestDeviceOrderAPI(t *testing.T) {
	env := testenv.New(t)
	office := createGroup(t, env, "办公")
	home := createGroup(t, env, "家里")
	first := createDevice(t, env, "one", testTOML)
	second := createDevice(t, env, "two", testTOML)
	third := createDevice(t, env, "three", testTOML)

	order := []map[string]any{
		{"id": first, "groupId": home.ID, "position": 1},
		{"id": second, "groupId": office.ID, "position": 0},
		{"id": third, "groupId": home.ID, "position": 0},
	}
	res, data := env.Do("PUT", "/api/admin/layout", map[string]any{"devices": order})
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT layout = %d, want 204: %s", res.StatusCode, data)
	}
	layout := listDeviceLayout(t, env)
	checks := map[string]groupedDevice{
		first:  {ID: first, GroupID: home.ID, Position: 1},
		second: {ID: second, GroupID: office.ID, Position: 0},
		third:  {ID: third, GroupID: home.ID, Position: 0},
	}
	for id, want := range checks {
		if got := layout[id]; got != want {
			t.Fatalf("%s layout = %+v, want %+v", id, got, want)
		}
	}

	// A layout naming a missing group fails whole; the saved one stays.
	bad := []map[string]any{{"id": first, "groupId": 999, "position": 0}}
	res, data = env.Do("PUT", "/api/admin/layout", map[string]any{"devices": bad})
	if res.StatusCode != http.StatusNotFound || !strings.Contains(string(data), "group_not_found") {
		t.Fatalf("PUT order into missing group = %d: %s", res.StatusCode, data)
	}
	if got := listDeviceLayout(t, env)[first]; got.GroupID != home.ID || got.Position != 1 {
		t.Fatalf("rejected order changed the layout: %+v", got)
	}

	res, data = env.Do("PUT", "/api/admin/layout", map[string]any{"devices": []map[string]any{}})
	if res.StatusCode != http.StatusBadRequest || !strings.Contains(string(data), "invalid_request") {
		t.Fatalf("PUT empty layout = %d: %s", res.StatusCode, data)
	}
}

func TestGroupRoutesRequireSession(t *testing.T) {
	env := testenv.New(t, testenv.WithoutLogin())
	routes := []struct {
		method string
		path   string
	}{
		{"GET", "/api/admin/groups"},
		{"POST", "/api/admin/groups"},
		{"PUT", "/api/admin/groups/1"},
		{"DELETE", "/api/admin/groups/1"},
		{"PUT", "/api/admin/devices/d_000000/group"},
		{"PUT", "/api/admin/layout"},
	}
	for _, route := range routes {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			res, body := env.Do(route.method, route.path, map[string]string{"name": "x"})
			if res.StatusCode != http.StatusUnauthorized {
				t.Fatalf("%s %s = %d, want 401: %s", route.method, route.path, res.StatusCode, body)
			}
		})
	}
}

func itoa(id int64) string {
	return strconv.FormatInt(id, 10)
}
