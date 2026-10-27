package bus

import (
	"context"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core/access"
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
// (accesscheck spec 3.12, Eric's decision 4.1 (d)). An unscoped identity is
// unchanged.
func TestScopeGateChannels(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		h.rtScope("job1")
		coder := h.scopedIdentity("job1-coder", "job1")
		pat := h.localAs("pat")
		root := h.localAdmin("root")
		h.human("eric@corp.example")
		if err := (channels.Service{Core: h.core}).EnsureGeneral(ctx); err != nil {
			t.Fatal(err)
		}
		work, err := coder.CreateChannel(ctx, ab2bus.CreateChannelInput{Name: "job1-work"})
		if err != nil || work.Scope != "job1" {
			t.Fatalf("scoped create = %+v, %v", work, err)
		}
		if _, err := pat.CreateChannel(ctx, ab2bus.CreateChannelInput{Name: "other"}); err != nil {
			t.Fatal(err)
		}
		if _, err := coder.Send(ctx, ab2bus.SendInput{Channel: "job1-work", Content: "hi"}); err != nil {
			t.Fatalf("send inside the scope: %v", err)
		}
		if _, err := coder.ACL(ctx, "job1-work"); err != nil {
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
		if got := list(coder); !reflect.DeepEqual(got, []string{"job1-work"}) {
			t.Fatalf("scoped list = %v, want [job1-work]", got)
		}
		if _, err := coder.CreateChannel(ctx, ab2bus.CreateChannelInput{Name: "job1-next"}); err != nil {
			t.Fatal(err)
		}
		if got := list(coder); !reflect.DeepEqual(got, []string{"job1-next", "job1-work"}) {
			t.Fatalf("scoped list = %v, want [job1-next job1-work]", got)
		}
		if got := list(pat); len(got) != 4 { // every channel here carries the default everyone row
			t.Fatalf("unscoped list = %v, want all four channels", got)
		}

		// An ACL identity the granter cannot see is the Decision 96 text.
		// (the default entries stay: an agent cannot remove them, spec 4.6)
		err = coder.SetACL(ctx, ab2bus.SetACLInput{Channel: "job1-work", Entries: []ab2bus.ACLEntry{
			{Identity: ab2store.Everyone, Permissions: []ab2bus.Permission{ab2bus.PermRead, ab2bus.PermWrite}},
			{Identity: ab2store.Admins, Permissions: []ab2bus.Permission{ab2bus.PermAdmin}},
			{Identity: "pat", Permissions: []ab2bus.Permission{ab2bus.PermRead}}}})
		codeOf(t, err, ab2bus.CodeValidation)
		if got, want := msgOf(err), `ACL identity "pat" is not a registered identity`; got != want {
			t.Errorf("scoped SetACL naming pat: %q, want %q", got, want)
		}
		_, err = coder.CreateChannel(ctx, ab2bus.CreateChannelInput{Name: "job1-acl2", ACL: []ab2bus.ACLEntry{{Identity: "pat", Permissions: []ab2bus.Permission{ab2bus.PermRead}}}})
		codeOf(t, err, ab2bus.CodeValidation)
		// The administrator may name it; the row is inert for pat (the gate runs first).
		if err := root.SetACL(ctx, ab2bus.SetACLInput{Channel: "job1-work", Entries: []ab2bus.ACLEntry{{Identity: "pat", Permissions: []ab2bus.Permission{ab2bus.PermRead, ab2bus.PermWrite}}}}); err != nil {
			t.Fatalf("administrator SetACL naming pat: %v", err)
		}
		_, err = pat.History(ctx, ab2bus.HistoryInput{Channel: "job1-work"})
		if err != nil {
			t.Fatalf("unscoped pat with a row: %v", err) // the gate never restricts an unscoped principal
		}

		// A task owner outside the scope is unregistered for the scoped caller.
		if _, err := coder.CreateChannel(ctx, ab2bus.CreateChannelInput{Name: "job1-jobs", Kind: ab2bus.KindTasks}); err != nil {
			t.Fatal(err)
		}
		task, err := coder.Create(ctx, ab2bus.TaskCreateInput{Channel: "job1-jobs", Subject: "t"})
		if err != nil {
			t.Fatal(err)
		}
		owner := "pat"
		_, err = coder.Update(ctx, ab2bus.TaskPatch{ID: task.ID, Owner: &owner})
		codeOf(t, err, ab2bus.CodeNotFound)
		if got, want := msgOf(err), `identity "pat" is not registered`; got != want {
			t.Errorf("owner outside the scope: %q, want %q", got, want)
		}
		// A human and an administrator are visible to every agent (Eric 2026-10-25).
		for _, who := range []string{"eric@corp.example", "root"} {
			tk, err := coder.Create(ctx, ab2bus.TaskCreateInput{Channel: "job1-jobs", Subject: "owned by " + who})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := coder.Update(ctx, ab2bus.TaskPatch{ID: tk.ID, Owner: &who}); err != nil {
				t.Errorf("owner %s for a scoped caller: %v", who, err)
			}
		}

		// The Identities leg of the set form: the scope, every human, every administrator, never pat.
		var seen []string
		h.tx(func(ctx context.Context, tx ab2store.Tx) error {
			p, err := access.Resolve(ctx, tx, "job1-coder")
			if err != nil {
				return err
			}
			ids, err := access.Checker{}.Identities(ctx, tx, p)
			for _, id := range ids {
				seen = append(seen, id.Name)
			}
			return err
		})
		if want := []string{"eric@corp.example", "job1-coder", "root"}; !reflect.DeepEqual(seen, want) {
			t.Errorf("scoped Identities = %v, want %v", seen, want)
		}

		// Creation: a scoped identity's name carries its scope prefix, so an
		// out-of-scope name is validation, never a collision (spec 4.2).
		_, err = coder.CreateChannel(ctx, ab2bus.CreateChannelInput{Name: "other"})
		codeOf(t, err, ab2bus.CodeValidation)
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
		if _, err := coder.CreateChannel(ctx, ab2bus.CreateChannelInput{Name: "job1-mine", Kind: ab2bus.KindMemory}); err != nil {
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
		foreign, own := memOf("elsewhere"), memOf("job1-mine")
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

// TestScopeGateBeforeKind: the scope gate runs before every kind check, so a
// scoped caller cannot learn the kind (or, through a by-id memory operation,
// the name) of a channel outside its scope. Each answer is byte-identical to
// the never-existing case; in scope the post-gate kind refusal still holds.
func TestScopeGateBeforeKind(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		h.rtScope("job1")
		coder := h.scopedIdentity("job1-coder", "job1")
		pat := h.localAs("pat")
		mk := func(b ab2bus.Bus, name string, kind ab2bus.Kind) {
			t.Helper()
			if _, err := b.CreateChannel(ctx, ab2bus.CreateChannelInput{Name: name, Kind: kind}); err != nil {
				t.Fatal(err)
			}
		}
		mk(pat, "oo-ord", ab2bus.KindOrdinary)
		mk(pat, "oo-tasks", ab2bus.KindTasks)
		mk(pat, "oo-mem", ab2bus.KindMemory)
		mk(coder, "job1-in-ord", ab2bus.KindOrdinary)
		mk(coder, "job1-in-tasks", ab2bus.KindTasks)
		mk(coder, "job1-in-mem", ab2bus.KindMemory)

		idOf := func(ch string) int64 { // the memory id of one message on ch
			t.Helper()
			var c ab2store.Channel
			h.tx(func(ctx context.Context, tx ab2store.Tx) error {
				var err error
				c, err = tx.ChannelByName(ctx, ch)
				return err
			})
			seq := h.append(c, "pat", "", "x")
			var id int64
			h.tx(func(ctx context.Context, tx ab2store.Tx) error {
				ms, err := tx.MessagesBySeqs(ctx, []int64{seq})
				if err == nil && len(ms) == 1 && ms[0].MemoryID != nil {
					id = *ms[0].MemoryID
				}
				return err
			})
			return id
		}
		ooTask, err := pat.Create(ctx, ab2bus.TaskCreateInput{Channel: "oo-tasks", Subject: "t"})
		if err != nil {
			t.Fatal(err)
		}
		ooMem := idOf("oo-mem")
		inMem := idOf("job1-in-mem")
		const unknown = 987654
		// norm masks digits and the channel name, which differ by construction.
		norm := func(err error, name string) string {
			return digits.ReplaceAllString(strings.ReplaceAll(msgOf(err), `"`+name+`"`, `"C"`), "N")
		}
		str := func(s string) *string { return &s }
		type call func(ch string, id int64) error
		byName := map[string]call{
			"Send": func(ch string, _ int64) error {
				_, err := coder.Send(ctx, ab2bus.SendInput{Channel: ch, Content: "x"})
				return err
			},
			"TaskCreate": func(ch string, _ int64) error {
				_, err := coder.Create(ctx, ab2bus.TaskCreateInput{Channel: ch, Subject: "x"})
				return err
			},
			"TaskList": func(ch string, _ int64) error {
				_, err := coder.List(ctx, ab2bus.TaskListInput{Channel: ch})
				return err
			},
		}
		for op, c := range byName {
			never := c("never-existed", 0)
			codeOf(t, never, ab2bus.CodeNotFound)
			for _, ch := range []string{"oo-ord", "oo-tasks", "oo-mem"} {
				got := c(ch, 0)
				codeOf(t, got, ab2bus.CodeNotFound)
				if norm(got, ch) != norm(never, "never-existed") {
					t.Errorf("%s on %s: %q differs from never-existing %q", op, ch, msgOf(got), msgOf(never))
				}
			}
		}
		byID := map[string]call{
			"Edit": func(_ string, id int64) error {
				_, err := coder.Edit(ctx, ab2bus.EditMemoryInput{ID: id, Content: str("y")})
				return err
			},
			"Delete":    func(_ string, id int64) error { return coder.Delete(ctx, ab2bus.DeleteMemoryInput{ID: id}) },
			"GetTask":   func(_ string, id int64) error { _, err := coder.GetTask(ctx, id); return err },
			"GetMemory": func(_ string, id int64) error { _, err := coder.GetMemory(ctx, id); return err },
			"Revisions": func(_ string, id int64) error { _, err := coder.Revisions(ctx, id); return err },
			"Update": func(_ string, id int64) error {
				_, err := coder.Update(ctx, ab2bus.TaskPatch{ID: id, Subject: str("y")})
				return err
			},
			"Claim": func(_ string, id int64) error { _, err := coder.Claim(ctx, ab2bus.TaskClaimInput{ID: id}); return err },
			"Release": func(_ string, id int64) error {
				_, err := coder.Release(ctx, ab2bus.TaskReleaseInput{ID: id})
				return err
			},
		}
		for op, c := range byID {
			never := c("", unknown)
			codeOf(t, never, ab2bus.CodeNotFound)
			for _, id := range []int64{ooTask.ID, ooMem} {
				got := c("", id)
				codeOf(t, got, ab2bus.CodeNotFound)
				if norm(got, "") != norm(never, "") {
					t.Errorf("%s by id %d: %q differs from unknown %q", op, id, msgOf(got), msgOf(never))
				}
				for _, leak := range []string{"oo-tasks", "oo-mem", "task list", "memory channel"} {
					if strings.Contains(msgOf(got), leak) {
						t.Errorf("%s by id %d leaks %q: %q", op, id, leak, msgOf(got))
					}
				}
			}
		}

		// In scope the kind refusals stay (after the gate).
		_, err = coder.Send(ctx, ab2bus.SendInput{Channel: "job1-in-tasks", Content: "x"})
		codeOf(t, err, ab2bus.CodeValidation)
		_, err = coder.Create(ctx, ab2bus.TaskCreateInput{Channel: "job1-in-ord", Subject: "x"})
		codeOf(t, err, ab2bus.CodeValidation)
		_, err = coder.List(ctx, ab2bus.TaskListInput{Channel: "job1-in-ord"})
		codeOf(t, err, ab2bus.CodeValidation)
		inTask, err := coder.Create(ctx, ab2bus.TaskCreateInput{Channel: "job1-in-tasks", Subject: "t"})
		if err != nil {
			t.Fatal(err)
		}
		_, err = coder.Edit(ctx, ab2bus.EditMemoryInput{ID: inTask.ID, Content: str("y")})
		codeOf(t, err, ab2bus.CodeValidation)
		codeOf(t, coder.Delete(ctx, ab2bus.DeleteMemoryInput{ID: inTask.ID}), ab2bus.CodeValidation)
		_, err = coder.GetTask(ctx, inMem)
		codeOf(t, err, ab2bus.CodeNotFound)
		if !strings.Contains(msgOf(err), "is not a task") {
			t.Errorf("in-scope GetTask of a memory: %q", msgOf(err))
		}
	})
}

// TestUnsubscribeNeedsNoRow: an identity with no ACL row on a channel in its
// scope (or any channel, unscoped) can still leave it (Decision 95). This
// is the Unsubscribe action, the scope gate alone.
func TestUnsubscribeNeedsNoRow(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		h.rtScope("job1")
		coder := h.scopedIdentity("job1-coder", "job1")
		pat := h.localAs("pat")
		root := h.localAdmin("root")
		for _, c := range []struct {
			who  ab2bus.Bus
			ch   string
			mine ab2bus.Bus
		}{{coder, "job1-scoped-ch", coder}, {pat, "plain-ch", pat}} {
			if _, err := c.mine.CreateChannel(ctx, ab2bus.CreateChannelInput{Name: c.ch}); err != nil {
				t.Fatal(err)
			}
			if err := c.who.Subscribe(ctx, ab2bus.SubscribeInput{Channel: c.ch}); err != nil {
				t.Fatal(err)
			}
			// Revoke every row: nothing names the subscriber.
			if err := root.SetACL(ctx, ab2bus.SetACLInput{Channel: c.ch}); err != nil {
				t.Fatal(err)
			}
			if err := c.who.Unsubscribe(ctx, ab2bus.UnsubscribeInput{Channel: c.ch}); err != nil {
				t.Errorf("Unsubscribe from %s with no ACL row: %v", c.ch, err)
			}
		}
	})
}

// TestReceiveFilterNeedsNoRow: naming a channel in a receive filter needs no
// ACL row (the FilterReceive action is the scope gate alone): the call returns
// no error, and the message pending on the channel stays undelivered because
// the caller no longer holds read.
func TestReceiveFilterNeedsNoRow(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		h.rtScope("job1")
		coder := h.scopedIdentity("job1-coder", "job1")
		pat := h.localAs("pat")
		root := h.localAdmin("root")
		for _, c := range []struct {
			who ab2bus.Bus
			ch  string
		}{{coder, "job1-filter-ch"}, {pat, "plain-filter-ch"}} {
			if _, err := c.who.CreateChannel(ctx, ab2bus.CreateChannelInput{Name: c.ch}); err != nil {
				t.Fatal(err)
			}
			s, err := c.who.Open(ctx, ab2bus.OpenSessionInput{})
			if err != nil {
				t.Fatal(err)
			}
			if err := c.who.Subscribe(ctx, ab2bus.SubscribeInput{Channel: c.ch}); err != nil {
				t.Fatal(err)
			}
			if _, err := root.Send(ctx, ab2bus.SendInput{Channel: c.ch, Content: "pending"}); err != nil {
				t.Fatal(err)
			}
			// Revoke every row: nothing names the receiver.
			if err := root.SetACL(ctx, ab2bus.SetACLInput{Channel: c.ch}); err != nil {
				t.Fatal(err)
			}
			got, err := c.who.Receive(ctx, ab2bus.ReceiveInput{Session: s.ID, Channels: []string{c.ch}})
			if err != nil || len(got.Messages) != 0 {
				t.Errorf("Receive filtered to %s with no ACL row = %+v, %v; want no error, no messages", c.ch, got, err)
			}
		}
	})
}

// TestScopedGranterNamesHumansAndAdmins: humans and administrators are visible
// to a scoped granter (Eric 2026-10-25), so it may name them; an unscoped agent
// stays invisible and keeps the Decision 96 text.
func TestScopedGranterNamesHumansAndAdmins(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		h.rtScope("job1")
		coder := h.scopedIdentity("job1-coder", "job1")
		h.human("eric@corp.example")
		root := h.localAdmin("root")
		h.localAs("pat")
		if _, err := coder.CreateChannel(ctx, ab2bus.CreateChannelInput{Name: "job1-w"}); err != nil {
			t.Fatal(err)
		}
		rd := []ab2bus.Permission{ab2bus.PermRead}
		// The default everyone and admins entries stay: an agent cannot remove them (spec 4.6).
		keep := []ab2bus.ACLEntry{
			{Identity: ab2store.Everyone, Permissions: []ab2bus.Permission{ab2bus.PermRead, ab2bus.PermWrite}},
			{Identity: ab2store.Admins, Permissions: []ab2bus.Permission{ab2bus.PermAdmin}},
			{Identity: "eric@corp.example", Permissions: rd},
			{Identity: "root", Permissions: []ab2bus.Permission{ab2bus.PermRead, ab2bus.PermWrite}},
		}
		granter := ab2bus.ACLEntry{Identity: "job1-coder", Permissions: []ab2bus.Permission{ab2bus.PermRead, ab2bus.PermWrite, ab2bus.PermAdmin}} // keep the granter's admin
		acl := append(slices.Clone(keep), granter)
		if err := coder.SetACL(ctx, ab2bus.SetACLInput{Channel: "job1-w", Entries: acl}); err != nil {
			t.Fatalf("scoped granter naming a human and an administrator: %v", err)
		}
		if got, err := root.ACL(ctx, "job1-w"); err != nil || !reflect.DeepEqual(got, acl) {
			t.Errorf("ACL = %+v, %v; want %+v", got, err, acl)
		}
		bad := append(slices.Clone(keep), ab2bus.ACLEntry{Identity: "job1-coder", Permissions: []ab2bus.Permission{ab2bus.PermAdmin}}, ab2bus.ACLEntry{Identity: "pat", Permissions: rd})
		err := coder.SetACL(ctx, ab2bus.SetACLInput{Channel: "job1-w", Entries: bad})
		codeOf(t, err, ab2bus.CodeValidation)
		if got, want := msgOf(err), `ACL identity "pat" is not a registered identity`; got != want {
			t.Errorf("scoped granter naming an unscoped agent: %q, want %q", got, want)
		}
		// A malformed name keeps the Store's own format refusal, for scoped and unscoped alike.
		fmtBad := append(slices.Clone(keep), ab2bus.ACLEntry{Identity: "job1-coder", Permissions: []ab2bus.Permission{ab2bus.PermAdmin}}, ab2bus.ACLEntry{Identity: "bad/name", Permissions: rd})
		errScoped := coder.SetACL(ctx, ab2bus.SetACLInput{Channel: "job1-w", Entries: fmtBad})
		errAdmin := root.SetACL(ctx, ab2bus.SetACLInput{Channel: "job1-w", Entries: fmtBad})
		codeOf(t, errScoped, ab2bus.CodeValidation)
		if msgOf(errScoped) != msgOf(errAdmin) || strings.Contains(msgOf(errScoped), "is not a registered identity") {
			t.Errorf("malformed name: scoped %q, admin %q; want the Store's format text for both", msgOf(errScoped), msgOf(errAdmin))
		}
	})
}
