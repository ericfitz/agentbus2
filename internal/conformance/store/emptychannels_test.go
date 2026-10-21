package store

import (
	"context"
	"testing"

	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/store/sqlstore"
)

// Decision (Eric, 2026-10-21), Milestone 3 item 8: EmptyChannelsBefore and
// DeleteChannelIfEmpty (v1 reapEmptyChannels' condition).

func channelAt(t *testing.T, s ab2store.Store, name string, kind ab2store.Kind, at int64) ab2store.Channel {
	t.Helper()
	var ch ab2store.Channel
	mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) (err error) {
		ch, err = tx.CreateChannel(ctx, ab2store.ChannelCreate{Name: name, Kind: kind, CreatedAt: at})
		return err
	})
	return ch
}

func emptyNames(t *testing.T, s ab2store.Store, before int64) []string {
	t.Helper()
	var names []string
	mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
		chs, err := tx.EmptyChannelsBefore(ctx, before)
		names = nil
		for _, c := range chs {
			names = append(names, c.Name)
		}
		return err
	})
	return names
}

func deleteIfEmpty(t *testing.T, s ab2store.Store, id, before int64) bool {
	t.Helper()
	var gone bool
	mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) (err error) {
		gone, err = tx.DeleteChannelIfEmpty(ctx, id, before)
		return err
	})
	return gone
}

func TestChannelEmptyBefore(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		mustIdentity(t, s, "pat")
		const old, cutoff, young = 100, 1000, 2000
		channelAt(t, s, "b-empty-old", ab2store.KindOrdinary, old)
		channelAt(t, s, "a-empty-old", ab2store.KindOrdinary, old)
		channelAt(t, s, "empty-young", ab2store.KindOrdinary, young)
		channelAt(t, s, "empty-at-cutoff", ab2store.KindOrdinary, cutoff) // created_at < cutoff is strict
		channelAt(t, s, "memory-old", ab2store.KindMemory, old)
		channelAt(t, s, "tasks-old", ab2store.KindTasks, old)
		withMsg := channelAt(t, s, "with-message", ab2store.KindOrdinary, old)
		withTomb := channelAt(t, s, "with-tombstone", ab2store.KindOrdinary, old)
		withSub := channelAt(t, s, "with-subscription", ab2store.KindOrdinary, old)
		channelAt(t, s, "empty-despite-tag-sub", ab2store.KindOrdinary, old)

		mustAppend(t, s, ab2store.NewMessage{ChannelID: withMsg.ID, Sender: "pat", Content: "x"})
		tomb := mustAppend(t, s, ab2store.NewMessage{ChannelID: withTomb.ID, Sender: "pat", Content: "x"})
		mustTombstone(t, s, tomb) // the tombstone row stays: the channel is not empty
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			return tx.Subscribe(ctx, "pat", withSub.ID, 0, fixedNow)
		})
		tagSubscribe(t, s, tagSet("pat", "k", 0, "x")) // a tag subscription is not a channel subscription

		got := emptyNames(t, s, cutoff)
		want := []string{"a-empty-old", "b-empty-old", "empty-despite-tag-sub"}
		if len(got) != len(want) {
			t.Fatalf("EmptyChannelsBefore = %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("EmptyChannelsBefore = %v, want %v", got, want)
			}
		}

		// DeleteChannelIfEmpty: refuses every channel that is not a candidate.
		for _, name := range []string{"with-message", "with-tombstone", "with-subscription", "empty-young", "empty-at-cutoff", "memory-old", "tasks-old"} {
			var id int64
			mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
				ch, err := tx.ChannelByName(ctx, name)
				id = ch.ID
				return err
			})
			if deleteIfEmpty(t, s, id, cutoff) {
				t.Errorf("DeleteChannelIfEmpty deleted %s", name)
			}
		}
		if deleteIfEmpty(t, s, 99999, cutoff) {
			t.Error("DeleteChannelIfEmpty deleted an unknown id")
		}
		var ids []int64
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			chs, err := tx.EmptyChannelsBefore(ctx, cutoff)
			for _, c := range chs {
				ids = append(ids, c.ID)
			}
			return err
		})
		for _, id := range ids {
			if !deleteIfEmpty(t, s, id, cutoff) {
				t.Errorf("DeleteChannelIfEmpty(%d) = false on a candidate", id)
			}
			if deleteIfEmpty(t, s, id, cutoff) { // second run: gone, a no-op
				t.Errorf("second DeleteChannelIfEmpty(%d) = true", id)
			}
		}
		if got := emptyNames(t, s, cutoff); len(got) != 0 {
			t.Errorf("after deleting, EmptyChannelsBefore = %v", got)
		}
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			chs, err := tx.ListChannels(ctx)
			if err != nil || len(chs) != 7 { // 10 created, 3 candidates deleted
				t.Errorf("channels left = %d (%v), want 7", len(chs), err)
			}
			return nil
		})
	})
}

// A deleted channel's ACL rows go with it (the cascade of a normal delete).
func TestChannelEmptyDeleteCascadesACL(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, rawDSN := openStore(t, e, sqlstore.Options{})
		mustIdentity(t, s, "pat")
		ch := channelAt(t, s, "gone", ab2store.KindOrdinary, 1)
		if n := scalar[int](t, rawDB(t, e, rawDSN), "SELECT count(*) FROM channel_acl"); n == 0 {
			t.Fatal("fixture has no ACL rows")
		}
		if !deleteIfEmpty(t, s, ch.ID, 1000) {
			t.Fatal("not deleted")
		}
		if n := scalar[int](t, rawDB(t, e, rawDSN), "SELECT count(*) FROM channel_acl"); n != 0 {
			t.Fatalf("%d ACL rows left", n)
		}
	})
}
