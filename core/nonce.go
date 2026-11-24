package core

import (
	"sync"
	"time"
)

// NonceCache remembers nonces seen inside the replay window (CONTRACT.md 1.3).
type NonceCache struct {
	mu        sync.Mutex
	ttl       time.Duration
	seen      map[string]time.Time
	lastSweep time.Time
}

// NewNonceCache returns a cache whose entries expire after ttl.
func NewNonceCache(ttl time.Duration) *NonceCache {
	return &NonceCache{ttl: ttl, seen: make(map[string]time.Time)}
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
	if expires, ok := c.seen[nonce]; ok && expires.After(now) {
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
