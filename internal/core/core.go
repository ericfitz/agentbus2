package core

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/ericfitz/agentbus2/internal/core/notify"
	"github.com/ericfitz/agentbus2/internal/store"
)

// Embedder turns texts into vectors (Decision 51). The core normalizes the
// vectors after Embed returns.
type Embedder interface {
	Model() string
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// Options are the injected collaborators of New.
type Options struct {
	Now      func() time.Time // nil: time.Now (Decision 49)
	Notifier notify.Notifier  // nil: notify.NewMemory()
	Embedder Embedder         // nil: text search only
	Logger   *slog.Logger     // nil: discard
}

// Core is the rule engine over a Store. Safe for concurrent use.
type Core struct {
	Store  store.Store
	Cfg    Config
	Limits *Limiter
	// Owner is random per instance; the maintenance lease owner.
	Owner string
	// EmbedSoon is set by the search aggregate; Send and Edit call it after
	// commit when non-nil.
	EmbedSoon func()
	Log       *slog.Logger

	now      func() time.Time
	notifier notify.Notifier
	embedder Embedder
}

// New validates cfg and builds a Core.
func New(s store.Store, cfg Config, opts Options) (*Core, error) {
	if s == nil {
		return nil, errors.New("core: store is required")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if opts.Embedder != nil {
		if cfg.EmbeddingModel == "" {
			return nil, errors.New("core: embedding_model is required when an embedder is given")
		}
		if cfg.EmbeddingModel != opts.Embedder.Model() {
			return nil, fmt.Errorf("core: embedding_model %q does not match the embedder's model %q", cfg.EmbeddingModel, opts.Embedder.Model())
		}
	}
	owner, err := RandomHex(12)
	if err != nil {
		return nil, err
	}
	c := &Core{Store: s, Cfg: cfg, Owner: owner, now: opts.Now, notifier: opts.Notifier, embedder: opts.Embedder, Log: opts.Logger}
	if c.now == nil {
		c.now = time.Now
	}
	if c.notifier == nil {
		c.notifier = notify.NewMemory()
	}
	if c.Log == nil {
		c.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	c.Limits = NewLimiter(cfg.SendMessagesPerSecond, cfg.SendKiBPerSecond, cfg.MaxMessageKiB)
	return c, nil
}

// Now is the injected clock (Decision 49).
func (c *Core) Now() time.Time { return c.now() }

// NowMs is Now in Unix milliseconds, the unit of every stored time.
func (c *Core) NowMs() int64 { return c.now().UnixMilli() }

// Embedder returns the embedder, nil when none.
func (c *Core) Embedder() Embedder { return c.embedder }

// RandomToken returns n random bytes, base64url without padding.
func RandomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// RandomHex returns n random bytes in hex (v1 internal/bus/bus.go randomToken).
func RandomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
