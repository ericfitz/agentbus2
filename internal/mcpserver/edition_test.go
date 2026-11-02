package mcpserver_test

import (
	"testing"

	"github.com/ericfitz/agentbus2/internal/mcpserver"
)

// The commands an edition's messages name are the ones that exist in its binary.
func TestEditionCommands(t *testing.T) {
	cases := []struct {
		e                 mcpserver.Edition
		serve, registerOK string
	}{
		{mcpserver.EditionFull, "agentbus2 mcp", "agentbus2 register"},
		{mcpserver.EditionMCPOnly, "agentbus2-mcponly serve", "agentbus2-mcponly register"},
	}
	for _, c := range cases {
		if got := c.e.Serve(); got != c.serve {
			t.Errorf("Serve() = %q, want %q", got, c.serve)
		}
		if got := c.e.Register(); got != c.registerOK {
			t.Errorf("Register() = %q, want %q", got, c.registerOK)
		}
	}
	if got := mcpserver.Edition(0).Serve(); got != "" {
		t.Errorf("unknown edition Serve() = %q, want empty", got)
	}
}
