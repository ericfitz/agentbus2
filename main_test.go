package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/version"
)

func TestRunVersion(t *testing.T) {
	for _, arg := range []string{"version", "-v", "--version"} {
		var out, errb bytes.Buffer
		if code := run([]string{arg}, &out, &errb); code != 0 {
			t.Fatalf("%s: exit %d, stderr %q", arg, code, errb.String())
		}
		if got := strings.TrimSpace(out.String()); got != version.Version {
			t.Fatalf("%s: printed %q, want %q", arg, got, version.Version)
		}
	}
}

func TestRunHelp(t *testing.T) {
	for _, arg := range []string{"help", "-h", "--help"} {
		var out, errb bytes.Buffer
		if code := run([]string{arg}, &out, &errb); code != 0 {
			t.Fatalf("%s: exit %d", arg, code)
		}
		if !strings.HasPrefix(out.String(), "usage: agentbus2 <command>") || errb.Len() != 0 {
			t.Fatalf("%s: stdout %q stderr %q", arg, out.String(), errb.String())
		}
	}
}

func TestRunRejectsUnknownAndEmpty(t *testing.T) {
	for _, args := range [][]string{nil, {"frobnicate"}} {
		var out, errb bytes.Buffer
		if code := run(args, &out, &errb); code != 2 {
			t.Fatalf("%v: exit %d, want 2", args, code)
		}
		if out.Len() != 0 || !strings.Contains(errb.String(), "usage: agentbus2") {
			t.Fatalf("%v: stdout %q stderr %q", args, out.String(), errb.String())
		}
	}
	var errb bytes.Buffer
	run([]string{"frobnicate"}, io.Discard, &errb)
	if !strings.Contains(errb.String(), `unknown command "frobnicate"`) {
		t.Fatalf("stderr %q lacks the unknown-command line", errb.String())
	}
}

func TestUsageListsServer(t *testing.T) {
	var out bytes.Buffer
	if code := run([]string{"help"}, &out, io.Discard); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(out.String(), "  server ") {
		t.Fatalf("usage %q lacks the server command", out.String())
	}
}

func TestRunServerRejectsMissingFlags(t *testing.T) {
	var errb bytes.Buffer
	if code := run([]string{"server"}, io.Discard, &errb); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if !strings.Contains(errb.String(), "-listen is required") {
		t.Fatalf("stderr %q", errb.String())
	}
}

// TestSignalContextHelper is the child process of TestSecondSignalExits, not a
// test of its own: it stands in for `agentbus2 server` draining after the
// first signal.
func TestSignalContextHelper(t *testing.T) {
	if os.Getenv("AGENTBUS2_TEST_SIGNAL_HELPER") != "1" {
		t.Skip("child process of TestSecondSignalExits")
	}
	ctx := signalContext()
	fmt.Println("ready")
	<-ctx.Done()
	fmt.Println("canceled")
	time.Sleep(time.Minute) // the Shutdown drain the second signal must cut short
}

// After the first SIGINT cancels the context, a second must restore the default
// action and end the process at once instead of waiting out the drain
// (os/signal.NotifyContext: default handling returns only after stop is
// called).
func TestSecondSignalExits(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SIGINT cannot be delivered to a child process on Windows")
	}
	child := exec.Command(os.Args[0], "-test.run=^TestSignalContextHelper$")
	child.Env = append(os.Environ(), "AGENTBUS2_TEST_SIGNAL_HELPER=1")
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill() })

	lines := make(chan string, 16)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()
	waitLine := func(want string) {
		t.Helper()
		timeout := time.After(10 * time.Second)
		for {
			select {
			case l, ok := <-lines:
				if !ok {
					t.Fatalf("child exited before printing %q", want)
				}
				if l == want {
					return
				}
			case <-timeout:
				t.Fatalf("child did not print %q", want)
			}
		}
	}

	waitLine("ready")
	if err := child.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	waitLine("canceled")

	exited := make(chan error, 1)
	go func() { exited <- child.Wait() }()
	// The stop that restores default handling runs just after the context is
	// canceled, so a signal sent too early is swallowed: repeat until it counts.
	deadline := time.After(5 * time.Second)
	for {
		select {
		case err := <-exited:
			var ee *exec.ExitError
			if !errors.As(err, &ee) {
				t.Fatalf("child ended with %v, want death by SIGINT", err)
			}
			if ws, ok := ee.Sys().(syscall.WaitStatus); !ok || !ws.Signaled() || ws.Signal() != syscall.SIGINT {
				t.Fatalf("child ended with %v, want death by SIGINT", err)
			}
			return
		case <-time.After(50 * time.Millisecond):
			_ = child.Process.Signal(syscall.SIGINT)
		case <-deadline:
			t.Fatal("a second SIGINT was swallowed: the signal stop is never called")
		}
	}
}
