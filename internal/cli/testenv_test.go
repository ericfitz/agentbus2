package cli

// This file is the one package-level test fixture of package cli. Tasks 6, 8,
// 9 and 10 add no package-level test helper of their own.
//
// newTestEnv sets package-level variables (testClientOptions, processTable,
// selfPID, procs.HarnessPollInterval) and restores them in t.Cleanup, so no
// test in package cli calls t.Parallel().

import (
	"bytes"
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/client"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/procs"
	"github.com/ericfitz/agentbus2/internal/profile"
	"github.com/ericfitz/agentbus2/internal/sessionstate"
)

// testHarness is the fake harness ref every state file in this package uses.
var testHarness = procs.Ref{Pid: 10, Start: 5}

// pinned is core.Defaults with the wake merge window off, so a wake does not
// wait two seconds for more messages.
func pinned() *core.Config {
	c := core.Defaults()
	c.WatchMergeSeconds = 0
	return &c
}

type testEnv struct {
	Home, ConfigDir, DataDir string
	Environ                  []string // HOME, AGENTBUS2_CONFIG_DIR, AGENTBUS2_DATA_DIR, PATH
	Config                   profile.Config
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	root := t.TempDir()
	e := &testEnv{Home: root + "/home", ConfigDir: root + "/config", DataDir: root + "/data"}
	for _, d := range []string{e.Home, e.ConfigDir, e.DataDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	e.Environ = []string{"HOME=" + e.Home, "AGENTBUS2_CONFIG_DIR=" + e.ConfigDir, "AGENTBUS2_DATA_DIR=" + e.DataDir, "PATH=" + os.Getenv("PATH")}
	e.Config = profile.Config{Path: e.ConfigDir + "/config.json", Profiles: map[string]profile.Profile{
		"default": {Name: "default", Mode: profile.ModeLocal, Identity: "pat"},
	}}
	if err := e.Config.Save(); err != nil {
		t.Fatal(err)
	}

	prevOpts, prevPoll, prevTable, prevSelf := testClientOptions, procs.HarnessPollInterval, processTable, selfPID
	testClientOptions = func(o *client.Options) {
		o.PollInterval = 50 * time.Millisecond
		o.Config = pinned()
	}
	procs.HarnessPollInterval = 10 * time.Millisecond
	tbl := newFakeTable()
	tbl.set(10, 1, 5, "claude")
	tbl.set(42, 10, 7, "agentbus2")
	processTable = tbl
	selfPID = func() int { return 42 }
	t.Cleanup(func() {
		testClientOptions, procs.HarnessPollInterval, processTable, selfPID = prevOpts, prevPoll, prevTable, prevSelf
	})
	return e
}

// conn opens the default profile as identity with the same pins as
// testClientOptions, plus tune; it is closed in t.Cleanup.
func (e *testEnv) conn(t *testing.T, identity string, tune ...func(*client.Options)) *client.Conn {
	t.Helper()
	o := client.Options{Environ: e.Environ, Cwd: e.Home, Identity: identity, PollInterval: 50 * time.Millisecond, Config: pinned()}
	for _, f := range tune {
		f(&o)
	}
	c, err := client.Open(context.Background(), o)
	if err != nil {
		t.Fatalf("open as %s: %v", identity, err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// writeProfile adds or replaces a profile in config.json.
func (e *testEnv) writeProfile(t *testing.T, p profile.Profile) {
	t.Helper()
	cfg, err := profile.Load(e.Config.Path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Profiles[p.Name] = p
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	e.Config = cfg
}

// writeState writes a session state file under DataDir.
func (e *testEnv) writeState(t *testing.T, f sessionstate.File) {
	t.Helper()
	if err := sessionstate.Write(e.DataDir, f); err != nil {
		t.Fatal(err)
	}
}

// syncBuf is a bytes.Buffer safe for a writer goroutine and a polling reader.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type fakeProc struct {
	ppid  int
	start int64
	comm  string
}

// fakeTable is a procs.Table over a map. An unknown pid answers procs.ErrGone.
type fakeTable struct {
	mu    sync.Mutex
	procs map[int]fakeProc
	gone  map[int]bool
	errs  map[int]error
}

func newFakeTable() *fakeTable {
	return &fakeTable{procs: map[int]fakeProc{}, gone: map[int]bool{}, errs: map[int]error{}}
}

func (f *fakeTable) set(pid, ppid int, start int64, comm string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.procs[pid] = fakeProc{ppid, start, comm}
}

// setGone makes every method answer procs.ErrGone for pid.
func (f *fakeTable) setGone(pid int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gone[pid] = true
}

// setError makes every method answer err for pid (a non-ErrGone lookup error,
// the "unsupported platform" case).
func (f *fakeTable) setError(pid int, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.errs[pid] = err
}

func (f *fakeTable) get(pid int) (fakeProc, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.errs[pid]; err != nil {
		return fakeProc{}, err
	}
	p, ok := f.procs[pid]
	if !ok || f.gone[pid] {
		return fakeProc{}, procs.ErrGone
	}
	return p, nil
}

func (f *fakeTable) Parent(pid int) (int, error)  { p, err := f.get(pid); return p.ppid, err }
func (f *fakeTable) Comm(pid int) (string, error) { p, err := f.get(pid); return p.comm, err }
func (f *fakeTable) StartTime(pid int) (int64, error) {
	p, err := f.get(pid)
	return p.start, err
}

// stubSessions is a bus.Sessions double for the watchLoop seam: Watch calls
// watch; the embedded nil interface makes any other method panic.
type stubSessions struct {
	bus.Sessions
	watch func(context.Context, bus.WatchInput) (bus.WatchResult, error)
}

func (s stubSessions) Watch(ctx context.Context, in bus.WatchInput) (bus.WatchResult, error) {
	return s.watch(ctx, in)
}
