package signingkeys

import (
	"context"
	"errors"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/store"
)

// The admin API forms (issue #6; Eric 2026-10-27 decision 6): the server's
// handlers call them with the authenticated identity and the Service checks
// that it may administer the server inside the transaction, as the other
// administrator operations do. The host command and the scheduler call List,
// Rotate and RotatePrepared, which are trusted and check nothing.

// busKey converts a row to the Bus form. The private key is never copied.
func busKey(r store.SigningKey) bus.SigningKey {
	return bus.SigningKey{
		KID: r.KID, State: r.State, Provider: r.Provider, ProviderRef: r.ProviderRef, Algorithm: r.Algorithm,
		CreatedAt: r.CreatedAt, RetiredAt: r.RetiredAt, RemoveAfter: r.RemoveAfter,
	}
}

// ListSigningKeys returns the key set, the active key first, for an
// administrator.
func (s Service) ListSigningKeys(ctx context.Context, admin string) ([]bus.SigningKey, error) {
	var out []bus.SigningKey
	err := s.Store.Read(ctx, func(ctx context.Context, tx store.Tx) error {
		out = nil
		if err := s.RequireServerAdmin(ctx, tx, admin); err != nil {
			return err
		}
		rows, err := tx.ListSigningKeys(ctx)
		if err != nil {
			return err
		}
		out = make([]bus.SigningKey, len(rows))
		for i, r := range rows {
			out[i] = busKey(r)
		}
		return nil
	})
	if err != nil {
		return nil, core.FromStore(err)
	}
	return out, nil
}

// errReceiptMiss ends the receipt peek of RotateSigningKey when no receipt
// exists, so WithReceipt never stores one from a read transaction.
var errReceiptMiss = errors.New("no receipt")

// RotateSigningKey rotates the active key for an administrator. The order is
// fixed by Decision 26 and the idempotency rule:
//
//  1. a read transaction checks the administrator flag and the receipt of
//     in.IdempotencyKey: a replay returns the original result, and a receipt
//     for another payload is a conflict, both without calling Provider.Next;
//  2. PrepareRotation runs outside every transaction;
//  3. the write transaction checks the flag again, then WithReceipt runs
//     ApplyRotation, which stores the result as the receipt in the same
//     transaction, so a rotation and its receipt commit together or not at all;
//     The result's Previous is the retired key with its retirement times, or,
//     for RevokePrevious, the deleted key as it was;
//  4. OnChange runs after the commit, for a new rotation only.
//
// State (Definition of Done D): a second call with the same key replays; a
// second call without a key rotates again, and answers the cooldown conflict
// unless RevokePrevious is set. Two calls at once with the same key: both may
// call Next, one wins the commit and the other reruns inside WithReceipt and
// replays the winner's result (the unused key is discarded; a store key was
// never persisted). A call that fails after Next leaves the rows and receipts
// as they were, so the retry with the same key runs again.
func (s Service) RotateSigningKey(ctx context.Context, admin string, in bus.RotateSigningKeyInput) (bus.SigningKeyRotation, error) {
	payload := struct {
		RevokePrevious bool `json:"revoke_previous"`
	}{in.RevokePrevious}

	var (
		out      bus.SigningKeyRotation
		replayed bool
	)
	err := s.Store.Read(ctx, func(ctx context.Context, tx store.Tx) error {
		out, replayed = bus.SigningKeyRotation{}, false
		if err := s.RequireServerAdmin(ctx, tx, admin); err != nil {
			return err
		}
		r, err := s.WithReceipt(ctx, tx, admin, in.IdempotencyKey, payload, &out, func() error { return errReceiptMiss })
		if errors.Is(err, errReceiptMiss) {
			return nil
		}
		replayed = r
		return err
	})
	if err != nil {
		return bus.SigningKeyRotation{}, core.FromStore(err)
	}
	if replayed {
		return out, nil
	}

	p, err := s.PrepareRotation(ctx)
	if err != nil {
		return bus.SigningKeyRotation{}, err
	}
	for attempt := 1; ; attempt++ {
		err = s.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
			out, replayed = bus.SigningKeyRotation{}, false
			if err := s.RequireServerAdmin(ctx, tx, admin); err != nil {
				return err
			}
			r, err := s.WithReceipt(ctx, tx, admin, in.IdempotencyKey, payload, &out, func() error {
				res, e := s.ApplyRotation(ctx, tx, p, RotateInput{RevokePrevious: in.RevokePrevious})
				if e != nil {
					return e
				}
				out = bus.SigningKeyRotation{Active: busKey(res.Active), Previous: busKey(res.Previous), Revoked: res.Revoked}
				if out.Previous.KID != "" && !res.Revoked {
					// The retired key as the rotation left it (ApplyRotation retires
					// at the new key's creation time); a deleted key is reported
					// as it was.
					out.Previous.State = store.SigningKeyRetiring
					out.Previous.RetiredAt = res.Active.CreatedAt
					out.Previous.RemoveAfter = RemoveAfter(res.Active.CreatedAt)
				}
				return nil
			})
			replayed = r
			return err
		})
		if rerun(err, attempt) {
			continue
		}
		break
	}
	if err != nil {
		return bus.SigningKeyRotation{}, core.FromStore(err)
	}
	if !replayed {
		s.changed()
	}
	return out, nil
}
