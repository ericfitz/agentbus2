package store

import (
	"context"
	"errors"
	"testing"

	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/store/sqlstore"
)

func subList(t *testing.T, s ab2store.Store, identity string) []ab2store.Subscription {
	t.Helper()
	var out []ab2store.Subscription
	mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) (err error) {
		out, err = tx.Subscriptions(ctx, identity)
		return err
	})
	return out
}

func subscribe(t *testing.T, s ab2store.Store, identity string, ch, acked, now int64) {
	t.Helper()
	mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error { return tx.Subscribe(ctx, identity, ch, acked, now) })
}

func TestSubscriptionIdempotent(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		mustIdentity(t, s, "kim")
		ch := mustChannel(t, s, "a", ab2store.KindOrdinary, "", nil)
		subscribe(t, s, "kim", ch.ID, 3, fixedNow+1)
		subscribe(t, s, "kim", ch.ID, 9, fixedNow+2)
		got := subList(t, s, "kim")
		want := ab2store.Subscription{Identity: "kim", ChannelID: ch.ID, AckedSeq: 3, CreatedAt: fixedNow + 1}
		if len(got) != 1 || got[0] != want {
			t.Errorf("Subscriptions = %+v, want [%+v]", got, want)
		}
	})
}

func TestSubscriptionRejectsUnknown(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		mustIdentity(t, s, "kim")
		ch := mustChannel(t, s, "a", ab2store.KindOrdinary, "", nil)
		for name, c := range map[string]struct {
			id string
			ch int64
		}{"identity": {"nobody", ch.ID}, "channel": {"kim", ch.ID + 99}} {
			err := s.Tx(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
				return tx.Subscribe(ctx, c.id, c.ch, 0, fixedNow)
			})
			if !errors.Is(err, ab2store.ErrNotFound) {
				t.Errorf("Subscribe unknown %s = %v, want ErrNotFound", name, err)
			}
		}
		err := s.Tx(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
			return tx.AdvanceAckedMark(ctx, "kim", ch.ID, 5)
		})
		if !errors.Is(err, ab2store.ErrNotFound) {
			t.Errorf("AdvanceAckedMark without a subscription = %v, want ErrNotFound", err)
		}
	})
}

func TestSubscriptionUnsubscribeAndList(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		mustIdentity(t, s, "kim")
		mustIdentity(t, s, "pat")
		a := mustChannel(t, s, "a", ab2store.KindOrdinary, "", nil)
		b := mustChannel(t, s, "b", ab2store.KindOrdinary, "", nil)
		subscribe(t, s, "kim", b.ID, 0, fixedNow)
		subscribe(t, s, "kim", a.ID, 0, fixedNow)
		subscribe(t, s, "pat", a.ID, 0, fixedNow)
		got := subList(t, s, "kim")
		if len(got) != 2 || got[0].ChannelID != a.ID || got[1].ChannelID != b.ID {
			t.Fatalf("Subscriptions = %+v, want channels %d,%d in id order", got, a.ID, b.ID)
		}
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error { return tx.Unsubscribe(ctx, "kim", a.ID) })
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error { return tx.Unsubscribe(ctx, "kim", a.ID) }) // absent: no-op
		got = subList(t, s, "kim")
		if len(got) != 1 || got[0].ChannelID != b.ID {
			t.Errorf("after Unsubscribe = %+v, want only channel %d", got, b.ID)
		}
		if p := subList(t, s, "pat"); len(p) != 1 {
			t.Errorf("pat's subscription affected: %+v", p)
		}
	})
}

func TestSubscriptionAdvanceAckedMarkIsMax(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		mustIdentity(t, s, "kim")
		ch := mustChannel(t, s, "a", ab2store.KindOrdinary, "", nil)
		subscribe(t, s, "kim", ch.ID, 2, fixedNow)
		for _, step := range []struct{ seq, want int64 }{{7, 7}, {4, 7}, {7, 7}, {10, 10}} {
			mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
				return tx.AdvanceAckedMark(ctx, "kim", ch.ID, step.seq)
			})
			if got := subList(t, s, "kim")[0].AckedSeq; got != step.want {
				t.Errorf("after Advance(%d) AckedSeq = %d, want %d", step.seq, got, step.want)
			}
		}
	})
}

func TestSubscriptionDeletedWithChannel(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		mustIdentity(t, s, "kim")
		a := mustChannel(t, s, "a", ab2store.KindOrdinary, "", nil)
		b := mustChannel(t, s, "b", ab2store.KindOrdinary, "", nil)
		subscribe(t, s, "kim", a.ID, 0, fixedNow)
		subscribe(t, s, "kim", b.ID, 0, fixedNow)
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error { return tx.DeleteChannel(ctx, a.ID) })
		got := subList(t, s, "kim")
		if len(got) != 1 || got[0].ChannelID != b.ID {
			t.Errorf("Subscriptions after DeleteChannel = %+v, want only channel %d", got, b.ID)
		}
	})
}
