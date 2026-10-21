package store

import (
	"context"
	"errors"
	"testing"

	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/store/sqlstore"
)

func TestScaffold(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		db := e.Open(t)
		var one int
		if err := db.QueryRowContext(context.Background(), "SELECT 1").Scan(&one); err != nil || one != 1 {
			t.Fatalf("%s: SELECT 1 = %d, %v", e.Name, one, err)
		}
	})
}

// TestStoreImplementsEveryAggregate calls one cheap method of every aggregate
// interface inside a single Append on each engine. A regression that
// re-introduces a stub (a method returning an error instead of a result) fails
// here on both engines.
func TestStoreImplementsEveryAggregate(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		mustAppendTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			checks := []struct {
				name string
				call func() error
			}{
				{"MaxSeq", func() error { _, err := tx.MaxSeq(ctx); return err }},
				{"ListEnrollmentTokens", func() error { _, err := tx.ListEnrollmentTokens(ctx); return err }},
				{"SigningKey", func() error {
					if _, err := tx.SigningKey(ctx); !errors.Is(err, ab2store.ErrNotFound) {
						return err // an empty store answers ErrNotFound; nil would be a stub
					}
					return nil
				}},
				{"PutAssertionJTI", func() error { return tx.PutAssertionJTI(ctx, "probe", 1) }},
				{"ListIdentities", func() error { _, err := tx.ListIdentities(ctx); return err }},
				{"Receipt", func() error { _, _, err := tx.Receipt(ctx, "nobody", "k", fixedNow); return err }},
				{"ListChannels", func() error { _, err := tx.ListChannels(ctx); return err }},
				{"Subscriptions", func() error { _, err := tx.Subscriptions(ctx, "nobody"); return err }},
				{"ListSessions", func() error { _, err := tx.ListSessions(ctx, "nobody"); return err }},
				{"MessagesAfter", func() error { _, err := tx.MessagesAfter(ctx, 1, 0, ab2store.MessageFilter{}, 10); return err }},
				{"MemoryRevisions", func() error { _, err := tx.MemoryRevisions(ctx, 1); return err }},
				{"LiveMemories", func() error { _, err := tx.LiveMemories(ctx, 1); return err }},
			}
			for _, c := range checks {
				if err := c.call(); err != nil {
					t.Errorf("%s: %v", c.name, err)
				}
			}
			return nil
		})
	})
}
