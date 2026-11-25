package api_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"mesh-server/internal/testenv"
)

// formalTOML is a seed-like configuration: a fixed address and network_secret.
const formalTOML = `hostname = "ali-seed"
ipv4 = "10.126.126.1/24"

[network_identity]
network_name = "test-net"
network_secret = "seed-secret"
`

// TestDevicePayloadCarriesConfigFacts pins the fields the console derives from
// configToml: the node identity, the fixed mesh address and whether DHCP is on.
// The list and the detail must agree; the browser depends on both.
func TestDevicePayloadCarriesConfigFacts(t *testing.T) {
	env := testenv.New(t)
	seed := registerDevice(t, env, "ali-seed", formalTOML)
	dhcp := registerDevice(t, env, "wsl", "dhcp = true\n\n[network_identity]\nnetwork_name = \"test-net\"\n")

	res, data := env.Do("GET", "/api/admin/devices", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list devices = %d: %s", res.StatusCode, data)
	}
	var list []deviceSummary
	if err := json.Unmarshal(data, &list); err != nil {
		t.Fatalf("decode devices: %v", err)
	}
	byID := make(map[string]deviceSummary, len(list))
	for _, summary := range list {
		byID[summary.ID] = summary
	}
	if got := byID[seed.internalID]; got.Identity != "formal" || got.IPv4 != "10.126.126.1/24" || got.DHCP {
		t.Fatalf("formal summary = %+v, want identity=formal ipv4=10.126.126.1/24 dhcp=false", got)
	}
	if got := byID[dhcp.internalID]; got.Identity != "credential" || got.IPv4 != "" || !got.DHCP {
		t.Fatalf("dhcp summary = %+v, want identity=credential ipv4= dhcp=true", got)
	}

	res, data = env.Do("GET", "/api/admin/devices/"+seed.internalID, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("get device = %d: %s", res.StatusCode, data)
	}
	var detail deviceDetail
	if err := json.Unmarshal(data, &detail); err != nil {
		t.Fatalf("decode device: %v", err)
	}
	if detail.Identity != "formal" || detail.IPv4 != "10.126.126.1/24" || detail.DHCP {
		t.Fatalf("formal detail = %+v, want the same facts as the summary", detail)
	}
	if detail.Credential != nil {
		t.Fatalf("detail of a device without credentials carries %+v", detail.Credential)
	}
}
