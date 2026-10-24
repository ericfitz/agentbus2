package server

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/auth/keys"
	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core/identities"
	"github.com/ericfitz/agentbus2/internal/gen"
	"github.com/ericfitz/agentbus2/internal/store"
)

// keyFor registers a fresh key for an identity through the service and
// returns its kid.
func (e *env) keyFor(name string) string {
	e.t.Helper()
	k, err := keys.Generate(keys.EdDSA)
	if err != nil {
		e.t.Fatal(err)
	}
	der, err := keys.MarshalPublic(k.Public())
	if err != nil {
		e.t.Fatal(err)
	}
	kid, err := keys.KID(k.Public())
	if err != nil {
		e.t.Fatal(err)
	}
	err = e.store.Tx(context.Background(), func(ctx context.Context, tx store.Tx) error {
		return tx.PutPublicKey(ctx, store.PublicKey{Identity: name, KID: kid, Algorithm: string(keys.EdDSA), Key: der, CreatedAt: e.core.NowMs()})
	})
	if err != nil {
		e.t.Fatalf("PutPublicKey %q: %v", name, err)
	}
	return kid
}

func (e *env) keyCount(name string) int {
	e.t.Helper()
	var n int
	err := e.store.Read(context.Background(), func(ctx context.Context, tx store.Tx) error {
		ks, err := tx.PublicKeys(ctx, name)
		n = len(ks)
		return err
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *env) hasIdentity(name string) bool {
	e.t.Helper()
	found := false
	err := e.store.Read(context.Background(), func(ctx context.Context, tx store.Tx) error {
		ids, err := tx.ListIdentities(ctx)
		for _, i := range ids {
			if i.Name == name {
				found = true
			}
		}
		return err
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return found
}

func (e *env) tokenCount() int {
	e.t.Helper()
	var n int
	err := e.store.Read(context.Background(), func(ctx context.Context, tx store.Tx) error {
		ts, err := tx.ListEnrollmentTokens(ctx)
		n = len(ts)
		return err
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *env) post(identity, path string, hdr map[string]string) (int, string) {
	e.t.Helper()
	req, err := http.NewRequest(http.MethodPost, "http://inproc"+path, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp := e.raw(identity, req)
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func channelNames(t *testing.T, e *env, as string) []string {
	t.Helper()
	r, err := e.client(as).ListChannelsWithResponse(context.Background())
	if err != nil || r.JSON200 == nil {
		t.Fatalf("ListChannels: %v %v", err, r)
	}
	var out []string
	for _, c := range *r.JSON200 {
		out = append(out, c.Name)
	}
	return out
}

func TestAdminResetNeedsAdmin(t *testing.T) {
	e := newEnv(t, envOptions{})
	e.identity("root", true)
	e.identity("pat", false)
	if _, err := e.client("root").CreateChannelWithResponse(context.Background(), &gen.CreateChannelParams{}, gen.CreateChannelJSONRequestBody{Name: "extra"}); err != nil {
		t.Fatal(err)
	}
	if st, body := e.post("pat", "/v1/admin/reset", nil); st != 403 || !strings.Contains(body, `"forbidden"`) {
		t.Fatalf("pat: %d %s", st, body)
	}
	if got := channelNames(t, e, "root"); len(got) != 1 || got[0] != "extra" {
		t.Fatalf("channels after refused reset = %v", got)
	}
	for i := 0; i < 2; i++ { // the same Idempotency-Key twice
		if st, body := e.post("root", "/v1/admin/reset", map[string]string{"Idempotency-Key": "k1"}); st != 204 {
			t.Fatalf("root #%d: %d %s", i, st, body)
		}
	}
	if got := channelNames(t, e, "root"); len(got) != 1 || got[0] != "general" {
		t.Fatalf("channels = %v, want [general]", got)
	}
}

func TestAdminResetScopes(t *testing.T) {
	setup := func() *env {
		e := newEnv(t, envOptions{})
		e.identity("root", true)
		e.identity("pat", false)
		e.keyFor("root")
		e.keyFor("pat")
		if _, _, err := (identities.Service{Core: e.core}).CreateEnrollmentToken(context.Background(), identities.TokenInput{}); err != nil {
			t.Fatal(err)
		}
		return e
	}
	for _, path := range []string{"/v1/admin/reset?scope=data", "/v1/admin/reset"} {
		e := setup()
		if st, body := e.post("root", path, nil); st != 204 {
			t.Fatalf("%s: %d %s", path, st, body)
		}
		if !e.hasIdentity("pat") || e.keyCount("pat") != 1 || e.tokenCount() != 1 {
			t.Fatalf("%s: data scope removed identities, keys or tokens", path)
		}
	}
	e := setup()
	if st, body := e.post("root", "/v1/admin/reset?scope=everything", nil); st != 204 {
		t.Fatalf("everything: %d %s", st, body)
	}
	if e.hasIdentity("pat") || e.keyCount("pat") != 0 {
		t.Fatal("everything left pat behind")
	}
	if !e.hasIdentity("root") || e.keyCount("root") != 1 || e.tokenCount() != 0 {
		t.Fatal("everything removed root or left tokens")
	}
	if st, _ := e.post("root", "/v1/admin/reset", nil); st != 204 {
		t.Fatalf("root's next request = %d, want 204", st)
	}
	// An unknown scope is refused by the validator and changes nothing.
	e = setup()
	st, body := e.post("root", "/v1/admin/reset?scope=all", nil)
	if st != 400 || !strings.Contains(body, `"validation"`) {
		t.Fatalf("scope=all: %d %s", st, body)
	}
	if !e.hasIdentity("pat") || e.tokenCount() != 1 {
		t.Fatal("a refused scope changed state")
	}
}

func TestResetCredentialWire(t *testing.T) {
	e := newEnv(t, envOptions{})
	e.identity("root", true)
	e.identity("pat", false)
	e.keyFor("pat")
	ctx := context.Background()
	if st, _ := e.post("pat", "/v1/admin/identities/pat/reset-credential", nil); st != 403 {
		t.Fatalf("as pat = %d, want 403", st)
	}
	if e.keyCount("pat") != 1 {
		t.Fatal("a refused reset removed the key")
	}
	r, err := e.client("root").ResetCredentialWithResponse(ctx, "pat", &gen.ResetCredentialParams{})
	if err != nil || r.JSON200 == nil || r.JSON200.Identity != "pat" || r.JSON200.KeysRemoved != 1 {
		t.Fatalf("first: %v %+v", err, r)
	}
	r, err = e.client("root").ResetCredentialWithResponse(ctx, "pat", &gen.ResetCredentialParams{})
	if err != nil || r.JSON200 == nil || r.JSON200.KeysRemoved != 0 {
		t.Fatalf("second: %v %+v", err, r)
	}
	if st, body := e.post("root", "/v1/admin/identities/ghost/reset-credential", nil); st != 403 || !strings.Contains(body, "not_registered") {
		t.Fatalf("ghost: %d %s", st, body)
	}
	// The same key replays the first count.
	e.keyFor("pat")
	k := "same"
	for i := 0; i < 2; i++ {
		r, err = e.client("root").ResetCredentialWithResponse(ctx, "pat", &gen.ResetCredentialParams{IdempotencyKey: &k})
		if err != nil || r.JSON200 == nil || r.JSON200.KeysRemoved != 1 {
			t.Fatalf("keyed #%d: %v %+v", i, err, r)
		}
	}
}

// TestResetCredentialKeepsMintedToken: after reset-credential the key is gone
// (no new assertion verifies) while the identity row stays, which is what
// keeps an already minted bearer valid until exp (Eric, 2026-10-21, item 4).
func TestResetCredentialKeepsMintedToken(t *testing.T) {
	e := newEnv(t, envOptions{})
	e.identity("root", true)
	e.identity("pat", false)
	svc := identities.Service{Core: e.core}
	ctx := context.Background()
	kid := e.keyFor("pat")
	if _, _, err := svc.PublicKey(ctx, "pat", kid); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ResetCredentialAs(ctx, "root", "pat", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.PublicKey(ctx, "pat", kid); bus.CodeOf(err) != bus.CodeNotFound {
		t.Fatalf("PublicKey after reset = %v, want not_found", err)
	}
	err := e.store.Read(ctx, func(ctx context.Context, tx store.Tx) error {
		_, err := e.core.RequireIdentity(ctx, tx, "pat")
		return err
	})
	if err != nil {
		t.Fatalf("RequireIdentity after reset: %v", err)
	}
}

// postBody is post with a JSON body (an empty body sends none).
func (e *env) postBody(identity, path, body string) (int, string, string) {
	e.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(http.MethodPost, "http://inproc"+path, rd)
	if err != nil {
		e.t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp := e.raw(identity, req)
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header.Get("Content-Type")
}

func TestCreateIdentityByEmailWire(t *testing.T) {
	e := newEnv(t, envOptions{})
	e.identity("root", true)
	const path = "/v1/admin/identities/by-email"
	st, body, ct := e.postBody("root", path, "")
	if st != 400 || !strings.HasPrefix(ct, "application/problem+json") {
		t.Fatalf("no body: %d %s %s", st, ct, body)
	}
	if st, body, _ = e.postBody("root", path, `{"email":"a@b.c","extra":1}`); st != 400 {
		t.Fatalf("unknown member: %d %s", st, body)
	}
	if e.hasIdentity("a@b.c") {
		t.Fatal("a refused request created the identity")
	}
	st, body, _ = e.postBody("root", path, `{"email":"A@b.c"}`)
	if st != 200 || !strings.Contains(body, `"identity":"a@b.c"`) || !strings.Contains(body, `"created":true`) {
		t.Fatalf("success: %d %s", st, body)
	}
	st, body, _ = e.postBody("root", path, `{"email":"a@b.c"}`)
	if st != 200 || !strings.Contains(body, `"created":false`) {
		t.Fatalf("again: %d %s", st, body)
	}
	e.identity("pat", false)
	if st, _, _ = e.postBody("pat", path, `{"email":"z@b.c"}`); st != 403 {
		t.Fatalf("as pat = %d, want 403", st)
	}
}
