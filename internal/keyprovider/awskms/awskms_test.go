package awskms

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"

	"github.com/ericfitz/agentbus2/internal/auth/keys"
	"github.com/ericfitz/agentbus2/internal/auth/token"
	"github.com/ericfitz/agentbus2/internal/keyprovider"
)

const (
	account  = "111122223333"
	arnFmt   = "arn:aws:kms:us-east-1:" + account + ":key/"
	uuid1    = "1234abcd-12ab-34cd-56ef-1234567890ab"
	uuid2    = "2234abcd-12ab-34cd-56ef-1234567890ab"
	aliasURI = "/alias/agentbus2"
)

// kmsKey is one key of the fake service.
type kmsKey struct {
	id    string
	priv  crypto.Signer
	spec  string // KeySpec
	state string // KeyState, "" means Enabled
	usage string // KeyUsage, "" means SIGN_VERIFY
}

// fakeKMS is an httptest.NewTLSServer speaking AWS JSON 1.1 for DescribeKey,
// GetPublicKey and Sign, with aliases, a request counter and failure switches.
type fakeKMS struct {
	srv *httptest.Server

	mu      sync.Mutex
	keys    map[string]*kmsKey // by key id
	aliases map[string]string  // "alias/NAME" -> key id
	calls   map[string]int     // operation -> requests
	status  int                // when non-zero, every request answers it
	errType string             // the X-Amzn-ErrorType of that answer
	errMsg  string             // the message of that answer, "injected" when empty
	hangOps map[string]bool    // operations that never answer

	release chan struct{}
}

func newFakeKMS(t *testing.T) *fakeKMS {
	t.Helper()
	f := &fakeKMS{
		keys:    map[string]*kmsKey{},
		aliases: map[string]string{},
		calls:   map[string]int{},
		hangOps: map[string]bool{},
		release: make(chan struct{}),
	}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(f.handle))
	t.Cleanup(func() {
		close(f.release)
		f.srv.Close()
	})
	return f
}

func (f *fakeKMS) host() string { return strings.TrimPrefix(f.srv.URL, "https://") }

func (f *fakeKMS) addP256(t *testing.T, id string) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.keys[id] = &kmsKey{id: id, priv: k, spec: "ECC_NIST_P256"}
	f.mu.Unlock()
	return k
}

func (f *fakeKMS) add(id, spec string, priv crypto.Signer, state string) {
	f.mu.Lock()
	f.keys[id] = &kmsKey{id: id, priv: priv, spec: spec, state: state}
	f.mu.Unlock()
}

func (f *fakeKMS) point(alias, id string) {
	f.mu.Lock()
	f.aliases[alias] = id
	f.mu.Unlock()
}

func (f *fakeKMS) count(op string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[op]
}

func (f *fakeKMS) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		n += c
	}
	return n
}

func (f *fakeKMS) failAll(status int, errType string) {
	f.mu.Lock()
	f.status, f.errType = status, errType
	f.mu.Unlock()
}

func (f *fakeKMS) hang(op string) {
	f.mu.Lock()
	f.hangOps[op] = true
	f.mu.Unlock()
}

func (f *fakeKMS) lookup(ref string) *kmsKey {
	if i := strings.LastIndex(ref, ":alias/"); i >= 0 {
		ref = ref[i+1:]
	}
	if id, ok := f.aliases[ref]; ok {
		ref = id
	}
	ref = strings.TrimPrefix(ref, arnFmt)
	return f.keys[ref]
}

func writeErr(w http.ResponseWriter, status int, typ, msg string) {
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	w.Header().Set("X-Amzn-ErrorType", typ)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"__type": typ, "message": msg})
}

func (f *fakeKMS) handle(w http.ResponseWriter, r *http.Request) {
	op := strings.TrimPrefix(r.Header.Get("X-Amz-Target"), "TrentService.")
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.calls[op]++
	status, errType, hang, msg := f.status, f.errType, f.hangOps[op], f.errMsg
	f.mu.Unlock()
	if status != 0 {
		if msg == "" {
			msg = "injected"
		}
		writeErr(w, status, errType, msg)
		return
	}
	if hang {
		select {
		case <-r.Context().Done():
		case <-f.release:
		}
		return
	}
	var in struct {
		KeyID            string `json:"KeyId"`
		Message          []byte
		MessageType      string
		SigningAlgorithm string
	}
	if err := json.Unmarshal(body, &in); err != nil {
		writeErr(w, 400, "SerializationException", err.Error())
		return
	}
	f.mu.Lock()
	k := f.lookup(in.KeyID)
	f.mu.Unlock()
	if k == nil {
		writeErr(w, 400, "NotFoundException", "Key not found")
		return
	}
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	switch op {
	case "DescribeKey":
		state := k.state
		if state == "" {
			state = "Enabled"
		}
		usage := k.usage
		if usage == "" {
			usage = "SIGN_VERIFY"
		}
		algs := []string{"ECDSA_SHA_256"}
		_ = json.NewEncoder(w).Encode(map[string]any{"KeyMetadata": map[string]any{
			"Arn": arnFmt + k.id, "KeyId": k.id, "KeySpec": k.spec, "KeyUsage": usage,
			"KeyState": state, "SigningAlgorithms": algs,
		}})
	case "GetPublicKey":
		der, err := x509.MarshalPKIXPublicKey(k.priv.Public())
		if err != nil {
			writeErr(w, 500, "KMSInternalException", err.Error())
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"KeyId": arnFmt + k.id, "PublicKey": der, "KeySpec": k.spec, "KeyUsage": "SIGN_VERIFY",
			"SigningAlgorithms": []string{"ECDSA_SHA_256"},
		})
	case "Sign":
		if in.MessageType != "DIGEST" || in.SigningAlgorithm != "ECDSA_SHA_256" {
			writeErr(w, 400, "ValidationException", "message type "+in.MessageType+" algorithm "+in.SigningAlgorithm)
			return
		}
		sig, err := k.priv.Sign(rand.Reader, in.Message, crypto.SHA256)
		if err != nil {
			writeErr(w, 400, "ValidationException", err.Error())
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"KeyId": arnFmt + k.id, "Signature": sig, "SigningAlgorithm": "ECDSA_SHA_256"})
	default:
		writeErr(w, 400, "UnknownOperationException", op)
	}
}

// opts points the SDK at the fake: its TLS client, static credentials, one
// attempt per request.
func (f *fakeKMS) opts(t *testing.T) []func(*config.LoadOptions) error {
	t.Helper()
	t.Setenv("AWS_CONFIG_FILE", "/nonexistent")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", "/nonexistent")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	return []func(*config.LoadOptions) error{
		config.WithHTTPClient(f.srv.Client()),
		config.WithRegion("us-east-1"),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("AKIDEXAMPLE", "secret", "")),
		config.WithRetryMaxAttempts(1),
	}
}

func (f *fakeKMS) provider(t *testing.T, path string) keyprovider.Provider {
	t.Helper()
	p, err := newProvider("awskms://"+f.host()+path, f.opts(t)...)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestNameIsAwskms(t *testing.T) {
	f := newFakeKMS(t)
	if got := f.provider(t, aliasURI).Name(); got != "awskms" {
		t.Fatalf("Name = %q", got)
	}
}

func TestNextResolvesAndPins(t *testing.T) {
	f := newFakeKMS(t)
	k1 := f.addP256(t, uuid1)
	k2 := f.addP256(t, uuid2)
	f.point("alias/agentbus2", uuid1)
	p := f.provider(t, aliasURI)
	ctx := context.Background()

	a, err := p.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := "awskms://" + f.host() + "/" + arnFmt + uuid1; a.Ref != want {
		t.Fatalf("Ref = %q, want %q", a.Ref, want)
	}
	if kid, _ := keys.KID(k1.Public()); a.KID != kid || a.Algorithm != keys.ES256 || a.PrivatePEM != nil {
		t.Fatalf("key %+v", a)
	}
	if again, err := p.Next(ctx); err != nil || again.Ref != a.Ref {
		t.Fatalf("second Next = %+v, %v", again, err)
	}

	// Repointing the alias changes what the next rotation adopts; the old ref
	// still names the old key.
	f.point("alias/agentbus2", uuid2)
	b, err := p.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if kid, _ := keys.KID(k2.Public()); b.KID != kid || b.Ref != "awskms://"+f.host()+"/"+arnFmt+uuid2 {
		t.Fatalf("after repoint: %+v", b)
	}

	// A key id and a key ARN resolve to the same pinned ref.
	for _, path := range []string{"/" + uuid1, "/" + arnFmt + uuid1} {
		c, err := f.provider(t, path).Next(ctx)
		if err != nil || c.Ref != a.Ref {
			t.Errorf("%s: %+v, %v; want ref %q", path, c, err, a.Ref)
		}
	}
}

func TestNextRefusesKeyType(t *testing.T) {
	f := newFakeKMS(t)
	_, ed, _ := ed25519.GenerateKey(rand.Reader)
	p384, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	f.add(uuid1, "ECC_NIST_EDWARDS25519", ed, "")
	f.add(uuid2, "ECC_NIST_P384", p384, "")
	for id, want := range map[string]string{
		uuid1: "external signing key is Ed25519; only ECDSA P-256 (ES256) is supported",
		uuid2: "external signing key is ECDSA P-384; only ECDSA P-256 (ES256) is supported",
	} {
		_, err := f.provider(t, "/"+id).Next(context.Background())
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want text %q", id, err, want)
		}
		if errors.Is(err, keyprovider.ErrUnavailable) {
			t.Errorf("%s: a refused key type must not be retryable: %v", id, err)
		}
	}
}

func TestNextRefusesDisabledKey(t *testing.T) {
	f := newFakeKMS(t)
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	f.add(uuid1, "ECC_NIST_P256", k, "Disabled")
	_, err := f.provider(t, "/"+uuid1).Next(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Disabled") || errors.Is(err, keyprovider.ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

func TestSignerIsLazy(t *testing.T) {
	f := newFakeKMS(t)
	f.addP256(t, uuid1)
	f.point("alias/agentbus2", uuid1)
	p := f.provider(t, aliasURI)
	k, err := p.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	before := f.total()
	if before == 0 {
		t.Fatal("Next made no request")
	}
	sg, err := p.Signer(context.Background(), k)
	if err != nil {
		t.Fatal(err)
	}
	if sg.Public() == nil {
		t.Fatal("no public key")
	}
	if f.total() != before {
		t.Fatalf("New/Signer made %d requests", f.total()-before)
	}
	// A provider that was only constructed and asked for a signer makes none.
	f2 := newFakeKMS(t)
	f2.addP256(t, uuid1)
	p2 := f2.provider(t, "/"+uuid1)
	if _, err := p2.Signer(context.Background(), k); err != nil {
		t.Fatal(err)
	}
	if f2.total() != 0 {
		t.Fatalf("%d requests before the first sign", f2.total())
	}
}

func TestSignVerifiesAsJWS(t *testing.T) {
	f := newFakeKMS(t)
	f.addP256(t, uuid1)
	f.point("alias/agentbus2", uuid1)
	p := f.provider(t, aliasURI)
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
	// Sign sends a digest, never a message; the request asked for ECDSA_SHA_256.
	if f.count("Sign") != 1 {
		t.Fatalf("%d Sign requests", f.count("Sign"))
	}
}

type oneKey struct{ set token.KeySet }

func (o oneKey) LoadKeys(context.Context) (token.KeySet, error) { return o.set, nil }

func TestSignerRefusesWhatItCannotSign(t *testing.T) {
	f := newFakeKMS(t)
	f.addP256(t, uuid1)
	p := f.provider(t, "/"+uuid1)
	k, err := p.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ed, _, _ := ed25519.GenerateKey(rand.Reader)
	bad := map[string]keyprovider.Key{
		"empty ref":      {KID: k.KID, Algorithm: k.Algorithm, Public: k.Public},
		"other scheme":   {KID: k.KID, Algorithm: k.Algorithm, Public: k.Public, Ref: "gcpkms://x/y"},
		"malformed ref":  {KID: k.KID, Algorithm: k.Algorithm, Public: k.Public, Ref: "awskms://" + f.host() + "/not-a-key"},
		"no public key":  {KID: k.KID, Algorithm: k.Algorithm, Ref: k.Ref},
		"ed25519 public": {KID: k.KID, Algorithm: keys.EdDSA, Public: ed, Ref: k.Ref},
		"algorithm":      {KID: k.KID, Algorithm: keys.EdDSA, Public: k.Public, Ref: k.Ref},
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
	for name, opts := range map[string]crypto.SignerOpts{"nil": nil, "SHA-512": crypto.SHA512, "none": crypto.Hash(0)} {
		if _, err := sg.SignContext(context.Background(), digest[:], opts); err == nil {
			t.Errorf("opts %s: signed", name)
		}
	}
	if _, err := sg.SignContext(context.Background(), digest[:5], crypto.SHA256); err == nil {
		t.Error("a 5-byte digest signed")
	}
}

// A failed load of the sigstore signer is retried on the next call: the first
// sign has no credentials, the second does.
func TestSignRetriesAfterFailedLoad(t *testing.T) {
	f := newFakeKMS(t)
	f.addP256(t, uuid1)
	opts := f.opts(t)
	p, err := newProvider("awskms://"+f.host()+"/"+uuid1, opts...)
	if err != nil {
		t.Fatal(err)
	}
	k, err := p.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sg, err := p.Signer(context.Background(), k)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("x"))
	// Break the load: the opts fail the config load once.
	asImpl := sg.(*signer)
	good := asImpl.opts
	calls := 0
	asImpl.opts = append(append([]func(*config.LoadOptions) error{}, good...), func(*config.LoadOptions) error {
		calls++
		if calls == 1 {
			return errors.New("config unavailable")
		}
		return nil
	})
	if _, err := sg.SignContext(context.Background(), digest[:], crypto.SHA256); err == nil {
		t.Fatal("first sign should fail")
	}
	sig, err := sg.SignContext(context.Background(), digest[:], crypto.SHA256)
	if err != nil {
		t.Fatalf("second sign: %v", err)
	}
	if !ecdsa.VerifyASN1(k.Public.(*ecdsa.PublicKey), digest[:], sig) {
		t.Fatal("signature does not verify")
	}
}

func TestSignHonorsContext(t *testing.T) {
	f := newFakeKMS(t)
	f.addP256(t, uuid1)
	p := f.provider(t, "/"+uuid1)
	k, err := p.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sg, err := p.Signer(context.Background(), k)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("x"))

	// An already canceled context never reaches a signature.
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := sg.SignContext(canceled, digest[:], crypto.SHA256); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled: err = %v", err)
	}

	// A service that never answers Sign ends at the deadline, and the request
	// context reaches the fake (the handler returns when the client goes).
	f.hang("Sign")
	ctx, cancel2 := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel2()
	start := time.Now()
	_, err = sg.SignContext(ctx, digest[:], crypto.SHA256)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline: err = %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("took %v", time.Since(start))
	}
	// An out-of-time service is a failure of the service.
	if !errors.Is(err, keyprovider.ErrUnavailable) {
		t.Fatalf("a deadline is a service failure: %v", err)
	}
}

func TestServiceFailuresAreUnavailable(t *testing.T) {
	f := newFakeKMS(t)
	f.addP256(t, uuid1)
	p := f.provider(t, "/"+uuid1)
	ctx := context.Background()
	k, err := p.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("x"))

	for name, c := range map[string]struct {
		status  int
		errType string
		want    bool
	}{
		"500":                 {500, "KMSInternalException", true},
		"503":                 {503, "ServiceUnavailableException", true},
		"throttling 400":      {400, "ThrottlingException", true},
		"429":                 {429, "TooManyRequestsException", true},
		"dependency timeout":  {400, "DependencyTimeoutException", true},
		"bad credentials":     {400, "UnrecognizedClientException", false},
		"access denied":       {400, "AccessDeniedException", false},
		"missing key":         {400, "NotFoundException", false},
		"invalid key state":   {400, "KMSInvalidStateException", false},
		"expired credentials": {400, "ExpiredTokenException", false},
	} {
		f.failAll(c.status, c.errType)
		_, errNext := p.Next(ctx)
		fresh, _ := p.Signer(ctx, k) // a new signer: nothing cached in sigstore
		_, errSign := fresh.SignContext(ctx, digest[:], crypto.SHA256)
		for op, err := range map[string]error{"Next": errNext, "Sign": errSign} {
			if err == nil {
				t.Errorf("%s %s: no error", name, op)
				continue
			}
			if got := errors.Is(err, keyprovider.ErrUnavailable); got != c.want {
				t.Errorf("%s %s: ErrUnavailable = %v, want %v (%v)", name, op, got, c.want, err)
			}
		}
	}
	f.failAll(0, "")
	if _, err := p.Next(ctx); err != nil {
		t.Fatalf("recovered Next: %v", err)
	}
}

func TestUnreachableEndpointIsUnavailable(t *testing.T) {
	f := newFakeKMS(t)
	f.addP256(t, uuid1)
	p := f.provider(t, "/"+uuid1)
	k, err := p.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	f.srv.Close() // connection refused from here on
	if _, err := p.Next(context.Background()); !errors.Is(err, keyprovider.ErrUnavailable) {
		t.Fatalf("Next: %v", err)
	}
	sg, _ := p.Signer(context.Background(), k)
	digest := sha256.Sum256([]byte("x"))
	if _, err := sg.SignContext(context.Background(), digest[:], crypto.SHA256); !errors.Is(err, keyprovider.ErrUnavailable) {
		t.Fatalf("Sign: %v", err)
	}
}

// Errors name the key, never the whole URI: a URI may carry a token.
func TestErrorsDoNotQuoteTheURI(t *testing.T) {
	f := newFakeKMS(t)
	uri := "awskms://" + f.host() + "/alias/missing"
	p, err := newProvider(uri, f.opts(t)...)
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Next(context.Background())
	if err == nil {
		t.Fatal("a missing alias resolved")
	}
	if strings.Contains(err.Error(), uri) {
		t.Fatalf("error quotes the URI: %v", err)
	}
	if _, err := New("awskms://tok3n@bad/zzz"); err == nil || strings.Contains(err.Error(), "tok3n") {
		t.Fatalf("New error: %v", err)
	}
}

func TestNewRefusesURI(t *testing.T) {
	for _, uri := range []string{
		"",
		"awskms://",
		"awskms:///",
		"awskms:///not-a-key",
		"awskms://host",
		"awskms:///arn:aws:kms:us-east-1:123:key/" + uuid1, // account is not 12 digits
		"awskms:///arn:aws:iam::123456789012:key/" + uuid1,
		"awskms:///arn:aws:kms:us-east-1:123456789012:alias", // no alias name separator
		"AWSKMS:///alias/x",
		"gcpkms:///alias/x",
		"hashivault://key",
		"https:///alias/x",
		"awskms:/alias/x",
		"awskms:///" + uuid1 + "/extra",
	} {
		if p, err := New(uri); err == nil || p != nil {
			t.Errorf("New(%q) = %v, %v, want an error", uri, p, err)
		}
	}
	for _, uri := range []string{
		"awskms:///alias/agentbus2",
		"awskms://localhost:4566/alias/agentbus2",
		"awskms:///" + uuid1,
		"awskms:///" + arnFmt + uuid1,
		"awskms://localhost:4566/arn:aws:kms:us-east-1:" + account + ":alias/agentbus2",
	} {
		p, err := New(uri)
		if err != nil || p == nil || p.Name() != "awskms" {
			t.Errorf("New(%q) = %v, %v", uri, p, err)
		}
	}
}

func TestPinnedRefMustBeAcceptedBack(t *testing.T) {
	// The fake reports an ARN in a partition the module cannot load again:
	// Next refuses it rather than pin a ref Signer would refuse.
	f := newFakeKMS(t)
	f.addP256(t, "not-a-uuid")
	f.point("alias/odd", "not-a-uuid")
	if _, err := f.provider(t, "/alias/odd").Next(context.Background()); err == nil {
		t.Fatal("Next pinned a ref Signer cannot load")
	}
}

type failingCreds struct{ err error }

func (f failingCreds) Retrieve(context.Context) (aws.Credentials, error) {
	return aws.Credentials{}, f.err
}

// A credential failure is classified by the type of its cause: a network or
// timeout error reaching the credential source is the service's outage; every
// other one (no provider, invalid or expired credentials) is a refusal.
func TestCredentialFailuresAreClassifiedByType(t *testing.T) {
	f := newFakeKMS(t)
	f.addP256(t, uuid1)
	ctx := context.Background()
	good := f.provider(t, "/"+uuid1)
	k, err := good.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("x"))
	for name, c := range map[string]struct {
		err  error
		want bool
	}{
		"network error":   {&net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}, true},
		"deadline":        {context.DeadlineExceeded, true},
		"no provider":     {errors.New("no EC2 IMDS role found"), false},
		"invalid/expired": {errors.New("the security token included in the request is expired"), false},
	} {
		opts := append(f.opts(t), config.WithCredentialsProvider(failingCreds{c.err}))
		p, err := newProvider("awskms://"+f.host()+"/"+uuid1, opts...)
		if err != nil {
			t.Fatal(err)
		}
		_, errNext := p.Next(ctx)
		sg, err := p.Signer(ctx, k)
		if err != nil {
			t.Fatal(err)
		}
		_, errSign := sg.SignContext(ctx, digest[:], crypto.SHA256)
		for op, e := range map[string]error{"Next": errNext, "Sign": errSign} {
			if e == nil {
				t.Errorf("%s %s: no error", name, op)
				continue
			}
			if got := errors.Is(e, keyprovider.ErrUnavailable); got != c.want {
				t.Errorf("%s %s: ErrUnavailable = %v, want %v (%v)", name, op, got, c.want, e)
			}
		}
	}
}

func TestNewRefusesEndpointUserInfo(t *testing.T) {
	for _, uri := range []string{
		"awskms://user:s3cret@host:4566/alias/x",
		"awskms://s3cret@host/alias/x",
		"awskms://host@evil/alias/x",
	} {
		p, err := New(uri)
		if err == nil || p != nil {
			t.Errorf("New(%q) = %v, %v, want an error", uri, p, err)
			continue
		}
		if strings.Contains(err.Error(), "s3cret") || strings.Contains(err.Error(), "user:") {
			t.Errorf("New(%q) error quotes the URI: %v", uri, err)
		}
	}
}

func TestNextRefusesWrongKeyUsage(t *testing.T) {
	f := newFakeKMS(t)
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	f.mu.Lock()
	f.keys[uuid1] = &kmsKey{id: uuid1, priv: k, spec: "ECC_NIST_P256", usage: "ENCRYPT_DECRYPT"}
	f.mu.Unlock()
	_, err := f.provider(t, "/"+uuid1).Next(context.Background())
	if err == nil || !strings.Contains(err.Error(), "ENCRYPT_DECRYPT") || errors.Is(err, keyprovider.ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

// Provider text is capped at 256 bytes (never splitting a rune) and a secret
// from the environment is redacted, on Next and on Sign, for a refusal and for
// an outage alike; the classification by type is unchanged.
func TestProviderTextIsRedactedAndBounded(t *testing.T) {
	const secret = "s3cr3t-AWS-key-material"
	t.Setenv("AWS_SECRET_ACCESS_KEY", secret)
	t.Setenv("AWS_SESSION_TOKEN", secret+"-session")
	f := newFakeKMS(t)
	f.addP256(t, uuid1)
	p := f.provider(t, "/"+uuid1)
	ctx := context.Background()
	k, err := p.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("x"))
	long := strings.Repeat("é", 400) // multi-byte: the cut must not split a rune
	for name, c := range map[string]struct {
		status  int
		errType string
		want    bool
	}{
		"refusal": {400, "AccessDeniedException", false},
		"outage":  {503, "ServiceUnavailableException", true},
	} {
		for _, msg := range []string{strings.Repeat("A", 1000), long, "echo " + secret + " and " + secret + "-session"} {
			f.mu.Lock()
			f.status, f.errType, f.errMsg = c.status, c.errType, msg
			f.mu.Unlock()
			_, errNext := p.Next(ctx)
			fresh, _ := p.Signer(ctx, k)
			_, errSign := fresh.SignContext(ctx, digest[:], crypto.SHA256)
			for op, err := range map[string]error{"Next": errNext, "Sign": errSign} {
				if err == nil {
					t.Fatalf("%s %s: no error", name, op)
				}
				text := err.Error()
				if len(text) > 256+200 { // the cap plus our own prefix
					t.Errorf("%s %s: %d bytes of error text: %.120s...", name, op, len(text), text)
				}
				if !utf8.ValidString(text) {
					t.Errorf("%s %s: error text is not valid UTF-8", name, op)
				}
				if strings.Contains(text, secret) {
					t.Errorf("%s %s: the error leaks a secret: %s", name, op, text)
				}
				if got := errors.Is(err, keyprovider.ErrUnavailable); got != c.want {
					t.Errorf("%s %s: ErrUnavailable = %v, want %v", name, op, got, c.want)
				}
			}
		}
	}
	f.failAll(0, "")
}
