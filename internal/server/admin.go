package server

import (
	"context"

	"github.com/ericfitz/agentbus2/internal/gen"
)

func (a *api) AdminStatus(ctx context.Context, _ gen.AdminStatusRequestObject) (gen.AdminStatusResponseObject, error) {
	st, err := a.channels.Status(ctx, IdentityFrom(ctx))
	if err != nil {
		return nil, err
	}
	out := gen.Status{
		Sessions:          make([]gen.SessionInfo, len(st.Sessions)),
		Channels:          make([]gen.ChannelInfo, len(st.Channels)),
		Notices:           make([]gen.Notice, len(st.Notices)),
		EmbeddingBacklog:  st.EmbeddingBacklog,
		EmbeddingRejected: st.EmbeddingRejected,
	}
	for i, s := range st.Sessions {
		out.Sessions[i] = gen.SessionInfo{Id: s.ID, Identity: s.Identity, DisplayName: s.DisplayName, OpenedAt: s.OpenedAt}
	}
	for i, c := range st.Channels {
		out.Channels[i] = channelInfoToGen(c)
	}
	for i, n := range st.Notices {
		out.Notices[i] = gen.Notice{Kind: n.Kind, Message: n.Message, SetAt: n.SetAt}
	}
	if st.EmbeddingRejectedLast != "" {
		out.EmbeddingRejectedLast = &st.EmbeddingRejectedLast
	}
	if st.EmbeddingError != "" {
		out.EmbeddingError = &st.EmbeddingError
	}
	if st.EmbeddingErrorAt != 0 {
		out.EmbeddingErrorAt = &st.EmbeddingErrorAt
	}
	return gen.AdminStatus200JSONResponse(out), nil
}
