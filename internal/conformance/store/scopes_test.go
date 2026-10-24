package store

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/store/sqlstore"
)

// Scopes (Slack bridge spec 4): the registry, scope columns, retired names,
// the reserved slack-bridge identity and the activity touch.

func mustScope(t *testing.T, s ab2store.Store, name string, now int64) {
	t.Helper()
	mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error { return tx.CreateScope(ctx, name, now) })
}

func getScope(t *testing.T, s ab2store.Store, name string) ab2store.Scope {
	t.Helper()
	var sc ab2store.Scope
	mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) (err error) {
		sc, err = tx.Scope(ctx, name)
		return err
	})
	return sc
}

func txErr(s ab2store.Store, fn txFunc) error { return s.Tx(context.Background(), fn) }

func TestScopesCreateGetList(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		mustScope(t, s, "job2", 2000)
		mustScope(t, s, "job1", 1000)
		want := ab2store.Scope{Name: "job1", State: ab2store.ScopeOpen, Retire: "", CreatedAt: 1000, LastActivity: 1000}
		if got := getScope(t, s, "job1"); got != want {
			t.Fatalf("Scope(job1) = %+v, want %+v", got, want)
		}
		err := txErr(s, func(ctx context.Context, tx ab2store.Tx) error { return tx.CreateScope(ctx, "job1", 5) })
		if !errors.Is(err, ab2store.ErrConflict) {
			t.Errorf("duplicate CreateScope = %v, want ErrConflict", err)
		}
		var list []ab2store.Scope
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) (err error) {
			list, err = tx.ListScopes(ctx)
			return err
		})
		if len(list) != 2 || list[0].Name != "job1" || list[1].Name != "job2" {
			t.Errorf("ListScopes = %+v, want job1, job2", list)
		}
	})
}

func TestScopesReject(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		for _, bad := range []string{"", "a/b", "a\x00b"} {
			err := txErr(s, func(ctx context.Context, tx ab2store.Tx) error { return tx.CreateScope(ctx, bad, 1) })
			if !errors.Is(err, ab2store.ErrInvalid) {
				t.Errorf("CreateScope(%q) = %v, want ErrInvalid", bad, err)
			}
		}
		mustScope(t, s, "job1", 1000)
		for _, mode := range []ab2store.RetireMode{"later", ""} {
			err := txErr(s, func(ctx context.Context, tx ab2store.Tx) error { return tx.MarkScopeDone(ctx, "job1", mode, 1) })
			if !errors.Is(err, ab2store.ErrInvalid) {
				t.Errorf("MarkScopeDone(mode %q) = %v, want ErrInvalid", mode, err)
			}
		}
		if got := getScope(t, s, "job1"); got.State != ab2store.ScopeOpen {
			t.Errorf("a refused mode changed the state to %q", got.State)
		}
		err := txErr(s, func(ctx context.Context, tx ab2store.Tx) error {
			return tx.MarkScopeDone(ctx, "nope", ab2store.RetireNormal, 1)
		})
		if !errors.Is(err, ab2store.ErrNotFound) {
			t.Errorf("MarkScopeDone(unknown) = %v, want ErrNotFound", err)
		}
		err = txErr(s, func(ctx context.Context, tx ab2store.Tx) error { return tx.MarkScopeRetired(ctx, "nope", 1) })
		if !errors.Is(err, ab2store.ErrNotFound) {
			t.Errorf("MarkScopeRetired(unknown) = %v, want ErrNotFound", err)
		}
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			return tx.MarkScopeDone(ctx, "job1", ab2store.RetireWithBridge, 3000)
		})
		want := ab2store.Scope{Name: "job1", State: ab2store.ScopeDone, Retire: ab2store.RetireWithBridge, CreatedAt: 1000, DoneAt: 3000, LastActivity: 1000}
		if got := getScope(t, s, "job1"); got != want {
			t.Errorf("done scope = %+v, want %+v", got, want)
		}
		err = txErr(s, func(ctx context.Context, tx ab2store.Tx) error {
			return tx.MarkScopeDone(ctx, "job1", ab2store.RetireNormal, 4000)
		})
		if !errors.Is(err, ab2store.ErrConflict) {
			t.Errorf("second MarkScopeDone = %v, want ErrConflict", err)
		}
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error { return tx.MarkScopeRetired(ctx, "job1", 5000) })
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error { return tx.MarkScopeRetired(ctx, "job1", 6000) }) // second run: nil
		if got := getScope(t, s, "job1"); got.State != ab2store.ScopeRetired || got.RetiredAt != 5000 {
			t.Errorf("retired scope = %+v, want retired at 5000 (the first retire stands)", got)
		}
		err = txErr(s, func(ctx context.Context, tx ab2store.Tx) error {
			return tx.MarkScopeDone(ctx, "job1", ab2store.RetireNormal, 7000)
		})
		if !errors.Is(err, ab2store.ErrConflict) {
			t.Errorf("MarkScopeDone on retired = %v, want ErrConflict", err)
		}
	})
}

func TestDeleteRetiredScopes(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		for _, n := range []string{"old", "new", "held", "open"} {
			mustScope(t, s, n, 1)
		}
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			if _, err := tx.CreateChannel(ctx, ab2store.ChannelCreate{Name: "held-ch", Kind: ab2store.KindOrdinary, Scope: "held", CreatedAt: 1}); err != nil {
				return err
			}
			for n, at := range map[string]int64{"old": 1000, "new": 5000, "held": 1000} {
				if err := tx.MarkScopeRetired(ctx, n, at); err != nil {
					return err
				}
			}
			return nil
		})
		var n int
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) (err error) {
			n, err = tx.DeleteRetiredScopes(ctx, 2000)
			return err
		})
		if n != 1 {
			t.Fatalf("DeleteRetiredScopes = %d, want 1 (old; held is referenced, new is too recent, open is not retired)", n)
		}
		var names []string
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			l, err := tx.ListScopes(ctx)
			for _, sc := range l {
				names = append(names, sc.Name)
			}
			return err
		})
		if want := []string{"held", "new", "open"}; !slices.Equal(names, want) {
			t.Errorf("scopes left = %v, want %v", names, want)
		}
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) (err error) { // second run: nothing more
			n, err = tx.DeleteRetiredScopes(ctx, 2000)
			return err
		})
		if n != 0 {
			t.Errorf("second DeleteRetiredScopes = %d, want 0", n)
		}
	})
}

func TestScopedChannelAndIdentity(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		mustScope(t, s, "job1", 1)
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			ch, err := tx.CreateChannel(ctx, ab2store.ChannelCreate{Name: "job1-work", Kind: ab2store.KindOrdinary, Scope: "job1", CreatedAt: 1})
			if err != nil || ch.Scope != "job1" {
				t.Errorf("CreateChannel(scope job1) = %+v, %v", ch, err)
			}
			return nil
		})
		err := txErr(s, func(ctx context.Context, tx ab2store.Tx) error {
			_, err := tx.CreateChannel(ctx, ab2store.ChannelCreate{Name: "x", Kind: ab2store.KindOrdinary, Scope: "nope", CreatedAt: 1})
			return err
		})
		if !errors.Is(err, ab2store.ErrNotFound) {
			t.Errorf("CreateChannel(unknown scope) = %v, want ErrNotFound", err)
		}
		err = txErr(s, func(ctx context.Context, tx ab2store.Tx) error { return tx.CreateScopedIdentity(ctx, "x", "nope", 1) })
		if !errors.Is(err, ab2store.ErrNotFound) {
			t.Errorf("CreateScopedIdentity(unknown scope) = %v, want ErrNotFound", err)
		}
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			return tx.CreateScopedIdentity(ctx, "job1-coder", "job1", 2)
		})
		mustIdentity(t, s, "free")
		mustChannel(t, s, "free-ch", ab2store.KindOrdinary, "", nil)
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			id, err := tx.Identity(ctx, "job1-coder")
			if err != nil || id.Scope != "job1" || id.Kind != ab2store.IdentityAgent || id.Admin {
				t.Errorf("Identity(job1-coder) = %+v, %v", id, err)
			}
			free, err := tx.Identity(ctx, "free")
			if err != nil || free.Scope != "" {
				t.Errorf("unscoped identity = %+v, %v", free, err)
			}
			fc, err := tx.ChannelByName(ctx, "free-ch")
			if err != nil || fc.Scope != "" {
				t.Errorf("unscoped channel = %+v, %v", fc, err)
			}
			chs, ids, err := tx.ScopeMembers(ctx, "job1")
			if err != nil || len(chs) != 1 || chs[0].Name != "job1-work" || len(ids) != 1 || ids[0].Name != "job1-coder" {
				t.Errorf("ScopeMembers = %+v, %+v, %v", chs, ids, err)
			}
			return nil
		})
	})
}

func TestDeleteEnrollmentTokensFor(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		mustIdentity(t, s, "pat")
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			for _, tok := range []ab2store.EnrollmentToken{
				{ID: "t1", TokenHash: "h1", Identity: "pat", ExpiresAt: 10, UsesLeft: 1, CreatedAt: 1},
				{ID: "t2", TokenHash: "h2", Identity: "pat", ExpiresAt: 10, UsesLeft: 1, CreatedAt: 2},
				{ID: "t3", TokenHash: "h3", ExpiresAt: 10, UsesLeft: 1, CreatedAt: 3},
			} {
				if err := tx.PutEnrollmentToken(ctx, tok); err != nil {
					return err
				}
			}
			return nil
		})
		for i, want := range []int{2, 0} { // the second run deletes nothing
			var n int
			mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) (err error) {
				n, err = tx.DeleteEnrollmentTokensFor(ctx, "pat")
				return err
			})
			if n != want {
				t.Errorf("run %d: DeleteEnrollmentTokensFor = %d, want %d", i+1, n, want)
			}
		}
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			l, err := tx.ListEnrollmentTokens(ctx)
			if err != nil || len(l) != 1 || l[0].ID != "t3" {
				t.Errorf("tokens left = %+v, %v; want only the unbound t3", l, err)
			}
			return nil
		})
	})
}

func TestRetireIdentity(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, rawDSN := openStore(t, e, sqlstore.Options{})
		mustScope(t, s, "job1", 1)
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			return tx.CreateScopedIdentity(ctx, "job1-coder", "job1", 2)
		})
		ops := mustChannel(t, s, "ops", ab2store.KindOrdinary, "", nil)
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			if err := tx.PutPublicKey(ctx, ab2store.PublicKey{Identity: "job1-coder", KID: "k", Algorithm: "EdDSA", Key: []byte{1}, CreatedAt: 1}); err != nil {
				return err
			}
			if err := tx.Subscribe(ctx, "job1-coder", ops.ID, 0, 1); err != nil {
				return err
			}
			if err := tx.SetACL(ctx, ops.ID, append(ab2store.DefaultACL(""), ab2store.ACLEntry{Identity: "job1-coder", Permissions: []ab2store.Permission{ab2store.PermRead}})); err != nil {
				return err
			}
			return tx.PutEnrollmentToken(ctx, ab2store.EnrollmentToken{ID: "t1", TokenHash: "h1", Identity: "job1-coder", ExpiresAt: 10, UsesLeft: 1, CreatedAt: 1})
		})
		liveSession(t, s, "s1", "job1-coder", 9000, 0)
		mustAppend(t, s, ab2store.NewMessage{ChannelID: ops.ID, Sender: "job1-coder", Content: "hello"})

		for i, want := range []bool{true, false} { // the second run finds nothing
			var did bool
			mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) (err error) {
				did, err = tx.RetireIdentity(ctx, "job1-coder", 50)
				return err
			})
			if did != want {
				t.Fatalf("run %d: RetireIdentity = %v, want %v", i+1, did, want)
			}
		}
		db := rawDB(t, e, rawDSN)
		for _, q := range []string{
			"SELECT count(*) FROM identities WHERE name = 'job1-coder'",
			"SELECT count(*) FROM identity_keys WHERE identity = 'job1-coder'",
			"SELECT count(*) FROM sessions WHERE identity = 'job1-coder'",
			"SELECT count(*) FROM subscriptions WHERE identity = 'job1-coder'",
			"SELECT count(*) FROM channel_acl WHERE identity = 'job1-coder'",
			"SELECT count(*) FROM enrollment_tokens WHERE identity = 'job1-coder'",
		} {
			if n := scalar[int](t, db, q); n != 0 {
				t.Errorf("%s = %d, want 0", q, n)
			}
		}
		if n := scalar[int](t, db, "SELECT count(*) FROM messages WHERE sender = 'job1-coder'"); n != 1 {
			t.Errorf("messages by the retired identity = %d, want 1 (messages stay)", n)
		}
		if got := scalar[string](t, db, "SELECT scope FROM retired_identities WHERE name = 'job1-coder'"); got != "job1" {
			t.Errorf("retired_identities scope = %q, want job1", got)
		}
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			if r, err := tx.IdentityRetired(ctx, "job1-coder"); err != nil || !r {
				t.Errorf("IdentityRetired = %v, %v; want true", r, err)
			}
			if r, err := tx.IdentityRetired(ctx, "nobody"); err != nil || r {
				t.Errorf("IdentityRetired(nobody) = %v, %v; want false", r, err)
			}
			return nil
		})
		for name, create := range map[string]func(ctx context.Context, tx ab2store.Tx) error{
			"CreateIdentity": func(ctx context.Context, tx ab2store.Tx) error { return tx.CreateIdentity(ctx, "job1-coder", 60) },
			"CreateScopedIdentity": func(ctx context.Context, tx ab2store.Tx) error {
				return tx.CreateScopedIdentity(ctx, "job1-coder", "job1", 60)
			},
			"CreateHuman": func(ctx context.Context, tx ab2store.Tx) error {
				return tx.CreateHuman(ctx, ab2store.HumanBinding{Identity: "job1-coder", ClaimType: ab2store.ClaimEmail, Value: "a@example.com"}, 60)
			},
		} {
			err := txErr(s, create)
			if !errors.Is(err, ab2store.ErrConflict) || err == nil || !strings.Contains(err.Error(), "retired") {
				t.Errorf("%s on a retired name = %v, want ErrConflict naming \"retired\"", name, err)
			}
		}
	})
}

func TestSlackBridgeReserved(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		mustAdmin(t, s, "adm")
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			id, err := tx.Identity(ctx, ab2store.SlackBridge)
			if err != nil || id.Kind != ab2store.IdentityAgent || id.Admin || id.Scope != "" || id.CreatedAt <= 0 {
				t.Errorf("Identity(slack-bridge) = %+v, %v", id, err)
			}
			if keys, err := tx.PublicKeys(ctx, ab2store.SlackBridge); err != nil || len(keys) != 0 {
				t.Errorf("slack-bridge keys = %+v, %v", keys, err)
			}
			if subs, err := tx.Subscriptions(ctx, ab2store.SlackBridge); err != nil || len(subs) != 0 {
				t.Errorf("slack-bridge subscriptions = %+v, %v", subs, err)
			}
			return nil
		})
		if !ab2store.ReservedIdentity("slack-bridge") || !ab2store.ReservedIdentity("everyone") || !ab2store.ReservedIdentity("admins") || ab2store.ReservedIdentity("pat") {
			t.Error("ReservedIdentity wrong")
		}
		for name, fn := range map[string]txFunc{
			"CreateIdentity": func(ctx context.Context, tx ab2store.Tx) error {
				return tx.CreateIdentity(ctx, ab2store.SlackBridge, 1)
			},
			"CreateHuman": func(ctx context.Context, tx ab2store.Tx) error {
				return tx.CreateHuman(ctx, ab2store.HumanBinding{Identity: ab2store.SlackBridge, ClaimType: ab2store.ClaimEmail, Value: "a@example.com"}, 1)
			},
			"PutPublicKey": func(ctx context.Context, tx ab2store.Tx) error {
				return tx.PutPublicKey(ctx, ab2store.PublicKey{Identity: ab2store.SlackBridge, KID: "k", Algorithm: "EdDSA", Key: []byte{1}})
			},
			"PutEnrollmentToken": func(ctx context.Context, tx ab2store.Tx) error {
				return tx.PutEnrollmentToken(ctx, ab2store.EnrollmentToken{ID: "t", TokenHash: "h", Identity: ab2store.SlackBridge, ExpiresAt: 5, UsesLeft: 1})
			},
		} {
			if err := txErr(s, fn); !errors.Is(err, ab2store.ErrInvalid) {
				t.Errorf("%s(slack-bridge) = %v, want ErrInvalid", name, err)
			}
		}
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			if err := tx.ResetData(ctx); err != nil {
				return err
			}
			return tx.ResetRegistry(ctx, "adm")
		})
		mustChannel(t, s, "bridged", ab2store.KindOrdinary, "adm", nil)
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			if _, err := tx.Identity(ctx, ab2store.SlackBridge); err != nil {
				t.Errorf("slack-bridge after ResetRegistry: %v", err)
			}
			ch, err := tx.ChannelByName(ctx, "bridged")
			if err != nil {
				return err
			}
			return tx.SetACL(ctx, ch.ID, append(ab2store.DefaultACL("adm"), ab2store.ACLEntry{Identity: ab2store.SlackBridge, Permissions: []ab2store.Permission{ab2store.PermRead}}))
		})
	})
}

func TestAppendTouchesScopeActivity(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		mustIdentity(t, s, "pat")
		mustScope(t, s, "job1", 1000)
		mustScope(t, s, "other", 1000)
		var scoped ab2store.Channel
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) (err error) {
			scoped, err = tx.CreateChannel(ctx, ab2store.ChannelCreate{Name: "job1-work", Kind: ab2store.KindOrdinary, Scope: "job1", CreatedAt: 1})
			return err
		})
		plain := mustChannel(t, s, "plain", ab2store.KindOrdinary, "", nil)
		act := func(name string) int64 { return getScope(t, s, name).LastActivity }
		mustAppend(t, s, ab2store.NewMessage{ChannelID: scoped.ID, Sender: "pat", Content: "x", CreatedAt: 5000})
		if got := act("job1"); got != 5000 {
			t.Errorf("LastActivity = %d, want 5000", got)
		}
		mustAppend(t, s, ab2store.NewMessage{ChannelID: scoped.ID, Sender: "pat", Content: "y", CreatedAt: 4000})
		if got := act("job1"); got != 5000 {
			t.Errorf("LastActivity after an older message = %d, want 5000", got)
		}
		mustAppend(t, s, ab2store.NewMessage{ChannelID: plain.ID, Sender: "pat", Content: "z", CreatedAt: 9000})
		if a, b := act("job1"), act("other"); a != 5000 || b != 1000 {
			t.Errorf("an unscoped append changed scopes: job1 %d, other %d", a, b)
		}
	})
}

// TestOpenMigratesVersion5Scopes opens a version 5 database holding a
// slack-bridge row in every shape version 6 must clean, then reopens with the
// full set.
func TestOpenMigratesVersion5Scopes(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		full := migrationsFor(e)
		if len(full) < 6 {
			t.Fatalf("%d migrations, want at least 6", len(full))
		}
		s, reopen, rawDSN := openStore(t, e, sqlstore.Options{Migrations: full[:5]})
		if got := schemaVersion(t, s); got != 5 {
			t.Fatalf("SchemaVersion = %d, want 5", got)
		}
		db := rawDB(t, e, rawDSN)
		tr := "1"
		if e.Name == "postgres" {
			tr = "TRUE"
		}
		execAll(t, db,
			"INSERT INTO identities (name, is_admin, tags_acked_seq, created_at) VALUES ('slack-bridge', "+tr+", 0, 0)",
			"INSERT INTO identity_keys (identity, kid, algorithm, public_key, created_at) VALUES ('slack-bridge', 'k', 'EdDSA', 'x', 1)",
			"INSERT INTO channels (name, kind, creator, created_seq, created_at) VALUES ('general', 'ordinary', '', 0, 9)",
			"INSERT INTO sessions (id, identity, display_name, resume_token_hash, opened_at, lease_expires_at) VALUES ('s1', 'slack-bridge', 'd', 'rth', 1, 2)",
			"INSERT INTO session_cursors (session_id, channel_id, cursor_seq, pending_end_seq) VALUES ('s1', 1, 0, 0)",
			"INSERT INTO subscriptions (identity, channel_id, acked_seq, created_at) VALUES ('slack-bridge', 1, 0, 1)",
			"INSERT INTO tag_subscriptions (identity, tags_key, created_seq) VALUES ('slack-bridge', 'tk', 0)",
			"INSERT INTO enrollment_tokens (id, token_hash, identity, expires_at, uses_left, created_at) VALUES ('t1', 'h1', 'slack-bridge', 10, 1, 1)",
			"INSERT INTO human_bindings (identity, issuer, claim_type, value, provider_subject, created_at) VALUES ('slack-bridge', '', 'email', 'a@example.com', '', 1)",
			"INSERT INTO access_refs (ref_list, ref_kind, issuer, value, created_at) VALUES ('admins', 'identity', '', 'slack-bridge', 1)",
			"INSERT INTO access_refs (ref_list, ref_kind, issuer, value, created_at) VALUES ('users', 'identity', '', 'slack-bridge', 1)",
		)
		execAll(t, db, "UPDATE identities SET kind = 'human' WHERE name = 'slack-bridge'")
		_ = s.Close()
		s2, err := reopen(sqlstore.Options{})
		if err != nil {
			t.Fatalf("reopen: %v", err)
		}
		if got := schemaVersion(t, s2); got != len(full) {
			t.Fatalf("SchemaVersion = %d, want %d", got, len(full))
		}
		mustRead(t, s2, func(ctx context.Context, tx ab2store.Tx) error {
			id, err := tx.Identity(ctx, ab2store.SlackBridge)
			if err != nil || id.Admin || id.Kind != ab2store.IdentityAgent || id.Scope != "" || id.CreatedAt <= 0 {
				t.Errorf("slack-bridge = %+v, %v; want a non-admin agent with created_at > 0", id, err)
			}
			ch, err := tx.ChannelByName(ctx, "general")
			if err != nil || ch.Scope != "" {
				t.Errorf("general = %+v, %v; want Scope \"\"", ch, err)
			}
			return nil
		})
		for _, table := range []string{"identity_keys", "sessions", "session_cursors", "subscriptions", "tag_subscriptions", "enrollment_tokens", "human_bindings"} {
			col := "identity"
			if table == "session_cursors" {
				col = "session_id"
			}
			q := "SELECT count(*) FROM " + table
			if table != "session_cursors" {
				q += " WHERE " + col + " = 'slack-bridge'"
			}
			if n := scalar[int](t, db, q); n != 0 {
				t.Errorf("%s: %d rows left, want 0", table, n)
			}
		}
		if n := scalar[int](t, db, "SELECT count(*) FROM access_refs WHERE ref_kind = 'identity' AND value = 'slack-bridge'"); n != 0 {
			t.Errorf("access_refs rows for slack-bridge = %d, want 0", n)
		}
		for _, table := range []string{"scopes", "retired_identities", "bridges", "bridge_messages", "slack_users", "bridge_dropped"} {
			if tableCount(t, e, db, table) != 1 {
				t.Errorf("table %s missing", table)
			}
		}
		// A second open runs nothing more.
		_ = s2.Close()
		s3, err := reopen(sqlstore.Options{})
		if err != nil {
			t.Fatalf("second reopen: %v", err)
		}
		if got := schemaVersion(t, s3); got != len(full) {
			t.Fatalf("SchemaVersion after second open = %d", got)
		}
		_ = s3.Close()
	})
}

// TestResetDataKeepsBridgeTables: ResetData keeps scopes, retired names and
// the bridge tables (a bridge whose channel went is cleaned by the runner).
func TestResetDataKeepsBridgeTables(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, rawDSN := openStore(t, e, sqlstore.Options{})
		mustScope(t, s, "job1", 1)
		db := rawDB(t, e, rawDSN)
		execAll(t, db,
			"INSERT INTO retired_identities (name, scope, retired_at) VALUES ('gone', 'job1', 1)",
			"INSERT INTO bridges (channel_id, channel_name, scope, slack_name, state, created_at) VALUES (7, 'c', 'job1', 's', 'live', 1)",
		)
		id := scalar[int](t, db, "SELECT id FROM bridges")
		execAll(t, db,
			"INSERT INTO bridge_messages (bridge_id, seq, slack_ts, root_ts, created_at) VALUES ("+strconv.Itoa(id)+", 1, '1.1', '1.1', 1)",
			"INSERT INTO bridge_dropped (bridge_id, slack_ts, created_at) VALUES ("+strconv.Itoa(id)+", '2.2', 1)",
		)
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error { return tx.ResetData(ctx) })
		for _, table := range []string{"scopes", "retired_identities", "bridges", "bridge_messages", "bridge_dropped"} {
			if n := scalar[int](t, db, "SELECT count(*) FROM "+table); n != 1 {
				t.Errorf("%s after ResetData: %d rows, want 1", table, n)
			}
		}
	})
}

// withoutBridge drops the reserved slack-bridge row, which every store holds
// from schema version 6.
func withoutBridge(ids []ab2store.Identity) []ab2store.Identity {
	return slices.DeleteFunc(slices.Clone(ids), func(i ab2store.Identity) bool { return i.Name == ab2store.SlackBridge })
}
