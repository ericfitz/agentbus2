# Postgres Budget Eviction and Capacity Notices Implementation Plan (issue #7; Eric's decisions of 2026-10-27)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The budget backstop runs on both engines against a measure that bloat cannot move: on Postgres a logical byte sum over every table eviction can delete from, on SQLite the used-page size as today; under pressure it deletes in Eric's four-step order (useless, expired, idle, ordinary) above fixed floors, bounded by a byte allowance per tick, and health, `admin usage`, `status`, the MCP health tool and the TUI report the disk size, the disk-to-measure ratio and a five-value pressure tier.

**Architecture:** Every table eviction deletes from, directly or by cascade, gains a generated `size_bytes` column (Postgres `STORED`, SQLite `VIRTUAL`); the sweep's Store queries return the bytes of what they select, cascaded rows included, so the core counts exactly what it deletes. `store.Usage` becomes `{Bytes, DiskBytes, Logical}`; Postgres `Bytes` is `sum(size_bytes)` over `sqlstore.MeasuredTables`. The backstop stays the "budget eviction" job in `internal/core/maintenance` (one code path; the only engine branch is `Usage.Logical`: re-measure after each chunk, or count the deleted bytes). The pressure tier is one pure function, `rules.StorageTier`, fed by the measure, the budget, the target and `Tx.EvictionLeft` (existence facts), so the job, the notice and the `Usage` operation agree on every replica without stored state. Task lists record reads in `channels.last_read_at`, written at most once an hour per list.

**Tech Stack:** Go, modernc SQLite 3.54.1 (`modernc.org/sqlite` v1.61.0; generated columns need 3.31), pgx Postgres 18.6 in CI (generated `STORED` columns need 12, `INCLUDE` indexes 11), one `sqlstore`, kin-openapi + oapi-codegen (`make generate`), testcontainers, Bubble Tea v2 (TUI), the MCP Go SDK.

**Spec:** `docs/superpowers/specs/2026-10-27-postgres-budget-design.md` (all 20 decisions are Eric's, section 5; sections 4.2 to 4.15 give the chosen options), read with `docs/adr/0004-store-contract-and-storage.md` (Milestone 7 choices 4 and 5 `:374-389`, Gate 0 item 2 `:405-410`), `docs/superpowers/specs/2026-10-21-gate-0-decisions.md:126-131` and `docs/superpowers/specs/2026-10-19-agentbus2-design.md:766-773`.

**Eric's decisions of 2026-10-27 this plan implements** (recorded by Task 6 in the ADR 0004 addendum): 17 logical enforcement, physical reported; 18 plain table ownership, no extension; 19 age first, size as backstop; 1 option B (oldest-first backstop after the age jobs, tiers by outcome); 2 and 3 Eric's order (1 orphaned `bridge_messages`; 2 tombstones past their floor and expired `enrollment_tokens`; 3 idle task channels and idle memories; 4 ordinary messages), never an active task channel; 4 floors 1 hour (messages) and 72 hours (tombstones) even under pressure; 5 A (one code path, measure per engine); 6 A (start above the budget, stop at the target, allowance `u0 - target`); 7 B (health and log at every tier, the notice only when a sweep ends over budget, two texts); 8 A (`postgres_budget_mib` beside `sqlite_budget_mib`); 9 A (2048, on by default, bounds 64 to 1048576); 10 A extended and 11 (on-demand `sum(size_bytes)` over every table eviction can delete from); 12 A (`disk_bytes`, `disk_ratio`, `tier`; `evictable` redefined); 13 A (no extension detail); 14 A (`Reclaim` stays a no-op on Postgres); 15 A (equal budgets on every replica, documented); 16 A (bloat asserted through the measures only); 20 (`idle_window_hours`, default 168; activity is a new message, a task change or a read; an idle memory is deleted outright).

## Settled open notes (agent recommendations; each has a findable marker so Eric can reverse it with one edit)

1. **Soft and hard tiers** (brief 4.2, agent-derived). Above the budget the tier is a fact of what is still eligible, not of the sweep's history: `soft` while step 1 or 2 has an eligible row, `hard` when only step 3 or 4 has one, `exhausted` when none has; `normal` at or below the target, `approaching` above the target and at or below the budget. Rationale: no stored tier state, so every replica, the standalone `RunJob("capacity notices")` and the `Usage` operation compute the same value. Marker: `// open note: postgres-budget 1` on `rules.StorageTier` (Task 2).
2. **`memory_expiry_hours` or `task_expiry_hours` of 0 does not exempt a type from step 3.** Rationale: 0 disables the age rule; the backstop is a separate policy whose own knob is `idle_window_hours`, and an exemption would let a full shared database stop every replica. Marker: `// open note: postgres-budget 2` in `idleTaskChannelChunk` (the one place an `if s.Cfg.TaskExpiryHours == 0 { return 0, 0, nil }` and its memory twin would go) and on `TestEvictionIgnoresZeroExpiry` (Task 6).
3. **Recording a task-list read.** `channels.last_read_at BIGINT NOT NULL DEFAULT 0`, set by `tasks.Service.Get` and `List` (the `task_get` and `task_list` operations) only when the snapshot's value is older than one hour (`readMarkStepMs`), in a short `Tx` after the read, conditional (`WHERE id = ? AND last_read_at < ?`), its failure logged and never returned. `receive`, `history` and `search` on a task list do not count (each delivered message is already a write that counts). `ChannelLastActivity`, which task expiry uses, does not read it. Rationale: at most one write per list per hour, no new table (which would itself need `size_bytes`), and the age job keeps its meaning. Marker: `// open note: postgres-budget 3` on `markRead` in `internal/core/tasks/read.go` (Task 4).

**Agent choices in this plan** (not Eric's; each is one constant or one line):
- Row overhead `store.RowOverheadBytes = 32`. Per-table formula: 32 plus the byte length of every `TEXT` and `BYTEA`/`BLOB` column, a `NULL` counting 0 (table below). Generated `tsvector` columns are not counted.
- `idle_window_hours` bounds 24 to 8760: with the one-hour read marker, a window under a day would let the marker's lag decide.
- Step 2 runs tombstones, then expired tokens; step 3 runs idle task channels, then idle memories (the brief's listing order). An idle memory is deleted with every revision row of its `memory_id` and its `memory_access` row.
- Notice texts (Task 6) name the engine's setting, the measure, the disk size and the ratio on both engines.
- `health` already reports `degraded` for any notice (`internal/core/health/health.go:104`), so the over-budget notice degrades health as well as the exhausted one; no code change, pinned by a test in Task 6.
- `evictable` is always `true` now (no 0 budget is allowed on either engine); the member stays, documented as "a budget is enforced".

## Global Constraints

- Done gate per task: `make verify` (needs Docker; Postgres from testcontainers unless `AGENTBUS2_TEST_POSTGRES_DSN` names a reachable server; a run that skips the Postgres suites is not a pass); show its output in the report. Every task also runs `make lint` and `make test`. Work is done only when `ci.yml` and `windows.yml` are green on the pushed commit, `release-check` included; a red `windows.yml` is fixed first. No push without Eric.
- Pre-release rule (Eric, 2026-10-25): no new migration; edit `internal/store/postgres/schema/0001_initial.sql` and `internal/store/sqlite/schema/0001_initial.sql` in place; schema version stays 1; no compatibility layer, shim, alias or deprecation path; local databases are recreated.
- Protocol stays `1.0.0` (Eric, 2026-10-27): `api/openapi.yaml` `info.version` and `internal/version.Protocol` do not change. `api/openapi.yaml` changes only in Task 5, followed by `make generate`; `internal/gen` and `internal/gen/spec` are never edited by hand; `make generate-check` is clean.
- Access: no new access rule. `TestAccessRuleSites` (`internal/core/access/sites_test.go`) must pass with no allowlist edit: the new SQL filters on `kind` (a Store fact), and core code never names `KindMemory`, `Admin`, `Creator`, `Owner` or `Scope` (step 3 finds memories with `c.kind <> 'tasks'` in SQL, as `ExpiredMemories` does). If a task needs an allowlist edit, stop and report it as a design change.
- Import rules: any task that adds an import runs `bash scripts/m4-exit-checks.sh --deps-only && bash scripts/m5-exit-checks.sh --deps-only && bash scripts/m6-exit-checks.sh --deps-only && bash scripts/m7-exit-checks.sh --deps-only && bash scripts/m8-exit-checks.sh --deps-only`. Expected new edges: `internal/core/channels` and `internal/core/maintenance` import `internal/core/rules` (allowed; `rules` imports only `internal/bus` of this module). `internal/store/...` never imports `internal/core/...`. `internal/tui` imports only `internal/bus` and `internal/version`.
- Pinned counts that stay: MCP tool count 25; `TestACLMatrix` 52 leaves per engine; `TestReplicasWake` 4/6 and `TestReplicasMaintenanceExclusive` 2 per engine (`scripts/m7-exit-checks.sh`); e2e 13 steps and 11 cells; `TestBothModes` 2. No exit-count script counts the maintenance tests (checked: `rg -n 'Maintenance|Budget' scripts/m3-exit-counts.sh scripts/m4-exit-checks.sh` finds nothing).
- A new gate rule gets a mutation in `scripts/check-gates-selftest.sh` (Task 7: blocks 30 and 31; numbers assigned across the 2026-10-27 plans in execution order: 30 and 31 postgres budget, 32 agent status, 33 MCP-only client).
- Agent-facing text: the capacity notice reaches agents through `status` and `receive`. It is outside the files `scripts/m6-exit-checks.sh` checks, so this plan applies the rule itself: no `dm/`, `-filter`, `@<`, `memory/`, `tasks/` or `general/` in it; the MCP output (`internal/mcpserver/server.go`) is checked by the script.
- Reject unknown values (Definition of Done B): an unknown `store.Engine` in `core.Budget` is `internal`; an unknown `tier` from the wire is `internal` in the remote client (pattern `TestHealthRejectsUnknownEnums`); each has a test.
- Docs move with the behavior, in the task that changes it; check every sentence against the committed code (Definition of Done E). American English.
- Definition of Done D for every stateful step: a second run at or below the budget deletes nothing; overlapping runs are serialized by the `maintenance` lock (a failover overlap is bounded by two allowances, floors hold); a killed sweep leaves committed chunks, each with its `evicted_before_seq`, and no tier or counter state to repair.
- Each task: failing tests first, exact `git add` list (never `-A`), one commit on `main` ending with:
  ```
  Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_011fnCFu154ZJG5c5ro7rrqs
  ```

## Measured tables and formulas (Task 1 transcribes these; `sqlstore.MeasuredTables` lists them in this order)

`B(x)` is `octet_length(x)` on Postgres and `length(CAST(x AS BLOB))` on SQLite; a nullable column is `coalesce(B(x), 0)`. Every formula is `32 + ...` (`store.RowOverheadBytes`).

| Table | Deleted by | Counted columns |
| --- | --- | --- |
| `channels` | step 3 (idle task list) | `name`, `kind`, `creator`, `scope` (nullable) |
| `channel_acl` | cascade from `channels` | `identity` |
| `subscriptions` | cascade from `channels` | `identity` |
| `session_cursors` | cascade from `channels` | `session_id` |
| `messages` | steps 2 to 4, cascade from `channels` | `sender`, `session_id`, `type`, `subject`, `content`, `metadata` (nullable), `sender_kind` |
| `message_tags` | cascade from `messages` | `tag` |
| `embeddings` | cascade from `messages` | `model`, `vector` |
| `embed_failures` | cascade from `messages` | `model`, `error` |
| `memory_access` | step 3 (idle memory) | none (the formula is the constant 32) |
| `bridge_messages` | step 1 | `slack_ts`, `root_ts` |
| `enrollment_tokens` | step 2 | `id`, `token_hash`, `identity` |

Postgres also gets `CREATE INDEX messages_size ON messages (seq) INCLUDE (size_bytes);` (Postgres 18 makes an unqualified generated column `VIRTUAL`, which cannot be indexed, so `STORED` is written out). The direct-delete set the guard test starts from is `messages`, `channels`, `bridge_messages`, `enrollment_tokens`, `memory_access`.

## Review Focus

1. A task list whose only activity in the last week is a `task_list` or `task_get` read is never deleted under pressure, at any tier; one with no read, message or tombstone for longer than `idle_window_hours` is (Task 4 `TestTaskReadMarksChannel`, Task 6 `TestEvictionKeepsActiveTaskChannel`).
2. A memory whose only recent activity is a read (`get_memory`, a search hit: `memory_access.accessed_at`) or a new revision is kept; a memory on a task list is never a step 3 memory (Task 1 `TestIdleMemories`, Task 6 `TestEvictionOrder`).
3. A task list with a live or creating bridge is never offered as idle, and a `bridge_messages` row whose `seq` still exists is never orphaned (Task 1 `TestIdleTaskChannels`, `TestOrphanedBridgeMessages`).
4. The bytes a sweep counts for a message include its tags, embedding and failure rows, and for a task list every row its delete cascades to, so on Postgres `Usage.Bytes` falls by exactly the counted sum (Task 1 `TestSweepBytesIncludeCascades`, Task 3 `TestUsageIgnoresBloat`).
5. Multibyte text is counted in bytes, not characters, identically on both engines (Task 1 `TestSizeBytesFormula`, `é` and `✓` in the content).

## Task dependencies and parallelism

| Task | Depends on | Wave |
| --- | --- | --- |
| 1 Schema `size_bytes` and `last_read_at`; sized and new eviction queries | none | 1 |
| 2 Settings, floors, `core.Budget`, `core.EvictionCutoffs`, `rules.StorageTier` | 1 | 2 |
| 3 `store.Usage{Bytes, DiskBytes, Logical}` per engine | 1 | 2 |
| 4 Task-list reads recorded | 1 | 2 |
| 5 The `Usage` operation, the wire and every client | 2, 3 | 3 |
| 6 The backstop sweep and notices on both engines; ADR addendum and docs | 1, 2, 3, 4 | 3 |
| 7 The bloat and runaway regression on Postgres; gate self-test blocks | 6 | 4 |

Wave 2 tasks touch disjoint files. In wave 3, Task 5 owns `internal/core/channels/usage.go`, `internal/bus/admin.go` and the usage and health tests; Task 6 owns `internal/core/maintenance` and `internal/conformance/bus/maintenance_test.go`; `docs/ops/deploy.md` is Task 6's.

---

### Task 1: Schema `size_bytes` and `last_read_at`; sized and new eviction queries

**Files:**
- Modify: `internal/store/postgres/schema/0001_initial.sql` and `internal/store/sqlite/schema/0001_initial.sql` (a `size_bytes` generated column on the eleven tables above; `channels.last_read_at BIGINT NOT NULL DEFAULT 0` (SQLite `INTEGER`); the Postgres `messages_size` index), `internal/store/messages.go:39-40,101-105` (`SeqChannel` gains `Bytes`; `ExpiredTombstones` returns `[]SeqChannel`), `internal/store/channels.go:16-23` (`Channel.LastReadAt`), new `internal/store/eviction.go` (the `Eviction` aggregate, embedded in `Tx` at `internal/store/store.go:49-65`), `internal/store/sqlstore/messages.go:404-429`, `internal/store/sqlstore/channels.go:32` (`channelCols` gains `last_read_at`) and its `channelRow`, new `internal/store/sqlstore/eviction.go` (`MeasuredTables`, `messageBytesExpr`, the methods), `internal/core/maintenance/jobs.go:143-160` (`tombstonePurge` takes the seqs from the refs; no behavior change)
- Test: new `internal/conformance/store/eviction_test.go`

**Interfaces:**
- Produces (`internal/store`):
  - `const RowOverheadBytes = 32`; `type SeqChannel struct{ Seq, ChannelID, Bytes int64 }` (Bytes: the row plus its `message_tags`, `embeddings` and `embed_failures` rows); `ExpiredTombstones(ctx, before int64, limit int) ([]SeqChannel, error)` (same rows and order as today); `OrdinaryMessagesBefore` fills `Bytes`.
  - `Channel.LastReadAt int64`; `Channels.TouchChannelRead(ctx, id, at int64) error` (`UPDATE channels SET last_read_at = ? WHERE id = ? AND last_read_at < ?`; no row changed is not an error).
  - `type BridgeMessageRef struct{ BridgeID, Seq, Bytes int64 }`, `type TokenRef struct{ ID string; Bytes int64 }`, `type SizedMemory struct{ MemoryID, Bytes int64 }`, `type EvictionCutoffs struct{ TombstoneBefore, TokenExpiredBefore, IdleBefore, MessageBefore int64 }`, `type EvictionLeft struct{ Useless, Expired, Idle, Ordinary bool }` with `func (l EvictionLeft) Early() bool` (`Useless || Expired`) and `Late() bool` (`Idle || Ordinary`).
  - `type Eviction interface`:
    - `OrphanedBridgeMessages(ctx, limit int) ([]BridgeMessageRef, error)`: rows with no `messages.seq`, by `bridge_id, seq`.
    - `DeleteBridgeMessages(ctx, refs []BridgeMessageRef) error`.
    - `ExpiredEnrollmentTokens(ctx, now int64, limit int) ([]TokenRef, error)`: `expires_at < now`, by `expires_at, id`.
    - `DeleteEnrollmentTokens(ctx, ids []string) error`.
    - `IdleTaskChannels(ctx, idleBefore int64, limit int) ([]Channel, error)`: `kind = 'tasks'`, no bridge with `state <> 'archived'`, and the greatest of `created_at`, `last_read_at`, its messages' `created_at` and `tombstone_at` below `idleBefore`; oldest activity first, then id.
    - `ChannelBytes(ctx, channelID int64) (int64, error)`: the channel row plus every `channel_acl`, `subscriptions`, `session_cursors` row and every message (with `messageBytesExpr`) of the channel; ErrNotFound for an unknown id.
    - `DeleteIdleTaskChannel(ctx, channelID, idleBefore int64) (bool, error)`: one conditional `DELETE` that re-checks the idle condition (the `DeleteChannelIfEmpty` pattern, `sqlstore/channels.go:221`).
    - `IdleMemories(ctx, idleBefore int64, limit int) ([]SizedMemory, error)`: live memories (`tombstone = FALSE`, `memory_id IS NOT NULL`) on channels of `kind <> 'tasks'` whose greatest of the live row's `created_at` and `memory_access.accessed_at` (0 when absent) is below `idleBefore`; Bytes sums every `messages` row of the `memory_id` (with cascades) plus its `memory_access` row; oldest activity first, then `memory_id`.
    - `PurgeMemory(ctx, memoryID int64) error`: deletes every `messages` row with that `memory_id` and its `memory_access` row (no foreign key joins them).
    - `EvictionLeft(ctx, c EvictionCutoffs) (EvictionLeft, error)`: each flag is its step's listing query with limit 1 (`Useless`: an orphan; `Expired`: `ExpiredTombstones(c.TombstoneBefore)` or `ExpiredEnrollmentTokens(c.TokenExpiredBefore)`; `Idle`: `IdleTaskChannels` or `IdleMemories` at `c.IdleBefore`; `Ordinary`: `OrdinaryMessagesBefore(c.MessageBefore)`).
  - `sqlstore.MeasuredTables []string` (the eleven names in the table's order); `messageBytesExpr` (unexported SQL fragment over alias `m`) used by every sized query.

- [ ] **Step 1: Write the failing Store-suite tests** (`internal/conformance/store/eviction_test.go`, every test under `dbtest.ForEachEngine`):
  - `TestSizeBytesFormula`: append a message with `Sender "pat"`, `SessionID "s1"`, `Type "t"`, `Subject "sub"`, `Content "héllo ✓"`, `Metadata {"k": "v"}`; through `rawDB` read `size_bytes, sender, session_id, type, subject, content, coalesce(metadata, ''), sender_kind` and assert `size_bytes == 32 + Σ len([]byte(col))` and `size_bytes != 32 + Σ utf8.RuneCountInString(col)`. Then one row per other measured table (a tag, an embedding of vector `[]byte{1,2,3}`, an embed failure, an access row, an ACL row, a subscription, a cursor, a bridge map row, a token) and the same assertion over that table's counted columns (a test-local map `countedColumns` from the table above).
  - `TestMeasuredTablesCoverCascades`: from the catalog (Postgres: `pg_constraint` with `contype = 'f' AND confdeltype = 'c'` in `current_schema()`; SQLite: `pragma_foreign_key_list` with `on_delete = 'CASCADE'` over every table of `sqlite_master`), the cascade closure of `{messages, channels, bridge_messages, enrollment_tokens, memory_access}` equals `set(sqlstore.MeasuredTables)`, and each has a `size_bytes` column (Postgres `information_schema.columns`; SQLite `pragma_table_xinfo`). The failure names the table.
  - `TestSweepBytesIncludeCascades`: a message with two tags; `OrdinaryMessagesBefore(1<<62, 10)` returns it with `Bytes` equal to the raw `size_bytes` of the row plus both tag rows; a tombstoned memory revision with an embed failure: `ExpiredTombstones` likewise.
  - `TestOrphanedBridgeMessages`: a bridge (`CreateBridge`), two map rows for appended seqs, delete one message: exactly the other row's seq is absent and the deleted seq's row is listed with its `Bytes`; `DeleteBridgeMessages` removes it; the mapped row stays.
  - `TestExpiredEnrollmentTokens`: tokens expiring at `now-1` and `now+1`: only the first is listed (with `Bytes`), and `DeleteEnrollmentTokens` removes it.
  - `TestIdleTaskChannels` (cutoff `T`): a task list created before `T` with nothing else is listed; one with a message at `T` is not; one with a `tombstone_at` at `T` is not; one with `last_read_at = T` is not; one with a `live` bridge row is not, with an `archived` one is; an ordinary channel never is. `ChannelBytes` equals the raw sum over the eight tables for that channel. `DeleteIdleTaskChannel` returns false after a message at `T` lands, true otherwise, and an unknown id is false with no error.
  - `TestIdleMemories` (cutoff `T`): a memory created before `T` with `accessed_at < T` is listed with `Bytes` = all its revision rows plus the access row; one with `accessed_at = T` is not; a memory on a task list (a task row) never is; `PurgeMemory` leaves no row with that `memory_id` and no access row.
  - `TestEvictionLeft`: on an empty bus every flag is false; adding one fact per step sets exactly that flag.
  - `TestTouchChannelRead`: `TouchChannelRead(id, 100)` sets 100; `(id, 50)` leaves 100; `ChannelByName` returns `LastReadAt` 100.
- [ ] **Step 2: Run them to verify they fail**
  Run: `go test -count=1 -p 1 -run 'TestSizeBytes|TestMeasuredTables|TestSweepBytes|TestOrphaned|TestExpiredEnrollment|TestIdle|TestEvictionLeft|TestTouchChannelRead' ./internal/conformance/store/`
  Expected: compile failure (undefined `sqlstore.MeasuredTables`, `Eviction` methods).
- [ ] **Step 3: Implement** the schema edits (the formulas above, written once per engine; SQLite `INTEGER GENERATED ALWAYS AS (...) VIRTUAL`, Postgres `BIGINT GENERATED ALWAYS AS (...) STORED`), the Store types and methods. Every `INSERT` in `sqlstore` already names its columns (checked), so the generated columns need no insert change. Each delete method checks `t.writable()` first. `ExpiredTombstones`' doc gains "Bytes as OrdinaryMessagesBefore".
- [ ] **Step 4: Run** `make lint && make test && make test-conformance && make verify`. Expected: PASS on both engines and both transports.
- [ ] **Step 5: Commit**
  ```bash
  git add internal/store/postgres/schema/0001_initial.sql internal/store/sqlite/schema/0001_initial.sql internal/store/messages.go internal/store/channels.go internal/store/eviction.go internal/store/store.go internal/store/sqlstore/messages.go internal/store/sqlstore/channels.go internal/store/sqlstore/eviction.go internal/core/maintenance/jobs.go internal/conformance/store/eviction_test.go
  git commit -m "feat(store): size_bytes on every table eviction deletes from; sized and new eviction queries (#7)"
  ```

---

### Task 2: Settings, floors, `core.Budget`, `core.EvictionCutoffs` and `rules.StorageTier`

**Files:**
- Modify: `internal/core/config.go:63-68,99-100,119-120,128` (fields `PostgresBudgetMiB`, `IdleWindowHours`; defaults 2048 and 168; bounds `{"postgres_budget_mib", 64, 1048576}`, `{"idle_window_hours", 24, 8760}`; constants `MessageFloorHours = 1`, `TombstoneFloorHours = 72` used as the lower bounds of `message_retention_hours` and `tombstone_min_hours`; the `SQLiteBudgetMiB` comment), `internal/server/config/config.go:72-73,122-123,173-174` (`PostgresBudgetMiB int json:"postgres_budget_mib"`, `IdleWindowHours int json:"idle_window_hours"`, `Default`, `Core()`), `internal/bus/admin.go` (the `PressureTier` type), `docs/ops/configuration.md` (two new rows after `:86`)
- Create: `internal/core/budget.go`, `internal/core/rules/storage.go`
- Test: `internal/core/config_test.go` (`TestConfigBounds` `:24`), `internal/server/config/config_test.go` (`TestValidateRejects` `:67`), new `internal/core/budget_test.go`, new `internal/core/rules/storage_test.go`; unchanged and must pass: `TestDefaultMatchesCore`, `TestSettingsListsEveryJSONName`, `TestConfigurationReferenceCoversEverySetting`

**Interfaces:**
- Consumes: `store.Engine`, `store.EvictionCutoffs` (Task 1).
- Produces:
  - `bus.PressureTier string` with `PressureNormal = "normal"`, `PressureApproaching = "approaching"`, `PressureSoft = "soft"`, `PressureHard = "hard"`, `PressureExhausted = "exhausted"`, `func ValidPressureTier(t PressureTier) bool`.
  - `func core.Budget(c Config, e store.Engine) (bytes int64, setting string, err error)`: SQLite `SQLiteBudgetMiB << 20`, `"sqlite_budget_mib"`; Postgres `PostgresBudgetMiB << 20`, `"postgres_budget_mib"`; any other engine `E(bus.CodeInternal, "unknown store engine %q", e)`.
  - `func core.TargetBytes(budget int64, cleanupFreePercent int) int64` = `budget - budget*int64(cleanupFreePercent)/100`.
  - `func core.EvictionCutoffs(c Config, now int64) store.EvictionCutoffs`: `TombstoneBefore = now - TombstoneFloorHours h`, `TokenExpiredBefore = now`, `IdleBefore = now - IdleWindowHours h`, `MessageBefore = now - MessageFloorHours h`.
  - `func rules.StorageTier(bytes, budget, target int64, early, late bool) bus.PressureTier` (settled open note 1; marker `// open note: postgres-budget 1`).

- [ ] **Step 1: Write the failing tests.** `TestConfigBounds` gains `{"postgres_budget_mib", 64, 1048576, ...}` and `{"idle_window_hours", 24, 8760, ...}`. `TestValidateRejects` gains `{"postgres budget bound", func(c *Config) { c.PostgresBudgetMiB = 1 }, "postgres_budget_mib"}` and `{"idle window bound", func(c *Config) { c.IdleWindowHours = 23 }, "idle_window_hours"}`. `TestFloorsAreTheLowerBounds`: `MessageRetentionHours = MessageFloorHours - 1` and `TombstoneMinHours = TombstoneFloorHours - 1` each fail `Validate`, and `Defaults()` at the floors passes. `TestBudgetPicksEngineSetting`: `Budget(Defaults() with SQLiteBudgetMiB 100, PostgresBudgetMiB 200, sqlite)` is `100<<20, "sqlite_budget_mib"`, postgres `200<<20, "postgres_budget_mib"`, `store.Engine("mysql")` is `internal`. `TestEvictionCutoffs` pins the four values at `now = 1_000 * 3_600_000`. `TestStorageTier` (table, budget 1000, target 750): `(750, …)` normal; `(751, …)` approaching; `(1000, …)` approaching; `(1001, true, true)` soft; `(1001, true, false)` soft; `(1001, false, true)` hard; `(1001, false, false)` exhausted; `(0, false, false)` normal.
- [ ] **Step 2: Run** `go test -count=1 ./internal/core/ ./internal/core/rules/ ./internal/server/config/`. Expected: FAIL (undefined names), then `TestConfigurationReferenceCoversEverySetting` fails until the doc rows exist.
- [ ] **Step 3: Implement.** Doc rows: `postgres_budget_mib` | `AGENTBUS2_SERVER_POSTGRES_BUDGET_MIB` | int | `2048` | "64 to 1048576. Logical size (the bytes of every row eviction can delete, with a fixed per-row overhead) above which the budget backstop deletes, on Postgres. Disk use is a multiple of it; `GET /v1/health` reports the ratio." and `idle_window_hours` | `AGENTBUS2_SERVER_IDLE_WINDOW_HOURS` | int | `168` | "24 to 8760. Under space pressure, a task list or memory with no new message, task change or read for this long may be deleted." The `SQLiteBudgetMiB` comment: "the budget of each engine's enforced measure (core.Budget): used pages on SQLite, the logical sum on Postgres (ADR 0004 Postgres budget addendum)".
- [ ] **Step 4: Run** `make lint && make test && make verify`. Expected: PASS.
- [ ] **Step 5: Commit** `git add internal/core/config.go internal/core/config_test.go internal/core/budget.go internal/core/budget_test.go internal/core/rules/storage.go internal/core/rules/storage_test.go internal/bus/admin.go internal/server/config/config.go internal/server/config/config_test.go docs/ops/configuration.md` with message `feat(core): postgres_budget_mib, idle_window_hours, the pressure floors and the storage tier rule (#7, Eric 2026-10-27)`.

---

### Task 3: `store.Usage{Bytes, DiskBytes, Logical}` per engine

**Files:**
- Modify: `internal/store/space.go:5-12` (`Usage{Bytes, DiskBytes int64; Logical bool}`, no `Evictable`; the doc), `internal/store/postgres/space.go:11-21` (one query: the logical sum over `sqlstore.MeasuredTables` and today's relation sum; the return line is exactly `return store.Usage{Bytes: logical, DiskBytes: disk, Logical: true}, nil`, the anchor of Task 7's block 30), `internal/store/sqlite/space.go:19-37` (`Bytes` as today, `DiskBytes = page_count * page_size`, `Logical: false`), interim adapters that keep behavior: `internal/core/maintenance/budget.go:39,75` (`if u.Logical { return 0, nil }`), `internal/core/channels/usage.go:37` (`out.Evictable = !u.Logical`)
- Test: `internal/conformance/store/space_test.go` (`TestUsageGrowsWithData` `:34`; new `TestUsageIgnoresBloat`; new `BenchmarkUsageLogical`)

**Interfaces:**
- Consumes: `sqlstore.MeasuredTables`, the `size_bytes` columns (Task 1).
- Produces: `store.Usage{Bytes, DiskBytes int64; Logical bool}`. Bytes is the enforced measure; DiskBytes the physical size; Logical true when deleting a row lowers Bytes by exactly its `size_bytes` (Postgres), false when Bytes must be re-measured (SQLite).

- [ ] **Step 1: Write the failing tests.**
  - `TestUsageGrowsWithData`: `:43` becomes `after.Logical == (e.Name == "postgres")`, `after.DiskBytes >= after.Bytes` on SQLite, `after.DiskBytes > 0` on both, and on Postgres `after.Bytes - before.Bytes` equals the raw `sum(size_bytes)` of the 200 appended rows.
  - `TestUsageIgnoresBloat` (both engines): on Postgres, through `rawDB`, `ALTER TABLE messages SET (autovacuum_enabled = false, toast.autovacuum_enabled = false)`; `appendKiB(t, s, 2000)`; `u0`; delete the 1000 oldest through `OrdinaryMessagesBefore` and `DeleteMessages` in one `Tx`, summing their `Bytes` as `d`; `u1`. Postgres: `u1.Bytes == u0.Bytes - d` and `u1.DiskBytes >= u0.DiskBytes`; then `VACUUM messages` outside a transaction, `u2.Bytes == u1.Bytes` and `u2.DiskBytes >= u1.DiskBytes` (the oldest rows go first, so no trailing page can be truncated). SQLite: `u1.Bytes < u0.Bytes` and `u1.DiskBytes == u0.DiskBytes` (pages go to the freelist; the file shrinks only on `Reclaim`). No dead-tuple statistic is asserted (decision 16).
  - `BenchmarkUsageLogical` (Postgres only, runs only under `-bench`): fills 2,000,000 rows of 1 KiB in batches, then times `Usage` and logs `EXPLAIN SELECT coalesce(sum(size_bytes), 0) FROM messages`; it asserts nothing about the plan (the planner's choice depends on the visibility map) and is not part of `verify`.
- [ ] **Step 2: Run** `go test -count=1 -p 1 -run 'TestUsage' ./internal/conformance/store/`. Expected: compile failure on `Logical`/`DiskBytes`.
- [ ] **Step 3: Implement** the three files and the two interim adapters (their behavior is today's; Tasks 5 and 6 replace them). The Postgres query is built once at package init from `sqlstore.MeasuredTables` (`(SELECT coalesce(sum(size_bytes), 0) FROM <t>) + ...`), cast to `bigint`, and runs outside any transaction as today.
- [ ] **Step 4: Run** `make lint && make test && make test-conformance && make verify`; once, `go test -run '^$' -bench BenchmarkUsageLogical -benchtime 3x ./internal/conformance/store/` and paste the timing and plan into the report (not a gate; if it is too slow at the default budget, the fallback is brief 4.10 option B, trigger counters with the append lock, which needs Eric).
- [ ] **Step 5: Commit** `git add internal/store/space.go internal/store/postgres/space.go internal/store/sqlite/space.go internal/core/maintenance/budget.go internal/core/channels/usage.go internal/conformance/store/space_test.go` with message `feat(store): Usage reports the enforced measure and the disk size; Postgres measures logical bytes (#7)`.

---

### Task 4: Task-list reads are recorded

**Files:**
- Modify: `internal/core/tasks/read.go:19-63` (`Get`), `:65-108` (`List`), new helper `markRead` in the same file
- Test: new `internal/conformance/bus/taskread_test.go`

**Interfaces:**
- Consumes: `Tx.TouchChannelRead`, `store.Channel.LastReadAt` (Task 1).
- Produces: `const readMarkStepMs = 3_600_000`; `func (s Service) markRead(ctx context.Context, ch store.Channel, now int64)`: when `ch.LastReadAt < now - readMarkStepMs`, runs `s.Store.Tx(... tx.TouchChannelRead(ctx, ch.ID, now))`; an error is logged (`"task list read mark failed"`, channel name, err) and dropped. Marker `// open note: postgres-budget 3` on it. `Get` and `List` call it once after a successful read, before returning (the reclaim pass included), never on an error path.

- [ ] **Step 1: Write the failing test** `TestTaskReadMarksChannel` (`dbtest.ForEachEngine`): a task list `todo` with one task created by `pat` at `fixedNowMs`; `h.clock.Advance(2 * time.Hour)`; `TaskList` by `pat` → `h.channelRow(todo).LastReadAt == h.core.NowMs()`; advance 10 minutes, `TaskList` again → unchanged; advance 61 minutes, `TaskGet` → equals the new now; an identity with no read on `todo` gets `forbidden` from `TaskList` and the mark does not move; `Receive` and `History` on `todo` do not move it.
- [ ] **Step 2: Run** `go test -count=1 -p 1 -run TestTaskReadMarksChannel ./internal/conformance/bus/`. Expected: FAIL (`LastReadAt` stays 0).
- [ ] **Step 3: Implement** `markRead` and the two calls.
- [ ] **Step 4: Run** `make lint && make test && make test-conformance && make verify`. Expected: PASS.
- [ ] **Step 5: Commit** `git add internal/core/tasks/read.go internal/conformance/bus/taskread_test.go` with message `feat(tasks): task_get and task_list record a read at most once an hour (#7, open note postgres-budget 3)`.

---

### Task 5: The `Usage` operation, the wire and every client

**Files:**
- Modify: `internal/bus/admin.go:18-25` (`Usage` gains `DiskBytes int64`, `DiskRatio float64`, `Tier PressureTier`; doc: "Evictable: a budget is enforced, always true in this release"), `internal/core/channels/usage.go` (whole function), `api/openapi.yaml:1845-1859` (`Usage.required: [bytes, budget_bytes, evictable, disk_bytes, disk_ratio, tier]`; `disk_bytes: {type: integer, format: int64}`, `disk_ratio: {type: number, format: double, description: "disk_bytes / bytes; 0 when bytes is 0"}`, `tier: {type: string, enum: [normal, approaching, soft, hard, exhausted]}`; `bytes` and `evictable` get descriptions: "the enforced measure: used file bytes on SQLite, the logical sum on Postgres" and "a budget is enforced"), `internal/gen/*` and `internal/gen/spec/*` (by `make generate`), `internal/server/admin_usage.go:14` and `internal/server/convert.go:79` (one `usageToGen(u bus.Usage) gen.Usage` used by both), `internal/bus/remote/usage.go:22` and `internal/bus/remote/convert.go:184` (one `fromGenUsage(g gen.Usage) (ab2bus.Usage, error)` used by both; an unknown tier is `unknownEnum("storage.tier", ...)` style `internal`), `internal/mcpserver/server.go:270-276,321` (`usageOut` gains `disk_bytes`, `disk_ratio`, `tier`), `internal/cli/status.go:17-19,109-117`, `internal/tui/health.go:303-311`, `docs/ops/health.md:55,101`, `docs/ops/admin-bootstrap.md:106` ("the database size, the budget of the store's engine, the disk size and ratio, the pressure tier and the capacity notice"), `docs/tui.md:139` (the storage and disk lines)
- Test: `internal/conformance/bus/usage_test.go:13-70`, `internal/conformance/bus/health_test.go:48`, `internal/core/health/health_test.go:134`, `internal/bus/remote/health_test.go:44-56` and new `internal/bus/remote/usage_test.go`, `internal/cli/status_test.go:126-138`, `internal/tui/health_test.go:129-136`, `internal/mcpserver/health_test.go`

**Interfaces:**
- Consumes: `core.Budget`, `core.TargetBytes`, `core.EvictionCutoffs`, `rules.StorageTier`, `bus.PressureTier` (Task 2); `store.Usage{Bytes, DiskBytes, Logical}` (Task 3); `Tx.EvictionLeft` (Task 1).
- Produces: `channels.Service.Usage` in this order: (1) the `Read` transaction with `RequireServerAdmin` and the capacity notice, as today (the admin check stays before any measure, so a non-administrator never triggers a sum); (2) `Store.Usage`; (3) `core.Budget(s.Cfg, s.Store.Engine())`; (4) only when `u.Bytes > budget`, a second `Read` with `tx.EvictionLeft(ctx, core.EvictionCutoffs(s.Cfg, s.NowMs()))`; (5) `Tier = rules.StorageTier(u.Bytes, budget, core.TargetBytes(budget, s.Cfg.CleanupFreePercent), left.Early(), left.Late())`, `DiskRatio = float64(DiskBytes)/float64(Bytes)` or 0, `Evictable = true`. Health's admin tier gets the same document through this function (`internal/core/health/health.go:127`).

- [ ] **Step 1: Write the failing tests.**
  - `TestUsageAdminOnly`: the harness gets `cfg` with `SQLiteBudgetMiB 100`, `PostgresBudgetMiB 200`; `BudgetBytes` is `100<<20` on SQLite and `200<<20` on Postgres; `Evictable` true on both; `DiskBytes > 0`; `DiskRatio > 0` and equal to `float64(DiskBytes)/float64(Bytes)`; `Tier == PressureNormal`. It runs under `local` and `inproc`, so it covers the wire.
  - `TestHealthTiers` `:48`: `a.Storage.Evictable` is true and `a.Storage.Tier == PressureNormal` on both engines. `internal/core/health/health_test.go:134`: `!h.Storage.Evictable` stays a failure; add `h.Storage.DiskBytes <= 0` as one.
  - Remote: `TestHealthConvertsEveryMember` input gains `"disk_bytes":30,"disk_ratio":3,"tier":"soft"` and the expected `Usage` gains `DiskBytes: 30, DiskRatio: 3, Tier: ab2bus.PressureSoft`; `TestHealthRejectsUnknownEnums` gains a `storage.tier` `"melting"` case; new `TestUsageRejectsUnknownTier` calls `fromGenUsage` with `Tier: "melting"` and expects `internal` naming `tier`, and with each of the five values expects no error.
  - `TestStatusUsageLine`: `{Bytes 3<<19, BudgetBytes 512<<20, Evictable, DiskBytes 3<<20, DiskRatio 2, Tier PressureNormal}` → `usage: 1.5 MiB of 512 MiB budget (disk 3.0 MiB, 2.00x, normal)`; with `Evictable` false → `usage: 1.5 MiB (disk 3.0 MiB, 2.00x, normal)`.
  - `TestHealthStorageLine`: `Usage{Bytes 3<<30, BudgetBytes 2<<30, DiskBytes 6<<30, DiskRatio 2, Tier PressureHard}` → the body contains `storage 3.0 GiB of 2.0 GiB`, `150%` and `disk 6.0 GiB, 2.00x, hard`.
  - `TestHealthTool` (mcpserver): the admin output's `storage` carries `disk_bytes`, `disk_ratio` and `tier`.
- [ ] **Step 2: Run** `go test -count=1 ./internal/cli/ ./internal/tui/ ./internal/bus/remote/ ./internal/mcpserver/ ./internal/core/health/ && go test -count=1 -p 1 -run 'TestUsage|TestHealth' ./internal/conformance/bus/`. Expected: FAIL.
- [ ] **Step 3: Implement** in this order: `bus.Usage`; `channels.Service.Usage`; `api/openapi.yaml` then `make generate`; the server and remote converters (one helper each, Definition of Done B); MCP, CLI (`usageLine`: the budget clause when `Evictable`, then always ` (disk %.1f MiB, %.2fx, %s)`; the doc at `status.go:17-19` drops "a Postgres size is not held to it"), TUI (a `disk` line after `storage`: `p("%s %s, %.2fx, %s\n", dim.Render("disk"), fmtBytes(u.DiskBytes), u.DiskRatio, u.Tier)`). Docs: `health.md:55` lists `bytes` (the enforced measure), `budget_bytes`, `evictable` (always true), `disk_bytes`, `disk_ratio`, `tier` (the five values and their conditions, open note 1) and `notice`; the example at `:101` gains the three members with values a real run prints (`make docker-smoke` or a local server; paste the body).
- [ ] **Step 4: Run** `make generate-check && make lint && make test && make test-conformance && bash scripts/m6-exit-checks.sh --deps-only && bash scripts/m8-exit-checks.sh --deps-only && make verify`. Expected: PASS; the MCP tool count stays 25.
- [ ] **Step 5: Commit** every file listed above (the `internal/gen` and `internal/gen/spec` files `make generate` changed, named one by one) with message `feat(usage): disk_bytes, disk_ratio and the pressure tier on the admin usage, health and every client; the budget per engine (#7, Eric 2026-10-27)`.

---

### Task 6: The backstop sweep and notices on both engines; ADR addendum and docs

**Files:**
- Modify: `internal/core/maintenance/budget.go` (whole file), `internal/core/maintenance/jobs.go:100-160` (`deleteOrdinaryChunk` and a new `expiredTombstoneChunk` return bytes; `messageRetention` and `tombstonePurge` keep their behavior), `internal/core/maintenance/service.go:26-38` (fields), `:133-136` (`Tick` adds the outcome value next to the deadline), `docs/adr/0004-store-contract-and-storage.md` (new section "Postgres budget addendum (Eric's decisions 2026-10-27, issue #7)" after `:473-495`; `:299`, `:374-389` and `:405-410` each gain "Superseded by the Postgres budget addendum"), `docs/superpowers/specs/2026-10-19-agentbus2-design.md:766-773`, `docs/superpowers/specs/2026-10-21-gate-0-decisions.md:128-129` (one line: "Superseded 2026-10-27 by ADR 0004's Postgres budget addendum (issue #7)"), `docs/ops/configuration.md:85-86` and a "Retention and the budget backstop" paragraph after the table, `docs/ops/deploy.md` (Postgres 12 or later; `postgres_budget_mib` and `idle_window_hours` equal on every replica, in the list at `:63`; sizing a disk from `storage.disk_ratio` with headroom, autovacuum keeps the disk at its high-water mark), `CLAUDE.md:53` (drop "(SQLite only)"; say the budget backstop runs on both engines)
- Test: new `internal/core/maintenance/budget_test.go`; `internal/conformance/bus/maintenance_test.go:498-611` (rewrite both tests; new `TestMaintenanceEvictionFloors`); unchanged and must pass: `TestTickRunsBudgetJobsInOrder`, `TestMaintenanceLockExclusiveAcrossCores`

**Interfaces:**
- Consumes: Task 1's queries and refs, `core.Budget`, `core.TargetBytes`, `core.EvictionCutoffs`, `rules.StorageTier` (Task 2), `store.Usage` (Task 3), `markRead` (Task 4, through `tasks.Service.List` in tests).
- Produces (all unexported in `maintenance`):
  - `Service.usage func(ctx context.Context) (store.Usage, error)` (nil: `s.Store.Usage`; tests only); `BudgetOverride` keeps its meaning ("bytes, tests only; the setting name stays the engine's").
  - `func (s Service) budget() (bytes int64, setting string, err error)`.
  - Chunk functions, each one transaction returning `(rows int, bytes int64, err error)`: `orphanChunk`, `expiredTombstoneChunk(ctx, before)`, `expiredTokenChunk(ctx, now)`, `idleTaskChannelChunk(ctx, idleBefore)` (one channel per transaction: `ChannelBytes`, then `DeleteIdleTaskChannel`; a false result counts nothing; `store.ErrNotFound`, a list deleted since the listing, counts nothing and the chunk goes on, the `taskExpiry` pattern at `jobs.go:214`), `idleMemoryChunk(ctx, idleBefore)` (`IdleMemories` then `PurgeMemory` each), `deleteOrdinaryChunk(ctx, createdBefore)`. The steps run in the order orphan, expired tombstone, expired token, idle task channel, idle memory, ordinary.
  - `type sweepOutcome struct{ final store.Usage; done bool }`, `type outcomeKey struct{}`.
  - `func overBudgetText(setting string, budget int64, u store.Usage) string` and `func exhaustedText(setting string, budget int64, u store.Usage) string`, exactly:
    - `fmt.Sprintf("agentbus2 storage is over budget: %s=%d MiB holds %d MiB (disk %d MiB, %.2fx); orphaned and expired rows, idle task lists and memories, and ordinary messages older than 1 hour are being deleted to make room", setting, budget>>20, u.Bytes>>20, u.DiskBytes>>20, ratio)`
    - `fmt.Sprintf("agentbus2 capacity exhausted: %s=%d MiB (disk %d MiB, %.2fx) is filled by task lists and memories active within idle_window_hours, ordinary messages younger than 1 hour and tombstones younger than 72 hours, which are never evicted; raise %s and restart every replica, lower idle_window_hours, or have an administrator reset the bus to discard all data", setting, budget>>20, u.DiskBytes>>20, ratio, setting)`
    - `ratio` is `DiskBytes/Bytes`, 0 when `Bytes` is 0.
  - The over-budget tiers of the `Usage` operation are not driven end to end (the smallest configurable budget is 64 MiB); they are covered by `TestStorageTier` (Task 2), `TestEvictionLeft` (Task 1) and the converter tests with `soft` (Task 5).

The sweep (`evictToBudget(ctx, now)`), which the tests determine except for this order:
```
u0 := usage(); budget, setting := budget(); target := core.TargetBytes(budget, Cfg.CleanupFreePercent)
if u0.Bytes > budget:
    allowance := u0.Bytes - target; deleted := 0
    for each step in order, while deleted < allowance and !pastDeadline:
        if !u0.Logical: Reclaim; u := usage(); if u.Bytes <= target: stop the sweep
        rows, bytes := step chunk; deleted += bytes; if rows == 0: next step
final := usage() when u0.Bytes > budget (the sweep started, even if it deleted nothing), else final := u0; store final in the outcome (when present)
if rows were deleted and final.Bytes >= u0.Bytes: Log.Warn("budget eviction did not lower usage", before, after, deleted)
Log.Info("budget eviction swept", engine, before, after, allowance, deleted_bytes, rows per step)   // only when it swept
return total rows, Reclaim()
```
`capacityNotice(ctx, now)`: the final `Usage` from the outcome when `done`, else `usage()`; at or below the budget it clears the notice; above it, in one `Tx`, `EvictionLeft(core.EvictionCutoffs(...))`, the tier, and `SetNotice` with `exhaustedText` when the tier is `exhausted`, else `overBudgetText`; it returns 1 when it set the notice. The `Logical` early return of Task 3 is gone from both jobs.

- [ ] **Step 1: Write the failing unit tests** (`internal/core/maintenance/budget_test.go`, over SQLite like `tick_test.go`, Docker-free so they run on Windows; the fake measure is `Service.usage` returning a fixed `store.Usage{Bytes: X, DiskBytes: 2 * X, Logical: true}`, so the allowance is `X - target` and only the counted bytes stop the sweep):
  - `TestEvictionOrder`: the sweep starts only above the budget, so the smallest allowance is `budget - target`; with `BudgetOverride = 4096` (target 3072) every run uses `X = 4097`, an allowance of 1025 bytes, and each seeded item is sized so the allowance ends inside a step. Seed one orphaned map row (about 70 bytes), one tombstone 73 hours old with 2 KiB of content, one expired token, one task list idle for 169 hours holding tasks of 2 KiB in all, one memory idle for 169 hours with 3 KiB of content, and 3 ordinary messages 2 hours old. Run 1: the orphan and the tombstone are gone; the token, the list, the memory and the messages remain. Run 2: the token and the list are gone; the memory and the messages remain. Run 3: the memory is gone; the messages remain. Run 4: the messages are gone (one chunk). Run 5 deletes 0 and `capacity notices` sets the `exhaustedText` notice. Assert presence and absence per run, and that each run's return value equals the rows it deleted.
  - `TestEvictionNeverFallingMeasure`: `X = 5 << 20`, `BudgetOverride = 4 << 20` (allowance 2 MiB), 3000 ordinary messages of 1 KiB 2 hours old; one run deletes at least `allowance` bytes and at most `allowance` plus one chunk's bytes (summed from `OrdinaryMessagesBefore`), and a captured `slog` handler (`core.Options.Logger`) holds `budget eviction did not lower usage`.
  - `TestEvictionFloors`: ordinary messages 59 minutes old and a tombstone 71 hours old under any allowance: nothing deleted; `capacity notices` sets the `exhaustedText` notice.
  - `TestEvictionKeepsActiveTaskChannel`: a task list created 200 hours ago whose task changed 100 hours ago, and one whose only activity is a `tasks.Service.List` read 100 hours ago (through Task 4's mark), survive an allowance larger than the bus; a third with no activity for 169 hours goes. Row counts of `identities`, `identity_keys`, `sessions`, `bridges`, `retired_identities`, `notices` other than `capacity`, unexpired `enrollment_tokens` and ordinary `channels` are unchanged.
  - `TestEvictionIgnoresZeroExpiry` (marker `// open note: postgres-budget 2`): with `TaskExpiryHours = 0` and `MemoryExpiryHours = 0`, an idle task list and an idle memory are still deleted under pressure.
  - `TestEvictionHysteresis`: `X` between target and budget, and `X` at the budget: 0 deleted, no usage read beyond the first.
  - `TestTickSweepFeedsNotice`: a counting `usage` with `Logical: true`: one `Tick` calls it exactly twice (start and final) when over budget, once when at or below it; `RunJob("capacity notices")` alone calls it once.
  - `TestCapacityTexts`: `overBudgetText("postgres_budget_mib", 2048<<20, Usage{Bytes: 3000<<20, DiskBytes: 6000<<20})` and `exhaustedText(...)` equal the exact strings above with `2.00x`; neither contains `memory/`, `tasks/`, `general/`, `dm/`.
- [ ] **Step 2: Rewrite the conformance tests** (`maintenance_test.go`, both engines and both transports, no engine branch except where named):
  - `TestMaintenanceBudgetEviction`: after the appends, `h.clock.Advance(2 * time.Hour)` (past the message floor, inside the idle window, so the memories are active); the hysteresis block as today; the sweep block as today (more than one chunk and not all; `usageBytes(h) <= target`, which on SQLite guards the overhead constant; the newest contiguous range survives; `EvictedBeforeSeq`; memories live; a second run deletes 0). On Postgres also: before the run, map every ordinary seq to its `Bytes` (`OrdinaryMessagesBefore(1<<62, 1<<30)`); after it, `usageBytes` equals the value before minus the sum of the `Bytes` of the seqs that are gone.
  - `TestMaintenanceCapacityNotice`: 5 memories and `BudgetOverride = 4 << 10` → eviction deletes 0, `capacity notices` returns 1 on both engines, the notice contains `capacity exhausted: ` and `<setting>=0 MiB` (`sqlite_budget_mib` or `postgres_budget_mib` by engine), `SetAt == now`; `Health` as `root` is `degraded`; `Send` still works; `BudgetOverride = 1 << 30` clears it. Then an ordinary message 2 hours old with `BudgetOverride = 4 << 10` and `RunJob("capacity notices")` alone (no sweep) → the notice starts `agentbus2 storage is over budget: `.
  - `TestMaintenanceEvictionFloors`: 50 ordinary messages appended, `BudgetOverride = 4 << 10`; `budget eviction` deletes 0 (all younger than 1 hour); `h.clock.Advance(61 * time.Minute)`; it deletes all 50. Tombstone floor (harness `cfg` with `TombstoneMinHours = 100`, so "tombstone purge" would not take it): edit a memory once at the start, which makes its first revision a tombstone at `t0`; at `t0 + 71h` a run leaves it; at `t0 + 72h + 1m` a run deletes it (the memory itself is active, its edit being younger than `idle_window_hours`).
- [ ] **Step 3: Run** `go test -count=1 ./internal/core/maintenance/ && go test -count=1 -p 1 -run 'TestMaintenance' ./internal/conformance/bus/`. Expected: FAIL.
- [ ] **Step 4: Implement** the jobs, then the docs. The ADR addendum lists Eric's decisions 1 to 20 (each labeled "Eric's decision 2026-10-27"), then "Agent choices" (the three settled open notes with their markers, the overhead constant, the idle bounds, the step sub-order, the texts, `evictable` always true), and states what it supersedes: Milestone 7 choice 4's Postgres half, choice 5's `Evictable` gate and Gate 0 item 2. Master spec `:766-773`: budget eviction and capacity notices run on both engines (ADR 0004 Postgres budget addendum). Configuration paragraph: age policies first; the backstop above the budget, down to the target, in the four steps; the floors; never deleted (active task lists, sessions, receipts, assertion rows, identities and keys, ACLs and subscriptions other than an idle list's, scopes, bridges, retired identities, notices); writes are never refused.
- [ ] **Step 5: Run** `make lint && make test && make test-conformance && go test -count=1 -run TestAccessRuleSites ./internal/core/access/ && bash scripts/m4-exit-checks.sh --deps-only && bash scripts/m7-exit-checks.sh --deps-only && make verify`. Expected: PASS with no allowlist edit.
- [ ] **Step 6: Commit** `git add internal/core/maintenance/budget.go internal/core/maintenance/jobs.go internal/core/maintenance/service.go internal/core/maintenance/budget_test.go internal/conformance/bus/maintenance_test.go docs/adr/0004-store-contract-and-storage.md docs/superpowers/specs/2026-10-19-agentbus2-design.md docs/superpowers/specs/2026-10-21-gate-0-decisions.md docs/ops/configuration.md docs/ops/deploy.md CLAUDE.md` with message `feat(maintenance): the budget backstop on both engines in Eric's order, with floors, an allowance and two notices (#7, Eric 2026-10-27)`.

---

### Task 7: The bloat and runaway regression on Postgres; gate self-test blocks

**Files:**
- Modify: `internal/conformance/bus/maintenance_test.go` (new `TestMaintenanceBudgetEvictionUnderBloat`), `scripts/check-gates-selftest.sh` (blocks 30 and 31 before the call list `:910`, two calls appended after `block_access_rule_sites_fires`)
- Test: the two above; `make check-gates-selftest`

**Interfaces:**
- Consumes: the `Usage` return line of `internal/store/postgres/space.go` (Task 3) and the `MessageBefore` line of `core.EvictionCutoffs` (Task 2), as mutation anchors; `mutate_block` (`scripts/check-gates-selftest.sh:217-241`).

- [ ] **Step 1: Write the test** `TestMaintenanceBudgetEvictionUnderBloat` (`dbtest.ForEachEngine`; the body runs for `postgres` only, so the leaf is `TestMaintenanceBudgetEvictionUnderBloat/postgres`), brief section 8 steps 1 to 8: through `sql.Open(h.e.Driver, h.dsn)`, `ALTER TABLE messages SET (autovacuum_enabled = false, toast.autovacuum_enabled = false)`; 2500 ordinary 1 KiB messages and 5 memories; advance 2 hours; `P0 = DiskBytes`, `L0 = Bytes`, `BudgetOverride = L0 / 2`; one run deletes more than 1000 and fewer than 2500, `Bytes <= target`, `DiskBytes >= P0`, the survivors are the newest contiguous range, `EvictedBeforeSeq` is right, the memories are live; three more runs each return 0 (the runaway regression); `VACUUM messages`, a run returns 0, `Bytes` unchanged, `DiskBytes` not lower; `Tick` across 5 intervals of the fake clock (`h.clock.Advance(time.Duration(h.cfg.MaintenanceIntervalSeconds) * time.Second)`) deletes nothing more; finally `BudgetOverride = Bytes + 1` with `DiskBytes` far above it: a run deletes 0, `capacity notices` sets nothing, and `Usage` as `root` reports `DiskRatio > 1` and `Tier` `normal` (the operation reads the configured 2048 MiB budget, not `BudgetOverride`).
- [ ] **Step 2: Run** `go test -count=1 -p 1 -v -run '^TestMaintenanceBudgetEvictionUnderBloat$' ./internal/conformance/bus/`. Expected: PASS (the behavior exists since Task 6). Then prove it bites: edit `internal/store/postgres/space.go` to `Bytes: disk` by hand, rerun, see `--- FAIL: TestMaintenanceBudgetEvictionUnderBloat/postgres`, revert.
- [ ] **Step 3: Add the self-test blocks** with `mutate_block`:
  - `# --- 30. postgres-budget-physical-measure` `block_postgres_budget_physical_measure`: file `internal/store/postgres/space.go`, from `return store.Usage{Bytes: logical, DiskBytes: disk, Logical: true}, nil`, to `return store.Usage{Bytes: disk, DiskBytes: disk, Logical: true}, nil`, test `TestMaintenanceBudgetEvictionUnderBloat`, want `--- FAIL: TestMaintenanceBudgetEvictionUnderBloat/postgres`.
  - `# --- 31. eviction-message-floor-removed` `block_eviction_message_floor_removed`: file `internal/core/budget.go`, from the committed `MessageBefore:` line of `EvictionCutoffs` (copy it byte for byte, gofmt alignment included), to the same field set to `now`, test `TestMaintenanceEvictionFloors`, want `--- FAIL: TestMaintenanceEvictionFloors/sqlite` and `--- FAIL: TestMaintenanceEvictionFloors/postgres`.
  - The header comments say what each mutation proves (a physical measure under bloat empties the bus; pressure without the floor deletes a burst younger than an hour).
- [ ] **Step 4: Commit** (the self-test tests `HEAD`, so this comes first) `git add internal/conformance/bus/maintenance_test.go scripts/check-gates-selftest.sh` with message `test(maintenance): the Postgres bloat and runaway regression, with gate self-test mutations for the measure and the floor (#7)`.
- [ ] **Step 5: Run** `make lint && make verify && make release-lint && make check-gates-selftest`. Expected: `selftest: ok postgres-budget-physical-measure`, `selftest: ok eviction-message-floor-removed`, `check-gates-selftest: OK`. If a block fails, fix it, amend this task's commit and rerun before reporting.
