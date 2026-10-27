package version

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestProtocolMatchesOpenAPI pins Protocol to info.version of the document.
// The package stays a leaf, so only the standard library is used.
func TestProtocolMatchesOpenAPI(t *testing.T) {
	const file = "../../api/openapi.yaml"
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	inInfo, found := false, ""
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimRight(line, "\r ")
		if line == "info:" {
			inInfo = true
			continue
		}
		if inInfo && strings.HasPrefix(line, "  version:") {
			found = strings.TrimSpace(strings.TrimPrefix(line, "  version:"))
			break
		}
	}
	if found == "" {
		t.Fatalf("%s has no info: version: line", file)
	}
	if found != Protocol {
		t.Errorf("info.version %q in %s, Protocol %q", found, file, Protocol)
	}
	if !regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`).MatchString(Protocol) {
		t.Errorf("Protocol %q is not MAJOR.MINOR.PATCH", Protocol)
	}
}
