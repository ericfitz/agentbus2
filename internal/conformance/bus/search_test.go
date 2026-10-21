package bus

import (
	"context"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/embed"
	"github.com/ericfitz/agentbus2/internal/core/rules"
	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
)

// searchRow is one appended message of the search tests.
type searchRow struct {
	ch               string
	sender           string
	subject, content string
	replyTo          int64
	tags             []string
}

// appendRow writes r through core.Append, one second after the previous row
// (so row n has created_at fixedNowMs + n*1000 when the clock starts at
// fixedNowMs and rows are appended in order), and returns its seq.
func (h *harness) appendRow(ch ab2store.Channel, r searchRow) int64 {
	h.t.Helper()
	h.clock.Advance(time.Second)
	sender := r.sender
	if sender == "" {
		sender = "pat"
	}
	var reply *int64
	if r.replyTo != 0 {
		reply = &r.replyTo
	}
	var seq int64
	err := h.raw.Append(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
		var err error
		seq, _, err = h.core.Append(ctx, tx, core.AppendInput{
			ChannelID: ch.ID, Channel: ch.Name, Kind: ch.Kind, Sender: sender,
			Subject: r.subject, Content: r.content, ReplyTo: reply, Tags: r.tags,
			TaskRow: ch.Kind == ab2store.KindTasks,
		})
		return err
	})
	if err != nil {
		h.t.Fatalf("appendRow: %v", err)
	}
	return seq
}

// searchCorpus is the Milestone 2 corpus: row r<n> has seq n; channel "b" is
// the scoping row, every other row is on "a".
var searchCorpus = []searchRow{
	{ch: "a", content: "the rate limit hit"},                                        // r1
	{ch: "a", content: "the limit rate hit", replyTo: 1, sender: "kim"},             // r2
	{ch: "a", content: "rate of the limit", replyTo: 1, tags: []string{"env:prod"}}, // r3
	{ch: "a", subject: "rate", content: "limit hit", tags: []string{"environment"}}, // r4
	{ch: "a", content: "Rate hit", sender: "kim"},                                   // r5
	{ch: "a", content: "commit:e159e8c landed"},                                     // r6
	{ch: "a", content: "commit abc e159e8c"},                                        // r7
	{ch: "a", content: "e159e8c commit"},                                            // r8
	{ch: "a", content: "committed:e159e8c"},                                         // r9
	{ch: "a", content: "see owner/repo@e159e8c now"},                                // r10
	{ch: "a", content: "owner repo@e159e8c"},                                        // r11
	{ch: "a", content: "commit:d159"},                                               // r12
	{ch: "a", content: "foo-bar"},                                                   // r13
	{ch: "a", content: "foo bar"},                                                   // r14
	{ch: "a", content: "café"},                                                      // r15
	{ch: "a", content: "cafe"},                                                      // r16
	{ch: "b", content: "rate limit on B"},                                           // r17
	{ch: "a", content: "tombstoned rate limit"},                                     // r18
}

func hitSeqs(r ab2bus.SearchResult) []int64 {
	out := make([]int64, 0, len(r.Hits))
	for _, h := range r.Hits {
		out = append(out, h.Seq)
	}
	return out
}

func sortedSeqs(r ab2bus.SearchResult) []int64 {
	s := hitSeqs(r)
	slices.Sort(s)
	return s
}

func TestSearchMatchesAgreeAcrossEngines(t *testing.T) {
	i64 := func(v int64) *int64 { return &v }
	cases := []struct {
		name string
		in   ab2bus.SearchInput
		want []int64
	}{
		{"word", ab2bus.SearchInput{Query: "rate"}, []int64{1, 2, 3, 4, 5, 17}},
		{"two words", ab2bus.SearchInput{Query: "rate limit"}, []int64{1, 2, 3, 4, 17}},
		{"phrase", ab2bus.SearchInput{Query: `"rate limit"`}, []int64{1, 17}},
		{"prefix", ab2bus.SearchInput{Query: "e159*"}, []int64{6, 7, 8, 9, 10, 11}},
		{"punctuated", ab2bus.SearchInput{Query: "commit:e159e8c"}, []int64{6}},
		{"path compound", ab2bus.SearchInput{Query: "owner/repo@e159*"}, []int64{10, 11}},
		{"hyphen", ab2bus.SearchInput{Query: "foo-bar"}, []int64{13, 14}},
		{"no diacritic folding", ab2bus.SearchInput{Query: "cafe"}, []int64{16}},
		{"diacritic exact", ab2bus.SearchInput{Query: "café"}, []int64{15}},
		{"missing", ab2bus.SearchInput{Query: "missing"}, []int64{}},
		{"channel scoping", ab2bus.SearchInput{Query: "rate", Channel: "a"}, []int64{1, 2, 3, 4, 5}},
		{"sender", ab2bus.SearchInput{Query: "rate", Sender: "kim"}, []int64{2, 5}},
		{"since", ab2bus.SearchInput{Query: "rate", Since: i64(fixedNowMs + 4000)}, []int64{4, 5, 17}},
		{"thread", ab2bus.SearchInput{Query: "rate", Thread: i64(1)}, []int64{1, 2, 3}},
		{"tag pattern", ab2bus.SearchInput{Query: "rate", Tags: []string{"env*"}}, []int64{3, 4}},
		{"tag exact", ab2bus.SearchInput{Query: "rate", Tags: []string{"environment"}}, []int64{4}},
	}
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		chs := map[string]ab2store.Channel{
			"a": h.channel("a", ab2store.KindOrdinary, "pat", nil),
			"b": h.channel("b", ab2store.KindOrdinary, "pat", nil),
		}
		for i, r := range searchCorpus {
			if seq := h.appendRow(chs[r.ch], r); seq != int64(i+1) {
				t.Fatalf("row r%d got seq %d", i+1, seq)
			}
		}
		h.tx(func(ctx context.Context, tx ab2store.Tx) error {
			ok, err := tx.TombstoneMessage(ctx, 18, h.core.NowMs())
			if !ok {
				t.Fatal("r18 not tombstoned")
			}
			return err
		})
		b := h.as("kim")
		for _, c := range cases {
			c.in.Mode = "text"
			res, err := b.Search(context.Background(), c.in)
			if err != nil {
				t.Errorf("%s: %v", c.name, err)
				continue
			}
			if got := sortedSeqs(res); !reflect.DeepEqual(got, c.want) {
				t.Errorf("%s = %v, want %v", c.name, got, c.want)
			}
			// Text hits come newest first.
			if got := hitSeqs(res); !slices.IsSortedFunc(got, func(a, b int64) int { return int(b - a) }) {
				t.Errorf("%s: hits %v not newest first", c.name, got)
			}
		}
	})
}

// With more matches than one search can rank, the cap drops the oldest, never
// the newest: a message appended after the cap was reached must be on page 1.
func TestSearchTextCapKeepsNewestMatches(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ch := h.channel("c", ab2store.KindOrdinary, "pat", nil)
		var first, last int64
		// One Append transaction: a thousand separate ones would dominate the run.
		err := h.raw.Append(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
			for i := 0; i <= rules.SearchPageMax; i++ {
				seq, _, err := h.core.Append(ctx, tx, core.AppendInput{
					ChannelID: ch.ID, Channel: ch.Name, Kind: ch.Kind, Sender: "pat", Content: "rate " + strconv.Itoa(i),
				})
				if err != nil {
					return err
				}
				if i == 0 {
					first = seq
				}
				last = seq
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if last-first != int64(rules.SearchPageMax) {
			t.Fatalf("appended seqs %d..%d, want %d matches", first, last, rules.SearchPageMax+1)
		}
		ctx := context.Background()
		b := h.as("pat")
		res, err := b.Search(ctx, ab2bus.SearchInput{Query: "rate", Mode: "text", Count: 1})
		if err != nil || !reflect.DeepEqual(hitSeqs(res), []int64{last}) || res.Next != "1" {
			t.Fatalf("page 1 = %v next %q, %v; want the newest [%d]", hitSeqs(res), res.Next, err, last)
		}
		// The last reachable hit is the second-oldest match: only the oldest is
		// dropped, and nothing follows.
		res, err = b.Search(ctx, ab2bus.SearchInput{Query: "rate", Mode: "text", Count: 5, Cursor: strconv.Itoa(rules.SearchPageMax - 1)})
		if err != nil || !reflect.DeepEqual(hitSeqs(res), []int64{first + 1}) || res.Next != "" {
			t.Fatalf("last page = %v next %q, %v; want [%d] and no next", hitSeqs(res), res.Next, err, first+1)
		}
	})
}

func TestSearchACLScoping(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{embedder: true})
		open := h.channel("open", ab2store.KindMemory, "pat", nil)
		secret := h.channel("secret", ab2store.KindMemory, "pat", []ab2store.ACLEntry{
			{Identity: "pat", Permissions: []ab2store.Permission{ab2store.PermRead, ab2store.PermWrite, ab2store.PermAdmin}},
		})
		openSeq := h.append(open, "pat", "", "alpha open")
		secretSeq := h.append(secret, "pat", "", "alpha secret")
		if _, err := embed.EmbedBatch(context.Background(), h.core); err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		kim, pat := h.as("kim"), h.as("pat")
		for _, mode := range []string{"text", "semantic", "both"} {
			res, err := kim.Search(ctx, ab2bus.SearchInput{Query: "alpha", Mode: mode})
			if err != nil || !reflect.DeepEqual(sortedSeqs(res), []int64{openSeq}) {
				t.Errorf("kim %s unscoped = %v, %v; want only %d", mode, hitSeqs(res), err, openSeq)
			}
			_, err = kim.Search(ctx, ab2bus.SearchInput{Query: "alpha", Mode: mode, Channel: "secret"})
			codeOf(t, err, ab2bus.CodeForbidden)
			res, err = pat.Search(ctx, ab2bus.SearchInput{Query: "alpha", Mode: mode})
			if err != nil || !reflect.DeepEqual(sortedSeqs(res), []int64{openSeq, secretSeq}) {
				t.Errorf("pat %s unscoped = %v, %v", mode, hitSeqs(res), err)
			}
		}
		_, err := kim.Search(ctx, ab2bus.SearchInput{Query: "alpha", Channel: "nosuch"})
		codeOf(t, err, ab2bus.CodeNotFound)
	})
}

func TestSearchSemanticAndBoth(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{embedder: true})
		mem := h.channel("mem", ab2store.KindMemory, "pat", nil)
		h.append(mem, "pat", "", "alpha bravo") // seq 1
		h.append(mem, "pat", "", "gamma")       // seq 2
		h.append(mem, "pat", "", "alpha")       // seq 3
		if n, err := embed.EmbedBatch(context.Background(), h.core); err != nil || n != 3 {
			t.Fatalf("EmbedBatch = %d, %v", n, err)
		}
		ctx := context.Background()
		b := h.as("pat")

		sem, err := b.Search(ctx, ab2bus.SearchInput{Query: "alpha", Mode: "semantic"})
		if err != nil || !reflect.DeepEqual(hitSeqs(sem), []int64{3, 1, 2}) || sem.SemanticUnavailable {
			t.Fatalf("semantic = %v, %v, unavailable=%v; want [3 1 2] %+v", hitSeqs(sem), err, sem.SemanticUnavailable, sem.Hits)
		}
		if !(sem.Hits[0].Score > sem.Hits[1].Score && sem.Hits[1].Score > sem.Hits[2].Score) {
			t.Fatalf("scores not descending: %+v", sem.Hits)
		}
		both, err := b.Search(ctx, ab2bus.SearchInput{Query: "alpha", Mode: "both"})
		if err != nil || !reflect.DeepEqual(hitSeqs(both), []int64{3, 1, 2}) {
			t.Fatalf("both = %v, %v; want RRF order [3 1 2]", hitSeqs(both), err)
		}
		if both.Hits[0].Score <= both.Hits[2].Score {
			t.Fatalf("RRF scores: %+v", both.Hits)
		}
		def, err := b.Search(ctx, ab2bus.SearchInput{Query: "alpha"}) // default mode with an embedder is both
		if err != nil || !reflect.DeepEqual(hitSeqs(def), hitSeqs(both)) {
			t.Fatalf("default mode = %v, %v; want both's %v", hitSeqs(def), err, hitSeqs(both))
		}

		h.embedder.Fail = true
		for _, mode := range []string{"semantic", "both"} {
			res, err := b.Search(ctx, ab2bus.SearchInput{Query: "alpha", Mode: mode})
			if err != nil || !res.SemanticUnavailable || !reflect.DeepEqual(hitSeqs(res), []int64{3, 1}) {
				t.Errorf("Fail %s = %v, %v, unavailable=%v; want text fallback [3 1]", mode, hitSeqs(res), err, res.SemanticUnavailable)
			}
		}
		h.embedder.Fail = false

		// No embedder at all: semantic falls back to text with the flag.
		h2 := newHarness(t, e, harnessOptions{})
		mem2 := h2.channel("mem", ab2store.KindMemory, "pat", nil)
		h2.append(mem2, "pat", "", "alpha")
		res, err := h2.as("pat").Search(ctx, ab2bus.SearchInput{Query: "alpha", Mode: "semantic"})
		if err != nil || !res.SemanticUnavailable || len(res.Hits) != 1 {
			t.Fatalf("no embedder = %+v, %v", res, err)
		}
		res, err = h2.as("pat").Search(ctx, ab2bus.SearchInput{Query: "alpha"})
		if err != nil || res.SemanticUnavailable || len(res.Hits) != 1 {
			t.Fatalf("no embedder default mode = %+v, %v", res, err)
		}
	})
}

func TestSearchPagingAndTrim(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		cfg := core.Defaults()
		cfg.ResultDefaultKiB = 1
		h := newHarness(t, e, harnessOptions{cfg: &cfg})
		ch := h.channel("c", ab2store.KindOrdinary, "pat", nil)
		for i := 0; i < 5; i++ {
			h.appendRow(ch, searchRow{content: "rate " + strings.Repeat("x", 300)})
		}
		ctx := context.Background()
		b := h.as("pat")

		// Trimmed to the 1 KiB result limit: fewer than 5 hits, a Next, and
		// following Next yields every row once, newest first.
		var all []int64
		cursor := ""
		for pages := 0; pages < 10; pages++ {
			res, err := b.Search(ctx, ab2bus.SearchInput{Query: "rate", Cursor: cursor})
			if err != nil {
				t.Fatal(err)
			}
			if pages == 0 && (len(res.Hits) >= 5 || res.Next == "") {
				t.Fatalf("first page not trimmed: %d hits, next %q", len(res.Hits), res.Next)
			}
			if len(res.Hits) == 0 {
				t.Fatalf("empty page with next %q", res.Next)
			}
			all = append(all, hitSeqs(res)...)
			if cursor = res.Next; cursor == "" {
				break
			}
		}
		if !reflect.DeepEqual(all, []int64{5, 4, 3, 2, 1}) {
			t.Fatalf("paged seqs = %v", all)
		}

		h2 := newHarness(t, e, harnessOptions{})
		ch2 := h2.channel("c", ab2store.KindOrdinary, "pat", nil)
		for i := 0; i < 5; i++ {
			h2.appendRow(ch2, searchRow{content: "rate"})
		}
		b2 := h2.as("pat")
		res, err := b2.Search(ctx, ab2bus.SearchInput{Query: "rate", Count: 2})
		if err != nil || !reflect.DeepEqual(hitSeqs(res), []int64{5, 4}) || res.Next != "2" {
			t.Fatalf("page 1 = %v next %q, %v", hitSeqs(res), res.Next, err)
		}
		res, err = b2.Search(ctx, ab2bus.SearchInput{Query: "rate", Count: 2, Cursor: res.Next})
		if err != nil || !reflect.DeepEqual(hitSeqs(res), []int64{3, 2}) || res.Next != "4" {
			t.Fatalf("page 2 = %v next %q, %v", hitSeqs(res), res.Next, err)
		}
		res, err = b2.Search(ctx, ab2bus.SearchInput{Query: "rate", Count: 2, Cursor: "4"})
		if err != nil || !reflect.DeepEqual(hitSeqs(res), []int64{1}) || res.Next != "" {
			t.Fatalf("page 3 = %v next %q, %v", hitSeqs(res), res.Next, err)
		}
		res, err = b2.Search(ctx, ab2bus.SearchInput{Query: "rate", Cursor: strconv.Itoa(99)})
		if err != nil || len(res.Hits) != 0 || res.Next != "" {
			t.Fatalf("cursor past the end = %+v, %v", res, err)
		}

		for name, in := range map[string]ab2bus.SearchInput{
			"negative cursor": {Query: "rate", Cursor: "-1"},
			"junk cursor":     {Query: "rate", Cursor: "abc"},
			"unknown mode":    {Query: "rate", Mode: "fuzzy"},
			"empty query":     {Query: "  "},
			"star only":       {Query: "*"},
			"bad tag":         {Query: "rate", Tags: []string{"No Good"}},
		} {
			_, err := b2.Search(ctx, in)
			if ab2bus.CodeOf(err) != ab2bus.CodeValidation {
				t.Errorf("%s: err = %v, want validation", name, err)
			}
		}
	})
}

func TestSearchTouchesHitsOnly(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		mem := h.channel("mem", ab2store.KindMemory, "pat", nil)
		tasks := h.channel("tasks", ab2store.KindTasks, "pat", nil)
		hitSeq := h.append(mem, "pat", "", "alpha")
		missSeq := h.append(mem, "pat", "", "bravo")
		taskSeq := h.append(tasks, "pat", "", "alpha task")
		h.clock.Advance(time.Hour)
		res, err := h.as("pat").Search(context.Background(), ab2bus.SearchInput{Query: "alpha", Mode: "text"})
		if err != nil || !reflect.DeepEqual(sortedSeqs(res), []int64{hitSeq, taskSeq}) {
			t.Fatalf("search = %v, %v", hitSeqs(res), err)
		}
		var stale []ab2store.MemoryRef
		h.tx(func(ctx context.Context, tx ab2store.Tx) error {
			var err error
			stale, err = tx.ExpiredMemories(ctx, h.core.NowMs()-30*60*1000, 10)
			return err
		})
		ids := map[int64]bool{}
		for _, r := range stale {
			ids[r.MemoryID] = true
		}
		if ids[hitSeq] || !ids[missSeq] {
			t.Fatalf("stale access rows = %v; the hit must be touched, the miss not", ids)
		}
		if ids[taskSeq] {
			t.Fatalf("a tasks-kind memory has no access row, yet ExpiredMemories lists it: %v", ids)
		}
	})
}
