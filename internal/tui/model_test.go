package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/store"
)

// fixture is the busFixture (a core over SQLite, sam with dev and dev-notes,
// the TUI's client as eric) plus a Model whose Init has run.
type fixture struct {
	*busFixture
	m Model
}

// newFixture is a model over the TUI's client as eric, 100x32, in insert mode
// (most tests type first) with Init run.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	return newFixtureWith(t, nil, Host{})
}

// newCountingFixture is newFixture over a countingBus wrapped around the
// TUI's own bus, so a test can assert which calls the model makes.
func newCountingFixture(t *testing.T) (*fixture, *countingBus) {
	t.Helper()
	var cb *countingBus
	f := newFixtureWith(t, func(b bus.Bus) bus.Bus { cb = newCountingBus(b); return cb }, Host{})
	return f, cb
}

// newFixtureWith builds the fixture; wrap, when set, replaces the client's bus
// before the model starts.
func newFixtureWith(t *testing.T, wrap func(bus.Bus) bus.Bus, h Host) *fixture {
	t.Helper()
	bf := newBusFixture(t)
	if wrap != nil {
		bf.c.b = wrap(bf.c.b)
	}
	f := &fixture{busFixture: bf}
	f.m = New(bf.c, LoadTheme(Settings{}, io.Discard), h)
	f.m.width, f.m.height = 100, 32
	f.run(f.m.Init())
	f.key("i") // the TUI starts on the channel list; most tests type first
	return f
}

// reinit replaces the model with a fresh one over the same client, as a
// restart would, and runs Init (after the messages the test just sent exist).
func (f *fixture) reinit() {
	f.m = New(f.c, f.m.theme, f.m.host)
	f.m.width, f.m.height = 100, 32
	f.run(f.m.Init())
}

func TestStartsOnChannelList(t *testing.T) {
	f := newFixture(t)
	if m := New(f.c, f.m.theme, Host{}); m.mode != modeNormal || m.pane() != paneChannels {
		t.Fatalf("mode=%v pane=%v", m.mode, m.pane())
	}
}

// Timers (status tick, toast clear) would sleep for real inside run; tests
// drive those messages by hand instead.
func init() { tick = func(time.Duration, func(time.Time) tea.Msg) tea.Cmd { return nil } }

// run executes cmd synchronously and feeds every resulting message back
// through Update, recursively, so tests see the same state the program would.
func (f *fixture) run(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	msg := cmd()
	if msg == nil {
		return
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			f.run(c)
		}
		return
	}
	if _, ok := msg.(statusTickMsg); ok {
		return // the periodic timer; tests trigger status explicitly
	}
	f.send(msg)
}

func (f *fixture) send(msg tea.Msg) {
	next, cmd := f.m.Update(msg)
	f.m = next.(Model)
	f.run(cmd)
}

// moveTo presses down or up until the rail selects the channel name (the
// rail is one list of headings and items, grouped by section, so "the next
// channel" depends on the fixture's mix).
func (f *fixture) moveTo(t *testing.T, name string) {
	t.Helper()
	i := slices.IndexFunc(f.m.channels, func(c bus.Channel) bool { return c.Name == name })
	if i < 0 {
		t.Fatalf("no channel %q", name)
	}
	target := railPos{channelSection(f.m.channels[i]), i}
	for range len(f.m.railPositions()) {
		ps := f.m.railPositions()
		at, to := slices.Index(ps, f.m.railPos()), slices.Index(ps, target)
		switch {
		case at == to:
			return
		case to < at:
			f.key("up")
		default:
			f.key("down")
		}
	}
	t.Fatalf("never reached %q, at %q", name, f.m.selName())
}

func (f *fixture) key(k string) { f.send(keyPress(k)) }

// keyPress is the Bubble Tea v2 key press a terminal sends for k, named as
// keyString names it: "enter", " " (the space bar), "ctrl+u", "alt+enter",
// or text, which a single press types whole (as v1's KeyRunes did).
func keyPress(k string) tea.KeyPressMsg {
	switch k {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case " ":
		// Text carries the literal space so a textarea types it; normal-mode
		// " " (cycle a task's state) matches on keyString, not Text.
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "shift+tab":
		return tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	case "home":
		return tea.KeyPressMsg{Code: tea.KeyHome}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "pgup":
		return tea.KeyPressMsg{Code: tea.KeyPgUp}
	case "pgdown":
		return tea.KeyPressMsg{Code: tea.KeyPgDown}
	case "left":
		return tea.KeyPressMsg{Code: tea.KeyLeft}
	case "right":
		return tea.KeyPressMsg{Code: tea.KeyRight}
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	case "ctrl+u":
		return tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl}
	case "alt+enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt}
	}
	if r := []rune(k); len(r) == 1 {
		return tea.KeyPressMsg{Code: r[0], Text: k}
	}
	return tea.KeyPressMsg{Code: tea.KeyExtended, Text: k}
}

// TestKeyPressNamesMatchKeyString checks the helper against the production
// key names: every key the fixture sends is the one keyString reads back.
func TestKeyPressNamesMatchKeyString(t *testing.T) {
	for _, k := range []string{"enter", " ", "esc", "tab", "shift+tab", "home", "up", "down", "pgup", "pgdown",
		"left", "right", "ctrl+c", "ctrl+u", "alt+enter", "q", "G", "?", "/"} {
		if got := keyString(keyPress(k)); got != k {
			t.Errorf("keyString(keyPress(%q)) = %q", k, got)
		}
	}
	if got := keyString(tea.KeyReleaseMsg{Code: 'q', Text: "q"}); got != "" {
		t.Errorf("a key release is %q, want no key", got)
	}
	if got := keyString(tea.PasteMsg{Content: "q"}); got != "" {
		t.Errorf("a paste is %q, want no key", got)
	}
}

// receive pulls whatever is pending for the TUI, as the receive goroutine
// would, and feeds it to the model.
func (f *fixture) receive(t *testing.T) {
	t.Helper()
	f.send(batchMsg{res: f.receiveRaw()})
}

// drainAndAck fully acknowledges whatever is currently pending for the TUI's
// session without feeding it to the model, as if the human wasn't running the
// TUI when it arrived: it can only be discovered later via History, the same
// as any message that predates the client's subscription.
func (f *fixture) drainAndAck(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	res, err := f.bus.Receive(ctx, bus.ReceiveInput{Session: f.c.sessionID(), IncludeOwn: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Batch == "" {
		return
	}
	if _, err := f.bus.Receive(ctx, bus.ReceiveInput{Session: f.c.sessionID(), Ack: res.Batch, IncludeOwn: true}); err != nil {
		t.Fatal(err)
	}
}

// selectTaskChannel walks the rail down until ch is selected, bounded so a
// key that stops moving focus can't hang the test.
func (f *fixture) selectTaskChannel(t *testing.T, ch string) {
	t.Helper()
	for i := 0; i <= len(f.m.railPositions()); i++ {
		if f.m.selName() == ch {
			return
		}
		f.key("down")
	}
	t.Fatalf("could not select %q, at %q", ch, f.m.selName())
}

// followTags subscribes the TUI's identity to each set and refreshes the rail.
func (f *fixture) followTags(t *testing.T, sets ...[]string) {
	t.Helper()
	for _, s := range sets {
		if err := f.c.b.SubscribeTags(context.Background(), bus.TagSubscribeInput{Patterns: s}); err != nil {
			t.Fatal(err)
		}
	}
	f.run(f.m.statusCmd())
}

// stream is the rendered stream without escapes.
func (f *fixture) stream() string { return stripANSI(f.m.renderStream()) }

// pendingChannels is the set of channels the identity of b is subscribed to,
// read the way a new session sees it: Open{}, closed at once.
func pendingChannels(t *testing.T, b bus.Bus) []string {
	t.Helper()
	ctx := context.Background()
	s, err := b.Open(ctx, bus.OpenSessionInput{})
	if err != nil {
		t.Fatalf("probe open: %v", err)
	}
	defer func() { _ = b.Close(ctx, bus.CloseSessionInput{Session: s.ID}) }()
	var out []string
	for _, p := range s.Pending {
		out = append(out, p.Channel)
	}
	return out
}

func TestInitSelectsFirstChannelAndLoadsHistory(t *testing.T) {
	f := newFixture(t)
	f.samSend("dev", "old one")
	f.reinit() // after the message exists
	if got := f.m.selected(); got == nil || got.Name != "dev" {
		t.Fatalf("selected = %v", got)
	}
	if len(f.m.msgs["dev"]) != 1 || f.m.msgs["dev"][0].Content != "old one" {
		t.Fatalf("history not loaded: %+v", f.m.msgs["dev"])
	}
	if f.m.unread("dev") != 0 {
		t.Fatalf("history is not unread, got %d", f.m.unread("dev"))
	}
}

// The compose text goes to the selected channel with the session id, so the
// server stamps the message with the server-assigned display name (ADR 0003).
// Display is asserted, not Sender: a Send without the session id would still
// arrive with Sender "eric" and Display "eric" (Review Focus 6).
func TestEnterSendsToSelectedChannel(t *testing.T) {
	f := newFixture(t)
	f.key("hello")
	f.key("enter")
	res, err := f.sam.Receive(context.Background(), bus.ReceiveInput{Session: f.samSession.ID, IncludeOwn: true})
	if err != nil {
		t.Fatal(err)
	}
	var got *bus.Message
	for i, x := range res.Messages {
		if x.Channel == "dev" && x.Content == "hello" {
			got = &res.Messages[i]
		}
	}
	if got == nil {
		t.Fatalf("sam did not receive hello on dev: %+v", res.Messages)
	}
	if got.Display != "eric/tui" || got.Sender != "eric" {
		t.Fatalf("Display=%q Sender=%q, want eric/tui from eric", got.Display, got.Sender)
	}
}

func TestReceiveShowsMessageAndUnread(t *testing.T) {
	f := newFixture(t)
	f.samSend("dev-notes", "remember this")
	f.receive(t)
	if f.m.unread("dev-notes") != 1 || f.m.unread("dev") != 0 {
		t.Fatalf("unread dev-notes=%d dev=%d", f.m.unread("dev-notes"), f.m.unread("dev"))
	}
	f.key("esc") // normal mode
	f.moveTo(t, "dev-notes")
	if f.m.selected().Name != "dev-notes" {
		t.Fatalf("moveTo did not select dev-notes: %v", f.m.selected())
	}
	if f.m.unread("dev-notes") != 0 {
		t.Fatalf("selecting must mark seen, unread=%d", f.m.unread("dev-notes"))
	}
	if f.m.divider < 0 {
		t.Fatal("divider must mark where new messages start")
	}
	if !strings.Contains(f.stream(), "remember this") {
		t.Fatalf("stream lacks the message:\n%s", f.stream())
	}
	f.moveTo(t, "dev")
	if f.m.selected().Name != "dev" {
		t.Fatal("did not go back to dev")
	}
}

// TestTabCyclesRailStreamCompose: compose -> rail -> stream -> compose, three
// panes wrapping; shift+tab goes back; home selects the channels heading from
// anywhere; there is no sessions pane to tab into (ADR 0006).
func TestTabCyclesRailStreamCompose(t *testing.T) {
	f := newFixture(t)
	f.samSend("dev", "x")
	f.receive(t)
	if f.m.pane() != paneCompose {
		t.Fatalf("start in compose, got %v", f.m.pane())
	}
	f.key("tab")
	if f.m.pane() != paneChannels || f.m.mode != modeNormal {
		t.Fatalf("tab from compose wraps to the rail, got pane=%v mode=%v", f.m.pane(), f.m.mode)
	}
	f.key("tab")
	if f.m.pane() != paneStream || f.m.selName() != "dev" {
		t.Fatalf("tab from the rail focuses dev's messages, got pane=%v sel=%q", f.m.pane(), f.m.selName())
	}
	f.key("tab")
	if f.m.pane() != paneCompose || f.m.mode != modeInsert {
		t.Fatalf("tab from the stream focuses compose, got pane=%v", f.m.pane())
	}
	f.key("tab")
	if f.m.pane() != paneChannels || !f.m.follow {
		t.Fatalf("a full lap must not leave follow off, pane=%v follow=%v", f.m.pane(), f.m.follow)
	}
	f.key("shift+tab")
	if f.m.pane() != paneCompose {
		t.Fatalf("shift+tab from the rail goes back to compose, got %v", f.m.pane())
	}
	f.key("shift+tab")
	if f.m.pane() != paneStream || f.m.cursor != 0 {
		t.Fatalf("shift+tab from compose focuses dev's newest message, got pane=%v cursor=%d", f.m.pane(), f.m.cursor)
	}

	f.key("down") // stays in the stream: the only message
	f.key("home")
	if f.m.pane() != paneChannels || f.m.cursor != -1 || f.m.headSel != secChannels {
		t.Fatalf("home selects the channels heading, got pane=%v cursor=%d headSel=%d", f.m.pane(), f.m.cursor, f.m.headSel)
	}
	f.key("i")
	f.key("x")
	if f.m.compose.Value() != "x" {
		t.Fatalf("compose must type, got %q", f.m.compose.Value())
	}
	f.key("home")
	if f.m.pane() != paneChannels || f.m.headSel != secChannels {
		t.Fatalf("home from compose returns to the channels heading, got pane=%v headSel=%d", f.m.pane(), f.m.headSel)
	}
	// With a heading selected, tab goes to the stream and back to the heading.
	f.key("tab")
	f.key("tab")
	f.key("tab")
	if f.m.pane() != paneChannels || f.m.headSel != secChannels {
		t.Fatalf("tab from the stream returns to the selected heading, got pane=%v headSel=%d", f.m.pane(), f.m.headSel)
	}
}

func TestGapsAreRecordedPerChannel(t *testing.T) {
	f := newFixture(t)
	f.send(batchMsg{res: bus.ReceiveResult{Gaps: []bus.Gap{{Channel: "dev", From: 3, To: 7}}}})
	if len(f.m.gaps["dev"]) != 1 || f.m.gapCount != 1 {
		t.Fatalf("gaps=%v count=%d", f.m.gaps, f.m.gapCount)
	}
}

func TestGapRendersEvictedDivider(t *testing.T) {
	f := newFixture(t)
	f.samSend("dev", "survivor")
	f.receive(t)
	f.send(batchMsg{res: bus.ReceiveResult{Gaps: []bus.Gap{{Channel: "dev", From: 3, To: 7}}}})
	if s := f.stream(); !strings.Contains(s, "5 evicted") {
		t.Fatalf("stream lacks the evicted divider:\n%s", s)
	}
}

func TestAddMessagesMergesAndDedupes(t *testing.T) {
	f := newFixture(t)
	f.m.addMessages("dev", []bus.Message{{Seq: 5}, {Seq: 2}})
	f.m.addMessages("dev", []bus.Message{{Seq: 3}, {Seq: 5}})
	got := f.m.msgs["dev"]
	if len(got) != 3 || got[0].Seq != 2 || got[1].Seq != 3 || got[2].Seq != 5 {
		t.Fatalf("got %+v", got)
	}
}

func TestPgUpAtTopLoadsOlderHistory(t *testing.T) {
	f := newFixture(t)
	for range 3 {
		f.samSend("dev", "m")
	}
	f.reinit()
	// Pretend only the newest message was loaded, then page up.
	f.m.msgs["dev"] = f.m.msgs["dev"][2:]
	f.key("pgup")
	if len(f.m.msgs["dev"]) != 3 {
		t.Fatalf("pgup at top must prepend older history, have %d", len(f.m.msgs["dev"]))
	}
}

func TestStatusMsgTracksSessionsAndNewChannels(t *testing.T) {
	f := newFixture(t)
	if _, err := f.sam.CreateChannel(context.Background(), bus.CreateChannelInput{Name: "late"}); err != nil {
		t.Fatal(err)
	}
	f.run(f.m.statusCmd())
	// general, dev, late; dev-notes (memory).
	if len(f.m.channels) != 4 || !f.c.isSubscribed("late") {
		t.Fatalf("new channel not picked up: %v subscribed=%v", f.m.channels, f.c.isSubscribed("late"))
	}
	for _, name := range []string{"sam", "eric/tui"} {
		if _, ok := f.m.sessionsSeen[name]; !ok {
			t.Fatalf("%s must be listed as seen: %v", name, f.m.sessionsSeen)
		}
	}
	if k := f.m.kind("dev-notes"); k != bus.KindMemory {
		t.Fatalf("kind(dev-notes) = %q", k)
	}
}

// A session not listed live for idleSessionTTL leaves the rail; one listed
// now stays.
func TestSessionsSeenAgeOut(t *testing.T) {
	f := newFixture(t)
	f.m.sessionsSeen["gone"] = time.Now().Add(-2 * idleSessionTTL)
	f.m.sessionsSeen["recent"] = time.Now().Add(-idleSessionTTL / 2)
	f.run(f.m.statusCmd())
	if _, ok := f.m.sessionsSeen["gone"]; ok {
		t.Fatal("a session unseen past the TTL must age out")
	}
	if _, ok := f.m.sessionsSeen["recent"]; !ok {
		t.Fatal("a session inside the TTL stays")
	}
}

func TestToastClearsOnKey(t *testing.T) {
	f := newFixture(t)
	f.send(receiveErrMsg{err: &bus.Error{Code: "internal", Message: "boom"}})
	if f.m.toast == "" {
		t.Fatal("receive error must toast")
	}
	f.key("x")
	if f.m.toast != "" {
		t.Fatal("any key clears the toast")
	}
}

// TestArrowsFollowTheFocusedPane: arrows act within the focused pane and
// never change it; tab/home move between panes.
func TestArrowsFollowTheFocusedPane(t *testing.T) {
	f := newFixture(t)
	f.samSend("dev", "one")
	f.samSend("dev", "two")
	f.reinit()    // after the messages exist
	f.key("esc")  // normal mode, channel pane
	f.key("down") // channels section: dev, then general
	if f.m.selected().Name != "general" || f.m.cursor >= 0 {
		t.Fatalf("down in the channel pane selects the next channel, got %v cursor=%d", f.m.selected(), f.m.cursor)
	}
	f.key("right")
	if f.m.pane() != paneChannels {
		t.Fatal("right in the channel pane never changes pane")
	}
	f.key("up")
	f.key("i")
	f.key("shift+tab")
	if f.m.pane() != paneStream || f.m.cursor != 1 {
		t.Fatalf("shift+tab from compose focuses the stream on its newest message, got pane=%v cursor=%d", f.m.pane(), f.m.cursor)
	}
	f.key("left")
	if f.m.pane() != paneStream {
		t.Fatal("left in the message pane never changes pane")
	}
	f.key("up")
	if f.m.cursor != 0 || f.m.selected().Name != "dev" {
		t.Fatalf("up in the message pane moves the cursor, not the channel: cursor=%d sel=%v", f.m.cursor, f.m.selected())
	}
	f.key("home")
	if f.m.pane() != paneChannels || f.m.cursor != -1 {
		t.Fatalf("home returns to the channel pane, got pane=%v cursor=%d", f.m.pane(), f.m.cursor)
	}
}

// TestEnterRepliesToTheCursorMessage: enter in the message pane replies to
// the cursor message; in the channel pane it just opens compose.
func TestEnterRepliesToTheCursorMessage(t *testing.T) {
	f := newFixture(t)
	one := f.samSend("dev", "one")
	f.samSend("dev", "two")
	f.receive(t)
	f.key("esc")
	f.key("enter")
	if f.m.mode != modeInsert || f.m.replyTo != nil {
		t.Fatalf("enter in the channel pane composes without a reply target, got mode=%v replyTo=%v", f.m.mode, f.m.replyTo)
	}
	f.key("shift+tab")
	f.key("up")
	f.key("enter")
	if f.m.mode != modeInsert || f.m.replyTo == nil || f.m.replyTo.Seq != one.Seq {
		t.Fatalf("enter on a message replies to it, got mode=%v replyTo=%v", f.m.mode, f.m.replyTo)
	}
}

// TestUnreadDividerPlacedOnce: the "new" divider sits before the first unread
// message of a first visit, and a second visit (everything seen) has none.
func TestUnreadDividerPlacedOnce(t *testing.T) {
	f := newFixture(t)
	old := f.samSend("dev-notes", "old")
	f.drainAndAck(t) // "old" is ack'd unseen: only History will ever surface it
	live := f.samSend("dev-notes", "live")
	f.receive(t)             // dev-notes now has "live" loaded but not the pre-existing "old" history
	f.key("esc")             // normal mode
	f.moveTo(t, "dev-notes") // triggers the first-ever history load
	if f.m.selected().Name != "dev-notes" {
		t.Fatalf("moveTo did not select dev-notes: %v", f.m.selected())
	}
	if want := live.Seq - 1; f.m.divider != want {
		t.Fatalf("divider = %d, want %d (== old.Seq %d)", f.m.divider, want, old.Seq)
	}
	if n := strings.Count(f.stream(), " new "); n != 1 {
		t.Fatalf("want exactly one new divider, got %d:\n%s", n, f.stream())
	}
	f.moveTo(t, "dev")
	f.moveTo(t, "dev-notes")
	if n := strings.Count(f.stream(), " new "); n != 0 {
		t.Fatalf("a revisit has nothing unread, got %d dividers:\n%s", n, f.stream())
	}
}

// TestNormalModeCursorScrollsIntoView: moving the cursor far in either
// direction must keep its rendered line inside the stream viewport.
func TestNormalModeCursorScrollsIntoView(t *testing.T) {
	f := newFixture(t)
	for i := range 40 {
		f.samSend("dev", fmt.Sprintf("msg %d", i))
	}
	f.receive(t)
	f.m.height = 20
	f.m.layout()
	f.key("shift+tab") // compose -> stream directly, into the message pane
	for range 30 {
		f.key("up")
	}
	if f.m.cursorLine < f.m.stream.YOffset() || f.m.cursorLine >= f.m.stream.YOffset()+f.m.stream.Height() {
		t.Fatalf("cursor scrolled out of view going up: cursorLine=%d YOffset=%d Height=%d", f.m.cursorLine, f.m.stream.YOffset(), f.m.stream.Height())
	}
	for range 30 {
		f.key("down")
	}
	if f.m.cursorLine < f.m.stream.YOffset() || f.m.cursorLine >= f.m.stream.YOffset()+f.m.stream.Height() {
		t.Fatalf("cursor scrolled out of view going down: cursorLine=%d YOffset=%d Height=%d", f.m.cursorLine, f.m.stream.YOffset(), f.m.stream.Height())
	}
}

func TestQuitKeysInNormalModeOnly(t *testing.T) {
	f := newFixture(t)
	f.key("q")
	if f.m.compose.Value() != "q" {
		t.Fatal("q in insert mode types")
	}
	f.key("esc")
	next, cmd := f.m.Update(keyPress("q"))
	f.m = next.(Model)
	if cmd == nil {
		t.Fatal("q in normal mode must quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("q must return tea.Quit")
	}
}

// A refresh keeps the selection by channel name, whichever way the channel
// list's sort order shifts under it.
func TestSetChannelsKeepsSelectionByName(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if _, err := f.sam.CreateChannel(ctx, bus.CreateChannelInput{Name: "alpha"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.sam.CreateChannel(ctx, bus.CreateChannelInput{Name: "zulu", Kind: bus.KindMemory}); err != nil {
		t.Fatal(err)
	}
	f.run(f.m.statusCmd()) // channels: alpha, dev, general; memories dev-notes, zulu
	f.key("esc")
	f.moveTo(t, "zulu")
	if err := f.sam.DeleteChannel(ctx, bus.DeleteChannelInput{Name: "alpha"}); err != nil {
		t.Fatal(err)
	}
	f.run(f.m.statusCmd()) // alpha drops out from ahead of zulu in the sort order
	if f.m.sel < 0 || f.m.channels[f.m.sel].Name != "zulu" {
		t.Fatalf("must still be on zulu after a channel ahead of it was deleted, got sel=%d", f.m.sel)
	}
	f.key("home")
	f.key("down")
	if f.m.selName() != "dev" {
		t.Fatalf("down from the channels heading must reach the first channel, got sel=%q", f.m.selName())
	}
}

// TestHomeFromStreamKeepsScrollAndDivider: focusPane must not run
// showSelected's full reset when the rail selection isn't changing, or home
// from the stream snaps it to the bottom and discards the "new" divider.
func TestHomeFromStreamKeepsScrollAndDivider(t *testing.T) {
	f := newFixture(t)
	for i := range 40 {
		f.samSend("dev", fmt.Sprintf("msg %d", i))
	}
	f.receive(t)
	f.m.height = 20
	f.m.layout()
	f.key("shift+tab") // compose -> stream directly
	f.key("g")         // scroll to the top; follow=false
	f.m.divider = 12   // a sentinel the full reset would clobber
	offset := f.m.stream.YOffset()
	f.key("home")
	if f.m.pane() != paneChannels {
		t.Fatalf("home must reach the channel pane, got %v", f.m.pane())
	}
	if f.m.stream.YOffset() != offset {
		t.Fatalf("home must not move the stream, got YOffset=%d want=%d", f.m.stream.YOffset(), offset)
	}
	if f.m.divider != 12 {
		t.Fatalf("home must not reset the divider, got %d", f.m.divider)
	}
}

// s toggles the identity's subscription: the server's subscription is gone
// (a probe session no longer lists dev) and a status tick does not bring it
// back; s again resubscribes (Decision 37).
func TestSubscribeToggle(t *testing.T) {
	f := newFixture(t)
	f.key("esc")
	if !slices.Contains(pendingChannels(t, f.bus), "dev") {
		t.Fatalf("setup: eric must be subscribed to dev: %v", pendingChannels(t, f.bus))
	}
	f.key("s")
	if f.c.isSubscribed("dev") || !strings.Contains(f.m.toast, "unsubscribed from dev") {
		t.Fatalf("s unsubscribes the selected channel: subscribed=%v toast=%q", f.c.isSubscribed("dev"), f.m.toast)
	}
	if slices.Contains(pendingChannels(t, f.bus), "dev") {
		t.Fatal("the server still lists dev for eric after s")
	}
	if rail := stripANSI(f.m.renderRails()); !strings.Contains(rail, "dev (off)") {
		t.Fatalf("rail must mark dev off:\n%s", rail)
	}
	// A status tick must not resubscribe a channel that is deliberately off.
	f.run(f.m.statusCmd())
	if f.c.isSubscribed("dev") || slices.Contains(pendingChannels(t, f.bus), "dev") {
		t.Fatal("status tick must not resubscribe an explicitly unsubscribed channel")
	}
	f.key("s")
	if !f.c.isSubscribed("dev") || !slices.Contains(pendingChannels(t, f.bus), "dev") {
		t.Fatal("s again resubscribes")
	}
}

// c opens "<name> [memory|tasks]". The kind picks the rail section (Decision
// 90); a bad name, an unknown kind, an extra word and an empty line are each
// refused with a toast and create nothing (Definition of Done B).
func TestCreateChannelPromptKinds(t *testing.T) {
	f, cb := newCountingFixture(t)
	create := func(line string) {
		t.Helper()
		f.key("esc")
		f.key("c")
		if !f.m.prompt.active {
			t.Fatal("c opens the create-channel prompt")
		}
		if line != "" {
			f.key(line)
		}
		f.key("enter")
	}
	names := func() []string {
		var out []string
		for _, c := range f.m.channels {
			out = append(out, c.Name)
		}
		return out
	}
	find := func(name string) (bus.Channel, bool) {
		for _, c := range f.m.channels {
			if c.Name == name {
				return c, true
			}
		}
		return bus.Channel{}, false
	}

	create("notes memory")
	if c, ok := find("notes"); !ok || c.Kind != bus.KindMemory || channelSection(c) != secMemories {
		t.Fatalf("notes must be a memory channel in the memories section: %v", names())
	}
	if rail := f.m.renderRails(); !strings.Contains(rail, iconMem+"notes") {
		t.Fatalf("rail lacks the memory icon on notes:\n%s", stripANSI(rail))
	}
	create("todo tasks")
	if c, ok := find("todo"); !ok || c.Kind != bus.KindTasks || channelSection(c) != secTasks {
		t.Fatalf("todo must be a tasks channel in the tasks section: %v", names())
	}
	f.key("esc")
	f.selectTaskChannel(t, "todo")
	if !f.m.isTasks("todo") || f.m.isTasks("dev") || f.m.kind("notes") != bus.KindMemory {
		t.Fatalf("kinds: todo=%q dev=%q notes=%q", f.m.kind("todo"), f.m.kind("dev"), f.m.kind("notes"))
	}
	create("plain")
	if c, ok := find("plain"); !ok || c.Kind != bus.KindOrdinary {
		t.Fatalf("an omitted kind is ordinary: %v", names())
	}

	calls := cb.count("CreateChannel")
	for _, tc := range []struct{ line, name, toast string }{
		{"bad/name", "bad/name", ""},
		{"x foo", "x", ""},
		{"x memory extra", "x", "usage: <name> [memory|tasks]"},
		{"", "", "usage: <name> [memory|tasks]"},
	} {
		f.m.toast = ""
		create(tc.line)
		if f.m.toast == "" {
			t.Errorf("%q: want a toast", tc.line)
		}
		if tc.toast != "" && f.m.toast != tc.toast {
			t.Errorf("%q: toast %q, want %q", tc.line, f.m.toast, tc.toast)
		}
		if _, ok := find(tc.name); ok && tc.name != "" {
			t.Errorf("%q: channel %q must not exist", tc.line, tc.name)
		}
		if tc.toast != "" {
			if n := cb.count("CreateChannel"); n != calls {
				t.Errorf("%q: the prompt's own refusal must not call CreateChannel (%d calls, was %d)", tc.line, n, calls)
			}
		}
		calls = cb.count("CreateChannel")
	}
}

func TestDeleteChannelConfirm(t *testing.T) {
	f := newFixture(t)
	f.key("esc")
	f.key("d")
	if f.m.mode != modeConfirmChannel || !strings.Contains(f.m.View().Content, "PERMANENTLY") {
		t.Fatalf("d must open the confirm line; mode=%v", f.m.mode)
	}
	f.key("n")
	if f.m.mode != modeNormal || f.m.selName() != "dev" {
		t.Fatal("any other key cancels")
	}
	hasChannel := func(name string) bool {
		return slices.ContainsFunc(f.m.channels, func(c bus.Channel) bool { return c.Name == name })
	}
	onBus := func(name string) bool {
		chans, err := f.bus.ListChannels(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return slices.ContainsFunc(chans, func(c bus.Channel) bool { return c.Name == name })
	}
	// A channel eric created can be deleted by eric.
	f.key("c")
	f.key("scratch")
	f.key("enter")
	f.moveTo(t, "scratch")
	f.key("d")
	f.key("y")
	if hasChannel("scratch") || onBus("scratch") {
		t.Fatalf("scratch must be gone: rail=%v bus=%v", hasChannel("scratch"), onBus("scratch"))
	}
	// A refusal is a toast and the channel stays: dev is sam's (eric holds no
	// admin permission on it), general is protected for everyone.
	for _, name := range []string{"dev", "general"} {
		f.m.toast = ""
		f.moveTo(t, name)
		f.key("d")
		f.key("y")
		if !strings.Contains(f.m.toast, "delete:") {
			t.Fatalf("deleting %s must toast a refusal, got %q", name, f.m.toast)
		}
		if !hasChannel(name) || !onBus(name) {
			t.Fatalf("%s must stay: rail=%v bus=%v", name, hasChannel(name), onBus(name))
		}
	}
}

// Review Focus 1: a non-admin's tick never calls Status and never toasts
// forbidden (Decision 73).
func TestStatusTickNonAdminIsQuiet(t *testing.T) {
	f, cb := newCountingFixture(t)
	before := cb.count("Identities")
	for range 3 {
		f.run(f.m.statusCmd())
	}
	if f.m.toast != "" || f.m.statusOK || f.m.admin {
		t.Fatalf("toast=%q statusOK=%v admin=%v", f.m.toast, f.m.statusOK, f.m.admin)
	}
	if n := cb.count("Status"); n != 0 {
		t.Fatalf("a non-admin called Status %d times", n)
	}
	if n := cb.count("Identities") - before; n != 3 {
		t.Fatalf("%d Identities calls, want 3 (the rounds must have run)", n)
	}
}

func TestStatusTickAdminShowsNotices(t *testing.T) {
	f, cb := newCountingFixture(t)
	setAdmin(t, f.core, "eric")
	err := f.core.Store.Tx(context.Background(), func(ctx context.Context, tx store.Tx) error {
		return tx.SetNotice(ctx, store.Notice{Kind: "capacity", Message: "store at 95%", SetAt: f.core.NowMs()})
	})
	if err != nil {
		t.Fatal(err)
	}
	for round := 1; round <= 2; round++ {
		f.run(f.m.statusCmd())
		if !f.m.statusOK || !f.m.admin {
			t.Fatalf("round %d: statusOK=%v admin=%v err=%v", round, f.m.statusOK, f.m.admin, f.m.statusErr)
		}
		if bar := stripANSI(f.m.renderStatusBar()); !strings.Contains(bar, "store at 95%") {
			t.Fatalf("round %d: status bar lacks the notice: %q", round, bar)
		}
		if n := cb.count("Status"); n != round {
			t.Fatalf("round %d: %d Status calls", round, n)
		}
		if f.m.toast != "" {
			t.Fatalf("round %d: toast %q", round, f.m.toast)
		}
	}
}

// Review Focus 2: a channel whose ACL denies read is marked once in the rail,
// selecting it shows one dim line, and nothing retries it on the tick.
func TestNoAccessChannelInRail(t *testing.T) {
	f, cb := newCountingFixture(t)
	ctx := context.Background()
	_, err := f.sam.CreateChannel(ctx, bus.CreateChannelInput{Name: "private", ACL: []bus.ACLEntry{
		{Identity: "sam", Permissions: []bus.Permission{bus.PermRead, bus.PermWrite, bus.PermAdmin}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	f.m.host.RailWidth = 40 // wide enough that the marker is not cut
	f.m.layout()
	f.run(f.m.statusCmd())
	if !f.c.noAccess("private") {
		t.Fatal("private must be marked no access after the first round")
	}
	if rail := stripANSI(f.m.renderRails()); !strings.Contains(rail, "private (no access)") {
		t.Fatalf("rail row must read private (no access):\n%s", rail)
	}
	if dim := openSeq(f.m.theme.Style(f.m.theme.Dim)); dim == "" || !strings.Contains(f.m.renderRails(), dim+iconChat+"private (no access)") {
		t.Fatalf("the no-access row must be drawn in the dim style (%q):\n%q", dim, f.m.renderRails())
	}
	f.key("esc")
	f.moveTo(t, "private")
	if s := f.stream(); !strings.Contains(s, "no read access to private") {
		t.Fatalf("stream must show the dim line, got %q", s)
	}
	subs, hist := cb.count("Subscribe"), cb.count("History")
	seq := f.m.toastSeq
	for range 3 {
		f.run(f.m.statusCmd())
	}
	f.run(f.m.loadHistory("private", nil))
	if got := f.m.toastSeq - seq; got > 1 {
		t.Fatalf("%d toasts for one refused History, want at most 1", got)
	}
	if n := cb.count("Subscribe"); n != subs {
		t.Fatalf("Subscribe grew from %d to %d: a no-access channel is never retried", subs, n)
	}
	if n := cb.count("History"); n != hist+1 {
		t.Fatalf("History calls %d, want %d (selecting it must not have asked, the one explicit load did)", n, hist+1)
	}
	// Selecting it again asks for nothing.
	f.moveTo(t, "dev")
	f.moveTo(t, "private")
	if n := cb.count("History"); n != hist+1 {
		t.Fatalf("re-selecting a no-access channel loaded history (%d calls)", n)
	}
}

func TestUsageMsgStoredQuietly(t *testing.T) {
	f := newFixture(t)
	f.send(usageMsg{err: errors.New("boom")})
	if f.m.usage != nil || f.m.toast != "" {
		t.Fatalf("a failed usage leaves usage nil and toasts nothing: usage=%v toast=%q", f.m.usage, f.m.toast)
	}
	f.send(usageMsg{usage: "u1"})
	f.send(usageMsg{err: errors.New("boom")})
	if f.m.usage != "u1" || f.m.toast != "" {
		t.Fatalf("an error keeps the last value: usage=%v toast=%q", f.m.usage, f.m.toast)
	}
}

// The sessions rail names live sessions by their server-assigned display
// name; the TUI's own is marked (you) in the user color.
func TestOwnSessionIsYou(t *testing.T) {
	f := newFixture(t)
	f.run(f.m.statusCmd())
	agentOpen := openSeq(f.m.theme.Style(f.m.theme.Agent))
	userOpen := openSeq(f.m.theme.Style(f.m.theme.User))
	var samRow, ericRow string
	for _, l := range strings.Split(f.m.renderRails(), "\n") {
		plain := stripANSI(l)
		switch {
		case strings.Contains(plain, "eric/tui"):
			ericRow = l
		case strings.Contains(plain, "sam"):
			samRow = l
		}
	}
	if !strings.Contains(ericRow, userOpen+"eric/tui (you)") {
		t.Fatalf("the TUI's own session row must read eric/tui (you) in the user color: %q", ericRow)
	}
	if !strings.Contains(samRow, agentOpen+"sam") || strings.Contains(stripANSI(samRow), "(you)") {
		t.Fatalf("another identity's session row must use the agent color: %q", samRow)
	}
}

// Pending item 2: the lines the entry printed before the alternate screen
// are carried in Host.Warnings and shown in health. The line itself is built
// by cli.RunTUI (Task 10).
func TestSharedIdentityWarningLine(t *testing.T) {
	line := "agentbus2 tui: identity eric has 2 other live session(s); subscriptions and acks are shared with them (pass -identity for a separate name)"
	f := newFixtureWith(t, nil, Host{Warnings: []string{line}})
	f.key("esc")
	f.key("h")
	if f.m.mode != modeHealth {
		t.Fatalf("h opens health, got %v", f.m.mode)
	}
	if got := stripANSI(strings.Join(f.m.healthLines(), "\n")); !strings.Contains(got, "identity eric has 2 other live session(s)") {
		t.Fatalf("health lacks the warning:\n%s", got)
	}
}

func TestSessionLostQuits(t *testing.T) {
	f := newFixture(t)
	lost := fmt.Errorf("%w: %w", errSessionLost, &bus.Error{Code: bus.CodeReplaced, Message: "another process took this session"})
	next, cmd := f.m.Update(sessionLostMsg{err: lost})
	f.m = next.(Model)
	if cmd == nil {
		t.Fatal("sessionLostMsg must quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("sessionLostMsg must return tea.Quit, got %T", cmd())
	}
	if !errors.Is(f.m.lost, errSessionLost) {
		t.Fatalf("m.lost = %v", f.m.lost)
	}
}

// Review Focus 8: no inbox view, behaviorally. Session rows are rail stops
// (TUI decision 14) that show details from what the TUI already holds, so
// walking every rail position and pressing keys on each makes no bus call
// for a session or identity, and every channel the TUI reads (History) or
// subscribes is one ListChannels returned: a tags: pane never reaches the bus.
func TestNoInboxView(t *testing.T) {
	f, cb := newCountingFixture(t)
	ctx := context.Background()
	if _, err := f.sam.CreateChannel(ctx, bus.CreateChannelInput{Name: "todo", Kind: bus.KindTasks}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.sam.Open(ctx, bus.OpenSessionInput{Hint: "impl"}); err != nil {
		t.Fatal(err)
	}
	f.followTags(t, []string{"docs"})
	chans, err := f.bus.ListChannels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, c := range chans {
		listed[c.Name] = true
	}
	tagPane := tagPanePrefix + "docs"
	wantStops := map[string]bool{tagPane: true}
	for n := range listed {
		wantStops[n] = true
	}
	notChannels := map[string]bool{"eric": true, "sam": true, "eric/tui": true, "sam/impl": true}
	for n := range listed {
		delete(notChannels, n)
	}

	counts := func() [6]int {
		return [6]int{cb.count("ListChannels"), cb.count("Identities"), cb.count("CreateChannel"), cb.count("Subscribe"), cb.count("History"), cb.count("Status")}
	}
	validMode := func() {
		t.Helper()
		if f.m.mode < modeInsert || f.m.mode > modeAssign {
			t.Fatalf("mode %d is not one of the seven declared", f.m.mode)
		}
	}

	f.key("esc")
	f.key("home")
	seen := map[string]bool{}
	sessionStops := map[string]bool{}
	sawSessions := false
	bound := 4 * len(f.m.railPositions())
	for step := 0; ; step++ {
		if step > bound {
			t.Fatalf("down never stopped within %d steps", bound)
		}
		pos := f.m.railPos()
		sel := f.m.selName()
		if notChannels[sel] {
			t.Fatalf("stop %+v selects %q, a session or identity name that is not a channel", pos, sel)
		}
		if pos.sec == secSessions && pos.item >= 0 {
			if sel != "" || f.m.sessSel == "" {
				t.Fatalf("session stop %+v selects channel %q (sessSel %q)", pos, sel, f.m.sessSel)
			}
			sessionStops[f.m.sessSel] = true
			// Every key a channel row would act on does nothing on a session row.
			before := counts()
			for _, k := range []string{"enter", "esc", "right", "left", "g", "G", "pgup", "s", "d", "r", "i", "tab", "esc"} {
				f.key(k)
				validMode()
				if f.m.mode == modeInsert {
					t.Fatalf("key %q on session row %q entered insert mode", k, f.m.sessSel)
				}
			}
			f.key("esc")
			if got := counts(); got != before {
				t.Fatalf("keys on session row %q made bus calls: %v -> %v", f.m.sessSel, before, got)
			}
			if f.m.railPos() != pos {
				t.Fatalf("keys moved the selection off session stop %+v to %+v", pos, f.m.railPos())
			}
			f.key("down")
			if f.m.railPos() == pos {
				break
			}
			continue
		}
		if pos.item >= 0 {
			seen[sel] = true
		}
		before := counts()
		wasCollapsed := f.m.collapsed
		f.key("enter")
		validMode()
		if pos.item < 0 {
			f.key("enter") // toggle back so the walk's positions stay put
			if f.m.collapsed != wasCollapsed {
				t.Fatalf("enter twice on heading %d must restore collapsed: %v -> %v", pos.sec, wasCollapsed, f.m.collapsed)
			}
		}
		f.key("esc")
		f.key("right")
		validMode()
		f.key("esc")
		f.key("esc")
		if pos.sec == secSessions && pos.item < 0 {
			sawSessions = true
			f.key("enter")
			if !f.m.collapsed[secSessions] {
				t.Fatalf("enter on the sessions heading must toggle collapsed[secSessions]")
			}
			if got := counts(); got != before {
				t.Fatalf("the sessions heading made bus calls: %v -> %v", before, got)
			}
			f.key("enter")
		}
		f.key("down")
		validMode()
		if f.m.railPos() == pos {
			break
		}
	}
	if !sawSessions {
		t.Fatal("the walk never reached the sessions heading")
	}
	if last := f.m.railPos(); last.sec != secSessions || last.item != len(f.m.sessionRows())-1 {
		t.Fatalf("down must end on the last session row, ended on %+v", last)
	}
	wantSessions := map[string]bool{}
	for _, r := range f.m.sessionRows() {
		wantSessions[r.display] = true
	}
	if len(wantSessions) < 3 || !sameSet(sessionStops, wantSessions) {
		t.Fatalf("session stops %v, want every session row %v", sessionStops, wantSessions)
	}
	if !sameSet(seen, wantStops) {
		t.Fatalf("item stops %v, want the ListChannels names plus the tags pane %v", seen, wantStops)
	}
	if cb.count("History") < 1 {
		t.Fatal("the walk loaded no history")
	}
	for _, name := range []string{"History", "Subscribe"} {
		for _, ch := range cb.channelsOf(name) {
			if !listed[ch] {
				t.Fatalf("%s was called with %q, which ListChannels did not return", name, ch)
			}
		}
	}
}

// stripANSI is ansi.Strip.
func stripANSI(s string) string { return ansi.Strip(s) }

// noColorTheme is th with every role the terminal default, so nothing it
// styles emits a color escape: the plain rendering Lip Gloss v1 gave under the
// Ascii profile (v2 has no global profile; it downsamples at output).
func noColorTheme(th Theme) Theme {
	nc := lipgloss.NoColor{}
	th.BG, th.Text, th.Dim, th.Stamp, th.Tag, th.Agent, th.User, th.Mem = nc, nc, nc, nc, nc, nc, nc, nc
	th.Tasks, th.Health, th.Warn, th.Error, th.Sel, th.SelInactive, th.SelText, th.Unsaved = nc, nc, nc, nc, nc, nc, nc, nc
	return th
}

// openSeq is the escape that opens style's color, "" when it renders none.
func openSeq(st lipgloss.Style) string {
	open, _, _ := strings.Cut(st.Render("\x00"), "\x00")
	return open
}

// sameSet reports whether a and b hold the same keys.
func sameSet(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}
