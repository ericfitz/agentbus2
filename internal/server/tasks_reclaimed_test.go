package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ericfitz/agentmesh/internal/gen"
)

// TestTaskWireReclaimedMark: a reclaimed task carries reclaimed_from and
// reclaimed_at in the get_task and list_tasks JSON; a task never reclaimed
// carries neither key.
func TestTaskWireReclaimedMark(t *testing.T) {
	f := newMTFixture(t)
	ctx := context.Background()
	create := func(subject string) int64 {
		r, err := f.c.CreateTaskWithResponse(ctx, "work", &gen.CreateTaskParams{}, gen.TaskCreateRequest{Subject: subject})
		if err != nil || r.JSON201 == nil {
			t.Fatalf("create %s: %+v, %v", subject, r, err)
		}
		return r.JSON201.Id
	}
	marked, plain := create("marked"), create("plain")
	lease := f.e.clock.Now().Add(time.Second).UnixMilli()
	if r, err := f.c.ClaimTaskWithResponse(ctx, marked, &gen.ClaimTaskParams{}, gen.ClaimTaskRequest{LeasedUntil: &lease}); err != nil || r.JSON200 == nil {
		t.Fatalf("claim: %+v, %v", r, err)
	}
	f.e.clock.Advance(2 * time.Second) // the lease lapses; the next read reclaims
	at := f.e.clock.Now().UnixMilli()

	l, err := f.c.ListTasksWithResponse(ctx, "work", &gen.ListTasksParams{})
	if err != nil || l.JSON200 == nil {
		t.Fatalf("list: %+v, %v", l, err)
	}
	for _, s := range *l.JSON200 {
		switch s.Id {
		case marked:
			if s.ReclaimedFrom == nil || *s.ReclaimedFrom != "pat" || s.ReclaimedAt == nil || *s.ReclaimedAt != at {
				t.Errorf("list row of the reclaimed task: %+v", s)
			}
		case plain:
			if s.ReclaimedFrom != nil || s.ReclaimedAt != nil {
				t.Errorf("list row of the plain task carries a mark: %+v", s)
			}
		}
	}
	if body := string(l.Body); strings.Count(body, `"reclaimed_from":"pat"`) != 1 || strings.Count(body, `"reclaimed_at":`) != 1 {
		t.Errorf("list JSON %s: want one reclaimed_from and one reclaimed_at", body)
	}

	g, err := f.c.GetTaskWithResponse(ctx, marked)
	if err != nil || g.JSON200 == nil || !strings.Contains(string(g.Body), `"reclaimed_from":"pat"`) || !strings.Contains(string(g.Body), `"reclaimed_at":`) {
		t.Fatalf("get reclaimed: %s, %v", g.Body, err)
	}
	g, err = f.c.GetTaskWithResponse(ctx, plain)
	if err != nil || g.JSON200 == nil || strings.Contains(string(g.Body), "reclaimed") {
		t.Fatalf("get plain: %s, %v", g.Body, err)
	}
}
