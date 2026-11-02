//go:build !windows

package mcpcli

import (
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A named pipe with no writer must be refused without opening it (open would
// block forever).
func TestEnrollmentTokenFileFIFORefused(t *testing.T) {
	p := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(p, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := enrollmentToken("", p, nil)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), p) || !strings.Contains(err.Error(), "regular file") {
			t.Errorf("err = %v, want one naming %s and a regular file", err, p)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("enrollmentToken blocked on a FIFO")
	}
}
