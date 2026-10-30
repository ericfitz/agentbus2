package bus

// StatusTag is an agent's self-reported state (issue #10). The wire carries a
// plain string: the core is the one validator, so a client that meets a tag
// it does not know passes it through.
type StatusTag string

// The status tags. StatusOffline is set by the bus for an agent with no live
// session and is never accepted from an agent.
const (
	StatusIdle         StatusTag = "idle"
	StatusPlanning     StatusTag = "planning"
	StatusImplementing StatusTag = "implementing"
	StatusReviewing    StatusTag = "reviewing"
	StatusWaiting      StatusTag = "waiting"
	StatusBlocked      StatusTag = "blocked"
	StatusNeedsHuman   StatusTag = "needs-human"
	StatusOffline      StatusTag = "offline" // bus only
)

// SettableStatusTags lists the tags an agent may set, in display order.
func SettableStatusTags() []StatusTag {
	return []StatusTag{
		StatusIdle, StatusPlanning, StatusImplementing, StatusReviewing,
		StatusWaiting, StatusBlocked, StatusNeedsHuman,
	}
}

// AgentStatus is one status revision of one identity.
type AgentStatus struct {
	Identity string
	Revision int64
	// Tag is the stored tag; on AgentStatuses entries it reads offline when
	// the entry is not Live.
	Tag StatusTag
	// LastTag is the stored tag, on AgentStatuses entries only; empty
	// elsewhere (open note: agent-status 2).
	LastTag     StatusTag
	Text        string
	Problem     string
	Session     string
	DisplayName string
	SetAt       int64 // unix ms
}

// AgentStatusEntry is one identity's current status in the agent list.
type AgentStatusEntry struct {
	Identity string
	Kind     IdentityKind
	Live     bool
	Status   AgentStatus
}

// SetAgentStatusInput sets the calling agent's status from one of its live
// sessions.
type SetAgentStatusInput struct {
	Session        string
	Tag            StatusTag
	Text           string
	Problem        string
	IdempotencyKey string
}
