package bus

import (
	"context"
	"testing"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
)

const (
	bridgedACLAdminOnly = "only a server administrator may change the ACL of a bridged channel"
)

// TestBridgedChannelGuards: a creating or live bridge pins its channel's
// ACL, name and existence (spec 6.1b, Decision 12); an archived bridge or no
// bridge leaves the rules unchanged.
func TestBridgedChannelGuards(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		h.rtScope("job1")
		coder := h.scopedIdentity("job1-coder", "job1")
		adm := h.admin("adm")
		h.human("eric@corp.example")
		h.human("kim@corp.example")

		acl := []ab2store.ACLEntry{
			{Identity: "job1-coder", Permissions: []ab2store.Permission{ab2store.PermRead, ab2store.PermWrite, ab2store.PermAdmin}},
			{Identity: "eric@corp.example", Permissions: []ab2store.Permission{ab2store.PermRead, ab2store.PermWrite}},
			{Identity: ab2store.SlackBridge, Permissions: []ab2store.Permission{ab2store.PermRead}},
			{Identity: ab2store.Admins, Permissions: []ab2store.Permission{ab2store.PermAdmin}},
		}
		var ch ab2store.Channel
		h.tx(func(ctx context.Context, tx ab2store.Tx) error {
			seq, err := tx.MaxSeq(ctx)
			if err != nil {
				return err
			}
			ch, err = tx.CreateChannel(ctx, ab2store.ChannelCreate{
				Name: "job1-chat", Kind: ab2store.KindOrdinary, Creator: "job1-coder", Scope: "job1", ACL: acl, CreatedSeq: seq, CreatedAt: h.core.NowMs(),
			})
			return err
		})
		b := h.rtBridge(ch, "job1", "C1")

		bus := func(entries ...ab2bus.ACLEntry) ab2bus.SetACLInput {
			return ab2bus.SetACLInput{Channel: "job1-chat", Entries: entries}
		}
		perm := func(id string, ps ...ab2bus.Permission) ab2bus.ACLEntry {
			return ab2bus.ACLEntry{Identity: id, Permissions: ps}
		}
		R, W, A := ab2bus.PermRead, ab2bus.PermWrite, ab2bus.PermAdmin
		base := []ab2bus.ACLEntry{
			perm("job1-coder", R, W, A), perm("eric@corp.example", R, W),
			perm(ab2store.SlackBridge, R), perm(ab2store.Admins, A),
		}
		with := func(extra ...ab2bus.ACLEntry) ab2bus.SetACLInput {
			return bus(append(append([]ab2bus.ACLEntry{}, base...), extra...)...)
		}

		// A channel admin that is not a server administrator is refused,
		// before the ACL rules look at the entries.
		err := coder.SetACL(ctx, with(perm("kim@corp.example", W)))
		codeOf(t, err, ab2bus.CodeForbidden)
		if got := msgOf(err); got != bridgedACLAdminOnly {
			t.Fatalf("coder SetACL message = %q", got)
		}
		// The server administrator is held to the bridge ACL rules.
		everyoneMsg := "a bridged channel may grant read or write only to named identities (Decision 12)"
		pinnedMsg := "the slack-bridge read entry is pinned while the bridge is live"
		for _, c := range []struct {
			name string
			in   ab2bus.SetACLInput
			msg  string
		}{
			{"everyone read", with(perm(ab2store.Everyone, R)), everyoneMsg},
			{"admins read", bus(perm("job1-coder", R, W, A), perm("eric@corp.example", R, W), perm(ab2store.SlackBridge, R), perm(ab2store.Admins, R)), everyoneMsg},
			{"slack-bridge dropped", bus(perm("job1-coder", R, W, A), perm("eric@corp.example", R, W), perm(ab2store.Admins, A)), pinnedMsg},
		} {
			err := adm.SetACL(ctx, c.in)
			codeOf(t, err, ab2bus.CodeForbidden)
			if got := msgOf(err); got != c.msg {
				t.Fatalf("%s: message = %q, want %q", c.name, got, c.msg)
			}
		}
		if err := adm.SetACL(ctx, with(perm("kim@corp.example", W))); err != nil {
			t.Fatalf("admin adding a named identity: %v", err)
		}

		// Delete and rename are refused for the administrator too.
		wantMsg := `channel "job1-chat" has a live Slack bridge; run bridges remove first`
		err = adm.DeleteChannel(ctx, ab2bus.DeleteChannelInput{Name: "job1-chat"})
		codeOf(t, err, ab2bus.CodeConflict)
		if got := msgOf(err); got != wantMsg {
			t.Fatalf("delete message = %q", got)
		}
		err = adm.RenameChannel(ctx, ab2bus.RenameChannelInput{Name: "job1-chat", NewName: "job1-talk"})
		codeOf(t, err, ab2bus.CodeConflict)
		if got := msgOf(err); got != wantMsg {
			t.Fatalf("rename message = %q", got)
		}

		// A creating bridge pins the channel the same way.
		h.tx(func(ctx context.Context, tx ab2store.Tx) error { return tx.DeleteBridge(ctx, b.ID) })
		h.tx(func(ctx context.Context, tx ab2store.Tx) error {
			_, err := tx.CreateBridge(ctx, ab2store.Bridge{ChannelID: ch.ID, ChannelName: ch.Name, Scope: "job1", SlackName: "ab-job1-chat", CreatedAt: 9})
			return err
		})
		codeOf(t, adm.DeleteChannel(ctx, ab2bus.DeleteChannelInput{Name: "job1-chat"}), ab2bus.CodeConflict)
		codeOf(t, coder.SetACL(ctx, with()), ab2bus.CodeForbidden)

		// Archived: the ordinary rules return.
		h.tx(func(ctx context.Context, tx ab2store.Tx) error {
			cur, err := tx.BridgeByChannel(ctx, ch.ID)
			if err != nil {
				return err
			}
			return tx.SetBridgeArchived(ctx, cur.ID, h.core.NowMs())
		})
		if err := coder.SetACL(ctx, bus(perm("job1-coder", R, W, A), perm("eric@corp.example", R, W), perm("kim@corp.example", W), perm(ab2store.Admins, A))); err != nil {
			t.Fatalf("SetACL after archive: %v", err)
		}
		if err := adm.RenameChannel(ctx, ab2bus.RenameChannelInput{Name: "job1-chat", NewName: "job1-talk"}); err != nil {
			t.Fatalf("rename after archive: %v", err)
		}
		if err := adm.DeleteChannel(ctx, ab2bus.DeleteChannelInput{Name: "job1-talk"}); err != nil {
			t.Fatalf("delete after archive: %v", err)
		}

		// No bridge: the creator still sets the ACL of its own channel.
		h.channel("plain", ab2store.KindOrdinary, "plain-owner", nil)
		if err := h.as("plain-owner").SetACL(ctx, ab2bus.SetACLInput{Channel: "plain", Entries: []ab2bus.ACLEntry{perm("plain-owner", R, W, A), perm(ab2store.Everyone, R, W), perm(ab2store.Admins, A)}}); err != nil {
			t.Fatalf("unbridged SetACL: %v", err)
		}
		// The creator administers through its stored row only (the implicit
		// creator entry is retired): once the row is gone it is forbidden.
		plain := h.as("plain-owner")
		if err := plain.SetACL(ctx, ab2bus.SetACLInput{Channel: "plain", Entries: []ab2bus.ACLEntry{perm(ab2store.Everyone, R, W), perm(ab2store.Admins, A)}}); err != nil {
			t.Fatalf("SetACL dropping the creator's row: %v", err)
		}
		codeOf(t, plain.SetACL(ctx, ab2bus.SetACLInput{Channel: "plain", Entries: []ab2bus.ACLEntry{perm("plain-owner", R, W, A)}}), ab2bus.CodeForbidden)
	})
}
