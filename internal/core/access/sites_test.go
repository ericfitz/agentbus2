package access_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The gate of spec 2026-10-25-accesscheck-design 3.7 item 4: no code outside
// internal/core/access decides who may do what. The allowlist below is a
// literal, so adding a site is a visible edit with a review question
// attached: a failure names a site the plan missed, and the fix is the site,
// not the list (unless the spec's Appendix A files it as kind N).
//
// What the gate covers (issue #19):
//   - Every selector of Admin, Creator, Owner or Scope on a value (not a
//     package-qualified name such as access.Admin, not a method call such as
//     tx.Scope(ctx, name)), read or written, anywhere in a function or a
//     file-scope var initializer: conditions, returns, assignments,
//     composite-literal values and call arguments alike.
//   - Every selector of store.IdentityHuman, store.IdentityAgent and
//     KindMemory anywhere; KindMemory also over internal/mcpserver and
//     internal/cli.
//   - A store.Perm* constant inside a condition (an if condition, a switch tag
//     or case, an operand of && || == !=); building an ACL is not a decision.
//   - Each count is exact per site and clause: one occurrence more fails and
//     names the site, one fewer fails the same way, so the list holds neither
//     a second rule hidden behind a copy nor a dead exemption.
//   - Every method call named ACL under internal, whatever the receiver and
//     however many arguments, outside internal/core/access and
//     internal/store/sqlstore (Eric 2026-10-27). The match is syntactic, so
//     besides store.Tx.ACL(ctx, id) the callers of Bus.ACL(ctx, channel) and
//     of channels.Service.ACL(ctx, identity, channel) are listed too.
//   - Package-level var and const initializers, function literals inside them
//     included, as the site "file <init:name>".
//   - The sqlstore SQL: no string literal, and no + chain of string literals,
//     filters on is_admin, can_read, can_write, can_admin or creator after a
//     WHERE, ON, HAVING or WHEN (regexps, case-insensitive, across lines),
//     except the listed repair.
//
// What it still does not cover: a rule spelled without these names (a local
// bool copied from an allowlisted site and tested elsewhere, a map keyed by
// the flag, reflection, a method named otherwise that returns the flag); a
// selector whose receiver merely shares an import's name; a Tx.ACL call made
// through a method value or an interface wrapper with another name; SQL built
// from non-literal pieces (fmt.Sprintf, a variable column name) and filters on
// the kind or scope columns, which the Store answers as facts (WHERE kind = ?,
// WHERE scope = ?); and every package outside the trees walked below (clients
// are excluded by design, and the TUI is presentation).

// ruleScope are the server-side trees the flag, creator, owner and scope
// clauses walk (clients are excluded by design).
var ruleScope = []string{
	"internal/core",
	"internal/server",
	"internal/slackbridge",
	"internal/bus/local",
}

// memoryScope are the extra trees walked for the MemoryKind clause only: after
// Task 4 no client decides what an agent is delivered.
var memoryScope = []string{"internal/mcpserver", "internal/cli"}

// ruleAllowed maps a site ("file func", or "file <init:name>" for a
// package-level initializer) to the exact number of occurrences of each clause
// it may hold: Admin, Creator, Owner, Scope (a selector of that name),
// IdentityKind (store.IdentityHuman or IdentityAgent), MemoryKind (KindMemory)
// and Perm (a store.Perm* constant inside a condition). internal/core/access
// is exempt as a whole. Each entry says why its occurrences copy, set or
// validate a value and decide nothing (the spec's kind N).
var ruleAllowed = map[string]map[string]int{
	// Sites that copy the flag or the scope (spec 3.7 item 4).
	"internal/core/channels/channels.go Create":             {"Scope": 4},               // the idempotency payload, scope := p.Scope, and in.Scope picking the Administer check and copied in
	"internal/core/channels/channels.go Rename":             {"Scope": 1},               // copies the channel's scope into the TakeName facts
	"internal/core/convert.go ChannelInfoToBus":             {"Creator": 1, "Scope": 1}, // converter
	"internal/core/sessions/identities.go Identities":       {"Admin": 1},               // copies the flag into bus.IdentityInfo
	"internal/core/identities/agent.go createAgentIdentity": {"Scope": 1},               // copies the request's scope into the result
	"internal/bus/local/admin_identities.go Reset":          {"Scope": 1},               // passes the reset scope through to the core
	"internal/server/admin_auth.go AdminReset":              {"Scope": 2},               // wire copy of the request's reset scope (spec Appendix A, kind N)
	"internal/server/bridges.go bridgeToGen":                {"Scope": 1},               // wire copy of the bridge's scope (kind N)
	"internal/server/channels.go CreateChannel":             {"Scope": 3},               // wire copy of the request's scope field (kind N)
	"internal/server/channels.go channelInfoToGen":          {"Creator": 1, "Scope": 3}, // wire copy of the channel's creator and scope fields (kind N)
	"internal/server/scopes.go CreateAgentIdentity":         {"Scope": 6},               // wire copy of the request's and result's scope (kind N)
	"internal/server/scopes.go GetScope":                    {"Scope": 1},               // the path parameter naming the scope (kind N)
	"internal/server/scopes.go DoneScope":                   {"Scope": 1},               // the path parameter naming the scope (kind N)
	"internal/server/sessions.go ListIdentities":            {"Admin": 1},               // wire copy of the identity's flag (kind N)
	"internal/slackbridge/service.go toBus":                 {"Scope": 1},               // converter
	"internal/slackbridge/outbound.go drainOne":             {"Scope": 1},               // the bridge's scope as the post's display prefix
	"internal/slackbridge/lifecycle.go lifecycleOne":        {"Scope": 1},               // loads the bridge's scope row: state
	"internal/slackbridge/lifecycle.go memberLeft":          {"Scope": 1},               // loads the bridge's scope row: state
	// Identity rules in registration and sign-in (spec 3.7 item 4, question 5).
	"internal/core/identities/human.go signInHumanTx":   {"Admin": 4},                                 // sign-in sets a human's flag from the admins references (4.5)
	"internal/core/identities/human.go createByEmailTx": {"IdentityKind": 1},                          // picks the conflict text for a human row (kind N)
	"internal/core/identities/register.go registerTx":   {"Admin": 2, "IdentityKind": 1, "Scope": 1},  // registration rules (a2), (c2), the human refusal and (a3)
	"internal/core/identities/agent.go ensureAgent":     {"Admin": 1, "IdentityKind": 2, "Scope": 10}, // the scoped refusals (4.1 (a), 4.5), the open-scope check and their texts
	"internal/core/identities/admin.go resetCredential": {"IdentityKind": 1},                          // a human's reset clears its bindings, an agent's its keys: state
	// Converters, state and ACL shape.
	"internal/core/convert.go KindFromBus":            {"MemoryKind": 2},               // converter
	"internal/core/messaging/send.go SendWith":        {"MemoryKind": 1},               // embed-after-commit for a memory: state
	"internal/core/memories/memories.go Edit":         {"MemoryKind": 1},               // the revision message's own kind: state
	"internal/core/rules/trim.go EnvelopeUpperBound":  {"IdentityKind": 1},             // the longest sender kind, for a size bound
	"internal/core/tasks/reclaim.go reclaimAbandoned": {"IdentityKind": 1, "Owner": 1}, // the reclaim revision's sender kind, and the owner it clears: state
	"internal/slackbridge/create.go notice":           {"IdentityKind": 1},             // the bridge notice's sender kind: state
	"internal/core/scopes/acl.go CheckBridgeACL":      {"Perm": 3},                     // judges an ACL's shape
	"internal/slackbridge/create.go withBridgeRead":   {"Perm": 1},                     // ACL shape: adds the bridge's read row
	"internal/slackbridge/create.go reserveTx":        {"Scope": 3},                    // "a bridge requires a scoped channel": validation, the open-scope check, the bridge row's copy
	"internal/mcpserver/server.go toKind":             {"MemoryKind": 1},               // input validation of the kind argument
	"internal/core/health/health.go Health":           {"Owner": 1},                    // the maintenance lock's holder, not a task owner
	"internal/server/convert.go healthToGen":          {"Owner": 1},                    // wire copy of the maintenance lock's holder (kind N)
	"internal/core/maintenance/service.go Tick":       {"Owner": 1},                    // this replica's lock owner name
	"internal/core/maintenance/service.go Release":    {"Owner": 1},                    // this replica's lock owner name
	"internal/slackbridge/runner.go Run":              {"Owner": 2},                    // this replica's lock owner name
	// Task fields judged as state or query filters, never a principal.
	"internal/core/tasks/doc.go taskContent":      {"Owner": 1},  // serializes the task's owner field
	"internal/core/tasks/doc.go loadTasks":        {"Owner": 1},  // parses the task's owner field
	"internal/core/tasks/read.go summaries":       {"Owner": 4},  // in.Owner is a query filter; the summary copies the field
	"internal/core/tasks/reclaim.go abandonedSet": {"Owner": 4},  // t.Owner == "" is state; the owner's sessions decide liveness
	"internal/core/tasks/update.go applyPatch":    {"Owner": 15}, // the patch's own fields: an in_progress task needs an owner, a new owner must be registered, release and completion set it
	"internal/server/tasks.go taskToGen":          {"Owner": 1},  // wire copy of the task's owner (kind N)
	"internal/server/tasks.go ListTasks":          {"Owner": 2},  // wire copy of the owner filter and the summaries' owner (kind N)
	"internal/server/tasks.go UpdateTask":         {"Owner": 1},  // wire copy of the patch's owner (kind N)
}

// aclAllowed maps a site to the exact number of ACL( calls it may make (the
// get-ACL display and the bridge's two ACL mutations; access reads the rows
// to decide).
var aclAllowed = map[string]int{
	"internal/core/channels/channels.go ACL":   1,
	"internal/slackbridge/archive.go Remove":   1,
	"internal/slackbridge/create.go reserveTx": 1, // step 1: reads the ACL to check its shape (CheckBridgeACL)
	"internal/slackbridge/create.go goLive":    1, // step 4: pins the slack-bridge read row
	// The get-ACL display through the Bus and the service, not the Store.
	"internal/cli/channels.go runChannelsACL": 1, // Bus.ACL(ctx, channel)
	"internal/mcpserver/server.go addTools":   1, // Bus.ACL(ctx, channel)
	"internal/bus/local/channels.go ACL":      1, // channels.Service.ACL(ctx, identity, channel)
	"internal/server/channels.go GetACL":      1, // channels.Service.ACL(ctx, identity, channel)
}

// sqlFilters are the flag and creator filters no SQL of internal/store/sqlstore
// may hold: the Store serves facts, never rules (spec 3.7).
var sqlFilters = map[string]*regexp.Regexp{
	"is_admin": regexp.MustCompile(`(?is)\b(where|on|having|when)\b[^;]*\bis_admin\b`),
	"can_*":    regexp.MustCompile(`(?is)\b(where|on|having|when)\b[^;]*\bcan_(read|write|admin)\b`),
	"creator":  regexp.MustCompile(`(?is)\b(where|on|having|when)\b[^;]*\bcreator\b`),
}

// sqlAllowed maps a sqlstore site to the exact number of SQL strings per filter
// it may hold.
var sqlAllowed = map[string]map[string]int{
	// The repair of a scope no administrator or human may hold (accesscheck Task 5).
	"internal/store/sqlstore/identities.go ClearInvalidScopes": {"is_admin": 1},
}

var flagSelectors = map[string]bool{"Admin": true, "Creator": true, "Owner": true, "Scope": true}
var permConsts = map[string]bool{"PermRead": true, "PermWrite": true, "PermAdmin": true}

const moduleRoot = "../../.."

// unit is one site: a top-level function, or one package-level var or const
// spec (its function literals included).
type unit struct {
	site    string
	node    ast.Node
	imports map[string]bool // the file's import names
}

// sites parses every non-test .go file under dirs, skips the files under the
// skip prefixes, and returns each file's units.
func sites(t *testing.T, dirs []string, skip ...string) []unit {
	t.Helper()
	fset := token.NewFileSet()
	var out []unit
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
			for _, s := range skip {
				if strings.HasPrefix(rel, s) {
					return nil
				}
			}
			f, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			imports := map[string]bool{}
			for _, imp := range f.Imports {
				p, _ := strconv.Unquote(imp.Path.Value)
				name := p[strings.LastIndex(p, "/")+1:]
				if imp.Name != nil {
					name = imp.Name.Name
				}
				imports[name] = true
			}
			for _, decl := range f.Decls {
				switch decl := decl.(type) {
				case *ast.FuncDecl:
					out = append(out, unit{rel + " " + decl.Name.Name, decl, imports})
				case *ast.GenDecl:
					if decl.Tok != token.VAR && decl.Tok != token.CONST {
						continue
					}
					for _, spec := range decl.Specs {
						vs := spec.(*ast.ValueSpec)
						names := make([]string, len(vs.Names))
						for i, n := range vs.Names {
							names[i] = n.Name
						}
						out = append(out, unit{rel + " <init:" + strings.Join(names, ",") + ">", vs, imports})
					}
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return out
}

// conditions returns every expression of n that is a decision: an if
// condition, a switch tag, a case expression, or an operand of && || == !=.
func conditions(n ast.Node) []ast.Expr {
	var out []ast.Expr
	ast.Inspect(n, func(n ast.Node) bool {
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

// clauses counts the clauses of one unit. A selector node counts once however
// many conditions nest it.
func clauses(u unit, memoryOnly bool) map[string]int {
	got := map[string]int{}
	calls := map[*ast.SelectorExpr]bool{} // selectors that are a call's function
	ast.Inspect(u.node, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok {
			if s, ok := c.Fun.(*ast.SelectorExpr); ok {
				calls[s] = true
			}
		}
		return true
	})
	ast.Inspect(u.node, func(n ast.Node) bool {
		s, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		name := s.Sel.Name
		if name == "KindMemory" {
			got["MemoryKind"]++
		}
		if memoryOnly {
			return true
		}
		switch {
		case name == "IdentityHuman" || name == "IdentityAgent":
			got["IdentityKind"]++
		case flagSelectors[name] && !calls[s]:
			if x, ok := s.X.(*ast.Ident); ok && u.imports[x.Name] {
				return true // a package's name, such as the action access.Admin
			}
			got[name]++
		}
		return true
	})
	if !memoryOnly {
		perms := map[*ast.SelectorExpr]bool{}
		for _, c := range conditions(u.node) {
			ast.Inspect(c, func(n ast.Node) bool {
				if s, ok := n.(*ast.SelectorExpr); ok && permConsts[s.Sel.Name] {
					perms[s] = true
				}
				return true
			})
		}
		if len(perms) > 0 {
			got["Perm"] = len(perms)
		}
	}
	return got
}

// compare appends to bad every clause whose count differs from the allowlist.
func compare(bad *[]string, what string, got, allowed map[string]map[string]int) {
	for site, cs := range got {
		for clause, n := range cs {
			switch want := allowed[site][clause]; {
			case want == 0:
				*bad = append(*bad, fmt.Sprintf("%s: %s %s (%d, none allowed)", site, what, clause, n))
			case n > want:
				*bad = append(*bad, fmt.Sprintf("%s: %s %s %d times, %d allowed", site, what, clause, n, want))
			}
		}
	}
	for site, cs := range allowed {
		for clause, want := range cs {
			if n := got[site][clause]; n < want {
				*bad = append(*bad, fmt.Sprintf("%s: %s %s %d times, the allowlist holds %d (lower it or remove it)", site, what, clause, n, want))
			}
		}
	}
}

// TestAccessRuleSites is the gate; it fails with every site it finds.
func TestAccessRuleSites(t *testing.T) {
	var bad []string
	got := map[string]map[string]int{}
	add := func(site string, cs map[string]int) {
		for clause, n := range cs {
			if got[site] == nil {
				got[site] = map[string]int{}
			}
			got[site][clause] += n
		}
	}

	// Clauses 1 and 2: the flag, creator, owner and scope selectors, the
	// identity and memory kinds and the Perm conditions; the memory kind also
	// over the clients.
	for _, u := range sites(t, ruleScope, "internal/core/access/") {
		add(u.site, clauses(u, false))
	}
	for _, u := range sites(t, memoryScope) {
		add(u.site, clauses(u, true))
	}
	compare(&bad, "mentions", got, ruleAllowed)

	// Clause 3: ACL( with any arguments, on any receiver.
	acl := map[string]map[string]int{}
	for _, u := range sites(t, []string{"internal"}, "internal/core/access/", "internal/store/sqlstore/") {
		ast.Inspect(u.node, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "ACL" {
					if acl[u.site] == nil {
						acl[u.site] = map[string]int{}
					}
					acl[u.site]["ACL("]++
				}
			}
			return true
		})
	}
	aclWant := map[string]map[string]int{}
	for site, n := range aclAllowed {
		aclWant[site] = map[string]int{"ACL(": n}
	}
	compare(&bad, "calls", acl, aclWant)

	// Clause 4: no rule in the Store's SQL.
	sql := map[string]map[string]int{}
	for _, u := range sites(t, []string{"internal/store/sqlstore"}) {
		for _, s := range sqlStrings(u.node) {
			for name, re := range sqlFilters {
				if re.MatchString(s) {
					if sql[u.site] == nil {
						sql[u.site] = map[string]int{}
					}
					sql[u.site][name]++
				}
			}
		}
	}
	compare(&bad, "SQL filters on", sql, sqlAllowed)

	if len(bad) > 0 {
		sort.Strings(bad)
		bad = slices.Compact(bad)
		t.Fatalf("access decisions outside internal/core/access (fix the site; do not widen the allowlist):\n  %s", strings.Join(bad, "\n  "))
	}
}

// sqlStrings returns the string literals of n, each maximal + chain of string
// literals joined as one string (a non-literal operand reads as a space), and
// the chain's own literals are not repeated.
func sqlStrings(n ast.Node) []string {
	var out []string
	seen := map[*ast.BasicLit]bool{}
	var join func(e ast.Expr) (string, bool)
	join = func(e ast.Expr) (string, bool) {
		switch e := e.(type) {
		case *ast.BasicLit:
			if e.Kind != token.STRING {
				return " ", false
			}
			seen[e] = true
			v, err := strconv.Unquote(e.Value)
			if err != nil {
				return e.Value, true
			}
			return v, true
		case *ast.ParenExpr:
			return join(e.X)
		case *ast.BinaryExpr:
			if e.Op != token.ADD {
				return " ", false
			}
			l, lok := join(e.X)
			r, rok := join(e.Y)
			return l + r, lok || rok
		}
		return " ", false
	}
	ast.Inspect(n, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.BinaryExpr:
			if n.Op == token.ADD {
				if s, ok := join(n); ok {
					out = append(out, s)
				}
				return false
			}
		case *ast.BasicLit:
			if n.Kind == token.STRING && !seen[n] {
				v, err := strconv.Unquote(n.Value)
				if err != nil {
					v = n.Value
				}
				out = append(out, v)
			}
		}
		return true
	})
	return out
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
