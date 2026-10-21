// Stub created by Milestone 8 Task 3 from the call sites in v1 906dec8 model.go, view.go and compose.go; Task 4 replaces this file.
//
// Signatures and the v1 call site each came from:
//
//	paneMsgs(ch string) []bus.Message         view.go:749 renderStream
//	type row struct{ msg, depth, hidden, open, latest, newest, root }  view.go:769 renderStream
//	rows(ch string) []row                     model.go:237 updateNormal, view.go:769 renderStream
//	cursorRow() (row, bool)                   model.go:476 updateNormal
//	rowAvail(depth int) int                   view.go:787 renderStream
//	openCursor()                              model.go:519 updateNormal
//	closeCursor()                             model.go:528 updateNormal
//	peekReply(ch string, x bus.Message)       model.go:1197 onBatch
//	rootSeq, nextParent, collapseCursor, setExpanded: called only inside v1 thread.go
//
// The stub draws the loaded messages flat: one row per message, depth 0, no
// replies shown or hidden.

package tui

import (
	"github.com/ericfitz/agentbus2/internal/bus"
)

// paneMsgs returns the messages a pane shows: a tag pane's matches, else the
// channel's own.
func (m *Model) paneMsgs(ch string) []bus.Message {
	if isTagPane(ch) {
		return m.tagPaneMsgs(ch)
	}
	return m.msgs[ch]
}

// row is one visible message in the stream's display order.
type row struct {
	msg    bus.Message
	depth  int   // 0 for a thread root, +1 per reply level
	hidden int   // descendants not shown under this row (a summary line follows)
	open   bool  // at least one direct reply is shown under this row
	latest int64 // newest created_at in the thread; set on root rows only
	newest int64 // newest seq in the thread; set on root rows only
	root   int64 // seq of the thread root
}

// rows returns ch's loaded messages as flat rows, oldest first.
func (m *Model) rows(ch string) []row {
	ms := m.paneMsgs(ch)
	out := make([]row, 0, len(ms))
	for _, x := range ms {
		out = append(out, row{msg: x, latest: x.CreatedAt, newest: x.Seq, root: x.Seq})
	}
	return out
}

func nextParent(map[int64]bus.Message, int64) (int64, bool) { return 0, false } //nolint:unused // stub; Task 4 uses it

func (m *Model) rootSeq(_ string, seq int64) int64 { return seq } //nolint:unused // stub; Task 4 uses it

func (m *Model) peekReply(string, bus.Message) {}

// cursorRow returns the stream row under the normal-mode cursor.
func (m *Model) cursorRow() (row, bool) {
	rs := m.rows(m.selName())
	if m.cursor < 0 || m.cursor >= len(rs) {
		return row{}, false
	}
	return rs[m.cursor], true
}

// rowAvail is the width left for a row's text after its tree prefix: two
// columns per depth level plus the two-column marker.
func (m *Model) rowAvail(depth int) int {
	return max(m.stream.Width, 20) - 2*depth - 2
}

func (m *Model) openCursor() {}

func (m *Model) closeCursor() {}

func (m *Model) collapseCursor() {} //nolint:unused // stub; Task 4 uses it

func (m *Model) setExpanded(row, bool) {} //nolint:unused // stub; Task 4 uses it
