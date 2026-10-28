package bus

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/ericfitz/agentbus2/internal/auth/assertion"
	"github.com/ericfitz/agentbus2/internal/auth/keys"
	"github.com/ericfitz/agentbus2/internal/auth/token"
	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/bus/remote"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/identities"
	"github.com/ericfitz/agentbus2/internal/core/signingkeys"
	"github.com/ericfitz/agentbus2/internal/dbtest"
	"github.com/ericfitz/agentbus2/internal/gen"
	"github.com/ericfitz/agentbus2/internal/keyprovider"
	"github.com/ericfitz/agentbus2/internal/server/cmd"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
)

// syncBuffer is a bytes.Buffer safe for the server goroutine to write while
// the test reads.
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

// listeningRE captures the engine, mode, issuer URL, port and kid from the
// line `agentbus2 server listening on ADDR (ENGINE, registration MODE,
// issuer URL, kid KID)`.
var listeningRE = regexp.MustCompile(`listening on (\S+) \((\w+), registration (\w+), issuer (http://[^:,]+:(\d+)), kid (\S+)\)`)

// authWho is one identity of the exit test.
type authWho struct {
	name   string
	key    crypto.Signer
	bearer string
	bus    *remote.Bus
}

// mintBearer signs a client assertion for name against the issuer's token
// endpoint and exchanges it through c.
func mintBearer(ctx context.Context, c *gen.ClientWithResponses, issuer *token.Issuer, name string, key crypto.Signer) (string, error) {
	kid, err := keys.KID(key.Public())
	if err != nil {
		return "", err
	}
	raw, err := assertion.Sign(assertion.Input{Identity: name, Audience: issuer.URL(), Key: key, KID: kid, Now: time.Now()})
	if err != nil {
		return "", err
	}
	at, err := remote.Token(ctx, c, raw)
	return at.Token, err
}

// TestAuthEndToEnd is the Milestone 5 exit criterion over TCP on each engine
// (spec section 10): the real `agentbus2 server` command, started through
// cmd.Run on a non-loopback listen address with no -registration, so the
// enrollment default is the command's own classification and not a
// composition of unit tests. It is transport-independent (it always uses the
// wire), so it runs identically under both AGENTBUS2_BUS_TRANSPORT values.
// The subtests that expect a refusal are the negative halves; each fails when
// its check is removed (blocks 14 and 15 of scripts/check-gates-selftest.sh
// break two of them).
func TestAuthEndToEnd(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		st, _, dsn := openStoreDSN(t, e)

		// The test creates the signing key before the server starts, so it
		// holds the real key to forge against its kid and to back-date a
		// token; the server's Run then loads the row (the second-start path).
		c, err := core.New(st, core.Defaults(), core.Options{Now: time.Now})
		if err != nil {
			t.Fatal(err)
		}
		svc := identities.Service{Core: c}
		keysSvc := signingkeys.Service{Core: c, Provider: keyprovider.StoreProvider{}, Settings: signingkeys.DefaultSettings()}
		activeRow, created, err := keysSvc.EnsureActive(ctx)
		if err != nil || !created {
			t.Fatalf("EnsureActive = created %v, %v", created, err)
		}
		serverKey, _, err := keys.ParsePrivatePEM([]byte(activeRow.PrivateKeyPEM))
		if err != nil {
			t.Fatal(err)
		}
		serverKID, err := keys.KID(serverKey.Public())
		if err != nil {
			t.Fatal(err)
		}

		storeFlag := "-sqlite"
		if e.Name == "postgres" {
			storeFlag = "-postgres"
		}
		var stdout, stderr syncBuffer
		done := make(chan int, 1)
		go func() {
			done <- cmd.Run(ctx, []string{"-listen", "0.0.0.0:0", storeFlag, dsn, "-set", "signing_key_rotation_cooldown_seconds=3"}, nil, &stdout, &stderr)
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

		engine, mode, issuerURL, port, kid := m[2], m[3], m[4], m[5], m[6]
		if engine != e.Name {
			t.Errorf("engine = %q, want %q", engine, e.Name)
		}
		if mode != "enrollment" {
			t.Errorf("registration mode = %q, want enrollment (Decisions 20, 58, 72: the default on a non-loopback listener)", mode)
		}
		if issuerURL != "http://0.0.0.0:"+port {
			t.Errorf("issuer = %q, want http://0.0.0.0:%s", issuerURL, port)
		}
		if kid != serverKID {
			t.Errorf("kid = %q, want the pre-created key's %q", kid, serverKID)
		}
		out := stdout.String()
		if strings.Contains(out, "generated signing key") {
			t.Errorf("stdout says a signing key was generated, but the row existed:\n%s", out)
		}
		if !strings.Contains(out, "issuer is unset") {
			t.Errorf("stdout lacks the issuer-is-unset warning:\n%s", out)
		}
		if t.Failed() {
			t.FailNow()
		}

		// The client dials 127.0.0.1 while iss and aud are
		// http://0.0.0.0:<port>: the issuer is a configured string, not the
		// request host (choice 7).
		base := "http://127.0.0.1:" + port
		issuer, err := token.NewIssuer(context.Background(), issuerURL, token.StaticKeys(serverKey), time.Hour, time.Now)
		if err != nil {
			t.Fatal(err)
		}
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

		// svc runs on a second Core over the same store: the host command's
		// path to enrollment tokens (cmd.RunAdmin itself is exercised below
		// for make-admin and in the cmd package's tests).
		enroll := func(identity string) string {
			t.Helper()
			tok, _, err := svc.CreateEnrollmentToken(ctx, identities.TokenInput{Identity: identity})
			if err != nil {
				t.Fatal(err)
			}
			return tok
		}
		newWho := func(name string, alg keys.Algorithm) *authWho {
			k, err := keys.Generate(alg)
			if err != nil {
				t.Fatal(err)
			}
			return &authWho{name: name, key: k}
		}
		register := func(w *authWho, tok string) {
			t.Helper()
			res, err := remote.Register(ctx, anon, w.name, w.key.Public(), tok)
			if err != nil || !res.Created {
				t.Fatalf("Register %s = %+v, %v", w.name, res, err)
			}
		}
		login := func(w *authWho) {
			t.Helper()
			bearer, err := mintBearer(ctx, anon, issuer, w.name, w.key)
			if err != nil {
				t.Fatalf("authenticate %s: %v", w.name, err)
			}
			w.bearer = bearer
			w.bus = remote.New(newClient(bearer))
		}

		pat := newWho("pat", keys.EdDSA)
		kim := newWho("kim", keys.ES256) // Decision 55's second algorithm, over TCP
		root := newWho("root", keys.EdDSA)

		// (1) A non-loopback listener without a token refuses registration.
		t.Run("register-without-token-refused", func(t *testing.T) {
			_, err := remote.Register(ctx, anon, "pat", pat.key.Public(), "")
			codeOf(t, err, ab2bus.CodeForbidden)
		})

		// (2)
		t.Run("register-with-token", func(t *testing.T) {
			register(pat, enroll(""))
		})

		// (3)
		t.Run("authenticate", func(t *testing.T) { login(pat) })
		if pat.bus == nil {
			t.Fatal("pat has no bearer; the remaining subtests cannot run")
		}

		// (4) and (5): the bearer requirement.
		t.Run("protected-without-bearer-refused", func(t *testing.T) {
			status, body := getChannels(t, base, "")
			if status != http.StatusUnauthorized || !strings.Contains(body, "unauthenticated") {
				t.Fatalf("GET /v1/channels without a bearer = %d %s, want 401 unauthenticated", status, body)
			}
		})
		t.Run("protected-with-bearer", func(t *testing.T) {
			if status, body := getChannels(t, base, pat.bearer); status != http.StatusOK {
				t.Fatalf("GET /v1/channels with a bearer = %d %s, want 200", status, body)
			}
		})
		t.Run("forged-bearer-refused", func(t *testing.T) {
			// Signed by a key that is not the server's, but carrying the
			// server's iss, aud and kid and a valid subject and expiry.
			_, priv, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			sg, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.EdDSA, Key: priv},
				(&jose.SignerOptions{}).WithType("at+jwt").WithHeader("kid", serverKID))
			if err != nil {
				t.Fatal(err)
			}
			n := time.Now()
			raw, err := jwt.Signed(sg).Claims(jwt.Claims{
				Issuer: issuerURL, Subject: "pat", Audience: jwt.Audience{issuerURL},
				IssuedAt: jwt.NewNumericDate(n), Expiry: jwt.NewNumericDate(n.Add(time.Hour)),
			}).Serialize()
			if err != nil {
				t.Fatal(err)
			}
			if status, body := getChannels(t, base, raw); status != http.StatusUnauthorized {
				t.Fatalf("GET /v1/channels with a forged bearer = %d %s, want 401", status, body)
			}
		})

		// (6) A non-administrator is refused every admin operation.
		t.Run("admin-refused-as-non-admin", func(t *testing.T) {
			_, err := pat.bus.Status(ctx)
			codeOf(t, err, ab2bus.CodeForbidden)
			codeOf(t, pat.bus.Reset(ctx, ab2bus.ResetInput{}), ab2bus.CodeForbidden)
			_, err = pat.bus.ResetCredential(ctx, ab2bus.ResetCredentialInput{Identity: "pat"})
			codeOf(t, err, ab2bus.CodeForbidden)
		})

		// (7) The host command grants the administrator flag (Decision 73).
		t.Run("make-admin-on-host", func(t *testing.T) {
			register(root, enroll(""))
			var so, se bytes.Buffer
			// A nil environ: the developer's AGENTBUS2_SERVER_* variables
			// never choose the store.
			if code := cmd.RunAdmin(ctx, []string{storeFlag, dsn, "admins", "add", "-identity", "root"}, nil, &so, &se); code != 0 {
				t.Fatalf("make-admin exited %d: %s%s", code, so.String(), se.String())
			}
			login(root)
			if _, err := root.bus.Status(ctx); err != nil {
				t.Fatalf("Status as the administrator: %v", err)
			}
		})
		if root.bus == nil {
			t.Fatal("root has no bearer; the remaining subtests cannot run")
		}

		// (8) ACL refusals and successes, over TCP with real bearers.
		t.Run("acl-refusals-and-successes", func(t *testing.T) {
			register(kim, enroll("kim"))
			login(kim)
			// The ACL is non-empty and omits kim (so the clause does not
			// ride on how the remote encodes an empty list).
			sent := []ab2bus.ACLEntry{{Identity: "pat", Permissions: []ab2bus.Permission{ab2bus.PermAdmin}}}
			if _, err := pat.bus.CreateChannel(ctx, ab2bus.CreateChannelInput{Name: "priv", ACL: sent}); err != nil {
				t.Fatalf("CreateChannel: %v", err)
			}
			if got, err := pat.bus.ACL(ctx, "priv"); err != nil || !sameACL(got, sent) {
				t.Fatalf("ACL(priv) = %v, %v; want exactly the entry sent", got, err)
			}
			// Read and write are refused to an identity the ACL omits.
			codeOf(t, kim.bus.Subscribe(ctx, ab2bus.SubscribeInput{Channel: "priv"}), ab2bus.CodeForbidden)
			_, err := kim.bus.History(ctx, ab2bus.HistoryInput{Channel: "priv"})
			codeOf(t, err, ab2bus.CodeForbidden)
			_, err = kim.bus.Send(ctx, ab2bus.SendInput{Channel: "priv", Content: "x"})
			codeOf(t, err, ab2bus.CodeForbidden)
			// The creator holds admin only (Decision 80: discrete sets), so cannot read...
			_, err = pat.bus.History(ctx, ab2bus.HistoryInput{Channel: "priv"})
			codeOf(t, err, ab2bus.CodeForbidden)
			// ...but admin lets the creator rewrite the ACL.
			if err := pat.bus.SetACL(ctx, ab2bus.SetACLInput{Channel: "priv", Entries: []ab2bus.ACLEntry{
				{Identity: "pat", Permissions: []ab2bus.Permission{ab2bus.PermRead, ab2bus.PermWrite}},
				{Identity: "kim", Permissions: []ab2bus.Permission{ab2bus.PermRead}},
			}}); err != nil {
				t.Fatalf("SetACL as the creator: %v", err)
			}
			if _, err := pat.bus.Send(ctx, ab2bus.SendInput{Channel: "priv", Content: "hello"}); err != nil {
				t.Fatalf("creator Send after SetACL: %v", err)
			}
			for _, w := range []*authWho{pat, kim} {
				msgs, err := w.bus.History(ctx, ab2bus.HistoryInput{Channel: "priv"})
				if err != nil || len(msgs) != 1 || msgs[0].Content != "hello" {
					t.Fatalf("%s History = %v, %v; want the one message", w.name, msgs, err)
				}
			}
			_, err = kim.bus.Send(ctx, ab2bus.SendInput{Channel: "priv", Content: "x"})
			codeOf(t, err, ab2bus.CodeForbidden)
			// An administrator with no entry holds admin. SetACL replaces
			// every entry, so the full list is sent.
			if _, err := root.bus.ACL(ctx, "priv"); err != nil {
				t.Fatalf("ACL as the administrator: %v", err)
			}
			full := []ab2bus.ACLEntry{
				{Identity: "pat", Permissions: []ab2bus.Permission{ab2bus.PermRead, ab2bus.PermWrite}},
				{Identity: "kim", Permissions: []ab2bus.Permission{ab2bus.PermRead}},
				{Identity: "root", Permissions: []ab2bus.Permission{ab2bus.PermRead, ab2bus.PermWrite}},
			}
			if err := root.bus.SetACL(ctx, ab2bus.SetACLInput{Channel: "priv", Entries: full}); err != nil {
				t.Fatalf("SetACL as the administrator: %v", err)
			}
			if got, err := root.bus.ACL(ctx, "priv"); err != nil || !sameACL(got, full) {
				t.Fatalf("ACL(priv) after the administrator's SetACL = %v, %v; want %v", got, err, full)
			}
			if _, err := root.bus.Send(ctx, ab2bus.SendInput{Channel: "priv", Content: "from root"}); err != nil {
				t.Fatalf("administrator Send: %v", err)
			}
			if msgs, err := kim.bus.History(ctx, ab2bus.HistoryInput{Channel: "priv"}); err != nil || len(msgs) != 2 {
				t.Fatalf("kim History = %v, %v; want two messages", msgs, err)
			}
			// A channel created without an ACL carries the Decision 88 default.
			if _, err := pat.bus.CreateChannel(ctx, ab2bus.CreateChannelInput{Name: "open"}); err != nil {
				t.Fatal(err)
			}
			if _, err := kim.bus.Send(ctx, ab2bus.SendInput{Channel: "open", Content: "hi"}); err != nil {
				t.Fatalf("kim Send on the default-ACL channel: %v", err)
			}
			if msgs, err := kim.bus.History(ctx, ab2bus.HistoryInput{Channel: "open"}); err != nil || len(msgs) != 1 {
				t.Fatalf("kim History on the default-ACL channel = %v, %v", msgs, err)
			}
		})

		// (9) reset-credential removes the keys, not the minted bearers.
		t.Run("reset-credential-keeps-token", func(t *testing.T) {
			res, err := root.bus.ResetCredential(ctx, ab2bus.ResetCredentialInput{Identity: "pat"})
			if err != nil || res.KeysRemoved != 1 {
				t.Fatalf("ResetCredential = %+v, %v; want 1 key removed", res, err)
			}
			if status, body := getChannels(t, base, pat.bearer); status != http.StatusOK {
				t.Fatalf("pat's minted bearer after reset-credential = %d %s, want 200 until exp (Decision (Eric, 2026-10-21), Milestone 5 item 4)", status, body)
			}
			if _, err := mintBearer(ctx, anon, issuer, "pat", pat.key); ab2bus.CodeOf(err) != ab2bus.CodeUnauthenticated {
				t.Fatalf("token with the old key = %v, want unauthenticated", err)
			}
			newKey, err := keys.Generate(keys.EdDSA)
			if err != nil {
				t.Fatal(err)
			}
			rr, err := remote.Register(ctx, anon, "pat", newKey.Public(), enroll("pat"))
			if err != nil || rr.Created {
				t.Fatalf("Register with the new key = %+v, %v; want Created false", rr, err)
			}
			if _, err := mintBearer(ctx, anon, issuer, "pat", newKey); err != nil {
				t.Fatalf("token with the new key: %v", err)
			}
		})

		// (10) A stale bearer is replaced on the token endpoint, refused elsewhere.
		t.Run("stale-bearer-on-token-endpoint", func(t *testing.T) {
			past, err := token.NewIssuer(context.Background(), issuerURL, token.StaticKeys(serverKey), time.Hour, func() time.Time { return time.Now().Add(-2 * time.Hour) })
			if err != nil {
				t.Fatal(err)
			}
			expired, _, err := past.Mint(context.Background(), token.MintInput{Identity: "root", Kind: token.KindAgent})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := mintBearer(ctx, newClient(expired), issuer, "root", root.key); err != nil {
				t.Fatalf("token endpoint with an expired bearer: %v", err)
			}
			if status, body := getChannels(t, base, expired); status != http.StatusUnauthorized {
				t.Fatalf("GET /v1/channels with an expired bearer = %d %s, want 401", status, body)
			}
		})

		// (11) Signing key rotation over TCP. The server runs with a 3 s
		// cooldown (the 3600 s default would refuse the first rotation of a
		// key created seconds ago); the second rotate follows at once.
		t.Run("signing-key-rotation", func(t *testing.T) {
			before := root.bearer // signed by the key created at the start
			set, err := root.bus.ListSigningKeys(ctx)
			if err != nil || len(set) != 1 {
				t.Fatalf("ListSigningKeys = %+v, %v; want the one pre-created key", set, err)
			}
			if wait := time.Until(time.UnixMilli(set[0].CreatedAt).Add(3500 * time.Millisecond)); wait > 0 {
				time.Sleep(wait)
			}
			rot, err := root.bus.RotateSigningKey(ctx, ab2bus.RotateSigningKeyInput{})
			if err != nil || rot.Active.KID == serverKID || rot.Previous.KID != serverKID {
				t.Fatalf("RotateSigningKey = %+v, %v; want a new active key and %s as previous", rot, err, serverKID)
			}

			// Right after the first rotation, inside the cooldown.
			_, err = root.bus.RotateSigningKey(ctx, ab2bus.RotateSigningKeyInput{})
			codeOf(t, err, ab2bus.CodeConflict)

			resp, err := http.Get(base + "/v1/auth/jwks")
			if err != nil {
				t.Fatal(err)
			}
			var jwks struct {
				Keys []struct {
					KID string `json:"kid"`
				} `json:"keys"`
			}
			err = json.NewDecoder(resp.Body).Decode(&jwks)
			_ = resp.Body.Close()
			if err != nil || len(jwks.Keys) != 2 {
				t.Fatalf("JWKS = %+v, %v; want two keys", jwks, err)
			}
			if status, body := getChannels(t, base, before); status != http.StatusOK {
				t.Fatalf("a bearer minted before the rotation = %d %s, want 200", status, body)
			}
			fresh, err := mintBearer(ctx, anon, issuer, "root", root.key)
			if err != nil {
				t.Fatal(err)
			}
			if got := bearerKID(t, fresh); got != rot.Active.KID {
				t.Fatalf("a new login is signed with kid %s, want the new active %s", got, rot.Active.KID)
			}

			// The revoked key is the one active before: the new one (rot.Active).
			var revokedPEM string
			if err := st.Read(ctx, func(ctx context.Context, tx ab2store.Tx) error {
				rows, err := tx.ListSigningKeys(ctx)
				for _, r := range rows {
					if r.KID == rot.Active.KID {
						revokedPEM = r.PrivateKeyPEM
					}
				}
				return err
			}); err != nil || revokedPEM == "" {
				t.Fatalf("reading the key %s: %v", rot.Active.KID, err)
			}
			revokedKey, _, err := keys.ParsePrivatePEM([]byte(revokedPEM))
			if err != nil {
				t.Fatal(err)
			}
			rev, err := root.bus.RotateSigningKey(ctx, ab2bus.RotateSigningKeyInput{RevokePrevious: true})
			if err != nil || !rev.Revoked || rev.Previous.KID != rot.Active.KID {
				t.Fatalf("revoking RotateSigningKey = %+v, %v; want %s revoked", rev, err, rot.Active.KID)
			}
			local, err := token.NewIssuer(context.Background(), issuerURL, token.StaticKeys(revokedKey), time.Hour, time.Now)
			if err != nil {
				t.Fatal(err)
			}
			forged, _, err := local.Mint(context.Background(), token.MintInput{Identity: "root", Kind: token.KindAgent})
			if err != nil {
				t.Fatal(err)
			}
			if status, body := getChannels(t, base, forged); status != http.StatusUnauthorized {
				t.Fatalf("a bearer signed by the revoked key = %d %s, want 401", status, body)
			}
			if status, body := getChannels(t, base, before); status != http.StatusOK {
				t.Fatalf("the retiring key's bearer after the revoke = %d %s, want 200", status, body)
			}
		})

		stop()
	})
}

// sameACL compares two ACLs ignoring entry and permission order.
func sameACL(a, b []ab2bus.ACLEntry) bool {
	norm := func(es []ab2bus.ACLEntry) string {
		var out []string
		for _, e := range es {
			ps := make([]string, len(e.Permissions))
			for i, p := range e.Permissions {
				ps[i] = string(p)
			}
			sort.Strings(ps)
			out = append(out, e.Identity+"="+strings.Join(ps, ","))
		}
		sort.Strings(out)
		return fmt.Sprint(out)
	}
	return norm(a) == norm(b)
}

// getChannels is a raw GET /v1/channels with an optional bearer.
func getChannels(t *testing.T, base, bearer string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, base+"/v1/channels", nil)
	if err != nil {
		t.Fatal(err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}
