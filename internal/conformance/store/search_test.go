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

// searchCorpus is the fixed corpus of Milestone 2 Task 11: row r<n> has seq n.
// Channel "b" marks the channel-scoping row; every other row is on "a".
var searchCorpus = []struct{ ch, subject, content string }{
	{"a", "", "the rate limit hit"},         // r1
	{"a", "", "the limit rate hit"},         // r2
	{"a", "", "rate of the limit"},          // r3
	{"a", "rate", "limit hit"},              // r4
	{"a", "", "Rate hit"},                   // r5
	{"a", "", "commit:e159e8c landed"},      // r6
	{"a", "", "commit abc e159e8c"},         // r7
	{"a", "", "e159e8c commit"},             // r8
	{"a", "", "committed:e159e8c"},          // r9
	{"a", "", "see owner/repo@e159e8c now"}, // r10
	{"a", "", "owner repo@e159e8c"},         // r11
	{"a", "", "commit:d159"},                // r12
	{"a", "", "foo-bar"},                    // r13
	{"a", "", "foo bar"},                    // r14
	{"a", "", "café"},                       // r15
	{"a", "", "cafe"},                       // r16
	{"b", "", "rate limit on B"},            // r17
	{"a", "", "tombstoned rate limit"},      // r18
}

func TestSearchTextMatchesAgreeAcrossEngines(t *testing.T) {
	cases := []struct {
		name  string
		q     string
		onlyA bool
		limit int
		want  []int64
	}{
		{"word", "rate", false, 100, []int64{1, 2, 3, 4, 5, 17}},
		{"two words", "rate limit", false, 100, []int64{1, 2, 3, 4, 17}},
		{"phrase", `"rate limit"`, false, 100, []int64{1, 17}},
		{"prefix", "e159*", false, 100, []int64{6, 7, 8, 9, 10, 11}},
		{"punctuated", "commit:e159e8c", false, 100, []int64{6}},
		{"punctuated prefix", "commit:e159*", false, 100, []int64{6}},
		{"path compound", "owner/repo@e159*", false, 100, []int64{10, 11}},
		{"hyphen", "foo-bar", false, 100, []int64{13, 14}},
		{"hyphen phrase", `"foo bar"`, false, 100, []int64{13, 14}},
		{"no diacritic folding", "cafe", false, 100, []int64{16}},
		{"diacritic exact", "café", false, 100, []int64{15}},
		{"missing", "missing", false, 100, []int64{}},
		{"channel scoping", "rate", true, 100, []int64{1, 2, 3, 4, 5}},
		{"limit", "rate", false, 2, []int64{1, 2}},
	}
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		ch := map[string]ab2store.Channel{
			"a": mustChannel(t, s, "a", ab2store.KindOrdinary, "", nil),
			"b": mustChannel(t, s, "b", ab2store.KindOrdinary, "", nil),
		}
		for i, r := range searchCorpus {
			seq := mustAppend(t, s, ab2store.NewMessage{ChannelID: ch[r.ch].ID, Sender: "p", Subject: r.subject, Content: r.content})
			if seq != int64(i+1) {
				t.Fatalf("row r%d got seq %d", i+1, seq)
			}
		}
		mustTombstone(t, s, 18)
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				ids := []int64{ch["a"].ID, ch["b"].ID}
				if c.onlyA {
					ids = ids[:1]
				}
				mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
					got, err := tx.SearchText(ctx, search.Parse(c.q), ids, ab2store.SearchFilter{}, c.limit)
					if err != nil || !reflect.DeepEqual(messageSeqs(got), c.want) {
						t.Errorf("%q = %v, %v; want %v", c.q, messageSeqs(got), err, c.want)
					}
					return nil
				})
			})
		}
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			if _, err := tx.SearchText(ctx, search.Query{}, []int64{ch["a"].ID}, ab2store.SearchFilter{}, 10); !errors.Is(err, ab2store.ErrInvalid) {
				t.Errorf("empty query err = %v, want ErrInvalid", err)
			}
			return nil
		})
	})
}
