package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/store/sqlstore"
)

func enrollTok(id, hash string, createdAt int64) ab2store.EnrollmentToken {
	return ab2store.EnrollmentToken{ID: id, TokenHash: hash, ExpiresAt: fixedNow + 1000, UsesLeft: 1, CreatedAt: createdAt}
}

func putEnrollment(t *testing.T, s ab2store.Store, e ab2store.EnrollmentToken) {
	t.Helper()
	mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error { return tx.PutEnrollmentToken(ctx, e) })
}

func TestEnrollmentPutListGet(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		a := enrollTok("bbbbbbbbbbbbbbbb", "hash-a", 20)
		b := enrollTok("aaaaaaaaaaaaaaaa", "hash-b", 20)
		b.Identity = "pat"
		b.UsesLeft = 3
		c := enrollTok("cccccccccccccccc", "hash-c", 10)
		for _, tok := range []ab2store.EnrollmentToken{a, b, c} {
			putEnrollment(t, s, tok)
		}
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			list, err := tx.ListEnrollmentTokens(ctx)
			if err != nil {
				return err
			}
			want := []ab2store.EnrollmentToken{c, b, a} // CreatedAt, then ID
			if len(list) != len(want) {
				t.Fatalf("list = %+v", list)
			}
			for i := range want {
				if list[i] != want[i] {
					t.Errorf("list[%d] = %+v, want %+v", i, list[i], want[i])
				}
			}
			for _, tok := range want {
				got, err := tx.EnrollmentTokenByHash(ctx, tok.TokenHash)
				if err != nil || got != tok {
					t.Errorf("ByHash(%q) = %+v, %v; want %+v", tok.TokenHash, got, err, tok)
				}
			}
			if _, err := tx.EnrollmentTokenByHash(ctx, "nope"); !errors.Is(err, ab2store.ErrNotFound) {
				t.Errorf("unknown hash: %v, want ErrNotFound", err)
			}
			return nil
		})
	})
}

func TestEnrollmentRejects(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		putEnrollment(t, s, enrollTok("0123456789abcdef", "h1", 1))
		mod := func(f func(*ab2store.EnrollmentToken)) ab2store.EnrollmentToken {
			tok := enrollTok("fedcba9876543210", "h2", 1)
			f(&tok)
			return tok
		}
		invalid := map[string]ab2store.EnrollmentToken{
			"empty ID":       mod(func(x *ab2store.EnrollmentToken) { x.ID = "" }),
			"empty hash":     mod(func(x *ab2store.EnrollmentToken) { x.TokenHash = "" }),
			"uses 0":         mod(func(x *ab2store.EnrollmentToken) { x.UsesLeft = 0 }),
			"expiry 0":       mod(func(x *ab2store.EnrollmentToken) { x.ExpiresAt = 0 }),
			"identity slash": mod(func(x *ab2store.EnrollmentToken) { x.Identity = "a/b" }),
			"identity every": mod(func(x *ab2store.EnrollmentToken) { x.Identity = "everyone" }),
			"identity admin": mod(func(x *ab2store.EnrollmentToken) { x.Identity = "admins" }),
			"NUL in ID":      mod(func(x *ab2store.EnrollmentToken) { x.ID = "a\x00b" }),
		}
		for name, tok := range invalid {
			err := s.Tx(context.Background(), func(ctx context.Context, tx ab2store.Tx) error { return tx.PutEnrollmentToken(ctx, tok) })
			if !errors.Is(err, ab2store.ErrInvalid) {
				t.Errorf("%s: err = %v, want ErrInvalid", name, err)
			}
		}
		for name, tok := range map[string]ab2store.EnrollmentToken{
			"same ID":   enrollTok("0123456789abcdef", "other", 1),
			"same hash": enrollTok("1111111111111111", "h1", 1),
		} {
			err := s.Tx(context.Background(), func(ctx context.Context, tx ab2store.Tx) error { return tx.PutEnrollmentToken(ctx, tok) })
			if !errors.Is(err, ab2store.ErrConflict) {
				t.Errorf("%s: err = %v, want ErrConflict", name, err)
			}
		}
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			list, err := tx.ListEnrollmentTokens(ctx)
			if err != nil || len(list) != 1 {
				t.Errorf("after rejects: %+v, %v; want only the first token", list, err)
			}
			return nil
		})
	})
}

func TestEnrollmentConsume(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		two := enrollTok("2222222222222222", "h-two", 1)
		two.UsesLeft = 2
		putEnrollment(t, s, two)
		stale := enrollTok("3333333333333333", "h-stale", 2)
		putEnrollment(t, s, stale)

		consume := func(id string, now int64) (bool, error) {
			var ok bool
			err := s.Tx(context.Background(), func(ctx context.Context, tx ab2store.Tx) (err error) {
				ok, err = tx.ConsumeEnrollmentToken(ctx, id, now)
				return err
			})
			return ok, err
		}
		for i, want := range []bool{true, true, false} {
			if ok, err := consume(two.ID, fixedNow); err != nil || ok != want {
				t.Errorf("consume %d = %v, %v; want %v", i, ok, err, want)
			}
		}
		// Expired: false, and no decrement.
		if ok, err := consume(stale.ID, stale.ExpiresAt); err != nil || ok {
			t.Errorf("consume at expiry = %v, %v; want false", ok, err)
		}
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			got, err := tx.EnrollmentTokenByHash(ctx, stale.TokenHash)
			if err != nil || got.UsesLeft != 1 {
				t.Errorf("expired token = %+v, %v; want UsesLeft 1", got, err)
			}
			return nil
		})
		if _, err := consume("ffffffffffffffff", fixedNow); !errors.Is(err, ab2store.ErrNotFound) {
			t.Errorf("unknown id: %v, want ErrNotFound", err)
		}
		err := s.Read(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
			_, err := tx.ConsumeEnrollmentToken(ctx, two.ID, fixedNow)
			return err
		})
		if !errors.Is(err, ab2store.ErrReadOnly) {
			t.Errorf("Read consume: %v, want ErrReadOnly", err)
		}
	})
}

func TestEnrollmentDelete(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		tok := enrollTok("4444444444444444", "h4", 1)
		putEnrollment(t, s, tok)
		del := func() error {
			return s.Tx(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
				return tx.DeleteEnrollmentToken(ctx, tok.ID)
			})
		}
		if err := del(); err != nil {
			t.Fatalf("delete: %v", err)
		}
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			if _, err := tx.EnrollmentTokenByHash(ctx, tok.TokenHash); !errors.Is(err, ab2store.ErrNotFound) {
				t.Errorf("after delete: %v, want ErrNotFound", err)
			}
			return nil
		})
		if err := del(); !errors.Is(err, ab2store.ErrNotFound) {
			t.Errorf("second delete: %v, want ErrNotFound", err)
		}
	})
}

func TestEnrollmentConsumeConcurrent(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		tok := enrollTok("5555555555555555", "h5", 1)
		putEnrollment(t, s, tok)
		const n = 8
		var wg sync.WaitGroup
		wins := make([]bool, n)
		errs := make([]error, n)
		start := make(chan struct{})
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				errs[i] = s.Tx(context.Background(), func(ctx context.Context, tx ab2store.Tx) (err error) {
					wins[i], err = tx.ConsumeEnrollmentToken(ctx, tok.ID, fixedNow)
					return err
				})
			}()
		}
		close(start)
		wg.Wait()
		won := 0
		for i := range n {
			if errs[i] != nil && !errors.Is(errs[i], ab2store.ErrRetryable) {
				t.Errorf("consume %d: %v", i, errs[i])
			}
			if errs[i] == nil && wins[i] {
				won++
			}
		}
		if won != 1 {
			t.Fatalf("%d consumers won, want exactly 1 (%s)", won, fmt.Sprint(wins))
		}
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			got, err := tx.EnrollmentTokenByHash(ctx, tok.TokenHash)
			if err != nil || got.UsesLeft != 0 {
				t.Errorf("after race: %+v, %v; want UsesLeft 0", got, err)
			}
			return nil
		})
	})
}
