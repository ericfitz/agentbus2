package token

import (
	"fmt"
	"testing"
	"time"
)

func TestReplaySetBounded(t *testing.T) {
	r := NewReplay()
	now := t0
	exp := now.Add(5 * time.Minute)
	if r.Seen("a", exp, now) {
		t.Fatal("first Seen true")
	}
	if !r.Seen("a", exp, now.Add(time.Minute)) {
		t.Fatal("second Seen false")
	}
	if r.Seen("b", exp, now) {
		t.Fatal("other jti reported seen")
	}
	// Once now reaches exp the entry is dropped and the jti is accepted again.
	if r.Seen("a", exp.Add(time.Hour), exp) {
		t.Fatal("expired entry still reported seen")
	}
	if got := r.len(); got != 1 {
		t.Fatalf("len after expiry = %d, want 1", got)
	}
	// 10 000 expired entries are pruned by the next call.
	r = NewReplay()
	for i := 0; i < 10000; i++ {
		r.Seen(fmt.Sprintf("j%d", i), now.Add(time.Minute), now)
	}
	if r.len() != 10000 {
		t.Fatalf("len = %d", r.len())
	}
	r.Seen("fresh", now.Add(time.Hour), now.Add(2*time.Minute))
	if got := r.len(); got != 1 {
		t.Fatalf("len after prune = %d, want 1", got)
	}
}
