package sqlstore

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/ericfitz/agentbus2/internal/store"
)

// Ported from v1 internal/bus/tasks.go (loadTasks's row selection;
// taskLastActivitySQL). Task JSON, ranks, links and reclaim are the core's.

func (t *tx) LiveMemories(ctx context.Context, channelID int64) ([]store.Message, error) {
	return scanMessages(ctx, t, "SELECT "+messageCols+" FROM messages m WHERE m.channel_id = ? AND m.memory_id IS NOT NULL AND m.tombstone = FALSE ORDER BY m.seq", channelID)
}

// ChannelLastActivity is the greatest of the channel's created_at and its
// messages' created_at and tombstone_at; an unknown channel is ErrNotFound.
func (t *tx) ChannelLastActivity(ctx context.Context, channelID int64) (int64, error) {
	var at sql.NullInt64
	err := t.get(ctx, &at, `SELECT max(x) FROM (
		SELECT created_at AS x FROM channels WHERE id = ?
		UNION ALL SELECT created_at FROM messages WHERE channel_id = ?
		UNION ALL SELECT tombstone_at FROM messages WHERE channel_id = ? AND tombstone_at IS NOT NULL) a`,
		channelID, channelID, channelID)
	if err != nil {
		return 0, err
	}
	if !at.Valid {
		return 0, fmt.Errorf("%w: channel %d", store.ErrNotFound, channelID)
	}
	return at.Int64, nil
}
