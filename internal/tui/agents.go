package tui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/ericfitz/agentbus2/internal/bus"
)

// The agents board (issue #10): one line per agent with its self-reported
// status, opened with enter on the sessions heading, and one agent's status
// history under it. The data is the status round's (AgentStatuses, every
// 5 seconds); the history is fetched only while it is open.

// agentBoard is the board's state: open, the selected agent's identity (not
// a row index: the rows re-sort every status round, like sessSel) and, while
// one agent's history is shown, its identity and rows.
type agentBoard struct {
	open bool
	sel  string // selected identity; "" or one that left selects the first row
	hist string // identity whose history is shown; "" on the board
	rows []bus.AgentStatus
	err  error
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

// boardRows lists the board: the entries of the last status round plus each
// live agent identity without one. Order: live needs-human, live blocked,
// the other live agents (with or without a status) by identity, then the
// offline ones by identity (open note: agent-status 6).
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
	rank := func(r boardRow) int {
		switch {
		case !r.live:
			return 3
		case r.tag == bus.StatusNeedsHuman:
			return 0
		case r.tag == bus.StatusBlocked:
			return 1
		}
		return 2
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if ri, rj := rank(rows[i]), rank(rows[j]); ri != rj {
			return ri < rj
		}
		return rows[i].identity < rows[j].identity
	})
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

// openBoard shows the board with the first row selected.
func (m *Model) openBoard() {
	m.board = agentBoard{open: true}
}

// closeBoard hides the board; the selection stays on the sessions heading.
func (m *Model) closeBoard() {
	m.board = agentBoard{}
	m.headSel = secSessions
}

// onAgentHistory stores a history answer for the identity still shown.
func (m *Model) onAgentHistory(msg agentHistoryMsg) {
	if !m.board.open || m.board.hist != msg.identity {
		return // stale: the history was closed or another one opened
	}
	m.board.rows, m.board.err = msg.rows, msg.err
}

// updateBoard handles a key while the board is open; every key it does not
// name is ignored.
func (m *Model) updateBoard(msg tea.Msg) tea.Cmd {
	switch keyString(msg) {
	case "q":
		return tea.Quit
	case "?":
		return m.openHelp()
	case "h":
		return m.openHealth()
	case "esc":
		if m.board.hist != "" {
			m.board.hist, m.board.rows, m.board.err = "", nil, nil
			return nil
		}
		m.closeBoard()
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
	case "enter":
		rows := m.boardRows()
		if m.board.hist != "" || len(rows) == 0 {
			return nil
		}
		m.board.hist, m.board.rows, m.board.err = rows[m.boardSel()].identity, nil, nil
		return m.agentHistoryCmd(m.board.hist)
	}
	return nil
}

// renderBoard is the main pane while the board is open.
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

// boardLines is the board for a pane h lines high: the title, the column
// heads, one line per row and the offline divider before the first offline
// row. When the rows do not fit, they scroll to keep the selected one visible
// and the title and heads stay.
func (m Model) boardLines(w, h int) []string {
	th := m.theme
	dim, warn := th.Style(th.Dim), th.Style(th.Warn)
	rows := m.boardRows()
	lines := []string{
		dim.Render(fmt.Sprintf("agents (%d) · enter history · esc back", len(rows))),
		dim.Render(fmt.Sprintf("%-16s %-17s %-6s %s", "AGENT", "TAG", "SINCE", "NOW")),
	}
	nowMs := time.Now().UnixMilli()
	divided := false
	sel := m.boardSel()
	selLine := 0 // index of the selected row's line within body
	var body []string
	for i, r := range rows {
		if !r.live && !divided {
			divided = true
			body = append(body, dim.Render("── offline ──"))
		}
		tag, since, text := "-", "-", "-"
		var problem string
		if r.st != nil {
			tag = string(r.tag)
			since = shortDur(time.Duration(nowMs-r.st.SetAt) * time.Millisecond)
			text, problem = r.st.Text, r.st.Problem
			if !r.live {
				text = "last: " + text
			}
		}
		icon := statusIcon(r.tag)
		if r.st == nil {
			icon = iconAgent
		}
		note := text
		if problem != "" {
			note += warn.Render(" · " + problem)
		}
		pad := func(s string, n int) string {
			return s + strings.Repeat(" ", max(n-lipgloss.Width(s), 0))
		}
		line := pad(cut(r.identity, 16, "…"), 16) + " " + pad(icon+cut(tag, 14, "…"), 17) + " " + pad(since, 6) + " " + note
		if i == sel {
			line = th.RailSel(true, cut(markSel+line, w, "…"), w)
			selLine = len(body)
		} else {
			line = " " + line
		}
		body = append(body, line)
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
	return m.renderHistory(m.board.hist, m.board.rows, h)
}

// renderHistory is one agent's revisions, oldest first, one line each; when
// they do not fit in h lines the newest are kept.
func (m Model) renderHistory(identity string, rows []bus.AgentStatus, h int) string {
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
		lines = lines[len(lines)-room:]
	}
	return title + "\n" + strings.Join(lines, "\n")
}

// attentionText is "N need you" for the status bar and the sessions heading.
func attentionText(n int) string { return strconv.Itoa(n) + " need you" }
