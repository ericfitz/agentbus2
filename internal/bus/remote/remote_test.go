package remote

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/gen"
)

// canned serves one fixed response and counts requests.
type canned struct {
	status int
	ctype  string
	body   string
	n      atomic.Int64
}

func (c *canned) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	c.n.Add(1)
	w.Header().Set("Content-Type", c.ctype)
	w.WriteHeader(c.status)
	_, _ = w.Write([]byte(c.body))
}

func newBus(t *testing.T, c *canned) *Bus {
	t.Helper()
	srv := httptest.NewServer(c)
	t.Cleanup(srv.Close)
	cl, err := gen.NewClientWithResponses(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return New(cl)
}

// do sends one real request through the generated client to the canned server
// and maps the response with errorOf, the way every remote method does.
func do(t *testing.T, c *canned) error {
	t.Helper()
	b := newBus(t, c)
	ctx := context.Background()
	r, err := b.c.AdminStatusWithResponse(ctx)
	if err != nil {
		return errorOf(ctx, 0, nil, nil, err)
	}
	return errorOf(ctx, r.StatusCode(), r.ApplicationproblemJSONDefault, r.Body, nil)
}

func asBusError(t *testing.T, err error) *ab2bus.Error {
	t.Helper()
	var be *ab2bus.Error
	if !errors.As(err, &be) {
		t.Fatalf("want *bus.Error, got %T: %v", err, err)
	}
	return be
}

func TestRemoteMapsKnownProblem(t *testing.T) {
	err := do(t, &canned{
		status: 409, ctype: "application/problem+json",
		body: `{"type":"about:blank","title":"Conflict","status":409,"code":"conflict","detail":"taken","retryable":false}`,
	})
	be := asBusError(t, err)
	if *be != (ab2bus.Error{Code: ab2bus.CodeConflict, Message: "taken", Retryable: false}) {
		t.Fatalf("got %+v", be)
	}
	if ab2bus.CodeOf(err) != ab2bus.CodeConflict {
		t.Fatalf("CodeOf = %q", ab2bus.CodeOf(err))
	}
}

func TestRemoteTitleWhenNoDetail(t *testing.T) {
	err := do(t, &canned{
		status: 404, ctype: "application/problem+json",
		body: `{"type":"about:blank","title":"Not Found","status":404,"code":"not_found","retryable":false}`,
	})
	if be := asBusError(t, err); be.Message != "Not Found" || be.Code != ab2bus.CodeNotFound {
		t.Fatalf("got %+v", be)
	}
}

func TestRemoteNonProblemAndUnknownCode(t *testing.T) {
	be := asBusError(t, do(t, &canned{status: 502, ctype: "text/html", body: "<html>bad gateway</html>"}))
	if be.Code != ab2bus.CodeUnavailable || !be.Retryable || be.Message != "HTTP 502" {
		t.Fatalf("502: %+v", be)
	}
	be = asBusError(t, do(t, &canned{status: 418, ctype: "text/plain", body: "teapot"}))
	if be.Code != ab2bus.CodeInternal || be.Retryable || be.Message != "HTTP 418: teapot" {
		t.Fatalf("418: %+v", be)
	}
	be = asBusError(t, do(t, &canned{
		status: 400, ctype: "application/problem+json",
		body: `{"type":"about:blank","title":"Bad","status":400,"code":"nonsense","detail":"x","retryable":true}`,
	}))
	if be.Code != "nonsense" || !be.Retryable || be.Message != "x" {
		t.Fatalf("unknown code: %+v", be)
	}
	if ab2bus.ValidCode(be.Code) {
		t.Fatal("unknown code must stay unknown")
	}
}

func TestRemoteBodyExcerptBounded(t *testing.T) {
	be := asBusError(t, do(t, &canned{status: 400, ctype: "text/plain", body: strings.Repeat("a", 1000)}))
	if want := "HTTP 400: " + strings.Repeat("a", 200); be.Message != want {
		t.Fatalf("message length %d", len(be.Message))
	}
}

func TestRemoteTransportErrors(t *testing.T) {
	srv := httptest.NewServer(&canned{status: 200, ctype: "application/json", body: "{}"})
	cl, err := gen.NewClientWithResponses(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	srv.Close()
	b := New(cl)
	ctx := context.Background()
	_, terr := b.c.AdminStatusWithResponse(ctx)
	if terr == nil {
		t.Fatal("want a transport error from a closed server")
	}
	be := asBusError(t, errorOf(ctx, 0, nil, nil, terr))
	if be.Code != ab2bus.CodeUnavailable || !be.Retryable {
		t.Fatalf("got %+v", be)
	}

	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if got := errorOf(cctx, 0, nil, nil, fmt.Errorf("boom")); !errors.Is(got, context.Canceled) {
		t.Fatalf("got %v", got)
	}
}

func TestRemoteSuccessIsNil(t *testing.T) {
	if err := errorOf(context.Background(), 204, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := errorOf(context.Background(), 200, nil, []byte(`{}`), nil); err != nil {
		t.Fatal(err)
	}
}

func TestRemoteKeyAndSeconds(t *testing.T) {
	if key("") != nil {
		t.Fatal(`key("") must be nil`)
	}
	if k := key("k"); k == nil || *k != "k" {
		t.Fatalf("key(k) = %v", k)
	}
	if seconds(0) != nil {
		t.Fatal("seconds(0) must be nil")
	}
	if s := seconds(200 * time.Millisecond); s == nil || *s != 0.2 {
		t.Fatalf("seconds(200ms) = %v", s)
	}
	if s := seconds(-1); s == nil || *s >= 0 {
		t.Fatalf("seconds(-1) = %v", s)
	}
}

func TestRemoteConvertRoundTrips(t *testing.T) {
	reply := int64(3)
	mem := int64(9)
	rev := int64(2)
	m := ab2bus.Message{
		Seq: 1, Channel: "c", Sender: "s", Session: "sess", Display: "d", CreatedAt: 5,
		Type: "t", Subject: "sub", Content: "body", ReplyTo: &reply,
		Metadata: map[string]string{"k": "v"}, MemoryID: &mem, Revision: &rev,
		Tags: []string{"a"}, MatchedTags: []string{"a"},
	}
	if got := fromGenMessage(toGenMessage(m)); !reflect.DeepEqual(got, m) {
		t.Fatalf("message: %+v != %+v", got, m)
	}
	if got := fromGenMessage(toGenMessage(ab2bus.Message{Content: "x"})); got.Content != "x" || got.Session != "" || got.Tags != nil {
		t.Fatalf("minimal message: %+v", got)
	}
	e := ab2bus.ACLEntry{Identity: "i", Permissions: []ab2bus.Permission{ab2bus.PermRead, ab2bus.PermAdmin}}
	if got := fromGenACLEntry(toGenACLEntry(e)); !reflect.DeepEqual(got, e) {
		t.Fatalf("acl: %+v", got)
	}
	for _, k := range []ab2bus.Kind{ab2bus.KindOrdinary, ab2bus.KindMemory, ab2bus.KindTasks} {
		if got := fromGenKind(toGenKind(k)); got != k {
			t.Fatalf("kind %q -> %q", k, got)
		}
	}
	tk := ab2bus.Task{
		ID: 1, Channel: "c", Revision: 2, Subject: "s", Description: "d", Status: "open", Owner: "o",
		Parent: 4, Rank: "r", BlockedBy: []int64{5}, LeasedUntil: 9, Metadata: map[string]string{"a": "b"},
		Blocked: true, OpenBlockers: []int64{5}, UpdatedAt: 7, UpdatedBy: "u",
	}
	if got := fromGenTask(toGenTask(tk)); !reflect.DeepEqual(got, tk) {
		t.Fatalf("task: %+v", got)
	}
	ts := ab2bus.TaskSummary{ID: 1, Subject: "s", Status: "open", Owner: "o", Parent: 2, Depth: 1, OpenBlockers: []int64{3}, LeasedUntil: 4, HasDetails: true}
	if got := fromGenTaskSummary(toGenTaskSummary(ts)); !reflect.DeepEqual(got, ts) {
		t.Fatalf("summary: %+v", got)
	}
}

// TestRemoteStubsNameTheirMethod calls every Bus method once. Each outcome is
// one of two: a stub (errors.Is ErrNotImplemented, text starting
// "remote.<Method>: ", no request sent) or an implemented method (exactly one
// request, and a *bus.Error or nil back). Stubs are replaced task by task, so
// this stays green throughout; Task 11 deletes it with the stubs.
func TestRemoteStubsNameTheirMethod(t *testing.T) {
	ctx := context.Background()
	calls := map[string]func(b *Bus) error{
		"Open":             func(b *Bus) error { _, err := b.Open(ctx, ab2bus.OpenSessionInput{}); return err },
		"Close":            func(b *Bus) error { return b.Close(ctx, ab2bus.CloseSessionInput{}) },
		"Receive":          func(b *Bus) error { _, err := b.Receive(ctx, ab2bus.ReceiveInput{}); return err },
		"Watch":            func(b *Bus) error { _, err := b.Watch(ctx, ab2bus.WatchInput{}); return err },
		"Identities":       func(b *Bus) error { _, err := b.Identities(ctx); return err },
		"ListChannels":     func(b *Bus) error { _, err := b.ListChannels(ctx); return err },
		"CreateChannel":    func(b *Bus) error { _, err := b.CreateChannel(ctx, ab2bus.CreateChannelInput{}); return err },
		"DeleteChannel":    func(b *Bus) error { return b.DeleteChannel(ctx, ab2bus.DeleteChannelInput{}) },
		"RenameChannel":    func(b *Bus) error { return b.RenameChannel(ctx, ab2bus.RenameChannelInput{}) },
		"ACL":              func(b *Bus) error { _, err := b.ACL(ctx, "c"); return err },
		"SetACL":           func(b *Bus) error { return b.SetACL(ctx, ab2bus.SetACLInput{}) },
		"Subscribe":        func(b *Bus) error { return b.Subscribe(ctx, ab2bus.SubscribeInput{}) },
		"Unsubscribe":      func(b *Bus) error { return b.Unsubscribe(ctx, ab2bus.UnsubscribeInput{}) },
		"SubscribeTags":    func(b *Bus) error { return b.SubscribeTags(ctx, ab2bus.TagSubscribeInput{}) },
		"UnsubscribeTags":  func(b *Bus) error { return b.UnsubscribeTags(ctx, ab2bus.TagSubscribeInput{Patterns: []string{"a"}}) },
		"TagSubscriptions": func(b *Bus) error { _, err := b.TagSubscriptions(ctx); return err },
		"Status":           func(b *Bus) error { _, err := b.Status(ctx); return err },
		"Send":             func(b *Bus) error { _, err := b.Send(ctx, ab2bus.SendInput{Channel: "c"}); return err },
		"History":          func(b *Bus) error { _, err := b.History(ctx, ab2bus.HistoryInput{}); return err },
		"Search":           func(b *Bus) error { _, err := b.Search(ctx, ab2bus.SearchInput{}); return err },
		"GetMemory":        func(b *Bus) error { _, err := b.GetMemory(ctx, 1); return err },
		"Edit":             func(b *Bus) error { _, err := b.Edit(ctx, ab2bus.EditMemoryInput{}); return err },
		"Delete":           func(b *Bus) error { return b.Delete(ctx, ab2bus.DeleteMemoryInput{}) },
		"Revisions":        func(b *Bus) error { _, err := b.Revisions(ctx, 1); return err },
		"Create":           func(b *Bus) error { _, err := b.Create(ctx, ab2bus.TaskCreateInput{}); return err },
		"GetTask":          func(b *Bus) error { _, err := b.GetTask(ctx, 1); return err },
		"List":             func(b *Bus) error { _, err := b.List(ctx, ab2bus.TaskListInput{}); return err },
		"Update":           func(b *Bus) error { _, err := b.Update(ctx, ab2bus.TaskPatch{}); return err },
		"Claim":            func(b *Bus) error { _, err := b.Claim(ctx, ab2bus.TaskClaimInput{}); return err },
		"Release":          func(b *Bus) error { _, err := b.Release(ctx, ab2bus.TaskReleaseInput{}); return err },
	}
	// One entry per method of the Bus interface (DeleteChannel and
	// RenameChannel are shared by Messaging and Admin).
	if want := reflect.TypeOf((*ab2bus.Bus)(nil)).Elem().NumMethod(); len(calls) != want {
		t.Fatalf("%d calls for %d Bus methods", len(calls), want)
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			c := &canned{
				status: 500, ctype: "application/problem+json",
				body: `{"type":"about:blank","title":"x","status":500,"code":"internal","retryable":true}`,
			}
			err := call(newBus(t, c))
			n := c.n.Load()
			if errors.Is(err, ErrNotImplemented) {
				if !strings.HasPrefix(err.Error(), "remote."+name+": ") {
					t.Fatalf("stub text %q does not name %s", err, name)
				}
				if n != 0 {
					t.Fatalf("stub sent %d requests", n)
				}
				return
			}
			if n != 1 {
				t.Fatalf("implemented method sent %d requests", n)
			}
			if err != nil {
				_ = asBusError(t, err)
			}
		})
	}
}

// TestUnsubscribeTagsEmptyPathSegment pins the one client-side rule of the
// remote Bus (pending Eric: Milestone 4 item 5, reopened for {patterns}): the
// set travels as one path segment, and an empty segment is the router's 404
// on the wire where the core answers validation. The guard fires for exactly
// the inputs that would encode to an empty segment and sends no request; every
// other input, bad or not, goes to the wire and is judged by the core.
func TestUnsubscribeTagsEmptyPathSegment(t *testing.T) {
	ctx := context.Background()
	for name, patterns := range map[string][]string{
		"nil":       nil,
		"empty":     {},
		"one empty": {""},
	} {
		t.Run("guarded/"+name, func(t *testing.T) {
			c := &canned{status: 204}
			err := newBus(t, c).UnsubscribeTags(ctx, ab2bus.TagSubscribeInput{Patterns: patterns})
			if be := asBusError(t, err); be.Code != ab2bus.CodeValidation {
				t.Fatalf("code = %q, want validation", be.Code)
			}
			if n := c.n.Load(); n != 0 {
				t.Fatalf("the guard let %d requests through", n)
			}
		})
	}
	for name, patterns := range map[string][]string{
		"one pattern":     {"a"},
		"empty then name": {"", "a"},
		"two empty":       {"", ""},
	} {
		t.Run("sent/"+name, func(t *testing.T) {
			c := &canned{status: 204}
			if err := newBus(t, c).UnsubscribeTags(ctx, ab2bus.TagSubscribeInput{Patterns: patterns}); err != nil {
				t.Fatalf("err = %v", err)
			}
			if n := c.n.Load(); n != 1 {
				t.Fatalf("%d requests, want exactly 1 (no rule beyond the empty segment)", n)
			}
		})
	}
}
