package identities

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core/scopes"
	"github.com/ericfitz/agentbus2/internal/store"
)

// adminSvc returns a Service whose "root" is an administrator, plus the scopes
// service over the same Core.
func adminSvc(t *testing.T) (Service, scopes.Service, *clock) {
	t.Helper()
	s, clk := newSvc(t, nil)
	ctx := context.Background()
	if err := s.RegisterLocal(ctx, "root"); err != nil {
		t.Fatal(err)
	}
	if err := s.MakeAdmin(ctx, "root", true); err != nil {
		t.Fatal(err)
	}
	return s, scopes.Service{Core: s.Core}, clk
}

func mkScope(t *testing.T, sc scopes.Service, name string) {
	t.Helper()
	if _, err := sc.Create(context.Background(), "root", bus.CreateScopeInput{Name: name}); err != nil {
		t.Fatalf("create scope %s: %v", name, err)
	}
}

func boundTokens(t *testing.T, s Service, name string) []store.EnrollmentToken {
	t.Helper()
	rows, err := s.ListEnrollmentTokens(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out []store.EnrollmentToken
	for _, r := range rows {
		if r.Identity == name {
			out = append(out, r)
		}
	}
	return out
}

func TestCreateAgentIdentity(t *testing.T) {
	ctx := context.Background()
	s, sc, _ := adminSvc(t)
	mkScope(t, sc, "job1")
	mkScope(t, sc, "job2")

	t.Run("scoped", func(t *testing.T) {
		res, err := s.CreateAgentIdentityAs(ctx, "root", bus.CreateAgentIdentityInput{Name: "job1-coder", Scope: "job1"})
		if err != nil {
			t.Fatal(err)
		}
		if !res.Created || res.Identity != "job1-coder" || res.Scope != "job1" || !strings.HasPrefix(res.EnrollmentToken, "abe_") {
			t.Fatalf("result = %+v", res)
		}
		if want := startMs + (24 * time.Hour).Milliseconds(); res.ExpiresAt != want {
			t.Fatalf("ExpiresAt = %d, want %d", res.ExpiresAt, want)
		}
		id, ok := identityRow(t, s, "job1-coder")
		if !ok || id.Scope != "job1" || id.Kind != store.IdentityAgent {
			t.Fatalf("row = %+v, %v", id, ok)
		}
		if _, ok := generalSub(t, s, "job1-coder"); ok {
			t.Fatal("a scoped agent holds a general subscription (Decision 19)")
		}
		toks := boundTokens(t, s, "job1-coder")
		if len(toks) != 1 || toks[0].UsesLeft != 1 || toks[0].ExpiresAt != res.ExpiresAt {
			t.Fatalf("tokens = %+v", toks)
		}

		// A second call: a fresh token, the first revoked, one bound token left.
		res2, err := s.CreateAgentIdentityAs(ctx, "root", bus.CreateAgentIdentityInput{Name: "job1-coder", Scope: "job1"})
		if err != nil {
			t.Fatal(err)
		}
		if res2.Created || res2.EnrollmentToken == res.EnrollmentToken || res2.EnrollmentToken == "" {
			t.Fatalf("second = %+v", res2)
		}
		if n := len(boundTokens(t, s, "job1-coder")); n != 1 {
			t.Fatalf("%d bound tokens after a retry, want 1", n)
		}
		_, err = s.Register(ctx, ModeEnrollment, RegisterInput{Identity: "job1-coder", Key: genKey(t), EnrollmentToken: res.EnrollmentToken})
		wantCode(t, err, bus.CodeForbidden)

		// The fresh token registers the agent's own key.
		if _, err := s.Register(ctx, ModeEnrollment, RegisterInput{Identity: "job1-coder", Key: genKey(t), EnrollmentToken: res2.EnrollmentToken}); err != nil {
			t.Fatal(err)
		}
		// With a key: conflict.
		_, err = s.CreateAgentIdentityAs(ctx, "root", bus.CreateAgentIdentityInput{Name: "job1-coder", Scope: "job1"})
		wantCode(t, err, bus.CodeConflict)
		wantDetail(t, err, `identity "job1-coder" already has a credential`)
	})

	t.Run("unscoped", func(t *testing.T) {
		res, err := s.CreateAgentIdentityAs(ctx, "root", bus.CreateAgentIdentityInput{Name: "pai-2"})
		if err != nil {
			t.Fatal(err)
		}
		if !res.Created || res.Scope != "" || res.EnrollmentToken == "" {
			t.Fatalf("result = %+v", res)
		}
		if _, ok := generalSub(t, s, "pai-2"); !ok {
			t.Fatal("an unscoped agent lacks the general subscription")
		}
		// Another scope, or scoped vs unscoped: conflict naming both.
		_, err = s.CreateAgentIdentityAs(ctx, "root", bus.CreateAgentIdentityInput{Name: "pai-2", Scope: "job1"})
		wantCode(t, err, bus.CodeConflict)
		wantDetail(t, err, "pai-2")
	})

	t.Run("other scope", func(t *testing.T) {
		if _, err := s.CreateAgentIdentityAs(ctx, "root", bus.CreateAgentIdentityInput{Name: "job2-a", Scope: "job2"}); err != nil {
			t.Fatal(err)
		}
		_, err := s.CreateAgentIdentityAs(ctx, "root", bus.CreateAgentIdentityInput{Name: "job2-a", Scope: "job1"})
		wantCode(t, err, bus.CodeConflict)
		wantDetail(t, err, "job2")
		wantDetail(t, err, "job1")
		_, err = s.CreateAgentIdentityAs(ctx, "root", bus.CreateAgentIdentityInput{Name: "job2-a"})
		wantCode(t, err, bus.CodeConflict)
	})

	t.Run("human name", func(t *testing.T) {
		name, _, err := s.CreateIdentityByEmail(ctx, "eric@corp.example")
		if err != nil {
			t.Fatal(err)
		}
		_, err = s.CreateAgentIdentityAs(ctx, "root", bus.CreateAgentIdentityInput{Name: name, Scope: "job1"})
		wantCode(t, err, bus.CodeConflict)
		_, err = s.CreateAgentIdentityAs(ctx, "root", bus.CreateAgentIdentityInput{Name: name})
		wantCode(t, err, bus.CodeConflict)
	})

	t.Run("scope states", func(t *testing.T) {
		_, err := s.CreateAgentIdentityAs(ctx, "root", bus.CreateAgentIdentityInput{Name: "x", Scope: "nope"})
		wantCode(t, err, bus.CodeNotFound)
		mkScope(t, sc, "job3")
		if _, err := sc.Done(ctx, "root", bus.DoneScopeInput{Name: "job3", Retire: bus.RetireNormal}); err != nil {
			t.Fatal(err)
		}
		_, err = s.CreateAgentIdentityAs(ctx, "root", bus.CreateAgentIdentityInput{Name: "job3-a", Scope: "job3"})
		wantCode(t, err, bus.CodeConflict)
		if _, ok := identityRow(t, s, "job3-a"); ok {
			t.Fatal("a row was created in a done scope")
		}
	})

	t.Run("reserved and bad names", func(t *testing.T) {
		for _, n := range []string{"everyone", "admins", "slack-bridge", "a/b", ""} {
			_, err := s.CreateAgentIdentityAs(ctx, "root", bus.CreateAgentIdentityInput{Name: n})
			wantCode(t, err, bus.CodeValidation)
		}
	})

	t.Run("admin only", func(t *testing.T) {
		if err := s.RegisterLocal(ctx, "pat"); err != nil {
			t.Fatal(err)
		}
		_, err := s.CreateAgentIdentityAs(ctx, "pat", bus.CreateAgentIdentityInput{Name: "x1"})
		wantCode(t, err, bus.CodeForbidden)
		if _, ok := identityRow(t, s, "x1"); ok {
			t.Fatal("a non-admin created a row")
		}
	})

	t.Run("host path", func(t *testing.T) {
		res, err := s.CreateAgentIdentity(ctx, bus.CreateAgentIdentityInput{Name: "job1-host", Scope: "job1"})
		if err != nil || !res.Created || res.EnrollmentToken == "" {
			t.Fatalf("CreateAgentIdentity = %+v, %v", res, err)
		}
	})
}

func TestRegisterScopedNeedsBoundToken(t *testing.T) {
	ctx := context.Background()
	for _, mode := range []Mode{ModeOpen, ModeEnrollment} {
		t.Run(string(mode), func(t *testing.T) {
			s, sc, _ := adminSvc(t)
			mkScope(t, sc, "job1")
			res, err := s.CreateAgentIdentityAs(ctx, "root", bus.CreateAgentIdentityInput{Name: "job1-a", Scope: "job1"})
			if err != nil {
				t.Fatal(err)
			}
			want := `identity "job1-a" is scoped; it registers only with the enrollment token bound to it`

			_, err = s.Register(ctx, mode, RegisterInput{Identity: "job1-a", Key: genKey(t)})
			wantCode(t, err, bus.CodeForbidden)
			wantDetail(t, err, want)

			unbound, urow := newToken(t, s, TokenInput{})
			_, err = s.Register(ctx, mode, RegisterInput{Identity: "job1-a", Key: genKey(t), EnrollmentToken: unbound})
			wantCode(t, err, bus.CodeForbidden)
			wantDetail(t, err, want)
			if tokenRow(t, s, urow.ID).UsesLeft != 1 {
				t.Fatal("the unbound token was consumed")
			}

			other, orow := newToken(t, s, TokenInput{Identity: "someone-else"})
			_, err = s.Register(ctx, mode, RegisterInput{Identity: "job1-a", Key: genKey(t), EnrollmentToken: other})
			wantCode(t, err, bus.CodeForbidden)
			if tokenRow(t, s, orow.ID).UsesLeft != 1 {
				t.Fatal("the other-name token was consumed")
			}
			if len(keysOf(t, s, "job1-a")) != 0 {
				t.Fatal("a key was bound without the bound token")
			}

			got, err := s.Register(ctx, mode, RegisterInput{Identity: "job1-a", Key: genKey(t), EnrollmentToken: res.EnrollmentToken})
			if err != nil || got.Created {
				t.Fatalf("bound token = %+v, %v", got, err)
			}
			if len(keysOf(t, s, "job1-a")) != 1 {
				t.Fatal("key not bound")
			}
			if n := len(boundTokens(t, s, "job1-a")); n != 1 || boundTokens(t, s, "job1-a")[0].UsesLeft != 0 {
				t.Fatalf("bound token not consumed: %+v", boundTokens(t, s, "job1-a"))
			}
		})
	}
}

func TestRegisterNeverCreatesScoped(t *testing.T) {
	s, _ := newSvc(t, nil)
	if _, err := s.Register(context.Background(), ModeOpen, RegisterInput{Identity: "pat", Key: genKey(t)}); err != nil {
		t.Fatal(err)
	}
	id, ok := identityRow(t, s, "pat")
	if !ok || id.Scope != "" {
		t.Fatalf("row = %+v, %v", id, ok)
	}
}

func TestRegisterBoundTokenRecovers(t *testing.T) {
	ctx := context.Background()
	s, _, _ := adminSvc(t)

	tok, _ := newToken(t, s, TokenInput{Identity: "root"})
	if _, err := s.Register(ctx, ModeEnrollment, RegisterInput{Identity: "root", Key: genKey(t), EnrollmentToken: tok}); err != nil {
		t.Fatal(err)
	}
	if n, err := s.ResetCredential(ctx, "root"); err != nil || n != 1 {
		t.Fatalf("ResetCredential = %d, %v", n, err)
	}
	tok2, _ := newToken(t, s, TokenInput{Identity: "root"})
	if _, err := s.Register(ctx, ModeEnrollment, RegisterInput{Identity: "root", Key: genKey(t), EnrollmentToken: tok2}); err != nil {
		t.Fatalf("a bound token must recover a reset administrator: %v", err)
	}

	res, err := s.CreateAgentIdentityAs(ctx, "root", bus.CreateAgentIdentityInput{Name: "kim"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Register(ctx, ModeEnrollment, RegisterInput{Identity: "kim", Key: genKey(t), EnrollmentToken: res.EnrollmentToken}); err != nil {
		t.Fatalf("the unscoped identity's bound token must register: %v", err)
	}
}

func TestResetCredentialHumanClearsSlackUser(t *testing.T) {
	ctx := context.Background()
	s, _, _ := adminSvc(t)
	name, _, err := s.CreateIdentityByEmail(ctx, "eric@corp.example")
	if err != nil {
		t.Fatal(err)
	}
	tx(t, s, func(ctx context.Context, tx store.Tx) error {
		if err := tx.SetHumanSubject(ctx, name, "corp", "sub-1"); err != nil {
			return err
		}
		return tx.PutSlackUser(ctx, store.SlackUser{Identity: name, TeamID: "T1", UserID: "U1", CreatedAt: 1})
	})
	if n, err := s.ResetCredential(ctx, name); err != nil || n != 1 {
		t.Fatalf("ResetCredential = %d, %v", n, err)
	}
	tx(t, s, func(ctx context.Context, tx store.Tx) error {
		if _, err := tx.SlackUserOf(ctx, name); err == nil {
			t.Error("the slack_users row survived the reset")
		}
		b, err := tx.HumanBinding(ctx, name)
		if err != nil || b.ProviderSubject != "" {
			t.Errorf("binding = %+v, %v", b, err)
		}
		return nil
	})
	// A second run, and a human whose subject is already clear but who has a
	// Slack row: the row still goes.
	tx(t, s, func(ctx context.Context, tx store.Tx) error {
		return tx.PutSlackUser(ctx, store.SlackUser{Identity: name, TeamID: "T1", UserID: "U2", CreatedAt: 2})
	})
	if n, err := s.ResetCredential(ctx, name); err != nil || n != 0 {
		t.Fatalf("second ResetCredential = %d, %v", n, err)
	}
	tx(t, s, func(ctx context.Context, tx store.Tx) error {
		if _, err := tx.SlackUserOf(ctx, name); err == nil {
			t.Error("the slack_users row survived the second reset")
		}
		return nil
	})
}

// Two overlapping CreateAgentIdentityAs calls for the same scoped name end
// with exactly one live bound token (agent.go revokes before it inserts, and
// the Store serializes the transactions).
func TestCreateAgentIdentityOverlap(t *testing.T) {
	ctx := context.Background()
	s, sc, _ := adminSvc(t)
	mkScope(t, sc, "job1")
	const n = 4
	var wg sync.WaitGroup
	errs := make([]error, n)
	toks := make([]string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := s.CreateAgentIdentityAs(ctx, "root", bus.CreateAgentIdentityInput{Name: "job1-coder", Scope: "job1"})
			errs[i], toks[i] = err, res.EnrollmentToken
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	rows := boundTokens(t, s, "job1-coder")
	if len(rows) != 1 {
		t.Fatalf("%d bound tokens after %d overlapping calls, want exactly 1", len(rows), n)
	}
	matches := 0
	for _, tok := range toks {
		if HashToken(tok) == rows[0].TokenHash {
			matches++
		}
	}
	if matches != 1 {
		t.Fatalf("the surviving token matches %d of the returned tokens, want 1", matches)
	}
}

// A bound token past its expiry does not register the agent, and the refusal
// consumes nothing (register.go).
func TestRegisterScopedExpiredBoundToken(t *testing.T) {
	ctx := context.Background()
	for _, mode := range []Mode{ModeOpen, ModeEnrollment} {
		t.Run(string(mode), func(t *testing.T) {
			s, sc, clk := adminSvc(t)
			mkScope(t, sc, "job1")
			res, err := s.CreateAgentIdentityAs(ctx, "root", bus.CreateAgentIdentityInput{Name: "job1-a", Scope: "job1"})
			if err != nil {
				t.Fatal(err)
			}
			clk.advance(agentTokenLifetime + time.Second)
			_, err = s.Register(ctx, mode, RegisterInput{Identity: "job1-a", Key: genKey(t), EnrollmentToken: res.EnrollmentToken})
			if err == nil {
				t.Fatal("an expired bound token registered the agent")
			}
			wantCode(t, err, bus.CodeForbidden)
			if len(keysOf(t, s, "job1-a")) != 0 {
				t.Fatal("a key was bound")
			}
			// A fresh token issued after the expiry works.
			res2, err := s.CreateAgentIdentityAs(ctx, "root", bus.CreateAgentIdentityInput{Name: "job1-a", Scope: "job1"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Register(ctx, mode, RegisterInput{Identity: "job1-a", Key: genKey(t), EnrollmentToken: res2.EnrollmentToken}); err != nil {
				t.Fatalf("fresh token: %v", err)
			}
		})
	}
}
