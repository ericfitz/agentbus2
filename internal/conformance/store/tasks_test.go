package store

import (
	"context"
	"reflect"
	"testing"

	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/store/sqlstore"
)

func TestTaskLiveMemoriesOnChannel(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		tl := mustChannel(t, s, "tl", ab2store.KindTasks, "", nil)
		other := mustChannel(t, s, "other", ab2store.KindTasks, "", nil)
		mem := func(ch ab2store.Channel) int64 {
			seq := mustAppend(t, s, ab2store.NewMessage{ChannelID: ch.ID, Sender: "pat", Content: "t", Tags: []string{"x"}})
			mustSetMemory(t, s, seq, seq, 1)
			return seq
		}
		a := mem(tl)
		dead := mem(tl)
		mustTombstone(t, s, dead)
		_ = mem(other)
		ordinary := mustAppend(t, s, ab2store.NewMessage{ChannelID: tl.ID, Sender: "pat", Content: "plain"})
		b := mem(tl)
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			ms, err := tx.LiveMemories(ctx, tl.ID)
			if err != nil {
				return err
			}
			if got, want := messageSeqs(ms), []int64{a, b}; !reflect.DeepEqual(got, want) {
				t.Errorf("LiveMemories = %v, want %v (ordinary %d and tombstoned %d excluded)", got, want, ordinary, dead)
			}
			if len(ms) == 2 && !reflect.DeepEqual(ms[0].Tags, []string{"x"}) {
				t.Errorf("tags = %v, want [x]", ms[0].Tags)
			}
			return nil
		})
	})
}

func TestTaskChannelLastActivity(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		tl := mustChannel(t, s, "tl", ab2store.KindTasks, "", nil)
		other := mustChannel(t, s, "other", ab2store.KindTasks, "", nil)
		last := func() int64 {
			var at int64
			mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) (err error) {
				at, err = tx.ChannelLastActivity(ctx, tl.ID)
				return err
			})
			return at
		}
		if got := last(); got != fixedNow {
			t.Errorf("empty channel = %d, want created_at %d", got, fixedNow)
		}
		seq := mustAppend(t, s, ab2store.NewMessage{ChannelID: tl.ID, Sender: "pat", Content: "x", CreatedAt: fixedNow + 10})
		if got := last(); got != fixedNow+10 {
			t.Errorf("after message = %d, want %d", got, fixedNow+10)
		}
		mustAppend(t, s, ab2store.NewMessage{ChannelID: other.ID, Sender: "pat", Content: "x", CreatedAt: fixedNow + 500})
		if got := last(); got != fixedNow+10 {
			t.Errorf("other channel's activity leaked: %d", got)
		}
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			_, err := tx.TombstoneMessage(ctx, seq, fixedNow+30)
			return err
		})
		if got := last(); got != fixedNow+30 {
			t.Errorf("after tombstone = %d, want %d", got, fixedNow+30)
		}
	})
}

func TestTaskKindIsStored(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		tl := mustChannel(t, s, "tl", ab2store.KindTasks, "", nil)
		mc := mustChannel(t, s, "mc", ab2store.KindMemory, "", nil)
		tmem := mustAppend(t, s, ab2store.NewMessage{ChannelID: tl.ID, Sender: "pat", Content: "m"})
		mustSetMemory(t, s, tmem, tmem, 1)
		mmem := mustAppend(t, s, ab2store.NewMessage{ChannelID: mc.ID, Sender: "pat", Content: "m"})
		mustSetMemory(t, s, mmem, mmem, 1)
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			chs, err := tx.ListChannelsByKind(ctx, ab2store.KindTasks)
			if err != nil || len(chs) != 1 || chs[0].ID != tl.ID {
				t.Errorf("ListChannelsByKind(tasks) = %v, %v; want only %d", chs, err, tl.ID)
			}
			ms, err := tx.UnembeddedMemories(ctx, "m", 10)
			if err != nil || !reflect.DeepEqual(messageSeqs(ms), []int64{mmem}) {
				t.Errorf("UnembeddedMemories = %v, %v; want only %d (task memory %d skipped)", messageSeqs(ms), err, mmem, tmem)
			}
			return nil
		})
	})
}
