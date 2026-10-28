package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core/signingkeys"
	"github.com/ericfitz/agentbus2/internal/keyprovider"
)

// countingProvider counts Next calls, so a test can tell that a replayed
// rotation never reached the provider.
type countingProvider struct {
	keyprovider.StoreProvider
	next atomic.Int32
}

func (p *countingProvider) Next(ctx context.Context) (keyprovider.Key, error) {
	p.next.Add(1)
	return p.StoreProvider.Next(ctx)
}

// keysEnv is an env with an issuer, an administrator "root", a non-administrator
// "pat" and a signing key service whose active key is two hours old (older than
// the one hour default cooldown), so the first rotation is allowed.
func keysEnv(t *testing.T) (*env, *countingProvider) {
	t.Helper()
	e := newEnv(t, envOptions{issuer: true, auth: InProcess{}})
	e.identity("root", true)
	e.identity("pat", false)
	p := &countingProvider{}
	svc := signingkeys.Service{Core: e.core, Provider: p, Settings: signingkeys.DefaultSettings()}
	e.opts.SigningKeys = svc
	if _, _, err := svc.EnsureActive(context.Background()); err != nil {
		t.Fatalf("EnsureActive: %v", err)
	}
	e.clock.Advance(2 * time.Hour)
	p.next.Store(0)
	return e, p
}

type wireKey struct {
	KID         string `json:"kid"`
	State       string `json:"state"`
	Provider    string `json:"provider"`
	ProviderRef string `json:"provider_ref"`
	Algorithm   string `json:"algorithm"`
	CreatedAt   int64  `json:"created_at"`
	RetiredAt   int64  `json:"retired_at"`
	RemoveAfter int64  `json:"remove_after"`
}

type wireRotation struct {
	Active   wireKey  `json:"active"`
	Previous *wireKey `json:"previous"`
	Revoked  bool     `json:"revoked"`
}

func (e *env) rotate(identity, query, idemKey string) (int, string) {
	e.t.Helper()
	req, err := http.NewRequest(http.MethodPost, "http://inproc/v1/admin/signing-keys/rotate"+query, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	resp := e.raw(identity, req)
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func (e *env) listKeys(identity string) (int, string, []wireKey) {
	e.t.Helper()
	st, body, _ := e.send(identity, http.MethodGet, "/v1/admin/signing-keys")
	var doc struct {
		Keys []wireKey `json:"keys"`
	}
	if st == 200 {
		if err := json.Unmarshal([]byte(body), &doc); err != nil {
			e.t.Fatalf("list body %q: %v", body, err)
		}
	}
	return st, body, doc.Keys
}

func TestListSigningKeysAdmin(t *testing.T) {
	e, _ := keysEnv(t)
	st, body, ks := e.listKeys("root")
	if st != 200 || len(ks) != 1 {
		t.Fatalf("list: %d %s", st, body)
	}
	k := ks[0]
	if k.State != "active" || k.Provider != "store" || k.Algorithm != "EdDSA" || k.KID == "" || k.RetiredAt != 0 || k.RemoveAfter != 0 || k.CreatedAt != fixedNowMs {
		t.Fatalf("active key = %+v", k)
	}
	if strings.Contains(strings.ToLower(body), "private") || strings.Contains(body, "BEGIN") {
		t.Fatalf("the body names a private key: %s", body)
	}
}

func TestSigningKeysForbiddenForAgent(t *testing.T) {
	e, p := keysEnv(t)
	st, body, _ := e.send("pat", http.MethodGet, "/v1/admin/signing-keys")
	if st != 403 || !strings.Contains(body, `"code":"forbidden"`) {
		t.Fatalf("list as agent: %d %s", st, body)
	}
	st, body = e.rotate("pat", "", "")
	if st != 403 || !strings.Contains(body, `"code":"forbidden"`) {
		t.Fatalf("rotate as agent: %d %s", st, body)
	}
	if n := p.next.Load(); n != 0 {
		t.Fatalf("a refused rotation called Provider.Next %d times", n)
	}
	if _, _, ks := e.listKeys("root"); len(ks) != 1 {
		t.Fatalf("a refused rotation changed the set: %+v", ks)
	}
}

func TestRotateSigningKeyAPI(t *testing.T) {
	e, _ := keysEnv(t)
	_, _, before := e.listKeys("root")
	st, body := e.rotate("root", "", "")
	if st != 200 {
		t.Fatalf("rotate: %d %s", st, body)
	}
	var r wireRotation
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatal(err)
	}
	if r.Revoked || r.Active.State != "active" || r.Active.KID == before[0].KID || r.Previous == nil || r.Previous.KID != before[0].KID || r.Previous.State != "retiring" {
		t.Fatalf("rotation = %s", body)
	}
	if r.Previous.RetiredAt != e.core.NowMs() || r.Previous.RemoveAfter != signingkeys.RemoveAfter(e.core.NowMs()) {
		t.Fatalf("previous retirement times = %+v", r.Previous)
	}
	if _, _, ks := e.listKeys("root"); len(ks) != 2 || ks[0].State != "active" || ks[0].KID != r.Active.KID || ks[1].State != "retiring" {
		t.Fatalf("set after rotate = %+v", ks)
	}

	// A second rotation inside the cooldown is a conflict that says so.
	st, body = e.rotate("root", "", "")
	if st != 409 || !strings.Contains(body, `"code":"conflict"`) || !strings.Contains(body, "cooldown") {
		t.Fatalf("second rotate: %d %s", st, body)
	}
	if _, _, ks := e.listKeys("root"); len(ks) != 2 {
		t.Fatalf("a refused rotation changed the set: %+v", ks)
	}

	// revoke_previous skips the cooldown and deletes the key it replaces.
	st, body = e.rotate("root", "?revoke_previous=true", "")
	if st != 200 {
		t.Fatalf("revoking rotate: %d %s", st, body)
	}
	var rv wireRotation
	if err := json.Unmarshal([]byte(body), &rv); err != nil {
		t.Fatal(err)
	}
	if !rv.Revoked || rv.Previous == nil || rv.Previous.KID != r.Active.KID {
		t.Fatalf("revoking rotation = %s", body)
	}
	_, _, ks := e.listKeys("root")
	if len(ks) != 2 || ks[0].KID != rv.Active.KID || ks[1].KID != r.Previous.KID {
		t.Fatalf("set after revoke = %+v", ks)
	}

	// The query parameter is a boolean.
	if st, body = e.rotate("root", "?revoke_previous=maybe", ""); st != 400 {
		t.Fatalf("revoke_previous=maybe: %d %s", st, body)
	}
}

// A set with no key yet answers the first rotation without a previous member.
func TestRotateSigningKeyFirstKey(t *testing.T) {
	e := newEnv(t, envOptions{issuer: true, auth: InProcess{}})
	e.identity("root", true)
	e.opts.SigningKeys = signingkeys.Service{Core: e.core, Provider: keyprovider.StoreProvider{}, Settings: signingkeys.DefaultSettings()}
	st, body := e.rotate("root", "", "")
	if st != 200 || strings.Contains(body, `"previous"`) || !strings.Contains(body, `"revoked":false`) {
		t.Fatalf("first rotate: %d %s", st, body)
	}
}

func TestRotateSigningKeyIdempotent(t *testing.T) {
	e, p := keysEnv(t)
	st, first := e.rotate("root", "", "k1")
	if st != 200 {
		t.Fatalf("first: %d %s", st, first)
	}
	if n := p.next.Load(); n != 1 {
		t.Fatalf("Next calls after the first = %d", n)
	}
	st, second := e.rotate("root", "", "k1")
	if st != 200 || second != first {
		t.Fatalf("replay: %d %s\nwant %s", st, second, first)
	}
	if n := p.next.Load(); n != 1 {
		t.Fatalf("a replay called Provider.Next (%d calls)", n)
	}
	if _, _, ks := e.listKeys("root"); len(ks) != 2 {
		t.Fatalf("a replay rotated again: %+v", ks)
	}
	// The same key with another payload is a conflict, again without Next.
	st, body := e.rotate("root", "?revoke_previous=true", "k1")
	if st != 409 || !strings.Contains(body, "different payload") {
		t.Fatalf("changed payload: %d %s", st, body)
	}
	if n := p.next.Load(); n != 1 {
		t.Fatalf("a payload conflict called Provider.Next (%d calls)", n)
	}
	if _, _, ks := e.listKeys("root"); len(ks) != 2 {
		t.Fatalf("a payload conflict changed the set: %+v", ks)
	}
	// A refused rotation stores no receipt: the retry with the same key runs.
	e2, _ := keysEnv(t)
	if st, body = e2.rotate("root", "", ""); st != 200 {
		t.Fatalf("setup: %d %s", st, body)
	}
	if st, _ = e2.rotate("root", "", "k2"); st != 409 {
		t.Fatalf("inside the cooldown: %d", st)
	}
	e2.clock.Advance(2 * time.Hour)
	if st, body = e2.rotate("root", "", "k2"); st != 200 {
		t.Fatalf("retry after a refusal: %d %s", st, body)
	}
}

func TestSigningKeysUnconfigured(t *testing.T) {
	e := newEnv(t, envOptions{})
	e.identity("root", true)
	if e.opts.SigningKeys != nil {
		t.Fatal("the default env sets SigningKeys")
	}
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/v1/admin/signing-keys"},
		{http.MethodPost, "/v1/admin/signing-keys/rotate"},
	} {
		st, body, ct := e.send("root", c.method, c.path)
		if st != 503 || !strings.HasPrefix(ct, "application/problem+json") || !strings.Contains(body, `"code":"unavailable"`) ||
			!strings.Contains(body, `"retryable":false`) || !strings.Contains(body, bus.SigningKeysNotConfigured) {
			t.Errorf("%s %s: %d %s %s", c.method, c.path, st, ct, body)
		}
	}
}
