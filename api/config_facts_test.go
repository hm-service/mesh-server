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

// TestUnparsableConfigIsReportedNotClassified pins F7: a configuration that
// cannot be parsed is not a credential node. That device carries identity "" and
// the parser's message in configError, while the other devices still list
// normally — one broken row must not turn the whole payload into an error.
func TestUnparsableConfigIsReportedNotClassified(t *testing.T) {
	env := testenv.New(t)
	good := registerDevice(t, env, "ali-seed", formalTOML)
	broken := registerDevice(t, env, "broken", "[[peer]\nuri = \"udp://seed:11010\"\n")

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
	if got := byID[good.internalID]; got.Identity != "formal" || got.ConfigError != "" {
		t.Fatalf("formal summary = %+v, want identity=formal without a config error", got)
	}
	brokenSummary := byID[broken.internalID]
	if brokenSummary.Identity != "" || brokenSummary.IPv4 != "" || brokenSummary.DHCP {
		t.Fatalf("broken summary = %+v, want no derived identity or address", brokenSummary)
	}
	if brokenSummary.ConfigError == "" {
		t.Fatalf("broken summary = %+v, want the parse error reported", brokenSummary)
	}

	res, data = env.Do("GET", "/api/admin/devices/"+broken.internalID, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("get device = %d: %s", res.StatusCode, data)
	}
	var detail deviceDetail
	if err := json.Unmarshal(data, &detail); err != nil {
		t.Fatalf("decode device: %v", err)
	}
	if detail.Identity != "" || detail.ConfigError == "" {
		t.Fatalf("broken detail = %+v, want identity empty and the parse error", detail)
	}
}
