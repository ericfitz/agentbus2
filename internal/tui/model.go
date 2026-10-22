package tui

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/ericfitz/agentbus2/internal/bus"
)

type mode int

const (
	modeInsert mode = iota // compose line focused; letters type
	modeNormal             // letter keymap active
	modeSearch
	modeHealth
	modeHelp
	modeConfirmChannel // d on the channel list; y deletes, anything else cancels
	modeAssign         // a in a task list: pick who the cursor task goes to
)

// pane is the focused main-screen region; tab and shift+tab cycle them and
// home returns to the channel list. Focus is derived from mode and cursor
// rather than stored, so the existing keymap keeps working unchanged. There
// is no sessions pane: a selected sessions row shows its details in the main
// pane and nothing else (ADR 0006, TUI decision 14).
type pane int

const (
	paneChannels pane = iota
	paneStream
	paneCompose
)

const (
	historyPage    = 200
	statusEvery    = 5 * time.Second
	toastFor       = 5 * time.Second
	idleSessionTTL = time.Hour
	railMin        = 16   // channel names stay readable at the 60-column minimum
	railStep       = 2    // columns a < or > press moves the divider
	railMax        = 1000 // the largest saved width the profile accepts
	streamMin      = 40   // the narrowest messages pane the rail may leave
)

// kindTags is the kind of a tag pane: a synthetic rail entry that never
// reaches the bus.
const kindTags bus.Kind = "tags"

// tick is tea.Tick, swapped out by tests so no command ever sleeps.
var tick = tea.Tick

func statusTick() tea.Cmd {
	return tick(statusEvery, func(time.Time) tea.Msg { return statusTickMsg{} })
}

// Host is what the entry that opened the bus tells the model about its
// surroundings; the model never imports the profile or client packages.
type Host struct {
	ConfigPath    string          // the config file the health overlay names and o edits
	Describe      string          // "local · <path>" or "server · <url>"; the health overlay's mode line
	RailWidth     int             // saved rail width; 0 for the default
	SaveRailWidth func(int) error // nil: not saved, no toast
	ReloadConfig  func() error    // re-validates the config after the editor closes; nil: not checked
	Warnings      []string        // lines the entry printed before the alternate screen; shown in health
}

// Model is the whole TUI state. Bubble Tea copies it by value on every
// Update, so maps and slices are shared and the pointer-receiver helpers
// mutate the copy Update is working on.
type Model struct {
	c      *client
	theme  Theme
	host   Host
	width  int
	height int
	mode   mode
	lost   error // set by sessionLostMsg; the entry returns it

	channels     []bus.Channel // sorted by section then name, sections secChannels..secTags contiguous; tag panes last
	kinds        map[string]bus.Kind
	sel          int
	railTop      [railSections]int  // each rail section's scroll offset: its first item drawn (see railWindow)
	headSel      int                // the selected rail heading (a section index), -1 when an item is selected; the stream keeps showing the last item
	sessSel      string             // display name of the selected sessions row, "" when none; the main pane shows its details and no channel is selected
	collapsed    [railSections]bool // sections drawn as their heading alone; lives only in this running TUI
	identities   []bus.IdentityInfo
	sessionsSeen map[string]time.Time // session display name -> last listed live
	admin        bool
	status       bus.Status
	statusOK     bool
	usage        any // *bus.Usage once the server reports it; nil otherwise
	statusErr    error

	msgs         map[string][]bus.Message
	tasks        map[string][]bus.TaskSummary // task channel name -> its current tree
	taskOpen     map[int64]bool               // task id -> its detail block is expanded
	taskExpanded map[int64]bool               // task id -> its direct children are shown (children start collapsed)
	taskDetail   map[int64]bus.Task           // task id -> its full task_get result, once loaded
	gaps         map[string][]bus.Gap
	loaded       map[string]bool
	seen         map[string]int64
	divider      int64
	cursor       int             // index into rows(selName()), the visible display order (taskRows(selName()) on a task channel); -1 for none
	cursorLine   int             // rendered line index of the cursor row's first line, from renderStream; -1 with no cursor
	cursorEnd    int             // rendered line index just past the cursor row's block (body, shown replies or subtasks, details), from renderStream
	expanded     map[int64]bool  // message seq -> its direct replies are shown
	peek         map[int64]int64 // thread root seq -> the one reply shown while collapsed
	bodyOpen     map[int64]bool  // message seq -> its body (subject line and full content) is shown

	// pendingTaskCursorCh/ID: a task_get/task_list jump target picked by
	// placeCursor before ch's tree had loaded, e.g. a search jump into a
	// task channel never visited yet. The tasksMsg handler consumes it (and
	// clears it) once ch's tasks arrive. "" / 0 for none.
	pendingTaskCursorCh string
	pendingTaskCursorID int64
	stream              viewport.Model
	follow              bool

	compose  textarea.Model
	replyTo  *bus.Message
	lastSent string
	prompt   promptState

	lastBatchAt time.Time
	gapCount    int
	receiveErr  error

	toast      string
	toastSeq   int
	toastHint  bool      // the toast is a hint (text color, no error prefix), not an error
	draft      taskDraft // the one unsaved task state change; id 0 for none
	assign     assignPicker
	lastNotice string

	// noColor: the terminal shows no color (Bubble Tea's color profile is
	// NoTTY or Ascii), so the tag chips fall back to words (tagChips).
	noColor bool

	search     searchState
	mem        memView
	health     healthState
	helpScroll int
}

func New(c *client, th Theme, h Host) Model {
	ta := textarea.New()
	ta.Prompt = ""
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.SetHeight(1)
	ta.KeyMap.InsertNewline.SetEnabled(false)
	// Dark until the terminal answers Init's background query, as Bubbles v1
	// assumed when nothing answered.
	ta.SetStyles(composeStyles(true))
	return Model{
		c:            c,
		theme:        th,
		host:         h,
		mode:         modeNormal, // channel list focused; i or enter opens compose
		sel:          -1,
		headSel:      -1,
		cursor:       -1,
		cursorLine:   -1,
		expanded:     map[int64]bool{},
		peek:         map[int64]int64{},
		bodyOpen:     map[int64]bool{},
		divider:      -1,
		follow:       true,
		kinds:        map[string]bus.Kind{},
		msgs:         map[string][]bus.Message{},
		tasks:        map[string][]bus.TaskSummary{},
		taskOpen:     map[int64]bool{},
		taskExpanded: map[int64]bool{},
		taskDetail:   map[int64]bus.Task{},
		gaps:         map[string][]bus.Gap{},
		loaded:       map[string]bool{},
		seen:         map[string]int64{},
		sessionsSeen: map[string]time.Time{},
		stream:       viewport.New(viewport.WithWidth(80), viewport.WithHeight(20)),
		compose:      ta,
	}
}

// Init loads the status and asks the terminal for its background color: the
// compose line's default colors depend on it (composeStyles). This is the only
// terminal query, and it runs only once `agentbus2 tui` has started, never at
// package init (Bubble Tea v1 queried at init in every agentbus2 process).
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.statusCmd(), statusTick(), tea.RequestBackgroundColor)
}

// composeStyles is the compose textarea's styles: the Bubbles defaults for a
// dark or light background with a static cursor in the terminal's own color,
// as Bubbles v1 drew it. Blinking is a perpetual tea.Cmd chain (bubbles/cursor
// reschedules itself on every message forever); fine for the real async
// runtime, but it never terminates against the fixture's synchronous cmd
// runner, so the cursor is static.
func composeStyles(isDark bool) textarea.Styles {
	s := textarea.DefaultStyles(isDark)
	s.Cursor.Color = lipgloss.NoColor{}
	s.Cursor.Blink = false
	return s
}

// keyString is the key as Bubble Tea names it ("q", "ctrl+c", "alt+enter"),
// or "" for any other message. Bubble Tea v2 names the space bar "space";
// it is " " here, as in v1, so the keymap is unchanged.
func keyString(msg tea.Msg) string {
	if k, ok := msg.(tea.KeyPressMsg); ok {
		if s := k.String(); s != "space" {
			return s
		}
		return " "
	}
	return ""
}

// Update is update followed by saving the rail sections' scroll offsets, so
// every path that moves the selection, resizes the screen, or changes a
// list scrolls the rail the same way.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.update(msg)
	if nm, ok := next.(Model); ok {
		nm.scrollRail()
		next = nm
	}
	return next, cmd
}

func (m Model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	if k := keyString(msg); k != "" {
		if m.toast != "" {
			m.toast = ""
			m.layout()
		}
		if k == "ctrl+c" {
			return m, tea.Quit
		}
	}
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		return m, nil
	case tea.BackgroundColorMsg:
		m.compose.SetStyles(composeStyles(msg.IsDark()))
		return m, nil
	case tea.ColorProfileMsg:
		m.noColor = msg.Profile == colorprofile.NoTTY || msg.Profile == colorprofile.Ascii
		return m, nil
	case batchMsg:
		cmds = append(cmds, m.onBatch(msg.res))
	case sessionLostMsg:
		// Another process took this session's watch or closed it: a retry
		// would fight it, so the program ends and the entry prints the error.
		m.lost = msg.err
		return m, tea.Quit
	case receiveErrMsg:
		m.receiveErr = msg.err
		cmds = append(cmds, m.showToast("receive: "+errText(msg.err)))
	case usageMsg:
		// Storage use is an administrator view; a failure leaves the last
		// value and is never a toast.
		if msg.err == nil {
			m.usage = msg.usage
		}
	case statusTickMsg:
		// The one periodic chain: Init starts it, and only this case
		// re-arms it, so opening Health (which also calls statusCmd) never
		// doubles the tick rate.
		cmds = append(cmds, m.statusCmd(), statusTick())
	case statusMsg:
		cmds = append(cmds, m.onStatus(msg))
	case subscribedMsg:
		if msg.err != nil {
			cmds = append(cmds, m.showToast("subscribe "+msg.channel+": "+errText(msg.err)))
		} else {
			cmds = append(cmds, m.statusCmd()) // a channel created from the prompt shows up in the rail
		}
	case historyMsg:
		if msg.err != nil {
			cmds = append(cmds, m.showToast("history: "+errText(msg.err)))
			break
		}
		m.loaded[msg.channel] = true
		// A prepend shifts
		// indices; keep the cursor on the same message. On a task channel
		// m.cursor indexes taskRows(selName()), not rows(selName()) -- tasksMsg
		// owns that cursor (see placeTaskCursor/pendingTaskCursorID), so it's
		// left untouched here.
		var cursorSeq int64
		if !m.isTasks(m.selName()) {
			if rs := m.rows(m.selName()); m.cursor >= 0 && m.cursor < len(rs) {
				cursorSeq = rs[m.cursor].msg.Seq
			}
		}
		// A pgup-at-top prepend grows the content above what's on screen;
		// remember the line count so the offset can grow by the same
		// amount below and the row the user was reading stays put.
		prevLines := 0
		if msg.prepend && msg.channel == m.selName() {
			prevLines = m.stream.TotalLineCount()
		}
		m.addMessages(msg.channel, msg.msgs)
		if !msg.prepend {
			// selectChannel already set the divider from whatever was
			// loaded at open time (e.g. a live message received before
			// this history page arrived); only fill it in here if nothing
			// was known to be unread yet, using seen as it stood at open
			// time (markSeen below hasn't advanced it for this load yet).
			if msg.channel == m.selName() && m.divider < 0 {
				m.divider = m.dividerFor(msg.channel)
			}
			if msg.channel == m.selName() {
				m.markSeen(msg.channel)
			}
		}
		m.refreshStream()
		switch {
		case cursorSeq > 0:
			m.placeCursor(cursorSeq)
		case msg.prepend && msg.channel == m.selName():
			m.stream.SetYOffset(m.stream.YOffset() + (m.stream.TotalLineCount() - prevLines))
		}
	case tasksMsg:
		if msg.err != nil {
			m.pendingTaskCursorCh, m.pendingTaskCursorID = "", 0
			cmds = append(cmds, m.showToast("tasks: "+errText(msg.err)))
			break
		}
		var cursorID int64
		if msg.ch == m.selName() {
			if t, ok := m.cursorTask(); ok {
				cursorID = t.ID
			}
		}
		if msg.ch == m.pendingTaskCursorCh {
			if msg.ch == m.selName() {
				cursorID = m.pendingTaskCursorID
			}
			m.pendingTaskCursorCh, m.pendingTaskCursorID = "", 0
		}
		m.tasks[msg.ch] = msg.tasks
		if cursorID != 0 {
			// A pending jump target may sit under collapsed parents.
			m.revealTask(msg.ch, cursorID)
			if i := m.taskIndex(msg.ch, cursorID); i >= 0 {
				m.cursor = i
			}
		}
		if msg.ch == m.selName() {
			if len(msg.tasks) > 0 {
				m.cursor = min(m.cursor, m.paneLen(msg.ch)-1)
			} else {
				m.cursor = -1
			}
		}
		for _, t := range msg.tasks {
			if m.taskOpen[t.ID] {
				cmds = append(cmds, m.loadTask(t.ID))
			}
		}
		m.refreshStream()
		if cursorID != 0 {
			m.scrollCursorIntoView()
		} else if m.follow && msg.ch == m.selName() {
			m.stream.GotoBottom()
		}
	case taskMsg:
		if msg.err != nil {
			cmds = append(cmds, m.showToast("task: "+errText(msg.err)))
			break
		}
		m.taskDetail[msg.task.ID] = msg.task
		m.refreshStream()
		if t, ok := m.cursorTask(); ok && t.ID == msg.task.ID && m.taskOpen[t.ID] {
			m.scrollOpenedIntoView()
		}
	case toastClearMsg:
		if msg.seq == m.toastSeq {
			m.toast = ""
			m.layout()
		}
	case sentMsg:
		if msg.err != nil {
			cmds = append(cmds, m.showToast("send: "+errText(msg.err)))
		}
	case searchMsg:
		if msg.query != m.search.query {
			break // stale: the overlay was reopened or a newer search is already in flight
		}
		m.recordQuery(msg.err == nil && !msg.res.SemanticUnavailable)
		m.search.err = msg.err
		m.search.ran = true
		if msg.err == nil {
			m.search.hits = msg.res.Hits
			m.search.cursor = 0
			m.search.textOnly = msg.res.SemanticUnavailable
			if m.search.mode != "text" {
				m.search.semanticDown = msg.res.SemanticUnavailable
			}
		} else {
			m.search.hits = nil
			m.search.textOnly = false
		}
	case configEditedMsg:
		cmds = append(cmds, m.onConfigEdited(msg))
	case revisionsMsg:
		cmds = append(cmds, m.applyRevisions(msg))
	}
	switch m.mode {
	case modeInsert:
		cmds = append(cmds, m.updateInsert(msg))
	case modeNormal:
		cmds = append(cmds, m.updateNormal(msg))
	case modeSearch:
		cmds = append(cmds, m.updateSearch(msg))
	case modeConfirmChannel:
		cmds = append(cmds, m.updateConfirmChannel(msg))
	case modeHealth:
		cmds = append(cmds, m.updateHealth(msg))
	case modeHelp:
		cmds = append(cmds, m.updateHelp(msg))
	case modeAssign:
		cmds = append(cmds, m.updateAssign(msg))
	}
	// A draft lives only on the cursor row of the focused task pane (ADR
	// 0011 decision 2): whatever the message above did, if that is no longer
	// where the draft is, the draft goes.
	if cmd := m.dropStaleDraft(); cmd != nil {
		cmds = append(cmds, cmd)
	}
	return m, tea.Batch(cmds...)
}

// updateInsert: compose focused. Only the keys the compose line owns plus
// tab/shift+tab/home (panes), pgup/pgdn (stream), esc (to normal mode) are
// handled here; everything else types.
func (m *Model) updateInsert(msg tea.Msg) tea.Cmd {
	if m.prompt.active {
		return m.updatePrompt(msg)
	}
	switch keyString(msg) {
	case "esc":
		if m.replyTo != nil {
			m.replyTo = nil
			m.layout()
			return nil
		}
		m.mode = modeNormal
		m.compose.Blur()
		return nil
	case "tab", "shift+tab", "home":
		return m.paneKey(msg)
	case "pgup", "pgdown":
		return m.scrollStream(msg)
	case "enter":
		if m.isTasks(m.selName()) {
			return nil
		}
		if isTagPane(m.selName()) {
			return m.showToast(tagReadOnlyToast)
		}
		return m.submitCompose()
	case "alt+enter":
		m.compose.InsertString("\n")
		m.fitCompose()
		return nil
	case "ctrl+u":
		m.compose.Reset()
		m.fitCompose()
		return nil
	case "up":
		if m.compose.Value() == "" && m.lastSent != "" {
			m.compose.SetValue(m.lastSent)
			m.fitCompose()
			return nil
		}
	case "down":
		// On the draft's last line down clears it (like ctrl+u); on an
		// earlier line it falls through so multi-line editing still works.
		if m.compose.Line() == m.compose.LineCount()-1 {
			m.compose.Reset()
			m.fitCompose()
			return nil
		}
	}
	var cmd tea.Cmd
	m.compose, cmd = m.compose.Update(msg)
	m.fitCompose()
	return cmd
}

// updateNormal is the letter keymap from the design notes.
func (m *Model) updateNormal(msg tea.Msg) tea.Cmd {
	if m.prompt.active {
		return m.updatePrompt(msg)
	}
	k := keyString(msg)
	if k != "" && !isVersionKey(k) {
		m.mem = memView{} // any other key shows the latest version again
	}
	switch k {
	case "q":
		return tea.Quit
	case "i":
		if m.sessSel != "" {
			return nil // no messaging from a session row
		}
		if m.isTasks(m.selName()) {
			return nil // no compose in a task list
		}
		if isTagPane(m.selName()) {
			return m.showToast(tagReadOnlyToast)
		}
		m.mode = modeInsert
		return m.compose.Focus()
	// enter performs the pane's action: save the task draft in a task list,
	// reply to the cursor message in the stream, compose to the selected
	// channel otherwise.
	case "enter":
		if m.headSel >= 0 && m.pane() == paneChannels {
			m.toggleSection(m.headSel, !m.collapsed[m.headSel])
			return nil
		}
		if m.sessSel != "" {
			return nil // no messaging from a session row
		}
		if m.isTasks(m.selName()) {
			if m.draft.id != 0 {
				return m.saveDraft()
			}
			return nil
		}
		if isTagPane(m.selName()) {
			return m.showToast(tagReadOnlyToast)
		}
		if r, ok := m.cursorRow(); ok {
			m.replyTo = &r.msg
			m.layout()
		}
		m.mode = modeInsert
		return m.compose.Focus()
	case "esc":
		if m.draft.id != 0 {
			m.draft = taskDraft{} // cancel the draft, and only the draft
			m.refreshStream()
			return nil
		}
		m.replyTo = nil
		m.cursor = -1
		m.layout()
	// up/down move within the focused pane. The rail is one list of headings
	// and items (railPositions); the stream moves its cursor. With a heading
	// selected, left collapses its section and right expands it; otherwise
	// right opens the cursor message's
	// body, then its direct replies; left hides its whole subtree, then its
	// body. In a task list right opens the task's details, then its
	// children; left hides its children (whole subtree), then its details.
	case "down":
		if m.pane() == paneStream {
			return m.moveCursor(1)
		}
		return m.stepRail(1)
	case "up":
		if m.pane() == paneStream {
			return m.moveCursor(-1)
		}
		return m.stepRail(-1)
	case "right":
		if m.headSel >= 0 && m.pane() == paneChannels {
			m.toggleSection(m.headSel, false)
			return nil
		}
		if m.isTasks(m.selName()) {
			return m.openTask()
		}
		m.openCursor()
	case "left":
		if m.headSel >= 0 && m.pane() == paneChannels {
			m.toggleSection(m.headSel, true)
			return nil
		}
		if m.isTasks(m.selName()) {
			m.closeTask()
		} else {
			m.closeCursor()
		}
	case "tab", "shift+tab", "home":
		return m.paneKey(msg)
	case "pgup", "pgdown":
		return m.scrollStream(msg)
	case "g":
		m.follow = false
		m.stream.GotoTop()
		return m.loadOlder()
	case "G":
		m.follow = true
		m.stream.GotoBottom()
	case " ":
		// Not a show/hide key anywhere (ADR 0011 decision 4): the arrows do
		// that; in a task list space cycles the state.
		if m.isTasks(m.selName()) {
			return m.cycleTaskState()
		}
	case "r":
		if m.sessSel != "" {
			return nil // no messaging from a session row
		}
		if m.isTasks(m.selName()) {
			return nil // no compose in a task list
		}
		if isTagPane(m.selName()) {
			return m.showToast(tagReadOnlyToast)
		}
		if rs := m.rows(m.selName()); m.cursor >= 0 && m.cursor < len(rs) {
			target := rs[m.cursor].msg
			m.replyTo = &target
		} else if n := len(rs); n > 0 {
			target := rs[n-1].msg
			m.replyTo = &target
		}
		m.layout()
		m.mode = modeInsert
		return m.compose.Focus()
	case "c":
		return m.createChannelPrompt()
	case "s":
		if m.headSel >= 0 {
			return nil
		}
		return m.toggleSubscribe()
	case "t":
		// Rail: follow a tag set. Task list: take (ADR 0011 decision 7).
		switch m.pane() {
		case paneChannels:
			return m.tagPrompt()
		case paneStream:
			if m.isTasks(m.selName()) {
				return m.takeTask()
			}
		}
	case "u":
		if m.isTasks(m.selName()) {
			return m.unassignTask()
		}
	case "a":
		if m.pane() == paneStream && m.isTasks(m.selName()) {
			return m.openAssign()
		}
	case "d":
		if m.headSel >= 0 || m.selected() == nil {
			return nil
		}
		if isTagPane(m.selName()) {
			return m.unfollowTagPane()
		}
		m.mode = modeConfirmChannel
	case "/":
		return m.openSearch()
	case ".":
		return m.stepVersion(true)
	case ",":
		return m.stepVersion(false)
	case "<":
		return m.resizeRail(-railStep)
	case ">":
		return m.resizeRail(railStep)
	case "h":
		return m.openHealth()
	case "?":
		return m.openHelp()
	}
	return nil
}

// resizeRail moves the rail/messages divider by delta columns from the
// saved width (the width shown now, when none is saved), not from the
// clamped on-screen one, so a narrow terminal never overwrites a wide saved
// setting (ADR 0006 item 7). It saves the result through Host.SaveRailWidth
// and renders whatever fits; a press at railMin or railMax does nothing, and
// a terminal too narrow to show the rail ignores the key.
func (m *Model) resizeRail(delta int) tea.Cmd {
	if !m.showLeft() {
		return nil
	}
	cur := m.host.RailWidth
	if cur <= 0 {
		cur = m.railWidth()
	}
	cur = max(cur, railMin) // a saved width under railMin shows as railMin
	next := min(max(cur+delta, railMin), railMax)
	if next == cur {
		return nil
	}
	m.host.RailWidth = next
	m.layout()
	if m.host.SaveRailWidth == nil {
		return nil
	}
	if err := m.host.SaveRailWidth(next); err != nil {
		return m.showToast("rail width not saved: " + err.Error())
	}
	return nil
}

// pane reports which main-screen region has focus.
func (m *Model) pane() pane {
	switch {
	case m.mode == modeInsert:
		return paneCompose
	case m.cursor >= 0:
		return paneStream
	case m.headSel >= 0:
		return paneChannels // a heading is selected
	default:
		return paneChannels
	}
}

// paneKey handles tab (next pane), shift+tab (previous pane), and home
// (the channels heading). The cycle is rail, messages, compose, where the rail is
// whichever of a heading or a channel holds the selection (arrows move
// between those), so the messages of the highlighted channel are always one
// tab away. Panes with nothing to focus are skipped: the stream when it has
// no messages, compose when nothing is selected.
func (m *Model) paneKey(msg tea.Msg) tea.Cmd {
	d := 1
	switch keyString(msg) {
	case "shift+tab":
		d = -1
	case "home":
		m.leaveSession()
		m.headSel = secChannels
		return m.focusPane(paneChannels)
	}
	order := []pane{paneChannels, paneStream, paneCompose}
	i := slices.Index(order, m.pane())
	for range len(order) - 1 {
		i = (i + d + len(order)) % len(order)
		p := order[i]
		switch {
		case p == paneChannels:
			return m.focusPane(p)
		case p == paneStream && m.paneLen(m.selName()) > 0:
			return m.focusPane(p)
		case p == paneCompose && m.selName() != "" && !m.isTasks(m.selName()) && !isTagPane(m.selName()):
			return m.focusPane(p)
		}
	}
	return nil
}

// focusPane moves focus. Entering the stream keeps a cursor that is still
// valid, else lands on the newest message; leaving it resumes following new
// messages if the view is at the bottom (moveCursor stops following).
// Entering the rail keeps the lighter reset: it only drops the message
// cursor and resumes following, so it does not snap the stream to the bottom
// or discard the "new" divider.
func (m *Model) focusPane(p pane) tea.Cmd {
	if p == paneStream {
		m.mode = modeNormal
		m.compose.Blur()
		if n := m.paneLen(m.selName()); m.cursor < 0 || m.cursor >= n {
			m.cursor = n - 1
		}
		return m.moveCursor(0)
	}
	if p == paneCompose {
		m.follow = m.stream.AtBottom()
		m.mode = modeInsert
		return m.compose.Focus()
	}
	m.mode = modeNormal
	m.compose.Blur()
	// A heading may hold the selection: the stream keeps showing its item,
	// so there is nothing to switch to, only the cursor to drop.
	m.follow = m.stream.AtBottom()
	m.cursor = -1
	m.refreshStream()
	return nil
}

// railPos is one stop of the rail's up/down order: a section heading
// (item -1) or an item, which is an index into m.channels or, in the sessions
// section, into sessionRows (TUI decision 14).
type railPos struct{ sec, item int }

// railPositions is the rail's up/down order: each section's heading, then
// its items unless the section is collapsed.
func (m Model) railPositions() []railPos {
	var ps []railPos
	for sec := range railSections {
		ps = append(ps, railPos{sec, -1})
		if m.collapsed[sec] {
			continue
		}
		if sec == secSessions {
			for i := range m.sessionRows() {
				ps = append(ps, railPos{sec, i})
			}
			continue
		}
		lo, hi := m.sectionRange(sec)
		for i := lo; i < hi; i++ {
			ps = append(ps, railPos{sec, i})
		}
	}
	return ps
}

// railPos is where the rail's selection stands now: the selected heading,
// else the selected session row, else the shown channel.
func (m Model) railPos() railPos {
	if m.headSel >= 0 {
		return railPos{m.headSel, -1}
	}
	if m.sessSel != "" {
		if i := m.sessionIndex(m.sessSel); i >= 0 {
			return railPos{secSessions, i}
		}
		return railPos{secSessions, -1}
	}
	return m.shownPos()
}

// sessionIndex is the index of the sessions row displayed as name, -1 when
// there is none.
func (m Model) sessionIndex(name string) int {
	return slices.IndexFunc(m.sessionRows(), func(r sessionRow) bool { return r.display == name })
}

// leaveSession drops the session-row selection and restores the stream to
// the channel it showed. The details pane needs no bus call, so there is
// nothing to cancel.
func (m *Model) leaveSession() {
	if m.sessSel != "" {
		m.sessSel = ""
		m.refreshStream()
	}
}

// reconcileSession moves the selection to the sessions heading when the
// selected row is no longer listed (a session ended, an idle one aged out).
func (m *Model) reconcileSession() {
	if m.sessSel != "" && m.sessionIndex(m.sessSel) < 0 {
		m.leaveSession()
		m.headSel = secSessions
	}
}

// shownPos is the item the stream shows, whether or not a heading holds the
// highlight.
func (m Model) shownPos() railPos {
	if m.sel >= 0 && m.sel < len(m.channels) {
		return railPos{channelSection(m.channels[m.sel]), m.sel}
	}
	return railPos{secChannels, -1}
}

// headingFor is sec when that section is collapsed, else -1: the headSel
// value after an item in sec is selected. An item of a collapsed section
// (a search hit, say) leaves its heading selected, never a hidden row.
func (m Model) headingFor(sec int) int {
	if m.collapsed[sec] {
		return sec
	}
	return -1
}

// stepRail moves the rail selection d stops along railPositions, stopping
// at either end.
func (m *Model) stepRail(d int) tea.Cmd {
	ps := m.railPositions()
	cur := m.railPos()
	i := slices.Index(ps, cur)
	if i < 0 {
		i = slices.Index(ps, railPos{cur.sec, -1})
	}
	next := min(max(i+d, 0), len(ps)-1)
	if i < 0 || next == i {
		return nil
	}
	return m.selectRailPos(ps[next])
}

// selectRailPos selects p. A heading only takes the highlight: the stream
// keeps showing the last selected channel. A session row takes it too and
// the main pane shows the session's details. An item is selected as
// selectChannel would.
func (m *Model) selectRailPos(p railPos) tea.Cmd {
	switch {
	case p.item < 0:
		m.leaveSession()
		m.headSel = p.sec
		return nil
	case p.sec == secSessions:
		rows := m.sessionRows()
		if p.item >= len(rows) {
			return nil
		}
		m.headSel = -1
		m.sessSel = rows[p.item].display
		m.cursor = -1
		m.refreshStream() // draws nothing: no channel is selected
		return nil
	case m.headSel >= 0 && m.sel == p.item:
		m.headSel = -1 // leaving a heading for the channel the stream already shows
		return nil
	}
	return m.selectChannel(p.item)
}

// toggleSection collapses or expands section sec. Collapsing the section
// that holds the shown item moves the selection to its heading, since the
// item's row is gone; the stream keeps showing the item.
func (m *Model) toggleSection(sec int, collapse bool) {
	m.collapsed[sec] = collapse
	if collapse && (m.shownPos().sec == sec || sec == secSessions && m.sessSel != "") {
		m.leaveSession()
		m.headSel = sec
	}
}

// sectionUnread is the unread total over the items of section sec. The
// sessions section has none.
func (m *Model) sectionUnread(sec int) int {
	if sec == secSessions {
		return 0
	}
	n := 0
	lo, hi := m.sectionRange(sec)
	for _, c := range m.channels[lo:hi] {
		n += m.unread(c.Name)
	}
	return n
}

// paneLen is the stream pane's cursor bound for ch: the visible task rows
// of a task channel (renderTasks draws one row per shown task, not per raw
// revision in m.msgs), else the row count rows(ch) shows (fewer than
// paneMsgs(ch) when a thread's replies are collapsed).
func (m *Model) paneLen(ch string) int {
	if m.isTasks(ch) {
		return len(m.taskRows(ch))
	}
	return len(m.rows(ch))
}

// moveCursor moves the normal-mode stream cursor and scrolls to keep it
// visible (refreshStream renders the cursor row highlighted). paneLen bounds
// it to the visible task rows on a task channel (taskRows(ch), children
// collapsed by default) and the visible row count otherwise.
func (m *Model) moveCursor(d int) tea.Cmd {
	n := m.paneLen(m.selName())
	if n == 0 {
		m.cursor = -1
		return nil
	}
	m.cursor = min(max(m.cursor+d, 0), n-1)
	m.follow = false
	m.refreshStream()
	m.scrollCursorIntoView()
	return nil
}

// placeCursor puts the normal-mode cursor on the message with seq (no-op if
// it is not loaded) and scrolls just enough to bring it into view. On a task
// channel the cursor indexes taskRows(ch), not rows(ch), so this resolves to
// the task-channel case instead.
func (m *Model) placeCursor(seq int64) {
	ch := m.selName()
	if m.isTasks(ch) {
		m.placeTaskCursor(ch, seq)
		return
	}
	for i, r := range m.rows(ch) {
		if r.msg.Seq == seq {
			m.cursor = i
		}
	}
	m.follow = false
	m.refreshStream()
	m.scrollCursorIntoView()
}

// placeTaskCursor is placeCursor's task-channel case: seq is a raw
// revision's seq (e.g. a search hit), resolved to its task via that
// revision's MemoryID (m.msgs[ch] carries it; jumpTo adds the hit there
// before calling placeCursor), then to that task's row index in
// taskRows(ch) (revealTask expands any collapsed ancestors first so the
// row is shown). If ch's tree hasn't loaded yet, the id is remembered in
// pendingTaskCursorID for the tasksMsg that follows (every jump into a
// task channel triggers a loadTasks) to place once it arrives.
func (m *Model) placeTaskCursor(ch string, seq int64) {
	var id int64
	for _, x := range m.msgs[ch] {
		if x.Seq == seq && x.MemoryID != nil {
			id = *x.MemoryID
			break
		}
	}
	m.cursor = -1
	if id != 0 {
		m.revealTask(ch, id)
		m.cursor = m.taskIndex(ch, id)
	}
	if m.cursor < 0 && id != 0 {
		m.pendingTaskCursorCh, m.pendingTaskCursorID = ch, id
	}
	m.follow = false
	m.refreshStream()
	m.scrollCursorIntoView()
}

// scrollCursorIntoView clamps the stream's YOffset so cursorLine (the
// cursor's first rendered line, set by renderStream) lies within the visible
// window, scrolling by the minimum amount rather than recentering every move.
func (m *Model) scrollCursorIntoView() {
	if m.cursor < 0 || m.cursorLine < 0 || m.stream.Height() <= 0 {
		return
	}
	switch {
	case m.cursorLine < m.stream.YOffset():
		m.stream.SetYOffset(m.cursorLine)
	case m.cursorLine >= m.stream.YOffset()+m.stream.Height():
		m.stream.SetYOffset(m.cursorLine - m.stream.Height() + 1)
	}
}

// scrollOpenedIntoView scrolls up just enough to show what → revealed
// under the cursor (cursorLine through cursorEnd), keeping the cursor's
// header on screen when the block is taller than the pane.
func (m *Model) scrollOpenedIntoView() {
	if m.cursor < 0 || m.cursorLine < 0 || m.stream.Height() <= 0 {
		return
	}
	if m.cursorEnd > m.stream.YOffset()+m.stream.Height() {
		m.stream.SetYOffset(min(m.cursorLine, m.cursorEnd-m.stream.Height()))
	}
	m.scrollCursorIntoView()
}

func (m *Model) scrollStream(msg tea.Msg) tea.Cmd {
	if keyString(msg) == "pgup" && m.stream.AtTop() {
		return m.loadOlder()
	}
	m.follow = false
	var cmd tea.Cmd
	m.stream, cmd = m.stream.Update(msg)
	if m.stream.AtBottom() {
		m.follow = true
	}
	return cmd
}

// loadOlder pages history backwards from the oldest loaded message. A task
// channel has no history view (renderTasks ignores m.msgs), so paging it is
// pure waste.
func (m *Model) loadOlder() tea.Cmd {
	ch := m.selName()
	if m.isTasks(ch) {
		return nil
	}
	ms := m.msgs[ch]
	if ch == "" || len(ms) == 0 {
		return nil
	}
	before := ms[0].Seq
	return m.loadHistory(ch, &before)
}

func (m *Model) selName() string {
	if c := m.selected(); c != nil {
		return c.Name
	}
	return ""
}

// kind is the kind of channel name: a tag pane answers kindTags and a name
// the rail does not list answers the empty kind.
func (m *Model) kind(name string) bus.Kind {
	if k, ok := m.kinds[name]; ok {
		return k
	}
	for _, c := range m.channels {
		if c.Name == name {
			return c.Kind
		}
	}
	return ""
}

// isTasks reports whether name is a task list: the kind says so, the name
// never does (ADR 0008).
func (m *Model) isTasks(name string) bool { return m.kind(name) == bus.KindTasks }

// selected is the channel the stream shows, or nil.
func (m *Model) selected() *bus.Channel {
	if m.sessSel != "" || m.sel < 0 || m.sel >= len(m.channels) {
		return nil
	}
	return &m.channels[m.sel]
}

func (m *Model) unread(ch string) int {
	n := 0
	for _, x := range m.msgs[ch] {
		if x.Seq > m.seen[ch] {
			n++
		}
	}
	return n
}

// markSeen records the newest loaded seq of ch as seen.
func (m *Model) markSeen(ch string) {
	if ms := m.msgs[ch]; len(ms) > 0 {
		m.seen[ch] = max(m.seen[ch], ms[len(ms)-1].Seq)
	}
}

// dividerFor returns the "new" divider for ch: the seq just before the
// oldest currently-loaded message newer than the last-seen seq, or -1 if
// nothing loaded so far is unread. Call before markSeen advances seen[ch].
func (m *Model) dividerFor(ch string) int64 {
	for _, x := range m.msgs[ch] {
		if x.Seq > m.seen[ch] {
			return x.Seq - 1
		}
	}
	return -1
}

// selectChannel moves the channel-list selection (clamped) and applies
// showSelected's reset.
func (m *Model) selectChannel(i int) tea.Cmd {
	m.sessSel = ""
	if len(m.channels) == 0 {
		m.sel = -1
		return nil
	}
	m.sel = min(max(i, 0), len(m.channels)-1)
	m.headSel = m.headingFor(channelSection(m.channels[m.sel]))
	return m.showSelected()
}

// showSelected resets stream state for whatever selected() now points at:
// cursor cleared, following resumed, any pending reply cancelled, the "new"
// divider placed just before the oldest unread message loaded so far,
// everything marked seen, and history loaded on first visit. Shared by
// selectChannel and setChannels so every way of switching channel applies
// the same reset.
func (m *Model) showSelected() tea.Cmd {
	m.cursor = -1
	m.follow = true
	m.replyTo = nil
	m.pendingTaskCursorCh, m.pendingTaskCursorID = "", 0
	ch := m.selName()
	m.divider = m.dividerFor(ch)
	m.markSeen(ch)
	m.refreshStream()
	m.stream.GotoBottom()
	if m.isTasks(ch) {
		return m.loadTasks(ch)
	}
	var cmds []tea.Cmd
	if ch != "" && !m.loaded[ch] && !m.c.noAccess(ch) && !isTagPane(ch) {
		cmds = append(cmds, m.loadHistory(ch, nil))
	}
	// A tag pane is drawn from its source channels' loaded messages, so it
	// needs their latest pages; one page per source channel on first visit.
	if isTagPane(ch) {
		for _, c := range m.tagPaneSources() {
			if !m.loaded[c] {
				cmds = append(cmds, m.loadHistory(c, nil))
			}
		}
	}
	return tea.Batch(cmds...)
}

// addMessages merges in into the channel buffer, ascending by seq, dropping
// duplicates (a message can arrive via history and receive both).
func (m *Model) addMessages(ch string, in []bus.Message) {
	all := append(append([]bus.Message{}, m.msgs[ch]...), in...)
	sort.Slice(all, func(i, j int) bool { return all[i].Seq < all[j].Seq })
	out := all[:0]
	for i, x := range all {
		if i == 0 || x.Seq != all[i-1].Seq {
			out = append(out, x)
		}
	}
	m.msgs[ch] = out
}

func (m *Model) onBatch(res bus.ReceiveResult) tea.Cmd {
	m.lastBatchAt = time.Now()
	m.receiveErr = nil
	byCh := map[string][]bus.Message{}
	for _, x := range res.Messages {
		byCh[x.Channel] = append(byCh[x.Channel], x)
	}
	var cmds []tea.Cmd
	for ch, ms := range byCh {
		m.addMessages(ch, ms)
		for _, x := range ms {
			m.peekReply(ch, x)
		}
		if m.isTasks(ch) {
			// Only the list on screen reloads; the others load fresh when
			// selected (showSelected).
			if ch == m.selName() {
				cmds = append(cmds, m.loadTasks(ch))
			}
			// A revision of the drafted task newer than the one it was
			// taken from discards the draft (ADR 0011 decision 2). Comparing
			// against m.draft.rev, not just matching the id, is what tells
			// a genuine change from a message the draft already reflects:
			// a fresh "oldest" subscription's backlog can replay the task's
			// own earlier revisions (including its creation), and the TUI's
			// own save produces a revision that arrives here asynchronously,
			// possibly after a new draft has already started on the same
			// task from that saved state.
			if m.draft.id != 0 && ch == m.draft.ch {
				for _, x := range ms {
					if x.MemoryID != nil && *x.MemoryID == m.draft.id && x.Revision != nil && *x.Revision > m.draft.rev {
						cmds = append(cmds, m.dropDraft())
						break
					}
				}
			}
		}
	}
	for _, g := range res.Gaps {
		m.gaps[g.Channel] = append(m.gaps[g.Channel], g)
		m.gapCount++
	}
	if cur := m.selName(); cur != "" {
		if _, ok := byCh[cur]; ok {
			m.markSeen(cur)
		}
	}
	m.refreshStream()
	if m.follow {
		m.stream.GotoBottom()
	}
	if res.Notice != "" && res.Notice != m.lastNotice {
		cmds = append(cmds, m.showToast(res.Notice))
	}
	m.lastNotice = res.Notice
	return tea.Batch(cmds...)
}

// onStatus refreshes the rail's data: the channels (learning those created
// since startup and subscribing from oldest so nothing is missed), the
// identities with their live sessions, the followed tag sets and, for an
// administrator, the status report. sessionsSeen tracks when each session
// display name was last listed live.
func (m *Model) onStatus(msg statusMsg) tea.Cmd {
	m.statusErr = msg.err
	if msg.err != nil {
		return nil
	}
	m.identities = msg.identities
	m.admin = msg.admin
	m.status, m.statusOK = msg.status, msg.statusOK
	now := time.Now()
	for _, id := range msg.identities {
		for _, s := range id.Sessions {
			m.sessionsSeen[s.DisplayName] = now
		}
	}
	for name, at := range m.sessionsSeen {
		if now.Sub(at) > idleSessionTTL {
			delete(m.sessionsSeen, name)
		}
	}
	m.reconcileSession()
	return m.setChannels(msg.channels, msg.tags, "oldest")
}

// setChannels replaces the channel list (sorted by section, then name), keeps
// the current selection by name, and subscribes to any channel not yet
// subscribed. Kinds come from the bus (ADR 0008); tag sets are synthetic
// entries after the channels.
func (m *Model) setChannels(chans []bus.Channel, tags [][]string, from string) tea.Cmd {
	cur := ""
	if m.sel >= 0 && m.sel < len(m.channels) {
		cur = m.channels[m.sel].Name
	}
	chans = slices.Clone(chans) // don't sort the caller's slice in place
	// Sections are contiguous in m.channels (sectionRange relies on it), each
	// sorted by name; tag sets are appended last, after the sort.
	sort.Slice(chans, func(i, j int) bool {
		si, sj := channelSection(chans[i]), channelSection(chans[j])
		if si != sj {
			return si < sj
		}
		return chans[i].Name < chans[j].Name
	})
	m.channels = slices.Clone(chans)
	m.kinds = make(map[string]bus.Kind, len(chans)+len(tags))
	for _, c := range chans {
		m.kinds[c.Name] = c.Kind
	}
	// Tag sets are rail entries too (a "tags" section under the channels):
	// synthetic, never subscribed on the bus, drawn as chips, rendered from
	// the messages already loaded for the ordinary and memory channels.
	for _, set := range tags {
		name := tagPanePrefix + strings.Join(set, ",")
		m.channels = append(m.channels, bus.Channel{Name: name, Kind: kindTags})
		m.kinds[name] = kindTags
	}
	m.sel = -1
	for i, c := range m.channels {
		if c.Name == cur {
			m.sel = i
		}
	}
	var cmds []tea.Cmd
	for _, c := range chans {
		if !m.c.known(c.Name) {
			cmds = append(cmds, m.subscribeCmd(c, from))
		}
	}
	// Only auto-select a first channel while nothing has picked one yet.
	if m.sel < 0 && len(m.channels) > 0 {
		// A selected session row stays selected: the channel only becomes
		// the one the stream shows once the selection leaves the row.
		sess := m.sessSel
		cmds = append(cmds, m.selectChannel(0))
		if sess != "" {
			m.sessSel, m.headSel = sess, -1
			m.refreshStream()
		}
	}
	return tea.Batch(cmds...)
}

func (m *Model) subscribeCmd(ch bus.Channel, from string) tea.Cmd {
	c := m.c
	return func() tea.Msg { return subscribedMsg{channel: ch.Name, err: c.subscribe(ch, from)} }
}

// statusCmd is one command: ListChannels, Identities and TagSubscriptions,
// then Status when the own row of that Identities result has Admin set (the
// answer of this round, not m.admin, which is last round's). A non-admin
// never calls Status, so a forbidden is never a toast (Decision 73); a
// Status failure only clears statusOK.
func (m *Model) statusCmd() tea.Cmd {
	c := m.c
	return func() tea.Msg {
		chans, err := c.b.ListChannels(c.ctx)
		if err != nil {
			return statusMsg{err: err}
		}
		ids, err := c.b.Identities(c.ctx)
		if err != nil {
			return statusMsg{channels: chans, err: err}
		}
		tags, err := c.b.TagSubscriptions(c.ctx)
		if err != nil {
			return statusMsg{channels: chans, identities: ids, err: err}
		}
		msg := statusMsg{channels: chans, identities: ids, tags: tags}
		for _, id := range ids {
			if id.Name == c.identity {
				msg.admin = id.Admin
			}
		}
		if msg.admin {
			st, err := c.b.Status(c.ctx)
			msg.status, msg.statusOK = st, err == nil
		}
		return msg
	}
}

// loadHistory fetches the newest historyPage messages of ch (before == nil)
// or the page before seq *before. A channel whose ACL denies read answers
// forbidden: it is marked no access through the client and toasts once.
func (m *Model) loadHistory(ch string, before *int64) tea.Cmd {
	c := m.c
	return func() tea.Msg {
		b := before
		if b == nil {
			top := int64(math.MaxInt64)
			b = &top
		}
		ms, err := c.b.History(c.ctx, bus.HistoryInput{Channel: ch, Before: b, Count: historyPage})
		if bus.CodeOf(err) == bus.CodeForbidden {
			c.markNoAccess(ch)
		}
		return historyMsg{channel: ch, msgs: ms, prepend: before != nil, err: err}
	}
}

// showToast shows s as an error (red, ✗ prefix) for toastFor.
func (m *Model) showToast(s string) tea.Cmd {
	m.toast = s
	m.toastHint = false
	m.toastSeq++
	m.layout()
	seq := m.toastSeq
	return tick(toastFor, func(time.Time) tea.Msg { return toastClearMsg{seq: seq} })
}

// showHint is showToast in the hint style: the text color, no prefix.
func (m *Model) showHint(s string) tea.Cmd {
	cmd := m.showToast(s)
	m.toastHint = true
	return cmd
}

// errText renders a bus error as "code: message" and anything else as-is.
func errText(err error) string {
	var be *bus.Error
	if errors.As(err, &be) {
		if be.Retryable {
			return fmt.Sprintf("%s: %s · retryable", be.Code, be.Message)
		}
		return string(be.Code) + ": " + be.Message
	}
	return err.Error()
}

// layout sizes the stream viewport and compose line against the rails
// (hidden when the terminal is narrow) and the extra rows the reply banner
// and toast add.
func (m *Model) layout() {
	w := m.width
	if m.showLeft() {
		w -= m.railWidth() + 1
	}
	m.stream.SetWidth(max(w, 20))
	// header (1) is already subtracted below; divider and status bar are the
	// only other always-present rows -- compose's own height is accounted
	// for separately via composeHeight, so it does not belong here too.
	extra := 2 // divider, status bar
	if m.replyTo != nil {
		extra++
	}
	if m.toast != "" {
		extra++
	}
	m.stream.SetHeight(max(m.height-1-composeHeight(m.compose)-extra, 3))
	m.compose.SetWidth(max(m.stream.Width()-len(m.selName())-16, 10))
	m.refreshStream()
}

func composeHeight(ta textarea.Model) int { return min(max(ta.LineCount(), 1), 3) }

func (m *Model) fitCompose() {
	if h := composeHeight(m.compose); h != m.compose.Height() {
		m.compose.SetHeight(h)
		m.layout()
	}
}

func (m *Model) refreshStream() { m.stream.SetContent(m.renderStream()) }

// markNoAccess records that the bus answered forbidden for reading name, so
// the rail marks it and nothing retries it (Decisions 80, 94). It lives here
// because the model is the only caller; the state it sets is the client's.
func (c *client) markNoAccess(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.subscribed[name] = subNoAccess
}
