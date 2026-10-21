package store

import (
	"context"
	"reflect"
	"testing"

	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/store/sqlstore"
)

// embeddingMemory appends a memory revision with the given memory id.
func embeddingMemory(t *testing.T, s ab2store.Store, ch int64, memID int64) int64 {
	t.Helper()
	rev := int64(1)
	return mustAppend(t, s, ab2store.NewMessage{ChannelID: ch, Sender: "p", Content: "mem", MemoryID: &memID, Revision: &rev})
}

func embeddingCount(t *testing.T, e dbtest.Engine, rawDSN func() string, tbl string) int {
	t.Helper()
	return scalar[int](t, rawDB(t, e, rawDSN), "SELECT count(*) FROM "+tbl)
}

func TestEmbeddingPutOnlyForLiveMemory(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, rawDSN := openStore(t, e, sqlstore.Options{})
		a := mustChannel(t, s, "a", ab2store.KindMemory, "", nil)
		ordinary := mustAppend(t, s, ab2store.NewMessage{ChannelID: a.ID, Sender: "p", Content: "x"})
		live := embeddingMemory(t, s, a.ID, 1)
		dead := embeddingMemory(t, s, a.ID, 2)
		mustTombstone(t, s, dead)
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			for _, seq := range []int64{ordinary, live, dead, 999} {
				if err := tx.PutEmbedding(ctx, ab2store.Embedding{Seq: seq, Model: "m", Vector: []byte{1, 2, 3, 4}}); err != nil {
					return err
				}
				if err := tx.PutEmbedFailure(ctx, ab2store.EmbedFailure{Seq: seq, Model: "m", Error: "e", FailedAt: fixedNow}); err != nil {
					return err
				}
			}
			// A second put replaces the row.
			return tx.PutEmbedding(ctx, ab2store.Embedding{Seq: live, Model: "m2", Vector: []byte{9}})
		})
		if n := embeddingCount(t, e, rawDSN, "embeddings"); n != 1 {
			t.Errorf("embeddings rows = %d, want 1", n)
		}
		if n := embeddingCount(t, e, rawDSN, "embed_failures"); n != 1 {
			t.Errorf("embed_failures rows = %d, want 1", n)
		}
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			got, err := tx.Embeddings(ctx, "m2", []int64{a.ID})
			want := []ab2store.Embedding{{Seq: live, Model: "m2", Vector: []byte{9}}}
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Errorf("Embeddings = %v, %v; want %v", got, err, want)
			}
			return nil
		})
	})
}

func TestEmbeddingUnembeddedMemories(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		a := mustChannel(t, s, "a", ab2store.KindOrdinary, "", nil)
		tasks := mustChannel(t, s, "jobs", ab2store.KindTasks, "", nil)
		plain := embeddingMemory(t, s, a.ID, 1)
		_ = embeddingMemory(t, s, tasks.ID, 2)
		embedded := embeddingMemory(t, s, a.ID, 3)
		failed := embeddingMemory(t, s, a.ID, 4)
		tomb := embeddingMemory(t, s, a.ID, 5)
		mustTombstone(t, s, tomb)
		mustAppend(t, s, ab2store.NewMessage{ChannelID: a.ID, Sender: "p", Content: "ordinary"})
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			if err := tx.PutEmbedding(ctx, ab2store.Embedding{Seq: embedded, Model: "m", Vector: []byte{1}}); err != nil {
				return err
			}
			return tx.PutEmbedFailure(ctx, ab2store.EmbedFailure{Seq: failed, Model: "m", Error: "e", FailedAt: fixedNow})
		})
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			got, err := tx.UnembeddedMemories(ctx, "m", 10)
			if err != nil || !reflect.DeepEqual(messageSeqs(got), []int64{plain}) {
				t.Errorf("model m = %v, %v; want [%d]", messageSeqs(got), err, plain)
			}
			got, err = tx.UnembeddedMemories(ctx, "other", 10)
			if err != nil || !reflect.DeepEqual(messageSeqs(got), []int64{plain, embedded, failed}) {
				t.Errorf("model other = %v, %v; want [%d %d %d]", messageSeqs(got), err, plain, embedded, failed)
			}
			got, err = tx.UnembeddedMemories(ctx, "other", 2)
			if err != nil || len(got) != 2 {
				t.Errorf("limit 2 = %v, %v", messageSeqs(got), err)
			}
			return nil
		})
	})
}

func TestEmbeddingDeleteStaleModel(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, rawDSN := openStore(t, e, sqlstore.Options{})
		a := mustChannel(t, s, "a", ab2store.KindMemory, "", nil)
		one := embeddingMemory(t, s, a.ID, 1)
		two := embeddingMemory(t, s, a.ID, 2)
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			if err := tx.PutEmbedding(ctx, ab2store.Embedding{Seq: one, Model: "old", Vector: []byte{1}}); err != nil {
				return err
			}
			if err := tx.PutEmbedding(ctx, ab2store.Embedding{Seq: two, Model: "new", Vector: []byte{2}}); err != nil {
				return err
			}
			return tx.PutEmbedFailure(ctx, ab2store.EmbedFailure{Seq: one, Model: "old", Error: "e", FailedAt: fixedNow})
		})
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error { return tx.DeleteStaleModel(ctx, "new") })
		if n := embeddingCount(t, e, rawDSN, "embeddings"); n != 1 {
			t.Errorf("embeddings rows = %d, want 1", n)
		}
		if n := embeddingCount(t, e, rawDSN, "embed_failures"); n != 0 {
			t.Errorf("embed_failures rows = %d, want 0", n)
		}
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error { return tx.DeleteStaleModel(ctx, "new") }) // idempotent
	})
}

func TestEmbeddingByChannels(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		a := mustChannel(t, s, "a", ab2store.KindMemory, "", nil)
		b := mustChannel(t, s, "b", ab2store.KindMemory, "", nil)
		one := embeddingMemory(t, s, a.ID, 1)
		two := embeddingMemory(t, s, b.ID, 2)
		three := embeddingMemory(t, s, a.ID, 3)
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			for _, seq := range []int64{one, two, three} {
				if err := tx.PutEmbedding(ctx, ab2store.Embedding{Seq: seq, Model: "m", Vector: []byte{byte(seq)}}); err != nil {
					return err
				}
			}
			return nil
		})
		mustTombstone(t, s, three)
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			got, err := tx.Embeddings(ctx, "m", []int64{a.ID})
			if err != nil || len(got) != 1 || got[0].Seq != one {
				t.Errorf("a = %v, %v; want only seq %d", got, err, one)
			}
			got, err = tx.Embeddings(ctx, "m", []int64{a.ID, b.ID})
			if err != nil || len(got) != 2 {
				t.Errorf("a+b = %v, %v; want 2", got, err)
			}
			got, err = tx.Embeddings(ctx, "m", nil)
			if err != nil || len(got) != 0 {
				t.Errorf("no channels = %v, %v; want empty", got, err)
			}
			got, err = tx.Embeddings(ctx, "other", []int64{a.ID, b.ID})
			if err != nil || len(got) != 0 {
				t.Errorf("other model = %v, %v; want empty", got, err)
			}
			return nil
		})
	})
}

func TestEmbeddingDelete(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, rawDSN := openStore(t, e, sqlstore.Options{})
		a := mustChannel(t, s, "a", ab2store.KindMemory, "", nil)
		one := embeddingMemory(t, s, a.ID, 1)
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			return tx.PutEmbedding(ctx, ab2store.Embedding{Seq: one, Model: "m", Vector: []byte{1}})
		})
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			if err := tx.DeleteEmbedding(ctx, one); err != nil {
				return err
			}
			return tx.DeleteEmbedding(ctx, one) // second delete is a no-op
		})
		if n := embeddingCount(t, e, rawDSN, "embeddings"); n != 0 {
			t.Errorf("embeddings rows = %d, want 0", n)
		}
	})
}
