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
   local. There is an explicit `-registration=open|enrollment|closed`
   override (for Docker). A lost credential is recovered only with an admin `reset-credential`
   command. (Decision 58)
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
  `open`; an auto-created row is never admin.
- **Milestone 5 item 7.** `Register` refuses trust-on-first-use (no
  enrollment token) on a keyless identity row whose admin flag is set:
  `forbidden` "identity %q requires an enrollment token". A non-admin
  keyless row can still be claimed in `open` mode.
- **Milestone 6 items 1, 3, 5, 6 and 7.** As planned: the state file holds
  the full planned shape, the resume token included, mode 0600; `receive`
  keeps `wait_seconds`, documented as replacing an armed watch; `init` in a
  repository creates and selects profile `<repo>`; `register -server URL`
  creates the profile; the bearer is cached on disk, 0600, until 60 s
  before `exp`.
- **Milestone 7 item 4.** The assertion `jti` replay set is a shared Store
  table (`assertion_jti`) consulted and written in the token transaction,
  so an assertion is single-use across replicas; Milestone 5's per-process
  `token.Replay` is the interim form until Milestone 7 lands.

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
   the 60 s leeway ahead. `token.Replay` rejects a repeated `jti`, per
   process: a restart, or Milestone 7's replicas, each accept one reuse of a
   still-valid assertion, bounded by `assertion.MaxLifetime` plus leeway.
   Decision (Eric, 2026-10-21), Milestone 7 item 4, B: Milestone 7 replaces
   it with a shared `assertion_jti` table consulted in the token
   transaction.
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
   socket file is mode 0600 (Decision 58).
8. **Registration semantics.** The same `(identity, kid)` is checked first
   inside the transaction, so a retry answers `Created false` and consumes
   nothing; a different key for a name with a credential is `conflict`;
   `open` is trust on first use, `enrollment` requires a token consumed in
   the same transaction, `closed` refuses. The transaction is re-run on
   `ErrConflict`, at most twice. Decision (Eric, 2026-10-21), Milestone 5
   item 6: `register` takes no `Idempotency-Key`.
9. **Enrollment tokens.** `abe_` plus 32 random bytes, shown once, stored as
   SHA-256 hex; optional bound identity, expiry (default 24 h), uses
   (default 1).
10. **Registration mode is decided once at start.** `-registration` (or the
    setting), else `open` on a local listener and `enrollment` on any other
    (Decision 72). `agentbus2 server admin` never consults `listen` or
    `registration`.
11. **External issuers** are configuration (`issuers`), not rows. A subject
    gets its row from `server admin create-identity` or, in `open` mode only,
    from its first valid external token (never admin): Decision (Eric,
    2026-10-21), Milestone 5 item 5.
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
  `TestRunWiresExternalIssuers`.
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
