package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/ericfitz/agentbus2/internal/store"
)

// Ported from v1 internal/bus/channels.go onto surrogate ids: CreateChannel
// (:137-180: refuse an existing name, created_seq from the caller),
// deleteChannel (:223-307: the three deletes become one cascade),
// RenameChannel (:347-385: one row update, plan choice 6) and ChannelKind
// (:109-121, now Channel.Kind). v1's ensureDefaults, isDefaultChannel and
// reapEmptyChannels are not ported, except that EmptyChannelsBefore and
// DeleteChannelIfEmpty port reapEmptyChannels' condition (Decision (Eric,
// 2026-10-21), Milestone 3 item 8).

type channelRow struct {
	ID               int64  `db:"id"`
	Name             string `db:"name"`
	Kind             string `db:"kind"`
	Creator          string `db:"creator"`
	Scope            string `db:"scope"`
	CreatedSeq       int64  `db:"created_seq"`
	EvictedBeforeSeq int64  `db:"evicted_before_seq"`
	CreatedAt        int64  `db:"created_at"`
	LastReadAt       int64  `db:"last_read_at"`
}

const channelCols = "id, name, kind, creator, COALESCE(scope, '') AS scope, created_seq, evicted_before_seq, created_at, last_read_at"

func (r channelRow) channel() store.Channel {
	return store.Channel{ID: r.ID, Name: r.Name, Kind: store.Kind(r.Kind), Creator: r.Creator, Scope: r.Scope,
		CreatedSeq: r.CreatedSeq, EvictedBeforeSeq: r.EvictedBeforeSeq, CreatedAt: r.CreatedAt, LastReadAt: r.LastReadAt}
}

func channelsOf(rows []channelRow) []store.Channel {
	out := make([]store.Channel, len(rows))
	for i, r := range rows {
		out[i] = r.channel()
	}
	return out
}

// identityRegistered reports whether name is a row of identities; a name
// failing TextRule is not, without a query.
func (t *tx) identityRegistered(ctx context.Context, name string) (bool, error) {
	if badText(name) {
		return false, nil
	}
	var n int
	if err := t.get(ctx, &n, "SELECT count(*) FROM identities WHERE name = ?", name); err != nil {
		return false, err
	}
	return n > 0, nil
}

func (t *tx) CreateChannel(ctx context.Context, c store.ChannelCreate) (store.Channel, error) {
	if err := t.writable(); err != nil {
		return store.Channel{}, err
	}
	if err := store.NameRule(c.Name); err != nil {
		return store.Channel{}, fmt.Errorf("%w: channel name %q %s", store.ErrInvalid, c.Name, err)
	}
	if !store.ValidKind(c.Kind) {
		return store.Channel{}, fmt.Errorf("%w: unknown channel kind %q", store.ErrInvalid, c.Kind)
	}
	if c.Creator != "" {
		ok, err := t.identityRegistered(ctx, c.Creator)
		if err != nil {
			return store.Channel{}, err
		}
		if !ok {
			return store.Channel{}, fmt.Errorf("%w: channel creator %q is not a registered identity", store.ErrInvalid, c.Creator)
		}
	}
	acl := c.ACL
	if acl == nil {
		acl = store.DefaultACL(c.Creator) // plan choice 9
	}
	if err := t.validateACL(ctx, acl); err != nil {
		return store.Channel{}, err
	}
	var scope any
	if c.Scope != "" {
		if badText(c.Scope) {
			return store.Channel{}, fmt.Errorf("%w: scope %q", store.ErrNotFound, c.Scope)
		}
		scope = c.Scope
	}
	var id int64
	err := t.getW(ctx, &id, "INSERT INTO channels (name, kind, creator, scope, created_seq, evicted_before_seq, created_at) VALUES (?, ?, ?, ?, ?, 0, ?) RETURNING id",
		c.Name, string(c.Kind), c.Creator, scope, c.CreatedSeq, c.CreatedAt)
	if isConflict(err) {
		return store.Channel{}, fmt.Errorf("%w: channel %q", store.ErrConflict, c.Name)
	}
	if isForeignKey(err) {
		return store.Channel{}, fmt.Errorf("%w: scope %q", store.ErrNotFound, c.Scope)
	}
	if err != nil {
		return store.Channel{}, err
	}
	if err := t.insertACL(ctx, id, acl); err != nil {
		return store.Channel{}, err
	}
	return t.Channel(ctx, id)
}

func (t *tx) Channel(ctx context.Context, id int64) (store.Channel, error) {
	var r channelRow
	err := t.get(ctx, &r, "SELECT "+channelCols+" FROM channels WHERE id = ?", id)
	if errors.Is(err, sql.ErrNoRows) {
		return store.Channel{}, fmt.Errorf("%w: channel %d", store.ErrNotFound, id)
	}
	if err != nil {
		return store.Channel{}, err
	}
	return r.channel(), nil
}

// ChannelByName answers ErrNotFound for a name failing TextRule.
func (t *tx) ChannelByName(ctx context.Context, name string) (store.Channel, error) {
	if badText(name) {
		return store.Channel{}, fmt.Errorf("%w: channel %q", store.ErrNotFound, name)
	}
	var r channelRow
	err := t.get(ctx, &r, "SELECT "+channelCols+" FROM channels WHERE name = ?", name)
	if errors.Is(err, sql.ErrNoRows) {
		return store.Channel{}, fmt.Errorf("%w: channel %q", store.ErrNotFound, name)
	}
	if err != nil {
		return store.Channel{}, err
	}
	return r.channel(), nil
}

func (t *tx) ListChannels(ctx context.Context) ([]store.Channel, error) {
	var rows []channelRow
	if err := t.sel(ctx, &rows, "SELECT "+channelCols+" FROM channels"); err != nil {
		return nil, err
	}
	sortByBytes(rows, func(r channelRow) string { return r.Name })
	return channelsOf(rows), nil
}

// ListChannelsByKind: ErrInvalid on an unknown kind.
func (t *tx) ListChannelsByKind(ctx context.Context, k store.Kind) ([]store.Channel, error) {
	if !store.ValidKind(k) {
		return nil, fmt.Errorf("%w: unknown channel kind %q", store.ErrInvalid, k)
	}
	var rows []channelRow
	if err := t.sel(ctx, &rows, "SELECT "+channelCols+" FROM channels WHERE kind = ?", string(k)); err != nil {
		return nil, err
	}
	sortByBytes(rows, func(r channelRow) string { return r.Name })
	return channelsOf(rows), nil
}

// channelUpdated maps an UPDATE or DELETE on channels to ErrNotFound when no
// row matched.
func channelUpdated(res sql.Result, err error, id int64) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%w: channel %d", store.ErrNotFound, id)
	}
	return nil
}

func (t *tx) RenameChannel(ctx context.Context, id int64, name string) error {
	if err := t.writable(); err != nil {
		return err
	}
	if err := store.NameRule(name); err != nil {
		return fmt.Errorf("%w: channel name %q %s", store.ErrInvalid, name, err)
	}
	res, err := t.execW(ctx, "UPDATE channels SET name = ? WHERE id = ?", name, id)
	if isConflict(err) {
		return fmt.Errorf("%w: channel %q", store.ErrConflict, name)
	}
	return channelUpdated(res, err, id)
}

// DeleteChannel removes the row; ACL rows, and later the subscriptions,
// cursors and messages, go with it by ON DELETE CASCADE.
func (t *tx) DeleteChannel(ctx context.Context, id int64) error {
	res, err := t.execW(ctx, "DELETE FROM channels WHERE id = ?", id)
	return channelUpdated(res, err, id)
}

// emptyChannelCond is the reaping condition over channels c: ordinary, older
// than the cutoff, no message rows, and no counting subscription (Decision
// 20). A subscription counts only when its identity is not slack-bridge and
// holds a session that is not closed and whose lease has not lapsed before
// the resumable cutoff, so idle subscribers (v1 reapEmptyChannels' liveness
// join, restored with the retention window) do not keep a channel. A channel
// whose bridge is not archived is never offered (spec 6.1b).
const emptyChannelCond = `c.kind = 'ordinary' AND c.created_at < ?
	AND NOT EXISTS (SELECT 1 FROM messages m WHERE m.channel_id = c.id)
	AND NOT EXISTS (SELECT 1 FROM subscriptions s WHERE s.channel_id = c.id
		AND s.identity <> '` + store.SlackBridge + `'
		AND EXISTS (SELECT 1 FROM sessions x WHERE x.identity = s.identity AND x.closed_at = 0 AND x.lease_expires_at >= ?))
	AND NOT EXISTS (SELECT 1 FROM bridges b WHERE b.channel_id = c.id AND b.state <> 'archived')`

func (t *tx) EmptyChannelsBefore(ctx context.Context, createdBefore, resumableAfter int64) ([]store.Channel, error) {
	var rows []channelRow
	if err := t.sel(ctx, &rows, "SELECT "+channelCols+" FROM channels c WHERE "+emptyChannelCond, createdBefore, resumableAfter); err != nil {
		return nil, err
	}
	sortByBytes(rows, func(r channelRow) string { return r.Name })
	return channelsOf(rows), nil
}

func (t *tx) DeleteChannelIfEmpty(ctx context.Context, id, createdBefore, resumableAfter int64) (bool, error) {
	res, err := t.execW(ctx, "DELETE FROM channels WHERE id = ? AND id IN (SELECT c.id FROM channels c WHERE "+emptyChannelCond+")", id, createdBefore, resumableAfter)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// TouchChannelRead: see store.Channels.
func (t *tx) TouchChannelRead(ctx context.Context, id, at int64) error {
	_, err := t.execW(ctx, "UPDATE channels SET last_read_at = ? WHERE id = ? AND last_read_at < ?", at, id, at)
	return err
}

// SetEvictedBefore sets the mark to max(current, seq) with a CASE (no scalar
// max, portability rule).
func (t *tx) SetEvictedBefore(ctx context.Context, id int64, seq int64) error {
	res, err := t.execW(ctx, "UPDATE channels SET evicted_before_seq = CASE WHEN evicted_before_seq < ? THEN ? ELSE evicted_before_seq END WHERE id = ?", seq, seq, id)
	return channelUpdated(res, err, id)
}
