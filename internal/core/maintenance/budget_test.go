package maintenance

import (
	"bytes"
	"context"
	"database/sql"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/tasks"
	"github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/store/sqlite"
)

const hour = int64(time.Hour / time.Millisecond)

// bfx is a SQLite core with a fake clock, a raw handle and a fixed fake
// measure, so a test determines the allowance (usage less the target) and only
// the counted bytes stop a sweep.
type bfx struct {
	t    *testing.T
	svc  Service
	clk  *testClock
	db   *sql.DB
	logs *syncBuf
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func newBfx(t *testing.T, mut func(*core.Config)) *bfx {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agentbus2.db")
	clk := &testClock{t: time.UnixMilli(startMs)}
	st, err := sqlite.Open(context.Background(), path, sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cfg := core.Defaults()
	if mut != nil {
		mut(&cfg)
	}
	logs := &syncBuf{}
	c, err := core.New(st, cfg, core.Options{Now: clk.now, Logger: slog.New(slog.NewTextHandler(logs, nil))})
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	f := &bfx{t: t, svc: New(c), clk: clk, db: db, logs: logs}
	f.mustTx(func(ctx context.Context, tx store.Tx) error { return tx.CreateIdentity(ctx, "pat", startMs) })
	return f
}

// measure fixes the fake measure at x bytes (disk 2x, logical).
func (f *bfx) measure(x int64) {
	f.svc.usage = func(context.Context) (store.Usage, error) {
		return store.Usage{Bytes: x, DiskBytes: 2 * x, Logical: true}, nil
	}
}

func (f *bfx) mustTx(fn func(ctx context.Context, tx store.Tx) error) {
	f.t.Helper()
	if err := f.svc.Store.Tx(context.Background(), fn); err != nil {
		f.t.Fatalf("tx: %v", err)
	}
}

func (f *bfx) mustAppend(fn func(ctx context.Context, tx store.Tx) error) {
	f.t.Helper()
	if err := f.svc.Store.Append(context.Background(), fn); err != nil {
		f.t.Fatalf("append: %v", err)
	}
}

func (f *bfx) now() int64 { return f.clk.now().UnixMilli() }

// channel creates a channel created ageH hours ago.
func (f *bfx) channel(name string, kind store.Kind, ageH int64) store.Channel {
	f.t.Helper()
	var ch store.Channel
	f.mustTx(func(ctx context.Context, tx store.Tx) error {
		seq, err := tx.MaxSeq(ctx)
		if err != nil {
			return err
		}
		ch, err = tx.CreateChannel(ctx, store.ChannelCreate{Name: name, Kind: kind, Creator: "pat", CreatedSeq: seq, CreatedAt: f.now() - ageH*hour})
		return err
	})
	return ch
}

// msg appends an ordinary message of n bytes of content created ageMs ago.
func (f *bfx) msg(ch store.Channel, n int, ageMs int64) int64 {
	f.t.Helper()
	var seq int64
	f.mustAppend(func(ctx context.Context, tx store.Tx) error {
		var err error
		seq, err = tx.AppendMessage(ctx, store.NewMessage{ChannelID: ch.ID, Sender: "pat", CreatedAt: f.now() - ageMs, Content: strings.Repeat("x", n)})
		return err
	})
	return seq
}

// item appends revision 1 of memory (or task) id on ch, ageMs old.
func (f *bfx) item(ch store.Channel, id int64, n int, ageMs int64) {
	f.t.Helper()
	f.mustAppend(func(ctx context.Context, tx store.Tx) error {
		rev := int64(1)
		_, err := tx.AppendMessage(ctx, store.NewMessage{ChannelID: ch.ID, Sender: "pat", CreatedAt: f.now() - ageMs,
			Content: strings.Repeat("x", n), MemoryID: &id, Revision: &rev})
		return err
	})
}

func (f *bfx) count(table, where string) int {
	f.t.Helper()
	q := "SELECT COUNT(*) FROM " + table
	if where != "" {
		q += " WHERE " + where
	}
	var n int
	if err := f.db.QueryRow(q).Scan(&n); err != nil {
		f.t.Fatalf("%s: %v", q, err)
	}
	return n
}

func (f *bfx) run(name string) int {
	f.t.Helper()
	n, err := f.svc.RunJob(context.Background(), name)
	if err != nil {
		f.t.Fatalf("RunJob(%q): %v", name, err)
	}
	return n
}

func (f *bfx) notice() (store.Notice, bool) {
	f.t.Helper()
	var n store.Notice
	var ok bool
	if err := f.svc.Store.Read(context.Background(), func(ctx context.Context, tx store.Tx) error {
		var err error
		n, ok, err = tx.Notice(ctx, "capacity")
		return err
	}); err != nil {
		f.t.Fatal(err)
	}
	return n, ok
}

func TestEvictionOrder(t *testing.T) {
	f := newBfx(t, nil)
	f.svc.BudgetOverride = 4096 // target 3072
	f.measure(4097)             // allowance 1025
	dev := f.channel("dev", store.KindOrdinary, 300)
	var br store.Bridge
	f.mustTx(func(ctx context.Context, tx store.Tx) error {
		if err := tx.CreateScope(ctx, "job1", 1); err != nil {
			return err
		}
		var err error
		if br, err = tx.CreateBridge(ctx, store.Bridge{ChannelID: dev.ID, ChannelName: "dev", Scope: "job1", SlackName: "ab-dev", CreatedAt: 5}); err != nil {
			return err
		}
		return tx.PutBridgeMessage(ctx, store.BridgeMessage{BridgeID: br.ID, Seq: 999_999, SlackTS: "1700000000.000001", RootTS: "1700000000.000001"})
	})
	tomb := f.msg(dev, 2048, 73*hour)
	f.mustTx(func(ctx context.Context, tx store.Tx) error {
		_, err := tx.TombstoneMessage(ctx, tomb, f.now()-73*hour)
		return err
	})
	f.mustTx(func(ctx context.Context, tx store.Tx) error {
		return tx.PutEnrollmentToken(ctx, store.EnrollmentToken{ID: "tok1", TokenHash: "hash1", ExpiresAt: f.now() - 1000, UsesLeft: 1, CreatedAt: 1})
	})
	list := f.channel("work", store.KindTasks, 200)
	f.item(list, 1, 2048, 169*hour)
	mem := f.channel("mem", store.KindMemory, 200)
	f.item(mem, 2, 3072, 169*hour)
	for range 3 {
		f.msg(dev, 40, 2*hour)
	}

	type state struct{ orphan, tomb, token, list, mem, msgs int }
	read := func() state {
		return state{
			f.count("bridge_messages", ""), f.count("messages", "seq = "+itoa(tomb)), f.count("enrollment_tokens", ""),
			f.count("channels", "name = 'work'"), f.count("messages", "memory_id = 2"), f.count("messages", "content LIKE 'xxxx%' AND length(content) = 40"),
		}
	}
	if s := read(); s != (state{1, 1, 1, 1, 1, 3}) {
		t.Fatalf("seeded state %+v", s)
	}
	runs := []struct {
		deleted int
		want    state
	}{
		{2, state{0, 0, 1, 1, 1, 3}},
		{2, state{0, 0, 0, 0, 1, 3}},
		{1, state{0, 0, 0, 0, 0, 3}},
		{3, state{0, 0, 0, 0, 0, 0}},
		{0, state{0, 0, 0, 0, 0, 0}},
	}
	for i, r := range runs {
		if n := f.run("budget eviction"); n != r.deleted {
			t.Fatalf("run %d deleted %d, want %d", i+1, n, r.deleted)
		}
		if s := read(); s != r.want {
			t.Fatalf("run %d state %+v, want %+v", i+1, s, r.want)
		}
	}
	if n := f.run("capacity notices"); n != 1 {
		t.Fatalf("capacity notices = %d", n)
	}
	u := store.Usage{Bytes: 4097, DiskBytes: 2 * 4097}
	n, ok := f.notice()
	if !ok || n.Message != exhaustedText("sqlite_budget_mib", 4096, u) || n.SetAt != f.now() {
		t.Fatalf("notice = %+v, %v", n, ok)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestEvictionNeverFallingMeasure(t *testing.T) {
	f := newBfx(t, nil)
	f.svc.BudgetOverride = 4 << 20
	f.measure(5 << 20) // allowance 2 MiB (target 3 MiB)
	allowance := int64(5<<20) - core.TargetBytes(4<<20, f.svc.Cfg.CleanupFreePercent)
	dev := f.channel("dev", store.KindOrdinary, 10)
	for range 3000 {
		f.msg(dev, 1024, 2*hour)
	}
	var first []store.SeqChannel
	f.mustTx(func(ctx context.Context, tx store.Tx) error {
		var err error
		first, err = tx.OrdinaryMessagesBefore(ctx, f.now(), cleanupChunk)
		return err
	})
	var chunk int64
	for _, r := range first {
		chunk += r.Bytes
	}
	before := f.count("messages", "")
	f.run("budget eviction")
	deleted := int64(before-f.count("messages", "")) * (chunk / int64(len(first)))
	if deleted < allowance || deleted > allowance+chunk {
		t.Fatalf("deleted %d bytes, want [%d, %d]", deleted, allowance, allowance+chunk)
	}
	if !strings.Contains(f.logs.String(), "budget eviction did not lower usage") {
		t.Fatalf("no warning in log:\n%s", f.logs.String())
	}
}

func TestEvictionFloors(t *testing.T) {
	f := newBfx(t, nil)
	f.svc.BudgetOverride = 4096
	f.measure(1 << 30)
	dev := f.channel("dev", store.KindOrdinary, 10)
	for range 5 {
		f.msg(dev, 100, 59*60*1000)
	}
	tomb := f.msg(dev, 100, 71*hour)
	f.mustTx(func(ctx context.Context, tx store.Tx) error {
		_, err := tx.TombstoneMessage(ctx, tomb, f.now()-71*hour)
		return err
	})
	// The tombstone's message is itself an ordinary message older than the
	// floor; only the tombstone floor protects it from step 2, and step 4
	// would take it: age it inside the message floor so only step 2 applies.
	if _, err := f.db.Exec("UPDATE messages SET created_at = ? WHERE seq = ?", f.now()-59*60*1000, tomb); err != nil {
		t.Fatal(err)
	}
	if n := f.run("budget eviction"); n != 0 {
		t.Fatalf("deleted %d under the floors", n)
	}
	if got := f.count("messages", ""); got != 6 {
		t.Fatalf("%d messages remain, want 6", got)
	}
	if n := f.run("capacity notices"); n != 1 {
		t.Fatalf("capacity notices = %d", n)
	}
	n, _ := f.notice()
	if !strings.HasPrefix(n.Message, "agentbus2 capacity exhausted: ") {
		t.Fatalf("notice = %q", n.Message)
	}
}

func TestEvictionKeepsActiveTaskChannel(t *testing.T) {
	f := newBfx(t, nil)
	f.svc.BudgetOverride = 4096
	f.measure(1 << 30)
	changed := f.channel("changed", store.KindTasks, 200)
	f.item(changed, 1, 100, 100*hour)
	_ = f.channel("read", store.KindTasks, 200)
	idle := f.channel("idle", store.KindTasks, 200)
	f.item(idle, 2, 100, 169*hour)
	plain := f.channel("plain", store.KindOrdinary, 300)
	f.msg(plain, 10, 30*60*1000)

	f.clk.advance(-100 * time.Hour)
	if _, err := (tasks.Service{Core: f.svc.Core}).List(context.Background(), "pat", bus.TaskListInput{Channel: "read"}); err != nil {
		t.Fatal(err)
	}
	f.clk.advance(100 * time.Hour)

	tables := []string{"identities", "identity_keys", "sessions", "bridges", "retired_identities", "notices",
		"enrollment_tokens", "channels WHERE kind = 'ordinary'"}
	count := func() []int {
		var out []int
		for _, tb := range tables {
			q := tb
			if i := strings.Index(tb, " WHERE "); i >= 0 {
				out = append(out, f.count(tb[:i], tb[i+7:]))
			} else {
				out = append(out, f.count(q, ""))
			}
		}
		return out
	}
	before := count()
	if n := f.run("budget eviction"); n != 1 {
		t.Fatalf("deleted %d, want only the idle list", n)
	}
	for name, want := range map[string]int{"changed": 1, "read": 1, "idle": 0} {
		if got := f.count("channels", "name = '"+name+"'"); got != want {
			t.Errorf("channel %s: %d rows, want %d", name, got, want)
		}
	}
	after := count()
	for i := range before {
		if before[i] != after[i] {
			t.Errorf("%s: %d rows before, %d after", tables[i], before[i], after[i])
		}
	}
}

// open note: postgres-budget 2
func TestEvictionIgnoresZeroExpiry(t *testing.T) {
	f := newBfx(t, func(c *core.Config) { c.TaskExpiryHours, c.MemoryExpiryHours = 0, 0 })
	f.svc.BudgetOverride = 4096
	f.measure(1 << 30)
	list := f.channel("work", store.KindTasks, 200)
	f.item(list, 1, 100, 169*hour)
	mem := f.channel("mem", store.KindMemory, 200)
	f.item(mem, 2, 100, 169*hour)
	if n := f.run("budget eviction"); n != 2 {
		t.Fatalf("deleted %d, want 2", n)
	}
	if f.count("channels", "name = 'work'") != 0 || f.count("messages", "memory_id = 2") != 0 {
		t.Fatal("an idle list or memory survived with the age rules off")
	}
}

func TestEvictionHysteresis(t *testing.T) {
	for _, x := range []int64{3500, 4096} { // between target (3072) and budget, and at the budget
		f := newBfx(t, nil)
		f.svc.BudgetOverride = 4096
		calls := 0
		f.svc.usage = func(context.Context) (store.Usage, error) {
			calls++
			return store.Usage{Bytes: x, DiskBytes: x, Logical: true}, nil
		}
		dev := f.channel("dev", store.KindOrdinary, 10)
		f.msg(dev, 100, 5*hour)
		if n := f.run("budget eviction"); n != 0 {
			t.Fatalf("x=%d: deleted %d", x, n)
		}
		if calls != 1 || f.count("messages", "") != 1 {
			t.Fatalf("x=%d: %d usage reads, %d messages", x, calls, f.count("messages", ""))
		}
	}
}

func TestTickSweepFeedsNotice(t *testing.T) {
	for _, tc := range []struct {
		x    int64
		want int
	}{{5000, 2}, {4000, 1}} {
		f := newBfx(t, nil)
		f.svc.BudgetOverride = 4096
		calls := 0
		f.svc.usage = func(context.Context) (store.Usage, error) {
			calls++
			return store.Usage{Bytes: tc.x, DiskBytes: tc.x, Logical: true}, nil
		}
		f.svc.jobs = f.svc.Jobs()
		ran, rep := f.svc.Tick(context.Background())
		if !ran {
			t.Fatal("Tick did not run")
		}
		for _, r := range rep {
			if r.Err != nil {
				t.Fatalf("job %s: %v", r.Name, r.Err)
			}
		}
		if calls != tc.want {
			t.Fatalf("x=%d: Tick read usage %d times, want %d", tc.x, calls, tc.want)
		}
		calls = 0
		f.run("capacity notices")
		if calls != 1 {
			t.Fatalf("x=%d: capacity notices alone read usage %d times, want 1", tc.x, calls)
		}
	}
}

func TestCapacityTexts(t *testing.T) {
	u := store.Usage{Bytes: 3000 << 20, DiskBytes: 6000 << 20}
	over := overBudgetText("postgres_budget_mib", 2048<<20, u)
	ex := exhaustedText("postgres_budget_mib", 2048<<20, u)
	wantOver := "agentbus2 storage is over budget: postgres_budget_mib=2048 MiB holds 3000 MiB (disk 6000 MiB, 2.00x); orphaned and expired rows, idle task lists and memories, and ordinary messages older than 1 hour are being deleted to make room"
	wantEx := "agentbus2 capacity exhausted: postgres_budget_mib=2048 MiB (disk 6000 MiB, 2.00x) is filled by task lists and memories active within idle_window_hours, ordinary messages younger than 1 hour and tombstones younger than 72 hours, which are never evicted; raise postgres_budget_mib and restart every replica, lower idle_window_hours, or have an administrator reset the bus to discard all data"
	if over != wantOver || ex != wantEx {
		t.Fatalf("texts:\n%s\n%s", over, ex)
	}
	for _, s := range []string{over, ex} {
		for _, bad := range []string{"dm/", "-filter", "@<", "memory/", "tasks/", "general/"} {
			if strings.Contains(s, bad) {
				t.Errorf("%q contains %q", s, bad)
			}
		}
	}
	if !strings.Contains(overBudgetText("x", 1<<20, store.Usage{DiskBytes: 5}), "0.00x") {
		t.Error("zero bytes must give ratio 0, not a division by zero")
	}
}

// Idle task lists honor the allowance: it ends after the first of several
// lists, so one tick deletes one list, and the next tick the next.
func TestEvictionIdleListsHonorAllowance(t *testing.T) {
	f := newBfx(t, nil)
	f.svc.BudgetOverride = 4096
	f.measure(4097) // allowance 1025
	for _, name := range []string{"a", "b", "c"} {
		ch := f.channel(name, store.KindTasks, 200)
		f.item(ch, int64(len(name))+int64(name[0]), 2048, 169*hour)
	}
	for i, want := range []int{1, 2, 3} {
		if n := f.run("budget eviction"); n != 1 {
			t.Fatalf("run %d deleted %d lists, want 1", i+1, n)
		}
		if got := 3 - f.count("channels", "kind = 'tasks'"); got != want {
			t.Fatalf("run %d: %d lists gone, want %d", i+1, got, want)
		}
	}
}

// Idle memories honor the allowance too: with three idle memories and an
// allowance of one, each tick purges one memory (the overshoot is one memory).
func TestEvictionIdleMemoriesHonorAllowance(t *testing.T) {
	f := newBfx(t, nil)
	f.svc.BudgetOverride = 4096
	f.measure(4097) // allowance 1025
	mem := f.channel("mem", store.KindMemory, 200)
	for id := int64(1); id <= 3; id++ {
		f.item(mem, id, 2048, 169*hour)
	}
	for i, want := range []int{1, 2, 3} {
		if n := f.run("budget eviction"); n != 1 {
			t.Fatalf("run %d purged %d memories, want 1", i+1, n)
		}
		if got := 3 - f.count("messages", "memory_id IS NOT NULL"); got != want {
			t.Fatalf("run %d: %d memories gone, want %d", i+1, got, want)
		}
	}
}

// Definition of Done D: a sweep stopped after its first chunk keeps that
// chunk, with its evicted_before_seq, and a later run continues.
func TestEvictionStoppedSweepKeepsChunks(t *testing.T) {
	f := newBfx(t, nil)
	f.svc.BudgetOverride = 4096
	dev := f.channel("dev", store.KindOrdinary, 10)
	var seqs []int64
	for range 2500 {
		seqs = append(seqs, f.msg(dev, 10, 2*hour))
	}
	calls := 0
	f.svc.usage = func(context.Context) (store.Usage, error) {
		calls++
		// Reads: the start, then one before each chunk. The five earlier steps
		// find nothing (one read each), so read 7 precedes the first ordinary
		// chunk, during which the deadline passes.
		if calls == 7 {
			f.clk.advance(time.Minute)
		}
		return store.Usage{Bytes: 1 << 30, DiskBytes: 1 << 30}, nil
	}
	ctx := context.WithValue(context.Background(), deadlineKey{}, f.now()+1000)
	n, err := f.svc.evictToBudget(ctx, f.now())
	if err != nil || n != cleanupChunk {
		t.Fatalf("stopped sweep deleted %d, %v; want exactly one chunk", n, err)
	}
	evicted := func() int64 {
		var ch store.Channel
		f.mustTx(func(ctx context.Context, tx store.Tx) error {
			var e error
			ch, e = tx.Channel(ctx, dev.ID)
			return e
		})
		return ch.EvictedBeforeSeq
	}
	if got, want := f.count("messages", ""), 1500; got != want {
		t.Fatalf("%d messages remain, want %d", got, want)
	}
	if got, want := evicted(), seqs[cleanupChunk-1]+1; got != want {
		t.Fatalf("EvictedBeforeSeq = %d, want %d", got, want)
	}
	f.measure(1 << 30)
	if n := f.run("budget eviction"); n != 1500 {
		t.Fatalf("later run deleted %d, want 1500", n)
	}
	if got, want := evicted(), seqs[len(seqs)-1]+1; got != want {
		t.Fatalf("EvictedBeforeSeq = %d, want %d", got, want)
	}
}
