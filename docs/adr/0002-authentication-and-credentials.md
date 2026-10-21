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
