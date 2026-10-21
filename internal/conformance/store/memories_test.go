package store

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/store/sqlstore"
)

// mustSetMemory marks seq as revision rev of memory id.
func mustSetMemory(t *testing.T, s ab2store.Store, seq, id, rev int64) {
	t.Helper()
	mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
		return tx.SetMemory(ctx, seq, id, rev)
	})
}

func memRevSeqs(t *testing.T, s ab2store.Store, id int64) []int64 {
	t.Helper()
	var out []int64
	mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
		ms, err := tx.MemoryRevisions(ctx, id)
		out = messageSeqs(ms)
		return err
	})
	return out
}

func TestMemoryRevisionsAndLive(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		ch := mustChannel(t, s, "mem", ab2store.KindMemory, "", nil)
		s1 := mustAppend(t, s, ab2store.NewMessage{ChannelID: ch.ID, Sender: "pat", Content: "v1", Tags: []string{"a"}})
		mustSetMemory(t, s, s1, s1, 1)
		s2 := mustAppend(t, s, ab2store.NewMessage{ChannelID: ch.ID, Sender: "pat", Content: "v2"})
		mustSetMemory(t, s, s2, s1, 2)
		mustTombstone(t, s, s1)

		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			m, err := tx.LiveMemory(ctx, s1)
			if err != nil || m.Seq != s2 || m.Content != "v2" || m.Revision == nil || *m.Revision != 2 || m.MemoryID == nil || *m.MemoryID != s1 {
				t.Errorf("LiveMemory = %+v, %v; want seq %d revision 2", m, err, s2)
			}
			return nil
		})
		if got, want := memRevSeqs(t, s, s1), []int64{s1, s2}; !reflect.DeepEqual(got, want) {
			t.Errorf("MemoryRevisions = %v, want %v", got, want)
		}
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			ms, _ := tx.MemoryRevisions(ctx, s1)
			if len(ms) == 2 && !reflect.DeepEqual(ms[0].Tags, []string{"a"}) {
				t.Errorf("revision 1 tags = %v, want [a] (tombstoned rows are included with tags)", ms[0].Tags)
			}
			if ms, err := tx.MemoryRevisions(ctx, 999); err != nil || len(ms) != 0 {
				t.Errorf("MemoryRevisions(999) = %v, %v; want empty", ms, err)
			}
			return nil
		})
		mustTombstone(t, s, s2)
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			if _, err := tx.LiveMemory(ctx, s1); !errors.Is(err, ab2store.ErrNotFound) {
				t.Errorf("LiveMemory after tombstoning all = %v, want ErrNotFound", err)
			}
			return nil
		})
	})
}

func TestMemorySetMemoryUnknownSeq(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		err := s.Tx(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
			return tx.SetMemory(ctx, 99, 99, 1)
		})
		if !errors.Is(err, ab2store.ErrNotFound) {
			t.Errorf("SetMemory(99) = %v, want ErrNotFound", err)
		}
	})
}

func expiredIDs(t *testing.T, s ab2store.Store, before int64, limit int) []int64 {
	t.Helper()
	out := []int64{}
	mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
		refs, err := tx.ExpiredMemories(ctx, before, limit)
		for _, r := range refs {
			out = append(out, r.MemoryID)
		}
		return err
	})
	return out
}

// threeMemories creates three memories, each with one live revision, and
// returns their ids.
func threeMemories(t *testing.T, s ab2store.Store) (ids [3]int64) {
	t.Helper()
	ch := mustChannel(t, s, "mem", ab2store.KindMemory, "", nil)
	for i := range ids {
		seq := mustAppend(t, s, ab2store.NewMessage{ChannelID: ch.ID, Sender: "pat", Content: "m"})
		mustSetMemory(t, s, seq, seq, 1)
		ids[i] = seq
	}
	return ids
}

func TestMemoryAccess(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		ids := threeMemories(t, s)
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			if err := tx.TouchMemoryAccess(ctx, []int64{ids[0], ids[1]}, 100); err != nil {
				return err
			}
			return tx.TouchMemoryAccess(ctx, []int64{ids[0]}, 300) // upsert: later time wins
		})
		if got, want := expiredIDs(t, s, 200, 10), []int64{ids[1]}; !reflect.DeepEqual(got, want) {
			t.Errorf("Expired(<200) = %v, want %v", got, want)
		}
		if got, want := expiredIDs(t, s, 400, 10), []int64{ids[0], ids[1]}; !reflect.DeepEqual(got, want) {
			t.Errorf("Expired(<400) = %v, want %v (ascending)", got, want)
		}
		if got := expiredIDs(t, s, 400, 1); len(got) != 1 || got[0] != ids[0] {
			t.Errorf("Expired limit 1 = %v, want [%d]", got, ids[0])
		}
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			if err := tx.DeleteMemoryAccess(ctx, ids[0]); err != nil {
				return err
			}
			return tx.DeleteMemoryAccess(ctx, 12345) // absent: no error
		})
		if got, want := expiredIDs(t, s, 400, 10), []int64{ids[1]}; !reflect.DeepEqual(got, want) {
			t.Errorf("Expired after delete = %v, want %v", got, want)
		}
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			if _, err := tx.ExpiredMemories(ctx, 1, 0); !errors.Is(err, ab2store.ErrInvalid) {
				t.Errorf("limit 0 err = %v, want ErrInvalid", err)
			}
			return nil
		})
	})
}

func TestMemoryExpiredIgnoresTombstoned(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		ids := threeMemories(t, s)
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			return tx.TouchMemoryAccess(ctx, ids[:], 100)
		})
		mustTombstone(t, s, ids[1])
		got := expiredIDs(t, s, 200, 10)
		if want := []int64{ids[0], ids[2]}; !reflect.DeepEqual(got, want) {
			t.Errorf("Expired = %v, want %v", got, want)
		}
		// A new live revision of the tombstoned memory counts again.
		ch := mustChannel(t, s, "mem2", ab2store.KindMemory, "", nil)
		r2 := mustAppend(t, s, ab2store.NewMessage{ChannelID: ch.ID, Sender: "pat", Content: "v2"})
		mustSetMemory(t, s, r2, ids[1], 2)
		got = expiredIDs(t, s, 200, 10)
		if want := []int64{ids[0], ids[1], ids[2]}; !reflect.DeepEqual(got, want) {
			t.Errorf("Expired with new live revision = %v, want %v", got, want)
		}
	})
}
