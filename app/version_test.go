package app

import "testing"

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
		{name: "year boundary", a: "2025.12.31-7", b: "2026.01.01-1", wantOrder: -1},
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

	for _, bad := range []string{"", "v1", "2026.11.26", "2026.11.26-", "2026.11.26-x", "2026.11.26-1-2", "2026.11.26-01a"} {
		if !ValidAppVersion(bad) {
			continue
		}
		t.Fatalf("ValidAppVersion(%q) = true, want false", bad)
	}
	if _, ok := CompareVersions("2026.11.26-1", "v2"); ok {
		t.Fatal("CompareVersions accepted a label that is not a build version")
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
