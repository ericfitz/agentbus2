package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/ericfitz/agentbus2/internal/dbtest"
	"github.com/ericfitz/agentbus2/internal/search"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/store/sqlstore"
)

const resetWord = "zyxwvuquux"

// dataTables are the tables ResetData empties.
var dataTables = []string{
	"notices", "messages", "message_tags", "channels", "channel_acl", "subscriptions",
	"tag_subscriptions", "tag_subscription_tags", "sessions", "session_cursors",
	"memory_access", "receipts",
}

// resetFixtures fills every table ResetData or ResetRegistry touches and
// returns the channel id and MaxSeq.
func resetFixtures(t *testing.T, s ab2store.Store) (chID, maxSeq int64) {
	t.Helper()
	mustAdmin(t, s, "root")
	mustIdentity(t, s, "pat")
	mustChannel(t, s, "general", ab2store.KindOrdinary, "root", nil)
	var ch ab2store.Channel
	mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) (err error) {
		ch, err = tx.ChannelByName(ctx, "general")
		return err
	})
	chID = ch.ID
	mustAppend(t, s, ab2store.NewMessage{ChannelID: chID, Sender: "pat", Subject: "s", Content: "hello " + resetWord, Tags: []string{"t1"}})
	mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
		for _, name := range []string{"root", "pat"} {
			if err := tx.PutPublicKey(ctx, ab2store.PublicKey{Identity: name, KID: "k-" + name, Algorithm: "EdDSA", Key: []byte{1, 2, 3}, CreatedAt: fixedNow}); err != nil {
				return err
			}
		}
		if err := tx.PutEnrollmentToken(ctx, enrollTok("6666666666666666", "h6", 1)); err != nil {
			return err
		}
		if err := tx.PutSigningKey(ctx, signingKey("sk")); err != nil {
			return err
		}
		if err := tx.Subscribe(ctx, "pat", chID, 0, fixedNow); err != nil {
			return err
		}
		if err := tx.SubscribeTags(ctx, ab2store.TagSubscription{Identity: "pat", TagsKey: "k", Patterns: []ab2store.TagPattern{ab2store.NewTagPattern("t1")}, CreatedSeq: 0}); err != nil {
			return err
		}
		if err := tx.OpenSession(ctx, ab2store.Session{ID: "s1", Identity: "pat", DisplayName: "d", ResumeTokenHash: "rth", OpenedAt: fixedNow, LeaseExpiresAt: fixedNow + 10}); err != nil {
			return err
		}
		if err := tx.PutCursor(ctx, ab2store.Cursor{SessionID: "s1", ChannelID: chID, CursorSeq: 1}); err != nil {
			return err
		}
		if err := tx.PutReceipt(ctx, ab2store.Receipt{Identity: "pat", Key: "k", Fingerprint: "f", Result: []byte("1"), ExpiresAt: fixedNow + 10}); err != nil {
			return err
		}
		if err := tx.SetNotice(ctx, ab2store.Notice{Kind: "capacity", Message: "m", SetAt: fixedNow}); err != nil {
			return err
		}
		return tx.TouchMemoryAccess(ctx, []int64{7}, fixedNow)
	})
	mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) (err error) {
		maxSeq, err = tx.MaxSeq(ctx)
		return err
	})
	return chID, maxSeq
}

func requireCounts(t *testing.T, db *sql.DB, want int, tables ...string) {
	t.Helper()
	for _, tbl := range tables {
		if got := scalar[int](t, db, "SELECT count(*) FROM "+tbl); got != want {
			t.Errorf("%s rows = %d, want %d", tbl, got, want)
		}
	}
}

func TestResetDataWipesDataKeepsRegistry(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, rawDSN := openStore(t, e, sqlstore.Options{})
		chID, maxSeq := resetFixtures(t, s)
		db := rawDB(t, e, rawDSN)
		requireCounts(t, db, 1, "messages", "channels", "sessions", "receipts", "notices", "memory_access")
		wipe := func() {
			mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error { return tx.ResetData(ctx) })
		}
		wipe()

		requireCounts(t, db, 0, dataTables...)
		if e.Name == "sqlite" {
			if got := scalar[int](t, db, "SELECT count(*) FROM messages_fts WHERE messages_fts MATCH '"+resetWord+"'"); got != 0 {
				t.Errorf("messages_fts ghost hits = %d, want 0", got)
			}
		}
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			got, err := tx.SearchText(ctx, search.Parse(resetWord), []int64{chID}, ab2store.SearchFilter{}, 10)
			if err != nil || len(got) != 0 {
				t.Errorf("SearchText after reset = %v, %v; want none", got, err)
			}
			ids, err := tx.ListIdentities(ctx)
			if err != nil || len(ids) != 2 || !ids[0].Admin && !ids[1].Admin {
				t.Errorf("identities = %+v, %v; want two with an admin", ids, err)
			}
			for _, n := range []string{"root", "pat"} {
				if ks, err := tx.PublicKeys(ctx, n); err != nil || len(ks) != 1 {
					t.Errorf("PublicKeys(%s) = %v, %v; want 1", n, ks, err)
				}
			}
			if l, err := tx.ListEnrollmentTokens(ctx); err != nil || len(l) != 1 {
				t.Errorf("tokens = %v, %v; want 1", l, err)
			}
			if _, err := tx.SigningKey(ctx); err != nil {
				t.Errorf("SigningKey: %v", err)
			}
			if got, err := tx.MaxSeq(ctx); err != nil || got != maxSeq {
				t.Errorf("MaxSeq = %d, %v; want %d", got, err, maxSeq)
			}
			return nil
		})
		requireCounts(t, db, 1, "identity_keys WHERE identity = 'root'", "identity_keys WHERE identity = 'pat'", "leases WHERE name = 'maintenance'", "schema_meta")

		wipe() // a second run is a no-op
		requireCounts(t, db, 0, dataTables...)
		requireCounts(t, db, 2, "identities")
	})
}

func TestResetRegistryKeepsOne(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, rawDSN := openStore(t, e, sqlstore.Options{})
		_, maxSeq := resetFixtures(t, s)
		db := rawDB(t, e, rawDSN)
		reset := func(keep string) error {
			return s.Tx(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
				if err := tx.ResetData(ctx); err != nil {
					return err
				}
				return tx.ResetRegistry(ctx, keep)
			})
		}
		if err := reset("root"); err != nil {
			t.Fatal(err)
		}
		check := func() {
			mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
				ids, err := tx.ListIdentities(ctx)
				if err != nil || len(ids) != 1 || ids[0].Name != "root" || !ids[0].Admin {
					t.Errorf("identities = %+v, %v; want [root admin]", ids, err)
				}
				if ks, err := tx.PublicKeys(ctx, "root"); err != nil || len(ks) != 1 {
					t.Errorf("PublicKeys(root) = %v, %v; want 1", ks, err)
				}
				if l, err := tx.ListEnrollmentTokens(ctx); err != nil || len(l) != 0 {
					t.Errorf("tokens = %v, %v; want none", l, err)
				}
				if _, err := tx.SigningKey(ctx); err != nil {
					t.Errorf("SigningKey: %v", err)
				}
				if got, err := tx.MaxSeq(ctx); err != nil || got != maxSeq {
					t.Errorf("MaxSeq = %d, %v; want %d", got, err, maxSeq)
				}
				return nil
			})
			requireCounts(t, db, 1, "identity_keys")
		}
		check()
		if err := reset("root"); err != nil { // second run
			t.Fatal(err)
		}
		check()

		if err := reset(""); err != nil {
			t.Fatal(err)
		}
		requireCounts(t, db, 0, "identities", "identity_keys", "enrollment_tokens")
		requireCounts(t, db, 1, "signing_keys")

		err := s.Tx(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
			return tx.ResetRegistry(ctx, "a\x00b")
		})
		if !errors.Is(err, ab2store.ErrInvalid) {
			t.Errorf("ResetRegistry(NUL) = %v, want ErrInvalid", err)
		}
	})
}

func TestResetDataReadOnly(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		for name, f := range map[string]func(context.Context, ab2store.Tx) error{
			"ResetData":     func(ctx context.Context, tx ab2store.Tx) error { return tx.ResetData(ctx) },
			"ResetRegistry": func(ctx context.Context, tx ab2store.Tx) error { return tx.ResetRegistry(ctx, "") },
		} {
			if err := s.Read(context.Background(), f); !errors.Is(err, ab2store.ErrReadOnly) {
				t.Errorf("Read + %s = %v, want ErrReadOnly", name, err)
			}
		}
	})
}
