package core

import (
	"strings"
	"testing"
)

func TestNormalize(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain", in: "a = 1", want: "a = 1\n"},
		{name: "bom", in: "\ufeffa = 1\n", want: "a = 1\n"},
		{name: "crlf", in: "a = 1\r\nb = 2\r\n", want: "a = 1\nb = 2\n"},
		{name: "cr", in: "a = 1\rb = 2", want: "a = 1\nb = 2\n"},
		{name: "trailing spaces", in: "a = 1   \n", want: "a = 1\n"},
		{name: "trailing tab", in: "a = 1\t\n", want: "a = 1\n"},
		{name: "trailing blank lines", in: "a = 1\n\n\n", want: "a = 1\n"},
		{name: "inner blank line kept", in: "a = 1\n\nb = 2\n", want: "a = 1\n\nb = 2\n"},
		{name: "empty", in: "", want: "\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Normalize(tc.in); got != tc.want {
				t.Fatalf("Normalize(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestHashIgnoresFormattingDifferences(t *testing.T) {
	formatted := "\ufeffhostname = \"auto\"  \r\n\r\n"
	plain := "hostname = \"auto\"\n"
	if Hash(formatted) != Hash(plain) {
		t.Fatalf("Hash() differs for equivalent input: %s vs %s", Hash(formatted), Hash(plain))
	}
	if Hash(plain) == Hash("hostname = \"other\"\n") {
		t.Fatal("Hash() ignored a content change")
	}
	if got := Hash(plain); len(got) != HashLen || strings.Trim(got, "0123456789abcdef") != "" {
		t.Fatalf("Hash() = %q, want %d hex characters", got, HashLen)
	}
}

// TestFullHashExtendsTheShortHash pins the relationship between the field
// clients compare and the digest they verify a download against.
func TestFullHashExtendsTheShortHash(t *testing.T) {
	full := FullHash("hostname = \"auto\"\n")
	if len(full) != 64 {
		t.Fatalf("FullHash() = %q, want 64 hex characters", full)
	}
	if Hash("hostname = \"auto\"\n") != full[:HashLen] {
		t.Fatal("Hash() is not the prefix of FullHash()")
	}
}
