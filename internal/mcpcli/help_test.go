package mcpcli

import (
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the goldens in testdata/help")

// helpNames is the overview (topic "") and every topic, keyed by golden name.
func helpNames() map[string]string {
	m := map[string]string{"overview": ""}
	for _, t := range Topics() {
		m[t] = t
	}
	return m
}

func TestTopics(t *testing.T) {
	want := []string{"connect", "register", "configure", "verify", "protocol", "troubleshoot"}
	if got := Topics(); !slices.Equal(got, want) {
		t.Fatalf("Topics = %v, want %v", got, want)
	}
	for _, topic := range []string{"bogus", "overview", " connect", "Connect"} {
		if text, ok := Help(topic); ok || text != "" {
			t.Errorf("Help(%q) = %q, %v; want no text", topic, text, ok)
		}
	}
}

func TestHelpGolden(t *testing.T) {
	for name, topic := range helpNames() {
		t.Run(name, func(t *testing.T) {
			got, ok := Help(topic)
			if !ok {
				t.Fatalf("no text for %q", topic)
			}
			path := filepath.Join("testdata", "help", name+".txt")
			if *update {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if got != string(want) {
				t.Errorf("%s differs from the golden; run go test ./internal/mcpcli -run TestHelpGolden -update and read the diff\n--- got\n%s", path, got)
			}
		})
	}
}

// TestHelpWords: the MCP-only binary never tells the reader to use a command or
// path that exists only in the full agentbus2 binary.
func TestHelpWords(t *testing.T) {
	banned := []string{"agentbus2 wait", "agentbus2 watch", "run_in_background", "agentbus2 init", "agentbus2 mcp", "agentbus2 park", "stop-hook", "dm/", "-filter", "@<", "memory/", "tasks/", "general/"}
	for name, topic := range helpNames() {
		text, _ := Help(topic)
		for _, w := range banned {
			if strings.Contains(text, w) {
				t.Errorf("%s contains %q", name, w)
			}
		}
	}
}

var (
	dollarRE = regexp.MustCompile(`^  \$ agentbus2-mcponly (\S+)`)
	wantRE   = regexp.MustCompile(`^    > `)
	exitRE   = regexp.MustCompile(`^    \(exit [1-9][0-9]*\)$`)
)

// TestHelpGrammar pins open note: mcp-only-client 12.
func TestHelpGrammar(t *testing.T) {
	cmds := commandNames()
	for name, topic := range helpNames() {
		text, _ := Help(topic)
		if name == "overview" && firstLine(text) != usageLine {
			t.Errorf("overview first line = %q, want usageLine", firstLine(text))
		}
		if !strings.HasSuffix(text, "\n") || strings.HasSuffix(text, "\n\n") {
			t.Errorf("%s must end with exactly one newline", name)
		}
		lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
		dollars := 0
		placeholder := regexp.MustCompile(`<[^>]*>`)
		for i, l := range lines {
			if wantRE.MatchString(l) && strings.TrimSpace(placeholder.ReplaceAllString(l[6:], "")) == "" {
				t.Errorf("%s: %q has no literal character, so it matches any line", name, l)
			}
			if dollarRE.MatchString(l) {
				next := ""
				if i+1 < len(lines) {
					next = lines[i+1]
				}
				if !wantRE.MatchString(next) && !exitRE.MatchString(next) {
					t.Errorf("%s: %q states no output and no exit code", name, l)
				}
			}
		}
		for i, l := range lines {
			trimmed := strings.TrimSpace(l)
			prev := ""
			if i > 0 {
				prev = lines[i-1]
			}
			prevInExample := dollarRE.MatchString(prev) || wantRE.MatchString(prev)
			switch {
			case dollarRE.MatchString(l):
				dollars++
				if c := dollarRE.FindStringSubmatch(l)[1]; !slices.Contains(cmds, c) {
					t.Errorf("%s: %q names no command", name, l)
				}
			case wantRE.MatchString(l):
				if !prevInExample {
					t.Errorf("%s: output line outside an example: %q", name, l)
				}
			case exitRE.MatchString(l):
				if !prevInExample {
					t.Errorf("%s: exit line not after an example: %q", name, l)
				}
			case strings.HasPrefix(trimmed, "$"), strings.HasPrefix(trimmed, ">"), strings.HasPrefix(trimmed, "(exit"):
				t.Errorf("%s: malformed example line %q", name, l)
			}
			if strings.TrimRight(l, " ") != l {
				t.Errorf("%s: trailing space in %q", name, l)
			}
		}
		if name == "protocol" && dollars != 0 {
			t.Errorf("protocol has %d $ lines, want none", dollars)
		}
	}
}

// TestHelpNamesEveryFlagAndVariable: every flag a command defines and every
// variable the code reads appears in the overview.
func TestHelpNamesEveryFlagAndVariable(t *testing.T) {
	overview, _ := Help("")
	flagRE := regexp.MustCompile(`(?m)^  -(\S+)`)
	for c := range commands {
		_, _, errs := run([]string{c, "-h"}, nil)
		ms := flagRE.FindAllStringSubmatch(errs, -1)
		if len(ms) == 0 {
			t.Errorf("%s -h lists no flags: %q", c, errs)
		}
		for _, m := range ms {
			if !strings.Contains(overview, "-"+m[1]) {
				t.Errorf("the overview does not name %s's flag -%s", c, m[1])
			}
		}
	}
	for _, v := range []string{"AGENTBUS2_SERVER", "AGENTBUS2_IDENTITY", "AGENTBUS2_CREDENTIAL", "AGENTBUS2_ENROLLMENT_TOKEN", "AGENTBUS2_PROFILE"} {
		if !strings.Contains(overview, v) {
			t.Errorf("the overview does not name %s", v)
		}
	}
}

// TestCommandHelpPointsAtTopic: -h prints the flags, then the topic to read.
func TestCommandHelpPointsAtTopic(t *testing.T) {
	want := map[string]string{"serve": "configure", "register": "register", "check": "verify", "config": "configure"}
	if len(want) != len(commands) {
		t.Fatalf("the table names %d commands, commands has %d", len(want), len(commands))
	}
	for c, topic := range want {
		code, out, errs := run([]string{c, "-h"}, nil)
		last := lastLine(errs)
		if code != 0 || out != "" || last != "see: agentbus2-mcponly help "+topic {
			t.Errorf("%s -h: code %d, stdout %q, last stderr line %q", c, code, out, last)
		}
		if !strings.Contains(errs, "\n  -server ") {
			t.Errorf("%s -h does not list the flags: %q", c, errs)
		}
		if _, ok := Help(topic); !ok {
			t.Errorf("%s names the unknown topic %q", c, topic)
		}
	}
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	return lines[len(lines)-1]
}
