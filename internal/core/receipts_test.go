package core

import (
	"context"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/store"
)

type rcpt struct{ N int }

// receiptCall runs WithReceipt in its own transaction and reports the result,
// whether fn ran, and whether it was a replay.
func receiptCall(t *testing.T, c *Core, identity, key string, payload any, n int) (res rcpt, ran, replayed bool, err error) {
	t.Helper()
	err = c.Store.Tx(context.Background(), func(ctx context.Context, tx store.Tx) error {
		ran = false
		res = rcpt{}
		var e error
		replayed, e = c.WithReceipt(ctx, tx, identity, key, payload, &res, func() error {
			ran = true
			res = rcpt{N: n}
			return nil
		})
		return e
	})
	return
}

func TestWithReceiptReplaysEqualPayload(t *testing.T) {
	c, _ := newTestCore(t, nil)
	if res, ran, replayed, err := receiptCall(t, c, "pat", "k1", map[string]any{"a": 1, "b": 2}, 7); err != nil || !ran || replayed || res.N != 7 {
		t.Fatalf("first = %+v ran=%v replayed=%v err=%v", res, ran, replayed, err)
	}
	// Same payload, different map construction order, different fn result.
	res, ran, replayed, err := receiptCall(t, c, "pat", "k1", map[string]any{"b": 2, "a": 1}, 99)
	if err != nil || ran || !replayed || res.N != 7 {
		t.Fatalf("second = %+v ran=%v replayed=%v err=%v; want the stored result, fn not run", res, ran, replayed, err)
	}
}

func TestWithReceiptConflictOnDifferentPayload(t *testing.T) {
	c, _ := newTestCore(t, nil)
	if _, _, _, err := receiptCall(t, c, "pat", "k1", "one", 1); err != nil {
		t.Fatal(err)
	}
	_, ran, _, err := receiptCall(t, c, "pat", "k1", "two", 2)
	if bus.CodeOf(err) != bus.CodeConflict || ran {
		t.Fatalf("err = %v ran=%v, want conflict and fn not run", err, ran)
	}
}

func TestWithReceiptExpired(t *testing.T) {
	c, clk := newTestCore(t, nil)
	if _, _, _, err := receiptCall(t, c, "pat", "k1", "p", 1); err != nil {
		t.Fatal(err)
	}
	clk.advance(time.Duration(c.Cfg.ReceiptRetentionMinutes)*time.Minute + time.Second)
	res, ran, replayed, err := receiptCall(t, c, "pat", "k1", "p", 2)
	if err != nil || !ran || replayed || res.N != 2 {
		t.Fatalf("after expiry = %+v ran=%v replayed=%v err=%v; want fn to run again", res, ran, replayed, err)
	}
}

func TestWithReceiptEmptyKeyRunsAlways(t *testing.T) {
	c, _ := newTestCore(t, nil)
	for i := 1; i <= 2; i++ {
		res, ran, replayed, err := receiptCall(t, c, "pat", "", "p", i)
		if err != nil || !ran || replayed || res.N != i {
			t.Fatalf("call %d = %+v ran=%v replayed=%v err=%v", i, res, ran, replayed, err)
		}
	}
}

func TestWithReceiptScopedPerIdentity(t *testing.T) { // Decision 67
	c, _ := newTestCore(t, nil)
	if _, _, _, err := receiptCall(t, c, "pat", "k1", "p", 1); err != nil {
		t.Fatal(err)
	}
	res, ran, replayed, err := receiptCall(t, c, "kim", "k1", "other payload", 2)
	if err != nil || !ran || replayed || res.N != 2 {
		t.Fatalf("kim's call with pat's key = %+v ran=%v replayed=%v err=%v; want an independent run", res, ran, replayed, err)
	}
}

func TestWithReceiptFnErrorStoresNothing(t *testing.T) {
	c, _ := newTestCore(t, nil)
	wantErr := bus.Errorf(bus.CodeNotFound, false, "nope")
	err := c.Store.Tx(context.Background(), func(ctx context.Context, tx store.Tx) error {
		var res rcpt
		_, e := c.WithReceipt(ctx, tx, "pat", "k1", "p", &res, func() error { return wantErr })
		return e
	})
	if err != error(wantErr) && bus.CodeOf(err) != bus.CodeNotFound {
		t.Fatalf("err = %v", err)
	}
	if _, ran, replayed, err := receiptCall(t, c, "pat", "k1", "p", 5); err != nil || !ran || replayed {
		t.Fatalf("after a failed fn: ran=%v replayed=%v err=%v; want a fresh run", ran, replayed, err)
	}
}

func TestCheckReceiptInRead(t *testing.T) {
	c, _ := newTestCore(t, nil)
	ctx := context.Background()
	check := func(key string, payload any) (res rcpt, replayed bool, fp string, err error) {
		err = c.Store.Read(ctx, func(ctx context.Context, tx store.Tx) error {
			res = rcpt{}
			var e error
			replayed, fp, e = c.CheckReceipt(ctx, tx, "pat", key, payload, &res)
			return e
		})
		return
	}
	if _, replayed, fp, err := check("k1", "one"); err != nil || replayed || fp == "" {
		t.Fatalf("miss: replayed=%v fp=%q err=%v, want a miss with a fingerprint", replayed, fp, err)
	}
	if _, replayed, fp, err := check("", "one"); err != nil || replayed || fp != "" {
		t.Fatalf("empty key: replayed=%v fp=%q err=%v, want false, empty, nil", replayed, fp, err)
	}
	if _, _, _, err := receiptCall(t, c, "pat", "k1", "one", 5); err != nil {
		t.Fatal(err)
	}
	if res, replayed, _, err := check("k1", "one"); err != nil || !replayed || res.N != 5 {
		t.Fatalf("hit: res=%+v replayed=%v err=%v, want the stored result", res, replayed, err)
	}
	if _, replayed, _, err := check("k1", "two"); bus.CodeOf(err) != bus.CodeConflict || replayed {
		t.Fatalf("other payload: replayed=%v err=%v, want conflict", replayed, err)
	}
	if _, replayed, _, err := check("k-other", "one"); err != nil || replayed {
		t.Fatalf("a read wrote a receipt: replayed=%v err=%v", replayed, err)
	}
}

func TestStoreReceiptThenCheck(t *testing.T) {
	c, _ := newTestCore(t, nil)
	ctx := context.Background()
	var fp string
	err := c.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		var res rcpt
		var e error
		_, fp, e = c.CheckReceipt(ctx, tx, "pat", "k1", "p", &res)
		if e != nil {
			return e
		}
		if e = c.StoreReceipt(ctx, tx, "pat", "", fp, rcpt{N: 1}); e != nil {
			return e
		}
		return c.StoreReceipt(ctx, tx, "pat", "k1", fp, rcpt{N: 9})
	})
	if err != nil {
		t.Fatal(err)
	}
	err = c.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		var res rcpt
		replayed, _, e := c.CheckReceipt(ctx, tx, "pat", "k1", "p", &res)
		if e != nil || !replayed || res.N != 9 {
			t.Errorf("after StoreReceipt: res=%+v replayed=%v err=%v", res, replayed, e)
		}
		replayed, _, e = c.CheckReceipt(ctx, tx, "pat", "", "p", &res)
		if e != nil || replayed {
			t.Errorf("empty key replayed=%v err=%v", replayed, e)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
