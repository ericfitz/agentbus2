package tui

import (
	"fmt"
	"testing"

	"github.com/ericfitz/agentbus2/internal/bus"
)

// TestBatchReloadsOnlyTheSelectedTaskList: a receive batch that touches
// task lists reloads the one on screen; the others load when selected.
func TestBatchReloadsOnlyTheSelectedTaskList(t *testing.T) {
	f := newFixture(t)
	f.key("esc")
	f.taskTree(t)
	if _, err := f.sam.CreateChannel(taskCtx, bus.CreateChannelInput{Name: "zeta", Kind: bus.KindTasks}); err != nil {
		t.Fatal(err)
	}
	for _, ch := range []string{"todo", "zeta"} {
		if _, err := f.sam.Create(taskCtx, bus.TaskCreateInput{Channel: ch, Subject: "new in " + ch}); err != nil {
			t.Fatal(err)
		}
	}
	before := len(f.m.tasks["todo"])
	f.send(batchMsg{res: bus.ReceiveResult{Messages: []bus.Message{
		{Channel: "todo", Seq: 1000}, {Channel: "zeta", Seq: 1001},
	}}})
	if got := len(f.m.tasks["todo"]); got != before+1 {
		t.Fatalf("selected list has %d tasks, want %d", got, before+1)
	}
	if got := len(f.m.tasks["zeta"]); got != 0 {
		t.Fatalf("unselected list was loaded: %d tasks", got)
	}
	f.run(f.m.statusCmd())
	f.key("shift+tab") // back to the rail
	f.selectTaskChannel(t, "zeta")
	if got := len(f.m.tasks["zeta"]); got != 1 {
		t.Fatalf("selecting the list loaded %d tasks, want 1", got)
	}
}

// TestTasksReloadScrollsToBottomWhileFollowing: a growing task tree follows
// to its newest row, as a growing message stream does.
func TestTasksReloadScrollsToBottomWhileFollowing(t *testing.T) {
	f := newFixture(t)
	f.key("esc")
	f.taskTree(t)
	for i := range 60 {
		if _, err := f.sam.Create(taskCtx, bus.TaskCreateInput{Channel: "todo", Subject: fmt.Sprintf("t%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	f.m.cursor, f.m.follow = -1, true
	f.run(f.m.loadTasks("todo"))
	if !f.m.stream.AtBottom() {
		t.Fatalf("following tree not at bottom: offset %d of %d lines", f.m.stream.YOffset(), f.m.stream.TotalLineCount())
	}
}

// TestOtherTaskListReloadKeepsScroll: reloading a list that is not shown
// does not move the shown pane.
func TestOtherTaskListReloadKeepsScroll(t *testing.T) {
	f := newFixture(t)
	f.key("esc")
	f.taskTree(t)
	for i := range 60 {
		if _, err := f.sam.Create(taskCtx, bus.TaskCreateInput{Channel: "todo", Subject: fmt.Sprintf("t%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.sam.CreateChannel(taskCtx, bus.CreateChannelInput{Name: "other", Kind: bus.KindTasks}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.sam.Create(taskCtx, bus.TaskCreateInput{Channel: "other", Subject: "o"}); err != nil {
		t.Fatal(err)
	}
	f.m.cursor, f.m.follow = -1, true
	f.run(f.m.loadTasks("todo"))
	f.m.stream.GotoTop()
	f.run(f.m.loadTasks("other"))
	if f.m.stream.YOffset() != 0 {
		t.Fatalf("reloading another list scrolled the pane to %d", f.m.stream.YOffset())
	}
}

// TestNewerRevisionDropsDraft: a revision of the drafted task newer than the
// one the draft was taken from discards it; one at or below it does not.
func TestNewerRevisionDropsDraft(t *testing.T) {
	f := newFixture(t)
	a := f.openTasks(t, "a")[0]
	f.key(" ")
	if f.m.draft.id != a.ID {
		t.Fatalf("draft = %+v", f.m.draft)
	}
	rev, id := f.m.draft.rev, a.ID
	f.send(batchMsg{res: bus.ReceiveResult{Messages: []bus.Message{
		{Channel: "todo", Seq: 900, MemoryID: &id, Revision: &rev},
	}}})
	if f.m.draft.id != a.ID {
		t.Fatalf("the revision the draft was taken from keeps it: %+v", f.m.draft)
	}
	newer := rev + 1
	f.send(batchMsg{res: bus.ReceiveResult{Messages: []bus.Message{
		{Channel: "todo", Seq: 901, MemoryID: &id, Revision: &newer},
	}}})
	if f.m.draft.id != 0 || f.m.toast != draftDiscardedToast {
		t.Fatalf("a newer revision drops the draft: draft=%+v toast=%q", f.m.draft, f.m.toast)
	}
}

// TestSearchJumpIntoUnloadedTaskList: a jump to a task whose list was never
// loaded records the pending cursor; the tasksMsg that follows lands on it,
// revealing it under a collapsed parent, and consumes the pending target.
func TestSearchJumpIntoUnloadedTaskList(t *testing.T) {
	f := newFixture(t)
	f.key("esc")
	_, a1, _ := f.taskTree(t)
	f.m.taskExpanded = map[int64]bool{}
	f.m.tasks["todo"] = nil
	f.m.pendingTaskCursorCh, f.m.pendingTaskCursorID = "todo", a1.ID
	f.run(f.m.loadTasks("todo"))
	if got, ok := f.m.cursorTask(); !ok || got.ID != a1.ID {
		t.Fatalf("cursor = %+v, want the pending a1", got)
	}
	if f.m.pendingTaskCursorCh != "" || f.m.pendingTaskCursorID != 0 {
		t.Fatalf("pending target not consumed: %q %d", f.m.pendingTaskCursorCh, f.m.pendingTaskCursorID)
	}
}
