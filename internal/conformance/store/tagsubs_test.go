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

func tagSet(identity, key string, created int64, patterns ...string) ab2store.TagSubscription {
	ps := make([]ab2store.TagPattern, len(patterns))
	for i, p := range patterns {
		ps[i] = ab2store.NewTagPattern(p)
	}
	return ab2store.TagSubscription{Identity: identity, TagsKey: key, Patterns: ps, CreatedSeq: created}
}

func tagSubscribe(t *testing.T, s ab2store.Store, sub ab2store.TagSubscription) {
	t.Helper()
	mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error { return tx.SubscribeTags(ctx, sub) })
}

func tagSubs(t *testing.T, s ab2store.Store, identity string) []ab2store.TagSubscription {
	t.Helper()
	var out []ab2store.TagSubscription
	mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) (err error) {
		out, err = tx.TagSubscriptions(ctx, identity)
		return err
	})
	return out
}

func tagMatches(t *testing.T, s ab2store.Store, identity string, after int64, chans []int64, limit int) []ab2store.Message {
	t.Helper()
	var out []ab2store.Message
	mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) (err error) {
		out, err = tx.TagMatchesAfter(ctx, identity, after, 0, chans, ab2store.MessageFilter{}, limit)
		return err
	})
	return out
}

func TestTagSubStoresPatternsWithRanges(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		mustIdentity(t, s, "kim")
		want := tagSet("kim", "env*,team:a", 4, "env*", "team:a")
		tagSubscribe(t, s, want)
		tagSubscribe(t, s, tagSet("kim", "env*,team:a", 99, "other")) // present: no-op
		got := tagSubs(t, s, "kim")
		if len(got) != 1 || !reflect.DeepEqual(got[0], want) {
			t.Fatalf("TagSubscriptions = %+v, want [%+v]", got, want)
		}
		if got[0].Patterns[0].Lo != "env" || got[0].Patterns[0].Hi != "env~" {
			t.Errorf("prefix range = %+v", got[0].Patterns[0])
		}
		err := s.Tx(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
			return tx.SubscribeTags(ctx, tagSet("nobody", "x", 0, "x"))
		})
		if !errors.Is(err, ab2store.ErrNotFound) {
			t.Errorf("SubscribeTags unknown identity = %v, want ErrNotFound", err)
		}
		err = s.Tx(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
			return tx.SubscribeTags(ctx, ab2store.TagSubscription{Identity: "kim", TagsKey: "empty"})
		})
		if !errors.Is(err, ab2store.ErrInvalid) {
			t.Errorf("SubscribeTags with no patterns = %v, want ErrInvalid", err)
		}
	})
}

func TestTagSubUnsubscribeCascades(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, rawDSN := openStore(t, e, sqlstore.Options{})
		mustIdentity(t, s, "kim")
		tagSubscribe(t, s, tagSet("kim", "a", 0, "a"))
		tagSubscribe(t, s, tagSet("kim", "b,c", 0, "b", "c"))
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error { return tx.UnsubscribeTags(ctx, "kim", "b,c") })
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error { return tx.UnsubscribeTags(ctx, "kim", "b,c") }) // absent: no-op
		got := tagSubs(t, s, "kim")
		if len(got) != 1 || got[0].TagsKey != "a" {
			t.Fatalf("TagSubscriptions = %+v, want only set a", got)
		}
		db := rawDB(t, e, rawDSN)
		if n := tableCount(t, e, db, "tag_subscription_tags"); n != 1 {
			t.Errorf("tag_subscription_tags rows = %d, want 1 (the surviving set's)", n)
		}
	})
}

func TestTagSubMatchesAfter(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		mustIdentity(t, s, "kim")
		chA := mustChannel(t, s, "a", ab2store.KindOrdinary, "", nil)
		chB := mustChannel(t, s, "b", ab2store.KindOrdinary, "", nil)
		chC := mustChannel(t, s, "c", ab2store.KindOrdinary, "", nil)
		app := func(ch ab2store.Channel, tags ...string) int64 {
			return mustAppend(t, s, ab2store.NewMessage{ChannelID: ch.ID, Sender: "pat", Content: "x", Tags: tags})
		}
		m1 := app(chA, "env:prod")                  // env*, seq above 0
		app(chB, "team:a")                          // half of set 2
		app(chA, "team:a", "prio:high")             // set 2, but seq 3 is not above 3
		m4 := app(chB, "team:a", "prio:high", "z")  // set 2, seq 4 above 3
		app(chC, "env:prod", "team:a", "prio:high") // channel C: scoped out
		m6 := app(chA, "env:staging", "env:prod")   // env* once: two tags under one pattern
		m7 := app(chB, "env:dev")                   // tombstoned below
		app(chA, "other")                           // no match
		app(chB, "team:ab", "prio:high")            // team:a is exact: no match
		mustTombstone(t, s, m7)
		tagSubscribe(t, s, tagSet("kim", "env*", 0, "env*"))
		tagSubscribe(t, s, tagSet("kim", "prio:high,team:a", 3, "team:a", "prio:high"))

		chans := []int64{chA.ID, chB.ID}
		if got, want := messageSeqs(tagMatches(t, s, "kim", 0, chans, 100)), []int64{m1, m4, m6}; !reflect.DeepEqual(got, want) {
			t.Errorf("matches = %v, want %v", got, want)
		}
		if got := messageSeqs(tagMatches(t, s, "kim", m1, chans, 100)); !reflect.DeepEqual(got, []int64{m4, m6}) {
			t.Errorf("after %d = %v", m1, got)
		}
		if got := messageSeqs(tagMatches(t, s, "kim", 0, chans, 2)); !reflect.DeepEqual(got, []int64{m1, m4}) {
			t.Errorf("limit 2 = %v", got)
		}
		if got := tagMatches(t, s, "kim", 0, nil, 100); len(got) != 0 {
			t.Errorf("empty channel list = %v, want empty", got)
		}
		if got := tagMatches(t, s, "pat", 0, chans, 100); got == nil || len(got) != 0 {
			t.Errorf("identity without sets = %v, want empty slice", got)
		}
		ms := tagMatches(t, s, "kim", m4-1, chans, 1)
		if len(ms) != 1 || ms[0].Seq != m4 || !reflect.DeepEqual(ms[0].Tags, []string{"prio:high", "team:a", "z"}) {
			t.Errorf("message tags not loaded: %+v", ms)
		}
		err := s.Read(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
			_, err := tx.TagMatchesAfter(ctx, "kim", 0, 0, chans, ab2store.MessageFilter{}, 0)
			return err
		})
		if !errors.Is(err, ab2store.ErrInvalid) {
			t.Errorf("limit 0 = %v, want ErrInvalid", err)
		}
	})
}

// TagSubscriptions lists the sets in byte order of the key and the patterns of
// a set in byte order of the pattern on both engines: '*', '-', digits and ':'
// sort differently under Postgres's database collation than by byte.
func TestTagSubscriptionsAreByteOrdered(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		mustIdentity(t, s, "kim")
		scrambled := []string{"a:b", "a0", "a*", "a-b"}
		wantOrder := []string{"a*", "a-b", "a0", "a:b"}
		for _, k := range scrambled {
			tagSubscribe(t, s, tagSet("kim", k, 1, "x"))
		}
		tagSubscribe(t, s, tagSet("kim", "patterns", 1, scrambled...))
		got := tagSubs(t, s, "kim")
		var keys []string
		for _, g := range got {
			keys = append(keys, g.TagsKey)
		}
		if want := append(append([]string{}, wantOrder...), "patterns"); !equalStrings(keys, want) {
			t.Errorf("TagSubscriptions keys = %q, want byte order %q", keys, want)
		}
		var pats []string
		for _, g := range got {
			if g.TagsKey == "patterns" {
				for _, p := range g.Patterns {
					pats = append(pats, p.Pattern)
				}
			}
		}
		if !equalStrings(pats, wantOrder) {
			t.Errorf("patterns of the set = %q, want byte order %q", pats, wantOrder)
		}
	})
}
