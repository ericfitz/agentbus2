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

	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/channels"
	"github.com/ericfitz/agentbus2/internal/core/maintenance"
	"github.com/ericfitz/agentbus2/internal/core/notify"
	"github.com/ericfitz/agentbus2/internal/server"
	"github.com/ericfitz/agentbus2/internal/store"
)

// Config is what Serve needs.
type Config struct {
	Listen      string
	Store       store.Store          // already open; the caller closes it
	Auth        server.Authenticator // nil: RefuseAll
	Notifier    notify.Notifier      // nil: notify.NewMemory()
	Cfg         *core.Config         // nil: core.Defaults()
	Now         func() time.Time     // nil: time.Now (Decision 49)
	ReadTimeout time.Duration        // 0: 30 s (choice 15)
	Logger      *slog.Logger
	Ready       func(addr net.Addr) // called once, when listening
}

const (
	defaultReadTimeout = 30 * time.Second
	shutdownTimeout    = 60 * time.Second
)

// httpServer builds the http.Server with choice 15's values. WriteTimeout is
// 0: the watch handler's own deadline bounds a long-poll.
func httpServer(c Config, h http.Handler) *http.Server {
	rt := c.ReadTimeout
	if rt == 0 {
		rt = defaultReadTimeout
	}
	return &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       rt,
		WriteTimeout:      0,
		IdleTimeout:       120 * time.Second,
	}
}

// Serve runs the server until ctx is done. Start-up order (choice 14):
// core.New, EnsureGeneral, the maintenance goroutine, net.Listen, Ready,
// http.Server. It returns nil after a clean Shutdown (60 s) and after the
// maintenance goroutine has returned.
//
// State (Definition of Done D): a second start over the same store finds the
// migrations at their version and general present, so it writes nothing; two
// servers over one Postgres serialize maintenance on the lease row; two over
// one SQLite file are unsupported (their notifiers cannot see each other); a
// start after a crash finds the lease expired after twice the interval.
func Serve(ctx context.Context, c Config) error {
	if c.Store == nil {
		return errors.New("server: a Store is required")
	}
	cfg := core.Defaults()
	if c.Cfg != nil {
		cfg = *c.Cfg
	}
	cr, err := core.New(c.Store, cfg, core.Options{Now: c.Now, Notifier: c.Notifier, Logger: c.Logger})
	if err != nil {
		return err
	}
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

	ln, err := net.Listen("tcp", c.Listen)
	if err != nil {
		return fmt.Errorf("server: listen: %w", err)
	}
	if c.Ready != nil {
		c.Ready(ln.Addr())
	}
	srv := httpServer(c, server.New(cr, server.Options{Auth: c.Auth, Logger: c.Logger}))

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
