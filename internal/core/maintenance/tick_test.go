package maintenance

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/store/sqlite"
)

const startMs = 1_700_000_000_000

type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// twoCores opens two cores over one SQLite file sharing one fake clock.
func twoCores(t *testing.T) (a, b Service, clk *testClock) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agentbus2.db")
	clk = &testClock{t: time.UnixMilli(startMs)}
	mk := func() Service {
		s, err := sqlite.Open(context.Background(), path, sqlite.Options{})
		if err != nil {
			t.Fatalf("sqlite.Open: %v", err)
		}
		t.Cleanup(func() { _ = s.Close() })
		c, err := core.New(s, core.Defaults(), core.Options{Now: clk.now})
		if err != nil {
			t.Fatalf("core.New: %v", err)
		}
		return New(c)
	}
	return mk(), mk(), clk
}

func TestTickLockHeldAcrossTicks(t *testing.T) {
	a, b, clk := twoCores(t)
	ctx := context.Background()
	interval := time.Duration(a.Cfg.MaintenanceIntervalSeconds) * time.Second
	if ran, _ := a.Tick(ctx); !ran {
		t.Fatal("first Tick did not run")
	}
	if ran, rep := b.Tick(ctx); ran || rep != nil {
		t.Fatalf("second core ran while the first holds the lock: %v %v", ran, rep)
	}
	// The holder renews; the lock is not released between ticks.
	clk.advance(interval)
	if ran, _ := a.Tick(ctx); !ran {
		t.Fatal("holder could not renew")
	}
	if ran, _ := b.Tick(ctx); ran {
		t.Fatal("second core ran after the holder's renewal")
	}
	clk.advance(2*interval - time.Second)
	if ran, _ := b.Tick(ctx); ran {
		t.Fatal("second core ran before the renewed lock expired")
	}
	clk.advance(2 * time.Second)
	if ran, _ := b.Tick(ctx); !ran {
		t.Fatal("second core did not take the expired lock")
	}
	if ran, _ := a.Tick(ctx); ran {
		t.Fatal("first core ran while the second holds the lock")
	}

	// A fresh pair: after Release the other core takes it at once.
	a, b, _ = twoCores(t)
	if ran, _ := a.Tick(ctx); !ran {
		t.Fatal("fresh pair: first Tick did not run")
	}
	if err := a.Release(ctx); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if err := a.Release(ctx); err != nil {
		t.Fatalf("second Release: %v", err)
	}
	if ran, _ := b.Tick(ctx); !ran {
		t.Fatal("second core did not take the released lock")
	}
}

func TestTickRunsBudgetJobsInOrder(t *testing.T) {
	a, _, _ := twoCores(t)
	ran, rep := a.Tick(context.Background())
	if !ran {
		t.Fatal("Tick did not run")
	}
	idx := map[string]int{}
	for i, r := range rep {
		idx[r.Name] = i
	}
	for _, n := range []string{"memory expiry", "budget eviction", "capacity notices", "embeddings"} {
		if _, ok := idx[n]; !ok {
			t.Fatalf("report lacks %q: %+v", n, rep)
		}
	}
	if idx["budget eviction"] <= idx["memory expiry"] || idx["capacity notices"] != idx["budget eviction"]+1 || idx["embeddings"] <= idx["capacity notices"] {
		t.Fatalf("job order wrong: %+v", idx)
	}
	for _, r := range rep {
		if r.Err != nil {
			t.Fatalf("job %s failed: %v", r.Name, r.Err)
		}
	}
}

func TestTickRunsEveryJobOnce(t *testing.T) {
	a, _, _ := twoCores(t)
	counts := map[string]int{}
	names := []string{"one", "two", "three"}
	for _, n := range names {
		a.jobs = append(a.jobs, Job{Name: n, Run: func(context.Context, int64) (int, error) {
			counts[n]++
			return 7, nil
		}})
	}
	ran, rep := a.Tick(context.Background())
	if !ran || len(rep) != len(names) {
		t.Fatalf("ran=%v report=%v", ran, rep)
	}
	for i, n := range names {
		if counts[n] != 1 || rep[i].Name != n || rep[i].N != 7 || rep[i].Skipped || rep[i].Err != nil {
			t.Fatalf("job %s: count %d, result %+v", n, counts[n], rep[i])
		}
	}
}

func TestTickDeadlineOnClock(t *testing.T) {
	a, _, clk := twoCores(t)
	ran2 := false
	a.jobs = []Job{
		{Name: "slow", Run: func(context.Context, int64) (int, error) {
			clk.advance(time.Duration(a.Cfg.MaintenanceIntervalSeconds)*time.Second/2 + time.Millisecond)
			return 1, nil
		}},
		{Name: "late", Run: func(context.Context, int64) (int, error) { ran2 = true; return 1, nil }},
	}
	start := time.Now()
	ran, rep := a.Tick(context.Background())
	if !ran || len(rep) != 2 {
		t.Fatalf("ran=%v report=%v", ran, rep)
	}
	if rep[0].Skipped || !rep[1].Skipped || ran2 {
		t.Fatalf("report %+v, late job ran %v; want slow run and late skipped", rep, ran2)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("deadline test slept on the wall clock")
	}
}

func TestTickJobErrorDoesNotAbort(t *testing.T) {
	a, _, _ := twoCores(t)
	a.jobs = []Job{
		{Name: "bad", Run: func(context.Context, int64) (int, error) { return 0, context.Canceled }},
		{Name: "good", Run: func(context.Context, int64) (int, error) { return 3, nil }},
	}
	_, rep := a.Tick(context.Background())
	if rep[0].Err == nil || rep[1].N != 3 || rep[1].Skipped {
		t.Fatalf("report %+v", rep)
	}
}

func TestTickRecordsLastTick(t *testing.T) {
	ctx := context.Background()
	a, b, _ := twoCores(t)
	if _, ok := a.LastMaintenanceTick(); ok {
		t.Fatal("a record exists before any tick")
	}
	if ran, _ := a.Tick(ctx); !ran {
		t.Fatal("a did not tick")
	}
	ra, ok := a.LastMaintenanceTick()
	if !ok || !ra.Held || ra.At != startMs || ra.LockErr != "" || len(ra.Jobs) != len(a.Jobs()) {
		t.Fatalf("a record = %+v, %v", ra, ok)
	}
	for _, j := range ra.Jobs {
		if j.Err != "" || j.Skipped {
			t.Fatalf("job %+v", j)
		}
	}
	if ran, _ := b.Tick(ctx); ran {
		t.Fatal("b ticked while a holds the lock")
	}
	rb, ok := b.LastMaintenanceTick()
	if !ok || rb.Held || rb.LockErr != "" || len(rb.Jobs) != 0 || rb.At != startMs {
		t.Fatalf("b record = %+v, %v", rb, ok)
	}
	if again, _ := a.LastMaintenanceTick(); len(again.Jobs) != len(ra.Jobs) || !again.Held {
		t.Fatalf("a record changed by b's tick: %+v", again)
	}

	a.jobs = []Job{
		{Name: "bad", Run: func(context.Context, int64) (int, error) { return 2, context.Canceled }},
		{Name: "good", Run: func(context.Context, int64) (int, error) { return 3, nil }},
	}
	a.Tick(ctx)
	rj, _ := a.LastMaintenanceTick()
	if len(rj.Jobs) != 2 || rj.Jobs[0].Name != "bad" || rj.Jobs[0].Changed != 2 || rj.Jobs[0].Err != context.Canceled.Error() ||
		rj.Jobs[1].Name != "good" || rj.Jobs[1].Changed != 3 || rj.Jobs[1].Err != "" {
		t.Fatalf("job record = %+v", rj.Jobs)
	}

	// RunJob is the operator command, not a tick: it records nothing.
	before, _ := a.LastMaintenanceTick()
	if _, err := a.RunJob(ctx, "good"); err != nil {
		t.Fatal(err)
	}
	if after, _ := a.LastMaintenanceTick(); after.At != before.At || len(after.Jobs) != len(before.Jobs) {
		t.Fatalf("RunJob changed the record: %+v", after)
	}
}

func TestTickRecordsLockError(t *testing.T) {
	a, _, _ := twoCores(t)
	if err := a.Store.Close(); err != nil {
		t.Fatal(err)
	}
	if ran, _ := a.Tick(context.Background()); ran {
		t.Fatal("ticked on a closed store")
	}
	r, ok := a.LastMaintenanceTick()
	if !ok || r.Held || r.LockErr == "" || len(r.Jobs) != 0 {
		t.Fatalf("record = %+v, %v", r, ok)
	}
}
