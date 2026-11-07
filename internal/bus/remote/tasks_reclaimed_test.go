package remote

import (
	"testing"

	"github.com/ericfitz/agentmesh/internal/bus"
)

// TestTaskConvertReclaimedMark: the reclaimed mark survives the round trip
// through the generated Task and TaskSummary, and an unmarked task stays
// unmarked (no pointer to an empty value on the wire).
func TestTaskConvertReclaimedMark(t *testing.T) {
	const at = int64(1_700_000_000_000)
	task := bus.Task{ID: 1, Subject: "s", Status: "pending", Rank: "m", ReclaimedFrom: "pat", ReclaimedAt: at}
	if got := fromGenTask(toGenTask(task)); got.ReclaimedFrom != "pat" || got.ReclaimedAt != at {
		t.Fatalf("task round trip mark = %q, %d", got.ReclaimedFrom, got.ReclaimedAt)
	}
	sum := bus.TaskSummary{ID: 1, Subject: "s", Status: "pending", ReclaimedFrom: "pat", ReclaimedAt: at}
	if got := fromGenTaskSummary(toGenTaskSummary(sum)); got.ReclaimedFrom != "pat" || got.ReclaimedAt != at {
		t.Fatalf("summary round trip mark = %q, %d", got.ReclaimedFrom, got.ReclaimedAt)
	}
	g := toGenTask(bus.Task{ID: 1, Subject: "s", Status: "pending", Rank: "m"})
	gs := toGenTaskSummary(bus.TaskSummary{ID: 1, Subject: "s", Status: "pending"})
	if g.ReclaimedFrom != nil || g.ReclaimedAt != nil || gs.ReclaimedFrom != nil || gs.ReclaimedAt != nil {
		t.Fatalf("unmarked task sends a mark: %v %v %v %v", g.ReclaimedFrom, g.ReclaimedAt, gs.ReclaimedFrom, gs.ReclaimedAt)
	}
}
