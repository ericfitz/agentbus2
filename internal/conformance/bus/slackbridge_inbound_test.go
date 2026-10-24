package bus

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/identities"
	"github.com/ericfitz/agentbus2/internal/dbtest"
	"github.com/ericfitz/agentbus2/internal/slackapi"
	"github.com/ericfitz/agentbus2/internal/slackbridge"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
)

const (
	siBob   = "bob@corp.example"   // human, no ACL entry
	siKim   = "kim@corp.example"   // human, in the ACL, bound to U5 by the tests that need it
	siRon   = "ron@corp.example"   // human, in the ACL
	siCat   = "cat@corp.example"   // human, Slack account of another workspace
	siDee   = "dee@corp.example"   // human, Slack account deleted
	siBotU  = "bot@corp.example"   // human, Slack account a bot
	siUla   = "ula@corp.example"   // human, Slack account ultra restricted
	siGhost = "ghost@corp.example" // Slack account with no identity
)

// inFix is a live bridge of job1-chat with Slack users for the inbound cases.
type inFix struct {
	*slackFix
	ch ab2store.Channel
	sl string // the Slack channel ID

	synthN int
}

// core0 is the core Config with a send limit of one message per second.
func core0() core.Config {
	c := core.Defaults()
	c.SendMessagesPerSecond = 1
	return c
}

// raceTx overrides the Tx calls a test makes fail or race.
type raceTx struct {
	ab2store.Tx
	putSlackUser func(ab2store.SlackUser) error // nil: pass through
}

func (t raceTx) PutSlackUser(ctx context.Context, u ab2store.SlackUser) error {
	if t.putSlackUser != nil {
		return t.putSlackUser(u)
	}
	return t.Tx.PutSlackUser(ctx, u)
}

// bindRaceStore makes every PutSlackUser inside an Append lose a binding race.
type bindRaceStore struct{ ab2store.Store }

func (s *bindRaceStore) Append(ctx context.Context, fn func(context.Context, ab2store.Tx) error) error {
	return s.Store.Append(ctx, func(ctx context.Context, tx ab2store.Tx) error {
		return fn(ctx, raceTx{Tx: tx, putSlackUser: func(ab2store.SlackUser) error {
			return fmt.Errorf("simulated race: %w", ab2store.ErrConflict)
		}})
	})
}

// archiveRaceStore archives the bridge inside every Append before the send
// runs, as a maintenance tick that commits between the lookup and the send.
type archiveRaceStore struct {
	ab2store.Store
	bridgeID int64
}

func (s *archiveRaceStore) Append(ctx context.Context, fn func(context.Context, ab2store.Tx) error) error {
	return s.Store.Append(ctx, func(ctx context.Context, tx ab2store.Tx) error {
		if err := tx.SetBridgeArchived(ctx, s.bridgeID, 1); err != nil {
			return err
		}
		return fn(ctx, tx)
	})
}

func newInFix(t *testing.T, e dbtest.Engine) *inFix {
	return newInFixOpts(t, e, harnessOptions{}, nil)
}

func newInFixOpts(t *testing.T, e dbtest.Engine, o harnessOptions, mod func(*slackbridge.Config)) *inFix {
	t.Helper()
	x := newSlackFixOpts(t, e, o, mod)
	x.fake.AddUser(slackapi.User{ID: "U1", TeamID: slTeam, Email: slAlice, DisplayName: "Alice"})
	for _, u := range []slackapi.User{
		{ID: "U4", TeamID: slTeam, Email: siBob, DisplayName: "Bob"},
		{ID: "U5", TeamID: slTeam, Email: siKim, DisplayName: "Kim"},
		{ID: "U6", TeamID: "T9", Email: siCat, DisplayName: "Cat"},
		{ID: "U7", TeamID: slTeam, Email: siDee, DisplayName: "Dee", Deleted: true},
		{ID: "U8", TeamID: slTeam, Email: siBotU, DisplayName: "Botty", IsBot: true},
		{ID: "U9", TeamID: slTeam, Email: siGhost, DisplayName: "Ghost"},
		{ID: "U10", TeamID: slTeam, Email: slAlice, DisplayName: "Alice Clone"},
		{ID: "U11", TeamID: slTeam, Email: siRon, DisplayName: "Ron"},
		{ID: "U12", TeamID: slTeam, Email: siUla, DisplayName: "Ula", UltraRestricted: true},
	} {
		x.fake.AddUser(u)
	}
	for _, em := range []string{siBob, siKim, siRon, siCat, siDee, siBotU, siUla} {
		if _, _, err := (identities.Service{Core: x.h.core}).CreateIdentityByEmail(context.Background(), em); err != nil {
			t.Fatalf("CreateIdentityByEmail %s: %v", em, err)
		}
	}
	R, W, A := ab2store.PermRead, ab2store.PermWrite, ab2store.PermAdmin
	acl := []ab2store.ACLEntry{
		slEntry("job1-coder", R, W, A), slEntry(slAlice, R, W), slEntry(slGina, R, W), slEntry(slXav, R, W),
		slEntry(slNone, R, W), slEntry(siKim, R, W), slEntry(siRon, R, W), slEntry(siCat, R, W), slEntry(siDee, R, W),
		slEntry(siBotU, R, W), slEntry(siUla, R, W), slEntry(ab2store.Admins, A),
	}
	ch := x.slChannel("job1-chat", "job1", acl)
	got, err := x.create("job1-chat", "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	f := &inFix{slackFix: x, ch: ch, sl: got.Bridge.SlackChannelID}
	if err := x.h.localAs("job1-coder").Subscribe(context.Background(), ab2bus.SubscribeInput{Channel: "job1-chat", From: "oldest"}); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	return f
}

func (f *inFix) event(m slackapi.Message) slackapi.Event {
	return slackapi.Event{Kind: slackapi.EventMessage, EnvelopeID: "env", Channel: f.sl, User: m.User, Message: m}
}

// handle runs the message through the event path and answers the outcome.
func (f *inFix) handle(m slackapi.Message) string {
	f.h.t.Helper()
	return f.svc.ProcessEvent(context.Background(), f.event(m))
}

func (f *inFix) want(m slackapi.Message, outcome string) {
	f.h.t.Helper()
	if got := f.handle(m); got != outcome {
		f.h.t.Errorf("outcome for ts %s = %q, want %q", m.TS, got, outcome)
	}
}

// human lists the messages of the channel sent by humans.
func (f *inFix) human() []ab2store.Message {
	f.h.t.Helper()
	var out []ab2store.Message
	for _, m := range f.messages(f.ch) {
		if m.SenderKind == ab2store.IdentityHuman {
			out = append(out, m)
		}
	}
	return out
}

func (f *inFix) bridge() ab2store.Bridge {
	f.h.t.Helper()
	b, ok := f.bridgeRow(f.ch)
	if !ok {
		f.h.t.Fatal("no bridge row")
	}
	return b
}

func (f *inFix) mapRow(ts string) (ab2store.BridgeMessage, bool) {
	f.h.t.Helper()
	var row ab2store.BridgeMessage
	err := f.h.raw.Read(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
		var err error
		row, err = tx.BridgeMessageByTS(ctx, f.bridge().ID, ts)
		return err
	})
	if errors.Is(err, ab2store.ErrNotFound) {
		return row, false
	}
	if err != nil {
		f.h.t.Fatalf("BridgeMessageByTS: %v", err)
	}
	return row, true
}

func (f *inFix) dropped(ts string) bool {
	f.h.t.Helper()
	var d bool
	f.h.tx(func(ctx context.Context, tx ab2store.Tx) error {
		var err error
		d, err = tx.BridgeDropped(ctx, f.bridge().ID, ts)
		return err
	})
	return d
}

// boundTo answers the Slack user bound to identity ("" when none).
func (f *inFix) boundTo(identity string) string {
	f.h.t.Helper()
	var id string
	f.h.tx(func(ctx context.Context, tx ab2store.Tx) error {
		u, err := tx.SlackUserOf(ctx, identity)
		if errors.Is(err, ab2store.ErrNotFound) {
			return nil
		}
		id = u.TeamID + "/" + u.UserID
		return err
	})
	return id
}

func (f *inFix) bindingOwner(user string) string {
	f.h.t.Helper()
	var id string
	f.h.tx(func(ctx context.Context, tx ab2store.Tx) error {
		o, err := tx.IdentityOfSlackUser(ctx, slTeam, user)
		if errors.Is(err, ab2store.ErrNotFound) {
			return nil
		}
		id = o
		return err
	})
	return id
}

func (f *inFix) bind(identity, user string) {
	f.h.t.Helper()
	f.h.tx(func(ctx context.Context, tx ab2store.Tx) error {
		return tx.PutSlackUser(ctx, ab2store.SlackUser{Identity: identity, TeamID: slTeam, UserID: user, CreatedAt: 1})
	})
}

// agentPost writes a bus message from job1-coder (replying to replyTo when
// not 0) and its Slack post with a map row, as the outbound worker would:
// the Slack message is a bot message with the seq in its metadata.
func (f *inFix) agentPost(text string, replyTo int64, thread string) (int64, slackapi.Message) {
	f.h.t.Helper()
	in := ab2bus.SendInput{Channel: "job1-chat", Content: text}
	if replyTo != 0 {
		in.ReplyTo = &replyTo
	}
	res, err := newBus(f.h, "job1-coder").Send(context.Background(), in)
	if err != nil {
		f.h.t.Fatalf("agent Send: %v", err)
	}
	m := f.fake.SayAsBot(f.sl, "BBOT", text, thread, res.Seq)
	root := thread
	if root == "" {
		root = m.TS
	}
	f.h.tx(func(ctx context.Context, tx ab2store.Tx) error {
		return tx.PutBridgeMessage(ctx, ab2store.BridgeMessage{BridgeID: f.bridge().ID, Seq: res.Seq, SlackTS: m.TS, RootTS: root, CreatedAt: 1})
	})
	return res.Seq, m
}

// synth is a Slack message that was never added to the fake (its channel may
// be archived), with a fresh valid ts.
func (f *inFix) synth(user, text string) slackapi.Message {
	f.synthN++
	return slackapi.Message{TS: fmt.Sprintf("1800000000.%06d", f.synthN), User: user, Text: text}
}

func (f *inFix) droppedLogs() int { return strings.Count(f.logs.String(), "slack message dropped") }

func tsLink(channel, ts string) string {
	return "https://corp.slack.com/archives/" + channel + "/p" + strings.Replace(ts, ".", "", 1)
}

func TestSlackBridgeInbound(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		ctx := context.Background()

		t.Run("authorized-top-level", func(t *testing.T) {
			f := newInFix(t, e)
			m := f.fake.Say(f.sl, "U1", "hi *there*", "")
			f.want(m, "sent")
			got := f.human()
			if len(got) != 1 || got[0].Sender != slAlice || got[0].SenderKind != ab2store.IdentityHuman || got[0].Content != "hi *there*" || got[0].ReplyTo != nil {
				t.Fatalf("messages = %+v", got)
			}
			if got[0].Metadata["slack_display_name"] != "Alice" {
				t.Errorf("metadata = %v", got[0].Metadata)
			}
			if row, ok := f.mapRow(m.TS); !ok || row.Seq != got[0].Seq || row.RootTS != m.TS {
				t.Errorf("map row = %+v, %v", row, ok)
			}
			if f.boundTo(slAlice) != slTeam+"/U1" || f.bindingOwner("U1") != slAlice {
				t.Errorf("binding = %q / %q", f.boundTo(slAlice), f.bindingOwner("U1"))
			}
			// The agent receives it, from a human, in the Slack session.
			coder := f.h.localAs("job1-coder")
			sess, err := coder.Open(ctx, ab2bus.OpenSessionInput{})
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			res, err := coder.Receive(ctx, ab2bus.ReceiveInput{Session: sess.ID, Channels: []string{"job1-chat"}})
			if err != nil {
				t.Fatalf("Receive: %v", err)
			}
			var rec []ab2bus.Message
			for _, rm := range res.Messages {
				if rm.Sender == slAlice {
					rec = append(rec, rm)
				}
			}
			if len(rec) != 1 || rec[0].SenderKind != ab2bus.IdentityHuman || rec[0].Display != slAlice+"/slack" || rec[0].Metadata["slack_display_name"] != "Alice" {
				t.Fatalf("received = %+v", res.Messages)
			}
			f.noSecretsLogged(t)
		})

		t.Run("repeat-ignored", func(t *testing.T) {
			f := newInFix(t, e)
			m := f.fake.Say(f.sl, "U1", "once", "")
			f.want(m, "sent")
			f.want(m, "duplicate")
			f.want(m, "duplicate")
			if n := len(f.human()); n != 1 {
				t.Errorf("%d messages, want 1", n)
			}
		})

		t.Run("own-bot-ignored", func(t *testing.T) {
			f := newInFix(t, e)
			f.want(f.fake.SayAsBot(f.sl, "BBOT", "mine", "", 0), "ignored")
			f.want(f.fake.SayAsBot(f.sl, "BBOT", "mine", "", 7), "ignored") // not catch-up: no repair
			f.want(f.fake.Say(f.sl, slBot, "mine too", ""), "ignored")
			if f.droppedLogs() != 0 || len(f.human()) != 0 {
				t.Errorf("own posts reached the bus or the drop log")
			}
		})

		t.Run("subtypes-ignored", func(t *testing.T) {
			f := newInFix(t, e)
			for _, st := range []string{"message_changed", "message_deleted", "channel_join", "channel_leave", "file_share", "bot_message", "weird"} {
				f.want(f.fake.SaySubtype(f.sl, "U1", "text "+st, "", st), "ignored")
			}
			if len(f.human()) != 0 || f.droppedLogs() != 0 {
				t.Error("an ignored subtype reached the bus or was recorded")
			}
		})

		t.Run("thread-broadcast-accepted", func(t *testing.T) {
			f := newInFix(t, e)
			root := f.fake.Say(f.sl, "U1", "root", "")
			f.want(root, "sent")
			f.want(f.fake.SayBroadcast(f.sl, "U1", "also to the channel", root.TS), "sent")
			got := f.human()
			if len(got) != 2 || got[1].ReplyTo == nil || *got[1].ReplyTo != got[0].Seq {
				t.Fatalf("messages = %+v", got)
			}
		})

		t.Run("empty-user-ignored", func(t *testing.T) {
			f := newInFix(t, e)
			f.want(f.fake.Say(f.sl, "", "nobody", ""), "ignored")
			f.want(f.fake.Say(f.sl, "U1", "   ", ""), "ignored")
			f.want(slackapi.Message{TS: "not-a-ts", User: "U1", Text: "x"}, "ignored")
			f.want(slackapi.Message{TS: "1700000099.000001", ThreadTS: "bad", User: "U1", Text: "x"}, "ignored")
			if len(f.human()) != 0 {
				t.Error("an ignored message reached the bus")
			}
		})

		t.Run("other-kinds-ignored", func(t *testing.T) {
			f := newInFix(t, e)
			m := f.fake.Say(f.sl, "U1", "x", "")
			for _, k := range []slackapi.EventKind{slackapi.EventConnected, slackapi.EventMemberLeft, "reaction_added", ""} {
				ev := f.event(m)
				ev.Kind = k
				if got := f.svc.ProcessEvent(ctx, ev); got != "ignored" {
					t.Errorf("kind %q = %q", k, got)
				}
				f.svc.HandleEvent(ctx, ev)
			}
			if len(f.human()) != 0 {
				t.Error("a non-message event reached the bus")
			}
		})

		refusals := []struct {
			name, user, bound string
		}{
			{"guest-dropped", "U2", ""},
			{"ultra-restricted-dropped", "U12", ""},
			{"stranger-dropped", "U3", ""},
			{"other-team-dropped", "U6", ""},
			{"deleted-user-dropped", "U7", ""},
			{"bot-user-dropped", "U8", ""},
			{"no-human-dropped", "U9", ""},
			{"not-in-acl-dropped", "U4", ""},
		}
		for _, c := range refusals {
			t.Run(c.name, func(t *testing.T) {
				f := newInFix(t, e)
				text := "secret-" + c.name
				m := f.fake.Say(f.sl, c.user, text, "")
				f.want(m, "dropped")
				if len(f.human()) != 0 {
					t.Error("a dropped message reached the bus")
				}
				if _, ok := f.mapRow(m.TS); ok || !f.dropped(m.TS) {
					t.Error("a dropped message must have a drop row and no map row")
				}
				log := f.logs.String()
				if !strings.Contains(log, "slack message dropped") || !strings.Contains(log, "slack_user="+c.user) || !strings.Contains(log, "slack_channel="+f.sl) {
					t.Errorf("log lacks the drop line with user and channel:\n%s", log)
				}
				if strings.Contains(log, text) {
					t.Errorf("log contains the message text:\n%s", log)
				}
				f.noSecretsLogged(t)
				f.want(m, "ignored") // a drop is final
				if f.droppedLogs() != 1 {
					t.Errorf("drop logged %d times", f.droppedLogs())
				}
				if f.boundTo(slAlice) != "" || f.boundTo(siBob) != "" {
					t.Error("a dropped message created a binding")
				}
			})
		}

		t.Run("not-in-acl-then-granted", func(t *testing.T) {
			f := newInFix(t, e)
			f.want(f.fake.Say(f.sl, "U4", "first", ""), "dropped")
			f.h.tx(func(ctx context.Context, tx ab2store.Tx) error {
				acl, err := tx.ACL(ctx, f.ch.ID)
				if err != nil {
					return err
				}
				return tx.SetACL(ctx, f.ch.ID, append(acl, slEntry(siBob, ab2store.PermRead, ab2store.PermWrite)))
			})
			f.want(f.fake.Say(f.sl, "U4", "second", ""), "sent")
			got := f.human()
			if len(got) != 1 || got[0].Content != "second" || got[0].Sender != siBob {
				t.Fatalf("messages = %+v", got)
			}
		})

		t.Run("read-only-acl-dropped", func(t *testing.T) {
			f := newInFix(t, e)
			f.h.tx(func(ctx context.Context, tx ab2store.Tx) error {
				acl, err := tx.ACL(ctx, f.ch.ID)
				if err != nil {
					return err
				}
				return tx.SetACL(ctx, f.ch.ID, append(acl, slEntry(siBob, ab2store.PermRead)))
			})
			f.want(f.fake.Say(f.sl, "U4", "read only", ""), "dropped")
		})

		t.Run("everyone-write-is-not-explicit", func(t *testing.T) {
			f := newInFix(t, e)
			f.h.tx(func(ctx context.Context, tx ab2store.Tx) error {
				acl, err := tx.ACL(ctx, f.ch.ID)
				if err != nil {
					return err
				}
				return tx.SetACL(ctx, f.ch.ID, append(acl, slEntry(ab2store.Everyone, ab2store.PermRead, ab2store.PermWrite)))
			})
			f.want(f.fake.Say(f.sl, "U4", "via everyone", ""), "dropped")
		})

		t.Run("email-moved-to-other-account", func(t *testing.T) {
			f := newInFix(t, e)
			f.want(f.fake.Say(f.sl, "U1", "from the real account", ""), "sent")
			if f.boundTo(slAlice) != slTeam+"/U1" {
				t.Fatalf("alice is bound to %q", f.boundTo(slAlice))
			}
			// A second Slack account presents alice's email: refused every time.
			for i := range 2 {
				f.want(f.fake.Say(f.sl, "U10", fmt.Sprintf("impostor %d", i), ""), "dropped")
			}
			if f.bindingOwner("U10") != "" || f.boundTo(slAlice) != slTeam+"/U1" {
				t.Errorf("bindings changed: U10 -> %q, alice -> %q", f.bindingOwner("U10"), f.boundTo(slAlice))
			}

			// U1's profile email changes to kim's, who is bound to U5.
			f.bind(siKim, "U5")
			f.fake.AddUser(slackapi.User{ID: "U1", TeamID: slTeam, Email: siKim, DisplayName: "Alice"})
			f.h.clock.Advance(11 * time.Minute) // past the users.info cache
			for i := range 2 {
				f.want(f.fake.Say(f.sl, "U1", fmt.Sprintf("moved %d", i), ""), "dropped")
			}
			if f.boundTo(siKim) != slTeam+"/U5" || f.boundTo(slAlice) != slTeam+"/U1" || f.bindingOwner("U1") != slAlice {
				t.Errorf("bindings changed: kim -> %q, alice -> %q, U1 -> %q", f.boundTo(siKim), f.boundTo(slAlice), f.bindingOwner("U1"))
			}
			if n := len(f.human()); n != 1 {
				t.Errorf("%d human messages, want only the first", n)
			}
			f.noSecretsLogged(t)
		})

		t.Run("binding-race-is-dropped-not-duplicate", func(t *testing.T) {
			f := newInFix(t, e)
			orig := f.h.core.Store
			f.h.core.Store = &bindRaceStore{Store: orig}
			defer func() { f.h.core.Store = orig }()
			m := f.fake.Say(f.sl, "U11", "racing", "")
			f.want(m, "dropped")
			if len(f.human()) != 0 {
				t.Error("a lost binding race delivered the message")
			}
			if _, ok := f.mapRow(m.TS); ok {
				t.Error("a map row survived the rolled-back send")
			}
			if f.boundTo(siRon) != "" {
				t.Error("a binding survived the rolled-back send")
			}
			f.h.core.Store = orig
			// The binding is available again: a later message is sent.
			f.want(f.fake.Say(f.sl, "U11", "after the race", ""), "sent")
		})

		t.Run("thread-reply", func(t *testing.T) {
			f := newInFix(t, e)
			s1, root := f.agentPost("agent root", 0, "")
			s2, _ := f.agentPost("agent follow-up", s1, root.TS)
			m := f.fake.Say(f.sl, "U1", "my reply", root.TS)
			f.want(m, "sent")
			got := f.human()
			if len(got) != 1 || got[0].ReplyTo == nil || *got[0].ReplyTo != s1 {
				t.Fatalf("messages = %+v, want reply_to %d", got, s1)
			}
			if got[0].Metadata["slack_after"] != strconv.FormatInt(s2, 10) {
				t.Errorf("slack_after = %q, want %d", got[0].Metadata["slack_after"], s2)
			}
			if row, ok := f.mapRow(m.TS); !ok || row.RootTS != root.TS {
				t.Errorf("map row = %+v, %v", row, ok)
			}
			// A second reply sees the first one as the newest in the thread.
			m2 := f.fake.Say(f.sl, "U1", "my second reply", root.TS)
			f.want(m2, "sent")
			got = f.human()
			if len(got) != 2 || got[1].Metadata["slack_after"] != strconv.FormatInt(got[0].Seq, 10) {
				t.Errorf("second reply metadata = %v, want slack_after %d", got[1].Metadata, got[0].Seq)
			}
		})

		t.Run("permalink-parent", func(t *testing.T) {
			f := newInFix(t, e)
			s1, root := f.agentPost("agent root", 0, "")
			s2, reply := f.agentPost("agent follow-up", s1, root.TS)
			m := f.fake.Say(f.sl, "U1", "see "+tsLink(f.sl, reply.TS)+" please", "")
			f.want(m, "sent")
			got := f.human()
			if len(got) != 1 || got[0].ReplyTo == nil || *got[0].ReplyTo != s1 {
				t.Fatalf("messages = %+v, want reply_to %d (the root)", got, s1)
			}
			if got[0].Metadata["slack_after"] != strconv.FormatInt(s2, 10) {
				t.Errorf("slack_after = %q, want %d", got[0].Metadata["slack_after"], s2)
			}
			// A permalink to another Slack channel is only text.
			m2 := f.fake.Say(f.sl, "U1", "other "+tsLink("C99999999", reply.TS), "")
			f.want(m2, "sent")
			got = f.human()
			if len(got) != 2 || got[1].ReplyTo != nil || got[1].Metadata["slack_after"] != "" {
				t.Errorf("foreign permalink message = %+v", got[1])
			}
		})

		t.Run("aged-out-root-quoted", func(t *testing.T) {
			f := newInFix(t, e)
			root := f.fake.SayAsBot(f.sl, "BBOT", "Original *text*\nsecond line", "", 3)
			m := f.fake.Say(f.sl, "U1", "a late reply", root.TS)
			f.want(m, "sent")
			got := f.human()
			if len(got) != 1 || got[0].ReplyTo != nil {
				t.Fatalf("messages = %+v", got)
			}
			want := "> Original *text*\n> second line\n\na late reply"
			if got[0].Content != want {
				t.Errorf("content = %q, want %q", got[0].Content, want)
			}
		})

		t.Run("unauthorized-root-not-quoted", func(t *testing.T) {
			f := newInFix(t, e)
			root := f.fake.Say(f.sl, "U2", "guest root text", "")
			f.want(root, "dropped")
			m := f.fake.Say(f.sl, "U1", "alice replies", root.TS)
			f.want(m, "sent")
			got := f.human()
			if len(got) != 1 || got[0].ReplyTo != nil || got[0].Content != "alice replies" {
				t.Fatalf("messages = %+v", got)
			}
			if strings.Contains(got[0].Content, "guest") {
				t.Error("the unauthorized root leaked into the reply")
			}
		})

		t.Run("archived-bridge-ignored", func(t *testing.T) {
			f := newInFix(t, e)
			if err := f.svc.ArchiveBridge(ctx, f.bridge(), "test"); err != nil {
				t.Fatalf("ArchiveBridge: %v", err)
			}
			m := f.synth("U4", "late")
			f.want(m, "ignored")
			m2 := f.synth("U1", "late too")
			f.want(m2, "ignored")
			if len(f.human()) != 0 {
				t.Error("an event for an archived bridge reached the bus")
			}
			for _, ts := range []string{m.TS, m2.TS} {
				if _, ok := f.mapRow(ts); ok || f.dropped(ts) {
					t.Errorf("rows written for ts %s", ts)
				}
			}
			if f.boundTo(slAlice) != "" || f.boundTo(siBob) != "" {
				t.Error("a binding was written")
			}
			if err := f.svc.CatchUp(ctx); err != nil || f.fake.Calls("History") != 0 {
				t.Errorf("CatchUp = %v, History calls %d", err, f.fake.Calls("History"))
			}
		})

		t.Run("removed-bridge-ignored", func(t *testing.T) {
			f := newInFix(t, e)
			if err := f.svc.Remove(ctx, "adm", ab2bus.RemoveBridgeInput{Channel: "job1-chat"}); err != nil {
				t.Fatalf("Remove: %v", err)
			}
			f.want(f.synth("U1", "after removal"), "ignored")
			if len(f.human()) != 0 {
				t.Error("an event for a removed bridge reached the bus")
			}
			if f.boundTo(slAlice) != "" {
				t.Error("a binding was written")
			}
			if _, ok := f.bridgeRow(f.ch); ok {
				t.Error("the bridge row came back")
			}
		})

		t.Run("archived-during-send", func(t *testing.T) {
			f := newInFix(t, e)
			orig := f.h.core.Store
			f.h.core.Store = &archiveRaceStore{Store: orig, bridgeID: f.bridge().ID}
			defer func() { f.h.core.Store = orig }()
			m := f.fake.Say(f.sl, "U1", "racing the archive", "")
			f.want(m, "ignored")
			f.h.core.Store = orig
			if len(f.human()) != 0 || f.boundTo(slAlice) != "" {
				t.Error("a send that lost the race with the archive left rows")
			}
			if _, ok := f.mapRow(m.TS); ok || f.dropped(m.TS) {
				t.Error("rows written for the raced message")
			}
		})

		t.Run("drop-is-final", func(t *testing.T) {
			f := newInFix(t, e)
			first := f.fake.Say(f.sl, "U4", "denied", "")
			f.want(first, "dropped")
			f.h.tx(func(ctx context.Context, tx ab2store.Tx) error {
				acl, err := tx.ACL(ctx, f.ch.ID)
				if err != nil {
					return err
				}
				return tx.SetACL(ctx, f.ch.ID, append(acl, slEntry(siBob, ab2store.PermRead, ab2store.PermWrite)))
			})
			if err := f.svc.CatchUp(ctx); err != nil {
				t.Fatalf("CatchUp: %v", err)
			}
			if len(f.human()) != 0 || f.droppedLogs() != 1 {
				t.Errorf("an old drop was delivered or logged again: %d messages, %d drop logs", len(f.human()), f.droppedLogs())
			}
			f.want(f.fake.Say(f.sl, "U4", "now allowed", ""), "sent")
			got := f.human()
			if len(got) != 1 || got[0].Content != "now allowed" {
				t.Errorf("messages = %+v", got)
			}
		})

		t.Run("rate-limited-retried", func(t *testing.T) {
			cfg := core0()
			f := newInFixOpts(t, e, harnessOptions{cfg: &cfg}, nil)
			f.want(f.fake.Say(f.sl, "U1", "one", ""), "sent")
			m2 := f.fake.Say(f.sl, "U1", "two", "")
			f.want(m2, "retry")
			if len(f.human()) != 1 {
				t.Fatalf("%d messages after the limit", len(f.human()))
			}
			f.h.clock.Advance(2 * time.Second)
			f.want(m2, "sent")
			f.want(m2, "duplicate")
			got := f.human()
			if len(got) != 2 || got[0].Content != "one" || got[1].Content != "two" {
				t.Errorf("messages = %+v", got)
			}
		})

		t.Run("rate-limited-requeued", func(t *testing.T) {
			cfg := core0()
			f := newInFixOpts(t, e, harnessOptions{cfg: &cfg}, func(c *slackbridge.Config) { c.RetryBase = 5 * time.Millisecond })
			f.svc.HandleEvent(ctx, f.event(f.fake.Say(f.sl, "U1", "one", "")))
			f.svc.HandleEvent(ctx, f.event(f.fake.Say(f.sl, "U1", "two", "")))
			f.h.clock.Advance(2 * time.Second)
			deadline := time.Now().Add(5 * time.Second)
			for len(f.human()) < 2 && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			got := f.human()
			if len(got) != 2 || got[1].Content != "two" {
				t.Fatalf("messages = %+v", got)
			}
		})
	})
}

func TestSlackBridgeCatchUp(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		ctx := context.Background()

		t.Run("missed-events", func(t *testing.T) {
			f := newInFix(t, e)
			s1, root := f.agentPost("old root", 0, "")
			f.fake.DropNext(3)
			a := f.fake.Say(f.sl, "U1", "first", "")
			b := f.fake.Say(f.sl, "U1", "reply to the old root", root.TS)
			c := f.fake.Say(f.sl, "U1", "third", "")
			if len(f.human()) != 0 {
				t.Fatal("a dropped event reached the bus")
			}
			if err := f.svc.CatchUp(ctx); err != nil {
				t.Fatalf("CatchUp: %v", err)
			}
			got := f.human()
			if len(got) != 3 || got[0].Content != "first" || got[1].Content != "reply to the old root" || got[2].Content != "third" {
				t.Fatalf("messages = %+v", got)
			}
			if got[1].ReplyTo == nil || *got[1].ReplyTo != s1 {
				t.Errorf("thread reply = %+v, want reply_to %d", got[1], s1)
			}
			for _, m := range []slackapi.Message{a, b, c} {
				if _, ok := f.mapRow(m.TS); !ok {
					t.Errorf("ts %s is not mapped", m.TS)
				}
			}
			if cur := f.bridge().CursorTS; cur != c.TS {
				t.Errorf("cursor = %q, want %q", cur, c.TS)
			}
			// A second pass finds nothing.
			calls := f.fake.Calls("History")
			if err := f.svc.CatchUp(ctx); err != nil {
				t.Fatalf("second CatchUp: %v", err)
			}
			if f.fake.Calls("History") <= calls {
				t.Error("the second pass did not read history")
			}
			if len(f.human()) != 3 || f.bridge().CursorTS != c.TS {
				t.Errorf("the second pass changed state: %d messages, cursor %q", len(f.human()), f.bridge().CursorTS)
			}
			f.noSecretsLogged(t)
		})

		t.Run("crash-repair", func(t *testing.T) {
			f := newInFix(t, e)
			// A bus message the worker posted, then crashed before mapping it.
			seq := f.h.append(f.ch, ab2store.SlackBridge, "", "posted before the crash")
			post := f.fake.SayAsBot(f.sl, "BBOT", "posted before the crash", "", seq)
			if err := f.svc.CatchUp(ctx); err != nil {
				t.Fatalf("CatchUp: %v", err)
			}
			row, ok := f.mapRow(post.TS)
			if !ok || row.Seq != seq || row.RootTS != post.TS {
				t.Fatalf("map row = %+v, %v", row, ok)
			}
			var acked int64
			f.h.tx(func(ctx context.Context, tx ab2store.Tx) error {
				subs, err := tx.Subscriptions(ctx, ab2store.SlackBridge)
				for _, s := range subs {
					if s.ChannelID == f.ch.ID {
						acked = s.AckedSeq
					}
				}
				return err
			})
			if acked != seq {
				t.Errorf("acked mark = %d, want %d", acked, seq)
			}
			// An own post naming a seq that is not in this channel is not mapped.
			other := f.h.channel("elsewhere", ab2store.KindOrdinary, "job1-coder", nil)
			foreign := f.h.append(other, "job1-coder", "", "not bridged")
			bad := f.fake.SayAsBot(f.sl, "BBOT", "forged", "", foreign)
			if err := f.svc.CatchUp(ctx); err != nil {
				t.Fatalf("CatchUp: %v", err)
			}
			if _, ok := f.mapRow(bad.TS); ok {
				t.Error("an own post naming another channel's seq was mapped")
			}
		})

		t.Run("unauthorized-dropped-once", func(t *testing.T) {
			f := newInFix(t, e)
			g := f.fake.Say(f.sl, "U2", "guest words", "")
			ok := f.fake.Say(f.sl, "U1", "fine", "")
			for range 3 {
				if err := f.svc.CatchUp(ctx); err != nil {
					t.Fatalf("CatchUp: %v", err)
				}
			}
			if !f.dropped(g.TS) || f.droppedLogs() != 1 {
				t.Errorf("dropped = %v, drop logs = %d", f.dropped(g.TS), f.droppedLogs())
			}
			got := f.human()
			if len(got) != 1 || got[0].Content != "fine" {
				t.Errorf("messages = %+v", got)
			}
			if f.bridge().CursorTS != ok.TS {
				t.Errorf("cursor = %q, want %q", f.bridge().CursorTS, ok.TS)
			}
		})

		t.Run("retry-holds-the-cursor", func(t *testing.T) {
			cfg := core0()
			f := newInFixOpts(t, e, harnessOptions{cfg: &cfg}, nil)
			a := f.fake.Say(f.sl, "U1", "one", "")
			b := f.fake.Say(f.sl, "U1", "two", "")
			c := f.fake.Say(f.sl, "U11", "three", "")
			if err := f.svc.CatchUp(ctx); err != nil {
				t.Fatalf("CatchUp: %v", err)
			}
			// one and three are sent; two hit the limit.
			if cur := f.bridge().CursorTS; cur < a.TS || cur >= b.TS {
				t.Fatalf("cursor = %q, want in [%s, %s)", cur, a.TS, b.TS)
			}
			if _, ok := f.mapRow(b.TS); ok {
				t.Fatal("the limited message was mapped")
			}
			f.h.clock.Advance(2 * time.Second)
			if err := f.svc.CatchUp(ctx); err != nil {
				t.Fatalf("second CatchUp: %v", err)
			}
			got := f.human()
			contents := make([]string, len(got))
			for i, m := range got {
				contents[i] = m.Content
			}
			slices.Sort(contents)
			if !slices.Equal(contents, []string{"one", "three", "two"}) {
				t.Errorf("contents = %v, each must arrive once", contents)
			}
			if f.bridge().CursorTS != c.TS {
				t.Errorf("cursor = %q, want %q", f.bridge().CursorTS, c.TS)
			}
		})

		t.Run("slack-failure-keeps-the-cursor", func(t *testing.T) {
			f := newInFix(t, e)
			f.fake.Say(f.sl, "U1", "waiting", "")
			f.fake.FailNext("History", errors.New("connection reset"))
			if err := f.svc.CatchUp(ctx); err == nil {
				t.Fatal("CatchUp hid a Slack failure")
			}
			if f.bridge().CursorTS != "" || len(f.human()) != 0 {
				t.Error("a failed pass moved the cursor or delivered")
			}
			if err := f.svc.CatchUp(ctx); err != nil {
				t.Fatalf("CatchUp: %v", err)
			}
			if len(f.human()) != 1 {
				t.Errorf("%d messages after the retry, want 1", len(f.human()))
			}
		})

		t.Run("root-cap", func(t *testing.T) {
			f := newInFix(t, e)
			for i := 1; i <= 250; i++ {
				m := f.fake.SayAsBot(f.sl, "BBOT", "root "+strconv.Itoa(i), "", int64(1000+i))
				f.h.tx(func(ctx context.Context, tx ab2store.Tx) error {
					return tx.PutBridgeMessage(ctx, ab2store.BridgeMessage{BridgeID: f.bridge().ID, Seq: int64(1000 + i), SlackTS: m.TS, RootTS: m.TS, CreatedAt: 1})
				})
			}
			if err := f.svc.CatchUp(ctx); err != nil {
				t.Fatalf("CatchUp: %v", err)
			}
			if n := f.fake.Calls("Replies"); n != 200 {
				t.Errorf("Replies calls = %d, want 200", n)
			}
			if !strings.Contains(f.logs.String(), "skipped thread roots") {
				t.Error("the skipped roots were not logged")
			}
		})
	})
}
