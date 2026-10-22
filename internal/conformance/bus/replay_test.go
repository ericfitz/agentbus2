package bus

import (
	"context"
	"errors"
	"testing"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
)

func TestMaintenanceAssertionReplayExpiry(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		now := h.core.NowMs()
		h.tx(func(ctx context.Context, tx ab2store.Tx) error {
			if err := tx.PutAssertionJTI(ctx, "alice", "past", now-1); err != nil {
				return err
			}
			return tx.PutAssertionJTI(ctx, "alice", "future", now+60_000)
		})
		if n := h.runJob("assertion replay expiry"); n != 1 {
			t.Fatalf("job deleted %d, want 1", n)
		}
		if n := h.runJob("assertion replay expiry"); n != 0 {
			t.Fatalf("second run deleted %d, want 0", n)
		}
		err := h.raw.Tx(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
			return tx.PutAssertionJTI(ctx, "alice", "future", now+60_000)
		})
		if !errors.Is(err, ab2store.ErrConflict) {
			t.Errorf("future row after the job: %v, want ErrConflict", err)
		}
		jobs := maint(h).Jobs()
		if got := jobs[len(jobs)-1].Name; got != "assertion replay expiry" {
			t.Errorf("last job = %q", got)
		}
		_, err = maint(h).RunJob(context.Background(), "vacuum")
		if ab2bus.CodeOf(err) != ab2bus.CodeValidation {
			t.Errorf("RunJob(vacuum) = %v, want validation", err)
		}
	})
}
