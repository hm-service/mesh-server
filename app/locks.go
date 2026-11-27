package app

import "sync"

// keyedLock 给每台设备一把锁：同一台设备的签发、确认、撤销、删除、换绑排队，
// 不同设备互不影响。
//
// 这些操作都是「读设备快照 → 让种子签或撤（最长 15 秒） → 写本地」，中间没有
// 锁的话删除可以穿过正在进行的签发，留下一条种子认、控制面里没有主人的凭据。
// 条目随最后一个持有者释放：删除设备时不必显式回收，map 也不会无界增长。
type keyedLock struct {
	mu      sync.Mutex
	entries map[string]*keyedLockEntry
}

type keyedLockEntry struct {
	mu   sync.Mutex
	refs int
}

func newKeyedLock() *keyedLock {
	return &keyedLock{entries: make(map[string]*keyedLockEntry)}
}

// lock 等到 key 空闲并返回配对的 unlock。
func (k *keyedLock) lock(key string) (unlock func()) {
	k.mu.Lock()
	entry, ok := k.entries[key]
	if !ok {
		entry = &keyedLockEntry{}
		k.entries[key] = entry
	}
	entry.refs++
	k.mu.Unlock()

	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		k.mu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(k.entries, key)
		}
		k.mu.Unlock()
	}
}
