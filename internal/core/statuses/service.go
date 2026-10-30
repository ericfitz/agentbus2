// Package statuses holds the agent status rules (issue #10): an agent sets
// its own one-line status from a live session, the bus keeps the newest
// StatusHistoryCount revisions per identity, and a reader lists every visible
// identity's latest status, with offline derived from "no live session".
//
// open note: agent-status 11: the aggregate is this package (the delivery
// shape: a Core plus the session service); sessions.Service.Identities reads
// the latest rows itself, so sessions never imports statuses.
package statuses

import (
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/sessions"
)

// Service is the agent status aggregate over a Core.
type Service struct {
	*core.Core
	Sessions sessions.Service
}
