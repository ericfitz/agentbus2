# Milestone 4: OpenAPI, Generated Server and Client, Local Mode Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The real OpenAPI 3.1 document in `api/` with both security schemes (Decision 65), regenerated into `internal/gen` by the pinned oapi-codegen v2.8.0 (Decision 64), hand-written `net/http` handlers over the Milestone 3 core with `application/problem+json` for every error the framework or the core produces (Decision 46, spec 3.3), the `Idempotency-Key` header mapped onto the core's receipts (Decision 66), discovery at `/.well-known/agentbus2`, `GET /v1/discover` and `/.well-known/openid-configuration` (Decisions 43, 65, 91), non-blocking `receive` and the long-poll `GET /v1/sessions/{session}/watch` with 200, 204 and 409 `replaced` (Decisions 42, 45, 68, 83), the in-process `http.RoundTripper` (Decision 35), a remote `Bus` over the generated client that keeps generated types behind the `Bus` interfaces (Decision 47), and an `agentbus2 server` command that serves over SQLite or Postgres and refuses every authenticated operation until Milestone 5. Exit: generation and the kin-openapi request validator pass on the real 3.1 document; the Milestone 3 core/`Bus` conformance suite passes through the generated client over the in-memory transport on both engines, beside its local run; a stale `internal/gen` fails CI; a newer watch replaces an older one with 409, also across two processes sharing one SQLite file (v1 issue ericfitz/agentbus#39).

**Architecture:** `api/openapi.yaml` is the one source of the wire protocol; `make generate` writes `internal/gen/gen.go` (`std-http-server`, `strict-server`, `client`, `models`, now with `embedded-spec` so the kin-openapi validator and `GET /v1/discover` read the same document). `internal/server` builds one `http.Handler` from a `*core.Core`: an `Authenticator` seam that only attaches an identity to the request context (`RefuseAll` until Milestone 5; `InProcess` for the in-memory transport of Decision 35), the `oapi-codegen/nethttp-middleware` validator that enforces the document's security requirements and parameter and body schemas, the generated router with the three strict-server hooks, and one `WriteProblem` that every hook and every handler error goes through. Handlers are thin: identity from the context, generated request to `bus` input, one Milestone 3 `Service` call, `bus` result to generated response; every failure is returned as the error and becomes a problem with the status of its `bus.Code`. `internal/bus/remote` implements the five `Bus` interfaces over `gen.ClientWithResponses`, converting generated types to domain types and problems to `*bus.Error`. `internal/server/cmd` is the `agentbus2 server` command: store, `EnsureGeneral`, the maintenance loop, an `http.Server` whose timeouts allow a 50 s long-poll. The conformance suite's `newBus` factory is switched by `AGENTBUS2_BUS_TRANSPORT` between `local.New` and the remote `Bus` over the in-process transport, and `make test-conformance` runs the `bus` package once per transport.

**Tech Stack:** Go 1.27, `CGO_ENABLED=0`; oapi-codegen v2.8.0 (`tool` directive, already pinned), `github.com/oapi-codegen/runtime` v1.7.0 (present), `github.com/oapi-codegen/nethttp-middleware` v1.2.0 and `github.com/getkin/kin-openapi` v0.142.0 (become direct requirements; the versions `go mod tidy` already resolved for the spike); `net/http` with Go 1.22 `ServeMux` patterns; `log/slog`; the Milestone 2 Store and the Milestone 3 core and `Bus`. No other new module.

**Spec:** `docs/superpowers/specs/2026-10-19-agentbus2-design.md` sections 2.1 to 2.4, 3.1 to 3.5, 4.3 to 4.6, 5.5, 7, 8, 9 and 10 (Milestone 4); ADR 0001 (Decisions 1, 2, 3, 4, 9, 10, 35, 42, 43, 45, 46, 47, 66, 67, 68, 83), ADR 0002 (Decisions 36, 84, 85), ADR 0003 (Decisions 8, 14, 15, 16, 40, 44), ADR 0004 (Decisions 5, 6, 7, 49, 52, 54), ADR 0005 (Decisions 31, 32, 33), ADR 0007 (Decisions 64, 65, 91), ADR 0008 (Decisions 80, 89, 94); the codegen spike `docs/superpowers/specs/2026-10-19-codegen-spike.md` with `2026-10-19-codegen-spike/openapi.yaml`, `oapi/cfg.yaml` and `oapi/spike_test.go`; the Milestone 3 plan `docs/superpowers/plans/2026-10-21-milestone-3-core-bus.md` revision 3 (`99095cd`) for every core and `Bus` name below; the Milestone 1 plan for the gate scripts.

**Evidence gathered while planning (2026-10-21):** a copy of the spike's spec with only `openapi: 3.1.0` changed, no `servers:` block and `openIdConnectUrl: /.well-known/openid-configuration` generated with oapi-codegen v2.8.0, loaded through the generated `GetSwagger()`, passed `openapi3.T.Validate`, and passed the spike's `TestSecurityViaValidatorMiddleware` and `TestIdempotencyKeyHeader` (the `nethttp-middleware` v1.2.0 validator over kin-openapi v0.142.0: 401 before the handler on a protected operation, `security: []` honored, parameter binding). That is still a sample with no 3.1-only idiom; Task 1 repeats the generation and validator tests on the real document, which is the exit criterion's re-test.

## Task dependencies and parallelism

Tasks with no dependency between them run in parallel in separate worktrees and are cherry-picked onto `main`; every task's `Files:` list is disjoint from every task it can run beside. The two generated-code owners are Task 1 (`api/`, `internal/gen`, `go.mod`, `go.sum`, `scripts/check-gates.sh`) and nobody else: a handler task that needs a schema change reports it to the integrator, who makes it in a serial commit and regenerates (Milestone 3's rule for the Store). `gen.StrictServerInterface` is one interface and `ab2bus.Bus` is one interface, so Task 2 and Task 3 create one `api` type and one `remote.Bus` type with a stub file per aggregate (each stub holds exactly the methods its aggregate task replaces, with the file comment "replaced by Task N"), and Tasks 4 to 7 each replace their own two stubs and own their own test files.

**External gate:** every task except Task 1 imports `internal/core/...` and `internal/bus/local`, so Tasks 2 to 10 start only once Milestone 3's Task 13 commit is on `main` (`make verify` green with the full core suite, `ErrNotImplemented` gone). Task 1 touches neither; it edits `internal/bus/errors.go`, so it needs Milestone 3 Task 1 (`feat(bus)`) on `main` and nothing later, and may run while the rest of Milestone 3 is still landing. Before Wave 2 the integrator diffs every name in "Core and Bus names assumed" against `main` and reports drift.

| Task | Depends on | Wave |
| --- | --- | --- |
| 1 OpenAPI 3.1 document, generation, the 3.1 and validator re-test | none (needs only Milestone 3 Task 1's `internal/bus/errors.go` on `main`) | 1 |
| 2 Server framework: problems, auth seam, validator, hooks, discovery, stubs | 1 | 2 |
| 3 Remote Bus skeleton, in-process transport wiring, suite transport switch | 2 | 3 |
| 4 Sessions, receive, watch, identities (handlers and remote) | 3 | 4 |
| 5 Channels, ACL, subscriptions, admin status (handlers and remote) | 3 | 4 |
| 6 Send, history, search (handlers and remote) | 3 | 4 |
| 7 Memories and tasks (handlers and remote) | 3 | 4 |
| 8 `agentbus2 server` command | 2 | 3 |
| 9 Watch replaced across two processes (issue #39) | 4, 8 | 5 |
| 10 Integration, milestone exit, docs | 5, 6, 7, 9 | 6 |

Waves: 1; 2; 3 (Tasks 3 and 8 in parallel: 8 needs only Task 2's `server.New` and the stubs compile); 4 (Tasks 4 to 7 in parallel); 5; 6. Ten tasks: the document (1), the two skeletons (2, 3), four aggregate pairs (4 to 7), the command (8), the cross-process proof (9) and the exit (10).

## Global Constraints

- Module `github.com/ericfitz/agentbus2`, `go 1.27`, `CGO_ENABLED=0`; every method takes a `context.Context` (Decision 6). Work lands on `main`, nothing is pushed in this milestone, Conventional Commits with the session's attribution trailer; each task commits only its own files.
- Decision 47 as a build rule: `internal/bus`, `internal/core/...` and `internal/bus/local` never import `internal/gen` or `internal/server`; `internal/bus/remote` imports `internal/gen` and `internal/bus` and nothing under `internal/server` or `internal/core`; `internal/server` imports `internal/gen`, `internal/bus`, `internal/core/...` and never `internal/bus/local` or `internal/bus/remote`. Task 10's `scripts/m4-exit-checks.sh` checks the three with `go list -deps`.
- The document is OpenAPI 3.1.0 (Decision 65) written with no 3.1-only idiom: no `type: [T, "null"]`, no `const`, no `$schema`, no webhooks; an optional member is absent from the JSON, never `null` (the generated field is a pointer). Rationale: that is exactly what the planning probe covered, and the validator and generator are only known to work on that subset.
- Every error on the wire is `application/problem+json` (spec 3.3): the three strict-server hooks, the validator's `ErrorHandlerWithOpts`, and every handler error share `server.WriteProblem`. A `text/plain` body with a status of 400 or more anywhere is a bug and Task 2's `TestEveryFrameworkErrorIsProblem` fails on it.
- Unrecognized inputs are rejected with a test: an unknown `AGENTBUS2_BUS_TRANSPORT` value, an unknown `-store` engine flag combination, a nil `Authenticator` means `RefuseAll` (never accept-all), an enum value outside the document (the validator's 400 carries code `validation`, the same code the core gives, so the suite behaves identically through both transports).
- The Milestone 1 and 3 gate checks stay in force: no `.Skip(` under `internal/conformance` or `internal/dbtest`; `make test` is Docker-free, so `internal/server` and `internal/bus/remote` unit tests open SQLite through `sqlite.Open` on a temp path; both-engine coverage lives in `internal/conformance/bus` only.
- `scripts/check-gates.sh` check 4 mutates the line `^        discover:$` of `api/openapi.yaml`; the real document keeps the `WellKnown` schema's `discover` property at that indentation (`grep -c '^        discover:$' api/openapi.yaml` prints `1`, asserted in Task 1).
- Timeouts: no `http.Server.WriteTimeout` and no client `http.Client.Timeout` below 60 s on any path a `watch` crosses (the server caps the wait at `WatchMaxSeconds` 50; a transport timeout under it would turn a timeout into an error).
- Definition of Done D applies to Task 8 (`Serve` start-up: second run, two servers on one store, a start after a crash).

## Choices this milestone makes (delegated by the spec, recorded with rationale)

1. **Operation table** (spec 3.2: "the others are illustrative and are settled when the schema is written"). Only `/.well-known/agentbus2`, `GET /v1/discover`, `GET /v1/sessions/{session}/watch` and `POST /v1/auth/token` were fixed; the rest are below, chosen so that one `operationId` is one `Bus` method and the path names the aggregate. JSON members are snake_case as v1's tags. "IK" = takes the `Idempotency-Key` header; "tags" is the OpenAPI tag (the API section of Decision 48).

| operationId | Method and path | Security | IK | Request | Responses | Bus method |
| --- | --- | --- | --- | --- | --- | --- |
| `wellKnown` | `GET /.well-known/agentbus2` | `[]` | | | 200 `WellKnown{discover}` | (discovery) |
| `openidConfiguration` | `GET /.well-known/openid-configuration` | `[]` | | | 200 `OpenIDConfiguration` | (discovery, Decision 91) |
| `discover` | `GET /v1/discover` | `[]` | | | 200 `Discovery` | (discovery) |
| `openSession` | `POST /v1/sessions` | bearer, oidc | no (item 12 A) | `OpenSessionRequest{hint?, resume_token?, cursors?[{channel, seq}]}` | 201 `Session` | `Sessions.Open` |
| `closeSession` | `DELETE /v1/sessions/{session}` | | IK | | 204 | `Sessions.Close` |
| `receive` | `POST /v1/sessions/{session}/receive` | | no (item 12 A) | `ReceiveRequest{ack?, count?, channels?, include_own?}` | 200 `ReceiveResult` | `Sessions.Receive` |
| `watch` | `GET /v1/sessions/{session}/watch?after&timeout&include_own` | | | query | 200 `Wake{seq, count, channels}`, 204, 409 `Problem` | `Sessions.Watch` |
| `listIdentities` | `GET /v1/identities` | | | | 200 `[Identity]` | `Sessions.Identities` |
| `listChannels` | `GET /v1/channels` | | | | 200 `[Channel{name, kind}]` | `Messaging.ListChannels` |
| `createChannel` | `POST /v1/channels` | | IK | `CreateChannelRequest{name, kind?, acl?}` | 201 `ChannelInfo` | `Messaging.CreateChannel` |
| `deleteChannel` (tags `channels`, `admin`) | `DELETE /v1/channels/{channel}` | | IK | | 204 | `Messaging.DeleteChannel`, `Admin.DeleteChannel` |
| `renameChannel` (tags `channels`, `admin`) | `PATCH /v1/channels/{channel}` | | IK | `RenameChannelRequest{name}` | 204 | `Messaging.RenameChannel`, `Admin.RenameChannel` |
| `getACL` | `GET /v1/channels/{channel}/acl` | | | | 200 `[ACLEntry]` | `Messaging.ACL` |
| `setACL` | `PUT /v1/channels/{channel}/acl` | | IK | `[ACLEntry]` | 204 | `Messaging.SetACL` |
| `subscribe` | `PUT /v1/channels/{channel}/subscription` | | IK | `SubscribeRequest{from?}` | 204 | `Messaging.Subscribe` |
| `unsubscribe` | `DELETE /v1/channels/{channel}/subscription` | | IK | | 204 | `Messaging.Unsubscribe` |
| `listTagSubscriptions` | `GET /v1/tag-subscriptions` | | | | 200 `[[string]]` | `Messaging.TagSubscriptions` |
| `subscribeTags` | `POST /v1/tag-subscriptions` | | IK | `TagPatterns{patterns}` | 204 | `Messaging.SubscribeTags` |
| `unsubscribeTags` | `DELETE /v1/tag-subscriptions/{patterns}` (comma-joined) | | IK | | 204 | `Messaging.UnsubscribeTags` |
| `sendMessage` | `POST /v1/channels/{channel}/messages` | | IK | `SendRequest{subject?, content, type?, reply_to?, metadata?, tags?, session?}` | 201 `SendResult{seq, memory_id?}` | `Messaging.Send` |
| `history` | `GET /v1/channels/{channel}/messages?before&after&count&tags` | | | query | 200 `[Message]` | `Messaging.History` |
| `search` | `GET /v1/search?q&mode&channel&sender&since&until&thread&tags&count&cursor` | | | query | 200 `SearchResult{hits[{message, score}], next?, semantic_unavailable}` | `Messaging.Search` |
| `getMemory` | `GET /v1/memories/{id}` | | | | 200 `Message` | `Memories.GetMemory` |
| `editMemory` | `POST /v1/memories/{id}/revisions` | | IK | `EditMemoryRequest{subject?, content, type?, metadata?, tags?}` | 201 `EditResult{seq, memory_id, revision, replaced}` | `Memories.Edit` |
| `listMemoryRevisions` | `GET /v1/memories/{id}/revisions` | | | | 200 `[Message]` | `Memories.Revisions` |
| `deleteMemory` | `DELETE /v1/memories/{id}` | | IK | | 204 | `Memories.Delete` |
| `createTask` | `POST /v1/channels/{channel}/tasks` | | IK | `TaskCreateRequest` (v1 `TaskCreateInput` minus `channel` and `idempotency_key`) | 201 `Task` | `Tasks.Create` |
| `listTasks` | `GET /v1/channels/{channel}/tasks?status&owner` | | | query | 200 `[TaskSummary]` | `Tasks.List` |
| `getTask` | `GET /v1/tasks/{id}` | | | | 200 `Task` | `Tasks.GetTask` |
| `updateTask` | `PATCH /v1/tasks/{id}` | | IK | `TaskPatch` (v1 minus `task_id` and `idempotency_key`) | 200 `TaskUpdateResult{task, replaced, deleted}` | `Tasks.Update` |
| `claimTask` | `POST /v1/tasks/{id}/claim` | | IK | `ClaimTaskRequest{leased_until?}` | 200 `TaskUpdateResult` | `Tasks.Claim` |
| `releaseTask` | `POST /v1/tasks/{id}/release` | | IK | | 200 `TaskUpdateResult` | `Tasks.Release` |
| `adminStatus` (tag `admin`) | `GET /v1/admin/status` | | | | 200 `Status` | `Admin.Status` |

   Every operation declares `default: $ref: '#/components/responses/Problem'`; `watch` declares `409` explicitly (spec 3.5's table). Decision 89's "the admin API section keeps both" is one operation each for delete and rename carrying both tags, so the remote `Admin` methods call the same generated operations as `Messaging`'s; two paths for one operation would be two routes to maintain for no behavior. Array query parameters (`tags`, `channels`) are `style: form, explode: false` (comma-joined).
2. **`Idempotency-Key` is an optional header** (`required: false`) on every operation marked IK, and "absent or empty" means "no receipt", exactly as `core.WithReceipt` treats `key == ""`; the remote `Bus` sends the header only when the input's `IdempotencyKey` is non-empty. Rationale: v1's `idempotency_key` is `omitempty` and the Milestone 3 suite calls `Send`, `Edit`, `Create` and the rest with empty keys in most tests; a required header would make the remote `Bus` mint keys, store a receipt per call and make the suite behave differently through the client than locally, which would hollow out the exit criterion. The spike's sample made it required only to prove the binder (need 4). Pending item 1 records the departure from that sample.
3. **Error codes on the wire.** The `Problem.code` enumeration in the document is Milestone 3's eleven codes (`validation`, `not_found`, `conflict`, `forbidden`, `not_registered`, `rate_limited`, `replaced`, `session_lapsed`, `session_closed`, `unavailable`, `internal`) plus one the HTTP layer needs for a failed security requirement (spec 3.3 names it as a framework error that must be problem+json): `unauthenticated`, added to `internal/bus/errors.go` as `CodeUnauthenticated` (401, `Retryable false`) in Task 1, the one file that task touches outside the generated set. Status per code (`server.StatusFor`): `validation` 400, `unauthenticated` 401, `forbidden` 403, `not_registered` 403, `not_found` 404, `conflict` 409, `replaced` 409, `session_lapsed` 410, `session_closed` 410, `rate_limited` 429, `unavailable` 503, `internal` 500; the validator's own statuses map 400 and 422 to `validation`, 401 to `unauthenticated`, 404 to `not_found` (its router reports an unknown path and a wrong method on a known path both as 404, the latter with the text "method not allowed"; probed on 2026-10-21 with nethttp-middleware v1.2.0), 413 to `validation`, anything else to `internal`. `Problem.type` is `urn:agentbus2:problem:<code>` (a URN, because no domain is claimed and RFC 9457 requires only a URI reference), `title` is `http.StatusText(status)`, `detail` the `bus.Error.Message` (already bounded to 1024 bytes by Milestone 3), `instance` the request path.
4. **One error path.** Handlers return `nil, err` for every failure and the strict server's `ResponseErrorHandlerFunc` writes the problem through `server.ProblemFor(err)` (a `*bus.Error` by its code, `context.Canceled` and `DeadlineExceeded` as 499-style silence: the client is gone, nothing is written, any other error `internal`). The generated typed `default` response objects are not used by handlers: with every status decided by the code, one hook is the whole mapping and no handler can forget it. The spike routed only unexpected errors this way; here it is every error, which is why Task 2's hook test asserts a `*bus.Error{conflict}` returned by a handler reaches the client as a typed 409.
5. **Authentication seam.** `server.Authenticator` is `Authenticate(r *http.Request) (identity string, err error)`; it attaches an identity to the request context and never refuses by itself. The document's security requirements are enforced by the validator's `AuthenticationFunc`, which succeeds exactly when the context holds an identity, so which operations need one is stated once, in the document (`security: []` on the three discovery operations), not in Go. `server.RefuseAll` (the default and the only authenticator `agentbus2 server` constructs until Milestone 5) returns `""`, so every protected operation is 401 `unauthenticated`. `server.InProcess` returns `IdentityFrom(r.Context())`, which only `server.InProcessTransport(h, identity)` sets: a context value never crosses a socket, so a network request cannot carry it. This is the local mode of Decisions 35 and 36 (identity is the registered name; the client is in the same process), not a test hook; Milestone 6 wires it for the local profile and Milestone 5 adds the bearer authenticator beside it.
6. **The validator middleware is adopted** (`embedded-spec: true`, `nethttp-middleware` with `ErrorHandlerWithOpts` writing problems). Rationale: ADR 0007 lists it as the recommended auth path for oapi-codegen v2.8.0, the exit criterion names it, and it makes unknown routes and methods problem+json without a hand-written router fallback (its router answers an unknown path and a wrong method, both as 404, before the `ServeMux` would). The `openapi3.T` is loaded once per process (`sync.Once` over `gen.GetSwagger()`), with `Servers` cleared so any host matches.
7. **Discovery is computed from the embedded document** (Decision 65 by construction): `auth_methods` is one entry per `components.securitySchemes` entry, `{name, type, scheme?, bearer_format?, openid_configuration?}`, with the `openIdConnect` entry's URL resolved against the request (`<scheme>://<host>/.well-known/openid-configuration`, scheme `https` when `r.TLS != nil` else `http`); `openid_configuration` repeats that URL; `issuers` is the configured external issuers, `[]` until Milestone 5. `api_versions` is `["v1"]`, `server_version` is `version.Version`, `capabilities` is computed from what is available: `long_poll` always, `semantic_search` when `core.Embedder() != nil`; `pgvector` and `websocket` are never present in this milestone. `/.well-known/openid-configuration` returns `{issuer: <scheme>://<host>, token_endpoint: <issuer>/v1/auth/token, jwks_uri: <issuer>/v1/auth/jwks, token_endpoint_auth_methods_supported: ["private_key_jwt"], token_endpoint_auth_signing_alg_values_supported: ["EdDSA", "ES256"]}` (Decisions 55, 70, 84, 91). The document's `openIdConnectUrl` is the server-relative `/.well-known/openid-configuration` (probed: kin-openapi loads and validates it).
8. **The auth operations are not declared in this milestone's document.** `POST /v1/auth/token`, `GET /v1/auth/jwks` and registration arrive with their schemas in Milestone 5. Rationale: a declared operation needs a strict handler, and a handler that can only say "not yet" would need a status and a code the design does not have (a 503 `unavailable` would tell clients to retry). The `openid-configuration` document names the two paths now so Decision 91's URL is stable; until Milestone 5 they answer 404 `not_found`, stated in pending item 2.
9. **Watch parameters on the wire.** `after` is an optional `int64` defaulting to 0 (the floor rule of Decision 42 with no floor); `timeout` is an optional `number` (`format: double`, generated as `*float64`) of seconds, fractional allowed (`0` and absent mean `WatchMaxSeconds`; above the cap is clamped by the core; negative is `validation`); `include_own` is an optional boolean defaulting to `false` (Decision 45). Rationale for a fractional `timeout`: the Milestone 3 suite watches with sub-second timeouts and asserts that `-1 ns` is `validation`, and an integer-seconds parameter would turn both into 0, which means the cap.
10. **`receive` is a POST** with its body (it writes the pending token and cursors) and no `Idempotency-Key` (Milestone 3 pending item 12, recommendation A, which this plan proceeds on; it is idempotent by construction).
11. **Body limit.** Request bodies are read through `http.MaxBytesReader` with the limit `core.Cfg.MaxMessageKiB * 1024 + 1 MiB` (the largest legitimate body is a send near the envelope bound plus metadata and tags); an oversized body is 413 with code `validation`.
12. **Suite transport switch.** `internal/conformance/bus` reads `AGENTBUS2_BUS_TRANSPORT` once in `TestMain`: `""` or `local` keeps Milestone 3's `local.New`; `inproc` builds, per harness, one `server.New(h.core, server.Options{Auth: server.InProcess{}})` and, per `newBus` call, a `remote.New` over `gen.NewClientWithResponses("http://inproc", gen.WithHTTPClient(&http.Client{Transport: server.InProcessTransport(handler, identity)}))`; any other value fails every test with the two accepted values named. `make test-conformance` runs the `bus` package twice, once per value. Rationale: Milestone 3's `var newBus` exists for this; a subtest layer would rename every test and break `scripts/m3-exit-counts.sh` and the `TestScaffold/postgres` greps in `scripts/check-gates.sh`, while an environment variable keeps every name and log shape. The negative half (that the `inproc` run really crosses the wire) is Task 3's `TestTransportInUse` and Task 10's self-test block 11.
13. **Remote error mapping** (spec 3.3 "never by guessing"). A problem+json body with a known code becomes `&bus.Error{Code, Message: detail (title when detail is empty), Retryable}` by struct literal (never `bus.Errorf`, which panics on an unknown code); an unknown code is preserved as received with the wire's `retryable`, so `bus.CodeOf` returns it and no caller mistakes it for a known one. A non-2xx response without a parsable problem (a proxy's HTML 502) is `{unavailable, "HTTP <status>", true}` for 5xx and `{internal, "HTTP <status>: <first 200 bytes>", false}` otherwise. A transport error returns `ctx.Err()` when the context is done and `{unavailable, err.Error(), true}` otherwise. `watch` 204 is `WatchResult{Woke: false}`; `watch` 409 goes through the problem path and is `replaced`.
14. **Server command flags.** `agentbus2 server -listen <host:port> (-sqlite <path> | -postgres <dsn>)`: `-listen` required (no default port is decided anywhere; port `0` is accepted and the bound address is printed), exactly one store flag required, an actionable message names the missing or conflicting flags. Decision 85's configuration file and `AGENTBUS2_SERVER_*` variables are Milestone 5, which replaces this flag set (pending item 3). The notifier is `notify.NewMemory()` (Decision 52: SQLite server and, until Milestone 7's `NOTIFY`, Postgres); two `agentbus2 server` processes over one SQLite file are therefore unsupported (each would miss the other's hints; a watch would still end at its timeout), and the local multi-process mode of Decision 68 is the Milestone 6 client with `notify.NewPolling`, which Task 9 exercises through `cmd.Serve`. At start, in order: open the store (Decision 34's rules and migrations), `core.New` with `core.Defaults()`, `channels.Service.EnsureGeneral` (Milestone 3 Task 4 names this milestone's server as its caller), the maintenance loop (`maintenance.Service.Tick` every `MaintenanceIntervalSeconds`, stopped on shutdown), then listen. No embedder (its endpoint is Milestone 5 configuration, Decision 85).
15. **`http.Server` settings** for the long-poll: `ReadHeaderTimeout` 10 s, `ReadTimeout` 30 s, `WriteTimeout` 0 (the watch handler's own deadline bounds it), `IdleTimeout` 120 s, graceful `Shutdown` with a 60 s context so armed watches finish (spike need 1: a handler blocked on `r.Context()` sees the client go).
16. **Package layout.** `internal/server` (handler construction, problems, auth seam, in-process transport, discovery, one handler file per aggregate), `internal/server/cmd` (the command: `Run`, `Serve`), `internal/bus/remote` (the remote `Bus`), the suite's `transport_test.go`. `internal/bus/remote` has no knowledge of `internal/server`: the local mode wiring (handler plus transport plus client) is the caller's (Milestone 6's profile code, the suite here).

## Claude proposal, pending Eric

Four points the frozen design does not decide and this plan reads one way; each proceeds on recommendation (A) with a `// pending Eric: Milestone 4 item N` marker on the test that encodes it (`rg -n 'pending Eric: Milestone 4' internal/`), so a reversal is a small, findable change. Task 10 records the answers in the ADR 0001 and 0007 addenda.

1. **`Idempotency-Key` optional on the wire** (choice 2). Spec 3.4 says every mutating call "takes" a key and the spike's sample made the header required. A) Optional; absent means no receipt, as v1's `omitempty` and `core.WithReceipt` (recommended; keeps the suite equivalent through both transports). B) Required; the remote `Bus` mints a key when the input has none. Encoded in Task 6's `TestSendIdempotencyWire`.
2. **Auth paths declared in Milestone 5, not now** (choice 8). The task text allows either. A) Not declared; `/.well-known/openid-configuration` names `/v1/auth/token` and `/v1/auth/jwks`, which 404 until Milestone 5 (recommended; no placeholder handler). B) Declared now with handlers answering a status and code Eric names. Encoded in Task 2's `TestOpenIDConfigurationNamesMilestone5Paths`.
3. **Interim server flags** (choice 14). A) `-listen`, `-sqlite`, `-postgres` until Milestone 5's Decision 85 configuration replaces them (recommended). B) Ship Decision 85's file and variables now, ahead of the rest of Milestone 5. Encoded in Task 8's `TestRunFlags`.
4. **`watch` `timeout` as fractional seconds** (choice 9). Decision 42 writes `timeout=<s>`. A) A JSON number of seconds, so `0.2` is valid (recommended; sub-second timeouts are what the suite and a merge-window test need). B) An integer, with the suite's sub-second watch tests rewritten for the `inproc` transport. Encoded in Task 4's `TestWatchTimeoutWire`.

## Not ported (Decision 31)

Nothing of v1 is an HTTP server; the ports in this milestone are the spike's `inproc` transport and `writeProblem` (`docs/superpowers/specs/2026-10-19-codegen-spike/oapi/spike_test.go`), the spike's `Problem` and `Discovery` schema shapes (`2026-10-19-codegen-spike/openapi.yaml`), and the v1 `main.go` subcommand dispatch and flag style (`loadConfig`, `parseExit`) for `agentbus2 server`. The v1 `watch` and `wait` CLI contracts (`internal/cli/watch.go`, `wait.go`, `main.go`) and the MCP layer are Milestone 6; `internal/filelock` and the wait pidfile are not ported (Decision 68).

## Core and Bus names assumed (Milestone 3 plan revision 3; diffed against `main` before Wave 2)

From `docs/superpowers/plans/2026-10-21-milestone-3-core-bus.md` (`ctx` first everywhere): package `ab2bus "github.com/ericfitz/agentbus2/internal/bus"` with the five interfaces (`Messaging`, `Sessions`, `Memories`, `Tasks`, `Admin`), `Bus`, every input and result type of its Task 1, `Code`, `Error`, `ValidCode`, `CodeOf`, `Errorf`; `core.New`, `core.Defaults`, `core.Config` (`WatchMaxSeconds`, `MaintenanceIntervalSeconds`, `MaxMessageKiB`), `core.Options{Now, Notifier, Embedder, Logger}`, `Core.Embedder()`, `Core.Cfg`; the services `channels.Service{*core.Core}` (`List`, `Create`, `Delete`, `Rename`, `ACL`, `SetACL`, `Subscribe`, `Unsubscribe`, `SubscribeTags`, `UnsubscribeTags`, `TagSubscriptions`, `Status`, `EnsureGeneral`), `sessions.Service` (`Open`, `Close`, `Identities`), `delivery.Service{*core.Core; Sessions sessions.Service}` (`Receive`, `Watch`), `messaging.Service` (`Send`, `History`), `search.Service` (`Search`), `memories.Service` (`Get`, `Revisions`, `Edit`, `Delete`), `tasks.Service` (`Create`, `Get`, `List`, `Update`, `Claim`, `Release`), `maintenance.Service{*core.Core; Tasks tasks.Service}` (`Tick`), each method taking `identity string` after `ctx`; `local.New(core, identity)`; `notify.NewMemory`, `notify.NewPolling`; the suite's `harness` (`t`, `e`, `store`, `core`, `clock`), `newHarness`, `harnessOptions`, `var newBus = func(h *harness, identity string) ab2bus.Bus`, `h.as`, `h.admin`, `h.session`, `codeOf`, and `scripts/m3-exit-counts.sh`; from Milestone 2, `sqlite.Open(ctx, path, sqlite.Options{})`, `postgres.Open(ctx, dsn, postgres.Options{})`, `store.Store.Close`, `dbtest.Engine.DSN`, `dbtest.ForEachEngine`, `dbtest.Run`.

## Review Focus

1. A `watch` whose client disconnects mid-poll: the handler returns when `r.Context()` is canceled, writes nothing, and leaves the registration; the next watch on the session replaces it and the lease is as the last registration left it. (Task 4: `TestWatchClientGoneReleasesHandler`.)
2. A non-2xx response that is not a problem (an HTML 502 from a proxy, a problem with an unknown `code`): the remote `Bus` returns an error the caller can act on by `Retryable`, never panics, never reports success. (Task 3: `TestRemoteNonProblemAndUnknownCode`.)
3. `timeout` above the cap, negative, fractional or absent; `after` absent: clamped, `validation`, honored, cap, floor 0 respectively, and a `timeout` of `abc` is a 400 problem from the validator, not a 500. (Task 4: `TestWatchTimeoutWire`.)
4. A body larger than the limit, a body that is not JSON, a JSON body with a wrong type (`count: "ten"`), an enum outside the document (`kind: "dm"`): each is a 400 or 413 problem with code `validation` and the handler never runs. (Task 2: `TestEveryFrameworkErrorIsProblem`.)
5. The production wiring with no identity: every operation whose security requirement is non-empty, enumerated from the embedded document (paths times methods), answers 401 `unauthenticated` as problem+json, whatever headers the request carries; the three `security: []` operations answer 200. (Task 8: `TestRunRefusesEveryProtectedOperation`.)
6. Two processes, one SQLite file, one session: the older process's `watch` returns 409 `replaced` once the newer registers, through the database alone (Decision 68), and the older client's exit would be 3. (Task 9: `TestWatchReplacedAcrossProcesses`.)

---

### Task 1: OpenAPI 3.1 document, generation, the 3.1 and validator re-test

**Depends on:** none (Milestone 3 Task 1 on `main` for `internal/bus/errors.go`).

**Files:**
- Modify: `api/openapi.yaml` (the real document replacing the Milestone 1 stub), `api/oapi-codegen.yaml` (add `embedded-spec: true`), `internal/gen/gen.go` (regenerated, committed), `internal/gen/smoke_test.go` (the stub gains the embedded-interface trick below), `go.mod`, `go.sum` (`github.com/oapi-codegen/nethttp-middleware` v1.2.0 and `github.com/getkin/kin-openapi` v0.142.0 become direct), `internal/bus/errors.go` (`CodeUnauthenticated`), `internal/bus/errors_test.go` (`ValidCode` knows it)
- Create: `internal/gen/openapi31_test.go` (package `gen_test`)
- Verify unchanged: `scripts/check-gates.sh` check 4's pattern still matches once (Global Constraints)

**Port from:** `docs/superpowers/specs/2026-10-19-codegen-spike/openapi.yaml` (the `Problem`, `Wake`, `WellKnown` and `Discovery` shapes, the `components.responses.Problem` default, the `security` block with `bearerAuth` and `oidc`, the `watch` parameters), extended to the operation table of choice 1 and rewritten to 3.1.0.

**Interfaces (produces; every later task consumes these generated names):**

- `api/openapi.yaml`: `openapi: 3.1.0`, `info.version` `2.0.0-dev`, no `servers`, global `security: [{bearerAuth: []}, {oidc: []}]`, `components.securitySchemes.bearerAuth` (`http`, `bearer`, `bearerFormat: JWT`) and `oidc` (`openIdConnect`, `openIdConnectUrl: /.well-known/openid-configuration`), the operations of choice 1 with their `operationId`s verbatim, every schema named in that table, `Problem.code` with `enum` of the twelve codes of choice 3, `ACLEntry.permissions[]` enum `[read, write, admin]`, `CreateChannelRequest.kind` enum `[ordinary, memory, tasks]`, `SubscribeRequest.from` enum `[now, oldest]`, `search` `mode` enum `[text, semantic, both]`, `RejectedCursor.reason` enum `[before_retention, ahead, unreadable, unsubscribed]`, `Message` with the fields of `bus.Message` (`seq`, `channel`, `sender`, `session?`, `display`, `created_at`, `type`, `subject`, `content`, `reply_to?`, `metadata?`, `memory_id?`, `revision?`, `tags?`, `matched_tags?`), `Session` (`id`, `identity`, `display_name`, `resume_token`, `resumed`, `rejected_cursors`, `pending[{channel, pending}]`), `ReceiveResult` (`messages`, `batch`, `redelivered`, `ack_ignored`, `gaps[{channel, from, to}]`, `notice?`, `instruction?`), `Identity` (`name`, `admin`, `sessions[{id, identity, display_name, opened_at}]`), `Status` (`sessions`, `channels[ChannelInfo]`, `notices[{kind, message, set_at}]`, `embedding_backlog`, `embedding_rejected`, `embedding_rejected_last?`, `embedding_error?`, `embedding_error_at?`), `Task`, `TaskSummary`, `TaskPatch` with v1's JSON member names; `required` lists exactly the members that are never absent (`seq`, `channel`, `sender`, `display`, `created_at`, `type`, `subject`, `content` on `Message`; `content` on `SendRequest` and `EditMemoryRequest`; `name` on `CreateChannelRequest` and `RenameChannelRequest`; `patterns` on `TagPatterns`; `subject` on `TaskCreateRequest`). Every `int64` member carries `format: int64`; `watch.timeout` is `type: number, format: double` (choice 9). Optional members have no default except `watch.include_own` (`false`) and `watch.after` (`0`).
- Generated: `gen.StrictServerInterface` with one method per `operationId` (`WellKnown`, `OpenidConfiguration`, `Discover`, `OpenSession`, `CloseSession`, `Receive`, `Watch`, `ListIdentities`, `ListChannels`, `CreateChannel`, `DeleteChannel`, `RenameChannel`, `GetACL`, `SetACL`, `Subscribe`, `Unsubscribe`, `ListTagSubscriptions`, `SubscribeTags`, `UnsubscribeTags`, `SendMessage`, `History`, `Search`, `GetMemory`, `EditMemory`, `ListMemoryRevisions`, `DeleteMemory`, `CreateTask`, `ListTasks`, `GetTask`, `UpdateTask`, `ClaimTask`, `ReleaseTask`, `AdminStatus`), `<Op>RequestObject`, `<Op>ResponseObject`, `<Op><status>JSONResponse`, `<Op>204Response`, `Watch409ApplicationProblemPlusJSONResponse`, `<Op>Params` with `IdempotencyKey *string`, `gen.NewStrictHandlerWithOptions`, `gen.HandlerWithOptions`, `gen.StdHTTPServerOptions`, `gen.StrictHTTPServerOptions`, `gen.GetSwagger`, `gen.NewClientWithResponses`, `gen.WithHTTPClient`, `<Op>WithResponse`, `gen.Problem`. The implementer records the exact names oapi-codegen produces for the `operationId`s in `internal/gen/doc.go` only if they differ from the table (they should not).

```go
package bus // internal/bus/errors.go, one added constant
const CodeUnauthenticated Code = "unauthenticated" // Milestone 4: a failed security requirement (spec 3.3); ValidCode knows it; the core never produces it
```

- [ ] **Step 1: Write the failing tests**

`internal/gen/openapi31_test.go`: `TestDocumentIs31AndValidates` (`gen.GetSwagger()` loads; `OpenAPI == "3.1.0"`; `Validate(ctx)` nil; `Components.SecuritySchemes` has exactly `bearerAuth` of type `http` scheme `bearer` and `oidc` of type `openIdConnect` with `OpenIdConnectUrl == "/.well-known/openid-configuration"`; `Security` global requirement names both; the three discovery operations have `Security` set and empty; the `Problem.code` enum equals the twelve-code list in order), `TestOperationTable` (every `operationId` of choice 1 is present with its method and path, and no other operation exists: a literal table in the test), `TestValidatorOnRealDocument` (the exit criterion's re-test: `nethttpmiddleware.OapiRequestValidatorWithOptions` over the real document in front of a `gen.NewStrictHandler` whose implementation is `type stub struct{ gen.StrictServerInterface; ... }` with only `SendMessage` and `History` implemented (the nil embedded interface makes any other call panic, which is fine: none is made); with an `AuthenticationFunc` that accepts `Authorization: Bearer ok`: `GET /v1/channels/dev/messages?count=abc` is 400 before the handler; `POST /v1/channels/dev/messages` with `{"content":"hi"}` and the token reaches the handler with the body intact and `Idempotency-Key` bound when sent and `nil` when absent; the same without the token is 401 before the handler; `GET /v1/discover` without a token reaches the handler; `PATCH /v1/sessions/x` and `GET /v1/nope` are both 404 from the validator's router, the former with detail `method not allowed`), `TestSmokeStillRoundTrips` is the existing `smoke_test.go` adapted to the embedded-interface stub. `internal/bus/errors_test.go`: `TestValidCodeKnowsUnauthenticated`.

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/gen/ ./internal/bus/`
Expected: build failure (`undefined: gen.GetSwagger`, `undefined: gen.SendMessageRequestObject`, `undefined: bus.CodeUnauthenticated`).

- [ ] **Step 3: Write the document, regenerate, add the dependencies**

Write `api/openapi.yaml` as in Interfaces; add `embedded-spec: true` to `api/oapi-codegen.yaml`; `make generate`; `go get github.com/oapi-codegen/nethttp-middleware@v1.2.0 github.com/getkin/kin-openapi@v0.142.0 && go mod tidy`; add the constant. Check `grep -c '^        discover:$' api/openapi.yaml` prints `1` and `head -5 internal/gen/gen.go` shows the v2.8.0 header.

- [ ] **Step 4: Run the tests and the gate**

Run: `go test ./internal/gen/ ./internal/bus/`, then `make build-all vet fmt-check lint generate-check test tidy-check`, then `make check-gates` (Docker; check 4 must stay `ok  generator-reacts-to-schema`), then `make verify` (600000 ms).
Expected: pass; `verify: OK`.

- [ ] **Step 5: Commit**

```bash
git add api internal/gen internal/bus/errors.go internal/bus/errors_test.go go.mod go.sum
git commit -m "feat(api): the OpenAPI 3.1 document with both security schemes, regenerated server and client, validator re-tested

Replaces the Milestone 1 stub with every operation of spec section 3.2
except the Milestone 5 auth operations (Decisions 43, 46, 65, 66, 83, 89,
91, 94), extending the Problem, Wake, WellKnown and Discovery shapes of the
spike docs/superpowers/specs/2026-10-19-codegen-spike/openapi.yaml. Generated
by the pinned oapi-codegen v2.8.0 with embedded-spec; the kin-openapi
validator is re-tested on the real 3.1 document (ADR 0007). Adds the
unauthenticated code for failed security requirements (spec 3.3)."
```

---

### Task 2: Server framework: problems, auth seam, validator, hooks, discovery, stubs

**Depends on:** 1 (and the external gate: Milestone 3 on `main`).

**Files:**
- Create: `internal/server/doc.go`, `internal/server/problem.go`, `internal/server/auth.go`, `internal/server/inproc.go`, `internal/server/server.go` (`New`, `Options`, the middleware chain, the spec loader), `internal/server/discovery.go` (`WellKnown`, `OpenidConfiguration`, `Discover`), `internal/server/api.go` (the `api` type, its services, `var _ gen.StrictServerInterface = (*api)(nil)`), and the stubs `internal/server/sessions_stub.go` (Task 4: `OpenSession`, `CloseSession`, `Receive`, `Watch`, `ListIdentities`), `channels_stub.go` (Task 5: `ListChannels`, `CreateChannel`, `DeleteChannel`, `RenameChannel`, `GetACL`, `SetACL`, `Subscribe`, `Unsubscribe`, `ListTagSubscriptions`, `SubscribeTags`, `UnsubscribeTags`, `AdminStatus`), `messages_stub.go` (Task 6: `SendMessage`, `History`, `Search`), `memories_tasks_stub.go` (Task 7: `GetMemory`, `EditMemory`, `ListMemoryRevisions`, `DeleteMemory`, `CreateTask`, `ListTasks`, `GetTask`, `UpdateTask`, `ClaimTask`, `ReleaseTask`), each method returning `nil, errNotImplemented` (`server.go`: `var errNotImplemented = errors.New("server: not implemented in this milestone")`, deleted by Task 10)
- Test: `internal/server/testenv_test.go` (fixtures shared by Tasks 4 to 7), `internal/server/problem_test.go`, `internal/server/server_test.go`, `internal/server/discovery_test.go`

**Port from:** the spike's `inproc` transport and `writeProblem` (`docs/superpowers/specs/2026-10-19-codegen-spike/oapi/spike_test.go`), its `problemServer` hook wiring and `TestSecurityViaValidatorMiddleware`'s `ErrorHandlerWithOpts` and `AuthenticationFunc` shape, and its `Discover` handler.

**Interfaces (produces):**

```go
package server // internal/server
type Authenticator interface { Authenticate(r *http.Request) (identity string, err error) } // "" and nil: no identity (protected operations then fail the document's security requirement); err: 401 with err.Error() as detail
type RefuseAll struct{}   // always "", nil; the default and what agentbus2 server uses until Milestone 5
type InProcess struct{}   // IdentityFrom(r.Context()), nil; set only by InProcessTransport (Decisions 35, 36)
func WithIdentity(ctx context.Context, identity string) context.Context
func IdentityFrom(ctx context.Context) string
func InProcessTransport(h http.Handler, identity string) http.RoundTripper // ServeHTTP on an httptest.ResponseRecorder with req.WithContext(WithIdentity(req.Context(), identity)); returns req.Context().Err() when the context is done (spike need 6)
type Options struct { Auth Authenticator /* nil: RefuseAll */; Version string /* "": version.Version */; Issuers []string /* external issuers for discover; nil until Milestone 5 */; Logger *slog.Logger /* nil: discard */ }
func New(c *core.Core, o Options) http.Handler // chain, outermost first: body limit (choice 11) → auth (attaches identity; an Authenticate error is a 401 problem) → validator (security and request validation; errors through WriteProblem) → gen.HandlerWithOptions(strict, StdHTTPServerOptions{BaseRouter: http.NewServeMux(), ErrorHandlerFunc}) with gen.NewStrictHandlerWithOptions(api, nil, StrictHTTPServerOptions{RequestErrorHandlerFunc, ResponseErrorHandlerFunc})
func Spec() *openapi3.T  // gen.GetSwagger() once (sync.Once), Servers cleared; panics on a load error (a build defect, not a runtime condition)

// problem.go
const ProblemTypePrefix = "urn:agentbus2:problem:"
func StatusFor(code bus.Code) int                                   // choice 3's table; an unknown code is 500
func ProblemFor(err error) (status int, p gen.Problem, write bool)   // *bus.Error → StatusFor(code), detail Message, retryable Retryable; context.Canceled / DeadlineExceeded → write false (nothing is sent); any other error → 500 internal retryable true with err.Error() as detail
func WriteProblem(w http.ResponseWriter, r *http.Request, status int, code bus.Code, detail string, retryable bool) // Content-Type application/problem+json, type ProblemTypePrefix+code, title http.StatusText, instance r.URL.Path
func validatorCode(status int) bus.Code                             // 400, 413, 422 → validation; 401 → unauthenticated; 404 → not_found; else internal

// discovery.go
func Capabilities(c *core.Core) []string          // ["long_poll"] plus "semantic_search" when c.Embedder() != nil; sorted
func baseURL(r *http.Request) string              // https when r.TLS != nil else http, plus r.Host
```

```go
package server // testenv_test.go
type env struct { t *testing.T; store store.Store; core *core.Core; clock *fakeClock; embedder core.Embedder }
func newEnv(t *testing.T, o envOptions) *env     // sqlite.Open on t.TempDir(), core.New(core.Defaults() with o.cfg overrides, Options{Now: clock, Notifier: notify.NewMemory(), Embedder: o.embedder})
type envOptions struct { cfg func(*core.Config); embedder bool; auth Authenticator /* nil: InProcess{} */ }
func (e *env) handler() http.Handler                              // New(e.core, Options{Auth: o.auth})
func (e *env) identity(name string, admin bool)                  // Store.CreateIdentity (+ SetAdmin)
func (e *env) client(identity string) *gen.ClientWithResponses   // over InProcessTransport(e.handler(), identity); identity "" → no identity on the context
func (e *env) raw(identity string, req *http.Request) *http.Response // the same transport, for requests the generated client cannot send (a malformed body, a wrong method)
type fakeClock struct{...}
```

- [ ] **Step 1: Write the failing tests**

`problem_test.go`: `TestStatusForEveryCode` (a table over the twelve codes; an unknown code is 500), `TestProblemForContextErrorsWriteNothing`, `TestWriteProblemShape` (content type, `type`, `title`, `status`, `detail`, `instance`, `code`, `retryable` members; `detail` of 2000 bytes passes through unchanged because the core already bounded it, so the helper truncates nothing). `server_test.go`: `TestEveryFrameworkErrorIsProblem` (Review Focus 4, through `e.raw` with an identity: `GET /v1/nope` 404 `not_found`; `PATCH /v1/sessions/x` 404 `not_found` with detail `method not allowed`; `GET /v1/sessions/s/watch?timeout=abc` 400 `validation`; `POST /v1/channels` with body `{"name":1}` 400 `validation`; with `not json` 400; with `{"name":"a","kind":"dm"}` 400 `validation`; a body of the limit plus one byte 413 `validation`; and every response's `Content-Type` is `application/problem+json`; the test fails on any `text/plain`), `TestSecurityFromDocument` (without an identity: `GET /v1/channels` 401 `unauthenticated` problem; `GET /v1/discover`, `GET /.well-known/agentbus2`, `GET /.well-known/openid-configuration` 200; with `InProcess` and the registered identity `pat` (`e.identity("pat", false)`): `GET /v1/channels` is not 401, which proves the chain order (500 `internal` from the stub now, 200 once Task 5 lands; the test asserts only `!= 401` so Task 5 cannot break it)), `TestRefuseAllNeverAttaches` (`envOptions{auth: RefuseAll{}}`: a request with `Authorization: Bearer anything` and the in-process identity on the context is still 401), `TestAuthenticateErrorIs401` (an `Authenticator` returning an error: 401 with its text as `detail`), `TestResponseErrorHookWritesTypedProblem` (choice 4: the `ResponseErrorHandlerFunc` the server installs, reachable as the package function `responseError(w, r, err)`, called with `&bus.Error{Code: bus.CodeConflict, Message: "taken", Retryable: false}` writes 409 `application/problem+json` with `code conflict` and `detail taken`; with `context.Canceled` it writes nothing (the recorder's status stays 200 and the body empty); with `errors.New("disk")` it writes 500 `internal` retryable; the end-to-end form through a real handler is Task 4's `TestWatchWire200And409` and Task 5's `TestCreateChannelIdempotencyHeader`), `TestInProcessTransportCancels` (a canceled context returns `context.Canceled` from `RoundTrip`). `discovery_test.go`: `TestWellKnownPointsAtDiscover` (`discover == "/v1/discover"`), `TestDiscoverAgreesWithDocument` (Decision 65: `auth_methods` names equal the keys of `Spec().Components.SecuritySchemes` with their types; the `oidc` entry's `openid_configuration` and the top-level member equal `http://inproc/.well-known/openid-configuration`; `api_versions == ["v1"]`; `server_version == version.Version`; `issuers == []`), `TestCapabilitiesComputed` (no embedder: `["long_poll"]`; with the fake embedder: `["long_poll", "semantic_search"]`), `TestOpenIDConfigurationNamesMilestone5Paths` (`// pending Eric: Milestone 4 item 2`; `issuer == "http://inproc"`, `token_endpoint == issuer + "/v1/auth/token"`, `jwks_uri == issuer + "/v1/auth/jwks"`, the two `token_endpoint_*` lists as choice 7; `GET /v1/auth/token` is 404 `not_found` in this milestone), `TestBaseURLHonorsTLS` (`r.TLS` set → `https://`).

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/server/`
Expected: build failure (`undefined: New`).

- [ ] **Step 3: Implement** as in Interfaces. The validator is `nethttpmiddleware.OapiRequestValidatorWithOptions(Spec(), &nethttpmiddleware.Options{Options: openapi3filter.Options{AuthenticationFunc: func(ctx, in) error { if IdentityFrom(ctx) == "" { return errors.New("no identity") }; return nil }}, ErrorHandlerWithOpts: func(ctx, err, w, r, opts) { status := opts.StatusCode; if errors.As(err, new(*http.MaxBytesError)) { status = 413 }; WriteProblem(w, r, status, validatorCode(status), err.Error(), false) }})` (the body limit surfaces inside the validator's body read as a `*http.MaxBytesError`, which is how 413 is told from 400; if `openapi3filter.RequestError`'s wrapping hides it from `errors.As`, the body-limit middleware checks `r.ContentLength` and a `MaxBytesError` from its own read before the validator runs and writes the 413 itself); the `AuthenticationFunc` reads the request's context, which the auth middleware replaced before the validator ran. The three strict-server hooks: `ErrorHandlerFunc` and `RequestErrorHandlerFunc` write 400 `validation`; `ResponseErrorHandlerFunc` writes `ProblemFor(err)` when `write` is true and logs at debug otherwise. `api` holds the seven services built from the `*core.Core` (`delivery.Service{Core: c, Sessions: sessions.Service{c}}`).

- [ ] **Step 4: Run the tests and the gate**

Run: `make build-all vet fmt-check lint test`, then `make verify` (600000 ms).
Expected: pass; `verify: OK`.

- [ ] **Step 5: Commit**

```bash
git add internal/server
git commit -m "feat(server): http.Handler over the core with problem+json hooks, the validator, the auth seam, discovery and the in-process transport

Ports the inproc transport, writeProblem and the three strict-server hooks
from the spike docs/superpowers/specs/2026-10-19-codegen-spike/oapi/spike_test.go
(spec 3.3, Decisions 35, 46); discovery computed from the embedded document
(Decisions 43, 65, 91); the Authenticator seam with RefuseAll and InProcess
(Decisions 8, 36, 64). Handlers are stubs replaced by Tasks 4 to 7."
```

---

### Task 3: Remote Bus skeleton, in-process wiring, suite transport switch

**Depends on:** 2.

**Files:**
- Create: `internal/bus/remote/doc.go`, `internal/bus/remote/remote.go` (`Bus`, `New`, the error mapping, `var _ ab2bus.Bus = (*Bus)(nil)`), `internal/bus/remote/convert.go` (generated ↔ domain for the shared types: `Message`, `ACLEntry`, `Kind`, `Permission`, `Task`, `TaskSummary`), and the stubs `sessions_stub.go` (Task 4: `Open`, `Close`, `Receive`, `Watch`, `Identities`), `channels_stub.go` (Task 5: the eleven channel and subscription methods plus `Status`), `messages_stub.go` (Task 6: `Send`, `History`, `Search`), `memories_tasks_stub.go` (Task 7: `GetMemory`, `Edit`, `Delete`, `Revisions`, `Create`, `GetTask`, `List`, `Update`, `Claim`, `Release`), each returning `ErrNotImplemented` (`remote.go`: `var ErrNotImplemented`, deleted by Task 10)
- Create: `internal/conformance/bus/transport_test.go`; Modify: `internal/conformance/bus/main_test.go` (read the variable before `dbtest.Run`)
- Test: `internal/bus/remote/remote_test.go` (Docker-free: an `httptest.Server` serving canned responses)

**Interfaces (produces):**

```go
package remote // internal/bus/remote; imports internal/gen and internal/bus only
type Bus struct { c *gen.ClientWithResponses }
func New(c *gen.ClientWithResponses) *Bus
var _ ab2bus.Bus = (*Bus)(nil)
func errorOf(ctx context.Context, status int, p *gen.Problem, body []byte, err error) error // choice 13; nil for a 2xx with err nil
func key(k string) *string      // "" → nil (header omitted), else &k (choice 2)
func seconds(d time.Duration) *float64 // the watch timeout: 0 → nil; else d.Seconds() (a negative duration stays negative)
```

```go
package bus // internal/conformance/bus/transport_test.go
const transportEnv = "AGENTBUS2_BUS_TRANSPORT"
var transport string // "local" or "inproc", set in TestMain before dbtest.Run; any other value: fmt.Fprintln(os.Stderr, ...) and os.Exit(2) naming the two values
func handlerFor(h *harness) http.Handler   // one server.New(h.core, server.Options{Auth: server.InProcess{}}) per harness, kept in a mutex-guarded map[*harness]http.Handler
// in TestMain (main_test.go): transport = os.Getenv(transportEnv); if "" → "local"; if "inproc" → newBus = func(h, identity) ab2bus.Bus { c, _ := gen.NewClientWithResponses("http://inproc", gen.WithHTTPClient(&http.Client{Transport: server.InProcessTransport(handlerFor(h), identity)})); return remote.New(c) }
```

- [ ] **Step 1: Write the failing tests**

`remote_test.go` (a `httptest.NewServer` handler returning a canned status, content type and body per test; `remote.New` over `gen.NewClientWithResponses(srv.URL)`; the stubbed methods are not called, only the error mapping and the two helpers are, through an exported-for-test shim `ErrorOf`): `TestRemoteMapsKnownProblem` (409 `{"code":"conflict","detail":"taken","retryable":false,...}` → `*bus.Error{conflict, "taken", false}`; `bus.CodeOf` is `conflict`), `TestRemoteNonProblemAndUnknownCode` (Review Focus 2: 502 `text/html` → `unavailable` retryable; 418 `text/plain` body `teapot` → `internal` with `HTTP 418: teapot`, not retryable; 400 problem with `"code":"nonsense","retryable":true` → `Code == "nonsense"` preserved, `Retryable true`, no panic), `TestRemoteTransportErrors` (a closed server → `unavailable` retryable; a canceled context → `context.Canceled`), `TestRemoteKeyAndSeconds` (`key("")` nil, `key("k")` `"k"`; `seconds(0)` nil, `seconds(200*time.Millisecond)` `0.2`, `seconds(-1)` negative). `transport_test.go`: `TestTransportInUse` (under `local`: `newBus(h, "pat")` is a `*local.Bus`; under `inproc`: a `*remote.Bus`, and one `DiscoverWithResponse` through a `gen` client over `server.InProcessTransport(handlerFor(h), "pat")` is 200 and leaves the harness's request counter (a wrapping `http.Handler` in `handlerFor`) at exactly one; the test asserts `transport` is one of the two values. Task 10 extends it: under `inproc`, `ListChannels` through `newBus` raises the counter, which the stubs of this task cannot do).

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/bus/remote/ && go test -count=1 -p 1 -run '^TestTransportInUse$' ./internal/conformance/bus/`
Expected: build failure (`undefined: remote.New`).

- [ ] **Step 3: Implement** as in Interfaces. `main_test.go` keeps `TestMain(m) { os.Exit(dbtest.Run(m)) }` with the transport selection inserted before `dbtest.Run`. Under `inproc` every Milestone 3 test except `TestTransportInUse` and `TestScaffold` fails with `ErrNotImplemented` until Tasks 4 to 7 land; that is expected and is why `make test-conformance` does not run the `inproc` pass until Task 10.

- [ ] **Step 4: Run the tests and the gate**

Run: `make build-all vet fmt-check lint test`, then `AGENTBUS2_BUS_TRANSPORT=inproc go test -count=1 -p 1 -run '^Test(TransportInUse|Scaffold)$' ./internal/conformance/bus/`, then `AGENTBUS2_BUS_TRANSPORT=bogus go test -count=1 -p 1 -run '^TestScaffold$' ./internal/conformance/bus/ 2>&1 | tail -3`, then `make verify` (600000 ms; the local pass only).
Expected: pass; the `inproc` subset passes on both engines; `bogus` exits non-zero with `AGENTBUS2_BUS_TRANSPORT must be local or inproc`; `verify: OK`.

- [ ] **Step 5: Commit**

```bash
git add internal/bus/remote internal/conformance/bus/transport_test.go internal/conformance/bus/main_test.go
git commit -m "feat(bus): remote Bus skeleton over the generated client with the problem mapping; the suite's transport switch

Generated types stay behind the Bus interfaces (Decision 47); unknown codes
are kept as received and judged by retryable and status (spec 3.3); the
conformance suite selects local.New or the remote Bus over the in-process
transport (Decision 35) by AGENTBUS2_BUS_TRANSPORT."
```

---

### Task 4: Sessions, receive, watch, identities (handlers and remote)

**Depends on:** 3.

**Files:**
- Delete: `internal/server/sessions_stub.go`, `internal/bus/remote/sessions_stub.go`; Create: `internal/server/sessions.go`, `internal/bus/remote/sessions.go`
- Test: `internal/server/sessions_test.go`

**Interfaces:**
- Consumes: `sessions.Service.Open`, `Close`, `Identities`, `delivery.Service.Receive`, `Watch`; Task 2's `env`; Task 3's `errorOf`, `key`, `seconds`.
- Produces: the five `api` methods and the five `remote.Bus` methods, with these wire rules: `Watch` returns `gen.Watch200JSONResponse` when `Woke`, `gen.Watch204Response{}` otherwise, and `nil, err` for `replaced` (the hook writes 409); `WatchInput.Timeout` is `time.Duration(float64(*timeout) * float64(time.Second))` when the parameter is set, else 0; `After` is the parameter or 0; `IncludeOwn` the parameter or false; the remote omits `after`, `timeout` and `include_own` at their zero values and sends `timeout` through `seconds`; a 204 is `WatchResult{Woke: false}`; `Receive` maps `count` 0 to absent and the server maps absent to 0 (the core defaults it); `Open` returns 201 with the token; `Close` 204.

- [ ] **Step 1: Write the failing tests**

`sessions_test.go` (`newEnv`, identity `pat`, through the generated client and, where the client cannot express a case, `e.raw`): `TestWatchTimeoutWire` (`// pending Eric: Milestone 4 item 4`; Review Focus 3; cfg `WatchMaxSeconds: 1`: `timeout=600` returns 204 between 1 s and 1.5 s; `timeout=-1` is 400 `validation`; `timeout=0.2` with nothing arriving returns 204 within 0.5 s; absent `timeout` and absent `after` are accepted (204 after the cap); `timeout=abc` is 400 from the validator), `TestWatchWire200And409` (a goroutine sends through the core's `messaging.Service.Send` after 100 ms: 200 with `seq`, `count 1`, `channels ["dev"]`; a second watch on the session: the first returns 409 with `Content-Type: application/problem+json` and `code: replaced`; `ApplicationproblemJSON409` is set on the client), `TestWatchClientGoneReleasesHandler` (Review Focus 1: a 100 ms client context; the handler returns within 500 ms, observed through a wrapping handler that records completion; the session's `WatchID` through the Store is unchanged; a following watch succeeds), `TestReceiveIsNonBlocking` (Decision 83: an empty subscription returns 200 with no messages at once, under 100 ms), `TestOpenCloseReceiveRoundTrip` (open with a hint → 201 with `display_name pat/impl` and a 43-character token; receive after a send → the batch; receive with `ack` → empty; close → 204; receive after close → 410 `session_closed`), `TestReceiveNoIdempotencyKey` (Milestone 3 item 12 A through the wire: `Idempotency-Key` on `receive` is ignored by the binder (no such parameter) and two receives without `ack` redeliver under the same token), `TestIdentitiesWire` (two identities with live sessions; the admin flag).

- [ ] **Step 2: Run to verify they fail**

Run: `go test -run 'Watch|Receive|Open|Identities' ./internal/server/ && AGENTBUS2_BUS_TRANSPORT=inproc go test -count=1 -p 1 -run '^Test(Session|Receive|Watch)' ./internal/conformance/bus/`
Expected: `--- FAIL` with the not-implemented 500s, then `ErrNotImplemented` from the remote stubs.

- [ ] **Step 3: Implement**; delete the two stubs. `Watch` passes `ctx` straight to the service (the strict wrapper gives it `r.Context()`, spike need 1).

- [ ] **Step 4: Run the suite under both transports and the gate**

Run: `go test ./internal/server/`, then `env -u AGENTBUS2_TEST_POSTGRES_DSN AGENTBUS2_BUS_TRANSPORT=inproc go test -count=1 -p 1 -v -run '^Test(Session|Receive|Watch)' ./internal/conformance/bus/ 2>&1 | grep -E '^(--- FAIL|ok|FAIL)'`, then the same with `-race`, then `make verify` (600000 ms).
Expected: no `--- FAIL` on either engine; `ok` under race; `verify: OK`.

- [ ] **Step 5: Commit**

```bash
git add internal/server internal/bus/remote
git commit -m "feat(server): session open and close, non-blocking receive, long-poll watch with 200/204/409, identities, on both sides of the wire

Decisions 15, 16, 40, 42, 45, 68, 83; the watch timeout travels as
fractional seconds; the pending-batch, merge and replaced rules stay in the
core (Milestone 3 Task 10)."
```

---

### Task 5: Channels, ACL, subscriptions, admin status (handlers and remote)

**Depends on:** 3.

**Files:**
- Delete: `internal/server/channels_stub.go`, `internal/bus/remote/channels_stub.go`; Create: `internal/server/channels.go`, `internal/server/admin.go`, `internal/bus/remote/channels.go`, `internal/bus/remote/admin.go`
- Test: `internal/server/channels_test.go`

**Interfaces:**
- Consumes: `channels.Service` (the twelve methods); Task 2's `env`; Task 3's helpers and converters.
- Produces: twelve `api` methods and the `remote.Bus` `Messaging` channel methods plus `Admin.Status`, `Admin.DeleteChannel` and `Admin.RenameChannel` (the latter two call the same generated operations as the `Messaging` ones). Wire rules: `CreateChannelRequest.acl` absent → `ACL nil` (the configured default), `[]` → `ACL []bus.ACLEntry{}` (empty), so the remote sends the member only when the input's `ACL != nil` (Milestone 2 choice 9 through the wire); `kind` absent → `""`; `unsubscribeTags` splits `{patterns}` on `,`; `listTagSubscriptions` returns the sets as arrays.

- [ ] **Step 1: Write the failing tests**

`channels_test.go`: `TestCreateChannelACLNilVersusEmpty` (the two bodies through `e.raw`; the stored rows through the Store: the default three rows versus none), `TestCreateChannelIdempotencyHeader` (same key twice → one channel and the same 201 body; same key with another `kind` → 409 `conflict`; no header → a second create of the same name is 409 `conflict` from the core, not a replay), `TestACLWire` (set with an entry naming `Permission("owner")` → 400 `validation` from the validator's enum, never reaching the core; get as a non-admin → 403 `forbidden`), `TestDeleteAndRenameCarryAdminTag` (the embedded document's `deleteChannel` and `renameChannel` operations have tags `channels` and `admin`; `GET /v1/admin/status` as a non-admin is 403), `TestTagSubscriptionsWire` (subscribe `["env:*","deploy"]`; list shows one sorted set; `DELETE /v1/tag-subscriptions/deploy,env:*` removes it; a pattern failing the tag rule is 400 `validation`).

- [ ] **Step 2: Run to verify they fail**

Run: `go test -run 'Channel|ACL|Tag|Admin' ./internal/server/ && AGENTBUS2_BUS_TRANSPORT=inproc go test -count=1 -p 1 -run '^Test(Channel|Subscription|Admin|ACL)' ./internal/conformance/bus/`
Expected: not-implemented failures.

- [ ] **Step 3: Implement**; delete the two stubs.

- [ ] **Step 4: Run the suite under both transports and the gate**

As Task 4 Step 4 with `-run '^Test(Channel|Subscription|Admin|ACL)'` (the ACL matrix's rows for channels pass once Tasks 4, 6 and 7 are also on `main`; until then run `-run '^Test(Channel|Subscription|Admin)'` and record it). Expected: no `--- FAIL`; `verify: OK`.

- [ ] **Step 5: Commit**

```bash
git add internal/server internal/bus/remote
git commit -m "feat(server): channels with kinds and ACLs, subscriptions, tag subscriptions and admin status on both sides of the wire

Decisions 37, 38, 80, 88, 89, 93, 94; delete and rename are one operation
each carrying the channels and admin tags (Decision 89)."
```

---

### Task 6: Send, history, search (handlers and remote)

**Depends on:** 3.

**Files:**
- Delete: `internal/server/messages_stub.go`, `internal/bus/remote/messages_stub.go`; Create: `internal/server/messages.go`, `internal/bus/remote/messages.go`
- Test: `internal/server/messages_test.go`

**Interfaces:**
- Consumes: `messaging.Service.Send`, `History`, `search.Service.Search` (the latter's package is imported as `coresearch "github.com/ericfitz/agentbus2/internal/core/search"` in `api.go`, Task 2, because `internal/search` may be imported beside it); Task 2's `env`; Task 3's helpers.
- Produces: three `api` methods and three `remote.Bus` methods. Wire rules: `SendRequest.session` → `SendInput.Session`; `reply_to` pointer; `history` `before`/`after` pointers and `count` absent → 0; `search` `q` required, `mode` absent → `""`, `tags` comma-joined, `cursor` string; `SearchResult.hits[i]` is `{message, score}`.

- [ ] **Step 1: Write the failing tests**

`messages_test.go`: `TestSendIdempotencyWire` (`// pending Eric: Milestone 4 item 1`; Review Focus 4's wire half: the same key and body twice → the same `seq` and one row; the same key with another `content` → 409 `conflict`; the header absent on two identical sends → two rows; `Idempotency-Key:` with an empty value behaves as absent), `TestSendSessionAttribution` (a `session` of the sender's live session → `Message.session` and `display` in `history`; another identity's session id → 404 `not_found`), `TestHistoryWire` (`before`, `after`, `count`, `tags=env*`; `count=-1` is 400 `validation`; `count=abc` 400), `TestSearchWire` (`q` missing is 400 from the validator; `mode=fuzzy` 400 `validation`; a hit's `message` and `score` members; `next` present when more remain).

- [ ] **Step 2: Run to verify they fail**

Run: `go test -run 'Send|History|Search' ./internal/server/ && AGENTBUS2_BUS_TRANSPORT=inproc go test -count=1 -p 1 -run '^Test(Send|History|Search)' ./internal/conformance/bus/`
Expected: not-implemented failures.

- [ ] **Step 3: Implement**; delete the two stubs. The remote `Send` passes the key on one line that appears verbatim, `IdempotencyKey: key(in.IdempotencyKey),` (Task 10's self-test block 11 mutates it).

- [ ] **Step 4: Run the suite under both transports and the gate**

As Task 4 Step 4 with `-run '^Test(Send|History|Search)'`; `TestSendConcurrentSeqOrder` and `TestSearchMatchesAgreeAcrossEngines` must pass under `inproc` on both engines. Expected: no `--- FAIL`; `verify: OK`.

- [ ] **Step 5: Commit**

```bash
git add internal/server internal/bus/remote
git commit -m "feat(server): send with the Idempotency-Key header, history and search on both sides of the wire

Decisions 46, 51, 66, 67, 86, 95; the key travels as the header and the
core's receipts replay it."
```

---

### Task 7: Memories and tasks (handlers and remote)

**Depends on:** 3.

**Files:**
- Delete: `internal/server/memories_tasks_stub.go`, `internal/bus/remote/memories_tasks_stub.go`; Create: `internal/server/memories.go`, `internal/server/tasks.go`, `internal/bus/remote/memories.go`, `internal/bus/remote/tasks.go`
- Test: `internal/server/memories_tasks_test.go`

**Interfaces:**
- Consumes: `memories.Service.Get`, `Revisions`, `Edit`, `Delete`; `tasks.Service.Create`, `Get`, `List`, `Update`, `Claim`, `Release`; Task 2's `env`; Task 3's helpers and the `Task`/`TaskSummary` converters.
- Produces: ten `api` methods and ten `remote.Bus` methods. Wire rules: `EditMemoryRequest.subject` pointer (absent keeps, `""` clears), `tags` absent → nil (keeps), `[]` → clears (the remote sends the member only when `Tags != nil`); `TaskPatch` members are pointers as v1; `ClaimTaskRequest.leased_until` absent → 0; `listTasks` `status` absent → `""`.

- [ ] **Step 1: Write the failing tests**

`memories_tasks_test.go`: `TestEditMemoryKeepVersusClear` (three bodies: no `tags`, `"tags":[]`, no `subject` with `"subject":""`; the stored revision through the Store), `TestMemoryRoutesWire` (get 404 after delete; revisions still list; `POST .../revisions` 201 with `revision 2`), `TestTaskRoutesWire` (create on an ordinary channel 400 `validation`; `status=paused` on list 400 `validation`; claim, patch with `{"status":"completed"}`, release; `PATCH` with `{"status":"bogus"}` 400 `validation`; `claim` with the same key twice → the same result), `TestTaskPatchMembersRoundTrip` (every `TaskPatch` member set once through the client reaches the core: observed through the resulting `Task`).

- [ ] **Step 2: Run to verify they fail**

Run: `go test -run 'Memory|Task' ./internal/server/ && AGENTBUS2_BUS_TRANSPORT=inproc go test -count=1 -p 1 -run '^Test(Memory|Task)' ./internal/conformance/bus/`
Expected: not-implemented failures.

- [ ] **Step 3: Implement**; delete the two stubs.

- [ ] **Step 4: Run the suite under both transports and the gate**

As Task 4 Step 4 with `-run '^Test(Memory|Task)'`. Expected: no `--- FAIL`; `verify: OK`.

- [ ] **Step 5: Commit**

```bash
git add internal/server internal/bus/remote
git commit -m "feat(server): memories with revisions and tasks with leases on both sides of the wire

Decisions 46, 81, 90, 95; v1 task JSON member names kept on the wire."
```

---

### Task 8: `agentbus2 server` command

**Depends on:** 2 (runs beside Tasks 3 to 7; it needs only `server.New`, which compiles over the stubs).

**Files:**
- Create: `internal/server/cmd/doc.go`, `internal/server/cmd/cmd.go` (`Run`, flags), `internal/server/cmd/serve.go` (`Serve`, `Config`, the start-up order, the maintenance loop, the `http.Server`)
- Modify: `main.go` (the `server` case and the usage line), `main_test.go` (usage lists `server`)
- Test: `internal/server/cmd/cmd_test.go` (Docker-free, SQLite), `internal/conformance/bus/server_test.go` (both engines through `Serve`)

**Port from:** v1 `main.go` (`run`'s subcommand dispatch, `loadConfig`'s `flag.NewFlagSet` with `flag.ContinueOnError`, `parseExit`), adapted to the two-flag store choice.

**Interfaces (produces):**

```go
package cmd // internal/server/cmd
type Config struct { Listen string; Store store.Store /* already open */; Auth server.Authenticator /* nil: RefuseAll */; Notifier notify.Notifier /* nil: notify.NewMemory() */; Cfg *core.Config /* nil: core.Defaults() */; Now func() time.Time /* nil: time.Now; passed to core.Options.Now so tests drive the lease and the maintenance deadline (Decision 49) */; Logger *slog.Logger; Ready func(addr net.Addr) /* called once listening; tests read the port */ }
func Serve(ctx context.Context, c Config) error // open order of choice 14: core.New → channels.Service.EnsureGeneral → maintenance goroutine (Tick every MaintenanceIntervalSeconds, first tick after one interval, stopped with ctx) → net.Listen("tcp", Listen) → Ready → http.Server with choice 15's timeouts and server.New(core, Options{Auth}); returns when ctx is done after Shutdown (60 s) and the maintenance goroutine has returned; the Store is closed by the caller
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int // flags -listen, -sqlite, -postgres; exactly one of the two store flags (else exit 2 with "exactly one of -sqlite or -postgres is required"); -listen required ("-listen is required, for example 127.0.0.1:0"); opens the store (sqlite.Open / postgres.Open), prints "agentbus2 server listening on <addr> (<engine>)" to stdout from Ready, Serve with Auth RefuseAll{} and Notifier nil; exit 0 on a clean shutdown, 1 on an error (message to stderr), 2 on a flag error; -h prints usage, exit 0
// main.go: case "server": return cmd.Run(signalContext(), args[1:], stdout, stderr) with SIGINT and SIGTERM canceling the context
```

- [ ] **Step 1: Write the failing tests**

`cmd_test.go`: `TestRunFlags` (`// pending Eric: Milestone 4 item 3`; no flags → 2 and both messages; both store flags → 2; `-h` → 0 with usage; `-listen 127.0.0.1:0 -sqlite <tmp>` with a context canceled after `Ready` → 0 and the stdout line), `TestRunRefusesEveryProtectedOperation` (Review Focus 5: `Run` with `-listen 127.0.0.1:0 -sqlite <tmp>` in a goroutine, the address parsed from the `listening on` stdout line; for every path and method of `server.Spec().Paths` whose operation's security is non-empty, a request with `Authorization: Bearer x`, an `X-Agentbus2-Test-Identity: pat` header and a JSON body `{}` where a body is required gets 401 `unauthenticated` as problem+json; the three `security: []` operations get 200; the count of protected operations checked equals the document's, asserted against a literal 30), `TestServeStartsGeneralAndMaintenance` (after `Ready`, the Store lists `general` with the default rows and no creator; with `MaintenanceIntervalSeconds: 1` and a seeded expired receipt, the receipt is gone within 3 s), `TestServeSecondRunAndCrashLeftovers` (Definition of Done D: `Serve` twice in sequence on one SQLite file: one `general`, both start; a `Serve` canceled mid-way (right after `Ready`) followed by another: starts, migrations run once, the maintenance lease row of the first is taken over after `2 * interval` on the injected `Now` clock, which the test advances), `TestServeTimeoutsAllowLongPoll` (the `http.Server` the command builds has `WriteTimeout == 0` and `ReadHeaderTimeout == 10 * time.Second`, through an exported-for-test `httpServer(cfg)` builder), `TestUsageListsServer` in `main_test.go`. `internal/conformance/bus/server_test.go`: `TestServerServesBothEngines` (`dbtest.ForEachEngine`: `openStore` as the suite does, `Serve` on `127.0.0.1:0` in a goroutine with `Ready`; `GET /.well-known/agentbus2` 200; `GET /v1/discover` lists `long_poll`; `GET /v1/channels` 401 problem; cancel; `Serve` returns nil within 5 s). This test is transport-independent and runs identically under both `AGENTBUS2_BUS_TRANSPORT` values.

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/server/cmd/ . && go test -count=1 -p 1 -run '^TestServerServesBothEngines$' ./internal/conformance/bus/`
Expected: build failure (`undefined: cmd.Run`).

- [ ] **Step 3: Implement.** State (Definition of Done D): a second start over the same store is a no-op for migrations (Decision 34) and `EnsureGeneral` (idempotent); two servers over one Postgres are Milestone 7's case (the lease row serializes maintenance already; wakes need `NOTIFY`); two servers over one SQLite file are unsupported (choice 14) and documented in Task 10's docs; a start after a crash finds the lease row expired after `2 * interval` and the migrations at their committed version.

- [ ] **Step 4: Run the tests and the gate**

Run: `make build && ./agentbus2 server 2>&1 | head -3; make build-all vet fmt-check lint test`, then `make verify` (600000 ms).
Expected: the bare `server` prints the two required-flag messages and exits 2; `verify: OK`.

- [ ] **Step 5: Commit**

```bash
git add main.go main_test.go internal/server/cmd internal/conformance/bus/server_test.go
git commit -m "feat(cli): agentbus2 server over SQLite or Postgres, refusing every authenticated operation until Milestone 5

Ports the subcommand dispatch and flag handling of v1 main.go; starts the
store (Decision 34), general (Decision 92) and the maintenance loop
(Decision 49) before listening; RefuseAll is the only authenticator the
command constructs (Decisions 8, 64); interim flags until Decision 85."
```

---

### Task 9: Watch replaced across two processes (issue #39)

**Depends on:** 4, 8.

**Files:**
- Create: `internal/server/cmd/replaced_test.go` (Docker-free, SQLite; the re-exec pattern)

**Interfaces:**
- Consumes: `cmd.Serve` with `Auth` and `Notifier`; `notify.NewPolling`; the generated client; `sqlite.Open`.
- Produces: the proof for the exit criterion's issue #39 clause.

- [ ] **Step 1: Write the test**

`replaced_test.go`: `TestHelperProcess` (runs only when `AGENTBUS2_HELPER_SERVER=1`: opens the SQLite file named by `AGENTBUS2_HELPER_SQLITE`, `Serve` on `127.0.0.1:0` with `Auth: headerAuth{}` (a test-only `Authenticator` reading `X-Agentbus2-Test-Identity`, defined in this file), `Notifier: notify.NewPolling(100 * time.Millisecond)`, `Ready` printing `listening <addr>` to stdout; exits when stdin closes). `TestWatchReplacedAcrossProcesses` (Review Focus 6: two helpers started with `exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")` over one temp file (the identity `pat` created through the Store before they start); through process A's address: open a session; arm a watch with `timeout=10` in a goroutine; after 300 ms, through process B's address, arm a watch on the same session; A's watch returns 409 `replaced` within 1.5 s of B's registration (the poll interval bounds it, Decision 68); B's watch is still live and returns 204 at its 1 s timeout; `TestHelperProcess` in a process without the variable returns at once). The exit-code reading (3 for a replaced waiter) is Milestone 6's CLI; this test asserts the wire.

- [ ] **Step 2: Run it to verify the failure mode is real**

Run with `Notifier: notify.NewMemory()` temporarily in the helper: `go test -count=1 -run 'TestWatchReplacedAcrossProcesses' -v ./internal/server/cmd/`
Expected: FAIL (A's watch never learns of B's registration before its own timeout: the in-memory notifier does not cross processes). Restore the polling notifier.

- [ ] **Step 3: Run it to verify it passes**

Run: `go test -count=1 -race -run 'TestWatchReplacedAcrossProcesses' -v ./internal/server/cmd/`, then `make verify` (600000 ms).
Expected: PASS within 5 s; `verify: OK`.

- [ ] **Step 4: Commit**

```bash
git add internal/server/cmd/replaced_test.go
git commit -m "test(server): a newer watch replaces an older one across two processes sharing one SQLite file (v1 issue ericfitz/agentbus#39, Decision 68)"
```

---

### Task 10: Integration, milestone exit and docs

**Depends on:** 5, 6, 7, 9.

**Files:**
- Delete: `server.errNotImplemented` in `internal/server/server.go` and `remote.ErrNotImplemented` in `internal/bus/remote/remote.go` (no stub may survive); Modify: `internal/conformance/bus/transport_test.go` (`TestTransportInUse` gains the `ListChannels`-through-`newBus` counter assertion), `Makefile` (`test-conformance` runs the `bus` package under both transports), `scripts/check-gates-selftest.sh` (block 11), `CLAUDE.md`, `README.md`, `docs/adr/0001-transport-and-api.md` and `docs/adr/0007-openapi-code-generator.md` (Milestone 4 addenda), `HANDOFF.md` (untracked); Create: `scripts/m4-exit-checks.sh`

- [ ] **Step 1: No stub survives**

`rg -n 'errNotImplemented|ErrNotImplemented|_stub\.go' internal/` prints nothing; `rg -n 'pending Eric: Milestone 4' internal/` lists exactly the four items.

- [ ] **Step 2: The Makefile and the exit script**

`test-conformance` becomes: `go test -count=1 -p 1 ./internal/conformance/...` then `AGENTBUS2_BUS_TRANSPORT=inproc go test -count=1 -p 1 ./internal/conformance/bus/`, with the comment "the bus suite runs through local.New and through the generated client over the in-process transport (spec 10, Milestone 4 exit)". `scripts/m4-exit-checks.sh` (`#!/usr/bin/env bash`, `set -euo pipefail`; arguments: the local and the `inproc` `-v` suite logs): runs `scripts/m3-exit-counts.sh` on each (so both logs must show `fail=0`, `matrix_sqlite=52`, `matrix_postgres=52`, `search_agree=2`, `concurrent_send=2`); prints `deps_ok=1` after checking with `go list -deps` that `./internal/bus ./internal/core/... ./internal/bus/local` list no `internal/gen` or `internal/server`, `./internal/bus/remote` lists no `internal/server` or `internal/core`, and `./internal/server/...` lists no `internal/bus/local` or `internal/bus/remote` (Decision 47; Global Constraints); exits 1 on any miss, naming the package and the forbidden import.

- [ ] **Step 3: The milestone exit, positive**

Run (600000 ms or in the background): `env -u AGENTBUS2_TEST_POSTGRES_DSN make verify 2>&1 | tee /tmp/agentbus2-m4-verify.log | tail -3`; `env -u AGENTBUS2_TEST_POSTGRES_DSN make test-race 2>&1 | tail -4`; then `env -u AGENTBUS2_TEST_POSTGRES_DSN go test -count=1 -p 1 -v ./internal/conformance/bus/ > /tmp/agentbus2-m4-local.log 2>&1; env -u AGENTBUS2_TEST_POSTGRES_DSN AGENTBUS2_BUS_TRANSPORT=inproc go test -count=1 -p 1 -v ./internal/conformance/bus/ > /tmp/agentbus2-m4-inproc.log 2>&1; bash scripts/m4-exit-checks.sh /tmp/agentbus2-m4-local.log /tmp/agentbus2-m4-inproc.log`.
Expected, quoted verbatim in the report: `verify: OK`; `test-race` `ok` everywhere; for both logs `fail=0`, `matrix_sqlite=52`, `matrix_postgres=52`, `search_agree=2`, `concurrent_send=2`; `deps_ok=1`; exit 0. That is the exit criterion: the core suite passes through the generated client over the in-memory transport on both engines (the `inproc` log), beside the local run (the local log), generation and the validator re-tested on the real 3.1 document (Task 1's tests inside `make test`), the two-process replaced case (Task 9 inside `make test`), and a stale `internal/gen` failing (`generate-check` inside `verify`, proven by self-test blocks 3 and 4).

- [ ] **Step 4: The milestone exit, negative half** (one block of `scripts/check-gates-selftest.sh` after Milestone 3's blocks 9 and 10)

Block 11 `inproc-crosses-the-wire`: in the throwaway worktree replace, in `internal/bus/remote/messages.go`, the one line that passes the send's key (`IdempotencyKey: key(in.IdempotencyKey)`, which the implementer keeps verbatim on one line) with `IdempotencyKey: nil` through a temp file and `cp` (`problem` if `grep -c 'IdempotencyKey: nil'` is 0); run `AGENTBUS2_BUS_TRANSPORT=inproc go test -count=1 -p 1 -v -run '^TestSendIdempotencyReplay$' ./internal/conformance/bus/` and expect `--- FAIL: TestSendIdempotencyReplay/sqlite` and `/postgres` in the log (two rows instead of one replay); run the same with `AGENTBUS2_BUS_TRANSPORT=local` and expect `--- PASS` on both (the mutation is invisible to the local path, which is the proof that the `inproc` run goes through the wire); revert. Run after Step 6's commit: `make check-gates-selftest 2>&1 | tail -16`. Expected: `selftest: ok inproc-crosses-the-wire` and every earlier block `ok`.

- [ ] **Step 5: Docs**

- `CLAUDE.md` Commands: `make test-conformance` runs the bus suite twice (`AGENTBUS2_BUS_TRANSPORT` `local` and `inproc`); `agentbus2 server -listen ... (-sqlite ... | -postgres ...)`. Design: one line each for `api/openapi.yaml` (the wire protocol; read spec 3 and ADR 0001/0007 before changing it; `make generate` after), `internal/server` (handlers over the core; every error through `WriteProblem`; the `Authenticator` seam), `internal/bus/remote` (the client side of Decision 47), and the Decision 47 import rule. Facts only, checked against the tree.
- `README.md`: `Status: Milestone 4 (OpenAPI, server, client, local mode) done; the server refuses every authenticated call until Milestone 5.`
- ADR 0001 addendum `## Milestone 4 addendum (date)`: choices 1 (the operation table), 2, 3, 4, 9, 10, 11, 13 and 15 and pending items 1 and 4 with their test markers; the two-process SQLite limitation of choice 14. ADR 0007 addendum: the 3.1 re-test result on the real document (Task 1's tests and the planning probe), choices 6, 7 and 8, pending item 2, `embedded-spec: true`, the validator as the security enforcement point. ADR 0002 addendum: choice 5 (the `Authenticator` seam, `RefuseAll`, `InProcess` for Decision 36) and pending item 3.
- `HANDOFF.md` via `/session:handoff`: Milestone 4 done at the Step 6 commit, unpushed; the Step 3 and 4 outputs; the pending items; Milestone 5 next.

- [ ] **Step 6: Commit**

```bash
git add internal/server/server.go internal/bus/remote/remote.go Makefile scripts/m4-exit-checks.sh scripts/check-gates-selftest.sh CLAUDE.md README.md docs/adr
git commit -m "feat(server): Milestone 4 exit: the core suite through the generated client over the in-process transport on both engines, the wire-crossing proof, docs"
```

- [ ] **Step 7: Report**

End with the Step 3 outputs (verify, race, both logs' counts, `deps_ok`), the Step 4 self-test tail quoted verbatim, and the pending items still open. No push: the next push is Eric's (`Approve: push main to origin (git push origin main)`).

---

## Self-review notes

- Spec coverage, section 10 Milestone 4: the 3.1 document with both security schemes (Task 1, Decision 65), oapi-codegen (Task 1, Decision 64), handlers (Tasks 2 and 4 to 7), problem+json everywhere (Task 2's hooks and `TestEveryFrameworkErrorIsProblem`, Decision 46, spec 3.3), `Idempotency-Key` (Tasks 1, 3, 5, 6, 7, Decision 66), discovery with computed capabilities and the agreement rule (Task 2, Decisions 43, 65, 91), non-blocking `receive` and the watch with 200/204/409, the cap, merging and `include_own` false (Task 4, Decisions 42, 45, 83; the rules themselves are Milestone 3's), the Decision 68 watch registration (Milestone 3's row, proven across processes in Task 9), the in-process transport (Task 2, Decision 35), the remote `Bus` with generated types behind the interfaces (Tasks 3 to 7, Decision 47, the `go list -deps` check in Task 10), `agentbus2 server` refusing until Milestone 5 (Task 8). Exit criterion: Task 1 (generation and validator on the real 3.1 document), Task 10 Step 3 (the suite through the client over the in-memory transport, both engines), `generate-check` with self-test blocks 3 and 4 (stale `internal/gen`), Task 9 (issue #39 across two processes).
- Decisions traced: 1, 2, 3, 4, 9, 10, 35, 42, 43, 45, 46, 47, 66, 67, 68, 83 (ADR 0001); 36, 84, 85 (ADR 0002); 8, 14, 15, 16, 40, 44 (ADR 0003); 5, 6, 7, 49, 52, 54 (ADR 0004); 31, 32, 33 (ADR 0005); 64, 65, 91 (ADR 0007); 80, 89, 94 (ADR 0008); 55, 70 (the openid-configuration lists).
- Names used across tasks: the `operationId`s and generated names of Task 1; `server.New`, `Options`, `Authenticator`, `RefuseAll`, `InProcess`, `WithIdentity`, `IdentityFrom`, `InProcessTransport`, `Spec`, `StatusFor`, `ProblemFor`, `WriteProblem`, `Capabilities`, `errNotImplemented`, the `env` fixtures (Task 2); `remote.New`, `Bus`, `errorOf`, `key`, `seconds`, `ErrNotImplemented` (Task 3); `transport`, `handlerFor`, `transportEnv` (Task 3); `cmd.Run`, `Serve`, `Config`, `Ready` (Task 8); `scripts/m4-exit-checks.sh` (Task 10); Milestone 3's names in "Core and Bus names assumed".
- Type consistency: `WatchInput.Timeout` is a `time.Duration` on both seams and a `number` of seconds on the wire (Task 3's `seconds`, Task 4's conversion back); `IdempotencyKey *string` on every IK operation's `Params` (Task 1) and `key()` on the client (Task 3); `ACL nil` versus `[]` and `Tags nil` versus `[]` carried by pointer-to-slice members (Tasks 5, 7).
- Review Focus: each line has its test in the owning task (1 and 3 in Task 4, 2 in Task 3, 4 in Task 2, 5 in Task 8, 6 in Task 9).
- Proportion: the plan fixes the operation table, the generated and hand-written names, the error and status mapping, the middleware order, the suite switch and the exit mechanics; handler bodies are the implementer's.
- Checked against the tree on 2026-10-21: `api/openapi.yaml` is the Milestone 1 stub; `internal/gen/gen.go` is 514 lines from it; `go.mod` already carries the `tool` directive, `oapi-codegen/runtime` and, indirectly, `kin-openapi` v0.142.0 and `nethttp-middleware` is absent; `scripts/check-gates.sh` check 4 mutates `^        discover:$`; `scripts/check-gates-selftest.sh` blocks 3 and 4 mutate `internal/gen`; `internal/conformance/bus` holds `TestScaffold` and `TestMain` only (Milestone 3 has not landed), so every Milestone 3 name above is assumed from its plan revision 3.
