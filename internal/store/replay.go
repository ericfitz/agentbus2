package store

import "context"

// AssertionReplays is the shared single-use record of client assertions
// (Decision (Eric, 2026-10-21), Milestone 7 item 4, option B). RFC 7523
// section 3 lets an authorization server reject a JWT whose jti it has seen;
// the row lives in the database so every replica refuses what another
// accepted. Times are Unix milliseconds.
type AssertionReplays interface {
	// PutAssertionJTI records jti until expiresAt. ErrInvalid: jti empty or
	// failing TextRule, expiresAt <= 0. ErrConflict: the jti is already stored
	// (a replay), enforced by the primary key so concurrent inserters on any
	// number of replicas are serialized by the database. Write transactions
	// only.
	PutAssertionJTI(ctx context.Context, jti string, expiresAt int64) error
	// DeleteAssertionJTIBefore deletes the rows with expires_at < now and
	// returns how many. Idempotent. Write transactions only.
	DeleteAssertionJTIBefore(ctx context.Context, now int64) (int, error)
}
