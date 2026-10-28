// Package awskms is the external key provider over AWS KMS, a thin adapter on
// sigstore's kms/aws module (signing-keys plan Task 10; Eric 2026-10-27
// decisions 1, 3 and 12). The private key never leaves KMS: Next resolves the
// configured key URI to one pinned key, and the signer asks KMS to sign a
// SHA-256 digest with ECDSA P-256.
//
// A key URI is awskms://[endpoint]/{key-id|key-ARN|alias/NAME|alias-ARN}. The
// endpoint is host[:port] and is reached over https; leave it empty for the
// regional AWS endpoint. Credentials and region come from the SDK's default
// chain. A URI is never printed: messages name the key with
// keyprovider.KeyName.
package awskms

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	sigaws "github.com/sigstore/sigstore/pkg/signature/kms/aws"
	"github.com/sigstore/sigstore/pkg/signature/options"

	"github.com/ericfitz/agentbus2/internal/auth/keys"
	"github.com/ericfitz/agentbus2/internal/keyprovider"
)

// Name is the provider's name and its key URI scheme.
const Name = "awskms"

// loadOpts are SDK config options; tests pass an HTTP client and static
// credentials, production passes none (the default credential chain).
type loadOpts []func(*config.LoadOptions) error

// Provider implements keyprovider.Provider over one key URI.
type Provider struct {
	uri      string
	endpoint string
	keyID    string // the id, ARN, alias name or alias ARN the URI names
	opts     loadOpts

	mu     sync.Mutex
	client *kms.Client // built on first use, kept once it exists
}

var _ keyprovider.Provider = (*Provider)(nil)

// New validates uri against the module's own grammar and returns a provider.
// It makes no network call and reads no credentials.
func New(uri string) (keyprovider.Provider, error) {
	p, err := newProvider(uri)
	if err != nil {
		return nil, err // never a typed nil inside the interface
	}
	return p, nil
}

func newProvider(uri string, opts ...func(*config.LoadOptions) error) (*Provider, error) {
	if err := sigaws.ValidReference(uri); err != nil {
		return nil, fmt.Errorf("awskms: %s: %w", describeURI(uri), err)
	}
	endpoint, keyID, _, err := sigaws.ParseReference(uri)
	if err != nil {
		return nil, fmt.Errorf("awskms: %s: %w", describeURI(uri), err)
	}
	if strings.Contains(endpoint, "@") {
		// The endpoint is host[:port]; user info would be sent to nobody and
		// printed by nothing, so it can only be a mistake or a leaked secret.
		return nil, errors.New("awskms: the endpoint must be host[:port] with no user info")
	}
	return &Provider{uri: uri, endpoint: endpoint, keyID: keyID, opts: opts}, nil
}

// describeURI names a URI in an error without printing it.
func describeURI(uri string) string {
	if n := keyprovider.KeyName(uri); n != "" {
		return "key " + n
	}
	return "key URI"
}

// Name implements keyprovider.Provider.
func (p *Provider) Name() string { return Name }

func (p *Provider) kmsClient(ctx context.Context) (*kms.Client, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.client != nil {
		return p.client, nil
	}
	cfg, err := config.LoadDefaultConfig(ctx, p.opts...)
	if err != nil {
		return nil, err // Next adds the "awskms: loading AWS config" prefix
	}
	var fns []func(*kms.Options)
	if p.endpoint != "" {
		fns = append(fns, func(o *kms.Options) { o.BaseEndpoint = aws.String("https://" + p.endpoint) })
	}
	p.client = kms.NewFromConfig(cfg, fns...)
	return p.client, nil
}

// Next resolves the URI to one key: DescribeKey on what the URI names (a key
// id, an alias, either as an ARN), then GetPublicKey on the key's ARN, so an
// alias repointed between the two calls cannot pair one key's metadata with
// another's public key. The pinned ref is the URI with the key ARN in place of
// whatever the URI named.
func (p *Provider) Next(ctx context.Context) (keyprovider.Key, error) {
	name := describeURI(p.uri)
	c, err := p.kmsClient(ctx)
	if err != nil {
		return keyprovider.Key{}, classify("awskms: loading AWS config", err)
	}
	d, err := c.DescribeKey(ctx, &kms.DescribeKeyInput{KeyId: aws.String(p.keyID)})
	if err != nil {
		return keyprovider.Key{}, classify("awskms: "+name+": describe key", err)
	}
	md := d.KeyMetadata
	if md == nil || md.Arn == nil || *md.Arn == "" {
		return keyprovider.Key{}, fmt.Errorf("awskms: %s: KMS returned no key ARN", name)
	}
	if md.KeyUsage != types.KeyUsageTypeSignVerify {
		return keyprovider.Key{}, fmt.Errorf("awskms: %s: the key usage is %q, not SIGN_VERIFY", name, md.KeyUsage)
	}
	if md.KeyState != types.KeyStateEnabled {
		return keyprovider.Key{}, fmt.Errorf("awskms: %s: the key state is %s, not Enabled", name, md.KeyState)
	}
	ref := "awskms://" + p.endpoint + "/" + *md.Arn
	if err := sigaws.ValidReference(ref); err != nil {
		return keyprovider.Key{}, fmt.Errorf("awskms: %s: the key ARN is not one this build can sign with: %w", name, err)
	}
	pk, err := c.GetPublicKey(ctx, &kms.GetPublicKeyInput{KeyId: md.Arn})
	if err != nil {
		return keyprovider.Key{}, classify("awskms: "+name+": get public key", err)
	}
	pub, err := x509.ParsePKIXPublicKey(pk.PublicKey)
	if err != nil {
		return keyprovider.Key{}, fmt.Errorf("awskms: %s: parsing the public key: %w", name, err)
	}
	k, err := keyprovider.ExternalKey(pub, ref)
	if err != nil {
		return keyprovider.Key{}, fmt.Errorf("awskms: %s: %w", name, err)
	}
	return k, nil
}

// Signer opens k for signing. It makes no network call: the sigstore signer is
// built on the first SignContext.
func (p *Provider) Signer(_ context.Context, k keyprovider.Key) (keyprovider.Signer, error) {
	if k.Algorithm != keys.ES256 {
		return nil, fmt.Errorf("awskms: key %s: algorithm %q, only ES256 is supported", k.KID, k.Algorithm)
	}
	pub, ok := k.Public.(*ecdsa.PublicKey)
	if !ok || pub == nil {
		return nil, fmt.Errorf("awskms: key %s has no ECDSA public key", k.KID)
	}
	if alg, err := keys.AlgorithmOf(pub); err != nil || alg != keys.ES256 {
		return nil, fmt.Errorf("awskms: key %s: the public key is not ECDSA P-256", k.KID)
	}
	if !strings.HasPrefix(k.Ref, Name+"://") {
		return nil, fmt.Errorf("awskms: key %s: the key reference is not an %s URI", k.KID, Name)
	}
	if err := sigaws.ValidReference(k.Ref); err != nil {
		return nil, fmt.Errorf("awskms: key %s: %s: %w", k.KID, describeURI(k.Ref), err)
	}
	return &signer{ref: k.Ref, pub: pub, opts: p.opts}, nil
}

// signer signs through sigstore's SignerVerifier for the pinned ref, built on
// first use. A failed build is not remembered.
type signer struct {
	ref  string
	pub  *ecdsa.PublicKey
	opts loadOpts

	mu sync.Mutex
	sv *sigaws.SignerVerifier
}

// Public is the key the row holds; it asks KMS nothing.
func (s *signer) Public() crypto.PublicKey { return s.pub }

func (s *signer) load(ctx context.Context) (*sigaws.SignerVerifier, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sv != nil {
		return s.sv, nil
	}
	sv, err := sigaws.LoadSignerVerifier(ctx, s.ref, s.opts...)
	if err != nil {
		return nil, err
	}
	s.sv = sv
	return sv, nil
}

// SignContext asks KMS to sign digest, a SHA-256 digest, with ECDSA_SHA_256
// and returns the ASN.1 DER signature. ctx reaches every KMS request.
func (s *signer) SignContext(ctx context.Context, digest []byte, opts crypto.SignerOpts) ([]byte, error) {
	if opts == nil || opts.HashFunc() != crypto.SHA256 {
		return nil, errors.New("awskms: only SHA-256 digests are signed")
	}
	if len(digest) != crypto.SHA256.Size() {
		return nil, fmt.Errorf("awskms: a SHA-256 digest is %d bytes, got %d", crypto.SHA256.Size(), len(digest))
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sv, err := s.load(ctx)
	if err != nil {
		return nil, classify("awskms: "+describeURI(s.ref), err)
	}
	sig, err := sv.SignMessage(nil, options.WithDigest(digest), options.WithContext(ctx), options.WithCryptoSignerOpts(crypto.SHA256))
	if err != nil {
		return nil, classify("awskms: "+describeURI(s.ref), err)
	}
	return sig, nil
}

// throttleCodes are the KMS and generic AWS error codes that mean "try later"
// rather than "this request is wrong". LimitExceededException is absent on
// purpose: it is a quota refusal (too many grants or aliases), and retrying
// does not clear it.
var throttleCodes = map[string]bool{
	"ThrottlingException":                    true,
	"Throttling":                             true,
	"ThrottledException":                     true,
	"RequestLimitExceeded":                   true,
	"RequestThrottled":                       true,
	"TooManyRequestsException":               true,
	"DependencyTimeoutException":             true,
	"KMSInternalException":                   true,
	"InternalFailure":                        true,
	"ServiceUnavailable":                     true,
	"ServiceUnavailableException":            true,
	"RequestTimeout":                         true,
	"RequestTimeoutException":                true,
	"ProvisionedThroughputExceededException": true,
	"BandwidthLimitExceeded":                 true,
	"TransactionInProgressException":         true,
	"EC2ThrottledException":                  true,
	"SlowDown":                               true,
	"PriorRequestNotComplete":                true,
	"InternalError":                          true,
	"InternalServerError":                    true,
}

// maxProviderText caps the AWS-supplied text kept in an error.
const maxProviderText = 256

// capText redacts the AWS secrets in the environment from text and cuts it to
// maxProviderText bytes without splitting a rune. The SDK does not put a secret
// in an error today; this holds if a proxy or endpoint echoes one.
func capText(text string) string {
	for _, name := range []string{"AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN"} {
		if sec := os.Getenv(name); sec != "" {
			text = strings.ReplaceAll(text, sec, "[redacted]")
		}
	}
	if len(text) > maxProviderText {
		text = strings.ToValidUTF8(text[:maxProviderText], "") + "..."
	}
	return text
}

// providerError is an SDK failure with a redacted, bounded message. Is and As
// still reach the original chain (and keyprovider.ErrUnavailable when marked),
// so classification by type is unchanged.
type providerError struct {
	msg  string
	errs []error
}

func (e *providerError) Error() string   { return e.msg }
func (e *providerError) Unwrap() []error { return e.errs }

// classify names what failed (prefix) and marks an error that is the key
// service's own failure (unreachable, timing out, throttling, 5xx) with
// keyprovider.ErrUnavailable so a rotate answers a retryable `unavailable`. A
// refusal (bad credentials, a missing or disabled key, a bad request) and a
// canceled context are not marked. The AWS text is redacted and capped; the
// original error stays in the chain.
func classify(prefix string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, keyprovider.ErrUnavailable) {
		return err
	}
	msg := prefix + ": " + capText(err.Error())
	if !errors.Is(err, context.Canceled) && unavailable(err) {
		return &providerError{msg: keyprovider.ErrUnavailable.Error() + ": " + msg, errs: []error{keyprovider.ErrUnavailable, err}}
	}
	return &providerError{msg: msg, errs: []error{err}}
}

func unavailable(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	// Credential failures need no case of their own: one caused by a network
	// or timeout error (the instance metadata service, a token endpoint) is a
	// net.Error or a deadline below and counts as an outage; any other (no
	// provider, invalid or expired credentials) matches nothing and is a refusal.
	var api smithy.APIError
	if errors.As(err, &api) && throttleCodes[api.ErrorCode()] {
		return true
	}
	var resp *smithyhttp.ResponseError
	if errors.As(err, &resp) && resp.HTTPStatusCode() != 0 {
		st := resp.HTTPStatusCode()
		return st == 429 || st >= 500
	}
	// net.Error alone is too broad: every syscall.Errno satisfies it, a missing
	// credentials file (a wrapped fs.PathError) included. Decide by type.
	var op *net.OpError
	var dns *net.DNSError
	var to interface{ Timeout() bool }
	switch {
	case errors.As(err, &op), errors.As(err, &dns):
		return true
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return true
	case errors.As(err, &to) && to.Timeout():
		return true
	}
	return false
}
