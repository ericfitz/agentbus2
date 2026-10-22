package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/bus"
)

// sessionFixture is a fixture whose rail lists two live sessions (the TUI's
// own and sam/impl) and one idle one (codex), selected on the sessions heading.
func sessionFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	f.m.width = 200
	f.m.layout()
	f.m.identities = []bus.IdentityInfo{
		{Name: "sam", Sessions: []bus.SessionInfo{{ID: "s1", Identity: "sam", DisplayName: "sam/impl"}}},
		{Name: "eric", Sessions: []bus.SessionInfo{{ID: f.c.sessionID(), Identity: "eric", DisplayName: "eric/tui"}}},
	}
	f.m.sessionsSeen = map[string]time.Time{"codex": time.Now().Add(-14 * time.Minute)}
	f.key("esc")
	f.key("home")
	for range len(f.m.railPositions()) {
		if p := f.m.railPos(); p.sec == secSessions && p.item < 0 {
			break
		}
		f.key("down")
	}
	if p := f.m.railPos(); p.sec != secSessions || p.item >= 0 {
		t.Fatalf("never reached the sessions heading, at %+v", p)
	}
	return f
}

// TestSessionRowsAreRailStops: down from the sessions heading reaches each
// row of sessionRows() in order and stops at the last; up walks back.
func TestSessionRowsAreRailStops(t *testing.T) {
	f := sessionFixture(t)
	rows := f.m.sessionRows()
	if len(rows) != 3 {
		t.Fatalf("session rows = %+v, want 3", rows)
	}
	for i, r := range rows {
		f.key("down")
		if p := f.m.railPos(); p != (railPos{secSessions, i}) || f.m.sessSel != r.display {
			t.Fatalf("down %d: at %+v sessSel %q, want session row %d %q", i+1, p, f.m.sessSel, i, r.display)
		}
	}
	f.key("down")
	if p := f.m.railPos(); p != (railPos{secSessions, len(rows) - 1}) {
		t.Fatalf("down past the last row moved to %+v", p)
	}
	for range rows {
		f.key("up")
	}
	if p := f.m.railPos(); p != (railPos{secSessions, -1}) || f.m.sessSel != "" {
		t.Fatalf("up from the first row: at %+v sessSel %q, want the sessions heading", p, f.m.sessSel)
	}
	if f.m.selected() == nil {
		t.Fatal("leaving the session rows must give the stream its channel back")
	}
}

// TestSessionDetailPane: a selected session row shows its details in the
// main pane: live rows the identity and session id, idle rows `unknown` and
// the last time seen.
func TestSessionDetailPane(t *testing.T) {
	f := sessionFixture(t)
	f.key("down") // eric/tui, the own live row
	f.key("down") // sam/impl
	v := stripANSI(f.m.View().Content)
	for _, want := range []string{"sam/impl", "identity  sam", "session  s1", "live"} {
		if !strings.Contains(v, want) {
			t.Errorf("live row details lack %q:\n%s", want, v)
		}
	}
	f.key("down") // codex, idle
	v = stripANSI(f.m.View().Content)
	seen := f.m.sessionsSeen["codex"]
	for _, want := range []string{"codex", "identity  unknown", "idle, last seen " + stamp(seen.UnixMilli())} {
		if !strings.Contains(v, want) {
			t.Errorf("idle row details lack %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "session  ") {
		t.Errorf("an idle row has no session id:\n%s", v)
	}
}

// TestSessionSelectionFallsToHeadingOnRefresh: when a refresh removes the
// selected row, the selection moves to the sessions heading.
func TestSessionSelectionFallsToHeadingOnRefresh(t *testing.T) {
	f := newFixture(t)
	f.run(f.m.statusCmd()) // sam's session is listed live
	f.key("esc")
	f.key("home")
	for range len(f.m.railPositions()) {
		f.key("down")
	}
	if f.m.sessSel == "" {
		t.Fatalf("down must end on a session row, at %+v", f.m.railPos())
	}
	gone := f.m.sessSel
	if gone == "eric/tui" { // the own row is last-but-not-removable; pick sam's
		f.key("up")
		gone = f.m.sessSel
	}
	if err := f.sam.Close(context.Background(), bus.CloseSessionInput{Session: f.samSession.ID}); err != nil {
		t.Fatal(err)
	}
	f.m.sessionsSeen[gone] = time.Now().Add(-2 * idleSessionTTL) // seen long ago: the row is dropped
	f.run(f.m.statusCmd())
	if p := f.m.railPos(); p != (railPos{secSessions, -1}) || f.m.sessSel != "" {
		t.Fatalf("after the row %q went: at %+v sessSel %q, want the sessions heading", gone, p, f.m.sessSel)
	}
	if f.m.selected() == nil {
		t.Fatal("the stream must show its channel again")
	}
}

// TestCollapsingSessionsMovesSelectionToHeading: collapsing the section
// while one of its rows is selected leaves the heading selected.
func TestCollapsingSessionsMovesSelectionToHeading(t *testing.T) {
	f := sessionFixture(t)
	f.key("down")
	if f.m.sessSel == "" {
		t.Fatal("no session row selected")
	}
	f.m.toggleSection(secSessions, true)
	if p := f.m.railPos(); p != (railPos{secSessions, -1}) || f.m.sessSel != "" || f.m.selected() == nil {
		t.Fatalf("at %+v sessSel %q, want the sessions heading and the stream's channel", p, f.m.sessSel)
	}
}
