package token

import (
	"sync"
	"time"
)

// Replay remembers the jti of every accepted client assertion until it
// expires, so an assertion is single-use per process (Milestone 5 choice 4).
// A restart, or a second replica, forgets it; the exposure is bounded by
// assertion.MaxLifetime plus Leeway.
type Replay struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

// NewReplay returns an empty set.
func NewReplay() *Replay { return &Replay{seen: make(map[string]time.Time)} }

// Seen reports whether jti was recorded and has not expired. Otherwise it
// records jti until exp and returns false. Entries with exp <= now are dropped
// first, on every call, so the set holds only live assertions.
func (r *Replay) Seen(jti string, exp, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for k, e := range r.seen {
		if !e.After(now) {
			delete(r.seen, k)
		}
	}
	if _, ok := r.seen[jti]; ok {
		return true
	}
	r.seen[jti] = exp
	return false
}

func (r *Replay) len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.seen)
}
