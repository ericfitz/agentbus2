package tui

import (
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/ericfitz/agentbus2/internal/bus"
)

// openTasks creates tasks/work with unassigned tasks of the given subjects,
// selects it and focuses the stream (cursor on the last task).
func (f *fixture) openTasks(t *testing.T, subjects ...string) []bus.Task {
	t.Helper()
	f.key("esc")
	if _, err := f.sam.CreateChannel(taskCtx, bus.CreateChannelInput{Name: "todo", Kind: bus.KindTasks}); err != nil {
		t.Fatal(err)
	}
	var out []bus.Task
	for _, s := range subjects {
		tk, err := f.sam.Create(taskCtx, bus.TaskCreateInput{Channel: "todo", Subject: s})
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, tk)
	}
	f.run(f.m.statusCmd())
	f.selectTaskChannel(t, "todo")
	f.key("tab")
	return out
}

// task reads id back from the bus as the TUI.
func (f *fixture) task(t *testing.T, id int64) bus.Task {
	t.Helper()
	tk, err := f.c.b.GetTask(taskCtx, id)
	if err != nil {
		t.Fatal(err)
	}
	return tk
}

// TestSpaceCyclesADraftAndAssignsAnUnassignedTask (ADR 0011 decisions 2,
// 6): space drafts pending → in progress → completed → pending, taking the
// task while off pending; the row shows the draft in the unsaved color
// with a hint toast; nothing reaches the bus; a full cycle clears it.
func TestSpaceCyclesADraftAndAssignsAnUnassignedTask(t *testing.T) {
	f := newFixture(t)
	a := f.openTasks(t, "a")[0]
	unsaved, _, _ := strings.Cut(f.m.theme.Style(f.m.theme.Unsaved).Render("\x00"), "\x00")
	f.key(" ")
	if d := f.m.draft; d.ch != "todo" || d.id != a.ID || d.status != "in_progress" || d.owner != f.c.identity {
		t.Fatalf("draft after one space = %+v", d)
	}
	if f.m.toast != draftHintToast || !f.m.toastHint {
		t.Fatalf("toast = %q hint=%v", f.m.toast, f.m.toastHint)
	}
	raw := f.m.renderStream()
	if !strings.Contains(raw, unsaved+taskInProgress) || !strings.Contains(ansi.Strip(raw), f.c.identity) {
		t.Fatalf("drafted row must show in progress with the TUI's name in the unsaved color: %q", raw)
	}
	if got := f.task(t, a.ID); got.Status != "pending" || got.Owner != "" {
		t.Fatalf("a draft must not touch the bus: %+v", got)
	}
	f.key(" ")
	if d := f.m.draft; d.status != "completed" || d.owner != f.c.identity {
		t.Fatalf("draft after two = %+v", d)
	}
	if raw = f.m.renderStream(); !strings.Contains(raw, unsaved+taskCompleted) {
		t.Fatalf("drafted completed row is unsaved-colored, not dim: %q", raw)
	}
	f.key(" ")
	if f.m.draft.id != 0 {
		t.Fatalf("cycling back to the saved state clears the draft: %+v", f.m.draft)
	}
	if raw = f.m.renderStream(); strings.Contains(raw, unsaved+taskPending) {
		t.Fatalf("color returns to normal: %q", raw)
	}
}

// TestEnterSavesTheDraftAsAClaimThenAnUpdate: into in progress is a claim
// (owner and status), the rest a task_update carrying status and owner;
// the tree refreshes after each save and reopening your task keeps it yours.
func TestEnterSavesTheDraftAsAClaimThenAnUpdate(t *testing.T) {
	f := newFixture(t)
	a := f.openTasks(t, "a")[0]
	f.key(" ")
	f.key("enter")
	if f.m.draft.id != 0 || f.m.toast != "" {
		t.Fatalf("enter clears the draft quietly: draft=%+v toast=%q", f.m.draft, f.m.toast)
	}
	if got := f.task(t, a.ID); got.Status != "in_progress" || got.Owner != f.c.identity {
		t.Fatalf("enter must claim: %+v", got)
	}
	if got, ok := f.m.cursorTask(); !ok || got.Status != "in_progress" || got.Owner != f.c.identity {
		t.Fatalf("the tree refreshes after a save: %+v", got)
	}
	f.key(" ") // completed
	f.key("enter")
	if got := f.task(t, a.ID); got.Status != "completed" || got.Owner != f.c.identity {
		t.Fatalf("complete: %+v", got)
	}
	f.key(" ") // back to pending, still yours
	if d := f.m.draft; d.status != "pending" || d.owner != f.c.identity {
		t.Fatalf("reopening a task you own keeps the assignment in the draft: %+v", d)
	}
	f.key("enter")
	if got := f.task(t, a.ID); got.Status != "pending" || got.Owner != f.c.identity {
		t.Fatalf("reopen keeps it yours: %+v", got)
	}
	f.key("enter")
	if f.m.toast != "" {
		t.Fatalf("enter with no draft does nothing: %q", f.m.toast)
	}
}

// TestBusRefusalKeepsTheDraftForEsc: b is blocked, so claiming it is
// refused; the bus's message shows in the error style (hint style does not
// stick), the draft stays, and esc cancels only the draft.
func TestBusRefusalKeepsTheDraftForEsc(t *testing.T) {
	f := newFixture(t)
	f.key("esc")
	_, _, b := f.taskTree(t) // cursor on b, blocked by a1
	f.key(" ")
	if f.m.toast != draftHintToast || !f.m.toastHint {
		t.Fatalf("hint first: %q", f.m.toast)
	}
	f.key("enter")
	if !strings.Contains(f.m.toast, "blocked") || f.m.toastHint {
		t.Fatalf("refusal toast = %q hint=%v, want the bus's message in the error style", f.m.toast, f.m.toastHint)
	}
	if f.m.draft.id != b.ID {
		t.Fatalf("a refused save keeps the draft: %+v", f.m.draft)
	}
	if got := f.task(t, b.ID); got.Status != "pending" || got.Owner != "" {
		t.Fatalf("nothing saved: %+v", got)
	}
	f.key("esc")
	if f.m.draft.id != 0 || f.m.cursor < 0 {
		t.Fatalf("esc cancels the draft and only the draft: draft=%+v cursor=%d", f.m.draft, f.m.cursor)
	}
	if got := f.task(t, b.ID); got.Status != "pending" {
		t.Fatalf("esc writes nothing: %+v", got)
	}
}

// TestDraftIsDiscardedOnCursorMoveFocusChangeAndRevision (decision 2): the
// draft goes, with "change discarded", when the cursor leaves its row, when
// focus leaves the task pane, and when the bus shows a new revision of the
// drafted task; a revision of another task leaves it alone.
func TestDraftIsDiscardedOnCursorMoveFocusChangeAndRevision(t *testing.T) {
	f := newFixture(t)
	ts := f.openTasks(t, "a", "b") // cursor on b
	f.key(" ")
	f.key("up")
	if f.m.draft.id != 0 || f.m.toast != draftDiscardedToast || !f.m.toastHint {
		t.Fatalf("cursor move: draft=%+v toast=%q", f.m.draft, f.m.toast)
	}
	f.key(" ") // draft on a
	f.key("home")
	if f.m.draft.id != 0 || f.m.toast != draftDiscardedToast {
		t.Fatalf("focus change: draft=%+v toast=%q", f.m.draft, f.m.toast)
	}
	f.key("tab") // stream again, cursor on the last task (b)
	f.key(" ")
	drafted, ok := f.m.cursorTask()
	if !ok || f.m.draft.id != drafted.ID {
		t.Fatalf("draft=%+v cursor=%+v", f.m.draft, drafted)
	}
	other := ts[0].ID
	if other == drafted.ID {
		other = ts[1].ID
	}
	desc := "changed elsewhere"
	if _, err := f.sam.Update(taskCtx, bus.TaskPatch{ID: other, Description: &desc}); err != nil {
		t.Fatal(err)
	}
	f.receive(t)
	if f.m.draft.id != drafted.ID {
		t.Fatalf("a revision of another task keeps the draft: %+v", f.m.draft)
	}
	if _, err := f.sam.Update(taskCtx, bus.TaskPatch{ID: drafted.ID, Description: &desc}); err != nil {
		t.Fatal(err)
	}
	f.receive(t)
	if f.m.draft.id != 0 || f.m.toast != draftDiscardedToast {
		t.Fatalf("revision of the drafted task: draft=%+v toast=%q", f.m.draft, f.m.toast)
	}
}

// TestDraftIsDiscardedWhenTheTaskDisappears: the drafted task drops out of
// the next tree; the cursor clamps and the draft is discarded.
func TestDraftIsDiscardedWhenTheTaskDisappears(t *testing.T) {
	f := newFixture(t)
	ts := f.openTasks(t, "a", "b") // cursor on b
	f.key(" ")
	f.send(tasksMsg{ch: "todo", tasks: []bus.TaskSummary{{ID: ts[0].ID, Subject: "a", Status: "pending"}}})
	if f.m.draft.id != 0 || f.m.toast != draftDiscardedToast || f.m.cursor != 0 {
		t.Fatalf("draft=%+v toast=%q cursor=%d", f.m.draft, f.m.toast, f.m.cursor)
	}
}

// TestAgentOwnedTaskIsSentToTheServer: the TUI decides nothing about task
// ownership. On a task another identity owns, t (a claim takes over) and
// space then enter (a status change) are sent and succeed; u on a pending
// one releases it.
func TestAgentOwnedTaskIsSentToTheServer(t *testing.T) {
	f := newFixture(t)
	ts := f.openTasks(t, "theirs", "assigned") // cursor on "assigned"
	if _, err := f.sam.Claim(taskCtx, bus.TaskClaimInput{ID: ts[0].ID}); err != nil {
		t.Fatal(err)
	}
	owner := "sam"
	if _, err := f.sam.Update(taskCtx, bus.TaskPatch{ID: ts[1].ID, Owner: &owner}); err != nil {
		t.Fatal(err)
	}
	f.receive(t)
	f.key("t") // takes over sam's pending task
	if got := f.task(t, ts[1].ID); got.Owner != "eric" || got.Status != "in_progress" || f.m.toast != "" {
		t.Fatalf("take over: %+v toast=%q", got, f.m.toast)
	}
	f.key("up")
	f.key(" ") // sam's in-progress task drafts completed
	if f.m.draft.id != ts[0].ID {
		t.Fatalf("space on another's task must draft: draft=%+v toast=%q", f.m.draft, f.m.toast)
	}
	f.key("enter")
	if got := f.task(t, ts[0].ID); got.Owner != "sam" || got.Status != "completed" || f.m.toast != "" {
		t.Fatalf("status change on another's task: %+v toast=%q", got, f.m.toast)
	}
}

// TestReleaseOfAnothersPendingTask: u is sent for a task sam owns.
func TestReleaseOfAnothersPendingTask(t *testing.T) {
	f := newFixture(t)
	a := f.openTasks(t, "a")[0]
	owner := "sam"
	if _, err := f.sam.Update(taskCtx, bus.TaskPatch{ID: a.ID, Owner: &owner}); err != nil {
		t.Fatal(err)
	}
	f.receive(t)
	f.key("u")
	if got := f.task(t, a.ID); got.Owner != "" || f.m.toast != "" {
		t.Fatalf("release: %+v toast=%q", got, f.m.toast)
	}
}

// TestTTakesUnassignedTask (decision 6): t claims an unassigned task for the
// TUI's identity at once (Claim: owner eric, in progress); on your own task it
// does nothing.
func TestTTakesUnassignedTask(t *testing.T) {
	f := newFixture(t)
	a := f.openTasks(t, "a")[0]
	f.key("t")
	if f.m.prompt.active {
		t.Fatal("t in a task list is take, not the tag prompt")
	}
	if got := f.task(t, a.ID); got.Owner != "eric" || got.Status != "in_progress" {
		t.Fatalf("take: %+v", got)
	}
	if got, ok := f.m.cursorTask(); !ok || got.Owner != "eric" {
		t.Fatalf("the tree refreshes after take: %+v", got)
	}
	rev := f.task(t, a.ID).Revision
	f.key("t")
	if got := f.task(t, a.ID); got.Revision != rev || f.m.toast != "" {
		t.Fatalf("t on your own task is a no-op: %+v toast=%q", got, f.m.toast)
	}
}

// TestUUnassignsOwnNotStartedTask (decision 6): u releases your pending task
// (Release: no owner); an unassigned task is left alone; an in-progress one is
// refused; t, u and a only hint while a draft is pending.
func TestUUnassignsOwnNotStartedTask(t *testing.T) {
	f := newFixture(t)
	a := f.openTasks(t, "a")[0]
	f.key("u")
	if f.m.toast != "" {
		t.Fatalf("u on an unassigned task does nothing: %q", f.m.toast)
	}
	mine := "eric"
	if _, err := f.bus.Update(taskCtx, bus.TaskPatch{ID: a.ID, Owner: &mine}); err != nil {
		t.Fatal(err)
	}
	f.run(f.m.loadTasks("todo"))
	f.key("u")
	if got := f.task(t, a.ID); got.Owner != "" || got.Status != "pending" {
		t.Fatalf("unassign: %+v", got)
	}
	f.key("t") // claimed: in progress, yours
	f.key("u")
	if f.m.toast != unassignRefusedToast || f.m.toastHint {
		t.Fatalf("u on an in-progress task: toast=%q", f.m.toast)
	}
	if got := f.task(t, a.ID); got.Owner != "eric" {
		t.Fatalf("refused unassign changed the task: %+v", got)
	}
	f.key(" ") // draft: completed
	for _, k := range []string{"t", "u", "a"} {
		f.key(k)
		if f.m.toast != draftHintToast || !f.m.toastHint || f.m.draft.id != a.ID {
			t.Fatalf("%q while a draft is pending: toast=%q draft=%+v", k, f.m.toast, f.m.draft)
		}
	}
	if got := f.task(t, a.ID); got.Status != "in_progress" {
		t.Fatalf("t/u/a while a draft is pending change nothing: %+v", got)
	}
}

// TestEscDiscardsDraft: esc cancels the draft and writes nothing.
func TestEscDiscardsDraft(t *testing.T) {
	f := newFixture(t)
	a := f.openTasks(t, "a")[0]
	f.key(" ")
	f.key("esc")
	if f.m.draft.id != 0 || f.m.cursor < 0 {
		t.Fatalf("esc cancels only the draft: draft=%+v cursor=%d", f.m.draft, f.m.cursor)
	}
	if got := f.task(t, a.ID); got.Status != "pending" || got.Owner != "" {
		t.Fatalf("esc writes nothing: %+v", got)
	}
}

// TestDraftSurvivesABacklogRevisionItAlreadyReflects: a task edited before
// the draft is taken is already at the revision the draft was built from
// (space fetches it fresh), so that edit's message finally arriving via a
// backlogged "oldest" subscription must not look like a newer change.
func TestDraftSurvivesABacklogRevisionItAlreadyReflects(t *testing.T) {
	f := newFixture(t)
	a := f.openTasks(t, "a")[0]
	desc := "already there before the draft"
	if _, err := f.sam.Update(taskCtx, bus.TaskPatch{ID: a.ID, Description: &desc}); err != nil {
		t.Fatal(err)
	}
	f.key(" ") // drafts from the current, already-edited revision
	if f.m.draft.id != a.ID {
		t.Fatalf("draft = %+v", f.m.draft)
	}
	f.receive(t) // delivers the creation and the edit, both backlogged until now
	if f.m.draft.id != a.ID {
		t.Fatalf("a revision the draft already reflects must not discard it: %+v", f.m.draft)
	}
}

// TestSecondDraftSurvivesTheFirstSavesOwnRevision: enter's own save
// produces a revision that reaches the model asynchronously; a new draft
// started right after, from that just-saved state, must not be discarded
// when that revision finally arrives.
func TestSecondDraftSurvivesTheFirstSavesOwnRevision(t *testing.T) {
	f := newFixture(t)
	a := f.openTasks(t, "a")[0]
	f.key(" ")
	f.key("enter") // claims: a is in_progress, owned by the TUI, at a new revision
	f.key(" ")     // a second draft, taken from that just-saved revision
	if f.m.draft.id != a.ID {
		t.Fatalf("draft = %+v", f.m.draft)
	}
	f.receive(t) // delivers the claim's own revision, backlogged until now
	if f.m.draft.id != a.ID {
		t.Fatalf("the save's own revision must not discard the draft it enabled: %+v", f.m.draft)
	}
}

// TestSpaceRefusesAStaleRow: Sam claims the row's task on the bus after the
// tree loaded, but that change's batch is never delivered to the TUI, so
// the row still shows it unassigned and pending. Space's synchronous
// TaskGet (starting a new draft) sees Sam's claim and must refuse to draft
// from a row that stale: no draft, an error toast, and a reload so the row
// catches up.
func TestSpaceRefusesAStaleRow(t *testing.T) {
	f := newFixture(t)
	a := f.openTasks(t, "a")[0]
	if _, err := f.sam.Claim(taskCtx, bus.TaskClaimInput{ID: a.ID}); err != nil {
		t.Fatal(err)
	}

	f.key(" ") // no receive(): the tree still shows a unassigned and pending

	if f.m.draft.id != 0 {
		t.Fatalf("a stale row must not draft: %+v", f.m.draft)
	}
	if f.m.toast != staleTaskToast || f.m.toastHint {
		t.Fatalf("toast = %q hint=%v, want the stale-row error toast", f.m.toast, f.m.toastHint)
	}
	if got, ok := f.m.cursorTask(); !ok || got.Owner != "sam" || got.Status != "in_progress" {
		t.Fatalf("the reload must show the row's real state: %+v", got)
	}
}

// TestAssignPicker (#19): a opens a picker of the TUI's identity and the
// live sessions; esc closes it without a change, enter assigns the cursor
// task; an agent's task is refused with the owned toast; the TUI's own
// task can be handed to an agent (ADR 0005 amendment 2026-09-30).
func TestAssignPicker(t *testing.T) {
	f := newFixture(t)
	tasks := f.openTasks(t, "a", "b")
	b := tasks[1] // the cursor starts on the last task
	pick := func(name string) {
		t.Helper()
		f.key("a")
		if f.m.mode != modeAssign {
			t.Fatalf("a in a task list opens the picker: mode=%v toast=%q", f.m.mode, f.m.toast)
		}
		if f.m.assign.names[0] != f.c.identity {
			t.Fatalf("the TUI's own identity is listed first: %v", f.m.assign.names)
		}
		if v := stripANSI(f.m.View().Content); !strings.Contains(v, "eric (you)") {
			t.Fatalf("the picker marks the TUI's identity:\n%s", v)
		}
		var want []string
		for _, id := range f.m.identities {
			if id.Name != f.c.identity {
				want = append(want, id.Name)
			}
		}
		if got := f.m.assign.names[1:]; !slices.Equal(got, want) {
			t.Fatalf("picker lists the other identities %v, got %v", want, got)
		}
		for f.m.assign.names[f.m.assign.sel] != name {
			before := f.m.assign.sel
			f.key("down")
			if f.m.assign.sel == before {
				t.Fatalf("%q not in the picker: %v", name, f.m.assign.names)
			}
		}
		if v := f.m.View().Content; !strings.Contains(v, "assign") || !strings.Contains(v, "sam") {
			t.Fatalf("picker view:\n%s", v)
		}
		f.key("enter")
	}

	f.key("a")
	f.key("esc")
	if f.m.mode != modeNormal || f.task(t, b.ID).Owner != "" {
		t.Fatalf("esc closes without a change: mode=%v %+v", f.m.mode, f.task(t, b.ID))
	}

	pick("sam")
	if got := f.task(t, b.ID); got.Owner != "sam" || got.Status != "pending" || f.m.mode != modeNormal {
		t.Fatalf("assign to an agent: %+v mode=%v", got, f.m.mode)
	}
	if got, ok := f.m.cursorTask(); !ok || got.Owner != "sam" {
		t.Fatalf("the tree refreshes after assign: %+v", got)
	}

	f.key("a")
	if f.m.mode != modeAssign {
		t.Fatalf("the picker opens on an agent's task too: mode=%v toast=%q", f.m.mode, f.m.toast)
	}
	f.key("esc")

	f.key("up") // task a
	f.key("t")
	pick("sam")
	if got := f.task(t, tasks[0].ID); got.Owner != "sam" {
		t.Fatalf("the TUI hands its own task to an agent: %+v toast=%q", got, f.m.toast)
	}
}

// TestSpaceRefusesAnUnknownStatus: a row whose status this TUI does not know
// is never cycled into a draft.
func TestSpaceRefusesAnUnknownStatus(t *testing.T) {
	f := newFixture(t)
	a := f.openTasks(t, "a")[0]
	f.send(tasksMsg{ch: "todo", tasks: []bus.TaskSummary{{ID: a.ID, Subject: "a", Status: "frozen"}}})
	f.key(" ")
	if f.m.draft.id != 0 || !strings.Contains(f.m.toast, "unknown status") {
		t.Fatalf("draft=%+v toast=%q", f.m.draft, f.m.toast)
	}
}

// TestAssignPickerIgnoresOtherKeys: only up, down, enter and esc act in the
// picker; enter on the current owner changes nothing.
func TestAssignPickerIgnoresOtherKeys(t *testing.T) {
	f := newFixture(t)
	a := f.openTasks(t, "a")[0]
	f.key("a")
	rev := f.task(t, a.ID).Revision
	f.key("x")
	f.key(" ")
	if f.m.mode != modeAssign || f.m.assign.sel != 0 {
		t.Fatalf("mode=%v sel=%d", f.m.mode, f.m.assign.sel)
	}
	f.key("up")
	if f.m.assign.sel != 0 {
		t.Fatalf("sel = %d, want clamped at 0", f.m.assign.sel)
	}
	f.key("enter") // an unassigned task: the first row (eric) assigns it
	if got := f.task(t, a.ID); got.Owner != "eric" || got.Revision <= rev {
		t.Fatalf("enter on the first row assigns it: %+v", got)
	}
	rev = f.task(t, a.ID).Revision
	f.key("a")
	f.key("enter") // already the owner: no write
	if got := f.task(t, a.ID); got.Revision != rev {
		t.Fatalf("enter on the current owner wrote: %+v", got)
	}
}

// TestBlockedClaimShowsTheServersAnswer: t on a blocked task is sent; the
// server's conflict text is the toast and nothing changes.
func TestBlockedClaimShowsTheServersAnswer(t *testing.T) {
	f := newFixture(t)
	ts := f.openTasks(t, "first")
	b, err := f.sam.Create(taskCtx, bus.TaskCreateInput{Channel: "todo", Subject: "second", BlockedBy: []int64{ts[0].ID}})
	if err != nil {
		t.Fatal(err)
	}
	f.run(f.m.loadTasks("todo"))
	f.key("down")
	if c, ok := f.m.cursorTask(); !ok || c.ID != b.ID {
		t.Fatalf("cursor not on the blocked task: %+v", c)
	}
	f.key("t")
	if !strings.HasPrefix(f.m.toast, "task: ") || f.task(t, b.ID).Owner != "" {
		t.Fatalf("toast=%q task=%+v", f.m.toast, f.task(t, b.ID))
	}
}
