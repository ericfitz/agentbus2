package pgnotify

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ericfitz/agentbus2/internal/core/notify"
	"github.com/ericfitz/agentbus2/internal/store/postgres"
)

const (
	defaultMinBackoff = 100 * time.Millisecond
	defaultMaxBackoff = 5 * time.Second
	defaultAppName    = "agentbus2-listen"
)

// Options configures New. The zero value is usable.
type Options struct {
	Logger          *slog.Logger // nil: discard
	MinBackoff      time.Duration
	MaxBackoff      time.Duration // 0 values: 100 ms and 5 s
	ApplicationName string        // "": agentbus2-listen
	Connector       connector     // nil: pgx; tests pass a fake
}

// Listener is a notify.Notifier (by embedding) fed by Postgres NOTIFY.
type Listener struct {
	*notify.Memory
	log       *slog.Logger
	dsn       string
	conn      connector
	min, max  time.Duration
	connected atomic.Bool
	channel   atomic.Pointer[string] // the LISTENed channel of the latest session
	cancel    context.CancelFunc
	done      chan struct{}
	closeOnce sync.Once
	// sleep waits d or until ctx is done; tests inject a recorder through
	// newListener.
	sleep func(ctx context.Context, d time.Duration)
}

// New starts the listener goroutine at once. A bad DSN or an unreachable
// server is logged and retried, never returned: the server serves without the
// doorbell. It panics on a negative backoff or MinBackoff > MaxBackoff.
func New(ctx context.Context, dsn string, o Options) *Listener {
	return newListener(ctx, dsn, o, nil)
}

func newListener(ctx context.Context, dsn string, o Options, sleep func(context.Context, time.Duration)) *Listener {
	if o.MinBackoff < 0 {
		panic(fmt.Sprintf("pgnotify: Options.MinBackoff must not be negative, got %v", o.MinBackoff))
	}
	if o.MaxBackoff < 0 {
		panic(fmt.Sprintf("pgnotify: Options.MaxBackoff must not be negative, got %v", o.MaxBackoff))
	}
	if o.MinBackoff == 0 {
		o.MinBackoff = defaultMinBackoff
	}
	if o.MaxBackoff == 0 {
		o.MaxBackoff = defaultMaxBackoff
	}
	if o.MinBackoff > o.MaxBackoff {
		panic(fmt.Sprintf("pgnotify: Options.MinBackoff %v exceeds Options.MaxBackoff %v", o.MinBackoff, o.MaxBackoff))
	}
	if o.ApplicationName == "" {
		o.ApplicationName = defaultAppName
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if o.Connector == nil {
		o.Connector = pgxConnector{applicationName: o.ApplicationName}
	}
	if sleep == nil {
		sleep = realSleep
	}
	lctx, cancel := context.WithCancel(ctx)
	l := &Listener{
		Memory: notify.NewMemory(), log: o.Logger, dsn: dsn, conn: o.Connector,
		min: o.MinBackoff, max: o.MaxBackoff, cancel: cancel,
		done: make(chan struct{}), sleep: sleep,
	}
	go l.run(lctx)
	return l
}

func realSleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}

// Close stops the goroutine, closes the connection and waits for the
// goroutine. It is idempotent.
func (l *Listener) Close() {
	l.closeOnce.Do(l.cancel)
	<-l.done
}

// Connected is true between a successful LISTEN and the next error.
func (l *Listener) Connected() bool { return l.connected.Load() }

// Channel is the channel the latest session LISTENed on ("" before the first).
// It exists for tests that compare it with the dialect's channel expression.
func (l *Listener) Channel() string {
	if p := l.channel.Load(); p != nil {
		return *p
	}
	return ""
}

func (l *Listener) run(ctx context.Context) {
	defer close(l.done)
	backoff := l.min
	for ctx.Err() == nil {
		if l.session(ctx) {
			backoff = l.min
		}
		if ctx.Err() != nil {
			return
		}
		// Jitter inside [d/2, d].
		d := backoff/2 + time.Duration(rand.Int64N(int64(backoff/2)+1))
		l.sleep(ctx, d)
		backoff = min(backoff*2, l.max)
	}
}

// session runs one connection until it fails; it reports whether LISTEN
// succeeded (so the caller resets the backoff).
func (l *Listener) session(ctx context.Context) (listened bool) {
	c, err := l.conn.Connect(ctx, l.dsn)
	if err != nil {
		if ctx.Err() == nil {
			l.log.Warn("pgnotify: connect failed", "err", err)
		}
		return false
	}
	defer func() {
		l.connected.Store(false)
		cctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = c.Close(cctx)
	}()
	channel, err := c.Channel(ctx)
	if err == nil {
		err = c.Listen(ctx, channel)
	}
	if err != nil {
		if ctx.Err() == nil {
			l.log.Warn("pgnotify: listen failed", "err", err)
		}
		return false
	}
	l.channel.Store(&channel)
	// Catch up on anything missed while not listening.
	l.Publish(notify.Hint{})
	l.connected.Store(true)
	for {
		payload, err := c.Wait(ctx)
		if err != nil {
			l.connected.Store(false)
			if ctx.Err() == nil {
				l.log.Warn("pgnotify: wait failed", "err", err)
			}
			return true
		}
		if id, seq, ok := postgres.ParsePayload(payload); ok {
			l.Publish(notify.Hint{ChannelID: id, Seq: seq})
		} else {
			l.log.Warn("pgnotify: malformed payload", "payload", logPrefix(payload), "bytes", len(payload))
			l.Publish(notify.Hint{})
		}
	}
}

// logPrefix bounds a payload quoted in a log line: whoever can NOTIFY on the
// channel controls its content.
func logPrefix(p string) string {
	const limit = 64
	if len(p) <= limit {
		return p
	}
	return p[:limit] + "..."
}
