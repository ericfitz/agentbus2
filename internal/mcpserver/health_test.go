package mcpserver_test

import (
	"context"
	"testing"

	"github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/version"
)

func TestHealthTool(t *testing.T) {
	e := newTestEnv(t)
	r := startServer(t, e, newFakeTable(), "test-client")
	r.must(t, "register", nil)

	got := r.must(t, "health", nil)
	if got["status"] != "ok" || got["tier"] != "identity" {
		t.Fatalf("identity tier: %v", got)
	}
	if got["protocol_version"] != version.Protocol {
		t.Errorf("protocol_version = %v, want %q", got["protocol_version"], version.Protocol)
	}
	if st, _ := got["store"].(map[string]any); st["engine"] != "sqlite" {
		t.Errorf("store = %v", got["store"])
	}
	if em, _ := got["embeddings"].(map[string]any); em["state"] != "off" {
		t.Errorf("embeddings = %v", got["embeddings"])
	}
	for _, k := range []string{"storage", "notices", "maintenance"} {
		if _, ok := got[k]; ok {
			t.Errorf("identity tier carries %q", k)
		}
	}

	// Flag the identity administrator through the same database.
	c := e.conn(t, "pat")
	if err := c.Core.Store.Tx(context.Background(), func(ctx context.Context, tx store.Tx) error {
		return tx.SetAdmin(ctx, "pat", true)
	}); err != nil {
		t.Fatal(err)
	}
	got = r.must(t, "health", nil)
	if got["tier"] != "admin" {
		t.Fatalf("admin tier: %v", got)
	}
	m, _ := got["maintenance"].(map[string]any)
	if m["lock"] != "unknown" {
		t.Errorf("maintenance = %v", m)
	}
	if jobs, ok := m["jobs"].([]any); !ok || len(jobs) != 0 {
		t.Errorf("jobs = %#v, want an empty array", m["jobs"])
	}
	if n, ok := got["notices"].([]any); !ok || len(n) != 0 {
		t.Errorf("notices = %#v, want an empty array", got["notices"])
	}
	if s, _ := got["storage"].(map[string]any); s == nil || s["bytes"].(float64) <= 0 {
		t.Errorf("storage = %v", got["storage"])
	}
}
