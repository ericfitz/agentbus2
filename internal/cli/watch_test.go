package cli

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/auth/keys"
	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/client"
	"github.com/ericfitz/agentbus2/internal/credential"
	"github.com/ericfitz/agentbus2/internal/profile"
	"github.com/ericfitz/agentbus2/internal/sessionstate"
)

func TestWatchFlags(t *testing.T) {
	e := newTestEnv(t)
	for _, args := range [][]string{{"-channel", "x"}, {"-filter", "x"}, {"-timeout", "1s"}, {"extra"}} {
		var so, se syncBuf
		if code := RunWatch(context.Background(), args, e.Environ, strings.NewReader(""), &so, &se); code != 2 {
			t.Errorf("%v: exit %d, want 2 (stderr %q)", args, code, se.String())
		}
	}
	var so, se syncBuf
	if code := RunWatch(context.Background(), []string{"-h"}, e.Environ, strings.NewReader(""), &so, &se); code != 0 {
		t.Errorf("-h: exit %d, want 0", code)
	}
}

func TestWatchPrintsOneLinePerWakeAndNeverExits(t *testing.T) {
	e := newTestEnv(t)
	prev := watchMinGap
	watchMinGap = 40 * time.Millisecond
	t.Cleanup(func() { watchMinGap = prev })
	ctx := context.Background()

	pat, sam := e.conn(t, "pat"), e.conn(t, "sam")
	sess, err := pat.Bus.Open(ctx, bus.OpenSessionInput{})
	if err != nil {
		t.Fatal(err)
	}
	e.writeState(t, sessionstate.File{Session: sess.ID, Identity: "pat", Profile: "default", DisplayName: sess.DisplayName, ResumeToken: sess.ResumeToken,
		HarnessPID: testHarness.Pid, HarnessStart: testHarness.Start, Primary: true, OpenedAt: 1})
	send := func() {
		t.Helper()
		if _, err := sam.Bus.Send(ctx, bus.SendInput{Channel: "general", Content: "hi"}); err != nil {
			t.Fatal(err)
		}
	}
	send() // the backlog, announced once

	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var so, se syncBuf
	done := make(chan int, 1)
	go func() { done <- RunWatch(wctx, nil, e.Environ, strings.NewReader(""), &so, &se) }()

	var stamps []time.Time
	lines := func() []string {
		s := strings.TrimSpace(so.String())
		if s == "" {
			return nil
		}
		return strings.Split(s, "\n")
	}
	waitLines := func(n int) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for len(lines()) < n {
			if time.Now().After(deadline) {
				t.Fatalf("want %d lines, have %q (stderr %q)", n, so.String(), se.String())
			}
			time.Sleep(5 * time.Millisecond)
		}
		stamps = append(stamps, time.Now())
	}
	waitLines(1)
	time.Sleep(300 * time.Millisecond)
	send()
	waitLines(2)
	time.Sleep(300 * time.Millisecond)
	send()
	waitLines(3)

	re := regexp.MustCompile(`^agentbus2: \d+ new message\(s\) for ` + regexp.QuoteMeta(sess.DisplayName) + ` on general; call receive$`)
	for i, l := range lines() {
		if !re.MatchString(l) {
			t.Errorf("line %d %q does not match", i, l)
		}
	}
	if !strings.Contains(lines()[0], "1 new message(s)") {
		t.Errorf("the backlog line %q", lines()[0])
	}
	for i := 1; i < len(stamps); i++ {
		if stamps[i].Sub(stamps[i-1]) < watchMinGap {
			t.Errorf("lines %d and %d only %v apart", i, i+1, stamps[i].Sub(stamps[i-1]))
		}
	}
	select {
	case code := <-done:
		t.Fatalf("watch exited %d on its own", code)
	case <-time.After(500 * time.Millisecond):
	}
	before := so.String()
	cancel()
	select {
	case code := <-done:
		if code != 3 {
			t.Fatalf("exit %d after cancel, want 3 (stderr %q)", code, se.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watch did not exit after cancel")
	}
	if so.String() != before || se.String() != "" {
		t.Fatalf("output after cancel: stdout %q stderr %q", so.String(), se.String())
	}
}

func TestWatchResumesLapsedSession(t *testing.T) {
	e := newTestEnv(t)
	prev := watchMinGap
	watchMinGap = 40 * time.Millisecond
	t.Cleanup(func() { watchMinGap = prev })
	ctx := context.Background()
	tenMinutesAgo := func() time.Time { return time.Now().Add(-10 * time.Minute) }

	old := e.conn(t, "pat", func(o *client.Options) { o.Now = tenMinutesAgo })
	sess, err := old.Bus.Open(ctx, bus.OpenSessionInput{})
	if err != nil {
		t.Fatal(err)
	}
	sam := e.conn(t, "sam")
	e.writeState(t, sessionstate.File{Session: sess.ID, Identity: "pat", Profile: "default", DisplayName: sess.DisplayName, ResumeToken: sess.ResumeToken,
		HarnessPID: testHarness.Pid, HarnessStart: testHarness.Start, Primary: true, OpenedAt: 1})

	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var so, se syncBuf
	done := make(chan int, 1)
	go func() { done <- RunWatch(wctx, nil, e.Environ, strings.NewReader(""), &so, &se) }()
	time.Sleep(300 * time.Millisecond)
	if _, err := sam.Bus.Send(ctx, bus.SendInput{Channel: "general", Content: "hi"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(so.String(), "call receive") {
		select {
		case code := <-done:
			t.Fatalf("exit %d (stderr %q)", code, se.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("no line; stdout %q stderr %q", so.String(), se.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if code := <-done; code != 3 {
		t.Fatalf("exit %d, want 3", code)
	}

	// -session with no state file: nothing to resume with.
	e2 := newTestEnv(t)
	old2 := e2.conn(t, "pat", func(o *client.Options) { o.Now = tenMinutesAgo })
	sess2, err := old2.Bus.Open(ctx, bus.OpenSessionInput{})
	if err != nil {
		t.Fatal(err)
	}
	var so2, se2 syncBuf
	if code := RunWatch(ctx, []string{"-session", sess2.ID}, e2.Environ, strings.NewReader(""), &so2, &se2); code != 2 ||
		!strings.Contains(se2.String(), "session_lapsed") {
		t.Fatalf("exit %d, stderr %q", code, se2.String())
	}
}

func TestWatchReplacedExits3Silent(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	pat := e.conn(t, "pat")
	sess, err := pat.Bus.Open(ctx, bus.OpenSessionInput{})
	if err != nil {
		t.Fatal(err)
	}
	e.writeState(t, sessionstate.File{Session: sess.ID, Identity: "pat", Profile: "default", DisplayName: sess.DisplayName, ResumeToken: sess.ResumeToken,
		HarnessPID: testHarness.Pid, HarnessStart: testHarness.Start, Primary: true, OpenedAt: 1})

	var so1, se1, so2, se2 syncBuf
	c1, c2 := make(chan int, 1), make(chan int, 1)
	ctx2, cancel2 := context.WithCancel(ctx)
	defer cancel2()
	go func() { c1 <- RunWatch(ctx, nil, e.Environ, strings.NewReader(""), &so1, &se1) }()
	time.Sleep(400 * time.Millisecond)
	go func() { c2 <- RunWatch(ctx2, nil, e.Environ, strings.NewReader(""), &so2, &se2) }()
	select {
	case code := <-c1:
		if code != 3 || so1.String() != "" || se1.String() != "" {
			t.Fatalf("first: exit %d stdout %q stderr %q", code, so1.String(), se1.String())
		}
	case <-time.After(time.Second):
		t.Fatal("the first watch was not replaced within 1 s")
	}
	select {
	case code := <-c2:
		t.Fatalf("the second watch exited %d", code)
	case <-time.After(200 * time.Millisecond):
	}
	cancel2()
	if code := <-c2; code != 3 {
		t.Fatalf("second after cancel: %d", code)
	}
}

func TestWatchHarnessGoneExits4(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	pat := e.conn(t, "pat")
	sess, err := pat.Bus.Open(ctx, bus.OpenSessionInput{})
	if err != nil {
		t.Fatal(err)
	}
	e.writeState(t, sessionstate.File{Session: sess.ID, Identity: "pat", Profile: "default", DisplayName: sess.DisplayName, ResumeToken: sess.ResumeToken,
		HarnessPID: testHarness.Pid, HarnessStart: testHarness.Start, Primary: true, OpenedAt: 1})

	var so, se syncBuf
	done := make(chan int, 1)
	go func() { done <- RunWatch(ctx, nil, e.Environ, strings.NewReader(""), &so, &se) }()
	time.Sleep(300 * time.Millisecond)
	processTable.(*fakeTable).set(10, 1, 5, "claude") // keep the walk answering
	processTable.(*fakeTable).setGone(10)
	select {
	case code := <-done:
		if code != 4 || so.String() != "" || se.String() != "" {
			t.Fatalf("exit %d stdout %q stderr %q", code, so.String(), se.String())
		}
	case <-time.After(time.Second):
		t.Fatal("no exit 4")
	}

	// -no-harness-watch: the lookup finds the harness gone only at the start;
	// here the harness is already gone, so the start exits 4 at once.
	var so2, se2 syncBuf
	if code := RunWatch(ctx, []string{"-no-harness-watch"}, e.Environ, strings.NewReader(""), &so2, &se2); code != 4 {
		t.Fatalf("already-gone harness: exit %d", code)
	}
}

func TestWatchNoHarnessWatchKeepsRunning(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	pat := e.conn(t, "pat")
	sess, err := pat.Bus.Open(ctx, bus.OpenSessionInput{})
	if err != nil {
		t.Fatal(err)
	}
	e.writeState(t, sessionstate.File{Session: sess.ID, Identity: "pat", Profile: "default", DisplayName: sess.DisplayName, ResumeToken: sess.ResumeToken,
		HarnessPID: testHarness.Pid, HarnessStart: testHarness.Start, Primary: true, OpenedAt: 1})
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var so, se syncBuf
	done := make(chan int, 1)
	go func() {
		done <- RunWatch(wctx, []string{"-no-harness-watch"}, e.Environ, strings.NewReader(""), &so, &se)
	}()
	time.Sleep(200 * time.Millisecond)
	// The harness's start time changes (pid reuse) after the lookup.
	processTable.(*fakeTable).set(10, 1, 6, "claude")
	select {
	case code := <-done:
		t.Fatalf("exited %d with -no-harness-watch", code)
	case <-time.After(300 * time.Millisecond):
	}
	cancel()
	if code := <-done; code != 3 {
		t.Fatalf("exit %d", code)
	}
}

func TestWatchErrorExits2(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	e.writeProfile(t, profile.Profile{Name: "lab", Mode: profile.ModeServer, Server: "http://127.0.0.1:1", Identity: "pat"})
	key, err := keys.Generate(keys.EdDSA)
	if err != nil {
		t.Fatal(err)
	}
	if err := (credential.Store{Dir: profile.CredentialDir(e.ConfigDir, "lab")}).Save("pat", key); err != nil {
		t.Fatal(err)
	}
	var so, se syncBuf
	if code := RunWatch(ctx, []string{"-profile", "lab", "-session", "s9"}, e.Environ, strings.NewReader(""), &so, &se); code != 2 ||
		!strings.HasPrefix(se.String(), "agentbus2:") || so.String() != "" {
		t.Fatalf("exit %d stdout %q stderr %q", code, so.String(), se.String())
	}

	// No session for the harness and no -session.
	var so2, se2 syncBuf
	if code := RunWatch(ctx, nil, e.Environ, strings.NewReader(""), &so2, &se2); code != 2 || !strings.Contains(se2.String(), "-session") {
		t.Fatalf("exit %d stderr %q", code, se2.String())
	}

	// A malformed session id never reaches the bus.
	var so3, se3 syncBuf
	if code := RunWatch(ctx, []string{"-session", "../x"}, e.Environ, strings.NewReader(""), &so3, &se3); code != 2 {
		t.Fatalf("bad session id: exit %d", code)
	}
}

func TestWatchUsesStateFileProfile(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
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

	run := func(env []string, args ...string) (int, string) {
		var so, se syncBuf
		code := RunWatch(ctx, args, env, strings.NewReader(""), &so, &se)
		return code, se.String()
	}
	if code, stderr := run(e.Environ); code != 2 || hits.Load() < 1 || !strings.Contains(stderr, srv.URL) {
		t.Fatalf("no flags: exit %d hits %d stderr %q", code, hits.Load(), stderr)
	}
	hits.Store(0)
	code, stderr := run(e.Environ, "-profile", "default")
	if code != 2 || !strings.Contains(stderr, "lab") || !strings.Contains(stderr, "default") || hits.Load() != 0 {
		t.Fatalf("-profile default: exit %d hits %d stderr %q", code, hits.Load(), stderr)
	}
	code, stderr = run(e.Environ, "-identity", "sam")
	if code != 2 || !strings.Contains(stderr, "-identity") || hits.Load() != 0 {
		t.Fatalf("-identity sam: exit %d hits %d stderr %q", code, hits.Load(), stderr)
	}
	if code, stderr := run(e.Environ, "-profile", "lab"); code != 2 || hits.Load() < 1 || !strings.Contains(stderr, srv.URL) {
		t.Fatalf("-profile lab: exit %d hits %d stderr %q", code, hits.Load(), stderr)
	}
	hits.Store(0)
	env := append(append([]string{}, e.Environ...), "AGENTBUS2_PROFILE=default")
	if code, stderr := run(env); code != 2 || hits.Load() < 1 || !strings.Contains(stderr, srv.URL) {
		t.Fatalf("AGENTBUS2_PROFILE=default: exit %d hits %d stderr %q", code, hits.Load(), stderr)
	}
}

func TestWatchNeverExits0Or1(t *testing.T) {
	prev := watchMinGap
	watchMinGap = 40 * time.Millisecond
	t.Cleanup(func() { watchMinGap = prev })

	for _, woke := range []bool{true, false} {
		var n atomic.Int64
		var calls atomic.Int64
		s := stubSessions{watch: func(ctx context.Context, in bus.WatchInput) (bus.WatchResult, error) {
			calls.Add(1)
			if !woke {
				select { // a timeout at the server's cap, here 5 ms
				case <-ctx.Done():
				case <-time.After(5 * time.Millisecond):
				}
				return bus.WatchResult{}, nil
			}
			return bus.WatchResult{Woke: true, Seq: n.Add(1), Count: 1, Channels: []string{"general"}}, nil
		}}
		ctx, cancel := context.WithCancel(context.Background())
		var so syncBuf
		type res struct {
			floor int64
			err   error
		}
		done := make(chan res, 1)
		go func() {
			f, err := watchLoop(ctx, s, "s1", "pat", 0, false, &so)
			done <- res{f, err}
		}()
		time.Sleep(200 * time.Millisecond)
		select {
		case r := <-done:
			t.Fatalf("woke=%v: watchLoop returned (%d, %v) on its own", woke, r.floor, r.err)
		default:
		}
		lines := strings.Count(so.String(), "\n")
		if woke && lines < 3 {
			t.Errorf("only %d lines within 200 ms", lines)
		}
		if !woke && lines != 0 {
			t.Errorf("timeouts printed %q", so.String())
		}
		cancel()
		r := <-done
		err := cancelCause(ctx, r.err)
		if !errors.Is(err, errReplaced) || exitCode(err) != 3 {
			t.Errorf("woke=%v: err %v exit %d, want errReplaced and 3", woke, err, exitCode(err))
		}
		if woke && r.floor < 1 {
			t.Errorf("floor %d", r.floor)
		}
	}
}

func TestWatchLine(t *testing.T) {
	got := watchLine("pat", bus.WatchResult{Count: 3, Channels: []string{"b", "a", "b"}})
	if want := "agentbus2: 3 new message(s) for pat on a, b; call receive"; got != want {
		t.Fatalf("%q, want %q", got, want)
	}
}

func TestExitCodeTable(t *testing.T) {
	for _, c := range []struct {
		err  error
		want int
	}{
		{nil, 0},
		{errTimeout, 1},
		{errors.New("boom"), 2},
		{&bus.Error{Code: bus.CodeSessionLapsed}, 2},
		{errReplaced, 3},
		{&bus.Error{Code: bus.CodeReplaced}, 3},
		{errHarnessGone, 4},
	} {
		if got := exitCode(c.err); got != c.want {
			t.Errorf("exitCode(%v) = %d, want %d", c.err, got, c.want)
		}
	}
	// cancelCause: a live context leaves the error alone; a canceled one reads
	// the cause, ignoring the (wrapped transport) error.
	live := context.Background()
	boom := errors.New("boom")
	if cancelCause(live, boom) != boom {
		t.Error("live context changed the error")
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(errHarnessGone)
	if cancelCause(ctx, boom) != errHarnessGone {
		t.Error("harness cause not mapped")
	}
	ctx2, cancel2 := context.WithCancelCause(context.Background())
	cancel2(nil)
	if cancelCause(ctx2, boom) != errReplaced {
		t.Error("nil cause not mapped to replaced")
	}
}

// lapsingSessions answers every Watch with the scripted results in order (the
// last one repeating) and counts the resumes; any other method panics.
type lapsingSessions struct {
	bus.Sessions
	mu      sync.Mutex
	script  []error // nil: a successful Woke-false Watch
	watches int
	opens   []string
}

func (s *lapsingSessions) Watch(ctx context.Context, in bus.WatchInput) (bus.WatchResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := min(s.watches, len(s.script)-1)
	s.watches++
	if s.watches > 1000 {
		return bus.WatchResult{}, errors.New("runaway re-arm")
	}
	return bus.WatchResult{}, s.script[i]
}

func (s *lapsingSessions) Open(ctx context.Context, in bus.OpenSessionInput) (bus.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.opens = append(s.opens, in.ResumeToken)
	return bus.Session{}, nil
}

// TestResumeOncePerLapse: a session that answers session_lapsed again right
// after a successful resume is resumed once, not in a loop, and the command
// ends with session_lapsed (exit 2); a successful Watch between two lapses
// allows the second resume.
func TestResumeOncePerLapse(t *testing.T) {
	lapsed := &bus.Error{Code: bus.CodeSessionLapsed}
	l := located{Session: "s1", ResumeToken: "tok", FromFile: true}
	runs := map[string]func(s bus.Sessions) error{
		"watch": func(s bus.Sessions) error {
			return watchResuming(context.Background(), s, l, "pat", false, io.Discard)
		},
		"wait": func(s bus.Sessions) error {
			return waitResuming(context.Background(), s, l, false, 0, io.Discard)
		},
	}
	for name, run := range runs {
		s := &lapsingSessions{script: []error{lapsed}}
		err := run(s)
		if bus.CodeOf(err) != bus.CodeSessionLapsed || exitCode(err) != 2 || len(s.opens) != 1 || s.opens[0] != "tok" {
			t.Errorf("%s, lapsed forever: err %v, %d opens, want one resume and session_lapsed", name, err, len(s.opens))
		}
		s = &lapsingSessions{script: []error{lapsed, nil, lapsed}}
		err = run(s)
		if bus.CodeOf(err) != bus.CodeSessionLapsed || len(s.opens) != 2 {
			t.Errorf("%s, lapse, success, lapse: err %v, %d opens, want two resumes", name, err, len(s.opens))
		}
		// No resume token: no resume at all.
		s = &lapsingSessions{script: []error{lapsed}}
		var err2 error
		if name == "watch" {
			err2 = watchResuming(context.Background(), s, located{Session: "s1"}, "pat", false, io.Discard)
		} else {
			err2 = waitResuming(context.Background(), s, located{Session: "s1"}, false, 0, io.Discard)
		}
		if bus.CodeOf(err2) != bus.CodeSessionLapsed || len(s.opens) != 0 {
			t.Errorf("%s, no token: err %v, %d opens", name, err2, len(s.opens))
		}
	}
}
