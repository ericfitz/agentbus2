package store

import "context"

// MaxJTIBytes bounds a stored assertion jti: the primary key (identity, jti)
// of a longer one can exceed the Postgres btree entry limit (2704 bytes; the
// identity adds at most 128 bytes), and no client needs more than a random
// identifier. assertion.MaxJTIBytes is the same bound at parse time.
const MaxJTIBytes = 256

// AssertionReplays is the shared single-use record of client assertions
// (Decision (Eric, 2026-10-21), Milestone 7 item 4, option B). RFC 7523
// section 3 lets an authorization server reject a JWT whose jti it has seen;
// the (identity, jti) row lives in the database so every replica refuses what another
// accepted. Times are Unix milliseconds.
type AssertionReplays interface {
	// PutAssertionJTI records jti for identity until expiresAt. ErrInvalid:
	// identity failing NameRule, jti empty, longer than MaxJTIBytes or
	// failing TextRule, expiresAt <= 0. ErrConflict: the (identity, jti) pair
	// is already stored (a replay), enforced by the primary key so concurrent
	// inserters on any number of replicas are serialized by the database; the
	// same jti under another identity is not a replay. Write transactions
	// only.
	PutAssertionJTI(ctx context.Context, identity, jti string, expiresAt int64) error
	// DeleteAssertionJTIBefore deletes the rows with expires_at < now and
	// returns how many. Idempotent. Write transactions only.
	DeleteAssertionJTIBefore(ctx context.Context, now int64) (int, error)
}
