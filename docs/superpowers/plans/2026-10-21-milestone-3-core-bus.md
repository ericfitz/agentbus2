# Milestone 3: Core and Bus Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The rules of spec sections 3.4, 3.5, 4.3 to 4.7, 5.4, 5.5 and 5.7, ported from the v1 sources of section 9, implemented once in a core over the Milestone 2 `Store`: channels and kinds with the channel ACL check for every row of the section 4.6 table (Decision 80), subscriptions and tag subscriptions, send with subjects, tags, receipts and per-identity rate limits (Decisions 46, 67, 86), delivery with batch tokens, session-scoped cursors and the per-identity acked mark (Decisions 16, 40, 75), sessions with leases, display names, resume tokens and offered cursors (Decisions 15, 39, 40, 44, 79), the watch wake primitive with replaced semantics and the notifier (Decisions 52, 68), memories, tasks with leases, reclaim and the owner registry check (Decision 81), search with the core query language and Go `dot()` scoring (Decision 51), and the maintenance jobs under the store lease (Decision 49); the `Bus` interfaces split by consumer (Decision 7) with domain types; a local in-process `Bus` over the core; and the core/`Bus` conformance suite on both engines with one refusal and one success test per section 4.6 row.

**Architecture:** `internal/bus` holds the consumer-facing seam: the five interfaces (`Messaging`, `Sessions`, `Memories`, `Tasks`, `Admin`), the domain types, and the `Error` with `code` and `retryable` ported from v1 `errors.go`; it imports neither `store` nor `core`, so clients never see storage types (Decision 47). `internal/core` is the base every rule shares: `Config` with the v1 defaults, the injected clock (Decision 49), the per-identity limiter, receipts, the ACL check, the append and tombstone helpers, the store-error mapping and the notifier seam (`internal/core/notify` holds `Hint`, `Notifier`, the in-memory broadcast and the polling form, Decision 52). The rules themselves are one package per aggregate under `internal/core/` (`channels`, `sessions`, `messaging`, `memories`, `tasks`, `search`, `delivery`, `maintenance`), each a `Service` over `*core.Core` whose methods take the already-authenticated identity explicitly (authentication is Milestone 5). `internal/bus/local` binds a `*core.Core` to one identity and implements the `Bus` interfaces by delegation; Milestone 4's handlers call the same `Service` methods, and the suite in `internal/conformance/bus` drives the interfaces through a factory Milestone 4 swaps for the generated client over the in-memory transport.

**Tech Stack:** Go 1.27, `CGO_ENABLED=0`; the Milestone 2 Store (`internal/store`, `internal/store/sqlstore`, `internal/store/sqlite`, `internal/store/postgres`, `internal/search`); `internal/dbtest` and testcontainers from Milestone 1; `log/slog`; `crypto/rand`, `crypto/sha256`; `net/http` for the embedder port. No new module dependencies.

**Spec:** `docs/superpowers/specs/2026-10-19-agentbus2-design.md` sections 3.3 to 3.5, 4.3 to 4.7, 5.4, 5.5, 5.7, 8, 9 and 10 (Milestone 3); ADR 0001 (Decisions 10, 42, 45, 46, 66, 67, 68, 83), ADR 0003 (Decisions 8, 12, 15, 16, 17, 18, 37, 38, 39, 40, 44, 59, 73, 75, 77, 79, 86), ADR 0004 (Decisions 5, 6, 7, 26, 48, 49, 51, 52, 54), ADR 0006 (Decisions 13, 75, 81), ADR 0008 (Decisions 76, 80, 82, 88, 89, 90, 92, 93, 94, 95, 96), ADR 0005 (Decision 31). v1 at `/Users/efitz/Projects/agentbus` (`v1.15.0`, `33e8d34`, `internal/bus/`); the Milestone 2 plan `docs/superpowers/plans/2026-10-20-milestone-2-store-engines.md` revision 3 (`575fa8f`) for every Store signature named below.

## Task dependencies and parallelism

Tasks with no dependency between them run in parallel in separate worktrees and are cherry-picked onto `main`; every task's `Files:` list is disjoint from every task it can run beside. Each aggregate task (4 to 11) owns exactly: its `internal/core/<aggregate>/` package, its `internal/bus/local/<aggregate>.go` (replacing the stub of the same name that Task 3 creates), and its `internal/conformance/bus/<aggregate>_test.go` files with aggregate-prefixed test names. The shared files (`internal/bus`, `internal/core/*.go`, `internal/core/notify`, `internal/bus/local/local.go`, the suite's `helpers_test.go` and `fixtures_test.go`) belong to Tasks 1 and 3 alone; a gap an aggregate task finds in them is reported to the integrator, who adds it in a serial commit.

**External gate:** every task except Task 1 reads `internal/store`, so Tasks 2 to 13 start only once Milestone 2's Task 12 commit is on `main` (`make verify` green with the full Store suite). Task 1 imports nothing from `internal/store` and may run while Milestone 2 is still landing. Before Wave 1 the integrator diffs every Store signature in the "Store methods assumed" section against `main` and reports drift; Task 2 is where the five amended signatures and the three new methods (`MessagesBySeqs`, `MessagesBefore`, `EmbedFailures`) are made.

| Task | Depends on | Wave |
| --- | --- | --- |
| 1 Bus interfaces, domain types, errors, pure rule ports, notifier | none | 1 |
| 2 Store amendment: message filters, exclusion, batch loader | none (external gate) | 1 |
| 3 Core base, local Bus skeleton, suite fixtures | 1, 2 | 2 |
| 4 Channels, ACL operations, subscriptions, admin | 3 | 3 |
| 5 Sessions: open, resume, close, display names, offered cursors, identities | 3 | 3 |
| 6 Send and history | 3 | 3 |
| 7 Memories | 3 | 3 |
| 8 Tasks | 3 | 3 |
| 9 Search and embeddings | 3 | 3 |
| 10 Delivery: receive and watch | 4, 5, 6 | 4 |
| 11 Maintenance | 7, 8, 9 | 4 |
| 12 ACL matrix: every section 4.6 row | 7, 8, 9, 10 | 5 |
| 13 Integration, milestone exit, docs | 11, 12 | 6 |

Waves: 1 and 2 in parallel; 3; then 4 to 9 in parallel; then 10, 11 and 12 in parallel (12 starts once 7, 8, 9 and 10 are on `main`, which is before 11 finishes; it drives memories, tasks and search, so it cannot start from 10 alone); 13. The `notify` package is in Task 1 because it is a leaf with no `store` import; the `local` skeleton stays in Task 3 because it holds a `*core.Core`. Thirteen tasks, one above the aim, because the Store amendment (Task 2) is a reviewable unit on its own and must not be hidden inside the core base.

## Global Constraints

- Module `github.com/ericfitz/agentbus2`, `go 1.27`, `CGO_ENABLED=0`; `database/sql` only through the Store; the core never holds a `*sql.DB` (Decision 5: rules over `Store`). Every method takes a `context.Context` (Decision 6).
- Decision 26 in every core write: `Store.Tx` and `Store.Append` may run `fn` more than once on Postgres, so `fn` has no side effects. Three rules every aggregate task follows: (1) `Limits.Check` is read-only and may run inside `fn` (it needs the envelope size, which depends on rows read there); `Limits.Charge` runs only after a successful commit, never inside `fn`; (2) random values (batch tokens, resume tokens, session and watch ids) are generated once, before `fn`, and used inside it; (3) the notifier publish, the embedding kick and any log line run after `Tx` returns nil. Receipts are read and written inside `fn` (they are rows). The limiter is in memory per server process (Milestone 2 Gate 0 item 5).
- Authorization is in the core, once, for every operation of the section 4.6 table: `Core.Authorize(ctx, tx, identity, channelID, perm)` is the only ACL check, called inside the same transaction as the operation; a refusal is `bus.Error{Code: "forbidden", Retryable: false}`. Create needs no channel permission (the `ChannelCreation` switch, Decision 93); list, unsubscribe need none (Decisions 94, 95).
- The core takes an authenticated identity name; it never derives one (Decision 14) and registers none (Milestone 5). `Core.RequireIdentity` maps an unregistered name to `not_registered`. `everyone` and `admins` are never identities (Milestone 2 Gate 0 item 1).
- Unrecognized inputs are rejected with a test: an unknown `bus.Kind`, `bus.Permission`, search `Mode`, subscribe `From`, task `Status`, `Config.ChannelCreation` value, an empty ACL permission list, a tag failing the tag rule, a negative watch `Timeout` (a `Timeout` above `WatchMaxSeconds` is clamped to it, spec 3.5 "caps the wait", so Milestone 4's `timeout=` never turns into a 400), a `Count` below 1 after defaulting.
- The Milestone 1 gate checks stay in force: no `.Skip(` under `internal/conformance` or `internal/dbtest`; `make test` is Docker-free, so a core unit test that needs a database opens SQLite through `sqlite.Open` on a temp path and nothing under `internal/core` or `internal/bus` asks for Postgres; both-engine coverage lives in `internal/conformance/bus` only.
- Conformance package: every file under `internal/conformance/bus` is `package bus` (as `main_test.go` and `bus_test.go` on `main`), which shadows nothing because the contract package is imported as `ab2bus "github.com/ericfitz/agentbus2/internal/bus"` and the Store as `ab2store "github.com/ericfitz/agentbus2/internal/store"` (both aliases fixed in `fixtures_test.go`'s doc comment). Every helper and test an aggregate task adds is prefixed with its aggregate (`TestChannel`, `TestSubscription`, `TestAdmin`, `TestSession`, `TestSend`, `TestHistory`, `TestMemory`, `TestTask`, `TestSearch`, `TestReceive`, `TestWatch`, `TestMaintenance`, `TestACL`), and each task's `-run` pattern is anchored on its prefixes.
- Decision 31: every porting commit names its v1 source file; nothing reads or writes a v1 path (Decision 32). Work lands on `main`, nothing is pushed in this milestone, Conventional Commits with the session's attribution trailer.
- Pending decisions are visible in the code: a test that encodes an item of the "Claude proposal, pending Eric" list carries `// pending Eric: Milestone 3 item N` on its first line (`rg -n 'pending Eric' internal/`).
- Definition of Done D applies to `channels.Service.EnsureGeneral`, `embed.EmbedBatch`, `maintenance.Service.Tick`, session open and resume: each task says what a second run, an overlapping run and a failed run's leftovers do.

## Choices this milestone makes (delegated by the spec, recorded with rationale)

1. **Caller binding.** Core `Service` methods take `identity string` explicitly (Milestone 4's handler has it per request from the bearer token); the `Bus` interfaces carry no identity, because a `Bus` value is bound to one credential at construction (`local.New(core, identity)`; Milestone 6's remote `Bus` is built per profile credential). Session-scoped operations (`Receive`, `Watch`, `Close`) take the session id explicitly on both seams, so one `Bus` can drive several sessions of its identity (a parent and its subagents, Decision 15).
2. **Domain types are the Bus's own.** `internal/bus` defines `Kind`, `Permission`, `ACLEntry`, `Message` and the rest without importing `internal/store`; the core converts and rejects unknown values. Rationale: Decision 47 keeps storage and generated types behind the seam, and Task 1 can then land before Milestone 2 finishes.
3. **Error codes.** `bus.Error{Code, Message, Retryable}` ports v1 `errors.go` (with `MaxErrorMessageBytes` 1024 and `TruncateErrorMessage`). The code set, the enumeration Milestone 4 writes into the schema (section 3.3): `validation`, `not_found`, `conflict` (duplicate name, task owner guard, and an idempotency key reused with a different payload, as v1), `forbidden` (an ACL or admin refusal, new), `not_registered`, `rate_limited` (retryable), `replaced` (a newer watch, Decision 42), `session_lapsed` (resumable with the token), `session_closed`, `unavailable` (`store.ErrRetryable`, retryable, Decision 69), `internal` (retryable, as v1). Nothing else; a core function that needs another code reports it to the integrator.
4. **Resume token.** 32 random bytes, base64url without padding, returned once by `Open`; stored as its SHA-256 hex (`sessions.resume_token_hash`, Milestone 2 choice 12, which expected a later milestone to fix the format; the core is the generator, so it is fixed here).
5. **Display names (Decision 39).** The first free of `<identity>`, `<identity>-2`, `<identity>-3`, ...; with a hint, `<identity>/<hint>`, `<identity>/<hint>-2`, ... The hint must pass `store.NameRule` (1 to 128 bytes, no `/`, no control characters), so a display name has at most one `/`. The set of taken names is the identity's session rows with `ClosedAt == 0`: the live ones (Decision 39's "first live session") plus the lapsed-but-resumable ones, because a lapsed session that resumes keeps its name (Decision 79) and two live sessions must never share one. A closed row can never resume (`session_closed`), so closing frees the name at once: an agent that opens and closes sessions in turn is `pat` every time. Keeping the lapsed rows in the set is the one departure from a literal reading of Decision 39 and is pending item 11 (a crashed client's lapsed `pat` makes the next session `pat-2` until the retention sweep or a `Close` of the lapsed row); the hint and name never affect authorization (Decision 15). A registered identity literally named `<identity>-2` could collide in display only.
6. **Defaults for the settings the spec names without a value.** `SessionLeaseSeconds` 90 (a 50 s watch plus margin, Decision 42), `LeaseSkewSeconds` 5 (Decision 49 "a few seconds"), `SessionRetentionHours` 72 (Decision 79; v1's `cursor_idle_hours` order of magnitude), `WatchMaxSeconds` 50 (Decision 42), `WatchMergeSeconds` 2 (Decision 45 "a few seconds"), `MaintenanceIntervalSeconds` 60 (v1 `cleanup_interval_seconds`), `ChannelCreation` `"open"` or `"admins"` (Decision 93; a string so the zero value cannot silently mean "closed" and an unknown value is rejected), `DefaultACL func(creator string) []store.ACLEntry` nil meaning `store.DefaultACL` (Decision 88; Milestone 5's server configuration fills it). The v1 fields that concern the store keep their v1 defaults and bounds (`config.go`): `MessageRetentionHours` 168, `TombstoneMinHours` 72, `TaskExpiryHours` 720, `MemoryExpiryHours` 8760, `ReceiptRetentionMinutes` 60, `MaxMessageKiB` 64, `SendMessagesPerSecond` 100, `SendKiBPerSecond` 1024, `ReceiveDefaultCount` 100, `ReceiveMaxCount` 1000, `ResultDefaultKiB` 64, `EmbeddingQueryTimeoutSeconds` 10.
7. **Rate limiting across retries** (Decision 26 rule 1): `Limits.Check`, a read-only refill-and-compare that may run inside `fn`, refuses with `rate_limited` when the bucket could not pay; `Limits.Charge` after commit pays. A concurrent burst can overshoot by the in-flight calls, which v1's inside-the-transaction charge also allowed under its single writer; a retried transaction is charged once.
8. **Text search order.** The Store returns matches in ascending `seq` with no rank (Decision 51: engines agree on matching, not ranking), so text hits are returned newest first (descending `seq`) with `Score` 0; `both` mode fuses that order with the semantic ranking by reciprocal rank fusion (v1 `rrf`, k = 60) unchanged; semantic scoring is v1 `dot()` over normalized vectors. `searchPageDefault` 100 and `searchPageMax` 1000 as v1.
9. **Result bounds are core rules.** v1 `trimToBytes` (`ResultDefaultKiB`, the 4 MiB hard ceiling, always at least one item), `envelopeUpperBound` against `MaxMessageKiB`, and `maxNoticesPerReceive` 256 are ported into `internal/core/rules`, so Milestone 4 only serializes.
10. **One receipt helper for every mutating operation** (Decision 46): `Core.WithReceipt(ctx, tx, identity, key, payload, &result, fn)` replays the stored result on a hit, rejects a different payload with `conflict`, runs `fn` otherwise and stores the result; `key == ""` means no receipt. Every mutating input carries `IdempotencyKey`. Keys are scoped per identity and expire after `ReceiptRetentionMinutes` (Decision 67).
    Exempt from the key, and why: `Open` returns the resume token once and the row stores only its hash (choice 4), so a receipt would persist the plaintext token for `ReceiptRetentionMinutes`; a retried `Open` after a lost response leaves a lapsed row that the Task 11 sweep removes after `SessionRetentionHours`, and the client resumes nothing it never received. `Receive` is idempotent by construction: a retry without `Ack` redelivers the same pending batch under the same token, and a replayed `Ack` is `AckIgnored` (Decisions 16, 40). `Watch` writes only the lease and the watch id, which the next call replaces (Decision 68).
11. **Store amendment** (Task 2): own-message exclusion and the search filters are SQL, not Go post-filters over a `limit`-bounded page (a chatty sender would otherwise starve a batch or a search page): `MessagesAfter`, `MessagesInRange` and `TagMatchesAfter` take a `MessageFilter{ExcludeSender, ExcludeSessionID}`, `MessagesBefore` serves history's newest-first window, `TagMatchesAfter` an upper bound for bounded redelivery, `SearchText` and `Embeddings` a `SearchFilter{Sender, Since, Until, Thread, Patterns}`, `MessagesBySeqs` loads the semantic hits, and `EmbedFailures` reads the rejected-row count and the latest reason for `Admin.Status` (v1 `StatusReport` reads `embed_failures` directly; the core has no SQL). Both engines, with Store conformance cases, before any core task uses them.
12. **Notifier shape** (Decision 52): `notify.Hint{ChannelID, Seq int64}` (a zero `Seq` is a non-message hint: a watch registration, an ACL change); `Notifier.Publish(Hint)` and `Subscribe() (<-chan Hint, cancel func())`. `notify.Memory` fans out to subscribers through one-slot coalescing channels (a slow watcher gets one pending hint, never a backlog); `notify.Polling(interval)` delivers `Hint{}` every interval and ignores `Publish`, which is the local multi-process form (v1 `wait.go` `WaitPollInterval`, one second). Postgres `NOTIFY` is Milestone 7's third implementation.
13. **Package layout.** `internal/bus` (seam), `internal/bus/local` (in-process Bus), `internal/core` (base), `internal/core/rules` (pure ports: tags, subjects, ranks, vectors, trimming, fingerprints; no store import), `internal/core/notify`, `internal/core/{channels,sessions,messaging,memories,tasks,search,delivery,maintenance}` (one `Service` each), `internal/conformance/bus` (suite).

## Claude proposal, pending Eric

Eleven points the spec and ADRs do not decide; each proceeds on recommendation (A) with `// pending Eric: Milestone 3 item N` on the test that encodes it, so a reversal is a small, findable change. Task 13 records the answers in an ADR 0003/0008 addendum.

1. **Own-message exclusion scope.** `include_own=false` (Decision 45) excludes messages sent from the same session (`messages.session_id`), not every message of the identity. A) per session (recommended: a parent and its subagents share one credential, Decision 15, and v1 delivered a subagent's posts to its parent; per identity would hide them). B) per identity. Encoded in Task 10's `TestReceiveExcludesOwnSessionOnly`; Task 2's `MessageFilter` carries both fields so the reversal is one line.
2. **Lease renewal and lapsed sessions.** A) `Open`, `Receive` and `Watch` renew the lease (Decision 42 names the armed watch; nothing forbids the others); a session-scoped call on a lapsed-but-retained session is refused with `session_lapsed`, and `Open` with its resume token revives the same row (same id, display name, cursors, pending batch; the same token, Task 5) within the Decision 79 retention window; a lapsed row holds its display name while it can resume (choice 5, pending item 11). B) a lapsed session's call silently renews it. Encoded in Task 5's `TestSessionLapsedRefusesAndResumes`.
3. **Budget eviction, capacity notices and usage.** Spec section 5.7 lists budget eviction and capacity notices among the ported jobs, and the admin section lists usage; all three need an engine-specific size query (`PRAGMA page_count` on SQLite, `pg_total_relation_size` on Postgres) that the Milestone 2 Store has no method for, and Milestone 2 parked `auto_vacuum` for Milestone 7. A) Defer the three to Milestone 7 with a `Dialect.Usage` hook; the Milestone 3 job list ships without them and `Admin.Status` has no usage number (recommended). B) Add `Usage` to the Store now (a second Store amendment while Milestone 2 is still fresh). Encoded by the job list of Task 11.
4. **Idle subscription expiry** (v1 `cursor_idle_hours`, `receive.go` `Expired`). A) Not ported: subscriptions belong to the identity and persist (Decision 37); message retention bounds the backlog and gaps are reported (recommended). B) Port with the identity's last activity.
5. **Inspection hook** (v1 `inspect.go`, `inspection_command`). Section 9 names it neither as ported nor as dropped. A) Not ported in 2.0.0 (recommended; a server-side exec hook is a deployment decision for Milestone 7's operations docs). B) Port into the core before the write transaction.
6. **`general` delete and rename.** A) Refused with `validation` for every caller, administrators included, because the server recreates it at start and every identity is subscribed to it (Decision 92) (recommended). B) Allowed to holders of `admin`. Encoded in Task 4's `TestChannelGeneralIsProtected`.
7. **Live-subscriber refusal on delete** (v1 `deleteChannel` "active subscribers"). A) Not ported: `admin` on the channel decides (Decision 89) and subscriptions and cursors cascade (recommended). B) Port with `except` for the caller.
8. **Empty-channel reaping** (v1 `reapEmptyChannels`). A) Not ported: a channel with an ACL and a creator is configuration, not garbage; only task lists expire (ADR 0013, Decision 90) (recommended). B) Port.
9. **Polling guard** (v1 `pollingGuardEmptyWaits`, `wait_seconds`). A) Not ported: `receive` is non-blocking (Decision 83) and the guard counted empty waits (recommended). B) Port against empty non-blocking receives.
10. **Wake grace and swept-row restore** (v1 ADR 0022, 0023, `wake.go`, `meta`). A) Not ported: both ride on `meta`, which Milestone 2 Gate 0 item 4 dropped; v2 liveness is the lease, renewed by the client's armed watch, and a lapsed session resumes with its token (Decision 79) (recommended). B) Port the clock-sample detection onto a lease extension.
11. **Lapsed sessions hold their display name.** Decision 39 says the first *live* session gets `<identity>`. A) The taken set is every row with `ClosedAt == 0`, live or lapsed, so a resumed session keeps the name Decision 79 restores and two live sessions never share one; a lapsed row frees its name only when closed or swept (recommended; the cost is `pat-2` after a crash, for at most `SessionRetentionHours`). B) Live rows only: a lapsed session resuming after another took its name gets the next free one (`Resumed true`, a changed `DisplayName`). Encoded in Task 5's `TestSessionDisplayNameUniqueAcrossResumable`.

## Not ported (artifacts of decisions that changed; Decision 31)

`dm.go` and every inbox special case (Decision 13); `SessionIdentity`, harness pid and start on sessions, `IdentityForHarness`, `SessionHarness` (Decisions 14, 78); `validateContext` and the session `context` column (not in the v2 session row); `ChannelNameRule`, `prefixKinds`, `resolveKind`'s prefix branch, `IsTaskChannel`, `TaskPrefix`, `DefaultChannels` (`memory`), `ensureDefaults` (Decisions 76, 82, 90, 92); the `tags/` pseudo-subscription row (`tagSource`; Milestone 2 choice 7 re-keyed it to session and identity columns); `ReceiveInput.WaitSeconds`, `receivePollInterval` and the polling guard (Decision 83; pending item 9); `-filter` and `waitMatch` (Decision 12); `wait.go`'s `Wait` loop and the CLI wait lock (`watch` is Milestone 4 and 6; the core keeps `peek`); `meta`, `resetGeneration`, `restore`, `remember`, `wake.go` (pending item 10; Gate 0 item 4); `Reset` and `Usage` (Milestone 5 and pending item 3); `inspect.go` (pending item 5); `cursor_idle_hours` (pending item 4); `reapEmptyChannels` (pending item 8); `DiscoveryEnabled` (the identities list is an API section of its own, spec 3.2, unconditional); `PRAGMA incremental_vacuum` and `SQLiteBudgetMiB` (Milestone 7).

## Store methods assumed (Milestone 2 plan revision 3; diffed against `main` before Wave 1)

Every signature below is from `docs/superpowers/plans/2026-10-20-milestone-2-store-engines.md` Task 1 (abbreviated: `ctx` first on every method). Marked **amended** where Task 2 changes it.

| Aggregate | Methods the core calls | Consumed by |
| --- | --- | --- |
| `Meta` | `NextSeq`, `MaxSeq`, `TryLease(name, owner, now, until)`, `SetNotice`, `ClearNotice`, `Notice(kind)` | 4, 5, 6, 10, 11 |
| `Identities` | `Identity(name)`, `ListIdentities`, `AdvanceTagAckedMark(name, seq)` | 3, 4, 5, 8, 10 |
| `Receipts` | `Receipt(identity, key, now)`, `PutReceipt(r)`, `DeleteExpiredReceipts(now, limit)` | 3, 11 |
| `Channels` | `CreateChannel(c)`, `Channel(id)`, `ChannelByName(name)`, `ListChannels`, `ListChannelsByKind(k)`, `RenameChannel(id, name)`, `DeleteChannel(id)`, `SetEvictedBefore(id, seq)`, `ACL(id)`, `SetACL(id, entries)`, `EffectivePermissions(id, identity)`, `ReadableChannels(identity)` | 3, 4, 9, 10, 11 |
| `Subscriptions` | `Subscribe(identity, channelID, ackedSeq, now)`, `Unsubscribe`, `Subscriptions(identity)`, `AdvanceAckedMark(identity, channelID, seq)`, `SubscribeTags(s)`, `UnsubscribeTags(identity, tagsKey)`, `TagSubscriptions(identity)`, **amended** `TagMatchesAfter` | 4, 5, 10 |
| `Sessions` | `OpenSession(s)`, `Session(id)`, `SessionByResumeTokenHash(hash)`, `ListSessions(identity)`, `RenewLease(id, until)`, `CloseSession(id, at)`, `DeleteSessions(lapsedBefore, limit)`, `RegisterWatch(id, watchID, leaseUntil)`, `SetPendingToken(id, token)`, `SetTagsCursor(id, cursor, pendingEnd)`, `Cursors(sessionID)`, `PutCursor(c)`, `DeleteCursor(sessionID, channelID)` | 3, 5, 8, 10, 11 |
| `Messages` | `AppendMessage(m)` (Append tx only), `Message(seq)`, **amended** `MessagesAfter`, **amended** `MessagesInRange`, **amended** `SearchText`, `TombstoneMessage(seq, at)`, `DeleteMessages(seqs)`, `ExpiredTombstones(before, limit)`, `OrdinaryMessagesBefore(createdBefore, limit)`, `UnembeddedMemories(model, limit)`, `PutEmbedding`, `PutEmbedFailure`, `DeleteEmbedding(seq)`, `DeleteStaleModel(model)`, **amended** `Embeddings`, **new** `MessagesBySeqs`, **new** `MessagesBefore`, **new** `EmbedFailures(model)` | 3, 4, 6, 9, 10, 11 |
| `Memories` | `LiveMemory(memoryID)`, `MemoryRevisions(memoryID)`, `SetMemory(seq, memoryID, revision)`, `TouchMemoryAccess(ids, at)`, `DeleteMemoryAccess(memoryID)`, `ExpiredMemories(accessedBefore, limit)` | 3, 7, 9, 11 |
| `Tasks` | `LiveMemories(channelID)`, `ChannelLastActivity(channelID)` | 8, 11 |

Also assumed: `store.NameRule`, `store.ValidKind`, `store.ValidateEntry`, `store.DefaultACL`, `store.NewTagPattern`, `store.Everyone`/`Admins`, the errors `ErrNotFound`, `ErrConflict`, `ErrInvalid`, `ErrRetryable`, `ErrNotAppend`, `ErrReadOnly`; `search.Parse` and `search.Query`; `sqlite.Open(ctx, path, sqlite.Options{})`, `postgres.Open(ctx, dsn, postgres.Options{})`, `postgres.WithSearchPath(dsn, schema)`; `dbtest.Engine.DSN`, `Engine.Open`, `ForEachEngine`. Sessions are "live" when `ClosedAt == 0 && LeaseExpiresAt + LeaseSkew >= now` (choice 6).

## Review Focus

1. A subscribed channel whose ACL later loses the identity's `read` (`SetACL` by a channel admin): `Receive` delivers nothing from it and leaves its cursor untouched; `Watch` does not wake on it; a tag match on it is not delivered; re-granting `read` resumes from the same cursor with nothing lost. (Tests in Task 10: `TestReceiveSkipsUnreadableSubscription`, `TestWatchIgnoresUnreadableChannel`, `TestReceiveTagMatchNeedsRead`.)
2. A resume token whose session row the retention sweep already deleted: `Open` opens a fresh session with `Resumed == false` and no error (Decision 18 "valid and usable"); a token of a closed session is `session_closed`; a malformed token is `validation`. (Task 5: `TestSessionResumeAfterRetention`, `TestSessionResumeClosed`.)
3. A serialization failure on the first run of `Send`'s transaction on Postgres: exactly one rate-limit charge, one receipt, one message, one notifier hint; on SQLite the injected error (which the SQLite dialect does not classify) surfaces unwrapped and nothing is charged. (Task 6: `TestSendChargesOncePerCommit`, through the `faultStore` fixture of Task 3.)
4. An ack token replayed, or another session's token of the same identity: `AckIgnored == true`, cursors and acked marks unchanged; an ack of the right token after the channel was renamed still advances (cursors are keyed by channel id). (Task 10: `TestReceiveAckReplayIgnored`, `TestReceiveAckSurvivesRename`.)
5. A task claimed or assigned to `everyone` or `admins`, or to an identity whose name passes `NameRule` but is not registered: `not_found` "identity %q is not registered" (Decision 81; reserved names are never identities). (Task 8: `TestTaskOwnerRegistryCheck`.)
6. Two `Core` instances over one SQLite file with the polling notifier (local multi-process mode): the older `Watch` returns `replaced` within one poll interval after the newer registers, through the store alone (Decision 68). (Task 10: `TestWatchReplacedAcrossCores`.)
7. The same idempotency key with a different payload is `conflict`; the same key and payload after the created message was deleted by retention still replays the stored result (v1 R2, receipts are checked before any mutable lookup). (Task 6: `TestSendIdempotencyConflict`, `TestSendReplayAfterRetention`.)
8. `Search` scoped to a channel the identity cannot read is `forbidden`; an unscoped search never returns a matching row from an unreadable channel, in text, semantic and both modes; `TouchMemoryAccess` runs only for the hits returned (Decision 49). (Task 9: `TestSearchACLScoping`, `TestSearchTouchesHitsOnly`.)

---

### Task 1: Bus interfaces, domain types, errors, pure rule ports, notifier

**Depends on:** none. Imports nothing from `internal/store`; may run while Milestone 2 lands.

**Files:**
- Create: `internal/bus/doc.go`, `internal/bus/errors.go`, `internal/bus/types.go` (shared domain types), `internal/bus/messaging.go`, `internal/bus/sessions.go`, `internal/bus/memories.go`, `internal/bus/tasks.go`, `internal/bus/admin.go`, `internal/bus/bus.go` (`Bus` composite)
- Create: `internal/core/rules/tags.go`, `internal/core/rules/subject.go`, `internal/core/rules/rank.go`, `internal/core/rules/vector.go`, `internal/core/rules/trim.go`, `internal/core/rules/fingerprint.go`, `internal/core/rules/doc.go`
- Create: `internal/core/notify/doc.go`, `internal/core/notify/memory.go`, `internal/core/notify/polling.go` (a leaf package: `core` imports it, never the reverse; no `store` import)
- Test: `internal/bus/errors_test.go`, `internal/core/rules/tags_test.go`, `rank_test.go`, `vector_test.go`, `trim_test.go`, `subject_test.go`, `fingerprint_test.go`, `internal/core/notify/memory_test.go`, `internal/core/notify/polling_test.go`

**Port from v1:** `internal/bus/errors.go` (`Error`, `MaxErrorMessageBytes`, `TruncateErrorMessage`, `errf`, `internal`); `internal/bus/tags.go` (`maxTags`, `maxTagLen`, `tagRe`, `tagRuleText`, `validTag`, `normalizeTagList`, `NormalizeTags`, `tagPrefixRe`, `NormalizeTagPatterns`, `validTagPattern`, `MatchTag`, `MatchTags`, `matchedTags`; `tagRange` is already `store.TagRange`); `internal/bus/messages.go` (`maxSubjectRunes`, `normalizeSubject`, `envelopeBytes`, `envelopeUpperBound` with `upperBoundSeqDigits`/`upperBoundCreatedAtDigits`, `marshalLen`, `trimFramingBytes`, `trimHardCeilingBytes`, `trimToBytes`); `internal/bus/rank.go` (whole file: `rankDigits`, `rankBetween`, `lastDigitIndex`, `rankAfter`, `rankBefore`); `internal/bus/embed.go` (`normalize`, `dot`, `encodeVec`, `decodeVec`, `embedText`); `internal/bus/receipts.go` (`fingerprint`); `internal/bus/search.go` (`rrf`, `searchPageDefault`, `searchPageMax`); the `Message`, `SendInput`, `SendResult`, `ReceiveInput`, `ReceiveResult`, `Gap`, `EditInput`, `EditResult`, `SearchInput`, `SearchHit`, `SearchResult`, `Task`, `TaskSummary`, `TaskCreateInput`, `TaskListInput`, `TaskPatch`, `TaskUpdateResult`, `Channel`, `Session` shapes from `messages.go`, `receive.go`, `memories.go`, `search.go`, `tasks.go`, `tasks_update.go`, `channels.go`, `sessions.go`, `admin.go`, with the v2 changes below. Not ported: `Registration` (Milestone 5), `MatchedTags`-less inbox fields, `Context`, `Harness*`.

**Interfaces (produces; every later task consumes these exact names):**

```go
package bus

type Code string
const (
	CodeValidation Code = "validation"; CodeNotFound Code = "not_found"; CodeConflict Code = "conflict"
	CodeForbidden Code = "forbidden"; CodeNotRegistered Code = "not_registered"; CodeRateLimited Code = "rate_limited"
	CodeReplaced Code = "replaced"; CodeSessionLapsed Code = "session_lapsed"; CodeSessionClosed Code = "session_closed"
	CodeUnavailable Code = "unavailable"; CodeInternal Code = "internal"
)
func ValidCode(c Code) bool
type Error struct { Code Code `json:"code"`; Message string `json:"message"`; Retryable bool `json:"retryable"` }
func (e *Error) Error() string                                       // the JSON, as v1
const MaxErrorMessageBytes = 1024
func TruncateErrorMessage(s string) string
func Errorf(code Code, retryable bool, format string, args ...any) *Error // panics on an invalid code (a programming error)
func Internal(err error) error                                       // nil → nil; *Error passthrough; else {internal, err.Error(), true}
func CodeOf(err error) Code                                          // "" when err is not an *Error (errors.As)

// types.go
type Kind string; const (KindOrdinary Kind = "ordinary"; KindMemory Kind = "memory"; KindTasks Kind = "tasks")
type Permission string; const (PermRead Permission = "read"; PermWrite Permission = "write"; PermAdmin Permission = "admin")
type ACLEntry struct { Identity string; Permissions []Permission }
type Channel struct { Name string; Kind Kind; Creator string; CreatedAt int64 }               // Decision 94: name and kind only; no counts
type Message struct { Seq int64; Channel, Sender, Session, Display string; CreatedAt int64; Type, Subject, Content string; ReplyTo *int64; Metadata map[string]string; MemoryID, Revision *int64; Tags, MatchedTags []string }
// Sender is the identity; Session the sending session id ("" when none); Display the sending session's display name, or Sender when its row is gone.
type Gap struct { Channel string; From, To int64 }
type Notice struct { Kind, Message string; SetAt int64 }

// messaging.go
type CreateChannelInput struct { Name string; Kind Kind; ACL []ACLEntry; IdempotencyKey string } // Kind "" → ordinary; ACL nil → configured default; [] → empty (Milestone 2 choice 9)
type DeleteChannelInput struct { Name, IdempotencyKey string }
type RenameChannelInput struct { Name, NewName, IdempotencyKey string }
type SetACLInput struct { Channel string; Entries []ACLEntry; IdempotencyKey string }
type SubscribeInput struct { Channel string; From string /* "" or "now" | "oldest" */; IdempotencyKey string }
type UnsubscribeInput struct { Channel, IdempotencyKey string }
type TagSubscribeInput struct { Patterns []string; IdempotencyKey string }
type SendInput struct { Channel, Subject, Content, Type string; ReplyTo *int64; Metadata map[string]string; Tags []string; Session string; IdempotencyKey string } // Session: optional, the caller's session id for attribution and own-exclusion
type SendResult struct { Seq int64; MemoryID *int64 }
type HistoryInput struct { Channel string; Before, After *int64; Count int; Tags []string }
type SearchInput struct { Query, Mode, Channel, Sender string; Since, Until, Thread *int64; Tags []string; Count int; Cursor string } // Mode "" | "text" | "semantic" | "both"
type SearchHit struct { Message; Score float64 }
type SearchResult struct { Hits []SearchHit; Next string; SemanticUnavailable bool }
type Messaging interface {
	ListChannels(ctx) ([]Channel, error)
	CreateChannel(ctx, in CreateChannelInput) (Channel, error)
	DeleteChannel(ctx, in DeleteChannelInput) error
	RenameChannel(ctx, in RenameChannelInput) error
	ACL(ctx, channel string) ([]ACLEntry, error)
	SetACL(ctx, in SetACLInput) error
	Subscribe(ctx, in SubscribeInput) error
	Unsubscribe(ctx, in UnsubscribeInput) error
	SubscribeTags(ctx, in TagSubscribeInput) error
	UnsubscribeTags(ctx, in TagSubscribeInput) error
	TagSubscriptions(ctx) ([][]string, error)
	Send(ctx, in SendInput) (SendResult, error)
	History(ctx, in HistoryInput) ([]Message, error)
	Search(ctx, in SearchInput) (SearchResult, error)
}

// sessions.go
type OpenSessionInput struct { Hint, ResumeToken string; Cursors []OfferedCursor }
type OfferedCursor struct { Channel string; Seq int64 }
type RejectedCursor struct { Channel, Reason string } // "before_retention" | "ahead" | "unreadable" | "unsubscribed" (Decision 44)
type PendingChannel struct { Channel string; Pending int64 }
type Session struct { ID, Identity, DisplayName, ResumeToken string; Resumed bool; RejectedCursors []RejectedCursor; Pending []PendingChannel }
type CloseSessionInput struct { Session, IdempotencyKey string }
type ReceiveInput struct { Session, Ack string; Count int; Channels []string; IncludeOwn bool }
type ReceiveResult struct { Messages []Message; Batch string; Redelivered, AckIgnored bool; Gaps []Gap; Notice, Instruction string }
type WatchInput struct { Session string; After int64; Timeout time.Duration; IncludeOwn bool }
type WatchResult struct { Woke bool; Seq int64; Count int; Channels []string } // Woke false: timeout (204 in Milestone 4)
type SessionInfo struct { ID, Identity, DisplayName string; OpenedAt int64 }
type IdentityInfo struct { Name string; Admin bool; Sessions []SessionInfo }   // live sessions only
type Sessions interface {
	Open(ctx, in OpenSessionInput) (Session, error)
	Close(ctx, in CloseSessionInput) error
	Receive(ctx, in ReceiveInput) (ReceiveResult, error)
	Watch(ctx, in WatchInput) (WatchResult, error)
	Identities(ctx) ([]IdentityInfo, error)                                      // v1 discover: identities with a live session
}

// memories.go
type EditMemoryInput struct { ID int64; Subject *string; Content, Type string; Metadata map[string]string; Tags []string; IdempotencyKey string }
type EditResult struct { Seq, MemoryID, Revision, Replaced int64 }
type DeleteMemoryInput struct { ID int64; IdempotencyKey string }
type Memories interface { GetMemory(ctx, id int64) (Message, error); Edit(ctx, in EditMemoryInput) (EditResult, error); Delete(ctx, in DeleteMemoryInput) error; Revisions(ctx, id int64) ([]Message, error) }
// GetMemory, not Get: Bus embeds Memories and Tasks, and Go allows an overlapping method name in embedded interfaces only with an identical signature.

// tasks.go: Task, TaskSummary, TaskCreateInput, TaskListInput, TaskPatch, TaskUpdateResult exactly as v1 (Channel is the task list's name)
type TaskClaimInput struct { ID, LeasedUntil int64; IdempotencyKey string }
type TaskReleaseInput struct { ID int64; IdempotencyKey string }
type Tasks interface { Create(ctx, in TaskCreateInput) (Task, error); GetTask(ctx, id int64) (Task, error); List(ctx, in TaskListInput) ([]TaskSummary, error); Update(ctx, p TaskPatch) (TaskUpdateResult, error); Claim(ctx, in TaskClaimInput) (TaskUpdateResult, error); Release(ctx, in TaskReleaseInput) (TaskUpdateResult, error) }
// GetTask, not Get (see Memories). The only other overlap, DeleteChannel and RenameChannel on Messaging and Admin, has identical signatures and is legal.

// admin.go
type Status struct { Sessions []SessionInfo; Channels []Channel; Notices []Notice; EmbeddingBacklog, EmbeddingRejected int; EmbeddingRejectedLast, EmbeddingError string; EmbeddingErrorAt int64 }
type Admin interface { Status(ctx) (Status, error); DeleteChannel(ctx, in DeleteChannelInput) error; RenameChannel(ctx, in RenameChannelInput) error } // Decision 89: same operations, admin section

// bus.go
type Bus interface { Messaging; Sessions; Memories; Tasks; Admin } // compiles only while no two embedded interfaces declare one name with different signatures; Task 3's `var _ ab2bus.Bus = (*local.Bus)(nil)` is the proof
```

```go
package notify // internal/core/notify; a leaf package: core imports it, never the reverse
type Hint struct { ChannelID, Seq int64 }  // Seq 0: a non-message hint (a watch registration, an ACL change)
type Notifier interface { Publish(h Hint); Subscribe() (hints <-chan Hint, cancel func()) }
func NewMemory() *Memory          // Publish fans out non-blocking into each subscriber's 1-slot channel (a full slot is left full: hints coalesce)
func NewPolling(interval time.Duration) *Polling // Subscribe returns a channel fed Hint{} every interval by a goroutine stopped by cancel; Publish is a no-op
```

```go
package rules // internal/core/rules; pure, no store import
const MaxTags = 10; const MaxTagLen = 32
func NormalizeTags(tags []string) ([]string, error)            // *bus.Error validation, v1 text
func NormalizeTagPatterns(patterns []string) ([]string, error)
func MatchTag(pattern, tag string) bool; func MatchTags(patterns, tags []string) bool
type TagSet struct { Patterns []string; CreatedSeq int64 }
func MatchedTags(sets []TagSet, seq int64, tags []string) []string  // v1 matchedTags
func TagsKey(patterns []string) string                               // strings.Join(",")
const MaxSubjectRunes = 200
func NormalizeSubject(s string) (string, error)
func RankBetween(a, b string) string; func RankAfter(a string) string; func RankBefore(b string) string; const RankDigits
func Normalize(v []float32); func Dot(a, b []float32) float64; func EncodeVec(v []float32) []byte; func DecodeVec(b []byte) []float32; func EmbedText(subject, content string) string
const TrimFramingBytes = 32; const TrimHardCeilingBytes = 4 << 20
func TrimToBytes[T any](items []T, size func(T) int, limit int) []T
func MarshalLen(v any) int; func EnvelopeBytes(m bus.Message) int
func EnvelopeUpperBound(m bus.Message, memoryKind bool) int   // v1 envelopeUpperBound over a bus.Message with Seq/CreatedAt unset
func Fingerprint(payload any) (string, error)                 // sha256 hex of canonical JSON
func RRF[T any](text, sem []T, seqOf func(T) int64, setScore func(*T, float64)) []T // k = 60; ties by seq ascending
const SearchPageDefault = 100; const SearchPageMax = 1000
```

- [ ] **Step 1: Write the failing tests**

`errors_test.go`: `TestErrorJSON` (the string form is the v1 JSON with the three members), `TestTruncateErrorMessage` (1024 boundary, the `…` marker), `TestErrorfRejectsUnknownCode` (panics on `Code("bogus")`), `TestInternal` (nil, passthrough, wrap), `TestCodeOf` (wrapped with `fmt.Errorf("%w")` still found). `tags_test.go`: port the normalization cases of v1 `tags_test.go` (`TestNormalizeTags`, `TestNormalizeTagPatterns`, `TestMatchTag`, `TestMatchedTags`; the v2 names). `subject_test.go`: `TestNormalizeSubject` (trim, newline rejected, 201 runes rejected, 200 pass). `rank_test.go`: port v1 `rank_test.go`. `vector_test.go`: `TestEncodeDecodeRoundTrip`, `TestDotOfNormalized` (1 for equal unit vectors, 0 for orthogonal), `TestEmbedText`. `trim_test.go`: `TestTrimToBytesKeepsFirst`, `TestTrimToBytesHardCeiling`, `TestEnvelopeUpperBoundMemoryKind`. `fingerprint_test.go`: `TestFingerprintKeyOrderIndependent`. `notify/memory_test.go`: `TestMemoryFanOutAndCoalesce` (two subscribers each get one hint for three rapid publishes; cancel stops delivery). `notify/polling_test.go`: `TestPollingTicksAndIgnoresPublish` (10 ms interval: at least 3 hints in 100 ms; `Publish` adds none out of band).

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/bus/ ./internal/core/rules/ ./internal/core/notify/`
Expected: build failure (`undefined: Errorf`, `undefined: NormalizeTags`, `undefined: NewMemory`).

- [ ] **Step 3: Implement** as in Interfaces. Doc comments name the decision each type serves (`Session`: Decisions 15, 39, 40, 44; `ReceiveResult`: Decision 16; `Channel`: Decision 94; `WatchResult`: Decision 42).

- [ ] **Step 4: Run the tests, vet, lint, format**

Run: `make build-all vet fmt-check lint test`
Expected: pass.

- [ ] **Step 5: Commit**

```bash
git add internal/bus internal/core/rules internal/core/notify
git commit -m "feat(bus): Bus interfaces split by consumer, domain types, errors, pure rule ports and the notifier

Ports Error, MaxErrorMessageBytes and TruncateErrorMessage from v1
internal/bus/errors.go into the RFC 9457 members of Decision 46, the tag
rules from v1 internal/bus/tags.go (v1 ADR 0009), normalizeSubject,
envelopeUpperBound and trimToBytes from v1 internal/bus/messages.go (v1
ADR 0010), rank.go whole, normalize/dot/encodeVec/decodeVec from v1
internal/bus/embed.go, fingerprint from v1 internal/bus/receipts.go and
rrf from v1 internal/bus/search.go. Interfaces per Decision 7, generated
and storage types behind the seam per Decision 47; the Notifier of
Decision 52 with in-memory and polling forms (the polling interval of v1
internal/bus/wait.go)."
```

---

### Task 2: Store amendment: message filters, own-sender exclusion, batch loader

**Depends on:** none (external gate: Milestone 2 Task 12 on `main`). Runs beside Task 1; touches only Store files.

**Files:**
- Modify: `internal/store/messages.go` (`MessageFilter`, `SearchFilter`, the four signatures, `MessagesBySeqs`), `internal/store/subscriptions.go` (`TagMatchesAfter`), `internal/store/sqlstore/messages.go`, `internal/store/sqlstore/tagsubs.go`, `internal/store/sqlstore/embeddings.go`
- Modify (call sites and new cases): `internal/conformance/store/messages_test.go`, `internal/conformance/store/tagsubs_test.go`, `internal/conformance/store/search_test.go`, `internal/conformance/store/embeddings_test.go` (filters and `EmbedFailures`), `internal/conformance/store/seq_test.go` (if it calls `MessagesAfter`)

**Port from v1:** `internal/bus/search.go` (`searchFilters`: sender, since, until, thread `(m.seq = ? OR m.reply_to = ?)`, tags through `tagsFilter`; the DM branch is not ported), `internal/bus/receive.go` (`ownClause`: `AND sender <> ?`, re-keyed to `session_id` as well), `internal/bus/admin.go` (`StatusReport`'s two `embed_failures` queries, lines 126 to 129: the count joined to live messages and the latest reason, as `EmbedFailures`).

**Interfaces (produces):**

```go
package store
type MessageFilter struct { ExcludeSender, ExcludeSessionID string } // "" = no exclusion; both may be set (AND)
type SearchFilter struct { Sender string; Since, Until, Thread *int64; Patterns []TagPattern } // Since/Until on created_at inclusive; Thread: seq == t OR reply_to == t; Patterns: every pattern matched by some tag (tagsFilter)
MessagesAfter(ctx, channelID, afterSeq int64, f MessageFilter, limit int) ([]Message, error)
MessagesInRange(ctx, channelID, fromSeq, toSeq int64, patterns []TagPattern, f MessageFilter, limit int) ([]Message, error)
MessagesBySeqs(ctx, seqs []int64) ([]Message, error)                                   // live rows only, ascending seq; unknown seqs skipped; empty → empty, no query
MessagesBefore(ctx, channelID, beforeSeq int64, patterns []TagPattern, f MessageFilter, limit int) ([]Message, error) // live, seq < beforeSeq, DESCENDING seq, limit: v1 History with before only
SearchText(ctx, q search.Query, channelIDs []int64, f SearchFilter, limit int) ([]Message, error)
Embeddings(ctx, model string, channelIDs []int64, f SearchFilter) ([]Embedding, error) // live memories on channelIDs matching f (join messages)
TagMatchesAfter(ctx, identity string, afterSeq, upToSeq int64, channelIDs []int64, f MessageFilter, limit int) ([]Message, error) // upToSeq 0 = unbounded
EmbedFailures(ctx, model string) (count int, last EmbedFailure, err error) // v1 admin.go: failures for model whose message is still a live memory (tombstone = 0); last by failed_at DESC, seq DESC; none → 0 and the zero EmbedFailure, not ErrNotFound
```

- [ ] **Step 1: Update call sites and write the failing cases**

Existing callers pass `store.MessageFilter{}` / `store.SearchFilter{}` / `upToSeq 0`. New cases (both engines): `TestMessagesAfterExcludesSenderAndSession` (three rows from `pat` session `s1`, `pat` session `s2`, `kim`: `ExcludeSender: "pat"` returns only kim's; `ExcludeSessionID: "s1"` returns the other two; limit applies after exclusion), `TestMessagesInRangeExcludes`, `TestMessagesBySeqs` (order, tombstoned excluded, unknown skipped, empty input), `TestMessagesBeforeDescending` (seq < before, newest first, limit, patterns and exclusion honored), `TestSearchTextFilters` (sender, since/until inclusive bounds, thread with a reply, patterns; each narrows the `rate` corpus set identically on both engines), `TestEmbeddingByChannelsFiltered` (sender and since narrow), `TestTagSubMatchesUpToAndExcludes` (`upToSeq` bounds; `ExcludeSessionID` removes the session's own tagged message), `TestEmbedFailuresCountAndLast` (three failures for model `m` at increasing `failed_at`, one for model `other`: count 3 and the latest reason; tombstoning the latest row drops the count to 2 and moves `last`; an unknown model is 0 and the zero value).

- [ ] **Step 2: Run to verify they fail**

Run: `go test -count=1 -p 1 -run '^Test(Message|Search|Embedding|TagSub)' ./internal/conformance/store/`
Expected: build failure on the new signatures.

- [ ] **Step 3: Implement** in `sqlstore`: the exclusion is `AND m.sender <> ?` / `AND m.session_id <> ?` appended when set; the search filter is v1 `searchFilters` over alias `m` (portable: `=`, `>=`, `<=`, `OR`, the `EXISTS` tag filter); `MessagesBySeqs` uses `sqlx.In`; `Embeddings` joins `messages m` and applies the filter; `TagMatchesAfter` adds `AND mt.seq <= ?` when `upToSeq > 0`; `EmbedFailures` is the two v1 queries joined on `messages` with `tombstone = 0`, the second with `ORDER BY f.failed_at DESC, f.seq DESC LIMIT 1` and `sql.ErrNoRows` mapped to the zero value.

- [ ] **Step 4: Run the Store suite on both engines and the gate**

Run: `env -u AGENTBUS2_TEST_POSTGRES_DSN go test -count=1 -p 1 -v ./internal/conformance/store/ 2>&1 | grep -E '^(--- FAIL|ok|FAIL)'`, then `make verify` (600000 ms).
Expected: no `--- FAIL`; `verify: OK`.

- [ ] **Step 5: Commit**

```bash
git add internal/store internal/conformance/store
git commit -m "feat(store): message filters, own-sender exclusion, bounded tag matches, a batch loader and embed failure stats for the core

Ports searchFilters from v1 internal/bus/search.go and ownClause from v1
internal/bus/receive.go into Store parameters so delivery (Decision 45
include_own) and search (Decisions 51, 95) filter in SQL on both engines,
and StatusReport's embed_failures queries from v1 internal/bus/admin.go
as EmbedFailures."
```

---

### Task 3: Core base, local Bus skeleton, suite fixtures

**Depends on:** 1, 2.

**Files:**
- Create: `internal/core/doc.go`, `internal/core/config.go`, `internal/core/core.go`, `internal/core/errors.go`, `internal/core/authz.go`, `internal/core/limiter.go`, `internal/core/receipts.go`, `internal/core/append.go`, `internal/core/memory.go` (tombstone and live-revision helpers), `internal/core/sessions.go` (liveness helpers), `internal/core/convert.go` (store ↔ bus types), `internal/core/notifier.go` (the `Publish` wrapper over Task 1's `notify.Notifier`)
- Create: `internal/bus/local/local.go` (`Bus`, `New`, `ErrNotImplemented`) and the eight stubs, each holding exactly the methods the task of the same aggregate later replaces (file comment "replaced by Task N"): `channels_stub.go` (Task 4: `ListChannels`, `CreateChannel`, `DeleteChannel`, `RenameChannel`, `ACL`, `SetACL`, `Subscribe`, `Unsubscribe`, `SubscribeTags`, `UnsubscribeTags`, `TagSubscriptions`; the eleven channel and subscription methods, `DeleteChannel` and `RenameChannel` serving both `Messaging` and `Admin`), `admin_stub.go` (Task 4: `Status`), `sessions_stub.go` (Task 5: `Open`, `Close`, `Identities`), `send_stub.go` (Task 6: `Send`, `History`), `memories_stub.go` (Task 7: `GetMemory`, `Edit`, `Delete`, `Revisions`), `tasks_stub.go` (Task 8: `Create`, `GetTask`, `List`, `Update`, `Claim`, `Release`), `search_stub.go` (Task 9: `Search`), `delivery_stub.go` (Task 10: `Receive`, `Watch`)
- Create: `internal/conformance/bus/helpers_test.go`, `internal/conformance/bus/fixtures_test.go`; Modify: `internal/conformance/bus/bus_test.go` (keep `TestScaffold`; add `TestLocalBusImplementsEveryInterface`)
- Test (Docker-free, SQLite): `internal/core/config_test.go`, `limiter_test.go`, `receipts_test.go`, `authz_test.go`, `append_test.go`

**Port from v1:** `internal/bus/ratelimit.go` (whole: `limiter`, `bucket`, `newLimiter`, `allow`; keyed per identity, Decision 86; split into `Check` and `Charge`), `internal/bus/receipts.go` (`checkReceipt`, `storeReceipt`; keyed per identity, Decision 67), `internal/config/config.go` (the store-concerning fields, their defaults and `validate` bounds; nothing of the TUI, paths, inspection or discovery fields), `internal/bus/messages.go` (`insertMessage`'s memory branch: `memory_id = seq`, `revision = 1`, the `memory_access` touch for non-task kinds; `sendEnvelope`'s size gate), `internal/bus/memories.go` (`liveRevision`, `tombstoneMemoryRow`), `internal/bus/sessions.go` (`auth`'s "is registered" half only), `internal/bus/bus.go` (`randomToken`, `nowMs`).

**Interfaces (produces):**

```go
package core
type Config struct { /* choice 6 fields, with doc comments naming each decision */ }
func Defaults() Config
func (c Config) Validate() error         // v1 bounds; ChannelCreation in {"open", "admins"}; EmbeddingModel required when an Embedder is given (checked in New)
type Embedder interface { Model() string; Embed(ctx context.Context, texts []string) ([][]float32, error) } // vectors are normalized by the core after Embed
type Options struct { Now func() time.Time /* nil: time.Now */; Notifier notify.Notifier /* nil: notify.NewMemory() */; Embedder Embedder /* nil: text search only */; Logger *slog.Logger /* nil: discard */ }
type Core struct { Store store.Store; Cfg Config; Limits *Limiter; Owner string /* random per instance; the maintenance lease owner */; EmbedSoon func() /* nil until Task 9 sets it; Send and Edit call it after commit when non-nil */; ... }
func New(s store.Store, cfg Config, opts Options) (*Core, error)
func (c *Core) Now() time.Time; func (c *Core) NowMs() int64
func (c *Core) Publish(h notify.Hint)
func (c *Core) Embedder() Embedder        // nil when none
func RandomToken(n int) (string, error)   // n random bytes, base64url no padding
func RandomHex(n int) (string, error)     // v1 randomToken shape (hex)

// errors.go
func FromStore(err error) error   // nil→nil; *bus.Error passthrough; ErrNotFound→not_found; ErrConflict→conflict; ErrInvalid→validation (store's reason text); ErrRetryable→unavailable; ErrNotAppend/ErrReadOnly/other→internal
func E(code bus.Code, format string, args ...any) *bus.Error  // Errorf with the code's fixed retryable (rate_limited, unavailable, internal true; others false)

// authz.go
func (c *Core) Authorize(ctx, tx store.Tx, identity string, channelID int64, p store.Permission) error // EffectivePermissions; forbidden "%s needs %s on channel %q"
func (c *Core) RequireIdentity(ctx, tx, identity string) (store.Identity, error)                      // not_registered; also refuses "" and the reserved names
func (c *Core) RequireAdmin(ctx, tx, identity string) (store.Identity, error)                        // forbidden "administrator privilege required" (Decision 73)
func (c *Core) ChannelByName(ctx, tx, name string) (store.Channel, error)                            // not_found "channel %q does not exist"
func (c *Core) ReadableChannelIDs(ctx, tx, identity string) ([]int64, error)

// limiter.go
type Limiter struct{...}; func NewLimiter(perSec, kibPerSec, maxMessageKiB int) *Limiter
func (l *Limiter) Check(identity string, n int, now time.Time) bool   // refills, no charge
func (l *Limiter) Charge(identity string, n int, now time.Time)       // refills, charges (may go negative; a later Check then refuses)

// receipts.go
func (c *Core) WithReceipt(ctx, tx store.Tx, identity, key string, payload any, result any, fn func() error) (replayed bool, err error)
// key "": runs fn, stores nothing. Hit with equal fingerprint: json.Unmarshal(stored, result), replayed true, fn not run. Hit with another fingerprint: conflict "idempotency_key %q was used with a different payload". Miss: fn, then PutReceipt(result JSON, ExpiresAt now + ReceiptRetentionMinutes).

// append.go
type AppendInput struct { ChannelID int64; Kind store.Kind; Sender, SessionID string; Type, Subject, Content string; ReplyTo *int64; Metadata map[string]string; Tags []string /* normalized */; TaskRow bool /* kind tasks: no memory_access row */ }
func (c *Core) EnvelopeSize(in AppendInput, sender string) (int, error)  // rules.EnvelopeUpperBound; validation "envelope exceeds max_message_kib (%d KiB)"
func (c *Core) Append(ctx, tx store.Tx, in AppendInput) (seq int64, memoryID *int64, err error) // AppendMessage; memory kind: SetMemory(seq, seq, 1) and TouchMemoryAccess unless TaskRow; reply_to must exist (not_found "reply_to %d does not exist")

// memory.go
func (c *Core) LiveRevision(ctx, tx, memoryID int64) (store.Message, store.Channel, error) // not_found "memory %d has no live revision"
func (c *Core) TombstoneMemory(ctx, tx, memoryID, seq, at int64) (bool, error)             // v1 tombstoneMemoryRow: TombstoneMessage, DeleteEmbedding, DeleteMemoryAccess

// sessions.go
func (c *Core) IsLive(s store.Session, nowMs int64) bool                     // ClosedAt == 0 && LeaseExpiresAt + skew >= now
func (c *Core) LiveSessions(ctx, tx, identity string) ([]store.Session, error)
func (c *Core) LeaseUntil(nowMs int64) int64                                 // now + SessionLeaseSeconds

// convert.go
func (c *Core) ToBusMessages(ctx, tx store.Tx, ms []store.Message) ([]bus.Message, error) // channel names and display names resolved once per call (maps keyed by id)
func KindFromBus(k bus.Kind) (store.Kind, error); func KindToBus(store.Kind) bus.Kind        // "" → ordinary; unknown → validation
func ACLFromBus([]bus.ACLEntry) ([]store.ACLEntry, error); func ACLToBus([]store.ACLEntry) []bus.ACLEntry // unknown permission → validation
func ChannelToBus(store.Channel) bus.Channel
```

```go
package local
type Bus struct { c *core.Core; identity string }
func New(c *core.Core, identity string) *Bus
var _ ab2bus.Bus = (*Bus)(nil)
var ErrNotImplemented = errors.New("local: not implemented in this milestone") // deleted by Task 13
```

```go
package bus // internal/conformance/bus; aliases ab2bus, ab2store
// helpers_test.go: isolate/openStore as Milestone 2's (schema b_<unix seconds>_<8 hex>, sweep of b_% older than an hour when AGENTBUS2_TEST_POSTGRES_DSN is set), opening through sqlite.Open / postgres.Open
func openStore(t *testing.T, e dbtest.Engine) (s ab2store.Store, reopen func() (ab2store.Store, error))
// fixtures_test.go
type fakeClock struct{...}; func (f *fakeClock) Now() time.Time; func (f *fakeClock) Advance(d time.Duration)
type fakeEmbedder struct{...}  // Model() "fake"; a text's vector is its lowercase-word histogram over a fixed 16-word vocabulary, normalized; words outside it are ignored; Fail bool makes Embed return an error
type faultStore struct { ab2store.Store; failFirst *pgconn.PgError /* returned once, after fn's first run, from Tx and Append */ } // Review Focus 3
type harness struct { t *testing.T; e dbtest.Engine; store ab2store.Store; core *core.Core; clock *fakeClock; notifier notify.Notifier /* *notify.Memory by default, *notify.Polling when harnessOptions.polling > 0; tests use Subscribe() only */; embedder *fakeEmbedder; cfg core.Config }
type harnessOptions struct { cfg *core.Config; polling time.Duration /* >0: notify.NewPolling */; embedder bool; fault *pgconn.PgError }
func newHarness(t *testing.T, e dbtest.Engine, o harnessOptions) *harness  // openStore, core.New with fixedNow 1_700_000_000_000 ms as the clock start
var newBus = func(h *harness, identity string) ab2bus.Bus { return local.New(h.core, identity) } // Milestone 4 reassigns it
func (h *harness) sibling(o harnessOptions) *harness   // a second Core over the same database (the same SQLite path or Postgres schema), its own notifier, clock and limiter: the local multi-process cases (Review Focus 6, Task 11's lease overlap)
func (h *harness) as(identity string) ab2bus.Bus          // CreateIdentity when missing; newBus
func (h *harness) admin(identity string) ab2bus.Bus       // as + SetAdmin(true)
func (h *harness) channel(name string, kind ab2store.Kind, creator string, acl []ab2store.ACLEntry) ab2store.Channel // through the Store; CreatedSeq = MaxSeq
func (h *harness) append(ch ab2store.Channel, sender, session, content string, tags ...string) int64 // through Store.Append, CreatedAt = clock
func (h *harness) open(identity string) (ab2bus.Bus, ab2bus.Session)   // as(identity) then Open with no hint; t.Fatal on error
func codeOf(t *testing.T, err error, want ab2bus.Code)                   // t.Fatalf unless CodeOf(err) == want
```

- [ ] **Step 1: Write the failing unit tests (SQLite, Docker-free) and the suite scaffold**

`config_test.go`: `TestDefaultsValidate`, `TestConfigRejectsOutOfRange` (each v1 bound; `ChannelCreation: "closed"` → error naming the two values; `SessionLeaseSeconds: 0`). `limiter_test.go`: port v1 `ratelimit` behavior as `TestLimiterCheckThenCharge` (100/s: 100 charges pass, the 101st `Check` is false; after 1 s one more), `TestLimiterBytes`, `TestLimiterKeyedPerIdentity` (two identities independent; Decision 86). `receipts_test.go` (SQLite store on a temp path): `TestWithReceiptReplaysEqualPayload`, `TestWithReceiptConflictOnDifferentPayload`, `TestWithReceiptExpired` (clock past `ReceiptRetentionMinutes` → fn runs again), `TestWithReceiptEmptyKeyRunsAlways`, `TestWithReceiptScopedPerIdentity`. `authz_test.go`: `TestAuthorizeUsesImplicitEntries` (creator has admin only; `everyone` row grants read to a stranger; `forbidden` text names the permission and channel), `TestRequireIdentityRejectsReservedAndUnknown` (`everyone`, `admins`, `ghost`, `""` → `not_registered`), `TestRequireAdmin`. `append_test.go`: `TestAppendMemoryKindSetsMemoryAndAccess`, `TestAppendTaskRowSkipsAccess`, `TestAppendRejectsOversizedEnvelope`, `TestAppendRejectsUnknownReplyTo`, `TestFromStoreMapping` (table over the Store errors). Suite: `TestLocalBusImplementsEveryInterface` (compile-time `var _` plus one call per stub returning `ErrNotImplemented`, both engines, so the fixtures are exercised on Postgres from day one; Task 13 rewrites it to expect success).

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/core/... ./internal/bus/... && go test -count=1 -p 1 ./internal/conformance/bus/`
Expected: build failure (`undefined: New`).

- [ ] **Step 3: Implement** as in Interfaces. `New` validates `cfg`, requires `cfg.EmbeddingModel == opts.Embedder.Model()` when an embedder is given, generates `Owner` with `RandomHex(12)`. `Authorize` reads `EffectivePermissions` and picks the field by `p`; an unknown `p` is a programming error (panic). `WithReceipt` marshals `result` after `fn`. The suite's `openStore` mirrors Milestone 2's `helpers_test.go` with the `b_` prefix (it cannot import that `_test.go` file).

- [ ] **Step 4: Run the tests, the suite scaffold on both engines, and the gate**

Run: `make build-all vet fmt-check lint test`, then `env -u AGENTBUS2_TEST_POSTGRES_DSN go test -count=1 -p 1 -v ./internal/conformance/bus/ 2>&1 | grep -E '^(--- |ok|FAIL)'`, then `make verify` (600000 ms).
Expected: pass; `--- PASS` for `/sqlite` and `/postgres`; `verify: OK`.

- [ ] **Step 5: Commit**

```bash
git add internal/core internal/bus/local internal/conformance/bus
git commit -m "feat(core): core base over Store: config, clock, per-identity limiter, receipts, ACL check, append and notifier

Ports the token buckets of v1 internal/bus/ratelimit.go keyed per identity
(Decision 86), checkReceipt and storeReceipt from v1 internal/bus/receipts.go
keyed per identity (Decisions 46, 67), the store-concerning settings and
bounds of v1 internal/config/config.go, insertMessage's memory branch from v1
internal/bus/messages.go, liveRevision and tombstoneMemoryRow from v1
internal/bus/memories.go; adds the injected clock (Decision 49), the one
Authorize for Decision 80, the Publish seam over the Decision 52 Notifier,
and the local Bus skeleton."
```

---

### Task 4: Channels, ACL operations, subscriptions, tag subscriptions, admin

**Depends on:** 3.

**Files:**
- Create: `internal/core/channels/service.go` (`Service`, `EnsureGeneral`), `channels.go` (list, create, delete, rename, ACL, SetACL), `subscriptions.go` (subscribe, unsubscribe, tag subscriptions), `admin.go` (`Status`)
- Delete: `internal/bus/local/channels_stub.go`, `internal/bus/local/admin_stub.go`; Create: `internal/bus/local/channels.go`, `internal/bus/local/admin.go`
- Test: `internal/conformance/bus/channels_test.go`, `subscriptions_test.go`, `admin_test.go`

**Port from v1:** `internal/bus/channels.go` (`CreateChannel`: a free name, `created_seq` from `MaxSeq`; `ListChannels`; `deleteChannel` without the subscriber refusal, pending item 7; `RenameChannel` on the id), `internal/bus/receive.go` (`Subscribe` with `from` now|oldest where oldest is `max(evicted - 1, 0)`; `Unsubscribe`), `internal/bus/tags.go` (`SubscribeTags`: `created_seq = MaxSeq`, idempotent; `UnsubscribeTags`; `TagSubscriptions` sorted by key), `internal/bus/admin.go` (`StatusReport`: sessions and channels; the embedding counters through `UnembeddedMemories` and the `embedding` notice; no usage, pending item 3). Not ported: `EnsureChannel`, `ensureDefaults`, `isDefaultChannel`, `reapEmptyChannels`, `PrefixKind`, `dmOwner`.

**Interfaces:**
- Consumes: Task 3's `Core`, `Authorize`, `RequireIdentity`, `RequireAdmin`, `WithReceipt`, `ChannelByName`, converters; Store `Channels`, `Subscriptions`, `Meta`, `Identities`, `Sessions.ListSessions`, Task 2's `Messages.EmbedFailures`.
- Produces:

```go
package channels
type Service struct{ *core.Core }
func (s Service) EnsureGeneral(ctx) error                         // Decision 92: create "general" with Creator "" and the configured default ACL: cfg.DefaultACL("") when cfg.DefaultACL is set, else nil (the Store writes store.DefaultACL("")); either way no creator entry, administrators act through the implicit admins entry. Unless it exists; idempotent; ErrConflict from a concurrent creator is success. Subscribes nobody: Decision 92's "every identity is subscribed to general" happens at registration, Milestone 5 (and Milestone 5 migrates nothing here; this milestone registers identities only through the test harness).
func (s Service) List(ctx, identity string) ([]bus.Channel, error)                       // Decision 94: every channel, by name
func (s Service) Create(ctx, identity string, in bus.CreateChannelInput) (bus.Channel, error) // ChannelCreation "open": any registered identity; "admins": RequireAdmin; name via Store (NameRule, Decision 93); kind default ordinary; Creator = identity; ACL from bus (validation on unknown permission or empty list); conflict on a taken name
func (s Service) Delete(ctx, identity string, in bus.DeleteChannelInput) error            // admin on the channel (Decision 89); general refused (pending item 6)
func (s Service) Rename(ctx, identity string, in bus.RenameChannelInput) error           // admin; general refused; conflict on a taken new name
func (s Service) ACL(ctx, identity, channel string) ([]bus.ACLEntry, error)              // admin
func (s Service) SetACL(ctx, identity string, in bus.SetACLInput) error                   // admin; entries validated by the Store (Decisions 80, 96); publishes Hint{ChannelID, 0}
func (s Service) Subscribe(ctx, identity string, in bus.SubscribeInput) error            // read; From "" | "now" (acked mark = MaxSeq) | "oldest" (max(EvictedBeforeSeq-1, 0)); other → validation; idempotent
func (s Service) Unsubscribe(ctx, identity string, in bus.UnsubscribeInput) error        // no permission (Decision 95); unknown channel → not_found; not subscribed → no-op
func (s Service) SubscribeTags(ctx, identity string, in bus.TagSubscribeInput) error     // rules.NormalizeTagPatterns; 1..10; CreatedSeq = MaxSeq; idempotent
func (s Service) UnsubscribeTags(ctx, identity string, in bus.TagSubscribeInput) error
func (s Service) TagSubscriptions(ctx, identity string) ([][]string, error)
func (s Service) Status(ctx, identity string) (bus.Status, error)                        // RequireAdmin (Decision 73); live sessions of every identity; channels; both notices; when an embedder is set: EmbeddingBacklog = len(UnembeddedMemories(model, SearchPageMax)), EmbeddingRejected and EmbeddingRejectedLast from EmbedFailures(model) (Task 2), EmbeddingError/At from the "embedding" notice; all zero without one
```

- [ ] **Step 1: Write the failing conformance tests** (both engines through `newHarness`; names prefixed `TestChannel`, `TestSubscription`, `TestAdmin`)

`channels_test.go`: `TestChannelCreateDefaults` (kind ordinary, creator, `ACL` by the creator shows the Decision 88 rows), `TestChannelCreateWithKindAndACL` (tasks kind; own ACL stored as given; `[]` stores none), `TestChannelCreateRejects` (`a/b` validation; `Kind("dm")` validation; `Permission("owner")` validation; duplicate conflict; `ghost` entry validation, Decision 96), `TestChannelCreationSwitch` (Decision 93; cfg `ChannelCreation: "admins"`: `kim` forbidden, admin `root` succeeds; `"open"`: `kim` succeeds), `TestChannelListNeedsNoRead` (Decision 94: a channel with ACL `[{pat,[read]}]` is listed for `kim` with name and kind), `TestChannelACLNeedsAdmin` (`kim` with read only: `ACL` and `SetACL` forbidden; creator succeeds; an administrator without rows succeeds; after `SetACL` removing the creator's rows the creator still edits, Decision 80), `TestChannelDeleteAndRenameNeedAdmin` (refusal for `kim`; success for the creator; rename keeps the id: a subscription made before the rename is listed under the new name), `TestChannelGeneralIsProtected` (`// pending Eric: Milestone 3 item 6`; `EnsureGeneral` twice is one channel; delete and rename of `general` are `validation` for an administrator; `general` has Creator `""` and the `store.DefaultACL("")` rows; with cfg `DefaultACL` set to a function returning `[{everyone,[read]}]`, a fresh database's `general` has that row and no creator entry; nobody is subscribed by `EnsureGeneral`), `TestChannelEnsureGeneralConcurrent` (two goroutines; one channel; no error).

`subscriptions_test.go`: `TestSubscriptionNeedsRead` (ACL `[{pat,[write]}]`: `pat` forbidden; add read: succeeds), `TestSubscriptionFromNowAndOldest` (acked mark through the Store: `now` = MaxSeq at the call; `oldest` = evicted-1; `From: "later"` validation), `TestSubscriptionUnsubscribeNeedsNothing` (Decision 95: read revoked after subscribing, unsubscribe still succeeds; unknown channel not_found), `TestSubscriptionTagsRoundTrip` (patterns normalized and sorted; `env:*` kept; 11 patterns validation; `TagSubscriptions` lists sets by key; unsubscribe removes one set).

`admin_test.go`: `TestAdminStatusNeedsAdmin` (`kim` forbidden; `root` sees two live sessions opened through the Store with future leases, the channels by name, a notice set through the Store), `TestAdminStatusEmbeddingCounters` (harness with the fake embedder: two unembedded memories and one `PutEmbedFailure` through the Store → `EmbeddingBacklog 2`, `EmbeddingRejected 1`, `EmbeddingRejectedLast` the stored reason; a harness without an embedder reports zeros), `TestAdminDeleteAndRenameAreTheChannelOperations` (the `Admin` methods behave as `Messaging`'s: a channel admin who is not a server admin succeeds; a stranger is forbidden).

- [ ] **Step 2: Run to verify they fail**

Run: `go test -count=1 -p 1 -run '^Test(Channel|Subscription|Admin)' ./internal/conformance/bus/`
Expected: `--- FAIL` with `ErrNotImplemented` on both engines.

- [ ] **Step 3: Implement**; delete the two stubs. Every mutation is one `Store.Tx` wrapped in `WithReceipt`; `Create` publishes nothing (no message); `SetACL` publishes a zero-seq hint so an armed watch re-checks readability.

- [ ] **Step 4: Run the suite and the gate**

Run: `env -u AGENTBUS2_TEST_POSTGRES_DSN go test -count=1 -p 1 -v ./internal/conformance/bus/ 2>&1 | grep -E '^(--- FAIL|ok|FAIL)'`, then `make verify` (600000 ms).
Expected: no `--- FAIL`; `verify: OK`.

- [ ] **Step 5: Commit**

```bash
git add internal/core/channels internal/bus/local internal/conformance/bus
git commit -m "feat(core): channels with kinds and ACL operations, subscriptions, tag subscriptions, admin status

Ports CreateChannel, ListChannels, deleteChannel and RenameChannel from v1
internal/bus/channels.go, Subscribe and Unsubscribe from v1
internal/bus/receive.go, SubscribeTags, UnsubscribeTags and
TagSubscriptions from v1 internal/bus/tags.go and StatusReport from v1
internal/bus/admin.go, under the channel ACL check (Decisions 80, 89, 93,
94, 95), with general created by the server (Decision 92)."
```

---

### Task 5: Sessions: open, resume, close, display names, offered cursors, identities

**Depends on:** 3.

**Files:**
- Create: `internal/core/sessions/service.go`, `open.go` (open and resume, display names, offered cursors), `close.go`, `identities.go`
- Delete: `internal/bus/local/sessions_stub.go`; Create: `internal/bus/local/sessions.go`
- Test: `internal/conformance/bus/sessions_test.go`; unit: `internal/core/sessions/display_test.go` (pure display-name rule)

**Port from v1:** `internal/bus/sessions.go` (`RegisterWithClient`: the display-name walk `base`, `base-2`, ... re-shaped to Decision 39; the pending-count query; `Discover`/`liveSessions` for `Identities`; `EndSessions` → `Close`). Not ported: `owner`, `reused`, `harness*`, `context`, `resume=false` subscription wipe (a v2 session never drops the identity's subscriptions), `restore`/`remember`.

**Interfaces:**
- Consumes: Task 3's `Core`, `RequireIdentity`, `IsLive`, `LeaseUntil`, `RandomToken`, `ReadableChannelIDs`; Store `Sessions`, `Subscriptions`, `Channels`, `Identities`, `Meta.MaxSeq`.
- Produces:

```go
package sessions
type Service struct{ *core.Core }
func DisplayName(identity, hint string, taken []string) string // choice 5; pure; the caller passes the names of ListSessions(identity) rows with ClosedAt == 0 (live and lapsed-resumable), never closed rows
func (s Service) Open(ctx, identity string, in bus.OpenSessionInput) (bus.Session, error)
// Order: RequireIdentity; hint "" or NameRule (validation); ResumeToken "" → new; else sha256 → SessionByResumeTokenHash: not found → new with Resumed false (Decision 18); ClosedAt != 0 → session_closed; another identity's → not_found; else revive: RenewLease, cursors and pending batch untouched, Resumed true, and the same token returned (Decision 79: the token stays valid while the row is retained; the Store has no method to rotate the hash and nothing decided a rotation).
// New: id = RandomHex(16), token = RandomToken(32), DisplayName over the ListSessions(identity) rows with ClosedAt == 0, OpenSession{LeaseExpiresAt: LeaseUntil, TagsCursorSeq: identity.TagsAckedSeq}; one cursor per subscription at its AckedSeq (Decision 75), then offered cursors (Decision 44): unknown or unsubscribed channel → "unsubscribed"; no read → "unreadable"; Seq < EvictedBeforeSeq-1 → "before_retention"; Seq > MaxSeq → "ahead"; else PutCursor(Seq). Pending per subscription = count of live messages above the cursor, capped at ReceiveMaxCount (MessagesAfter with that limit; a known cost, up to ReceiveMaxCount rows loaded per subscription to count them, where v1 used count(*); a `CountAfter` Store method is a later amendment if it shows in profiling); PendingChannel listed by channel name.
func (s Service) Close(ctx, identity string, in bus.CloseSessionInput) error   // own session only (not_found otherwise); CloseSession(now) and RenewLease(now) so the retention sweep removes it; idempotent (closed twice is fine)
func (s Service) Require(ctx, tx store.Tx, identity, sessionID string) (store.Session, error) // shared by Task 10: not_found unless the row belongs to identity; session_closed; session_lapsed (pending item 2); renews the lease
func (s Service) Identities(ctx, identity string) ([]bus.IdentityInfo, error) // RequireIdentity; every identity with at least one live session, by name; sessions by OpenedAt
```

- [ ] **Step 1: Write the failing tests**

`display_test.go`: `TestDisplayName` table: no rows → `pat`; `[pat]` → `pat-2`; `[pat, pat-2]` → `pat-3`; hint `impl` with none → `pat/impl`; `[pat/impl]` → `pat/impl-2`; a taken `pat-2` with `pat` free → `pat` (the function sees only what the caller passes; the closed-row exclusion is the service's and is tested in `sessions_test.go`).

`sessions_test.go` (prefix `TestSession`): `TestSessionOpenAssignsNamesAndToken` (three opens: `pat`, `pat-2`, `pat/impl`; tokens distinct and 43 characters; `Resumed false`; `Pending` lists each subscription with its backlog count), `TestSessionOpenRejects` (unregistered identity not_registered; hint `a/b` validation; hint of 129 bytes validation), `TestSessionStartsFromAckedMark` (Decision 75: subscribe at seq 0, advance the acked mark to 5 through the Store, open → cursor 5 through `Cursors`), `TestSessionOfferedCursors` (Decision 44, one case per reason plus one honored; the server cursor is used for every rejected one; `RejectedCursors` names the reason), `TestSessionResumeRestores` (open, put a cursor and a pending token through the Store, let the lease lapse on the clock, open with the token: same id and display name, cursors and pending token intact, `Resumed true`, the same token; opening with it again revives again), `TestSessionResumeAfterRetention` (delete the row through `DeleteSessions`; the token opens a fresh session, `Resumed false`), `TestSessionResumeClosed` (`session_closed`), `TestSessionResumeOtherIdentity` (not_found), `TestSessionLapsedRefusesAndResumes` (`// pending Eric: Milestone 3 item 2`; `Require` through a stub call: after `LeaseSeconds + skew + 1` on the clock the call is `session_lapsed`; within skew it succeeds and renews), `TestSessionDisplayNameUniqueAcrossResumable` (`// pending Eric: Milestone 3 item 11`; a lapsed `pat` still retained → the next open is `pat-2`; then `Close` the lapsed row through its own identity and open again → `pat`; open and close three times in a row → `pat` every time; a closed row past the retention window behaves the same), `TestSessionOpenRetryLeavesLapsedRow` (choice 10: `Open` has no key; two opens with the same hint are two rows with distinct names and tokens, the orphan lapses on the clock and the Task 11 sweep removes it later; asserted through `ListSessions`), `TestSessionCloseIdempotent`, `TestSessionIdentitiesListsLive` (two identities, one lapsed session excluded, admin flag shown).

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/core/sessions/ && go test -count=1 -p 1 -run '^TestSession' ./internal/conformance/bus/`
Expected: build failure, then `ErrNotImplemented`.

- [ ] **Step 3: Implement**; delete the stub. Tokens and ids are generated before `Store.Tx` (Decision 26 rule 2). State (Definition of Done D): a second `Open` with the same token revives the same row again (idempotent); two concurrent opens of one identity get distinct names because `OpenSession` runs under the serializable transaction that read the rows; a crashed client leaves a lapsed row that the Task 11 sweep removes after the retention window.

- [ ] **Step 4: Run the suite and the gate**

As Task 4 Step 4. Expected: no `--- FAIL`; `verify: OK`.

- [ ] **Step 5: Commit**

```bash
git add internal/core/sessions internal/bus/local internal/conformance/bus
git commit -m "feat(core): sessions with leases, display names, resume tokens, offered cursors and the identities list

Ports the display-name walk, pending counts, liveSessions and EndSessions
from v1 internal/bus/sessions.go, re-shaped to Decisions 15, 39, 40, 44,
75 and 79 (resume token stored hashed, Milestone 2 choice 12); harness
identity and owner rows are not ported (Decisions 14, 78)."
```

---

### Task 6: Send and history

**Depends on:** 3.

**Files:**
- Create: `internal/core/messaging/service.go`, `send.go`, `history.go`
- Delete: `internal/bus/local/send_stub.go`; Create: `internal/bus/local/send.go`
- Test: `internal/conformance/bus/send_test.go`, `internal/conformance/bus/history_test.go`

**Port from v1:** `internal/bus/messages.go` (`Send`: shape check, subject and tag normalization before the receipt lookup, the receipt check, channel and `reply_to` lookups, the envelope gate, the rate limit, `insertMessage`, `storeReceipt`, `embedSoon` after commit; `History`: before/after, count defaults and cap, tag patterns, DESC fetch reversed; `validateSendShape` without the DM branch). Not ported: the inspection hook (pending item 5), `checkCapacity` (pending item 3), `IsTaskChannel` (the kind check replaces it), `lockKey` (the serializable transaction serializes same-key retries).

**Interfaces:**
- Consumes: Task 3's `Core`, `Append`, `EnvelopeSize`, `Limits`, `WithReceipt`, `Authorize`, `ChannelByName`, `ToBusMessages`, `Publish`; Task 2's `MessagesInRange` and `MessagesAfter` with filters; `rules`.
- Produces:

```go
package messaging
type Service struct{ *core.Core }
func (s Service) Send(ctx, identity string, in bus.SendInput) (bus.SendResult, error)
// Order: RequireIdentity (in the tx); channel and content required (validation); NormalizeSubject, NormalizeTags; Session "" or a live session of identity (validation otherwise); Store.Append: WithReceipt{ ChannelByName; kind tasks → validation "%s is a task list; use task_create, ..."; Authorize write; EnvelopeSize; Limits.Check (rate_limited); Append }; after commit: Limits.Charge, Publish(Hint{ChannelID, seq}), embedding kick (Task 9's hook, a func on Core set by Task 9: c.EmbedSoon, nil until then) for memory kind
func (s Service) History(ctx, identity string, in bus.HistoryInput) ([]bus.Message, error)
// Store.Read: ChannelByName; Authorize read; count default/cap; patterns normalized; Before set and After not: MessagesBefore(Before, patterns, {}, count) then reversed to ascending (v1 DESC fetch); otherwise MessagesInRange over [After+1, Before-1] (open ends: 1 and MaxSeq) with limit count; TrimToBytes(ResultDefaultKiB) in fetch order before the reversal, as v1
```

- [ ] **Step 1: Write the failing conformance tests** (prefixes `TestSend`, `TestHistory`)

`send_test.go`: `TestSendRoundTrip` (seq 1; `Message` through `History` has every field, sorted tags, normalized subject, `Sender`, `Session` and `Display` of the sending session), `TestSendNeedsWrite` (ACL `[{kim,[read]}]`: kim forbidden; add write: seq returned), `TestSendRejects` (empty content validation; unknown channel not_found; tasks-kind channel validation; 11 tags validation; subject with newline validation; `reply_to` 99 not_found; `Session` of another identity validation), `TestSendMemoryKind` (MemoryID set, revision 1, `memory_access` touched: `ExpiredMemories(now+1, 10)` lists it), `TestSendIdempotencyReplay` (same key and payload twice → same seq, one message), `TestSendIdempotencyConflict` (same key, other content → conflict), `TestSendReplayAfterRetention` (Review Focus 7: delete the message through the Store, resend with the key → the stored seq), `TestSendRateLimited` (cfg `SendMessagesPerSecond: 2`: third send `rate_limited`, `Retryable true`; clock +1 s: one more), `TestSendChargesOncePerCommit` (Review Focus 3: `faultStore` with `40001` once, `SendMessagesPerSecond: 2`; postgres: first send succeeds, second succeeds, third rate_limited, one message, one receipt; sqlite: first send returns an error whose `CodeOf` is `internal`, no message, then two sends succeed), `TestSendPublishesHint` (the harness notifier's subscriber receives `{channelID, seq}` once per send), `TestSendOversized` (`MaxMessageKiB: 1`: 1100 bytes of content validation), `TestSendConcurrentSeqOrder` (Decision 54 at the Bus level, the shape of the Store suite's `seq_test.go`: cfg `SendMessagesPerSecond: 10000`, `SendKiBPerSecond: 100000`; 8 goroutines each holding its own `Bus` value for `pat` (`newBus(h, "pat")`; separate `Bus` values over one `Core` share the limiter, which is why the cfg override is needed) send 25 messages each to `dev` with distinct idempotency keys; every error is collected and the test fails on any; then the channel is drained through `History` paged on `After` (not `Receive`, which is Task 10's) until empty: exactly 200 rows, seqs strictly ascending with no gap from the channel's first seq, `Meta.MaxSeq` equal to the last; on both engines, so the Postgres serialization retries of `Store.Append` are exercised through the core's receipt and limiter wrappers).

`history_test.go`: `TestHistoryNeedsRead` (forbidden then success), `TestHistoryWindowsAndTags` (before/after bounds, newest-first window with `Before` only, count cap at `ReceiveMaxCount`, `env*` pattern filter, tombstoned excluded, ascending order), `TestHistoryTrims` (`ResultDefaultKiB: 1`: large rows trimmed, at least one kept).

- [ ] **Step 2: Run to verify they fail**

Run: `go test -count=1 -p 1 -run '^Test(Send|History)' ./internal/conformance/bus/`
Expected: `--- FAIL` with `ErrNotImplemented`.

- [ ] **Step 3: Implement**; delete the stub. Add to Task 3's `Core` nothing: the embedding kick is `Core.EmbedSoon func()` (a field Task 3 declared as nil-safe; if it is missing on `main`, report to the integrator rather than editing `internal/core/core.go` here).

- [ ] **Step 4: Run the suite and the gate**

As Task 4 Step 4. Expected: no `--- FAIL`; `verify: OK`.

- [ ] **Step 5: Commit**

```bash
git add internal/core/messaging internal/bus/local internal/conformance/bus
git commit -m "feat(core): send with subjects, tags, receipts and per-identity rate limits; history

Ports Send, validateSendShape, History and the envelope gate from v1
internal/bus/messages.go under the write and read ACL checks (Decision 80),
receipts per identity (Decisions 46, 67) and buckets per identity
(Decision 86), with the rate limit charged after commit and the notifier
hint published after commit (Decisions 26, 52)."
```

---

### Task 7: Memories

**Depends on:** 3.

**Files:**
- Create: `internal/core/memories/service.go`, `memories.go`
- Delete: `internal/bus/local/memories_stub.go`; Create: `internal/bus/local/memories.go`
- Test: `internal/conformance/bus/memories_test.go`

**Port from v1:** `internal/bus/memories.go` (`GetMemory`, `MemoryRevisions`, `EditMemory`: subject and tags kept when omitted, tombstone the live revision, delete its embedding, insert the next revision with `memory_id` and `revision + 1`, touch access; `DeleteMemory`; `TouchMemoryAccess` for `Get`). Not ported: the inspection hook, `checkCapacity`, `lockKey`.

**Interfaces:**
- Consumes: Task 3's `LiveRevision`, `TombstoneMemory`, `Append`, `WithReceipt`, `Authorize`, `Limits`, `ToBusMessages`; Store `Memories`, `Messages`.
- Produces:

```go
package memories
type Service struct{ *core.Core }
func (s Service) Get(ctx, identity string, id int64) (bus.Message, error)         // read on the memory's channel (Decision 95); Store.Tx (the access touch is a write, Decision 49): LiveRevision, Authorize, TouchMemoryAccess([id], now); local.Bus.GetMemory delegates here (the Bus name differs because Bus embeds Memories and Tasks, Task 1)
func (s Service) Revisions(ctx, identity string, id int64) ([]bus.Message, error) // read; every revision by revision, tombstoned included; not_found when none
func (s Service) Edit(ctx, identity string, in bus.EditMemoryInput) (bus.EditResult, error)
// content required; subject normalized when given; tags normalized when non-nil ([] clears); Store.Append: WithReceipt{ LiveRevision (not_found); kind tasks → validation; Authorize write; resolve kept subject/tags from the live row; EnvelopeSize; Limits.Check; TombstoneMessage(old, now); DeleteEmbedding(old); Append (ordinary path, then SetMemory(seq, id, rev+1)); TouchMemoryAccess }; after commit: Charge, Publish, EmbedSoon
func (s Service) Delete(ctx, identity string, in bus.DeleteMemoryInput) error    // write; kind tasks → validation; TombstoneMemory; Charge(0 bytes) after commit
```

- [ ] **Step 1: Write the failing conformance tests** (prefix `TestMemory`)

`TestMemoryGetNeedsReadAndTouches` (ACL without read: forbidden; with read: the live revision, and `memory_access` moved to the clock through `ExpiredMemories`), `TestMemoryEditNeedsWrite`, `TestMemoryEditKeepsSubjectAndTags` (omitted subject and nil tags carried; `[]` clears tags; `Subject: ""` pointer clears), `TestMemoryEditRevisionChain` (seq, revision 2, `Replaced` = old seq; `Revisions` lists both; the old embedding row, put through the Store, is gone), `TestMemoryEditRejects` (unknown id not_found; tasks-kind memory validation; empty content validation; 11 tags validation), `TestMemoryDelete` (tombstoned; `Get` not_found; `Revisions` still lists it; receipt replay returns nil twice), `TestMemoryEditIdempotent` (same key twice → same `EditResult`, one new revision).

- [ ] **Step 2: Run to verify they fail**

Run: `go test -count=1 -p 1 -run '^TestMemory' ./internal/conformance/bus/`
Expected: `--- FAIL` with `ErrNotImplemented`.

- [ ] **Step 3: Implement**; delete the stub.

- [ ] **Step 4: Run the suite and the gate**

As Task 4 Step 4. Expected: no `--- FAIL`; `verify: OK`.

- [ ] **Step 5: Commit**

```bash
git add internal/core/memories internal/bus/local internal/conformance/bus
git commit -m "feat(core): memories with revisions, tombstones and access touches

Ports GetMemory, MemoryRevisions, EditMemory, DeleteMemory and
TouchMemoryAccess from v1 internal/bus/memories.go (v1 ADR 0013) under the
read and write ACL checks (Decision 95), with the idempotency key on
edit_memory (Decision 46)."
```

---

### Task 8: Tasks

**Depends on:** 3.

**Files:**
- Create: `internal/core/tasks/service.go`, `doc.go` (task document, `loadTasks`, tree, links, ranks), `create.go`, `update.go` (patch, claim, release), `reclaim.go` (abandonment, `ReclaimAbandoned`, `ReclaimChannel`), `read.go` (get, list)
- Delete: `internal/bus/local/tasks_stub.go`; Create: `internal/bus/local/tasks.go`
- Test: `internal/conformance/bus/tasks_test.go`; unit: `internal/core/tasks/doc_test.go` (pure: tree order, `placeRank`, `validateTaskLinks`, `applyPatch`)

**Port from v1:** `internal/bus/tasks.go` (`Task`, `TaskSummary`, `taskDoc`, `taskContent`, `isTaskStatus`, `validateTaskSubject`, `dedupeIDs`, `loadTasks` over `LiveMemories`, `fillDerived`, `taskByID`, `effectiveParent`, `taskTree`, `followsToSelf`, `validateTaskLinks`, `rankStep`, `rankCollision`, `rerankSiblings`, `placeRank`, `TaskCreate`, `TaskGet`, `TaskList`, `tryFixUpAbandoned`), `internal/bus/tasks_update.go` (`TaskPatch`, `TaskUpdateResult`, `openBlockers`, `ownerGuardBlocked`, `applyPatch`, `writeTaskRevision`, `TaskUpdate`, `TaskClaim`, `TaskRelease`), `internal/bus/tasks_reclaim.go` (`abandoned`, `abandonedSet`, `reclaimAbandoned`, `fixUpAbandoned`; `reclaimAllAbandonedTasks` becomes Task 11's job over `ListChannelsByKind(tasks)`). Not ported: `IsTaskChannel`/`TaskPrefix` (the stored kind, Decision 90), the inbox owner check (Decision 81), `livenessCutoff` grace (pending item 10), the inspection hook, `checkCapacity`, `lockKey`.

**Interfaces:**
- Consumes: Task 3's `Append` (with `TaskRow: true`), `LiveRevision`, `WithReceipt`, `Authorize`, `Limits`, `LiveSessions`, `RequireIdentity`; Store `Tasks`, `Messages`, `Identities`; `rules.Rank*`.
- Produces:

```go
package tasks
type Service struct{ *core.Core }
func (s Service) Create(ctx, identity string, in bus.TaskCreateInput) (bus.Task, error)   // channel of kind tasks (validation "%q is not a task list"); write; subject rule; links; rank; Append TaskRow; receipt; Charge after commit
func (s Service) Get(ctx, identity string, id int64) (bus.Task, error)                      // local.Bus.GetTask delegates here; read on the list; reclaims the task first when abandoned (its own Store.Append, since the reclaimed revision is an AppendMessage, as v1 tryFixUpAbandoned; failures logged), then reads
func (s Service) List(ctx, identity string, in bus.TaskListInput) ([]bus.TaskSummary, error) // read; status filter validated; reclaim pass first
func (s Service) Update(ctx, identity string, p bus.TaskPatch) (bus.TaskUpdateResult, error) // write; v1 pipeline: receipt, live revision, kind, reclaim target, applyPatch with ownerKnown = Identity(owner) exists and is not reserved (Decision 81), rank collision rerank, forced delete marker, Charge after commit
func (s Service) Claim(ctx, identity string, in bus.TaskClaimInput) (bus.TaskUpdateResult, error)
func (s Service) Release(ctx, identity string, in bus.TaskReleaseInput) (bus.TaskUpdateResult, error)
func (s Service) ReclaimChannel(ctx, channelID int64, now int64) (int, error)               // Task 11's job body: one Store.Append per list; abandoned = in_progress with (LeasedUntil != 0 && LeasedUntil <= now) or an owner with no live session (LiveSessions empty); revision type "reclaimed", sender ""
```

- [ ] **Step 1: Write the failing tests**

`doc_test.go`: port v1 `tasks_test.go`'s pure cases for `taskTree` order, `placeRank` (append, before, after, collision), `validateTaskLinks` (self parent, cycle, 65 blockers) and `tasks_update_test.go`'s `applyPatch` rules 1 to 11 as a table (owner guard, delete with children conflict, status transitions, lease validation, no-op), with `ownerKnown` a map.

`tasks_test.go` (prefix `TestTask`): `TestTaskCreateNeedsWriteAndKind` (ordinary channel validation; ACL without write forbidden; with write: id, rank `V`, status pending), `TestTaskGetAndListNeedRead`, `TestTaskClaimUpdateRelease` (claim sets owner and in_progress with lease; another identity's update conflict; `Force` writes a `forced` revision; release clears), `TestTaskOwnerRegistryCheck` (Review Focus 5: owner `ghost`, `everyone`, `admins` → not_found; a registered identity with no session is accepted), `TestTaskAbandonedByLease` (claim with `LeasedUntil` now+10 s; clock +11 s; `Get` returns pending with a `reclaimed` revision in `Revisions`), `TestTaskAbandonedByDeadOwner` (owner's only session lapses on the clock; `List` reclaims), `TestTaskReclaimChannelIdempotent` (second call reclaims 0), `TestTaskIdempotentCreate` (same key → same task, one row), `TestTaskRateLimited`.

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/core/tasks/ && go test -count=1 -p 1 -run '^TestTask' ./internal/conformance/bus/`
Expected: build failure, then `ErrNotImplemented`.

- [ ] **Step 3: Implement**; delete the stub. `writeTaskRevision` is `TombstoneMessage(old)` then `Append` with `Type` and `TaskRow: true` then `SetMemory(seq, id, rev+1)`, inside the caller's `Store.Append` transaction; the reclaim in `Update` shares that transaction (a refused patch rolls the reclaim back, as v1).

- [ ] **Step 4: Run the suite and the gate**

As Task 4 Step 4. Expected: no `--- FAIL`; `verify: OK`.

- [ ] **Step 5: Commit**

```bash
git add internal/core/tasks internal/bus/local internal/conformance/bus
git commit -m "feat(core): tasks with ranks, links, leases and abandoned-task reclaim on channels of kind tasks

Ports v1 internal/bus/tasks.go, tasks_update.go and tasks_reclaim.go (v1
ADR 0005, 0013) over LiveMemories; the owner is checked against the
identity registry (Decision 81), a task list is the stored kind tasks
(Decision 90), owner liveness is a live session of the owner identity."
```

---

### Task 9: Search and embeddings

**Depends on:** 3.

**Files:**
- Create: `internal/core/search/service.go`, `search.go` (text, semantic, both, paging), `internal/core/embed/http.go` (the HTTP embedder, implements `core.Embedder`), `internal/core/embed/batch.go` (`EmbedBatch`, `EmbedSoon`)
- Delete: `internal/bus/local/search_stub.go`; Create: `internal/bus/local/search.go`
- Test: `internal/conformance/bus/search_test.go`, `internal/conformance/bus/embed_test.go`; unit: `internal/core/embed/http_test.go` (httptest server: request shape, response validation, `rowRejected` codes), `internal/core/search/query_test.go` (mode and cursor validation)

**Port from v1:** `internal/bus/search.go` (`Search`: query required, `searchTerms` → `search.Parse` empty check, channel scoping, count and cursor, mode default `both` when an embedder exists, paging; `textSearch` through `SearchText`; `rankedSearch` fallback with `SemanticUnavailable`; `semanticSearch` over `Embeddings` + `MessagesBySeqs` and `dot()`; `rrf`), `internal/bus/embed.go` (`embedder`, `embed` with the strict response validation, `embedHTTPError`, `rowRejected`, `noteEmbed`, `embedBatch` with the row-by-row retry of v1 ADR 0018, `embedTimed`, `embedBatchSize` 64, `embedRequestTimeout` 30 s), `internal/bus/maintenance.go` (`embedSoon`). Not ported: `readKeyFile` and the key-file lookup (the key is given to `embed.NewHTTP` by Milestone 5's configuration), the DM filter branch.

**Interfaces:**
- Consumes: Task 2's `SearchText`, `Embeddings`, `MessagesBySeqs` with `SearchFilter`; Task 3's `Authorize`, `ReadableChannelIDs`, `ToBusMessages`, `Embedder`; Store `Memories.TouchMemoryAccess`, `Messages.UnembeddedMemories`, `PutEmbedding`, `PutEmbedFailure`, `DeleteStaleModel`; `rules.Dot`, `DecodeVec`, `EncodeVec`, `RRF`, `TrimToBytes`.
- Produces:

```go
package search
type Service struct{ *core.Core }
func (s Service) Search(ctx, identity string, in bus.SearchInput) (bus.SearchResult, error)
// Before any transaction: query non-blank, search.Parse non-empty (validation "query must contain a word"); Mode "" → "both" with an embedder else "text"; unknown → validation; Cursor a non-negative int; for semantic and both, Embedder.Embed(query) under EmbeddingQueryTimeoutSeconds (HTTP never runs inside fn, Decision 26; a failure sets SemanticUnavailable and falls back to text). Then one Store.Tx (the touches are writes): Channel set: ChannelByName + Authorize read, channelIDs = [id]; else ReadableChannelIDs (Decisions 80, 95); filter from Sender/Since/Until/Thread/Tags; text: SearchText(limit SearchPageMax) reversed to seq desc, Score 0; semantic: Embeddings → dot against the pre-fetched query vector → top SearchPageMax → MessagesBySeqs; both: RRF; page = ranked[cursor:cursor+count]; TrimToBytes with the exact reserve (v1); TouchMemoryAccess over the page's memory ids; Next when more remain
```

```go
package embed
type HTTP struct{...}; func NewHTTP(endpoint, model, apiKey string, client *http.Client) *HTTP // core.Embedder
func EmbedBatch(ctx, c *core.Core) (n int, err error) // v1 embedBatch: a Read for the candidates (DeleteStaleModel in its own Tx when needed; UnembeddedMemories(model, 64)); the HTTP calls outside any transaction (Embed; row retry on rowRejected); then one Tx for the rows: PutEmbedding / PutEmbedFailure (the Store skips rows no longer live memories); notice "embedding" set on error, cleared on success
func Install(c *core.Core) // sets c.EmbedSoon to a TryLock-guarded goroutine running EmbedBatch (v1 embedSoon); no-op when c.Embedder() == nil
// State (Definition of Done D): a second EmbedBatch finds the rows already embedded and does nothing; two cores over one database (local multi-process, or two Milestone 7 replicas) may embed the same backlog twice, and PutEmbedding and PutEmbedFailure replace, so the result is identical and the only cost is the duplicate HTTP calls (the Task 11 lease makes this rare: only the lease holder's tick runs the batch step, the kick after Send and Edit is per process); a batch killed between the HTTP calls and the row transaction leaves the rows unembedded for the next batch.
```

- [ ] **Step 1: Write the failing tests** (prefixes `TestSearch`, `TestEmbed`)

`search_test.go`: `TestSearchMatchesAgreeAcrossEngines` (the Milestone 2 corpus rows r1 to r18 appended through the harness on channels A and B and the same query table, through `Search` in `text` mode, each expected set asserted identically on both engines as ascending seqs after sorting; plus the filters: `Sender`, `Since`, `Thread`, `Tags: ["env*"]` each narrowing one query identically), `TestSearchACLScoping` (Review Focus 8: a matching row on a channel whose ACL excludes `kim` never appears unscoped; scoped → forbidden; all three modes with the fake embedder), `TestSearchSemanticAndBoth` (fake embedder: memories `alpha beta`, `gamma`, `alpha`; query `alpha` ranks the two alpha rows first by dot; `both` returns the RRF order; `SemanticUnavailable` when `fakeEmbedder.Fail`; `semantic` mode without an embedder falls back with the flag), `TestSearchPagingAndTrim` (count 2, cursor `2`, `Next`; invalid cursor validation; unknown mode validation; empty query validation; `*` validation), `TestSearchTouchesHitsOnly` (access rows of the returned page moved to the clock, the rest not).

`embed_test.go`: `TestEmbedBatchEmbedsLiveMemoriesOnly` (ordinary rows and tasks-kind memories skipped; tombstoned skipped; an edited memory's old revision not embedded), `TestEmbedBatchRecordsRowFailure` (fake embedder rejecting the text `bad` with a 422-class error: `embed_failures` row, the rest embedded, notice cleared), `TestEmbedBatchNotesEndpointError` (every call fails: notice `embedding` set with the error; cleared on the next success), `TestEmbedBatchStaleModel` (an embedding under model `old` is deleted). `http_test.go`: `TestHTTPEmbedRequestAndValidation` (the request JSON, bearer header when a key is given, index/dimension validation errors, 401 text names the key).

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/core/search/ ./internal/core/embed/ && go test -count=1 -p 1 -run '^Test(Search|Embed)' ./internal/conformance/bus/`
Expected: build failure, then `ErrNotImplemented`.

- [ ] **Step 3: Implement**; delete the stub. The conformance tests never rely on the background kick: they call `embed.EmbedBatch(ctx, h.core)` synchronously after the writes, so results are deterministic; `Install` is covered by the unit test `TestInstallRunsBatchOnce` in `internal/core/embed/batch_test.go` (SQLite, a fake embedder, `EmbedSoon` twice in a row runs one batch, then one more after it returns).

- [ ] **Step 4: Run the suite and the gate**

As Task 4 Step 4. Expected: no `--- FAIL`; `verify: OK`.

- [ ] **Step 5: Commit**

```bash
git add internal/core/search internal/core/embed internal/bus/local internal/conformance/bus
git commit -m "feat(core): search over the core query language with Go dot() scoring, RRF and the embedding batches

Ports Search, textSearch, rankedSearch, semanticSearch and rrf from v1
internal/bus/search.go (v1 ADR 0019) over the Store translations
(Decision 51), scoped to readable channels (Decisions 80, 95) with the
access touch inside Search (Decision 49), and the embedder, embedBatch and
embedSoon from v1 internal/bus/embed.go and maintenance.go (v1 ADR 0018)."
```

---

### Task 10: Delivery: receive and watch

**Depends on:** 4, 5, 6.

**Files:**
- Create: `internal/core/delivery/service.go`, `receive.go` (the receive pipeline), `peek.go` (the wake check shared by receive's selection and watch), `watch.go` (registration, replaced, merge window, notifier loop)
- Delete: `internal/bus/local/delivery_stub.go`; Create: `internal/bus/local/delivery.go`
- Test: `internal/conformance/bus/receive_test.go`, `internal/conformance/bus/watch_test.go`

**Port from v1:** `internal/bus/receive.go` (`receiveOnce`: load subscriptions, ack, gaps, the pending redelivery branch with `redeliveryHardCeilingBytes` and the re-stamped pending ends, the new-selection branch with the exact reserve, `makeBatchToken` with the `:o0`/`:o1` suffix, `maxBatchTokenLen`, `maxNoticesPerReceive`, `srcOf`/`attribute` with `matchedTags`; re-keyed from the registered name to the session, Decision 16), `internal/bus/wait.go` (`peek`: the floor per source `max(cursor, pending_end, above)`, the pending batch never wakes, v1 ADR 0012; `WaitAbove`'s floor rule for the `After` of Decision 42), `internal/cli/watch.go` (the merge gap, as the server-side `WatchMergeSeconds`). Not ported: `Receive`'s wait loop and `WaitSeconds` (Decision 83), the polling guard (pending item 9), `Expired` (pending item 4), `NoPollGuard`, the inbox and `dmReadable` branches, the `tags/` row.

**Interfaces:**
- Consumes: Task 5's `sessions.Service.Require`; Task 4's subscriptions in the Store; Task 2's `MessagesAfter`, `MessagesInRange`, `TagMatchesAfter` with `MessageFilter`; Task 3's `Authorize` (through `ReadableChannelIDs`), `ToBusMessages`, `Notifier`, `IsLive`; Store `Sessions` (cursors, pending token, tags cursor, `RegisterWatch`), `Subscriptions` (`AdvanceAckedMark`, `TagSubscriptions`), `Identities.AdvanceTagAckedMark`, `Channels.Channel` (evicted boundary), `Meta.Notice("capacity")`.
- Produces:

```go
package delivery
type Service struct{ *core.Core; Sessions sessions.Service }
func (s Service) Receive(ctx, identity string, in bus.ReceiveInput) (bus.ReceiveResult, error)
// Count default/cap; token minted before the Tx (RandomHex(12) + ":o0"/":o1"); one Store.Tx:
//  1 Require(session) (renews the lease; session_lapsed/closed/not_found);
//  2 sources: Subscriptions(identity) joined with Cursors(session): a subscription without a cursor gets one at AckedSeq (PutCursor); a cursor without a subscription is deleted; a source whose channel the identity cannot read now (ReadableChannelIDs) is skipped untouched (Review Focus 1); the tag source is the session's tags cursor when TagSubscriptions is non-empty, over readable channels minus the directly subscribed ones (Decision 95, v1 ADR 0009 item 7);
//  3 ack: in.Ack == session.PendingToken → every cursor with PendingEndSeq > 0: CursorSeq = PendingEndSeq, PendingEndSeq = 0, AdvanceAckedMark(identity, channel, end) (Decision 75); tags likewise with AdvanceTagAckedMark; SetPendingToken(""); else AckIgnored = in.Ack != "";
//  4 gaps: CursorSeq < EvictedBeforeSeq-1 → Gap{from cursor+1, to evicted-1}, cursor moved, a pending end at or below it cleared; capped at maxNoticesPerReceive with gapsDeferred as v1;
//  5 pending token set → redelivery bounded by each source's pending end (MessagesInRange cursor+1..end, TagMatchesAfter upTo end) with the token's own include_own, re-stamping ends to the highest seq shown; else the new selection over in.Channels (names → ids) with MessageFilter{ExcludeSessionID: session} unless IncludeOwn (pending item 1), merged by seq, limit Count, TrimToBytes against the exact reserve, pending ends per source and SetPendingToken(token) when anything was delivered;
//  6 Notice = Notice("capacity") when set; Instruction as v1.
func (s Service) Watch(ctx, identity string, in bus.WatchInput) (bus.WatchResult, error)
// Timeout == 0 → WatchMaxSeconds; > WatchMaxSeconds → clamped to WatchMaxSeconds (spec 3.5 "caps the wait"); < 0 → validation; watchID = RandomHex(8) before any Tx; Tx: Require(session), RegisterWatch(session, watchID, LeaseUntil); Publish(Hint{0,0}) so an older in-process watcher re-checks; then loop:
//   peek (Store.Read): Session(id).WatchID != watchID → replaced; for every readable source, floor = max(CursorSeq, PendingEndSeq, in.After), MessagesAfter(floor, filter own unless IncludeOwn, limit ReceiveMaxCount) and TagMatchesAfter likewise; any row → woke;
//   woke: sleep min(WatchMergeSeconds, remaining) (Decision 45), peek once more, return {Woke true, Seq max, Count, Channels sorted unique};
//   not woke: select on the notifier subscription (made before the first peek, so a hint between peek and wait is not lost), ctx.Done (ctx.Err()), or the deadline (Woke false). A hint with Seq 0 only re-checks WatchID and readability.
```

- [ ] **Step 1: Write the failing conformance tests** (prefixes `TestReceive`, `TestWatch`; messages appended through `Send` or the harness)

`receive_test.go`: `TestReceiveBatchAndAck` (two messages → batch of two with a token and the instruction; same call again → `Redelivered`; ack → empty; cursors and the acked mark advanced through the Store), `TestReceiveAckReplayIgnored` (Review Focus 4; plus another session's token), `TestReceiveAckSurvivesRename`, `TestReceiveExcludesOwnSessionOnly` (`// pending Eric: Milestone 3 item 1`: `pat` session A sends; session B of `pat` receives it; session A does not; `IncludeOwn` shows it), `TestReceiveNewSessionContinuesFromAckedMark` (Decision 75: session A acks seq 3 and closes; session B opens without a token and receives from 4), `TestReceiveResumedSessionKeepsPendingBatch` (Decision 40: a pending batch survives a lapse and resume and is redelivered under the same token), `TestReceiveSkipsUnreadableSubscription` (Review Focus 1), `TestReceiveTagMatchNeedsRead` (Decision 95; a tagged message on a readable unsubscribed channel is delivered with `MatchedTags`; the same on an unreadable channel is not; a directly subscribed channel's message arrives once, without `MatchedTags`), `TestReceiveChannelsFilterAndCount` (filter by names; count 1 twice; pending batch from outside the filter still redelivered), `TestReceiveGaps` (retention boundary raised through `SetEvictedBefore`: `Gaps` reported, cursor moved), `TestReceiveNeedsSession` (unknown not_found; another identity's session not_found; lapsed session_lapsed), `TestReceiveTrimsAndRedeliveryCeiling` (`ResultDefaultKiB: 1`, three 600-byte messages: first batch one message, pending end re-stamped to it).

`watch_test.go` (real time, short timeouts: `WatchMergeSeconds: 0` unless the test is about merging; `Timeout` 2 s): `TestWatchWakesOnSend` (a goroutine sends after 100 ms; `Woke`, `Count 1`, `Channels [dev]`, within 2 s), `TestWatchTimeout` (`Timeout` 200 ms, nothing: `Woke false`, no error), `TestWatchIgnoresPendingBatch` (receive without ack, then watch: timeout, v1 ADR 0012; after a new message: wakes with count 1), `TestWatchAfterFloor` (`After` = the last seq: nothing wakes; a new send wakes), `TestWatchReplaced` (a second watch on the same session: the first returns `replaced` within 500 ms; the second is live), `TestWatchReplacedAcrossCores` (Review Focus 6: both cores on the polling notifier, `h := newHarness(t, e, harnessOptions{polling: 50 * time.Millisecond})` and `h.sibling(harnessOptions{polling: 50 * time.Millisecond})`, over one database on both engines; the sibling's `RegisterWatch` publishes only into its own notifier, so `h`'s watcher learns of the replacement only through its poll, which is the point; the older watch on `h` returns `replaced` within 500 ms of the newer registering on the sibling), `TestWatchMergesWakes` (`WatchMergeSeconds: 1`; two sends 100 ms apart after the watch arms: one result with `Count 2`), `TestWatchIgnoresUnreadableChannel` (Review Focus 1), `TestWatchRenewsLease` (lease through the Store is later than before), `TestWatchTagMatchWakes` (Decision 38), `TestWatchClampsTimeoutToCap` (cfg `WatchMaxSeconds: 1`; `Timeout: 10 * time.Minute` with nothing arriving returns `Woke false` between 1 s and 1.5 s; `Timeout: -1` is `validation`), `TestWatchContextCancel` (ctx canceled → `context.Canceled`).

- [ ] **Step 2: Run to verify they fail**

Run: `go test -count=1 -p 1 -run '^Test(Receive|Watch)' ./internal/conformance/bus/`
Expected: `--- FAIL` with `ErrNotImplemented`.

- [ ] **Step 3: Implement**; delete the stub. The two lines Task 13's self-test mutates must appear verbatim: in `peek.go`, `floor := max(c.CursorSeq, c.PendingEndSeq, after)`; in `receive.go`, the readability skip `if !readable[sub.ChannelID] {` with the comment `// Decision 80: no read, no delivery`. State (Definition of Done D): a receive that fails after minting a token minted nothing durable (the token is only written by the transaction); two concurrent receives on one session serialize on the transaction, the second sees the first's pending token and redelivers; a watch whose process dies leaves `watch_id` set, which the next watch replaces.

- [ ] **Step 4: Run the suite and the gate**

As Task 4 Step 4, plus `go test -count=1 -p 1 -race -run '^TestWatch' ./internal/conformance/bus/`. Expected: no `--- FAIL`; `ok` under race; `verify: OK`.

- [ ] **Step 5: Commit**

```bash
git add internal/core/delivery internal/bus/local internal/conformance/bus
git commit -m "feat(core): delivery with batch tokens and session-scoped cursors, the per-identity acked mark, and the watch wake primitive

Ports receiveOnce and makeBatchToken from v1 internal/bus/receive.go
re-keyed to the session (Decisions 16, 40) with the acked mark of Decision
75, peek from v1 internal/bus/wait.go (the pending batch never wakes, v1
ADR 0012) as the watch check with the merge window of Decision 45, watch
registration with replaced semantics through the store (Decision 68) and
the notifier (Decision 52); tag matches scoped to readable channels
(Decision 95)."
```

---

### Task 11: Maintenance

**Depends on:** 7, 8, 9.

**Files:**
- Create: `internal/core/maintenance/service.go` (`Service`, `Tick`, the lease), `jobs.go` (one function per job)
- Test: `internal/conformance/bus/maintenance_test.go`; unit: `internal/core/maintenance/tick_test.go` (SQLite: lease, overlap, deadline)

**Port from v1:** `internal/bus/maintenance.go` (`takeLease` through `TryLease`; `Tick` with its deadline of half the interval and the step list; `loopChunks` with `cleanupChunk` 1000; `deleteOrdinaryChunk` through `OrdinaryMessagesBefore`, `DeleteMessages`, `SetEvictedBefore(max seq + 1)`; the tombstone purge through `ExpiredTombstones`; receipts through `DeleteExpiredReceipts`; `taskExpiry` and `expireStaleChannels` over `ListChannelsByKind(tasks)` and `ChannelLastActivity` with the in-transaction re-check; `memoryExpiryChunk` through `ExpiredMemories` and `TombstoneMemory`; the embedding step over `embed.EmbedBatch`, `embedBatchesPT` 4), `internal/bus/tasks_reclaim.go` (`reclaimAllAbandonedTasks` through `tasks.Service.ReclaimChannel`), `internal/bus/sessions.go` (the stale-session sweep, now `DeleteSessions(now - SessionRetentionHours)`, Decision 79). Not ported: `evictToBudget`, `checkCapacity`, `capacityNotice`, `vacuum` (pending item 3, Milestone 7), `reapEmptyChannels` (pending item 8).

**Interfaces:**
- Consumes: Store `Meta.TryLease`, the retention queries, `Sessions.DeleteSessions`, `Channels.ListChannelsByKind`/`DeleteChannel`, `Tasks.ChannelLastActivity`; Task 3's `TombstoneMemory`; Task 8's `ReclaimChannel`; Task 9's `EmbedBatch`.
- Produces:

```go
package maintenance
type Service struct{ *core.Core; Tasks tasks.Service }
type Job struct { Name string; Run func(ctx context.Context, now int64) (n int, err error) }
func (s Service) Jobs() []Job // in order: "lapsed sessions", "abandoned tasks", "receipts", "message retention", "tombstone purge", "task expiry", "memory expiry", "embeddings"
func (s Service) Tick(ctx context.Context) (ran bool, report []JobResult) // TryLease("maintenance", c.Owner, now, now + 2*interval) in a Tx; false when another owner holds it (Decision 68); deadline now + interval/2; each job in its own transactions, chunked, logged on error, never aborting the tick
func (s Service) RunJob(ctx, name string) (int, error) // one job, no lease (tests and the Milestone 7 operator command); unknown name → validation
```

- [ ] **Step 1: Write the failing tests**

`tick_test.go` (SQLite): `TestTickTakesAndRenewsLease` (a second core over the same path: its `Tick` returns `ran false` while the first holds it; after `2*interval` on the clock the second takes it), `TestTickRunsEveryJobOnce` (a job list with a counting fake; the deadline stops the list when the clock is advanced past it by a job).

`maintenance_test.go` (prefix `TestMaintenance`, both engines, through `RunJob` and `Tick`): `TestMaintenanceLapsedSessions` (a row past the retention window gone with its cursors; a live one kept; a closed one gone once past retention), `TestMaintenanceMessageRetention` (ordinary rows older than `MessageRetentionHours` deleted, memories kept, `Channel(id).EvictedBeforeSeq` through the Store raised to the highest deleted seq plus one; the gap `Receive` then reports is Task 10's `TestReceiveGaps`, not asserted here, because Task 11 runs beside Task 10), `TestMaintenanceTombstonePurge`, `TestMaintenanceReceipts`, `TestMaintenanceTaskExpiry` (a `tasks` list inactive past `TaskExpiryHours` deleted with its tasks; an active one kept; `0` disables), `TestMaintenanceMemoryExpiry` (unaccessed past `MemoryExpiryHours` tombstoned, tasks never, `0` disables), `TestMaintenanceAbandonedTasks`, `TestMaintenanceEmbeddings` (fake embedder: the backlog drains to 0 within `embedBatchesPT` batches), `TestMaintenanceTickIdempotent` (a second `Tick` after the clock passes the lease changes nothing more), `TestMaintenanceRunJobRejectsUnknown`.

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/core/maintenance/ && go test -count=1 -p 1 -run '^TestMaintenance' ./internal/conformance/bus/`
Expected: build failure (`undefined: Tick`).

- [ ] **Step 3: Implement.** State (Definition of Done D): every job is idempotent (a second run finds nothing); two overlapping ticks from two cores are serialized by the lease row, and the loser skips; a tick killed mid-job leaves committed chunks and the lease, which expires after `2*interval`, after which the next tick continues from wherever the queries find work.

- [ ] **Step 4: Run the suite and the gate**

As Task 4 Step 4. Expected: no `--- FAIL`; `verify: OK`.

- [ ] **Step 5: Commit**

```bash
git add internal/core/maintenance internal/conformance/bus
git commit -m "feat(core): maintenance jobs as idempotent functions under the store lease

Ports Tick, takeLease, loopChunks, deleteOrdinaryChunk, the tombstone and
receipt sweeps, taskExpiry and memoryExpiryChunk from v1
internal/bus/maintenance.go, reclaimAllAbandonedTasks from v1
internal/bus/tasks_reclaim.go and the session sweep from v1
internal/bus/sessions.go, over the Store with the lease row of Decisions
49 and 68 and the session retention of Decision 79 (v1 ADR 0013); budget
eviction waits for Milestone 7."
```

---

### Task 12: ACL matrix: every section 4.6 row

**Depends on:** 7, 8, 9, 10 (every `Service` the rows drive; it runs beside Task 11, which it does not use).

**Files:**
- Test: `internal/conformance/bus/aclmatrix_test.go`

**Interfaces:** consumes every `Service` through the `Bus` interfaces and the harness.

- [ ] **Step 1: Write the matrix**

`TestACLMatrix` (prefix `TestACL`): a table with exactly fifteen rows, one per row of the section 4.6 table (`subscribe`; `receive, watch wake, history, search, filter and query`; `send (publish, post), tag a message`; `get the ACL, set the ACL`; `rename`; `delete a channel`; `create a channel`; `list channels`; `unsubscribe`; `tag subscription match on a channel`; `memory get, revisions`; `memory edit, memory delete`; `task get, task list`; `task create, claim, release, update`; `offered cursor on a channel`), each `{row string; perm bus.Permission /* "" for none */; ops []struct{ name string; op func(t, h, b bus.Bus, ch string) error }; setup func(...)}`, run under `newHarness`, so the engine is the outermost subtest: `TestACLMatrix/<engine>/<row>/<op>/<refusal|success>`. The fifteen rows carry twenty operations between them (the sub-cases listed below); the count of rows is the spec's, the count of operations is this test's. Row 2's "filter and query": `-filter` is not ported (Decision 12) and "query" is `search`, so the row's sub-cases are `receive`, `watch wake`, `history` and `search`, with that sentence in the row's comment. For each row the test builds a channel `m` created by `pat` whose stored ACL is `[{kim, [<the two permissions the row does not need>]}]` for the refusal case and `[{kim, [<the row's permission>]}]` for the success case, then runs `op` as `kim` and asserts `forbidden` and success respectively. The rows and their discriminating operation: `subscribe` (Subscribe), `receive` (a message on `m`, kim subscribed before the revoke, `Receive` returns nothing from `m` versus the message), `watch wake` (as Review Focus 1: no wake versus wake within 2 s), `history`, `search` (scoped), `send`, `tag a message` (Send with tags), `get the ACL`, `set the ACL`, `rename`, `delete`, `create` (the `none` row: `ChannelCreation "open"` succeeds for an identity with no rows anywhere; `"admins"` refuses it and admits `root`), `list channels` (the `none` row: kim with no entries sees `m`'s name and kind; the paired refusal is `History` on the same channel), `unsubscribe` (the `none` row: read revoked, unsubscribe succeeds; the paired refusal is `Subscribe` again), `tag subscription match` (a tag set, a tagged message on `m`: delivered versus not), `memory get, revisions` (`GetMemory` and `Revisions`, one sub-case), `memory edit, delete`, `task get, list` (`GetTask` and `List`, one sub-case), `task create, claim, release, update`, `offered cursor` (`Open` with a cursor on `m`: `RejectedCursors` names `unreadable` versus honored). Where a table row names several operations, each operation is its own sub-case under the row (`<row>/<op>/<refusal|success>`), so one refusal and one success exist per operation named by the table. A final assertion compares the fifteen `row` strings, in order, to a literal copy of the section 4.6 table's first column and fails on any difference (a deleted or renamed row fails).

- [ ] **Step 2: Run it**

Run: `env -u AGENTBUS2_TEST_POSTGRES_DSN go test -count=1 -p 1 -v -run '^TestACLMatrix' ./internal/conformance/bus/ 2>&1 | tee /tmp/agentbus2-aclmatrix.log | grep -cE '^\s+--- FAIL'`, then `for e in sqlite postgres; do grep -cE "^\s+--- PASS: TestACLMatrix/$e/.*/(refusal|success)$" /tmp/agentbus2-aclmatrix.log; done`.
Expected: `0` failures; the two per-engine counts are equal and each is at least 40 (2 × the twenty operations); a failure is a core bug fixed in the owning package in this task (the table stays).

- [ ] **Step 3: Prove it can fail**

Temporarily change one row's refusal ACL to grant the permission; run; confirm `--- FAIL` for that sub-case on both engines; restore.

- [ ] **Step 4: Run the suite and the gate**

As Task 4 Step 4. Expected: no `--- FAIL`; `verify: OK`.

- [ ] **Step 5: Commit**

```bash
git add internal/conformance/bus/aclmatrix_test.go
git commit -m "test(bus): one refusal and one success per section 4.6 row on both engines (Decision 80)"
```

---

### Task 13: Integration, milestone exit and docs

**Depends on:** 11, 12.

**Files:**
- Delete: `internal/bus/local/local.go`'s `ErrNotImplemented` (no stub may survive); Modify: `internal/conformance/bus/bus_test.go` (`TestLocalBusImplementsEveryInterface` now calls one cheap method per interface and expects success), `scripts/check-gates-selftest.sh` (blocks 9 and 10), `CLAUDE.md`, `README.md`, `docs/adr/0003-identity-sessions-and-delivery.md` and `docs/adr/0008-channel-access-control-lists.md` (Milestone 3 addenda), `HANDOFF.md` (untracked)

- [ ] **Step 1: No stub survives**

`rg -n 'ErrNotImplemented|_stub\.go' internal/` prints nothing after the deletion; `rg -n 'pending Eric: Milestone 3' internal/` lists exactly the items of the pending list still unanswered.

- [ ] **Step 2: The milestone exit, positive**

Run (600000 ms or in the background): `env -u AGENTBUS2_TEST_POSTGRES_DSN make verify 2>&1 | tee /tmp/agentbus2-m3-verify.log | tail -3`, `env -u AGENTBUS2_TEST_POSTGRES_DSN make test-race 2>&1 | tail -4`.
Expected: `verify: OK`; `ok ... internal/conformance/bus` with no `--- FAIL` and no `SKIP`; `test-race` `ok` everywhere. Then, over `env -u AGENTBUS2_TEST_POSTGRES_DSN go test -count=1 -p 1 -v ./internal/conformance/bus/ 2>&1 | tee /tmp/agentbus2-m3-bus.log`: `grep -cE '^\s+--- PASS: TestACLMatrix/sqlite/.*/(refusal|success)$' /tmp/agentbus2-m3-bus.log` and the same with `postgres` print the same number, at least 40 each (twenty operations × refusal and success; the engine is the outermost subtest, as `newHarness` under `dbtest.ForEachEngine` nests it), and `grep -cE '^\s+--- FAIL' /tmp/agentbus2-m3-bus.log` prints 0 (the exit criterion's "every table row has a refusal and a success test"); `grep -cE '^\s+--- PASS: TestSearchMatchesAgreeAcrossEngines/(sqlite|postgres)$' /tmp/agentbus2-m3-bus.log` prints 2 (the "search matching agrees across engines" clause); `grep -cE '^\s+--- PASS: TestSendConcurrentSeqOrder/(sqlite|postgres)$' /tmp/agentbus2-m3-bus.log` prints 2 (Decision 54's concurrent writers at the Bus level).

- [ ] **Step 3: The milestone exit, negative halves** (two blocks of `scripts/check-gates-selftest.sh`, after the Milestone 2 blocks 7 and 8; the harness already mutates a throwaway worktree of `HEAD`)

Block 9 `acl-check-removed`: in the worktree replace the line `if !readable[sub.ChannelID] {` in `internal/core/delivery/receive.go` with `if false {` through a temp file and `cp` (`problem` if `grep -c 'if false {'` is 0), run `go test -count=1 -p 1 -v -run '^TestACLMatrix$' ./internal/conformance/bus/`, expect `--- FAIL: TestACLMatrix/sqlite` and `--- FAIL: TestACLMatrix/postgres` in the log; revert. Block 10 `pending-batch-wakes`: replace `floor := max(c.CursorSeq, c.PendingEndSeq, after)` in `internal/core/delivery/peek.go` with `floor := max(c.CursorSeq, after)` (`problem` if absent after the edit), run `-run '^TestWatchIgnoresPendingBatch$'`, expect `--- FAIL` on both engines; revert. Run after Step 5's commit: `make check-gates-selftest 2>&1 | tail -14`. Expected: `selftest: ok acl-check-removed`, `selftest: ok pending-batch-wakes`, every earlier block `ok`.

- [ ] **Step 4: Docs**

- `CLAUDE.md` Design: one line each for `internal/bus` (the seam; clients never import `store`), `internal/core` and its aggregate packages (rules over the Store; every mutation obeys the Decision 26 rules in the plan's Global Constraints), `internal/bus/local`, and `internal/conformance/bus` (both engines; the ACL matrix). Facts only, checked against the tree.
- `README.md`: `Status: Milestone 3 (core and Bus) done; nothing usable yet.`
- ADR 0003 addendum `## Milestone 3 addendum (date)`: choices 1, 4, 5, 6, 7, 10 (the `Open`, `Receive` and `Watch` key exemptions) and the pending items 1, 2, 4, 9, 10, 11 as `Decision (Eric, <date>)` or `Claude proposal, pending Eric` with their test markers. ADR 0008 addendum: the core check (`Core.Authorize`), the matrix, pending items 6, 7, 8. ADR 0004 addendum: the Task 2 amendment, choice 11, pending item 3.
- `HANDOFF.md` via `/session:handoff`: Milestone 3 done at the Step 5 commit, unpushed; the Step 2 and 3 outputs; the pending items; Milestone 4 next.

- [ ] **Step 5: Commit**

```bash
git add internal/bus/local internal/conformance/bus scripts/check-gates-selftest.sh CLAUDE.md README.md docs/adr
git commit -m "feat(core): Milestone 3 exit: full core and Bus suite on both engines, ACL matrix mutation proofs, docs"
```

- [ ] **Step 6: Report**

End with the Step 2 outputs (verify, race, the per-engine matrix counts, the search-agreement and concurrent-send counts), the Step 3 self-test tail quoted verbatim, and the pending items still open. No push: the next push is Eric's (`Approve: push main to origin (git push origin main)`).

---

## Self-review notes

- Spec coverage, section 10 Milestone 3: rules ported over `Store` (Tasks 4 to 11, each naming its section 9 source), the per-identity acked mark (Tasks 5, 10), rate limits per identity (Tasks 3, 6, 7, 8), the owner registry check (Task 8), the channel ACL check for every section 4.6 row (Task 3's `Authorize`, Task 12's matrix), the split `Bus` interfaces (Task 1), the local Bus (Tasks 3 to 10), the core conformance suite on both engines with search agreement (Task 9), concurrent writers for seq order at the Bus level (Task 6, Decision 54) and a refusal and a success per row (Task 12). Task-text items: batch tokens and session cursors (Task 10), receipts (Task 3), tags and tag subscriptions (Tasks 1, 4, 10), subjects (Tasks 1, 6), memories (Task 7), tasks with leases and reclaim (Task 8), channels and kinds (Task 4), search with the query language and `dot()` (Task 9), maintenance under the lease (Task 11), sessions and display names (Task 5), offered cursors (Task 5), injected clock (Task 3), watch registration with replaced semantics and the notifier with memory and polling forms (Tasks 3, 10).
- Decisions traced: 5, 6, 7, 26, 49, 52, 54 (ADR 0004); 10, 42, 45, 46, 66, 67, 68, 83 (ADR 0001); 8, 12, 15, 16, 17, 18, 37, 38, 39, 40, 44, 73, 75, 77, 79, 86 (ADR 0003); 13, 75, 81 (ADR 0006); 80, 88, 89, 90, 92, 93, 94, 95, 96 (ADR 0008); 31, 32 (ADR 0005); 51 (search).
- Names used across tasks: `bus.Code*`, `bus.Errorf`, `bus.CodeOf`, the input and result types of Task 1; `rules.*`; `core.Config`, `Defaults`, `Options`, `New`, `Core.Authorize`, `RequireIdentity`, `RequireAdmin`, `ChannelByName`, `ReadableChannelIDs`, `WithReceipt`, `Append`, `AppendInput`, `EnvelopeSize`, `LiveRevision`, `TombstoneMemory`, `IsLive`, `LiveSessions`, `LeaseUntil`, `ToBusMessages`, `Publish`, `Embedder`, `EmbedSoon`, `Limits.Check`/`Charge`, `RandomToken`, `RandomHex`, `FromStore`, `E`; `notify.Hint`, `Notifier`, `NewMemory`, `NewPolling` (Task 1); `local.New`; `channels.Service.EnsureGeneral`; `sessions.Service.Require`, `DisplayName`; `tasks.Service.ReclaimChannel`; `embed.EmbedBatch`, `Install`, `NewHTTP`; `maintenance.Service.Tick`, `RunJob`, `Jobs`; the fixtures `newHarness`, `harnessOptions`, `newBus`, `as`, `admin`, `channel`, `append`, `open`, `codeOf`, `fakeClock`, `fakeEmbedder`, `faultStore`, `openStore`; the amended Store signatures of Task 2 and its new `EmbedFailures`.
- Revision 2 (review): `Memories.GetMemory` and `Tasks.GetTask` so `Bus` compiles; the matrix nests the engine outermost and counts per engine; `EmbedFailures` added to Task 2 for `Status`; Task 11 asserts the evicted mark through the Store; both cores poll in `TestWatchReplacedAcrossCores`; display names over `ClosedAt == 0` rows with pending item 11; `TestSendConcurrentSeqOrder`; the watch timeout is clamped; `Open`, `Receive` and `Watch` are stated key exemptions; the stub split and `harness.notifier notify.Notifier`; `notify` in Task 1, Task 12 on 7, 8, 9 and 10 beside 11, Task 13 on 11 and 12; `EnsureGeneral` with `cfg.DefaultACL("")` and no subscriptions; row 2's "filter and query"; `EmbedBatch` overlap.
- Proportion: the plan fixes the seam (Task 1's types), the base API (Task 3), each `Service`'s method list and pipeline order, the amended Store signatures, the matrix table and the test names and assertions; bodies are the implementer's except the two anchor lines Task 13 mutates.
- Checked against the tree on 2026-10-21: `internal/conformance/bus/{main_test.go,bus_test.go}` exist with `TestScaffold`; `internal/store` is partially on `main` (Milestone 2 Tasks 1 and 2 at `160e883`: contract, `sqlstore` framework, SQLite engine; the aggregates are stubs), so the pre-Wave-1 signature diff is against that tree and the external gate on Milestone 2 Task 12 stands.
