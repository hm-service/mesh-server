package app

import "testing"

// TestValidAppVersion pins the domain the control plane shares with the release
// tool: YYYY.MM.DD-N with a four-digit year in 2000..2999, two-digit month and
// day, and a one-to-four-digit sequence without a leading zero.
func TestValidAppVersion(t *testing.T) {
	for _, good := range []string{
		"2026.11.27-117",
		"2026.11.27-1",
		"2000.01.01-1",
		"2999.12.31-9999",
	} {
		t.Run("accepts "+good, func(t *testing.T) {
			if !ValidAppVersion(good) {
				t.Fatalf("ValidAppVersion(%q) = false", good)
			}
		})
	}
	for name, bad := range map[string]string{
		"empty":                      "",
		"single digit month and day": "2026.1.1-1",
		"leading zero sequence":      "2026.11.26-013",
		"v prefix":                   "v2026.11.26-1",
		"release candidate":          "2026.11.26-1-rc1",
		"zero sequence":              "2026.11.26-0",
		"sequence above 9999":        "2026.11.26-10000",
		"missing sequence":           "2026.11.26-",
		"year below 2000":            "1999.12.31-1",
		"year above 2999":            "3000.01.01-1",
		"month above 12":             "2026.13.01-1",
		"day above 31":               "2026.11.32-1",
	} {
		t.Run(name, func(t *testing.T) {
			if ValidAppVersion(bad) {
				t.Fatalf("ValidAppVersion(%q) = true", bad)
			}
			if _, ok := CompareVersions(bad, "2026.11.26-1"); ok {
				t.Fatalf("CompareVersions ordered %q", bad)
			}
		})
	}
}

// TestCompareVersions pins the order: build date first, then that day's
// sequence number. The sequence must be compared as a number, because string
// order puts "-9" after "-11" and would make a newer device look outdated.
func TestCompareVersions(t *testing.T) {
	cases := []struct {
		name      string
		a, b      string
		wantOrder int
	}{
		{name: "same build", a: "2026.11.26-4", b: "2026.11.26-4", wantOrder: 0},
		{name: "sequence is numeric", a: "2026.11.26-9", b: "2026.11.26-11", wantOrder: -1},
		{name: "sequence is numeric, reversed", a: "2026.11.26-11", b: "2026.11.26-9", wantOrder: 1},
		{name: "many digits", a: "2026.11.26-99", b: "2026.11.26-100", wantOrder: -1},
		{name: "date wins over sequence", a: "2026.11.25-99", b: "2026.11.26-1", wantOrder: -1},
		{name: "month boundary", a: "2026.10.31-99", b: "2026.11.01-1", wantOrder: -1},
		{name: "year boundary", a: "2099.12.31-7", b: "2100.01.01-1", wantOrder: -1},
		{name: "month digits", a: "2026.09.30-1", b: "2026.10.01-1", wantOrder: -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			order, ok := CompareVersions(tc.a, tc.b)
			if !ok || order != tc.wantOrder {
				t.Fatalf("CompareVersions(%q, %q) = %d, %v; want %d, true", tc.a, tc.b, order, ok, tc.wantOrder)
			}
		})
	}
}

// TestNeedsUpdate pins when the control plane points a device at the published
// build: only a reported version that is provably older. A newer device (a
// rolled-back release, a hand-installed package) stays current, so auto_update
// cannot loop on the same old package.
func TestNeedsUpdate(t *testing.T) {
	cases := []struct {
		name      string
		reported  string
		published string
		want      bool
	}{
		{name: "older", reported: "2026.11.26-9", published: "2026.11.26-11", want: true},
		{name: "same", reported: "2026.11.26-11", published: "2026.11.26-11", want: false},
		{name: "newer", reported: "2026.11.26-12", published: "2026.11.26-11", want: false},
		{name: "newer day", reported: "2026.11.27-1", published: "2026.11.26-99", want: false},
		{name: "nothing published", reported: "2026.11.26-9", published: "", want: false},
		{name: "never reported", reported: "", published: "2026.11.26-11", want: true},
		{name: "unorderable report", reported: "dev-build", published: "2026.11.26-11", want: false},
		{name: "leading zero report", reported: "2026.11.26-013", published: "2026.11.26-11", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NeedsUpdate(tc.reported, tc.published); got != tc.want {
				t.Fatalf("NeedsUpdate(%q, %q) = %v, want %v", tc.reported, tc.published, got, tc.want)
			}
		})
	}
}

// TestEffectiveAutoUpdate pins the three states: the device override wins when it
// is set, and nil follows the package. The client only ever sees the result.
func TestEffectiveAutoUpdate(t *testing.T) {
	always := true
	manual := false
	cases := []struct {
		name    string
		device  *bool
		release bool
		want    bool
	}{
		{name: "follows an automatic package", device: nil, release: true, want: true},
		{name: "follows a manual package", device: nil, release: false, want: false},
		{name: "forces automatic over a manual package", device: &always, release: false, want: true},
		{name: "forces manual over an automatic package", device: &manual, release: true, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := EffectiveAutoUpdate(tc.device, tc.release); got != tc.want {
				t.Fatalf("EffectiveAutoUpdate(%v, %v) = %v, want %v", tc.device, tc.release, got, tc.want)
			}
		})
	}
}
