//go:build windows

package cmd

import (
	"syscall"

	"golang.org/x/sys/windows"
)

// staleDialErrors are the dial errors that mark a socket file as stale.
// Winsock reports a socket file nothing listens on as WSAECONNREFUSED
// (10061), which is not syscall.ECONNREFUSED: syscall's Windows ECONNREFUSED
// is an invented number no Windows call returns. A file that vanished
// between the Lstat and the dial is ERROR_FILE_NOT_FOUND, which matches
// syscall.ENOENT.
var staleDialErrors = []error{windows.WSAECONNREFUSED, syscall.ECONNREFUSED, syscall.ENOENT}
