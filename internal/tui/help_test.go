package tui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// normalKeys are the string cases of updateNormal's switch, read from
// model.go itself so a new key without a help row fails here.
func normalKeys(t *testing.T) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "model.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, d := range file.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "updateNormal" {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			sw, ok := n.(*ast.SwitchStmt)
			if !ok {
				return true
			}
			if id, ok := sw.Tag.(*ast.Ident); !ok || id.Name != "k" {
				return true
			}
			for _, s := range sw.Body.List {
				for _, e := range s.(*ast.CaseClause).List {
					if lit, ok := e.(*ast.BasicLit); ok && lit.Kind == token.STRING {
						k, _ := strconv.Unquote(lit.Value)
						keys = append(keys, k)
					}
				}
			}
			return false
		})
	}
	if len(keys) < 20 {
		t.Fatalf("read only %d keys from updateNormal: %v", len(keys), keys)
	}
	return keys
}

// helpToken is how the help text spells a key name.
var helpToken = map[string]string{
	"up": "↑", "down": "↓", "left": "←", "right": "→", "pgdown": "pgdn", " ": "space",
}

func TestHelpListsEveryKey(t *testing.T) {
	f := newFixture(t)
	body := stripANSI(strings.Join(f.m.helpLines(), "\n"))
	for _, k := range normalKeys(t) {
		tok := k
		if v, ok := helpToken[k]; ok {
			tok = v
		}
		if !strings.Contains(body, tok) {
			t.Errorf("help lacks the key %q (looked for %q)", k, tok)
		}
	}
	// The heading rows, and the compose and task-list groups.
	for _, want := range []string{"enter (heading)", "home", "rail: select the channels heading", "new channel: <name> [memory|tasks]", "alt+enter", "(rail)", "(task lists)", "(tags)"} {
		if !strings.Contains(body, want) {
			t.Errorf("help lacks %q", want)
		}
	}
	// ADR 0006: no direct messages, no inbox, no sessions pane. A plain
	// substring "dm" would match "admin".
	lower := strings.ToLower(body)
	for _, bad := range []string{"direct", "inbox", "sessions pane", "the sessions pane", "general/, memory/"} {
		if strings.Contains(lower, bad) {
			t.Errorf("help mentions %q", bad)
		}
	}
	if regexp.MustCompile(`\bdm\b`).MatchString(lower) {
		t.Error("help mentions dm")
	}
}

func TestHelpOverlayListsKeysAndHealthIsSeparate(t *testing.T) {
	f := newFixture(t)
	f.key("esc")
	f.m.height = 52
	f.key("?")
	if f.m.mode != modeHelp {
		t.Fatalf("? opens help, got mode %v", f.m.mode)
	}
	v := f.m.View()
	for _, want := range []string{"help", "tab / shift+tab", "next / previous pane", "home", "<name> [memory|tasks]", "alt+enter", "quit"} {
		if !strings.Contains(v, want) {
			t.Errorf("help lacks %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "storage") {
		t.Fatalf("help must not be the health overlay:\n%s", v)
	}
	f.key("down")
	if f.m.helpScroll != 1 {
		t.Fatalf("down scrolls help, got %d", f.m.helpScroll)
	}
	f.key("esc")
	if f.m.mode != modeNormal {
		t.Fatal("esc closes help")
	}
	f.key("h")
	if f.m.mode != modeHealth {
		t.Fatal("h opens health")
	}
	f.key("?")
	if f.m.mode != modeHelp {
		t.Fatal("? inside health switches to help")
	}
}

// The (task lists) row is its own trailing group, preceded by a blank row.
func TestTaskListsHelpRowIsItsOwnGroup(t *testing.T) {
	f := newFixture(t)
	lines := f.m.helpLines()
	for i, l := range lines {
		if !strings.Contains(l, "task lists") {
			continue
		}
		if i == 0 || strings.TrimSpace(lines[i-1]) != "" {
			t.Fatalf("(task lists) row at %d must be preceded by a blank separator, got %q", i, lines[i-1])
		}
		return
	}
	t.Fatal("(task lists) row not found in helpLines")
}
