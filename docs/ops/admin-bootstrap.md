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
- `reset-credential NAME`: remove every registered key of the identity so the name can be registered again. For a human it clears the recorded provider subject instead (the next sign-in binds afresh). A bearer token already issued stays valid until it expires. For an administrator, the new key needs a token bound to `NAME` (`enrollment create -identity NAME`).
- `create-identity NAME`: create a keyless row for an identity that will register a key later. In `open` mode any registrant may bind a key to a keyless name that is not an administrator; a keyless administrator can only be claimed with an enrollment token bound to its name.
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

## Humans: sign-in through a corporate provider

A human signs in by exchanging a corporate access token at the token endpoint (see `configuration.md`, `issuers`). The walk-through, all on the server host:

1. Configure the provider as an `issuers` entry (name `corp` below).
2. Admit people: `agentbus2 server admin ... users add -issuer corp -group pai-users` (or `-email V`, `-subject V`). A person who matches no `users` entry is refused at the exchange and no identity is created.
3. Name the administrators: `agentbus2 server admin ... admins add -issuer corp -group pai-admins`. The administrator flag is re-evaluated at every sign-in from the token's claims, so removing someone from the group clears the flag at their next sign-in (a bearer already issued keeps working until it expires, but the flag it is checked against is the stored one).
4. Optionally pre-create a person: `agentbus2 server admin ... create-human eric@corp.example`. Their first sign-in binds the existing identity to the provider subject.

The first sign-in of an admitted person creates the identity in every registration mode, `closed` included (Decision 10). In `open` registration an agent may register an email-shaped name first; that human's first sign-in then answers a conflict, because the name belongs to an agent. Pre-create humans with `create-human`, or run `enrollment`, to avoid the race. A human cannot register a key.

If the provider reassigns an address to a different person (a different subject for the same email), the sign-in is refused (`invalid_request`, "identity bound to a different subject") and the refusal is logged with the identity; run `reset-credential NAME` once you have confirmed the change is legitimate.

## The admin API

An administrator (an identity with the flag) can also use the admin section of the API, which the flag gates (Decision 48): `GET /v1/admin/status`, `GET /v1/admin/usage` (database size, the SQLite budget and the capacity notice), `POST /v1/admin/reset` and `POST /v1/admin/identities/{identity}/reset-credential` and `POST /v1/admin/identities/by-email` (body `{"email": ...}`; creates the human identity bound to that email, or answers the existing one; it honors `Idempotency-Key` and returns `{identity, created}`, where `identity` is the normalized email). Channel delete and rename are authorized by `admin` on the channel, which administrators hold on every channel. `reset` takes `scope=data` (the default) or `scope=everything`:

- `data` wipes the bus data and keeps identities, keys, enrollment tokens, the signing key, the scope registry, retired identity names and the Slack bridge records;
- `everything` also removes the other identities with their keys and bindings, every enrollment token and the `users` list, keeping the caller, the reserved `slack-bridge` identity and the signing key. The `admins` list restarts as exactly `{identity: CALLER}`, so the caller stays an administrator even when the flag came from a group reference; a caller who is a human then signs in only after a host command restores the `users` list. `data` touches neither list nor any binding.

Both recreate `general` and subscribe every remaining identity to it. Any other `scope` is refused. The definitive description is `api/openapi.yaml`.
