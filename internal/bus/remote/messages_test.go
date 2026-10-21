package remote

import (
	"context"
	"testing"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
)

const notFoundProblem = `{"type":"about:blank","title":"Not Found","status":404,"code":"not_found","detail":"no matching operation was found","retryable":false}`

// TestSendEmptyChannelIsValidationWithoutARequest pins the empty-name guard:
// core Send answers validation for Channel "", while the wire's empty path
// segment is the router's 404 not_found, so the remote Bus answers validation
// itself and sends nothing.
// pending Eric: Milestone 4 item N (A: keep the guard; B: core answers
// not_found for an empty channel and the guard goes).
func TestSendEmptyChannelIsValidationWithoutARequest(t *testing.T) {
	c := &canned{status: 404, ctype: "application/problem+json", body: notFoundProblem}
	b := newBus(t, c)
	_, err := b.Send(context.Background(), ab2bus.SendInput{Content: "x"})
	if got := ab2bus.CodeOf(err); got != ab2bus.CodeValidation {
		t.Fatalf("code = %q (%v), want validation", got, err)
	}
	if n := c.n.Load(); n != 0 {
		t.Fatalf("%d requests were sent, want 0", n)
	}
}

// TestHistoryEmptyChannelIsUnguarded: the core answers not_found for a lookup
// by "" (ChannelByName), the same code the wire's empty segment gives, so the
// remote Bus adds no rule and sends what the generated client builds.
func TestHistoryEmptyChannelIsUnguarded(t *testing.T) {
	c := &canned{status: 404, ctype: "application/problem+json", body: notFoundProblem}
	b := newBus(t, c)
	_, err := b.History(context.Background(), ab2bus.HistoryInput{})
	if got := ab2bus.CodeOf(err); got != ab2bus.CodeNotFound {
		t.Fatalf("code = %q (%v), want not_found", got, err)
	}
	if n := c.n.Load(); n != 1 {
		t.Fatalf("%d requests were sent, want 1", n)
	}
}
