package bus

import (
	"context"
	"strings"
	"testing"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
)

func TestUsageAdminOnly(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		cfg := core.Defaults()
		cfg.SQLiteBudgetMiB, cfg.PostgresBudgetMiB = 100, 200
		h := newHarness(t, e, harnessOptions{cfg: &cfg})
		ctx := context.Background()

		_, err := h.as("kim").(ab2bus.Admin).Usage(ctx)
		codeOf(t, err, ab2bus.CodeForbidden)

		h.channel("dev", ab2store.KindOrdinary, "pat", nil)
		if _, err := h.as("pat").Send(ctx, ab2bus.SendInput{Channel: "dev", Content: "hello"}); err != nil {
			t.Fatal(err)
		}
		root := h.admin("root").(ab2bus.Admin)
		u, err := root.Usage(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if u.Bytes <= 0 {
			t.Errorf("Bytes = %d on %s", u.Bytes, e.Name)
		}
		want := int64(100) << 20
		if e.Name == "postgres" {
			want = int64(200) << 20
		}
		if u.BudgetBytes != want {
			t.Errorf("BudgetBytes = %d, want %d", u.BudgetBytes, want)
		}
		if !u.Evictable {
			t.Errorf("Evictable = false on %s", e.Name)
		}
		if u.DiskBytes <= 0 || u.DiskRatio <= 0 || u.DiskRatio != float64(u.DiskBytes)/float64(u.Bytes) {
			t.Errorf("DiskBytes = %d, DiskRatio = %v, Bytes = %d", u.DiskBytes, u.DiskRatio, u.Bytes)
		}
		if u.Tier != ab2bus.PressureNormal {
			t.Errorf("Tier = %q, want normal", u.Tier)
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
