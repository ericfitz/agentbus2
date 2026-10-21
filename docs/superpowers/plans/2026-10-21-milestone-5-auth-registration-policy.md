# Milestone 5: Authentication, Registration, Policy and ACL Enforcement Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Real authentication on the Milestone 4 server: Ed25519 and ES256 key pairs (Decision 55), RFC 7523 `private_key_jwt` client assertions with `iss` = `sub` = identity and `aud` = the token endpoint (Decisions 19, 70), `POST /v1/auth/token` minting short-lived bearer JWTs with the server as issuer (lifetime a setting, default one hour; Decisions 57, 70, 84), `GET /v1/auth/jwks` and validation of every call's bearer (own issuer and configured external issuers with JWKS and `iss`+`sub` mapping, Decision 57), `POST /v1/auth/register` with trust-on-first-use, enrollment tokens, listener classification and the `-registration=open|enrollment|closed` override with `enrollment` as the non-loopback default (Decisions 20, 58, 72), the default policy for every identity including the `general` subscription (Decisions 59, 77, 92), the default channel ACL, creator admin and the open-creation switch enforced end to end (Decisions 80, 88, 93), `agentbus2 server admin ...` host commands against the store without a credential (Decisions 58, 73), the admin API section requiring the admin flag with reset and reset-credential (Decision 48), and server configuration from a file given by a flag with `AGENTBUS2_SERVER_*` overriding it and flags overriding both (Decision 85, the settings of spec 6.5). Local mode keeps no authentication (Decision 36). Exit (spec 10): an end-to-end test on both engines registers, authenticates, is refused on a non-loopback listener without a token, is refused admin operations as a non-admin, and is refused `read` and `write` on a channel whose ACL omits it while the creator and an administrator succeed, under `make verify`.

**Architecture:** `internal/auth` is a leaf (standard library plus go-jose only, never `internal/store`, `internal/core`, `internal/server` or `internal/gen`): `keys` (key pairs, PKCS#8 PEM, PKIX DER, JWK, RFC 7638 thumbprints as `kid`), `assertion` (signing and verifying client assertions) and `token` (the server's issuer, bearer verification, the JWKS cache for external issuers); Milestone 6's client links `keys` and `assertion` unchanged. `internal/core/identities` is the one place the registration, enrollment, admin-flag, reset-credential and admin-reset rules live, over the Store (Decision 5); the HTTP handlers and the `server admin` host commands both call it. The Store gains enrollment-token rows (a version 2 migration on both engines, as the Milestone 2 plan reserved) and a `ResetData` method. `internal/server` gains the `Bearer` authenticator behind Milestone 4's `Authenticator` seam, three handlers (`token`, `jwks`, `register`) and two admin handlers (`adminReset`, `resetCredential`), with `openid-configuration` and `discover` reading the configured issuer. `internal/server/config` loads Decision 85's file, environment and flags and maps them onto `core.Config`. `internal/server/cmd` classifies the listener, builds the issuer and the signing key, wires `Bearer`, and hosts `agentbus2 server admin`.

**Tech Stack:** Go 1.27, `CGO_ENABLED=0`; `github.com/go-jose/go-jose/v4` v4.1.6 (chosen below); `crypto/ed25519`, `crypto/ecdsa`, `crypto/x509`, `encoding/pem` from the standard library; the Milestone 2 Store, the Milestone 3 core and `Bus`, the Milestone 4 document, generated code, server, remote `Bus` and command. No other new module.

**Spec:** `docs/superpowers/specs/2026-10-19-agentbus2-design.md` sections 1.1, 2.2 to 2.4, 3.1 to 3.4, 4.1, 4.2, 4.6, 4.7, 5.6, 5.8, 6.1, 6.3, 6.5, 8, 9 and 10 (Milestone 5); ADR 0002 (Decisions 19, 20, 22, 36, 55, 57, 58, 70, 72, 73, 84, 85), ADR 0003 (Decisions 8, 14, 59, 73, 77, 86), ADR 0004 (Decisions 26, 34, 48, 53, 54), ADR 0007 (Decisions 64, 65, 91), ADR 0008 (Decisions 80, 88, 89, 92, 93, 96); the Milestone 2 plan (`2026-10-20-milestone-2-store-engines.md`, its Store names and choice 11's "enrollment tokens are Milestone 5 rows, a version 2 migration"), the Milestone 3 plan (`2026-10-21-milestone-3-core-bus.md`, every core and `Bus` name below) and the Milestone 4 plan (`2026-10-21-milestone-4-openapi-server-client-local.md` revision 3, every `server`, `remote`, `cmd` and generated name below).

**Evidence gathered while planning (2026-10-21):** (1) oapi-codegen v2.8.0 (the pinned `go tool`) on a 3.1.0 probe with `POST /v1/auth/token` taking `application/x-www-form-urlencoded` generated `TokenFormdataRequestBody`, `TokenRequestObject{Body *TokenFormdataRequestBody}`, a server side that calls `r.ParseForm()` then `runtime.BindForm`, and `ClientWithResponses.TokenWithFormdataBodyWithResponse`; through nethttp-middleware v1.2.0 over kin-openapi v0.142.0 with a read-ahead body (Milestone 4 choice 11's shape) the generated client round-tripped 200, a missing required field and a value outside an `enum` were 400 from the validator before the handler, and a chunked body (`ContentLength -1`) reached the handler bound. A schema named `TokenResponse` collided with the client wrapper type oapi-codegen generates for operationId `token` (`TokenResponse redeclared`), so the response schema is `AccessToken` and no schema may take an `<OperationId>Response` name. (2) go-jose v4.1.6 (`go.mod`: `go 1.24.0`, no requirements): `jose.NewSigner(jose.SigningKey{Algorithm: jose.EdDSA | jose.ES256, Key: signer}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", kid))`, `jwt.Signed(signer).Claims(jwt.Claims{...}).Serialize()`, `jwt.ParseSigned(raw, []jose.SignatureAlgorithm{jose.EdDSA, jose.ES256})` refusing `HS256` and `none` with `unexpected signature algorithm`, `tok.Headers[0].KeyID` and `.Algorithm`, `tok.UnsafeClaimsWithoutVerification`, `tok.Claims(pub, &claims)`, `claims.ValidateWithLeeway(jwt.Expected{Issuer, Subject, AnyAudience, Time}, leeway)`, `jose.JSONWebKey{Key, KeyID, Algorithm, Use}.Thumbprint(crypto.SHA256)` for both key types, `jose.JSONWebKeySet.Key(kid)` after a JSON round trip, `JSONWebKey.IsPublic()` false for a JWK carrying private material, and `x509.MarshalPKCS8PrivateKey`/`ParsePKCS8PrivateKey` for Ed25519 and P-256. The probe lives in the session scratchpad (`formprobe/`), not in the repository. (3) The open tests on `main` pin the schema version literally (`internal/conformance/store/open_test.go` `want 1`, `UPDATE schema_meta SET schema_version = 2`, `openStep(2, ...)`; `internal/store/sqlite/open_test.go` likewise; `internal/store/postgres/migrations_test.go` compares the two engines' version lists), so the version 2 migration must land on both engines in one commit with those tests rewritten against `len(Migrations())`. (4) The working tree carried uncommitted files from a concurrent Milestone 2 agent (`internal/store/sqlstore/text.go` and friends) while this plan was written; the plan commit touches its own file only.

## Task dependencies and parallelism

Tasks with no dependency between them run in parallel in separate worktrees and are cherry-picked onto `main`; every task's `Files:` list is disjoint from every task it can run beside. Shared-file owners: `go.mod` and `go.sum` belong to Task 2 alone (go-jose); `api/openapi.yaml`, `internal/gen` and `internal/bus/admin.go` to Task 3 alone; the schema files and `migrations.go` of both engines to Task 1 alone; `main.go`, `main_test.go` and `internal/server/cmd` to Task 8 (Task 3 touches `internal/server/cmd/cmd_test.go` first, in Wave 1, and Task 8 depends on it); `Makefile`, `scripts/check-gates-selftest.sh`, `CLAUDE.md`, `README.md` and the ADRs to Task 9. A task that needs a schema or interface change outside its files reports it to the integrator, who makes it in a serial commit (the Milestone 3 and 4 rule).

**External gate:** Tasks 1, 2 and 4 import nothing from Milestone 4 (Task 1 the Store, Task 2 nothing of this module, Task 4 `internal/core` and `internal/store` only) and may start as soon as Milestone 3 is on `main`; Task 3 and every later task need Milestone 4 Task 11 on `main` (`make verify` green, `internal/gen/openapi31_test.go`, `internal/server`, `internal/bus/remote` and `internal/server/cmd` present with no stub). Before Wave 2 the integrator diffs every name in "Names assumed from Milestones 3 and 4" against `main` and reports drift.

**Gate 0** (before Wave 1): the items of "Claude proposal, pending Eric" go to Eric in one message with options, the recommendation (A) and the test that encodes each (`// pending Eric: Milestone 5 item N`); every wave proceeds on (A) with the markers in place (`rg -n 'pending Eric: Milestone 5' internal/`), and Task 9 records the answers in the ADR addenda. Items 1 (admin reset keeps identities and credentials) and 4 (reset-credential does not revoke minted tokens) are the two that read the frozen text one way where another is possible, so the message names them first.

| Task | Depends on | Wave |
| --- | --- | --- |
| 1 Store: enrollment tokens and data reset (version 2 migration, both engines) | none | 1 |
| 2 `internal/auth`: keys, client assertions, issuer, bearer verification, external JWKS | none | 1 |
| 3 OpenAPI: token, jwks, register, admin reset and reset-credential; `bus.Admin` extension; stubs | none (external gate) | 1 |
| 4 Server configuration (Decision 85) | none | 1 |
| 5 Core identities service: register, enrollment, default policy, admin flag, reset-credential, reset | 1, 2 | 2 |
| 6 Server auth: `Bearer`, token, jwks, register, discovery issuers | 3, 5 | 3 |
| 7 Admin reset and reset-credential on the wire; local and remote `Admin`; remote register and token | 3, 5 | 3 |
| 8 Listener classification, `agentbus2 server` with Decision 85, `agentbus2 server admin`, `Serve` wiring | 4, 6, 7 | 4 |
| 9 Milestone exit: end-to-end on both engines, negative proofs, docs | 8 | 5 |

Waves: 1 (Tasks 1, 2, 3 and 4 in parallel; four disjoint trees: the Store, `internal/auth` with `go.mod`, the document with `internal/gen`, `internal/bus` and the stubs, and `internal/server/config`); 2 (Task 5); 3 (Tasks 6 and 7 in parallel: `internal/server/auth_*.go` and `discovery*.go` against `internal/server/admin*.go`, `internal/bus/local`, `internal/bus/remote`); 4 (Task 8); 5 (Task 9). Nine tasks; the critical path is 1 or 2, 5, 6 or 7, 8, 9.

## Global Constraints

- Module `github.com/ericfitz/agentbus2`, `go 1.27`, `CGO_ENABLED=0`; every method takes a `context.Context` (Decision 6). Work lands on `main`, nothing is pushed, Conventional Commits with the session's attribution trailer; each task commits only its own files, listed by path in its commit command.
- Import rule (Decision 47 extended): `internal/auth/...` imports only the standard library and `github.com/go-jose/go-jose/v4` (`internal/auth/keys` and `internal/auth/assertion` ship in Milestone 6's client binaries); `internal/bus/remote` may import `internal/auth/keys` and never `internal/auth/token`; `internal/core/...`, `internal/bus` and `internal/bus/local` never import `internal/server`, `internal/gen` or `internal/gen/spec`. Task 9's `scripts/m5-exit-checks.sh` checks these with `go list -deps` beside Milestone 4's `scripts/m4-exit-checks.sh`.
- The server stores public keys only (Decision 57): `identity_keys.public_key` is PKIX DER; the only private key the server holds is its token signing key (choice 6), never in the Store. Enrollment tokens are stored as SHA-256 hex (choice 9), never in clear.
- Every JWT the server parses goes through `jwt.ParseSigned` with the explicit allowlist `[EdDSA, ES256]` (external issuers: their configured list, default `[EdDSA, ES256, RS256]`); `none`, `HS256` and any other algorithm are refused before any key is looked up (Task 2's `TestParseRefusesOtherAlgorithms`). Claims are judged on the injected clock (`core.Now()`, Decision 49) with `token.Leeway` 60 s (choice 5).
- Unrecognized inputs are rejected with a test: an unknown `-registration` value, an unknown `AGENTBUS2_SERVER_*` name, an unknown JSON key in the server configuration file, an unknown `-set` name, an unknown `grant_type` or `client_assertion_type` (the validator's enum), a JWK of an unsupported key type or curve or carrying private material, a `kid` that names no stored key, an assertion whose `iss` differs from `sub`, an `aud` that is not the token endpoint, an `exp` more than `assertion.MaxLifetime` plus leeway ahead, a replayed `jti`, an enrollment token that is unknown, expired, exhausted or bound to another name, a `server admin` subcommand that does not exist.
- Every error on the wire stays `application/problem+json` through `server.WriteProblem` (spec 3.3); the token endpoint's errors are problems with `code` `validation` or `unauthenticated`, not RFC 6749 `{"error": ...}` bodies (choice 3). The validator's 400 for a malformed form body carries `validation`, as in Milestone 4.
- The Milestone 1, 3 and 4 gate checks stay in force: no `.Skip(` under `internal/conformance` or `internal/dbtest`; `make test` is Docker-free (unit tests of `internal/auth`, `internal/core/identities`, `internal/server`, `internal/server/config`, `internal/server/cmd` and `internal/bus/remote` open SQLite on a temp path when they need a store); both-engine coverage lives in `internal/conformance/store` and `internal/conformance/bus`; `scripts/check-gates.sh` check 4's `^        discover:$` line stays unique in `api/openapi.yaml`.
- Decision 26 in every core write: identifiers, tokens and hashes are generated before `Store.Tx`; `fn` has no side effects; `EnsureGeneral` runs in its own transaction before `Register`'s.
- Definition of Done D applies to `Register` (a second register with the same key, two concurrent registers of one name, a register after a crashed one), `CreateEnrollmentToken`, `Reset`, `keys.LoadOrCreateFile` (a second start, two servers over one file, a crash during creation) and the version 2 migration (Milestone 2's rules).

## Choices this milestone makes (delegated by the spec, recorded with rationale)

1. **JOSE library: `github.com/go-jose/go-jose/v4` v4.1.6.** Candidates on 2026-10-21 (proxy `@latest`): go-jose v4.1.6 (2026-10-14), `github.com/golang-jwt/jwt/v5` v5.3.1 (2026-01-28), `github.com/lestrrat-go/jwx/v3` v3.3.0 (2026-09-08). go-jose: zero module dependencies (its `go.mod` has none; `golang.org/x/crypto` is already in this module's graph anyway), one package for JWS signing and verification, JWK and JWKS (OKP, EC and RSA, with RFC 7638 thumbprints), JWT claims with `Expected` validation and leeway, and a `ParseSigned` that refuses every algorithm not in an explicit allowlist (the alg-confusion guard is the API's shape, not a caller discipline); maintained under the go-jose organization and depended on by `coreos/go-oidc`. golang-jwt is also zero-dependency and widely used but has no JWK or JWKS, so the JWKS endpoint, external issuer keys and the register body would need hand-written JWK parsing (the part of JOSE where mistakes hide); jwx covers everything but brings `httprc`, `blackmagic`, `option` and friends into Milestone 6's client binaries. Evidence (2) above is the planning probe of every call this plan names.
2. **Wire shape of the token endpoint** (spec 3.2 fixes the path only). RFC 7523 section 2.2 and RFC 6749: `POST /v1/auth/token` takes `application/x-www-form-urlencoded` with `grant_type=client_credentials`, `client_assertion_type=urn:ietf:params:oauth:client-assertion-type:jwt-bearer` and `client_assertion=<JWT>`, and answers `200 application/json {"access_token", "token_type": "Bearer", "expires_in"}` with `Cache-Control: no-store`. Evidence (1) proves the generated stack handles the form body. Rationale: a standard token endpoint means a `credential_process` returning an access token (Decision 56) or a stock OAuth client can talk to it; the only cost is one form-bound operation.
3. **Token endpoint errors are problems** (Decision 46 says every error; spec 3.3 says every framework response too): a malformed or non-parsable assertion is 400 `validation`; an unknown identity or `kid`, a bad signature, a wrong `aud`, an expired or over-long assertion or a replayed `jti` is 401 `unauthenticated` with a `detail` that names the check (never which key or identity exists beyond what the caller sent); a server without an issuer (local mode) is 503 `unavailable`. RFC 6749's `{"error": "invalid_client"}` body is not emitted; the ADR 0002 addendum records this departure from the OAuth error format as the price of Decision 46.
4. **Client assertion rules** (RFC 7523 section 3, Decision 70): header `alg` in `[EdDSA, ES256]`, `typ` `JWT`, `kid` = the key's RFC 7638 thumbprint; claims `iss` = `sub` = identity (both required and equal), `aud` contains the token endpoint URL (`<issuer>/v1/auth/token`, a string compare), `jti` required (16 random bytes base64url), `iat` present, `exp` required, at most `assertion.MaxLifetime` 5 min after `now` plus leeway, `nbf` honored when present. The server keeps a bounded in-memory `jti` set per process until each `exp` (`token.Replay`): a replay is 401. The signing side (`assertion.Sign`) produces exactly this; Milestone 6's client and `credential_process` glue call it.
5. **Bearer token rules** (Decisions 57, 70, 84): header `alg` of the signing key, `kid` its thumbprint; claims `iss` = `aud` = the issuer URL (choice 7), `sub` = identity, `iat`, `exp` = `iat` + `token_lifetime_seconds` (default 3600), `jti`. Verification: `kid` must equal the issuer's current `kid`; `Expected{Issuer, AnyAudience: {issuer}}`; `token.Leeway` 60 s on `exp`, `nbf` and `iat` (standard JWT practice; Decision 49's "few seconds" is the lease rule and is unrelated). A verified `sub` is attached as the request identity; the core's `RequireIdentity` still refuses a name with no row (`not_registered`), so a token for a deleted identity is 403, not 401.
6. **Server signing key: a PKCS#8 PEM file named by the setting `signing_key_file`, mode 0600, generated (Ed25519) when absent; when the setting is empty, an ephemeral Ed25519 key per process** with a warning on stdout at start ("tokens will not survive a restart"). Rationale: the key must survive restarts in any real deployment and be shared by Milestone 7's replicas, which a file on a shared volume or secret gives without putting a private key in the Store (Decision 57's spirit); ephemeral keys keep `agentbus2 server -listen 127.0.0.1:0 -sqlite x` and every test free of files, and a client that holds a stale token simply re-authenticates on 401 (Milestone 6). Windows owner-only ACLs are Milestone 6's credential-file helper; until then the file is created with `os.OpenFile(..., 0o600)` and renamed into place (Decision 22). Pending item 2 asks Eric to confirm the file over a Store row.
7. **One canonical issuer URL.** Setting `issuer` (spec 6.5 does not list it; it is the value Decisions 70 and 91 both need): the public base URL, no trailing slash. It is the bearer `iss` and `aud`, the token endpoint is `<issuer>/v1/auth/token` (the assertion `aud`), the JWKS URI `<issuer>/v1/auth/jwks`, and `/.well-known/openid-configuration` and `discover` return it. Default when unset: `http://<bound address>` as `Serve` sees it after `net.Listen` (`127.0.0.1:0` becomes the real port), which is what tests and a loopback home lab use; a deployment behind TLS termination or a hostname sets it. Milestone 4's per-request `baseURL(r)` is replaced for these fields; a request whose `Host` differs from the issuer still authenticates (the issuer is a string, not the request). Pending item 3.
8. **Registration semantics** (Decisions 20, 58, 72). `POST /v1/auth/register` is `security: []` and takes `{identity, public_key_jwk, enrollment_token?}`. Mode `open`: the name must have no credential (TOFU); `enrollment`: a valid token is required and consumed in the same transaction; `closed`: refused. A token supplied in `open` mode is validated and consumed too (never ignored, so a misconfigured client notices). Re-registering the same key (`kid` equal) is success and idempotent (a client that lost its config but kept its key); a different key for a name with a credential is 409 `conflict`; a lost key is recovered only by `reset-credential` (Decision 58) followed by registration. Registration binds one key per identity in 2.0.0 (the Store's upsert by `(identity, kid)` supports more; nothing decided rotation). Registration creates the identity row when missing and subscribes it to `general` (Decisions 59, 77, 92), calling `channels.Service.EnsureGeneral` first so the policy holds before the server's first serve (`server admin` paths). The public key travels as a JWK, not PEM, because JWKS already speaks JWK and go-jose marshals both key types; the client converts its PEM key with `keys.ToJWK`.
9. **Enrollment tokens** (Decision 20): `abe_` + 32 random bytes base64url, shown once by `server admin enrollment create`, stored as SHA-256 hex with an 8-byte random hex `id` (what `list` and `revoke` name), an optional bound `identity` (empty: any free name), `expires_at` (default 24 h) and `uses_left` (default 1). Consumption decrements `uses_left` in the register transaction; an exhausted or expired token stays as a row until revoked (`list` shows the state); no maintenance job prunes them in this milestone.
10. **Registration mode is decided once at start**, in `cmd` (`-registration` setting, environment or flag; empty means classify): `ClassifyListen` judges the `listen` value before binding (a `unix:` prefix, a loopback IP including `::ffff:127.0.0.1`, or a hostname every address of which is loopback is local; an empty host, `0.0.0.0`, `::` or any other address is not), local gives `open`, anything else `enrollment` (Decision 72). The mode is passed to `server.Options.Registration` and printed on the `listening on` line. A `unix:<path>` listener is added to `Serve` for Decision 58 (`net.Listen("unix", path)`, the stale socket file removed when nothing answers on it).
11. **External issuers** (Decision 57) are configuration (Decision 85), not rows: `issuers: [{issuer, jwks_uri, audience, algorithms?, subjects}]`, where `subjects` maps `sub` values to identity names. A bearer whose `iss` is a configured issuer is verified against that JWKS (fetched lazily, cached `jwks_refresh_seconds` 300, refetched on an unknown `kid` at most once a minute), its `aud` must contain `audience`, and the mapped identity must already exist (`not_registered` otherwise); a `sub` without a mapping is 401. Identities for external subjects are created by `server admin create-identity` (pending item 5) or by an ordinary registration.
12. **Admin reset** (spec 3.2's "Reset", v1 `internal/bus/admin.go` `Reset`): `POST /v1/admin/reset` (admin flag) deletes every message, memory, channel, subscription, tag subscription, session, cursor, receipt and notice, keeps `schema_meta`, `seq_counter`, the maintenance lease, every identity, its keys and admin flag and the enrollment tokens, then recreates `general` with the configured default ACL and subscribes every identity to it in the same transaction. Rationale: on a networked server identities and credentials are enrollment state, not data; wiping them (as v1 did, where a name was free) would lock every agent and administrator out. Pending item 1.
13. **`reset-credential` on the API** (`POST /v1/admin/identities/{identity}/reset-credential`, admin flag, Decision 73's "the API path is then ordinary authorization") and on the host (`server admin reset-credential NAME`) both call `identities.Service.ResetCredential`, which deletes the identity's keys and returns the count (0 is success). Minted bearer tokens are not revoked and stay valid until `exp` (at most the lifetime); pending item 4.
14. **Server configuration surface** (Decision 85, spec 6.5): a JSON object, strict (unknown keys, `null`, trailing content and an empty file rejected, ported from v1 `internal/config/config.go` `Load`), with these settings: `listen`, `registration`, `sqlite`, `postgres` (exactly one of the two), `issuer`, `signing_key_file`, `token_lifetime_seconds` (3600), `jwks_refresh_seconds` (300), `issuers`, `default_acl` (the Decision 88 literal with `<creator>` as the placeholder), `channel_creation` (`open`), `receipt_retention_minutes`, `session_retention_hours`, `send_messages_per_second`, `send_kib_per_second`, `message_retention_hours`, `tombstone_min_hours`, `task_expiry_hours`, `memory_expiry_hours`, `session_lease_seconds`, `lease_skew_seconds`, `watch_max_seconds`, `watch_merge_seconds`, `maintenance_interval_seconds`, `max_message_kib`, `receive_default_count`, `receive_max_count`, `result_default_kib`, `embedding_endpoint`, `embedding_model`, `embedding_api_key_file`, `embedding_api_key_env`, `embedding_query_timeout_seconds`, `log_level`. Environment: `AGENTBUS2_SERVER_` + the setting name upper-cased (`AGENTBUS2_SERVER_TOKEN_LIFETIME_SECONDS`); scalars as text, `issuers` and `default_acl` as JSON text. Flags: `-config`, `-listen`, `-registration`, `-sqlite`, `-postgres` (the Milestone 4 names kept) and a repeatable `-set name=value` for every other setting, so "flags override both" holds for all of them without thirty flags. Precedence: defaults, file, environment, flags. The core defaults are `core.Defaults()`'s (Milestone 3 choice 6) so the two never drift: `config.Default()` is built from them.
15. **Package layout.** `internal/auth/keys`, `internal/auth/assertion`, `internal/auth/token`; `internal/core/identities`; `internal/server/config`; in `internal/server`: `auth_bearer.go`, `auth_handlers.go`, `admin_auth.go`; in `internal/server/cmd`: `listen.go`, `admin.go`, `config.go` (flag plumbing); in `internal/bus/remote`: `auth.go` (`Register`, `Token`, `WithBearer`) beside the `Admin` methods. The conformance exit test is `internal/conformance/bus/auth_test.go`.
16. **`register` and `token` are not `Bus` methods.** A `Bus` is bound to one identity (Milestone 3 choice 1) and both operations precede one; they are free functions over the generated client in `remote` and handlers in `server`. `Reset` and `ResetCredential` are `Admin` methods (Decision 48 names them in the admin section).
17. **Local mode registration** (Decision 36): `identities.Service.RegisterLocal(ctx, name)` creates the row and the `general` subscription with no key; Milestone 6's local profile calls it in process before building its `Bus`. The HTTP `register` never takes that path (a key is required on the wire).

## Claude proposal, pending Eric

Five points the frozen design leaves open; each proceeds on recommendation (A) with a `// pending Eric: Milestone 5 item N` marker on the test that encodes it. Task 9 records the answers in the ADR 0002 and 0003 addenda.

1. **Admin reset keeps identities, keys, admin flags and enrollment tokens** (choice 12). v1's `Reset` wiped identities too. A) Keep them; wipe data only (recommended). B) Wipe everything but the administrator who called it. C) Wipe everything, as v1. Encoded in Task 5's `TestResetKeepsIdentitiesAndKeys`.
2. **Server signing key in a PEM file, ephemeral when unset** (choice 6). A) File by `signing_key_file`, generated when absent, ephemeral per process when the setting is empty (recommended). B) A Store row holding the private key, so replicas share it without a volume. C) A file, required (no ephemeral mode). Encoded in Task 8's `TestSigningKeyFileLifecycle`.
3. **A configured `issuer` URL with the bound address as its default** (choice 7). A) As stated (recommended). B) Per-request `Host`, as Milestone 4's discovery did (breaks when clients and proxies disagree on the host). Encoded in Task 6's `TestIssuerIsConfiguredNotRequestHost`.
4. **`reset-credential` leaves minted tokens valid until `exp`** (choice 13). A) As stated; the lifetime bounds the exposure (recommended). B) The authenticator also requires the identity to hold at least one key on every request (one `PublicKeys` read per call). Encoded in Task 7's `TestResetCredentialKeepsMintedToken`.
5. **`server admin create-identity NAME`** for identities whose credential is an external issuer (choice 11). A) The host command creates the row and the `general` subscription (recommended; Decision 73 lists only make-admin and reset-credential). B) Auto-create the mapped identity on its first valid external token. Encoded in Task 8's `TestAdminCreateIdentity`.

## Not ported (Decision 31)

v1 has no authentication, registration, admin flag or server configuration; the ports in this milestone are v1 `internal/config/config.go` (`Load`'s strict JSON rules and `validate`'s bound messages, Task 4), v1 `main.go` (`run`'s subcommand dispatch and `parseExit`, Task 8, extending Milestone 4's port), v1 `internal/bus/admin.go` (`Reset`'s whole-table wipe re-scoped by choice 12, Task 5). Not ported: v1 `internal/cli/reset.go` (the `yes` confirmation prompt: reset is an API operation called by an authenticated administrator and there is no host reset command), `AGENTBUS_CONFIG` and `AGENTBUS_DATA_DIR` (the file is `-config` only and there is no data directory: the SQLite path is a setting), `DataDirectory` resolution, `InspectionCommand`, `DiscoveryEnabled`, the TUI fields, `LiveSessionCount`'s warning.

## Names assumed from Milestones 3 and 4 (diffed against `main` before Wave 2)

From Milestone 3: `core.New`, `core.Defaults`, `core.Config` with `ChannelCreation`, `DefaultACL func(creator string) []store.ACLEntry`, `SessionRetentionHours`, `SessionLeaseSeconds`, `LeaseSkewSeconds`, `WatchMaxSeconds`, `WatchMergeSeconds`, `MaintenanceIntervalSeconds`, `ReceiptRetentionMinutes`, `MessageRetentionHours`, `TombstoneMinHours`, `TaskExpiryHours`, `MemoryExpiryHours`, `MaxMessageKiB`, `SendMessagesPerSecond`, `SendKiBPerSecond`, `ReceiveDefaultCount`, `ReceiveMaxCount`, `ResultDefaultKiB`, `EmbeddingQueryTimeoutSeconds`, `EmbeddingModel`, `Validate`; `core.Options{Now, Notifier, Embedder, Logger}`; `Core.Now`, `NowMs`, `Publish`, `RequireIdentity`, `RequireAdmin`, `ChannelByName`, `WithReceipt`, `E`, `FromStore`, `RandomToken`, `RandomHex`; `channels.Service{*core.Core}` with `EnsureGeneral`, `Create`, `Subscribe`; `embed.NewHTTP(endpoint, model, apiKey, client)`, `embed.Install`; `bus.Admin`, `bus.Status`, `bus.Code*`, `bus.Error`, `bus.CodeOf`; `local.New`; the suite's `newHarness`, `harness{core, store, e}`, `openStore`, `h.as`, `h.admin`, `codeOf`, `dbtest.ForEachEngine`. From Milestone 4: `server.New(c, Options{Auth, Version, Issuers, Logger})`, `server.Authenticator`, `RefuseAll`, `InProcess`, `IdentityFrom`, `WithIdentity`, `InProcessTransport`, `WriteProblem`, `StatusFor`, `ProblemFor`, `Spec`, `baseURL`, `Capabilities`, the `api` type in `internal/server/api.go` holding the `*core.Core` (field name as the Milestone 4 implementer wrote it; Tasks 6 and 7 build `identities.Service{Core: <that field>}` inline), the `env`/`newEnv`/`envOptions`/`e.client`/`e.raw`/`e.identity` fixtures of `testenv_test.go`, `discovery_test.go`'s `TestOpenIDConfigurationNamesMilestone5Paths`; `remote.New`, `errorOf`, `key`; `cmd.Run`, `cmd.Serve`, `cmd.Config{Listen, Store, Auth, Notifier, Cfg, Now, ReadTimeout, Logger, Ready}`, `httpServer`, `headerAuth`, `startHelper`, `cmd_test.go`'s `TestRunFlags` and `TestRunRefusesEveryProtectedOperation` (30 protected, 3 open); `gen.StrictServerInterface`, `spec.GetSwagger`, the `operationId`s of Milestone 4 choice 1; `scripts/m4-exit-checks.sh --deps-only`, `scripts/check-gates-selftest.sh` blocks 1 to 13 and its `begin`/`finish`/`problem`/`revert`/`run_gates`/`expect_checks` helpers and `CHECKS` array.

## Review Focus

1. A bearer signed with the right key but the wrong claims (another issuer's `iss`, an `aud` of the token endpoint instead of the issuer, `exp` in the past, a `kid` of a rotated key): each is 401 `unauthenticated` as problem+json, never 500 and never an accepted identity. (Task 2: `TestIssuerVerifyRefusesWrongClaims`; Task 6: `TestBearerRefusals`.)
2. A client assertion presented twice, or signed by a key registered to another identity (`iss` names the victim, `kid` the attacker's key): 401 both times; the `jti` set never grows past the assertions still valid. (Task 2: `TestReplaySetBounded`; Task 6: `TestTokenRefusesForeignKeyAndReplay`.)
3. Registration races: two concurrent registers of one free name with different keys, one wins with 201 and the other gets 409; the same key twice is 201 twice; an enrollment token with `uses_left` 1 used by two concurrent registers is consumed once. (Task 5: `TestRegisterConcurrent`, both engines.)
4. Configuration precedence and rejection: a setting given in the file, the environment and `-set` resolves to the flag; an unknown key, an unknown variable name and a value of the wrong type each fail at start with a message naming the setting; `AGENTBUS2_SERVER_DEFAULT_ACL` with an entry lacking permissions is rejected before the server listens. (Task 4: `TestPrecedenceTable`, `TestRejectsUnknownAndMalformed`; Task 8: `TestRunLoadsConfigFileEnvAndFlags`.)
5. The non-loopback listener: a server bound to `0.0.0.0:0` with no `-registration` is in `enrollment` mode, so `register` without a token is 403 and with a fresh token 201; a loopback bind with no override is `open`; `-registration=closed` on loopback refuses every register. (Task 8: `TestClassifyListen`, `TestRegistrationDefaultsByListener`; Task 9's exit test.)

---

### Task 1: Store: enrollment tokens and data reset (version 2 migration, both engines)

**Depends on:** none (needs Milestone 2 on `main`; may run while Milestone 3 and 4 land).

**Files:**
- Create: `internal/store/enrollment.go` (`EnrollmentToken`, `EnrollmentTokens`), `internal/store/sqlstore/enrollment.go`, `internal/store/sqlite/schema/0002_enrollment_tokens.sql`, `internal/store/postgres/schema/0002_enrollment_tokens.sql`
- Modify: `internal/store/store.go` (`Tx` embeds `EnrollmentTokens`), `internal/store/meta.go` (`ResetData`), `internal/store/sqlstore/meta.go` (`ResetData`), `internal/store/sqlite/migrations.go` and `internal/store/postgres/migrations.go` (`{Version: 2, Up: runFile("schema/0002_enrollment_tokens.sql")}`)
- Test: create `internal/conformance/store/enrollment_test.go`, `internal/conformance/store/reset_test.go`; modify `internal/conformance/store/open_test.go`, `internal/store/sqlite/open_test.go` (every literal `1` that means "the latest version" becomes `latest := len(migrationsFor(e))` / `len(sqlite.Migrations())`, every `2` that means "newer than latest" or "one step past latest" becomes `latest+1`, built with `fmt.Sprintf` for the `UPDATE schema_meta` strings and `openStep(latest+1, ...)` for the synthetic step), `internal/conformance/store/store_test.go` (`TestStoreImplementsEveryAggregate` gains `ListEnrollmentTokens`)

**Port from v1:** `internal/bus/admin.go` `Reset` (the table wipe in one transaction; the table list re-scoped by choice 12). Enrollment tokens are new (Decision 20; no v1 source).

**Interfaces (produces):**

```go
package store
type EnrollmentToken struct { ID string /* 16 hex, random, what list and revoke name */; TokenHash string /* sha256 hex of the token */; Identity string /* "" or a bound name */; ExpiresAt int64; UsesLeft int; CreatedAt int64 }
type EnrollmentTokens interface {
	PutEnrollmentToken(ctx context.Context, t EnrollmentToken) error               // ErrInvalid: empty ID or TokenHash, UsesLeft < 1, ExpiresAt <= 0, Identity non-empty and failing NameRule or reserved; ErrConflict: ID or TokenHash taken
	EnrollmentTokenByHash(ctx context.Context, hash string) (EnrollmentToken, error) // ErrNotFound
	ListEnrollmentTokens(ctx context.Context) ([]EnrollmentToken, error)             // by CreatedAt, then ID, byte order
	ConsumeEnrollmentToken(ctx context.Context, id string, now int64) (bool, error)  // uses_left-- when uses_left > 0 and expires_at > now, returning true; false (no change) otherwise; ErrNotFound unknown id
	DeleteEnrollmentToken(ctx context.Context, id string) error                      // ErrNotFound
}
// meta.go
// ResetData deletes every row of notices, channels (channel_acl, subscriptions, session_cursors, messages, message_tags, embeddings, embed_failures cascade), subscriptions, tag_subscriptions (tag_subscription_tags cascades), sessions, memory_access and receipts, in that order; keeps schema_meta, seq_counter, leases, identities, identity_keys and enrollment_tokens (choice 12). Write transactions only (ErrReadOnly in Read).
ResetData(ctx context.Context) error
```

Schema 0002 (SQLite; Postgres with `BIGINT` for `expires_at` and `created_at`):

```sql
CREATE TABLE enrollment_tokens (
  id TEXT PRIMARY KEY,
  token_hash TEXT NOT NULL UNIQUE,
  identity TEXT NOT NULL DEFAULT '',
  expires_at INTEGER NOT NULL,
  uses_left INTEGER NOT NULL CHECK (uses_left >= 0),
  created_at INTEGER NOT NULL
);
```

- [ ] **Step 1: Write the failing tests** (both engines through `dbtest.ForEachEngine`; prefix `TestEnrollment`, `TestReset`, `TestOpen`)

`enrollment_test.go`: `TestEnrollmentPutListGet` (two tokens; list order by `CreatedAt` then `ID`; `EnrollmentTokenByHash` finds each; unknown hash `ErrNotFound`), `TestEnrollmentRejects` (empty `ID`, empty `TokenHash`, `UsesLeft 0`, `Identity "a/b"`, `Identity "everyone"` each `ErrInvalid`; a second put with the same `ID` and with the same `TokenHash` each `ErrConflict`), `TestEnrollmentConsume` (`UsesLeft 2`: consume true, true, false; `ExpiresAt` past `now`: false without decrement; unknown id `ErrNotFound`), `TestEnrollmentDelete` (deleted then `ErrNotFound`; delete again `ErrNotFound`), `TestEnrollmentConsumeConcurrent` (a token with `UsesLeft 1`; 8 goroutines each in their own `Tx` calling `ConsumeEnrollmentToken`; exactly one true across both engines, the rest false or `ErrRetryable` retried by the Store). `reset_test.go`: `TestResetDataWipesDataKeepsRegistry` (fixtures through the Store: two identities, one admin, a key on each, an enrollment token, a channel with an ACL and a subscription, a message with a tag, a session with a cursor, a receipt, a notice, a memory-access row; `ResetData` inside `Tx`; afterwards every data table counts 0 through `rawDB`, `ListIdentities` still two with the admin flag, `PublicKeys` still one each, `ListEnrollmentTokens` still one, `MaxSeq` unchanged, the maintenance lease row present; a second `ResetData` is a no-op without error), `TestResetDataReadOnly` (`Read` + `ResetData` → `ErrReadOnly`). `open_test.go` (both files): the rewrite above plus `TestOpenMigratesVersion1Database` (open with `Migrations()[:1]` so the database is at version 1 and has no `enrollment_tokens`; reopen with the full set; `SchemaVersion == len(Migrations())`; `tableCount("enrollment_tokens") == 1`; reopen again changes nothing). `store_test.go`: the `ListEnrollmentTokens` row.

- [ ] **Step 2: Run to verify they fail**

Run: `go test -count=1 -p 1 -run '^Test(Enrollment|Reset|Open)' ./internal/conformance/store/ && go test ./internal/store/...`
Expected: build failure (`undefined: ab2store.EnrollmentToken`, `tx.ResetData undefined`).

- [ ] **Step 3: Implement.** The migration files as above; `validateMigrations` already requires versions 1..n in order, so the two `Migrations()` lists gain the step at index 1. `PutEnrollmentToken` validates in Go, then `INSERT`, mapping the unique violation with `isConflict`. `ConsumeEnrollmentToken` is one `UPDATE ... SET uses_left = uses_left - 1 WHERE id = ? AND uses_left > 0 AND expires_at > ?` whose rows-affected decides the bool, preceded by a `SELECT` that gives `ErrNotFound`. `ResetData` is a fixed list of `DELETE FROM` statements, the parent tables first so the cascades do the rest on both engines (the SQLite dialect enables foreign keys; confirm `PRAGMA foreign_keys` in `sqlite.DSN` before relying on the cascade, else delete the children explicitly). State (Definition of Done D): the migration is Milestone 2's per-version transaction under the Decision 68 lock, so a second start does nothing, an overlapping start waits and sees version 2, and a failed step leaves version 1 for the next start (`TestOpenFailedStepLeavesPreviousVersion` already proves the mechanism); `ResetData` twice is twice empty.

- [ ] **Step 4: Run the suite on both engines and the gate**

Run: `env -u AGENTBUS2_TEST_POSTGRES_DSN go test -count=1 -p 1 -v ./internal/conformance/store/ 2>&1 | grep -E '^(\s+--- FAIL|ok|FAIL)'`, then `make verify` (600000 ms).
Expected: no `--- FAIL`; `verify: OK`.

- [ ] **Step 5: Commit**

```bash
git add internal/store/enrollment.go internal/store/store.go internal/store/meta.go internal/store/sqlstore/enrollment.go internal/store/sqlstore/meta.go internal/store/sqlite/schema/0002_enrollment_tokens.sql internal/store/postgres/schema/0002_enrollment_tokens.sql internal/store/sqlite/migrations.go internal/store/postgres/migrations.go internal/conformance/store/enrollment_test.go internal/conformance/store/reset_test.go internal/conformance/store/open_test.go internal/conformance/store/store_test.go internal/store/sqlite/open_test.go
git commit -m "feat(store): enrollment token rows (schema version 2 on both engines) and ResetData

Enrollment tokens are the Milestone 5 rows the Milestone 2 plan reserved
(Decision 20; stored hashed). ResetData ports the table wipe of v1
internal/bus/admin.go Reset, keeping identities, keys, admin flags and
enrollment tokens (Milestone 5 pending item 1). The open tests judge the
schema version by len(Migrations()) instead of literals."
```

---

### Task 2: `internal/auth`: keys, client assertions, issuer, bearer verification, external JWKS

**Depends on:** none (imports nothing from this module; may run while Milestones 3 and 4 land).

**Files:**
- Create: `internal/auth/doc.go` (the import rule of Global Constraints), `internal/auth/keys/keys.go`, `internal/auth/keys/file.go`, `internal/auth/assertion/assertion.go`, `internal/auth/token/issuer.go`, `internal/auth/token/replay.go`, `internal/auth/token/external.go`, `internal/auth/token/verifier.go`
- Modify: `go.mod`, `go.sum` (`github.com/go-jose/go-jose/v4` v4.1.6 direct)
- Test: `internal/auth/keys/keys_test.go`, `internal/auth/keys/file_test.go`, `internal/auth/assertion/assertion_test.go`, `internal/auth/token/issuer_test.go`, `internal/auth/token/replay_test.go`, `internal/auth/token/external_test.go`

**Port from v1:** nothing (v1 has no authentication). RFC 7523, RFC 7638 and RFC 7517 are the sources.

**Interfaces (produces):**

```go
package keys // internal/auth/keys
type Algorithm string
const ( EdDSA Algorithm = "EdDSA"; ES256 Algorithm = "ES256" )               // Decision 55; the two values store.PublicKey.Algorithm admits
func Generate(alg Algorithm) (crypto.Signer, error)                           // ed25519.GenerateKey or ecdsa P-256; unknown alg → error
func AlgorithmOf(pub crypto.PublicKey) (Algorithm, error)                     // ed25519.PublicKey → EdDSA; *ecdsa.PublicKey on P-256 → ES256; anything else (RSA, P-384, a private key) → error "unsupported key type"
func KID(pub crypto.PublicKey) (string, error)                                // RFC 7638: jose.JSONWebKey{Key: pub}.Thumbprint(crypto.SHA256), base64url without padding
func EncodePrivatePEM(k crypto.Signer) ([]byte, error)                        // one "PRIVATE KEY" block, PKCS#8 (Decision 55)
func ParsePrivatePEM(b []byte) (crypto.Signer, Algorithm, error)              // exactly one PEM block of type "PRIVATE KEY"; any other type, a second block or trailing non-PEM bytes → error; AlgorithmOf on the public half
func MarshalPublic(pub crypto.PublicKey) ([]byte, error)                      // PKIX DER, the store.PublicKey.Key form
func ParsePublic(der []byte) (crypto.PublicKey, Algorithm, error)
func ToJWK(pub crypto.PublicKey, kid string) (jose.JSONWebKey, error)         // Algorithm string(alg), Use "sig", KeyID kid
func FromJWK(j jose.JSONWebKey) (pub crypto.PublicKey, alg Algorithm, kid string, err error) // !j.IsPublic() → error "private key material"; AlgorithmOf; kid = KID(pub); a non-empty j.KeyID that differs from it → error "kid does not match the key"; j.Algorithm non-empty and ≠ alg → error
// file.go
func WriteFile(path string, pem []byte) error                                 // O_CREATE|O_EXCL|O_WRONLY 0o600 on "<path>.tmp-<random>" in the same directory, write, close, os.Rename (Decision 22); leaves no temp file on error
func LoadOrCreateFile(path string, alg Algorithm) (k crypto.Signer, got Algorithm, created bool, err error) // ParsePrivatePEM when the file exists (its own algorithm wins, alg ignored); else Generate(alg) + WriteFile; a file with mode wider than 0600 on Unix → error "must be mode 0600"
```

```go
package assertion // internal/auth/assertion
const Type = "urn:ietf:params:oauth:client-assertion-type:jwt-bearer"
const MaxLifetime = 5 * time.Minute
type Input struct { Identity, TokenEndpoint string; Key crypto.Signer; KID string; Now time.Time; Lifetime time.Duration /* 0: MaxLifetime; above it: error */ }
func Sign(in Input) (string, error)   // choice 4: header typ JWT, alg from keys.AlgorithmOf(Key.Public()), kid; claims iss = sub = Identity, aud [TokenEndpoint], jti 16 random bytes base64url, iat Now, exp Now+Lifetime; empty Identity, TokenEndpoint or KID → error
type Parsed struct { Identity, KID, JTI string; Alg keys.Algorithm; Expiry time.Time; tok *jwt.JSONWebToken }
func Parse(raw string) (Parsed, error) // jwt.ParseSigned(raw, []jose.SignatureAlgorithm{jose.EdDSA, jose.ES256}); UnsafeClaimsWithoutVerification; iss and sub non-empty and equal, kid non-empty, jti non-empty, exp present; else a wrapped ErrMalformed
var ErrMalformed = errors.New("assertion: malformed")
func (p Parsed) Verify(pub crypto.PublicKey, tokenEndpoint string, now time.Time, leeway time.Duration) error // keys.AlgorithmOf(pub) must equal p.Alg; p.tok.Claims(pub, &c); c.ValidateWithLeeway(jwt.Expected{Issuer: p.Identity, Subject: p.Identity, AnyAudience: jwt.Audience{tokenEndpoint}, Time: now}, leeway); p.Expiry.Sub(now) > MaxLifetime+leeway → error "exp too far ahead"; every failure wraps ErrInvalid
var ErrInvalid = errors.New("assertion: invalid")
```

```go
package token // internal/auth/token
const Leeway = 60 * time.Second                   // choice 5
type Issuer struct{ ... }
func NewIssuer(issuerURL string, key crypto.Signer, lifetime time.Duration, now func() time.Time) (*Issuer, error) // issuerURL absolute http(s), no trailing slash, no path, query or fragment; lifetime > 0; kid = keys.KID
func (i *Issuer) URL() string; func (i *Issuer) KID() string; func (i *Issuer) Algorithm() keys.Algorithm
func (i *Issuer) TokenEndpoint() string              // URL() + "/v1/auth/token"
func (i *Issuer) JWKSURI() string                    // URL() + "/v1/auth/jwks"
func (i *Issuer) Lifetime() time.Duration
func (i *Issuer) Mint(identity string) (raw string, expiresAt time.Time, err error) // choice 5: iss = aud = URL, sub identity, jti, iat now, exp now+lifetime, header kid and typ JWT
func (i *Issuer) Verify(raw string, now time.Time) (identity string, err error)   // ParseSigned [EdDSA, ES256]; header kid == KID() else ErrUnknownKey; Claims(pub); ValidateWithLeeway(Expected{Issuer: URL, AnyAudience: {URL}, Time: now}, Leeway); sub non-empty; failures wrap ErrInvalid
func (i *Issuer) JWKS() jose.JSONWebKeySet            // one public key: kid, alg, use sig
var ErrInvalid = errors.New("token: invalid"); var ErrUnknownKey = errors.New("token: unknown key"); var ErrUnknownIssuer = errors.New("token: unknown issuer")
// replay.go
type Replay struct{ ... }                             // choice 4: a mutex-guarded map jti → exp, pruned on every call
func NewReplay() *Replay
func (r *Replay) Seen(jti string, exp, now time.Time) bool // true when jti was recorded and has not expired; otherwise records it and returns false; entries with exp <= now are dropped first
// external.go (choice 11)
type External struct { Issuer, JWKSURI, Audience string; Algorithms []jose.SignatureAlgorithm /* nil: EdDSA, ES256, RS256 */; Subjects map[string]string; Client *http.Client /* nil: a 10 s client */; TTL time.Duration /* 0: 300 s */; Now func() time.Time }
func (e *External) Verify(ctx context.Context, raw string) (identity string, err error) // fetch the JWKS when none is cached or the cache is older than TTL; look up the header kid; on an unknown kid refetch once if the last refetch is over a minute old; ParseSigned with Algorithms; Expected{Issuer: e.Issuer, AnyAudience: {e.Audience}}; Subjects[sub] else error "subject %q is not mapped" (wraps ErrInvalid)
// verifier.go
func IssuerOf(raw string) (string, error)            // UnsafeClaimsWithoutVerification's iss, for routing only
type Verifier struct { Own *Issuer /* nil: no own issuer */; External []*External; Now func() time.Time }
func (v Verifier) Verify(ctx context.Context, raw string) (identity string, err error) // IssuerOf; == Own.URL() → Own.Verify; a configured External → its Verify; else ErrUnknownIssuer
```

- [ ] **Step 1: Write the failing tests**

`keys_test.go`: `TestGenerateAndAlgorithmOf` (both algorithms; an RSA key and a P-384 key → error), `TestKIDIsThumbprint` (a fixed Ed25519 public key's `KID` equals the RFC 7638 thumbprint computed in the test with `jose.JSONWebKey.Thumbprint` and base64url), `TestPrivatePEMRoundTrip` (both algorithms; a `EC PRIVATE KEY` SEC 1 block, two blocks and trailing bytes each refused), `TestPublicDERRoundTrip`, `TestJWKRoundTrip` (`ToJWK` then JSON then `FromJWK` gives the same key and kid for both algorithms), `TestFromJWKRejects` (private material, RSA, a mismatching `kid`, an `alg` naming the other algorithm; the error text names the reason). `file_test.go`: `TestWriteFileAtomicAndMode` (0600; no `.tmp-` leftover; an existing file is replaced; a write into a missing directory fails with no leftover), `TestLoadOrCreateFile` (created true then false with the same key; a file of the other algorithm is loaded as that algorithm; mode 0644 refused on Unix; a truncated file refused). `assertion_test.go`: `TestSignThenParseAndVerify` (both algorithms; `Parsed` fields; `Verify` nil), `TestParseRefusesOtherAlgorithms` (Global Constraints: tokens signed `HS256` with a shared secret and an `alg: none` compact string → `ErrMalformed`), `TestParseRefusesClaimShapes` (`iss` ≠ `sub`, no `kid`, no `jti`, no `exp`), `TestVerifyRefuses` (wrong key, the other identity's key of the right algorithm, `aud` of another endpoint, `exp` in the past beyond leeway, `exp` 20 min ahead, `nbf` in the future, algorithm of `pub` ≠ header `alg`), `TestSignRejectsLongLifetime`. `issuer_test.go`: `TestMintThenVerify` (identity back; `expiresAt` = now + lifetime; header `kid` = `KID()`), `TestIssuerVerifyRefusesWrongClaims` (Review Focus 1: a token minted by a second `Issuer` with another key and the same URL → `ErrUnknownKey`; the same key with `iss` of another URL, with `aud` of the token endpoint, expired past leeway → `ErrInvalid`; an assertion presented as a bearer → `ErrUnknownKey` or `ErrInvalid`, never nil), `TestJWKSShape` (`keys[0]` has `kid`, `alg`, `use`, `kty` `OKP` or `EC`), `TestNewIssuerRejectsURL` (trailing slash, a path, `ftp://`, empty). `replay_test.go`: `TestReplaySetBounded` (Review Focus 2: `Seen` false then true; after `now` passes `exp` the entry is gone and `Seen` is false again; 10 000 expired entries are pruned by the next call: the map length is checked through a package-level accessor `r.len()`). `external_test.go` (an `httptest.Server` serving a JWKS from a test-generated key, a counter on fetches): `TestExternalVerifyMapsSubject`, `TestExternalRefetchOnUnknownKidOnce` (two unknown-kid tokens within a minute: one refetch), `TestExternalRefusesUnmappedAndWrongAudience`, `TestVerifierRoutesByIssuer` (own, external, unknown → `ErrUnknownIssuer`; a token with no `iss` → `ErrUnknownIssuer`).

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/auth/...`
Expected: build failure (`undefined: Generate`, `undefined: NewIssuer`).

- [ ] **Step 3: Implement** as in Interfaces; `go get github.com/go-jose/go-jose/v4@v4.1.6 && go mod tidy`. Random values come from `crypto/rand` (16 bytes base64url for `jti`, 8 bytes hex for temp file names).

- [ ] **Step 4: Run the tests, the import rule and the gate**

Run: `go test -race ./internal/auth/...`, then `go list -deps ./internal/auth/... | grep -E 'ericfitz/agentbus2/internal/(store|core|server|gen|bus)' ; echo "exit=$?"` (expected `exit=1`: no match), then `make build-all vet fmt-check lint test tidy-check`, then `make verify` (600000 ms).
Expected: pass; `verify: OK`.

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum internal/auth/doc.go internal/auth/keys/keys.go internal/auth/keys/file.go internal/auth/keys/keys_test.go internal/auth/keys/file_test.go internal/auth/assertion/assertion.go internal/auth/assertion/assertion_test.go internal/auth/token/issuer.go internal/auth/token/replay.go internal/auth/token/external.go internal/auth/token/verifier.go internal/auth/token/issuer_test.go internal/auth/token/replay_test.go internal/auth/token/external_test.go
git commit -m "feat(auth): Ed25519 and ES256 keys, RFC 7523 client assertions, the token issuer, bearer verification and external JWKS

Decisions 19, 55, 57, 70, 84 with go-jose v4.1.6 (Milestone 5 choice 1):
explicit algorithm allowlists, RFC 7638 thumbprints as kid, PKCS#8 PEM
files written atomically at mode 0600 (Decision 22), a bounded jti replay
set, and iss plus sub mapping for configured issuers. A leaf package that
Milestone 6's client links unchanged."
```

---

### Task 3: OpenAPI: token, jwks, register, admin reset and reset-credential; `bus.Admin` extension; stubs

**Depends on:** none among this plan's tasks (external gate: Milestone 4 Task 11 on `main`).

**Files:**
- Modify: `api/openapi.yaml`, `internal/gen/gen.go` and `internal/gen/spec/spec.go` (regenerated, committed), `internal/gen/openapi31_test.go` (`TestOperationTable` gains the five rows; 32 protected and 6 open, 38 in all; `TestValidatorOnRealDocument` gains the form-body case below), `internal/bus/admin.go` (`ResetInput`, `ResetCredentialInput`, `ResetCredentialResult`, the two `Admin` methods), `internal/server/discovery_test.go` (`TestOpenIDConfigurationNamesMilestone5Paths` loses its 404 assertion and its `pending Eric: Milestone 4 item 2` marker; the token endpoint and JWKS URI assertions stay), `internal/server/cmd/cmd_test.go` (`TestRunRefusesEveryProtectedOperation`: 32 protected and 6 open; an open operation is asserted as "not 401" rather than 200, because `register` and `token` with a `{}` body are 400 from the validator and `jwks` is 200 or, until Task 6, 503)
- Create: `internal/server/auth_stub.go` (`Token`, `Jwks`, `Register`; replaced by Task 6), `internal/server/admin_stub.go` (`AdminReset`, `ResetCredential`; replaced by Task 7), `internal/bus/local/admin_stub.go` (`Reset`, `ResetCredential`; replaced by Task 7), `internal/bus/remote/admin_stub.go` (the same two; replaced by Task 7). Each stub file declares its own error (`errAuthNotImplemented`, `errAdminNotImplemented`, `ErrNotImplemented` in `local` and `remote`) and returns `fmt.Errorf("<pkg>.<Method>: %w", err)`; the server stubs answer 500 `internal` through the hook, which `TestRunRefusesEveryProtectedOperation`'s "not 401" tolerates. Task 9 asserts none survive.

**Port from:** the Milestone 4 document's conventions (choice 1's table, problem `default`, `Idempotency-Key` optional). The operations are new.

**Interfaces (produces):**

| operationId | Method and path | Security | IK | Request | Responses | Served by |
| --- | --- | --- | --- | --- | --- | --- |
| `token` | `POST /v1/auth/token` | `[]` | | `application/x-www-form-urlencoded` `TokenRequest{grant_type enum [client_credentials], client_assertion_type enum [urn:ietf:params:oauth:client-assertion-type:jwt-bearer], client_assertion}` all required | 200 `AccessToken{access_token, token_type, expires_in (int64)}` | Task 6 |
| `jwks` | `GET /v1/auth/jwks` | `[]` | | | 200 `JWKS{keys: [JWK]}` where `JWK` is `type: object, additionalProperties: true` (go-jose marshals it) | Task 6 |
| `register` | `POST /v1/auth/register` | `[]` | | `RegisterRequest{identity, public_key_jwk (JWK), enrollment_token?}`; `identity` and `public_key_jwk` required | 201 `Registration{identity, kid, algorithm enum [EdDSA, ES256], created (bool)}` | Task 6 |
| `adminReset` (tag `admin`) | `POST /v1/admin/reset` | bearer, oidc | IK | | 204 | Task 7 |
| `resetCredential` (tag `admin`) | `POST /v1/admin/identities/{identity}/reset-credential` | bearer, oidc | IK | | 200 `CredentialReset{identity, keys_removed (int)}` | Task 7 |

Generated names (verified in Evidence (1) for the form body): `gen.TokenRequestObject{Body *gen.TokenFormdataRequestBody}`, `gen.Token200JSONResponse`, `gen.ClientWithResponses.TokenWithFormdataBodyWithResponse`, `gen.JwksRequestObject`, `gen.RegisterRequestObject{Body *gen.RegisterJSONRequestBody}`, `gen.Register201JSONResponse`, `gen.AdminResetRequestObject{Params}`, `gen.AdminReset204Response`, `gen.ResetCredentialRequestObject{Identity string; Params}`, `gen.ResetCredential200JSONResponse`; the `JWK` schema generates as `map[string]interface{}`. No schema is named `TokenResponse`, `JwksResponse`, `RegisterResponse`, `AdminResetResponse` or `ResetCredentialResponse` (Evidence (1)).

```go
package bus // internal/bus/admin.go, added
type ResetInput struct { IdempotencyKey string }
type ResetCredentialInput struct { Identity, IdempotencyKey string }
type ResetCredentialResult struct { Identity string; KeysRemoved int }
// Admin gains: Reset(ctx context.Context, in ResetInput) error; ResetCredential(ctx context.Context, in ResetCredentialInput) (ResetCredentialResult, error)   // Decisions 48, 73; the admin section
```

- [ ] **Step 1: Write the failing tests**

`openapi31_test.go`: the five rows in `TestOperationTable`'s literal table with the counts 32 and 6; `TestDocumentIs31AndValidates` unchanged (the twelve codes stay); `TestValidatorOnRealDocument` gains: `POST /v1/auth/token` with `Content-Type: application/x-www-form-urlencoded` and body `grant_type=client_credentials&client_assertion_type=urn:ietf:params:oauth:client-assertion-type:jwt-bearer&client_assertion=a.b.c` reaches a stub `Token` with `Body.ClientAssertion == "a.b.c"` and no token; the same body minus `client_assertion` is 400 before the handler; `grant_type=password` is 400; `POST /v1/auth/register` with `{"identity":"pat"}` (no key) is 400; `POST /v1/admin/reset` without a token is 401. `cmd_test.go`: the counts and the "not 401" assertion. `discovery_test.go`: the marker and the 404 line removed.

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/gen/... ./internal/bus/ ./internal/server/ ./internal/server/cmd/`
Expected: `TestOperationTable` fails on the missing rows; after the `bus.Admin` edit, a build failure in `local` and `remote` (`does not implement ab2bus.Bus (missing method Reset)`) until the stubs exist.

- [ ] **Step 3: Implement**: the document, `make generate`, the four stub files, the `bus` types. `grep -c '^        discover:$' api/openapi.yaml` prints `1`.

- [ ] **Step 4: Run the tests and the gate**

Run: `go test ./internal/gen/... ./internal/bus/... ./internal/server/...`, then `make build-all vet fmt-check lint generate-check test`, then `make verify` (600000 ms), then `make check-gates` (check 4 stays `ok  generator-reacts-to-schema`).
Expected: pass; `verify: OK`.

- [ ] **Step 5: Commit**

```bash
git add api/openapi.yaml internal/gen/gen.go internal/gen/spec/spec.go internal/gen/openapi31_test.go internal/bus/admin.go internal/server/auth_stub.go internal/server/admin_stub.go internal/server/discovery_test.go internal/server/cmd/cmd_test.go internal/bus/local/admin_stub.go internal/bus/remote/admin_stub.go
git commit -m "feat(api): token, jwks, register, admin reset and reset-credential operations; Admin gains Reset and ResetCredential

The RFC 7523 token endpoint takes a form body (Milestone 5 choice 2,
probed on the generated stack), registration takes a JWK, and the admin
section carries reset and reset-credential (Decisions 20, 48, 70, 73, 84,
91). Handlers are stubs replaced by Tasks 6 and 7."
```

---

### Task 4: Server configuration (Decision 85)

**Depends on:** none (imports `internal/core` and `internal/store` only; may run while Milestone 4 lands).

**Files:**
- Create: `internal/server/config/doc.go`, `internal/server/config/config.go` (`Config`, `Default`, `Settings`, `Validate`, `Core`), `internal/server/config/load.go` (`Load`, `Overrides`, `EnvName`, the file, environment and flag layers), `internal/server/config/acl.go` (`ACLEntry`, the `<creator>` placeholder, `DefaultACLFunc`)
- Test: `internal/server/config/config_test.go`, `internal/server/config/load_test.go`, `internal/server/config/acl_test.go`

**Port from v1:** `internal/config/config.go` (`Load`: a missing file means defaults only when no path was given, `DisallowUnknownFields`, "file is empty, expected a JSON object", "must contain exactly one JSON object", the `null` rejection per key; `validate`'s "%s must be between %d and %d, got %d" bound messages; `embedding_model is required when embedding_endpoint is set`; the absolute-URL check; `log_level must be one of debug, info, warn, error`). Not ported: `AGENTBUS_CONFIG`, `AGENTBUS_DATA_DIR`, `DataDirectory`, the TUI, inspection and discovery fields.

**Interfaces (produces):**

```go
package config // internal/server/config
type Issuer struct { Issuer string `json:"issuer"`; JWKSURI string `json:"jwks_uri"`; Audience string `json:"audience"`; Algorithms []string `json:"algorithms"`; Subjects map[string]string `json:"subjects"` }
type ACLEntry struct { Identity string `json:"identity"`; Permissions []string `json:"permissions"` }
const CreatorPlaceholder = "<creator>"
type Config struct {
	Listen string `json:"listen"`; Registration string `json:"registration"` /* "", open, enrollment, closed */
	SQLite string `json:"sqlite"`; Postgres string `json:"postgres"`
	Issuer string `json:"issuer"`; SigningKeyFile string `json:"signing_key_file"`; TokenLifetimeSeconds int `json:"token_lifetime_seconds"`; JWKSRefreshSeconds int `json:"jwks_refresh_seconds"`
	Issuers []Issuer `json:"issuers"`
	DefaultACL []ACLEntry `json:"default_acl"`; ChannelCreation string `json:"channel_creation"`
	ReceiptRetentionMinutes, SessionRetentionHours, SendMessagesPerSecond, SendKiBPerSecond, MessageRetentionHours, TombstoneMinHours, TaskExpiryHours, MemoryExpiryHours, SessionLeaseSeconds, LeaseSkewSeconds, WatchMaxSeconds, WatchMergeSeconds, MaintenanceIntervalSeconds, MaxMessageKiB, ReceiveDefaultCount, ReceiveMaxCount, ResultDefaultKiB int // each with its snake_case json tag as in choice 14
	EmbeddingEndpoint string `json:"embedding_endpoint"`; EmbeddingModel string `json:"embedding_model"`; EmbeddingAPIKeyFile string `json:"embedding_api_key_file"`; EmbeddingAPIKeyEnv string `json:"embedding_api_key_env"`; EmbeddingQueryTimeoutSeconds float64 `json:"embedding_query_timeout_seconds"`
	LogLevel string `json:"log_level"`
}
func Default() Config                     // the core values from core.Defaults() (never literals), TokenLifetimeSeconds 3600, JWKSRefreshSeconds 300, DefaultACL = the Decision 88 literal with CreatorPlaceholder, ChannelCreation "open", LogLevel "info"; Listen, Registration, SQLite, Postgres, Issuer, SigningKeyFile empty
func Settings() []string                  // every json name, in struct order (used by -h and by the unknown-name errors)
func EnvName(setting string) string       // "AGENTBUS2_SERVER_" + strings.ToUpper(setting)
type Overrides map[string]string          // setting name → textual value (the -set form); the named flags are sugar for the same map
func Load(path string, lookupEnv func(string) (string, bool), environ []string, flags Overrides) (Config, error)
// Order: Default(); the file when path != "" (a missing file is an error: the operator named it); every AGENTBUS2_SERVER_* variable in environ (an unknown name after the prefix → error naming it and listing Settings()); then flags (unknown name → the same error). A textual value is parsed by the field's kind: int (strconv.Atoi), float64, bool, string, and for issuers and default_acl JSON text; a parse failure names the setting, the source ("file", "environment", "flag") and the text. Then Validate.
func (c Config) Validate() error          // exactly one of SQLite and Postgres non-empty ("exactly one of sqlite or postgres is required"); Registration in {"", open, enrollment, closed}; Issuer empty or an absolute http(s) URL without path, query, fragment or trailing slash; TokenLifetimeSeconds 1..86400; JWKSRefreshSeconds 10..86400; every Issuer: issuer and jwks_uri absolute URLs, audience non-empty, algorithms ⊆ {EdDSA, ES256, RS256}, subjects non-empty with every value passing store.NameRule and not reserved; DefaultACL through ValidateDefaultACL; ChannelCreation in {open, admins}; the core bounds through Core().Validate(); LogLevel as v1; EmbeddingModel required with EmbeddingEndpoint; at most one of EmbeddingAPIKeyFile and EmbeddingAPIKeyEnv
func (c Config) Core() (core.Config, error) // the field-by-field map; DefaultACL → DefaultACLFunc(c.DefaultACL) (nil when the list equals the shipped default, so store.DefaultACL applies unchanged); ChannelCreation as given
// acl.go
func ValidateDefaultACL(entries []ACLEntry) error           // each entry: identity is everyone, admins, CreatorPlaceholder or passes store.NameRule; permissions non-empty, each one of read, write, admin, no duplicates; the list may be empty (then every new channel has only the implicit entries)
func DefaultACLFunc(entries []ACLEntry) func(creator string) []store.ACLEntry // substitutes creator for CreatorPlaceholder, dropping that entry when creator == "" (Decision 92: general has no creator entry)
func IsShippedDefault(entries []ACLEntry) bool
```

- [ ] **Step 1: Write the failing tests**

`config_test.go`: `TestDefaultMatchesCore` (every core field of `Default()` equals the `core.Defaults()` field of the same name, asserted through `Default().Core()` compared with `core.Defaults()` after zeroing `DefaultACL` on both), `TestSettingsListsEveryJSONName` (reflection over `Config`'s tags equals `Settings()`), `TestValidateRejects` (a table: both stores, neither store, `registration: "tofu"`, `issuer: "http://x/base"`, `issuer: "http://x/"`, `token_lifetime_seconds: 0`, an issuer without `audience`, an issuer with `algorithms: ["HS256"]`, a subject mapped to `everyone`, `channel_creation: "closed"`, `log_level: "trace"`, `embedding_endpoint` without model, `receive_default_count` above `receive_max_count`; each error names the setting), `TestCoreMapsDefaultACL` (a custom list `[{everyone,[read]}, {<creator>,[read,write,admin]}]` → `Core().DefaultACL("pat")` is the two store entries with `pat`; `DefaultACL("")` has one entry; the shipped default → `DefaultACL == nil`). `load_test.go`: `TestPrecedenceTable` (Review Focus 4: for each of `listen`, `registration`, `token_lifetime_seconds`, `channel_creation`, `issuers` and `default_acl`, a file value, an environment value and a flag value; `Load` with all three gives the flag's; with file and environment gives the environment's; with the file alone gives the file's; `environ` built as `[]string{"AGENTBUS2_SERVER_LISTEN=..."}`), `TestLoadFileRules` (a missing named file → error; path `""` → defaults; an empty file, an unknown key `"colour"`, `null` for `listen`, two objects, a non-object each rejected with v1's messages), `TestRejectsUnknownAndMalformed` (`AGENTBUS2_SERVER_COLOUR=1` → error naming `colour` and the list; `-set colour=1` the same; `AGENTBUS2_SERVER_TOKEN_LIFETIME_SECONDS=soon` → error naming the setting, source and text; `AGENTBUS2_SERVER_DEFAULT_ACL='[{"identity":"everyone","permissions":[]}]'` → error naming the entry; an environment variable with the prefix but no name (`AGENTBUS2_SERVER_=x`) → error; variables without the prefix are ignored), `TestEnvName`. `acl_test.go`: `TestValidateDefaultACL` (the shipped default passes; `ghost` passes NameRule and is accepted (Decision 96 is judged by the Store at create time); `a/b` rejected; duplicate permission rejected; `owner` rejected; empty list accepted), `TestDefaultACLFuncSubstitutes`, `TestIsShippedDefault` (order-sensitive equality to the literal).

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/server/config/`
Expected: build failure (`undefined: Default`).

- [ ] **Step 3: Implement** as in Interfaces. `Load` parses the file into a `map[string]json.RawMessage` for the null check (v1) and into `Config` with `DisallowUnknownFields`; textual values are applied through one `set(c *Config, name, text, source string) error` switch over `Settings()` so the environment and the flags share it.

- [ ] **Step 4: Run the tests and the gate**

Run: `go test -race ./internal/server/config/`, then `make build-all vet fmt-check lint test`, then `make verify` (600000 ms).
Expected: pass; `verify: OK`.

- [ ] **Step 5: Commit**

```bash
git add internal/server/config/doc.go internal/server/config/config.go internal/server/config/load.go internal/server/config/acl.go internal/server/config/config_test.go internal/server/config/load_test.go internal/server/config/acl_test.go
git commit -m "feat(server): server configuration from a file, AGENTBUS2_SERVER_* variables and flags, mapped onto the core

Decision 85 with the spec 6.5 settings (Decisions 2, 57, 58, 67, 79, 84,
86, 88, 93); the strict JSON rules and bound messages ported from v1
internal/config/config.go; the default ACL with a <creator> placeholder."
```

---

### Task 5: Core identities service: register, enrollment, default policy, admin flag, reset-credential, reset

**Depends on:** 1, 2.

**Files:**
- Create: `internal/core/identities/service.go` (`Service`, `Mode`, `ParseMode`), `internal/core/identities/register.go` (`Register`, `RegisterLocal`, the `general` subscription), `internal/core/identities/enrollment.go` (`CreateEnrollmentToken`, `ListEnrollmentTokens`, `RevokeEnrollmentToken`, `HashToken`), `internal/core/identities/admin.go` (`MakeAdmin`, `CreateIdentity`, `ResetCredential`, `ResetCredentialAs`, `Reset`, `PublicKey`)
- Test: `internal/core/identities/service_test.go` (Docker-free, SQLite on a temp path, a fixed clock), `internal/conformance/bus/identities_test.go` (both engines through `newHarness`; names prefixed `TestIdentities`)

**Port from v1:** `internal/bus/admin.go` `Reset` (re-scoped by choice 12 and pending item 1); the registration rules are new (Decisions 20, 58, 72, 73, 77).

**Interfaces (produces):**

```go
package identities // internal/core/identities
type Mode string
const ( ModeOpen Mode = "open"; ModeEnrollment Mode = "enrollment"; ModeClosed Mode = "closed" )
func ParseMode(s string) (Mode, error)      // the three values; anything else (including "") → error naming them
type Service struct{ *core.Core }
type RegisterInput struct { Identity string; Key crypto.PublicKey; EnrollmentToken string }
type RegisterResult struct { Identity, KID string; Algorithm keys.Algorithm; Created bool /* the identity row was created by this call */ }
func (s Service) Register(ctx context.Context, mode Mode, in RegisterInput) (RegisterResult, error)
// Choice 8, in order: ParseMode-valid mode (a zero Mode is a programming error: panic); mode closed → forbidden "registration is closed"; Key nil → validation "public key is required"; keys.AlgorithmOf and keys.KID (validation on an unsupported key); mode enrollment and EnrollmentToken "" → forbidden "registration requires an enrollment token"; channels.Service{s.Core}.EnsureGeneral(ctx) (its own transaction); then one Store.Tx: when EnrollmentToken != "": EnrollmentTokenByHash(HashToken(tok)) not found, ExpiresAt <= now, UsesLeft == 0, or Identity != "" && != in.Identity → forbidden "enrollment token is invalid" (one text for all four, never saying which); Identity(in.Identity) missing → CreateIdentity(now) (ErrInvalid → validation: NameRule and the reserved names) and Subscribe(in.Identity, general.ID, MaxSeq, now) (Decisions 59, 77, 92); PublicKeys(in.Identity): none → PutPublicKey{KID, Algorithm, PKIX DER, now}; one whose KID equals → nothing (idempotent); any other → conflict "identity %q already has a credential"; when a token was given: ConsumeEnrollmentToken(id, now) false → forbidden "enrollment token is invalid" (the race loser); commit. No idempotency key: the operation is idempotent for the same key by construction (Milestone 3 pending item 12's reasoning).
func (s Service) RegisterLocal(ctx context.Context, name string) error        // choice 17 (Decision 36): EnsureGeneral; row when missing + general subscription; no key; idempotent; validation on a bad name
func (s Service) CreateIdentity(ctx context.Context, name string) (bool, error) // pending item 5: RegisterLocal's rule, returning whether the row was created (the host command prints it)
func (s Service) MakeAdmin(ctx context.Context, name string, admin bool) error  // RequireIdentity (not_registered "identity %q is not registered; register it first"), SetAdmin; idempotent
func (s Service) ResetCredential(ctx context.Context, name string) (int, error) // RequireIdentity; DeletePublicKeys; the count (0 is success, Decision 58's "only with an admin reset-credential")
func (s Service) ResetCredentialAs(ctx context.Context, admin, name string, key string) (int, error) // RequireAdmin(admin) (Decision 73's API path) then ResetCredential, under WithReceipt(admin, key, payload{name})
func (s Service) PublicKey(ctx context.Context, identity, kid string) (crypto.PublicKey, keys.Algorithm, error) // Store.Read; PublicKeys(identity) filtered by kid; none → not_found "no such key" (the token endpoint turns it into 401)
func (s Service) Reset(ctx context.Context, admin string, key string) error    // choice 12: RequireAdmin; WithReceipt(admin, key); Tx: ResetData; CreateChannel{Name: "general", Kind: ordinary, Creator: "", ACL: cfg.DefaultACL("") or nil}; Subscribe every ListIdentities() row to it at MaxSeq; commit; then Publish(notify.Hint{}) so armed watches re-check
// enrollment.go (choice 9)
type TokenInput struct { Identity string /* "" any free name */; Expires time.Duration /* 0: 24 h */; Uses int /* 0: 1 */ }
func (s Service) CreateEnrollmentToken(ctx context.Context, in TokenInput) (token string, row store.EnrollmentToken, err error) // token "abe_" + RandomToken(32) generated before Tx; ID RandomHex(8); row {ID, HashToken(token), Identity, now + Expires, Uses, now}; Identity validated by the Store (validation); Expires and Uses negative → validation
func (s Service) ListEnrollmentTokens(ctx context.Context) ([]store.EnrollmentToken, error)
func (s Service) RevokeEnrollmentToken(ctx context.Context, id string) error    // not_found
func HashToken(token string) string                                             // sha256 hex
```

- [ ] **Step 1: Write the failing tests**

`service_test.go` (SQLite, `core.New` with a fixed clock, keys from `keys.Generate`): `TestRegisterOpenTOFU` (a free name: `Created true`, the row, one key with the thumbprint `KID` and `Algorithm`, a `general` subscription at `MaxSeq`; the same key again: `Created false`, still one key; another key: `conflict`; `general` exists although nobody called `EnsureGeneral` before), `TestRegisterClosed` (`forbidden`, no row), `TestRegisterEnrollment` (no token → `forbidden`; an unknown token → `forbidden`; `CreateEnrollmentToken{Uses: 1}` then register → 201 shape and `UsesLeft 0`; the same token again → `forbidden` "enrollment token is invalid"; a token bound to `kim` used for `pat` → `forbidden`; an expired token (clock advanced) → `forbidden`), `TestRegisterOpenValidatesSuppliedToken` (open mode with a bogus token → `forbidden`; with a valid one → success and consumed), `TestRegisterRejects` (`a/b`, `everyone`, 129 bytes → `validation`; an RSA key → `validation`; nil key → `validation`; `Mode("")` panics), `TestRegisterLocalAndCreateIdentity` (row and subscription, no key; idempotent; `CreateIdentity` returns true then false), `TestMakeAdmin` (unknown → `not_registered` with "register it first"; true, true again, false), `TestResetCredential` (2 keys → 2; again → 0; unknown → `not_registered`; `ResetCredentialAs` by a non-admin → `forbidden`; with a key replays the count), `TestResetKeepsIdentitiesAndKeys` (`// pending Eric: Milestone 5 item 1`; fixtures through the Store: two identities with keys, an admin, a token, channels with messages, subscriptions, a session, a receipt; `Reset` by the admin: `ListChannels` is `[general]` with the default rows and no creator, every identity is subscribed to it, `ListIdentities` and `PublicKeys` and `ListEnrollmentTokens` unchanged, `MaxSeq` unchanged; by a non-admin → `forbidden` and nothing changed; the same key twice runs once), `TestResetUsesConfiguredDefaultACL` (cfg `DefaultACL` returning `[{everyone,[read]}]`: `general` after `Reset` has that one row), `TestPublicKey` (found; wrong kid → `not_found`; unknown identity → `not_found`), `TestEnrollmentTokenLifecycle` (`abe_` prefix, 47 characters, `HashToken` equals the row; defaults 24 h and 1 use; list; revoke; revoke again `not_found`; `Uses -1` → `validation`). `identities_test.go` (both engines; `h.core`): `TestIdentitiesRegisterConcurrent` (Review Focus 3: eight goroutines register `pat` with distinct keys in open mode: exactly one nil error, seven `conflict`, one key stored; eight register the same key: eight nil; a token with `Uses 1` and eight registers of distinct free names: exactly one success and seven `forbidden`, `UsesLeft 0`), `TestIdentitiesRegisterThenBus` (after `Register`, `h.as("pat")` can `Send` on `general` and `Receive` it through a session: the default policy end to end on both engines).

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/core/identities/ && go test -count=1 -p 1 -run '^TestIdentities' ./internal/conformance/bus/`
Expected: build failure (`undefined: identities.Service`).

- [ ] **Step 3: Implement** as in Interfaces. State (Definition of Done D): a second `Register` with the same key changes nothing; two concurrent registers of one name serialize on the Store transaction (the loser sees the winner's key and answers `conflict`, or on Postgres retries and then sees it); a register whose client died after commit leaves a registered identity with a key the client holds, which it re-registers idempotently; `Reset` twice gives the same empty state; `CreateEnrollmentToken` never reuses an id or hash (random, unique-constrained).

- [ ] **Step 4: Run the tests, the suite on both engines and the gate**

Run: `go test -race ./internal/core/identities/`, then `env -u AGENTBUS2_TEST_POSTGRES_DSN go test -count=1 -p 1 -v -run '^TestIdentities' ./internal/conformance/bus/ 2>&1 | grep -E '^(\s+--- FAIL|ok|FAIL)'`, then `make verify` (600000 ms).
Expected: pass; no `--- FAIL`; `verify: OK`.

- [ ] **Step 5: Commit**

```bash
git add internal/core/identities/service.go internal/core/identities/register.go internal/core/identities/enrollment.go internal/core/identities/admin.go internal/core/identities/service_test.go internal/conformance/bus/identities_test.go
git commit -m "feat(core): identities service: TOFU and enrollment registration, the general subscription for every identity, admin flag, reset-credential and admin reset

Decisions 20, 58, 59, 72, 73, 77, 92 over the Store; Reset ports the wipe
of v1 internal/bus/admin.go keeping identities and credentials (Milestone 5
pending item 1); one rule set for the HTTP handlers and the host commands."
```

---

### Task 6: Server auth: `Bearer`, token, jwks, register, discovery issuers

**Depends on:** 3, 5.

**Files:**
- Delete: `internal/server/auth_stub.go`; Create: `internal/server/auth_bearer.go` (`Bearer`), `internal/server/auth_handlers.go` (`Token`, `Jwks`, `Register`)
- Modify: `internal/server/server.go` (`Options` gains `Registration`, `Issuer`, `External`; `New` builds one `token.Replay`), `internal/server/discovery.go` (`openid-configuration` and `discover` read the configured issuer when set; `discover.issuers` lists `External` URLs), `internal/server/testenv_test.go` (`envOptions` gains `issuer bool` and `registration identities.Mode`; `e.register`, `e.token` helpers)
- Test: `internal/server/auth_test.go`; modify `internal/server/discovery_test.go` (`TestOpenIDConfigurationNamesMilestone5Paths` asserts the configured issuer; `TestDiscoverAgreesWithDocument` asserts `issuers` lists the configured external URLs)

**Interfaces:**
- Consumes: Task 2's `assertion`, `token`, `keys`; Task 5's `identities.Service` (`Register`, `PublicKey`); Task 3's generated names; Milestone 4's `Authenticator`, `WriteProblem`, `env`.
- Produces:

```go
package server
// Options gains:
//   Registration identities.Mode // the mode the register handler enforces; "" is treated as ModeClosed (never open by omission: Definition of Done B)
//   Issuer *token.Issuer         // nil: the token endpoint and JWKS answer 503 unavailable "token issuer not configured" (local mode, Decision 36), and openid-configuration falls back to baseURL(r)
//   External []*token.External   // configured external issuers (Decision 57); their Issuer URLs are what discover.issuers lists (Options.Issuers of Milestone 4 is removed: one source)
type Bearer struct { Verifier token.Verifier; Now func() time.Time } // Authenticator (Milestone 4 choice 5): no Authorization header → "", nil; a header not of the form "Bearer <token>" (case-insensitive scheme, exactly one space) → "", error "malformed Authorization header"; Verifier.Verify error → "", error whose text is "invalid bearer token: <reason>" with the reason one of expired, unknown issuer, unknown key, bad signature, wrong audience, unmapped subject, malformed (never the token); the middleware turns the error into 401 unauthenticated
// handlers (auth_handlers.go), on the api type:
// Token: Issuer nil → unavailable; assertion.Parse(body.ClientAssertion) error → validation "client assertion: <reason>"; identities.Service{core}.PublicKey(p.Identity, p.KID) not_found → unauthenticated "unknown identity or key"; p.Verify(pub, Issuer.TokenEndpoint(), now, token.Leeway) → unauthenticated with the reason; replay.Seen(p.JTI, p.Expiry, now) → unauthenticated "assertion replayed"; Issuer.Mint(p.Identity) → 200 AccessToken{access_token, "Bearer", int64(Lifetime.Seconds())} with Cache-Control: no-store and Pragma: no-cache (set through a response header on the strict response: the implementer uses gen's VisitTokenResponse to add headers, or a middleware keyed on the path; whichever, TestTokenNoStore proves it)
// Jwks: Issuer nil → unavailable; else 200 Issuer.JWKS() marshaled as the JWKS schema
// Register: keys.FromJWK(body.PublicKeyJwk) error → validation "public_key_jwk: <reason>"; identities.Service{core}.Register(Options.Registration, RegisterInput{...}) → 201 Registration{identity, kid, algorithm, created}; errors through the hook (forbidden 403, conflict 409, validation 400)
```

- [ ] **Step 1: Write the failing tests**

`auth_test.go` (`newEnv` with `issuer: true`, which builds `token.NewIssuer("http://inproc", <a test Ed25519 key>, time.Hour, clock.Now)` and `Options{Auth: Bearer{Verifier: token.Verifier{Own: issuer}}}`; identities registered through `e.register(name, key)` which signs nothing and calls the handler with `keys.ToJWK`; `e.token(name, key)` signs an assertion with `assertion.Sign` and exchanges it): `TestRegisterTokenThenCall` (register `pat` in `ModeOpen` → 201 with `kid` = `keys.KID`; token → 200 `access_token`, `token_type Bearer`, `expires_in 3600`; `GET /v1/channels` with `Authorization: Bearer <token>` → 200; without → 401; with `Bearer x.y.z` → 401 problem whose `detail` starts `invalid bearer token`), `TestTokenNoStore` (`Cache-Control: no-store` on the 200), `TestTokenRefusesForeignKeyAndReplay` (Review Focus 2: `kim`'s key signing an assertion with `iss = sub = pat` and `kid` of kim's key → 401 "unknown identity or key"; the same assertion twice → second 401 "assertion replayed"; `aud` of `http://inproc` (the issuer, not the token endpoint) → 401; `exp` 20 min ahead → 401; `HS256` → 400 `validation`; `grant_type=password` → 400 from the validator; a JSON body → 400), `TestBearerRefusals` (Review Focus 1: a token minted by a second issuer with another key and the same URL → 401 unknown key; a bearer for `ghost` minted by the real issuer after `ghost`'s row was never created → 403 `not_registered` from the core, proving the authenticator attaches and the core judges; an expired token on the clock → 401 expired; an assertion used as a bearer → 401), `TestRegisterModes` (`ModeClosed` → 403; `ModeEnrollment` without token → 403, with a token from `identities.Service.CreateEnrollmentToken` → 201; `Options{}` with no `Registration` → 403 (the zero value is closed); a JWK with private material → 400; `identity: "a/b"` → 400; a second key for `pat` → 409), `TestIssuerIsConfiguredNotRequestHost` (`// pending Eric: Milestone 5 item 3`: `openid-configuration` reports `issuer http://inproc`, `token_endpoint http://inproc/v1/auth/token`, `jwks_uri http://inproc/v1/auth/jwks` even when the request carries `Host: other.example`; `GET /v1/auth/jwks` returns one key whose `kid` equals the issuer's; without an issuer (`issuer: false`) `openid-configuration` falls back to `http://other.example` and `jwks` and `token` are 503 `unavailable`), `TestExternalIssuerBearer` (an `httptest.Server` JWKS with a test key; `Options.External` with `Subjects{"sub-1": "pat"}`; a token signed by that key with `iss` the external URL and `aud` `agentbus2` → 200 on `GET /v1/channels`; `sub-2` → 401 unmapped; `aud` wrong → 401; `discover.issuers == [that URL]`), `TestInProcessStillWorks` (`envOptions{}` default `InProcess{}` is untouched: Milestone 4's suite transport is unaffected).

- [ ] **Step 2: Run to verify they fail**

Run: `go test -run 'Register|Token|Bearer|Issuer|External' ./internal/server/`
Expected: build failure (`undefined: Bearer`), then the stub's 500s.

- [ ] **Step 3: Implement**; delete the stub. The `jti` replay set is one `token.NewReplay()` per `New` call, held by `api`.

- [ ] **Step 4: Run the tests, the suite under both transports and the gate**

Run: `go test -race ./internal/server/`, then `AGENTBUS2_BUS_TRANSPORT=inproc go test -count=1 -p 1 ./internal/conformance/bus/` (SQLite and Postgres; the `inproc` transport is untouched), then `make verify` (600000 ms).
Expected: pass; `verify: OK`.

- [ ] **Step 5: Commit**

```bash
git add internal/server/auth_bearer.go internal/server/auth_handlers.go internal/server/server.go internal/server/discovery.go internal/server/testenv_test.go internal/server/auth_test.go internal/server/discovery_test.go
git rm --quiet internal/server/auth_stub.go
git commit -m "feat(server): bearer authentication, the private_key_jwt token endpoint, JWKS, registration and configured external issuers

Decisions 19, 20, 57, 65, 70, 72, 84, 91: the server verifies client
assertions against stored public keys and mints its own bearer JWTs;
registration enforces the mode the command decided; discovery and
openid-configuration name the configured issuer (Milestone 5 choice 7)."
```

---

### Task 7: Admin reset and reset-credential on the wire; local and remote `Admin`; remote register and token

**Depends on:** 3, 5 (runs in Wave 3 beside Task 6: disjoint files).

**Files:**
- Delete: `internal/server/admin_stub.go`, `internal/bus/local/admin_stub.go`, `internal/bus/remote/admin_stub.go`; Create: `internal/server/admin_auth.go` (`AdminReset`, `ResetCredential`), `internal/bus/local/admin_identities.go` (`Reset`, `ResetCredential`), `internal/bus/remote/admin_identities.go` (`Reset`, `ResetCredential`), `internal/bus/remote/auth.go` (`Register`, `Token`, `WithBearer`)
- Test: `internal/server/admin_auth_test.go`, `internal/bus/remote/auth_test.go` (canned `httptest.Server`), `internal/conformance/bus/admin_identities_test.go` (both engines, both transports; prefix `TestAdminIdentities`)

**Interfaces:**
- Consumes: Task 5's `identities.Service` (`Reset`, `ResetCredentialAs`); Task 3's generated names and `bus.ResetInput`, `ResetCredentialInput`, `ResetCredentialResult`; Milestone 4's `remote.errorOf`, `key`, `env`, `newBus`.
- Produces:

```go
// server: AdminReset → identities.Service{core}.Reset(identity, params.IdempotencyKey) → 204; ResetCredential → ResetCredentialAs(identity, pathIdentity, key) → 200 CredentialReset{identity, keys_removed}
// local: Reset → Service.Reset(b.identity, in.IdempotencyKey); ResetCredential → ResetCredentialAs(b.identity, in.Identity, in.IdempotencyKey)
package remote
type RegisterResult struct { Identity, KID string; Algorithm keys.Algorithm; Created bool }
func Register(ctx context.Context, c *gen.ClientWithResponses, identity string, pub crypto.PublicKey, enrollmentToken string) (RegisterResult, error) // keys.KID + ToJWK; the generated register; errors through errorOf
type AccessToken struct { Token string; ExpiresIn time.Duration }
func Token(ctx context.Context, c *gen.ClientWithResponses, clientAssertion string) (AccessToken, error) // TokenWithFormdataBodyWithResponse with grant_type client_credentials and assertion.Type; errors through errorOf
func WithBearer(token string) gen.RequestEditorFn // sets Authorization: Bearer <token>; Milestone 6 passes it to gen.NewClientWithResponses(url, gen.WithRequestEditorFn(WithBearer(tok)))
```

- [ ] **Step 1: Write the failing tests**

`admin_auth_test.go` (`newEnv` with `InProcess{}`; `e.identity("root", true)`, `e.identity("pat", false)` plus a key for `pat` through the Store): `TestAdminResetNeedsAdmin` (as `pat` → 403 `forbidden`; as `root` → 204, then `GET /v1/channels` lists `general` only; the same `Idempotency-Key` twice → 204 twice), `TestResetCredentialWire` (as `pat` → 403; as `root` on `pat` → 200 `{identity: pat, keys_removed: 1}` then `0`; on `ghost` → 403 `not_registered`; with the same key twice → the first count replayed), `TestResetCredentialKeepsMintedToken` (`// pending Eric: Milestone 5 item 4`; this file's env has no issuer, so the proof is at the service level: after `ResetCredentialAs`, `identities.Service.PublicKey` is `not_found` while `Core.RequireIdentity` still succeeds, which is what keeps a minted bearer valid until `exp`; Task 9's exit test asserts the same over TCP with a real token). `remote/auth_test.go`: `TestRemoteRegisterSendsJWK` (the canned server records the body: `identity`, a `public_key_jwk` with `kty OKP`, `kid` equal to `keys.KID`, `enrollment_token` present only when non-empty; a 409 problem → `conflict`), `TestRemoteTokenSendsForm` (`Content-Type: application/x-www-form-urlencoded`, the three fields, `client_assertion_type == assertion.Type`; a 200 body → `Token` and `ExpiresIn 3600s`; a 401 problem → `unauthenticated`), `TestWithBearer`. `admin_identities_test.go` (both engines; under both `AGENTBUS2_BUS_TRANSPORT` values through `newBus`): `TestAdminIdentitiesResetAndResetCredential` (`h.admin("root")` resets: channels are `[general]`; `h.as("pat")` is `forbidden` on both methods; `ResetCredential` on `pat` by `root` returns the key count stored through the Store).

- [ ] **Step 2: Run to verify they fail**

Run: `go test -run 'AdminReset|ResetCredential|Remote' ./internal/server/ ./internal/bus/remote/ && AGENTBUS2_BUS_TRANSPORT=inproc go test -count=1 -p 1 -run '^TestAdminIdentities' ./internal/conformance/bus/`
Expected: the stubs' not-implemented errors.

- [ ] **Step 3: Implement**; delete the three stubs.

- [ ] **Step 4: Run the tests, the suite under both transports and the gate**

Run: `go test -race ./internal/server/ ./internal/bus/remote/`, then `env -u AGENTBUS2_TEST_POSTGRES_DSN go test -count=1 -p 1 -run '^TestAdminIdentities' ./internal/conformance/bus/ && env -u AGENTBUS2_TEST_POSTGRES_DSN AGENTBUS2_BUS_TRANSPORT=inproc go test -count=1 -p 1 -run '^TestAdminIdentities' ./internal/conformance/bus/`, then `go list -deps ./internal/bus/remote | grep -E 'internal/(server|core|gen/spec|auth/token)|kin-openapi' ; echo "exit=$?"` (expected `exit=1`), then `make verify` (600000 ms).
Expected: pass on both engines and both transports; `verify: OK`.

- [ ] **Step 5: Commit**

```bash
git add internal/server/admin_auth.go internal/server/admin_auth_test.go internal/bus/local/admin_identities.go internal/bus/remote/admin_identities.go internal/bus/remote/auth.go internal/bus/remote/auth_test.go internal/conformance/bus/admin_identities_test.go
git rm --quiet internal/server/admin_stub.go internal/bus/local/admin_stub.go internal/bus/remote/admin_stub.go
git commit -m "feat(server): admin reset and reset-credential on both sides of the wire; remote register and token exchange

Decisions 48, 58, 73; the admin section requires the admin flag; the
remote Bus gains the pre-identity register and token functions Milestone 6
builds the client on (Milestone 5 choice 16)."
```

---

### Task 8: Listener classification, `agentbus2 server` with Decision 85, `agentbus2 server admin`, `Serve` wiring

**Depends on:** 4, 6, 7.

**Files:**
- Create: `internal/server/cmd/listen.go` (`ClassifyListen`, `Listen`), `internal/server/cmd/admin.go` (`RunAdmin` and its subcommands), `internal/server/cmd/config.go` (the flag set shared by `server` and `server admin`, `openFromConfig`)
- Modify: `internal/server/cmd/cmd.go` (`Run` dispatches `admin`; the flags of choice 14), `internal/server/cmd/serve.go` (`Config` gains `Registration`, `Issuer`, `External`, `Embedder`, `Listener`; `Serve` listens through `Listen` unless `Listener` is set, prints the mode, wires `Bearer`), `internal/server/cmd/doc.go`, `main.go` (usage lines for `server` and `server admin`), `main_test.go`, `internal/conformance/bus/server_test.go` (Milestone 4's `TestServerServesBothEngines` gains `Registration: identities.ModeOpen` in its `Config` literal)
- Test: modify `internal/server/cmd/cmd_test.go` (`TestRunFlags` rewritten for the new flag set and its `pending Eric: Milestone 4 item 3` marker removed: the interim flags are now Decision 85's flags); create `internal/server/cmd/listen_test.go`, `internal/server/cmd/admin_test.go`, `internal/server/cmd/config_test.go`

**Port from v1:** `main.go` (`run`'s nested subcommand dispatch and `parseExit`, continuing Milestone 4's port) for `server admin <sub>`.

**Interfaces (produces):**

```go
package cmd
func ClassifyListen(listen string) (local bool, err error)   // choice 10: "unix:<path>" → true; host:port split; host "" / "0.0.0.0" / "::" / "[::]" → false; an IP → ip.IsLoopback() (so ::1, 127.0.0.0/8 and ::ffff:127.0.0.1 are true); a hostname → net.DefaultResolver.LookupIPAddr, every address loopback → true, none resolved → error
func Listen(listen string) (net.Listener, error)             // "unix:<path>" → net.Listen("unix", path) after removing a stale socket file nothing answers on (a net.Dial that fails with ECONNREFUSED or ENOENT); else net.Listen("tcp", listen)
// serve.go: Config gains Registration identities.Mode (required: Serve refuses "" with an error), Issuer *token.Issuer (nil allowed: local mode), External []*token.External, Embedder core.Embedder (nil: none; embed.Install when set)
// Serve: open order as Milestone 4 choice 14 plus: Listen (not net.Listen), Ready(addr), server.New(core, Options{Auth: c.Auth, Registration: c.Registration, Issuer: c.Issuer, External: c.External}) ; the listening line printed by Run reads "agentbus2 server listening on <addr> (<engine>, registration <mode>, issuer <url|ephemeral key>)"
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int
// args[0] == "admin" → RunAdmin(ctx, args[1:], stdout, stderr). Else flags: -config PATH, -listen, -registration, -sqlite, -postgres, -set name=value (repeatable; "name=value" split at the first '='), -h. config.Load(path, os.LookupEnv, os.Environ(), overrides) where the named flags fill overrides["listen"] etc. only when set (flag.Visit); a Load error → stderr and exit 2 (a configuration error is a usage error). Then: the store (sqlite.Open / postgres.Open), the signing key (choice 6: config.SigningKeyFile != "" → keys.LoadOrCreateFile(path, keys.EdDSA), printing "generated signing key <path>" when created; else keys.Generate(EdDSA) and the warning "signing key is ephemeral: tokens will not survive a restart (set signing_key_file)"), the registration mode (config.Registration != "" → ParseMode; else ClassifyListen(listen): local → open, else enrollment), the issuer URL (config.Issuer != "" → it; else "http://" + the bound address, which needs the listener first: Run binds through Listen before building the Issuer and passes the listener to Serve through Config.Listener net.Listener /* non-nil: Serve uses it instead of calling Listen */), token.NewIssuer(url, key, lifetime, core clock), External from config.Issuers (token.External with Algorithms parsed from the strings, TTL from JWKSRefreshSeconds), the embedder when EmbeddingEndpoint is set (embed.NewHTTP with the key from EmbeddingAPIKeyFile or EmbeddingAPIKeyEnv), core.Config from config.Core(); Serve with Auth: server.Bearer{Verifier: token.Verifier{Own: issuer, External: ext}}. Exit 0 on a clean shutdown, 1 on an error, 2 on a flag or configuration error.
func RunAdmin(ctx context.Context, args []string, stdout, stderr io.Writer) int
// Decisions 58, 73: the same -config/-sqlite/-postgres/-set flags (no -listen, no -registration), then a subcommand: make-admin NAME [-off] ; reset-credential NAME ; create-identity NAME (pending item 5) ; enrollment create [-identity NAME] [-expires 24h] [-uses 1] ; enrollment list ; enrollment revoke ID. Opens the store with the configuration (no listener, no HTTP, no credential), builds core.New with config.Core() and runs identities.Service: make-admin prints "<name> is now an administrator" (or "is no longer"), reset-credential "<name>: <n> credential(s) removed; the name may be registered again", create-identity "<name> created" or "<name> already exists", enrollment create prints the token once as "token: abe_..." and "id: <id> identity: <name|any> expires: <RFC 3339> uses: <n>", list one line per row "<id> <identity|any> <expires> <uses_left> [expired]" sorted as the Store returns them, revoke "<id> revoked". Errors: an unknown subcommand → exit 2 with the list; not_registered → exit 1 with the service's message; a store open error → exit 1.
```

- [ ] **Step 1: Write the failing tests**

`listen_test.go`: `TestClassifyListen` (Review Focus 5: a table: `127.0.0.1:0`, `[::1]:0`, `[::ffff:127.0.0.1]:0`, `localhost:0`, `unix:/tmp/x.sock` → local; `0.0.0.0:0`, `:0`, `[::]:0`, `192.0.2.1:0`, `example.invalid:0` (error, not local) → not local; `nonsense` → error), `TestListenUnixAndStaleSocket` (`Listen("unix:"+path)` in `t.TempDir()`; a client connects; close; a stale file left behind is removed by the next `Listen`; a live socket is not). `config_test.go`: `TestRunLoadsConfigFileEnvAndFlags` (Review Focus 4 end to end: a file with `listen 127.0.0.1:0`, `sqlite <tmp>`, `token_lifetime_seconds 7200`, `registration closed`; `AGENTBUS2_SERVER_REGISTRATION=enrollment` in the environment (`t.Setenv`); `-set token_lifetime_seconds=60` and `-registration open` on the command line; `Run` in a goroutine with a context canceled after the `listening on` line; that line says `registration open`; a token minted through the real endpoint has `expires_in 60`), `TestRunRejectsConfigErrors` (an unknown key in the file → exit 2 naming it; `-set colour=1` → exit 2; both `-sqlite` and `-postgres` → exit 2 with "exactly one"; a named missing `-config` → exit 2), `TestRunFlags` (rewritten: no flags → exit 2 naming `listen` and the store; `-h` lists every setting name from `config.Settings()`). `admin_test.go`: `TestAdminMakeAdminAndResetCredential` (a SQLite file; `RunAdmin make-admin pat` before `pat` exists → exit 1 "register it first"; after `identities.Service.RegisterLocal` through a core over the same file → exit 0 and the Store shows `Admin true`; `-off` → false; `reset-credential pat` → "0 credential(s)" exit 0; after a key through the Store → "1 credential(s)"), `TestAdminCreateIdentity` (`// pending Eric: Milestone 5 item 5`: created, then "already exists"; the row has a `general` subscription), `TestAdminEnrollmentLifecycle` (create with defaults → the two lines, the token starts `abe_`; list shows it; `-uses 3 -expires 1h -identity kim`; revoke; revoke again → exit 1; an unknown subcommand → exit 2), `TestAdminWorksWithoutServer` (no listener is opened: `RunAdmin` on a file never consults `listen`; a config whose `listen` is nonsense still works), `TestSigningKeyFileLifecycle` (`// pending Eric: Milestone 5 item 2`; Definition of Done D: `Run` with `-set signing_key_file=<tmp>/key.pem`: the first start prints "generated signing key", the file is mode 0600 and PKCS#8; a second start prints nothing of the kind and `GET /v1/auth/jwks` shows the same `kid`; a token minted by the first run verifies on the second; two servers started over the same file at once both start with the same `kid` (`LoadOrCreateFile`'s `O_EXCL` temp file makes the second creator lose and reload); a crash simulated by a leftover `key.pem.tmp-x` file is ignored and the real file wins; without the setting the line says `issuer ... ephemeral key` and a restart changes the `kid`), `TestRegistrationDefaultsByListener` (Review Focus 5: `-listen 0.0.0.0:0` prints `registration enrollment` and `register` through `remote.Register` without a token is 403; `-listen 127.0.0.1:0` prints `open` and the same register is 201; `-listen 127.0.0.1:0 -registration closed` → 403; `-registration tofu` → exit 2). `main_test.go`: `TestUsageListsServerAdmin`.

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/server/cmd/ .`
Expected: build failure (`undefined: ClassifyListen`, `undefined: RunAdmin`).

- [ ] **Step 3: Implement** as in Interfaces. Milestone 4's `TestServeStartsGeneralAndMaintenance`, `TestServeSecondRunAndCrashLeftovers`, `replaced_test.go` and `longpoll_test.go` keep passing with `Config.Registration: identities.ModeOpen` added to their `Config` literals (the one edit to those files, in this task). State (Definition of Done D): `make-admin` and `create-identity` are idempotent; `enrollment create` never collides (random id and token, unique constraints); the signing key file as `TestSigningKeyFileLifecycle`; a `server admin` command running while the server serves is an ordinary concurrent writer on the Store (the server sees the admin flag on the identity's next request: the flag is read per request by `RequireAdmin` and `EffectivePermissions`).

- [ ] **Step 4: Run the tests and the gate**

Run: `make build && ./agentbus2 server 2>&1 | head -3; ./agentbus2 server admin 2>&1 | head -3; go test -race ./internal/server/cmd/ .`, then `make verify` (600000 ms).
Expected: the bare commands print their usage and exit 2; `verify: OK`.

- [ ] **Step 5: Commit**

```bash
git add main.go main_test.go internal/server/cmd/doc.go internal/server/cmd/cmd.go internal/server/cmd/serve.go internal/server/cmd/listen.go internal/server/cmd/admin.go internal/server/cmd/config.go internal/server/cmd/cmd_test.go internal/server/cmd/listen_test.go internal/server/cmd/admin_test.go internal/server/cmd/config_test.go internal/server/cmd/replaced_test.go internal/server/cmd/longpoll_test.go internal/conformance/bus/server_test.go
git commit -m "feat(cli): agentbus2 server configured by file, AGENTBUS2_SERVER_* and flags, listener classification with the enrollment default, the token issuer and signing key, and agentbus2 server admin

Decisions 20, 58, 72, 73, 85 (Milestone 5 choices 6, 7, 10, 14); the
subcommand dispatch continues the port of v1 main.go. Replaces the
Milestone 4 interim flags (its pending item 3)."
```

---

### Task 9: Milestone exit: end-to-end on both engines, negative proofs, docs

**Depends on:** 8.

**Files:**
- Create: `internal/conformance/bus/auth_test.go` (`TestAuthEndToEnd`, both engines, transport-independent), `scripts/m5-exit-checks.sh`
- Modify: `internal/conformance/bus/helpers_test.go` (only if `openStore` does not already expose the DSN it opened), `scripts/check-gates-selftest.sh` (blocks 14 and 15), `.github/workflows/ci.yml` (the `gates-selftest` job gains `- run: bash scripts/m5-exit-checks.sh --deps-only`), `Makefile` (no new target; the `help` comment of `verify` unchanged), `CLAUDE.md`, `README.md`, `docs/adr/0002-authentication-and-credentials.md`, `docs/adr/0003-identity-sessions-and-delivery.md`, `docs/adr/0008-channel-access-control-lists.md` (Milestone 5 addenda), `HANDOFF.md` (untracked)

- [ ] **Step 1: No stub survives**

`rg -n '_stub\.go|NotImplemented' internal/` prints nothing; `rg -n 'pending Eric: Milestone 5' internal/` lists exactly items 1 to 5, one test each; `rg -n 'pending Eric: Milestone 4 item (2|3)' internal/` prints nothing (both closed by this milestone).

- [ ] **Step 2: The exit test**

`auth_test.go`: `TestAuthEndToEnd` under `dbtest.ForEachEngine`, each engine: `openStore` as the suite does; `cmd.Serve` in a goroutine on `Listen("0.0.0.0:0")` (so `ClassifyListen` gives not-local; the test passes `Config{Listener: l, Registration: <the mode ClassifyListen("0.0.0.0:0") decides, asserted to be ModeEnrollment>, Issuer: token.NewIssuer("http://127.0.0.1:<port>", key, time.Hour, now), Auth: server.Bearer{...}, Notifier: notify.NewMemory(), Ready}`); a generated client over `http://127.0.0.1:<port>`; then, in order, each step its own subtest so the log names the clause: (1) `register-without-token-refused`: `remote.Register(pat, patKey, "")` is `forbidden` (the "non-loopback listener without a token" clause, reading one); (2) `register-with-token`: a token from `identities.Service{core}.CreateEnrollmentToken` over a second `core.New` on the same store (the host command's path; `cmd.RunAdmin` is exercised in Task 8, and here the service it calls is used directly so the test does not shell out) → `Register` 201 with `Created true`; (3) `authenticate`: `assertion.Sign` with `pat`'s key against `issuer.TokenEndpoint()` then `remote.Token` → a bearer; (4) `protected-without-bearer-refused`: `GET /v1/channels` without `Authorization` → 401 `unauthenticated` (clause reading two); (5) `protected-with-bearer`: the same with `WithBearer` → 200; (5b) `forged-bearer-refused`: a JWT signed by a test key that is not the server's, carrying a real token's `iss`, `sub`, `aud` and `kid` → 401 (the mutation block 14 target); (6) `admin-refused-as-non-admin`: `remote.New(client with bearer).Status()` → `forbidden`; `Reset` → `forbidden`; `ResetCredential{Identity: "pat"}` → `forbidden`; (7) `make-admin-on-host`: `cmd.RunAdmin(ctx, []string{"-postgres"|"-sqlite", dsn, "make-admin", "root"}, ...)` after registering `root` with a second enrollment token (so Decision 73's command is what grants the flag; for Postgres the DSN is the schema-scoped one the harness opened, which `openStore` exposes, extending `helpers_test.go` in this task if it does not); `root` authenticates and `Status()` succeeds; (8) `acl-refusals-and-successes`: `pat` creates `priv` with ACL `[]` (empty: only the implicit entries); a third identity `kim` (registered with a token) is refused `read` (`Subscribe` and `History` → `forbidden`) and `write` (`Send` → `forbidden`); the creator `pat` cannot read either (`admin` only, Decision 80's discrete sets: `History` → `forbidden`) but holds `admin`: `SetACL([{pat,[read,write]}, {kim,[read]}])` succeeds, after which `pat` sends and reads, `kim` reads (`History` returns the message) and is still refused `write`; the administrator `root` with no entry holds `admin`: `ACL("priv")` succeeds and `SetACL` adding `{root,[read,write]}` then `Send` succeeds; a channel created by `pat` without an ACL carries the Decision 88 default: `kim` sends and reads it; (9) `reset-credential-keeps-token`: `root` calls `ResetCredential{pat}` → `KeysRemoved 1`; `pat`'s bearer still lists channels (pending item 4); `pat`'s next `remote.Token` with its old key → `unauthenticated`; `remote.Register(pat, newKey, token3)` → 201 `Created false`; the new key's assertion → a token; The `-registration=closed` override is Task 8's. Cancel; `Serve` returns within 5 s. Expected on both engines: all ten subtests `--- PASS`.

- [ ] **Step 3: The exit script**

`scripts/m5-exit-checks.sh` (`#!/usr/bin/env bash`, `set -euo pipefail`): usage `m5-exit-checks.sh <auth.log>` or `--deps-only`. The log form refuses a missing or empty log (exit 2 naming the path) and prints `auth_sqlite=<n>` and `auth_postgres=<n>` counting `^\s+--- PASS: TestAuthEndToEnd/<engine>/` leaf lines and `fail=<n>` for `^\s+--- FAIL`, exiting 1 unless `fail=0` and both counts are exactly 10; then the import check. `--deps-only`: `go list -deps` (no `-test`) for `./internal/auth/...` (forbidden: `internal/store`, `internal/core`, `internal/server`, `internal/gen`, `internal/bus`), `./internal/bus/remote` (forbidden: `internal/auth/token`), `./internal/core/...` and `./internal/bus/local` (forbidden: `internal/server`, `internal/gen`), matching in-module packages with `grep -F -x` as `m4-exit-checks.sh` does; `deps_ok=1` or `deps_violation=<package> imports <forbidden>` and exit 1.

- [ ] **Step 4: The milestone exit, positive**

Run (600000 ms or in the background): `env -u AGENTBUS2_TEST_POSTGRES_DSN make verify 2>&1 | tee /tmp/agentbus2-m5-verify.log | tail -3`; `env -u AGENTBUS2_TEST_POSTGRES_DSN make test-race 2>&1 | tail -4`; `env -u AGENTBUS2_TEST_POSTGRES_DSN go test -count=1 -p 1 -v -run '^TestAuthEndToEnd$' ./internal/conformance/bus/ > /tmp/agentbus2-m5-auth.log 2>&1; bash scripts/m5-exit-checks.sh /tmp/agentbus2-m5-auth.log`; `bash scripts/m4-exit-checks.sh --deps-only`.
Expected, quoted verbatim in the report: `verify: OK`; `test-race` `ok` everywhere; `fail=0`, `auth_sqlite=10`, `auth_postgres=10`, `deps_ok=1`, exit 0; `deps_ok=1` from the Milestone 4 script too.

- [ ] **Step 5a: Prove the exit script can fail** (Definition of Done C)

In a throwaway worktree of `HEAD`: (1) `internal/auth/keys/probe_m5.go` with `import _ "github.com/ericfitz/agentbus2/internal/store"`; `bash scripts/m5-exit-checks.sh --deps-only` → exit 1 and `deps_violation=github.com/ericfitz/agentbus2/internal/auth/keys imports github.com/ericfitz/agentbus2/internal/store`; remove. (2) `internal/bus/remote/probe_m5.go` importing `internal/auth/token` → exit 1 naming it; remove. (3) the script on `/tmp/does-not-exist.log` and on an empty file → exit 2 naming the path. (4) a copy of the real auth log with one `--- PASS: TestAuthEndToEnd/sqlite/authenticate` line deleted → `auth_sqlite=9`, exit 1. Quote the four outputs.

- [ ] **Step 5b: The milestone exit, negative halves** (two blocks of `scripts/check-gates-selftest.sh` after Milestone 4's block 13; Docker-free: both run `go test` on the SQLite half of the exit test through `-run '^TestAuthEndToEnd$/sqlite'`)

Block 14 `bearer-signature-ignored`: in the worktree replace, in `internal/auth/token/issuer.go`, the one line `if err := tok.Claims(i.key.Public(), &c); err != nil {` (the implementer keeps it verbatim on one line) with `if err := tok.UnsafeClaimsWithoutVerification(&c); err != nil {` through a temp file and `cp` (`problem` if `grep -c 'UnsafeClaimsWithoutVerification(&c)'` is not 1); run `go test -count=1 -p 1 -v -run '^TestAuthEndToEnd$/sqlite' ./internal/conformance/bus/` and expect `--- FAIL: TestAuthEndToEnd/sqlite/forged-bearer-refused` (subtest 5b; the no-token subtest cannot catch this mutation because it sends nothing to verify); revert. Block 15 `admin-flag-ignored`: replace in `internal/core/authz.go` the line `if !id.Admin {` of `RequireAdmin` (verbatim on one line since Milestone 3) with `if false {` (`problem` if absent after the edit); run the same; expect `--- FAIL: TestAuthEndToEnd/sqlite/admin-refused-as-non-admin`; revert. Run after Step 7's commit: `make check-gates-selftest 2>&1 | tail -22`. Expected: `selftest: ok bearer-signature-ignored`, `selftest: ok admin-flag-ignored`, every earlier block `ok`.

- [ ] **Step 6: Docs**

- `CLAUDE.md` Commands: `agentbus2 server -config FILE [-listen] [-registration] [-sqlite|-postgres] [-set name=value]`, `agentbus2 server admin ...` (the six subcommands), `AGENTBUS2_SERVER_*`, `scripts/m5-exit-checks.sh`. Design: one line each for `internal/auth` (a leaf; the import rule), `internal/core/identities` (registration and admin rules; the host commands and handlers both call it), `internal/server/config` (Decision 85; `config.Settings()` is the list), the signing key file and the ephemeral default, and that `make verify` now covers schema version 2 on both engines. Facts only, checked against the tree.
- `README.md`: `Status: Milestone 5 (authentication, registration, policy) done; a server with enrollment or TOFU registration is usable from a hand-written client; the CLI and MCP server are Milestone 6.`
- ADR 0002 addendum `## Milestone 5 addendum (date)`: choices 1 (the library and the candidates), 2, 3 (the OAuth error-format departure), 4, 5, 6, 7, 8, 9, 10, 11, 13, 14, 17 and pending items 2, 3, 4, 5 with their test markers; the closure of Milestone 4 pending items 2 and 3. ADR 0003 addendum: Decision 77's mechanism (`Register`, `RegisterLocal` and `CreateIdentity` subscribe to `general`; `Reset` re-subscribes everyone), choice 12 and pending item 1. ADR 0008 addendum: the end-to-end ACL proof over TCP with real bearers (subtest 8), the configured `default_acl` with its placeholder, `channel_creation` as a setting.
- `HANDOFF.md` via `/session:handoff`: Milestone 5 done at the Step 7 commit, unpushed; the Step 4, 5a and 5b outputs; the pending items; Milestones 6 and 7 next.

- [ ] **Step 7: Commit**

```bash
git add internal/conformance/bus/auth_test.go internal/conformance/bus/helpers_test.go scripts/m5-exit-checks.sh scripts/check-gates-selftest.sh .github/workflows/ci.yml CLAUDE.md README.md docs/adr/0002-authentication-and-credentials.md docs/adr/0003-identity-sessions-and-delivery.md docs/adr/0008-channel-access-control-lists.md
git commit -m "feat(server): Milestone 5 exit: registration, authentication, admin and ACL refusals and successes end to end on both engines over TCP; the forged-bearer and admin-flag mutation proofs; docs"
```

- [ ] **Step 8: Report**

End with the Step 4 outputs, the Step 5a and 5b outputs quoted verbatim, and the pending items still open. No push: the next push is Eric's (`Approve: push main to origin (git push origin main)`).

---

## Self-review notes

- Spec coverage, section 10 Milestone 5: key pairs (Task 2 `keys`, Decision 55), `private_key_jwt` (Task 2 `assertion`, Task 6 `Token`, Decisions 19, 70), `POST /v1/auth/token` with the server as issuer and the lifetime setting (Tasks 2, 3, 4, 6, Decisions 70, 84), JWKS and bearer validation at every call (Tasks 2, 6, Decision 57), external issuers (Tasks 2, 4, 6, Decision 57), TOFU and enrollment with `enrollment` as the non-loopback default and the override (Tasks 1, 5, 6, 8, Decisions 20, 58, 72), listener classification (Task 8, Decision 58), the default policy for every identity with the `general` subscription (Task 5, Decisions 59, 77, 92), the default ACL and creator admin end to end (Tasks 4, 9, Decisions 80, 88), the open creation switch as a setting (Task 4, Decision 93), `agentbus2 server admin` without a credential (Task 8, Decisions 58, 73), the admin section requiring the flag with reset and reset-credential (Tasks 3, 5, 7, Decision 48), server configuration (Tasks 4, 8, Decision 85), local mode without authentication (choice 17, `InProcess` untouched, Decision 36). Exit criterion: Task 9's `TestAuthEndToEnd` on both engines under `make verify` with the negative halves in blocks 14 and 15 and `scripts/m5-exit-checks.sh`.
- Decisions traced: 19, 20, 22, 36, 55, 57, 58, 70, 72, 73, 84, 85 (ADR 0002); 8, 14, 59, 73, 77, 86 (ADR 0003); 26, 34, 48, 53, 54 (ADR 0004); 64, 65, 91 (ADR 0007); 80, 88, 89, 92, 93, 96 (ADR 0008); 31 (the v1 sources named in Tasks 1, 4, 5, 8).
- Names used across tasks: `store.EnrollmentToken`, `EnrollmentTokens`, `ResetData` (Task 1); `keys.Algorithm`, `Generate`, `AlgorithmOf`, `KID`, `ToJWK`, `FromJWK`, `MarshalPublic`, `ParsePublic`, `EncodePrivatePEM`, `ParsePrivatePEM`, `WriteFile`, `LoadOrCreateFile`; `assertion.Sign`, `Parse`, `Parsed.Verify`, `Type`, `MaxLifetime`; `token.Issuer`, `NewIssuer`, `Mint`, `Verify`, `JWKS`, `TokenEndpoint`, `JWKSURI`, `Replay`, `External`, `Verifier`, `IssuerOf`, `Leeway` (Task 2); the five `operationId`s, `bus.ResetInput`, `ResetCredentialInput`, `ResetCredentialResult`, `Admin.Reset`, `Admin.ResetCredential` (Task 3); `config.Config`, `Default`, `Settings`, `EnvName`, `Overrides`, `Load`, `Validate`, `Core`, `ACLEntry`, `CreatorPlaceholder`, `DefaultACLFunc`, `ValidateDefaultACL` (Task 4); `identities.Mode`, `ModeOpen`, `ModeEnrollment`, `ModeClosed`, `ParseMode`, `Service`, `Register`, `RegisterLocal`, `CreateIdentity`, `MakeAdmin`, `ResetCredential`, `ResetCredentialAs`, `PublicKey`, `Reset`, `TokenInput`, `CreateEnrollmentToken`, `ListEnrollmentTokens`, `RevokeEnrollmentToken`, `HashToken` (Task 5); `server.Bearer`, `Options.Registration`, `Options.Issuer`, `Options.External` (Task 6); `remote.Register`, `Token`, `WithBearer`, `AccessToken`, `RegisterResult` (Task 7); `cmd.ClassifyListen`, `Listen`, `RunAdmin`, `Config.Registration`, `Config.Issuer`, `Config.External`, `Config.Embedder`, `Config.Listener` (Task 8); `scripts/m5-exit-checks.sh` (Task 9).
- Type consistency: `keys.Algorithm` values equal `store.PublicKey.Algorithm`'s two strings and the document's `algorithm` enum; the public key is `crypto.PublicKey` in Go, PKIX DER in the Store, a JWK on the wire; `identities.Mode` is the `-registration` value and `Options.Registration`; the token endpoint string is `Issuer.TokenEndpoint()` on both the signing and the verifying side; `token_lifetime_seconds` becomes `time.Duration` in `NewIssuer` and `int64` seconds in `expires_in`.
- Review Focus: 1 in Tasks 2 and 6, 2 in Tasks 2 and 6, 3 in Task 5, 4 in Tasks 4 and 8, 5 in Tasks 8 and 9.
- Parallelism: Wave 1 holds four tasks with disjoint trees (Store; `internal/auth` and `go.mod`; `api/`, `internal/gen`, `internal/bus/admin.go`, the stubs, `discovery_test.go` and `cmd_test.go`; `internal/server/config`); Wave 3 holds Tasks 6 and 7 (`auth_*.go`, `server.go`, `discovery*.go`, `testenv_test.go`, `auth_test.go` against `admin_auth*.go`, `internal/bus/local`, `internal/bus/remote`, `admin_identities_test.go`); Task 8 alone edits `internal/server/cmd` after Task 3's count edit; Task 9 alone edits the scripts, CI and docs.
- Proportion: the plan fixes the library, the wire shapes, the claim rules, the Store rows, the configuration surface, the registration and reset semantics, the command grammar and the exit mechanics; handler, service and command bodies are the implementer's.
- Checked against the tree on 2026-10-21: `internal/store/identities.go` (`PublicKey{Identity, KID, Algorithm, Key, CreatedAt}`, `PutPublicKey`, `PublicKeys`, `DeletePublicKeys`, `SetAdmin`, the reserved names), `internal/store/names.go` (`NameRule`), `internal/store/acl.go` (`DefaultACL`, `ValidateEntry`), the two `0001_initial.sql` files (`identity_keys` with the algorithm `CHECK`, cascades from `channels` and `identities`), `internal/store/sqlite/migrations.go` and `internal/store/postgres/migrations.go` (one step each), `internal/store/sqlstore/dialect.go` `Options{Migrations, TxAttempts, Now}`, `internal/store/meta.go` (`Meta` with `TryLease` and notices), the open tests' literal version pins, `Makefile`, `scripts/check-gates.sh` and the `check-gates-selftest.sh` helpers and `CHECKS` array, `.github/workflows/ci.yml` (the `gates-selftest` job), `main.go` (`run` with `help` and `version` only: Milestone 4's `server` case is assumed), v1 `main.go`, `internal/config/config.go`, `internal/cli/reset.go`, `internal/bus/admin.go` and `internal/bus/sessions.go` (`NameRule`); Milestones 3 and 4 are not on `main` (`internal/core`, `internal/server`, `internal/bus/remote` absent), so every name of theirs above is assumed from their plans and diffed before Wave 2.
