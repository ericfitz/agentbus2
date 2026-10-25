package health

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/channels"
	"github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/store/sqlite"
)

const startMs = 1_700_000_000_000

type fakeEmbedder struct{}

func (fakeEmbedder) Model() string { return "fake" }
func (fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = []float32{1, 0}
	}
	return out, nil
}

type env struct {
	svc  Service
	path string
}

func newEnv(t *testing.T, emb core.Embedder) env {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agentbus2.db")
	s, err := sqlite.Open(context.Background(), path, sqlite.Options{})
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	cfg := core.Defaults()
	if emb != nil {
		cfg.EmbeddingModel = emb.Model()
	}
	now := time.UnixMilli(startMs)
	c, err := core.New(s, cfg, core.Options{Now: func() time.Time { return now }, Embedder: emb})
	if err != nil {
		t.Fatalf("core.New: %v", err)
	}
	e := env{svc: Service{Core: c}, path: path}
	e.tx(t, func(ctx context.Context, tx store.Tx) error {
		if err := tx.CreateIdentity(ctx, "pat", startMs); err != nil {
			return err
		}
		if err := tx.CreateIdentity(ctx, "root", startMs); err != nil {
			return err
		}
		return tx.SetAdmin(ctx, "root", true)
	})
	return e
}

func (e env) tx(t *testing.T, fn func(ctx context.Context, tx store.Tx) error) {
	t.Helper()
	if err := e.svc.Store.Tx(context.Background(), fn); err != nil {
		t.Fatalf("tx: %v", err)
	}
}

func (e env) setNotice(t *testing.T, kind, msg string) {
	t.Helper()
	e.tx(t, func(ctx context.Context, tx store.Tx) error {
		return tx.SetNotice(ctx, store.Notice{Kind: kind, Message: msg, SetAt: startMs + 5})
	})
}

func (e env) clearNotice(t *testing.T, kind string) {
	t.Helper()
	e.tx(t, func(ctx context.Context, tx store.Tx) error { return tx.ClearNotice(ctx, kind) })
}

func (e env) health(t *testing.T, identity string) bus.Health {
	t.Helper()
	h, err := e.svc.Health(context.Background(), identity, "9.9.9")
	if err != nil {
		t.Fatalf("Health(%q): %v", identity, err)
	}
	return h
}

func TestHealthProbeTier(t *testing.T) {
	e := newEnv(t, nil)
	h := e.health(t, "")
	if h.Status != bus.HealthOK || h.Tier != bus.TierProbe || h.Version != "9.9.9" || h.Now != startMs {
		t.Fatalf("probe = %+v", h)
	}
	if h.StartedAt != 0 || h.Store != nil || h.Embeddings != nil || h.Storage != nil || h.Notices != nil || h.Maintenance != nil {
		t.Fatalf("probe tier leaks members: %+v", h)
	}
}

func TestHealthIdentityTier(t *testing.T) {
	e := newEnv(t, nil)
	h := e.health(t, "pat")
	if h.Tier != bus.TierIdentity || h.StartedAt != startMs {
		t.Fatalf("identity = %+v", h)
	}
	if h.Store == nil || h.Store.Engine != "sqlite" || h.Store.SchemaVersion <= 0 {
		t.Fatalf("store = %+v", h.Store)
	}
	if h.Embeddings == nil || h.Embeddings.State != bus.EmbeddingsOff || h.Embeddings.Model != "" {
		t.Fatalf("embeddings = %+v", h.Embeddings)
	}
	if h.Storage != nil || h.Notices != nil || h.Maintenance != nil {
		t.Fatalf("identity tier leaks admin members: %+v", h)
	}
}

func TestHealthAdminTier(t *testing.T) {
	e := newEnv(t, nil)
	h := e.health(t, "root")
	if h.Tier != bus.TierAdmin {
		t.Fatalf("tier = %q", h.Tier)
	}
	if h.Storage == nil || h.Storage.Bytes <= 0 || !h.Storage.Evictable {
		t.Fatalf("storage = %+v", h.Storage)
	}
	if h.Notices == nil || len(h.Notices) != 0 {
		t.Fatalf("notices = %#v, want non-nil and empty", h.Notices)
	}
	m := h.Maintenance
	if m == nil || m.Lock != bus.LockUnknown || m.Owner != e.svc.Owner || m.IntervalSeconds != e.svc.Cfg.MaintenanceIntervalSeconds ||
		m.Jobs == nil || len(m.Jobs) != 0 || m.LastTickAt != 0 || m.LockError != "" {
		t.Fatalf("maintenance = %+v", m)
	}
}

func TestHealthAdminAfterTick(t *testing.T) {
	e := newEnv(t, nil)
	e.svc.RecordMaintenanceTick(core.MaintenanceTick{At: 5, Held: true, Jobs: []core.MaintenanceJob{{Name: "receipts", Changed: 2}}})
	h := e.health(t, "root")
	m := h.Maintenance
	if m.Lock != bus.LockHeld || m.LastTickAt != 5 || len(m.Jobs) != 1 || m.Jobs[0] != (bus.HealthJob{Name: "receipts", Changed: 2}) {
		t.Fatalf("held tick = %+v", m)
	}
	if h.Status != bus.HealthOK {
		t.Fatalf("status = %q", h.Status)
	}
	e.svc.RecordMaintenanceTick(core.MaintenanceTick{Held: false})
	if m := e.health(t, "root").Maintenance; m.Lock != bus.LockHeldElsewhere || len(m.Jobs) != 0 {
		t.Fatalf("not held tick = %+v", m)
	}
	e.svc.RecordMaintenanceTick(core.MaintenanceTick{LockErr: "boom"})
	h = e.health(t, "root")
	if h.Maintenance.Lock != bus.LockUnknown || h.Maintenance.LockError != "boom" || h.Status != bus.HealthDegraded {
		t.Fatalf("lock error tick = %+v status %q", h.Maintenance, h.Status)
	}
}

func TestHealthDegradedOnNotice(t *testing.T) {
	e := newEnv(t, nil)
	e.setNotice(t, "capacity", "full")
	for _, who := range []string{"", "pat", "root"} {
		if got := e.health(t, who).Status; got != bus.HealthDegraded {
			t.Fatalf("%q status = %q, want degraded", who, got)
		}
	}
	if e.health(t, "").Notices != nil || e.health(t, "pat").Notices != nil {
		t.Fatal("notices leak below the admin tier")
	}
	if n := e.health(t, "root").Notices; len(n) != 1 || n[0].Kind != "capacity" || n[0].Message != "full" {
		t.Fatalf("admin notices = %+v", n)
	}
	e.clearNotice(t, "capacity")
	if got := e.health(t, "").Status; got != bus.HealthOK {
		t.Fatalf("status after clear = %q", got)
	}

	e = newEnv(t, fakeEmbedder{})
	if got := e.health(t, "pat").Embeddings.State; got != bus.EmbeddingsOK {
		t.Fatalf("embeddings = %q, want ok", got)
	}
	e.setNotice(t, "embedding", "endpoint down")
	pat, root := e.health(t, "pat"), e.health(t, "root")
	if pat.Status != bus.HealthDegraded || root.Status != bus.HealthDegraded {
		t.Fatalf("statuses %q %q", pat.Status, root.Status)
	}
	if pat.Embeddings.State != bus.EmbeddingsFailing || root.Embeddings.State != bus.EmbeddingsFailing {
		t.Fatalf("states %q %q", pat.Embeddings.State, root.Embeddings.State)
	}
	if pat.Embeddings.LastError != "" || pat.Embeddings.LastErrorAt != 0 || pat.Embeddings.Model != "" || pat.Embeddings.RejectedLast != "" {
		t.Fatalf("identity tier sees admin embedding members: %+v", pat.Embeddings)
	}
	if root.Embeddings.LastError != "endpoint down" || root.Embeddings.LastErrorAt != startMs+5 || root.Embeddings.Model != "fake" {
		t.Fatalf("admin embeddings = %+v", root.Embeddings)
	}
}

func TestHealthDegradedOnJobError(t *testing.T) {
	e := newEnv(t, nil)
	e.svc.RecordMaintenanceTick(core.MaintenanceTick{At: 1, Held: true, Jobs: []core.MaintenanceJob{{Name: "x", Err: "bad"}}})
	probe := e.health(t, "")
	if probe.Status != bus.HealthDegraded || probe.Tier != bus.TierProbe || probe.Maintenance != nil || probe.Notices != nil {
		t.Fatalf("probe = %+v", probe)
	}
	if j := e.health(t, "root").Maintenance.Jobs; len(j) != 1 || j[0].Error != "bad" {
		t.Fatalf("jobs = %+v", j)
	}
}

func TestHealthUnregisteredIsNotRegistered(t *testing.T) {
	e := newEnv(t, nil)
	_, err := e.svc.Health(context.Background(), "ghost", "v")
	if bus.CodeOf(err) != bus.CodeNotRegistered {
		t.Fatalf("err = %v, want not_registered", err)
	}
}

func TestHealthStoreDownIsUnavailable(t *testing.T) {
	e := newEnv(t, nil)
	if err := e.svc.Store.Close(); err != nil {
		t.Fatal(err)
	}
	for _, who := range []string{"", "pat", "root"} {
		_, err := e.svc.Health(context.Background(), who, "v")
		var be *bus.Error
		if !errors.As(err, &be) || be.Code != bus.CodeUnavailable || !be.Retryable {
			t.Fatalf("%q err = %v, want retryable unavailable", who, err)
		}
		if !strings.HasPrefix(be.Message, "store unreachable: ") || strings.Contains(be.Message, e.path) {
			t.Fatalf("message = %q", be.Message)
		}
	}
}

func TestEmbeddingReportMatchesStatus(t *testing.T) {
	e := newEnv(t, fakeEmbedder{})
	var ch store.Channel
	e.tx(t, func(ctx context.Context, tx store.Tx) error {
		var err error
		ch, err = tx.CreateChannel(ctx, store.ChannelCreate{Name: "mem", Kind: store.KindMemory, CreatedAt: startMs})
		return err
	})
	err := e.svc.Store.Append(context.Background(), func(ctx context.Context, tx store.Tx) error {
		_, _, err := e.svc.Append(ctx, tx, core.AppendInput{ChannelID: ch.ID, Channel: ch.Name, Kind: ch.Kind, Sender: "pat", SenderKind: store.IdentityAgent, Content: "remember"})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	e.setNotice(t, "embedding", "endpoint down")
	st, err := channels.Service{Core: e.svc.Core}.Status(context.Background(), "root")
	if err != nil {
		t.Fatal(err)
	}
	var rep bus.HealthEmbeddings
	err = e.svc.Store.Read(context.Background(), func(ctx context.Context, tx store.Tx) error {
		var err error
		rep, err = channels.EmbeddingReport(ctx, tx, e.svc.Core)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if st.EmbeddingBacklog != 1 || rep.Backlog != st.EmbeddingBacklog || rep.Rejected != st.EmbeddingRejected ||
		rep.RejectedLast != st.EmbeddingRejectedLast || rep.LastError != st.EmbeddingError || rep.LastErrorAt != st.EmbeddingErrorAt {
		t.Fatalf("report %+v disagrees with status %+v", rep, st)
	}
	if h := e.health(t, "root"); *h.Embeddings != rep {
		t.Fatalf("health embeddings %+v != report %+v", h.Embeddings, rep)
	}
}
