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
func (c *NonceCache) Claim(nonce string, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sweepLocked(now)
	if expires, ok := c.seen[nonce]; ok && expires.After(now) {
		return false
	}
	c.seen[nonce] = now.Add(c.ttl)
	return true
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
