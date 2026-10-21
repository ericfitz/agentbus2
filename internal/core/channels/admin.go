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
// (pending Eric: Milestone 3 item 3).
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
		model := s.Cfg.EmbeddingModel
		backlog, err := tx.UnembeddedMemories(ctx, model, rules.SearchPageMax)
		if err != nil {
			return err
		}
		st.EmbeddingBacklog = len(backlog)
		count, last, err := tx.EmbedFailures(ctx, model)
		if err != nil {
			return err
		}
		st.EmbeddingRejected, st.EmbeddingRejectedLast = count, last.Error
		if n, ok, err := tx.Notice(ctx, noticeEmbedding); err != nil {
			return err
		} else if ok {
			st.EmbeddingError, st.EmbeddingErrorAt = n.Message, n.SetAt
		}
		return nil
	})
	return st, core.FromStore(err)
}
