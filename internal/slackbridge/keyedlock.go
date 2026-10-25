package slackbridge

import "sync"

// keyedLock is a set of mutexes by bus channel ID: lock(k) blocks while
// another goroutine holds k, and different keys never block each other. An
// entry lives only while held or awaited.
type keyedLock struct {
	mu sync.Mutex
	m  map[int64]*keyedEntry
}

type keyedEntry struct {
	mu   sync.Mutex
	refs int
}

// lock acquires k and returns its release func.
func (k *keyedLock) lock(key int64) func() {
	k.mu.Lock()
	if k.m == nil {
		k.m = map[int64]*keyedEntry{}
	}
	e := k.m[key]
	if e == nil {
		e = &keyedEntry{}
		k.m[key] = e
	}
	e.refs++
	k.mu.Unlock()
	e.mu.Lock()
	return func() {
		e.mu.Unlock()
		k.mu.Lock()
		if e.refs--; e.refs == 0 {
			delete(k.m, key)
		}
		k.mu.Unlock()
	}
}
