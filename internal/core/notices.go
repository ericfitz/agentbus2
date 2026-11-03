package core

import (
	"context"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/store"
)

// The operator notice kinds (the Store's notices table). Status and the health
// document read every kind of NoticeKinds, so a new kind is added here once.
const (
	NoticeCapacity   = "capacity"
	NoticeEmbedding  = "embedding"
	NoticeInspection = "inspection"
)

// NoticeKinds lists the notice kinds Status and the health document report, in
// their reporting order.
func NoticeKinds() []string {
	return []string{NoticeCapacity, NoticeEmbedding, NoticeInspection}
}

// Notices reads the notices of NoticeKinds inside tx, in that order. An
// inspection notice that no failing replica has rewritten for
// Cfg.InspectionNoticeTTLSeconds (now - SetAt >= the TTL) is left out; a notice
// set in the future stays. The other kinds never expire.
func (c *Core) Notices(ctx context.Context, tx store.Tx) ([]bus.Notice, error) {
	now := c.Now().UnixMilli()
	ttl := int64(c.Cfg.InspectionNoticeTTLSeconds) * 1000
	var out []bus.Notice
	for _, kind := range NoticeKinds() {
		n, ok, err := tx.Notice(ctx, kind)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		if kind == NoticeInspection && now-n.SetAt >= ttl {
			continue
		}
		out = append(out, bus.Notice{Kind: n.Kind, Message: n.Message, SetAt: n.SetAt})
	}
	return out, nil
}
