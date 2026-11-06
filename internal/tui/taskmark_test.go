package tui

import (
	"testing"
	"time"

	"github.com/ericfitz/agentmesh/internal/bus"
)

func markAgents() []bus.AgentStatusEntry {
	e := func(id string, live bool, tag bus.StatusTag, problem string) bus.AgentStatusEntry {
		return bus.AgentStatusEntry{Identity: id, Live: live, Status: bus.AgentStatus{Identity: id, Tag: tag, Problem: problem}}
	}
	return []bus.AgentStatusEntry{
		e("sam", true, bus.StatusImplementing, ""),
		e("pat", true, bus.StatusNeedsHuman, ""),
		e("ann", true, bus.StatusImplementing, "disk full"),
		e("bob", false, bus.StatusImplementing, ""),
	}
}

func TestTaskStatePrecedence(t *testing.T) {
	now := time.UnixMilli(1_000_000)
	past, future := now.UnixMilli()-1000, now.UnixMilli()+60_000
	cases := []struct {
		name   string
		t      bus.TaskSummary
		want   taskState
		suffix string
	}{
		{"completed beats everything", bus.TaskSummary{Status: "completed", Owner: "pat", OpenBlockers: []int64{1}, ReclaimedFrom: "sam", LeasedUntil: past}, stateDone, ""},
		{"blocked beats reclaimed", bus.TaskSummary{Status: "pending", OpenBlockers: []int64{1}, ReclaimedFrom: "sam"}, stateBlocked, ""},
		{"needs-human beats expired lease", bus.TaskSummary{Status: "in_progress", Owner: "pat", LeasedUntil: past}, stateNeedsHuman, ""},
		{"expired lease", bus.TaskSummary{Status: "in_progress", Owner: "sam", LeasedUntil: past}, stateStalled, ""},
		{"offline owner", bus.TaskSummary{Status: "in_progress", Owner: "bob", LeasedUntil: future}, stateStalled, " offline"},
		{"absent owner", bus.TaskSummary{Status: "in_progress", Owner: "nobody"}, stateStalled, " offline"},
		{"reclaimed", bus.TaskSummary{Status: "pending", ReclaimedFrom: "sam"}, stateStalled, " reclaimed from sam"},
		{"problem", bus.TaskSummary{Status: "in_progress", Owner: "ann", LeasedUntil: future}, stateProblem, ""},
		{"in progress", bus.TaskSummary{Status: "in_progress", Owner: "sam", LeasedUntil: future}, stateInProgress, ""},
		{"pending offline assignee", bus.TaskSummary{Status: "pending", Owner: "bob"}, statePending, ""},
		{"pending", bus.TaskSummary{Status: "pending"}, statePending, ""},
		{"lease exactly now is expired", bus.TaskSummary{Status: "in_progress", Owner: "sam", LeasedUntil: now.UnixMilli()}, stateStalled, ""},
		{"lease one ms ahead is live", bus.TaskSummary{Status: "in_progress", Owner: "sam", LeasedUntil: now.UnixMilli() + 1}, stateInProgress, ""},
	}
	for _, c := range cases {
		got, suffix := taskStateOf(c.t, markAgents(), now)
		if got != c.want || suffix != c.suffix {
			t.Errorf("%s: got (%d, %q), want (%d, %q)", c.name, got, suffix, c.want, c.suffix)
		}
	}
}

func TestTaskMarkGlyphs(t *testing.T) {
	want := map[taskState]string{
		statePending: taskPending, stateInProgress: taskInProgress, stateDone: taskCompleted,
		stateBlocked: taskBlocked, stateStalled: taskStalled, stateProblem: taskStalled, stateNeedsHuman: taskNeedsHuman,
	}
	for st, g := range want {
		if got := taskMark(st); got != g {
			t.Errorf("taskMark(%d) = %q, want %q", st, got, g)
		}
	}
}

// Blocked wins over the reclaimed mark and carries no reclaimed suffix.
func TestBlockedTaskWithMarkShowsBlocked(t *testing.T) {
	st, suffix := taskStateOf(bus.TaskSummary{Status: "pending", OpenBlockers: []int64{2}, ReclaimedFrom: "sam"}, markAgents(), time.UnixMilli(1))
	if st != stateBlocked || suffix != "" {
		t.Fatalf("got (%d, %q)", st, suffix)
	}
}

func TestDraftTaskStateOf(t *testing.T) {
	now := time.UnixMilli(1_000_000)
	cases := []struct {
		name string
		t    bus.TaskSummary
		want taskState
		sfx  string
	}{
		{"in progress, owner not in agents", bus.TaskSummary{Status: "in_progress", Owner: "eric", ReclaimedFrom: "pat"}, stateInProgress, ""},
		{"pending with owner drops mark", bus.TaskSummary{Status: "pending", Owner: "sam", ReclaimedFrom: "pat"}, statePending, ""},
		{"pending unowned keeps mark", bus.TaskSummary{Status: "pending", ReclaimedFrom: "pat"}, stateStalled, " reclaimed from pat"},
		{"completed", bus.TaskSummary{Status: "completed", Owner: "eric"}, stateDone, ""},
	}
	for _, c := range cases {
		got, sfx := draftTaskStateOf(c.t, markAgents(), now)
		if got != c.want || sfx != c.sfx {
			t.Errorf("%s: got (%d, %q), want (%d, %q)", c.name, got, sfx, c.want, c.sfx)
		}
	}
}
