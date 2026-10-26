package scopes

import (
	"slices"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/store"
)

// CheckBridgeACL is the ACL rule of a bridged channel (Decision 12, spec
// 6.1b), pure so the bridge creation and the ACL change share it: read or
// write may be granted only to named identities, never to everyone,
// admins or humans (Eric 2026-10-25), and the slack-bridge read entry stays while the bridge is live.
// (Administrators act through the implicit admins entry; an admins entry
// holding only the admin permission is fine.)
func CheckBridgeACL(entries []store.ACLEntry) error {
	bridgeRead := false
	for _, e := range entries {
		rw := slices.Contains(e.Permissions, store.PermRead) || slices.Contains(e.Permissions, store.PermWrite)
		if rw && (e.Identity == store.Everyone || e.Identity == store.Admins || e.Identity == store.Humans) {
			return core.E(bus.CodeForbidden, "a bridged channel may grant read or write only to named identities (Decision 12)")
		}
		if e.Identity == store.SlackBridge && slices.Contains(e.Permissions, store.PermRead) {
			bridgeRead = true
		}
	}
	if !bridgeRead {
		return core.E(bus.CodeForbidden, "the slack-bridge read entry is pinned while the bridge is live")
	}
	return nil
}
