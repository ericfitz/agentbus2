package bus

import (
	"context"
	"strings"
	"testing"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
)

func TestUsageAdminOnly(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()

		_, err := h.as("kim").(ab2bus.Admin).Usage(ctx)
		codeOf(t, err, ab2bus.CodeForbidden)

		root := h.admin("root").(ab2bus.Admin)
		u, err := root.Usage(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if u.Bytes <= 0 {
			t.Errorf("Bytes = %d, want > 0", u.Bytes)
		}
		if want := int64(h.cfg.SQLiteBudgetMiB) << 20; u.BudgetBytes != want {
			t.Errorf("BudgetBytes = %d, want %d", u.BudgetBytes, want)
		}
		if want := e.Name == "sqlite"; u.Evictable != want {
			t.Errorf("Evictable = %v on %s", u.Evictable, e.Name)
		}
		if u.Notice != "" {
			t.Errorf("Notice = %q, want empty", u.Notice)
		}

		h.tx(func(ctx context.Context, tx ab2store.Tx) error {
			return tx.SetNotice(ctx, ab2store.Notice{Kind: "capacity", Message: "full", SetAt: 1})
		})
		u, err = root.Usage(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if u.Notice != "full" {
			t.Errorf("Notice = %q, want full", u.Notice)
		}
	})
}

func TestUsageGrowsWithData(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		h.channel("dev", ab2store.KindOrdinary, "pat", nil)
		pat := h.as("pat")
		root := h.admin("root").(ab2bus.Admin)
		before, err := root.Usage(ctx)
		if err != nil {
			t.Fatal(err)
		}
		body := strings.Repeat("x", 1024)
		for i := 0; i < 100; i++ {
			if _, err := pat.Send(ctx, ab2bus.SendInput{Channel: "dev", Content: body}); err != nil {
				t.Fatal(err)
			}
		}
		after, err := root.Usage(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if after.Bytes < before.Bytes || (e.Name == "sqlite" && after.Bytes <= before.Bytes) {
			t.Errorf("Bytes %d -> %d after 100 KiB of messages", before.Bytes, after.Bytes)
		}
	})
}
