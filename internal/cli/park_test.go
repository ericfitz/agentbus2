package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/sessionstate"
)

// parkSession opens a session for pat on general and writes its state file.
func parkSession(t *testing.T, e *testEnv) bus.Session {
	t.Helper()
	sess, err := e.conn(t, "pat").Bus.Open(context.Background(), bus.OpenSessionInput{})
	if err != nil {
		t.Fatal(err)
	}
	e.writeState(t, sessionstate.File{Session: sess.ID, Identity: "pat", Profile: "default", DisplayName: sess.DisplayName, ResumeToken: sess.ResumeToken,
		HarnessPID: testHarness.Pid, HarnessStart: testHarness.Start, HarnessClient: "claude-code", Primary: true, OpenedAt: 1})
	return sess
}

func runStop(t *testing.T, e *testEnv) string {
	t.Helper()
	var so, se syncBuf
	if code := RunStopHook(context.Background(), nil, e.Environ, strings.NewReader(`{"cwd":""}`), &so, &se); code != 0 || se.String() != "" {
		t.Fatalf("stop hook: exit %d stderr %q", code, se.String())
	}
	return so.String()
}

func TestParkSilencesStopHookAndUnparkRestores(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	sess := parkSession(t, e)
	if _, err := e.conn(t, "sam").Bus.Send(ctx, bus.SendInput{Channel: "general", Content: "hi"}); err != nil {
		t.Fatal(err)
	}
	if out := runStop(t, e); !strings.Contains(out, `"block"`) {
		t.Fatalf("before park: %q", out)
	}
	for i := 0; i < 2; i++ { // twice: a no-op that succeeds
		var so, se syncBuf
		if code := RunPark(ctx, nil, e.Environ, strings.NewReader(""), &so, &se); code != 0 {
			t.Fatalf("park: exit %d stderr %q", code, se.String())
		}
	}
	if f, _ := sessionstate.Read(e.DataDir, sess.ID); !f.Parked {
		t.Fatal("the state file is not parked")
	}
	if out := runStop(t, e); out != "" {
		t.Fatalf("parked, messages pending: stop hook printed %q", out)
	}
	for i := 0; i < 2; i++ {
		var so, se syncBuf
		if code := RunUnpark(ctx, nil, e.Environ, strings.NewReader(`{"prompt":"hi"}`), &so, &se); code != 0 || so.String() != "" || se.String() != "" {
			t.Fatalf("unpark: exit %d stdout %q stderr %q", code, so.String(), se.String())
		}
	}
	if out := runStop(t, e); !strings.Contains(out, `"block"`) {
		t.Fatalf("after unpark: %q", out)
	}
}

func TestParkWithoutSessionExits2(t *testing.T) {
	e := newTestEnv(t)
	var so, se syncBuf
	if code := RunPark(context.Background(), nil, e.Environ, strings.NewReader(""), &so, &se); code != 2 || se.String() == "" {
		t.Fatalf("no state file: exit %d stderr %q", code, se.String())
	}
	var so2, se2 syncBuf
	if code := RunPark(context.Background(), []string{"-session", "nope"}, e.Environ, strings.NewReader(""), &so2, &se2); code != 2 || se2.String() == "" {
		t.Fatalf("-session nope: exit %d stderr %q", code, se2.String())
	}
	if code := RunPark(context.Background(), []string{"extra"}, e.Environ, strings.NewReader(""), &so2, &se2); code != 2 {
		t.Fatalf("extra argument: exit %d", code)
	}
}

func TestUnparkIsSilentAndExits0Always(t *testing.T) {
	e := newTestEnv(t)
	run := func(label string, args ...string) {
		t.Helper()
		var so, se syncBuf
		if code := RunUnpark(context.Background(), args, e.Environ, strings.NewReader("not json"), &so, &se); code != 0 || so.String() != "" || se.String() != "" {
			t.Fatalf("%s: exit %d stdout %q stderr %q", label, code, so.String(), se.String())
		}
	}
	logLines := func() string {
		b, _ := os.ReadFile(filepath.Join(e.DataDir, "logs", "hook.log"))
		return string(b)
	}
	run("no state file")
	if l := logLines(); l != "" {
		t.Fatalf("no session for the harness is not a failure, but hook.log has %q", l)
	}
	run("-session without a file", "-session", "nope")
	run("a bad flag", "-nope")
	if err := os.MkdirAll(sessionstate.Dir(e.DataDir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionstate.Dir(e.DataDir), "bad.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("an unreadable file", "-session", "bad")
	if logLines() == "" {
		t.Fatal("a real failure left no line in hook.log")
	}
	if err := os.Remove(filepath.Join(e.DataDir, "logs", "hook.log")); err != nil {
		t.Fatal(err)
	}
	processTable = newFakeTable() // no harness found: errHarnessGone
	run("no harness")
	if l := logLines(); l != "" {
		t.Fatalf("a gone harness is not a failure, but hook.log has %q", l)
	}
}

func TestWaitStartedBeforeParkDoesNotWake(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	parkSession(t, e)
	var so, se syncBuf
	done := make(chan int, 1)
	go func() {
		done <- RunWait(ctx, []string{"-timeout", "3s"}, e.Environ, strings.NewReader(""), &so, &se)
	}()
	time.Sleep(300 * time.Millisecond)
	var pso, pse syncBuf
	if code := RunPark(ctx, nil, e.Environ, strings.NewReader(""), &pso, &pse); code != 0 {
		t.Fatalf("park: %d %q", code, pse.String())
	}
	if _, err := e.conn(t, "sam").Bus.Send(ctx, bus.SendInput{Channel: "general", Content: "hi"}); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-done:
		if code != 1 || so.String() != "" {
			t.Fatalf("exit %d stdout %q stderr %q, want 1 and nothing", code, so.String(), se.String())
		}
	case <-time.After(6 * time.Second):
		t.Fatal("wait did not time out")
	}
}

func TestWatchStartedBeforeParkPrintsNothing(t *testing.T) {
	e := newTestEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	parkSession(t, e)
	var so, se syncBuf
	done := make(chan int, 1)
	go func() { done <- RunWatch(ctx, nil, e.Environ, strings.NewReader(""), &so, &se) }()
	time.Sleep(300 * time.Millisecond)
	var pso, pse syncBuf
	if code := RunPark(context.Background(), nil, e.Environ, strings.NewReader(""), &pso, &pse); code != 0 {
		t.Fatalf("park: %d %q", code, pse.String())
	}
	if _, err := e.conn(t, "sam").Bus.Send(context.Background(), bus.SendInput{Channel: "general", Content: "hi"}); err != nil {
		t.Fatal(err)
	}
	<-done
	if so.String() != "" {
		t.Fatalf("a parked watch printed %q", so.String())
	}
}

func TestInitListsUnparkHookForEveryHarness(t *testing.T) {
	for _, h := range []string{"claude", "codex", "grok"} {
		e := newTestEnv(t)
		var out bytes.Buffer
		o := InitOptions{Global: true, Harness: h, DryRun: true, Home: e.Home, Cwd: t.TempDir(), Environ: e.Environ, OS: "linux",
			LookPath: func(n string) (string, error) { return "/fake/" + n, nil }}
		if err := Init(o, &out); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "UserPromptSubmit hook: agentbus2 unpark") {
			t.Errorf("%s dry run lacks the UserPromptSubmit hook:\n%s", h, out.String())
		}
	}
}

func TestUnparkDrainsLargeStdin(t *testing.T) {
	e := newTestEnv(t)
	sess := parkSession(t, e)
	if err := sessionstate.SetParked(e.DataDir, sess.ID, true); err != nil {
		t.Fatal(err)
	}
	r := &countReader{n: 200 << 10}
	var so, se syncBuf
	if code := RunUnpark(context.Background(), nil, e.Environ, r, &so, &se); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if r.n != 0 {
		t.Fatalf("%d bytes of stdin left unread", r.n)
	}
	if f, _ := sessionstate.Read(e.DataDir, sess.ID); f.Parked {
		t.Fatal("still parked")
	}
}

type countReader struct{ n int }

func (c *countReader) Read(p []byte) (int, error) {
	if c.n == 0 {
		return 0, io.EOF
	}
	k := min(len(p), c.n)
	c.n -= k
	return k, nil
}
