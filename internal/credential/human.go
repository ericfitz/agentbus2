package credential

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ericfitz/agentbus2/internal/auth/keys"
	"github.com/ericfitz/agentbus2/internal/bus/remote"
	"github.com/ericfitz/agentbus2/internal/fileacl"
	"github.com/ericfitz/agentbus2/internal/gen"
	"github.com/ericfitz/agentbus2/internal/oidcclient"
)

// HumanSession is a human's cached sign-in: the agentbus2 bearer and, when
// the provider issued one, the provider's refresh token (spec 5.3). The
// corporate access token is never stored. Server is the server the bearer was
// minted for: a session for another server is a miss. AccessToken is empty
// after Invalidate, which keeps the refresh token.
type HumanSession struct {
	Server               string    `json:"server"`
	Identity             string    `json:"identity"`
	AccessToken          string    `json:"access_token"`
	ExpiresAt            time.Time `json:"expires_at"`
	Discovery            Discovery `json:"discovery"`
	ProviderRefreshToken string    `json:"provider_refresh_token,omitempty"`
}

// HumanCache holds the session at Dir/human/session.json (Dir =
// dataDir/tokens/<profile>), the file 0600 and the directory 0700, replaced
// atomically. The "human" directory cannot collide with an <identity>.json of
// Cache. A zero Dir disables the cache.
type HumanCache struct{ Dir string }

func (c HumanCache) path() string { return filepath.Join(c.Dir, "human", "session.json") }

// Load returns the session; false when there is none. A file that cannot be
// decoded is an error.
func (c HumanCache) Load() (HumanSession, bool, error) {
	if c.Dir == "" {
		return HumanSession{}, false, nil
	}
	b, err := os.ReadFile(c.path())
	if errors.Is(err, os.ErrNotExist) {
		return HumanSession{}, false, nil
	}
	if err != nil {
		return HumanSession{}, false, fmt.Errorf("credential: read human session: %w", err)
	}
	var s HumanSession
	if err := json.Unmarshal(b, &s); err != nil || s.Identity == "" {
		return HumanSession{}, false, fmt.Errorf("credential: human session %s is unusable", c.path())
	}
	return s, true, nil
}

// Save writes the session at mode 0600, atomically. A session with no
// identity is refused.
func (c HumanCache) Save(s HumanSession) error {
	if c.Dir == "" {
		return nil
	}
	if s.Identity == "" {
		return errors.New("credential: human session has no identity")
	}
	b, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("credential: encode human session: %w", err)
	}
	dir := filepath.Dir(c.path())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("credential: create %s: %w", dir, err)
	}
	if err := keys.WriteFile(c.path(), b); err != nil {
		return err
	}
	if err := fileacl.OwnerOnly(c.path()); err != nil {
		return fmt.Errorf("credential: restrict %s: %w", c.path(), err)
	}
	return nil
}

// Remove deletes the session; nil when absent.
func (c HumanCache) Remove() error {
	if c.Dir == "" {
		return nil
	}
	if err := os.Remove(c.path()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("credential: remove human session: %w", err)
	}
	return nil
}

// HumanSource mints and caches the bearers of a human who signs in through the
// corporate provider (spec 5.3): the cached bearer, else the provider's
// refresh token, else the browser. Token and Refresh may block for up to
// OIDC.Timeout while the browser sign-in completes.
type HumanSource struct {
	Server string
	OIDC   oidcclient.Config
	Client *gen.ClientWithResponses // over Server, no editor
	HTTP   *http.Client             // discovery; nil: a 10 s client
	Cache  HumanCache
	Now    func() time.Time // nil: time.Now

	mu sync.Mutex // serializes Token, Refresh, SignIn, Invalidate

	annMu       sync.Mutex
	announce    func(string)
	announceSet bool
}

func (s *HumanSource) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// SetAnnounce sets the function that receives the browser URL of a sign-in,
// replacing OIDC.Announce; nil silences it. Safe to call while a sign-in runs.
func (s *HumanSource) SetAnnounce(f func(string)) {
	s.annMu.Lock()
	s.announce, s.announceSet = f, true
	s.annMu.Unlock()
}

func (s *HumanSource) announceURL(u string) {
	s.annMu.Lock()
	f, set := s.announce, s.announceSet
	s.annMu.Unlock()
	if !set {
		f = s.OIDC.Announce
	}
	if f != nil {
		f(u)
	}
}

// session returns the cached session when it is for this server; an unreadable
// cache is a miss.
func (s *HumanSource) session() (HumanSession, bool) {
	e, ok, err := s.Cache.Load()
	if err != nil || !ok || e.Server != s.Server {
		return HumanSession{}, false
	}
	return e, true
}

// Token returns a bearer valid for more than Skew: the cached one, else a new one.
func (s *HumanSource) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.session(); ok && e.AccessToken != "" && e.ExpiresAt.After(s.now().Add(Skew)) {
		return e.AccessToken, nil
	}
	return s.refresh(ctx)
}

// Refresh mints a bearer regardless of the cache: through the provider's
// refresh token when there is one, else through the browser.
func (s *HumanSource) Refresh(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refresh(ctx)
}

// SignIn always runs the browser sign-in and caches the result.
func (s *HumanSource) SignIn(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.signIn(ctx)
}

func (s *HumanSource) refresh(ctx context.Context) (string, error) {
	e, ok := s.session()
	if !ok || e.ProviderRefreshToken == "" {
		return s.signIn(ctx)
	}
	tokens, err := oidcclient.Refresh(ctx, s.OIDC, e.ProviderRefreshToken)
	if errors.Is(err, oidcclient.ErrRefreshRejected) {
		return s.signIn(ctx)
	}
	if err != nil {
		return "", err
	}
	// The provider may have rotated the refresh token (Dex retires the one
	// presented): cache it before the exchange, so a failed exchange does not
	// leave a retired token behind.
	if tokens.RefreshToken != e.ProviderRefreshToken {
		e.ProviderRefreshToken, e.AccessToken = tokens.RefreshToken, ""
		if err := s.Cache.Save(e); err != nil {
			return "", err
		}
	}
	return s.exchange(ctx, tokens, e.Discovery)
}

// withoutDeadline returns a context that is canceled when ctx is canceled but
// ignores ctx's deadline: a browser sign-in has its own timeout and must not be
// cut short by the caller's request deadline (the TUI gives bus calls 10 s).
func withoutDeadline(ctx context.Context) (context.Context, context.CancelFunc) {
	c, cancel := context.WithCancel(context.WithoutCancel(ctx))
	stop := context.AfterFunc(ctx, func() {
		if errors.Is(ctx.Err(), context.Canceled) {
			cancel()
		}
	})
	return c, func() { stop(); cancel() }
}

func (s *HumanSource) signIn(ctx context.Context) (string, error) {
	ctx, cancelSign := withoutDeadline(ctx)
	defer cancelSign()
	c := s.OIDC
	c.Announce = s.announceURL
	tokens, err := oidcclient.SignIn(ctx, c)
	if err != nil {
		return "", err
	}
	var d Discovery
	if e, ok := s.session(); ok {
		d = e.Discovery
	}
	if d.TokenEndpoint == "" {
		// Informational (login output); a server that cannot be discovered
		// fails the exchange below with a better message.
		d, _ = Discover(ctx, s.HTTP, s.Server)
	}
	return s.exchange(ctx, tokens, d)
}

// exchange trades the provider's access token for a bearer and caches it with
// the provider's refresh token.
func (s *HumanSource) exchange(ctx context.Context, tokens oidcclient.Tokens, d Discovery) (string, error) {
	xctx, cancel := context.WithTimeout(ctx, exchangeTimeout)
	defer cancel()
	at, err := remote.TokenExchange(xctx, s.Client, tokens.AccessToken)
	if err != nil {
		return "", err // a *bus.Error is returned as is
	}
	if at.Token == "" || at.ExpiresIn <= 0 {
		return "", errors.New("credential: the token endpoint returned no usable access token")
	}
	sub := bearerSub(at.Token)
	if sub == "" {
		return "", errors.New("credential: the server's bearer has no sub claim")
	}
	err = s.Cache.Save(HumanSession{
		Server: s.Server, Identity: sub, AccessToken: at.Token, ExpiresAt: s.now().Add(at.ExpiresIn),
		Discovery: d, ProviderRefreshToken: tokens.RefreshToken,
	})
	if err != nil {
		return "", err
	}
	return at.Token, nil
}

// Identity is the sub of the cached bearer, read without verifying it (the
// server verifies every request); empty when this server has no session.
func (s *HumanSource) Identity() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.session()
	if !ok {
		return ""
	}
	if sub := bearerSub(e.AccessToken); sub != "" {
		return sub
	}
	return e.Identity
}

// Invalidate drops the cached bearer and keeps the provider's refresh token,
// so the next Token refreshes without the browser.
func (s *HumanSource) Invalidate() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.session()
	if !ok {
		return nil
	}
	e.AccessToken, e.ExpiresAt = "", time.Time{}
	return s.Cache.Save(e)
}

// Editor sets Authorization: Bearer <Token(ctx)> on every request.
func (s *HumanSource) Editor() gen.RequestEditorFn {
	return func(ctx context.Context, req *http.Request) error {
		t, err := s.Token(ctx)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+t)
		return nil
	}
}

// bearerSub returns the sub claim of a compact JWT without verifying it; empty
// when there is none.
func bearerSub(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var c struct {
		Sub string `json:"sub"`
	}
	if json.Unmarshal(b, &c) != nil {
		return ""
	}
	return c.Sub
}
