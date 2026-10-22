package agentview

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/bus"
)

type fakeMessaging struct {
	bus.Messaging
	chans []bus.Channel
	calls int
	err   error
}

func (f *fakeMessaging) ListChannels(context.Context) ([]bus.Channel, error) {
	f.calls++
	return f.chans, f.err
}

func TestKindIndexCachesAndRefreshesOnMiss(t *testing.T) {
	f := &fakeMessaging{chans: []bus.Channel{{Name: "mem", Kind: bus.KindMemory}, {Name: "general", Kind: bus.KindOrdinary}, {Name: "work", Kind: bus.KindTasks}}}
	k := NewKindIndex()
	ctx := context.Background()
	for name, want := range map[string]bool{"mem": true, "general": false, "work": false} {
		got, err := k.IsMemory(ctx, f, name)
		if err != nil || got != want {
			t.Fatalf("IsMemory(%s) = %v, %v", name, got, err)
		}
	}
	if f.calls != 1 {
		t.Fatalf("ListChannels called %d times, want 1 (cached)", f.calls)
	}
	f.chans = append(f.chans, bus.Channel{Name: "mem2", Kind: bus.KindMemory})
	if got, _ := k.IsMemory(ctx, f, "mem2"); !got || f.calls != 2 {
		t.Fatalf("a new name must refresh: got %v after %d calls", got, f.calls)
	}
	// A name that does not exist is not a memory channel.
	if got, _ := k.IsMemory(ctx, f, "gone"); got {
		t.Fatal("unknown channel counted as memory")
	}
}

func TestKindIndexTTLAndErrors(t *testing.T) {
	f := &fakeMessaging{chans: []bus.Channel{{Name: "c", Kind: bus.KindOrdinary}}}
	k := NewKindIndex()
	now := time.Unix(1000, 0)
	k.now = func() time.Time { return now }
	ctx := context.Background()
	_, _ = k.IsMemory(ctx, f, "c")
	f.chans = []bus.Channel{{Name: "c", Kind: bus.KindMemory}} // deleted and recreated as memory
	if got, _ := k.IsMemory(ctx, f, "c"); got {
		t.Fatal("served from cache before the TTL")
	}
	now = now.Add(2 * kindTTL)
	if got, _ := k.IsMemory(ctx, f, "c"); !got {
		t.Fatal("stale after the TTL")
	}
	f.err = errors.New("boom")
	now = now.Add(2 * kindTTL)
	if _, err := k.IsMemory(ctx, f, "c"); err == nil {
		t.Fatal("a ListChannels failure must surface (fail closed for the caller to decide)")
	}
}

func TestDropMemoryAndAgentWake(t *testing.T) {
	f := &fakeMessaging{chans: []bus.Channel{{Name: "mem", Kind: bus.KindMemory}, {Name: "general", Kind: bus.KindOrdinary}, {Name: "work", Kind: bus.KindTasks}}}
	k := NewKindIndex()
	ctx := context.Background()
	in := []bus.Message{{Seq: 1, Channel: "general"}, {Seq: 2, Channel: "mem"}, {Seq: 3, Channel: "work"}}
	out, err := k.DropMemory(ctx, f, in)
	if err != nil || len(out) != 2 || out[0].Seq != 1 || out[1].Seq != 3 {
		t.Fatalf("DropMemory = %v, %v", out, err)
	}
	if len(in) != 3 {
		t.Fatal("DropMemory modified its input")
	}
	w, _, _ := k.AgentWake(ctx, f, bus.WatchResult{Woke: true, Seq: 9, Count: 2, Channels: []string{"mem"}})
	if w.Woke {
		t.Fatalf("memory-only wake kept: %+v", w)
	}
	w, filtered, _ := k.AgentWake(ctx, f, bus.WatchResult{Woke: true, Seq: 9, Count: 2, Channels: []string{"mem", "work"}})
	if !w.Woke || !filtered || w.Count != 0 || len(w.Channels) != 1 || w.Channels[0] != "work" {
		t.Fatalf("mixed wake = %+v", w)
	}
	w, filtered, _ = k.AgentWake(ctx, f, bus.WatchResult{Woke: true, Seq: 9, Count: 1})
	if !w.Woke || filtered || w.Count != 1 {
		t.Fatalf("a wake with no channel list must stay a wake: %+v", w)
	}
}

func TestKindIndexRemembersMisses(t *testing.T) {
	f := &fakeMessaging{chans: []bus.Channel{{Name: "c", Kind: bus.KindOrdinary}}}
	k := NewKindIndex()
	now := time.Unix(1000, 0)
	k.now = func() time.Time { return now }
	ctx := context.Background()
	for range 5 {
		if got, _ := k.IsMemory(ctx, f, "gone"); got {
			t.Fatal("deleted channel counted as memory")
		}
	}
	if f.calls != 1 {
		t.Fatalf("ListChannels called %d times for one deleted channel, want 1", f.calls)
	}
	now = now.Add(2 * missTTL)
	_, _ = k.IsMemory(ctx, f, "gone")
	if f.calls != 2 {
		t.Fatalf("the miss outlived its TTL: %d calls", f.calls)
	}
}

type memoryOnlyBus struct {
	bus.Bus
	chans    []bus.Channel
	receives int
}

func (b *memoryOnlyBus) ListChannels(context.Context) ([]bus.Channel, error) { return b.chans, nil }
func (b *memoryOnlyBus) Receive(context.Context, bus.ReceiveInput) (bus.ReceiveResult, error) {
	b.receives++
	return bus.ReceiveResult{Batch: "b", Messages: []bus.Message{{Channel: "mem"}}}, nil
}

func TestReceiveAgentLoopIsBounded(t *testing.T) {
	b := &memoryOnlyBus{chans: []bus.Channel{{Name: "mem", Kind: bus.KindMemory}}}
	res, err := NewKindIndex().ReceiveAgent(context.Background(), b, bus.ReceiveInput{Session: "s"})
	if err != nil || len(res.Messages) != 0 || b.receives != maxSkipped+1 || res.Batch != "b" {
		t.Fatalf("res %+v err %v after %d receives", res, err, b.receives)
	}
}
