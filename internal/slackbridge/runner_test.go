package slackbridge

import (
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
