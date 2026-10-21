package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/ericfitz/agentbus2/internal/auth/token"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/channels"
	"github.com/ericfitz/agentbus2/internal/core/embed"
	"github.com/ericfitz/agentbus2/internal/core/identities"
	"github.com/ericfitz/agentbus2/internal/core/maintenance"
	"github.com/ericfitz/agentbus2/internal/core/notify"
	"github.com/ericfitz/agentbus2/internal/server"
	"github.com/ericfitz/agentbus2/internal/store"
)

// Config is what Serve needs.
type Config struct {
	Listen      string
	Listener    net.Listener         // non-nil: Serve uses it instead of calling Listen, and closes it
	Store       store.Store          // already open; the caller closes it
	Auth        server.Authenticator // nil: RefuseAll
	Notifier    notify.Notifier      // nil: notify.NewMemory()
	Cfg         *core.Config         // nil: core.Defaults()
	Now         func() time.Time     // nil: time.Now (Decision 49)
	ReadTimeout time.Duration        // 0: 30 s (choice 15)
	Logger      *slog.Logger         // nil: discard
	Ready       func(addr net.Addr)  // called once, when listening

	// Registration is the register handler's mode (Decisions 20 and 72). It
	// is required: Serve refuses "" and any value identities.ParseMode
	// rejects, so a mode is never open by omission.
	Registration identities.Mode
	Issuer       *token.Issuer     // nil: local mode, the token endpoint answers 503
	External     []*token.External // configured external issuers (Decision 57)
	Embedder     core.Embedder     // nil: text search only; embed.Install when set
}

const (
	defaultReadTimeout = 30 * time.Second
	shutdownTimeout    = 60 * time.Second
)

// httpServer builds the http.Server with choice 15's values. WriteTimeout is
// 0: the watch handler's own deadline bounds a long-poll. net/http's own
// errors go to the Logger at error level (or nowhere, like the core's).
func httpServer(c Config, h http.Handler) *http.Server {
	rt := c.ReadTimeout
	if rt == 0 {
		rt = defaultReadTimeout
	}
	log := c.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &http.Server{
		Handler:           h,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelError),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       rt,
		WriteTimeout:      0,
		IdleTimeout:       120 * time.Second,
	}
}

// Serve runs the server until ctx is done. Start-up order (choice 14):
// core.New, EnsureGeneral, the maintenance goroutine, Listen (unless
// Config.Listener is set), Ready, http.Server. It returns nil after a clean Shutdown (60 s) and after the
// maintenance goroutine has returned.
//
// State (Definition of Done D): a second start over the same store finds the
// migrations at their version and general present, so it writes nothing; two
// servers over one Postgres serialize maintenance on the lease row; two over
// one SQLite file are unsupported (their notifiers cannot see each other); a
// start after a crash finds the lease expired after twice the interval.
func Serve(ctx context.Context, c Config) error {
	if c.Listener != nil {
		// http.Server.Serve closes it once serving; this covers the early
		// returns before that, and a second Close is harmless.
		defer func() { _ = c.Listener.Close() }()
	}
	if c.Store == nil {
		return errors.New("server: a Store is required")
	}
	if _, err := identities.ParseMode(string(c.Registration)); err != nil {
		return fmt.Errorf("server: registration mode: %w", err)
	}
	cfg := core.Defaults()
	if c.Cfg != nil {
		cfg = *c.Cfg
	}
	cr, err := core.New(c.Store, cfg, core.Options{Now: c.Now, Notifier: c.Notifier, Embedder: c.Embedder, Logger: c.Logger})
	if err != nil {
		return err
	}
	embed.Install(cr)
	if err := (channels.Service{Core: cr}).EnsureGeneral(ctx); err != nil {
		return fmt.Errorf("server: creating the general channel: %w", err)
	}

	mctx, stopMaint := context.WithCancel(ctx)
	var wg sync.WaitGroup
	defer func() { stopMaint(); wg.Wait() }()
	wg.Add(1)
	go func() {
		defer wg.Done()
		m := maintenance.New(cr)
		t := time.NewTicker(time.Duration(cfg.MaintenanceIntervalSeconds) * time.Second)
		defer t.Stop()
		for {
			select {
			case <-mctx.Done():
				return
			case <-t.C:
				m.Tick(mctx)
			}
		}
	}()

	ln := c.Listener
	if ln == nil {
		var err error
		if ln, err = Listen(c.Listen); err != nil {
			return fmt.Errorf("server: listen: %w", err)
		}
	}
	if c.Ready != nil {
		c.Ready(ln.Addr())
	}
	srv := httpServer(c, server.New(cr, server.Options{Auth: c.Auth, Logger: c.Logger, Registration: c.Registration, Issuer: c.Issuer, External: c.External}))

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		_ = srv.Close()
		return err
	}
	if err := <-errc; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
