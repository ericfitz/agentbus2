package sqlstore

import (
	"context"
	"fmt"

	"github.com/ericfitz/agentbus2/internal/store"
)

// Ported from v1 internal/bus/memories.go (GetMemory, MemoryRevisions,
// liveRevision, tombstoneMemoryRow's access delete, TouchMemoryAccess) and
// internal/bus/maintenance.go (memoryExpiryChunk's selection). The edit
// sequence itself (tombstone old, append new, SetMemory) is the core's.

func (t *tx) LiveMemory(ctx context.Context, memoryID int64) (store.Message, error) {
	ms, err := scanMessages(ctx, t, "SELECT "+messageCols+" FROM messages m WHERE m.memory_id = ? AND m.tombstone = FALSE ORDER BY m.revision DESC, m.seq DESC LIMIT 1", memoryID)
	if err != nil {
		return store.Message{}, err
	}
	if len(ms) == 0 {
		return store.Message{}, fmt.Errorf("%w: memory %d", store.ErrNotFound, memoryID)
	}
	return ms[0], nil
}

func (t *tx) MemoryRevisions(ctx context.Context, memoryID int64) ([]store.Message, error) {
	return scanMessages(ctx, t, "SELECT "+messageCols+" FROM messages m WHERE m.memory_id = ? ORDER BY m.revision, m.seq", memoryID)
}

func (t *tx) SetMemory(ctx context.Context, seq, memoryID, revision int64) error {
	res, err := t.execW(ctx, "UPDATE messages SET memory_id = ?, revision = ? WHERE seq = ?", memoryID, revision, seq)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%w: message %d", store.ErrNotFound, seq)
	}
	return nil
}

// TouchMemoryAccess upserts one access row per id (the interface's contract;
// v1 only updated existing rows, the core decides which ids to pass).
func (t *tx) TouchMemoryAccess(ctx context.Context, ids []int64, at int64) error {
	if err := t.writable(); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := t.execW(ctx, "INSERT INTO memory_access (memory_id, accessed_at) VALUES (?, ?) ON CONFLICT (memory_id) DO UPDATE SET accessed_at = excluded.accessed_at", id, at); err != nil {
			return err
		}
	}
	return nil
}

func (t *tx) DeleteMemoryAccess(ctx context.Context, memoryID int64) error {
	_, err := t.execW(ctx, "DELETE FROM memory_access WHERE memory_id = ?", memoryID)
	return err
}

// ExpiredMemories lists live memories off task lists whose access is older
// than accessedBefore, ascending by memory id (v1 memoryExpiryChunk: tasks
// never expire).
func (t *tx) ExpiredMemories(ctx context.Context, accessedBefore int64, limit int) ([]store.MemoryRef, error) {
	if err := checkLimit(limit); err != nil {
		return nil, err
	}
	var rows []struct {
		MemoryID int64 `db:"memory_id"`
		Seq      int64 `db:"seq"`
	}
	if err := t.sel(ctx, &rows, `SELECT m.memory_id, m.seq FROM messages m
		JOIN memory_access a ON a.memory_id = m.memory_id
		JOIN channels c ON c.id = m.channel_id
		WHERE m.tombstone = FALSE AND c.kind <> 'tasks' AND a.accessed_at < ?
		ORDER BY m.memory_id, m.seq LIMIT ?`, accessedBefore, limit); err != nil {
		return nil, err
	}
	out := make([]store.MemoryRef, len(rows))
	for i, r := range rows {
		out[i] = store.MemoryRef{MemoryID: r.MemoryID, Seq: r.Seq}
	}
	return out, nil
}
