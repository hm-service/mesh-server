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

// Claim records nonce and reports whether it was unused. Expired entries are
// swept opportunistically, so no background goroutine is needed.
//
// The entry expires ttl after the later of now and signedAt: a signature whose
// timestamp lies in the future is accepted until signedAt+ttl, so remembering
// the nonce for ttl after it was seen would let the same signature in again
// inside its own window.
func (c *NonceCache) Claim(nonce string, now, signedAt time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sweepLocked(now)
	if expires, ok := c.seen[nonce]; ok {
		if expires.After(now) {
			return false
		}
		delete(c.seen, nonce)
	}
	// 饱和时拒绝新 nonce，不驱逐仍在窗口内的条目：驱逐等于让一份已经用过的
	// 签名重新可用。两条路径对调用方是同一种结果（401），策略本身是确定的。
	if len(c.seen) >= c.max {
		return false
	}
	c.seen[nonce] = later(now, signedAt).Add(c.ttl)
	return true
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
