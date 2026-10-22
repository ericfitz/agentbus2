package mcpserver_test

import (
	"context"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/bus"
)

// Decision 20: agents never receive memory channels.

func TestSubscribeRefusesMemoryChannel(t *testing.T) {
	e := newTestEnv(t)
	r := startServer(t, e, newFakeTable(), "test-client")
	r.must(t, "register", nil)
	r.must(t, "create_channel", map[string]any{"name": "mem", "kind": "memory"})
	be := r.failure(t, "subscribe", map[string]any{"channel": "mem"}, bus.CodeValidation)
	if !strings.Contains(be.Message, "search") || !strings.Contains(be.Message, "get_memory") {
		t.Fatalf("message does not point at search and get_memory: %q", be.Message)
	}
	// Tasks and ordinary channels stay subscribable.
	r.must(t, "create_channel", map[string]any{"name": "work", "kind": "tasks"})
	r.must(t, "subscribe", map[string]any{"channel": "work"})
	r.must(t, "subscribe", map[string]any{"channel": "general"})
}

func hasPending(pending []bus.PendingChannel, ch string) bool {
	for _, p := range pending {
		if p.Channel == ch {
			return true
		}
	}
	return false
}

func TestRegisterDropsMemorySubscriptions(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	pat := e.conn(t, "pat")
	if _, err := pat.Bus.CreateChannel(ctx, bus.CreateChannelInput{Name: "mem", Kind: bus.KindMemory}); err != nil {
		t.Fatal(err)
	}
	for _, ch := range []string{"mem", "general"} {
		if err := pat.Bus.Subscribe(ctx, bus.SubscribeInput{Channel: ch}); err != nil {
			t.Fatal(err)
		}
	}
	r := startServer(t, e, newFakeTable(), "test-client")
	reg := r.must(t, "register", nil)
	for _, p := range arr(reg, "pending") {
		if str(p.(map[string]any), "channel") == "mem" {
			t.Fatalf("register reports the memory channel: %v", reg)
		}
	}
	s, err := pat.Bus.Open(ctx, bus.OpenSessionInput{Hint: "probe"})
	if err != nil {
		t.Fatal(err)
	}
	if hasPending(s.Pending, "mem") || !hasPending(s.Pending, "general") {
		t.Fatalf("subscriptions after register: %v", s.Pending)
	}
}

func TestReceiveFiltersTagMatchedMemoryAndAcks(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	r := startServer(t, e, newFakeTable(), "test-client")
	sid := str(r.must(t, "register", nil), "session")
	r.must(t, "create_channel", map[string]any{"name": "mem", "kind": "memory"})
	r.must(t, "subscribe", map[string]any{"tags": []any{"env:prod"}})
	sam := e.conn(t, "sam")
	if _, err := sam.Bus.Send(ctx, bus.SendInput{Channel: "mem", Content: "a memory", Tags: []string{"env:prod"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := sam.Bus.Send(ctx, bus.SendInput{Channel: "general", Content: "chat", Tags: []string{"env:prod"}}); err != nil {
		t.Fatal(err)
	}
	got := r.must(t, "receive", map[string]any{"session": sid})
	msgs := arr(got, "messages")
	if len(msgs) != 1 || str(msgs[0].(map[string]any), "channel") != "general" {
		t.Fatalf("want only the general message: %v", got)
	}
	got = r.must(t, "receive", map[string]any{"session": sid, "ack": str(got, "batch")})
	if len(arr(got, "messages")) != 0 {
		t.Fatalf("the memory came back after the ack: %v", got)
	}
}

func TestReceiveWithOnlyMemoryPendingStillAcks(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	r := startServer(t, e, newFakeTable(), "test-client")
	sid := str(r.must(t, "register", nil), "session")
	r.must(t, "create_channel", map[string]any{"name": "mem", "kind": "memory"})
	r.must(t, "subscribe", map[string]any{"tags": []any{"x"}})
	sam := e.conn(t, "sam")
	if _, err := sam.Bus.Send(ctx, bus.SendInput{Channel: "mem", Content: "m", Tags: []string{"x"}}); err != nil {
		t.Fatal(err)
	}
	got := r.must(t, "receive", map[string]any{"session": sid})
	if len(arr(got, "messages")) != 0 {
		t.Fatalf("memory delivered: %v", got)
	}
	// The unacked batch would redeliver; acking it must leave nothing behind.
	got = r.must(t, "receive", map[string]any{"session": sid, "ack": str(got, "batch")})
	if len(arr(got, "messages")) != 0 {
		t.Fatalf("memory redelivered: %v", got)
	}
	got = r.must(t, "receive", map[string]any{"session": sid})
	if len(arr(got, "messages")) != 0 {
		t.Fatalf("memory came back: %v", got)
	}
}

// The TUI path is the bus itself: a tag subscription still sees memories.
func TestBusStillDeliversMemoryToObservers(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	pat := e.conn(t, "pat")
	if _, err := pat.Bus.CreateChannel(ctx, bus.CreateChannelInput{Name: "mem", Kind: bus.KindMemory}); err != nil {
		t.Fatal(err)
	}
	if err := pat.Bus.Subscribe(ctx, bus.SubscribeInput{Channel: "mem"}); err != nil {
		t.Fatal(err)
	}
	s, err := pat.Bus.Open(ctx, bus.OpenSessionInput{Hint: "tui"})
	if err != nil {
		t.Fatal(err)
	}
	sam := e.conn(t, "sam")
	if _, err := sam.Bus.Send(ctx, bus.SendInput{Channel: "mem", Content: "m"}); err != nil {
		t.Fatal(err)
	}
	res, err := pat.Bus.Receive(ctx, bus.ReceiveInput{Session: s.ID})
	if err != nil || len(res.Messages) != 1 {
		t.Fatalf("observer receive: %v %v", res, err)
	}
}
