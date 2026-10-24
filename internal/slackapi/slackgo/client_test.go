package slackgo

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/slackapi"
)

const (
	testApp = "xapp-1-TESTAPPSECRET"
	testBot = "xoxb-TESTBOTSECRET"
)

type call struct {
	Method string
	Form   url.Values
}

// fakeSlack answers each Web API method with the next canned reply for it (the
// last repeats) and records every call.
type fakeSlack struct {
	t       *testing.T
	mu      sync.Mutex
	calls   []call
	replies map[string][]func(w http.ResponseWriter)
}

func newFake(t *testing.T) (*fakeSlack, *Client) {
	t.Helper()
	f := &fakeSlack{t: t, replies: map[string][]func(http.ResponseWriter){}}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	c, err := New(testApp, testBot, Options{APIURL: srv.URL + "/api/"})
	if err != nil {
		t.Fatal(err)
	}
	return f, c
}

func (f *fakeSlack) serve(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	m := strings.TrimPrefix(r.URL.Path, "/api/")
	f.mu.Lock()
	f.calls = append(f.calls, call{m, r.PostForm})
	rs := f.replies[m]
	var fn func(http.ResponseWriter)
	if len(rs) > 1 {
		fn, f.replies[m] = rs[0], rs[1:]
	} else if len(rs) == 1 {
		fn = rs[0]
	}
	f.mu.Unlock()
	if fn == nil {
		fn = jsonReply(`{"ok":true}`)
	}
	fn(w)
}

func jsonReply(body string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}
}

func slackErr(code string) func(http.ResponseWriter) {
	return jsonReply(`{"ok":false,"error":"` + code + `"}`)
}

func (f *fakeSlack) on(method string, fns ...func(http.ResponseWriter)) {
	f.mu.Lock()
	f.replies[method] = fns
	f.mu.Unlock()
}

func (f *fakeSlack) callsOf(method string) []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []call
	for _, c := range f.calls {
		if c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

func TestCreatePrivateChannelParams(t *testing.T) {
	f, c := newFake(t)
	f.on("conversations.create", jsonReply(`{"ok":true,"channel":{"id":"C123"}}`))
	id, err := c.CreatePrivateChannel(context.Background(), "scope-a")
	if err != nil || id != "C123" {
		t.Fatalf("got %q, %v", id, err)
	}
	got := f.callsOf("conversations.create")[0].Form
	if got.Get("name") != "scope-a" || got.Get("is_private") != "true" {
		t.Fatalf("form %v", got)
	}
	f.on("conversations.create", slackErr("name_taken"))
	if _, err := c.CreatePrivateChannel(context.Background(), "scope-a"); !errors.Is(err, slackapi.ErrNameTaken) {
		t.Fatalf("got %v", err)
	}
}

func TestFindPrivateChannelPaged(t *testing.T) {
	f, c := newFake(t)
	f.on("conversations.list",
		jsonReply(`{"ok":true,"channels":[{"id":"C1","name":"a"}],"response_metadata":{"next_cursor":"p2"}}`),
		jsonReply(`{"ok":true,"channels":[{"id":"C2","name":"b","is_archived":true}],"response_metadata":{"next_cursor":""}}`))
	info, err := c.FindPrivateChannel(context.Background(), "b")
	if err != nil || info != (slackapi.ChannelInfo{ID: "C2", Name: "b", Archived: true}) {
		t.Fatalf("got %+v, %v", info, err)
	}
	calls := f.callsOf("conversations.list")
	if calls[0].Form.Get("types") != "private_channel" || calls[0].Form.Has("exclude_archived") || calls[1].Form.Get("cursor") != "p2" {
		t.Fatalf("forms %v", calls)
	}
	f.on("conversations.list", jsonReply(`{"ok":true,"channels":[]}`))
	if _, err := c.FindPrivateChannel(context.Background(), "zz"); !errors.Is(err, slackapi.ErrChannelNotFound) {
		t.Fatalf("got %v", err)
	}
}

func TestInvitePerUser(t *testing.T) {
	f, c := newFake(t)
	f.on("conversations.invite", jsonReply(`{"ok":true,"channel":{"id":"C1"}}`), slackErr("cant_invite_guest"), slackErr("already_in_channel"))
	fails, err := c.Invite(context.Background(), "C1", []string{"U1", "U2", "U3"})
	if err != nil {
		t.Fatal(err)
	}
	if len(fails) != 1 || fails[0] != (slackapi.InviteFailure{User: "U2", Code: "cant_invite_guest"}) {
		t.Fatalf("failures %+v", fails)
	}
	calls := f.callsOf("conversations.invite")
	if len(calls) != 3 {
		t.Fatalf("%d calls", len(calls))
	}
	for i, u := range []string{"U1", "U2", "U3"} {
		if calls[i].Form.Get("users") != u || calls[i].Form.Has("force") {
			t.Fatalf("call %d form %v", i, calls[i].Form)
		}
	}
}

func TestInviteErrors(t *testing.T) {
	ctx := context.Background()
	t.Run("user-not-found-is-a-failure", func(t *testing.T) {
		f, c := newFake(t)
		f.on("conversations.invite", slackErr("user_not_found"))
		fails, err := c.Invite(ctx, "C1", []string{"U1", "U2"})
		if err != nil || len(fails) != 2 || fails[0].Code != "user_not_found" {
			t.Fatalf("%+v, %v", fails, err)
		}
	})
	t.Run("channel-gone-is-an-error", func(t *testing.T) {
		f, c := newFake(t)
		f.on("conversations.invite", jsonReply(`{"ok":true}`), slackErr("channel_not_found"))
		fails, err := c.Invite(ctx, "C1", []string{"U1", "U2", "U3"})
		if !errors.Is(err, slackapi.ErrChannelGone) || len(fails) != 0 || len(f.callsOf("conversations.invite")) != 2 {
			t.Fatalf("%+v, %v", fails, err)
		}
	})
	t.Run("rate-limit-stops-and-keeps-failures", func(t *testing.T) {
		f, c := newFake(t)
		f.on("conversations.invite", slackErr("user_not_found"), func(w http.ResponseWriter) {
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusTooManyRequests)
		})
		fails, err := c.Invite(ctx, "C1", []string{"U1", "U2", "U3"})
		var rl *slackapi.RateLimitedError
		if !errors.As(err, &rl) || rl.RetryAfter != 2*time.Second {
			t.Fatalf("err %v", err)
		}
		if len(fails) != 1 || fails[0].User != "U1" {
			t.Fatalf("failures %+v", fails)
		}
	})
}

func TestArchiveErrors(t *testing.T) {
	f, c := newFake(t)
	f.on("conversations.archive", slackErr("already_archived"))
	if err := c.Archive(context.Background(), "C1"); !errors.Is(err, slackapi.ErrAlreadyArchived) {
		t.Fatalf("got %v", err)
	}
	f.on("conversations.archive", jsonReply(`{"ok":true}`))
	if err := c.Archive(context.Background(), "C1"); err != nil {
		t.Fatal(err)
	}
	f.on("chat.postMessage", slackErr("is_archived"))
	if _, err := c.Post(context.Background(), slackapi.PostInput{Channel: "C1", Section: "x"}); !errors.Is(err, slackapi.ErrChannelGone) {
		t.Fatalf("got %v", err)
	}
	f.on("chat.postMessage", slackErr("msg_too_long"))
	_, err := c.Post(context.Background(), slackapi.PostInput{Channel: "C1", Section: "x"})
	var ae *slackapi.APIError
	if !errors.As(err, &ae) || ae.Code != "msg_too_long" || ae.Method != "chat.postMessage" {
		t.Fatalf("got %v", err)
	}
}

func TestPostParams(t *testing.T) {
	f, c := newFake(t)
	f.on("chat.postMessage", jsonReply(`{"ok":true,"channel":"C1","ts":"1700000000.000100"}`))
	ts, err := c.Post(context.Background(), slackapi.PostInput{
		Channel: "C1", Fallback: "plain", Section: "*hi*", Context: []string{"#a", "#b"},
		Username: "agent-1", IconEmoji: ":robot_face:", ThreadTS: "1700000000.000001", Seq: 42,
	})
	if err != nil || ts != "1700000000.000100" {
		t.Fatalf("got %q, %v", ts, err)
	}
	form := f.callsOf("chat.postMessage")[0].Form
	for k, want := range map[string]string{"channel": "C1", "text": "plain", "username": "agent-1", "icon_emoji": ":robot_face:", "thread_ts": "1700000000.000001"} {
		if form.Get(k) != want {
			t.Errorf("%s = %q, want %q", k, form.Get(k), want)
		}
	}
	var blocks []struct {
		Type     string `json:"type"`
		Text     struct{ Type, Text string }
		Elements []struct{ Type, Text string }
	}
	if err := json.Unmarshal([]byte(form.Get("blocks")), &blocks); err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 2 || blocks[0].Type != "section" || blocks[0].Text.Type != "mrkdwn" || blocks[0].Text.Text != "*hi*" ||
		blocks[1].Type != "context" || len(blocks[1].Elements) != 2 || blocks[1].Elements[1].Text != "#b" {
		t.Fatalf("blocks %s", form.Get("blocks"))
	}
	var md struct {
		EventType    string         `json:"event_type"`
		EventPayload map[string]int `json:"event_payload"`
	}
	if err := json.Unmarshal([]byte(form.Get("metadata")), &md); err != nil || md.EventType != "agentbus2_message" || md.EventPayload["seq"] != 42 {
		t.Fatalf("metadata %s, %v", form.Get("metadata"), err)
	}
	if strings.Contains(form.Get("metadata"), "\"seq\":42.") {
		t.Fatalf("seq is not an integer: %s", form.Get("metadata"))
	}
}

func TestPostWithoutTagsOrThread(t *testing.T) {
	f, c := newFake(t)
	f.on("chat.postMessage", jsonReply(`{"ok":true,"ts":"1700000000.000100"}`))
	if _, err := c.Post(context.Background(), slackapi.PostInput{Channel: "C1", Section: "x", Seq: 1}); err != nil {
		t.Fatal(err)
	}
	form := f.callsOf("chat.postMessage")[0].Form
	if form.Has("thread_ts") || form.Has("username") || form.Has("icon_emoji") || strings.Contains(form.Get("blocks"), `"context"`) {
		t.Fatalf("form %v", form)
	}
}

func TestRateLimitedMapped(t *testing.T) {
	f, c := newFake(t)
	f.on("chat.postMessage", func(w http.ResponseWriter) {
		w.Header().Set("Retry-After", "3")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	_, err := c.Post(context.Background(), slackapi.PostInput{Channel: "C1", Section: "x"})
	var rl *slackapi.RateLimitedError
	if !errors.As(err, &rl) || rl.RetryAfter != 3*time.Second || rl.Method != "chat.postMessage" {
		t.Fatalf("got %#v", err)
	}
}

func TestHTTPErrorMapped(t *testing.T) {
	f, c := newFake(t)
	f.on("auth.test", func(w http.ResponseWriter) { w.WriteHeader(http.StatusBadGateway) })
	_, err := c.AuthTest(context.Background())
	var ae *slackapi.APIError
	if !errors.As(err, &ae) || ae.Code != "http_502" {
		t.Fatalf("got %#v", err)
	}
}

func TestAuthTest(t *testing.T) {
	f, c := newFake(t)
	f.on("auth.test", jsonReply(`{"ok":true,"team_id":"T1","user_id":"UBOT","bot_id":"B1"}`))
	self, err := c.AuthTest(context.Background())
	if err != nil || self != (slackapi.Self{TeamID: "T1", UserID: "UBOT", BotID: "B1"}) {
		t.Fatalf("got %+v, %v", self, err)
	}
	f.on("auth.test", slackErr("invalid_auth"))
	_, err = c.AuthTest(context.Background())
	var ae *slackapi.APIError
	if !errors.As(err, &ae) || ae.Code != "invalid_auth" {
		t.Fatalf("got %v", err)
	}
}

func TestUserMapping(t *testing.T) {
	f, c := newFake(t)
	f.on("users.info", jsonReply(`{"ok":true,"user":{"id":"U1","team_id":"T1","deleted":true,"is_bot":true,
		"is_restricted":true,"is_ultra_restricted":true,"is_stranger":true,
		"real_name":"Real Name","profile":{"email":"a@b.c","display_name":"disp"}}}`))
	u, err := c.UserInfo(context.Background(), "U1")
	want := slackapi.User{ID: "U1", TeamID: "T1", Email: "a@b.c", DisplayName: "disp", IsBot: true, Deleted: true, Restricted: true, UltraRestricted: true, Stranger: true}
	if err != nil || u != want {
		t.Fatalf("got %+v, %v", u, err)
	}
	if f.callsOf("users.info")[0].Form.Get("user") != "U1" {
		t.Fatal("user param")
	}
	f.on("users.info", jsonReply(`{"ok":true,"user":{"id":"U2","real_name":"Real Name","profile":{"display_name":""}}}`))
	u, err = c.UserInfo(context.Background(), "U2")
	if err != nil || u.DisplayName != "Real Name" || u.Restricted || u.Email != "" {
		t.Fatalf("fallback: %+v, %v", u, err)
	}
	f.on("users.info", slackErr("user_not_found"))
	if _, err := c.UserInfo(context.Background(), "U3"); !errors.Is(err, slackapi.ErrUserNotFound) {
		t.Fatalf("got %v", err)
	}
	f.on("users.lookupByEmail", slackErr("users_not_found"))
	if _, err := c.LookupByEmail(context.Background(), "x@y.z"); !errors.Is(err, slackapi.ErrUserNotFound) {
		t.Fatalf("got %v", err)
	}
	f.on("users.lookupByEmail", jsonReply(`{"ok":true,"user":{"id":"U9","profile":{"email":"x@y.z","real_name":"P Real"}}}`))
	u, err = c.LookupByEmail(context.Background(), "x@y.z")
	if err != nil || u.ID != "U9" || u.DisplayName != "P Real" || f.callsOf("users.lookupByEmail")[1].Form.Get("email") != "x@y.z" {
		t.Fatalf("got %+v, %v", u, err)
	}
}

func TestHistoryIncludesMetadata(t *testing.T) {
	f, c := newFake(t)
	f.on("conversations.history", jsonReply(`{"ok":true,"messages":[
		{"ts":"1700000002.000000","user":"UBOT","bot_id":"B1","text":"b","metadata":{"event_type":"agentbus2_message","event_payload":{"seq":42}}},
		{"ts":"1700000001.000000","user":"U1","text":"a","thread_ts":"1700000001.000000","reply_count":2},
		{"ts":"1700000000.000000","user":"U2","text":"c","metadata":{"event_type":"other","event_payload":{"seq":7}}}],
		"response_metadata":{"next_cursor":"nx"}}`))
	p, err := c.History(context.Background(), "C1", "1699999999.000000", "cur")
	if err != nil {
		t.Fatal(err)
	}
	form := f.callsOf("conversations.history")[0].Form
	if form.Get("include_all_metadata") != "1" || form.Get("oldest") != "1699999999.000000" || form.Get("cursor") != "cur" || form.Get("channel") != "C1" {
		t.Fatalf("form %v", form)
	}
	if p.NextCursor != "nx" || len(p.Messages) != 3 {
		t.Fatalf("page %+v", p)
	}
	if m := p.Messages[0]; m.MetadataSeq != 42 || m.BotID != "B1" || m.TS != "1700000002.000000" {
		t.Fatalf("message 0 %+v", m)
	}
	if m := p.Messages[1]; m.MetadataSeq != 0 || m.ReplyCount != 2 || m.ThreadTS != "1700000001.000000" {
		t.Fatalf("message 1 %+v", m)
	}
	if p.Messages[2].MetadataSeq != 0 {
		t.Fatalf("foreign metadata read as seq: %+v", p.Messages[2])
	}
}

func TestRepliesAndMembers(t *testing.T) {
	f, c := newFake(t)
	f.on("conversations.replies", jsonReply(`{"ok":true,"has_more":true,"messages":[{"ts":"1.1","text":"r","metadata":{"event_type":"agentbus2_message","event_payload":{"seq":5}}}],"response_metadata":{"next_cursor":"n2"}}`))
	p, err := c.Replies(context.Background(), "C1", "1700000000.000001", "1699999999.000000", "")
	if err != nil || p.NextCursor != "n2" || len(p.Messages) != 1 || p.Messages[0].MetadataSeq != 5 {
		t.Fatalf("got %+v, %v", p, err)
	}
	form := f.callsOf("conversations.replies")[0].Form
	if form.Get("ts") != "1700000000.000001" || form.Get("include_all_metadata") != "1" {
		t.Fatalf("form %v", form)
	}
	f.on("conversations.members",
		jsonReply(`{"ok":true,"members":["U1","U2"],"response_metadata":{"next_cursor":"m2"}}`),
		jsonReply(`{"ok":true,"members":["U3"],"response_metadata":{"next_cursor":""}}`))
	ids, err := c.Members(context.Background(), "C1")
	if err != nil || strings.Join(ids, ",") != "U1,U2,U3" {
		t.Fatalf("got %v, %v", ids, err)
	}
}

func TestNewRefusesTokenShapes(t *testing.T) {
	_, err := New("xoxb-SECRETVALUE", "xoxb-OTHERSECRET", Options{})
	if err == nil || !strings.Contains(err.Error(), "slack_app_token_file") {
		t.Fatalf("got %v", err)
	}
	if strings.Contains(err.Error(), "SECRETVALUE") || strings.Contains(err.Error(), "OTHERSECRET") {
		t.Fatalf("token in error: %v", err)
	}
	_, err = New("xapp-1-SECRETVALUE", "xapp-WRONG", Options{})
	if err == nil || !strings.Contains(err.Error(), "slack_bot_token_file") || strings.Contains(err.Error(), "SECRETVALUE") || strings.Contains(err.Error(), "WRONG") {
		t.Fatalf("got %v", err)
	}
	if _, err := New("", "", Options{}); err == nil {
		t.Fatal("empty tokens accepted")
	}
	if _, err := New("xapp-1-a", "xoxb-b", Options{}); err != nil {
		t.Fatal(err)
	}
}

func TestErrorsCarryNoToken(t *testing.T) {
	f, c := newFake(t)
	f.on("chat.postMessage", slackErr("msg_too_long"))
	_, err := c.Post(context.Background(), slackapi.PostInput{Channel: "C1", Section: "x"})
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("got %v", err)
	}
	// A transport failure (server gone) wraps the Go error; it must not hold a token either.
	srv := httptest.NewServer(http.NotFoundHandler())
	c2, _ := New(testApp, testBot, Options{APIURL: srv.URL + "/api/"})
	srv.Close()
	_, err = c2.AuthTest(context.Background())
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("got %v", err)
	}
	_ = f
}
