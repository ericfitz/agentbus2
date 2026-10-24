package bus

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/identities"
	"github.com/ericfitz/agentbus2/internal/core/maintenance"
	"github.com/ericfitz/agentbus2/internal/core/scopes"
	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
)

const retireJob = "scope retirement"

// archiveCall is one ArchiveBridge call.
type archiveCall struct{ Channel, Reason string }

// fakeArchiver records ArchiveBridge calls and, like the real archiver,
// marks the row archived. failOnce makes the first call fail before it does
// anything; failAfterMark makes the first call mark the row and then fail
// (Slack archived, the transaction after it lost).
type fakeArchiver struct {
	h             *harness
	mu            sync.Mutex
	calls         []archiveCall
	failOnce      bool
	failAfterMark bool
}

func (f *fakeArchiver) ArchiveBridge(ctx context.Context, b ab2store.Bridge, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, archiveCall{b.ChannelName, reason})
	if f.failOnce {
		f.failOnce = false
		return errors.New("fake slack is down")
	}
	err := f.h.raw.Tx(ctx, func(ctx context.Context, tx ab2store.Tx) error {
		return tx.SetBridgeArchived(ctx, b.ID, f.h.core.NowMs())
	})
	if err != nil {
		return err
	}
	if f.failAfterMark {
		f.failAfterMark = false
		return errors.New("fake: lost after archiving")
	}
	return nil
}

func (f *fakeArchiver) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// retireMaint is the maintenance service over h with the archiver (nil: the
// bridge is off).
func retireMaint(h *harness, a *fakeArchiver) maintenance.Service {
	m := maint(h)
	if a != nil { // a nil *fakeArchiver in the interface would not be nil
		m.Archiver = a
	}
	return m
}

func (h *harness) retireRun(m maintenance.Service) int {
	h.t.Helper()
	n, err := m.RunJob(context.Background(), retireJob)
	if err != nil {
		h.t.Fatalf("RunJob(%q): %v", retireJob, err)
	}
	return n
}

// rtScope creates an open scope through the core as administrator root.
func (h *harness) rtScope(name string) {
	h.t.Helper()
	h.admin("root")
	if _, err := (scopes.Service{Core: h.core}).Create(context.Background(), "root", ab2bus.CreateScopeInput{Name: name}); err != nil {
		h.t.Fatalf("create scope %s: %v", name, err)
	}
}

// rtDone marks the scope done with mode.
func (h *harness) rtDone(name string, mode ab2bus.RetireMode) {
	h.t.Helper()
	if _, err := (scopes.Service{Core: h.core}).Done(context.Background(), "root", ab2bus.DoneScopeInput{Name: name, Retire: mode}); err != nil {
		h.t.Fatalf("done scope %s: %v", name, err)
	}
}

// rtChannel creates a channel of kind in the scope.
func (h *harness) rtChannel(scope, name string, kind ab2store.Kind) ab2store.Channel {
	h.t.Helper()
	var ch ab2store.Channel
	h.tx(func(ctx context.Context, tx ab2store.Tx) error {
		seq, err := tx.MaxSeq(ctx)
		if err != nil {
			return err
		}
		ch, err = tx.CreateChannel(ctx, ab2store.ChannelCreate{
			Name: name, Kind: kind, Creator: "root", Scope: scope, CreatedSeq: seq, CreatedAt: h.core.NowMs(),
		})
		return err
	})
	return ch
}

// rtBridge puts a live bridge (a map row included) on the scope's channel.
func (h *harness) rtBridge(ch ab2store.Channel, scope, slackID string) ab2store.Bridge {
	h.t.Helper()
	var b ab2store.Bridge
	h.tx(func(ctx context.Context, tx ab2store.Tx) error {
		var err error
		b, err = tx.CreateBridge(ctx, ab2store.Bridge{ChannelID: ch.ID, ChannelName: ch.Name, Scope: scope, SlackName: "ab-" + ch.Name, CreatedAt: 5})
		if err != nil {
			return err
		}
		if err := tx.SetBridgeSlackChannel(ctx, b.ID, slackID); err != nil {
			return err
		}
		if err := tx.SetBridgeLive(ctx, b.ID); err != nil {
			return err
		}
		return tx.PutBridgeMessage(ctx, ab2store.BridgeMessage{BridgeID: b.ID, Seq: 1, SlackTS: "1.000001", RootTS: "1.000001", CreatedAt: 6})
	})
	b.State = ab2store.BridgeLive
	return b
}

// rtArchiveBridge archives the bridge the way the Slack side would.
func (h *harness) rtArchiveBridge(id int64) {
	h.t.Helper()
	h.tx(func(ctx context.Context, tx ab2store.Tx) error { return tx.SetBridgeArchived(ctx, id, h.core.NowMs()) })
}

// rtBridgeRows is how many bridge rows the scope has and whether the map row
// of bridge id is still there.
func (h *harness) rtBridgeRows(scope string, id int64) (rows int, mapRow bool) {
	h.t.Helper()
	h.tx(func(ctx context.Context, tx ab2store.Tx) error {
		bs, err := tx.ScopeBridges(ctx, scope)
		if err != nil {
			return err
		}
		rows = len(bs)
		_, err = tx.BridgeMessageBySeq(ctx, id, 1)
		mapRow = err == nil
		return nil
	})
	return rows, mapRow
}

// rtState is the scope's stored state.
func (h *harness) rtState(name string) ab2store.ScopeState {
	h.t.Helper()
	var st ab2store.ScopeState
	h.tx(func(ctx context.Context, tx ab2store.Tx) error {
		sc, err := tx.Scope(ctx, name)
		st = sc.State
		return err
	})
	return st
}

func (h *harness) rtMembers(scope string) (chs, ids []string) {
	h.t.Helper()
	h.tx(func(ctx context.Context, tx ab2store.Tx) error {
		cs, is, err := tx.ScopeMembers(ctx, scope)
		for _, c := range cs {
			chs = append(chs, c.Name)
		}
		for _, i := range is {
			ids = append(ids, i.Name)
		}
		return err
	})
	return chs, ids
}

func (h *harness) idleFor() time.Duration { return hours(h.cfg.ScopeIdleHours + 1) }

func TestScopeRetirementRules(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		t.Run("normal", func(t *testing.T) {
			h := newHarness(t, e, harnessOptions{})
			h.rtScope("job1")
			ch := h.rtChannel("job1", "job1-chat", ab2store.KindOrdinary)
			h.rtDone("job1", ab2bus.RetireNormal)
			m := retireMaint(h, nil)
			if n := h.retireRun(m); n != 0 {
				t.Fatalf("retired %d while the scope still has a channel", n)
			}
			h.tx(func(ctx context.Context, tx ab2store.Tx) error { return tx.DeleteChannel(ctx, ch.ID) })
			if n := h.retireRun(m); n != 1 {
				t.Fatalf("retired %d after the last channel went, want 1", n)
			}
			if st := h.rtState("job1"); st != ab2store.ScopeRetired {
				t.Fatalf("state = %q", st)
			}
		})

		t.Run("with_bridge", func(t *testing.T) {
			h := newHarness(t, e, harnessOptions{})
			a := &fakeArchiver{h: h}
			m := retireMaint(h, a)
			h.rtScope("job2")
			ch := h.rtChannel("job2", "job2-chat", ab2store.KindOrdinary)
			b := h.rtBridge(ch, "job2", "G2")
			h.rtDone("job2", ab2bus.RetireWithBridge)
			if n := h.retireRun(m); n != 0 || a.count() != 0 {
				t.Fatalf("retired %d, archived %d while the bridge is live", n, a.count())
			}
			h.rtArchiveBridge(b.ID)
			if n := h.retireRun(m); n != 1 {
				t.Fatalf("retired %d after the bridge was archived, want 1", n)
			}
			if a.count() != 0 {
				t.Errorf("archiver called %d times for an archived bridge", a.count())
			}
			if rows, mapRow := h.rtBridgeRows("job2", b.ID); rows != 0 || mapRow {
				t.Errorf("bridge rows = %d, map row = %v, want none", rows, mapRow)
			}
			if chs, _ := h.rtMembers("job2"); len(chs) != 0 {
				t.Errorf("channels left: %v", chs)
			}
		})

		t.Run("idle", func(t *testing.T) {
			h := newHarness(t, e, harnessOptions{})
			a := &fakeArchiver{h: h}
			m := retireMaint(h, a)
			h.rtScope("job3")
			ch := h.rtChannel("job3", "job3-chat", ab2store.KindOrdinary)
			b := h.rtBridge(ch, "job3", "G3")
			h.clock.Advance(h.idleFor() - 2*time.Hour)
			if n := h.retireRun(m); n != 0 {
				t.Fatalf("retired %d inside the idle window", n)
			}
			h.clock.Advance(2 * time.Hour)
			if n := h.retireRun(m); n != 1 {
				t.Fatalf("retired %d past the idle window, want 1", n)
			}
			if len(a.calls) != 1 || a.calls[0] != (archiveCall{"job3-chat", "idle"}) {
				t.Fatalf("archiver calls = %+v, want one idle archive of job3-chat", a.calls)
			}
			if rows, mapRow := h.rtBridgeRows("job3", b.ID); rows != 0 || mapRow {
				t.Errorf("bridge rows = %d, map row = %v, want none", rows, mapRow)
			}
		})

		t.Run("idle without an archiver holds the scope", func(t *testing.T) {
			h := newHarness(t, e, harnessOptions{})
			var logs syncBuffer
			c, err := core.New(h.raw, h.cfg, core.Options{Now: h.clock.Now, Notifier: h.notifier,
				Logger: slog.New(slog.NewTextHandler(&logs, nil))})
			if err != nil {
				t.Fatal(err)
			}
			m := maintenance.New(c) // Archiver nil: the bridge is off
			h.rtScope("job4")
			ch := h.rtChannel("job4", "job4-chat", ab2store.KindOrdinary)
			b := h.rtBridge(ch, "job4", "G4")
			h.clock.Advance(h.idleFor())
			for i := 1; i <= 2; i++ {
				if n := h.retireRun(m); n != 0 {
					t.Fatalf("tick %d retired %d with a live bridge and no archiver", i, n)
				}
				if st := h.rtState("job4"); st != ab2store.ScopeOpen {
					t.Fatalf("tick %d: state = %q, want open", i, st)
				}
				if rows, mapRow := h.rtBridgeRows("job4", b.ID); rows != 1 || !mapRow {
					t.Fatalf("tick %d: bridge rows = %d, map row = %v, want both intact", i, rows, mapRow)
				}
				if got := strings.Count(logs.String(), "level=WARN"); got != i {
					t.Fatalf("tick %d: %d warnings, want %d:\n%s", i, got, i, logs.String())
				}
			}
			for _, want := range []string{"scope=job4", "slack_channels=G4"} {
				if !strings.Contains(logs.String(), want) {
					t.Errorf("warning lacks %q:\n%s", want, logs.String())
				}
			}
			h.rtArchiveBridge(b.ID)
			if n := h.retireRun(m); n != 1 {
				t.Fatalf("retired %d once the bridge was archived, want 1", n)
			}
		})
	})
}

// failNthTx fails the nth Tx call after arm with an injected error, without
// running it, and passes every other call through.
type failNthTx struct {
	ab2store.Store
	// before, when set, runs ahead of every Tx call with its 1-based number
	// since arm (outside the lock, before the call reaches the Store).
	before func(n int)
	mu     sync.Mutex
	n, cnt int
}

func (f *failNthTx) arm(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n, f.cnt = n, 0
}

func (f *failNthTx) Tx(ctx context.Context, fn func(context.Context, ab2store.Tx) error) error {
	f.mu.Lock()
	f.cnt++
	hit := f.n != 0 && f.cnt == f.n
	cnt := f.cnt
	f.mu.Unlock()
	if f.before != nil {
		f.before(cnt)
	}
	if hit {
		return errors.New("injected transaction fault")
	}
	return f.Store.Tx(ctx, fn)
}

func TestScopeRetirementResumesPartialRun(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		fs := &failNthTx{Store: h.raw}
		c, err := core.New(fs, h.cfg, core.Options{Now: h.clock.Now, Notifier: h.notifier})
		if err != nil {
			t.Fatal(err)
		}
		m := maintenance.New(c)
		h.rtScope("job1")
		h.rtChannel("job1", "job1-a", ab2store.KindOrdinary)
		h.rtChannel("job1", "job1-b", ab2store.KindMemory)
		h.scopedIdentity("job1-coder", "job1")
		h.clock.Advance(h.idleFor())

		// Transaction 1 deletes the bridges, 2 the first channel, 3 the second.
		fs.arm(3)
		if _, err := m.RunJob(context.Background(), retireJob); err == nil {
			t.Fatal("the injected fault was swallowed")
		}
		if st := h.rtState("job1"); st != ab2store.ScopeOpen {
			t.Fatalf("state after the partial run = %q, want open", st)
		}
		if chs, _ := h.rtMembers("job1"); len(chs) != 1 {
			t.Fatalf("channels after the partial run = %v, want one left", chs)
		}

		fs.arm(0)
		if n := h.retireRun(m); n != 1 {
			t.Fatalf("the resumed run retired %d, want 1", n)
		}
		chs, ids := h.rtMembers("job1")
		if len(chs) != 0 || len(ids) != 0 || h.rtState("job1") != ab2store.ScopeRetired {
			t.Fatalf("after the resumed run: channels %v, identities %v, state %q", chs, ids, h.rtState("job1"))
		}
	})
}

func TestScopeRetirementScrub(t *testing.T) {
	// Decision 19: outside the scope, the retired identities' ACL entries,
	// subscriptions and task claims go; their messages stay.
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		h.rtScope("job1")
		coder := h.scopedIdentity("job1-coder", "job1")
		h.session("job1-coder", "coder") // a live session: its claim is not abandoned yet
		pat := h.as("pat")
		eric := h.human("eric@corp.example")
		acl := []ab2store.ACLEntry{
			{Identity: "pat", Permissions: []ab2store.Permission{ab2store.PermRead, ab2store.PermWrite, ab2store.PermAdmin}},
			{Identity: "job1-coder", Permissions: []ab2store.Permission{ab2store.PermRead, ab2store.PermWrite}},
		}
		ops := h.channel("ops", ab2store.KindOrdinary, "pat", acl)
		h.channel("backlog", ab2store.KindTasks, "pat", acl)
		if err := coder.Subscribe(ctx, ab2bus.SubscribeInput{Channel: "ops"}); err != nil {
			t.Fatal(err)
		}
		made, err := pat.Create(ctx, ab2bus.TaskCreateInput{Channel: "backlog", Subject: "first"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := coder.Claim(ctx, ab2bus.TaskClaimInput{ID: made.ID}); err != nil {
			t.Fatal(err)
		}
		sent, err := coder.Send(ctx, ab2bus.SendInput{Channel: "ops", Content: "hello from the coder"})
		if err != nil {
			t.Fatal(err)
		}
		h.rtDone("job1", ab2bus.RetireNormal) // no channel: due by rule 2

		ran, rep := retireMaint(h, nil).Tick(ctx)
		if !ran {
			t.Fatal("tick did not run")
		}
		for _, r := range rep {
			if r.Err != nil {
				t.Errorf("job %q: %v", r.Name, r.Err)
			}
		}

		h.tx(func(ctx context.Context, tx ab2store.Tx) error {
			if _, err := tx.Identity(ctx, "job1-coder"); !errors.Is(err, ab2store.ErrNotFound) {
				t.Errorf("identity after retirement: %v, want ErrNotFound", err)
			}
			if retired, err := tx.IdentityRetired(ctx, "job1-coder"); err != nil || !retired {
				t.Errorf("IdentityRetired = %v, %v", retired, err)
			}
			entries, err := tx.ACL(ctx, ops.ID)
			if err != nil {
				return err
			}
			for _, en := range entries {
				if en.Identity == "job1-coder" {
					t.Errorf("ops ACL still has job1-coder: %+v", entries)
				}
			}
			if len(entries) != 1 || entries[0].Identity != "pat" {
				t.Errorf("ops ACL = %+v, want pat only", entries)
			}
			subs, err := tx.Subscriptions(ctx, "job1-coder")
			if err != nil || len(subs) != 0 {
				t.Errorf("subscriptions = %+v, %v", subs, err)
			}
			if _, err := tx.Identity(ctx, "eric@corp.example"); err != nil {
				t.Errorf("human identity: %v", err)
			}
			return nil
		})
		got, err := pat.GetTask(ctx, made.ID)
		if err != nil || got.Status != "pending" || got.Owner != "" {
			t.Errorf("task after retirement = %+v, %v; want pending with no owner", got, err)
		}
		if live := h.liveSeqs(sent.Seq); !sameSeqs(live, sent.Seq) {
			t.Errorf("the coder's message is gone: %v", live)
		}
		if _, err := eric.ListChannels(ctx); err != nil {
			t.Errorf("human ListChannels: %v", err)
		}
		if _, err := pat.History(ctx, ab2bus.HistoryInput{Channel: "ops"}); err != nil {
			t.Errorf("ops: %v", err)
		}

		// The retired name answers not_registered, and nothing can take it.
		_, err = newBus(h, "job1-coder").Send(ctx, ab2bus.SendInput{Channel: "ops", Content: "again"})
		codeOf(t, err, ab2bus.CodeNotRegistered)
		_, err = (identities.Service{Core: h.core}).CreateAgentIdentityAs(ctx, "root", ab2bus.CreateAgentIdentityInput{Name: "job1-coder"})
		codeOf(t, err, ab2bus.CodeConflict)
		_, err = (scopes.Service{Core: h.core}).Get(ctx, "root", "job1")
		codeOf(t, err, ab2bus.CodeNotFound)
		if !strings.Contains(msgOf(err), `scope "job1" is retired`) {
			t.Errorf("Get error = %q", msgOf(err))
		}
	})
}

func TestScopeRetirementIdempotent(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		a := &fakeArchiver{h: h}
		m := retireMaint(h, a)
		h.rtScope("job1")
		h.rtBridge(h.rtChannel("job1", "job1-chat", ab2store.KindOrdinary), "job1", "G1")
		h.clock.Advance(h.idleFor())

		if n := h.retireRun(m); n != 1 {
			t.Fatalf("first run retired %d, want 1", n)
		}
		if n := h.retireRun(m); n != 0 {
			t.Fatalf("second run retired %d, want 0", n)
		}
		if a.count() != 1 {
			t.Fatalf("archiver called %d times, want 1", a.count())
		}

		// Two replicas ticking at once: the maintenance lock lets one run.
		h2 := newHarness(t, e, harnessOptions{})
		h2.rtScope("job2")
		h2.rtChannel("job2", "job2-chat", ab2store.KindOrdinary)
		sib := h2.sibling(harnessOptions{})
		a2 := &fakeArchiver{h: h2}
		ms := []maintenance.Service{retireMaint(h2, a2), retireMaint(sib, a2)}
		h2.clock.Advance(h2.idleFor())
		sib.clock.Advance(h2.idleFor())
		var wg sync.WaitGroup
		var mu sync.Mutex
		ranCount, retired := 0, 0
		start := make(chan struct{})
		for _, svc := range ms {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				ran, rep := svc.Tick(context.Background())
				mu.Lock()
				defer mu.Unlock()
				if ran {
					ranCount++
				}
				for _, r := range rep {
					if r.Name == retireJob {
						retired += r.N
					}
				}
			}()
		}
		close(start)
		wg.Wait()
		if ranCount != 1 || retired != 1 {
			t.Fatalf("ran = %d, retired = %d, want 1 and 1", ranCount, retired)
		}
	})
}

func TestScopeRetirementAfterArchiveFailure(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		t.Run("archive fails", func(t *testing.T) {
			h := newHarness(t, e, harnessOptions{})
			a := &fakeArchiver{h: h, failOnce: true}
			m := retireMaint(h, a)
			h.rtScope("job1")
			h.rtBridge(h.rtChannel("job1", "job1-chat", ab2store.KindOrdinary), "job1", "G1")
			h.clock.Advance(h.idleFor())
			if _, err := m.RunJob(context.Background(), retireJob); err == nil {
				t.Fatal("the archive failure was swallowed")
			}
			if st := h.rtState("job1"); st != ab2store.ScopeOpen {
				t.Fatalf("state after the failure = %q, want open", st)
			}
			if n := h.retireRun(m); n != 1 {
				t.Fatalf("next run retired %d, want 1", n)
			}
			if a.count() != 2 {
				t.Errorf("archiver called %d times, want 2", a.count())
			}
		})
		t.Run("archived then lost", func(t *testing.T) {
			// Slack archived and the row is archived, but the call failed: the
			// next tick retires the scope without a second Slack call.
			h := newHarness(t, e, harnessOptions{})
			a := &fakeArchiver{h: h, failAfterMark: true}
			m := retireMaint(h, a)
			h.rtScope("job1")
			h.rtBridge(h.rtChannel("job1", "job1-chat", ab2store.KindOrdinary), "job1", "G1")
			h.clock.Advance(h.idleFor())
			if _, err := m.RunJob(context.Background(), retireJob); err == nil {
				t.Fatal("the failure was swallowed")
			}
			if st := h.rtState("job1"); st != ab2store.ScopeOpen {
				t.Fatalf("state = %q, want open", st)
			}
			if n := h.retireRun(m); n != 1 {
				t.Fatalf("next run retired %d, want 1", n)
			}
			if a.count() != 1 {
				t.Errorf("archiver called %d times, want 1", a.count())
			}
		})
	})
}

func TestRetiredScopePurged(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		sv := scopes.Service{Core: h.core}
		h.rtScope("job1")
		h.rtDone("job1", ab2bus.RetireNormal)
		m := retireMaint(h, nil)
		if n := h.retireRun(m); n != 1 {
			t.Fatalf("retired %d, want 1", n)
		}
		_, err := sv.Get(ctx, "root", "job1")
		codeOf(t, err, ab2bus.CodeNotFound)
		if !strings.Contains(msgOf(err), "is retired") {
			t.Errorf("error %q", msgOf(err))
		}
		h.clock.Advance(hours(h.cfg.SessionRetentionHours) + time.Hour)
		if n := h.retireRun(m); n != 1 {
			t.Fatalf("purged %d, want 1", n)
		}
		_, err = sv.Get(ctx, "root", "job1")
		codeOf(t, err, ab2bus.CodeNotFound)
		if !strings.Contains(msgOf(err), "does not exist") {
			t.Errorf("error after purge %q", msgOf(err))
		}
	})
}

func TestScopeRetirementRaces(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		// build is a maintenance service over a failNthTx store.
		build := func(h *harness, a *fakeArchiver) (*failNthTx, maintenance.Service) {
			fs := &failNthTx{Store: h.raw}
			c, err := core.New(fs, h.cfg, core.Options{Now: h.clock.Now, Notifier: h.notifier})
			if err != nil {
				t.Fatal(err)
			}
			m := maintenance.New(c)
			if a != nil {
				m.Archiver = a
			}
			return fs, m
		}

		t.Run("bridge goes live after the due read", func(t *testing.T) {
			h := newHarness(t, e, harnessOptions{})
			a := &fakeArchiver{h: h}
			fs, m := build(h, a)
			h.rtScope("job1")
			ch := h.rtChannel("job1", "job1-chat", ab2store.KindOrdinary)
			h.clock.Advance(h.idleFor())
			var b ab2store.Bridge
			fs.arm(0)
			fs.before = func(n int) {
				if n == 1 { // RetireDue found no bridge; Retire's first transaction is next
					b = h.rtBridge(ch, "job1", "G1")
				}
			}
			if _, err := m.RunJob(context.Background(), retireJob); err == nil {
				t.Fatal("the active bridge was deleted or the conflict swallowed")
			}
			if st := h.rtState("job1"); st != ab2store.ScopeOpen {
				t.Fatalf("state = %q, want open", st)
			}
			if rows, mapRow := h.rtBridgeRows("job1", b.ID); rows != 1 || !mapRow {
				t.Fatalf("bridge rows = %d, map row = %v, want both intact", rows, mapRow)
			}
			fs.before = nil
			if n := h.retireRun(m); n != 1 {
				t.Fatalf("next run retired %d, want 1", n)
			}
			if len(a.calls) != 1 || a.calls[0].Reason != "idle" {
				t.Fatalf("archiver calls = %+v, want one idle archive", a.calls)
			}
		})

		t.Run("channel joins before the final step", func(t *testing.T) {
			h := newHarness(t, e, harnessOptions{})
			fs, m := build(h, nil)
			h.rtScope("job1")
			h.rtChannel("job1", "job1-a", ab2store.KindOrdinary)
			h.clock.Advance(h.idleFor())
			fs.arm(0)
			fs.before = func(n int) {
				if n == 3 { // 1 bridges, 2 the channel, 3 the retired mark
					h.rtChannel("job1", "job1-late", ab2store.KindOrdinary)
				}
			}
			if _, err := m.RunJob(context.Background(), retireJob); err == nil {
				t.Fatal("the final step retired a scope that gained a channel")
			}
			if st := h.rtState("job1"); st != ab2store.ScopeOpen {
				t.Fatalf("state = %q, want open", st)
			}
			if chs, _ := h.rtMembers("job1"); len(chs) != 1 || chs[0] != "job1-late" {
				t.Fatalf("channels = %v, want [job1-late]", chs)
			}
			fs.before = nil
			if n := h.retireRun(m); n != 1 {
				t.Fatalf("next run retired %d, want 1", n)
			}
			if chs, _ := h.rtMembers("job1"); len(chs) != 0 {
				t.Fatalf("channels left: %v", chs)
			}
			// Retire on a retired or unknown scope is (false, nil).
			sv := scopes.Service{Core: h.core}
			for _, name := range []string{"job1", "nope"} {
				if ok, err := sv.Retire(context.Background(), name, h.core.NowMs()); ok || err != nil {
					t.Errorf("Retire(%q) = %v, %v; want false, nil", name, ok, err)
				}
			}
		})
	})
}
