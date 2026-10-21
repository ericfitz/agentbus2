# Milestone 2: Store and Engines Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The `Store` contract of spec section 5, implemented once in portable SQL over SQLite and Postgres, with `Tx` under the Decision 26 and 69 contract, `seq_counter`, `schema_meta` with the Decision 34 open rules, migrations under the Decision 68 lock, every aggregate's persistence (messages with tags, subjects, full text and embeddings; channels with ACL rows and creator; identities with the admin flag and public keys; subscriptions and tag subscriptions; sessions with leases, watch registration, cursors and the pending batch; memories with revisions; task lists; receipts; the maintenance lease; notices), and the Store conformance suite with the concurrent-writer test and the section 5.8 ACL cases, passing on both engines under `make verify`.

**Architecture:** `internal/store` is the contract: interfaces grouped by aggregate, domain types, typed errors and the input rules the Store enforces (`NameRule`, kinds, permissions). `internal/store/sqlstore` implements it once with `sqlx` and `?` placeholders rebound per engine, behind a small `Dialect` interface that holds the only engine-specific code: placeholders, the retryable-error test, the migration lock, the empty-database test, the text-search condition and the Milestone 7 append notification hook. `internal/store/sqlite` and `internal/store/postgres` each provide `Open`, the dialect and one embedded migration set numbered from version 1. The Store conformance suite in `internal/conformance/store` opens both engines through their own `Open` (never through `dbtest.Engine.Open`, which sets no pragmas) and isolates each Postgres test in its own schema through the `search_path` runtime parameter.

**Tech Stack:** Go 1.27, `CGO_ENABLED=0`, `database/sql`, `modernc.org/sqlite` v1.60.1 (FTS5 built in), `github.com/jackc/pgx/v5` v5.11.0 through `stdlib`, `github.com/jmoiron/sqlx` v1.4.0 (struct scanning and `Rebind`), `internal/dbtest` and testcontainers from Milestone 1, `postgres:17`.

**Spec:** `docs/superpowers/specs/2026-10-19-agentbus2-design.md` sections 4.4 to 4.7 (what the rows must hold), 5 (the Store), 8 (the suites), 9 (the porting map), 10 (Milestone 2); ADR 0004 (Decisions 2, 5, 6, 25, 26, 27, 30, 34, 49, 50, 51, 52, 53, 54, 68, 69), ADR 0008 (Decisions 80, 88, 90, 92, 93, 94, 95, 96), ADR 0003 (Decisions 16, 37, 40, 75, 79), ADR 0002 (Decisions 55, 57, 73), ADR 0005 (Decisions 29, 31, 32). v1 at `/Users/efitz/Projects/agentbus` (`v1.14.3-4-g8bb09e2`, `internal/bus/`).

## Task dependencies and parallelism

Tasks with no dependency between them run in parallel in separate worktrees and are cherry-picked onto `main`. Every task's `Files:` list is disjoint from every task it can run beside: the engine DDL lives in Tasks 2 and 3 only, each aggregate task deletes its own stub file and adds its own repository and conformance files, and `go.mod` is touched by Task 1 alone.

| Task | Depends on | Wave |
| --- | --- | --- |
| 1 Store contract and types | none | 1 |
| 2 `sqlstore` framework and SQLite engine | 1 | 2 |
| 3 Postgres engine and the engine conformance tests | 2 | 3 |
| 4 Identities, public keys, receipts | 3 | 4 |
| 5 Channels and ACL rows | 4 | 5 |
| 6 Messages, tags, history, retention queries, embeddings | 5 | 6 |
| 7 Sessions, cursors, pending batch, watch registration | 5 | 6 |
| 8 Subscriptions, tag subscriptions, acked marks, tag matches | 6 | 7 |
| 9 Memories, access, revisions, task-list queries | 6 | 7 |
| 10 Search: query parser and both text-search translations | 6 | 7 |
| 11 Integration, milestone exit, docs | 4, 5, 6, 7, 8, 9, 10 | 8 |

Waves: 1; 2; 3; 4; 5; then 6 and 7 in parallel; then 8, 9 and 10 in parallel; then 11. The chain 1 to 5 is forced by the foreign keys (every aggregate references identities and channels) and by the engines; from Task 6 on, the aggregates fan out.

## Global Constraints

- Module `github.com/ericfitz/agentbus2`, `go 1.27`, `CGO_ENABLED=0` everywhere (Decision 53). Libraries: `database/sql` with `modernc.org/sqlite` and `pgx` through its stdlib adapter, plus `sqlx`; no ORM, no sqlc (Decision 50). The native `pgx` connection for `LISTEN` is Milestone 7.
- Decision 26: `Store.Tx(ctx, fn)` is serializable, may run `fn` more than once, and `fn` must have no side effects. Decision 69: Postgres retries SQLSTATE 40001 a small fixed count with jittered backoff; SQLite relies on the immediate transaction and a 5 s busy timeout with no further retry; both then surface a retryable error. Decision 27: `seq` comes from `UPDATE seq_counter SET v = v + 1 ... RETURNING v` inside the append transaction.
- Decision 34: `schema_meta` (`app = 'agentbus2'`, `schema_version`, `engine`, `created_at`); on open, wrong or missing `app` on a non-empty database refuses, a newer version refuses with "upgrade agentbus2", an older version migrates under a lock, anything else rejects; SQLite also sets a distinct `PRAGMA application_id`. Decision 53: one migration set per engine, numbered from version 1. Decision 68: the SQLite lock is an immediate transaction re-reading `schema_version` before each step. Decision 29: no migration from v1 and none between engines.
- Decision 25: repository methods grouped by aggregate, portable SQL, dialect hooks only where the engines differ. Portability rules for every repository method: `?` placeholders rebound with `sqlx.Rebind` (no `?1` numbered parameters, no `$1`); `INSERT ... ON CONFLICT (...) DO NOTHING | DO UPDATE SET ...` (never `INSERT OR IGNORE` / `OR REPLACE`); `RETURNING` is fine on both; no `group_concat` (fetch tags with a second query), no `instr`, no `rowid`, no `max(a, b)` scalar (use `CASE WHEN`), no `LIMIT` inside `DELETE` (select the keys first, then `DELETE ... WHERE key IN (...)`), booleans bound as Go `bool` and compared with `= TRUE` / `= FALSE` (both engines accept the literals; SQLite stores 0/1), timestamps as `int64` milliseconds (v1 `nowMs`), blobs as `[]byte`.
- Decision 54: the Store conformance suite runs against both engines; Decision 28: `make verify` requires Postgres and a run that skips the Postgres suites is not a pass. Milestone 1's gate checks are in force: no `.Skip(` call under `internal/conformance` or `internal/dbtest`; `make test` is Docker-free and must pass with `AGENTBUS2_TEST_POSTGRES_DSN` pointed at an unreachable server, so every test that needs Postgres lives under `internal/conformance/store`, never beside the engine code; the suite opens engines through the Store's own `Open`, taking the file path or DSN from `dbtest.Engine.DSN(t)`.
- Unrecognized inputs are rejected with a test (project rule; spec section 4.6.1 elaboration): an unknown `Kind`, an unknown `Permission`, an empty permission list, a name failing `NameRule`, an unknown engine name in `schema_meta`, an unknown `Algorithm` on a public key, an unknown migration version.
- Decision 31: every porting commit names its v1 source file. Decision 32: nothing here reads or writes a v1 path. Work lands on `main`; nothing is pushed to origin in this milestone. Commit messages are Conventional Commits with the attribution trailer from the session's instructions.
- Shell beyond a one-liner is a script run with `bash` under `set -euo pipefail`; `sed -i` is never used (BSD/GNU difference).
- Definition of Done D applies to the migration runner and to the conformance helpers: each task's steps say what a second run, an overlapping run and a failed run's leftovers do.

## Choices this milestone makes (delegated by the spec, recorded with rationale)

The spec delegates two storage choices to Milestone 2; the rest below are plan-level choices about shape that re-decide nothing. Task 11 records choices 1 and 2 in an addendum to ADR 0004 and replaces the spec's "chosen in Milestone 2" placeholders.

1. **Postgres full-text equivalent of FTS5 (spec section 5.4, Decision 51).** A stored generated column `fts tsvector GENERATED ALWAYS AS (to_tsvector('simple', subject || ' ' || content)) STORED` with a GIN index, queried with `fts @@ (...)`. The `simple` configuration lowercases and does not stem, the closest match to FTS5's `unicode61` tokenizer (no stemming, no stop words), so "which messages match" agrees across engines for words, prefixes and phrases. Each query term is bound as its own parameter: a word through `plainto_tsquery('simple', ?)`, a prefix through `to_tsquery('simple', ?)` with the argument `'<text>':*` (the lexeme single-quoted, inner quotes doubled), a phrase through `phraseto_tsquery('simple', ?)`; terms are joined with the tsquery `&&` operator. Probed on `postgres:17.11` on 2026-10-20: `to_tsquery('simple', 'Commit')` is `'commit'`; `quote_literal('e159') || ':*'` is `'e159':*` and matches `abc e159e8c def`; `phraseto_tsquery('simple', 'rate limit')` is `'rate' <-> 'limit'`, matches `the rate limit hit` and not `the limit rate hit`; `plainto_tsquery('simple', 'commit:e159e8c')` is `'commit' & 'e159e8c'`; `to_tsvector('simple', 'foo-bar')` is `'bar':3 'foo':2 'foo-bar':1` (whole and parts, where FTS5 yields only the parts); `to_tsvector('simple', 'café Résumé')` keeps the accents (`unicode61` folds them); `plainto_tsquery('simple', '!!!')` is the empty query, which matches nothing. Rejected: `pg_trgm` / `ILIKE` (no phrase or token semantics, so matching would diverge from FTS5 on every word boundary) and a stemming configuration such as `english` (FTS5 does not stem, so `running` would match `run` on one engine only). Pgvector stays an optional capability for a later milestone; embeddings are `BYTEA` scored in Go (Decision 51).
2. **ACL permission storage (spec section 5.8, Decision 80).** Three boolean columns per stored entry: `channel_acl(id, channel_id, identity, can_read, can_write, can_admin)` with `CHECK (can_read OR can_write OR can_admin)`. One row is one stored entry, exactly as Decisions 80 and 88 speak of entries: the default ACL is three rows, "removing the stored entries for `admins`" is a `DELETE` by identity, `ACL(ctx, channel)` returns what `SetACL` stored in the order it was given, and the `CHECK` enforces the non-empty rule in the database as well as in Go. Several rows may name one identity; `EffectivePermissions` takes the union. The alternative, one row per `(channel, identity, permission)` with `CHECK (permission IN ('read', 'write', 'admin'))`, cannot represent "an entry" (two entries for one identity collapse into one set), so a stored ACL could not round-trip as given. Unknown permission values are rejected in Go before the row is built (the column names are the constraint); the conformance case for an unknown value therefore calls `SetACL` with `Permission("owner")`.
3. **Package layout.** `internal/store` (contract), `internal/store/sqlstore` (the one implementation), `internal/store/sqlite` and `internal/store/postgres` (engines: `Open`, dialect, embedded migrations). The dialect interface is unexported-by-convention (`sqlstore.Dialect`, exported so the engine packages implement it) and holds exactly: `Name`, `BindType` (for `sqlx.Rebind`), `IsRetryable(err)`, `IsEmpty(ctx, db)`, `Stamp(ctx, db)` and `CheckStamp(ctx, db)` (SQLite `application_id`; no-ops on Postgres), `LockMigrations(ctx, tx)`, `LockAppend(ctx, tx)`, `TextSearch(q)`, `NotifyAppend(ctx, tx, channel, seq)`.
4. **Three transaction kinds.** `Store.Tx` (serializable, retried), `Store.Append` (as `Tx`, and the only kind in which `NextSeq` and `AppendMessage` are allowed: on Postgres its first statement is `LOCK TABLE seq_counter IN EXCLUSIVE MODE`, so concurrent appends queue behind the lock instead of aborting with 40001 on the counter row; on SQLite the immediate transaction already queues them), `Store.Read` (a read-only snapshot, no retry). Probed on 2026-10-20 with 8 goroutines appending 50 messages each under `SERIALIZABLE`: without the lock 1,843 serialization failures and 49 appends that exhausted 20 attempts; with the lock as the first statement 0 failures and 400 of 400 appends. Decision 27 already accepts that the counter serializes every append; the lock only moves the serialization from "abort and retry" to "wait". `NextSeq` outside `Append` returns `ErrNotAppend`.
5. **Retry constants (Decision 69).** Postgres: `TxAttempts = 5`, backoff before attempt n (n from 1) a uniform random duration in `[0, 10ms * 2^(n-1))` capped at 200 ms; SQLSTATE `40001` and `40P01` (deadlock detected, the other serialization-class abort) are retryable. SQLite: one attempt, `SQLITE_BUSY` (5) and `SQLITE_LOCKED` (6) after the busy timeout are retryable. Both are then returned wrapped in `store.ErrRetryable`.
6. **Surrogate channel ids.** `channels.id` is the key every other table references, `channels.name` is `UNIQUE`; a rename (Decision 89) is one row update instead of v1's three (`channels.go:376`). Identities are keyed by name (the authentication subject).
7. **Delivery rows (Decisions 16, 37, 40, 75).** `subscriptions(identity, channel_id, acked_seq)` is identity-level and carries the acked mark; `session_cursors(session_id, channel_id, cursor_seq, pending_end_seq)` is the per-session delivery state; `sessions.pending_token` is the one pending batch per session (v1 keeps the token per subscription row, `schema.go` `subscriptions.pending_token`); tag-match delivery has one cursor per session (`sessions.tags_cursor_seq`, `tags_pending_end_seq`) and one acked mark per identity (`identities.tags_acked_seq`), v1's `tags/` pseudo-channel row (`tags.go:216`) re-keyed.
8. **Tasks are memory rows**, as in v1 (`tasks.go:168` `loadTasks` reads `messages WHERE memory_id IS NOT NULL` on the task channel; leases live in the task JSON, `Task.LeasedUntil`). No `tasks` table; the Store adds the stored kind `tasks` (Decision 90), `LiveMemories(channel)` and `ChannelLastActivity(channel)` (v1 `taskLastActivitySQL`).
9. **The default ACL is substituted by the core**, not the Store (`Milestone 3`): the Store stores exactly the entries it is given; `store.DefaultACL(creator)` returns the Decision 88 literal with the creator substituted (and without the creator entry when `creator == ""`, Decision 92), so the section 5.8 "default rows exist after a create without an ACL" case passes `store.DefaultACL(creator)` and the "absent after a create with one" case passes its own list.
10. **Per-test Postgres isolation** is a schema per test (`t_<16 hex>`), created on `dbtest.Engine.Open(t)`'s connection and selected through `&search_path=<schema>` on the DSN (pgx treats unknown DSN keys as runtime parameters; probed: `current_schema()` returns the schema). The migration lock is `pg_advisory_xact_lock(hashtext(current_schema()))`, so parallel schemas on one server never share a lock key.
11. **What is not here.** Rate-limit buckets stay in memory in the core (v1 `ratelimit.go`); enrollment tokens and external-issuer mappings are Milestone 5 rows (a version 2 migration); `pg_notify` and `pg_try_advisory_lock` are Milestone 7, which fills the `NotifyAppend` hook and may replace `TryLease` on Postgres; the admin `Reset` (v1 `meta.reset_generation`) is Milestone 3 or 5 and `meta` is not ported.

## Claude proposal, pending Eric

- `everyone` and `admins` are reserved: `CreateIdentity` refuses them, because an ACL entry naming either must mean the special identity (Decision 80) and `NameRule` would otherwise allow a registered identity of that name. Elaboration Decision 80 forces; not in the spec text.
- `40P01` (deadlock detected) is treated as retryable alongside `40001`; Decision 69 names only 40001.
- The retry constants of choice 5 (five attempts, 10 ms doubling with jitter, 200 ms cap).
- v1 `meta` (`reset_generation`, v1 ADR 0022) is not ported; v2's admin reset will be designed with the admin section (Milestone 5). v1 `notices` is ported as is (capacity and embedding notices surface through `receive` in v1; where v2 surfaces them is Milestone 3's).
- Rate-limit buckets stay in memory per server process (choice 11). Two Milestone 7 replicas would therefore each grant the full per-identity rate; a shared bucket row would need a store write on every send.

## Review Focus

1. A query term with no letter or digit (`!!!`, `*`, `"  "`): `search.Parse` drops it (v1 drops star-only words, `search.go:63`); a query that is empty after parsing is rejected by `SearchText` with `ErrInvalid`, never sent to the engine, where FTS5 would raise a syntax error and Postgres would match nothing with a NOTICE. (Tests in Task 10.)
2. A message whose `content` is a diacritic word (`café`) searched as `cafe`: FTS5 `unicode61` folds, Postgres `simple` does not. The corpus test searches `café` and expects a match on both; the plan promises no diacritic folding. (Task 10 documents it in the test.)
3. `AppendMessage` or `NextSeq` called inside `Store.Tx` rather than `Store.Append`: the Postgres path would then take the counter row under `SERIALIZABLE` without the table lock and fail with 40001 under contention; `ErrNotAppend` is returned instead, on both engines. (Test in Task 2, both engines in Task 3.)
4. A second process opening an empty database at the same moment: both must succeed and the migration must run once (bootstrap `CREATE TABLE IF NOT EXISTS schema_meta` under the lock, `INSERT ... ON CONFLICT DO NOTHING`). (Test in Task 3.)
5. A serialization failure surfacing from `Commit`, not from `fn`: the retry loop classifies the commit error too, or a conflict detected at commit would be returned unwrapped and the client would not know to retry. (Test in Task 3, injected and real.)
6. `SetACL` with an entry naming a `/`-containing or unregistered identity, or an entry with `Permissions: nil`: rejected before any row is written, and the previous ACL is intact (the replace runs after validation, in the same transaction). (Tests in Task 5.)
7. `DeleteChannel` while sessions hold cursors and messages hold tags and embeddings: every dependent row goes with the channel (cascades) and `ReadableChannels` no longer lists it. (Test in Task 5 for ACL rows, Tasks 6 and 7 for messages and cursors.)

---

## Schema, version 1

The DDL is the cross-task contract: Task 2 ships the SQLite set, Task 3 the Postgres set, byte for byte as below (the aggregate tasks add Go, not DDL; a gap found by an aggregate task is reported to the integrator, who fixes both files in a serial commit). `schema_meta` is created by the bootstrap step, not by version 1. Every timestamp is `int64` milliseconds. v1 reference: `internal/bus/schema.go`.

Disposition of the v1 tables (`schema.go`, 13 tables plus `messages_fts`):

| v1 table | v2 | Why |
| --- | --- | --- |
| `channels` | ported: `id` surrogate, `creator`, kind `tasks`; `created_seq`, `evicted_before_seq`, `created_at` kept | Decisions 80, 90; choice 6 |
| `sessions` | re-keyed: `id`, `identity`, `display_name`, `hint`, `resume_secret`, lease, `closed_at`, pending token, tag cursor, `watch_id`; `harness*` columns dropped | Decisions 14, 15, 39, 40, 41, 68, 78 (harness-derived identity not ported) |
| `subscriptions` | identity-level with `acked_seq`; cursors move to `session_cursors` | Decisions 16, 37, 75 |
| `messages` | ported: `channel_id`, `session_id` replaces `context`, `tombstone` boolean | Decision 25; choice 6 |
| `messages_fts` | SQLite: ported with its two triggers; Postgres: `messages.fts` generated column | Decision 51; choice 1 |
| `embeddings`, `embed_failures` | ported | Decision 51; v1 ADR 0018 |
| `message_tags` | ported | v1 ADR 0009 |
| `tag_subscriptions`, `tag_subscription_tags` | ported, keyed by identity | Decisions 37, 38 |
| `receipts` | ported, keyed by identity | Decision 67 |
| `leases` | ported, with the `maintenance` row | Decisions 49, 68 |
| `meta` | not ported | pending Eric (above) |
| `notices` | ported | pending Eric (above) |
| `memory_access` | ported | v1 ADR 0013 |
| new | `schema_meta`, `seq_counter`, `identities`, `identity_keys`, `channel_acl`, `session_cursors` | Decisions 27, 34, 57, 73, 80, 16 |

### SQLite (`internal/store/sqlite/schema/0001_initial.sql`)

```sql
CREATE TABLE seq_counter (id INTEGER PRIMARY KEY CHECK (id = 1), v INTEGER NOT NULL);
INSERT INTO seq_counter (id, v) VALUES (1, 0);
CREATE TABLE leases (name TEXT PRIMARY KEY, owner TEXT NOT NULL, expires_at INTEGER NOT NULL);
INSERT INTO leases (name, owner, expires_at) VALUES ('maintenance', '', 0);
CREATE TABLE notices (kind TEXT PRIMARY KEY, message TEXT NOT NULL, set_at INTEGER NOT NULL);
CREATE TABLE identities (
  name TEXT PRIMARY KEY,
  is_admin INTEGER NOT NULL DEFAULT 0 CHECK (is_admin IN (0, 1)),
  tags_acked_seq INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL
);
CREATE TABLE identity_keys (
  identity TEXT NOT NULL REFERENCES identities(name) ON DELETE CASCADE,
  kid TEXT NOT NULL,
  algorithm TEXT NOT NULL CHECK (algorithm IN ('EdDSA', 'ES256')),
  public_key BLOB NOT NULL,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (identity, kid)
);
CREATE TABLE channels (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL UNIQUE,
  kind TEXT NOT NULL CHECK (kind IN ('ordinary', 'memory', 'tasks')),
  creator TEXT NULL,
  created_seq INTEGER NOT NULL,
  evicted_before_seq INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL
);
CREATE TABLE channel_acl (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  channel_id INTEGER NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
  identity TEXT NOT NULL,
  can_read INTEGER NOT NULL CHECK (can_read IN (0, 1)),
  can_write INTEGER NOT NULL CHECK (can_write IN (0, 1)),
  can_admin INTEGER NOT NULL CHECK (can_admin IN (0, 1)),
  CHECK (can_read = 1 OR can_write = 1 OR can_admin = 1)
);
CREATE INDEX channel_acl_channel_identity ON channel_acl(channel_id, identity);
CREATE TABLE subscriptions (
  identity TEXT NOT NULL REFERENCES identities(name) ON DELETE CASCADE,
  channel_id INTEGER NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
  acked_seq INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (identity, channel_id)
);
CREATE TABLE tag_subscriptions (
  identity TEXT NOT NULL REFERENCES identities(name) ON DELETE CASCADE,
  tags_key TEXT NOT NULL,
  created_seq INTEGER NOT NULL,
  PRIMARY KEY (identity, tags_key)
);
CREATE TABLE tag_subscription_tags (
  identity TEXT NOT NULL,
  tags_key TEXT NOT NULL,
  tag TEXT NOT NULL,
  lo TEXT NOT NULL,
  hi TEXT NOT NULL,
  PRIMARY KEY (identity, tags_key, tag),
  FOREIGN KEY (identity, tags_key) REFERENCES tag_subscriptions(identity, tags_key) ON DELETE CASCADE
);
CREATE TABLE sessions (
  id TEXT PRIMARY KEY,
  identity TEXT NOT NULL REFERENCES identities(name) ON DELETE CASCADE,
  display_name TEXT NOT NULL,
  hint TEXT NOT NULL DEFAULT '',
  resume_secret TEXT NOT NULL UNIQUE,
  opened_at INTEGER NOT NULL,
  lease_expires_at INTEGER NOT NULL,
  closed_at INTEGER NOT NULL DEFAULT 0,
  pending_token TEXT NOT NULL DEFAULT '',
  tags_cursor_seq INTEGER NOT NULL DEFAULT 0,
  tags_pending_end_seq INTEGER NOT NULL DEFAULT 0,
  watch_id TEXT NOT NULL DEFAULT ''
);
CREATE INDEX sessions_identity ON sessions(identity);
CREATE INDEX sessions_lease ON sessions(lease_expires_at);
CREATE TABLE session_cursors (
  session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  channel_id INTEGER NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
  cursor_seq INTEGER NOT NULL,
  pending_end_seq INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (session_id, channel_id)
);
CREATE TABLE messages (
  seq INTEGER PRIMARY KEY,
  channel_id INTEGER NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
  sender TEXT NOT NULL,
  session_id TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  type TEXT NOT NULL DEFAULT '',
  subject TEXT NOT NULL DEFAULT '',
  content TEXT NOT NULL,
  reply_to INTEGER NULL,
  metadata TEXT NULL,
  memory_id INTEGER NULL,
  revision INTEGER NULL,
  tombstone INTEGER NOT NULL DEFAULT 0 CHECK (tombstone IN (0, 1)),
  tombstone_at INTEGER NULL
);
CREATE INDEX messages_channel_seq ON messages(channel_id, seq);
CREATE INDEX messages_memory ON messages(memory_id, tombstone);
CREATE INDEX messages_tombstone_at ON messages(tombstone_at);
CREATE INDEX messages_created_at ON messages(created_at);
CREATE VIRTUAL TABLE messages_fts USING fts5(subject, content, content='messages', content_rowid='seq');
CREATE TRIGGER messages_ai AFTER INSERT ON messages BEGIN
  INSERT INTO messages_fts(rowid, subject, content) VALUES (new.seq, new.subject, new.content);
END;
CREATE TRIGGER messages_ad AFTER DELETE ON messages BEGIN
  INSERT INTO messages_fts(messages_fts, rowid, subject, content) VALUES ('delete', old.seq, old.subject, old.content);
END;
CREATE TABLE message_tags (
  seq INTEGER NOT NULL REFERENCES messages(seq) ON DELETE CASCADE,
  tag TEXT NOT NULL,
  PRIMARY KEY (seq, tag)
);
CREATE INDEX message_tags_tag_seq ON message_tags(tag, seq);
CREATE TABLE embeddings (
  seq INTEGER PRIMARY KEY REFERENCES messages(seq) ON DELETE CASCADE,
  model TEXT NOT NULL,
  vector BLOB NOT NULL
);
CREATE TABLE embed_failures (
  seq INTEGER NOT NULL REFERENCES messages(seq) ON DELETE CASCADE,
  model TEXT NOT NULL,
  error TEXT NOT NULL,
  failed_at INTEGER NOT NULL,
  PRIMARY KEY (seq, model)
);
CREATE TABLE memory_access (memory_id INTEGER PRIMARY KEY, accessed_at INTEGER NOT NULL);
CREATE TABLE receipts (
  identity TEXT NOT NULL,
  key TEXT NOT NULL,
  fingerprint TEXT NOT NULL,
  result TEXT NOT NULL,
  expires_at INTEGER NOT NULL,
  PRIMARY KEY (identity, key)
);
CREATE INDEX receipts_expires ON receipts(expires_at);
```

`schema_meta` (bootstrap, both engines, created by the framework before version 1 runs): `CREATE TABLE IF NOT EXISTS schema_meta (id INTEGER PRIMARY KEY CHECK (id = 1), app TEXT NOT NULL, schema_version INTEGER NOT NULL, engine TEXT NOT NULL, created_at INTEGER NOT NULL)` and `INSERT INTO schema_meta (id, app, schema_version, engine, created_at) VALUES (1, 'agentbus2', 0, <engine>, <now>) ON CONFLICT (id) DO NOTHING` (Postgres: `id INTEGER PRIMARY KEY CHECK (id = 1)` likewise; `schema_version BIGINT`).

### Postgres (`internal/store/postgres/schema/0001_initial.sql`)

The same tables with these substitutions and nothing else: `INTEGER` columns become `BIGINT` except `schema_meta.id`, `seq_counter.id`; `INTEGER PRIMARY KEY AUTOINCREMENT` becomes `BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY`; the `INTEGER ... CHECK (x IN (0, 1))` booleans (`is_admin`, `can_read`, `can_write`, `can_admin`, `tombstone`) become `BOOLEAN NOT NULL` (with their `DEFAULT FALSE` where SQLite has `DEFAULT 0`) and the ACL check becomes `CHECK (can_read OR can_write OR can_admin)`; `BLOB` becomes `BYTEA`; `messages` gains `fts tsvector GENERATED ALWAYS AS (to_tsvector('simple', subject || ' ' || content)) STORED` and `CREATE INDEX messages_fts ON messages USING GIN (fts)` in place of the virtual table and its triggers; `TEXT NULL` and `INTEGER NULL` keep their meaning. `messages.seq BIGINT PRIMARY KEY` (no identity: the value comes from `seq_counter`).

---
### Task 1: Store contract, domain types, errors, input rules

**Depends on:** none.

**Files:**
- Create: `internal/store/store.go` (`Store`, `Tx`, `Engine`), `internal/store/errors.go`, `internal/store/meta.go`, `internal/store/identities.go`, `internal/store/channels.go`, `internal/store/subscriptions.go`, `internal/store/sessions.go`, `internal/store/messages.go`, `internal/store/memories.go`, `internal/store/receipts.go` (each: that aggregate's interface and types), `internal/store/names.go` (`NameRule`, `ValidKind`), `internal/store/acl.go` (permissions, entry validation, `DefaultACL`, `Permissions.Union`), `internal/store/tags.go` (`TagRange`), `internal/store/doc.go`
- Create: `internal/search/query.go` (`Query`, `Term`; `Parse` is Task 10)
- Create: `internal/store/sqlstore/doc.go` (package comment and a blank import of `sqlx`, so `go.mod` is settled here and Task 2 only adds files to the package)
- Test: `internal/store/names_test.go`, `internal/store/acl_test.go`, `internal/store/tags_test.go`
- Modify: `go.mod`, `go.sum` (`github.com/jmoiron/sqlx` v1.4.0)

**Port from v1:** `internal/bus/sessions.go:87-100` (`NameRule`: 1 to 128 bytes, no `/`, no control characters; the `/` ban is what Decision 93 relies on), `internal/bus/tags.go:93-99` (`tagRange`: an exact tag maps to `[tag, tag]`, a prefix `p*` to `[p, p~]`). Not ported: `ChannelNameRule`, `PrefixKind`, `resolveKind` (`channels.go`), `IsTaskChannel`, `TaskPrefix` (`tasks.go`), `dm/` handling (Decisions 13, 76, 82).

**Interfaces (produces; every later task consumes these exact names):**

```go
package store

type Engine string
const (EngineSQLite Engine = "sqlite"; EnginePostgres Engine = "postgres")

type Store interface {
	Engine() Engine
	// Tx: serializable; fn may run more than once and must have no side effects (Decision 26).
	Tx(ctx context.Context, fn func(ctx context.Context, tx Tx) error) error
	// Append: as Tx, and the only kind in which NextSeq and AppendMessage work (choice 4).
	Append(ctx context.Context, fn func(ctx context.Context, tx Tx) error) error
	// Read: a read-only snapshot; a write inside it fails; never retried.
	Read(ctx context.Context, fn func(ctx context.Context, tx Tx) error) error
	Close() error
}

type Tx interface { Meta; Identities; Receipts; Channels; Subscriptions; Sessions; Messages; Memories; Tasks }

// errors.go
var (
	ErrNotFound      = errors.New("store: not found")
	ErrConflict      = errors.New("store: already exists")
	ErrInvalid       = errors.New("store: invalid input")          // always wrapped with the reason: fmt.Errorf("%w: channel name must not contain '/'", ErrInvalid)
	ErrRetryable     = errors.New("store: transaction could not complete, retry") // Decision 69
	ErrNotAppend     = errors.New("store: NextSeq and AppendMessage need Store.Append")
	ErrReadOnly      = errors.New("store: write inside Store.Read")
	ErrNotAgentbus2  = errors.New("store: not an agentbus2 database")          // Decision 34: wrong or missing app, wrong application_id
	ErrUpgradeNeeded = errors.New("store: database schema is newer than this binary: upgrade agentbus2") // Decision 34
	ErrSchemaInvalid = errors.New("store: schema_meta is invalid")             // Decision 34 "anything else"
)

// meta.go
type Meta interface {
	NextSeq(ctx) (int64, error)                                              // Append only (ErrNotAppend otherwise)
	MaxSeq(ctx) (int64, error)                                               // seq_counter.v
	TryLease(ctx, name, owner string, now, until int64) (bool, error)        // v1 takeLease: UPDATE ... WHERE name=? AND (expires_at < now OR owner = owner)
	SetNotice(ctx, n Notice) error; ClearNotice(ctx, kind string) error; Notice(ctx, kind string) (Notice, bool, error)
}
type Notice struct { Kind, Message string; SetAt int64 }

// identities.go
type Identity struct { Name string; Admin bool; TagsAckedSeq int64; CreatedAt int64 }
type PublicKey struct { Identity, KID, Algorithm string; Key []byte; CreatedAt int64 } // Algorithm: "EdDSA" | "ES256" (Decision 55); Key: PKIX DER
const (Everyone = "everyone"; Admins = "admins")                                      // special identities (Decision 80); reserved as names (pending Eric)
type Identities interface {
	CreateIdentity(ctx, name string, now int64) error        // ErrInvalid (NameRule, reserved), ErrConflict
	Identity(ctx, name string) (Identity, error)             // ErrNotFound
	ListIdentities(ctx) ([]Identity, error)                  // by name
	SetAdmin(ctx, name string, admin bool) error             // ErrNotFound
	AdvanceTagAckedMark(ctx, name string, seq int64) error   // max(current, seq)
	PutPublicKey(ctx, k PublicKey) error                     // upsert by (identity, kid); ErrInvalid on unknown Algorithm; ErrNotFound on unknown identity
	PublicKeys(ctx, name string) ([]PublicKey, error)        // by kid
	DeletePublicKeys(ctx, name string) (int, error)          // reset-credential (Decision 73)
}

// receipts.go
type Receipt struct { Identity, Key, Fingerprint string; Result []byte; ExpiresAt int64 }
type Receipts interface {
	Receipt(ctx, identity, key string, now int64) (Receipt, bool, error) // unexpired only (expires_at > now)
	PutReceipt(ctx, r Receipt) error                                     // replace
	DeleteExpiredReceipts(ctx, now int64, limit int) (int, error)
}

// channels.go
type Kind string
const (KindOrdinary Kind = "ordinary"; KindMemory Kind = "memory"; KindTasks Kind = "tasks")
type Channel struct { ID int64; Name string; Kind Kind; Creator string; CreatedSeq, EvictedBeforeSeq, CreatedAt int64 } // Creator "" = none (Decision 92)
type ChannelCreate struct { Name string; Kind Kind; Creator string; ACL []ACLEntry; CreatedSeq, CreatedAt int64 }          // ACL stored as given (choice 9)
type Channels interface {
	CreateChannel(ctx, c ChannelCreate) (Channel, error)          // ErrInvalid (name, kind, ACL), ErrConflict (name)
	Channel(ctx, id int64) (Channel, error); ChannelByName(ctx, name string) (Channel, error)
	ListChannels(ctx) ([]Channel, error); ListChannelsByKind(ctx, k Kind) ([]Channel, error)  // by name
	RenameChannel(ctx, id int64, name string) error               // ErrInvalid, ErrConflict, ErrNotFound
	DeleteChannel(ctx, id int64) error                            // cascades to ACL rows, subscriptions, cursors, messages
	SetEvictedBefore(ctx, id int64, seq int64) error              // max(current, seq)
	ACL(ctx, id int64) ([]ACLEntry, error)                        // stored entries, insertion order
	SetACL(ctx, id int64, entries []ACLEntry) error               // validate all, then replace all, one transaction
	EffectivePermissions(ctx, id int64, identity string) (Permissions, error) // union of stored rows for identity, everyone, admins (when identity is admin) plus the implicit entries
	ReadableChannels(ctx, identity string) ([]int64, error)       // ids where EffectivePermissions.Read, ascending
}

// acl.go
type Permission string
const (PermRead Permission = "read"; PermWrite Permission = "write"; PermAdmin Permission = "admin")
type ACLEntry struct { Identity string; Permissions []Permission }
type Permissions struct { Read, Write, Admin bool }
func (p Permissions) Union(q Permissions) Permissions
func ValidPermission(p Permission) bool
func ValidateEntry(e ACLEntry) error        // ErrInvalid: empty list, unknown permission, identity failing NameRule (everyone/admins pass)
func DefaultACL(creator string) []ACLEntry // Decision 88 literal: {everyone,[read,write]}, {creator,[read,write,admin]} (omitted when creator == ""), {admins,[admin]}

// names.go
func NameRule(name string) error  // v1 text: "must be 1-128 bytes", "must not contain '/'", "must not contain control characters"
func ValidKind(k Kind) bool

// tags.go
func TagRange(pattern string) (lo, hi string)
type TagPattern struct { Pattern, Lo, Hi string }
func NewTagPattern(pattern string) TagPattern // fills Lo, Hi with TagRange

// subscriptions.go
type Subscription struct { Identity string; ChannelID int64; AckedSeq, CreatedAt int64 }
type TagSubscription struct { Identity, TagsKey string; Patterns []TagPattern; CreatedSeq int64 }
type Subscriptions interface {
	Subscribe(ctx, identity string, channelID int64, ackedSeq, now int64) error // no-op when present; ErrNotFound on unknown identity or channel (FK)
	Unsubscribe(ctx, identity string, channelID int64) error
	Subscriptions(ctx, identity string) ([]Subscription, error)                 // by channel id
	AdvanceAckedMark(ctx, identity string, channelID int64, seq int64) error    // max(current, seq)
	SubscribeTags(ctx, s TagSubscription) error                                  // insert set and its pattern rows; no-op when present
	UnsubscribeTags(ctx, identity, tagsKey string) error
	TagSubscriptions(ctx, identity string) ([]TagSubscription, error)
	TagMatchesAfter(ctx, identity string, afterSeq int64, channelIDs []int64, limit int) ([]Message, error) // v1 tagCond, scoped to channelIDs (Decision 95); live messages only; ascending
}

// sessions.go
type Session struct { ID, Identity, DisplayName, Hint, ResumeSecret string; OpenedAt, LeaseExpiresAt, ClosedAt int64; PendingToken string; TagsCursorSeq, TagsPendingEndSeq int64; WatchID string }
type Cursor struct { SessionID string; ChannelID, CursorSeq, PendingEndSeq int64 }
type Sessions interface {
	OpenSession(ctx, s Session) error                                   // ErrConflict on id or resume secret; ErrNotFound on identity
	Session(ctx, id string) (Session, error); SessionByResumeSecret(ctx, secret string) (Session, error)
	ListSessions(ctx, identity string) ([]Session, error)              // "" = every identity; by opened_at, id
	RenewLease(ctx, id string, until int64) error
	CloseSession(ctx, id string, at int64) error
	DeleteSessions(ctx, lapsedBefore int64, limit int) (int, error)    // lease_expires_at < lapsedBefore (Decision 79 retention); cascades cursors
	RegisterWatch(ctx, id, watchID string, leaseUntil int64) error     // sets watch_id and renews the lease (Decisions 42, 68)
	SetPendingToken(ctx, id, token string) error
	SetTagsCursor(ctx, id string, cursorSeq, pendingEndSeq int64) error
	Cursors(ctx, sessionID string) ([]Cursor, error)                   // by channel id
	PutCursor(ctx, c Cursor) error                                     // upsert
	DeleteCursor(ctx, sessionID string, channelID int64) error
}

// messages.go
type Message struct { Seq, ChannelID int64; Sender, SessionID string; CreatedAt int64; Type, Subject, Content string; ReplyTo *int64; Metadata map[string]string; MemoryID, Revision *int64; Tombstone bool; TombstoneAt *int64; Tags []string }
type NewMessage struct { ChannelID int64; Sender, SessionID string; CreatedAt int64; Type, Subject, Content string; ReplyTo *int64; Metadata map[string]string; MemoryID, Revision *int64; Tags []string }
type SeqChannel struct { Seq, ChannelID int64 }
type Embedding struct { Seq int64; Model string; Vector []byte }
type EmbedFailure struct { Seq int64; Model, Error string; FailedAt int64 }
type Messages interface {
	AppendMessage(ctx, m NewMessage) (int64, error)                                   // Append only; NextSeq, insert, tags, dialect NotifyAppend
	Message(ctx, seq int64) (Message, error)                                           // tombstoned rows too; ErrNotFound
	MessagesAfter(ctx, channelID, afterSeq int64, limit int) ([]Message, error)       // live, ascending
	MessagesInRange(ctx, channelID, fromSeq, toSeq int64, patterns []TagPattern, limit int) ([]Message, error) // live, fromSeq <= seq <= toSeq, every pattern matched by some tag (v1 tagsFilter)
	SearchText(ctx, q search.Query, channelIDs []int64, limit int) ([]Message, error) // live, on channelIDs only; ErrInvalid on an empty query
	TombstoneMessage(ctx, seq, at int64) (bool, error)                                // false when already tombstoned or missing
	DeleteMessages(ctx, seqs []int64) error
	ExpiredTombstones(ctx, before int64, limit int) ([]int64, error)                   // tombstone and tombstone_at < before
	OrdinaryMessagesBefore(ctx, createdBefore int64, limit int) ([]SeqChannel, error) // memory_id IS NULL and created_at < createdBefore, ascending seq (v1 deleteOrdinaryChunk)
	UnembeddedMemories(ctx, model string, limit int) ([]Message, error)               // v1 unembeddedFrom: live memories on channels of kind <> 'tasks' with no embedding and no failure for model
	PutEmbedding(ctx, e Embedding) error; PutEmbedFailure(ctx, f EmbedFailure) error  // replace; only when the message is a live memory (v1 embed.go:362-367), else no-op
	DeleteEmbedding(ctx, seq int64) error; DeleteStaleModel(ctx, model string) error  // rows whose model <> model, both tables
	Embeddings(ctx, model string, channelIDs []int64) ([]Embedding, error)            // live memories on channelIDs
}

// memories.go
type MemoryRef struct { MemoryID, Seq int64 }
type Memories interface {
	LiveMemory(ctx, memoryID int64) (Message, error)                       // tombstone = false; ErrNotFound
	MemoryRevisions(ctx, memoryID int64) ([]Message, error)                // by revision
	SetMemory(ctx, seq, memoryID, revision int64) error
	TouchMemoryAccess(ctx, ids []int64, at int64) error                    // upsert
	DeleteMemoryAccess(ctx, memoryID int64) error
	ExpiredMemories(ctx, accessedBefore int64, limit int) ([]MemoryRef, error) // live memories whose access row is older (v1 memoryExpiryChunk)
}
type Tasks interface {
	LiveMemories(ctx, channelID int64) ([]Message, error)                  // v1 loadTasks: live memory rows on the channel, ascending seq
	ChannelLastActivity(ctx, channelID int64) (int64, error)               // v1 taskLastActivitySQL: max of created_at, tombstone_at over the channel's messages and the channel's created_at
}
```

`internal/search/query.go`: `type Term struct { Text string; Prefix, Phrase bool }`, `type Query struct { Terms []Term }`, `func (q Query) Empty() bool`.

Every `ctx` above is `context.Context` (Decision 6); the signatures are abbreviated here only. Interfaces exist so that `sqlstore` can implement them one file per aggregate; nothing else implements them in this milestone.

- [ ] **Step 1: Write the failing unit tests**

`names_test.go`: `TestNameRule` (table: `ok`, `a-b.c`, 128 bytes pass; empty, 129 bytes, `a/b`, `a\x01b`, `tab\there` fail with the v1 texts); `TestValidKind` (the three kinds pass, `Kind("")`, `Kind("dm")`, `Kind("Ordinary")` fail). `acl_test.go`: `TestValidateEntry` (ok: `{everyone,[read]}`, `{admins,[admin]}`, `{pat,[read,write,admin]}`; `ErrInvalid`: `Permissions: nil`, `[]Permission{}`, `Permission("owner")`, `Permission("Read")`, identity `a/b`, identity `""`); `TestDefaultACL` (with `pat`: exactly `[{everyone,[read,write]},{pat,[read,write,admin]},{admins,[admin]}]`; with `""`: the first and last only); `TestPermissionsUnion`. `tags_test.go`: `TestTagRange` (`env` → `env`,`env`; `env*` → `env`,`env~`; `env:*` → `env:`,`env:~`).

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/store/`
Expected: build failure, `undefined: NameRule` and friends.

- [ ] **Step 3: Implement the package and `internal/search/query.go`**

`go get github.com/jmoiron/sqlx@v1.4.0`, then `internal/store/sqlstore/doc.go` with `import _ "github.com/jmoiron/sqlx"` so that `go mod tidy` keeps the require line (`make tidy-check` passes) and Task 2 never touches `go.mod`. Each aggregate file holds its interface and types with doc comments naming the decision each field serves (as listed above).

- [ ] **Step 4: Run the tests, vet, lint, format**

Run: `make build-all vet fmt-check lint test`
Expected: pass.

- [ ] **Step 5: Commit**

```bash
git add internal/store internal/search go.mod go.sum
git commit -m "feat(store): Store contract, domain types, errors and input rules

Interfaces grouped by aggregate (Decision 25) over Tx, Append and Read
(Decisions 26, 27, 69). Ports NameRule from v1 internal/bus/sessions.go
(Decision 93) and tagRange from v1 internal/bus/tags.go; does not port
ChannelNameRule, prefix kinds or IsTaskChannel (Decisions 76, 82, 90)."
```

---

### Task 2: `sqlstore` framework and the SQLite engine

**Depends on:** 1.

**Files:**
- Create: `internal/store/sqlstore/dialect.go` (`Dialect`), `internal/store/sqlstore/store.go` (`Open`, `Store`, `Tx`/`Append`/`Read`, retry), `internal/store/sqlstore/migrate.go` (`Migration`, bootstrap, Decision 34 rules, runner), `internal/store/sqlstore/tx.go` (`tx` type, `exec`/`get`/`sel` helpers with `Rebind`), `internal/store/sqlstore/meta.go` (`Meta` methods), `internal/store/sqlstore/errors.go` (`ErrNotImplemented`, the stub sentinel), one stub file per remaining aggregate: `identities_stub.go`, `receipts_stub.go`, `channels_stub.go`, `subscriptions_stub.go`, `sessions_stub.go`, `messages_stub.go`, `memories_stub.go`, `tasks_stub.go`
- Create: `internal/store/sqlite/sqlite.go` (`Open`, `Options`, `DSN`, `uriPath`, `ApplicationID`, the dialect), `internal/store/sqlite/migrations.go` (`//go:embed schema/*.sql`, `Migrations()`), `internal/store/sqlite/schema/0001_initial.sql` (the SQLite DDL above, verbatim)
- Test: `internal/store/sqlite/dsn_test.go`, `internal/store/sqlite/open_test.go`, `internal/store/sqlite/tx_test.go`, `internal/store/sqlstore/migrate_test.go` (pure: `TestValidateMigrations`, versions must be exactly `1..n` in order, a gap, a duplicate or a start other than 1 is `ErrSchemaInvalid`; `Open` runs this check before touching the database)

**Port from v1:** `internal/bus/bus.go:92-134` (`SQLiteDSN`, `sqliteURIPath`: `file:` URI through `net/url`, `_txlock=immediate&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)`; file name `agentbus2.db` is the caller's), `internal/bus/dsn_test.go` (both tests), `internal/bus/bus.go:140-170` (the version check shape; v2 reads `schema_meta` and `application_id` instead of `user_version`), `internal/bus/migrate.go:43-96` (`migrate`, `runMigration`: one transaction per step, re-read the version under the write lock, stamp before commit; v2 re-reads `schema_meta.schema_version`), `internal/bus/maintenance.go:18-33` (`takeLease`), `internal/bus/embed.go:249-256` (notices upsert and delete). Not ported: `PRAGMA user_version`, `auto_vacuum` conversion (budget eviction is Milestone 7's; add it there if the maintenance port needs it), `checkBoundary`, `ensureDefaults`.

**Interfaces:**
- Consumes: everything in Task 1.
- Produces:

```go
package sqlstore

type Dialect interface {
	Name() store.Engine
	BindType() int                                            // sqlx.QUESTION or sqlx.DOLLAR
	IsRetryable(err error) bool                               // choice 5
	IsEmpty(ctx context.Context, db *sqlx.DB) (bool, error)   // no user tables (SQLite: sqlite_master; Postgres: information_schema.tables in current_schema())
	Stamp(ctx context.Context, db *sqlx.DB) error             // SQLite: PRAGMA application_id = ApplicationID; Postgres: no-op
	CheckStamp(ctx context.Context, db *sqlx.DB) error        // SQLite: ErrNotAgentbus2 unless application_id == ApplicationID; Postgres: no-op
	LockMigrations(ctx context.Context, tx *sqlx.Tx) error    // SQLite: no-op (immediate tx); Postgres: pg_advisory_xact_lock(hashtext(current_schema()))
	LockAppend(ctx context.Context, tx *sqlx.Tx) error        // SQLite: no-op; Postgres: LOCK TABLE seq_counter IN EXCLUSIVE MODE (choice 4)
	TextSearch(q search.Query) (cond string, args []any, err error) // Task 10; until then returns ErrNotImplemented
	NotifyAppend(ctx context.Context, tx *sqlx.Tx, channel string, seq int64) error // Milestone 7; no-op now
}

type Migration struct { Version int; Up func(ctx context.Context, tx *sqlx.Tx) error }

type Options struct {
	Migrations []Migration // nil: the engine's set; tests may pass the engine's set plus extra steps
	TxAttempts int         // 0: engine default (Postgres 5, SQLite 1)
	Now        func() int64 // 0 value: time.Now().UnixMilli(); schema_meta.created_at only
}

func Open(ctx context.Context, db *sqlx.DB, d Dialect, opts Options) (*Store, error) // applies the Decision 34 rules then migrates; closes db on error
func (s *Store) Engine() store.Engine; Tx; Append; Read; Close
func (s *Store) SchemaVersion(ctx context.Context) (int, error)
var ErrNotImplemented = errors.New("sqlstore: not implemented in this milestone") // deleted by Task 11
```

```go
package sqlite

const ApplicationID int32 = 0x61623200 // "ab2\0"; distinct from v1's 0 (v1 stamps user_version, not application_id)
type Options struct { BusyTimeout time.Duration /* 0: 5 s (Decision 69) */; Store sqlstore.Options }
func DSN(path string, busy time.Duration) (string, error) // v1 SQLiteDSN with the file name given by the caller and busy_timeout from busy
func Open(ctx context.Context, path string, opts Options) (store.Store, error)
func Migrations() []sqlstore.Migration                  // [{1, run schema/0001_initial.sql}]
```

Open rules, implemented in `sqlstore.Open` in this order (Decision 34): `d.IsEmpty` → if empty: `d.Stamp`, then the bootstrap transaction (`d.LockMigrations`, `CREATE TABLE IF NOT EXISTS schema_meta`, insert version 0 with `ON CONFLICT (id) DO NOTHING`); if not empty: `d.CheckStamp`, then read `schema_meta`: table missing → `ErrNotAgentbus2`; `app != "agentbus2"` → `ErrNotAgentbus2`; `engine != d.Name()` → `ErrSchemaInvalid` (`"engine %q, want %q"`); `schema_version < 0` → `ErrSchemaInvalid`; `schema_version > latest` → `ErrUpgradeNeeded`. Then migrate from the current version: for each step `v → v+1`, one transaction: `d.LockMigrations`, re-read `schema_version`; if it is not `v`, skip the step (another process got there first); run `Up`; `UPDATE schema_meta SET schema_version = v+1`; commit. A missing step (`Migrations` has no entry for `v+1`) is `ErrSchemaInvalid`. State (Definition of Done D): a second open does nothing; an overlapping open waits on the lock and then sees the new version; a failed step rolls back (DDL is transactional on both engines), leaves the previous version and the error, and the next open retries it.

Transactions: `Tx` and `Append` begin with `sql.TxOptions{Isolation: sql.LevelSerializable}` (modernc.org/sqlite ignores the isolation level and uses `_txlock=immediate`, `tx.go:23` of v1.60.1; pgx issues `BEGIN ISOLATION LEVEL SERIALIZABLE`); `Append` runs `d.LockAppend` first and marks the `tx` as append-capable; `Read` begins with `sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true}` (pgx issues `BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY`, one snapshot for the whole `fn`, which is what the seq tests of Tasks 3 and 11 rely on when they read `MaxSeq` and then the rows; under the default `READ COMMITTED` each statement would take its own snapshot; modernc.org/sqlite ignores the level and issues a plain deferred `begin`, `tx.go:23`, which in WAL mode is one snapshot from the first read), and the `tx` rejects `exec` calls with `ErrReadOnly` before the engine sees them. The retry loop classifies the error from `fn` and from `Commit` alike with `d.IsRetryable`; after `TxAttempts` failures (or on the first for SQLite) it returns `fmt.Errorf("%w: %v", store.ErrRetryable, err)`; a `fn` error that is not retryable is returned as is after rollback; a context error is returned as is.

`Meta` methods in `meta.go`: `NextSeq` (`UPDATE seq_counter SET v = v + 1 WHERE id = 1 RETURNING v`; `ErrNotAppend` unless the tx is an append tx), `MaxSeq`, `TryLease` (v1 `takeLease` SQL, `RowsAffected() == 1`), `SetNotice` (`ON CONFLICT (kind) DO UPDATE`), `ClearNotice`, `Notice`.

Stubs: each `*_stub.go` implements its aggregate's methods on `*tx` with `return ..., ErrNotImplemented` and a file comment "replaced by Task N". `var _ store.Tx = (*tx)(nil)` in `tx.go`.

- [ ] **Step 1: Write the failing tests (SQLite-only, Docker-free; they run in `make test`)**

`dsn_test.go`: port both v1 `dsn_test.go` tests (`TestURIPath`, `TestDSNHasEmptyAuthority`) plus `TestDSNCarriesPragmas` (contains `_txlock=immediate`, `busy_timeout(250)` when `busy = 250 * time.Millisecond`, `journal_mode(WAL)`, `foreign_keys(1)`).

`open_test.go` (each opens `filepath.Join(t.TempDir(), "agentbus2.db")`):
- `TestOpenEmptyCreatesSchema`: `SchemaVersion == 1`; `schema_meta` row is `{app: agentbus2, engine: sqlite, created_at: opts.Now()}`; `PRAGMA application_id == ApplicationID`; `PRAGMA user_version == 0`; `seq_counter.v == 0`; `leases` has the `maintenance` row; a second `Open` on the same path succeeds and `created_at` is unchanged.
- `TestOpenRefusesNonEmptyWithoutSchemaMeta`: a v1-shaped file made with plain `sql.Open("sqlite", path)`: `CREATE TABLE channels (name TEXT PRIMARY KEY, kind TEXT)` and `PRAGMA user_version = 12`; `Open` returns `errors.Is(err, store.ErrNotAgentbus2)` and the file is unchanged (`sqlite_master` count and `user_version` as before).
- `TestOpenRefusesWrongApp`: open once, then `UPDATE schema_meta SET app = 'other'`; reopen → `ErrNotAgentbus2`. Also: correct `schema_meta` but `PRAGMA application_id = 0` → `ErrNotAgentbus2`.
- `TestOpenRefusesNewerVersion`: `UPDATE schema_meta SET schema_version = 2` (with the production set); reopen → `ErrUpgradeNeeded` and `err.Error()` contains `upgrade agentbus2`.
- `TestOpenMigratesOlderVersion`: open with `Migrations: append(sqlite.Migrations(), Migration{2, CREATE TABLE probe (x INTEGER)})` on a database first opened at version 1 → `SchemaVersion == 2`, `probe` exists; a third open with the same set changes nothing (second-run proof).
- `TestOpenFailedStepLeavesPreviousVersion`: step 2 creates `probe` then returns an error → `Open` fails, `schema_version` stays 1, `probe` does not exist; reopening with a working step 2 succeeds (leftovers proof).
- `TestOpenRejectsInvalidMeta`: `engine = 'postgres'` → `ErrSchemaInvalid`; `schema_version = -1` → `ErrSchemaInvalid`; a set missing version 2 while the file is at 1 and the set's latest is 3 → `ErrSchemaInvalid`.
- `TestOpenConcurrentEmpty`: two goroutines `Open` the same fresh path at once; both succeed; `schema_version == 1`; exactly one `schema_meta` row (overlap proof; the immediate transaction serializes the bootstrap).

`tx_test.go`:
- `TestNextSeqNeedsAppend`: inside `Tx`, `NextSeq` → `ErrNotAppend`; inside `Append`, 1 then 2; `MaxSeq` inside a later `Read` is 2.
- `TestReadRejectsWrites`: `SetNotice` inside `Read` → `ErrReadOnly`; no row written.
- `TestBusyIsRetryable`: `BusyTimeout: 100 * time.Millisecond`; one store holds an `Append` open on a channel (block `fn` on a channel until released); a second `Open` of the same file runs `Tx` that writes → returns within a second with `errors.Is(err, store.ErrRetryable)`; release; the second store's `Tx` then succeeds (Decision 69, SQLite half).
- `TestTxRetriesNothingOnSQLite`: a counting `fn` that returns a wrapped `*sqlite.Error` with code 5 fabricated via a real lock conflict is covered above; here assert `TxAttempts` defaults to 1 for the SQLite dialect (`store.(*sqlstore.Store)` exposes `Attempts()` for tests).
- `TestLeaseAndNotices`: `TryLease("maintenance", "a", 100, 200)` true; `("maintenance", "b", 150, 300)` false; `("maintenance", "a", 150, 300)` true (owner renews); `("maintenance", "b", 250, 400)` true (expired); notices set, read, replaced, cleared.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/store/sqlite/ ./internal/store/sqlstore/`
Expected: build failure (`undefined: Open`).

- [ ] **Step 3: Implement**

As in Interfaces. Notes the signatures do not determine: `sqlite.Open` builds the DSN with `DSN(path, busy)`, `sqlx.Open("sqlite", dsn)`, `db.SetMaxOpenConns(1)` is **not** set (readers in WAL mode run on other connections; the immediate lock serializes writers), then `sqlstore.Open`. `IsEmpty` on SQLite: `SELECT count(*) FROM sqlite_master WHERE type = 'table'` is 0. `IsRetryable` on SQLite: `errors.As(err, &e)` with `e *sqlite.Error` (`modernc.org/sqlite`) and `e.Code()` in `{5, 6}`. The migration runner executes the embedded file as one `ExecContext` (modernc.org/sqlite runs multi-statement strings; the file must not contain `BEGIN`/`COMMIT`). `tx.exec`/`get`/`sel` rebind with `sqlx.Rebind(d.BindType(), q)`.

- [ ] **Step 4: Run the tests, vet, lint, format**

Run: `make build-all vet fmt-check lint test`
Expected: pass; `TestBusyIsRetryable` under one second.

- [ ] **Step 5: Commit**

```bash
git add internal/store/sqlstore internal/store/sqlite
git commit -m "feat(store): sqlstore framework and SQLite engine with schema_meta, migrations and Tx

Open rules of Decision 34 with PRAGMA application_id; one migration set
from version 1 under an immediate transaction re-reading schema_version
(Decisions 53, 68; v1 internal/bus/migrate.go runMigration); Tx, Append
and Read with SQLITE_BUSY surfaced as ErrRetryable after the busy timeout
(Decision 69); seq_counter (Decision 27). Ports SQLiteDSN and
sqliteURIPath from v1 internal/bus/bus.go with dsn_test.go, takeLease
from v1 internal/bus/maintenance.go, and the v1 schema.go tables as the
version 1 DDL (no migration from v1, Decision 29)."
```

---
### Task 3: Postgres engine and the engine conformance tests on both engines

**Depends on:** 2.

**Files:**
- Create: `internal/store/postgres/postgres.go` (`Open`, `Options`, the dialect), `internal/store/postgres/migrations.go`, `internal/store/postgres/schema/0001_initial.sql` (the Postgres DDL above, verbatim)
- Create: `internal/conformance/store/helpers_test.go` (`openStore`, `reopen`, `pgSchema`), `internal/conformance/store/open_test.go`, `internal/conformance/store/tx_test.go`, `internal/conformance/store/seq_test.go`
- Test (pure, beside the code): `internal/store/postgres/dialect_test.go` (`IsRetryable` on fabricated `*pgconn.PgError` codes; `withSearchPath` DSN rewriting)

**Interfaces:**
- Consumes: `sqlstore.Open`, `Dialect`, `Migration`, `Options` (Task 2); `dbtest.Engine.DSN`, `dbtest.Engine.Open`, `dbtest.ForEachEngine` (Milestone 1).
- Produces:

```go
package postgres
const TxAttempts = 5
type Options struct { Store sqlstore.Options }
func Open(ctx context.Context, dsn string, opts Options) (store.Store, error) // pgx stdlib; ParseConfig first (never print the DSN; Milestone 1 Task 4's rule)
func Migrations() []sqlstore.Migration
func WithSearchPath(dsn, schema string) (string, error) // appends search_path=<schema> as a DSN query parameter (pgx runtime param); ErrInvalid unless schema matches ^[a-z][a-z0-9_]{0,62}$
```

```go
package store_test // internal/conformance/store

// openStore opens a fresh, isolated store on e through the engine's own Open and registers cleanup.
// sqlite: filepath.Join(t.TempDir(), "agentbus2.db"); postgres: a schema t_<16 hex> created on e.Open(t), DSN via WithSearchPath.
func openStore(t *testing.T, e dbtest.Engine, opts sqlstore.Options) (s store.Store, reopen func(opts sqlstore.Options) (store.Store, error), rawDSN func() string)
```

`rawDSN` returns the path (sqlite) or the schema-scoped DSN (postgres) so a test can open a plain `*sql.DB` on the same database to make fixtures (a stray table, an edited `schema_meta` row); it is never printed. Cleanup order: the `DROP SCHEMA ... CASCADE` cleanup is registered before any store is opened, so `t.Cleanup`'s LIFO order closes stores first. State: a test killed mid-way leaves a `t_<hex>` schema on the server, dropped by hand with `DROP SCHEMA t_<hex> CASCADE`; two overlapping test runs use different random schemas; the SQLite temp dir is `t.TempDir()`'s.

Dialect on Postgres: `BindType` `sqlx.DOLLAR`; `IsRetryable`: `*pgconn.PgError` with `Code` `40001` or `40P01`; `IsEmpty`: `SELECT count(*) FROM information_schema.tables WHERE table_schema = current_schema()` is 0; `Stamp`/`CheckStamp` no-ops; `LockMigrations`: `SELECT pg_advisory_xact_lock(hashtext(current_schema()))`; `LockAppend`: `LOCK TABLE seq_counter IN EXCLUSIVE MODE`; `TextSearch`: `ErrNotImplemented` until Task 10; `NotifyAppend`: no-op (Milestone 7). `Open`: `pgx.ParseConfig(dsn)` (error text never includes the DSN), `sqlx.Open("pgx", dsn)`, `PingContext`, `sqlstore.Open`.

- [ ] **Step 1: Write the failing conformance tests (both engines, `dbtest.ForEachEngine`)**

`open_test.go`, the Decision 34 rule set once more, now on both engines through `openStore`/`reopen`/`rawDSN` (the SQLite-only tests of Task 2 stay as the fast path): `TestOpenEmptyCreatesSchema` (plus: `schema_meta.engine` equals `e.Name`), `TestOpenRefusesNonEmptyWithoutSchemaMeta` (sqlite: the v1-shaped file; postgres: `CREATE TABLE stray (x INTEGER)` in the test schema before the first `Open`), `TestOpenRefusesWrongApp`, `TestOpenRefusesNewerVersion` (`upgrade agentbus2` in the text), `TestOpenMigratesOlderVersion` (extra step 2 from the engine's `Migrations()` plus a `probe` table; third open is a no-op), `TestOpenFailedStepLeavesPreviousVersion` (postgres proves DDL rollback too: `probe` must not exist), `TestOpenRejectsInvalidMeta` (engine mismatch: sqlite row says `postgres` and vice versa; negative version; missing step), `TestOpenConcurrentEmpty` (two `reopen` calls on a fresh database from two goroutines; one `schema_meta` row; version 1).

`tx_test.go`:
- `TestNextSeqNeedsAppend`, `TestReadRejectsWrites`, `TestLeaseAndNotices` as in Task 2, on both engines.
- `TestTxRetriesInjectedSerializationFailure`: `fn` returns a `*pgconn.PgError{Code: "40001"}` on its first call and nil after; on postgres `Tx` returns nil and `fn` ran twice; on sqlite the same `fn` makes `Tx` return `ErrRetryable` after one call (the SQLite dialect does not recognize pgx errors and attempts is 1; the assertion is "ran once, error is not retryable-wrapped": `errors.Is(err, store.ErrRetryable)` is false and the raw error comes back). State both expectations in one table-driven test keyed by `e.Name`.
- `TestTxSerializationFailureAtCommitIsRetried`: two stores on the same database; tx A reads notice `x` and writes notice `y`, tx B reads `y` and writes `x`, started concurrently. On postgres a barrier after the reads (released only on each `fn`'s first run) makes both snapshots predate both writes, so one commit fails with 40001; the retry runs that `fn` again, both `Tx` calls return nil, the counters show at least one re-run, and the final notices are consistent with a serial order. On sqlite there is no barrier (B blocks at `BEGIN IMMEDIATE` until A commits, so a barrier inside `fn` would deadlock): both return nil with exactly one run each and the same serial-order assertion.
- `TestTxGivesUpAfterAttempts`: `TxAttempts: 2`, `fn` always returns 40001 → postgres: `ErrRetryable` after exactly 2 runs; sqlite: as above, 1 run.
- `TestTxNonRetryableErrorPassesThrough`: `fn` returns `errors.New("boom")` → the same error, one run, nothing committed.

`seq_test.go`, the Decision 54 concurrent-writer test: 8 writer goroutines, each over its own store opened by `reopen` (separate connections), each appending 50 rows through `Append` with `NextSeq` into a scratch table created by the test? No: the suite must not add tables. Use `NextSeq` plus `SetNotice(kind = "seq-<v>")` inside the same `Append` as the commit marker (the messages aggregate lands in Task 6; Task 11 switches this test to `AppendMessage`, see its Step 2). A reader goroutine polls inside one `Read` per poll: `m := MaxSeq()`, then `Notice("seq-<k>")` must exist for every `k` in `1..m` (the contiguous-prefix property: a snapshot that shows counter value `m` shows every lower seq's row, because each lower value's transaction held the counter row earlier and therefore committed earlier; a Postgres sequence would not give this, Decision 27), and `m` never decreases between polls. Assert 400 appends, `MaxSeq == 400`, no error escaped (`ErrRetryable` included), and the reader saw contiguous prefixes on every poll. On postgres also assert the run stays under 30 s (the probe took well under that with the table lock; without `LockAppend` the retries would exhaust the budget and the no-error assertion fails, which is the test's teeth).

`dialect_test.go` (pure): `TestIsRetryable` (`40001`, `40P01` true; `23505`, `errors.New` false), `TestWithSearchPath` (appends `search_path=t_ab` to a DSN with and without an existing query; rejects `T_ab`, `a-b`, `""`).

- [ ] **Step 2: Run them to verify they fail**

Run: `go test -count=1 -run 'TestOpen|TestTx|TestSeq' ./internal/conformance/store/`
Expected: build failure (`undefined: postgres.Open`).

- [ ] **Step 3: Implement `internal/store/postgres` and the helpers**

As in Interfaces. The embedded `0001_initial.sql` runs as one `ExecContext` (pgx stdlib sends a multi-statement simple-protocol query when there are no arguments; verified by the migration test itself). `openStore` generates the schema name with `crypto/rand` (8 bytes, hex), runs `CREATE SCHEMA t_<hex>` on `e.Open(t)`, registers `DROP SCHEMA t_<hex> CASCADE` in `t.Cleanup`, then returns `reopen` closures that call `postgres.Open` with the `WithSearchPath` DSN.

- [ ] **Step 4: Run the suite on both engines, then the gate**

Run: `env -u AGENTBUS2_TEST_POSTGRES_DSN go test -count=1 -p 1 -v -run 'TestOpen|TestTx|TestSeq' ./internal/conformance/store/ 2>&1 | grep -E '^(--- |ok|FAIL)' | head -60`
Expected: every test has `--- PASS` for both `/sqlite` and `/postgres`; no `SKIP`. Then `make verify` with a 600000 ms timeout: `verify: OK`.

- [ ] **Step 5: Commit**

```bash
git add internal/store/postgres internal/conformance/store
git commit -m "feat(store): Postgres engine; Decision 34, Tx retry and concurrent-writer conformance on both engines

SERIALIZABLE with 40001/40P01 retried up to five attempts with jittered
backoff (Decision 69), the append transaction locking seq_counter so
appends queue instead of aborting (Decision 27), migrations under
pg_advisory_xact_lock per schema (Decision 53), per-test schemas through
search_path. The version 1 DDL re-expresses v1 internal/bus/schema.go
for Postgres (no migration from v1, Decision 29)."
```

---

### Task 4: Identities, public keys, receipts

**Depends on:** 3.

**Files:**
- Delete: `internal/store/sqlstore/identities_stub.go`, `internal/store/sqlstore/receipts_stub.go`
- Create: `internal/store/sqlstore/identities.go`, `internal/store/sqlstore/receipts.go`
- Modify: `internal/store/sqlstore/tx.go` (add `isConflict(err) bool` and `isForeignKey(err) bool`; Tasks 5 to 8 reuse both, so they are added here, by the first task that needs one, and never by two parallel tasks)
- Test: `internal/conformance/store/identities_test.go`, `internal/conformance/store/receipts_test.go`

**Port from v1:** `internal/bus/receipts.go` (`checkReceipt`: `fingerprint, result WHERE sender = ? AND key = ? AND expires_at > ?`; `storeReceipt`: replace; keyed by identity in v2, Decision 67). Identities and keys are new (Decisions 14, 57, 73; no v1 source).

**Interfaces:** consumes Task 1's `Identities` and `Receipts`; produces their `*tx` implementations.

- [ ] **Step 1: Write the failing conformance tests**

`identities_test.go`: `TestIdentityCreateGetList` (create `pat`, `kim`; `Identity("pat")` has `Admin false`, `CreatedAt` as given; list is `[kim, pat]`; unknown → `ErrNotFound`), `TestIdentityRejects` (`a/b` → `ErrInvalid`; `""`; 129 bytes; `everyone` and `admins` → `ErrInvalid` naming them reserved; duplicate → `ErrConflict`), `TestSetAdmin` (true then false; unknown → `ErrNotFound`), `TestAdvanceTagAckedMark` (10 then 5 leaves 10, 12 raises), `TestPublicKeys` (put two kids, list by kid, replace one (`Key` changes), unknown algorithm `RS256` → `ErrInvalid`, unknown identity → `ErrNotFound`, delete returns 2 then list empty), `TestDeleteIdentityCascade` is not here (identities are not deleted in 2.0.0; no method).

`receipts_test.go`: `TestReceiptRoundTrip` (put, get with `now < ExpiresAt` found with equal fields, `now >= ExpiresAt` not found; other identity same key not found, Decision 67 scope), `TestReceiptReplace` (second put with a new fingerprint wins), `TestDeleteExpiredReceipts` (3 rows, 2 expired at `now`, limit 1 → deletes 1, then 1, then 0).

- [ ] **Step 2: Run to verify they fail**

Run: `go test -count=1 -p 1 -run 'TestIdentity|TestSetAdmin|TestAdvanceTag|TestPublicKeys|TestReceipt|TestDeleteExpired' ./internal/conformance/store/`
Expected: `--- FAIL` with `ErrNotImplemented` on both engines.

- [ ] **Step 3: Implement** the two files; delete the two stubs. `CreateIdentity` runs `store.NameRule`, the reserved-name check, then `INSERT`, mapping the unique violation to `ErrConflict` (detect with the `isConflict` helper in `tx.go`: SQLite `*sqlite.Error` code 1555/2067 (`SQLITE_CONSTRAINT_PRIMARYKEY`/`UNIQUE`), Postgres `23505`). Add `isForeignKey` beside it (SQLite code 787, `SQLITE_CONSTRAINT_FOREIGNKEY`; Postgres `23503`), which `PutPublicKey` uses to map an unknown identity to `ErrNotFound` and which Tasks 6 (`AppendMessage` on an unknown channel), 7 (`OpenSession` on an unknown identity) and 8 (`Subscribe`) reuse. `PutPublicKey` validates `Algorithm` against the two values before the `INSERT ... ON CONFLICT (identity, kid) DO UPDATE`. `DeleteExpiredReceipts` selects up to `limit` keys then deletes them (portability rule).

- [ ] **Step 4: Run the suite and the gate**

Run: `env -u AGENTBUS2_TEST_POSTGRES_DSN go test -count=1 -p 1 -v ./internal/conformance/store/ 2>&1 | grep -E '^(--- FAIL|ok|FAIL)'`, then `make verify` (600000 ms).
Expected: no `--- FAIL`; `verify: OK`.

- [ ] **Step 5: Commit**

```bash
git add internal/store/sqlstore internal/conformance/store
git commit -m "feat(store): identities with admin flag and public keys; receipts keyed per identity

Receipts port v1 internal/bus/receipts.go (checkReceipt, storeReceipt)
re-keyed to the identity (Decision 67). Identities, the admin flag and
PKIX public keys are new rows for Decisions 14, 55, 57, 73; everyone and
admins are reserved names (pending Eric)."
```

---

### Task 5: Channels and ACL rows

**Depends on:** 4.

**Files:**
- Delete: `internal/store/sqlstore/channels_stub.go`
- Create: `internal/store/sqlstore/channels.go`, `internal/store/sqlstore/acl.go`
- Test: `internal/conformance/store/channels_test.go`, `internal/conformance/store/acl_test.go`

**Port from v1:** `internal/bus/channels.go:137-180` (`CreateChannel`: refuse an existing name, `created_seq` from the caller), `channels.go:223-307` (`deleteChannel`: the three deletes become one cascade), `channels.go:347-385` (`RenameChannel`: one row update, choice 6), `channels.go:109-121` (`ChannelKind` → `Channel.Kind`). Not ported: `ensureDefaults` (general is created by the server, Decision 92, Milestone 5), `isDefaultChannel`, `reapEmptyChannels` (Milestone 7 if the maintenance port keeps it), name prefixes.

**Interfaces:** consumes Task 1's `Channels`, `ACLEntry`, `ValidateEntry`, `DefaultACL`, `Permissions`; Task 4's `Identity` (for `EffectivePermissions` and Decision 96).

- [ ] **Step 1: Write the failing conformance tests**

`channels_test.go`: `TestCreateChannel` (fields round-trip; `ID > 0`; `ChannelByName`; `ListChannels` by name; `ListChannelsByKind(tasks)`), `TestCreateChannelRejects` (`a/b`, `""`, 129 bytes → `ErrInvalid` (Decision 93); `Kind("dm")`, `Kind("")` → `ErrInvalid` (Decision 90); duplicate name → `ErrConflict`; `Creator` not registered → `ErrInvalid`), `TestRenameChannel` (messages and cursors are Tasks 6 and 7; here: name changes, old name `ErrNotFound`, taken name `ErrConflict`, invalid name `ErrInvalid`, id unchanged), `TestDeleteChannelRemovesACL` (ACL rows gone, `ReadableChannels` no longer lists it, `Channel(id)` `ErrNotFound`), `TestSetEvictedBefore` (max semantics).

`acl_test.go`, the section 5.8 Store cases, one test each, named after the case:
- `TestACLCreatorRecorded`: `Channel.Creator == "pat"`.
- `TestACLDefaultRowsStoredWhenCreatedWithDefault`: create with `ACL: store.DefaultACL("pat")`: `ACL(id)` equals exactly `[{everyone,[read,write]},{pat,[read,write,admin]},{admins,[admin]}]` in that order.
- `TestACLDefaultRowsAbsentWhenCreatedWithOwn`: create with `ACL: [{kim,[read]}]`: `ACL(id)` is exactly that; `EffectivePermissions(id, "everyone")` is all false.
- `TestACLImplicitEntriesGrantAdmin`: stored ACL `[{kim,[read]}]`, creator `pat`, identity `root` with `SetAdmin(true)`: `EffectivePermissions(id, "pat") == {false,false,true}`, `(id, "root") == {false,false,true}`, `(id, "kim") == {true,false,false}`, `(id, "lee") == {false,false,false}`.
- `TestACLDuplicateEntriesAreAdditive`: `[{kim,[read]},{kim,[write]}]` → `{true,true,false}`; `ACL(id)` still returns both rows.
- `TestACLRejectsUnknownPermissionAndEmptyList`: `SetACL` with `Permission("owner")` → `ErrInvalid`; with `[]Permission{}` → `ErrInvalid`; with `nil` → `ErrInvalid`; after each, `ACL(id)` is unchanged (Review Focus 6).
- `TestACLRejectsUnregisteredIdentity`: `SetACL` with `{ghost,[read]}` → `ErrInvalid` (Decision 96); `everyone` and `admins` pass; `CreateChannel` with such an ACL fails and creates no channel.
- `TestACLRejectsNameWithSlash` and `TestACLRejectsUnknownKind`: covered by `TestCreateChannelRejects`; keep them as explicit one-assertion tests so the section 5.8 list maps one to one.
- `TestACLGeneralHasDefaultAndNoCreator`: create `general` with `Creator: ""` and `ACL: store.DefaultACL("")` → rows `[{everyone,[read,write]},{admins,[admin]}]`, `Creator == ""`, `EffectivePermissions(id, "pat")` for a non-admin is `{true,true,false}` (Decision 92).
- `TestACLRemovingAdminsAndCreatorRowsKeepsImplicitAdmin`: start from the default for `pat`; `SetACL(id, [{everyone,[read,write]}])`; `pat` and the admin `root` have `{true,true,true}` (everyone's read and write plus implicit admin); then `SetACL(id, [])`: both have `{false,false,true}` ("unable to read until they grant it"); `SetACL(id, [{pat,[read]}])` → `pat` is `{true,false,true}`.
- `TestACLDeleteChannelRemovesEntries`: as `TestDeleteChannelRemovesACL`, listed here for the section 5.8 map.
- `TestReadableChannels`: three channels, `kim` readable on two through different entries (`kim` direct, `everyone`), the admin `root` readable only where a read row exists (implicit entries grant admin, not read), ascending ids.

The last section 5.8 case ("the check the core makes for each row of the section 4.6 table") is Milestone 3's core suite.

- [ ] **Step 2: Run to verify they fail**

Run: `go test -count=1 -p 1 -run 'TestCreateChannel|TestRenameChannel|TestDeleteChannel|TestSetEvicted|TestACL|TestReadable' ./internal/conformance/store/`
Expected: `--- FAIL` with `ErrNotImplemented` on both engines.

- [ ] **Step 3: Implement** `channels.go` and `acl.go`; delete the stub. `CreateChannel` order: `NameRule`, `ValidKind`, creator registered (when non-empty), every entry through `ValidateEntry` then the Decision 96 registry check (`everyone`/`admins` exempt), then `INSERT ... RETURNING id`, then the ACL rows in order. `SetACL`: validate all (zero entries is valid and clears the stored ACL; the implicit entries still apply), `DELETE FROM channel_acl WHERE channel_id = ?`, insert in order. `EffectivePermissions`: `SELECT can_read, can_write, can_admin FROM channel_acl WHERE channel_id = ? AND identity IN (?, ?, ?)` with the identity, `everyone`, and `admins` only when `identities.is_admin` for the identity (one prior query), union in Go; add `Admin = true` when the identity is the creator or an admin. `ReadableChannels`: the same rule in one query (`EXISTS` over `channel_acl` with `can_read = TRUE` for the identity, `everyone`, or `admins` when admin), ordered by id.

- [ ] **Step 4: Run the suite and the gate**

As Task 4 Step 4. Expected: no `--- FAIL`; `verify: OK`.

- [ ] **Step 5: Commit**

```bash
git add internal/store/sqlstore internal/conformance/store
git commit -m "feat(store): channels with creator, kind tasks and ACL rows; section 5.8 conformance cases

Ports CreateChannel, deleteChannel and RenameChannel from v1
internal/bus/channels.go onto surrogate ids; adds the creator column,
the tasks kind (Decision 90) and channel_acl as three boolean columns
per stored entry (Decision 80; Milestone 2 choice 2), with the implicit
admins and creator entries evaluated from identities.is_admin and
channels.creator (Decisions 73, 96)."
```

---
### Task 6: Messages, tags, history, retention queries, embeddings

**Depends on:** 5.

**Files:**
- Delete: `internal/store/sqlstore/messages_stub.go`
- Create: `internal/store/sqlstore/messages.go` (append, get, after, range, tombstone, delete, retention queries, `messageCols`/scan, tags), `internal/store/sqlstore/embeddings.go`
- Test: `internal/conformance/store/messages_test.go`, `internal/conformance/store/embeddings_test.go`

**Port from v1:** `internal/bus/messages.go:27-59` (`Message`, `messageCols`; the `group_concat` tag subquery becomes a second query `SELECT seq, tag FROM message_tags WHERE seq IN (...) ORDER BY seq, tag`), `internal/bus/tags.go:117-160` (`insertTags`, `tagsOf`, `tagsFilter`: `EXISTS (SELECT 1 FROM message_tags t WHERE t.seq = m.seq AND t.tag BETWEEN ? AND ?)` per pattern, ANDed), `internal/bus/maintenance.go:78` (expired tombstones), `maintenance.go:151-190` (`deleteOrdinaryChunk`: select then delete, `evicted_before_seq` is the caller's through `SetEvictedBefore`), `internal/bus/embed.go:167-183` (`encodeVec`/`decodeVec` stay in the core; the Store stores bytes), `embed.go:194-199` (`unembeddedFrom`, with `m.channel <> 'tasks' AND ... NOT LIKE 'tasks/%'` replaced by `channels.kind <> 'tasks'`, Decision 90), `embed.go:277-290` (`DeleteStaleModel`), `embed.go:362-367` (conditional put), `internal/bus/memories.go:266` (`DeleteEmbedding`). `SearchText` is Task 10 (returns the dialect's `ErrNotImplemented` until then).

**Interfaces:** consumes Task 1's `Messages`, `NewMessage`, `Message`, `TagPattern`; Task 2's `NextSeq`, `Dialect.NotifyAppend`; produces the `*tx` implementations and the unexported `scanMessages(ctx, tx, query, args) ([]store.Message, error)` helper (query selects the fixed column list in `messageCols`) that Tasks 8 and 9 reuse.

- [ ] **Step 1: Write the failing conformance tests**

`messages_test.go`: `TestAppendMessageAssignsSeqAndStoresTags` (three appends on two channels: seq 1, 2, 3; `Message(2)` round-trips every field including `Metadata`, `ReplyTo`, sorted `Tags`; `MaxSeq == 3`), `TestAppendNeedsAppendTx` (`AppendMessage` inside `Tx` → `ErrNotAppend`, nothing stored; Review Focus 3), `TestAppendRejectsUnknownChannel` (FK → `ErrNotFound`), `TestMessagesAfter` (channel scoping, `afterSeq`, limit, tombstoned excluded, ascending), `TestMessagesInRangeWithTagFilter` (`[from, to]` inclusive; patterns `env*` and `team:a` both required; no patterns returns all live rows in range), `TestTombstoneMessage` (true once, false the second time, false for unknown; `Message(seq).Tombstone` and `TombstoneAt`), `TestDeleteMessagesCascades` (tags, embeddings, failures rows gone), `TestExpiredTombstones` and `TestOrdinaryMessagesBefore` (memories excluded, ascending, limit), `TestRenameChannelKeepsMessages` (messages found by the channel id after `RenameChannel`), `TestDeleteChannelDeletesMessages` (Review Focus 7).

`embeddings_test.go`: `TestPutEmbeddingOnlyForLiveMemory` (ordinary message: no row; live memory via `SetMemory`: row; tombstoned memory: no row), `TestUnembeddedMemories` (memory on an ordinary channel listed; on a `tasks` channel not; with an embedding for the model not; with a failure for the model not; for another model yes), `TestDeleteStaleModel`, `TestEmbeddingsByChannels` (scoped, live only), `TestDeleteEmbedding`.

- [ ] **Step 2: Run to verify they fail**

Run: `go test -count=1 -p 1 -run 'TestAppend|TestMessages|TestTombstone|TestDeleteMessages|TestExpiredTomb|TestOrdinary|TestRenameChannelKeeps|TestDeleteChannelDeletes|TestPutEmbedding|TestUnembedded|TestDeleteStale|TestEmbeddings|TestDeleteEmbedding' ./internal/conformance/store/`
Expected: `--- FAIL` with `ErrNotImplemented`.

- [ ] **Step 3: Implement**; delete the stub. `AppendMessage`: `ErrNotAppend` unless append tx; `NextSeq`; `INSERT INTO messages (seq, channel_id, ...)` with `metadata` as JSON text or NULL; tags in sorted order (the core normalizes; the Store sorts for determinism); `d.NotifyAppend(ctx, tx, channelName, seq)` (one lookup of the name by id, the hook that Milestone 7 fills). `Message.Tags` is loaded by `scanMessages` with the second query. `tombstone` is bound as a Go `bool`.

- [ ] **Step 4: Run the suite and the gate**

As Task 4 Step 4. Expected: no `--- FAIL`; `verify: OK`.

- [ ] **Step 5: Commit**

```bash
git add internal/store/sqlstore internal/conformance/store
git commit -m "feat(store): messages with tags, history ranges, retention queries and embeddings

Ports messageCols from v1 internal/bus/messages.go, insertTags, tagsOf
and tagsFilter from v1 internal/bus/tags.go, the retention selects from
v1 internal/bus/maintenance.go and the embedding rows from v1
internal/bus/embed.go (ADR 0018). seq comes from seq_counter inside the
append transaction (Decision 27) with the NotifyAppend hook for
Milestone 7 (Decision 52)."
```

---

### Task 7: Sessions, cursors, pending batch, watch registration

**Depends on:** 5.

**Files:**
- Delete: `internal/store/sqlstore/sessions_stub.go`
- Create: `internal/store/sqlstore/sessions.go`, `internal/store/sqlstore/cursors.go`
- Test: `internal/conformance/store/sessions_test.go`

**Port from v1:** `internal/bus/sessions.go:235` (sweep of lapsed rows → `DeleteSessions`), `sessions.go:534-541` (`Heartbeat` → `RenewLease`), `sessions.go:567` (`Unregister` → `CloseSession`), `internal/bus/receive.go:249` (per-subscription cursor rows → `session_cursors`), `receive.go:336,561,595` (the pending end per subscription and the batch token: `PutCursor`, `SetPendingToken`). Not ported: harness pid/start on the session (v1 ADR 0017; Decision 78 keeps that on the client), display-name assignment (Decision 39 is the core's), idle unsubscribe (`sessions.go:279`, ADR 0006).

**Interfaces:** consumes Task 1's `Sessions`, `Session`, `Cursor`; Task 4's identities (FK); Task 5's channels (FK).

- [ ] **Step 1: Write the failing conformance tests**

`sessions_test.go`: `TestOpenSessionRoundTrip` (all fields; `SessionByResumeSecret`; `ListSessions("pat")` and `ListSessions("")` order), `TestOpenSessionRejects` (duplicate id and duplicate resume secret → `ErrConflict`; unknown identity → `ErrNotFound`), `TestRenewLeaseAndClose` (`LeaseExpiresAt`, `ClosedAt`; unknown id → `ErrNotFound`), `TestRegisterWatchReplaces` (`WatchID` becomes the newer value and the lease is renewed; a reader of the older watch id sees it is no longer current: the Decision 68 replaced rule), `TestPendingTokenAndTagsCursor`, `TestCursorsUpsertAndDelete` (put, list by channel id, upsert changes `CursorSeq` and `PendingEndSeq`, delete), `TestDeleteSessionsLapsedBefore` (two lapsed, one live, limit 1 → 1 then 1 then 0; cursors of deleted sessions gone; the live one keeps its cursors: Decision 79), `TestDeleteChannelDeletesCursors` (Review Focus 7).

- [ ] **Step 2: Run to verify they fail**

Run: `go test -count=1 -p 1 -run 'TestOpenSession|TestRenewLease|TestRegisterWatch|TestPendingToken|TestCursors|TestDeleteSessions|TestDeleteChannelDeletesCursors' ./internal/conformance/store/`
Expected: `--- FAIL` with `ErrNotImplemented`.

- [ ] **Step 3: Implement**; delete the stub. `DeleteSessions`: select up to `limit` ids with `lease_expires_at < ?` ordered by `lease_expires_at, id`, then delete by id (cascade removes cursors). `RegisterWatch`: `UPDATE sessions SET watch_id = ?, lease_expires_at = ? WHERE id = ?` (`ErrNotFound` when no row).

- [ ] **Step 4: Run the suite and the gate**

As Task 4 Step 4. Expected: no `--- FAIL`; `verify: OK`.

- [ ] **Step 5: Commit**

```bash
git add internal/store/sqlstore internal/conformance/store
git commit -m "feat(store): sessions with leases, watch registration, per-session cursors and pending batch

Ports the heartbeat, sweep and unregister writes of v1
internal/bus/sessions.go and the cursor and pending-batch columns of v1
internal/bus/receive.go, re-keyed to a session row with a lease
(Decisions 16, 40, 42, 68, 79). Harness pid and start are not ported
(Decision 78)."
```

---

### Task 8: Subscriptions, tag subscriptions, acked marks, tag matches

**Depends on:** 6.

**Files:**
- Delete: `internal/store/sqlstore/subscriptions_stub.go`
- Create: `internal/store/sqlstore/subscriptions.go`, `internal/store/sqlstore/tagsubs.go`
- Test: `internal/conformance/store/subscriptions_test.go`, `internal/conformance/store/tagsubs_test.go`

**Port from v1:** `internal/bus/receive.go:88-160` (`Subscribe`: insert-or-ignore; `Unsubscribe`), `internal/bus/tags.go:180-250` (`SubscribeTags`, `UnsubscribeTags`: the set row and its pattern rows with `lo`/`hi`; the `tags/` pseudo-subscription row is replaced by the session and identity columns of choice 7), `tags.go:253-300` (`tagSets`), `tags.go:306-316` (`tagCond`: a message matches a set when every pattern of the set is matched by some tag (`HAVING count(DISTINCT st.tag) = (SELECT count(*) ...)`), `seq > max(floor, created_seq)`; the `kind = 'ordinary' AND name NOT LIKE 'dm/%'` and `NOT IN subscriptions` clauses become the caller's `channelIDs` list, Decision 95). Not ported: `tagSource` pseudo-channel, DM exclusions.

**Interfaces:** consumes Task 1's `Subscriptions`, `TagSubscription`, `TagPattern`; Task 6's `scanMessages`.

- [ ] **Step 1: Write the failing conformance tests**

`subscriptions_test.go`: `TestSubscribeIdempotent` (second call keeps the first `AckedSeq` and `CreatedAt`), `TestSubscribeRejectsUnknown` (identity or channel → `ErrNotFound`), `TestUnsubscribeAndList`, `TestAdvanceAckedMarkIsMax`, `TestDeleteChannelDeletesSubscriptions`.

`tagsubs_test.go`: `TestSubscribeTagsStoresPatternsWithRanges` (`TagSubscriptions` returns `Patterns` with `Lo`/`Hi` from `store.NewTagPattern`, by `TagsKey`), `TestUnsubscribeTagsCascades` (pattern rows gone; proven with a second set still present), `TestTagMatchesAfter`: messages on channels A, B, C with tags; identity `kim` has sets `{env*}` (created at seq 0) and `{team:a, prio:high}` (created after seq 3); `TagMatchesAfter("kim", 0, [A, B], 100)` returns, ascending, exactly the live messages on A or B that (match `env*` with seq > 0) or (carry a tag in `[team:a, team:a]` and one in `[prio:high, prio:high]` with seq > 3); a message on C is never returned (Decision 95 scoping); `afterSeq` and `limit` honored; a tombstoned match excluded; an identity with no sets gets an empty slice.

- [ ] **Step 2: Run to verify they fail**

Run: `go test -count=1 -p 1 -run 'TestSubscribe|TestUnsubscribe|TestAdvanceAcked|TestDeleteChannelDeletesSubscriptions|TestTagMatches' ./internal/conformance/store/`
Expected: `--- FAIL` with `ErrNotImplemented`.

- [ ] **Step 3: Implement**; delete the stub. `TagMatchesAfter` with an empty `channelIDs` returns an empty slice without querying. The `IN (?)` lists expand with `sqlx.In` before `Rebind`.

- [ ] **Step 4: Run the suite and the gate**

As Task 4 Step 4. Expected: no `--- FAIL`; `verify: OK`.

- [ ] **Step 5: Commit**

```bash
git add internal/store/sqlstore internal/conformance/store
git commit -m "feat(store): identity-level subscriptions with acked marks, tag subscriptions and tag matches

Ports Subscribe and Unsubscribe from v1 internal/bus/receive.go and
SubscribeTags, UnsubscribeTags, tagSets and tagCond from v1
internal/bus/tags.go (v1 ADR 0009), keyed by identity (Decision 37) with
the acked mark of Decision 75 and the readable-channel scoping of
Decision 95."
```

---

### Task 9: Memories, access, revisions, task-list queries

**Depends on:** 6.

**Files:**
- Delete: `internal/store/sqlstore/memories_stub.go`, `internal/store/sqlstore/tasks_stub.go`
- Create: `internal/store/sqlstore/memories.go`, `internal/store/sqlstore/tasks.go`
- Test: `internal/conformance/store/memories_test.go`, `internal/conformance/store/tasks_test.go`

**Port from v1:** `internal/bus/memories.go:31-80` (`GetMemory`, `MemoryRevisions`, `liveRevision`), `memories.go:260-280` (the edit transaction's row writes: tombstone the old revision, insert the new one, `SetMemory`, touch access; the sequence itself is the core's), `memories.go:400-440` (`tombstoneMemoryRow`, `TouchMemoryAccess`), `internal/bus/maintenance.go:287-330` (`memoryExpiryChunk` → `ExpiredMemories`), `internal/bus/tasks.go:33-40` (`taskLastActivitySQL`), `tasks.go:168-200` (`loadTasks` → `LiveMemories`). Not ported: task JSON handling, ranks, links, reclaim (Milestone 3 core).

**Interfaces:** consumes Task 1's `Memories`, `Tasks`, `MemoryRef`; Task 6's `scanMessages`, `TombstoneMessage`.

- [ ] **Step 1: Write the failing conformance tests**

`memories_test.go`: `TestMemoryRevisionsAndLive` (append seq 5, `SetMemory(5, 5, 1)`; append seq 6, `SetMemory(6, 5, 2)`, `TombstoneMessage(5)`: `LiveMemory(5).Seq == 6`, `MemoryRevisions(5)` is `[5, 6]` by revision; tombstone 6 → `LiveMemory` `ErrNotFound`), `TestMemoryAccess` (touch two ids, touch one again with a later time, `ExpiredMemories(before, limit)` lists live memories whose access is older, ascending; `DeleteMemoryAccess`), `TestExpiredMemoriesIgnoresTombstoned`.

`tasks_test.go`: `TestLiveMemoriesOnChannel` (live memory rows on the channel only, ordinary rows and tombstoned rows excluded, ascending seq), `TestChannelLastActivity` (empty channel → `created_at`; after a message → its `created_at`; after a tombstone with a later `tombstone_at` → that), `TestTasksKindIsStored` (a channel of kind `tasks` is listed by `ListChannelsByKind(store.KindTasks)` and `UnembeddedMemories` skips its memories: the Decision 90 replacement of `IsTaskChannel`).

- [ ] **Step 2: Run to verify they fail**

Run: `go test -count=1 -p 1 -run 'TestMemory|TestExpiredMemories|TestLiveMemories|TestChannelLastActivity|TestTasksKind' ./internal/conformance/store/`
Expected: `--- FAIL` with `ErrNotImplemented`.

- [ ] **Step 3: Implement**; delete the two stubs.

- [ ] **Step 4: Run the suite and the gate**

As Task 4 Step 4. Expected: no `--- FAIL`; `verify: OK`.

- [ ] **Step 5: Commit**

```bash
git add internal/store/sqlstore internal/conformance/store
git commit -m "feat(store): memory revisions, access touches and task-list queries

Ports GetMemory, MemoryRevisions, tombstoneMemoryRow and
TouchMemoryAccess from v1 internal/bus/memories.go, memoryExpiryChunk
from v1 internal/bus/maintenance.go, and loadTasks and
taskLastActivitySQL from v1 internal/bus/tasks.go; task lists are
channels of the stored kind tasks (Decision 90; v1 ADR 0013)."
```

---

### Task 10: Search: query parser and both text-search translations

**Depends on:** 6.

**Files:**
- Create: `internal/search/parse.go`, `internal/search/parse_test.go`
- Modify: `internal/store/sqlite/sqlite.go` (the dialect's `TextSearch`), `internal/store/postgres/postgres.go` (the dialect's `TextSearch`)
- Create: `internal/store/sqlite/search_test.go`, `internal/store/postgres/search_test.go` (pure: the translation strings and args)
- Test: `internal/conformance/store/search_test.go`

**Port from v1:** `internal/bus/search.go:50-91` (`searchTerms`: fields, trailing stars mark a prefix, star-only words dropped; `ftsQuery`: every term quoted with `"` doubled, `*` after a prefix term, terms joined by spaces for implicit AND), `search.go:145-182` (`textSearch`: `FROM messages_fts JOIN messages m ON m.seq = messages_fts.rowid WHERE messages_fts MATCH ? AND m.tombstone = 0`; `bm25` ranking is not part of the contract, Decision 51). Phrases are new (Decision 51).

**Interfaces:** consumes Task 1's `search.Query`/`Term`, Task 2's `Dialect.TextSearch`, Task 6's `SearchText` (which already calls the dialect and returns its error). Produces `search.Parse(q string) Query`: whitespace-separated words; a word ending in one or more `*` is a prefix of the word without them; a `"`-delimited run is one phrase term (its inner words kept in order; a star after the closing quote is dropped; an unterminated quote runs to the end); a term left with no letter or digit (`!!!`, `*`, `""`) is dropped (Review Focus 1).

- [ ] **Step 1: Write the failing tests**

`parse_test.go`: `TestParse` table: `rate limit` → two words; `e159*` → prefix `e159`; `commit:e159e8c` → one word; `"rate limit" hit` → phrase `rate limit` + word `hit`; `"rate limit"*` → phrase only; `** !!! a` → word `a`; `""` and `"  "` → empty; `"unterminated phrase` → phrase `unterminated phrase`; `Hello` keeps case (the engines lowercase).

`sqlite/search_test.go`: `TestTextSearchSQLite`: for `Parse("rate limit e159* \"commit hash\"")` the condition is `m.seq IN (SELECT rowid FROM messages_fts WHERE messages_fts MATCH ?)` with the single arg `"rate" "limit" "e159"* "commit hash"`; a term containing `"` is doubled; an empty query → `ErrInvalid`.

`postgres/search_test.go`: `TestTextSearchPostgres`: the same query gives `m.fts @@ (plainto_tsquery('simple', ?) && plainto_tsquery('simple', ?) && to_tsquery('simple', ?) && phraseto_tsquery('simple', ?))` with args `rate`, `limit`, `'e159':*`, `commit hash`; a prefix containing `'` is doubled inside the quotes (`'it''s':*`); an empty query → `ErrInvalid`.

`conformance/store/search_test.go`: `TestSearchTextMatchesAgreeAcrossEngines`: one corpus (fixed slice of `{channel, subject, content}`) appended on channels A and B; a table of queries with the expected set of content ids, asserted identically on both engines (Decision 51 "which messages match, not ranking"): `rate` (word in subject or content, case-insensitive: `Rate` matches); `e159*` (prefix matches `e159e8c`, not `d159`); `"rate limit"` (adjacent in order only); `rate limit` (both words anywhere); `commit:e159e8c` (matches the message containing it); `café` (matches the `café` message on both; the test's comment records that `cafe` would match only on SQLite, Review Focus 2); `missing` (empty). Also: `channelIDs = [A]` excludes B's hits (Decision 95 scoping); a tombstoned match is excluded; `limit` honored; `search.Query{}` → `ErrInvalid` on both.

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/search/ ./internal/store/sqlite/ ./internal/store/postgres/ && go test -count=1 -p 1 -run TestSearchText ./internal/conformance/store/`
Expected: the first fails to build (`undefined: Parse`); the conformance test fails with `ErrNotImplemented`.

- [ ] **Step 3: Implement** `Parse` and both `TextSearch` methods. SQLite's condition uses the `IN (SELECT rowid ...)` subquery so `SearchText`'s one query shape (`SELECT <messageCols> FROM messages m WHERE <cond> AND m.tombstone = FALSE AND m.channel_id IN (...) ORDER BY m.seq LIMIT ?`) is engine-independent.

- [ ] **Step 4: Run the tests, the suite and the gate**

Run: `make test` then `env -u AGENTBUS2_TEST_POSTGRES_DSN go test -count=1 -p 1 -v -run TestSearchText ./internal/conformance/store/ 2>&1 | grep -E '^(--- |ok|FAIL)'`, then `make verify` (600000 ms).
Expected: `--- PASS` for `/sqlite` and `/postgres`; `verify: OK`.

- [ ] **Step 5: Commit**

```bash
git add internal/search internal/store/sqlite internal/store/postgres internal/conformance/store
git commit -m "feat(search): core query language with FTS5 and Postgres tsquery translations

Ports searchTerms and ftsQuery from v1 internal/bus/search.go (v1 ADR
0019) into internal/search and the SQLite dialect, adds phrases, and
translates the same query to to_tsquery/plainto_tsquery/phraseto_tsquery
over a simple-configuration tsvector on Postgres (Decision 51; Milestone
2 choice 1). The conformance corpus checks which messages match on both
engines, not ranking."
```

---

### Task 11: Integration, milestone exit and docs

**Depends on:** 4, 5, 6, 7, 8, 9, 10.

**Files:**
- Delete: `internal/store/sqlstore/errors.go` (`ErrNotImplemented`; no stub may survive the build)
- Modify: `internal/conformance/store/seq_test.go` (switch the concurrent-writer test to `AppendMessage`), `internal/conformance/store/store_test.go` (keep `TestScaffold`; add `TestStoreImplementsEveryAggregate`), `CLAUDE.md`, `README.md`, `docs/adr/0004-store-contract-and-storage.md` (addendum), `docs/superpowers/specs/2026-10-19-agentbus2-design.md` (sections 5.4 and 5.8 placeholders), `HANDOFF.md` (untracked)

- [ ] **Step 1: Remove the stub sentinel and prove no stub survived**

Delete `errors.go`; `go build ./...` must pass with no reference to `ErrNotImplemented` left (`rg -n ErrNotImplemented internal/` prints nothing; the dialects' `TextSearch` placeholders were replaced in Task 10). Add `TestStoreImplementsEveryAggregate` in `store_test.go`: for each engine, open a store and, inside one `Append`, call one cheap method of every aggregate interface (`MaxSeq`, `ListIdentities`, `Receipt`, `ListChannels`, `Subscriptions`, `ListSessions`, `MessagesAfter`, `MemoryRevisions`, `LiveMemories`) and require no error: a regression that re-introduces a stub fails here on both engines.

- [ ] **Step 2: Concurrent writers through `AppendMessage`**

Rewrite `seq_test.go`'s writers to `AppendMessage` on three channels (every writer uses its own store from `reopen`), and the reader to one `Read` per poll: `m := MaxSeq()`, then `MessagesAfter(channel, 0, 1000)` for each channel; the union of the seqs returned must be exactly `1..m` (contiguous, nothing above `m`) and each channel's list ascending; 400 appends; no error escaped; postgres under 30 s. Run: `env -u AGENTBUS2_TEST_POSTGRES_DSN go test -count=1 -p 1 -race -run TestSeq ./internal/conformance/store/` (the race detector works with `CGO_ENABLED=0` on this toolchain, Milestone 1 Task 9). Expected: `ok`.

- [ ] **Step 3: The milestone exit, positive**

Run (600000 ms timeout or in the background): `env -u AGENTBUS2_TEST_POSTGRES_DSN make verify 2>&1 | tee /tmp/agentbus2-m2-verify.log | tail -3` and `env -u AGENTBUS2_TEST_POSTGRES_DSN make test-race 2>&1 | tail -4`.
Expected: `verify: OK`; the log holds `ok  	github.com/ericfitz/agentbus2/internal/conformance/store` with no `--- FAIL` and no `SKIP`; `test-race` prints `ok` for every package. Then `env -u AGENTBUS2_TEST_POSTGRES_DSN go test -count=1 -p 1 -v ./internal/conformance/store/ 2>&1 | grep -cE '^\s+--- PASS: .*/(sqlite|postgres)$'` prints an even number at least twice the test count, and `grep -E 'TestOpen(Empty|Refuses|Migrates|Failed|Rejects|Concurrent)' | grep -c PASS` shows the Decision 34 rules passing on both engines (the exit criterion's second clause).

- [ ] **Step 4: The milestone exit, negative halves**

1. Unreachable Postgres fails the Store suite, not skips: `AGENTBUS2_TEST_POSTGRES_DSN='postgres://agentbus2:agentbus2@127.0.0.1:1/agentbus2?sslmode=disable&connect_timeout=2' make verify 2>&1 | grep -E 'unreachable|conformance/store|verify: OK|^make'; echo "exit=${PIPESTATUS[0]}"`. Expected: `FAIL	github.com/ericfitz/agentbus2/internal/conformance/store`, the `is unreachable` text, no `verify: OK`, non-zero exit.
2. The suite is load-bearing per engine: a scratch script (not committed; `bash`, `set -euo pipefail`) copies `internal/store/postgres/postgres.go` through `sed 's/phraseto_tsquery/plainto_tsquery/'` to a temp file and `cp`s it back, runs `env -u AGENTBUS2_TEST_POSTGRES_DSN go test -count=1 -p 1 -v -run TestSearchText ./internal/conformance/store/ 2>&1 | grep -E '^\s+--- (PASS|FAIL): TestSearchText'`, then `git checkout -- internal/store/postgres/postgres.go`. Expected: `--- FAIL: TestSearchTextMatchesAgreeAcrossEngines/postgres` (the phrase case) and `--- PASS: .../sqlite`.
3. The same for SQLite: `sed 's/"\*/"/'` on the prefix marker in `internal/store/sqlite/sqlite.go`'s `TextSearch` (adapt the pattern to the implementation) → `/sqlite` fails on the prefix case, `/postgres` passes; revert.

- [ ] **Step 5: Docs**

- `CLAUDE.md`: under Design, add one line: the Store contract is `internal/store`; the engines are `internal/store/sqlite` and `internal/store/postgres` over `internal/store/sqlstore`; the Store suite is `internal/conformance/store` and isolates Postgres tests in a schema per test. Under Commands: `make test` covers the SQLite-only engine tests; the suites need Docker or the variable, as before. Facts only, checked against the tree (Definition of Done E).
- `README.md`: the status line becomes `Milestone 2 (Store and engines) done; nothing usable yet`.
- `docs/adr/0004-store-contract-and-storage.md`: append a section `## Milestone 2 choices (Claude, 2026-10-21; delegated by Decisions 51 and 80)` with choices 1 and 2 from this plan's "Choices" section, each with its rationale and the probe results, clearly labeled as Claude's choices under the spec's delegation, not Eric's decisions.
- Spec: in section 5.4 replace `the Postgres equivalent, chosen in Milestone 2` with `the Postgres equivalent, chosen in Milestone 2: a simple-configuration tsvector queried with to_tsquery, plainto_tsquery and phraseto_tsquery (ADR 0004, Milestone 2 choices)`; in section 5.8 replace `the list is stored as three boolean columns or a constrained set, chosen in Milestone 2` with `the list is stored as three boolean columns per entry with CHECK (can_read OR can_write OR can_admin), chosen in Milestone 2 (ADR 0004, Milestone 2 choices)`. Nothing else in the spec changes.
- `HANDOFF.md` via `/session:handoff`: Milestone 2 done at the Step 6 commit, unpushed; the Step 3 and 4 outputs; the pending-Eric list from this plan; Milestone 3 next.

- [ ] **Step 6: Commit**

```bash
git add internal/store/sqlstore internal/conformance/store CLAUDE.md README.md docs/adr/0004-store-contract-and-storage.md docs/superpowers/specs/2026-10-19-agentbus2-design.md
git commit -m "feat(store): Milestone 2 exit: full Store suite on both engines, concurrent writers, docs

Removes the stub sentinel, switches the Decision 54 concurrent-writer
test to AppendMessage, records the two delegated choices (Postgres
full text, ACL storage) in ADR 0004 and the spec."
```

- [ ] **Step 7: Report**

End the report with the Step 3 outputs (positive, race, Decision 34 rule count), the Step 4 outputs (unreachable, both per-engine mutations), and the pending-Eric list. No push: the next push is Eric's (`Approve: push main to origin (git push origin main)`).

---

## Self-review notes

- Spec coverage, section 10 Milestone 2: `Store` interface (Task 1), `Tx` with the Decision 69 bounds (Tasks 2, 3), `seq_counter` (Task 2, exercised in Tasks 3, 6, 11), `schema_meta` and the Decision 34 rules each covered by a test (Tasks 2, 3, counted in Task 11 Step 3), migrations under the Decision 68 lock (Tasks 2, 3), both stores (Tasks 2, 3), channel ACL rows and the identity admin flag (Tasks 4, 5), the Store conformance suite with the concurrent-writer test (Tasks 3, 11) and the ACL cases (Task 5, one test per section 5.8 case). Section 5.5's `NotifyAppend` hook and section 5.7's lease row are shaped for Milestone 7 (Tasks 2, 6). Milestone 3's needs: delivery rows (Task 7), receipts (Task 4), tags and tag subscriptions (Tasks 6, 8), leases (Task 2), memories and tasks (Task 9), ACL queries (Task 5), search (Task 10).
- Decisions traced: 2, 5, 6, 25, 26, 27, 29, 30, 31, 34, 49, 50, 51, 52, 53, 54, 68, 69 (ADR 0004); 80, 88, 90, 92, 93, 94 (names and kinds visible: `ListChannels` needs no ACL), 95, 96 (ADR 0008); 16, 37, 40, 42, 75, 79 (ADR 0003); 55, 57, 73 (ADR 0002); 67 (ADR 0001); 32 (no v1 path); 14, 15, 39, 41, 78 (what is deliberately not stored on the session).
- Names used across tasks (checked against `main` at `ac1ea8d` on 2026-10-21: `dbtest.Engine.DSN`, `Engine.Open`, `ForEachEngine`, `Run`, `internal/conformance/store/{main_test.go,store_test.go}` with `TestScaffold`, `make test-conformance`, `PKGS_UNIT`): `store.Store`, `store.Tx`, the nine aggregate interfaces, `ErrNotFound`/`ErrConflict`/`ErrInvalid`/`ErrRetryable`/`ErrNotAppend`/`ErrReadOnly`/`ErrNotAgentbus2`/`ErrUpgradeNeeded`/`ErrSchemaInvalid`, `sqlstore.Dialect`/`Migration`/`Options`/`Open`/`ErrNotImplemented`, `sqlite.Open`/`DSN`/`ApplicationID`/`Migrations`, `postgres.Open`/`WithSearchPath`/`Migrations`/`TxAttempts`, the conformance helpers `openStore`/`reopen`/`rawDSN`, `scanMessages`, `IsConflict`; the test names listed per task. `ctx context.Context` is implied on every method.
- Proportion: the plan fixes the DDL (exact copy the implementers cannot invent independently), the interface signatures and the test names and assertions; bodies are left to the implementers except the Decision 34 order, the retry loop's classification rule and the two search translations.
- Verified against sources on 2026-10-20: modernc.org/sqlite v1.60.1 `tx.go:23` (ReadOnly uses a plain `begin`; `_txlock` sets `beginMode`), `error.go:21` (`Code()`), `lib/sqlite.go:3108,3938` (BUSY 5, LOCKED 6); pgx v5.11.0 `pgconn/config.go:498-536` (unknown DSN keys become runtime params); `postgres:17.11` probes for the tsquery shapes, `hashtext(current_schema())` as an advisory key, `search_path` through pgx, and the `LOCK TABLE` effect on 40001 (0 of 400 with the lock, 1,843 retries and 49 give-ups without).
