package slackbridge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/scopes"
	"github.com/ericfitz/agentbus2/internal/slackapi"
	"github.com/ericfitz/agentbus2/internal/store"
)

// notReady is the fixed text of the unavailable answer a bridge operation
// gives until the Service has reached Slack once.
const notReady = "the Slack bridge is not connected to Slack yet; retry"

// Config configures a Service. Zero durations take the defaults: PostInterval
// 1 s, LockRenew 10 s, LifecycleInterval 60 s.
type Config struct {
	ArchiveAfterDone                           time.Duration
	MaxMessageChars                            int
	PostInterval, LockRenew, LifecycleInterval time.Duration
	// RetryBase is the first requeue delay of an inbound Slack message that hit
	// a transient failure; it doubles to 30 s. Zero takes 1 s.
	RetryBase time.Duration
	Logger    *slog.Logger
}

const (
	minMessageChars = 200
	maxMessageChars = 3000 // Slack's limit for a section's text
)

func (c Config) withDefaults() (Config, error) {
	if c.ArchiveAfterDone <= 0 {
		return c, errors.New("slackbridge: ArchiveAfterDone must be positive")
	}
	if c.MaxMessageChars < minMessageChars || c.MaxMessageChars > maxMessageChars {
		return c, fmt.Errorf("slackbridge: MaxMessageChars must be between %d and %d, got %d", minMessageChars, maxMessageChars, c.MaxMessageChars)
	}
	for _, d := range []time.Duration{c.PostInterval, c.LockRenew, c.LifecycleInterval, c.RetryBase} {
		if d < 0 {
			return c, errors.New("slackbridge: durations must not be negative")
		}
	}
	if c.PostInterval == 0 {
		c.PostInterval = time.Second
	}
	if c.LockRenew == 0 {
		c.LockRenew = 10 * time.Second
	}
	if c.LifecycleInterval == 0 {
		c.LifecycleInterval = 60 * time.Second
	}
	if c.RetryBase == 0 {
		c.RetryBase = time.Second
	}
	if c.Logger == nil {
		c.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return c, nil
}

// Service is the Slack bridge: the bridge operations the server serves
// (server.Bridges, implemented structurally) and the archive step maintenance
// calls (scopes.Archiver). It never logs a token, a bus message's content or
// a Slack message's text.
type Service struct {
	core *core.Core
	sc   slackapi.Client
	cfg  Config
	log  *slog.Logger

	// createLocks serialize steps 1 and 2 of Create per bus channel within
	// this process, so two overlapping Creates of one channel never race for
	// the Slack name here (Creates of different channels run in parallel);
	// across replicas the resumed row adopts the winner's channel (see Create).
	createLocks keyedLock

	users userCache // users.info answers, 10 minutes

	pace    pacer      // post spacing per Slack channel
	drainMu sync.Mutex // one Drain at a time in this process

	mu      sync.Mutex
	ready   bool
	self    slackapi.Self
	bridged map[int64]bool       // bus channels with a live bridge, for the drain wake
	connGen uint64               // bumped on every Socket Mode (re)connect
	checked map[int64]uint64     // bridge ID to the connGen of its last membership check
	posted  map[postedKey]string // posted but not yet recorded: Slack ts by (bridge, seq)
	// uncertain: a post that failed in a way that may have reached Slack, with
	// the time of the first such attempt (searched for before posting again).
	uncertain map[postedKey]time.Time
	retired   map[int64]bool // archived or removed bridges: nothing is remembered for them
}

var _ scopes.Archiver = (*Service)(nil)

// New validates cfg and calls AuthTest once. Only an invalid_auth answer fails
// New (a wrong token is an operator error). Any other failure (network, 5xx,
// rate limit, another code) returns a Service that is not ready: Run retries
// AuthTest, and until it succeeds every bridge operation answers unavailable
// and ArchiveBridge returns an error, so maintenance retries on its next tick.
func New(ctx context.Context, c *core.Core, sc slackapi.Client, cfg Config) (*Service, error) {
	if c == nil || sc == nil {
		return nil, errors.New("slackbridge: a Core and a Slack client are required")
	}
	cfg, err := cfg.withDefaults()
	if err != nil {
		return nil, err
	}
	s := &Service{core: c, sc: sc, cfg: cfg, log: cfg.Logger}
	if err := s.authenticate(ctx); err != nil {
		var ae *slackapi.APIError
		if errors.As(err, &ae) && ae.Code == "invalid_auth" {
			return nil, errors.New("slack rejected the bot token (invalid_auth)")
		}
		s.log.Warn("slack bridge: auth.test failed; the bridge is not ready", "err", err)
	}
	return s, nil
}

// authenticate calls AuthTest and, on success, marks the Service ready.
func (s *Service) authenticate(ctx context.Context) error {
	self, err := s.sc.AuthTest(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.self, s.ready = self, true
	s.mu.Unlock()
	return nil
}

// Self is the app's identity from auth.test, and whether Slack has been
// reached.
func (s *Service) Self() (slackapi.Self, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.self, s.ready
}

// requireReady answers unavailable until AuthTest has succeeded.
func (s *Service) requireReady() (slackapi.Self, error) {
	self, ok := s.Self()
	if !ok {
		return slackapi.Self{}, core.E(bus.CodeUnavailable, "%s", notReady)
	}
	return self, nil
}

// requireAdmin refuses a caller that is not a server administrator.
func (s *Service) requireAdmin(ctx context.Context, admin string) error {
	err := s.core.Store.Read(ctx, func(ctx context.Context, tx store.Tx) error {
		_, err := s.core.RequireAdmin(ctx, tx, admin)
		return err
	})
	return core.FromStore(err)
}

// guard is the admin check then the readiness check every operation starts
// with.
func (s *Service) guard(ctx context.Context, admin string) (slackapi.Self, error) {
	if err := s.requireAdmin(ctx, admin); err != nil {
		return slackapi.Self{}, err
	}
	return s.requireReady()
}

// slackErr maps an error from a Slack call to the bus error the caller sees.
// The texts name the method and Slack's code, never a token.
func slackErr(err error) error {
	var be *bus.Error
	var rl *slackapi.RateLimitedError
	var ae *slackapi.APIError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &be):
		return err
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	case errors.As(err, &rl):
		return core.E(bus.CodeRateLimited, "Slack is rate limiting the bridge (%s); retry after %s", rl.Method, rl.RetryAfter)
	case errors.As(err, &ae):
		return core.E(bus.CodeUnavailable, "Slack refused %s: %s; retry", ae.Method, ae.Code)
	case errors.Is(err, slackapi.ErrChannelGone):
		return core.E(bus.CodeUnavailable, "the Slack channel is gone or archived")
	}
	return core.E(bus.CodeUnavailable, "Slack could not be reached; retry")
}

func toBus(b store.Bridge) bus.Bridge {
	return bus.Bridge{
		Channel: b.ChannelName, Scope: b.Scope, SlackChannelID: b.SlackChannelID, SlackName: b.SlackName,
		State: bus.BridgeState(b.State), CreatedAt: b.CreatedAt, ArchivedAt: b.ArchivedAt,
	}
}

func noBridge(channel string) error {
	return core.E(bus.CodeNotFound, "channel %q has no bridge", channel)
}

// bridgeOf finds the bridge of the channel name in tx. It matches the stored
// name, so a bridge whose channel was removed behind its back is still found.
func bridgeOf(ctx context.Context, tx store.Tx, channel string) (store.Bridge, error) {
	all, err := tx.ListBridges(ctx)
	if err != nil {
		return store.Bridge{}, err
	}
	for _, b := range all {
		if b.ChannelName == channel {
			return b, nil
		}
	}
	return store.Bridge{}, noBridge(channel)
}

// List answers every bridge, by channel name.
func (s *Service) List(ctx context.Context, admin string) ([]bus.Bridge, error) {
	if _, err := s.guard(ctx, admin); err != nil {
		return nil, err
	}
	var out []bus.Bridge
	err := s.core.Store.Read(ctx, func(ctx context.Context, tx store.Tx) error {
		out = nil
		all, err := tx.ListBridges(ctx)
		for _, b := range all {
			out = append(out, toBus(b))
		}
		return err
	})
	if err != nil {
		return nil, core.FromStore(err)
	}
	return out, nil
}

// Get answers the bridge of a channel, not_found when it has none.
func (s *Service) Get(ctx context.Context, admin, channel string) (bus.Bridge, error) {
	if _, err := s.guard(ctx, admin); err != nil {
		return bus.Bridge{}, err
	}
	var out bus.Bridge
	err := s.core.Store.Read(ctx, func(ctx context.Context, tx store.Tx) error {
		b, err := bridgeOf(ctx, tx, channel)
		out = toBus(b)
		return err
	})
	if err != nil {
		return bus.Bridge{}, core.FromStore(err)
	}
	return out, nil
}
