package notify

import "sync"

// Memory is the in-process Notifier. Each subscriber has a one-slot channel;
// a Publish into a full slot is dropped, so a slow watcher holds one pending
// hint, never a backlog.
type Memory struct {
	mu   sync.Mutex
	subs map[*memSub]struct{}
}

type memSub struct{ ch chan Hint }

// NewMemory returns an empty in-memory notifier.
func NewMemory() *Memory { return &Memory{subs: map[*memSub]struct{}{}} }

// Publish offers h to every subscriber without blocking.
func (m *Memory) Publish(h Hint) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for s := range m.subs {
		select {
		case s.ch <- h:
		default:
		}
	}
}

// Subscribe registers a subscriber. The cancel func removes it; the channel is
// never closed, so a late Publish cannot panic.
func (m *Memory) Subscribe() (<-chan Hint, func()) {
	s := &memSub{ch: make(chan Hint, 1)}
	m.mu.Lock()
	m.subs[s] = struct{}{}
	m.mu.Unlock()
	return s.ch, func() {
		m.mu.Lock()
		delete(m.subs, s)
		m.mu.Unlock()
	}
}
