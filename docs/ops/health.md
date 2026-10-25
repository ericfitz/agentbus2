# Health

Two probes with different jobs, a tiered document, and four places to read it (HTTP, the TUI, the MCP tool and the CLI). The definitive description of the document is `api/openapi.yaml` (operation `health`, schema `Health`); this page explains how to use it. The design decisions are in ADR 0010.

## The two probes

| Probe | Request | Answers 200 when | Use it for |
| --- | --- | --- | --- |
| Liveness | `GET /v1/discover` | the process serves; it never touches the store | restarting a stuck process |
| Readiness | `GET /v1/health` | the store answers (`status` is `ok` or `degraded`) | taking a replica out of rotation |

A database outage fails readiness (503) and never fails liveness, so an orchestrator does not restart a healthy process because Postgres is down. A probe must send no `Authorization` header: a stale or invalid bearer on `/v1/health` is a 401, not the probe tier.

Kubernetes:

```yaml
livenessProbe:
  httpGet: {path: /v1/discover, port: 8420}
readinessProbe:
  httpGet: {path: /v1/health, port: 8420}
```

A plain proxy (any health check that accepts a 2xx) points at `/v1/health`; a 503 removes the replica and the next 200 returns it. The image has no shell or `curl` and no Docker `HEALTHCHECK`, so probe from the host or the orchestrator, not from inside the container.

## The document

`GET /v1/health` answers one JSON document; what it contains depends on who asks (Decision 73). Members above the caller's tier are absent, never null.

| Caller | Sees |
| --- | --- |
| No `Authorization` header | the probe tier: `status`, `tier` (`probe`), `version`, `now` |
| A valid bearer for a registered identity | the identity tier (`tier` `identity`): the probe tier plus `started_at`, `store` and `embeddings` (state, backlog, rejected) |
| A valid bearer for an administrator | the admin tier (`tier` `admin`): everything, including the embedding model and last error, `storage`, `notices` and `maintenance` |
| A bearer the server rejects | 401 `unauthenticated`, no document |
| A valid bearer whose identity no longer exists | 403 `not_registered` |

The administrator flag is the one that gates `GET /v1/admin/status` (see `admin-bootstrap.md`).

| Member | Type | Tier | Meaning |
| --- | --- | --- | --- |
| `status` | `ok`, `degraded` | probe | see the status rule below |
| `tier` | `probe`, `identity`, `admin` | probe | what the caller got, so "healthy" and "I only got the probe view" differ |
| `version` | string | probe | the server's version; the same value as `server_version` in `/v1/discover` |
| `now` | integer, Unix ms | probe | the server's clock |
| `started_at` | integer, Unix ms | identity | when this process created its core |
| `store.engine` | `sqlite`, `postgres` | identity | the store engine |
| `store.schema_version` | integer | identity | the schema version the database holds |
| `embeddings.state` | `off`, `ok`, `failing` | identity | `off` with no embedder; `failing` while the `embedding` notice is set |
| `embeddings.backlog` | integer | identity | memories waiting for an embedding, capped at 1000 (`rules.SearchPageMax`), so 1000 means at least 1000 |
| `embeddings.rejected` | integer | identity | memories whose embedding was rejected |
| `embeddings.model` | string | admin | the embedding model (absent when `off`) |
| `embeddings.last_error`, `last_error_at` | string, integer | admin | the `embedding` notice's text and time (absent when unset) |
| `embeddings.rejected_last` | string | admin | the latest rejection reason (absent when none) |
| `storage` | object | admin | `bytes`, `budget_bytes`, `evictable` and, when set, the capacity `notice` (as in `GET /v1/admin/usage`) |
| `notices` | array | admin | the `capacity` and `embedding` operator notices; `[]` when none |
| `maintenance.interval_seconds` | integer | admin | the maintenance tick period |
| `maintenance.owner` | string | admin | this process's lock owner |
| `maintenance.lock` | `held`, `held_elsewhere`, `unknown` | admin | the result of this process's last attempt to take the maintenance lock |
| `maintenance.last_tick_at` | integer, Unix ms | admin | when this process last ticked (absent before its first tick) |
| `maintenance.lock_error` | string | admin | the last lock error (absent when none) |
| `maintenance.jobs` | array | admin | the last tick's jobs: `name`, `changed`, `skipped`, and `error` when one failed; `[]` before the first tick or when the lock was held elsewhere |

### The status rule

- `degraded` when the `capacity` or the `embedding` notice is set, or this process's last maintenance tick recorded a lock error or a failed job. Every tier sees the same `status`; only an administrator sees why.
- `ok` otherwise.
- Not a document: when the store does not answer, the response is a 503 problem (`application/problem+json`) with `code` `unavailable`, `retryable` true and `detail` exactly `store unreachable`. The cause (a driver error that names a host, user or file path) is logged on the server and never sent, because the probe tier is unauthenticated.

## Examples

Output from a scratch server on loopback (`agentbus2 server -listen 127.0.0.1:8431 -sqlite FILE`).

Anonymous (the probe tier):

```
$ curl -sS -i http://127.0.0.1:8431/v1/health
HTTP/1.1 200 OK
Content-Type: application/json
...

{"now":1792911374927,"status":"ok","tier":"probe","version":"2.0.0-dev"}
```

A rejected bearer:

```
$ curl -sS -i -H 'Authorization: Bearer bad' http://127.0.0.1:8431/v1/health
HTTP/1.1 401 Unauthorized
Content-Type: application/problem+json
...

{"code":"unauthenticated","detail":"invalid credential: invalid bearer token: malformed","instance":"/v1/health","retryable":false,"status":401,"title":"Unauthorized","type":"urn:agentbus2:problem:unauthenticated"}
```

With a bearer: `agentbus2 login -profile P -identity I` on a server profile caches one in `<data dir>/tokens/P/I.json` (member `access_token`; mode 0600; it expires after `token_lifetime_seconds`, an hour by default):

```
$ BEARER=$(jq -r .access_token "$AGENTBUS2_DATA_DIR/tokens/P/I.json")
$ curl -sS -H "Authorization: Bearer $BEARER" http://127.0.0.1:8431/v1/health
{"embeddings":{"backlog":0,"rejected":0,"state":"off"},"maintenance":{"interval_seconds":60,"jobs":[],"lock":"unknown","owner":"3b38e90889724e8727354d4f"},"notices":[],"now":1792911383866,"started_at":1792911383259,"status":"ok","storage":{"budget_bytes":2147483648,"bytes":352256,"evictable":true},"store":{"engine":"sqlite","schema_version":1},"tier":"admin","version":"2.0.0-dev"}
```

`agentbus2 health [-profile P] [-identity I]` prints the same document, indented, using the profile's own credential. It exits 0 for `ok` and `degraded`, 1 for a `bus.Error` (an unreachable store among them) and 2 for a flag or resolution error.

## Reading `maintenance`

The maintenance block is this process's view, kept in memory; nothing about a run is stored in the database. The loop ticks once per `maintenance_interval_seconds`, the first tick one interval after start on the server (in local mode, in `agentbus2 mcp` and `agentbus2 tui`, after a random delay of up to one interval), so `lock` is `unknown` and `jobs` is empty until then (and after a lock error). After a tick (here with an interval of 1 second):

```
"maintenance":{"interval_seconds":1,"jobs":[{"changed":0,"name":"lapsed sessions","skipped":false}, ...13 jobs...],"last_tick_at":1792911399430,"lock":"held","owner":"724fe2f04ea92cd30e839391"}
```

With several replicas over Postgres exactly one holds the maintenance lock. Ask each replica: the one that answers `held` runs the jobs, the others answer `held_elsewhere` with no jobs. Two replicas both answering `held`, or none while every replica reports recent ticks, is worth investigating. `owner` is the answering process's own lock owner, not the holder's.

A process that does not run the maintenance loop (an `agentbus2 health` or a hook over a local profile) reports `unknown` and no jobs. In local mode only `agentbus2 mcp` and `agentbus2 tui` run the loop.

## Other surfaces

- TUI: `h` opens the overlay; its server line (status, server version, engine, schema, embeddings) comes from this endpoint when the overlay opens, and an administrator also sees the maintenance line. A failing endpoint shows `server unavailable · <error>`, never a toast. See `docs/tui.md`.
- MCP: the `health` tool returns the document as the agent's identity may see it.
- Discovery: `GET /v1/discover` lists `health` among `capabilities`, so a client can tell a server with the operation from an older one.
