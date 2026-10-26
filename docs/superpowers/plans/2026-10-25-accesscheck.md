# One Access Check Implementation Plan (Eric's decisions of 2026-10-25)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every access decision on the server side (channel permissions, the scope boundary, server administration, session ownership, the agent memory filter, the Slack bridge's "who may post" and "who is invited", the health tier) is made by one function in one package, `internal/core/access`; the Store serves facts and holds no rule; the implicit creator entry and the task owner guard are gone; a test proves no other code decides; the ACL matrix, the auth exit test and the e2e code table keep their counts.

**Architecture:** A new package `internal/core/access` holds `Principal`, `Resolve`, the object kinds (`Channel`, `Server`, `Session`, `Identity`), the actions and one pure `decide` over the rule table of spec section 3.2 (the scope gate first, then the ACL rows), wrapped by `Checker.Check`, `Require` (point form), `Channels` and `Identities` (set forms) and `Holders` (reverse set form). The Store gains one policy-free query, `Tx.ACLRowsFor(names...)`, and loses `EffectivePermissions` and `ReadableChannels`. `internal/core/authz.go` and `internal/agentview` are deleted; their call sites, the six self-made decisions and the five session-ownership copies move to `Access`. The task owner guard and the `force` wire member are removed. The `check-gates-selftest.sh` administrator anchor moves with the rule. One wire change (`force` removed, spec 3.14), one code change (`unavailable` for an unconfigured bridge), no schema change, no setting.

**Tech Stack:** Go 1.27, modernc SQLite and pgx Postgres (one `sqlstore` implementation), testcontainers for the Postgres suites, kin-openapi + oapi-codegen (`make generate`, Task 8 only), go/ast for the gate test.

**Spec:** `docs/superpowers/specs/2026-10-25-accesscheck-design.md`. Sections 1, 4, 4.1 and 4.2 are Eric's decisions and are implemented as written (marked "(4.1 x)" in the task that implements each ruling; 4.2, the scope-prefixed names, is Task 11). Nothing is open. Section numbers below refer to it.

## Global Constraints

- Done gate: `make verify` (needs Docker; a run that skips the Postgres suites is not a pass). Work is done only when `ci.yml` and `windows.yml` are green on the pushed commit (including `release-check`, `check-gates-selftest` and `m9-exit-selftest`); a red `windows.yml` is fixed first. Every task runs `make lint && make test && make test-conformance`; Tasks 1, 5, 6, 8 and 10 run `make verify`; Task 5 also runs `make check-gates-selftest`; Task 8 runs `make generate` and `make generate-check`.
- Import rules (CLAUDE.md), re-checked by `bash scripts/m4-exit-checks.sh --deps-only && bash scripts/m5-exit-checks.sh --deps-only && bash scripts/m6-exit-checks.sh --deps-only && bash scripts/m7-exit-checks.sh --deps-only && bash scripts/m8-exit-checks.sh --deps-only` in every task: `internal/core/access` imports only `internal/store` and `internal/bus` of this module (never `internal/core`, `internal/gen`, `internal/server`, `internal/bus/local`, `internal/bus/remote`); `internal/store/...` never imports `internal/core/...`; `internal/server`, `internal/bus/remote`, `internal/mcpserver`, `internal/cli`, `internal/tui` never import `internal/core/access`.
- Pinned counts and anchors that stay: `TestACLMatrix` 15 rows, 26 sub-cases, 52 leaves per engine (`scripts/m3-exit-counts.sh`; never edit `section46Rows`, `matrixSubcases` or a row's content; every matrix channel stores the creator's `[read, write, admin]` row, so retiring the implicit creator entry moves nothing there), `TestAuthEndToEnd` 11 leaves per engine, the e2e counts (`TestSection24Sequence` 13, `TestExitCodeTable` 11), the Milestone 8 counts, the MCP tool count 25, the `check-gates-selftest.sh` anchor `if !readable[sub.ChannelID] {` in `internal/core/delivery/receive.go` (block 8: `load` keeps a `readable` set under that name). Counts and anchors that move on purpose, each in the task named: block 15's file and anchor (`internal/core/authz.go` `if !id.Admin {` to `internal/core/access/decide.go` `if !p.Admin {`, Task 5; this supersedes the health plan's and the visibility plan's "never edit `RequireAdmin`"); the store suite loses `TestACLReadableChannels` and the `EffectivePermissions`/`ReadableChannels` halves of `acl_test.go`, `channels_test.go:183` and `text_test.go:296-323` (Task 6) and gains `TestACLRowsFor` (Task 1); `internal/core/authz_test.go` is replaced by the access package's tests (Task 5); the `internal/agentview` tests go with the package (Task 4); the `force`/owner-guard assertions of `internal/conformance/bus/tasks_test.go`, `internal/server/memories_tasks_test.go`, `internal/core/tasks/doc_test.go`, `internal/cli/tasks_test.go` (Task 8).
- Error codes change in exactly two places, both Eric's decisions: the task owner guard's `conflict` no longer exists (the call succeeds, Task 8) and bridge-not-configured is `unavailable`, `retryable: false`, same text (Task 7). One new denial exists: an object outside a scoped principal's scope is `not_found` with the text the operation uses for a thing that does not exist (Reason `out_of_scope`; Eric's decision 4.1 (d)), never `forbidden`, never a distinct text; a scoped identity's create or rename to a name without its `<scope>-` prefix, and anyone's to a `<scope>-` name without that scope, is `validation` (spec 4.2, Task 11; between Task 2 and Task 11 the collision answers today's `conflict`, pinned in `TestScopeGateChannels` and flipped by Task 11). Everything else keeps its code: channel and server denials `forbidden`, a reserved or unknown principal `not_registered`, a session not the caller's `not_found`, an agent subscribing to a memory channel `validation` with today's text. Denial texts the matrix and e2e tests assert on keep their wording (`"%s needs %s on channel %q"`, `"administrator privilege required"`, `"session %q does not exist"`, `"%q is not a registered identity"`). An undefined (object, action) pair is `internal`, never a panic (Definition of Done B), with a test.
- The rule lives in `internal/core/access/decide.go` and nowhere else: no task adds a conditional on `.Admin`, `.Creator`, `.Owner`, `.Scope`, `Kind == store.IdentityHuman`, `Kind == KindMemory`, a `store.Perm*` membership test or a `tx.ACL(` call outside that package except the allowlist of Task 10's gate; no SQL under `sqlstore` filters on `is_admin`, `can_read`, `can_write`, `can_admin` or `creator`.
- Definition of Done D: this plan adds no state; the Store query is read-only; the `force` removal writes no migration (the `forced` revision rows already stored stay readable as history, spec 3.14).
- Docs move with the code in the task that changes the behavior; Task 10 is the sweep. American English.
- Commits land on `main`, one per task, ending with the session's `Co-Authored-By` and `Claude-Session` lines. No push without Eric. Other agents may be editing this tree: `git add` only the files listed in the task, never `-A`. `internal/gen` is edited only by `make generate` (Task 8).

## Review Focus

1. The rule is one function: `decide` is the only place that reads a principal's `Admin` or `Scope`, a channel's scope or memory kind, a stored row's flags, a session's identity (Task 1; Task 10's `TestAccessRuleSites` proves it over the server-side packages and, for the memory kind, the MCP server and the CLI).
2. The set forms and the point form agree by test, not by inspection: `TestChannelsAgreesWithCheck` on both engines, every channel action, scoped and unscoped principals (Task 1).
3. The scope gate denies everything outside the scope and nothing inside it: `TestScopeGateChannels` (Task 2), `TestScopeGateDelivery` (Task 3), `TestScopeGateServer` (Task 5); an unscoped identity's behavior is unchanged in every one of them.
4. The implicit creator entry is gone and nothing else moved: a creator without a stored `admin` row is `forbidden`, a server administrator is allowed, the matrix counts are unchanged (Task 2).
5. The administrator anchor moved with the rule and the self-test proves the moved anchor still fails the auth exit test when mutated (Task 5, `make check-gates-selftest` output in the report).
6. The Store carries no policy after Task 6; Receive, Peek and Watch resolve the principal once from the caller's identity and answer `not_registered` for a deleted identity (Task 3); an agent principal is never delivered a memory channel and the clients hold no copy of that rule (Tasks 3 and 4).
7. Every trace of `force` and the owner guard is gone (Task 8: `rg -n -i '\bforce\b|forced|ownerGuard' internal api docs -g '!internal/gen/**'` prints only the installer and Slack comments spec 3.14 names), and the wire change is recorded.

---

### Task 1: `internal/core/access` and the fact query `Tx.ACLRowsFor`

**Files:**
- Create: `internal/core/access/doc.go`, `principal.go` (`Principal`, `Anonymous`, `Resolve`), `objects.go` (`Object`, `Channel`, `Server`, `Session`, `Identity`, `Action` constants), `decide.go` (`inScope`, `decide`, the table of spec 3.2, the denial codes, texts and Reasons), `checker.go` (`Checker`, `Config`, `Decision`, `Check`, `Require`, `Channels`, `Identities`, `Holders`, the `WithText` option), `decide_test.go`, `checker_test.go` (both engines through `dbtest.ForEachEngine` with a `sqlstore` store opened as `internal/core/authz_test.go` does today)
- Modify: `internal/store/channels.go` (`ACLRow`, `ACLRowsFor` on `Channels`; `EffectivePermissions` and `ReadableChannels` stay until Task 6), `internal/store/sqlstore/acl.go` (`ACLRowsFor`), `internal/store/doc.go` (one sentence)
- Test: `internal/conformance/store/acl_test.go` (`TestACLRowsFor`)

**Interfaces:**
- Consumes: `store.Tx` (`Identity`, `Channel`, `ListChannels`, `ACL`, `Session`, `ListIdentities`), `store.Everyone`, `store.Admins`, `store.ReservedIdentity`, `store.KindMemory`, `store.IdentityAgent`, `bus.Errorf`, `bus.Code*`.
- Produces: in `internal/store`: `type ACLRow struct{ ChannelID int64; Identity string; Read, Write, Admin bool }`; `ACLRowsFor(ctx context.Context, names ...string) ([]ACLRow, error)` with the doc comment "ACLRowsFor returns the stored channel_acl rows whose identity is one of names, ordered by (channel_id, identity). It is a fact query: no permission filter, no administrator join, no binding of an unregistered or malformed name (such a name matches nothing). The rule over these rows is internal/core/access." In `internal/core/access`: the API of spec 3.3 verbatim; `Config{ChannelCreation string}` (the `core.Config.ChannelCreation` value, copied in by `core.New` in Task 2); `inScope(p Principal, o facts) bool` as spec 3.12; `decide(p Principal, f facts, a Action) Decision` where `facts` is the union type over `channelFacts{Channel store.Channel; Rows []store.ACLEntry}`, `serverFacts{}`, `sessionFacts{store.Session}`, `identityFacts{store.Identity}`; `Decision{Allowed bool; Code bus.Code; Text, Reason string}` with Reasons `out_of_scope`, `no_permission`, `not_admin`, `not_owner`, `memory_channel`, `not_human`, `no_write`. The `decide.go` file contains the literal `if !p.Admin {` exactly once, in the `Server`/`Administer` clause.

- [ ] **Step 1: Write the failing tests.** `decide_test.go`, a table over spec 3.2 and 3.12: (a) `Channel/Read` with rows `[{everyone,[read]}]` allowed for a non-admin, with `[{admins,[read]}]` allowed only for an admin, with `[{pat,[write]}]` denied `no_permission` for `pat`; (b) `Channel/Admin` allowed for an admin with no rows, **denied** for the creator with no rows (the implicit creator entry is retired, spec 3.13), denied for a non-admin with `[{pat,[read,write]}]`; `Channel/Read` denied for an admin with no rows (implicit `admin` is not `read`); (c) `Channel/List` (interim clause, spec 3.2) allowed for every unscoped principal with or without rows (Decision 94 until the visibility rebuild), denied `out_of_scope` for a scoped principal on an out-of-scope channel; (d) `Channel/Deliver` allowed for an agent on an ordinary channel with read, denied `memory_channel` (code `validation`, text `%s is a memory channel: agents do not subscribe to memories (decision 20); use search to find a memory and get_memory to read it`) for an agent on a memory channel with read, allowed for a human on that memory channel; `Channel/Read` on the memory channel allowed for the agent (search and get_memory still work); (e) `Channel/Post` allowed for a human with write, denied `not_human` for an agent with write, denied `no_write` for a human without; (f) `Server/Administer` by the flag; `Server/CreateChannel` allowed for anyone with `Config{ChannelCreation: "open"}`, for an admin only with `"admins"`; (g) `Session/Use` by identity equality, denial `not_found`; (h) `Identity/See` allowed for an unscoped principal on any identity; (i) the scope gate: a principal `{Name: job1-coder, Scope: job1}` on a channel with `Scope: job1` and `[{everyone,[read,write]}]` is allowed `Read`; on a channel with `Scope: ""` (general) with the same rows is denied for `Read`, `Write`, `Admin`, `List`, `Deliver`, `Post` with Reason `out_of_scope`, Code `not_found` and Text `channel %q does not exist` (Eric's decision 4.1 (d)); on a channel with `Scope: job2` likewise; `Server/Administer` denied with Reason `not_admin`, Code `forbidden`, Text `administrator privilege required` even with `Admin: true` (4.1 (a): the answer every non-administrator gets); `Server/CreateChannel` allowed under `open`; `Identity/See` allowed for an identity in `job1`, denied `not_found` `identity %q is not registered` for one in `job2` and for an unscoped one (4.1 (c), (d)); `Session/Use` on another's session `not_found` as always; an unscoped principal with identical rows is allowed everywhere the scoped one was denied; (j) `Anonymous` denied every action; (k) `decide(p, serverFacts{}, Read)` returns an `internal` decision with the text `access: action "read" is not defined for server`. `checker_test.go` under `dbtest.ForEachEngine`: `TestResolve` (`""`, `everyone`, `admins`, `slack-bridge`, unknown: `not_registered` with the text `%q is not a registered identity`; a registered human: `Kind` human, `Admin` as set; a scoped agent: its `Scope`); `TestRequireTexts` (the texts of the Global Constraints, byte for byte, through `Require` and through `Require` with `WithText("only an administrator may name a scope")`); `TestChannelsAgreesWithCheck`: seed `root` (admin), `pat`, `kim`, `hal` (human) and a scope `job1` with `job1-coder` (`CreateScopedIdentity`); 40 channels with random creator in `{"", pat, kim}`, random scope in `{"", job1}` and random rows drawn from `{pat, kim, hal, job1-coder, everyone, admins}` x random non-empty permission sets (seeded `math/rand/v2`, seed logged); for each principal and each of `Read`, `Write`, `Admin`, `List`, `Deliver`, `Post`: `Channels(p, a)` equals the ascending ids where `Check(p, Channel{id}, a).Allowed`; `TestIdentitiesAgreesWithCheck` likewise over `Identities(p)` and `Check(p, Identity{name}, See)`; `TestHolders`: `Holders(ch, Post)` lists exactly the human identities whose `Check` allows `Post`, never `everyone`/`admins`/`slack-bridge`. `TestACLRowsFor` (store suite): rows for `pat`, `everyone`; a name failing `TextRule` matches nothing and returns no error; order `(channel_id, identity)`.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test -count=1 ./internal/core/access/ ; go test -count=1 -p 1 -run TestACLRowsFor ./internal/conformance/store/`
Expected: FAIL to compile (the package and `ACLRowsFor` do not exist).

- [ ] **Step 3: Implement** the Store query (one `sqlstore` implementation; `go vet ./...` finds any other `store.Tx` implementer, including the suites' fault wrappers), then the access package: `Resolve` as today's `RequireIdentity` (`authz.go:53-71`), `inScope` and `decide` as the table, `Check` loading facts (`tx.Channel` + `tx.ACL`; `tx.Session`; `tx.Identity`; nothing for `Server`), `Channels` as `ListChannels` + `ACLRowsFor(p.Name, everyone[, admins])` grouped by channel + `decide`, `Identities` as `ListIdentities` + `decide`, `Holders` as `ListIdentities` x `Check` (rows loaded once with `tx.ACL`). Denials are never retryable. Do not touch any caller yet.

- [ ] **Step 4: Run**

Run: `make lint && make test && make test-conformance && bash scripts/m4-exit-checks.sh --deps-only && bash scripts/m7-exit-checks.sh --deps-only`
Expected: PASS; both engines; the new package has no import outside `internal/store` and `internal/bus`.

- [ ] **Step 5: Commit**

```bash
git add internal/core/access internal/store/channels.go internal/store/doc.go internal/store/sqlstore/acl.go internal/conformance/store/acl_test.go
git commit -m "feat(core): internal/core/access, the one access check (scope gate, no implicit creator); Tx.ACLRowsFor fact query"
```

---

### Task 2: The channel-shaped callers move to `Access`; the implicit creator entry is retired; `core.Authorize` is deleted

**Files:**
- Modify: `internal/core/core.go` (`Access access.Checker` on `Core`, built in `New` from `cfg.ChannelCreation`), `internal/core/authz.go` (delete `Authorize`; keep the rest for Tasks 3 and 5), `internal/core/channels/service.go` (`mutate`/`read` call `access.Resolve` and pass the `Principal` to `fn`), `internal/core/channels/channels.go` (`Create`: `Access.Require(p, Server{}, CreateChannel)` unconditionally, the `ChannelCreation` test moving into `decide` (Decision 93); `scope := p.Scope`; then `Require(p, Server{}, Administer, WithText("only an administrator may name a scope"))` when `in.Scope != ""`; `adminChannel`: `Require(p, Channel{ch.ID}, Admin)`; `SetACL` on a bridged channel: `Require(p, Server{}, Administer, WithText("only a server administrator may change the ACL of a bridged channel"))`), `internal/core/channels/channels.go` `List` (`Access.Channels(p, List)` filtering `ListChannels`; the interim `List` clause of spec 3.2 lists every channel for an unscoped principal, so only a scoped principal's list changes now), `internal/core/channels/subscriptions.go` (`Subscribe`: `Require(p, Channel{ch.ID}, Deliver)`), `internal/core/messaging/send.go`, `history.go`, `internal/core/memories/memories.go` (4 sites), `internal/core/tasks/create.go`, `read.go` (2), `update.go` (1 `Authorize`; the owner guard stays until Task 8), `internal/core/search/service.go` (`:63` point form `Read`; `:68` `Access.Channels(p, Read)`), `internal/core/sessions/open.go` (`:187`, `:221`: `Check(p, Channel{id}, Deliver).Allowed` in place of catching `forbidden`)
- Test: `internal/conformance/bus/channels_test.go` (`TestChannelCreateWithKindAndACL` `:82` and `:197`: the creator with no stored `admin` row is now `forbidden` and a server administrator succeeds; new `TestNoImplicitCreatorEntry` and `TestScopeGateChannels`), `internal/conformance/bus/bridgeguard_test.go:132` (the creator sets the ACL through its stored row; add a leg where the row was removed and the creator is `forbidden`), `internal/conformance/bus/retire_test.go` `TestScopeRetirementScrub` (`:371`: its fixture has `job1-coder` subscribe, claim and send on the unscoped channels `ops` and `backlog` created by `pat`; under the scope gate those are `forbidden`, so `h.admin("root")` creates them with `Scope: "job1"`; the test's assertions (subscriptions, claims and ACL rows scrubbed, messages kept) are unchanged), `internal/core/authz_test.go` (delete `TestAuthorizeUsesImplicitEntries`, `TestAuthorizeRefusesReservedNames`, `TestAuthorizeUnknownPermissionPanics`)

**Interfaces:**
- Consumes: Task 1.
- Produces: no new API. Every `RequireIdentity` in the files above becomes `access.Resolve` returning a `Principal` that the site then passes to `Require`; `who.Kind` (send, tasks, memories) reads `p.Kind`.

- [ ] **Step 1: Write the failing tests.** `TestNoImplicitCreatorEntry` under `dbtest.ForEachEngine`: `pat` creates `c` with `acl: []`; `pat.SetACL`, `RenameChannel`, `DeleteChannel`, `ACL` are `forbidden`; `pat.ListChannels` lacks `c`... (**not yet**: `List` is still Decision 94 until the visibility rebuild; assert only the four `forbidden`); `h.admin("root")` succeeds at `SetACL` granting `pat` `[read,write,admin]`; `pat` then administers. `TestScopeGateChannels`: scope `job1`, scoped identity `job1-coder` (`h.tx` `CreateScope`, `CreateScopedIdentity`; pattern `scopes_test.go:131`), channel `work` created by `job1-coder` (takes scope `job1`, default ACL) and channel `other` unscoped with the default ACL; as `job1-coder`: `Send`/`History`/`Subscribe`/`Unsubscribe`/`ACL`/`SetACL`/`RenameChannel`/`DeleteChannel` on `work` answer as the ACL says; on `other` and on `general` each is `not_found` with exactly the text `channel "other" does not exist` (byte-equal to the answer for a name that was never created: assert both in the same test, Eric's decision 4.1 (d)); `Search` with `Channel: "other"` is `not_found`; `ListChannels` is exactly `[work]` plus the channel it creates next; `SetACL` on `work` naming `pat` (unscoped) is `validation` `ACL identity "pat" is not a registered identity` (4.1 (b), (d)) while `root` naming `pat` on `work` succeeds and the row is inert for `pat`; a task on a `job1` task list with `owner: pat` is `not_found` `identity "pat" is not registered`; `CreateChannel` with `Name: "other"` (an existing out-of-scope name) is `conflict` until Task 11 makes it `validation` (no `job1-` prefix); the test asserts `conflict` here with a comment naming Task 11, which flips it; `CreateChannel` with `Scope: "job1"` is `forbidden` (`only an administrator may name a scope`), without a scope succeeds and the result's `Scope` is `job1`; as `pat` (unscoped) every one of those succeeds as today and `pat.ListChannels` lists every channel (Decision 94, interim). `TestCreateScopeNamingText`: a non-admin naming a scope gets `forbidden` with exactly `only an administrator may name a scope`; an admin naming an unknown scope gets `not_found` (pins).

- [ ] **Step 2: Run them to verify they fail**

Run: `go test -count=1 -p 1 -run 'TestNoImplicitCreatorEntry|TestScopeGateChannels|TestCreateScopeNamingText' ./internal/conformance/bus/`
Expected: `TestNoImplicitCreatorEntry` FAIL (the creator still administers), `TestScopeGateChannels` FAIL (`other` and `general` are readable today), `TestCreateScopeNamingText` PASS.

- [ ] **Step 3: Rewrite the call sites**, delete `Authorize`, rewrite the three existing tests of spec 3.13, delete the three `authz_test.go` tests. `grep -rn 'Authorize(' internal --include='*.go'` must print nothing.

- [ ] **Step 4: Run**

Run: `make lint && make test && make test-conformance && go test -count=1 -p 1 -v -run 'TestACLMatrix' ./internal/conformance/bus/ > /tmp/m3.log 2>&1; bash scripts/m3-exit-counts.sh /tmp/m3.log`
Expected: PASS; `matrix_sqlite=52 matrix_postgres=52 fail=0`.

- [x] **Task 2 deviations (as implemented).**
  - `TestACLMatrix` "set the ACL" success leg reads the ACL back as a server administrator (`h.admin("root")`) instead of `pat`: the leg replaces the ACL, the creator's stored row goes with it, and the implicit creator entry is retired. No row, count or `section46Rows` content changed (52 leaves per engine).
  - `TestScopeRetirementScrub` keeps `ops` and `backlog` unscoped (making them `job1` channels would change what retirement does); the coder's footprint is seeded past the Bus: a raw subscription, the task assigned by `pat` (owner and `in_progress`), and a message through the Store. Assertions unchanged.
  - Fixtures that subscribed an agent to a memory channel (TUI `sam`, two `mcpserver` tests, `TestSkillDeliveryClaims`) seed the row through `internal/storetest.SubscribeRaw`; the TUI test identities are humans except `sam`.
  - The scope gate runs before every kind check (send, task create/list, task by id, memory edit/delete); in-scope precedence changed accordingly (a caller with no write on a task list gets `forbidden`, not `validation`).
  - `access.WithNotFound` words an out-of-scope denial as the operation's unknown-id text; `Unsubscribe` uses `List` as a scope-only gate (see the visibility plan).

- [ ] **Step 5: Commit**

```bash
git add internal/core/core.go internal/core/authz.go internal/core/authz_test.go internal/core/channels internal/core/messaging internal/core/memories internal/core/tasks internal/core/search internal/core/sessions/open.go internal/conformance/bus/channels_test.go internal/conformance/bus/bridgeguard_test.go
git commit -m "refactor(core): channel permissions and the scope gate through access.Checker; retire the implicit creator entry; delete core.Authorize"
```

---

### Task 3: Delivery and sessions: the `Deliver` set form, the memory filter on the server, session ownership

**Files:**
- Modify: `internal/core/delivery/receive.go` (`load`: `readable` filled from `Access.Channels(p, Deliver)`; the anchor line `if !readable[sub.ChannelID] {` stays exactly once; `receive` resolves `p := access.Resolve(ctx, tx, identity)`, the caller's identity, once and before the session lookup, so a deleted row is `not_registered` first; `Session/Use` then compares the session row's identity to `p.Name`), `peek.go`, `peek_counts.go` (`:29` through `Require(p, Session{in.Session}, Use)`), `watch.go` (`:100` likewise), `internal/core/sessions/service.go` (`Require(ctx, tx, identity, sessionID)` resolves `p` from the caller's `identity`, calls `Access.Require(p, Session{sessionID}, Use)`, which loads the row and compares its identity to `p.Name`, then keeps the closed/lapsed state checks), `open.go` (`:82` resume), `close.go` (`:30`), `internal/core/authz.go` (delete `ReadableChannelIDs`)
- Test: `internal/conformance/bus/` (`TestReceiveAfterIdentityDeleted`, `TestSessionNotOwnedIsNotFound`, `TestAgentNotDeliveredMemory`, `TestScopeGateDelivery`), `internal/core/authz_test.go` (delete `TestReadableChannelIDsRefusesReservedNames` and the readable half of `TestChannelByNameAndReadable`)

**Interfaces:**
- Consumes: Task 1 `Channels(p, Deliver)`, `Require(Session, Use)`.
- Produces: `sessions.Service.Require(ctx, tx, identity, sessionID)` keeps its signature and its `not_found`/`session_closed` codes; the `not_found` for a foreign session now comes from `Access`.

- [ ] **Step 1: Write the failing tests** under `dbtest.ForEachEngine`. `TestReceiveAfterIdentityDeleted`: open a session as `kim`, retire `kim` through a scope (`RetireIdentity` via `h.tx`, pattern `scopes_test.go`), then `Receive`, `Peek`, `Watch` answer `not_registered`. `TestSessionNotOwnedIsNotFound`: one call each of `Receive`, `Peek`, `Watch`, `CloseSession`, `OpenSession` with a foreign resume token answers `not_found` `session %q does not exist` (pins). `TestAgentNotDeliveredMemory`: `mem` (memory kind, default ACL) and `chat` (ordinary); agent `kim` is subscribed to both through `h.tx` (`Subscribe`), human `hal` likewise; a memory is sent on `mem` and a message on `chat`; `kim`'s `Receive` carries only the `chat` message and its `Peek` counts 1; `hal`'s `Receive` carries both and counts 2; `kim.Subscribe(mem)` is `validation` with today's text; `kim.Search` finds the memory. `TestScopeGateDelivery`: `job1-coder` subscribed (through `h.tx`) to `work` (scope `job1`) and to `other` (unscoped), messages on both; its `Receive` carries only `work`'s, `Peek` counts 1, `Watch` wakes on `work` only; its `Search` without a channel finds `work`'s message only; a tag subscription of `job1-coder` matches only on `work`; `pat` (unscoped, same subscriptions) receives both.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test -count=1 -p 1 -run 'TestReceiveAfterIdentityDeleted|TestSessionNotOwnedIsNotFound|TestAgentNotDeliveredMemory|TestScopeGateDelivery' ./internal/conformance/bus/`
Expected: the first, third and fourth FAIL (today: the receive proceeds; the memory is delivered; `other` is delivered); the second PASS.

- [ ] **Step 3: Rewrite** the sites; delete `ReadableChannelIDs` and its tests. `grep -rn 'ReadableChannelIDs\|Identity != identity' internal/core --include='*.go' | grep -v _test` must print nothing.

- [ ] **Step 4: Run**

Run: `make lint && make test && make test-conformance && grep -c 'if !readable\[sub.ChannelID\] {' internal/core/delivery/receive.go`
Expected: PASS; the grep prints `1`. (`internal/mcpserver` and `internal/cli` tests that assert the client-side memory filter still pass: the server now filters too.)

- [x] **Task 3 deviations (as implemented).**
  - `sessions.Service` gains `Owned(ctx, tx, p, id)` (the `Session/Use` check plus the row) and `RequireAs(p, id)`; `Require(identity, id)` resolves the Principal and calls it. Receive, Peek, Watch's check, Close and `messaging.Send` (a fifth ownership copy the plan did not list) use them. `Open`, `EnsureHinted`, `resume`, `offered` and `pending` take the already-resolved Principal.
  - Open with another identity's resume token keeps its own text (`resume token does not name a session of %q`), by `Check` then that text; the plan's "`session %q does not exist`" for it was not today's behavior. Peek and Close of a never-existing session used to answer the store text `session by id`; they now answer `session %q does not exist`, byte-equal to a foreign session.
  - A `Receive` channel filter naming a channel outside the scope is `not_found` as for an unknown name (`List` as the scope-only gate); before, it was accepted silently.
  - Client tests that read memories through the Bus as an agent (`internal/cli/memory_test.go`, `skill_test.go`, `internal/mcpserver/memory_test.go`) now expect none, the server filtering; `TestFilteredWakeHasNoCount` became `TestServerFilteredWakeIsComplete` (the wake count is true). Task 4 removes the client filter and its tests.

- [ ] **Step 5: Commit**

```bash
git add internal/core/delivery internal/core/sessions internal/core/authz.go internal/core/authz_test.go internal/conformance/bus/
git commit -m "refactor(core): delivery (Deliver: no memory channels for agents), scope-gated sets and session ownership through access.Checker; delete ReadableChannelIDs"
```

---

### Task 4: Delete the client-side memory filter (`internal/agentview`)

**Files:**
- Delete: `internal/agentview/kinds.go`, `kinds_test.go`
- Modify: `internal/mcpserver/server.go` (`:545-549` the `subscribe` pre-check goes, the server answers; `:868-875` the `register` memory-unsubscribe sweep goes: a memory subscription of an agent is inert, the server never delivers it; the `kinds` member goes), `internal/cli/channels.go:238-242`, `internal/cli/receive.go`, `internal/cli/stophook.go:148-152`, `internal/cli/wait.go` (the `KindIndex` and every `IsMemory` call go; the Stop hook counts what `Peek` returns, `wait` wakes on what `Watch` returns, `receive -session` prints what `Receive` returns), `CLAUDE.md` (the "Agents never receive memory channels (decision 20)" Design bullet: the rule is the `Deliver` action of `internal/core/access`; the Bus and the TUI are unfiltered because the TUI's identity is human)
- Test: `internal/mcpserver/server_test.go` and `internal/cli/*_test.go` (tests that assert the client refused or filtered keep their assertions where the server now does it with the same code and text; tests that count `ListChannels` calls of the index go)

- [ ] **Step 1: Delete the package first** and run `go vet ./...`: every caller is a compile error and is listed. Expected: the six files above.
- [ ] **Step 2: Rewrite the callers and their tests.** `grep -rn 'agentview\|IsMemory\|KindIndex' internal --include='*.go'` prints nothing; `grep -rn 'KindMemory' internal/mcpserver internal/cli --include='*.go' | grep -v _test` prints only `toKind` (`server.go:390`, the input validator) and the CLI `-kind` flag parser.
- [ ] **Step 3: Run** `make lint && make test && make test-conformance && bash scripts/m6-exit-checks.sh --deps-only`. Expected: PASS; the e2e counts unchanged (the e2e test exercises the Stop hook and `wait` against a real server).
- [ ] **Step 4: Commit**

```bash
git rm -r internal/agentview
git add internal/mcpserver/server.go internal/mcpserver/server_test.go internal/cli CLAUDE.md
git commit -m "refactor(cli,mcpserver): drop the client-side memory filter; the server's Deliver rule is the one rule (decision 20 inside access)"
```

---

### Task 5: Server administration, the health tier, the `Identities` listing, the anchor move; `authz.go` is deleted

**Files:**
- Modify: `internal/core/channels/admin.go` (`Status`), `usage.go`, `internal/core/scopes/service.go` (4 sites), `internal/core/identities/admin.go` (`:114`, `:181`; `MakeAdmin` and `AddReference` to the `admins` list clear a scoped identity's scope instead of refusing (spec 4.3); `CreateScopedIdentity` for an administrator and any scope assignment to one fail `validation` "an administrator cannot be scoped"), `human.go` (`:107`), `agent.go` (`:72`), `internal/core/health/health.go` (`:60-70`: `p := access.Anonymous` for `""`, else `Resolve`; `tier` from `Access.Check(p, Server{}, Administer).Allowed`; `:111` reads the tier), `internal/core/sessions/identities.go` (`Access.Identities(p)` in place of `ListIdentities` (4.1 (c))), `internal/slackbridge/service.go` (`requireAdmin` through `Access.Require(p, Server{}, Administer)`), every remaining `RequireIdentity` caller (`grep -rn 'RequireIdentity(' internal --include='*.go'`), `scripts/check-gates-selftest.sh` (block 15: file `internal/core/access/decide.go`, anchor `if !p.Admin {`; the block's comment names the new home), `docs/superpowers/plans/2026-10-25-health.md` and `2026-10-25-channel-visibility.md` (a one-line note under Global Constraints: "the `RequireAdmin` anchor moved to `internal/core/access/decide.go` (accesscheck plan Task 5)")
- Delete: `internal/core/authz.go`, `internal/core/authz_test.go` (`ChannelByName` moves unchanged to `internal/core/lookup.go`)
- Test: `internal/conformance/bus/` (`TestScopeGateServer`), `internal/core/health/health_test.go` (`TestHealthTierFromAccess`), `internal/core/identities/service_test.go` (`MakeAdmin` on a scoped identity), `internal/store` identity scan normalization + `ClearScopeOfAdmins` (`internal/store/identities.go`, `sqlstore`) and its maintenance call (`internal/core/maintenance`), per spec 4.3

- [ ] **Step 1: Write the failing tests.** `TestScopeGateServer`: `job1-coder` calls `Status`, `Usage`, `ListScopes`, `CreateAgentIdentity`, `ListBridges`, `Reset`, `GetBridge("other")`: each `forbidden` `administrator privilege required`, byte-equal to `pat`'s (an unscoped non-administrator) answers (4.1 (a), (d): nothing depends on what exists); a store row holding admin and a scope reads back (every read path) as admin with no scope and an error logged (spec 4.3), so the row is then an ordinary administrator; its `Identities` lists `job1-coder` and any other `job1` identity, never `pat`; `pat`'s `Identities` lists everyone. `TestMakeAdminClearsScope` in the identities tests: `MakeAdmin` and `AddReference` to the `admins` list clear the scope; creating a scoped identity that is an administrator is `validation`; `TestClearScopeOfAdmins` (store suite, both engines: repairs, logs each name, a second run changes nothing, two concurrent runs leave one consistent result) and the maintenance job that calls it under the lock. `TestHealthTierFromAccess`: admin, demoted, `""` tiers (pins).
- [ ] **Step 2: Run** `go test -count=1 -p 1 -run 'TestScopeGateServer' ./internal/conformance/bus/ ; go test -count=1 -run 'TestMakeAdminClearsScope|TestHealthTierFromAccess' ./internal/core/...`. Expected: the first two FAIL, the third PASS.
- [ ] **Step 3: Rewrite** the sites, move the anchor, edit block 15, delete `authz.go`. `grep -rn 'RequireAdmin\|RequireIdentity' internal --include='*.go'` must print nothing; `grep -c 'if !p.Admin {' internal/core/access/decide.go` prints `1`; `grep -rn 'if !id.Admin' internal` prints nothing.
- [ ] **Step 4: Run**

Run: `make verify && make check-gates-selftest 2>&1 | tail -n 20`
Expected: `verify` PASS on both engines and both transports; the self-test reports every block ok, block `admin-flag-ignored` included (its log shows `--- FAIL: TestAuthEndToEnd/sqlite/admin-refused-as-non-admin` under the mutation).

- [ ] **Step 5: Commit**

```bash
git add internal/core/channels/admin.go internal/core/channels/usage.go internal/core/scopes/service.go internal/core/identities internal/core/health internal/core/sessions/identities.go internal/slackbridge/service.go internal/core/lookup.go internal/core/tasks internal/core/memories internal/core/search internal/core/messaging internal/core/sessions internal/conformance/bus/ scripts/check-gates-selftest.sh docs/superpowers/plans/2026-10-25-health.md docs/superpowers/plans/2026-10-25-channel-visibility.md
git rm internal/core/authz.go internal/core/authz_test.go
git commit -m "refactor(core): server administration, the health tier and the identities listing through access.Checker; delete authz.go; move the admin gate anchor"
```

---

### Task 6: The Store carries no rule: delete `EffectivePermissions` and `ReadableChannels`

**Files:**
- Modify: `internal/store/channels.go` (remove the two methods and their comments), `internal/store/sqlstore/acl.go` (remove `isAdmin`, `EffectivePermissions`, `ReadableChannels`; `badText` stays for the other queries), `internal/store/doc.go`, `docs/adr/0004-store-contract-and-storage.md` (addendum "Access facts, not rules (2026-10-25)": what was removed, `ACLRowsFor`, why: Eric's decision accesscheck 1 and his answer to question 9)
- Test: `internal/conformance/store/acl_test.go` (`TestACLReadableChannels` deleted; `TestACLImplicitEntriesGrantAdmin` and `TestACLRemovingAdminsAndCreatorRowsKeepsImplicitAdmin` deleted (the rule they asserted is retired and the replacement is asserted in `decide_test.go`); `TestACLDuplicateEntriesAreAdditive` keeps its stored-row assertions through `tx.ACL`), `channels_test.go:183` (`ACLRowsFor("pat")` in place of `ReadableChannels`), `text_test.go:296-323` (a malformed name: `ACLRowsFor(bad)` returns no rows and no error)

- [ ] **Step 1: Delete the methods first** and run `go vet ./...`: every remaining caller is a compile error and is listed. Expected: only the test files above.
- [ ] **Step 2: Rewrite the tests**, write the ADR addendum.
- [ ] **Step 3: Run**

Run: `make verify && grep -rn 'is_admin = TRUE\|can_read = TRUE\|can_write = TRUE\|can_admin = TRUE\|creator = ?' internal/store/sqlstore/ ; echo "exit=$?"`
Expected: `verify` PASS; the grep prints nothing and `exit=1`.

- [ ] **Step 4: Commit**

```bash
git add internal/store/channels.go internal/store/sqlstore/acl.go internal/store/doc.go internal/conformance/store/acl_test.go internal/conformance/store/channels_test.go internal/conformance/store/text_test.go docs/adr/0004-store-contract-and-storage.md
git commit -m "refactor(store): drop EffectivePermissions and ReadableChannels; the Store serves ACL facts (ADR 0004 addendum)"
```

---

### Task 7: The Slack bridge: `Post` and `Holders` replace the stored-row scans; bridge-not-configured is `unavailable`

**Files:**
- Modify: `internal/slackbridge/inbound.go` (`trust`: `Resolve(identity)`; `Access.Check(p, Channel{b.ChannelID}, Post)`; `Reason == "not_human"` raises `reasonNotHuman`, `"no_write"` raises `reasonNoWrite`, `"out_of_scope"` cannot occur (humans are unscoped) and is treated as `refused`; `dropReason` and the drop log unchanged; the Slack binding checks stay), `internal/slackbridge/create.go` (`writeHumans` takes the channel id and calls `Access.Holders(Channel{id}, Post)`, then the email binding as today; step 1 still reads `acl` for `CheckBridgeACL`), `internal/bus/bridges.go` (the comment: `unavailable`), `internal/bus/admin.go:43`, `internal/bus/local/scopes.go:40` (`ab2bus.Errorf(ab2bus.CodeUnavailable, false, ...)`), `internal/server/bridges.go:32` (same; `core.E` would mark `unavailable` retryable, so build the error with `bus.Errorf(..., false, ...)`), `api/openapi.yaml` only if the bridge operations document a 403 with that text (then 503; a doc-only patch change under the protocol-versioning decision; `make generate`), `docs/ops/slack-bridge.md:3`, `docs/ops/admin-bootstrap.md:80`, `CLAUDE.md:24, 56`, `internal/cli/bridges.go:36` comment
- Test: `internal/conformance/bus/scopes_wire_test.go:174` (`unavailable`, `Retryable == false`, same text), `internal/slackbridge/inbound_test.go`, `create_test.go` (existing drop-reason and invite tests pin the texts; add `TestTrustThroughAccess`: a human with `write` through two additive rows posts; an agent with `write` is dropped `not a human identity`; an administrator human with no stored `write` row is dropped `no explicit write permission` (the implicit administrator right is `admin`, not `write`))

- [ ] **Step 1: Write the failing tests**; run `go test -count=1 -run 'TestTrustThroughAccess' ./internal/slackbridge/ ; go test -count=1 -p 1 -run 'TestBridgesNotConfigured' ./internal/conformance/bus/` (the `scopes_wire_test.go:174` test's name; look it up). Expected: the wire test FAIL (`forbidden` today); `TestTrustThroughAccess` PASS or FAIL depending on whether the additive-rows case differs; either way it pins.
- [ ] **Step 2: Rewrite.** `grep -n 'tx.ACL(' internal/slackbridge/*.go | grep -v _test` prints only `archive.go` (`Remove`) and `create.go` step 1 and step 4 (the shape check and the pin); `grep -rn 'BridgeNotConfigured' internal docs CLAUDE.md | grep -i forbidden` prints nothing. No client retries on `unavailable` by code alone (checked: `internal/bus/remote/remote.go:64` copies the problem's `retryable` flag through; the TUI's `errText` only renders it; the CLI exits 1), so `retryable: false` reaches the user as `agentbus2: unavailable: the Slack bridge is not configured` and nothing spins; add `TestBridgesNotConfiguredNotRetryable` on the inproc transport asserting `Retryable == false` after the round trip.
- [ ] **Step 3: Run** `make lint && make test && make test-conformance && bash scripts/m7-exit-checks.sh --deps-only` (plus `make generate && make generate-check` if `openapi.yaml` changed). Expected: PASS; the Slack import rule holds.
- [ ] **Step 4: Commit**

```bash
git add internal/slackbridge internal/bus/bridges.go internal/bus/admin.go internal/bus/local/scopes.go internal/server/bridges.go internal/conformance/bus/scopes_wire_test.go internal/cli/bridges.go docs/ops/slack-bridge.md docs/ops/admin-bootstrap.md CLAUDE.md
git commit -m "refactor(slackbridge): who may post and who is invited through access.Checker; bridge not configured is unavailable (Eric 2026-10-25)"
```

---

### Task 8: Remove the task owner guard and the `force` member (spec 3.14)

**Files:**
- Modify: `internal/core/tasks/update.go` (delete `ownerGuardBlocked`, rules 2 and 3, the `forced` revision branch `:299-313` and `:336-337`; `applyPatch` loses the `as`-based guard but keeps rule 6 (owner registered), rule 7 (status transitions; `completed` sets the owner to the caller when empty), "an in_progress task needs an owner"), `doc.go` comments, `internal/bus/tasks.go:76` (`Force` removed from `TaskPatch`), `api/openapi.yaml:1773` (`force` removed from the task update body) then `make generate`, `internal/bus/remote/tasks.go:57-58`, `internal/server/tasks.go:107`, `internal/mcpserver/inputs.go:197`, `internal/mcpserver/server.go:724, 731` (the `task_update` description: "Any identity with write on the task list may change or delete any task on it (Eric's decision 2026-10-25); owner=<name> assigns the task to a registered identity (keeping its status) ..."), `internal/cli/tasks.go:37, 67, 174` (the `-force` flag), `internal/cli/skills/using-agentbus2/SKILL.md:143, 162` (one sentence: any writer may change any task; claim before working so others see the owner), `docs/` (`rg -n -i '\bforce\b|forced' docs` after the edit prints nothing about tasks), `CHANGELOG.md` or the release notes file if one exists (the wire change: `force` removed from the task update body; `2.0.0-dev`, no release shipped)
- Test: `internal/conformance/bus/tasks_test.go` (the four `force`/`conflict` assertions become success assertions: a non-owner updates an `in_progress` task, releases it, deletes it; no `forced` revision is written), `internal/server/memories_tasks_test.go` (4), `internal/core/tasks/doc_test.go` (2), `internal/cli/tasks_test.go` (1); `TestTaskOwnerRegistryCheck`, `TestTaskAbandonedByDeadOwner` unchanged

- [ ] **Step 1: Write the failing test** `TestAnyWriterChangesAnyTask` in `internal/conformance/bus/tasks_test.go`: `kim` claims a task; `pat` (write on the list, not the owner, no force) sets its status to `completed`, then creates and `kim` claims another and `pat` deletes it; both succeed; the revision list holds no `forced` type. Run: `go test -count=1 -p 1 -run TestAnyWriterChangesAnyTask ./internal/conformance/bus/`. Expected: FAIL (`conflict` today).
- [ ] **Step 2: Remove every trace**, `make generate`. `rg -n -i '\bforce\b|forced|ownerGuard' internal api docs CHANGELOG.md -g '!internal/gen/**'` prints only `internal/cli/install.go:225`, `internal/cli/init.go:21-22`, `internal/slackapi/slackapi.go:114`, `internal/slackapi/slackgo/client.go:159` and their tests (unrelated uses of the word).
- [ ] **Step 3: Run** `make generate-check && make verify && bash scripts/m6-exit-checks.sh --deps-only`. Expected: PASS; the ACL matrix's `task create, claim, release, update` row unchanged (it tests `write`, not ownership); the e2e counts unchanged.
- [ ] **Step 4: Commit**

```bash
git add internal/core/tasks internal/bus/tasks.go api/openapi.yaml internal/gen internal/bus/remote/tasks.go internal/server/tasks.go internal/server/memories_tasks_test.go internal/mcpserver/inputs.go internal/mcpserver/server.go internal/cli/tasks.go internal/cli/tasks_test.go internal/cli/skills/using-agentbus2/SKILL.md internal/conformance/bus/tasks_test.go
git commit -m "feat(tasks)!: remove the owner guard and the force member; write on a task list permits changing any task (Eric 2026-10-25)"
```

---

### Task 9: The TUI reacts to server codes (no client-side access decisions)

**Files:**
- Modify: `internal/tui/model.go` (`:1363-1394` `statusCmd`: always call `Status`; `msg.admin = err == nil`; a `forbidden` is not a toast (the comment's reason changes: the server decided); `msg.status, msg.statusOK` as today), `internal/tui/tasks.go` (`:55-58` `ownedToast`, `:66-68` `taskEditable`, `:95`, `:193-197` deleted; the status cycle and claim send the patch and show the server's error text as the toast, as `staleTaskToast` is shown today)
- Test: `internal/tui/*_test.go` (tests asserting `ownedToast` or the admin pre-check are rewritten: a `forbidden` `Status` leaves `admin` false and shows no toast; a task action on another's task is sent and succeeds against the fake Bus; the Milestone 8 counts `TestBothModes` (2), `TestNoInboxView` (1), `TestRunTUI{Local,Server}Profile` (2) stay)

- [ ] **Step 1: Confirm the pins**: `go test -count=1 ./internal/tui/` PASS before the change; list the tests that mention `ownedToast`, `taskEditable` or `admin` gating.
- [ ] **Step 2: Rewrite**; `grep -n 'taskEditable\|ownedToast\|id.Admin' internal/tui/*.go` prints nothing (`IdentityInfo.Admin` may still be displayed; it decides nothing).
- [ ] **Step 3: Run** `make lint && make test && go test -count=1 -v ./internal/tui/ > /tmp/tui.log 2>&1; bash scripts/m8-exit-checks.sh /tmp/tui.log`. Expected: PASS; the m8 counts and the import allowlist hold.
- [ ] **Step 4: Commit**

```bash
git add internal/tui
git commit -m "refactor(tui): react to the server's access codes; drop the client-side admin and task-owner decisions (Eric 2026-10-25)"
```

---

### Task 10: The gate and the records

**Files:**
- Create: `internal/core/access/sites_test.go` (`TestAccessRuleSites`, go/ast, the pattern of `internal/core/identities/putkey_callers_test.go`), `docs/adr/0011-one-access-check.md`
- Modify: `docs/adr/README.md` (row 0011), `docs/adr/0008-channel-access-control-lists.md` (section "Decisions of 2026-10-25 (Eric)": the implicit creator entry retired, the implicit administrator right, the rule's home; "superseded in part" markers, spec section 5), `docs/adr/0009-scopes-and-slack-bridge.md` (the note of spec section 5; decision 19's "messages they sent on other channels stay" is marked moot: under the scope gate a scoped agent never writes outside its scope, and the retained-message rule now applies to its scope's channels that are not retired with it), `docs/ops/upgrade.md` (the implicit-creator change and the operator SQL of spec 3.13), `docs/superpowers/specs/2026-10-19-agentbus2-design.md` section 4.6.1 (one paragraph: the table is evaluated by `internal/core/access`; the implicit creator entry is retired; scope is an access boundary), `CLAUDE.md` (Design bullets: `internal/core/access` is the one access check and the gate; `authz.go` gone; the Store bullet; the anchor's home; the scope bullet), `docs/superpowers/plans/2026-10-25-channel-visibility.md` (a "superseded in part" note at the top of Task 1 and Review Focus 1 pointing at spec 3.10; the rebuilt plan is a separate document written afterwards)

**Interfaces:**
- Produces: `TestAccessRuleSites` with the allowlist as a literal, exactly as spec 3.7 item 4 lists it: `tx.ACL(` callers `internal/core/access`, `internal/core/channels/channels.go ACL`, `internal/slackbridge/archive.go Remove`, `internal/slackbridge/create.go <step 1 and step 4 function names>`; `.Admin`/`.Creator`/`.Owner`/`.Scope` conditions, `Kind == store.Identity*` and `Kind == KindMemory` comparisons allowed only in `internal/core/access`, plus by function name: `internal/core/identities/human.go signInHumanTx`, `register.go registerTx`, `agent.go ensureAgent`, `admin.go resetCredential`, `admin.go MakeAdmin` (the scoped refusal, 4.1 (a)), `internal/core/sessions/identities.go Identities`, `internal/core/convert.go` (converters), `internal/core/channels/channels.go Create` (`scope := p.Scope`, a copy), `internal/core/scopes/service.go RequireOpen`, `internal/core/scopes/retire.go` (state), `internal/core/scopes/acl.go CheckBridgeACL` (ACL shape), `internal/slackbridge/create.go` (the "a bridge requires a scoped channel" validation), `internal/core/messaging/send.go`, `internal/core/memories/memories.go`, `internal/core/tasks/*` (the task-list kind validations), `internal/core/tasks/reclaim.go` (`t.Owner == ""` is state), `internal/core/tasks/read.go taskList` (`in.Owner` is a query filter), `internal/mcpserver/server.go toKind` and the CLI `-kind` parser (input validation). Scope: non-test `.go` files under `internal/core`, `internal/server`, `internal/slackbridge`, `internal/bus/local`, and for the `KindMemory` clause `internal/mcpserver` and `internal/cli`. A second check walks `internal/store/sqlstore` string literals for the five SQL fragments of the Global Constraints.

- [ ] **Step 1: Write the gate test** and run it: `go test -count=1 -run TestAccessRuleSites ./internal/core/access/`. Expected: PASS after Tasks 2 to 9; if it fails, the failure names a site this plan missed: fix the site, never the allowlist, unless the site is listed in spec Appendix A as kind N.
- [ ] **Step 2: Negative check of the gate**: in a throwaway worktree (`git worktree add /tmp/ab2-gate HEAD`), add `if p.Admin { return nil }` to `internal/core/channels/channels.go Delete` (the `Principal` is in scope there after Task 2), run the gate, expect FAIL naming `channels.go Delete`; then add `if ch.Kind == bus.KindMemory { continue }` to the Stop hook's count loop in `internal/cli/stophook.go`, expect FAIL naming it; remove the worktree. Record both outputs in the commit message body.
- [ ] **Step 3: Records**: ADR 0011 (sections 1 and 4 of the spec transcribed as Eric's decisions, the table of spec 3.2 with the scope gate, the denial codes, the gate, the anchor move, the `force` wire change, the protocol-versioning decision as context; status "Accepted (Eric, 2026-10-25)"; the 4.1 sub-items as "Proposed" with the reading implemented), ADR 0008 and 0009 sections, `docs/ops/upgrade.md`, the spec paragraph, `CLAUDE.md`, the visibility plan note.
- [ ] **Step 4: Run**

Run: `make verify && go test -count=1 -p 1 -v ./internal/conformance/bus/ > /tmp/m3.log 2>&1; bash scripts/m3-exit-counts.sh /tmp/m3.log && bash scripts/m4-exit-checks.sh --deps-only && bash scripts/m5-exit-checks.sh --deps-only && bash scripts/m6-exit-checks.sh --deps-only && bash scripts/m7-exit-checks.sh --deps-only && bash scripts/m8-exit-checks.sh --deps-only`
Expected: PASS; `matrix_sqlite=52 matrix_postgres=52`; every import rule holds.

- [ ] **Step 5: Commit**

```bash
git add internal/core/access/sites_test.go docs/adr/0011-one-access-check.md docs/adr/README.md docs/adr/0008-channel-access-control-lists.md docs/adr/0009-scopes-and-slack-bridge.md docs/ops/upgrade.md docs/superpowers/specs/2026-10-19-agentbus2-design.md CLAUDE.md docs/superpowers/plans/2026-10-25-channel-visibility.md
git commit -m "test(access): the no-other-rule gate; ADR 0011 and the ADR 0008 change (Eric 2026-10-25)"
```

---

### Task 11 (approved, spec 4.2): scope-prefixed channel names, so a create or rename collision cannot disclose an out-of-scope name

Eric's decision 2026-10-25 (option A). `TestScopeGateChannels` pins today's `conflict` until this task flips it.

**Files:**
- Modify: `internal/core/channels/channels.go` (`Create`, `Rename`: a scoped principal's name must be `<p.Scope>-<rest>` (`validation`, `a channel of scope %q is named %q-<name>`); any caller's name starting with `<s>-` for an existing scope `s` (open, done, or retired and not yet purged: `tx.Scope` any state) must carry `Scope: s` (`validation`, `names starting with %q- belong to scope %q`)), `internal/core/scopes/service.go` (`Create`: `conflict` `scope %q: the name prefix %q- is in use by channel %q` while any channel of another or no scope is named `<name>-*`; one `ListChannels` scan, channels are few), `docs/ops/admin-bootstrap.md` and the skill text (the naming rule, one sentence each), ADR 0011 (the decision recorded, with Eric's ruling that existing names violating the rule get no migration or compatibility handling: the local stack's channels and scopes are deleted and recreated by hand)
- Test: `internal/conformance/bus/` (`TestScopeNamespace`: `job1-coder` creating `x` is `validation`, creating `job1-x` succeeds; `pat` creating `job1-y` is `validation`; `root` creating `job1-z` with `Scope: job1` succeeds and `k-chat` with `Scope: job1` still succeeds (`TestScopedChannels` unchanged); `job1-coder` creating `job1-z` is `conflict` and `job1-z` is in its list, so nothing leaked; `root` creating scope `job2` while an unscoped `job2-old` exists is `conflict`); `TestScopeGateChannels`'s pinned `conflict` on `other` becomes `validation` (no prefix)

- [ ] **Step 1: Write the failing test**, run, expect FAIL. **Step 2: Implement.** **Step 3: Run** `make lint && make test && make test-conformance && go test -count=1 -p 1 -v -run TestACLMatrix ./internal/conformance/bus/ > /tmp/m3.log 2>&1; bash scripts/m3-exit-counts.sh /tmp/m3.log` (the matrix creates `m<N>` channels as `pat`, unscoped, unaffected). **Step 4: Commit** `feat(channels): scope-prefixed names close the create/rename disclosure (Eric 2026-10-25, spec 4.2)`.

---

## After this plan

The channel-visibility plan is rewritten against the tree this plan leaves: its Task 1 becomes `channels.Service.List` filtering `ListChannels` by `Access.Channels(p, access.List)` (no Store query; "any ACL entry lists the channel" is a stored row, "admins list all" is the implicit administrator right of Eric's answer to question 6, and a scoped identity lists its scope's channels), its Task 2 replaces the `channel_capabilities` discovery capability with the protocol-version bump of Eric's protocol-versioning decision (spec Context section: `info.version`, `protocol_version` on discover and health), and its client tasks stand.
