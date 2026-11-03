package core

import (
	"context"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/store"
)

func setNotice(t *testing.T, c *Core, kind string, setAt int64) {
	t.Helper()
	mustTx(t, c, func(ctx context.Context, tx store.Tx) error {
		return tx.SetNotice(ctx, store.Notice{Kind: kind, Message: kind + " notice", SetAt: setAt})
	})
}

func readNotices(t *testing.T, c *Core) []string {
	t.Helper()
	var kinds []string
	mustTx(t, c, func(ctx context.Context, tx store.Tx) error {
		ns, err := c.Notices(ctx, tx)
		for _, n := range ns {
			kinds = append(kinds, n.Kind)
		}
		return err
	})
	return kinds
}

func TestNoticesInspectionExpiry(t *testing.T) {
	c, clk := newTestCore(t, func(cfg *Config) { cfg.InspectionNoticeTTLSeconds = 300 })
	setNotice(t, c, NoticeInspection, startMs)
	clk.advance(299_999 * time.Millisecond)
	if got := readNotices(t, c); len(got) != 1 || got[0] != NoticeInspection {
		t.Fatalf("at +299999 ms notices = %v, want [inspection]", got)
	}
	clk.advance(time.Millisecond)
	if got := readNotices(t, c); len(got) != 0 {
		t.Fatalf("at +300000 ms notices = %v, want none", got)
	}
}

func TestNoticesOtherKindsNeverExpire(t *testing.T) {
	c, clk := newTestCore(t, nil)
	setNotice(t, c, NoticeEmbedding, startMs)
	setNotice(t, c, NoticeCapacity, startMs)
	clk.advance(48 * time.Hour)
	got := readNotices(t, c)
	if len(got) != 2 || got[0] != NoticeCapacity || got[1] != NoticeEmbedding {
		t.Fatalf("notices = %v, want [capacity embedding]", got)
	}
}

func TestNoticesFutureSetAt(t *testing.T) {
	c, _ := newTestCore(t, nil)
	setNotice(t, c, NoticeInspection, startMs+10_000)
	if got := readNotices(t, c); len(got) != 1 || got[0] != NoticeInspection {
		t.Fatalf("notices = %v, want [inspection]", got)
	}
}
