# Scopes and the Slack Bridge (Part 2) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Scopes group channels and agent identities with a lifecycle and retire as a unit; one scoped bus channel bridges both ways to one private Slack channel over Socket Mode with no echoes, Slack humans arrive as their part 1 human identities, and Slack channels and bus resources are reclaimed without pai's help; `reply_to` is validated everywhere and the subscription retention rule is fixed for every deployment.

**Architecture:** Store schema version 6 adds `scopes`, scope columns on `channels` and `identities`, `retired_identities`, `bridges`, `bridge_messages`, `slack_users` and the reserved `slack-bridge` identity row. A new core aggregate `internal/core/scopes` owns scope rules, the live-bridge ACL rule and retirement (a maintenance job that reaches Slack only through an `Archiver` port). Every Slack call goes through the interface in `internal/slackapi` (standard library only); `internal/slackapi/slackgo` is its only implementation over `github.com/slack-go/slack`, and `internal/slackapi/slackfake` is the in-memory fake for tests. `internal/slackbridge` holds the bridge service (create, list, get, remove, archive) and the runner (one replica, `Store.Locks` lock `slack-bridge`); the server reaches it through the `server.Bridges` seam and `internal/server/cmd` wires it.

**Tech Stack:** Go 1.27, `github.com/slack-go/slack` v0.30.1 (Socket Mode in its `socketmode` package; pure Go over `github.com/gorilla/websocket`, so `CGO_ENABLED=0` builds), kin-openapi + oapi-codegen (`make generate`), modernc SQLite and pgx Postgres, testcontainers for the Postgres suites.

**Library choice (plan choice):** slack-go/slack v0.30.1, because it already implements Socket Mode envelopes, acks, reconnects and `Retry-After` (`slack.RateLimitedError`) that a hand-written client would have to re-derive, and it stays behind `internal/slackapi` so it can be replaced without touching the bridge.

**Spec:** `docs/superpowers/specs/2026-10-23-slack-bridge-design.md` (frozen; every Decision and Claude's call ratified by Eric on 2026-10-23). It depends on part 1 (`docs/superpowers/specs/2026-10-23-federated-identity-design.md`, plan `docs/superpowers/plans/2026-10-23-federated-identity.md`), which must have landed: schema version 5 and the names under "Consumed from part 1". External facts: the fact check of 2026-10-23, items 7 to 14.

## Global Constraints

- Done gate: `make verify` (needs Docker; a run that skips the Postgres suites is not a pass). Work is done only when `ci.yml` and `windows.yml` are green on the pushed commit, `release-check` and `check-gates-selftest` jobs included.
- Per task: `make lint` and `make test`; a task that changes `internal/store`, `internal/core` or a Bus behavior also runs `make test-conformance` (both engines, both transports). A task that edits `api/openapi.yaml` runs `make generate` and commits `internal/gen` (never edited by hand). A task that adds an import runs `bash scripts/m4-exit-checks.sh --deps-only`, `m5`, `m6`, `m7`, `m8` `--deps-only`.
- Import rules (CLAUDE.md, unchanged) plus, from Task 10 on, checked by `scripts/m7-exit-checks.sh`: `internal/slackapi` imports only the standard library; `github.com/slack-go/slack` is imported only by `internal/slackapi/slackgo`; `internal/slackapi/slackfake` imports only the standard library and `internal/slackapi`; `internal/slackbridge` never imports `internal/server`, `internal/gen`, `internal/slackapi/slackgo` or slack-go; `internal/bus`, `internal/bus/local`, `internal/bus/remote`, `internal/core/...`, `internal/cli`, `internal/mcpserver`, `internal/client` and `internal/server` (the package, not `internal/server/cmd`) never import `internal/slackapi`, `internal/slackbridge` or slack-go. `internal/tui` is already limited by the m8 allowlist.
- Gate-script anchors stay verbatim and exactly once (or the moving task updates `scripts/check-gates-selftest.sh` and runs `make check-gates-selftest`): `if !id.Admin {` (`internal/core/authz.go`), `if !readable[sub.ChannelID] {` (`internal/core/delivery/receive.go`), `floor := max(c.CursorSeq, c.PendingEndSeq, after)` (`internal/core/delivery/peek.go`), `held, err = t.TryLease(ctx, name, owner, now, until)` (`internal/store/sqlstore/lock.go`), and part 1's issuer and token-endpoint anchors. Server-admin checks go through `core.RequireAdmin`, never a second `if !id.Admin {`.
- Counted tests keep their names and counts: `TestACLMatrix` 52 (`scripts/m3-exit-counts.sh`), `TestAuthEndToEnd` 11 per engine, `TestReplicasWake` and `TestReplicasMaintenanceExclusive` (`m7`), the `internal/e2e` counts (`m6`), the m8 TUI counts, part 1's `TestFederatedEndToEnd` 7 per engine. New coverage goes in new tests.
- Closed enumerations, each refused otherwise (ErrInvalid in the Store, `validation` in the core and wire, exit 2 in commands, `internal` when a client receives one from a server), with a test: scope states `open`, `done`, `retired`; retire modes `with_bridge`, `normal`; bridge states `creating`, `live`, `archived`. Accepted Slack message subtypes are exactly `""` and `thread_broadcast`; handled Socket Mode events are exactly `message` and `member_left_channel` (every other envelope is acked and ignored, with a test).
- Error codes stay the closed set of `internal/bus/errors.go`. Fixed texts: `forbidden` "the Slack bridge is not configured" (`bus.BridgeNotConfigured`); `not_found` "scope %q is retired"; `validation` "reply_to %d is in another channel"; `not_found` "reply_to %d does not exist" (unchanged); `conflict` "channel %q is already bridged".
- Defaults (spec 5.1): `slack_archive_after_done_hours` 168, `scope_idle_hours` 336, `slack_max_message_chars` 3000. Plan bounds: hours 1 to 8760 each; chars 200 to 3000 (3000 is Slack's section text limit, the reason for the default).
- `slack-bridge` is a reserved identity name like `everyone` and `admins`: its row exists from the migration, no registration can create it or attach a key, it holds no general subscription.
- No Slack token, bus message content or Slack message text appears in a log line, error text or response body. A dropped Slack message is logged with the Slack user ID, the Slack channel ID and the reason only.
- No test reaches Slack: bridge tests use `slackfake`; adapter tests use an `httptest` server through `slack.OptionAPIURL`.
- Docs move with the code in the task that changes behavior: `docs/ops/configuration.md`, new `docs/ops/slack-bridge.md`, `docs/ops/admin-bootstrap.md`, `docs/ops/README.md`, new `docs/adr/0009-scopes-and-slack-bridge.md` (Eric's 2026-10-23 decisions labeled as his), stale CLAUDE.md lines, `internal/cli/skills/using-agentbus2/SKILL.md`, MCP tool descriptions. American English.
- Commits land on `main`, one per task, ending with the session's `Co-Authored-By` and `Claude-Session` lines. No push without Eric.

## Consumed from part 1

`store.IdentityKind`, `store.IdentityAgent`, `store.IdentityHuman`, `store.Identity.Kind`; `store.HumanBinding`, `store.ClaimEmail`, `Tx.HumanBinding(ctx, identity)`; `identities.Service.LookupHumanByEmail(ctx, email) (string, error)`; `identities.NormalizeEmail`; `identities.Service.CreateIdentityByEmail`; `store.NewMessage.SenderKind`, `core.AppendInput.SenderKind` (set from the `RequireIdentity` result in `messaging.Send`); `bus.Message.SenderKind`; `bus.IdentityHuman`; part 1's `hostEnv{Svc, Issuers}` and `adminAction func(context.Context, hostEnv, io.Writer) error` in `internal/server/cmd/admin.go` (part 1 plan, Task 4); part 1's `reset-credential` on a human (`resetCredential` in `internal/core/identities/admin.go`); schema version 5.

## Review Focus

1. A Slack event that arrives after its bridge was archived, removed or its scope retired (a race with maintenance or `bridges remove`) is dropped: no bus message, no map or binding row, no row resurrected (Task 13 subtest `archived-bridge-ignored` and `removed-bridge-ignored`).
2. A Slack user changes their profile email to the address of another human already bound to a different Slack account, or a second Slack account presents a bound email: refused every time, with no binding change (Task 13 subtest `email-moved-to-other-account`).
3. A long bus message with multi-byte characters, emoji, a Markdown link and a code fence near the limit is cut by runes, never inside a link or fence, and carries the truncation note within `slack_max_message_chars` (Task 11 `TestTruncateKeepsMarkup`).
4. A burst of 429 answers with `Retry-After` during the drain after an outage: each message is posted once, in seq order, and the acked mark advances only after a successful post (Task 14 subtest `rate-limited-drain`).
5. A Slack thread reply whose root came from an unauthorized user (dropped, unmapped, not the app's own post) goes to the bus top-level and never quotes the root, so unauthorized text cannot leak in (Task 13 subtest `unauthorized-root-not-quoted`).

---

### Task 1: Store schema version 6 (scopes, scope columns, retired names, `slack-bridge`, bridge tables) and the retention fix

**Files:**
- Create: `internal/store/sqlite/schema/0006_scopes_bridges.sql`, `internal/store/postgres/schema/0006_scopes_bridges.sql`, `internal/store/scopes.go`, `internal/store/sqlstore/scopes.go`
- Modify: `internal/store/sqlite/migrations.go`, `internal/store/postgres/migrations.go` (`{Version: 6, ...}`), `internal/store/sqlstore/humans.go` (part 1's `CreateHuman` goes through the same reserved and retired name guard), `internal/store/enrollment.go` (`DeleteEnrollmentTokensFor`), `internal/store/store.go` (`Tx` embeds `Scopes`), `internal/store/identities.go` (`Identity.Scope`, `SlackBridge`, `ReservedIdentity`), `internal/store/channels.go` (`Channel.Scope`, `ChannelCreate.Scope`, `EmptyChannelsBefore`/`DeleteChannelIfEmpty` signatures and docs), `internal/store/meta.go` (Reset docs), `internal/store/sqlstore/identities.go` (`identityCols`, reserved and retired names), `internal/store/sqlstore/channels.go` (scope column, the new empty rule), `internal/store/sqlstore/messages.go` (`AppendMessage` touches the scope), `internal/store/sqlstore/enrollment.go`, `internal/store/sqlstore/meta.go` (`ResetRegistry` keeps `slack-bridge`), `internal/core/maintenance/jobs.go` (`emptyChannels` passes `resumableAfter`), `internal/core/maintenance/service.go` (doc only), `docs/ops/configuration.md` (the `empty_channel_reap_hours` row)
- Test: `internal/conformance/store/scopes_test.go`, `internal/conformance/store/emptychannels_test.go`, `internal/conformance/store/identities_test.go`, `internal/conformance/bus/maintenance_test.go`

**Interfaces:**
- Consumes: part 1 schema version 5 and `store.Identity.Kind`; `sqlstore.Migration`; `isConflict`, `isForeignKey`, `guardIdentityName`, `t.writable()`.
- Produces (package `store`):
  - `const SlackBridge = "slack-bridge"`; `func ReservedIdentity(name string) bool` (true for `Everyone`, `Admins`, `SlackBridge`).
  - `Identity.Scope string` (after `Kind`), `Channel.Scope string`, `ChannelCreate.Scope string` (`""` is unscoped, stored as NULL).
  - `type ScopeState string` (`ScopeOpen = "open"`, `ScopeDone = "done"`, `ScopeRetired = "retired"`); `type RetireMode string` (`RetireWithBridge = "with_bridge"`, `RetireNormal = "normal"`); `func ValidRetireMode(m RetireMode) bool`; `type Scope struct { Name string; State ScopeState; Retire RetireMode; CreatedAt, DoneAt, RetiredAt, LastActivity int64 }`.
  - `type Scopes interface`:
    - `CreateScope(ctx, name string, now int64) error`: ErrInvalid (NameRule); ErrConflict (the name exists in any state). `LastActivity = now`.
    - `Scope(ctx, name string) (Scope, error)` (ErrNotFound); `ListScopes(ctx) ([]Scope, error)` (every state, by name in byte order).
    - `MarkScopeDone(ctx, name string, mode RetireMode, now int64) error`: ErrInvalid (unknown mode); ErrNotFound; ErrConflict unless `open`.
    - `MarkScopeRetired(ctx, name string, now int64) error`: ErrNotFound; nil when already retired.
    - `DeleteRetiredScopes(ctx, retiredBefore int64) (int, error)`: retired rows with `retired_at < retiredBefore` that no channel or identity references.
    - `CreateScopedIdentity(ctx, name, scope string, now int64) error`: as `CreateIdentity` plus the scope; ErrNotFound for an unknown scope (foreign key).
    - `ScopeMembers(ctx, scope string) ([]Channel, []Identity, error)`: both by name.
    - `RetireIdentity(ctx, name string, now int64) (bool, error)`: in one transaction deletes the name's `channel_acl` rows on every channel, its bound `enrollment_tokens`, then the identity row (keys, subscriptions, tag subscriptions, sessions and cursors, `slack_users` cascade) and inserts `retired_identities (name, scope, retired_at)`; false, nil when the row is already gone. Messages stay.
    - `IdentityRetired(ctx, name string) (bool, error)`.
  - `EnrollmentTokens` gains `DeleteEnrollmentTokensFor(ctx, identity string) (int, error)` (every token bound to the name; 0 for none; `RetireIdentity` uses it, and Task 4 uses it when it re-issues a token).
  - `CreateIdentity`, `CreateHuman` (part 1) and `CreateScopedIdentity` refuse `ReservedIdentity` names (ErrInvalid) and retired names (ErrConflict "identity name %q is retired"); `PutPublicKey` and `PutEnrollmentToken` refuse `SlackBridge` (ErrInvalid).
  - `EmptyChannelsBefore(ctx, createdBefore, resumableAfter int64) ([]Channel, error)` and `DeleteChannelIfEmpty(ctx, id, createdBefore, resumableAfter int64) (bool, error)` (Decision 20): a subscription counts only when its identity is not `slack-bridge` and holds a session row with `closed_at = 0` and `lease_expires_at >= resumableAfter`; a channel with a `bridges` row in state `creating` or `live` (`state <> 'archived'`) is never offered (spec 6.1b: an archived bridge does not pin its channel, so rule 4.3(2) can still fire).
  - `AppendMessage` also runs `UPDATE scopes SET last_activity = ? WHERE name = (SELECT scope FROM channels WHERE id = ?) AND last_activity < ?` with the message's `CreatedAt`.
  - `ResetRegistry(ctx, keep)` keeps `slack-bridge` besides `keep`; `ResetData` keeps `scopes`, `retired_identities`, `bridges`, `bridge_messages` and `slack_users` (a bridge whose channel `ResetData` removed is cleaned by the runner, Task 14).

- [ ] **Step 1: Write the failing tests** (each under `dbtest.ForEachEngine`)
  - `TestScopesCreateGetList`: `CreateScope("job1", 1000)`; `Scope("job1")` is `{State: ScopeOpen, Retire: "", CreatedAt: 1000, LastActivity: 1000}`; again -> ErrConflict; `ListScopes` by name.
  - `TestScopesReject`: names `""`, `"a/b"`, `"a\x00b"` -> ErrInvalid; `MarkScopeDone("job1", "later", ...)` -> ErrInvalid; unknown scope -> ErrNotFound; done twice -> ErrConflict; `MarkScopeRetired` twice -> nil.
  - `TestDeleteRetiredScopes`: retired at 1000 and at 5000; `DeleteRetiredScopes(2000)` returns 1; a retired scope still referenced by a channel is kept.
  - `TestScopedChannelAndIdentity`: `CreateChannel{Scope: "job1"}` reads back `Scope == "job1"`; `Scope: "nope"` -> ErrNotFound; `CreateScopedIdentity("job1-coder", "job1")` has `Scope == "job1"`, `Kind == IdentityAgent`; `ScopeMembers("job1")` lists both; an unscoped channel and identity read `Scope == ""`.
  - `TestDeleteEnrollmentTokensFor`: two tokens bound to `pat`, one unbound; returns 2; the unbound token stays; again returns 0.
  - `TestRetireIdentity`: `job1-coder` with a key, a session, a subscription and an ACL entry on unscoped `ops`, a bound enrollment token and a message on `ops`; `RetireIdentity` -> `true`; identity, key, session, subscription, ACL row and token are gone; the message stays; `IdentityRetired` true; again -> `false, nil`; `CreateIdentity("job1-coder")` -> ErrConflict naming "retired".
  - `TestSlackBridgeReserved`: after open, `Identity("slack-bridge")` exists with `Kind == IdentityAgent`, no keys, no subscriptions; `CreateIdentity("slack-bridge")` -> ErrInvalid; `PutPublicKey` for it -> ErrInvalid; `PutEnrollmentToken{Identity: "slack-bridge"}` -> ErrInvalid; after `ResetRegistry("adm")` it still exists; `SetACL` with `{slack-bridge, [read]}` succeeds.
  - `TestEmptyChannelsIgnoreIdleSubscribers` (in `emptychannels_test.go`; the old `with-subscription` case now gives `pat` a live session): channels `sub-no-session` (reaped), `sub-live` (kept), `sub-resumable` (lease lapsed after `resumableAfter`: kept), `sub-closed` (closed session: reaped), `sub-bridge-only` (`slack-bridge` subscription: reaped), `bridged` (a live `bridges` row: never offered), `bridge-archived` (an archived `bridges` row, otherwise empty: reaped); `DeleteChannelIfEmpty` agrees with `EmptyChannelsBefore` for each.
  - `TestAppendTouchesScopeActivity`: append at 5000 on a `job1` channel -> `LastActivity == 5000`; append with `CreatedAt` 4000 leaves 5000; an unscoped append changes no scope.
  - `TestOpenMigratesVersion5Scopes` (modeled on `TestOpenMigratesVersion3AssertionJTI`): open with `full[:5]`, raw-insert identity `slack-bridge` with `is_admin` true, an `identity_keys` row, an open session, a `general` subscription, a tag subscription, a bound enrollment token, part 1's `human_bindings` row and an `access_refs` `{admins identity slack-bridge}` row, and a channel; reopen: `schemaVersion == len(full)`, `slack-bridge` is a non-admin agent with no keys, sessions, subscriptions, tag subscriptions, tokens, binding or `access_refs` row; the channel reads `Scope == ""`; `created_at > 0`.
  - `TestMaintenanceEmptyChannels` (bus suite, existing): flip the `with-subscription` expectation; the comment "a subscription of any identity still counts" becomes "kim has no live or resumable session, so the subscription does not keep the channel (Decision 20)"; add `with-live-subscriber` (kim with an open session) kept.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test -count=1 -p 1 -run 'TestScopes|TestDeleteRetiredScopes|TestDeleteEnrollmentTokensFor|TestScopedChannelAndIdentity|TestRetireIdentity|TestSlackBridgeReserved|TestEmptyChannelsIgnoreIdleSubscribers|TestAppendTouchesScopeActivity|TestOpenMigratesVersion5' ./internal/conformance/store/`
Expected: FAIL to compile (`undefined: store.Scope`, `store.SlackBridge`, ...).

- [ ] **Step 3: Write `0006_scopes_bridges.sql` for each engine** (no BEGIN/COMMIT; `<INT>` is INTEGER on SQLite, BIGINT on Postgres; the bridges id is `INTEGER PRIMARY KEY AUTOINCREMENT` / `BIGSERIAL PRIMARY KEY`):
  - `scopes (name TEXT PRIMARY KEY, state TEXT NOT NULL CHECK (state IN ('open','done','retired')), retire_mode TEXT NOT NULL DEFAULT '' CHECK (retire_mode IN ('','with_bridge','normal')), created_at <INT> NOT NULL, done_at <INT> NOT NULL DEFAULT 0, retired_at <INT> NOT NULL DEFAULT 0, last_activity <INT> NOT NULL)`.
  - `ALTER TABLE channels ADD COLUMN scope TEXT NULL REFERENCES scopes(name)`; the same on `identities`; indexes `channels_scope`, `identities_scope`.
  - `retired_identities (name TEXT PRIMARY KEY, scope TEXT NOT NULL, retired_at <INT> NOT NULL)`.
  - `bridges (id ..., channel_id <INT> NOT NULL UNIQUE, channel_name TEXT NOT NULL, scope TEXT NOT NULL, slack_channel_id TEXT NOT NULL DEFAULT '', slack_name TEXT NOT NULL, state TEXT NOT NULL CHECK (state IN ('creating','live','archived')), cursor_ts TEXT NOT NULL DEFAULT '', created_at <INT> NOT NULL, archived_at <INT> NOT NULL DEFAULT 0)` (no foreign key to `channels`: a bridge outlives a channel removed behind its back, spec 6.1b); `CREATE UNIQUE INDEX bridges_slack_channel ON bridges (slack_channel_id) WHERE slack_channel_id <> ''`.
  - `bridge_messages (bridge_id <INT> NOT NULL REFERENCES bridges(id) ON DELETE CASCADE, seq <INT> NOT NULL, slack_ts TEXT NOT NULL, root_ts TEXT NOT NULL, created_at <INT> NOT NULL, PRIMARY KEY (bridge_id, seq), UNIQUE (bridge_id, slack_ts))`; index `(bridge_id, root_ts)`.
  - `slack_users (identity TEXT PRIMARY KEY REFERENCES identities(name) ON DELETE CASCADE, team_id TEXT NOT NULL, user_id TEXT NOT NULL, created_at <INT> NOT NULL, UNIQUE (team_id, user_id))`.
  - `INSERT INTO identities (name, is_admin, tags_acked_seq, created_at) VALUES ('slack-bridge', <false>, 0, <now>) ON CONFLICT (name) DO NOTHING`, where `<now>` is `CAST(strftime('%s','now') AS INTEGER) * 1000` in the SQLite file and `(EXTRACT(EPOCH FROM now()) * 1000)::BIGINT` in the Postgres file (each engine has its own file).
  - Cleanup of a pre-existing row of that name (plan choice; see the report): `UPDATE identities SET is_admin = <false>, kind = 'agent', scope = NULL WHERE name = 'slack-bridge'`, then `DELETE FROM` each of `identity_keys`, `sessions` (cursors cascade), `subscriptions`, `tag_subscriptions`, `enrollment_tokens`, `human_bindings` `WHERE identity = 'slack-bridge'`, and `DELETE FROM access_refs WHERE ref_kind = 'identity' AND value = 'slack-bridge'` (part 1's `admins`/`users` entries).
  - `bridge_dropped (bridge_id <INT> NOT NULL REFERENCES bridges(id) ON DELETE CASCADE, slack_ts TEXT NOT NULL, created_at <INT> NOT NULL, PRIMARY KEY (bridge_id, slack_ts))`: Slack messages dropped as unauthorized; a drop is final (pruned with its bridge, so with the scope, Decision 15).
  - Header comment: spec 4, 5.3, 8; State (Definition of Done D): `runStep` locks, re-reads the version and runs the step in one transaction, so a second open skips it, two overlapping opens run it once, and a failed run rolls back to version 5 and reruns from clean.

- [ ] **Step 4: Implement** the Store methods in `internal/store/sqlstore/scopes.go` and the edits listed under Files; map unique and foreign-key violations with `isConflict`/`isForeignKey`. Update `maintenance.emptyChannels` to pass `resumableAfter = now - SessionRetentionHours*hourMs` and its doc comment (Decision 20). Docs: the `empty_channel_reap_hours` row reads "... with no messages and no subscriber that has a live or resumable session (the Slack bridge's subscription never counts)".

- [ ] **Step 5: Run the tests and the suites**

Run: `make test && make test-conformance`
Expected: PASS on SQLite and Postgres; `TestMigrationSetsAlign` passes (versions 1 to 6 on both); `go test ./internal/server/config/` passes (docs row).

- [ ] **Step 6: Commit**

```bash
git add internal/store internal/core/maintenance internal/conformance docs/ops/configuration.md
git commit -m "feat(store): schema version 6: scopes, retired names, slack-bridge, bridge tables; idle subscribers no longer keep a channel (Decision 20)"
```

---

### Task 2: Store methods for bridges, the message map and Slack user bindings

**Files:**
- Create: `internal/store/bridges.go`, `internal/store/sqlstore/bridges.go`
- Modify: `internal/store/store.go` (`Tx` embeds `Bridges`)
- Test: `internal/conformance/store/bridges_test.go`

**Interfaces:**
- Consumes: Task 1 tables.
- Produces (package `store`):
  - `type BridgeState string` (`BridgeCreating = "creating"`, `BridgeLive = "live"`, `BridgeArchived = "archived"`); `type Bridge struct { ID, ChannelID int64; ChannelName, Scope, SlackChannelID, SlackName string; State BridgeState; CursorTS string; CreatedAt, ArchivedAt int64 }`; `type BridgeMessage struct { BridgeID, Seq int64; SlackTS, RootTS string; CreatedAt int64 }`; `type SlackUser struct { Identity, TeamID, UserID string; CreatedAt int64 }`.
  - `type Bridges interface`:
    - `CreateBridge(ctx, b Bridge) (Bridge, error)`: inserts in state `creating` and returns it with `ID`; ErrConflict when the channel has a row; ErrInvalid for an empty `SlackName` or `Scope`, text failing TextRule, or `State` other than `""`/`creating`.
    - `Bridge(ctx, id int64)`, `BridgeByChannel(ctx, channelID int64)`, `BridgeBySlackChannel(ctx, slackChannelID string)` -> `(Bridge, error)` (ErrNotFound); `ListBridges(ctx) ([]Bridge, error)` (by channel name); `ScopeBridges(ctx, scope string) ([]Bridge, error)`.
    - `SetBridgeSlackChannel(ctx, id int64, slackChannelID string) error`: only in `creating` (ErrConflict otherwise, or when another bridge has the Slack id; ErrInvalid when empty; nil when already that id).
    - `SetBridgeLive(ctx, id int64) error`: `creating` with a Slack id -> `live`; `live` -> nil; no Slack id -> ErrInvalid; `archived` -> ErrConflict.
    - `SetBridgeArchived(ctx, id, now int64) error`: any state -> `archived` (`archived_at = now`); already archived -> nil.
    - `AdvanceBridgeCursor(ctx, id int64, ts string) error`: `cursor_ts = ts` when greater in byte order (Slack `ts` is fixed-width `seconds.micros`).
    - `DeleteBridge(ctx, id int64) error` (map rows cascade; ErrNotFound).
    - `PutBridgeMessage(ctx, m BridgeMessage) error`: ErrConflict on `(bridge, seq)` or `(bridge, slack_ts)`; ErrNotFound for an unknown bridge; ErrInvalid for an empty ts.
    - `BridgeMessageBySeq(ctx, bridgeID, seq int64)`, `BridgeMessageByTS(ctx, bridgeID int64, ts string)` -> `(BridgeMessage, error)` (ErrNotFound); `NewestInThread(ctx, bridgeID int64, rootTS, beforeTS string) (int64, error)` (the largest seq with that root and `slack_ts < beforeTS`; ErrNotFound); `ThreadRoots(ctx, bridgeID int64, sinceTS string, limit int) ([]string, error)` (distinct `root_ts` having a map row with `slack_ts >= sinceTS`, newest first, at most `limit`).
    - `PutBridgeDrop(ctx, bridgeID int64, ts string, now int64) error` (idempotent; ErrNotFound for an unknown bridge; ErrInvalid for an empty ts) and `BridgeDropped(ctx, bridgeID int64, ts string) (bool, error)`.
    - `SlackUserOf(ctx, identity string) (SlackUser, error)`, `IdentityOfSlackUser(ctx, teamID, userID string) (string, error)` (ErrNotFound); `PutSlackUser(ctx, u SlackUser) error` (the same row again: nil; the identity bound to another user or the user bound to another identity: ErrConflict; unknown identity: ErrNotFound); `DeleteSlackUser(ctx, identity string) (bool, error)`.

- [ ] **Step 1: Write the failing tests** (`dbtest.ForEachEngine`)
  - `TestBridgeLifecycle`: create on channel `job1-chat` -> `creating`, `ID > 0`; second create on it -> ErrConflict; `SetBridgeLive` without Slack id -> ErrInvalid; `SetBridgeSlackChannel("G1")` twice -> nil; `SetBridgeLive` twice -> nil; `BridgeBySlackChannel("G1")` finds it; `SetBridgeArchived` twice -> nil; `SetBridgeLive` after archive -> ErrConflict; `SetBridgeSlackChannel` after live -> ErrConflict; a second bridge taking `G1` -> ErrConflict.
  - `TestBridgeRejects`: empty `SlackName`, `Scope`, state `"paused"`, name with NUL -> ErrInvalid; unknown id -> ErrNotFound for each setter.
  - `TestBridgeMessages`: put `(seq 10, ts "1700000000.000100", root same)`, `(11, ".000200", root ".000100")`, `(12, ".000300", root ".000100")`; same seq -> ErrConflict; same ts -> ErrConflict; `NewestInThread(root, ".000250") == 11`; `NewestInThread(root, ".000100")` -> ErrNotFound; `ThreadRoots(id, "", 10) == [".000100"]`; `ThreadRoots(id, ".000400", 10)` is empty; lookups by seq and ts.
  - `TestBridgeDrops`: `PutBridgeDrop` twice -> nil; `BridgeDropped` true; unknown bridge -> ErrNotFound; after `DeleteBridge` the drop row is gone.
  - `TestBridgeCursorMonotonic`: advance to `.000300`, then `.000200` leaves `.000300`.
  - `TestDeleteBridgeCascades`: map and drop rows gone; `DeleteBridge` again -> ErrNotFound.
  - `TestSlackUsers`: bind eric to `(T1, U1)`; again -> nil; eric to `U2` -> ErrConflict; kim to `U1` -> ErrConflict; delete -> true, again false; deleting the identity (`RetireIdentity` or part 1 reset) removes the row.

- [ ] **Step 2: Run to verify failure** — `go test -count=1 -p 1 -run 'TestBridge|TestDeleteBridge|TestSlackUsers' ./internal/conformance/store/` -> FAIL to compile.
- [ ] **Step 3: Implement** in `internal/store/sqlstore/bridges.go`.
- [ ] **Step 4: Run** `make test && make test-conformance` -> PASS.
- [ ] **Step 5: Commit** `feat(store): bridges, the message map and Slack user bindings (spec 8)` with `internal/store internal/conformance/store`.

---

### Task 3: `reply_to` rules (Decisions 13, 14) and the history thread filter

**Files:**
- Modify: `internal/core/append.go`, `internal/store/messages.go` (`MessageFilter.Thread`), `internal/store/sqlstore/messages.go` (`MessagesInRange`, `MessagesBefore` apply it), `internal/bus/messaging.go` (`HistoryInput.Thread`), `internal/core/messaging/history.go`, `api/openapi.yaml` (`thread` query parameter on `GET /v1/channels/{channel}/messages`, integer, minimum 1), `internal/gen/*` (generated), `internal/server/messages.go`, `internal/bus/remote/messages.go`, `internal/mcpserver/inputs.go` (`historyIn.Thread`), `internal/mcpserver/server.go` (history call and description), `internal/cli/history.go` (`-thread SEQ`)
- Test: `internal/conformance/bus/replyto_test.go` (new), `internal/conformance/store/filters_test.go`, `internal/core/append_test.go`, `internal/mcpserver/server_test.go`, `internal/cli/messages_test.go`

**Interfaces:**
- Consumes: Task 2 `Tx.BridgeByChannel`, `store.BridgeLive`.
- Produces:
  - `core.Append` rules, in order, before the write: (1) parent missing -> `not_found` "reply_to %d does not exist" (unchanged); (2) `parent.ChannelID != in.ChannelID` -> `validation` "reply_to %d is in another channel" (on memory and task channels this is the whole memory-record rule, spec 7); (3) when the channel has a bridge in state `live` and the parent has a `reply_to`, the stored `reply_to` is the root: follow `reply_to` until a message without one (each hop has a smaller seq, so the walk ends; a hop whose row is gone stops at the last row found). `AppendInput.ReplyTo` is not mutated for the caller; the rewritten value is what `NewMessage.ReplyTo` gets.
  - The thread filter (additive; spec 7 assumed `history` already had one, and ADR 0009 records the correction): `store.MessageFilter.Thread *int64` keeps `seq == *Thread OR reply_to == *Thread` in `MessagesInRange` and `MessagesBefore` (nil: no filter; every other caller passes nil); `bus.HistoryInput.Thread *int64`; `History` refuses `Thread < 1` with `validation` "thread must be at least 1" and otherwise returns the root and its direct replies in the requested window (on a bridged channel every reply points at the root, Decision 14); wire query parameter `thread`; MCP `history` input `thread` ("only the thread rooted at this sequence number: the message and every message whose reply_to is it"); CLI `history -thread SEQ`.

- [ ] **Step 1: Write the failing tests**
  - `TestReplyToRules` (bus suite, both engines and transports): `pat` sends `a` on `dev`, `b` on `ops`; a reply on `dev` to `b` -> `validation` with "another channel"; a reply on `dev` to a memory seq on `notes` -> `validation`; to 9999 -> `not_found` (the existing `send_test.go` case still passes); to `a` -> success and `History` shows `reply_to == a`; on `notes` (memory) a send replying to a memory of `notes` succeeds.
  - `TestReplyToRewriteOnBridgedChannel` (bus suite): channel `job1-chat` (scope `job1`); before any bridge, `r1` replies to root `m0`, `r2` replies to `r1` (stored as `r1`: no bridge yet); add a bridge row via `h.tx` (`CreateBridge`, `SetBridgeSlackChannel`, `SetBridgeLive`); a reply to `r2` is stored with `reply_to == m0` (the walk); a reply to `m0` stays `m0`; on an archived bridge the reply to `r2` stays `r2`.
  - `TestHistoryThread` (bus suite, both engines and transports): on `dev`, root `t`, two replies to `t`, an unrelated message in between and 150 later messages; `History{Channel: "dev", Thread: &t}` returns exactly `t` and its two replies, ascending, however far apart; `Thread: &other` returns only `other` when it has no replies; `Thread` 0 -> validation; `Before`/`After` still bound the window.
  - `TestMessagesThreadFilter` (store suite, `filters_test.go`): `MessagesInRange` and `MessagesBefore` with `MessageFilter{Thread: &t}` keep the root and replies only; nil keeps everything.
  - `TestAppendRefusesOtherChannelParent` (core unit, SQLite file): the validation fires before `AppendMessage` (no row written, `MaxSeq` unchanged).
  - `TestHistoryToolThread` (mcpserver): the `history` tool accepts `thread` and returns only the thread; `TestHistoryThreadFlag` (cli): `history -thread 7 dev` passes `Thread: 7`, `-thread x` exits 2.

- [ ] **Step 2: Run to verify failure** — `go test -count=1 -p 1 -run 'TestReplyTo|TestHistoryThread' ./internal/conformance/bus/` -> FAIL (the other-channel reply succeeds; `Thread` undefined).
- [ ] **Step 3: Implement**; update the doc comments (Decisions 13, 14); `make generate`.
- [ ] **Step 4: Run** `make generate-check; make test && make test-conformance && go test -count=1 -v ./internal/e2e/ > /tmp/e2e.log; bash scripts/m6-exit-checks.sh /tmp/e2e.log` -> PASS, `words_ok=1`.
- [ ] **Step 5: Commit** `feat(core): reply_to must be in the same channel; flat threads on bridged channels; history thread filter (Decisions 13, 14, spec 7)`.

---

### Task 4: Scopes in the core (scopes, scoped channels, scoped agent identities, registration)

**Files:**
- Create: `internal/core/scopes/service.go`, `internal/core/scopes/acl.go`, `internal/bus/scopes.go`, `internal/core/identities/agent.go`
- Modify: `internal/core/authz.go` (`reservedIdentity` uses `store.ReservedIdentity`), `internal/core/channels/channels.go` (`Create` with scope), `internal/core/convert.go` (`ChannelInfoToBus` copies `Scope`, `ScopeToBus`), `internal/bus/types.go` (`ChannelInfo.Scope`), `internal/bus/messaging.go` (`CreateChannelInput.Scope`), `internal/core/identities/register.go` (rule a3), `internal/core/identities/enrollment.go` (extract `mintToken` from `CreateEnrollmentToken`, line 29), `internal/core/identities/admin.go` (`resetCredential` on a human also deletes its `slack_users` row, plan choice), `internal/server/cmd/admin.go` (`create-identity NAME [-scope S]`), `internal/core/identities/service.go` (modes), `internal/server/config/config.go` (`registration`), `internal/server/cmd/config.go` (flag help), `internal/server/auth_handlers.go` (mode mapping), `internal/server/server.go`, `scripts/docker-smoke.sh` (`-registration open` in place of `closed`, still proving the flag overrides the container's `enrollment` default), `docs/ops/admin-bootstrap.md`, `docs/ops/configuration.md`, `docs/ops/deploy.md`, `docs/adr/0002-authentication-and-credentials.md` (decisions 5 and 8 amended, labeled Eric's, 2026-10-23), `CLAUDE.md` (server and host commands lines)
- Test: `internal/conformance/bus/scopes_test.go` (new), `internal/core/identities/service_test.go`, `internal/core/scopes/acl_test.go`, `internal/server/cmd/admin_test.go`, `internal/server/config/config_test.go`, `internal/server/config/load_test.go`, `internal/server/cmd/cmd_test.go`, `internal/server/cmd/config_test.go`, `internal/server/auth_test.go`

**Interfaces:**
- Consumes: Task 1 `Tx` scope methods, `store.ReservedIdentity`; Task 2 `ScopeBridges`; part 1 `hostEnv` and `adminAction` (the scoped `create-identity` action reads `env.Svc`).
- Produces:
  - Package `bus` (`internal/bus/scopes.go`): `type ScopeState string` (`ScopeOpen`, `ScopeDone`, `ScopeRetired`), `type RetireMode string` (`RetireWithBridge`, `RetireNormal`); `type Scope struct { Name string; State ScopeState; Retire RetireMode; CreatedAt, DoneAt, LastActivity int64; Channels, Identities []string }` (`Channels`/`Identities` filled by Get only); `type CreateScopeInput struct{ Name, IdempotencyKey string }`; `type DoneScopeInput struct { Name string; Retire RetireMode; IdempotencyKey string }`; `type CreateAgentIdentityInput struct{ Name, Scope string }`; `type AgentIdentity struct { Identity, Scope string; Created bool; EnrollmentToken string; ExpiresAt int64 }`. `CreateChannelInput.Scope string`; `ChannelInfo.Scope string`.
  - Package `scopes`: `type Service struct{ *core.Core }`; `Create(ctx, admin string, in bus.CreateScopeInput) (bus.Scope, error)`; `List(ctx, admin string) ([]bus.Scope, error)` (open and done only); `Get(ctx, admin, name string) (bus.Scope, error)`; `Done(ctx, admin string, in bus.DoneScopeInput) (bus.Scope, error)`; all admin only (`core.RequireAdmin`), mutations under `WithReceipt`. `func RequireOpen(ctx context.Context, c *core.Core, tx store.Tx, name string) (store.Scope, error)`: unknown -> `not_found` "scope %q does not exist"; retired -> `not_found` "scope %q is retired"; done -> `conflict` "scope %q is done; nothing new may join it". `Done` with `with_bridge` needs a bridge of the scope in state `live` (`conflict` "scope %q has no live bridge" otherwise, Claude's call 4.1); an unknown `Retire` -> `validation`. Get/Done on a retired scope -> the retired `not_found`.
  - `func CheckBridgeACL(entries []store.ACLEntry) error` (pure, `acl.go`): `forbidden` "a bridged channel may grant read or write only to named identities (Decision 12)" when `everyone` or `admins` holds read or write; `forbidden` "the slack-bridge read entry is pinned while the bridge is live" unless some `slack-bridge` entry holds read. Used by Tasks 6 and 12.
  - `channels.Service.Create`: `in.Scope != ""` from a non-admin -> `forbidden` "only an administrator may name a scope"; from an admin -> `RequireOpen`; `in.Scope == ""` and a scoped creator -> the creator's scope, after `RequireOpen`; the payload fingerprint includes the scope.
  - Package `identities`: `func (s Service) CreateAgentIdentity(ctx, in bus.CreateAgentIdentityInput) (bus.AgentIdentity, error)` and `CreateAgentIdentityAs(ctx, admin string, in bus.CreateAgentIdentityInput) (bus.AgentIdentity, error)` (admin only; no idempotency key: the token is never stored in a receipt). Rules: scope `""` -> unscoped agent row with the general subscription (`createWithGeneral`); a scope -> `RequireOpen`, `CreateScopedIdentity`, no general subscription (Decision 19); in the same transaction a token bound to the name, 1 use, 24 hours (plan choice), minted before the transaction by `mintToken(identity string, expires time.Duration, uses int) (token string, row store.EnrollmentToken, err error)`, extracted from `CreateEnrollmentToken` (`internal/core/identities/enrollment.go:29`), which then calls it. An existing keyless agent row of the same scope -> `Created: false` and a fresh bound token, after `DeleteEnrollmentTokensFor(name)` in the same transaction revokes the earlier unused ones (a retry after a lost response leaves one live token); with keys -> `conflict` "identity %q already has a credential"; another scope, unscoped vs scoped, or a human -> `conflict` naming both; a retired name -> `conflict` from the Store.
  - `registerTx` rule (a3), after (a): an existing row with `Scope != ""` needs a supplied token whose `Identity` equals the name, else `forbidden` "identity %q is scoped; it registers only with the enrollment token bound to it", consuming nothing. New rows from registration are never scoped (unchanged `createWithGeneral`).
  - **Registration modes (spec Decision 21, Eric's, 2026-10-23).** `ModeClosed` and the `closed` value are deleted (config `Validate`: "registration must be open or enrollment, got %q"; the flag help says `open or enrollment`); `registerTx` is otherwise unchanged apart from rule (a3), and binding is the admin's choice at mint time (no new setting). An unset or unrecognized mode reaching the handler still refuses every register (`forbidden` "registration is not configured"), never opens: the fail-closed fallback that `ModeClosed` used to provide.
  - Host command: `agentbus2 server admin create-identity NAME [-scope S]`; without `-scope` the output is unchanged; with it: `<name> created in scope <S>` (or `<name> already exists in scope <S>`), then `token: <token>` and `expires: <RFC 3339>`.

- [ ] **Step 1: Write the failing tests**
  - `TestCheckBridgeACL` (unit, table): `{everyone [read]}`, `{admins [write]}` -> forbidden; `{admins [admin]}` plus `{slack-bridge [read]}` -> nil; no slack-bridge entry -> forbidden; `{slack-bridge [write]}` only -> forbidden.
  - `TestScopesCore` (bus suite, both engines; `scopes.Service{Core: h.core}`): admin creates `job1` -> open; again -> conflict; non-admin -> forbidden; `Get("nope")` -> not_found; `Done(normal)` -> done; again -> conflict; `Done{Retire: "later"}` -> validation; `Done(with_bridge)` on `job2` without a bridge -> conflict; with a live bridge row -> done; after `MarkScopeRetired`, `Get` and `Done` -> not_found containing "is retired"; `List` omits it.
  - `TestScopedChannels` (bus suite): admin creates `job1-chat` with `Scope: "job1"` -> `ChannelInfo.Scope == "job1"`; non-admin naming a scope -> forbidden; scoped `job1-coder` creating `job1-notes` (kind memory) -> scope `job1`; unscoped `pat` -> `""`; after `Done(job1)` both the admin and the scoped creator get conflict; unknown scope -> not_found.
  - `TestCreateAgentIdentity` (core, SQLite file): scoped create returns a token `abe_...`, `Created`, `ExpiresAt == now + 24h`; the row has the scope and no general subscription; unscoped create has general; a second call returns `Created == false` and a different token, and the first token no longer registers (revoked; only one bound token is listed); after a key registers -> conflict; other scope -> conflict; done scope -> conflict; human name -> conflict; non-admin `CreateAgentIdentityAs` -> forbidden.
  - `TestRegisterScopedNeedsBoundToken` (core): in `ModeOpen` and `ModeEnrollment`: no token -> forbidden; an unbound token -> forbidden and its `UsesLeft` unchanged; a token bound to another name -> forbidden; the bound token -> success, consumed.
  - `TestRegistrationModes` (config and cmd): `registration=closed` -> validation error naming `registration`; the existing closed-mode cmd and config tests move to `open`/`enrollment`; `-registration open` on a non-loopback listener -> listening line says `registration open`.
  - `TestRegisterBoundTokenRecovers` (core, `ModeEnrollment`): after `ResetCredential` on an admin, a token bound to it registers a new key; after `CreateAgentIdentity` (unscoped), its bound token registers.
  - `TestRegisterUnsetModeRefuses` (server): a handler built with no registration mode answers `forbidden` "registration is not configured" and creates nothing.
  - `TestRegisterNeverCreatesScoped`: open-mode registration of a new name yields `Scope == ""`.
  - `TestResetCredentialHumanClearsSlackUser`: a human with a `slack_users` row; `ResetCredential` removes it.
  - `TestAdminCreateScopedIdentity` (host, SQLite file): with scope `job1` created through `scopes.Service`, `create-identity job1-coder -scope job1` exits 0 with the three lines; `-scope nope` exits 1 naming the scope; `create-identity pat` output unchanged.

- [ ] **Step 2: Run to verify failure** — `go test -run 'TestCheckBridgeACL' ./internal/core/scopes/ && go test -run 'TestCreateAgentIdentity|TestRegisterScoped|TestRegisterBoundTokenRecovers|TestRegisterNeverCreatesScoped|TestResetCredentialHumanClearsSlackUser' ./internal/core/identities/` -> FAIL to compile.
- [ ] **Step 3: Implement** as in Produces. Docs: `admin-bootstrap.md` gains "Scopes and scoped agents" (create a scope, a scoped agent and its one-time token, the agent registers its own key; a scoped name can never be claimed without its bound token; an administrator who wants only pre-created names to register mints only bound tokens); `configuration.md` and `deploy.md` drop `closed` (the Docker override example becomes `-registration open`); ADR 0002 decisions 5 and 8 amended (Eric's, 2026-10-23); CLAUDE.md server line says `-registration open|enrollment` and host commands line gains `create-identity NAME [-scope S]`. `rg -n -i 'closed' internal/core/identities internal/server docs/ops scripts/docker-smoke.sh CLAUDE.md` shows no registration-mode use afterwards.
- [ ] **Step 4: Run** `make test && make test-conformance` -> PASS.
- [ ] **Step 5: Commit** `feat(scopes): scopes, scoped channels and scoped agent identities with bound tokens (spec 4.1, 4.2, Decisions 8, 9, 17)`.

---

### Task 5: Scope retirement (the maintenance job, Decision 19 scrub, `scope_idle_hours`)

**Files:**
- Create: `internal/core/scopes/retire.go`
- Modify: `internal/core/maintenance/service.go` (`Service.Archiver`, the job list), `internal/core/maintenance/jobs.go` (`scopeRetirement`), `internal/core/config.go` (`ScopeIdleHours`, default 336, bounds 1 to 8760), `internal/server/config/config.go` (`scope_idle_hours`), `docs/ops/configuration.md`
- Test: `internal/conformance/bus/retire_test.go` (new), `internal/core/config_test.go`, `internal/server/config/config_test.go`

**Interfaces:**
- Consumes: Tasks 1 and 2 Store methods; Task 4 `scopes.Service`; `tasks.Service.ReclaimChannel` through the existing "abandoned tasks" job.
- Produces:
  - `type Archiver interface { ArchiveBridge(ctx context.Context, b store.Bridge, reason string) error }` in `internal/core/scopes` (idempotent: posts the final line, archives in Slack, marks the row `archived`); reasons `ReasonDone = "done"`, `ReasonNoHumans = "no humans"`, `ReasonIdle = "idle"`, `ReasonRemoved = "removed"`, `ReasonChannelGone = "channel gone"`.
  - `func (s Service) RetireDue(ctx context.Context, now int64, a Archiver) (int, error)`: for each open or done scope, due when (1) done `with_bridge` and none of its bridges is `creating` or `live`; (2) done `normal` and it has no channel; (3) `LastActivity < now - ScopeIdleHours*hour`, whatever its state. For a due scope, each bridge still `creating` or `live` goes to `a.ArchiveBridge(ctx, b, reason)` first (reason `idle` for rule 3); an error leaves the scope for the next tick; `a == nil` while such a bridge exists holds the scope (not retired, no row deleted) and logs a warning naming the scope and the Slack channel id on each tick, so a Slack channel with humans in it is never orphaned (the operator restores the Slack settings, or archives by hand and runs `bridges remove` once they are back). Then `Retire`.
  - `func (s Service) Retire(ctx context.Context, name string, now int64) (bool, error)`: a sequence of short transactions, each re-reading the scope (false when already retired or gone): one deleting the scope's bridges (map and drop rows cascade); one per channel (`tx.DeleteChannel`, every kind); one per scoped identity (`RetireIdentity`); and last `MarkScopeRetired`. This keeps the SQLite writer lock and the Postgres transaction short. Logs one line with the scope and counts. `scopes` imports neither `core/channels` nor `core/identities` (both import it); it works on `tx` only.
  - `func (s Service) PurgeRetired(ctx context.Context, now int64) (int, error)`: `DeleteRetiredScopes(now - SessionRetentionHours*hour)`.
  - `maintenance.Service.Archiver scopes.Archiver` (nil: the bridge is off); job `{"scope retirement", s.scopeRetirement}` placed after "lapsed sessions" and before "abandoned tasks", so the retired identities' claims (owners with no session left) are reclaimed in the same tick (Decision 19). `scopeRetirement` runs `RetireDue` then `PurgeRetired` and returns the sum.
  - `core.Config.ScopeIdleHours int`; `config.Config.ScopeIdleHours` (`scope_idle_hours`), mapped in `Config.Core()`.

State (Definition of Done D): a second tick finds no due scope (retired rows are skipped) and archives nothing (`ArchiveBridge` on an archived row is a no-op); a run that stopped part way through `Retire` leaves a scope that is still due by the same rule (its idle clock, its absent live bridges or its absent channels are unchanged), and the next tick finishes the remaining steps, each idempotent; two overlapping ticks are serialized by the maintenance lock, and `Retire` re-reads the state inside its transaction; a tick that archived in Slack and then failed before or inside the transaction leaves the bridge `archived` and the scope `open`/`done`, and the next tick retires it (rule 1 now holds for `with_bridge`; rule 3 still holds for idle) without a second Slack call.

- [ ] **Step 1: Write the failing tests** (bus suite, both engines; a `fakeArchiver` recording calls and optionally failing once, marking the row archived through `h.tx`)
  - `TestScopeRetirementRules`: (normal) `job1` done `normal` with channel `job1-chat` -> `RunJob("scope retirement")` returns 0; delete the channel -> next run retires it. (with_bridge) `job2` with a live bridge row, done `with_bridge` -> not retired; bridge archived -> retired, bridge and map rows gone. (idle) `job3` open, clock advanced `ScopeIdleHours + 1h` -> archiver called once with reason `idle`, then retired; with `Archiver == nil` and a live bridge the scope is held (not retired, bridge and map rows intact) and each tick logs the warning; once the bridge is archived through `h.tx`, the next tick retires it.
  - `TestScopeRetirementResumesPartialRun`: a `faultStore` failing the third transaction of `Retire` (after one channel is deleted) -> the scope stays; the next tick deletes the rest and marks it retired.
  - `TestScopeRetirementScrub` (Decision 19): `job1-coder` (scoped) holds an ACL entry and a subscription on unscoped `ops`, claimed task 1 on unscoped tasks list `backlog`, sent a message on `ops`; human `eric@corp.example` and channel `ops` exist; after `Tick`: the identity is gone; the `ops` ACL has no `job1-coder` row; task 1 is `pending` with no owner; the message on `ops` stays; eric and `ops` are untouched; `newBus(h, "job1-coder")` calls answer `not_registered` (not `h.as`: its `ensureIdentity` would hit the retired-name `ErrConflict` in the fixture); `CreateAgentIdentity{Name: "job1-coder"}` -> conflict; `Get("job1")` -> not_found "is retired".
  - `TestScopeRetirementIdempotent`: a second `RunJob` returns 0 and the archiver count is unchanged; two siblings `Tick` at once -> exactly one `ran`.
  - `TestScopeRetirementAfterArchiveFailure`: the archiver fails once -> the scope stays; the next tick archives and retires.
  - `TestRetiredScopePurged`: after `SessionRetentionHours + 1h`, `Get` -> not_found "does not exist".
  - `TestScopeIdleHoursBounds` (core and server config): 0 and 8761 refused naming `scope_idle_hours`; default 336.

- [ ] **Step 2: Run to verify failure** — `go test -count=1 -p 1 -run 'TestScopeRetirement|TestRetiredScopePurged' ./internal/conformance/bus/` -> FAIL ("unknown maintenance job").
- [ ] **Step 3: Implement**; docs: the `scope_idle_hours` row ("1 to 8760. A scope with no message on any of its channels, in either direction, for this long is retired with everything in it.").
- [ ] **Step 4: Run** `make test && make test-conformance` -> PASS; `TestReplicasMaintenanceExclusive` counts unchanged.
- [ ] **Step 5: Commit** `feat(maintenance): scope retirement with the Decision 19 scrub (spec 4.3)`.

---

### Task 6: Live-bridge guards in the channel rules (spec 6.1b, 6.4)

**Files:**
- Modify: `internal/core/channels/channels.go` (`SetACL`, `Delete`, `Rename`)
- Test: `internal/conformance/bus/bridgeguard_test.go` (new)

**Interfaces:**
- Consumes: Task 2 `Tx.BridgeByChannel`; Task 4 `scopes.CheckBridgeACL`; `core.RequireAdmin`.
- Produces: with a bridge in state `creating` or `live` on the channel: `SetACL` requires the server admin flag (`RequireAdmin`; a channel `admin` holder gets `forbidden` "only a server administrator may change the ACL of a bridged channel") and then `CheckBridgeACL(entries)`; `Delete` and `Rename` -> `conflict` "channel %q has a live Slack bridge; run bridges remove first". An `archived` bridge or no bridge: unchanged rules.

- [ ] **Step 1: Write the failing test** `TestBridgedChannelGuards` (bus suite, both engines and transports): channel `job1-chat` created by scoped `job1-coder` with ACL `[{job1-coder [read write admin]}, {eric@corp.example [read write]}, {slack-bridge [read]}, {admins [admin]}]`, a live bridge row; `job1-coder` `SetACL` -> forbidden; admin `adm` adding `{everyone [read]}` -> forbidden; `{admins [read]}` -> forbidden; dropping the slack-bridge entry -> forbidden; adding `{kim@corp.example [write]}` -> success; `DeleteChannel` and `RenameChannel` as `adm` -> conflict; after `SetBridgeArchived`, delete succeeds; an unbridged channel's creator still sets its ACL.
- [ ] **Step 2: Run to verify failure** — `go test -count=1 -p 1 -run TestBridgedChannelGuards ./internal/conformance/bus/` -> FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `make test && make test-conformance` -> PASS; `TestACLMatrix` still 52 (`go test -count=1 -p 1 -v ./internal/conformance/bus/ > /tmp/m3.log; bash scripts/m3-exit-counts.sh /tmp/m3.log`).
- [ ] **Step 5: Commit** `feat(channels): a live bridge pins its channel's ACL, name and existence (spec 6.1b, Decision 12)`.

---

### Task 7: Scopes, scoped identities and bridges on the wire and the Bus

**Files:**
- Create: `internal/server/scopes.go`, `internal/server/bridges.go`, `internal/bus/bridges.go`, `internal/bus/local/scopes.go`, `internal/bus/remote/scopes.go`
- Modify: `api/openapi.yaml`, `internal/gen/*` (generated), `internal/bus/admin.go` (`Admin` methods), `internal/server/server.go` (`Options.Bridges`), `internal/server/api.go` (`scopes` service), `internal/server/admin_auth.go` (`CreateAgentIdentity`), `internal/server/channels.go` (scope in and out), `internal/server/convert.go`, `internal/bus/remote/convert.go`, `internal/bus/remote/channels.go`, `internal/bus/local/channels.go`
- Test: `internal/conformance/bus/scopes_wire_test.go` (new), `internal/server/bridges_test.go`, `internal/bus/remote/scopes_test.go`

**Interfaces:**
- Consumes: Task 4 services and bus types.
- Produces:
  - Package `bus` (`bridges.go`): `type BridgeState string` (`BridgeCreating`, `BridgeLive`, `BridgeArchived`); `type Bridge struct { Channel, Scope, SlackChannelID, SlackName string; State BridgeState; CreatedAt, ArchivedAt int64 }`; `type CreateBridgeInput struct{ Channel, SlackName string }`; `type BridgeNotInvited struct{ Identity, Reason string }`; `type BridgeCreated struct { Bridge Bridge; NotInvited []BridgeNotInvited }`; `type RemoveBridgeInput struct{ Channel string }`; `const BridgeNotConfigured = "the Slack bridge is not configured"`.
  - `bus.Admin` gains `CreateScope(ctx, CreateScopeInput) (Scope, error)`, `ListScopes(ctx) ([]Scope, error)`, `GetScope(ctx, name string) (Scope, error)`, `DoneScope(ctx, DoneScopeInput) (Scope, error)`, `CreateAgentIdentity(ctx, CreateAgentIdentityInput) (AgentIdentity, error)`, `CreateBridge(ctx, CreateBridgeInput) (BridgeCreated, error)`, `ListBridges(ctx) ([]Bridge, error)`, `GetBridge(ctx, channel string) (Bridge, error)`, `RemoveBridge(ctx, RemoveBridgeInput) error`. `local.Bus` bridges methods answer `forbidden` `BridgeNotConfigured` (local mode has no Slack).
  - `server.Bridges` interface: `Create(ctx, admin string, in bus.CreateBridgeInput) (bus.BridgeCreated, error)`, `List(ctx, admin string) ([]bus.Bridge, error)`, `Get(ctx, admin, channel string) (bus.Bridge, error)`, `Remove(ctx, admin string, in bus.RemoveBridgeInput) error`; `Options.Bridges Bridges` (nil: every bridge operation answers `forbidden` `BridgeNotConfigured`).
  - OpenAPI (tag admin, problem+json default, `additionalProperties: false` like the neighbors): `POST /v1/admin/scopes` `createScope` (`IdempotencyKeyParam`, body `CreateScopeRequest {name}`, 200 `Scope`); `GET /v1/admin/scopes` `listScopes` (200 `{scopes: [Scope]}`); `GET /v1/admin/scopes/{scope}` `getScope`; `POST /v1/admin/scopes/{scope}/done` `doneScope` (`IdempotencyKeyParam`, body `DoneScopeRequest {retire: enum [with_bridge, normal]}`); `POST /v1/admin/identities` `createAgentIdentity` (body `{name, scope?}`, 200 `AgentIdentity {identity, scope?, created, enrollment_token, expires_at}`); `POST /v1/admin/bridges` `createBridge` (body `{channel, slack_name?}`, 200 `BridgeCreated {bridge, not_invited: [{identity, reason}]}`); `GET /v1/admin/bridges` `listBridges`; `GET /v1/admin/bridges/{channel}` `getBridge`; `DELETE /v1/admin/bridges/{channel}` `removeBridge` (204). Schemas `Scope {name, state enum [open, done, retired], retire? enum, created_at, done_at?, last_activity, channels?, identities?}`, `Bridge {channel, scope, slack_channel_id?, slack_name, state enum [creating, live, archived], created_at, archived_at?}`; `CreateChannelRequest.scope?`; `ChannelInfo.scope?`.
  - `remote.Bus` converters refuse an unknown `state` or `retire` with `CodeInternal` naming the member (Definition of Done B).

- [ ] **Step 1: Write the failing tests**
  - `TestScopesBus` (bus suite, both engines and transports): create, list, get (with `Channels` and `Identities`), done; a replayed idempotency key returns the first result; non-admin forbidden on each; retired get -> not_found "is retired".
  - `TestCreateAgentIdentityBus`: admin gets a token and `Created`; non-admin forbidden; done scope conflict.
  - `TestChannelScopeBus`: `CreateChannel{Scope: "job1"}` by an admin returns `ChannelInfo.Scope == "job1"`; `Status` lists it with the scope.
  - `TestBridgesUnconfiguredForbidden` (bus suite, both transports): each of the four bridge calls -> `forbidden` with message `BridgeNotConfigured`.
  - `TestBridgesWire` (server unit, a stub `Bridges` recording calls): create maps body to `CreateBridgeInput` and back, `not_invited` always present (empty array); remove answers 204; a stub `not_found` answers 404 problem+json; `retire: "later"` on `doneScope` answers 400 before the handler.
  - `TestFromGenScopeRejectsUnknownState` (remote): state `"paused"` -> `CodeInternal` naming `state`; same for a bridge.
- [ ] **Step 2: Run to verify failure** — `go test -count=1 -p 1 -run 'TestScopesBus|TestCreateAgentIdentityBus|TestChannelScopeBus|TestBridgesUnconfiguredForbidden' ./internal/conformance/bus/` -> FAIL to compile.
- [ ] **Step 3: Implement**; `make generate`.
- [ ] **Step 4: Run** `make generate-check; make test && make test-conformance && bash scripts/m4-exit-checks.sh --deps-only` -> PASS (`AGENTBUS2_BUS_TRANSPORT=inproc` run included).
- [ ] **Step 5: Commit** `feat(api): scopes, scoped agent identities and bridge operations on the wire (spec 4.1, 6.1a, Decision 17)`.

---

### Task 8: CLI client commands (`scopes`, `bridges`, `identities`, `channels create -scope`)

**Files:**
- Create: `internal/cli/scopes.go`, `internal/cli/bridges.go`, `internal/cli/identities.go`, `internal/cli/scopes_test.go`, `internal/cli/bridges_test.go`
- Modify: `internal/cli/cli.go` (registry and help), `internal/cli/channels.go` (`-scope`), `README.md` (command list), `docs/ops/admin-bootstrap.md` (the pai walk-through), `CLAUDE.md` (client commands line)

**Interfaces:**
- Consumes: Task 7 `bus.Admin` methods.
- Produces: `agentbus2 scopes create [-key K] NAME | list | get NAME | done -retire with_bridge|normal [-key K] NAME`; `agentbus2 bridges create [-slack-name N] CHANNEL | list | get CHANNEL | remove CHANNEL`; `agentbus2 identities create [-scope S] NAME`; `agentbus2 channels create ... [-scope S] NAME`. Each takes `-profile`/`-identity`, prints one JSON document, maps `bus.Error` to exit 1 as today; an unknown action, a missing argument or `-retire` outside the two values exits 2 naming the allowed values; flags before positional arguments.

- [ ] **Step 1: Write the failing tests** (over the CLI test environment of `testenv_test.go`, a local profile whose identity is made admin)
  - `TestScopesCommands`: create prints `{"name":"job1","state":"open",...}`; get lists channels; `done -retire normal job1` prints state `done`; `done -retire later job1` exits 2 naming `with_bridge, normal`; `scopes frob` exits 2.
  - `TestIdentitiesCreateCommand`: prints `identity`, `scope`, `enrollment_token`, `expires_at`.
  - `TestChannelsCreateScope`: `channels create -scope job1 job1-chat` prints `"scope":"job1"`.
  - `TestBridgesCommandsUnconfigured`: `bridges list` on a local profile exits 1 with `agentbus2: forbidden: the Slack bridge is not configured`; `bridges create` with no channel exits 2.
- [ ] **Step 2: Run to verify failure** — `go test -run 'TestScopesCommands|TestIdentitiesCreateCommand|TestChannelsCreateScope|TestBridgesCommands' ./internal/cli/` -> FAIL.
- [ ] **Step 3: Implement**; docs: the walk-through (pai creates scope, agents, humans by email, the channel with a named-only ACL, the bridge, and later `scopes done -retire with_bridge`).
- [ ] **Step 4: Run** `make test && bash scripts/m6-exit-checks.sh --deps-only` -> PASS.
- [ ] **Step 5: Commit** `feat(cli): scopes, bridges and identities commands (spec 4.1, 6.1a)`.

---

### Task 9: The Slack seam (`internal/slackapi`) and the fake (`internal/slackapi/slackfake`)

**Files:**
- Create: `internal/slackapi/slackapi.go`, `internal/slackapi/slackapi_test.go`, `internal/slackapi/slackfake/fake.go`, `internal/slackapi/slackfake/socket.go`, `internal/slackapi/slackfake/fake_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces (package `slackapi`, standard library only):
  - Errors: `ErrNameTaken`, `ErrAlreadyArchived`, `ErrChannelGone` (`channel_not_found`, `is_archived`), `ErrChannelNotFound` (lookup miss), `ErrUserNotFound` (`users_not_found`, `user_not_found`); `type RateLimitedError struct{ Method string; RetryAfter time.Duration }`; `type APIError struct{ Method, Code string }` (any other Slack error code). No error text ever carries a token.
  - `type Self struct{ TeamID, UserID, BotID string }`; `type User struct { ID, TeamID, Email, DisplayName string; IsBot, Deleted, Restricted, UltraRestricted, Stranger bool }`; `type Message struct { TS, ThreadTS, User, BotID, Subtype, Text string; ReplyCount int; MetadataSeq int64 }` (`MetadataSeq` 0: none); `type Page struct { Messages []Message; NextCursor string }`; `type ChannelInfo struct{ ID, Name string; Archived bool }`; `type InviteFailure struct{ User, Code string }`; `type PostInput struct { Channel, Fallback, Section string; Context []string; Username, IconEmoji, ThreadTS string; Seq int64 }` (`Section` is mrkdwn, `Context` the tag chips, `Seq` goes into message metadata `{"event_type": "agentbus2_message", "event_payload": {"seq": N}}`).
  - `type EventKind string` (`EventConnected = "connected"`, `EventMessage = "message"`, `EventMemberLeft = "member_left_channel"`); `type Event struct { Kind EventKind; EnvelopeID, Channel, User string; Message Message }`.
  - `type Client interface { AuthTest(ctx) (Self, error); CreatePrivateChannel(ctx, name string) (string, error); FindPrivateChannel(ctx, name string) (ChannelInfo, error); Invite(ctx, channelID string, userIDs []string) ([]InviteFailure, error); Archive(ctx, channelID string) error; Post(ctx, in PostInput) (string, error); History(ctx, channelID, oldest, cursor string) (Page, error); Replies(ctx, channelID, threadTS, oldest, cursor string) (Page, error); Members(ctx, channelID string) ([]string, error); UserInfo(ctx, userID string) (User, error); LookupByEmail(ctx, email string) (User, error); RunSocket(ctx context.Context, handle func(context.Context, Event)) error }`. `Invite` invites one user per Web API call (no `force`), so each user's error code is visible, collects each failure as an `InviteFailure` and treats `already_in_channel` as success; `RunSocket` acks each envelope before calling `handle`, calls `handle` with `EventConnected` after every (re)connect, and returns nil when ctx ends.
  - `func ValidTS(ts string) bool` (`^[0-9]{10}\.[0-9]{6}$`); `func ValidChannelName(name string) bool` (1 to 80 of `a-z0-9-_`).
- Produces (package `slackfake`, imports the standard library and `slackapi`): `type Fake struct` implementing `slackapi.Client`; `func New(self slackapi.Self) *Fake`; `AddUser(slackapi.User)`; `Say(channel, user, text, threadTS string) slackapi.Message` (adds to history with the next ts, from `"1700000000.000001"` upward, and queues the event); `SayAsBot(channel, botID, text, threadTS string, seq int64) slackapi.Message`; `Join(channel, user string)`, `Leave(channel, user string)` (queues `member_left_channel`); `ArchiveByHand(channel string)`; `Emit(slackapi.Event)`; `DropNext(n int)`, `RepeatNext()`, `ReorderNext(n int)` (the next n events are delivered in reverse order); `Disconnect()` (the running `RunSocket` emits `EventConnected` again); `FailNext(method string, err error)`; `RateLimit(method string, times int, retryAfter time.Duration)`; `Channel(id string) (FakeChannel, bool)` with `FakeChannel{ ID, Name string; Private, Archived bool; Members []string; Messages []slackapi.Message; Posts []slackapi.PostInput }`; `Calls(method string) int`; `Acks() []string`; `MaxConcurrentSockets() int`; `SocketDone() <-chan struct{}`. It enforces Slack's rules: invalid names -> `APIError{Code: "invalid_name"}`, duplicate names -> `ErrNameTaken`, posts to an archived channel -> `ErrChannelGone`, archive twice -> `ErrAlreadyArchived`, unknown user -> `ErrUserNotFound`.

- [ ] **Step 1: Harness self-tests first** (Definition of Done C)
  - `TestValidTSAndName` (slackapi, table): `1700000000.000100` ok; `170000000.0001`, `"x"`, `""` refused; names `job-1_a` ok, `Job`, `a b`, 81 chars, `""` refused.
  - `TestFakeChannelRules`: create, duplicate -> `ErrNameTaken`, `Job` -> `invalid_name`, archive twice -> `ErrAlreadyArchived`, post after archive -> `ErrChannelGone`, `FindPrivateChannel` finds by name, invite of a member is success, invite of an unknown user reports a failure (`user_not_found`) and still invites the others.
  - `TestFakeSocketAckBeforeHandle`: the ack of an envelope is recorded before `handle` sees it; the first event is `EventConnected`.
  - `TestFakeDropRepeatReorder`: drop 1 of 3 -> 2 delivered, all 3 in history; repeat -> the same envelope twice; reorder 3 -> reverse order.
  - `TestFakeDisconnectReconnects`: `Disconnect` -> a second `EventConnected`; `MaxConcurrentSockets() == 1`.
  - `TestFakeRateLimitAndFail`: `RateLimit("Post", 2, time.Second)` -> two `*RateLimitedError` with `RetryAfter == 1s`, then success; `FailNext("Archive", err)` once.
  - `TestFakeHistoryReplies`: `History` with `oldest` excludes it and returns ascending ts; `Replies` returns the root first then its replies newer than `oldest`; paging by `NextCursor` with a page size of 2.
  - `TestFakeStopsWithContext`: `RunSocket` returns nil within 1 s of cancel, and the channel from `SocketDone()` (closed by the fake when its delivery goroutine has exited) is closed within 1 s.
- [ ] **Step 2: Run to verify failure** — `go test ./internal/slackapi/...` -> FAIL to compile.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race -count=1 ./internal/slackapi/... && make lint` -> PASS.
- [ ] **Step 5: Commit** `feat(slackapi): the Slack client seam and an in-memory fake with drop, repeat and reorder (spec 9)`.

---

### Task 10: The slack-go adapter (`internal/slackapi/slackgo`), the dependency and the import rule

**Files:**
- Create: `internal/slackapi/slackgo/client.go`, `internal/slackapi/slackgo/socket.go`, `internal/slackapi/slackgo/client_test.go`, `internal/slackapi/slackgo/socket_test.go`
- Modify: `go.mod`, `go.sum`, `scripts/m7-exit-checks.sh` (import rule, allowlist, importer scan, header), `scripts/check-gates-selftest.sh` (five `deps_probe` lines and the block 22 header comment), `CLAUDE.md` (import rule line)

**Interfaces:**
- Consumes: Task 9 `slackapi`.
- Produces: `func New(appToken, botToken string, opts Options) (*Client, error)` with `type Options struct { APIURL string; HTTP *http.Client; Logger *slog.Logger }` (`APIURL` "" is Slack's; tests point it at `httptest`); `New` refuses a bot token without the `xoxb-` prefix or an app token without `xapp-` (the error names the setting, never the value). `*Client` implements `slackapi.Client` over `slack.New(botToken, slack.OptionAppLevelToken(appToken), slack.OptionAPIURL(...))` and `socketmode.New`: `CreatePrivateChannel` -> `CreateConversationContext{IsPrivate: true}`; `FindPrivateChannel` -> `GetConversationsContext{Types: ["private_channel"], ExcludeArchived: false}` paged; `Invite` -> one `InviteUsersToConversationContext(ctx, channelID, user)` per user (slack-go's force variant drops the per-user `errors` array), the error code of each failure mapped to `InviteFailure{User, Code}`, `already_in_channel` success, `*slack.RateLimitedError` returned as is so the caller can wait and retry the rest; `Archive` -> `ArchiveConversationContext`; `Post` -> `PostMessageContext` with `MsgOptionBlocks` (a section block and, with tags, a context block), `MsgOptionText(Fallback)`, `MsgOptionUsername`, `MsgOptionIconEmoji`, `MsgOptionTS` for a thread, `MsgOptionMetadata`; `History`/`Replies` with `IncludeAllMetadata: true`; `Members` -> `GetUsersInConversationContext` paged; `UserInfo`/`LookupByEmail` map `is_restricted`, `is_ultra_restricted`, `is_stranger`, `is_bot`, `deleted`, `team_id`, `profile.email`, `profile.display_name` (else `real_name`). Error mapping: `name_taken` -> `ErrNameTaken`; `already_archived` -> `ErrAlreadyArchived`; `channel_not_found`, `is_archived` -> `ErrChannelGone`; `users_not_found`, `user_not_found` -> `ErrUserNotFound`; `*slack.RateLimitedError` -> `*slackapi.RateLimitedError`; others -> `*slackapi.APIError`. `RunSocket`: `socketmode` `EventTypeConnected` -> `EventConnected`; `EventTypeEventsAPI` -> `Ack(*evt.Request)` first, then `slackevents.MessageEvent` -> `EventMessage`, `slackevents.MemberLeftChannelEvent` -> `EventMemberLeft`, every other inner type ignored at debug level; `EventTypeInvalidAuth` ends `RunSocket` with an error; other socketmode events (connecting, hello, disconnect, errors) are logged at debug and left to socketmode's reconnect.
- Import rule (added to `import_check` of `scripts/m7-exit-checks.sh`, header updated to "Milestone 7 and Slack bridge import rules"): `check_allowlist` (ported from `scripts/m5-exit-checks.sh`) `./internal/slackapi "$MOD/internal/slackapi"` and `./internal/slackapi/slackfake "$MOD/internal/slackapi"` (no third-party package at all); `check_importers github.com/slack-go/slack "$MOD/internal/slackapi/slackgo"`, a new function that scans the direct imports of every package of `./...` (`go list -f '{{.ImportPath}} {{join .Imports " "}}'`) and prints `deps_violation=<pkg> imports github.com/slack-go/slack` for any other importer (prefix match); `check_deps ./internal/slackapi internal/core internal/store internal/server internal/bus github.com/slack-go/slack`; `check_deps ./internal/slackapi/slackfake internal/core internal/store internal/server internal/bus github.com/slack-go/slack`; `check_deps './internal/core/...' internal/slackapi internal/slackbridge github.com/slack-go/slack`; the same for `./internal/bus/...`, `./internal/cli`, `./internal/mcpserver`, `./internal/client` and `./internal/server` (the package only). The `./internal/slackbridge` line (`internal/server internal/gen internal/slackapi/slackgo github.com/slack-go/slack`) is added in Task 11, which creates the package (`go list` of a missing package fails the script).

- [ ] **Step 1: Add the dependency** — `go get github.com/slack-go/slack@v0.30.1 && go mod tidy`; `CGO_ENABLED=0 go build ./...` succeeds.
- [ ] **Step 2: Write the failing adapter tests** against an `httptest` server that answers Slack's JSON (`{"ok": false, "error": "name_taken"}` and so on) and, for the socket, `apps.connections.open` returning a `ws://` URL served by `github.com/gorilla/websocket` in the test
  - `TestCreatePrivateChannelParams`: the request has `is_private=true` and the name; `name_taken` -> `ErrNameTaken`.
  - `TestInvitePerUser`: three users -> three `conversations.invite` requests, one `users` value each and no `force`; the httptest server answers the second with `{"ok":false,"error":"cant_invite_guest"}` and the third with `already_in_channel` -> failures exactly `[{U2 cant_invite_guest}]`.
  - `TestArchiveErrors`: `already_archived` -> `ErrAlreadyArchived`; `is_archived` on post -> `ErrChannelGone`.
  - `TestPostParams`: `username`, `icon_emoji`, `thread_ts`, blocks with a section and a context element per tag, metadata `{"event_type":"agentbus2_message","event_payload":{"seq":42}}`.
  - `TestRateLimitedMapped`: HTTP 429 with `Retry-After: 3` -> `*RateLimitedError{RetryAfter: 3s}`.
  - `TestUserMapping`: `is_restricted`, `is_ultra_restricted`, `is_stranger`, `team_id`, email and display-name fallback.
  - `TestHistoryIncludesMetadata`: the request has `include_all_metadata=true`; a message's metadata seq maps to `MetadataSeq`.
  - `TestNewRefusesTokenShapes`: `New("xoxb-..", "xoxb-..")` errors naming `slack_app_token_file`; the error text contains no token.
  - `TestRunSocketAcksFirst`: the server sends `hello`, then an `events_api` envelope with a `message` event; the server receives `{"envelope_id": ...}` before the handler returns; the handler saw `EventConnected` then `EventMessage`; an unknown inner event type is acked and not handled; a `disconnect` envelope leads to a second `EventConnected`.
- [ ] **Step 3: Run to verify failure** — `go test ./internal/slackapi/slackgo/` -> FAIL.
- [ ] **Step 4: Implement**; extend `m7-exit-checks.sh` and the selftest: probes `internal/slackapi/probe_m7.go` importing `$m/internal/store` (want `deps_violation=$m/internal/slackapi imports $m/internal/store`), `internal/slackapi/slackfake/probe_m7.go` importing `github.com/jmoiron/sqlx` (want `deps_violation=$m/internal/slackapi/slackfake imports github.com/jmoiron/sqlx`, the allowlist), `internal/auth/keys/probe_m7.go` importing `github.com/slack-go/slack` (want `deps_violation=$m/internal/auth/keys imports github.com/slack-go/slack`, the importer scan), `internal/core/scopes/probe_m7.go` importing `github.com/slack-go/slack` (want `deps_violation=$m/internal/core/scopes imports github.com/slack-go/slack`), `internal/cli/probe_m7.go` importing `$m/internal/slackapi` (want `deps_violation=$m/internal/cli imports $m/internal/slackapi`).
- [ ] **Step 5: Run** `make tidy-check && make test && bash scripts/m7-exit-checks.sh --deps-only && make check-gates-selftest` -> PASS, `deps_ok=1`, the five new probes fire.
- [ ] **Step 6: Commit** `feat(slackapi): slack-go adapter with Socket Mode; Slack import rule (spec 5.1, 5.2)` with `go.mod go.sum internal/slackapi scripts CLAUDE.md`.

---

### Task 11: Bridge formatting rules (pure functions in `internal/slackbridge`)

**Files:**
- Create: `internal/slackbridge/format.go`, `internal/slackbridge/mrkdwn.go`, `internal/slackbridge/permalink.go`, `internal/slackbridge/format_test.go`, `internal/slackbridge/mrkdwn_test.go`, `internal/slackbridge/permalink_test.go`
- Modify: `scripts/m7-exit-checks.sh` (the `./internal/slackbridge` line of Task 10)

**Interfaces:**
- Consumes: Task 9 `slackapi.PostInput`, `ValidChannelName`.
- Produces:
  - `func SlackChannelName(busName string) (string, error)`: lowercase, every character outside `a-z0-9-_` becomes `-`, cut to 80 bytes; a result that is all `-` or empty -> error "channel %q has no usable Slack name; pass -slack-name" (spec 6.1 step 1).
  - `func ShortName(identity, scope string) string`: `identity` without the prefix `scope + "-"` when it starts with it and something remains; else `identity` (spec 6.2).
  - `func IconEmoji(identity string) string`: FNV-32a of the identity over the fixed list `:robot_face:`, `:gear:`, `:hammer_and_wrench:`, `:mag:`, `:memo:`, `:satellite:`, `:test_tube:`, `:jigsaw:` (plan choice, builder's call).
  - `func MarkdownToMrkdwn(md string) string`: `**x**`/`__x__` -> `*x*`; `*x*`/`_x_` -> `_x_`; `~~x~~` -> `~x~`; `[t](u)` -> `<u|t>`; `# h` (any level) -> `*h*`; `- `/`* ` list items -> `• `; `&`, `<`, `>` escaped outside links; inline code and fenced blocks pass through unchanged.
  - `func SlackToMarkdown(text string, name func(userID string) string) string`: `<url|t>` -> `[t](url)`; `<url>` -> `url`; `<@U..>` -> `@` + `name(U..)`; `&amp;`, `&lt;`, `&gt;` unescaped.
  - `func Truncate(s string, max int, note string) string`: when `utf8.RuneCountInString(s) > max`, cut by runes so that the result plus `"\n" + note` has at most `max` runes, moving the cut back before an open `<...>` link or an unclosed code fence (closing the fence); else `s`.
  - `func FormatPost(m bus.Message, scope string, maxChars int, parentQuote string) slackapi.PostInput`: `Section` = `*subject*` line (when set), then `> ` quoted `parentQuote` (when set), then the mrkdwn content, truncated with note `"(truncated; the full message is seq <N> on agentbus2 channel <C>)"`; `Context` = one `` `tag` `` element per tag; `Fallback` = the plain first 150 runes; `Username` = `ShortName`; `IconEmoji`; `Seq` = `m.Seq`.
  - `type Permalink struct{ Channel, TS string }`; `func FindPermalinks(text string) []Permalink`: `https://<host>.slack.com/archives/<C|G…>/p<16 digits>` (query ignored), ts = digits with a dot before the last six; the raw text, including `<url|t>` forms.

- [ ] **Step 1: Write the failing tests** (table-driven)
  - `TestSlackChannelName`: `"pai-job-12345"` same; `"Job_1 Alpha"` -> `"job_1-alpha"`; 100 chars -> 80; `"日本"` -> error.
  - `TestShortName`: `("pai-job-1-coder", "pai-job-1")` -> `coder`; `("pai-job-1", "pai-job-1")` -> `pai-job-1`; `("coder", "pai-job-1")` -> `coder`.
  - `TestIconEmojiStable`: same identity same emoji; result in the list.
  - `TestMarkdownToMrkdwn`: each rule above, plus `a < b & c` escaped, code fence untouched, a link inside bold.
  - `TestSlackToMarkdown`: links, mentions, entities.
  - `TestTruncateKeepsMarkup` (Review Focus 3): emoji-heavy content counted by runes; a cut inside `<u|t>` moves before it; an open fence is closed; result length ≤ max including the note; short input unchanged.
  - `TestFormatPost`: subject bold first line; tags as context; quote line; `Seq` carried.
  - `TestFindPermalinks`: plain, angle-bracket and `?thread_ts=` forms; wrong digit count ignored; two links found in order.
- [ ] **Step 2: Run to verify failure** — `go test ./internal/slackbridge/` -> FAIL to compile.
- [ ] **Step 3: Implement**; add the `./internal/slackbridge` line to `m7-exit-checks.sh`.
- [ ] **Step 4: Run** `go test ./internal/slackbridge/ && bash scripts/m7-exit-checks.sh --deps-only` -> PASS, `deps_ok=1`.
- [ ] **Step 5: Commit** `feat(slackbridge): Slack names, usernames, mrkdwn, truncation and permalinks (spec 6.1, 6.2, 6.3)`.

---

### Task 12: The bridge service (create and resume, list, get, remove, archive), configuration and wiring

**Files:**
- Create: `internal/slackbridge/service.go`, `internal/slackbridge/create.go`, `internal/slackbridge/archive.go`, `internal/slackbridge/service_test.go`, `internal/conformance/bus/slackbridge_create_test.go`, `docs/ops/slack-bridge.md`
- Modify: `internal/server/config/config.go` (four settings), `internal/server/cmd/cmd.go` (read token files, build `slackgo.Client`), `internal/server/cmd/serve.go` (`Config.Slack`, `Config.SlackCfg`; `slackbridge.New`; `server.Options.Bridges`; `maintenance.Service.Archiver`), `docs/ops/configuration.md`, `docs/ops/README.md`, `docs/ops/deploy.md` (what replicas share)
- Test: `internal/server/config/config_test.go`, `internal/server/cmd/cmd_test.go`

**Interfaces:**
- Consumes: Tasks 2, 4 (`RequireOpen`, `CheckBridgeACL`), 5 (`scopes.Archiver`), 7 (`server.Bridges`, bus types), 9, 10, 11 (`SlackChannelName`).
- Produces:
  - `type Config struct { ArchiveAfterDone time.Duration; MaxMessageChars int; PostInterval, LockRenew, LifecycleInterval time.Duration; Logger *slog.Logger }` (zero durations: 1 s, 10 s, 60 s).
  - `func New(ctx context.Context, c *core.Core, sc slackapi.Client, cfg Config) (*Service, error)`: validates `cfg` (`ArchiveAfterDone > 0`, `MaxMessageChars` 200 to 3000) and calls `AuthTest` once. Only `*slackapi.APIError{Code: "invalid_auth"}` fails `New` (a wrong token is an operator error); any other failure (network, 5xx, rate limit, another code) returns a Service that is not ready: `Run` retries `AuthTest` every `LockRenew` before it competes for the lock, and until it succeeds the bridge operations answer `unavailable` "the Slack bridge is not connected to Slack yet; retry" (retryable) and `ArchiveBridge` returns an error, so maintenance retries next tick.
  - `*Service` implements `server.Bridges` (`Create`, `List`, `Get`, `Remove`, each `RequireAdmin` first) and `scopes.Archiver` (`ArchiveBridge`).
  - `Create` (spec 6.1): (1) one `Store.Tx`: channel exists (`not_found`), kind ordinary (`validation` "a bridge needs an ordinary channel"), scoped (`validation` "channel %q has no scope; a bridge requires a scoped channel"), `RequireOpen`, `CheckBridgeACL` against the ACL plus the `{slack-bridge, [read]}` entry it will add; an existing bridge `live` or `archived` -> `conflict` "channel %q is already bridged"; `creating` -> resume with its stored name (a different `-slack-name` -> `conflict` naming the stored one); else `CreateBridge` with `SlackName = in.SlackName` (validated with `slackapi.ValidChannelName`, `validation` otherwise) or `SlackChannelName(channel)`. (2) No Slack id yet: `CreatePrivateChannel`; on `ErrNameTaken`, a resumed row looks up `FindPrivateChannel` and adopts a found, unarchived channel; otherwise the fresh row is deleted and the answer is `conflict` "Slack channel name %q is taken"; then `SetBridgeSlackChannel`. (3) For each identity with an explicit `write` entry whose `Kind` is human: `HumanBinding` email -> `LookupByEmail`; not found -> `NotInvited{reason: "not found in Slack"}`; restricted, ultra-restricted, stranger or another team -> `NotInvited{reason: "Slack guest or external user"}` and not invited (Decision 18); the rest go to one `Invite`; each `InviteFailure` -> `NotInvited{reason: "invite failed: <code>"}`. (4) One `Store.Tx`: `SetBridgeLive`, `SetACL` adding `{slack-bridge, [read]}`, `Subscribe(slack-bridge, channel, MaxSeq)`. (5) When `NotInvited` is non-empty: a Slack post as the bot and a bus message from `slack-bridge` of type `notice` (appended through `core.Append` with `SenderKind` agent, no ACL check) naming the identities, never emails.
  - `Remove` (spec 6.1a): `not_found` "channel %q has no bridge"; `ArchiveBridge(b, ReasonRemoved)`, then one `Store.Tx`: `DeleteBridge`, `Unsubscribe(slack-bridge)`, the ACL without the `slack-bridge` entry.
  - `ArchiveBridge(ctx, b, reason)`: no-op when `archived`; else post the final line "This channel is archived; the record stays in agentbus2 until its scope is retired." (an `ErrChannelGone` answer is ignored), `Archive` (`ErrAlreadyArchived` and `ErrChannelGone` are success), then `SetBridgeArchived` and `Unsubscribe(slack-bridge)` in one transaction; logs the channel and reason.
  - `List`/`Get` map `store.Bridge` to `bus.Bridge`.
  - Settings (`config.Config`, Decision 85, file paths only): `slack_app_token_file`, `slack_bot_token_file` (both or neither: "slack_app_token_file and slack_bot_token_file must be set together"), `slack_archive_after_done_hours` (168; 1 to 8760), `slack_max_message_chars` (3000; 200 to 3000). `cmd.Run` reads both files (trimmed; a read error exits 2 naming the setting), builds `slackgo.New`, and passes `Config.Slack slackapi.Client` and `Config.SlackCfg slackbridge.Config` to `Serve`; `Serve` calls `slackbridge.New` after `core.New` (only `invalid_auth` is a start-up error, naming the setting; anything else starts the server with the bridge not yet ready), sets `server.Options.Bridges` and `m.Archiver`. No Slack settings: nothing is built, and bridge calls answer `forbidden` (Task 7).

State (Definition of Done D): a second `Create` on a live channel answers `conflict`; a run killed after step 1 leaves a `creating` row that the next `Create` resumes with the same name; killed after step 2 the row has the Slack id and the next run skips creation and re-invites (`already_in_channel` is success); two overlapping creates share the unique row, the loser of the Slack name race adopts the winner's channel through `FindPrivateChannel`, and both end `live` with one Slack channel. `Remove` twice: the second answers `not_found`. `ArchiveBridge` twice or from two replicas: the second finds `archived` or gets `already_archived`.

- [ ] **Step 1: Write the failing tests**
  - `TestSlackBridgeCreate` (bus suite, both engines; `slackfake` with users alice (full member), gina (restricted), xavier (stranger), and no user for nobody@corp.example; humans created with `CreateIdentityByEmail`; channel `job1-chat` in scope `job1` with ACL `[{job1-coder rwa}, {alice@corp.example rw}, {gina@corp.example rw}, {xavier@corp.example rw}, {nobody@corp.example rw}, {admins [admin]}]`): `Create` -> bridge `live`, Slack channel private named `job1-chat`, members bot and alice only, `NotInvited` has gina and xavier ("guest or external") and nobody ("not found"); the ACL has `{slack-bridge [read]}`; `slack-bridge` is subscribed; a notice from `slack-bridge` on the bus and in Slack names the three identities and no email; second `Create` -> conflict "already bridged".
  - `TestSlackBridgeCreateRefusals`: non-admin forbidden; unscoped channel validation; memory channel validation; done scope conflict; ACL with `{everyone [read]}` forbidden; `-slack-name "Bad Name"` validation; a Slack name already taken by a channel the bot cannot see -> conflict and no bridge row left.
  - `TestSlackBridgeCreateResume`: `FailNext("Invite", err)` -> `Create` fails; the row is `creating` with a Slack id; the retry creates no second Slack channel (`Calls("CreatePrivateChannel") == 1`) and ends `live`. And: the row left `creating` without a Slack id while the fake already has the channel (simulated crash after the Slack call) -> the retry gets `ErrNameTaken`, adopts the channel through `FindPrivateChannel`, ends `live`.
  - `TestSlackBridgeRemoveAndArchive`: `Remove` posts the final line, archives, deletes the row and map rows, removes the ACL entry and subscription; the scope and channel stay; `Remove` again -> not_found; `ArchiveBridge` twice -> one `Archive` call and no error; `ArchiveBridge` on a channel archived by hand (`ArchiveByHand`) -> success, row `archived`; re-`Create` after `Remove` with the same name -> conflict (Slack keeps the archived name), with a new `-slack-name` -> live.
  - `TestSlackSettings` (server config): one token file alone refused with the message above; hours and chars bounds; defaults 168 and 3000.
  - `TestSlackBridgeNotReady` (bus suite): the fake's `AuthTest` fails with a network error twice -> `New` succeeds, `List` answers `unavailable`; `Run` retries and, after the third `AuthTest` succeeds, `List` works; `AuthTest` answering `invalid_auth` -> `New` fails with an error that names no token.
  - `TestRunSlackTokenFiles` (cmd): a missing token file exits 2 naming `slack_bot_token_file`; an app token without `xapp-` exits 2 and stderr contains no file content; docs rows exist (`go test ./internal/server/config/`).
- [ ] **Step 2: Run to verify failure** — `go test -count=1 -p 1 -run 'TestSlackBridgeCreate|TestSlackBridgeRemoveAndArchive' ./internal/conformance/bus/` -> FAIL to compile.
- [ ] **Step 3: Implement.** Docs: four rows in `configuration.md`; `docs/ops/slack-bridge.md` (create a Slack app internal to one workspace, never publicly distributed; enable Socket Mode; app-level token `xapp-` with `connections:write`; bot scopes `groups:write`, `groups:write.invites`, `groups:history`, `groups:read`, `users:read`, `users:read.email`, `chat:write`, `chat:write.customize`; bot events `message.groups` and `member_left_channel`; install and copy the `xoxb-` token; write both tokens to files readable only by the server user, mode 0600; set `slack_app_token_file` and `slack_bot_token_file`; what pai does (scope, agents, humans, named-only ACL, `bridges create`, `scopes done`); archive and retirement rules; only full members count; one replica runs the bridge, every replica needs the token files to serve bridge calls; the crash repair reads message metadata back through `conversations.history` with `include_all_metadata`, which the fact check does not cover: before relying on it, verify on a real workspace that the bot reads its own message metadata with the scopes above (Slack names `message_metadata:read` only for events), and treat the repair as best effort: without it a crash between a post and its map row can duplicate one Slack post, never a bus message; an idle scope whose Slack settings were removed is held, not retired, until the settings return or the bridge is removed); link it from `docs/ops/README.md`; `deploy.md` "What replicas must share" names the token files.
- [ ] **Step 4: Run** `make test && make test-conformance && bash scripts/m7-exit-checks.sh --deps-only` -> PASS.
- [ ] **Step 5: Commit** `feat(slackbridge): bridge create with resume, list, get, remove and archive; Slack settings (spec 5.1, 6.1, 6.1a, 6.5)`.

---

### Task 13: Slack to bus (trust, bindings, threads, permalinks, catch-up)

**Files:**
- Create: `internal/slackbridge/inbound.go`, `internal/slackbridge/catchup.go`, `internal/conformance/bus/slackbridge_inbound_test.go`
- Modify: `internal/core/messaging/send.go` (`SendWith`, `AppendHook`), `internal/core/sessions/open.go` (`EnsureHinted`)
- Test: `internal/core/sessions/hinted_test.go` (core, SQLite file)

**Interfaces:**
- Consumes: Task 2 map, drop and binding methods; Task 11 `SlackToMarkdown`, `FindPermalinks`; Task 12 `*Service`; part 1 `LookupHumanByEmail`, `Kind`.
- Produces:
  - `type AppendHook func(ctx context.Context, tx store.Tx, seq int64) error`; `func (s Service) SendWith(ctx context.Context, identity string, in bus.SendInput, hook AppendHook) (bus.SendResult, error)`: `Send` is `SendWith(ctx, identity, in, nil)`; the hook runs inside the same `Store.Append` transaction right after `Append`, and an error from it rolls the whole send back.
  - `func (s Service) EnsureHinted(ctx context.Context, identity, hint string) (string, error)` (package `sessions`): one transaction; the identity's open (`ClosedAt == 0`) session with that hint gets its lease renewed and its id returned; none -> a new session with that hint (display `identity/hint`, resume token generated and discarded).
  - `func (s *Service) HandleEvent(ctx context.Context, ev slackapi.Event)`: `EventMessage` -> `handleMessage`; `EventMemberLeft` and every other kind ignored here (Task 14 adds the membership check for `EventMemberLeft`).
  - `handleMessage(ctx, channelID string, m slackapi.Message, catchUp bool) (outcome string, err error)` with outcomes `sent`, `duplicate`, `ignored`, `dropped`, `retry` (tests read them). Order: (1) the bridge by Slack channel, state `live`, else `ignored`; (2) `m.BotID == self.BotID`: during catch-up, an own post with `MetadataSeq` whose `BridgeMessageBySeq` is ErrNotFound gets, in one transaction, its map row and the acked mark advanced (crash repair, plan choice; the lookup comes first because a failed statement ends the transaction, `store.Store.Tx`), else `ignored`; (3) subtype not `""` or `thread_broadcast`, empty `User`, or invalid ts -> `ignored`; (4) ts mapped -> `duplicate`; ts in the drop set (`BridgeDropped`) -> `ignored` (a drop is final: a later ACL grant does not deliver an old message, and it is logged once); (5) `UserInfo` (cached for 10 minutes) and refuse `IsBot`, `Deleted`, `Restricted`, `UltraRestricted`, `Stranger`, `TeamID != self.TeamID`, empty email -> `dropped`; (6) `LookupHumanByEmail` (`not_found` -> `dropped`); (7) thread: `ThreadTS != "" && ThreadTS != TS` -> root map row: found -> `reply_to` = its seq and `slack_after` = `NewestInThread(root, TS)`; not found and the root (first of `Replies(channel, ThreadTS, "", "")`) is the app's own post -> top-level with the root's text quoted; not found otherwise -> top-level, no quote; (8) the first permalink to this Slack channel whose ts is mapped makes that seq the parent and `slack_after` (it overrides step 7; the core rewrites it to the root, Decision 14); (9) `EnsureHinted(identity, "slack")`; (10) `SendWith` as the human with `Content = SlackToMarkdown(text)` (with the quote first, as `> ` lines), `Metadata{"slack_display_name": ..., "slack_after": "<seq>" when set}`, the session, and a hook that: requires an explicit ACL entry for the identity holding `write` (`forbidden` otherwise -> `dropped`), checks and records the Slack user binding (`SlackUserOf` and `IdentityOfSlackUser` first; a mismatch -> `forbidden`; `PutSlackUser` only when both are absent, and any `ErrConflict` from it, a binding race, is turned into `forbidden` "Slack user binding conflict" by the hook, so a binding problem is never read as a duplicate; Decision 18), and writes `PutBridgeMessage{seq, ts, root = ThreadTS or TS}` after a `BridgeMessageByTS` lookup (found -> `conflict`); a `conflict` from `SendWith` (the whole send rolled back) -> `duplicate`; a `validation` (for example a message with empty text, only blocks or attachments) -> `ignored`; a `forbidden` -> `dropped`; `rate_limited`, `unavailable` or `internal` -> `retry` (the worker requeues the message with backoff from 1 s doubling to 30 s, at most 5 attempts, then leaves it to the next catch-up, which does not move the cursor past a `retry`). Every `dropped` records `PutBridgeDrop(bridge, ts)` in a transaction of its own and logs `slack message dropped` with `slack_user`, `slack_channel`, `reason`; never the text or email.
  - `func (s *Service) CatchUp(ctx context.Context) error`: for each `live` bridge: `History(oldest = CursorTS)` all pages; then `Replies(root, oldest = CursorTS)` for each root of `ThreadRoots(bridge, ts of now - 7 days, 200)` (plan choice: roots active within a week, at most 200 per pass, newest first; a skipped root is logged) and every history message with `ReplyCount > 0`; merge, dedupe by ts, sort ascending, run `handleMessage(..., true)` on each, then `AdvanceBridgeCursor` to the newest ts read, or to just below the oldest `retry` outcome when there was one. A failure stops that bridge without moving its cursor (the next catch-up repeats; the map makes it idempotent).

Plan choice (see the report): the cursor moves only when a catch-up completes, to the newest Slack ts it read; events never move it. A cursor that followed the bridge's own posts or event order would skip a human message posted during an outage before a later bridge post, or one delivered out of order.

- [ ] **Step 1: Write the failing tests**
  - `TestEnsureHinted` (core): first call creates `alice@corp.example/slack`; second returns the same id with a renewed lease; a closed one is replaced; hint failing `NameRule` -> validation.
  - `TestSendWithHookRollsBack` (core): a hook error leaves no message and `MaxSeq` unchanged.
  - `TestSlackBridgeInbound` (bus suite, both engines; a live bridge from `Create` as in Task 12; agent `job1-coder` subscribed): subtests
    - `authorized-top-level`: `fake.Say(G, aliceU, "hi *there*", "")` + `HandleEvent` -> outcome `sent`; `job1-coder` receives one message from `alice@corp.example`, `SenderKind == IdentityHuman`, `Display == "alice@corp.example/slack"`, `Metadata["slack_display_name"] == "Alice"`; one map row; one `slack_users` row.
    - `repeat-ignored`: the same event again -> `duplicate`, still one message.
    - `own-bot-ignored`, `subtypes-ignored` (`message_changed`, `message_deleted`, `channel_join`, `channel_leave`, `file_share`, `bot_message`, `weird`), `empty-user-ignored`.
    - `guest-dropped`, `ultra-restricted-dropped`, `stranger-dropped`, `other-team-dropped`, `deleted-user-dropped`, `no-human-dropped`, `not-in-acl-dropped` (a human without an ACL entry; then added with `write` by the admin -> next message `sent`), each with no bus message and a log line containing the Slack user id and channel and not the text.
    - `email-moved-to-other-account` (Review Focus 2): alice bound to U1; U2 with alice's email -> `dropped`; a binding race (a `PutSlackUser` conflict injected through a `faultStore`) -> `dropped`, not `duplicate`; U1 whose email changed to kim's (kim bound to U3) -> `dropped`; bindings unchanged.
    - `thread-reply`: agent message posted to Slack (map row inserted through `h.tx` for seq S at ts T1), alice's reply in thread T1 posted after a second mapped reply S2 -> bus `reply_to == S`, `slack_after == S2`.
    - `permalink-parent`: alice top-level text with a permalink to S2 (a reply in the thread of S) -> `reply_to == S` (rewritten), `slack_after == S2`; a permalink to another channel is ignored.
    - `aged-out-root-quoted`: thread reply to an own bot post with no map row -> top-level, content starts with `> ` and the root text.
    - `unauthorized-root-not-quoted` (Review Focus 5): gina (guest) posts a root (dropped); alice replies in its thread -> top-level, content is alice's text only.
    - `archived-bridge-ignored` and `removed-bridge-ignored` (Review Focus 1): after `ArchiveBridge` or `Remove`, an event for the channel -> `ignored`, no rows written.
    - `drop-is-final`: alice without an ACL entry posts (dropped, one log line); the admin grants her `write`; `CatchUp` re-reads that message -> `ignored`, no bus message, no second log line; her next message -> `sent`.
    - `rate-limited-retried`: `SendMessagesPerSecond: 1`; two alice messages in the same instant -> the second is `retry`, then `sent` after the backoff (fake clock advanced), each once.
  - `TestSlackBridgeCatchUp` (bus suite): `fake.DropNext(3)`, alice posts three messages (one a reply to an old mapped root) -> no bus message; `CatchUp` -> three messages, in ts order; a second `CatchUp` -> none (`Calls("History")` grew, bus unchanged); the cursor equals the newest ts read; an own bot post with `MetadataSeq` S9 and no map row (crash repair) gets its map row and the `slack-bridge` acked mark reaches S9; an unauthorized message in the window is dropped once, recorded, and ignored by later catch-ups; a bridge with 250 roots active this week calls `Replies` 200 times in one pass.
- [ ] **Step 2: Run to verify failure** — `go test -count=1 -p 1 -run 'TestSlackBridgeInbound|TestSlackBridgeCatchUp' ./internal/conformance/bus/` -> FAIL to compile.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `make test && make test-conformance && bash scripts/m7-exit-checks.sh --deps-only` -> PASS.
- [ ] **Step 5: Commit** `feat(slackbridge): Slack to bus with trust checks, bindings, flat threads, permalinks and catch-up (spec 5.3, 6.3, Decisions 4, 11, 18)`.

---

### Task 14: Bus to Slack, the lifecycle and the runner (one replica, lock takeover)

**Files:**
- Create: `internal/slackbridge/outbound.go`, `internal/slackbridge/lifecycle.go`, `internal/slackbridge/runner.go`, `internal/conformance/bus/slackbridge_runner_test.go`
- Modify: `internal/server/cmd/serve.go` (the runner goroutine beside maintenance, released at shutdown)

**Interfaces:**
- Consumes: Tasks 11 (`FormatPost`), 12 (`ArchiveBridge`), 13 (`HandleEvent`, `CatchUp`); `core.Notifier()`; `Store.TryLock`/`Unlock`.
- Produces:
  - `func (s *Service) Drain(ctx context.Context) error`: for each `live` bridge, the `slack-bridge` subscription's acked mark, then `MessagesAfter(channel, acked, no filter, 100)` in seq order: a message whose sender is `slack-bridge`, a memory-kind message, or one with a map row (sent by the bridge on a human's behalf, Decision 2) only advances the mark; otherwise `FormatPost` with `thread_ts` = the root ts of the parent's map row, or, for a parent without one, top-level with the parent's content quoted when `tx.Message` still finds it; then `Post`; then one `Store.Tx`: `BridgeMessageBySeq` first, `PutBridgeMessage{seq, ts, root}` only when it is ErrNotFound (an overlapping runner already wrote it otherwise; never swallow a failed insert, because a failed statement ends the transaction on Postgres, `store.Store.Tx`), then `AdvanceAckedMark`. Posts on one Slack channel are at least `PostInterval` apart; `*RateLimitedError` waits `RetryAfter` and retries the same message; `ErrChannelGone` marks the bridge `archived` (archived by hand) and stops it; any other error stops that bridge until the next drain without advancing.
  - `func (s *Service) Lifecycle(ctx context.Context) error` (spec 6.5, 6.1b): for each `live` bridge: channel gone -> `ArchiveBridge(ReasonChannelGone)` then `DeleteBridge`; scope `done` and `now >= DoneAt + ArchiveAfterDone` -> `ArchiveBridge(ReasonDone)`; scope `done` and not yet checked since the last `EventConnected` -> membership check (the check runs again after every reconnect, because Socket Mode does not replay a missed `member_left_channel`); scope idle (`LastActivity < now - ScopeIdleHours`) -> `ArchiveBridge(ReasonIdle)`. Membership check: `Members` minus `self.UserID` minus users whose `UserInfo` says `IsBot` or `Deleted`; none left -> `ArchiveBridge(ReasonNoHumans)`. An `EventMemberLeft` on a bridge whose scope is done runs the check at once.
  - `func (s *Service) Run(ctx context.Context) error`: every `LockRenew`, `TryLock(ctx, "slack-bridge", s.core.Owner, now, now + 3*LockRenew)` (the Core's random `Owner`); on gaining it: start `RunSocket` (events go to one worker goroutine that also runs `CatchUp` on each `EventConnected`, so Slack-to-bus work is serial), a drain loop (woken by notifier hints for bridged channel ids and every 5 s) and a lifecycle loop (`LifecycleInterval`); on losing it or a `TryLock` error: stop all three before the next renew; on return: `Unlock`. `Serve` runs it in a goroutine under the same wait group as maintenance and releases the lock with `releaseTimeout` at shutdown.

State (Definition of Done D): a second runner over the same store never gets the lock while the first renews it, so at most one Socket Mode connection exists; a runner killed without `Unlock` loses the SQLite lease after `3*LockRenew` or its Postgres session at once, and the taker connects, catches up (`EventConnected`) and drains from the acked mark, posting each unacked message once; a post that succeeded before a crash and before its map row is repaired by catch-up through the message metadata (when Slack keeps it) before the drain runs; an overlap window during takeover is bounded by the lease, and the `BridgeMessageBySeq` check keeps the second runner from double counting. Archive rules run twice harmlessly (`ArchiveBridge` is idempotent).

- [ ] **Step 1: Write the failing tests** (bus suite, both engines; fake clock; `PostInterval: time.Millisecond`, `LockRenew: 50 * time.Millisecond`)
  - `TestSlackBridgeOutbound` subtests: `agent-message-posted` (`job1-coder` sends subject "Plan", tags `[build]`, content `**done**` -> one post, `Username == "coder"`, `IconEmoji == IconEmoji("job1-coder")`, section starts `*Plan*`, context `` `build` ``, `ThreadTS == ""`, `Seq` set; map row written; acked mark advanced); `reply-threaded` (a reply to a mapped message posts with the root's ts); `unmapped-parent-quoted` (a reply to a message sent before the bridge posts top-level with the quote); `human-origin-not-echoed` (a message the bridge sent for alice is never posted; mark advanced); `notices-not-echoed` (the `slack-bridge` notice is not posted); `truncated` (content over `MaxMessageChars` ends with the note); `rate-limited-drain` (Review Focus 4: `RateLimit("Post", 3, 10ms)` over five queued messages -> five posts in seq order, each once, mark at the last seq); `archived-by-hand` (`ArchiveByHand` then a send -> bridge `archived`, no retry loop).
  - `TestSlackBridgeRunner` subtests: `one-holder` (two `Service`s over sibling harnesses, both `Run` -> `fake.MaxConcurrentSockets() == 1` over 1 s); `takeover-catches-up` (cancel the holder without `Unlock`; three bus messages and two Slack messages arrive meanwhile with events dropped; the second runner takes the lock, posts the three once and sends the two once); `disconnect-catches-up` (`DropNext(1)` + `Disconnect` -> the dropped message is sent once after the reconnect); `repeat-and-reorder` (`RepeatNext`, `ReorderNext(3)` -> each sent once).
  - `TestSlackBridgeLifecycle` subtests: `done-after-hours` (done `with_bridge`, clock + 168 h -> one final line, archived; maintenance then retires the scope); `done-no-humans` (alice leaves -> `member_left_channel` -> archived before the 168 h); `done-never-joined` (alice invited but `Leave`s before done; done -> the first lifecycle pass archives); `missed-member-left` (done; `DropNext(1)` swallows alice's `member_left_channel`; nothing happens until `Disconnect`; after the reconnect the next lifecycle pass archives); `idle` (clock + 336 h with no traffic -> archived); `channel-gone` (`Reset(data)` removes the channel -> Slack archived, bridge row deleted); `archive-twice` (lifecycle and maintenance both archive -> one `Archive` call that changed state, no error).
- [ ] **Step 2: Run to verify failure** — `go test -count=1 -p 1 -run 'TestSlackBridgeOutbound|TestSlackBridgeRunner|TestSlackBridgeLifecycle' ./internal/conformance/bus/` -> FAIL to compile.
- [ ] **Step 3: Implement**; wire `Run` in `Serve`.
- [ ] **Step 4: Run** `make test && make test-conformance && go test -race -count=1 ./internal/slackbridge/ ./internal/slackapi/...` -> PASS; `go test -count=1 -p 1 -v -run '^TestReplicas' ./internal/conformance/bus/ > /tmp/replicas.log; bash scripts/m7-exit-checks.sh /tmp/replicas.log` -> counts unchanged, `deps_ok=1`.
- [ ] **Step 5: Commit** `feat(slackbridge): bus to Slack, archive rules and the single runner with takeover (spec 5.2, 5.3, 6.2, 6.5)`.

---

### Task 15: Agent-facing text, ADR 0009, CLAUDE.md and the done gate

**Files:**
- Create: `docs/adr/0009-scopes-and-slack-bridge.md`
- Modify: `docs/adr/README.md` (index row), `internal/mcpserver/server.go` (`receive`, `send` and `history` descriptions), `internal/cli/skills/using-agentbus2/SKILL.md`, `CLAUDE.md` (schema version 6 line; `internal/core/scopes`, `internal/slackapi`, `internal/slackbridge` lines; the Slack import rule; Decision 20 note on the empty-channel rule), `docs/ops/upgrade.md` (schema 6 migration note and the `slack-bridge` name), any stale hit of `rg -n 'no subscriptions|subscription of any identity|reply_to' docs README.md CLAUDE.md internal/mcpserver internal/cli/skills`
- Test: `internal/mcpserver/server_test.go`, `internal/cli/skill_test.go`

- [ ] **Step 1: Write the failing tests**
  - `TestToolDescriptionsThreadReplies` (mcpserver): the `receive` description contains `reply_to`, `human`, `slack_after`, `history` and `thread`; the `send` description says `reply_to` must name a message in the same channel.
  - `TestSkillThreadReplies` (cli): the skill text contains `slack_after` and "same channel".
- [ ] **Step 2: Run to verify failure** — `go test -run TestToolDescriptionsThreadReplies ./internal/mcpserver/ && go test -run TestSkillThreadReplies ./internal/cli/` -> FAIL.
- [ ] **Step 3: Write the text.** MCP `receive` (appended): "A message whose sender_kind is human and that carries reply_to answers a thread: before acting, read the thread with history and thread = that reply_to (the root and its replies). Its metadata slack_after names the newest message the human had seen." `send`: "reply_to must name a message in the same channel." The skill gains a short "Replies from people" paragraph saying the same, with `history` `thread` (Task 3). ADR 0009: context (pai, Slack, scopes), "Decisions (Eric, 2026-10-23)" restating Decisions 1 to 21 of the spec, each labeled as Eric's; "Claude's calls (ratified by Eric, 2026-10-23)"; "Corrections to the spec": spec 7 assumed `history` had a thread filter; it had none, so Task 3 added `thread` to `history` (bus, wire, MCP, CLI), which is what the agent text names. Decision 21 (registration modes) is restated with them, labeled as decided after the plan review. "Plan choices" (slack-go v0.30.1; the cursor rule of Task 13 and final drops; per-user invites; only `invalid_auth` fails start; the Archiver port and archive calls from the maintenance replica; a scope with a live bridge and no Archiver is held, never orphaning its Slack channel; token 24 h and 1 use, re-issue revokes the earlier token; `reset-credential` clears the Slack binding; guests not invited; icon list; the `slack-bridge` cleanup in the migration; the metadata crash repair is best effort and its scope is unverified); consequences.
- [ ] **Step 4: Run** `make test && go test -count=1 -v ./internal/e2e/ > /tmp/e2e.log; bash scripts/m6-exit-checks.sh /tmp/e2e.log` -> PASS, `words_ok=1`, counts unchanged.
- [ ] **Step 5: Done gate**

Run: `make verify`
Expected: ends with `verify: OK` (build, build-all, vet, fmt-check, lint, tidy-check, generate-check, unit tests, both conformance suites on SQLite and Postgres through both transports, check-gates).

Then: `make check-gates-selftest` (the three new m7 probes fire, the existing blocks still fire) and `make docker-smoke`.
Expected: both exit 0.

- [ ] **Step 6: Commit** `docs: scopes and Slack bridge ADR, agent text and CLAUDE.md (issue #14)`; report the `make verify` output tail. CI (`ci.yml`, `windows.yml`) green on the pushed commit is the last condition and needs Eric's push.
