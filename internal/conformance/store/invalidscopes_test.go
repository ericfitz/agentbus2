package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/store/sqlstore"
)

// syncBuffer is a bytes.Buffer safe for the logger's concurrent writers.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func capturedLogger() (*slog.Logger, *syncBuffer) {
	buf := &syncBuffer{}
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})), buf
}

// invalidScopeFixture holds a scoped agent (valid), an administrator and a
// human that each hold a scope the Store itself refuses to write: the rows
// are forced through raw SQL, as an earlier build or a manual edit would.
func invalidScopeFixture(t *testing.T, e dbtest.Engine, opts sqlstore.Options) (ab2store.Store, func(sqlstore.Options) (ab2store.Store, error), func() string) {
	t.Helper()
	s, reopen, rawDSN := openStore(t, e, opts)
	mustScope(t, s, "job1", fixedNow)
	mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
		if err := tx.CreateScopedIdentity(ctx, "coder", "job1", fixedNow); err != nil {
			return err
		}
		if err := tx.CreateIdentity(ctx, "root", fixedNow); err != nil {
			return err
		}
		return tx.CreateHuman(ctx, ab2store.HumanBinding{Identity: "eric@corp.example", ClaimType: ab2store.ClaimEmail, Value: "eric@corp.example"}, fixedNow)
	})
	db := rawDB(t, e, rawDSN)
	for _, q := range []string{
		"UPDATE identities SET is_admin = " + map[string]string{"sqlite": "1", "postgres": "TRUE"}[e.Name] + ", scope = 'job1' WHERE name = 'root'",
		"UPDATE identities SET scope = 'job1' WHERE name = 'eric@corp.example'",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	return s, reopen, rawDSN
}

func storedScope(t *testing.T, e dbtest.Engine, rawDSN func() string, name string) string {
	t.Helper()
	return scalar[string](t, rawDB(t, e, rawDSN), fmt.Sprintf("SELECT COALESCE(scope, '') FROM identities WHERE name = '%s'", name))
}

// TestIdentityReadNormalizesInvalidScope: every read path reports an
// administrator and a human unscoped and logs an error naming each; a scoped
// agent is unchanged (spec 4.3, 4.5).
func TestIdentityReadNormalizesInvalidScope(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		lg, buf := capturedLogger()
		s, _, rawDSN := invalidScopeFixture(t, e, sqlstore.Options{Logger: lg})
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			for _, name := range []string{"root", "eric@corp.example"} {
				got, err := tx.Identity(ctx, name)
				if err != nil || got.Scope != "" {
					t.Errorf("Identity(%s) = %+v, %v; want no scope", name, got, err)
				}
			}
			got, err := tx.Identity(ctx, "coder")
			if err != nil || got.Scope != "job1" {
				t.Errorf("Identity(coder) = %+v, %v; want scope job1", got, err)
			}
			all, err := tx.ListIdentities(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range all {
				want := ""
				if id.Name == "coder" {
					want = "job1"
				}
				if id.Scope != want {
					t.Errorf("ListIdentities: %s scope %q, want %q", id.Name, id.Scope, want)
				}
			}
			_, members, err := tx.ScopeMembers(ctx, "job1")
			if err != nil {
				t.Fatal(err)
			}
			if n := identityNames(members); !slices.Equal(n, []string{"coder", "eric@corp.example", "root"}) {
				t.Errorf("ScopeMembers names = %v", n)
			}
			for _, m := range members {
				if m.Name != "coder" && m.Scope != "" {
					t.Errorf("ScopeMembers: %s scope %q, want none", m.Name, m.Scope)
				}
			}
			return nil
		})
		log := buf.String()
		for _, name := range []string{"root", "eric@corp.example"} {
			if !strings.Contains(log, "level=ERROR") || !strings.Contains(log, "identity="+name) {
				t.Errorf("log lacks an error naming %s:\n%s", name, log)
			}
		}
		if strings.Contains(log, "identity=coder") {
			t.Errorf("log names the valid scoped agent:\n%s", log)
		}
		// A read does not write: the stored rows still hold the scope.
		if storedScope(t, e, rawDSN, "root") != "job1" {
			t.Error("a read repaired the stored row")
		}
	})
}

func clearInvalid(s ab2store.Store) (names []string, err error) {
	err = s.Tx(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
		var err error
		names, err = tx.ClearInvalidScopes(ctx)
		return err
	})
	return names, err
}

// TestClearInvalidScopes: the repair clears an administrator's and a human's
// scope and returns the names, leaves a scoped agent alone, and a second run
// changes nothing.
func TestClearInvalidScopes(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, rawDSN := invalidScopeFixture(t, e, sqlstore.Options{})
		names, err := clearInvalid(s)
		if err != nil || !slices.Equal(names, []string{"eric@corp.example", "root"}) {
			t.Fatalf("first run = %v, %v; want [eric@corp.example root]", names, err)
		}
		for name, want := range map[string]string{"root": "", "eric@corp.example": "", "coder": "job1"} {
			if got := storedScope(t, e, rawDSN, name); got != want {
				t.Errorf("stored scope of %s = %q, want %q", name, got, want)
			}
		}
		names, err = clearInvalid(s)
		if err != nil || len(names) != 0 {
			t.Fatalf("second run = %v, %v; want nothing", names, err)
		}
	})
}

// TestClearInvalidScopesRefusedInRead: a read transaction cannot repair.
func TestClearInvalidScopesRefusedInRead(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := invalidScopeFixture(t, e, sqlstore.Options{})
		err := s.Read(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
			_, err := tx.ClearInvalidScopes(ctx)
			return err
		})
		if !errors.Is(err, ab2store.ErrReadOnly) {
			t.Fatalf("ClearInvalidScopes in Read = %v, want ErrReadOnly", err)
		}
	})
}

// TestClearInvalidScopesConcurrent (Definition of Done D): overlapping runs
// across two handles leave one consistent result: every bad row is reported
// exactly once in all, the valid row is untouched, no run errors.
func TestClearInvalidScopesConcurrent(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, reopen, rawDSN := invalidScopeFixture(t, e, sqlstore.Options{})
		s2, err := reopen(sqlstore.Options{})
		if err != nil {
			t.Fatal(err)
		}
		var (
			mu  sync.Mutex
			got []string
			wg  sync.WaitGroup
		)
		for i := range 6 {
			st := s
			if i%2 == 1 {
				st = s2
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				names, err := clearInvalid(st)
				if err != nil && !errors.Is(err, ab2store.ErrRetryable) {
					t.Errorf("run %d: %v", i, err)
				}
				mu.Lock()
				got = append(got, names...)
				mu.Unlock()
			}()
		}
		wg.Wait()
		// A run that gave up retrying changed nothing; a final run takes what is left.
		rest, err := clearInvalid(s)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, rest...)
		slices.Sort(got)
		if !slices.Equal(got, []string{"eric@corp.example", "root"}) {
			t.Fatalf("names across all runs = %v, want each of [eric@corp.example root] once", got)
		}
		if storedScope(t, e, rawDSN, "coder") != "job1" || storedScope(t, e, rawDSN, "root") != "" || storedScope(t, e, rawDSN, "eric@corp.example") != "" {
			t.Error("final state inconsistent")
		}
	})
}

// TestStoreRefusesScopeOnAdminOrHuman: the writes that would create the bad
// combination fail (validation-class ErrInvalid) or repair it (spec 4.3, 4.5).
func TestStoreRefusesScopeOnAdminOrHuman(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, rawDSN := openStore(t, e, sqlstore.Options{})
		mustScope(t, s, "job1", fixedNow)
		mustAdmin(t, s, "root")
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			if err := tx.CreateHuman(ctx, ab2store.HumanBinding{Identity: "eric@corp.example", ClaimType: ab2store.ClaimEmail, Value: "eric@corp.example"}, fixedNow); err != nil {
				return err
			}
			// A name on the admins list with no row yet.
			_, err := tx.AddReference(ctx, ab2store.Reference{List: ab2store.ListAdmins, Kind: ab2store.RefIdentity, Value: "future-admin", CreatedAt: fixedNow})
			return err
		})
		for name, want := range map[string]string{
			"root":              "an administrator cannot be scoped",
			"eric@corp.example": "a human identity cannot be scoped",
			"future-admin":      "an administrator cannot be scoped",
		} {
			err := s.Tx(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
				return tx.CreateScopedIdentity(ctx, name, "job1", fixedNow)
			})
			if !errors.Is(err, ab2store.ErrInvalid) || !strings.Contains(err.Error(), want) {
				t.Errorf("CreateScopedIdentity(%s) = %v, want ErrInvalid %q", name, err, want)
			}
		}
		// SetAdmin(true) clears the scope in the same statement.
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			return tx.CreateScopedIdentity(ctx, "coder", "job1", fixedNow)
		})
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error { return tx.SetAdmin(ctx, "coder", true) })
		if got := storedScope(t, e, rawDSN, "coder"); got != "" {
			t.Errorf("scope after SetAdmin(true) = %q, want none", got)
		}
		// MarkHuman clears it too.
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			return tx.CreateScopedIdentity(ctx, "kim", "job1", fixedNow)
		})
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error { return tx.MarkHuman(ctx, "kim") })
		if got := storedScope(t, e, rawDSN, "kim"); got != "" {
			t.Errorf("scope after MarkHuman = %q, want none", got)
		}
	})
}
