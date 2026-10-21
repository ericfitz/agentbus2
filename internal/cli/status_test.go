package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core/identities"
)

func TestStatusReport(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	pat := e.conn(t, "pat")
	s, err := pat.Bus.Open(ctx, bus.OpenSessionInput{})
	if err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		code := RunStatus(ctx, args, e.Environ, strings.NewReader(""), &out, &errb)
		return code, out.String(), errb.String()
	}

	code, out, errs := run("-identity", "pat")
	if code != 0 {
		t.Fatalf("status: %d %q", code, errs)
	}
	for _, want := range []string{"profile: default (local, " + e.DataDir + "/agentbus2.sqlite)", "identity: pat", "  " + s.DisplayName + " (pat) since ", "  general [ordinary]"} {
		if !strings.Contains(out, want) {
			t.Errorf("report lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "embedding") {
		t.Errorf("a non-administrator sees the embedding lines:\n%s", out)
	}

	if err := (identities.Service{Core: pat.Core}).MakeAdmin(ctx, "pat", true); err != nil {
		t.Fatal(err)
	}
	code, out, errs = run("-identity", "pat")
	if code != 0 {
		t.Fatalf("admin status: %d %q", code, errs)
	}
	for _, want := range []string{"embedding backlog: ", "general [ordinary] created by "} {
		if !strings.Contains(out, want) {
			t.Errorf("admin report lacks %q:\n%s", want, out)
		}
	}
}

func TestStatusRejectsBadInput(t *testing.T) {
	e := newTestEnv(t)
	for _, args := range [][]string{{"extra"}, {"-nosuchflag"}, {"-profile", "nosuch"}} {
		var out, errb bytes.Buffer
		if code := RunStatus(context.Background(), args, e.Environ, strings.NewReader(""), &out, &errb); code != 2 {
			t.Errorf("%v: exit %d (stderr %q), want 2", args, code, errb.String())
		}
	}
}
