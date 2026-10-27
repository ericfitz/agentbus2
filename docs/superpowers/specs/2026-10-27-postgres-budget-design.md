# Postgres budget eviction and capacity notices: design brief

## 1. Status

- Draft of 2026-10-27, written by an agent. Planning only: no code, no commit.
- Nothing in it is a decision until Eric marks it so. Section 5 lists every decision with its status.
- Issue: #7, "feat: budget eviction and capacity notices on Postgres".
- **Eric's decision 2026-10-27**: "Eric 2026-10-27: a design discussion on what Postgres exposes that can be used for budget-based eviction comes before any decision here." That discussion took place on 2026-10-27. Its outcome is the three decisions below.
- **Eric's decision 2026-10-27**: "Protocol version will be 1.0.0, end of discussion." Any addition in this brief ships in protocol 1.0.0, and `info.version` and `version.Protocol` stay `1.0.0` before the first release.
- **Eric's decision 2026-10-27 (what the budget protects): C, both.** Eviction is enforced on a logical measure (what the bus stores). Capacity notices and health report the physical size (`pg_total_relation_size` and related functions) and the physical-to-logical ratio, so an operator can translate a disk limit into a logical budget.
- **Eric's decision 2026-10-27 (privileges): plain table ownership.** Eviction must work with no extension and no extra privilege. Extensions such as `pgstattuple` and `pg_freespacemap` may only add detail to reports when present. Eviction never depends on them.
- **Eric reviewed this brief on 2026-10-27.** Of its 20 decisions, **20 are decided** and **0 are open**. He ratified decisions 1, 4, 5, 6, 7, 8, 9, 12, 13, 14, 15 and 16 as recommended (decision 4: the floors are 1 hour for messages and 72 hours for tombstones, even under pressure). He replaced decisions 2 and 3 with his own eviction order (section 4.3) and changed decisions 10 and 11 (the logical measure covers every table eviction can delete from). Decisions 17 to 19 were decided earlier the same day. Decision 20, new from this review, the definition of "idle" for task channels and memories, was decided later the same day: a configurable `idle_window_hours` setting, default 168 (7 days); activity means a new message, a task change or a read; an idle memory is deleted outright, not tombstoned. Open notes remain: whether `memory_expiry_hours` or `task_expiry_hours` of 0 exempts a type from step 3, how a read of a task channel is recorded, and that the soft and hard tier definitions of 4.2 are the brief author's derivation from Eric's order, not his words. The brief is rewritten so that sections 4.2 to 4.5, 4.7, 4.8, 4.10, 4.11, 7 and 8 follow Eric's order.
- **Eric's decision 2026-10-27 (retention model).** In Eric's words: "retention is supposed to be based on age (different policies based on object type) but size usage is there to backstop us - the idea is that we will keep the database healthy by deleting more aggressively under space pressure."
  - Age-based retention per object type is the primary policy.
  - The size budget is a backstop. Under space pressure the bus deletes more aggressively.

## 2. Problem and scope

Issue #7 asks for a safe budget eviction on Postgres. Today both budget jobs act only where `Store.Usage` reports `Evictable`, which is SQLite (Gate 0 item 2, Decision (Eric, 2026-10-21)). On Postgres, `pg_total_relation_size` counts dead tuples until vacuum. A sweep that re-measures after each chunk therefore never sees the size fall, and it would delete ordinary messages tick after tick until none are left.

Eric's three decisions of 2026-10-27 reframe the issue:

- The budget is a backstop behind the age policies, not a second retention policy.
- What the backstop enforces is a logical measure, so physical bloat can never drive deletion.
- The physical size is reported, never enforced.

Acceptance criteria (restated from the issue, as the decisions now read them):

1. A size measure that reflects live data. It is logical, and it needs no extension (Eric's decisions 2026-10-27).
2. Eviction bounded per tick, so that bloat can never empty the bus.
3. A Postgres budget setting (today there is only `sqlite_budget_mib`), and capacity notices on Postgres.
4. Conformance tests on Postgres that cover bloat (rows deleted but not yet vacuumed).

Out of scope:

- Refusing writes when over budget. v1's `checkCapacity` stays unported (Gate 0 item 3, Decision (Eric, 2026-10-21)).
- Shrinking the Postgres files on disk (`VACUUM FULL`, `pg_repack`). Both take an exclusive lock.
- New age policies for the object types that have none (section 3.1). Adding a policy to one is a separate issue. Under Eric's decision 2026-10-27, expired `enrollment_tokens` and orphaned `bridge_messages` are not a follow-up: they become steps of the eviction order (section 4.3).

In scope after Eric's decision 2026-10-27: the logical measure covers every table that eviction can delete from, not only `messages` (section 4.11). Orphaned `bridge_messages` rows (that table has no foreign key to `messages.seq`, `internal/store/postgres/schema/0001_initial.sql:220-229`, and nothing deletes its rows except the bridge cascade) are step 1 of the order.

## 3. What exists today

### 3.1 Age-based retention

Every age job below is engine-neutral. It runs the same `internal/store/sqlstore` SQL on SQLite and on Postgres, inside the same tick. Only the two budget jobs are gated on `Usage.Evictable`, which makes them SQLite only (section 3.3).

**Object types with an age policy.**

| Object type | Setting: default (bounds) | Job (`internal/core/maintenance/jobs.go`) | Store query | What it does |
|---|---|---|---|---|
| Ordinary messages (`memory_id IS NULL`), every channel kind | `message_retention_hours`: 168 (1 to 8760) | "message retention", `messageRetention` `:100-106`, through `deleteOrdinaryChunk` `:108-140` | `OrdinaryMessagesBefore` (`internal/store/sqlstore/messages.go:413-429`) | Deletes rows older than the age, oldest first. Raises each channel's `evicted_before_seq` in the same transaction. |
| Tombstones: deleted memories, superseded memory revisions (`internal/core/memories/memories.go:179`), deleted and superseded tasks (`internal/core/tasks/update.go:281`, `tasks/service.go:108`), expired memories | `tombstone_min_hours`: 72 (72 to 720) | "tombstone purge", `tombstonePurge` `:142-160` | `ExpiredTombstones` (`sqlstore/messages.go:404-411`) | Deletes tombstones whose `tombstone_at` is older than the age. Tombstoning keeps `content` (`sqlstore/messages.go:376-386`), so a purge frees the whole row. |
| Live memories, off task lists | `memory_expiry_hours`: 8760 (0 to 87600; 0 disables) | "memory expiry", `memoryExpiry` `:276-311` | `ExpiredMemories`, by `memory_access.accessed_at` (`sqlstore/memories.go:64-88`) | **Tombstones** a memory that has not been accessed for the age, and drops its embedding and access row (`internal/core/memory.go:32-50`). It frees bytes only when the tombstone is purged, at least 72 hours later. |
| Task lists, as a whole (no age per task) | `task_expiry_hours`: 720 (0 to 87600; 0 disables) | "task expiry", `taskExpiry` `:162-226` | `ChannelLastActivity`: the latest channel `created_at`, message `created_at` or `tombstone_at` (`sqlstore/tasks.go:18-33`) | Deletes an inactive task list with all of its tasks and subscriptions. |
| Sessions and their cursors | `session_retention_hours`: 72 (1 to 8760), counted from lease expiry | "lapsed sessions", `lapsedSessions` `:16-29` | `DeleteSessions` (`sqlstore/sessions.go:179`) | Deletes sessions that lapsed before the age. Cursors go by cascade. |
| Idempotency receipts | `receipt_retention_minutes`: 60 (1 to 1440), stored as `expires_at` (`internal/core/receipts.go:18`) | "receipts", `receipts` `:88-98` | `DeleteExpiredReceipts` (`sqlstore/receipts.go:76`) | Deletes expired receipts. |
| Assertion replay rows (`assertion_jti`) | No setting: the assertion's `exp` plus `token.Leeway` plus 1 s | "assertion replay expiry", `assertionReplayExpiry` `:313-324` | `DeleteAssertionJTIBefore` (`sqlstore/replay.go:39`) | Deletes expired rows. |
| Empty ordinary channels (not `general`) | `empty_channel_reap_hours`: 24 (1 to 8760) | "empty channels", `emptyChannels` `:228-274` | `EmptyChannelsBefore`, `DeleteChannelIfEmpty` (`sqlstore/channels.go:212-222`) | Deletes channels with no messages and no counting subscription. |
| Scopes | `scope_idle_hours`: 336 (1 to 8760); retired rows purged after `session_retention_hours` | "scope retirement", `scopeRetirement` `:51-59` | `RetireDue`, `PurgeRetired` (`internal/core/scopes/retire.go:305-314`), `DeleteRetiredScopes` (`sqlstore/scopes.go:131`) | Retires an idle scope with its channels, identities and bridges, then purges the retired scope row. |
| Rows derived from a message: `message_tags`, `embeddings`, `embed_failures` | Inherited from the message | None of their own | `ON DELETE CASCADE` (`postgres/schema/0001_initial.sql:132-149`) | Go with their message. Tombstoning also drops the embedding (`core/memory.go:43`; `memories.go:182`). |

The defaults come from `core.Defaults` (`internal/core/config.go:77-103`), the bounds from `Config.Validate` (`config.go:107-134`), and the server settings from `internal/server/config/config.go:53-73`. `docs/ops/configuration.md:66-75` documents them.

**Object types with no age policy (both engines).**

- Live tasks on an active list. Only the whole list expires, and never when `task_expiry_hours` is 0.
- Live memories when `memory_expiry_hours` is 0.
- `enrollment_tokens`. The rows carry `expires_at` (`sqlstore/enrollment.go:20`), but no maintenance job deletes expired ones. Only revocation, the identity's deletion and `ResetRegistry` remove rows (`enrollment.go:141,164`; `sqlstore/meta.go:49`). Under Eric's decision 2026-10-27 this gap closes in eviction, not in an age job: expired tokens are step 2 of the order (section 4.3).
- `retired_identities`, kept on purpose so that a retired name stays reserved (`retire.go:300-304`).
- `bridges`, `bridge_messages` and `bridge_dropped`. They go only with their bridge (orphaned `bridge_messages` rows are step 1 of the eviction order, section 4.3) (removal, the runner, scope retirement: `retire.go:195`, `internal/slackbridge/archive.go:104`). `ResetData` keeps them (`meta.go:21-24`).
- `slack_users`, which go with their identity.
- The registry and configuration rows: `identities`, `identity_keys`, `human_bindings`, `access_refs`, `signing_keys`, `channel_acl`, `subscriptions`, `tag_subscriptions`, `notices`, and memory and task channels.

**Order of the jobs in one tick** (`internal/core/maintenance/service.go:74-103`): lapsed sessions, invalid scopes, scope retirement, abandoned tasks, receipts, message retention, tombstone purge, task expiry, memory expiry, empty channels, budget eviction, capacity notices, embeddings, assertion replay expiry. The age jobs therefore run before the budget jobs in the same tick.

### 3.2 Live probe (2026-10-27, against the CI image)

The probe ran against the same digest as `internal/dbtest/dbtest.go:24` (PostgreSQL 18.6, aarch64). Its schema copies the `messages` shape: two GIN-indexed generated `tsvector` columns, B-tree indexes, and a foreign key to `channels` with `ON DELETE CASCADE`. It was filled with 2500 rows of `repeat('x', 1024)` and 500 rows of 1022 bytes of words, with `autovacuum_enabled = false, toast.autovacuum_enabled = false` on the table. The scripts are scratch files, not in the repository:

- `/private/tmp/claude-501/-Users-efitz-Projects-agentbus2/5920b4ef-ec1e-4462-8eba-51fb721e308c/scratchpad/pgprobe/bloat.sql`
- `/private/tmp/claude-501/-Users-efitz-Projects-agentbus2/5920b4ef-ec1e-4462-8eba-51fb721e308c/scratchpad/pgprobe/forks.sql`
- `/private/tmp/claude-501/-Users-efitz-Projects-agentbus2/5920b4ef-ec1e-4462-8eba-51fb721e308c/scratchpad/pgprobe/ssi.sh`
- `/private/tmp/claude-501/-Users-efitz-Projects-agentbus2/5920b4ef-ec1e-4462-8eba-51fb721e308c/scratchpad/pgprobe/scan2.sql`

| Step | Sum of `pg_total_relation_size` | Logical bytes (content) | Rows |
|---|---|---|---|
| Filled | 5,677,056 | 3,071,000 | 3000 |
| Deleted the 1500 oldest | 5,677,056 (unchanged) | 1,535,000 | 1500 |
| After `VACUUM` | 7,618,560 (**grew**) | 1,535,000 | 1500 |
| Deleted channel 2 (cascade) | 7,618,560 | 1,024,000 | 1000 |
| Inserted 1500 rows after the vacuum | 7,733,248 | 2,560,000 | 2500 |

Findings:

- **Physical size never falls, and it can grow after `VACUUM`.** `forks.sql` isolates the cause. The GIN index went from 1,073,152 to 2,981,888 bytes when `VACUUM` merged its fast-update pending list. The visibility map added 8 KiB. The heap stayed at 3,514,368 bytes.
- **Dead-tuple statistics fall to zero after vacuum while the size does not.** After the delete, `pg_stat_user_tables` reported `n_live_tup = 1500, n_dead_tup = 1500` (read after `pg_stat_force_next_flush()`, which exists from PostgreSQL 15). After `VACUUM` it reported `n_dead_tup = 0`, but the size was larger than before.
- **`pgstattuple` already reported zero dead tuples before any vacuum.** Right after the delete it showed `dead_tuple_count = 0` and `free_space = 1,788,072`. The probe's own `SELECT` had pruned the pages opportunistically. Dead counts are therefore not a stable signal, even with autovacuum off. `pgstattuple_approx` after the vacuum gave `approx_tuple_len = 1,723,488`, which covers the heap only and no indexes.
- **`pgstattuple` cannot be installed by the server's role.** `pg_available_extension_versions` shows `trusted = f, superuser = t` for versions 1.4 and 1.5. The image ships it, together with `pg_freespacemap` and `pg_visibility`.
- **Disk is a multiple of the logical measure.** Physical size over logical bytes was 1.85 when freshly filled and 3.02 after a delete, a vacuum and a refill. This is the ratio that Eric's decision 1 has health report.
- **A trigger-maintained counter catches cascades.** A statement-level trigger with a transition table tracked the counter exactly through the cascade from the channel delete.
- **A trigger-maintained counter row aborts under `SERIALIZABLE`.** Session A began `SERIALIZABLE` and read. Session B took the append lock, inserted (its trigger bumped the counter) and committed. Session A then deleted rows, and its trigger's `UPDATE space_counter` failed with `ERROR: could not serialize access due to concurrent update` (40001).
- **The cost of an on-demand sum.** Summing an `int` column over 200,000 rows in a 223 MB heap took 16 to 20 ms with warm caches and a parallel scan. Scaled linearly, the 2048 MiB default would take roughly 150 to 200 ms warm. A cold read is bounded by storage throughput instead: about 8 s at 250 MB/s. That last figure is an extrapolation, not a measurement.

### 3.3 Store, engines and the budget jobs

**Store contract.**

- `store.Usage{Bytes, Evictable}` and `store.Space{Usage, Reclaim}` are defined at `internal/store/space.go:5-20`.
- `sqlstore` forwards both to the dialect (`internal/store/sqlstore/space.go:9-13`, `dialect.go:39-41`).

**SQLite.**

- `Usage` is `(page_count - freelist_count) * page_size`, read on one connection, with `Evictable: true` (`internal/store/sqlite/space.go:19-37`).
- `Reclaim` runs `PRAGMA incremental_vacuum(256)` outside any transaction (`sqlite/space.go:39-43`).
- The file is converted to `auto_vacuum = INCREMENTAL` once (`sqlite/space.go:45-66`).
- ADR 0004 records these as Milestone 7 choice 4 (`docs/adr/0004-store-contract-and-storage.md:374-382`).
- This measure already meets both properties of section 4.1, because deleted pages go to the freelist, which `Usage` subtracts.

**Postgres.**

- `Usage` sums `pg_total_relation_size` over the plain relations of `current_schema()`, with `Evictable` false (`internal/store/postgres/space.go:11-21`). `Reclaim` is a no-op (`postgres/space.go:23-24`).
- Write transactions are `SERIALIZABLE` (`internal/store/sqlstore/store.go:93`). They are retried on 40001 and 40P01 (`internal/store/postgres/postgres.go:171-176`) up to `TxAttempts = 5` (`postgres.go:27`; Decision 69).
- Appends are the `kindAppend` transactions. They take `LOCK TABLE seq_counter IN EXCLUSIVE MODE` before their first query (`sqlstore/store.go:102-106`, `postgres/postgres.go:228-232`), so concurrent appends queue instead of aborting.
- The `messages` table carries two generated `tsvector` columns and two GIN indexes besides its B-tree indexes (`postgres/schema/0001_initial.sql:107-131`). SQLite uses an external-content FTS5 table kept in step by triggers (`sqlite/schema/0001_initial.sql:128-134`).

**The budget jobs.**

- `budget()` is `BudgetOverride` (tests only) or `SQLiteBudgetMiB << 20` (`internal/core/maintenance/budget.go:16-21`).
- `evictToBudget` (`budget.go:34-64`):
  - It returns at once when the measure is not `Evictable`.
  - It sweeps only once usage exceeds the budget (v1's hysteresis).
  - It deletes `deleteOrdinaryChunk(ctx, math.MaxInt64)` chunks, then calls `Reclaim` and re-measures, until usage is at or below `target = budget - budget*cleanup_free_percent/100`, a chunk deletes nothing, or the tick deadline passes.
  - The `math.MaxInt64` cutoff means it ignores age: on SQLite today, pressure can delete an ordinary message seconds after it was sent. It never touches tombstones, memories or tasks.
  - It ends with one unconditional `Reclaim`.
- `capacityNotice` (`budget.go:70-90`) runs after the sweep. It sets or clears the `capacity` notice in one transaction, and only where usage is `Evictable`. Because it reads usage after the sweep, the notice in practice means "still over budget after eviction". `capacityText` names `sqlite_budget_mib`, says the budget "is filled by live memories and protected tombstones, which are never evicted automatically", and says "restart the server" (`budget.go:93-95`).
- `Tick` takes the `maintenance` lock on every tick and sets the deadline to half the interval (`service.go:123-158`, deadline at `service.go:136`). `RunJob` runs a job without the lock and without a deadline (`service.go:169-179`).
- `deleteOrdinaryChunk` (`jobs.go:108-140`) asks for `OrdinaryMessagesBefore(createdBefore, 1000)`, which is `memory_id IS NULL AND created_at < ? ORDER BY seq LIMIT ?` across all channels. The chunk's messages are deleted with `DeleteMessages`, and each channel's `evicted_before_seq` is raised to the highest deleted seq plus one, all in one transaction. `SeqChannel` is `{Seq, ChannelID}` (`internal/store/messages.go:40`). The same chunk function serves "message retention".

**Settings.**

- `core.Config.SQLiteBudgetMiB` defaults to 2048, bounds 64 to 1048576; `CleanupFreePercent` defaults to 25, bounds 5 to 90 (`internal/core/config.go:63-68`, `99-100`, `128-129`).
- The server settings are `sqlite_budget_mib` and `cleanup_free_percent` (`internal/server/config/config.go:72-73`).
- `docs/ops/configuration.md:85` says the budget is "accepted and has no effect" on Postgres, and `internal/server/config/docs_test.go` checks that table.

**Readers of `Usage`.**

- `channels.Service.Usage`, the admin operation (`internal/core/channels/usage.go:14-40`). It always reports `BudgetBytes` from `SQLiteBudgetMiB` (`usage.go:38`).
- The administrator tier of health (`internal/core/health/health.go:127-131`).
- `agentbus2 status`, which prints the budget only when `Evictable` (`internal/cli/status.go:109-117`).
- The wire `Usage` schema: `bytes`, `budget_bytes`, `evictable`, `notice` (`api/openapi.yaml:1845-1859`). Protocol `1.0.0` (`api/openapi.yaml:4`, `internal/version/version.go:12`). `docs/ops/health.md:55` documents the same members.
- `bus.Usage` says "Evictable is false on Postgres, where no budget is enforced" (`internal/bus/admin.go:18-25`).
- The converters at `internal/server/admin_usage.go:14`, `internal/server/convert.go:79`, `internal/bus/remote/usage.go:22`, `internal/bus/remote/convert.go:184` and `internal/mcpserver/server.go:275,321` all carry `Evictable`.

**Decisions in force.**

- Gate 0 item 2 (`docs/superpowers/specs/2026-10-21-gate-0-decisions.md:127-130`; plan text at `docs/superpowers/plans/2026-10-21-milestone-7-server-deployment.md:69`; ADR 0004 at `:405-410`): the jobs act only where `Evictable`, the notice is never set on Postgres, and **the setting keeps its v1 name `sqlite_budget_mib`**. Issue #7 is the follow-up that item foresaw.
- The plan's first revision listed two rejected alternatives (`git show 41e5064:docs/superpowers/plans/2026-10-21-milestone-7-server-deployment.md`, line 64): "Evict on Postgres too, bounded per tick by the estimated bytes of the deleted rows, accepting the runaway under bloat", and "Postgres eviction only when the `pgstattuple` extension is installed (an advertised capability)". Eric's decisions of 2026-10-27 now rule out both (section 4.1).
- Gate 0 item 3: tick-only eviction, and writes are never refused.
- The master spec repeats item 2 (`docs/superpowers/specs/2026-10-19-agentbus2-design.md:768-775`).

**Tests that encode today's behavior.**

- `TestMaintenanceBudgetEviction` (`internal/conformance/bus/maintenance_test.go:500-569`): its Postgres half asserts that nothing is deleted.
- `TestMaintenanceCapacityNotice` (`maintenance_test.go:572-611`): its Postgres half asserts that no notice is set.
- The bus harness keeps the schema-scoped DSN (`internal/conformance/bus/fixtures_test.go:146`), so a test can open its own `*sql.DB` on the test schema.
- Postgres for the suites is `cgr.dev/chainguard/postgres` pinned by digest (`internal/dbtest/dbtest.go:24`). The probe found it is PostgreSQL 18.6, and the testcontainers role is a superuser.
- `windows.yml` runs `go test` on everything except `internal/conformance` (`.github/workflows/windows.yml:30`).

## 4. Options

### 4.1 Settled by Eric's decisions of 2026-10-27

A correct enforced measure must have two properties. Today's Postgres measure has neither.

1. **Within a tick:** deleting rows lowers it before any vacuum, so the sweep can stop at the target.
2. **Across ticks:** the "over budget?" check at the start of a tick does not count dead tuples or free space, so a tick under bloat does not delete another budget's worth of rows. This is the real runaway. A per-tick cap only slows it down.

**The enforced measure is logical (decided: Eric's decision 1).** The basis the rest of the brief builds on:

- The budgeted quantity is the sum, over the rows of `messages`, of a fixed per-row formula: `octet_length` of `content`, `subject`, `type` and `metadata`, plus a fixed per-row overhead constant. It is computed identically on both engines (`length(CAST(x AS BLOB))` on SQLite).
- It meets both properties by construction: a delete lowers it in the same transaction, and dead tuples, free space, indexes and TOAST never enter it.
- It needs no extension and no privilege (Eric's decision 2). It is deterministic, so a test can predict it to the byte on both engines.
- Disk is some multiple of it: 1.85 to 3.02 in the probe. Health and notices report the disk figure and the ratio (Eric's decision 1), and the operations docs explain how to turn a disk limit into a budget with that ratio.
- How the measure is kept is still open (section 4.10), and so is what it counts (section 4.11).

**Superseded options** (kept for the record):

- *`pgstattuple` when installed, and no budget elsewhere* (the plan's original option C). Superseded by Eric's decision 2: eviction never depends on an extension. Section 4.13 keeps only its permitted remainder, extra detail in reports.
- *Per-row byte estimates against `pg_total_relation_size`* (the plan's original option B). Superseded by Eric's decision 1: physical size is never enforced. It also failed property 2.
- *`pg_total_relation_size` scaled by `pg_stat_user_tables` dead-tuple statistics.* Superseded by Eric's decision 1. It also failed property 2, because `n_dead_tup` falls to 0 after vacuum while the size stays the same or grows (section 3.2).
- *No wire change for usage.* Superseded by Eric's decision 1: health must report the physical size and the ratio (section 4.12).
- *Asserting bloat through `pgstattuple` in tests.* Superseded by Eric's decision 2 (section 8).
- *A Postgres-specific capacity notice job.* Superseded by Eric's decision 3 together with section 4.6: one backstop model, one code path.
- *A retained floor as the per-tick bound* (option D of the earlier draft's per-tick bound section). It reappears in section 4.5, where floors are minimum guarantees rather than a bound.

### 4.2 How "more aggressive under space pressure" works

The age jobs stay the primary policy (Eric's decision 3). The question is what the backstop adds when the measure is over the budget. "Budget" and "target" below are today's numbers: `target = budget - budget*cleanup_free_percent/100`.

**Option A: stepped age tiers.**

- Thresholds as fractions of the budget, for example warn at 90%, soft at 100% and hard at 110%.
- At soft, each eligible type's effective age is its configured age times a factor (for example one half), and at hard a smaller factor (for example one quarter), never below the type's floor (section 4.5).
- The tier would have to be computed at the top of `Tick`, from one `Usage` read, and passed into each age job, because the age jobs run before the budget jobs (section 3.1). Otherwise the shorter age lands a tick late.
- Cost: a step deletes a fixed slice of history whatever the overshoot. Halving 168 hours deletes 84 hours of messages when the bus is 1% over. It needs at least two new threshold settings and a factor per type, and every age job changes its signature.

**Option B: an oldest-first backstop after the age jobs, with tiers defined by outcome (Recommended).**

- The age jobs stay exactly as they are. The backstop is today's "budget eviction" job, generalized: when the measure exceeds the budget at the start of the job, it deletes the oldest eligible rows in a fixed type order until the allowance is met (section 4.7), never past each type's floor (section 4.5).
- This is the exact limit of option A. Deleting the oldest ordinary messages until the target is reached is the same as shortening their effective age to the age of the oldest survivor. It just computes the shortest sufficient age instead of stepping toward it.
- The tiers are defined from the two numbers that already exist, so no new threshold setting is needed:

| Tier | Condition | What is deleted beyond the age policies | Reported |
|---|---|---|---|
| Normal | usage at or below the target | Nothing | Health: `normal` |
| Approaching | target < usage <= budget | Nothing (hysteresis: the sweep starts only above the budget) | Health: `approaching`; no notice (section 4.8) |
| Pressure (soft) | usage > budget at the start of the job | Steps 1 and 2 of Eric's order (section 4.3): useless data (orphaned `bridge_messages`), then expired data (tombstones older than their floor, expired `enrollment_tokens`), until the allowance is met | Health: `soft`; notice only if the sweep ends still over budget |
| Hard | steps 1 and 2 are done and usage is still above the target | Steps 3 and 4: idle data (idle task channels and idle memories; decision 20), then ordinary messages, oldest first by `seq`, older than their floor, until the allowance is met | Health: `hard`; notice only if the sweep ends still over budget |
| Exhausted | all four steps reached their limits (nothing eligible is left) and usage is still over budget | Nothing more | Health: `exhausted`; the `capacity` notice |

- The sweep logs one line per run: the rows deleted per type, the bytes, and the effective age it reached for each type.
- The tier is computed by one pure function from `(usage, budget, target, what the sweep could still delete)`, which a table test covers.
- Known cost of tiers defined by outcome: the approaching band is as wide as `cleanup_free_percent`. At its maximum of 90 (`internal/core/config.go:129`) the band is 10% to 100% of the budget, so health reports `approaching` almost all the time. It sets no notice and deletes nothing, so the cost is a less informative health value, not extra deletion. A separate warn threshold setting would fix it, and the brief does not recommend one.

**Option C: continuous proportional age.**

- Each tick, every eligible type's effective age becomes its configured age times `target / usage`, never below its floor.
- It reacts smoothly, but it overshoots or undershoots depending on how age maps to bytes, it can oscillate across ticks, and its outcome is hard for a test to predict.

**DECIDED: B (Eric's decision 2026-10-27).** **Open note:** the soft and hard tier definitions in the table are the brief author's derivation from Eric's order, not his words; Eric has not confirmed them. The tier names and conditions in the table follow Eric's eviction order (section 4.3), which replaces the earlier two-step order. It deletes only what the backstop needs, it reuses `evictToBudget`'s loop and `deleteOrdinaryChunk`, it needs no new threshold setting, it does not touch the age jobs, and its tiers are predictable in a test.

### 4.3 Which object types pressure deletes, and in what order

**DECIDED, replaces the earlier decisions 2 and 3 (Eric's decision 2026-10-27): Eric's eviction order.** Under pressure the sweep deletes in this order, taking a step only while usage is still above the target:

1. **Useless data.** Rows nothing can reach any more. Today that is orphaned `bridge_messages`: a row whose `seq` no longer exists in `messages` (the table has no foreign key to it, so retention leaves such rows behind).
2. **Expired data.** Tombstones older than their floor (section 4.5), expired `enrollment_tokens` (`expires_at` in the past), and any other row that has an expiry and no job.
3. **Idle data.** Idle task channels, and idle memories. Eric: "we can define in more detail what this means". Decision 20 defines idle: no activity (a new message, a task change or a read) within `idle_window_hours`, default 168 (Eric's decision 2026-10-27).
4. **Ordinary messages**, oldest first, older than the message floor (section 4.5).

**Never deleted: an active task channel**, meaning any task channel with activity within the idle window. This replaces the earlier rules "never memories or tasks" and "never types with no age policy". Still never deleted by pressure: sessions and cursors, receipts, assertion rows, identities and keys, ACLs and subscriptions, channels other than idle task channels, scopes, bridges, retired identities, and notices. Each still follows its own age policy, if it has one.

Consequences:

- Expired enrollment tokens and orphaned `bridge_messages` are part of eviction, so the two follow-up issues that the earlier draft proposed are not filed.
- Tombstones are no longer the second lever behind ordinary messages. They are step 2, ahead of idle data and ordinary messages, but only past the 72-hour floor.
- A tombstone keeps its whole `content` (`sqlstore/messages.go:376-386`), so purging one frees the whole row. Tombstoning a live memory frees nothing until the tombstone is purged, so step 3 deletes an idle memory outright, which frees space at once (Eric's decision 2026-10-27, decision 20).
- Deleting an idle task channel deletes the list with every task in it, as task expiry does. Its size is the sum of the rows it holds (section 4.10).

Earlier options, all **superseded by Eric's decision 2026-10-27**:

- *A: ordinary messages first, then tombstones; never memories or tasks (the earlier recommendation).* The order is now Eric's, and memories and task channels can be deleted when idle.
- *B: A, plus a shortened memory expiry and task expiry in the hard tier, each with a floor.* Replaced by step 3.
- *C: ordinary messages only (today's SQLite behavior).*

### 4.4 Object types with no age policy

**DECIDED, changed by Eric (2026-10-27): the two bookkeeping gaps move into eviction.** These are the types listed in section 3.1: live tasks on an active list, memories when `memory_expiry_hours` is 0, `enrollment_tokens`, `retired_identities`, the bridge tables, `slack_users`, and the registry and configuration rows.

- **Now in eviction:** expired `enrollment_tokens` (step 2) and orphaned `bridge_messages` (step 1). Pressure, not an age job, is their only trigger, as Eric decided.
- **Idle live tasks and memories** are step 3 of the order, as decision 20 defines idle. **Open note:** whether an operator's explicit `memory_expiry_hours` or `task_expiry_hours` of 0 ("keep these") stops step 3 from touching that type; Eric's answers did not say.
- **Still outside the backstop:** `retired_identities` (kept on purpose so that a retired name stays reserved), `bridges`, `bridge_dropped`, `slack_users`, and the registry and configuration rows. The exhausted notice says so.

*Superseded by Eric's decision 2026-10-27:* Option A, "never" for every type without an age policy, and its two follow-up issues (expired `enrollment_tokens`, orphaned `bridge_messages`). Option B (the bookkeeping rows with an expiry column) is adopted as steps 1 and 2. Option C (a last-resort tier over memories by oldest access, whatever the setting) was rejected earlier; step 3 replaces it with the idle definition.

### 4.5 Minimum retention guarantees under pressure

What every tier keeps, whatever the measure says.

**DECIDED: the recommendations are ratified (Eric's decision 2026-10-27): messages 1 hour, tombstones 72 hours, even under pressure.**

**Floor for ordinary messages.**

- **Option A: one hour, a constant equal to the lower bound of `message_retention_hours` (`internal/core/config.go:119`) (Recommended; DECIDED).** Pressure never deletes a message that an operator could not have asked retention to delete. It needs no new setting.
- **Option B: a new setting, `pressure_min_message_minutes` (for example a default of 60, bounds 1 to `message_retention_hours * 60`).**
- **Option C: no age floor; keep the newest N messages per channel instead.**
- **Option D: no floor (today's SQLite behavior).**
- Each option except D changes SQLite: a burst younger than the floor is no longer evicted, so the file can exceed the budget until the burst ages past the floor, with the notice set meanwhile. Writes are still never refused (Gate 0 item 3).

**Floor for tombstones.**

- **Option A: 72 hours, the lower bound of `tombstone_min_hours` (`config.go:120`) (Recommended; DECIDED).** Step 2 then frees tombstone space only where an operator set `tombstone_min_hours` above 72. With the default of 72, step 2 deletes no tombstone beyond what tombstone purge already does, so the further relief comes from its other expired rows and from steps 3 and 4.
- **Option B: the configured `tombstone_min_hours` itself,** which takes tombstones out of the backstop altogether (section 4.3 option C).
- **Option C: a lower pressure floor, for example 24 hours.** The brief found no recorded rationale for the 72-hour lower bound, so this option needs Eric's confirmation that nothing depends on it. Two known consumers: tombstones are a memory's revision history (`memories revisions`), and `tombstone_at` counts as activity in `ChannelLastActivity` (`sqlstore/tasks.go:18-33`), so purging a task list's tombstones early can make the list look inactive sooner and expire earlier.

**Never deleted by pressure, whatever the tier:** the list of section 4.3, and in particular an active task channel. Live memories and tasks are no longer on the list: an idle one is step 3.

### 4.6 SQLite in the same model: one code path

Eric's decision 1 places the logical measure on eviction and names `pg_total_relation_size` for the reports, which is Postgres-specific. This brief reads the decision as "the enforced measure must be immune to bloat", and SQLite's used-page measure already is (section 3.3). Option C below is the literal reading in which both engines use the logical sum. Eric chooses.

**Option A: one backstop in `core/maintenance`; only the measure differs per engine (Recommended).**

- `store.Usage` becomes `{Bytes, DiskBytes, Logical}`. `Bytes` is the enforced measure: used pages on SQLite, the logical sum on Postgres. `DiskBytes` is the physical size: the whole file on SQLite (`page_count * page_size`, freelist included), the relation sum on Postgres. The ratio is then meaningful on both engines.
- The tier function, the floors, the type order, the allowance and the notice text are engine-free. The single engine branch is `Usage.Logical`: subtract the deleted rows' sizes (logical), or re-measure after each chunk (SQLite, where the PRAGMA is cheap).
- `Reclaim` stays per engine (section 4.14).
- SQLite gains the floors of section 4.5 and the four steps of section 4.3. Its budget keeps meaning file bytes.
- Decision 5 A is **DECIDED** (ratified by Eric 2026-10-27).

**Option B: one code path, with SQLite also using the logical sum.**

- One meaning of the budget on both engines, and the same test numbers on both.
- A SQLite user loses the file-size bound v1 gave. It reverses the SQLite half of ADR 0004 Milestone 7 choice 4 (architectural). It pairs with the single `budget_mib` of section 4.9 option C.

**Option C: keep SQLite's loop and add a separate Postgres path.**

- Rejected. It duplicates the tier rules, which then drift apart.

Recommendation: A (**DECIDED**, ratified).

### 4.7 Hysteresis and the per-tick bound

With a logical measure, bloat cannot cause a runaway: deletion is driven only by `Bytes`, and `DiskBytes` never enters any decision (Eric's decision 1). A per-tick bound is defense in depth against a broken measure or a future regression.

**Hysteresis (the same under every option below):** the sweep starts only when `Bytes > budget` and stops at or below the target, as v1 and today's SQLite path do. The approaching tier is the band between them, so a bus that hovers just under the budget never sweeps.

**Option A: a byte allowance per tick, the same on both engines (Recommended).**

- At the start of a sweep, read `u0` once. If `u0 > budget`, the allowance is `u0 - target`.
- The sweep deletes chunks in the order of section 4.3 (useless data, expired data, idle data, then ordinary messages), until the sum of the deleted rows' `size_bytes` reaches the allowance, all four steps reach their limits, or the deadline passes. Where re-measuring is cheap (SQLite), it also stops once the re-read usage is at or below the target.
- On Postgres it does not re-measure inside the loop. The estimate `u0 - deleted` is exact because it uses the same formula as the measure.
- One final `Usage` read follows, and the notice job reuses it (section 4.8), so a tick reads `Usage` twice on Postgres. When the final measure is at or above `u0` although rows were deleted, the job logs a warning. This check is intentionally coarse, because `u0` goes stale by whatever was appended during the sweep. The warning is not a job error.
- Net effect: no tick deletes more than one allowance's worth of rows, whatever the measure does.
- The SQLite sweep reaches the target in one run because a row frees at least its logical bytes. That needs the overhead constant of section 4.11 to be conservative, and a test pins it.

**Option B: a fixed row cap per tick,** for example ten chunks (10,000 rows). It is simple but unrelated to bytes: too small for a burst of small messages, too large for 64 KiB messages.

**Option C: a fraction cap,** at most `cleanup_free_percent` of the eligible rows present at the start of the tick. It bounds rows per tick, but repeated ticks still compound under a broken measure.

Recommendation: A (**DECIDED**, ratified).

### 4.8 Capacity notices and health at each tier

The notice reaches agents through `status` and `receive`. Agents cannot raise a budget, so a notice that asks nothing of them is noise. Health's administrator tier is the operator's channel.

**Option A: one `capacity` notice from the approaching tier up, with the text changing per tier.**

**Option B: health and the log at every tier; the `capacity` notice only when the sweep ends still over budget (Recommended).**

- Health's administrator tier always carries the tier, `bytes`, `budget_bytes`, `disk_bytes` and the ratio (section 4.12; Eric's decision 1). It has no notice in the normal and approaching tiers.
- After a sweep that ended over budget with eligible rows left (the deadline or the allowance stopped it): "agentbus2 storage is over budget: <setting>=<N> MiB holds <B> MiB of messages (disk <D> MiB, <R>x). Orphaned and expired rows, idle task channels and memories, and ordinary messages older than <age> are being deleted to make room."
- Exhausted: "agentbus2 capacity exhausted: <setting>=<N> MiB is filled by active task channels, memories that are not idle, ordinary messages younger than <floor> and tombstones younger than <floor>, which pressure never deletes. Raise <setting> and restart every replica, shorten memory_expiry_hours or task_expiry_hours, or have an administrator reset the bus." On Postgres the text also gives the disk figure and the ratio, so the operator can size the disk.
- The notice is cleared once a later tick ends at or below the budget. It never flaps inside the hysteresis band, because the approaching tier sets no notice.

**Option C: today's behavior.** The notice is set only in the exhausted case, and the earlier tiers appear only in the log.

Recommendation: B (**DECIDED**, ratified). Whether the exhausted tier also marks health `degraded` is part of this decision. The brief recommends that it does: an operator acts on it.

### 4.9 The budget setting (Gate 0 item 2)

Every option here changes Gate 0 item 2 ("the jobs act only where `Usage.Evictable`", "the notice is never set" on Postgres, and "the setting keeps its v1 name"). That makes it an **architectural change that needs Eric's approval**. Issue #7 is the follow-up that item foresaw.

**Option A: add `postgres_budget_mib` and keep `sqlite_budget_mib` (Recommended; DECIDED, ratified).**

- Each setting names the measure it governs: used file bytes on SQLite, logical bytes on Postgres.
- `budget()` picks one by `Store.Engine()`, and the notice text names the engine's setting.
- The SQLite setting keeps its v1 name, as item 2 decided.
- No new setting for the tiers: they come from the budget and `cleanup_free_percent` (section 4.2), and the floors from existing bounds (section 4.5 option A).

**Option B: rename to one engine-neutral `budget_mib` whose meaning differs by engine.** One knob, but the same number means a file size on one engine and content bytes on the other. It renames a setting that item 2 decided to keep. Before the first release the rename is allowed, with no compatibility alias.

**Option C: one `budget_mib` in logical bytes on both engines.** It goes with section 4.6 option B.

**Default for the Postgres budget.**

- **A (Recommended; DECIDED, ratified): 2048, on by default, as on SQLite.** The budget is a backstop (Eric's decision 3), and a backstop that is off by default does not catch the deployment that never set it. A full shared database is an outage for every replica. The bounds 64 to 1048576 match SQLite's. At the probe's ratios, 2048 MiB logical is 3.7 to 6 GiB of disk, which the deploy docs state.
- **B: 0 means off, an explicit opt-in;** the bounds become 0, or 64 to 1048576. Nothing beyond the age policies is ever deleted until an operator turns the backstop on.

### 4.10 How the logical measure is kept

**Option A: computed on demand from a stored per-row size (Recommended).**

- **DECIDED, changed by Eric (2026-10-27): this extends to every table that eviction can delete from, not only `messages`** (section 4.11). Each such table gets a generated stored column `size_bytes` holding that table's formula, and a covering index where the sum would otherwise read the heap. The rest of this option is written for `messages` and applies to each table in turn.
- A generated stored column `size_bytes` on `messages` holds the formula.
  - It fits the schema's practice: the schema already has generated stored columns (`0001_initial.sql:122-123`, PostgreSQL 12 or later).
  - SQLite supports generated columns from 3.31. `modernc.org/sqlite` is v1.61.0 (`go.mod:27`); the implementer confirms the embedded SQLite version. The column exists on SQLite too, so the shared `sqlstore` queries can return it, even though SQLite does not enforce on it (section 4.6 option A).
- `Usage` on Postgres is the sum of `coalesce(sum(size_bytes), 0)` over those tables (for `messages`, `SELECT coalesce(sum(size_bytes), 0) FROM messages`). A covering B-tree index, `(seq) INCLUDE (size_bytes)`, can let it run as an index-only scan of about 24 to 32 bytes per row instead of reading the heap. The implementer confirms with `EXPLAIN` that the planner chooses it.
- No counter means there is nothing to drift, no new hot row, no trigger and no new `SERIALIZABLE` conflict.
- Cost: one aggregate scan per `Usage` call (section 3.2: roughly 150 to 200 ms warm at 2 GiB without the index). The sweep therefore does not re-measure after each chunk on Postgres (section 4.7). The callers are the sweep (start and end), the notice job (which reuses the sweep's final read, section 4.7), the admin usage operation, and the administrator tier of health. An operator who polls that tier pays one scan per poll. `GET /v1/discover` stays the liveness probe and does not read `Usage`.
- Every query the sweep uses to pick rows (`OrdinaryMessagesBefore`, `ExpiredTombstones`, and the new orphaned-row, expired-token and idle-data queries) returns each row's `size_bytes`, so the sweep counts exactly what it deletes. A channel delete (an idle task channel) sums the sizes of the rows it will cascade to, in the same transaction, before it deletes them.

**Option B: a counter row maintained by statement-level triggers.**

- `AFTER INSERT` and `AFTER DELETE ... FOR EACH STATEMENT` triggers with transition tables keep the counter current. The probe showed they also catch `ON DELETE CASCADE`.
- `Usage` becomes a one-row read.
- Cost: a new contention point under `SERIALIZABLE`. The probe reproduced 40001 when a delete transaction's snapshot predated a committed append. Avoiding it means every message-deleting transaction must take the append lock before its first query: the maintenance chunks (`jobs.go:128,152`), the channel delete (`internal/core/channels/channels.go:229`), scope retirement (`internal/core/scopes/retire.go:229`), the reaping jobs (`jobs.go:208,262`) and `ResetData` (`internal/core/identities/admin.go:185`, `internal/store/sqlstore/meta.go:17-35`).
- Taking that lock serializes eviction with appends, and `Store.Tx` would need an option that does not exist today. A reconciliation job is still prudent.

**Option C: a counter maintained in `sqlstore` Go code at each insert and delete site.** It needs no triggers, but `DeleteChannel` and `ResetData` must pre-sum what their cascade will remove, and every future delete path must remember to do the same (Definition of Done B). It has the same `SERIALIZABLE` conflict as option B.

Recommendation: A (**DECIDED**, ratified, with the extension to every deletable table). It has no drift and no `SERIALIZABLE` conflicts; the probe reproduced 40001 for option B.

### 4.11 What the measure counts

**DECIDED, changed by Eric (2026-10-27): every table that eviction can delete from.**

- The measure is the sum, over those tables, of each table's fixed per-row formula plus a per-row overhead constant chosen so that the measure stays at or below the bytes a row frees. Section 4.7 relies on that bound.
- The set is exactly the tables the sweep deletes from, directly or by cascade: `messages` (ordinary messages, tombstones, memories and task messages), `bridge_messages`, `enrollment_tokens`, and the rows that go with a deleted message or channel (`message_tags`, `embeddings`, `embed_failures`, `memory_access`, and the rest of an idle channel's rows). The implementer lists the final set from the schema.
- A test guards the set (Definition of Done B): it fails when a table that any eviction step can delete from has no `size_bytes`, so a future delete path cannot go uncounted.
- Rows no step deletes (sessions, receipts, registry rows and the like) stay out of the measure, as they stay out of eviction.

*Superseded by Eric's decision 2026-10-27:* Option A, `messages` rows only (the earlier recommendation), and Option B as a limited extension to `message_tags` and `embeddings`. Eric's rule is wider and is defined by what eviction deletes, not by a table list.

### 4.12 The usage figure on the wire and in health

Eric's decision 1 requires health and the notices to report the physical size and the ratio, so the disk figure goes on the wire. The protocol stays 1.0.0 (Eric's decision 2026-10-27).

**Option A: add `disk_bytes`, `disk_ratio` and `tier`, and redefine `evictable` (Recommended).**

- `bytes` is the enforced measure (section 4.6). `disk_bytes` is the physical size. `disk_ratio` is `disk_bytes / bytes`, or 0 when `bytes` is 0. `tier` is one of the five tiers of section 4.2: `normal`, `approaching`, `soft`, `hard` or `exhausted`. Any other value is rejected by the clients' converters.
- `evictable` means "a budget is enforced", which is false only when a Postgres budget of 0 is allowed and set (section 4.9).
- They appear in the admin `Usage` schema and in the health document's `storage` member.
- `store.Usage` drops `Evictable` and gains `DiskBytes` and `Logical`.

**Option B: A without `disk_ratio`; clients derive the ratio from `bytes` and `disk_bytes`.** Less on the wire. But Eric's decision 1 names the ratio as something health reports, and a client that reads the JSON would compute it itself.

**Option C: replace `evictable` with a `measure` enum (`file`, `logical`, `none`), plus the members of A.** More explicit, but it removes a member, which is allowed before the first release, and every client and test changes.

Recommendation: A (**DECIDED**, ratified).

### 4.13 Optional extension detail in reports

Eric's decision 2 allows extensions to add detail to reports when they are present, and never to drive eviction.

- **Option A: none in this issue (Recommended).** The ratio already answers the operator's question, which is how much disk a budget needs. The probe showed that the extension's numbers are unstable (page pruning zeroed the dead count) and cover the heap only. Detail can come later as its own issue.
- **Option B: when `pgstattuple` is installed and callable, health adds `pgstattuple_approx` free and dead bytes for `messages`.** It needs a probe for presence and privilege on every health read, plus a test that runs only where the extension can be installed, which an external `AGENTBUS2_TEST_POSTGRES_DSN` role may not allow.

Recommendation: A (**DECIDED**, ratified).

### 4.14 `Reclaim` on Postgres

- **Option A: stay a no-op; autovacuum owns it (Recommended; DECIDED, ratified).** The logical measure does not need vacuum to be correct. A sweep of 25% of the rows exceeds the default `autovacuum_vacuum_scale_factor` of 0.2, so autovacuum follows. The disk levels off at its high-water mark, and the docs say so.
- **Option B: run `VACUUM messages` after a sweep that deleted rows.** It runs outside a transaction, as `Reclaim` already does, and needs only ownership, which the schema's role has (so Eric's decision 2 allows it). It makes space reusable sooner but never lowers `pg_total_relation_size` (section 3.2), and it adds input/output work on the tick.

### 4.15 Replicas under `Store.Locks`

- The jobs run only under the `maintenance` advisory lock (`internal/store/postgres/lock.go:10`; `service.go:126`), so normally one replica evicts.
- **Failover in the middle of a sweep.** If the holder's lock session dies, it does not notice until its next `TryLock`. Its chunks run on pooled connections, so a second replica can hold the lock and sweep during the same interval.
  - Both pick the oldest chunks. Overlapping deletes either conflict under `SERIALIZABLE` and retry, or delete rows that are already gone, which is harmless: `SetEvictedBefore` takes the maximum (`internal/store/sqlstore/channels.go:230-235`).
  - With the allowance of section 4.7, the worst case is two allowances in one interval. That is bounded, not a runaway, and the floors of section 4.5 hold in both.
  - The chunks that committed stay committed, with `evicted_before_seq` raised in the same transaction, and the next holder re-measures from scratch.
- **Settings skew.** The lock holder's budget is the one enforced, but each replica's `admin usage` reports its own `budget_bytes`.
  - A, Recommended (**DECIDED**, ratified): the deploy docs require every replica to have the same budget, alongside the shared `issuer`.
  - B: the holder writes the budget it enforced into the notice or meta row, and every replica reports that value.

## 5. Decisions for Eric

Status: 20 decided, 0 open. Three open notes remain (see the Status section and decision 20). Every "ratified" below is Eric's acceptance on 2026-10-27 of the recommendation as written.

1. **How "more aggressive under pressure" works** (section 4.2). **DECIDED: B (Eric's decision 2026-10-27)**, ratified; its tiers follow Eric's eviction order of decision 2.
   - A: stepped age tiers with new thresholds and factors, passed into the age jobs.
   - B: an oldest-first backstop after the age jobs, with tiers defined by outcome from the budget and `cleanup_free_percent`.
   - C: continuous proportional age.
2. **Which types pressure deletes, and the order** (section 4.3). **DECIDED: Eric's eviction order (Eric's decision 2026-10-27)**, replacing the options below. Under pressure, delete in this order: (1) useless data, such as orphaned `bridge_messages`; (2) expired data: tombstones, expired enrollment tokens and so on; (3) idle data: idle task channels and idle memories (definition open, decision 20); (4) ordinary messages. **Never** delete an active task channel, meaning any activity within the idle window.
   - *Superseded by Eric's decision 2026-10-27:* A: ordinary messages, then tombstones; never memories or tasks. B: A, plus a shortened memory expiry and task expiry in the hard tier. C: ordinary messages only.
3. **Types with no age policy** (section 4.4). **DECIDED: Eric's eviction order covers them (Eric's decision 2026-10-27).** Expired `enrollment_tokens` and orphaned `bridge_messages` become part of eviction (steps 2 and 1), not follow-up issues. The registry, bridge and identity rows stay outside the backstop.
   - *Superseded by Eric's decision 2026-10-27:* A: never touched by pressure, with follow-up issues for expired `enrollment_tokens` and orphaned `bridge_messages`. B: only the bookkeeping rows with an expiry column (adopted, as steps 1 and 2). C: a last-resort tier over memories.
4. **Minimum retention guarantees** (section 4.5). **DECIDED: A for both (Eric's decision 2026-10-27)**: messages 1 hour, tombstones 72 hours, even under pressure. This changes SQLite: a burst younger than one hour is no longer evicted.
   - Messages: A, a one-hour constant; B, a new setting; C, the newest N per channel; D, no floor.
   - Tombstones: A, 72 hours; B, the configured `tombstone_min_hours`; C, a lower pressure floor.
5. **SQLite in the same model** (section 4.6; option B is architectural). **DECIDED: A (Eric's decision 2026-10-27)**, ratified.
   - A: one code path, with the measure differing per engine (used pages on SQLite, logical on Postgres).
   - B: logical on both engines.
   - C: two paths.
6. **Hysteresis and the per-tick bound** (section 4.7). **DECIDED: A (Eric's decision 2026-10-27)**, ratified.
   - A: start above the budget, stop at the target, with a byte allowance of `u0 - target` counted from the deleted rows' sizes.
   - B: a fixed row cap.
   - C: a fraction cap.
7. **Capacity notices and health per tier** (section 4.8). **DECIDED: B (Eric's decision 2026-10-27)**, ratified.
   - A: a notice from the approaching tier up.
   - B: health and the log at every tier; the notice only when the sweep ends over budget, with separate "over budget" and "exhausted" texts; exhausted marks health degraded.
   - C: today's behavior (exhausted only).
8. **Enabling the backstop on Postgres changes Gate 0 item 2, and the setting** (section 4.9; architectural). **DECIDED: A (Eric's decision 2026-10-27)**, ratified.
   - A: add `postgres_budget_mib` beside `sqlite_budget_mib`.
   - B: rename both to `budget_mib`, with a meaning that depends on the engine.
   - C: one logical `budget_mib` on both engines (goes with decision 5 B).
9. **The default Postgres budget** (section 4.9). **DECIDED: A (Eric's decision 2026-10-27)**, ratified.
   - A: 2048 MiB, on by default.
   - B: 0, off by default.
10. **How the logical measure is kept** (section 4.10). **DECIDED: A, extended to every table eviction can delete from (Eric's decision 2026-10-27)**, changed.
    - A: an on-demand `sum(size_bytes)` over a generated column, with a covering index. Extended: the column and the sum cover every table the sweep can delete from, not only `messages`.
    - B: a counter row maintained by triggers, with every delete path taking the append lock.
    - C: a counter maintained in Go at each site.
11. **What the measure counts** (section 4.11). **DECIDED: every table that eviction can delete from (Eric's decision 2026-10-27)**, changed.
    - *Superseded by Eric's decision 2026-10-27:* A: `messages` only. B: also `message_tags` and `embeddings`.
12. **Usage on the wire and in health** (section 4.12). **DECIDED: A (Eric's decision 2026-10-27)**, ratified. The protocol stays 1.0.0 in every option (Eric's decision 2026-10-27).
    - A: add `disk_bytes`, `disk_ratio` and `tier` (five values), and redefine `evictable`.
    - B: A without `disk_ratio`.
    - C: a `measure` enum replacing `evictable`.
13. **Optional extension detail in reports** (section 4.13). **DECIDED: A (Eric's decision 2026-10-27)**, ratified.
    - A: none in this issue.
    - B: `pgstattuple_approx` detail in health when installed and callable.
14. **`Reclaim` on Postgres** (section 4.14). **DECIDED: A (Eric's decision 2026-10-27)**, ratified.
    - A: a no-op.
    - B: `VACUUM messages` after a sweep.
15. **Replica settings skew** (section 4.15). **DECIDED: A (Eric's decision 2026-10-27)**, ratified.
    - A: the docs require equal budgets.
    - B: the holder records the budget it enforced.
16. **The bloat assertion in tests** (section 8). **DECIDED: A (Eric's decision 2026-10-27)**, ratified.
    - A: assert physical size against the logical measure only, with no extension and no statistics.
    - B: also assert `n_dead_tup` through `pg_stat_force_next_flush()`, which needs PostgreSQL 15 or later.
    - Dead counts are unstable, because page pruning cleared them in the probe. (Asserting through `pgstattuple` is superseded by Eric's decision 2.)

Decided earlier on 2026-10-27:

17. **What the budget protects: decided, C, both** (Eric's decision 2026-10-27). Eviction is enforced on a logical measure. Notices and health report the physical size and the physical-to-logical ratio.
18. **Privileges: decided, plain table ownership** (Eric's decision 2026-10-27). No extension or extra privilege for eviction. Extensions may only add detail to reports.
19. **Retention model: decided, age first, size as a backstop** (Eric's decision 2026-10-27). Age-based retention per object type is primary. Under space pressure the bus deletes more aggressively.

Decided after the first review pass, the same day:

20. **What "idle" means for task channels and memories, and the idle window.** **DECIDED (Eric's decision 2026-10-27).** Step 3 of the eviction order uses it:
    - Activity is a new message, a task change or a read. Any activity within the window makes a channel active, and an active task channel is never deleted. `ChannelLastActivity` already counts the latest channel creation, message and tombstone time (`sqlstore/tasks.go:18-33`), which covers new messages and task changes; a read is not recorded today, so the implementation adds a last-read marker for task channels (how is **an open note**; reads must not become a write on every receive).
    - For a memory, the same three kinds of activity count; a read is already recorded in `memory_access.accessed_at`, as memory expiry uses.
    - The idle window is a configurable setting, `idle_window_hours`, default 168 (7 days). It is shorter than the defaults of `task_expiry_hours` (720) and `memory_expiry_hours` (8760), so it matters; the settings validation rejects values below 1, and the bounds are chosen in the settings task.
    - **Open note:** whether an operator's `memory_expiry_hours` or `task_expiry_hours` of 0 exempts that type from step 3. Eric's answers did not say.
    - An idle memory is deleted outright, not tombstoned (a tombstone frees nothing before its 72-hour floor).
    - Both engines' settings tables gain `idle_window_hours` (task 3), and step 3 is built with the others.

## 6. Dependencies

- **Issues:** none blocking. Issues #6 and #8 (signing keys) are unrelated. Under Eric's decision 2026-10-27 expired `enrollment_tokens` and orphaned `bridge_messages` are eviction steps, not follow-up issues. Decision 20 (idle) is decided.
- **Libraries:** none new.
  - pgx v5.11.0 (`go.mod:18`), testcontainers-go v0.44.0 (`go.mod:24`) and modernc.org/sqlite v1.61.0 (`go.mod:27`) stay as they are.
  - SQLite generated columns need SQLite 3.31 or later, which the implementer confirms for the embedded version.
- **PostgreSQL:**
  - Generated stored columns need version 12 or later, and the schema already uses them.
  - `INCLUDE` indexes need version 11 or later.
  - The docs name no minimum version. `deploy.md:50` and CI run Chainguard Postgres 18. The brief proposes stating "12 or later" in `docs/ops/deploy.md`.
  - The recommended design uses no extension and needs only table ownership (Eric's decision 2).
- **CI:**
  - The new Postgres tests run in `make verify` and `ci.yml` through testcontainers, or through `AGENTBUS2_TEST_POSTGRES_DSN`.
  - The bloat recipe needs only table ownership, which the test role has.
  - `windows.yml` excludes `internal/conformance` (`windows.yml:30`), so on Windows only the unit tests run (maintenance, cli, tui and remote converters).

## 7. Implementation outline (provisional; assumes the recommended options)

1. **Store schema and per-row size.**
   - Files: both `0001_initial.sql` files, edited in place under the pre-release rule.
     - Both get `size_bytes` generated from the per-table formula on `messages` and on every other table the sweep can delete from (section 4.11): at least `bridge_messages`, `enrollment_tokens` and the rows a deleted message or channel cascades to.
     - Postgres also gets `messages_size (seq) INCLUDE (size_bytes)`.
   - `internal/store/messages.go`: `SeqChannel.Bytes`; the `OrdinaryMessagesBefore` contract; `ExpiredTombstones` returns `{Seq, ChannelID, Bytes}` instead of bare seqs. New Store queries, each returning sizes: the orphaned `bridge_messages` rows (step 1), expired `enrollment_tokens` (step 2) and the idle task channels and idle memories (step 3; their shape waits on decision 20). `internal/store/sqlstore/messages.go:404-429` selects `size_bytes`. The tombstone purge job adapts to the new return type.
   - Tests first, in the Store suite (`internal/conformance/store`):
     - a row with multibyte text has the same `size_bytes` on both engines, equal to the hand-computed value;
     - every sweep query returns sizes;
     - a schema test lists the tables any eviction step can delete from and fails when one has no `size_bytes` (section 4.11).
   - Gates: `make verify`, Store suite on both engines.
2. **`Store.Space` per engine.**
   - Files: `internal/store/space.go` (`Usage{Bytes, DiskBytes, Logical}`, without `Evictable`), `internal/store/postgres/space.go` (`Bytes` is the sum of `size_bytes` over the tables of section 4.11, `DiskBytes` is today's relation sum, `Logical: true`) and `internal/store/sqlite/space.go` (`DiskBytes` is `page_count * page_size`, the whole file).
   - Tests first:
     - `TestUsageGrowsWithData` is extended.
     - A new `TestUsageIgnoresBloat` on Postgres: disable autovacuum on `messages` through `h.dsn`; fill; delete; then `Bytes` falls by exactly the deleted sizes and `DiskBytes` does not fall; `VACUUM messages`, after which `Bytes` is unchanged.
     - `TestReclaimRunsOutsideTransactions` stays.
     - A benchmark at about 2 million rows, with `EXPLAIN` confirming the index-only plan.
   - Gates: `make verify`.
3. **Settings.**
   - Files: `internal/core/config.go` (`PostgresBudgetMiB` and `IdleWindowHours` (`idle_window_hours`, default 168), default and bounds; the floor constants beside the bounds they equal), `internal/server/config/config.go`, its `Core()` mapping, and `docs/ops/configuration.md` (a new row; fix line 85; a paragraph on the retention model).
   - Tests: `TestConfigRejectsOutOfRange`, `TestValidateRejects` and the reflective `TestDefaultMatchesCore`, `TestSettingsListsEveryJSONName` and `docs_test.go`.
4. **The backstop in `core/maintenance`.**
   - Files: `internal/core/maintenance/budget.go`:
     - `budget()` chooses by `Store.Engine()`;
     - a pure `tier` function (section 4.2 option B);
     - the sweep, in Eric's order (section 4.3): step 1 orphaned `bridge_messages`, step 2 expired data (tombstones above the tombstone floor, expired `enrollment_tokens`), step 3 idle data (idle task channels and idle memories, window `idle_window_hours`, memories deleted outright), step 4 ordinary messages above the message floor; the allowance, the `Logical` branch, the final read and the warning;
     - `capacityNotice` without the `Evictable` early return, reusing the sweep's final read, with the two texts of section 4.8;
     - the per-sweep log line.
   - `jobs.go`: `deleteOrdinaryChunk` and a tombstone chunk function return the bytes they deleted and accept a cutoff (the floor). `service.go`: the job names "budget eviction" and "capacity notices" stay, so `TestTickRunsBudgetJobsInOrder` is unchanged; a test seam for `Usage` if needed, like `jobs`.
   - The seam between the two jobs: jobs are independent `func(ctx, now) (int, error)` on a value receiver, so the sweep's outcome (its final `Usage` and which floors it reached) travels in a per-tick value on the context, set up by `Tick` the way the deadline is (`deadlineKey`, `service.go:136`). The notice job uses that value when it is present. Run alone (`RunJob("capacity notices")`), it reads `Usage` itself and decides "eligible rows left" with two cheap existence queries (an ordinary message older than the message floor, a tombstone older than the tombstone floor), then computes the tier with the same pure function.
   - Tests first (unit, Docker-free, so they run on Windows):
     - a table test of `tier` across all five tiers and the boundaries (`target`, `budget`, every step at its limit);
     - a measure that never falls: the sweep deletes at most the allowance plus one chunk, and logs the warning;
     - floors: over budget with every ordinary message younger than one hour deletes nothing and reports exhausted; tombstones younger than 72 hours are kept;
     - order: orphaned rows go before expired rows, expired before idle, idle before ordinary messages; a step is not taken while the target is met;
     - an active task channel (any activity within the idle window) is never deleted, at any tier; registry, session and bridge rows are never deleted;
     - expired `enrollment_tokens` and orphaned `bridge_messages` are deleted under pressure and kept otherwise.
   - Gates: `make verify`, `scripts/m7-exit-checks.sh`.
5. **Conformance on both engines and both transports.**
   - Rewrite `TestMaintenanceBudgetEviction` so that the hysteresis, sweep, floor and every-step cases run on both engines.
   - Rewrite `TestMaintenanceCapacityNotice`: the notice is set on both engines in the over-budget and exhausted cases, carries the right text and the engine's setting, and is never set in the approaching band.
   - Add `TestMaintenanceBudgetEvictionUnderBloat` (Postgres; section 8).
   - The bus suite already runs under `local` and `inproc` (`make test-conformance`).
   - Gates: `make verify`; `scripts/m3-exit-counts.sh` and `m4-exit-checks.sh` counts updated if the leaf counts change.
6. **Wire.**
   - `api/openapi.yaml`: `Usage.disk_bytes`, `disk_ratio`, `tier`, the `evictable` description, and the health `storage` member. `info.version` and `version.Protocol` stay `1.0.0` (Eric's decision 2026-10-27), then run `make generate`.
   - Converters: `internal/server/admin_usage.go`, `internal/server/convert.go`, `internal/bus/admin.go`, `internal/bus/remote/usage.go` and `convert.go`, `internal/mcpserver/server.go:275,321`, `internal/cli/status.go:109-117` (print the disk figure, the ratio and the tier), the TUI health view, and `internal/core/health` (exhausted marks degraded, per decision 7).
   - Tests: `usage_test.go`, `health_test.go` (bus, remote and core), `status_test.go` and the TUI `health_test.go`.
   - Gates: `make verify` (`generate-check`).
7. **Gate self-test.**
   - Add a block to `scripts/check-gates-selftest.sh`. It reverts the Postgres `Bytes` to the relation sum, and `TestMaintenanceBudgetEvictionUnderBloat` must then fail. This proves the test catches the runaway. A second block removes the message floor, and the floor test must fail.
   - Gate: `make check-gates-selftest`.
8. **Docs and ADRs.**
   - ADR 0004: a dated addendum that records Eric's decisions of 2026-10-27 (the three earlier ones, the eviction order, the floors, and the logical measure over every deletable table), each labeled as Eric's decision, and supersedes choice 4's Postgres half, choice 5's `Evictable` gate and Gate 0 item 2.
   - Master spec lines 768-775.
   - `docs/ops/configuration.md`: the retention model (age first, budget as backstop, the tiers, the floors, what is never deleted). `docs/ops/health.md:55`, `admin-bootstrap.md:106`, and `deploy.md`: the disk ratio and how to size a budget from a disk limit, equal budgets on every replica, and the minimum Postgres version.
   - The comments at `internal/bus/admin.go:18-19`, `internal/store/space.go:5-8` and `internal/core/config.go:63-66`.

**New import edges:** none. `internal/core/maintenance` already imports `internal/store` (`budget.go:8`), and `Store.Engine()` is part of the Store interface (`internal/store/store.go:15`). The Milestone 4, 6 and 7 import rules are untouched.

## 8. Risks and test strategy

**Reproducing bloat deterministically.** (The recipe uses `messages`; the same recipe on `bridge_messages` and `enrollment_tokens` covers the wider measure of section 4.11.) The probe verified the recipe, which needs table ownership only (Eric's decision 2), so it works under testcontainers and under `AGENTBUS2_TEST_POSTGRES_DSN` with a role that is not a superuser. On the test schema, through `h.dsn`:

1. Run `ALTER TABLE messages SET (autovacuum_enabled = false, toast.autovacuum_enabled = false)`.
2. Append 2500 ordinary messages of 1 KiB, older than the message floor on the fake clock, and 5 memories.
3. Record `DiskBytes` as P0 and `Bytes` as L0, and set the budget to L0 / 2.
4. Run `RunJob("budget eviction")`. It deletes more than 1000 and fewer than 2500 rows; `Bytes` ends at or below the target; `DiskBytes` is at least P0 (the bloat is present); the survivors are the newest contiguous range; `evicted_before_seq` is right; the memories are untouched.
5. Run the job three more times. Each returns 0. **This is the runaway regression**, which the old physical measure would fail.
6. Run `VACUUM messages` outside a transaction, then the job again. It returns 0, `Bytes` is unchanged, and `DiskBytes` has not fallen.
7. Run `Tick` across several fake-clock intervals. The total deleted does not change.
8. **Physical size never drives deletion (Eric's decision 1).** With `Bytes` under the budget and `DiskBytes` far above it, a run deletes nothing, sets no notice, and health reports the disk figure and the ratio.

The test asserts no dead-tuple counts, because page pruning made them unstable in the probe.

**Second run, overlapping runs, a failed run's leftovers (Definition of Done D).**

- Second run: every step above is idempotent. At or below the budget, a run deletes nothing. A run at the exhausted tier deletes nothing and leaves the notice as it was.
- Overlapping runs: the lock serializes them. Failover in the middle of a sweep is bounded by two allowances (section 4.15), and the floors hold in both. `TestMaintenanceLockExclusiveAcrossCores` stays as it is.
- A failed run's leftovers: each chunk commits its delete and its `evicted_before_seq` together, so a killed sweep leaves a consistent prefix. With no counter (section 4.10 option A) there is no state to repair, and the next tick re-measures exactly. The tier is recomputed every tick, so no tier state survives a failure.

**Risks.**

- **Disk can still fill.** Enforcement is logical (Eric's decision 1), and the ratio moved from 1.85 to 3.02 in the probe after a vacuum. The deploy docs tell operators to size the budget from the disk limit with the reported ratio and headroom. Health's ratio is the early signal.
- **SQLite behavior changes** under section 4.5 option A: a burst younger than one hour now survives over budget, and the notice says so. Today's assertion `usage <= target after one run` in `TestMaintenanceBudgetEviction` stays for data older than the floor, and guards the overhead constant.
- **Idle-data deletion (step 3) loses live work.** Deleting an idle task channel or memory is the only step that removes data an agent may still want. The idle definition and window (decision 20) are the main risk; the active-channel rule (any activity within the window is never deleted) is the guard and has its own test.
- **Tombstone purge under pressure** loses memory revision history sooner and can make a task list look inactive sooner (`ChannelLastActivity`). The 72-hour floor bounds both, and with the default `tombstone_min_hours` of 72 the hard tier deletes nothing extra.
- **Cost.** The index-only sum must be cheap enough at the default budget: two reads per tick (section 4.7) plus one per health poll. If the benchmark of task 2 is too slow, section 4.10 option B is the fallback, together with its append-lock requirement.
- **Concurrency.** `Usage` runs outside a transaction (`postgres/space.go:16`), so it cannot cause serialization failures. The chunk transactions are those of retention today, so their conflict profile is unchanged.
- **Windows CI.** Nothing Postgres-specific runs there. The schema change touches SQLite, so the Windows unit tests that open SQLite files run against the new column, and the tier and floor unit tests run there. A red `windows.yml` is fixed first.
- **Local data.** Under the pre-release rule both schemas change in place, so local databases are recreated, not migrated.

## 9. Size estimate

**L, at the upper end.** It touches both engine schemas (`size_bytes` on every table eviction can delete from), the Store contract (`Usage`, query results, and new queries for orphaned rows, expired tokens and idle data), the maintenance core (a tiered sweep in Eric's four-step order with floors, and a new notice model), a setting, the wire (an OpenAPI change and `make generate`), health and four client surfaces, the conformance suites on both engines, two gate self-test blocks, the operations docs on the retention model, and an ADR addendum that records Eric's decisions and supersedes a Gate 0 decision. Eric's order and the wider measure push it to the top of L; the last-read marker for task channels (an open note) is the part most likely to grow. It does not change the age jobs (section 4.2 option B). Option A of that section would also change every age job and add threshold settings, which would make it XL.

## Appendix: probe recipe (for re-running section 3.2)

Container, as `internal/dbtest/postgres.go:55-62` starts it:

```sh
docker run -d --name ab2-pgprobe -e POSTGRES_USER=agentbus2 -e POSTGRES_PASSWORD=agentbus2 -e POSTGRES_DB=agentbus2 \
  --entrypoint /usr/bin/docker-entrypoint.sh \
  'cgr.dev/chainguard/postgres:latest@sha256:0926ac7262a2c1963179eb939cd5d610e1be3ecca90b4b5cad8db8df16fab87e' postgres
docker exec -i ab2-pgprobe psql -h 127.0.0.1 -U agentbus2 -d agentbus2 -At -f - < probe.sql
docker rm -f ab2-pgprobe
```

Bloat and measures (abridged `bloat.sql`):

```sql
CREATE SCHEMA b_probe; SET search_path = b_probe;
CREATE TABLE channels (id BIGINT PRIMARY KEY);
CREATE TABLE messages (seq BIGINT PRIMARY KEY,
  channel_id BIGINT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
  sender TEXT NOT NULL, created_at BIGINT NOT NULL, subject TEXT NOT NULL DEFAULT '',
  content TEXT NOT NULL, metadata TEXT NULL, memory_id BIGINT NULL,
  fts_subject tsvector GENERATED ALWAYS AS (to_tsvector('simple', regexp_replace(subject, '[^[:alnum:]]+', ' ', 'g'))) STORED,
  fts_content tsvector GENERATED ALWAYS AS (to_tsvector('simple', regexp_replace(content, '[^[:alnum:]]+', ' ', 'g'))) STORED);
CREATE INDEX messages_channel_seq ON messages(channel_id, seq);
CREATE INDEX messages_created_at ON messages(created_at);
CREATE INDEX messages_fts_content ON messages USING GIN (fts_content);
ALTER TABLE messages SET (autovacuum_enabled = false, toast.autovacuum_enabled = false);
INSERT INTO channels VALUES (1), (2);
INSERT INTO messages (seq, channel_id, sender, created_at, content)
  SELECT g, 1, 'pat', g, repeat('x', 1024) FROM generate_series(1, 2500) g;
INSERT INTO messages (seq, channel_id, sender, created_at, content)
  SELECT 2500+g, 2, 'pat', 2500+g, (SELECT string_agg(md5((g*1000+i)::text), ' ') FROM generate_series(1, 31) i)
  FROM generate_series(1, 500) g;
-- measure: physical vs logical
SELECT (SELECT sum(pg_total_relation_size(c.oid)) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
         WHERE n.nspname = current_schema() AND c.relkind = 'r') AS total_rel,
       (SELECT sum(octet_length(content) + octet_length(subject) + coalesce(octet_length(metadata), 0)) FROM messages) AS logical;
DELETE FROM messages WHERE seq IN (SELECT seq FROM messages WHERE memory_id IS NULL ORDER BY seq LIMIT 1500);
SELECT pg_stat_force_next_flush();  -- PostgreSQL 15 or later
SELECT n_live_tup, n_dead_tup FROM pg_stat_user_tables WHERE schemaname = 'b_probe' AND relname = 'messages';
VACUUM b_probe.messages;
-- per-fork sizes (forks.sql): heap, fsm, vm, GIN
SELECT pg_relation_size('messages', 'main'), pg_relation_size('messages', 'fsm'),
       pg_relation_size('messages', 'vm'), pg_relation_size('messages_fts_content');
SELECT name, version, trusted, superuser FROM pg_available_extension_versions WHERE name = 'pgstattuple';
```

The `SERIALIZABLE` conflict (`ssi.sh`). Install statement-level `AFTER INSERT` and `AFTER DELETE` triggers with transition tables that add to and subtract from `space_counter(id, bytes)`, then run:

1. Session A: `BEGIN ISOLATION LEVEL SERIALIZABLE; SELECT count(*) FROM messages;` (this takes the snapshot).
2. Session B: `BEGIN ISOLATION LEVEL SERIALIZABLE; LOCK TABLE space_counter IN EXCLUSIVE MODE; INSERT INTO messages ...; COMMIT;`
3. Session A: `DELETE FROM messages WHERE seq IN (SELECT seq FROM messages ORDER BY seq LIMIT 10);`, which fails with `could not serialize access due to concurrent update` inside the trigger's `UPDATE space_counter`.
