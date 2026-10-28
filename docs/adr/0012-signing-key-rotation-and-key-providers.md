# ADR 0012: Signing key rotation and key providers

Status: Accepted (Eric, 2026-10-27, decisions 1 to 12 below); the controller rulings in "Controller rulings" are pending Eric's review.

Covers issues #6 (rotation) and #8 (key providers). The design is `docs/superpowers/specs/2026-10-27-signing-keys-design.md` (sections 4 and 5 are Eric's decisions); the plan is `docs/superpowers/plans/2026-10-27-signing-keys.md`; the operator guide is `docs/ops/signing-keys.md`.

Supersedes the singleton of ADR 0002 item 6 and ADR 0004's Milestone 5 addendum (`signing_keys` as a singleton row, `LoadOrCreateSigningKey`). Those texts stay as history and point here.

## Context

Until 2026-10-27 the server had one signing key: a single `signing_keys` row, created at the first start as plaintext PKCS#8 PEM, never rotated, with database read access equal to the administrator flag (Eric, 2026-10-21, Milestone 5 item 2). Issue #6 asked for rotation, including rotate now; issue #8 asked for the private part to live in a key service (AWS KMS, Google Cloud KMS, Azure Key Vault, HashiCorp Vault transit).

## Decisions (Eric, 2026-10-27)

Each of these is Eric's decision, recorded by the plan as "Eric's decisions of 2026-10-27".

1. **External keys are chosen by key URI** (`awskms://`, `gcpkms://`, `azurekms://`, `hashivault://`) over sigstore's CGO-free per-backend modules. No URI means the Store-row provider. The algorithm is ES256 whenever the key is held externally and Ed25519 for the Store-row key.
2. **The Store is the registry for every provider.** A `signing_keys` row carries the state, the provider, the resolved provider reference and the SPKI public key; the provider holds only the private part.
3. **The configured key URI (or an alias in it) is resolved at each rotation** and the resolved reference is recorded in the row. Replicas sign by the recorded reference.
4. **Replicas reload on an unknown `kid`** (single-flight, at most once per 5 s) **and every 30 s** (`token.UnknownKeyReloadInterval`, `token.ReloadInterval`).
5. **`remove_after = retired_at + 30 s + 86400 s + 60 s`**: the reload interval, the configured bearer lifetime ceiling and the leeway.
6. **Rotate now is a host command and an admin API operation** (`Bus.Admin`, the client CLI), in protocol `1.0.0`. Eric: "Protocol version will be 1.0.0, end of discussion."
7. **`-revoke-previous` deletes the previous active key in the rotation transaction.** The cooldown never refuses it.
8. **Two states**, `active` and `retiring`.
9. **A configured provider that differs from the active row's provider refuses the start.** The operator rotates explicitly.
10. **A rotation inside the cooldown is refused with `conflict`** (`signing_key_rotation_cooldown_seconds`, default 3600), checked inside the rotation transaction with no extra lock.
11. **Scheduled rotation every 720 hours by default**, with a minimum of 48 hours (`signing_key_rotation_hours`).
12. **Each backend sits behind a build tag** (`awskms`, `gcpkms`, `azurekms`, `hashivault`). All four are in this delivery; the default binary stays lean.

## Consequences of those decisions

The brief's "Changes to existing decisions" were not individually decided by Eric. Each is recorded here as a consequence of the decisions above, not as a new decision:

1. The singleton becomes a key set (decision 8 and issue #6): the partial unique index `signing_keys_one_active` admits one active row and any number of retiring rows (ADR 0004, "Signing keys addendum").
2. The settings `signing_key_uri`, `signing_key_rotation_hours` and `signing_key_rotation_cooldown_seconds` (decisions 1, 11 and 10). Bounds: hours is 0 or 48 to 8760; cooldown is 1 to 86400, always below the 48-hour minimum, so a cooldown never delays a scheduled rotation.
3. "Database read access equals the administrator flag" holds only for the Store-row provider (decision 1 and the issue #8 acceptance criterion): an external provider's private key never reaches the database or a backup.
4. The external SDK dependencies sit behind build tags (decisions 1 and 12). The import rule that enforces it (`keyprovider-leaf`, `keyprovider-consumers` in `scripts/m7-exit-checks.sh`) is an agent choice.
5. The wire addition lands in `1.0.0` (decision 6): `GET /v1/admin/signing-keys` and `POST /v1/admin/signing-keys/rotate`. `api/openapi.yaml` now declares 54 operations, 8 open and 46 protected (ADR 0001 and ADR 0010 counted 52 and 44 protected before).

## Settled open notes

Each is reversible with one findable edit: `rg -n 'open note: signing-keys' .`. They are agent choices, settled in the plan and not Eric's.

1. **Release archive build tags.** `release/build-archive.sh` builds with no tag; the Docker image (`Dockerfile`, the server deployment target) builds with all four through `ARG BUILD_TAGS=awskms,gcpkms,azurekms,hashivault` passed as `-tags "${BUILD_TAGS}"`. The archives are the brew, scoop and winget installs that run agents, the CLI and the TUI, where a cloud SDK is dead weight (decision 12's "lean"); the image is what `docs/ops/deploy.md` deploys. Markers: `Dockerfile`, `release/build-archive.sh`. `release/build-archive.sh --self-test` asserts the archive binary links no sigstore module; `scripts/docker-smoke.sh` step 2c asserts the image has all four backends.
2. **Where the build tag sits.** The adapter packages `internal/keyprovider/<backend>` carry no tag; only `internal/server/cmd/backend_<backend>.go` does. Every untagged gate (`vet`, `lint`, `test`, `vet-cross`, `windows.yml`, the m7 importer scan) then compiles every adapter on every GOOS, while the binary links an SDK only when `main` reaches the tagged file. Markers: `internal/server/cmd/keyprovider.go`, `keyprovider_test.go`.
3. **Azure test coverage.** sigstore's Azure `LoadSignerVerifier` builds its own credential and client with no options, so its sign path cannot point at a fake. The adapter resolves the URI with its own `azkeys` client (injectable transport, tested against `httptest`), signs through sigstore by the pinned URI behind an unexported `signFunc` seam, and covers the sigstore sign path only in the opt-in live test. Eric may prefer signing through `azkeys` directly (a departure from decision 1 for Azure). Marker: `internal/keyprovider/azurekms/azurekms_test.go`.
4. **Scheduled rotation with an external provider** resolves the key URI on every maintenance tick while `signing_key_rotation_hours > 0`; 0 turns scheduled rotation off for every provider, and then only `rotate` adopts a repointed URI. Marker: `internal/core/signingkeys/schedule_test.go`.
5. **The two maintenance jobs** (`signing key rotation`, `signing key removal`) exist only when the server wires the port (`maintenance.Service.SigningKeys`); local mode has neither. They sit before `assertion replay expiry`, which stays last. Marker: `internal/core/maintenance/jobs_test.go`.
6. **Bounds** of the two settings (consequence 2). Marker: `internal/server/config/config_test.go`.
7. **External sign timeout.** `Issuer.Mint` bounds the sign call with `token.SignTimeout` (10 s) on the request context. Marker: `internal/auth/token/keyset_test.go`.
8. **Visibility of a stale external key and of a provider mismatch.** The lock holder's `signing key rotation` job returns them as its error, so health shows them; every replica's `Keyring` logs a warning on each reload that finds them. Marker: `internal/core/signingkeys/keyring_test.go`.
9. **Revoke propagation in the replica test** polls the other replica for up to `token.ReloadInterval + 5 s` (about 30 s per engine at worst). Marker: `internal/conformance/bus/replicas_test.go`.

## How the four backends work

Facts from the committed adapters (`internal/keyprovider/<backend>`):

- **Resolution.** Each backend calls its cloud client directly for `Next`, because sigstore hides the resolved version or ARN. AWS also does so for `DescribeKey` and `GetPublicKey`. All four sign through sigstore's signer, by the pinned reference the row records.
- **`Next` is bounded centrally** by `signingkeys.NextTimeout` (30 s), whatever the backend.
- **Sign path.** `hashivault`, `gcpkms` and `azurekms` run the sign call in a goroutine bounded by the request context, with 16 slots (`signSlots`) and a `recover`; a call abandoned at the context's end finishes in the background. `awskms` has no goroutine, no slots and no `recover`: it passes the context to sigstore's `SignMessage` (`options.WithContext`), which honors it.
- **Verify after sign.** `hashivault`, `gcpkms` and `azurekms` verify the returned signature under the public key the row holds (`ecdsa.VerifyASN1`) and refuse one that does not verify. `awskms` does not, because a key ARN is globally unique.
- **Errors never quote the URI.** Messages name the key with `keyprovider.KeyName` (the URI without user info, query or fragment); provider text is redacted of known secrets and capped at 256 bytes.
- **Credentials come from the environment.** `azurekms` requires `AZURE_TOKEN_CREDENTIALS` and refuses to start without it (a configuration error, exit 2). `hashivault` reads `VAULT_ADDR` (or `BAO_ADDR`) and `VAULT_TOKEN` (or `BAO_TOKEN`); it does not use the Vault token helper and does not log in or renew.
- **Outage.** `keyprovider.ErrUnavailable` is how a provider reports its service being down: `rotate` answers a retryable `unavailable`, the token endpoint answers 503 `temporarily_unavailable` ("the signing key is unavailable"), and the server exits 1 at start (a configuration error exits 2).

## Controller rulings (pending Eric's review)

These were made during execution by the controller or an implementing agent. They are not Eric's decisions; each is reversible and awaits his review.

1. **Vault authentication is environment-only**: no token helper, no renewal.
2. **The central `signingkeys.NextTimeout`** of 30 s for every backend's `Next`.
3. **Verify-after-sign** in `hashivault`, `gcpkms` and `azurekms`, and not in `awskms`.
4. **The `gcpkms` project-segment tolerance**: when it checks that the version name the service returns belongs to the requested key, the adapter does not compare the project segment (the service may spell it with the project number instead of the project ID), and the pinned reference is the requested path plus the version number, never the service's spelling.
5. **`azurekms` fails closed** without `AZURE_TOKEN_CREDENTIALS`, so the default credential chain never runs `az`, `azd` or `pwsh` from the server's `PATH`; Azure Germany is not supported.
6. **Docker smoke assertion** (this task): `scripts/docker-smoke.sh` starts the image with `<scheme>://none` for each backend and asserts the backend's own refusal (exit 2 for the three that validate the URI, exit 1 for `hashivault`, which fails at `EnsureActive` on the unset `VAULT_ADDR`), which proves the tags are in the image without a key service.

## Binary sizes

Measured at the commit before this one, `CGO_ENABLED=0 go build -trimpath` with no `-ldflags`, on darwin/arm64 (the Docker image is linux and its binary is stripped with `-s -w`):

| Tags | Bytes |
| --- | --- |
| none (lean) | 37461170 |
| `awskms` | 44047426 |
| `hashivault` | 41188738 |
| `gcpkms` | 56835250 |
| `azurekms` | 42113378 |
| all four | 67020274 |

The figures recorded in the task commits were 37527266, 44113426, 41254674, 56917858 and 42196066 (lean, awskms, hashivault, gcpkms, azurekms); they were built without `-trimpath`, and the difference of about 66000 bytes each is the embedded source paths that `-trimpath` removes (the lean figure was the same 37527266 bytes from Task 10 to Task 13).

## Alternatives not taken

- Sign through `azkeys` directly for Azure (open note 3): a departure from decision 1, left for Eric.
- A key-file setting (`signing_key_file`): rejected in ADR 0002; the Store row stays the default.
