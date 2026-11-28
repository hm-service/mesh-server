package app

import (
	"strings"
	"testing"
)

// TestConfigFactsOf pins how the console classifies a device: the identity
// comes from network_secret under [network_identity] only (the top level is no
// longer accepted), and a fixed mesh address only counts when DHCP is not on.
func TestConfigFactsOf(t *testing.T) {
	credential := ConfigFacts{Identity: IdentityCredential}
	formalFixed := ConfigFacts{Identity: IdentityFormal, IPv4: "10.126.126.1/24"}
	cases := []struct {
		name string
		toml string
		want ConfigFacts
	}{
		{
			name: "credential node",
			toml: "dhcp = true\n\n[network_identity]\nnetwork_name = \"test-net\"\n",
			want: ConfigFacts{Identity: IdentityCredential, DHCP: true},
		},
		{
			name: "formal node in network_identity",
			toml: "ipv4 = \"10.126.126.1/24\"\n\n[network_identity]\nnetwork_secret = \"s\"\n",
			want: formalFixed,
		},
		{
			name: "top level network_secret is not an identity",
			toml: "network_secret = \"s\"\nipv4 = \"10.126.126.1/24\"\n",
			want: ConfigFacts{Identity: IdentityCredential, IPv4: "10.126.126.1/24"},
		},
		{
			name: "blank network_secret is not formal",
			toml: "[network_identity]\nnetwork_secret = \"  \"\n",
			want: credential,
		},
		{
			name: "ipv4 array uses the first address",
			toml: "ipv4 = [\"10.126.126.3/24\", \"10.126.126.4/24\"]\n",
			want: ConfigFacts{Identity: IdentityCredential, IPv4: "10.126.126.3/24"},
		},
		{
			name: "dhcp on leaves the address unused",
			toml: "dhcp = true\nipv4 = \"10.126.126.3/24\"\n",
			want: ConfigFacts{Identity: IdentityCredential, IPv4: "10.126.126.3/24", DHCP: true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ConfigFactsOf(tc.toml)
			if err != nil {
				t.Fatalf("ConfigFactsOf() error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("ConfigFactsOf() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestConfigFactsOfKeepsTheParseError pins F7: a configuration that cannot be
// parsed is not a credential node. The console shows the error on that device
// instead of inventing an identity, and issuance uses the same answer.
func TestConfigFactsOfKeepsTheParseError(t *testing.T) {
	const broken = "[[peer]\nuri = \"udp://seed:11010\"\n"
	facts, err := ConfigFactsOf(broken)
	if err == nil {
		t.Fatal("ConfigFactsOf() accepted an unparsable configuration")
	}
	if facts != (ConfigFacts{}) {
		t.Fatalf("ConfigFactsOf() = %+v, want the zero facts", facts)
	}

	// 签发路径拿的是同一个答案：坏配置既不算正式节点，也不算凭据节点。
	a := openCredentialTemp(t)
	pubkey, _ := testKeypair(t)
	device, err := a.CreateDevice("broken", broken, pubkey)
	if err != nil {
		t.Fatalf("CreateDevice() error: %v", err)
	}
	_, err = a.IssueCredential(t.Context(), device.PublicID)
	if err == nil || !strings.Contains(err.Error(), "读不出来") {
		t.Fatalf("IssueCredential() on an unparsable config = %v, want the parse failure", err)
	}
}
