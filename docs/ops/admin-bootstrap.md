# Admin commands and the first administrator

`agentbus2 server admin` works against the store directly, with no listener and no credential (Decision 73). Whoever can run it and read the database can do everything below, so it is a server-host command. It takes the same `-config`, `-sqlite`, `-postgres` and `-set` flags and the same `AGENTBUS2_SERVER_*` environment as `agentbus2 server` (see `configuration.md`), and rejects `-listen` and `-registration`. Exit codes: 0 success, 1 runtime or service error, 2 flag, configuration or usage error.

The outputs below are from one scratch SQLite file:

```
$ agentbus2 server admin -sqlite t.db create-identity alice
alice created
$ agentbus2 server admin -sqlite t.db create-identity alice
alice already exists
$ agentbus2 server admin -sqlite t.db admins add -identity alice
added
$ agentbus2 server admin -sqlite t.db admins list
{identity: alice}
$ agentbus2 server admin -sqlite t.db admins remove -identity alice
removed
$ agentbus2 server admin -sqlite t.db enrollment create -identity bob
token: abe_<token>
id: 4ac4e3ca7b8aac8c identity: bob expires: 2026-10-22T20:50:28Z uses: 1
$ agentbus2 server admin -sqlite t.db enrollment create -expires 1h -uses 2
token: abe_<token>
id: 8004dc4bec2d0520 identity: any expires: 2026-10-21T21:50:28Z uses: 2
$ agentbus2 server admin -sqlite t.db enrollment list
4ac4e3ca7b8aac8c bob 2026-10-22T20:50:28Z 1
8004dc4bec2d0520 any 2026-10-21T21:50:28Z 2
$ agentbus2 server admin -sqlite t.db reset-credential alice
alice: 0 credential(s) removed; the name may be registered again
$ agentbus2 server admin -sqlite t.db admins add -identity ghost
agentbus2 server admin admins: {"code":"not_registered","message":"identity \"ghost\" is not registered; register it first","retryable":false}
```

(the last line exits 1.) The `enrollment list` columns are id, bound identity (`any` when unbound), expiry and uses left; an expired token has `expired` appended.

## Commands

- `users|admins add|remove|list [-issuer NAME] [-email V | -group V | -subject V | -identity NAME]`: edit the two reference lists (federated identity spec 6). `admins add -identity NAME` sets the administrator flag (the identity must exist, registered or created with `create-identity`); `admins remove -identity NAME` clears it. A claim kind (`-email`, `-group`, `-subject`) needs `-issuer` naming a configured issuer; an identity reference takes none. `add` prints `added` or `already present`, `remove` prints `removed` or `not present`, `list` prints one `{identity: pat}` or `{issuer: corp, group: pai-users}` line per entry. With no `issuers` entry configured, every claim-kind `add` is refused; the error lists the configured names. `make-admin` is gone: it exits 2 and names this replacement.
- `create-human EMAIL`: create a human identity named by the address (trimmed and lowercased; exactly one `@` with text on each side). The identity has no key, joins `general`, and is bound to the address with no provider subject until the person first signs in. Prints `<identity> created` or `<identity> already exists`; an agent of that name is a conflict (exit 1). A human cannot register a key.
- `reset-credential NAME`: remove every registered key of the identity so the name can be registered again. For a human it clears the recorded provider subject instead (the next sign-in binds afresh) and also removes the Slack user bound to that person (the next verified Slack message binds afresh). A bearer token already issued stays valid until it expires. For an administrator, the new key needs a token bound to `NAME` (`enrollment create -identity NAME`).
- `create-identity NAME [-scope S]`: create a keyless row for an identity that will register a key later. With `-scope S` it creates a scoped agent instead and prints three lines: `NAME created in scope S` (`NAME already exists in scope S` on a repeat), `token: abe_...` and `expires: <RFC 3339>`; it fails on an unknown scope (`not_found`) and on a done or retired one (`conflict`, `not_found`). The same operation is `agentbus2 identities create [-scope S] NAME` for an administrator's client. In `open` mode any registrant may bind a key to a keyless name that is not an administrator; a keyless administrator can only be claimed with an enrollment token bound to its name.
- `enrollment create [-identity NAME] [-expires 24h] [-uses 1]`: print a token and its row. Without `-identity` the token binds a key to a free name or to a keyless row that is not an administrator; presented for a keyless administrator it is refused `forbidden` (`identity "NAME" requires an enrollment token bound to it`) and not consumed. `-expires` and `-uses` must be positive.
- `enrollment list`, `enrollment revoke ID`.

## First administrator on a non-loopback server

A server on a non-loopback address, or in a container, defaults to `enrollment` registration (Decision 72): nothing registers without a token.

1. Start the server.
2. On the server host: `agentbus2 server admin ... enrollment create -identity NAME` and hand the token to the agent.
3. The agent registers its public key with the token (`POST /v1/auth/register`, body members `identity`, `public_key_jwk` and `enrollment_token`).
4. On the server host: `agentbus2 server admin ... admins add -identity NAME`.

Alternatively run `create-identity NAME` and `admins add -identity NAME` first, then create a token bound to `NAME`; the agent claims the keyless administrator row with it.

In a container the same commands run against the same store:

```sh
docker exec <container> /agentbus2 server admin -sqlite /data/agentbus2.db enrollment create -identity NAME
docker compose -f deploy/docker-compose.yml exec agentbus2-a /agentbus2 server admin enrollment create -identity NAME
```

The second form needs no store flag: `AGENTBUS2_SERVER_POSTGRES` is in the container's environment and `server admin` reads it. Both forms were run against the example image and compose file.

## Scopes and scoped agents

A scope groups the channels and agent identities of one job so they are retired together (Decisions 8 and 9 of the Slack bridge design). Only an administrator creates scopes and scoped agents.

1. Create the scope first, as an administrator (`agentbus2 scopes create [-key K] NAME`; a scope name follows the channel name rule).
2. On the server host: `agentbus2 server admin ... create-identity NAME -scope S`. It prints `NAME created in scope S`, then `token: abe_...` and `expires: <RFC 3339>`. The row has no key and no `general` subscription; the token is bound to `NAME`, usable once, and valid for 24 hours. The token is shown once and never stored in clear.
3. Hand the token to the agent. It registers its own public key with it (`POST /v1/auth/register`, body members `identity`, `public_key_jwk` and `enrollment_token`), so the private key never leaves the agent's host.

A scoped name can never be claimed without its bound token, in `open` and in `enrollment` registration alike: a register with no token, an unbound token or a token bound to another name is `forbidden` and consumes nothing. Self registration never creates a scoped identity. Running the command again for an agent that has no key yet prints a fresh token and revokes the earlier unused one, so a retry after a lost response leaves one live token; for an agent that already has a key it answers `conflict`. The scope must be open: a done or retired scope refuses new identities and channels (`conflict`, or `not_found` when retired).

An administrator who wants only pre-created names to register mints only bound tokens (`create-identity` and `enrollment create -identity NAME`); there is no server setting that forbids unbound tokens (Decision 21). The `closed` registration mode no longer exists.

`reset-credential` on a human also removes the Slack account bound to that person, so the next verified Slack message binds afresh.

## Walk-through: a job with agents, people and a Slack channel

The commands below are the administrator's client commands (`-profile` and `-identity` select the administrator; each prints one JSON document). They assume a server with the Slack bridge configured (see [slack-bridge.md](slack-bridge.md) for the Slack app and the token files); without one every `bridges` action answers `forbidden` ("the Slack bridge is not configured"). The example is a job called `pai1`.

1. Create the scope: `agentbus2 scopes create pai1`.
2. Create the agents: `agentbus2 identities create -scope pai1 pai1-worker`. It prints `identity`, `scope`, `created`, `enrollment_token` and `expires_at`; hand the token to the agent, which registers with it. (On the server host `server admin ... create-identity pai1-worker -scope pai1` does the same.)
3. Create the people by email: `agentbus2 server admin ... create-human eric@corp.example`, or let them sign in; the channel's ACL names them.
4. Create the channel in the scope with a named-only ACL (no `everyone`), so only the listed identities, administrators and the bridge reach it: `agentbus2 channels create -scope pai1 -acl '[{"identity":"pai1-worker","permissions":["read","write"]},{"identity":"eric@corp.example","permissions":["read","write"]}]' pai1-chat`.
5. Bridge it: `agentbus2 bridges create [-slack-name N] pai1-chat`. The answer is the bridge plus `not_invited`, each human Slack could not invite and why. `bridges list`, `bridges get CHANNEL` and `bridges remove CHANNEL` (archives the Slack channel and deletes the bridge; it prints `{"channel": C, "removed": true}`) manage it later.
6. When the job ends: `agentbus2 scopes done -retire with_bridge pai1`, or `-retire normal`. `with_bridge` retires the scope once its Slack channel is archived (after `slack_archive_after_done_hours`, or sooner by another bridge rule) and is `conflict` for a scope with no live bridge; `normal` retires it when its last channel is removed by the normal rules. `-retire` is required and takes exactly `with_bridge` or `normal`; anything else exits 2. `scopes list` shows each scope's state and times; `scopes get NAME` also lists its channels and identities.

## Humans: sign-in through a corporate provider

A human signs in by exchanging a corporate access token at the token endpoint (see `configuration.md`, `issuers`). The walk-through, all on the server host:

1. Configure the provider as an `issuers` entry (name `corp` below).
2. Admit people: `agentbus2 server admin ... users add -issuer corp -group pai-users` (or `-email V`, `-subject V`). A person who matches no `users` entry is refused at the exchange and no identity is created.
3. Name the administrators: `agentbus2 server admin ... admins add -issuer corp -group pai-admins`. The administrator flag is re-evaluated at every sign-in from the token's claims, so removing someone from the group clears the flag at their next sign-in (a bearer already issued keeps working until it expires, but the flag it is checked against is the stored one).
4. Optionally pre-create a person: `agentbus2 server admin ... create-human eric@corp.example`. Their first sign-in binds the existing identity to the provider subject.

The first sign-in of an admitted person creates the identity in every registration mode (Decision 10). In `open` registration an agent may register an email-shaped name first; that human's first sign-in then answers a conflict, because the name belongs to an agent. Pre-create humans with `create-human`, or run `enrollment`, to avoid the race. A human cannot register a key. Identity-kind `users` entries (and so a pre-created human) carry no issuer: with two issuers configured, a pre-created human binds to whichever issuer first presents that email.

If the provider reassigns an address to a different person (a different subject for the same email), the sign-in is refused (`invalid_request`, "identity bound to a different subject") and the refusal is logged with the identity; run `reset-credential NAME` once you have confirmed the change is legitimate.

## The admin API

An administrator (an identity with the flag) can also use the admin section of the API, which the flag gates (Decision 48): `GET /v1/admin/status`, `GET /v1/admin/usage` (database size, the SQLite budget and the capacity notice), `POST /v1/admin/reset` and `POST /v1/admin/identities/{identity}/reset-credential` and `POST /v1/admin/identities/by-email` (body `{"email": ...}`; creates the human identity bound to that email, or answers the existing one; it honors `Idempotency-Key` and returns `{identity, created}`, where `identity` is the normalized email). The administrator tier of `GET /v1/health` (storage, notices, maintenance, the embedding detail) is gated by the same flag (`health.md`). Channel delete and rename are authorized by `admin` on the channel, which administrators hold on every channel. `reset` takes `scope=data` (the default) or `scope=everything`:

- `data` wipes the bus data and keeps identities, keys, enrollment tokens, the signing key, the scope registry, retired identity names and the Slack bridge records;
- `everything` also removes the other identities with their keys and bindings, every enrollment token and the `users` list, keeping the caller, the reserved `slack-bridge` identity and the signing key. The `admins` list restarts as exactly `{identity: CALLER}`, so the caller stays an administrator even when the flag came from a group reference; a caller who is a human then signs in only after a host command restores the `users` list. `data` touches neither list nor any binding.

Both recreate `general` and subscribe every remaining identity to it. Any other `scope` is refused. The definitive description is `api/openapi.yaml`.
