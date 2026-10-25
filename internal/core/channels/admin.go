package channels

import (
	"context"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/rules"
	"github.com/ericfitz/agentbus2/internal/store"
)

// Notice kinds Status reports.
const (
	noticeCapacity  = "capacity"
	noticeEmbedding = "embedding"
)

// Status is the administrator's view (Decision 73), ported from v1
// internal/bus/admin.go StatusReport: live sessions of every identity, every
// channel with its creator, the server notices and, when an embedder is
// configured, the embedding backlog and rejections. There is no usage figure
// (Decision (Eric, 2026-10-21): Milestone 3 item 3).
func (s Service) Status(ctx context.Context, identity string) (bus.Status, error) {
	var st bus.Status
	err := s.Store.Read(ctx, func(ctx context.Context, tx store.Tx) error {
		st = bus.Status{Sessions: []bus.SessionInfo{}, Channels: []bus.ChannelInfo{}, Notices: []bus.Notice{}}
		if _, err := s.RequireAdmin(ctx, tx, identity); err != nil {
			return err
		}
		sessions, err := tx.ListSessions(ctx, "")
		if err != nil {
			return err
		}
		now := s.NowMs()
		for _, ss := range sessions {
			if s.IsLive(ss, now) {
				st.Sessions = append(st.Sessions, bus.SessionInfo{
					ID: ss.ID, Identity: ss.Identity, DisplayName: ss.DisplayName, OpenedAt: ss.OpenedAt,
				})
			}
		}
		chs, err := tx.ListChannels(ctx)
		if err != nil {
			return err
		}
		for _, c := range chs {
			st.Channels = append(st.Channels, core.ChannelInfoToBus(c))
		}
		for _, kind := range []string{noticeCapacity, noticeEmbedding} {
			n, ok, err := tx.Notice(ctx, kind)
			if err != nil {
				return err
			}
			if ok {
				st.Notices = append(st.Notices, bus.Notice{Kind: n.Kind, Message: n.Message, SetAt: n.SetAt})
			}
		}
		if s.Embedder() == nil {
			return nil
		}
		rep, err := EmbeddingReport(ctx, tx, s.Core)
		if err != nil {
			return err
		}
		st.EmbeddingBacklog, st.EmbeddingRejected, st.EmbeddingRejectedLast = rep.Backlog, rep.Rejected, rep.RejectedLast
		st.EmbeddingError, st.EmbeddingErrorAt = rep.LastError, rep.LastErrorAt
		return nil
	})
	return st, core.FromStore(err)
}

// EmbeddingReport reads the embedding state inside tx: off when c has no
// embedder (counts 0, other members empty); else the backlog (capped at
// rules.SearchPageMax), the rejection count and latest reason, the model and
// the embedding notice, with state failing while that notice is set. Status
// and the health document both call it, so the two views cannot drift.
func EmbeddingReport(ctx context.Context, tx store.Tx, c *core.Core) (bus.HealthEmbeddings, error) {
	if c.Embedder() == nil {
		return bus.HealthEmbeddings{State: bus.EmbeddingsOff}, nil
	}
	model := c.Cfg.EmbeddingModel
	rep := bus.HealthEmbeddings{State: bus.EmbeddingsOK, Model: model}
	backlog, err := tx.UnembeddedMemories(ctx, model, rules.SearchPageMax)
	if err != nil {
		return bus.HealthEmbeddings{}, err
	}
	rep.Backlog = len(backlog)
	count, last, err := tx.EmbedFailures(ctx, model)
	if err != nil {
		return bus.HealthEmbeddings{}, err
	}
	rep.Rejected, rep.RejectedLast = count, last.Error
	n, ok, err := tx.Notice(ctx, noticeEmbedding)
	if err != nil {
		return bus.HealthEmbeddings{}, err
	}
	if ok {
		rep.State, rep.LastError, rep.LastErrorAt = bus.EmbeddingsFailing, n.Message, n.SetAt
	}
	return rep, nil
}
