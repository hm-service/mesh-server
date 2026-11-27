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

// TestNonceCacheRejectsWhenFull pins the saturation policy: a full cache refuses
// a new nonce instead of evicting one that is still inside its window — evicting
// would let a used signature in again. Capacity comes back once the window has
// passed and the sweep drops the expired entries.
func TestNonceCacheRejectsWhenFull(t *testing.T) {
	base := time.Unix(1_732_400_000, 0)
	// 直接构造满载的小缓存：容量在测试里不该只有一条 8192 次循环的路。
	cache := &NonceCache{ttl: ClockSkew, max: 2, seen: make(map[string]time.Time)}
	if !cache.Claim("a", base, base) || !cache.Claim("b", base, base) {
		t.Fatal("the cache rejected a claim below its capacity")
	}
	if cache.Claim("c", base, base) {
		t.Fatal("a claim over the capacity was accepted")
	}
	if cache.Claim("a", base, base) {
		t.Fatal("a nonce already inside its window was accepted again")
	}
	late := base.Add(2*ClockSkew + time.Second)
	if !cache.Claim("c", late, late) {
		t.Fatal("the capacity did not come back after the window")
	}
}

// TestNonceCacheCapacityIsBounded keeps the default capacity honest: the shared
// map must not grow past NonceCacheEntries.
func TestNonceCacheCapacityIsBounded(t *testing.T) {
	base := time.Unix(1_732_400_000, 0)
	cache := NewNonceCache(ClockSkew)
	for i := range NonceCacheEntries {
		if !cache.Claim(strconv.Itoa(i), base, base) {
			t.Fatalf("claim %d was rejected below the capacity", i)
		}
	}
	if cache.Claim("one-too-many", base, base) {
		t.Fatal("the cache accepted more than NonceCacheEntries")
	}
}
