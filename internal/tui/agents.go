package tui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/charmbracelet/x/ansi"

	"github.com/ericfitz/agentmesh/internal/bus"
)

// The agents board (issue #10): one line per agent with its self-reported
// status, shown whenever the sessions heading is selected (ADR 0014
// decision 20), and one agent's status history in its place. The data is the
// status round's (AgentStatuses, every 5 seconds); the history is fetched
// only while it is open.

// agentBoard is the board's state: focus, the selected agent's identity (not
// a row index: the rows re-sort every status round, like sessSel) and, while
// one agent's history is shown, its identity and rows.
type agentBoard struct {
	focus bool   // the board takes the keys; otherwise the rail does
	sel   string // selected identity; "" or one that left selects the first row
	sort  boardSort
	// expanded holds the identities whose full text and problem show below
	// their row; kept until the TUI quits.
	expanded map[string]bool
	hist     string // identity whose history is shown; "" on the board
	// histOff is how many of the newest history rows are scrolled out of
	// view: 0 shows the newest, the open default.
	histOff int
	rows    []bus.AgentStatus
	err     error
}

// boardRow is one line of the board. st is nil for a live agent that never
// set a status.
type boardRow struct {
	identity string
	live     bool
	tag      bus.StatusTag
	st       *bus.AgentStatus
}

// statusIcon is the icon of a status tag: idle reuses the idle icon, a tag
// this build does not know draws the agent icon (open note: agent-status 1).
func statusIcon(t bus.StatusTag) string {
	switch t {
	case bus.StatusIdle:
		return iconIdle
	case bus.StatusPlanning:
		return iconStatusPlanning
	case bus.StatusImplementing:
		return iconStatusImplementing
	case bus.StatusReviewing:
		return iconStatusReviewing
	case bus.StatusWaiting:
		return iconStatusWaiting
	case bus.StatusBlocked:
		return iconStatusBlocked
	case bus.StatusNeedsHuman:
		return iconStatusNeedsHuman
	case bus.StatusOffline:
		return iconStatusOffline
	}
	return iconAgent
}

// needsAttention reports a live tag that asks for a human.
func needsAttention(t bus.StatusTag) bool {
	return t == bus.StatusNeedsHuman || t == bus.StatusBlocked
}

// boardSort is the board's sort mode; `o` cycles it (ADR 0014 decision 19).
type boardSort int

const (
	sortTime boardSort = iota // newest set_at first, never-set last
	sortName                  // by identity
	sortTag                   // by the displayed tag
)

func (s boardSort) String() string {
	switch s {
	case sortName:
		return "name"
	case sortTag:
		return "tag"
	}
	return "time"
}

// next is the mode after s in the cycle time, name, tag.
func (s boardSort) next() boardSort { return (s + 1) % 3 }

// displayTag is the tag a row shows: offline for an offline agent, "-" for a
// live agent that never set a status.
func (r boardRow) displayTag() string {
	switch {
	case r.st == nil:
		return "-"
	case !r.live:
		return string(bus.StatusOffline)
	}
	return string(r.tag)
}

// sortBoardRows orders rows by mode; every mode breaks ties by identity.
func sortBoardRows(rows []boardRow, mode boardSort) {
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		switch mode {
		case sortTime:
			if (a.st == nil) != (b.st == nil) {
				return a.st != nil
			}
			if a.st != nil && a.st.SetAt != b.st.SetAt {
				return a.st.SetAt > b.st.SetAt
			}
		case sortTag:
			if ta, tb := a.displayTag(), b.displayTag(); ta != tb {
				return ta < tb
			}
		}
		return a.identity < b.identity
	})
}

// boardRows lists the board: the entries of the last status round plus each
// live agent identity without one, in the board's sort order.
func (m Model) boardRows() []boardRow {
	var rows []boardRow
	have := map[string]bool{}
	for i := range m.agents {
		e := &m.agents[i]
		have[e.Identity] = true
		rows = append(rows, boardRow{identity: e.Identity, live: e.Live, tag: e.Status.Tag, st: &e.Status})
	}
	for _, id := range m.identities {
		if id.Kind == bus.IdentityAgent && !have[id.Name] && len(id.Sessions) > 0 {
			rows = append(rows, boardRow{identity: id.Name, live: true})
		}
	}
	sortBoardRows(rows, m.board.sort)
	return rows
}

// boardSel is the index of the selected row: the row of board.sel, else the
// first.
func (m Model) boardSel() int {
	for i, r := range m.boardRows() {
		if r.identity == m.board.sel {
			return i
		}
	}
	return 0
}

// attention counts the live agents whose tag is needs-human or blocked. An
// agent that went offline reads offline and is not counted (open note:
// agent-status 7).
func (m Model) attention() int {
	n := 0
	for _, e := range m.agents {
		if e.Live && needsAttention(e.Status.Tag) {
			n++
		}
	}
	return n
}

// agentHistoryCmd fetches one agent's status history.
func (m *Model) agentHistoryCmd(identity string) tea.Cmd {
	c := m.c
	return func() tea.Msg {
		rows, err := c.b.AgentStatusHistory(c.ctx, identity)
		return agentHistoryMsg{identity: identity, rows: rows, err: err}
	}
}

// boardShown reports whether the board is the main pane: the rail selection
// is the sessions heading, collapsed or not (ADR 0014 decision 20).
func (m Model) boardShown() bool {
	return m.headSel == secSessions && m.sessSel == ""
}

// selectSessionsHeading selects the sessions heading, which shows the board.
// It expands and collapses nothing.
func (m *Model) selectSessionsHeading() {
	m.leaveSession()
	m.cursor = -1
	m.headSel = secSessions
}

// syncBoard drops the history and the focus once the board is no longer
// shown, so any rail selection change closes them.
func (m *Model) syncBoard() {
	if !m.boardShown() {
		m.board.focus = false
		m.board.hist, m.board.rows, m.board.err, m.board.histOff = "", nil, nil, 0
	}
}

// onAgentHistory stores a history answer for the identity still shown.
func (m *Model) onAgentHistory(msg agentHistoryMsg) {
	if !m.boardShown() || m.board.hist != msg.identity {
		return // stale: the history was closed or another one opened
	}
	if m.board.histOff > 0 && msg.err == nil {
		m.board.histOff += max(len(msg.rows)-len(m.board.rows), 0) // stay on the same rows
	}
	m.board.rows, m.board.err = msg.rows, msg.err
}

// histRoom is the number of history rows the pane shows: its height less the
// title line.
func (m Model) histRoom() int { return max(m.stream.Height()-1, 1) }

// histMaxOff is the largest histOff: the oldest row at the top of the pane.
func (m Model) histMaxOff() int { return max(len(m.board.rows)-m.histRoom(), 0) }

// updateBoard handles a key while the board has focus; every key it does not
// name is ignored.
func (m *Model) updateBoard(msg tea.Msg) tea.Cmd {
	switch keyString(msg) {
	case "q":
		return tea.Quit
	case "?":
		return m.openHelp()
	case "h":
		return m.openHealth()
	case "tab", "shift+tab":
		m.board.focus = false // an open history stays
	case "esc":
		if m.board.hist != "" {
			m.board.hist, m.board.rows, m.board.err, m.board.histOff = "", nil, nil, 0
			return nil
		}
		m.board.focus = false
	case "up", "down":
		rows := m.boardRows()
		if m.board.hist != "" || len(rows) == 0 {
			return nil
		}
		i := m.boardSel()
		if keyString(msg) == "up" {
			i = max(i-1, 0)
		} else {
			i = min(i+1, len(rows)-1)
		}
		m.board.sel = rows[i].identity
	case "pgup", "pgdown", "g", "G":
		if m.board.hist == "" {
			return nil
		}
		switch keyString(msg) {
		case "pgup":
			m.board.histOff = min(m.board.histOff+m.histRoom(), m.histMaxOff())
		case "pgdown":
			m.board.histOff = max(m.board.histOff-m.histRoom(), 0)
		case "g":
			m.board.histOff = m.histMaxOff()
		default:
			m.board.histOff = 0
		}
	case "o":
		if m.board.hist != "" {
			return nil
		}
		m.board.sort = m.board.sort.next() // sel is an identity: it follows its agent
	case "right", "left":
		if m.board.hist != "" || len(m.boardRows()) == 0 {
			return nil
		}
		if m.board.expanded == nil {
			m.board.expanded = map[string]bool{}
		}
		id := m.boardRows()[m.boardSel()].identity
		if keyString(msg) == "right" {
			m.board.expanded[id] = true
		} else {
			delete(m.board.expanded, id)
		}
	case "enter":
		rows := m.boardRows()
		if m.board.hist != "" || len(rows) == 0 {
			return nil
		}
		m.board.hist, m.board.rows, m.board.err, m.board.histOff = rows[m.boardSel()].identity, nil, nil, 0
		return m.agentHistoryCmd(m.board.hist)
	}
	return nil
}

// renderBoard is the main pane while the board is shown.
func (m Model) renderBoard() string {
	w, h := m.stream.Width(), m.stream.Height()
	var lines []string
	if m.board.hist != "" {
		lines = strings.Split(m.renderHistoryFor(h), "\n")
	} else {
		lines = m.boardLines(w, h)
	}
	for i := range lines {
		lines[i] = cut(lines[i], w, "…")
	}
	if len(lines) > h {
		lines = lines[:h]
	}
	return lipgloss.NewStyle().Width(w).Height(h).MaxHeight(h).Render(strings.Join(lines, "\n"))
}

// ageText is the age of a status: whole minutes, hours or days, floored.
func ageText(d time.Duration) string {
	if d >= 24*time.Hour {
		return strconv.Itoa(int(d.Hours())/24) + "d"
	}
	return shortDur(d)
}

// boardFixedCols is the width of the columns before the text: the select
// mark, AGENT, TAG, SINCE and their separators.
const boardFixedCols = 1 + 16 + 1 + 17 + 1 + 6 + 1

// boardNote is the NOW column cut to avail cells: text, then " · problem"
// when there is one, each cut with an ellipsis. Text and problem share avail;
// the problem takes at most half of what the separator leaves.
func boardNote(text, problem string, avail int, warn lipgloss.Style, plain bool) string {
	avail = max(avail, 1)
	if problem == "" {
		return cut(text, avail, "…")
	}
	const sep = " · "
	rest := avail - lipgloss.Width(sep)
	if rest < 2 || lipgloss.Width(text)+lipgloss.Width(problem) <= rest {
		if rest < 2 {
			return cut(text, avail, "…")
		}
		return noteJoin(text, problem, sep, warn, plain)
	}
	pw := min(lipgloss.Width(problem), max(rest/2, 1))
	tw := rest - pw
	if tw > lipgloss.Width(text) {
		tw = lipgloss.Width(text)
		pw = rest - tw
	}
	return noteJoin(cut(text, tw, "…"), cut(problem, pw, "…"), sep, warn, plain)
}

func noteJoin(text, problem, sep string, warn lipgloss.Style, plain bool) string {
	if plain {
		return text + sep + problem
	}
	return text + warn.Render(sep+problem)
}

// boardLines is the board for a pane h lines high: the title, the column
// heads and one line per row, with an expanded row's full text and problem
// wrapped below it. When the lines do not fit, they scroll to keep the
// selected row visible and the title and heads stay.
func (m Model) boardLines(w, h int) []string {
	th := m.theme
	dim, warn := th.Style(th.Dim), th.Style(th.Warn)
	rows := m.boardRows()
	lines := []string{
		dim.Render(fmt.Sprintf("agents (%d) · sort: %s · o sort · →/← expand · enter history", len(rows), m.board.sort)),
		dim.Render(fmt.Sprintf("%-16s %-17s %-6s %s", "AGENT", "TAG", "SINCE", "NOW")),
	}
	nowMs := time.Now().UnixMilli()
	sel := m.boardSel()
	selLine := 0 // index of the selected row's line within body
	var body []string
	pad := func(s string, n int) string {
		return s + strings.Repeat(" ", max(n-lipgloss.Width(s), 0))
	}
	for i, r := range rows {
		since, text := "-", "-"
		var problem string
		if r.st != nil {
			since = ageText(time.Duration(nowMs-r.st.SetAt) * time.Millisecond)
			text, problem = r.st.Text, r.st.Problem
			if !r.live {
				text = "last: " + text
			}
		}
		icon := statusIcon(r.tag)
		if r.st == nil {
			icon = iconAgent
		}
		note := boardNote(text, problem, w-boardFixedCols, warn, !r.live)
		line := pad(cut(r.identity, 16, "…"), 16) + " " + pad(icon+cut(r.displayTag(), 14, "…"), 17) + " " + pad(since, 6) + " " + note
		switch {
		case i == sel:
			line = th.RailSel(true, cut(markSel+line, w, "…"), w)
			selLine = len(body)
		case !r.live:
			line = dim.Render(" " + line)
		default:
			line = " " + line
		}
		body = append(body, line)
		if m.board.expanded[r.identity] && r.st != nil {
			indent := strings.Repeat(" ", 4)
			for _, l := range strings.Split(ansi.Wrap(text, max(w-4, 8), ""), "\n") {
				body = append(body, indent+l)
			}
			if problem != "" {
				for _, l := range strings.Split(ansi.Wrap(problem, max(w-4, 8), ""), "\n") {
					body = append(body, indent+warn.Render(l))
				}
			}
		}
	}
	if len(rows) == 0 {
		body = append(body, dim.Render("no agents"))
	}
	if room := h - len(lines); room >= 1 && len(body) > room {
		lo, hi := window(selLine, len(body), room)
		body = body[lo:hi]
	}
	return append(lines, body...)
}

// renderHistoryFor is the history of the open board for a pane h lines high.
func (m Model) renderHistoryFor(h int) string {
	if m.board.err != nil {
		title := m.theme.Style(m.theme.Dim).Render(m.board.hist + " · history, oldest first · esc back")
		return title + "\n" + m.theme.Style(m.theme.Error).Render(iconError+"history: "+errText(m.board.err))
	}
	return m.renderHistory(m.board.hist, m.board.rows, h, min(m.board.histOff, m.histMaxOff()))
}

// renderHistory is one agent's revisions, oldest first, one line each; when
// they do not fit in h lines the newest are kept, or, with off > 0, the page
// that many rows older.
func (m Model) renderHistory(identity string, rows []bus.AgentStatus, h, off int) string {
	dim, warn := m.theme.Style(m.theme.Dim), m.theme.Style(m.theme.Warn)
	title := dim.Render(identity + " · history, oldest first · esc back")
	if len(rows) == 0 {
		return title + "\n" + dim.Render("no status yet")
	}
	var lines []string
	for _, r := range rows {
		l := stamp(r.SetAt) + " " + statusIcon(r.Tag) + string(r.Tag) + " " + r.Text
		if r.Problem != "" {
			l += warn.Render(" · " + r.Problem)
		}
		lines = append(lines, l+" by "+r.DisplayName)
	}
	if room := h - 1; room >= 1 && len(lines) > room {
		end := len(lines) - min(off, len(lines)-room)
		lines = lines[end-room : end]
	}
	return title + "\n" + strings.Join(lines, "\n")
}

// attentionText is "N need you" for the status bar and the sessions heading.
func attentionText(n int) string { return strconv.Itoa(n) + " need you" }
