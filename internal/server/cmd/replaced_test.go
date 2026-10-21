package cmd

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/identities"
	"github.com/ericfitz/agentbus2/internal/core/messaging"
	"github.com/ericfitz/agentbus2/internal/core/notify"
	"github.com/ericfitz/agentbus2/internal/gen"
	"github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/store/sqlite"
)

const identityHeader = "X-Agentbus2-Test-Identity"

// headerAuth is a test-only Authenticator: the identity is whatever the
// header says. Shared by replaced_test.go and longpoll_test.go.
type headerAuth struct{}

func (headerAuth) Authenticate(r *http.Request) (string, error) {
	return r.Header.Get(identityHeader), nil
}

// TestHelperProcess is the re-exec target of startHelper: a server over the
// SQLite file named by AGENTBUS2_HELPER_SQLITE with the polling notifier. In a
// process without AGENTBUS2_HELPER_SERVER=1 it returns at once.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("AGENTBUS2_HELPER_SERVER") != "1" {
		return
	}
	s, err := sqlite.Open(context.Background(), os.Getenv("AGENTBUS2_HELPER_SQLITE"), sqlite.Options{})
	if err != nil {
		fmt.Fprintln(os.Stderr, "helper: sqlite.Open:", err)
		os.Exit(2)
	}
	cfg := core.Defaults()
	cfg.WatchMergeSeconds = 0
	ctx, cancel := context.WithCancel(context.Background())
	go func() { // exit when the parent closes our stdin
		_, _ = io.Copy(io.Discard, os.Stdin)
		cancel()
	}()
	err = Serve(ctx, Config{
		Listen:       "127.0.0.1:0",
		Registration: identities.ModeOpen,
		Store:        s,
		Auth:         headerAuth{},
		Notifier:     notify.NewPolling(100 * time.Millisecond),
		Cfg:          &cfg,
		Ready:        func(a net.Addr) { fmt.Printf("listening %s\n", a) },
	})
	_ = s.Close()
	if err != nil {
		fmt.Fprintln(os.Stderr, "helper: Serve:", err)
		os.Exit(2)
	}
	os.Exit(0)
}

// startHelper re-executes the test binary as a server over dbPath and returns
// its address. The cleanup closes its stdin, waits 5 s and kills it, so no
// server outlives the test holding the file.
func startHelper(t *testing.T, dbPath string) string {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
	cmd.Env = append(os.Environ(), "AGENTBUS2_HELPER_SERVER=1", "AGENTBUS2_HELPER_SQLITE="+dbPath)
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	waited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(waited) }()
	t.Cleanup(func() {
		_ = stdin.Close()
		select {
		case <-waited:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-waited
		}
	})
	lines := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			if a, ok := strings.CutPrefix(sc.Text(), "listening "); ok {
				lines <- a
				break
			}
		}
		_, _ = io.Copy(io.Discard, stdout) // never let the helper block on stdout
	}()
	select {
	case a := <-lines:
		return a
	case <-time.After(10 * time.Second):
		t.Fatal("helper did not report listening within 10 s")
		return ""
	}
}

// identityClient is the generated client over TCP acting as identity.
func identityClient(t *testing.T, addr, identity string, opts ...gen.ClientOption) *gen.ClientWithResponses {
	t.Helper()
	opts = append(opts, gen.WithRequestEditorFn(func(_ context.Context, r *http.Request) error {
		r.Header.Set(identityHeader, identity)
		return nil
	}))
	c, err := gen.NewClientWithResponses("http://"+addr, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// seedIdentities creates identities in a SQLite file before any server opens it.
func seedIdentities(t *testing.T, path string, names ...string) {
	t.Helper()
	s, err := sqlite.Open(context.Background(), path, sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	err = s.Tx(context.Background(), func(ctx context.Context, tx store.Tx) error {
		for _, n := range names {
			if err := tx.CreateIdentity(ctx, n, time.Now().UnixMilli()); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed identities: %v", err)
	}
}

func openSessionID(t *testing.T, c *gen.ClientWithResponses) string {
	t.Helper()
	hint := "t"
	r, err := c.OpenSessionWithResponse(context.Background(), gen.OpenSessionJSONRequestBody{Hint: &hint})
	if err != nil || r.JSON201 == nil {
		t.Fatalf("open session: %v %+v", err, r)
	}
	return r.JSON201.Id
}

type watchOutcome struct {
	resp *gen.WatchResponse
	err  error
	at   time.Time
}

func watchAsync(c *gen.ClientWithResponses, session string, timeout float64) <-chan watchOutcome {
	out := make(chan watchOutcome, 1)
	go func() {
		r, err := c.WatchWithResponse(context.Background(), session, &gen.WatchParams{Timeout: &timeout})
		out <- watchOutcome{r, err, time.Now()}
	}()
	return out
}

func waitOutcome(t *testing.T, ch <-chan watchOutcome, d time.Duration, what string) watchOutcome {
	t.Helper()
	select {
	case o := <-ch:
		if o.err != nil {
			t.Fatalf("%s: transport error: %v", what, o.err)
		}
		return o
	case <-time.After(d):
		t.Fatalf("%s: no response within %v", what, d)
		return watchOutcome{}
	}
}

// Issue ericfitz/agentbus#39, Decision 68: a watch armed through process B
// replaces the one armed through process A, over one SQLite file.
func TestWatchReplacedAcrossProcesses(t *testing.T) {
	db := filepath.Join(t.TempDir(), "bus.sqlite")
	seedIdentities(t, db, "pat")
	a, b := startHelper(t, db), startHelper(t, db)
	ca, cb := identityClient(t, a, "pat"), identityClient(t, b, "pat")

	session := openSessionID(t, ca)
	wa := watchAsync(ca, session, 10)
	time.Sleep(300 * time.Millisecond)
	registered := time.Now()
	wb := watchAsync(cb, session, 1)

	oa := waitOutcome(t, wa, 3*time.Second, "watch A")
	if oa.resp.StatusCode() != http.StatusConflict || oa.resp.ApplicationproblemJSON409 == nil ||
		oa.resp.ApplicationproblemJSON409.Code != gen.Replaced {
		t.Fatalf("watch A: status %d body %s, want 409 replaced", oa.resp.StatusCode(), oa.resp.Body)
	}
	if d := oa.at.Sub(registered); d > 1500*time.Millisecond {
		t.Fatalf("watch A learned of the replacement after %v, want within 1.5 s", d)
	}
	ob := waitOutcome(t, wb, 5*time.Second, "watch B")
	if ob.resp.StatusCode() != http.StatusNoContent {
		t.Fatalf("watch B: status %d body %s, want 204 at its timeout", ob.resp.StatusCode(), ob.resp.Body)
	}
}

// A send from another process (here the test process, with a memory notifier
// whose hints cannot reach the helper) wakes a polling watcher.
func TestWatchWakesAcrossProcesses(t *testing.T) {
	db := filepath.Join(t.TempDir(), "bus.sqlite")
	seedIdentities(t, db, "pat", "sam")
	a := startHelper(t, db)
	ca := identityClient(t, a, "pat")
	ctx := context.Background()

	session := openSessionID(t, ca)
	if r, err := ca.CreateChannelWithResponse(ctx, nil, gen.CreateChannelRequest{Name: "dev"}); err != nil || r.JSON201 == nil {
		t.Fatalf("create dev: %v %+v", err, r)
	}
	if r, err := ca.SubscribeWithResponse(ctx, "dev", nil, gen.SubscribeJSONRequestBody{}); err != nil || r.StatusCode() >= 300 {
		t.Fatalf("subscribe dev: %v %+v", err, r)
	}
	w := watchAsync(ca, session, 5)
	time.Sleep(300 * time.Millisecond)

	s, err := sqlite.Open(ctx, db, sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	cr, err := core.New(s, core.Defaults(), core.Options{Notifier: notify.NewMemory()})
	if err != nil {
		t.Fatal(err)
	}
	sent := time.Now()
	if _, err := (messaging.Service{Core: cr}).Send(ctx, "sam", ab2bus.SendInput{Channel: "dev", Content: "hi"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	o := waitOutcome(t, w, 4*time.Second, "watch A")
	if o.resp.JSON200 == nil || o.resp.JSON200.Count != 1 || len(o.resp.JSON200.Channels) != 1 || o.resp.JSON200.Channels[0] != "dev" {
		t.Fatalf("watch: status %d body %s, want 200 count 1 channels [dev]", o.resp.StatusCode(), o.resp.Body)
	}
	if d := o.at.Sub(sent); d > time.Second {
		t.Fatalf("watch woke %v after the send, want within 1 s", d)
	}
}
