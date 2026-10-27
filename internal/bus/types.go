package bus

// Kind is a channel's kind (Decision 76).
type Kind string

// The channel kinds.
const (
	KindOrdinary Kind = "ordinary"
	KindMemory   Kind = "memory"
	KindTasks    Kind = "tasks"
)

// Permission is one channel ACL permission (Decision 80).
type Permission string

// The permissions.
const (
	PermRead  Permission = "read"
	PermWrite Permission = "write"
	PermAdmin Permission = "admin"
)

// ACLEntry grants Permissions on a channel to one Identity.
type ACLEntry struct {
	Identity    string
	Permissions []Permission
}

// ChannelCapability is a trait of a channel. A value present in
// Channel.Capabilities means the trait applies; clients ignore values they do
// not know.
type ChannelCapability string

// ChannelBridged is present exactly while the channel's Slack bridge is live.
const ChannelBridged ChannelCapability = "bridged"

// Channel is what a caller sees of a channel it may list (a stored ACL entry,
// or the administrator flag): name, kind and capabilities, no creator and no
// counts. Capabilities is never nil on the Bus.
type Channel struct {
	Name         string
	Kind         Kind
	Capabilities []ChannelCapability
}

// Has reports whether the channel carries the capability.
func (c Channel) Has(capability ChannelCapability) bool {
	for _, have := range c.Capabilities {
		if have == capability {
			return true
		}
	}
	return false
}

// ChannelInfo is the creator's view on CreateChannel and the administrator's
// on Status; ListChannels never returns it.
type ChannelInfo struct {
	Channel
	Creator   string
	Scope     string // "" when unscoped
	CreatedAt int64
}

// IdentityKind is what an identity is: an agent or a human. Every other
// value is refused.
type IdentityKind string

const (
	IdentityAgent IdentityKind = "agent"
	IdentityHuman IdentityKind = "human"
)

// Message is a delivered, read or searched message. Sender is the identity;
// Session the sending session id ("" when none); Display the sending
// session's display name, or Sender when its row is gone.
type Message struct {
	Seq     int64  `json:"seq"`
	Channel string `json:"channel"`
	Sender  string `json:"sender"`
	// SenderKind marks the sender as an agent or a human; always present.
	SenderKind IdentityKind      `json:"sender_kind"`
	Session    string            `json:"session,omitempty"`
	Display    string            `json:"display,omitempty"`
	CreatedAt  int64             `json:"created_at"`
	Type       string            `json:"type,omitempty"`
	Subject    string            `json:"subject,omitempty"`
	Content    string            `json:"content"`
	ReplyTo    *int64            `json:"reply_to,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	MemoryID   *int64            `json:"memory_id,omitempty"`
	Revision   *int64            `json:"revision,omitempty"`
	// Tags is the message's lowercase tag set, sorted.
	Tags []string `json:"tags,omitempty"`
	// MatchedTags is set on a delivery through a tag subscription: the tags
	// of the sets the message satisfied.
	MatchedTags []string `json:"matched_tags,omitempty"`
}

// Gap reports sequence numbers a receiver will never see because retention
// removed them (Decision 16).
type Gap struct {
	Channel string
	From    int64
	To      int64
}

// Notice is a server notice shown to administrators.
type Notice struct {
	Kind    string
	Message string
	SetAt   int64
}
