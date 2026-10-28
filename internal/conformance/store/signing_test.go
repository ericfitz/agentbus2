package store

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/store/sqlstore"
)

const testPEM = "-----BEGIN PRIVATE KEY-----\nMC4CAQAwBQYDK2VwBCIEIA==\n-----END PRIVATE KEY-----\n"

// signingKey is an active store-provider row.
func signingKey(kid string) ab2store.SigningKey {
	return ab2store.SigningKey{
		KID: kid, State: ab2store.SigningKeyActive, Provider: ab2store.SigningProviderStore,
		Algorithm: "EdDSA", PublicKey: []byte{1, 2, 3}, PrivateKeyPEM: testPEM, CreatedAt: fixedNow,
	}
}

// retiringKey is a retiring store-provider row.
func retiringKey(kid string, retiredAt, removeAfter int64) ab2store.SigningKey {
	k := signingKey(kid)
	k.State = ab2store.SigningKeyRetiring
	k.RetiredAt, k.RemoveAfter = retiredAt, removeAfter
	return k
}

func putSigning(t *testing.T, s ab2store.Store, ks ...ab2store.SigningKey) {
	t.Helper()
	mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
		for _, k := range ks {
			if err := tx.PutSigningKey(ctx, k); err != nil {
				return err
			}
		}
		return nil
	})
}

func listSigning(t *testing.T, s ab2store.Store) []ab2store.SigningKey {
	t.Helper()
	var got []ab2store.SigningKey
	mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
		var err error
		got, err = tx.ListSigningKeys(ctx)
		return err
	})
	return got
}

func kids(ks []ab2store.SigningKey) []string {
	out := []string{}
	for _, k := range ks {
		out = append(out, k.KID)
	}
	return out
}

func TestSigningKeyPutList(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		if got := listSigning(t, s); got == nil || len(got) != 0 {
			t.Errorf("empty store: %#v, want an empty non-nil slice", got)
		}
		active := signingKey("act")
		active.Algorithm = "ES256"
		r1 := retiringKey("r-old", 1000, 5000)
		r2 := retiringKey("r-new-b", 2000, 6000)
		r3 := retiringKey("r-new-a", 2000, 6000)
		ext := retiringKey("r-ext", 500, 700)
		ext.Provider, ext.ProviderRef, ext.PrivateKeyPEM = "awskms", "arn:aws:kms:x", ""
		want := []ab2store.SigningKey{active, r3, r2, r1, ext} // active, then retired_at desc, then kid asc
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			for _, k := range []ab2store.SigningKey{r1, r2, active, ext, r3} {
				if err := tx.PutSigningKey(ctx, k); err != nil {
					return err
				}
			}
			got, err := tx.ListSigningKeys(ctx) // visible inside the writing Tx
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Errorf("in Tx: %+v, %v; want %+v", got, err, want)
			}
			return nil
		})
		if got := listSigning(t, s); !reflect.DeepEqual(got, want) {
			t.Errorf("in Read: %+v; want %+v", got, want)
		}
	})
}

func TestSigningKeyRejects(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		put := func(k ab2store.SigningKey) error {
			return s.Tx(context.Background(), func(ctx context.Context, tx ab2store.Tx) error { return tx.PutSigningKey(ctx, k) })
		}
		mod := func(f func(*ab2store.SigningKey)) ab2store.SigningKey {
			k := signingKey("k1")
			f(&k)
			return k
		}
		ext := func(f func(*ab2store.SigningKey)) ab2store.SigningKey {
			return mod(func(k *ab2store.SigningKey) {
				k.Provider, k.ProviderRef, k.PrivateKeyPEM = "gcpkms", "projects/p/keys/k", ""
				f(k)
			})
		}
		for name, k := range map[string]ab2store.SigningKey{
			"empty KID":              mod(func(k *ab2store.SigningKey) { k.KID = "" }),
			"empty public key":       mod(func(k *ab2store.SigningKey) { k.PublicKey = nil }),
			"state next":             mod(func(k *ab2store.SigningKey) { k.State = "next" }),
			"empty state":            mod(func(k *ab2store.SigningKey) { k.State = "" }),
			"provider vault":         mod(func(k *ab2store.SigningKey) { k.Provider = "vault" }),
			"empty provider":         mod(func(k *ab2store.SigningKey) { k.Provider = "" }),
			"RS256":                  mod(func(k *ab2store.SigningKey) { k.Algorithm = "RS256" }),
			"empty algo":             mod(func(k *ab2store.SigningKey) { k.Algorithm = "" }),
			"lower eddsa":            mod(func(k *ab2store.SigningKey) { k.Algorithm = "eddsa" }),
			"no created":             mod(func(k *ab2store.SigningKey) { k.CreatedAt = 0 }),
			"active with retired_at": mod(func(k *ab2store.SigningKey) { k.RetiredAt = 5 }),
			"active with remove":     mod(func(k *ab2store.SigningKey) { k.RemoveAfter = 5 }),
			"retiring no retired_at": retiringKey("k1", 0, 10),
			"retiring remove early":  retiringKey("k1", 10, 9),
			"store empty PEM":        mod(func(k *ab2store.SigningKey) { k.PrivateKeyPEM = "" }),
			"NUL in PEM":             mod(func(k *ab2store.SigningKey) { k.PrivateKeyPEM = "a\x00b" }),
			"store with ref":         mod(func(k *ab2store.SigningKey) { k.ProviderRef = "ref" }),
			"external with PEM":      ext(func(k *ab2store.SigningKey) { k.PrivateKeyPEM = testPEM }),
			"external no ref":        ext(func(k *ab2store.SigningKey) { k.ProviderRef = "" }),
		} {
			if err := put(k); !errors.Is(err, ab2store.ErrInvalid) {
				t.Errorf("%s: err = %v, want ErrInvalid", name, err)
			}
		}
		if got := listSigning(t, s); len(got) != 0 {
			t.Fatalf("rejected puts left rows: %v", kids(got))
		}
		first := signingKey("first")
		if err := put(first); err != nil {
			t.Fatalf("first put: %v", err)
		}
		if err := put(signingKey("second")); !errors.Is(err, ab2store.ErrConflict) {
			t.Errorf("second active put: %v, want ErrConflict", err)
		}
		if err := put(retiringKey("first", 10, 20)); !errors.Is(err, ab2store.ErrConflict) {
			t.Errorf("duplicate kid: %v, want ErrConflict", err)
		}
		if got := listSigning(t, s); !reflect.DeepEqual(got, []ab2store.SigningKey{first}) {
			t.Errorf("after conflicts: %+v; want the first row unchanged", got)
		}
	})
}

// TestSigningKeyPutConcurrent: eight writers over two Store handles each
// insert a different active row; the partial unique index lets exactly one in.
func TestSigningKeyPutConcurrent(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s1, reopen, rawDSN := openStore(t, e, sqlstore.Options{})
		s2, err := reopen(sqlstore.Options{})
		if err != nil {
			t.Fatalf("second handle: %v", err)
		}
		t.Cleanup(func() { _ = s2.Close() })
		handles := []ab2store.Store{s1, s2}
		const n = 8
		var wg sync.WaitGroup
		errs := make([]error, n)
		start := make(chan struct{})
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				errs[i] = handles[i%2].Tx(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
					return tx.PutSigningKey(ctx, signingKey(fmt.Sprintf("kid-%d", i)))
				})
			}()
		}
		close(start)
		wg.Wait()
		winner := -1
		for i, err := range errs {
			switch {
			case err == nil:
				if winner >= 0 {
					t.Fatalf("two puts succeeded: %d and %d", winner, i)
				}
				winner = i
			case errors.Is(err, ab2store.ErrConflict):
			default:
				t.Errorf("put %d: %v", i, err)
			}
		}
		if winner < 0 {
			t.Fatal("no put succeeded")
		}
		if got := scalar[int](t, rawDB(t, e, rawDSN), "SELECT count(*) FROM signing_keys"); got != 1 {
			t.Fatalf("signing_keys rows = %d, want 1", got)
		}
		got := listSigning(t, s2)
		if want := fmt.Sprintf("kid-%d", winner); len(got) != 1 || got[0].KID != want {
			t.Errorf("rows = %v, want kid %s", kids(got), want)
		}
	})
}

func TestSigningKeyRetireCAS(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		putSigning(t, s, signingKey("a"))
		retire := func(kid string, at, after int64) error {
			return s.Tx(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
				return tx.RetireSigningKey(ctx, kid, at, after)
			})
		}
		if err := retire("a", 1000, 9000); err != nil {
			t.Fatalf("retire: %v", err)
		}
		want := signingKey("a")
		want.State, want.RetiredAt, want.RemoveAfter = ab2store.SigningKeyRetiring, 1000, 9000
		if got := listSigning(t, s); !reflect.DeepEqual(got, []ab2store.SigningKey{want}) {
			t.Errorf("after retire: %+v; want %+v", got, want)
		}
		if err := retire("a", 2000, 9500); !errors.Is(err, ab2store.ErrNotFound) {
			t.Errorf("second retire: %v, want ErrNotFound", err)
		}
		if err := retire("nope", 2000, 9500); !errors.Is(err, ab2store.ErrNotFound) {
			t.Errorf("unknown kid: %v, want ErrNotFound", err)
		}
		if got := listSigning(t, s); !reflect.DeepEqual(got, []ab2store.SigningKey{want}) {
			t.Errorf("after failed retires: %+v; want unchanged", got)
		}
		putSigning(t, s, signingKey("b")) // the slot is free again
		if got := kids(listSigning(t, s)); !reflect.DeepEqual(got, []string{"b", "a"}) {
			t.Errorf("kids = %v, want [b a]", got)
		}
		// A retire that would break the row's invariants is refused.
		if err := retire("b", 0, 10); !errors.Is(err, ab2store.ErrInvalid) {
			t.Errorf("retire with retired_at 0: %v, want ErrInvalid", err)
		}
		if err := retire("b", 10, 9); !errors.Is(err, ab2store.ErrInvalid) {
			t.Errorf("retire with remove_after < retired_at: %v, want ErrInvalid", err)
		}
	})
}

func TestSigningKeyDelete(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		putSigning(t, s, signingKey("a"), retiringKey("r", 10, 20))
		del := func(kid string) error {
			return s.Tx(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
				return tx.DeleteSigningKey(ctx, kid)
			})
		}
		for _, kid := range []string{"r", "a"} {
			if err := del(kid); err != nil {
				t.Errorf("delete %s: %v", kid, err)
			}
			if err := del(kid); !errors.Is(err, ab2store.ErrNotFound) {
				t.Errorf("delete %s again: %v, want ErrNotFound", kid, err)
			}
		}
		if got := listSigning(t, s); len(got) != 0 {
			t.Errorf("rows left: %v", kids(got))
		}
	})
}

func TestSigningKeyDeleteBefore(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		putSigning(t, s, signingKey("act"), retiringKey("r100", 10, 100), retiringKey("r200", 10, 200), retiringKey("r300", 10, 300))
		before := func(now int64) int {
			var n int
			mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
				var err error
				n, err = tx.DeleteSigningKeysBefore(ctx, now)
				return err
			})
			return n
		}
		if n := before(250); n != 2 {
			t.Errorf("DeleteSigningKeysBefore(250) = %d, want 2", n)
		}
		if n := before(250); n != 0 {
			t.Errorf("again = %d, want 0", n)
		}
		if got := kids(listSigning(t, s)); !reflect.DeepEqual(got, []string{"act", "r300"}) {
			t.Errorf("kids = %v, want [act r300]", got)
		}
		if n := before(1 << 40); n != 1 { // never the active row
			t.Errorf("far future = %d, want 1", n)
		}
		if got := kids(listSigning(t, s)); !reflect.DeepEqual(got, []string{"act"}) {
			t.Errorf("kids = %v, want [act]", got)
		}
	})
}

func TestSigningKeyReadOnly(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		putSigning(t, s, signingKey("a"))
		for name, fn := range map[string]func(context.Context, ab2store.Tx) error{
			"put":    func(ctx context.Context, tx ab2store.Tx) error { return tx.PutSigningKey(ctx, retiringKey("b", 1, 2)) },
			"retire": func(ctx context.Context, tx ab2store.Tx) error { return tx.RetireSigningKey(ctx, "a", 1, 2) },
			"delete": func(ctx context.Context, tx ab2store.Tx) error { return tx.DeleteSigningKey(ctx, "a") },
			"delete before": func(ctx context.Context, tx ab2store.Tx) error {
				_, err := tx.DeleteSigningKeysBefore(ctx, 1<<40)
				return err
			},
		} {
			if err := s.Read(context.Background(), fn); !errors.Is(err, ab2store.ErrReadOnly) {
				t.Errorf("%s in Read: %v, want ErrReadOnly", name, err)
			}
		}
		if got := listSigning(t, s); len(got) != 1 {
			t.Errorf("rows = %v, want the one active row", kids(got))
		}
	})
}
