// Stub created by Milestone 8 Task 3 from the call sites in v1 906dec8 model.go, view.go and compose.go; Task 6 replaces this file.
//
// Signatures and the v1 call site each came from:
//
//	type searchState struct{ input, mode, query, hits, cursor, ran, textOnly, semanticDown, err }   model.go:120 Model.search, :333-349 update (searchMsg)
//	openSearch() tea.Cmd                      model.go:601 updateNormal
//	updateSearch(msg tea.Msg) tea.Cmd         model.go:361 update
//	viewSearch() string                       view.go:262 View
//	runSearch, jumpTo: called only inside v1 search.go

package tui

import (
	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/ericfitz/agentbus2/internal/bus"
)

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
	// Static cursor: see the compose textarea's Cursor.SetMode note in model.go.
	in.Cursor.SetMode(cursor.CursorStatic)
	m.search = searchState{input: in, mode: "text", semanticDown: m.search.semanticDown}
	m.mode = modeSearch
	return m.search.input.Focus() // focus the copy the model keeps, not the local
}

func (m *Model) updateSearch(msg tea.Msg) tea.Cmd {
	switch keyString(msg) {
	case "esc":
		m.mode = modeNormal
		return nil
	case "enter":
		return m.runSearch()
	}
	return nil
}

func (m *Model) runSearch() tea.Cmd { return nil }

func (m *Model) jumpTo(bus.Message) tea.Cmd { return nil } //nolint:unused // stub; Task 6 uses it

func (m Model) viewSearch() string {
	return m.overlay("search", m.theme.Agent, "", m.hints("esc", "close"))
}
