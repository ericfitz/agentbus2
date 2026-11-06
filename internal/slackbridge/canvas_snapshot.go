package slackbridge

import (
	"context"
	"slices"
	"sort"
	"time"

	"github.com/ericfitz/agentmesh/internal/bus"
	"github.com/ericfitz/agentmesh/internal/core"
	"github.com/ericfitz/agentmesh/internal/core/access"
	"github.com/ericfitz/agentmesh/internal/core/sessions"
	"github.com/ericfitz/agentmesh/internal/core/statuses"
	"github.com/ericfitz/agentmesh/internal/core/tasks"
	"github.com/ericfitz/agentmesh/internal/store"
)

// CanvasSnapshot is what the scope canvas shows at one instant: the task
// lists and memory channels of a scope that every allowed human of the
// bridge may read.
type CanvasSnapshot struct {
	ScopeName      string
	At             time.Time
	TaskLists      []CanvasTaskList
	MemoryChannels []CanvasMemoryChannel
}

// CanvasTaskList is one task channel's live tasks in tree order.
type CanvasTaskList struct {
	Channel string
	Tasks   []CanvasTask
}

// CanvasTask is one task. Stalled is the abandonment flag; ReclaimedFrom and
// ReclaimedAt are the mark a reclaim leaves (ReclaimedAt is zero when unset); LeaseExpiredAt is
// zero unless the lease lapsed; OwnerStatus is nil for a completed task, a
// task with no assignee and an assignee with no live status.
type CanvasTask struct {
	ID                        int64
	Subject, Status, Assignee string
	Depth                     int
	OpenBlockers              []int64
	Stalled                   bool
	LeaseExpiredAt            time.Time
	ReclaimedFrom             string
	ReclaimedAt               time.Time
	OwnerStatus               *CanvasOwnerStatus
}

// CanvasOwnerStatus is the assignee's one-line status.
type CanvasOwnerStatus struct{ Tag, Text, Problem string }

// CanvasMemoryChannel is one memory channel's live memories, newest edit first.
type CanvasMemoryChannel struct {
	Channel  string
	Memories []CanvasMemory
}

// CanvasMemory is one live memory.
type CanvasMemory struct {
	ID                             int64
	Subject, Type, Content, Sender string
	Tags                           []string
	EditedAt                       time.Time
}

// CanvasSnapshot reads the canvas content of scope in one Store.Read. The
// caller derives scope from the bridge's channel; this function never reads
// b.Scope. A candidate (a task or memory channel of the scope) is shown only
// when the bridge has at least one human and every human may read it.
func (s *Service) CanvasSnapshot(ctx context.Context, b store.Bridge, scope string) (CanvasSnapshot, error) {
	nowMs := s.core.NowMs()
	out := CanvasSnapshot{ScopeName: scope, At: time.UnixMilli(nowMs).UTC()}
	svcTasks := tasks.Service{Core: s.core}
	svcStatuses := statuses.Service{Core: s.core, Sessions: sessions.Service{Core: s.core}}
	err := s.core.Store.Read(ctx, func(ctx context.Context, tx store.Tx) error {
		out.TaskLists, out.MemoryChannels = nil, nil
		if _, err := tx.Scope(ctx, scope); err != nil {
			return core.FromStore(err)
		}
		posters, err := s.core.Access.Holders(ctx, tx, access.Channel{ID: b.ChannelID}, access.Post)
		if err != nil {
			return err
		}
		humans := make([]string, 0, len(posters))
		for _, p := range posters {
			humans = append(humans, p.Name)
		}
		chans, _, err := tx.ScopeMembers(ctx, scope)
		if err != nil {
			return core.FromStore(err)
		}
		type taskChan struct {
			name  string
			tasks []tasks.SnapshotTask
		}
		var taskChans []taskChan
		assignees := map[string]bool{}
		for _, ch := range chans {
			if !store.ValidKind(ch.Kind) {
				return core.E(bus.CodeInternal, "channel %q has unknown kind %q", ch.Name, string(ch.Kind))
			}
			if ch.Kind == store.KindOrdinary || len(humans) == 0 {
				continue
			}
			readers, err := s.core.Access.Holders(ctx, tx, access.Channel{ID: ch.ID}, access.Read)
			if err != nil {
				return err
			}
			names := make(map[string]bool, len(readers))
			for _, r := range readers {
				names[r.Name] = true
			}
			if !allIn(humans, names) {
				continue
			}
			// ValidKind above guards the else branch: a kind that is neither
			// ordinary nor tasks is memory.
			switch ch.Kind {
			case store.KindTasks:
				ts, err := svcTasks.Snapshot(ctx, tx, ch, nowMs)
				if err != nil {
					return err
				}
				for _, t := range ts {
					if t.Status != "completed" && t.Assignee != "" {
						assignees[t.Assignee] = true
					}
				}
				taskChans = append(taskChans, taskChan{ch.Name, ts})
			default:
				ms, err := tx.LiveMemories(ctx, ch.ID)
				if err != nil {
					return core.FromStore(err)
				}
				out.MemoryChannels = append(out.MemoryChannels, memoryChannel(ch.Name, ms))
			}
		}
		live, err := svcStatuses.LiveStatuses(ctx, tx, keys(assignees))
		if err != nil {
			return err
		}
		for _, tc := range taskChans {
			l := CanvasTaskList{Channel: tc.name, Tasks: make([]CanvasTask, 0, len(tc.tasks))}
			for _, t := range tc.tasks {
				ct := CanvasTask{
					ID: t.ID, Subject: t.Subject, Status: t.Status, Assignee: t.Assignee,
					Depth: t.Depth, OpenBlockers: t.OpenBlockers, Stalled: t.Abandoned,
					ReclaimedFrom: t.ReclaimedFrom,
				}
				if t.ReclaimedAt != 0 {
					ct.ReclaimedAt = time.UnixMilli(t.ReclaimedAt).UTC()
				}
				if t.LeaseExpired {
					ct.LeaseExpiredAt = time.UnixMilli(t.LeasedUntil).UTC()
				}
				if st, ok := live[t.Assignee]; ok && t.Status != "completed" && t.Assignee != "" {
					ct.OwnerStatus = &CanvasOwnerStatus{Tag: string(st.Tag), Text: st.Text, Problem: st.Problem}
				}
				l.Tasks = append(l.Tasks, ct)
			}
			out.TaskLists = append(out.TaskLists, l)
		}
		return nil
	})
	if err != nil {
		return CanvasSnapshot{}, err
	}
	return out, nil
}

func allIn(want []string, have map[string]bool) bool {
	for _, n := range want {
		if !have[n] {
			return false
		}
	}
	return true
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func memoryChannel(name string, ms []store.Message) CanvasMemoryChannel {
	mc := CanvasMemoryChannel{Channel: name, Memories: make([]CanvasMemory, 0, len(ms))}
	for _, m := range ms {
		if m.Tombstone || m.MemoryID == nil {
			continue
		}
		mc.Memories = append(mc.Memories, CanvasMemory{
			ID: *m.MemoryID, Subject: m.Subject, Type: m.Type, Content: m.Content, Sender: m.Sender,
			Tags: m.Tags, EditedAt: time.UnixMilli(m.CreatedAt).UTC(),
		})
	}
	sort.SliceStable(mc.Memories, func(i, j int) bool {
		a, b := mc.Memories[i], mc.Memories[j]
		if !a.EditedAt.Equal(b.EditedAt) {
			return a.EditedAt.After(b.EditedAt)
		}
		return a.ID > b.ID
	})
	return mc
}
