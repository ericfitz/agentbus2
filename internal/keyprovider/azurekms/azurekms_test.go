package azurekms

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"

	"github.com/ericfitz/agentbus2/internal/auth/keys"
	"github.com/ericfitz/agentbus2/internal/auth/token"
	"github.com/ericfitz/agentbus2/internal/keyprovider"
)

const (
	vaultHost = "kv-test.vault.azure.net"
	keyName   = "agentbus-sign"
	keyURI    = "azurekms://" + vaultHost + "/" + keyName
	ver1      = "0123456789abcdef0123456789abcdef"
	ver2      = "fedcba98765432100123456789abcdef"
	bearer    = "eyJ-SECRET-BEARER-TOKEN"
)

func setenv(k, v string) {
	if err := os.Setenv(k, v); err != nil {
		panic(err)
	}
}

// TestMain keeps every test off the machine's Azure state and off the
// network: no CLI session, no managed identity endpoint, no credential file.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "azurekms-test-")
	if err != nil {
		panic(err)
	}
	for _, k := range []string{
		"AZURE_CLIENT_ID", "AZURE_TENANT_ID", "AZURE_CLIENT_SECRET", "AZURE_CLIENT_CERTIFICATE_PATH",
		"AZURE_CLIENT_CERTIFICATE_PASSWORD", "AZURE_FEDERATED_TOKEN_FILE", "AZURE_USERNAME", "AZURE_PASSWORD",
		"AZURE_ENVIRONMENT", "AZURE_AUTHORITY_HOST", "IDENTITY_ENDPOINT", "IDENTITY_HEADER", "IMDS_ENDPOINT",
		"MSI_ENDPOINT", "MSI_SECRET", "IDENTITY_SERVER_THUMBPRINT",
	} {
		_ = os.Unsetenv(k)
	}
	setenv("AZURE_TOKEN_CREDENTIALS", "EnvironmentCredential")
	setenv("AZURE_CONFIG_DIR", dir)
	setenv("HOME", dir)
	setenv("APPDATA", dir)
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func p256() *ecdsa.PrivateKey {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	return k
}

func sha(s string) []byte {
	d := sha256.Sum256([]byte(s))
	return d[:]
}

// fver is one version of the fake's key. jwk, when set, replaces the key
// material the fake answers with.
type fver struct {
	id      string
	priv    *ecdsa.PrivateKey
	ops     []string
	enabled *bool
	nbf     *int64
	exp     *int64
	jwk     func(kid string) map[string]any
	kid     string // overrides the answered kid
}

type fakeVault struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	versions []*fver // oldest first; the last is the newest
	paths    []string
	unauth   int
	authz    []string
	status   int    // a non-zero status is answered for every authenticated request
	code     string // the error code in that answer
	hanging  bool
	release  chan struct{}
	freed    sync.Once
	other    *fver // answered instead of the requested version

	cred     *fakeCred
	signs    []signCall
	signWith *ecdsa.PrivateKey
	signHook func(ctx context.Context, ref string, digest []byte) ([]byte, error)
}

type signCall struct {
	ref    string
	digest []byte
}

func newFakeVault(t *testing.T) *fakeVault {
	t.Helper()
	f := &fakeVault{t: t, release: make(chan struct{}), cred: &fakeCred{token: bearer}}
	f.srv = httptest.NewUnstartedServer(f)
	f.srv.Config.ErrorLog = log.New(io.Discard, "", 0) // handshake noise at shutdown
	f.srv.StartTLS()
	t.Cleanup(func() {
		f.free()
		f.srv.Close()
	})
	return f
}

func (f *fakeVault) free() { f.freed.Do(func() { close(f.release) }) }

func (f *fakeVault) add(priv *ecdsa.PrivateKey, id string) *fver {
	v := &fver{id: id, priv: priv, ops: []string{"sign", "verify"}}
	f.mu.Lock()
	f.versions = append(f.versions, v)
	f.mu.Unlock()
	return v
}

func (f *fakeVault) addKey() *ecdsa.PrivateKey {
	k := p256()
	f.add(k, ver1)
	return k
}

func (f *fakeVault) failWith(status int, code string) {
	f.mu.Lock()
	f.status, f.code = status, code
	f.mu.Unlock()
}

func (f *fakeVault) hang() {
	f.mu.Lock()
	f.hanging = true
	f.mu.Unlock()
}

// total counts every request, the unauthenticated challenge probes included.
func (f *fakeVault) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.paths) + f.unauth
}

func ecJWK(kid string, pub *ecdsa.PublicKey, crv string, ops []string) map[string]any {
	pb, err := pub.Bytes() // 0x04 || X || Y, each coordinate full width
	if err != nil {
		panic(err)
	}
	size := (len(pb) - 1) / 2
	enc := base64.RawURLEncoding
	return map[string]any{
		"kid": kid, "kty": "EC", "crv": crv, "key_ops": ops,
		"x": enc.EncodeToString(pb[1 : 1+size]),
		"y": enc.EncodeToString(pb[1+size:]),
	}
}

func (f *fakeVault) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	hanging := f.hanging
	f.mu.Unlock()
	if hanging {
		select {
		case <-r.Context().Done():
		case <-f.release:
		}
		return
	}
	if r.Header.Get("Authorization") == "" {
		f.mu.Lock()
		f.unauth++
		f.mu.Unlock()
		w.Header().Set("WWW-Authenticate", `Bearer authorization="https://login.microsoftonline.com/tenant-1" resource="https://vault.azure.net"`)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	f.mu.Lock()
	f.paths = append(f.paths, r.Method+" "+r.URL.Path)
	f.authz = append(f.authz, r.Header.Get("Authorization"))
	status, code := f.status, f.code
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if status != 0 {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": code, "message": "no. " + code}})
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if r.Method != http.MethodGet || len(parts) < 2 || len(parts) > 3 || parts[0] != "keys" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var v *fver
	switch {
	case parts[1] != keyName || len(f.versions) == 0:
	case len(parts) == 2:
		v = f.versions[len(f.versions)-1]
	default:
		for _, c := range f.versions {
			if c.id == parts[2] {
				v = c
			}
		}
	}
	if v == nil {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "KeyNotFound", "message": "not found"}})
		return
	}
	if f.other != nil {
		v = f.other
	}
	kid := "https://" + vaultHost + "/keys/" + keyName + "/" + v.id
	if v.kid != "" {
		kid = v.kid
	}
	jwk := ecJWK(kid, &v.priv.PublicKey, "P-256", v.ops)
	if v.jwk != nil {
		jwk = v.jwk(kid)
	}
	attrs := map[string]any{"enabled": true}
	if v.enabled != nil {
		attrs["enabled"] = *v.enabled
	}
	if v.nbf != nil {
		attrs["nbf"] = *v.nbf
	}
	if v.exp != nil {
		attrs["exp"] = *v.exp
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"key": jwk, "attributes": attrs})
}

type fakeCred struct {
	mu    sync.Mutex
	token string
	err   error
	calls int
}

func (c *fakeCred) GetToken(_ context.Context, _ policy.TokenRequestOptions) (azcore.AccessToken, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.err != nil {
		return azcore.AccessToken{}, c.err
	}
	return azcore.AccessToken{Token: c.token, ExpiresOn: time.Now().Add(time.Hour)}, nil
}

func (c *fakeCred) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func (f *fakeVault) setCredErr(err error) {
	f.cred.mu.Lock()
	f.cred.err = err
	f.cred.mu.Unlock()
}

// sign is the seam the tests install: it signs with the key of the version the
// reference names, as Key Vault would.
func (f *fakeVault) sign(ctx context.Context, ref string, digest []byte) ([]byte, error) {
	f.mu.Lock()
	f.signs = append(f.signs, signCall{ref, append([]byte(nil), digest...)})
	hook, with := f.signHook, f.signWith
	f.mu.Unlock()
	if hook != nil {
		return hook(ctx, ref, digest)
	}
	key := with
	if key == nil {
		f.mu.Lock()
		for _, v := range f.versions {
			if strings.HasSuffix(ref, "/"+v.id) {
				key = v.priv
			}
		}
		f.mu.Unlock()
	}
	if key == nil {
		return nil, errors.New("fake: no such version")
	}
	return ecdsa.SignASN1(rand.Reader, key, digest)
}

func (f *fakeVault) conn() connector {
	return connector{
		endpoint:  f.srv.URL + "/",
		transport: f.srv.Client(),
		cred:      func(context.Context) (azcore.TokenCredential, error) { return f.cred, nil },
		sign:      f.sign,
		now:       time.Now,
		// The fake is not a vault host, so its challenge resource cannot match.
		skipChallengeCheck: true,
	}
}

func (f *fakeVault) provider(t *testing.T, uri string) keyprovider.Provider {
	t.Helper()
	p, err := newProvider(uri, f.conn())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func mustNext(t *testing.T, p keyprovider.Provider) keyprovider.Key {
	t.Helper()
	k, err := p.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func mustSigner(t *testing.T, p keyprovider.Provider, k keyprovider.Key) keyprovider.Signer {
	t.Helper()
	s, err := p.Signer(context.Background(), k)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func refusal(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || errors.Is(err, keyprovider.ErrUnavailable) || !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %v, want a non-retryable refusal containing %q", err, want)
	}
}

func TestNameIsAzurekms(t *testing.T) {
	p, err := New(keyURI)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != "azurekms" || Name != "azurekms" {
		t.Fatalf("name %q", p.Name())
	}
}

func TestNextResolvesAndPins(t *testing.T) {
	f := newFakeVault(t)
	v1 := f.addKey()
	p := f.provider(t, keyURI)
	k := mustNext(t, p)
	if k.Ref != "azurekms://"+vaultHost+"/"+keyName+"/"+ver1 || k.Algorithm != keys.ES256 {
		t.Fatalf("key %+v", k)
	}
	if !v1.PublicKey.Equal(k.Public) {
		t.Fatal("the public key is not the key the vault answered")
	}
	want, err := keys.KID(&v1.PublicKey)
	if err != nil || k.KID != want {
		t.Fatalf("kid %q, want %q (%v)", k.KID, want, err)
	}
	// Repointing the vault (a new version) changes the pin.
	f.add(p256(), ver2)
	if k2 := mustNext(t, p); k2.Ref != "azurekms://"+vaultHost+"/"+keyName+"/"+ver2 || k2.KID == k.KID {
		t.Fatalf("after a new version: %+v", k2)
	}
	// A URI with a version pins that version although a newer one exists.
	pv := f.provider(t, keyURI+"/"+ver1)
	if k3 := mustNext(t, pv); k3.Ref != k.Ref || k3.KID != k.KID {
		t.Fatalf("pinned URI: %+v", k3)
	}
	f.mu.Lock()
	last := f.paths[len(f.paths)-1]
	f.mu.Unlock()
	if last != "GET /keys/"+keyName+"/"+ver1 {
		t.Fatalf("last request %q", last)
	}
}

func TestNextRefusesKeyType(t *testing.T) {
	p384, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(kid string) map[string]any{
		"P-384": func(kid string) map[string]any {
			return ecJWK(kid, &p384.PublicKey, "P-384", []string{"sign"})
		},
		"P-256K": func(kid string) map[string]any {
			return ecJWK(kid, &p384.PublicKey, "P-256K", []string{"sign"})
		},
		"RSA": func(kid string) map[string]any {
			return map[string]any{"kid": kid, "kty": "RSA", "key_ops": []string{"sign"}, "n": "AQAB", "e": "AQAB"}
		},
		"OKP": func(kid string) map[string]any {
			pub, _, _ := ed25519.GenerateKey(rand.Reader)
			return map[string]any{"kid": kid, "kty": "OKP", "crv": "Ed25519", "key_ops": []string{"sign"}, "x": base64.RawURLEncoding.EncodeToString(pub)}
		},
		"oct": func(kid string) map[string]any {
			return map[string]any{"kid": kid, "kty": "oct", "key_ops": []string{"sign"}}
		},
		"P-384 curve on a P-256 point": func(kid string) map[string]any {
			j := ecJWK(kid, &p384.PublicKey, "P-256", []string{"sign"})
			return j // 48-byte coordinates under a P-256 label
		},
		"no kty": func(kid string) map[string]any {
			return map[string]any{"kid": kid, "key_ops": []string{"sign"}}
		},
	}
	for name, jwk := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFakeVault(t)
			f.add(p256(), ver1).jwk = jwk
			_, err := f.provider(t, keyURI).Next(context.Background())
			refusal(t, err, "P-256")
		})
	}
}

func TestNextRefusesWhatIsNotUsable(t *testing.T) {
	no, yes := false, true
	past, future := time.Now().Add(-time.Hour).Unix(), time.Now().Add(time.Hour).Unix()
	_ = yes
	cases := map[string]struct {
		mod  func(v *fver)
		want string
	}{
		"no sign operation": {func(v *fver) { v.ops = []string{"verify"} }, "sign"},
		"no operations":     {func(v *fver) { v.ops = nil }, "sign"},
		"disabled":          {func(v *fver) { v.enabled = &no }, "disabled"},
		"expired":           {func(v *fver) { v.exp = &past }, "expired"},
		"not yet valid":     {func(v *fver) { v.nbf = &future }, "not valid yet"},
		"another key's kid": {func(v *fver) { v.kid = "https://" + vaultHost + "/keys/other/" + ver1 }, "another key"},
		"another vault":     {func(v *fver) { v.kid = "https://evil.vault.azure.net/keys/" + keyName + "/" + ver1 }, "another key"},
		"kid with a port":   {func(v *fver) { v.kid = "https://" + vaultHost + ":8443/keys/" + keyName + "/" + ver1 }, "another key"},
		"kid with no version": {func(v *fver) {
			v.kid = "https://" + vaultHost + "/keys/" + keyName
		}, "another key"},
		"kid with a bad version": {func(v *fver) {
			v.kid = "https://" + vaultHost + "/keys/" + keyName + "/LATEST"
		}, "another key"},
		"kid not https": {func(v *fver) { v.kid = "http://" + vaultHost + "/keys/" + keyName + "/" + ver1 }, "another key"},
		"point off the curve": {func(v *fver) {
			v.jwk = func(kid string) map[string]any {
				j := ecJWK(kid, &v.priv.PublicKey, "P-256", []string{"sign"})
				j["y"] = base64.RawURLEncoding.EncodeToString(make([]byte, 32))
				return j
			}
		}, "not a point"},
		"no x": {func(v *fver) {
			v.jwk = func(kid string) map[string]any {
				j := ecJWK(kid, &v.priv.PublicKey, "P-256", []string{"sign"})
				delete(j, "x")
				return j
			}
		}, "not a point"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFakeVault(t)
			c.mod(f.add(p256(), ver1))
			_, err := f.provider(t, keyURI).Next(context.Background())
			refusal(t, err, c.want)
			if strings.Contains(err.Error(), keyURI) {
				t.Fatalf("err quotes the URI: %v", err)
			}
		})
	}
	t.Run("the version asked for", func(t *testing.T) {
		f := newFakeVault(t)
		f.add(p256(), ver1)
		f.other = &fver{id: ver2, priv: p256(), ops: []string{"sign"}}
		_, err := f.provider(t, keyURI+"/"+ver1).Next(context.Background())
		refusal(t, err, "another key version")
	})
	t.Run("no such key", func(t *testing.T) {
		f := newFakeVault(t)
		_, err := f.provider(t, keyURI).Next(context.Background())
		refusal(t, err, "404")
	})
}

func TestSignerIsLazy(t *testing.T) {
	f := newFakeVault(t)
	f.addKey()
	p := f.provider(t, keyURI)
	k := mustNext(t, p)
	before, creds := f.total(), f.cred.count()
	sg := mustSigner(t, p, k)
	if f.total() != before || f.cred.count() != creds || len(f.signs) != 0 {
		t.Fatalf("Signer made requests: %d -> %d, credentials %d -> %d", before, f.total(), creds, f.cred.count())
	}
	if !sg.Public().(*ecdsa.PublicKey).Equal(k.Public) {
		t.Fatal("Public differs")
	}
	// New makes none either.
	g := newFakeVault(t)
	if _, err := newProvider(keyURI, g.conn()); err != nil || g.total() != 0 || g.cred.count() != 0 {
		t.Fatalf("New: %v, %d requests, %d credential calls", err, g.total(), g.cred.count())
	}
}

type oneKey struct{ set token.KeySet }

func (o oneKey) LoadKeys(context.Context) (token.KeySet, error) { return o.set, nil }

func TestSignVerifiesAsJWS(t *testing.T) {
	f := newFakeVault(t)
	f.addKey()
	p := f.provider(t, keyURI)
	k := mustNext(t, p)
	sg := mustSigner(t, p, k)
	ctx := context.Background()
	tk := token.Key{KID: k.KID, Algorithm: k.Algorithm, Public: k.Public}
	src := oneKey{token.KeySet{Active: tk, Signer: sg, Keys: []token.Key{tk}}}
	now := time.Now()
	iss, err := token.NewIssuer(ctx, "https://issuer.example", src, time.Hour, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	raw, _, err := iss.Mint(ctx, token.MintInput{Identity: "agent-1", Kind: token.KindAgent})
	if err != nil {
		t.Fatal(err)
	}
	if id, err := iss.Verify(ctx, raw, now); err != nil || id != "agent-1" {
		t.Fatalf("Verify = %q, %v", id, err)
	}
}

func TestSignThroughSeam(t *testing.T) {
	f := newFakeVault(t)
	f.addKey()
	p := f.provider(t, keyURI)
	k := mustNext(t, p)
	sg := mustSigner(t, p, k)
	d := sha("seam")
	sig, err := sg.SignContext(context.Background(), d, crypto.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	if !ecdsa.VerifyASN1(k.Public.(*ecdsa.PublicKey), d, sig) {
		t.Fatal("DER signature does not verify")
	}
	if len(f.signs) != 1 || f.signs[0].ref != k.Ref || string(f.signs[0].digest) != string(d) {
		t.Fatalf("seam calls %+v, want the pinned ref %q and the digest", f.signs, k.Ref)
	}
	// A raw r||s answer (what Key Vault itself returns) is refused: the
	// issuer wants DER.
	f.mu.Lock()
	f.signHook = func(context.Context, string, []byte) ([]byte, error) { return make([]byte, 64), nil }
	f.mu.Unlock()
	_, err = sg.SignContext(context.Background(), d, crypto.SHA256)
	refusal(t, err, "does not verify")
	raw := make([]byte, 64)
	f.mu.Lock()
	f.signHook = func(context.Context, string, []byte) ([]byte, error) {
		der, _ := ecdsa.SignASN1(rand.Reader, f.versions[0].priv, d)
		_ = raw
		return der, nil
	}
	f.mu.Unlock()
	if _, err := sg.SignContext(context.Background(), d, crypto.SHA256); err != nil {
		t.Fatalf("DER refused: %v", err)
	}
}

func TestSignRefusesAnotherKeysSignature(t *testing.T) {
	f := newFakeVault(t)
	f.addKey()
	p := f.provider(t, keyURI)
	sg := mustSigner(t, p, mustNext(t, p))
	f.mu.Lock()
	f.signWith = p256()
	f.mu.Unlock()
	_, err := sg.SignContext(context.Background(), sha("x"), crypto.SHA256)
	refusal(t, err, "does not verify")
	if !strings.Contains(err.Error(), "replaced") {
		t.Fatalf("the message names no likely cause: %v", err)
	}
}

func TestSignerRefusesWhatItCannotSign(t *testing.T) {
	f := newFakeVault(t)
	f.addKey()
	p := f.provider(t, keyURI)
	k := mustNext(t, p)
	ed, _, _ := ed25519.GenerateKey(rand.Reader)
	bad := map[string]keyprovider.Key{
		"algorithm":  {KID: "k", Algorithm: keys.EdDSA, Public: k.Public, Ref: k.Ref},
		"public key": {KID: "k", Algorithm: keys.ES256, Public: ed, Ref: k.Ref},
		"no version": {KID: "k", Algorithm: keys.ES256, Public: k.Public, Ref: keyURI},
		"bad ref":    {KID: "k", Algorithm: keys.ES256, Public: k.Public, Ref: "azurekms://u:p@" + vaultHost + "/" + keyName + "/" + ver1},
		"other":      {KID: "k", Algorithm: keys.ES256, Public: k.Public, Ref: "awskms://x/" + keyName},
		"empty":      {KID: "k", Algorithm: keys.ES256, Public: k.Public},
	}
	for name, key := range bad {
		if _, err := p.Signer(context.Background(), key); err == nil {
			t.Errorf("%s: accepted", name)
		} else if strings.Contains(err.Error(), "p@") {
			t.Errorf("%s: err quotes the URI: %v", name, err)
		}
	}
	sg := mustSigner(t, p, k)
	if _, err := sg.SignContext(context.Background(), sha("x"), crypto.SHA512); err == nil {
		t.Error("SHA-512 accepted")
	}
	if _, err := sg.SignContext(context.Background(), sha("x"), nil); err == nil {
		t.Error("nil options accepted")
	}
	if _, err := sg.SignContext(context.Background(), []byte("short"), crypto.SHA256); err == nil {
		t.Error("a 5-byte digest accepted")
	}
	if len(f.signs) != 0 {
		t.Fatalf("seam called %d times", len(f.signs))
	}
}

func TestSignHonorsContext(t *testing.T) {
	f := newFakeVault(t)
	f.addKey()
	p := f.provider(t, keyURI)
	sg := mustSigner(t, p, mustNext(t, p))
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	f.mu.Lock()
	f.signHook = func(context.Context, string, []byte) ([]byte, error) { <-stop; return nil, nil } // ignores ctx
	f.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := sg.SignContext(ctx, sha("x"), crypto.SHA256)
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, keyprovider.ErrUnavailable) || time.Since(start) > 2*time.Second {
		t.Fatalf("err = %v after %v", err, time.Since(start))
	}
	cctx, ccancel := context.WithCancel(context.Background())
	ccancel()
	if _, err := sg.SignContext(cctx, sha("x"), crypto.SHA256); !errors.Is(err, context.Canceled) || errors.Is(err, keyprovider.ErrUnavailable) {
		t.Fatalf("canceled: err = %v", err)
	}
	// The context reaches the seam.
	got := make(chan context.Context, 1)
	f.mu.Lock()
	f.signHook = func(c context.Context, _ string, d []byte) ([]byte, error) {
		got <- c
		return ecdsa.SignASN1(rand.Reader, f.versions[0].priv, d)
	}
	f.mu.Unlock()
	type key struct{}
	vctx := context.WithValue(context.Background(), key{}, "v")
	if _, err := sg.SignContext(vctx, sha("x"), crypto.SHA256); err != nil {
		t.Fatal(err)
	}
	if c := <-got; c.Value(key{}) != "v" {
		t.Fatal("the seam did not receive the caller's context")
	}
}

func waitSlots(t *testing.T, want int) {
	t.Helper()
	for range 200 {
		if len(signSlots) == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%d sign slots in use, want %d", len(signSlots), want)
}

func TestHungSignsAreBounded(t *testing.T) {
	f := newFakeVault(t)
	f.addKey()
	old := signSlots
	signSlots = make(chan struct{}, 2)
	t.Cleanup(func() { signSlots = old })
	p := f.provider(t, keyURI)
	sg := mustSigner(t, p, mustNext(t, p))
	stop := make(chan struct{})
	f.mu.Lock()
	f.signHook = func(context.Context, string, []byte) ([]byte, error) { <-stop; return nil, errors.New("late") }
	f.mu.Unlock()
	for range 2 {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		_, _ = sg.SignContext(ctx, sha("x"), crypto.SHA256)
		cancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := sg.SignContext(ctx, sha("x"), crypto.SHA256); !errors.Is(err, keyprovider.ErrUnavailable) || !strings.Contains(err.Error(), "too many") {
		t.Fatalf("err = %v", err)
	}
	close(stop)
	waitSlots(t, 0)
	f.mu.Lock()
	f.signHook = nil
	f.mu.Unlock()
	if _, err := sg.SignContext(context.Background(), sha("x"), crypto.SHA256); err != nil {
		t.Fatalf("sign after the drain: %v", err)
	}
}

func TestSignSurvivesAPanic(t *testing.T) {
	f := newFakeVault(t)
	f.addKey()
	p := f.provider(t, keyURI)
	sg := mustSigner(t, p, mustNext(t, p))
	f.mu.Lock()
	f.signHook = func(context.Context, string, []byte) ([]byte, error) {
		var kty *string
		return []byte(*kty), nil // the nil dereference sigstore makes on a key with no kty
	}
	f.mu.Unlock()
	_, err := sg.SignContext(context.Background(), sha("x"), crypto.SHA256)
	refusal(t, err, "panic")
	waitSlots(t, 0)
}

func TestConcurrentSigns(t *testing.T) {
	f := newFakeVault(t)
	f.addKey()
	p := f.provider(t, keyURI)
	k := mustNext(t, p)
	sg := mustSigner(t, p, k)
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d := sha(strconv.Itoa(i))
			sig, err := sg.SignContext(context.Background(), d, crypto.SHA256)
			if err == nil && !ecdsa.VerifyASN1(k.Public.(*ecdsa.PublicKey), d, sig) {
				err = errors.New("bad signature")
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
}

func TestNextHonorsContext(t *testing.T) {
	for name, uri := range map[string]string{"latest": keyURI, "pinned": keyURI + "/" + ver1} {
		t.Run(name, func(t *testing.T) {
			f := newFakeVault(t)
			f.addKey()
			p := f.provider(t, uri)
			f.hang()
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			start := time.Now()
			_, err := p.Next(ctx)
			if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, keyprovider.ErrUnavailable) || time.Since(start) > 3*time.Second {
				t.Fatalf("err = %v after %v", err, time.Since(start))
			}
			cctx, ccancel := context.WithCancel(context.Background())
			ccancel()
			if _, err := p.Next(cctx); !errors.Is(err, context.Canceled) || errors.Is(err, keyprovider.ErrUnavailable) {
				t.Fatalf("canceled: err = %v", err)
			}
		})
	}
}

func TestServiceFailuresAreClassified(t *testing.T) {
	retryable := map[int]string{429: "Throttled", 500: "InternalError", 502: "BadGateway", 503: "ServiceUnavailable", 504: "Timeout"}
	refused := map[int]string{400: "BadParameter", 401: "Unauthorized", 403: "Forbidden", 404: "KeyNotFound", 409: "Conflict"}
	for _, uri := range []string{keyURI, keyURI + "/" + ver1} {
		for st, code := range retryable {
			f := newFakeVault(t)
			f.addKey()
			f.failWith(st, code)
			_, err := f.provider(t, uri).Next(context.Background())
			if !errors.Is(err, keyprovider.ErrUnavailable) || !strings.Contains(err.Error(), strconv.Itoa(st)) {
				t.Errorf("%d: err = %v, want retryable", st, err)
			}
		}
		for st, code := range refused {
			f := newFakeVault(t)
			f.addKey()
			f.failWith(st, code)
			_, err := f.provider(t, uri).Next(context.Background())
			if err == nil || errors.Is(err, keyprovider.ErrUnavailable) || !strings.Contains(err.Error(), strconv.Itoa(st)) {
				t.Errorf("%d: err = %v, want a refusal", st, err)
			}
			if err != nil && strings.Contains(err.Error(), keyURI) {
				t.Errorf("%d: err quotes the URI: %v", st, err)
			}
		}
	}
}

func TestUnreachableEndpointIsUnavailable(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close() // nothing listens here now
	f := newFakeVault(t)
	c := f.conn()
	c.endpoint = "https://" + addr + "/"
	p, err := newProvider(keyURI, c)
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Next(context.Background())
	if !errors.Is(err, keyprovider.ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

func TestCredentialFailures(t *testing.T) {
	resp := func(code int) error {
		return &azidentity.AuthenticationFailedError{RawResponse: &http.Response{StatusCode: code}}
	}
	cases := []struct {
		name      string
		err       error
		retryable bool
	}{
		{"endpoint 503", resp(503), true},
		{"endpoint 429", resp(429), true},
		{"endpoint 500", resp(500), true},
		{"bad secret 401", resp(401), false},
		{"unknown tenant 400", resp(400), false},
		{"nothing configured", azidentity.NewCredentialUnavailableError("no credential"), false},
		{"dial failure", &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}, true},
		{"dns failure", &net.DNSError{Err: "no such host", Name: "login.example"}, true},
		{"deadline", context.DeadlineExceeded, true},
		{"timeout error", timeoutErr{}, true},
		{"plain failure", errors.New("some failure"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeVault(t)
			f.addKey()
			f.setCredErr(c.err)
			_, err := f.provider(t, keyURI).Next(context.Background())
			if err == nil || errors.Is(err, keyprovider.ErrUnavailable) != c.retryable {
				t.Fatalf("err = %v, retryable want %v", err, c.retryable)
			}
			if strings.Contains(err.Error(), keyURI) {
				t.Fatalf("err quotes the URI: %v", err)
			}
		})
	}
	t.Run("the credential cannot be built", func(t *testing.T) {
		f := newFakeVault(t)
		c := f.conn()
		c.cred = func(context.Context) (azcore.TokenCredential, error) { return nil, errors.New("bad configuration") }
		p, _ := newProvider(keyURI, c)
		_, err := p.Next(context.Background())
		refusal(t, err, "bad configuration")
		if f.total() != 0 {
			t.Fatal("a request was made without a credential")
		}
	})
	t.Run("the default chain refuses a bad AZURE_TOKEN_CREDENTIALS before any request", func(t *testing.T) {
		t.Setenv("AZURE_TOKEN_CREDENTIALS", "bogus")
		p, err := New(keyURI)
		if err != nil {
			t.Fatal(err)
		}
		_, err = p.Next(context.Background())
		refusal(t, err, "AZURE_TOKEN_CREDENTIALS")
	})
}

type timeoutErr struct{}

func (timeoutErr) Error() string { return "i/o timeout" }
func (timeoutErr) Timeout() bool { return true }

func TestNewRefusesURI(t *testing.T) {
	good := []string{
		keyURI,
		keyURI + "/" + ver1,
		"azurekms://a-b.vault.azure.net/Key-1",
		"azurekms://hsm1.managedhsm.azure.net/k",
		"azurekms://gov1.vault.usgovcloudapi.net/k",
		"azurekms://cn1.vault.azure.cn/k",
	}
	for _, u := range good {
		if _, err := New(u); err != nil {
			t.Errorf("%s refused: %v", u, err)
		}
	}
	bad := []string{
		"",
		"azurekms://",
		"azurekms://" + vaultHost,
		"azurekms://" + vaultHost + "/",
		"azurekms://us3r:p4ss@" + vaultHost + "/" + keyName,
		"azurekms://us3r@" + vaultHost + "/" + keyName,
		"azurekms://" + vaultHost + ":8443/" + keyName,
		"azurekms://" + vaultHost + ":443/" + keyName,
		"azurekms://localhost/" + keyName,
		"azurekms://127.0.0.1/" + keyName,
		"azurekms://169.254.169.254/" + keyName,
		"azurekms://evil.example.com/" + keyName,
		"azurekms://kv.vault.azure.net.evil.example.com/" + keyName,
		"azurekms://evilvault.azure.net/" + keyName,
		"azurekms://.vault.azure.net/" + keyName,
		"azurekms://ab.vault.azure.net/" + keyName, // vault names are 3 to 24 characters
		"azurekms://" + strings.Repeat("a", 25) + ".vault.azure.net/" + keyName,
		"azurekms://-kv.vault.azure.net/" + keyName,
		"azurekms://sub.kv.vault.azure.net/" + keyName,
		"azurekms://" + vaultHost + "/" + keyName + "/",
		"azurekms://" + vaultHost + "/" + keyName + "/" + ver1 + "/extra",
		"azurekms://" + vaultHost + "/" + keyName + "/latest",
		"azurekms://" + vaultHost + "/" + keyName + "/" + strings.ToUpper(ver1),
		"azurekms://" + vaultHost + "/" + keyName + "/" + ver1[:31],
		"azurekms://" + vaultHost + "/" + keyName + "/" + ver1 + "0",
		"azurekms://" + vaultHost + "/" + keyName + "?v=1",
		"azurekms://" + vaultHost + "/" + keyName + "#f",
		"azurekms://" + vaultHost + "/..",
		"azurekms://" + vaultHost + "/a%2fb",
		"azurekms://" + vaultHost + "/a b",
		"azurekms://" + vaultHost + "/" + strings.Repeat("k", 128),
		"azurekms://" + vaultHost + "/_key",
		"awskms://" + vaultHost + "/" + keyName,
		" azurekms://" + vaultHost + "/" + keyName,
		"AZUREKMS://" + vaultHost + "/" + keyName,
		"azurekms://" + vaultHost + "/" + keyName + "\n",
	}
	for _, u := range bad {
		p, err := New(u)
		if err == nil {
			t.Errorf("%q accepted", u)
			continue
		}
		if p != nil {
			t.Errorf("%q: a refusal returned a non-nil provider", u)
		}
		for _, secret := range []string{"us3r", "p4ss", "evil", "169.254", "8443"} {
			if strings.Contains(u, secret) && strings.Contains(err.Error(), secret) {
				t.Errorf("%q: error quotes %q: %v", u, secret, err)
			}
		}
		if len(u) > len(scheme)+1 && strings.Contains(err.Error(), u) {
			t.Errorf("%q: error quotes the URI", u)
		}
	}
}

func TestStatusTextIsBoundedAndRedacted(t *testing.T) {
	t.Run("long multibyte error code", func(t *testing.T) {
		f := newFakeVault(t)
		f.addKey()
		f.failWith(403, strings.Repeat("é", 3000))
		_, err := f.provider(t, keyURI).Next(context.Background())
		if err == nil || !utf8.ValidString(err.Error()) || len(err.Error()) > 800 {
			t.Fatalf("err = %v", err)
		}
		if n := strings.Count(err.Error(), "é"); n > 128 || n < 100 {
			t.Fatalf("%d runes of provider text kept", n)
		}
	})
	t.Run("the access token in a vault answer", func(t *testing.T) {
		f := newFakeVault(t)
		f.addKey()
		f.failWith(403, "denied for "+bearer)
		_, err := f.provider(t, keyURI).Next(context.Background())
		if err == nil || strings.Contains(err.Error(), bearer) || !strings.Contains(err.Error(), "[redacted]") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("the credential's secrets in its error", func(t *testing.T) {
		t.Setenv("AZURE_CLIENT_SECRET", "s3cr3t-value")
		t.Setenv("AZURE_CLIENT_CERTIFICATE_PASSWORD", "c3rt-pass")
		f := newFakeVault(t)
		f.addKey()
		f.setCredErr(errors.New("bad s3cr3t-value and c3rt-pass " + strings.Repeat("x", 1000)))
		_, err := f.provider(t, keyURI).Next(context.Background())
		if err == nil || strings.Contains(err.Error(), "s3cr3t-value") || strings.Contains(err.Error(), "c3rt-pass") || len(err.Error()) > 700 {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("classify", func(t *testing.T) {
		const secret = "tok-SECRET"
		err := classify("reading", fmt.Errorf("odd %s %s", secret, strings.Repeat("x", 1000)), secret)
		if strings.Contains(err.Error(), secret) || len(err.Error()) > 600 {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestErrorsDoNotQuoteTheURI(t *testing.T) {
	f := newFakeVault(t)
	f.addKey()
	for _, st := range []int{401, 403, 404, 429, 503} {
		f.failWith(st, "x")
		_, err := f.provider(t, keyURI+"/"+ver1).Next(context.Background())
		if err == nil || strings.Contains(err.Error(), "azurekms://") || strings.Contains(err.Error(), "https://") {
			t.Fatalf("%d: err = %v", st, err)
		}
	}
}

func TestNextLeavesNoStateBehind(t *testing.T) {
	f := newFakeVault(t)
	f.addKey()
	p := f.provider(t, keyURI)
	k1 := mustNext(t, p)
	k2 := mustNext(t, p) // a second run
	if k1.KID != k2.KID || k1.Ref != k2.Ref {
		t.Fatalf("second run: %+v vs %+v", k1, k2)
	}
	var wg sync.WaitGroup // overlapping runs
	results := make(chan keyprovider.Key, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			k, err := p.Next(context.Background())
			if err != nil {
				t.Error(err)
			}
			results <- k
		}()
	}
	wg.Wait()
	close(results)
	for k := range results {
		if k.KID != k1.KID {
			t.Errorf("overlapping run returned %q", k.KID)
		}
	}
	// A half-failed run: the vault fails, then recovers.
	f.failWith(503, "ServiceUnavailable")
	if _, err := p.Next(context.Background()); !errors.Is(err, keyprovider.ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
	f.failWith(0, "")
	if k3 := mustNext(t, p); k3.KID != k1.KID {
		t.Fatalf("after the failure: %+v", k3)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, path := range f.paths {
		if !strings.HasPrefix(path, "GET ") {
			t.Fatalf("Next made a non-read request: %q", path)
		}
	}
	if len(f.versions) != 1 || len(f.signs) != 0 {
		t.Fatalf("Next changed the vault: %d versions, %d signs", len(f.versions), len(f.signs))
	}
	for _, a := range f.authz {
		if !strings.HasPrefix(a, "Bearer ") {
			t.Fatalf("authorization %q", a)
		}
	}
}

func TestCloudFollowsAZUREENVIRONMENT(t *testing.T) {
	for env, want := range map[string]string{
		"":                       "https://login.microsoftonline.com/",
		"AZURECLOUD":             "https://login.microsoftonline.com/",
		"AZUREPUBLICCLOUD":       "https://login.microsoftonline.com/",
		"AZUREUSGOVERNMENT":      "https://login.microsoftonline.us/",
		"AZUREUSGOVERNMENTCLOUD": "https://login.microsoftonline.us/",
		"AZURECHINACLOUD":        "https://login.chinacloudapi.cn/",
		"something-else":         "https://login.microsoftonline.com/",
	} {
		if got := cloudFor(env).ActiveDirectoryAuthorityHost; got != want {
			t.Errorf("%q: authority %q, want %q", env, got, want)
		}
	}
}
