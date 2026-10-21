package notify

import (
	"sync"
	"time"
)

// Polling is the local multi-process Notifier: every subscriber gets Hint{}
// each interval and Publish is a no-op, because a writer in another process
// cannot reach this one (Decision 52; the store is the shared truth).
type Polling struct{ interval time.Duration }

// NewPolling returns a polling notifier. It panics on a non-positive interval,
// which is a programming error (time.NewTicker would panic anyway).
func NewPolling(interval time.Duration) *Polling {
	if interval <= 0 {
		panic("notify: polling interval must be positive")
	}
	return &Polling{interval: interval}
}

// Publish does nothing.
func (p *Polling) Publish(Hint) {}

// Subscribe starts a goroutine feeding the channel until cancel is called.
// The channel has one slot and a tick into a full slot is dropped.
func (p *Polling) Subscribe() (<-chan Hint, func()) {
	ch := make(chan Hint, 1)
	stop := make(chan struct{})
	var once sync.Once
	go func() {
		t := time.NewTicker(p.interval)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				select {
				case ch <- Hint{}:
				default:
				}
			}
		}
	}()
	return ch, func() { once.Do(func() { close(stop) }) }
}
