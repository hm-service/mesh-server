package core

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ClockSkew is the accepted distance between the signed timestamp and server time.
const ClockSkew = 5 * time.Minute

var (
	ErrTimestamp = errors.New("timestamp rejected")
	ErrSignature = errors.New("signature rejected")
)

// SigningString builds the exact text a device signs (CONTRACT.md 1.3).
//
// The fields are joined in a fixed order instead of hashing the JSON body: the
// heartbeat carries its own signature, so any body-digest scheme would have to
// strip and re-serialise it on both sides and stay in lockstep forever.
func SigningString(method, path, deviceID, timestamp, nonce, platformID, configHash, appHash string) string {
	return strings.Join([]string{
		"MESH2", method, path, deviceID, timestamp, nonce, platformID, configHash, appHash,
	}, "\n")
}

// CheckTimestamp rejects timestamps outside the allowed window.
func CheckTimestamp(raw string, now time.Time) error {
	ts, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return fmt.Errorf("%w: not an integer", ErrTimestamp)
	}
	skew := now.Unix() - ts
	if skew < 0 {
		skew = -skew
	}
	if skew > int64(ClockSkew/time.Second) {
		return fmt.Errorf("%w: outside %s", ErrTimestamp, ClockSkew)
	}
	return nil
}

// CheckPubkey reports whether s is base64 of a 32-byte Ed25519 public key.
func CheckPubkey(s string) error {
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return fmt.Errorf("pubkey base64: %w", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return fmt.Errorf("pubkey length %d", len(raw))
	}
	return nil
}

// VerifySignature checks signatureB64 over msg with the given base64 pubkey.
func VerifySignature(pubkeyB64, signatureB64 string, msg []byte) error {
	pub, err := base64.StdEncoding.DecodeString(pubkeyB64)
	if err != nil {
		return fmt.Errorf("%w: pubkey base64", ErrSignature)
	}
	if len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("%w: pubkey length", ErrSignature)
	}
	sig, err := base64.StdEncoding.DecodeString(signatureB64)
	if err != nil {
		return fmt.Errorf("%w: signature base64", ErrSignature)
	}
	if len(sig) != ed25519.SignatureSize {
		return fmt.Errorf("%w: signature length", ErrSignature)
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), msg, sig) {
		return fmt.Errorf("%w: mismatch", ErrSignature)
	}
	return nil
}
