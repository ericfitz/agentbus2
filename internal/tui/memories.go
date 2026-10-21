// Stub created by Milestone 8 Task 3 from the call sites in v1 906dec8 model.go, view.go and compose.go; Task 4 replaces this file.
//
// Signatures and the v1 call site each came from:
//
//	type memView struct{ seq, revs, idx }         model.go:121 Model.mem, view.go:797 renderStream
//	(memView) version(x bus.Message) (bus.Message, bool)   view.go:792 renderStream
//	isVersionKey(k string) bool                   model.go:444 updateNormal
//	stepVersion(older bool) tea.Cmd               model.go:603 updateNormal
//	applyRevisions(msg revisionsMsg) tea.Cmd      model.go:353 update

package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/ericfitz/agentbus2/internal/bus"
)

// memView is the version shown for the cursor's memory row: revs are its
// revisions oldest first (Memories.Revisions), idx the one shown. seq is the
// live row's seq, so a live edit or a different cursor row no longer matches
// and the row shows its latest version again. revs is nil while the fetch is
// in flight.
type memView struct {
	seq  int64
	revs []bus.Message
	idx  int
}

// version returns the revision to show in place of row x: none in the stub.
func (v memView) version(bus.Message) (bus.Message, bool) { return bus.Message{}, false }

func isVersionKey(k string) bool { return k == "." || k == "," }

func (m *Model) stepVersion(bool) tea.Cmd { return nil }

func (m *Model) applyRevisions(msg revisionsMsg) tea.Cmd {
	if msg.err != nil {
		return m.showToast("revisions: " + errText(msg.err))
	}
	m.mem = memView{seq: msg.seq, revs: msg.revs}
	return nil
}
