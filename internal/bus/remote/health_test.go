package remote

import (
	"context"
	"strings"
	"testing"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
)

func TestHealthUnavailableError(t *testing.T) {
	c := &canned{status: 503, ctype: "application/problem+json", body: `{"code":"unavailable","title":"x","retryable":true}`}
	_, err := newBus(t, c).Health(context.Background())
	be := asBusError(t, err)
	if be.Code != ab2bus.CodeUnavailable || !be.Retryable {
		t.Fatalf("err = %+v", be)
	}
}

func TestHealthRejectsUnknownEnums(t *testing.T) {
	cases := map[string]struct{ body, member string }{
		"status":     {`{"status":"meh","tier":"probe","version":"v","now":1}`, "status"},
		"tier":       {`{"status":"ok","tier":"root","version":"v","now":1}`, "tier"},
		"embeddings": {`{"status":"ok","tier":"identity","version":"v","now":1,"embeddings":{"state":"warm","backlog":0,"rejected":0}}`, "embeddings.state"},
		"lock":       {`{"status":"ok","tier":"admin","version":"v","now":1,"maintenance":{"interval_seconds":1,"owner":"o","lock":"maybe","jobs":[]}}`, "maintenance.lock"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := &canned{status: 200, ctype: "application/json", body: tc.body}
			_, err := newBus(t, c).Health(context.Background())
			be := asBusError(t, err)
			if be.Code != ab2bus.CodeInternal || !strings.Contains(be.Message, tc.member) {
				t.Fatalf("err = %+v, want internal naming %q", be, tc.member)
			}
		})
	}
}

func TestHealthConvertsEveryMember(t *testing.T) {
	body := `{"status":"degraded","tier":"admin","version":"v1","now":9,"started_at":3,
	"store":{"engine":"sqlite","schema_version":6},
	"embeddings":{"state":"failing","backlog":2,"rejected":1,"model":"m","last_error":"e","last_error_at":4,"rejected_last":"r"},
	"storage":{"bytes":10,"budget_bytes":20,"evictable":true,"notice":"n"},
	"notices":[{"kind":"capacity","message":"full","set_at":5}],
	"maintenance":{"interval_seconds":60,"owner":"o","lock":"held","last_tick_at":7,"lock_error":"le","jobs":[{"name":"j","changed":2,"error":"x","skipped":true}]}}`
	c := &canned{status: 200, ctype: "application/json", body: body}
	h, err := newBus(t, c).Health(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m := h.Maintenance
	if h.Status != ab2bus.HealthDegraded || h.Tier != ab2bus.TierAdmin || h.Version != "v1" || h.Now != 9 || h.StartedAt != 3 ||
		h.Store == nil || *h.Store != (ab2bus.HealthStore{Engine: "sqlite", SchemaVersion: 6}) ||
		h.Embeddings == nil || *h.Embeddings != (ab2bus.HealthEmbeddings{State: ab2bus.EmbeddingsFailing, Backlog: 2, Rejected: 1, Model: "m", LastError: "e", LastErrorAt: 4, RejectedLast: "r"}) ||
		h.Storage == nil || *h.Storage != (ab2bus.Usage{Bytes: 10, BudgetBytes: 20, Evictable: true, Notice: "n"}) ||
		len(h.Notices) != 1 || h.Notices[0] != (ab2bus.Notice{Kind: "capacity", Message: "full", SetAt: 5}) ||
		m == nil || m.IntervalSeconds != 60 || m.Owner != "o" || m.Lock != ab2bus.LockHeld || m.LastTickAt != 7 || m.LockError != "le" ||
		len(m.Jobs) != 1 || m.Jobs[0] != (ab2bus.HealthJob{Name: "j", Changed: 2, Error: "x", Skipped: true}) {
		t.Fatalf("converted = %+v", h)
	}
}
