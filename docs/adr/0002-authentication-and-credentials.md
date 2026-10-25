# ADR 0002: Authentication and credentials

Status: Accepted (Eric, 2026-10-19)

Covers Decisions 19, 20, 21, 22, 23, 33, 36, 55, 56, 57, 58 and 63 of
`HANDOFF.md`, all made by Eric on 2026-10-19, and Decisions 70, 71, 72,
73, 74, 84 and 85 (Eric, 2026-10-20; see "Decisions of 2026-10-20"
below).

## Context

v1 has no authentication. Every process opens the shared SQLite file, and
the name a session registers under is whatever `agentbus identity` prints
for the working directory (`internal/cli/identity.go`). That is fine for a
file only the local user can read. A server with a network endpoint
(ADR 0001) needs real authentication: a remote caller must prove which
identity it is, and a lost or leaked credential must be recoverable
without trusting the caller.

Configuration in v1 is partly repo-local. With a server, configuration
carries credentials, and anything repo-local can end up in a worktree or
in a commit.

## Decision

Eric decided:

1. **Real authentication: Ed25519 key pairs with OAuth `private_key_jwt`
   (RFC 7523).** The client signs an assertion and exchanges it at a token
   endpoint for a short-lived bearer token. This leaves room for other
   OAuth flows later, such as cloud workload identity. (Decision 19)
2. **Key algorithms: Ed25519 by default, ES256 also accepted** (for KMS
   keys that cannot sign Ed25519). Keys are PKCS#8 PEM, mode 600
   (owner-only ACL on Windows). (Decision 55)
3. **Who validates what.** The server validates JWTs from configured
   trusted issuers (JWKS) and maps `iss` plus `sub` to an identity. It
   stores public keys only. HANDOFF notes this was not asked separately; it
   follows from Decision 19. (Decision 57)
4. **Trust-on-first-use registration.** An unauthenticated `register` may
   claim only a name that has no credential yet. Open registration is on by
   default when the server listens only on loopback (127.0.0.1 or ::1), off
   otherwise. Enrollment tokens are supported for home labs: a token
   authorizes binding a public key to a name. (Decision 20)
5. **Listener classification.** Loopback (including `::ffff:127.0.0.1`), a
   Unix socket, or a hostname whose every address is loopback counts as
   local. There is an explicit `-registration=open|enrollment`
   override (for Docker). A lost credential is recovered only with an admin `reset-credential`
   command. (Decision 58)
   **Amended (Eric, 2026-10-23, Slack bridge Decision 21):** the `closed`
   registration mode is removed; the override names `open` or `enrollment`,
   and `closed` is an unknown value like any other.
6. **Global config only, AWS-style, under `$HOME`.** There is no repo-local
   config, because of worktrees and the risk of committing credentials.
   Named profiles are chosen in this order: command-line flag, then
   environment variable, then the profile named in the repo's `.local/`
   JSON file, then `default`. (Decision 21)
7. **Profile settings and credentials are separate files in the same
   directory. One file per credential. Files are only ever replaced
   atomically.** (Decision 22)
8. **Paths and names.** Config lives in `~/.config/agentbus2/`, with
   credentials at `credentials/<profile>/<identity>`. The repo file is
   `.local/agentbus2.json` and may only select a profile; any credential
   setting found there is rejected, which stops a malicious repository from
   setting `credential_process`. The environment prefix is `AGENTBUS2_`.
   (Decision 33; ADR 0005 covers the rest of the naming)
9. **A `credential_process` escape hatch**, so glue code can use cloud
   vaults or other secret stores. (Decision 23) Its contract:
   - input on stdin: token endpoint and audience, identity, `jti`;
   - output: versioned JSON returning one of a private key PEM, a signed
     client assertion, or an access token plus expiry;
   - results are cached until expiry. (Decision 56)
10. **The local SQLite profile has no authentication.** Identity there is
    just the registered name. (Decision 36)
11. **AWS workload identity is deferred** past 2.0.0. AWS hosts use key
    pairs or `credential_process`. (Decision 63)

## Consequences

Positive:

- The server never holds a secret that can be replayed: it stores public
  keys only, and bearer tokens are short-lived.
- A home lab gets a usable default (open registration on loopback) without
  any enrollment ceremony, while anything reachable from the network
  requires an enrollment token or an administrator.
- `credential_process` returning an access token keeps the door open for
  external issuers without changing the server: it already validates JWTs
  from configured issuers.
- One file per credential, replaced atomically, means a concurrent reader
  never sees a half-written key, and a rotation touches one file.
- A repository can select a profile but cannot name a program to run, so
  cloning an untrusted repository cannot execute code through the bus
  configuration.

Negative:

- Every agent on a networked server needs a key pair and a bound name
  before it can do anything; `agentbus2` has to make enrollment and TOFU
  registration simple enough that this is not a setup burden.
- A lost private key cannot be recovered by the agent; only an
  administrator's `reset-credential` unbinds the name.
- Two code paths for identity: authenticated (server mode) and
  name-only (local SQLite profile). ADR 0003's "one credential equals one
  identity" does not hold in local mode.
- The Docker case needs an explicit `-registration` flag; the listener
  classification alone does not decide it (Decision 58).
- Caching `credential_process` results until expiry means a revoked
  external token keeps working locally until it expires.

## Alternatives considered

- **Repo-local configuration** was rejected because of worktrees and the
  risk of committing credentials (Decision 21).
- **Cloud workload identity (AWS) now** was deferred past 2.0.0; key pairs
  and `credential_process` cover AWS hosts until then (Decisions 19, 63).

## Decisions of 2026-10-20 (Eric)

From Eric's answers to the design spec's section 12
(`.local/review-answers-2026-10-20.md`):

- **Decision 70.** The agentbus2 server is the issuer. A registered key
  signs a client assertion with `iss` and `sub` both equal to the identity
  name and `aud` equal to the token endpoint; the server verifies it
  against the stored public key, mints its own short-lived bearer JWT, and
  validates that JWT at every call. External issuers (JWKS, `iss` plus
  `sub` mapping, Decision 57) are configured in addition. (Question 7)
- **Decision 71.** A profile setting `identity` chooses the credential,
  overridable by a flag and an environment variable; a profile with
  exactly one credential uses it without a setting. (Question 8)
- **Decision 72.** The non-loopback registration default is `enrollment`.
  (Question 9)
- **Decision 73.** `agentbus2 server admin ...` on the server host works
  against the store without a credential: it flags an identity as
  administrator and resets credentials. The admin API section requires an
  identity carrying that flag. (Question 10; also ADR 0003)
- **Decision 74.** The profile settings file is
  `~/.config/agentbus2/config.json`, a JSON object with a `profiles` map.
  (Question 11)
- **Decision 84.** The token endpoint is `POST /v1/auth/token`; the bearer
  token lifetime is a server setting defaulting to one hour. (Question 21)
- **Decision 85.** The server reads a configuration file (path given by a
  flag), overridden by `AGENTBUS2_SERVER_*` environment variables, then by
  flags. (Question 22)

Consequences: the server now holds a signing key for the tokens it mints,
in addition to the public keys it stores; a one-hour token keeps
`credential_process` calls rare; and `reset-credential` on the host means
a lost administrator credential never locks the deployment.

## Open questions for Eric

All five resolved on 2026-10-20:

1. Assertion `iss` and `sub`, and the issuer: the identity name for both,
   and the server mints the bearer token (Decision 70).
2. Identity selection within a profile: the `identity` setting, flag or
   environment variable, or the only credential (Decision 71).
3. Non-loopback default: `enrollment` (Decision 72).
4. `reset-credential` and admin bootstrap: a server-host command against
   the store; the API requires the admin flag (Decision 73).
5. Settings file: `~/.config/agentbus2/config.json` with a `profiles` map
   (Decision 74).

## Milestone 4 addendum (2026-10-21)

- **Choice 5, the authentication seam.** `server.Authenticator` is
  `Authenticate(r *http.Request) (identity string, err error)`. The
  authentication middleware always stores the result under the `identity`
  context key (even `""`) and never refuses; the validator's
  `AuthenticationFunc` succeeds exactly when `IdentityFrom(ctx)` is
  non-empty, so which operations need an identity is stated once, in the
  document. `server.RefuseAll` (the only authenticator `agentbus2 server`
  constructs until Milestone 5) returns `""`: every protected operation is
  401 `unauthenticated`. `server.InProcess` returns the value of a second,
  unexported context key that only `server.InProcessTransport(h, identity)`
  sets; a context value cannot cross a socket, and the middleware overwrites
  the `identity` key, so a `RefuseAll` server behind the transport still sees
  `""`. `InProcess` is the local mode of Decision 36 (identity is the
  registered name, the client is in the same process).
- **Item 3** (interim flags `-listen`, `-sqlite`, `-postgres` until
  Decision 85's configuration file and `AGENTBUS2_SERVER_*` variables in
  Milestone 5): Decision (Eric, 2026-10-21), as planned
  (`docs/superpowers/specs/2026-10-21-gate-0-decisions.md`, Milestone 4
  item 3); test `TestRunFlags`, whose marker Milestone 5 removes.

## Gate 0 decisions for Milestones 5 to 7 (Eric, 2026-10-21)

Recorded here ahead of those milestones' addenda, from
`docs/superpowers/specs/2026-10-21-gate-0-decisions.md`; each milestone's
addendum restates the item with its test when it lands.

- **Milestone 5 item 1.** Admin reset takes a scope: `data` (default) keeps
  identities, keys, admin flags and enrollment tokens and wipes the rest;
  `everything` also wipes identities, keys and enrollment tokens except the
  calling administrator's row and keys.
- **Milestone 5 item 2.** The server's token signing key is a Store row,
  created at startup when absent, so replicas share it without a volume;
  stored as plaintext PEM, so database read access is equivalent to admin
  and the deployment docs say so. Rotation (including rotate now) is issue
  #6; a KMS or vault key provider is issue #8. There is no
  `signing_key_file` setting.
- **Milestone 5 items 3, 4 and 6.** As planned: a configured `issuer` URL
  whose default is the bound address; `reset-credential` leaves minted
  tokens valid until `exp`; `register` takes no `Idempotency-Key` (spec 3.4
  lists the exemptions).
- **Milestone 5 item 5.** Both mechanisms for an external issuer's
  subject: `agentbus2 server admin create-identity NAME` creates the row
  and its `general` subscription, and the first valid external token for an
  unmapped name auto-creates the row, but only when the registration mode is
  `open`; an auto-created row is never admin. Superseded by Decision 10
  (federated identity, 2026-10-23): the auto-create is removed.
- **Milestone 5 item 7.** `Register` refuses trust-on-first-use (no
  enrollment token) on a keyless identity row whose admin flag is set:
  `forbidden` "identity %q requires an enrollment token". A non-admin
  keyless row can still be claimed in `open` mode.
- **Post-build decision 1 (Eric, 2026-10-21), extending Milestone 5 item
  7.** An enrollment token claims a keyless administrator row only when it
  is bound to that name. An unbound token presented for such a row is
  refused `forbidden` "identity %q requires an enrollment token bound to
  it" and is not consumed, so a token issued for new agents cannot take
  over `root` after `reset-credential`. An unbound token still claims a
  free name or a keyless non-admin row. Tests:
  `TestRegisterUnboundTokenKeylessAdmin` (core and server),
  `TestIdentitiesUnboundTokenKeylessAdmin` (both engines).
- **Milestone 6 items 1, 3, 5, 6 and 7.** As planned: the state file holds
  the full planned shape, the resume token included, mode 0600; `receive`
  keeps `wait_seconds`, documented as replacing an armed watch; `init` in a
  repository creates and selects profile `<repo>`; `register -server URL`
  creates the profile; the bearer is cached on disk, 0600, until 60 s
  before `exp`.
- **Milestone 7 item 4.** The assertion `jti` replay set is a shared Store
  table (`assertion_jti`) consulted and written in the token transaction,
  so an assertion is single-use across replicas; Milestone 5's per-process
  `token.Replay` was the interim form and is deleted (Milestone 7 Task 9;
  see the Milestone 7 addendum).

## Milestone 5 addendum (2026-10-21)

Milestone 5 built authentication, registration and the host and admin
commands. These are Claude's choices under the spec's delegation, then the
items Eric decided on 2026-10-21
(`docs/superpowers/specs/2026-10-21-gate-0-decisions.md`, "Milestone 5").
The plan is
`docs/superpowers/plans/2026-10-21-milestone-5-auth-registration-policy.md`;
the choice numbers below are its.

### Choices delegated by the spec

1. **JOSE library.** `github.com/go-jose/go-jose/v4` v4.1.6. Candidates:
   `github.com/golang-jwt/jwt/v5` (no JWK or JWKS, so the register body, the
   JWKS endpoint and external keys would need hand-written JWK parsing) and
   `github.com/lestrrat-go/jwx/v3` (complete, but its dependencies would
   enter the client binaries). go-jose has no module dependencies, and its
   `ParseSigned` takes an explicit algorithm allowlist, which the server
   always passes (`EdDSA` and `ES256`; for an external issuer its configured
   list, by default those two and `RS256`).
2. **Token endpoint wire shape.** `POST /v1/auth/token` takes a form body
   (`grant_type=client_credentials`, the `jwt-bearer`
   `client_assertion_type`, `client_assertion`) and answers `access_token`,
   `token_type` and `expires_in`.
3. **Token endpoint errors are problems.** A malformed assertion is 400
   `validation`; a failed check is 401 `unauthenticated` with a detail that
   names the check. RFC 6749's `{"error": "invalid_client"}` body is not
   emitted: this departs from the OAuth error format, the price of Decision
   46 (every error is problem+json).
4. **Client assertion rules.** `alg` `EdDSA` or `ES256`, `kid` the key's
   thumbprint, `iss` = `sub` = the identity, `aud` the token endpoint,
   `jti` required, `exp` at most `assertion.MaxLifetime` (5 minutes) plus
   the 60 s leeway ahead. The token endpoint rejects a repeated `jti` through the shared
   `assertion_jti` table, written in the token transaction (Decision (Eric,
   2026-10-21), Milestone 7 item 4, B). Milestone 5 built this as a
   per-process `token.Replay`, which let a restart or each replica accept
   one reuse of a still-valid assertion; Milestone 7 deleted it (see the
   Milestone 7 addendum).
5. **Bearer rules.** `iss` = `aud` = the issuer URL, `sub` the identity,
   `exp` = `iat` + `token_lifetime_seconds` (default 3600), `kid` must be
   the issuer's, 60 s leeway on every time claim. A token for a name with no
   row verifies, then fails `not_registered` (403) in the core.
6. **Signing key.** One Store row (`signing_keys`, a singleton), created at
   the first start when absent, as plaintext PKCS#8 PEM (Decision (Eric,
   2026-10-21), Milestone 5 item 2). Consequence: read access to the
   database is equivalent to the admin flag. Rotation is issue #6, a key
   provider issue #8. There is no `signing_key_file` setting.
7. **One canonical issuer URL.** The `issuer` setting, defaulting to
   `http://<bound address>`. A request whose `Host` differs still
   authenticates: the issuer is a configured string. A `unix:` listener
   requires `issuer` (exit 2); a non-loopback listener with `issuer` unset
   starts with a warning that clients will see the bound address. A unix
   socket file is mode 0600 (Decision 58); on Windows it also gets an
   owner-only ACL (post-build decision 9).
8. **Registration semantics.** The same `(identity, kid)` is checked first
   inside the transaction, so a retry answers `Created false` and consumes
   nothing; a different key for a name with a credential is `conflict`;
   `open` is trust on first use, `enrollment` requires a token consumed in
   the same transaction. The transaction is re-run on
   `ErrConflict`, at most twice. Decision (Eric, 2026-10-21), Milestone 5
   item 6: `register` takes no `Idempotency-Key`.
   **Amended (Eric, 2026-10-23, Slack bridge Decisions 17 and 21):** the
   `closed` mode is removed. Whether an enrollment token is bound to a name
   is the administrator's choice when minting it; no server setting forbids
   unbound tokens. A scoped identity is claimed only with the token bound
   to it, in both modes. A server that reaches the register handler with an
   unset or unrecognized mode (only `server.New` called directly can) still
   refuses every register (`forbidden`, 403) and the detail says why; it
   never opens.
9. **Enrollment tokens.** `abe_` plus 32 random bytes, shown once, stored as
   SHA-256 hex; optional bound identity, expiry (default 24 h), uses
   (default 1). An unbound token claims a free name or a keyless non-admin
   row, never a keyless administrator row (post-build decision 1).
10. **Registration mode is decided once at start.** `-registration` (or the
    setting), else `open` on a local listener and `enrollment` on any other
    (Decision 72). `agentbus2 server admin` never consults `listen` or
    `registration`.
11. **External issuers** are configuration (`issuers`), not rows. A subject
    gets its row from `server admin create-identity` or, in `open` mode only,
    from its first valid external token (never admin): Decision (Eric,
    2026-10-21), Milestone 5 item 5. Superseded: direct acceptance is gone
    (federated identity addendum, Decision 10).
13. **`reset-credential`** deletes an identity's keys. Minted bearers stay
    valid until `exp`: Decision (Eric, 2026-10-21), Milestone 5 item 4, as
    planned.
14. **Server configuration** is `internal/server/config`;
    `config.Settings()` lists the settings. Precedence: defaults, file,
    `AGENTBUS2_SERVER_*`, flags.
17. **Local mode** keeps no authentication (Decision 36):
    `identities.Service.RegisterLocal` creates a keyless row.

### The authentication middleware rule

The middleware never refuses by itself. A bearer error reaches the
validator's `AuthenticationFunc` and matters only on an operation with a
non-empty security requirement. A stale bearer sent to `POST /v1/auth/token`
is therefore ignored and a fresh token is minted, which closes Milestone 4
choice 5's "err: 401" reading. Proof over TCP: subtest
`stale-bearer-on-token-endpoint` of `TestAuthEndToEnd`.

### Gate 0 decisions (Decision (Eric, 2026-10-21))

Each line: the answer, the test that encodes it, and
`docs/superpowers/specs/2026-10-21-gate-0-decisions.md`.

- **Item 2, signing key in a Store row.** `TestSigningKeyLifecycle`,
  `TestIdentitiesSigningKeyConcurrent`, `TestSigningKeyStoreLifecycle`.
- **Item 3, configured `issuer`.** `TestIssuerIsConfiguredNotRequestHost`.
- **Item 4, `reset-credential` keeps minted tokens.**
  `TestResetCredentialKeepsMintedToken`, and subtest
  `reset-credential-keeps-token` of `TestAuthEndToEnd`.
- **Item 5, both mechanisms for external subjects.**
  `TestAdminCreateIdentity`, `TestExternalIssuerBearer`,
  `TestRunWiresExternalIssuers`. Superseded by Decision 10 (federated
  identity, 2026-10-23): `TestExternalIssuerBearer` is deleted (replaced by
  `TestBearerRefusesCorporateToken`) and `TestRunWiresExternalIssuers` now
  asserts that a corporate bearer is refused.
- **Item 6, `register` takes no `Idempotency-Key`.** A spec 3.4 exemption,
  listed there. `TestRegisterOpenTOFU` (same key twice).

Milestone 4 items 2 (auth paths) and 3 (the interim server flags replaced by
Decision 85's configuration) are decided as planned and built.

### Milestone 5 exit

`TestAuthEndToEnd` (`internal/conformance/bus/auth_test.go`) runs on SQLite
and Postgres through `cmd.Run` on `0.0.0.0:0` with no `-registration`:
registration without a token is refused (the `enrollment` default), a
protected call without a bearer and one with a forged bearer are 401, a
non-administrator is refused the admin operations, and an identity the ACL
omits is refused read and write while the creator and an administrator
succeed. Gate self-test blocks 14 and 15 break the bearer signature check
and the admin flag check and require the exit test to fail.
`scripts/m5-exit-checks.sh` counts the subtests and checks the import rule.

## Milestone 6 addendum (2026-10-21)

Milestone 6 built the client side: profiles, credential files, the token
source and the CLI that registers and logs in. These are Claude's choices
under the spec's delegation, then the items Eric decided on 2026-10-21
(`docs/superpowers/specs/2026-10-21-gate-0-decisions.md`, "Milestone 6").
The plan is `docs/superpowers/plans/2026-10-21-milestone-6-client-side.md`;
the choice numbers below are its.

- **Choice 1, directories.** Config is `$AGENTBUS2_CONFIG_DIR`, else
  `$XDG_CONFIG_HOME/agentbus2`, else `$HOME/.config/agentbus2`; data is
  `$AGENTBUS2_DATA_DIR`, else `$XDG_DATA_HOME/agentbus2`, else
  `$HOME/.local/share/agentbus2`. Windows uses `%APPDATA%\agentbus2` and
  `%LOCALAPPDATA%\agentbus2`. `HOME` is read from the command's environment,
  then `os.UserHomeDir`; `os.UserConfigDir` is not used (on macOS it is
  `~/Library/Application Support`, and the spec says `~/.config`).
- **Choice 2, the file shape.** `config.json` is `{"profiles": {"<name>":
  {"mode", "server", "identity", "credential_process", "sqlite"}}}`, loaded
  strictly (an unknown key, `null`, trailing content or an empty file is an
  error; an absent file is no profiles). `mode` is `local` or `server`;
  `server` is an absolute `http` or `https` URL with no path, query or
  fragment and only in server mode; `credential_process` only in server
  mode; `sqlite` only in local mode. An undefined `default` is the built-in
  local profile; any other undefined name is an error naming the file.
- **Choice 3, selection.** `-profile`, then `AGENTBUS2_PROFILE`, then the
  nearest `.local/agentbus2.json` walking up to the nearest `.git`, then
  `default`. The repository file is `{"profile": "<name>"}` and every other
  key is rejected, so a cloned repository cannot name a
  `credential_process`.
- **Choice 4, identity.** `-identity`, then `AGENTBUS2_IDENTITY`, then the
  profile's `identity`, then, in server mode only, the one credential file
  of the profile when exactly one exists; otherwise an error naming the
  three ways to set it. A local profile has no credential to infer from.
- **Choice 5, credential files.** `<config dir>/credentials/<profile>/
  <identity>` is one PKCS#8 PEM block, written through a temp file at mode
  0600 and renamed. A file wider than 0600 is refused on load (Unix); on
  Windows an owner-only ACL is set after the rename. `register` generates
  Ed25519, or ES256 with `-algorithm ES256`.
- **Choice 6, the `credential_process` protocol.** Stdin is one JSON object
  `{"version": 1, "identity", "token_endpoint", "audience", "jti"}`; stdout
  is one JSON object with `"version": 1` and exactly one of
  `private_key_pem`, `client_assertion`, `access_token` (the last with
  `expires_at`). Any other shape, a non-zero exit, no output or more than
  64 KiB is an error naming the process. It runs with a 10 s context and its
  argv is executed without a shell. A returned client assertion is exchanged
  at once, because its `jti` is single-use; a returned private key is held in
  memory only.
- **Choice 7 and item 7, the token cache (Decision (Eric, 2026-10-21)).**
  Discovery reads `<server>/.well-known/oauth-authorization-server` once per
  process (changed by the federated identity addendum); the assertion `aud`
  is its `issuer` (formerly its `token_endpoint`), and the request goes
  to the client's base URL, so a server bound to `0.0.0.0`, which advertises
  `http://0.0.0.0:<port>`, is reachable at the address the client dials. The
  bearer is cached on disk at `<data dir>/tokens/<profile>/<identity>.json`
  (0600, atomic), reused until 60 s before `exp`, so hooks and one-shot
  commands do not exchange on every call. An entry minted for another server
  is a miss. The transport refreshes once on a 401 and replays the buffered
  request; a failed refresh is `unauthenticated`. Tests:
  `TestSourceCachesToken`, and step `9-stale-bearer-refreshes` of
  `internal/e2e`'s `TestSection24Sequence`.
- **Choice 14, `register` and `login` (item 6, Decision (Eric,
  2026-10-21)).** `register -server URL` on an undefined profile creates it
  as `{mode: server, server: URL, identity: I}`. A new key is first written
  to `credentials/<profile>/.pending/<identity>` (0600, `List` skips it) and
  moved onto the credential after the server answers 201, so a crash or lost
  response keeps the key; the next run reuses the pending key and the server
  answers the same key idempotently, without consulting the used enrollment
  token (post-build decision 6). A saved credential wins over a pending file
  (which is removed); an explicit `-algorithm` that disagrees with the pending
  key is exit 2 naming the file; a 409 for a pending key names the file and
  says to remove it or have an administrator reset the credential. Overlapping
  runs race on the pending file: the commit saves the registered key as the
  credential first, then removes the pending file only when it still holds
  that key, and leaves the other run's file otherwise. `login`
  discovers, asserts, exchanges and caches, and refuses a local profile with
  exit 2. Tests: `TestRegisterCreatesServerProfile`; in `internal/e2e` a
  server on `0.0.0.0` (enrollment mode) refuses `register` without a token
  as `forbidden` and accepts the same identity with one.

## Milestone 7 addendum (2026-10-21)

- **Milestone 7 item 4, as decided (B).** The assertion `jti` replay set is
  the `assertion_jti` table (schema version 3, both engines; version 4 keys
  it on `(identity, jti)`, post-build decision 4), consulted and
  written in the token transaction (Decision (Eric, 2026-10-21), Milestone 7
  item 4; `docs/superpowers/specs/2026-10-21-gate-0-decisions.md`).
  `token.Replay` is deleted. `Token` verifies the assertion, then one
  `Store.Tx` calls `PutAssertionJTI(identity, jti, exp + token.Leeway + 1 s)`
  and nothing else (Decision 26); `store.ErrConflict` answers 401
  `unauthenticated` "assertion replayed", and `Issuer.Mint` runs after the
  transaction. Replicas racing on one `(identity, jti)` serialize on the
  primary key and exactly one commits. A replica that dies between the commit and the mint
  leaves a row the client cannot reuse; it signs a fresh assertion, as it
  does after any failed exchange. The "assertion replay expiry" maintenance
  job deletes rows past their retention, so the table holds only assertions
  still acceptable. Encoded in `TestReplayPutConcurrent`, `TestReplayExpiry`,
  `TestReplayPerIdentity`, `TestOpenMigratesVersion3AssertionJTI`
  and `TestTokenReplayIsShared`, and in the
  `assertion-single-use-across-replicas` leaf of `TestReplicasWake` on both
  engines (one assertion is 200 through replica A and 401 "assertion
  replayed" through replica B); block `assertion-replay-ignored` of
  `scripts/check-gates-selftest.sh` breaks it.
- **Docker `-registration` pass-through (Decision 58).** The image's
  entrypoint is `/agentbus2 server` with an empty `CMD`, so
  `docker run <image> -registration open|enrollment` reaches the
  server; the container listens on a non-loopback address, where the default
  is `enrollment` (Decision 72).
- **Replicas share the issuer and the signing key.** Every replica must be
  configured with the same `issuer`, because a bearer carries `iss` and
  `aud` equal to it. The signing key is the `signing_keys` row of the shared
  database (Milestone 5 item 2, as decided: database read access equals the
  admin flag), so every replica reports the same `kid` with nothing to
  configure; whichever replica starts first creates it. Encoded in
  `TestReplicasWake/shared-key-and-issuer` and the check that exactly one
  replica prints `generated signing key`.

## Federated identity addendum (2026-10-23)

Spec: `docs/superpowers/specs/2026-10-23-federated-identity-design.md`. This
section starts in Task 7 (own tokens, client assertions and metadata) and
later tasks extend it.

### Decisions (Eric, 2026-10-23)

1. **Decision 1 (Eric).** One token type on every API: agentbus2's own
   bearer. Humans exchange a corporate token for it at the token endpoint
   (RFC 8693); agents keep `client_credentials` with `private_key_jwt`.
2. **Decision 2 (Eric).** Align with the OAuth and OIDC RFCs as closely as
   practical (spec section 8, below).
3. **Decision 3 (Eric).** Corporate provider: OCI IAM Identity Domains,
   OIDC; browser sign-in only, no device flow, no headless humans.
4. **Decision 4 (Eric).** Human identity is the email claim; other binding
   types may be added later.
5. **Decision 5 (Eric).** Two built-in reference lists, `users` and
   `admins`, whose entries match claims (email, group, subject) or name an
   agentbus2 identity; no directory.
6. **Decision 6 (Eric).** The gate: a token is issued only to an agent with
   a registered key or to a human whose claims match `users`.
7. **Decision 7 (Eric).** Admins come from `admins`.
8. **Decision 8 (Eric).** ACLs keep naming identities; an admin pre-creates
   a human by email and the first sign-in binds to it.
9. **Decision 9 (Eric).** TUI parity: the same corporate path and the same
   identity as the Slack human, differing only in the session display name.
10. **Decision 10 (Eric).** Exchange only: direct acceptance of external
    tokens and the static `subjects` map are removed; a human's first
    sign-in creates the identity in every registration mode.
11. **Decision 11 (Eric).** TUI identity by mode: the OS login name in local
    mode; the exchange's identity in every network mode.
12. **Decision 12 (Eric).** Admin reset scopes: `data` keeps `users`,
    `admins` and bindings; `everything` also wipes humans, bindings and
    `users`, and reduces `admins` to the caller's own entry.
13. **Decision 13 (Eric, after the plan review).** A `users` miss is
    `invalid_request` (RFC 8693 section 2.2.2).

### Claude's calls (ratified by Eric, 2026-10-23)

- `response_types_supported` is `[]` in the metadata (RFC 8414 requires the
  member; there is no authorization endpoint).
- `/.well-known/openid-configuration` keeps serving the same document for
  existing clients; agentbus2 does not claim to be an OpenID provider.
- The server keeps accepting the token endpoint URL as a client assertion
  `aud` during the transition.
- `client_id` on an agent token is `sub`; on an exchanged token it is the
  provider token's `client_id`, else its `azp`, else `sub`.
- The token endpoint answers 503 `temporarily_unavailable` and 500
  `server_error` for an unconfigured issuer or an internal failure.

### Standards alignment

- **Item 8.2, authorization server metadata (RFC 8414).** The server
  serves `/.well-known/oauth-authorization-server` and, as the same
  document, `/.well-known/openid-configuration`. Members: `issuer`,
  `token_endpoint`, `jwks_uri`, `response_types_supported: []`,
  `grant_types_supported: ["client_credentials"]`,
  `token_endpoint_auth_methods_supported: ["private_key_jwt"]` and
  `token_endpoint_auth_signing_alg_values_supported: ["EdDSA", "ES256"]`.
  The metadata gains the token-exchange grant (and the `none` method, the
  exchange's public client) only in Task 11, when the grant exists. The
  client discovers from the RFC 8414 path and requires `issuer` and
  `token_endpoint`; `Discover` (`GET /v1/discover`) adds
  `authorization_server` and keeps `openid_configuration`.
- **Item 8.3, client assertion `aud` and `typ`
  (draft-ietf-oauth-rfc7523bis).** Clients send `aud` = issuer URL and
  `typ: client-authentication+jwt`. The server accepts `aud` = issuer URL
  or the token endpoint URL; the token endpoint URL stays an accepted
  audience until the draft is published and every shipped client sends the
  issuer. `assertion.Parse` accepts only no `typ`, `JWT` or
  `client-authentication+jwt` (case-insensitive), so an access token
  (`at+jwt`) presented as an assertion is malformed.
- **Item 8.4, access token profile (RFC 9068).** Bearers carry header
  `typ: at+jwt` and claims `iss`, `exp`, `aud` (the issuer URL), `sub`,
  `client_id`, `iat`, `jti` and the private `agentbus2_kind` (`agent` or
  `human`). `Issuer.Verify` requires `typ` `at+jwt` or `application/at+jwt`
  (case-insensitive). `client_id` is `sub` on agent tokens; Task 11 adds the
  exchanged-token rule.
- **Item 8.6, the `openIdConnect` security scheme.** The scheme is removed
  from `api/openapi.yaml` and from `Discover`'s `auth_methods`
  (`openid_configuration` is gone from `AuthMethod`); the `bearerAuth`
  description names the RFC 8414 document. Supersedes the `openIdConnect`
  scheme of Decision 91 and ADR 0007.
- **Item 8.1, token endpoint errors (RFC 6749 section 5.2).** Only
  `/v1/auth/token` answers `{"error", "error_description"}` as
  `application/json` (with the section 5.1 cache headers): status 400, or
  401 for `invalid_client`; also 405 for a method other than `POST`, 413 for
  an oversized body and 404 for the trailing-slash path (audit items 5 and 6
  below). Every other endpoint keeps problem+json
  (Decision 46). This supersedes Milestone 5 addendum item 3, which put
  problem+json on the token endpoint. A request the validator, mux or strict
  handler refuses before the handler runs (no `grant_type`, a JSON body)
  is `invalid_request` with a fixed description, because the framework's own
  message echoes the offending values and so possibly the assertion.
  `client_credentials` failures of the assertion (type, absence, parse, key,
  verification, replay, a `client_id` that is not the assertion's `iss`)
  are `invalid_client`; any other grant is `unsupported_grant_type`. Plan
  choice: an unconfigured issuer is 503 `temporarily_unavailable` and an
  internal failure is 500 `server_error` (RFC 6749 lists neither status; the
  description of a 500 is fixed). The client (`remote.Token`) maps
  `invalid_client` and `invalid_grant` to unauthenticated, `access_denied` to
  forbidden, `invalid_request`, `unsupported_grant_type`, `invalid_target`
  and `invalid_scope` to validation, `temporarily_unavailable` to
  unavailable (retryable) and `server_error` or an unrecognized code to
  internal.
- **Item 8.5, standard parameters.** `TokenRequest` declares `grant_type`
  (a plain string, so an unsupported grant is `unsupported_grant_type`, not
  a schema failure) and optionally `client_assertion_type`,
  `client_assertion`, `client_id`, `scope`, `subject_token`,
  `subject_token_type`, `requested_token_type`, `audience`, `resource`,
  `actor_token` and `actor_token_type`; the schema allows unknown members.
  `scope` is ignored; `client_id`, when present, must equal the assertion's
  `iss`. `AccessToken.issued_token_type` is optional. The exchange
  parameters are consumed in Task 11.

### Decision 10 and the issuers entry (Task 9)

- **Direct acceptance removed.** `token.Verifier.External`,
  `server.Bearer.AutoCreate`, `identities.Service.EnsureIdentity`,
  `token.ErrUnmappedSubject` and the `subjects` member of an `issuers` entry
  are deleted. `Verifier` verifies the own issuer only; a provider token
  presented as a bearer is `unknown issuer` (401). A configured issuer's
  token is useful only at the token endpoint, which exchanges it.
  This supersedes Milestone 5 item 5 (both mechanisms for external subjects)
  and item 11 above; `Options.External` stays only to list the issuer URLs
  in `Discover`.
- **Entry fields (spec 5.1).** `name` (required, a valid identity name, not
  `everyone` or `admins`, unique), `issuer`, `jwks_uri`, `audience`,
  `algorithms`, `claims` (`email`, `groups`, `subject`; empty means `email`,
  `groups`, `sub`) and `human_token_lifetime_seconds` (0 means 900; 1 to
  86400). The entry names are what `users add -issuer` accepts.
- **Provider token claims.** `token.External.Verify` returns
  `token.Federated` (issuer, entry name, email, groups, subject, client id):
  the email claim must be a non-empty string (`ErrNoEmail`, also
  `ErrInvalid`); groups may be absent, a string or an array of strings; the
  subject claim must be a non-empty string; `client_id` (else `azp`) must be
  a string when present. A JWKS fetch that fails and leaves no usable key is
  `ErrJWKSUnavailable`, so the exchange can answer
  `temporarily_unavailable` rather than blame the token.

### The token exchange endpoint (Task 11)

- **Grant.** `urn:ietf:params:oauth:grant-type:token-exchange` (RFC 8693).
  `subject_token` is required; `subject_token_type` and
  `requested_token_type` (when set) must be
  `urn:ietf:params:oauth:token-type:access_token`.
- **ID tokens are refused** (`invalid_request`, "ID tokens are not accepted;
  present the provider's access token"): an ID token is addressed to the
  client application, not to this server, and many providers do not give it
  the audience the entry checks.
- **`audience` and `resource` are single-valued** and, when set, must equal
  this server's issuer URL, else `invalid_target`. `actor_token` and
  `actor_token_type` are refused (`invalid_request`); `scope` is ignored.
- **A `users` miss answers `invalid_request`** (`exchangeDeniedCode`), not
  `access_denied`: RFC 8693 section 2.2.2 gives a failed exchange
  `invalid_request`. Decision (Eric, 2026-10-23); spec Decision 13.
- **`client_id` of the minted bearer** is the provider token's `client_id`,
  else its `azp`, else its subject claim. It is recorded for the
  authorization code that reads it; it grants nothing.
- **Provider outage** (the entry's JWKS cannot be fetched and no cached key
  fits) answers 503 `temporarily_unavailable`, not `invalid_request`: the
  token is not at fault.
- **Metadata** advertises both grants and the auth methods
  `private_key_jwt` and `none` (the exchange authenticates by the subject
  token, not by a client credential).
- **Logging.** A refusal is logged at warn with the entry name (or the
  claimed issuer URL when no entry matches), the reason and the identity when
  known; a token is never logged. The reassigned-email refusal (spec 4.2) is
  one of these lines.
- **`internal/oidctest`** is the fake provider the tests use; it is a test
  harness with its own self-tests.

### Human sign-in on the client (Task 13)

- **Profile `oidc`** (`issuer`, `client_id`, `redirect_port`, `scopes`) is
  valid on server profiles only and is required for a human. The issuer is
  `https`, or `http` on a loopback host (controller ruling, 2026-10-24): the
  authorization code and PKCE verifier must not cross a network in
  cleartext.
- **`login -human`** (plan choice) is a flag, not a new command, because one
  server profile can hold agent keys and an `oidc` setting at once; plain
  `login` keeps meaning the agent's assertion. `-human` refuses `-identity`
  and always runs the browser.
- **The TUI on a server profile** is always the human who signs in
  (Decision 11): `-identity` is refused (exit 2), and `AGENTBUS2_IDENTITY`,
  the profile's `identity` and its credentials are not consulted. A profile
  without `oidc` exits 2 naming it.
- **Session cache** `<data dir>/tokens/<profile>/human/session.json` (0600,
  directory 0700, owner-only ACL on Windows) holds the server's bearer, its
  discovery document and the provider's refresh token. The corporate access
  token is never stored. A 401 invalidates the bearer, keeps the refresh
  token, and the transport refreshes once: through the refresh token, else the
  browser (which blocks that request for up to 5 minutes).
- **Identity** is the unverified `sub` of the cached bearer (the server
  verifies every request); a bearer without one is an error at save.

### Standards audit (spec 8.7, Task 14)

RFC 6749 (sections 2.3, 3.2, 4.4, 5), 7521, 7523, 8414, 8693, 9068 and OpenID
Connect Discovery were read against `/v1/auth/token`, `/v1/auth/jwks` and
both well-known documents. The departures that remain, each checked against
the running handler (2026-10-24), and why:

1. **The token endpoint URL is still accepted as the assertion `aud`** (Task
   7). The RFC 7523 revision draft forbids it; shipped clients sent it
   before this work. Dropped when the draft is published and every shipped
   client sends the issuer.
2. **`/.well-known/openid-configuration` is still served**, with the same
   document as `/.well-known/oauth-authorization-server`. agentbus2 is not
   an OpenID provider (no authorization endpoint, no ID tokens, no
   `userinfo`) and the document claims nothing of the kind; it stays so
   clients built against the old path keep working.
3. **`audience` and `resource` are single-valued.** RFC 8693 allows
   several of each; an agentbus2 bearer has one audience, its issuer.
   A different value is `invalid_target`. A repeated parameter of any name is
   `invalid_request` (RFC 6749 sections 3.2 and 5.2; the description names the
   parameter, never its value), checked before the handler on both grants;
   a parameter sent with an empty value (`client_id=`, `scope=`) is treated as
   omitted (section 3.2).
4. **`none` is listed in `token_endpoint_auth_methods_supported`** for the
   exchange's public client. RFC 8414 describes the member per endpoint, not
   per grant, so the document cannot say that `none` is valid for the token
   exchange only; the handler enforces it (a `client_credentials` request
   without a valid assertion is `invalid_client`).
5. **`GET` (or any non-`POST` method) on `/v1/auth/token` answers 405
   `invalid_request`** with `Allow: POST` and the section 5.2 body (RFC 6749
   section 3.2 requires `POST`; RFC 9110 section 15.5.6 requires `Allow` on a
   405). `tokenRouteGuard` answers it ahead of the validator, so the
   framework's message, which may echo an assertion, never reaches the body.
   Any other request the validator, mux or strict handler refuses on this
   path answers the same fixed `invalid_request` body with status 400.
   (This closes the earlier departure that answered 400 for a wrong method.)
6. **The refusals made before the token handler can be reached answer the
   section 5.2 body too**: the 413 from the body limit (`limitBody` wraps the
   whole handler; it writes `invalid_request` with status 413 when the path
   is the token endpoint) and the routing-level 404 for the trailing-slash
   path `/v1/auth/token/`, which is not an operation (`tokenRouteGuard`,
   `invalid_request` with status 404). Any other unknown path keeps
   problem+json. (This closes the earlier departure that answered
   problem+json for both.)
7. **The token endpoint's 500 and 503** are not in RFC 6749 section 5.2
   (plan choice, above); the bodies are still the section 5.2 shape.
8. **Error descriptions echo the caller's own values.** `assertion.Parse`
   reports `algorithm "<alg>"` and `typ "<typ>"` in the `invalid_client`
   description, and an unsupported grant is reported as `grant_type "<g>"`.
   Accepted, not changed: each is the `%q`-quoted value of a header or
   parameter the unauthenticated caller just sent, returned to that caller
   only, so it discloses nothing it did not send; it carries no signature,
   claim or key material; and it helps a client author find the wrong value.
   (The assertion refusals are not logged with their description.)

No departure from RFC 7521's client-assertion framing, RFC 7523 section 3
validation (`iss`, `sub`, `aud`, `exp`, `jti`, signature), RFC 8693 section
2.2 responses (`issued_token_type`, `token_type: Bearer`), RFC 9068 section
2.2 required claims or RFC 8414 section 2 required members was found.
