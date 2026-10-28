package tasks

import (
	"testing"

	"github.com/ericfitz/agentbus2/internal/bus"
)

func TestContentChanged(t *testing.T) {
	base := func() task {
		return task{Task: bus.Task{
			ID: 7, Channel: "work", Subject: "s", Description: "d", Status: "pending", Rank: "V",
			Metadata: map[string]string{"k": "v"},
		}}
	}
	tests := []struct {
		name string
		mut  func(*task)
		want bool
	}{
		{"equal", func(*task) {}, false},
		{"subject", func(t *task) { t.Subject = "s2" }, true},
		{"description", func(t *task) { t.Description = "d2" }, true},
		{"metadata value", func(t *task) { t.Metadata = map[string]string{"k": "w"} }, true},
		{"metadata key added", func(t *task) { t.Metadata = map[string]string{"k": "v", "x": "y"} }, true},
		{"metadata key removed", func(t *task) { t.Metadata = map[string]string{} }, true},
		{"status", func(t *task) { t.Status = "in_progress" }, false},
		{"owner", func(t *task) { t.Owner = "kim" }, false},
		{"rank", func(t *task) { t.Rank = "W" }, false},
		{"lease", func(t *task) { t.LeasedUntil = 99 }, false},
		{"parent", func(t *task) { t.Parent = 3 }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cur, patched := base(), base()
			tt.mut(&patched)
			if got := contentChanged(cur, patched); got != tt.want {
				t.Errorf("contentChanged = %v, want %v", got, tt.want)
			}
		})
	}
	// nil and empty metadata are the same content.
	a, b := base(), base()
	a.Metadata, b.Metadata = nil, map[string]string{}
	if contentChanged(a, b) {
		t.Error("nil and empty metadata differ")
	}
}
