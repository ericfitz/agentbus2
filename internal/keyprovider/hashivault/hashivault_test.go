package hashivault

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ericfitz/agentbus2/internal/auth/keys"
	"github.com/ericfitz/agentbus2/internal/auth/token"
	"github.com/ericfitz/agentbus2/internal/keyprovider"
)

const (
	secretToken = "hvs.s3cr3t-token-do-not-print"
	keyName     = "agentbus2"
)

// tkey is one transit key of the fake: a type and its versions (index 0 is
// version 1).
type tkey struct {
	typ      string
	versions []crypto.Signer
	noSign   bool   // supports_signing false
	badPEM   bool   // the latest version's public_key is not a key
	noLatest bool   // latest_version is missing from the answer
	rawPub   string // when set, the public_key of the latest version
}

// fakeVault is an httptest.NewServer speaking the Vault HTTP API for the
// transit key read and the sign endpoint, with a request log and failure
// switches.
type fakeVault struct {
	srv *httptest.Server

	mu      sync.Mutex
	mount   string
	token   string
	keys    map[string]*tkey
	reqs    []string // "METHOD path"
	signs   []map[string]any
	status  int // when non-zero every request answers it
	body    string
	hangOps map[string]bool   // "GET" or "PUT": never answers
	empty   bool              // the sign endpoint answers 204 with no body
	echo    bool              // failAll bodies echo the X-Vault-Token header
	other   *ecdsa.PrivateKey // when set, sign with this key instead
	freed   sync.Once
	release chan struct{}
}

func newFakeVault(t *testing.T) *fakeVault {
	t.Helper()
	f := &fakeVault{
		mount:   "transit",
		token:   secretToken,
		keys:    map[string]*tkey{},
		hangOps: map[string]bool{},
		release: make(chan struct{}),
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(func() {
		f.free()
		f.srv.Close()
	})
	t.Setenv("VAULT_ADDR", f.srv.URL)
	t.Setenv("VAULT_TOKEN", secretToken)
	for _, v := range []string{"BAO_ADDR", "BAO_TOKEN", "TRANSIT_SECRET_ENGINE_PATH", "VAULT_NAMESPACE", "VAULT_HEADERS", "VAULT_CACERT", "VAULT_CAPATH", "VAULT_CLIENT_CERT", "VAULT_CLIENT_KEY", "VAULT_SKIP_VERIFY", "VAULT_CLIENT_TIMEOUT", "VAULT_MAX_RETRIES", "VAULT_AGENT_ADDR", "VAULT_PROXY_ADDR", "VAULT_TLS_SERVER_NAME"} {
		t.Setenv(v, "")
	}
	t.Setenv("HOME", t.TempDir())
	return f
}

// free releases every hung request.
func (f *fakeVault) free() { f.freed.Do(func() { close(f.release) }) }

func (f *fakeVault) addP256(t *testing.T, name string) *ecdsa.PrivateKey {
	t.Helper()
	k := f.addVersion(t, name)
	return k
}

// addVersion appends a new P-256 version to name (creating an ecdsa-p256 key).
func (f *fakeVault) addVersion(t *testing.T, name string) *ecdsa.PrivateKey {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	k := f.keys[name]
	if k == nil {
		k = &tkey{typ: "ecdsa-p256"}
		f.keys[name] = k
	}
	k.versions = append(k.versions, priv)
	return priv
}

func (f *fakeVault) addOther(t *testing.T, name, typ string, priv crypto.Signer) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys[name] = &tkey{typ: typ, versions: []crypto.Signer{priv}}
}

func (f *fakeVault) edit(name string, fn func(*tkey)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f.keys[name])
}

func (f *fakeVault) failAll(status int, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status, f.body = status, body
}

func (f *fakeVault) hang(method string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hangOps[method] = true
}

func (f *fakeVault) count(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.reqs {
		if strings.HasPrefix(r, method+" ") {
			n++
		}
	}
	return n
}

func (f *fakeVault) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reqs)
}

func (f *fakeVault) lastSign() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.signs) == 0 {
		return nil
	}
	return f.signs[len(f.signs)-1]
}

func publicPEM(pub crypto.PublicKey) string {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		panic(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

func (f *fakeVault) reply(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

func (f *fakeVault) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.reqs = append(f.reqs, r.Method+" "+r.URL.Path)
	status, body := f.status, f.body
	if f.echo {
		body = `{"errors":["rejected token ` + r.Header.Get("X-Vault-Token") + `"]}`
	}
	hang := f.hangOps[r.Method]
	tok := f.token
	mount := f.mount
	f.mu.Unlock()
	if hang {
		select {
		case <-r.Context().Done():
		case <-f.release:
		}
		return
	}
	if status != 0 {
		f.reply(w, status, body)
		return
	}
	if r.Header.Get("X-Vault-Token") != tok {
		f.reply(w, 403, `{"errors":["permission denied"]}`)
		return
	}
	prefix := "/v1/" + mount + "/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		f.reply(w, 404, `{"errors":["no handler for route"]}`)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, prefix)
	switch {
	case r.Method == http.MethodGet && strings.HasPrefix(rest, "keys/"):
		f.readKey(w, strings.TrimPrefix(rest, "keys/"))
	case (r.Method == http.MethodPut || r.Method == http.MethodPost) && strings.HasPrefix(rest, "sign/"):
		f.sign(w, r, strings.TrimPrefix(rest, "sign/"))
	default:
		f.reply(w, 405, `{"errors":["unsupported"]}`)
	}
}

func (f *fakeVault) readKey(w http.ResponseWriter, name string) {
	f.mu.Lock()
	k := f.keys[name]
	f.mu.Unlock()
	if k == nil {
		f.reply(w, 404, `{"errors":[]}`)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	vers := map[string]any{}
	for i, s := range k.versions {
		entry := map[string]any{"creation_time": "2026-10-27T00:00:00Z"}
		switch pub := s.Public().(type) {
		case ed25519.PublicKey:
			entry["name"] = "ed25519"
			entry["public_key"] = base64.StdEncoding.EncodeToString(pub)
		default:
			entry["name"] = "P-256"
			entry["public_key"] = publicPEM(pub)
		}
		if k.badPEM && i == len(k.versions)-1 {
			entry["public_key"] = "not a key"
		}
		if k.rawPub != "" && i == len(k.versions)-1 {
			entry["public_key"] = k.rawPub
		}
		vers[strconv.Itoa(i+1)] = entry
	}
	data := map[string]any{"type": k.typ, "keys": vers, "name": name, "supports_signing": !k.noSign}
	if !k.noLatest {
		data["latest_version"] = len(k.versions)
	}
	out, _ := json.Marshal(map[string]any{"data": data})
	f.reply(w, 200, string(out))
}

func (f *fakeVault) sign(w http.ResponseWriter, r *http.Request, rest string) {
	name, hash, ok := strings.Cut(rest, "/")
	if !ok || hash != "sha2-256" {
		f.reply(w, 400, `{"errors":["unsupported hash"]}`)
		return
	}
	var in map[string]any
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		f.reply(w, 400, `{"errors":["bad body"]}`)
		return
	}
	f.mu.Lock()
	f.signs = append(f.signs, in)
	k := f.keys[name]
	empty := f.empty
	f.mu.Unlock()
	if empty {
		w.WriteHeader(204)
		return
	}
	if k == nil {
		f.reply(w, 404, `{"errors":["encryption key not found"]}`)
		return
	}
	ver, _ := strconv.Atoi(fmt.Sprint(in["key_version"]))
	if ver == 0 {
		ver = len(k.versions)
	}
	if ver < 1 || ver > len(k.versions) {
		f.reply(w, 400, `{"errors":["invalid key version"]}`)
		return
	}
	if in["prehashed"] != true {
		f.reply(w, 400, `{"errors":["this fake signs prehashed input only"]}`)
		return
	}
	digest, err := base64.StdEncoding.DecodeString(fmt.Sprint(in["input"]))
	if err != nil || len(digest) != 32 {
		f.reply(w, 400, `{"errors":["bad input"]}`)
		return
	}
	priv, ok := k.versions[ver-1].(*ecdsa.PrivateKey)
	f.mu.Lock()
	if f.other != nil {
		priv, ok = f.other, true
	}
	f.mu.Unlock()
	if !ok {
		f.reply(w, 400, `{"errors":["not an ecdsa key"]}`)
		return
	}
	sig, err := ecdsa.SignASN1(rand.Reader, priv, digest)
	if err != nil {
		f.reply(w, 500, `{"errors":["sign failed"]}`)
		return
	}
	out, _ := json.Marshal(map[string]any{"data": map[string]any{
		"signature":   fmt.Sprintf("vault:v%d:%s", ver, base64.StdEncoding.EncodeToString(sig)),
		"key_version": ver,
	}})
	f.reply(w, 200, string(out))
}

func (f *fakeVault) provider(t *testing.T, name string) keyprovider.Provider {
	t.Helper()
	p, err := New("hashivault://" + name)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestNameIsHashivault(t *testing.T) {
	newFakeVault(t)
	if got := newFakeVault(t).provider(t, keyName).Name(); got != "hashivault" {
		t.Fatalf("Name = %q", got)
	}
}

func TestNextResolvesAndPins(t *testing.T) {
	f := newFakeVault(t)
	v1 := f.addP256(t, keyName)
	p := f.provider(t, keyName)
	k1, err := p.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if k1.Ref != "hashivault://"+keyName+"/1" || k1.Algorithm != keys.ES256 {
		t.Fatalf("key %+v", k1)
	}
	if !v1.PublicKey.Equal(k1.Public) {
		t.Fatal("Next returned another key's public half")
	}
	// A second Next with nothing changed is the same key.
	again, err := p.Next(context.Background())
	if err != nil || again.KID != k1.KID || again.Ref != k1.Ref {
		t.Fatalf("second Next = %+v, %v", again, err)
	}
	// Adding a version (the operator's rotation in Vault) changes what Next
	// pins: the new latest version, a new kid.
	v2 := f.addVersion(t, keyName)
	k2, err := p.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if k2.Ref != "hashivault://"+keyName+"/2" || k2.KID == k1.KID || !v2.PublicKey.Equal(k2.Public) {
		t.Fatalf("after a new version: %+v", k2)
	}
	if f.count("PUT")+f.count("POST")+f.count("DELETE") != 0 {
		t.Fatal("Next wrote to Vault")
	}
}

func TestNextRefusesKeyType(t *testing.T) {
	f := newFakeVault(t)
	ed, edPriv, _ := ed25519.GenerateKey(rand.Reader)
	_ = ed
	f.addOther(t, "ed", "ed25519", edPriv)
	p384, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	f.addOther(t, "p384", "ecdsa-p384", p384)
	// A key that says it is ecdsa-p256 but whose public half is P-384 is
	// still refused by the shared rule.
	f.addOther(t, "liar", "ecdsa-p256", p384)
	for _, name := range []string{"ed", "p384", "liar"} {
		_, err := f.provider(t, name).Next(context.Background())
		if err == nil || !strings.Contains(err.Error(), "only ECDSA P-256 (ES256) is supported") {
			t.Errorf("%s: err = %v", name, err)
		}
		if errors.Is(err, keyprovider.ErrUnavailable) {
			t.Errorf("%s: a key type is not an outage", name)
		}
	}
}

func TestNextRefusesWhatIsNotUsable(t *testing.T) {
	f := newFakeVault(t)
	f.addP256(t, "nosign")
	f.edit("nosign", func(k *tkey) { k.noSign = true })
	f.addP256(t, "badpem")
	f.edit("badpem", func(k *tkey) { k.badPEM = true })
	f.addP256(t, "nolatest")
	f.edit("nolatest", func(k *tkey) { k.noLatest = true })
	f.addP256(t, "zeroed")
	f.edit("zeroed", func(k *tkey) { k.versions = nil })
	for _, name := range []string{"missing", "nosign", "badpem", "nolatest", "zeroed"} {
		_, err := f.provider(t, name).Next(context.Background())
		if err == nil {
			t.Errorf("%s: Next succeeded", name)
			continue
		}
		if errors.Is(err, keyprovider.ErrUnavailable) {
			t.Errorf("%s: not retryable, got %v", name, err)
		}
		if strings.Contains(err.Error(), "hashivault://") || strings.Contains(err.Error(), secretToken) {
			t.Errorf("%s: error quotes a URI or the token: %v", name, err)
		}
	}
}

func TestSignerIsLazy(t *testing.T) {
	f := newFakeVault(t)
	f.addP256(t, keyName)
	p := f.provider(t, keyName)
	if f.total() != 0 {
		t.Fatalf("New made %d requests", f.total())
	}
	k, err := p.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	before := f.total()
	sg, err := p.Signer(context.Background(), k)
	if err != nil || sg == nil {
		t.Fatalf("Signer = %v, %v", sg, err)
	}
	if f.total() != before {
		t.Fatalf("Signer made %d requests", f.total()-before)
	}
	if !sg.Public().(*ecdsa.PublicKey).Equal(k.Public) {
		t.Fatal("Public is not the row's key")
	}
	if f.total() != before {
		t.Fatal("Public made a request")
	}
}

type oneKey struct{ set token.KeySet }

func (o oneKey) LoadKeys(context.Context) (token.KeySet, error) { return o.set, nil }

func TestSignVerifiesAsJWS(t *testing.T) {
	f := newFakeVault(t)
	f.addP256(t, keyName)
	p := f.provider(t, keyName)
	ctx := context.Background()
	k, err := p.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sg, err := p.Signer(ctx, k)
	if err != nil {
		t.Fatal(err)
	}
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
	// Vault was asked to sign a digest of the pinned version.
	body := f.lastSign()
	if body["prehashed"] != true || fmt.Sprint(body["key_version"]) != "1" {
		t.Fatalf("sign request %v", body)
	}
}

func TestSignUsesThePinnedVersion(t *testing.T) {
	f := newFakeVault(t)
	v1 := f.addP256(t, keyName)
	p := f.provider(t, keyName)
	k1, err := p.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	f.addVersion(t, keyName) // Vault now has a version 2; k1 stays pinned to 1
	sg, err := p.Signer(context.Background(), k1)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("pinned"))
	sig, err := sg.SignContext(context.Background(), digest[:], crypto.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	if !ecdsa.VerifyASN1(&v1.PublicKey, digest[:], sig) {
		t.Fatal("the signature is not by version 1")
	}
	if fmt.Sprint(f.lastSign()["key_version"]) != "1" {
		t.Fatalf("sign request %v", f.lastSign())
	}
}

func TestSignerRefusesWhatItCannotSign(t *testing.T) {
	f := newFakeVault(t)
	f.addP256(t, keyName)
	p := f.provider(t, keyName)
	k, err := p.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ed, _, _ := ed25519.GenerateKey(rand.Reader)
	with := func(ref string) keyprovider.Key {
		return keyprovider.Key{KID: k.KID, Algorithm: k.Algorithm, Public: k.Public, Ref: ref}
	}
	bad := map[string]keyprovider.Key{
		"empty ref":        with(""),
		"other scheme":     with("awskms://x/1"),
		"openbao scheme":   with("openbao://" + keyName + "/1"),
		"no version":       with("hashivault://" + keyName),
		"version zero":     with("hashivault://" + keyName + "/0"),
		"version signed":   with("hashivault://" + keyName + "/-1"),
		"version text":     with("hashivault://" + keyName + "/latest"),
		"version overflow": with("hashivault://" + keyName + "/99999999999999999999999"),
		"extra part":       with("hashivault://" + keyName + "/1/2"),
		"query":            with("hashivault://" + keyName + "/1?x=y"),
		"user info":        with("hashivault://u:p@" + keyName + "/1"),
		"no public key":    {KID: k.KID, Algorithm: k.Algorithm, Ref: k.Ref},
		"ed25519 public":   {KID: k.KID, Algorithm: keys.EdDSA, Public: ed, Ref: k.Ref},
		"algorithm":        {KID: k.KID, Algorithm: keys.EdDSA, Public: k.Public, Ref: k.Ref},
	}
	for name, key := range bad {
		if sg, err := p.Signer(context.Background(), key); err == nil || sg != nil {
			t.Errorf("%s: Signer = %v, %v, want an error", name, sg, err)
		}
	}
	sg, err := p.Signer(context.Background(), k)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("x"))
	for name, c := range map[string]struct {
		d []byte
		o crypto.SignerOpts
	}{
		"nil opts":     {digest[:], nil},
		"sha512 opts":  {digest[:], crypto.SHA512},
		"short digest": {digest[:16], crypto.SHA256},
		"long digest":  {append(digest[:], 0), crypto.SHA256},
	} {
		if _, err := sg.SignContext(context.Background(), c.d, c.o); err == nil {
			t.Errorf("%s: signed", name)
		}
	}
	if f.count("PUT")+f.count("POST") != 0 {
		t.Fatal("a refused digest reached Vault")
	}
}

func TestSignRetriesAfterFailedLoad(t *testing.T) {
	f := newFakeVault(t)
	f.addP256(t, keyName)
	p := f.provider(t, keyName)
	k, err := p.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sg, err := p.Signer(context.Background(), k)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("x"))
	t.Setenv("VAULT_TOKEN", "")
	_, err = sg.SignContext(context.Background(), digest[:], crypto.SHA256)
	if err == nil || !strings.Contains(err.Error(), "VAULT_TOKEN") {
		t.Fatalf("no token: err = %v", err)
	}
	if errors.Is(err, keyprovider.ErrUnavailable) {
		t.Fatal("a missing token is not an outage")
	}
	t.Setenv("VAULT_TOKEN", secretToken)
	if _, err := sg.SignContext(context.Background(), digest[:], crypto.SHA256); err != nil {
		t.Fatalf("the next call must retry the load: %v", err)
	}
}

func TestSignHonorsContext(t *testing.T) {
	f := newFakeVault(t)
	f.addP256(t, keyName)
	p := f.provider(t, keyName)
	k, err := p.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sg, err := p.Signer(context.Background(), k)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("x"))

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := sg.SignContext(canceled, digest[:], crypto.SHA256); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled: err = %v", err)
	}
	if f.count("PUT")+f.count("POST") != 0 {
		t.Fatal("a canceled context reached Vault")
	}

	// Sigstore's signer ignores its context; a Vault that never answers still
	// ends at the deadline.
	f.hang("PUT")
	f.hang("POST")
	ctx, cancel2 := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel2()
	start := time.Now()
	_, err = sg.SignContext(ctx, digest[:], crypto.SHA256)
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, keyprovider.ErrUnavailable) {
		t.Fatalf("deadline: err = %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("took %v", time.Since(start))
	}

	// A cancel in flight is a cancel, not an outage.
	ctx3, cancel3 := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel3() }()
	_, err = sg.SignContext(ctx3, digest[:], crypto.SHA256)
	if !errors.Is(err, context.Canceled) || errors.Is(err, keyprovider.ErrUnavailable) {
		t.Fatalf("cancel in flight: err = %v", err)
	}
}

func TestNextHonorsContext(t *testing.T) {
	f := newFakeVault(t)
	f.addP256(t, keyName)
	f.hang("GET")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := f.provider(t, keyName).Next(ctx)
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, keyprovider.ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("took %v", time.Since(start))
	}
}

func TestHungSignsAreBounded(t *testing.T) {
	f := newFakeVault(t)
	f.addP256(t, keyName)
	p := f.provider(t, keyName)
	k, _ := p.Next(context.Background())
	sg, err := p.Signer(context.Background(), k)
	if err != nil {
		t.Fatal(err)
	}
	old := signSlots
	signSlots = make(chan struct{}, 2)
	t.Cleanup(func() { signSlots = old })
	f.hang("PUT")
	f.hang("POST")
	digest := sha256.Sum256([]byte("x"))
	for range 2 {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		_, err := sg.SignContext(ctx, digest[:], crypto.SHA256)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v", err)
		}
	}
	// Both slots now belong to requests Vault never answered: the next call
	// fails at once, retryably, without another request.
	before := f.total()
	_, err = sg.SignContext(context.Background(), digest[:], crypto.SHA256)
	if !errors.Is(err, keyprovider.ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
	if f.total() != before {
		t.Fatal("a request went out with no free slot")
	}
}

func TestServiceFailuresAreClassified(t *testing.T) {
	f := newFakeVault(t)
	f.addP256(t, keyName)
	p := f.provider(t, keyName)
	ctx := context.Background()
	k, err := p.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sg, err := p.Signer(ctx, k)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("x"))
	for _, c := range []struct {
		status      int
		body        string
		unavailable bool
	}{
		{429, `{"errors":["rate limit exceeded"]}`, true},
		{500, `{"errors":["internal error"]}`, true},
		{502, ``, true},
		{503, `{"errors":["Vault is sealed"]}`, true},
		{504, ``, true},
		{400, `{"errors":["bad request"]}`, false},
		{403, `{"errors":["permission denied"]}`, false},
		{404, `{"errors":["no handler"]}`, false},
	} {
		f.failAll(c.status, c.body)
		_, nerr := p.Next(ctx)
		_, serr := sg.SignContext(ctx, digest[:], crypto.SHA256)
		for op, err := range map[string]error{"Next": nerr, "Sign": serr} {
			if err == nil {
				t.Errorf("%d %s: no error", c.status, op)
				continue
			}
			if got := errors.Is(err, keyprovider.ErrUnavailable); got != c.unavailable {
				t.Errorf("%d %s: unavailable = %v, want %v (%v)", c.status, op, got, c.unavailable, err)
			}
			// A 404 on a read is a missing key (the client reports no secret,
			// not a status), so only the other answers carry their status.
			if c.status != 404 && !strings.Contains(err.Error(), strconv.Itoa(c.status)) {
				t.Errorf("%d %s: the status is not in %v", c.status, op, err)
			}
		}
	}
	f.failAll(0, "")
	if _, err := p.Next(ctx); err != nil {
		t.Fatalf("recovered Next: %v", err)
	}
}

func TestMissingKeyIsNotAnOutage(t *testing.T) {
	f := newFakeVault(t)
	_, err := f.provider(t, "nothere").Next(context.Background())
	if err == nil || errors.Is(err, keyprovider.ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

func TestUnreachableVaultIsUnavailable(t *testing.T) {
	f := newFakeVault(t)
	f.addP256(t, keyName)
	p := f.provider(t, keyName)
	k, err := p.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sg, err := p.Signer(context.Background(), k)
	if err != nil {
		t.Fatal(err)
	}
	f.srv.Close()
	if _, err := p.Next(context.Background()); !errors.Is(err, keyprovider.ErrUnavailable) {
		t.Fatalf("Next: %v", err)
	}
	digest := sha256.Sum256([]byte("x"))
	if _, err := sg.SignContext(context.Background(), digest[:], crypto.SHA256); !errors.Is(err, keyprovider.ErrUnavailable) {
		t.Fatalf("Sign: %v", err)
	}
}

// Ruling 3: VAULT_TOKEN is in no error, whatever fails.
func TestTokenNeverAppearsInErrors(t *testing.T) {
	f := newFakeVault(t)
	f.addP256(t, keyName)
	p := f.provider(t, keyName)
	ctx := context.Background()
	k, err := p.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(k.Ref, secretToken) {
		t.Fatal("the pinned ref holds the token")
	}
	sg, err := p.Signer(ctx, k)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("x"))
	var errs []error
	collect := func() {
		_, e1 := p.Next(ctx)
		_, e2 := sg.SignContext(ctx, digest[:], crypto.SHA256)
		errs = append(errs, e1, e2)
	}
	for _, st := range []int{403, 404, 429, 503} {
		f.failAll(st, `{"errors":["boom"]}`)
		collect()
	}
	f.failAll(0, "")
	f.mu.Lock()
	f.token = "another-token" // the server now refuses ours: a real 403
	f.mu.Unlock()
	collect()
	t.Setenv("VAULT_ADDR", "http://user:"+secretToken+"@127.0.0.1:1")
	collect()
	for i, e := range errs {
		if e == nil {
			t.Errorf("error %d is nil", i)
			continue
		}
		if strings.Contains(e.Error(), secretToken) {
			t.Errorf("error %d leaks the token: %v", i, e)
		}
	}
}

func TestEnvironment(t *testing.T) {
	f := newFakeVault(t)
	f.addP256(t, keyName)
	ctx := context.Background()
	p := f.provider(t, keyName)

	t.Run("missing address", func(t *testing.T) {
		t.Setenv("VAULT_ADDR", "")
		_, err := p.Next(ctx)
		if err == nil || !strings.Contains(err.Error(), "VAULT_ADDR") || errors.Is(err, keyprovider.ErrUnavailable) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("missing token", func(t *testing.T) {
		t.Setenv("VAULT_TOKEN", "")
		_, err := p.Next(ctx)
		if err == nil || !strings.Contains(err.Error(), "VAULT_TOKEN") || errors.Is(err, keyprovider.ErrUnavailable) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("bao names", func(t *testing.T) {
		t.Setenv("BAO_ADDR", f.srv.URL)
		t.Setenv("BAO_TOKEN", secretToken)
		t.Setenv("VAULT_ADDR", "")
		t.Setenv("VAULT_TOKEN", "")
		if _, err := p.Next(ctx); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("vault names win", func(t *testing.T) {
		t.Setenv("BAO_ADDR", "http://127.0.0.1:1")
		t.Setenv("BAO_TOKEN", "wrong")
		if _, err := p.Next(ctx); err != nil {
			t.Fatal(err)
		}
	})
	for _, addr := range []string{"ftp://x", "unix:///run/vault.sock", "http://u:p@127.0.0.1:1", "not a url", "http://"} {
		t.Run("refuses address "+addr, func(t *testing.T) {
			before := f.total()
			t.Setenv("VAULT_ADDR", addr)
			_, err := p.Next(ctx)
			if err == nil || errors.Is(err, keyprovider.ErrUnavailable) || strings.Contains(err.Error(), "p@") {
				t.Fatalf("err = %v", err)
			}
			if f.total() != before {
				t.Fatal("a request went out")
			}
		})
	}
	t.Run("custom mount", func(t *testing.T) {
		f.mu.Lock()
		f.mount = "team/keys"
		f.mu.Unlock()
		defer func() { f.mu.Lock(); f.mount = "transit"; f.mu.Unlock() }()
		if _, err := p.Next(ctx); err == nil {
			t.Fatal("the default mount found a key in another mount")
		}
		t.Setenv("TRANSIT_SECRET_ENGINE_PATH", "team/keys")
		k, err := p.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		sg, err := p.Signer(ctx, k)
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256([]byte("x"))
		if _, err := sg.SignContext(ctx, digest[:], crypto.SHA256); err != nil {
			t.Fatalf("sign through the custom mount: %v", err)
		}
	})
	for _, mount := range []string{"..", "a/../b", "/transit", "transit/", "a b", "t?x=y", "a//b", ".hidden"} {
		t.Run("refuses mount "+mount, func(t *testing.T) {
			before := f.total()
			t.Setenv("TRANSIT_SECRET_ENGINE_PATH", mount)
			_, err := p.Next(ctx)
			if err == nil || !strings.Contains(err.Error(), "TRANSIT_SECRET_ENGINE_PATH") {
				t.Fatalf("err = %v", err)
			}
			if f.total() != before {
				t.Fatal("a request went out")
			}
		})
	}
}

func TestNewRefusesURI(t *testing.T) {
	newFakeVault(t)
	secrets := []string{
		"hashivault://user:t0ken@name",
		"hashivault://name?token=t0ken",
		"hashivault://name#t0ken",
		"hashivault://t0ken@",
		"hashivault://na/me?x=t0ken",
	}
	for _, uri := range append([]string{
		"",
		"hashivault://",
		"hashivault:///name",
		"hashivault://a/b",
		"hashivault://name/",
		"hashivault://na me",
		"hashivault://-name",
		"hashivault://name-",
		"hashivault://name\n",
		"hashivault://na%00me",
		"hashivault://name/1",
		"openbao://name",
		"HASHIVAULT://name",
		"awskms://name",
		"https://name",
		"hashivault:/name",
		"hashivault:name",
	}, secrets...) {
		p, err := New(uri)
		if err == nil || p != nil {
			t.Errorf("New(%q) = %v, %v, want an error", uri, p, err)
			continue
		}
		if uri != "" && uri != scheme && strings.Contains(err.Error(), uri) {
			t.Errorf("New(%q): the error quotes the URI: %v", uri, err)
		}
		if strings.Contains(err.Error(), "t0ken") {
			t.Errorf("New(%q): the error leaks a secret: %v", uri, err)
		}
	}
	for _, uri := range []string{"hashivault://agentbus2", "hashivault://a", "hashivault://a.b-c_d", "hashivault://A1"} {
		p, err := New(uri)
		if err != nil || p == nil || p.Name() != "hashivault" {
			t.Errorf("New(%q) = %v, %v", uri, p, err)
		}
	}
}

func TestNewMakesNoRequest(t *testing.T) {
	f := newFakeVault(t)
	f.provider(t, keyName)
	if f.total() != 0 {
		t.Fatalf("%d requests", f.total())
	}
}

func TestErrorsDoNotQuoteTheURI(t *testing.T) {
	f := newFakeVault(t)
	_, err := f.provider(t, "missing").Next(context.Background())
	if err == nil {
		t.Fatal("a missing key resolved")
	}
	if strings.Contains(err.Error(), "hashivault://") {
		t.Fatalf("error quotes the URI: %v", err)
	}
}

func TestPinnedRefMustBeAcceptedBack(t *testing.T) {
	f := newFakeVault(t)
	f.addP256(t, "a.b-c_d")
	p := f.provider(t, "a.b-c_d")
	k, err := p.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Signer(context.Background(), k); err != nil {
		t.Fatalf("Signer refused the ref Next pinned: %v", err)
	}
}

// Next stays read-only, so a second rotation, two overlapping ones and a Next
// that failed halfway leave nothing behind (Definition of Done D).
func TestNextLeavesNoStateBehind(t *testing.T) {
	f := newFakeVault(t)
	f.addP256(t, keyName)
	p := f.provider(t, keyName)
	ctx := context.Background()

	// A Next that fails halfway (the read answers 503 once) changes nothing,
	// and the next one succeeds.
	f.failAll(503, `{"errors":["Vault is sealed"]}`)
	if _, err := p.Next(ctx); !errors.Is(err, keyprovider.ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
	f.failAll(0, "")

	// Overlapping Next calls agree.
	const n = 8
	got := make([]keyprovider.Key, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got[i], errs[i] = p.Next(ctx)
		}()
	}
	wg.Wait()
	for i := range n {
		if errs[i] != nil || got[i].KID != got[0].KID || got[i].Ref != got[0].Ref {
			t.Fatalf("Next %d = %+v, %v", i, got[i], errs[i])
		}
	}
	// A second Next after them is the same key again, and nothing was written.
	if again, err := p.Next(ctx); err != nil || again.KID != got[0].KID {
		t.Fatalf("again = %+v, %v", again, err)
	}
	for _, m := range []string{"PUT", "POST", "DELETE", "PATCH"} {
		if f.count(m) != 0 {
			t.Fatalf("Next sent %d %s requests", f.count(m), m)
		}
	}
}

// Sigstore dereferences the sign answer unchecked: an empty reply (here a 204)
// is no secret and no error to the API client, and sigstore panics. The signer
// turns the panic into an error rather than ending the server.
func TestSignSurvivesAnEmptyAnswer(t *testing.T) {
	f := newFakeVault(t)
	f.addP256(t, keyName)
	p := f.provider(t, keyName)
	k, err := p.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sg, err := p.Signer(context.Background(), k)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("x"))
	if _, err := sg.SignContext(context.Background(), digest[:], crypto.SHA256); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.empty = true
	f.mu.Unlock()
	_, err = sg.SignContext(context.Background(), digest[:], crypto.SHA256)
	if err == nil || errors.Is(err, keyprovider.ErrUnavailable) || !strings.Contains(err.Error(), "panic") {
		t.Fatalf("err = %v", err)
	}
	// The slot is given back.
	deadline := time.Now().Add(2 * time.Second)
	for len(signSlots) != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(signSlots) != 0 {
		t.Fatalf("%d sign slots still held", len(signSlots))
	}
}

func TestVaultTextIsRedactedAndBounded(t *testing.T) {
	f := newFakeVault(t)
	f.addP256(t, keyName)
	p := f.provider(t, keyName)
	ctx := context.Background()
	k, err := p.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sg, err := p.Signer(ctx, k)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("x"))
	// A server (or proxy) that echoes the token header into its error body.
	f.mu.Lock()
	f.echo = true
	f.mu.Unlock()
	for _, st := range []int{403, 500, 503} {
		f.failAll(st, "")
		_, e1 := p.Next(ctx)
		_, e2 := sg.SignContext(ctx, digest[:], crypto.SHA256)
		for _, e := range []error{e1, e2} {
			if e == nil || strings.Contains(e.Error(), secretToken) || !strings.Contains(e.Error(), "[redacted]") {
				t.Errorf("%d: err = %v", st, e)
			}
		}
	}
	// Long text is cut.
	f.mu.Lock()
	f.echo = false
	f.mu.Unlock()
	f.failAll(500, `{"errors":["`+strings.Repeat("x", 5000)+`"]}`)
	_, err = p.Next(ctx)
	if err == nil || len(err.Error()) > 600 {
		t.Fatalf("long text kept: %d bytes", len(fmt.Sprint(err)))
	}
}

func TestSignRefusesAnotherKeysSignature(t *testing.T) {
	f := newFakeVault(t)
	f.addP256(t, keyName)
	p := f.provider(t, keyName)
	ctx := context.Background()
	k, err := p.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sg, err := p.Signer(ctx, k)
	if err != nil {
		t.Fatal(err)
	}
	// A same-named key under another mount or namespace, or a recreated key,
	// answers 200 with a signature that does not verify under the row's key.
	other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	f.mu.Lock()
	f.other = other
	f.mu.Unlock()
	digest := sha256.Sum256([]byte("x"))
	_, err = sg.SignContext(ctx, digest[:], crypto.SHA256)
	if err == nil || errors.Is(err, keyprovider.ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
	for _, want := range []string{"mount", "namespace", "recreated"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
	for _, bad := range []string{"hashivault://", secretToken} {
		if strings.Contains(err.Error(), bad) {
			t.Errorf("error holds %q: %v", bad, err)
		}
	}
}

func TestClientSetupFailureHasNoFormatNoise(t *testing.T) {
	f := newFakeVault(t)
	f.addP256(t, keyName)
	p := f.provider(t, keyName)
	k, err := p.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sg, err := p.Signer(context.Background(), k)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("VAULT_SKIP_VERIFY", "maybe") // fails the Vault client's environment parse
	digest := sha256.Sum256([]byte("x"))
	_, e1 := sg.SignContext(context.Background(), digest[:], crypto.SHA256)
	_, e2 := p.Next(context.Background())
	for _, e := range []error{e1, e2} {
		if e == nil || strings.Contains(e.Error(), "%!") || errors.Is(e, keyprovider.ErrUnavailable) {
			t.Errorf("err = %v", e)
		}
	}
}

func TestHungFirstSignReadIsBounded(t *testing.T) {
	f := newFakeVault(t)
	f.addP256(t, keyName)
	p := f.provider(t, keyName)
	k, err := p.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sg, err := p.Signer(context.Background(), k)
	if err != nil {
		t.Fatal(err)
	}
	f.hang("GET") // sigstore's cold-cache key read, before it signs
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	digest := sha256.Sum256([]byte("x"))
	start := time.Now()
	_, err = sg.SignContext(ctx, digest[:], crypto.SHA256)
	if !errors.Is(err, keyprovider.ErrUnavailable) || time.Since(start) > 3*time.Second {
		t.Fatalf("err = %v after %v", err, time.Since(start))
	}
}

func TestSlotsDrainAfterRelease(t *testing.T) {
	f := newFakeVault(t)
	f.addP256(t, keyName)
	p := f.provider(t, keyName)
	k, _ := p.Next(context.Background())
	sg, err := p.Signer(context.Background(), k)
	if err != nil {
		t.Fatal(err)
	}
	old := signSlots
	signSlots = make(chan struct{}, 2)
	t.Cleanup(func() { signSlots = old })
	digest := sha256.Sum256([]byte("x"))
	if _, err := sg.SignContext(context.Background(), digest[:], crypto.SHA256); err != nil {
		t.Fatal(err) // warms sigstore's key cache
	}
	f.hang("PUT")
	f.hang("POST")
	for range 2 {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		_, _ = sg.SignContext(ctx, digest[:], crypto.SHA256)
		cancel()
	}
	if len(signSlots) != 2 {
		t.Fatalf("%d slots held, want 2", len(signSlots))
	}
	f.free()
	deadline := time.Now().Add(5 * time.Second)
	for len(signSlots) != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(signSlots) != 0 {
		t.Fatalf("%d slots never drained", len(signSlots))
	}
	f.mu.Lock()
	f.hangOps = map[string]bool{}
	f.mu.Unlock()
	if _, err := sg.SignContext(context.Background(), digest[:], crypto.SHA256); err != nil {
		t.Fatalf("sign after the drain: %v", err)
	}
}

func TestConcurrentSigns(t *testing.T) {
	f := newFakeVault(t)
	f.addP256(t, keyName)
	p := f.provider(t, keyName)
	k, _ := p.Next(context.Background())
	sg, err := p.Signer(context.Background(), k)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d := sha256.Sum256([]byte{byte(i)})
			sig, err := sg.SignContext(context.Background(), d[:], crypto.SHA256)
			if err == nil && !ecdsa.VerifyASN1(k.Public.(*ecdsa.PublicKey), d[:], sig) {
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

// New refuses an unusable environment at once, so the server reports a
// configuration error (exit 2) instead of failing at the first Next.
func TestNewRefusesUnusableEnvironment(t *testing.T) {
	f := newFakeVault(t)
	cases := []struct {
		name string
		set  map[string]string
		want string
	}{
		{"no address", map[string]string{"VAULT_ADDR": "", "BAO_ADDR": ""}, "VAULT_ADDR"},
		{"no token", map[string]string{"VAULT_TOKEN": "", "BAO_TOKEN": ""}, "VAULT_TOKEN"},
		{"bad address", map[string]string{"VAULT_ADDR": "ftp://x"}, "VAULT_ADDR must be"},
		{"address with user info", map[string]string{"VAULT_ADDR": "http://u:" + secretToken + "@127.0.0.1:1"}, "no user info"},
		{"bad mount", map[string]string{"TRANSIT_SECRET_ENGINE_PATH": "/x"}, "TRANSIT_SECRET_ENGINE_PATH"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for k, v := range c.set {
				t.Setenv(k, v)
			}
			p, err := New("hashivault://" + keyName)
			if err == nil || p != nil || !strings.Contains(err.Error(), c.want) || errors.Is(err, keyprovider.ErrUnavailable) || strings.Contains(err.Error(), secretToken) {
				t.Fatalf("New = %v, %v; want an error naming %q", p, err, c.want)
			}
		})
	}
	if f.total() != 0 {
		t.Fatalf("%d requests", f.total())
	}
}

// TestTransportAndSetupTextIsRedactedAndBounded: the texts of a transport
// failure, the fallback and the signer build go through the same redaction and
// cap as Vault's own text.
func TestTransportAndSetupTextIsRedactedAndBounded(t *testing.T) {
	t.Setenv("VAULT_TOKEN", secretToken)
	long := strings.Repeat("é", 400)
	for name, err := range map[string]error{
		"url error": &url.Error{Op: "Get", URL: "http://x", Err: errors.New("boom " + secretToken + " " + long)},
		"fallback":  errors.New("boom " + secretToken + " " + long),
		"url net":   &url.Error{Op: "Get", URL: "http://x", Err: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New(secretToken + long)}},
	} {
		text := classify("read", err).Error()
		if strings.Contains(text, secretToken) {
			t.Errorf("%s: leaks the token: %.80s", name, text)
		}
		if len(text) > maxVaultText+200 {
			t.Errorf("%s: %d bytes of error text", name, len(text))
		}
		if !utf8.ValidString(text) {
			t.Errorf("%s: not valid UTF-8", name)
		}
	}
	// A missing file under the transport error is a refusal, not an outage.
	pe := &url.Error{Op: "Get", URL: "http://x", Err: &fs.PathError{Op: "open", Path: "/x", Err: syscall.ENOENT}}
	if errors.Is(classify("read", pe), keyprovider.ErrUnavailable) {
		t.Error("a wrapped fs.PathError is unavailable")
	}
}

// TestSignerBuildTextIsBounded: the text of a failed signer build is capped.
func TestSignerBuildTextIsBounded(t *testing.T) {
	f := newFakeVault(t)
	f.addP256(t, keyName)
	p := f.provider(t, keyName)
	k, err := p.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sg, err := p.Signer(context.Background(), k)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("VAULT_SKIP_VERIFY", strings.Repeat("x", 3000)) // the client's environment parse echoes the value
	digest := sha256.Sum256([]byte("x"))
	_, e := sg.SignContext(context.Background(), digest[:], crypto.SHA256)
	if e == nil || len(e.Error()) > maxVaultText+300 {
		t.Fatalf("err = %.200v (%d bytes)", e, len(fmt.Sprint(e)))
	}
}
