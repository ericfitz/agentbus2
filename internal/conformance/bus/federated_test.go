package bus

import (
	"bytes"
	"context"
	"crypto"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/auth/assertion"
	"github.com/ericfitz/agentbus2/internal/auth/keys"
	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/bus/remote"
	"github.com/ericfitz/agentbus2/internal/dbtest"
	"github.com/ericfitz/agentbus2/internal/gen"
	"github.com/ericfitz/agentbus2/internal/oidctest"
	"github.com/ericfitz/agentbus2/internal/server/cmd"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
)

// TestFederatedEndToEnd is the federated-identity exit over TCP on each engine
// (spec 10): the real `agentbus2 server` command with a fake OIDC provider
// configured as the issuers entry "corp", users and admins entries and a
// pre-created human added by the host commands. It is transport-independent
// (it always uses the wire). Seven leaves per engine.
func TestFederatedEndToEnd(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		st, _, dsn := openStoreDSN(t, e)
		prov := oidctest.New(t, oidctest.Options{Audience: "pai"})
		issuersJSON, err := json.Marshal([]map[string]any{{
			"name": "corp", "issuer": prov.URL(), "jwks_uri": prov.JWKSURI(), "audience": "pai",
		}})
		if err != nil {
			t.Fatal(err)
		}
		storeFlag := "-sqlite"
		if e.Name == "postgres" {
			storeFlag = "-postgres"
		}
		setIssuers := "issuers=" + string(issuersJSON)

		// The host commands run before the server starts (a nil environ: the
		// developer's AGENTBUS2_SERVER_* variables never choose the store).
		admin := func(args ...string) {
			t.Helper()
			var so, se bytes.Buffer
			full := append([]string{storeFlag, dsn, "-set", setIssuers}, args...)
			if code := cmd.RunAdmin(ctx, full, nil, &so, &se); code != 0 {
				t.Fatalf("admin %v exited %d: %s%s", args, code, so.String(), se.String())
			}
		}
		admin("users", "add", "-issuer", "corp", "-group", "pai-users")
		admin("admins", "add", "-issuer", "corp", "-group", "pai-admins")
		admin("create-human", "kim@corp.example")

		var stdout, stderr syncBuffer
		done := make(chan int, 1)
		go func() {
			done <- cmd.Run(ctx, []string{"-listen", "127.0.0.1:0", storeFlag, dsn, "-set", setIssuers}, nil, &stdout, &stderr)
		}()
		stopped := false
		stop := func() {
			if stopped {
				return
			}
			stopped = true
			cancel()
			select {
			case code := <-done:
				if code != 0 {
					t.Errorf("Run returned %d after cancel, want 0; stderr %q", code, stderr.String())
				}
			case <-time.After(5 * time.Second):
				t.Error("Run did not return within 5 s of cancel")
			}
		}
		defer stop()
		var m []string
		deadline := time.Now().Add(20 * time.Second)
		for m = listeningRE.FindStringSubmatch(stdout.String()); m == nil; m = listeningRE.FindStringSubmatch(stdout.String()) {
			select {
			case code := <-done:
				stopped = true
				t.Fatalf("Run exited %d before listening; stdout %q stderr %q", code, stdout.String(), stderr.String())
			case <-time.After(20 * time.Millisecond):
			}
			if time.Now().After(deadline) {
				t.Fatalf("no listening line; stdout %q stderr %q", stdout.String(), stderr.String())
			}
		}
		issuerURL, port := m[4], m[5]
		base := "http://127.0.0.1:" + port
		newClient := func(bearer string) *gen.ClientWithResponses {
			var opts []gen.ClientOption
			if bearer != "" {
				opts = append(opts, gen.WithRequestEditorFn(remote.WithBearer(bearer)))
			}
			cl, err := gen.NewClientWithResponses(base, opts...)
			if err != nil {
				t.Fatal(err)
			}
			return cl
		}
		anon := newClient("")

		// An agent registers a key (open registration on a loopback listener)
		// and signs in with a client assertion.
		var agentKey crypto.Signer
		agentKey, err = keys.Generate(keys.EdDSA)
		if err != nil {
			t.Fatal(err)
		}
		if res, err := remote.Register(ctx, anon, "agent1", agentKey.Public(), ""); err != nil || !res.Created {
			t.Fatalf("Register agent1 = %+v, %v", res, err)
		}
		kid, err := keys.KID(agentKey.Public())
		if err != nil {
			t.Fatal(err)
		}
		raw, err := assertion.Sign(assertion.Input{Identity: "agent1", Audience: issuerURL, Key: agentKey, KID: kid, Now: time.Now()})
		if err != nil {
			t.Fatal(err)
		}
		at, err := remote.Token(ctx, anon, raw)
		if err != nil {
			t.Fatalf("agent token: %v", err)
		}
		agent := remote.New(newClient(at.Token))

		const ericName, kimName = "eric@corp.example", "kim@corp.example"
		corp := func(email, sub string, groups ...string) string {
			return prov.Mint(map[string]any{"email": email, "sub": sub, "groups": groups})
		}
		humanBus := func(subjectToken string) *remote.Bus {
			t.Helper()
			at, err := remote.TokenExchange(ctx, anon, subjectToken)
			if err != nil {
				t.Fatalf("TokenExchange: %v", err)
			}
			return remote.New(newClient(at.Token))
		}
		identityRow := func(name string) (found bool, binding ab2store.HumanBinding, count int) {
			t.Helper()
			err := st.Read(ctx, func(ctx context.Context, tx ab2store.Tx) error {
				all, err := tx.ListIdentities(ctx)
				if err != nil {
					return err
				}
				count = len(all)
				if _, err := tx.Identity(ctx, name); err != nil {
					if errors.Is(err, ab2store.ErrNotFound) {
						return nil
					}
					return err
				}
				found = true
				binding, err = tx.HumanBinding(ctx, name)
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			return
		}

		// The leaves share the server and the Store but no state of their own:
		// each sets up what it needs, and they run in reverse of the order
		// written here so a hidden dependency on that order fails.
		var leaves []struct {
			name string
			run  func(*testing.T)
		}
		leaf := func(name string, run func(*testing.T)) {
			leaves = append(leaves, struct {
				name string
				run  func(*testing.T)
			}{name, run})
		}

		leaf("exchange-then-send", func(t *testing.T) {
			eric := humanBus(corp(ericName, "g-eric", "pai-users"))
			sess, err := agent.Open(ctx, ab2bus.OpenSessionInput{Hint: "impl"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := eric.Send(ctx, ab2bus.SendInput{Channel: "general", Content: "hello from a human"}); err != nil {
				t.Fatalf("human Send: %v", err)
			}
			rec, err := agent.Receive(ctx, ab2bus.ReceiveInput{Session: sess.ID})
			if err != nil {
				t.Fatal(err)
			}
			var got *ab2bus.Message
			for i := range rec.Messages {
				if rec.Messages[i].Content == "hello from a human" {
					got = &rec.Messages[i]
				}
			}
			if got == nil || got.SenderKind != ab2bus.IdentityHuman || got.Sender != ericName {
				t.Fatalf("agent received %+v, want the human's message with sender_kind human", rec.Messages)
			}
		})

		leaf("not-in-users-refused", func(t *testing.T) {
			_, err := remote.TokenExchange(ctx, anon, corp("mallory@corp.example", "g-mal", "elsewhere"))
			codeOf(t, err, ab2bus.CodeValidation)
			if found, _, _ := identityRow("mallory@corp.example"); found {
				t.Fatal("a refused sign-in created an identity")
			}
		})

		leaf("precreated-binds", func(t *testing.T) {
			found, b, before := identityRow(kimName)
			if !found || b.ProviderSubject != "" {
				t.Fatalf("precreated kim: found %v, binding %+v", found, b)
			}
			kim := humanBus(corp(kimName, "g-kim", "pai-users"))
			if _, err := kim.Identities(ctx); err != nil {
				t.Fatalf("kim with the exchanged bearer: %v", err)
			}
			found, b, after := identityRow(kimName)
			if !found || b.ProviderSubject != "g-kim" || b.Issuer != prov.URL() {
				t.Fatalf("kim after sign-in: found %v, binding %+v", found, b)
			}
			if after != before {
				t.Errorf("identity rows %d -> %d, want no new identity", before, after)
			}
		})

		leaf("admins-group-sets-flag", func(t *testing.T) {
			eric := humanBus(corp(ericName, "g-eric", "pai-users"))
			if _, err := eric.Status(ctx); ab2bus.CodeOf(err) != ab2bus.CodeForbidden {
				t.Fatalf("Status without the admins group = %v, want forbidden", err)
			}
			ericAdmin := humanBus(corp(ericName, "g-eric", "pai-users", "pai-admins"))
			if _, err := ericAdmin.Status(ctx); err != nil {
				t.Fatalf("Status with the admins group: %v", err)
			}
		})

		leaf("group-removed-clears-flag", func(t *testing.T) {
			ericAdmin := humanBus(corp(ericName, "g-eric", "pai-users", "pai-admins"))
			if _, err := ericAdmin.Status(ctx); err != nil {
				t.Fatalf("Status with the admins group: %v", err)
			}
			eric := humanBus(corp(ericName, "g-eric", "pai-users"))
			_, err := eric.Status(ctx)
			codeOf(t, err, ab2bus.CodeForbidden)
			// The flag is the stored evaluation: the older bearer loses it too.
			_, err = ericAdmin.Status(ctx)
			codeOf(t, err, ab2bus.CodeForbidden)
		})

		leaf("key-registration-on-human-refused", func(t *testing.T) {
			humanBus(corp(ericName, "g-eric", "pai-users")) // eric exists, whichever leaf ran first
			k, err := keys.Generate(keys.EdDSA)
			if err != nil {
				t.Fatal(err)
			}
			_, err = remote.Register(ctx, anon, ericName, k.Public(), "")
			codeOf(t, err, ab2bus.CodeForbidden)
		})

		leaf("direct-corporate-bearer-refused", func(t *testing.T) {
			if status, body := getChannels(t, base, corp(ericName, "g-eric", "pai-users")); status != http.StatusUnauthorized {
				t.Fatalf("GET /v1/channels with a corporate token = %d %s, want 401", status, body)
			}
		})

		for i := len(leaves) - 1; i >= 0; i-- {
			t.Run(leaves[i].name, leaves[i].run)
		}
		stop()
	})
}
