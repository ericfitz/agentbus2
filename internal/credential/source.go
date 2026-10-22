package credential

import (
	"context"
	"crypto"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ericfitz/agentbus2/internal/auth/assertion"
	"github.com/ericfitz/agentbus2/internal/auth/keys"
	"github.com/ericfitz/agentbus2/internal/bus/remote"
	"github.com/ericfitz/agentbus2/internal/fileacl"
	"github.com/ericfitz/agentbus2/internal/gen"
)

// Skew is how long before expiry a cached bearer stops being used.
const Skew = 60 * time.Second

// Cache holds the bearer and the discovery document per identity at
// Dir/<identity>.json (Dir = dataDir/tokens/<profile>), 0600, replaced
// atomically. A zero Dir disables the cache.
type Cache struct{ Dir string }

// Entry is one cached bearer. Server is the server it was minted for: an
// entry for another server is a miss, so a profile pointed elsewhere never
// sends the old server's bearer to the new one.
type Entry struct {
	Server      string    `json:"server"`
	AccessToken string    `json:"access_token"`
	ExpiresAt   time.Time `json:"expires_at"`
	Discovery   Discovery `json:"discovery"`
}

func (c Cache) path(identity string) string { return filepath.Join(c.Dir, identity+".json") }

// Load returns the entry of identity; false when there is none.
func (c Cache) Load(identity string) (Entry, bool, error) {
	if c.Dir == "" {
		return Entry{}, false, nil
	}
	if err := checkName(identity); err != nil {
		return Entry{}, false, err
	}
	b, err := os.ReadFile(c.path(identity))
	if errors.Is(err, os.ErrNotExist) {
		return Entry{}, false, nil
	}
	if err != nil {
		return Entry{}, false, fmt.Errorf("credential: read token cache: %w", err)
	}
	var e Entry
	if err := json.Unmarshal(b, &e); err != nil || e.AccessToken == "" {
		return Entry{}, false, fmt.Errorf("credential: token cache %s is unusable", c.path(identity))
	}
	return e, true, nil
}

// Save writes the entry at mode 0600, atomically.
func (c Cache) Save(identity string, e Entry) error {
	if c.Dir == "" {
		return nil
	}
	if err := checkName(identity); err != nil {
		return err
	}
	b, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("credential: encode token cache: %w", err)
	}
	if err := os.MkdirAll(c.Dir, 0o700); err != nil {
		return fmt.Errorf("credential: create %s: %w", c.Dir, err)
	}
	path := c.path(identity)
	if err := keys.WriteFile(path, b); err != nil {
		return err
	}
	if err := fileacl.OwnerOnly(path); err != nil {
		return fmt.Errorf("credential: restrict %s: %w", path, err)
	}
	return nil
}

// Remove deletes the entry; nil when absent.
func (c Cache) Remove(identity string) error {
	if c.Dir == "" {
		return nil
	}
	if err := checkName(identity); err != nil {
		return err
	}
	if err := os.Remove(c.path(identity)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("credential: remove token cache: %w", err)
	}
	return nil
}

// Source mints and caches bearers for one identity (Decisions 70 and 84).
type Source struct {
	Server, Identity string
	Signer           crypto.Signer // nil when Process is set
	Process          []string
	Client           *gen.ClientWithResponses // over Server, no editor
	HTTP             *http.Client             // discovery; nil: a 10 s client
	Cache            Cache
	Now              func() time.Time // nil: time.Now

	mu      sync.Mutex
	disc    *Discovery
	procKey crypto.Signer // a private_key_pem result, held in memory only
}

func (s *Source) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Token returns a bearer valid for more than Skew: the cached one, else a fresh one.
func (s *Source) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// A cache that cannot be read is a miss: the refresh rewrites it.
	if e, ok, err := s.Cache.Load(s.Identity); err == nil && ok && e.Server == s.Server {
		if s.disc == nil && e.Discovery.TokenEndpoint != "" {
			d := e.Discovery
			s.disc = &d
		}
		if e.ExpiresAt.After(s.now().Add(Skew)) {
			return e.AccessToken, nil
		}
	}
	return s.refresh(ctx)
}

// Refresh mints a bearer regardless of the cache.
func (s *Source) Refresh(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refresh(ctx)
}

// discovery returns the held or cached discovery document, else a fresh one;
// fresh reports whether it was fetched by this call.
func (s *Source) discovery(ctx context.Context) (d Discovery, fresh bool, err error) {
	if s.disc != nil {
		return *s.disc, false, nil
	}
	if e, ok, err := s.Cache.Load(s.Identity); err == nil && ok && e.Server == s.Server && e.Discovery.TokenEndpoint != "" {
		d := e.Discovery
		s.disc = &d
		return d, false, nil
	}
	if d, err = Discover(ctx, s.HTTP, s.Server); err != nil {
		return Discovery{}, false, err
	}
	s.disc = &d
	return d, true, nil
}

// exchangeTimeout bounds one token exchange, as discoveryTimeout bounds
// discovery and ProcessTimeout a credential_process (Global Constraints). A
// var so a test does not wait it out.
var exchangeTimeout = 10 * time.Second

func newJTI() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("credential: jti: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// refresh mints a bearer. When that fails with a discovery document that was
// held or cached rather than fetched now, it rediscovers once and, if the
// server now advertises another token endpoint, mints again: a stale cached
// document must not wedge the profile until its cache file is deleted.
func (s *Source) refresh(ctx context.Context) (string, error) {
	if s.Signer == nil && len(s.Process) == 0 {
		return "", errors.New("credential: no signing key and no credential_process")
	}
	d, fresh, err := s.discovery(ctx)
	if err != nil {
		return "", err
	}
	tok, err := s.mint(ctx, d)
	if err == nil || fresh || ctx.Err() != nil {
		return tok, err
	}
	nd, derr := Discover(ctx, s.HTTP, s.Server)
	if derr != nil || nd == d {
		return "", err
	}
	s.disc = &nd
	return s.mint(ctx, nd)
}

// mint runs the credential_process or signs an assertion for d's token
// endpoint, exchanges it and caches the result with d.
func (s *Source) mint(ctx context.Context, d Discovery) (string, error) {
	signer := s.Signer
	if signer == nil {
		signer = s.procKey
	}
	var raw string
	if signer == nil {
		jti, err := newJTI()
		if err != nil {
			return "", err
		}
		out, err := RunProcess(ctx, s.Process, ProcessInput{Identity: s.Identity, TokenEndpoint: d.TokenEndpoint, Audience: d.TokenEndpoint, JTI: jti})
		if err != nil {
			return "", err
		}
		switch {
		case out.AccessToken != "":
			exp, _ := time.Parse(time.RFC3339, out.ExpiresAt) // RunProcess checked it
			if err := s.Cache.Save(s.Identity, Entry{Server: s.Server, AccessToken: out.AccessToken, ExpiresAt: exp, Discovery: d}); err != nil {
				return "", err
			}
			return out.AccessToken, nil
		case out.ClientAssertion != "":
			raw = out.ClientAssertion
		default:
			k, _, err := keys.ParsePrivatePEM([]byte(out.PrivateKeyPEM))
			if err != nil {
				return "", fmt.Errorf("credential_process %q: private_key_pem: %w", s.Process[0], err)
			}
			s.procKey, signer = k, k
		}
	}
	if raw == "" {
		kid, err := keys.KID(signer.Public())
		if err != nil {
			return "", err
		}
		raw, err = assertion.Sign(assertion.Input{Identity: s.Identity, TokenEndpoint: d.TokenEndpoint, Key: signer, KID: kid, Now: s.now()})
		if err != nil {
			return "", err
		}
	}
	xctx, cancel := context.WithTimeout(ctx, exchangeTimeout)
	defer cancel()
	at, err := remote.Token(xctx, s.Client, raw)
	if err != nil {
		return "", err // a *bus.Error is returned as is
	}
	if at.Token == "" || at.ExpiresIn <= 0 {
		return "", errors.New("credential: the token endpoint returned no usable access token")
	}
	if err := s.Cache.Save(s.Identity, Entry{Server: s.Server, AccessToken: at.Token, ExpiresAt: s.now().Add(at.ExpiresIn), Discovery: d}); err != nil {
		return "", err
	}
	return at.Token, nil
}

// Editor sets Authorization: Bearer <Token(ctx)> on every request.
func (s *Source) Editor() gen.RequestEditorFn {
	return func(ctx context.Context, req *http.Request) error {
		t, err := s.Token(ctx)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+t)
		return nil
	}
}
