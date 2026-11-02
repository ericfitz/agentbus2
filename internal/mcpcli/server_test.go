package mcpcli

import (
	"bytes"
	"context"
	"path/filepath"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/server/cmd"
)

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *lockedBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *lockedBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

var listeningRE = regexp.MustCompile(`listening on (\S+) `)

// startServer runs `agentbus2 server` on a loopback port over a temp SQLite
// file (open registration unless extra says otherwise) and returns its base URL.
func startServer(t *testing.T, extra ...string) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	out, errb := &lockedBuf{}, &lockedBuf{}
	done := make(chan int, 1)
	db := filepath.Join(t.TempDir(), "srv.sqlite")
	args := append([]string{"-listen", "127.0.0.1:0", "-sqlite", db}, extra...)
	go func() { done <- cmd.Run(ctx, args, nil, out, errb) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Error("server did not stop")
		}
	})
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if m := listeningRE.FindStringSubmatch(out.String()); m != nil {
			return "http://" + m[1]
		}
		select {
		case code := <-done:
			t.Fatalf("server exited %d: %s %s", code, out.String(), errb.String())
		case <-time.After(10 * time.Millisecond):
		}
	}
	t.Fatalf("no listening line: %s %s", out.String(), errb.String())
	return ""
}
