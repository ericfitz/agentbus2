//go:build darwin || linux

package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/ericfitz/agentbus2/internal/version"
)

// openPTY opens a pseudo-terminal pair with golang.org/x/sys/unix alone (the
// module has no pty dependency and this test adds none): the master, which the
// test reads and never answers, and the slave, which becomes the child's
// controlling terminal.
func openPTY() (master, slave *os.File, err error) {
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("open /dev/ptmx: %w", err)
	}
	master = os.NewFile(uintptr(fd), "/dev/ptmx")
	name, err := ptsName(fd)
	if err != nil {
		_ = master.Close()
		return nil, nil, err
	}
	sfd, err := unix.Open(name, unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		_ = master.Close()
		return nil, nil, fmt.Errorf("open %s: %w", name, err)
	}
	return master, os.NewFile(uintptr(sfd), name), nil
}

// buildBinary builds agentbus2 into a temporary directory, as the release build
// does (no cgo, -trimpath).
func buildBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "agentbus2")
	build := exec.Command("go", "build", "-trimpath", "-o", bin, ".")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

// silentPTYBudget is how long `agentbus2 version` may take on a terminal that
// never answers. A package init that queries the terminal (Bubble Tea v1's
// background-color and cursor-position probe) waits seconds for a reply; a
// process that queries nothing exits in milliseconds.
const silentPTYBudget = 500 * time.Millisecond

// TestVersionOnSilentPTY is the proof of post-build decision 2: no agentbus2
// command queries the terminal at package init. It runs the built binary with
// a pty that never answers as its controlling terminal and its stdin, stdout
// and stderr, and requires `version` to print the version and exit well under
// a second.
func TestVersionOnSilentPTY(t *testing.T) {
	bin := buildBinary(t)
	master, slave, err := openPTY()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = master.Close() }()

	cmd := exec.Command(bin, "version")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	// A terminal that would answer a query, were anyone listening.
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "COLORTERM=truecolor")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}

	// Drain the master without ever writing to it: whatever the child sends
	// (its output, or a terminal query) goes unanswered.
	var out bytes.Buffer
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		_, _ = io.Copy(&out, master)
	}()

	start := time.Now()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = slave.Close() // the child holds its own copy
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	select {
	case err = <-exited:
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		<-exited
		t.Fatalf("agentbus2 version still running after 10s on a silent pty")
	}
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("agentbus2 version: %v", err)
	}
	// The slave is closed on both sides now, so the master reads EIO (Linux)
	// or EOF: the drain ends.
	select {
	case <-drained:
	case <-time.After(2 * time.Second):
		_ = master.Close()
		<-drained
	}
	t.Logf("agentbus2 version on a silent pty: %v", elapsed)

	got := out.String()
	if strings.ContainsRune(got, '\x1b') {
		t.Errorf("agentbus2 version wrote an escape sequence to the terminal: %q", got)
	}
	if strings.TrimSpace(got) != version.Version {
		t.Errorf("printed %q, want %q", got, version.Version)
	}
	if elapsed >= silentPTYBudget {
		t.Fatalf("agentbus2 version took %v on a silent pty, want under %v: something queries the terminal at init", elapsed, silentPTYBudget)
	}
}

// TestOpenPTYIsATerminal checks the helper itself: the slave is a terminal and
// bytes written to it arrive on the master.
func TestOpenPTYIsATerminal(t *testing.T) {
	master, slave, err := openPTY()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = master.Close(); _ = slave.Close() }()
	if _, err := unix.IoctlGetTermios(int(slave.Fd()), ioctlGetTermios); err != nil {
		t.Fatalf("slave is not a terminal: %v", err)
	}
	if _, err := slave.Write([]byte("ping\n")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	n, err := master.Read(buf)
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	if !strings.Contains(string(buf[:n]), "ping") {
		t.Fatalf("master read %q, want ping", buf[:n])
	}
}
