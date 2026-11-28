package app

import (
	"context"
	"sync"
)

// keyedLock 给每台设备一把锁：同一台设备的签发、确认、撤销、删除、换绑排队，
// 不同设备互不影响。
//
// 这些操作都是「读设备快照 → 让种子签或撤（最长 15 秒） → 写本地」，中间没有
// 锁的话删除可以穿过正在进行的签发，留下一条种子认、控制面里没有主人的凭据。
// 条目随最后一个引用释放：删除设备时不必显式回收，map 也不会无界增长。
type keyedLock struct {
	mu      sync.Mutex
	entries map[string]*keyedLockEntry
}

// keyedLockEntry 是一台设备的队列。token 是一只一格信号量，里面那一个值就是锁
// 本身；refs 计数持有者与全部等待者，最后一个引用离开时条目被删掉。
type keyedLockEntry struct {
	token chan struct{}
	refs  int
}

func newKeyedLock() *keyedLock {
	return &keyedLock{entries: make(map[string]*keyedLockEntry)}
}

// lock 等到 key 空闲并返回配对的 unlock。等待跟着调用方的 context 结束：排队中
// 被取消或超时的请求当场放掉自己的引用并返回 ctx.Err()，不会在锁空出来之后进入
// 业务路径——那时写下的结果已经没有人在等，而一次签发可以压住它 15 秒。
//
// 拿到锁之后再看一次 context：锁可能正是在请求失效之后才空出来的。两处检查合起
// 来保证「引用被释放」与「不进入写副作用路径」是同一件事，调用方不必各自重复。
func (k *keyedLock) lock(ctx context.Context, key string) (unlock func(), err error) {
	k.mu.Lock()
	entry, ok := k.entries[key]
	if !ok {
		entry = &keyedLockEntry{token: make(chan struct{}, 1)}
		entry.token <- struct{}{}
		k.entries[key] = entry
	}
	entry.refs++
	k.mu.Unlock()

	select {
	case <-entry.token:
	case <-ctx.Done():
		k.forget(key, entry)
		return nil, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		entry.token <- struct{}{}
		k.forget(key, entry)
		return nil, err
	}
	return func() {
		entry.token <- struct{}{}
		k.forget(key, entry)
	}, nil
}

// forget 放掉一个引用，最后一个引用离开时删掉条目。
func (k *keyedLock) forget(key string, entry *keyedLockEntry) {
	k.mu.Lock()
	entry.refs--
	if entry.refs == 0 {
		delete(k.entries, key)
	}
	k.mu.Unlock()
}
