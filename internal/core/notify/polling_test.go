package notify

import (
	"testing"
	"time"
)

func TestPollingTicksAndIgnoresPublish(t *testing.T) {
	p := NewPolling(10 * time.Millisecond)
	ch, cancel := p.Subscribe()
	defer cancel()

	// Out-of-band publishes add nothing: the only hints are ticks, all zero.
	for i := 0; i < 50; i++ {
		p.Publish(Hint{ChannelID: 7, Seq: int64(i + 1)})
	}
	deadline := time.After(100 * time.Millisecond)
	n := 0
loop:
	for {
		select {
		case h := <-ch:
			if h != (Hint{}) {
				t.Fatalf("polling delivered %+v, want the zero hint", h)
			}
			n++
		case <-deadline:
			break loop
		}
	}
	if n < 3 {
		t.Fatalf("only %d ticks in 100ms at a 10ms interval", n)
	}
	if n > 12 {
		t.Fatalf("%d ticks in 100ms: Publish must not add hints", n)
	}
}

func TestPollingCancelStopsTicks(t *testing.T) {
	p := NewPolling(5 * time.Millisecond)
	ch, cancel := p.Subscribe()
	cancel()
	cancel() // idempotent
	time.Sleep(30 * time.Millisecond)
	// Drain a possibly buffered tick, then nothing more may arrive.
	select {
	case <-ch:
	default:
	}
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("tick after cancel")
		}
	case <-time.After(30 * time.Millisecond):
	}
}

func TestPollingRejectsNonPositiveInterval(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewPolling(0) must panic")
		}
	}()
	NewPolling(0)
}
