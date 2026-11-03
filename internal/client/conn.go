package client

import (
	"context"
	"fmt"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/credential"
	"github.com/ericfitz/agentbus2/internal/gen"
	"github.com/ericfitz/agentbus2/internal/profile"
)

// Conn is one open Bus for one resolved profile and identity.
type Conn struct {
	Bus      bus.Bus
	Resolved Resolved
	Client   *gen.ClientWithResponses
	// Local is set by the LocalOpener that built the Conn; localmode.Core
	// reads it. Nil in server mode.
	Local  any
	Source *credential.Source // server mode, agent identity
	// Human is the sign-in source of a Conn opened with Options.HumanSignIn.
	Human *credential.HumanSource

	close func() error
}

// NewConn builds a Conn for an opener outside this package: b is the Bus, gc
// the generated client behind it (nil when there is none), local the opener's
// own state and closeFn what Close runs once.
func NewConn(b bus.Bus, r Resolved, gc *gen.ClientWithResponses, local any, closeFn func() error) *Conn {
	return &Conn{Bus: b, Resolved: r, Client: gc, Local: local, close: closeFn}
}

// Open resolves the profile and identity and builds the Bus. In local mode it
// calls Options.Local, which opens (creating) the SQLite file; in server mode
// nothing is dialed or exchanged until the first call.
func Open(ctx context.Context, o Options) (*Conn, error) {
	r, err := Resolve(o)
	if err != nil {
		return nil, err
	}
	switch r.Profile.Mode {
	case profile.ModeLocal:
		if o.Local == nil {
			return nil, NoLocalModeError(r.Profile.Name)
		}
		return o.Local(ctx, o, r)
	case profile.ModeServer:
		return openServer(ctx, o, r)
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
