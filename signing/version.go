package signing

import "cmp"

// Version is a release version: the build date and that day's sequence number.
// The client package and the launcher slot share the label, and it is the only
// label the control plane can order.
type Version struct {
	Year  int
	Month int
	Day   int
	Seq   int
}

// ParseVersion parses the only release version shape:
//
//	YYYY.MM.DD-N
//
// YYYY is exactly four digits in 2000..2999, MM is 01..12, DD is 01..31, and N
// is one to four decimal digits with no leading zero (1..9999). Any other shape
// is not a version: 2026.1.1-1, 2026.11.26-013, v2026.11.26-1 and
// 2026.11.26-1-rc1 are all rejected, and none of them can be ordered.
func ParseVersion(s string) (Version, bool) {
	// "YYYY.MM.DD-" is 11 bytes; N adds one to four.
	if len(s) < 12 || len(s) > 15 {
		return Version{}, false
	}
	if s[4] != '.' || s[7] != '.' || s[10] != '-' {
		return Version{}, false
	}
	year, ok := decimal(s[0:4])
	if !ok || year < 2000 || year > 2999 {
		return Version{}, false
	}
	month, ok := decimal(s[5:7])
	if !ok || month < 1 || month > 12 {
		return Version{}, false
	}
	day, ok := decimal(s[8:10])
	if !ok || day < 1 || day > 31 {
		return Version{}, false
	}
	if s[11] == '0' {
		return Version{}, false
	}
	seq, ok := decimal(s[11:])
	if !ok || seq < 1 || seq > 9999 {
		return Version{}, false
	}
	return Version{Year: year, Month: month, Day: day, Seq: seq}, true
}

// ValidVersion reports whether s is a release version both ends can order.
func ValidVersion(s string) bool {
	_, ok := ParseVersion(s)
	return ok
}

// Compare orders two release versions by (year, month, day, sequence). String
// order gets the sequence wrong — "-9" sorts after "-11" — so the fields are
// compared as numbers. ok is false when either label is outside the domain; a
// label that cannot be ordered is not evidence of an older build.
func Compare(a, b string) (order int, ok bool) {
	left, ok := ParseVersion(a)
	if !ok {
		return 0, false
	}
	right, ok := ParseVersion(b)
	if !ok {
		return 0, false
	}
	switch {
	case left.Year != right.Year:
		return cmp.Compare(left.Year, right.Year), true
	case left.Month != right.Month:
		return cmp.Compare(left.Month, right.Month), true
	case left.Day != right.Day:
		return cmp.Compare(left.Day, right.Day), true
	default:
		return cmp.Compare(left.Seq, right.Seq), true
	}
}

// decimal parses a non-empty run of ASCII digits.
func decimal(s string) (int, bool) {
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		n = n*10 + int(s[i]-'0')
	}
	return n, true
}
