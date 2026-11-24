package core

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strconv"
	"strings"
	"time"
)

const (
	// bearerPrefix domain-separates these signatures from anything else the
	// session key may sign in the future.
	bearerPrefix = "v1|"
	// NonceBytes is the random part of one credential.
	NonceBytes = 16
	// SessionKeyBytes is the size of a generated signing key.
	SessionKeyBytes = 32
)

// SignBearer issues <base64url(exp|nonce)>.<base64url(HMAC-SHA256(key, "v1|" + payload))>.
// The credential is stateless: revoking every session means rotating the key.
func SignBearer(key []byte, expires time.Time, nonce []byte) string {
	payload := strconv.FormatInt(expires.Unix(), 10) + "|" + base64.RawURLEncoding.EncodeToString(nonce)
	encoded := base64.RawURLEncoding.EncodeToString([]byte(payload))
	return encoded + "." + base64.RawURLEncoding.EncodeToString(signBearer(key, encoded))
}

// VerifyBearer checks the signature first and the expiry second, so a forged
// credential is rejected before its expiry is even looked at.
func VerifyBearer(key []byte, token string, now time.Time) bool {
	encoded, macPart, ok := strings.Cut(token, ".")
	if !ok || encoded == "" || macPart == "" {
		return false
	}
	got, err := base64.RawURLEncoding.DecodeString(macPart)
	if err != nil || !hmac.Equal(got, signBearer(key, encoded)) {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return false
	}
	expPart, _, ok := strings.Cut(string(raw), "|")
	if !ok {
		return false
	}
	exp, err := strconv.ParseInt(expPart, 10, 64)
	if err != nil {
		return false
	}
	return now.Before(time.Unix(exp, 0))
}

func signBearer(key []byte, encoded string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(bearerPrefix + encoded))
	return mac.Sum(nil)
}
