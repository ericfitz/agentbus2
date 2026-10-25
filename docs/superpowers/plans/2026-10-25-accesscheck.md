# One Access Check Implementation Plan (Eric's decisions of 2026-10-25)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every access decision on the server side (channel permissions, server administration, session ownership, the Slack bridge's "who may post" and "who is invited", the health tier and, per Eric's answer to question 1, task ownership) is made by one function in one package, `internal/core/access`; the Store serves facts and holds no rule; a test proves no other code decides; the ACL matrix, the auth exit test and the e2e code table are unchanged.

**Architecture:** A new package `internal/core/access` holds `Principal`, `Resolve`, the object kinds, the actions and one pure `decide` over the rule table of spec section 3.2, wrapped by `Checker.Check`, `Require` (point form), `Channels` (set form) and `Holders` (reverse set form). The Store gains one policy-free query, `Tx.ACLRowsFor(names...)`, and loses `EffectivePermissions` and `ReadableChannels`. `internal/core/authz.go` is deleted and its 24 call sites, the six self-made decisions and the five session-ownership copies move to `Access`. The `check-gates-selftest.sh` administrator anchor moves with the rule. No schema change, no wire change, no setting.

**Tech Stack:** Go 1.27, modernc SQLite and pgx Postgres (one `sqlstore` implementation), testcontainers for the Postgres suites, go/ast for the gate test.

**Spec:** `docs/superpowers/specs/2026-10-25-accesscheck-design.md`. Section 1 is Eric's decisions and is implemented as written; section 4 lists twelve open questions with the agent's recommendations. **Before Task 1 starts, Eric's answers to questions 1, 3, 9 and 10 are needed** (they change Tasks 5, 7 and 8 and the anchor move of Task 4); until he answers, each task below carries the recommended option and marks the lines that flip. Section numbers below refer to the spec.

## Global Constraints

- Done gate: `make verify` (needs Docker; a run that skips the Postgres suites is not a pass). Work is done only when `ci.yml` and `windows.yml` are green on the pushed commit (including `release-check`, `check-gates-selftest` and `m9-exit-selftest`); a red `windows.yml` is fixed first. Every task runs `make lint && make test && make test-conformance`; Tasks 1, 4, 5 and 8 run `make verify`; Task 4 also runs `make check-gates-selftest`.
- Import rules (CLAUDE.md), re-checked by `bash scripts/m4-exit-checks.sh --deps-only && bash scripts/m5-exit-checks.sh --deps-only && bash scripts/m6-exit-checks.sh --deps-only && bash scripts/m7-exit-checks.sh --deps-only && bash scripts/m8-exit-checks.sh --deps-only` in every task: `internal/core/access` imports only `internal/store` and `internal/bus` of this module (never `internal/core`, `internal/gen`, `internal/server`, `internal/bus/local`, `internal/bus/remote`); `internal/store/...` never imports `internal/core/...`; `internal/server`, `internal/bus/remote`, `internal/mcpserver`, `internal/cli`, `internal/tui` never import `internal/core/access`.
- Pinned counts and anchors that stay: `TestACLMatrix` 15 rows, 26 sub-cases, 52 leaves per engine (`scripts/m3-exit-counts.sh`; never edit `section46Rows`, `matrixSubcases` or a row's content), `TestAuthEndToEnd` 11 leaves per engine, the e2e counts (`TestSection24Sequence` 13, `TestExitCodeTable` 11), the Milestone 8 counts, the MCP tool count 25, the `check-gates-selftest.sh` anchor `if !readable[sub.ChannelID] {` in `internal/core/delivery/receive.go` (block 8: `load` keeps a `readable` set under that name). Counts and anchors that move on purpose, each in the task named: block 15's file and anchor (`internal/core/authz.go` `if !id.Admin {` to `internal/core/access/decide.go` `if !p.Admin {`, Task 4; this supersedes the health plan's and the visibility plan's "never edit `RequireAdmin`"); the store suite loses `TestACLReadableChannels` and the `EffectivePermissions`/`ReadableChannels` halves of `TestACLImplicitEntriesGrantAdmin`, `TestACLDuplicateEntriesAreAdditive`, `TestACLRemovingAdminsAndCreatorRowsKeepsImplicitAdmin`, `TestACLDeleteChannelRemovesEntries`-adjacent `channels_test.go:183` and `text_test.go:296-323` (Task 5) and gains `TestACLRowsFor` (Task 1); `internal/core/authz_test.go` is replaced by the access package's tests (Task 4).
- Error codes do not change for any operation the suites cover: channel and server denials `forbidden`, a reserved or unknown principal `not_registered`, a session not the caller's `not_found`, a task owned by another `conflict`. Denial texts the matrix and e2e tests assert on keep their wording (`"%s needs %s on channel %q"`, `"administrator privilege required"`, `"session %q does not exist"`, `"%q is not a registered identity"`). An undefined (object, action) pair is `internal`, never a panic (Definition of Done B), with a test.
- The rule lives in `internal/core/access/decide.go` and nowhere else: no task adds a conditional on `.Admin`, `.Creator`, `.Owner`, `Kind == store.IdentityHuman`, a `store.Perm*` membership test or a `tx.ACL(` call outside that package except the allowlist of Task 8's gate; no SQL under `sqlstore` filters on `is_admin`, `can_read`, `can_write`, `can_admin` or `creator`.
- Definition of Done D for anything stateful: this plan adds no state; the Store query is read-only.
- Docs move with the code in the task that changes the behavior; Task 8 is the sweep. American English.
- Commits land on `main`, one per task, ending with the session's `Co-Authored-By` and `Claude-Session` lines. No push without Eric. Other agents may be editing this tree: `git add` only the files listed in the task, never `-A`. Never edit `internal/gen` (no task touches `api/openapi.yaml`).

## Review Focus

1. The rule is one function: `decide` is the only place that reads a principal's `Admin`, a channel's `Creator`, a stored row's flags, a session's identity, a task's owner (Task 1; Task 8's `TestAccessRuleSites` proves it over the server-side packages).
2. The set form and the point form agree by test, not by inspection: `TestChannelsAgreesWithCheck` on both engines, every action (Task 1).
3. No code change moves a code or a text the suites assert on: the ACL matrix, `TestAuthEndToEnd`, the e2e table and the slackbridge drop-reason tests pass unchanged (Tasks 2 to 6).
4. The administrator anchor moved with the rule and the self-test proves the moved anchor still fails the auth exit test when mutated (Task 4, `make check-gates-selftest` output in the report).
5. The Store carries no policy after Task 5: `sqlstore/acl.go` has no `is_admin` join, no `everyone` binding for a malformed name, no `can_* = TRUE` filter; `TestACLRowsFor` covers the malformed name (matches nothing).
6. Receive, Peek and Watch resolve the principal once from the session's identity and answer `not_registered` for a deleted identity (Task 3, `TestReceiveAfterIdentityDeleted`), the one intended behavior change (spec 3.9).

---

### Task 1: `internal/core/access` and the fact query `Tx.ACLRowsFor`

**Files:**
- Create: `internal/core/access/doc.go`, `principal.go` (`Principal`, `Anonymous`, `Resolve`), `objects.go` (`Object`, `Channel`, `Server`, `Session`, `Task`, `Action` constants), `decide.go` (`decide`, the table of spec 3.2, the denial codes and texts), `checker.go` (`Checker`, `Config`, `Decision`, `Check`, `Require`, `Channels`, `Holders`, the `WithMessage` option), `decide_test.go`, `checker_test.go` (both engines through `dbtest.ForEachEngine` with a `sqlstore` store opened as `internal/core/authz_test.go` does today)
- Modify: `internal/store/channels.go` (`ACLRow`, `ACLRowsFor` on `Channels`; `EffectivePermissions` and `ReadableChannels` stay until Task 5), `internal/store/sqlstore/acl.go` (`ACLRowsFor`), `internal/store/doc.go` (one sentence)
- Test: `internal/conformance/store/acl_test.go` (`TestACLRowsFor`)

**Interfaces:**
- Consumes: `store.Tx` (`Identity`, `Channel`, `ListChannels`, `ACL`, `Session`, `ListIdentities`), `store.Everyone`, `store.Admins`, `store.ReservedIdentity`, `store.Permissions`, `bus.Errorf`, `bus.Code*`.
- Produces: in `internal/store`: `type ACLRow struct{ ChannelID int64; Identity string; Read, Write, Admin bool }`; `ACLRowsFor(ctx context.Context, names ...string) ([]ACLRow, error)` with the doc comment "ACLRowsFor returns the stored channel_acl rows whose identity is one of names, ordered by (channel_id, identity). It is a fact query: no permission filter, no administrator join, no binding of an unregistered or malformed name (such a name matches nothing). The rule over these rows is internal/core/access." In `internal/core/access`: the API of spec 3.3 verbatim; `Config{ChannelCreation string}` (the `core.Config.ChannelCreation` value, copied in by `core.New` in Task 2); `decide(p Principal, f facts, a Action) (allowed bool, reason denial)` where `facts` is the union type over `channelFacts{Channel store.Channel; Rows []store.ACLEntry}`, `serverFacts{}`, `sessionFacts{store.Session}`, `taskFacts{Owner, Status string; TakesOwner, Force bool}`; `denial` is one of `denyChannel`, `denyServer`, `denySession`, `denyTaskInProgress`, `denyTaskOwned`, `denyNotHuman`, `denyNoWrite`, each carrying its code and text template; `Decision{Allowed bool; Code bus.Code; Message string; Reason string}` (`Reason` is the denial's name so a caller like the bridge can map it to a fixed text without parsing `Message`). The `decide.go` file contains the literal `if !p.Admin {` exactly once, in the `Server`/`Administer` clause.

- [ ] **Step 1: Write the failing tests.** `decide_test.go`: a table over spec 3.2: (a) `Channel/Read` with rows `[{everyone,[read]}]` allowed for a non-admin, with `[{admins,[read]}]` allowed only for an admin, with `[{pat,[write]}]` denied for `pat`; (b) `Channel/Admin` allowed for the creator with no rows, for an admin with no rows, denied for a non-creator non-admin with `[{pat,[read,write]}]`, and `Channel/Read` denied for a creator with no rows (the implicit entry grants `admin` only); (c) `Channel/List` allowed for any row, for the creator, for an admin with no rows, denied otherwise; (d) `Channel/Post` allowed for a human with write, denied `denyNotHuman` for an agent with write, denied `denyNoWrite` for a human without; (e) `Server/Administer` by the flag; `Server/CreateChannel` allowed for anyone with `Config{ChannelCreation: "open"}`, for an admin only with `"admins"`; (f) `Session/Use` by identity equality; (g) `Task/Modify`: unowned allowed; owned by another and `in_progress` denied `denyTaskInProgress`; patch taking another's owner denied `denyTaskOwned`; `Force` allowed; (h) `Anonymous` denied every action with `denyServer` for `Server` and `denyChannel` for channels; (i) `decide(p, serverFacts{}, Read)` returns an `internal` decision with the text `access: action "read" is not defined for server`. `checker_test.go` under `dbtest.ForEachEngine`: `TestResolve` (`""`, `everyone`, `admins`, `slack-bridge`, unknown: `not_registered` with the text `%q is not a registered identity`; a registered human: `Kind` human, `Admin` as set); `TestRequireTexts` (the three texts of the Global Constraints, byte for byte, through `Require` and through `Require` with `WithMessage("only an administrator may name a scope")`); `TestChannelsAgreesWithCheck`: seed `root` (admin), `pat`, `kim`, `hal` (human); 40 channels with random creator in `{"", pat, kim}` and random rows drawn from `{pat, kim, hal, everyone, admins}` x random non-empty permission sets (seeded `math/rand/v2` with the seed logged); for each principal and each of `Read`, `Write`, `Admin`, `List`, `Post`: `Channels(p, a)` equals the ascending ids where `Check(p, Channel{id}, a).Allowed`; `TestHolders`: `Holders(ch, Post)` lists exactly the human identities whose `Check` allows `Post`, never `everyone`/`admins`/`slack-bridge`. `TestACLRowsFor` (store suite): rows for `pat`, `everyone`; a name failing `TextRule` matches nothing and returns no error; order `(channel_id, identity)`.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test -count=1 ./internal/core/access/ ; go test -count=1 -p 1 -run TestACLRowsFor ./internal/conformance/store/`
Expected: FAIL to compile (the package and `ACLRowsFor` do not exist).

- [ ] **Step 3: Implement** the Store query (one `sqlstore` implementation; `go vet ./...` finds any other `store.Tx` implementer, including the suites' fault wrappers), then the access package: `Resolve` as today's `RequireIdentity` (`authz.go:53-71`), `decide` as the table, `Check` loading facts (`tx.Channel` + `tx.ACL`; `tx.Session`; nothing for `Server`), `Channels` as `ListChannels` + `ACLRowsFor(p.Name, everyone[, admins])` grouped by channel + `decide`, `Holders` as `ListIdentities` x `Check` (rows loaded once with `tx.ACL`). Denials are never retryable. Do not touch any caller yet.

- [ ] **Step 4: Run**

Run: `make lint && make test && make test-conformance && bash scripts/m4-exit-checks.sh --deps-only && bash scripts/m7-exit-checks.sh --deps-only`
Expected: PASS; both engines; the new package has no import outside `internal/store` and `internal/bus`.

- [ ] **Step 5: Commit**

```bash
git add internal/core/access internal/store/channels.go internal/store/doc.go internal/store/sqlstore/acl.go internal/conformance/store/acl_test.go
git commit -m "feat(core): internal/core/access, the one access check; Tx.ACLRowsFor fact query"
```

---

### Task 2: The channel-shaped callers move to `Access`; `core.Authorize` is deleted

**Files:**
- Modify: `internal/core/core.go` (`Access access.Checker` on `Core`, built in `New` from `cfg.ChannelCreation`), `internal/core/authz.go` (delete `Authorize`; keep the rest for Tasks 3 and 4), `internal/core/channels/service.go` (`mutate`/`read` call `access.Resolve` and pass the `Principal` to `fn`), `internal/core/channels/channels.go` (`Create`: `Access.Require(p, Server{}, CreateChannel)` unconditionally, the `ChannelCreation` test moving into `decide` (Decision 93), then `Require(p, Server{}, Administer, WithMessage("only an administrator may name a scope"))` when a scope is named; `adminChannel`: `Require(p, Channel{ch.ID}, Admin)`; `SetACL`: `Require(p, Server{}, Administer, WithMessage("only a server administrator may change the ACL of a bridged channel"))`), `internal/core/channels/subscriptions.go` (`Subscribe`), `internal/core/messaging/send.go`, `history.go`, `internal/core/memories/memories.go` (4 sites), `internal/core/tasks/create.go`, `read.go` (2), `update.go` (1), `internal/core/search/service.go` (`:63` point form; `:68` `Access.Channels(p, Read)`), `internal/core/sessions/open.go` (`:187`, `:221`: `Check(...).Allowed` in place of catching `forbidden`)
- Test: `internal/core/authz_test.go` (delete `TestAuthorizeUsesImplicitEntries`, `TestAuthorizeRefusesReservedNames`, `TestAuthorizeUnknownPermissionPanics`; their assertions already live in Task 1's tests), existing aggregate tests

**Interfaces:**
- Consumes: Task 1.
- Produces: no new API. Every `RequireIdentity` in the files above becomes `access.Resolve` returning a `Principal` that the site then passes to `Require`; `who.Kind` (send, tasks, memories) reads `p.Kind`.

- [ ] **Step 1: Write the failing test** `TestCreateScopeNamingText` in `internal/core/channels/channels_test.go`: a non-admin naming a scope gets `forbidden` with exactly `only an administrator may name a scope`; an admin naming an unknown scope gets `not_found`. (Pins the `WithMessage` path before the rewrite.) Run: `go test -count=1 -run TestCreateScopeNamingText ./internal/core/channels/`. Expected: PASS already (it pins behavior); keep it.

- [ ] **Step 2: Rewrite the call sites** listed above, delete `Authorize`, delete the three `authz_test.go` tests. `grep -rn 'Authorize(' internal --include='*.go'` must print nothing.

- [ ] **Step 3: Run**

Run: `make lint && make test && make test-conformance && go test -count=1 -p 1 -v -run 'TestACLMatrix' ./internal/conformance/bus/ > /tmp/m3.log 2>&1; bash scripts/m3-exit-counts.sh /tmp/m3.log`
Expected: PASS; `matrix_sqlite=52 matrix_postgres=52 fail=0`.

- [ ] **Step 4: Commit**

```bash
git add internal/core/core.go internal/core/authz.go internal/core/authz_test.go internal/core/channels internal/core/messaging internal/core/memories internal/core/tasks internal/core/search internal/core/sessions/open.go
git commit -m "refactor(core): channel permissions through access.Checker; delete core.Authorize"
```

---

### Task 3: Delivery and sessions: the set form and session ownership

**Files:**
- Modify: `internal/core/delivery/receive.go` (`load`: `readable` filled from `Access.Channels(p, Read)`; the anchor line `if !readable[sub.ChannelID] {` stays exactly once; `receive` resolves `p := access.Resolve(ctx, tx, identity)`, the caller's identity, once and before the session lookup, so a deleted row is `not_registered` first; `Session/Use` then compares the session row's identity to `p.Name`), `peek.go`, `peek_counts.go` (`:29` session ownership through `Require(p, Session{in.Session}, Use)`), `watch.go` (`:100` likewise), `internal/core/sessions/service.go` (`Require(ctx, tx, identity, sessionID)` resolves `p` from the caller's `identity`, calls `Access.Require(p, Session{sessionID}, Use)`, which loads the row and compares its identity to `p.Name`, then keeps the closed/lapsed state checks), `open.go` (`:82` resume), `close.go` (`:30`), `internal/core/authz.go` (delete `ReadableChannelIDs`)
- Test: `internal/conformance/bus/delivery_test.go` (or the file holding the receive tests): `TestReceiveAfterIdentityDeleted`; `internal/core/authz_test.go` (delete `TestReadableChannelIDsRefusesReservedNames`, `TestChannelByNameAndReadable`'s readable half)

**Interfaces:**
- Consumes: Task 1 `Channels`, `Require(Session, Use)`.
- Produces: `sessions.Service.Require(ctx, tx, identity, sessionID)` keeps its signature (callers unchanged) and its `not_found`/`session_closed` codes; the `not_found` for a foreign session now comes from `Access`.

- [ ] **Step 1: Write the failing test** `TestReceiveAfterIdentityDeleted` under `dbtest.ForEachEngine`: open a session as `kim`, delete the row through `h.tx` (`DELETE FROM identities` is not on the contract; use `RetireIdentity` via a scope, pattern `scopes_test.go`, or `Reset` with scope `all` if it leaves sessions, check which keeps the session row), then `Receive`, `Peek`, `Watch` answer `not_registered`. Also assert the five ownership sites still answer `not_found` with `session %q does not exist` for another identity's session (`TestSessionNotOwnedIsNotFound`, one call each: `Receive`, `Peek`, `Watch`, `CloseSession`, `OpenSession` with a foreign resume token).

- [ ] **Step 2: Run them to verify they fail**

Run: `go test -count=1 -p 1 -run 'TestReceiveAfterIdentityDeleted|TestSessionNotOwnedIsNotFound' ./internal/conformance/bus/`
Expected: `TestReceiveAfterIdentityDeleted` FAIL (today the receive proceeds with `everyone` rows); the ownership test PASS (it pins).

- [ ] **Step 3: Rewrite** the sites; delete `ReadableChannelIDs` and its tests. `grep -rn 'ReadableChannelIDs\|Identity != identity' internal/core --include='*.go' | grep -v _test` must print nothing.

- [ ] **Step 4: Run**

Run: `make lint && make test && make test-conformance && grep -c 'if !readable\[sub.ChannelID\] {' internal/core/delivery/receive.go`
Expected: PASS; the grep prints `1`.

- [ ] **Step 5: Commit**

```bash
git add internal/core/delivery internal/core/sessions internal/core/authz.go internal/core/authz_test.go internal/conformance/bus/
git commit -m "refactor(core): delivery and session ownership through access.Checker; delete ReadableChannelIDs"
```

---

### Task 4: Server administration, the health tier, the anchor move; `authz.go` is deleted

**Files:**
- Modify: `internal/core/channels/admin.go` (`Status`), `usage.go`, `internal/core/scopes/service.go` (4 sites), `internal/core/identities/admin.go` (`:114`, `:181`), `human.go` (`:107`), `agent.go` (`:72`), `internal/core/health/health.go` (`:60-70`: `p := access.Anonymous` for `""`, else `Resolve`; `tier` from `Access.Check(p, Server{}, Administer).Allowed`; `:111` reads the tier, not the flag), `internal/slackbridge/service.go` (`requireAdmin` through `Access.Require(p, Server{}, Administer)`), every remaining `RequireIdentity` caller (`grep -rn 'RequireIdentity(' internal --include='*.go'`: health, tasks `read.go`, memories, search, messaging, sessions `identities.go`, `close.go`, `open.go`), `scripts/check-gates-selftest.sh` (block 15: file `internal/core/access/decide.go`, anchor `if !p.Admin {`; the block's comment names the new home), `docs/superpowers/plans/2026-10-25-health.md` and `2026-10-25-channel-visibility.md` (a one-line note under Global Constraints: "the `RequireAdmin` anchor moved to `internal/core/access/decide.go` (accesscheck plan Task 4)")
- Delete: `internal/core/authz.go`, `internal/core/authz_test.go` (`TestRequireIdentityRejectsReservedAndUnknown` and `TestRequireAdmin` are covered by Task 1's `TestResolve` and `decide_test.go`; `ChannelByName` moves to `internal/core/channels.go` or `core.go` unchanged)

**Interfaces:**
- Consumes: Task 1 `Require(Server, Administer)`, `Anonymous`.
- Produces: `core.ChannelByName` keeps its signature in a new file (`internal/core/lookup.go`). No other API.

- [ ] **Step 1: Write the failing test** `TestHealthTierFromAccess` in `internal/core/health/health_test.go`: an identity made admin through `tx.SetAdmin` gets `TierAdmin` on the next call, demoted gets `TierIdentity`, `""` gets `TierProbe`; and a grep assertion in the same test file is not possible, so add to Task 8's gate instead. Run it; expected PASS (pins).

- [ ] **Step 2: Rewrite** the sites, move the anchor, edit block 15, delete `authz.go`. `grep -rn 'RequireAdmin\|RequireIdentity' internal --include='*.go'` must print nothing; `grep -c 'if !p.Admin {' internal/core/access/decide.go` prints `1`; `grep -rn 'if !id.Admin' internal` prints nothing.

- [ ] **Step 3: Run**

Run: `make verify && make check-gates-selftest 2>&1 | tail -n 20`
Expected: `verify` PASS on both engines and both transports; the self-test reports every block ok, block `admin-flag-ignored` included (its log shows `--- FAIL: TestAuthEndToEnd/sqlite/admin-refused-as-non-admin` under the mutation).

- [ ] **Step 4: Commit**

```bash
git add internal/core/channels/admin.go internal/core/channels/usage.go internal/core/scopes/service.go internal/core/identities internal/core/health internal/slackbridge/service.go internal/core/lookup.go internal/core/tasks internal/core/memories internal/core/search internal/core/messaging internal/core/sessions scripts/check-gates-selftest.sh docs/superpowers/plans/2026-10-25-health.md docs/superpowers/plans/2026-10-25-channel-visibility.md
git rm internal/core/authz.go internal/core/authz_test.go
git commit -m "refactor(core): server administration and the health tier through access.Checker; delete authz.go; move the admin gate anchor"
```

---

### Task 5: The Store carries no rule: delete `EffectivePermissions` and `ReadableChannels` (question 9, recommended A)

**Files:**
- Modify: `internal/store/channels.go` (remove the two methods and their comments), `internal/store/sqlstore/acl.go` (remove `isAdmin`, `EffectivePermissions`, `ReadableChannels`; `badText` stays for the other queries), `internal/store/doc.go`, `docs/adr/0004-store-contract-and-storage.md` (addendum "Access facts, not rules (2026-10-25)": what was removed, `ACLRowsFor`, why: Eric's decision accesscheck 1)
- Test: `internal/conformance/store/acl_test.go` (`TestACLReadableChannels` deleted; `TestACLImplicitEntriesGrantAdmin`, `TestACLDuplicateEntriesAreAdditive`, `TestACLRemovingAdminsAndCreatorRowsKeepsImplicitAdmin` keep their stored-row assertions through `tx.ACL` and drop the `EffectivePermissions` calls, since the rule is asserted in `internal/core/access/decide_test.go`), `channels_test.go:183` (`ACLRowsFor("pat")` in place of `ReadableChannels`), `text_test.go:296-323` (a malformed name: `ACLRowsFor(bad)` returns no rows and no error)

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

### Task 6: The Slack bridge: `Post` and `Holders` replace the stored-row scans

**Files:**
- Modify: `internal/slackbridge/inbound.go` (`trust`: `Resolve(identity)`; `Access.Check(p, Channel{b.ChannelID}, Post)`; a denial with `Reason == "not_human"` raises `reasonNotHuman`, `"no_write"` raises `reasonNoWrite`, so `dropReason` and the drop log are unchanged; the Slack binding checks stay), `internal/slackbridge/create.go` (`writeHumans` takes the channel id and calls `Access.Holders(Channel{id}, Post)`, then the email binding as today; the `acl` it received is still read by step 1 for `CheckBridgeACL`)
- Test: `internal/slackbridge/inbound_test.go` and `create_test.go` (existing drop-reason and invite tests pin the texts; add `TestTrustThroughAccess`: a human with `write` through two additive rows posts; an agent with `write` is dropped `not a human identity`; a human whose only grant is `everyone` `write` is impossible on a bridged channel by Decision 12, so assert `CheckBridgeACL` still refuses it)

- [ ] **Step 1: Write the failing test**, run `go test -count=1 -run TestTrustThroughAccess ./internal/slackbridge/`. Expected: FAIL (the additive-rows case: today's scan requires one row with `write`; two rows `[{hal,[read]},{hal,[write]}]` pass the scan too, so pick the case that differs: a human with `write` and an admin human with no stored write both decided by `Check`; if no observable difference exists, the test pins and Step 2 is a pure refactor).

- [ ] **Step 2: Rewrite** `trust` and `writeHumans`. `grep -n 'tx.ACL(' internal/slackbridge/*.go | grep -v _test` prints only `archive.go` (`Remove`) and `create.go` step 4 (the pin).

- [ ] **Step 3: Run**

Run: `make lint && make test && make test-conformance && bash scripts/m7-exit-checks.sh --deps-only`
Expected: PASS; the Slack import rule holds (`internal/slackbridge` imports `internal/core/access`, never `internal/server`).

- [ ] **Step 4: Commit**

```bash
git add internal/slackbridge
git commit -m "refactor(slackbridge): who may post and who is invited through access.Checker (Decision 11 unchanged)"
```

---

### Task 7: Task ownership through `Access` (question 1, recommended A; skip this task if Eric answers B)

**Files:**
- Modify: `internal/core/tasks/update.go` (`ownerGuardBlocked` deleted; `applyPatch` takes a `func() error` guard, or `update` calls `Access.Require(p, Task{Channel: ch.ID, Owner: cur.Owner, Status: cur.Status, TakesOwner: ..., Force: p.Force}, Modify)` before `applyPatch` with the two `conflict` texts preserved), `internal/core/tasks/doc.go` comments
- Test: `internal/core/tasks/update_test.go` (the owner-guard table moves to `decide_test.go` in Task 1; here keep the end-to-end `conflict` assertions with their texts `task %d is in progress, owned by %s` and `task %d is owned by %s`)

- [ ] **Step 1: Confirm the pin**: `go test -count=1 -run 'Owner|Guard|Force' ./internal/core/tasks/` PASS before the change.
- [ ] **Step 2: Rewrite**; `grep -n 'Owner != as\|Owner == as' internal/core --include='*.go' -r | grep -v _test` prints nothing outside `internal/core/access`.
- [ ] **Step 3: Run** `make lint && make test && make test-conformance`. Expected: PASS; the matrix row `task create, claim, release, update` unchanged.
- [ ] **Step 4: Commit**

```bash
git add internal/core/tasks
git commit -m "refactor(tasks): the owner guard is access.Checker's Task/Modify rule (conflict unchanged)"
```

---

### Task 8: The gate, the client mirrors (question 3) and the records

**Files:**
- Create: `internal/core/access/sites_test.go` (`TestAccessRuleSites`, go/ast, the pattern of `internal/core/identities/putkey_callers_test.go`), `docs/adr/0011-one-access-check.md`
- Modify: `internal/tui/model.go` (question 3, recommended A: `statusCmd` always calls `Status` and treats `forbidden` as "not an administrator"; the `admin` member stays, set from the call's success), `internal/tui/tasks.go` (`taskEditable` removed; the patch is sent and the server's `conflict` is the toast; `ownedToast` keeps its text), `docs/adr/README.md` (row 0011), `docs/adr/0008-channel-access-control-lists.md` and `0009-scopes-and-slack-bridge.md` (one note each), `docs/superpowers/specs/2026-10-19-agentbus2-design.md` section 4.6.1 (one paragraph), `CLAUDE.md` (Design bullets: `internal/core/access` is the one access check; `authz.go` gone; the Store bullet; the anchor's home), `docs/superpowers/plans/2026-10-25-channel-visibility.md` (a "superseded in part" note at the top of Task 1 and Review Focus 1 pointing at spec 3.10; the rebuilt plan is a separate document written afterwards)
- Test: `internal/tui/*_test.go` (the status and task tests that assert on the client-side checks are rewritten to assert on the reaction to `forbidden`/`conflict`; the Milestone 8 counts stay)

**Interfaces:**
- Produces: `TestAccessRuleSites` with the allowlist as a literal: `tx.ACL(` callers `internal/core/access`, `internal/core/channels/channels.go ACL`, `internal/slackbridge/archive.go Remove`, `internal/slackbridge/create.go <the step-4 function name>`; `.Admin`/`.Creator`/`.Owner` conditions and `Kind == store.Identity*` comparisons allowed only in `internal/core/access`, plus by function name: `internal/core/identities/human.go signInHumanTx` (sets the flag), `register.go registerTx` (Decision 17 and the human refusal), `agent.go ensureAgent` (kind conflict), `admin.go resetCredential` (human has no keys), `internal/core/sessions/identities.go Identities` (copies the flag), `internal/core/convert.go` (converters), `internal/core/tasks/reclaim.go` (`t.Owner == ""` is state, not access; allow the file), `internal/core/tasks/read.go taskList` (`in.Owner` filter is a query parameter; allow the function), `internal/core/scopes/acl.go CheckBridgeACL` (ACL shape). Scope: non-test `.go` files under `internal/core`, `internal/server`, `internal/slackbridge`, `internal/bus/local`. A second check walks `internal/store/sqlstore` string literals for the five SQL fragments of the Global Constraints.

- [ ] **Step 1: Write the gate test** and run it: `go test -count=1 -run TestAccessRuleSites ./internal/core/access/`. Expected: PASS after Tasks 2 to 7; if it fails, the failure names a site this plan missed: fix the site, never the allowlist, unless the site is listed in spec Appendix A as kind N.

- [ ] **Step 2: Negative check of the gate**: in a throwaway worktree (`git worktree add /tmp/ab2-gate HEAD`), add `if p.Admin { return nil }` (the `Principal` is in scope there after Task 2) to `internal/core/channels/channels.go Delete`, run the gate, expect FAIL naming `channels.go Delete`; remove the worktree. Record the output in the commit message body.

- [ ] **Step 3: Client mirrors** per question 3; rewrite their tests.

- [ ] **Step 4: Records**: ADR 0011 (Eric's decisions 1 and 2 of the spec, the table of spec 3.2 transcribed, the denial codes, the gate, the anchor move; the protocol-versioning decision as context; status "Accepted (Eric, 2026-10-25)" for section 1, "Proposed" for every section 4 item he has not answered, each marked), the notes in ADR 0008 and 0009, the spec paragraph, `CLAUDE.md`, the visibility plan note.

- [ ] **Step 5: Run**

Run: `make verify && go test -count=1 -p 1 -v ./internal/conformance/bus/ > /tmp/m3.log 2>&1; bash scripts/m3-exit-counts.sh /tmp/m3.log && bash scripts/m4-exit-checks.sh --deps-only && bash scripts/m5-exit-checks.sh --deps-only && bash scripts/m6-exit-checks.sh --deps-only && bash scripts/m7-exit-checks.sh --deps-only && bash scripts/m8-exit-checks.sh --deps-only`
Expected: PASS; `matrix_sqlite=52 matrix_postgres=52`; every import rule holds.

- [ ] **Step 6: Commit**

```bash
git add internal/core/access/sites_test.go internal/tui docs/adr/0011-one-access-check.md docs/adr/README.md docs/adr/0008-channel-access-control-lists.md docs/adr/0009-scopes-and-slack-bridge.md docs/superpowers/specs/2026-10-19-agentbus2-design.md CLAUDE.md docs/superpowers/plans/2026-10-25-channel-visibility.md
git commit -m "test(access): the no-other-rule gate; tui reacts to codes instead of mirroring rules; ADR 0011"
```

---

## After this plan

The channel-visibility plan is rewritten against the tree this plan leaves: its Task 1 becomes `channels.Service.List` filtering `ListChannels` by `Access.Channels(p, access.List)` (no Store query), its Task 2 replaces the `channel_capabilities` discovery capability with the protocol-version bump of Eric's protocol-versioning decision (spec Context section: `info.version`, `protocol_version` on discover and health), and its client tasks stand.
