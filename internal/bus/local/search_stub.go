// This file is replaced by Task 9: search.

package local

import (
	"context"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
)

func (b *Bus) Search(ctx context.Context, in ab2bus.SearchInput) (ab2bus.SearchResult, error) {
	return ab2bus.SearchResult{}, ErrNotImplemented
}
