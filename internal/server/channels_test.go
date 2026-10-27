package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/bus/local"
	"github.com/ericfitz/agentbus2/internal/bus/remote"
	"github.com/ericfitz/agentbus2/internal/gen"
	"github.com/ericfitz/agentbus2/internal/store"
)

func storedACL(t *testing.T, e *env, channel string) []store.ACLEntry {
	t.Helper()
	var acl []store.ACLEntry
	err := e.store.Read(context.Background(), func(ctx context.Context, tx store.Tx) error {
		ch, err := tx.ChannelByName(ctx, channel)
		if err != nil {
			return err
		}
		acl, err = tx.ACL(ctx, ch.ID)
		return err
	})
	if err != nil {
		t.Fatalf("stored ACL of %q: %v", channel, err)
	}
	return acl
}

func jsonPost(t *testing.T, path, body string) *http.Request {
	t.Helper()
	return newReq(t, "POST", path, strings.NewReader(body))
}

func TestCreateChannelACLNilVersusEmpty(t *testing.T) {
	e := newEnv(t, envOptions{})
	e.identity("pat", false)

	resp := e.raw("pat", jsonPost(t, "/v1/channels", `{"name":"dflt"}`))
	if resp.StatusCode != 201 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("absent acl: %d %s", resp.StatusCode, b)
	}
	if got := len(storedACL(t, e, "dflt")); got != 3 {
		t.Fatalf("absent acl stored %d rows, want the default 3", got)
	}

	resp = e.raw("pat", jsonPost(t, "/v1/channels", `{"name":"none","acl":[]}`))
	if resp.StatusCode != 201 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("empty acl: %d %s", resp.StatusCode, b)
	}
	if got := storedACL(t, e, "none"); len(got) != 0 {
		t.Fatalf("empty acl stored %+v, want none", got)
	}
}

func TestCreateChannelIdempotencyHeader(t *testing.T) {
	e := newEnv(t, envOptions{})
	e.identity("pat", false)
	c := e.client("pat")
	ctx := context.Background()
	k := "key-1"
	params := &gen.CreateChannelParams{IdempotencyKey: &k}

	r1, err := c.CreateChannelWithResponse(ctx, params, gen.CreateChannelRequest{Name: "idem"})
	if err != nil || r1.JSON201 == nil {
		t.Fatalf("first: %v %s", err, r1.Body)
	}
	r2, err := c.CreateChannelWithResponse(ctx, params, gen.CreateChannelRequest{Name: "idem"})
	if err != nil || r2.JSON201 == nil {
		t.Fatalf("replay: %v %s", err, r2.Body)
	}
	if !reflect.DeepEqual(*r1.JSON201, *r2.JSON201) {
		t.Fatalf("replay body %+v != %+v", *r2.JSON201, *r1.JSON201)
	}
	chs, err := c.ListChannelsWithResponse(ctx)
	if err != nil || chs.JSON200 == nil {
		t.Fatal(err)
	}
	n := 0
	for _, ch := range *chs.JSON200 {
		if ch.Name == "idem" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d channels named idem, want 1", n)
	}

	memory := gen.CreateChannelRequestKind("memory")
	r3, err := c.CreateChannelWithResponse(ctx, params, gen.CreateChannelRequest{Name: "idem", Kind: &memory})
	if err != nil {
		t.Fatal(err)
	}
	if r3.StatusCode() != 409 || r3.ApplicationproblemJSONDefault == nil || r3.ApplicationproblemJSONDefault.Code != gen.ProblemCode("conflict") {
		t.Fatalf("same key other kind: %d %s", r3.StatusCode(), r3.Body)
	}

	// No header: a second create of the same name is the core's conflict.
	r4, err := c.CreateChannelWithResponse(ctx, &gen.CreateChannelParams{}, gen.CreateChannelRequest{Name: "idem"})
	if err != nil {
		t.Fatal(err)
	}
	if r4.StatusCode() != 409 || r4.ApplicationproblemJSONDefault == nil || r4.ApplicationproblemJSONDefault.Code != gen.ProblemCode("conflict") {
		t.Fatalf("no header duplicate: %d %s", r4.StatusCode(), r4.Body)
	}
}

func TestACLWire(t *testing.T) {
	e := newEnv(t, envOptions{})
	e.identity("pat", false)
	e.identity("sam", false)
	ctx := context.Background()
	pat, sam := e.client("pat"), e.client("sam")
	if r, err := pat.CreateChannelWithResponse(ctx, &gen.CreateChannelParams{}, gen.CreateChannelRequest{Name: "acl"}); err != nil || r.JSON201 == nil {
		t.Fatalf("create: %v %s", err, r.Body)
	}

	resp := e.raw("pat", newReq(t, "PUT", "/v1/channels/acl/acl",
		strings.NewReader(`[{"identity":"pat","permissions":["owner"]}]`)))
	p := readProblem(t, resp)
	if resp.StatusCode != 400 || p.Code != gen.ProblemCode("validation") {
		t.Fatalf("owner permission: %d %+v", resp.StatusCode, p)
	}

	r, err := sam.GetACLWithResponse(ctx, "acl")
	if err != nil {
		t.Fatal(err)
	}
	if r.StatusCode() != 403 || r.ApplicationproblemJSONDefault == nil || r.ApplicationproblemJSONDefault.Code != gen.ProblemCode("forbidden") {
		t.Fatalf("non-admin get: %d %s", r.StatusCode(), r.Body)
	}

	g, err := pat.GetACLWithResponse(ctx, "acl")
	if err != nil || g.JSON200 == nil || len(*g.JSON200) != 3 {
		t.Fatalf("creator get: %v %s", err, g.Body)
	}
}

func TestDeleteAndRenameCarryAdminTag(t *testing.T) {
	spec := Spec()
	for _, op := range []string{"deleteChannel", "renameChannel"} {
		found := false
		for _, item := range spec.Paths.Map() {
			for _, o := range item.Operations() {
				if !strings.EqualFold(o.OperationID, op) { // the embedded copy capitalizes ids
					continue
				}
				found = true
				tags := map[string]bool{}
				for _, tg := range o.Tags {
					tags[tg] = true
				}
				if !tags["channels"] || !tags["admin"] {
					t.Errorf("%s tags = %v, want channels and admin", op, o.Tags)
				}
			}
		}
		if !found {
			t.Errorf("operation %s not in the document", op)
		}
	}

	e := newEnv(t, envOptions{})
	e.identity("pat", false)
	e.identity("root", true)
	resp := e.raw("pat", newReq(t, "GET", "/v1/admin/status", nil))
	p := readProblem(t, resp)
	if resp.StatusCode != 403 || p.Code != gen.ProblemCode("forbidden") {
		t.Fatalf("non-admin status: %d %+v", resp.StatusCode, p)
	}
	r, err := e.client("root").AdminStatusWithResponse(context.Background())
	if err != nil || r.JSON200 == nil {
		t.Fatalf("admin status: %v %s", err, r.Body)
	}
	if r.JSON200.Sessions == nil || r.JSON200.Channels == nil || r.JSON200.Notices == nil {
		t.Fatalf("status arrays must not be null: %s", r.Body)
	}
}

// pathRecorder records the method, escaped path and raw query of every request.
type pathRecorder struct {
	next  http.RoundTripper
	mu    sync.Mutex
	paths []string
}

func (p *pathRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	p.mu.Lock()
	p.paths = append(p.paths, req.Method+" "+req.URL.EscapedPath()+"?"+req.URL.RawQuery)
	p.mu.Unlock()
	return p.next.RoundTrip(req)
}

func TestTagSubscriptionsWire(t *testing.T) {
	e := newEnv(t, envOptions{})
	e.identity("pat", false)
	ctx := context.Background()
	rec := &pathRecorder{next: InProcessTransport(e.handler(), "pat")}
	c, err := gen.NewClientWithResponses("http://inproc", gen.WithHTTPClient(&http.Client{Transport: rec}))
	if err != nil {
		t.Fatal(err)
	}

	list := func() [][]string {
		t.Helper()
		r, err := c.ListTagSubscriptionsWithResponse(ctx)
		if err != nil || r.JSON200 == nil {
			t.Fatalf("list: %v %s", err, r.Body)
		}
		return *r.JSON200
	}
	sub := func(patterns ...string) {
		t.Helper()
		r, err := c.SubscribeTagsWithResponse(ctx, &gen.SubscribeTagsParams{}, gen.TagPatterns{Patterns: patterns})
		if err != nil || r.StatusCode() != 204 {
			t.Fatalf("subscribe %v: %v %d %s", patterns, err, r.StatusCode(), r.Body)
		}
	}

	sub("env:*", "deploy")
	if got, want := list(), [][]string{{"deploy", "env:*"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("list = %v, want %v", got, want)
	}
	r, err := c.UnsubscribeTagsWithResponse(ctx, &gen.UnsubscribeTagsParams{Pattern: []string{"env:*", "deploy"}})
	if err != nil || r.StatusCode() != 204 {
		t.Fatalf("unsubscribe: %v %d %s", err, r.StatusCode(), r.Body)
	}
	if got := list(); len(got) != 0 {
		t.Fatalf("after unsubscribe: %v", got)
	}
	rec.mu.Lock()
	var unsubPath string
	for _, p := range rec.paths {
		if strings.HasPrefix(p, "DELETE /v1/tag-subscriptions?") {
			unsubPath = p
		}
	}
	rec.mu.Unlock()
	if want := "DELETE /v1/tag-subscriptions?pattern=env%3A%2A&pattern=deploy"; unsubPath != want {
		t.Fatalf("generated client request = %q, want %q", unsubPath, want)
	}

	sub("env:*", "deploy")
	resp := e.raw("pat", newReq(t, "DELETE", "/v1/tag-subscriptions?pattern=deploy&pattern=env:*", nil))
	if resp.StatusCode != 204 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("literal form: %d %s", resp.StatusCode, b)
	}
	if got := list(); len(got) != 0 {
		t.Fatalf("after literal unsubscribe: %v", got)
	}

	resp = e.raw("pat", jsonPost(t, "/v1/tag-subscriptions", `{"patterns":["Bad Tag!"]}`))
	if p := readProblem(t, resp); resp.StatusCode != 400 || p.Code != gen.ProblemCode("validation") {
		t.Fatalf("bad pattern: %d %+v", resp.StatusCode, p)
	}
}

// TestUnsubscribeTagsCommaAgrees pins that a comma inside one pattern does not
// split on the wire (Milestone 4 item 6, resolved by the repeated query
// parameter): the remote Bus's ["a,b"] reaches the core as the one pattern
// "a,b", so both transports answer validation (the tag rule refuses the
// comma) and neither removes the [a b] set.
func TestUnsubscribeTagsCommaAgrees(t *testing.T) {
	e := newEnv(t, envOptions{})
	e.identity("pat", false)
	ctx := context.Background()
	in := ab2bus.TagSubscribeInput{Patterns: []string{"a,b"}}
	sets := func(b ab2bus.Bus) [][]string {
		t.Helper()
		got, err := b.TagSubscriptions(ctx)
		if err != nil {
			t.Fatalf("TagSubscriptions: %v", err)
		}
		return got
	}

	lb := local.New(e.core, "pat")
	if err := lb.SubscribeTags(ctx, ab2bus.TagSubscribeInput{Patterns: []string{"a", "b"}}); err != nil {
		t.Fatalf("SubscribeTags: %v", err)
	}
	if err := lb.UnsubscribeTags(ctx, in); ab2bus.CodeOf(err) != ab2bus.CodeValidation {
		t.Fatalf("local UnsubscribeTags %v = %v, want validation", in.Patterns, err)
	}
	if got, want := sets(lb), [][]string{{"a", "b"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after local: %v, want %v", got, want)
	}

	rb := remote.New(e.client("pat"))
	if err := rb.UnsubscribeTags(ctx, in); ab2bus.CodeOf(err) != ab2bus.CodeValidation {
		t.Fatalf("remote UnsubscribeTags %v = %v, want validation", in.Patterns, err)
	}
	if got, want := sets(rb), [][]string{{"a", "b"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after remote: %v, want %v", got, want)
	}
}

// TestUnsubscribeTagsCommaIsOnePattern pins the wire: each pattern is its own
// pattern query value, so a comma stays inside one pattern, and an absent
// pattern is refused by the request validator.
func TestUnsubscribeTagsCommaIsOnePattern(t *testing.T) {
	e := newEnv(t, envOptions{})
	e.identity("pat", false)
	ctx := context.Background()
	rec := &pathRecorder{next: InProcessTransport(e.handler(), "pat")}
	c, err := gen.NewClientWithResponses("http://inproc", gen.WithHTTPClient(&http.Client{Transport: rec}))
	if err != nil {
		t.Fatal(err)
	}
	r, err := c.UnsubscribeTagsWithResponse(ctx, &gen.UnsubscribeTagsParams{Pattern: []string{"a,b"}})
	if err != nil {
		t.Fatal(err)
	}
	if r.StatusCode() != 400 || r.ApplicationproblemJSONDefault == nil || r.ApplicationproblemJSONDefault.Code != gen.ProblemCode("validation") {
		t.Fatalf("comma pattern: %d %s, want 400 validation about the one pattern", r.StatusCode(), r.Body)
	}
	if !strings.Contains(string(r.Body), "a,b") {
		t.Fatalf("the core did not see the one pattern a,b: %s", r.Body)
	}
	rec.mu.Lock()
	got := rec.paths[len(rec.paths)-1]
	rec.mu.Unlock()
	if want := "DELETE /v1/tag-subscriptions?pattern=a%2Cb"; got != want {
		t.Fatalf("request = %q, want %q", got, want)
	}
	resp := e.raw("pat", newReq(t, "DELETE", "/v1/tag-subscriptions", nil))
	if p := readProblem(t, resp); resp.StatusCode != 400 || p.Code != gen.ProblemCode("validation") {
		t.Fatalf("no pattern: %d %+v", resp.StatusCode, p)
	}
}

// TestChannelCapabilitiesOnTheWire: every response that carries a channel
// has "capabilities":[] (never null, never absent), and the list is filtered
// by ACL entry.
func TestChannelCapabilitiesOnTheWire(t *testing.T) {
	e := newEnv(t, envOptions{})
	e.identity("pat", false)
	e.identity("kim", false)
	e.identity("root", true)
	resp := e.raw("pat", jsonPost(t, "/v1/channels", `{"name":"open"}`))
	if resp.StatusCode != 201 {
		t.Fatalf("create open: %d", resp.StatusCode)
	}
	created := decodeChannels(t, resp, false)
	resp = e.raw("pat", jsonPost(t, "/v1/channels", `{"name":"private","acl":[{"identity":"pat","permissions":["read","write","admin"]}]}`))
	if resp.StatusCode != 201 {
		t.Fatalf("create private: %d", resp.StatusCode)
	}
	created = append(created, decodeChannels(t, resp, false)...)
	list := decodeChannels(t, e.raw("pat", newReq(t, "GET", "/v1/channels", nil)), true)
	status := decodeChannels(t, e.raw("root", newReq(t, "GET", "/v1/admin/status", nil)), false)
	for name, cs := range map[string][]map[string]json.RawMessage{"create": created, "list": list, "status": status} {
		if len(cs) == 0 {
			t.Fatalf("%s carries no channel", name)
		}
		for _, c := range cs {
			if got := string(c["capabilities"]); got != "[]" {
				t.Errorf("%s: capabilities = %q, want []", name, got)
			}
		}
	}
	other := decodeChannels(t, e.raw("kim", newReq(t, "GET", "/v1/channels", nil)), true)
	for _, c := range other {
		if string(c["name"]) == `"private"` {
			t.Errorf("kim lists private: %s", c["name"])
		}
	}
}

// decodeChannels reads a channel object, an array of them (array) or the
// channels of an admin status document.
func decodeChannels(t *testing.T, resp *http.Response, array bool) []map[string]json.RawMessage {
	t.Helper()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if array {
		var out []map[string]json.RawMessage
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatalf("decode list %s: %v", body, err)
		}
		return out
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	if raw, ok := obj["channels"]; ok {
		var out []map[string]json.RawMessage
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("decode channels %s: %v", raw, err)
		}
		return out
	}
	return []map[string]json.RawMessage{obj}
}
