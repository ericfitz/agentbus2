package tui

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/version"
)

func TestViewShowsRailsStreamComposeAndStatus(t *testing.T) {
	f := newFixture(t)
	f.samSend("dev", "hello from sam")
	f.receive(t)
	f.run(f.m.statusCmd())
	// Wide enough that the version prefix doesn't truncate the normal-mode
	// key hints this test checks for below; truncation itself is
	// TestStatusBarShowsVersion's job.
	f.m.width = 140

	// Insert mode is the fixture's starting mode: the compose line reads
	// "dev ›" and the status bar shows the insert-mode key hints.
	v := stripANSI(f.m.View().Content)
	for _, want := range []string{"channels", "memories", "tasks", "tags", "sessions", "dev", iconMem + "dev-notes", "hello from sam", "sam", "eric/tui", "as eric", "esc commands", "alt+enter", "dev ›"} {
		if !strings.Contains(v, want) {
			t.Errorf("view lacks %q:\n%s", want, v)
		}
	}
	assertStatusBarIsLastLine(t, v, f.m.height)

	// Normal mode swaps the status bar for the command-key help.
	f.key("esc")
	v = stripANSI(f.m.View().Content)
	for _, want := range []string{"? help", "/ search", "h health", "q quit"} {
		if !strings.Contains(v, want) {
			t.Errorf("normal-mode view lacks %q:\n%s", want, v)
		}
	}
	assertStatusBarIsLastLine(t, v, f.m.height)
}

// assertStatusBarIsLastLine catches both a status bar that wrapped onto two
// rows (pushing the real last row off screen) and a layout that reserves one
// row too many or too few for it.
func assertStatusBarIsLastLine(t *testing.T, v string, height int) {
	t.Helper()
	lines := strings.Split(v, "\n")
	if len(lines) > height {
		t.Fatalf("view is %d lines for height %d:\n%s", len(lines), height, v)
	}
	if last := lines[len(lines)-1]; !strings.Contains(last, "agentbus2 v") {
		t.Fatalf("status bar must be the last line, got %q:\n%s", last, v)
	}
}

func TestStatusBarShowsVersion(t *testing.T) {
	f := newFixture(t)
	bar := ansi.Strip(f.m.renderStatusBar())
	if !strings.HasPrefix(bar, "agentbus2 v"+version.Version+"  as eric") || strings.Contains(bar, "eric/tui") {
		t.Fatalf("status bar: %q", bar)
	}
	f = newFixtureWith(t, nil, Host{Describe: "local · /tmp/x/bus.sqlite"})
	if bar := ansi.Strip(f.m.renderStatusBar()); !strings.Contains(bar, "as eric") {
		t.Fatalf("status bar: %q", bar)
	}
	for _, w := range []int{60, 40} {
		f.m.width = w
		if got := lipgloss.Width(f.m.renderStatusBar()); got > w {
			t.Fatalf("width %d: status bar is %d cols wide, must stay one row: %q", w, got, f.m.renderStatusBar())
		}
	}
}

func TestLongChannelNameAndShortRailDoNotOverflow(t *testing.T) {
	f := newFixture(t)
	long := strings.Repeat("x", 30)
	if _, err := f.sam.CreateChannel(context.Background(), bus.CreateChannelInput{Name: long}); err != nil {
		t.Fatal(err)
	}
	f.run(f.m.statusCmd())
	f.m.stream.SetHeight(1) // fewer rows than channels: rails must not grow past this
	v := stripANSI(f.m.View().Content)
	assertStatusBarIsLastLine(t, v, f.m.height)
}

// Review Focus 5: an 80x24 terminal with long server-assigned names
// (sam/impl-plan-the-release-12): every rail row and the status bar is
// exactly one line, with both icon sets, selected or not.
func TestNarrowTerminalKeepsOneLineRows(t *testing.T) {
	t.Cleanup(func() { setIcons(emojiIcons) })
	for _, set := range []string{"emoji", "nerdfont"} {
		LoadIcons(Settings{Icons: set}, io.Discard)
		f := newFixture(t)
		if _, err := f.sam.Open(context.Background(), bus.OpenSessionInput{Hint: "impl-plan-the-release-12"}); err != nil {
			t.Fatal(err)
		}
		if _, err := f.sam.CreateChannel(context.Background(), bus.CreateChannelInput{Name: "a-rather-long-channel-name-for-a-narrow-rail"}); err != nil {
			t.Fatal(err)
		}
		f.send(tea.WindowSizeMsg{Width: 80, Height: 24})
		f.run(f.m.statusCmd())
		f.key("esc")
		for step := range 2 * len(f.m.railPositions()) {
			rows := strings.Split(f.m.renderRails(), "\n")
			if len(rows) > f.m.stream.Height()+1 {
				t.Fatalf("%s: rail is %d rows, stream+1 is %d", set, len(rows), f.m.stream.Height()+1)
			}
			for _, r := range rows {
				if strings.Contains(r, "\n") || ansi.StringWidth(r) > f.m.railWidth() {
					t.Fatalf("%s step %d: rail row %q is %d wide, rail is %d", set, step, ansi.Strip(r), ansi.StringWidth(r), f.m.railWidth())
				}
			}
			v := stripANSI(f.m.View().Content)
			assertStatusBarIsLastLine(t, v, f.m.height)
			bar := f.m.renderStatusBar()
			if w := lipgloss.Width(bar); w > f.m.width || strings.Contains(bar, "\n") {
				t.Fatalf("%s: status bar is %d wide at width %d (or wraps): %q", set, w, f.m.width, bar)
			}
			f.key("down")
		}
	}
}

func TestStreamShowsNewDividerAndGap(t *testing.T) {
	f := newFixture(t)
	one := f.samSend("dev-notes", "one")
	f.receive(t)
	f.key("esc")
	f.moveTo(t, "dev-notes") // visit it: "one" is now seen
	f.moveTo(t, "dev")       // back to dev
	f.samSend("dev-notes", "two")
	f.send(batchMsg{res: bus.ReceiveResult{Gaps: []bus.Gap{{Channel: "dev-notes", From: one.Seq, To: one.Seq}}}})
	f.receive(t)
	f.moveTo(t, "dev-notes") // again: divider sits after "one"
	s := f.m.renderStream()
	if !strings.Contains(s, "new") || !strings.Contains(s, "1 evicted") {
		t.Fatalf("stream:\n%s", s)
	}
	if strings.Index(s, "one") > strings.Index(s, "new") {
		t.Fatalf("divider must sit after the last seen message:\n%s", s)
	}
}

func TestReplyBannerAndMemoryHint(t *testing.T) {
	f := newFixture(t)
	r := f.samSend("dev", "q")
	f.receive(t)
	f.key("esc")
	f.key("r")
	if v := stripANSI(f.m.View().Content); !strings.Contains(v, "replying to sam") || !strings.Contains(v, "#"+itoa(r.Seq)) {
		t.Fatalf("view:\n%s", v)
	}
	f.key("esc")
	f.key("esc")
	f.moveTo(t, "dev-notes")
	if v := stripANSI(f.m.View().Content); !strings.Contains(v, "new memory") {
		t.Fatalf("memory channel hint missing:\n%s", v)
	}
}

func TestIdleSessionShowsAge(t *testing.T) {
	f := newFixture(t)
	f.m.sessionsSeen["codex"] = time.Now().Add(-14 * time.Minute)
	rail := stripANSI(f.m.renderRails())
	if !strings.Contains(rail, iconIdle+"codex 14m") {
		t.Fatalf("rail:\n%s", rail)
	}
}

func TestRailIsFifthOfScreenWithFloor(t *testing.T) {
	f := newFixture(t)
	for _, tc := range []struct{ width, rail int }{{60, 16}, {100, 20}, {200, 40}} {
		f.m.width = tc.width
		f.m.layout()
		if f.m.stream.Width() != tc.width-tc.rail-1 {
			t.Errorf("width %d: stream %d, want %d", tc.width, f.m.stream.Width(), tc.width-tc.rail-1)
		}
	}
}

func TestEmptyChannelShowsHint(t *testing.T) {
	f := newFixture(t)
	v := stripANSI(f.m.View().Content)
	if !strings.Contains(v, "no messages yet in dev") {
		t.Fatalf("view:\n%s", v)
	}
	// The header counts what is loaded, not a server-side total (Decision 94).
	if h := ansi.Strip(f.m.renderHeader()); !strings.Contains(h, "dev · 0 unread · 0 loaded") {
		t.Fatalf("header %q", h)
	}
}

// A selected row must keep its background across every styled segment: the
// inner timestamp and sender styles end with a reset, which used to drop the
// highlight for the rest of the first line.
func TestSelectedRowKeepsBackgroundAcrossSegments(t *testing.T) {
	f := newFixture(t)
	f.samSend("dev", "hello from sam")
	f.receive(t)
	f.key("shift+tab") // compose -> stream directly: cursor lands on the last message
	first := strings.SplitN(f.m.renderStream(), "\n", 2)[0]
	if !strings.Contains(first, "sam") || strings.Count(first, "44m") < 2 {
		t.Fatalf("segments after a reset lost the selection background: %q", first)
	}
	rail := strings.SplitN(f.m.renderRails(), "\n", 3)[1]
	if !strings.Contains(rail, markSel) || !strings.Contains(rail, "100m") || strings.Contains(rail, "44m") {
		t.Fatalf("selected channel row must keep a background, the inactive one while the stream has focus: %q", rail)
	}
	if f.m.theme.Sel == f.m.theme.Dim {
		t.Fatal("selection background must differ from the dim text color")
	}
}

// TestRailSelectionUsesInactiveColorWithoutFocus (ADR 0011 section 3): the
// rail's selected channel row is on the selection color (blue, 44m) while the
// rail has focus and on selection_inactive (brightblack, 100m) while the
// stream does; the stream cursor stays on selection.
func TestRailSelectionUsesInactiveColorWithoutFocus(t *testing.T) {
	f := newFixture(t)
	f.samSend("dev", "hello from sam")
	f.receive(t)
	f.key("esc") // compose -> channel list: the rail has focus
	selectedRow := func() string {
		for _, l := range strings.Split(f.m.renderRails(), "\n") {
			if strings.Contains(l, markSel) {
				return l
			}
		}
		return ""
	}
	if row := selectedRow(); !strings.Contains(row, "44m") || strings.Contains(row, "100m") {
		t.Fatalf("focused rail row must use selection: %q", row)
	}
	f.key("tab") // stream
	if f.m.pane() != paneStream {
		t.Fatalf("pane = %v, want stream", f.m.pane())
	}
	if row := selectedRow(); !strings.Contains(row, "100m") || strings.Contains(row, "44m") {
		t.Fatalf("unfocused rail row must use selection_inactive: %q", row)
	}
	if first := strings.SplitN(f.m.renderStream(), "\n", 2)[0]; !strings.Contains(first, "44m") {
		t.Fatalf("stream cursor keeps selection: %q", first)
	}
}

// TestRailSelectionTextColorOnFocus: a focused rail's selected row renders
// its whole text in selection_text (black, 30m), replacing the per-segment
// colors (cyan agent fg, 36m); an unfocused selection keeps the original
// colors.
func TestRailSelectionTextColorOnFocus(t *testing.T) {
	f := newFixture(t)
	f.samSend("dev", "hello from sam")
	f.receive(t)
	f.key("esc") // compose -> channel list: the rail has focus
	selectedRow := func() string {
		for _, l := range strings.Split(f.m.renderRails(), "\n") {
			if strings.Contains(l, markSel) {
				return l
			}
		}
		return ""
	}
	if row := selectedRow(); !strings.Contains(row, "30m") || strings.Contains(row, "36m") {
		t.Fatalf("focused rail row must use selection_text, not the agent color: %q", row)
	}
	f.key("tab") // stream: rail loses focus
	if row := selectedRow(); !strings.Contains(row, "36m") || strings.Contains(row, "30m") {
		t.Fatalf("unfocused rail row must keep its original color: %q", row)
	}
}

// The gear's erase and cursor-forward escapes must stay invisible to lipgloss,
// or the rail border drifts.
func TestIconAgentWidth(t *testing.T) {
	if w := lipgloss.Width(iconAgent); w != lipgloss.Width("⚙️ ") {
		t.Fatal(w)
	}
}

// The task-list icon is a Wide emoji like chat and memory, so it needs no
// CSI erase/cursor trick: rail, compose, search, and the task pane title all
// line up on the same column.
func TestIconTasksIsWideEmojiWithoutEscapes(t *testing.T) {
	if w := lipgloss.Width(iconTasks); w != lipgloss.Width(iconChat) {
		t.Fatalf("iconTasks width %d, want %d", w, lipgloss.Width(iconChat))
	}
	if strings.Contains(iconTasks, "\x1b[") {
		t.Fatalf("iconTasks must not need the CSI erase trick: %q", iconTasks)
	}
}

// The channel name carries its kind's color (agent for chat, memory for
// memory) in the rail and the stream header, as it does in the compose row.
func TestChannelNameColoredInRailAndHeader(t *testing.T) {
	f := newFixture(t)
	ch := f.m.selected()
	if ch == nil {
		t.Fatal("no selected channel")
	}
	st := f.m.chanStyle(*ch)
	if want := st.Render(iconChat + ch.Name); !strings.Contains(f.m.renderCompose(), want) {
		t.Fatalf("compose lost the channel color: %q", f.m.renderCompose())
	}
	// The selected rail row re-applies the background after each reset, so
	// compare on the color's opening sequence plus the name.
	open := openSeq(st)
	if open == "" {
		t.Fatal("channel style renders no color")
	}
	if rail := f.m.renderRails(); !strings.Contains(rail, open+iconChat+ch.Name) {
		t.Fatalf("rail channel name uncolored: %q", rail)
	}
	if h := f.m.renderHeader(); !strings.Contains(h, open+iconChat+ch.Name) {
		t.Fatalf("header channel name uncolored: %q", h)
	}
}

// A memory channel's name carries the memory color, not the chat color, and
// chanStyle assigns the same color to the same channel every time it's asked
// (no per-render randomness or drift).
func TestMemoryChannelColorIsStable(t *testing.T) {
	f := newFixture(t)
	var notes bus.Channel
	for _, c := range f.m.channels {
		if c.Name == "dev-notes" {
			notes = c
		}
	}
	if notes.Name == "" {
		t.Fatal("dev-notes not in the channel list")
	}
	open := openSeq(f.m.chanStyle(notes))
	if open == "" {
		t.Fatal("memory channel style renders no color")
	}
	if open == openSeq(f.m.chanStyle(*f.m.selected())) {
		t.Fatal("memory and chat channels must not share a color")
	}
	if got := openSeq(f.m.chanStyle(notes)); got != open {
		t.Fatalf("chanStyle must assign the same channel the same color every call: %q vs %q", got, open)
	}
	if rail := f.m.renderRails(); !strings.Contains(rail, open+iconMem+"dev-notes") {
		t.Fatalf("memory channel uncolored in rail: %q", rail)
	}
}

// msgAt injects one already-loaded message so a header test controls every
// field (sender, channel, time, tags) without a bus round trip.
func (f *fixture) msgAt(ch, sender string, seq, createdAt int64, content string, tags ...string) {
	f.m.addMessages(ch, []bus.Message{{Seq: seq, Channel: ch, Sender: sender, CreatedAt: createdAt, Content: content, Tags: tags}})
	f.m.refreshStream()
}

func TestHeaderShowsSenderArrowChannel(t *testing.T) {
	f := newFixture(t)
	f.samSend("dev", "hello")
	f.receive(t)
	first := ansi.Strip(strings.SplitN(f.m.renderStream(), "\n", 2)[0])
	want := ansi.Strip(iconAgent) + "sam"
	if !strings.Contains(first, want) || !strings.Contains(first, "(today)") {
		t.Fatalf("channel header %q lacks %q", first, want)
	}
	if strings.Contains(first, strings.TrimSpace(iconArrow)) || strings.Contains(first, "dev") {
		t.Fatalf("own-channel header must drop the target: %q", first)
	}
	lines := strings.Split(ansi.Strip(f.m.renderStream()), "\n")
	if len(lines) < 2 || !strings.Contains(lines[1], "hello") || strings.Contains(lines[0], "hello") {
		t.Fatalf("body must start on the next line: %q", lines)
	}
}

// A sender renders as the server-assigned display name (ADR 0003), falling
// back to the bare sender when the server set none; the TUI's own identity
// takes the user icon whatever its session is called.
func TestHeaderUsesServerAssignedDisplayName(t *testing.T) {
	f := newFixture(t)
	st := lipgloss.NewStyle()
	msg := bus.Message{Seq: 1, Channel: "dev", Sender: "sam", Display: "sam/impl", CreatedAt: time.Now().UnixMilli()}
	if got := ansi.Strip(f.m.header(msg, "dev", "", st, 200)); !strings.Contains(got, ansi.Strip(iconAgent)+"sam/impl") {
		t.Errorf("header %q lacks the display name", got)
	}
	msg.Display = ""
	if got := ansi.Strip(f.m.header(msg, "dev", "", st, 200)); !strings.Contains(got, ansi.Strip(iconAgent)+"sam") || strings.Contains(got, "sam/") {
		t.Errorf("header %q must fall back to the sender", got)
	}
	own := bus.Message{Seq: 2, Channel: "dev", Sender: "eric", Display: "eric/tui", CreatedAt: time.Now().UnixMilli()}
	if got := ansi.Strip(f.m.header(own, "dev", "", st, 200)); !strings.Contains(got, ansi.Strip(iconUser)+"eric/tui") {
		t.Errorf("own header %q lacks the user icon and display name", got)
	}
}

// header drops "→ channel" only when the message's channel is the one being
// viewed; a mixed pane (tags, search) keeps it.
func TestHeaderTargetOnlyOutsideOwnChannel(t *testing.T) {
	f := newFixture(t)
	st := lipgloss.NewStyle()
	mk := func(ch string) bus.Message {
		return bus.Message{Seq: 1, Channel: ch, Sender: "sam", CreatedAt: time.Now().UnixMilli()}
	}
	arrow := strings.TrimSpace(iconArrow)
	for _, ch := range []string{"general", "dev-notes", "dev"} {
		if got := ansi.Strip(f.m.header(mk(ch), ch, "", st, 200)); strings.Contains(got, arrow) || strings.Contains(got, ch) {
			t.Errorf("own-channel header for %q keeps the target: %q", ch, got)
		}
	}
	for _, viewed := range []string{tagPanePrefix + "bug", "general"} {
		if got := ansi.Strip(f.m.header(mk("dev"), viewed, "", st, 200)); !strings.Contains(got, iconArrow+iconChat+"dev") {
			t.Errorf("header viewed from %q lost the target: %q", viewed, got)
		}
	}
}

func TestSelectedRowSwapsDimToText(t *testing.T) {
	f := newFixture(t)
	f.m.theme.Text = lipgloss.Color("7")
	f.samSend("dev", "one")
	f.samSend("dev", "two")
	f.receive(t)
	f.key("shift+tab") // cursor on "two"
	lines := strings.Split(f.m.renderStream(), "\n")
	stampOpen := openSeq(f.m.theme.Style(f.m.theme.Stamp))
	textOpen := openSeq(f.m.theme.Style(f.m.theme.Text))
	if !strings.Contains(lines[0], stampOpen+"(today)") {
		t.Fatalf("unselected row keeps the timestamp color: %q", lines[0])
	}
	if !strings.Contains(lines[2], textOpen+"(today)") || strings.Contains(lines[2], stampOpen+"(today)") {
		t.Fatalf("selected row swaps dim text to the text color: %q", lines[2])
	}
	if f.m.cursorLine != 2 {
		t.Fatalf("cursorLine=%d, want 2 (header + body of the first row)", f.m.cursorLine)
	}
}

func TestTagChipsAndFallback(t *testing.T) {
	f := newFixture(t)
	f.msgAt("dev", "sam", 9001, time.Now().UnixMilli(), "tagged", "release", "bug")
	chip := lipgloss.NewStyle().Background(f.m.theme.Tag).Foreground(lipgloss.Color("15"))
	first := strings.SplitN(f.m.renderStream(), "\n", 2)[0]
	if !strings.Contains(first, chip.Render(" release ")+" "+chip.Render(" bug ")) {
		t.Fatalf("chips missing: %q", first)
	}
	f.key("shift+tab") // selected row keeps the chip background
	if first := strings.SplitN(f.m.renderStream(), "\n", 2)[0]; !strings.Contains(first, chip.Render(" release ")) {
		t.Fatalf("selected row lost chip background: %q", first)
	}
	f.m.theme.Tag = lipgloss.NoColor{}
	if first := ansi.Strip(strings.SplitN(f.m.renderStream(), "\n", 2)[0]); !strings.Contains(first, "#release #bug") {
		t.Fatalf("fallback: %q", first)
	}
}

func TestHeaderNeverWrapsOnNarrowPane(t *testing.T) {
	f := newFixture(t)
	long := strings.Repeat("x", 40)
	if _, err := f.sam.CreateChannel(context.Background(), bus.CreateChannelInput{Name: long}); err != nil {
		t.Fatal(err)
	}
	f.run(f.m.statusCmd())
	f.m.width = 60
	f.m.layout()
	// Select the channel before the message arrives: msgAt calls addMessages
	// directly, and a first select on an empty channel leaves divider at -1,
	// so injecting afterward never inserts a "new" divider ahead of the
	// header line this test measures.
	for i, c := range f.m.channels {
		if c.Name == long {
			f.run(f.m.selectChannel(i))
		}
	}
	f.msgAt(long, "sam", 9001, time.Now().UnixMilli(), "body", "a", "b", "c")
	lines := strings.Split(f.m.renderStream(), "\n")
	if w := lipgloss.Width(lines[0]); w > f.m.stream.Width() {
		t.Fatalf("header wrapped or overflowed: width %d > %d: %q", w, f.m.stream.Width(), lines[0])
	}
	// The own-channel header drops the recipient, so only a header viewed
	// from elsewhere (a tag pane) is long enough to need the cut.
	msg := bus.Message{Channel: long, Sender: "sam", CreatedAt: time.Now().UnixMilli(), Tags: []string{"a", "b", "c"}}
	if w := lipgloss.Width(f.m.header(msg, tagPanePrefix+"a", "", lipgloss.NewStyle(), f.m.stream.Width())); w > f.m.stream.Width() {
		t.Fatalf("mixed-pane header overflowed: width %d > %d", w, f.m.stream.Width())
	}
	lines[0] = f.m.header(msg, tagPanePrefix+"a", "", lipgloss.NewStyle(), f.m.stream.Width())
	if s := ansi.Strip(lines[0]); !strings.Contains(s, "…") || strings.Contains(s, " a ") {
		t.Fatalf("tags go first, then the recipient is cut with …: %q", s)
	}
	if !strings.Contains(ansi.Strip(lines[1]), "body") {
		t.Fatalf("body still on line 2: %q", lines)
	}
}

// When the cut lands before an icon, ansi.Truncate drops the glyph but
// keeps its escapes; the gear's CSI 1C (cursor forward) past the ellipsis
// would push the line a column wider than counted. lipgloss measures CSI
// as zero width, so this inspects the bytes after the ellipsis: only SGR
// may follow it.
func TestHeaderCutDropsCursorEscapesPastEllipsis(t *testing.T) {
	f := newFixture(t)
	msg := bus.Message{Channel: "dev-notes", Sender: "sam", CreatedAt: time.Now().UnixMilli()}
	colored := f.m.theme
	for _, plain := range []bool{true, false} {
		profile := "colored"
		f.m.theme = colored
		if plain {
			profile = "no-color"
			f.m.theme = noColorTheme(colored)
		}
		full := lipgloss.Width(f.m.header(msg, tagPanePrefix+"a", "", lipgloss.NewStyle(), 200))
		for avail := 1; avail < full; avail++ {
			head := f.m.header(msg, tagPanePrefix+"a", "", lipgloss.NewStyle(), avail)
			i := strings.LastIndex(head, "…")
			if i < 0 {
				t.Fatalf("%s avail %d: no ellipsis: %q", profile, avail, head)
			}
			for rest := head[i+len("…"):]; rest != ""; {
				seq, _, n, _ := ansi.DecodeSequence(rest, ansi.NormalState, nil)
				if !strings.HasPrefix(seq, "\x1b[") || !strings.HasSuffix(seq, "m") {
					t.Fatalf("%s avail %d: %q after the ellipsis: %q", profile, avail, seq, head)
				}
				rest = rest[n:]
			}
			if !plain && !strings.HasSuffix(head, ansi.ResetStyle) {
				t.Fatalf("avail %d: the cut line must still close its style: %q", avail, head)
			}
		}
	}
}

func TestRailWidthClampsToTerminal(t *testing.T) {
	f := newFixture(t)
	for _, tc := range []struct{ saved, width, rail int }{
		{0, 100, 20},   // unset: a fifth
		{30, 100, 30},  // honored
		{4, 100, 16},   // floor
		{90, 100, 59},  // leaves the messages pane 40 columns
		{30, 60, 19},   // a narrow terminal shrinks the saved width
		{30, 200, 30},  // and a wide one restores it
		{5000, 60, 19}, // out of range in a hand edit
	} {
		f.m.host.RailWidth = tc.saved
		f.m.width = tc.width
		f.m.layout()
		if got := f.m.railWidth(); got != tc.rail {
			t.Errorf("saved %d, width %d: rail %d, want %d", tc.saved, tc.width, got, tc.rail)
		}
		if f.m.stream.Width() != tc.width-tc.rail-1 {
			t.Errorf("saved %d, width %d: stream %d, want %d", tc.saved, tc.width, f.m.stream.Width(), tc.width-tc.rail-1)
		}
	}
}

// recordedWidths is a Host.SaveRailWidth that records every width saved.
func recordedWidths(saved *[]int) func(int) error {
	return func(n int) error { *saved = append(*saved, n); return nil }
}

func TestRailKeysResizeAndPersist(t *testing.T) {
	var saved []int
	f := newFixtureWith(t, nil, Host{SaveRailWidth: recordedWidths(&saved)})
	f.key("esc")
	f.key(">")
	if got := f.m.railWidth(); got != 22 {
		t.Fatalf("after >: rail %d, want 22", got)
	}
	if f.m.stream.Width() != 100-22-1 {
		t.Fatalf("stream %d did not reflow", f.m.stream.Width())
	}
	f.key("<")
	f.key("<")
	if got := f.m.railWidth(); got != 18 {
		t.Fatalf("after < <: rail %d, want 18", got)
	}
	if len(saved) != 3 || saved[0] != 22 || saved[2] != 18 {
		t.Fatalf("saved widths %v, want 22, 20, 18", saved)
	}
	// A restart reads the saved width back through the Host.
	m := New(f.c, f.m.theme, Host{RailWidth: saved[len(saved)-1]})
	m.width = 100
	if got := m.railWidth(); got != 18 {
		t.Fatalf("restart rail %d", got)
	}
}

func TestRailKeysStopAtBounds(t *testing.T) {
	var saved []int
	f := newFixtureWith(t, nil, Host{SaveRailWidth: recordedWidths(&saved)})
	f.key("esc")
	f.m.width = 60
	f.m.layout()
	f.key("<") // already at the floor: nothing changes, nothing is saved
	if f.m.host.RailWidth != 0 || len(saved) != 0 {
		t.Fatalf("a no-op press saved %d / %v", f.m.host.RailWidth, saved)
	}
	f.key(">") // 16 -> 18
	f.key(">") // 19 is the ceiling at 60 columns; the saved width keeps stepping
	f.key(">")
	if got := f.m.railWidth(); got != 19 || f.m.host.RailWidth != 22 {
		t.Fatalf("rail %d, saved %d; want 19, 22", got, f.m.host.RailWidth)
	}
	if f.m.stream.Width() < streamMin {
		t.Fatalf("stream %d below the %d-column minimum", f.m.stream.Width(), streamMin)
	}
	for range 20 {
		f.key("<")
	}
	if got := f.m.railWidth(); got != railMin {
		t.Fatalf("rail %d, want floor %d", got, railMin)
	}
}

// ADR 0006 item 7: < and > step the saved width, not the clamped one on
// screen, so a narrow terminal does not overwrite a wide saved setting.
// A saved width under railMin steps from railMin, so < never saves a wider
// rail and > shows at once.
func TestRailKeysFromASavedWidthBelowMin(t *testing.T) {
	f := newFixture(t)
	f.key("esc")
	f.m.width = 200
	f.m.host.RailWidth = 5
	f.m.layout()
	f.key("<")
	if f.m.host.RailWidth != 5 {
		t.Fatalf("< from 5 saved %d, want 5 (no change)", f.m.host.RailWidth)
	}
	f.key(">")
	if f.m.host.RailWidth != railMin+2 || f.m.railWidth() != railMin+2 {
		t.Fatalf("> from 5: saved %d, shown %d; want %d", f.m.host.RailWidth, f.m.railWidth(), railMin+2)
	}
}

func TestRailKeysStepFromTheSavedWidth(t *testing.T) {
	var saved []int
	f := newFixtureWith(t, nil, Host{SaveRailWidth: recordedWidths(&saved)})
	f.key("esc")
	f.m.width = 60
	f.m.host.RailWidth = 40 // shows as 19
	f.m.layout()
	f.key("<")
	if f.m.host.RailWidth != 38 || f.m.railWidth() != 19 {
		t.Fatalf("after <: saved %d, shown %d; want 38, 19", f.m.host.RailWidth, f.m.railWidth())
	}
	f.key(">")
	f.key(">")
	if f.m.host.RailWidth != 42 || f.m.railWidth() != 19 {
		t.Fatalf("after > >: saved %d, shown %d; want 42, 19", f.m.host.RailWidth, f.m.railWidth())
	}
	if saved[len(saved)-1] != 42 {
		t.Fatalf("last saved %v; want 42", saved)
	}
	f.m.width = 200 // a wide terminal shows the saved width
	f.m.layout()
	if got := f.m.railWidth(); got != 42 {
		t.Fatalf("wide terminal shows %d, want 42", got)
	}
}

// A save that fails is one toast; with no SaveRailWidth the width just applies.
func TestRailKeysSaveFailureToasts(t *testing.T) {
	f := newFixtureWith(t, nil, Host{SaveRailWidth: func(int) error { return context.DeadlineExceeded }})
	f.key("esc")
	f.key(">")
	if !strings.Contains(f.m.toast, "rail width not saved") || f.m.railWidth() != 22 {
		t.Fatalf("toast=%q rail=%d", f.m.toast, f.m.railWidth())
	}
	g := newFixture(t)
	g.key("esc")
	g.key(">")
	if g.m.toast != "" || g.m.railWidth() != 22 {
		t.Fatalf("with no saver: toast=%q rail=%d", g.m.toast, g.m.railWidth())
	}
}

// With a revision indicator the header reads stamp, sender, [-> channel],
// indicator, chips; a narrow pane cuts the chips before the indicator, then
// the whole line with an ellipsis.
func TestHeaderRevisionIndicatorOrderAndNarrowCut(t *testing.T) {
	f := newFixture(t)
	st := lipgloss.NewStyle()
	msg := bus.Message{Seq: 1, Channel: "dev", Sender: "sam", CreatedAt: time.Now().UnixMilli(), Tags: []string{"alpha", "beta"}}
	full := ansi.Strip(f.m.header(msg, tagPanePrefix+"a", "r3 · 5 kept", st, 200))
	iArrow, iRev, iTag := strings.Index(full, strings.TrimSpace(iconArrow)), strings.Index(full, "r3 · 5 kept"), strings.Index(full, "alpha")
	if iArrow <= 0 || iArrow >= iRev || iRev >= iTag {
		t.Fatalf("want sender, arrow, indicator, chips in order: %q", full)
	}
	noTags := msg
	noTags.Tags = nil
	base := f.m.header(noTags, tagPanePrefix+"a", "r3", st, 200)
	bw := lipgloss.Width(base)
	// Room for the indicator but none for chips: chips go, indicator stays.
	if got := ansi.Strip(f.m.header(msg, tagPanePrefix+"a", "r3", st, bw+3)); !strings.Contains(got, "r3") || strings.Contains(got, "alpha") || strings.Contains(got, "…") {
		t.Fatalf("chips must be cut before the indicator: %q", got)
	}
	// Too narrow for the indicator: the whole line is cut with an ellipsis.
	got := f.m.header(msg, tagPanePrefix+"a", "r3", st, bw-2)
	if w := lipgloss.Width(got); w > bw-2 || !strings.Contains(ansi.Strip(got), "…") {
		t.Fatalf("narrow header %q (width %d)", ansi.Strip(got), w)
	}
	// No indicator: unchanged from before.
	if got := ansi.Strip(f.m.header(noTags, tagPanePrefix+"a", "", st, 200)); got != strings.TrimRight(got, " ") {
		t.Fatalf("no indicator, no trailing gap: %q", got)
	}
}

// The sessions section lists the display name of every live session, an
// identity dim only when it differs from the display name's prefix, and the
// TUI's own row reads "name (you)".
func TestSessionRowShowsIdentityOnlyWhenItDiffers(t *testing.T) {
	f := newFixture(t)
	f.m.width = 200
	f.m.identities = []bus.IdentityInfo{
		{Name: "sam", Sessions: []bus.SessionInfo{{ID: "s1", Identity: "sam", DisplayName: "sam/impl"}, {ID: "s2", Identity: "sam", DisplayName: "renamed"}}},
		{Name: "eric", Sessions: []bus.SessionInfo{{ID: f.c.sessionID(), Identity: "eric", DisplayName: "eric/tui"}}},
	}
	rows := map[string]string{}
	for _, l := range strings.Split(ansi.Strip(f.m.renderRails()), "\n") {
		for _, name := range []string{"sam/impl", "renamed", "eric/tui"} {
			if i := strings.Index(l, name); i >= 0 {
				rows[name] = strings.TrimSpace(l[i:])
			}
		}
	}
	for name, want := range map[string]string{
		"sam/impl": "sam/impl",
		"renamed":  "renamed (sam)",
		"eric/tui": "eric/tui (you)",
	} {
		if got := rows[name]; got != want {
			t.Errorf("row for %s = %q, want %q", name, got, want)
		}
	}
}
