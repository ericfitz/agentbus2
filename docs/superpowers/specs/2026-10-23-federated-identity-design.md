# Federated identity and human identities (part 1 of the Slack bridge work)

Status: open questions resolved by Eric, 2026-10-23. Part 2, the Slack bridge, is
`2026-10-23-slack-bridge-design.md` and depends on this document. Tracking:
issue #14 (bridge); part 1 gets its own issue.

Labels used below: **Decision (Eric, 2026-10-23)** marks a choice Eric made
in the brainstorm. **Claude's call** marks a choice the builder may revisit
without asking, as long as the decisions hold; Eric ratified every Claude's
call in this document on 2026-10-23. **Watch** marks an open fact Eric will
investigate; build as written until he reports.

## 1. Intent

agentbus2 today knows only agents: an identity holds an Ed25519 or ES256
key, signs a client assertion and exchanges it at the token endpoint for a
short-lived bearer. Humans use the TUI under an identity chosen by
convention (the OS login name), and agents cannot tell a human from an
agent.

The first consumer is pai, an orchestrator that runs agentic jobs. Each job
has agents and humans. Humans steer agents, from the TUI or (part 2) from
Slack, and agents must know that a message came from a human and which
human, so they treat it as operator steering. The company's identity
provider (OCI IAM Identity Domains, formerly IDCS) already authenticates
those humans, and so does Slack.

This part adds: humans who sign in through a corporate OIDC provider, a
human/agent marker on every message, a sign-in gate and an administrator
set expressed as references to claims, and a pass over every departure from
the OAuth and OIDC standards.

agentbus2 must not become pai-specific, and must not become an identity
management system or a directory. pai keeps the books (which agents and
humans belong to which job); agentbus2 keeps two short reference lists.

## 2. Goals, non-goals, anti-goals

Goals:
- A human signs in to the TUI with the corporate provider (browser, OIDC
  authorization code with PKCE) and gets an agentbus2 bearer.
- A human's identity is derived from a verified claim (email), never from a
  display name; the same person is the same identity in the TUI, over the
  API and (part 2) through Slack.
- Every message carries whether its sender is a human or an agent.
- One gate: a token is issued only to (1) an agent whose credential
  agentbus2 issued or (2) a human whose claims match the `users` list.
- Admins are a list of references (claims or identity names), not a flag
  set by hand on each identity.
- An admin can create a human identity by email before that human has ever
  signed in, so channel ACLs can name them.
- Align with the OAuth and OIDC RFCs as closely as practical; every
  remaining departure is written down with its reason.

Non-goals:
- Device authorization flow or any headless human sign-in (Decision (Eric,
  2026-10-23): humans sign in through a browser only).
- User-ID-based human identities (`createIdentityByUserId`). The binding
  model below leaves room for it.
- Groups as ACL principals, nested groups, a user directory, SCIM.
- Changing how agents authenticate, beyond the standards alignment.

Anti-goals (a failure even if it works):
- A person who is not in `users` obtains any agentbus2 token.
- Two token types, two validation paths, in every handler.
- A display name, or any claim the provider does not verify, decides an
  identity or a permission.
- pai has to understand claims to set channel permissions.

## 3. Decisions (Eric, 2026-10-23)

1. **One token type on every API: agentbus2's own bearer.** Humans exchange
   a corporate token for it at the token endpoint (RFC 8693 token
   exchange). This combines "protocol transition" (the corporate token is
   traded for a short-lived agentbus2 token) with "agentbus2 is its own
   OAuth provider" (agents keep `client_credentials` with
   `private_key_jwt`).
2. **Standards alignment.** Revisit every current departure from the OAuth
   and OIDC RFCs and align as closely as practical, to maximize
   interoperability (section 8).
3. **Corporate provider: OCI IAM Identity Domains, OIDC.** Browser sign-in
   only; no device flow; no headless humans.
4. **Human identity = email.** The email claim is the identity name.
   Humans must present their email to sign in to the corporate provider.
   User IDs may be added later as another binding type; this must not be a
   one-way door.
5. **Two built-in reference lists, `users` and `admins`.** Their entries are
   references that match claims (email, group, subject) or name an
   agentbus2 identity. They can be filled in before any matching person
   exists. agentbus2 does not grow a directory: no agents group, no
   per-job groups.
6. **The gate.** A token is issued only to (1) an agent whose key agentbus2
   registered (holding a registered key is the membership; no list) or (2)
   a human asserted by the corporate provider whose claims match `users`.
7. **Admins come from `admins`.** For pai: `group:pai-admins` and the
   orchestrator's service account (an agentbus2 key identity named in
   `admins`).
8. **ACLs keep naming identities.** pai stays identity-aware, not
   claim-aware. An admin pre-creates a human identity by email
   (`createIdentityByEmail`) before naming it in an ACL; the human's first
   sign-in binds to it.
9. **TUI parity.** A TUI human authenticates through the same corporate
   path and is the same identity as the Slack human; they differ only in
   decoration (`ericf@company.com/macmini` versus
   `ericf@company.com/slack`, the session display name).
10. **Exchange only; direct acceptance removed.** Direct acceptance of
    external tokens as bearers and the static `subjects` map (Decision 57,
    Milestone 5 item 5) are removed. The token exchange is for humans
    only: a token whose claims do not yield a human (no email claim) is
    refused, and a workload registers a key instead. A human's first
    sign-in creates the identity in every registration mode, because
    `users` is now the gate.
11. **TUI identity by mode.** In local (single-machine) mode the TUI keeps
    today's behavior (`internal/cli/osuser.go`: the OS login name). In
    every network mode (a server profile) the human must authenticate
    with the corporate provider and uses the identity the exchange
    returns; there is no OS-login fallback and no agent-kind TUI.
12. **Admin reset scopes (extends Milestone 5 item 1).** `data` keeps
    `users`, `admins` and human bindings. `everything` also wipes human
    identities, their bindings and `users`, and reduces `admins` to the
    calling administrator's own `{identity: NAME}` entry.

## 4. Identities

### 4.1 Kinds

An identity has a kind, `agent` or `human`, fixed at creation:
- `agent`: created by key registration (open, enrollment) or by an admin
  (`create-identity`, and part 2's scoped agent creation). Authenticates
  with `client_credentials` and `private_key_jwt`.
- `human`: created by `createIdentityByEmail` or by a first federated
  sign-in. Authenticates only by token exchange. Holds no keys; key
  registration on a human identity is refused.

Existing identities become `agent` in the migration (Claude's call: there
are no released deployments, 2.0.0 is unreleased, so no deployment holds a
human identity today).

Local mode (Decision 11): the identity the TUI resolves from the OS login
name is created, or marked, `human` (Claude's call: Decision 16 already
reserves that name for the human, and the marker is the point of this
work). A local profile has no corporate sign-in and no `users` gate.

### 4.2 Bindings

A human identity has one binding row: `(identity, issuer, claim_type,
value, provider_subject)`. `claim_type` is `email` today. `value` is the
normalized email. `provider_subject` is the provider's stable user ID
(for OCI, `user_id`, a GUID; Watch item 1), recorded at the first sign-in
and checked on every later one.

The name is never parsed to find the binding: adding a `user_id` binding
type later is a new `claim_type` and a new create call, with no change to
existing identities or ACLs (Decision 4).

Email normalization (Claude's call): trim, lowercase the whole address.
The result must pass the identity name rule (1 to 128 bytes, no `/`, no
control characters) and must not be `everyone` or `admins`.

Reassigned email: if a sign-in presents an email whose binding records a
different `provider_subject`, the exchange is refused (`unauthenticated`,
"identity bound to a different subject") and logged. An admin resolves it
with `reset-credential NAME`, which clears the recorded subject (Claude's
call; it reuses the existing recovery command).

Changed email (Claude's call): `(issuer, provider_subject)` is unique
across bindings. A sign-in whose subject is already bound to another email
is refused ("subject bound to identity %q") rather than creating a second
identity for the same person; there is no rename. An admin resolves it by
deleting or resetting the old identity. Adding a rename later is additive.

### 4.3 Creating a human identity

- `createIdentityByEmail(email)`: admin only, over the API and as a host
  command (`agentbus2 server admin create-human EMAIL`). Idempotent: an
  existing human identity with that binding returns success. An existing
  agent identity with that name is a `conflict`.
- First sign-in with no identity: created if the claims match `users`, in
  every registration mode (Decision 10).
- Scoped agent identities, and the admin API that creates them, belong to
  part 2.

## 5. Sign-in

### 5.1 Configuration

The existing `issuers` setting (Decision 57; the repository cites it as 84
in `internal/server/config/config.go` and `docs/ops/configuration.md`,
which the builder corrects) is extended. Per entry, in
addition to `issuer`, `jwks_uri`, `audience`, `algorithms`:
- `name`: a short alias used in references (`corp`).
- `claims`: which claim holds the email, the groups and the stable subject.
  OCI does not emit email or groups by default; the operator adds them as
  custom claims in the identity domain (Watch item 1). Defaults: `email`,
  `groups`, `sub`.
- `human_token_lifetime_seconds` (Claude's call, default 900): the
  lifetime of a bearer issued by exchange. Agents keep
  `token_lifetime_seconds` (3600). Group removal therefore takes effect
  within 15 minutes, at the human's next exchange.

The static `subjects` map and direct acceptance of external tokens as
bearers are removed (Decision 10): `token.Verifier.External` and
`server.Bearer.AutoCreate` go, and `issuers` entries are used only by the
exchange.

### 5.2 The exchange (server)

`POST /v1/auth/token`, form body, RFC 8693:
- `grant_type=urn:ietf:params:oauth:grant-type:token-exchange`
- `subject_token`: the corporate token.
- `subject_token_type`: `urn:ietf:params:oauth:token-type:access_token`
  only (Claude's call). ID tokens are refused (`invalid_request`): their
  `aud` is the TUI's client ID, so accepting them would accept any token
  minted for the TUI, and would need a second audience per issuer.
- Optional `audience`, `resource`, `scope`, `requested_token_type` are
  accepted; an unsupported value is `invalid_target` or `invalid_request`.

The server: verifies the token against the matching issuer (signature,
`iss`, `aud` = the configured audience, `exp`, `nbf`, algorithm allowlist);
reads the email, groups and subject through `claims`; evaluates `users`;
refuses with `access_denied` when nothing matches (no identity row is
created); resolves or creates the human identity (4.2, 4.3); evaluates
`admins` and writes the identity's admin flag (6.2); issues a bearer with
`human_token_lifetime_seconds`. The response follows RFC 8693 section 2.2:
`access_token`, `issued_token_type`
(`urn:ietf:params:oauth:token-type:access_token`), `token_type`
(`Bearer`), `expires_in`.

The corporate token is never stored and never logged.

### 5.3 The TUI (client)

- A new profile setting names the corporate provider for human sign-in:
  `oidc` with `issuer`, `client_id`, `redirect_port`, `scopes`. `agentbus2
  login` (and `tui` when it has no cached human bearer) runs the
  authorization code flow with PKCE (S256): it opens the system browser,
  listens on `http://127.0.0.1:<redirect_port>/callback`, exchanges the
  code at the provider, then exchanges the provider's access token at
  agentbus2 (5.2) and caches the agentbus2 bearer as today.
- Fixed registered port (Watch item 2): OCI does not document loopback
  redirects on an ephemeral port (RFC 8252 section 7.3), so the port is
  configured and registered exactly. If Eric confirms ephemeral ports
  work, `redirect_port` becomes optional.
- The TUI is a public client (OCI "Mobile Application", no secret).
  agentbus2 is registered in OCI as a resource server (a confidential
  application with a primary audience), so the access token the TUI
  presents has `aud` = agentbus2's audience.
- The TUI on a server profile (Decision 11): `oidc` is required; without
  it `tui` exits 2 naming the missing setting. The identity is the bound
  email from the exchange; the OS login name fallback does not apply.
- Refresh (Claude's call): when the agentbus2 bearer nears expiry, the TUI
  uses the provider's refresh token, if one was issued, to get a new
  provider token and exchanges again; otherwise it reopens the browser.
  Refresh tokens are stored in the bearer cache directory, 0600.

## 6. The gate and the reference lists

### 6.1 References

A reference is one of:
- `{issuer: NAME, email: VALUE}`
- `{issuer: NAME, group: VALUE}` (matches when the token's groups claim
  contains VALUE; whatever OCI emits, names or IDs, Watch item 1)
- `{issuer: NAME, subject: VALUE}`
- `{identity: NAME}` (an agentbus2 identity of either kind). NAME must
  exist when the entry is added; otherwise the add is refused (Claude's
  call). Allowing a not-yet-existing name would let whoever registers it
  first in `open` mode become admin, the hole Milestone 5 items 5 and 7
  closed.

`users` and `admins` are stored in the Store (Claude's call: the admin
flag already lives there, and replicas share it). Host commands:
`agentbus2 server admin users|admins add|remove|list` with the reference
as flags (`-issuer corp -group pai-users`, `-identity pai-orchestrator`).
An admin API for them is out of scope.

### 6.2 Evaluation

- `users` gates the token exchange only (Decision 6). Key holders
  never consult `users`.
- `admins` is evaluated (a) at every human exchange, writing the
  identity's existing admin flag, so the core's authorization code is
  unchanged; and (b) when an `{identity: NAME}` entry is added or removed,
  writing that identity's flag at once.
- `make-admin NAME` becomes `admins add -identity NAME`; existing admin
  flags migrate to `{identity: NAME}` entries (Claude's call; no released
  deployments).
- Removing a human from the corporate admins group takes effect at the
  next exchange: within `human_token_lifetime_seconds`.
- The `admins` ACL principal keeps its meaning (identities whose admin
  flag is set).

## 7. The human marker on the wire

- `Message` gains `sender_kind`: `agent` or `human`, required in
  responses, written from the sender's kind at send time (stored on the
  message row, so history stays right). `api/openapi.yaml` changes;
  `make generate`.
- The MCP `receive` and `history` results carry it; the tool descriptions
  say a `human` sender is the agent's operator and its message is
  steering. The using-agentbus skill says the same.
- `discover` (the live identities list) shows each identity's kind.
- The TUI renders humans distinctly (it already styles the TUI's own
  sessions; the builder picks the style).
- The bearer carries the kind as a claim (`agentbus2_kind`) so a resource
  server can tell without a lookup (Claude's call).

## 8. Standards alignment

Every item below is a current departure (checked against the code and the
fact check of 2026-10-23). The builder aligns each, or records why not in
ADR 0002.

1. **Token endpoint errors (RFC 6749 section 5.2).** The token endpoint,
   and only it, answers with the OAuth error body (`error`,
   `error_description`; `invalid_request`, `invalid_client`,
   `invalid_grant`, `unsupported_grant_type`, `invalid_target`,
   `access_denied`), status 400, or 401 for `invalid_client`. Every other
   endpoint keeps problem+json (Decision 46). Supersedes ADR 0002
   addendum item 3.
2. **Authorization server metadata (RFC 8414).** Serve
   `/.well-known/oauth-authorization-server` with `issuer`,
   `token_endpoint`, `jwks_uri`, `response_types_supported` (required;
   `[]`, since there is no authorization endpoint; Claude's call),
   `grant_types_supported`
   (`client_credentials`, token exchange), auth methods and algorithms.
   Keep `/.well-known/openid-configuration` serving the same document for
   existing clients (Claude's call), but agentbus2 is not an OpenID
   provider and does not claim to be.
3. **Client assertion `aud` (draft-ietf-oauth-rfc7523bis, in the RFC
   Editor queue).** Accept the issuer identifier as `aud`, and make the
   client send it. Keep accepting the token endpoint URL during the
   transition (Claude's call; the draft forbids it, so drop it when the
   draft is published and all shipped clients send the issuer). Send
   `typ: client-authentication+jwt`.
4. **Access token profile (RFC 9068).** Issue `typ: at+jwt` with `iss`,
   `exp`, `aud`, `sub`, `client_id`, `iat`, `jti`.
5. **Extra standard parameters.** The token endpoint tolerates `client_id`
   (must equal the assertion's `iss`), `scope` (ignored or checked), and
   the RFC 8693 optional parameters.
6. **The `openIdConnect` security scheme.** `api/openapi.yaml` declares a
   security scheme of `type: openIdConnect` and `Discover` advertises it,
   but agentbus2 is not an OpenID provider. Replace it with an `oauth2`
   scheme (`clientCredentials` flow plus the token exchange) or a plain
   bearer scheme whose description points to the RFC 8414 metadata.
7. **Audit pass.** The builder reads RFC 6749, 7521, 7523, 8414, 8693,
   9068 and OIDC Discovery against the endpoints and lists any further
   departure.

## 9. Open questions for Eric

None. The builder check's questions were answered on 2026-10-23
(Decisions 10 to 12).

## 10. Watch items (Eric investigating)

1. **OCI claims.** Email and groups are not in OCI tokens by default; they
   must be added as custom claims (the fact check found
   `$user.emails.0.value`). Whether groups appear as names or IDs, and
   whether `sub` is the username or a GUID, is unverified: key the stable
   subject on `user_id` until a real token says otherwise.
2. **Loopback redirect.** OCI documents only exact redirect URLs, so the
   TUI uses a fixed registered port.
3. **PKCE requirement** for OCI Mobile Applications (required or optional)
   is unverified; the TUI always sends it.

## 11. Left to the builder

Store schema details and the version number; error wording; the TUI's
human styling; how the browser is opened per OS; the exact host command
flag names; test design (the exchange must be tested with a fake OIDC
issuer, never against OCI).
