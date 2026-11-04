package tui

import (
	tea "charm.land/bubbletea/v2"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ericfitz/agentmesh/internal/bus"
)

// statusSet is one status revision a test sets: tag, text, problem.
type statusSet struct {
	tag           bus.StatusTag
	text, problem string
}

// agentWithStatus creates the agent name, opens a session and sets each
// revision in order; with live false it closes the session afterwards, so
// the agent reads offline with its last status stored.
func (f *fixture) agentWithStatus(t *testing.T, name string, live bool, sets ...statusSet) {
	t.Helper()
	ctx := context.Background()
	b := agentBus(t, f.core, name)
	s, err := b.Open(ctx, bus.OpenSessionInput{})
	if err != nil {
		t.Fatalf("Open %s: %v", name, err)
	}
	for _, x := range sets {
		if _, err := b.SetAgentStatus(ctx, bus.SetAgentStatusInput{Session: s.ID, Tag: x.tag, Text: x.text, Problem: x.problem}); err != nil {
			t.Fatalf("SetAgentStatus %s: %v", name, err)
		}
	}
	if !live {
		if err := b.Close(ctx, bus.CloseSessionInput{Session: s.ID}); err != nil {
			t.Fatalf("Close %s: %v", name, err)
		}
	}
}

// boardFixture is a wide fixture on the sessions heading with the five
// agents of the ordering test and one status round run.
func boardFixture(t *testing.T) (*fixture, *countingBus) {
	t.Helper()
	f, cb := newCountingFixture(t)
	f.m.width = 200
	f.m.layout()
	f.agentWithStatus(t, "zed", true, statusSet{bus.StatusImplementing, "wiring the board", ""})
	f.agentWithStatus(t, "amy", true, statusSet{bus.StatusBlocked, "waiting on keys", "no token"})
	f.agentWithStatus(t, "bob", true, statusSet{bus.StatusNeedsHuman, "pick a color", ""})
	f.agentWithStatus(t, "old", false, statusSet{bus.StatusNeedsHuman, "was asking", ""})
	f.agentWithStatus(t, "cal", true)
	f.key("esc")
	f.run(f.m.statusCmd())
	f.toHeading(t, secSessions)
	return f, cb
}

// rowNames is the identities of the board rows, in order, among want.
func rowNames(m Model, want ...string) []string {
	var out []string
	for _, r := range m.boardRows() {
		if slices.Contains(want, r.identity) {
			out = append(out, r.identity)
		}
	}
	return out
}

// TestBoardShownOnHeadingByArrows: the board is the main pane exactly while
// the sessions heading is selected.
func TestBoardShownOnHeadingByArrows(t *testing.T) {
	f, _ := boardFixture(t)
	if !f.m.boardShown() {
		t.Fatal("the sessions heading is selected, yet no board")
	}
	if v := stripANSI(f.m.View().Content); !strings.Contains(v, "agents (") {
		t.Fatalf("the pane lacks the board title:\n%s", v)
	}
	f.key("up") // the memories heading or the last row above
	if f.m.boardShown() {
		t.Fatal("up off the heading still shows the board")
	}
	if v := stripANSI(f.m.View().Content); strings.Contains(v, "agents (") {
		t.Fatalf("the board title shows off the heading:\n%s", v)
	}
	f.key("down")
	if !f.m.boardShown() {
		t.Fatal("down onto the heading did not show the board")
	}
}

// TestBoardKeyB: b selects the sessions heading from a channel row and
// shows the board; in compose and in a prompt it types a b.
func TestBoardKeyB(t *testing.T) {
	f, _ := boardFixture(t)
	f.key("home")
	f.key("down") // a channel row below the channels heading
	if f.m.boardShown() {
		t.Fatal("setup: the board still shows")
	}
	f.key("b")
	if !f.m.boardShown() || f.m.collapsed[secSessions] {
		t.Fatalf("b: shown=%v collapsed=%v, want the board with nothing collapsed", f.m.boardShown(), f.m.collapsed[secSessions])
	}
	f.key("tab")
	f.m.cursor = 3 // only selectSessionsHeading resets it; the focused board ignores keys
	f.key("b")     // b works with focus on the board
	if !f.m.boardShown() || f.m.cursor != -1 {
		t.Fatalf("b with the board focused: shown=%v cursor=%d, want the heading selected and cursor -1", f.m.boardShown(), f.m.cursor)
	}
	f.key("esc")
	f.key("home")
	f.key("down")
	f.key("i")
	f.key("b")
	if f.m.mode != modeInsert || f.m.boardShown() || f.m.compose.Value() != "b" {
		t.Fatalf("b in compose: mode=%v shown=%v draft=%q, want a typed b", f.m.mode, f.m.boardShown(), f.m.compose.Value())
	}
	f.key("esc")
	f.key("esc")
	f.key("c") // the channel create prompt
	if !f.m.prompt.active {
		t.Fatal("setup: c did not open the prompt")
	}
	f.key("b")
	if f.m.boardShown() || !strings.Contains(f.m.prompt.input.Value(), "b") {
		t.Fatalf("b in a prompt: shown=%v input=%q, want a typed b", f.m.boardShown(), f.m.prompt.input.Value())
	}
}

// TestSessionsHeadingEnterCollapses: enter and the arrows on the sessions
// heading collapse and expand it like any heading; the board still shows.
func TestSessionsHeadingEnterCollapses(t *testing.T) {
	f, _ := boardFixture(t)
	for _, step := range []struct {
		key       string
		collapsed bool
	}{{"enter", true}, {"enter", false}, {"left", true}, {"right", false}} {
		f.key(step.key)
		if f.m.collapsed[secSessions] != step.collapsed || !f.m.boardShown() {
			t.Fatalf("%s: collapsed=%v shown=%v, want collapsed=%v and the board", step.key, f.m.collapsed[secSessions], f.m.boardShown(), step.collapsed)
		}
	}
}

// TestBoardFocus: tab focuses the board, up and down then move its row, esc
// returns focus to the rail and the board stays.
func TestBoardFocus(t *testing.T) {
	f, _ := boardFixture(t)
	f.key("down") // rail focus: off the heading
	f.key("up")
	if f.m.board.focus {
		t.Fatal("the board has focus before tab")
	}
	f.key("tab")
	if !f.m.board.focus {
		t.Fatal("tab did not focus the board")
	}
	f.key("home") // ignored with focus
	if !f.m.boardShown() || !f.m.board.focus {
		t.Fatal("home with the board focused changed the selection or focus")
	}
	f.key("down")
	if f.m.boardSel() != 1 || !f.m.boardShown() {
		t.Fatalf("down with focus: sel=%d shown=%v, want row 1 on the board", f.m.boardSel(), f.m.boardShown())
	}
	f.key("esc")
	if f.m.board.focus || !f.m.boardShown() {
		t.Fatalf("esc: focus=%v shown=%v, want the rail focused and the board shown", f.m.board.focus, f.m.boardShown())
	}
	f.key("shift+tab")
	if !f.m.board.focus {
		t.Fatal("shift+tab did not focus the board")
	}
	if cmd := f.m.updateNormal(keyPress("q")); cmd == nil {
		t.Fatal("q from the focused board returned no command")
	} else if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("q from the focused board returned %T, want tea.QuitMsg", cmd())
	}
	f.key("esc")
	f.key("down") // rail again: leaves the heading
	if f.m.boardShown() {
		t.Fatal("down after esc did not move the rail")
	}
}

// TestBoardHistoryLifetime: a history survives tab away and back, esc
// returns to the board on the same row, and any other rail selection closes
// it.
func TestBoardHistoryLifetime(t *testing.T) {
	f, _ := boardFixture(t)
	f.key("tab")
	f.key("down")
	id := f.m.boardRows()[f.m.boardSel()].identity
	f.key("enter")
	if f.m.board.hist != id {
		t.Fatalf("enter opened %q, want %q", f.m.board.hist, id)
	}
	f.key("tab") // focus back to the rail
	f.key("tab") // and to the board
	if f.m.board.hist != id || !f.m.board.focus {
		t.Fatalf("tab away and back: hist=%q focus=%v, want %q focused", f.m.board.hist, f.m.board.focus, id)
	}
	f.key("esc")
	if f.m.board.hist != "" || f.m.boardRows()[f.m.boardSel()].identity != id {
		t.Fatalf("esc: hist=%q row=%q, want the board on %q", f.m.board.hist, f.m.boardRows()[f.m.boardSel()].identity, id)
	}
	f.key("enter")
	f.key("esc") // history -> board
	f.key("enter")
	f.key("tab") // rail focus with the history open
	f.key("up")  // another rail row
	if f.m.boardShown() || f.m.board.hist != "" || f.m.board.focus {
		t.Fatalf("selecting another row: shown=%v hist=%q focus=%v, want all closed", f.m.boardShown(), f.m.board.hist, f.m.board.focus)
	}
	f.key("down")
	if f.m.board.hist != "" {
		t.Fatalf("history came back: %q", f.m.board.hist)
	}
}

// setAges pins each named agent's SetAt to now minus its age, so a test does
// not depend on the order the fixture's statuses were set in the same
// millisecond.
func setAges(m *Model, ages map[string]time.Duration) {
	now := time.Now().UnixMilli()
	for i := range m.agents {
		if d, ok := ages[m.agents[i].Identity]; ok {
			m.agents[i].Status.SetAt = now - d.Milliseconds()
		}
	}
}

func boardView(f *fixture) []string {
	return strings.Split(stripANSI(f.m.View().Content), "\n")
}

func lineWith(lines []string, sub string) string {
	for _, l := range lines {
		if strings.Contains(l, sub) {
			return l
		}
	}
	return ""
}

func TestBoardSortCycle(t *testing.T) {
	f, _ := boardFixture(t)
	setAges(&f.m, map[string]time.Duration{
		"zed": 5 * time.Minute, "amy": 3 * time.Hour, "bob": 2 * time.Minute, "old": 50 * time.Hour,
	})
	all := []string{"amy", "bob", "cal", "old", "zed"}
	check := func(mode string, want ...string) {
		t.Helper()
		if got := rowNames(f.m, all...); !slices.Equal(got, want) {
			t.Fatalf("%s order = %v, want %v", mode, got, want)
		}
		if title := lineWith(boardView(f), "agents ("); !strings.Contains(title, "sort: "+mode) {
			t.Fatalf("title lacks sort: %s: %q", mode, title)
		}
	}
	f.key("tab")
	check("time", "bob", "zed", "amy", "old", "cal") // cal never set: last
	f.key("down")
	f.key("down") // amy
	f.key("o")
	check("name", "amy", "bob", "cal", "old", "zed")
	if got := f.m.boardRows()[f.m.boardSel()].identity; got != "amy" {
		t.Fatalf("selection moved to %q across o", got)
	}
	f.key("o")
	// displayed tags: blocked(amy) -(cal) needs-human(bob) offline(old) implementing(zed)
	check("tag", "cal", "amy", "zed", "bob", "old")
	if got := f.m.boardRows()[f.m.boardSel()].identity; got != "amy" {
		t.Fatalf("selection moved to %q across o", got)
	}
	f.key("o")
	check("time", "bob", "zed", "amy", "old", "cal")
	if f.m.board.focus != true || !f.m.boardShown() {
		t.Fatal("o lost the board")
	}
}

func TestBoardSortTiesByIdentity(t *testing.T) {
	now := int64(1000)
	mk := func(id string, live bool, tag bus.StatusTag, at int64) boardRow {
		return boardRow{identity: id, live: live, tag: tag, st: &bus.AgentStatus{Tag: tag, SetAt: at}}
	}
	rows := []boardRow{mk("b", true, bus.StatusIdle, now), mk("a", true, bus.StatusIdle, now), mk("c", true, bus.StatusIdle, now)}
	for _, mode := range []boardSort{sortTime, sortName, sortTag} {
		sortBoardRows(rows, mode)
		if rows[0].identity != "a" || rows[1].identity != "b" || rows[2].identity != "c" {
			t.Fatalf("mode %v: ties not by identity: %v %v %v", mode, rows[0].identity, rows[1].identity, rows[2].identity)
		}
	}
}

func TestBoardSelectionSurvivesStatusRound(t *testing.T) {
	f, _ := boardFixture(t)
	f.key("tab")
	f.key("o") // name: amy bob cal old zed
	f.key("down")
	f.agentWithStatus(t, "aaa", true, statusSet{bus.StatusIdle, "x", ""})
	f.run(f.m.statusCmd())
	if got := f.m.boardRows()[f.m.boardSel()].identity; got != "bob" {
		t.Fatalf("selection = %q after a status round, want bob", got)
	}
}

func TestBoardRowColumns(t *testing.T) {
	f, _ := boardFixture(t)
	setAges(&f.m, map[string]time.Duration{
		"zed": 3 * time.Minute, "amy": 5 * time.Hour, "bob": 49 * time.Hour, "old": 2 * time.Hour,
	})
	lines := boardView(f)
	if l := lineWith(lines, "zed"); !strings.Contains(l, "implementing") || !strings.Contains(l, " 3m") || !strings.Contains(l, "wiring the board") {
		t.Fatalf("zed row: %q", l)
	}
	if l := lineWith(lines, "amy"); !strings.Contains(l, " 5h") || !strings.Contains(l, "waiting on keys · no token") {
		t.Fatalf("amy row: %q", l)
	}
	if l := lineWith(lines, "bob"); !strings.Contains(l, " 2d") {
		t.Fatalf("bob row: %q", l)
	}
	if l := lineWith(lines, "old"); !strings.Contains(l, "offline") || !strings.Contains(l, "last: was asking") {
		t.Fatalf("old row: %q", l)
	}
	if l := lineWith(lines, "cal"); !strings.Contains(l, " -") || strings.Count(l, "-") < 3 {
		t.Fatalf("cal row must show - for tag, age and text: %q", l)
	}
	if lineWith(lines, "── offline ──") != "" {
		t.Fatal("offline divider is gone")
	}
	// stream width 43 (terminal 60): the NOW column has almost no room
	f.m.width = 60
	f.m.layout()
	if sw := f.m.stream.Width(); sw != 43 {
		t.Fatalf("stream width = %d, want 43", sw)
	}
	l43 := lineWith(boardView(f), "amy")
	if !strings.Contains(l43, "blocked") || !strings.Contains(l43, "5h") || strings.Contains(l43, "waiting on keys") {
		t.Fatalf("at width 43 the row keeps tag and age and cuts the text: %q", l43)
	}
	// narrow: text and problem end with an ellipsis
	f.m.width = 70
	f.m.layout()
	l := lineWith(boardView(f), "amy")
	if !strings.Contains(l, "…") || !strings.Contains(l, "·") {
		t.Fatalf("narrow amy row must cut text and problem: %q", l)
	}
	if strings.Count(l, "…") < 2 {
		t.Fatalf("both text and problem must end with …: %q", l)
	}
}

func TestBoardExpand(t *testing.T) {
	f, _ := boardFixture(t)
	f.m.width = 70
	f.m.layout()
	f.key("tab")
	f.key("o") // name: amy first
	f.key("right")
	v := strings.Join(boardView(f), "\n")
	if !strings.Contains(v, "waiting on keys") || !strings.Contains(v, "no token") {
		t.Fatalf("expanded amy lacks full text and problem:\n%s", v)
	}
	if !f.m.board.expanded["amy"] {
		t.Fatal("amy not expanded")
	}
	f.key("down") // bob is not expanded
	if f.m.board.expanded["bob"] {
		t.Fatal("expansion leaked to bob")
	}
	f.key("up")
	f.key("left")
	if f.m.board.expanded["amy"] {
		t.Fatal("left did not collapse")
	}
}

func TestBoardEmpty(t *testing.T) {
	f, _ := newCountingFixture(t)
	f.m.width = 200
	f.m.layout()
	f.toHeading(t, secSessions)
	f.m.agents, f.m.identities = nil, nil
	f.key("tab")
	for _, k := range []string{"o", "right", "left", "up", "down", "enter", "esc"} {
		f.key(k)
	}
	if v := stripANSI(f.m.View().Content); !strings.Contains(v, "no agents") {
		t.Fatalf("empty board lacks no agents:\n%s", v)
	}
}

func TestBoardSelectedRowGone(t *testing.T) {
	f, _ := boardFixture(t)
	f.key("tab")
	f.key("o")
	f.key("down") // bob
	f.key("right")
	f.m.agents = slices.DeleteFunc(f.m.agents, func(a bus.AgentStatusEntry) bool { return a.Identity == "bob" })
	f.m.identities = slices.DeleteFunc(f.m.identities, func(i bus.IdentityInfo) bool { return i.Name == "bob" })
	if got := f.m.boardRows()[f.m.boardSel()].identity; got != f.m.boardRows()[0].identity {
		t.Fatalf("selection = %q, want the first row", got)
	}
	_ = f.m.View()
	f.key("right")
	f.key("left")
}

func TestOUnfocusedIsHarmless(t *testing.T) {
	f, _ := boardFixture(t)
	f.m.board.focus = false
	before := f.m.board.sort
	f.key("o")
	if f.m.board.sort != before || f.m.mode != modeNormal || !f.m.boardShown() {
		t.Fatalf("o unfocused changed state: sort=%v mode=%v", f.m.board.sort, f.m.mode)
	}
}

func TestHelpListsBoardKeys(t *testing.T) {
	f, _ := boardFixture(t)
	help := stripANSI(strings.Join(f.m.helpLines(), "\n"))
	for _, want := range []string{"agents board", "  b  ", "select the sessions heading", "  o  ", "cycle the sort", "  → / ←", "expand / collapse the selected agent", "  enter  ", "status history", "  esc  ", "board back to the rail", "pgup / pgdn", "g / G"} {
		if !strings.Contains(help, want) {
			t.Errorf("help lacks %q", want)
		}
	}
	if strings.Contains(help, "on the sessions heading, open the agents board") {
		t.Error("help still says enter on the sessions heading opens the board")
	}
}

// open note: agent-status 14
func TestAgentsBoardHistoryOldestFirst(t *testing.T) {
	f, cb := boardFixture(t)
	f.agentWithStatus(t, "hal", true,
		statusSet{bus.StatusPlanning, "first thought", ""},
		statusSet{bus.StatusImplementing, "second thought", ""},
		statusSet{bus.StatusReviewing, "third thought", "tests red"})
	f.run(f.m.statusCmd())
	f.key("tab")
	for i := 0; i < 20 && f.m.boardRows()[f.m.boardSel()].identity != "hal"; i++ {
		f.key("down")
	}
	if f.m.boardRows()[f.m.boardSel()].identity != "hal" {
		t.Fatal("could not select hal")
	}
	before := cb.count("AgentStatusHistory")
	f.key("enter")
	if n := cb.count("AgentStatusHistory") - before; n != 1 {
		t.Fatalf("enter made %d history calls, want 1", n)
	}
	v := stripANSI(f.m.View().Content)
	i1, i2, i3 := strings.Index(v, "first thought"), strings.Index(v, "second thought"), strings.Index(v, "third thought")
	if i1 < 0 || i1 >= i2 || i2 >= i3 {
		t.Fatalf("revisions not oldest first (%d %d %d):\n%s", i1, i2, i3, v)
	}
	if !strings.Contains(v, "hal · history, oldest first") || !strings.Contains(v, "by hal") || !strings.Contains(v, "· tests red") {
		t.Fatalf("history lacks its title, author or problem:\n%s", v)
	}
	f.run(f.m.statusCmd())
	if n := cb.count("AgentStatusHistory") - before; n != 2 {
		t.Fatalf("a status round while open made %d history calls in all, want 2", n)
	}
	f.key("esc")
	if !f.m.boardShown() || !f.m.board.focus || f.m.board.hist != "" {
		t.Fatalf("esc from history: shown=%v hist=%q, want the board", f.m.boardShown(), f.m.board.hist)
	}
	if n := cb.count("AgentStatusHistory") - before; n != 2 {
		t.Fatalf("esc to the board made a call: %d", n)
	}
	f.run(f.m.statusCmd())
	if n := cb.count("AgentStatusHistory") - before; n != 2 {
		t.Fatalf("a status round on the board made a history call: %d", n)
	}
}

func TestAgentsBoardHistoryEmptyAndTail(t *testing.T) {
	m := Model{}
	if got := m.renderHistory("x", nil, 5, 0); !strings.Contains(got, "no status yet") {
		t.Fatalf("empty history: %q", got)
	}
	var rows []bus.AgentStatus
	for i := range 10 {
		rows = append(rows, bus.AgentStatus{Revision: int64(i + 1), Tag: bus.StatusWaiting, Text: "rev" + string(rune('a'+i)), DisplayName: "x"})
	}
	got := stripANSI(m.renderHistory("x", rows, 4, 0))
	if strings.Contains(got, "reva") || !strings.Contains(got, "revj") {
		t.Fatalf("a long history must keep the newest rows:\n%s", got)
	}
}

// open note: agent-status 7
func TestAttentionCountLiveOnly(t *testing.T) {
	f, _ := boardFixture(t)
	if n := f.m.attention(); n != 2 {
		t.Fatalf("attention = %d, want 2 (amy blocked, bob needs-human; old is offline)", n)
	}
	v := stripANSI(f.m.View().Content)
	if !strings.Contains(v, "2 need you") || !strings.Contains(v, "(2 need you)") {
		t.Fatalf("the status bar and the sessions heading must show the count:\n%s", v)
	}
	f.m.collapsed[secSessions] = true
	if v := stripANSI(f.m.View().Content); !strings.Contains(v, ", 2 need you)") {
		t.Fatalf("collapsed heading lacks the count:\n%s", v)
	}
	f.m.collapsed[secSessions] = false

	g := newFixture(t)
	g.m.width = 200
	g.m.layout()
	g.agentWithStatus(t, "old", false, statusSet{bus.StatusNeedsHuman, "was asking", ""})
	g.agentWithStatus(t, "zed", true, statusSet{bus.StatusImplementing, "busy", ""})
	g.run(g.m.statusCmd())
	if v := stripANSI(g.m.View().Content); strings.Contains(v, "need you") || g.m.attention() != 0 {
		t.Fatalf("no live agent needs a human, yet:\n%s", v)
	}
}

func TestAgentsBoardKeys(t *testing.T) {
	f, _ := boardFixture(t)
	f.key("tab")
	f.key("x") // ignored
	f.key("i") // ignored: no compose
	if !f.m.boardShown() || f.m.mode != modeNormal {
		t.Fatalf("a stray key changed the state: shown=%v mode=%v", f.m.boardShown(), f.m.mode)
	}
	f.key("down")
	f.key("down")
	if f.m.boardSel() != 2 {
		t.Fatalf("sel = %d after two downs", f.m.boardSel())
	}
	for range 50 {
		f.key("down")
	}
	if f.m.boardSel() != len(f.m.boardRows())-1 {
		t.Fatalf("sel = %d, want the last row", f.m.boardSel())
	}
	for range 50 {
		f.key("up")
	}
	if f.m.boardSel() != 0 {
		t.Fatalf("sel = %d, want 0", f.m.boardSel())
	}
	f.key("?")
	if f.m.mode != modeHelp {
		t.Fatalf("? did not open help, mode=%v", f.m.mode)
	}
	f.key("esc")
	if !f.m.boardShown() || !f.m.board.focus {
		t.Fatal("closing help lost the board or its focus")
	}
	f.key("h")
	if f.m.mode != modeHealth {
		t.Fatalf("h did not open health, mode=%v", f.m.mode)
	}
}

// TestStatusRoundCallsAgentStatuses: one status round reads the agent
// statuses once and never the history.
func TestStatusRoundCallsAgentStatuses(t *testing.T) {
	f, cb := newCountingFixture(t)
	a, h := cb.count("AgentStatuses"), cb.count("AgentStatusHistory")
	f.run(f.m.statusCmd())
	if n := cb.count("AgentStatuses") - a; n != 1 {
		t.Fatalf("AgentStatuses calls = %d, want 1", n)
	}
	if n := cb.count("AgentStatusHistory") - h; n != 0 {
		t.Fatalf("AgentStatusHistory calls = %d, want 0", n)
	}
}

// TestAgentsBoardSelectionSurvivesResort: the selection follows the agent,
// not the row index, when a status round re-sorts the rows (final review
// Important 1).
func TestAgentsBoardSelectionSurvivesResort(t *testing.T) {
	f, _ := boardFixture(t)
	f.key("tab")
	f.key("o") // name: amy bob cal old zed
	f.key("down")
	if got := f.m.boardRows()[f.m.boardSel()].identity; got != "bob" {
		t.Fatalf("selected %q, want bob", got)
	}
	f.agentWithStatus(t, "aaa", true, statusSet{bus.StatusNeedsHuman, "look here", ""})
	f.run(f.m.statusCmd())
	rows := f.m.boardRows()
	if rows[0].identity != "aaa" {
		t.Fatalf("aaa did not sort first: %v", rowNames(f.m, "aaa", "bob", "amy"))
	}
	if got := rows[f.m.boardSel()].identity; got != "bob" {
		t.Fatalf("selection moved to %q after a re-sort, want bob", got)
	}
	f.key("enter")
	if f.m.board.hist != "bob" {
		t.Fatalf("enter opened %q, want bob", f.m.board.hist)
	}
}

// TestAgentsBoardScrolls: with more rows than the pane, moving down keeps
// the selected row visible (final review Important 2).
func TestAgentsBoardScrolls(t *testing.T) {
	f, _ := boardFixture(t)
	for _, n := range []string{"a1", "a2", "a3", "a4", "a5", "a6", "a7", "a8", "a9"} {
		f.agentWithStatus(t, n, true, statusSet{bus.StatusImplementing, "busy " + n, ""})
	}
	f.run(f.m.statusCmd())
	f.m.height = 14
	f.m.layout()
	h := f.m.stream.Height()
	if h >= len(f.m.boardRows())+2 {
		t.Fatalf("pane (%d lines) is not smaller than the board (%d rows)", h, len(f.m.boardRows()))
	}
	f.key("tab")
	for range len(f.m.boardRows()) {
		f.key("down")
		sel := f.m.boardRows()[f.m.boardSel()].identity
		out := stripANSI(f.m.renderBoard())
		if n := len(strings.Split(out, "\n")); n > h {
			t.Fatalf("board is %d lines in a pane of %d", n, h)
		}
		if !strings.Contains(out, sel) {
			t.Fatalf("selected row %q is not visible:\n%s", sel, out)
		}
		if !strings.Contains(out, "agents (") {
			t.Fatalf("title scrolled away:\n%s", out)
		}
	}
}

// TestAgentsBoardFailedRefreshKeepsTitle: a failed history refresh keeps the
// title and the esc hint (final review Minor 11).
func TestAgentsBoardFailedRefreshKeepsTitle(t *testing.T) {
	f, _ := boardFixture(t)
	f.key("tab")
	f.key("enter")
	id := f.m.board.hist
	if id == "" {
		t.Fatal("history did not open")
	}
	f.send(agentHistoryMsg{identity: id, err: errors.New("boom")})
	out := stripANSI(f.m.renderBoard())
	if !strings.Contains(out, id+" · history") || !strings.Contains(out, "esc back") || !strings.Contains(out, "boom") {
		t.Fatalf("failed refresh lost the title or error:\n%s", out)
	}
}

// histFixture opens bob's history with 12 revisions on a pane that shows 5.
func histFixture(t *testing.T) *fixture {
	t.Helper()
	f, _ := boardFixture(t)
	f.key("tab")
	f.key("o") // name: amy, bob
	f.key("down")
	var sets []statusSet
	for i := 1; i <= 12; i++ {
		sets = append(sets, statusSet{bus.StatusImplementing, fmt.Sprintf("rev%02d", i), ""})
	}
	f.agentWithStatus(t, "hh", true, sets...)
	f.run(f.m.statusCmd())
	f.m.board.sel = "hh"
	f.key("enter")
	f.run(f.m.agentHistoryCmd("hh"))
	f.m.stream.SetHeight(6) // title + 5 rows
	return f
}

func TestBoardHistoryPaging(t *testing.T) {
	f := histFixture(t)
	if f.m.histRoom() != 5 || len(f.m.board.rows) < 12 {
		t.Fatalf("room=%d rows=%d", f.m.histRoom(), len(f.m.board.rows))
	}
	has := func(sub string) bool { return strings.Contains(strings.Join(boardView(f), "\n"), sub) }
	if !has("rev12") || has("rev07") {
		t.Fatal("history must open on the newest rows")
	}
	f.key("pgup")
	if !has("rev07") || has("rev12") {
		t.Fatalf("pgup must show older rows:\n%s", strings.Join(boardView(f), "\n"))
	}
	f.key("pgdown")
	if !has("rev12") {
		t.Fatal("pgdown must return to the newest")
	}
	f.key("pgdown")
	if f.m.board.histOff != 0 {
		t.Fatal("pgdown past the newest must clamp")
	}
	f.key("g")
	if !has("rev01") || has("rev12") {
		t.Fatalf("g must show the first revision:\n%s", strings.Join(boardView(f), "\n"))
	}
	f.key("pgup")
	f.key("pgup")
	if f.m.board.histOff != f.m.histMaxOff() || !has("rev01") {
		t.Fatalf("pgup past the oldest must clamp, off=%d max=%d", f.m.board.histOff, f.m.histMaxOff())
	}
	f.key("o")
	if f.m.board.sort != sortName {
		t.Fatal("o must do nothing while a history is open")
	}
	f.key("G")
	if !has("rev12") || f.m.board.histOff != 0 {
		t.Fatal("G must show the newest")
	}
	f.key("pgup")
	f.key("esc")
	f.key("enter")
	if f.m.board.histOff != 0 {
		t.Fatal("reopening a history must reset the offset")
	}
}

func TestBoardHistoryRefetchKeepsPlace(t *testing.T) {
	f := histFixture(t)
	f.key("g")
	rows := f.m.board.rows
	off := f.m.board.histOff
	more := append(slices.Clone(rows), bus.AgentStatus{Tag: bus.StatusIdle, Text: "new1"}, bus.AgentStatus{Tag: bus.StatusIdle, Text: "new2"})
	f.m.onAgentHistory(agentHistoryMsg{identity: "hh", rows: more})
	if f.m.board.histOff != off+2 {
		t.Fatalf("off = %d, want %d (same rows in view)", f.m.board.histOff, off+2)
	}
	f.key("G")
	f.m.onAgentHistory(agentHistoryMsg{identity: "hh", rows: append(more, bus.AgentStatus{Text: "new3"})})
	if f.m.board.histOff != 0 {
		t.Fatal("at the newest end the view follows new rows")
	}
}
