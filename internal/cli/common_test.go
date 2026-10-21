package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/auth/keys"
	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/client"
	"github.com/ericfitz/agentbus2/internal/credential"
	"github.com/ericfitz/agentbus2/internal/procs"
	"github.com/ericfitz/agentbus2/internal/profile"
	"github.com/ericfitz/agentbus2/internal/sessionstate"
)

func TestFailFormatsBusError(t *testing.T) {
	var b bytes.Buffer
	be := &bus.Error{Code: bus.CodeForbidden, Message: "nope"}
	if code := fail(&b, be); code != 1 || b.String() != "agentbus2: forbidden: nope\n" {
		t.Fatalf("code %d, output %q", code, b.String())
	}
	b.Reset()
	if code := fail(&b, fmt.Errorf("wrapped: %w", be)); code != 1 || b.String() != "agentbus2: forbidden: nope\n" {
		t.Fatalf("wrapped: code %d, output %q", code, b.String())
	}
	b.Reset()
	if code := fail(&b, errors.New("plain")); code != 1 || b.String() != "agentbus2: plain\n" {
		t.Fatalf("plain: code %d, output %q", code, b.String())
	}
}

func TestParseExit(t *testing.T) {
	for _, c := range []struct {
		err  error
		want int
	}{
		{flag.ErrHelp, 0},
		{fmt.Errorf("x: %w", flag.ErrHelp), 0},
		{errors.New("flag provided but not defined: -z"), 2},
	} {
		if got := parseExit(c.err); got != c.want {
			t.Errorf("parseExit(%v) = %d, want %d", c.err, got, c.want)
		}
	}
	var stderr bytes.Buffer
	fs := newFlagSet("probe", &stderr)
	fs.Bool("x", false, "")
	if err := fs.Parse([]string{"-z"}); err == nil || parseExit(err) != 2 {
		t.Fatalf("an unknown flag: %v", err)
	}
	if !strings.Contains(stderr.String(), "agentbus2 probe") {
		t.Fatalf("usage output %q lacks the command name", stderr.String())
	}
	if err := newFlagSet("probe", &stderr).Parse([]string{"-h"}); parseExit(err) != 0 {
		t.Fatalf("-h: %v", err)
	}
}

func TestPrintJSON(t *testing.T) {
	var b bytes.Buffer
	if err := printJSON(&b, map[string]int{"a": 1}); err != nil || b.String() != "{\n  \"a\": 1\n}\n" {
		t.Fatalf("%q, %v", b.String(), err)
	}
	if err := printJSON(&b, make(chan int)); err == nil {
		t.Fatal("printJSON accepted an unmarshalable value")
	}
}

func TestOpenUsageVersusRuntimeExit(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()

	// A profile that is not defined is a usage error.
	var stderr bytes.Buffer
	c, code := open(ctx, client.Options{Environ: e.Environ, Cwd: e.Home, Profile: "nope"}, &stderr)
	if c != nil || code != 2 || !strings.Contains(stderr.String(), `profile "nope" is not defined`) {
		t.Fatalf("code %d, stderr %q", code, stderr.String())
	}

	// A server profile without a credential is a usage error too.
	e.writeProfile(t, profile.Profile{Name: "lab", Mode: profile.ModeServer, Server: "http://127.0.0.1:1", Identity: "pat"})
	stderr.Reset()
	if c, code = open(ctx, client.Options{Environ: e.Environ, Cwd: e.Home, Profile: "lab"}, &stderr); c != nil || code != 2 ||
		!strings.Contains(stderr.String(), "run agentbus2 register") {
		t.Fatalf("no credential: code %d, stderr %q", code, stderr.String())
	}

	// A failure to open the store is a runtime error.
	stderr.Reset()
	e.writeProfile(t, profile.Profile{Name: "broken", Mode: profile.ModeLocal, Identity: "pat", SQLite: e.DataDir + "/no/such/dir/x.sqlite"})
	if c, code = open(ctx, client.Options{Environ: e.Environ, Cwd: e.Home, Profile: "broken"}, &stderr); c != nil || code != 1 {
		t.Fatalf("store failure: code %d, stderr %q", code, stderr.String())
	}

	// A good local profile opens.
	stderr.Reset()
	c, code = open(ctx, client.Options{Environ: e.Environ, Cwd: e.Home}, &stderr)
	if c == nil || code != 0 || stderr.Len() != 0 {
		t.Fatalf("good profile: code %d, stderr %q", code, stderr.String())
	}
	_ = c.Close()
}

// Nothing is dialed at open: a server profile pointing at a closed port opens,
// and the failure comes with the first call.
func TestOpenDialsNothingInServerMode(t *testing.T) {
	e := newTestEnv(t)
	e.writeProfile(t, profile.Profile{Name: "lab", Mode: profile.ModeServer, Server: "http://127.0.0.1:1", Identity: "pat"})
	key, err := keys.Generate(keys.EdDSA)
	if err != nil {
		t.Fatal(err)
	}
	if err := (credential.Store{Dir: profile.CredentialDir(e.ConfigDir, "lab")}).Save("pat", key); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	c, code := open(context.Background(), client.Options{Environ: e.Environ, Cwd: e.Home, Profile: "lab"}, &stderr)
	if c == nil || code != 0 {
		t.Fatalf("code %d, stderr %q", code, stderr.String())
	}
	defer func() { _ = c.Close() }()
	if _, err := c.Bus.ListChannels(context.Background()); err == nil {
		t.Fatal("ListChannels reached a server on port 1")
	}
}

func TestOpenAppliesTestClientOptions(t *testing.T) {
	e := newTestEnv(t)
	var seen client.Options
	prev := testClientOptions
	testClientOptions = func(o *client.Options) { prev(o); seen = *o }
	var stderr bytes.Buffer
	c, code := open(context.Background(), client.Options{Environ: e.Environ, Cwd: e.Home}, &stderr)
	if c == nil || code != 0 {
		t.Fatalf("code %d, stderr %q", code, stderr.String())
	}
	defer func() { _ = c.Close() }()
	if seen.PollInterval != 50*time.Millisecond || c.Core.Cfg.WatchMergeSeconds != 0 {
		t.Fatalf("seen %+v, WatchMergeSeconds %d", seen, c.Core.Cfg.WatchMergeSeconds)
	}

	// With the hook nil the defaults hold.
	testClientOptions = nil
	c2, code := open(context.Background(), client.Options{Environ: e.Environ, Cwd: e.Home}, &stderr)
	if c2 == nil || code != 0 {
		t.Fatalf("code %d, stderr %q", code, stderr.String())
	}
	defer func() { _ = c2.Close() }()
	if c2.Core.Cfg.WatchMergeSeconds != 2 {
		t.Fatalf("WatchMergeSeconds %d, want the default 2", c2.Core.Cfg.WatchMergeSeconds)
	}
}

func TestTestEnvFixture(t *testing.T) {
	ctx := context.Background()
	e := newTestEnv(t)

	// conn as pat and sam over the one default profile.
	pat, sam := e.conn(t, "pat"), e.conn(t, "sam")
	if pat.Resolved.Identity != "pat" || sam.Resolved.Identity != "sam" {
		t.Fatalf("identities %q %q", pat.Resolved.Identity, sam.Resolved.Identity)
	}
	if _, err := sam.Bus.ListChannels(ctx); err != nil {
		t.Fatal(err)
	}

	// writeState, then ForHarness finds it.
	e.writeState(t, sessionstate.File{Session: "s1", Identity: "pat", Profile: "default", HarnessPID: testHarness.Pid, HarnessStart: testHarness.Start, MCPPID: 99, Primary: true, OpenedAt: 1})
	f, ok, err := sessionstate.ForHarness(e.DataDir, testHarness)
	if err != nil || !ok || f.Session != "s1" {
		t.Fatalf("ForHarness: %+v %v %v", f, ok, err)
	}

	// The fresh fixture's harness walk answers testHarness.
	h, err := procs.FindHarness(processTable, selfPID())
	if err != nil || h != testHarness {
		t.Fatalf("FindHarness: %+v, %v", h, err)
	}
	if !procs.Alive(processTable, testHarness) {
		t.Fatal("the fake harness is not alive")
	}

	// setGone(10): not alive, and the walk reports the harness gone.
	processTable.(*fakeTable).setGone(10)
	if procs.Alive(processTable, testHarness) {
		t.Fatal("setGone(10) left the harness alive")
	}
	if _, err := procs.FindHarness(processTable, selfPID()); !errors.Is(err, procs.ErrGone) {
		t.Fatalf("FindHarness after setGone: %v", err)
	}

	// setError(42, ...): the lookup error is not ErrGone.
	unsupported := errors.New("unsupported")
	processTable.(*fakeTable).setError(42, unsupported)
	if _, err := procs.FindHarness(processTable, selfPID()); !errors.Is(err, unsupported) || errors.Is(err, procs.ErrGone) {
		t.Fatalf("FindHarness after setError: %v", err)
	}

	// stubSessions: Watch reaches the recorded function.
	var got bus.WatchInput
	var ss bus.Sessions = stubSessions{watch: func(_ context.Context, in bus.WatchInput) (bus.WatchResult, error) {
		got = in
		return bus.WatchResult{Woke: true, Seq: 9}, nil
	}}
	res, err := ss.Watch(ctx, bus.WatchInput{Session: "s1", After: 3})
	if err != nil || res.Seq != 9 || got.Session != "s1" || got.After != 3 {
		t.Fatalf("stubSessions.Watch: %+v %+v %v", got, res, err)
	}

	// syncBuf under two writers (run with -race).
	var sb syncBuf
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				_, _ = sb.Write([]byte("x"))
				_ = sb.String()
			}
		}()
	}
	wg.Wait()
	if len(sb.String()) != 400 {
		t.Fatalf("syncBuf holds %d bytes, want 400", len(sb.String()))
	}
}

func TestTestEnvRestoresGlobals(t *testing.T) {
	prevPoll := procs.HarnessPollInterval
	t.Run("inner", func(t *testing.T) {
		_ = newTestEnv(t)
		if procs.HarnessPollInterval == prevPoll || testClientOptions == nil || selfPID() != 42 {
			t.Fatal("newTestEnv did not pin the globals")
		}
	})
	if procs.HarnessPollInterval != prevPoll || testClientOptions != nil || selfPID() == 42 {
		t.Fatal("newTestEnv did not restore the globals")
	}
	if _, ok := processTable.(*fakeTable); ok {
		t.Fatal("processTable still the fake")
	}
}
