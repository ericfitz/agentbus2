package bus

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
)

// matrixSubcases pins the number of sub-cases of TestACLMatrix: adding or
// dropping one is a deliberate edit (and a change to scripts/m3-exit-counts.sh).
const matrixSubcases = 26

// subcase is one operation of a section 4.6 row. refuse runs against the
// channel whose stored ACL withholds the row's permission from kim, allow
// against the channel that grants it. refuse is not always an error check:
// six sub-cases refuse by delivering nothing.
type subcase struct {
	name          string
	pre           func(t *testing.T, h *harness, kim ab2bus.Bus, ch string) // runs while kim still holds every permission
	refuse, allow func(t *testing.T, h *harness, b ab2bus.Bus, ch string)
}

type aclRow struct {
	row  string
	perm ab2bus.Permission // "" for none
	kind ab2store.Kind
	subs []subcase
}

// aclFixture is what a channel was seeded with.
type aclFixture struct {
	chID int64
	kim  string // the identity of this leg: fresh per leg, so no subscription, tag set or session leaks between legs
	seq  int64  // the seeded message (memory id on a memory channel)
	task int64  // the seeded task on a tasks channel
}

type fixKey struct {
	h    *harness
	name string
}

var aclFixtures sync.Map

func fixOf(h *harness, ch string) aclFixture {
	v, ok := aclFixtures.Load(fixKey{h, ch})
	if !ok {
		h.t.Fatalf("no fixture for channel %s", ch)
	}
	return v.(aclFixture)
}

func aclPerms(ps ...ab2bus.Permission) []ab2bus.Permission { return ps }

// othersThan is the two permissions of read, write and admin that p is not.
func othersThan(p ab2bus.Permission) []ab2bus.Permission {
	var out []ab2bus.Permission
	for _, q := range []ab2bus.Permission{ab2bus.PermRead, ab2bus.PermWrite, ab2bus.PermAdmin} {
		if q != p {
			out = append(out, q)
		}
	}
	return out
}

var matrixSeq int

// matrixChannel builds channel m for one sub-case leg: created by pat, seeded
// and prepared while kim holds every permission, then its stored ACL becomes
// exactly [{kim, perms}] (for a row with no permission, no kim entry: pat's
// entry only).
func matrixChannel(t *testing.T, h *harness, r aclRow, s subcase, perms []ab2bus.Permission) (kim ab2bus.Bus, name string) {
	t.Helper()
	matrixSeq++
	name = fmt.Sprintf("m%d", matrixSeq)
	all := []ab2store.Permission{ab2store.PermRead, ab2store.PermWrite, ab2store.PermAdmin}
	kimName := fmt.Sprintf("kim%d", matrixSeq)
	h.ensureIdentity(kimName)
	ch := h.channel(name, r.kind, "pat", []ab2store.ACLEntry{{Identity: "pat", Permissions: all}, {Identity: kimName, Permissions: all}})
	fx := aclFixture{chID: ch.ID, kim: kimName}
	pat := h.as("pat")
	if r.kind == ab2store.KindTasks {
		task, err := pat.Create(context.Background(), ab2bus.TaskCreateInput{Channel: name, Subject: "seed"})
		if err != nil {
			t.Fatalf("seed task: %v", err)
		}
		fx.task = task.ID
	} else {
		fx.seq = h.append(ch, "pat", "", "needle", "env:prod")
	}
	aclFixtures.Store(fixKey{h, name}, fx)
	kim = h.as(kimName)
	if s.pre != nil {
		s.pre(t, h, kim, name)
	}
	entries := []ab2bus.ACLEntry{{Identity: "pat", Permissions: aclPerms(ab2bus.PermRead, ab2bus.PermWrite, ab2bus.PermAdmin)}}
	if len(perms) > 0 {
		entries = []ab2bus.ACLEntry{{Identity: kimName, Permissions: perms}}
	}
	if err := pat.SetACL(context.Background(), ab2bus.SetACLInput{Channel: name, Entries: entries}); err != nil {
		t.Fatalf("final ACL: %v", err)
	}
	return kim, name
}

// subscribeKim subscribes kim to ch from the oldest message.
func subscribeKim(t *testing.T, h *harness, kim ab2bus.Bus, ch string) {
	t.Helper()
	if err := kim.Subscribe(context.Background(), ab2bus.SubscribeInput{Channel: ch, From: "oldest"}); err != nil {
		t.Fatalf("pre subscribe: %v", err)
	}
}

func openKim(t *testing.T, b ab2bus.Bus) ab2bus.Session {
	t.Helper()
	s, err := b.Open(context.Background(), ab2bus.OpenSessionInput{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// lateTagged appends a tagged message after the session opened: a tag subscription
// matches only messages past the session start.
func lateTagged(h *harness, ch string) int64 {
	h.t.Helper()
	var sc ab2store.Channel
	h.tx(func(c context.Context, tx ab2store.Tx) error {
		var err error
		sc, err = tx.ChannelByName(c, ch)
		return err
	})
	return h.append(sc, "pat", "", "late", "env:prod")
}

func hasSeq(ms []ab2bus.Message, seq int64) bool {
	return slices.ContainsFunc(ms, func(m ab2bus.Message) bool { return m.Seq == seq })
}

func channelNames(t *testing.T, b ab2bus.Bus) map[string]ab2bus.Kind {
	t.Helper()
	cs, err := b.ListChannels(context.Background())
	must(t, err)
	out := map[string]ab2bus.Kind{}
	for _, c := range cs {
		out[c.Name] = c.Kind
	}
	return out
}

// claimedByKim claims the seeded task as kim, with a live session so the
// claim is not abandoned.
func claimedByKim(t *testing.T, h *harness, kim ab2bus.Bus, ch string) {
	t.Helper()
	h.session(fixOf(h, ch).kim, "kim")
	_, err := kim.Claim(context.Background(), ab2bus.TaskClaimInput{ID: fixOf(h, ch).task, LeasedUntil: h.taskLeaseIn(time.Hour)})
	must(t, err)
}

func matrixRows() []aclRow {
	ctx := context.Background
	forb := func(t *testing.T, err error) { t.Helper(); codeOf(t, err, ab2bus.CodeForbidden) }
	return []aclRow{
		{row: "subscribe", perm: ab2bus.PermRead, kind: ab2store.KindOrdinary, subs: []subcase{
			{name: "subscribe",
				refuse: func(t *testing.T, h *harness, b ab2bus.Bus, ch string) {
					forb(t, b.Subscribe(ctx(), ab2bus.SubscribeInput{Channel: ch}))
				},
				allow: func(t *testing.T, h *harness, b ab2bus.Bus, ch string) {
					must(t, b.Subscribe(ctx(), ab2bus.SubscribeInput{Channel: ch}))
				}},
		}},
		// "filter and query": -filter is not ported (Decision 12) and query is
		// search, so the sub-cases are receive, watch wake, history and search.
		{row: "receive, watch wake, history, search, filter and query", perm: ab2bus.PermRead, kind: ab2store.KindOrdinary, subs: []subcase{
			{name: "receive", pre: subscribeKim,
				refuse: func(t *testing.T, h *harness, b ab2bus.Bus, ch string) {
					s := openKim(t, b)
					before := sessionCursors(h, s.ID)
					r := recv(t, b, ab2bus.ReceiveInput{Session: s.ID})
					if len(r.Messages) != 0 {
						t.Fatalf("delivered %d messages from a channel without read", len(r.Messages))
					}
					if after := sessionCursors(h, s.ID); !reflect.DeepEqual(before, after) {
						t.Fatalf("cursors moved: %v -> %v", before, after)
					}
				},
				allow: func(t *testing.T, h *harness, b ab2bus.Bus, ch string) {
					s := openKim(t, b)
					r := recv(t, b, ab2bus.ReceiveInput{Session: s.ID})
					if !hasSeq(r.Messages, fixOf(h, ch).seq) {
						t.Fatalf("seq %d not delivered: %+v", fixOf(h, ch).seq, r.Messages)
					}
				}},
			{name: "watch wake", pre: subscribeKim,
				refuse: func(t *testing.T, h *harness, b ab2bus.Bus, ch string) {
					s := openKim(t, b)
					o := awaitWatch(t, startWatch(ctx(), b, ab2bus.WatchInput{Session: s.ID, Timeout: time.Second}), 2*time.Second)
					if o.err != nil || o.res.Woke {
						t.Fatalf("watch = %+v, %v; want a timeout", o.res, o.err)
					}
				},
				allow: func(t *testing.T, h *harness, b ab2bus.Bus, ch string) {
					s := openKim(t, b)
					o := awaitWatch(t, startWatch(ctx(), b, ab2bus.WatchInput{Session: s.ID, Timeout: time.Second}), 2*time.Second)
					if o.err != nil || !o.res.Woke {
						t.Fatalf("watch = %+v, %v; want a wake", o.res, o.err)
					}
				}},
			{name: "history",
				refuse: func(t *testing.T, h *harness, b ab2bus.Bus, ch string) {
					_, err := b.History(ctx(), ab2bus.HistoryInput{Channel: ch})
					forb(t, err)
				},
				allow: func(t *testing.T, h *harness, b ab2bus.Bus, ch string) {
					ms, err := b.History(ctx(), ab2bus.HistoryInput{Channel: ch})
					must(t, err)
					if !hasSeq(ms, fixOf(h, ch).seq) {
						t.Fatalf("history = %+v", ms)
					}
				}},
			{name: "search",
				refuse: func(t *testing.T, h *harness, b ab2bus.Bus, ch string) {
					_, err := b.Search(ctx(), ab2bus.SearchInput{Query: "needle", Channel: ch})
					forb(t, err)
				},
				allow: func(t *testing.T, h *harness, b ab2bus.Bus, ch string) {
					res, err := b.Search(ctx(), ab2bus.SearchInput{Query: "needle", Channel: ch})
					must(t, err)
					if len(res.Hits) == 0 || res.Hits[0].Seq != fixOf(h, ch).seq {
						t.Fatalf("hits = %+v", res.Hits)
					}
				}},
		}},
		{row: "send (publish, post), tag a message", perm: ab2bus.PermWrite, kind: ab2store.KindOrdinary, subs: []subcase{
			{name: "send",
				refuse: func(t *testing.T, h *harness, b ab2bus.Bus, ch string) {
					_, err := b.Send(ctx(), ab2bus.SendInput{Channel: ch, Content: "x"})
					forb(t, err)
				},
				allow: func(t *testing.T, h *harness, b ab2bus.Bus, ch string) {
					res, err := b.Send(ctx(), ab2bus.SendInput{Channel: ch, Content: "x"})
					must(t, err)
					if res.Seq <= 0 {
						t.Fatalf("seq = %d", res.Seq)
					}
				}},
			{name: "tag a message",
				refuse: func(t *testing.T, h *harness, b ab2bus.Bus, ch string) {
					_, err := b.Send(ctx(), ab2bus.SendInput{Channel: ch, Content: "x", Tags: []string{"env:test"}})
					forb(t, err)
				},
				allow: func(t *testing.T, h *harness, b ab2bus.Bus, ch string) {
					res, err := b.Send(ctx(), ab2bus.SendInput{Channel: ch, Content: "x", Tags: []string{"env:test"}})
					must(t, err)
					var ms []ab2store.Message
					h.tx(func(c context.Context, tx ab2store.Tx) error {
						var err error
						ms, err = tx.MessagesBySeqs(c, []int64{res.Seq})
						return err
					})
					if len(ms) != 1 || !slices.Equal(ms[0].Tags, []string{"env:test"}) {
						t.Fatalf("stored = %+v", ms)
					}
				}},
		}},
		{row: "get the ACL, set the ACL", perm: ab2bus.PermAdmin, kind: ab2store.KindOrdinary, subs: []subcase{
			{name: "get the ACL",
				refuse: func(t *testing.T, h *harness, b ab2bus.Bus, ch string) {
					_, err := b.ACL(ctx(), ch)
					forb(t, err)
				},
				allow: func(t *testing.T, h *harness, b ab2bus.Bus, ch string) {
					e, err := b.ACL(ctx(), ch)
					must(t, err)
					if len(e) == 0 {
						t.Fatal("no entries")
					}
				}},
			{name: "set the ACL",
				refuse: func(t *testing.T, h *harness, b ab2bus.Bus, ch string) {
					forb(t, b.SetACL(ctx(), ab2bus.SetACLInput{Channel: ch, Entries: []ab2bus.ACLEntry{{Identity: "everyone", Permissions: aclPerms(ab2bus.PermRead)}}}))
				},
				allow: func(t *testing.T, h *harness, b ab2bus.Bus, ch string) {
					want := []ab2bus.ACLEntry{{Identity: "everyone", Permissions: aclPerms(ab2bus.PermRead)}}
					must(t, b.SetACL(ctx(), ab2bus.SetACLInput{Channel: ch, Entries: want}))
					// Read back as a server administrator: the success leg replaced the
					// ACL, so the creator's stored row (its only way to read it, the
					// implicit creator entry being retired) is gone.
					got, err := h.admin("root").ACL(ctx(), ch)
					must(t, err)
					if len(got) != 1 || got[0].Identity != "everyone" || !slices.Equal(got[0].Permissions, want[0].Permissions) {
						t.Fatalf("ACL = %+v", got)
					}
				}},
		}},
		{row: "rename", perm: ab2bus.PermAdmin, kind: ab2store.KindOrdinary, subs: []subcase{
			{name: "rename",
				refuse: func(t *testing.T, h *harness, b ab2bus.Bus, ch string) {
					forb(t, b.RenameChannel(ctx(), ab2bus.RenameChannelInput{Name: ch, NewName: ch + "-new"}))
				},
				allow: func(t *testing.T, h *harness, b ab2bus.Bus, ch string) {
					must(t, b.RenameChannel(ctx(), ab2bus.RenameChannelInput{Name: ch, NewName: ch + "-new"}))
					if _, ok := channelNames(t, b)[ch+"-new"]; !ok {
						t.Fatal("new name not listed")
					}
				}},
		}},
		{row: "delete a channel", perm: ab2bus.PermAdmin, kind: ab2store.KindOrdinary, subs: []subcase{
			{name: "delete a channel",
				refuse: func(t *testing.T, h *harness, b ab2bus.Bus, ch string) {
					forb(t, b.DeleteChannel(ctx(), ab2bus.DeleteChannelInput{Name: ch}))
				},
				allow: func(t *testing.T, h *harness, b ab2bus.Bus, ch string) {
					must(t, b.DeleteChannel(ctx(), ab2bus.DeleteChannelInput{Name: ch}))
					if _, ok := channelNames(t, b)[ch]; ok {
						t.Fatal("name still listed")
					}
				}},
		}},
		// The switch (Decision 93): the refusal is a server setting, not an
		// ACL, so refuse builds its own harness with ChannelCreation "admins"
		// (kim is forbidden, root succeeds) and allow uses the shared one
		// ("open"), where kim succeeds.
		{row: "create a channel", perm: "", kind: ab2store.KindOrdinary, subs: []subcase{
			{name: "create a channel",
				refuse: func(t *testing.T, h *harness, _ ab2bus.Bus, ch string) {
					cfg := core.Defaults()
					cfg.ChannelCreation = core.ChannelCreationAdmins
					a := newHarness(t, h.e, harnessOptions{cfg: &cfg})
					_, err := a.as("kim").CreateChannel(ctx(), ab2bus.CreateChannelInput{Name: ch + "-k", Kind: ab2bus.KindOrdinary})
					forb(t, err)
					if _, err := a.admin("root").CreateChannel(ctx(), ab2bus.CreateChannelInput{Name: ch + "-r", Kind: ab2bus.KindOrdinary}); err != nil {
						t.Fatalf("root: %v", err)
					}
				},
				allow: func(t *testing.T, h *harness, b ab2bus.Bus, ch string) {
					if _, err := b.CreateChannel(ctx(), ab2bus.CreateChannelInput{Name: ch + "-k", Kind: ab2bus.KindOrdinary}); err != nil {
						t.Fatal(err)
					}
				}},
		}},
		{row: "list channels", perm: "", kind: ab2store.KindOrdinary, subs: []subcase{
			{name: "list channels",
				refuse: func(t *testing.T, h *harness, b ab2bus.Bus, ch string) {
					if _, ok := channelNames(t, b)[ch]; ok {
						t.Fatalf("channel %s listed without a row: %v", ch, channelNames(t, b))
					}
				},
				allow: func(t *testing.T, h *harness, b ab2bus.Bus, ch string) {
					must(t, h.as("pat").SetACL(ctx(), ab2bus.SetACLInput{Channel: ch, Entries: []ab2bus.ACLEntry{
						{Identity: "pat", Permissions: aclPerms(ab2bus.PermRead, ab2bus.PermWrite, ab2bus.PermAdmin)},
						{Identity: fixOf(h, ch).kim, Permissions: aclPerms(ab2bus.PermWrite)},
					}}))
					if k := channelNames(t, b)[ch]; k != ab2bus.KindOrdinary {
						t.Fatalf("channels = %v", channelNames(t, b))
					}
				}},
		}},
		{row: "unsubscribe", perm: "", kind: ab2store.KindOrdinary, subs: []subcase{
			{name: "unsubscribe", pre: subscribeKim,
				refuse: func(t *testing.T, h *harness, b ab2bus.Bus, ch string) {
					forb(t, b.Subscribe(ctx(), ab2bus.SubscribeInput{Channel: ch}))
				},
				allow: func(t *testing.T, h *harness, b ab2bus.Bus, ch string) {
					must(t, b.Unsubscribe(ctx(), ab2bus.UnsubscribeInput{Channel: ch}))
					if got := ackedOf(h, fixOf(h, ch).kim, fixOf(h, ch).chID); got != -1 {
						t.Fatalf("subscription remains (acked %d)", got)
					}
				}},
		}},
		{row: "tag subscription match on a channel", perm: ab2bus.PermRead, kind: ab2store.KindOrdinary, subs: []subcase{
			{name: "tag subscription match",
				pre: func(t *testing.T, h *harness, kim ab2bus.Bus, ch string) {
					must(t, kim.SubscribeTags(ctx(), ab2bus.TagSubscribeInput{Patterns: []string{"env:*"}}))
				},
				refuse: func(t *testing.T, h *harness, b ab2bus.Bus, ch string) {
					s := openKim(t, b)
					lateTagged(h, ch)
					if r := recv(t, b, ab2bus.ReceiveInput{Session: s.ID}); len(r.Messages) != 0 {
						t.Fatalf("delivered %+v", r.Messages)
					}
				},
				allow: func(t *testing.T, h *harness, b ab2bus.Bus, ch string) {
					s := openKim(t, b)
					seq := lateTagged(h, ch)
					r := recv(t, b, ab2bus.ReceiveInput{Session: s.ID})
					if len(r.Messages) != 1 || r.Messages[0].Seq != seq || !slices.Equal(r.Messages[0].MatchedTags, []string{"env:prod"}) {
						t.Fatalf("delivered %+v", r.Messages)
					}
				}},
		}},
		{row: "memory get, revisions", perm: ab2bus.PermRead, kind: ab2store.KindMemory, subs: []subcase{
			{name: "memory get",
				refuse: func(t *testing.T, h *harness, b ab2bus.Bus, ch string) {
					_, err := b.GetMemory(ctx(), fixOf(h, ch).seq)
					forb(t, err)
				},
				allow: func(t *testing.T, h *harness, b ab2bus.Bus, ch string) {
					m, err := b.GetMemory(ctx(), fixOf(h, ch).seq)
					must(t, err)
					if m.Content != "needle" {
						t.Fatalf("memory = %+v", m)
					}
				}},
			{name: "memory revisions",
				refuse: func(t *testing.T, h *harness, b ab2bus.Bus, ch string) {
					_, err := b.Revisions(ctx(), fixOf(h, ch).seq)
					forb(t, err)
				},
				allow: func(t *testing.T, h *harness, b ab2bus.Bus, ch string) {
					ms, err := b.Revisions(ctx(), fixOf(h, ch).seq)
					must(t, err)
					if len(ms) == 0 {
						t.Fatal("no revisions")
					}
				}},
		}},
		{row: "memory edit, memory delete", perm: ab2bus.PermWrite, kind: ab2store.KindMemory, subs: []subcase{
			{name: "memory edit",
				refuse: func(t *testing.T, h *harness, b ab2bus.Bus, ch string) {
					_, err := b.Edit(ctx(), ab2bus.EditMemoryInput{ID: fixOf(h, ch).seq, Content: memStrp("edited")})
					forb(t, err)
				},
				allow: func(t *testing.T, h *harness, b ab2bus.Bus, ch string) {
					res, err := b.Edit(ctx(), ab2bus.EditMemoryInput{ID: fixOf(h, ch).seq, Content: memStrp("edited")})
					must(t, err)
					if res.MemoryID != fixOf(h, ch).seq || res.Revision != 2 {
						t.Fatalf("edit = %+v", res)
					}
				}},
			{name: "memory delete",
				refuse: func(t *testing.T, h *harness, b ab2bus.Bus, ch string) {
					forb(t, b.Delete(ctx(), ab2bus.DeleteMemoryInput{ID: fixOf(h, ch).seq}))
				},
				allow: func(t *testing.T, h *harness, b ab2bus.Bus, ch string) {
					must(t, b.Delete(ctx(), ab2bus.DeleteMemoryInput{ID: fixOf(h, ch).seq}))
				}},
		}},
		{row: "task get, task list", perm: ab2bus.PermRead, kind: ab2store.KindTasks, subs: []subcase{
			{name: "task get",
				refuse: func(t *testing.T, h *harness, b ab2bus.Bus, ch string) {
					_, err := b.GetTask(ctx(), fixOf(h, ch).task)
					forb(t, err)
				},
				allow: func(t *testing.T, h *harness, b ab2bus.Bus, ch string) {
					tk, err := b.GetTask(ctx(), fixOf(h, ch).task)
					must(t, err)
					if tk.Subject != "seed" || tk.Channel != ch {
						t.Fatalf("task = %+v", tk)
					}
				}},
			{name: "task list",
				refuse: func(t *testing.T, h *harness, b ab2bus.Bus, ch string) {
					_, err := b.List(ctx(), ab2bus.TaskListInput{Channel: ch})
					forb(t, err)
				},
				allow: func(t *testing.T, h *harness, b ab2bus.Bus, ch string) {
					ts, err := b.List(ctx(), ab2bus.TaskListInput{Channel: ch})
					must(t, err)
					if len(ts) != 1 || ts[0].ID != fixOf(h, ch).task {
						t.Fatalf("list = %+v", ts)
					}
				}},
		}},
		{row: "task create, claim, release, update", perm: ab2bus.PermWrite, kind: ab2store.KindTasks, subs: []subcase{
			{name: "task create",
				refuse: func(t *testing.T, h *harness, b ab2bus.Bus, ch string) {
					_, err := b.Create(ctx(), ab2bus.TaskCreateInput{Channel: ch, Subject: "new"})
					forb(t, err)
				},
				allow: func(t *testing.T, h *harness, b ab2bus.Bus, ch string) {
					tk, err := b.Create(ctx(), ab2bus.TaskCreateInput{Channel: ch, Subject: "new"})
					must(t, err)
					if tk.ID == 0 || tk.Subject != "new" {
						t.Fatalf("task = %+v", tk)
					}
				}},
			{name: "task claim",
				refuse: func(t *testing.T, h *harness, b ab2bus.Bus, ch string) {
					h.session(fixOf(h, ch).kim, "kim")
					_, err := b.Claim(ctx(), ab2bus.TaskClaimInput{ID: fixOf(h, ch).task, LeasedUntil: h.taskLeaseIn(time.Hour)})
					forb(t, err)
				},
				allow: func(t *testing.T, h *harness, b ab2bus.Bus, ch string) {
					h.session(fixOf(h, ch).kim, "kim")
					res, err := b.Claim(ctx(), ab2bus.TaskClaimInput{ID: fixOf(h, ch).task, LeasedUntil: h.taskLeaseIn(time.Hour)})
					must(t, err)
					if res.Task.Status != "in_progress" {
						t.Fatalf("task = %+v", res.Task)
					}
				}},
			{name: "task release", pre: claimedByKim,
				refuse: func(t *testing.T, h *harness, b ab2bus.Bus, ch string) {
					_, err := b.Release(ctx(), ab2bus.TaskReleaseInput{ID: fixOf(h, ch).task})
					forb(t, err)
				},
				allow: func(t *testing.T, h *harness, b ab2bus.Bus, ch string) {
					res, err := b.Release(ctx(), ab2bus.TaskReleaseInput{ID: fixOf(h, ch).task})
					must(t, err)
					if res.Task.Status != "pending" {
						t.Fatalf("task = %+v", res.Task)
					}
				}},
			{name: "task update",
				refuse: func(t *testing.T, h *harness, b ab2bus.Bus, ch string) {
					s := "renamed"
					_, err := b.Update(ctx(), ab2bus.TaskPatch{ID: fixOf(h, ch).task, Subject: &s})
					forb(t, err)
				},
				allow: func(t *testing.T, h *harness, b ab2bus.Bus, ch string) {
					s := "renamed"
					res, err := b.Update(ctx(), ab2bus.TaskPatch{ID: fixOf(h, ch).task, Subject: &s})
					must(t, err)
					if res.Task.Subject != "renamed" {
						t.Fatalf("task = %+v", res.Task)
					}
				}},
		}},
		{row: "offered cursor on a channel", perm: ab2bus.PermRead, kind: ab2store.KindOrdinary, subs: []subcase{
			{name: "offered cursor", pre: subscribeKim,
				refuse: func(t *testing.T, h *harness, b ab2bus.Bus, ch string) {
					fx := fixOf(h, ch)
					s, err := b.Open(ctx(), ab2bus.OpenSessionInput{Cursors: []ab2bus.OfferedCursor{{Channel: ch, Seq: fx.seq}}})
					must(t, err)
					if len(s.RejectedCursors) != 1 || s.RejectedCursors[0] != (ab2bus.RejectedCursor{Channel: ch, Reason: "unreadable"}) {
						t.Fatalf("rejected = %+v", s.RejectedCursors)
					}
					if got := sessionCursors(h, s.ID)[fx.chID].CursorSeq; got == fx.seq {
						t.Fatalf("the offered cursor %d was honored", got)
					}
				},
				allow: func(t *testing.T, h *harness, b ab2bus.Bus, ch string) {
					fx := fixOf(h, ch)
					s, err := b.Open(ctx(), ab2bus.OpenSessionInput{Cursors: []ab2bus.OfferedCursor{{Channel: ch, Seq: fx.seq}}})
					must(t, err)
					if len(s.RejectedCursors) != 0 {
						t.Fatalf("rejected = %+v", s.RejectedCursors)
					}
					if got := sessionCursors(h, s.ID)[fx.chID].CursorSeq; got != fx.seq {
						t.Fatalf("cursor = %d, want the offered %d", got, fx.seq)
					}
				}},
		}},
	}
}

// section46Rows is a literal copy of the first column of the section 4.6
// table of the design spec, as the plan words the rows.
var section46Rows = []string{
	"subscribe",
	"receive, watch wake, history, search, filter and query",
	"send (publish, post), tag a message",
	"get the ACL, set the ACL",
	"rename",
	"delete a channel",
	"create a channel",
	"list channels",
	"unsubscribe",
	"tag subscription match on a channel",
	"memory get, revisions",
	"memory edit, memory delete",
	"task get, task list",
	"task create, claim, release, update",
	"offered cursor on a channel",
}

// legPerms is kim's stored permissions: for a row with a permission, the two
// the row does not need (refusal) or the one it needs (success); none for a
// row without.
func legPerms(r aclRow, refusal bool) []ab2bus.Permission {
	switch {
	case r.perm == "":
		return nil
	case refusal:
		return othersThan(r.perm)
	default:
		return aclPerms(r.perm)
	}
}

func TestACLMatrix(t *testing.T) {
	rows := matrixRows()
	var names []string
	n := 0
	for _, r := range rows {
		names = append(names, r.row)
		n += len(r.subs)
	}
	if !slices.Equal(names, section46Rows) {
		t.Fatalf("rows = %q, want the section 4.6 rows %q", names, section46Rows)
	}
	if n != matrixSubcases {
		t.Fatalf("%d sub-cases, want %d", n, matrixSubcases)
	}
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, watchOpts(nil))
		for _, r := range rows {
			t.Run(r.row, func(t *testing.T) {
				for _, s := range r.subs {
					t.Run(s.name, func(t *testing.T) {
						legs := []struct {
							name  string
							perms []ab2bus.Permission
							run   func(*testing.T, *harness, ab2bus.Bus, string)
						}{
							{"refusal", legPerms(r, true), s.refuse},
							{"success", legPerms(r, false), s.allow},
						}
						for _, leg := range legs {
							t.Run(leg.name, func(t *testing.T) {
								h.t = t
								kim, ch := matrixChannel(t, h, r, s, leg.perms)
								leg.run(t, h, kim, ch)
							})
						}
					})
				}
			})
		}
	})
}
