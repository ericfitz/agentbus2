package bus

import (
	"context"
	"reflect"
	"regexp"
	"testing"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core/channels"
	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
)

// TestNoImplicitCreatorEntry: the implicit creator entry of ADR 0008
// decision 1 is retired (accesscheck spec 3.13). A creator with no stored
// admin row is forbidden; a server administrator manages the channel and
// grants the creator a row.
func TestNoImplicitCreatorEntry(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		pat := h.as("pat")
		root := h.admin("root")
		if _, err := pat.CreateChannel(ctx, ab2bus.CreateChannelInput{Name: "c", ACL: []ab2bus.ACLEntry{}}); err != nil {
			t.Fatal(err)
		}
		_, err := pat.ACL(ctx, "c")
		codeOf(t, err, ab2bus.CodeForbidden)
		codeOf(t, pat.SetACL(ctx, ab2bus.SetACLInput{Channel: "c"}), ab2bus.CodeForbidden)
		codeOf(t, pat.RenameChannel(ctx, ab2bus.RenameChannelInput{Name: "c", NewName: "d"}), ab2bus.CodeForbidden)
		codeOf(t, pat.DeleteChannel(ctx, ab2bus.DeleteChannelInput{Name: "c"}), ab2bus.CodeForbidden)

		grant := []ab2bus.ACLEntry{{Identity: "pat", Permissions: []ab2bus.Permission{ab2bus.PermRead, ab2bus.PermWrite, ab2bus.PermAdmin}}}
		if err := root.SetACL(ctx, ab2bus.SetACLInput{Channel: "c", Entries: grant}); err != nil {
			t.Fatalf("administrator SetACL: %v", err)
		}
		if acl, err := pat.ACL(ctx, "c"); err != nil || !reflect.DeepEqual(acl, grant) {
			t.Fatalf("creator with a stored row: ACL = %+v, %v", acl, err)
		}
	})
}

// TestScopeGateChannels: a scoped identity reaches only the channels of its
// scope; everything else answers as for a name that was never created
// (accesschecks spec 3.12, Eric's decision 4.1 (d)). An unscoped identity is
// unchanged.
func TestScopeGateChannels(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		h.rtScope("job1")
		coder := h.scopedIdentity("job1-coder", "job1")
		pat := h.localAs("pat")
		root := h.localAdmin("root")
		if err := (channels.Service{Core: h.core}).EnsureGeneral(ctx); err != nil {
			t.Fatal(err)
		}
		work, err := coder.CreateChannel(ctx, ab2bus.CreateChannelInput{Name: "work"})
		if err != nil || work.Scope != "job1" {
			t.Fatalf("scoped create = %+v, %v", work, err)
		}
		if _, err := pat.CreateChannel(ctx, ab2bus.CreateChannelInput{Name: "other"}); err != nil {
			t.Fatal(err)
		}
		if _, err := coder.Send(ctx, ab2bus.SendInput{Channel: "work", Content: "hi"}); err != nil {
			t.Fatalf("send inside the scope: %v", err)
		}
		if _, err := coder.ACL(ctx, "work"); err != nil {
			t.Fatalf("ACL inside the scope: %v", err)
		}

		_, neverErr := coder.History(ctx, ab2bus.HistoryInput{Channel: "never-created"})
		codeOf(t, neverErr, ab2bus.CodeNotFound)
		for _, name := range []string{"other", "general"} {
			want := `channel "` + name + `" does not exist`
			calls := map[string]func() error{
				"Send":    func() error { _, err := coder.Send(ctx, ab2bus.SendInput{Channel: name, Content: "x"}); return err },
				"History": func() error { _, err := coder.History(ctx, ab2bus.HistoryInput{Channel: name}); return err },
				"Subscribe": func() error {
					return coder.Subscribe(ctx, ab2bus.SubscribeInput{Channel: name})
				},
				"Unsubscribe": func() error { return coder.Unsubscribe(ctx, ab2bus.UnsubscribeInput{Channel: name}) },
				"ACL":         func() error { _, err := coder.ACL(ctx, name); return err },
				"SetACL":      func() error { return coder.SetACL(ctx, ab2bus.SetACLInput{Channel: name}) },
				"Rename": func() error {
					return coder.RenameChannel(ctx, ab2bus.RenameChannelInput{Name: name, NewName: "zz"})
				},
				"Delete": func() error { return coder.DeleteChannel(ctx, ab2bus.DeleteChannelInput{Name: name}) },
				"Search": func() error {
					_, err := coder.Search(ctx, ab2bus.SearchInput{Query: "x", Channel: name})
					return err
				},
			}
			for op, call := range calls {
				err := call()
				codeOf(t, err, ab2bus.CodeNotFound)
				if got := msgOf(err); got != want {
					t.Errorf("%s on %s: message %q, want %q", op, name, got, want)
				}
			}
		}
		if got, want := msgOf(neverErr), `channel "never-created" does not exist`; got != want {
			t.Errorf("never-created: message %q, want %q", got, want)
		}

		// The set form: the list is the scope's channels only.
		list := func(b ab2bus.Bus) []string {
			t.Helper()
			cs, err := b.ListChannels(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, c := range cs {
				names = append(names, c.Name)
			}
			return names
		}
		if got := list(coder); !reflect.DeepEqual(got, []string{"work"}) {
			t.Fatalf("scoped list = %v, want [work]", got)
		}
		if _, err := coder.CreateChannel(ctx, ab2bus.CreateChannelInput{Name: "next"}); err != nil {
			t.Fatal(err)
		}
		if got := list(coder); !reflect.DeepEqual(got, []string{"next", "work"}) {
			t.Fatalf("scoped list = %v, want [next work]", got)
		}
		if got := list(pat); len(got) != 4 { // Decision 94, interim: every channel
			t.Fatalf("unscoped list = %v, want all four channels", got)
		}

		// An ACL identity the granter cannot see is the Decision 96 text.
		err = coder.SetACL(ctx, ab2bus.SetACLInput{Channel: "work", Entries: []ab2bus.ACLEntry{{Identity: "pat", Permissions: []ab2bus.Permission{ab2bus.PermRead}}}})
		codeOf(t, err, ab2bus.CodeValidation)
		if got, want := msgOf(err), `ACL identity "pat" is not a registered identity`; got != want {
			t.Errorf("scoped SetACL naming pat: %q, want %q", got, want)
		}
		_, err = coder.CreateChannel(ctx, ab2bus.CreateChannelInput{Name: "acl2", ACL: []ab2bus.ACLEntry{{Identity: "pat", Permissions: []ab2bus.Permission{ab2bus.PermRead}}}})
		codeOf(t, err, ab2bus.CodeValidation)
		// The administrator may name it; the row is inert for pat (the gate runs first).
		if err := root.SetACL(ctx, ab2bus.SetACLInput{Channel: "work", Entries: []ab2bus.ACLEntry{{Identity: "pat", Permissions: []ab2bus.Permission{ab2bus.PermRead, ab2bus.PermWrite}}}}); err != nil {
			t.Fatalf("administrator SetACL naming pat: %v", err)
		}
		_, err = pat.History(ctx, ab2bus.HistoryInput{Channel: "work"})
		if err != nil {
			t.Fatalf("unscoped pat with a row: %v", err) // the gate never restricts an unscoped principal
		}

		// A task owner outside the scope is unregistered for the scoped caller.
		if _, err := coder.CreateChannel(ctx, ab2bus.CreateChannelInput{Name: "jobs", Kind: ab2bus.KindTasks}); err != nil {
			t.Fatal(err)
		}
		task, err := coder.Create(ctx, ab2bus.TaskCreateInput{Channel: "jobs", Subject: "t"})
		if err != nil {
			t.Fatal(err)
		}
		owner := "pat"
		_, err = coder.Update(ctx, ab2bus.TaskPatch{ID: task.ID, Owner: &owner})
		codeOf(t, err, ab2bus.CodeNotFound)
		if got, want := msgOf(err), `identity "pat" is not registered`; got != want {
			t.Errorf("owner outside the scope: %q, want %q", got, want)
		}

		// Creation: an existing out-of-scope name is conflict until Task 11
		// (scope-prefixed names) makes it validation.
		_, err = coder.CreateChannel(ctx, ab2bus.CreateChannelInput{Name: "other"})
		codeOf(t, err, ab2bus.CodeConflict)
		_, err = coder.CreateChannel(ctx, ab2bus.CreateChannelInput{Name: "named", Scope: "job1"})
		codeOf(t, err, ab2bus.CodeForbidden)
		if got, want := msgOf(err), "only an administrator may name a scope"; got != want {
			t.Errorf("scoped create naming a scope: %q, want %q", got, want)
		}
	})
}

// TestCreateScopeNamingText pins the two answers of naming a scope.
func TestCreateScopeNamingText(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		pat := h.as("pat")
		root := h.admin("root")
		_, err := pat.CreateChannel(ctx, ab2bus.CreateChannelInput{Name: "x", Scope: "job1"})
		codeOf(t, err, ab2bus.CodeForbidden)
		if got, want := msgOf(err), "only an administrator may name a scope"; got != want {
			t.Errorf("non-admin naming a scope: %q, want %q", got, want)
		}
		_, err = root.CreateChannel(ctx, ab2bus.CreateChannelInput{Name: "x", Scope: "nope"})
		codeOf(t, err, ab2bus.CodeNotFound)
	})
}

var digits = regexp.MustCompile(`[0-9]+`)

// TestScopeGateByID: a memory or a task on a channel outside a scoped
// caller's scope answers exactly as an id that does not exist (spec 4.1 (d));
// in scope the ACL decides; an unscoped caller is unchanged.
func TestScopeGateByID(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		h.rtScope("job1")
		coder := h.scopedIdentity("job1-coder", "job1")
		pat := h.localAs("pat")

		if _, err := pat.CreateChannel(ctx, ab2bus.CreateChannelInput{Name: "elsewhere", Kind: ab2bus.KindMemory}); err != nil {
			t.Fatal(err)
		}
		if _, err := coder.CreateChannel(ctx, ab2bus.CreateChannelInput{Name: "mine", Kind: ab2bus.KindMemory}); err != nil {
			t.Fatal(err)
		}
		if _, err := pat.CreateChannel(ctx, ab2bus.CreateChannelInput{Name: "elsewhere-tasks", Kind: ab2bus.KindTasks}); err != nil {
			t.Fatal(err)
		}
		memOf := func(ch string) int64 {
			t.Helper()
			var id int64
			var c ab2store.Channel
			h.tx(func(ctx context.Context, tx ab2store.Tx) error {
				var err error
				c, err = tx.ChannelByName(ctx, ch)
				return err
			})
			seq := h.append(c, "pat", "", "remember")
			h.tx(func(ctx context.Context, tx ab2store.Tx) error {
				ms, err := tx.MessagesBySeqs(ctx, []int64{seq})
				if err != nil || len(ms) != 1 {
					return err
				}
				if ms[0].MemoryID != nil {
					id = *ms[0].MemoryID
				}
				return nil
			})
			if id == 0 {
				t.Fatalf("no memory id on %s", ch)
			}
			return id
		}
		foreign, own := memOf("elsewhere"), memOf("mine")
		const unknown = 987654
		task, err := pat.Create(ctx, ab2bus.TaskCreateInput{Channel: "elsewhere-tasks", Subject: "t"})
		if err != nil {
			t.Fatal(err)
		}

		same := func(name string, foreignCall, unknownCall func() error) {
			t.Helper()
			fe, ue := foreignCall(), unknownCall()
			codeOf(t, fe, ab2bus.CodeNotFound)
			codeOf(t, ue, ab2bus.CodeNotFound)
			// The ids differ in the text, so compare with the digits masked.
			mask := func(s string) string { return digits.ReplaceAllString(s, "N") }
			if mask(msgOf(fe)) != mask(msgOf(ue)) {
				t.Errorf("%s: out-of-scope %q differs from unknown %q", name, msgOf(fe), msgOf(ue))
			}
		}
		same("GetMemory", func() error { _, err := coder.GetMemory(ctx, foreign); return err }, func() error { _, err := coder.GetMemory(ctx, unknown); return err })
		same("Revisions", func() error { _, err := coder.Revisions(ctx, foreign); return err }, func() error { _, err := coder.Revisions(ctx, unknown); return err })
		same("Edit", func() error {
			c := "x"
			_, err := coder.Edit(ctx, ab2bus.EditMemoryInput{ID: foreign, Content: &c})
			return err
		}, func() error {
			c := "x"
			_, err := coder.Edit(ctx, ab2bus.EditMemoryInput{ID: unknown, Content: &c})
			return err
		})
		same("Delete", func() error { return coder.Delete(ctx, ab2bus.DeleteMemoryInput{ID: foreign}) }, func() error { return coder.Delete(ctx, ab2bus.DeleteMemoryInput{ID: unknown}) })
		same("GetTask", func() error { _, err := coder.GetTask(ctx, task.ID); return err }, func() error { _, err := coder.GetTask(ctx, unknown); return err })
		same("task Update", func() error {
			s := "x"
			_, err := coder.Update(ctx, ab2bus.TaskPatch{ID: task.ID, Subject: &s})
			return err
		}, func() error {
			s := "x"
			_, err := coder.Update(ctx, ab2bus.TaskPatch{ID: unknown, Subject: &s})
			return err
		})
		_, err = coder.Create(ctx, ab2bus.TaskCreateInput{Channel: "elsewhere-tasks", Subject: "x"})
		codeOf(t, err, ab2bus.CodeNotFound)
		_, err = coder.List(ctx, ab2bus.TaskListInput{Channel: "elsewhere-tasks"})
		codeOf(t, err, ab2bus.CodeNotFound)

		if _, err := coder.GetMemory(ctx, own); err != nil {
			t.Errorf("GetMemory in scope: %v", err)
		}
		if _, err := pat.GetMemory(ctx, own); err != nil {
			t.Errorf("unscoped GetMemory of a scoped channel: %v", err)
		}
		if _, err := pat.GetMemory(ctx, foreign); err != nil {
			t.Errorf("unscoped GetMemory: %v", err)
		}
	})
}
