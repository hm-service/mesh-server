package core

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"

	"golang.org/x/crypto/scrypt"
)

const (
	scryptN     = 1 << 15
	scryptR     = 8
	scryptP     = 1
	passwordKey = 32
	saltBytes   = 16
	// hashGate caps concurrent scrypt runs. Each one needs about 32 MiB, so the
	// gate keeps the whole process inside the memory budget.
	hashGate = 4
)

var hashSlots = make(chan struct{}, hashGate)

// HashPassword derives a scrypt hash; the salt and hash are returned as hex.
func HashPassword(password string) (saltHex, hashHex string, err error) {
	salt := make([]byte, saltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", "", fmt.Errorf("read salt: %w", err)
	}
	key, err := derive(password, salt)
	if err != nil {
		return "", "", err
	}
	return hex.EncodeToString(salt), hex.EncodeToString(key), nil
}

// VerifyPassword reports whether password matches the stored salt and hash.
func VerifyPassword(password, saltHex, hashHex string) bool {
	salt, err := hex.DecodeString(saltHex)
	if err != nil {
		return false
	}
	want, err := hex.DecodeString(hashHex)
	if err != nil || len(want) != passwordKey {
		return false
	}
	got, err := derive(password, salt)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}

func derive(password string, salt []byte) ([]byte, error) {
	hashSlots <- struct{}{}
	defer func() { <-hashSlots }()
	key, err := scrypt.Key([]byte(password), salt, scryptN, scryptR, scryptP, passwordKey)
	if err != nil {
		return nil, fmt.Errorf("derive password: %w", err)
	}
	return key, nil
}
