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
// stalls by it, by the identities with a live session (sessions: a human or an
// agent that never set a status has no status row) and by the last
// agent-status round (the owner's tag and problem). The suffix is the dim
// text the state adds to the row ("" for none).
func taskStateOf(t bus.TaskSummary, agents []bus.AgentStatusEntry, sessions map[string]bool, now time.Time) (st taskState, suffix string) {
	return judgeTask(t, agents, sessions, now, false)
}

// draftTaskStateOf is taskStateOf for a drafted (unsaved) row, which previews
// the state a save will produce: it does not judge the owner's liveness; a
// draft that leaves pending or gains an owner shows no reclaimed mark,
// because the server clears the mark on that write; and an expired saved
// lease is ignored, because the save reclaims such a task before it applies
// the draft (the claim sets no lease), so the result never carries it.
func draftTaskStateOf(t bus.TaskSummary, agents []bus.AgentStatusEntry, sessions map[string]bool, now time.Time) (st taskState, suffix string) {
	if t.Status != "pending" || t.Owner != "" {
		t.ReclaimedFrom, t.ReclaimedAt = "", 0
	}
	if t.LeasedUntil != 0 && t.LeasedUntil <= now.UnixMilli() {
		t.LeasedUntil = 0
	}
	return judgeTask(t, agents, sessions, now, true)
}

func judgeTask(t bus.TaskSummary, agents []bus.AgentStatusEntry, sessions map[string]bool, now time.Time, drafted bool) (st taskState, suffix string) {
	if t.Status == "completed" {
		return stateDone, ""
	}
	if len(t.OpenBlockers) > 0 {
		return stateBlocked, ""
	}
	var owner *bus.AgentStatusEntry // the owner's live status row, if any
	ownerLive := t.Owner != "" && sessions[t.Owner]
	if t.Owner != "" {
		for i := range agents {
			if agents[i].Identity == t.Owner && agents[i].Live {
				owner = &agents[i]
				ownerLive = true
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
	case inProgress && !ownerLive && !drafted:
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

// liveSessions is the set of identities with a live session in the last
// identities round, the source the core's abandoned-task rule judges by.
func (m Model) liveSessions() map[string]bool {
	live := map[string]bool{}
	for _, id := range m.identities {
		if len(id.Sessions) > 0 {
			live[id.Name] = true
		}
	}
	return live
}
