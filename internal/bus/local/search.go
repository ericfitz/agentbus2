package local

import (
	"context"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	coresearch "github.com/ericfitz/agentbus2/internal/core/search"
)

// Search delegates to the search aggregate.
func (b *Bus) Search(ctx context.Context, in ab2bus.SearchInput) (ab2bus.SearchResult, error) {
	return coresearch.Service{Core: b.c}.Search(ctx, b.identity, in)
}
