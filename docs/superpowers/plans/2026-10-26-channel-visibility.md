# Channel Visibility Implementation Plan (rebuilt on the access check; Eric's decisions of 2026-10-25 and 2026-10-26)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `ListChannels` returns only the channels on which the caller holds a stored ACL row (a server administrator: every channel), each with a required `capabilities` string array whose first known value is `bridged`; the scope-prefix name rule moves into `decide` (issue #18); the wire carries a stamped protocol version (`info.version` `1.0.0`, `protocol_version` on `GET /v1/discover` and `GET /v1/health`); the MCP tool, the CLI and the TUI follow.

**Architecture:** Every new rule is an action of `decide` in `internal/core/access/decide.go`: `List` becomes "a stored row for the principal's names, or the administrator flag", the new `Unsubscribe` and `FilterReceive` actions are each the scope gate alone (one per operation: Decision 95 unsubscribe, and the receive channel filter), and the new `TakeName` action on two new objects (`ChannelName`, `ScopeName`) carries the scope-prefix rules; no Store query is added. `bus.Channel` gains `Capabilities`, computed in `internal/core/channels` from `tx.ListBridges` (state `live` exactly). `internal/version.Protocol` is the protocol version, pinned to `api/openapi.yaml` by a test; the server, `internal/core/health`, the remote client, the MCP/CLI health output and the TUI health overlay carry it.

**Tech Stack:** Go, kin-openapi + oapi-codegen (`make generate`), modernc SQLite and pgx Postgres (one `sqlstore`), testcontainers for the Postgres suites, Bubble Tea v2 (TUI), the MCP Go SDK.

**Spec:** `docs/superpowers/specs/2026-10-25-channel-visibility-design.md` (sections 1 and 4 are Eric's decisions; its 3.1 `Tx.VisibleChannels`, its 3.7 creator clause and its section 5 discovery capability are superseded), read with `docs/superpowers/specs/2026-10-25-accesscheck-design.md` (Context "protocol versioning", 3.2, 3.10, 3.12, 3.13, 4.2, Appendix A C51 and C52), ADR 0011 and issue #18. Supersedes `docs/superpowers/plans/2026-10-25-channel-visibility.md`.

**Eric's decisions of 2026-10-26 this plan implements** (recorded by Task 7):
1. `List` = the principal holds a stored ACL row on the channel (for its name, `everyone`, `admins` when an administrator, `humans` when a human, the names `permsOf` already resolves), or is a server administrator. No creator clause. The scope gate runs first.
2. Unsubscribe gets a dedicated scope-only action, so `List` means one thing. Refined (option A): the scope-only gate is two actions, each named for its operation: `Unsubscribe` (Decision 95, `internal/core/channels/subscriptions.go`) and `FilterReceive` (the `Receive` channel filter, `internal/core/delivery/receive.go:160`, a second scope-only caller of `List` today; leaving it on `List` would turn a filter on a row-less channel from "nothing delivered" into a denial). Both decide identically (the scope gate, then allowed). The names are internal (Go constants of `internal/core/access`); the wire, MCP and CLI names do not change.
3. Issue #18, both parts: `channels.namePrefixRule` and the `scopes.Service.Create` prefix and overlap checks become `decide` rules; both allowlist entries leave `TestAccessRuleSites`; `TestScopeNamespace` and `TestScopeGateChannels` keep their codes and texts.
4. `info.version` becomes `1.0.0`; major = the `/v1` path, minor per addition, patch doc-only; the constant is in `internal/version`, pinned by a test to `info.version`; `GET /v1/discover` and `GET /v1/health` (probe tier, next to `version`) expose `protocol_version`, required in both schemas; no new discovery capability string.
5. Channel `capabilities` (required string array, `[]` never null, first value `bridged` = bridge state `live` exactly, unknown strings passed through by every client) and the TUI bridged icon (🔗 emoji, `fa-slack` Nerd Font) stand as in the old plan.
6. Search hits use the same channel icon as the rail, header and compose prompt: the chat bubble for an ordinary channel, the link icon for a bridged one, the tasks and memory icons as today (today's blank mark for ordinary hits goes).

**Agent choices in this plan** (not Eric's; each is one constant or one line to change):
- The `List` denial (seen by no caller after Tasks 1 and 2; the set form drops the channel) is `forbidden`, Reason `no_permission`, text `%s holds no ACL entry on channel %q`.
- The name rule is one action, `TakeName`, on two objects: `ChannelName{Name, Scope}` (codes `validation`) and `ScopeName{Name}` (codes `conflict`), with a new Reason `name_prefix`.
- The TUI health overlay shows the protocol version on its `server` line.

## Global Constraints

- Done gate: `make verify` (needs Docker; Postgres comes from testcontainers unless `AGENTBUS2_TEST_POSTGRES_DSN` names a reachable server; a run that skips the Postgres suites is not a pass). Work is done only when `ci.yml` and `windows.yml` are green on the pushed commit, `release-check` included; a red `windows.yml` is fixed first. No push without Eric. Every task runs `make lint` and `make test`; Tasks 1 to 4 also run `make test-conformance` (both engines, both transports) and `make verify`; Task 7 runs `make verify`.
- New access rules go only in `decide` (`internal/core/access/decide.go`). Adding or removing a `TestAccessRuleSites` allowlist entry (`internal/core/access/sites_test.go:44-79`) is an explicit edit named in the task; Task 2 removes exactly two (`:47`, `:48`), no task adds one.
- Gate anchors (`scripts/check-gates-selftest.sh`): `if !p.Admin {` stays exactly once in `decide.go` (block 15; `decideServer` `:163`): every new admin test in `decide.go` is written `actsAsAdmin(p)`, never a second `if !p.Admin {`. `if !readable[sub.ChannelID] {` in `internal/core/delivery/receive.go` is not touched.
- Pinned counts that stay: `TestACLMatrix` 15 rows, 26 sub-cases, 52 leaves per engine (`matrixSubcases = 26`, `section46Rows` unchanged; the `list channels` row keeps `perm: ""` and changes content only). `scripts/m3-exit-counts.sh` exits 1 on a matrix-only log (its `search_agree` and `concurrent_send` are 0 there), so a matrix-only run is judged by the three lines `fail=0`, `matrix_sqlite=52`, `matrix_postgres=52`; a full `-v` run of `./internal/conformance/bus/` must exit 0. MCP tool count 25. e2e `TestSection24Sequence` 13 steps and `TestExitCodeTable` 11 cells (`scripts/m6-exit-checks.sh`). `TestAuthEndToEnd` 11 leaves per engine (`scripts/m5-exit-checks.sh`). Milestone 8: `TestBothModes` 2, `TestNoInboxView` 1, `TestRunTUI{Local,Server}Profile` 2 (`scripts/m8-exit-checks.sh`). `TestCapabilitiesComputed` (`internal/server/discovery_test.go:70`) keeps `["health","long_poll"]` and `["health","long_poll","semantic_search"]`: no capability string is added.
- Import rules: any task that adds an import runs `bash scripts/m4-exit-checks.sh --deps-only && bash scripts/m5-exit-checks.sh --deps-only && bash scripts/m6-exit-checks.sh --deps-only && bash scripts/m7-exit-checks.sh --deps-only && bash scripts/m8-exit-checks.sh --deps-only`. New edges in this plan: `internal/core/scopes` imports `internal/core/access` (Task 2; `channels` already does; `access` imports only `internal/bus` and `internal/store`, so no cycle); `internal/core/health` imports `internal/version` (Task 4; the m4 rule forbids only `internal/gen` and `internal/server` there); `internal/tui` already imports `internal/version` (allowed by m8). `internal/version` stays a leaf: its new test uses the standard library only.
- Word checks: `scripts/m6-exit-checks.sh` covers `internal/mcpserver/server.go`, `inputs.go` and `internal/cli/skills/using-agentbus2/SKILL.md` (no `dm/`, `-filter`, `@<`, `memory/`, `tasks/`, `general/` in added text); `scripts/m8-exit-checks.sh` covers `internal/tui` non-test files and `internal/cli/tui.go` (no `inbox`, `dm/`, `DMPrefix`, `DMChannel`, `IsTaskChannel`, `tui_name`).
- `api/openapi.yaml` changes only in Tasks 3 and 4, each followed by `make generate`; `internal/gen` and `internal/gen/spec` are committed as generated, never edited by hand; `make generate-check` is clean afterwards.
- Pre-release rule (Eric, 2026-10-25): no migrations, compatibility layers, shims or deprecation paths. The `1.0.0` protocol already includes everything in this plan.
- Capability principle (spec 3.2): `capabilities` is an array of strings with no enum in the schema; every client copies unknown strings through and acts only on `bridged`; an absent array normalizes to `[]` and is never refused; JSON arrays are `[]` when empty, never null.
- Each task: failing tests first, exact `git add` list (never `-A`), one commit on `main` ending with:
  ```
  Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01DRnnV9QPme9fCDtmKoxFiW
  ```
- Docs move with the code in the task that changes the behavior, with one exception: the agent-facing text (the MCP `list_channels` description and `SKILL.md`) changes once, in Task 5, for both the filtered list and `capabilities`. Task 7 is the records sweep. Check every factual sentence against the committed code (Definition of Done E). American English. Out of scope: issues #19 and #20.
- Definition of Done F: anything beyond a one-liner goes in a script file with `set -euo pipefail`. Logs go to `${TMPDIR:-/tmp}`.

## Review Focus

1. A reader whose rows were revoked can still `unsubscribe` and can still name the channel in a `receive` channel filter (empty result, no error), scoped in-scope and unscoped: `Unsubscribe` and `FilterReceive`, pinned by `TestUnsubscribeNeedsNoRow` and the new `TestReceiveFilterNeedsNoRow` (Task 1).
2. Group rows list a channel exactly as they grant permissions: a human with only a `humans` row lists it, an agent with only a `humans` row does not, a non-administrator with only an `admins` row does not, an administrator with no row lists everything, a creator with no row lists nothing (unit cases in Task 1; `TestChannelsAgreesWithCheck` proves the set form agrees, including `humans`).
3. A scope in any state (open, done, retired and not yet purged) claims its `<scope>-` prefix for channel names and for nesting scope names, through `Checker.load` reading `tx.ListScopes` (every state): `TestCheckTakeNameLoadsEveryScopeState` (Task 2).
4. `capabilities` is present and `[]` (never null) on every wire response that carries a channel: `GET /v1/channels`, the `POST /v1/channels` 201 and `GET /v1/admin/status` channels (Task 3 `TestChannelCapabilitiesOnTheWire`).
5. A response without `capabilities` or without `protocol_version` (a server speaking an earlier draft) is mapped by the remote client to `[]` and `""` without an error (Tasks 3 and 4 remote tests); an unknown capability string passes through: Task 3's `TestListChannelsCapabilities` (`"future"` kept) and Task 6's `TestBridgedChannelIcon` (`odd` draws the plain chat icon); Task 5's outputs copy `c.Capabilities` as strings unfiltered.

---

### Task 1: `List` is a stored row or the administrator flag; the scope-only `Unsubscribe` and `FilterReceive` actions

**Files:**
- Modify: `internal/core/access/objects.go:12` (`List` comment; new `Unsubscribe` and `FilterReceive` constants), `internal/core/access/decide.go` (`decideChannel` `:215-258`: the action switch `:217` and the `List` case `:238-239`; a helper shared with `permsOf` `:186-209`), `internal/core/access/checker.go:145-151` (`channelAction` accepts both), `internal/core/channels/channels.go:20-24` (`List` doc comment), `internal/core/channels/subscriptions.go:60-65` (Unsubscribe uses `access.Unsubscribe`), `internal/core/delivery/receive.go:158-162` (the filter uses `access.FilterReceive`), `docs/tui.md:91` (the `(no access)` marker: an administrator, a write-only or admin-only holder, the revocation window)
- Test: `internal/core/access/decide_test.go`, `internal/conformance/access/checker_test.go:184`, `internal/conformance/bus/channels_test.go:155-176` (replace `TestChannelListNeedsNoRead`), `internal/conformance/bus/aclmatrix_test.go:359-370` (the `list channels` row), `internal/conformance/bus/accesschannels_test.go` (new `TestReceiveFilterNeedsNoRow` after `TestUnsubscribeNeedsNoRow` `:475`; the comment at `:133`), `internal/tui/model_test.go:863-913` (`TestNoAccessChannelInRail`), `internal/tui/client_test.go:191-222` (`TestNoAccessChannelIsMarkedOnce`)

**Interfaces:**
- Consumes: `permsOf`, `actsAsAdmin`, `inScope`, `needs`, `deny` (`decide.go`); `Checker.Channels` (`checker.go:155`, already loads `ACLRowsFor(p.Name, everyone, admins-if-admin, humans-if-human)`); harness helpers `h.channel`, `h.ensureIdentity`, `h.as`, `h.admin`, `h.human`, `h.localAs`, `h.localAdmin`, `h.scopedIdentity`, `h.rtScope`, `channelNames` (`aclmatrix_test.go:156`), `fixOf(h, ch).kim`, `aclPerms`, `must`; TUI `setAdmin` (`internal/tui/fixture_test.go:123`), `newCountingFixture` (TUI identity `eric`), `identityBus` (client_test identity `ann`).
- Produces: `Unsubscribe Action = "unsubscribe"` (channel: the scope gate alone; Decision 95) and `FilterReceive Action = "filter_receive"` (channel: the scope gate alone; the receive channel filter). `List` with the rule of Eric's decision 1. Helper `func appliesTo(p Principal, e store.ACLEntry) bool` (the name match `permsOf` uses today: `p.Name`, `everyone`, `admins` when `actsAsAdmin(p)`, `humans` when `p.Kind == store.IdentityHuman`); `permsOf` calls it; `List` allows when `actsAsAdmin(p)` or any row satisfies `appliesTo`.

- [ ] **Step 1: Write the failing unit tests** in `internal/core/access/decide_test.go`:
  - `TestDecideChannelRules` (`:34`): change `:55` to `{"list unscoped, no rows", pat, chFacts("", ord), List, false, ReasonNoPermission}`, `:56` to `{"list unscoped, another's row", pat, chFacts("", ord, entry("kim", r)), List, false, ReasonNoPermission}`, `:66` to `{"scoped in scope list, no rows", coder, chFacts("job1", ord), List, false, ReasonNoPermission}`; add `{"list via own write row", pat, chFacts("", ord, entry("pat", w)), List, true, ""}`, `{"list via own admin row", pat, chFacts("", ord, entry("pat", []store.Permission{store.PermAdmin})), List, true, ""}`, `{"list via everyone", pat, chFacts("", ord, entry(store.Everyone, w)), List, true, ""}`, `{"list admin, no rows", root, chFacts("", ord), List, true, ""}`, `{"list admins row, non-admin", pat, chFacts("", ord, entry(store.Admins, r)), List, false, ReasonNoPermission}`, `{"list humans row, human", hal, chFacts("", ord, entry(store.Humans, r)), List, true, ""}`, `{"list humans row, agent", pat, chFacts("", ord, entry(store.Humans, r)), List, false, ReasonNoPermission}`, `{"creator without a row does not list", pat, channelFacts{Channel: store.Channel{ID: 1, Name: "work", Creator: "pat"}}, List, false, ReasonNoPermission}`, `{"scoped list via everyone", coder, chFacts("job1", ord, entry(store.Everyone, r)), List, true, ""}`, and for each of `Unsubscribe` and `FilterReceive` (as `A`): `{"A unscoped, no rows", pat, chFacts("", ord), A, true, ""}`, `{"A scoped in scope, no rows", coder, chFacts("job1", ord), A, true, ""}`, `{"A admin, no rows", root, chFacts("", ord), A, true, ""}`.
  - `TestDecideDenialTexts` (`:87`): `decide(pat, chFacts("", store.KindOrdinary), List)` is `forbidden` with Text `pat holds no ACL entry on channel "work"`.
  - `TestScopeGate` (`:188`): the action list `:193` gains `Unsubscribe` and `FilterReceive`; the unscoped-principal check `:199` covers `Read`, `Write`, `List`, `Unsubscribe`, `FilterReceive` (the rows there are `everyone [read, write]`).
  - `TestAnonymousDeniedEverything` (`:232`): `all` gains `Unsubscribe` and `FilterReceive`.
  - `TestEmptyNameOnChannelWithoutCreator` (`:370`): the action list gains `List`.
  - `internal/conformance/access/checker_test.go:184`: `actions` gains `access.Unsubscribe` and `access.FilterReceive` (`TestChannelsAgreesWithCheck` then proves `Channels` agrees with `Check` for both).

- [ ] **Step 2: Write the failing bus-suite tests.**
  - Replace `TestChannelListNeedsNoRead` (`channels_test.go:155`) with `TestChannelListFiltersByEntry` under `dbtest.ForEachEngine`: `h.ensureIdentity("pat")`, `h.ensureIdentity("kim")`, `hal := h.human("hal@corp.example")`; channels through `h.channel`: `secret` (`KindMemory`, creator `""`, `[{pat, [read]}]`), `alpha` (ordinary, creator `pat`, `nil` = the default ACL with `everyone`), `adminonly` (creator `kim`, `[{kim, [admin]}]`), `writeonly` (creator `""`, `[{kim, [write]}]`), `mine` (creator `kim`, `[]ab2store.ACLEntry{}`), `humansonly` (creator `""`, `[{humans, [read]}]`). Assert with `channelNames` (exact map equality): `h.as("kim")` = `{adminonly: ordinary, alpha: ordinary, writeonly: ordinary}` (`mine` absent: a creator without a row lists nothing); `h.as("pat")` = `{alpha, secret: memory}`; `hal` = `{alpha, humansonly}`; `h.admin("root")` = all six. Also assert `h.as("kim").ListChannels` returns names in byte order.
  - Matrix row `list channels` (`aclmatrix_test.go:359-370`), keep `row`, `perm: ""`, `kind` and the sub-case name: `refuse` asserts `_, ok := channelNames(t, b)[ch]; ok` is false (`t.Fatalf("channel %s listed without a row: %v", ...)`); `allow` first runs `must(t, h.as("pat").SetACL(ctx(), ab2bus.SetACLInput{Channel: ch, Entries: []ab2bus.ACLEntry{{Identity: "pat", Permissions: aclPerms(ab2bus.PermRead, ab2bus.PermWrite, ab2bus.PermAdmin)}, {Identity: fixOf(h, ch).kim, Permissions: aclPerms(ab2bus.PermWrite)}}}))`, then asserts `channelNames(t, b)[ch] == ab2bus.KindOrdinary` (any row lists, not only `read`).
  - `TestReceiveFilterNeedsNoRow` (`accesschannels_test.go`, after `:503`), modeled on `TestUnsubscribeNeedsNoRow`: for `{coder, "job1-filter-ch"}` (`h.rtScope("job1")`, `h.scopedIdentity("job1-coder", "job1")`) and `{pat, "plain-filter-ch"}` (`h.localAs("pat")`): the identity creates the channel, `root := h.localAdmin("root")` clears its ACL with `root.SetACL(ctx, SetACLInput{Channel: ch})`, the identity opens a session and `Receive(ctx, ReceiveInput{Session: s.ID, Channels: []string{ch}})` returns no error and no messages. It passes today (the filter uses the interim `List`) and guards the move to `FilterReceive`.
  - `accesschannels_test.go:133`: the assertion `len(got) != 4` stays; the comment becomes `// every channel here carries the default everyone row`.
  - TUI (`internal/tui/model_test.go`): `TestNoAccessChannelInRail` becomes three tests over `newCountingFixture` (the TUI identity is `eric`): `TestChannelWithoutEntryAbsentFromRail` (sam creates `private` with `[{sam, [read, write, admin]}]`; after `f.run(f.m.statusCmd())` the rail lacks `private`, `f.c.known("private")` is false, `cb.count("Subscribe")` and `cb.count("History")` record no call naming `private`); `TestAdminSeesNoAccessRow` (`setAdmin(t, f.core, "eric")` before the first `statusCmd`, then every assertion of today's test body `:874-912` unchanged); `TestWriteOnlySeesNoAccessRow` (`private`'s ACL adds `{eric, [write]}`, then the same assertions). `internal/tui/client_test.go` `TestNoAccessChannelIsMarkedOnce`: `setAdmin(t, f.core, "ann")` before `newClient`; its assertions stay.

- [ ] **Step 3: Run them to verify they fail**

Run: `go test -count=1 ./internal/core/access/ ; go test -count=1 -p 1 -run 'TestChannelListFiltersByEntry|TestACLMatrix|TestChannelsAgreesWithCheck' ./internal/conformance/bus/ ./internal/conformance/access/ ; go test -count=1 -run 'TestChannelWithoutEntryAbsentFromRail' ./internal/tui/`
Expected: compile failure (`Unsubscribe`/`FilterReceive` undefined), then after a stub the `List` unit cases, `TestChannelListFiltersByEntry`, the matrix `list channels/refusal` leaves and `TestChannelWithoutEntryAbsentFromRail` fail.

- [ ] **Step 4: Implement.** `appliesTo` extracted from `permsOf`'s condition (`:191-192`); `decideChannel` accepts `Unsubscribe` and `FilterReceive` in its switch (one case) and returns `allowed` for them after the scope gate; the `List` case allows `actsAsAdmin(p)` or any `appliesTo` row, else `deny(bus.CodeForbidden, ReasonNoPermission, "%s holds no ACL entry on channel %q", p.Name, f.Channel.Name)`. `channelAction` adds both. `subscriptions.go:63` calls `Require(..., access.Unsubscribe)` and `receive.go:160` calls `Require(..., access.FilterReceive)`; their comments say the action is the scope gate alone. `channels.List` doc: "List returns the channels the identity may list (the List action: a stored ACL row for the identity's names, or the administrator flag; Eric's decision 2026-10-26 (1)), by name with their kind." `docs/tui.md:91` rewritten for the three cases. `rg -n 'access\.List\b' internal --glob '!*_test.go'` then shows exactly one hit (`internal/core/channels/channels.go:28`, the set form). Then run `make test` and `make test-conformance`; any other test that relied on Decision 94 (a channel listed without a row) is changed to grant the lister a row or to list as an administrator, and each such file is added to Step 6's `git add` list and named in the commit body.

- [ ] **Step 5: Run**

Run: `make lint && make test && make test-conformance && go test -count=1 -p 1 -v -run TestACLMatrix ./internal/conformance/bus/ > "${TMPDIR:-/tmp}/ab2-m3.log" 2>&1; bash scripts/m3-exit-counts.sh "${TMPDIR:-/tmp}/ab2-m3.log"; make verify`
Expected: PASS on both engines and both transports; the m3 script prints `fail=0`, `matrix_sqlite=52`, `matrix_postgres=52` (and exits 1 for the zero `search_agree`/`concurrent_send` of a matrix-only log); `make verify` exits 0 (keep its output for the report).

- [ ] **Step 6: Commit**

```bash
git add internal/core/access/objects.go internal/core/access/decide.go internal/core/access/checker.go internal/core/access/decide_test.go internal/core/channels/channels.go internal/core/channels/subscriptions.go internal/core/delivery/receive.go internal/conformance/access/checker_test.go internal/conformance/bus/channels_test.go internal/conformance/bus/aclmatrix_test.go internal/conformance/bus/accesschannels_test.go internal/tui/model_test.go internal/tui/client_test.go docs/tui.md
git commit -m "feat(access): List is a stored ACL row or the administrator flag; Unsubscribe and FilterReceive are the scope-only gates (Eric 2026-10-26)

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01DRnnV9QPme9fCDtmKoxFiW"
```

---

### Task 2: The scope-prefix name rules are `decide`'s `TakeName` action (issue #18)

**Files:**
- Modify: `internal/core/access/objects.go` (`ChannelName`, `ScopeName`, `TakeName`), `internal/core/access/decide.go` (facts types, `inScope`, `objectKind`, `decide`'s switch, `decideChannelName`, `decideScopeName`, `ReasonNamePrefix`), `internal/core/access/checker.go` (`load` cases), `internal/core/channels/channels.go` (delete `namePrefixRule` `:115-137`; `Create` `:96` and `Rename` `:244` call `Require`; drop the `strings` import if unused), `internal/core/scopes/service.go:53-100` (`Create`), `internal/core/access/sites_test.go:47-48` (delete both entries), `docs/superpowers/specs/2026-10-25-accesscheck-design.md` (Appendix A rows C51 `:1323` and C52 `:1324`; the 3.7 item 4 sentence `:521-523` naming them), `docs/adr/0011-one-access-check.md:56` (item 19: the rule is `decide`'s `TakeName`; "Moving the prefix rule into `decide` is issue #18" becomes "issue #18, closed")
- Test: `internal/core/access/decide_test.go` (`TestDecideTakeName`; `TestUndefinedPairIsInternal`, `TestAnonymousDeniedEverything`), `internal/conformance/access/checker_test.go` (`TestCheckTakeNameLoadsEveryScopeState`); unchanged and must pass: `internal/conformance/bus/scopenamespace_test.go` `TestScopeNamespace`, `accesschannels_test.go:51` `TestScopeGateChannels`, `scopes_test.go:173` `TestScopedChannels`

**Interfaces:**
- Consumes: `access.Resolve`, `Checker.Require`, `tx.ListScopes` (every state, by name), `tx.ListChannels`; `deny`, `undefined`.
- Produces: `type ChannelName struct{ Name, Scope string }` (a name proposed for a channel that will carry `Scope`); `type ScopeName struct{ Name string }` (a name proposed for a new scope); `const TakeName Action = "take_name"` (defined for those two objects only); `const ReasonNamePrefix = "name_prefix"`; facts `channelNameFacts{Name, Scope string; Scopes []store.Scope}` and `scopeNameFacts{Name string; Scopes []store.Scope; Channels []store.Channel}`. The rules, transcribed from `namePrefixRule` and `scopes.Service.Create` in their current order, with byte-identical texts:
  - ChannelName/TakeName: (1) `p.Scope != ""` and the name lacks the prefix `p.Scope+"-"`: `validation`, `a channel of scope %q is named %s-<name>` (args `p.Scope, p.Scope`; note `%s`); (2) for each scope `sc` with the prefix `sc.Name+"-"` and `f.Scope != sc.Name`: `validation`, `names starting with %q- belong to scope %q` (`sc.Name, sc.Name`).
  - ScopeName/TakeName: (1) for each scope `sc` whose `sc.Name+"-"` and `f.Name+"-"` nest (either is a prefix of the other) and `sc.Name != f.Name` (the same name is the Store's own conflict): `conflict`, `scope %q: the name prefix %q- overlaps scope %q` (`f.Name, f.Name, sc.Name`); (2) for each channel `c` with the prefix `f.Name+"-"` and `c.Scope != f.Name`: `conflict`, `scope %q: the name prefix %q- is in use by channel %q` (`f.Name, f.Name, c.Name`).
  - `inScope` returns true for both facts kinds (the scoped principal is handled by rule (1); `ScopeName` callers hold `Administer` first). `objectKind`: `"channel name"`, `"scope name"`. Any other action on them, and `TakeName` on any other object, is `undefined` (`internal`).

- [ ] **Step 1: Write the failing tests.**
  - `TestDecideTakeName` (table, exact `Code`, `Reason`, `Text`): `coder` + `channelNameFacts{Name: "x", Scope: "job1", Scopes: [job1]}` → `validation` `a channel of scope "job1" is named job1-<name>`; `coder` + `job1-x` → allowed; `pat` + `{Name: "job1-y", Scope: ""}` → `validation` `names starting with "job1"- belong to scope "job1"`; `root` + `{"job1-z", "job1"}` → allowed; `root` + `{"k-chat", "job1"}` → allowed; `pat` + `{"job2-a", "", Scopes: [{Name: "job2", State: store.ScopeRetired}]}` → `validation`; `pat` + `{"plain", ""}` → allowed; `root` + `scopeNameFacts{Name: "ova-2", Scopes: [ova]}` → `conflict` `scope "ova-2": the name prefix "ova-2"- overlaps scope "ova"`; `{Name: "ovb", Scopes: [ovb-2]}` → `conflict` overlaps `"ovb-2"`; `{Name: "job1", Scopes: [job1]}` → allowed; `{Name: "job2", Channels: [{Name: "job2-old"}]}` → `conflict` `scope "job2": the name prefix "job2"- is in use by channel "job2-old"`; `{Name: "job2", Channels: [{Name: "job2-old", Scope: "job2"}]}` → allowed; `{Name: "jo", Channels: [{Name: "job2-old"}]}` → allowed (`job2-old` does not start with `jo-`).
  - `TestUndefinedPairIsInternal`: `decide(root, channelNameFacts{}, Read)` and `decide(root, scopeNameFacts{}, Administer)` are `internal`; `decide(root, chFacts("", store.KindOrdinary), TakeName)` is `internal`.
  - `TestAnonymousDeniedEverything`: `fs` gains both facts kinds, `all` gains `TakeName`.
  - `TestCheckTakeNameLoadsEveryScopeState` (conformance/access, both engines): through `mustTx`, `CreateScope` `old` then `MarkScopeRetired`, `CreateScope` `dn` then `MarkScopeDone(…, store.RetireNormal, now)`; with `p` resolved for `pat`: `checker.Check(ctx, tx, p, access.ChannelName{Name: "old-x"}, access.TakeName)` and `{Name: "dn-x"}` are denied `validation`; with `root`: `access.ScopeName{Name: "old-2"}` is denied `conflict` (overlaps `old`).

- [ ] **Step 2: Run them to verify they fail**

Run: `go test -count=1 -run 'TestDecideTakeName|TestUndefinedPairIsInternal|TestAnonymousDeniedEverything' ./internal/core/access/ ; go test -count=1 -p 1 -run TestCheckTakeNameLoadsEveryScopeState ./internal/conformance/access/`
Expected: compile failure (`ChannelName`, `ScopeName`, `TakeName` undefined).

- [ ] **Step 3: Implement.** `Checker.load`: `ChannelName` → `tx.ListScopes`; `ScopeName` → `tx.ListScopes` and `tx.ListChannels` (store errors through `fromStore`). `channels.Create`: replace the `namePrefixRule` call at `:96` (same position, after `RequireOpen`) with `s.Access.Require(ctx, tx, p, access.ChannelName{Name: in.Name, Scope: scope}, access.TakeName)`; `Rename` `:244` with `access.ChannelName{Name: in.NewName, Scope: ch.Scope}` (after `refuseBridged`, as today). `scopes.Service.Create`: replace `s.RequireServerAdmin(ctx, tx, admin)` `:58` with `p, err := access.Resolve(ctx, tx, admin)` and `s.Access.Require(ctx, tx, p, access.Server{}, access.Administer)` (the body of `RequireServerAdmin`, so its codes and texts are unchanged), and replace the two loops `:62-86` inside the `WithReceipt` closure (keyed replay still answers before the checks) with `s.Access.Require(ctx, tx, p, access.ScopeName{Name: in.Name}, access.TakeName)`; drop the `strings` import if unused. Delete `sites_test.go:47` (`channels.go namePrefixRule`) and `:48` (`scopes/service.go Create`); `:46` (`channels.go Create`, `{"Scope"}`) stays because `in.Scope != ""` still fires. C51 and C52 become: "moved into `decide` (`TakeName` on `ChannelName` and `ScopeName`; Eric's decision 2026-10-26; issue #18); no longer a gate exemption"; the 3.7 item 4 sentence drops them from the allowlist list.

- [ ] **Step 4: Run**

Run: `make lint && make test && make test-conformance && go test -count=1 -run TestAccessRuleSites -v ./internal/core/access/ && bash scripts/m4-exit-checks.sh --deps-only && bash scripts/m5-exit-checks.sh --deps-only && bash scripts/m6-exit-checks.sh --deps-only && bash scripts/m7-exit-checks.sh --deps-only && bash scripts/m8-exit-checks.sh --deps-only && make verify`
Expected: PASS; `TestScopeNamespace`, `TestScopeGateChannels` and `TestScopedChannels` pass unedited (codes and texts unchanged); `TestAccessRuleSites` passes with the two entries gone; `deps_ok`; `make verify` exits 0.

- [ ] **Step 5: Commit**

```bash
git add internal/core/access/objects.go internal/core/access/decide.go internal/core/access/checker.go internal/core/access/decide_test.go internal/core/access/sites_test.go internal/core/channels/channels.go internal/core/scopes/service.go internal/conformance/access/checker_test.go docs/superpowers/specs/2026-10-25-accesscheck-design.md docs/adr/0011-one-access-check.md
git commit -m "refactor(access): the scope-prefix name rules are decide's TakeName action on ChannelName and ScopeName

Channels Create and Rename and scopes Create call Access.Require; the
namePrefixRule and scopes Create allowlist entries leave TestAccessRuleSites
(Eric's decision 2026-10-26). Codes and texts are unchanged.

Closes #18

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01DRnnV9QPme9fCDtmKoxFiW"
```

---

### Task 3: Channel capabilities on the Bus and the wire (`bridged` while the bridge is `live`)

The Bus type, the core computation and the wire land together: the bus suite runs under the `inproc` transport too, so a Bus member the remote client cannot yet carry would fail that leg.

**Files:**
- Modify: `internal/bus/types.go:29-42` (`ChannelCapability`, `ChannelBridged`, `Channel.Capabilities`, `Channel.Has`; the comments stop citing Decision 94), `internal/core/convert.go:113-122` (`ChannelToBus` sets `Capabilities: []bus.ChannelCapability{}`; comment no longer cites Decision 94), `internal/core/channels/channels.go` (`List` `:24-48` loads bridges; new `capabilitiesOf` and `bridgesByChannel`; the `liveBridge` comment `:172-176` says the pin rule (`creating` or `live`) is deliberately wider than the `bridged` capability), `internal/core/channels/admin.go:42-48` (`Status` sets the same capabilities), `api/openapi.yaml` (`Channel` `:1454` `required: [name, kind, capabilities]`; `ChannelInfo` `:1463` `required: [name, kind, creator, created_at, capabilities]`; both gain `capabilities: {type: array, items: {type: string}, description: "Traits of the channel; a value present means the trait applies. Known: bridged (a live Slack bridge). Clients ignore values they do not know."}`, no enum; the `listChannels` 200 description `:241` becomes "Channels the caller holds an ACL entry on; a server administrator sees every channel."), `internal/gen/*` and `internal/gen/spec/*` (by `make generate`), `internal/server/channels.go:43-61` (`ListChannels` and `channelInfoToGen` copy the strings, `[]string{}` when empty: a nil `[]string` would marshal as `null`), `internal/bus/remote/channels.go:13-29,60-65` (`ListChannels` and `fromGenChannelInfo` copy the strings; nil normalizes to `[]ab2bus.ChannelCapability{}`; `admin.go:40` inherits it through `fromGenChannelInfo`)
- Test: `internal/conformance/bus/channels_test.go` (`TestChannelListCapabilities`; `TestChannelListFiltersByEntry` gains a capabilities assertion), `internal/server/channels_test.go` (`TestChannelCapabilitiesOnTheWire`), `internal/bus/remote/channels_test.go` (new: `TestListChannelsCapabilities`)

**Interfaces:**
- Consumes: `tx.ListBridges` (`internal/store/bridges.go:55`; one bridge row per channel, `CreateBridge` is `ErrConflict` when the channel has one), `store.BridgeLive`; harness `h.rtScope`, `h.admin`, `h.as` (transport-aware, unlike `h.localAs`), `h.rtChannel` (`retire_test.go:102`, creator `root`, the default ACL with `everyone`), `h.rtBridge` (`:119`, a live bridge), `h.rtArchiveBridge` (`:141`), `h.tx` + `tx.CreateBridge` (a `creating` bridge, pattern `bridgeguard_test.go:107-110`); server env `e.identity(name, admin bool)`, `e.client(identity)`, `e.raw(identity, req)` (`internal/server/testenv_test.go:149,166,178`); remote `canned` and `newBus` (`internal/bus/remote/remote_test.go:20,34`); generated `gen.Channel.Capabilities []string`, `gen.ChannelInfo.Capabilities []string`.
- Produces: in `internal/bus`: `type ChannelCapability string`; `const ChannelBridged ChannelCapability = "bridged"`; `Channel.Capabilities []ChannelCapability` (never nil on the Bus; doc: "traits of the channel; a value present means the trait applies; clients ignore values they do not know"); `func (c Channel) Has(cap ChannelCapability) bool`. In `internal/core/channels`: `func bridgesByChannel(ctx context.Context, tx store.Tx) (map[int64]store.Bridge, error)` and `func capabilitiesOf(bridges map[int64]store.Bridge, channelID int64) []bus.ChannelCapability` (`[]bus.ChannelCapability{bus.ChannelBridged}` when that channel's bridge has `State == store.BridgeLive`, else `[]bus.ChannelCapability{}`). `CreateChannel` answers `[]` (no bridge can exist yet).

- [ ] **Step 1: Write the failing tests.**
  - `TestChannelListCapabilities` (both engines, both transports): `h.rtScope("job1")`, `root := h.admin("root")`, `pat := h.as("pat")` (`newBus`, so the `inproc` leg goes through the remote client); four channels by `h.rtChannel("job1", name, ab2store.KindOrdinary)`: `job1-live` (`h.rtBridge(ch, "job1", "C1")`), `job1-creating` (`tx.CreateBridge` only), `job1-archived` (`h.rtBridge(ch, "job1", "C2")` then `h.rtArchiveBridge(b.ID)`), `job1-none`. `pat.ListChannels` reports `Capabilities` equal (`reflect.DeepEqual`) to `[]ab2bus.ChannelCapability{ab2bus.ChannelBridged}` for `job1-live` and to `[]ab2bus.ChannelCapability{}` (non-nil) for the other three; `Has(ab2bus.ChannelBridged)` agrees; `root.Status` reports the same per channel in `Channels`; `pat.CreateChannel(ctx, {Name: "fresh"})` answers `[]ab2bus.ChannelCapability{}`. `TestChannelListFiltersByEntry`: every listed channel's `Capabilities` is non-nil and empty.
  - `TestChannelCapabilitiesOnTheWire`: through `e.raw`, the JSON of `GET /v1/channels`, of a `POST /v1/channels` 201 and of `GET /v1/admin/status` (as an administrator) carries `"capabilities":[]` on every channel (decode each channel into `map[string]json.RawMessage` and require the raw value `[]`, never `null` or absent); a second identity not named in a private channel's ACL does not get that channel from `GET /v1/channels`.
  - `TestListChannelsCapabilities` (canned 200 `[{"name":"a","kind":"ordinary","capabilities":["bridged","future"]},{"name":"b","kind":"ordinary"}]`): `a` maps both strings and `Has(ChannelBridged)` is true; `b` maps to a non-nil empty slice; no error.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test -count=1 -p 1 -run 'TestChannelListCapabilities|TestChannelListFiltersByEntry' ./internal/conformance/bus/ ; go test -count=1 -run TestChannelCapabilitiesOnTheWire ./internal/server/ ; go test -count=1 -run TestListChannelsCapabilities ./internal/bus/remote/`
Expected: compile failure (`Capabilities`, `ChannelCapability` undefined; `gen.Channel` has no `Capabilities`).

- [ ] **Step 3: Implement.** The Bus type; `List` and `Status` call `bridgesByChannel` once inside their read transaction and set `Capabilities` from `capabilitiesOf`; `ChannelToBus` gives every other path an empty non-nil slice (`ChannelInfo` inherits it through the embedded `Channel`). Edit `api/openapi.yaml` as listed, `make generate`, then the server and remote mappings.

- [ ] **Step 4: Run**

Run: `make generate-check && make lint && make test && make test-conformance && make verify`
Expected: PASS on both engines and both transports (the `inproc` leg proves the remote `Bus` returns the filtered list with `Capabilities`); `make verify` exits 0.

- [ ] **Step 5: Commit**

```bash
git add internal/bus/types.go internal/core/convert.go internal/core/channels/channels.go internal/core/channels/admin.go api/openapi.yaml internal/gen internal/server/channels.go internal/server/channels_test.go internal/bus/remote/channels.go internal/bus/remote/channels_test.go internal/conformance/bus/channels_test.go
git commit -m "feat(core,api): required channel capabilities on the Bus and the wire; bridged while the channel's bridge is live (Eric 2026-10-25)

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01DRnnV9QPme9fCDtmKoxFiW"
```

---

### Task 4: Protocol version `1.0.0`: `info.version`, `version.Protocol`, `protocol_version` on discover and health

**Files:**
- Modify: `internal/version/version.go` (`const Protocol = "1.0.0"`), `api/openapi.yaml` (`info.version` `:4` → `1.0.0`; `Discovery` `:1205-1233`: `required` gains `protocol_version`, property `protocol_version: {type: string, description: "The protocol version (this document's info.version): the major is the /v1 path, the minor is bumped on every addition, the patch on a documentation-only change. Independent of server_version."}`; `Health` `:1844-1878`: `required: [status, tier, version, protocol_version, now]` and the same property, a probe-tier member), `internal/gen/*`, `internal/gen/spec/*` (by `make generate`), `internal/server/discovery.go:115-123` (`ProtocolVersion: version.Protocol`), `internal/server/convert.go:51-56` (`healthToGen` copies `ProtocolVersion`), `internal/bus/health.go:66-77` (`Health.ProtocolVersion string`, probe tier), `internal/core/health/health.go:46-50,109` (rename the `version` parameter to `serverVersion`, import `internal/version`, set `ProtocolVersion: version.Protocol` in the probe-tier literal), `internal/bus/remote/convert.go:156-160` (`fromGenHealth` copies `ProtocolVersion`; `""` is accepted, never refused), `docs/ops/health.md` (`:43` table gains a `protocol_version` row, probe tier; the example bodies `:81` and `:100` gain `"protocol_version":"1.0.0"`; `:121` says a new operation or member bumps the protocol minor instead of adding a capability, and the existing capabilities stay), `docs/ops/deploy.md:101` (the liveness body lists `protocol_version`), `docs/ops/upgrade.md:6` (one line: `GET /v1/discover` and `GET /v1/health` report `protocol_version`, the wire protocol, independent of the build version)
- Test: `internal/version/version_test.go` (new), `internal/server/discovery_test.go` (`TestDiscoverAgreesWithDocument` `:23`; `TestCapabilitiesComputed` `:70` unchanged), `internal/server/health_test.go:16-44` (`TestHealthProbeTier`: five keys), `internal/core/health/health_test.go:96` (`TestHealthProbeTier`), `internal/bus/remote/health_test.go` (`TestHealthConvertsEveryMember` `:40`; new `TestHealthWithoutProtocolVersion`)

**Interfaces:**
- Consumes: generated `gen.Discovery.ProtocolVersion string`, `gen.Health.ProtocolVersion string`.
- Produces: `version.Protocol` (const; doc: "Protocol is the wire protocol version, api/openapi.yaml info.version (Eric's decision 2026-10-26): the major is the /v1 path, the minor is bumped on every addition, the patch on a documentation-only change. It is independent of Version."); `bus.Health.ProtocolVersion`.

- [ ] **Step 1: Write the failing tests.**
  - `TestProtocolMatchesOpenAPI` (`internal/version/version_test.go`, standard library only, so the package stays a leaf): read `../../api/openapi.yaml`, strip each line's trailing `\r` and spaces (`strings.TrimRight(line, "\r ")`; `windows.yml` may check out CRLF), find the first line with `strings.HasPrefix(line, "  version:")` after the line `info:`, and `strings.TrimSpace` the value after the colon; it equals `Protocol`, and `Protocol` matches `^[0-9]+\.[0-9]+\.[0-9]+$`; a file without `info:` or the line fails naming the file.
  - `TestDiscoverAgreesWithDocument`: `d.ProtocolVersion == version.Protocol` and `d.ProtocolVersion == Spec().Info.Version`.
  - `TestHealthProbeTier` (server): `h.ProtocolVersion == version.Protocol`; the probe body has exactly five keys, `status`, `tier`, `version`, `protocol_version`, `now` (the message at `:37` names five). `TestHealthProbeTier` (core/health): `h.ProtocolVersion == version.Protocol`.
  - `TestHealthConvertsEveryMember` body gains `"protocol_version":"1.0.0"` and the result's `ProtocolVersion` is `1.0.0`; `TestHealthWithoutProtocolVersion`: a probe body without the member maps to `""` with no error.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test -count=1 ./internal/version/ ; go test -count=1 -run 'TestDiscoverAgreesWithDocument|TestHealthProbeTier|TestCapabilitiesComputed' ./internal/server/ ; go test -count=1 -run 'TestHealth' ./internal/bus/remote/ ./internal/core/health/`
Expected: FAIL (`Protocol` undefined; `gen.Discovery` and `gen.Health` lack `ProtocolVersion`; `info.version` is `2.0.0-dev`).

- [ ] **Step 3: Edit the document and generate.** Edit `api/openapi.yaml` as listed, `make generate`. Then `rg -n '2\.0\.0-dev' --glob '!docs/superpowers/**' --glob '!internal/gen/**' .` shows only build-version uses (`internal/version/version.go:6`, `scripts/m9-exit-checks.sh:385`, `docs/ops/upgrade.md:5`, the `version` values of the `docs/ops/health.md` examples, the historical sentences of ADR 0001 `:369` and ADR 0011 item 15); none of them is the protocol version, and none is edited.

- [ ] **Step 4: Implement** the constant and the server, health, remote and docs edits listed above.

- [ ] **Step 5: Run**

Run: `make generate-check && make lint && make test && make test-conformance && bash scripts/m4-exit-checks.sh --deps-only && bash scripts/m5-exit-checks.sh --deps-only && bash scripts/m6-exit-checks.sh --deps-only && bash scripts/m7-exit-checks.sh --deps-only && bash scripts/m8-exit-checks.sh --deps-only && make verify`
Expected: PASS; `TestCapabilitiesComputed` unchanged (no capability string added); `deps_ok`; `make verify` exits 0.

- [ ] **Step 6: Commit**

```bash
git add internal/version/version.go internal/version/version_test.go api/openapi.yaml internal/gen internal/server/discovery.go internal/server/discovery_test.go internal/server/convert.go internal/server/health_test.go internal/bus/health.go internal/core/health/health.go internal/core/health/health_test.go internal/bus/remote/convert.go internal/bus/remote/health_test.go docs/ops/health.md docs/ops/deploy.md docs/ops/upgrade.md
git commit -m "feat(api): protocol version 1.0.0: info.version, version.Protocol, protocol_version on discover and health (Eric 2026-10-26)

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01DRnnV9QPme9fCDtmKoxFiW"
```

---

### Task 5: MCP and CLI: `capabilities`, `protocol_version`, the `channels delete` prompt, the skill text

**Files:**
- Modify: `internal/mcpserver/server.go` (`channelOut` `:164` and `channelInfoOut` `:169` gain `Capabilities []string \`json:"capabilities"\`` (`[]string{}` when empty); the `create_channel` answer `:463` and `list_channels` `:475` fill it; the `list_channels` description `:466` becomes "Agentbus2: list the channels you hold an access entry on, with their kind and capabilities (bridged: people follow the channel from Slack and replies land in its thread; ignore capability values you do not know). A server administrator sees every channel."; `healthOut` `:231` gains `ProtocolVersion string \`json:"protocol_version"\`` and `healthToOut` `:296` fills it), `internal/cli/channels.go` (`opChannelOut` `:13` and `opChannelInfoOut` `:19` gain `Capabilities []string \`json:"capabilities"\``, never null, filled at `:64` and `:96`; `runChannelsDelete` `:100-140`: when the list lacks the name and no `-key`, prompt without a kind and let the Bus answer; the doc comment above it; `confirmDelete` prints `Channel %q and everything in it will be deleted.` when the kind is `""`), `internal/cli/status.go:78-84` (a row whose capabilities include `bridged` ends with ` bridged`), `internal/cli/skills/using-agentbus2/SKILL.md` (`:40`: "Use `list_channels` to see the channels you hold an access entry on (their names, kinds and capabilities)"; `:105`: the other agent finds it with `list_channels` once it is named in the ACL; `:116` and `:300` keep `list_channels`, "the channels you can see"; one sentence after `:40`: "A channel whose `capabilities` include `bridged` is followed from Slack: people read it there and their replies arrive in its thread. Ignore capability values you do not know.")
- Test: `internal/mcpserver/server_test.go` (`TestACLToolsRoundTrip` `:621`: `list_channels` entries carry `capabilities` `[]`; a second connection as `sam`, not named in `private`'s ACL, does not list it), `internal/mcpserver/health_test.go` (`TestHealthTool`: `got["protocol_version"] == version.Protocol`), `internal/cli/channels_test.go` (`TestChannelsLifecycle`: the `list` JSON carries `"capabilities":[]`; `:137-139`: `run("y\n", "delete", "nosuch")` now prompts (stdout holds `Channel "nosuch" and everything`), and exits 1 with `not_found` from the Bus; the comment says so), `internal/cli/health_test.go` (`TestHealthPrintsDocument`: `m["protocol_version"] == version.Protocol`), `internal/cli/status_test.go` (new `TestStatusMarksBridged`: through `pat.Core.Store.Tx`, `CreateScope("job1")`, `CreateChannel` `job1-chat` with `Scope: "job1"` and the default ACL, `CreateBridge` + `SetBridgeSlackChannel` + `SetBridgeLive`; the report holds `  job1-chat [ordinary] bridged`)

**Interfaces:**
- Consumes: `bus.Channel.Capabilities`, `Channel.Has`, `bus.ChannelBridged` (Task 3); `bus.Health.ProtocolVersion` (Task 4); `version.Protocol`.
- Produces: JSON member `capabilities` on the MCP `list_channels` and `create_channel` answers and the CLI `channels list` and `channels create` output; `protocol_version` in the MCP `health` tool and `agentbus2 health` output. No new flag, no new tool.

- [ ] **Step 1: Write the failing tests** as listed.
- [ ] **Step 2: Run them to verify they fail**

Run: `go test -count=1 -run 'TestACLToolsRoundTrip|TestHealthTool' ./internal/mcpserver/ ; go test -count=1 -run 'TestChannelsLifecycle|TestHealthPrintsDocument|TestStatusMarksBridged' ./internal/cli/`
Expected: FAIL on the `capabilities` and `protocol_version` members, the delete prompt and the ` bridged` suffix.

- [ ] **Step 3: Implement**, then the word check: `bash scripts/m6-exit-checks.sh --deps-only` and `rg -n 'dm/|-filter|@<|memory/|tasks/|general/' internal/mcpserver/server.go internal/cli/skills/using-agentbus2/SKILL.md` shows no line this task added.

- [ ] **Step 4: Run** `make lint && make test && go test -count=1 -v ./internal/e2e/ > "${TMPDIR:-/tmp}/ab2-e2e.log" 2>&1; bash scripts/m6-exit-checks.sh "${TMPDIR:-/tmp}/ab2-e2e.log"`
Expected: PASS; the e2e counts unchanged (13 steps, 11 cells), the word check and the Milestone 6 import rule pass.

- [ ] **Step 5: Commit**

```bash
git add internal/mcpserver/server.go internal/mcpserver/server_test.go internal/mcpserver/health_test.go internal/cli/channels.go internal/cli/channels_test.go internal/cli/health_test.go internal/cli/status.go internal/cli/status_test.go internal/cli/skills/using-agentbus2/SKILL.md
git commit -m "feat(mcp,cli): channel capabilities and protocol_version in the outputs; the entry-filtered list in the tool and skill text; delete prompts without the kind

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01DRnnV9QPme9fCDtmKoxFiW"
```

---

### Task 6: TUI: the bridged icon through one `channelIcon` helper; the protocol version in the health overlay

**Files** (line numbers at 69828fb; re-read before editing):
- Modify: `internal/tui/icons.go` (`iconBridged` in the rail-icons `var` block `:15-24`: U+1F517 then U+FE0F then a space, written like its neighbors, comment `// link symbol; Emoji_Presentation=Yes, so no appleTerminalIcons variant`; `iconVars["bridged"] = &iconBridged` `:84-89`; `nerdIcons["bridged"] = ""` with `// fa-slack` `:104-118`), `internal/tui/view.go` (new `func channelIcon(c bus.Channel) string`: tasks, memory, then `c.Has(bus.ChannelBridged)` → `iconBridged`, else `iconChat`; new `func (m Model) channelOf(name string) bus.Channel` scanning `m.channels`, defaulting to an ordinary channel with empty `Capabilities`; `channelLabel` `:61-75` uses both; the rail row `:547-552` and the compose prompt `:784-790` use `channelIcon` (the compose prompt keeps its memory `hint`); the rail comment `:543-544` names the administrator, the write-only or admin-only holder and the revocation window instead of Decision 94), `internal/tui/search.go:132-137` (Eric's decision 2026-10-26: a hit's mark is `channelIcon(m.channelOf(hit.Channel))`, the same helper as the rail, header and compose prompt: tasks and memory as today, `iconBridged` for a bridged channel, `iconChat` for any other ordinary channel, which replaces today's blank `"  "`; styled as today, ordinary and bridged hits in `m.chanStyle(c)`), `internal/tui/health.go:212` (after the build version, ` · protocol <h.ProtocolVersion>` when it is not empty), `internal/tui/view_test.go:193` (comment no longer cites Decision 94), `docs/tui.md` (the icon table near `:199-215` gains `bridged` with 🔗 and U+F198; the `icons` section says a channel with the `bridged` capability carries the link icon wherever its kind icon is drawn: rail, header and stream labels, compose prompt, search hits)
- Test: `internal/tui/icons_test.go` (`TestLoadIconsNerdfontWidthParity` `:30` and `TestIconSetsCoverTheSameNames` `:144` pass unchanged; new `TestBridgedIconGlyphs`: the emoji `iconBridged` starts with U+1F517, `nerdIcons["bridged"] == ""`, an `icon_map` `{"bridged": "L"}` overrides it, `LoadIconsFor` with `TERM_PROGRAM=Apple_Terminal` leaves the emoji value as `emojiIcons` has it), `internal/tui/rail_test.go` (new `TestBridgedChannelIcon`: `f.m.setChannels([]bus.Channel{{Name: "job1-chat", Kind: bus.KindOrdinary, Capabilities: []bus.ChannelCapability{bus.ChannelBridged}}, {Name: "odd", Kind: bus.KindOrdinary, Capabilities: []bus.ChannelCapability{"future"}}}, nil, "oldest")`; the rail holds `iconBridged + "job1-chat"` and `iconChat + "odd"`; `channelLabel("job1-chat")` and the compose prompt for it start with `iconBridged`; a search hit on `job1-chat` carries `iconBridged`, a hit on `odd` carries `iconChat`), `internal/tui/health_test.go:20` (`TestHOpensHealthWithLines`: the `server` line reads `"server ok · agentbus2 v" + version.Version + " · protocol " + version.Protocol + " · sqlite schema "`)

**Interfaces:**
- Consumes: `bus.Channel.Capabilities`, `Has`, `ChannelBridged` (Task 3); `bus.Health.ProtocolVersion` (Task 4); `padIcon`, `setIcons`, `emojiIcons`, `nerdIcons`, `LoadIconsFor`, `m.chanStyle`.
- Produces: `channelIcon`, `channelOf`, `iconBridged`; no `Settings` change (`icon_map` accepts any `iconVars` name).

- [ ] **Step 1: Write the failing tests** as listed.
- [ ] **Step 2: Run them to verify they fail**

Run: `go test -count=1 -race -run 'TestBridged|TestHOpensHealthWithLines|TestLoadIcons|TestIconSets' ./internal/tui/`
Expected: FAIL (`iconBridged` undefined; the health line lacks ` · protocol `).

- [ ] **Step 3: Implement**, then `GOOS=windows GOARCH=amd64 go vet ./internal/tui/`.

- [ ] **Step 4: Run** `make lint && make test && go test -count=1 -v ./internal/tui/ ./internal/cli/ > "${TMPDIR:-/tmp}/ab2-m8.log" 2>&1; bash scripts/m8-exit-checks.sh "${TMPDIR:-/tmp}/ab2-m8.log"`
Expected: PASS; `modes=2`, `noinbox=1`, `cli=2`, `words_ok=1`, `deps_ok=1`.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/icons.go internal/tui/icons_test.go internal/tui/view.go internal/tui/view_test.go internal/tui/search.go internal/tui/health.go internal/tui/health_test.go internal/tui/rail_test.go docs/tui.md
git commit -m "feat(tui): the bridged icon (link, fa-slack) through one channelIcon helper; the protocol version in the health overlay

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01DRnnV9QPme9fCDtmKoxFiW"
```

---

### Task 7: Records and the sweep

**Files:**
- Modify:
  - `docs/adr/0011-one-access-check.md`: new section "Eric's decisions of 2026-10-26" transcribing this plan's six decisions, each labeled "Eric's decision 2026-10-26"; item 3's action table (Channel gains `Unsubscribe` and `FilterReceive`; new rows ChannelName and ScopeName with `TakeName`); item 5's `List` row (the stored-row rule; `Unsubscribe` and `FilterReceive` rows: the scope gate alone; `TakeName` rows); item 20 "(review 2) the interim `List` clause" marked superseded by Eric's decision 2026-10-26 (1); the "Context: protocol versioning" heading and text say it is implemented (`info.version` `1.0.0`, `internal/version.Protocol`, `protocol_version` on discover and health); the last Consequences bullet points to this plan.
  - `docs/superpowers/specs/2026-10-25-channel-visibility-design.md`: a status line naming this plan; new section 8 "Eric's decisions of 2026-10-26" (the six decisions, labeled), stating that 3.1 (`Tx.VisibleChannels`), the creator clause of 3.7 and section 4 question 1's "or the implicit creator entry", and section 5 (the discovery capability name, moot under the protocol-versioning decision) are superseded; 3.2's discovery bullet and 3.8's "the discovery capability tells" sentences marked superseded by `protocol_version`.
  - `docs/superpowers/specs/2026-10-25-accesscheck-design.md`: 3.1 Action list gains `Unsubscribe`, `FilterReceive` and `TakeName`; 3.2's `List` row loses "interim" and states the rule, plus `Unsubscribe`, `FilterReceive` and `TakeName` rows; the Context section says implemented by this plan; 3.10 gains one line "Rebuilt as `docs/superpowers/plans/2026-10-26-channel-visibility.md`".
  - `docs/adr/0008-channel-access-control-lists.md`: new section "Channel visibility (Eric, 2026-10-25 and 2026-10-26)": the filtered list (stored rows or the administrator flag; no creator clause), `capabilities` and `bridged`; Decision 94 `:184` and "Decision 94 in the types" `:217` marked "superseded by Eric's decision 2026-10-25 (1) as amended 2026-10-26 (1)"; the Milestone 8 addendum sentence `:276` replaced (an unreadable channel is listed to an administrator and to a write-only or admin-only holder).
  - `docs/adr/0009-scopes-and-slack-bridge.md` Consequences: "`ListChannels` reports a channel whose bridge is `live` with the `bridged` capability; its named identities and the administrators are the only identities that list it."
  - `docs/adr/0001-transport-and-api.md`: new "Protocol versioning addendum (Eric's decision 2026-10-26)": `info.version` `1.0.0` is the first stamped protocol version; major = `/v1`, minor per addition, patch doc-only; `protocol_version` on discover and health; no new capability strings (Decision 43's "additions get a capability" superseded for additions); the sentence at `:369` keeps its history and gains "(superseded: `info.version` is `1.0.0` from 2026-10-26)".
  - `docs/superpowers/specs/2026-10-19-agentbus2-design.md`: section 3.1 Decision 43 paragraph (`:201-207`), section 3.2 channels row, section 4.6.1 Decision 94 paragraph and its "list channels" row, Question 30: each gains a one-line "Superseded by ..." marker naming the visibility spec section 8 or ADR 0001's addendum; the text stays.
  - `docs/superpowers/plans/2026-10-21-milestone-8-tui.md:36`: a leading "(superseded 2026-10-25)" marker.
  - `docs/ops/slack-bridge.md`: one sentence after the named-only ACL item: who lists a bridged channel, the `bridged` capability and the TUI's link icon.
  - `CLAUDE.md`: the `internal/core/access` bullet names the actions (`List`: a stored row or the administrator flag; `Unsubscribe` and `FilterReceive`: the scope gate alone; `TakeName`: the scope-prefix name rules); the `GET /v1/health` bullet `:45` adds that discover and health carry `protocol_version` (`internal/version.Protocol`, equal to `api/openapi.yaml` `info.version`) and that additions bump the protocol minor instead of adding a capability; the `api/openapi.yaml` bullet `:48` adds "bump `info.version` and `version.Protocol` together: minor per addition, patch for a documentation-only change".
- Verify: `git log --stat <Task 1 commit>^..HEAD` covers every file of the visibility spec section 7 except those it names for `VisibleChannels` (ADR 0004 gets no addendum: no Store change); `rg -n 'Decision 94' docs internal CLAUDE.md` shows only historical statements carrying a superseded marker; `rg -n 'channel_capabilities' docs internal CLAUDE.md` shows only superseded mentions; `rg -n 'interim' internal/core docs/adr/0011-one-access-check.md` shows no live interim `List` clause.

- [ ] **Step 1: Edit the records** as listed; check each factual sentence against the code committed by Tasks 1 to 6 (Definition of Done E), not against this plan.

- [ ] **Step 2: Run the full gate and the exit scripts**

Run: `make verify && go test -count=1 -p 1 -v ./internal/conformance/bus/ > "${TMPDIR:-/tmp}/ab2-m3.log" 2>&1 && bash scripts/m3-exit-counts.sh "${TMPDIR:-/tmp}/ab2-m3.log" && go test -count=1 -v ./internal/e2e/ > "${TMPDIR:-/tmp}/ab2-e2e.log" 2>&1 && bash scripts/m6-exit-checks.sh "${TMPDIR:-/tmp}/ab2-e2e.log"`
Expected: PASS; the m3 script exits 0 with `fail=0`, `matrix_sqlite=52`, `matrix_postgres=52`, `search_agree=2`, `concurrent_send=2`; the e2e counts unchanged.

- [ ] **Step 3: Commit**

```bash
git add docs/adr/0011-one-access-check.md docs/adr/0008-channel-access-control-lists.md docs/adr/0009-scopes-and-slack-bridge.md docs/adr/0001-transport-and-api.md docs/superpowers/specs/2026-10-25-channel-visibility-design.md docs/superpowers/specs/2026-10-25-accesscheck-design.md docs/superpowers/specs/2026-10-19-agentbus2-design.md docs/superpowers/plans/2026-10-21-milestone-8-tui.md docs/ops/slack-bridge.md CLAUDE.md
git commit -m "docs(adr,spec): record Eric's channel visibility and protocol-versioning decisions of 2026-10-26; Decision 94 and the interim List clause superseded

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01DRnnV9QPme9fCDtmKoxFiW"
```

- [ ] **Step 4: Report** to Eric: the `make verify` output, the m3 and m6 script lines, and the push request (`Approve: push main to origin`); `ci.yml` and `windows.yml` must then be green on the pushed commit.
