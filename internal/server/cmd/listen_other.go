//go:build !windows

package cmd

import "syscall"

// staleDialErrors are the dial errors that mark a socket file as stale:
// nothing listens on it (ECONNREFUSED), or it vanished between the Lstat and
// the dial (ENOENT).
var staleDialErrors = []error{syscall.ECONNREFUSED, syscall.ENOENT}
