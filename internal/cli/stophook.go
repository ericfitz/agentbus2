package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/client"
	"github.com/ericfitz/agentbus2/internal/profile"
)

// hookBudget bounds a whole hook run (Decision 41, choice 8). A var so tests
// do not wait.
var hookBudget = 500 * time.Millisecond

// stopHookInput is the part of a Stop hook's stdin that RunStopHook reads.
// Claude Code and Codex send snake_case; Grok sends camelCase.
type stopHookInput struct {
	Cwd                string `json:"cwd"`
	StopHookActive     bool   `json:"stop_hook_active"`
	StopHookActiveGrok bool   `json:"stopHookActive"`
}

// hookLog appends one fall-open diagnostic line to <data dir>/logs/hook.log:
// `<RFC 3339> pid=<pid> <stage>: <err>`. One shared file, so a misconfigured
// host does not gain a file per stop; a line is under PIPE_BUF, so appends
// from two hooks do not interleave. Any failure to write is ignored: the
// hook's exit is 0 either way.
func hookLog(dataDir, stage string, err error) {
	dir := filepath.Join(dataDir, "logs")
	if os.MkdirAll(dir, 0o700) != nil {
		return
	}
	f, ferr := os.OpenFile(filepath.Join(dir, "hook.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if ferr != nil {
		return
	}
	defer func() { _ = f.Close() }()
	msg := strings.Join(strings.Fields(err.Error()), " ")
	if len(msg) > 1000 {
		msg = msg[:1000]
	}
	_, _ = fmt.Fprintf(f, "%s pid=%d %s: %s\n", time.Now().Format(time.RFC3339), os.Getpid(), stage, msg)
}

// hookResult is what a hook's work produced: the bytes for stdout, or the
// stage and error it fell open on. Both empty is a silent success.
type hookResult struct {
	out   []byte
	stage string
	err   error
}

// runHook runs work under hookBudget and always returns 0. The work runs in a
// goroutine so that a step that ignores its context cannot hold the hook past
// the budget; its result is written here, never from the goroutine.
func runHook(ctx context.Context, environ []string, stdout io.Writer, work func(context.Context) hookResult) int {
	dataDir, derr := profile.DataDir(environ)
	ctx, cancel := context.WithTimeout(ctx, hookBudget)
	defer cancel()
	ch := make(chan hookResult, 1)
	go func() { ch <- work(ctx) }()
	select {
	case r := <-ch:
		switch {
		case r.err != nil:
			stage := r.stage
			if ctx.Err() != nil {
				stage = "budget"
			}
			if derr == nil {
				hookLog(dataDir, stage, r.err)
			}
		case len(r.out) > 0:
			_, _ = stdout.Write(r.out)
		}
	case <-ctx.Done():
		if derr == nil {
			hookLog(dataDir, "budget", ctx.Err())
		}
	}
	return 0
}

func stopHookWork(ctx context.Context, environ []string, stdin io.Reader) hookResult {
	var hi stopHookInput
	if err := json.NewDecoder(stdin).Decode(&hi); err != nil {
		return hookResult{stage: "decode", err: err}
	}
	if hi.StopHookActive || hi.StopHookActiveGrok {
		return hookResult{}
	}
	dataDir, err := profile.DataDir(environ)
	if err != nil {
		return hookResult{stage: "locate", err: err}
	}
	l, err := locateSession(dataDir, "", processTable, selfPID())
	if err != nil {
		return hookResult{stage: "locate", err: err}
	}
	// Profile and identity come from the state file only (bindProfile on zero
	// flags), never from flags, the environment or the repository file.
	o := client.Options{Environ: environ, Cwd: hi.Cwd}
	if err := bindProfile(&o, l); err != nil {
		return hookResult{stage: "bind", err: err}
	}
	if testClientOptions != nil {
		testClientOptions(&o)
	}
	conn, err := client.Open(ctx, o)
	if err != nil {
		return hookResult{stage: "open", err: err}
	}
	defer func() { _ = conn.Close() }()

	r, err := conn.Bus.Peek(ctx, bus.PeekInput{Session: l.Session})
	if bus.CodeOf(err) == bus.CodeSessionLapsed {
		again, rerr := resumeIfLapsed(ctx, conn.Bus, l, err)
		if rerr != nil {
			return hookResult{stage: "resume", err: rerr}
		}
		if again {
			r, err = conn.Bus.Peek(ctx, bus.PeekInput{Session: l.Session})
		}
	}
	if err != nil {
		return hookResult{stage: "peek", err: err}
	}
	if r.Count == 0 {
		return hookResult{}
	}
	display := l.DisplayName
	if display == "" {
		display = conn.Resolved.Identity
	}
	names := make([]string, 0, len(r.Channels))
	for _, c := range r.Channels {
		names = append(names, c.Channel)
	}
	head := fmt.Sprintf("Agentbus2: %d new message(s) for %s on %s. Call receive "+
		"(ack the previous batch) and handle what concerns you. ", r.Count, display, strings.Join(names, ", "))
	tail := "Then, except on Codex, make sure a background `agentbus2 wait` is running " +
		"(Bash with run_in_background; start one if none is; it replaces any running wait or watch) " +
		"so the next message wakes you, and stop."
	b, err := json.Marshal(map[string]string{"decision": "block", "reason": head + tail})
	if err != nil {
		return hookResult{stage: "peek", err: err}
	}
	return hookResult{out: append(b, '\n')}
}

// RunStopHook is the Stop hook (choice 8, Decision 41): when messages above
// the session's watch floor are waiting it prints
// {"decision":"block","reason":...} so the harness keeps the agent working;
// otherwise nothing. It always exits 0, falling open on any failure within
// hookBudget and leaving one line in hook.log. It never blocks a stop the
// harness is already continuing because of a Stop hook. Its only write is the
// resume of a lapsed session.
func RunStopHook(ctx context.Context, args []string, environ []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return runHook(ctx, environ, stdout, func(ctx context.Context) hookResult {
		return stopHookWork(ctx, environ, stdin)
	})
}
