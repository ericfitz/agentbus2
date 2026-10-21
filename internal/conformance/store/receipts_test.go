package store

import (
	"context"
	"errors"
	"math"
	"strconv"
	"testing"

	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/store/sqlstore"
)

func receiptPut(t *testing.T, s ab2store.Store, r ab2store.Receipt) {
	t.Helper()
	mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error { return tx.PutReceipt(ctx, r) })
}

func receiptGet(t *testing.T, s ab2store.Store, identity, key string, now int64) (r ab2store.Receipt, ok bool) {
	t.Helper()
	mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) (err error) {
		r, ok, err = tx.Receipt(ctx, identity, key, now)
		return err
	})
	return r, ok
}

func TestReceiptRoundTrip(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		want := ab2store.Receipt{Identity: "pat", Key: "k1", Fingerprint: "fp1", Result: []byte(`{"seq":7}`), ExpiresAt: fixedNow + 1000}
		receiptPut(t, s, want)
		got, ok := receiptGet(t, s, "pat", "k1", fixedNow)
		if !ok || got.Identity != want.Identity || got.Key != want.Key || got.Fingerprint != want.Fingerprint ||
			string(got.Result) != string(want.Result) || got.ExpiresAt != want.ExpiresAt {
			t.Errorf("Receipt = %+v, %v; want %+v", got, ok, want)
		}
		if _, ok := receiptGet(t, s, "pat", "k1", want.ExpiresAt-1); !ok {
			t.Error("receipt not found just before expiry")
		}
		if _, ok := receiptGet(t, s, "pat", "k1", want.ExpiresAt); ok {
			t.Error("receipt found at now == ExpiresAt")
		}
		if _, ok := receiptGet(t, s, "pat", "k1", want.ExpiresAt+1); ok {
			t.Error("receipt found after expiry")
		}
		// Decision 67: scoped to the identity.
		if _, ok := receiptGet(t, s, "kim", "k1", fixedNow); ok {
			t.Error("receipt visible to another identity")
		}
	})
}

func TestReceiptReplace(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		receiptPut(t, s, ab2store.Receipt{Identity: "pat", Key: "k", Fingerprint: "old", Result: []byte("1"), ExpiresAt: fixedNow + 10})
		receiptPut(t, s, ab2store.Receipt{Identity: "pat", Key: "k", Fingerprint: "new", Result: []byte("2"), ExpiresAt: fixedNow + 20})
		got, ok := receiptGet(t, s, "pat", "k", fixedNow)
		if !ok || got.Fingerprint != "new" || string(got.Result) != "2" || got.ExpiresAt != fixedNow+20 {
			t.Errorf("Receipt = %+v, %v; want the replacement", got, ok)
		}
	})
}

func TestReceiptDeleteExpired(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		for i, exp := range []int64{fixedNow - 2, fixedNow, fixedNow + 5} {
			receiptPut(t, s, ab2store.Receipt{Identity: "pat", Key: string(rune('a' + i)), Fingerprint: "f", Result: []byte("r"), ExpiresAt: exp})
		}
		del := func(limit int) int {
			var n int
			mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) (err error) {
				n, err = tx.DeleteExpiredReceipts(ctx, fixedNow, limit)
				return err
			})
			return n
		}
		for i, want := range []int{1, 1, 0} {
			if got := del(1); got != want {
				t.Errorf("delete #%d = %d, want %d", i+1, got, want)
			}
		}
		if _, ok := receiptGet(t, s, "pat", "c", fixedNow); !ok {
			t.Error("unexpired receipt was deleted")
		}
	})
}

// PutReceipt refuses an empty identity or key and an Identity, Key,
// Fingerprint or Result that is not UTF-8 (the columns are TEXT: SQLite stored
// such bytes while Postgres failed with a raw SQLSTATE 22021).
func TestReceiptRejectsInvalid(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		ok := ab2store.Receipt{Identity: "pat", Key: "k", Fingerprint: "f", Result: []byte(`{"seq":1}`), ExpiresAt: fixedNow + 10}
		for name, mutate := range map[string]func(*ab2store.Receipt){
			"empty identity":        func(r *ab2store.Receipt) { r.Identity = "" },
			"empty key":             func(r *ab2store.Receipt) { r.Key = "" },
			"non-UTF-8":             func(r *ab2store.Receipt) { r.Result = []byte{0xff, 0xfe} },
			"non-UTF-8 identity":    func(r *ab2store.Receipt) { r.Identity = "pat\xff" },
			"non-UTF-8 key":         func(r *ab2store.Receipt) { r.Key = "k\xff" },
			"non-UTF-8 fingerprint": func(r *ab2store.Receipt) { r.Fingerprint = "f\xff" },
		} {
			r := ok
			mutate(&r)
			err := s.Tx(context.Background(), func(ctx context.Context, tx ab2store.Tx) error { return tx.PutReceipt(ctx, r) })
			if !errors.Is(err, ab2store.ErrInvalid) {
				t.Errorf("PutReceipt(%s) err = %v, want ErrInvalid", name, err)
			}
		}
		// Nothing was stored by the rejected puts.
		if _, found := receiptGet(t, s, "pat", "k", fixedNow); found {
			t.Error("a rejected PutReceipt stored a receipt")
		}
		if _, found := receiptGet(t, s, "", "", fixedNow); found {
			t.Error("a rejected PutReceipt stored a receipt with an empty identity and key")
		}
		// Count every row without binding the rejected strings (Postgres refuses
		// them even in a SELECT): deleting all receipts expired by the end of
		// time deletes every receipt.
		var all int
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) (err error) {
			all, err = tx.DeleteExpiredReceipts(ctx, math.MaxInt64, 100)
			return err
		})
		if all != 0 {
			t.Errorf("rejected PutReceipts stored %d receipts, want 0", all)
		}
		receiptPut(t, s, ok) // the same receipt with valid fields is stored
		if _, found := receiptGet(t, s, "pat", "k", fixedNow); !found {
			t.Error("a valid receipt was not stored")
		}
	})
}

// DeleteExpiredReceipts deletes in chunks: one statement for the whole limit
// binds 2 variables per key and nests one OR per key, which exceeds SQLite's
// 32766 variables and its expression depth of 1000.
func TestReceiptDeleteExpiredBeyondBindLimit(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		const n = 17000 // 34000 variables in one statement
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			for i := range n {
				r := ab2store.Receipt{Identity: "pat", Key: "k" + strconv.Itoa(i), Fingerprint: "f", Result: []byte("r"), ExpiresAt: fixedNow - 1}
				if err := tx.PutReceipt(ctx, r); err != nil {
					return err
				}
			}
			return tx.PutReceipt(ctx, ab2store.Receipt{Identity: "pat", Key: "live", Fingerprint: "f", Result: []byte("r"), ExpiresAt: fixedNow + 1})
		})
		del := func(limit int) int {
			var got int
			mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) (err error) {
				got, err = tx.DeleteExpiredReceipts(ctx, fixedNow, limit)
				return err
			})
			return got
		}
		// 1000 is the maintenance sweep's chunk (v1 cleanupChunk): a single OR
		// of 1000 key pairs exceeds SQLite's expression depth of 1000.
		if got := del(1000); got != 1000 {
			t.Errorf("DeleteExpiredReceipts(1000) = %d, want 1000", got)
		}
		if got := del(n + 1000); got != n-1000 {
			t.Errorf("DeleteExpiredReceipts(%d) = %d, want %d", n+1000, got, n-1000)
		}
		if _, found := receiptGet(t, s, "pat", "live", fixedNow); !found {
			t.Error("the unexpired receipt was deleted")
		}
		if _, found := receiptGet(t, s, "pat", "k0", fixedNow-5); found {
			t.Error("an expired receipt survived")
		}
	})
}

// Every receipt write fails with ErrReadOnly inside Store.Read, with valid
// input so the guard (not validation) is what rejects it, and leaves no trace.
func TestReceiptRejectsWritesInRead(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		ctx := context.Background()
		s, _, _ := openStore(t, e, sqlstore.Options{})
		receiptPut(t, s, ab2store.Receipt{Identity: "pat", Key: "old", Fingerprint: "f", Result: []byte("1"), ExpiresAt: fixedNow - 1})
		for name, write := range map[string]func(context.Context, ab2store.Tx) error{
			"PutReceipt": func(ctx context.Context, tx ab2store.Tx) error {
				return tx.PutReceipt(ctx, ab2store.Receipt{Identity: "pat", Key: "new", Fingerprint: "f", Result: []byte("2"), ExpiresAt: fixedNow + 10})
			},
			"DeleteExpiredReceipts": func(ctx context.Context, tx ab2store.Tx) error {
				_, err := tx.DeleteExpiredReceipts(ctx, fixedNow, 10)
				return err
			},
		} {
			err := s.Read(ctx, func(ctx context.Context, tx ab2store.Tx) error { return write(ctx, tx) })
			if !errors.Is(err, ab2store.ErrReadOnly) {
				t.Errorf("%s inside Read: err = %v, want ErrReadOnly", name, err)
			}
		}
		if _, found := receiptGet(t, s, "pat", "new", fixedNow); found {
			t.Error("PutReceipt inside Read stored a receipt")
		}
		// The expired row is still there: deleting it now reports 1.
		var n int
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) (err error) {
			n, err = tx.DeleteExpiredReceipts(ctx, fixedNow, 10)
			return err
		})
		if n != 1 {
			t.Errorf("DeleteExpiredReceipts after the rejected one = %d, want 1 (the row must have survived)", n)
		}
	})
}

// Like every other limit in the Store, a limit below 1 is ErrInvalid (it used
// to return 0 without error) and deletes nothing.
func TestReceiptDeleteExpiredRejectsBadLimit(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		receiptPut(t, s, ab2store.Receipt{Identity: "pat", Key: "old", Fingerprint: "f", Result: []byte("1"), ExpiresAt: fixedNow - 1})
		for _, limit := range []int{0, -1} {
			err := s.Tx(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
				_, err := tx.DeleteExpiredReceipts(ctx, fixedNow, limit)
				return err
			})
			if !errors.Is(err, ab2store.ErrInvalid) {
				t.Errorf("DeleteExpiredReceipts(limit %d) err = %v, want ErrInvalid", limit, err)
			}
		}
		var n int
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) (err error) {
			n, err = tx.DeleteExpiredReceipts(ctx, fixedNow, 10)
			return err
		})
		if n != 1 {
			t.Errorf("DeleteExpiredReceipts after the rejected limits = %d, want 1", n)
		}
	})
}

// Receipt with an identity or key that is not valid UTF-8 finds nothing on
// both engines (PutReceipt refuses such strings, so none is stored); Postgres
// used to fail with a raw SQLSTATE 22021 while SQLite answered not found.
func TestReceiptLookupOfNonUTF8(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		receiptPut(t, s, ab2store.Receipt{Identity: "pat", Key: "k", Fingerprint: "f", Result: []byte(`{}`), ExpiresAt: fixedNow + 10})
		for _, ik := range [][2]string{{"pat\xff", "k"}, {"pat", "k\xff"}} {
			err := s.Read(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
				_, found, err := tx.Receipt(ctx, ik[0], ik[1], fixedNow)
				if err == nil && found {
					t.Errorf("Receipt(%q, %q) found a receipt", ik[0], ik[1])
				}
				return err
			})
			if err != nil {
				t.Errorf("Receipt(%q, %q) err = %v, want nil (not found)", ik[0], ik[1], err)
			}
		}
	})
}
