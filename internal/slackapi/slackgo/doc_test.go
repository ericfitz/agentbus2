package slackgo

import (
	"os"
	"strings"
	"testing"
)

// TestInviteFatalDocumented keeps the slackapi.Client.Invite doc in step with
// inviteFatal: every code the adapter treats as an error is named there.
func TestInviteFatalDocumented(t *testing.T) {
	src, err := os.ReadFile("../slackapi.go")
	if err != nil {
		t.Fatal(err)
	}
	for code := range inviteFatal {
		if !strings.Contains(string(src), code) {
			t.Errorf("inviteFatal code %q is not named in the slackapi.Client.Invite doc", code)
		}
	}
}
