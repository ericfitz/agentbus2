package bus

import (
	"context"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/channels"
	"github.com/ericfitz/agentbus2/internal/core/identities"
	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
)

// defaultACLFor is the Decision 88 default as the Bus shows it.
func defaultACLFor(creator string) []ab2bus.ACLEntry {
	rw := []ab2bus.Permission{ab2bus.PermRead, ab2bus.PermWrite}
	acl := []ab2bus.ACLEntry{{Identity: "everyone", Permissions: rw}}
	if creator != "" {
		acl = append(acl, ab2bus.ACLEntry{Identity: creator, Permissions: []ab2bus.Permission{ab2bus.PermRead, ab2bus.PermWrite, ab2bus.PermAdmin}})
	}
	return append(acl, ab2bus.ACLEntry{Identity: "admins", Permissions: []ab2bus.Permission{ab2bus.PermAdmin}})
}

func channelInfoFor(t *testing.T, b ab2bus.Bus, name string) ab2bus.ChannelInfo {
	t.Helper()
	root := b.(ab2bus.Admin)
	st, err := root.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	for _, c := range st.Channels {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("channel %q not in Status", name)
	return ab2bus.ChannelInfo{}
}

func TestChannelCreateDefaults(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		pat := h.as("pat")
		info, err := pat.CreateChannel(ctx, ab2bus.CreateChannelInput{Name: "work"})
		if err != nil {
			t.Fatal(err)
		}
		if info.Name != "work" || info.Kind != ab2bus.KindOrdinary || info.Creator != "pat" || info.CreatedAt != fixedNowMs {
			t.Fatalf("info = %+v", info)
		}
		acl, err := pat.ACL(ctx, "work")
		if err != nil {
			t.Fatal(err)
		}
		if want := defaultACLFor("pat"); !reflect.DeepEqual(acl, want) {
			t.Fatalf("ACL = %+v, want %+v", acl, want)
		}
	})
}

func TestChannelCreateWithKindAndACL(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		pat := h.as("pat")
		h.ensureIdentity("kim")
		own := []ab2bus.ACLEntry{
			{Identity: "kim", Permissions: []ab2bus.Permission{ab2bus.PermRead}},
			{Identity: "pat", Permissions: []ab2bus.Permission{ab2bus.PermRead, ab2bus.PermWrite, ab2bus.PermAdmin}},
		}
		info, err := pat.CreateChannel(ctx, ab2bus.CreateChannelInput{Name: "jobs", Kind: ab2bus.KindTasks, ACL: own})
		if err != nil || info.Kind != ab2bus.KindTasks {
			t.Fatalf("Create tasks = %+v, %v", info, err)
		}
		if acl, err := pat.ACL(ctx, "jobs"); err != nil || !reflect.DeepEqual(acl, own) {
			t.Fatalf("ACL = %+v, %v; want the ACL as given", acl, err)
		}
		// [] stores no entries; the creator holds no right on it (the implicit
		// creator entry is retired, accesscheck spec 3.13) and a server
		// administrator reads it.
		if _, err := pat.CreateChannel(ctx, ab2bus.CreateChannelInput{Name: "bare", ACL: []ab2bus.ACLEntry{}}); err != nil {
			t.Fatal(err)
		}
		_, err = pat.ACL(ctx, "bare")
		codeOf(t, err, ab2bus.CodeForbidden)
		if acl, err := h.admin("root").ACL(ctx, "bare"); err != nil || len(acl) != 0 {
			t.Fatalf("ACL of bare = %+v, %v; want none", acl, err)
		}
	})
}

func TestChannelCreateRejects(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		pat := h.as("pat")
		rw := []ab2bus.Permission{ab2bus.PermRead}
		cases := []struct {
			name string
			in   ab2bus.CreateChannelInput
			want ab2bus.Code
		}{
			{"slash", ab2bus.CreateChannelInput{Name: "a/b"}, ab2bus.CodeValidation},
			{"empty name", ab2bus.CreateChannelInput{Name: ""}, ab2bus.CodeValidation},
			{"dm kind", ab2bus.CreateChannelInput{Name: "x", Kind: ab2bus.Kind("dm")}, ab2bus.CodeValidation},
			{"owner permission", ab2bus.CreateChannelInput{Name: "x", ACL: []ab2bus.ACLEntry{{Identity: "pat", Permissions: []ab2bus.Permission{"owner"}}}}, ab2bus.CodeValidation},
			{"empty permission list", ab2bus.CreateChannelInput{Name: "x", ACL: []ab2bus.ACLEntry{{Identity: "pat"}}}, ab2bus.CodeValidation},
			{"unregistered entry", ab2bus.CreateChannelInput{Name: "x", ACL: []ab2bus.ACLEntry{{Identity: "ghost", Permissions: rw}}}, ab2bus.CodeValidation},
		}
		for _, c := range cases {
			if _, err := pat.CreateChannel(ctx, c.in); ab2bus.CodeOf(err) != c.want {
				t.Errorf("%s: code = %q (%v), want %q", c.name, ab2bus.CodeOf(err), err, c.want)
			}
		}
		if _, err := pat.CreateChannel(ctx, ab2bus.CreateChannelInput{Name: "dup"}); err != nil {
			t.Fatal(err)
		}
		_, err := pat.CreateChannel(ctx, ab2bus.CreateChannelInput{Name: "dup"})
		codeOf(t, err, ab2bus.CodeConflict)
		// Nothing was created by the rejected calls.
		chs, err := pat.ListChannels(ctx)
		if err != nil || len(chs) != 1 {
			t.Fatalf("ListChannels = %+v, %v; want only dup", chs, err)
		}
		// An unregistered caller is refused.
		_, err = newBus(h, "nobody").CreateChannel(ctx, ab2bus.CreateChannelInput{Name: "y"})
		codeOf(t, err, ab2bus.CodeNotRegistered)
	})
}

func TestChannelCreationSwitch(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		ctx := context.Background()
		cfg := core.Defaults()
		cfg.ChannelCreation = core.ChannelCreationAdmins
		h := newHarness(t, e, harnessOptions{cfg: &cfg})
		_, err := h.as("kim").CreateChannel(ctx, ab2bus.CreateChannelInput{Name: "k"})
		codeOf(t, err, ab2bus.CodeForbidden)
		if _, err := h.admin("root").CreateChannel(ctx, ab2bus.CreateChannelInput{Name: "r"}); err != nil {
			t.Fatalf("admin create: %v", err)
		}

		open := newHarness(t, e, harnessOptions{})
		if _, err := open.as("kim").CreateChannel(ctx, ab2bus.CreateChannelInput{Name: "k"}); err != nil {
			t.Fatalf("open create: %v", err)
		}
	})
}

func TestChannelListFiltersByEntry(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		h.ensureIdentity("pat")
		h.ensureIdentity("kim")
		hal := h.human("hal@corp.example")
		perms := func(p ...ab2store.Permission) []ab2store.Permission { return p }
		h.channel("secret", ab2store.KindMemory, "", []ab2store.ACLEntry{{Identity: "pat", Permissions: perms(ab2store.PermRead)}})
		h.channel("alpha", ab2store.KindOrdinary, "pat", nil)
		h.channel("adminonly", ab2store.KindOrdinary, "kim", []ab2store.ACLEntry{{Identity: "kim", Permissions: perms(ab2store.PermAdmin)}})
		h.channel("writeonly", ab2store.KindOrdinary, "", []ab2store.ACLEntry{{Identity: "kim", Permissions: perms(ab2store.PermWrite)}})
		h.channel("mine", ab2store.KindOrdinary, "kim", []ab2store.ACLEntry{})
		h.channel("humansonly", ab2store.KindOrdinary, "", []ab2store.ACLEntry{{Identity: ab2store.Humans, Permissions: perms(ab2store.PermRead)}})
		o, m := ab2bus.KindOrdinary, ab2bus.KindMemory
		for _, c := range []struct {
			who  string
			b    ab2bus.Bus
			want map[string]ab2bus.Kind
		}{
			{"kim", h.as("kim"), map[string]ab2bus.Kind{"adminonly": o, "alpha": o, "writeonly": o}}, // mine: a creator without a row lists nothing
			{"pat", h.as("pat"), map[string]ab2bus.Kind{"alpha": o, "secret": m}},
			{"hal", hal, map[string]ab2bus.Kind{"alpha": o, "humansonly": o}},
			{"root", h.admin("root"), map[string]ab2bus.Kind{"adminonly": o, "alpha": o, "humansonly": o, "mine": o, "secret": m, "writeonly": o}},
		} {
			if got := channelNames(t, c.b); !reflect.DeepEqual(got, c.want) {
				t.Errorf("%s lists %v, want %v", c.who, got, c.want)
			}
		}
		cs, err := h.as("kim").ListChannels(ctx)
		must(t, err)
		var names []string
		for _, c := range cs {
			if c.Capabilities == nil || len(c.Capabilities) != 0 {
				t.Errorf("%s capabilities = %#v, want empty non-nil", c.Name, c.Capabilities)
			}
			names = append(names, c.Name)
		}
		if !sort.StringsAreSorted(names) {
			t.Errorf("ListChannels names %v are not in byte order", names)
		}
	})
}

func TestChannelACLNeedsAdmin(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		h.ensureIdentity("kim")
		pat := h.as("pat")
		_, err := pat.CreateChannel(ctx, ab2bus.CreateChannelInput{Name: "c", ACL: []ab2bus.ACLEntry{
			{Identity: "kim", Permissions: []ab2bus.Permission{ab2bus.PermRead}},
			{Identity: "pat", Permissions: []ab2bus.Permission{ab2bus.PermRead, ab2bus.PermAdmin}},
		}})
		if err != nil {
			t.Fatal(err)
		}
		kim := h.as("kim")
		_, err = kim.ACL(ctx, "c")
		codeOf(t, err, ab2bus.CodeForbidden)
		codeOf(t, kim.SetACL(ctx, ab2bus.SetACLInput{Channel: "c", Entries: []ab2bus.ACLEntry{{Identity: "kim", Permissions: []ab2bus.Permission{ab2bus.PermAdmin}}}}), ab2bus.CodeForbidden)
		if _, err := pat.ACL(ctx, "c"); err != nil {
			t.Fatalf("creator ACL: %v", err)
		}
		if _, err := h.admin("root").ACL(ctx, "c"); err != nil {
			t.Fatalf("administrator without rows: %v", err)
		}
		_, err = pat.ACL(ctx, "missing")
		codeOf(t, err, ab2bus.CodeNotFound)
		// Removing the creator's rows leaves the creator without a right: the
		// implicit creator entry is retired (accesscheck spec 3.13), so only a
		// server administrator edits afterwards.
		if err := pat.SetACL(ctx, ab2bus.SetACLInput{Channel: "c", Entries: []ab2bus.ACLEntry{{Identity: "kim", Permissions: []ab2bus.Permission{ab2bus.PermRead}}}}); err != nil {
			t.Fatal(err)
		}
		if _, err := pat.ACL(ctx, "c"); ab2bus.CodeOf(err) != ab2bus.CodeForbidden {
			t.Fatalf("creator after removing its rows: %v", err)
		}
		root := h.admin("root")
		if acl, err := root.ACL(ctx, "c"); err != nil || len(acl) != 1 || acl[0].Identity != "kim" {
			t.Fatalf("ACL after SetACL = %+v, %v", acl, err)
		}
		if err := root.SetACL(ctx, ab2bus.SetACLInput{Channel: "c", Entries: []ab2bus.ACLEntry{{Identity: "kim", Permissions: []ab2bus.Permission{"owner"}}}}); ab2bus.CodeOf(err) != ab2bus.CodeValidation {
			t.Fatalf("unknown permission: %v", err)
		}
		if err := root.SetACL(ctx, ab2bus.SetACLInput{Channel: "c", Entries: []ab2bus.ACLEntry{{Identity: "ghost", Permissions: []ab2bus.Permission{ab2bus.PermRead}}}}); ab2bus.CodeOf(err) != ab2bus.CodeValidation {
			t.Fatalf("unregistered entry: %v", err)
		}
		if err := root.SetACL(ctx, ab2bus.SetACLInput{Channel: "c", Entries: []ab2bus.ACLEntry{{Identity: "kim"}}}); ab2bus.CodeOf(err) != ab2bus.CodeValidation {
			t.Fatalf("empty permission list: %v", err)
		}
	})
}

func TestChannelSetACLPublishesHint(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		pat := h.as("pat")
		info, err := pat.CreateChannel(ctx, ab2bus.CreateChannelInput{Name: "c"})
		if err != nil {
			t.Fatal(err)
		}
		_ = info
		hints, cancel := h.notifier.Subscribe()
		defer cancel()
		// A refused SetACL publishes nothing.
		codeOf(t, h.as("kim").SetACL(ctx, ab2bus.SetACLInput{Channel: "c"}), ab2bus.CodeForbidden)
		select {
		case hint := <-hints:
			t.Fatalf("hint %+v after a refused SetACL", hint)
		case <-time.After(50 * time.Millisecond):
		}
		if err := pat.SetACL(ctx, ab2bus.SetACLInput{Channel: "c", Entries: defaultACLFor("pat")}); err != nil {
			t.Fatal(err)
		}
		select {
		case hint := <-hints:
			if hint.ChannelID == 0 || hint.Seq != 0 {
				t.Fatalf("hint = %+v, want the channel id and a zero seq", hint)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("no hint after SetACL")
		}
	})
}

func TestChannelDeleteAndRenameNeedAdmin(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		pat, kim := h.as("pat"), h.as("kim")
		if _, err := pat.CreateChannel(ctx, ab2bus.CreateChannelInput{Name: "old"}); err != nil {
			t.Fatal(err)
		}
		codeOf(t, kim.RenameChannel(ctx, ab2bus.RenameChannelInput{Name: "old", NewName: "new"}), ab2bus.CodeForbidden)
		codeOf(t, kim.DeleteChannel(ctx, ab2bus.DeleteChannelInput{Name: "old"}), ab2bus.CodeForbidden)
		codeOf(t, pat.DeleteChannel(ctx, ab2bus.DeleteChannelInput{Name: "missing"}), ab2bus.CodeNotFound)
		if err := kim.Subscribe(ctx, ab2bus.SubscribeInput{Channel: "old"}); err != nil {
			t.Fatal(err)
		}
		if err := pat.RenameChannel(ctx, ab2bus.RenameChannelInput{Name: "old", NewName: "new"}); err != nil {
			t.Fatal(err)
		}
		// The id stayed: kim's subscription is on the renamed channel.
		h.tx(func(ctx context.Context, tx ab2store.Tx) error {
			ch, err := tx.ChannelByName(ctx, "new")
			if err != nil {
				t.Fatalf("ChannelByName(new): %v", err)
			}
			subs, err := tx.Subscriptions(ctx, "kim")
			if err != nil || len(subs) != 1 || subs[0].ChannelID != ch.ID {
				t.Fatalf("subscriptions = %+v, %v; want one on channel %d", subs, err, ch.ID)
			}
			return nil
		})
		if _, err := pat.CreateChannel(ctx, ab2bus.CreateChannelInput{Name: "other"}); err != nil {
			t.Fatal(err)
		}
		codeOf(t, pat.RenameChannel(ctx, ab2bus.RenameChannelInput{Name: "new", NewName: "other"}), ab2bus.CodeConflict)
		codeOf(t, pat.RenameChannel(ctx, ab2bus.RenameChannelInput{Name: "new", NewName: "a/b"}), ab2bus.CodeValidation)
		if err := pat.DeleteChannel(ctx, ab2bus.DeleteChannelInput{Name: "new"}); err != nil {
			t.Fatal(err)
		}
		h.tx(func(ctx context.Context, tx ab2store.Tx) error {
			if subs, err := tx.Subscriptions(ctx, "kim"); err != nil || len(subs) != 0 {
				t.Fatalf("subscriptions after delete = %+v, %v; want none (cascade)", subs, err)
			}
			return nil
		})
	})
}

func TestChannelIdempotencyKey(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		pat := h.as("pat")
		in := ab2bus.CreateChannelInput{Name: "once", IdempotencyKey: "k1"}
		first, err := pat.CreateChannel(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		again, err := pat.CreateChannel(ctx, in)
		if err != nil || !reflect.DeepEqual(first, again) {
			t.Fatalf("replay = %+v, %v; want %+v", again, err, first)
		}
		_, err = pat.CreateChannel(ctx, ab2bus.CreateChannelInput{Name: "twice", IdempotencyKey: "k1"})
		codeOf(t, err, ab2bus.CodeConflict)
		// The key survives the deletion of what it created.
		if err := pat.DeleteChannel(ctx, ab2bus.DeleteChannelInput{Name: "once"}); err != nil {
			t.Fatal(err)
		}
		if again, err := pat.CreateChannel(ctx, in); err != nil || !reflect.DeepEqual(first, again) {
			t.Fatalf("replay after delete = %+v, %v", again, err)
		}
		if chs, _ := pat.ListChannels(ctx); len(chs) != 0 {
			t.Fatalf("channels = %+v; a replay must not recreate", chs)
		}
	})
}

func TestChannelGeneralIsProtected(t *testing.T) {
	// Decision (Eric, 2026-10-21): Milestone 3 item 6
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		svc := channels.Service{Core: h.core}
		for i := 0; i < 2; i++ {
			if err := svc.EnsureGeneral(ctx); err != nil {
				t.Fatalf("EnsureGeneral run %d: %v", i+1, err)
			}
		}
		pat := h.as("pat")
		root := h.admin("root")
		chs, err := pat.ListChannels(ctx)
		if err != nil || len(chs) != 1 || chs[0].Name != "general" {
			t.Fatalf("ListChannels = %+v, %v; want exactly general", chs, err)
		}
		if got := channelInfoFor(t, root, "general"); got.Creator != "" {
			t.Fatalf("Creator = %q, want none", got.Creator)
		}
		acl, err := root.ACL(ctx, "general")
		if want := defaultACLFor(""); err != nil || !reflect.DeepEqual(acl, want) {
			t.Fatalf("ACL = %+v, %v; want %+v", acl, err, want)
		}
		codeOf(t, root.DeleteChannel(ctx, ab2bus.DeleteChannelInput{Name: "general"}), ab2bus.CodeValidation)
		codeOf(t, root.RenameChannel(ctx, ab2bus.RenameChannelInput{Name: "general", NewName: "main"}), ab2bus.CodeValidation)
		codeOf(t, pat.DeleteChannel(ctx, ab2bus.DeleteChannelInput{Name: "general"}), ab2bus.CodeForbidden)
		h.tx(func(ctx context.Context, tx ab2store.Tx) error {
			for _, id := range []string{"pat", "root"} {
				if subs, err := tx.Subscriptions(ctx, id); err != nil || len(subs) != 0 {
					t.Errorf("%s subscriptions = %+v, %v; EnsureGeneral subscribes nobody", id, subs, err)
				}
			}
			return nil
		})

		// A configured default ACL applies, without a creator entry.
		cfg := core.Defaults()
		cfg.DefaultACL = func(creator string) []ab2store.ACLEntry {
			return []ab2store.ACLEntry{{Identity: "everyone", Permissions: []ab2store.Permission{ab2store.PermRead}}}
		}
		h2 := newHarness(t, e, harnessOptions{cfg: &cfg})
		if err := (channels.Service{Core: h2.core}).EnsureGeneral(ctx); err != nil {
			t.Fatal(err)
		}
		acl, err = h2.admin("root").ACL(ctx, "general")
		want := []ab2bus.ACLEntry{{Identity: "everyone", Permissions: []ab2bus.Permission{ab2bus.PermRead}}}
		if err != nil || !reflect.DeepEqual(acl, want) {
			t.Fatalf("configured ACL = %+v, %v; want %+v", acl, err, want)
		}
	})
}

func TestChannelEnsureGeneralConcurrent(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		sib := h.sibling(harnessOptions{})
		var wg sync.WaitGroup
		errs := make([]error, 4)
		for i := range errs {
			svc := channels.Service{Core: h.core}
			if i%2 == 1 {
				svc = channels.Service{Core: sib.core}
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs[i] = svc.EnsureGeneral(context.Background())
			}()
		}
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Errorf("run %d: %v", i, err)
			}
		}
		h.tx(func(ctx context.Context, tx ab2store.Tx) error {
			chs, err := tx.ListChannels(ctx)
			if err != nil || len(chs) != 1 {
				t.Fatalf("channels = %+v, %v; want one", chs, err)
			}
			return nil
		})
	})
}

// TestHumansGroup: a humans row applies to every human and to no agent, is
// accepted by the Store's registry check, and cannot be registered as a name
// (Eric 2026-10-25).
func TestHumansGroup(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		root := h.localAdmin("root")
		pat := h.localAs("pat")
		kim := h.localAs("kim")
		hal := h.human("hal@corp.example")
		if _, err := root.CreateChannel(ctx, ab2bus.CreateChannelInput{Name: "c"}); err != nil {
			t.Fatal(err)
		}
		acl := []ab2bus.ACLEntry{
			{Identity: "humans", Permissions: []ab2bus.Permission{ab2bus.PermRead, ab2bus.PermWrite}},
			{Identity: "admins", Permissions: []ab2bus.Permission{ab2bus.PermAdmin}},
		}
		if err := root.SetACL(ctx, ab2bus.SetACLInput{Channel: "c", Entries: acl}); err != nil {
			t.Fatalf("SetACL naming humans: %v", err)
		}
		if _, err := hal.Send(ctx, ab2bus.SendInput{Channel: "c", Content: "hi"}); err != nil {
			t.Fatalf("human send through the humans row: %v", err)
		}
		if r, err := hal.History(ctx, ab2bus.HistoryInput{Channel: "c"}); err != nil || len(r) != 1 {
			t.Fatalf("human history = %+v, %v", r, err)
		}
		_, err := kim.History(ctx, ab2bus.HistoryInput{Channel: "c"})
		codeOf(t, err, ab2bus.CodeForbidden)
		_, err = kim.Send(ctx, ab2bus.SendInput{Channel: "c", Content: "x"})
		codeOf(t, err, ab2bus.CodeForbidden)

		// CreateChannel accepts the group in its ACL.
		if _, err := pat.CreateChannel(ctx, ab2bus.CreateChannelInput{Name: "d", ACL: []ab2bus.ACLEntry{
			{Identity: "humans", Permissions: []ab2bus.Permission{ab2bus.PermRead}}}}); err != nil {
			t.Fatalf("CreateChannel with a humans row: %v", err)
		}

		// The name is reserved as everyone is.
		svc := identities.Service{Core: h.core}
		_, err = svc.Register(ctx, identities.ModeOpen, identities.RegisterInput{Identity: "humans", Key: idKey(t)})
		codeOf(t, err, ab2bus.CodeValidation)
		_, err = svc.CreateIdentity(ctx, "humans")
		codeOf(t, err, ab2bus.CodeValidation)
	})
}

// TestAgentCannotRemoveHumanOrAdminAccess: an agent's set_acl never takes a
// permission from everyone, admins, humans, a human or an administrator
// (Eric 2026-10-25, spec 4.6).
func TestAgentCannotRemoveHumanOrAdminAccess(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		pat := h.localAs("pat")
		root := h.localAdmin("root")
		h.localAs("kim")
		hal := h.human("hal@corp.example")
		perms := func(p ...ab2bus.Permission) []ab2bus.Permission { return p }
		R, W, A := ab2bus.PermRead, ab2bus.PermWrite, ab2bus.PermAdmin
		set := func(b ab2bus.Bus, es ...ab2bus.ACLEntry) error {
			return b.SetACL(ctx, ab2bus.SetACLInput{Channel: "c", Entries: es})
		}
		everyone := ab2bus.ACLEntry{Identity: "everyone", Permissions: perms(R, W)}
		admins := ab2bus.ACLEntry{Identity: "admins", Permissions: perms(A)}
		self := ab2bus.ACLEntry{Identity: "pat", Permissions: perms(R, W, A)}

		if _, err := pat.CreateChannel(ctx, ab2bus.CreateChannelInput{Name: "c"}); err != nil {
			t.Fatal(err)
		}
		// The default ACL is everyone [read, write] and admins [admin] and the
		// creator's own row; a replacement without the group rows is refused.
		if err := set(root, everyone, admins, self); err != nil {
			t.Fatalf("root sets the base ACL: %v", err)
		}
		err := set(pat, self)
		codeOf(t, err, ab2bus.CodeForbidden)
		if got, want := msgOf(err), `an agent cannot remove read, write from ACL entry "everyone": humans and administrators keep their access`; got != want {
			t.Fatalf("message = %q, want %q", got, want)
		}
		if err := set(pat, everyone, admins, self, ab2bus.ACLEntry{Identity: "kim", Permissions: perms(R)}); err != nil {
			t.Fatalf("keeping the protected rows and adding kim: %v", err)
		}
		if err := set(root, everyone, admins, self, ab2bus.ACLEntry{Identity: "hal@corp.example", Permissions: perms(R)}); err != nil {
			t.Fatal(err)
		}
		codeOf(t, set(pat, everyone, admins, self), ab2bus.CodeForbidden)
		codeOf(t, set(hal), ab2bus.CodeForbidden)
		if err := set(root, everyone, admins, self, ab2bus.ACLEntry{Identity: "hal@corp.example", Permissions: perms(R, A)}); err != nil {
			t.Fatal(err)
		}
		if err := set(hal); err != nil {
			t.Fatalf("a human with admin clears the ACL: %v", err)
		}
		// No stored ACL to lose from at creation (spec 4.6).
		if _, err := pat.CreateChannel(ctx, ab2bus.CreateChannelInput{Name: "d", ACL: []ab2bus.ACLEntry{self}}); err != nil {
			t.Fatalf("CreateChannel with an explicit private ACL: %v", err)
		}
	})
}

// TestChannelListCapabilities: a channel lists "bridged" exactly while its
// bridge is live; a creating bridge, an archived one and no bridge all list
// the empty (non-nil) set, on every engine and transport.
func TestChannelListCapabilities(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		h.rtScope("job1")
		root := h.admin("root")
		pat := h.as("pat")
		live := h.rtChannel("job1", "job1-live", ab2store.KindOrdinary)
		h.rtBridge(live, "job1", "C1")
		creating := h.rtChannel("job1", "job1-creating", ab2store.KindOrdinary)
		h.tx(func(ctx context.Context, tx ab2store.Tx) error {
			_, err := tx.CreateBridge(ctx, ab2store.Bridge{ChannelID: creating.ID, ChannelName: creating.Name, Scope: "job1", SlackName: "ab-job1-creating", CreatedAt: 5})
			return err
		})
		archived := h.rtChannel("job1", "job1-archived", ab2store.KindOrdinary)
		h.rtArchiveBridge(h.rtBridge(archived, "job1", "C2").ID)
		h.rtChannel("job1", "job1-none", ab2store.KindOrdinary)

		bridged := []ab2bus.ChannelCapability{ab2bus.ChannelBridged}
		none := []ab2bus.ChannelCapability{}
		want := map[string][]ab2bus.ChannelCapability{"job1-live": bridged, "job1-creating": none, "job1-archived": none, "job1-none": none}
		check := func(who string, cs []ab2bus.Channel) {
			t.Helper()
			seen := 0
			for _, c := range cs {
				w, ok := want[c.Name]
				if !ok {
					continue
				}
				seen++
				if !reflect.DeepEqual(c.Capabilities, w) {
					t.Errorf("%s: %s capabilities = %#v, want %#v", who, c.Name, c.Capabilities, w)
				}
				if c.Has(ab2bus.ChannelBridged) != (c.Name == "job1-live") {
					t.Errorf("%s: %s Has(bridged) = %v", who, c.Name, c.Has(ab2bus.ChannelBridged))
				}
			}
			if seen != len(want) {
				t.Errorf("%s saw %d of %d channels", who, seen, len(want))
			}
		}
		cs, err := pat.ListChannels(ctx)
		must(t, err)
		check("ListChannels", cs)
		st, err := root.Status(ctx)
		must(t, err)
		var infos []ab2bus.Channel
		for _, c := range st.Channels {
			infos = append(infos, c.Channel)
		}
		check("Status", infos)
		fresh, err := pat.CreateChannel(ctx, ab2bus.CreateChannelInput{Name: "fresh"})
		must(t, err)
		if !reflect.DeepEqual(fresh.Capabilities, none) {
			t.Errorf("CreateChannel capabilities = %#v, want empty non-nil", fresh.Capabilities)
		}
	})
}
