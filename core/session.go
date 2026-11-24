package core

import (
	"sync"
	"time"
)

// Sessions keeps admin sessions in memory; they do not survive a restart.
// CONTRACT.md 1.4 keeps the admin surface on one opaque cookie, so revoking a
// single session is not part of the model.
type Sessions struct {
	mu        sync.Mutex
	ttl       time.Duration
	now       func() time.Time
	items     map[string]time.Time
	lastSweep time.Time
}

// NewSessions returns a session table whose entries live for ttl.
func NewSessions(ttl time.Duration, now func() time.Time) *Sessions {
	return &Sessions{ttl: ttl, now: now, items: make(map[string]time.Time)}
}

// Create stores a new session and returns its id.
func (s *Sessions) Create() (string, error) {
	id, err := RandomHex(32)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.sweepLocked(now)
	s.items[id] = now.Add(s.ttl)
	return id, nil
}

// Valid reports whether id names an unexpired session.
func (s *Sessions) Valid(id string) bool {
	if id == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	expires, ok := s.items[id]
	if !ok {
		return false
	}
	if !s.now().Before(expires) {
		delete(s.items, id)
		return false
	}
	return true
}

// Delete drops a session; deleting an unknown id is a no-op.
func (s *Sessions) Delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.items, id)
}

func (s *Sessions) sweepLocked(now time.Time) {
	if now.Sub(s.lastSweep) < s.ttl/4 {
		return
	}
	for id, expires := range s.items {
		if !expires.After(now) {
			delete(s.items, id)
		}
	}
	s.lastSweep = now
}
