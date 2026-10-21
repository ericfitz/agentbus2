package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/client"
	"github.com/ericfitz/agentbus2/internal/procs"
	"github.com/ericfitz/agentbus2/internal/profile"
)

// watchMinGap is the least time between two wake lines (v1). A var so tests do
// not wait seconds.
var watchMinGap = 5 * time.Second

// watchLine formats the wake line for r (never empty): channels sorted and
// deduplicated.
func watchLine(display string, r bus.WatchResult) string {
	chans := slices.Clone(r.Channels)
	slices.Sort(chans)
	chans = slices.Compact(chans)
	return fmt.Sprintf("agentbus2: %d new message(s) for %s on %s; call receive", r.Count, display, strings.Join(chans, ", "))
}

// watchLoop arms Watch{After: floor, Timeout: 0} in a loop. A wake prints one
// line, raises the floor to its Seq and sleeps watchMinGap (or until ctx is
// done); a timeout re-arms silently. It returns only with a non-nil error (the
// Watch's, a failed write, or ctx's), together with the floor reached, so a
// resumed re-arm continues from it.
func watchLoop(ctx context.Context, s bus.Sessions, session, display string, floor int64, includeOwn bool, stdout io.Writer) (int64, error) {
	for {
		if err := ctx.Err(); err != nil {
			return floor, err
		}
		r, err := s.Watch(ctx, bus.WatchInput{Session: session, After: floor, IncludeOwn: includeOwn})
		if err != nil {
			return floor, err
		}
		if !r.Woke {
			continue
		}
		floor = r.Seq
		if _, err := fmt.Fprintln(stdout, watchLine(display, r)); err != nil {
			return floor, err
		}
		gap := time.NewTimer(watchMinGap)
		select {
		case <-ctx.Done():
			gap.Stop()
			return floor, ctx.Err()
		case <-gap.C:
		}
	}
}

// waiter is what watch and wait share once the session is located and the Conn
// is open.
type waiter struct {
	ctx     context.Context
	conn    *client.Conn
	l       located
	display string
	stop    func()
}

// exitFail prints err unless the exit is the silent 3 or 4, and returns the
// exit code.
func exitFail(stderr io.Writer, err error) int {
	code := exitCode(err)
	if code == 2 {
		fail(stderr, err)
	}
	return code
}

// startWaiter is the setup of watch and wait (choice 9): locate the session,
// bind the profile, open, find the display name and start the harness watch.
// A non-zero code means the command is over; the error is already reported.
func startWaiter(ctx context.Context, environ []string, o client.Options, session string, noHarnessWatch bool, stderr io.Writer) (*waiter, int) {
	dataDir, err := profile.DataDir(environ)
	if err != nil {
		return nil, exitFail(stderr, err)
	}
	l, err := locateSession(dataDir, session, processTable, selfPID())
	if err != nil {
		return nil, exitFail(stderr, err)
	}
	if err := bindProfile(&o, l); err != nil {
		return nil, exitFail(stderr, err)
	}
	o.Environ = environ
	conn, code := open(ctx, o, stderr)
	if code != 0 {
		return nil, 2
	}
	wctx, cancel := context.WithCancelCause(ctx)
	watched := make(chan struct{})
	if l.HarnessFound && !noHarnessWatch {
		go func() {
			defer close(watched)
			procs.WatchHarness(wctx, processTable, l.Harness, cancel)
		}()
	} else {
		close(watched)
	}
	return &waiter{
		ctx: wctx, conn: conn, l: l,
		display: displayNameFor(wctx, conn, l),
		stop:    func() { cancel(nil); <-watched; _ = conn.Close() },
	}, 0
}

// probe records a Watch that returned without error, so a resume is allowed
// once per lapse and never twice in a row without a successful Watch between.
type probe struct {
	bus.Sessions
	ok *atomic.Bool
}

func (p probe) Watch(ctx context.Context, in bus.WatchInput) (bus.WatchResult, error) {
	r, err := p.Sessions.Watch(ctx, in)
	if err == nil {
		p.ok.Store(true)
	}
	return r, err
}

// RunWatch is `agentbus2 watch` (choice 9, v1 ADR 0021): one line per wake on
// stdout, until replaced (3), the harness is gone (4) or an error (2). It
// never exits 0 or 1.
func RunWatch(ctx context.Context, args []string, environ []string, stdin io.Reader, stdout, stderr io.Writer) int {
	var o client.Options
	var session string
	var includeOwn, noHarness bool
	fs := newFlagSet("watch", stderr)
	client.Flags(fs, &o)
	fs.StringVar(&session, "session", "", "session id (default: the session of this harness)")
	fs.BoolVar(&includeOwn, "include-own", false, "also wake for this identity's own messages")
	fs.BoolVar(&noHarness, "no-harness-watch", false, "keep running after the harness exits")
	if err := fs.Parse(args); err != nil {
		return parseExit(err)
	}
	if fs.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "agentbus2 watch: unexpected argument", fs.Arg(0))
		return 2
	}
	ctx, stopSignals := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stopSignals()
	w, code := startWaiter(ctx, environ, o, session, noHarness, stderr)
	if w == nil {
		return code
	}
	defer w.stop()
	return exitFail(stderr, cancelCause(w.ctx, watchResuming(w.ctx, w.conn.Bus, w.l, w.display, includeOwn, stdout)))
}

// watchResuming runs watchLoop from floor 0 and answers a session_lapsed
// error with one resume: once per lapse, never twice in a row without a
// successful Watch in between. It returns watchLoop's final error, read
// through cancelCause.
func watchResuming(ctx context.Context, s bus.Sessions, l located, display string, includeOwn bool, stdout io.Writer) error {
	var ok atomic.Bool
	p := probe{Sessions: s, ok: &ok}
	var floor int64
	resumed := false
	for {
		var err error
		floor, err = watchLoop(ctx, p, l.Session, display, floor, includeOwn, stdout)
		err = cancelCause(ctx, err)
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
