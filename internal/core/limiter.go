package core

import (
	"sync"
	"time"
)

// Limiter holds per-identity token buckets for count and bytes with a
// one-second burst (Decision 86; v1 internal/bus/ratelimit.go, split into
// Check and Charge so a refused send charges nothing and a committed one
// charges after the commit).
//
// Buckets are never evicted: the map grows with the identities a process
// sees, and an identity is a registered principal, a bounded set.
type Limiter struct {
	mu       sync.Mutex
	perSec   float64
	bytesSec float64
	byteCap  float64
	buckets  map[string]*bucket
}

type bucket struct {
	count, bytes float64
	last         time.Time
}

// NewLimiter builds a limiter of perSec messages and kibPerSec KiB per
// second per identity; the byte burst is at least maxMessageKiB so one
// maximal message can always pass.
func NewLimiter(perSec, kibPerSec, maxMessageKiB int) *Limiter {
	byteCap := float64(kibPerSec * 1024)
	if m := float64(maxMessageKiB * 1024); byteCap < m {
		byteCap = m
	}
	return &Limiter{perSec: float64(perSec), bytesSec: float64(kibPerSec * 1024), byteCap: byteCap, buckets: map[string]*bucket{}}
}

// refill returns the identity's bucket brought up to now. Caller holds mu.
func (l *Limiter) refill(identity string, now time.Time) *bucket {
	bk, ok := l.buckets[identity]
	if !ok {
		bk = &bucket{count: l.perSec, bytes: l.byteCap, last: now}
		l.buckets[identity] = bk
	}
	if el := now.Sub(bk.last).Seconds(); el > 0 {
		bk.count = min(l.perSec, bk.count+el*l.perSec)
		bk.bytes = min(l.byteCap, bk.bytes+el*l.bytesSec)
		bk.last = now
	}
	return bk
}

// Check refills the identity's bucket and reports whether one message of n
// bytes fits. It charges nothing.
func (l *Limiter) Check(identity string, n int, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	bk := l.refill(identity, now)
	return bk.count >= 1 && bk.bytes >= float64(n)
}

// Charge refills the bucket and charges one message of n bytes. The bucket
// may go negative (concurrent senders that passed Check together); a later
// Check then refuses until it refills.
func (l *Limiter) Charge(identity string, n int, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	bk := l.refill(identity, now)
	bk.count--
	bk.bytes -= float64(n)
}
