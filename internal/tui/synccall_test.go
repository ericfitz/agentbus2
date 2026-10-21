package tui

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/bus"
)

// stallBus wraps a real bus; while stall is set, every call the model makes
// synchronously inside Update blocks until its context ends, as a request to
// a server that accepted the connection and then stopped answering would
// (server mode's HTTP client has no Timeout, for the 50 s long poll).
type stallBus struct {
	bus.Bus
	stall atomic.Bool
}

func (b *stallBus) wait(ctx context.Context) (bool, error) {
	if !b.stall.Load() {
		return false, nil
	}
	<-ctx.Done()
	return true, ctx.Err()
}

func (b *stallBus) GetTask(ctx context.Context, id int64) (bus.Task, error) {
	if ok, err := b.wait(ctx); ok {
		return bus.Task{}, err
	}
	return b.Bus.GetTask(ctx, id)
}

func (b *stallBus) Claim(ctx context.Context, in bus.TaskClaimInput) (bus.TaskUpdateResult, error) {
	if ok, err := b.wait(ctx); ok {
		return bus.TaskUpdateResult{}, err
	}
	return b.Bus.Claim(ctx, in)
}

func (b *stallBus) Release(ctx context.Context, in bus.TaskReleaseInput) (bus.TaskUpdateResult, error) {
	if ok, err := b.wait(ctx); ok {
		return bus.TaskUpdateResult{}, err
	}
	return b.Bus.Release(ctx, in)
}

func (b *stallBus) Update(ctx context.Context, p bus.TaskPatch) (bus.TaskUpdateResult, error) {
	if ok, err := b.wait(ctx); ok {
		return bus.TaskUpdateResult{}, err
	}
	return b.Bus.Update(ctx, p)
}

func (b *stallBus) UnsubscribeTags(ctx context.Context, in bus.TagSubscribeInput) error {
	if ok, err := b.wait(ctx); ok {
		return err
	}
	return b.Bus.UnsubscribeTags(ctx, in)
}

func (b *stallBus) Subscribe(ctx context.Context, in bus.SubscribeInput) error {
	if ok, err := b.wait(ctx); ok {
		return err
	}
	return b.Bus.Subscribe(ctx, in)
}

func (b *stallBus) Unsubscribe(ctx context.Context, in bus.UnsubscribeInput) error {
	if ok, err := b.wait(ctx); ok {
		return err
	}
	return b.Bus.Unsubscribe(ctx, in)
}

func (b *stallBus) DeleteChannel(ctx context.Context, in bus.DeleteChannelInput) error {
	if ok, err := b.wait(ctx); ok {
		return err
	}
	return b.Bus.DeleteChannel(ctx, in)
}

func setCallTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	old := callTimeout
	callTimeout = d
	t.Cleanup(func() { callTimeout = old })
}

// TestSyncCallsAreBounded: a key whose bus call runs inside Update (so that
// no receive batch can race it) must not freeze the program, quit included,
// when the server stops answering: the call gives up after callTimeout and
// the refusal is a toast.
func TestSyncCallsAreBounded(t *testing.T) {
	setCallTimeout(t, 50*time.Millisecond)
	var sb *stallBus
	f := newFixtureWith(t, func(b bus.Bus) bus.Bus { sb = &stallBus{Bus: b}; return sb }, Host{})
	press := func(what, wantToast string, keys ...string) {
		t.Helper()
		done := make(chan struct{})
		go func() {
			defer close(done)
			for _, k := range keys {
				f.key(k)
			}
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: Update blocked on a stalled bus call", what)
		}
		if !strings.HasPrefix(f.m.toast, wantToast) || !strings.Contains(f.m.toast, context.DeadlineExceeded.Error()) {
			t.Fatalf("%s: toast = %q, want %q and the deadline", what, f.m.toast, wantToast)
		}
	}

	f.openTasks(t, "a")
	sb.stall.Store(true)
	press("t (Claim)", "task: ", "t")
	press("space (GetTask)", "task: ", " ")
	press("a, enter (Update)", "task: ", "a", "enter")
	sb.stall.Store(false)
	f.key(" ")
	if f.m.draft.id == 0 {
		t.Fatal("space did not start a draft")
	}
	sb.stall.Store(true)
	press("enter (Claim from a draft)", "task: ", "enter")
	if f.m.draft.id == 0 {
		t.Fatal("a refused save must keep the draft")
	}
	f.key(" ") // the kept draft moves on to completed: saved through Update
	press("enter (Update from a draft)", "task: ", "enter")
	sb.stall.Store(false)
	f.key("esc")

	mine := "eric"
	if _, err := f.bus.Update(taskCtx, bus.TaskPatch{ID: f.m.tasks["todo"][0].ID, Owner: &mine}); err != nil {
		t.Fatal(err)
	}
	f.run(f.m.loadTasks("todo"))
	for i := 0; i < 4 && f.m.pane() != paneStream; i++ {
		f.key("tab")
	}
	if f.m.pane() != paneStream {
		t.Fatalf("pane = %v, want the stream", f.m.pane())
	}
	sb.stall.Store(true)
	press("u (Release)", "task: ", "u")
	sb.stall.Store(false)

	f.key("esc")
	f.followTags(t, []string{"a"})
	f.key("esc")
	f.moveTo(t, tagPanePrefix+"a")
	sb.stall.Store(true)
	press("d on a tag set (UnsubscribeTags)", "unsubscribe: ", "d")
	sb.stall.Store(false)

	f.moveTo(t, "dev")
	sb.stall.Store(true)
	press("s (Unsubscribe)", "unsubscribe: ", "s")
	press("d, y (DeleteChannel)", "delete: ", "d", "y")
	sb.stall.Store(false)
	f.key("s")
	if f.c.isSubscribed("dev") {
		t.Fatal("s did not unsubscribe dev")
	}
	sb.stall.Store(true)
	press("s (Subscribe)", "subscribe: ", "s")
}
