package bus

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core/identities"
	"github.com/ericfitz/agentbus2/internal/dbtest"
	"github.com/ericfitz/agentbus2/internal/slackapi"
	"github.com/ericfitz/agentbus2/internal/slackapi/slackfake"
	"github.com/ericfitz/agentbus2/internal/slackbridge"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
)

const (
	reasonGuest = "Slack guest or external user"
	slBot       = "UBOT"
	slTeam      = "T1"
	slAlice     = "alice@corp.example"
	slGina      = "gina@corp.example"
	slXav       = "xavier@corp.example"
	slNone      = "nobody@corp.example"
)

// slackFix is a harness with the Slack fake, the bridge Service over it, a
// scope job1 with an agent and the humans of the bridge tests.
type logBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

type slackFix struct {
	logs *logBuf
	h    *harness
	fake *slackfake.Fake
	svc  *slackbridge.Service
	adm  string
}

func slackUsers(f *slackfake.Fake) {
	f.AddUser(slackapi.User{ID: "U1", TeamID: slTeam, Email: slAlice, DisplayName: "alice"})
	f.AddUser(slackapi.User{ID: "U2", TeamID: slTeam, Email: slGina, DisplayName: "gina", Restricted: true})
	f.AddUser(slackapi.User{ID: "U3", TeamID: "T9", Email: slXav, DisplayName: "xavier", Stranger: true})
}

func newSlackFix(t *testing.T, e dbtest.Engine) *slackFix {
	t.Helper()
	return newSlackFixOpts(t, e, harnessOptions{}, nil)
}

// newSlackFixOpts is newSlackFix with harness options and a bridge Config
// modifier (nil: none).
func newSlackFixOpts(t *testing.T, e dbtest.Engine, o harnessOptions, mod func(*slackbridge.Config)) *slackFix {
	t.Helper()
	h := newHarness(t, e, o)
	f := slackfake.New(slackapi.Self{TeamID: slTeam, UserID: slBot, BotID: "BBOT"})
	slackUsers(f)
	h.admin("adm")
	h.rtScope("job1")
	h.scopedIdentity("job1-coder", "job1")
	for _, em := range []string{slAlice, slGina, slXav, slNone} {
		if _, _, err := (identities.Service{Core: h.core}).CreateIdentityByEmail(context.Background(), em); err != nil {
			t.Fatalf("CreateIdentityByEmail %s: %v", em, err)
		}
	}
	logs := &logBuf{}
	cfg := slackbridge.Config{
		ArchiveAfterDone: time.Hour, MaxMessageChars: 3000, LockRenew: 10 * time.Millisecond,
		Logger: slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
	if mod != nil {
		mod(&cfg)
	}
	svc, err := slackbridge.New(context.Background(), h.core, f, cfg)
	if err != nil {
		t.Fatalf("slackbridge.New: %v", err)
	}
	return &slackFix{h: h, fake: f, svc: svc, adm: "adm", logs: logs}
}

// noSecretsLogged fails when the captured log holds a token, the notice text
// or the final line.
func (x *slackFix) noSecretsLogged(t *testing.T) {
	t.Helper()
	log := x.logs.String()
	for _, bad := range []string{"xoxb", "xapp", "Not invited", "This channel is archived", slGina, slAlice, slXav} {
		if strings.Contains(log, bad) {
			t.Errorf("the log contains %q:\n%s", bad, log)
		}
	}
}

func slEntry(id string, ps ...ab2store.Permission) ab2store.ACLEntry {
	return ab2store.ACLEntry{Identity: id, Permissions: ps}
}

// slChannel creates an ordinary channel in scope with acl (nil: the standard
// bridge ACL).
func (x *slackFix) slChannel(name, scope string, acl []ab2store.ACLEntry) ab2store.Channel {
	x.h.t.Helper()
	R, W, A := ab2store.PermRead, ab2store.PermWrite, ab2store.PermAdmin
	if acl == nil {
		acl = []ab2store.ACLEntry{
			slEntry("job1-coder", R, W, A), slEntry(slAlice, R, W), slEntry(slGina, R, W),
			slEntry(slXav, R, W), slEntry(slNone, R, W), slEntry(ab2store.Admins, A),
		}
	}
	var ch ab2store.Channel
	x.h.tx(func(ctx context.Context, tx ab2store.Tx) error {
		seq, err := tx.MaxSeq(ctx)
		if err != nil {
			return err
		}
		ch, err = tx.CreateChannel(ctx, ab2store.ChannelCreate{
			Name: name, Kind: ab2store.KindOrdinary, Creator: "job1-coder", Scope: scope, ACL: acl, CreatedSeq: seq, CreatedAt: x.h.core.NowMs(),
		})
		return err
	})
	return ch
}

func (x *slackFix) create(channel, slackName string) (ab2bus.BridgeCreated, error) {
	return x.svc.Create(context.Background(), x.adm, ab2bus.CreateBridgeInput{Channel: channel, SlackName: slackName})
}

// bridgeRow reads the bridge of ch, found false when it has none.
func (x *slackFix) bridgeRow(ch ab2store.Channel) (b ab2store.Bridge, found bool) {
	x.h.t.Helper()
	err := x.h.raw.Read(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
		var err error
		b, err = tx.BridgeByChannel(ctx, ch.ID)
		return err
	})
	if errors.Is(err, ab2store.ErrNotFound) {
		return b, false
	}
	if err != nil {
		x.h.t.Fatalf("BridgeByChannel: %v", err)
	}
	return b, true
}

func (x *slackFix) acl(ch ab2store.Channel) []ab2store.ACLEntry {
	x.h.t.Helper()
	var acl []ab2store.ACLEntry
	x.h.tx(func(ctx context.Context, tx ab2store.Tx) error {
		var err error
		acl, err = tx.ACL(ctx, ch.ID)
		return err
	})
	return acl
}

func (x *slackFix) bridgeSubscribed(ch ab2store.Channel) bool {
	x.h.t.Helper()
	var subs []ab2store.Subscription
	x.h.tx(func(ctx context.Context, tx ab2store.Tx) error {
		var err error
		subs, err = tx.Subscriptions(ctx, ab2store.SlackBridge)
		return err
	})
	return slices.ContainsFunc(subs, func(s ab2store.Subscription) bool { return s.ChannelID == ch.ID })
}

func (x *slackFix) messages(ch ab2store.Channel) []ab2store.Message {
	x.h.t.Helper()
	var ms []ab2store.Message
	x.h.tx(func(ctx context.Context, tx ab2store.Tx) error {
		var err error
		ms, err = tx.MessagesAfter(ctx, ch.ID, 0, ab2store.MessageFilter{}, 100)
		return err
	})
	return ms
}

func hasACL(acl []ab2store.ACLEntry, id string, p ab2store.Permission) bool {
	return slices.ContainsFunc(acl, func(e ab2store.ACLEntry) bool { return e.Identity == id && slices.Contains(e.Permissions, p) })
}

func TestSlackBridgeCreate(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		x := newSlackFix(t, e)
		ch := x.slChannel("job1-chat", "job1", nil)

		got, err := x.create("job1-chat", "")
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		b := got.Bridge
		if b.State != ab2bus.BridgeLive || b.Channel != "job1-chat" || b.Scope != "job1" || b.SlackName != "job1-chat" || b.SlackChannelID == "" {
			t.Fatalf("bridge = %+v", b)
		}
		fc, ok := x.fake.Channel(b.SlackChannelID)
		if !ok || !fc.Private || fc.Name != "job1-chat" {
			t.Fatalf("slack channel = %+v, %v", fc, ok)
		}
		wantMembers := []string{slBot, "U1"}
		if !slices.Equal(slices.Sorted(slices.Values(fc.Members)), slices.Sorted(slices.Values(wantMembers))) {
			t.Errorf("members = %v, want %v", fc.Members, wantMembers)
		}
		reasons := map[string]string{}
		for _, n := range got.NotInvited {
			reasons[n.Identity] = n.Reason
		}
		want := map[string]string{slGina: "Slack guest or external user", slXav: "Slack guest or external user", slNone: "not found in Slack"}
		if len(reasons) != len(want) {
			t.Fatalf("NotInvited = %+v", got.NotInvited)
		}
		for id, r := range want {
			if reasons[id] != r {
				t.Errorf("NotInvited[%s] = %q, want %q", id, reasons[id], r)
			}
		}
		if row, _ := x.bridgeRow(ch); row.State != ab2store.BridgeLive || row.SlackChannelID != b.SlackChannelID {
			t.Errorf("stored bridge = %+v", row)
		}
		if !hasACL(x.acl(ch), ab2store.SlackBridge, ab2store.PermRead) {
			t.Errorf("ACL lacks slack-bridge read: %+v", x.acl(ch))
		}
		if !x.bridgeSubscribed(ch) {
			t.Error("slack-bridge is not subscribed")
		}

		// The notice, on the bus and in Slack, names the identities.
		var notice *ab2store.Message
		for _, m := range x.messages(ch) {
			if m.Sender == ab2store.SlackBridge && m.Type == "notice" {
				notice = &m
			}
		}
		if notice == nil || notice.SenderKind != ab2store.IdentityAgent {
			t.Fatalf("no notice from slack-bridge: %+v", x.messages(ch))
		}
		var slackText string
		for _, p := range fc.Posts {
			slackText += p.Section + "\n"
		}
		for _, id := range []string{slGina, slXav, slNone} {
			if !strings.Contains(notice.Content, id) || !strings.Contains(slackText, id) {
				t.Errorf("notice does not name %s: bus %q slack %q", id, notice.Content, slackText)
			}
		}
		if strings.Contains(notice.Content, slAlice) || strings.Contains(slackText, slAlice) {
			t.Errorf("notice names an invited human: %q", notice.Content)
		}

		// A second Create is a conflict.
		_, err = x.create("job1-chat", "")
		codeOf(t, err, ab2bus.CodeConflict)
		if m := msgOf(err); m != `channel "job1-chat" is already bridged` {
			t.Errorf("second create message = %q", m)
		}
		if x.fake.Calls("CreatePrivateChannel") != 1 {
			t.Errorf("CreatePrivateChannel calls = %d", x.fake.Calls("CreatePrivateChannel"))
		}

		// List and Get map the row.
		l, err := x.svc.List(context.Background(), "adm")
		if err != nil || len(l) != 1 || l[0] != b {
			t.Errorf("List = %+v, %v", l, err)
		}
		g, err := x.svc.Get(context.Background(), "adm", "job1-chat")
		if err != nil || g != b {
			t.Errorf("Get = %+v, %v", g, err)
		}
		_, err = x.svc.Get(context.Background(), "adm", "nope")
		codeOf(t, err, ab2bus.CodeNotFound)
		if m := msgOf(err); m != `channel "nope" has no bridge` {
			t.Errorf("Get message = %q", m)
		}
		for name, call := range map[string]func() error{
			"List": func() error { _, err := x.svc.List(context.Background(), "job1-coder"); return err },
			"Get":  func() error { _, err := x.svc.Get(context.Background(), "job1-coder", "job1-chat"); return err },
			"Remove": func() error {
				return x.svc.Remove(context.Background(), "job1-coder", ab2bus.RemoveBridgeInput{Channel: "job1-chat"})
			},
		} {
			if err := call(); ab2bus.CodeOf(err) != ab2bus.CodeForbidden {
				t.Errorf("%s by a non-admin = %v", name, err)
			}
		}
		if !strings.Contains(x.logs.String(), "created") {
			t.Error("the create logged nothing; the log assertion below would be vacuous")
		}
		x.noSecretsLogged(t)
	})
}

// TestSlackBridgeDecision18: each reason alone keeps a user out of Slack.
func TestSlackBridgeDecision18(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		x := newSlackFix(t, e)
		R, W := ab2store.PermRead, ab2store.PermWrite
		cases := []struct {
			id, email string
			user      slackapi.User
			reason    string
		}{
			{"noteam@corp.example", "noteam@corp.example", slackapi.User{ID: "UG", Email: "noteam@corp.example"}, reasonGuest},
			{"otherteam@corp.example", "otherteam@corp.example", slackapi.User{ID: "UA", TeamID: "T9", Email: "otherteam@corp.example"}, reasonGuest},
			{"ultra@corp.example", "ultra@corp.example", slackapi.User{ID: "UB", TeamID: slTeam, Email: "ultra@corp.example", UltraRestricted: true}, reasonGuest},
			{"restricted@corp.example", "restricted@corp.example", slackapi.User{ID: "UC", TeamID: slTeam, Email: "restricted@corp.example", Restricted: true}, reasonGuest},
			{"stranger@corp.example", "stranger@corp.example", slackapi.User{ID: "UD", TeamID: slTeam, Email: "stranger@corp.example", Stranger: true}, reasonGuest},
			{"deleted@corp.example", "deleted@corp.example", slackapi.User{ID: "UE", TeamID: slTeam, Email: "deleted@corp.example", Deleted: true}, "not found in Slack"},
			{"bot@corp.example", "bot@corp.example", slackapi.User{ID: "UF", TeamID: slTeam, Email: "bot@corp.example", IsBot: true}, "not found in Slack"},
		}
		acl := []ab2store.ACLEntry{slEntry("job1-coder", R, W), slEntry(slAlice, R, W)}
		for _, c := range cases {
			x.fake.AddUser(c.user)
			if _, _, err := (identities.Service{Core: x.h.core}).CreateIdentityByEmail(context.Background(), c.email); err != nil {
				t.Fatal(err)
			}
			acl = append(acl, slEntry(c.id, R, W))
		}
		x.slChannel("job1-chat", "job1", acl)
		got, err := x.create("job1-chat", "")
		if err != nil {
			t.Fatal(err)
		}
		reasons := map[string]string{}
		for _, n := range got.NotInvited {
			reasons[n.Identity] = n.Reason
		}
		fc, _ := x.fake.Channel(got.Bridge.SlackChannelID)
		for _, c := range cases {
			if reasons[c.id] != c.reason {
				t.Errorf("%s: reason %q, want %q", c.id, reasons[c.id], c.reason)
			}
			if slices.Contains(fc.Members, c.user.ID) {
				t.Errorf("%s was invited", c.id)
			}
		}
		if !slices.Contains(fc.Members, "U1") {
			t.Error("the full member was not invited")
		}
	})
}

func TestSlackBridgeCreateRefusals(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		x := newSlackFix(t, e)
		R, A := ab2store.PermRead, ab2store.PermAdmin
		x.slChannel("job1-chat", "job1", nil)

		_, err := x.svc.Create(context.Background(), "job1-coder", ab2bus.CreateBridgeInput{Channel: "job1-chat"})
		codeOf(t, err, ab2bus.CodeForbidden)

		_, err = x.create("missing", "")
		codeOf(t, err, ab2bus.CodeNotFound)

		x.h.channel("plain", ab2store.KindOrdinary, "job1-coder", nil)
		_, err = x.create("plain", "")
		codeOf(t, err, ab2bus.CodeValidation)
		if m := msgOf(err); m != `channel "plain" has no scope; a bridge requires a scoped channel` {
			t.Errorf("unscoped message = %q", m)
		}

		x.h.channel("mem", ab2store.KindMemory, "job1-coder", nil)
		_, err = x.create("mem", "")
		codeOf(t, err, ab2bus.CodeValidation)
		if m := msgOf(err); m != "a bridge needs an ordinary channel" {
			t.Errorf("memory message = %q", m)
		}

		x.slChannel("open-to-all", "job1", []ab2store.ACLEntry{slEntry("job1-coder", R, A), slEntry(ab2store.Everyone, R)})
		_, err = x.create("open-to-all", "")
		codeOf(t, err, ab2bus.CodeForbidden)

		for _, bad := range []string{"Bad Name", "has.dot", strings.Repeat("a", 81)} {
			_, err = x.create("job1-chat", bad)
			codeOf(t, err, ab2bus.CodeValidation)
		}

		// A name taken by a channel the bridge did not create: conflict, and
		// no row is left behind.
		if _, err := x.fake.CreatePrivateChannel(context.Background(), "taken-name"); err != nil {
			t.Fatal(err)
		}
		_, err = x.create("job1-chat", "taken-name")
		codeOf(t, err, ab2bus.CodeConflict)
		if m := msgOf(err); m != `Slack channel name "taken-name" is taken` {
			t.Errorf("taken message = %q", m)
		}
		ch := x.h.rtChannelByName("job1-chat")
		if _, found := x.bridgeRow(ch); found {
			t.Error("a bridge row was left after the name conflict")
		}

		// A done scope accepts no new bridge, and no refusal reaches Slack.
		creates := x.fake.Calls("CreatePrivateChannel")
		x.h.rtScope("job2")
		x.slChannel("job2-chat", "job2", []ab2store.ACLEntry{slEntry("job1-coder", R, A)})
		x.h.rtDone("job2", ab2bus.RetireNormal)
		_, err = x.create("job2-chat", "")
		codeOf(t, err, ab2bus.CodeConflict)
		if n := x.fake.Calls("CreatePrivateChannel"); n != creates {
			t.Errorf("the done-scope refusal reached Slack: %d create calls, was %d", n, creates)
		}
	})
}

func TestSlackBridgeCreateResume(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		t.Run("invite failure then retry", func(t *testing.T) {
			x := newSlackFix(t, e)
			ch := x.slChannel("job1-chat", "job1", nil)
			x.fake.FailNext("Invite", errors.New("connection reset"))
			_, err := x.create("job1-chat", "")
			codeOf(t, err, ab2bus.CodeUnavailable)
			row, _ := x.bridgeRow(ch)
			if row.State != ab2store.BridgeCreating || row.SlackChannelID == "" {
				t.Fatalf("after failure: %+v", row)
			}
			if hasACL(x.acl(ch), ab2store.SlackBridge, ab2store.PermRead) {
				t.Error("the ACL changed before the bridge was live")
			}
			// A different name while creating names the stored one.
			_, err = x.create("job1-chat", "other-name")
			codeOf(t, err, ab2bus.CodeConflict)
			if !strings.Contains(msgOf(err), `"job1-chat"`) {
				t.Errorf("resume conflict does not name the stored name: %q", msgOf(err))
			}
			got, err := x.create("job1-chat", "")
			if err != nil {
				t.Fatalf("retry: %v", err)
			}
			if got.Bridge.State != ab2bus.BridgeLive || got.Bridge.SlackChannelID != row.SlackChannelID {
				t.Errorf("retry bridge = %+v", got.Bridge)
			}
			if n := x.fake.Calls("CreatePrivateChannel"); n != 1 {
				t.Errorf("CreatePrivateChannel calls = %d, want 1", n)
			}
		})
		t.Run("rate limit during invite then retry", func(t *testing.T) {
			x := newSlackFix(t, e)
			ch := x.slChannel("job1-chat", "job1", nil)
			x.fake.RateLimit("Invite", 1, 5*time.Second)
			_, err := x.create("job1-chat", "")
			codeOf(t, err, ab2bus.CodeRateLimited)
			if row, _ := x.bridgeRow(ch); row.State != ab2store.BridgeCreating {
				t.Fatalf("after rate limit: %+v", row)
			}
			got, err := x.create("job1-chat", "")
			if err != nil || got.Bridge.State != ab2bus.BridgeLive {
				t.Fatalf("retry = %+v, %v", got, err)
			}
			fc, _ := x.fake.Channel(got.Bridge.SlackChannelID)
			if !slices.Contains(fc.Members, "U1") || x.fake.Calls("CreatePrivateChannel") != 1 {
				t.Errorf("members %v, create calls %d", fc.Members, x.fake.Calls("CreatePrivateChannel"))
			}
		})
		t.Run("crash after step 1 resumes with the stored name", func(t *testing.T) {
			x := newSlackFix(t, e)
			ch := x.slChannel("job1-chat", "job1", nil)
			x.h.tx(func(ctx context.Context, tx ab2store.Tx) error {
				_, err := tx.CreateBridge(ctx, ab2store.Bridge{ChannelID: ch.ID, ChannelName: ch.Name, Scope: "job1", SlackName: "stored-name", CreatedAt: 1})
				return err
			})
			got, err := x.create("job1-chat", "")
			if err != nil || got.Bridge.SlackName != "stored-name" || got.Bridge.State != ab2bus.BridgeLive {
				t.Fatalf("resume = %+v, %v", got, err)
			}
			if fc, _ := x.fake.Channel(got.Bridge.SlackChannelID); fc.Name != "stored-name" {
				t.Errorf("slack channel name = %q", fc.Name)
			}
		})
		t.Run("crash after the Slack call adopts the channel", func(t *testing.T) {
			x := newSlackFix(t, e)
			ch := x.slChannel("job1-chat", "job1", nil)
			x.h.tx(func(ctx context.Context, tx ab2store.Tx) error {
				_, err := tx.CreateBridge(ctx, ab2store.Bridge{ChannelID: ch.ID, ChannelName: ch.Name, Scope: "job1", SlackName: "job1-chat", CreatedAt: 1})
				return err
			})
			orphan, err := x.fake.CreatePrivateChannel(context.Background(), "job1-chat")
			if err != nil {
				t.Fatal(err)
			}
			got, err := x.create("job1-chat", "")
			if err != nil {
				t.Fatalf("resume: %v", err)
			}
			if got.Bridge.SlackChannelID != orphan || got.Bridge.State != ab2bus.BridgeLive {
				t.Errorf("bridge = %+v, want channel %s", got.Bridge, orphan)
			}
			if x.fake.Calls("FindPrivateChannel") != 1 {
				t.Errorf("FindPrivateChannel calls = %d", x.fake.Calls("FindPrivateChannel"))
			}
		})
		t.Run("a resumed name held by an archived channel is a conflict and keeps the row", func(t *testing.T) {
			x := newSlackFix(t, e)
			ch := x.slChannel("job1-chat", "job1", nil)
			x.h.tx(func(ctx context.Context, tx ab2store.Tx) error {
				_, err := tx.CreateBridge(ctx, ab2store.Bridge{ChannelID: ch.ID, ChannelName: ch.Name, Scope: "job1", SlackName: "job1-chat", CreatedAt: 1})
				return err
			})
			old, _ := x.fake.CreatePrivateChannel(context.Background(), "job1-chat")
			x.fake.ArchiveByHand(old)
			_, err := x.create("job1-chat", "")
			codeOf(t, err, ab2bus.CodeConflict)
			if row, found := x.bridgeRow(ch); !found || row.State != ab2store.BridgeCreating {
				t.Errorf("a row this call did not insert was touched: %+v, %v", row, found)
			}
			// bridges remove clears the stuck row; a new name then works.
			if err := x.svc.Remove(context.Background(), "adm", ab2bus.RemoveBridgeInput{Channel: "job1-chat"}); err != nil {
				t.Fatalf("Remove: %v", err)
			}
			if got, err := x.create("job1-chat", "job1-chat-2"); err != nil || got.Bridge.State != ab2bus.BridgeLive {
				t.Errorf("create with a new name = %+v, %v", got, err)
			}
		})
		t.Run("a held create lock answers retryable unavailable", func(t *testing.T) {
			x := newSlackFix(t, e)
			ch := x.slChannel("job1-chat", "job1", nil)
			ctx := context.Background()
			lock := fmt.Sprintf("bridge-create:%d", ch.ID)
			now := x.h.core.Now()
			held, err := x.h.raw.TryLock(ctx, lock, "other-replica", now.UnixMilli(), now.Add(time.Hour).UnixMilli())
			if err != nil || !held {
				t.Fatalf("pre-hold TryLock = %v, %v", held, err)
			}
			_, err = x.create("job1-chat", "")
			codeOf(t, err, ab2bus.CodeUnavailable)
			if m := msgOf(err); m != "another create of this bridge is in progress; retry" {
				t.Errorf("message = %q", m)
			}
			if be := new(ab2bus.Error); !errors.As(err, &be) || !be.Retryable {
				t.Errorf("not retryable: %v", err)
			}
			if _, found := x.bridgeRow(ch); found {
				t.Error("a refused create left a bridge row")
			}
			if n := x.fake.Calls("CreatePrivateChannel"); n != 0 {
				t.Errorf("CreatePrivateChannel calls = %d, want 0", n)
			}
			if err := x.h.raw.Unlock(ctx, lock, "other-replica"); err != nil {
				t.Fatal(err)
			}
			if got, err := x.create("job1-chat", ""); err != nil || got.Bridge.State != ab2bus.BridgeLive {
				t.Errorf("create after the lock was released = %+v, %v", got, err)
			}
		})
		t.Run("two replicas creating concurrently", func(t *testing.T) {
			x := newSlackFix(t, e)
			sib := x.h.sibling(harnessOptions{})
			svc2, err := slackbridge.New(context.Background(), sib.core, x.fake, slackbridge.Config{ArchiveAfterDone: time.Hour, MaxMessageChars: 3000})
			if err != nil {
				t.Fatal(err)
			}
			ch := x.slChannel("job1-chat", "job1", nil)
			var wg sync.WaitGroup
			svcs := []*slackbridge.Service{x.svc, svc2}
			res := make([]ab2bus.BridgeCreated, 2)
			errs := make([]error, 2)
			for i := range 2 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					res[i], errs[i] = svcs[i].Create(context.Background(), "adm", ab2bus.CreateBridgeInput{Channel: "job1-chat"})
				}()
			}
			wg.Wait()
			for i := range 2 {
				switch {
				case errs[i] == nil:
				case ab2bus.CodeOf(errs[i]) == ab2bus.CodeUnavailable && msgOf(errs[i]) == "another create of this bridge is in progress; retry":
					// Retryable: the retry must succeed.
					if got, err := svcs[i].Create(context.Background(), "adm", ab2bus.CreateBridgeInput{Channel: "job1-chat"}); err != nil && (ab2bus.CodeOf(err) != ab2bus.CodeConflict || msgOf(err) != `channel "job1-chat" is already bridged`) {
						t.Fatalf("retry %d = %+v, %v", i, got, err)
					}
				case ab2bus.CodeOf(errs[i]) == ab2bus.CodeConflict && msgOf(errs[i]) == `channel "job1-chat" is already bridged`:
				default:
					t.Fatalf("create %d = %+v, %v", i, res[i], errs[i])
				}
			}
			row, found := x.bridgeRow(ch)
			if !found || row.State != ab2store.BridgeLive || row.SlackChannelID == "" {
				t.Fatalf("row = %+v, %v", row, found)
			}
			if n := x.fake.Calls("CreatePrivateChannel"); n != 1 {
				t.Errorf("CreatePrivateChannel calls = %d, want exactly one Slack channel", n)
			}
			fc, _ := x.fake.Channel(row.SlackChannelID)
			notices := 0
			for _, p := range fc.Posts {
				if strings.Contains(p.Section, "Not invited") {
					notices++
				}
			}
			if notices != 1 {
				t.Errorf("not-invited notices in Slack = %d, want 1", notices)
			}
		})
		t.Run("overlapping creates end live with one Slack channel", func(t *testing.T) {
			x := newSlackFix(t, e)
			ch := x.slChannel("job1-chat", "job1", nil)
			var wg sync.WaitGroup
			res := make([]ab2bus.BridgeCreated, 2)
			errs := make([]error, 2)
			for i := range 2 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					res[i], errs[i] = x.create("job1-chat", "")
				}()
			}
			wg.Wait()
			// The loser either resumed the shared row (live) or arrived after
			// the winner went live (already bridged); never anything else.
			ok := 0
			for i := range 2 {
				switch {
				case errs[i] == nil && res[i].Bridge.State == ab2bus.BridgeLive:
					ok++
				case ab2bus.CodeOf(errs[i]) == ab2bus.CodeConflict && msgOf(errs[i]) == `channel "job1-chat" is already bridged`:
				default:
					t.Fatalf("create %d = %+v, %v", i, res[i], errs[i])
				}
			}
			if ok == 0 {
				t.Fatal("no create succeeded")
			}
			if x.fake.Calls("CreatePrivateChannel") != 1 {
				t.Errorf("create calls %d, want 1", x.fake.Calls("CreatePrivateChannel"))
			}
			if row, _ := x.bridgeRow(ch); row.State != ab2store.BridgeLive {
				t.Errorf("row = %+v", row)
			}
		})
	})
}

func TestSlackBridgeRemoveAndArchive(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		x := newSlackFix(t, e)
		ctx := context.Background()
		ch := x.slChannel("job1-chat", "job1", nil)
		got, err := x.create("job1-chat", "")
		if err != nil {
			t.Fatal(err)
		}
		sid := got.Bridge.SlackChannelID
		// A map row and a drop row, to see them go with the bridge.
		row, _ := x.bridgeRow(ch)
		x.h.tx(func(ctx context.Context, tx ab2store.Tx) error {
			if err := tx.PutBridgeMessage(ctx, ab2store.BridgeMessage{BridgeID: row.ID, Seq: 1, SlackTS: "1700000000.000001", RootTS: "1700000000.000001", CreatedAt: 1}); err != nil {
				return err
			}
			return tx.PutBridgeDrop(ctx, row.ID, "1700000000.000002", 1)
		})

		if err := x.svc.Remove(ctx, x.adm, ab2bus.RemoveBridgeInput{Channel: "job1-chat"}); err != nil {
			t.Fatalf("Remove: %v", err)
		}
		fc, _ := x.fake.Channel(sid)
		if !fc.Archived || len(fc.Posts) == 0 || fc.Posts[len(fc.Posts)-1].Section != slackbridge.FinalLine {
			t.Errorf("slack channel after remove: archived %v, last post %+v", fc.Archived, fc.Posts)
		}
		if _, found := x.bridgeRow(ch); found {
			t.Error("bridge row remains")
		}
		x.h.tx(func(ctx context.Context, tx ab2store.Tx) error {
			if _, err := tx.BridgeMessageBySeq(ctx, row.ID, 1); !errors.Is(err, ab2store.ErrNotFound) {
				t.Errorf("map row remains: %v", err)
			}
			if d, _ := tx.BridgeDropped(ctx, row.ID, "1700000000.000002"); d {
				t.Error("drop row remains")
			}
			return nil
		})
		if hasACL(x.acl(ch), ab2store.SlackBridge, ab2store.PermRead) || x.bridgeSubscribed(ch) {
			t.Error("slack-bridge ACL entry or subscription remains")
		}
		x.h.tx(func(ctx context.Context, tx ab2store.Tx) error {
			if _, err := tx.ChannelByName(ctx, "job1-chat"); err != nil {
				t.Errorf("channel gone: %v", err)
			}
			if sc, err := tx.Scope(ctx, "job1"); err != nil || sc.State != ab2store.ScopeOpen {
				t.Errorf("scope = %+v, %v", sc, err)
			}
			return nil
		})

		// A second Remove finds nothing.
		err = x.svc.Remove(ctx, x.adm, ab2bus.RemoveBridgeInput{Channel: "job1-chat"})
		codeOf(t, err, ab2bus.CodeNotFound)

		x.noSecretsLogged(t)

		// The archived Slack channel keeps its name.
		_, err = x.create("job1-chat", "")
		codeOf(t, err, ab2bus.CodeConflict)
		if m := msgOf(err); m != `Slack channel name "job1-chat" is taken` {
			t.Errorf("re-create message = %q", m)
		}
		again, err := x.create("job1-chat", "job1-chat-b")
		if err != nil || again.Bridge.State != ab2bus.BridgeLive || again.Bridge.SlackChannelID == sid {
			t.Fatalf("re-create with a new name = %+v, %v", again, err)
		}

		// ArchiveBridge twice: one Slack Archive call, no error.
		cur, _ := x.bridgeRow(ch)
		before := x.fake.Calls("Archive")
		for range 2 {
			if err := x.svc.ArchiveBridge(ctx, cur, "idle"); err != nil {
				t.Fatalf("ArchiveBridge: %v", err)
			}
		}
		if n := x.fake.Calls("Archive") - before; n != 1 {
			t.Errorf("Archive calls = %d, want 1", n)
		}
		if r, _ := x.bridgeRow(ch); r.State != ab2store.BridgeArchived || r.ArchivedAt == 0 || x.bridgeSubscribed(ch) {
			t.Errorf("row after archive = %+v (subscribed %v)", r, x.bridgeSubscribed(ch))
		}

		// A channel archived by hand is success, whichever replica got there first.
		x2 := newSlackFix(t, e)
		ch2 := x2.slChannel("job1-chat", "job1", nil)
		g2, err := x2.create("job1-chat", "")
		if err != nil {
			t.Fatal(err)
		}
		x2.fake.ArchiveByHand(g2.Bridge.SlackChannelID)
		cur2, _ := x2.bridgeRow(ch2)
		if err := x2.svc.ArchiveBridge(ctx, cur2, "idle"); err != nil {
			t.Fatalf("ArchiveBridge on a hand-archived channel: %v", err)
		}
		if r, _ := x2.bridgeRow(ch2); r.State != ab2store.BridgeArchived {
			t.Errorf("row = %+v", r)
		}
		// A stale snapshot of a removed bridge is a no-op.
		if err := x2.svc.Remove(ctx, "adm", ab2bus.RemoveBridgeInput{Channel: "job1-chat"}); err != nil {
			t.Fatalf("Remove of an archived bridge: %v", err)
		}
		if err := x2.svc.ArchiveBridge(ctx, cur2, "removed"); err != nil {
			t.Errorf("ArchiveBridge of a removed bridge: %v", err)
		}
	})
}

func TestSlackBridgeNotReady(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		f := slackfake.New(slackapi.Self{TeamID: slTeam, UserID: slBot, BotID: "BBOT"})
		h.admin("adm")
		ctx := context.Background()
		f.FailNext("AuthTest", errors.New("dial tcp: no route"))
		f.FailNext("AuthTest", errors.New("dial tcp: no route"))
		svc, err := slackbridge.New(ctx, h.core, f, slackbridge.Config{ArchiveAfterDone: time.Hour, MaxMessageChars: 3000, LockRenew: 5 * time.Millisecond})
		if err != nil {
			t.Fatalf("New with a network failure: %v", err)
		}
		_, err = svc.List(ctx, "adm")
		codeOf(t, err, ab2bus.CodeUnavailable)
		if m := msgOf(err); m != "the Slack bridge is not connected to Slack yet; retry" {
			t.Errorf("message = %q", m)
		}
		if be := new(ab2bus.Error); !errors.As(err, &be) || !be.Retryable {
			t.Errorf("not retryable: %v", err)
		}
		_, err = svc.Create(ctx, "adm", ab2bus.CreateBridgeInput{Channel: "x"})
		codeOf(t, err, ab2bus.CodeUnavailable)
		if err := svc.ArchiveBridge(ctx, ab2store.Bridge{ID: 1}, "idle"); err == nil {
			t.Error("ArchiveBridge before ready returned nil")
		}
		// The admin check comes first.
		_, err = svc.List(ctx, "nobody")
		if ab2bus.CodeOf(err) == ab2bus.CodeUnavailable {
			t.Errorf("a non-registered caller got unavailable: %v", err)
		}

		rctx, cancel := context.WithCancel(ctx)
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- svc.Run(rctx) }()
		// Run retries AuthTest until Slack answers, then goes on to compete for
		// the runner lock; it returns nil only when its context ends.
		waitFor(t, 10*time.Second, "the bridge to become ready", func() bool { _, ok := svc.Self(); return ok })
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("Run did not return after its context ended")
		}
		if n := f.Calls("AuthTest"); n != 3 {
			t.Errorf("AuthTest calls = %d, want 3", n)
		}
		if l, err := svc.List(ctx, "adm"); err != nil || len(l) != 0 {
			t.Errorf("List after ready = %v, %v", l, err)
		}

		// A wrong token fails New, naming no token.
		bad := slackfake.New(slackapi.Self{})
		bad.FailNext("AuthTest", &slackapi.APIError{Method: "AuthTest", Code: "invalid_auth"})
		_, err = slackbridge.New(ctx, h.core, bad, slackbridge.Config{ArchiveAfterDone: time.Hour, MaxMessageChars: 3000})
		if err == nil || !strings.Contains(err.Error(), "invalid_auth") || strings.Contains(err.Error(), "xoxb") {
			t.Errorf("New with invalid_auth = %v", err)
		}
		// Config bounds.
		for _, c := range []slackbridge.Config{
			{MaxMessageChars: 3000},
			{ArchiveAfterDone: time.Hour, MaxMessageChars: 199},
			{ArchiveAfterDone: time.Hour, MaxMessageChars: 3001},
			{ArchiveAfterDone: time.Hour, MaxMessageChars: 3000, LockRenew: -1},
		} {
			if _, err := slackbridge.New(ctx, h.core, f, c); err == nil {
				t.Errorf("New accepted %+v", c)
			}
		}
	})
}

// rtChannelByName reads a channel through the Store.
func (h *harness) rtChannelByName(name string) ab2store.Channel {
	h.t.Helper()
	var ch ab2store.Channel
	h.tx(func(ctx context.Context, tx ab2store.Tx) error {
		var err error
		ch, err = tx.ChannelByName(ctx, name)
		return err
	})
	return ch
}
