package core

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode"
)

// Normalize applies the contract normalization steps in order: drop a UTF-8 BOM,
// turn CRLF/CR into LF, strip trailing whitespace per line, drop trailing empty
// lines and append exactly one final newline.
func Normalize(toml string) string {
	s := strings.TrimPrefix(toml, "\ufeff")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRightFunc(line, unicode.IsSpace)
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n") + "\n"
}

// HashLen is the length of config_hash: the first 16 hex characters of the
// sha256 digest, the same length app_hash uses (CONTRACT.md 1.2).
const HashLen = 16

// Hash returns the first HashLen hex characters of sha256(normalize(toml)).
func Hash(toml string) string {
	return FullHash(toml)[:HashLen]
}

// FullHash returns the complete sha256 hex digest of the normalized text.
func FullHash(toml string) string {
	sum := sha256.Sum256([]byte(Normalize(toml)))
	return hex.EncodeToString(sum[:])
}
