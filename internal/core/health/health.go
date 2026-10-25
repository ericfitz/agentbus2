package health

import (
	"context"
	"errors"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/channels"
	"github.com/ericfitz/agentbus2/internal/store"
)

// Notice kinds the document reads; they match the kinds channels.Status
// reports.
const (
	noticeCapacity  = "capacity"
	noticeEmbedding = "embedding"
)

// Service is the health aggregate over a Core.
type Service struct{ *core.Core }

// unavailable maps a store failure to the retryable unavailable error with a
// fixed message; only
// the core's own checks (not_registered, forbidden) pass through unchanged,
// so any other *bus.Error from the store path is still a store failure.
func (s Service) unavailable(err error) error {
	var be *bus.Error
	if errors.As(err, &be) && (be.Code == bus.CodeNotRegistered || be.Code == bus.CodeForbidden) {
		return be
	}
	// A canceled or timed-out request is the caller giving up, not a store
	// failure: answer unavailable but do not log it as one.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		s.Log.Debug("health: request ended", "err", err)
		return &bus.Error{Code: bus.CodeUnavailable, Retryable: true, Message: "store unreachable"}
	}
	// The probe tier is unauthenticated, so the driver text (a Postgres
	// error names host, user and database; SQLite the file path) goes to the
	// log and never to the caller.
	s.Log.Error("health: store unreachable", "err", err)
	return &bus.Error{Code: bus.CodeUnavailable, Retryable: true, Message: "store unreachable"}
}

// Health builds the document for identity; "" is the probe tier. version is
// the server's version string, passed in so the caller owns where it comes
// from. A store that does not answer is an unavailable error, never a
// document (spec section 3.1, agent decision 3).
func (s Service) Health(ctx context.Context, identity, version string) (bus.Health, error) {
	schema, err := s.Store.SchemaVersion(ctx)
	if err != nil {
		return bus.Health{}, s.unavailable(err)
	}
	tier := bus.TierProbe
	var (
		notices []bus.Notice
		emb     bus.HealthEmbeddings
	)
	err = s.Store.Read(ctx, func(ctx context.Context, tx store.Tx) error {
		notices, emb = nil, bus.HealthEmbeddings{}
		tier = bus.TierProbe
		if identity != "" {
			id, err := s.RequireIdentity(ctx, tx, identity)
			if err != nil {
				return err
			}
			tier = bus.TierIdentity
			if id.Admin {
				tier = bus.TierAdmin
			}
		}
		for _, kind := range []string{noticeCapacity, noticeEmbedding} {
			n, ok, err := tx.Notice(ctx, kind)
			if err != nil {
				return err
			}
			if ok {
				notices = append(notices, bus.Notice{Kind: n.Kind, Message: n.Message, SetAt: n.SetAt})
			}
		}
		if identity != "" {
			rep, err := channels.EmbeddingReport(ctx, tx, s.Core)
			if err != nil {
				return err
			}
			emb = rep
		}
		return nil
	})
	if err != nil {
		return bus.Health{}, s.unavailable(err)
	}

	tick, ticked := s.LastMaintenanceTick()
	degraded := len(notices) > 0 || (ticked && tick.LockErr != "")
	for _, j := range tick.Jobs {
		if j.Err != "" {
			degraded = true
		}
	}
	h := bus.Health{Status: bus.HealthOK, Tier: tier, Version: version, Now: s.NowMs()}
	if degraded {
		h.Status = bus.HealthDegraded
	}
	if tier == bus.TierProbe {
		return h, nil
	}

	h.StartedAt = s.StartedAt().UnixMilli()
	h.Store = &bus.HealthStore{Engine: string(s.Store.Engine()), SchemaVersion: schema}
	if tier != bus.TierAdmin {
		emb.Model, emb.LastError, emb.LastErrorAt, emb.RejectedLast = "", "", 0, ""
		h.Embeddings = &emb
		return h, nil
	}
	h.Embeddings = &emb

	usage, err := channels.Service{Core: s.Core}.Usage(ctx, identity)
	if err != nil {
		return bus.Health{}, s.unavailable(err)
	}
	h.Storage = &usage
	h.Notices = append([]bus.Notice{}, notices...)

	m := &bus.HealthMaintenance{
		IntervalSeconds: s.Cfg.MaintenanceIntervalSeconds,
		Owner:           s.Owner,
		Lock:            bus.LockUnknown,
		Jobs:            []bus.HealthJob{},
	}
	if ticked {
		m.LastTickAt, m.LockError = tick.At, bus.TruncateErrorMessage(tick.LockErr)
		switch {
		case tick.LockErr != "":
			m.Lock = bus.LockUnknown
		case tick.Held:
			m.Lock = bus.LockHeld
		default:
			m.Lock = bus.LockHeldElsewhere
		}
		for _, j := range tick.Jobs {
			m.Jobs = append(m.Jobs, bus.HealthJob{Name: j.Name, Changed: j.Changed, Error: bus.TruncateErrorMessage(j.Err), Skipped: j.Skipped})
		}
	}
	h.Maintenance = m
	return h, nil
}
