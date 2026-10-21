// Stub created by Milestone 8 Task 3 from the call sites in v1 906dec8 model.go, view.go and compose.go; Task 5 replaces this file.
//
// Signatures and the v1 call site each came from:
//
//	type taskDraft struct{ ch, id, status, owner, rev }   model.go:116 Model.draft, :1214 onBatch
//	type assignPicker struct{ ch, id, owner, names, sel } model.go:117 Model.assign, tasks.go viewAssign
//	type taskRow struct{ t, hidden, open }                tasks.go taskRows
//	loadTasks(ch string) tea.Cmd              model.go:1144 showSelected, :1203 onBatch
//	loadTask(id int64) tea.Cmd                model.go:304 update (tasksMsg)
//	cursorTask() (bus.TaskSummary, bool)      model.go:277, :320 update
//	taskRows(ch string) []taskRow             model.go:877 paneLen
//	taskIndex(ch string, id int64) int        model.go:291, :938
//	revealTask(ch string, id int64)           model.go:290, :937
//	cycleTaskState() tea.Cmd                  model.go:545 updateNormal
//	saveDraft() tea.Cmd                       model.go:469 updateNormal
//	dropDraft() tea.Cmd                       model.go:1217 onBatch
//	dropStaleDraft() tea.Cmd                  model.go:374 update
//	takeTask() tea.Cmd                        model.go:581 updateNormal
//	unassignTask() tea.Cmd                    model.go:586 updateNormal
//	openAssign() tea.Cmd                      model.go:590 updateNormal
//	updateAssign(msg tea.Msg) tea.Cmd         model.go:369 update
//	viewAssign() string                       view.go:268 View
//	openTask() tea.Cmd                        model.go:517 updateNormal
//	closeTask()                               model.go:526 updateNormal
//	renderTasks(ch string) string             view.go:745 renderStream

package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/ericfitz/agentbus2/internal/bus"
)

// taskDraft is the one unsaved task state change; id 0 means no draft. rev is
// the revision the draft was taken from.
type taskDraft struct { //nolint:unused // stub; Task 5 uses every field
	ch     string
	id     int64
	status string //nolint:unused // stub; Task 5 uses it
	owner  string //nolint:unused // stub; Task 5 uses it
	rev    int64
}

// assignPicker is a's overlay: the identities the cursor task can go to.
type assignPicker struct { //nolint:unused // stub; Task 5 uses it
	ch    string
	id    int64
	owner string
	names []string
	sel   int
}

// taskRow is one visible row of a task tree.
type taskRow struct { //nolint:unused // stub; Task 5 uses every field
	t      bus.TaskSummary //nolint:unused // stub; Task 5 uses it
	hidden int             //nolint:unused // descendants not shown under this row (a summary line follows)
	open   bool            //nolint:unused // at least one direct child is shown
}

func (m *Model) loadTasks(string) tea.Cmd { return nil }

func (m *Model) loadTask(int64) tea.Cmd { return nil }

func (m *Model) cursorTask() (bus.TaskSummary, bool) { return bus.TaskSummary{}, false }

func (m *Model) taskRows(string) []taskRow { return nil }

func (m *Model) taskIndex(string, int64) int { return -1 }

func (m *Model) revealTask(string, int64) {}

func (m *Model) cycleTaskState() tea.Cmd { return nil }

func (m *Model) saveDraft() tea.Cmd { return nil }

func (m *Model) dropDraft() tea.Cmd { return nil }

func (m *Model) dropStaleDraft() tea.Cmd { return nil }

func (m *Model) takeTask() tea.Cmd { return nil }

func (m *Model) unassignTask() tea.Cmd { return nil }

func (m *Model) openAssign() tea.Cmd { return nil }

func (m *Model) updateAssign(tea.Msg) tea.Cmd { return nil }

func (m Model) viewAssign() string { return "" }

func (m *Model) openTask() tea.Cmd { return nil }

func (m *Model) closeTask() {}

func (m *Model) renderTasks(string) string { return "" }
