package cli

import (
	"fmt"
	"os"
	"testing"
)

// TestMain runs the tests from an empty directory: the commands find the
// nearest .local/agentbus2.json from the working directory, so a checkout
// where `agentbus2 init` has run would otherwise select its profile.
func TestMain(m *testing.M) { os.Exit(runInEmptyDir(m)) }

func runInEmptyDir(m *testing.M) int {
	dir, err := os.MkdirTemp("", "agentbus2-cli-test-")
	if err == nil {
		err = os.Chdir(dir)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "TestMain:", err)
		return 1
	}
	defer func() { _ = os.RemoveAll(dir) }()
	return m.Run()
}
