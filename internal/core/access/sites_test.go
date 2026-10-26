package access_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

// The gate of spec 2026-10-25-accesscheck-design 3.7 item 4: no code outside
// internal/core/access decides who may do what. The allowlist below is a
// literal, so adding a site is a visible edit with a review question
// attached: a failure names a site the plan missed, and the fix is the site,
// not the list (unless the spec's Appendix A files it as kind N).

// ruleScope are the server-side trees the flag, creator, owner and scope
// clause walks (clients are excluded by design).
var ruleScope = []string{
	"internal/core",
	"internal/server",
	"internal/slackbridge",
	"internal/bus/local",
}

// memoryScope are the extra trees walked for the KindMemory clause only: after
// Task 4 no client decides what an agent is delivered.
var memoryScope = []string{"internal/mcpserver", "internal/cli"}

// ruleAllowed lists the sites that may mention Admin, Creator, Owner, Scope,
// an identity Kind (store.IdentityHuman, IdentityAgent), a memory Kind or a
// store.Perm* constant in a condition. An entry is "file func", "file" (every
// function in the file) or "dir/" (every file under the directory). The
// internal/core/access package is exempt as a whole.
var ruleAllowed = map[string]string{
	// Sites that copy the flag or the scope.
	"internal/core/sessions/identities.go Identities": "copies the principal's scope into a listing filter (spec 3.7)",
	"internal/core/convert.go":                        "converters between the Store and bus shapes (copies)",
	"internal/core/channels/channels.go Create":       "scope := p.Scope, a copy",
	// Sites that set the flag or the scope, and the identity-rule Store facts.
	"internal/core/identities/human.go signInHumanTx":   "sign-in sets a human's kind and never a scope (4.5)",
	"internal/core/identities/register.go registerTx":   "registration: a Kind test (question 5)",
	"internal/core/identities/agent.go ensureAgent":     "the scoped refusals of an agent row (4.1 (a), question 5)",
	"internal/core/identities/admin.go resetCredential": "reset: a Kind test (question 5)",
	"internal/core/identities/admin.go MakeAdmin":       "the scoped refusal and the scope clear (4.1 (a), 4.3)",
	// Sites added after the spec was written (Task 10 report): validation and
	// identity-rule Store facts, and the wire layer's copies of a scope field.
	"internal/core/append.go Append":                    "validation: an append on an unknown channel kind is internal",
	"internal/core/identities/human.go createByEmailTx": "identity rule: a Kind test in sign-in (question 5), not an access decision",
	"internal/server/admin_auth.go AdminReset":          "wire copy of the request's reset scope",
	"internal/server/channels.go CreateChannel":         "wire copy of the request's scope field",
	"internal/server/channels.go channelInfoToGen":      "wire copy of the channel's scope field",
	"internal/server/scopes.go CreateAgentIdentity":     "wire copy of the request's and result's scope field",
	// State rules that read a scope's own row or the shape of an ACL.
	"internal/core/scopes/service.go RequireOpen": "a scope's own state",
	"internal/core/scopes/retire.go":              "retirement reads a scope's own state",
	"internal/core/scopes/acl.go CheckBridgeACL":  "judges an ACL's shape (the bridged-channel rule)",
	"internal/slackbridge/create.go":              "a bridge requires a scoped channel: validation",
	// Operation-shape validations: they judge the operation, not a principal.
	"internal/core/messaging/send.go":      "send on a task list is refused: the shape of the operation",
	"internal/core/memories/memories.go":   "edit_memory on a task list is refused: the shape of the operation",
	"internal/core/tasks/":                 "the task-list kind validations",
	"internal/core/tasks/reclaim.go":       "t.Owner == \"\" is the task's state",
	"internal/core/tasks/read.go taskList": "in.Owner is a query filter",
}

// memoryAllowed lists the sites outside access that may name a memory kind.
var memoryAllowed = map[string]string{
	"internal/mcpserver/server.go toKind": "input validation of the kind argument",
}

// aclCallers are the only sites that call tx.ACL( (the get-ACL display and the
// bridge's two ACL mutations; access reads the rows to decide).
var aclCallers = map[string]bool{
	"internal/core/channels/channels.go ACL":   true,
	"internal/slackbridge/archive.go Remove":   true,
	"internal/slackbridge/create.go reserveTx": true, // step 1: reads the ACL to check its shape (CheckBridgeACL)
	"internal/slackbridge/create.go goLive":    true, // step 4: pins the slack-bridge read row
}

// sqlFragments must appear in no non-test string literal of
// internal/store/sqlstore: the Store serves facts, never rules (spec 3.7).
var sqlFragments = []string{
	"is_admin = TRUE", "can_read = TRUE", "can_write = TRUE", "can_admin = TRUE", "creator = ?",
}

var ruleSelectors = map[string]bool{"Admin": true, "Creator": true, "Owner": true, "Scope": true}
var kindConsts = map[string]bool{"IdentityHuman": true, "IdentityAgent": true, "KindMemory": true}
var permConsts = map[string]bool{"PermRead": true, "PermWrite": true, "PermAdmin": true}

const moduleRoot = "../../.."

// funcSites parses every non-test .go file under dirs and calls visit with the
// module-relative path and each top-level function.
func funcSites(t *testing.T, dirs []string, visit func(rel string, f *ast.File, fn *ast.FuncDecl)) {
	t.Helper()
	fset := token.NewFileSet()
	for _, dir := range dirs {
		err := filepath.WalkDir(filepath.Join(moduleRoot, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, _ := filepath.Rel(moduleRoot, path)
			rel = filepath.ToSlash(rel)
			if strings.HasPrefix(rel, "internal/core/access/") {
				return nil
			}
			f, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			for _, decl := range f.Decls {
				if fn, ok := decl.(*ast.FuncDecl); ok {
					visit(rel, f, fn)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func allowed(list map[string]string, rel, fn string) bool {
	if _, ok := list[rel+" "+fn]; ok {
		return true
	}
	if _, ok := list[rel]; ok {
		return true
	}
	for k := range list {
		if strings.HasSuffix(k, "/") && strings.HasPrefix(rel, k) {
			return true
		}
	}
	return false
}

// conditions returns every expression of fn that is a decision: an if
// condition, a switch tag, a case expression, or an operand of && || == !=.
func conditions(fn *ast.FuncDecl) []ast.Expr {
	var out []ast.Expr
	ast.Inspect(fn, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.IfStmt:
			out = append(out, n.Cond)
		case *ast.SwitchStmt:
			if n.Tag != nil {
				out = append(out, n.Tag)
			}
		case *ast.CaseClause:
			out = append(out, n.List...)
		case *ast.BinaryExpr:
			switch n.Op {
			case token.LAND, token.LOR, token.EQL, token.NEQ:
				out = append(out, n.X, n.Y)
			}
		}
		return true
	})
	return out
}

// selectorNames returns the selector names inside e.
func selectorNames(e ast.Expr) []string {
	var out []string
	ast.Inspect(e, func(n ast.Node) bool {
		if s, ok := n.(*ast.SelectorExpr); ok {
			out = append(out, s.Sel.Name)
		}
		return true
	})
	return out
}

// hasKindCompare reports whether e names a Kind constant.
func hasKindCompare(e ast.Expr) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		if s, ok := n.(*ast.SelectorExpr); ok && kindConsts[s.Sel.Name] {
			found = true
		}
		return true
	})
	return found
}

func TestAccessRuleSites(t *testing.T) {
	var bad []string

	// Clause 1: no condition outside access reads Admin, Creator, Owner, Scope,
	// compares an identity Kind, or names a store.Perm* constant.
	funcSites(t, ruleScope, func(rel string, _ *ast.File, fn *ast.FuncDecl) {
		if allowed(ruleAllowed, rel, fn.Name.Name) {
			return
		}
		for _, c := range conditions(fn) {
			for _, name := range selectorNames(c) {
				if ruleSelectors[name] || permConsts[name] || name == "IdentityHuman" || name == "IdentityAgent" {
					bad = append(bad, rel+" "+fn.Name.Name+": condition mentions "+name)
				}
			}
		}
	})

	// Clause 2: the memory Kind, also over the clients.
	funcSites(t, append(append([]string{}, ruleScope...), memoryScope...), func(rel string, _ *ast.File, fn *ast.FuncDecl) {
		if allowed(ruleAllowed, rel, fn.Name.Name) || allowed(memoryAllowed, rel, fn.Name.Name) {
			return
		}
		for _, c := range conditions(fn) {
			if hasKindCompare(c) {
				bad = append(bad, rel+" "+fn.Name.Name+": condition names a Kind constant")
			}
		}
	})

	// Clause 3: tx.ACL( has the listed callers only.
	funcSites(t, []string{"internal"}, func(rel string, _ *ast.File, fn *ast.FuncDecl) {
		if strings.HasPrefix(rel, "internal/store/") || strings.HasPrefix(rel, "internal/storetest/") {
			return
		}
		ast.Inspect(fn, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "ACL" {
				return true
			}
			if x, ok := sel.X.(*ast.Ident); ok && x.Name == "tx" && !aclCallers[rel+" "+fn.Name.Name] {
				bad = append(bad, rel+" "+fn.Name.Name+": calls tx.ACL(")
			}
			return true
		})
	})

	// Clause 4: no rule in the Store's SQL.
	fset := token.NewFileSet()
	err := filepath.WalkDir(filepath.Join(moduleRoot, "internal/store/sqlstore"), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				for _, frag := range sqlFragments {
					if strings.Contains(lit.Value, frag) {
						bad = append(bad, filepath.ToSlash(path)+": SQL literal contains "+frag)
					}
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(bad) > 0 {
		sort.Strings(bad)
		bad = slices.Compact(bad)
		t.Fatalf("access decisions outside internal/core/access (fix the site; do not widen the allowlist):\n  %s", strings.Join(bad, "\n  "))
	}
}

// TestStoretestHasNoNonTestImporter: internal/storetest is a test helper; no
// production file imports it.
func TestStoretestHasNoNonTestImporter(t *testing.T) {
	fset := token.NewFileSet()
	var bad []string
	err := filepath.WalkDir(moduleRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".claude", ".superpowers", "node_modules":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(moduleRoot, path)
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "internal/storetest/") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range f.Imports {
			if strings.HasSuffix(strings.Trim(imp.Path.Value, `"`), "/internal/storetest") {
				bad = append(bad, rel)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) > 0 {
		t.Fatalf("non-test importers of internal/storetest: %v", bad)
	}
}
