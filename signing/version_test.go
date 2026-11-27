package signing

import (
	"strings"
	"testing"
)

// TestParseVersion pins the only release version shape. Each illegal case names
// the rule it breaks so a failure says which part of the domain moved.
func TestParseVersion(t *testing.T) {
	legal := []struct {
		name string
		in   string
		want Version
	}{
		{name: "a plain build", in: "2026.11.27-117", want: Version{2026, 11, 27, 117}},
		{name: "first build of the day", in: "2026.11.27-1", want: Version{2026, 11, 27, 1}},
		{name: "lowest year", in: "2000.01.01-1", want: Version{2000, 1, 1, 1}},
		{name: "highest year", in: "2999.12.31-9999", want: Version{2999, 12, 31, 9999}},
		{name: "four digit sequence", in: "2026.11.27-1000", want: Version{2026, 11, 27, 1000}},
	}
	for _, c := range legal {
		t.Run(c.name, func(t *testing.T) {
			got, ok := ParseVersion(c.in)
			if !ok || got != c.want {
				t.Fatalf("ParseVersion(%q) = %+v, %v; want %+v, true", c.in, got, ok, c.want)
			}
			if !ValidVersion(c.in) {
				t.Fatalf("ValidVersion(%q) = false", c.in)
			}
		})
	}

	illegal := []struct {
		name string
		in   string
	}{
		{name: "empty", in: ""},
		{name: "single digit month and day", in: "2026.1.1-1"},
		{name: "leading zero in the sequence", in: "2026.11.26-013"},
		{name: "v prefix", in: "v2026.11.26-1"},
		{name: "release candidate suffix", in: "2026.11.26-1-rc1"},
		{name: "zero sequence", in: "2026.11.26-0"},
		{name: "sequence above 9999", in: "2026.11.26-10000"},
		{name: "missing sequence", in: "2026.11.26-"},
		{name: "missing day", in: "2026.11-1"},
		{name: "dashes instead of dots", in: "2026-11-26-1"},
		{name: "year below 2000", in: "1999.12.31-1"},
		{name: "year above 2999", in: "3000.01.01-1"},
		{name: "month zero", in: "2026.00.01-1"},
		{name: "month above 12", in: "2026.13.01-1"},
		{name: "day zero", in: "2026.11.00-1"},
		{name: "day above 31", in: "2026.11.32-1"},
		{name: "trailing space", in: "2026.11.26-1 "},
		{name: "signed sequence", in: "2026.11.26-+1"},
		{name: "non-digit sequence", in: "2026.11.26-x"},
	}
	for _, c := range illegal {
		t.Run(c.name, func(t *testing.T) {
			if got, ok := ParseVersion(c.in); ok {
				t.Fatalf("ParseVersion(%q) = %+v, true; want false", c.in, got)
			}
			if ValidVersion(c.in) {
				t.Fatalf("ValidVersion(%q) = true", c.in)
			}
		})
	}
}

// TestCompare pins the order: the (year, month, day, sequence) tuple, compared
// numerically. String order would put -9 after -11 and make a newer build look
// older; a label outside the domain has no order at all.
func TestCompare(t *testing.T) {
	cases := []struct {
		name string
		a    string
		b    string
		want int
	}{
		{name: "same build", a: "2026.11.26-4", b: "2026.11.26-4", want: 0},
		{name: "sequence is numeric", a: "2026.11.26-9", b: "2026.11.26-11", want: -1},
		{name: "sequence is numeric, reversed", a: "2026.11.26-11", b: "2026.11.26-9", want: 1},
		{name: "sequence grows past 99", a: "2026.11.26-99", b: "2026.11.26-100", want: -1},
		{name: "date wins over sequence", a: "2026.11.25-99", b: "2026.11.26-1", want: -1},
		{name: "month boundary", a: "2026.10.31-99", b: "2026.11.01-1", want: -1},
		{name: "year boundary", a: "2099.12.31-7", b: "2100.01.01-1", want: -1},
		{name: "month digits", a: "2026.09.30-1", b: "2026.10.01-1", want: -1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			order, ok := Compare(c.a, c.b)
			if !ok || order != c.want {
				t.Fatalf("Compare(%q, %q) = %d, %v; want %d, true", c.a, c.b, order, ok, c.want)
			}
		})
	}

	if _, ok := Compare("2026.11.26-1", "2026.11.26-013"); ok {
		t.Fatal("Compare ordered a label outside the domain")
	}
	if _, ok := Compare("v2026.11.26-1", "2026.11.26-1"); ok {
		t.Fatal("Compare ordered a label outside the domain")
	}
}

// TestValidPlatform pins the one platform shape the publish and signing paths
// share: one slash, a lower-case alphanumeric segment on each side, at most 32
// bytes in total.
func TestValidPlatform(t *testing.T) {
	for _, good := range []string{"windows/amd64", "windows/arm64", "linux/amd64", "linux/arm64", "a/0"} {
		if !ValidPlatform(good) {
			t.Fatalf("ValidPlatform(%q) = false", good)
		}
	}
	for name, bad := range map[string]string{
		"empty":        "",
		"no slash":     "windows",
		"two slashes":  "windows/amd64/extra",
		"missing os":   "/amd64",
		"missing arch": "windows/",
		"uppercase":    "Windows/amd64",
		"space":        "windows amd64",
		"dash":         "windows-x/amd64",
		"too long":     strings.Repeat("a", 31) + "/a",
	} {
		t.Run(name, func(t *testing.T) {
			if ValidPlatform(bad) {
				t.Fatalf("ValidPlatform(%q) = true", bad)
			}
		})
	}
}

// TestValidFileName pins the ASCII byte rule: no separators, no control bytes,
// at most 128 bytes, and not the manifest itself. Non-ASCII bytes are allowed.
func TestValidFileName(t *testing.T) {
	if !validFileName(strings.Repeat("a", 128)) {
		t.Fatal("a 128-byte name was rejected")
	}
	for _, good := range []string{"mesh-client.exe", "easytier-core", "配置.json"} {
		if !validFileName(good) {
			t.Fatalf("validFileName(%q) = false", good)
		}
	}
	for name, bad := range map[string]string{
		"empty":          "",
		"dot":            ".",
		"dot dot":        "..",
		"manifest":       "version.json",
		"slash":          "sub/a.exe",
		"backslash":      `sub\a.exe`,
		"nul byte":       "a\x00b",
		"unit separator": "a\x1fb",
		"delete":         "a\x7fb",
		"too long":       strings.Repeat("a", 129),
	} {
		t.Run(name, func(t *testing.T) {
			if validFileName(bad) {
				t.Fatalf("validFileName(%q) = true", bad)
			}
		})
	}
}

// TestRequiredFiles pins the mapping from slot+platform to the binaries a
// manifest must list; the client manifest carries the kernel beside the
// client, the launcher manifest only the launcher.
func TestRequiredFiles(t *testing.T) {
	cases := []struct {
		name     string
		slot     string
		platform string
		want     []string
	}{
		{name: "windows client", slot: SlotClient, platform: "windows/amd64", want: []string{"mesh-client.exe", "easytier-core.exe"}},
		{name: "windows arm64 client", slot: SlotClient, platform: "windows/arm64", want: []string{"mesh-client.exe", "easytier-core.exe"}},
		{name: "linux client", slot: SlotClient, platform: "linux/amd64", want: []string{"mesh-client", "easytier-core"}},
		{name: "linux arm64 client", slot: SlotClient, platform: "linux/arm64", want: []string{"mesh-client", "easytier-core"}},
		{name: "unknown client platform", slot: SlotClient, platform: "darwin/arm64", want: nil},
		{name: "windows launcher", slot: SlotLauncher, platform: "windows/arm64", want: []string{"mesh.exe"}},
		{name: "linux launcher", slot: SlotLauncher, platform: "linux/amd64", want: []string{"mesh"}},
		{name: "unknown launcher platform", slot: SlotLauncher, platform: "darwin/amd64", want: nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := requiredFiles(c.slot, c.platform)
			if strings.Join(got, ",") != strings.Join(c.want, ",") {
				t.Fatalf("requiredFiles(%q, %q) = %v, want %v", c.slot, c.platform, got, c.want)
			}
		})
	}
}
