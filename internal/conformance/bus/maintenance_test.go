package bus

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"time"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/channels"
	"github.com/ericfitz/agentbus2/internal/core/maintenance"
	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
)

func maint(h *harness) maintenance.Service { return maintenance.New(h.core) }

func hours(n int) time.Duration { return time.Duration(n) * time.Hour }

// runJob runs one job and fails the test on error.
func (h *harness) runJob(name string) int {
	h.t.Helper()
	n, err := maint(h).RunJob(context.Background(), name)
	if err != nil {
		h.t.Fatalf("RunJob(%q): %v", name, err)
	}
	return n
}

func (h *harness) liveSeqs(seqs ...int64) []int64 {
	h.t.Helper()
	var out []int64
	h.tx(func(ctx context.Context, tx ab2store.Tx) error {
		ms, err := tx.MessagesBySeqs(ctx, seqs)
		out = nil
		for _, m := range ms {
			out = append(out, m.Seq)
		}
		return err
	})
	return out
}

func sameSeqs(got []int64, want ...int64) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestMaintenanceLapsedSessions(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ch := h.channel("work", ab2store.KindOrdinary, "pat", nil)
		lapsed := h.session("kim", "lapsed")
		closed := h.session("kim", "closed")
		h.tx(func(ctx context.Context, tx ab2store.Tx) error {
			if err := tx.PutCursor(ctx, ab2store.Cursor{SessionID: lapsed.ID, ChannelID: ch.ID, CursorSeq: 1}); err != nil {
				return err
			}
			return tx.CloseSession(ctx, closed.ID, h.core.NowMs())
		})
		// Inside the retention window nothing goes, lapsed or closed.
		h.clock.Advance(hours(h.cfg.SessionRetentionHours) / 2)
		if n := h.runJob("lapsed sessions"); n != 0 {
			t.Fatalf("inside retention: deleted %d", n)
		}
		h.clock.Advance(hours(h.cfg.SessionRetentionHours)/2 + 10*time.Minute)
		live := h.session("kim", "live")
		if n := h.runJob("lapsed sessions"); n != 2 {
			t.Fatalf("past retention: deleted %d, want 2", n)
		}
		rows := sessionRows(h, "kim")
		if len(rows) != 1 || rows[0].ID != live.ID {
			t.Fatalf("sessions left = %+v, want only the live one", rows)
		}
		if cs := sessionCursors(h, lapsed.ID); len(cs) != 0 {
			t.Fatalf("cursors of the deleted session remain: %v", cs)
		}
		if n := h.runJob("lapsed sessions"); n != 0 {
			t.Fatalf("second run deleted %d", n)
		}
	})
}

func TestMaintenanceMessageRetention(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ord := h.channel("ord", ab2store.KindOrdinary, "pat", nil)
		mem := h.channel("mem", ab2store.KindMemory, "pat", nil)
		o1 := h.append(ord, "pat", "", "one")
		m1 := h.append(mem, "pat", "", "remember")
		o2 := h.append(ord, "pat", "", "two")
		h.clock.Advance(hours(h.cfg.MessageRetentionHours + 1))
		fresh := h.append(ord, "pat", "", "fresh")
		if n := h.runJob("message retention"); n != 2 {
			t.Fatalf("deleted %d, want 2", n)
		}
		if got := h.liveSeqs(o1, m1, o2, fresh); !sameSeqs(got, m1, fresh) {
			t.Fatalf("survivors = %v, want [%d %d]", got, m1, fresh)
		}
		var got ab2store.Channel
		h.tx(func(ctx context.Context, tx ab2store.Tx) error {
			var err error
			got, err = tx.Channel(ctx, ord.ID)
			return err
		})
		if got.EvictedBeforeSeq != o2+1 {
			t.Fatalf("EvictedBeforeSeq = %d, want %d", got.EvictedBeforeSeq, o2+1)
		}
		if n := h.runJob("message retention"); n != 0 {
			t.Fatalf("second run deleted %d", n)
		}
	})
}

func TestMaintenanceTombstonePurge(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ord := h.channel("ord", ab2store.KindOrdinary, "pat", nil)
		old := h.append(ord, "pat", "", "old")
		h.tx(func(ctx context.Context, tx ab2store.Tx) error {
			_, err := tx.TombstoneMessage(ctx, old, h.core.NowMs())
			return err
		})
		h.clock.Advance(hours(h.cfg.TombstoneMinHours) - time.Minute)
		if n := h.runJob("tombstone purge"); n != 0 {
			t.Fatalf("purged %d before the minimum age", n)
		}
		h.clock.Advance(2 * time.Minute)
		if n := h.runJob("tombstone purge"); n != 1 {
			t.Fatalf("purged %d, want 1", n)
		}
		var left []ab2store.SeqChannel
		h.tx(func(ctx context.Context, tx ab2store.Tx) error {
			var err error
			left, err = tx.ExpiredTombstones(ctx, h.core.NowMs()+1, 10)
			return err
		})
		if len(left) != 0 {
			t.Fatalf("tombstones left: %v", left)
		}
		if n := h.runJob("tombstone purge"); n != 0 {
			t.Fatalf("second run purged %d", n)
		}
	})
}

func TestMaintenanceReceipts(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		h.ensureIdentity("pat")
		now := h.core.NowMs()
		h.tx(func(ctx context.Context, tx ab2store.Tx) error {
			for _, r := range []ab2store.Receipt{
				{Identity: "pat", Key: "soon", Fingerprint: "f", Result: []byte("{}"), ExpiresAt: now + 1000},
				{Identity: "pat", Key: "later", Fingerprint: "f", Result: []byte("{}"), ExpiresAt: now + 3_600_000},
			} {
				if err := tx.PutReceipt(ctx, r); err != nil {
					return err
				}
			}
			return nil
		})
		if n := h.runJob("receipts"); n != 0 {
			t.Fatalf("deleted %d unexpired", n)
		}
		h.clock.Advance(2 * time.Second)
		if n := h.runJob("receipts"); n != 1 {
			t.Fatalf("deleted %d, want 1", n)
		}
		if n := h.runJob("receipts"); n != 0 {
			t.Fatalf("second run deleted %d", n)
		}
	})
}

func channelExists(h *harness, name string) bool {
	h.t.Helper()
	var ok bool
	h.tx(func(ctx context.Context, tx ab2store.Tx) error {
		_, err := tx.ChannelByName(ctx, name)
		if err != nil && !errors.Is(err, ab2store.ErrNotFound) {
			return err
		}
		ok = err == nil
		return nil
	})
	return ok
}

func TestMaintenanceTaskExpiry(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		for _, disabled := range []bool{false, true} {
			cfg := core.Defaults()
			if disabled {
				cfg.TaskExpiryHours = 0
			}
			h := newHarness(t, e, harnessOptions{cfg: &cfg})
			ctx := context.Background()
			pat := h.as("pat")
			h.channel("stale", ab2store.KindTasks, "pat", nil)
			if _, err := pat.Create(ctx, ab2bus.TaskCreateInput{Channel: "stale", Subject: "x"}); err != nil {
				t.Fatal(err)
			}
			h.clock.Advance(hours(720 + 1))
			h.channel("active", ab2store.KindTasks, "pat", nil)
			n := h.runJob("task expiry")
			if disabled {
				if n != 0 || !channelExists(h, "stale") {
					t.Fatalf("disabled: deleted %d, stale exists %v", n, channelExists(h, "stale"))
				}
				continue
			}
			if n != 1 || channelExists(h, "stale") || !channelExists(h, "active") {
				t.Fatalf("deleted %d; stale exists %v, active exists %v", n, channelExists(h, "stale"), channelExists(h, "active"))
			}
			if n := h.runJob("task expiry"); n != 0 {
				t.Fatalf("second run deleted %d", n)
			}
		}
	})
}

func TestMaintenanceMemoryExpiry(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		for _, disabled := range []bool{false, true} {
			cfg := core.Defaults()
			if disabled {
				cfg.MemoryExpiryHours = 0
			}
			h := newHarness(t, e, harnessOptions{cfg: &cfg})
			ctx := context.Background()
			mem := h.channel("mem", ab2store.KindMemory, "pat", nil)
			tl := h.channel("work", ab2store.KindTasks, "pat", nil)
			old1 := h.append(mem, "pat", "", "old one")
			old2 := h.append(mem, "pat", "", "old two")
			task, err := h.as("pat").Create(ctx, ab2bus.TaskCreateInput{Channel: tl.Name, Subject: "t"})
			if err != nil {
				t.Fatal(err)
			}
			h.clock.Advance(hours(cfg.MemoryExpiryHours + 1))
			fresh := h.append(mem, "pat", "", "fresh")
			n := h.runJob("memory expiry")
			live := h.liveSeqs(old1, old2, fresh)
			if disabled {
				if n != 0 || !sameSeqs(live, old1, old2, fresh) {
					t.Fatalf("disabled: n=%d live=%v", n, live)
				}
				continue
			}
			if n != 2 || !sameSeqs(live, fresh) {
				t.Fatalf("n=%d live=%v, want 2 and [%d]", n, live, fresh)
			}
			if got, err := h.as("pat").GetTask(ctx, task.ID); err != nil || got.Status != "pending" {
				t.Fatalf("task after memory expiry = %+v, %v", got, err)
			}
			if n := h.runJob("memory expiry"); n != 0 {
				t.Fatalf("second run expired %d", n)
			}
		}
	})
}

func TestMaintenanceAbandonedTasks(t *testing.T) {
	// Decision (Eric, 2026-10-21): Milestone 3 item 13: owner liveness is a live session row.
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		h.channel("work", ab2store.KindTasks, "pat", nil)
		pat, kim := h.as("pat"), h.as("kim")
		h.session("kim", "kim")
		made, err := pat.Create(ctx, ab2bus.TaskCreateInput{Channel: "work", Subject: "first"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := kim.Claim(ctx, ab2bus.TaskClaimInput{ID: made.ID}); err != nil {
			t.Fatal(err)
		}
		if n := h.runJob("abandoned tasks"); n != 0 {
			t.Fatalf("reclaimed %d with a live owner", n)
		}
		h.clock.Advance(time.Duration(h.cfg.SessionLeaseSeconds+h.cfg.LeaseSkewSeconds+1) * time.Second)
		if n := h.runJob("abandoned tasks"); n != 1 {
			t.Fatalf("reclaimed %d, want 1", n)
		}
		got, err := pat.GetTask(ctx, made.ID)
		if err != nil || got.Status != "pending" || got.Owner != "" {
			t.Fatalf("task = %+v, %v", got, err)
		}
		if n := h.runJob("abandoned tasks"); n != 0 {
			t.Fatalf("second run reclaimed %d", n)
		}
	})
}

func TestMaintenanceEmbeddings(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{embedder: true})
		mem := h.channel("mem", ab2store.KindMemory, "pat", nil)
		const total = 70 // more than one batch of 64, fewer than four
		for range total {
			h.append(mem, "pat", "", "alpha bravo")
		}
		if n := h.runJob("embeddings"); n != total {
			t.Fatalf("embedded %d, want %d", n, total)
		}
		if got := len(h.embeddedSeqs("fake")); got != total {
			t.Fatalf("%d embeddings stored, want %d", got, total)
		}
		if n := h.runJob("embeddings"); n != 0 {
			t.Fatalf("second run embedded %d", n)
		}
	})
}

func TestMaintenanceTickIdempotent(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		ord := h.channel("ord", ab2store.KindOrdinary, "pat", nil)
		h.session("kim", "kim")
		h.append(ord, "pat", "", "one")
		h.append(ord, "pat", "", "two")
		h.tx(func(ctx context.Context, tx ab2store.Tx) error {
			return tx.PutReceipt(ctx, ab2store.Receipt{Identity: "pat", Key: "k", Fingerprint: "f", Result: []byte("{}"), ExpiresAt: h.core.NowMs() + 60_000})
		})
		h.clock.Advance(hours(h.cfg.MessageRetentionHours + 1))
		// "ord" loses both messages to retention, then is empty and old in the same tick.
		want := map[string]int{"lapsed sessions": 1, "message retention": 2, "receipts": 1, "empty channels": 1}
		check := func(round string, want map[string]int) {
			t.Helper()
			ran, rep := maint(h).Tick(ctx)
			if !ran || len(rep) != len(maint(h).Jobs()) {
				t.Fatalf("%s: ran=%v report=%v", round, ran, rep)
			}
			for _, r := range rep {
				if r.Err != nil || r.Skipped || r.N != want[r.Name] {
					t.Fatalf("%s: job %q = %+v, want count %d", round, r.Name, r, want[r.Name])
				}
			}
		}
		check("first", want)
		h.clock.Advance(time.Duration(2*h.cfg.MaintenanceIntervalSeconds+1) * time.Second)
		check("second", nil)
	})
}

// Decision (Eric, 2026-10-21), Milestone 3 item 8 (v1 reapEmptyChannels).
func TestMaintenanceEmptyChannels(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		if err := (channels.Service{Core: h.core}).EnsureGeneral(ctx); err != nil {
			t.Fatal(err)
		}
		h.channel("empty", ab2store.KindOrdinary, "pat", nil)
		withMsg := h.channel("with-message", ab2store.KindOrdinary, "pat", nil)
		withSub := h.channel("with-subscription", ab2store.KindOrdinary, "pat", nil)
		withLive := h.channel("with-live-subscriber", ab2store.KindOrdinary, "pat", nil)
		h.channel("memory-empty", ab2store.KindMemory, "pat", nil)
		h.channel("tasks-empty", ab2store.KindTasks, "pat", nil)
		h.append(withMsg, "pat", "", "hello")
		h.ensureIdentity("kim")
		h.tx(func(ctx context.Context, tx ab2store.Tx) error {
			// kim has no live or resumable session, so the subscription does not
			// keep the channel (Decision 20).
			return tx.Subscribe(ctx, "kim", withSub.ID, 0, h.core.NowMs())
		})
		h.session("lee", "lee-s") // lee has an open session and subscribes to with-live-subscriber
		h.tx(func(ctx context.Context, tx ab2store.Tx) error {
			return tx.Subscribe(ctx, "lee", withLive.ID, 0, h.core.NowMs())
		})

		h.clock.Advance(hours(h.cfg.EmptyChannelReapHours) - time.Minute)
		h.channel("young", ab2store.KindOrdinary, "pat", nil)
		h.clock.Advance(2 * time.Minute)
		// "empty" is now N hours and a minute old; "young" is two minutes old.
		if n := h.runJob("empty channels"); n != 2 {
			t.Fatalf("reaped %d, want 2 (empty, with-subscription)", n)
		}
		for name, want := range map[string]bool{
			"empty": false, "with-message": true, "with-subscription": false, "with-live-subscriber": true, "memory-empty": true,
			"tasks-empty": true, "young": true, channels.GeneralChannel: true,
		} {
			if got := channelExists(h, name); got != want {
				t.Errorf("channel %q exists = %v, want %v", name, got, want)
			}
		}
		if n := h.runJob("empty channels"); n != 0 {
			t.Fatalf("second run reaped %d", n)
		}

		// Once young ages past N it goes too; general, empty and old, stays.
		h.clock.Advance(hours(h.cfg.EmptyChannelReapHours))
		if n := h.runJob("empty channels"); n != 1 {
			t.Fatalf("reaped %d, want 1 (young)", n)
		}
		if channelExists(h, "young") || !channelExists(h, channels.GeneralChannel) {
			t.Fatal("young should be gone and general kept")
		}
	})
}

func TestMaintenanceRunJobRejectsUnknown(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		for _, name := range []string{"vacuum", ""} {
			_, err := maint(h).RunJob(context.Background(), name)
			codeOf(t, err, ab2bus.CodeValidation)
			if !strings.Contains(err.Error(), "lapsed sessions") {
				t.Fatalf("error %q does not name the known jobs", err)
			}
		}
		for _, name := range []string{"budget eviction", "capacity notices"} {
			if _, err := maint(h).RunJob(context.Background(), name); err != nil {
				t.Fatalf("RunJob(%q): %v", name, err)
			}
		}
	})
}

// ordinaryRows returns the count and the first and last seq of the ordinary
// messages of ch.
func ordinaryRows(h *harness, ch ab2store.Channel) (n int, first, last int64) {
	h.t.Helper()
	h.tx(func(ctx context.Context, tx ab2store.Tx) error {
		rows, err := tx.OrdinaryMessagesBefore(ctx, 1<<62, 1<<30)
		if err != nil {
			return err
		}
		n, first, last = 0, 0, 0
		for _, r := range rows {
			if r.ChannelID != ch.ID {
				continue
			}
			if n == 0 || r.Seq < first {
				first = r.Seq
			}
			n++
			last = max(last, r.Seq)
		}
		return nil
	})
	return n, first, last
}

func usageBytes(h *harness) int64 {
	h.t.Helper()
	u, err := h.raw.Usage(context.Background())
	if err != nil {
		h.t.Fatalf("Usage: %v", err)
	}
	return u.Bytes
}

func capacityNotice(h *harness) (ab2store.Notice, bool) {
	h.t.Helper()
	var n ab2store.Notice
	var ok bool
	h.tx(func(ctx context.Context, tx ab2store.Tx) error {
		var err error
		n, ok, err = tx.Notice(ctx, "capacity")
		return err
	})
	return n, ok
}

func (h *harness) channelRow(ch ab2store.Channel) ab2store.Channel {
	h.t.Helper()
	var got ab2store.Channel
	h.tx(func(ctx context.Context, tx ab2store.Tx) error {
		var err error
		got, err = tx.Channel(ctx, ch.ID)
		return err
	})
	return got
}

func runJobOn(h *harness, m maintenance.Service, name string) int {
	h.t.Helper()
	n, err := m.RunJob(context.Background(), name)
	if err != nil {
		h.t.Fatalf("RunJob(%q): %v", name, err)
	}
	return n
}

// tinyBudget is a budget (bytes) every engine's measure exceeds with a handful
// of rows: the logical measure of Postgres is a few hundred bytes for them,
// the SQLite file is hundreds of KiB.
const tinyBudget = 64

func budgetSetting(e dbtest.Engine) string {
	if e.Name == "postgres" {
		return "postgres_budget_mib"
	}
	return "sqlite_budget_mib"
}

// ordinaryBytes maps the seq of every ordinary message to the bytes deleting
// it frees.
func ordinaryBytes(h *harness) map[int64]int64 {
	h.t.Helper()
	out := map[int64]int64{}
	h.tx(func(ctx context.Context, tx ab2store.Tx) error {
		rows, err := tx.OrdinaryMessagesBefore(ctx, 1<<62, 1<<30)
		for _, r := range rows {
			out[r.Seq] = r.Bytes
		}
		return err
	})
	return out
}

// The budget backstop runs on both engines (Eric, 2026-10-27, issue #7): the
// age jobs come first; above the budget it deletes down to the target.
func TestMaintenanceBudgetEviction(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		dev := h.channel("dev", ab2store.KindOrdinary, "pat", nil)
		mem := h.channel("mem", ab2store.KindMemory, "pat", nil)
		body := strings.Repeat("x", 1024)
		var lastSeq int64
		for range 2500 {
			lastSeq = h.append(dev, "pat", "", body)
		}
		var memSeqs []int64
		for i := range 5 {
			memSeqs = append(memSeqs, h.append(mem, "pat", "", "memory "+strconv.Itoa(i)))
		}
		// Past the message floor and inside the idle window: the messages are
		// eligible and the memories are active.
		h.clock.Advance(2 * time.Hour)
		peak := usageBytes(h)
		m := maint(h)

		// Hysteresis: at or below the budget but above the target, nothing goes.
		m.BudgetOverride = peak + peak/8
		before := h.channelRow(dev).EvictedBeforeSeq
		if n := runJobOn(h, m, "budget eviction"); n != 0 {
			t.Fatalf("between target and budget: evicted %d, want 0", n)
		}
		if n, _, _ := ordinaryRows(h, dev); n != 2500 {
			t.Fatalf("between target and budget: %d rows remain, want 2500", n)
		}
		if got := h.channelRow(dev).EvictedBeforeSeq; got != before {
			t.Fatalf("EvictedBeforeSeq moved %d -> %d", before, got)
		}

		// Sweep.
		perMsg := ordinaryBytes(h)
		m.BudgetOverride = peak / 2
		target := m.BudgetOverride - m.BudgetOverride*int64(h.cfg.CleanupFreePercent)/100
		deleted := runJobOn(h, m, "budget eviction")
		if deleted <= 1000 || deleted >= 2500 {
			t.Fatalf("evicted %d, want more than one chunk and not all", deleted)
		}
		after := usageBytes(h)
		if after > target {
			t.Fatalf("usage %d above target %d", after, target)
		}
		n, first, last := ordinaryRows(h, dev)
		if n != 2500-deleted || n == 0 || last != lastSeq || last-first+1 != int64(n) {
			t.Fatalf("survivors n=%d first=%d last=%d lastSeq=%d deleted=%d: want the newest contiguous range", n, first, last, lastSeq, deleted)
		}
		if got := h.channelRow(dev).EvictedBeforeSeq; got != first {
			t.Fatalf("EvictedBeforeSeq = %d, want %d (highest deleted + 1)", got, first)
		}
		if live := h.liveSeqs(memSeqs...); !sameSeqs(live, memSeqs...) {
			t.Fatalf("live memories = %v, want %v", live, memSeqs)
		}
		if e.Name == "postgres" {
			// The measure is the counted bytes: it falls by exactly the bytes
			// of the messages that are gone.
			left := ordinaryBytes(h)
			var gone int64
			for seq, b := range perMsg {
				if _, ok := left[seq]; !ok {
					gone += b
				}
			}
			if after != peak-gone {
				t.Fatalf("postgres: usage %d, want %d - %d = %d", after, peak, gone, peak-gone)
			}
		}
		if n := runJobOn(h, m, "budget eviction"); n != 0 {
			t.Fatalf("second run evicted %d", n)
		}
	})
}

// The notice is set on both engines, is one of two texts and never blocks a
// Send; health reports degraded.
func TestMaintenanceCapacityNotice(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		mem := h.channel("mem", ab2store.KindMemory, "pat", nil)
		dev := h.channel("dev", ab2store.KindOrdinary, "pat", nil)
		for i := range 5 {
			h.append(mem, "pat", "", "memory "+strconv.Itoa(i))
		}
		m := maint(h)
		m.BudgetOverride = tinyBudget
		if n := runJobOn(h, m, "budget eviction"); n != 0 {
			t.Fatalf("evicted %d with nothing evictable", n)
		}
		if n := runJobOn(h, m, "capacity notices"); n != 1 {
			t.Fatalf("capacity notices returned %d on %s", n, e.Name)
		}
		n, ok := capacityNotice(h)
		if !ok || !strings.Contains(n.Message, "capacity exhausted: ") || !strings.Contains(n.Message, budgetSetting(e)+"=0 MiB") || n.SetAt != h.core.NowMs() {
			t.Fatalf("notice = %+v, %v", n, ok)
		}
		if got := mustHealth(t, h.admin("root")).Status; got != ab2bus.HealthDegraded {
			t.Fatalf("health = %q under a capacity notice, want degraded", got)
		}
		if _, err := newBus(h, "pat").Send(context.Background(), ab2bus.SendInput{Channel: dev.Name, Content: "still works"}); err != nil {
			t.Fatalf("Send under a capacity notice: %v", err)
		}
		m.BudgetOverride = 1 << 30
		runJobOn(h, m, "capacity notices")
		if _, ok := capacityNotice(h); ok {
			t.Fatal("notice not cleared under a large budget")
		}

		// An ordinary message past the floor is evictable: the other text.
		h.clock.Advance(2 * time.Hour)
		m.BudgetOverride = tinyBudget
		if n := runJobOn(h, m, "capacity notices"); n != 1 {
			t.Fatalf("capacity notices returned %d", n)
		}
		if n, ok := capacityNotice(h); !ok || !strings.HasPrefix(n.Message, "agentbus2 storage is over budget: ") {
			t.Fatalf("notice = %+v, %v", n, ok)
		}
	})
}

// The floors hold under any pressure (Eric, 2026-10-27, decision 4): an
// ordinary message younger than 1 hour and a tombstone younger than 72 hours.
func TestMaintenanceEvictionFloors(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		dev := h.channel("dev", ab2store.KindOrdinary, "pat", nil)
		for range 50 {
			h.append(dev, "pat", "", "young")
		}
		m := maint(h)
		m.BudgetOverride = tinyBudget
		if n := runJobOn(h, m, "budget eviction"); n != 0 {
			t.Fatalf("deleted %d messages younger than 1 hour", n)
		}
		h.clock.Advance(61 * time.Minute)
		if n := runJobOn(h, m, "budget eviction"); n != 50 {
			t.Fatalf("deleted %d, want all 50", n)
		}
	})
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		cfg := core.Defaults()
		cfg.TombstoneMinHours = 100 // so "tombstone purge" would not take it
		h := newHarness(t, e, harnessOptions{cfg: &cfg})
		mem := h.channel("mem", ab2store.KindMemory, "pat", nil)
		id := h.append(mem, "pat", "", "first")
		c := "second"
		if _, err := h.as("pat").Edit(context.Background(), ab2bus.EditMemoryInput{ID: id, Content: &c}); err != nil {
			t.Fatalf("Edit: %v", err)
		}
		tombstones := func() int {
			n := 0
			h.tx(func(ctx context.Context, tx ab2store.Tx) error {
				refs, err := tx.ExpiredTombstones(ctx, 1<<62, 1<<30)
				n = len(refs)
				return err
			})
			return n
		}
		if tombstones() != 1 {
			t.Fatalf("%d tombstones after one edit, want 1", tombstones())
		}
		m := maint(h)
		m.BudgetOverride = tinyBudget
		h.clock.Advance(71 * time.Hour)
		if n := runJobOn(h, m, "budget eviction"); n != 0 || tombstones() != 1 {
			t.Fatalf("71 h: deleted %d, %d tombstones; the floor is 72 h", n, tombstones())
		}
		h.clock.Advance(time.Hour + time.Minute)
		if n := runJobOn(h, m, "budget eviction"); n != 1 || tombstones() != 0 {
			t.Fatalf("72 h 1 min: deleted %d, %d tombstones; want the tombstone gone", n, tombstones())
		}
		if revs := h.taskRevisions(id); len(revs) != 1 || revs[0].Tombstone {
			t.Fatalf("revisions left = %+v, want the one live revision", revs)
		}
	})
}

func TestMaintenanceLockExclusiveAcrossCores(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		ctx := context.Background()
		h := newHarness(t, e, harnessOptions{})
		sib := h.sibling(harnessOptions{})
		if ran, _ := maint(h).Tick(ctx); !ran {
			t.Fatal("first core did not run")
		}
		if ran, _ := maint(sib).Tick(ctx); ran {
			t.Fatal("sibling ran while the first holds the lock")
		}
		if err := maint(h).Release(ctx); err != nil {
			t.Fatalf("Release: %v", err)
		}
		if ran, _ := maint(sib).Tick(ctx); !ran {
			t.Fatal("sibling did not take the released lock")
		}
		// The SQLite row has no session to terminate; the rest is Postgres only.
		if e.Name != "postgres" {
			return
		}
		rdb := e.Open(t)
		rows, err := rdb.QueryContext(ctx, `SELECT l.pid FROM pg_locks l JOIN pg_namespace n ON l.classid = hashtext(n.nspname)::oid
			WHERE l.locktype = 'advisory' AND l.granted AND l.objid = hashtext('maintenance')::oid
			AND n.nspname LIKE 'b\_%' AND l.pid <> pg_backend_pid()`)
		if err != nil {
			t.Fatalf("lock lookup: %v", err)
		}
		var pids []int64
		for rows.Next() {
			var pid int64
			if err := rows.Scan(&pid); err != nil {
				t.Fatal(err)
			}
			pids = append(pids, pid)
		}
		_ = rows.Close()
		if len(pids) != 1 {
			t.Fatalf("maintenance advisory locks in b_ schemas = %d, want 1", len(pids))
		}
		if _, err := rdb.ExecContext(ctx, "SELECT pg_terminate_backend($1)", pids[0]); err != nil {
			t.Fatalf("terminate: %v", err)
		}
		if ran, _ := maint(h).Tick(ctx); !ran {
			t.Fatal("first core did not take the lock after the holder's session died")
		}
		if ran, _ := maint(sib).Tick(ctx); ran {
			t.Fatal("sibling kept running after its session died")
		}
	})
}

// TestMaintenanceInvalidScopes: the job repairs the stored row of an
// administrator and of a human that hold a scope and logs each name; a second
// run changes nothing; a scoped agent is left alone (spec 4.3, 4.5).
func TestMaintenanceInvalidScopes(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		buf := &syncBuffer{}
		h := newHarness(t, e, harnessOptions{logger: slog.New(slog.NewTextHandler(buf, nil))})
		h.rtScope("job1")
		h.scopedIdentity("job1-coder", "job1")
		h.admin("root")
		h.human("eric@corp.example")
		h.forceScope("root", "job1")
		h.forceScope("eric@corp.example", "job1")
		if n := h.runJob("invalid scopes"); n != 2 {
			t.Fatalf("first run changed %d, want 2", n)
		}
		log := buf.String()
		for _, name := range []string{"root", "eric@corp.example"} {
			if !strings.Contains(log, "level=ERROR") || !strings.Contains(log, "identity="+name) {
				t.Errorf("log lacks an error naming %s:\n%s", name, log)
			}
		}
		if n := h.runJob("invalid scopes"); n != 0 {
			t.Fatalf("second run changed %d, want 0", n)
		}
		h.tx(func(ctx context.Context, tx ab2store.Tx) error {
			for name, want := range map[string]string{"root": "", "eric@corp.example": "", "job1-coder": "job1"} {
				id, err := tx.Identity(ctx, name)
				if err != nil || id.Scope != want {
					t.Errorf("Identity(%s) = %+v, %v; want scope %q", name, id, err, want)
				}
			}
			return nil
		})
		// The Tick runs it under the maintenance lock.
		h.forceScope("root", "job1")
		ran, rep := maint(h).Tick(context.Background())
		if !ran {
			t.Fatal("Tick did not run")
		}
		for _, r := range rep {
			if r.Name == "invalid scopes" && (r.N != 1 || r.Err != nil) {
				t.Errorf("Tick job = %+v, want 1 change", r)
			}
		}
	})
}
