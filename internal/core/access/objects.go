package access

import "github.com/ericfitz/agentbus2/internal/store"

// Action is what a principal asks to do to an object.
type Action string

const (
	Read          Action = "read"           // channel (Decision 80)
	Write         Action = "write"          // channel
	Admin         Action = "admin"          // channel
	List          Action = "list"           // channel: a stored ACL row for the principal's names, or the administrator flag (Eric 2026-10-26)
	Unsubscribe   Action = "unsubscribe"    // channel: the scope gate alone (Decision 95)
	FilterReceive Action = "filter_receive" // channel: the scope gate alone (the receive channel filter)
	Deliver       Action = "deliver"        // channel: Read, minus memory channels for an agent (decision 20)
	Post          Action = "post"           // channel: Write held by a human (the Slack bridge, ADR 0009 decision 11)
	Administer    Action = "administer"     // server
	CreateChannel Action = "create_channel" // server (Decision 93)
	Use           Action = "use"            // session
	See           Action = "see"            // identity
	ReplaceACL    Action = "replace_acl"    // ACLChange (Eric 2026-10-25, spec 4.6)
	TakeName      Action = "take_name"      // ChannelName and ScopeName: the scope-prefix naming rules (Eric 2026-10-26, issue #18)
)

// Object is what is asked about. The facts of each kind are loaded inside the
// check, never passed by the caller.
type Object interface{ accessObject() }

// Channel is a channel by id. Tasks and memories are checked as their channel.
type Channel struct{ ID int64 }

// Server is the server-wide administrator operations and channel creation.
type Server struct{}

// Session is a session by id.
type Session struct{ ID string }

// Identity is an identity by name (the Identities listing).
type Identity struct{ Name string }

// ChannelName is a name proposed for a channel that will carry Scope (a create
// or a rename).
type ChannelName struct{ Name, Scope string }

// ScopeName is a name proposed for a new scope.
type ScopeName struct{ Name string }

// ACLChange is the replacement of a channel's whole ACL by Entries.
type ACLChange struct {
	ID      int64
	Entries []store.ACLEntry
}

func (ACLChange) accessObject()   {}
func (Channel) accessObject()     {}
func (Server) accessObject()      {}
func (Session) accessObject()     {}
func (Identity) accessObject()    {}
func (ChannelName) accessObject() {}
func (ScopeName) accessObject()   {}
