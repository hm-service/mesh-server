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
	if got := cache.Claim("nonce", base, signedAt); got != NonceAccepted {
		t.Fatalf("first claim = %v, want NonceAccepted", got)
	}
	// One second after the entry would have expired under a naive TTL, and
	// still inside [signedAt-ClockSkew, signedAt+ClockSkew].
	replay := base.Add(ClockSkew + time.Second)
	if _, err := CheckTimestamp(stamp, replay); err != nil {
		t.Fatalf("the replay is outside its own window: %v", err)
	}
	if got := cache.Claim("nonce", replay, signedAt); got != NonceReplay {
		t.Fatalf("replayed claim = %v, want NonceReplay", got)
	}
}

// TestNonceExpiresAfterItsWindow keeps the cache bounded: once the signature can
// no longer be accepted the nonce is forgotten.
func TestNonceExpiresAfterItsWindow(t *testing.T) {
	base := time.Unix(1_732_400_000, 0)
	cache := NewNonceCache(ClockSkew)
	if got := cache.Claim("nonce", base, base); got != NonceAccepted {
		t.Fatalf("first claim = %v, want NonceAccepted", got)
	}
	late := base.Add(2*ClockSkew + time.Second)
	if got := cache.Claim("nonce", late, late); got != NonceAccepted {
		t.Fatalf("claim after the window = %v, want the nonce to be forgotten", got)
	}
}

// TestNonceCacheReportsExhaustionWithoutEvicting pins the saturation policy: a
// full cache reports capacity exhaustion, not a replay, and refuses the new nonce
// instead of evicting one that is still inside its window — evicting would let a
// used signature in again. Capacity comes back once the window has passed and the
// sweep drops the expired entries.
func TestNonceCacheReportsExhaustionWithoutEvicting(t *testing.T) {
	base := time.Unix(1_732_400_000, 0)
	// 直接构造满载的小缓存：容量在测试里不该只有一条 8192 次循环的路。
	cache := &NonceCache{ttl: ClockSkew, max: 2, seen: make(map[string]time.Time)}
	for _, nonce := range []string{"a", "b"} {
		if got := cache.Claim(nonce, base, base); got != NonceAccepted {
			t.Fatalf("claim %q below the capacity = %v, want NonceAccepted", nonce, got)
		}
	}
	if got := cache.Claim("c", base, base); got != NonceExhausted {
		t.Fatalf("claim over the capacity = %v, want NonceExhausted", got)
	}
	// 饱和没有动过窗口内的条目：重放仍然是重放。
	if got := cache.Claim("a", base, base); got != NonceReplay {
		t.Fatalf("claim of a remembered nonce = %v, want NonceReplay", got)
	}
	late := base.Add(2*ClockSkew + time.Second)
	if got := cache.Claim("c", late, late); got != NonceAccepted {
		t.Fatalf("claim after the window = %v, want the capacity back", got)
	}
}

// TestNonceCacheCapacityIsBounded keeps the default capacity honest: the shared
// map must not grow past NonceCacheEntries, and the refusal says why.
func TestNonceCacheCapacityIsBounded(t *testing.T) {
	base := time.Unix(1_732_400_000, 0)
	cache := NewNonceCache(ClockSkew)
	for i := range NonceCacheEntries {
		if got := cache.Claim(strconv.Itoa(i), base, base); got != NonceAccepted {
			t.Fatalf("claim %d below the capacity = %v, want NonceAccepted", i, got)
		}
	}
	if got := cache.Claim("one-too-many", base, base); got != NonceExhausted {
		t.Fatalf("claim over the capacity = %v, want NonceExhausted", got)
	}
}
