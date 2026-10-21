package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/auth/keys"
	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/client"
	"github.com/ericfitz/agentbus2/internal/credential"
	"github.com/ericfitz/agentbus2/internal/profile"
	"github.com/ericfitz/agentbus2/internal/sessionstate"
	"github.com/ericfitz/agentbus2/internal/store"
)

func TestStopHookBlocksWhenPending(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	pat, sam := e.conn(t, "pat"), e.conn(t, "sam")
	sess, err := pat.Bus.Open(ctx, bus.OpenSessionInput{})
	if err != nil {
		t.Fatal(err)
	}
	state := func(harnessClient string) {
		e.writeState(t, sessionstate.File{Session: sess.ID, Identity: "pat", Profile: "default", DisplayName: sess.DisplayName, ResumeToken: sess.ResumeToken,
			HarnessPID: testHarness.Pid, HarnessStart: testHarness.Start, HarnessClient: harnessClient, Primary: true, OpenedAt: 1})
	}
	state("claude-code")
	for i := 0; i < 2; i++ {
		if _, err := sam.Bus.Send(ctx, bus.SendInput{Channel: "general", Content: "hi"}); err != nil {
			t.Fatal(err)
		}
	}
	run := func() (int, map[string]string, string) {
		var so, se syncBuf
		code := RunStopHook(ctx, nil, e.Environ, strings.NewReader(`{"cwd":"","stop_hook_active":false}`), &so, &se)
		var out map[string]string
		if so.String() != "" {
			if err := json.Unmarshal([]byte(so.String()), &out); err != nil {
				t.Fatalf("stdout %q: %v", so.String(), err)
			}
		}
		return code, out, se.String()
	}
	code, out, errOut := run()
	if errOut != "" {
		t.Fatalf("stderr %q: a hook never writes to stderr", errOut)
	}
	if code != 0 || out["decision"] != "block" || !strings.Contains(out["reason"], "2 new message(s) for "+sess.DisplayName+" on general") ||
		!strings.Contains(out["reason"], "`agentbus2 watch`") || strings.Contains(out["reason"], "-filter") {
		t.Fatalf("claude-code: exit %d %v", code, out)
	}
	state("codex")
	code, out, _ = run()
	if code != 0 || out["decision"] != "block" || !strings.Contains(out["reason"], "`agentbus2 wait`") || strings.Contains(out["reason"], "agentbus2 watch") {
		t.Fatalf("other harness: exit %d %v", code, out)
	}
	// Grok's camelCase flag also ends the hook silently.
	var so, se syncBuf
	if code := RunStopHook(ctx, nil, e.Environ, strings.NewReader(`{"stopHookActive":true}`), &so, &se); code != 0 || so.String() != "" {
		t.Fatalf("stopHookActive: exit %d stdout %q", code, so.String())
	}
}

func TestStopHookPeeksWithoutSideEffects(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	tenMinutesAgo := func() time.Time { return time.Now().Add(-10 * time.Minute) }
	pat, sam := e.conn(t, "pat"), e.conn(t, "sam")
	sess, err := pat.Bus.Open(ctx, bus.OpenSessionInput{})
	if err != nil {
		t.Fatal(err)
	}
	e.writeState(t, sessionstate.File{Session: sess.ID, Identity: "pat", Profile: "default", DisplayName: sess.DisplayName, ResumeToken: sess.ResumeToken,
		HarnessPID: testHarness.Pid, HarnessStart: testHarness.Start, Primary: true, OpenedAt: 1})
	lease := func(c *client.Conn, id string) int64 {
		t.Helper()
		var until int64
		err := c.Core.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
			row, err := tx.Session(ctx, id)
			until = row.LeaseExpiresAt
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return until
	}
	hook := func() string {
		var so, se syncBuf
		if code := RunStopHook(ctx, nil, e.Environ, strings.NewReader(`{}`), &so, &se); code != 0 {
			t.Fatalf("exit %d", code)
		}
		return so.String()
	}

	// A watch is armed before the hook and must survive it.
	watchRes := make(chan error, 1)
	var woke bus.WatchResult
	go func() {
		r, err := pat.Bus.Watch(ctx, bus.WatchInput{Session: sess.ID, After: 0, Timeout: 5 * time.Second})
		woke = r
		watchRes <- err
	}()
	time.Sleep(300 * time.Millisecond)
	before := lease(pat, sess.ID)
	if got := hook(); got != "" {
		t.Fatalf("nothing pending, hook printed %q", got)
	}
	if after := lease(pat, sess.ID); after != before {
		t.Fatalf("lease moved %d -> %d: peek wrote", before, after)
	}
	if _, err := sam.Bus.Send(ctx, bus.SendInput{Channel: "general", Content: "hi"}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-watchRes:
		if err != nil || !woke.Woke || woke.Count != 1 {
			t.Fatalf("the armed watch: %+v, %v (replaced by the hook?)", woke, err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the armed watch did not wake")
	}

	// The hook leaves the cursor alone: a Watch still wakes for the message,
	// and the hook counted it without receiving it.
	before = lease(pat, sess.ID)
	if got := hook(); !strings.Contains(got, "1 new message(s)") {
		t.Fatalf("hook printed %q", got)
	}
	if after := lease(pat, sess.ID); after != before {
		t.Fatalf("lease moved %d -> %d", before, after)
	}
	r, err := pat.Bus.Watch(ctx, bus.WatchInput{Session: sess.ID, After: 0, Timeout: time.Second})
	if err != nil || !r.Woke || r.Count != 1 {
		t.Fatalf("Watch after the hook: %+v, %v", r, err)
	}

	// The lapsed case: one Open{ResumeToken} (the one write), then the peek.
	e2 := newTestEnv(t)
	old := e2.conn(t, "pat", func(o *client.Options) { o.Now = tenMinutesAgo })
	sam2 := e2.conn(t, "sam")
	s2, err := old.Bus.Open(ctx, bus.OpenSessionInput{})
	if err != nil {
		t.Fatal(err)
	}
	e2.writeState(t, sessionstate.File{Session: s2.ID, Identity: "pat", Profile: "default", DisplayName: s2.DisplayName, ResumeToken: s2.ResumeToken,
		HarnessPID: testHarness.Pid, HarnessStart: testHarness.Start, Primary: true, OpenedAt: 1})
	for i := 0; i < 2; i++ {
		if _, err := sam2.Bus.Send(ctx, bus.SendInput{Channel: "general", Content: "hi"}); err != nil {
			t.Fatal(err)
		}
	}
	lapsedLease := lease(sam2, s2.ID)
	var so, se syncBuf
	if code := RunStopHook(ctx, nil, e2.Environ, strings.NewReader(`{}`), &so, &se); code != 0 || !strings.Contains(so.String(), "2 new message(s)") {
		t.Fatalf("lapsed: exit %d stdout %q", code, so.String())
	}
	if now := lease(sam2, s2.ID); now <= lapsedLease || now < time.Now().UnixMilli() {
		t.Fatalf("lease %d -> %d: the resume did not renew it", lapsedLease, now)
	}
}

func TestStopHookCountsUnackedBatch(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	pat, sam := e.conn(t, "pat"), e.conn(t, "sam")
	sess, err := pat.Bus.Open(ctx, bus.OpenSessionInput{})
	if err != nil {
		t.Fatal(err)
	}
	e.writeState(t, sessionstate.File{Session: sess.ID, Identity: "pat", Profile: "default", DisplayName: sess.DisplayName, ResumeToken: sess.ResumeToken,
		HarnessPID: testHarness.Pid, HarnessStart: testHarness.Start, Primary: true, OpenedAt: 1})
	hook := func() string {
		var so, se syncBuf
		RunStopHook(ctx, nil, e.Environ, strings.NewReader(`{}`), &so, &se)
		if se.String() != "" {
			t.Fatalf("stderr %q: a hook never writes to stderr", se.String())
		}
		return so.String()
	}
	send := func() {
		if _, err := sam.Bus.Send(ctx, bus.SendInput{Channel: "general", Content: "hi"}); err != nil {
			t.Fatal(err)
		}
	}
	send()
	send()
	rec, err := pat.Bus.Receive(ctx, bus.ReceiveInput{Session: sess.ID})
	if err != nil || len(rec.Messages) != 2 {
		t.Fatalf("receive: %+v, %v", rec, err)
	}
	if got := hook(); got != "" {
		t.Fatalf("the held batch re-blocked: %q", got)
	}
	if _, err := pat.Bus.Receive(ctx, bus.ReceiveInput{Session: sess.ID, Ack: rec.Batch}); err != nil {
		t.Fatal(err)
	}
	if got := hook(); got != "" {
		t.Fatalf("after the ack: %q", got)
	}
	send()
	if got := hook(); !strings.Contains(got, "1 new message(s)") {
		t.Fatalf("third send: %q", got)
	}
}

func TestStopHookSilentCases(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	pat := e.conn(t, "pat")
	sess, err := pat.Bus.Open(ctx, bus.OpenSessionInput{})
	if err != nil {
		t.Fatal(err)
	}
	good := sessionstate.File{Session: sess.ID, Identity: "pat", Profile: "default", DisplayName: sess.DisplayName, ResumeToken: sess.ResumeToken,
		HarnessPID: testHarness.Pid, HarnessStart: testHarness.Start, Primary: true, OpenedAt: 1}
	logPath := filepath.Join(e.DataDir, "logs", "hook.log")
	logLines := func() []string {
		b, err := os.ReadFile(logPath)
		if err != nil {
			return nil
		}
		return strings.Split(strings.TrimSpace(string(b)), "\n")
	}
	run := func(stdin string) (int, string) {
		var so, se syncBuf
		code := RunStopHook(ctx, nil, e.Environ, strings.NewReader(stdin), &so, &se)
		if se.String() != "" {
			t.Fatalf("stdin %q: stderr %q: a hook never writes to stderr", stdin, se.String())
		}
		return code, so.String()
	}

	// Silent by design: no log line.
	e.writeState(t, good)
	for name, stdin := range map[string]string{"stop_hook_active": `{"stop_hook_active":true}`, "nothing pending": `{}`} {
		if code, out := run(stdin); code != 0 || out != "" {
			t.Fatalf("%s: exit %d stdout %q", name, code, out)
		}
	}
	if _, err := os.Stat(logPath); err == nil {
		t.Fatalf("hook.log exists after silent-by-design runs: %v", logLines())
	}

	// Failures: nothing on stdout, exit 0, exactly one log line naming the stage.
	steps := []struct {
		name, stage, stdin string
		setup              func()
	}{
		{"no state file", "locate", `{}`, func() { _ = sessionstate.Remove(e.DataDir, sess.ID) }},
		{"malformed stdin", "decode", `{nope`, func() { e.writeState(t, good) }},
		{"profile no longer resolves", "open", `{}`, func() {
			f := good
			f.Profile = "gone"
			e.writeState(t, f)
		}},
		{"closed session", "peek", `{}`, func() {
			e.writeState(t, good)
			if err := pat.Bus.Close(ctx, bus.CloseSessionInput{Session: sess.ID}); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for i, st := range steps {
		st.setup()
		code, out := run(st.stdin)
		if code != 0 || out != "" {
			t.Fatalf("%s: exit %d stdout %q", st.name, code, out)
		}
		lines := logLines()
		if len(lines) != i+1 || !strings.Contains(lines[i], " "+st.stage+": ") || !strings.Contains(lines[i], "pid=") {
			t.Fatalf("%s: log %q, want %d lines, the last naming %q", st.name, lines, i+1, st.stage)
		}
	}
	if fi, err := os.Stat(logPath); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("hook.log: %v, %v", fi, err)
	}
	if fi, err := os.Stat(filepath.Dir(logPath)); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("logs dir: %v, %v", fi, err)
	}
}

func TestStopHookFailsOpenWithinBudget(t *testing.T) {
	e := newTestEnv(t)
	prev := hookBudget
	hookBudget = 200 * time.Millisecond
	t.Cleanup(func() { hookBudget = prev })

	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(entered) })
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) }) // runs before srv.Close
	e.writeProfile(t, profile.Profile{Name: "lab", Mode: profile.ModeServer, Server: srv.URL})
	key, err := keys.Generate(keys.EdDSA)
	if err != nil {
		t.Fatal(err)
	}
	if err := (credential.Store{Dir: profile.CredentialDir(e.ConfigDir, "lab")}).Save("pat", key); err != nil {
		t.Fatal(err)
	}
	e.writeState(t, sessionstate.File{Session: "s1", Identity: "pat", Profile: "lab", DisplayName: "pat", ResumeToken: "x",
		HarnessPID: testHarness.Pid, HarnessStart: testHarness.Start, Primary: true, OpenedAt: 1})

	var so, se syncBuf
	start := time.Now()
	code := RunStopHook(context.Background(), nil, e.Environ, strings.NewReader(`{}`), &so, &se)
	if d := time.Since(start); code != 0 || so.String() != "" || se.String() != "" || d > hookBudget+300*time.Millisecond {
		t.Fatalf("exit %d stdout %q stderr %q after %v", code, so.String(), se.String(), d)
	}
	select {
	case <-entered:
	default:
		t.Fatal("the server saw no request: the budget did not cut a request short")
	}
	b, err := os.ReadFile(filepath.Join(e.DataDir, "logs", "hook.log"))
	if err != nil || !strings.Contains(string(b), "budget") {
		t.Fatalf("hook.log %q, %v", b, err)
	}
}

// TestRunHookBudgetCutsWorkThatIgnoresContext: a step that never returns and
// ignores its context (a stuck stdin read, a blocking syscall) still cannot
// hold the hook past hookBudget; the hook prints nothing on stdout or stderr,
// exits 0 and logs the budget stage.
func TestRunHookBudgetCutsWorkThatIgnoresContext(t *testing.T) {
	e := newTestEnv(t)
	prev := hookBudget
	hookBudget = 100 * time.Millisecond
	t.Cleanup(func() { hookBudget = prev })
	stuck := make(chan struct{})
	t.Cleanup(func() { close(stuck) })
	var so syncBuf
	start := time.Now()
	res := make(chan int, 1)
	go func() {
		res <- runHook(context.Background(), e.Environ, &so, func(context.Context) hookResult {
			<-stuck
			return hookResult{out: []byte("late\n")}
		})
	}()
	var code int
	select {
	case code = <-res:
	case <-time.After(hookBudget + time.Second):
		t.Fatal("runHook waited on work that ignores its context, past hookBudget")
	}
	if d := time.Since(start); code != 0 || so.String() != "" || d > hookBudget+200*time.Millisecond {
		t.Fatalf("exit %d stdout %q after %v", code, so.String(), d)
	}
	b, err := os.ReadFile(filepath.Join(e.DataDir, "logs", "hook.log"))
	if err != nil || !strings.Contains(string(b), " budget: ") {
		t.Fatalf("hook.log %q, %v", b, err)
	}
}
