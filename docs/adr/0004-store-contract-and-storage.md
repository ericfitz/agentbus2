# ADR 0004: Store contract and storage

Status: Accepted (Eric, 2026-10-19)

Covers Decisions 2, 5, 6, 7, 25, 26, 27, 28, 30, 34, 48, 49, 50, 51, 52,
53 and 54 of `HANDOFF.md`, all made by Eric on 2026-10-19, and Decisions
68 and 69 (Eric, 2026-10-20; see "Decisions of 2026-10-20" below).

## Context

v1 is SQLite only. `internal/bus/schema.go` defines 13 `CREATE TABLE` plus the
`messages_fts` virtual table (FTS5) and its triggers; `seq` is `INTEGER PRIMARY KEY AUTOINCREMENT`
(`internal/bus/schema.go:84`); every write runs in an immediate
transaction (`_txlock=immediate`, `internal/bus/bus.go:111`); embeddings
are blobs scored by `dot()` in `embed.go` and `search.go`; and the rules
(rate limits, receipts, task leases, tag matching) are methods on the one
`Bus` struct that also owns the database handle.

agentbus2 adds Postgres (ADR 0001, Decision 2), and the server is the only
process that opens the store. The rules therefore have to live once, above
a persistence boundary that two engines can implement, and the properties
v1 got from SQLite's single writer (commit order equals `seq` order, no
lost updates) have to be stated as a contract so Postgres provides them
too.

## Decision

Eric decided:

1. **Two seams: `Bus` (client-facing) and `Store` (persistence). Rules
   live once in a core over `Store`**, never in the client. (Decision 5)
2. **Every method takes a `context.Context`.** (Decision 6)
3. **The `Bus` interface is split by consumer**, for example messaging,
   tasks, memories, sessions, admin. (Decision 7)
4. **Admin is a separate interface and API section with admin-only
   authorization** (reset, delete or rename a channel, usage, status).
   (Decision 48; superseded in part by Decision 80, Eric, 2026-10-20,
   which puts channel metadata management under the channel's `admin`
   permission, ADR 0008; admin bootstrap is Decision 73, ADR 0002)
5. **Backends are SQLite and Postgres.** Redis was considered and dropped.
   (Decision 2)
6. **Store boundary, Option C (hybrid):** repository methods grouped by
   aggregate, implemented once in portable SQL, with dialect hooks only
   where the engines really differ (FTS, vectors, placeholders, identity
   columns, blob types). (Decision 25)
7. **The transaction contract:** `Store.Tx(ctx, fn)` is serializable, may
   run `fn` more than once, and `fn` must have no side effects. On SQLite,
   immediate transactions provide this. On Postgres, use `SERIALIZABLE` and
   retry on SQLSTATE 40001. (Decision 26)
8. **`seq` comes from a counter row** (`UPDATE seq_counter SET v=v+1
   RETURNING v` inside the append transaction), so commit order matches
   `seq` order. (Decision 27)
9. **Libraries:** `database/sql` with `modernc.org/sqlite` and `pgx`
   (through its stdlib adapter; a native `pgx` connection only for
   `LISTEN`), plus `sqlx` for struct scanning and `Rebind`. No ORM, no
   sqlc. `pgx` keeps `CGO_ENABLED=0`. (Decisions 50, 53)
10. **Search:** the core defines the query language (words, `word*`
    prefix, phrases). Each store translates it, and conformance tests check
    which messages match, not ranking. Vectors are scored in Go; pgvector
    is an optional capability. (Decision 51)
11. **Notifier:**
    - Postgres: `NOTIFY` inside the append transaction, so it is delivered
      on commit;
    - SQLite server: an in-memory broadcast after commit;
    - local multi-process SQLite: polling.

    Notifications are hints: register interest, re-check the store, then
    block. (Decision 52)
12. **Server internals:** maintenance runs only on the server; each job is
    idempotent, plus `pg_try_advisory_lock` so usually only one replica
    runs it; memory-access touches happen inside `Search`; the clock is
    injected into the core; leases tolerate a few seconds of clock skew.
    (Decision 49)
13. **The database has a schema version.** (Decision 30) It is a
    `schema_meta` table (`app='agentbus2'`, `schema_version`, `engine`,
    `created_at`). On open:
    - wrong or missing `app` on a non-empty database: refuse;
    - a newer version: refuse with "upgrade agentbus2";
    - an older version: migrate under a lock;
    - anything else: reject.

    SQLite also sets a distinct `PRAGMA application_id`. (Decision 34)
14. **Migrations:** one set per engine, numbered in step from version 1,
    applied at server start under a lock. (Decision 53)
15. **Conformance:** suites at the Store level and at the core/`Bus`
    level, each run against both engines, including a test with concurrent
    writers for `seq` ordering. (Decision 54)
16. **`make verify` requires Postgres.** Postgres for `make verify` comes
    from testcontainers (Docker), with a DSN environment-variable override
    and a Postgres service container in CI. A run that skips the Postgres
    suites is not a pass (project `CLAUDE.md`, done gate).
    (Decisions 28, 53)

## Consequences

Positive:

- The rules exist once, in the core, and are exercised against both
  engines by the same conformance suites; an engine-specific regression
  shows up as a suite failure, not as a behavior difference in production.
- The counter row makes `seq` a commit-ordered sequence on both engines,
  so a session cursor can never skip a message whose transaction committed
  later than one with a higher `seq`.
- A retryable, side-effect-free `fn` makes Postgres serialization failures
  invisible to the core; the same contract is what SQLite's immediate
  transaction already gives.
- Notifications as hints mean a lost `NOTIFY` or broadcast costs latency,
  never a missed message: the store is always re-checked.
- `CGO_ENABLED=0` holds for both engines, so release builds stay plain Go
  cross-compiles.
- `schema_meta` with `app` and `PRAGMA application_id` stop agentbus2 from
  opening a v1 database or any other SQLite file by mistake (ADR 0005).

Negative:

- A single counter row serializes every append on both engines; that is
  the price of commit-ordered `seq`, and throughput is bounded by it.
- `fn` may run more than once, so any non-transactional side effect (a
  clock read, a log line, the SQLite in-memory broadcast) has to be moved
  outside `Tx` or made idempotent; this is a rule every contributor has to
  know. Postgres `NOTIFY` is transactional and stays inside the append
  transaction, while the SQLite broadcast happens after commit
  (Decision 52).
- Two migration sets must be kept in step by hand; a schema change is two
  files and a conformance run against both engines.
- `make verify` needs Docker or a reachable Postgres, so the done gate is
  heavier than v1's `go test`.
- Portable SQL with dialect hooks means the common path is written for the
  intersection of the two engines; an engine-specific feature (pgvector,
  FTS5 tokenizers) is a capability, not a baseline.
- Vectors scored in Go keep both engines equal but do not scale like
  pgvector; pgvector is optional and the capability is advertised by
  discovery (ADR 0001).

## Alternatives considered

- **Redis** as a backend was considered and dropped (Decision 2).
- **A Postgres sequence for `seq`** was rejected: a sequence is assigned
  before commit, so a cursor could skip a message whose transaction commits
  late (Decision 27).
- **An ORM or sqlc** were rejected in favor of `database/sql`, `pgx`,
  `modernc.org/sqlite` and `sqlx` (Decision 50).
- The Store boundary was chosen as **Option C, hybrid**, over the other
  options discussed in the design conversation; HANDOFF records only the
  chosen option.

## Decisions of 2026-10-20 (Eric)

From Eric's answers to the design spec's section 12
(`.local/review-answers-2026-10-20.md`):

- **Decision 68.** In local multi-process SQLite mode all cross-process
  state is in the shared database: session leases, a watch registration
  per session, a maintenance lease row (v1 `takeLease`, ported) that
  replaces the advisory lock, and migrations in an immediate transaction
  that re-reads `schema_version`. `internal/filelock` is not ported.
  (Question 5; also ADR 0001)
- **Decision 69.** `Tx` retry bounds: Postgres retries SQLSTATE 40001 up
  to a small fixed count with jittered backoff; SQLite relies on the
  immediate transaction and a 5 s busy timeout with no further retry; both
  then surface a `retryable` error. (Question 6)

Decision 80 (ADR 0008) adds ACL entries to the channels aggregate and
cases to the Store conformance suite (spec section 5.8).

## Open questions for Eric

All three resolved on 2026-10-20:

1. Maintenance in local multi-process mode: whichever process takes the
   lease row runs the jobs (Decision 68).
2. The SQLite migration lock: an immediate transaction re-reading
   `schema_version` (Decision 68).
3. `Tx` retries: bounded on Postgres, none beyond the busy timeout on
   SQLite (Decision 69).

## Milestone 2 addendum (2026-10-21)

### Choices delegated by the spec

These are Claude's choices under the spec's delegation (Decisions 51 and
80), not Eric's decisions.

1. **Postgres full-text equivalent of FTS5 (Decision 51).** Two stored
   generated columns, `fts_subject` and `fts_content`, each
   `to_tsvector('simple', regexp_replace(<col>, '[^[:alnum:]]+', ' ', 'g'))`
   with a GIN index. The `simple` configuration lowercases and does not
   stem; the `regexp_replace` stops Postgres' parser from keeping
   `owner/repo`, `foo-bar`, emails or URLs as one token (probed:
   `to_tsvector('simple', 'foo-bar')` is `'bar':3 'foo':2 'foo-bar':1` raw
   and `'bar':2 'foo':1` normalized), which matches what `unicode61` does on
   the SQLite side. Word and phrase terms both use `phraseto_tsquery`; a
   prefix term applies `:*` to the last token only (a rewrite of the
   `phraseto_tsquery` text form, accepted back by `to_tsquery`). A term must
   match within one column, as an FTS5 phrase must. Probed on
   `postgres:17.11` and modernc.org/sqlite v1.60.1 on 2026-10-21: every
   query of the conformance corpus returns the same set on both engines.
   Rejected: `plainto_tsquery` (AND anywhere, so `commit:e159e8c` matches
   `commit abc e159e8c` on Postgres only; proven by gate self-test block
   `search-postgres-phrase`), prefix on every token, one concatenated column
   (`"rate limit"` would match subject `rate` plus content `limit hit`),
   `pg_trgm`/`ILIKE` (no token semantics) and a stemming configuration.
2. **ACL permission storage (Decision 80).** Three boolean columns per
   stored entry, `channel_acl(id, channel_id, identity, can_read, can_write,
   can_admin)` with `CHECK (can_read OR can_write OR can_admin)`. One row is
   one entry, so a stored ACL round-trips as given (several rows may name
   one identity; `EffectivePermissions` takes the union), "removing the
   entries for `admins`" is a `DELETE` by identity, and the database
   enforces the non-empty rule as well as Go. One row per
   `(channel, identity, permission)` cannot represent "an entry".

### Gate 0 answers (Decision (Eric, 2026-10-21))

Eric did not answer before execution; each item proceeded on the plan's
recommendation (option A) with a `// pending Eric: Gate 0 item N` marker.
Eric decided every item on 2026-10-21
(`docs/superpowers/specs/2026-10-21-gate-0-decisions.md`, "Milestone 2");
the markers are removed as the decisions land.

1. Reserved identity names: `CreateIdentity` refuses `everyone` and `admins`
   with `ErrInvalid`. Decision (Eric, 2026-10-21): as built. Tests:
   `internal/conformance/store/identities_test.go`.
2. `40P01` is retryable alongside `40001`. Decision (Eric, 2026-10-21): as
   built. Tests: `internal/store/postgres/dialect_test.go`.
3. Retry constants: `TxAttempts = 5`, backoff before attempt n a uniform
   random duration in `[0, 10ms * 2^(n-1))` capped at 200 ms. Decision
   (Eric, 2026-10-21): as built. Tests:
   `internal/store/postgres/dialect_test.go`,
   `internal/conformance/store/tx_test.go`.
4. v1 `meta` is not ported (the admin reset is designed with the admin
   section); `notices` is ported as is. Decision (Eric, 2026-10-21): as
   built.
5. Rate-limit buckets stay in memory per server process, as in v1.
   Decision (Eric, 2026-10-21): as built; the Milestone 7 operations doc
   states the per-replica bound (N replicas each grant the full
   per-identity rate).
6. The Postgres database must use a UTF-8 ctype. Decision (Eric,
   2026-10-21): **changed** (option B). `Open` reads `pg_database.datctype`
   and refuses a non-UTF-8 database with `ErrSchemaInvalid`, an extension
   of the Decision 34 open rules; a Store change plus a conformance test on
   the open path. The deployment docs (Milestone 7) state the requirement
   and that the `postgres` image's default locale satisfies it.

### Shapes later milestones lean on

- The resume token is stored hashed (`sessions.resume_token_hash`). The Store
  stores and compares the hash it is given and computes nothing; Milestone 6
  fixes the token format and the hash (SHA-256 hex expected).
- `ChannelCreate.ACL == nil` means "without an ACL" and the Store
  substitutes `DefaultACL(Creator)`; an empty non-nil slice means "with an
  empty ACL" and stores no rows. This satisfies the section 5.8 conformance
  wording directly.
- `channels.creator` is stored as `''`, never NULL.
- The Postgres database needs a UTF-8 ctype: `[[:alnum:]]` is evaluated
  under it, and under `C` non-ASCII letters would split tokens.
- Residual difference, accepted: `unicode61` treats Unicode categories L*,
  N* and Co as token characters while `[[:alnum:]]` follows the C library's
  `iswalnum`, so private-use and some symbol-class code points may tokenize
  differently. No corpus row exercises them.
- Postgres `message_tags.tag` is `TEXT COLLATE "C"`, the one deviation from
  the plan's DDL substitutions: a tag pattern `p*` is the range
  `BETWEEN p AND p~`, which SQLite compares byte by byte (`BINARY`); the
  `C` collation gives Postgres the same byte order instead of the database
  collation (such as `en_US.utf8`). The stored `tag_subscription_tags.lo`
  and `hi` are therefore bound as query parameters, not joined as columns,
  so Postgres compares them under the tag column's `C` collation instead
  of failing on a collation conflict.
- Every caller-supplied string bound to a TEXT column passes `TextRule`
  (valid UTF-8, no NUL) before the query on both engines: the caller's own
  data on a write is `ErrInvalid`, a string naming another row answers as
  for an unknown row. The query helpers refuse any remaining failing string
  argument with `ErrInvalid`, so no such string reaches either engine.

## Milestone 3 addendum (2026-10-21)

- **Store amendment (choice 11).** Own-message exclusion and the search
  filters are SQL, not Go post-filters over a page: `MessagesAfter`,
  `MessagesInRange` and `TagMatchesAfter` take a
  `MessageFilter{ExcludeSender, ExcludeSessionID}`; `MessagesBefore` serves
  history's newest-first window; `TagMatchesAfter` takes an upper bound for
  bounded redelivery; `SearchText` and `Embeddings` take a `SearchFilter`;
  `MessagesBySeqs` loads semantic hits; `EmbedFailures` reads the
  rejected-row count and latest reason for `Admin.Status`. Both engines,
  with Store conformance cases.
- **`core.RowRejected`** is the embedder's row-refusal contract: an embedder
  that refuses one row returns an error wrapping `*core.RowRejected`
  (`IsRowRejected`), the batch records the failure and goes on instead of
  failing whole.

### Milestone 3 Gate 0 answers (Decision (Eric, 2026-10-21))

3. **Two spec 5.7 jobs are deferred to Milestone 7**: budget eviction and
   capacity notices (and the usage figure, its own admin operation) need an
   engine-specific size query the Store does not have. The Milestone 3 job
   list ships without them. Decision (Eric, 2026-10-21): ratified
   (`docs/superpowers/specs/2026-10-21-gate-0-decisions.md`, Milestone 3
   item 3 and Milestone 7 item 2): Milestone 7 adds `Store.Usage` and the
   two jobs, which act on SQLite only (where `Usage.Evictable` is true);
   Postgres eviction is issue #7. Test until then:
   `TestMaintenanceRunJobRejectsUnknown`, which asserts both names are
   `validation` and loses its `pending Eric: Milestone 3 item 3` marker in
   Milestone 7.

Milestone 7 item 4 (Decision (Eric, 2026-10-21), option B) adds a Store
table this ADR will carry: `assertion_jti`, a version 3 migration on both
engines, consulted and written in the token transaction so a client
assertion is single-use across replicas, with its rows expired by a
maintenance job. Milestone 5 item 2 (Decision (Eric, 2026-10-21)) adds the
`signing_keys` row (version 2, beside the enrollment tokens): the server's
private signing key as plaintext PEM, so database read access is
equivalent to admin.
