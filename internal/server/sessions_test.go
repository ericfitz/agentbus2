package server

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/bus/remote"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/channels"
	"github.com/ericfitz/agentbus2/internal/core/messaging"
	"github.com/ericfitz/agentbus2/internal/gen"
	"github.com/ericfitz/agentbus2/internal/store"
)

// sessionEnv is an env with identity pat, one open session and a "dev"
// channel the session is subscribed to.
type sessionEnv struct {
	*env
	session gen.Session
}

func newSessionEnv(t *testing.T, cfg func(*core.Config)) *sessionEnv {
	t.Helper()
	e := newEnv(t, envOptions{cfg: func(c *core.Config) {
		c.WatchMergeSeconds = 0
		if cfg != nil {
			cfg(c)
		}
	}})
	e.identity("pat", false)
	e.identity("sam", false)
	ctx := context.Background()
	if _, err := (channels.Service{Core: e.core}).Create(ctx, "pat", ab2bus.CreateChannelInput{Name: "dev"}); err != nil {
		t.Fatalf("create dev: %v", err)
	}
	r, err := e.client("pat").OpenSessionWithResponse(ctx, gen.OpenSessionJSONRequestBody{Hint: ptrTo("impl")})
	if err != nil || r.JSON201 == nil {
		t.Fatalf("open: %v %v", err, r)
	}
	se := &sessionEnv{env: e, session: *r.JSON201}
	if err := (channels.Service{Core: e.core}).Subscribe(ctx, "pat", ab2bus.SubscribeInput{Channel: "dev"}); err != nil {
		t.Fatalf("subscribe dev: %v", err)
	}
	return se
}

func ptrTo[T any](v T) *T { return &v }

func (s *sessionEnv) send(from, session string) int64 {
	s.t.Helper()
	r, err := (messaging.Service{Core: s.core}).Send(context.Background(), from,
		ab2bus.SendInput{Channel: "dev", Content: "hi", Session: session})
	if err != nil {
		s.t.Fatalf("send: %v", err)
	}
	return r.Seq
}

// pending Eric: Milestone 4 item 4 (watch timeout as fractional seconds).
func TestWatchTimeoutWire(t *testing.T) {
	s := newSessionEnv(t, func(c *core.Config) { c.WatchMaxSeconds = 1 })
	ctx := context.Background()
	cl := s.client("pat")
	id := s.session.Id

	start := time.Now()
	r, err := cl.WatchWithResponse(ctx, id, &gen.WatchParams{Timeout: ptrTo(600.0)})
	if err != nil || r.StatusCode() != 204 {
		t.Fatalf("timeout=600: %v %v", err, r)
	}
	if d := time.Since(start); d < time.Second || d > 1500*time.Millisecond {
		t.Errorf("timeout=600 took %v, want between 1s and 1.5s", d)
	}

	r, err = cl.WatchWithResponse(ctx, id, &gen.WatchParams{Timeout: ptrTo(-1.0)})
	if err != nil || r.StatusCode() != 400 || r.ApplicationproblemJSONDefault == nil || r.ApplicationproblemJSONDefault.Code != "validation" {
		t.Fatalf("timeout=-1: %v %s", err, r.Body)
	}

	start = time.Now()
	r, err = cl.WatchWithResponse(ctx, id, &gen.WatchParams{Timeout: ptrTo(0.2)})
	if err != nil || r.StatusCode() != 204 {
		t.Fatalf("timeout=0.2: %v %v", err, r)
	}
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Errorf("timeout=0.2 took %v", d)
	}

	start = time.Now()
	r, err = cl.WatchWithResponse(ctx, id, &gen.WatchParams{})
	if err != nil || r.StatusCode() != 204 {
		t.Fatalf("no params: %v %v", err, r)
	}
	if d := time.Since(start); d < time.Second {
		t.Errorf("absent timeout returned after %v, want the cap", d)
	}

	req := newReq(t, "GET", "/v1/sessions/"+id+"/watch?timeout=abc", nil)
	resp := s.raw("pat", req)
	p := readProblem(t, resp)
	if resp.StatusCode != 400 || p.Code != "validation" {
		t.Fatalf("timeout=abc: %d %v", resp.StatusCode, p)
	}
}

func TestWatchWire200And409(t *testing.T) {
	s := newSessionEnv(t, nil)
	ctx := context.Background()
	cl := s.client("pat")

	go func() {
		time.Sleep(100 * time.Millisecond)
		s.send("sam", "")
	}()
	r, err := cl.WatchWithResponse(ctx, s.session.Id, &gen.WatchParams{Timeout: ptrTo(5.0)})
	if err != nil || r.JSON200 == nil {
		t.Fatalf("watch: %v %v", err, r)
	}
	if r.JSON200.Seq < 1 || r.JSON200.Count != 1 || len(r.JSON200.Channels) != 1 || r.JSON200.Channels[0] != "dev" {
		t.Fatalf("wake %+v", *r.JSON200)
	}

	first := make(chan *gen.WatchResponse, 1)
	go func() {
		r, _ := cl.WatchWithResponse(ctx, s.session.Id, &gen.WatchParams{After: ptrTo(r.JSON200.Seq), Timeout: ptrTo(10.0)})
		first <- r
	}()
	time.Sleep(200 * time.Millisecond)
	second := make(chan *gen.WatchResponse, 1)
	go func() {
		r, _ := cl.WatchWithResponse(ctx, s.session.Id, &gen.WatchParams{After: ptrTo(r.JSON200.Seq), Timeout: ptrTo(0.5)})
		second <- r
	}()
	select {
	case r1 := <-first:
		if r1 == nil || r1.StatusCode() != 409 || r1.ApplicationproblemJSON409 == nil || r1.ApplicationproblemJSON409.Code != "replaced" {
			t.Fatalf("first watch: %v", r1)
		}
		if ct := r1.HTTPResponse.Header.Get("Content-Type"); ct != "application/problem+json" {
			t.Errorf("content type %q", ct)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first watch was not replaced")
	}
	<-second
}

func TestWatchIncludeOwnWire(t *testing.T) {
	s := newSessionEnv(t, nil)
	ctx := context.Background()
	cl := s.client("pat")
	for _, own := range []bool{false, true} {
		var params gen.WatchParams
		params.Timeout = ptrTo(0.5)
		if own {
			params.IncludeOwn = ptrTo(true)
		}
		params.After = ptrTo(s.send("sam", "")) // a floor: earlier messages do not wake
		go func() {
			time.Sleep(100 * time.Millisecond)
			s.send("pat", s.session.Id)
		}()
		r, err := cl.WatchWithResponse(ctx, s.session.Id, &params)
		if err != nil {
			t.Fatal(err)
		}
		if own {
			if r.JSON200 == nil || r.JSON200.Count != 1 {
				t.Fatalf("include_own=true: %d %s", r.StatusCode(), r.Body)
			}
		} else if r.StatusCode() != 204 {
			t.Fatalf("include_own absent: %d %s", r.StatusCode(), r.Body)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (s *sessionEnv) sessionRow() (row store.Session, err error) {
	err = s.store.Tx(context.Background(), func(ctx context.Context, tx store.Tx) error {
		row, err = tx.Session(ctx, s.session.Id)
		return err
	})
	return row, err
}

// recordingHandler notes when the wrapped handler returns.
type recordingHandler struct {
	next http.Handler
	done chan struct{}
	once sync.Once
}

func (h *recordingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.next.ServeHTTP(w, r)
	h.once.Do(func() { close(h.done) })
}

func TestWatchClientGoneReleasesHandler(t *testing.T) {
	s := newSessionEnv(t, nil)
	rec := &recordingHandler{next: s.handler(), done: make(chan struct{})}
	cl, err := gen.NewClientWithResponses("http://inproc",
		gen.WithHTTPClient(&http.Client{Transport: InProcessTransport(rec, "pat")}))
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.sessionRow()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _ = cl.WatchWithResponse(ctx, s.session.Id, &gen.WatchParams{Timeout: ptrTo(30.0)})
	select {
	case <-rec.done:
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("handler still running %v after the client went away", time.Since(start))
	}
	after, err := s.sessionRow()
	if err != nil {
		t.Fatal(err)
	}
	if after.WatchID == "" || after.WatchID == before.WatchID {
		t.Fatalf("the watch left no registration: before %q after %q", before.WatchID, after.WatchID)
	}
	again, err := s.sessionRow()
	if err != nil || again.WatchID != after.WatchID {
		t.Fatalf("WatchID changed after the handler returned: %q then %q (%v)", after.WatchID, again.WatchID, err)
	}
	r, err := s.client("pat").WatchWithResponse(context.Background(), s.session.Id, &gen.WatchParams{Timeout: ptrTo(0.2)})
	if err != nil || r.StatusCode() != 204 {
		t.Fatalf("following watch: %v %v", err, r)
	}
}

func TestReceiveIsNonBlocking(t *testing.T) {
	s := newSessionEnv(t, nil)
	start := time.Now()
	r, err := s.client("pat").ReceiveWithResponse(context.Background(), s.session.Id, gen.ReceiveJSONRequestBody{})
	if err != nil || r.JSON200 == nil {
		t.Fatalf("receive: %v %v", err, r)
	}
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Errorf("empty receive took %v", d)
	}
	if len(r.JSON200.Messages) != 0 {
		t.Errorf("messages %v", r.JSON200.Messages)
	}
	if r.JSON200.Messages == nil || r.JSON200.Gaps == nil {
		t.Errorf("messages and gaps must be arrays, not null")
	}
}

func TestOpenCloseReceiveRoundTrip(t *testing.T) {
	s := newSessionEnv(t, nil)
	ctx := context.Background()
	cl := s.client("pat")
	if s.session.DisplayName != "pat/impl" {
		t.Errorf("display name %q", s.session.DisplayName)
	}
	if len(s.session.ResumeToken) != 43 {
		t.Errorf("token %q is %d chars, want 43", s.session.ResumeToken, len(s.session.ResumeToken))
	}
	seq := s.send("sam", "")
	r, err := cl.ReceiveWithResponse(ctx, s.session.Id, gen.ReceiveJSONRequestBody{})
	if err != nil || r.JSON200 == nil || len(r.JSON200.Messages) != 1 || r.JSON200.Messages[0].Seq != seq {
		t.Fatalf("receive: %v %s", err, r.Body)
	}
	if r.JSON200.Messages[0].Channel != "dev" || r.JSON200.Messages[0].Sender != "sam" {
		t.Errorf("message %+v", r.JSON200.Messages[0])
	}
	r2, err := cl.ReceiveWithResponse(ctx, s.session.Id, gen.ReceiveJSONRequestBody{Ack: &r.JSON200.Batch})
	if err != nil || r2.JSON200 == nil || len(r2.JSON200.Messages) != 0 {
		t.Fatalf("receive with ack: %v %s", err, r2.Body)
	}
	c, err := cl.CloseSessionWithResponse(ctx, s.session.Id, &gen.CloseSessionParams{})
	if err != nil || c.StatusCode() != 204 {
		t.Fatalf("close: %v %v", err, c)
	}
	r3, err := cl.ReceiveWithResponse(ctx, s.session.Id, gen.ReceiveJSONRequestBody{})
	if err != nil || r3.StatusCode() != 410 || r3.ApplicationproblemJSONDefault == nil || r3.ApplicationproblemJSONDefault.Code != "session_closed" {
		t.Fatalf("receive after close: %v %s", err, r3.Body)
	}
}

func TestReceiveNoIdempotencyKey(t *testing.T) {
	s := newSessionEnv(t, nil)
	ctx := context.Background()
	cl := s.client("pat")
	s.send("sam", "")
	withKey := func(ctx context.Context, req *http.Request) error {
		req.Header.Set("Idempotency-Key", "k1")
		return nil
	}
	a, err := cl.ReceiveWithResponse(ctx, s.session.Id, gen.ReceiveJSONRequestBody{}, withKey)
	if err != nil || a.JSON200 == nil {
		t.Fatalf("first: %v %s", err, a.Body)
	}
	b, err := cl.ReceiveWithResponse(ctx, s.session.Id, gen.ReceiveJSONRequestBody{}, withKey)
	if err != nil || b.JSON200 == nil {
		t.Fatalf("second: %v %s", err, b.Body)
	}
	if a.JSON200.Batch != b.JSON200.Batch || !b.JSON200.Redelivered || len(b.JSON200.Messages) != 1 {
		t.Fatalf("not redelivered under the same token: %+v then %+v", *a.JSON200, *b.JSON200)
	}
}

func TestIdentitiesWire(t *testing.T) {
	s := newSessionEnv(t, nil)
	s.identity("root", true)
	ctx := context.Background()
	if r, err := s.client("root").OpenSessionWithResponse(ctx, gen.OpenSessionJSONRequestBody{}); err != nil || r.JSON201 == nil {
		t.Fatalf("open root: %v", err)
	}
	r, err := s.client("pat").ListIdentitiesWithResponse(ctx)
	if err != nil || r.JSON200 == nil {
		t.Fatalf("identities: %v %s", err, r.Body)
	}
	got := map[string]gen.Identity{}
	for _, i := range *r.JSON200 {
		got[i.Name] = i
	}
	if len(got) != 2 || len(got["pat"].Sessions) != 1 || got["pat"].Admin || !got["root"].Admin || len(got["root"].Sessions) != 1 {
		t.Fatalf("identities %+v", got)
	}
	if got["pat"].Sessions[0].Id != s.session.Id || got["pat"].Sessions[0].DisplayName != "pat/impl" {
		t.Errorf("pat session %+v", got["pat"].Sessions[0])
	}
}

// countingTransport counts requests that reach the wrapped transport.
type countingTransport struct {
	next http.RoundTripper
	n    atomic.Int64
}

func (c *countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.n.Add(1)
	return c.next.RoundTrip(r)
}

func TestSessionOddIdsOnRemote(t *testing.T) {
	s := newSessionEnv(t, nil)
	ct := &countingTransport{next: InProcessTransport(s.handler(), "pat")}
	cl, err := gen.NewClientWithResponses("http://inproc", gen.WithHTTPClient(&http.Client{Transport: ct}))
	if err != nil {
		t.Fatal(err)
	}
	b := remote.New(cl)
	ctx := context.Background()

	err = b.Close(ctx, ab2bus.CloseSessionInput{Session: ""})
	if ab2bus.CodeOf(err) != ab2bus.CodeNotFound || ct.n.Load() != 1 {
		t.Fatalf("Close empty: %v (requests %d)", err, ct.n.Load())
	}
	_, err = b.Receive(ctx, ab2bus.ReceiveInput{Session: "a/b"})
	if ab2bus.CodeOf(err) != ab2bus.CodeNotFound || ct.n.Load() != 2 {
		t.Fatalf("Receive a/b: %v (requests %d)", err, ct.n.Load())
	}

	req := newReq(t, "DELETE", "/v1/sessions/", nil)
	resp := s.raw("pat", req)
	p := readProblem(t, resp)
	if resp.StatusCode != 404 || p.Code != "not_found" {
		t.Fatalf("raw DELETE /v1/sessions/: %d %v", resp.StatusCode, p)
	}
}
