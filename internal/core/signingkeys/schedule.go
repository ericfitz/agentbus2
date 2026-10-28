package signingkeys

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/keyprovider"
	"github.com/ericfitz/agentbus2/internal/store"
)

// RotateDue is the scheduled rotation (the "signing key rotation" job; Eric
// 2026-10-27 decisions 5 and 11). It returns 1 when it rotated, else 0.
//
// Settings.Interval 0 turns it off. Without an active row it does nothing
// (EnsureActive creates the first key at start). A configured provider that is
// not the active key's is an error and nothing rotates.
//
// A store key is rotated once it is as old as the interval; younger than that
// it costs one read and no Next call. An external key cannot be rotated by
// the server, so every run asks the provider for its next key: the active key
// again means 0 (with an error naming the stale key once it is older than the
// interval); a different key is adopted as the new active key. A rotation
// refused by the cooldown, or one whose next key turned out to be the active
// key, is 0 and not an error: the next run tries again.
//
// State (Definition of Done D): a second run finds the new key young (or the
// repointed key adopted) and answers 0; two runs at once end in one rotation
// (the loser reruns and meets the cooldown or the new active key); a run that
// fails after Next leaves the rows as they were.
func (s Service) RotateDue(ctx context.Context, now int64) (int, error) {
	if s.Settings.Interval <= 0 {
		return 0, nil
	}
	var active *store.SigningKey
	err := s.Store.Read(ctx, func(ctx context.Context, tx store.Tx) error {
		active = nil
		rows, err := tx.ListSigningKeys(ctx)
		if err != nil {
			return err
		}
		if len(rows) > 0 && rows[0].State == store.SigningKeyActive {
			active = &rows[0]
		}
		return nil
	})
	if err != nil {
		return 0, core.FromStore(err)
	}
	if active == nil {
		return 0, nil
	}
	if err := s.checkActive(*active); err != nil {
		return 0, err
	}
	age := time.Duration(now-active.CreatedAt) * time.Millisecond
	if s.Provider.Name() == keyprovider.NameStore && age < s.Settings.Interval {
		return 0, nil
	}
	p, err := s.PrepareRotation(ctx)
	if err != nil {
		return 0, err
	}
	if p.KID() == active.KID {
		return 0, s.stale(*active, age)
	}
	switch _, err := s.RotatePrepared(ctx, p, RotateInput{}); {
	case err == nil:
		return 1, nil
	case errors.Is(err, ErrCooldown):
		return 0, nil
	case errors.Is(err, ErrRotateNoChange):
		return 0, s.stale(*active, age)
	default:
		return 0, err
	}
}

// stale is the error of an external active key the provider still names once
// it is older than the interval; nil while it is younger (or for a store key,
// which rotates itself).
func (s Service) stale(active store.SigningKey, age time.Duration) error {
	if s.Provider.Name() == keyprovider.NameStore || age < s.Settings.Interval {
		return nil
	}
	return fmt.Errorf("signing key %s is older than signing_key_rotation_hours (%d): point the key URI at a new key, then run agentbus2 server admin signing-key rotate",
		active.KID, int64(s.Settings.Interval/time.Hour))
}

// RemoveExpired is the "signing key removal" job: it deletes the retiring
// rows whose remove_after has passed and returns how many. The active key is
// never removed. State: a second run deletes nothing; two runs at once delete
// each row once; a crash leaves the rows for the next run.
func (s Service) RemoveExpired(ctx context.Context, now int64) (int, error) {
	var n int
	err := s.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		var e error
		n, e = tx.DeleteSigningKeysBefore(ctx, now)
		return e
	})
	if err != nil {
		return 0, core.FromStore(err)
	}
	if n > 0 {
		s.changed()
	}
	return n, nil
}
