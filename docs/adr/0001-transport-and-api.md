# ADR 0001: Transport and API

Status: Accepted (Eric, 2026-10-19)

Covers Decisions 1, 2, 3, 4, 9, 10, 11, 35, 42, 43, 45, 46 and 47 of
`HANDOFF.md`, all made by Eric on 2026-10-19, and Decisions 66, 67, 68
and 83 (Eric, 2026-10-20; see "Decisions of 2026-10-20" below).

## Context

agentbus v1 is a shared SQLite file: every process on a host (`agentbus mcp`
for each harness, hooks, `agentbus wait`, the TUI) opens the same database
and applies the rules itself in `internal/bus`. There is no server, so there
is no network endpoint, no authentication and no way to put the bus on
another machine or in a container. Waking an idle agent is a poll: v1
`internal/bus/wait.go` re-reads the database every `WaitPollInterval`
(one second, `internal/bus/bus.go:32`) until a message appears.

The headline feature of agentbus2 is a remote server. Eric's goal is simple,
clean code: one code path whether the bus is local or remote, and an API
that is defined once and generated from, not hand-maintained in two places.

## Decision

Eric decided:

1. **Two modes per host.** A host is configured either for a local shared
   database (today's model) or so that every agent on the host uses a
   server. The server can be a local process with a network endpoint, a
   Docker container or a real remote host. (Decision 1)
2. **The server encapsulates its data store.** The backends are SQLite and
   Postgres; ADR 0004 covers the storage contract. (Decision 2; the
   statement that clients never open the database is elaboration, not part
   of the decision.)
3. **A local stdio MCP server with an embedded API client, not a network
   MCP server.** Hooks, `wait`, the TUI and identity lookup all
   need a local binary anyway, so the MCP server stays a local
   `agentbus2` process that talks to the bus through the API client.
   (Decision 3)
4. **API-first.** The API is defined in an OpenAPI schema. Code generators
   produce the server framework and the client library; the local MCP
   server is built on that client library. (Decision 4)
5. **Local mode is the same code path.** Local mode uses the generated API
   client with an in-memory `http.RoundTripper` that calls the server
   handler (over SQLite) inside the same process. There is no daemon. When
   several processes share one local SQLite database, they poll for wakes.
   (Decision 35)
6. **Waiting is a server-side pub/sub subscription that works as a
   doorbell**, over long-poll. The wake tells the client that something
   arrived; the client then calls `receive` as before. (Decision 10)
7. **The long-poll watch endpoint** is
   `GET /v1/sessions/{session}/watch?after=<seq>&timeout=<s>`:
   - responses: `200 {seq,count,channels}` on wake, `204` at timeout,
     `409 replaced` when a newer watch starts on the same session;
   - the server caps the wait at about 50 s;
   - the pending batch (messages `receive` handed out but the agent has
     not acked) never wakes it, as in v1 ADR 0012;
   - an armed watch renews the session lease;
   - client exit codes stay as in v1: 0 woke, 1 timeout, 2 error,
     3 replaced, 4 harness gone. (Decision 42)
8. **Wake noise is merged.** `watch` merges wakes that arrive within a few
   seconds into one, and `include_own` defaults to false. (Decision 45)
9. **WebSocket later, not now.** A WebSocket endpoint behind a capability
   flag, with a separate AsyncAPI definition, is deferred. It is meant for
   Claude Code Monitor's `ws` source. (Decision 11)
10. **Discovery: both a well-known path and a versioned endpoint.**
    `/.well-known/agentbus2` points to the versioned, unauthenticated
    `GET /v1/discover`, which returns API versions, server version,
    capabilities (computed from what is actually available, not
    configured) and auth methods. Breaking changes get a new path version;
    additions get a capability. This is the version and capability
    exchange Decision 9 asked for. (Decisions 9, 43)
    Every JSON request body, and each object inside one, is closed
    (`additionalProperties: false`), so a new optional request member is
    gated by a capability a client checks first. (agent decision
    2026-10-25 under issue #16, pending Eric's review)
11. **Errors are RFC 9457 `application/problem+json`**, with `code` and
    `retryable` as extension members. Every mutating call takes an
    idempotency key; `send` and `edit_memory` gain one. (Decision 46)
12. **Generated types stay behind the `Bus` interfaces.** Consumers use
    domain types, the remote implementation wraps the generated client, and
    MCP tool schemas are hand-written. (Decision 47)

The OpenAPI code generator is not chosen here. HANDOFF names oapi-codegen
and ogen as the candidates and calls for a spike (strict server on
net/http, RFC 9457 errors, bearer and OIDC security schemes, long-poll).
The choice is ADR 0007: oapi-codegen v2.8.0 (Eric, 2026-10-20).

## Consequences

Positive:

- One code path. Local mode is the server handler called in-process; a
  bug fixed in the core is fixed for both modes, and the conformance suites
  of ADR 0004 cover both.
- The wire protocol is plain HTTP with long-poll, so any client that can
  issue a GET can wait for a wake; no WebSocket library is required for
  2.0.0.
- The `code` and `retryable` members carry v1's `bus.Error`
  (`internal/bus/errors.go`: `code`, `message`, `retryable`) into a
  standard envelope, so agents keep the retry hint they rely on today.
- Idempotency keys make retries after a lost response safe, which the
  remote server needs and the in-process bus never did.
- Capabilities computed from what is actually available (for example
  pgvector, ADR 0004) let a client adapt without a configuration contract.

Negative:

- Local mode with several processes on one SQLite file still polls for
  wakes (Decisions 35 and 52), so the doorbell is only server-side in
  server mode; that mode still polls, as v1 does.
- Long-poll holds a connection per armed watch for up to about 50 s; the
  client must re-arm after every response, and a watch replaced by a newer
  one (409) must not retry (as v1: a replaced waiter exits 3).
- A generated server and client add a generator dependency and a build
  step; the hand-written MCP tool schemas must be kept in step with the
  OpenAPI schema by hand.
- Breaking API changes cost a new path version (`/v2/...`), which the
  client has to discover and select.

## Alternatives considered

- **Redis as a backend** was considered and dropped; the backends are
  SQLite and Postgres (Decision 2).
- **A network MCP server** (agents connecting to a remote MCP endpoint
  directly) was rejected: hooks, `wait`, the TUI and identity lookup all
  need a local binary anyway, so a local stdio MCP server with an embedded
  client costs nothing extra and keeps one client code path (Decision 3).
- **A custom error schema** was the recommendation; Eric chose RFC 9457
  with extension members instead (Decision 46).
- **A single discovery endpoint** was the recommendation; Eric chose both
  the well-known path and the versioned endpoint (Decision 43).
- **MCP Events** (OpenAI; a draft spec, webhook-only, ChatGPT-only clients)
  was evaluated and rejected as a replacement for the v1 wake. Revisit if a
  supported harness ships stream-mode event subscription.
- **WebSocket now** was not rejected but deferred behind a capability flag
  and an AsyncAPI definition (Decision 11).

## Decisions of 2026-10-20 (Eric)

From Eric's answers to the design spec's section 12
(`.local/review-answers-2026-10-20.md`):

- **Decision 66.** The idempotency key is an `Idempotency-Key` request
  header on every mutating operation of the HTTP API; the MCP tools keep
  v1's `idempotency_key` body member, and the client maps the member to
  the header. (Question 3)
- **Decision 67.** Keys are scoped per identity and retained for a server
  setting ported from v1 `receipt_retention_minutes`. (Question 4)
- **Decision 68.** In local multi-process SQLite mode all cross-process
  state lives in the shared database: session rows with leases, a watch
  registration per session so the newer watch marks the older one
  `replaced`, a maintenance lease row (v1 `takeLease`), and migrations in
  an immediate transaction that re-reads `schema_version`. No pidfiles, no
  file locks. (Question 5; also ADR 0004)
- **Decision 83.** `receive` is non-blocking on the API; `watch` is the
  only server-side wait. The MCP `receive` tool may keep `wait_seconds`
  by calling `watch` then `receive` in the client. (Question 20)

Consequence: the "Negative" note above that local multi-process mode
still polls stands; what Decision 68 adds is that the `replaced` answer
comes from a row every process can see, so the process that armed the
older watch finds out on its next poll.

## Open questions for Eric

Both resolved on 2026-10-20:

1. Where cross-process session and watch state lives in local
   multi-process mode, and which process answers `replaced`: the shared
   SQLite database, and the older watch's own process when it sees the
   newer registration (Decision 68).
2. Idempotency key scope and retention: per identity, for the ported
   `receipt_retention_minutes` setting (Decision 67).

## Milestone 4 addendum (2026-10-21)

Choices the plan made where the spec delegated them (Milestone 4 plan,
"Choices this milestone makes"). Each is a choice of the plan, not a new
design decision. The items that were pending Eric proceeded on the
recommendation (A) with a `pending Eric: Milestone 4 item N` marker in the
source; Eric decided every one on 2026-10-21
(`docs/superpowers/specs/2026-10-21-gate-0-decisions.md`), and the markers
are removed as each decision lands (`rg -n 'pending Eric: Milestone 4'
internal/` then prints nothing).

- **Choice 1, the operation table.** `api/openapi.yaml` declared 33
  operations when this addendum was written. After the federated-identity
  work and the Slack bridge work (2026-10-23) it declares 52, counted from
  `operationId` and `security: []`: 44 protected, 7 open and 1 with optional
  security (`health`, issue #11; the cmd test counts it as protected because a
  bad bearer on it is a 401) (the Slack bridge
  adds the scope, scoped-identity and bridge operations, all admin). There is one `operationId` per
  `Bus` method plus the authentication paths (delete and rename channel
  carry both the `channels` and `admin` tags and serve `Messaging` and
  `Admin`). Three (`wellKnown`, `openidConfiguration`, `discover`) declared
  `security: []` then; with `token`, `jwks`, `register` and
  `authorizationServerMetadata` (added by the federated-identity Task 7 work)
  seven do now, and the rest take the global requirement. Admin reset and
  reset-credential arrive with the Milestone 5 admin section; usage is
  Milestone 3 item 3 (Milestone 7, SQLite only: Decision (Eric,
  2026-10-21)); `peek` (read-only pending counts with the watch floor) is
  Milestone 6 item 2 (Decision (Eric, 2026-10-21), option B). These are
  deliberate deferrals. Since 2026-10-21 `unsubscribeTags` is
  `DELETE /v1/tag-subscriptions?pattern=<p>&pattern=<p>` (a required
  `query` parameter, `style: form`, `explode: true`, `minItems: 1`); the
  former path `/v1/tag-subscriptions/{patterns}` is gone (item 6 below).
- **Choice 2.** `Idempotency-Key` is an optional header on every mutating
  operation; absent or empty means no receipt, as `core.WithReceipt` treats
  an empty key. The remote `Bus` sends the header only for a non-empty key.
- **Choice 3.** `Problem.code` is the eleven codes of the `Bus` plus
  `unauthenticated` (`CodeUnauthenticated`, 401). `server.StatusFor` fixes
  the status per code; `Problem.type` is `urn:agentbus2:problem:<code>`.
- **Choice 4.** Handlers return `nil, err` for every failure and one hook
  (`server.ProblemFor`) writes the problem.
- **Choice 9.** The watch `timeout` is a JSON number of seconds, fractional
  allowed (item 4, Decision (Eric, 2026-10-21): as built; test `TestWatchTimeoutWire`); `after` defaults to
  0 and `include_own` to false.
- **Choice 10.** `receive` is a `POST` with a body and no `Idempotency-Key`.
- **Choice 11.** `limitBody` is the outermost middleware (order: `limitBody`,
  authentication, validator, mux). It reads the body ahead with its own
  limit and answers 413 `validation`; it never uses `http.MaxBytesReader`.
- **Choice 13.** The remote `Bus` maps a problem+json body to a `*bus.Error`
  by struct literal (an unknown code is kept as received), a non-problem 5xx
  to `unavailable`, other non-problem statuses to `internal`, and a
  transport error to `ctx.Err()` or `unavailable`.
- **Choice 15.** `http.Server`: `ReadHeaderTimeout` 10 s, `ReadTimeout` 30 s,
  `WriteTimeout` 0, `IdleTimeout` 120 s, graceful shutdown for 60 s.
  `ReadTimeout` does not end a long-poll (test `TestWatchLongPollSurvivesReadTimeout`
  over a real socket).
- **Choice 17, path parameters.** The generated client percent-encodes each
  path parameter once; `ServeMux` decodes once; the binder treats the value
  as already unescaped. The one shape no route matches is the empty segment
  (router 404 `not_found`, the code the core gives an empty name). The suite
  runs odd names (`TestChannelNameRoundTripsOnTheWire`, now including `Send`
  and `History`) under both transports.
- **Item 1** (`Idempotency-Key` optional on the wire; absent means no
  receipt; test `TestSendIdempotencyWire`), **item 2** (auth paths declared
  in Milestone 5; test `TestOpenIDConfigurationNamesMilestone5Paths`, renamed
  `TestAuthorizationServerMetadata` by the federated-identity Task 7 work),
  **item 3** (interim server flags until Milestone 5's configuration; test
  `TestRunFlags`) and **item 4** (the watch `timeout` is fractional seconds;
  test `TestWatchTimeoutWire`): Decision (Eric, 2026-10-21), each as built
  or as planned (option A), recorded in
  `docs/superpowers/specs/2026-10-21-gate-0-decisions.md`. Their ADR 0002
  and 0007 parts are in those addenda.
- **Idempotency-Key exemptions** (Decision (Eric, 2026-10-21); spec 3.4):
  `Open`, `Receive` and `Watch` (Milestone 3 item 12), `register`
  (Milestone 5 item 6) and `peek` (Milestone 6 item 2) take no
  `Idempotency-Key`; every other mutating operation does.
- **Former item 5 (withdrawn in the plan; client-side path-parameter rule).**
  The probe showed no client-side rule is needed for names containing `/`
  (Decision 80 stands for them): they travel as one escaped segment and the
  core judges them. The number 5 stays withdrawn.
- **Item 6 (the empty-name guards and the comma inside a pattern).**
  The plan's empty-name contingency (Task dependencies) fired: for three
  inputs the core answers `validation` while the wire's empty path segment is
  the router's 404 `not_found`, so the remote `Bus` judges exactly those
  inputs itself and sends nothing: `Send` with an empty channel
  (`internal/bus/remote/messages.go`), `Close` with an empty session id
  (`sessions.go`) and `UnsubscribeTags` with no pattern or one empty pattern
  (`channels.go`). Each is a departure from Decision 80 limited to the input
  that encodes to the empty segment. Tests:
  `TestSendEmptyChannelIsValidationWithoutARequest`,
  `TestUnsubscribeTagsEmptyPathSegment`, `TestSessionOddIdsOnRemote`,
  `TestEmptySessionIDAgreesAcrossTransports`. A related divergence existed
  while the patterns travelled as the simple-style `{patterns}` path
  segment: a comma inside one pattern was the array separator, so the remote
  `Bus`'s `["a,b"]` reached the core as `["a", "b"]` where the local `Bus`
  answered `validation`. Decision (Eric, 2026-10-21), options A plus C
  (`docs/superpowers/specs/2026-10-21-gate-0-decisions.md`): the three
  guards stay, and `unsubscribeTags` takes its patterns as a repeated
  `pattern` query parameter, `DELETE /v1/tag-subscriptions?pattern=<p>&pattern=<p>`
  (`style: form`, `explode: true`, required, `minItems: 1`), so a comma is
  an ordinary character again and the old path is gone. With no `pattern`
  at all the validator answers 400 `validation` (the guard still spares the
  request for the remote's empty-pattern inputs). Tests:
  `TestUnsubscribeTagsCommaAgrees` (the two transports agree:
  `validation` for `["a,b"]` on both) replaced
  `TestUnsubscribeTagsCommaSplitsOnTheWire`, and
  `TestUnsubscribeTagsCommaIsOnePattern` is new. The `pending Eric:
  Milestone 4 item 6` markers are removed.
- **Choice 14 limitation.** `agentbus2 server` uses `notify.NewMemory()`
  (Decision 52); two server processes over one SQLite file are unsupported
  (each misses the other's hints; a watch still ends at its timeout). The
  multi-process local mode of Decision 68 is the client with
  `notify.NewPolling`, exercised in `internal/server/cmd`
  (`replaced_test.go`).

## Milestone 6 addendum (2026-10-21)

- **The `peek` operation (Task 12; item 2, Decision (Eric, 2026-10-21),
  option B).** `GET /v1/sessions/{session}/peek` with an optional
  `include_own` query parameter (default false) answers the pending counts,
  total and per channel (`bus.Sessions.Peek`; only channels with a count
  above zero, sorted by name), computed with the watch floor
  `max(CursorSeq, PendingEndSeq)`. It writes nothing: no lease renewal, no
  cursor change, no watch row, and it takes no `Idempotency-Key` (spec 3.4's
  exemption list). It requires a live session and answers `session_lapsed`,
  `session_closed` and `not_found` as `receive` does; another identity's
  session is `not_found`. Tests: `TestPeekUsesWatchFloor`,
  `TestPeekWritesNothing` (both engines, both transports).
- **Choice 9, `watch` and `wait` over the long-poll (item 4, Decision (Eric,
  2026-10-21)).** `watch` arms `GET .../watch` with the server's cap in a
  loop and prints one line per wake; `wait` prints one JSON line
  `{"seq","count","channels"}` and exits 0 (`count` is omitted on a wake
  whose memory channels were filtered out, decision 20). Exit codes are spec 3.5's:
  0 woke (`wait` only), 1 timeout (`wait -timeout`), 2 error, 3 replaced by
  a newer watch (the 409) or ended by SIGINT, SIGTERM or SIGHUP, 4 harness
  gone (polled every 2 s). When the context is canceled the exit follows
  `context.Cause`, because a canceled remote `Watch` returns a wrapped
  transport error and not `context.Canceled`. `-channel` and `-filter` are
  not offered; the Milestone 8 TUI filters for display. Tests:
  `TestWatchFlags`, and `TestExitCodeTable` in `internal/e2e` (a cell per
  exit code and command, run against the built binary and a real server).
- **Choice 11 and item 3, `wait_seconds` (Decision (Eric, 2026-10-21)).**
  `receive` with `wait_seconds` above 0 on an empty result runs `Watch` and
  receives again; the tool description and the skill say this replaces an
  armed `agentbus2 wait` or `watch` for that session, which then exits 3. Test:
  `TestReceiveWaitSecondsReplacesWatch`.
- **Decision 35 as shipped.** A local profile composes the core, the HTTP
  handler and the in-process transport in each process over the shared
  SQLite file; the notifier is the polling one (default 1 s), because
  in-memory hints do not cross processes. The maintenance loop runs only in
  `agentbus2 mcp`; one-shot commands never run it. Test:
  `TestLocalWatchWakesAcrossConns`, whose replaced-notifier mutation is
  block `replaced-exit-code-ignored` of `scripts/check-gates-selftest.sh`.

## Milestone 7 addendum (2026-10-21)

- **Choice 8, replicas.** A watch is a plain long-poll that any replica
  serves, so a deployment needs no session affinity. Over one Postgres, a
  `send` through one replica wakes a watch armed on another within
  `watch_merge_seconds` plus 1 s (the `NOTIFY` inside the append
  transaction reaches the other replica's `LISTEN` connection; the hint only
  prompts the re-check of the store), and a watch armed on a second replica
  answers the first `409 replaced` within 1.5 s (Decision (Eric, 2026-10-21),
  Milestone 7 item 1: `RegisterWatch` sends the control payload `0:0`).
  Encoded in `TestReplicasWake` (`wake-crosses-servers`,
  `watch-replaced-across-servers`, Postgres only); block
  `notify-append-removed` of `scripts/check-gates-selftest.sh` breaks the
  first by turning the `NOTIFY` into a plain `SELECT`. Delivery crosses
  replicas through the shared database on both engines
  (`deliver-crosses-servers`). The limitation stands for SQLite: two
  `agentbus2 server` processes over one SQLite file share no hints
  (Milestone 4 choice 14), so the two cross-server leaves exist for Postgres
  only and Milestone 4's `TestWatchWakesAcrossProcesses` and
  `TestWatchReplacedAcrossProcesses` keep the SQLite behavior pinned; local
  multi-process mode relies on the client's polling (Milestone 6). The
  per-identity rate-limit buckets are per process: N replicas each grant the
  full rate.
- **Choice 9, the exit test.** `TestReplicasWake` starts two real `cmd.Run`
  commands over one database and has six leaves on Postgres and four on
  SQLite; `TestReplicasMaintenanceExclusive` starts two `cmd.Serve` instances
  with `Config.OnTick` and has two leaves on each engine (`one-holder`,
  `failover`). `scripts/m7-exit-checks.sh` counts them (`wake_postgres=6`,
  `wake_sqlite=4`, `maint_*=2`) and checks the import rule.
