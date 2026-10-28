package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core/identities"
	"github.com/ericfitz/agentbus2/internal/store"
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
	for _, want := range []string{"profile: default (local, " + filepath.Join(e.DataDir, "agentbus2.sqlite") + ")", "identity: pat", "  " + s.DisplayName + " (pat) since ", "  general [ordinary]"} {
		if !strings.Contains(out, want) {
			t.Errorf("report lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "embedding") || strings.Contains(out, "usage:") {
		t.Errorf("a non-administrator sees the administrator lines:\n%s", out)
	}

	if err := (identities.Service{Core: pat.Core}).MakeAdmin(ctx, "pat", true); err != nil {
		t.Fatal(err)
	}
	code, out, errs = run("-identity", "pat")
	if code != 0 {
		t.Fatalf("admin status: %d %q", code, errs)
	}
	// SQLite is evictable, so the budget is shown.
	if !regexp.MustCompile(`(?m)^usage: \d+\.\d MiB of \d+ MiB budget \(disk \d+\.\d MiB, \d+\.\d\dx, normal\)$`).MatchString(out) {
		t.Errorf("admin report lacks the usage line:\n%s", out)
	}
	for _, want := range []string{"embedding backlog: ", "general [ordinary] created by "} {
		if !strings.Contains(out, want) {
			t.Errorf("admin report lacks %q:\n%s", want, out)
		}
	}
}

func TestStatusMarksBridged(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	pat := e.conn(t, "pat")
	err := pat.Core.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		if err := tx.CreateScope(ctx, "job1", 1); err != nil {
			return err
		}
		seq, err := tx.MaxSeq(ctx)
		if err != nil {
			return err
		}
		ch, err := tx.CreateChannel(ctx, store.ChannelCreate{
			Name: "job1-chat", Kind: store.KindOrdinary, Creator: "pat", Scope: "job1",
			ACL:        []store.ACLEntry{{Identity: "everyone", Permissions: []store.Permission{store.PermRead, store.PermWrite}}},
			CreatedSeq: seq, CreatedAt: 2,
		})
		if err != nil {
			return err
		}
		b, err := tx.CreateBridge(ctx, store.Bridge{ChannelID: ch.ID, ChannelName: ch.Name, Scope: "job1", SlackName: "ab-job1-chat", CreatedAt: 3})
		if err != nil {
			return err
		}
		if err := tx.SetBridgeSlackChannel(ctx, b.ID, "G1"); err != nil {
			return err
		}
		return tx.SetBridgeLive(ctx, b.ID)
	})
	if err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := RunStatus(ctx, []string{"-identity", "pat"}, e.Environ, strings.NewReader(""), &out, &errb); code != 0 {
		t.Fatalf("status: %d %q", code, errb.String())
	}
	if !strings.Contains(out.String(), "\n  job1-chat [ordinary] bridged\n") {
		t.Errorf("report lacks the bridged row:\n%s", out.String())
	}
	if strings.Contains(out.String(), "general [ordinary] bridged") {
		t.Errorf("an unbridged channel is marked bridged:\n%s", out.String())
	}
	// The administrator's row carries the creator and then the mark.
	if err := (identities.Service{Core: pat.Core}).MakeAdmin(ctx, "pat", true); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := RunStatus(ctx, []string{"-identity", "pat"}, e.Environ, strings.NewReader(""), &out, &errb); code != 0 {
		t.Fatalf("status as an administrator: %d %q", code, errb.String())
	}
	if !strings.Contains(out.String(), "\n  job1-chat [ordinary] created by pat bridged\n") {
		t.Errorf("the administrator's report lacks the creator and the mark:\n%s", out.String())
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

func TestStatusUsageLine(t *testing.T) {
	for _, tc := range []struct {
		u    bus.Usage
		want string
	}{
		{bus.Usage{Bytes: 3 << 19, BudgetBytes: 512 << 20, Evictable: true, DiskBytes: 3 << 20, DiskRatio: 2, Tier: bus.PressureNormal}, "usage: 1.5 MiB of 512 MiB budget (disk 3.0 MiB, 2.00x, normal)"},
		{bus.Usage{Bytes: 3 << 19, BudgetBytes: 512 << 20, DiskBytes: 3 << 20, DiskRatio: 2, Tier: bus.PressureNormal}, "usage: 1.5 MiB (disk 3.0 MiB, 2.00x, normal)"},
	} {
		if got := usageLine(tc.u); got != tc.want {
			t.Errorf("usageLine(%+v) = %q, want %q", tc.u, got, tc.want)
		}
	}
}
