// Package core holds the pure logic of the control plane: hashing, sessions,
// nonces, request signing and the log file.
package core

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// RandomHex returns n random bytes encoded as lowercase hex.
func RandomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("read random: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
