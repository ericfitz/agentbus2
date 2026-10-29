// Package gcpkms is the external key provider over Google Cloud KMS (signing-
// keys plan Task 12; Eric 2026-10-27 decisions 1, 3 and 12). The private key
// never leaves Cloud KMS: Next resolves the configured key to one pinned key
// version, and the signer asks Cloud KMS to sign a SHA-256 digest with
// EC_SIGN_P256_SHA256 (ECDSA P-256, a DER signature).
//
// A key URI is gcpkms://projects/P/locations/L/keyRings/R/cryptoKeys/K or the
// same with /cryptoKeyVersions/V. Without a version Next pins the newest
// ENABLED version (the highest number); with one it pins that version, which
// must be ENABLED. A pinned key reference is the URI with
// /cryptoKeyVersions/V. Credentials come from Application Default
// Credentials. A URI is never printed: messages name the key resource.
//
// sigstore's kms/gcp module does not expose the version it resolves to, so
// Next reads the key with the Cloud KMS client directly; signing goes through
// sigstore's signer.
package gcpkms

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"cloud.google.com/go/auth"
	"cloud.google.com/go/auth/credentials"
	"cloud.google.com/go/compute/metadata"
	kms "cloud.google.com/go/kms/apiv1"
	"cloud.google.com/go/kms/apiv1/kmspb"
	siggcp "github.com/sigstore/sigstore/pkg/signature/kms/gcp"
	"github.com/sigstore/sigstore/pkg/signature/options"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ericfitz/agentbus2/internal/auth/keys"
	"github.com/ericfitz/agentbus2/internal/keyprovider"
)

// Name is the provider's name and its key URI scheme.
const Name = "gcpkms"

const scheme = Name + "://"

// refRE is the key URI grammar: sigstore's, tightened. Segments hold letters,
// digits and a few separators, so no user info, query, escape or "..".
// "versions" (which sigstore also accepts) is not an alias here.
var refRE = regexp.MustCompile(`^gcpkms://projects/([A-Za-z0-9][A-Za-z0-9._:-]{0,127})/locations/([A-Za-z0-9][A-Za-z0-9_-]{0,62})/keyRings/([A-Za-z0-9_-]{1,63})/cryptoKeys/([A-Za-z0-9_-]{1,63})(?:/cryptoKeyVersions/([1-9][0-9]{0,17}))?$`)

// cloudPlatform is the OAuth scope the credentials are asked for.
const cloudPlatform = "https://www.googleapis.com/auth/cloud-platform"

// signSlots bounds the sign requests that may be in flight. Sigstore's first
// read of the key version runs on a background context, so a service that
// never answers would strand one goroutine per bearer; at the bound a sign
// fails at once, retryably.
var signSlots = make(chan struct{}, 16)

// connector is how the package reaches Cloud KMS: extra client options and
// the credential step. Production uses defaultConnector; tests point opts at an
// in-process server and leave creds nil.
type connector struct {
	opts  []option.ClientOption
	creds func(ctx context.Context) (*auth.Credentials, error)
}

func defaultConnector() connector { return connector{creds: detectCredentials} }

// detectCredentials finds Application Default Credentials. The lookup has no
// context parameter, so it runs bounded by ctx (see within).
func detectCredentials(ctx context.Context) (*auth.Credentials, error) {
	return within(ctx, func() (*auth.Credentials, error) {
		return credentials.DetectDefault(&credentials.DetectOptions{Scopes: []string{cloudPlatform}})
	})
}

// within runs f and returns when f does or ctx ends, whichever is first. A call
// abandoned at ctx's end finishes in the background.
func within[T any](ctx context.Context, f func() (T, error)) (T, error) {
	type result struct {
		v   T
		err error
	}
	ch := make(chan result, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				ch <- result{err: fmt.Errorf("unexpected panic (%T)", r)}
			}
		}()
		v, err := f()
		ch <- result{v, err}
	}()
	select {
	case r := <-ch:
		return r.v, r.err
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	}
}

// clientOptions returns the options for a Cloud KMS client, the credentials
// when there are any and the secrets to keep out of messages. It reads the
// credentials and fetches a token under ctx, so a credential failure is typed
// (gRPC would flatten it into an Unauthenticated status).
func (c connector) clientOptions(ctx context.Context) ([]option.ClientOption, *auth.Credentials, error) {
	opts := append([]option.ClientOption(nil), c.opts...)
	if c.creds == nil {
		return opts, nil, nil
	}
	creds, err := c.creds(ctx)
	if err != nil {
		return nil, nil, classify("finding Application Default Credentials", err)
	}
	if creds == nil {
		return opts, nil, nil
	}
	if _, err := creds.Token(ctx); err != nil {
		return nil, nil, classify("fetching an access token", err)
	}
	return append(opts, option.WithAuthCredentials(creds)), creds, nil
}

// secretsOf is the access token creds currently holds, for redaction.
func secretsOf(ctx context.Context, creds *auth.Credentials) []string {
	if creds == nil {
		return nil
	}
	if tok, err := creds.Token(ctx); err == nil && tok != nil && tok.Value != "" {
		return []string{tok.Value}
	}
	return nil
}

// Provider implements keyprovider.Provider over one key URI.
type Provider struct {
	label   string // "key projects/.../cryptoKeys/K", for messages
	keyName string // projects/.../cryptoKeys/K
	version string // "" or the version the URI pins
	conn    connector
}

var _ keyprovider.Provider = (*Provider)(nil)

// New validates uri and returns a provider. It reads no credentials and makes
// no network call.
func New(uri string) (keyprovider.Provider, error) {
	p, err := newProvider(uri, defaultConnector())
	if err != nil {
		return nil, err // never a typed nil inside the interface
	}
	return p, nil
}

func newProvider(uri string, c connector) (*Provider, error) {
	m := refRE.FindStringSubmatch(uri)
	if m == nil || siggcp.ValidReference(uri) != nil {
		// Nothing of the URI is printed: it may carry user info.
		return nil, errors.New("gcpkms: the key URI must be gcpkms://projects/PROJECT/locations/LOCATION/keyRings/RING/cryptoKeys/KEY, optionally followed by /cryptoKeyVersions/VERSION (a number of 1 or more), with no user info, query or other part")
	}
	name := "projects/" + m[1] + "/locations/" + m[2] + "/keyRings/" + m[3] + "/cryptoKeys/" + m[4]
	return &Provider{label: "key " + name, keyName: name, version: m[5], conn: c}, nil
}

// Name implements keyprovider.Provider.
func (p *Provider) Name() string { return Name }

// Next resolves the URI to one key version: the pinned version, or else the
// newest ENABLED one, and reads its public key. It only reads, so a repeated,
// overlapping or half-failed Next leaves nothing behind. ctx bounds every
// request; the caller (signingkeys) also bounds the whole call.
func (p *Provider) Next(ctx context.Context) (keyprovider.Key, error) {
	fail := func(what string, err error, secrets []string) error {
		if cerr := ctx.Err(); cerr != nil {
			err = cerr // the call ended because the caller's context did
		}
		return classify(p.label+": "+what, err, secrets...)
	}
	if err := ctx.Err(); err != nil {
		return keyprovider.Key{}, fail("reading the key", err, nil)
	}
	opts, creds, err := p.conn.clientOptions(ctx)
	if err != nil {
		return keyprovider.Key{}, fmt.Errorf("gcpkms: %s: %w", p.label, err)
	}
	secrets := secretsOf(ctx, creds)
	c, err := kms.NewKeyManagementClient(ctx, opts...)
	if err != nil {
		return keyprovider.Key{}, fail("building the Cloud KMS client", err, secrets)
	}
	defer func() { _ = c.Close() }()
	// No client-side retry or per-call timeout: a failure is reported at once
	// and ctx is the only bound.
	c.CallOptions = &kms.KeyManagementCallOptions{}

	key, err := c.GetCryptoKey(ctx, &kmspb.GetCryptoKeyRequest{Name: p.keyName})
	if err != nil {
		return keyprovider.Key{}, fail("reading the crypto key", err, secrets)
	}
	if key.GetPurpose() != kmspb.CryptoKey_ASYMMETRIC_SIGN {
		return keyprovider.Key{}, fmt.Errorf("gcpkms: %s: the key purpose is %s; it must be ASYMMETRIC_SIGN", p.label, key.GetPurpose())
	}
	kv, id, err := p.resolve(ctx, c, fail, secrets)
	if err != nil {
		return keyprovider.Key{}, err
	}
	// The pinned name is the requested path plus the version number, never the
	// service's spelling of it (which may carry the project number).
	pinned := p.keyName + "/cryptoKeyVersions/" + strconv.FormatUint(id, 10)
	if kv.GetAlgorithm() != kmspb.CryptoKeyVersion_EC_SIGN_P256_SHA256 {
		return keyprovider.Key{}, fmt.Errorf("gcpkms: %s: the version's algorithm is %s; only EC_SIGN_P256_SHA256 (ECDSA P-256, ES256) is supported", p.label, kv.GetAlgorithm())
	}
	pk, err := c.GetPublicKey(ctx, &kmspb.GetPublicKeyRequest{Name: pinned})
	if err != nil {
		return keyprovider.Key{}, fail("reading the public key", err, secrets)
	}
	if pk.GetAlgorithm() != kmspb.CryptoKeyVersion_EC_SIGN_P256_SHA256 {
		return keyprovider.Key{}, fmt.Errorf("gcpkms: %s: the public key's algorithm is %s; only EC_SIGN_P256_SHA256 (ECDSA P-256, ES256) is supported", p.label, pk.GetAlgorithm())
	}
	if want := pk.GetPemCrc32C(); want != nil && int64(crc32.Checksum([]byte(pk.GetPem()), crc32.MakeTable(crc32.Castagnoli))) != want.GetValue() {
		return keyprovider.Key{}, fmt.Errorf("gcpkms: %s: %w: the public key was corrupted in transit", p.label, keyprovider.ErrUnavailable)
	}
	block, _ := pem.Decode([]byte(pk.GetPem()))
	if block == nil {
		return keyprovider.Key{}, fmt.Errorf("gcpkms: %s: the public key is not PEM", p.label)
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return keyprovider.Key{}, fmt.Errorf("gcpkms: %s: parsing the public key: %w", p.label, err)
	}
	k, err := keyprovider.ExternalKey(pub, scheme+pinned)
	if err != nil {
		return keyprovider.Key{}, fmt.Errorf("gcpkms: %s: %w", p.label, err)
	}
	return k, nil
}

// resolve returns the version Next pins (the URI's, which must be ENABLED, or
// the ENABLED one with the highest number) and its number. A returned name is
// checked from its "locations/" segment on: Cloud KMS may spell the project as
// its number where the request used the ID, and the call is already scoped to
// the requested key.
func (p *Provider) resolve(ctx context.Context, c *kms.KeyManagementClient, fail func(string, error, []string) error, secrets []string) (*kmspb.CryptoKeyVersion, uint64, error) {
	prefix := p.keyName + "/cryptoKeyVersions/"
	if p.version != "" {
		kv, err := c.GetCryptoKeyVersion(ctx, &kmspb.GetCryptoKeyVersionRequest{Name: prefix + p.version})
		if err != nil {
			return nil, 0, fail("reading the key version", err, secrets)
		}
		id, err := versionID(kv.GetName(), prefix)
		if err != nil || strconv.FormatUint(id, 10) != p.version {
			return nil, 0, fmt.Errorf("gcpkms: %s: Cloud KMS answered for another key version", p.label)
		}
		if kv.GetState() != kmspb.CryptoKeyVersion_ENABLED {
			return nil, 0, fmt.Errorf("gcpkms: %s: version %s is %s, not ENABLED", p.label, p.version, kv.GetState())
		}
		return kv, id, nil
	}
	// The service's own ordering is by name, which sorts "9" after "10": read
	// every ENABLED version and take the highest number.
	it := c.ListCryptoKeyVersions(ctx, &kmspb.ListCryptoKeyVersionsRequest{Parent: p.keyName, Filter: "state=ENABLED"})
	var best *kmspb.CryptoKeyVersion
	var bestID uint64
	for {
		kv, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, 0, fail("listing the key versions", err, secrets)
		}
		if kv.GetState() != kmspb.CryptoKeyVersion_ENABLED {
			continue
		}
		id, err := versionID(kv.GetName(), prefix)
		if err != nil {
			return nil, 0, fmt.Errorf("gcpkms: %s: %w", p.label, err)
		}
		if best == nil || id > bestID {
			best, bestID = kv, id
		}
	}
	if best == nil {
		return nil, 0, fmt.Errorf("gcpkms: %s: the key has no ENABLED version", p.label)
	}
	return best, bestID, nil
}

// tail is name from its "locations/" segment on, without the project.
func tail(name string) string {
	_, rest, _ := strings.Cut(name, "/locations/")
	return rest
}

// versionID is the number at the end of a version name of the key whose
// requested name prefix is given; the project segment is not compared.
func versionID(name, prefix string) (uint64, error) {
	rest, ok := strings.CutPrefix(tail(name), tail(prefix))
	if !ok || !strings.HasPrefix(name, "projects/") {
		return 0, errors.New("the answer is a version of another key")
	}
	id, err := strconv.ParseUint(rest, 10, 63)
	if err != nil || id == 0 || rest != strconv.FormatUint(id, 10) {
		return 0, errors.New("the answer's version name does not end in a version number")
	}
	return id, nil
}

// Signer opens k for signing. It makes no network call and reads no
// credentials: the sigstore signer is built on the first SignContext.
func (p *Provider) Signer(_ context.Context, k keyprovider.Key) (keyprovider.Signer, error) {
	if k.Algorithm != keys.ES256 {
		return nil, fmt.Errorf("gcpkms: key %s: algorithm %q, only ES256 is supported", k.KID, k.Algorithm)
	}
	pub, ok := k.Public.(*ecdsa.PublicKey)
	if !ok || pub == nil {
		return nil, fmt.Errorf("gcpkms: key %s has no ECDSA public key", k.KID)
	}
	if alg, err := keys.AlgorithmOf(pub); err != nil || alg != keys.ES256 {
		return nil, fmt.Errorf("gcpkms: key %s: the public key is not ECDSA P-256", k.KID)
	}
	m := refRE.FindStringSubmatch(k.Ref)
	if m == nil || m[5] == "" {
		return nil, fmt.Errorf("gcpkms: key %s: the key reference must be a gcpkms key version URI (.../cryptoKeys/KEY/cryptoKeyVersions/VERSION)", k.KID)
	}
	name := "projects/" + m[1] + "/locations/" + m[2] + "/keyRings/" + m[3] + "/cryptoKeys/" + m[4] + "/cryptoKeyVersions/" + m[5]
	return &signer{label: "key " + name, ref: scheme + name, pub: pub, conn: p.conn}, nil
}

// signer signs through sigstore's SignerVerifier for one pinned version, built
// on first use. A failed build is not remembered.
type signer struct {
	label string
	ref   string
	pub   *ecdsa.PublicKey
	conn  connector

	mu    sync.Mutex
	sv    *siggcp.SignerVerifier
	creds *auth.Credentials
}

// Public is the key the row holds; it asks Cloud KMS nothing.
func (s *signer) Public() crypto.PublicKey { return s.pub }

// load builds the sigstore signer once; under the lock, so overlapping first
// signs build one. It returns the credentials the client uses, for the
// token check before each sign.
func (s *signer) load(ctx context.Context) (*siggcp.SignerVerifier, *auth.Credentials, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sv != nil {
		return s.sv, s.creds, nil
	}
	opts, creds, err := s.conn.clientOptions(ctx)
	if err != nil {
		return nil, nil, err
	}
	// Building the client dials lazily and reads no network; its context is
	// kept by the client, so it must outlive this request.
	sv, err := siggcp.LoadSignerVerifier(context.WithoutCancel(ctx), s.ref, opts...)
	if err != nil {
		return nil, nil, classify("building the Cloud KMS signer", err, secretsOf(ctx, creds)...)
	}
	s.sv, s.creds = sv, creds
	return sv, creds, nil
}

type signResult struct {
	sig []byte
	err error
}

// SignContext asks Cloud KMS to sign digest, a SHA-256 digest, with the pinned
// version and returns the ASN.1 DER signature. Sigstore's first read of the key
// version runs on a background context, so the work runs in a goroutine and the
// call returns when ctx ends.
func (s *signer) SignContext(ctx context.Context, digest []byte, opts crypto.SignerOpts) ([]byte, error) {
	if opts == nil || opts.HashFunc() != crypto.SHA256 {
		return nil, errors.New("gcpkms: only SHA-256 digests are signed")
	}
	if len(digest) != crypto.SHA256.Size() {
		return nil, fmt.Errorf("gcpkms: a SHA-256 digest is %d bytes, got %d", crypto.SHA256.Size(), len(digest))
	}
	if err := ctx.Err(); err != nil {
		return nil, classify(s.label+": signing", err)
	}
	slots := signSlots
	select {
	case slots <- struct{}{}:
	default:
		return nil, fmt.Errorf("gcpkms: %s: %w: too many sign requests are waiting on Cloud KMS", s.label, keyprovider.ErrUnavailable)
	}
	ch := make(chan signResult, 1)
	go func() {
		defer func() { <-slots }()
		// Sigstore dereferences the reply's SignatureCrc32C without a check: an
		// incomplete reply panics. A panic in this goroutine would end the server.
		defer func() {
			if r := recover(); r != nil {
				ch <- signResult{nil, fmt.Errorf("gcpkms: %s: unexpected panic in the Cloud KMS signer (%T), most likely an incomplete reply", s.label, r)}
			}
		}()
		sig, err := s.sign(ctx, digest)
		ch <- signResult{sig, err}
	}()
	select {
	case r := <-ch:
		return r.sig, r.err
	case <-ctx.Done():
		return nil, classify(s.label+": signing", ctx.Err())
	}
}

func (s *signer) sign(ctx context.Context, digest []byte) ([]byte, error) {
	fail := func(what string, err error, secrets []string) error {
		if cerr := ctx.Err(); cerr != nil {
			err = cerr
		}
		return classify(s.label+": "+what, err, secrets...)
	}
	sv, creds, err := s.load(ctx)
	if err != nil {
		return nil, fmt.Errorf("gcpkms: %s: %w", s.label, err)
	}
	if creds != nil {
		// A cached token costs nothing; a stale one is refreshed here, under ctx,
		// where a failure keeps its type.
		if _, err := creds.Token(ctx); err != nil {
			return nil, fail("fetching an access token", err, nil)
		}
	}
	secrets := secretsOf(ctx, creds)
	sig, err := sv.SignMessage(nil,
		options.WithDigest(digest),
		options.WithContext(ctx),
		options.WithCryptoSignerOpts(crypto.SHA256))
	if err != nil {
		return nil, fail("signing", err, secrets)
	}
	// A pinned version name is unique, but the check costs one verification and
	// makes a wrong answer loud here rather than at the first use of a bearer.
	if !ecdsa.VerifyASN1(s.pub, digest, sig) {
		return nil, fmt.Errorf("gcpkms: %s: Cloud KMS's signature does not verify under the key the row holds; the key version may have been replaced or the reply is not from Cloud KMS", s.label)
	}
	return sig, nil
}

// maxProviderText caps the provider-supplied text kept in an error.
const maxProviderText = 256

// capText redacts every secret from text and cuts it to maxProviderText bytes
// without splitting a rune.
func capText(text string, secrets []string) string {
	for _, sec := range secrets {
		if sec != "" {
			text = strings.ReplaceAll(text, sec, "[redacted]")
		}
	}
	if len(text) > maxProviderText {
		text = strings.ToValidUTF8(text[:maxProviderText], "") + "..."
	}
	return text
}

// retryable are the gRPC codes that mean the service itself failed.
var retryable = map[codes.Code]bool{
	codes.Unavailable:       true,
	codes.DeadlineExceeded:  true,
	codes.ResourceExhausted: true,
	codes.Internal:          true,
	codes.Unknown:           true,
}

// classify turns a failure into an error that names what failed. A failure of
// the service itself (unreachable, out of time, throttling, an internal error,
// a credential fetch that hit the network or a timeout) wraps
// keyprovider.ErrUnavailable; a refusal (permission, a missing key, a bad
// request) and a canceled context do not. It decides by error type and gRPC
// status code, never by message text. Provider text is redacted of secrets and
// capped; nothing here prints a URI.
//
// A DeadlineExceeded status also wraps context.DeadlineExceeded: the call's
// deadline is the caller's, sent to the service as grpc-timeout, and the
// server enforces its copy itself (grpc-go ends the stream when it fires), so
// the answer can arrive before the caller's context is marked done. The
// status is the deadline whichever side noticed it first.
func classify(what string, err error, secrets ...string) error {
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("gcpkms: %s: %w", what, context.Canceled)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("gcpkms: %s: %w: %w", what, keyprovider.ErrUnavailable, context.DeadlineExceeded)
	}
	var gs interface{ GRPCStatus() *status.Status }
	if errors.As(err, &gs) {
		if st := gs.GRPCStatus(); st != nil {
			if st.Code() == codes.Canceled {
				return fmt.Errorf("gcpkms: %s: %w", what, context.Canceled)
			}
			text := fmt.Sprintf("Cloud KMS answered %s", st.Code())
			if m := st.Message(); m != "" {
				text += ": " + capText(m, secrets)
			}
			if st.Code() == codes.DeadlineExceeded {
				return fmt.Errorf("gcpkms: %s: %w: %w: %s", what, keyprovider.ErrUnavailable, context.DeadlineExceeded, text)
			}
			if retryable[st.Code()] {
				return fmt.Errorf("gcpkms: %s: %w: %s", what, keyprovider.ErrUnavailable, text)
			}
			return fmt.Errorf("gcpkms: %s: %s", what, text)
		}
	}
	text := capText(err.Error(), secrets)
	if transient(err) {
		return fmt.Errorf("gcpkms: %s: %w: %s", what, keyprovider.ErrUnavailable, text)
	}
	return fmt.Errorf("gcpkms: %s: %s", what, text)
}

// transient reports a failure of the network or of a service on the way
// (credential endpoints included), decided by type. net.Error alone is too
// broad: every syscall.Errno satisfies it, a missing file included.
func transient(err error) bool {
	var op *net.OpError
	var dns *net.DNSError
	var md *metadata.Error
	var ae *auth.Error
	var to interface{ Timeout() bool }
	switch {
	case errors.As(err, &op), errors.As(err, &dns):
		return true
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return true
	case errors.As(err, &to) && to.Timeout():
		return true
	case errors.As(err, &md):
		return md.Code == http.StatusTooManyRequests || md.Code == http.StatusRequestTimeout || md.Code >= 500
	case errors.As(err, &ae):
		return ae.Temporary()
	}
	return false
}
