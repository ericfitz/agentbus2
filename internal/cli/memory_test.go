package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/sessionstate"
)

// Decision 20: agents never receive memory channels. The commands below are
// the agent-facing ones; the Bus itself (the TUI's path) still delivers them.

// memoryEnv is a test environment with a memory channel "mem", an open
// session for pat with its state file, and a tag subscription "x" so that a
// tagged memory reaches pat.
func memoryEnv(t *testing.T) (*testEnv, bus.Session, bus.Bus) {
	t.Helper()
	e := newTestEnv(t)
	ctx := context.Background()
	pat := e.conn(t, "pat")
	if _, err := pat.Bus.CreateChannel(ctx, bus.CreateChannelInput{Name: "mem", Kind: bus.KindMemory}); err != nil {
		t.Fatal(err)
	}
	if err := pat.Bus.SubscribeTags(ctx, bus.TagSubscribeInput{Patterns: []string{"x"}}); err != nil {
		t.Fatal(err)
	}
	sess, err := pat.Bus.Open(ctx, bus.OpenSessionInput{})
	if err != nil {
		t.Fatal(err)
	}
	e.writeState(t, sessionstate.File{Session: sess.ID, Identity: "pat", Profile: "default", DisplayName: sess.DisplayName, ResumeToken: sess.ResumeToken,
		HarnessPID: testHarness.Pid, HarnessStart: testHarness.Start, Primary: true, OpenedAt: 1})
	return e, sess, e.conn(t, "sam").Bus
}

func sendMemory(t *testing.T, sam bus.Bus) {
	t.Helper()
	if _, err := sam.Send(context.Background(), bus.SendInput{Channel: "mem", Content: "m", Tags: []string{"x"}}); err != nil {
		t.Fatal(err)
	}
}

func TestChannelsSubscribeRefusesMemory(t *testing.T) {
	e, _, _ := memoryEnv(t)
	var o, er bytes.Buffer
	code := RunChannels(context.Background(), []string{"subscribe", "mem"}, e.Environ, strings.NewReader(""), &o, &er)
	if code != 1 || !strings.Contains(er.String(), "validation") || !strings.Contains(er.String(), "search") || !strings.Contains(er.String(), "get_memory") {
		t.Fatalf("exit %d stderr %q", code, er.String())
	}
	o.Reset()
	er.Reset()
	if code := RunChannels(context.Background(), []string{"subscribe", "general"}, e.Environ, strings.NewReader(""), &o, &er); code != 0 {
		t.Fatalf("ordinary channel: exit %d %q", code, er.String())
	}
}

func TestWaitIgnoresAMemoryOnlyWake(t *testing.T) {
	e, _, sam := memoryEnv(t)
	sendMemory(t, sam)
	var so, se syncBuf
	start := time.Now()
	code := RunWait(context.Background(), []string{"-timeout", "700ms"}, e.Environ, strings.NewReader(""), &so, &se)
	if code != 1 || so.String() != "" || se.String() != "" {
		t.Fatalf("exit %d stdout %q stderr %q, want a silent timeout", code, so.String(), se.String())
	}
	if time.Since(start) < 600*time.Millisecond {
		t.Fatalf("returned after %v", time.Since(start))
	}
	// A later ordinary message still wakes it, and names only that channel.
	go func() {
		time.Sleep(300 * time.Millisecond)
		_, _ = sam.Send(context.Background(), bus.SendInput{Channel: "general", Content: "hi"})
	}()
	code = RunWait(context.Background(), []string{"-timeout", "5s"}, e.Environ, strings.NewReader(""), &so, &se)
	var w waitWake
	if code != 0 || json.Unmarshal([]byte(so.String()), &w) != nil || len(w.Channels) != 1 || w.Channels[0] != "general" {
		t.Fatalf("exit %d stdout %q stderr %q", code, so.String(), se.String())
	}
}

func TestStopHookIgnoresMemory(t *testing.T) {
	e, _, sam := memoryEnv(t)
	ctx := context.Background()
	hook := func() string {
		var so, se syncBuf
		RunStopHook(ctx, nil, e.Environ, strings.NewReader(`{}`), &so, &se)
		return so.String()
	}
	sendMemory(t, sam)
	if got := hook(); got != "" {
		t.Fatalf("a memory-only backlog blocked the stop: %q", got)
	}
	if _, err := sam.Send(ctx, bus.SendInput{Channel: "general", Content: "hi"}); err != nil {
		t.Fatal(err)
	}
	got := hook()
	if !strings.Contains(got, "1 new message(s)") || strings.Contains(got, "mem") {
		t.Fatalf("mixed backlog: %q", got)
	}
}

func TestReceiveWithSessionDropsMemoryAndAcks(t *testing.T) {
	e, sess, sam := memoryEnv(t)
	ctx := context.Background()
	sendMemory(t, sam)
	run := func(args ...string) struct {
		Messages []bus.Message
		Batch    string
	} {
		t.Helper()
		var o, er bytes.Buffer
		if code := RunReceive(ctx, append(args, "-session", sess.ID), e.Environ, strings.NewReader(""), &o, &er); code != 0 {
			t.Fatalf("receive: %d %s", code, er.String())
		}
		var r struct {
			Messages []bus.Message
			Batch    string
		}
		if err := json.Unmarshal(o.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	if r := run(); len(r.Messages) != 0 || r.Batch != "" {
		t.Fatalf("memory delivered or left unacked: %+v", r)
	}
	if r := run(); len(r.Messages) != 0 {
		t.Fatalf("memory came back: %+v", r)
	}
	// The Bus itself, which the TUI uses, still delivers memories.
	sendMemory(t, sam)
	pat := e.conn(t, "pat")
	res, err := pat.Bus.Receive(ctx, bus.ReceiveInput{Session: sess.ID})
	if err != nil || len(res.Messages) != 1 || res.Messages[0].Channel != "mem" {
		t.Fatalf("bus receive: %+v %v", res, err)
	}
}

func TestWatchLoopIgnoresMemoryWakes(t *testing.T) {
	prev := watchMinGap
	watchMinGap = 10 * time.Millisecond
	t.Cleanup(func() { watchMinGap = prev })
	e, sess, sam := memoryEnv(t)
	pat := e.conn(t, "pat")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var so syncBuf
	done := make(chan int64, 1)
	go func() {
		f, _ := watchLoop(ctx, pat.Bus, newMemoryFilter(pat.Bus), sess.ID, "pat", 0, false, &so)
		done <- f
	}()
	time.Sleep(200 * time.Millisecond)
	sendMemory(t, sam)
	time.Sleep(400 * time.Millisecond)
	if so.String() != "" {
		t.Fatalf("a memory-only wake printed %q", so.String())
	}
	if _, err := sam.Send(context.Background(), bus.SendInput{Channel: "general", Content: "hi"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	cancel()
	floor := <-done
	if out := so.String(); !strings.Contains(out, "on general;") || strings.Contains(out, "mem") || floor < 2 {
		t.Fatalf("floor %d, output %q", floor, out)
	}
}

// A wake that had memory channels filtered out says nothing true about how
// many messages are left, so wait omits count and watch prints none.
func TestFilteredWakeHasNoCount(t *testing.T) {
	e, sess, sam := memoryEnv(t)
	ctx := context.Background()
	sendMemory(t, sam)
	if _, err := sam.Send(ctx, bus.SendInput{Channel: "general", Content: "hi"}); err != nil {
		t.Fatal(err)
	}
	var so, se syncBuf
	if code := RunWait(ctx, []string{"-timeout", "5s"}, e.Environ, strings.NewReader(""), &so, &se); code != 0 {
		t.Fatalf("exit %d %q", code, se.String())
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(so.String()), &raw); err != nil {
		t.Fatal(err)
	}
	if _, has := raw["count"]; has || len(raw["channels"].([]any)) != 1 {
		t.Fatalf("wait line %q, want channels [general] and no count", so.String())
	}

	prev := watchMinGap
	watchMinGap = 10 * time.Millisecond
	t.Cleanup(func() { watchMinGap = prev })
	pat := e.conn(t, "pat")
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wo syncBuf
	done := make(chan struct{})
	go func() {
		_, _ = watchLoop(wctx, pat.Bus, newMemoryFilter(pat.Bus), sess.ID, "pat", 0, false, &wo)
		close(done)
	}()
	time.Sleep(300 * time.Millisecond)
	cancel()
	<-done
	if out := wo.String(); !strings.Contains(out, "agentbus2: new message(s) for pat on general;") {
		t.Fatalf("watch line %q", out)
	}
}
