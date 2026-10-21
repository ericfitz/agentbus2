# Code-generator spike: oapi-codegen vs ogen

Date: 2026-10-20 (task opened 2026-10-19). Author: Claude (Fable 5.1), for Eric.
Open question from `HANDOFF.md`: which OpenAPI code generator serves decisions 3, 4, 19, 35, 42, 43, 46 and 47.

Everything below was observed by running the spike, not recalled. The rerunnable
files (spec, driver, both test modules) are in
`docs/superpowers/specs/2026-10-19-codegen-spike/`.

## Versions

| What | Command | Output |
|---|---|---|
| Go | `go version` | `go1.27.2 darwin/arm64` |
| oapi-codegen, module proxy | `go list -m -versions github.com/oapi-codegen/oapi-codegen/v2 \| tr ' ' '\n' \| tail -1` | `v2.8.0` |
| oapi-codegen, GitHub | `gh release list --repo oapi-codegen/oapi-codegen --limit 1` | `v2.8.0: OpenAPI 3.1, fewer assumptions, and a giant bug hunt  Latest  2026-07-17` |
| ogen, module proxy | `go list -m -versions github.com/ogen-go/ogen \| tr ' ' '\n' \| tail -1` | `v1.24.0` |
| ogen, GitHub | `gh release list --repo ogen-go/ogen --limit 1` | `v1.24.0  Latest  2026-08-07` |
| Pinned in the spike | `go tool oapi-codegen -version` / `go tool ogen -version` | `v2.8.0` / `ogen version v1.24.0 (built with go1.27.2) darwin/arm64` |
| Generator `go` directives | `go list -m -f '{{.GoVersion}}' <module>` | oapi-codegen `1.25.0`, runtime `1.24.0`, ogen `1.25.0` |
| Validator middleware pulled by `go mod tidy` | `go list -m github.com/oapi-codegen/nethttp-middleware github.com/getkin/kin-openapi` | `v1.2.0`, `v0.142.0` |

Relevant release notes (oapi-codegen v2.8.0, `gh release view v2.8.0 --json body`):
"Go 1.25 is now required"; "Security scopes are no longer emitted by default
(#2440)" with the bridge flag `compatibility.enable-auth-scopes-on-context: true`
and the advice to use the request validation middleware instead; "Initial OpenAPI 3.1
support based on kin-openapi (#2336)"; "Trailing-slash routes on the net/http server
no longer act as catch-alls"; "Handlers are now registered in spec order".
ogen v1.23.0 and v1.24.0 notes list OpenAPI 3.1 `type: [T, "null"]` nullability,
space-delimited query style and validator fixes.

## Run command and result

```
bash /Users/efitz/Projects/agentbus2/docs/superpowers/specs/2026-10-19-codegen-spike/run.sh
```

The driver derives every path from its own location, exports `CGO_ENABLED=0`,
deletes `gen/`, `probes/` and `logs/` under each generator directory, then per
generator: generate with `go tool <generator>` (pinned by the `tool` directive in
each `go.mod`), `go mod tidy`, `go build ./...`, `go vet ./...`,
`go test -count=1 -timeout 120s -v ./...`. The exit status reflects only those ten
steps. It then runs informational probes. Run on 2026-10-20, exit status 0:

```
PASS  oapi-generate   PASS  oapi-tidy   PASS  oapi-build   PASS  oapi-vet   PASS  oapi-test
PASS  ogen-generate   PASS  ogen-tidy   PASS  ogen-build   PASS  ogen-vet   PASS  ogen-test
err   ogen-oidc-no-escape-hatch (exit 1)      <- expected: the openIdConnect hard error
ok    oapi-scopes-flag-generate   ok  oapi-scopes-flag-build
ok    oapi-min-generate           ok  oapi-min-build
ok    oapi-openapi31-generate     ok  oapi-openapi31-build
ok    ogen-openapi31-generate     ok  ogen-openapi31-build
ok    oapi-fresh-regenerate       ok  oapi-fresh-diff
ok    ogen-fresh-regenerate       ok  ogen-fresh-diff
ok    oapi-build-with-probes      ok  ogen-build-with-probes
RESULT: PASS
```

Rerunning writes `oapi/gen/`, `oapi/probes/`, `ogen/gen/`, `ogen/probes/` and
`logs/` next to `run.sh`; they are generated output and should not be committed.
`go mod tidy` and the first generation need network access to the module proxy.
A rerun from the docs location with warm module and build caches took 7 s
(`exit=0 elapsed=7s`, `RESULT: PASS`); a cold cache takes longer.

## The sample spec

`2026-10-19-codegen-spike/openapi.yaml`, OpenAPI **3.0.3**. Four operations:

- `GET /.well-known/agentbus2` (`security: []`) -> 200 `WellKnown`, default `Problem`
- `GET /v1/discover` (`security: []`) -> 200 `Discovery`, default `Problem`
- `GET /v1/sessions/{session}/watch?after=<int64>&timeout=<int, default 30>` -> 200 `Wake{seq,count,channels}`, 204 (no content), 409 `application/problem+json` `Problem`, default `Problem`
- `POST /v1/channels/{channel}/messages` with required header `Idempotency-Key` and JSON body -> 201 `Message`, default `Problem`

Global `security: [{bearerAuth: []}, {oidc: []}]` with `bearerAuth: {type: http, scheme: bearer, bearerFormat: JWT}`
and `oidc: {type: openIdConnect, openIdConnectUrl: ...}`. The shared response
`components.responses.Problem` is `application/problem+json` with schema `Problem`
(`type` (uri), `title`, `status`, `detail`, `instance`, plus extension members
`code` string and `retryable` boolean, all required except `detail`/`instance`).

A `default` response was added to `wellKnown` midway through the spike because
ogen logged `Convenient errors are not available {"reason": "operation has no
"default" response"}`; with it on every operation ogen generates `NewError`
(see need 2).

OpenAPI 3.1: a copy with only `openapi: 3.1.0` changed generated and built on
both. oapi-codegen's 3.1 output was identical to its 3.0 output apart from the
package-name comment line; ogen's was identical (`diff -r` printed nothing).
That is a thin test (the spec uses no 3.1-only idioms), so **3.0.3 stays the
recommendation** until a 3.1 feature is actually needed.

## Comparison by need

### 1. Long-poll `watch`: 200 / bodiless 204 / 409 problem+json; `r.Context()` cancellation

| | oapi-codegen v2.8.0 (`std-http-server` + `strict-server`) | ogen v1.24.0 |
|---|---|---|
| Server return type | `WatchResponseObject` interface; `Watch200JSONResponse` (alias of `Wake`), `Watch204Response struct{}`, `Watch409ApplicationProblemPlusJSONResponse` (alias of `Problem`), `WatchdefaultApplicationProblemPlusJSONResponse{Body Problem; StatusCode int}`. Each has a generated `VisitWatchResponse(w)` that sets status and content type. | `WatchRes` sum interface with members `*Wake`, `*WatchNoContent` (empty struct), `*Problem` (the 409). The default response is **not** a member: it is `*ProblemStatusCode`, which implements `error` (see need 2). |
| Client side | `WatchWithResponse` returns `*WatchResponse{Body, HTTPResponse, JSON200 *Wake, ApplicationproblemJSON409 *Problem, ApplicationproblemJSONDefault *Problem}`; `err == nil` for every status. 204 is status-only: `StatusCode()==204`, all pointers nil, `Body` empty (observed). | `Watch` returns `(WatchRes, error)`; 200 -> `*Wake`, 204 -> `*WatchNoContent`, 409 -> `*Problem`, all with `err == nil` (observed). |
| Wire 409 | `Content-Type: application/problem+json`, status 409 (observed in the client's `HTTPResponse`). | `Content-Type: application/problem+json`, status 409, body `{"type":"https://agentbus2.dev/problems/replaced","title":"Conflict","status":409,"detail":"","code":"replaced","retryable":false}` (observed through `httptest`). |
| Context | The strict wrapper calls `sh.ssi.Watch(r.Context(), ...)` (generated code, `gen.go:1485`). Handler blocked on `<-ctx.Done()`; with a 100 ms client deadline the handler saw `context deadline exceeded` and the client returned `Get ".../watch?after=0": context deadline exceeded` after 102 ms. | `ctx, span := s.cfg.Tracer.Start(r.Context(), ...)` then `s.h.Watch(ctx, params)`. Same test: handler saw `context deadline exceeded`, client returned `do request: Get ...: context deadline exceeded` after 102 ms. |

Both pass. The `"detail":""` in ogen's 409 body is there because the test set
`Detail` to an empty `NewOptString`; the generated encoder writes the field only
under `if s.Detail.Set` (`oas_json_gen.go:481`).

### 2. `default` response as `application/problem+json` with `code` and `retryable`

| | oapi-codegen | ogen |
|---|---|---|
| Content type honored | Yes. `WatchdefaultApplicationProblemPlusJSONResponse.VisitWatchResponse` writes `application/problem+json` and the given `StatusCode` (503 observed). Client fills `ApplicationproblemJSONDefault` (code `store_unavailable`, `retryable: true` observed). The client's decode condition is lenient: `strings.Contains(rsp.Header.Get("Content-Type"), "json") && true` (`ParseWatchResponse`, `gen.go:882`), so any `*json` body on an unlisted status lands in the default field. | Yes. Generated `ProblemStatusCode{StatusCode int; Response Problem}` is encoded with `Content-Type: application/problem+json` and the given status (503 observed). Client returns it as the `error`: `errors.As(err, &ps)` gives `ps.StatusCode == 503`, `ps.Response.Code == "store_unavailable"`. |
| Server-side Go `error` from a handler | Default wiring: `http.Error` -> `500 text/plain "disk on fire"`, client `ApplicationproblemJSONDefault == nil` (observed). With `NewStrictHandlerWithOptions{ResponseErrorHandlerFunc}` writing a problem: client gets typed 500 with `code: internal`, `detail: disk on fire` (observed). | "Convenient errors" (enabled automatically because every op has the same `default`): `Handler` gains `NewError(ctx, err) *ProblemStatusCode`; a plain `errors.New("disk on fire")` came back to the client as a typed 500 `code: internal`; returning `&ProblemStatusCode{StatusCode: 429, ...}` as the error was encoded as 429 unchanged (observed). |
| Framework errors (bad params, auth) | Default: `text/plain` via `http.Error` (observed `400 "Header parameter Idempotency-Key is required, but not found"`, `401 "security requirements failed: ..."`). To make them problem+json you set **three** hooks sharing one helper: `StdHTTPServerOptions.ErrorHandlerFunc` (binder), `StrictHTTPServerOptions.RequestErrorHandlerFunc` and `ResponseErrorHandlerFunc`, plus the validator middleware's `ErrorHandlerWithOpts`. With them, the client received typed 400 `bad_request` and 401 `unauthenticated` (observed). | Default `ogenerrors.DefaultErrorHandler` writes `application/json {"error_message": ...}` (observed `400 ct="application/json"` for the missing header), which the generated client **refuses** to decode: `decode response: default (code 401): unexpected Content-Type: application/json` (observed before convenient errors were on). `gen.WithErrorHandler(...)` using `ogenerrors.ErrorCode(err)` fixes it (one hook). Quirk: with convenient errors on, **SecurityHandler failures go through `NewError`, not the error handler** (generated `oas_handlers_gen.go:471`), while decode-params failures go through `cfg.ErrorHandler` (`:513`); `NewError` must therefore call `ogenerrors.ErrorCode(err)` or every auth failure becomes a 500 (observed before the fix). |
| Schema shape | `Problem{Code string; Detail *string; Instance *string; Retryable bool; Status int; Title string; Type string}`; `format: uri` is a plain string. | `Problem{Type url.URL; Title string; Status int; Detail OptString; Instance OptString; Code string; Retryable bool}`; `format: uri` is `net/url.URL`, which marshaled to the expected string on the wire. ogen also warned `Ensuring "status" matching HTTP status code will not be enforced yet!` for each problem+json response. |

### 3. Two security schemes: `http` bearer and `openIdConnect`

| | oapi-codegen | ogen |
|---|---|---|
| Accepts `openIdConnect` | Yes, silently (generation exit 0, no output). | **Hard error**, exit 1: `Feature "openIdConnect security" is not implemented yet.` followed by `Try to create ogen.yml with: generator: ignore_not_implemented: ["openIdConnect security"]` ... `generation failed`. |
| Escape hatch | n/a | With `ignore_not_implemented: ["openIdConnect security"]` generation succeeds and logs `INFO Skipping security {"at": "openapi.yaml:9:5", "reason_error": "openIdConnect security not implemented"}` for each secured op. The generated server then knows only `bearerAuth`; the oidc requirement is dropped from the generated security evaluation. |
| Generated auth hook | None by default in v2.8.0 (no `security`, `scope` or `bearer` identifiers in `gen.go`). The bridge flag `compatibility.enable-auth-scopes-on-context: true` emits `BearerAuthScopes`/`OidcScopes` context keys and `context.WithValue(ctx, BearerAuthScopes, []string{})` per secured op (observed in the probe); the release notes call this machinery "fundamentally broken" and recommend the validator middleware instead. With `embedded-spec: true` plus `github.com/oapi-codegen/nethttp-middleware`, `openapi3filter.AuthenticationFunc` is called per candidate scheme with `SecurityScheme.Type`; observed call list for one unauthenticated and two authenticated `watch` calls: `[bearerAuth:http oidc:openIdConnect bearerAuth:http oidc:openIdConnect bearerAuth:http]`. The middleware rejects a protected op without a token with 401 before the handler runs, and passes `security: []` ops with no header (observed). | Server: `SecurityHandler` interface with `HandleBearerAuth(ctx, operationName, BearerAuth{Token, Roles}) (context.Context, error)`; `NewServer(h, sec)` requires it. Client: `SecuritySource` interface with `BearerAuth(ctx, operationName) (BearerAuth, error)`; `NewClient(url, sec, ...)` requires it. A rejecting `HandleBearerAuth` produced `401 code=unauthenticated detail="operation Watch: security \"BearerAuth\": bad token"` on the client via `NewError` (observed); a request with no `Authorization` header produced 401 `security requirement is not satisfied` (observed). |

For agentbus2 the practical difference: decision 19 (private_key_jwt -> short-lived
bearer) only *needs* the `http` bearer scheme on the wire; the `openIdConnect`
scheme in the OpenAPI document would exist to advertise issuer discovery. ogen
cannot carry that scheme without the ignore flag, and the flag drops it from the
generated server. oapi-codegen leaves authentication entirely to middleware, which
is where decision 8 (server-side authz at the identity level) puts it anyway.

### 4. `Idempotency-Key` header on a mutating POST

| | oapi-codegen | ogen |
|---|---|---|
| Server | `SendMessageParams{IdempotencyKey string \`json:"Idempotency-Key"\`}` bound by the generated wrapper; handler saw `idem-123` (observed). Missing header: `400 "Header parameter Idempotency-Key is required, but not found"` before the handler. | `SendMessageParams{Channel string; IdempotencyKey string}`; handler saw `idem-123`. Missing header: `400 {"error_message":"operation SendMessage: decode params: header: \"Idempotency-Key\": invalid: Idempotency-Key (field required)"}` (default handler) before the handler. |
| Client | `SendMessageWithResponse(ctx, channel, &SendMessageParams{IdempotencyKey: ...}, body)`. | `SendMessage(ctx, &NewMessage{...}, SendMessageParams{Channel, IdempotencyKey})`. |

Both pass.

### 5. `/.well-known/agentbus2` (dot in a segment) and unauthenticated `GET /v1/discover`

| | oapi-codegen | ogen |
|---|---|---|
| Dot path | Registered verbatim as a Go 1.22 pattern: `m.HandleFunc(http.MethodGet+" "+options.BaseURL+"/.well-known/agentbus2", ...)` (`gen.go:1162`); round trip returned `discover: /v1/discover` (observed). | Generated radix router matches the literal `".well-known/agentbus2"` prefix (`oas_router_gen.go:73`); round trip returned the same (observed). |
| `security: []` | Nothing generated per op; the validator middleware let `discover` through with no `Authorization` header (observed). | Generated handlers for `discover` and `wellKnown` contain no `securityBearerAuth` call; the test's `SecurityHandler` recorded zero calls for them (observed). |

Both pass.

### 6. Decision 35: generated client over an in-memory `http.RoundTripper`

Both tests use the same 10-line transport: `RoundTrip` runs `h.ServeHTTP(httptest.NewRecorder(), req)` and returns `rec.Result()` (returning `req.Context().Err()` when the context was canceled, to mirror `net/http`). No listener, no goroutine.

| | oapi-codegen | ogen |
|---|---|---|
| Injection point | `NewClientWithResponses("http://inproc", gen.WithHTTPClient(doer))` where `HttpRequestDoer` is `interface{ Do(*http.Request) (*http.Response, error) }`; `*http.Client{Transport: inproc{h}}` satisfies it. | `NewClient("http://inproc", securitySource, gen.WithClient(c))` where `ht.Client` is `interface{ Do(*http.Request) (*http.Response, error) }` (`ogen/http/http.go:17`); same `*http.Client`. The `SecuritySource` argument is mandatory even when the op has no security. |
| Proven round trips | 200, 204, 409, default-503, cancellation, POST with header, both unauthenticated ops: all observed in `oapi-test.log`. | Same set, observed in `ogen-test.log`. |

Both are clean. ogen's generated client and server call OpenTelemetry tracer and
meter hooks on every request; when no `TracerProvider`/`MeterProvider` option is
given, `oas_cfg_gen.go:40-44` falls back to `otel.GetTracerProvider()` and
`otel.GetMeterProvider()`. oapi-codegen's client is plain `net/http`.

### 7. Server on plain `net/http`

| | oapi-codegen | ogen |
|---|---|---|
| Router | `std-http-server` target. `HandlerFromMux(si, m ServeMux)` / `HandlerWithOptions(si, StdHTTPServerOptions{BaseURL, BaseRouter, Middlewares, ErrorHandlerFunc})` register **Go 1.22+ `ServeMux` patterns** (`"GET /v1/sessions/{session}/watch"`, `"POST /v1/channels/{channel}/messages"`, observed at `gen.go:1162-1165`) on a `*http.ServeMux` you pass in. No third-party router. | ogen generates its own router: `*gen.Server` implements `http.Handler` (`oas_router_gen.go:37`) with a generated radix matcher; `WithPathPrefix` exists for mounting. It does not use `ServeMux` patterns, but can be mounted on a `ServeMux` like any handler. No third-party router. |

Both pass. Both generated servers are plain `http.Handler`s and can be served by
`http.Server` or called in-process.

### 8. Pinning and CI freshness

| | oapi-codegen | ogen |
|---|---|---|
| `go.mod` `tool` directive | `go get -tool github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0` added `tool github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen` plus 17 `// indirect` requires; `go tool oapi-codegen -config cfg.yaml openapi.yaml` works. | `go get -tool github.com/ogen-go/ogen/cmd/ogen@v1.24.0` added `tool github.com/ogen-go/ogen/cmd/ogen`; `go tool ogen -config ogen.yml -target gen -package gen -clean openapi.yaml` works. ogen auto-loads `./ogen.yml` from the **current directory** even without `-config` (observed: the probe only failed once run from a directory without one). |
| Determinism | Regenerate then `diff -r` against the previous output: no differences (probe `oapi-fresh-diff` exit 0). | Same (`ogen-fresh-diff` exit 0). |
| Module-graph cost | `go list -m all` in the spike module: 127 modules (includes the generator's graph and the test's middleware). | 51 modules. |

Proposed CI freshness check (both generators):

```
go tool oapi-codegen -config api/oapi-codegen.yaml api/openapi.yaml   # or: go tool ogen -config api/ogen.yml -target internal/gen -package gen -clean api/openapi.yaml
git diff --exit-code -- internal/gen
```

The `tool` directive drags the generator's own module graph into the project's
`go.mod` (as `// indirect` requires); if that is unwelcome, keep the directive in a
separate `tools/go.mod` and run `go tool -modfile=tools/go.mod ...` (not tested in
this spike).

### Size, dependencies, decision 47, CGO

| | oapi-codegen | ogen |
|---|---|---|
| Generated size | 1,642 lines in one file (`embedded-spec: true`); 1,523 lines without the embedded spec. | 4,648 lines in 19 files. |
| Non-std packages linked into a binary importing the generated package (`go list -deps`) | 5 without embedded spec: `github.com/oapi-codegen/runtime`, `github.com/google/uuid`, `github.com/apapsch/go-jsonmerge/v2`. 24 with the embedded spec (adds `kin-openapi`, `go-openapi/jsonpointer`, `oasdiff/yaml`, `oasdiff/yaml3`, `santhosh-tekuri/jsonschema/v6`, `golang.org/x/text`). The validator middleware is a test dependency here but would be a server dependency. | 79 packages from 27 modules, including `go.opentelemetry.io/otel` (+ `metric`, `trace`, `auto/sdk`), `go.uber.org/zap`, `go.uber.org/multierr`, `github.com/go-faster/jx`, `github.com/go-faster/errors`, `github.com/go-faster/yaml`, `github.com/dlclark/regexp2`, `github.com/shopspring/decimal`, `github.com/segmentio/asm`, `github.com/fatih/color`, `golang.org/x/exp`, `golang.org/x/net`. |
| CGO | `go list -deps -f '{{if .CgoFiles}}...'`: 0 cgo packages; whole run built with `CGO_ENABLED=0`. | Same: 0 cgo packages, builds under `CGO_ENABLED=0`. |
| Type shapes behind the `Bus` interfaces (decision 47) | Optional fields are pointers (`Detail *string`, `WatchParams.Timeout *int`), `format: uri` is `string`, `format: int64` is `int64`; response objects are plain structs/aliases with a `Visit*` method. Mapping generated types to domain types is pointer-to-value copying. | Optional fields are generated wrappers (`OptString`, `OptInt` with `NewOptString`), `format: uri` is `url.URL`; responses are sum-type interfaces that need a type switch. Mapping is more explicit and more verbose. Both keep the generated package isolated; neither leaks into MCP tool schemas, which stay hand-written. |

## Recommendation (Claude); accepted by Eric on 2026-10-20 (ADR 0007)

**oapi-codegen v2.8.0** (`std-http-server` + `strict-server` + `client` + `models`,
auth in hand-written `net/http` middleware).

1. Need 3 is the only hard failure in the matrix and it is ogen's: `openIdConnect`
   is "not implemented yet", and the escape hatch drops the scheme from the
   generated server. oapi-codegen accepts both schemes and, since v2.8.0,
   deliberately generates no auth code, which matches decision 8 (the server
   enforces authz at the identity level, by hand).
2. Plain `net/http` (the spike criterion in HANDOFF's open questions): oapi-codegen registers Go 1.22 `ServeMux`
   patterns on a mux you own; ogen brings its own router. Both work, but the
   oapi-codegen server is the one that stays readable next to hand-written
   handlers.
3. Footprint matches Eric's "simple, clean code" goal: 1.5 k generated lines and
   5 non-std packages versus 4.6 k lines and 79 packages including OpenTelemetry
   and zap in every binary (the MCP server and CLI included).
4. ogen's real advantages are sum-typed responses and one error hook, but its
   convenient-errors mode routes security failures through `NewError` and decode
   failures through the error handler, and its default error handler emits a
   content type its own client rejects; those are the kind of surprises that cost
   time later.
5. The cost on the oapi-codegen side is explicit and bounded: three error hooks
   (plus one in the validator middleware if used) sharing a single `writeProblem`
   helper so every framework error is RFC 9457, and a lenient `Contains(..., "json")`
   content-type match on the client. Both are visible in the spike test.

If Eric prefers ogen, the spec must omit the `openIdConnect` scheme (or carry
`ignore_not_implemented`) and the implementation needs `WithErrorHandler` plus a
`NewError` that calls `ogenerrors.ErrorCode`.

## How to rerun

```
bash /Users/efitz/Projects/agentbus2/docs/superpowers/specs/2026-10-19-codegen-spike/run.sh
```

Committed files (Eric, 2026-10-20: the spec, the driver and the test sources
only): `openapi.yaml`, `run.sh`, `oapi/{go.mod,go.sum,cfg.yaml,spike_test.go}`,
`ogen/{go.mod,go.sum,ogen.yml,spike_test.go}`. The script cleans and regenerates
`gen/`, `probes/` and `logs/` beside those files; logs hold every step's full
output. Those and `last-run-summary.log` are ignored by git. The original run happened in the session scratchpad at
`/private/tmp/claude-501/-Users-efitz-Projects-agentbus2/44a0f7e6-0f7e-4955-8e57-9a08d4697d00/scratchpad/spike/` with identical files.

## Memory candidates

- ogen v1.24.0 fails hard on `openIdConnect` security schemes (`Feature "openIdConnect security" is not implemented yet.`); `generator.ignore_not_implemented: ["openIdConnect security"]` drops the scheme from generated code rather than modeling it.
- ogen auto-loads `./ogen.yml` from the current directory even without `-config`; probes meant to run without a config must run from a directory that has none.
- ogen "convenient errors" (every op shares one `default` response) make the default response a Go `error` (`*XStatusCode` implements `error`) on both server and client, add `NewError` to the `Handler` interface, and route `SecurityHandler` failures through `NewError` while parameter-decode failures still go through `WithErrorHandler`; `NewError` must use `ogenerrors.ErrorCode(err)`.
- ogen's `ogenerrors.DefaultErrorHandler` writes `application/json {"error_message":...}`; a spec whose `default` is `application/problem+json` makes the generated client fail with `unexpected Content-Type: application/json` on any framework error unless the server sets `WithErrorHandler`.
- oapi-codegen v2.8.0 requires Go 1.25, emits no security scopes or context keys by default (`compatibility.enable-auth-scopes-on-context: true` brings the deprecated ones back), and its client decodes any `*json` content type on an unlisted status into the `ApplicationproblemJSONDefault` field.
- oapi-codegen `strict-server` on `std-http-server` needs three error hooks to make framework errors problem+json: `StdHTTPServerOptions.ErrorHandlerFunc`, `StrictHTTPServerOptions.RequestErrorHandlerFunc`, `StrictHTTPServerOptions.ResponseErrorHandlerFunc` (and `nethttp-middleware` `Options.ErrorHandlerWithOpts` when the validator is used).
- A `go.mod` `tool` directive pulls the generator's full module graph into `go list -m all` (127 modules for oapi-codegen, 51 for ogen) while the binary links only the runtime (5 vs 79 non-std packages); measure both numbers separately.
- In Go tests that call a generated server in-process, never hold a test mutex across a client call whose server hook locks the same mutex; the `go test` default 10-minute timeout turns that into a very slow failure. Pass `-timeout` explicitly in spike scripts.
