package gcpkms

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"hash/crc32"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"cloud.google.com/go/auth"
	"cloud.google.com/go/kms/apiv1/kmspb"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/ericfitz/agentbus2/internal/auth/keys"
	"github.com/ericfitz/agentbus2/internal/auth/token"
	"github.com/ericfitz/agentbus2/internal/keyprovider"
)

const keyPath = "projects/proj-1/locations/global/keyRings/ring-1/cryptoKeys/agentbus2"

var keyURI = "gcpkms://" + keyPath

// TestMain keeps Application Default Credentials from probing the real
// instance metadata server (169.254.169.254) and from reading the developer's
// own credentials: every test either bypasses credentials (the fake) or points
// them at a local port.
func setenv(k, v string) {
	if err := os.Setenv(k, v); err != nil {
		panic(err)
	}
}

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "gcpkms-test-")
	if err != nil {
		panic(err)
	}
	setenv("GCE_METADATA_HOST", "127.0.0.1:1")
	setenv("GOOGLE_APPLICATION_CREDENTIALS", "")
	setenv("APPDATA", dir)
	setenv("HOME", dir)
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// fver is one version of the fake's crypto key.
type fver struct {
	state kmspb.CryptoKeyVersion_CryptoKeyVersionState
	alg   kmspb.CryptoKeyVersion_CryptoKeyVersionAlgorithm
	priv  crypto.Signer
}

type fkey struct {
	purpose  kmspb.CryptoKey_CryptoKeyPurpose
	versions map[int]*fver
}

// fakeKMS is an in-process gRPC Cloud KMS: the read RPCs and AsymmetricSign.
type fakeKMS struct {
	kmspb.UnimplementedKeyManagementServiceServer

	mu      sync.Mutex
	keys    map[string]*fkey
	calls   []string
	signReq []*kmspb.AsymmetricSignRequest
	hangOps map[string]bool
	failOps map[string]error
	release chan struct{}
	freed   sync.Once
	// wrongKey makes AsymmetricSign sign with a key other than the version's.
	wrongKey crypto.Signer
	// noSigCRC leaves SignatureCrc32C unset (sigstore dereferences it).
	noSigCRC bool
	// answerProject, when set, replaces the project segment of every version
	// name the fake answers with (Cloud KMS may answer with the project number).
	answerProject string
	// rename answers a version with a name of its own (by version number).
	rename map[int]string

	srv  *grpc.Server
	addr string
}

func newFakeKMS(t *testing.T) *fakeKMS {
	t.Helper()
	f := &fakeKMS{
		keys:    map[string]*fkey{},
		hangOps: map[string]bool{},
		failOps: map[string]error{},
		release: make(chan struct{}),
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f.srv = grpc.NewServer()
	kmspb.RegisterKeyManagementServiceServer(f.srv, f)
	go func() { _ = f.srv.Serve(lis) }()
	f.addr = lis.Addr().String()
	t.Cleanup(func() { f.free(); f.srv.Stop() })
	// Sigstore retries an unavailable service on a background context, so a
	// failed sign can leave a goroutine (and its slot) behind for up to a
	// minute: each test gets its own slots.
	old := signSlots
	signSlots = make(chan struct{}, 16)
	t.Cleanup(func() { signSlots = old })
	return f
}

func (f *fakeKMS) free() { f.freed.Do(func() { close(f.release) }) }

func p256() *ecdsa.PrivateKey {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	return k
}

// addKey creates the crypto key keyPath with one ENABLED P-256 version 1.
func (f *fakeKMS) addKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys[keyPath] = &fkey{purpose: kmspb.CryptoKey_ASYMMETRIC_SIGN, versions: map[int]*fver{}}
	return f.addVersionLocked(1, kmspb.CryptoKeyVersion_ENABLED, kmspb.CryptoKeyVersion_EC_SIGN_P256_SHA256, p256())
}

func (f *fakeKMS) addVersionLocked(id int, st kmspb.CryptoKeyVersion_CryptoKeyVersionState, alg kmspb.CryptoKeyVersion_CryptoKeyVersionAlgorithm, priv crypto.Signer) *ecdsa.PrivateKey {
	f.keys[keyPath].versions[id] = &fver{state: st, alg: alg, priv: priv}
	k, _ := priv.(*ecdsa.PrivateKey)
	return k
}

func (f *fakeKMS) addVersion(id int, st kmspb.CryptoKeyVersion_CryptoKeyVersionState, alg kmspb.CryptoKeyVersion_CryptoKeyVersionAlgorithm, priv crypto.Signer) *ecdsa.PrivateKey {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.addVersionLocked(id, st, alg, priv)
}

func (f *fakeKMS) setPurpose(p kmspb.CryptoKey_CryptoKeyPurpose) {
	f.mu.Lock()
	f.keys[keyPath].purpose = p
	f.mu.Unlock()
}

func (f *fakeKMS) fail(method string, err error) {
	f.mu.Lock()
	f.failOps[method] = err
	f.mu.Unlock()
}

func (f *fakeKMS) unfail(method string) {
	f.mu.Lock()
	delete(f.failOps, method)
	f.mu.Unlock()
}

func (f *fakeKMS) hang(method string) {
	f.mu.Lock()
	f.hangOps[method] = true
	f.mu.Unlock()
}

func (f *fakeKMS) unhang() {
	f.mu.Lock()
	f.hangOps = map[string]bool{}
	f.mu.Unlock()
}

func (f *fakeKMS) count(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == method {
			n++
		}
	}
	return n
}

func (f *fakeKMS) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// gate records the call and applies a configured hang or failure.
func (f *fakeKMS) gate(_ context.Context, method string) error {
	f.mu.Lock()
	f.calls = append(f.calls, method)
	hang := f.hangOps[method]
	ferr := f.failOps[method]
	f.mu.Unlock()
	if hang {
		// A hung handler never answers. The deadline still ends the call: the
		// grpc server enforces its copy of grpc-timeout itself and ends the
		// stream when it fires, and the client's context ends it too;
		// TestNextHonorsServerSideDeadline covers the first path alone.
		<-f.release
	}
	return ferr
}

func (f *fakeKMS) lookup(name string) (*fkey, string, *fver, int, error) {
	parent, ver, hasVer := strings.Cut(name, "/cryptoKeyVersions/")
	f.mu.Lock()
	defer f.mu.Unlock()
	k, ok := f.keys[parent]
	if !ok {
		return nil, "", nil, 0, status.Error(codes.NotFound, "no such crypto key")
	}
	if !hasVer {
		return k, parent, nil, 0, nil
	}
	id, _ := strconv.Atoi(ver)
	v, ok := k.versions[id]
	if !ok {
		return nil, "", nil, 0, status.Error(codes.NotFound, "no such crypto key version")
	}
	return k, parent, v, id, nil
}

func (f *fakeKMS) GetCryptoKey(ctx context.Context, req *kmspb.GetCryptoKeyRequest) (*kmspb.CryptoKey, error) {
	if err := f.gate(ctx, "GetCryptoKey"); err != nil {
		return nil, err
	}
	k, parent, _, _, err := f.lookup(req.Name)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return &kmspb.CryptoKey{Name: parent, Purpose: k.purpose}, nil
}

func (f *fakeKMS) versionProto(parent string, id int, v *fver) *kmspb.CryptoKeyVersion {
	name := parent + "/cryptoKeyVersions/" + strconv.Itoa(id)
	if f.answerProject != "" {
		_, rest, _ := strings.Cut(name, "/locations/")
		name = "projects/" + f.answerProject + "/locations/" + rest
	}
	if n, ok := f.rename[id]; ok {
		name = n
	}
	return &kmspb.CryptoKeyVersion{Name: name, State: v.state, Algorithm: v.alg}
}

func (f *fakeKMS) GetCryptoKeyVersion(ctx context.Context, req *kmspb.GetCryptoKeyVersionRequest) (*kmspb.CryptoKeyVersion, error) {
	if err := f.gate(ctx, "GetCryptoKeyVersion"); err != nil {
		return nil, err
	}
	_, parent, v, id, err := f.lookup(req.Name)
	if err != nil {
		return nil, err
	}
	if v == nil {
		return nil, status.Error(codes.InvalidArgument, "not a version name")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.versionProto(parent, id, v), nil
}

// ListCryptoKeyVersions answers two versions a page, newest name first in
// string order ("9" before "10"), as the real service may order them.
func (f *fakeKMS) ListCryptoKeyVersions(ctx context.Context, req *kmspb.ListCryptoKeyVersionsRequest) (*kmspb.ListCryptoKeyVersionsResponse, error) {
	if err := f.gate(ctx, "ListCryptoKeyVersions"); err != nil {
		return nil, err
	}
	k, parent, _, _, err := f.lookup(req.Parent)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	var ids []int
	for id, v := range k.versions {
		if req.Filter == "state=ENABLED" && v.state != kmspb.CryptoKeyVersion_ENABLED {
			continue
		}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return strconv.Itoa(ids[i]) > strconv.Itoa(ids[j]) })
	start := 0
	if req.PageToken != "" {
		start, _ = strconv.Atoi(req.PageToken)
	}
	end := min(start+2, len(ids))
	resp := &kmspb.ListCryptoKeyVersionsResponse{TotalSize: int32(len(ids))}
	for _, id := range ids[start:end] {
		resp.CryptoKeyVersions = append(resp.CryptoKeyVersions, f.versionProto(parent, id, k.versions[id]))
	}
	f.mu.Unlock()
	if end < len(ids) {
		resp.NextPageToken = strconv.Itoa(end)
	}
	return resp, nil
}

func crc(b []byte) int64 { return int64(crc32.Checksum(b, crc32.MakeTable(crc32.Castagnoli))) }

func (f *fakeKMS) GetPublicKey(ctx context.Context, req *kmspb.GetPublicKeyRequest) (*kmspb.PublicKey, error) {
	if err := f.gate(ctx, "GetPublicKey"); err != nil {
		return nil, err
	}
	_, _, v, _, err := f.lookup(req.Name)
	if err != nil {
		return nil, err
	}
	if v == nil {
		return nil, status.Error(codes.InvalidArgument, "not a version name")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	der, err := x509.MarshalPKIXPublicKey(v.priv.Public())
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	return &kmspb.PublicKey{Pem: string(pemBytes), Algorithm: v.alg, Name: req.Name, PemCrc32C: wrapperspb.Int64(crc(pemBytes))}, nil
}

func (f *fakeKMS) AsymmetricSign(ctx context.Context, req *kmspb.AsymmetricSignRequest) (*kmspb.AsymmetricSignResponse, error) {
	if err := f.gate(ctx, "AsymmetricSign"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.signReq = append(f.signReq, req)
	f.mu.Unlock()
	_, _, v, _, err := f.lookup(req.Name)
	if err != nil {
		return nil, err
	}
	if v == nil {
		return nil, status.Error(codes.InvalidArgument, "not a version name")
	}
	digest := req.GetDigest().GetSha256()
	if len(digest) != sha256.Size {
		return nil, status.Error(codes.InvalidArgument, "need a SHA-256 digest")
	}
	f.mu.Lock()
	signer, noCRC := v.priv, f.noSigCRC
	if f.wrongKey != nil {
		signer = f.wrongKey
	}
	f.mu.Unlock()
	sig, err := signer.Sign(rand.Reader, digest, crypto.SHA256)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	resp := &kmspb.AsymmetricSignResponse{
		Name:                 req.Name,
		Signature:            sig,
		VerifiedDigestCrc32C: req.DigestCrc32C != nil && req.DigestCrc32C.Value == crc(digest),
	}
	if !noCRC {
		resp.SignatureCrc32C = wrapperspb.Int64(crc(sig))
	}
	return resp, nil
}

// signRequests returns the AsymmetricSign requests seen.
func (f *fakeKMS) signRequests() []*kmspb.AsymmetricSignRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*kmspb.AsymmetricSignRequest(nil), f.signReq...)
}

// testConn points the SDK at addr with no authentication and no credential
// step: nothing here touches Application Default Credentials.
func testConn(addr string) connector {
	return connector{opts: []option.ClientOption{
		option.WithEndpoint(addr),
		option.WithoutAuthentication(),
		option.WithGRPCDialOption(grpc.WithTransportCredentials(insecure.NewCredentials())),
	}}
}

func (f *fakeKMS) provider(t *testing.T, uri string) keyprovider.Provider {
	t.Helper()
	p, err := newProvider(uri, testConn(f.addr))
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
	sg, err := p.Signer(context.Background(), k)
	if err != nil {
		t.Fatal(err)
	}
	return sg
}

var sha = func(s string) []byte { d := sha256.Sum256([]byte(s)); return d[:] }

func TestNameIsGcpkms(t *testing.T) {
	p, err := New(keyURI)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != "gcpkms" {
		t.Fatalf("Name = %q", p.Name())
	}
}

func TestNextResolvesAndPins(t *testing.T) {
	f := newFakeKMS(t)
	v1 := f.addKey(t)
	p := f.provider(t, keyURI)
	k := mustNext(t, p)
	if k.Ref != keyURI+"/cryptoKeyVersions/1" || k.Algorithm != keys.ES256 {
		t.Fatalf("key %+v", k)
	}
	if !v1.PublicKey.Equal(k.Public) {
		t.Fatal("public key is not version 1's")
	}
	// Versions 2, 9 and 10 exist (the service may list 9 before 10), 11 is
	// disabled and 12 is destroyed: the newest ENABLED one by number wins.
	f.addVersion(2, kmspb.CryptoKeyVersion_ENABLED, kmspb.CryptoKeyVersion_EC_SIGN_P256_SHA256, p256())
	f.addVersion(9, kmspb.CryptoKeyVersion_ENABLED, kmspb.CryptoKeyVersion_EC_SIGN_P256_SHA256, p256())
	v10 := f.addVersion(10, kmspb.CryptoKeyVersion_ENABLED, kmspb.CryptoKeyVersion_EC_SIGN_P256_SHA256, p256())
	f.addVersion(11, kmspb.CryptoKeyVersion_DISABLED, kmspb.CryptoKeyVersion_EC_SIGN_P256_SHA256, p256())
	f.addVersion(12, kmspb.CryptoKeyVersion_DESTROYED, kmspb.CryptoKeyVersion_EC_SIGN_P256_SHA256, p256())
	k2 := mustNext(t, p)
	if k2.Ref != keyURI+"/cryptoKeyVersions/10" || !v10.PublicKey.Equal(k2.Public) || k2.KID == k.KID {
		t.Fatalf("after repoint: %+v", k2)
	}
	// An explicit version stays put, whatever is newer.
	pin := f.provider(t, keyURI+"/cryptoKeyVersions/2")
	if kp := mustNext(t, pin); kp.Ref != keyURI+"/cryptoKeyVersions/2" {
		t.Fatalf("pinned: %+v", kp)
	}
	// The pinned ref is accepted back by Signer.
	if _, err := p.Signer(context.Background(), k2); err != nil {
		t.Fatal(err)
	}
}

func TestNextRefusesKeyType(t *testing.T) {
	cases := []struct {
		name string
		alg  kmspb.CryptoKeyVersion_CryptoKeyVersionAlgorithm
		priv crypto.Signer
	}{
		{"p384", kmspb.CryptoKeyVersion_EC_SIGN_P384_SHA384, mustP384()},
		{"ed25519", kmspb.CryptoKeyVersion_EC_SIGN_ED25519, mustEd()},
		{"rsa algorithm label", kmspb.CryptoKeyVersion_RSA_SIGN_PKCS1_2048_SHA256, p256()},
		// The service claims P-256 but serves another curve's key.
		{"p256 label, p384 key", kmspb.CryptoKeyVersion_EC_SIGN_P256_SHA256, mustP384()},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeKMS(t)
			f.addKey(t)
			f.addVersion(2, kmspb.CryptoKeyVersion_ENABLED, c.alg, c.priv)
			_, err := f.provider(t, keyURI).Next(context.Background())
			if err == nil || errors.Is(err, keyprovider.ErrUnavailable) {
				t.Fatalf("err = %v, want a refusal", err)
			}
			if !strings.Contains(err.Error(), "P-256") {
				t.Fatalf("the refusal does not name P-256: %v", err)
			}
		})
	}
}

func mustP384() *ecdsa.PrivateKey {
	k, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		panic(err)
	}
	return k
}

func mustEd() ed25519.PrivateKey {
	_, k, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	return k
}

func TestNextRefusesWhatIsNotUsable(t *testing.T) {
	t.Run("purpose is not ASYMMETRIC_SIGN", func(t *testing.T) {
		f := newFakeKMS(t)
		f.addKey(t)
		f.setPurpose(kmspb.CryptoKey_ENCRYPT_DECRYPT)
		expectRefusal(t, f.provider(t, keyURI), "ASYMMETRIC_SIGN")
	})
	t.Run("no enabled version", func(t *testing.T) {
		f := newFakeKMS(t)
		f.addKey(t)
		f.addVersion(1, kmspb.CryptoKeyVersion_DISABLED, kmspb.CryptoKeyVersion_EC_SIGN_P256_SHA256, p256())
		expectRefusal(t, f.provider(t, keyURI), "ENABLED")
	})
	t.Run("pinned version is disabled", func(t *testing.T) {
		f := newFakeKMS(t)
		f.addKey(t)
		f.addVersion(3, kmspb.CryptoKeyVersion_DISABLED, kmspb.CryptoKeyVersion_EC_SIGN_P256_SHA256, p256())
		expectRefusal(t, f.provider(t, keyURI+"/cryptoKeyVersions/3"), "ENABLED")
	})
	t.Run("pinned version does not exist", func(t *testing.T) {
		f := newFakeKMS(t)
		f.addKey(t)
		expectRefusal(t, f.provider(t, keyURI+"/cryptoKeyVersions/7"), "")
	})
	t.Run("key does not exist", func(t *testing.T) {
		f := newFakeKMS(t)
		expectRefusal(t, f.provider(t, keyURI), "")
	})
}

func expectRefusal(t *testing.T, p keyprovider.Provider, want string) {
	t.Helper()
	_, err := p.Next(context.Background())
	if err == nil || errors.Is(err, keyprovider.ErrUnavailable) || !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %v, want a non-retryable refusal containing %q", err, want)
	}
	if strings.Contains(err.Error(), "gcpkms://") {
		t.Fatalf("the error quotes the URI: %v", err)
	}
}

func TestSignerIsLazy(t *testing.T) {
	f := newFakeKMS(t)
	f.addKey(t)
	var credCalls int
	c := testConn(f.addr)
	c.creds = func(context.Context) (*auth.Credentials, error) {
		credCalls++
		return nil, errors.New("test: no credentials")
	}
	// New reads no credentials and makes no request.
	p, err := newProvider(keyURI, c)
	if err != nil {
		t.Fatal(err)
	}
	if credCalls != 0 || f.total() != 0 {
		t.Fatalf("New: %d credential reads, %d requests", credCalls, f.total())
	}
	// Signer makes none either, with credentials that would fail.
	k, err := keyprovider.ExternalKey(&p256().PublicKey, keyURI+"/cryptoKeyVersions/1")
	if err != nil {
		t.Fatal(err)
	}
	sg, err := p.Signer(context.Background(), k)
	if err != nil {
		t.Fatal(err)
	}
	if credCalls != 0 || f.total() != 0 {
		t.Fatalf("Signer: %d credential reads, %d requests", credCalls, f.total())
	}
	if !k.Public.(*ecdsa.PublicKey).Equal(sg.Public()) || f.total() != 0 {
		t.Fatal("Public is not the row's key or asked the service")
	}
	// The first sign is where credentials are read.
	if _, err := sg.SignContext(context.Background(), sha("x"), crypto.SHA256); err == nil || credCalls != 1 {
		t.Fatalf("first sign: %v, %d credential reads", err, credCalls)
	}
}

type oneKey struct{ set token.KeySet }

func (o oneKey) LoadKeys(context.Context) (token.KeySet, error) { return o.set, nil }

func TestSignVerifiesAsJWS(t *testing.T) {
	f := newFakeKMS(t)
	f.addKey(t)
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
	reqs := f.signRequests()
	if len(reqs) != 1 || reqs[0].Name != keyPath+"/cryptoKeyVersions/1" || len(reqs[0].GetDigest().GetSha256()) != 32 {
		t.Fatalf("sign requests %v", reqs)
	}
}

func TestSignUsesThePinnedVersion(t *testing.T) {
	f := newFakeKMS(t)
	v1 := f.addKey(t)
	p := f.provider(t, keyURI)
	k1 := mustNext(t, p)
	f.addVersion(2, kmspb.CryptoKeyVersion_ENABLED, kmspb.CryptoKeyVersion_EC_SIGN_P256_SHA256, p256())
	sg := mustSigner(t, p, k1)
	d := sha("pinned")
	sig, err := sg.SignContext(context.Background(), d, crypto.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	if !ecdsa.VerifyASN1(&v1.PublicKey, d, sig) {
		t.Fatal("not signed by version 1")
	}
	if r := f.signRequests(); len(r) != 1 || !strings.HasSuffix(r[0].Name, "/cryptoKeyVersions/1") {
		t.Fatalf("requests %v", r)
	}
}

func TestSignerRefusesWhatItCannotSign(t *testing.T) {
	f := newFakeKMS(t)
	f.addKey(t)
	p := f.provider(t, keyURI)
	k := mustNext(t, p)
	bad := map[string]keyprovider.Key{}
	k1 := k
	k1.Algorithm = keys.EdDSA
	bad["algorithm"] = k1
	k2 := k
	k2.Public = mustEd().Public()
	bad["not ecdsa"] = k2
	k3 := k
	k3.Public = &mustP384().PublicKey
	bad["p384"] = k3
	for name, ref := range map[string]string{
		"other scheme":    "awskms:///alias/x",
		"versionless":     keyURI,
		"bad version":     keyURI + "/cryptoKeyVersions/0",
		"extra segment":   keyURI + "/cryptoKeyVersions/1/x",
		"userinfo":        "gcpkms://u:p@projects/p/locations/l/keyRings/r/cryptoKeys/k/cryptoKeyVersions/1",
		"empty reference": "",
	} {
		kk := k
		kk.Ref = ref
		bad["ref "+name] = kk
	}
	for name, kk := range bad {
		if _, err := p.Signer(context.Background(), kk); err == nil {
			t.Errorf("%s: Signer accepted the key", name)
		} else if strings.Contains(err.Error(), "u:p@") {
			t.Errorf("%s: error quotes the URI: %v", name, err)
		}
	}
	sg := mustSigner(t, p, k)
	if _, err := sg.SignContext(context.Background(), sha("x"), crypto.SHA512); err == nil {
		t.Error("a SHA-512 request was accepted")
	}
	if _, err := sg.SignContext(context.Background(), sha("x"), nil); err == nil {
		t.Error("nil options accepted")
	}
	if _, err := sg.SignContext(context.Background(), []byte("short"), crypto.SHA256); err == nil {
		t.Error("a short digest was accepted")
	}
	if f.count("AsymmetricSign") != 0 {
		t.Error("a refused request reached the service")
	}
}

func TestSignRetriesAfterFailedLoad(t *testing.T) {
	f := newFakeKMS(t)
	f.addKey(t)
	p := f.provider(t, keyURI).(*Provider)
	k := mustNext(t, p)
	// The credential step fails once, then lets the signer through.
	var calls int
	p.conn.creds = func(context.Context) (*auth.Credentials, error) {
		calls++
		if calls == 1 {
			return nil, &net.OpError{Op: "dial", Err: errors.New("connection refused")}
		}
		return nil, nil
	}
	sg := mustSigner(t, p, k)
	_, err := sg.SignContext(context.Background(), sha("x"), crypto.SHA256)
	if !errors.Is(err, keyprovider.ErrUnavailable) {
		t.Fatalf("first sign: %v", err)
	}
	sig, err := sg.SignContext(context.Background(), sha("x"), crypto.SHA256)
	if err != nil || !ecdsa.VerifyASN1(k.Public.(*ecdsa.PublicKey), sha("x"), sig) {
		t.Fatalf("second sign: %v", err)
	}
	if calls != 2 {
		t.Fatalf("%d credential reads", calls)
	}
	// Once built, the signer is kept: no further credential read.
	if _, err := sg.SignContext(context.Background(), sha("y"), crypto.SHA256); err != nil || calls != 2 {
		t.Fatalf("third sign: %v, %d reads", err, calls)
	}
}

func TestSignHonorsContext(t *testing.T) {
	for _, hung := range []string{"AsymmetricSign", "GetCryptoKey", "GetPublicKey"} {
		t.Run("hung "+hung, func(t *testing.T) {
			f := newFakeKMS(t)
			f.addKey(t)
			p := f.provider(t, keyURI)
			k := mustNext(t, p)
			sg := mustSigner(t, p, k)
			if hung == "AsymmetricSign" { // warm sigstore's version cache first
				if _, err := sg.SignContext(context.Background(), sha("w"), crypto.SHA256); err != nil {
					t.Fatal(err)
				}
			}
			f.hang(hung)
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			start := time.Now()
			_, err := sg.SignContext(ctx, sha("x"), crypto.SHA256)
			if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, keyprovider.ErrUnavailable) || time.Since(start) > 3*time.Second {
				t.Fatalf("err = %v after %v", err, time.Since(start))
			}
		})
	}
	t.Run("canceled", func(t *testing.T) {
		f := newFakeKMS(t)
		f.addKey(t)
		p := f.provider(t, keyURI)
		sg := mustSigner(t, p, mustNext(t, p))
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		n := f.total()
		_, err := sg.SignContext(ctx, sha("x"), crypto.SHA256)
		if !errors.Is(err, context.Canceled) || errors.Is(err, keyprovider.ErrUnavailable) || f.total() != n {
			t.Fatalf("err = %v, requests %d", err, f.total()-n)
		}
	})
	t.Run("canceled while waiting", func(t *testing.T) {
		f := newFakeKMS(t)
		f.addKey(t)
		p := f.provider(t, keyURI)
		sg := mustSigner(t, p, mustNext(t, p))
		f.hang("GetCryptoKey")
		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(100*time.Millisecond, cancel)
		_, err := sg.SignContext(ctx, sha("x"), crypto.SHA256)
		if !errors.Is(err, context.Canceled) || errors.Is(err, keyprovider.ErrUnavailable) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestNextHonorsContext(t *testing.T) {
	for _, hung := range []string{"GetCryptoKey", "ListCryptoKeyVersions", "GetPublicKey"} {
		t.Run(hung, func(t *testing.T) {
			f := newFakeKMS(t)
			f.addKey(t)
			f.hang(hung)
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			start := time.Now()
			_, err := f.provider(t, keyURI).Next(ctx)
			if !errors.Is(err, keyprovider.ErrUnavailable) || !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 3*time.Second {
				t.Fatalf("err = %v after %v", err, time.Since(start))
			}
		})
	}
	t.Run("canceled", func(t *testing.T) {
		f := newFakeKMS(t)
		f.addKey(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := f.provider(t, keyURI).Next(ctx)
		if !errors.Is(err, context.Canceled) || errors.Is(err, keyprovider.ErrUnavailable) {
			t.Fatalf("err = %v", err)
		}
	})
}

// A hung explicit-version read is bounded too (a different RPC than the list).
func TestNextHonorsContextForPinnedVersion(t *testing.T) {
	f := newFakeKMS(t)
	f.addKey(t)
	f.hang("GetCryptoKeyVersion")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := f.provider(t, keyURI+"/cryptoKeyVersions/1").Next(ctx)
	if !errors.Is(err, keyprovider.ErrUnavailable) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
}

// announcedDeadline is a context that advertises a deadline it never
// enforces: the gRPC client sends it as grpc-timeout and only the server's
// copy fires, which is what happens in a race the server wins.
type announcedDeadline struct{ dl time.Time }

func (a announcedDeadline) Deadline() (time.Time, bool) { return a.dl, true }
func (announcedDeadline) Done() <-chan struct{}         { return nil }
func (announcedDeadline) Err() error                    { return nil }
func (announcedDeadline) Value(any) any                 { return nil }

// The grpc server enforces the caller's deadline itself: when its copy of
// grpc-timeout fires it ends the stream, and the client reports a
// DeadlineExceeded status that can arrive before the caller's context is
// marked done. The answer is the deadline all the same.
func TestNextHonorsServerSideDeadline(t *testing.T) {
	for _, hung := range []string{"GetCryptoKey", "ListCryptoKeyVersions", "GetPublicKey", "GetCryptoKeyVersion"} {
		t.Run(hung, func(t *testing.T) {
			f := newFakeKMS(t)
			f.addKey(t)
			f.hang(hung)
			uri := keyURI
			if hung == "GetCryptoKeyVersion" {
				uri += "/cryptoKeyVersions/1"
			}
			start := time.Now()
			_, err := f.provider(t, uri).Next(announcedDeadline{time.Now().Add(200 * time.Millisecond)})
			if !errors.Is(err, keyprovider.ErrUnavailable) || !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 3*time.Second {
				t.Fatalf("err = %v after %v", err, time.Since(start))
			}
			if !strings.Contains(err.Error(), "DeadlineExceeded") {
				t.Fatalf("the error does not name the code: %v", err)
			}
		})
	}
}

func TestServiceFailuresAreClassified(t *testing.T) {
	retryable := []codes.Code{codes.Unavailable, codes.DeadlineExceeded, codes.ResourceExhausted, codes.Internal, codes.Unknown}
	refused := []codes.Code{codes.PermissionDenied, codes.Unauthenticated, codes.NotFound, codes.InvalidArgument, codes.FailedPrecondition}
	next := func(uri string) func(*testing.T, *fakeKMS, func()) error {
		return func(t *testing.T, f *fakeKMS, arm func()) error {
			p := f.provider(t, uri)
			arm()
			_, err := p.Next(context.Background())
			return err
		}
	}
	// sign fails a sign; warm first sends a good one so sigstore's cache is full.
	sign := func(warm bool) func(*testing.T, *fakeKMS, func()) error {
		return func(t *testing.T, f *fakeKMS, arm func()) error {
			p := f.provider(t, keyURI)
			sg := mustSigner(t, p, mustNext(t, p))
			if warm {
				if _, err := sg.SignContext(context.Background(), sha("warm"), crypto.SHA256); err != nil {
					t.Fatal(err)
				}
			}
			arm()
			// Sigstore's client retries an unavailable service until its context
			// ends, so a deadline stands for the issuer's.
			ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
			defer cancel()
			_, err := sg.SignContext(ctx, sha("x"), crypto.SHA256)
			return err
		}
	}
	type phase struct {
		rpc string
		run func(*testing.T, *fakeKMS, func()) error
	}
	phases := map[string]phase{
		"Next/GetCryptoKey":          {"GetCryptoKey", next(keyURI)},
		"Next/ListCryptoKeyVersions": {"ListCryptoKeyVersions", next(keyURI)},
		"Next/GetCryptoKeyVersion":   {"GetCryptoKeyVersion", next(keyURI + "/cryptoKeyVersions/1")},
		"Next/GetPublicKey":          {"GetPublicKey", next(keyURI)},
		"Sign/first read":            {"GetCryptoKey", sign(false)},
		"Sign/AsymmetricSign":        {"AsymmetricSign", sign(true)},
	}
	for name, ph := range phases {
		for _, code := range append(append([]codes.Code{}, retryable...), refused...) {
			want := slices.Contains(retryable, code)
			t.Run(name+"/"+code.String(), func(t *testing.T) {
				f := newFakeKMS(t)
				f.addKey(t)
				err := ph.run(t, f, func() { f.fail(ph.rpc, status.Error(code, "service said no")) })
				if err == nil {
					t.Fatal("no error")
				}
				if got := errors.Is(err, keyprovider.ErrUnavailable); got != want {
					t.Fatalf("unavailable = %v, want %v (%v)", got, want, err)
				}
				// A retried sign ends with its deadline, not the service's code.
				retriedSign := strings.HasPrefix(name, "Sign/") && want
				if (!retriedSign && !strings.Contains(err.Error(), code.String())) || strings.Contains(err.Error(), "gcpkms://") {
					t.Fatalf("the error does not name the code or quotes the URI: %v", err)
				}
			})
		}
	}
}

func TestUnreachableEndpointIsUnavailable(t *testing.T) {
	f := newFakeKMS(t)
	f.addKey(t)
	p := f.provider(t, keyURI)
	k := mustNext(t, p)
	sg := mustSigner(t, p, k)
	f.srv.Stop() // the port now refuses connections
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	if _, err := p.Next(ctx); !errors.Is(err, keyprovider.ErrUnavailable) {
		t.Fatalf("Next: %v", err)
	}
	if _, err := sg.SignContext(ctx, sha("x"), crypto.SHA256); !errors.Is(err, keyprovider.ErrUnavailable) {
		t.Fatalf("Sign: %v", err)
	}
}

func TestCredentialFailures(t *testing.T) {
	// The default connector runs Application Default Credentials. The instance
	// metadata server is a local fake (GCE_METADATA_HOST), never the real one.
	metadata := func(t *testing.T, h http.HandlerFunc) {
		srv := httptest.NewServer(h)
		t.Cleanup(srv.Close)
		t.Setenv("GCE_METADATA_HOST", strings.TrimPrefix(srv.URL, "http://"))
	}
	next := func(t *testing.T) error {
		p, err := New(keyURI)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, err = p.Next(ctx)
		return err
	}
	t.Run("metadata server answers 503", func(t *testing.T) {
		metadata(t, func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "try later", http.StatusServiceUnavailable)
		})
		if err := next(t); !errors.Is(err, keyprovider.ErrUnavailable) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("metadata server answers 403", func(t *testing.T) {
		metadata(t, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "forbidden", http.StatusForbidden) })
		if err := next(t); err == nil || errors.Is(err, keyprovider.ErrUnavailable) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("metadata server unreachable", func(t *testing.T) {
		t.Setenv("GCE_METADATA_HOST", "127.0.0.1:1")
		if err := next(t); !errors.Is(err, keyprovider.ErrUnavailable) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("metadata server never answers", func(t *testing.T) {
		done := make(chan struct{})
		t.Cleanup(func() { close(done) })
		metadata(t, func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
			case <-done:
			}
		})
		p, err := New(keyURI)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		start := time.Now()
		_, err = p.Next(ctx)
		if !errors.Is(err, keyprovider.ErrUnavailable) || time.Since(start) > 5*time.Second {
			t.Fatalf("err = %v after %v", err, time.Since(start))
		}
	})
	t.Run("credentials file is malformed", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "creds.json")
		if err := os.WriteFile(file, []byte(`{"type":"service_account","private_key":"-----BEGIN PRIVATE KEY-----S3CR3TMARKER`), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", file)
		err := next(t)
		if err == nil || errors.Is(err, keyprovider.ErrUnavailable) || strings.Contains(err.Error(), "S3CR3TMARKER") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("credentials file is missing", func(t *testing.T) {
		t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", filepath.Join(t.TempDir(), "absent.json"))
		if err := next(t); err == nil || errors.Is(err, keyprovider.ErrUnavailable) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("sign reads credentials before the first request", func(t *testing.T) {
		t.Setenv("GCE_METADATA_HOST", "127.0.0.1:1")
		p, _ := New(keyURI)
		k, _ := keyprovider.ExternalKey(&p256().PublicKey, keyURI+"/cryptoKeyVersions/1")
		sg := mustSigner(t, p, k)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := sg.SignContext(ctx, sha("x"), crypto.SHA256); !errors.Is(err, keyprovider.ErrUnavailable) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestNewRefusesURI(t *testing.T) {
	bad := []string{
		"",
		"gcpkms://",
		"gcpkms://projects",
		"awskms://projects/p/locations/l/keyRings/r/cryptoKeys/k",
		"GCPKMS://projects/p/locations/l/keyRings/r/cryptoKeys/k",
		"gcpkms://projects/p/locations/l/keyRings/r",
		"gcpkms://projects/p/locations/l/keyRings/r/cryptoKeys/",
		"gcpkms://projects//locations/l/keyRings/r/cryptoKeys/k",
		"gcpkms://projects/p/locations/l/keyRings/r/cryptoKeys/k/",
		"gcpkms://projects/p/locations/l/keyRings/r/cryptoKeys/k/extra",
		"gcpkms://projects/p/locations/l/keyRings/r/cryptoKeys/k/cryptoKeyVersions",
		"gcpkms://projects/p/locations/l/keyRings/r/cryptoKeys/k/cryptoKeyVersions/",
		"gcpkms://projects/p/locations/l/keyRings/r/cryptoKeys/k/cryptoKeyVersions/0",
		"gcpkms://projects/p/locations/l/keyRings/r/cryptoKeys/k/cryptoKeyVersions/01",
		"gcpkms://projects/p/locations/l/keyRings/r/cryptoKeys/k/cryptoKeyVersions/-1",
		"gcpkms://projects/p/locations/l/keyRings/r/cryptoKeys/k/cryptoKeyVersions/latest",
		"gcpkms://projects/p/locations/l/keyRings/r/cryptoKeys/k/cryptoKeyVersions/1/x",
		"gcpkms://projects/p/locations/l/keyRings/r/cryptoKeys/k/cryptoKeyVersions/99999999999999999999",
		"gcpkms://projects/p/locations/l/keyRings/r/cryptoKeys/k/versions/1",
		"gcpkms://projects/p/locations/l/keyRings/r/cryptoKeys/k?x=1",
		"gcpkms://projects/p/locations/l/keyRings/r/cryptoKeys/k#f",
		"gcpkms://projects/p/locations/l/keyRings/r/cryptoKeys/k%2Fx",
		"gcpkms://projects/p/locations/l/keyRings/r/cryptoKeys/k/../k",
		"gcpkms://projects/../locations/l/keyRings/r/cryptoKeys/k",
		"gcpkms://projects/p/locations/l/keyRings/r/cryptoKeys/k\n",
		"gcpkms://projects/p/locations/l/keyRings/r/cryptoKeys/k ",
		"gcpkms://us3r:p4ss@projects/p/locations/l/keyRings/r/cryptoKeys/k",
		"gcpkms://us3r@projects/p/locations/l/keyRings/r/cryptoKeys/k",
		"gcpkms://host:443/projects/p/locations/l/keyRings/r/cryptoKeys/k",
		"gcpkms:///projects/p/locations/l/keyRings/r/cryptoKeys/k",
		"gcpkms://projects/p@x/locations/l/keyRings/r/cryptoKeys/k",
	}
	for _, uri := range bad {
		p, err := New(uri)
		if err == nil || p != nil {
			t.Errorf("%q: accepted (%v, %v)", uri, p, err)
			continue
		}
		for _, leak := range []string{"us3r", "p4ss", "host:443"} {
			if strings.Contains(err.Error(), leak) {
				t.Errorf("%q: the error quotes %q: %v", uri, leak, err)
			}
		}
	}
	for _, uri := range []string{
		"gcpkms://projects/p/locations/global/keyRings/r/cryptoKeys/k",
		"gcpkms://projects/my-proj_1/locations/us-east1/keyRings/Ring_1-a/cryptoKeys/Key-1_a/cryptoKeyVersions/12",
		"gcpkms://projects/example.com:proj/locations/l/keyRings/r/cryptoKeys/k",
	} {
		if _, err := New(uri); err != nil {
			t.Errorf("%q refused: %v", uri, err)
		}
	}
}

func TestNewMakesNoRequestAndReadsNoEnvironment(t *testing.T) {
	// A bogus GOOGLE_APPLICATION_CREDENTIALS must not matter to New or Signer.
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "/nonexistent/creds.json")
	p, err := New(keyURI)
	if err != nil {
		t.Fatal(err)
	}
	k, _ := keyprovider.ExternalKey(&p256().PublicKey, keyURI+"/cryptoKeyVersions/3")
	if _, err := p.Signer(context.Background(), k); err != nil {
		t.Fatal(err)
	}
}

func TestErrorsDoNotQuoteTheURI(t *testing.T) {
	f := newFakeKMS(t)
	f.addKey(t)
	p := f.provider(t, keyURI)
	k := mustNext(t, p)
	f.fail("AsymmetricSign", status.Error(codes.PermissionDenied, "denied"))
	sg := mustSigner(t, p, k)
	_, err := sg.SignContext(context.Background(), sha("x"), crypto.SHA256)
	if err == nil || strings.Contains(err.Error(), "gcpkms://") || !strings.Contains(err.Error(), keyPath) {
		t.Fatalf("err = %v", err)
	}
}

func TestNextLeavesNoStateBehind(t *testing.T) {
	f := newFakeKMS(t)
	f.addKey(t)
	f.addVersion(2, kmspb.CryptoKeyVersion_ENABLED, kmspb.CryptoKeyVersion_EC_SIGN_P256_SHA256, p256())
	f.addVersion(3, kmspb.CryptoKeyVersion_ENABLED, kmspb.CryptoKeyVersion_EC_SIGN_P256_SHA256, p256())
	p := f.provider(t, keyURI)
	first := mustNext(t, p)
	if again := mustNext(t, p); again.KID != first.KID || again.Ref != first.Ref {
		t.Fatalf("a second Next differs: %+v vs %+v", again, first)
	}
	var wg sync.WaitGroup
	got := make(chan keyprovider.Key, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if k, err := p.Next(context.Background()); err == nil {
				got <- k
			} else {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	close(got)
	for k := range got {
		if k.KID != first.KID {
			t.Fatalf("overlapping Next returned %s, want %s", k.Ref, first.Ref)
		}
	}
	// A failure half-way (after the list, at the public key read) changes nothing.
	f.fail("GetPublicKey", status.Error(codes.Unavailable, "down"))
	if _, err := p.Next(context.Background()); !errors.Is(err, keyprovider.ErrUnavailable) {
		t.Fatal(err)
	}
	f.unfail("GetPublicKey")
	if after := mustNext(t, p); after.KID != first.KID {
		t.Fatal("a failed Next left state behind")
	}
	// Only read RPCs were ever made.
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		switch c {
		case "GetCryptoKey", "GetCryptoKeyVersion", "ListCryptoKeyVersions", "GetPublicKey":
		default:
			t.Fatalf("Next called %s", c)
		}
	}
	// And the fake's keys are untouched.
	if len(f.keys[keyPath].versions) != 3 {
		t.Fatal("the key set changed")
	}
}

func TestSignSurvivesAnEmptyAnswer(t *testing.T) {
	f := newFakeKMS(t)
	f.addKey(t)
	p := f.provider(t, keyURI)
	sg := mustSigner(t, p, mustNext(t, p))
	if _, err := sg.SignContext(context.Background(), sha("x"), crypto.SHA256); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.noSigCRC = true // sigstore dereferences SignatureCrc32C without a nil check
	f.mu.Unlock()
	_, err := sg.SignContext(context.Background(), sha("x"), crypto.SHA256)
	if err == nil || errors.Is(err, keyprovider.ErrUnavailable) || !strings.Contains(err.Error(), "panic") {
		t.Fatalf("err = %v", err)
	}
	waitSlots(t, 0)
}

func waitSlots(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for len(signSlots) != want && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(signSlots) != want {
		t.Fatalf("%d sign slots held, want %d", len(signSlots), want)
	}
}

func TestStatusTextIsBoundedAndRedacted(t *testing.T) {
	t.Run("long multibyte text", func(t *testing.T) {
		f := newFakeKMS(t)
		f.addKey(t)
		f.fail("GetCryptoKey", status.Error(codes.PermissionDenied, strings.Repeat("é", 3000)))
		_, err := f.provider(t, keyURI).Next(context.Background())
		if err == nil || !utf8.ValidString(err.Error()) || len(err.Error()) > 800 {
			t.Fatalf("len %d valid %v", len(err.Error()), utf8.ValidString(err.Error()))
		}
		// The provider's text is cut at 256 bytes without splitting a rune.
		if n := strings.Count(err.Error(), "é"); n > 128 || n < 100 {
			t.Fatalf("%d runes of provider text kept", n)
		}
	})
	t.Run("token and key text are redacted", func(t *testing.T) {
		const secret = "ya29.a0-SECRET-ACCESS-TOKEN"
		err := classify("reading", status.Error(codes.PermissionDenied, "bad bearer "+secret+" given"), secret)
		if strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), "[redacted]") {
			t.Fatalf("err = %v", err)
		}
		// A wrapped (non-status) error is redacted and cut as well.
		err = classify("reading", fmt.Errorf("odd %s %s", secret, strings.Repeat("x", 1000)), secret)
		if strings.Contains(err.Error(), secret) || len(err.Error()) > 600 {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestSignRefusesAnotherKeysSignature(t *testing.T) {
	f := newFakeKMS(t)
	f.addKey(t)
	p := f.provider(t, keyURI)
	sg := mustSigner(t, p, mustNext(t, p))
	f.mu.Lock()
	f.wrongKey = p256()
	f.mu.Unlock()
	_, err := sg.SignContext(context.Background(), sha("x"), crypto.SHA256)
	if err == nil || errors.Is(err, keyprovider.ErrUnavailable) || !strings.Contains(err.Error(), "does not verify") {
		t.Fatalf("err = %v", err)
	}
}

func TestHungSignsAreBounded(t *testing.T) {
	f := newFakeKMS(t)
	f.addKey(t)
	signSlots = make(chan struct{}, 2) // restored by newFakeKMS's cleanup
	p := f.provider(t, keyURI)
	sg := mustSigner(t, p, mustNext(t, p))
	// Sigstore's first read of the key version ignores the caller's context, so
	// a service that never answers pins a goroutine per sign until it ends.
	f.hang("GetCryptoKey")
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
	f.free() // the service answers at last: the slots drain
	waitSlots(t, 0)
	f.unhang()
	if _, err := sg.SignContext(context.Background(), sha("x"), crypto.SHA256); err != nil {
		t.Fatalf("sign after the drain: %v", err)
	}
}

func TestConcurrentSigns(t *testing.T) {
	f := newFakeKMS(t)
	f.addKey(t)
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

func TestNextAcceptsTheProjectNumber(t *testing.T) {
	for _, pinned := range []string{"", "/cryptoKeyVersions/1"} {
		f := newFakeKMS(t)
		f.addKey(t)
		f.answerProject = "123456789012"
		k := mustNext(t, f.provider(t, keyURI+pinned))
		// The pin is the requested path plus the number, not the service's name.
		if k.Ref != keyURI+"/cryptoKeyVersions/1" {
			t.Fatalf("pin %q: Ref = %q", pinned, k.Ref)
		}
		if f.count("GetPublicKey") != 1 {
			t.Fatalf("GetPublicKey calls = %d", f.count("GetPublicKey"))
		}
	}
}

func TestNextRefusesOddVersionNames(t *testing.T) {
	other := strings.Replace(keyPath, "/cryptoKeys/", "/cryptoKeys/x", 1)
	otherRing := strings.Replace(keyPath, "/keyRings/", "/keyRings/x", 1)
	cases := map[string]string{
		"non-numeric":        keyPath + "/cryptoKeyVersions/abc",
		"zero":               keyPath + "/cryptoKeyVersions/0",
		"non-canonical":      keyPath + "/cryptoKeyVersions/07",
		"another crypto key": other + "/cryptoKeyVersions/1",
		"another key ring":   otherRing + "/cryptoKeyVersions/1",
		"no project":         "locations" + strings.SplitN(keyPath, "/locations", 2)[1] + "/cryptoKeyVersions/1",
	}
	for name, answer := range cases {
		for _, pinned := range []string{"", "/cryptoKeyVersions/1"} {
			t.Run(name+pinned, func(t *testing.T) {
				f := newFakeKMS(t)
				f.addKey(t)
				f.rename = map[int]string{1: answer}
				expectRefusal(t, f.provider(t, keyURI+pinned), "")
			})
		}
	}
}
