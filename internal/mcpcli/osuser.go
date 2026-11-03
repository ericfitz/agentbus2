package mcpcli

import (
	"os/user"

	"github.com/ericfitz/agentmesh/internal/osuser"
)

// currentUser is user.Current; tests replace it.
var currentUser = user.Current

// osUserName is the OS login name, which belongs to the human (Decision 16).
func osUserName() string { return osuser.NameFrom(currentUser) }
