package remote

import (
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/gen"
)

func TestFromGenMessageRejectsUnknownSenderKind(t *testing.T) {
	_, err := fromGenMessage(gen.Message{Channel: "dev", Sender: "pat", SenderKind: "robot"})
	be, ok := err.(*bus.Error)
	if !ok || be.Code != bus.CodeInternal || !strings.Contains(be.Message, "sender_kind") {
		t.Fatalf("fromGenMessage(robot) = %v, want an internal *bus.Error naming sender_kind", err)
	}
	m, err := fromGenMessage(gen.Message{Channel: "dev", Sender: "eric", SenderKind: "human"})
	if err != nil || m.SenderKind != bus.IdentityHuman {
		t.Fatalf("fromGenMessage(human) = %+v, %v", m, err)
	}
}
