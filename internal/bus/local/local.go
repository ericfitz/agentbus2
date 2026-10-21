// Package local is the in-process Bus: a thin adapter that binds an identity
// to a core.Core and delegates each operation to the aggregate package that
// holds its rules (Decision 7).
package local

import (
	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
)

// Bus is the local implementation of ab2bus.Bus for one authenticated
// identity.
type Bus struct {
	c        *core.Core
	identity string
}

// New binds identity to the core.
func New(c *core.Core, identity string) *Bus { return &Bus{c: c, identity: identity} }

var _ ab2bus.Bus = (*Bus)(nil)
