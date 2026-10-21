package server

import (
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/channels"
	"github.com/ericfitz/agentbus2/internal/core/delivery"
	"github.com/ericfitz/agentbus2/internal/core/memories"
	"github.com/ericfitz/agentbus2/internal/core/messaging"
	"github.com/ericfitz/agentbus2/internal/core/search"
	"github.com/ericfitz/agentbus2/internal/core/sessions"
	"github.com/ericfitz/agentbus2/internal/core/tasks"
	"github.com/ericfitz/agentbus2/internal/gen"
	"github.com/ericfitz/agentbus2/internal/version"
)

// api implements gen.StrictServerInterface. Its services are used by the
// aggregate handlers of Tasks 5 to 8, which replace the stub files.
type api struct {
	core    *core.Core
	opts    Options
	version string

	channels  channels.Service
	sessions  sessions.Service
	delivery  delivery.Service
	messaging messaging.Service
	search    search.Service
	memories  memories.Service
	tasks     tasks.Service
}

var _ gen.StrictServerInterface = (*api)(nil)

func newAPI(c *core.Core, o Options) *api {
	v := o.Version
	if v == "" {
		v = version.Version
	}
	sess := sessions.Service{Core: c}
	return &api{
		core: c, opts: o, version: v,
		channels:  channels.Service{Core: c},
		sessions:  sess,
		delivery:  delivery.Service{Core: c, Sessions: sess},
		messaging: messaging.Service{Core: c},
		search:    search.Service{Core: c},
		memories:  memories.Service{Core: c},
		tasks:     tasks.Service{Core: c},
	}
}
