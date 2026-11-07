package slackbridge

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestRetryDelay(t *testing.T) {
	base := 100 * time.Millisecond
	want := []time.Duration{base, 2 * base, 4 * base, 8 * base, 16 * base}
	for i, w := range want {
		if got := retryDelay(base, i+1); got != w {
			t.Errorf("attempt %d = %v, want %v", i+1, got, w)
		}
	}
	// Capped, with no overflow however large the attempt or the base.
	for _, attempt := range []int{10, 64, 1 << 20} {
		if got := retryDelay(base, attempt); got != requeueMax {
			t.Errorf("attempt %d = %v, want %v", attempt, got, requeueMax)
		}
	}
	if got := retryDelay(time.Duration(1<<62), 3); got != requeueMax {
		t.Errorf("huge base = %v", got)
	}
}

// TestSocketLoopBackoff: consecutive failed attempts wait base doubling to
// socketRetryMax; an attempt that connected resets the sequence; each failure
// logs once; a cancelled context ends the loop without another wait.
func TestSocketLoopBackoff(t *testing.T) {
	base := time.Second
	// Attempts 1-12 fail without connecting, 13 connects then drops, 14-15
	// fail, then the loop is cancelled during attempt 16.
	const connectAt, stopAt = 13, 16
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	attempt := 0
	run := func(ctx context.Context, connected func()) error {
		attempt++
		switch attempt {
		case connectAt:
			connected()
			return nil // the connection dropped cleanly
		case stopAt:
			cancel()
			return ctx.Err()
		}
		return errors.New("slack socket: invalid_auth")
	}
	var waits []time.Duration
	wait := func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		return nil
	}
	socketLoop(ctx, base, log, run, wait)

	want := []time.Duration{
		1 * time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second,
		16 * time.Second, 32 * time.Second, 64 * time.Second, 128 * time.Second,
		256 * time.Second, 512 * time.Second, socketRetryMax, socketRetryMax, // 1024 s > 15 min
		base,                             // after the connected attempt
		2 * time.Second, 4 * time.Second, // the failures after it continue from 1 s
	}
	if !slices.Equal(waits, want) {
		t.Fatalf("waits = %v\nwant    %v", waits, want)
	}
	if attempt != stopAt {
		t.Fatalf("attempts = %d, want %d", attempt, stopAt)
	}
	// 14 failures logged (12 + 2); the clean drop and the cancelled attempt log nothing.
	if n := strings.Count(logs.String(), "socket failed"); n != 14 {
		t.Fatalf("socket failed logged %d times, want 14:\n%s", n, logs.String())
	}
}

// TestSocketLoopCancelledWait: a cancelled context ends the wait at once and
// the loop does not run again.
func TestSocketLoopCancelledWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	runs := 0
	run := func(context.Context, func()) error { runs++; return errors.New("down") }
	start := time.Now()
	done := make(chan struct{})
	go func() {
		defer close(done)
		socketLoop(ctx, time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil)), run, sleep)
	}()
	time.Sleep(20 * time.Millisecond) // the loop is in its one-hour wait
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("socketLoop did not return after cancel")
	}
	if runs != 1 || time.Since(start) > 5*time.Second {
		t.Fatalf("runs = %d, elapsed %v", runs, time.Since(start))
	}
}
