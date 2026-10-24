package remote

import (
	"context"
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

// The Identities path goes through fromGenIdentityKind too: an identity kind
// the server sends that is not agent or human is an internal error naming
// "kind", never a silently accepted row.
func TestIdentitiesRejectsUnknownKind(t *testing.T) {
	bad := &canned{status: 200, ctype: "application/json", body: `[{"name":"pat","kind":"robot","admin":false,"sessions":[]}]`}
	_, err := newBus(t, bad).Identities(context.Background())
	be, ok := err.(*bus.Error)
	if !ok || be.Code != bus.CodeInternal || !strings.Contains(be.Message, "kind") || !strings.Contains(be.Message, "robot") {
		t.Fatalf("Identities(robot) = %v, want an internal *bus.Error naming kind", err)
	}

	good := &canned{status: 200, ctype: "application/json", body: `[{"name":"pat","kind":"agent","admin":false,"sessions":[]},{"name":"eric@x.example","kind":"human","admin":true,"sessions":[]}]`}
	got, err := newBus(t, good).Identities(context.Background())
	if err != nil || len(got) != 2 || got[0].Kind != bus.IdentityAgent || got[1].Kind != bus.IdentityHuman {
		t.Fatalf("Identities(agent, human) = %+v, %v", got, err)
	}
}
