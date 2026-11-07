package server

import (
	"encoding/json"
	"strings"
	"testing"

	ab2bus "github.com/ericfitz/agentmesh/internal/bus"
)

// TestTaskWireReclaimedMark: a marked Task and TaskSummary carry
// reclaimed_from and reclaimed_at on the wire; an unmarked one carries
// neither key.
func TestTaskWireReclaimedMark(t *testing.T) {
	const at = int64(1_700_000_000_000)
	marked := map[string]any{
		"task":    taskToGen(ab2bus.Task{ID: 1, Subject: "s", Status: "pending", Rank: "m", ReclaimedFrom: "pat", ReclaimedAt: at}),
		"summary": taskSummaryToGen(ab2bus.TaskSummary{ID: 1, Subject: "s", Status: "pending", ReclaimedFrom: "pat", ReclaimedAt: at}),
	}
	plain := map[string]any{
		"task":    taskToGen(ab2bus.Task{ID: 1, Subject: "s", Status: "pending", Rank: "m"}),
		"summary": taskSummaryToGen(ab2bus.TaskSummary{ID: 1, Subject: "s", Status: "pending"}),
	}
	for name, v := range marked {
		j, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{`"reclaimed_from":"pat"`, `"reclaimed_at":1700000000000`} {
			if !strings.Contains(string(j), want) {
				t.Errorf("marked %s %s lacks %s", name, j, want)
			}
		}
	}
	for name, v := range plain {
		j, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(j), "reclaimed") {
			t.Errorf("unmarked %s %s carries a reclaimed key", name, j)
		}
	}
}
