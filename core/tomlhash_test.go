package core

import (
	"strings"
	"testing"
)

// TestHashCoversExactBytes pins that the digest follows the stored text byte
// for byte: clients compute it from their local config.toml, so normalizing
// here would make the two values disagree.
func TestHashCoversExactBytes(t *testing.T) {
	plain := "hostname = \"auto\"\n"
	if Hash(plain) == Hash("hostname = \"auto\"\r\n") {
		t.Fatal("Hash() ignored a line-ending change")
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
