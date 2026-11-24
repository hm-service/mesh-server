package core

import (
	"crypto/rand"
	"crypto/subtle"
	"fmt"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters come from the skeleton conventions. Each derivation
// needs about 19 MiB, so argonGate keeps four of them in flight at most.
const (
	argonMemory  = 19 * 1024 // KiB
	argonTime    = 2
	argonThreads = 1
	argonKeyLen  = 32
	argonSaltLen = 16
	argonGate    = 4
)

var argonSlots = make(chan struct{}, argonGate)

// TokenHash is the in-memory hash of AUTH_TOKEN; the raw token is dropped
// right after startup and never lives in the process.
type TokenHash struct {
	salt []byte
	hash []byte
}

// NewTokenHash derives the hash of token with a fresh random salt.
func NewTokenHash(token string) (*TokenHash, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("read salt: %w", err)
	}
	return &TokenHash{salt: salt, hash: deriveToken(token, salt)}, nil
}

// Matches reports whether candidate is the configured token.
func (t *TokenHash) Matches(candidate string) bool {
	if t == nil || len(t.hash) == 0 {
		return false
	}
	return subtle.ConstantTimeCompare(deriveToken(candidate, t.salt), t.hash) == 1
}

func deriveToken(token string, salt []byte) []byte {
	argonSlots <- struct{}{}
	defer func() { <-argonSlots }()
	return argon2.IDKey([]byte(token), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
}
