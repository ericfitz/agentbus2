package cli

import "testing"
import "strings"

func TestBridgesCommandsUnconfigured(t *testing.T) {
	e := adminEnv(t)
	run := runner(e, RunBridges)
	for _, args := range [][]string{
		{"list"}, {"get", "general"}, {"create", "general"}, {"create", "-slack-name", "x", "general"}, {"remove", "general"},
	} {
		code, _, er := run(args...)
		if code != 1 || !strings.Contains(er, "agentbus2: forbidden: the Slack bridge is not configured") {
			t.Fatalf("%v: %d %q", args, code, er)
		}
	}
	for _, args := range [][]string{{"create"}, {"get"}, {"remove"}, {"frob"}, {}, {"list", "extra"}} {
		if code, _, _ := run(args...); code != 2 {
			t.Fatalf("%v: exit %d, want 2", args, code)
		}
	}
}
