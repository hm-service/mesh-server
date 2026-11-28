package app

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestKeyedLockSerialisesOneKeyOnly pins the property the credential lifecycle
// leans on: holders of the same key queue up, holders of different keys do not.
func TestKeyedLockSerialisesOneKeyOnly(t *testing.T) {
	locks := newKeyedLock()
	ctx := context.Background()
	release, err := locks.lock(ctx, "a")
	if err != nil {
		t.Fatalf("lock(a) = %v", err)
	}

	sameKey := make(chan struct{})
	go func() {
		unlock, err := locks.lock(ctx, "a")
		if err != nil {
			close(sameKey)
			return
		}
		close(sameKey)
		unlock()
	}()
	otherKey := make(chan struct{})
	go func() {
		unlock, err := locks.lock(ctx, "b")
		if err != nil {
			close(otherKey)
			return
		}
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
	for range 100 {
		unlock, err := locks.lock(context.Background(), "device")
		if err != nil {
			t.Fatalf("lock() = %v", err)
		}
		unlock()
	}
	locks.mu.Lock()
	defer locks.mu.Unlock()
	if len(locks.entries) != 0 {
		t.Fatalf("entries = %d, want the released key to be dropped", len(locks.entries))
	}
}

// TestKeyedLockWaiterGivesUpWhenItsRequestEnds pins the cancellation half: a
// request that was cancelled while queued returns at once instead of waiting for
// the holder (which may be a 15-second call to the seed), does not enter the
// guarded section, and leaves no reference behind.
func TestKeyedLockWaiterGivesUpWhenItsRequestEnds(t *testing.T) {
	locks := newKeyedLock()
	release, err := locks.lock(context.Background(), "device")
	if err != nil {
		t.Fatalf("lock() = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	waiter := make(chan error, 1)
	entered := make(chan struct{})
	go func() {
		unlock, err := locks.lock(ctx, "device")
		if err != nil {
			waiter <- err
			return
		}
		close(entered)
		unlock()
		waiter <- nil
	}()

	// 让等待者真的排在队列里，再取消它的请求。
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-waiter:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("queued lock() = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("the cancelled waiter did not return while the lock was held")
	}
	select {
	case <-entered:
		t.Fatal("the cancelled request entered the guarded section")
	default:
	}

	// 等待者已经走了：条目里只剩持有者一个引用，释放后连条目一起消失。
	locks.mu.Lock()
	refs := locks.entries["device"].refs
	locks.mu.Unlock()
	if refs != 1 {
		t.Fatalf("refs = %d, want only the holder left", refs)
	}
	release()
	locks.mu.Lock()
	defer locks.mu.Unlock()
	if len(locks.entries) != 0 {
		t.Fatalf("entries = %d, want the entry dropped with its last reference", len(locks.entries))
	}
}

// TestKeyedLockStaysUsableAfterACancelledWaiter pins the other side of giving up:
// a request that was cancelled while queued never enters the guarded section even
// when the lock becomes free right after, and the next request gets a working lock
// instead of a broken entry.
func TestKeyedLockStaysUsableAfterACancelledWaiter(t *testing.T) {
	locks := newKeyedLock()
	release, err := locks.lock(context.Background(), "device")
	if err != nil {
		t.Fatalf("lock() = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	release()

	// 锁已经空出来，但请求早在排队时就结束了：它拿不到锁，也不该走进后面的写入。
	if _, err := locks.lock(ctx, "device"); !errors.Is(err, context.Canceled) {
		t.Fatalf("lock() with an already cancelled request = %v, want context.Canceled", err)
	}
	// 交出锁的持有者没有把锁弄坏：另一个请求还能拿到它。
	unlock, err := locks.lock(context.Background(), "device")
	if err != nil {
		t.Fatalf("lock() after the cancelled one = %v", err)
	}
	unlock()
}
