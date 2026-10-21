//go:build !windows

package e2e_test

import (
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// helperEnv marks the child process of startHelper.
const helperEnv = "AGENTBUS2_E2E_HELPER"

// TestHelperHarness is not a test of its own: it is the harness process of the
// harness-gone cells. startHelper runs the test binary with
// -test.run=^TestHelperHarness$ and helperEnv set. The helper starts
// `sh -c '"$0" "$@" > "$OUT"; echo $? > "$CODE"'` without waiting for it (so
// the shell, and the agentbus2 command under it, find the helper as their
// harness: FindHarness walks past the shell), then blocks on stdin. Closing
// stdin makes the helper exit; the orphaned shell keeps the command alive, and
// the command's harness poll finds the helper gone and exits 4, which the
// shell records in CODE.
func TestHelperHarness(t *testing.T) {
	if os.Getenv(helperEnv) != "1" {
		t.Skip("child process of the harness-gone cells")
	}
	bin := os.Getenv("AGENTBUS2_BIN")
	args := strings.Split(os.Getenv("AGENTBUS2_ARGS"), "\x1f")
	sh := exec.Command("sh", append([]string{"-c", `"$0" "$@" > "$AGENTBUS2_OUT"; echo $? > "$AGENTBUS2_CODE"`, bin}, args...)...)
	sh.Env = os.Environ()
	if err := sh.Start(); err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
	// Return without waiting: the shell is orphaned on purpose.
}

// helper is a running TestHelperHarness.
type helper struct {
	cmd           *exec.Cmd
	stdin         io.WriteCloser
	outFile, code string
	closed        bool
	waitDone      chan struct{}
}

// startHelper runs `agentbus2 args...` under a harness process of its own.
// The command's stdout goes to a file and its exit code to another.
func startHelper(t *testing.T, home string, args ...string) *helper {
	t.Helper()
	dir := t.TempDir()
	h := &helper{outFile: dir + "/out", code: dir + "/code", waitDone: make(chan struct{})}
	h.cmd = exec.Command(os.Args[0], "-test.run=^TestHelperHarness$")
	h.cmd.Env = append(environFor(home),
		helperEnv+"=1",
		"AGENTBUS2_BIN="+binPath,
		"AGENTBUS2_OUT="+h.outFile,
		"AGENTBUS2_CODE="+h.code,
		"AGENTBUS2_ARGS="+strings.Join(args, "\x1f"))
	in, err := h.cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	h.stdin = in
	if err := h.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		_ = h.cmd.Wait()
		close(h.waitDone)
	}()
	t.Cleanup(func() {
		h.closeStdin()
		select {
		case <-h.waitDone:
		case <-time.After(10 * time.Second):
			_ = h.cmd.Process.Kill()
			<-h.waitDone
		}
	})
	return h
}

// closeStdin lets the helper exit, which makes its harness "gone".
func (h *helper) closeStdin() {
	if !h.closed {
		h.closed = true
		_ = h.stdin.Close()
	}
}

// out is what the command has written to stdout so far.
func (h *helper) out() string {
	b, _ := os.ReadFile(h.outFile)
	return string(b)
}

// exitCode is the command's exit code once the shell has recorded it.
func (h *helper) exitCode() (string, bool) {
	b, err := os.ReadFile(h.code)
	if err != nil || !strings.HasSuffix(string(b), "\n") {
		return "", false
	}
	return strings.TrimSpace(string(b)), true
}
