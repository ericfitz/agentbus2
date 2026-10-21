package procs

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestHelperSleep is not a test: sleeperCommand runs this test binary with
// AGENTBUS_TEST_HELPER=sleep so tests have a live child to inspect on every
// OS (Windows has no `sleep`). It is skipped in a normal run.
func TestHelperSleep(t *testing.T) {
	if os.Getenv("AGENTBUS_TEST_HELPER") != "sleep" {
		t.Skip("helper process entry point")
	}
	time.Sleep(60 * time.Second)
}

// sleeperCommand is a child process that lives for 60 s unless killed.
func sleeperCommand() *exec.Cmd {
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperSleep$")
	cmd.Env = append(os.Environ(), "AGENTBUS_TEST_HELPER=sleep")
	return cmd
}

// helperComm is the command name the process table reports for the helper
// (the test binary's base name; Linux truncates comm to 15 bytes, which
// "procs.test" fits).
func helperComm() string {
	return strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe")
}

type fakeProc struct {
	ppid  int
	comm  string
	start int64
}

type fakeTable map[int]fakeProc

func (f fakeTable) get(pid int) (fakeProc, error) {
	p, ok := f[pid]
	if !ok {
		return fakeProc{}, ErrGone
	}
	return p, nil
}
func (f fakeTable) Parent(pid int) (int, error)  { p, err := f.get(pid); return p.ppid, err }
func (f fakeTable) Comm(pid int) (string, error) { p, err := f.get(pid); return p.comm, err }
func (f fakeTable) StartTime(pid int) (int64, error) {
	p, err := f.get(pid)
	return p.start, err
}
