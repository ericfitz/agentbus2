# Signing keys: rotation and key providers (design brief)

## 1. Status

- Draft of 2026-10-27, written by an agent from `main` at `9fc48e7`.
- Nothing in it is a decision until Eric marks it so.
- **Eric reviewed this brief on 2026-10-27.** Of its 12 decisions, **12 are decided** and **0 are open**. Decisions 1, 10 and 12 were changed by Eric and the brief is rewritten to match; decision 3 (the key rotation model for external keys) was put to him as a restatement for the cloud-neutral model and he ratified it later the same day, as did the cooldown default of 1 hour (decision 10). One note stays open: which build tags the release archives are built with (decision 12).
- The earlier "on hold" note is resolved: Eric chose a cloud-neutral model, key URI backends (decision 1), replacing the AWS-first recommendation.
- **Eric's decision 2026-10-27**: "Protocol version will be 1.0.0, end of discussion." Any addition in this brief ships in protocol 1.0.0, and `info.version` and `version.Protocol` stay `1.0.0` before the first release.
- Issues: #6 "feat: server signing key rotation, including rotate now" and #8 "feat: key storage provider for the signing key (KMS, vaults)". #8 depends on #6. Both are follow-ups of Milestone 5 item 2 (Eric, 2026-10-21).

## 2. Problem and scope

### What the issues ask

Issue #6: the server signs its own bearers with one key. That key lives in a singleton Store row and never changes. The issue asks for:

- several keys, each with its own `kid` and a state (active or retiring);
- scheduled rotation by a configured interval, run as a maintenance job;
- a "rotate now" admin operation or host command;
- removal of a retiring key once it is older than the maximum token lifetime plus leeway;
- safety across replicas: two servers rotating at once must yield one new active key, and a second run must be harmless.

Issue #8: the private key is plaintext PEM in the database, so database read access equals the power to mint any token. The issue asks for:

- a provider interface (sign, public keys, rotate), with the Store row as the default provider;
- at least one external provider, designed and selected through the Decision 85 configuration (Eric's decision 2026-10-27: cloud-neutral, selected by key URI, with AWS KMS, Google Cloud KMS, Azure Key Vault and HashiCorp Vault backends in the first delivery);
- providers that work with rotation;
- ops docs that state each provider's trust boundary.

### Acceptance criteria (restated)

Issue #6:

1. The Store holds several keys with states (active, retiring), each with its own `kid`. The JWKS publishes the active key and every retiring key.
2. A configured interval drives scheduled rotation, as a maintenance job. "Rotate now" exists as an admin operation and/or a host command.
3. A retiring key is removed once it is older than the maximum token lifetime plus leeway.
4. Two servers rotating at once yield one new active key. A second run is safe.

Issue #8:

5. A provider interface (sign, public keys, rotate) exists, with the Store-row provider as the default.
6. At least one external provider is designed and selected through Decision 85 configuration (here: four key URI backends, 4.7).
7. That provider works with rotation.
8. `docs/ops/` states the trust boundary of each provider.

### Out of scope

- Client key rotation: agents' registered keys and `reset-credential`.
- The Milestone 9 release-signing key: `AGENTBUS2_SIGNING_KEY` in the gate-0 decisions, at `docs/superpowers/specs/2026-10-21-gate-0-decisions.md:262`.
- External issuers' keys. Those are fetched by `token.External` (`internal/auth/token/external.go`).
- AWS workload identity for clients. Decision 63 defers it past 2.0.0 (`docs/adr/0002-authentication-and-credentials.md:75`).
- Local mode. It has no signing key (`docs/ops/deploy.md:15`), and it must stay without one.
- Data migration. Before the first release the schema changes in place in `0001_initial.sql` on both engines. Local databases are recreated (CLAUDE.md, Eric 2026-10-25).

## 3. What exists today

### The Store row

- Schema: `signing_keys(kid PK, singleton INTEGER NOT NULL DEFAULT 1 UNIQUE CHECK (singleton = 1), algorithm CHECK IN ('EdDSA','ES256'), private_key_pem NOT NULL, created_at)`. It is at `internal/store/sqlite/schema/0001_initial.sql:171-177` and `internal/store/postgres/schema/0001_initial.sql:168-174`.
- Store contract: `SigningKeys` has two methods, `PutSigningKey` and `SigningKey` (`internal/store/signing.go:16-25`). A second row is `ErrConflict`, through the singleton's unique constraint (`internal/store/sqlstore/signing.go:38-42`). `SigningKey()` selects without a `WHERE` (`sqlstore/signing.go:48`).
- `Meta.ResetData` and `ResetRegistry` keep `signing_keys` (`internal/store/meta.go:23-36`).
- Transactions are serializable (`internal/store/sqlstore/store.go:50,93`). Postgres retries serialization failures 5 times (`internal/store/postgres/postgres.go:25-27,171-174`).

### Creating the key

- `identities.Service.LoadOrCreateSigningKey` (`internal/core/identities/signing.go:22-77`) generates an EdDSA key before the transaction (Decision 26). Inside the transaction it loads the row, or inserts the generated key when there is none. It retries once on `ErrConflict`. It refuses a corrupt row, meaning one whose PEM, `kid` or algorithm disagree.
- `cmd.run` calls it at start (`internal/server/cmd/cmd.go:137`). It builds one `token.Issuer` from the key (`cmd.go:141`), prints `generated signing key <kid>` on first creation (`cmd.go:145-147`) and wires `token.Verifier{Own: issuer}` (`cmd.go:172`). The State note for this path is at `cmd.go:43-48`.

### `internal/auth` (leaf packages)

- `token.Issuer` holds exactly one `crypto.Signer`, `kid` and algorithm (`internal/auth/token/issuer.go:63-71`). It signs through `cryptosigner.Opaque(key)` with header `kid` (`issuer.go:110-111`). Any `crypto.Signer` works there: go-jose v4.1.6 `cryptosigner` passes the raw payload for EdDSA, and SHA-256 plus an ASN.1 DER signature for ES256.
- `Issuer.Verify` refuses any `kid` but its own with `ErrUnknownKey` (`issuer.go:213-215`).
- `Issuer.JWKS()` publishes one key (`issuer.go:244-251`).
- `token.Leeway = 60s` (`issuer.go:27`).
- `kid` is the RFC 7638 SHA-256 thumbprint (`internal/auth/keys/keys.go:71-72`). The same rule applies to client keys.
- The leaf rule: `internal/auth/...` imports only the standard library, `internal/auth/...` and go-jose v4 (`internal/auth/doc.go:5-9`). It is checked by `scripts/m5-exit-checks.sh:92-94`.
- `token.External` is the model for a key-set cache. It refetches on an unknown `kid`, rate-limited to once a minute (`external.go:150-224`).

### Endpoints

- `GET /v1/auth/jwks` (operation `jwks`, `api/openapi.yaml:808-821`) already returns an array (`JWKS.keys`, `api/openapi.yaml:2023-2031`). The handler copies `Issuer.JWKS()` (`internal/server/auth_handlers.go:120-137`). A key set with several keys therefore needs no schema change.
- The token endpoint mints after the replay transaction: agents at `auth_handlers.go:109`, and humans through the exchange at `internal/server/auth_exchange.go:129`. Signing is outside every Store transaction already.
- Bearer verification reaches `token.Verifier.Verify` with the request context (`internal/server/auth_bearer.go:46`). `Issuer.Verify` itself takes no context.
- Clients refresh once on a 401 (`internal/credential/doc.go:4`). A bearer whose key a replica no longer knows therefore costs one re-mint, not an outage.

### Token lifetimes: the inputs to "maximum token lifetime"

- Agent bearers last `token_lifetime_seconds`: default 3600 (`internal/server/config/config.go:94`), range 1 to 86400 (`config.go:201`).
- Human bearers last each issuer's `human_token_lifetime_seconds`: 0 means 900 (`external.go:55-65`), range up to 86400 (`config.go:317`).
- `agentTokenLifetime = 24h` (`internal/core/identities/agent.go:17`) is an opaque enrollment token, not a JWT. It does not count.
- The configured ceiling is therefore 86400 s. The worst-case lifetime of a bearer is that ceiling plus `Leeway`.

### Maintenance and locks

- The jobs run in order under `Store.TryLock("maintenance")` (`internal/core/maintenance/service.go:23,74-104,123-158`). The lock lapses at twice the interval on SQLite, or with the session on Postgres.
- `RunJob` runs a job without the lock (`service.go:166-179`).
- The loop runs in `Serve` (`internal/server/cmd/serve.go:145-184`), and also in `agentbus2 mcp` and `tui` in local mode (CLAUDE.md).
- `maintenance.Service.Archiver` (`service.go:33-35`) is the precedent for an optional, nil-when-off port.
- The default interval is 60 s (`internal/core/config.go:84`).
- The health document already reports each job's result and error (`internal/core/health/health.go:103-151`).

### Decisions and docs that assert one key

These are the statements an implementation would amend:

- ADR 0002, Milestone 5 item 2 (`docs/adr/0002-authentication-and-credentials.md:197-201`).
- ADR 0002, the signing-key rule, item 6 (`0002:280-284`).
- ADR 0002, "Replicas share the issuer and the signing key" (`0002:480-487`).
- ADR 0004, the Milestone 5 addendum: "`signing_keys` is a singleton" (`docs/adr/0004-store-contract-and-storage.md:310-321`).
- Gate-0 item 2 (`docs/superpowers/specs/2026-10-21-gate-0-decisions.md:89-93`).
- `docs/ops/README.md:13`.
- `docs/ops/deploy.md:40-47`: "no key file and no signing-key setting", "rotating the key is not implemented".
- `docs/ops/backup.md:5,8,36,43`.
- `docs/ops/configuration.md:124`.
- `CLAUDE.md`: "The server's token signing key is a Store row ... Rotation is issue #6, a key provider issue #8."

### Facts about external libraries, checked in the module cache on 2026-10-27

- AWS KMS asymmetric keys cannot rotate automatically or on demand. See `service/kms@v1.61.4/api_op_EnableKeyRotation.go:27-28` and `api_op_RotateKeyOnDemand.go:33`. "Rotate" with KMS therefore means a new KMS key, never a new version of the same key.
- KMS supports Ed25519: `KeySpecEccNistEdwards25519` (`types/enums.go:521`) and `SigningAlgorithmSpecEd25519Sha512` (`types/enums.go:724`). `ED25519_SHA_512` requires `MessageType: RAW` (`api_op_Sign.go:167` in v1.62.0).
- KMS messages are limited to 4096 bytes (`api_op_Sign.go:113` in v1.62.0). A JWS signing input of a few hundred bytes fits.
- KMS returns ECDSA signatures as DER (`api_op_Sign.go:243-245` in v1.61.4), which is what `cryptosigner` expects for ES256.
- The Ed25519 signature encoding is not stated in that comment. It is expected to be the raw 64 bytes of RFC 8032, and a test must confirm it (section 8). Since Eric's decision 2026-10-27, an externally held key is ES256 (4.10), so this concerns only the Store-row key and any later backend that offers Ed25519.
- The facts above are about the AWS SDK. Eric's decision 2026-10-27 puts the backends behind sigstore's CGO-free modules `github.com/sigstore/sigstore/pkg/signature/kms/{aws,gcp,azure,hashivault}` (v1.11.1 on the module proxy). Their API, their per-backend key resolution and their signature encodings have not been checked in the module cache for this brief; the first task of the adapter work checks them (section 7, task 8).

## 4. Options

### 4.1 Key states and the schema (issue #6)

The schema changes in place, with no migration. The `singleton` column goes. Proposed columns:

- `kid` (primary key)
- `state` (`CHECK IN ('active','retiring')`)
- `provider` (`CHECK IN ('store','awskms','gcpkms','azurekms','hashivault')`: `store`, or the scheme of the key URI, 4.7)
- `provider_ref` (the resolved key reference the backend returned at rotation, for example the KMS key ARN; `''` for `store`)
- `algorithm`
- `public_key` (SPKI DER, for every provider)
- `private_key_pem` (nullable, with `CHECK ((provider = 'store') = (private_key_pem IS NOT NULL))`)
- `created_at`
- `retired_at`
- `remove_after`

A partial unique index allows at most one active row: `CREATE UNIQUE INDEX signing_keys_one_active ON signing_keys(state) WHERE state = 'active'`. SQLite and Postgres both support it. Concurrent rotators are then serialized by the database, as the singleton does today.

- **Option A, two states (active, retiring): Recommended.** This is what the issue states. A replica that has not reloaded learns a new `kid` on first sight (4.3). Simple, and testable on both engines.
- **Option B, three states (next, active, retiring).** A key is published for one reload interval before it becomes active. That protects only third-party relying parties that cache our JWKS. No code in this repository consumes the JWKS: replicas read the Store, and clients only read `jwks_uri` (`internal/credential/discover.go:23`). It adds a state and a timer for no current consumer.

### 4.2 Where the provider sits: the Store as registry (issue #8)

- **Option A, the Store is the key registry for every provider, and the provider holds only the private part: Recommended.** Every key, whatever its provider, has a Store row with `kid`, state, timestamps and public key. JWKS, verification, rotation and replica coordination read and write the Store only, so the replica-safety argument of 4.4 holds unchanged for every provider. The provider supplies a new key's public part and reference at rotation time, and a signer for a recorded key. Verification never calls the provider: KMS is asked for the public key once, at rotation.
- **Option B, the provider owns everything (key list, states, public keys).** JWKS and rotation state would live in KMS or Vault. Replicas would query the provider on every reload. The Store-row default would need a second registry, and the safety argument would differ per provider. More code, weaker tests (a fake must model the states), and no gain.

The interface for Option A maps the issue's "sign, public keys, rotate" as follows:

- *rotate* is `Next`, which creates or resolves a key outside every transaction (Decision 26), plus the core's rotation transaction;
- *public keys* is the registry;
- *sign* is the provider's `Signer`.

Interface sketch (names provisional):

```go
// package keyprovider: stdlib and internal/auth/keys only
type Key struct {
	KID        string
	Algorithm  keys.Algorithm
	Public     crypto.PublicKey
	Ref        string // provider reference; "" for the store provider
	PrivatePEM []byte // store provider only
}
type Signer interface {
	Public() crypto.PublicKey
	SignContext(ctx context.Context, msg []byte, opts crypto.SignerOpts) ([]byte, error)
}
type Provider interface {
	Name() string                                      // "store", "awskms", "gcpkms", "azurekms", "hashivault"
	Next(ctx context.Context) (Key, error)             // outside any Tx
	Signer(ctx context.Context, k Key) (Signer, error) // for a recorded key
}
```

`SignContext` is there because `crypto.Signer.Sign` has no context and a KMS call needs one. `internal/auth/token` cannot import `internal/keyprovider` (the leaf rule, `m5-exit-checks.sh:92-94`). So `token` declares its own `KeySource` and context-signer interfaces in standard-library types, and `keyprovider.Signer` satisfies them structurally. The token package wraps a per-request `crypto.Signer` around the context signer for `cryptosigner.Opaque`.

There is one `Provider` port for all backends (Eric's decision 2026-10-27, decision 1). The backend is chosen by the configured key URI scheme (4.7), and each external backend is a thin adapter over the matching sigstore KMS signer module, which supplies the public key and the signature.

The list operation (host command and API) returns `kid`, algorithm, provider, the resolved key reference, state and timestamps, and never private material.

### 4.3 How replicas learn about a new or removed key

After replica 1 rotates, replica 2 still mints with the old key. That is harmless, because the old key is retiring and still trusted. But replica 2 refuses bearers that carry the new `kid` (`issuer.go:213`) until it reloads.

- **Option A, reload on an unknown `kid`, plus a periodic reload: Recommended.** This follows `token.External`. The issuer holds a key-set source. On `ErrUnknownKey` it reloads from the Store, single-flight and rate-limited (once per 5 s, so random `kid`s cannot flood the Store). A periodic reload (30 s) also picks up removals and a new active key for minting. It works on both engines and both transports with no new infrastructure. The reload interval is a term in the removal time (4.5).
- **Option B, reload on every maintenance tick only.** Only the lock holder ticks (`service.go:126-135`), so the other replicas would need a second loop anyway. A replica would refuse new-`kid` bearers for up to a minute.
- **Option C, Option A plus a Postgres `NOTIFY` doorbell on key changes.** This cuts the revoke latency on Postgres from the reload interval to about zero. It touches `internal/core/notify/pgnotify` and the payload format (`internal/store/postgres/notify.go`). SQLite replicas, which do not share hints anyway, keep Option A. It is worth it only if the immediate revoke (4.6) needs to be quicker than 30 s.

### 4.4 Rotation transaction and concurrency (issue #6)

Proposed, for every provider:

1. Outside any transaction, call `provider.Next(ctx)`. The store provider generates a key; KMS resolves it (4.8). For the store provider a cheap read first skips `Next` when the key is not due, so the lock holder does not generate a throwaway key every tick.
2. In one transaction, list the rows. A scheduled rotation that is not due does nothing. Due is per provider: the store provider is due when the active key's age is at least the interval; an external provider is due when `Next().KID` differs from the active `kid`, and there the interval only warns (4.8). Then apply the cooldown (Eric's decision 2026-10-27, decision 10): unless the call is the explicit leaked-key case (`-revoke-previous`), the active row's `created_at` plus the cooldown must not be in the future, otherwise return `conflict` with a message naming the earliest time a rotation is allowed. The active row's `created_at` is the time of the last rotation, so the cooldown needs no new column. Otherwise retire the active row with a compare-and-swap (`UPDATE ... WHERE kid = ? AND state = 'active'`, where 0 rows means `ErrNotFound`, then retry), compute its `remove_after`, and insert the new active row.
3. On `ErrConflict` (the partial index, or a duplicate `kid`), rerun the transaction once. Postgres serialization failures are already retried by the Store.

The maintenance lock is an optimization here, not the guard. `RunJob` and the host command run without it, and the transaction is safe anyway.

On what a second concurrent "rotate now" does (**DECIDED, changed by Eric 2026-10-27: a cooldown between rotations**):

- **Decided: require a cooldown between rotations, except for `-revoke-previous`.** A rotation is refused with `conflict` while the active key is younger than the cooldown. The guard is the transaction itself: the partial unique index and the compare-and-swap leave exactly one active row, and a second rotator that runs after the first finds an active key younger than the cooldown and is refused. No extra lock is needed, so there is a single effective rotator without `Store.Locks`; the `maintenance` lock still serializes scheduled runs as an optimization. The cooldown is a new setting, `signing_key_rotation_cooldown_seconds`; its default is 1 hour (3600 seconds) and it is configurable (Eric's decision 2026-10-27), and the interval minimum of 48 hours stays far above it. The leaked-key case, `-revoke-previous`, is never refused by the cooldown. Two concurrent revokes each rotate, which is deliberate: the operator asked for a revoke twice.
- **Superseded by Eric's decision 2026-10-27, Option A (rotate unconditionally, with an optional `-from KID` compare-and-swap).** It was the earlier recommendation. `-from` is dropped from the host command, the API and the tests; the cooldown covers the double-click case that `-from` addressed.
- **Not chosen, Option B (coalesce within N seconds):** it would hide a real second request without an error, and a cooldown that answers `conflict` is the visible form of the same idea.

### 4.5 Removal time: "maximum token lifetime plus leeway"

A replica that has not reloaded can mint with a key for up to one reload interval after the key retired. So: `remove_after = retired_at + reload_interval + max_lifetime + Leeway`.

- **Option A, `max_lifetime` is the configured ceiling (86400 s): Recommended.** This is robust to replicas with different lifetimes and to a lifetime lowered after minting. The cost: a retiring key stays published for about 24 hours. With a 30-day interval that means two keys in the JWKS for one day a month. A compromised key is handled by the immediate revoke (4.6), not by this timer.
- **Option B, `max_lifetime` is the largest lifetime in the retiring replica's configuration** (`token_lifetime_seconds` and every `human_token_lifetime_seconds`), written to `remove_after` at retirement. It is tighter (about 1 hour at the defaults). If a replica runs with a larger lifetime, its late bearers get a 401 and the client re-mints once (`credential/doc.go:4`). That is benign but noisy.
- **Option C, record the largest lifetime minted per key.** That is a write on every mint. Rejected.

The removal job deletes retiring rows with `remove_after < now`. For a store-provider row, deletion also deletes its PEM.

### 4.6 The "rotate now" surface and the immediate revoke

On the surface:

- **Option A, host command only:** `agentbus2 server admin signing-key list|rotate [-revoke-previous]`. No wire change.
- **Option B, host command plus an admin API operation: Recommended.** The admin API adds `POST /v1/admin/signing-keys/rotate` and `GET /v1/admin/signing-keys`, `Bus.Admin.RotateSigningKey` and `ListSigningKeys`, and the client commands `agentbus2 signing-keys list|rotate`. The issue names "admin operation" first. Existing host commands such as `reset-credential` and `create-identity` already have API twins (`adminReset`, `resetCredential`, `createAgentIdentity`). A Kubernetes deployment has no handy host shell. The local Bus answers `unavailable`, not retryable, as the bridge operations do (`internal/bus/local/scopes.go:38-49`). This option ships in protocol 1.0.0, and `info.version` and `version.Protocol` stay `1.0.0` before the first release (Eric's decision 2026-10-27).
- **Option C, the API only.** A host command that works with no credential is the recovery path when the admin's own bearer is the problem (Decision 73). Rejected.

On the emergency revoke:

- **Option A, `-revoke-previous`: Recommended.** It deletes the previous active key in the same transaction instead of retiring it, and it is exempt from the rotation cooldown (4.4). Every bearer that key signed fails once the replicas reload: at most the reload interval under 4.3 Option A, about zero under Option C. Clients re-mint once. Without it, a leaked key stays trusted for `remove_after`.
- **Option B, no revoke.** Simpler, but the operator then has no response to a leak short of editing the database.

### 4.7 External providers (issue #8)

**DECIDED, changed by Eric 2026-10-27: a cloud-neutral model, key URI backends.** This replaces the AWS-first recommendation (Option A below).

- One `crypto.Signer` port (4.2). The backend is chosen by the scheme of the configured key URI: `awskms://`, `gcpkms://`, `azurekms://` or `hashivault://`. With no key URI, the Store-row provider stays the default.
- The adapters are sigstore's CGO-free modules `github.com/sigstore/sigstore/pkg/signature/kms/{aws,gcp,azure,hashivault}` (v1.11.1 on the module proxy). As in Option A below, the key never leaves the external service and only minting calls it.
- The trust boundary is per backend: whoever may sign with the key (`kms:Sign`, the Cloud KMS signer role, the Key Vault sign permission, or the Vault Transit sign policy). The ops docs state it for each.
- The first delivery includes all four backends, behind build tags (4.10).

Earlier options, all **superseded by Eric's decision 2026-10-27**:

- **Option A, AWS KMS, sign only (the key never leaves KMS), the earlier recommendation: superseded.** It is the only option that removes the private key from both the database and the server's memory. The trust boundary becomes "whoever holds `kms:Sign` on the key". Ed25519 is available, so the server's default algorithm does not change. Verification uses the stored public key, so only minting calls KMS: one call per token, which is rare at the default lifetime of one hour. It fits Eric's AWS deployments, and Decision 55 already names KMS as a reason for ES256 (`docs/adr/0002-authentication-and-credentials.md:32`). Its costs are the AWS SDK dependency (section 6) and per-mint latency and quotas.
- **Option B, KMS envelope encryption of the Store row (not chosen).** The PEM is stored encrypted under a KMS key and decrypted at load. Signing is local and fast, and rotation stays purely in the Store. But the plaintext lives in process memory, and the boundary becomes database read access plus `kms:Decrypt`.
- **Option C, HashiCorp Vault Transit (now one of the four backends, `hashivault://`).** Remote signing with Ed25519 and P-256, and native key versions. It needs a Vault deployment.
- **Option D, AWS Secrets Manager or Vault KV holding the PEM (not chosen).** This only relocates the plaintext: the server reads it into memory at start. Rotation must write a secret, so the server needs write permission. Weakest of the four.

### 4.8 External key rotation model

**DECIDED (Eric's decision 2026-10-27): ratified.** The configured key URI (or an alias in it) is resolved at each rotation, and the resolved key reference is recorded in the row. The text below states that model in terms of the key URI, carried over from the earlier KMS alias model.

An external service may not rotate an asymmetric key in place (for AWS KMS see section 3), so a rotation is a different key behind the same configuration.

- **Option A, restated: the configured key URI (or an alias in it) is resolved at each rotation, and the resolved key reference is recorded in the row.** `Next` resolves the URI through the backend and returns the resolved reference and public key. Replicas sign by the recorded reference, never by the URI, so a repointed alias cannot split the fleet. The AWS example follows: `signing_key_uri = awskms:///alias/agentbus2-signing`. `Next` calls `DescribeKey` on the alias, then `GetPublicKey` on the target, and returns its ARN and public key. How each other backend resolves a URI to a pinned key version (Google Cloud KMS key version, Azure Key Vault key version, Vault Transit key version) is checked in the sigstore modules during task 8; where a backend has no alias, the operator rotates by pointing the configuration at a new key and running `rotate`. The row stores the ARN, and replicas sign by the recorded ARN, never by the alias, so a repointed alias cannot split the fleet. The operator rotates by creating a key and running `aws kms update-alias`, then `rotate now`. Alternatively, the scheduled job adopts a changed target on its next tick, as a rotation. "Not due" also covers "the alias still points at the active key", so a second run is a no-op. The server creates nothing in AWS, which means no orphaned keys and least privilege: `kms:Sign`, `kms:GetPublicKey`, `kms:DescribeKey`. The scheduled interval cannot create keys. With KMS it becomes a warning, in the log and the job result, when the active key is older than the interval. Removing a retiring row deletes nothing in AWS: the operator schedules deletion of the old KMS key after its `remove_after` (the list operation shows it).

The hashing is a risk for the AWS example, and generally for every backend. `cryptosigner` passes the raw payload for EdDSA and a SHA-256 digest for ES256 (go-jose `cryptosigner.go:105,117`). Since an external key is ES256 (4.10), the adapter must hand the backend a digest where the backend signs digests, or the message where the backend hashes itself. With the AWS SDK that was `ECDSA_SHA_256` with `MessageType: DIGEST` (`api_op_Sign.go:116`); sending `RAW` would sign the hash of the hash. How the sigstore signers take a message or digest is checked in task 8, and the adapter test asserts the signature verifies against the public key for every backend's fake.
- **Option B, explicit reference: `rotate now -key-ref ARN`.** The same Store behavior with no alias. It adds a flag to every rotation and gives no scheduled path.
- **Option C, the server calls `CreateKey`.** Full automatic rotation. It needs `kms:CreateKey`, `TagResource` and `ScheduleKeyDeletion`, and these are broad. A crash between `CreateKey` and the commit orphans a KMS key, so a tag-based sweeper is needed. Decision 26 keeps the side effect out of the transaction, but it cannot undo it.

### 4.9 A configured provider that differs from the active row's

Example: switching from `store` to `awskms` (the scheme of the configured key URI; switching between two external backends is the same case).

- **Option A, refuse to start: Recommended (ratified by Eric 2026-10-27, decision 9).** Exit 1 with "active signing key `<kid>` is provider store, configured provider is awskms: run `agentbus2 server admin signing-key rotate` with the new configuration". Two replicas with different configurations can never flip-flop. An already running replica that reloads and finds an active key it cannot sign with keeps minting with its last usable key while that key exists, and reports the condition as a maintenance-job error in health.

A start with existing rows makes no external call: the public keys are in the rows, and the external `Signer` only builds a client. A restart while the key service is down therefore succeeds; verification works, and minting answers 503 until it returns. Only a first start with no rows needs the service.
- **Option B, rotate automatically at start.** Convenient, but a rolling deploy with mixed configurations rotates on every start.

### 4.10 Smaller questions

- **Default rotation interval.** A: 0 (off) by default. **B: 720 hours (30 days): Recommended**, because rotation should be on without an operator action, and 30 days is far above the 24-hour ceiling. C: 2160 hours (90 days). The minimum should be larger than the removal window, so that at most two keys are normally published: for example, at least 48 hours.
- **Algorithm (DECIDED, Eric 2026-10-27, as part of decision 1):** ES256 whenever the key is held externally; Ed25519 stays for the Store-row key (a new store-provider key is EdDSA, as today). An external key is read for its algorithm at `Next`: a P-256 key maps to ES256, and anything else (including an Ed25519 external key) is refused with a message naming the key type (Definition of Done B).
- **Cloud SDK linking (DECIDED, changed by Eric 2026-10-27, decision 12).** The cloud SDKs go behind build tags, one per backend (names provisional: `awskms`, `gcpkms`, `azurekms`, `hashivault`), and the first delivery includes all four backends. The default binary stays lean. A key URI whose backend was not compiled in is refused at start with a message naming the tag. **Superseded by Eric's decision 2026-10-27:** the earlier recommendation, Option A (the AWS SDK always linked, one release artifact, after measuring the size increase). Consequences: CI builds and tests each tag, and `vet-cross` and `windows.yml` run with the tags on; which tags the release archives are built with is **an open note**: Eric's answer does not cover it, and it is settled in the release tooling task.

### Changes to existing decisions (architectural; each needs Eric's approval)

1. The singleton signing key becomes a key set: ADR 0002 item 6 and the Milestone 5 item 2 line, ADR 0004's addendum, and gate-0 item 2.
2. "There is no signing-key setting" (`deploy.md:45`, `configuration.md:124`, ADR 0002 `:284`) changes. New settings arrive: the key URI, the rotation interval and the rotation cooldown. There is still no key file.
3. With an external provider, "database read access equals the administrator flag" no longer holds (`docs/ops/README.md:13`). The ops docs must state it per provider.
4. The server gains external runtime dependencies (AWS KMS, Google Cloud KMS, Azure Key Vault, HashiCorp Vault, each behind a build tag) and a new import rule.
5. A wire addition (4.6 Option B) ships in protocol 1.0.0, and `info.version` and `version.Protocol` stay `1.0.0` before the first release (Eric's decision 2026-10-27).

## 5. Decisions for Eric

1. **External provider model.** **DECIDED: key URI backends, replacing the AWS-first recommendation (Eric's decision 2026-10-27)** One `crypto.Signer` port; the backend is chosen by key URI (`awskms://`, `gcpkms://`, `azurekms://`, `hashivault://`); adapters are sigstore's CGO-free `pkg/signature/kms/{aws,gcp,azure,hashivault}` modules (v1.11.1); the Store-row provider stays the default; ES256 whenever the key is held externally, Ed25519 stays for the Store-row key. *Superseded by Eric's decision 2026-10-27:* A: AWS KMS, sign only (the earlier recommendation). B: KMS envelope encryption of the Store row. C: Vault Transit alone. D: Secrets Manager or Vault KV holding the PEM.
2. **Provider architecture.** **DECIDED: A, the Store is the registry for every provider, and the provider holds only the private part (Eric's decision 2026-10-27)** (ratified). B: the provider owns the key list and states.
3. **Key rotation model for external keys.** **DECIDED: A (Eric's decision 2026-10-27)**, ratified. A: the configured key URI (or an alias in it) is resolved at each rotation, and the resolved key reference is recorded in the row. B: an explicit `-key-ref` on each rotation. C: the server creates the key itself (`CreateKey` for AWS).
4. **How replicas learn of key changes.** **DECIDED: A, reload on an unknown `kid` (single-flight, at most once per 5 s) plus a 30 s periodic reload (Eric's decision 2026-10-27)** (ratified). B: reload on maintenance ticks only. C: Option A plus a Postgres `NOTIFY` doorbell.
5. **`max_lifetime` in the removal time** (`retired_at + reload + max_lifetime + 60 s`). **DECIDED: A, the configured ceiling, 86400 s (Eric's decision 2026-10-27)** (ratified). B: the largest lifetime configured on the retiring replica.
6. **Rotate-now surface.** **DECIDED: B, host command plus an admin API operation, `Bus.Admin` and client CLI, in protocol 1.0.0 (Eric's decision 2026-10-27)** (ratified). A: host command only. C: the API only.
7. **Immediate revoke (`-revoke-previous`).** **DECIDED: A, include it (Eric's decision 2026-10-27)** (ratified; it is also the one rotation the cooldown of decision 10 never refuses). B: leave it out.
8. **Key states.** **DECIDED: A, active and retiring (Eric's decision 2026-10-27)** (ratified). B: next, active and retiring (publish ahead).
9. **Configured provider differs from the active row's.** **DECIDED: A, refuse to start, and require an explicit rotate (Eric's decision 2026-10-27)** (ratified). B: rotate automatically at start.
10. **Concurrent rotate-now.** **DECIDED: a cooldown between rotations, changed by Eric (Eric's decision 2026-10-27)**: a rotation is refused (`conflict`) while the active key is younger than the cooldown, enforced inside the rotation transaction (4.4), with no extra lock; `-revoke-previous` (the leaked-key case) is exempt. *Superseded by Eric's decision 2026-10-27:* A: unconditional, with an optional `-from KID` compare-and-swap (the earlier recommendation; `-from` is dropped). B: coalesce within N seconds. The cooldown default is **1 hour (3600 seconds), configurable** (Eric's decision 2026-10-27).
11. **Default rotation interval.** **DECIDED: B, 720 hours (Eric's decision 2026-10-27)** (ratified). A: off. C: 2160 hours. A minimum of 48 hours applies in every case.
12. **Cloud SDK linking.** **DECIDED: B, behind build tags, with all four backends in the first delivery, so the default binary stays lean (Eric's decision 2026-10-27)** (changed by Eric). *Superseded by Eric's decision 2026-10-27:* A, the AWS SDK always linked (the earlier recommendation).

## 6. Dependencies

- **Issues:** #8 builds on #6. The registry schema, the rotation transaction and the key-set issuer all come from #6.
- **Go modules for 4.7 (Eric's decision 2026-10-27).** All are new direct requirements.
  - `github.com/sigstore/sigstore v1.11.1` (on the module proxy), used through its CGO-free packages `pkg/signature/kms/aws`, `pkg/signature/kms/gcp`, `pkg/signature/kms/azure` and `pkg/signature/kms/hashivault`.
  - The cloud SDKs those packages pull in, each linked only under its backend's build tag (4.10). Their versions follow from the sigstore version and are listed in the task that adds them; the brief's earlier AWS SDK versions (`aws-sdk-go-v2 v1.47.3`, `service/kms v1.61.4`) no longer apply as direct requirements. Check that each tag's transitive modules support the repository's go 1.27 toolchain (`go.mod:3-5`) and are CGO-free.
  - `go-jose/v4 v4.1.6` is unchanged.
- **External services, one per backend in use:** an AWS KMS, Google Cloud KMS or Azure Key Vault asymmetric P-256 signing key, or a HashiCorp Vault Transit P-256 key. The server's credentials come from each SDK's default chain. CI never calls a real service (section 8).
- **CI:**
  - `tidy-check` and `generate-check` (new modules, regenerated code);
  - `vet-cross` and `windows.yml` (each backend's SDK on every GOOS, with and without its tag);
  - `release-check`, if the binary size or license list is checked there (to confirm);
  - the import-rule scripts (m5 and m7) and `check-gates-selftest` for any new rule block.

## 7. Implementation outline (provisional, assuming the recommendations)

### Tasks

1. **Store registry.**
   - Files: both `0001_initial.sql` files (in place); `internal/store/signing.go` (new contract: `PutSigningKey`, `SigningKeys`, `RetireSigningKey(kid, retiredAt, removeAfter)` as a compare-and-swap, `DeleteSigningKey(kid)`, `DeleteSigningKeysBefore(now)`); `internal/store/sqlstore/signing.go`.
   - Tests first, in `internal/conformance/store/signing_test.go`, on both engines:
     - `TestSigningKeyPutConcurrent` is rewritten from "one row" to "one active row";
     - the compare-and-swap on retire, and removal by time;
     - rejection of an unknown state or provider, and of a PEM on a non-store row or a missing one on a store row (Definition of Done B).
2. **Key-set issuer in `internal/auth/token`.**
   - Change: `Issuer` takes a `KeySource` interface (standard library plus `keys` only). It signs with the active key, verifies by `kid` against all keys, and publishes all keys in `JWKS()`. Reload on an unknown `kid` is single-flight and rate-limited.
   - Tests: fake clock and fake source; an unknown `kid` reloads once; random `kid`s are rate-limited; a removed key fails.
   - `Verifier{Own: issuer}` and the handlers keep their types. The leaf check in `m5-exit-checks.sh` is unchanged and must pass.
3. **Provider port and store provider.**
   - Files: `internal/keyprovider`, a leaf (standard library and `internal/auth/keys`). The store provider sits beside the rules: `internal/core/identities/signing.go` replaced, or a new `internal/core/signingkeys`.
   - Rules: `EnsureActive` at start, which replaces `LoadOrCreateSigningKey`; `Rotate(revokePrevious)` (the cooldown check of 4.4 inside its transaction); `RemoveExpired`; and the keyring that implements `token.KeySource` from the Store.
   - Tests: the State note in `TestSigningKeyLifecycle` (second run, overlap, leftovers); a corrupt row is refused; a mismatched provider is refused; a rotation inside the cooldown is refused with `conflict`, one after it succeeds, and `revokePrevious` inside the cooldown succeeds (on both engines).
4. **Maintenance jobs.**
   - Change: add "signing key rotation" and "signing key removal" to `Jobs()` (`service.go:78-103`). They run behind a `SigningKeys` field that is nil in local mode, like `Archiver`.
   - Tests: tick tests with a fake clock; `RunJob` without the lock is still safe.
5. **Server wiring and configuration (Decision 85).**
   - Files: `internal/server/config` adds `signing_key_uri` (empty means the Store-row provider; otherwise a key URI whose scheme is `awskms`, `gcpkms`, `azurekms` or `hashivault`, anything else refused, and a scheme whose build tag is not compiled in refused with a message naming the tag), `signing_key_rotation_hours` (0 or at least 48) and `signing_key_rotation_cooldown_seconds` (positive; default 3600, 1 hour, Eric's decision 2026-10-27). `internal/server/cmd/cmd.go` wires them; the Ready line prints the active `kid`.
   - Tests: `config_test.go` for every rule. `cmd_test.go` adapts `TestSigningKeyStoreLifecycle` and `TestSigningKeyAfterFailedStart`.
6. **Host command.**
   - Files: `internal/server/cmd/admin.go` adds `signing-key list|rotate [-revoke-previous]`, which builds the provider from the same configuration.
   - Tests: `admin_test.go` on both engines.
7. **Admin API (Decision 6, Option B).**
   - Wire: `api/openapi.yaml` adds `listSigningKeys` and `rotateSigningKey` under `admin`, with an Idempotency-Key, as `adminReset` has. `info.version` and `version.Protocol` stay `1.0.0` (Eric's decision 2026-10-27), then `make generate`.
   - Code: the handlers (`internal/server`, through `WriteProblem`); `bus.Admin` methods; `internal/bus/local` answering `unavailable` (not retryable); `internal/bus/remote`; the CLI commands. The admin check goes through `internal/core/access` (`TestAccessRuleSites` must stay green, with no new `Admin` read outside `access`).
   - The handlers also need the nil-issuer guard that `Jwks` has (`auth_handlers.go:122`, `issuerUnavailable()`), because a local profile runs the HTTP handler in process with no signing key (`internal/client/server.go`).
   - Tests: the Bus suite on both transports (the `inproc` run covers the handler's guard), and the ACL matrix row for a non-administrator.
8. **KMS adapter.**
   - First, read the sigstore v1.11.1 modules in the module cache and record, per backend, the key URI grammar, how a URI resolves to a pinned key version, whether the signer takes a message or a digest, the signature encoding for P-256, and how a context reaches the call (the adapter must pass the request context). Decision 3 (open) depends on the answers.
   - Files: `internal/keyprovider/{awskms,gcpkms,azurekms,hashivault}` (each the only importer of its sigstore and SDK packages, each file under its build tag, 4.10) and `internal/keyprovider/kmsfake`, a signer that models the resolve-and-sign behavior with an in-memory P-256 key.
   - Tests:
     - each adapter against an `httptest` server that speaks that service's protocol where the SDK allows a base endpoint, signing with an in-memory P-256 key, which checks the request mapping (digest versus message), DER versus raw signatures, the resolved key reference, and a refused key type (an Ed25519 external key is refused, 4.10);
     - the rotation rules against `kmsfake` on both engines;
     - a build matrix test: the default binary refuses each key URI scheme with the tag name, and each tagged build accepts its own scheme only;
     - an opt-in live test per backend behind an environment variable, never in CI.
9. **Replica tests.**
   - Files: `internal/conformance/bus/replicas_test.go` adds `TestReplicasSigningKeyRotation`: two replicas rotate at once and end with one active key; a bearer minted on one after the other rotated verifies on both; a revoke is refused on both after the reload.
   - Rotation over TCP goes in `TestAuthEndToEnd` (`internal/conformance/bus/auth_test.go`, which builds its key with `LoadOrCreateSigningKey` at `:104` and its issuer at `:181` and `:419`). Those call sites, `identities_test.go:63` and `internal/e2e/e2e_test.go:630`, move to the keyring.
   - Gates: update the counts in `scripts/m5-exit-checks.sh` (11 leaf subtests per engine today) and `scripts/m7-exit-checks.sh` (`TestReplicasWake`, if it changes).
10. **Docs and ADRs** (below).

### Gates for every task

- `make verify` (the SQLite and Postgres suites, `check-gates`).
- `make test-conformance`, which covers both bus transports (`local`, `inproc`).
- `scripts/m5-exit-checks.sh` (the auth leaf rule) and `scripts/m7-exit-checks.sh` (the import rules and replica counts).
- `scripts/m4-exit-checks.sh` (Decision 47) after task 7.
- `make check-gates-selftest` after any new rule block.
- Green `ci.yml` and `windows.yml` on the pushed commit.

### New import edges

- `internal/core/...` imports `internal/keyprovider`, a leaf. That is allowed: it is neither `internal/gen` nor `internal/server`.
- `internal/server/cmd` imports each `internal/keyprovider/<backend>` package from a file under that backend's build tag.
- Each `internal/keyprovider/<backend>` package imports its sigstore and cloud SDK packages, `internal/keyprovider` and `internal/auth/keys`.
- New rule, proposed for `m7-exit-checks.sh` beside the Slack rule:
  - `github.com/sigstore/sigstore/pkg/signature/kms/...` and each cloud SDK are imported only by the matching `internal/keyprovider/<backend>` package, in any file;
  - `internal/core/...`, `internal/bus/...`, `internal/cli`, `internal/mcpserver`, `internal/client` and `internal/server` (the package) never import a backend package;
  - `internal/keyprovider` imports only the standard library and `internal/auth/keys`.
- `internal/auth/...` gains no edge.

### Docs and ADRs

- A new ADR 0012, "Signing key rotation and key providers", recording Eric's answers (the key URI model, ES256 for external keys, the build tags, the rotation cooldown and its `-revoke-previous` exemption).
- Amendments, each marked as superseding the singleton: ADR 0002 (`:197-201`, `:280-284`, `:480-487`), ADR 0004 (`:310-321`). Gate-0 item 2 is left as history, with a pointer.
- `docs/ops/deploy.md` (the signing-key section), `README.md:13`, `backup.md` (with KMS a backup holds no private key; a restore re-trusts only the keys in the backup, so newer bearers re-mint once), `configuration.md` (the new settings rows, including the cooldown, and line 124), and `health.md` (the new job names).
- `CLAUDE.md`: the signing-key and import-rule lines.

## 8. Risks and test strategy

- **Two servers rotating at once.** Serializable transactions plus the partial unique index. The loser reruns and finds the key not due, or fails the compare-and-swap. Test: `TestReplicasSigningKeyRotation` on both engines, plus a Store-level concurrent `Put` and `Retire`.
- **A second run.** A scheduled rotation within the interval is a no-op. An external key URI still resolving to the active key is a no-op. A rotate-now inside the cooldown is refused with `conflict` and one after it rotates again; only `-revoke-previous` rotates inside the cooldown. Test: run the job and the command twice and check the rows.
- **Overlapping runs.** The maintenance lock serializes scheduled runs. The host command, the API and `RunJob` rely on the transaction alone, which is tested without the lock: the cooldown check and the partial unique index leave one winner, and the loser is refused. Two concurrent `-revoke-previous` calls each rotate, by design.
- **Leftovers from a failed run.** The store provider writes nothing outside the transaction, so a crash leaves the old state. An external key under 4.8 Option A (to be confirmed) creates nothing in the cloud, so there is nothing to orphan. A corrupt row stops the start and is never overwritten, as today (`cmd.go:43-48`). Test: inject a failure after `Next` and check the rows are unchanged.
- **Stale replicas.** A replica minting with a key retired a moment ago is covered by the reload term in `remove_after`. A replica refusing a new `kid` is covered by the unknown-`kid` reload. Removal of a key while a slow replica still mints with it would yield 401s. The 30 s reload against a removal window of at least 24 hours makes that practically impossible, and the client re-mints anyway.
- **A flood of unknown `kid`s.** Reloads are rate-limited and single-flight. Test: N random `kid`s cause at most one Store read per window.
- **Key service failures.** A mint returns `temporarily_unavailable` (503), as the exchange does for a down JWKS (`auth_exchange.go:101-105`). Verification is unaffected. The external call takes the request context with a bounded timeout. Signature encoding: with ES256 external keys, each adapter test and one opt-in live run per backend must show a signature that verifies as a JWS (raw `r||s`), converting from DER where the signer returns DER.
- **Restore from backup.** Keys created after the backup are gone. Their bearers fail once and re-mint. Document this.
- **Windows CI.** Nothing here is OS-specific. The risk is only the SDK compiling and `vet-cross` across GOOS, plus the host command's tests (no paths or permissions involved).
- **Binary size and supply chain.** The cloud SDKs are behind build tags, so the default binary stays lean; a tagged build adds that backend's modules. Measure each tagged build in task 8 and record it in the ADR. The tagged builds multiply the CI matrix (task 8's build matrix test).
- **Rotation cooldown.** A cooldown that is too long blocks a legitimate second rotation after a failed one; the leaked-key path (`-revoke-previous`) is exempt for that reason. Test: a rotation refused by the cooldown leaves the rows unchanged.

## 9. Size estimate

**XL** overall: #6 alone is L (a schema and Store contract change on both engines, a key-set issuer, rules, two maintenance jobs, a host command, an admin API, replica tests), and #8 grows from M to L after Eric's decision 2026-10-27 (a port, a store provider, four backend adapters over the sigstore modules, each with a fake and an HTTP test and a build tag, a build matrix in CI, a new import rule and trust-boundary docs per backend). The cooldown adds a small check to the rotation transaction. The overall size stays XL.
