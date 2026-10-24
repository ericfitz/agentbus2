package store

import (
	"context"
	"reflect"
	"testing"

	"github.com/ericfitz/agentbus2/internal/dbtest"
	"github.com/ericfitz/agentbus2/internal/search"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/store/sqlstore"
)

func pats(ps ...string) []ab2store.TagPattern {
	out := []ab2store.TagPattern{}
	for _, p := range ps {
		out = append(out, ab2store.NewTagPattern(p))
	}
	return out
}

func TestMessagesAfterExcludesSenderAndSession(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		a := mustChannel(t, s, "a", ab2store.KindOrdinary, "", nil)
		mustAppend(t, s, ab2store.NewMessage{ChannelID: a.ID, Sender: "pat", SessionID: "s1", Content: "1"})
		mustAppend(t, s, ab2store.NewMessage{ChannelID: a.ID, Sender: "pat", SessionID: "s2", Content: "2"})
		mustAppend(t, s, ab2store.NewMessage{ChannelID: a.ID, Sender: "kim", SessionID: "s3", Content: "3"})
		mustAppend(t, s, ab2store.NewMessage{ChannelID: a.ID, Sender: "kim", SessionID: "s1", Content: "4"})
		cases := []struct {
			name  string
			f     ab2store.MessageFilter
			limit int
			want  []int64
		}{
			{"none", ab2store.MessageFilter{}, 10, []int64{1, 2, 3, 4}},
			{"sender", ab2store.MessageFilter{ExcludeSender: "pat"}, 10, []int64{3, 4}},
			{"session", ab2store.MessageFilter{ExcludeSessionID: "s1"}, 10, []int64{2, 3}},
			{"both", ab2store.MessageFilter{ExcludeSender: "pat", ExcludeSessionID: "s1"}, 10, []int64{3}},
			{"limit after exclusion", ab2store.MessageFilter{ExcludeSender: "pat"}, 1, []int64{3}},
		}
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			for _, c := range cases {
				got, err := tx.MessagesAfter(ctx, a.ID, 0, c.f, c.limit)
				if err != nil || !reflect.DeepEqual(messageSeqs(got), c.want) {
					t.Errorf("%s = %v, %v; want %v", c.name, messageSeqs(got), err, c.want)
				}
			}
			return nil
		})
	})
}

func TestMessagesInRangeExcludes(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		a := mustChannel(t, s, "a", ab2store.KindOrdinary, "", nil)
		mustAppend(t, s, ab2store.NewMessage{ChannelID: a.ID, Sender: "pat", SessionID: "s1", Content: "1", Tags: []string{"x"}})
		mustAppend(t, s, ab2store.NewMessage{ChannelID: a.ID, Sender: "kim", SessionID: "s2", Content: "2", Tags: []string{"x"}})
		mustAppend(t, s, ab2store.NewMessage{ChannelID: a.ID, Sender: "kim", SessionID: "s1", Content: "3", Tags: []string{"y"}})
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			got, err := tx.MessagesInRange(ctx, a.ID, 1, 3, nil, ab2store.MessageFilter{ExcludeSender: "pat"}, 10)
			if err != nil || !reflect.DeepEqual(messageSeqs(got), []int64{2, 3}) {
				t.Errorf("sender = %v, %v", messageSeqs(got), err)
			}
			got, err = tx.MessagesInRange(ctx, a.ID, 1, 3, pats("x"), ab2store.MessageFilter{ExcludeSessionID: "s1"}, 10)
			if err != nil || !reflect.DeepEqual(messageSeqs(got), []int64{2}) {
				t.Errorf("session+pattern = %v, %v", messageSeqs(got), err)
			}
			return nil
		})
	})
}

func TestMessagesBySeqs(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		a := mustChannel(t, s, "a", ab2store.KindOrdinary, "", nil)
		for i := 0; i < 5; i++ {
			mustAppend(t, s, ab2store.NewMessage{ChannelID: a.ID, Sender: "pat", Content: "m", Tags: []string{"t"}})
		}
		mustTombstone(t, s, 3)
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			got, err := tx.MessagesBySeqs(ctx, []int64{5, 3, 1, 99, 2})
			if err != nil || !reflect.DeepEqual(messageSeqs(got), []int64{1, 2, 5}) {
				t.Errorf("MessagesBySeqs = %v, %v; want [1 2 5]", messageSeqs(got), err)
			}
			if len(got) > 0 && !reflect.DeepEqual(got[0].Tags, []string{"t"}) {
				t.Errorf("tags = %v, want [t]", got[0].Tags)
			}
			for _, in := range [][]int64{nil, {}} {
				got, err = tx.MessagesBySeqs(ctx, in)
				if err != nil || got == nil || len(got) != 0 {
					t.Errorf("MessagesBySeqs(%v) = %v, %v; want empty non-nil", in, got, err)
				}
			}
			return nil
		})
	})
}

func TestMessagesBeforeDescending(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		a := mustChannel(t, s, "a", ab2store.KindOrdinary, "", nil)
		b := mustChannel(t, s, "b", ab2store.KindOrdinary, "", nil)
		mustAppend(t, s, ab2store.NewMessage{ChannelID: a.ID, Sender: "pat", Content: "1", Tags: []string{"x"}})
		mustAppend(t, s, ab2store.NewMessage{ChannelID: a.ID, Sender: "kim", Content: "2", Tags: []string{"x"}})
		mustAppend(t, s, ab2store.NewMessage{ChannelID: b.ID, Sender: "kim", Content: "3", Tags: []string{"x"}})
		mustAppend(t, s, ab2store.NewMessage{ChannelID: a.ID, Sender: "kim", Content: "4", Tags: []string{"y"}})
		mustAppend(t, s, ab2store.NewMessage{ChannelID: a.ID, Sender: "pat", Content: "5", Tags: []string{"x"}})
		mustAppend(t, s, ab2store.NewMessage{ChannelID: a.ID, Sender: "pat", Content: "6", Tags: []string{"x"}})
		mustTombstone(t, s, 5)
		cases := []struct {
			name   string
			before int64
			ps     []ab2store.TagPattern
			f      ab2store.MessageFilter
			limit  int
			want   []int64
		}{
			{"descending", 6, nil, ab2store.MessageFilter{}, 10, []int64{4, 2, 1}},
			{"strictly before", 4, nil, ab2store.MessageFilter{}, 10, []int64{2, 1}},
			{"limit keeps newest", 7, nil, ab2store.MessageFilter{}, 2, []int64{6, 4}},
			{"patterns", 7, pats("x"), ab2store.MessageFilter{}, 10, []int64{6, 2, 1}},
			{"exclusion", 7, pats("x"), ab2store.MessageFilter{ExcludeSender: "pat"}, 10, []int64{2}},
			{"none", 1, nil, ab2store.MessageFilter{}, 10, []int64{}},
		}
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			for _, c := range cases {
				got, err := tx.MessagesBefore(ctx, a.ID, c.before, c.ps, c.f, c.limit)
				if err != nil || !reflect.DeepEqual(messageSeqs(got), c.want) {
					t.Errorf("%s = %v, %v; want %v", c.name, messageSeqs(got), err, c.want)
				}
			}
			if _, err := tx.MessagesBefore(ctx, a.ID, 5, nil, ab2store.MessageFilter{}, 0); err == nil {
				t.Error("limit 0: want ErrInvalid")
			}
			return nil
		})
	})
}

func TestSearchTextFilters(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		a := mustChannel(t, s, "a", ab2store.KindOrdinary, "", nil)
		app := func(sender string, at int64, content string, reply *int64, tags ...string) int64 {
			return mustAppend(t, s, ab2store.NewMessage{ChannelID: a.ID, Sender: sender, CreatedAt: at, Content: content, ReplyTo: reply, Tags: tags})
		}
		r1 := app("pat", 100, "rate one", nil, "x", "y")
		app("kim", 200, "rate two", &r1, "x")
		app("pat", 300, "rate three", nil, "y")
		app("kim", 400, "rate four", &r1)
		i64 := func(v int64) *int64 { return &v }
		cases := []struct {
			name string
			f    ab2store.SearchFilter
			want []int64
		}{
			{"none", ab2store.SearchFilter{}, []int64{4, 3, 2, 1}},
			{"sender", ab2store.SearchFilter{Sender: "kim"}, []int64{4, 2}},
			{"since inclusive", ab2store.SearchFilter{Since: i64(200)}, []int64{4, 3, 2}},
			{"until inclusive", ab2store.SearchFilter{Until: i64(300)}, []int64{3, 2, 1}},
			{"since and until", ab2store.SearchFilter{Since: i64(200), Until: i64(300)}, []int64{3, 2}},
			{"thread", ab2store.SearchFilter{Thread: i64(r1)}, []int64{4, 2, 1}},
			{"pattern", ab2store.SearchFilter{Patterns: pats("x")}, []int64{2, 1}},
			{"patterns all", ab2store.SearchFilter{Patterns: pats("x", "y")}, []int64{1}},
			{"combined", ab2store.SearchFilter{Sender: "kim", Thread: i64(r1), Patterns: pats("x")}, []int64{2}},
		}
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			for _, c := range cases {
				got, err := tx.SearchText(ctx, search.Parse("rate"), []int64{a.ID}, c.f, 10)
				if err != nil || !reflect.DeepEqual(messageSeqs(got), c.want) {
					t.Errorf("%s = %v, %v; want %v", c.name, messageSeqs(got), err, c.want)
				}
			}
			return nil
		})
	})
}

func TestEmbeddingByChannelsFiltered(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		a := mustChannel(t, s, "a", ab2store.KindMemory, "", nil)
		var seqs []int64
		for i, sender := range []string{"pat", "kim", "kim"} {
			memID, rev := int64(i+1), int64(1)
			seqs = append(seqs, mustAppend(t, s, ab2store.NewMessage{ChannelID: a.ID, Sender: sender, CreatedAt: int64(100 * (i + 1)), Content: "mem", MemoryID: &memID, Revision: &rev}))
		}
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			for _, seq := range seqs {
				if err := tx.PutEmbedding(ctx, ab2store.Embedding{Seq: seq, Model: "m", Vector: []byte{1}}); err != nil {
					return err
				}
			}
			return nil
		})
		since := int64(300)
		cases := []struct {
			name string
			f    ab2store.SearchFilter
			want int
		}{
			{"none", ab2store.SearchFilter{}, 3},
			{"sender", ab2store.SearchFilter{Sender: "kim"}, 2},
			{"since", ab2store.SearchFilter{Since: &since}, 1},
			{"sender and since", ab2store.SearchFilter{Sender: "pat", Since: &since}, 0},
		}
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			for _, c := range cases {
				got, err := tx.Embeddings(ctx, "m", []int64{a.ID}, c.f)
				if err != nil || len(got) != c.want {
					t.Errorf("%s = %v, %v; want %d", c.name, got, err, c.want)
				}
			}
			return nil
		})
	})
}

func TestTagSubMatchesUpToAndExcludes(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		mustIdentity(t, s, "kim")
		a := mustChannel(t, s, "a", ab2store.KindOrdinary, "", nil)
		tagSubscribe(t, s, tagSet("kim", "x", 0, "x"))
		mustAppend(t, s, ab2store.NewMessage{ChannelID: a.ID, Sender: "pat", SessionID: "s1", Content: "1", Tags: []string{"x"}})
		mustAppend(t, s, ab2store.NewMessage{ChannelID: a.ID, Sender: "kim", SessionID: "s2", Content: "2", Tags: []string{"x"}})
		mustAppend(t, s, ab2store.NewMessage{ChannelID: a.ID, Sender: "pat", SessionID: "s3", Content: "3", Tags: []string{"x"}})
		cases := []struct {
			name string
			up   int64
			f    ab2store.MessageFilter
			want []int64
		}{
			{"unbounded", 0, ab2store.MessageFilter{}, []int64{1, 2, 3}},
			{"upTo inclusive", 2, ab2store.MessageFilter{}, []int64{1, 2}},
			{"exclude session", 0, ab2store.MessageFilter{ExcludeSessionID: "s2"}, []int64{1, 3}},
			{"exclude sender", 0, ab2store.MessageFilter{ExcludeSender: "kim"}, []int64{1, 3}},
			{"both", 2, ab2store.MessageFilter{ExcludeSessionID: "s1"}, []int64{2}},
		}
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			for _, c := range cases {
				got, err := tx.TagMatchesAfter(ctx, "kim", 0, c.up, []int64{a.ID}, c.f, 10)
				if err != nil || !reflect.DeepEqual(messageSeqs(got), c.want) {
					t.Errorf("%s = %v, %v; want %v", c.name, messageSeqs(got), err, c.want)
				}
			}
			return nil
		})
	})
}

func TestEmbedFailuresCountAndLast(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		a := mustChannel(t, s, "a", ab2store.KindMemory, "", nil)
		one := embeddingMemory(t, s, a.ID, 1)
		two := embeddingMemory(t, s, a.ID, 2)
		three := embeddingMemory(t, s, a.ID, 3)
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			for i, seq := range []int64{one, two, three} {
				if err := tx.PutEmbedFailure(ctx, ab2store.EmbedFailure{Seq: seq, Model: "m", Error: "e" + string(rune('1'+i)), FailedAt: fixedNow + int64(i)}); err != nil {
					return err
				}
			}
			return tx.PutEmbedFailure(ctx, ab2store.EmbedFailure{Seq: one, Model: "other", Error: "o", FailedAt: fixedNow + 99})
		})
		check := func(model string, wantN int, want ab2store.EmbedFailure) {
			t.Helper()
			mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
				n, last, err := tx.EmbedFailures(ctx, model)
				if err != nil || n != wantN || last != want {
					t.Errorf("EmbedFailures(%q) = %d, %+v, %v; want %d, %+v", model, n, last, err, wantN, want)
				}
				return nil
			})
		}
		check("m", 3, ab2store.EmbedFailure{Seq: three, Model: "m", Error: "e3", FailedAt: fixedNow + 2})
		mustTombstone(t, s, three)
		check("m", 2, ab2store.EmbedFailure{Seq: two, Model: "m", Error: "e2", FailedAt: fixedNow + 1})
		check("other", 1, ab2store.EmbedFailure{Seq: one, Model: "other", Error: "o", FailedAt: fixedNow + 99})
		check("unknown", 0, ab2store.EmbedFailure{})
	})
}

func TestMessagesThreadFilter(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		a := mustChannel(t, s, "a", ab2store.KindOrdinary, "", nil)
		root := mustAppend(t, s, ab2store.NewMessage{ChannelID: a.ID, Sender: "pat", Content: "root"})
		mustAppend(t, s, ab2store.NewMessage{ChannelID: a.ID, Sender: "pat", Content: "plain"})
		r1 := mustAppend(t, s, ab2store.NewMessage{ChannelID: a.ID, Sender: "pat", Content: "r1", ReplyTo: &root})
		mustAppend(t, s, ab2store.NewMessage{ChannelID: a.ID, Sender: "pat", Content: "r1a", ReplyTo: &r1})
		r2 := mustAppend(t, s, ab2store.NewMessage{ChannelID: a.ID, Sender: "pat", Content: "r2", ReplyTo: &root})
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			f := ab2store.MessageFilter{Thread: &root}
			got, err := tx.MessagesInRange(ctx, a.ID, 1, 10, nil, f, 10)
			if want := []int64{root, r1, r2}; err != nil || !reflect.DeepEqual(messageSeqs(got), want) {
				t.Errorf("MessagesInRange thread = %v, %v; want %v", messageSeqs(got), err, want)
			}
			got, err = tx.MessagesBefore(ctx, a.ID, 10, nil, f, 10)
			if want := []int64{r2, r1, root}; err != nil || !reflect.DeepEqual(messageSeqs(got), want) {
				t.Errorf("MessagesBefore thread = %v, %v; want %v", messageSeqs(got), err, want)
			}
			got, err = tx.MessagesInRange(ctx, a.ID, 1, 10, nil, ab2store.MessageFilter{}, 10)
			if err != nil || len(got) != 5 {
				t.Errorf("MessagesInRange nil = %v, %v; want all 5", messageSeqs(got), err)
			}
			got, err = tx.MessagesBefore(ctx, a.ID, 10, nil, ab2store.MessageFilter{}, 10)
			if err != nil || len(got) != 5 {
				t.Errorf("MessagesBefore nil = %v, %v; want all 5", messageSeqs(got), err)
			}
			return nil
		})
	})
}
