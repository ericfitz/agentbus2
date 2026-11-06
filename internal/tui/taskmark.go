package tui

import (
	"time"

	"github.com/ericfitz/agentmesh/internal/bus"
)

// taskState is the state a task row shows: the Slack canvas states, in the
// canvas's precedence.
type taskState int

const (
	statePending taskState = iota
	stateInProgress
	stateDone
	stateBlocked
	stateStalled
	stateNeedsHuman
	stateProblem
)

// taskStateOf judges t's state; the first match wins: completed, an open
// blocker, the owner's needs-human tag, a stall (a reclaim mark, a passed
// lease, or an in-progress owner with no live session), the owner's problem,
// then the task's own status. now is the caller's clock; the TUI judges
// stalls by it and by the last agent-status round. The suffix is the dim
// text the state adds to the row ("" for none).
func taskStateOf(t bus.TaskSummary, agents []bus.AgentStatusEntry, now time.Time) (st taskState, suffix string) {
	return judgeTask(t, agents, now, false)
}

// draftTaskStateOf is taskStateOf for a drafted (unsaved) row, which previews
// the state a save will produce: it does not judge the owner's liveness, and
// a draft that leaves pending or gains an owner shows no reclaimed mark,
// because the server clears the mark on that write.
func draftTaskStateOf(t bus.TaskSummary, agents []bus.AgentStatusEntry, now time.Time) (st taskState, suffix string) {
	if t.Status != "pending" || t.Owner != "" {
		t.ReclaimedFrom, t.ReclaimedAt = "", 0
	}
	return judgeTask(t, agents, now, true)
}

func judgeTask(t bus.TaskSummary, agents []bus.AgentStatusEntry, now time.Time, drafted bool) (st taskState, suffix string) {
	if t.Status == "completed" {
		return stateDone, ""
	}
	if len(t.OpenBlockers) > 0 {
		return stateBlocked, ""
	}
	var owner *bus.AgentStatusEntry
	if t.Owner != "" {
		for i := range agents {
			if agents[i].Identity == t.Owner && agents[i].Live {
				owner = &agents[i]
				break
			}
		}
	}
	if owner != nil && owner.Status.Tag == bus.StatusNeedsHuman {
		return stateNeedsHuman, ""
	}
	inProgress := t.Status == "in_progress"
	switch {
	case t.ReclaimedFrom != "":
		return stateStalled, " reclaimed from " + t.ReclaimedFrom
	case inProgress && t.LeasedUntil != 0 && t.LeasedUntil <= now.UnixMilli():
		return stateStalled, ""
	case inProgress && owner == nil && !drafted:
		return stateStalled, " offline"
	}
	if owner != nil && owner.Status.Problem != "" {
		return stateProblem, ""
	}
	if inProgress {
		return stateInProgress, ""
	}
	return statePending, ""
}

// taskMark is the row icon of st, read from the icon vars at call time (the
// icon loader can reassign them).
func taskMark(st taskState) string {
	switch st {
	case stateInProgress:
		return taskInProgress
	case stateDone:
		return taskCompleted
	case stateBlocked:
		return taskBlocked
	case stateStalled, stateProblem:
		return taskStalled
	case stateNeedsHuman:
		return taskNeedsHuman
	}
	return taskPending
}
