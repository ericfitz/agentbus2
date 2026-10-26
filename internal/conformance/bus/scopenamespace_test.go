package bus

import (
	"context"
	"strings"
	"testing"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core/scopes"
	"github.com/ericfitz/agentbus2/internal/dbtest"
)

// TestScopeNamespace: channel names are scope-prefixed (accesscheck spec
// 4.2, Eric's decision 2026-10-25, option A), so a scoped identity's create
// or rename collision can only name a channel of its own scope.
func TestScopeNamespace(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		h.rtScope("job1")
		coder := h.scopedIdentity("job1-coder", "job1")
		pat := h.localAs("pat")
		root := h.localAdmin("root")
		create := func(b ab2bus.Bus, in ab2bus.CreateChannelInput) error {
			_, err := b.CreateChannel(ctx, in)
			return err
		}

		// A scoped identity names its channels <scope>-<rest>.
		err := create(coder, ab2bus.CreateChannelInput{Name: "x"})
		codeOf(t, err, ab2bus.CodeValidation)
		if want := `a channel of scope "job1" is named job1-<name>`; !strings.Contains(msgOf(err), want) {
			t.Errorf("message %q, want %q", msgOf(err), want)
		}
		if err := create(coder, ab2bus.CreateChannelInput{Name: "job1-x"}); err != nil {
			t.Fatalf("job1-x: %v", err)
		}
		// Nobody else takes the prefix without the scope.
		err = create(pat, ab2bus.CreateChannelInput{Name: "job1-y"})
		codeOf(t, err, ab2bus.CodeValidation)
		if want := `names starting with "job1"- belong to scope "job1"`; !strings.Contains(msgOf(err), want) {
			t.Errorf("message %q, want %q", msgOf(err), want)
		}
		err = create(root, ab2bus.CreateChannelInput{Name: "job1-q"})
		codeOf(t, err, ab2bus.CodeValidation)
		if err := create(root, ab2bus.CreateChannelInput{Name: "job1-z", Scope: "job1"}); err != nil {
			t.Fatalf("admin job1-z: %v", err)
		}
		// An administrator may still name a scope for an unprefixed name.
		if err := create(root, ab2bus.CreateChannelInput{Name: "k-chat", Scope: "job1"}); err != nil {
			t.Fatalf("k-chat: %v", err)
		}
		// A collision inside the scope is conflict and reveals nothing new.
		err = create(coder, ab2bus.CreateChannelInput{Name: "job1-z"})
		codeOf(t, err, ab2bus.CodeConflict)
		cs, err := coder.ListChannels(ctx)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, c := range cs {
			found = found || c.Name == "job1-z"
		}
		if !found {
			t.Errorf("job1-z missing from the scoped list %v", cs)
		}

		// Rename follows the same rules.
		if err := create(pat, ab2bus.CreateChannelInput{Name: "pat-c", ACL: nil}); err != nil {
			t.Fatal(err)
		}
		codeOf(t, coder.RenameChannel(ctx, ab2bus.RenameChannelInput{Name: "job1-x", NewName: "plain"}), ab2bus.CodeValidation)
		codeOf(t, coder.RenameChannel(ctx, ab2bus.RenameChannelInput{Name: "job1-x", NewName: "job1-z"}), ab2bus.CodeConflict)
		if err := coder.RenameChannel(ctx, ab2bus.RenameChannelInput{Name: "job1-x", NewName: "job1-x2"}); err != nil {
			t.Fatalf("scoped rename: %v", err)
		}
		codeOf(t, root.RenameChannel(ctx, ab2bus.RenameChannelInput{Name: "pat-c", NewName: "job1-pc"}), ab2bus.CodeValidation)
		if err := root.RenameChannel(ctx, ab2bus.RenameChannelInput{Name: "k-chat", NewName: "job1-k"}); err != nil {
			t.Fatalf("scoped channel into its prefix: %v", err)
		}

		// A scope cannot take a name prefix an unscoped or other-scope channel holds.
		if err := create(pat, ab2bus.CreateChannelInput{Name: "job2-old"}); err != nil {
			t.Fatal(err)
		}
		_, err = scopes.Service{Core: h.core}.Create(ctx, "root", ab2bus.CreateScopeInput{Name: "job2"})
		codeOf(t, err, ab2bus.CodeConflict)
		if want := `scope "job2": the name prefix "job2"- is in use by channel "job2-old"`; !strings.Contains(msgOf(err), want) {
			t.Errorf("message %q, want %q", msgOf(err), want)
		}

		// Scopes whose prefixes nest are refused in either creation order
		// (any state of the existing one), so the longer stays usable.
		sv := scopes.Service{Core: h.core}
		for _, c := range []struct{ first, second string }{{"ova", "ova-2"}, {"ovb-2", "ovb"}} {
			if _, err := sv.Create(ctx, "root", ab2bus.CreateScopeInput{Name: c.first}); err != nil {
				t.Fatal(err)
			}
			_, err := sv.Create(ctx, "root", ab2bus.CreateScopeInput{Name: c.second})
			codeOf(t, err, ab2bus.CodeConflict)
			want := `scope "` + c.second + `": the name prefix "` + c.second + `"- overlaps scope "` + c.first + `"`
			if msgOf(err) != want {
				t.Errorf("message %q, want %q", msgOf(err), want)
			}
		}
		// A done (and so retiring) scope still counts for the overlap.
		if _, err := sv.Create(ctx, "root", ab2bus.CreateScopeInput{Name: "ovd"}); err != nil {
			t.Fatal(err)
		}
		if _, err := sv.Done(ctx, "root", ab2bus.DoneScopeInput{Name: "ovd", Retire: ab2bus.RetireNormal}); err != nil {
			t.Fatal(err)
		}
		_, err = sv.Create(ctx, "root", ab2bus.CreateScopeInput{Name: "ovd-2"})
		codeOf(t, err, ab2bus.CodeConflict)
	})
}
