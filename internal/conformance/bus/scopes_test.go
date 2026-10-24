package bus

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/bus/local"
	"github.com/ericfitz/agentbus2/internal/core/scopes"
	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
)

// These tests drive the Bus in process (local.New) whatever the suite's
// transport: the wire carries no scope until Task 7 (scopes_wire_test.go runs
// the same cases through it).

// scopedIdentity creates an agent in scope (the scope must exist) and returns
// its in-process Bus.
func (h *harness) scopedIdentity(name, scope string) ab2bus.Bus {
	h.t.Helper()
	h.tx(func(ctx context.Context, tx ab2store.Tx) error {
		return tx.CreateScopedIdentity(ctx, name, scope, h.core.NowMs())
	})
	return local.New(h.core, name)
}

// localAs is as() over the in-process Bus.
func (h *harness) localAs(name string) ab2bus.Bus {
	h.t.Helper()
	h.ensureIdentity(name)
	return local.New(h.core, name)
}

// localAdmin is admin() over the in-process Bus.
func (h *harness) localAdmin(name string) ab2bus.Bus {
	h.t.Helper()
	h.admin(name)
	return local.New(h.core, name)
}

// msgOf is the error's message without the JSON encoding of Error().
func msgOf(err error) string {
	var be *ab2bus.Error
	if errors.As(err, &be) {
		return be.Message
	}
	return err.Error()
}

func TestScopesCore(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		sv := scopes.Service{Core: h.core}
		h.admin("root")
		h.as("pat")

		got, err := sv.Create(ctx, "root", ab2bus.CreateScopeInput{Name: "job1"})
		if err != nil || got.Name != "job1" || got.State != ab2bus.ScopeOpen || got.CreatedAt == 0 {
			t.Fatalf("Create = %+v, %v", got, err)
		}
		_, err = sv.Create(ctx, "root", ab2bus.CreateScopeInput{Name: "job1"})
		codeOf(t, err, ab2bus.CodeConflict)
		_, err = sv.Create(ctx, "pat", ab2bus.CreateScopeInput{Name: "job9"})
		codeOf(t, err, ab2bus.CodeForbidden)
		_, err = sv.Create(ctx, "root", ab2bus.CreateScopeInput{Name: "a/b"})
		codeOf(t, err, ab2bus.CodeValidation)
		_, err = sv.Get(ctx, "pat", "job1")
		codeOf(t, err, ab2bus.CodeForbidden)
		_, err = sv.List(ctx, "pat")
		codeOf(t, err, ab2bus.CodeForbidden)
		_, err = sv.Done(ctx, "pat", ab2bus.DoneScopeInput{Name: "job1", Retire: ab2bus.RetireNormal})
		codeOf(t, err, ab2bus.CodeForbidden)
		_, err = sv.Get(ctx, "root", "nope")
		codeOf(t, err, ab2bus.CodeNotFound)
		_, err = sv.Done(ctx, "root", ab2bus.DoneScopeInput{Name: "nope", Retire: ab2bus.RetireNormal})
		codeOf(t, err, ab2bus.CodeNotFound)

		// Get fills Channels and Identities; List leaves them empty.
		h.scopedIdentity("job1-coder", "job1")
		if _, err := h.localAdmin("root").CreateChannel(ctx, ab2bus.CreateChannelInput{Name: "job1-chat", Scope: "job1"}); err != nil {
			t.Fatal(err)
		}
		g, err := sv.Get(ctx, "root", "job1")
		if err != nil || !slices.Equal(g.Channels, []string{"job1-chat"}) || !slices.Equal(g.Identities, []string{"job1-coder"}) {
			t.Fatalf("Get = %+v, %v", g, err)
		}
		l, err := sv.List(ctx, "root")
		if err != nil || len(l) != 1 || l[0].Name != "job1" || l[0].Channels != nil || l[0].Identities != nil {
			t.Fatalf("List = %+v, %v", l, err)
		}

		// Done(normal), then done again, and nothing new joins.
		d, err := sv.Done(ctx, "root", ab2bus.DoneScopeInput{Name: "job1", Retire: ab2bus.RetireNormal})
		if err != nil || d.State != ab2bus.ScopeDone || d.Retire != ab2bus.RetireNormal || d.DoneAt == 0 {
			t.Fatalf("Done = %+v, %v", d, err)
		}
		_, err = sv.Done(ctx, "root", ab2bus.DoneScopeInput{Name: "job1", Retire: ab2bus.RetireNormal})
		codeOf(t, err, ab2bus.CodeConflict)

		// An unknown Retire is validation, whatever the scope's state.
		if _, err := sv.Create(ctx, "root", ab2bus.CreateScopeInput{Name: "job2"}); err != nil {
			t.Fatal(err)
		}
		for _, bad := range []ab2bus.RetireMode{"later", "", "NORMAL"} {
			_, err = sv.Done(ctx, "root", ab2bus.DoneScopeInput{Name: "job2", Retire: bad})
			codeOf(t, err, ab2bus.CodeValidation)
		}
		// with_bridge needs a live bridge.
		_, err = sv.Done(ctx, "root", ab2bus.DoneScopeInput{Name: "job2", Retire: ab2bus.RetireWithBridge})
		codeOf(t, err, ab2bus.CodeConflict)
		if !strings.Contains(msgOf(err), `scope "job2" has no live bridge`) {
			t.Errorf("error %q", err)
		}
		ch, err := h.localAdmin("root").CreateChannel(ctx, ab2bus.CreateChannelInput{Name: "job2-chat", Scope: "job2"})
		if err != nil {
			t.Fatal(err)
		}
		var chID int64
		h.tx(func(ctx context.Context, tx ab2store.Tx) error {
			c, err := tx.ChannelByName(ctx, ch.Name)
			chID = c.ID
			return err
		})
		var bridgeID int64
		h.tx(func(ctx context.Context, tx ab2store.Tx) error {
			b, err := tx.CreateBridge(ctx, ab2store.Bridge{ChannelID: chID, ChannelName: "job2-chat", Scope: "job2", SlackName: "ab-job2-chat", CreatedAt: 5})
			bridgeID = b.ID
			return err
		})
		// A creating bridge is not live.
		_, err = sv.Done(ctx, "root", ab2bus.DoneScopeInput{Name: "job2", Retire: ab2bus.RetireWithBridge})
		codeOf(t, err, ab2bus.CodeConflict)
		h.tx(func(ctx context.Context, tx ab2store.Tx) error {
			if err := tx.SetBridgeSlackChannel(ctx, bridgeID, "G2"); err != nil {
				return err
			}
			return tx.SetBridgeLive(ctx, bridgeID)
		})
		d, err = sv.Done(ctx, "root", ab2bus.DoneScopeInput{Name: "job2", Retire: ab2bus.RetireWithBridge})
		if err != nil || d.State != ab2bus.ScopeDone || d.Retire != ab2bus.RetireWithBridge {
			t.Fatalf("Done(with_bridge) = %+v, %v", d, err)
		}

		// Retired: Get and Done say so, List omits it.
		h.tx(func(ctx context.Context, tx ab2store.Tx) error {
			return tx.MarkScopeRetired(ctx, "job1", h.core.NowMs())
		})
		for name, call := range map[string]func() error{
			"Get": func() error { _, err := sv.Get(ctx, "root", "job1"); return err },
			"Done": func() error {
				_, err := sv.Done(ctx, "root", ab2bus.DoneScopeInput{Name: "job1", Retire: ab2bus.RetireNormal})
				return err
			},
		} {
			err := call()
			codeOf(t, err, ab2bus.CodeNotFound)
			if !strings.Contains(msgOf(err), `scope "job1" is retired`) {
				t.Errorf("%s: error %q", name, err)
			}
		}
		l, err = sv.List(ctx, "root")
		if err != nil || len(l) != 1 || l[0].Name != "job2" {
			t.Fatalf("List after retire = %+v, %v", l, err)
		}
	})
}

func TestScopedChannels(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		sv := scopes.Service{Core: h.core}
		admin := h.localAdmin("root")
		pat := h.localAs("pat")
		if _, err := sv.Create(ctx, "root", ab2bus.CreateScopeInput{Name: "job1"}); err != nil {
			t.Fatal(err)
		}
		coder := h.scopedIdentity("job1-coder", "job1")

		info, err := admin.CreateChannel(ctx, ab2bus.CreateChannelInput{Name: "job1-chat", Scope: "job1"})
		if err != nil || info.Scope != "job1" {
			t.Fatalf("admin scoped create = %+v, %v", info, err)
		}
		_, err = pat.CreateChannel(ctx, ab2bus.CreateChannelInput{Name: "p-chat", Scope: "job1"})
		codeOf(t, err, ab2bus.CodeForbidden)
		if !strings.Contains(err.Error(), "only an administrator may name a scope") {
			t.Errorf("error %q", err)
		}
		_, err = coder.CreateChannel(ctx, ab2bus.CreateChannelInput{Name: "c-chat", Scope: "job1"})
		codeOf(t, err, ab2bus.CodeForbidden)

		info, err = coder.CreateChannel(ctx, ab2bus.CreateChannelInput{Name: "job1-notes", Kind: ab2bus.KindMemory})
		if err != nil || info.Scope != "job1" {
			t.Fatalf("scoped creator = %+v, %v", info, err)
		}
		info, err = pat.CreateChannel(ctx, ab2bus.CreateChannelInput{Name: "pat-chat"})
		if err != nil || info.Scope != "" {
			t.Fatalf("unscoped creator = %+v, %v", info, err)
		}
		info, err = admin.CreateChannel(ctx, ab2bus.CreateChannelInput{Name: "root-chat"})
		if err != nil || info.Scope != "" {
			t.Fatalf("admin without a scope = %+v, %v", info, err)
		}
		_, err = admin.CreateChannel(ctx, ab2bus.CreateChannelInput{Name: "x-chat", Scope: "nope"})
		codeOf(t, err, ab2bus.CodeNotFound)

		// The same idempotency key with a different scope is a different payload.
		if _, err := sv.Create(ctx, "root", ab2bus.CreateScopeInput{Name: "job3"}); err != nil {
			t.Fatal(err)
		}
		if _, err := admin.CreateChannel(ctx, ab2bus.CreateChannelInput{Name: "k-chat", Scope: "job1", IdempotencyKey: "k1"}); err != nil {
			t.Fatal(err)
		}
		_, err = admin.CreateChannel(ctx, ab2bus.CreateChannelInput{Name: "k-chat", Scope: "job3", IdempotencyKey: "k1"})
		codeOf(t, err, ab2bus.CodeConflict)

		if _, err := sv.Done(ctx, "root", ab2bus.DoneScopeInput{Name: "job1", Retire: ab2bus.RetireNormal}); err != nil {
			t.Fatal(err)
		}
		_, err = admin.CreateChannel(ctx, ab2bus.CreateChannelInput{Name: "job1-late", Scope: "job1"})
		codeOf(t, err, ab2bus.CodeConflict)
		_, err = coder.CreateChannel(ctx, ab2bus.CreateChannelInput{Name: "job1-late2"})
		codeOf(t, err, ab2bus.CodeConflict)
		// An unscoped channel is still fine.
		if _, err := pat.CreateChannel(ctx, ab2bus.CreateChannelInput{Name: "pat-2"}); err != nil {
			t.Fatal(err)
		}
	})
}
