package slackfake

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/slackapi"
)

func TestDelayHistory(t *testing.T) {
	f := New(self)
	// "C1" does not exist: the answer is ErrChannelGone, after the delay.
	f.DelayHistory(200 * time.Millisecond)
	start := time.Now()
	if _, err := f.History(context.Background(), "C1", "0", ""); !errors.Is(err, slackapi.ErrChannelGone) {
		t.Fatalf("History = %v, want ErrChannelGone", err)
	}
	if d := time.Since(start); d < 200*time.Millisecond {
		t.Errorf("History returned after %v, want at least 200ms", d)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.History(ctx, "C1", "0", ""); !errors.Is(err, context.Canceled) {
		t.Errorf("History with a canceled context and a delay = %v, want context.Canceled", err)
	}

	f.DelayHistory(0)
	start = time.Now()
	if _, err := f.History(context.Background(), "C1", "0", ""); !errors.Is(err, slackapi.ErrChannelGone) {
		t.Fatalf("History after clearing = %v, want ErrChannelGone", err)
	}
	if d := time.Since(start); d > 150*time.Millisecond {
		t.Errorf("cleared delay still waited %v", d)
	}
}
