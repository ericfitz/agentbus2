package remote

import (
	"context"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
)

var _ ab2bus.Admin = (*Bus)(nil)

// Status is the administrator's view. DeleteChannel and RenameChannel, the
// other Admin methods, are the Messaging ones (channels.go): the same
// generated operations (Decision 89).
func (b *Bus) Status(ctx context.Context) (ab2bus.Status, error) {
	r, err := b.c.AdminStatusWithResponse(ctx)
	if err != nil {
		return ab2bus.Status{}, errorOf(ctx, 0, nil, nil, err)
	}
	if err := errorOf(ctx, r.StatusCode(), r.ApplicationproblemJSONDefault, r.Body, nil); err != nil {
		return ab2bus.Status{}, err
	}
	if r.JSON200 == nil {
		return ab2bus.Status{}, unparsable(r.StatusCode())
	}
	g := *r.JSON200
	st := ab2bus.Status{
		Sessions:              make([]ab2bus.SessionInfo, len(g.Sessions)),
		Channels:              make([]ab2bus.ChannelInfo, len(g.Channels)),
		Notices:               make([]ab2bus.Notice, len(g.Notices)),
		EmbeddingBacklog:      g.EmbeddingBacklog,
		EmbeddingRejected:     g.EmbeddingRejected,
		EmbeddingRejectedLast: deref(g.EmbeddingRejectedLast),
		EmbeddingError:        deref(g.EmbeddingError),
		EmbeddingErrorAt:      deref(g.EmbeddingErrorAt),
	}
	for i, s := range g.Sessions {
		st.Sessions[i] = ab2bus.SessionInfo{ID: s.Id, Identity: s.Identity, DisplayName: s.DisplayName, OpenedAt: s.OpenedAt}
	}
	for i, c := range g.Channels {
		st.Channels[i] = fromGenChannelInfo(c)
	}
	for i, n := range g.Notices {
		st.Notices[i] = ab2bus.Notice{Kind: n.Kind, Message: n.Message, SetAt: n.SetAt}
	}
	return st, nil
}
