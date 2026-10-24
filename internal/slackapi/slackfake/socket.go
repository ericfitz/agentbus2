package slackfake

import (
	"context"

	"github.com/ericfitz/agentbus2/internal/slackapi"
)

// RunSocket delivers queued events to handle, acking each envelope first, and
// starts with EventConnected (again after every Disconnect). It returns nil
// when ctx ends. Two concurrent calls are allowed so a test can detect a
// second runner through MaxConcurrentSockets.
func (f *Fake) RunSocket(ctx context.Context, handle func(context.Context, slackapi.Event)) error {
	f.mu.Lock()
	if err := f.begin("RunSocket"); err != nil {
		f.mu.Unlock()
		return err
	}
	if f.doneShut {
		f.done = make(chan struct{})
		f.doneShut = false
	}
	done := f.done
	f.active++
	f.maxActive = max(f.maxActive, f.active)
	seen := f.gen
	f.mu.Unlock()

	defer func() {
		f.mu.Lock()
		f.active--
		if f.active == 0 && !f.doneShut {
			close(done)
			f.doneShut = true
		}
		f.mu.Unlock()
	}()

	handle(ctx, slackapi.Event{Kind: slackapi.EventConnected})
	for {
		f.mu.Lock()
		if ctx.Err() != nil {
			f.mu.Unlock()
			return nil
		}
		var (
			ev      slackapi.Event
			deliver bool
		)
		switch {
		case f.gen != seen:
			seen = f.gen
			ev, deliver = slackapi.Event{Kind: slackapi.EventConnected}, true
		case len(f.queue) > 0:
			ev, f.queue = f.queue[0], f.queue[1:]
			f.acks = append(f.acks, ev.EnvelopeID)
			deliver = true
		}
		wait := f.changed
		f.mu.Unlock()
		if deliver {
			handle(ctx, ev)
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-wait:
		}
	}
}
