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

// Channel is what every identity may see of a channel: name and kind only,
// no creator and no counts (Decision 94).
type Channel struct {
	Name string
	Kind Kind
}

// ChannelInfo is the creator's view on CreateChannel and the administrator's
// on Status; ListChannels never returns it (Decision 94).
type ChannelInfo struct {
	Channel
	Creator   string
	CreatedAt int64
}

// Message is a delivered, read or searched message. Sender is the identity;
// Session the sending session id ("" when none); Display the sending
// session's display name, or Sender when its row is gone.
type Message struct {
	Seq       int64             `json:"seq"`
	Channel   string            `json:"channel"`
	Sender    string            `json:"sender"`
	Session   string            `json:"session,omitempty"`
	Display   string            `json:"display,omitempty"`
	CreatedAt int64             `json:"created_at"`
	Type      string            `json:"type,omitempty"`
	Subject   string            `json:"subject,omitempty"`
	Content   string            `json:"content"`
	ReplyTo   *int64            `json:"reply_to,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	MemoryID  *int64            `json:"memory_id,omitempty"`
	Revision  *int64            `json:"revision,omitempty"`
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
