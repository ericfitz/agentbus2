package tui

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/auth/keys"
	"github.com/ericfitz/agentbus2/internal/auth/token"
	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/bus/remote"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/gen"
	"github.com/ericfitz/agentbus2/internal/server"
)

// countingHandler counts the long polls (requests whose path ends in /watch)
// that reach a leg's server (a test-local copy of the conformance suite's).
type countingHandler struct {
	http.Handler
	watches atomic.Int64
}

func (c *countingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, "/watch") {
		c.watches.Add(1)
	}
	c.Handler.ServeHTTP(w, r)
}

// lockedBuf is the program's Output: written by Bubble Tea's goroutine, read
// by the test.
type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

type runResult struct {
	m   Model
	err error
}

// runHandle is one run in a goroutine with a piped Input.
type runHandle struct {
	in     *io.PipeWriter
	out    *lockedBuf
	done   chan runResult
	cancel context.CancelFunc
}

// startRun starts run over b as pat. The cancel is registered before the
// goroutine starts, so a t.Fatal later in the test ends the run.
func startRun(t *testing.T, b bus.Bus, w, h int) *runHandle {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })
	rh := &runHandle{in: pw, out: &lockedBuf{}, done: make(chan runResult, 1), cancel: cancel}
	go func() {
		m, err := run(ctx, Options{Bus: b, Identity: "pat", Input: pr, Output: rh.out, Stderr: io.Discard, Width: w, Height: h})
		rh.done <- runResult{m, err}
	}()
	return rh
}

func (r *runHandle) write(t *testing.T, s string) {
	t.Helper()
	if _, err := r.in.Write([]byte(s)); err != nil {
		t.Fatalf("write %q: %v", s, err)
	}
}

func (r *runHandle) wait(t *testing.T, d time.Duration) runResult {
	t.Helper()
	select {
	case res := <-r.done:
		return res
	case <-time.After(d):
		t.Fatalf("run did not return within %v", d)
		return runResult{}
	}
}

// lists reports whether b sees a live session displayed as name.
func lists(b bus.Bus, name string) bool {
	ids, err := b.Identities(context.Background())
	if err != nil {
		return false
	}
	for _, id := range ids {
		for _, s := range id.Sessions {
			if s.DisplayName == name {
				return true
			}
		}
	}
	return false
}

// samLoop is an agent-style session on a bus that records every message it
// receives, acking each batch.
type samLoop struct {
	mu   sync.Mutex
	msgs []bus.Message
}

func (s *samLoop) has(ch, content string) (bus.Message, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.msgs {
		if m.Channel == ch && m.Content == content {
			return m, true
		}
	}
	return bus.Message{}, false
}

func startSamLoop(t *testing.T, b bus.Bus) *samLoop {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	sess, err := b.Open(ctx, bus.OpenSessionInput{})
	if err != nil {
		t.Fatal(err)
	}
	sl := &samLoop{}
	go func() {
		var floor int64
		prev := ""
		for ctx.Err() == nil {
			wr, err := b.Watch(ctx, bus.WatchInput{Session: sess.ID, After: floor})
			if err != nil {
				time.Sleep(20 * time.Millisecond)
				continue
			}
			if !wr.Woke {
				continue
			}
			floor = max(floor, wr.Seq)
			for ctx.Err() == nil {
				res, err := b.Receive(ctx, bus.ReceiveInput{Session: sess.ID, Ack: prev})
				if err != nil {
					break
				}
				prev = res.Batch
				sl.mu.Lock()
				sl.msgs = append(sl.msgs, res.Messages...)
				sl.mu.Unlock()
				if len(res.Messages) == 0 {
					prev = ""
					break
				}
			}
		}
	}()
	return sl
}

// TestBothModes drives the one program over a remote.Bus in the two modes of
// the product: Decision 35's in-process transport and a TCP listener with a
// server-minted bearer. Each leg has its own core.
func TestBothModes(t *testing.T) {
	legs := []struct {
		name string
		open func(t *testing.T, c *core.Core) (bus.Bus, *countingHandler)
	}{
		{"local", func(t *testing.T, c *core.Core) (bus.Bus, *countingHandler) {
			h := &countingHandler{Handler: server.New(c, server.Options{Auth: server.InProcess{}})}
			gc, err := gen.NewClientWithResponses("http://inproc", gen.WithHTTPClient(&http.Client{Transport: server.InProcessTransport(h, "pat")}))
			if err != nil {
				t.Fatal(err)
			}
			return remote.New(gc), h
		}},
		{"server", func(t *testing.T, c *core.Core) (bus.Bus, *countingHandler) {
			srv := httptest.NewUnstartedServer(nil)
			url := "http://" + srv.Listener.Addr().String()
			key, err := keys.Generate(keys.EdDSA)
			if err != nil {
				t.Fatal(err)
			}
			iss, err := token.NewIssuer(url, key, time.Hour, time.Now)
			if err != nil {
				t.Fatal(err)
			}
			h := &countingHandler{Handler: server.New(c, server.Options{Auth: server.Bearer{Verifier: token.Verifier{Own: iss}}, Issuer: iss})}
			srv.Config.Handler = h
			srv.Start()
			t.Cleanup(srv.Close)
			tok, _, err := iss.Mint("pat")
			if err != nil {
				t.Fatal(err)
			}
			gc, err := gen.NewClientWithResponses(url, gen.WithRequestEditorFn(remote.WithBearer(tok)))
			if err != nil {
				t.Fatal(err)
			}
			return remote.New(gc), h
		}},
	}
	loaded := regexp.MustCompile(`general.*loaded`)
	for _, leg := range legs {
		t.Run(leg.name, func(t *testing.T) {
			ctx := context.Background()
			c := newCore(t)
			ensureIdentity(t, c, "pat")
			sam := identityBus(t, c, "sam")
			if err := sam.Subscribe(ctx, bus.SubscribeInput{Channel: "general", From: "oldest"}); err != nil {
				t.Fatal(err)
			}
			sl := startSamLoop(t, sam)
			// The probe subscribes pat from the floor so the TUI's own
			// subscribe from "now" is a no-op and the acked mark stays put.
			probe := identityBus(t, c, "pat")
			if err := probe.Subscribe(ctx, bus.SubscribeInput{Channel: "general", From: "oldest"}); err != nil {
				t.Fatal(err)
			}
			b, h := leg.open(t, c)
			rh := startRun(t, b, 80, 24)

			waitFor(t, 2*time.Second, func() bool { return lists(sam, "pat/tui") })
			if _, err := sam.Send(ctx, bus.SendInput{Channel: "general", Content: "hello from sam"}); err != nil {
				t.Fatal(err)
			}
			waitFor(t, 5*time.Second, func() bool { return pendingFor(t, probe, "general") == 0 })

			waitFor(t, 2*time.Second, func() bool { return loaded.MatchString(stripANSI(rh.out.String())) })
			rh.write(t, "i")
			rh.write(t, "hello from the tui\r")
			var got bus.Message
			waitFor(t, 2*time.Second, func() bool {
				m, ok := sl.has("general", "hello from the tui")
				got = m
				return ok
			})
			if got.Display != "pat/tui" {
				t.Fatalf("Display = %q, want pat/tui", got.Display)
			}

			rh.write(t, "\x03")
			res := rh.wait(t, 2*time.Second)
			if res.err != nil {
				t.Fatalf("run: %v", res.err)
			}
			found := false
			for _, m := range res.m.msgs["general"] {
				if m.Content == "hello from sam" {
					found = true
				}
			}
			if !found {
				t.Fatalf("final model lacks sam's message: %v", res.m.msgs["general"])
			}
			if res.m.lastBatchAt.IsZero() {
				t.Fatal("no batch reached the model")
			}
			if lists(sam, "pat/tui") {
				t.Fatal("pat/tui still listed after quit")
			}
			if h.watches.Load() < 1 {
				t.Fatal("no /watch request crossed this leg's handler")
			}
		})
	}
}

func TestRunQuitsOnCanceledContext(t *testing.T) {
	c := newCore(t)
	sam := identityBus(t, c, "sam")
	ensureIdentity(t, c, "pat")
	rh := startRun(t, identityBus(t, c, "pat"), 80, 24)
	waitFor(t, 2*time.Second, func() bool { return lists(sam, "pat/tui") })
	rh.cancel()
	res := rh.wait(t, time.Second)
	if res.err != nil {
		t.Fatalf("run after cancel: %v", res.err)
	}
	waitFor(t, time.Second, func() bool { return !lists(sam, "pat/tui") })
}

func TestRunRejectsMissingOptions(t *testing.T) {
	c := newCore(t)
	sam := identityBus(t, c, "sam")
	b := identityBus(t, c, "pat")
	before, err := sam.Identities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		o    Options
		want string
	}{
		{Options{}, "Bus"},
		{Options{Bus: b}, "Identity"},
	} {
		err := Run(context.Background(), tc.o)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("Run(%+v) = %v, want an error naming %s", tc.o, err, tc.want)
		}
	}
	after, err := sam.Identities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("identities changed: %v -> %v", before, after)
	}
	for i := range before {
		if len(before[i].Sessions) != len(after[i].Sessions) {
			t.Fatalf("sessions changed for %s", before[i].Name)
		}
	}
}

func TestRunHeadlessSizeRenders(t *testing.T) {
	c := newCore(t)
	sam := identityBus(t, c, "sam")
	ensureIdentity(t, c, "pat")
	rh := startRun(t, identityBus(t, c, "pat"), 80, 24)
	waitFor(t, time.Second, func() bool { return strings.Contains(rh.out.String(), "agentbus2 v") })
	rh.write(t, "\x03")
	rh.wait(t, 2*time.Second)

	// Negative half: with no size nothing is drawn.
	rh = startRun(t, identityBus(t, c, "pat"), 0, 0)
	waitFor(t, 2*time.Second, func() bool { return lists(sam, "pat/tui") })
	rh.write(t, "\x03")
	rh.wait(t, 2*time.Second)
	if strings.Contains(rh.out.String(), "agentbus2 v") {
		t.Fatalf("status bar drawn without a size: %q", rh.out.String())
	}
}

// slowListBus parks ListChannels until the call's context ends and then
// fails the way a canceled transport call does: a retryable internal error.
type slowListBus struct {
	bus.Bus
	entered chan struct{}
}

func (s *slowListBus) ListChannels(ctx context.Context) ([]bus.Channel, error) {
	close(s.entered)
	<-ctx.Done()
	return nil, bus.Internal(ctx.Err())
}

// A cancel that lands during start-up (after Open, inside a bus call) is a
// clean quit, and the opened session is closed.
func TestRunQuitsOnCancelDuringStartup(t *testing.T) {
	c := newCore(t)
	sam := identityBus(t, c, "sam")
	ensureIdentity(t, c, "pat")
	sb := &slowListBus{Bus: identityBus(t, c, "pat"), entered: make(chan struct{})}
	rh := startRun(t, sb, 80, 24)
	select {
	case <-sb.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("start-up never reached ListChannels")
	}
	rh.cancel()
	res := rh.wait(t, 2*time.Second)
	if res.err != nil {
		t.Fatalf("run after cancel: %v", res.err)
	}
	waitFor(t, time.Second, func() bool { return !lists(sam, "pat/tui") })
}
