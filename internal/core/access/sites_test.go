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

// ruleAllowed maps a site ("file func") to the clauses it may mention in a
// condition: Admin, Creator, Owner, Scope, IdentityKind (store.IdentityHuman or
// IdentityAgent), Perm (store.Perm*) and MemoryKind (KindMemory). A clause not
// listed for its site fails, and a listed clause that no longer fires fails
// too, so the list cannot keep a dead exemption. internal/core/access is
// exempt as a whole. Limits of the gate: it reads conditions only (an if
// condition, a switch tag or case, an operand of && || == !=), so
// `return p.Admin` is not seen; tx.ACL( is matched only on a receiver named
// tx; package-level initializers are not walked; the SQL fragments are exact
// substrings of string literals. Closing these limits is issue #19.
var ruleAllowed = map[string][]string{
	// Sites that copy the flag or the scope (spec 3.7 item 4).
	"internal/core/channels/channels.go Create":         {"Scope"}, // scope := p.Scope, a copy
	"internal/core/channels/channels.go namePrefixRule": {"Scope"}, // name validation: scope-prefixed channel names (spec 4.2), like CheckBridgeACL's shape checks
	"internal/core/scopes/service.go Create":            {"Scope"}, // name validation: the prefix <scope>- must be free (spec 4.2)
	"internal/server/admin_auth.go AdminReset":          {"Scope"}, // wire copy of the request's reset scope (spec Appendix A, kind N)
	"internal/server/channels.go CreateChannel":         {"Scope"}, // wire copy of the request's scope field (kind N)
	"internal/server/channels.go channelInfoToGen":      {"Scope"}, // wire copy of the channel's scope field (kind N)
	"internal/server/scopes.go CreateAgentIdentity":     {"Scope"}, // wire copy of the request's and result's scope (kind N)
	// Identity rules in registration and sign-in (spec 3.7 item 4, question 5).
	"internal/core/identities/human.go signInHumanTx":   {"Admin"},        // sign-in sets a human's kind and never a scope (4.5)
	"internal/core/identities/human.go createByEmailTx": {"IdentityKind"}, // picks the conflict text for a human row (kind N)
	"internal/core/identities/register.go registerTx":   {"Admin", "IdentityKind", "Scope"},
	"internal/core/identities/agent.go ensureAgent":     {"Admin", "IdentityKind", "Scope"}, // the scoped refusals (4.1 (a), 4.5)
	"internal/core/identities/admin.go resetCredential": {"IdentityKind"},
	// Converters, state and ACL shape.
	"internal/core/convert.go KindFromBus":          {"MemoryKind"}, // converter
	"internal/core/messaging/send.go SendWith":      {"MemoryKind"}, // embed-after-commit for a memory: state
	"internal/core/scopes/acl.go CheckBridgeACL":    {"Perm"},       // judges an ACL's shape
	"internal/slackbridge/create.go withBridgeRead": {"Perm"},       // ACL shape: adds the bridge's read row
	"internal/slackbridge/create.go reserveTx":      {"Scope"},      // "a bridge requires a scoped channel": validation
	"internal/mcpserver/server.go toKind":           {"MemoryKind"}, // input validation of the kind argument
	// Task fields judged as state or query filters, never a principal.
	"internal/core/tasks/read.go summaries":       {"Owner"}, // in.Owner is a query filter
	"internal/core/tasks/reclaim.go abandonedSet": {"Owner"}, // t.Owner == "" is state
	"internal/core/tasks/update.go applyPatch":    {"Owner"}, // the patch's own fields: an in_progress task needs an owner, a new owner must be registered
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

// hasKindCompare reports whether e names the memory Kind constant.
func hasKindCompare(e ast.Expr) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		if s, ok := n.(*ast.SelectorExpr); ok && s.Sel.Name == "KindMemory" {
			found = true
		}
		return true
	})
	return found
}

func TestAccessRuleSites(t *testing.T) {
	var bad []string
	fired := map[string]map[string]bool{} // site -> clauses that fire

	note := func(rel, fn, clause string) {
		site := rel + " " + fn
		if fired[site] == nil {
			fired[site] = map[string]bool{}
		}
		fired[site][clause] = true
	}

	// Clause 1: conditions that read Admin, Creator, Owner or Scope, compare an
	// identity Kind, or name a store.Perm* constant.
	funcSites(t, ruleScope, func(rel string, _ *ast.File, fn *ast.FuncDecl) {
		for _, c := range conditions(fn) {
			for _, name := range selectorNames(c) {
				switch {
				case ruleSelectors[name]:
					note(rel, fn.Name.Name, name)
				case permConsts[name]:
					note(rel, fn.Name.Name, "Perm")
				case name == "IdentityHuman" || name == "IdentityAgent":
					note(rel, fn.Name.Name, "IdentityKind")
				}
			}
		}
	})

	// Clause 2: the memory Kind, also over the clients.
	funcSites(t, append(append([]string{}, ruleScope...), memoryScope...), func(rel string, _ *ast.File, fn *ast.FuncDecl) {
		for _, c := range conditions(fn) {
			if hasKindCompare(c) {
				note(rel, fn.Name.Name, "MemoryKind")
			}
		}
	})

	for site, clauses := range fired {
		for clause := range clauses {
			if !slices.Contains(ruleAllowed[site], clause) {
				bad = append(bad, site+": condition mentions "+clause)
			}
		}
	}
	// An allowlist entry that fires nowhere is a hole: drop it.
	for site, clauses := range ruleAllowed {
		for _, clause := range clauses {
			if !fired[site][clause] {
				bad = append(bad, site+": allowlist entry for "+clause+" matches no site (remove it)")
			}
		}
	}

	// Clause 3: tx.ACL( has the listed callers only, and each listed caller
	// still calls it.
	aclSeen := map[string]bool{}
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
			if x, ok := sel.X.(*ast.Ident); ok && x.Name == "tx" {
				site := rel + " " + fn.Name.Name
				aclSeen[site] = true
				if !aclCallers[site] {
					bad = append(bad, site+": calls tx.ACL(")
				}
			}
			return true
		})
	})
	for site := range aclCallers {
		if !aclSeen[site] {
			bad = append(bad, site+": tx.ACL( allowlist entry matches no call (remove it)")
		}
	}

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
