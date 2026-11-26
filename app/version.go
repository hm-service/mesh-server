package app

import (
	"cmp"
	"regexp"
	"strconv"
)

// appVersionPattern is the only client build label: YYYY.MM.DD-N, the build
// date and that day's sequence number (client/build/version.ps1).
var appVersionPattern = regexp.MustCompile(`^(\d{4})\.(\d{2})\.(\d{2})-(\d+)$`)

// buildVersion is one parsed build label: the date as YYYYMMDD (numeric order
// is calendar order) and the sequence number of that day.
type buildVersion struct {
	date int
	seq  int
}

func parseAppVersion(s string) (buildVersion, bool) {
	m := appVersionPattern.FindStringSubmatch(s)
	if m == nil {
		return buildVersion{}, false
	}
	date, err := strconv.Atoi(m[1] + m[2] + m[3])
	if err != nil {
		return buildVersion{}, false
	}
	seq, err := strconv.Atoi(m[4])
	if err != nil {
		return buildVersion{}, false
	}
	return buildVersion{date: date, seq: seq}, true
}

// ValidAppVersion reports whether s is a build label the control plane can
// order. Both the published version and the one a device reports come from the
// same build, so anything else is a protocol violation.
func ValidAppVersion(s string) bool {
	_, ok := parseAppVersion(s)
	return ok
}

// CompareVersions orders two YYYY.MM.DD-N labels by build date and then by that
// day's sequence number. String order gets the sequence wrong (-9 sorts after
// -11), which would turn a newer device into an endless downgrade. ok is false
// when a label does not have that form.
func CompareVersions(a, b string) (order int, ok bool) {
	left, ok := parseAppVersion(a)
	if !ok {
		return 0, false
	}
	right, ok := parseAppVersion(b)
	if !ok {
		return 0, false
	}
	if left.date != right.date {
		return cmp.Compare(left.date, right.date), true
	}
	return cmp.Compare(left.seq, right.seq), true
}

// NeedsUpdate reports whether a device running reported must be pointed at
// published. Only a reported version that is provably older counts: a device
// at or above the published build (a rolled-back release, a hand-installed
// package) is left alone, so auto_update cannot loop on the same old package.
// A device that has never reported a version counts as behind: it is an older
// client that predates the field, and it reports its version right after
// installing, so this cannot loop. A reported label that cannot be ordered is
// not evidence of an older build and is left alone (contract/updates.md).
func NeedsUpdate(reported, published string) bool {
	if published == "" {
		return false
	}
	if reported == "" {
		return true
	}
	order, ok := CompareVersions(reported, published)
	return ok && order < 0
}
