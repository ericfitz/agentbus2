package core

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/store/sqlite"
)

const startMs = 1_700_000_000_000

// testClock is a settable clock.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// newTestCore opens a SQLite store on a temp path and builds a Core over it
// with a controllable clock.
func newTestCore(t *testing.T, mod func(*Config)) (*Core, *testClock) {
	t.Helper()
	s, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "agentbus2.db"), sqlite.Options{})
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	cfg := Defaults()
	if mod != nil {
		mod(&cfg)
	}
	clk := &testClock{t: time.UnixMilli(startMs)}
	c, err := New(s, cfg, Options{Now: clk.now})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c, clk
}

// mustTx runs fn in a Store.Tx and fails the test on error.
func mustTx(t *testing.T, c *Core, fn func(ctx context.Context, tx store.Tx) error) {
	t.Helper()
	if err := c.Store.Tx(context.Background(), fn); err != nil {
		t.Fatalf("tx: %v", err)
	}
}

// mustChannel creates a channel through the Store.
func mustChannel(t *testing.T, c *Core, cc store.ChannelCreate) store.Channel {
	t.Helper()
	var ch store.Channel
	mustTx(t, c, func(ctx context.Context, tx store.Tx) error {
		var err error
		cc.CreatedAt = c.NowMs()
		ch, err = tx.CreateChannel(ctx, cc)
		return err
	})
	return ch
}

func mustIdentity(t *testing.T, c *Core, name string, admin bool) {
	t.Helper()
	mustTx(t, c, func(ctx context.Context, tx store.Tx) error {
		if err := tx.CreateIdentity(ctx, name, c.NowMs()); err != nil {
			return err
		}
		if admin {
			return tx.SetAdmin(ctx, name, true)
		}
		return nil
	})
}
