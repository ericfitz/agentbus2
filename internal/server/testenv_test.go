package server

import (
	"context"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/auth/keys"
	"github.com/ericfitz/agentbus2/internal/auth/token"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/identities"
	"github.com/ericfitz/agentbus2/internal/core/notify"
	"github.com/ericfitz/agentbus2/internal/gen"
	"github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/store/sqlite"
)

// fixedNowMs is the fake clock's start; any fixed instant works.
const fixedNowMs = 1_700_000_000_000

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t
}

// Advance moves the clock forward (used by Tasks 5 to 8).
func (f *fakeClock) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.t = f.t.Add(d)
}

// fakeEmbedder is the smallest core.Embedder: it exists so Capabilities can
// see one.
type fakeEmbedder struct{}

func (fakeEmbedder) Model() string { return "fake" }
func (fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{1}
	}
	return out, nil
}

// env is one Core over a fresh SQLite file, with the clock steered by the test.
type env struct {
	t        *testing.T
	store    store.Store
	core     *core.Core
	clock    *fakeClock
	embedder core.Embedder
	auth     Authenticator
	issuer   *token.Issuer
	opts     Options // Registration, Issuer and External for New
	dbPath   string

	once sync.Once
	h    http.Handler
}

type envOptions struct {
	cfg      func(*core.Config)
	embedder bool
	auth     Authenticator // nil: InProcess{}, or Bearer when issuer is set

	// issuer builds token.NewIssuer("http://inproc", <a test Ed25519 key>,
	// time.Hour, clock.Now) and, unless auth is set, Bearer over it.
	issuer       bool
	registration identities.Mode
	external     []*token.External // sets Bearer.Verifier.External when issuer is set
	autoCreate   bool              // sets Bearer.AutoCreate to CreateIdentity

	// dbPath opens this SQLite file instead of a fresh one, so two envs
	// model two replicas over one database.
	dbPath string
}

func newEnv(t *testing.T, o envOptions) *env {
	t.Helper()
	path := o.dbPath
	if path == "" {
		path = filepath.Join(t.TempDir(), "ab2.db")
	}
	s, err := sqlite.Open(context.Background(), path, sqlite.Options{})
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	e := &env{t: t, store: s, clock: &fakeClock{t: time.UnixMilli(fixedNowMs)}, auth: o.auth, dbPath: path}
	if e.auth == nil {
		e.auth = InProcess{}
	}
	if o.issuer {
		key, err := keys.Generate(keys.EdDSA)
		if err != nil {
			t.Fatalf("keys.Generate: %v", err)
		}
		e.issuer, err = token.NewIssuer("http://inproc", key, time.Hour, e.clock.Now)
		if err != nil {
			t.Fatalf("token.NewIssuer: %v", err)
		}
		e.opts = Options{Registration: o.registration, Issuer: e.issuer, External: o.external}
		if o.auth == nil {
			b := Bearer{Verifier: token.Verifier{Own: e.issuer, External: o.external}, Now: e.clock.Now}
			e.auth = b
		}
	} else {
		e.opts = Options{Registration: o.registration, External: o.external}
	}
	cfg := core.Defaults()
	if o.cfg != nil {
		o.cfg(&cfg)
	}
	opts := core.Options{Now: e.clock.Now, Notifier: notify.NewMemory()}
	if o.embedder {
		e.embedder = fakeEmbedder{}
		cfg.EmbeddingModel = "fake"
		opts.Embedder = e.embedder
	}
	c, err := core.New(s, cfg, opts)
	if err != nil {
		t.Fatalf("core.New: %v", err)
	}
	e.core = c
	if b, ok := e.auth.(Bearer); ok && o.autoCreate {
		b.AutoCreate = func(ctx context.Context, name string) error {
			_, err := identities.Service{Core: c}.CreateIdentity(ctx, name)
			return err
		}
		e.auth = b
	}
	return e
}

func (e *env) handler() http.Handler {
	e.once.Do(func() { e.h = New(e.core, e.withAuth()) })
	return e.h
}

func (e *env) withAuth() Options {
	o := e.opts
	o.Auth = e.auth
	return o
}

// identity registers name (and makes it an administrator when admin).
func (e *env) identity(name string, admin bool) {
	e.t.Helper()
	err := e.store.Tx(context.Background(), func(ctx context.Context, tx store.Tx) error {
		if err := tx.CreateIdentity(ctx, name, e.core.NowMs()); err != nil {
			return err
		}
		if admin {
			return tx.SetAdmin(ctx, name, true)
		}
		return nil
	})
	if err != nil {
		e.t.Fatalf("identity %q: %v", name, err)
	}
}

// client is the generated client over the in-process transport.
func (e *env) client(identity string) *gen.ClientWithResponses {
	e.t.Helper()
	c, err := gen.NewClientWithResponses("http://inproc",
		gen.WithHTTPClient(&http.Client{Transport: InProcessTransport(e.handler(), identity)}))
	if err != nil {
		e.t.Fatalf("NewClientWithResponses: %v", err)
	}
	return c
}

// raw sends req over the same transport, for requests the generated client
// cannot build.
func (e *env) raw(identity string, req *http.Request) *http.Response {
	e.t.Helper()
	resp, err := InProcessTransport(e.handler(), identity).RoundTrip(req)
	if err != nil {
		e.t.Fatalf("RoundTrip %s %s: %v", req.Method, req.URL, err)
	}
	e.t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}
