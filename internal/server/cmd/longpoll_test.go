package cmd

import (
	"context"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/notify"
	"github.com/ericfitz/agentbus2/internal/gen"
	"github.com/ericfitz/agentbus2/internal/store"
)

// countingNotifier counts live subscriptions: a watch handler subscribes on
// entry and cancels on return, so zero live means every handler returned.
type countingNotifier struct {
	notify.Notifier
	live atomic.Int64
}

func (n *countingNotifier) Subscribe() (<-chan notify.Hint, func()) {
	ch, cancel := n.Notifier.Subscribe()
	n.live.Add(1)
	var done atomic.Bool
	return ch, func() {
		if done.CompareAndSwap(false, true) {
			n.live.Add(-1)
		}
		cancel()
	}
}

func startLongPoll(t *testing.T, cfg *core.Config, readTimeout time.Duration, n notify.Notifier) (addr string, s store.Store) {
	t.Helper()
	db := filepath.Join(t.TempDir(), "bus.sqlite")
	seedIdentities(t, db, "pat")
	s = openSQLite(t, db)
	addr, _ = startServe(t, Config{Store: s, Auth: headerAuth{}, Cfg: cfg, ReadTimeout: readTimeout, Notifier: n})
	return addr, s
}

func TestWatchLongPollSurvivesReadTimeout(t *testing.T) {
	cfg := core.Defaults()
	cfg.WatchMaxSeconds = 2
	addr, _ := startLongPoll(t, &cfg, time.Second, nil)
	c := identityClient(t, addr, "pat")
	session := openSessionID(t, c)

	start := time.Now()
	r, err := c.WatchWithResponse(context.Background(), session, &gen.WatchParams{})
	if err != nil {
		t.Fatalf("watch: transport error: %v", err)
	}
	d := time.Since(start)
	if r.StatusCode() != http.StatusNoContent {
		t.Fatalf("status %d body %s, want 204", r.StatusCode(), r.Body)
	}
	if d < 2*time.Second || d > 2500*time.Millisecond {
		t.Fatalf("watch took %v, want between 2 s and 2.5 s", d)
	}
}

func TestWatchClientDisconnectOverTCP(t *testing.T) {
	n := &countingNotifier{Notifier: notify.NewMemory()}
	addr, s := startLongPoll(t, nil, 0, n)
	session := openSessionID(t, identityClient(t, addr, "pat"))

	watchID := func() string {
		var id string
		err := s.Read(context.Background(), func(ctx context.Context, tx store.Tx) error {
			sess, err := tx.Session(ctx, session)
			id = sess.WatchID
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}

	hc := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	c := identityClient(t, addr, "pat", gen.WithHTTPClient(hc))
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	timeout := 5.0
	if _, err := c.WatchWithResponse(ctx, session, &gen.WatchParams{Timeout: &timeout}); err == nil {
		t.Fatal("watch: want a context error after the client gave up")
	}
	registered := watchID()
	if registered == "" {
		t.Fatal("the canceled watch never registered")
	}
	deadline := time.Now().Add(time.Second)
	for n.live.Load() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("watch handler still holds %d subscription(s) 1 s after the disconnect", n.live.Load())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := watchID(); got != registered {
		t.Fatalf("WatchID changed from %q to %q without a new watch", registered, got)
	}
	short := 0.2
	r, err := identityClient(t, addr, "pat").WatchWithResponse(context.Background(), session, &gen.WatchParams{Timeout: &short})
	if err != nil || r.StatusCode() != http.StatusNoContent {
		t.Fatalf("following watch: %v %+v", err, r)
	}
}

func TestOddChannelNameOverTCP(t *testing.T) {
	addr, _ := startLongPoll(t, nil, 0, nil)
	c := identityClient(t, addr, "pat")
	ctx := context.Background()
	openSessionID(t, c)

	names := func() map[string]bool {
		r, err := c.ListChannelsWithResponse(ctx)
		if err != nil || r.JSON200 == nil {
			t.Fatalf("list: %v %+v", err, r)
		}
		m := map[string]bool{}
		for _, ch := range *r.JSON200 {
			m[ch.Name] = true
		}
		return m
	}
	for _, name := range []string{"odd name", "100%", "a%2Fb", "what?", "tag#1", "c++", "café"} {
		cr, err := c.CreateChannelWithResponse(ctx, nil, gen.CreateChannelRequest{Name: name})
		if err != nil || cr.JSON201 == nil {
			t.Fatalf("%q: create: %v %s", name, err, cr.Body)
		}
		sr, err := c.SubscribeWithResponse(ctx, name, nil, gen.SubscribeJSONRequestBody{})
		if err != nil || sr.StatusCode() >= 300 {
			t.Fatalf("%q: subscribe: %v %d %s", name, err, sr.StatusCode(), sr.Body)
		}
		if !names()[name] {
			t.Fatalf("%q: not listed after create", name)
		}
		dr, err := c.DeleteChannelWithResponse(ctx, name, nil)
		if err != nil || dr.StatusCode() >= 300 {
			t.Fatalf("%q: delete: %v %d %s", name, err, dr.StatusCode(), dr.Body)
		}
		if names()[name] {
			t.Fatalf("%q: still listed after delete", name)
		}
	}

	sr, err := c.SubscribeWithResponse(ctx, "a/b", nil, gen.SubscribeJSONRequestBody{})
	if err != nil {
		t.Fatal(err)
	}
	p := sr.ApplicationproblemJSONDefault
	if sr.StatusCode() != http.StatusNotFound || p == nil || p.Code != gen.NotFound ||
		p.Detail == nil || *p.Detail != `channel "a/b" does not exist` {
		t.Fatalf("a/b: status %d body %s, want not_found naming the channel", sr.StatusCode(), sr.Body)
	}
}
