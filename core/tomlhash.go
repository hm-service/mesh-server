package core

import (
	"crypto/sha256"
	"encoding/hex"
)

// HashLen is the length of config_hash: the first 16 hex characters of the
// sha256 digest, the same length app_hash uses (CONTRACT.md 1.2).
const HashLen = 16

// Hash returns the first HashLen hex characters of sha256(toml).
// It hashes the exact bytes that are stored and delivered, so a client that
// digests its local config.toml reaches the same value without reimplementing
// any text normalization.
func Hash(toml string) string {
	return FullHash(toml)[:HashLen]
}

// FullHash returns the complete sha256 hex digest of the text.
func FullHash(toml string) string {
	sum := sha256.Sum256([]byte(toml))
	return hex.EncodeToString(sum[:])
}
