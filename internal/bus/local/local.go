// Package local is the in-process Bus: a thin adapter that binds an identity
// to a core.Core and delegates each operation to the aggregate package that
// holds its rules (Decision 7). Methods not yet implemented live in
// *_stub.go files and return ErrNotImplemented until the task that owns them
// replaces the stub.
package local

import (
	"errors"

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

// ErrNotImplemented is returned by the stubs; Task 13 deletes it.
var ErrNotImplemented = errors.New("local: not implemented in this milestone")
