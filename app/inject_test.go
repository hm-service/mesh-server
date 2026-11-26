package app

import (
	"errors"
	"strconv"
	"testing"
)

func TestAllocateIPv4(t *testing.T) {
	cases := []struct {
		name string
		used []string
		want string
		err  error
	}{
		{name: "no devices", used: nil, err: ErrNoAddressPool},
		{
			name: "picks the gap right after the lowest address",
			used: []string{"10.126.126.1/24", "10.126.126.2/24", "10.126.126.3/24", "10.126.126.100/24", "10.126.126.202/24"},
			want: "10.126.126.4",
		},
		{name: "single high address", used: []string{"10.126.126.202/24"}, want: "10.126.126.203"},
		{name: "bare address without prefix", used: []string{"10.126.126.7"}, want: "10.126.126.8"},
		{name: "fills a hole", used: []string{"10.126.126.1/24", "10.126.126.3/24"}, want: "10.126.126.2"},
		{
			name: "ignores unparsable entries",
			used: []string{"", "   ", "not-an-ip", "10.126.126.1/24"},
			want: "10.126.126.2",
		},
		{
			name: "stays inside the lowest address subnet",
			used: []string{"10.126.126.5/24", "10.9.9.9/24"},
			want: "10.9.9.10",
		},
		{name: "no room left below the broadcast address", used: fullSlash24(), err: ErrAddressExhausted},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := AllocateIPv4(tc.used)
			if tc.err != nil {
				if !errors.Is(err, tc.err) {
					t.Fatalf("AllocateIPv4(%v) error = %v, want %v", tc.used, err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatalf("AllocateIPv4(%v) error = %v", tc.used, err)
			}
			if got != tc.want {
				t.Fatalf("AllocateIPv4(%v) = %q, want %q", tc.used, got, tc.want)
			}
		})
	}
}

// fullSlash24 覆盖 10.126.126.1 到 .254，一个空位都不剩。
func fullSlash24() []string {
	used := make([]string, 0, 254)
	for last := 1; last <= 254; last++ {
		used = append(used, "10.126.126."+strconv.Itoa(last))
	}
	return used
}

func TestExpandTemplate(t *testing.T) {
	if got := expandTemplate(`ipv4 = "{{ IP }}/24"`, "10.126.126.4"); got != `ipv4 = "10.126.126.4/24"` {
		t.Fatalf("expandTemplate() = %q", got)
	}
	if got := expandTemplate("a={{IP}} b={{  IP  }}", "1.2.3.4"); got != "a=1.2.3.4 b=1.2.3.4" {
		t.Fatalf("expandTemplate() with spacing = %q", got)
	}
	if got := expandTemplate("no placeholder", "1.2.3.4"); got != "no placeholder" {
		t.Fatalf("expandTemplate() without placeholder = %q", got)
	}
	if got := expandTemplate("{{ OTHER }}", "1.2.3.4"); got != "{{ OTHER }}" {
		t.Fatalf("expandTemplate() must keep unknown placeholders, got %q", got)
	}
}

func TestPlaceholderScanning(t *testing.T) {
	if hasIPPlaceholder("ipv4 = \"10.126.126.1/24\"") {
		t.Fatal("hasIPPlaceholder() = true for a literal address")
	}
	if !hasIPPlaceholder("ipv4 = \"{{ IP }}/24\"") {
		t.Fatal("hasIPPlaceholder() = false for {{ IP }}")
	}
	if !hasIPPlaceholder("ipv4 = \"{{IP}}/24\"") {
		t.Fatal("hasIPPlaceholder() must tolerate missing spaces")
	}
	if got := firstUnknownPlaceholder("ipv4 = \"{{ IP }}/24\""); got != "" {
		t.Fatalf("firstUnknownPlaceholder() = %q, want empty", got)
	}
	if got := firstUnknownPlaceholder("{{ IP }} and {{ HOST }}"); got != "{{ HOST }}" {
		t.Fatalf("firstUnknownPlaceholder() = %q, want {{ HOST }}", got)
	}
	if got := firstUnknownPlaceholder("{{ IP }"); got != "{{ IP }" {
		t.Fatalf("firstUnknownPlaceholder() on unterminated = %q", got)
	}
}

func TestCreateDeviceInjectsAddress(t *testing.T) {
	a := openTemp(t)
	if _, _, err := a.CreateDevice("seed", "ipv4 = \"10.126.126.1/24\"\n"); err != nil {
		t.Fatalf("seed CreateDevice() error: %v", err)
	}
	device, _, err := a.CreateDevice("laptop", "ipv4 = \"{{ IP }}/24\"\n")
	if err != nil {
		t.Fatalf("CreateDevice() error: %v", err)
	}
	if got := ConfigFactsOf(device.ConfigTOML).IPv4; got != "10.126.126.2/24" {
		t.Fatalf("injected ipv4 = %q, want 10.126.126.2/24", got)
	}
	second, _, err := a.CreateDevice("phone", "ipv4 = \"{{ IP }}/24\"\n")
	if err != nil {
		t.Fatalf("second CreateDevice() error: %v", err)
	}
	if got := ConfigFactsOf(second.ConfigTOML).IPv4; got != "10.126.126.3/24" {
		t.Fatalf("second injected ipv4 = %q, want 10.126.126.3/24", got)
	}
}

func TestCreateDeviceNeedsAReferenceAddress(t *testing.T) {
	a := openTemp(t)
	if _, _, err := a.CreateDevice("laptop", "ipv4 = \"{{ IP }}/24\"\n"); !errors.Is(err, ErrNoAddressPool) {
		t.Fatalf("CreateDevice() on an empty console = %v, want ErrNoAddressPool", err)
	}
}

func TestCreateDeviceRejectsUnknownPlaceholder(t *testing.T) {
	a := openTemp(t)
	_, _, err := a.CreateDevice("laptop", "ipv4 = \"10.126.126.5/24\"\nhostname = \"{{ HOST }}\"\n")
	if !errors.Is(err, ErrUnknownPlaceholder) {
		t.Fatalf("CreateDevice() = %v, want ErrUnknownPlaceholder", err)
	}
}

func TestUpdateDeviceKeepsInjectedAddress(t *testing.T) {
	a := openTemp(t)
	if _, _, err := a.CreateDevice("seed", "ipv4 = \"10.126.126.1/24\"\n"); err != nil {
		t.Fatal(err)
	}
	device, _, err := a.CreateDevice("laptop", "ipv4 = \"{{ IP }}/24\"\n")
	if err != nil {
		t.Fatal(err)
	}
	updated, err := a.UpdateDevice(device.PublicID, "laptop", "ipv4 = \"{{ IP }}/24\"\nhostname = \"auto\"\n")
	if err != nil {
		t.Fatalf("UpdateDevice() error: %v", err)
	}
	if got := ConfigFactsOf(updated.ConfigTOML).IPv4; got != "10.126.126.2/24" {
		t.Fatalf("re-injected ipv4 = %q, want the unchanged 10.126.126.2/24", got)
	}
}

func TestUpdateDeviceInjectsWhenAddressWasMissing(t *testing.T) {
	a := openTemp(t)
	if _, _, err := a.CreateDevice("seed", "ipv4 = \"10.126.126.1/24\"\n"); err != nil {
		t.Fatal(err)
	}
	device, _, err := a.CreateDevice("laptop", "hostname = \"auto\"\n")
	if err != nil {
		t.Fatal(err)
	}
	updated, err := a.UpdateDevice(device.PublicID, "laptop", "ipv4 = \"{{ IP }}/24\"\n")
	if err != nil {
		t.Fatalf("UpdateDevice() error: %v", err)
	}
	if got := ConfigFactsOf(updated.ConfigTOML).IPv4; got != "10.126.126.2/24" {
		t.Fatalf("ipv4 = %q, want 10.126.126.2/24", got)
	}
}
