package delivery

import (
	"context"
	"time"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/notify"
	"github.com/ericfitz/agentbus2/internal/store"
)

// Watch is the wake primitive (Decisions 42, 45, 68): it registers a fresh
// watch id on the session, replacing any older watch, and returns when a
// message the session has not been handed arrives, the timeout passes (Woke
// false) or a newer watch replaces it. Ported from v1 internal/bus/wait.go
// (WaitAbove's floor rule) and internal/cli/watch.go (the merge gap, here the
// server-side WatchMergeSeconds). State (Definition of Done D): a watch whose
// process dies leaves watch_id set, which the next watch replaces.
func (s Service) Watch(ctx context.Context, identity string, in bus.WatchInput) (bus.WatchResult, error) {
	if in.Timeout < 0 {
		return bus.WatchResult{}, core.E(bus.CodeValidation, "timeout must not be negative")
	}
	if in.After < 0 {
		return bus.WatchResult{}, core.E(bus.CodeValidation, "after must not be negative")
	}
	limit := time.Duration(s.Cfg.WatchMaxSeconds) * time.Second
	timeout := in.Timeout
	if timeout == 0 || timeout > limit {
		timeout = limit // spec 3.5: the cap bounds the wait
	}
	watchID, err := core.RandomHex(8)
	if err != nil {
		return bus.WatchResult{}, bus.Internal(err)
	}
	// Subscribe before the first peek, so a hint between the peek and the
	// wait is not lost.
	hints, cancel := s.Notifier().Subscribe()
	defer cancel()
	err = s.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		if _, err := s.Sessions.Require(ctx, tx, identity, in.Session); err != nil {
			return err
		}
		return core.FromStore(tx.RegisterWatch(ctx, in.Session, watchID, s.LeaseUntil(s.NowMs())))
	})
	if err != nil {
		return bus.WatchResult{}, core.FromStore(err)
	}
	// An older watcher in this process re-checks its watch id.
	s.Publish(notify.Hint{})

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	deadline := time.Now().Add(timeout)
	for {
		res, woke, err := s.check(ctx, identity, in, watchID)
		if err != nil {
			return bus.WatchResult{}, err
		}
		if woke {
			// Merge window (Decision 45): let a burst finish, then report
			// the whole of it.
			merge := min(time.Duration(s.Cfg.WatchMergeSeconds)*time.Second, time.Until(deadline))
			if merge > 0 {
				select {
				case <-ctx.Done():
					return bus.WatchResult{}, ctx.Err()
				case <-time.After(merge):
				}
				if again, ok, err := s.check(ctx, identity, in, watchID); err != nil {
					return bus.WatchResult{}, err
				} else if ok {
					res = again
				}
			}
			return res, nil
		}
		select {
		case <-hints:
		case <-ctx.Done():
			return bus.WatchResult{}, ctx.Err()
		case <-timer.C:
			return bus.WatchResult{}, nil
		}
	}
}

// check is one peek: replaced when the session's watch id is no longer ours,
// otherwise whether anything wakes the session.
func (s Service) check(ctx context.Context, identity string, in bus.WatchInput, watchID string) (bus.WatchResult, bool, error) {
	var out bus.WatchResult
	var woke bool
	err := s.Store.Read(ctx, func(ctx context.Context, tx store.Tx) error {
		out, woke = bus.WatchResult{}, false
		sess, err := tx.Session(ctx, in.Session)
		if err != nil {
			return core.FromStore(err)
		}
		switch {
		case sess.Identity != identity:
			return core.E(bus.CodeNotFound, "session %q does not exist", in.Session)
		case sess.ClosedAt != 0:
			return core.E(bus.CodeSessionClosed, "session %q is closed", in.Session)
		case sess.WatchID != watchID:
			return core.E(bus.CodeReplaced, "a newer watch replaced this one")
		}
		w, err := s.peek(ctx, tx, identity, sess, in.After, in.IncludeOwn)
		if err != nil || w.count == 0 {
			return err
		}
		names, err := channelNames(ctx, tx, w.channels)
		if err != nil {
			return err
		}
		out, woke = bus.WatchResult{Woke: true, Seq: w.seq, Count: w.count, Channels: names}, true
		return nil
	})
	if err != nil {
		if ctx.Err() != nil {
			return bus.WatchResult{}, false, ctx.Err()
		}
		return bus.WatchResult{}, false, core.FromStore(err)
	}
	return out, woke, nil
}
