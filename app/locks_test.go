package app

import (
	"testing"
	"time"
)

// TestKeyedLockSerialisesOneKeyOnly pins the property the credential lifecycle
// leans on: holders of the same key queue up, holders of different keys do not.
func TestKeyedLockSerialisesOneKeyOnly(t *testing.T) {
	locks := newKeyedLock()
	release := locks.lock("a")

	sameKey := make(chan struct{})
	go func() {
		unlock := locks.lock("a")
		close(sameKey)
		unlock()
	}()
	otherKey := make(chan struct{})
	go func() {
		unlock := locks.lock("b")
		close(otherKey)
		unlock()
	}()

	select {
	case <-otherKey:
	case <-time.After(time.Second):
		t.Fatal("a holder of another key was blocked")
	}
	select {
	case <-sameKey:
		t.Fatal("a second holder of the same key got in before the first released it")
	case <-time.After(50 * time.Millisecond):
	}

	release()
	select {
	case <-sameKey:
	case <-time.After(time.Second):
		t.Fatal("the waiter did not continue after the release")
	}
}

// TestKeyedLockForgetsReleasedKeys pins that the map does not grow with every
// device ever touched.
func TestKeyedLockForgetsReleasedKeys(t *testing.T) {
	locks := newKeyedLock()
	for i := range 100 {
		unlock := locks.lock("device")
		unlock()
		_ = i
	}
	locks.mu.Lock()
	defer locks.mu.Unlock()
	if len(locks.entries) != 0 {
		t.Fatalf("entries = %d, want the released key to be dropped", len(locks.entries))
	}
}
