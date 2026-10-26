package bus

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
)

// These tests run through the suite's transport (newBus): the local Bus, or
// the remote Bus over the generated client when AGENTBUS2_BUS_TRANSPORT=inproc.

func TestScopesBus(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		ctx := context.Background()
		h := newHarness(t, e, harnessOptions{})
		root := h.admin("root").(ab2bus.Admin)
		pat := h.as("pat").(ab2bus.Admin)

		sc, err := root.CreateScope(ctx, ab2bus.CreateScopeInput{Name: "job1", IdempotencyKey: "k1"})
		if err != nil || sc.Name != "job1" || sc.State != ab2bus.ScopeOpen || sc.CreatedAt == 0 || sc.LastActivity == 0 {
			t.Fatalf("CreateScope = %+v, %v", sc, err)
		}
		// A replayed key returns the first result, not a conflict.
		again, err := root.CreateScope(ctx, ab2bus.CreateScopeInput{Name: "job1", IdempotencyKey: "k1"})
		if err != nil || !reflect.DeepEqual(again, sc) {
			t.Fatalf("replay = %+v, %v; want %+v", again, err, sc)
		}
		_, err = root.CreateScope(ctx, ab2bus.CreateScopeInput{Name: "job1"})
		codeOf(t, err, ab2bus.CodeConflict)

		// Members: a scoped channel and a scoped agent.
		if _, err := root.CreateAgentIdentity(ctx, ab2bus.CreateAgentIdentityInput{Name: "worker", Scope: "job1"}); err != nil {
			t.Fatal(err)
		}
		if _, err := h.admin("root").(ab2bus.Messaging).CreateChannel(ctx, ab2bus.CreateChannelInput{Name: "job1-chat", Scope: "job1"}); err != nil {
			t.Fatal(err)
		}
		list, err := root.ListScopes(ctx)
		if err != nil || len(list) != 1 || list[0].Name != "job1" || list[0].Channels != nil || list[0].Identities != nil {
			t.Fatalf("ListScopes = %+v, %v", list, err)
		}
		got, err := root.GetScope(ctx, "job1")
		if err != nil || !slices.Equal(got.Channels, []string{"job1-chat"}) || !slices.Equal(got.Identities, []string{"worker"}) {
			t.Fatalf("GetScope = %+v, %v", got, err)
		}

		done, err := root.DoneScope(ctx, ab2bus.DoneScopeInput{Name: "job1", Retire: ab2bus.RetireNormal, IdempotencyKey: "d1"})
		if err != nil || done.State != ab2bus.ScopeDone || done.Retire != ab2bus.RetireNormal || done.DoneAt == 0 {
			t.Fatalf("DoneScope = %+v, %v", done, err)
		}
		replay, err := root.DoneScope(ctx, ab2bus.DoneScopeInput{Name: "job1", Retire: ab2bus.RetireNormal, IdempotencyKey: "d1"})
		if err != nil || replay.State != ab2bus.ScopeDone || replay.DoneAt != done.DoneAt {
			t.Fatalf("done replay = %+v, %v", replay, err)
		}
		_, err = root.DoneScope(ctx, ab2bus.DoneScopeInput{Name: "job1", Retire: ab2bus.RetireNormal})
		codeOf(t, err, ab2bus.CodeConflict)
		_, err = root.DoneScope(ctx, ab2bus.DoneScopeInput{Name: "nope", Retire: ab2bus.RetireNormal})
		codeOf(t, err, ab2bus.CodeNotFound)
		_, err = root.DoneScope(ctx, ab2bus.DoneScopeInput{Name: "job1", Retire: "later"})
		codeOf(t, err, ab2bus.CodeValidation)

		// A non-admin is refused everywhere.
		_, err = pat.CreateScope(ctx, ab2bus.CreateScopeInput{Name: "x"})
		codeOf(t, err, ab2bus.CodeForbidden)
		_, err = pat.ListScopes(ctx)
		codeOf(t, err, ab2bus.CodeForbidden)
		_, err = pat.GetScope(ctx, "job1")
		codeOf(t, err, ab2bus.CodeForbidden)
		_, err = pat.DoneScope(ctx, ab2bus.DoneScopeInput{Name: "job1", Retire: ab2bus.RetireNormal})
		codeOf(t, err, ab2bus.CodeForbidden)

		// A retired scope is not_found with the retired text.
		h.tx(func(ctx context.Context, tx ab2store.Tx) error {
			return tx.MarkScopeRetired(ctx, "job1", h.core.NowMs())
		})
		_, err = root.GetScope(ctx, "job1")
		codeOf(t, err, ab2bus.CodeNotFound)
		if !strings.Contains(msgOf(err), "is retired") {
			t.Fatalf("message = %q", msgOf(err))
		}
		list, err = root.ListScopes(ctx)
		if err != nil || len(list) != 0 {
			t.Fatalf("ListScopes after retire = %+v, %v", list, err)
		}
	})
}

func TestCreateAgentIdentityBus(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		ctx := context.Background()
		h := newHarness(t, e, harnessOptions{})
		root := h.admin("root").(ab2bus.Admin)
		pat := h.as("pat").(ab2bus.Admin)

		res, err := root.CreateAgentIdentity(ctx, ab2bus.CreateAgentIdentityInput{Name: "worker"})
		if err != nil || res.Identity != "worker" || !res.Created || res.EnrollmentToken == "" || res.ExpiresAt <= h.core.NowMs() || res.Scope != "" {
			t.Fatalf("CreateAgentIdentity = %+v, %v", res, err)
		}
		if _, err := root.CreateScope(ctx, ab2bus.CreateScopeInput{Name: "job1"}); err != nil {
			t.Fatal(err)
		}
		sc, err := root.CreateAgentIdentity(ctx, ab2bus.CreateAgentIdentityInput{Name: "scoped", Scope: "job1"})
		if err != nil || sc.Scope != "job1" || !sc.Created || sc.EnrollmentToken == "" {
			t.Fatalf("scoped = %+v, %v", sc, err)
		}
		_, err = pat.CreateAgentIdentity(ctx, ab2bus.CreateAgentIdentityInput{Name: "other"})
		codeOf(t, err, ab2bus.CodeForbidden)
		if hasIdentity(t, h, "other") {
			t.Fatal("a refused request created the identity")
		}
		_, err = root.CreateAgentIdentity(ctx, ab2bus.CreateAgentIdentityInput{Name: "everyone"})
		codeOf(t, err, ab2bus.CodeValidation)
		_, err = root.CreateAgentIdentity(ctx, ab2bus.CreateAgentIdentityInput{Name: "x", Scope: "nope"})
		codeOf(t, err, ab2bus.CodeNotFound)

		if _, err := root.DoneScope(ctx, ab2bus.DoneScopeInput{Name: "job1", Retire: ab2bus.RetireNormal}); err != nil {
			t.Fatal(err)
		}
		_, err = root.CreateAgentIdentity(ctx, ab2bus.CreateAgentIdentityInput{Name: "late", Scope: "job1"})
		codeOf(t, err, ab2bus.CodeConflict)
		if hasIdentity(t, h, "late") {
			t.Fatal("a refused request created the identity")
		}
	})
}

func TestChannelScopeBus(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		ctx := context.Background()
		h := newHarness(t, e, harnessOptions{})
		root := h.admin("root")
		adm := root.(ab2bus.Admin)
		if _, err := adm.CreateScope(ctx, ab2bus.CreateScopeInput{Name: "job1"}); err != nil {
			t.Fatal(err)
		}
		info, err := root.CreateChannel(ctx, ab2bus.CreateChannelInput{Name: "c1", Scope: "job1"})
		if err != nil || info.Scope != "job1" {
			t.Fatalf("CreateChannel = %+v, %v", info, err)
		}
		if _, err := root.CreateChannel(ctx, ab2bus.CreateChannelInput{Name: "c2"}); err != nil {
			t.Fatal(err)
		}
		st, err := adm.Status(ctx)
		if err != nil {
			t.Fatal(err)
		}
		scopes := map[string]string{}
		for _, c := range st.Channels {
			scopes[c.Name] = c.Scope
		}
		if scopes["c1"] != "job1" || scopes["c2"] != "" {
			t.Fatalf("Status scopes = %v", scopes)
		}
		_, err = root.CreateChannel(ctx, ab2bus.CreateChannelInput{Name: "c3", Scope: "nope"})
		codeOf(t, err, ab2bus.CodeNotFound)
	})
}

// With no Slack bridge wired (local mode, or a server without Options.Bridges)
// every bridge call is unavailable and not retryable, with the fixed text
// (Eric 2026-10-25).
func TestBridgesNotConfigured(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		ctx := context.Background()
		h := newHarness(t, e, harnessOptions{})
		root := h.admin("root").(ab2bus.Admin)
		check := func(name string, err error) {
			t.Helper()
			var be *ab2bus.Error
			if !errors.As(err, &be) || be.Code != ab2bus.CodeUnavailable || be.Message != ab2bus.BridgeNotConfigured || be.Retryable {
				t.Errorf("%s: err = %v, want unavailable, retryable false, %q", name, err, ab2bus.BridgeNotConfigured)
			}
		}
		_, err := root.CreateBridge(ctx, ab2bus.CreateBridgeInput{Channel: "c"})
		check("CreateBridge", err)
		_, err = root.ListBridges(ctx)
		check("ListBridges", err)
		_, err = root.GetBridge(ctx, "c")
		check("GetBridge", err)
		check("RemoveBridge", root.RemoveBridge(ctx, ab2bus.RemoveBridgeInput{Channel: "c"}))
	})
}

// TestBridgesNotConfiguredNotRetryable: under AGENTBUS2_BUS_TRANSPORT=inproc
// the retryable flag survives the problem+json round trip as false, so no
// client retries on the unavailable code alone.
func TestBridgesNotConfiguredNotRetryable(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		_, err := h.admin("root").(ab2bus.Admin).ListBridges(context.Background())
		var be *ab2bus.Error
		if !errors.As(err, &be) || be.Retryable || be.Code != ab2bus.CodeUnavailable {
			t.Fatalf("err = %#v, want unavailable with Retryable false", err)
		}
	})
}
