package slackbridge

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/ericfitz/agentbus2/internal/slackapi"
)

const (
	// LockName is the Store lock the one bridge runner holds (not the
	// "bridge-create:" prefix of Create's lock).
	LockName = "slack-bridge"

	// releaseTimeout bounds the lock release at shutdown.
	releaseTimeout = 5 * time.Second
	// drainPoll is how often the drain loop runs without a hint.
	drainPoll = 5 * time.Second
	// eventQueue is the buffer between the socket handler and the worker; an
	// event that finds it full is dropped and a catch-up is requested.
	eventQueue = 1024
)

// retryDelay is the requeue delay before attempt (1-based): base doubling,
// capped at requeueMax without ever shifting past it.
func retryDelay(base time.Duration, attempt int) time.Duration {
	d := base
	for i := 1; i < attempt; i++ {
		if d >= requeueMax/2 {
			return requeueMax
		}
		d *= 2
	}
	return min(d, requeueMax)
}

// Run is the bridge runner (spec 5.2). It first retries AuthTest every
// LockRenew until Slack answers, then every LockRenew calls TryLock on
// "slack-bridge" for 3*LockRenew with the Core's Owner. On gaining the lock
// it starts the Socket Mode connection, the worker (Slack-to-bus work, serial:
// events, the catch-up after each connect and the retries), the drain loop
// (woken by notifier hints for bridged channels and every 5 s) and the
// lifecycle loop (every LifecycleInterval, with a catch-up request); on losing
// the lock, or on a TryLock error, it stops all four before the next renew; on
// return it releases the lock. It returns nil when ctx ends.
//
// State (Definition of Done D): a second runner over the same store never
// gets the lock while the first renews it, so at most one Socket Mode
// connection exists. A runner killed without Unlock loses the SQLite lease
// after 3*LockRenew, or its Postgres session at once; the taker catches up
// (before its first drain, and again on every connect) and drains from the
// acked mark, posting each unacked message once. An overlap during takeover
// is bounded by the lease; the map-row check in Drain keeps the second
// runner from double counting. A post that succeeded before a crash and
// before its map row is repaired by catch-up through the message metadata
// when Slack keeps it; otherwise that one message is posted again.
func (s *Service) Run(ctx context.Context) error {
	for {
		if _, ok := s.Self(); ok {
			break
		}
		if err := sleep(ctx, s.cfg.LockRenew); err != nil {
			return nil
		}
		if err := s.authenticate(ctx); err != nil {
			s.log.Warn("slack bridge: auth.test failed; the bridge is not ready", "err", err)
		}
	}

	var rs *runSession
	defer func() {
		if rs != nil {
			rs.stop()
		}
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), releaseTimeout)
		defer cancel()
		if err := s.core.Store.Unlock(rctx, LockName, s.core.Owner); err != nil {
			s.log.Error("slack bridge: lock release failed", "err", err)
		}
	}()
	renew := time.NewTicker(s.cfg.LockRenew)
	defer renew.Stop()
	for {
		now := s.core.NowMs()
		held, err := s.core.Store.TryLock(ctx, LockName, s.core.Owner, now, now+3*s.cfg.LockRenew.Milliseconds())
		if err != nil && ctx.Err() == nil {
			s.log.Warn("slack bridge: lock attempt failed", "err", err)
		}
		switch {
		case err != nil || !held:
			if rs != nil {
				s.log.Warn("slack bridge: runner stopped; the lock is not held")
				rs.stop()
				rs = nil
				s.clearBridgePosted(0)
			}
		case rs == nil && ctx.Err() == nil:
			s.log.Info("slack bridge: runner started")
			rs = s.startSession(ctx)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-renew.C:
		}
	}
}

// runSession is the goroutines of one lock tenure.
type runSession struct {
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func (r *runSession) stop() {
	r.cancel()
	r.wg.Wait()
}

func (s *Service) startSession(parent context.Context) *runSession {
	ctx, cancel := context.WithCancel(parent)
	rs := &runSession{cancel: cancel}
	events := make(chan slackapi.Event, eventQueue)
	catchup := make(chan struct{}, 1)
	request := func() {
		select {
		case catchup <- struct{}{}:
		default:
		}
	}
	hints, cancelHints := s.core.Notifier().Subscribe()

	rs.wg.Add(4)
	go func() { // the socket: hands events to the worker, never blocks on them
		defer rs.wg.Done()
		for ctx.Err() == nil {
			err := s.sc.RunSocket(ctx, func(_ context.Context, ev slackapi.Event) {
				if ev.Kind == slackapi.EventConnected {
					s.markConnected()
				}
				select {
				case events <- ev:
				default:
					s.log.Warn("slack bridge: event queue full; catch-up requested", "kind", string(ev.Kind))
					if ev.Kind == slackapi.EventMemberLeft {
						s.markConnected() // the next lifecycle pass rechecks every done scope
					}
					request()
				}
			})
			if err != nil && ctx.Err() == nil {
				s.log.Warn("slack bridge: socket failed", "err", err)
			}
			if sleep(ctx, s.cfg.RetryBase) != nil {
				return
			}
		}
	}()
	go func() { defer rs.wg.Done(); s.worker(ctx, events, catchup) }()
	go func() { // bus to Slack
		defer rs.wg.Done()
		defer cancelHints()
		// Catch-up first: it repairs the map row of a post whose row a crash
		// lost, so the drain does not post it again.
		if err := s.CatchUp(ctx); err != nil && ctx.Err() == nil {
			s.log.Warn("slack bridge: catch-up failed", "err", err)
		}
		poll := time.NewTicker(drainPoll)
		defer poll.Stop()
		for {
			if err := s.Drain(ctx); err != nil && ctx.Err() == nil {
				s.log.Warn("slack bridge: drain failed", "err", err)
			}
			for woke := false; !woke; {
				select {
				case <-ctx.Done():
					return
				case <-poll.C:
					woke = true
				case h, ok := <-hints:
					woke = ok && s.isBridged(h.ChannelID)
				}
			}
		}
	}()
	go func() {
		defer rs.wg.Done()
		t := time.NewTicker(s.cfg.LifecycleInterval)
		defer t.Stop()
		for {
			if err := s.Lifecycle(ctx); err != nil && ctx.Err() == nil {
				s.log.Warn("slack bridge: lifecycle failed", "err", err)
			}
			request() // a message whose retries ran out waits for no reconnect
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	return rs
}

// retry is an inbound event waiting for its next attempt.
type retry struct {
	ev      slackapi.Event
	attempt int // the attempt about to run, 1-based
	due     time.Time
}

// worker is the one goroutine of Slack-to-bus work: socket events, the
// catch-up after each connect and on request, and the retries of events that
// hit a transient failure. It owns the retry timers, so none outlives it.
func (s *Service) worker(ctx context.Context, events <-chan slackapi.Event, catchup <-chan struct{}) {
	var pending []retry
	schedule := func(ev slackapi.Event, attempt int) {
		if attempt > requeueAttempts {
			s.log.Warn("slack message left to the next catch-up", "slack_user", ev.Message.User, "slack_channel", ev.Channel)
			return
		}
		pending = append(pending, retry{ev, attempt, time.Now().Add(retryDelay(s.cfg.RetryBase, attempt))})
	}
	runCatchUp := func() {
		if err := s.CatchUp(ctx); err != nil && ctx.Err() == nil {
			s.log.Warn("slack bridge: catch-up failed", "err", err)
		}
	}
	for {
		var due <-chan time.Time
		var timer *time.Timer
		if len(pending) > 0 {
			timer = time.NewTimer(time.Until(slices.MinFunc(pending, func(a, b retry) int { return a.due.Compare(b.due) }).due))
			due = timer.C
		}
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return
		case ev := <-events:
			if ev.Kind == slackapi.EventConnected {
				runCatchUp()
			} else if s.Dispatch(ctx, ev) {
				schedule(ev, 1)
			}
		case <-catchup:
			runCatchUp()
		case <-due:
			now := time.Now()
			ready := slices.DeleteFunc(slices.Clone(pending), func(r retry) bool { return r.due.After(now) })
			pending = slices.DeleteFunc(pending, func(r retry) bool { return !r.due.After(now) })
			for _, r := range ready {
				if ctx.Err() != nil {
					return // the timer has fired
				}
				if s.ProcessEvent(ctx, r.ev) == OutcomeRetry {
					schedule(r.ev, r.attempt+1)
				}
			}
		}
		if timer != nil {
			timer.Stop()
		}
	}
}
