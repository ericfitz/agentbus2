package tui

import (
	"fmt"
	"image/color"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/version"
)

// senderName renders a message's sender for display: the server-assigned
// display name (ADR 0003), else the bare sender when the server set none.
func senderName(x bus.Message) string {
	if x.Display != "" {
		return x.Display
	}
	return x.Sender
}

// isOwn reports whether x was sent by the TUI's own identity.
func (m Model) isOwn(x bus.Message) bool { return x.Sender == m.c.identity }

// agentLabel is a message's sender with its icon and color: the TUI's own
// identity in the user color, everyone else in the agent color, named by the
// server-assigned display name.
func (m Model) agentLabel(x bus.Message) string {
	own := m.isOwn(x)
	style := m.theme.Style(m.theme.Agent)
	if own {
		style = m.theme.Style(m.theme.User)
	}
	return m.senderIcon(own) + style.Render(senderName(x))
}

// senderIcon is the sender icon drawn in its color: the user color for the
// TUI's own identity, the agent color for everyone else. The gear's CSI
// erase/cursor escapes sit inside the styled run, which lipgloss and ansi
// measure as zero width, so styling never shifts a column.
func (m Model) senderIcon(own bool) string {
	if own {
		return m.theme.Style(m.theme.User).Render(iconUser)
	}
	return m.theme.Style(m.theme.Agent).Render(iconAgent)
}

// channelLabel is a message's recipient: the full channel name with its kind
// icon in chanStyle's color. A channel not (yet) in the rail is drawn as chat.
func (m Model) channelLabel(name string) string {
	ch := bus.Channel{Name: name, Kind: bus.KindOrdinary}
	for _, c := range m.channels {
		if c.Name == name {
			ch = c
		}
	}
	icon := iconChat
	switch ch.Kind {
	case bus.KindTasks:
		icon = iconTasks
	case bus.KindMemory:
		icon = iconMem
	}
	return m.chanStyle(ch).Render(icon + name)
}

// tagChips renders tags as " tag " chips on the tag color, one space apart.
// When the background alone emits no escape (tag is "default"), or the
// terminal shows no color (Bubble Tea's ColorProfileMsg reported NoTTY or
// Ascii, see Model.noColor; Lip Gloss v1 rendered no escape there), a chip
// would be invisible, so they fall back to dim "#tag" words.
func (m Model) tagChips(tags []string) string {
	if len(tags) == 0 {
		return ""
	}
	chip := lipgloss.NewStyle().Background(m.theme.Tag).Foreground(chipText(m.theme.Tag))
	parts := make([]string, len(tags))
	if m.noColor || lipgloss.NewStyle().Background(m.theme.Tag).Render("x") == "x" {
		for i, t := range tags {
			parts[i] = "#" + t
		}
		return m.theme.Style(m.theme.Dim).Render(strings.Join(parts, " "))
	}
	for i, t := range tags {
		parts[i] = chip.Render(" " + t + " ")
	}
	return strings.Join(parts, " ")
}

// header is a message's first line: timestamp, sender → recipient, memory
// revision indicator (rev, when set), tag chips. In the pane of the
// message's own channel the recipient is dropped (it is the pane); it stays
// in mixed panes (tags). It never wraps: past avail columns the chips are cut
// first (dropped under four columns), then the whole line, indicator
// included, is cut with an ellipsis, with no cursor escape past it.
func (m Model) header(x bus.Message, viewed, rev string, stampStyle lipgloss.Style, avail int) string {
	head := stampStyle.Render(stamp(x.CreatedAt)) + "  " + m.agentLabel(x)
	if x.Channel != viewed {
		head += iconArrow + m.channelLabel(x.Channel)
	}
	if rev != "" {
		head += "  " + m.theme.Style(m.theme.Mem).Render(rev)
	}
	if chips := m.tagChips(x.Tags); chips != "" {
		if room := avail - lipgloss.Width(head) - 2; room >= lipgloss.Width(chips) {
			head += "  " + chips
		} else if room >= 4 {
			head += "  " + ansi.Truncate(chips, room, "…")
		}
	}
	if lipgloss.Width(head) > avail {
		head = cut(head, avail, "…")
	}
	return head
}

// rowLine is a collapsed row's one text line: the subject, else the
// content's first line, cut to avail columns with an ellipsis. hasBody
// reports whether the content holds anything that line doesn't show: any
// content under a subject, a second line, or a first line that was cut.
func rowLine(x bus.Message, avail int) (line string, hasBody bool) {
	if x.Subject != "" {
		return ansi.Truncate(x.Subject, avail, "…"), x.Content != ""
	}
	first, rest, _ := strings.Cut(x.Content, "\n")
	line = ansi.Truncate(first, avail, "…")
	return line, rest != "" || line != first
}

func (m Model) showLeft() bool { return m.width >= 60 }

// railWidth is the left column. Until the user resizes it (< and >, saved
// through Host.SaveRailWidth) it is a fifth of the screen; either way it is clamped to
// [railMin, width-streamMin-1], the floor winning on a narrow terminal.
func (m Model) railWidth() int {
	w := m.width / 5
	if m.host.RailWidth > 0 {
		w = m.host.RailWidth
	}
	return min(max(w, railMin), max(m.width-streamMin-1, railMin))
}

// View is the frame Bubble Tea draws: render's screen on the alternate screen
// (Bubble Tea v2 declares the alternate screen per frame; v1 took it as the
// WithAltScreen program option).
func (m Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	return v
}

// render is the screen as a string, "" until the first window size.
func (m Model) render() string {
	if m.width == 0 {
		return ""
	}
	switch m.mode {
	case modeSearch:
		return m.viewSearch()
	case modeHealth:
		return m.viewHealth()
	case modeHelp:
		return m.viewHelp()
	case modeAssign:
		return m.viewAssign()
	}
	dim := m.theme.Style(m.theme.Dim)
	header := m.renderHeader()
	pane := m.stream.View()
	if m.sessSel != "" {
		pane = m.renderSession()
	}
	center := lipgloss.JoinVertical(lipgloss.Left, header, pane)
	body := center
	if m.showLeft() {
		border := strings.TrimRight(strings.Repeat(dim.Render("│")+"\n", m.stream.Height()+1), "\n")
		body = lipgloss.JoinHorizontal(lipgloss.Top, m.renderRails(), border, center)
	}
	parts := []string{body, dim.Render(strings.Repeat("─", m.width))}
	if m.replyTo != nil {
		parts = append(parts, m.renderReplyBanner())
	}
	parts = append(parts, m.renderCompose())
	if m.toast != "" {
		parts = append(parts, m.toastLine())
	}
	parts = append(parts, m.renderStatusBar())
	out := lipgloss.JoinVertical(lipgloss.Left, parts...)
	return lipgloss.NewStyle().Background(m.theme.BG).Foreground(m.theme.Text).Width(m.width).MaxHeight(m.height).Render(out)
}

// toastLine renders the toast: an error in red with the ✗ prefix, or a
// hint (draft guidance) in the text color with no prefix.
func (m Model) toastLine() string {
	if m.toastHint {
		return m.theme.Style(m.theme.Text).Render(m.toast)
	}
	return m.theme.Style(m.theme.Error).Render(iconError + m.toast)
}

// chanStyle is the color a channel's name is drawn in everywhere: task
// lists in the tasks color, memory channels in the memory color, the others
// in the agent color.
func (m Model) chanStyle(c bus.Channel) lipgloss.Style {
	switch c.Kind {
	case bus.KindTasks:
		return m.theme.Style(m.theme.Tasks)
	case bus.KindMemory:
		return m.theme.Style(m.theme.Mem)
	default:
		return m.theme.Style(m.theme.Agent)
	}
}

func (m Model) renderHeader() string {
	if m.sessSel != "" {
		return cut(m.theme.Style(m.theme.Agent).Render(m.sessSel), m.stream.Width(), "")
	}
	ch := m.selected()
	if ch == nil {
		return m.theme.Style(m.theme.Dim).Render("no channels yet · c to create one")
	}
	var s string
	if isTagPane(ch.Name) {
		s = m.tagChips(tagPaneSet(ch.Name)) + fmt.Sprintf(" · %d messages", len(m.rows(ch.Name)))
	} else {
		s = fmt.Sprintf("%s · %d unread · %d loaded", m.channelLabel(ch.Name), m.unread(ch.Name), len(m.msgs[ch.Name]))
	}
	// Cut a long title instead of Width's word-wrap, which would add a row
	// and push the status bar off the bottom of the screen; cut, not
	// MaxWidth, so an icon's cursor escape is never left behind.
	return cut(s, m.stream.Width(), "")
}

// Rail sections, top to bottom. railTop holds each one's scroll offset.
// m.channels holds sections secChannels..secTags contiguously, in this
// order (setChannels sorts them so); sessions are not in m.channels.
const (
	secChannels = iota
	secMemories
	secTasks
	secTags
	secSessions
	railSections
)

// railFixed is the number of rail rows outside the sections' items: one
// heading per section, plus, when the blank separators are drawn, the blank
// row before each heading after the first.
func railFixed(blanks bool) int {
	if blanks {
		return 2*railSections - 1
	}
	return railSections
}

// railHeadings are the section headings, indexed by section.
var railHeadings = [railSections]string{"channels", "memories", "tasks", "tags", "sessions"}

// channelSection is the rail section a channel row belongs to, from its
// kind (ADR 0008, Decision 90); a tag pane is the one synthetic entry.
func channelSection(c bus.Channel) int {
	switch {
	case isTagPane(c.Name):
		return secTags
	case c.Kind == bus.KindTasks:
		return secTasks
	case c.Kind == bus.KindMemory:
		return secMemories
	}
	return secChannels
}

// sectionRange is the slice [lo, hi) of m.channels that section sec
// (secChannels..secTags) occupies.
func (m Model) sectionRange(sec int) (lo, hi int) {
	lo = slices.IndexFunc(m.channels, func(c bus.Channel) bool { return channelSection(c) >= sec })
	if lo < 0 {
		return len(m.channels), len(m.channels)
	}
	hi = lo
	for hi < len(m.channels) && channelSection(m.channels[hi]) == sec {
		hi++
	}
	return lo, hi
}

// railLayout splits rows (the whole rail's height) among the sections of n
// items each. A section gets every row it needs when they all fit; otherwise
// each gets room for at least two items -- four rows, so both scroll arrows
// fit too -- and what is left goes round-robin to the sections still short.
// When those floors do not fit, it degrades in order: first the blank rows
// between sections go (blanks false; the headings still separate them), then
// the floor shrinks to one item plus one combined scroll row (compact true,
// two rows). A collapsed section or an empty one has budget 0 and keeps
// only its heading. On a screen too short even for that, the rail's own
// MaxHeight clips the bottom.
func railLayout(rows int, n [railSections]int, collapsed [railSections]bool) (b [railSections]int, blanks, compact bool) {
	floors := func(per int) (f [railSections]int, sum int) {
		for i := range n {
			if !collapsed[i] {
				f[i] = min(n[i], per)
				sum += f[i]
			}
		}
		return f, sum
	}
	for _, try := range []struct {
		blanks, compact bool
		per             int
	}{{true, false, 4}, {false, false, 4}, {false, true, 2}} {
		fixed := railFixed(try.blanks)
		f, sum := floors(try.per)
		b, blanks, compact = f, try.blanks, try.compact
		if rows-fixed >= sum {
			left := rows - fixed - sum
			for left > 0 {
				grew := false
				for i := range n {
					if left > 0 && !collapsed[i] && b[i] < n[i] {
						b[i]++
						left--
						grew = true
					}
				}
				if !grew {
					break
				}
			}
			return b, blanks, compact
		}
	}
	return b, blanks, compact
}

// railWindow is the slice [top, end) of n items drawn in budget rows. When
// they do not all fit, an arrow row above (items hidden before top) and one
// below (items hidden from end) each take a row of the budget; in compact
// mode one row carries both arrows, so any hidden items cost a single row.
// top starts from the section's previous offset and moves as little as it
// takes to show sel (-1 when the selection is in another section), so
// stepping past the last shown item scrolls the list by one; it never
// leaves empty rows under the last item.
func railWindow(n, budget, top, sel int, compact bool) (int, int) {
	if n <= budget {
		return 0, n
	}
	end := func(t int) int {
		if compact {
			return min(t+max(budget-1, 1), n)
		}
		k := budget
		if t > 0 {
			k-- // the up arrow
		}
		if t+k < n {
			k-- // the down arrow
		}
		return min(t+max(k, 1), n)
	}
	top = min(max(top, 0), n-1)
	if sel >= 0 && sel < n {
		top = min(top, sel)
		for sel >= end(top) {
			top++
		}
	}
	for top > 0 && end(top-1) == n {
		top--
	}
	return top, end(top)
}

// railSpan is one section's place in the rail: its item count, the selected
// item's index in it (-1 if the selection is elsewhere), and the window
// railWindow picked.
type railSpan struct {
	n, sel, top, end int
	collapsed        bool // only the heading is drawn; top and end are unused
}

// railSpans lays the five sections out for the current height and
// selection, scrolling each from its saved offset (railTop). blanks and
// compact are railLayout's: whether separator rows are drawn and whether
// scroll arrows share one row.
func (m Model) railSpans() (s [railSections]railSpan, blanks, compact bool) {
	var n [railSections]int
	for sec := secChannels; sec <= secTags; sec++ {
		lo, hi := m.sectionRange(sec)
		s[sec] = railSpan{n: hi - lo, sel: -1}
		if m.headSel < 0 && m.sessSel == "" && m.sel >= lo && m.sel < hi {
			s[sec].sel = m.sel - lo
		}
	}
	s[secSessions] = railSpan{n: len(m.sessionRows()), sel: -1}
	if m.sessSel != "" && m.headSel < 0 {
		s[secSessions].sel = m.sessionIndex(m.sessSel)
	}
	for i := range s {
		n[i] = s[i].n
		s[i].collapsed = m.collapsed[i]
	}
	b, blanks, compact := railLayout(m.stream.Height()+1, n, m.collapsed)
	for i := range s {
		if !s[i].collapsed {
			s[i].top, s[i].end = railWindow(s[i].n, b[i], m.railTop[i], s[i].sel, compact)
		}
	}
	return s, blanks, compact
}

// scrollRail saves each section's scroll offset after an update, so the next
// selection move scrolls from where the list is drawn now.
func (m *Model) scrollRail() {
	spans, _, _ := m.railSpans()
	for i, s := range spans {
		m.railTop[i] = s.top
	}
}

// sessionRow is one row of the sessions section: a live session
// of some identity, or a display name seen live within idleSessionTTL and
// not listed now.
type sessionRow struct {
	display  string
	identity string
	id       string
	live     bool
	seen     time.Time
}

// sessionRows lists every live session (from Identities) then the idle ones
// (from sessionsSeen), each group sorted by display name.
func (m Model) sessionRows() []sessionRow {
	live := map[string]bool{}
	var out, idle []sessionRow
	for _, id := range m.identities {
		for _, s := range id.Sessions {
			live[s.DisplayName] = true
			out = append(out, sessionRow{display: s.DisplayName, identity: id.Name, id: s.ID, live: true})
		}
	}
	for name, at := range m.sessionsSeen {
		if !live[name] {
			idle = append(idle, sessionRow{display: name, seen: at})
		}
	}
	for _, rs := range [][]sessionRow{out, idle} {
		sort.Slice(rs, func(i, j int) bool { return rs[i].display < rs[j].display })
	}
	return append(out, idle...)
}

// renderRails draws the left column: the channels, memories and tasks, the
// followed tag sets, and the session list, each under its
// heading and always shown; an empty or collapsed section is only its heading. A section longer than its
// share of the stream's height scrolls (see railLayout and railWindow),
// with a dim arrow row above or below for the items out of view. Every row
// is one line, cut with a trailing ellipsis to the rail's width -- the
// selected one too, since the header over the stream names it in full.
// Never Style.Width alone: it word-wraps overlong content instead of
// clipping it, which would grow the rail past the stream's height and push
// the rows below it (the compose line, the status bar) off the bottom of
// the screen.
func (m Model) renderRails() string {
	th := m.theme
	dim := th.Style(th.Dim)
	rail := m.railWidth()
	// Focus as pane() reports it: a rail row selected while the stream or
	// compose has focus takes the inactive selection color, so the one blue
	// row on screen is the one the arrow keys move.
	focus := m.pane()
	spans, blanks, compact := m.railSpans()
	var b strings.Builder
	// heading draws section sec's heading row: the glyph for its state, the
	// name, and when collapsed its item count and unread total. Selected, it
	// takes the one highlight on screen.
	heading := func(sec int, s railSpan) string {
		text := markOpen + " " + railHeadings[sec]
		if s.collapsed {
			info := strconv.Itoa(s.n)
			if u := m.sectionUnread(sec); u > 0 {
				info += ", " + strconv.Itoa(u) + " unread"
			}
			text = markSel + " " + railHeadings[sec] + " (" + info + ")"
		}
		if m.headSel == sec {
			return th.RailSel(focus == paneChannels, cut(text, rail, "…"), rail)
		}
		return cut(dim.Render(text), rail, "…")
	}
	// list writes one section's heading and window of rows; row(i) draws
	// item i unselected.
	list := func(sec int, focused bool, row func(i int) string) {
		s := spans[sec]
		if sec > secChannels && blanks {
			b.WriteString("\n")
		}
		b.WriteString(heading(sec, s) + "\n")
		if s.collapsed {
			return // railWindow would still show an item for budget 0
		}
		if !compact && s.top > 0 {
			b.WriteString(cut(dim.Render(fmt.Sprintf(" ▲ %d more", s.top)), rail, "…") + "\n")
		}
		for i := s.top; i < s.end; i++ {
			line := row(i)
			if i == s.sel {
				line = th.RailSel(focused, cut(markSel+line, rail, "…"), rail)
			} else {
				line = cut(" "+line, rail, "…")
			}
			b.WriteString(line + "\n")
		}
		switch {
		case compact && (s.top > 0 || s.end < s.n):
			// One row under the items carries both arrows.
			var parts []string
			if s.top > 0 {
				parts = append(parts, fmt.Sprintf("▲%d", s.top))
			}
			if s.end < s.n {
				parts = append(parts, fmt.Sprintf("▼%d", s.n-s.end))
			}
			b.WriteString(cut(dim.Render(" "+strings.Join(parts, " ")+" more"), rail, "…") + "\n")
		case s.end < s.n:
			b.WriteString(cut(dim.Render(fmt.Sprintf(" ▼ %d more", s.n-s.end)), rail, "…") + "\n")
		}
	}

	// chanRow draws the channel at index i of m.channels: its kind icon, full
	// name, unread count and markers. A channel the identity may not read is
	// drawn dim and marked, never retried (Decision 94).
	chanRow := func(i int) string {
		c := m.channels[i]
		mark := iconChat
		switch channelSection(c) {
		case secTasks:
			mark = iconTasks
		case secMemories:
			mark = iconMem
		}
		if m.c.noAccess(c.Name) {
			return dim.Render(mark + c.Name + " (no access)")
		}
		line := m.chanStyle(c).Render(mark + c.Name)
		if n := m.unread(c.Name); n > 0 {
			line += " " + th.Style(th.Agent).Render(strconv.Itoa(n))
		}
		if !m.c.isSubscribed(c.Name) {
			line += dim.Render(" (off)")
		}
		return line
	}
	for _, sec := range []int{secChannels, secMemories, secTasks} {
		lo, _ := m.sectionRange(sec)
		list(sec, focus == paneChannels, func(i int) string { return chanRow(lo + i) })
	}
	tagLo, _ := m.sectionRange(secTags)
	list(secTags, focus == paneChannels, func(i int) string {
		return m.tagChips(tagPaneSet(m.channels[tagLo+i].Name))
	})

	rows := m.sessionRows()
	list(secSessions, focus == paneChannels, func(i int) string {
		r := rows[i]
		if !r.live {
			age := time.Since(r.seen).Round(time.Minute)
			return iconIdle + dim.Render(r.display+" "+shortDur(age))
		}
		own := r.id == m.c.sessionID()
		name := th.Style(th.Agent).Render(r.display)
		if own {
			name = th.Style(th.User).Render(r.display + " (you)")
		}
		row := m.senderIcon(own) + name
		// The identity is noise when the display name already starts with it.
		if prefix, _, _ := strings.Cut(r.display, "/"); prefix != r.identity {
			row += " " + dim.Render("("+r.identity+")")
		}
		return row
	})
	col := strings.TrimSuffix(b.String(), "\n")
	return lipgloss.NewStyle().Width(rail).MaxHeight(m.stream.Height() + 1).Render(col)
}

// renderSession is the main pane for a selected session row (TUI decision
// 14): what the rail already knows of it, no bus call. A live row shows its
// identity and session id; an idle row, whose identity the TUI never learned,
// shows `unknown` and when it was last listed live.
func (m Model) renderSession() string {
	th := m.theme
	dim := th.Style(th.Dim)
	var lines []string
	line := func(k, v string) { lines = append(lines, dim.Render(k+"  ")+v) }
	i := m.sessionIndex(m.sessSel)
	if i < 0 {
		return ""
	}
	r := m.sessionRows()[i]
	line("name", r.display)
	if r.live {
		line("identity", r.identity)
		line("session", r.id)
		line("state", "live")
		if r.id == m.c.sessionID() {
			lines = append(lines, "", dim.Render("this is your session"))
		}
	} else {
		line("identity", "unknown")
		line("state", "idle, last seen "+stamp(r.seen.UnixMilli()))
	}
	for j := range lines {
		lines[j] = cut(lines[j], m.stream.Width(), "…")
	}
	return lipgloss.NewStyle().Width(m.stream.Width()).Height(m.stream.Height()).MaxHeight(m.stream.Height()).Render(strings.Join(lines, "\n"))
}

// renderStream draws the selected channel's messages, oldest first, with the
// "new" divider after the last seen message, "n evicted" dividers where the
// bus reported gaps, and the normal-mode cursor row highlighted. Each message
// is a header line (timestamp, sender, → recipient unless it is this channel, tag chips) followed by
// its row line (subject or first line) or, when opened, its body, at the
// row's depth.
func (m *Model) renderStream() string {
	th := m.theme
	dim := th.Style(th.Dim)
	ch := m.selName()
	m.cursorLine = -1
	if ch == "" {
		return ""
	}
	if m.isTasks(ch) {
		return m.renderTasks(ch)
	}
	if m.c.noAccess(ch) {
		return dim.Render("no read access to " + ch)
	}
	if len(m.paneMsgs(ch)) == 0 {
		return dim.Render("no messages yet in " + ch + " · type below to send the first")
	}
	w := max(m.stream.Width(), 20)
	divider := func(label string) string {
		side := strings.Repeat("─", max((w-len(label)-2)/2, 1))
		return dim.Render(side + " " + label + " " + side)
	}
	gaps := append([]bus.Gap{}, m.gaps[ch]...)
	sort.Slice(gaps, func(i, j int) bool { return gaps[i].From < gaps[j].From })
	var b strings.Builder
	lineNum := 0
	// Threads reorder messages, so a seq range no longer maps to one place
	// in the stream: evicted gaps are listed at the top.
	for _, g := range gaps {
		b.WriteString(divider(itoa(g.To-g.From+1)+" evicted") + "\n")
		lineNum++
	}
	newShown := m.divider < 0
	cursorDepth := -1 // the cursor row's depth while its shown replies follow
	for i, r := range m.rows(ch) {
		if cursorDepth >= 0 && r.depth <= cursorDepth {
			cursorDepth = -1
		}
		x := r.msg
		if !newShown && r.depth == 0 && r.newest > m.divider {
			b.WriteString(divider("new") + "\n")
			lineNum++
			newShown = true
		}
		selected := i == m.cursor && m.mode == modeNormal
		// On the selected row every dim segment (timestamp, marker, summary)
		// takes the text color so it stays readable on the selection
		// background; chips keep their own background.
		rowDim, stampStyle := dim, th.Style(th.Stamp)
		if selected {
			rowDim, stampStyle = th.Style(th.Text), th.Style(th.Text)
		}
		avail := m.rowAvail(r.depth)
		label := ""
		if x.MemoryID != nil && x.Revision != nil && *x.Revision > 1 {
			label = "r" + itoa(*x.Revision)
		}
		if v, ok := m.mem.version(x); ok {
			x.Sender, x.CreatedAt, x.Subject, x.Content = v.Sender, v.CreatedAt, v.Subject, v.Content
			// The shown version's own revision number, not its position:
			// replaced revisions are purged after tombstone_min_hours, so the
			// kept list can start above r1 (ADR 0011 decision 5).
			rev := strconv.Itoa(m.mem.idx + 1)
			if v.Revision != nil {
				rev = itoa(*v.Revision)
			}
			label = "r" + rev + " · " + strconv.Itoa(len(m.mem.revs)) + " kept"
		}
		// The row line is the subject or the first line, cut to fit; the
		// body (what that line leaves out) shows only once opened with →,
		// as the subject line (when set) and the full content.
		text, hasBody := rowLine(x, avail)
		bodyOpen := hasBody && m.bodyOpen[x.Seq]
		if bodyOpen {
			text = x.Content
			if x.Subject != "" {
				text = x.Subject + "\n" + x.Content
			}
		}
		// The tree prefix (indent plus expand/collapse marker) is applied
		// after wrapping so every wrapped line sits at the row's depth.
		// ▶ means something is hidden (body or replies); ▼ means the body
		// is open, or the replies are shown with nothing left hidden.
		prefix := strings.Repeat("  ", r.depth)
		switch {
		case r.hidden > 0 || (hasBody && !bodyOpen):
			prefix += rowDim.Render(markSel + " ")
		case r.open || bodyOpen:
			prefix += rowDim.Render(markOpen + " ")
		default:
			prefix += "  "
		}
		pw := lipgloss.Width(prefix)
		line := m.header(x, ch, label, stampStyle, avail) + "\n" + text
		if r.hidden > 0 {
			summary := strconv.Itoa(r.hidden) + " replies"
			if r.hidden == 1 {
				summary = "1 reply"
			}
			if r.depth == 0 {
				summary += " \u00b7 " + clock(r.latest)
			}
			line += "\n" + rowDim.Render(summary)
		}
		line = lipgloss.NewStyle().Width(w - pw).Render(line)
		line = prefix + strings.ReplaceAll(line, "\n", "\n"+strings.Repeat(" ", pw))
		if selected {
			line = th.Highlight(th.Sel, line, w)
		}
		if i == m.cursor {
			m.cursorLine = lineNum
			cursorDepth = r.depth
		}
		b.WriteString(line + "\n")
		lineNum += strings.Count(line, "\n") + 1
		if cursorDepth >= 0 {
			m.cursorEnd = lineNum
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m Model) renderReplyBanner() string {
	th := m.theme
	r := m.replyTo
	quote := strings.SplitN(r.Content, "\n", 2)[0]
	if rs := []rune(quote); len(rs) > 40 {
		quote = string(rs[:40]) + "…"
	}
	return th.Style(th.Dim).Render("replying to ") + th.Style(th.Agent).Render(senderName(*r)) + th.Style(th.Dim).Render(" #"+itoa(r.Seq)+" “"+quote+"”  esc cancel")
}

func (m Model) renderCompose() string {
	th := m.theme
	if m.mode == modeConfirmChannel {
		return m.confirmChannelLine()
	}
	if m.prompt.active {
		return th.Style(th.Agent).Render(m.prompt.label+" › ") + m.prompt.input.View()
	}
	ch := m.selected()
	label := ""
	hint := ""
	switch {
	case ch == nil:
	case isTagPane(ch.Name):
		label = m.tagChips(tagPaneSet(ch.Name))
	default:
		mark := iconChat
		switch ch.Kind {
		case bus.KindTasks:
			mark = iconTasks
		case bus.KindMemory:
			mark = iconMem
			hint = th.Style(th.Dim).Render("  ⏎ new memory")
		}
		label = m.chanStyle(*ch).Render(mark + ch.Name)
	}
	prompt := label + th.Style(th.Dim).Render(" › ")
	if m.mode != modeInsert {
		prompt = label + th.Style(th.Dim).Render(" "+markSel+" ")
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, prompt, m.compose.View(), hint)
}

func (m Model) renderStatusBar() string {
	th := m.theme
	left := th.Style(th.Dim).Render("agentbus2 v"+version.Version) + "  "
	left += "as " + th.Style(th.User).Render(m.c.identity)
	if m.statusOK {
		// The administrator's view: the embedding pass and the notices
		// (capacity, embedding) the server reports (Decision 73).
		embed := th.Style(th.Health).Render("ok")
		switch {
		case m.status.EmbeddingError != "":
			embed = th.Style(th.Warn).Render("failing")
		case m.search.semanticDown:
			embed = th.Style(th.Warn).Render("unreachable")
		}
		left += fmt.Sprintf("  embed %s  backlog %d", embed, m.status.EmbeddingBacklog)
		for _, n := range m.status.Notices {
			left += "  " + th.Style(th.Warn).Render("! "+n.Kind+": "+n.Message)
		}
	}
	if m.statusErr != nil {
		left += "  " + th.Style(th.Error).Render("status: "+errText(m.statusErr))
	}
	help := m.hints("?", "help", "/", "search", "h", "health", "q", "quit")
	if m.mode == modeInsert {
		help = m.hints("esc", "commands", "tab", "next pane", "alt+enter", "newline")
	}
	// The status bar must stay exactly one row: a narrow terminal or a long
	// left side can make help too wide to fit; MaxWidth truncates it instead
	// of letting Width's word-wrap add a row that MaxHeight would then have
	// to clip from somewhere else on screen.
	avail := m.width - lipgloss.Width(left) - 1
	if avail <= 0 {
		return lipgloss.NewStyle().MaxWidth(m.width).Render(left)
	}
	help = lipgloss.NewStyle().MaxWidth(avail).Render(help)
	gap := max(m.width-lipgloss.Width(left)-lipgloss.Width(help), 1)
	return left + strings.Repeat(" ", gap) + help
}

// hints renders key/description pairs as a help line: keys in the agent
// color, descriptions dim, so a key stands apart from the words around it.
func (m Model) hints(pairs ...string) string {
	key, dim := m.theme.Style(m.theme.Agent), m.theme.Style(m.theme.Dim)
	var b strings.Builder
	for i := 0; i+1 < len(pairs); i += 2 {
		if i > 0 {
			b.WriteString("  ")
		}
		b.WriteString(key.Render(pairs[i]) + " " + dim.Render(pairs[i+1]))
	}
	return b.String()
}

// overlaySize returns the box width and height overlay renders at, so a
// view (e.g. viewSearch) can size and window its own content to match.
func (m Model) overlaySize() (w, h int) {
	return min(max(m.width-8, 40), 100), max(m.height-4, 10)
}

// overlay renders a titled, bordered box centered on the screen; the border
// color names the overlay (cyan search, green health).
func (m Model) overlay(title string, border color.Color, body, footer string) string {
	w, h := m.overlaySize()
	if m.toast != "" {
		footer = m.toastLine() + "\n" + footer
		h--
	}
	inner := lipgloss.JoinVertical(lipgloss.Left,
		m.theme.Style(border).Bold(true).Render(title),
		lipgloss.NewStyle().Width(w-4).Height(h-4).MaxHeight(h-4).Render(body),
		m.theme.Style(m.theme.Dim).Render(footer),
	)
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(border).Padding(0, 1).Width(w).Render(inner)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
}
