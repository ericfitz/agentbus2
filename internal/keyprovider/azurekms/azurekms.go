// Package azurekms is the external key provider over Azure Key Vault (signing-
// keys plan Task 13; Eric 2026-10-27 decisions 1, 3 and 12). The private key
// never leaves Key Vault: Next resolves the configured key to one pinned key
// version, and the signer asks Key Vault to sign a SHA-256 digest with ES256
// (ECDSA P-256; sigstore converts Key Vault's r||s answer to DER).
//
// A key URI is azurekms://VAULT-HOST/KEY or the same with /VERSION, where
// VAULT-HOST is the vault's DNS name (NAME.vault.azure.net, or the managed HSM,
// US Government or China equivalent) and VERSION is the 32-digit hex version
// Key Vault assigns. Without a version Next pins the key's current version;
// with one it pins that version. A pinned key reference is
// azurekms://VAULT-HOST/KEY/VERSION. Credentials come from the Azure SDK's
// default credential chain. A URI is never printed: messages name the key.
//
// sigstore's kms/azure module builds its own credential and client and does not
// expose the version it resolves to, so Next reads the key with its own
// azkeys client (whose transport the tests replace); signing goes through
// sigstore's signer by the pinned reference, behind the signFunc seam.
package azurekms

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/azkeys"
	sigazure "github.com/sigstore/sigstore/pkg/signature/kms/azure"
	"github.com/sigstore/sigstore/pkg/signature/options"

	"github.com/ericfitz/agentbus2/internal/auth/keys"
	"github.com/ericfitz/agentbus2/internal/keyprovider"
)

// Name is the provider's name and its key URI scheme.
const Name = "azurekms"

const scheme = Name + "://"

// refRE is the key URI grammar: sigstore's, tightened. The host must be a Key
// Vault or managed HSM name (3 to 24 letters, digits and hyphens under a known
// suffix), so no user info, port, address or other host is accepted, and the
// version must be the 32 lowercase hex digits Key Vault assigns.
var refRE = regexp.MustCompile(`^azurekms://([A-Za-z0-9][A-Za-z0-9-]{1,22}[A-Za-z0-9]\.(?:vault\.azure\.net|vault\.azure\.cn|vault\.usgovcloudapi\.net|vault\.microsoftazure\.de|managedhsm\.azure\.net|managedhsm\.azure\.cn|managedhsm\.usgovcloudapi\.net))/([A-Za-z0-9][A-Za-z0-9-]{0,126})(?:/([0-9a-f]{32}))?$`)

var versionRE = regexp.MustCompile(`^[0-9a-f]{32}$`)

// signSlots bounds the sign requests that may be in flight. A sign that does not
// return (a credential step that is slow to give up) would strand a goroutine
// per bearer; at the bound a sign fails at once, retryably.
var signSlots = make(chan struct{}, 16)

// signFunc signs digest, a SHA-256 digest, with the key version ref names
// (azurekms://HOST/KEY/VERSION) and returns an ASN.1 DER signature. Production
// uses sigstore's Azure signer; unit tests replace it, because that signer
// builds its own credential and client and cannot be pointed at a fake.
type signFunc func(ctx context.Context, ref string, digest []byte) ([]byte, error)

// connector is how the package reaches Key Vault. Production uses
// defaultConnector; tests set endpoint, transport and cred to a fake.
type connector struct {
	endpoint  string                                                    // replaces https://HOST/ when set
	transport policy.Transporter                                        // nil: the SDK's default
	cred      func(ctx context.Context) (azcore.TokenCredential, error) // nil: the default chain
	sign      signFunc                                                  // nil: sigstore
	now       func() time.Time
	// skipChallengeCheck turns off the SDK's check that the authentication
	// challenge names the vault's own domain. Tests only: a fake is not a vault.
	skipChallengeCheck bool
}

func defaultConnector() connector { return connector{now: time.Now} }

// cloudFor is the cloud sigstore's Azure module selects from AZURE_ENVIRONMENT:
// it picks the authority the credential signs in at. Unknown names are the
// public cloud, as there.
func cloudFor(env string) cloud.Configuration {
	switch env {
	case "AZUREUSGOVERNMENT", "AZUREUSGOVERNMENTCLOUD":
		return cloud.AzureGovernment
	case "AZURECHINACLOUD":
		return cloud.AzureChina
	default:
		return cloud.AzurePublic
	}
}

// defaultCredential is the Azure default credential chain. Building it reads the
// environment and files but makes no network call.
func defaultCredential(context.Context) (azcore.TokenCredential, error) {
	return azidentity.NewDefaultAzureCredential(&azidentity.DefaultAzureCredentialOptions{
		ClientOptions: azcore.ClientOptions{Cloud: cloudFor(os.Getenv("AZURE_ENVIRONMENT"))},
	})
}

// recorder remembers the access tokens a credential hands out, so they can be
// kept out of messages.
type recorder struct {
	azcore.TokenCredential
	mu   sync.Mutex
	seen []string
}

func (r *recorder) GetToken(ctx context.Context, opts policy.TokenRequestOptions) (azcore.AccessToken, error) {
	t, err := r.TokenCredential.GetToken(ctx, opts)
	if err == nil && t.Token != "" {
		r.mu.Lock()
		r.seen = append(r.seen, t.Token)
		r.mu.Unlock()
	}
	return t, err
}

func (r *recorder) secrets() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append(envSecrets(), r.seen...)
}

// envSecrets are the credential values the environment holds.
func envSecrets() []string {
	var out []string
	for _, k := range []string{"AZURE_CLIENT_SECRET", "AZURE_CLIENT_CERTIFICATE_PASSWORD", "AZURE_PASSWORD", "IDENTITY_HEADER", "MSI_SECRET"} {
		if v := os.Getenv(k); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// Provider implements keyprovider.Provider over one key URI.
type Provider struct {
	label   string // "key HOST/KEY", for messages
	host    string
	name    string
	version string // "" or the version the URI pins
	conn    connector

	credMu sync.Mutex
	cred   *recorder
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
	if m == nil || sigazure.ValidReference(uri) != nil {
		// Nothing of the URI is printed: it may carry user info.
		return nil, errors.New("azurekms: the key URI must be azurekms://VAULT-NAME.vault.azure.net/KEY, optionally followed by /VERSION (32 lowercase hex digits), with no user info, port, query or other part (managed HSM and sovereign cloud vault hosts are also accepted)")
	}
	if c.now == nil {
		c.now = time.Now
	}
	return &Provider{label: "key " + keyprovider.KeyName(uri), host: m[1], name: m[2], version: m[3], conn: c}, nil
}

// Name implements keyprovider.Provider.
func (p *Provider) Name() string { return Name }

// credential returns the provider's credential, built on first use; a failed
// build is not remembered.
func (p *Provider) credential(ctx context.Context) (*recorder, error) {
	p.credMu.Lock()
	defer p.credMu.Unlock()
	if p.cred != nil {
		return p.cred, nil
	}
	build := p.conn.cred
	if build == nil {
		build = defaultCredential
	}
	c, err := build(ctx)
	if err != nil {
		return nil, err
	}
	p.cred = &recorder{TokenCredential: c}
	return p.cred, nil
}

func (p *Provider) client(cred azcore.TokenCredential) (*azkeys.Client, error) {
	base := "https://" + p.host + "/"
	if p.conn.endpoint != "" {
		base = p.conn.endpoint
	}
	return azkeys.NewClient(base, cred, &azkeys.ClientOptions{
		ClientOptions: azcore.ClientOptions{
			Transport: p.conn.transport,
			// No client-side retry: a failure is reported at once and ctx (and
			// the caller's own bound) is the only limit.
			Retry:     policy.RetryOptions{MaxRetries: -1},
			Telemetry: policy.TelemetryOptions{Disabled: true},
		},
		DisableChallengeResourceVerification: p.conn.skipChallengeCheck,
	})
}

// Next resolves the URI to one key version, the pinned one or else the key's
// current one, and reads its public key. It only reads (one GET), so a repeated,
// overlapping or half-failed Next leaves nothing behind. ctx bounds the request;
// the caller (signingkeys) also bounds the whole call.
func (p *Provider) Next(ctx context.Context) (keyprovider.Key, error) {
	secrets := envSecrets
	fail := func(what string, err error) error {
		if cerr := ctx.Err(); cerr != nil {
			err = cerr // the call ended because the caller's context did
		}
		return classify(p.label+": "+what, err, secrets()...)
	}
	if err := ctx.Err(); err != nil {
		return keyprovider.Key{}, fail("reading the key", err)
	}
	cred, err := p.credential(ctx)
	if err != nil {
		return keyprovider.Key{}, fail("building the Azure credential", err)
	}
	secrets = cred.secrets
	c, err := p.client(cred)
	if err != nil {
		return keyprovider.Key{}, fail("building the Key Vault client", err)
	}
	resp, err := c.GetKey(ctx, p.name, p.version, nil)
	if err != nil {
		return keyprovider.Key{}, fail("reading the key", err)
	}
	pub, version, err := p.check(resp.KeyBundle)
	if err != nil {
		return keyprovider.Key{}, fmt.Errorf("azurekms: %s: %w", p.label, err)
	}
	k, err := keyprovider.ExternalKey(pub, scheme+p.host+"/"+p.name+"/"+version)
	if err != nil {
		return keyprovider.Key{}, fmt.Errorf("azurekms: %s: %w", p.label, err)
	}
	return k, nil
}

// check validates what Key Vault answered and returns the public key and the
// version it is. It refuses an answer for another key or version, a key that is
// not an enabled, valid EC P-256 signing key, and a point that is not on the
// curve. The refusals are not retryable.
func (p *Provider) check(b azkeys.KeyBundle) (*ecdsa.PublicKey, string, error) {
	const want = "only an EC key on curve P-256 (ES256) is supported"
	jwk := b.Key
	if jwk == nil || jwk.KID == nil {
		return nil, "", errors.New("the vault answered no key identifier")
	}
	version, ok := p.ownVersion(string(*jwk.KID))
	if !ok {
		return nil, "", errors.New("the vault answered the identifier of another key")
	}
	if p.version != "" && version != p.version {
		return nil, "", errors.New("the vault answered for another key version")
	}
	kty, crv := "", ""
	if jwk.Kty != nil {
		kty = capText(string(*jwk.Kty), nil)
	}
	if jwk.Crv != nil {
		crv = capText(string(*jwk.Crv), nil)
	}
	if (kty != string(azkeys.KeyTypeEC) && kty != string(azkeys.KeyTypeECHSM)) || crv != string(azkeys.CurveNameP256) {
		return nil, "", fmt.Errorf("the key is %q on curve %q; %s", kty, crv, want)
	}
	sign := false
	for _, op := range jwk.KeyOps {
		if op != nil && *op == azkeys.KeyOperationSign {
			sign = true
		}
	}
	if !sign {
		return nil, "", errors.New("the key's operations do not include sign")
	}
	if a := b.Attributes; a != nil {
		now := p.conn.now()
		switch {
		case a.Enabled != nil && !*a.Enabled:
			return nil, "", errors.New("the key version is disabled")
		case a.Expires != nil && !now.Before(*a.Expires):
			return nil, "", errors.New("the key version has expired")
		case a.NotBefore != nil && now.Before(*a.NotBefore):
			return nil, "", errors.New("the key version is not valid yet")
		}
	}
	notPoint := fmt.Errorf("the public key is not a point on P-256")
	if len(jwk.X) == 0 || len(jwk.Y) == 0 || len(jwk.X) > 32 || len(jwk.Y) > 32 {
		return nil, "", notPoint
	}
	// X and Y are big-endian and may have been trimmed; ParseUncompressedPublicKey
	// refuses a point that is not on the curve.
	point := make([]byte, 1+2*32)
	point[0] = 4
	copy(point[1+32-len(jwk.X):], jwk.X)
	copy(point[1+64-len(jwk.Y):], jwk.Y)
	pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), point)
	if err != nil {
		return nil, "", notPoint
	}
	return pub, version, nil
}

// ownVersion returns the version of kid, a key identifier
// https://HOST/keys/KEY/VERSION, when it names this provider's vault and key.
func (p *Provider) ownVersion(kid string) (string, bool) {
	u, err := url.Parse(kid)
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !strings.EqualFold(u.Host, p.host) {
		return "", false
	}
	seg := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(seg) != 3 || seg[0] != "keys" || !strings.EqualFold(seg[1], p.name) || !versionRE.MatchString(seg[2]) {
		return "", false
	}
	return seg[2], true
}

// Signer opens k for signing. It makes no network call and reads no
// credentials: the sigstore signer is built on the first SignContext.
func (p *Provider) Signer(_ context.Context, k keyprovider.Key) (keyprovider.Signer, error) {
	if k.Algorithm != keys.ES256 {
		return nil, fmt.Errorf("azurekms: key %s: algorithm %q, only ES256 is supported", k.KID, k.Algorithm)
	}
	pub, ok := k.Public.(*ecdsa.PublicKey)
	if !ok || pub == nil {
		return nil, fmt.Errorf("azurekms: key %s has no ECDSA public key", k.KID)
	}
	if alg, err := keys.AlgorithmOf(pub); err != nil || alg != keys.ES256 {
		return nil, fmt.Errorf("azurekms: key %s: the public key is not ECDSA P-256", k.KID)
	}
	m := refRE.FindStringSubmatch(k.Ref)
	if m == nil || m[3] == "" {
		return nil, fmt.Errorf("azurekms: key %s: the key reference must be an azurekms key version URI (azurekms://VAULT-HOST/KEY/VERSION)", k.KID)
	}
	sign := p.conn.sign
	if sign == nil {
		sign = (&sigstoreSigner{}).sign
	}
	return &signer{label: "key " + keyprovider.KeyName(k.Ref), ref: k.Ref, pub: pub, sign: sign}, nil
}

// signer signs through the signFunc for one pinned version.
type signer struct {
	label string
	ref   string
	pub   *ecdsa.PublicKey
	sign  signFunc
}

// Public is the key the row holds; it asks Key Vault nothing.
func (s *signer) Public() crypto.PublicKey { return s.pub }

type signResult struct {
	sig []byte
	err error
}

// SignContext asks Key Vault to sign digest, a SHA-256 digest, with the pinned
// version and returns the ASN.1 DER signature. The work runs in a goroutine and
// the call returns when ctx ends: sigstore's Azure code passes ctx to every
// request, but a credential step may be slow to give up, and the code can panic
// (see sigstoreSigner), which in a goroutine of the server would end the server.
func (s *signer) SignContext(ctx context.Context, digest []byte, opts crypto.SignerOpts) ([]byte, error) {
	if opts == nil || opts.HashFunc() != crypto.SHA256 {
		return nil, errors.New("azurekms: only SHA-256 digests are signed")
	}
	if len(digest) != crypto.SHA256.Size() {
		return nil, fmt.Errorf("azurekms: a SHA-256 digest is %d bytes, got %d", crypto.SHA256.Size(), len(digest))
	}
	if err := ctx.Err(); err != nil {
		return nil, classify(s.label+": signing", err)
	}
	slots := signSlots
	select {
	case slots <- struct{}{}:
	default:
		return nil, fmt.Errorf("azurekms: %s: %w: too many sign requests are waiting on Key Vault", s.label, keyprovider.ErrUnavailable)
	}
	ch := make(chan signResult, 1)
	go func() {
		defer func() { <-slots }()
		defer func() {
			if r := recover(); r != nil {
				ch <- signResult{nil, fmt.Errorf("azurekms: %s: unexpected panic in the Key Vault signer (%T), most likely a malformed key reply", s.label, r)}
			}
		}()
		sig, err := s.signOnce(ctx, digest)
		ch <- signResult{sig, err}
	}()
	select {
	case r := <-ch:
		return r.sig, r.err
	case <-ctx.Done():
		return nil, classify(s.label+": signing", ctx.Err())
	}
}

func (s *signer) signOnce(ctx context.Context, digest []byte) ([]byte, error) {
	sig, err := s.sign(ctx, s.ref, digest)
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			err = cerr
		}
		return nil, classify(s.label+": signing", err, envSecrets()...)
	}
	// A pinned version is unique, but the check costs one verification and makes a
	// wrong answer loud here rather than at the first use of a bearer. It also
	// refuses a raw r||s signature, which the issuer cannot use.
	if !ecdsa.VerifyASN1(s.pub, digest, sig) {
		return nil, fmt.Errorf("azurekms: %s: Key Vault's signature does not verify under the key the row holds (an ASN.1 DER signature was expected); the key version may have been replaced, or the key is not ES256, or the reply is not from Key Vault", s.label)
	}
	return sig, nil
}

// sigstoreSigner is the production signFunc: sigstore's Azure signer, built
// for a reference on first use (it builds its own default credential and
// azkeys client) and kept. A failed build is not remembered.
//
// Sigstore v1.11.1 passes the context to every request, and it can panic: it
// dereferences the key type of the reply unchecked (client.go fetchPublicKey).
// The signer's goroutine recovers.
type sigstoreSigner struct {
	mu  sync.Mutex
	ref string
	sv  *sigazure.SignerVerifier
}

func (s *sigstoreSigner) load(ctx context.Context, ref string) (*sigazure.SignerVerifier, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sv != nil && s.ref == ref {
		return s.sv, nil
	}
	// Building reads the environment and makes no request; the context it is
	// given is only a default for calls that pass none.
	sv, err := sigazure.LoadSignerVerifier(context.WithoutCancel(ctx), ref)
	if err != nil {
		return nil, err
	}
	s.sv, s.ref = sv, ref
	return sv, nil
}

func (s *sigstoreSigner) sign(ctx context.Context, ref string, digest []byte) ([]byte, error) {
	sv, err := s.load(ctx, ref)
	if err != nil {
		return nil, err
	}
	return sv.SignMessage(nil,
		options.WithDigest(digest),
		options.WithContext(ctx),
		options.WithCryptoSignerOpts(crypto.SHA256))
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

// retryableStatus are the HTTP statuses that mean the service itself failed.
func retryableStatus(code int) bool {
	return code == http.StatusRequestTimeout || code == http.StatusTooManyRequests || code >= 500
}

// classify turns a failure into an error that names what failed. A failure of
// the service itself (unreachable, out of time, throttling, a 5xx, a credential
// endpoint that did the same) wraps keyprovider.ErrUnavailable; a refusal (401,
// 403, 404 and other 4xx) and a canceled context do not. It decides by error
// type and HTTP status, never by message text. Provider text is redacted of
// secrets and capped; nothing here prints a URI or a response body.
func classify(what string, err error, secrets ...string) error {
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("azurekms: %s: %w", what, context.Canceled)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("azurekms: %s: %w: %w", what, keyprovider.ErrUnavailable, context.DeadlineExceeded)
	}
	answered := func(who string, code int, extra string) error {
		text := fmt.Sprintf("%s answered %d", who, code)
		if extra != "" {
			text += " (" + capText(extra, secrets) + ")"
		}
		if retryableStatus(code) {
			return fmt.Errorf("azurekms: %s: %w: %s", what, keyprovider.ErrUnavailable, text)
		}
		return fmt.Errorf("azurekms: %s: %s", what, text)
	}
	var re *azcore.ResponseError
	if errors.As(err, &re) {
		return answered("Key Vault", re.StatusCode, re.ErrorCode)
	}
	var af *azidentity.AuthenticationFailedError
	if errors.As(err, &af) && af.RawResponse != nil {
		// Error() would print the response body; the status is what matters.
		return answered("the Azure sign-in service", af.RawResponse.StatusCode, "")
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err // the URL is not printed
	}
	text := capText(err.Error(), secrets)
	if transient(err) {
		return fmt.Errorf("azurekms: %s: %w: %s", what, keyprovider.ErrUnavailable, text)
	}
	return fmt.Errorf("azurekms: %s: %s", what, text)
}

// transient reports a failure of the network on the way, decided by type.
// net.Error alone is too broad: every syscall.Errno satisfies it, a missing file
// included.
func transient(err error) bool {
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
