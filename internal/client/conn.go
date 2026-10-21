package client

import (
	"context"
	"fmt"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/credential"
	"github.com/ericfitz/agentbus2/internal/gen"
	"github.com/ericfitz/agentbus2/internal/profile"
)

// Conn is one open Bus for one resolved profile and identity.
type Conn struct {
	Bus      bus.Bus
	Resolved Resolved
	Client   *gen.ClientWithResponses
	Core     *core.Core         // local mode only
	Source   *credential.Source // server mode only

	close func() error
}

// Open resolves the profile and identity and builds the Bus. In local mode it
// opens (creating) the SQLite file; in server mode nothing is dialed or
// exchanged until the first call.
func Open(ctx context.Context, o Options) (*Conn, error) {
	r, err := Resolve(o)
	if err != nil {
		return nil, err
	}
	switch r.Profile.Mode {
	case profile.ModeLocal:
		return openLocal(ctx, o, r)
	case profile.ModeServer:
		return openServer(o, r)
	}
	return nil, resolveErr(fmt.Errorf("profile %q: unknown mode %q", r.Profile.Name, r.Profile.Mode))
}

// Close releases what Open acquired. A second call does nothing.
func (c *Conn) Close() error {
	if c == nil || c.close == nil {
		return nil
	}
	f := c.close
	c.close = nil
	return f()
}
