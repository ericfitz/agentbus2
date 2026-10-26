package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/ericfitz/agentbus2/internal/store"
)

// ACL rows are new in v2 (Decisions 73, 80, 88, 96; plan choice 2): one
// channel_acl row per stored entry with three boolean columns. No v1 source.

type aclRow struct {
	Identity string `db:"identity"`
	Read     bool   `db:"can_read"`
	Write    bool   `db:"can_write"`
	Admin    bool   `db:"can_admin"`
}

// validateACL checks every entry with store.ValidateEntry, then the Decision
// 96 registry rule (everyone, admins and humans are exempt), before anything is
// written.
func (t *tx) validateACL(ctx context.Context, entries []store.ACLEntry) error {
	for _, e := range entries {
		if err := store.ValidateEntry(e); err != nil {
			return err
		}
	}
	for _, e := range entries {
		if e.Identity == store.Everyone || e.Identity == store.Admins || e.Identity == store.Humans {
			continue
		}
		ok, err := t.identityRegistered(ctx, e.Identity)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%w: ACL identity %q is not a registered identity", store.ErrInvalid, e.Identity)
		}
	}
	return nil
}

func (t *tx) insertACL(ctx context.Context, channelID int64, entries []store.ACLEntry) error {
	for _, e := range entries {
		var p store.Permissions
		for _, perm := range e.Permissions {
			switch perm {
			case store.PermRead:
				p.Read = true
			case store.PermWrite:
				p.Write = true
			case store.PermAdmin:
				p.Admin = true
			default:
				return fmt.Errorf("%w: unknown permission %q", store.ErrInvalid, perm)
			}
		}
		if _, err := t.execW(ctx, "INSERT INTO channel_acl (channel_id, identity, can_read, can_write, can_admin) VALUES (?, ?, ?, ?, ?)",
			channelID, e.Identity, p.Read, p.Write, p.Admin); err != nil {
			return err
		}
	}
	return nil
}

// channelExists returns ErrNotFound unless the channel row exists.
func (t *tx) channelExists(ctx context.Context, id int64) error {
	var n int
	if err := t.get(ctx, &n, "SELECT count(*) FROM channels WHERE id = ?", id); err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%w: channel %d", store.ErrNotFound, id)
	}
	return nil
}

// ACL returns the stored entries in insertion order. An unknown channel has
// no rows.
func (t *tx) ACL(ctx context.Context, id int64) ([]store.ACLEntry, error) {
	var rows []aclRow
	if err := t.sel(ctx, &rows, "SELECT identity, can_read, can_write, can_admin FROM channel_acl WHERE channel_id = ? ORDER BY id", id); err != nil {
		return nil, err
	}
	out := make([]store.ACLEntry, len(rows))
	for i, r := range rows {
		e := store.ACLEntry{Identity: r.Identity}
		if r.Read {
			e.Permissions = append(e.Permissions, store.PermRead)
		}
		if r.Write {
			e.Permissions = append(e.Permissions, store.PermWrite)
		}
		if r.Admin {
			e.Permissions = append(e.Permissions, store.PermAdmin)
		}
		out[i] = e
	}
	return out, nil
}

// SetACL validates every entry, then replaces the stored ACL. Zero entries is
// valid and clears it; the implicit entries still apply.
func (t *tx) SetACL(ctx context.Context, id int64, entries []store.ACLEntry) error {
	if err := t.writable(); err != nil {
		return err
	}
	if err := t.validateACL(ctx, entries); err != nil {
		return err
	}
	if err := t.channelExists(ctx, id); err != nil {
		return err
	}
	if _, err := t.execW(ctx, "DELETE FROM channel_acl WHERE channel_id = ?", id); err != nil {
		return err
	}
	return t.insertACL(ctx, id, entries)
}

// ACLRowsFor returns the stored rows whose identity is one of names, in
// (channel_id, identity) order, with no rule applied. A name failing TextRule
// matches nothing.
func (t *tx) ACLRowsFor(ctx context.Context, names ...string) ([]store.ACLRow, error) {
	seen := make(map[string]bool, len(names))
	var args []any
	for _, n := range names {
		if badText(n) || seen[n] {
			continue
		}
		seen[n] = true
		args = append(args, n)
	}
	if len(args) == 0 {
		return nil, nil
	}
	q := "SELECT channel_id, identity, can_read, can_write, can_admin FROM channel_acl WHERE identity IN (?" +
		strings.Repeat(", ?", len(args)-1) + ") ORDER BY channel_id, identity"
	var rows []struct {
		ChannelID int64  `db:"channel_id"`
		Identity  string `db:"identity"`
		Read      bool   `db:"can_read"`
		Write     bool   `db:"can_write"`
		Admin     bool   `db:"can_admin"`
	}
	if err := t.sel(ctx, &rows, q, args...); err != nil {
		return nil, err
	}
	out := make([]store.ACLRow, len(rows))
	for i, r := range rows {
		out[i] = store.ACLRow{ChannelID: r.ChannelID, Identity: r.Identity, Read: r.Read, Write: r.Write, Admin: r.Admin}
	}
	return out, nil
}

func (t *tx) isAdmin(ctx context.Context, name string) (bool, error) {
	if badText(name) {
		return false, nil
	}
	var n int
	if err := t.get(ctx, &n, "SELECT count(*) FROM identities WHERE name = ? AND is_admin = TRUE", name); err != nil {
		return false, err
	}
	return n > 0, nil
}

// EffectivePermissions is the union of the stored rows for the identity,
// everyone, and admins (when the identity is admin), plus the implicit
// entries: the creator and every admin hold admin (Decisions 73, 96). An
// unregistered identity is not admin. An identity failing TextRule is such an
// identity: everyone is bound in its place (a repeat matches nothing new).
func (t *tx) EffectivePermissions(ctx context.Context, id int64, identity string) (store.Permissions, error) {
	var creator string
	if err := t.get(ctx, &creator, "SELECT creator FROM channels WHERE id = ?", id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return store.Permissions{}, fmt.Errorf("%w: channel %d", store.ErrNotFound, id)
		}
		return store.Permissions{}, err
	}
	admin, err := t.isAdmin(ctx, identity)
	if err != nil {
		return store.Permissions{}, err
	}
	adminsName := store.Everyone // a repeat of everyone matches nothing new
	if admin {
		adminsName = store.Admins
	}
	who := identity
	if badText(who) {
		who = store.Everyone
	}
	var rows []aclRow
	if err := t.sel(ctx, &rows, "SELECT identity, can_read, can_write, can_admin FROM channel_acl WHERE channel_id = ? AND identity IN (?, ?, ?)",
		id, who, store.Everyone, adminsName); err != nil {
		return store.Permissions{}, err
	}
	var p store.Permissions
	for _, r := range rows {
		p = p.Union(store.Permissions{Read: r.Read, Write: r.Write, Admin: r.Admin})
	}
	if admin || (creator != "" && creator == identity) {
		p.Admin = true
	}
	return p, nil
}

// ReadableChannels lists the ids where EffectivePermissions.Read holds, in
// one query. Implicit entries grant admin, not read. An identity failing
// TextRule is unregistered: everyone is bound in its place (no identity is
// named everyone, a reserved name).
func (t *tx) ReadableChannels(ctx context.Context, identity string) ([]int64, error) {
	if badText(identity) {
		identity = store.Everyone
	}
	var ids []int64
	err := t.sel(ctx, &ids, `SELECT c.id FROM channels c WHERE EXISTS (
  SELECT 1 FROM channel_acl a WHERE a.channel_id = c.id AND a.can_read = TRUE AND (
    a.identity = ? OR a.identity = ? OR
    (a.identity = ? AND EXISTS (SELECT 1 FROM identities i WHERE i.name = ? AND i.is_admin = TRUE))
  )) ORDER BY c.id`, identity, store.Everyone, store.Admins, identity)
	return ids, err
}
