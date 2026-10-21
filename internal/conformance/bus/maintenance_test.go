package bus

import (
	"context"
	"errors"
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
		var left []int64
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
	// pending Eric: Milestone 3 item 13: owner liveness is a live session row.
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
		h.channel("memory-empty", ab2store.KindMemory, "pat", nil)
		h.channel("tasks-empty", ab2store.KindTasks, "pat", nil)
		h.append(withMsg, "pat", "", "hello")
		h.ensureIdentity("kim")
		h.tx(func(ctx context.Context, tx ab2store.Tx) error {
			// kim has no live session; a subscription of any identity still counts.
			return tx.Subscribe(ctx, "kim", withSub.ID, 0, h.core.NowMs())
		})

		h.clock.Advance(hours(h.cfg.EmptyChannelReapHours) - time.Minute)
		h.channel("young", ab2store.KindOrdinary, "pat", nil)
		h.clock.Advance(2 * time.Minute)
		// "empty" is now N hours and a minute old; "young" is two minutes old.
		if n := h.runJob("empty channels"); n != 1 {
			t.Fatalf("reaped %d, want 1 (empty)", n)
		}
		for name, want := range map[string]bool{
			"empty": false, "with-message": true, "with-subscription": true, "memory-empty": true,
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
	// pending Eric: Milestone 3 item 3: budget eviction and capacity notices wait for Milestone 7.
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		for _, name := range []string{"vacuum", "budget eviction", "capacity notices", ""} {
			_, err := maint(h).RunJob(context.Background(), name)
			codeOf(t, err, ab2bus.CodeValidation)
			if !strings.Contains(err.Error(), "lapsed sessions") {
				t.Fatalf("error %q does not name the known jobs", err)
			}
		}
	})
}
