# Configuration reference

`agentbus2 server` and `agentbus2 server admin` read one set of settings. The tables below are checked against `config.Settings()` and `config.Default()` by `go test ./internal/server/config/` (`docs_test.go`): a missing, duplicated or unknown row, a wrong environment name, type or default fails the test.

## Precedence

Later wins:

1. the built-in defaults;
2. the JSON file named by `-config`;
3. environment variables: the prefix AGENTBUS2_SERVER_ plus the upper-cased setting name (`token_lifetime_seconds` is `AGENTBUS2_SERVER_TOKEN_LIFETIME_SECONDS`);
4. flags.

Flags:

- `-config FILE`: the JSON file. It is an error if it does not exist.
- `-listen ADDR`: `host:port` or `unix:<path>` (server only; `server admin` rejects it).
- `-registration MODE`: `open`, `enrollment` or `closed` (server only).
- `-sqlite PATH` and `-postgres DSN`: exactly one is required.
- `-set name=value`: any setting, repeatable. It wins over the dedicated flag for the same name.

A configuration problem exits 2 with a message that names the offender; a store that will not open exits 1. An environment variable whose name is not a setting, a file member that is not a setting, a repeated member and a member whose case differs from the setting name (`Listen`) are all rejected, as is `null`.

`server admin` uses the same sources and needs only the store (`sqlite` or `postgres`); it ignores `listen` and `registration`.

### The JSON file

One JSON object whose members are setting names, matched exact-case. `deploy/config.example.json` is a working file:

```json
{
  "listen": "0.0.0.0:8420",
  "sqlite": "/data/agentbus2.db",
  "registration": "enrollment"
}
```

### Lists in the environment and in `-set`

`issuers` and `default_acl` are JSON text (a JSON array) in the environment and in `-set`:

```sh
AGENTBUS2_SERVER_DEFAULT_ACL='[{"identity":"everyone","permissions":["read"]},{"identity":"<creator>","permissions":["read","write","admin"]},{"identity":"admins","permissions":["admin"]}]'
```

```sh
AGENTBUS2_SERVER_ISSUERS='[{"name":"corp","issuer":"https://idp.example.com","jwks_uri":"https://idp.example.com/keys","audience":"agentbus2","algorithms":["ES256"],"claims":{"email":"email","groups":"groups","subject":"sub"},"human_token_lifetime_seconds":900}]'
```

## Settings

Columns: setting, environment variable, type, default, bounds (inclusive; from `config.Validate` and `core.Validate`), meaning. A default of `(unset)` is the empty string; the defaults of `issuers` and `default_acl` are described below the table.

| Setting | Environment variable | Type | Default | Bounds and meaning |
| --- | --- | --- | --- | --- |
| `listen` | `AGENTBUS2_SERVER_LISTEN` | string | `(unset)` | Required to serve (no default port). `host:port`, or `unix:<path>` (mode 0600). A loopback, unix or all-loopback-hostname address is local; anything else (including `0.0.0.0` and an empty host) is not, which decides the default `registration` and whether `issuer` must be set. |
| `registration` | `AGENTBUS2_SERVER_REGISTRATION` | string | `(unset)` | `open`, `enrollment` or `closed`. Unset means `open` on a local listener and `enrollment` otherwise. |
| `sqlite` | `AGENTBUS2_SERVER_SQLITE` | string | `(unset)` | Database file. Exactly one of `sqlite` and `postgres`. A new file is created with mode 0600. |
| `postgres` | `AGENTBUS2_SERVER_POSTGRES` | string | `(unset)` | Postgres DSN. The database needs a UTF-8 ctype. |
| `issuer` | `AGENTBUS2_SERVER_ISSUER` | string | `(unset)` | Absolute `http` or `https` URL with no path, query, fragment or trailing slash. Unset means `http://<bound address>`, which is wrong behind a proxy or in a container: set it on every non-local server. Required for a unix socket. Replicas must share it. |
| `token_lifetime_seconds` | `AGENTBUS2_SERVER_TOKEN_LIFETIME_SECONDS` | int | `3600` | 1 to 86400. Lifetime of a bearer token the server issues. |
| `jwks_refresh_seconds` | `AGENTBUS2_SERVER_JWKS_REFRESH_SECONDS` | int | `300` | 10 to 86400. How long an external issuer's key set is cached. |
| `issuers` | `AGENTBUS2_SERVER_ISSUERS` | JSON list | `(see below)` | Corporate token issuers, whose tokens are exchanged for agentbus2 bearers (Decision 57, federated identity spec 5.1). Per entry: `name` (required, unique; the handle `users add -issuer` uses), `issuer` and `jwks_uri` (absolute URLs), `audience` (required), `algorithms` (any of `EdDSA`, `ES256`, `RS256`; empty means all three), `claims` (optional object `email`, `groups`, `subject` naming the token claims to read; defaults `email`, `groups`, `sub`), `human_token_lifetime_seconds` (0 or unset means 900; 1 to 86400). |
| `default_acl` | `AGENTBUS2_SERVER_DEFAULT_ACL` | JSON list | `(see below)` | ACL of a new channel when its creator gives none (Decision 88). |
| `channel_creation` | `AGENTBUS2_SERVER_CHANNEL_CREATION` | string | `"open"` | `open` (any registered identity) or `admins` (Decision 93). |
| `receipt_retention_minutes` | `AGENTBUS2_SERVER_RECEIPT_RETENTION_MINUTES` | int | `60` | 1 to 1440. How long the idempotency receipt of a mutation is kept. |
| `session_retention_hours` | `AGENTBUS2_SERVER_SESSION_RETENTION_HOURS` | int | `72` | 1 to 8760. How long a lapsed session is kept so it can be resumed. |
| `send_messages_per_second` | `AGENTBUS2_SERVER_SEND_MESSAGES_PER_SECOND` | int | `100` | 1 to 10000. Per-identity send rate (Decision 86); per process, see `deploy.md`. |
| `send_kib_per_second` | `AGENTBUS2_SERVER_SEND_KIB_PER_SECOND` | int | `1024` | 64 to 65536. Per-identity send bandwidth; per process. |
| `message_retention_hours` | `AGENTBUS2_SERVER_MESSAGE_RETENTION_HOURS` | int | `168` | 1 to 8760. Age at which ordinary messages are deleted. |
| `tombstone_min_hours` | `AGENTBUS2_SERVER_TOMBSTONE_MIN_HOURS` | int | `72` | 72 to 720. Minimum age before a tombstone is purged. |
| `task_expiry_hours` | `AGENTBUS2_SERVER_TASK_EXPIRY_HOURS` | int | `720` | 0 to 87600. 0 disables the rule. |
| `memory_expiry_hours` | `AGENTBUS2_SERVER_MEMORY_EXPIRY_HOURS` | int | `8760` | 0 to 87600. Memories not accessed for this long are tombstoned; 0 disables the rule. |
| `empty_channel_reap_hours` | `AGENTBUS2_SERVER_EMPTY_CHANNEL_REAP_HOURS` | int | `24` | 1 to 8760. Age after which an ordinary channel with no messages and no subscriptions is deleted. |
| `session_lease_seconds` | `AGENTBUS2_SERVER_SESSION_LEASE_SECONDS` | int | `90` | 1 to 86400. A session's lease; open, receive and watch extend it. |
| `lease_skew_seconds` | `AGENTBUS2_SERVER_LEASE_SKEW_SECONDS` | int | `5` | 0 to 300. Grace added to a lease before the session counts as lapsed. |
| `watch_max_seconds` | `AGENTBUS2_SERVER_WATCH_MAX_SECONDS` | int | `50` | 1 to 240. Cap on a watch (long-poll) timeout; a proxy must allow at least this long. |
| `watch_merge_seconds` | `AGENTBUS2_SERVER_WATCH_MERGE_SECONDS` | int | `2` | 0 to 60. Window in which a wake merges further messages. |
| `maintenance_interval_seconds` | `AGENTBUS2_SERVER_MAINTENANCE_INTERVAL_SECONDS` | int | `60` | 1 to 3600. Maintenance tick period. |
| `max_message_kib` | `AGENTBUS2_SERVER_MAX_MESSAGE_KIB` | int | `64` | 1 to 1024. Largest message. |
| `receive_default_count` | `AGENTBUS2_SERVER_RECEIVE_DEFAULT_COUNT` | int | `100` | 1 to 1000, and at most `receive_max_count`. |
| `receive_max_count` | `AGENTBUS2_SERVER_RECEIVE_MAX_COUNT` | int | `1000` | 1 to 10000. |
| `result_default_kib` | `AGENTBUS2_SERVER_RESULT_DEFAULT_KIB` | int | `64` | 1 to 4096. Default size cap of a history or task result. |
| `sqlite_budget_mib` | `AGENTBUS2_SERVER_SQLITE_BUDGET_MIB` | int | `2048` | 64 to 1048576. SQLite file size above which the oldest ordinary messages are evicted. Enforced on SQLite only; on Postgres it is accepted and has no effect. |
| `cleanup_free_percent` | `AGENTBUS2_SERVER_CLEANUP_FREE_PERCENT` | int | `25` | 5 to 90. How far below `sqlite_budget_mib` an eviction sweep goes. |
| `embedding_endpoint` | `AGENTBUS2_SERVER_EMBEDDING_ENDPOINT` | string | `(unset)` | Absolute URL of an embeddings API. When set, `embedding_model` is required. |
| `embedding_model` | `AGENTBUS2_SERVER_EMBEDDING_MODEL` | string | `(unset)` | Model name sent to the endpoint. |
| `embedding_api_key_file` | `AGENTBUS2_SERVER_EMBEDDING_API_KEY_FILE` | string | `(unset)` | File holding the API key. At most one of this and `embedding_api_key_env`. |
| `embedding_api_key_env` | `AGENTBUS2_SERVER_EMBEDDING_API_KEY_ENV` | string | `(unset)` | Name of the environment variable that holds the API key (it must be set when named). |
| `embedding_query_timeout_seconds` | `AGENTBUS2_SERVER_EMBEDDING_QUERY_TIMEOUT_SECONDS` | float | `10` | 0.1 to 120. Time allowed to embed a search query. |
| `log_level` | `AGENTBUS2_SERVER_LOG_LEVEL` | string | `"info"` | `debug`, `info`, `warn` or `error`. Logs are text on stderr. |

### Defaults of the two lists

`issuers` defaults to the empty list. Rules: an entry whose `issuer` equals the server's own `issuer`, or repeats another entry's `issuer`, is rejected. A `jwks_uri` that is plain `http` on a host other than `localhost` or a loopback address starts the server with a warning (anyone on the path can substitute the key set); use `https`. Each `name` must be a valid identity name (1 to 128 bytes, no `/`, no control characters), not `everyone` or `admins`, and unique across entries. The old `subjects` member is gone and an entry that has it fails to load. A token from an issuer is never accepted directly as a bearer; the token endpoint exchanges it for an agentbus2 bearer, and a human's identity is created at the first sign-in in every registration mode.

An `issuers` entry serves the token exchange only (RFC 8693, `POST /v1/auth/token` with `grant_type=urn:ietf:params:oauth:grant-type:token-exchange`). The caller sends the provider's access token as `subject_token` with `subject_token_type=urn:ietf:params:oauth:token-type:access_token`; the server finds the entry whose `issuer` equals the token's `iss`, verifies the signature against the entry's `jwks_uri`, then `iss`, `aud` (must equal the entry's `audience`) and `exp`, reads the email, groups and subject claims, runs the `users` gate and answers an agentbus2 human bearer that lives `human_token_lifetime_seconds`. Refusals are RFC 6749 bodies: `invalid_request` for a bad or unknown token, an unknown issuer, an ID token (`urn:ietf:params:oauth:token-type:id_token` is refused; present the access token), an `actor_token`, or a person the `users` list does not admit (Decision 13); `invalid_target` when `audience` or `resource` is set and is not this server's `issuer`; `temporarily_unavailable` (503) when the provider's key set cannot be fetched. Every refusal is logged with the entry name or issuer, the reason and the identity when known, never a token.

Example for a provider whose access token carries the person's identity in `user_id` instead of `sub` (an OCI-shaped entry; the claim names are the provider's, shown for illustration):

```json
[{
  "name": "corp",
  "issuer": "https://idp.example.com/oauth2",
  "jwks_uri": "https://idp.example.com/oauth2/keys",
  "audience": "agentbus2",
  "algorithms": ["RS256"],
  "claims": {"email": "email", "groups": "groups", "subject": "user_id"},
  "human_token_lifetime_seconds": 900
}]
```

The first successful exchange binds the person's identity to the token's subject claim and to the `issuer` URL of the entry. Changing an entry's `issuer` URL therefore makes every bound human's next sign-in fail with "identity bound to a different subject" until an operator runs `reset-credential NAME` for each of them (it clears the recorded subject, so the next sign-in binds afresh). Renaming the entry (`name`) does not matter to the bindings, but `users` and `admins` entries refer to the name.

`default_acl` defaults to three entries, in this order: `everyone` with `read` and `write`; `<creator>` with `read`, `write` and `admin`; `admins` with `admin`. Only `everyone`, `admins` and the placeholder `<creator>` are accepted as identities, each with a non-empty list of distinct `read`, `write` or `admin`. `<creator>` stands for the identity that creates the channel and is dropped when there is none (the `general` channel at start, and after a reset). Setting a list equal to the default changes nothing.

## What replicas must share

Replicas over one Postgres database must use the same `issuer`. There is no signing-key setting: the key is a row in the database (see `deploy.md`), so replicas share it by sharing the database.
