// Package hashivault is the external key provider over HashiCorp Vault's
// transit secrets engine, an adapter on sigstore's kms/hashivault module
// (signing-keys plan Task 11; Eric 2026-10-27 decisions 1, 3 and 12). The
// private key never leaves Vault: Next reads the transit key and pins its
// latest version, and the signer asks Vault to sign a SHA-256 digest with that
// version (ECDSA P-256, a DER signature).
//
// A key URI is hashivault://NAME, the name of a transit key of type
// ecdsa-p256. The Vault address, token and transit mount come from the
// environment (see resolveEnv). A pinned key reference is
// hashivault://NAME/VERSION. A URI is never printed: messages name the key
// with keyprovider.KeyName, and no message carries the token.
//
// sigstore does not expose the latest key version, so Next reads the key with
// the Vault API client directly; signing goes through sigstore's signer.
package hashivault

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"

	vault "github.com/hashicorp/vault/api"
	sighv "github.com/sigstore/sigstore/pkg/signature/kms/hashivault"
	"github.com/sigstore/sigstore/pkg/signature/options"

	"github.com/ericfitz/agentbus2/internal/auth/keys"
	"github.com/ericfitz/agentbus2/internal/keyprovider"
)

// Name is the provider's name and its key URI scheme.
const Name = "hashivault"

const scheme = Name + "://"

// nameRE is what hashivault://NAME allows. It is sigstore's own rule for the
// part after the scheme (the module also accepts openbao://, which this
// backend does not).
var nameRE = regexp.MustCompile(`^\w(?:[\w.-]*\w)?$`)

// mountRE is a transit mount path: slash-separated segments that start with a
// letter, digit or underscore, so no "..", no empty segment and no query.
var mountRE = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*(?:/[A-Za-z0-9_][A-Za-z0-9_.-]*)*$`)

// signSlots bounds the sign requests that sigstore's signer may have in
// flight. Sigstore ignores its context and sets no timeout, so a Vault that
// never answers would strand one goroutine per bearer; at the bound a sign
// fails at once, retryably.
var signSlots = make(chan struct{}, 16)

// Provider implements keyprovider.Provider over one transit key.
type Provider struct {
	name string // the transit key name
}

var _ keyprovider.Provider = (*Provider)(nil)

// New validates uri (hashivault://NAME) and the Vault environment (resolveEnv)
// and returns a provider, so a missing or invalid address, token or mount is a
// configuration error at start (exit 2), as for the other backends. It makes
// no network call; Next and Sign read the environment again on every use.
func New(uri string) (keyprovider.Provider, error) {
	p, err := newProvider(uri)
	if err != nil {
		return nil, err // never a typed nil inside the interface
	}
	return p, nil
}

func newProvider(uri string) (*Provider, error) {
	name, ok := strings.CutPrefix(uri, scheme)
	if !ok || !nameRE.MatchString(name) {
		return nil, fmt.Errorf("hashivault: %s: the key URI must be hashivault://NAME, where NAME is letters, digits, '_', '-' and '.' and starts and ends with a letter, digit or '_'", describeURI(uri))
	}
	if _, err := resolveEnv(); err != nil {
		return nil, err
	}
	return &Provider{name: name}, nil
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

// env is the connection the process environment names.
type env struct {
	addr  string
	token string
	mount string
}

// resolveEnv reads, on every use, the variables this backend honors:
//
//	VAULT_ADDR, else BAO_ADDR: the Vault address (http or https, no user info);
//	VAULT_TOKEN, else BAO_TOKEN: the token (required; the token helper and
//	  ~/.vault-token that the Vault CLI reads are not used);
//	TRANSIT_SECRET_ENGINE_PATH: the transit mount, default "transit".
//
// The Vault API client and sigstore's signer also read the variables the Vault
// client reads (VAULT_NAMESPACE, VAULT_HEADERS, VAULT_CACERT, VAULT_CAPATH,
// VAULT_CLIENT_CERT, VAULT_CLIENT_KEY, VAULT_SKIP_VERIFY, VAULT_TLS_SERVER_NAME,
// VAULT_CACERT_BYTES, VAULT_PROXY_ADDR, VAULT_HTTP_PROXY and the standard
// proxy variables). VAULT_CLIENT_TIMEOUT is parsed but not applied, because the
// Config is hand-built: Next is bounded by NextTimeout (signingkeys) and sign
// by the issuer's deadline.
func resolveEnv() (env, error) {
	e := env{
		addr:  firstSet("VAULT_ADDR", "BAO_ADDR"),
		token: firstSet("VAULT_TOKEN", "BAO_TOKEN"),
		mount: os.Getenv("TRANSIT_SECRET_ENGINE_PATH"),
	}
	if e.addr == "" {
		return env{}, errors.New("hashivault: VAULT_ADDR (or BAO_ADDR) is not set")
	}
	u, err := url.Parse(e.addr)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		// Never echo the address: it may carry user info.
		return env{}, errors.New("hashivault: VAULT_ADDR must be an http or https URL with a host and no user info")
	}
	if e.token == "" {
		return env{}, errors.New("hashivault: VAULT_TOKEN (or BAO_TOKEN) is not set")
	}
	if e.mount == "" {
		e.mount = "transit"
	}
	if !mountRE.MatchString(e.mount) {
		return env{}, errors.New("hashivault: TRANSIT_SECRET_ENGINE_PATH must be slash-separated names of letters, digits, '_', '-' and '.', with no leading or trailing slash")
	}
	return e, nil
}

func firstSet(names ...string) string {
	for _, n := range names {
		if v := os.Getenv(n); v != "" {
			return v
		}
	}
	return ""
}

// Next reads the transit key and pins its latest version: the pinned ref is
// hashivault://NAME/VERSION and the public half is that version's. It only
// reads, so a repeated, overlapping or half-failed Next leaves nothing behind.
func (p *Provider) Next(ctx context.Context) (keyprovider.Key, error) {
	label := "key " + p.name
	e, err := resolveEnv()
	if err != nil {
		return keyprovider.Key{}, err
	}
	cfg := &vault.Config{Address: e.addr}
	c, err := vault.NewClient(cfg)
	if err != nil {
		return keyprovider.Key{}, fmt.Errorf("hashivault: building the Vault client: %w", err)
	}
	// One client per call: do not leave its idle connections behind.
	defer cfg.HttpClient.CloseIdleConnections()
	c.SetToken(e.token)
	sec, err := c.Logical().ReadWithContext(ctx, e.mount+"/keys/"+p.name)
	if err != nil {
		return keyprovider.Key{}, classify(label+": reading the transit key", err)
	}
	if sec == nil || sec.Data == nil {
		return keyprovider.Key{}, fmt.Errorf("hashivault: %s: the transit key does not exist under mount %q", label, e.mount)
	}
	d := sec.Data
	if typ, _ := d["type"].(string); typ != "ecdsa-p256" {
		return keyprovider.Key{}, fmt.Errorf("hashivault: %s: the transit key type is %q; only ECDSA P-256 (ES256) is supported, so it must be ecdsa-p256", label, typ)
	}
	if v, ok := d["supports_signing"].(bool); ok && !v {
		return keyprovider.Key{}, fmt.Errorf("hashivault: %s: the transit key does not support signing", label)
	}
	latest, ok := d["latest_version"].(interface{ String() string })
	if !ok {
		return keyprovider.Key{}, fmt.Errorf("hashivault: %s: the answer has no latest_version", label)
	}
	version, err := strconv.ParseUint(latest.String(), 10, 63)
	if err != nil || version == 0 {
		return keyprovider.Key{}, fmt.Errorf("hashivault: %s: the answer's latest_version is not a version number", label)
	}
	versions, _ := d["keys"].(map[string]any)
	entry, _ := versions[strconv.FormatUint(version, 10)].(map[string]any)
	pubPEM, _ := entry["public_key"].(string)
	if pubPEM == "" {
		return keyprovider.Key{}, fmt.Errorf("hashivault: %s: the latest version (%d) has no readable public key", label, version)
	}
	block, _ := pem.Decode([]byte(pubPEM))
	if block == nil {
		return keyprovider.Key{}, fmt.Errorf("hashivault: %s: the latest version (%d) has a public key that is not PEM", label, version)
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return keyprovider.Key{}, fmt.Errorf("hashivault: %s: parsing the latest version's (%d) public key: %w", label, version, err)
	}
	k, err := keyprovider.ExternalKey(pub, pinnedRef(p.name, version))
	if err != nil {
		return keyprovider.Key{}, fmt.Errorf("hashivault: %s: %w", label, err)
	}
	return k, nil
}

func pinnedRef(name string, version uint64) string {
	return scheme + name + "/" + strconv.FormatUint(version, 10)
}

// parseRef splits a pinned reference hashivault://NAME/VERSION.
func parseRef(ref string) (name string, version uint64, err error) {
	rest, ok := strings.CutPrefix(ref, scheme)
	if !ok {
		return "", 0, errors.New("the key reference is not a hashivault URI")
	}
	name, ver, ok := strings.Cut(rest, "/")
	if !ok || !nameRE.MatchString(name) {
		return "", 0, errors.New("the key reference must be hashivault://NAME/VERSION")
	}
	version, perr := strconv.ParseUint(ver, 10, 63)
	if perr != nil || version == 0 || ver != strconv.FormatUint(version, 10) {
		return "", 0, errors.New("the key reference must be hashivault://NAME/VERSION with a version of 1 or more")
	}
	return name, version, nil
}

// Signer opens k for signing. It makes no network call: the sigstore signer is
// built on the first SignContext.
func (p *Provider) Signer(_ context.Context, k keyprovider.Key) (keyprovider.Signer, error) {
	if k.Algorithm != keys.ES256 {
		return nil, fmt.Errorf("hashivault: key %s: algorithm %q, only ES256 is supported", k.KID, k.Algorithm)
	}
	pub, ok := k.Public.(*ecdsa.PublicKey)
	if !ok || pub == nil {
		return nil, fmt.Errorf("hashivault: key %s has no ECDSA public key", k.KID)
	}
	if alg, err := keys.AlgorithmOf(pub); err != nil || alg != keys.ES256 {
		return nil, fmt.Errorf("hashivault: key %s: the public key is not ECDSA P-256", k.KID)
	}
	name, version, err := parseRef(k.Ref)
	if err != nil {
		return nil, fmt.Errorf("hashivault: key %s: %s: %w", k.KID, describeURI(k.Ref), err)
	}
	return &signer{name: name, version: version, pub: pub}, nil
}

// signer signs through sigstore's SignerVerifier for one pinned version, built
// on first use. A failed build is not remembered.
type signer struct {
	name    string
	version uint64
	pub     *ecdsa.PublicKey

	mu sync.Mutex
	sv *sighv.SignerVerifier
}

// Public is the key the row holds; it asks Vault nothing.
func (s *signer) Public() crypto.PublicKey { return s.pub }

func (s *signer) load() (*sighv.SignerVerifier, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sv != nil {
		return s.sv, nil
	}
	e, err := resolveEnv()
	if err != nil {
		return nil, err
	}
	// Address, token and mount are passed explicitly so sigstore applies the
	// same values this package validated and never reaches for the Vault token
	// helper or a login.
	sv, err := sighv.LoadSignerVerifier(scheme+s.name, crypto.SHA256,
		options.WithRPCAuthOpts(options.RPCAuth{Address: e.addr, Token: e.token, Path: e.mount}),
		options.WithKeyVersion(strconv.FormatUint(s.version, 10)))
	if err != nil {
		return nil, fmt.Errorf("hashivault: key %s: building the Vault signer: %s", s.name, strings.TrimPrefix(redact(err.Error()), "new vault client: "))
	}
	s.sv = sv
	return sv, nil
}

type signResult struct {
	sig []byte
	err error
}

// SignContext asks Vault to sign digest, a SHA-256 digest, with the pinned
// version and returns the ASN.1 DER signature. Sigstore's signer ignores ctx,
// so the request runs in a goroutine and the call returns when ctx ends.
func (s *signer) SignContext(ctx context.Context, digest []byte, opts crypto.SignerOpts) ([]byte, error) {
	label := "key " + s.name
	if opts == nil || opts.HashFunc() != crypto.SHA256 {
		return nil, errors.New("hashivault: only SHA-256 digests are signed")
	}
	if len(digest) != crypto.SHA256.Size() {
		return nil, fmt.Errorf("hashivault: a SHA-256 digest is %d bytes, got %d", crypto.SHA256.Size(), len(digest))
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sv, err := s.load()
	if err != nil {
		return nil, err
	}
	slots := signSlots
	select {
	case slots <- struct{}{}:
	default:
		return nil, fmt.Errorf("hashivault: %s: %w: too many sign requests are waiting on Vault", label, keyprovider.ErrUnavailable)
	}
	ch := make(chan signResult, 1)
	go func() {
		defer func() { <-slots }()
		// Sigstore dereferences the answer without checking it: an empty reply
		// the API client turns into "no secret, no error" (a 204, or a 404 with
		// no error text) panics. A panic in this goroutine would end the server.
		defer func() {
			if r := recover(); r != nil {
				ch <- signResult{nil, fmt.Errorf("hashivault: unexpected panic in the Vault signer (%T), most likely an empty reply from Vault", r)}
			}
		}()
		sig, err := sv.SignMessage(nil,
			options.WithDigest(digest),
			options.WithContext(ctx),
			options.WithCryptoSignerOpts(crypto.SHA256),
			options.WithKeyVersion(strconv.FormatUint(s.version, 10)))
		ch <- signResult{sig, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			return nil, classify(label+": signing", r.err)
		}
		// Vault answered 200 for a key name and version, not necessarily this
		// key: check the signature against the key the row holds.
		if !ecdsa.VerifyASN1(s.pub, digest, r.sig) {
			return nil, fmt.Errorf("hashivault: %s: Vault's signature does not verify under the key the row holds; likely causes: this replica uses another transit mount (TRANSIT_SECRET_ENGINE_PATH) or namespace (VAULT_NAMESPACE) than the one the key was resolved under, or the transit key was deleted and recreated under the same name", label)
		}
		return r.sig, nil
	case <-ctx.Done():
		return nil, classify(label+": signing", ctx.Err())
	}
}

// maxVaultText caps the Vault-supplied text kept in an error.
const maxVaultText = 256

// redact replaces the configured token wherever text holds it.
func redact(text string) string {
	for _, tok := range []string{os.Getenv("VAULT_TOKEN"), os.Getenv("BAO_TOKEN")} {
		if tok != "" {
			text = strings.ReplaceAll(text, tok, "[redacted]")
		}
	}
	return text
}

// classify turns a request failure into an error that names what failed
// without the request URL or anything from the connection setup, and marks the
// Vault service's own failures (unreachable, out of time, HTTP 429 or 5xx, a
// sealed or standby node) with keyprovider.ErrUnavailable. A refusal (bad
// token, missing key, bad request) and a canceled context are not marked. It
// decides by error type and status, never by message text.
func classify(what string, err error) error {
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("hashivault: %s: %w", what, err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("hashivault: %s: %w: %w", what, keyprovider.ErrUnavailable, err)
	}
	var resp *vault.ResponseError
	if errors.As(err, &resp) {
		text := fmt.Sprintf("Vault answered HTTP %d", resp.StatusCode)
		if len(resp.Errors) > 0 {
			// Vault's (or a proxy's) text: redact the token, cap the length.
			vt := redact(strings.Join(resp.Errors, "; "))
			if len(vt) > maxVaultText {
				vt = strings.ToValidUTF8(vt[:maxVaultText], "") + "..."
			}
			text += ": " + vt
		}
		if resp.StatusCode == 429 || resp.StatusCode >= 500 {
			return fmt.Errorf("hashivault: %s: %w: %s", what, keyprovider.ErrUnavailable, text)
		}
		return fmt.Errorf("hashivault: %s: %s", what, text)
	}
	var uerr *url.Error
	if errors.As(err, &uerr) {
		inner := uerr.Err
		text := "the request failed: " + inner.Error()
		var ne net.Error
		if uerr.Timeout() || errors.As(inner, &ne) || errors.Is(inner, io.EOF) || errors.Is(inner, io.ErrUnexpectedEOF) {
			return fmt.Errorf("hashivault: %s: %w: %s", what, keyprovider.ErrUnavailable, text)
		}
		return fmt.Errorf("hashivault: %s: %s", what, text)
	}
	return fmt.Errorf("hashivault: %s: %w", what, err)
}
