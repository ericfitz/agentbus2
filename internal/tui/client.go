package tui

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ericfitz/agentbus2/internal/bus"
)

// subState is what the client knows about one channel's subscription.
type subState int

const (
	// subUnknown: never subscribed.
	subUnknown subState = iota
	// subOn: subscribed.
	subOn
	// subOff: the user unsubscribed this run.
	subOff
	// subNoAccess: Subscribe answered forbidden (Decisions 80, 94).
	subNoAccess
)

// sessionHint makes the human's session show as <identity>/tui (Decision 39).
const sessionHint = "tui"

// errSessionLost wraps replaced and session_closed: nobody else may arm this
// session's watch, so a retry would fight it.
var errSessionLost = errors.New("session lost")

// retryBackoff is the wait after a failed call (Milestone 8 choice 3).
var retryBackoff = time.Second

// client is the TUI's bus session: one session opened with sessionHint, a
// subscription to every channel, and the Decision 83 receive loop. It sees
// the bus only as bus.Bus (ported from v1 internal/tui/client.go).
type client struct {
	b        bus.Bus
	identity string
	ctx      context.Context
	cancel   context.CancelFunc

	// mu guards session, subscribed and lapsedOnce: the loop goroutine
	// replaces the session on a resume while Bubble Tea's goroutine sends
	// with it.
	mu         sync.Mutex
	session    bus.Session
	subscribed map[string]subState
	lapsedOnce bool

	// floor is the Watch floor; only the receive loop touches it.
	floor int64
}

// newClient opens a session for identity with sessionHint and subscribes to
// every listed channel from "now". On a failure after the session opened it
// closes the session and returns the error.
func newClient(ctx context.Context, b bus.Bus, identity string) (*client, error) {
	s, err := b.Open(ctx, bus.OpenSessionInput{Hint: sessionHint})
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithCancel(ctx)
	c := &client{b: b, identity: identity, ctx: cctx, cancel: cancel, session: s, subscribed: map[string]subState{}}
	chans, err := b.ListChannels(ctx)
	if err != nil {
		_ = c.close()
		return nil, err
	}
	for _, ch := range chans {
		if err := c.subscribe(ch, "now"); err != nil {
			_ = c.close()
			return nil, err
		}
	}
	return c, nil
}

// sessionID reads the session id under mu: the one way the model reads it.
func (c *client) sessionID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.session.ID
}

// displayName reads the session's display name under mu.
func (c *client) displayName() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.session.DisplayName
}

// state reads the subscription state of name.
func (c *client) state(name string) subState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.subscribed[name]
}

// subscribe subscribes to ch from "now", "oldest" or "". It is idempotent per
// name: a channel already on, or marked no-access, makes no call. A forbidden
// answer marks the channel subNoAccess and returns nil (the rail marks it
// once; nothing retries it). Any other error is returned and the state is
// unchanged.
func (c *client) subscribe(ch bus.Channel, from string) error {
	switch c.state(ch.Name) {
	case subOn, subNoAccess:
		return nil
	}
	err := c.b.Subscribe(c.ctx, bus.SubscribeInput{Channel: ch.Name, From: from})
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case err == nil:
		c.subscribed[ch.Name] = subOn
	case bus.CodeOf(err) == bus.CodeForbidden:
		c.subscribed[ch.Name] = subNoAccess
	default:
		return err
	}
	return nil
}

// isSubscribed reports whether name is currently subscribed.
func (c *client) isSubscribed(name string) bool { return c.state(name) == subOn }

// noAccess reports whether Subscribe answered forbidden for name.
func (c *client) noAccess(name string) bool { return c.state(name) == subNoAccess }

// forget records name as explicitly unsubscribed (after Unsubscribe), rather
// than deleting the entry: the rail must tell "known but off" from "never
// subscribed" so it does not resubscribe from "oldest" and replay history on
// the next status tick.
func (c *client) forget(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.subscribed[name] = subOff
}

// known reports whether name has ever been subscribed (on, off or no access).
func (c *client) known(name string) bool { return c.state(name) != subUnknown }

// ok resets the one-resume-per-lapse allowance after any successful call.
func (c *client) ok() {
	c.mu.Lock()
	c.lapsedOnce = false
	c.mu.Unlock()
}

// resumeLapsed resumes the lapsed session once with its token (Decision 79),
// replacing c.session under mu. A second lapse before any successful call
// does not resume again. The hint goes with the token: a row the retention
// job swept makes Open start a fresh session (Decision 18), which must still
// be <identity>/tui; a real resume ignores it and keeps the name.
func (c *client) resumeLapsed() error {
	c.mu.Lock()
	if c.lapsedOnce {
		c.mu.Unlock()
		return errors.New("session lapsed again before any successful call")
	}
	c.lapsedOnce = true
	token := c.session.ResumeToken
	c.mu.Unlock()
	s, err := c.b.Open(c.ctx, bus.OpenSessionInput{Hint: sessionHint, ResumeToken: token})
	if err != nil {
		if bus.CodeOf(err) == "" {
			// A transport failure says nothing about the lapse: a later
			// attempt may resume.
			c.mu.Lock()
			c.lapsedOnce = false
			c.mu.Unlock()
		}
		return err
	}
	c.mu.Lock()
	c.session = s
	c.mu.Unlock()
	return nil
}

// wait sleeps retryBackoff; it reports false when the client was canceled.
func (c *client) wait() bool {
	select {
	case <-c.ctx.Done():
		return false
	case <-time.After(retryBackoff):
		return true
	}
}

// fail handles one failed call of the loop and reports whether the loop goes
// on. prev is reset after a resume (the pending batch, if any, comes back
// Redelivered and is deduped by seq).
func (c *client) fail(err error, send func(any), prev *string) bool {
	if c.ctx.Err() != nil {
		return false
	}
	switch bus.CodeOf(err) {
	case bus.CodeReplaced, bus.CodeSessionClosed:
		send(sessionLostMsg{err: fmt.Errorf("%w: %w", errSessionLost, err)})
		return false
	case bus.CodeSessionLapsed:
		rerr := c.resumeLapsed()
		if rerr == nil {
			*prev = ""
			return true
		}
		if code := bus.CodeOf(rerr); code == bus.CodeReplaced || code == bus.CodeSessionClosed {
			send(sessionLostMsg{err: fmt.Errorf("%w: %w", errSessionLost, rerr)})
			return false
		}
		err = rerr
	}
	send(receiveErrMsg{err: err})
	return c.wait()
}

// receiveLoop is the Decision 83 data path: arm Watch, and on a wake drain
// Receive with ack until a result carries no messages and no gaps. A timeout
// re-arms with the floor unchanged. It returns when the client is canceled or
// the session is lost. send takes a tea.Msg but is typed any so this file
// imports no charm package.
func (c *client) receiveLoop(send func(any)) {
	prev := ""
	for c.ctx.Err() == nil {
		wr, err := c.b.Watch(c.ctx, bus.WatchInput{
			Session:    c.sessionID(),
			After:      c.floor,
			IncludeOwn: true, // watch-own
		})
		if err != nil {
			if !c.fail(err, send, &prev) {
				return
			}
			continue
		}
		c.ok()
		if !wr.Woke {
			continue
		}
		c.floor = max(c.floor, wr.Seq)
		for c.ctx.Err() == nil {
			res, err := c.b.Receive(c.ctx, bus.ReceiveInput{
				Session:    c.sessionID(),
				Ack:        prev,
				IncludeOwn: true, // receive-own
			})
			if err != nil {
				if !c.fail(err, send, &prev) {
					return
				}
				continue
			}
			c.ok()
			prev = res.Batch
			if len(res.Messages) > 0 || len(res.Gaps) > 0 || res.Notice != "" || res.Instruction != "" {
				send(batchMsg{res: res})
			}
			if len(res.Messages) == 0 && len(res.Gaps) == 0 {
				prev = ""
				break
			}
		}
	}
}

// close stops the loop and closes the session so its name is freed at once.
// The Close goes out on its own context: on the canceled one it would fail at
// once and leave the name taken. A session already closed is not an error.
func (c *client) close() error {
	c.cancel()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := c.b.Close(ctx, bus.CloseSessionInput{Session: c.sessionID()})
	if code := bus.CodeOf(err); code == bus.CodeSessionClosed || code == bus.CodeNotFound {
		return nil
	}
	return err
}
