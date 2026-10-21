package store

import (
	"context"
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
