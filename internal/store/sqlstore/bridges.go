package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/ericfitz/agentbus2/internal/store"
)

// Bridges are new in v2 (Slack bridge spec 8); no v1 source.
//
// Slack ts values are fixed-width ASCII digits around one '.', so the SQL
// comparisons below order them the same under every collation.

type bridgeRow struct {
	ID             int64  `db:"id"`
	ChannelID      int64  `db:"channel_id"`
	ChannelName    string `db:"channel_name"`
	Scope          string `db:"scope"`
	SlackChannelID string `db:"slack_channel_id"`
	SlackName      string `db:"slack_name"`
	State          string `db:"state"`
	CursorTS       string `db:"cursor_ts"`
	CreatedAt      int64  `db:"created_at"`
	ArchivedAt     int64  `db:"archived_at"`
}

const bridgeCols = "id, channel_id, channel_name, scope, slack_channel_id, slack_name, state, cursor_ts, created_at, archived_at"

func (r bridgeRow) bridge() store.Bridge {
	return store.Bridge{ID: r.ID, ChannelID: r.ChannelID, ChannelName: r.ChannelName, Scope: r.Scope,
		SlackChannelID: r.SlackChannelID, SlackName: r.SlackName, State: store.BridgeState(r.State),
		CursorTS: r.CursorTS, CreatedAt: r.CreatedAt, ArchivedAt: r.ArchivedAt}
}

func notFoundBridge(what string) error {
	return fmt.Errorf("%w: bridge %s", store.ErrNotFound, what)
}

func (t *tx) CreateBridge(ctx context.Context, b store.Bridge) (store.Bridge, error) {
	if err := t.writable(); err != nil {
		return store.Bridge{}, err
	}
	if b.State != "" && b.State != store.BridgeCreating {
		return store.Bridge{}, fmt.Errorf("%w: new bridge state %q (want creating)", store.ErrInvalid, b.State)
	}
	if b.ChannelID <= 0 {
		return store.Bridge{}, fmt.Errorf("%w: bridge channel ID %d must be positive", store.ErrInvalid, b.ChannelID)
	}
	if b.ArchivedAt != 0 {
		return store.Bridge{}, fmt.Errorf("%w: a new bridge has no archive time (got %d)", store.ErrInvalid, b.ArchivedAt)
	}
	for _, f := range []struct{ name, v string }{{"channel name", b.ChannelName}, {"Slack name", b.SlackName}, {"scope", b.Scope}} {
		if f.v == "" {
			return store.Bridge{}, fmt.Errorf("%w: bridge %s must not be empty", store.ErrInvalid, f.name)
		}
	}
	if err := checkText("bridge", "channel name", b.ChannelName, "Slack name", b.SlackName, "scope", b.Scope,
		"Slack channel ID", b.SlackChannelID, "cursor", b.CursorTS); err != nil {
		return store.Bridge{}, err
	}
	var id int64
	err := t.getW(ctx, &id, "INSERT INTO bridges (channel_id, channel_name, scope, slack_channel_id, slack_name, state, cursor_ts, created_at) VALUES (?, ?, ?, ?, ?, 'creating', ?, ?) RETURNING id",
		b.ChannelID, b.ChannelName, b.Scope, b.SlackChannelID, b.SlackName, b.CursorTS, b.CreatedAt)
	if isConflict(err) {
		if uniqueOn(err, "bridges.slack_channel_id", "bridges_slack_channel") {
			return store.Bridge{}, fmt.Errorf("%w: Slack channel %q is already used by another bridge", store.ErrConflict, b.SlackChannelID)
		}
		return store.Bridge{}, fmt.Errorf("%w: channel %q is already bridged", store.ErrConflict, b.ChannelName)
	}
	if err != nil {
		return store.Bridge{}, err
	}
	return t.Bridge(ctx, id)
}

func (t *tx) bridgeWhere(ctx context.Context, what, where string, args ...any) (store.Bridge, error) {
	var r bridgeRow
	err := t.get(ctx, &r, "SELECT "+bridgeCols+" FROM bridges WHERE "+where, args...)
	if errors.Is(err, sql.ErrNoRows) {
		return store.Bridge{}, notFoundBridge(what)
	}
	if err != nil {
		return store.Bridge{}, err
	}
	return r.bridge(), nil
}

func (t *tx) Bridge(ctx context.Context, id int64) (store.Bridge, error) {
	return t.bridgeWhere(ctx, fmt.Sprintf("%d", id), "id = ?", id)
}

func (t *tx) BridgeByChannel(ctx context.Context, channelID int64) (store.Bridge, error) {
	return t.bridgeWhere(ctx, fmt.Sprintf("for channel %d", channelID), "channel_id = ?", channelID)
}

// BridgeBySlackChannel answers ErrNotFound for an empty ID: a bridge without
// a Slack channel has no ID to look up.
func (t *tx) BridgeBySlackChannel(ctx context.Context, slackChannelID string) (store.Bridge, error) {
	if slackChannelID == "" || badText(slackChannelID) {
		return store.Bridge{}, notFoundBridge("for Slack channel " + fmt.Sprintf("%q", slackChannelID))
	}
	return t.bridgeWhere(ctx, fmt.Sprintf("for Slack channel %q", slackChannelID), "slack_channel_id = ?", slackChannelID)
}

func (t *tx) listBridges(ctx context.Context, where string, args ...any) ([]store.Bridge, error) {
	var rows []bridgeRow
	if err := t.sel(ctx, &rows, "SELECT "+bridgeCols+" FROM bridges"+where, args...); err != nil {
		return nil, err
	}
	sortByBytes(rows, func(r bridgeRow) string { return r.ChannelName })
	out := make([]store.Bridge, len(rows))
	for i, r := range rows {
		out[i] = r.bridge()
	}
	return out, nil
}

func (t *tx) ListBridges(ctx context.Context) ([]store.Bridge, error) {
	return t.listBridges(ctx, "")
}

func (t *tx) ScopeBridges(ctx context.Context, scope string) ([]store.Bridge, error) {
	if badText(scope) {
		return []store.Bridge{}, nil
	}
	return t.listBridges(ctx, " WHERE scope = ?", scope)
}

func (t *tx) SetBridgeSlackChannel(ctx context.Context, id int64, slackChannelID string) error {
	if err := t.writable(); err != nil {
		return err
	}
	if slackChannelID == "" {
		return fmt.Errorf("%w: Slack channel ID must not be empty", store.ErrInvalid)
	}
	if err := checkText("bridge", "Slack channel ID", slackChannelID); err != nil {
		return err
	}
	b, err := t.Bridge(ctx, id)
	if err != nil {
		return err
	}
	if b.State != store.BridgeCreating {
		return fmt.Errorf("%w: bridge %d is %s, not creating", store.ErrConflict, id, b.State)
	}
	if b.SlackChannelID == slackChannelID {
		return nil
	}
	_, err = t.execW(ctx, "UPDATE bridges SET slack_channel_id = ? WHERE id = ? AND state = 'creating'", slackChannelID, id)
	if isConflict(err) {
		return fmt.Errorf("%w: Slack channel %q is already bridged", store.ErrConflict, slackChannelID)
	}
	return err
}

func (t *tx) SetBridgeLive(ctx context.Context, id int64) error {
	if err := t.writable(); err != nil {
		return err
	}
	b, err := t.Bridge(ctx, id)
	if err != nil {
		return err
	}
	switch b.State {
	case store.BridgeLive:
		return nil
	case store.BridgeArchived:
		return fmt.Errorf("%w: bridge %d is archived", store.ErrConflict, id)
	}
	if b.SlackChannelID == "" {
		return fmt.Errorf("%w: bridge %d has no Slack channel", store.ErrInvalid, id)
	}
	_, err = t.execW(ctx, "UPDATE bridges SET state = 'live' WHERE id = ? AND state = 'creating'", id)
	return err
}

func (t *tx) SetBridgeArchived(ctx context.Context, id, now int64) error {
	if err := t.writable(); err != nil {
		return err
	}
	res, err := t.execW(ctx, "UPDATE bridges SET state = 'archived', archived_at = ? WHERE id = ? AND state <> 'archived'", now, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	_, err = t.Bridge(ctx, id) // already archived, or unknown
	return err
}

// AdvanceBridgeCursor compares in Go so the byte order never depends on the
// engine's collation.
func (t *tx) AdvanceBridgeCursor(ctx context.Context, id int64, ts string) error {
	if err := t.writable(); err != nil {
		return err
	}
	if err := checkText("bridge", "cursor", ts); err != nil {
		return err
	}
	b, err := t.Bridge(ctx, id)
	if err != nil {
		return err
	}
	if ts <= b.CursorTS {
		return nil
	}
	_, err = t.execW(ctx, "UPDATE bridges SET cursor_ts = ? WHERE id = ?", ts, id)
	return err
}

func (t *tx) DeleteBridge(ctx context.Context, id int64) error {
	if err := t.writable(); err != nil {
		return err
	}
	res, err := t.execW(ctx, "DELETE FROM bridges WHERE id = ?", id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return notFoundBridge(fmt.Sprintf("%d", id))
	}
	return nil
}

type bridgeMessageRow struct {
	BridgeID  int64  `db:"bridge_id"`
	Seq       int64  `db:"seq"`
	SlackTS   string `db:"slack_ts"`
	RootTS    string `db:"root_ts"`
	CreatedAt int64  `db:"created_at"`
}

const bridgeMessageCols = "bridge_id, seq, slack_ts, root_ts, created_at"

func (r bridgeMessageRow) message() store.BridgeMessage {
	return store.BridgeMessage{BridgeID: r.BridgeID, Seq: r.Seq, SlackTS: r.SlackTS, RootTS: r.RootTS, CreatedAt: r.CreatedAt}
}

func (t *tx) PutBridgeMessage(ctx context.Context, m store.BridgeMessage) error {
	if err := t.writable(); err != nil {
		return err
	}
	if m.SlackTS == "" || m.RootTS == "" {
		return fmt.Errorf("%w: bridge message ts and root ts must not be empty", store.ErrInvalid)
	}
	if err := checkText("bridge message", "ts", m.SlackTS, "root ts", m.RootTS); err != nil {
		return err
	}
	_, err := t.execW(ctx, "INSERT INTO bridge_messages (bridge_id, seq, slack_ts, root_ts, created_at) VALUES (?, ?, ?, ?, ?)",
		m.BridgeID, m.Seq, m.SlackTS, m.RootTS, m.CreatedAt)
	if isConflict(err) {
		return fmt.Errorf("%w: bridge %d already maps seq %d or ts %q", store.ErrConflict, m.BridgeID, m.Seq, m.SlackTS)
	}
	if isForeignKey(err) {
		return notFoundBridge(fmt.Sprintf("%d", m.BridgeID))
	}
	return err
}

func (t *tx) bridgeMessageWhere(ctx context.Context, what, where string, args ...any) (store.BridgeMessage, error) {
	var r bridgeMessageRow
	err := t.get(ctx, &r, "SELECT "+bridgeMessageCols+" FROM bridge_messages WHERE "+where, args...)
	if errors.Is(err, sql.ErrNoRows) {
		return store.BridgeMessage{}, fmt.Errorf("%w: bridge message %s", store.ErrNotFound, what)
	}
	if err != nil {
		return store.BridgeMessage{}, err
	}
	return r.message(), nil
}

func (t *tx) BridgeMessageBySeq(ctx context.Context, bridgeID, seq int64) (store.BridgeMessage, error) {
	return t.bridgeMessageWhere(ctx, fmt.Sprintf("seq %d of bridge %d", seq, bridgeID), "bridge_id = ? AND seq = ?", bridgeID, seq)
}

func (t *tx) BridgeMessageByTS(ctx context.Context, bridgeID int64, ts string) (store.BridgeMessage, error) {
	if badText(ts) {
		return store.BridgeMessage{}, fmt.Errorf("%w: bridge message ts %q", store.ErrNotFound, ts)
	}
	return t.bridgeMessageWhere(ctx, fmt.Sprintf("ts %q of bridge %d", ts, bridgeID), "bridge_id = ? AND slack_ts = ?", bridgeID, ts)
}

func (t *tx) NewestInThread(ctx context.Context, bridgeID int64, rootTS, beforeTS string) (int64, error) {
	if badText(rootTS, beforeTS) {
		return 0, fmt.Errorf("%w: no message in thread %q", store.ErrNotFound, rootTS)
	}
	var seq sql.NullInt64
	err := t.get(ctx, &seq, "SELECT MAX(seq) FROM bridge_messages WHERE bridge_id = ? AND root_ts = ? AND slack_ts < ?", bridgeID, rootTS, beforeTS)
	if err != nil {
		return 0, err
	}
	if !seq.Valid {
		return 0, fmt.Errorf("%w: no message in thread %q before %q", store.ErrNotFound, rootTS, beforeTS)
	}
	return seq.Int64, nil
}

func (t *tx) ThreadRoots(ctx context.Context, bridgeID int64, sinceTS string, limit int) ([]string, error) {
	if limit < 1 {
		return nil, fmt.Errorf("%w: thread roots limit %d (want at least 1)", store.ErrInvalid, limit)
	}
	if badText(sinceTS) {
		return []string{}, nil
	}
	roots := []string{}
	err := t.sel(ctx, &roots, "SELECT root_ts FROM bridge_messages WHERE bridge_id = ? AND slack_ts >= ? GROUP BY root_ts ORDER BY MAX(slack_ts) DESC, root_ts DESC LIMIT ?",
		bridgeID, sinceTS, limit)
	return roots, err
}

func (t *tx) PutBridgeDrop(ctx context.Context, bridgeID int64, ts string, now int64) error {
	if err := t.writable(); err != nil {
		return err
	}
	if ts == "" {
		return fmt.Errorf("%w: dropped ts must not be empty", store.ErrInvalid)
	}
	if err := checkText("dropped message", "ts", ts); err != nil {
		return err
	}
	_, err := t.execW(ctx, "INSERT INTO bridge_dropped (bridge_id, slack_ts, created_at) VALUES (?, ?, ?) ON CONFLICT (bridge_id, slack_ts) DO NOTHING",
		bridgeID, ts, now)
	if isForeignKey(err) {
		return notFoundBridge(fmt.Sprintf("%d", bridgeID))
	}
	return err
}

func (t *tx) BridgeDropped(ctx context.Context, bridgeID int64, ts string) (bool, error) {
	if badText(ts) {
		return false, nil
	}
	var n int
	if err := t.get(ctx, &n, "SELECT count(*) FROM bridge_dropped WHERE bridge_id = ? AND slack_ts = ?", bridgeID, ts); err != nil {
		return false, err
	}
	return n > 0, nil
}

type slackUserRow struct {
	Identity  string `db:"identity"`
	TeamID    string `db:"team_id"`
	UserID    string `db:"user_id"`
	CreatedAt int64  `db:"created_at"`
}

func (t *tx) SlackUserOf(ctx context.Context, identity string) (store.SlackUser, error) {
	notFound := fmt.Errorf("%w: Slack user of %q", store.ErrNotFound, identity)
	if badText(identity) {
		return store.SlackUser{}, notFound
	}
	var r slackUserRow
	err := t.get(ctx, &r, "SELECT identity, team_id, user_id, created_at FROM slack_users WHERE identity = ?", identity)
	if errors.Is(err, sql.ErrNoRows) {
		return store.SlackUser{}, notFound
	}
	if err != nil {
		return store.SlackUser{}, err
	}
	return store.SlackUser{Identity: r.Identity, TeamID: r.TeamID, UserID: r.UserID, CreatedAt: r.CreatedAt}, nil
}

func (t *tx) IdentityOfSlackUser(ctx context.Context, teamID, userID string) (string, error) {
	notFound := fmt.Errorf("%w: identity of Slack user %q in team %q", store.ErrNotFound, userID, teamID)
	if badText(teamID, userID) {
		return "", notFound
	}
	var name string
	err := t.get(ctx, &name, "SELECT identity FROM slack_users WHERE team_id = ? AND user_id = ?", teamID, userID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", notFound
	}
	return name, err
}

// PutSlackUser looks both keys up first: on Postgres a failed INSERT aborts
// the transaction, so "the same row again" cannot be an insert that fails.
func (t *tx) PutSlackUser(ctx context.Context, u store.SlackUser) error {
	if err := t.writable(); err != nil {
		return err
	}
	if u.TeamID == "" || u.UserID == "" {
		return fmt.Errorf("%w: Slack team and user IDs must not be empty", store.ErrInvalid)
	}
	if err := checkText("Slack user", "team ID", u.TeamID, "user ID", u.UserID); err != nil {
		return err
	}
	if badText(u.Identity) {
		return fmt.Errorf("%w: identity %q", store.ErrNotFound, u.Identity)
	}
	var exists int
	if err := t.get(ctx, &exists, "SELECT count(*) FROM identities WHERE name = ?", u.Identity); err != nil {
		return err
	}
	if exists == 0 {
		return fmt.Errorf("%w: identity %q", store.ErrNotFound, u.Identity)
	}
	cur, err := t.SlackUserOf(ctx, u.Identity)
	switch {
	case err == nil:
		if cur.TeamID == u.TeamID && cur.UserID == u.UserID {
			return nil
		}
		return fmt.Errorf("%w: identity %q is bound to another Slack user", store.ErrConflict, u.Identity)
	case !errors.Is(err, store.ErrNotFound):
		return err
	}
	if owner, err := t.IdentityOfSlackUser(ctx, u.TeamID, u.UserID); err == nil {
		return fmt.Errorf("%w: Slack user %q is bound to identity %q", store.ErrConflict, u.UserID, owner)
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	_, err = t.execW(ctx, "INSERT INTO slack_users (identity, team_id, user_id, created_at) VALUES (?, ?, ?, ?)",
		u.Identity, u.TeamID, u.UserID, u.CreatedAt)
	if isConflict(err) {
		return fmt.Errorf("%w: Slack user %q or identity %q is already bound", store.ErrConflict, u.UserID, u.Identity)
	}
	if isForeignKey(err) {
		return fmt.Errorf("%w: identity %q", store.ErrNotFound, u.Identity)
	}
	return err
}

func (t *tx) DeleteSlackUser(ctx context.Context, identity string) (bool, error) {
	if err := t.writable(); err != nil {
		return false, err
	}
	if badText(identity) {
		return false, nil
	}
	res, err := t.execW(ctx, "DELETE FROM slack_users WHERE identity = ?", identity)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}
