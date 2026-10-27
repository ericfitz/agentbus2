package scopes

import (
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/store"
)

func TestCheckBridgeACL(t *testing.T) {
	rd := []store.Permission{store.PermRead}
	rw := []store.Permission{store.PermRead, store.PermWrite}
	for _, tc := range []struct {
		name    string
		entries []store.ACLEntry
		want    string // "" is nil
	}{
		{"everyone read", []store.ACLEntry{{Identity: store.Everyone, Permissions: rd}, {Identity: store.SlackBridge, Permissions: rd}}, "may not name everyone"},
		{"everyone admin only", []store.ACLEntry{{Identity: store.Everyone, Permissions: []store.Permission{store.PermAdmin}}, {Identity: store.SlackBridge, Permissions: rd}}, "may not name everyone"},
		{"admins write", []store.ACLEntry{{Identity: store.Admins, Permissions: []store.Permission{store.PermWrite}}, {Identity: store.SlackBridge, Permissions: rd}}, "named identities"},
		{"humans read", []store.ACLEntry{{Identity: store.Humans, Permissions: rd}, {Identity: store.SlackBridge, Permissions: rd}}, "may not name humans"},
		{"humans write", []store.ACLEntry{{Identity: store.Humans, Permissions: []store.Permission{store.PermWrite}}, {Identity: store.SlackBridge, Permissions: rd}}, "may not name humans"},
		{"humans admin only", []store.ACLEntry{{Identity: store.Humans, Permissions: []store.Permission{store.PermAdmin}}, {Identity: store.SlackBridge, Permissions: rd}}, "may not name humans"},
		{"admins admin only", []store.ACLEntry{{Identity: store.Admins, Permissions: []store.Permission{store.PermAdmin}}, {Identity: store.SlackBridge, Permissions: rd}}, ""},
		{"named plus bridge", []store.ACLEntry{{Identity: "pat", Permissions: rw}, {Identity: store.SlackBridge, Permissions: rw}}, ""},
		{"no slack-bridge entry", []store.ACLEntry{{Identity: "pat", Permissions: rw}}, "pinned"},
		{"empty", nil, "pinned"},
		{"slack-bridge write only", []store.ACLEntry{{Identity: store.SlackBridge, Permissions: []store.Permission{store.PermWrite}}}, "pinned"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckBridgeACL(tc.entries)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if bus.CodeOf(err) != bus.CodeForbidden || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want forbidden containing %q", err, tc.want)
			}
		})
	}
}
