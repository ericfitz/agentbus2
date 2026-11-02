package cli

import (
	"os/user"

	"github.com/ericfitz/agentbus2/internal/osuser"
)

// currentUser is user.Current; tests replace it.
var currentUser = user.Current

// osUserName is the OS login name, the TUI's identity when none is set.
func osUserName() string { return osuser.NameFrom(currentUser) }
