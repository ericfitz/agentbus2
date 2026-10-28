package messaging

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/store/sqlite"
)

func newSendSvc(t *testing.T) (Service, store.Store) {
	t.Helper()
	s, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "agentbus2.db"), sqlite.Options{})
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	c, err := core.New(s, core.Defaults(), core.Options{})
	if err != nil {
		t.Fatalf("core.New: %v", err)
	}
	err = s.Tx(context.Background(), func(ctx context.Context, tx store.Tx) error {
		if err := tx.CreateIdentity(ctx, "pat", c.NowMs()); err != nil {
			return err
		}
		_, err := tx.CreateChannel(ctx, store.ChannelCreate{Name: "chat", Kind: store.KindOrdinary, Creator: "pat", CreatedAt: c.NowMs()})
		return err
	})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	return Service{c}, s
}

func maxSeq(t *testing.T, s store.Store) int64 {
	t.Helper()
	var n int64
	err := s.Read(context.Background(), func(ctx context.Context, tx store.Tx) error {
		var err error
		n, err = tx.MaxSeq(ctx)
		return err
	})
	if err != nil {
		t.Fatalf("MaxSeq: %v", err)
	}
	return n
}

func TestSendWithHookRollsBack(t *testing.T) {
	ctx := context.Background()
	svc, st := newSendSvc(t)
	before := maxSeq(t, st)

	boom := errors.New("hook failed")
	_, err := svc.SendWith(ctx, "pat", bus.SendInput{Channel: "chat", Content: "hi"}, SendOptions{Hook: func(context.Context, store.Tx, int64) error { return boom }})
	if err == nil {
		t.Fatal("SendWith with a failing hook succeeded")
	}
	if got := maxSeq(t, st); got != before {
		t.Fatalf("MaxSeq = %d after a failed hook, want %d", got, before)
	}

	// A *bus.Error from the hook keeps its code.
	_, err = svc.SendWith(ctx, "pat", bus.SendInput{Channel: "chat", Content: "hi"}, SendOptions{Hook: func(context.Context, store.Tx, int64) error {
		return core.E(bus.CodeConflict, "nope")
	}})
	if bus.CodeOf(err) != bus.CodeConflict {
		t.Fatalf("hook conflict = %v", err)
	}
	if got := maxSeq(t, st); got != before {
		t.Fatalf("MaxSeq = %d after a conflicting hook, want %d", got, before)
	}

	// A passing hook sees the seq of the appended message, in its transaction.
	var seen int64
	res, err := svc.SendWith(ctx, "pat", bus.SendInput{Channel: "chat", Content: "hi"}, SendOptions{Hook: func(ctx context.Context, tx store.Tx, seq int64) error {
		seen = seq
		m, err := tx.Message(ctx, seq)
		if err != nil || m.Content != "hi" {
			return errors.New("the appended message is not visible to the hook")
		}
		return nil
	}})
	if err != nil || res.Seq != seen || res.Seq != before+1 {
		t.Fatalf("SendWith = %+v, %v (hook saw %d, before %d)", res, err, seen, before)
	}

	// Send is SendWith without a hook.
	res2, err := svc.Send(ctx, "pat", bus.SendInput{Channel: "chat", Content: "again"})
	if err != nil || res2.Seq != res.Seq+1 {
		t.Fatalf("Send = %+v, %v", res2, err)
	}
}

func TestSendWithUnknownSource(t *testing.T) {
	ctx := context.Background()
	svc, st := newSendSvc(t)
	before := maxSeq(t, st)
	_, err := svc.SendWith(ctx, "pat", bus.SendInput{Channel: "chat", Content: "hi"}, SendOptions{Source: "carrier-pigeon"})
	if bus.CodeOf(err) != bus.CodeInternal {
		t.Fatalf("unknown source = %v, want internal", err)
	}
	if got := maxSeq(t, st); got != before {
		t.Fatalf("MaxSeq = %d after an unknown source, want %d", got, before)
	}
}
