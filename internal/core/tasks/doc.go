// Package tasks holds the task rules: tasks are memory rows on a channel of
// kind tasks (Decision 90), each revision a JSON document, with a tree of
// parents, blocked_by edges, sibling ranks, owner leases and abandoned-task
// reclaim. Ported from v1 internal/bus/tasks.go, tasks_update.go and
// tasks_reclaim.go.
package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"unicode"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/rules"
	"github.com/ericfitz/agentbus2/internal/store"
)

// task is a bus.Task plus the seq of its live revision row.
type task struct {
	bus.Task
	seq int64
}

// taskDoc is the JSON object stored as a task's memory content: the subset of
// bus.Task that is neither envelope data (id, channel, revision, updated_at,
// updated_by) nor derived (blocked, open_blockers).
type taskDoc struct {
	Subject     string            `json:"subject"`
	Description string            `json:"description,omitempty"`
	Status      string            `json:"status"`
	Owner       string            `json:"owner,omitempty"`
	Parent      int64             `json:"parent,omitempty"`
	Rank        string            `json:"rank"`
	BlockedBy   []int64           `json:"blocked_by,omitempty"`
	LeasedUntil int64             `json:"leased_until,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// taskContent returns the JSON document stored as t's memory content.
func taskContent(t bus.Task) (string, error) {
	j, err := json.Marshal(taskDoc{
		Subject: t.Subject, Description: t.Description, Status: t.Status,
		Owner: t.Owner, Parent: t.Parent, Rank: t.Rank, BlockedBy: t.BlockedBy,
		LeasedUntil: t.LeasedUntil, Metadata: t.Metadata,
	})
	if err != nil {
		return "", bus.Internal(err)
	}
	return string(j), nil
}

// isTaskStatus reports whether s is one of the three task statuses.
func isTaskStatus(s string) bool {
	return s == "pending" || s == "in_progress" || s == "completed"
}

// validateTaskSubject enforces subject 1-256 bytes, no control characters.
func validateTaskSubject(s string) error {
	if len(s) == 0 || len(s) > 256 {
		return core.E(bus.CodeValidation, "subject must be 1-256 bytes")
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return core.E(bus.CodeValidation, "subject must not contain control characters")
		}
	}
	return nil
}

// dedupeIDs returns ids with duplicates removed, order preserved.
func dedupeIDs(ids []int64) []int64 {
	if len(ids) == 0 {
		return nil
	}
	seen := make(map[int64]bool, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// loadTasks returns every live task of the channel: rows whose content is not
// a JSON object with a non-empty subject and a valid status are skipped.
// Derived fields are filled in and the result is in tree order (taskTree).
// LiveMemories is ordered by seq, which keeps taskTree's cycle fallback
// deterministic.
func loadTasks(ctx context.Context, tx store.Tx, ch store.Channel) ([]task, error) {
	ms, err := tx.LiveMemories(ctx, ch.ID)
	if err != nil {
		return nil, core.FromStore(err)
	}
	var ts []task
	for _, m := range ms {
		if m.MemoryID == nil || m.Revision == nil {
			continue
		}
		var d taskDoc
		if err := json.Unmarshal([]byte(m.Content), &d); err != nil || d.Subject == "" || !isTaskStatus(d.Status) {
			continue
		}
		ts = append(ts, task{
			Task: bus.Task{
				ID: *m.MemoryID, Channel: ch.Name, Revision: *m.Revision,
				Subject: d.Subject, Description: d.Description, Status: d.Status,
				Owner: d.Owner, Parent: d.Parent, Rank: d.Rank, BlockedBy: d.BlockedBy,
				LeasedUntil: d.LeasedUntil, Metadata: d.Metadata,
				UpdatedAt: m.CreatedAt, UpdatedBy: m.Sender,
			},
			seq: m.Seq,
		})
	}
	fillDerived(ts)
	order, _ := taskTree(ts)
	return order, nil
}

// fillDerived computes Blocked and OpenBlockers for every task in ts, in
// place: a blocker is open while it is live (in ts) and not completed.
func fillDerived(ts []task) {
	byID := make(map[int64]*task, len(ts))
	for i := range ts {
		byID[ts[i].ID] = &ts[i]
	}
	for i := range ts {
		var open []int64
		for _, bid := range ts[i].BlockedBy {
			if blocker, ok := byID[bid]; ok && blocker.Status != "completed" {
				open = append(open, bid)
			}
		}
		ts[i].OpenBlockers = open
		ts[i].Blocked = len(open) > 0
	}
}

// taskByID returns the task in ts with the given id, or nil.
func taskByID(ts []task, id int64) *task {
	for i := range ts {
		if ts[i].ID == id {
			return &ts[i]
		}
	}
	return nil
}

// effectiveParent is parent's value for tree purposes: 0 (root) when parent is
// not a live task in ts, so a task whose parent is gone is a root.
func effectiveParent(ts []task, parent int64) int64 {
	if parent != 0 && taskByID(ts, parent) == nil {
		return 0
	}
	return parent
}

// sortSiblings orders by rank, then id.
func sortSiblings(sib []task) {
	sort.Slice(sib, func(i, j int) bool {
		if sib[i].Rank != sib[j].Rank {
			return sib[i].Rank < sib[j].Rank
		}
		return sib[i].ID < sib[j].ID
	})
}

// taskTree groups ts by effective parent and walks it depth-first from the
// root, returning the order (each task followed by its children, siblings by
// rank then id) and each task's depth. A cycle with no path from a live root
// (possible only from a corrupt write) is emitted last as extra roots at depth
// 0.
func taskTree(ts []task) ([]task, map[int64]int) {
	children := map[int64][]task{}
	for _, t := range ts {
		p := effectiveParent(ts, t.Parent)
		children[p] = append(children[p], t)
	}
	for p := range children {
		sortSiblings(children[p])
	}
	order := make([]task, 0, len(ts))
	depth := make(map[int64]int, len(ts))
	visited := make(map[int64]bool, len(ts))
	var walk func(parent int64, d int)
	walk = func(parent int64, d int) {
		for _, t := range children[parent] {
			if visited[t.ID] {
				continue
			}
			visited[t.ID] = true
			depth[t.ID] = d
			order = append(order, t)
			walk(t.ID, d+1)
		}
	}
	walk(0, 0)
	for _, t := range ts {
		if !visited[t.ID] {
			order = append(order, t)
			depth[t.ID] = 0
		}
	}
	return order, depth
}

// followsToSelf reports whether walking from start via next (the parent chain
// or the blocked_by edges) ever reaches target, guarding against a cycle that
// does not involve target.
func followsToSelf(ts []task, start, target int64, next func(task) []int64) bool {
	seen := map[int64]bool{}
	stack := []int64{start}
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if id == target {
			return true
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		t := taskByID(ts, id)
		if t == nil {
			continue
		}
		stack = append(stack, next(*t)...)
	}
	return false
}

// maxBlockers caps blocked_by.
const maxBlockers = 64

// validateTaskLinks checks t's parent and blocked_by against the live tasks in
// ts: existence, no self-reference, no cycles, and the cap on blocked_by.
// Duplicate blocked_by ids are skipped, not rejected or double-counted.
func validateTaskLinks(ts []task, t task) error {
	if t.Parent != 0 {
		if t.Parent == t.ID {
			return core.E(bus.CodeValidation, "task %d cannot be its own parent", t.ID)
		}
		if taskByID(ts, t.Parent) == nil {
			return core.E(bus.CodeValidation, "parent %d does not exist", t.Parent)
		}
		if followsToSelf(ts, t.Parent, t.ID, func(x task) []int64 {
			if x.Parent == 0 {
				return nil
			}
			return []int64{x.Parent}
		}) {
			return core.E(bus.CodeValidation, "parent %d would create a cycle", t.Parent)
		}
	}
	blockers := dedupeIDs(t.BlockedBy)
	if len(blockers) > maxBlockers {
		return core.E(bus.CodeValidation, "blocked_by accepts at most %d tasks", maxBlockers)
	}
	for _, bid := range blockers {
		if bid == t.ID {
			return core.E(bus.CodeValidation, "task %d cannot block itself", t.ID)
		}
		if taskByID(ts, bid) == nil {
			return core.E(bus.CodeValidation, "blocked_by %d does not exist", bid)
		}
		if followsToSelf(ts, bid, t.ID, func(x task) []int64 { return x.BlockedBy }) {
			return core.E(bus.CodeValidation, "blocked_by %d would create a cycle", bid)
		}
	}
	return nil
}

// rankStep positions a rank relative to one or two neighbors. A genuinely
// unbounded side steps by RankAfter/RankBefore instead of RankBetween, which
// would take a midpoint against the alphabet's edge and grow the key by a
// byte every few inserts. The caller (placeRank) guarantees lo < hi when both
// are set.
func rankStep(lo, hi string) string {
	switch {
	case lo != "" && hi == "":
		return rules.RankAfter(lo)
	case lo == "" && hi != "":
		return rules.RankBefore(hi)
	}
	return rules.RankBetween(lo, hi)
}

// rankCollision is placeRank's report that the two neighbors it must place a
// task between share a rank (possible only from a corrupt write). The caller
// renumbers the siblings under parent (rerankSiblings) in its transaction and
// places again.
type rankCollision struct{ parent int64 }

func (e *rankCollision) Error() string {
	return fmt.Sprintf("sibling ranks under parent %d collide", e.parent)
}

// placeRank computes the rank for self among the tasks in ts that share
// parent's effective value (self excluded), positioned by before/after
// (sibling ids, mutually exclusive) or appended after the last sibling when
// neither is set. An orphaned sibling is a root, as in the tree.
func placeRank(ts []task, self, parent, before, after int64) (string, error) {
	if before != 0 && after != 0 {
		return "", core.E(bus.CodeValidation, "before and after are mutually exclusive")
	}
	parent = effectiveParent(ts, parent)
	var sib []task
	for _, t := range ts {
		if t.ID != self && effectiveParent(ts, t.Parent) == parent {
			sib = append(sib, t)
		}
	}
	sortSiblings(sib)
	named := before
	if after != 0 {
		named = after
	}
	idx := -1
	if named != 0 {
		for i, t := range sib {
			if t.ID == named {
				idx = i
				break
			}
		}
		if idx == -1 {
			return "", core.E(bus.CodeValidation, "task %d is not a sibling under parent %d", named, parent)
		}
	}
	switch {
	case after != 0:
		hi := ""
		if idx+1 < len(sib) {
			hi = sib[idx+1].Rank
		}
		if hi != "" && sib[idx].Rank >= hi {
			return "", &rankCollision{parent}
		}
		return rankStep(sib[idx].Rank, hi), nil
	case before != 0:
		lo := ""
		if idx > 0 {
			lo = sib[idx-1].Rank
		}
		if lo != "" && lo >= sib[idx].Rank {
			return "", &rankCollision{parent}
		}
		return rankStep(lo, sib[idx].Rank), nil
	default:
		lo := ""
		if len(sib) > 0 {
			lo = sib[len(sib)-1].Rank
		}
		return rankStep(lo, ""), nil
	}
}
