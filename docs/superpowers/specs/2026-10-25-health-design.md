# Server health design (issue #11)

Status: Agent design of 2026-10-25, written overnight for autonomous
implementation. Every choice below that the existing decisions do not
settle is marked **Agent decision (ratified by Eric 2026-10-25)** with the
alternatives considered, so Eric could overturn it; he reviewed all of them on 2026-10-25 and ratified them. No
decision made by Eric is changed; where the issue seemed to need one,
the option that leaves the existing decision intact was taken and is
flagged.

Related: `docs/superpowers/specs/2026-10-19-agentbus2-design.md` sections
3 (API; Decisions 43, 46, 65), 4.7 (Admin; Decisions 48, 73), 5.6
(`schema_meta`; Decisions 30, 34), 5.7 (maintenance; Decisions 49, 68);
ADR 0001 (transport and API), ADR 0004 (Store contract), ADR 0007
(oapi-codegen); GitHub issue #11.

## 1. Purpose and scope

Issue #11 asks for server health through the REST API, surfaced in the
TUI health overlay and in an MCP tool:

- `api/openapi.yaml` defines a health operation (liveness and readiness
  for load balancers, plus a detailed health document); `make generate`;
  handlers in `internal/server`.
- The document reports at least: version, store engine and reachability,
  schema version, maintenance-lock holder and last run, embedding backlog
  and status, storage use and capacity notices where available.
- Access rules: what an unauthenticated probe, an authenticated identity
  and an admin-flagged identity each see, consistent with Decision 73.
- The `Bus` interface exposes the query; `internal/bus/local` and
  `internal/bus/remote` implement it; the Bus conformance suite covers it
  on both engines.
- The TUI health overlay (`internal/tui/health.go`) reads from it.
- A new MCP tool (hand-written schema) returns it to agents.
- `docs/ops/` documents the endpoint.

Out of scope (section 12): a cross-replica view of the maintenance lock
holder on Postgres, a schema change, a Docker `HEALTHCHECK`, metrics.

## 2. What exists today (checked against source, 2026-10-25)

- `GET /v1/discover` (`internal/server/discovery.go`): unauthenticated
  (Decision 43), always 200, needs no store access (it reads the embedded
  document and `core.Embedder()`), returns `server_version` and
  `capabilities` (`long_poll`, `semantic_search`). `docs/ops/deploy.md`
  section 5 names it as the probe; `scripts/docker-smoke.sh` step 5 curls
  it. `deploy/docker-compose.yml` has a healthcheck for Postgres only; the
  `Dockerfile` has no `HEALTHCHECK` (the image has no shell or curl).
- Admin views (Decision 73): `GET /v1/admin/status` (`bus.Status`: live
  sessions, channels, notices, embedding backlog and rejections, the last
  embedding error) and `GET /v1/admin/usage` (`bus.Usage`: bytes, budget,
  evictable, capacity notice). Both call `core.RequireAdmin`.
- `store.Store` (`internal/store/store.go`): `Engine()`, `Locks`
  (`TryLock`, `Unlock`: the `leases` row on SQLite, a session advisory
  lock on Postgres; `TryLock` reports only `held bool`, never the other
  owner), `Space` (`Usage`, `Reclaim`). `sqlstore.Store.SchemaVersion(ctx)`
  exists (`internal/store/sqlstore/migrate.go:176`) but is not on the
  `store.Store` interface; the store suite reaches it through a type
  assertion (`internal/conformance/store/helpers_test.go:267`).
- Maintenance (`internal/core/maintenance`): `Tick` takes the
  `maintenance` lock under `core.Owner` (random hex per process), runs 13
  jobs and returns `(ran bool, report []JobResult)`; nothing persists the
  last run. The server loop (`internal/server/cmd/serve.go`) and the
  local-mode loop (`internal/client/local.go`, only when
  `client.Options.Maintenance` is set, which `agentbus2 mcp` and `tui` do)
  both call `Tick`.
- Embedding state: `channels.Service.Status` computes the backlog
  (`tx.UnembeddedMemories`, capped at `rules.SearchPageMax`), the rejection
  count and last reason (`tx.EmbedFailures`) and the `embedding` notice.
- `internal/version.Version` is the binary's version;
  `server.Options.Version` overrides it for the handler (`api.version`).
- TUI (`internal/tui/health.go`): the overlay shows the client's version,
  the mode line, identity, sessions and channels, the receive state and,
  for an administrator only, notices, embeddings and storage from
  `Status` and `Usage`; a non-admin sees `admin no (status and storage are
  administrator views)`.
- Pinned counts: 24 MCP tools (`internal/mcpserver/run_test.go:111`, the
  name list at `internal/mcpserver/server_test.go:244`, `CLAUDE.md`
  line 19); `TestCapabilitiesComputed` pins `["long_poll"]`;
  `internal/bus/doc.go` says "the five interfaces"; the e2e counts
  (`scripts/m6-exit-checks.sh`: 13 sequence steps, 11 exit-code cells),
  the Milestone 3 counts (`scripts/m3-exit-counts.sh`), `TestAuthEndToEnd`
  (11 leaves per engine), the Milestone 8 counts and the gate anchors of
  `scripts/check-gates-selftest.sh` (`if !id.Admin {` in
  `internal/core/authz.go` among them) must not move.

## 3. The operation

### 3.1 Path, security, responses

`GET /v1/health`, tag `health`, operationId `health`.

```yaml
  /v1/health:
    get:
      operationId: health
      tags: [health]
      security:
        - {}
        - bearerAuth: []
      responses:
        "200":
          description: The health document, as the caller's tier may see it.
          content:
            application/json:
              schema:
                $ref: "#/components/schemas/Health"
        default:
          $ref: "#/components/responses/Problem"
```

**Agent decision 1 (ratified by Eric 2026-10-25): one operation with optional
security and a tiered body.** `security: [{}, {bearerAuth: []}]` makes
the bearer optional: kin-openapi v0.151.0
(`openapi3filter/validate_request.go`, `ValidateSecurityRequirements` and
`validateSecurityRequirement`) returns nil for the empty requirement, so
the validator never answers 401 on this operation (health is counted as an open operation: Eric's decision 12 of ADR 0010, 2026-10-25, "Count as open"; 8 open, 44 protected); the handler decides the
tier from `IdentityFrom(ctx)`. Alternatives: (A) two operations, an
unauthenticated `/v1/healthz` and a bearer-only `/v1/health/detail`
(more surface, two Bus methods or a flag); (B) a bearer-only operation
(no load-balancer probe). Rejected: A doubles the wire surface for one
document; B fails the issue's probe requirement.

**Agent decision 2 (ratified by Eric 2026-10-25): a presented but invalid
bearer is 401, not the probe tier.** `authenticate` (`internal/server/server.go`)
records a rejected credential under `authErrorKey` and leaves the identity
empty; on every other open operation the bad bearer is ignored, so health is the one open operation that can answer 401 (ADR 0010 decision 12). The health
handler checks `authErrorFrom(ctx)` first and answers
`bus.CodeUnauthenticated` (401 problem+json, `WriteProblem`), so a client
with a stale token sees the failure (and the transport's one-shot refresh
on 401, `internal/credential`, keeps working) instead of silently getting
the probe tier. Alternative: treat it as anonymous, as discover does.
Rejected: a monitoring client would misread "fewer fields" as "healthy".

**Agent decision 3 (ratified by Eric 2026-10-25): the document is 200-only;
an unreachable store is a 503 problem, never a document.** Decision 46 and
the document header ("Every error is application/problem+json") make a
5xx an error on the wire, and `remote.errorOf` already treats it so. When
the store ping fails the core answers `bus.CodeUnavailable` (retryable)
and the handler writes the 503 problem through the existing path; the
`Bus.Health` method returns that error. Alternative: a `503` response
carrying the `Health` schema with `status: unavailable`, so the TUI could
show the document. Rejected: it would be the first non-problem error body
(a change to Decision 46); the TUI shows the problem's message instead.

### 3.2 Liveness and readiness

**Agent decision 4 (ratified by Eric 2026-10-25): liveness is the existing
`GET /v1/discover`; readiness is `GET /v1/health`.** Discover is
unauthenticated, store-free and always 200 while the process serves, so a
liveness probe on it never restarts a replica because the database is
down; `/v1/health` is 200 when the store answers (status `ok` or
`degraded`) and 503 when it does not, so a readiness probe on it takes a
replica out of rotation only while it cannot serve. `docs/ops/deploy.md`
section 5 is rewritten to say exactly this. Alternative: one endpoint for
both, with `/v1/health` returning 503 on store failure. Rejected: a
liveness probe on it would restart pods for a Postgres outage.

### 3.3 Discover capability

**Agent decision 5 (ratified by Eric 2026-10-25): `health` is a new discover
capability.** Decision 43: "additions get a capability". `Capabilities`
(`internal/server/discovery.go`) returns `health` always, sorted with the
others, so a 2.0 client can tell a server that has the operation from one
that does not. `TestCapabilitiesComputed` changes from `["long_poll"]` to
`["health", "long_poll"]` (and `["health", "long_poll", "semantic_search"]`
with an embedder). `scripts/docker-smoke.sh` keeps matching `"long_poll"`.

## 4. The document

Snake_case JSON; optional members are absent, never null (document
header). Only the probe tier's members are `required`; every other member
is optional and present from the tier shown.

| Member | Type | Tier | Source |
| --- | --- | --- | --- |
| `status` | enum `ok`, `degraded` | probe | section 5 |
| `tier` | enum `probe`, `identity`, `admin` | probe | what the caller got; lets a client tell "healthy" from "I only got the probe view" |
| `version` | string | probe | `api.version` (`server.Options.Version`, else `version.Version`); the same value as discover's `server_version` |
| `now` | int64 Unix ms | probe | the core clock (`Core.NowMs`) |
| `started_at` | int64 Unix ms | identity | `Core.StartedAt()` (new; the clock at `core.New`) |
| `store.engine` | enum `sqlite`, `postgres` | identity | `Store.Engine()` |
| `store.schema_version` | integer | identity | `Store.SchemaVersion(ctx)` (new on the contract, section 6); this read is the reachability ping |
| `embeddings.state` | enum `off`, `ok`, `failing` | identity | `off` when `Core.Embedder() == nil`; `failing` when the `embedding` notice is set; else `ok` |
| `embeddings.backlog` | integer | identity | `tx.UnembeddedMemories(model, rules.SearchPageMax)` count (0 when `off`) |
| `embeddings.rejected` | integer | identity | `tx.EmbedFailures` count (0 when `off`) |
| `embeddings.model` | string | admin | `Cfg.EmbeddingModel` (absent when `off`) |
| `embeddings.last_error`, `embeddings.last_error_at` | string, int64 | admin | the `embedding` notice's message and `set_at` (absent when unset) |
| `embeddings.rejected_last` | string | admin | the latest rejection reason (absent when empty) |
| `storage` | `Usage` (existing schema) | admin | `channels.Service.Usage` |
| `notices` | array of `Notice` (existing schema) | admin | the `capacity` and `embedding` notices, as `Status` reports them; `[]` when none |
| `maintenance.interval_seconds` | integer | admin | `Cfg.MaintenanceIntervalSeconds` |
| `maintenance.owner` | string | admin | `Core.Owner`: this process's lease owner |
| `maintenance.lock` | enum `held`, `held_elsewhere`, `unknown` | admin | the last tick's `TryLock` result in this process; `unknown` before the first tick or after a `TryLock` error |
| `maintenance.last_tick_at` | int64 Unix ms | admin | when this process last ticked (absent before the first tick) |
| `maintenance.lock_error` | string | admin | the last tick's `TryLock` error text (absent when none) |
| `maintenance.jobs` | array of `{name, changed, error?, skipped}` | admin | the last tick's report, in job order; `[]` before the first tick or when the lock was held elsewhere |

Reachability is the response itself: 200 means the ping answered, 503
means it did not (section 3.1, decision 3). There is no `reachable`
member.

**Agent decision 6 (ratified by Eric 2026-10-25): the maintenance block is
this process's view, from an in-memory record, with no schema change.**
`store.Locks.TryLock` reports only whether this owner holds the lock; on
Postgres the advisory lock has no owner string anyone can read
(`pg_locks` shows the lock, not who holds it), and on SQLite the `leases`
row names the owner but not when it last ran. The issue asks for "holder
and last run"; the options were: (A) schema version 7 with a
`maintenance_runs` row written by every tick (a migration, two engines,
the `make verify covers every schema version up to the current 6`
sentence and its tests); (B) record the last run in the `notices` table
under a reserved kind (a key-value misuse of an operator-notice table);
(C) an in-memory record on `core.Core`, written by `maintenance.Tick`,
reporting `lock: held | held_elsewhere | unknown`, the owner of this
process, the last tick time and the job report. C was taken: it changes
no schema and no contract, is exact for a single server over SQLite, and
on Postgres a probe of each replica reveals which one holds the lock
(exactly one answers `held`). The document says so (`maintenance.owner`
is this process's). A later issue may add A if cross-replica history is
wanted; A is the alternative Eric is most likely to prefer, so the record
type is designed to be filled from a Store row later without changing the
wire shape.

In local mode only `agentbus2 mcp` and `agentbus2 tui` run the
maintenance loop (`client.Options.Maintenance`), so a `client.Open` from
`agentbus2 health` or a hook reports `lock: unknown` and no jobs: correct,
that process never ticks. The TUI sees its own ticks.

## 5. The status rule

- 503 problem (`unavailable`, retryable): `Store.SchemaVersion` or the
  `Store.Read` that reads the rest fails. Any store error is mapped to
  `bus.CodeUnavailable` with the message `store unreachable: <bounded
  text>` (`bus.TruncateErrorMessage`); a `*bus.Error` the callback itself
  produced (`not_registered`, below) passes through unchanged.
- `degraded`: the `capacity` or the `embedding` notice is set, or this
  process's last maintenance tick recorded a `TryLock` error or a job
  error. (The notices are read for every tier so the probe's `status` is
  the same value an administrator sees; the reasons are tiered.)
- `ok`: otherwise.

**Agent decision 7 (ratified by Eric 2026-10-25): the probe tier sees the true
`status`, `degraded` included.** It reveals that something is off, not
what. Alternative: collapse `degraded` to `ok` for anonymous callers.
Rejected: a load balancer dashboard that can show amber is the point of
the probe.

## 6. Access rules (Decision 73)

| Caller | How recognized | Sees |
| --- | --- | --- |
| Unauthenticated probe | no `Authorization` header (`IdentityFrom(ctx) == ""`, no auth error) | the probe tier: `status`, `tier: probe`, `version`, `now` |
| Rejected credential | `authErrorFrom(ctx) != nil` | 401 problem (`unauthenticated`), decision 2 |
| Authenticated identity | a verified bearer naming a registered identity whose `Admin` is false | the identity tier (adds `started_at`, `store`, `embeddings` without the admin members) |
| Administrator | the identity's `Admin` flag (the evaluated flag `core.RequireIdentity` returns; the `admins` list of schema version 5) | everything |
| Authenticated but unregistered | a verified bearer whose identity row is gone | `not_registered` (403), as every other operation answers |

Decision 73 is kept: nothing an administrator alone could read today
(`Status`, `Usage`: notices, error texts, storage, the lock owner) is
shown below the admin tier. The identity tier adds only what an agent
already learns indirectly (the engine, that embeddings exist and whether
semantic search is degraded; `search` already returns
`semantic_unavailable`). The core checks the flag by reading the identity
row (`RequireIdentity`, then `id.Admin`) and never calls `RequireAdmin`,
whose anchor line `if !id.Admin {` the gate selftest pins.

**Agent decision 8 (ratified by Eric 2026-10-25): the identity tier includes
`store.engine`, `store.schema_version`, `started_at` and the embedding
state and counts.** Alternative: admin-only for all of them (the probe
tier plus nothing). Rejected: an agent diagnosing "search returns text
only" needs the embedding state, and the schema version and engine are
not secrets from an identity that holds a key to the server.

Local mode (`internal/client`) binds an identity to every call, so the
probe tier never occurs there; the MCP tool and the TUI get the identity
or admin tier.

## 7. Store contract: `SchemaVersion`

`store.Store` gains `SchemaVersion(ctx context.Context) (int, error)`:
`schema_meta.schema_version`, read outside any transaction. `sqlstore.Store`
already implements it; this lifts it onto the interface so the core can
call it (`internal/core/...` never imports `internal/store/sqlstore`).
The store suite asserts it equals `len(migrationsFor(e))` on both engines
after `Open`, and the `helpers_test.go:267` type assertion goes away.
Recorded as a "Health addendum (2026-10-25)" in ADR 0004 (the repo's
pattern), marked as an agent decision pending review.

State (Definition of Done D): a read; a second call, two concurrent
calls and a call after a failed migration (which leaves the previous
version, spec 5.6) all return whatever `schema_meta` holds.

## 8. Core

### 8.1 Maintenance tick record and start time (`internal/core`)

```go
// MaintenanceJob is one job's outcome in a tick.
type MaintenanceJob struct {
	Name    string
	Changed int
	Err     string // "" when none
	Skipped bool
}

// MaintenanceTick is what this process's last maintenance tick found.
type MaintenanceTick struct {
	At      int64  // Core.NowMs at the tick
	Held    bool   // TryLock granted the lock to Core.Owner
	LockErr string // TryLock's error text; "" when none
	Jobs    []MaintenanceJob // empty unless Held
}

func (c *Core) RecordMaintenanceTick(t MaintenanceTick)
func (c *Core) LastMaintenanceTick() (MaintenanceTick, bool) // false before the first tick
func (c *Core) StartedAt() time.Time                          // the clock at New
```

Guarded by a mutex (or `atomic.Pointer`); `Jobs` is copied on record and
on read. `maintenance.Service.Tick` records on every path: `TryLock`
error (`LockErr`), not held (`Held: false`), ran (`Held: true`, `Jobs`
from the report, `Err` = `err.Error()`). `RunJob` does not record (it is
the operator command and the tests, not a tick). Second run, overlap,
leftovers: the record is in-memory and overwritten by every tick; two
loops in one process (tests) last-writer-wins, which is the latest tick.

### 8.2 The health aggregate (`internal/core/health`)

```go
type Service struct{ *core.Core }

// Health builds the document for identity ("" is the probe tier).
func (s Service) Health(ctx context.Context, identity, version string) (bus.Health, error)
```

Steps: `SchemaVersion` (the ping; a failure is `unavailable`); one
`Store.Read` that, for `identity != ""`, runs `RequireIdentity` (its
`not_registered` passes through) and reads `id.Admin`, reads the two
notices, and for the identity and admin tiers the embedding report; then
for the admin tier `channels.Service.Usage` (its own `Read`) and
`LastMaintenanceTick`. The embedding block is factored out of
`channels.Service.Status` into an exported helper
(`channels.EmbeddingReport(ctx, tx, c *core.Core) (bus.HealthEmbeddings, error)`
or equivalent) that both call, so the two views cannot drift. `version`
is passed in by the caller (the handler's `api.version`; the local Bus
passes `version.Version`), so the core never imports `internal/version`
for it (it may; this keeps the signature explicit).

Import rule: `internal/core/health` imports `internal/bus`, `internal/core`,
`internal/core/channels`, `internal/core/rules` and `internal/store` only
(no `internal/gen`, no `internal/server`: `scripts/m4-exit-checks.sh`).

## 9. Bus

```go
// bus/health.go
type HealthStatus string          // HealthOK = "ok", HealthDegraded = "degraded"
type HealthTier string            // TierProbe, TierIdentity, TierAdmin
type EmbeddingState string        // EmbeddingsOff, EmbeddingsOK, EmbeddingsFailing
type MaintenanceLock string       // LockHeld, LockHeldElsewhere, LockUnknown

type Health struct {
	Status      HealthStatus
	Tier        HealthTier
	Version     string
	Now         int64
	StartedAt   int64              // identity tier and up; 0 below
	Store       *HealthStore       // nil below the identity tier
	Embeddings  *HealthEmbeddings  // nil below the identity tier
	Storage     *Usage             // nil below the admin tier
	Notices     []Notice           // nil below the admin tier; non-nil (possibly empty) at it
	Maintenance *HealthMaintenance // nil below the admin tier
}
type HealthStore struct { Engine string; SchemaVersion int }
type HealthEmbeddings struct {
	State EmbeddingState; Backlog, Rejected int
	Model, LastError string; LastErrorAt int64; RejectedLast string // admin tier
}
type HealthMaintenance struct {
	IntervalSeconds int; Owner string; Lock MaintenanceLock
	LastTickAt int64; LockError string; Jobs []HealthJob
}
type HealthJob struct { Name string; Changed int; Error string; Skipped bool }

// Monitoring is the health seam: every caller may ask, the answer is tiered.
type Monitoring interface {
	Health(ctx context.Context) (Health, error)
}
```

**Agent decision 9 (ratified by Eric 2026-10-25): a sixth consumer interface,
`Monitoring`, embedded in `Bus`, rather than a method on `Admin`.**
Decision 7 splits `Bus` by consumer; `Admin` is "the administrator seam"
(Decision 48) and health is for every caller. `internal/bus/doc.go` and
the `Bus` comment ("the five consumer interfaces") change to six.
Alternative: add `Health` to `Admin`. Rejected: it would say the wrong
thing about who may call it.

`internal/bus/local`: `health.Service{Core: b.c}.Health(ctx, b.identity, version.Version)`.
`internal/bus/remote`: `HealthWithResponse`; 200 converts (every enum
member checked: an unknown `status`, `tier`, `embeddings.state` or
`maintenance.lock` is `internal` naming the member, like
`fromGenMessage`'s `sender_kind`; Definition of Done B); anything else
goes through `errorOf` (a 503 problem becomes `unavailable`, a 401
`unauthenticated`).

## 10. Server

`internal/server/health.go`: the strict handler `Health`. Order: the
auth error check (decision 2), then
`health.Service{Core: a.core}.Health(ctx, IdentityFrom(ctx), a.version)`;
the error path is the usual `return nil, err` (the strict handler's
`ResponseErrorHandlerFunc` writes the problem; `unavailable` is 503,
`not_registered` 403, `unauthenticated` 401). Conversion to `gen.Health`
is in `convert.go` beside the others; pointers for optional members,
`Notices` as `[]gen.Notice{}` (never null) at the admin tier.

The `nethttp-middleware` validator still validates the request (no
parameters, no body); the empty security requirement passes first, so the
`AuthenticationFunc` is never the one that refuses here.

## 11. MCP tool

`health` (tool 25), input `optSessionIn` like `list_channels`, output a
`healthOut` struct in snake_case with arrays never null and optional
members omitted (`omitempty` pointers), the same member names as the
wire. Description (word check of `scripts/m6-exit-checks.sh` applies:
none of `dm/`, `-filter`, `@<`, `memory/`, `tasks/`, `general/`):
"Agentbus2: the server's health as your identity may see it: status (ok
or degraded), version, store engine and schema version, embedding state
and backlog; an administrator also sees storage, notices and the
maintenance lock. Call it when something that should work does not,
before reporting a bus problem; a failure means the server or its store is
unreachable."

Decision 18 (CLAUDE.md, `park`/`unpark`; `internal/mcpserver/park.go`
`unparkOnToolCall`): every tool but `receive` unparks through the
middleware; `health` unparks too, no exception (carving one would change
that rule). `using-agentbus2/SKILL.md` gains
one line under its "something should work but does not" guidance.

Pins that move: `internal/mcpserver/run_test.go:111` (24 to 25),
`internal/mcpserver/server_test.go:244` (the name list gains `health`),
`CLAUDE.md` line 19 (24 to 25). `TestToolsReportUnresolvedIdentity` and
the park table (`park_test.go`) gain `health` so the tool is covered by
both.

## 12. TUI

`openHealth` batches `statusCmd`, `usageCmd` and a new `healthCmd`
(`Bus.Health`); the result arrives as `healthMsg{h *bus.Health; err error}`
added to `msgs.go` (the file comment: feature files never add message
types). The overlay gains, right after the `receive` line:

- `server <ok|degraded> · agentbus2 v<server version> · <engine> schema <n> · embeddings <off|ok|failing>`
  (identity tier; the version is the server's, which may differ from the
  client's `version` line above it), or
- `server unavailable · <error text>` when `Health` failed (a 503, a
  network error), or `server …` before the answer; and
- for the admin tier, after the storage line:
  `maintenance <held|held elsewhere|unknown> · last tick <HH:MM:SS|none yet> · <n> jobs, <m> failed`
  plus one indented `warn` line per failed job (`<name>: <error>`), and
  `lock error: <text>` when set.

The existing admin block (notices, embeddings detail, storage) and the
non-admin `admin no (…)` line stay; `docs/tui.md` "Health" says the server
line is from the health endpoint and lists the admin maintenance line.
`TestHOpensHealthWithLines` gains the server line and keeps its non-admin
exclusions, with `embeddings` replaced by the admin block's exact form
`embeddings ok · backlog` (the identity tier now shows the state word).
The import allowlist (`scripts/m8-exit-checks.sh`: `internal/bus`,
`internal/version`) is unchanged.

## 13. CLI (optional)

**Agent decision 10 (ratified by Eric 2026-10-25): `agentbus2 health [-profile P] [-identity I]`
is an optional last task.** It prints the document as one JSON object on
stdout (the client-command convention) and exits 0 for `ok` or
`degraded`, 1 for a `bus.Error` (an unreachable store among them, as the
convention says), 2 for a flag or resolution error. It is not in the
issue's acceptance criteria and grows `Usage`, `CLAUDE.md` and `README.md`,
so the plan puts it last and Eric may drop it. Alternative: no CLI
command; operators curl the endpoint (anonymous) or run `agentbus2 status`
(admin lines). Also considered and rejected for now: a Dockerfile
`HEALTHCHECK` with an `agentbus2 server probe URL` subcommand (the image
has no shell; nothing in the compose file depends on the replicas being
healthy; Kubernetes ignores `HEALTHCHECK`).

## 14. Documentation

- New `docs/ops/health.md`: the two probes, the document with its tiers
  (the table of section 4), the status rule, the 503, `curl` examples with
  and without a bearer, how to read `maintenance` on replicas, and the
  TUI and MCP surfaces. Linked from `docs/ops/README.md`.
- `docs/ops/deploy.md` section 5: liveness `GET /v1/discover`, readiness
  `GET /v1/health`, pointer to `health.md`.
- `docs/ops/admin-bootstrap.md` "The admin API": one sentence that the
  admin tier of `GET /v1/health` is flag-gated like the section.
- `docs/tui.md` "Health" (section 12 above).
- `internal/cli/skills/using-agentbus2/SKILL.md`: the one line of
  section 11.
- `CLAUDE.md`: tool count; the `health` client command if Task 7 lands;
  the core aggregate list gains `health`; the Store bullet mentions
  `SchemaVersion`; the Design section gains one line on the health
  operation and its tiers.
- `README.md`: the command list, if Task 7 lands.
- ADR 0004: "Health addendum (2026-10-25)" for `SchemaVersion` and the
  tick record. New ADR 0010 "Server health", Status "Accepted (agent proposal,
  ratified by Eric 2026-10-25)", transcribing agent decisions 1 to
  10 with their alternatives; `docs/adr/README.md` row.
- `scripts/docker-smoke.sh` step 5b: `curl -fsS .../v1/health` contains
  `"tier":"probe"` and `"status":"ok"` (the `docker-smoke` CI job, not
  `verify`).

## 15. Import rules, counts and gates (unchanged, verified by the plan)

- Decision 47 (`m4`): `internal/core/health` and `internal/bus` import no
  `internal/gen` or `internal/server`; `internal/bus/remote` imports no
  `internal/server`, `internal/core` or kin-openapi.
- `m5`: untouched (`internal/auth` is not involved).
- `m6`: `internal/mcpserver` and `internal/cli` reach health through
  `bus.Bus` only; the word check over `server.go` and `inputs.go`; the
  e2e counts (13, 11) are untouched because no step is added to
  `TestSection24Sequence` or `TestExitCodeTable`.
- `m7`: `internal/store/...` still imports no `internal/core`;
  `SchemaVersion` lives in `sqlstore`.
- `m8`: `internal/tui` imports only `internal/bus` and `internal/version`.
- `m3-exit-counts.sh`: new tests have new names; the ACL matrix (52 per
  engine) and the other counted tests are not edited.
- `check-gates-selftest.sh` anchors are not touched: the health core
  reads `id.Admin` itself and never edits `RequireAdmin`.
- No new server setting, so `go test ./internal/server/config/` (which
  checks `configuration.md`) is unaffected.

## 16. Testing map

| Claim | Test |
| --- | --- |
| `SchemaVersion` on both engines equals the migration count | store suite `TestSchemaVersionMatchesMigrations` |
| `Tick` records held, not held and lock-error ticks; `RunJob` does not | `internal/core/maintenance/tick_test.go` `TestTickRecordsLastTick` |
| Tiers, status rule, `not_registered`, unavailable mapping | `internal/core/health/health_test.go` (SQLite, unit) |
| Probe tier, bad bearer 401, stale-identity 403, 503 on a closed store, `health` capability | `internal/server/health_test.go` (`e.client("")`, a `Bearer` env, a closed store) |
| Identity and admin tiers through both engines and both transports; degraded on a notice; the maintenance block after a real `Tick`; the remote enum guard | `internal/conformance/bus/health_test.go`, `internal/bus/remote/health_test.go` |
| Tool 25 listed, callable, unparks, reports an unresolved identity | `internal/mcpserver` tests (the pinned lists) |
| The overlay's server line, admin maintenance line, error line | `internal/tui/health_test.go` |
| Docs name the endpoint and the probes | `docs/ops/health.md`, `deploy.md` (reviewed; no code check) |

The probe tier cannot be exercised by the Bus suite (`InProcessTransport`
always sets an identity), so it lives in the server tests.
