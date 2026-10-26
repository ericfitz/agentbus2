package tasks

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/bus"
)

func mk(id int64, parent int64, rank, status, owner string) task {
	return task{Task: bus.Task{ID: id, Subject: "t", Status: status, Owner: owner, Parent: parent, Rank: rank}, seq: id}
}

func ids(ts []task) []int64 {
	out := make([]int64, 0, len(ts))
	for _, t := range ts {
		out = append(out, t.ID)
	}
	return out
}

func wantCode(t *testing.T, err error, code bus.Code) {
	t.Helper()
	if got := bus.CodeOf(err); got != code {
		t.Fatalf("code = %q (%v), want %q", got, err, code)
	}
}

func TestTaskTreeOrder(t *testing.T) {
	// 1 (V) has children 4 (V) and 3 (a); 2 (W) is a second root; 5's parent 99 is gone, so it is a root.
	ts := []task{mk(1, 0, "V", "pending", ""), mk(2, 0, "W", "pending", ""), mk(3, 1, "a", "pending", ""), mk(4, 1, "V", "pending", ""), mk(5, 99, "X", "pending", "")}
	order, depth := taskTree(ts)
	if got, want := ids(order), []int64{1, 4, 3, 2, 5}; !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	if depth[1] != 0 || depth[4] != 1 || depth[5] != 0 {
		t.Fatalf("depth = %v", depth)
	}
	// Equal ranks fall back to id.
	order, _ = taskTree([]task{mk(9, 0, "V", "pending", ""), mk(7, 0, "V", "pending", "")})
	if got, want := ids(order), []int64{7, 9}; !reflect.DeepEqual(got, want) {
		t.Fatalf("tie order = %v, want %v", got, want)
	}
	// A parent cycle with no live root is emitted last as extra roots.
	order, depth = taskTree([]task{mk(1, 2, "V", "pending", ""), mk(2, 1, "V", "pending", ""), mk(3, 0, "V", "pending", "")})
	if len(order) != 3 || order[0].ID != 3 || depth[1] != 0 || depth[2] != 0 {
		t.Fatalf("cycle order = %v depth %v", ids(order), depth)
	}
}

func TestPlaceRank(t *testing.T) {
	ts := []task{mk(1, 0, "V", "pending", ""), mk(2, 0, "W", "pending", "")}
	r, err := placeRank(ts, 0, 0, 0, 0)
	if err != nil || r <= "W" {
		t.Fatalf("append rank = %q, %v; want above W", r, err)
	}
	r, err = placeRank(ts, 0, 0, 1, 0)
	if err != nil || r >= "V" {
		t.Fatalf("before 1 = %q, %v; want below V", r, err)
	}
	r, err = placeRank(ts, 0, 0, 0, 1)
	if err != nil || r <= "V" || r >= "W" {
		t.Fatalf("after 1 = %q, %v; want between V and W", r, err)
	}
	r, err = placeRank(nil, 0, 0, 0, 0)
	if err != nil || r != "V" {
		t.Fatalf("empty list rank = %q, %v; want V", r, err)
	}
	_, err = placeRank(ts, 0, 0, 1, 2)
	wantCode(t, err, bus.CodeValidation)
	_, err = placeRank(ts, 0, 0, 77, 0)
	wantCode(t, err, bus.CodeValidation)
	// A moving task is excluded from its own sibling set.
	r, err = placeRank(ts, 1, 0, 0, 2)
	if err != nil || r <= "W" {
		t.Fatalf("move 1 after 2 = %q, %v", r, err)
	}
	// Collision: both neighbors share a rank.
	col := []task{mk(1, 0, "V", "pending", ""), mk(2, 0, "V", "pending", "")}
	_, err = placeRank(col, 0, 0, 0, 1)
	var rc *rankCollision
	if !errors.As(err, &rc) || rc.parent != 0 {
		t.Fatalf("collision err = %v", err)
	}
	_, err = placeRank(col, 0, 0, 2, 0)
	if !errors.As(err, &rc) {
		t.Fatalf("collision before err = %v", err)
	}
	// An orphan counts as a root sibling.
	orph := []task{mk(1, 0, "V", "pending", ""), mk(2, 99, "W", "pending", "")}
	r, err = placeRank(orph, 0, 0, 0, 2)
	if err != nil || r <= "W" {
		t.Fatalf("after orphan = %q, %v", r, err)
	}
}

func TestValidateTaskLinks(t *testing.T) {
	ts := []task{mk(1, 0, "V", "pending", ""), mk(2, 1, "V", "pending", "")}
	ts[1].BlockedBy = []int64{1}
	cases := []struct {
		name string
		t    task
		ok   bool
	}{
		{"no links", mk(3, 0, "V", "pending", ""), true},
		{"existing parent", mk(3, 1, "V", "pending", ""), true},
		{"self parent", mk(3, 3, "V", "pending", ""), false},
		{"missing parent", mk(3, 42, "V", "pending", ""), false},
		{"parent cycle", mk(1, 2, "V", "pending", ""), false},
	}
	for _, c := range cases {
		err := validateTaskLinks(ts, c.t)
		if (err == nil) != c.ok {
			t.Errorf("%s: err = %v, ok want %v", c.name, err, c.ok)
		}
		if err != nil {
			wantCode(t, err, bus.CodeValidation)
		}
	}
	b := mk(3, 0, "V", "pending", "")
	b.BlockedBy = []int64{3}
	wantCode(t, validateTaskLinks(ts, b), bus.CodeValidation)
	b.BlockedBy = []int64{42}
	wantCode(t, validateTaskLinks(ts, b), bus.CodeValidation)
	b.BlockedBy = []int64{2, 2, 2} // duplicates are deduped, not rejected
	if err := validateTaskLinks(ts, b); err != nil {
		t.Errorf("duplicate blockers: %v", err)
	}
	cyc := mk(1, 0, "V", "pending", "")
	cyc.BlockedBy = []int64{2} // 2 is blocked by 1
	wantCode(t, validateTaskLinks(ts, cyc), bus.CodeValidation)
	many := mk(1000, 0, "V", "pending", "")
	big := make([]task, 0, 65)
	for i := int64(1); i <= 65; i++ {
		big = append(big, mk(i, 0, "V", "pending", ""))
		many.BlockedBy = append(many.BlockedBy, i)
	}
	wantCode(t, validateTaskLinks(big, many), bus.CodeValidation) // 65 blockers
	many.BlockedBy = many.BlockedBy[:64]
	if err := validateTaskLinks(big, many); err != nil {
		t.Errorf("64 blockers: %v", err)
	}
}

func TestValidateTaskSubject(t *testing.T) {
	for _, s := range []string{"", strings.Repeat("x", 257), "a\nb", "a\x00b"} {
		wantCode(t, validateTaskSubject(s), bus.CodeValidation)
	}
	for _, s := range []string{"x", strings.Repeat("x", 256), "héllo"} {
		if err := validateTaskSubject(s); err != nil {
			t.Errorf("%q: %v", s, err)
		}
	}
}

func str(s string) *string { return &s }
func i64(n int64) *int64   { return &n }

func owners(known ...string) func(string) (bool, error) {
	m := map[string]bool{}
	for _, k := range known {
		m[k] = true
	}
	return func(o string) (bool, error) { return m[o], nil }
}

func TestApplyPatch(t *testing.T) {
	const now = int64(1_000_000)
	base := func() []task {
		return []task{
			mk(1, 0, "V", "pending", ""),
			mk(2, 0, "W", "in_progress", "pat"),
			mk(3, 2, "V", "pending", ""),
			mk(4, 0, "X", "completed", "pat"),
		}
	}
	type tc struct {
		name string
		cur  int64
		p    bus.TaskPatch
		as   string
		code bus.Code // "" means success
		same bool     // success with no change
		chk  func(t *testing.T, got task)
	}
	cases := []tc{
		{name: "rule 1 delete with other changes", cur: 1, p: bus.TaskPatch{Delete: true, Subject: str("x")}, as: "pat", code: bus.CodeValidation},
		{name: "no owner guard: a non-owner changes an in_progress task", cur: 2, p: bus.TaskPatch{Description: str("d")}, as: "kim", chk: func(t *testing.T, g task) {
			if g.Description != "d" || g.Owner != "pat" {
				t.Errorf("got %+v", g)
			}
		}},
		{name: "rule 3 taking ownership of a pending task", cur: 1, p: bus.TaskPatch{Owner: str("kim")}, as: "pat", chk: func(t *testing.T, g task) {
			if g.Owner != "kim" {
				t.Errorf("owner %q", g.Owner)
			}
		}},
		{name: "rule 4 delete with children", cur: 2, p: bus.TaskPatch{Delete: true}, as: "pat", code: bus.CodeConflict},
		{name: "rule 4 delete with children by a non-owner", cur: 2, p: bus.TaskPatch{Delete: true}, as: "kim", code: bus.CodeConflict},
		{name: "rule 4 delete leaf", cur: 1, p: bus.TaskPatch{Delete: true}, as: "pat", same: true},
		{name: "rule 5 subject invalid", cur: 1, p: bus.TaskPatch{Subject: str("")}, as: "pat", code: bus.CodeValidation},
		{name: "rule 5 metadata merge and delete", cur: 1, p: bus.TaskPatch{Metadata: map[string]string{"a": "1", "b": ""}}, as: "pat", chk: func(t *testing.T, g task) {
			if !reflect.DeepEqual(g.Metadata, map[string]string{"a": "1"}) {
				t.Errorf("metadata %v", g.Metadata)
			}
		}},
		{name: "rule 6 unknown owner", cur: 1, p: bus.TaskPatch{Owner: str("ghost")}, as: "pat", code: bus.CodeNotFound},
		{name: "rule 7 unknown status", cur: 1, p: bus.TaskPatch{Status: str("paused")}, as: "pat", code: bus.CodeValidation},
		{name: "rule 7 completed to in_progress", cur: 4, p: bus.TaskPatch{Status: str("in_progress"), Owner: str("pat")}, as: "pat", code: bus.CodeValidation},
		{name: "rule 7 claim", cur: 1, p: bus.TaskPatch{Status: str("in_progress"), Owner: str("pat"), LeasedUntil: i64(now + 5)}, as: "pat", chk: func(t *testing.T, g task) {
			if g.Status != "in_progress" || g.Owner != "pat" || g.LeasedUntil != now+5 {
				t.Errorf("got %+v", g)
			}
		}},
		{name: "rule 7 release clears owner", cur: 2, p: bus.TaskPatch{Status: str("pending")}, as: "pat", chk: func(t *testing.T, g task) {
			if g.Status != "pending" || g.Owner != "" {
				t.Errorf("got %+v", g)
			}
		}},
		{name: "rule 7 complete defaults owner to caller", cur: 1, p: bus.TaskPatch{Status: str("completed")}, as: "pat", chk: func(t *testing.T, g task) {
			if g.Owner != "pat" {
				t.Errorf("owner %q", g.Owner)
			}
		}},
		{name: "rule 7 in_progress needs owner", cur: 1, p: bus.TaskPatch{Status: str("in_progress")}, as: "pat", code: bus.CodeValidation},
		{name: "rule 8 lease in the past", cur: 1, p: bus.TaskPatch{Status: str("in_progress"), Owner: str("pat"), LeasedUntil: i64(now)}, as: "pat", code: bus.CodeValidation},
		{name: "rule 8 lease cleared when not in_progress", cur: 1, p: bus.TaskPatch{LeasedUntil: i64(now + 9)}, as: "pat", same: true},
		{name: "rule 9 parent cycle", cur: 2, p: bus.TaskPatch{Parent: i64(3)}, as: "pat", code: bus.CodeValidation},
		{name: "rule 10 move recomputes rank", cur: 1, p: bus.TaskPatch{After: 4}, as: "pat", chk: func(t *testing.T, g task) {
			if g.Rank <= "X" {
				t.Errorf("rank %q", g.Rank)
			}
		}},
		{name: "rule 10 before and after together", cur: 1, p: bus.TaskPatch{Before: 4, After: 2}, as: "pat", code: bus.CodeValidation},
		{name: "rule 11 no-op", cur: 1, p: bus.TaskPatch{Subject: str("t")}, as: "pat", same: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ts := base()
			fillDerived(ts)
			cur := *taskByID(ts, c.cur)
			got, err := applyPatch(ts, cur, c.p, c.as, now, owners("pat", "kim"))
			if c.code != "" {
				wantCode(t, err, c.code)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if c.same && !reflect.DeepEqual(got, cur) {
				t.Fatalf("expected unchanged, got %+v", got)
			}
			if c.chk != nil {
				c.chk(t, got)
			}
		})
	}
}

func TestApplyPatchBlockedTask(t *testing.T) {
	ts := []task{mk(1, 0, "V", "pending", ""), mk(2, 0, "W", "pending", "")}
	ts[1].BlockedBy = []int64{1}
	fillDerived(ts)
	_, err := applyPatch(ts, ts[1], bus.TaskPatch{Status: str("in_progress"), Owner: str("pat")}, "pat", 1, owners("pat"))
	wantCode(t, err, bus.CodeConflict)
	// Renewing an already in_progress task ignores a reopened blocker.
	ts[1].Status, ts[1].Owner = "in_progress", "pat"
	fillDerived(ts)
	if _, err := applyPatch(ts, ts[1], bus.TaskPatch{Status: str("in_progress"), LeasedUntil: i64(100)}, "pat", 1, owners("pat")); err != nil {
		t.Fatalf("renewal: %v", err)
	}
}

func TestApplyPatchPrunesDeadBlockers(t *testing.T) {
	ts := []task{mk(1, 0, "V", "pending", "")}
	ts[0].BlockedBy = []int64{42}
	got, err := applyPatch(ts, ts[0], bus.TaskPatch{Description: str("d")}, "pat", 1, owners())
	if err != nil || len(got.BlockedBy) != 0 {
		t.Fatalf("got %+v, %v; want the dead blocker pruned", got.BlockedBy, err)
	}
}

func TestLeaseExpired(t *testing.T) {
	now := int64(1000)
	ts := []task{mk(1, 0, "V", "in_progress", "pat"), mk(2, 0, "W", "in_progress", "pat"), mk(3, 0, "X", "pending", ""), mk(4, 0, "Y", "in_progress", "pat")}
	ts[0].LeasedUntil = now
	ts[1].LeasedUntil = now + 1
	ts[2].LeasedUntil = now - 5
	if !leaseExpired(ts[0], now) || leaseExpired(ts[1], now) || leaseExpired(ts[2], now) || leaseExpired(ts[3], now) {
		t.Fatal("a lease expires at its instant; no lease and non-in_progress tasks never lease-expire")
	}
}

func TestDedupeIDs(t *testing.T) {
	if got := dedupeIDs([]int64{3, 1, 3, 2, 1}); !reflect.DeepEqual(got, []int64{3, 1, 2}) {
		t.Fatalf("got %v", got)
	}
	if dedupeIDs(nil) != nil {
		t.Fatal("nil in, nil out")
	}
}
