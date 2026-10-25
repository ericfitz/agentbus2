package slackgo

import (
	"context"
	"testing"
	"time"

	"github.com/slack-go/slack/socketmode"

	"github.com/ericfitz/agentbus2/internal/slackapi"
)

// TestRunSocketBoundsStuckLibrary: a RunContext that ignores its context must
// not hang shutdown; RunSocket gives up waiting after socketStopWait.
func TestRunSocketBoundsStuckLibrary(t *testing.T) {
	s := newSocketServer(t, func(int, func(any)) {})
	c := socketClient(t, s)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	c.socketRun = func(context.Context, *socketmode.Client) error {
		<-release
		return nil
	}
	c.socketStopWait = 50 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.RunSocket(ctx, func(context.Context, slackapi.Event) {}) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunSocket: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("RunSocket hung on a stuck RunContext")
	}
}
