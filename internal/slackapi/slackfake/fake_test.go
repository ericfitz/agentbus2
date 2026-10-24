package slackfake

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/slackapi"
)

var self = slackapi.Self{TeamID: "T1", UserID: "UBOT", BotID: "BBOT"}

// runner starts RunSocket and collects handled events.
type runner struct {
	f      *Fake
	cancel context.CancelFunc
	events chan slackapi.Event
	ret    chan error
}

func startSocket(t *testing.T, f *Fake) *runner {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r := &runner{f: f, cancel: cancel, events: make(chan slackapi.Event, 64), ret: make(chan error, 1)}
	go func() {
		r.ret <- f.RunSocket(ctx, func(_ context.Context, ev slackapi.Event) { r.events <- ev })
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-r.ret:
		case <-time.After(2 * time.Second):
			t.Error("RunSocket did not return at cleanup")
		}
	})
	return r
}

func (r *runner) next(t *testing.T) slackapi.Event {
	t.Helper()
	select {
	case ev := <-r.events:
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("no event within 2s")
		return slackapi.Event{}
	}
}

func (r *runner) none(t *testing.T) {
	t.Helper()
	select {
	case ev := <-r.events:
		t.Fatalf("unexpected event %+v", ev)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestFakeChannelRules(t *testing.T) {
	ctx := context.Background()
	f := New(self)
	f.AddUser(slackapi.User{ID: "U1", Email: "a@example.com"})
	f.AddUser(slackapi.User{ID: "U2", Email: "b@example.com"})

	id, err := f.CreatePrivateChannel(ctx, "job-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.CreatePrivateChannel(ctx, "job-1"); !errors.Is(err, slackapi.ErrNameTaken) {
		t.Errorf("duplicate: %v", err)
	}
	_, err = f.CreatePrivateChannel(ctx, "Job")
	var ae *slackapi.APIError
	if !errors.As(err, &ae) || ae.Code != "invalid_name" {
		t.Errorf("invalid name: %v", err)
	}
	info, err := f.FindPrivateChannel(ctx, "job-1")
	if err != nil || info.ID != id || info.Name != "job-1" || info.Archived {
		t.Errorf("find: %+v %v", info, err)
	}
	if _, err := f.FindPrivateChannel(ctx, "nope"); !errors.Is(err, slackapi.ErrChannelNotFound) {
		t.Errorf("find miss: %v", err)
	}

	// Invite: a member is success, an unknown user is a failure, the rest are invited.
	if _, err := f.Invite(ctx, id, []string{"U1"}); err != nil {
		t.Fatal(err)
	}
	fails, err := f.Invite(ctx, id, []string{"U1", "UNKNOWN", "U2"})
	if err != nil {
		t.Fatalf("invite returned an error: %v", err)
	}
	if len(fails) != 1 || fails[0].User != "UNKNOWN" || fails[0].Code != "user_not_found" {
		t.Errorf("failures: %+v", fails)
	}
	ch, _ := f.Channel(id)
	if !slices.Contains(ch.Members, "U1") || !slices.Contains(ch.Members, "U2") || slices.Contains(ch.Members, "UNKNOWN") {
		t.Errorf("members: %v", ch.Members)
	}
	if !ch.Private || ch.Name != "job-1" {
		t.Errorf("channel: %+v", ch)
	}

	if _, err := f.Post(ctx, slackapi.PostInput{Channel: id, Fallback: "hi", Seq: 4}); err != nil {
		t.Fatal(err)
	}
	if err := f.Archive(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := f.Archive(ctx, id); !errors.Is(err, slackapi.ErrAlreadyArchived) {
		t.Errorf("archive twice: %v", err)
	}
	if _, err := f.Post(ctx, slackapi.PostInput{Channel: id, Fallback: "x"}); !errors.Is(err, slackapi.ErrChannelGone) {
		t.Errorf("post after archive: %v", err)
	}
	if _, err := f.Post(ctx, slackapi.PostInput{Channel: "CNOPE", Fallback: "x"}); !errors.Is(err, slackapi.ErrChannelGone) {
		t.Errorf("post unknown channel: %v", err)
	}
	if err := f.Archive(ctx, "CNOPE"); !errors.Is(err, slackapi.ErrChannelGone) {
		t.Errorf("archive unknown: %v", err)
	}
	// An archived channel keeps its name.
	if _, err := f.CreatePrivateChannel(ctx, "job-1"); !errors.Is(err, slackapi.ErrNameTaken) {
		t.Errorf("name of archived channel: %v", err)
	}
	info, _ = f.FindPrivateChannel(ctx, "job-1")
	if !info.Archived {
		t.Error("find does not report archived")
	}

	// Users.
	if u, err := f.UserInfo(ctx, "U1"); err != nil || u.Email != "a@example.com" {
		t.Errorf("UserInfo: %+v %v", u, err)
	}
	if _, err := f.UserInfo(ctx, "UX"); !errors.Is(err, slackapi.ErrUserNotFound) {
		t.Errorf("UserInfo miss: %v", err)
	}
	if u, err := f.LookupByEmail(ctx, "B@Example.com"); err != nil || u.ID != "U2" {
		t.Errorf("LookupByEmail: %+v %v", u, err)
	}
	if _, err := f.LookupByEmail(ctx, "z@example.com"); !errors.Is(err, slackapi.ErrUserNotFound) {
		t.Errorf("LookupByEmail miss: %v", err)
	}
	if got, err := f.AuthTest(ctx); err != nil || got != self {
		t.Errorf("AuthTest: %+v %v", got, err)
	}
}

func TestFakePostRecordsMetadataAndThreads(t *testing.T) {
	ctx := context.Background()
	f := New(self)
	id, _ := f.CreatePrivateChannel(ctx, "c")
	root, err := f.Post(ctx, slackapi.PostInput{Channel: id, Fallback: "root", Section: "*root*", Seq: 7, Context: []string{"a"}})
	if err != nil {
		t.Fatal(err)
	}
	reply, err := f.Post(ctx, slackapi.PostInput{Channel: id, Fallback: "r", ThreadTS: root, Seq: 8})
	if err != nil {
		t.Fatal(err)
	}
	ch, _ := f.Channel(id)
	if len(ch.Posts) != 2 || ch.Posts[0].Seq != 7 || ch.Posts[1].ThreadTS != root {
		t.Errorf("posts: %+v", ch.Posts)
	}
	if !slackapi.ValidTS(root) || root >= reply {
		t.Errorf("ts %q %q", root, reply)
	}
	m := ch.Messages[0]
	if m.TS != root || m.MetadataSeq != 7 || m.BotID != "BBOT" || m.User != "UBOT" || m.ReplyCount != 1 {
		t.Errorf("root message: %+v", m)
	}
	// Mutating the copy does not touch the fake.
	ch.Messages[0].Text = "tampered"
	ch2, _ := f.Channel(id)
	if ch2.Messages[0].Text == "tampered" {
		t.Error("Channel returned shared state")
	}
	if _, ok := f.Channel("CNOPE"); ok {
		t.Error("unknown channel found")
	}
}

func TestFakeSocketAckBeforeHandle(t *testing.T) {
	f := New(self)
	id, _ := f.CreatePrivateChannel(context.Background(), "c")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	var ackedAtHandle []string
	got := make(chan slackapi.Event, 8)
	go func() {
		done <- f.RunSocket(ctx, func(_ context.Context, ev slackapi.Event) {
			if ev.Kind == slackapi.EventMessage {
				ackedAtHandle = f.Acks()
			}
			got <- ev
		})
	}()
	first := recv(t, got)
	if first.Kind != slackapi.EventConnected {
		t.Fatalf("first event %q", first.Kind)
	}
	m := f.Say(id, "U1", "hello", "")
	ev := recv(t, got)
	if ev.Kind != slackapi.EventMessage || ev.Message.TS != m.TS || ev.Channel != id || ev.User != "U1" || ev.EnvelopeID == "" {
		t.Fatalf("event %+v", ev)
	}
	if !slices.Contains(ackedAtHandle, ev.EnvelopeID) {
		t.Errorf("envelope %q not acked before handle: %v", ev.EnvelopeID, ackedAtHandle)
	}
	cancel()
	if err := <-done; err != nil {
		t.Error(err)
	}
}

func TestFakeSayTimestamps(t *testing.T) {
	f := New(self)
	id, _ := f.CreatePrivateChannel(context.Background(), "c")
	a := f.Say(id, "U1", "a", "")
	b := f.SayAsBot(id, "BOTHER", "b", "", 9)
	c := f.Say(id, "U1", "c", a.TS)
	if a.TS != "1700000000.000001" || b.TS != "1700000000.000002" || c.TS != "1700000000.000003" {
		t.Errorf("ts %s %s %s", a.TS, b.TS, c.TS)
	}
	if b.BotID != "BOTHER" || b.MetadataSeq != 9 || c.ThreadTS != a.TS {
		t.Errorf("messages %+v %+v", b, c)
	}
	f.Join(id, "U3")
	f.Leave(id, "U3")
	ch, _ := f.Channel(id)
	if slices.Contains(ch.Members, "U3") {
		t.Error("U3 still a member")
	}
}

func TestFakeDropRepeatReorder(t *testing.T) {
	f := New(self)
	id, _ := f.CreatePrivateChannel(context.Background(), "c")
	r := startSocket(t, f)
	if r.next(t).Kind != slackapi.EventConnected {
		t.Fatal("no connected")
	}

	// Drop 1 of 3.
	f.DropNext(1)
	m1 := f.Say(id, "U1", "1", "")
	m2 := f.Say(id, "U1", "2", "")
	m3 := f.Say(id, "U1", "3", "")
	if e := r.next(t); e.Message.TS != m2.TS {
		t.Errorf("first delivered %s, want %s", e.Message.TS, m2.TS)
	}
	if e := r.next(t); e.Message.TS != m3.TS {
		t.Errorf("second delivered %s, want %s", e.Message.TS, m3.TS)
	}
	r.none(t)
	ch, _ := f.Channel(id)
	if len(ch.Messages) != 3 || ch.Messages[0].TS != m1.TS {
		t.Errorf("history: %+v", ch.Messages)
	}

	// Repeat: the same envelope twice.
	f.RepeatNext()
	m4 := f.Say(id, "U1", "4", "")
	a, b := r.next(t), r.next(t)
	if a.Message.TS != m4.TS || b.Message.TS != m4.TS || a.EnvelopeID != b.EnvelopeID {
		t.Errorf("repeat: %+v %+v", a, b)
	}
	m5 := f.Say(id, "U1", "5", "")
	if e := r.next(t); e.Message.TS != m5.TS {
		t.Errorf("after repeat: %+v", e)
	}
	r.none(t)

	// Reorder 3: reverse order.
	f.ReorderNext(3)
	x := f.Say(id, "U1", "x", "")
	y := f.Say(id, "U1", "y", "")
	r.none(t) // held until the group is complete
	z := f.Say(id, "U1", "z", "")
	for _, want := range []string{z.TS, y.TS, x.TS} {
		if e := r.next(t); e.Message.TS != want {
			t.Errorf("reorder: got %s want %s", e.Message.TS, want)
		}
	}
	// Back to ordered delivery.
	w := f.Say(id, "U1", "w", "")
	if e := r.next(t); e.Message.TS != w.TS {
		t.Errorf("ordered again: %+v", e)
	}
	// Acks include one entry per delivery, repeats included.
	if n := len(f.Acks()); n != 2+2+1+3+1 {
		t.Errorf("acks %d: %v", n, f.Acks())
	}
}

func TestFakeBadControls(t *testing.T) {
	f := New(self)
	for name, fn := range map[string]func(){
		"DropNext":         func() { f.DropNext(-1) },
		"ReorderNext":      func() { f.ReorderNext(-1) },
		"FailNext method":  func() { f.FailNext("Nope", errors.New("x")) },
		"RateLimit method": func() { f.RateLimit("Nope", 1, time.Second) },
		"Calls method":     func() { f.Calls("Nope") },
		"FailNext nil":     func() { f.FailNext("Post", nil) },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: no panic", name)
				}
			}()
			fn()
		}()
	}
}

func TestFakeDisconnectReconnects(t *testing.T) {
	f := New(self)
	id, _ := f.CreatePrivateChannel(context.Background(), "c")
	r := startSocket(t, f)
	if r.next(t).Kind != slackapi.EventConnected {
		t.Fatal("no connected")
	}
	f.Disconnect()
	if e := r.next(t); e.Kind != slackapi.EventConnected {
		t.Fatalf("after disconnect: %+v", e)
	}
	m := f.Say(id, "U1", "after", "")
	if e := r.next(t); e.Message.TS != m.TS {
		t.Errorf("event after reconnect: %+v", e)
	}
	if n := f.MaxConcurrentSockets(); n != 1 {
		t.Errorf("MaxConcurrentSockets = %d", n)
	}
}

func TestFakeMemberLeft(t *testing.T) {
	f := New(self)
	id, _ := f.CreatePrivateChannel(context.Background(), "c")
	f.Join(id, "U9")
	r := startSocket(t, f)
	r.next(t)
	f.Leave(id, "U9")
	e := r.next(t)
	if e.Kind != slackapi.EventMemberLeft || e.Channel != id || e.User != "U9" {
		t.Errorf("event %+v", e)
	}
}

func TestFakeRateLimitAndFail(t *testing.T) {
	ctx := context.Background()
	f := New(self)
	id, _ := f.CreatePrivateChannel(ctx, "c")
	f.RateLimit("Post", 2, time.Second)
	for i := range 2 {
		_, err := f.Post(ctx, slackapi.PostInput{Channel: id, Fallback: "x"})
		var rl *slackapi.RateLimitedError
		if !errors.As(err, &rl) || rl.RetryAfter != time.Second || rl.Method != "Post" {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if _, err := f.Post(ctx, slackapi.PostInput{Channel: id, Fallback: "x"}); err != nil {
		t.Fatalf("third: %v", err)
	}
	ch, _ := f.Channel(id)
	if len(ch.Posts) != 1 {
		t.Errorf("rate-limited calls must not post: %d", len(ch.Posts))
	}
	if n := f.Calls("Post"); n != 3 {
		t.Errorf("Calls(Post) = %d", n)
	}

	boom := errors.New("boom")
	f.FailNext("Archive", boom)
	if err := f.Archive(ctx, id); !errors.Is(err, boom) {
		t.Fatalf("first archive: %v", err)
	}
	if err := f.Archive(ctx, id); err != nil {
		t.Fatalf("second archive: %v", err)
	}
	if n := f.Calls("Archive"); n != 2 {
		t.Errorf("Calls(Archive) = %d", n)
	}
}

func TestFakeHistoryReplies(t *testing.T) {
	ctx := context.Background()
	f := New(self)
	id, _ := f.CreatePrivateChannel(ctx, "c")
	f.SetPageSize(2)
	var top []slackapi.Message
	for _, s := range []string{"a", "b", "c", "d", "e"} {
		top = append(top, f.Say(id, "U1", s, ""))
	}
	r1 := f.Say(id, "U2", "r1", top[0].TS)
	r2 := f.Say(id, "U2", "r2", top[0].TS)
	r3 := f.Say(id, "U2", "r3", top[0].TS)

	// History excludes oldest and returns top-level messages ascending.
	p, err := f.History(ctx, id, top[0].TS, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Messages) != 2 || p.Messages[0].TS != top[1].TS || p.Messages[1].TS != top[2].TS || p.NextCursor == "" {
		t.Fatalf("page 1: %+v", p)
	}
	p, err = f.History(ctx, id, top[0].TS, p.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Messages) != 2 || p.Messages[0].TS != top[3].TS || p.Messages[1].TS != top[4].TS || p.NextCursor != "" {
		t.Fatalf("page 2: %+v", p)
	}
	all, _ := f.History(ctx, id, "", "")
	if len(all.Messages) != 2 || all.Messages[0].TS != top[0].TS || all.Messages[0].ReplyCount != 3 {
		t.Errorf("unfiltered first page: %+v", all)
	}
	if _, err := f.History(ctx, id, "", "garbage"); err == nil {
		t.Error("bad cursor accepted")
	}
	if _, err := f.History(ctx, "CNOPE", "", ""); !errors.Is(err, slackapi.ErrChannelGone) {
		t.Errorf("unknown channel: %v", err)
	}

	// Replies: root first, then replies newer than oldest.
	rp, err := f.Replies(ctx, id, top[0].TS, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rp.Messages) != 2 || rp.Messages[0].TS != top[0].TS || rp.Messages[1].TS != r1.TS || rp.NextCursor == "" {
		t.Fatalf("replies page 1: %+v", rp)
	}
	rp, _ = f.Replies(ctx, id, top[0].TS, "", rp.NextCursor)
	if len(rp.Messages) != 2 || rp.Messages[0].TS != r2.TS || rp.Messages[1].TS != r3.TS || rp.NextCursor != "" {
		t.Fatalf("replies page 2: %+v", rp)
	}
	f.SetPageSize(10)
	rp, _ = f.Replies(ctx, id, top[0].TS, r1.TS, "")
	if len(rp.Messages) != 3 || rp.Messages[0].TS != top[0].TS || rp.Messages[1].TS != r2.TS || rp.Messages[2].TS != r3.TS {
		t.Fatalf("replies after r1: %+v", rp)
	}
	if _, err := f.Replies(ctx, id, "1700000000.999999", "", ""); err == nil {
		t.Error("unknown thread accepted")
	}
	if got, err := f.Members(ctx, id); err != nil || !slices.Contains(got, "UBOT") {
		t.Errorf("Members: %v %v", got, err)
	}
}

func TestFakeStopsWithContext(t *testing.T) {
	f := New(self)
	ctx, cancel := context.WithCancel(context.Background())
	ret := make(chan error, 1)
	got := make(chan slackapi.Event, 4)
	go func() { ret <- f.RunSocket(ctx, func(_ context.Context, ev slackapi.Event) { got <- ev }) }()
	recv(t, got)
	cancel()
	select {
	case err := <-ret:
		if err != nil {
			t.Errorf("RunSocket = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("RunSocket did not return")
	}
	select {
	case <-f.SocketDone():
	case <-time.After(time.Second):
		t.Fatal("SocketDone not closed")
	}
	// A second run after the first is a fresh socket with a fresh done channel.
	ctx2, cancel2 := context.WithCancel(context.Background())
	ret2 := make(chan error, 1)
	go func() { ret2 <- f.RunSocket(ctx2, func(context.Context, slackapi.Event) { got <- slackapi.Event{} }) }()
	recv(t, got)
	select {
	case <-f.SocketDone():
		t.Fatal("SocketDone closed while running")
	default:
	}
	cancel2()
	if err := <-ret2; err != nil {
		t.Error(err)
	}
	if n := f.MaxConcurrentSockets(); n != 1 {
		t.Errorf("MaxConcurrentSockets = %d", n)
	}
}

func TestFakeNoTokenInErrors(t *testing.T) {
	f := New(self)
	_, err := f.CreatePrivateChannel(context.Background(), "BAD NAME")
	if err == nil || strings.Contains(strings.ToLower(err.Error()), "xox") {
		t.Errorf("error %v", err)
	}
}

func recv(t *testing.T, ch <-chan slackapi.Event) slackapi.Event {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("no event within 2s")
		return slackapi.Event{}
	}
}

func TestFakeBroadcastInHistoryAndReplies(t *testing.T) {
	ctx := context.Background()
	f := New(self)
	id, _ := f.CreatePrivateChannel(ctx, "c")
	root := f.Say(id, "U1", "root", "")
	plain := f.Say(id, "U2", "plain", root.TS)
	r := startSocket(t, f)
	r.next(t) // connected
	r.next(t) // root
	r.next(t) // plain
	b := f.SayBroadcast(id, "U2", "bc", root.TS)
	if b.Subtype != "thread_broadcast" {
		t.Fatalf("subtype %q", b.Subtype)
	}
	if e := r.next(t); e.Message.TS != b.TS || e.Message.Subtype != "thread_broadcast" {
		t.Errorf("event %+v", e)
	}
	h, _ := f.History(ctx, id, "", "")
	var got []string
	for _, m := range h.Messages {
		got = append(got, m.TS)
	}
	if !slices.Equal(got, []string{root.TS, b.TS}) || h.Messages[1].Subtype != "thread_broadcast" {
		t.Errorf("history %v (plain reply %s must be absent)", got, plain.TS)
	}
	rp, _ := f.Replies(ctx, id, root.TS, "", "")
	if len(rp.Messages) != 3 || rp.Messages[2].TS != b.TS || rp.Messages[2].Subtype != "thread_broadcast" {
		t.Errorf("replies %+v", rp)
	}
	s := f.SaySubtype(id, "U3", "x", "", "me_message")
	if s.Subtype != "me_message" {
		t.Errorf("SaySubtype %+v", s)
	}
}

func TestFakeTwoSocketsAndDisconnect(t *testing.T) {
	f := New(self)
	a := startSocket(t, f)
	b := startSocket(t, f)
	if a.next(t).Kind != slackapi.EventConnected || b.next(t).Kind != slackapi.EventConnected {
		t.Fatal("no connected")
	}
	if n := f.MaxConcurrentSockets(); n != 2 {
		t.Errorf("MaxConcurrentSockets = %d", n)
	}
	f.Disconnect()
	if a.next(t).Kind != slackapi.EventConnected || b.next(t).Kind != slackapi.EventConnected {
		t.Error("Disconnect did not reach both")
	}
}

func TestFakeEchoPostsAndArchivedSay(t *testing.T) {
	ctx := context.Background()
	f := New(self)
	id, _ := f.CreatePrivateChannel(ctx, "c")
	r := startSocket(t, f)
	r.next(t)
	if _, err := f.Post(ctx, slackapi.PostInput{Channel: id, Fallback: "quiet", Seq: 1}); err != nil {
		t.Fatal(err)
	}
	r.none(t)
	f.EchoPosts(true)
	ts, err := f.Post(ctx, slackapi.PostInput{Channel: id, Fallback: "loud", Seq: 5})
	if err != nil {
		t.Fatal(err)
	}
	e := r.next(t)
	if e.Kind != slackapi.EventMessage || e.User != "UBOT" || e.Message.BotID != "BBOT" || e.Message.TS != ts || e.Message.MetadataSeq != 5 {
		t.Errorf("echo %+v", e)
	}
	f.RateLimit("Post", 1, time.Second)
	_, _ = f.Post(ctx, slackapi.PostInput{Channel: id, Fallback: "limited"})
	r.none(t) // a failed post echoes nothing
	f.ArchiveByHand(id)
	defer func() {
		if recover() == nil {
			t.Error("Say into an archived channel did not panic")
		}
	}()
	f.Say(id, "U1", "x", "")
}
