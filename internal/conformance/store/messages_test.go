package store

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/ericfitz/agentbus2/internal/dbtest"
	"github.com/ericfitz/agentbus2/internal/search"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/store/sqlstore"
)

func messageSeqs(ms []ab2store.Message) []int64 {
	out := []int64{}
	for _, m := range ms {
		out = append(out, m.Seq)
	}
	return out
}

func messageGet(t *testing.T, s ab2store.Store, seq int64) ab2store.Message {
	t.Helper()
	var m ab2store.Message
	mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) (err error) {
		m, err = tx.Message(ctx, seq)
		return err
	})
	return m
}

func TestMessageAppendAssignsSeqAndStoresTags(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		a := mustChannel(t, s, "a", ab2store.KindOrdinary, "", nil)
		b := mustChannel(t, s, "b", ab2store.KindOrdinary, "", nil)
		s1 := mustAppend(t, s, ab2store.NewMessage{ChannelID: a.ID, Sender: "pat", Content: "one"})
		reply := int64(1)
		s2 := mustAppend(t, s, ab2store.NewMessage{
			ChannelID: b.ID, Sender: "sam", SessionID: "sess", CreatedAt: fixedNow + 5, Type: "note",
			Subject: "subj", Content: "two", ReplyTo: &reply, Metadata: map[string]string{"k": "v"},
			Tags: []string{"team:a", "env", "env"},
		})
		s3 := mustAppend(t, s, ab2store.NewMessage{ChannelID: a.ID, Sender: "pat", Content: "three"})
		if s1 != 1 || s2 != 2 || s3 != 3 {
			t.Fatalf("seqs = %d %d %d, want 1 2 3", s1, s2, s3)
		}
		want := ab2store.Message{
			Seq: 2, ChannelID: b.ID, Sender: "sam", SessionID: "sess", SenderKind: ab2store.IdentityAgent, CreatedAt: fixedNow + 5, Type: "note",
			Subject: "subj", Content: "two", ReplyTo: &reply, Metadata: map[string]string{"k": "v"},
			Tags: []string{"env", "team:a"},
		}
		if got := messageGet(t, s, 2); !reflect.DeepEqual(got, want) {
			t.Errorf("Message(2) = %+v, want %+v", got, want)
		}
		if got := messageGet(t, s, 1); len(got.Tags) != 0 || got.Metadata != nil || got.ReplyTo != nil {
			t.Errorf("Message(1) = %+v, want no tags, metadata or reply", got)
		}
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			if n, err := tx.MaxSeq(ctx); err != nil || n != 3 {
				t.Errorf("MaxSeq = %d, %v; want 3", n, err)
			}
			if _, err := tx.Message(ctx, 99); !errors.Is(err, ab2store.ErrNotFound) {
				t.Errorf("Message(99) err = %v, want ErrNotFound", err)
			}
			return nil
		})
	})
}

func TestMessageAppendNeedsAppendTx(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		a := mustChannel(t, s, "a", ab2store.KindOrdinary, "", nil)
		err := s.Tx(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
			_, err := tx.AppendMessage(ctx, ab2store.NewMessage{ChannelID: a.ID, Sender: "pat", CreatedAt: fixedNow, Content: "x"})
			return err
		})
		if !errors.Is(err, ab2store.ErrNotAppend) {
			t.Fatalf("AppendMessage in Tx err = %v, want ErrNotAppend", err)
		}
		err = s.Read(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
			_, err := tx.AppendMessage(ctx, ab2store.NewMessage{ChannelID: a.ID, Sender: "pat", CreatedAt: fixedNow, Content: "x"})
			return err
		})
		if !errors.Is(err, ab2store.ErrReadOnly) {
			t.Fatalf("AppendMessage in Read err = %v, want ErrReadOnly", err)
		}
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			if n, err := tx.MaxSeq(ctx); err != nil || n != 0 {
				t.Errorf("MaxSeq = %d, %v; want 0", n, err)
			}
			return nil
		})
	})
}

func TestMessageAppendRejectsUnknownChannel(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		err := s.Append(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
			_, err := tx.AppendMessage(ctx, ab2store.NewMessage{ChannelID: 999, Sender: "pat", CreatedAt: fixedNow, Content: "x"})
			return err
		})
		if !errors.Is(err, ab2store.ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})
}

func TestMessagesAfter(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		a := mustChannel(t, s, "a", ab2store.KindOrdinary, "", nil)
		b := mustChannel(t, s, "b", ab2store.KindOrdinary, "", nil)
		for i := 0; i < 6; i++ {
			ch := a.ID
			if i%3 == 2 {
				ch = b.ID
			}
			mustAppend(t, s, ab2store.NewMessage{ChannelID: ch, Sender: "pat", Content: "m"})
		}
		mustTombstone(t, s, 4)
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			got, err := tx.MessagesAfter(ctx, a.ID, 0, ab2store.MessageFilter{}, 10)
			if err != nil || !reflect.DeepEqual(messageSeqs(got), []int64{1, 2, 5}) {
				t.Errorf("MessagesAfter(a,0) = %v, %v; want [1 2 5]", messageSeqs(got), err)
			}
			got, err = tx.MessagesAfter(ctx, a.ID, 1, ab2store.MessageFilter{}, 10)
			if err != nil || !reflect.DeepEqual(messageSeqs(got), []int64{2, 5}) {
				t.Errorf("MessagesAfter(a,1) = %v, %v; want [2 5]", messageSeqs(got), err)
			}
			got, err = tx.MessagesAfter(ctx, a.ID, 0, ab2store.MessageFilter{}, 2)
			if err != nil || !reflect.DeepEqual(messageSeqs(got), []int64{1, 2}) {
				t.Errorf("MessagesAfter(a,0,limit 2) = %v, %v; want [1 2]", messageSeqs(got), err)
			}
			got, err = tx.MessagesAfter(ctx, b.ID, 0, ab2store.MessageFilter{}, 10)
			if err != nil || !reflect.DeepEqual(messageSeqs(got), []int64{3, 6}) {
				t.Errorf("MessagesAfter(b,0) = %v, %v; want [3 6]", messageSeqs(got), err)
			}
			if _, err := tx.MessagesAfter(ctx, a.ID, 0, ab2store.MessageFilter{}, 0); !errors.Is(err, ab2store.ErrInvalid) {
				t.Errorf("limit 0 err = %v, want ErrInvalid", err)
			}
			return nil
		})
	})
}

func TestMessagesInRangeWithTagFilter(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		a := mustChannel(t, s, "a", ab2store.KindOrdinary, "", nil)
		tagsets := [][]string{{"env:prod", "team:a"}, {"env:dev"}, {"env:dev", "team:a"}, nil, {"team:a"}}
		for _, tg := range tagsets {
			mustAppend(t, s, ab2store.NewMessage{ChannelID: a.ID, Sender: "pat", Content: "m", Tags: tg})
		}
		pats := func(ps ...string) []ab2store.TagPattern {
			var out []ab2store.TagPattern
			for _, p := range ps {
				out = append(out, ab2store.NewTagPattern(p))
			}
			return out
		}
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			check := func(name string, from, to int64, ps []ab2store.TagPattern, want []int64) {
				t.Helper()
				got, err := tx.MessagesInRange(ctx, a.ID, from, to, ps, ab2store.MessageFilter{}, 10)
				if err != nil || !reflect.DeepEqual(messageSeqs(got), want) {
					t.Errorf("%s = %v, %v; want %v", name, messageSeqs(got), err, want)
				}
			}
			check("no patterns", 1, 5, nil, []int64{1, 2, 3, 4, 5})
			check("inclusive range", 2, 4, nil, []int64{2, 3, 4})
			check("env* and team:a", 1, 5, pats("env*", "team:a"), []int64{1, 3})
			check("env:dev", 1, 5, pats("env:dev"), []int64{2, 3})
			check("range and pattern", 2, 5, pats("team:a"), []int64{3, 5})
			return nil
		})
	})
}

func TestMessageTombstone(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		a := mustChannel(t, s, "a", ab2store.KindOrdinary, "", nil)
		seq := mustAppend(t, s, ab2store.NewMessage{ChannelID: a.ID, Sender: "pat", Content: "m"})
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			if ok, err := tx.TombstoneMessage(ctx, seq, fixedNow+7); err != nil || !ok {
				t.Errorf("first = %v, %v; want true", ok, err)
			}
			if ok, err := tx.TombstoneMessage(ctx, seq, fixedNow+9); err != nil || ok {
				t.Errorf("second = %v, %v; want false", ok, err)
			}
			if ok, err := tx.TombstoneMessage(ctx, 999, fixedNow); err != nil || ok {
				t.Errorf("unknown = %v, %v; want false", ok, err)
			}
			return nil
		})
		m := messageGet(t, s, seq)
		if !m.Tombstone || m.TombstoneAt == nil || *m.TombstoneAt != fixedNow+7 {
			t.Errorf("Message = %+v, want tombstoned at %d", m, fixedNow+7)
		}
	})
}

func TestMessageDeleteCascades(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, rawDSN := openStore(t, e, sqlstore.Options{})
		a := mustChannel(t, s, "a", ab2store.KindOrdinary, "", nil)
		one, two := int64(1), int64(2)
		s1 := mustAppend(t, s, ab2store.NewMessage{ChannelID: a.ID, Sender: "pat", Content: "x", Tags: []string{"t"}, MemoryID: &one, Revision: &one})
		s2 := mustAppend(t, s, ab2store.NewMessage{ChannelID: a.ID, Sender: "pat", Content: "y", Tags: []string{"t"}, MemoryID: &two, Revision: &one})
		s3 := mustAppend(t, s, ab2store.NewMessage{ChannelID: a.ID, Sender: "pat", Content: "z", Tags: []string{"t"}})
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			if err := tx.PutEmbedding(ctx, ab2store.Embedding{Seq: s1, Model: "m", Vector: []byte{1}}); err != nil {
				return err
			}
			return tx.PutEmbedFailure(ctx, ab2store.EmbedFailure{Seq: s2, Model: "m", Error: "bad", FailedAt: fixedNow})
		})
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			return tx.DeleteMessages(ctx, []int64{s1, s2})
		})
		db := rawDB(t, e, rawDSN)
		for _, tbl := range []string{"embeddings", "embed_failures"} {
			if n := scalar[int](t, db, "SELECT count(*) FROM "+tbl); n != 0 {
				t.Errorf("%s rows = %d, want 0", tbl, n)
			}
		}
		if n := scalar[int](t, db, "SELECT count(*) FROM message_tags"); n != 1 {
			t.Errorf("message_tags rows = %d, want 1 (survivor %d)", n, s3)
		}
		if n := scalar[int](t, db, "SELECT count(*) FROM messages"); n != 1 {
			t.Errorf("messages rows = %d, want 1", n)
		}
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			if err := tx.DeleteMessages(ctx, nil); err != nil {
				t.Errorf("DeleteMessages(nil) = %v", err)
			}
			return tx.DeleteMessages(ctx, []int64{s1}) // a second delete is a no-op
		})
	})
}

func TestMessageExpiredTombstones(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		a := mustChannel(t, s, "a", ab2store.KindOrdinary, "", nil)
		for i := 0; i < 4; i++ {
			mustAppend(t, s, ab2store.NewMessage{ChannelID: a.ID, Sender: "pat", Content: "m"})
		}
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			for seq, at := range map[int64]int64{1: 100, 2: 200, 3: 300} {
				if _, err := tx.TombstoneMessage(ctx, seq, at); err != nil {
					return err
				}
			}
			return nil
		})
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			got, err := tx.ExpiredTombstones(ctx, 300, 10)
			if err != nil || !reflect.DeepEqual(seqsOf(got), []int64{1, 2}) {
				t.Errorf("ExpiredTombstones(300) = %v, %v; want [1 2]", got, err)
			}
			got, err = tx.ExpiredTombstones(ctx, 1000, 2)
			if err != nil || !reflect.DeepEqual(seqsOf(got), []int64{1, 2}) {
				t.Errorf("ExpiredTombstones(limit 2) = %v, %v; want [1 2]", got, err)
			}
			return nil
		})
	})
}

func TestMessageOrdinaryBefore(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		a := mustChannel(t, s, "a", ab2store.KindOrdinary, "", nil)
		b := mustChannel(t, s, "b", ab2store.KindOrdinary, "", nil)
		mem := int64(1)
		mustAppend(t, s, ab2store.NewMessage{ChannelID: a.ID, Sender: "p", Content: "m", CreatedAt: 100})                                 // 1
		mustAppend(t, s, ab2store.NewMessage{ChannelID: a.ID, Sender: "p", Content: "m", CreatedAt: 100, MemoryID: &mem, Revision: &mem}) // 2, memory
		mustAppend(t, s, ab2store.NewMessage{ChannelID: b.ID, Sender: "p", Content: "m", CreatedAt: 150})                                 // 3
		mustAppend(t, s, ab2store.NewMessage{ChannelID: a.ID, Sender: "p", Content: "m", CreatedAt: 200})                                 // 4
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			got, err := tx.OrdinaryMessagesBefore(ctx, 200, 10)
			want := []ab2store.SeqChannel{{Seq: 1, ChannelID: a.ID, Bytes: 32 + 1 + 1 + 5}, {Seq: 3, ChannelID: b.ID, Bytes: 32 + 1 + 1 + 5}} // sender "p", content "m", sender_kind "agent"; the other columns are empty
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Errorf("OrdinaryMessagesBefore = %v, %v; want %v", got, err, want)
			}
			got, err = tx.OrdinaryMessagesBefore(ctx, 1000, 1)
			if err != nil || !reflect.DeepEqual(got, want[:1]) {
				t.Errorf("limit 1 = %v, %v; want %v", got, err, want[:1])
			}
			return nil
		})
	})
}

func TestMessageSurvivesChannelRename(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		a := mustChannel(t, s, "a", ab2store.KindOrdinary, "", nil)
		mustAppend(t, s, ab2store.NewMessage{ChannelID: a.ID, Sender: "p", Content: "m"})
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error { return tx.RenameChannel(ctx, a.ID, "renamed") })
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			got, err := tx.MessagesAfter(ctx, a.ID, 0, ab2store.MessageFilter{}, 10)
			if err != nil || !reflect.DeepEqual(messageSeqs(got), []int64{1}) {
				t.Errorf("after rename = %v, %v; want [1]", messageSeqs(got), err)
			}
			return nil
		})
	})
}

func TestMessageDeletedWithChannel(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, rawDSN := openStore(t, e, sqlstore.Options{})
		a := mustChannel(t, s, "a", ab2store.KindOrdinary, "", nil)
		mem := int64(1)
		seq := mustAppend(t, s, ab2store.NewMessage{ChannelID: a.ID, Sender: "p", Content: "m", Tags: []string{"t"}, MemoryID: &mem, Revision: &mem})
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			return tx.PutEmbedding(ctx, ab2store.Embedding{Seq: seq, Model: "m", Vector: []byte{1}})
		})
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error { return tx.DeleteChannel(ctx, a.ID) })
		db := rawDB(t, e, rawDSN)
		for _, tbl := range []string{"messages", "message_tags", "embeddings"} {
			if n := scalar[int](t, db, "SELECT count(*) FROM "+tbl); n != 0 {
				t.Errorf("%s rows = %d, want 0", tbl, n)
			}
		}
	})
}

func TestMessageSearchTextWiring(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		a := mustChannel(t, s, "a", ab2store.KindOrdinary, "", nil)
		b := mustChannel(t, s, "b", ab2store.KindOrdinary, "", nil)
		hello := mustAppend(t, s, ab2store.NewMessage{ChannelID: a.ID, Sender: "p", Content: "hello world"})
		mustAppend(t, s, ab2store.NewMessage{ChannelID: a.ID, Sender: "p", Content: "goodbye"})
		elsewhere := mustAppend(t, s, ab2store.NewMessage{ChannelID: b.ID, Sender: "p", Content: "hello elsewhere"})
		gone := mustAppend(t, s, ab2store.NewMessage{ChannelID: a.ID, Sender: "p", Content: "hello tombstoned"})
		mustTombstone(t, s, gone)
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			if _, err := tx.SearchText(ctx, search.Query{}, []int64{a.ID}, ab2store.SearchFilter{}, 10); !errors.Is(err, ab2store.ErrInvalid) {
				t.Errorf("empty query err = %v, want ErrInvalid", err)
			}
			if _, err := tx.SearchText(ctx, search.Query{}, nil, ab2store.SearchFilter{}, 10); !errors.Is(err, ab2store.ErrInvalid) {
				t.Errorf("empty query, no channels err = %v, want ErrInvalid", err)
			}
			got, err := tx.SearchText(ctx, search.Parse("hello"), []int64{a.ID}, ab2store.SearchFilter{}, 10)
			if err != nil || !reflect.DeepEqual(messageSeqs(got), []int64{hello}) {
				t.Errorf("hello on a = %v, %v; want [%d]", messageSeqs(got), err, hello)
			}
			got, err = tx.SearchText(ctx, search.Parse("hello"), nil, ab2store.SearchFilter{}, 10)
			if err != nil || got == nil || len(got) != 0 {
				t.Errorf("hello on no channels = %v, %v; want empty non-nil", got, err)
			}
			got, err = tx.SearchText(ctx, search.Parse("missing"), []int64{a.ID, b.ID}, ab2store.SearchFilter{}, 10)
			if err != nil || len(got) != 0 {
				t.Errorf("missing = %v, %v; want empty", got, err)
			}
			got, err = tx.SearchText(ctx, search.Parse("hello"), []int64{a.ID, b.ID}, ab2store.SearchFilter{}, 1)
			if err != nil || len(got) != 1 || got[0].Seq != elsewhere {
				t.Errorf("limit 1 = %v, %v; want the newest match [%d]", messageSeqs(got), err, elsewhere)
			}
			return nil
		})
	})
}

func TestAppendSenderKind(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		ch := mustChannel(t, s, "a", ab2store.KindOrdinary, "", nil)
		human := mustAppend(t, s, ab2store.NewMessage{ChannelID: ch.ID, Sender: "eric@corp.example", SenderKind: ab2store.IdentityHuman, Content: "h"})
		dflt := mustAppend(t, s, ab2store.NewMessage{ChannelID: ch.ID, Sender: "pat", Content: "d"})
		agent := mustAppend(t, s, ab2store.NewMessage{ChannelID: ch.ID, Sender: "pat", SenderKind: ab2store.IdentityAgent, Content: "a"})
		for seq, want := range map[int64]ab2store.IdentityKind{human: ab2store.IdentityHuman, dflt: ab2store.IdentityAgent, agent: ab2store.IdentityAgent} {
			if got := messageGet(t, s, seq); got.SenderKind != want {
				t.Errorf("Message(%d).SenderKind = %q, want %q", seq, got.SenderKind, want)
			}
		}
		for _, bad := range []ab2store.IdentityKind{"robot", "Human"} {
			err := s.Append(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
				_, err := tx.AppendMessage(ctx, ab2store.NewMessage{ChannelID: ch.ID, Sender: "pat", SenderKind: bad, Content: "x", CreatedAt: fixedNow})
				return err
			})
			if !errors.Is(err, ab2store.ErrInvalid) {
				t.Errorf("SenderKind %q err = %v, want ErrInvalid", bad, err)
			}
		}
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			if n, err := tx.MaxSeq(ctx); err != nil || n != agent {
				t.Errorf("MaxSeq = %d, %v; want %d (no row from a refused append)", n, err, agent)
			}
			return nil
		})
	})
}

// seqsOf lists the seqs of refs.
func seqsOf(refs []ab2store.SeqChannel) []int64 {
	out := make([]int64, len(refs))
	for i, r := range refs {
		out[i] = r.Seq
	}
	return out
}
