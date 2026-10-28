package bus

import (
	"context"
	"testing"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/version"
)

func (h *harness) setNotice(kind, msg string) {
	h.t.Helper()
	h.tx(func(ctx context.Context, tx ab2store.Tx) error {
		return tx.SetNotice(ctx, ab2store.Notice{Kind: kind, Message: msg, SetAt: 7})
	})
}

func mustHealth(t *testing.T, b ab2bus.Bus) ab2bus.Health {
	t.Helper()
	got, err := b.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	return got
}

func TestHealthTiers(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})

		p := mustHealth(t, h.as("pat"))
		if p.Tier != ab2bus.TierIdentity || p.Status != ab2bus.HealthOK || p.Version != version.Version || p.Now != fixedNowMs || p.StartedAt != fixedNowMs {
			t.Fatalf("identity tier: %+v", p)
		}
		if p.Store == nil || p.Store.Engine != e.Name || p.Store.SchemaVersion <= 0 {
			t.Fatalf("store = %+v", p.Store)
		}
		if p.Embeddings == nil || p.Embeddings.State != ab2bus.EmbeddingsOff {
			t.Fatalf("embeddings = %+v", p.Embeddings)
		}
		if p.Storage != nil || p.Notices != nil || p.Maintenance != nil {
			t.Fatalf("identity tier carries admin members: %+v", p)
		}

		a := mustHealth(t, h.admin("root"))
		if a.Tier != ab2bus.TierAdmin || a.Storage == nil || !a.Storage.Evictable || a.Storage.Tier != ab2bus.PressureNormal {
			t.Fatalf("admin tier: %+v", a)
		}
		if a.Notices == nil || len(a.Notices) != 0 {
			t.Fatalf("notices = %#v, want empty non-nil", a.Notices)
		}
		if m := a.Maintenance; m == nil || m.Owner != h.core.Owner || m.Lock != ab2bus.LockUnknown || m.Jobs == nil {
			t.Fatalf("maintenance = %+v", m)
		}
	})
}

func TestHealthDegradedOnNotice(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		pat, root := h.as("pat"), h.admin("root")
		h.setNotice("capacity", "full")

		p, a := mustHealth(t, pat), mustHealth(t, root)
		if p.Status != ab2bus.HealthDegraded || a.Status != ab2bus.HealthDegraded {
			t.Fatalf("status = %q / %q, want degraded", p.Status, a.Status)
		}
		if p.Notices != nil {
			t.Fatalf("identity tier lists notices: %+v", p.Notices)
		}
		if len(a.Notices) != 1 || a.Notices[0].Kind != "capacity" || a.Notices[0].Message != "full" {
			t.Fatalf("admin notices = %+v", a.Notices)
		}
	})
}

func TestHealthShowsInspectionNotice(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		pat, root := h.as("pat"), h.admin("root")
		h.setNotice("inspection", "failing")

		p, a := mustHealth(t, pat), mustHealth(t, root)
		if p.Status != ab2bus.HealthDegraded || a.Status != ab2bus.HealthDegraded {
			t.Fatalf("status = %q / %q, want degraded", p.Status, a.Status)
		}
		if p.Notices != nil {
			t.Fatalf("identity tier lists notices: %+v", p.Notices)
		}
		if len(a.Notices) != 1 || a.Notices[0].Kind != "inspection" || a.Notices[0].Message != "failing" {
			t.Fatalf("admin notices = %+v, want exactly the inspection notice", a.Notices)
		}
	})
}

func TestStatusShowsInspectionNotice(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		root := h.admin("root")
		h.setNotice("inspection", "failing")

		st, err := root.Status(context.Background())
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if len(st.Notices) != 1 || st.Notices[0].Kind != "inspection" || st.Notices[0].Message != "failing" {
			t.Fatalf("status notices = %+v, want exactly the inspection notice", st.Notices)
		}
	})
}

func TestHealthEmbeddingsState(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{embedder: true})
		pat, root := h.as("pat"), h.admin("root")

		if p := mustHealth(t, pat).Embeddings; p.State != ab2bus.EmbeddingsOK || p.Model != "" {
			t.Fatalf("identity embeddings = %+v", p)
		}
		if a := mustHealth(t, root).Embeddings; a.State != ab2bus.EmbeddingsOK || a.Model != "fake" {
			t.Fatalf("admin embeddings = %+v", a)
		}

		h.setNotice("embedding", "endpoint down")
		if p := mustHealth(t, pat).Embeddings; p.State != ab2bus.EmbeddingsFailing || p.LastError != "" {
			t.Fatalf("identity embeddings = %+v", p)
		}
		if a := mustHealth(t, root).Embeddings; a.State != ab2bus.EmbeddingsFailing || a.LastError != "endpoint down" {
			t.Fatalf("admin embeddings = %+v", a)
		}
	})
}

func TestHealthMaintenanceBlock(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		root := h.admin("root")
		pat := h.as("pat")

		maint(h).Tick(ctx)
		m := mustHealth(t, root).Maintenance
		if m == nil || m.Lock != ab2bus.LockHeld || m.LastTickAt != h.core.NowMs() || len(m.Jobs) != len(maint(h).Jobs()) || m.LockError != "" {
			t.Fatalf("held block = %+v", m)
		}
		for _, j := range m.Jobs {
			if j.Error != "" {
				t.Fatalf("job %+v failed", j)
			}
		}

		sib := h.sibling(harnessOptions{})
		maint(sib).Tick(ctx)
		m = mustHealth(t, newBus(sib, "root")).Maintenance
		if m == nil || m.Lock != ab2bus.LockHeldElsewhere || len(m.Jobs) != 0 {
			t.Fatalf("sibling block = %+v", m)
		}
		if mustHealth(t, pat).Maintenance != nil {
			t.Fatal("identity tier sees the maintenance block")
		}
	})
}

func TestHealthUnregistered(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		_, err := newBus(h, "ghost").Health(context.Background())
		codeOf(t, err, ab2bus.CodeNotRegistered)
	})
}
