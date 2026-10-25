package cli

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/auth/keys"
	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/client"
	"github.com/ericfitz/agentbus2/internal/credential"
	"github.com/ericfitz/agentbus2/internal/profile"
	"github.com/ericfitz/agentbus2/internal/sessionstate"
)

func TestWaitWokeExits0(t *testing.T) {
	waitSessionFor := func(t *testing.T, e *testEnv, tune ...func(*client.Options)) bus.Session {
		t.Helper()
		sess, err := e.conn(t, "pat", tune...).Bus.Open(context.Background(), bus.OpenSessionInput{})
		if err != nil {
			t.Fatal(err)
		}
		e.writeState(t, sessionstate.File{Session: sess.ID, Identity: "pat", Profile: "default", DisplayName: sess.DisplayName, ResumeToken: sess.ResumeToken,
			HarnessPID: testHarness.Pid, HarnessStart: testHarness.Start, Primary: true, OpenedAt: 1})
		return sess
	}
	e := newTestEnv(t)
	ctx := context.Background()
	waitSessionFor(t, e)
	sam := e.conn(t, "sam")

	var so, se syncBuf
	done := make(chan int, 1)
	go func() { done <- RunWait(ctx, nil, e.Environ, strings.NewReader(""), &so, &se) }()
	time.Sleep(300 * time.Millisecond)
	sentAt := time.Now()
	if _, err := sam.Bus.Send(ctx, bus.SendInput{Channel: "general", Content: "hi"}); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-done:
		if code != 0 || se.String() != "" {
			t.Fatalf("exit %d stderr %q", code, se.String())
		}
		// 3 s, not 1 (see TestWatchWakesAcrossProcesses); the Conns poll every
		// 50 ms and RunWait has no timeout, so the guard below is the only
		// other bound.
		if d := time.Since(sentAt); d > 3*time.Second {
			t.Fatalf("woke %v after the send", d)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("wait did not wake")
	}
	var w struct {
		Seq      int64    `json:"seq"`
		Count    int      `json:"count"`
		Channels []string `json:"channels"`
	}
	if strings.Count(so.String(), "\n") != 1 || json.Unmarshal([]byte(so.String()), &w) != nil ||
		w.Seq < 1 || w.Count != 1 || len(w.Channels) != 1 || w.Channels[0] != "general" {
		t.Fatalf("stdout %q", so.String())
	}
}

func TestWaitTimeoutExits1(t *testing.T) {
	waitSessionFor := func(t *testing.T, e *testEnv, tune ...func(*client.Options)) bus.Session {
		t.Helper()
		sess, err := e.conn(t, "pat", tune...).Bus.Open(context.Background(), bus.OpenSessionInput{})
		if err != nil {
			t.Fatal(err)
		}
		e.writeState(t, sessionstate.File{Session: sess.ID, Identity: "pat", Profile: "default", DisplayName: sess.DisplayName, ResumeToken: sess.ResumeToken,
			HarnessPID: testHarness.Pid, HarnessStart: testHarness.Start, Primary: true, OpenedAt: 1})
		return sess
	}
	e := newTestEnv(t)
	waitSessionFor(t, e)
	var so, se syncBuf
	start := time.Now()
	if code := RunWait(context.Background(), []string{"-timeout", "300ms"}, e.Environ, strings.NewReader(""), &so, &se); code != 1 {
		t.Fatalf("exit %d stderr %q", code, se.String())
	}
	if so.String() != "" || se.String() != "" {
		t.Fatalf("stdout %q stderr %q", so.String(), se.String())
	}
	if d := time.Since(start); d < 250*time.Millisecond || d > 3*time.Second {
		t.Fatalf("took %v", d)
	}
}

func TestWaitFlags(t *testing.T) {
	e := newTestEnv(t)
	for _, args := range [][]string{{"-channel", "x"}, {"-filter", "x"}, {"-timeout", "-1s"}, {"-timeout", "soon"}, {"extra"}} {
		var so, se syncBuf
		if code := RunWait(context.Background(), args, e.Environ, strings.NewReader(""), &so, &se); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
	var so, se syncBuf
	if code := RunWait(context.Background(), []string{"-h"}, e.Environ, strings.NewReader(""), &so, &se); code != 0 {
		t.Errorf("-h: exit %d", code)
	}
}

func TestWaitReplacedExits3(t *testing.T) {
	waitSessionFor := func(t *testing.T, e *testEnv, tune ...func(*client.Options)) bus.Session {
		t.Helper()
		sess, err := e.conn(t, "pat", tune...).Bus.Open(context.Background(), bus.OpenSessionInput{})
		if err != nil {
			t.Fatal(err)
		}
		e.writeState(t, sessionstate.File{Session: sess.ID, Identity: "pat", Profile: "default", DisplayName: sess.DisplayName, ResumeToken: sess.ResumeToken,
			HarnessPID: testHarness.Pid, HarnessStart: testHarness.Start, Primary: true, OpenedAt: 1})
		return sess
	}
	e := newTestEnv(t)
	ctx := context.Background()
	waitSessionFor(t, e)
	var so1, se1, so2, se2 syncBuf
	c1, c2 := make(chan int, 1), make(chan int, 1)
	go func() { c1 <- RunWait(ctx, nil, e.Environ, strings.NewReader(""), &so1, &se1) }()
	time.Sleep(400 * time.Millisecond)
	ctx2, cancel2 := context.WithCancel(ctx)
	defer cancel2()
	go func() { c2 <- RunWait(ctx2, nil, e.Environ, strings.NewReader(""), &so2, &se2) }()
	select {
	case code := <-c1:
		if code != 3 || so1.String() != "" || se1.String() != "" {
			t.Fatalf("exit %d stdout %q stderr %q", code, so1.String(), se1.String())
		}
	case <-time.After(time.Second):
		t.Fatal("not replaced within 1 s")
	}
	// A canceled context (SIGTERM's path) is also 3.
	cancel2()
	if code := <-c2; code != 3 {
		t.Fatalf("canceled: exit %d", code)
	}
}

func TestWaitHarnessGoneExits4(t *testing.T) {
	waitSessionFor := func(t *testing.T, e *testEnv, tune ...func(*client.Options)) bus.Session {
		t.Helper()
		sess, err := e.conn(t, "pat", tune...).Bus.Open(context.Background(), bus.OpenSessionInput{})
		if err != nil {
			t.Fatal(err)
		}
		e.writeState(t, sessionstate.File{Session: sess.ID, Identity: "pat", Profile: "default", DisplayName: sess.DisplayName, ResumeToken: sess.ResumeToken,
			HarnessPID: testHarness.Pid, HarnessStart: testHarness.Start, Primary: true, OpenedAt: 1})
		return sess
	}
	e := newTestEnv(t)
	waitSessionFor(t, e)
	var so, se syncBuf
	done := make(chan int, 1)
	go func() { done <- RunWait(context.Background(), nil, e.Environ, strings.NewReader(""), &so, &se) }()
	time.Sleep(300 * time.Millisecond)
	processTable.(*fakeTable).setGone(10)
	select {
	case code := <-done:
		if code != 4 || so.String() != "" || se.String() != "" {
			t.Fatalf("exit %d stdout %q stderr %q", code, so.String(), se.String())
		}
	case <-time.After(time.Second):
		t.Fatal("no exit 4")
	}
}

func TestWaitErrorExits2(t *testing.T) {
	e := newTestEnv(t)
	e.writeProfile(t, profile.Profile{Name: "lab", Mode: profile.ModeServer, Server: "http://127.0.0.1:1", Identity: "pat"})
	key, err := keys.Generate(keys.EdDSA)
	if err != nil {
		t.Fatal(err)
	}
	if err := (credential.Store{Dir: profile.CredentialDir(e.ConfigDir, "lab")}).Save("pat", key); err != nil {
		t.Fatal(err)
	}
	var so, se syncBuf
	if code := RunWait(context.Background(), []string{"-profile", "lab", "-session", "s9"}, e.Environ, strings.NewReader(""), &so, &se); code != 2 ||
		!strings.HasPrefix(se.String(), "agentbus2:") {
		t.Fatalf("exit %d stderr %q", code, se.String())
	}
	// No session for the harness and no -session.
	var so2, se2 syncBuf
	if code := RunWait(context.Background(), nil, e.Environ, strings.NewReader(""), &so2, &se2); code != 2 || !strings.Contains(se2.String(), "-session") {
		t.Fatalf("exit %d stderr %q", code, se2.String())
	}
	// A session the bus does not know is a bus error: 2, not 1.
	var so3, se3 syncBuf
	if code := RunWait(context.Background(), []string{"-session", "nosuch"}, e.Environ, strings.NewReader(""), &so3, &se3); code != 2 ||
		!strings.Contains(se3.String(), "not_found") {
		t.Fatalf("unknown session: exit %d stderr %q", code, se3.String())
	}
}

func TestWaitSessionFromFlag(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	sess, err := e.conn(t, "pat").Bus.Open(ctx, bus.OpenSessionInput{})
	if err != nil {
		t.Fatal(err)
	}
	sam := e.conn(t, "sam")
	processTable.(*fakeTable).setError(42, errors.New("unsupported"))

	var so, se syncBuf
	done := make(chan int, 1)
	go func() {
		done <- RunWait(ctx, []string{"-session", sess.ID}, e.Environ, strings.NewReader(""), &so, &se)
	}()
	time.Sleep(300 * time.Millisecond)
	if _, err := sam.Bus.Send(ctx, bus.SendInput{Channel: "general", Content: "hi"}); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-done:
		if code != 0 || !strings.Contains(so.String(), `"count":1`) {
			t.Fatalf("exit %d stdout %q stderr %q", code, so.String(), se.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no wake")
	}

	// An orphan: ErrGone for the command's own pid is exit 4 at once.
	processTable.(*fakeTable).setError(42, nil)
	processTable.(*fakeTable).setGone(42)
	var so2, se2 syncBuf
	if code := RunWait(ctx, []string{"-session", sess.ID}, e.Environ, strings.NewReader(""), &so2, &se2); code != 4 || so2.String() != "" || se2.String() != "" {
		t.Fatalf("orphan: exit %d stdout %q stderr %q", code, so2.String(), se2.String())
	}
}

func TestWaitResumesLapsedSession(t *testing.T) {
	waitSessionFor := func(t *testing.T, e *testEnv, tune ...func(*client.Options)) bus.Session {
		t.Helper()
		sess, err := e.conn(t, "pat", tune...).Bus.Open(context.Background(), bus.OpenSessionInput{})
		if err != nil {
			t.Fatal(err)
		}
		e.writeState(t, sessionstate.File{Session: sess.ID, Identity: "pat", Profile: "default", DisplayName: sess.DisplayName, ResumeToken: sess.ResumeToken,
			HarnessPID: testHarness.Pid, HarnessStart: testHarness.Start, Primary: true, OpenedAt: 1})
		return sess
	}
	e := newTestEnv(t)
	ctx := context.Background()
	tenMinutesAgo := func() time.Time { return time.Now().Add(-10 * time.Minute) }
	waitSessionFor(t, e, func(o *client.Options) { o.Now = tenMinutesAgo })
	sam := e.conn(t, "sam")

	var so, se syncBuf
	done := make(chan int, 1)
	go func() { done <- RunWait(ctx, nil, e.Environ, strings.NewReader(""), &so, &se) }()
	time.Sleep(300 * time.Millisecond)
	if _, err := sam.Bus.Send(ctx, bus.SendInput{Channel: "general", Content: "hi"}); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-done:
		if code != 0 || !strings.Contains(so.String(), `"count":1`) {
			t.Fatalf("exit %d stdout %q stderr %q", code, so.String(), se.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("no wake; stderr %q", se.String())
	}

	// A bad resume token in the state file is an error, not a loop.
	e2 := newTestEnv(t)
	sess := waitSessionFor(t, e2, func(o *client.Options) { o.Now = tenMinutesAgo })
	e2.writeState(t, sessionstate.File{Session: sess.ID, Identity: "pat", Profile: "default", DisplayName: sess.DisplayName, ResumeToken: "bad",
		HarnessPID: testHarness.Pid, HarnessStart: testHarness.Start, Primary: true, OpenedAt: 1})
	var so2, se2 syncBuf
	if code := RunWait(ctx, nil, e2.Environ, strings.NewReader(""), &so2, &se2); code != 2 {
		t.Fatalf("bad token: exit %d stderr %q", code, se2.String())
	}
}
