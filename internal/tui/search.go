// Ported from v1 906dec8 internal/tui/search.go (over Messaging.Search; jumpTo selects channels only, ADR 0006).

package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/ericfitz/agentbus2/internal/bus"
)

// searchCount is how many hits one search asks for (ported from v1
// internal/tui/search.go).
const searchCount = 20

type searchState struct {
	input        textinput.Model
	mode         string
	query        string // the query the current hits answer; typing past it makes enter re-run
	hits         []bus.SearchHit
	cursor       int
	ran          bool
	textOnly     bool
	semanticDown bool
	err          error
}

func (m *Model) openSearch() tea.Cmd {
	in := textinput.New()
	in.Prompt = "> "
	in.CharLimit = 512
	in.SetStyles(promptStyles()) // a static cursor: see promptStyles
	m.search = searchState{input: in, mode: "text", semanticDown: m.search.semanticDown}
	m.mode = modeSearch
	return m.search.input.Focus() // focus the copy the model keeps, not the local
}

func (m *Model) updateSearch(msg tea.Msg) tea.Cmd {
	switch keyString(msg) {
	case "esc":
		m.mode = modeNormal
		return nil
	case "tab":
		switch m.search.mode {
		case "text":
			m.search.mode = "semantic"
		case "semantic":
			m.search.mode = "both"
		default:
			m.search.mode = "text"
		}
		m.search.ran = false // the last results answered the old mode; enter must re-run, not open a hit
		return nil
	case "up":
		m.search.cursor = max(m.search.cursor-1, 0)
		return nil
	case "down":
		m.search.cursor = min(m.search.cursor+1, max(len(m.search.hits)-1, 0))
		return nil
	case "enter":
		if m.search.ran && len(m.search.hits) > 0 && strings.TrimSpace(m.search.input.Value()) == m.search.query {
			hit := m.search.hits[m.search.cursor]
			m.mode = modeNormal
			return m.jumpTo(hit.Message)
		}
		return m.runSearch()
	}
	var cmd tea.Cmd
	m.search.input, cmd = m.search.input.Update(msg)
	return cmd
}

// runSearch asks the core for the query in the overlay's mode (text,
// semantic or both); the core decides what the mode means.
func (m *Model) runSearch() tea.Cmd {
	q := strings.TrimSpace(m.search.input.Value())
	if q == "" {
		return nil
	}
	m.search.query = q
	c := m.c
	in := bus.SearchInput{Query: q, Mode: m.search.mode, Count: searchCount}
	return func() tea.Msg {
		res, err := c.b.Search(c.ctx, in)
		return searchMsg{query: q, res: res, err: err}
	}
}

// jumpTo selects the hit's channel (loading its latest page on a first
// visit through selectChannel's own returned cmd), puts the normal-mode
// cursor on the hit, and loads the page of history before it too, so no gap
// is left between the hit and whatever was already loaded (the historyMsg
// handler keeps the cursor on the hit across both loads).
func (m *Model) jumpTo(hit bus.Message) tea.Cmd {
	var cmds []tea.Cmd
	for i, c := range m.channels {
		if c.Name == hit.Channel {
			cmds = append(cmds, m.selectChannel(i))
			break
		}
	}
	if len(cmds) == 0 {
		return m.showToast("channel " + hit.Channel + " not in the rail yet")
	}
	m.addMessages(hit.Channel, []bus.Message{hit})
	m.markSeen(hit.Channel)
	m.placeCursor(hit.Seq)
	before := hit.Seq
	return tea.Batch(append(cmds, m.loadHistory(hit.Channel, &before))...)
}

func (m Model) viewSearch() string {
	th := m.theme
	dim := th.Style(th.Dim)
	w, h := m.overlaySize()
	rowWidth := w - 4
	var b strings.Builder
	b.WriteString(m.search.input.View() + "\n\n")

	hits := m.search.hits
	// visible reserves the input line, the blank line after it, and the
	// summary and (possible) text-only badge lines below the list, so the hit
	// rows plus that fixed chrome never exceed the overlay's body height.
	visible := max(h-4-5, 1)
	start, end := window(m.search.cursor, len(hits), visible)
	for i := start; i < end; i++ {
		hit := hits[i]
		mark := "  "
		switch m.kind(hit.Channel) {
		case bus.KindTasks:
			mark = th.Style(th.Tasks).Render(iconTasks)
		case bus.KindMemory:
			mark = th.Style(th.Mem).Render(iconMem)
		}
		rev := ""
		if hit.Revision != nil {
			rev = " r" + itoa(*hit.Revision)
		}
		first := hit.Subject
		if first == "" {
			first = strings.SplitN(hit.Content, "\n", 2)[0]
		}
		line := fmt.Sprintf("%s%s #%d%s %s %s  %s", mark, hit.Channel, hit.Seq, rev, th.Style(th.Agent).Render(senderName(hit.Message)), dim.Render(clock(hit.CreatedAt)), first)
		if i == m.search.cursor {
			line = th.Style(th.Agent).Render(markSel+" ") + line
		} else {
			line = "  " + line
		}
		b.WriteString(lipgloss.NewStyle().MaxWidth(rowWidth).Render(line) + "\n")
	}
	if m.search.ran {
		var summary string
		switch {
		case m.search.err != nil:
			summary = th.Style(th.Error).Render(iconError + errText(m.search.err))
		case len(hits) > visible:
			summary = fmt.Sprintf("showing %d-%d of %d results", start+1, end, len(hits))
		default:
			summary = fmt.Sprintf("%d results", len(hits))
		}
		b.WriteString("\n" + dim.Render(summary))
	}
	if m.search.textOnly {
		b.WriteString("\n" + th.Style(th.Warn).Render("text only — embedding endpoint unreachable"))
	}
	title := "search  " + dim.Render("mode "+m.search.mode+" · tab to switch")
	return m.overlay(title, th.Agent, b.String(), m.hints("enter", "search / open", "tab", "mode", "↑↓", "move", "esc", "close"))
}
