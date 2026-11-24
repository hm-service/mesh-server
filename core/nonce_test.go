package core

import (
	"strconv"
	"testing"
	"time"
)

// TestNonceOutlivesAFutureTimestamp pins the replay window. A signature stamped
// ClockSkew ahead of the server is accepted on arrival, so the nonce has to stay
// remembered until that signature can no longer be accepted; a TTL measured only
// from arrival would expire it 300 seconds too early.
func TestNonceOutlivesAFutureTimestamp(t *testing.T) {
	base := time.Unix(1_732_400_000, 0)
	signedAt := base.Add(ClockSkew)
	stamp := strconv.FormatInt(signedAt.Unix(), 10)

	cache := NewNonceCache(ClockSkew)
	if !cache.Claim("nonce", base, signedAt) {
		t.Fatal("the first claim was rejected")
	}
	// One second after the entry would have expired under a naive TTL, and
	// still inside [signedAt-ClockSkew, signedAt+ClockSkew].
	replay := base.Add(ClockSkew + time.Second)
	if _, err := CheckTimestamp(stamp, replay); err != nil {
		t.Fatalf("the replay is outside its own window: %v", err)
	}
	if cache.Claim("nonce", replay, signedAt) {
		t.Fatal("the replayed nonce was accepted")
	}
}

// TestNonceExpiresAfterItsWindow keeps the cache bounded: once the signature can
// no longer be accepted the nonce is forgotten.
func TestNonceExpiresAfterItsWindow(t *testing.T) {
	base := time.Unix(1_732_400_000, 0)
	cache := NewNonceCache(ClockSkew)
	if !cache.Claim("nonce", base, base) {
		t.Fatal("the first claim was rejected")
	}
	late := base.Add(2*ClockSkew + time.Second)
	if !cache.Claim("nonce", late, late) {
		t.Fatal("the nonce was still remembered after its window")
	}
}
