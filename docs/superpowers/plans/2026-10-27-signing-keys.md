# Signing Keys Implementation Plan (issues #6 rotation and #8 key providers; Eric's decisions of 2026-10-27)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The server's single signing-key row becomes a key set (active and retiring keys, each with its own `kid`), rotated on a schedule, by a host command and by an admin API operation (with a cooldown and an emergency `-revoke-previous`), published whole in the JWKS and verified by `kid` on every replica; the private part can be held by the Store (default, Ed25519) or by an external key service (AWS KMS, Google Cloud KMS, Azure Key Vault, HashiCorp Vault Transit, ES256), chosen by a key URI and linked only under a build tag.

**Architecture:** The Store is the registry for every provider (decision 2): `signing_keys` rows carry state, provider, the resolved provider reference and the SPKI public key; a partial unique index allows one active row. A new leaf `internal/keyprovider` is the port (`Next` outside every transaction, `Signer` for a recorded key) with the Store-row provider built in; `internal/core/signingkeys` holds the rules (ensure-active at start, rotate with the cooldown inside the transaction, scheduled rotation, removal, the `Keyring` that feeds the issuer). `token.Issuer` takes a `KeySource`: it signs with the active key through a context-aware signer, verifies by `kid` against every key, and reloads on an unknown `kid` (at most every 5 s, single-flight) and every 30 s. Each external backend is a thin adapter package over sigstore's per-backend module; only the `internal/server/cmd` file that registers it carries the backend's build tag.

**Tech Stack:** Go 1.27 (`CGO_ENABLED=0`), go-jose v4.1.6 (`cryptosigner`), modernc SQLite and pgx Postgres over `sqlstore`, testcontainers, kin-openapi + oapi-codegen (`make generate`); new modules (Tasks 10 to 13 only): `github.com/sigstore/sigstore/pkg/signature/kms/{aws,hashivault,gcp,azure}` v1.11.1 (each its own Go module) and the SDKs they require.

**Spec:** `docs/superpowers/specs/2026-10-27-signing-keys-design.md` (sections 4 and 5 are Eric's decisions; its section 7 is the provisional outline this plan replaces), read with ADR 0002 (items 6, Milestone 5 item 2, "Replicas share the issuer and the signing key"), ADR 0004 (Milestone 5 addendum), ADR 0011, `docs/ops/deploy.md` "The signing key".

**Eric's decisions of 2026-10-27 this plan implements** (brief section 5; recorded by Task 14 in ADR 0012):
1. External keys are chosen by key URI (`awskms://`, `gcpkms://`, `azurekms://`, `hashivault://`) over sigstore's CGO-free modules; no URI = the Store-row provider; ES256 whenever the key is held externally, Ed25519 for the Store-row key.
2. The Store is the registry for every provider; the provider holds only the private part.
3. The configured key URI (or an alias in it) is resolved at each rotation and the resolved reference is recorded in the row; replicas sign by the recorded reference.
4. Replicas reload on an unknown `kid` (single-flight, at most once per 5 s) plus every 30 s.
5. `remove_after = retired_at + 30 s + 86400 s + 60 s` (the configured lifetime ceiling).
6. Rotate now is a host command and an admin API operation (`Bus.Admin`, client CLI), in protocol `1.0.0`.
7. `-revoke-previous` deletes the previous active key in the rotation transaction; the cooldown never refuses it.
8. Two states, `active` and `retiring`.
9. A configured provider that differs from the active row's refuses the start; the operator rotates explicitly.
10. A rotation inside the cooldown (`signing_key_rotation_cooldown_seconds`, default 3600) is refused with `conflict`, checked inside the rotation transaction, no extra lock.
11. Scheduled rotation every 720 hours by default; a minimum of 48 hours.
12. Each backend behind a build tag (`awskms`, `gcpkms`, `azurekms`, `hashivault`); all four in this delivery; the default binary stays lean.

**The brief's "Changes to existing decisions" (brief lines 278-284) are not individually marked DECIDED.** Each is a consequence of a decision above, not a new one, and this plan treats them as approved on that basis; Eric confirms with one line before Wave 1: (1) the singleton becomes a key set: decision 8 and issue #6; (2) the new settings `signing_key_uri`, `signing_key_rotation_hours`, `signing_key_rotation_cooldown_seconds`: decisions 1, 11 and 10 (10 names the cooldown setting); (3) "database read access equals the administrator flag" holds only for the Store-row provider: decision 1 and issue #8 acceptance criterion 8; (4) the external SDK dependencies behind tags: decisions 1 and 12 (the new import rule enforcing it is an agent choice, Task 9); (5) the wire addition in `1.0.0`: decision 6 and Eric's "Protocol version will be 1.0.0, end of discussion".

## Settled open notes

Each is reversible with one findable edit: `rg -n 'open note: signing-keys' .`

1. **Release archive build tags** (the brief's one open note): `release/build-archive.sh` builds with no tag; the Docker image (`Dockerfile`, the server deployment target) builds with all four through `ARG BUILD_TAGS=awskms,gcpkms,azurekms,hashivault` passed as `-tags "${BUILD_TAGS}"`. Rationale: the archives are the brew, scoop and winget installs that run agents, the CLI and the TUI, where a KMS SDK is dead weight (decision 12's "lean"); the image is what `docs/ops/deploy.md` deploys, so every backend works there without a rebuild. Marker in both files (Task 14).
2. **Where the build tag sits**: the adapter packages `internal/keyprovider/<backend>` carry no tag; only `internal/server/cmd/backend_<backend>.go` (the `init` that registers the backend) does. Rationale: `make build-all`, `vet`, `lint`, `test`, `vet-cross` and `windows.yml` then compile and test every adapter on every GOOS with no tag matrix, while the binary links an SDK only when `main` reaches the tagged file. (The brief's outline put every adapter file under its tag; that hides the adapters from every untagged gate, including the m7 importer scan at `scripts/m7-exit-checks.sh:131`.) Marker in `internal/server/cmd/keyprovider.go` (Task 5).
3. **Azure test coverage**: sigstore's Azure `LoadSignerVerifier(ctx, ref)` (`azure@v1.11.1/signer.go:59`) builds its own credential and `azkeys` client with no options (`client.go:110-130`), so its sign path cannot be pointed at a fake. The adapter resolves the URI with its own `azkeys.Client` (transport injectable, tested against `httptest`), signs through sigstore by the pinned URI behind an unexported `signFunc` seam the unit test replaces, and the sigstore sign path is covered by the opt-in live test only. Rationale: keeps decision 1's "thin adapter over the sigstore module" and still tests everything this repository writes. Marker in `internal/keyprovider/azurekms/azurekms_test.go` (Task 13). Eric may prefer signing through `azkeys` directly (a departure from decision 1 for Azure).
4. **Scheduled rotation with an external provider** resolves the key URI on every maintenance tick while `signing_key_rotation_hours > 0` (two read calls per tick for AWS); `0` turns scheduled rotation off for every provider, and then only `rotate` adopts a repointed URI. Rationale: no rate-limit state; a repointed alias is adopted within one tick. Marker in `internal/core/signingkeys/schedule_test.go` (Task 4).
5. **The two maintenance jobs** exist only when the server wires the port (`maintenance.Service.SigningKeys != nil`); local mode (`agentbus2 mcp`, `tui`) has neither. They sit before `"assertion replay expiry"`, which stays the last job (`internal/conformance/bus/replay_test.go:36`). Marker in `internal/core/maintenance/jobs_test.go` (Task 4).
6. **Bounds**: `signing_key_rotation_hours` is 0 or 48 to 8760; `signing_key_rotation_cooldown_seconds` is 1 to 86400 (always below the 48-hour interval minimum, so a cooldown never delays a scheduled rotation). Marker in `internal/server/config/config_test.go` (Task 5).
7. **External sign timeout**: `Issuer.Mint` bounds the sign call with `token.SignTimeout = 10 * time.Second` on the request context. sigstore's Vault sign path calls `Logical().Write` without a context (`hashivault@v1.11.1/client.go:342`), so the Vault adapter returns `ctx.Err()` on `ctx.Done()` and leaves the call to finish under the Vault client's own timeout (`VAULT_CLIENT_TIMEOUT`, documented). Marker in `internal/auth/token/issuer_test.go` (Task 2).
8. **Visibility of a stale external key and of a provider mismatch**: the lock holder's `"signing key rotation"` job returns them as its error (so health shows them); every replica's `Keyring` logs a warning on each reload that finds them. A replica that is not the lock holder shows them in its log only. Marker in `internal/core/signingkeys/keyring_test.go` (Task 3).
9. **Revoke propagation in the replica test** polls the other replica for up to `token.ReloadInterval + 5*time.Second` (no test hook for the reload interval; about 30 s per engine at worst). Marker in `internal/conformance/bus/replicas_test.go` (Task 8).

**Agent choices** (not Eric's; one constant or one line each): the denial texts below; the first-start line `adopted signing key <kid> (<provider> <key name>)` (the key name is the URI without user info, query or fragment, `keyprovider.KeyName`; the URI itself is never printed) for an external key beside today's `generated signing key <kid>`; `UnknownKeyReloadInterval = 5 s` and `ReloadInterval = 30 s` as `token` constants; the host `list` output format; `MaxBearerLifetime` pinned to the configuration ceiling by a test.

## Facts checked in the module cache (sigstore v1.11.1, 2026-10-27)

The four backends are separate modules (`pkg/signature/kms/<b>/go.mod`, each `go 1.27`, each tagged v1.11.1). Never call `kms.Get`: with no registered prefix it execs a `sigstore-kms-*` plugin (`kms.go:78`, `cliplugin`); call each backend's `LoadSignerVerifier`. Every backend's `SignMessage` accepts `options.WithDigest` and `options.WithContext` and returns ASN.1 DER for P-256, which is what go-jose's `cryptosigner` parses (`cryptosigner.go:106-135`); so a `keyprovider.Signer` returns DER for ES256 and raw 64 bytes for EdDSA, exactly like `crypto.Signer`. Do not use sigstore's `CryptoSigner`: its `Public()` asks the backend on every call, and `cryptosigner.Opaque` calls `Public()`.

| Backend | URI grammar (sigstore regexp) | Resolution to a pinned key (Next) | Sign request | Context | Hermetic test |
|---|---|---|---|---|---|
| awskms | `awskms://[endpoint]/{key-id\|key-ARN\|alias/NAME\|alias-ARN}` (`aws client.go:79-82`) | `DescribeKey` on the URI's id or alias → `KeyMetadata.Arn`, `GetPublicKey` on the ARN; pinned ref = the full URI `awskms://[endpoint]/<key ARN>` (the endpoint lives only in the URI) | `Sign`, `MessageType: DIGEST`, algorithm = the key's first (`ECDSA_SHA_256`) (`client.go:321-333`) | passed | `httptest.NewTLSServer` speaking AWS JSON 1.1; the endpoint is forced to `https://` (`client.go:148`), so pass `config.WithHTTPClient(srv.Client())` and static credentials through `LoadSignerVerifier`'s options (`signer.go:53`) |
| hashivault | `hashivault://NAME` (`client.go:61`); address `VAULT_ADDR`, token `VAULT_TOKEN`, mount `TRANSIT_SECRET_ENGINE_PATH` (default `transit`) | read `transit/keys/NAME` → `latest_version` and its public key; pinned ref = `hashivault://NAME` plus the version, passed as `options.WithKeyVersion` | `transit/sign/NAME/sha2-256`, `prehashed: true` | not passed (`client.go:342`) | `httptest.NewServer` (plain http) |
| gcpkms | `gcpkms://projects/P/locations/L/keyRings/R/cryptoKeys/K[/cryptoKeyVersions/V]` (`client.go:141`) | no version → newest `ENABLED` version (`client.go:175-213`); pinned ref = the URI with `/cryptoKeyVersions/V` | `AsymmetricSign` with `Digest_Sha256` (`client.go:347-384`) | passed | in-process gRPC `kmspb.KeyManagementServiceServer` on `127.0.0.1:0` with `option.WithEndpoint`, `option.WithoutAuthentication`, insecure transport credentials |
| azurekms | `azurekms://VAULT-HOST/NAME[/version]` (`client.go:71`) | `azkeys.GetKey(name, "")` → the key's `kid` carries the version; pinned ref = `azurekms://HOST/NAME/VERSION` | `Sign` ES256 on the digest; raw `r\|\|s` converted to DER by sigstore (`signer.go:118`) | passed | resolution only (settled note 3) |

## Task dependencies and parallelism

Parallel tasks run in separate worktrees and are cherry-picked onto `main` (the agent's own commits only: `git merge-base <branch> main`); every pair that runs side by side has disjoint `Files:` lists. The backend tasks (10 to 13) each edit `go.mod` and `go.sum`, so they run one after another and never need a conflict resolution.

| Task | Depends on | Wave |
|---|---|---|
| 1 Store registry | none | 1 |
| 2 Key-set issuer | none | 1 |
| 3 Port, Store-row provider, rules, keyring | 1, 2 | 2 |
| 4 Maintenance jobs | 3 | 3 |
| 5 Configuration and server wiring | 4 | 4 |
| 6 Host command | 5 | 5 |
| 9 External provider scaffolding | 5 | 5 |
| 7 Admin API, Bus, CLI | 6 | 6 |
| 10 awskms | 9 | 6 |
| 8 Replica and end-to-end tests | 6, 7 | 7 |
| 11 hashivault | 10 | 7 |
| 12 gcpkms | 11 | 8 |
| 13 azurekms | 12 | 9 |
| 14 Records, release tags | 8, 13 | 10 |

Waves: 1 and 2 in parallel (Task 1 touches the Store and its callers, Task 2 `internal/auth/token` and its callers; `internal/conformance/bus/auth_test.go` belongs to Task 2 alone, Task 1 leaves `LoadOrCreateSigningKey`'s signature unchanged); then 3, 4, 5 in order; then 6 beside 9 (6 owns `internal/server/cmd/admin.go` and the host section of `docs/ops/admin-bootstrap.md`; 9 owns `internal/keyprovider/kmsfake`, the CI and gate files); then 7 beside 10 (7 depends on 6 because both edit `admin-bootstrap.md`); then 8 beside 11; then 12, 13, 14. Every `CLAUDE.md` edit is in Task 14, so no parallel pair shares it.

## Global Constraints

- Done gate per task: `make verify` (needs Docker; Postgres from testcontainers unless `AGENTBUS2_TEST_POSTGRES_DSN` names a reachable server; a run that skips the Postgres suites is not a pass). Show its output in the report. Work is done only when `ci.yml` and `windows.yml` are green on the pushed commit (the `release-check` job included); a red `windows.yml` is fixed first. No push without Eric.
- Pre-release rule (Eric, 2026-10-25): no new migrations; `signing_keys` changes in place in `internal/store/sqlite/schema/0001_initial.sql:171-177` and `internal/store/postgres/schema/0001_initial.sql:168-174`; the schema version stays 1; no compatibility layers, shims, converters or deprecation paths. Local databases are recreated.
- Protocol: `info.version` and `internal/version.Protocol` stay `1.0.0` (Eric 2026-10-27). `api/openapi.yaml` changes only in Task 7, followed by `make generate`; `internal/gen` and `internal/gen/spec` are never edited by hand; `make generate-check` is clean.
- Decision 26: `Provider.Next` (key generation, any external call) runs before and outside every `Store.Tx`; the transaction function has no side effects and may run more than once (Postgres retries serialization failures 5 times, `internal/store/postgres/postgres.go:25-27`); `OnChange` (the issuer's `Invalidate`) and log lines run after `Tx` returns nil.
- Access: the admin check is `core.RequireServerAdmin` (`internal/core/lookup.go:16`, which calls `access.Require(..., access.Server{}, access.Administer)`); no code reads `Admin`, `Creator`, `Owner`, `Scope` or a `Kind` outside `internal/core/access`, so `TestAccessRuleSites` (`internal/core/access/sites_test.go`) gains no entry and its counts do not change. `if !p.Admin {` stays once in `decide.go`.
- Import rules: any task that adds an import runs `bash scripts/m4-exit-checks.sh --deps-only && bash scripts/m5-exit-checks.sh --deps-only && bash scripts/m6-exit-checks.sh --deps-only && bash scripts/m7-exit-checks.sh --deps-only && bash scripts/m8-exit-checks.sh --deps-only` (all print `deps_ok=1`). `internal/auth/...` gains no edge (`internal/auth/doc.go:5-9`, `m5-exit-checks.sh:92-94`). New edges: `internal/core/signingkeys` imports `internal/keyprovider`, `internal/auth/keys` and `internal/auth/token`; `internal/core/maintenance` imports `internal/core/signingkeys`; `internal/server/cmd` imports `internal/core/signingkeys` and `internal/keyprovider` (and each backend package from its tagged file only); `internal/server/config` imports `internal/keyprovider`. Task 9 adds the keyprovider rule to `m7-exit-checks.sh` with mutations in `scripts/check-gates-selftest.sh` `block_m7_deps_rule_fires`.
- Pinned counts: `TestACLMatrix` 15 rows, 52 leaves per engine (unchanged: no matrix row is added, see Task 7); MCP tools 25 (unchanged); `internal/server/cmd/cmd_test.go:432` 44 protected and 8 open becomes 46 and 8 in Task 7; `TestAuthEndToEnd` 11 leaves per engine becomes 12 in Task 8 (`scripts/m5-exit-checks.sh:31` `WANT=12`); `TestReplicasWake` and `TestReplicasMaintenanceExclusive` counts unchanged (`m7-exit-checks.sh:187-190`); e2e 13 steps and 11 exit-code cells unchanged.
- Build tags: Task 9 adds `make vet-tags` (`go vet -tags $t ./...` and `go build -tags $t -o /dev/null .` for each of the four tags and for all four together, for linux and windows), called from `vet-cross`; `ci.yml` gains a `signing-key-tags` job running `make vet-tags` and `go test -tags awskms,gcpkms,azurekms,hashivault ./internal/server/cmd/ ./internal/keyprovider/...`; `windows.yml` gains `go vet -tags awskms,gcpkms,azurekms,hashivault ./...`. `go mod tidy` considers every tag, so `tidy-check` covers the tagged imports unchanged.
- Docs move with the behavior in the task that changes it (each task's `Files:` names them); Task 14 is the records sweep (ADR 0012, the gate-0 pointer, release tags). Check every factual sentence against the committed code (Definition of Done E). American English. No agent-facing text (MCP descriptions, `SKILL.md`) changes, so the m6 and m8 word checks are untouched; run them anyway in Task 7.
- Each task: failing tests first, an exact `git add` list (never `-A`), one commit on `main` ending with:
  ```
  Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_011fnCFu154ZJG5c5ro7rrqs
  ```
- Definition of Done D in every stateful task: its commit body states what a second run, two overlapping runs and a failed run's leftovers do, and the task's tests cover each that can be tested.
- Definition of Done B: every new enum (state, provider, key URI scheme, algorithm of an external key) rejects unknown values with a test; no placeholder branch accepts input. Definition of Done F: anything beyond a one-liner goes in a script with `set -euo pipefail`; logs go to `${TMPDIR:-/tmp}`.

## Review Focus

1. Rotate now with an external provider whose URI still resolves to the active key (the operator forgot to repoint the alias or add a version): `conflict` with a text naming the fix, rows unchanged, never a `kid` collision surfacing as `internal`. Pinned by `TestRotateNextIsActiveKey` (Task 3, fake provider) and `TestExternalRotateWithoutRepoint` (Task 9, `kmsfake`).
2. The external key service is down: the token endpoint answers 503 `temporarily_unavailable` (agents and the exchange), never 500; bearers already minted keep verifying; JWKS keeps serving. Pinned by `TestMintSignerFailureIs503` (Task 2, failing signer) and `TestExternalServiceDown` (Task 9).
3. A restart while the key service is down: a start with existing rows makes no external call and serves (verification works, minting answers 503 until the service returns). Pinned by `TestEnsureActiveMakesNoExternalCall` (Task 9: `kmsfake` call count 0) and each adapter's `TestSignerIsLazy` (Tasks 10 to 13: zero requests to the fake until the first sign).
4. A rolling deploy that switches provider: the replica still on the old configuration keeps minting with its last usable key while that row exists and answers 503 after the row is removed, never mints with a key it cannot use, and logs the mismatch. Pinned by `TestKeyringProviderMismatch` (Task 3).
5. A rotate request retried with the same `Idempotency-Key` after it succeeded answers the original result, not the cooldown `conflict`. Pinned by `TestRotateSigningKeyIdempotent` (Task 7).

---

### Task 1: The Store registry: `signing_keys` is a key set

**Files:**
- Modify: `internal/store/signing.go` (whole file), `internal/store/sqlstore/signing.go` (whole file), `internal/store/sqlite/schema/0001_initial.sql:171-177`, `internal/store/postgres/schema/0001_initial.sql:168-174`, `internal/core/identities/signing.go:40-72` (reads through `ListSigningKeys`, writes the new columns; signature unchanged, replaced in Task 3), `docs/adr/0004-store-contract-and-storage.md` (a "Signing keys addendum (Eric, 2026-10-27)" after the Milestone 5 addendum `:315-321`, superseding "`signing_keys` is a singleton")
- Test: `internal/conformance/store/signing_test.go` (rewrite), `internal/conformance/store/reset_test.go:47,121,166`, `internal/conformance/store/store_test.go:38`, `internal/core/identities/service_test.go:611,849,869,877,889,902,1224`, `internal/conformance/bus/identities_test.go:92`

**Interfaces:**
- Produces (package `store`):
  - `type SigningKey struct { KID, State, Provider, ProviderRef, Algorithm string; PublicKey []byte /* SPKI DER */; PrivateKeyPEM string /* store provider only */; CreatedAt, RetiredAt, RemoveAfter int64 /* ms; RetiredAt and RemoveAfter 0 while active */ }`
  - `const SigningKeyActive = "active"`, `SigningKeyRetiring = "retiring"`; `const SigningProviderStore = "store"`; `var SigningProviders = []string{"store", "awskms", "gcpkms", "azurekms", "hashivault"}`
  - `SigningKeys` interface: `PutSigningKey(ctx, k SigningKey) error` (inserts one row); `ListSigningKeys(ctx) ([]SigningKey, error)` (the active row first, then retiring rows by `retired_at` descending, then `kid` ascending; empty slice, never nil, when none); `RetireSigningKey(ctx, kid string, retiredAt, removeAfter int64) error` (compare-and-swap `active` → `retiring`: `UPDATE ... WHERE kid = ? AND state = 'active'`, 0 rows → `ErrNotFound`); `DeleteSigningKey(ctx, kid string) error` (`ErrNotFound` when absent); `DeleteSigningKeysBefore(ctx, now int64) (int, error)` (deletes `retiring` rows with `remove_after < now`; never an active row).
  - `PutSigningKey` errors: `ErrInvalid` for an empty `KID` or `PublicKey`, a `State` or `Provider` outside the sets, an `Algorithm` other than `EdDSA` or `ES256`, `CreatedAt <= 0`, an active row with a non-zero `RetiredAt` or `RemoveAfter`, a retiring row with `RetiredAt <= 0` or `RemoveAfter < RetiredAt`, a `store` row with an empty `PrivateKeyPEM` or a non-empty `ProviderRef`, any other provider with a `PrivateKeyPEM` or an empty `ProviderRef`; `ErrConflict` for a duplicate `kid` or a second active row. Write methods in a `Read` transaction: `ErrReadOnly`.
- The `SigningKey(ctx)` method is removed (no shim).

- [ ] **Step 1: Write the failing Store suite tests** in `internal/conformance/store/signing_test.go` (both engines, `dbtest.ForEachEngine`): `TestSigningKeyPutList` (empty → `[]`; an active store row and two retiring rows round-trip every field, in the stated order, visible inside the writing Tx); `TestSigningKeyRejects` (table, one case per `ErrInvalid` rule above, plus state `"next"` and provider `"vault"`); `TestSigningKeyPutConcurrent` rewritten: eight goroutines over two store handles (`openStore` and `reopen`, as `TestReplayPutConcurrent`) each insert a different active row; exactly one nil, the rest `ErrConflict` (the partial index maps to `ErrConflict` on both engines), one active row afterwards; `TestSigningKeyRetireCAS` (retire the active row → its state, `retired_at`, `remove_after`; a second retire of the same `kid` → `ErrNotFound`; retire of an unknown `kid` → `ErrNotFound`; after a retire a new active row inserts); `TestSigningKeyDelete` (`DeleteSigningKey` of the active and of a retiring row; again → `ErrNotFound`); `TestSigningKeyDeleteBefore` (retiring rows with `remove_after` 100, 200, 300 and an active row: `DeleteSigningKeysBefore(250)` → 2, again → 0, the active row stays); `TestSigningKeyReadOnly` (`Read` + each write → `ErrReadOnly`). Update the other listed tests to `ListSigningKeys` and the new fields (`reset_test.go` asserts `ResetData` and `ResetRegistry` keep every row; `store_test.go:38` asserts an empty list).
- [ ] **Step 2: Run them to verify they fail.** Run: `go test -count=1 -p 1 -run 'TestSigningKey' ./internal/conformance/store/`. Expected: compile failure (`ListSigningKeys`, `State` undefined).
- [ ] **Step 3: Implement.** Schema, both engines (`BLOB`/`BYTEA`, `INTEGER`/`BIGINT` as the file uses):
  ```sql
  CREATE TABLE signing_keys (
    kid TEXT PRIMARY KEY,
    state TEXT NOT NULL CHECK (state IN ('active', 'retiring')),
    provider TEXT NOT NULL CHECK (provider IN ('store', 'awskms', 'gcpkms', 'azurekms', 'hashivault')),
    provider_ref TEXT NOT NULL DEFAULT '',
    algorithm TEXT NOT NULL CHECK (algorithm IN ('EdDSA', 'ES256')),
    public_key BLOB NOT NULL,
    private_key_pem TEXT,
    created_at INTEGER NOT NULL,
    retired_at INTEGER NOT NULL DEFAULT 0,
    remove_after INTEGER NOT NULL DEFAULT 0,
    CHECK ((provider = 'store') = (private_key_pem IS NOT NULL)),
    CHECK ((provider = 'store') = (provider_ref = '')),
    CHECK ((state = 'active') = (retired_at = 0))
  );
  CREATE UNIQUE INDEX signing_keys_one_active ON signing_keys (state) WHERE state = 'active';
  ```
  `sqlstore/signing.go`: the row type uses `sql.NullString` for `private_key_pem`; validation in Go before the insert (texts name the field); `isConflict` covers both the primary key and the partial index. `identities/signing.go`: load the active row of `ListSigningKeys` (none → insert `State: active, Provider: store, PublicKey: keys.MarshalPublic(gen.Public())`); the corrupt-row checks stay. The ADR 0004 addendum states the new contract and that the singleton is superseded (Eric's decisions 2 and 8, 2026-10-27).
- [ ] **Step 4: Run.** Run: `make lint && make test && make test-conformance && make verify`. Expected: PASS on both engines; `verify: OK`.
- [ ] **Step 5: Commit.**
  ```bash
  git add internal/store/signing.go internal/store/sqlstore/signing.go internal/store/sqlite/schema/0001_initial.sql internal/store/postgres/schema/0001_initial.sql internal/core/identities/signing.go internal/conformance/store/signing_test.go internal/conformance/store/reset_test.go internal/conformance/store/store_test.go internal/core/identities/service_test.go internal/conformance/bus/identities_test.go docs/adr/0004-store-contract-and-storage.md
  git commit -m "feat(store): signing_keys is a key set with one active row (issue #6; Eric 2026-10-27)" # body: State (DoD D) + trailers
  ```

---

### Task 2: `token.Issuer` over a key set

**Files:**
- Modify: `internal/auth/token/issuer.go` (`Issuer` `:62-71`, `NewIssuer` `:73-116`, `Mint` `:158-199`, `Verify` `:202-233`, `JWKS` `:243-251`), `internal/auth/token/verifier.go:42-55` (passes ctx), `internal/server/auth_handlers.go:109` and `:120-137`, `internal/server/auth_exchange.go:129-132`, `internal/server/cmd/cmd.go:141` (`NewIssuer(ctx, issuerURL, token.StaticKeys(key), ...)`, replaced in Task 3)
- Create: `internal/auth/token/keyset.go`
- Test: `internal/auth/token/issuer_test.go`, `internal/auth/token/keyset_test.go` (new), every `NewIssuer`/`Mint`/`Verify` call site: `internal/server/testenv_test.go:107`, `internal/server/auth_test.go:330,487`, `internal/e2e/e2e_test.go:630`, `internal/tui/run_test.go:203`, `internal/conformance/bus/auth_test.go:181,419`, and the `.Mint(` test callers (`rg -n '\.Mint\(' internal --glob '*_test.go'`)

**Interfaces:**
- Produces (package `token`, standard library, go-jose and `internal/auth/keys` only):
  - `type Key struct { KID string; Algorithm keys.Algorithm; Public crypto.PublicKey }`
  - `type ContextSigner interface { Public() crypto.PublicKey; SignContext(ctx context.Context, digest []byte, opts crypto.SignerOpts) ([]byte, error) }` (DER for ES256, raw for EdDSA, as `crypto.Signer`)
  - `type KeySet struct { Active Key; Signer ContextSigner /* nil: no usable signer */; Keys []Key /* every published key, Active included */ }`
  - `type KeySource interface { LoadKeys(ctx context.Context) (KeySet, error) }`
  - `func StaticKeys(s crypto.Signer) KeySource` (one active key, never changes; tests and the interim cmd wiring)
  - `const ReloadInterval = 30 * time.Second`, `UnknownKeyReloadInterval = 5 * time.Second`, `SignTimeout = 10 * time.Second`
  - `var ErrSignerUnavailable = errors.New("token: signing key unavailable")`
  - `func NewIssuer(ctx context.Context, issuerURL string, src KeySource, lifetime time.Duration, now func() time.Time) (*Issuer, error)` (loads once; error when the load fails or the set is invalid)
  - `func (i *Issuer) Mint(ctx context.Context, in MintInput) (string, time.Time, error)`; `func (i *Issuer) Verify(ctx context.Context, raw string, now time.Time) (string, error)`; `func (i *Issuer) JWKS(ctx context.Context) jose.JSONWebKeySet`; `func (i *Issuer) Invalidate()` (the next access reloads); `KID()` and `Algorithm()` report the active key of the cached set; `URL`, `Lifetime`, `TokenEndpoint`, `MetadataURI`, `JWKSURI` unchanged.

- [ ] **Step 1: Write the failing tests** (`keyset_test.go`, fake clock and a counting fake `KeySource`):
  - `TestIssuerVerifiesEveryKey`: set {active K2, retiring K1}; a bearer minted when K1 was active verifies; `JWKS` lists K2 then K1 with their `kid`s.
  - `TestIssuerReloadsOnUnknownKID`: the source gains K3; a bearer signed by K3 verifies after exactly one extra load.
  - `TestIssuerUnknownKIDRateLimited`: 100 bearers with random `kid`s within 5 s cause 1 load; after advancing the clock 5 s one more is allowed; 50 concurrent verifies of one unknown `kid` cause 1 load (single-flight).
  - `TestIssuerPeriodicReload`: after `ReloadInterval` any `Mint`, `Verify` or `JWKS` reloads once; a removed key's bearer then fails `ErrUnknownKey`; `Mint` uses the new active key's `kid` header.
  - `TestIssuerReloadFailureKeepsSet`: a failing reload keeps serving the cached set; `NewIssuer` with a failing source errors.
  - `TestIssuerInvalidate`: `Invalidate()` then `Verify` reloads before the interval.
  - `TestIssuerRejectsBadKeySet` (DoD B): `Active` not in `Keys`, a `KID` that is not the thumbprint, an algorithm that disagrees with the key, a signer whose public key is not `Active`'s.
  - `TestMintSignerFailureIs503` (Review Focus 2): with `Signer: nil` and with a signer whose `SignContext` errs, `Mint` returns an error wrapping `ErrSignerUnavailable`; through `internal/server` (`auth_test.go`), the agent token endpoint and the exchange answer 503 `temporarily_unavailable` with description `the signing key is unavailable`, and a previously minted bearer still authenticates.
  - `TestMintSignTimeout` (`// open note: signing-keys 7`): a signer that blocks until its context ends returns within `SignTimeout` on a fake that records the deadline it saw (≤ 10 s from now).
  - `TestMintES256DER`: a P-256 `ContextSigner` returning DER produces a JWS that `jwt.ParseSigned` verifies with the public key (the cryptosigner contract).
- [ ] **Step 2: Run them to verify they fail.** Run: `go test -count=1 ./internal/auth/token/`. Expected: compile failure.
- [ ] **Step 3: Implement.** `Mint` builds a `jose.Signer` per call over `cryptosigner.Opaque(ctxSigner{ctx, set.Signer})` (a `crypto.Signer` whose `Sign` calls `SignContext` with the request context bounded by `SignTimeout`) with headers `typ at+jwt` and `kid`; errors from the signer wrap `ErrSignerUnavailable`. The cached set is guarded by a mutex; the periodic reload is lazy (on access, judged by `i.now`), non-blocking for readers (they use the cached set while one goroutine reloads); the unknown-`kid` reload is forced, single-flight, at most once per `UnknownKeyReloadInterval`, and the waiters see its result. Handlers: `auth_handlers.go:109` and `auth_exchange.go:129` pass `ctx` and map `errors.Is(err, token.ErrSignerUnavailable)` to `oauthFail(503, "temporarily_unavailable", "the signing key is unavailable")`; `Jwks` passes `ctx`. Update every call site mechanically (`token.StaticKeys(key)`, `context.Background()` or the test's ctx).
- [ ] **Step 4: Run.** Run: `make lint && make test && make test-conformance && bash scripts/m5-exit-checks.sh --deps-only && make verify`. Expected: PASS; `deps_ok=1` (the `internal/auth` allowlist is unchanged).
- [ ] **Step 5: Commit** (`feat(token): the issuer signs with the active key of a key set and verifies by kid (issue #6; Eric 2026-10-27 decision 4)`; `git add` every file listed above).

---

### Task 3: The provider port, the Store-row provider and the rotation rules

**Files:**
- Create: `internal/keyprovider/keyprovider.go` (port, `StoreProvider`, scheme helpers, `ExternalKey`), `internal/keyprovider/keyprovider_test.go`, `internal/core/signingkeys/service.go` (`Service`, `EnsureActive`, `Rotate`, `List`), `internal/core/signingkeys/keyring.go`, `internal/core/signingkeys/service_test.go`, `internal/core/signingkeys/keyring_test.go`, `internal/conformance/bus/signingkeys_test.go`
- Delete: `internal/core/identities/signing.go`; `TestSigningKeyLifecycle` (`service_test.go:841`) and `TestLoadOrCreateSigningKeyRerunsAfterConflict` (`:1185`) move to `signingkeys/service_test.go`; `service_test.go:563,611` switch to `EnsureActive`
- Modify: `internal/server/cmd/cmd.go:43-48,132-147` (State note; `signingkeys.Service{Provider: keyprovider.StoreProvider{}, Settings: signingkeys.DefaultSettings()}`, `EnsureActive`, `NewKeyring`, `token.NewIssuer(ctx, ..., keyring, ...)`, then `svc.OnChange = issuer.Invalidate`), `internal/conformance/bus/identities_test.go:43-99` (delete `TestIdentitiesSigningKeyConcurrent`; `TestSigningKeysConcurrentEnsure` below replaces it), `internal/conformance/bus/auth_test.go:104` (`EnsureActive`, the active row's PEM for its local issuer), `internal/e2e/e2e_test.go` (key source at `:630`), `docs/adr/0002-authentication-and-credentials.md` (amend `:197-201`, `:280-284`, `:480-487`: a key set with active and retiring keys, Eric's decisions of 2026-10-27, superseding the singleton), `docs/ops/deploy.md:40-46` (the key set, the Store-row provider, the JWKS listing every key; rotation and settings point to `configuration.md` and Task 6's commands), `docs/ops/backup.md:5,8,36,43` (a restore re-trusts only the keys in the backup; bearers signed by a newer key fail once and re-mint)

**Interfaces:**
- Consumes: Task 1's `store.SigningKey` and methods; Task 2's `token.KeySet`, `token.Key`, `token.ContextSigner`, `token.ReloadInterval`, `token.Leeway`; `core.Core`, `core.E`, `core.FromStore`.
- Produces (package `keyprovider`; imports the standard library and `internal/auth/keys` only):
  - `type Key struct { KID string; Algorithm keys.Algorithm; Public crypto.PublicKey; Ref string /* "" for store */; PrivatePEM []byte /* store only */ }`
  - `type Signer interface { Public() crypto.PublicKey; SignContext(ctx context.Context, digest []byte, opts crypto.SignerOpts) ([]byte, error) }` (satisfies `token.ContextSigner` structurally)
  - `type Provider interface { Name() string; Next(ctx context.Context) (Key, error); Signer(ctx context.Context, k Key) (Signer, error) }` (`Next` runs outside any Tx; `Signer` makes no network call)
  - `const NameStore = "store"`; `type StoreProvider struct{}` (`Next` generates an EdDSA key with its PKCS#8 PEM and thumbprint `kid`; `Signer` parses `PrivatePEM` and refuses a `kid` or algorithm mismatch)
  - `func Schemes() []string` → `awskms`, `gcpkms`, `azurekms`, `hashivault`; `func SchemeOf(uri string) (string, error)` (`""` for `""`; an unknown scheme or a missing `://` is an error naming the four)
  - `func ExternalKey(pub crypto.PublicKey, ref string) (Key, error)`: a P-256 key → `ES256` with its thumbprint `kid`; anything else, Ed25519 included, → error `external signing key is %s; only ECDSA P-256 (ES256) is supported` (decision 1, DoD B)
- Produces (package `signingkeys`):
  - `const MaxBearerLifetime = 86400 * time.Second`; `func RemoveAfter(retiredAt int64) int64` = `retiredAt + (token.ReloadInterval + MaxBearerLifetime + token.Leeway).Milliseconds()`
  - `type Settings struct { Interval time.Duration /* 0: scheduled rotation off */; Cooldown time.Duration }`; `func DefaultSettings() Settings` → 720 h, 3600 s
  - `type Service struct { *core.Core; Provider keyprovider.Provider; Settings Settings; OnChange func() /* nil ok */ }`
  - `func (s Service) EnsureActive(ctx) (active store.SigningKey, created bool, err error)`
  - `type RotateInput struct { RevokePrevious bool }`; `type RotateResult struct { Active, Previous store.SigningKey; Revoked bool }`
  - `func (s Service) Rotate(ctx, in RotateInput) (RotateResult, error)` (no admin check: the host command; Task 7 adds the admin form)
  - `func (s Service) List(ctx) ([]store.SigningKey, error)`
  - `type Keyring struct{...}`; `func NewKeyring(s Service) *Keyring`; `func (k *Keyring) LoadKeys(ctx) (token.KeySet, error)` (implements `token.KeySource`)

- [ ] **Step 1: Write the failing tests.**
  - `keyprovider_test.go`: `TestStoreProviderRoundTrip` (Next → Signer signs, verifies with `Public`); `TestStoreProviderRefusesMismatch` (a PEM whose `kid` or algorithm differs); `TestSchemeOf` (the four, `""`, `"file://x"`, `"awskms:/x"`, `"AWSKMS://x"` refused); `TestExternalKey` (P-256 ok; Ed25519, P-384, RSA refused with the text).
  - `service_test.go` (SQLite, fake clock, a fake `Provider` that counts `Next` calls and can return a chosen key): `TestSigningKeyLifecycle` (the State note: first `EnsureActive` creates and reports `created`; a second loads and does not; a corrupt store row (bad PEM, `kid` or algorithm mismatch, a public key that disagrees) is refused `internal` and never overwritten); `TestEnsureActiveRerunsAfterConflict`; `TestEnsureActiveProviderMismatch` (active row `store`, configured fake `awskms` → error text exactly `active signing key <kid> is provider store, configured provider is awskms: run agentbus2 server admin signing-key rotate with the new configuration`); `TestRotateRetiresAndInserts` (one active, one retiring with `remove_after = RemoveAfter(now)`; `OnChange` called once after commit); `TestRotateCooldown` (inside 3600 s → `conflict` text `the active signing key <kid> was created at <RFC 3339>; the next rotation is allowed at <RFC 3339> (signing_key_rotation_cooldown_seconds 3600; -revoke-previous is exempt)`, rows unchanged; at exactly the cooldown it rotates); `TestRotateRevokePrevious` (inside the cooldown it succeeds; the previous active row is gone, older retiring rows stay; `Revoked` true); `TestRotateNextIsActiveKey` (Review Focus 1: `Next` returns the active `kid` → `conflict` text `the key URI resolves to the active signing key <kid>: point it at a new key, then rotate`, with and without `RevokePrevious`, rows unchanged); `TestRotateSwitchesProvider` (active `store`, configured fake external → the new row has the fake's provider and ref, the store row retires); `TestRotateFailureAfterNextLeavesRows` (the context is canceled after `Next` returns → error, rows unchanged).
  - `signingkeys_test.go` (Bus suite, both engines, two Cores over two store handles): `TestSigningKeysConcurrentEnsure` (8 concurrent `EnsureActive` → one created, one active row); `TestSigningKeysConcurrentRotate` (8 concurrent `Rotate` after the cooldown → exactly one success, the rest `conflict` from the cooldown, one active and one retiring row; Postgres serialization retries end the same way); `TestSigningKeysConcurrentRevoke` (two concurrent `RevokePrevious` → both succeed by design, one active row).
  - `keyring_test.go`: `TestKeyringLoadsSet` (active first, every retiring key, a signer for the active store key); `TestKeyringProviderMismatch` (Review Focus 4, `// open note: signing-keys 8`: another replica rotated to a provider this keyring is not configured for → the set's `Signer` is the last usable one while its row exists, `nil` once it is removed; a warning is logged naming both providers; `Issuer.Mint` then answers `ErrSignerUnavailable`); `TestKeyringCachesExternalSigners` (the provider's `Signer` is built once per `kid` across reloads).
- [ ] **Step 2: Run them to verify they fail.** Run: `go test -count=1 ./internal/keyprovider/ ./internal/core/signingkeys/ ; go test -count=1 -p 1 -run TestSigningKeys ./internal/conformance/bus/`. Expected: compile failure.
- [ ] **Step 3: Implement.** `EnsureActive`: one Read of `ListSigningKeys`; an active row whose provider is the configured one → validate it (store: parse PEM, compare `kid`, algorithm and `public_key`; external: parse `public_key`, compare `kid` and `ES256`) and return it, no `Next` call; a mismatch → the error above (`bus.CodeConflict`); no active row → `Next` outside the Tx, then insert in a Tx that first re-reads (a row that appeared meanwhile is loaded; `ErrConflict` reruns once). `Rotate`: `Next` outside, then one Tx: list; no active row → insert; `Next().KID == active.KID` → the conflict above; unless `RevokePrevious`, `active.CreatedAt + Cooldown > now` → the cooldown conflict; `RevokePrevious` → `DeleteSigningKey(active)`, else `RetireSigningKey(active, now, RemoveAfter(now))` (`ErrNotFound` → rerun the Tx once); insert the new active row (`ErrConflict` → rerun once). `OnChange` after commit. `Keyring.LoadKeys`: one Read Tx; converts rows to `token.Key` (the row's `public_key` through `keys.ParsePublic`); builds the active signer through `Provider.Signer` when the row's provider is `Provider.Name()`, caching signers by `kid`. Docs as listed.
- [ ] **Step 4: Run.** Run: `make lint && make test && make test-conformance && go test -count=1 -run TestAccessRuleSites ./internal/core/access/ && <the five --deps-only checks> && make verify`. Expected: PASS; `TestReplicasWake/shared-key-and-issuer` still sees exactly one `generated signing key`.
- [ ] **Step 5: Commit** (`feat(signingkeys): key provider port, Store-row provider and rotation rules with the cooldown (issues #6, #8; Eric 2026-10-27)`; body: State).

---

### Task 4: The scheduled rotation and removal jobs

**Files:**
- Create: `internal/core/signingkeys/schedule.go` (`RotateDue`, `RemoveExpired`), `internal/core/signingkeys/schedule_test.go`, `internal/core/maintenance/jobs_test.go` (if absent; else extend the existing tick tests)
- Modify: `internal/core/maintenance/service.go:26-39` (field `SigningKeys *signingkeys.Service`, nil means off), `:78-103` (the two jobs before `"assertion replay expiry"`, only when the field is non-nil), `docs/ops/health.md:111` (`...14 jobs...` becomes "14 jobs, 16 on a server": name the two jobs and their errors)

**Interfaces:**
- Consumes: Task 3's `Service`, `RemoveAfter`, `Settings`.
- Produces: `func (s Service) RotateDue(ctx context.Context, now int64) (int, error)` (1 when it rotated, else 0); `func (s Service) RemoveExpired(ctx context.Context, now int64) (int, error)` (rows deleted); job names `"signing key rotation"` and `"signing key removal"`.

- [ ] **Step 1: Write the failing tests.** `schedule_test.go` (SQLite, fake clock, fake provider): `TestRotateDueStore` (`// open note: signing-keys 4`; `Interval` 0 → never, no `Next` call; younger than the interval → 0 and no `Next` call (the cheap read first); at the interval → 1; a second run → 0); `TestRotateDueExternal` (the fake's `Next` returns the active `kid` → 0; repointed → 1; repointed while the active key is younger than the cooldown → 0, adopted on the first tick after it; active older than the interval and not repointed → 0 with error `signing key <kid> is older than signing_key_rotation_hours (<n>): point the key URI at a new key, then run agentbus2 server admin signing-key rotate`); `TestRotateDueProviderMismatch` (returns the Task 3 mismatch text as its error, rotates nothing); `TestRemoveExpired` (retiring rows past `remove_after` deleted, active never, a second run 0). `jobs_test.go` (`// open note: signing-keys 5`): with `SigningKeys` nil, `Jobs()` has neither name; with it set, both appear in that order immediately before `"assertion replay expiry"`, which is last; `RunJob("signing key rotation")` without the lock is safe (two concurrent `RunJob` calls → one rotation).
- [ ] **Step 2: Run them to verify they fail.** Run: `go test -count=1 ./internal/core/signingkeys/ ./internal/core/maintenance/`. Expected: FAIL (undefined).
- [ ] **Step 3: Implement.** `RotateDue`: Interval 0 → 0; read the rows; provider mismatch → its error; store provider: active age < Interval → 0; otherwise `Rotate(ctx, RotateInput{})`, where a cooldown `conflict` or a "resolves to the active key" `conflict` maps to 0 (plus the stale warning for an external key) rather than an error. `RemoveExpired`: `DeleteSigningKeysBefore(now)`, `OnChange` when it deleted any.
- [ ] **Step 4: Run.** Run: `make lint && make test && make test-conformance && make verify`. Expected: PASS (`replay_test.go:36` and the `len(Jobs())` tests unchanged).
- [ ] **Step 5: Commit** (`feat(maintenance): signing key rotation and removal jobs (issue #6; Eric 2026-10-27 decisions 5, 11)`; body: State).

---

### Task 5: Configuration (Decision 85) and server wiring

**Files:**
- Modify: `internal/server/config/config.go` (fields after `Issuers` `:48`: `SigningKeyURI string json:"signing_key_uri"`, `SigningKeyRotationHours int json:"signing_key_rotation_hours"`, `SigningKeyRotationCooldownSeconds int json:"signing_key_rotation_cooldown_seconds"`; `Default()` 720 and 3600; `Validate` rules), `internal/server/cmd/cmd.go:43-48,132-147,165-190` (provider from the configuration; `adopted signing key ...` line; pass `*signingkeys.Service` to `Serve`), `internal/server/cmd/serve.go` (`Config.SigningKeys *signingkeys.Service`; `m.SigningKeys = c.SigningKeys` beside `m.Archiver` `:146-147`), `docs/ops/configuration.md` (three rows; `:124` rewritten: the key is in the database unless `signing_key_uri` names an external key; no key file), `docs/ops/deploy.md` (the signing-key section: the three settings, the start refusal of decision 9)
- Create: `internal/server/cmd/keyprovider.go` (`// open note: signing-keys 2`), `internal/server/cmd/keyprovider_test.go`
- Test: `internal/server/config/config_test.go`, `internal/server/cmd/cmd_test.go` (`TestSigningKeyStoreLifecycle` `:464`, `TestSigningKeyAfterFailedStart` `:550`)

**Interfaces:**
- Consumes: Tasks 3 and 4 (`signingkeys.Service`, `Settings`, `NewKeyring`, `keyprovider.SchemeOf`, `StoreProvider`).
- Produces (package `cmd`): `var keyBackends = map[string]func(uri string) (keyprovider.Provider, error){}` (filled by tagged `init`s in Tasks 10 to 13); `func openKeyProvider(cfg config.Config) (keyprovider.Provider, error)` (`""` → `StoreProvider{}`; a known scheme without a registered backend → `signing_key_uri: the <scheme> backend is not in this build (build with -tags <scheme>)`); `func signingSettings(cfg config.Config) signingkeys.Settings`; `cmd.Config.SigningKeys`.

- [ ] **Step 1: Write the failing tests.** `config_test.go` (`// open note: signing-keys 6`): defaults 720 and 3600; `signing_key_uri` `""` and each `<scheme>://x` accepted, `gcs://x`, `awskms` and `AWSKMS://x` refused naming the four schemes; rotation hours 0, 48, 8760 ok, 1, 47, 8761, -1 refused; cooldown 1 and 86400 ok, 0 and 86401 refused; `TestMaxBearerLifetimeIsTheCeiling` (`signingkeys.MaxBearerLifetime` equals the upper bound of `token_lifetime_seconds` and `human_token_lifetime_seconds`); the existing doc-table test (`configuration.md` checked against `Settings()`) passes with the three new rows. `keyprovider_test.go`: in the default build every scheme is refused with its tag name; `""` gives the Store provider. `cmd_test.go`: `TestSigningKeyStoreLifecycle` keeps its assertions over the key set (first start prints `generated signing key <kid>`, the second loads); a new `TestSigningKeyProviderMismatchRefusesStart` (a database whose active row is `store`, a configuration naming `awskms://` with `keyBackends["awskms"]` registered to a stub provider defined in the test (`kmsfake` arrives in Task 9) and restored by `t.Cleanup` → exit 1 with the Task 3 text); `TestSigningKeyAfterFailedStart` unchanged in meaning.
- [ ] **Step 2: Run them to verify they fail.** Run: `go test -count=1 ./internal/server/config/ ./internal/server/cmd/`. Expected: FAIL.
- [ ] **Step 3: Implement.** `cmd.run`: `openKeyProvider` (exit 2 on a configuration error), `signingkeys.Service{Core: c, Provider: p, Settings: signingSettings(cfg)}`, `EnsureActive` (exit 1 with `signing key: <err>`), keyring, issuer, `svc.OnChange = issuer.Invalidate`; on `created`, `generated signing key <kid>` for the store provider, `adopted signing key <kid> (<provider> <key name>)` (the key name is the URI without user info, query or fragment, `keyprovider.KeyName`; the URI itself is never printed) otherwise; the Ready line keeps `kid %s` (the active key). Rewrite the State note at `:43-48` (second start, two starts, failed start, corrupt row, mismatch). `Serve` gives `maintenance.Service` the pointer.
- [ ] **Step 4: Run.** Run: `make lint && make test && make test-conformance && <the five --deps-only checks> && make verify`. Expected: PASS.
- [ ] **Step 5: Commit** (`feat(server): signing_key_uri, signing_key_rotation_hours and signing_key_rotation_cooldown_seconds (Decision 85; Eric 2026-10-27)`; body: State).

---

### Task 6: The host command `server admin signing-key list|rotate`

**Files:**
- Modify: `internal/server/cmd/admin.go` (`adminSubcommands`; `case "signing-key"` in the switch `:76-92`; `hostEnv` gains `Keys signingkeys.Service`: `rotate` builds it with `openKeyProvider(cfg)`; `list` gets `Provider: nil` (it reads the Core only), so `list` never fails on a backend this build lacks), `docs/ops/admin-bootstrap.md` (the host commands section)
- Test: `internal/server/cmd/admin_test.go`

**Interfaces:**
- Consumes: Task 3's `Service.List`, `Service.Rotate`; Task 5's `openKeyProvider`, `signingSettings`.
- Produces: `agentbus2 server admin signing-key list` prints one line per key, active first: `<kid> <state> <provider> <algorithm> created <RFC 3339>[ retired <RFC 3339> remove-after <RFC 3339>][ ref <ref>]`; `agentbus2 server admin signing-key rotate [-revoke-previous]` prints `active <new kid>; <old kid> retiring until <RFC 3339>` or `active <new kid>; <old kid> revoked`. Exit 0, 1 (service error, the cooldown `conflict` included), 2 (usage: an unknown verb, extra arguments, an unknown flag).

- [ ] **Step 1: Write the failing tests** (`admin_test.go`, SQLite, plus the Postgres DSN form the file already uses for engine coverage): `TestAdminSigningKeyList` (after a server start: one active line; never a PEM in the output: `strings.Contains(out, "PRIVATE KEY")` false); `TestAdminSigningKeyRotate` (rotate → two keys, list shows the retiring one with `remove-after`; an immediate second rotate → exit 1 with the cooldown text; `-revoke-previous` → exit 0, the revoked `kid` absent from `list`); `TestAdminSigningKeyUsage` (`signing-key`, `signing-key bogus`, `signing-key list extra`, `signing-key rotate -from x` → exit 2 before the store is opened).
- [ ] **Step 2: Run them to verify they fail.** Run: `go test -count=1 -run TestAdminSigningKey ./internal/server/cmd/`. Expected: FAIL.
- [ ] **Step 3: Implement** with the existing `subFlags` and `parseInterleaved` helpers.
- [ ] **Step 4: Run.** Run: `make lint && make test && make verify`. Expected: PASS.
- [ ] **Step 5: Commit** (`feat(admin): server admin signing-key list and rotate (issue #6; Eric 2026-10-27 decisions 6, 7)`; body: State: a second rotate is refused by the cooldown, two overlapping rotates leave one winner, a failed rotate leaves the rows).

---

### Task 7: The admin API, the Bus methods and the client commands

**Files:**
- Modify: `api/openapi.yaml` (two operations after `resetCredential` `:863-882`; schemas `SigningKey`, `SigningKeyList`, `SigningKeyRotation`), then `make generate` (`internal/gen`, `internal/gen/spec`), `internal/bus/admin.go:30-49` (two methods), `internal/bus/signingkeys.go` (new: types and `SigningKeysNotConfigured`), `internal/core/signingkeys/api.go` (new: the admin forms and conversion), `internal/server/server.go:30-49` (`Options.SigningKeys`), `internal/server/signing_keys.go` (new: handlers and the refusing stand-in, as `bridges.go:13-25`), `internal/server/cmd/serve.go:198` (`SigningKeys: c.SigningKeys`), `internal/bus/local/signingkeys.go` (new: `unavailable`), `internal/bus/remote/signingkeys.go` (new), `internal/cli/signingkeys.go` (new) and `internal/cli/cli.go:39` (dispatch `"signing-keys"`), `docs/ops/admin-bootstrap.md` (the admin API section)
- Test: `internal/server/signing_keys_test.go` (new), `internal/server/cmd/cmd_test.go:432` (46), `internal/conformance/bus/signingkeys_test.go` (extend), `internal/bus/remote/signingkeys_test.go` (new), `internal/cli/signingkeys_test.go` (new)

**Interfaces:**
- Consumes: Task 3's `Service.List`, `Service.Rotate`, `RotateResult`; `core.RequireServerAdmin`; `core.WithReceipt` (`internal/core/receipts.go:21`).
- Produces:
  - Wire: `GET /v1/admin/signing-keys` (`operationId: listSigningKeys`, tag `admin`, 200 `SigningKeyList {keys: [SigningKey]}`); `POST /v1/admin/signing-keys/rotate` (`operationId: rotateSigningKey`, tag `admin`, query `revoke_previous` boolean default false, `IdempotencyKeyParam`, 200 `SigningKeyRotation {active: SigningKey, previous: SigningKey, revoked: boolean}`). `SigningKey` (all required): `kid`, `state` (enum `active`, `retiring`), `provider` (enum of the five), `provider_ref`, `algorithm` (enum `EdDSA`, `ES256`), `created_at`, `retired_at`, `remove_after` (int64 Unix ms, 0 while active, as `Scope.created_at`). Never a private key. Both protected; `info.version` stays `1.0.0`.
  - `bus`: `type SigningKey struct { KID, State, Provider, ProviderRef, Algorithm string; CreatedAt, RetiredAt, RemoveAfter int64 }`; `type RotateSigningKeyInput struct { RevokePrevious bool; IdempotencyKey string }`; `type SigningKeyRotation struct { Active, Previous SigningKey; Revoked bool }`; `const SigningKeysNotConfigured = "this server has no signing keys (local mode)"`; `Admin.ListSigningKeys(ctx) ([]SigningKey, error)`, `Admin.RotateSigningKey(ctx, in RotateSigningKeyInput) (SigningKeyRotation, error)`.
  - `signingkeys`: `func (s Service) ListSigningKeys(ctx, admin string) ([]bus.SigningKey, error)` and `func (s Service) RotateSigningKey(ctx, admin string, in bus.RotateSigningKeyInput) (bus.SigningKeyRotation, error)` (`RequireServerAdmin` inside the transaction; rotate wraps its Tx in `WithReceipt` keyed by `in.IdempotencyKey`, payload `{revoke_previous}`).
  - `server`: `type SigningKeys interface { ListSigningKeys(ctx, admin string) ([]bus.SigningKey, error); RotateSigningKey(ctx, admin string, in bus.RotateSigningKeyInput) (bus.SigningKeyRotation, error) }`; nil → every call `unavailable`, not retryable, text `bus.SigningKeysNotConfigured`, through `WriteProblem`.

- [ ] **Step 1: Write the failing tests.**
  - `internal/server/signing_keys_test.go` (`newEnv` with `issuer: true` and a `signingkeys.Service` over the env's store in `Options.SigningKeys`): `TestListSigningKeysAdmin` (an administrator lists the active key; no `private` substring in the body); `TestSigningKeysForbiddenForAgent` (a non-administrator → 403 `forbidden` on both operations; this is the non-administrator check: the Bus suite cannot show it because both of its transports answer `unavailable` first, so no `TestACLMatrix` row is added); `TestRotateSigningKeyAPI` (rotate → 200, two keys listed; again → 409 `conflict` with the cooldown text; `revoke_previous=true` → 200 `revoked: true`); `TestRotateSigningKeyIdempotent` (Review Focus 5: the same key twice → the same body, one rotation; the same key with `revoke_previous` changed → 409 as `WithReceipt` does for a different payload); `TestSigningKeysUnconfigured` (nil → 503 `unavailable`, not retryable, the fixed text).
  - Bus suite `TestSigningKeysUnavailable` (both engines, both transports: `h.admin("root").ListSigningKeys` and `RotateSigningKey` → `unavailable`, `Retryable` false, `bus.SigningKeysNotConfigured`).
  - `cmd_test.go:432`: `protected != 46 || open != 8 || protected+open != 54`.
  - `remote` and `cli` tests as for bridges (`internal/cli/bridges_test.go`): JSON on stdout; `rotate -revoke-previous -key K`; a `bus.Error` prints `agentbus2: conflict: ...` and exits 1.
- [ ] **Step 2: Run them to verify they fail.** Run: `go test -count=1 ./internal/server/ ./internal/server/cmd/ ./internal/bus/remote/ ./internal/cli/`. Expected: compile failure.
- [ ] **Step 3: Implement**, then `make generate`. The local `Bus` methods return `ab2bus.Errorf(ab2bus.CodeUnavailable, false, "%s", ab2bus.SigningKeysNotConfigured)` (as `internal/bus/local/scopes.go:38-49`). `internal/bus/remote` never imports `internal/gen/spec`.
- [ ] **Step 4: Run.** Run: `make lint && make test && make test-conformance && make generate-check && <the five --deps-only checks> && go test -count=1 -run TestAccessRuleSites ./internal/core/access/ && make verify`. Expected: PASS; `TestAccessRuleSites` unchanged; m6 and m8 word checks pass.
- [ ] **Step 5: Commit** (`feat(api): listSigningKeys and rotateSigningKey in protocol 1.0.0 (issue #6; Eric 2026-10-27 decision 6)`; include `internal/gen` and `internal/gen/spec`).

---

### Task 8: Replica and end-to-end tests

**Files:**
- Modify: `internal/conformance/bus/replicas_test.go` (new `TestReplicasSigningKeyRotation`), `internal/conformance/bus/auth_test.go` (a twelfth leaf `signing-key-rotation`), `scripts/m5-exit-checks.sh:9,31` (`WANT=12`)

**Interfaces:**
- Consumes: Tasks 5 to 7 (two in-process replicas through `cmd.Run`, `cmd.RunAdmin` for `signing-key rotate`, the remote `Bus.Admin.RotateSigningKey` over TCP).

- [ ] **Step 1: Write the failing tests.**
  - `TestReplicasSigningKeyRotation` (both engines, the existing replica fixture): leaf `concurrent-rotate-one-active` (both replicas and both host commands run with `-set signing_key_rotation_cooldown_seconds=1`; after 1 s, two `cmd.RunAdmin ... signing-key rotate` at once: exactly one exit 0, the other exit 1 with the cooldown text; `list` shows one active key); leaf `new-kid-verifies-on-both` (an administrator rotates through replica A's API, so A mints with the new `kid`; a bearer from A authenticates on B at once: B's unknown-`kid` reload); leaf `revoke-propagates` (`// open note: signing-keys 9`; `revoke_previous` through A; a bearer signed by the revoked key is refused by A at once and by B within `token.ReloadInterval + 5*time.Second`, polled every 500 ms).
  - `TestAuthEndToEnd` leaf `signing-key-rotation` (over TCP, after the existing leaves): an administrator's remote Bus rotates; `GET /v1/auth/jwks` lists two keys; a bearer minted before still authenticates; a new login's header `kid` is the new one; a second rotate → `conflict`; `RevokePrevious` → a bearer signed by the revoked key (minted with the test's local issuer over that key's PEM) answers 401.
- [ ] **Step 2: Run them to verify they fail.** Run: `go test -count=1 -p 1 -v -run 'TestReplicasSigningKeyRotation|TestAuthEndToEnd' ./internal/conformance/bus/ > "${TMPDIR:-/tmp}/ab2-sk.log" 2>&1; bash scripts/m5-exit-checks.sh "${TMPDIR:-/tmp}/ab2-sk.log"`. Expected: the new leaves FAIL before the fixture work, then the m5 script reports 12 per engine.
- [ ] **Step 3: Implement** the fixtures only (no production change is expected; a needed one is reported to the integrator as a design gap).
- [ ] **Step 4: Run.** Run: `make test-conformance && go test -count=1 -p 1 -v -run '^TestReplicas' ./internal/conformance/bus/ > "${TMPDIR:-/tmp}/ab2-m7.log" 2>&1; bash scripts/m7-exit-checks.sh "${TMPDIR:-/tmp}/ab2-m7.log" && make verify`. Expected: m5 `fail=0` and 12 per engine; m7 counts unchanged; `verify: OK`.
- [ ] **Step 5: Commit** (`test(signingkeys): rotation across replicas and over TCP (issue #6 acceptance 4)`).

---

### Task 9: External provider scaffolding: `kmsfake`, the rules against it, build-tag gates

**Files:**
- Create: `internal/keyprovider/kmsfake/kmsfake.go` and `kmsfake_test.go`, `internal/conformance/bus/signingkeys_external_test.go`, `docs/ops/signing-keys.md` (the providers, the rotation model of decision 3 per backend, and the trust boundary of the Store-row provider; each backend's section is added by its task)
- Modify: `scripts/m7-exit-checks.sh` (rule `keyprovider-leaf`: `check_allowlist keyprovider-leaf ./internal/keyprovider "$MOD/internal/keyprovider" "$MOD/internal/auth/keys" github.com/go-jose/go-jose/v4` (go-jose because `internal/auth/keys` imports it and the allowlist is transitive); `check_allowlist` the same for `./internal/keyprovider/kmsfake`; `check_deps keyprovider-consumers` for `./internal/core/...`, `./internal/bus/...`, `./internal/cli`, `./internal/mcpserver`, `./internal/client`, `./internal/server` (the package) against `internal/keyprovider/awskms`, `gcpkms`, `azurekms`, `hashivault`; `check_importers` runs a second pass with `-tags awskms,gcpkms,azurekms,hashivault`), `scripts/check-gates-selftest.sh` (`block_m7_deps_rule_fires`: one `m7_probe` per new clause), `Makefile` (`vet-tags`, called from `vet-cross`), `.github/workflows/ci.yml` (job `signing-key-tags`), `.github/workflows/windows.yml` (tagged `go vet`), `docs/ops/README.md:13` (the Store-row provider's boundary; external providers: see `signing-keys.md`)

**Interfaces:**
- Consumes: Task 3's port; Task 5's `keyBackends`.
- Produces: `package kmsfake`: `type Fake struct{...}` implementing `keyprovider.Provider` with `Name()` settable (default `"awskms"`), an in-memory set of P-256 keys behind one alias; `func New(name string) *Fake`; `func (f *Fake) Repoint() keyprovider.Key` (a new key behind the alias); `func (f *Fake) SetDown(bool)` (every call fails); `func (f *Fake) Calls() int` (network-equivalent calls: `Next` and `SignContext`); `Signer` builds lazily and counts nothing until a sign.

- [ ] **Step 1: Write the failing tests.** `kmsfake_test.go` (its own behavior: `Next` resolves the alias and is stable until `Repoint`; signatures verify; `SetDown` fails both). `signingkeys_external_test.go` (both engines, `kmsfake` as the configured provider): `TestEnsureActiveMakesNoExternalCall` (Review Focus 3: with an active external row, `EnsureActive` and a keyring load make 0 calls; a first start with no rows calls `Next` once); `TestExternalRotateWithoutRepoint` (Review Focus 1: `Rotate` → the "resolves to the active key" `conflict`, rows unchanged); `TestExternalRotateAfterRepoint` (the new row carries `provider` and `provider_ref` of the repointed key; the old one retires); `TestExternalServiceDown` (Review Focus 2: `SetDown(true)` → `Mint` wraps `ErrSignerUnavailable`; verification and `JWKS` unaffected; `RotateDue` returns an error and changes nothing); `TestExternalRemovalDeletesOnlyTheRow` (removal never calls the fake). `scripts/check-gates-selftest.sh`: a probe per new clause that must print its `deps_violation=` line.
- [ ] **Step 2: Run them to verify they fail.** Run: `go test -count=1 -p 1 -run 'TestEnsureActiveMakesNoExternalCall|TestExternal' ./internal/conformance/bus/`. Expected: compile failure.
- [ ] **Step 3: Implement.** `make vet-tags` loops over `awskms gcpkms azurekms hashivault awskms,gcpkms,azurekms,hashivault` and GOOS `linux windows` (a script file if longer than the Makefile's other targets: `scripts/vet-tags.sh`, `set -euo pipefail`).
- [ ] **Step 4: Run.** Run: `make lint && make test && make test-conformance && make vet-tags && bash scripts/m7-exit-checks.sh --deps-only && make verify && make check-gates-selftest`. Expected: PASS; the selftest's m7 block reports each new probe firing.
- [ ] **Step 5: Commit** (`feat(keyprovider): kmsfake, external rotation rules and the build-tag gates (issue #8)`).

---

### Tasks 10 to 13: One external backend each (awskms, then hashivault, gcpkms, azurekms)

Each task has the same shape; `<b>` is the backend and its build tag, `<mod>` its sigstore module (`github.com/sigstore/sigstore/pkg/signature/kms/{aws,hashivault,gcp,azure}` v1.11.1).

**Files (per task):**
- Create: `internal/keyprovider/<b>/<b>.go`, `internal/keyprovider/<b>/<b>_test.go`, `internal/keyprovider/<b>/live_test.go` (opt-in: runs only when `AGENTBUS2_LIVE_<B>_URI` is set; never in CI), `internal/server/cmd/backend_<b>.go` (`//go:build <b>`: `func init() { keyBackends["<b>"] = <b>.New }`), `internal/server/cmd/backend_<b>_test.go` (`//go:build <b>`: the scheme is accepted, every other scheme still refused with its tag)
- Modify: `go.mod`, `go.sum` (`go mod tidy`; list the new direct and indirect modules with versions in the commit body; confirm each is CGO-free: `CGO_ENABLED=0 go build -tags <b> -o /dev/null .`), `scripts/m7-exit-checks.sh` (`check_importers <b>-importers <mod> "$MOD/internal/keyprovider/<b>"` and one per cloud SDK root the adapter imports directly), `scripts/check-gates-selftest.sh` (one importer probe), `docs/ops/signing-keys.md` (the backend's section: URI grammar, how to rotate (repoint the alias, add a version, or point the setting at a new key, then `rotate`), the permissions the server needs and nothing more, the trust boundary, credentials from the SDK's default chain, removal never deletes the external key: the operator retires it after `remove_after`, shown by `list`)

**Interfaces (per task):**
- Consumes: `keyprovider.Provider`, `Signer`, `Key`, `ExternalKey`; the facts table above.
- Produces: `package <b>`: `func New(uri string) (keyprovider.Provider, error)` (validates the URI with the module's own `ValidReference` or regexp, makes no network call); `Name()` returns `"<b>"`; `Next` resolves to the pinned reference per the facts table and returns `ExternalKey(pub, ref)`; `Signer(ctx, k)` builds the sigstore signer for `k.Ref` lazily on the first `SignContext` (a failed load is retried on the next call) and signs with `SignMessage(nil reader, options.WithDigest(digest), options.WithContext(ctx), options.WithCryptoSignerOpts(crypto.SHA256))`; never `kms.Get`, never `CryptoSigner`.

- [ ] **Step 1: Record the facts.** Read `<mod>` in the module cache (`go mod download -json <mod>@v1.11.1`) and confirm or correct the facts table row (URI grammar, resolution, digest versus message, DER, context, the options the constructor takes). A difference from the table that changes this task's design stops the task and is reported, not worked around.
- [ ] **Step 2: Write the failing tests** against the fake named in the table (Task 13: resolution only, `// open note: signing-keys 3`): `TestNextResolvesAndPins` (an alias or versionless URI → the pinned ref; repointing the fake changes it); `TestNextRefusesKeyType` (an Ed25519 and a P-384 key → the `ExternalKey` text; DoD B); `TestSignerIsLazy` (Review Focus 3: `New` and `Signer` make 0 requests); `TestSignVerifiesAsJWS` (a `token.Issuer` over a one-key source using this signer mints a bearer that `Issuer.Verify` accepts: proves digest mapping and DER); `TestSignHonorsContext` (Task 11: a fake that never answers returns `context.DeadlineExceeded` within the deadline although sigstore ignores ctx; others: the canceled context reaches the request); `TestNewRefusesURI` (malformed URIs per the grammar). Task 13 also: `TestSignThroughSeam` (the `signFunc` seam receives the pinned URI and the digest; a raw 64-byte return is refused, DER accepted).
- [ ] **Step 3: Run them to verify they fail.** Run: `go test -count=1 ./internal/keyprovider/<b>/ && go test -count=1 -tags <b> ./internal/server/cmd/`. Expected: FAIL.
- [ ] **Step 4: Implement**, add the modules, update the gates and the docs section.
- [ ] **Step 5: Run.** Run: `make lint && make test && make vet-tags && bash scripts/m7-exit-checks.sh --deps-only && go test -count=1 -tags <b> ./internal/server/cmd/ ./internal/keyprovider/... && CGO_ENABLED=0 go build -o "${TMPDIR:-/tmp}/ab2-lean" . && CGO_ENABLED=0 go build -tags <b> -o "${TMPDIR:-/tmp}/ab2-<b>" . && ls -l "${TMPDIR:-/tmp}/ab2-lean" "${TMPDIR:-/tmp}/ab2-<b>" && make verify && make check-gates-selftest`. Expected: PASS; `go version -m "${TMPDIR:-/tmp}/ab2-lean" | rg -c 'sigstore|aws-sdk|cloud.google|azure-sdk|hashicorp/vault'` prints 0; record both sizes in the commit body (for ADR 0012).
- [ ] **Step 6: Commit** (`feat(keyprovider): <b> backend over sigstore <mod> v1.11.1, build tag <b> (issue #8; Eric 2026-10-27 decisions 1, 3, 12)`).

---

### Task 14: Records and the release build tags

**Files:**
- Create: `docs/adr/0012-signing-key-rotation-and-key-providers.md` (Eric's twelve decisions of 2026-10-27, each labeled as his; the five consequences and their derivation; the settled open notes 1 to 9 with their markers; the measured binary sizes from Tasks 10 to 13; supersedes the singleton of ADR 0002 item 6 and ADR 0004's Milestone 5 addendum), `docs/adr/README.md` (index entry)
- Modify: `release/build-archive.sh:41` (comment `# open note: signing-keys 1: release archives are built without a key-provider tag (lean)`; no `-tags`), `Dockerfile:8` (`ARG BUILD_TAGS=awskms,gcpkms,azurekms,hashivault` and `-tags "${BUILD_TAGS}"` in the `go build` line, with the same marker), `docs/ops/deploy.md` (the image carries every backend; the archives carry none; build from source with `-tags` for a binary that needs one), `docs/install.md` (one sentence: the archives hold no key-provider backend), `docs/superpowers/specs/2026-10-21-gate-0-decisions.md:89-93` (a pointer to ADR 0012; the item stays as history), `CLAUDE.md` (every edit of this plan, made once here so no parallel pair shares the file: the "The server's token signing key is a Store row ..." line becomes the key set in `signing_keys`, rules in `internal/core/signingkeys`, port `internal/keyprovider`; host commands gain `signing-key list|rotate [-revoke-previous]`; client commands gain `signing-keys list|rotate [-revoke-previous] [-key K]`; "8 open and 44 protected" becomes "8 open and 46 protected"; the m5 line says 12 leaf subtests; the keyprovider import rule beside the Slack rule; the `release/` line: archives untagged, image tagged)
- Test: `scripts/docker-smoke.sh` (the image starts with `AGENTBUS2_SERVER_SIGNING_KEY_URI=hashivault://none` and fails at `EnsureActive` with a Vault error, not with `not in this build`: proves the tags are in the image without a real service), `release/build-archive.sh --self-test` (asserts the built archive's binary has no `sigstore` module in `go version -m`)

- [ ] **Step 1: Write the failing checks** (the smoke and self-test assertions above).
- [ ] **Step 2: Run them to verify they fail.** Run: `make docker-smoke; bash release/build-archive.sh --self-test`. Expected: the smoke assertion fails (no tags in the image yet).
- [ ] **Step 3: Implement** the Dockerfile and build-archive changes and write the records. Check every sentence of ADR 0012 and the docs against the committed code and the task commit bodies (Definition of Done E); no private hostnames or account details.
- [ ] **Step 4: Run.** Run: `make verify && make docker-smoke && make release-check && make release-lint && bash scripts/m9-exit-checks.sh --real-only`. Expected: PASS.
- [ ] **Step 5: Commit** (`docs(adr): 0012 signing key rotation and key providers; release archives untagged, image with every backend (Eric 2026-10-27)`).
