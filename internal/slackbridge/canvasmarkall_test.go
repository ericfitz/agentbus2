package slackbridge

import (
	"testing"
	"time"
)

// Two markAll calls keep the first deadline: the refresh marks every scope
// one window after it was first marked, not after the last mark.
func TestCanvasMarkAllKeepsFirstDeadline(t *testing.T) {
	s := &Service{cfg: Config{CanvasEnabled: true, CanvasCoalesce: 30 * time.Second}}
	p := newCanvasPublisher(s)
	p.markAll()
	p.mu.Lock()
	first := p.pendingAt
	p.mu.Unlock()
	time.Sleep(20 * time.Millisecond)
	p.markAll()
	p.mu.Lock()
	got, all := p.pendingAt, p.pendingAll
	p.mu.Unlock()
	if !all || !got.Equal(first) {
		t.Fatalf("pendingAt = %v (all=%v), want the first deadline %v", got, all, first)
	}
}
