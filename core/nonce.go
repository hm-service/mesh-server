package core

import (
	"sync"
	"time"
)

// NonceCacheEntries caps the replay cache. The control plane holds at most 200
// devices, each heartbeating every 30s, so a 300s window needs a few thousand
// entries; the cap only has to stop an authenticated device from growing the map
// without bound.
const NonceCacheEntries = 8192

// NonceCache remembers nonces seen inside the replay window (contract/device-api.md).
type NonceCache struct {
	mu        sync.Mutex
	ttl       time.Duration
	max       int
	seen      map[string]time.Time
	lastSweep time.Time
}

// NewNonceCache returns a cache whose entries expire after ttl.
func NewNonceCache(ttl time.Duration) *NonceCache {
	return &NonceCache{ttl: ttl, max: NonceCacheEntries, seen: make(map[string]time.Time)}
}

// NonceClaim is the outcome of claiming one nonce: the nonce was free, the nonce
// is a replay, or the cache is full and the server cannot tell.
type NonceClaim int

const (
	// NonceAccepted reports a nonce that had not been seen and is now remembered
	// for the rest of its replay window.
	NonceAccepted NonceClaim = iota
	// NonceReplay reports a nonce still inside its window: this request repeats a
	// signature that was already used. The caller answers an authentication
	// failure.
	NonceReplay
	// NonceExhausted reports a full cache. The nonce was not recorded, so the
	// server cannot say whether the request is a replay; refusing a remembered
	// nonce to make room is not an option either, because that would make a used
	// signature acceptable again. The condition is server capacity, not device
	// identity: it passes once the window has expired, so the caller must answer
	// a retryable failure instead of an authentication one.
	NonceExhausted
)

// Claim records nonce and reports what that meant. Expired entries are swept
// opportunistically, so no background goroutine is needed.
//
// The entry expires ttl after the later of now and signedAt: a signature whose
// timestamp lies in the future is accepted until signedAt+ttl, so remembering
// the nonce for ttl after it was seen would let the same signature in again
// inside its own window.
func (c *NonceCache) Claim(nonce string, now, signedAt time.Time) NonceClaim {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sweepLocked(now)
	if expires, ok := c.seen[nonce]; ok {
		if expires.After(now) {
			return NonceReplay
		}
		delete(c.seen, nonce)
	}
	// 饱和时拒绝新 nonce，不驱逐仍在窗口内的条目：驱逐等于让一份已经用过的
	// 签名重新可用。调用方据此回答可重试的服务不可用，而不是认证失败。
	if len(c.seen) >= c.max {
		return NonceExhausted
	}
	c.seen[nonce] = later(now, signedAt).Add(c.ttl)
	return NonceAccepted
}

// later returns the later of a and b.
func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func (c *NonceCache) sweepLocked(now time.Time) {
	if now.Sub(c.lastSweep) < c.ttl {
		return
	}
	for nonce, expires := range c.seen {
		if !expires.After(now) {
			delete(c.seen, nonce)
		}
	}
	c.lastSweep = now
}
