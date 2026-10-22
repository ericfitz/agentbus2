package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/ericfitz/agentbus2/internal/bus"
)

// searchSpyBus records the SearchInput and the History Before values of the
// calls the search overlay makes, and can fail Search.
type searchSpyBus struct {
	bus.Bus
	mu      sync.Mutex
	searchs []bus.SearchInput
	befores []int64
	fail    error
}

func (b *searchSpyBus) Search(ctx context.Context, in bus.SearchInput) (bus.SearchResult, error) {
	b.mu.Lock()
	b.searchs = append(b.searchs, in)
	fail := b.fail
	b.mu.Unlock()
	if fail != nil {
		return bus.SearchResult{}, fail
	}
	return b.Bus.Search(ctx, in)
}

func (b *searchSpyBus) History(ctx context.Context, in bus.HistoryInput) ([]bus.Message, error) {
	if in.Before != nil {
		b.mu.Lock()
		b.befores = append(b.befores, *in.Before)
		b.mu.Unlock()
	}
	return b.Bus.History(ctx, in)
}

func newSearchFixture(t *testing.T) (*fixture, *searchSpyBus) {
	t.Helper()
	var spy *searchSpyBus
	f := newFixtureWith(t, func(b bus.Bus) bus.Bus { spy = &searchSpyBus{Bus: b}; return spy }, Host{})
	return f, spy
}

func TestSlashOpensSearchAndEnterRuns(t *testing.T) {
	f, spy := newSearchFixture(t)
	f.samSend("dev", "deploy failed")
	f.receive(t)
	f.key("esc")
	f.key("/")
	if f.m.mode != modeSearch || f.m.search.mode != "text" {
		t.Fatalf("mode=%v search mode=%q", f.m.mode, f.m.search.mode)
	}
	f.key("deploy")
	f.key("enter")
	if len(f.m.search.hits) != 1 || !f.m.search.ran || f.m.search.err != nil {
		t.Fatalf("hits=%+v err=%v", f.m.search.hits, f.m.search.err)
	}
	if got := spy.searchs[0]; got.Query != "deploy" || got.Mode != "text" || got.Count != searchCount {
		t.Fatalf("SearchInput = %+v", got)
	}
	if v := stripANSI(f.m.View().Content); !strings.Contains(v, "1 results") || !strings.Contains(v, "deploy failed") {
		t.Fatalf("view:\n%s", v)
	}
	// A response for an older query is ignored.
	f.send(searchMsg{query: "older", res: bus.SearchResult{Hits: []bus.SearchHit{{}, {}, {}}}})
	if len(f.m.search.hits) != 1 {
		t.Fatalf("stale searchMsg must be ignored, hits=%d", len(f.m.search.hits))
	}
	// An empty query runs nothing.
	f.key("esc")
	f.key("/")
	f.key("enter")
	if len(spy.searchs) != 1 {
		t.Fatalf("empty query must not search, calls=%d", len(spy.searchs))
	}
}

func TestTabCyclesModes(t *testing.T) {
	f, spy := newSearchFixture(t)
	f.samSend("dev", "release dev-notes")
	f.receive(t)
	f.key("esc")
	f.key("/")
	f.key("release")
	f.key("enter")
	if !f.m.search.ran {
		t.Fatal("initial text search must run")
	}
	for _, want := range []string{"semantic", "both", "text"} {
		f.key("tab")
		if f.m.search.mode != want {
			t.Fatalf("tab: want %q, got %q", want, f.m.search.mode)
		}
		if f.m.search.ran {
			t.Fatal("tab must invalidate the last run so enter re-runs instead of opening a hit")
		}
	}
	f.key("tab") // semantic
	f.key("enter")
	if f.m.mode != modeSearch || !f.m.search.ran {
		t.Fatalf("enter after tab must re-run: mode=%v ran=%v", f.m.mode, f.m.search.ran)
	}
	if got := spy.searchs[len(spy.searchs)-1].Mode; got != "semantic" {
		t.Fatalf("Mode passed to the core = %q", got)
	}
}

func TestSemanticUnavailableShowsTextOnlyBadge(t *testing.T) {
	f, spy := newSearchFixture(t)
	f.samSend("dev", "release notes")
	f.receive(t)
	f.key("esc")
	f.key("/")
	f.key("tab")
	f.key("tab") // both
	f.key("release")
	f.key("enter")
	if got := spy.searchs[0].Mode; got != "both" {
		t.Fatalf("Mode = %q", got)
	}
	if !f.m.search.textOnly || !f.m.search.semanticDown || f.m.search.err != nil {
		t.Fatalf("textOnly=%v semanticDown=%v err=%v", f.m.search.textOnly, f.m.search.semanticDown, f.m.search.err)
	}
	v := stripANSI(f.m.View().Content)
	if !strings.Contains(v, "text only") || strings.Contains(v, iconError) {
		t.Fatalf("want the badge and no error:\n%s", v)
	}
	if len(f.m.search.hits) != 1 {
		t.Fatalf("text leg must still answer, hits=%d", len(f.m.search.hits))
	}
	f.key("esc")
	if f.m.mode != modeNormal {
		t.Fatal("esc closes the overlay")
	}
}

func TestEnterOnHitJumpsAndLoadsAround(t *testing.T) {
	f, spy := newSearchFixture(t)
	// dev-notes is never received live: jumpTo's first visit must load it.
	for i := 0; i < 3; i++ {
		f.samSend("dev-notes", fmt.Sprintf("before %d", i))
	}
	r := f.samSend("dev-notes", "release procedure memory")
	for i := 0; i < 2; i++ {
		f.samSend("dev-notes", fmt.Sprintf("after %d", i))
	}
	f.drainAndAck(t)
	f.key("esc")
	f.key("/")
	f.key("procedure")
	f.key("enter")
	if len(f.m.search.hits) != 1 {
		t.Fatalf("hits=%+v err=%v", f.m.search.hits, f.m.search.err)
	}
	f.key("enter")
	if f.m.mode != modeNormal || f.m.selected() == nil || f.m.selected().Name != "dev-notes" {
		t.Fatalf("enter must jump to the hit's channel: mode=%v sel=%v", f.m.mode, f.m.selected())
	}
	if f.m.cursor < 0 || f.m.msgs["dev-notes"][f.m.cursor].Seq != r.Seq {
		t.Fatalf("cursor must sit on the hit, cursor=%d", f.m.cursor)
	}
	if len(spy.befores) == 0 || spy.befores[len(spy.befores)-1] != r.Seq {
		t.Fatalf("History{Before: %d} was not requested: %v", r.Seq, spy.befores)
	}
	if got, want := len(f.m.msgs["dev-notes"]), 6; got != want {
		t.Fatalf("latest page and the page before the hit leave no hole: got %d, want %d", got, want)
	}
}

func TestJumpToAChannelNotInTheRailToasts(t *testing.T) {
	f, _ := newSearchFixture(t)
	f.key("esc")
	f.run(f.m.jumpTo(bus.Message{Channel: "ghost", Seq: 1}))
	if !strings.Contains(f.m.toast, "ghost") {
		t.Fatalf("toast=%q", f.m.toast)
	}
}

func TestSearchErrorIsShownInOverlay(t *testing.T) {
	f, spy := newSearchFixture(t)
	spy.fail = &bus.Error{Code: bus.CodeForbidden, Message: "no read"}
	f.key("esc")
	f.key("/")
	f.key("anything")
	f.key("enter")
	if f.m.search.err == nil || len(f.m.search.hits) != 0 || f.m.search.textOnly {
		t.Fatalf("err=%v hits=%d textOnly=%v", f.m.search.err, len(f.m.search.hits), f.m.search.textOnly)
	}
	v := stripANSI(f.m.View().Content)
	if !strings.Contains(v, "forbidden: no read") || f.m.mode != modeSearch {
		t.Fatalf("overlay must show the error and stay open:\n%s", v)
	}
	spy.fail = errors.New("boom")
	f.key("enter") // same query, no hits: re-runs
	if !strings.Contains(stripANSI(f.m.View().Content), "boom") {
		t.Fatalf("a plain error must show too:\n%s", f.m.View().Content)
	}
}

func TestSearchHitListWindowsToFitTheOverlay(t *testing.T) {
	f, _ := newSearchFixture(t)
	for i := 0; i < 10; i++ {
		f.samSend("dev", fmt.Sprintf("release note %d", i))
	}
	f.receive(t)
	f.m.height = 12
	f.key("esc")
	f.key("/")
	f.key("release")
	f.key("enter")
	if len(f.m.search.hits) != 10 {
		t.Fatalf("hits=%d err=%v", len(f.m.search.hits), f.m.search.err)
	}
	for range 9 {
		f.key("down")
	}
	f.key("down") // clamps at the last hit
	if f.m.search.cursor != 9 {
		t.Fatalf("cursor=%d", f.m.search.cursor)
	}
	f.m.search.textOnly = true
	v := stripANSI(f.m.View().Content)
	if !strings.Contains(v, "text only") {
		t.Fatalf("badge must render alongside a full hit list:\n%s", v)
	}
	if lines := strings.Count(v, "\n") + 1; lines > f.m.height {
		t.Fatalf("overlay must fit height=%d, rendered %d lines:\n%s", f.m.height, lines, v)
	}
}
