package access

// Action is what a principal asks to do to an object.
type Action string

const (
	Read          Action = "read"           // channel (Decision 80)
	Write         Action = "write"          // channel
	Admin         Action = "admin"          // channel
	List          Action = "list"           // channel (interim clause until the channel-visibility rebuild)
	Deliver       Action = "deliver"        // channel: Read, minus memory channels for an agent (decision 20)
	Post          Action = "post"           // channel: Write held by a human (the Slack bridge, ADR 0009 decision 11)
	Administer    Action = "administer"     // server
	CreateChannel Action = "create_channel" // server (Decision 93)
	Use           Action = "use"            // session
	See           Action = "see"            // identity
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

func (Channel) accessObject()  {}
func (Server) accessObject()   {}
func (Session) accessObject()  {}
func (Identity) accessObject() {}
