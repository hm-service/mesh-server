// Package core holds the pure logic of the control plane: token hashing,
// bearer credentials, nonces, request signing and the log file.
package core

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// RandomHex returns n random bytes encoded as lowercase hex.
func RandomHex(n int) (string, error) {
	buf, err := RandomBytes(n)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// RandomBytes returns n bytes read from the system entropy pool.
func RandomBytes(n int) ([]byte, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return nil, fmt.Errorf("read random: %w", err)
	}
	return buf, nil
}
