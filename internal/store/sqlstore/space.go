package sqlstore

import (
	"context"

	"github.com/ericfitz/agentbus2/internal/store"
)

// Usage: see store.Space.
func (s *Store) Usage(ctx context.Context) (store.Usage, error) { return s.d.Usage(ctx, s.db) }

// Reclaim: see store.Space.
func (s *Store) Reclaim(ctx context.Context) error { return s.d.Reclaim(ctx, s.db) }
