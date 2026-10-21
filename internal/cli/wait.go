package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/client"
)

// waitWake is the one JSON line a woken wait prints: the wire carries no
// message (Decision 10), receive reads them.
type waitWake struct {
	Seq      int64    `json:"seq"`
	Count    int      `json:"count"`
	Channels []string `json:"channels"`
}

// waitOnce arms Watch until a wake (nil), the deadline (errTimeout) or an
// error. timeout 0 waits forever.
func waitOnce(ctx context.Context, s bus.Sessions, session string, includeOwn bool, timeout time.Duration, stdout io.Writer) error {
	var deadline time.Time
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	for {
		var remaining time.Duration // 0: the server's cap
		if !deadline.IsZero() {
			if remaining = time.Until(deadline); remaining <= 0 {
				return errTimeout
			}
		}
		r, err := s.Watch(ctx, bus.WatchInput{Session: session, After: 0, Timeout: remaining, IncludeOwn: includeOwn})
		if err != nil {
			return err
		}
		if !r.Woke {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err // replaced or harness gone: print nothing (v1)
		}
		chans := r.Channels
		if chans == nil {
			chans = []string{}
		}
		b, err := json.Marshal(waitWake{Seq: r.Seq, Count: r.Count, Channels: chans})
		if err != nil {
			return err
		}
		_, err = stdout.Write(append(b, '\n'))
		return err
	}
}

// RunWait is `agentbus2 wait` (choice 9): one wake prints a JSON line and
// exits 0, -timeout exits 1, then 2 error, 3 replaced, 4 harness gone.
func RunWait(ctx context.Context, args []string, environ []string, stdin io.Reader, stdout, stderr io.Writer) int {
	var o client.Options
	var session string
	var includeOwn, noHarness bool
	var timeout time.Duration
	fs := newFlagSet("wait", stderr)
	client.Flags(fs, &o)
	fs.StringVar(&session, "session", "", "session id (default: the session of this harness)")
	fs.BoolVar(&includeOwn, "include-own", false, "also wake for this identity's own messages")
	fs.BoolVar(&noHarness, "no-harness-watch", false, "keep running after the harness exits")
	fs.DurationVar(&timeout, "timeout", 0, "give up after this long and exit 1 (default: wait forever)")
	if err := fs.Parse(args); err != nil {
		return parseExit(err)
	}
	if fs.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "agentbus2 wait: unexpected argument", fs.Arg(0))
		return 2
	}
	if timeout < 0 {
		_, _ = fmt.Fprintln(stderr, "agentbus2 wait: -timeout must not be negative")
		return 2
	}
	ctx, stopSignals := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stopSignals()
	w, code := startWaiter(ctx, environ, o, session, noHarness, stderr)
	if w == nil {
		return code
	}
	defer w.stop()
	if err := waitResuming(w.ctx, w.conn.Bus, w.l, includeOwn, timeout, stdout); err != nil {
		return exitFail(stderr, cancelCause(w.ctx, err))
	}
	return 0
}

// waitResuming runs waitOnce under one deadline for the whole command (it runs
// across a resume) and answers a session_lapsed error with one resume: once
// per lapse, never twice in a row without a successful Watch in between.
func waitResuming(ctx context.Context, s bus.Sessions, l located, includeOwn bool, timeout time.Duration, stdout io.Writer) error {
	var deadline time.Time
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	var ok atomic.Bool
	p := probe{Sessions: s, ok: &ok}
	resumed := false
	for {
		remaining := time.Duration(0)
		if !deadline.IsZero() {
			if remaining = time.Until(deadline); remaining <= 0 {
				return errTimeout
			}
		}
		err := cancelCause(ctx, waitOnce(ctx, p, l.Session, includeOwn, remaining, stdout))
		if err == nil || errors.Is(err, errTimeout) {
			return err
		}
		if ok.Swap(false) {
			resumed = false
		}
		if resumed {
			return err
		}
		again, rerr := resumeIfLapsed(ctx, s, l, err)
		if !again {
			return rerr
		}
		resumed = true
	}
}
