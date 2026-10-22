//go:build windows

package cmd

import (
	"net"
	"os"
	"testing"

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
