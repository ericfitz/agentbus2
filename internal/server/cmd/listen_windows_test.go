//go:build windows

package cmd

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/ericfitz/agentbus2/internal/fileacl/acltest"
	"golang.org/x/sys/windows"
)

// Winsock's refusal of a socket file nothing listens on is WSAECONNREFUSED,
// not syscall.ECONNREFUSED; without it a stale socket blocks every restart.
func TestIsStaleDialErrorWinsockRefused(t *testing.T) {
	err := &net.OpError{Op: "dial", Net: "unix", Err: os.NewSyscallError("connect", windows.WSAECONNREFUSED)}
	if !isStaleDialError(err) {
		t.Fatalf("%v is not taken as stale", err)
	}
}

// Decision 58 on Windows: the socket file carries an owner-only DACL, since
// its mode means nothing there.
func TestListenUnixSetsOwnerOnlyACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s")
	l, err := Listen("unix:" + path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	acltest.AssertOwnerOnly(t, path)
}
