package client

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/ericfitz/agentbus2/internal/bus/remote"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/channels"
	"github.com/ericfitz/agentbus2/internal/core/identities"
	"github.com/ericfitz/agentbus2/internal/core/maintenance"
	"github.com/ericfitz/agentbus2/internal/core/notify"
	"github.com/ericfitz/agentbus2/internal/gen"
	"github.com/ericfitz/agentbus2/internal/profile"
	"github.com/ericfitz/agentbus2/internal/server"
	"github.com/ericfitz/agentbus2/internal/store/sqlite"
)

// defaultPollInterval is v1's WaitPollInterval: how often a Watch re-reads the
// store for messages another process wrote (Decision 68).
const defaultPollInterval = time.Second

// openLocal composes the core, the HTTP handler and the in-process transport
// over the profile's SQLite file (Decisions 35, 36, 68; Milestone 5 choice
// 17). Every process over the file runs its own copy of this composition;
// the polling notifier is how one sees another's writes. The notifier is
// never the in-memory one in production, because its hints cannot cross
// processes.
//
// State (Definition of Done D): a second Open over the same file finds the
// migrations at their version, the identity and general present, and writes
// nothing; two Opens at once are serialized by the Store's migration lock;
// an Open after a crash finds the maintenance lease expired after twice the
// interval.
func openLocal(ctx context.Context, o Options, r Resolved) (*Conn, error) {
	if err := os.MkdirAll(r.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create %s: %w", r.DataDir, err)
	}
	openLogger := o.Logger
	if openLogger == nil {
		openLogger = o.OpenLogger
	}
	st, err := sqlite.Open(ctx, profile.SQLitePath(r.Profile, r.DataDir), sqlite.Options{Logger: openLogger})
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = st.Close()
		}
	}()

	cfg := core.Defaults()
	if o.Config != nil {
		cfg = *o.Config
	}
	n := o.Notifier
	if n == nil {
		poll := o.PollInterval
		if poll == 0 {
			poll = defaultPollInterval
		}
		n = notify.NewPolling(poll)
	}
	logger := o.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	c, err := core.New(st, cfg, core.Options{Now: o.Now, Notifier: n, Logger: logger})
	if err != nil {
		return nil, err
	}
	if err := (channels.Service{Core: c}).EnsureGeneral(ctx); err != nil {
		return nil, fmt.Errorf("creating the general channel: %w", err)
	}
	if err := (identities.Service{Core: c}).RegisterLocal(ctx, r.Identity); err != nil {
		return nil, fmt.Errorf("registering %q: %w", r.Identity, err)
	}

	h := server.New(c, server.Options{Auth: server.InProcess{}, Registration: identities.ModeOpen, Logger: logger})
	hc, err := gen.NewClientWithResponses("http://local", gen.WithHTTPClient(&http.Client{Transport: server.InProcessTransport(h, r.Identity)}))
	if err != nil {
		return nil, err
	}

	stop := func() {}
	if o.Maintenance {
		stop = startMaintenance(c, time.Duration(cfg.MaintenanceIntervalSeconds)*time.Second)
	}
	ok = true
	return &Conn{
		Bus:      remote.New(hc),
		Resolved: r,
		Client:   hc,
		Core:     c,
		close: func() error {
			stop()
			return st.Close()
		},
	}, nil
}

// startMaintenance ticks maintenance.New(c).Tick every interval after a random
// first delay in (0, interval], so processes started together do not tick
// together (ported from v1 internal/mcpserver/server.go StartBackgroundLoops;
// the heartbeat loop is not ported: an armed watch renews the lease, Decision
// 42). The returned function stops the goroutine and waits for it.
func startMaintenance(c *core.Core, interval time.Duration) func() {
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		m := maintenance.New(c)
		first := time.Duration(1 + rand.Int64N(int64(interval)))
		t := time.NewTimer(first)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				m.Tick(ctx)
				t.Reset(interval)
			}
		}
	}()
	return func() { cancel(); wg.Wait() }
}
