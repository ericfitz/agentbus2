// This file is replaced by Task 4: Status.

package local

import (
	"context"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
)

func (b *Bus) Status(ctx context.Context) (ab2bus.Status, error) {
	return ab2bus.Status{}, ErrNotImplemented
}
