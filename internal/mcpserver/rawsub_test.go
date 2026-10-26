package mcpserver_test

import (
	"testing"

	"github.com/ericfitz/agentbus2/internal/profile"
	"github.com/ericfitz/agentbus2/internal/storetest"
)

// subscribeRaw writes a subscription into the default profile's SQLite file,
// past the Bus (see storetest.SubscribeRaw).
func (e *testEnv) subscribeRaw(t *testing.T, identity, channel string) {
	t.Helper()
	storetest.SubscribeRaw(t, profile.SQLitePath(e.Config.Profiles["default"], e.DataDir), identity, channel)
}
