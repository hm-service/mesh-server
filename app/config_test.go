package app

import "testing"

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
		{
			name: "unparsable config falls back to credential",
			toml: "[[peer]\nuri = \"udp://seed:11010\"\n",
			want: credential,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ConfigFactsOf(tc.toml); got != tc.want {
				t.Fatalf("ConfigFactsOf() = %+v, want %+v", got, tc.want)
			}
		})
	}
}
