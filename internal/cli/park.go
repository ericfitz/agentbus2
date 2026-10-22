package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"

	"github.com/ericfitz/agentbus2/internal/profile"
	"github.com/ericfitz/agentbus2/internal/sessionstate"
)

// RunPark is `agentbus2 park [-session ID]` (decision 18, v1 ADR 0025): it
// marks the session parked in its state file, so that nothing wakes it: wait
// and watch ignore wakes and the Stop hook stays silent. agentbus2 has no
// direct messages, so no message class still wakes it; an unpark does (see
// RunUnpark). Parking a parked session succeeds. Exit 0 on success (a JSON
// document on stdout), 2 when no session is found.
func RunPark(ctx context.Context, args []string, environ []string, stdin io.Reader, stdout, stderr io.Writer) int {
	var session string
	flags := newFlagSet("park", stderr)
	flags.StringVar(&session, "session", "", "session id (default: the session of this harness)")
	if err := flags.Parse(args); err != nil {
		return parseExit(err)
	}
	if flags.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "agentbus2 park: unexpected argument", flags.Arg(0))
		return 2
	}
	dataDir, err := profile.DataDir(environ)
	if err != nil {
		fail(stderr, err)
		return 2
	}
	l, err := locateSession(dataDir, session, processTable, selfPID())
	if err == nil && !l.FromFile {
		err = fmt.Errorf("no state file for session %s; agentbus2 mcp writes it when the session registers", l.Session)
	}
	if err == nil {
		err = sessionstate.SetParked(dataDir, l.Session, true)
	}
	if err != nil {
		fail(stderr, err)
		return 2
	}
	if err := printJSON(stdout, map[string]any{"session": l.Session, "parked": true}); err != nil {
		fail(stderr, err)
		return 2
	}
	return 0
}

// RunUnpark is `agentbus2 unpark [-session ID]` (decision 18), the
// UserPromptSubmit hook: it clears the session's parked mark. It prints
// nothing and always exits 0, falling open within hookBudget and leaving one
// line in hook.log on a failure; a session with no state file is not a
// failure. Unparking a session that is not parked succeeds.
func RunUnpark(ctx context.Context, args []string, environ []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return runHook(ctx, environ, stdout, func(context.Context) hookResult {
		// The prompt arrives on stdin; read it all (bounded) so a large one
		// never meets a closed pipe.
		defer func() {
			if stdin != nil {
				_, _ = io.Copy(io.Discard, io.LimitReader(stdin, 4<<20))
			}
		}()
		var session string
		flags := newFlagSet("unpark", io.Discard)
		flags.StringVar(&session, "session", "", "")
		if err := flags.Parse(args); err != nil {
			return hookResult{stage: "flags", err: err}
		}
		dataDir, err := profile.DataDir(environ)
		if err != nil {
			return hookResult{stage: "locate", err: err}
		}
		l, err := locateSession(dataDir, session, processTable, selfPID())
		if errors.Is(err, errNoSession) || errors.Is(err, errHarnessGone) {
			return hookResult{} // not an agentbus2 session: nothing to unpark, nothing to log
		}
		if err != nil {
			return hookResult{stage: "locate", err: err}
		}
		if !l.FromFile {
			return hookResult{}
		}
		if err := sessionstate.SetParked(dataDir, l.Session, false); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return hookResult{stage: "unpark", err: err}
		}
		return hookResult{}
	})
}
