# Agent Status and History Implementation Plan (issue #10; Eric's decisions of 2026-10-27)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** An agent sets one status line (a tag from a fixed set of seven, a one-line text, an optional problem) through MCP `set_status`, the CLI and the Bus; each set is a new revision of a per-identity history pruned to N at write; every visible agent's current status (with `offline` derived from "no live session") and one agent's history are readable on the wire, in `register.others`/`discover`, in the CLI and on a TUI agents board.

**Architecture:** A new `agent_statuses` table (both engines, edited into `0001_initial.sql`) behind four `store.Tx` methods; one new `decide` clause (`Identity`/`SetStatus`: own identity, agent kind); a new core aggregate `internal/core/statuses` (set, list, history) over the Store, the access check and `sessions.Service.RequireAs`; three protected operations and an optional `Identity.status` on the wire; three methods on `bus.Sessions`; one MCP tool, one CLI command, protocol text and a TUI board. Nothing runs in maintenance and nothing publishes a notifier hint (pull only).

**Tech Stack:** Go, modernc SQLite and pgx Postgres over `sqlstore`, kin-openapi + oapi-codegen (`make generate`), testcontainers for the Postgres suites, the MCP Go SDK, Bubble Tea v2 / lipgloss v2.

**Spec:** `docs/superpowers/specs/2026-10-27-agent-status-design.md` (all 15 decisions of section 5 are Eric's, 2026-10-27; decision 5 is Layout 2 with the status-bar attention count and an icon-only tag on each session row), read with ADR 0011 (`docs/adr/0011-one-access-check.md`), `docs/tui.md` and Decision 18 (`docs/superpowers/specs/2026-10-21-gate-0-decisions.md:232-240`).

**Eric's decisions this plan implements** (brief section 5; recorded verbatim by Task 9 in ADR 0014 and by Task 10 in the v1 ADR): 1 a dedicated `agent_statuses` table with the revision pattern; 2 keyed per identity, recording the setting session and display name; 3 `offline` derived at read, never stored, never settable; 4 a new `Identity/SetStatus` action (own identity, agents only) plus `Session/Use`, reads ride `Identity/See`; 5 TUI Layout 2 (agents board) plus the attention count and session-row icons; 6 the tag set `idle`, `planning`, `implementing`, `reviewing`, `waiting`, `blocked`, `needs-human` (settable) and `offline` (bus only); 7 three operations plus `Identity.status`; 8 prune to N at write, N = `status_history_count`, default 20, range 1 to 1000; 9 one MCP tool `set_status` (26 tools); 10 CLI `agent-status set|list|history`; 11 protocol text sets `idle` before `agentbus2 park`; 12 the Stop hook is unchanged; 13 pull only (push later via #13); 14 every set adds a revision, an identical one too; 15 `problem` is optional for every tag. Protocol stays `1.0.0` (Eric, 2026-10-27).

## Global Constraints

- Done gate per task: `make verify` (needs Docker; Postgres from testcontainers unless `AGENTBUS2_TEST_POSTGRES_DSN` names a reachable server; a run that skips the Postgres suites is not a pass) and its output in the report. Every task also runs `make lint` and `make test`; Store and Bus suite tasks run `make test-conformance` (both engines, both transports). Work is done only when `ci.yml` and `windows.yml` are green on the pushed commit, `release-check` included; no push without Eric.
- Pre-release rule (Eric, 2026-10-25): no new migration; `agent_statuses` goes into `internal/store/sqlite/schema/0001_initial.sql` and `internal/store/postgres/schema/0001_initial.sql` in place; schema version stays 1; no compatibility layer, shim, converter or deprecation path.
- Protocol (Eric, 2026-10-27): `api/openapi.yaml` `info.version` and `internal/version.Protocol` stay `1.0.0`. After the yaml change run `make generate`; never hand-edit `internal/gen` or `internal/gen/spec`; `make generate-check` is clean. Operation totals become 47 protected, 8 open, 55 in all; the MCP tool count becomes 26. Every pin of those numbers changes in the task that changes the count (Task 4 for operations, Task 6 for tools).
- Access: the only new rule is `decide`'s `SetStatus` clause (Task 2). `if !p.Admin {` stays exactly once in `decide.go` (selftest block 15). `TestAccessRuleSites` (`internal/core/access/sites_test.go`) needs no new allowlist entry: no new code outside `internal/core/access` reads `Admin`, `Creator`, `Owner`, `Scope`, `store.IdentityHuman`/`IdentityAgent` or `KindMemory` (copying an identity's `Kind` is not gated; never copy `Admin` into a status result). A failure there is fixed at the site, not in the list. The new clause gets mutations in a new `scripts/check-gates-selftest.sh` block 32 (Tasks 2 and 5; block 29 is the `TestAccessRuleSites` probe set and `access` is exempt from it). The selftest tests `HEAD`: commit first, then run `make check-gates-selftest`.
- Import rules: any task that adds an import runs `bash scripts/m4-exit-checks.sh --deps-only && bash scripts/m5-exit-checks.sh --deps-only && bash scripts/m6-exit-checks.sh --deps-only && bash scripts/m7-exit-checks.sh --deps-only && bash scripts/m8-exit-checks.sh --deps-only`. New edges: `internal/core/statuses` imports `internal/bus`, `internal/core`, `internal/core/access`, `internal/core/rules`, `internal/core/sessions`, `internal/store` (no `internal/gen`, no `internal/server`); `internal/core/sessions` does not import `statuses` (no cycle: `sessions.Identities` reads `tx.LatestAgentStatuses` itself); `internal/server` and `internal/bus/local` import `internal/core/statuses`; `internal/tui` imports nothing new (the m8 allowlist stays `internal/bus` and `internal/version`); `internal/mcpserver` and `internal/cli` reach statuses through `internal/bus` only.
- Word checks: `scripts/m6-exit-checks.sh` scans `SKILL.md`, `internal/mcpserver/inputs.go`, `server.go`, `internal/cli/stophook.go`, `subagenthook.go` for `dm/|-filter|@<|memory/|tasks/|general/`; `TestToolListIsExact` rejects `dm/`, `persistent`, `tasks/` in descriptions; `scripts/m8-exit-checks.sh` scans `internal/tui` non-test files. New agent-facing text avoids all of them. New CLI exit-code tests live in `internal/cli`, never `internal/e2e` (m6 pins 13 steps and 11 cells).
- Pinned counts that stay: `TestACLMatrix` 52 leaves per engine (this plan adds no matrix row), `TestAuthEndToEnd` 11 leaves per engine, e2e 13 steps and 11 cells, `TestBothModes` 2, `TestNoInboxView` 1, `TestRunTUI{Local,Server}Profile` 2, `TestCapabilitiesComputed` (no capability string is added).
- Decision 26 in every write: `Store.Tx` may run `fn` more than once, so `fn` resets its outputs first, `Limits.Check` runs inside `fn`, `Limits.Charge` runs only after a successful commit and only when not replayed; nothing else happens after commit (no notifier publish, no embedding kick).
- Definition of Done D for the one write (`Set`): a replayed `idempotency_key` returns the stored result and adds no revision; two unkeyed calls add two revisions in commit order, the last wins; the insert and the prune share one transaction, so a failure between them rolls back whole; a second prune deletes nothing. Each is a test (Tasks 1, 3, 5).
- Definition of Done B: an unknown tag, a missing tag, `offline`, an empty text and a multi-line text or problem are each rejected `validation` with a test (Task 3 unit, Task 5 over both transports); the board and the remote client accept an unknown tag from a server without failing (Settled open note 1).
- Docs move with the code: each task updates the docs that assert the behavior it changes (named in its Files). Task 9 writes ADR 0014 and sweeps the records. American English. Check every factual sentence against the committed code.
- Plan order (2026-10-27): this plan runs after the signing-keys, postgres-budget and write-inspection plans. The postgres-budget plan rewrites `internal/core/maintenance/budget.go` (including the capacity notice text); Task 3 merges the agent-status wording into the text that plan committed, never overwrites it. ADR 0014 and selftest block 32 are this plan's assigned numbers.
- Each task: failing tests first, exact `git add` list (never `-A`), one commit on `main` ending with:
  ```
  Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_011fnCFu154ZJG5c5ro7rrqs
  ```
- Definition of Done F: anything beyond a one-liner goes in a script with `set -euo pipefail`; logs go to `${TMPDIR:-/tmp}`.

## Settled open notes

The brief leaves no decision open; these are the details it leaves to the implementer, settled here. Each carries `// open note: agent-status N` (or `-- open note: agent-status N` in SQL) at the named site so Eric can reverse it with a findable edit (`rg -n 'open note: agent-status' .`).

1. **Tag on the wire is a plain string, no enum, request or response.** The core is the one validator, so the rejection text is the same on every transport; the remote client and the TUI pass an unknown tag through (TUI: gear icon fallback). Rationale: the capability principle already used for channel `capabilities`; an added tag after the release is not a decode failure for old clients. Marker: `internal/bus/remote/agentstatus_test.go` `TestRemoteAgentStatusUnknownTagPassesThrough`.
2. **`last_tag`** is set on `listAgentStatuses` entries only, always (equal to `tag` when `live`); `tag` there reads `offline` when not live. History and `Identity.status` carry the stored tag and no `last_tag` (an identity in `listIdentities` is live). Rationale: one rule a client can read without comparing fields. Marker: `internal/core/statuses/statuses_test.go` `TestListDerivesOffline`.
3. **Emoji:** `planning` U+1F4DD (memo) and `implementing` U+1F528 (hammer) replace the brief's U+1F5FA and U+1F6E0, which are `Emoji_Presentation=No` like the gear and would need Terminal.app variants; the other five are as proposed (U+1F50D, U+23F3, U+26D4, U+1F64B, U+26AB) and `idle` reuses the existing icon. Nerd Font as proposed. Rationale: the brief's own "prefer Emoji_Presentation=Yes". Marker: `internal/tui/icons.go` status icon var block.
4. **Board key:** `enter` on the `sessions` heading opens the board (the brief's primary wording); `←`/`→` still collapse and expand that heading; `w` stays free. Marker: `internal/tui/agents_test.go` `TestAgentsBoardOpensFromSessionsHeading`.
5. **Session-row icon:** on a live agent row whose identity has a status, the status icon replaces the gear; no status keeps the gear; the TUI's own row and idle rows are unchanged. Rationale: icon-only keeps the rail width. Marker: `TestSessionRowStatusIcon`.
6. **Live agents with no status** appear on the board in the live group, ordered by name among the live agents that have a status, with `-` for tag, since and text. Rationale: "every agent" includes the ones that never reported. Marker: `TestAgentsBoardOrder`.
7. **Attention count** counts live entries with tag `needs-human` or `blocked` only (an offline agent's stored `needs-human` reads `offline` and does not count); it shows as `N need you` (warn color) in the status bar and as `(N need you)` after the `sessions` heading (collapsed: `(count, N need you)`). Marker: `TestAttentionCountLiveOnly`.
8. **Rate limit:** a set is checked and charged as one operation of `len(tag)+len(text)+len(problem)` bytes against `send_messages_per_second`/`send_kib_per_second`, text `send_messages_per_second rate limit exceeded for %s` (the `Edit` text). Marker: `internal/core/statuses/statuses.go` at the `Limits.Check` call.
9. **Default session:** MCP `set_status` without `session` uses this MCP server's own primary session (`ownPrimary`), else `validation` `set_status needs a session: call register first, or pass session`; the call goes through `withSession` (lapsed resume). CLI `agent-status set` without `-session` uses `locateSession` + `bindProfile` (the `park` pattern), exit 2 with the locate error when none; never a throwaway session (its display name would be wrong). Markers: `internal/mcpserver/agentstatus_test.go` `TestSetStatusDefaultsToOwnSession`, `internal/cli/agentstatus_test.go` `TestAgentStatusSetLocatesSession`.
10. **Denials of `SetStatus`:** out of scope `not_found` `identity %q is not registered` (Reason `out_of_scope`, the `See` text); another identity `forbidden` `%s may set only its own status` (Reason `not_owner`); a non-agent `forbidden` `%s is not an agent; only an agent sets a status` (new Reason `not_agent`); the kind test fails closed (`p.Kind != store.IdentityAgent`). Marker: `internal/core/access/decide_test.go` `TestDecideSetStatus`.
11. **Package shape:** the aggregate is `internal/core/statuses` (`Service{*core.Core; Sessions sessions.Service}`, the `delivery` shape); `sessions.Service.Identities` reads `tx.LatestAgentStatuses` and converts with `core.AgentStatusToBus`. Marker: `internal/core/statuses/service.go` package comment.
12. **Schema:** the primary key `(identity, revision)` is the identity index (no second index); no `CHECK` on `tag` (the Store applies no rule; the core validates). Marker: SQL comment above the table in both `0001_initial.sql` files.
13. **History read** takes no `limit`: it returns the stored rows, oldest first, capped at the current N. Marker: `internal/server/agentstatus_test.go` `TestAgentStatusHistoryWire`.
14. **Board history refresh:** while an agent's history is open the TUI refetches it once per 5-second status round (one call; none otherwise). Marker: `TestAgentsBoardHistoryOldestFirst`.
15. **`Status` on `IdentityInfo` only**, not on `SessionInfo` (brief 4.6 names both): the status is keyed per identity (decision 2), so a per-session copy would repeat it on every session row. Marker: `internal/bus/sessions.go` at the `IdentityInfo.Status` field.

## Review Focus

1. An agent that went offline while `needs-human` must not inflate the attention count, and must still show on the board under `offline` with its last text and age: `TestAttentionCountLiveOnly` (Task 8), `TestAgentStatusOfflineAfterLapse` (Task 5).
2. Text and problem at the boundary: exactly 200 runes after trimming passes, 201 fails, a `\r` or `\n` fails, whitespace-only text is "text is required", whitespace-only problem is stored as empty: `TestSetValidation` (Task 3).
3. A scoped agent's list omits out-of-scope agents but shows administrators' statuses; its history read of an out-of-scope agent is `not_found` with the `See` text, of an administrator allowed: `TestAgentStatusScope` (Task 5).
4. Lowering N: rows above N stay until that identity's next set, reads cap at N meanwhile, the next set prunes to N: `TestHistoryCapsAfterLoweringN` (Task 3).
5. A hinted session's set is accepted and records `display_name` `<identity>/<hint>`; a later set from the primary session replaces it (one line per identity) and both stay in history: `TestAgentStatusHintedSession` (Task 5).

---

## Tasks and waves

| Task | Depends on | Wave |
| --- | --- | --- |
| 1 Store: `agent_statuses`, the contract, both engines | none | 1 |
| 2 Access: `Identity/SetStatus` in `decide`, selftest block 32 | none | 1 |
| 3 Core: `internal/core/statuses`, rules, config, `Identities` status | 1, 2 | 2 |
| 4 Seam: Bus methods, wire (three operations, `Identity.status`), server, local, remote | 3 | 3 |
| 5 Bus suite on both engines and transports; block 32 core mutation | 4 | 4 |
| 6 MCP `set_status` and statuses in `others`/`discover`; CLI `agent-status` | 4 | 4 |
| 7 Protocol text: RegisterSteps, skill, init prompts, SubagentStart hook | 6 | 5 |
| 8 TUI: agents board, history, attention count, session-row icons | 4 | 4 |
| 9 ADR 0014 and the records sweep; final gates | 5, 6, 7, 8 | 6 |
| 10 v1 ADR in agentbus (ericfitz/agentbus#43) | 9 | 7 |

Waves: 1 and 2 in parallel (both touch `CLAUDE.md`, different paragraphs: integrate by cherry-pick); 3; 4 (the seam: the Bus interface, the yaml and both Bus implementations change together, so the tree compiles at every commit); then 5, 6 and 8 in parallel (disjoint files except `CLAUDE.md`); 7 after 6 (both edit `internal/mcpserver/server.go`); 9; 10 runs in the v1 repository and is outward-facing (see Task 10).

---

### Task 1: Store: the `agent_statuses` table and its contract

**Files:**
- Create: `internal/store/agentstatuses.go` (type and interface), `internal/store/sqlstore/agentstatuses.go`
- Modify: `internal/store/store.go:49-65` (`Tx` embeds `AgentStatuses`), `internal/store/sqlite/schema/0001_initial.sql` and `internal/store/postgres/schema/0001_initial.sql` (the table after `session_cursors`; `INTEGER` on SQLite, `BIGINT` on Postgres for `revision` and `set_at`), `internal/store/sqlstore/meta.go:17-20` (`resetDataTables` gains `agent_statuses`; its comment), `internal/store/meta.go:18-29` (the `ResetData` doc lists it; `ResetRegistry` doc says the rows go with their identity by cascade), `internal/store/scopes.go:60-65` (`RetireIdentity` doc: statuses cascade), `CLAUDE.md` (the schema-version-1 sentence lists `agent_statuses`)
- Test: `internal/conformance/store/agentstatuses_test.go` (new), `internal/conformance/store/reset_test.go:18-22` (`dataTables` gains `agent_statuses`; `resetFixtures` writes one status row)

**Interfaces:**
- Produces:
  ```go
  // store
  type AgentStatus struct {
      Identity                                      string
      Revision                                      int64 // assigned by AppendAgentStatus
      Tag, Text, Problem, SessionID, DisplayName    string
      SetAt                                         int64 // unix ms
  }
  type AgentStatuses interface {
      // AppendAgentStatus inserts the identity's next revision (max+1, 1 when none; s.Revision is ignored)
      // and returns it. ErrNotFound: unknown identity (foreign key); ErrInvalid: TextRule on a text field, named.
      // Write transactions only (ErrReadOnly in Read).
      AppendAgentStatus(ctx context.Context, s AgentStatus) (int64, error)
      // PruneAgentStatuses deletes the identity's rows with revision <= max-keep and returns how many.
      // ErrInvalid: keep < 1. Write transactions only. A second call deletes nothing.
      PruneAgentStatuses(ctx context.Context, identity string, keep int) (int, error)
      // LatestAgentStatuses: each identity's highest revision, by identity in byte order (sortByBytes).
      LatestAgentStatuses(ctx context.Context) ([]AgentStatus, error)
      // AgentStatusHistory: the identity's newest limit rows, returned oldest first; [] for an unknown
      // identity; ErrInvalid: limit <= 0 (checkLimit).
      AgentStatusHistory(ctx context.Context, identity string, limit int) ([]AgentStatus, error)
  }
  ```
  Table columns: `identity` (FK `identities(name) ON DELETE CASCADE`), `revision`, `tag`, `text`, `problem` (default `''`), `session_id`, `display_name` (plain text, no FK), `set_at`; `PRIMARY KEY (identity, revision)`.

- [ ] **Step 1: Write the failing Store suite tests** (`dbtest.ForEachEngine`, helpers `openStore`, `mustTx`, `mustRead`, `mustIdentity`, `rawDB`, `scalar`):
  - `TestAgentStatusAppendAndRead`: three appends for `pat` return 1, 2, 3; one for `kim` returns 1; `AgentStatusHistory(pat, 10)` is revisions `[1 2 3]` with every field round-tripped (`Problem: ""` and non-empty); `LatestAgentStatuses` for identities `pat`, `kim`, `alpha_1`, `alpha-2` (one row each, `pat` three) is in byte order `alpha-2, alpha_1, kim, pat` with `pat` at revision 3.
  - `TestAgentStatusHistoryLimit`: five rows; `limit 2` gives `[4 5]`; `limit 0` and `-1` are `ErrInvalid`; unknown identity gives `[]` (not nil, `len 0`).
  - `TestAgentStatusPrune`: five rows; `keep 5` deletes 0, `keep 4` deletes 1 (revisions `[2..5]` left), `keep 1` deletes 3 (`[5]`), a second `keep 1` deletes 0; `keep 0` is `ErrInvalid`; the next append after pruning is revision 6.
  - `TestAgentStatusAppendRejects`: unknown identity `ErrNotFound`; a NUL in `Text` or invalid UTF-8 in `Problem` is `ErrInvalid` naming the field; inside `s.Read` the append is `ErrReadOnly`.
  - `TestAgentStatusCascade`: rows for `job1-coder` (a scoped identity, `mustScope`) vanish after `RetireIdentity` (raw count 0, modeled on `scopes_test.go:271-295`); `ResetRegistry("")` removes every row; `ResetData` removes every row and keeps identities.
  - `TestAgentStatusConcurrentAppends`: two goroutines append 10 rows each for `pat` through `s.Tx`; no error; the revisions are exactly `1..20`. If Postgres surfaces `23505` instead of a retried `40001`, stop and report it as a design change (the brief assumes serialization retry, `sqlstore/store.go:50-93`).
  - `reset_test.go`: `dataTables` and `resetFixtures` gain the table, so `TestResetData*` prove it is emptied.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test -count=1 -p 1 -run 'TestAgentStatus|TestReset' ./internal/conformance/store/`
Expected: compile failure (`AppendAgentStatus` undefined).

- [ ] **Step 3: Implement** the type, the interface (embedded in `Tx`), the two schema edits (comment `-- open note: agent-status 12` above each table) and `sqlstore/agentstatuses.go`: `t.writable()` first on both writes; `store.TextRule` on every text field before the query; revision by `SELECT COALESCE(MAX(revision), 0) + 1 ... WHERE identity = ?` then `INSERT`; `isForeignKey` maps to `ErrNotFound`; latest by a `revision = (SELECT MAX ...)` subquery, sorted with `sortByBytes`; history by `ORDER BY revision DESC LIMIT ?` reversed in Go. Engine-neutral SQL only (no `ORDER BY` on a text column).

- [ ] **Step 4: Run**

Run: `make lint && make test && make test-conformance && bash scripts/m7-exit-checks.sh --deps-only && make verify`
Expected: PASS on both engines; `deps_ok`; `make verify` exits 0.

- [ ] **Step 5: Commit**

```bash
git add internal/store/agentstatuses.go internal/store/sqlstore/agentstatuses.go internal/store/store.go internal/store/sqlite/schema/0001_initial.sql internal/store/postgres/schema/0001_initial.sql internal/store/sqlstore/meta.go internal/store/meta.go internal/store/scopes.go internal/conformance/store/agentstatuses_test.go internal/conformance/store/reset_test.go CLAUDE.md
git commit -m "feat(store): agent_statuses table with append, prune, latest and history (issue #10)"
```

---

### Task 2: Access: the `Identity/SetStatus` action

**Files:**
- Modify: `internal/core/access/objects.go:8-23` (`SetStatus Action = "set_status" // identity: own identity, agents only (Eric 2026-10-27, agent status decision 4)`), `internal/core/access/decide.go` (`ReasonNotAgent = "not_agent"` in the Reason block `:15-27`; the `identityFacts` case `:179-185` becomes a switch over `See` and `SetStatus`), `scripts/check-gates-selftest.sh` (block 32 after block 29 `:834-907`, its helper, and `block_agent_status_rules_fire` in the run list `:909-939`), `docs/adr/0011-one-access-check.md:22-23` and `:42-43` (the `Identity` row gains `SetStatus`; its rule "the scope gate, then the principal is the identity and an agent"; a line that ADR 0014 adds it), `CLAUDE.md` (the access paragraph names `Identity/SetStatus`)
- Test: `internal/core/access/decide_test.go`, `internal/conformance/access/checker_test.go:141` (two `check` rows after `identity scope`)

**Interfaces:**
- Consumes: `identityFacts{store.Identity}`, `inScope`, `deny`, `undefined`, the fixtures `pat`, `root`, `hal`, `coder` (`decide_test.go:27-32`).
- Produces: `access.SetStatus`, `access.ReasonNotAgent`. Rule, in this order: (1) `!inScope(p, f)`: `not_found`, `out_of_scope`, `identity %q is not registered`; (2) `if f.Name != p.Name {`: `forbidden`, `not_owner`, `%s may set only its own status`; (3) `if p.Kind != store.IdentityAgent {`: `forbidden`, `not_agent`, `%s is not an agent; only an agent sets a status`; else allowed. Both anchor lines appear exactly once in `decide.go`. Callers: `access.Identity{Name: p.Name}`.

- [ ] **Step 1: Write the failing tests** in `decide_test.go`:
  - `TestDecideSetStatus` (table, `t.Run(name)`, exact `Code`, `Reason`, `Text`; `// open note: agent-status 10`): `own agent allowed` (`pat`, `identityFacts{store.Identity{Name: "pat", Kind: agent}}`); `scoped own allowed` (`coder` on its own row, scope `job1`); `another identity refused` (`pat` on `kim`) `forbidden`/`not_owner`/`pat may set only its own status`; `human refused` (`hal` on its own human row) `forbidden`/`not_agent`/`hal is not an agent; only an agent sets a status`; `out of scope refused` (`coder` on `kim`, unscoped agent) `not_found`/`out_of_scope`/`identity "kim" is not registered`; `admin on another refused` (`root` on `pat`) `not_owner`; `unknown kind fails closed` (`Principal{Name: "x", Kind: "robot"}` on its own row) `not_agent`.
  - `TestDecideSessionAndIdentity` (`:151`): `See` rows unchanged and still pass.
  - `TestAnonymousDeniedEverything` (`:250`): `all` gains `SetStatus`.
  - `TestUndefinedPairIsInternal` (`:269`): `decide(root, chFacts("", store.KindOrdinary), SetStatus)` and `decide(root, sessionFacts{}, SetStatus)` are `internal`; `decide(pat, identityFacts{...}, Use)` stays `internal`.
  - `checker_test.go` (both engines, through `Checker.Check`, so the facts load): `check("set status own", "pat", access.Identity{Name: "pat"}, access.SetStatus, nil)` and `check("set status other", "pat", access.Identity{Name: "kim"}, access.SetStatus, &want{bus.CodeForbidden, "pat may set only its own status", false})`.

- [ ] **Step 2: Run to verify failure**

Run: `go test -count=1 -run 'TestDecideSetStatus|TestAnonymousDeniedEverything|TestUndefinedPairIsInternal' ./internal/core/access/`
Expected: compile failure (`SetStatus` undefined).

- [ ] **Step 3: Implement** the constant, the Reason and the clause. Then block 32 in `check-gates-selftest.sh`: a helper `mutate_unit_block LABEL FILE FROM TO PKG RUN WANT` on the `mutate_auth_block` pattern (`:430-448`: anchor exactly once, mutate, require the change, run `go test -count=1 -p 1 -v -run "$RUN" "$PKG"`, require `--- FAIL: $WANT`, `finish`, `revert`), and `block_agent_status_rules_fire` with two mutations, both Docker-free:
  - `status-agent-clause-ignored`: `internal/core/access/decide.go` `if p.Kind != store.IdentityAgent {` → `if false {`, run `^TestDecideSetStatus$` in `./internal/core/access/`, want `TestDecideSetStatus/human_refused`.
  - `status-owner-clause-ignored`: `if f.Name != p.Name {` → `if false {`, want `TestDecideSetStatus/another_identity_refused`.
  Header comment: "30. agent status: the SetStatus clauses of decide (Task 2) and the core's call of it (Task 5)".

- [ ] **Step 4: Run**

Run: `make lint && make test && go test -count=1 -run TestAccessRuleSites -v ./internal/core/access/ && make verify`, then commit (Step 5) and run `make check-gates-selftest`.
Expected: PASS; `TestAccessRuleSites` unchanged and green; selftest ends `check-gates-selftest: OK` with both block 32 mutations failing their named subtests. If the selftest fails, fix and amend before reporting.

- [ ] **Step 5: Commit**

```bash
git add internal/core/access/objects.go internal/core/access/decide.go internal/core/access/decide_test.go internal/conformance/access/checker_test.go scripts/check-gates-selftest.sh docs/adr/0011-one-access-check.md CLAUDE.md
git commit -m "feat(access): Identity/SetStatus, own identity and agents only (Eric 2026-10-27, issue #10)"
```

---

### Task 3: Core: the `statuses` aggregate, the tag rule, `status_history_count`, statuses in `Identities`

**Files:**
- Create: `internal/bus/agentstatus.go` (domain types only; no interface change), `internal/core/statuses/service.go`, `internal/core/statuses/statuses.go`, `internal/core/rules/status.go`
- Modify: `internal/core/rules/subject.go` (`NormalizeSubject` delegates to `NormalizeLine("subject", s)`, texts byte-identical), `internal/core/convert.go` (`AgentStatusToBus`), `internal/core/config.go` (`StatusHistoryCount int`; default 20 in `Defaults` `:75-100`; bound `{"status_history_count", c.StatusHistoryCount, 1, 1000}` in `Validate` `:113-135`), `internal/server/config/config.go` (`StatusHistoryCount int json:"status_history_count"` after `ScopeIdleHours` `:62`; `Default` `:112`; the core mapping `:165`), `docs/ops/configuration.md` (a row after `scope_idle_hours` `:75`: `` | `status_history_count` | `AGENTBUS2_SERVER_STATUS_HISTORY_COUNT` | int | `20` | 1 to 1000. How many statuses each agent keeps, its current one included; older ones are deleted at its next status. | ``), `internal/core/sessions/identities.go:14-49` (fill `Status` from one `tx.LatestAgentStatuses` call mapped by identity), `internal/bus/sessions.go:129-135` (`IdentityInfo` gains `Status *AgentStatus`), `internal/core/maintenance/budget.go:92-94` (`capacityText`: "filled by live memories, protected tombstones and agent status history, which are never evicted automatically"), `CLAUDE.md` (the `internal/core` aggregate list gains `statuses`)
- Test: `internal/core/statuses/statuses_test.go` (new; SQLite temp store and settable clock, the `sessions/hinted_test.go:19-35` pattern), `internal/core/rules/status_test.go` (new), `internal/core/rules/subject_test.go` (if present; unchanged texts), `internal/core/config_test.go:49-50` (bound row), `internal/server/config/config_test.go` (default and bound rows, `:41`, `:116-118` pattern), `internal/server/config/docs_test.go` (unchanged; must pass with the new row), `internal/core/maintenance` test that pins `capacityText`, if any (`rg -n 'capacity exhausted' internal`)

**Interfaces:**
- Consumes (Task 1): `store.AgentStatus`, `tx.AppendAgentStatus`, `tx.PruneAgentStatuses`, `tx.LatestAgentStatuses`, `tx.AgentStatusHistory`. (Task 2): `access.SetStatus`, `access.Identity`. Existing: `access.Resolve`, `Checker.Require`, `Checker.Identities`, `sessions.Service.RequireAs(ctx, tx, p, sessionID) (store.Session, error)`, `Core.WithReceipt`, `Core.LiveSessions`, `Core.Limits.Check/Charge`, `Core.NowMs`, `core.E`, `core.FromStore`.
- Produces:
  ```go
  // bus (internal/bus/agentstatus.go)
  type StatusTag string
  const (
      StatusIdle StatusTag = "idle"; StatusPlanning StatusTag = "planning"; StatusImplementing StatusTag = "implementing"
      StatusReviewing StatusTag = "reviewing"; StatusWaiting StatusTag = "waiting"; StatusBlocked StatusTag = "blocked"
      StatusNeedsHuman StatusTag = "needs-human"; StatusOffline StatusTag = "offline" // offline: bus only
  )
  func SettableStatusTags() []StatusTag // the seven, in the order above
  type AgentStatus struct {
      Identity    string
      Revision    int64
      Tag         StatusTag // on AgentStatuses entries: offline when the entry is not Live
      LastTag     StatusTag // AgentStatuses entries only: the stored tag; "" elsewhere
      Text, Problem, Session, DisplayName string
      SetAt       int64 // unix ms
  }
  type AgentStatusEntry struct { Identity string; Kind IdentityKind; Live bool; Status AgentStatus }
  type SetAgentStatusInput struct { Session string; Tag StatusTag; Text, Problem, IdempotencyKey string }

  // rules
  const MaxLineRunes = 200
  func NormalizeLine(field, s string) (string, error) // trim; CR/LF: "<field> must be a single line"; > 200 runes: "<field> must be at most 200 characters"
  func CheckStatusTag(t StatusTag) error // bus.StatusTag; texts below

  // core
  func AgentStatusToBus(r store.AgentStatus) bus.AgentStatus // Tag = r.Tag, LastTag ""

  // statuses
  type Service struct { *core.Core; Sessions sessions.Service }
  func (s Service) Set(ctx context.Context, identity string, in bus.SetAgentStatusInput) (bus.AgentStatus, error)
  func (s Service) List(ctx context.Context, identity string) ([]bus.AgentStatusEntry, error)          // [] never nil
  func (s Service) History(ctx context.Context, identity, of string) ([]bus.AgentStatus, error)        // [] never nil
  ```
  Tag texts (`validation`): `""` → `tag is required: one of idle, planning, implementing, reviewing, waiting, blocked, needs-human`; `offline` → `tag offline is set by the bus for an agent with no live session; set one of idle, planning, implementing, reviewing, waiting, blocked, needs-human`; other → `unknown tag %q: one of idle, planning, implementing, reviewing, waiting, blocked, needs-human`. Empty text after trimming → `text is required: one line saying what you are doing now`. Empty session → `session is required`.
  `Set` order: validate (tag, `NormalizeLine("text")`, required, `NormalizeLine("problem")`, session) before any Store call; `key := in.IdempotencyKey; in.IdempotencyKey = ""`; `s.Store.Tx`: reset outputs, `access.Resolve`, `WithReceipt(ctx, tx, identity, key, in, &res, fn)` where `fn` = `s.Sessions.RequireAs(ctx, tx, p, in.Session)` (Use and lease renewal; `session_lapsed`/`session_closed` as `RequireAs` answers), `if err := s.Access.Require(ctx, tx, p, access.Identity{Name: p.Name}, access.SetStatus); err != nil {` (this exact line, once in the file: selftest block 32 anchors on it), `Limits.Check` (open note 8), `AppendAgentStatus` with the session's `ID` and `DisplayName` and `SetAt = s.NowMs()`, `PruneAgentStatuses(identity, s.Cfg.StatusHistoryCount)`, `res = core.AgentStatusToBus(row with the returned revision)`; after commit `Limits.Charge` unless replayed. Errors through `core.FromStore`.
  `List`: `s.Store.Read`; `access.Resolve`; `s.Access.Identities(ctx, tx, p)` (the `See` set); `tx.LatestAgentStatuses`; for each latest row whose identity is visible: `Live` = `len(s.LiveSessions(...)) > 0`, `Status = AgentStatusToBus(row)` with `LastTag = row.Tag` and `Tag = StatusOffline` when not live; by identity in byte order (the Store's order).
  `History`: `s.Store.Read`; `access.Resolve`; `s.Access.Require(ctx, tx, p, access.Identity{Name: of}, access.See)` (unknown and out of scope: the existing `identity %q is not registered`); `tx.AgentStatusHistory(of, s.Cfg.StatusHistoryCount)`.

- [ ] **Step 1: Write the failing tests.**
  - `rules/status_test.go` `TestCheckStatusTag`: each of the seven nil; `""`, `offline`, `Idle`, `done` give `validation` with the texts above (exact). `TestNormalizeLine`: `NormalizeLine("text", " a ")` is `"a"`; 200 runes (`strings.Repeat("é", 200)`) passes; 201 fails `text must be at most 200 characters`; `"a\nb"` and `"a\rb"` fail `text must be a single line`; `NormalizeSubject` texts unchanged (`subject must be a single line`).
  - `statuses_test.go` (helpers: a Core over a temp SQLite store with a settable clock, `CreateIdentity`, a live session via `sessions.Service.Open`):
    - `TestSetValidation`: missing tag, `offline`, unknown tag, empty text, whitespace text, 201-rune text, multi-line text, multi-line problem, empty session: each `validation` with its exact text and no row written (`AgentStatusHistory` empty). Whitespace-only problem is stored as `""`.
    - `TestSetAddsRevisionsAndPrunes`: `StatusHistoryCount 3`; five sets (an identical one included, decision 14) return revisions 1..5; `History` is `[3 4 5]` oldest first; each row's `Session` and `DisplayName` are the session's.
    - `TestSetReplay`: two sets with key `k` and the same input return equal results and leave one revision; the same key with another text is `conflict` (`WithReceipt`); an unkeyed repeat adds a revision.
    - `TestSetRefusesHuman`: a human identity (`tx.CreateHuman`) with its own session gets `forbidden` `... is not an agent; only an agent sets a status`; another agent's session id gives `not_found` (`Session/Use`).
    - `TestSetLapsedSession`: advance past `SessionLeaseSeconds + LeaseSkewSeconds + 1`: `session_lapsed`; closed: `session_closed`.
    - `TestSetRenewsLease`: after a set, the session's `LeaseExpiresAt` is `LeaseUntil(now)`.
    - `TestListDerivesOffline` (`// open note: agent-status 2`): live `pat` entry has `Tag == LastTag == implementing`, `Live` true; after its only session lapses: `Live` false, `Tag offline`, `LastTag implementing`, `SetAt` unchanged; an identity with no status is absent; a second live session of `pat` keeps it live when the first lapses (the `IsLive` skew boundary pinned: live at exactly `LeaseExpiresAt + skew`, offline 1 ms after).
    - `TestHistoryCapsAfterLoweringN` (Review Focus 4): N 5, five sets; then `c.Cfg.StatusHistoryCount = 2` on the test's Core; `History` returns `[4 5]` while the Store holds 5 rows (`tx.AgentStatusHistory(pat, 100)`); the next set leaves `[5 6]`.
    - `TestIdentitiesCarryStatus`: `sessions.Service.Identities` as `kim` lists `pat` with `Status.Revision` of its latest and `Status.LastTag ""`; an identity without a status has `Status == nil`.
  - `config_test.go`: `{"status_history_count", 1, 1000, ...}`; default 20. Server `config_test.go`: default 20; 0 and 1001 rejected naming `status_history_count`.

- [ ] **Step 2: Run to verify failure**

Run: `go test -count=1 ./internal/core/rules/ ./internal/core/statuses/ ./internal/core/ ./internal/server/config/`
Expected: compile failures (`NormalizeLine`, `statuses` undefined).

- [ ] **Step 3: Implement** per the Interfaces block. The package comment of `service.go` carries `// open note: agent-status 11`; the `Limits.Check` call carries `// open note: agent-status 8`. Run `rg -n 'capacity exhausted' internal docs/ops` and update every pin of the capacity text.

- [ ] **Step 4: Run**

Run: `make lint && make test && go test -count=1 -run TestAccessRuleSites ./internal/core/access/ && bash scripts/m4-exit-checks.sh --deps-only && bash scripts/m7-exit-checks.sh --deps-only && make verify`
Expected: PASS; `TestAccessRuleSites` green with no allowlist edit; `deps_ok`; `make verify` exits 0.

- [ ] **Step 5: Commit**

```bash
git add internal/bus/agentstatus.go internal/bus/sessions.go internal/core/statuses/service.go internal/core/statuses/statuses.go internal/core/statuses/statuses_test.go internal/core/rules/status.go internal/core/rules/status_test.go internal/core/rules/subject.go internal/core/convert.go internal/core/config.go internal/core/config_test.go internal/core/sessions/identities.go internal/core/maintenance/budget.go internal/server/config/config.go internal/server/config/config_test.go docs/ops/configuration.md CLAUDE.md
git commit -m "feat(core): agent statuses: set with the tag rule and prune to N, list with derived offline, history (issue #10)"
```
(Add any capacity-text test file Step 3 found.)

---

### Task 4: Seam: `bus.Sessions` methods, three operations, `Identity.status`, server, local, remote

**Files:**
- Modify: `internal/bus/sessions.go:137-150` (three methods on `Sessions`), `api/openapi.yaml` (paths after `/v1/identities` `:220-234`; schemas after `Identity` `:1445-1459`, which gains optional `status`), generated `internal/gen` and `internal/gen/spec` (`make generate`), `internal/server/api.go:28,48` (`statuses statuses.Service` built with `Sessions: sess`), `internal/server/sessions.go:108-122` (`ListIdentities` copies `Status`), `internal/bus/remote/sessions.go:125-149` (`Identities` copies `status` when present), `internal/server/cmd/cmd_test.go:432-433` (47 and 8, 55), `internal/gen/openapi31_test.go:120-174` (three operationIds; 8 and 47), `docs/adr/0001-transport-and-api.md:188-191` (55: 47 protected and 8 open; the agent-status operations), `docs/adr/0010-server-health.md:27` (decision 12's totals become a dated note: "52 when decided; 55 after the agent-status operations (ADR 0014), 47 protected"), `docs/superpowers/specs/2026-10-19-agentbus2-design.md:244` (a row after `Identities`: `Agent status | set (through a session), list, history | Eric's decisions 2026-10-27, ADR 0014`), `CLAUDE.md:45` (8 open and 47 protected)
- Create: `internal/server/agentstatus.go`, `internal/bus/local/agentstatus.go`, `internal/bus/remote/agentstatus.go`
- Test: `internal/server/agentstatus_test.go` (new, `testenv_test.go` fixtures), `internal/bus/remote/agentstatus_test.go` (new)

**Interfaces:**
- Consumes (Task 3): `bus.AgentStatus`, `bus.AgentStatusEntry`, `bus.SetAgentStatusInput`, `bus.StatusTag`, `statuses.Service.Set/List/History`, `IdentityInfo.Status`.
- Produces, on `bus.Sessions` (names used by Tasks 5, 6, 8 verbatim):
  ```go
  SetAgentStatus(ctx context.Context, in SetAgentStatusInput) (AgentStatus, error)
  AgentStatuses(ctx context.Context) ([]AgentStatusEntry, error)          // every visible identity with a stored status, by identity
  AgentStatusHistory(ctx context.Context, identity string) ([]AgentStatus, error) // oldest first, at most N
  ```
  Wire (all protected: no `security` key, so they inherit the global bearer requirement; `openapi31_test.go:148-150` counts `op.Security == nil` as protected and fails on any other shape; tag `sessions`): `setAgentStatus` `POST /v1/sessions/{session}/status` (`SessionParam`, `IdempotencyKeyParam`; body `SetAgentStatusRequest` required `[tag, text]`, `tag`/`text`/`problem` plain strings, the `tag` description listing the seven and "offline is set by the server"; 200 `AgentStatus`); `listAgentStatuses` `GET /v1/agent-statuses` (200 array of `AgentStatusEntry`); `agentStatusHistory` `GET /v1/identities/{identity}/statuses` (inline `identity` path parameter as `resetCredential` `:868-872`; 200 array of `AgentStatus`, "oldest first, at most status_history_count"). Schemas: `AgentStatus` required `[identity, revision, tag, text, session, display_name, set_at]`, optional `problem` (absent when empty) and `last_tag` ("listAgentStatuses only: the stored tag; tag reads offline when live is false"); `AgentStatusEntry` required `[identity, kind, live, status]` (`kind` enum agent, human as `Identity.kind`); `Identity.status` optional `AgentStatus`. `info.version` stays `1.0.0`.

- [ ] **Step 1: Write the failing tests.**
  - `internal/server/agentstatus_test.go`: `TestSetAgentStatusWire` (a registered agent opens a session; POST `{tag: implementing, text: "Task 3"}` is 200 with the body fields and no `problem` key in the raw JSON; POST `{tag: offline, text: x}` is a problem+json `validation` with the core's text; `{tag: "", text: x}` likewise `tag is required: ...`; the same `Idempotency-Key` twice returns the same revision); `TestListAgentStatusesWire` (array, `[]` not `null` when empty; `last_tag` present); `TestAgentStatusHistoryWire` (`// open note: agent-status 13`; oldest first; unknown identity is 404 `identity "ghost" is not registered`); `TestListIdentitiesCarriesStatus` (raw JSON: `status` present for an identity that set one, absent otherwise).
  - `internal/bus/remote/agentstatus_test.go`: `TestRemoteAgentStatusUnknownTagPassesThrough` (`// open note: agent-status 1`; a canned 200 with `tag: "future"` maps to `bus.StatusTag("future")` without error); `TestRemoteAgentStatusRoundTrip` (the converters both ways, `reflect.DeepEqual`, `Problem ""` and `LastTag ""` absent on the wire).
  - Pins: `cmd_test.go` 47/8/55 and `openapi31_test.go` table and 8/47 updated in the same step, so they fail until the yaml lands.

- [ ] **Step 2: Run to verify failure**

Run: `go test -count=1 ./internal/server/... ./internal/bus/remote/ ./internal/gen/`
Expected: compile failures and count mismatches.

- [ ] **Step 3: Implement.** Add the methods to `bus.Sessions`; the yaml; `make generate`; handlers in `internal/server/agentstatus.go` (errors returned to the strict handler so they go through `WriteProblem`; `IdentityFrom(ctx)` as the caller); `local` methods delegating to `statuses.Service{Core: b.c, Sessions: b.sessions()}` with `b.identity`; `remote` methods over `SetAgentStatusWithResponse`, `ListAgentStatusesWithResponse`, `AgentStatusHistoryWithResponse` with the `errorOf`/`unparsable` pattern of `remote/sessions.go:125-149`, tag copied as a string, `kind` through `fromGenIdentityKind`; `remote.Identities` copies `status` when present. `var _ ab2bus.Bus` assertions (`local.go:21`, `remote.go:28`, `conformance/bus/bus_test.go:26`) compile.

- [ ] **Step 4: Run**

Run: `make generate && make generate-check && make lint && make test && make test-conformance && bash scripts/m4-exit-checks.sh --deps-only && bash scripts/m5-exit-checks.sh --deps-only && bash scripts/m6-exit-checks.sh --deps-only && make verify`
Expected: PASS; `TestRunRefusesEveryProtectedOperation` counts 47 protected (each new one 401 `unauthenticated` with a bad bearer) and 8 open; `deps_ok`; `make verify` exits 0.

- [ ] **Step 5: Commit**

```bash
git add api/openapi.yaml internal/gen internal/bus/sessions.go internal/bus/local/agentstatus.go internal/bus/remote/agentstatus.go internal/bus/remote/sessions.go internal/bus/remote/agentstatus_test.go internal/server/api.go internal/server/sessions.go internal/server/agentstatus.go internal/server/agentstatus_test.go internal/server/cmd/cmd_test.go internal/gen/openapi31_test.go docs/adr/0001-transport-and-api.md docs/adr/0010-server-health.md docs/superpowers/specs/2026-10-19-agentbus2-design.md CLAUDE.md
git commit -m "feat(api): setAgentStatus, listAgentStatuses, agentStatusHistory and Identity.status; protocol stays 1.0.0 (issue #10)"
```

---

### Task 5: Bus suite: agent status on both engines and both transports

**Files:**
- Create: `internal/conformance/bus/agentstatus_test.go`
- Modify: `scripts/check-gates-selftest.sh` (block 32 gains the core mutation)

**Interfaces:**
- Consumes (Task 4): the three `Sessions` methods; harness `newHarness(t, e, harnessOptions{cfg: &cfg})`, `h.as`, `h.admin`, `h.human`, `h.scopedIdentity`, `h.rtScope`, `h.clock.Advance`, `h.cfg`, `dbtest.ForEachEngine`; `Open(ctx, OpenSessionInput{Hint: ...})`.
- Produces: `TestAgentStatusAccess` with the subtest `human_set_refused` (block 32 depends on that name).

- [ ] **Step 1: Write the tests** (each `dbtest.ForEachEngine`; leaf names via `t.Run`):
  - `TestAgentStatusSetAndRead`: `pat` opens a session and sets `planning`, `implementing` (with problem), `waiting`; `h.as("kim").AgentStatuses` has one entry `pat`, `Kind agent`, `Live`, `Status.Revision 3`, `Tag == LastTag == waiting`; `AgentStatusHistory("pat")` is revisions `[1 2 3]` with problem only on 2; `h.as("kim").Identities` lists `pat` with `Status.Tag waiting`.
  - `TestAgentStatusHistoryBoundedAtN`: `cfg.StatusHistoryCount = 3`; five sets; history `[3 4 5]`.
  - `TestAgentStatusValidation`: missing tag, `offline`, `bogus`, empty text, `"a\nb"` text each `validation` (codes only; texts are Task 3's), on both transports.
  - `TestAgentStatusReplay`: same key twice → one revision; unkeyed twice → two.
  - `TestAgentStatusAccess`: `human_set_refused` (`h.human("hal@corp.example")` opens a session and sets: `forbidden`); `other_session_refused` (`kim` passes `pat`'s session id: `not_found`); `admin_reads_all` (`h.admin("root")` lists `pat`).
  - `TestAgentStatusScope` (Review Focus 3): `h.rtScope("job1")`, `coder := h.scopedIdentity("job1-coder", "job1")`; `pat` (unscoped) and `root` (admin) and `coder` each set a status; `coder.AgentStatuses` names `job1-coder` and `root`, not `pat`; `coder.AgentStatusHistory("pat")` is `not_found`; `coder.AgentStatusHistory("root")` succeeds.
  - `TestAgentStatusOfflineAfterLapse`: `pat` sets `needs-human`; advance `SessionLeaseSeconds + LeaseSkewSeconds + 1` s: entry `Live false`, `Tag offline`, `LastTag needs-human`; `Identities` no longer lists `pat`.
  - `TestAgentStatusHintedSession` (Review Focus 5): `pat` opens a primary and a `Hint: "reviewer"` session; the hinted set records `DisplayName "pat/reviewer"`; a later primary set wins the list; history holds both in order.
  - `TestAgentStatusRetired`: after the identity is retired through the admin Bus (as `retire_test.go` does), `AgentStatuses` omits it and history is `not_found`.

- [ ] **Step 2: Run them; they pass against Tasks 1 to 4** (this task adds coverage, not behavior; a failure is a bug in an earlier task, fixed here and named in the commit body).

Run: `go test -count=1 -p 1 -run TestAgentStatus ./internal/conformance/bus/ && AGENTBUS2_BUS_TRANSPORT=inproc go test -count=1 -p 1 -run TestAgentStatus ./internal/conformance/bus/`
Expected: PASS on sqlite and postgres, both transports.

- [ ] **Step 3: Extend block 32** with `status-core-check-skipped`: `internal/core/statuses/statuses.go` `if err := s.Access.Require(ctx, tx, p, access.Identity{Name: p.Name}, access.SetStatus); err != nil {` → `if err := error(nil); err != nil {`, run `'^TestAgentStatusAccess$/sqlite'` in `./internal/conformance/bus/` (Docker-free, as blocks 14 and 15), want `TestAgentStatusAccess/sqlite/human_set_refused`.

- [ ] **Step 4: Run**

Run: `make lint && make test-conformance && make verify`, commit (Step 5), then `make check-gates-selftest`.
Expected: PASS; selftest `OK` with all three block 32 mutations firing.

- [ ] **Step 5: Commit**

```bash
git add internal/conformance/bus/agentstatus_test.go scripts/check-gates-selftest.sh
git commit -m "test(bus): agent status on both engines and transports; selftest block 32 covers the core check (issue #10)"
```

---

### Task 6: MCP `set_status`, statuses in `others` and `discover`; CLI `agent-status`

**Files:**
- Modify: `internal/mcpserver/server.go` (`set_status` in `addTools` after `discover` `:683`; `identityOut` `:209-214` gains `Status *statusOut json:"status,omitempty"`; `identitiesOut` `:341-351` copies it; the `register` `:434` and `discover` `:683` descriptions say "each with its status (tag, text, problem, display_name, set_at) when it has set one"), `internal/mcpserver/inputs.go` (`setStatusIn`), `internal/mcpserver/server_test.go:241-262` (`set_status` in the name table; `len(want) != 26`), `internal/mcpserver/run_test.go:111` (26), `internal/cli/cli.go:17` (`Usage` gains `agent-status`) and `:25-50` (`"agent-status": RunAgentStatus`), `docs/adr/0010-server-health.md:32` ("The MCP server has 26 tools"), `CLAUDE.md` (`mcp` "26 tools"; the client-commands list gains `agent-status set [-session ID] [-problem P] [-key K] TAG TEXT | list | history IDENTITY`)
- Create: `internal/cli/agentstatus.go`, `internal/cli/agentstatus_test.go`, `internal/mcpserver/agentstatus_test.go`

**Interfaces:**
- Consumes (Task 4): `Bus.SetAgentStatus`, `Bus.AgentStatuses`, `Bus.AgentStatusHistory`, `IdentityInfo.Status`; MCP `withSession` (`sessions.go:203`), `s.ownPrimary(r)` under `s.smu`, `s.do`, `result`; CLI `newOpCmd`, `opDispatch`, `c.parse`, `c.connect`, `c.usageErr`, `locateSession`, `bindProfile`, `processTable`, `selfPID`, `profile.DataDir`, `printJSON`, `fail`.
- Produces:
  - `setStatusIn{Session, Tag, Text, Problem, IdempotencyKey string}` (json `session,omitempty`, `tag`, `text`, `problem,omitempty`, `idempotency_key,omitempty`, each with a jsonschema description).
  - `statusOut{Tag string "tag"; Text "text"; Problem "problem,omitempty"; Revision int64 "revision"; DisplayName "display_name"; SetAt int64 "set_at"}`; `set_status` returns it.
  - `set_status` description (exact): `Agentbus2: set your status, the one line the operator's board shows for your identity. tag is one of idle, planning, implementing, reviewing, waiting, blocked, needs-human (offline is set by the bus when you have no live session). text says what you are doing now (one line, at most 200 characters); problem (optional, one line) says what is in your way. Each call replaces your current status and keeps the earlier ones as history. Set it at the start of each task, when you are blocked or need the user, and set idle before agentbus2 park. A session registered with a hint does not set status. Use idempotency_key to make retries safe.`
  - CLI: `RunAgentStatus` (`set`, `list`, `history`); `set` takes exactly two positionals `TAG TEXT` and flags `-session`, `-problem`, `-key`; `list` none; `history` exactly `IDENTITY`. Outputs (one JSON document, snake_case, arrays never null): `set` → `{identity, revision, tag, text, problem?, session, display_name, set_at}`; `list` → `[{identity, kind, live, status: {..., last_tag}}]`; `history` → `[{...}]` oldest first. A `bus.Error` is exit 1 (`agentbus2: <code>: <message>`); a flag, arity or locate error exit 2.

- [ ] **Step 1: Write the failing tests.**
  - `TestToolListIsExact` and `TestRunServesStdio` at 26.
  - `internal/mcpserver/agentstatus_test.go`: `TestSetStatusDefaultsToOwnSession` (`// open note: agent-status 9`; after `register`, `set_status {tag: planning, text: x}` without `session` succeeds with the registered display name; before `register` it is `validation` `set_status needs a session: call register first, or pass session`); `TestSetStatusUnknownTagFromServer` (`tag: bogus` returns the server's `validation` text, the client does not validate); `TestDiscoverCarriesStatus` (`discover` and `register.others` carry `status` for an identity that set one, no `status` key otherwise); `TestSetStatusUnparks` (a parked primary state file is unparked by `set_status`, as for every tool but `receive`, Decision 18).
  - `internal/cli/agentstatus_test.go`: `TestAgentStatusSetLocatesSession` (`// open note: agent-status 9`; a harness state file supplies the session; no state file and no `-session` is exit 2 with `errNoSession`'s text); `TestAgentStatusSetArity` (one positional, three positionals: exit 2); `TestAgentStatusUnknownTagExit1` (`agent-status set -session S bogus x`: exit 1, `agentbus2: validation: unknown tag "bogus": ...`); `TestAgentStatusListAndHistoryJSON` (shapes above; `[]` when empty); `TestUsageNamesAgentStatus`.

- [ ] **Step 2: Run to verify failure**

Run: `go test -count=1 ./internal/mcpserver/ ./internal/cli/`
Expected: FAIL (25 tools; unknown command `agent-status`).

- [ ] **Step 3: Implement** per the Interfaces block. `set_status` resolves the default session under `s.smu` from `ownPrimary`, then calls `withSession(ctx, s, id, func(b bus.Bus, id string) (bus.AgentStatus, error) {...})`.

- [ ] **Step 4: Run**

Run: `make lint && make test && bash scripts/m6-exit-checks.sh --deps-only && go test -count=1 -v ./internal/e2e/ > "${TMPDIR:-/tmp}/ab2-e2e.log" 2>&1; bash scripts/m6-exit-checks.sh "${TMPDIR:-/tmp}/ab2-e2e.log" && make verify`
Expected: PASS; m6 prints `sequence=13`, `exitcodes=11`, `words_ok=1`; `make verify` exits 0.

- [ ] **Step 5: Commit**

```bash
git add internal/mcpserver/server.go internal/mcpserver/inputs.go internal/mcpserver/server_test.go internal/mcpserver/run_test.go internal/mcpserver/agentstatus_test.go internal/cli/cli.go internal/cli/agentstatus.go internal/cli/agentstatus_test.go docs/adr/0010-server-health.md CLAUDE.md
git commit -m "feat(mcp,cli): set_status tool, statuses in others and discover, agent-status set|list|history (issue #10)"
```

---

### Task 7: Protocol text

**Files:**
- Modify: `internal/mcpserver/server.go:88` (`RegisterSteps` gains one sentence after the receive sentence: `Set your status with set_status at the start of each task, when you are blocked or need the user (tag needs-human, with a problem), and set tag idle before agentbus2 park; a session registered with a hint does not set status.`), `internal/cli/skills/using-agentbus2/SKILL.md` (a new `## Status` section after `## Session protocol` `:289-327`: the tag table with one-line meanings from brief 4.3, when to set, `problem` for `blocked`/`needs-human`, one line per identity so hinted sessions do not set it, `offline` is derived, history is the last `status_history_count` (default 20); `## Park` `:399-415`: "set `idle` with `set_status` first, then run `agentbus2 park`; never after, because `set_status` unparks"; `## Subagents` `:468-482`: "does not call `set_status`"), `internal/cli/install.go:70-74` (`initPromptTail`: "set your status to idle with set_status, then run `agentbus2 park` as your last agentbus2 action ...") and `:94-104` (`grokRule`, same sentence), `internal/cli/subagenthook.go:54-56` (append `Do not call set_status; your parent's status covers you.`)
- Test: `internal/cli/skill_test.go` (new `TestSkillStatusProtocol`), `internal/cli/install_test.go` (new `TestInitPromptsSetIdleBeforePark`), `internal/cli/subagenthook_test.go:45` (the `want` list gains `Do not call set_status`), `internal/mcpserver/server_test.go` `TestInstructions` (unchanged; passes), `internal/cli/sessionstarthook_test.go` (unchanged; passes with the new `RegisterSteps`)

- [ ] **Step 1: Write the failing tests.** `TestSkillStatusProtocol`: `SkillMD` contains `set_status`, each of the seven tags, `needs-human`, `set_status` before `agentbus2 park` in the Park section (index order), and `does not call` `set_status` in the Subagents section. `TestInitPromptsSetIdleBeforePark`: in `InitPrompt`, `codexPrompt` and `grokRule`, `idle` appears and its index is before `agentbus2 park`. `RegisterSteps` contains `set_status` and `idle before agentbus2 park`.

- [ ] **Step 2: Run to verify failure**

Run: `go test -count=1 -run 'TestSkillStatusProtocol|TestInitPromptsSetIdleBeforePark|TestSubagentHook|TestInstructions|TestSessionStart' ./internal/cli/ ./internal/mcpserver/`
Expected: FAIL on the three new assertions.

- [ ] **Step 3: Implement** the text edits. Keep `TestInitPromptsAvoidRetiredVocabulary` and the m6 word list clean.

- [ ] **Step 4: Run**

Run: `make lint && make test && bash scripts/m6-exit-checks.sh --deps-only && make verify`
Expected: PASS; `words_ok` in a full m6 run; `make verify` exits 0.

- [ ] **Step 5: Commit**

```bash
git add internal/mcpserver/server.go internal/cli/skills/using-agentbus2/SKILL.md internal/cli/install.go internal/cli/subagenthook.go internal/cli/skill_test.go internal/cli/install_test.go internal/cli/subagenthook_test.go
git commit -m "docs(protocol): agents set their status at task boundaries and idle before park; subagents do not (issue #10)"
```

---

### Task 8: TUI: the agents board, history, attention count and session-row icons

**Files:**
- Create: `internal/tui/agents.go` (board rows, ordering, `renderBoard`, `renderHistory`, `statusIcon`, `attention`), `internal/tui/agents_test.go`
- Modify: `internal/tui/msgs.go` (`statusMsg.agents []bus.AgentStatusEntry`; `historyMsg{identity string; rows []bus.AgentStatus; err error}`), `internal/tui/model.go` (fields `agents []bus.AgentStatusEntry` and `board agentBoard{open bool; sel int; hist string; rows []bus.AgentStatus; err error}`; `statusCmd` `:1366-1396` calls `AgentStatuses` after `TagSubscriptions`, an error aborts the round as the others do; `onStatus` `:1277-1301` stores it and, while `board.hist != ""`, returns `historyCmd(board.hist)`; the `enter` case `:538-541` opens the board when `m.headSel == secSessions`; board keys handled before the normal switch), `internal/tui/view.go` (`render` `:179-218`: `if m.board.open { pane = m.renderBoard() }` before the `sessSel` case; the session row `:588-604` uses `statusIcon` per open note 5; the sessions heading suffix; `renderStatusBar` `:808-845`: `N need you` after `as <identity>`), `internal/tui/icons.go` (vars `iconStatusPlanning`, `iconStatusImplementing`, `iconStatusReviewing`, `iconStatusWaiting`, `iconStatusBlocked`, `iconStatusNeedsHuman`, `iconStatusOffline`; `iconVars` names `status_planning`, `status_implementing`, `status_reviewing`, `status_waiting`, `status_blocked`, `status_needs_human`, `status_offline`; `nerdIcons` ``, ``, ``, ``, ``, ``, ``; emoji per open note 3, each `<codepoint>️ ` like the existing set), `internal/tui/help.go:40` (`enter (heading)`: "... on the sessions heading, open the agents board"; new rows for the board: `↑ / ↓` move, `enter` history, `esc` back), `internal/tui/fixture_test.go:270-312` (`countingBus` notes `AgentStatuses` and `AgentStatusHistory`), `docs/tui.md` (`:84` and `:86` the sessions row icon and `enter` on its heading; a new `### Agents board` section after `### Health` `:137-141`: columns, order, offline group, history, attention count, refresh; Icons table `:195-216` gains the seven `status_*` names with both glyphs)
- Test: `internal/tui/agents_test.go`, `internal/tui/icons_test.go`, `internal/tui/session_rows_test.go`

**Interfaces:**
- Consumes (Task 4): `Bus.AgentStatuses`, `Bus.AgentStatusHistory`, `bus.AgentStatusEntry`, `bus.AgentStatus`, `bus.StatusTag` constants, `IdentityInfo.Status`; existing `secSessions`, `m.headSel`, `m.pane()`, `paneChannels`, `stamp`, `shortDur`, `cut`, `m.theme` (`Warn`, `Dim`, `Agent`), `markSel`, `iconIdle`, `iconAgent`.
- Produces: `func statusIcon(t bus.StatusTag) string` (idle → `iconIdle`, the seven others → their vars, unknown → `iconAgent`); `func (m Model) boardRows() []boardRow` (`boardRow{identity string; live bool; tag bus.StatusTag; st *bus.AgentStatus}`): entries of `m.agents`, plus every `m.identities` item of kind agent with no entry (`st == nil`, live); order: live `needs-human`, live `blocked`, other live (status or not) by identity, then offline by identity; `func (m Model) attention() int`.
  Board layout: title line `agents (N) · enter history · esc back`; columns `AGENT`, `TAG` (icon + tag), `SINCE` (`shortDur(now - SetAt)`), `NOW` (text, then ` · <problem>` in warn); a dim `── offline ──` line before the first offline row (its NOW is `last: <text>`); `-` for a row with no status. History: title `<identity> · history, oldest first · esc back`; one line per revision `stamp(SetAt) icon tag text [· problem] by <display_name>`; empty → `no status yet`; when longer than the pane, the newest rows are kept (the tail). Keys while open: `↑`/`↓` move `sel` (board only), `enter` opens the selected row's history (one `AgentStatusHistory` call), `esc` history → board → closed (selection back on the `sessions` heading), `q` quits, `?` and `h` open their overlays; every other key is ignored.

- [ ] **Step 1: Write the failing tests** (fixtures: `newFixture`, `newCountingFixture`, `sessionFixture` (`session_rows_test.go:13`), `f.key`, `f.run(f.m.statusCmd())`, statuses set through the fixture's core as an agent identity):
  - `icons_test.go` `TestStatusIconsCoverTags` (`// open note: agent-status 3`): `statusIcon` of each of the eight tags is non-empty and `lipgloss.Width == 3` in the emoji set and after `LoadIcons(Settings{Icons: "nerdfont"})`; each emoji string's base codepoint is the one in open note 3; `statusIcon("future") == iconAgent`. `TestIconSetsCoverTheSameNames` passes with the seven new names. `TestLoadIconsForAppleTerminal` unchanged (no new variant).
  - `TestAgentsBoardOpensFromSessionsHeading` (`// open note: agent-status 4`): `enter` on the sessions heading sets `board.open`, the pane shows `agents (`; `←` then `→` on the heading still collapse and expand it; `esc` closes the board with the selection on the heading.
  - `TestAgentsBoardOrder` (`// open note: agent-status 6`): entries `zed` live `implementing`, `amy` live `blocked`, `bob` live `needs-human`, `old` offline (last `needs-human`), plus live agent `cal` with no status: row order `bob, amy, cal, zed, old`, with the offline divider before `old` and `cal` showing `-`.
  - `TestAgentsBoardHistoryOldestFirst` (`// open note: agent-status 14`): `enter` on `zed` calls `AgentStatusHistory` once (`countingBus`); the pane lists revisions in increasing order with `by zed`; a status round while open calls it once more; `esc` back to the board makes no further call.
  - `TestAttentionCountLiveOnly` (`// open note: agent-status 7`; Review Focus 1): with `amy` blocked and `bob` needs-human live and `old` offline needs-human, the status bar contains `2 need you` and the sessions heading `(2 need you)`; with none, neither text appears.
  - `TestSessionRowStatusIcon` (`// open note: agent-status 5`): a live row of `sam` (status `reviewing`) begins with the reviewing icon; a live row of an agent with no status begins with `iconAgent`; the own row keeps the user icon; the idle row keeps `iconIdle`.
  - `TestStatusRoundCallsAgentStatuses`: one `statusCmd` makes exactly one `AgentStatuses` call and no `AgentStatusHistory` call.
  - `TestNoInboxView` and `TestBothModes` unchanged.

- [ ] **Step 2: Run to verify failure**

Run: `go test -count=1 ./internal/tui/`
Expected: compile failure (`statusIcon`, `board` undefined).

- [ ] **Step 3: Implement** per the Interfaces block. No new import outside `internal/bus` and `internal/version`; no channel name is read to decide anything.

- [ ] **Step 4: Run**

Run: `make lint && make test && go test -count=1 -v -run 'TestBothModes|TestNoInboxView|TestRunTUI' ./internal/tui/ ./internal/cli/ > "${TMPDIR:-/tmp}/ab2-tui.log" 2>&1; bash scripts/m8-exit-checks.sh "${TMPDIR:-/tmp}/ab2-tui.log" && GOOS=windows go vet ./internal/tui/ && make verify`
Expected: PASS; m8 prints `modes=2`, `noinbox=1`, `cli=2` and an unchanged allowlist; `make verify` exits 0. `windows.yml` runs `icons_windows_test.go` (unchanged; the new emoji have no escapes).

- [ ] **Step 5: Commit**

```bash
git add internal/tui/agents.go internal/tui/agents_test.go internal/tui/msgs.go internal/tui/model.go internal/tui/view.go internal/tui/icons.go internal/tui/icons_test.go internal/tui/help.go internal/tui/fixture_test.go internal/tui/session_rows_test.go docs/tui.md
git commit -m "feat(tui): agents board with history, attention count and status icons on session rows (Eric 2026-10-27, issue #10)"
```

---

### Task 9: ADR 0014 and the records sweep

**Files:**
- Create: `docs/adr/0014-agent-status.md` (status `Accepted (Eric, 2026-10-27)`; the 15 decisions labeled as Eric's, copied from brief section 5 with the chosen options spelled out; the tag table with meanings and both glyphs; N = `status_history_count` default 20, range 1 to 1000, pruned at write; offline derived at read from "no live session" with `last_tag`; pull only, push a follow-up through #13; the access rule and its denials; the wire (three operations, `Identity.status`, protocol 1.0.0, 55 operations); the rows go with the identity (retire, `ResetRegistry`) and with `ResetData`, never by time; the bound identities × N rows of at most about 600 bytes; a section "Settled open notes (agent choices, not Eric's)" listing this plan's fifteen with their markers; "Shared with agentbus v1 ADR 0026": decisions 3, 6, 8, 13 and 15 and the protocol text, verbatim)
- Modify: `docs/adr/README.md` (row `0014 | Agent status and history | Accepted (Eric, 2026-10-27)`), `CLAUDE.md` (one Design bullet: "Agent status (ADR 0014): `agent_statuses` per identity, `internal/core/statuses`, `Identity/SetStatus`, offline derived at read, prune to `status_history_count` at write, pull only"), `docs/superpowers/specs/2026-10-27-agent-status-design.md` section 1 (one line: "Implemented by `docs/superpowers/plans/2026-10-27-agent-status.md`; recorded in ADR 0014")

- [ ] **Step 1: Check** every number and name the ADR states against the tree: `rg -n 'StatusHistoryCount|status_history_count' internal docs`, `rg -c 'operationId' api/openapi.yaml` (55), the tool table in `server_test.go` (26), `rg -n 'open note: agent-status' .` (fifteen notes, each at its named site), `rg -n '25 tools|44 protected|52 in all' CLAUDE.md docs --glob '!docs/superpowers/**'` (no stale hit; the dated ADR 0010 note is the only mention of 52).
- [ ] **Step 2: Write** the ADR and the edits above. If ADR 0014 was taken by a plan that landed first, use the next free number and update every reference this plan made to 0014 (`rg -n 'ADR 0014' docs internal CLAUDE.md`).
- [ ] **Step 3: Run the full gates**

Run: `make verify && make check-gates-selftest && make release-check && bash scripts/m4-exit-checks.sh --deps-only && bash scripts/m5-exit-checks.sh --deps-only && bash scripts/m6-exit-checks.sh --deps-only && bash scripts/m7-exit-checks.sh --deps-only && bash scripts/m8-exit-checks.sh --deps-only`
Expected: every command exits 0; keep the `make verify` and selftest output for the report. Then ask Eric for the push (`Approve: git push origin main for the agent-status plan (Tasks 1 to 9)`) and confirm `ci.yml` and `windows.yml` are green on the pushed commit.

- [ ] **Step 4: Commit**

```bash
git add docs/adr/0014-agent-status.md docs/adr/README.md CLAUDE.md docs/superpowers/specs/2026-10-27-agent-status-design.md
git commit -m "docs(adr): 0014 agent status and history (Eric's decisions of 2026-10-27, issue #10)"
```

---

### Task 10: The shared decisions in agentbus v1 (ericfitz/agentbus#43)

Planned here, executed in `/Users/efitz/Projects/agentbus` under that repository's own `CLAUDE.md` (read it first: its branch, PR and done-gate rules govern, not this plan's). No v1 code changes.

**Files (v1 repository):**
- Create: `/Users/efitz/Projects/agentbus/docs/adr/0026-agent-status.md` (newest v1 record is 0025; take the next free number if that changed)

- [ ] **Step 1: Write the record:** status "Accepted (Eric, 2026-10-27), shared with agentbus2 ADR 0014"; the decisions that are not v2-specific, copied verbatim from agentbus2 ADR 0014: the tag set and meanings (decision 6), offline derived from "no live session" and never set by an agent (3), history N default 20 pruned at write (8), pull only (13), `problem` optional for every tag (15), every set a new revision (14), and the protocol text (set at task boundaries and when blocked or needing the user, `idle` before park, hinted sessions and subagents do not set status); a closing section naming what v1 decides for itself (storage, access, wire, TUI) and linking agentbus2 ADR 0014 and both issues.
- [ ] **Step 2: Gate:** v1's documented done gate (its `CLAUDE.md` `Done gate:` line) and its docs checks; a docs-only change still runs them.
- [ ] **Step 3: Land** per v1's rules (commit with the attribution footer; push or PR only with `Approve: <v1 push or PR for ADR 0026>` from Eric), and comment on ericfitz/agentbus#43 and ericfitz/agentbus2#10 with the two ADR links only after Eric approves the outward-facing step.
