package mcpserver_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ericfitz/agentbus2/internal/client"
	"github.com/ericfitz/agentbus2/internal/mcpserver"
	"github.com/ericfitz/agentbus2/internal/sessionstate"
)

// errTable is a procs.Table whose every lookup fails with something other
// than procs.ErrGone.
type errTable struct{}

func (errTable) Parent(int) (int, error)      { return 0, errors.New("process table unsupported") }
func (errTable) Comm(int) (string, error)     { return "", errors.New("process table unsupported") }
func (errTable) StartTime(int) (int64, error) { return 0, errors.New("process table unsupported") }

// TestMain turns the test binary into the stdio MCP server when
// AGENTBUS2_MCP_HELPER is set (v1 integration_test.go's pattern).
func TestMain(m *testing.M) {
	if os.Getenv("AGENTBUS2_MCP_HELPER") == "1" {
		o := mcpserver.Options{Client: client.Options{Environ: os.Environ()}}
		if os.Getenv("AGENTBUS2_MCP_HELPER_TABLE") == "error" {
			o.Table = errTable{}
		}
		if err := mcpserver.Run(context.Background(), o); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	// Run from an empty directory: the server finds the nearest
	// .local/agentbus2.json from the working directory (the helper inherits
	// it), so a checkout where `agentbus2 init` has run would otherwise select
	// its profile.
	dir, err := os.MkdirTemp("", "agentbus2-mcpserver-test-")
	if err == nil {
		err = os.Chdir(dir)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "TestMain:", err)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// helperSession starts the helper process over e and connects an SDK client
// named claude-code 2.0.0 through the stdio command transport.
func helperSession(t *testing.T, e *testEnv, extraEnv ...string) (*running, *syncBuf) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(append([]string{"AGENTBUS2_MCP_HELPER=1"}, e.Environ...), extraEnv...)
	stderr := &syncBuf{}
	cmd.Stderr = stderr
	c := mcp.NewClient(&mcp.Implementation{Name: "claude-code", Version: "2.0.0"}, nil)
	cs, err := c.Connect(context.Background(), &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("connect: %v (stderr %q)", err, stderr.String())
	}
	r := &running{CS: cs}
	t.Cleanup(func() { _ = cs.Close() })
	return r, stderr
}

func logFiles(t *testing.T, e *testEnv) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(e.DataDir, "logs", "mcp-*.log"))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestRunServesStdio(t *testing.T) {
	e := newTestEnv(t)
	// A stale file whose MCP server and harness are both dead is swept at start.
	if err := sessionstate.Write(e.DataDir, sessionstate.File{
		Session: "stale", Identity: "pat", Profile: "default", HarnessPID: 2147483000, MCPPID: 2147483001, Primary: true, OpenedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}

	r, stderr := helperSession(t, e)
	tools, err := r.CS.ListTools(context.Background(), nil)
	if err != nil || len(tools.Tools) != 24 {
		t.Fatalf("tools/list: %v, %d tools", err, len(tools.Tools))
	}
	if _, err := sessionstate.Read(e.DataDir, "stale"); err == nil {
		t.Errorf("the stale state file was not swept")
	}
	reg := r.must(t, "register", nil)
	sid := str(reg, "session")
	f, err := sessionstate.Read(e.DataDir, sid)
	if err != nil {
		t.Fatal(err)
	}
	if f.HarnessClient != "claude-code" || f.HarnessClientVersion != "2.0.0" || !f.Primary || f.HarnessPID != os.Getpid() || f.MCPPID == 0 {
		t.Fatalf("state file: %+v", f)
	}
	if got := logFiles(t, e); len(got) != 1 {
		t.Fatalf("log files while running: %v", got)
	}

	// A clean close removes the log file and keeps the state file.
	if err := r.CS.Close(); err != nil {
		t.Logf("close: %v", err)
	}
	waitFor(t, "the log file to be removed", func() bool { return len(logFiles(t, e)) == 0 })
	if _, err := sessionstate.Read(e.DataDir, sid); err != nil {
		t.Fatalf("state file after a clean close: %v", err)
	}
	if stderr.Len() != 0 {
		t.Errorf("the server wrote to stderr: %q", stderr.String())
	}

	// The restart under the same harness (this process) resumes the session.
	r2, stderr2 := helperSession(t, e)
	got := r2.must(t, "register", nil)
	if got["resumed"] != true || str(got, "session") != sid {
		t.Fatalf("register after the restart: %v (first %v)", got, reg)
	}
	if stderr2.Len() != 0 {
		t.Errorf("the restarted server wrote to stderr: %q", stderr2.String())
	}
}

func TestRunSurvivesHarnessLookupFailure(t *testing.T) {
	e := newTestEnv(t)
	r, _ := helperSession(t, e, "AGENTBUS2_MCP_HELPER_TABLE=error")
	reg := r.must(t, "register", nil)
	f, err := sessionstate.Read(e.DataDir, str(reg, "session"))
	if err != nil {
		t.Fatal(err)
	}
	if f.HarnessPID != 0 || f.HarnessStart != 0 || f.MCPPID == 0 {
		t.Fatalf("state file with an unknown harness: %+v", f)
	}
	// With no harness a restart cannot identify its file, so it starts anew.
	_ = r.CS.Close()
	r2, _ := helperSession(t, e, "AGENTBUS2_MCP_HELPER_TABLE=error")
	got := r2.must(t, "register", nil)
	if got["resumed"] != false {
		t.Errorf("resumed without a harness: %v", got)
	}
}

func TestOpenLog(t *testing.T) {
	dir := t.TempDir()
	log, closeLog, err := mcpserver.OpenLog(dir, 4242)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "logs", "mcp-4242.log")
	log.Info("hello")
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	// Windows has no mode bits (a file reads as 0666) and no ACL is set on
	// the log.
	if runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Errorf("log mode %v", st.Mode().Perm())
	}
	closeLog()
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Errorf("log file after close: %v", err)
	}
	closeLog() // a second call does nothing
}

// syncBuf is a bytes.Buffer safe for the exec copier goroutine and the test.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Len()
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
