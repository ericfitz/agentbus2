package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/core/identities"
	"github.com/ericfitz/agentbus2/internal/version"
)

func runHealth(t *testing.T, e *testEnv, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := RunHealth(context.Background(), args, e.Environ, strings.NewReader(""), &out, &errb)
	return code, out.String(), errb.String()
}

func TestHealthPrintsDocument(t *testing.T) {
	e := newTestEnv(t)
	pat := e.conn(t, "pat")

	code, out, errs := runHealth(t, e, "-identity", "pat")
	if code != 0 || errs != "" {
		t.Fatalf("health: %d %q", code, errs)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, out)
	}
	store, _ := m["store"].(map[string]any)
	if m["tier"] != "identity" || m["status"] != "ok" || store["engine"] != "sqlite" {
		t.Fatalf("document = %s", out)
	}
	if m["protocol_version"] != version.Protocol {
		t.Fatalf("protocol_version = %v, want %q", m["protocol_version"], version.Protocol)
	}
	if _, ok := m["maintenance"]; ok {
		t.Fatalf("a non-administrator got maintenance: %s", out)
	}

	if err := (identities.Service{Core: pat.Core}).MakeAdmin(context.Background(), "pat", true); err != nil {
		t.Fatal(err)
	}
	code, out, errs = runHealth(t, e, "-identity", "pat")
	if code != 0 || errs != "" {
		t.Fatalf("admin health: %d %q", code, errs)
	}
	m = nil
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatal(err)
	}
	if m["tier"] != "admin" || m["maintenance"] == nil || m["notices"] == nil {
		t.Fatalf("admin document = %s", out)
	}
}

func TestHealthUsageErrors(t *testing.T) {
	e := newTestEnv(t)
	for _, args := range [][]string{{"extra"}, {"-nosuchflag"}, {"-profile", "nosuch"}} {
		code, out, errs := runHealth(t, e, args...)
		if code != 2 || out != "" || errs == "" {
			t.Errorf("%v: exit %d stdout %q stderr %q, want 2, no stdout, a message", args, code, out, errs)
		}
	}
	if code, _, errs := runHealth(t, e, "extra"); !strings.Contains(errs, "Usage of agentbus2 health") {
		t.Errorf("exit %d: stderr %q lacks the usage", code, errs)
	}
}
