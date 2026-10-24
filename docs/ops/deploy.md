# Deploying agentbus2 server

All settings are in `configuration.md`; admin commands are in `admin-bootstrap.md`.

## 1. One host over SQLite

```sh
agentbus2 server -listen 127.0.0.1:8420 -sqlite /var/lib/agentbus2/agentbus2.db
```

There is no default port: `listen` is required, and the server exits 2 with `listen is required` without it. On a loopback address (or a `unix:<path>` socket, created mode 0600; on Windows it gets an owner-only ACL) registration defaults to `open` (Decision 20): an agent claims any free name with an unauthenticated `register`. Any other address defaults to `enrollment` (Decision 58, 72); `-registration open|enrollment` overrides either default.

On start the server prints `generated signing key <kid>` (first start only) and `agentbus2 server listening on ...` with the engine, registration mode, issuer and key id. SIGINT or SIGTERM starts a shutdown that gives in-flight requests up to 60 seconds; a clean shutdown exits 0.

The SQLite file is created with mode 0600. An existing file readable or writable by group or others is refused: `agentbus2 server` and `agentbus2 server admin` exit non-zero with an error that names the path and mode; fix it with `chmod 600`. (Local mode, which has no signing key, only warns.) The file runs in WAL mode, so `-wal` and `-shm` files sit next to it.

Set `issuer` on every server that is not local. When it is unset the issuer is `http://<bound address>`, so a server bound to `0.0.0.0` tells clients `http://0.0.0.0:<port>`, and the server prints a notice saying so. Behind a TLS proxy set it to the public `https` URL (no path, no trailing slash).

## 2. The Docker image

```sh
make docker-image          # docker build -t agentbus2:local .
docker build --build-arg VERSION=2.0.0 -t agentbus2:2.0.0 .
```

`make docker-image` builds with `VERSION=dev`; give a release build its version with `--build-arg VERSION=...` and use the tag as the version (the value is what `agentbus2 version` and `server_version` in `GET /v1/discover` report). The image is a static binary on a distroless base, entrypoint `/agentbus2 server`, runs as `nonroot` (uid and gid 65532), declares `VOLUME /data` (owned by 65532) and has no shell.

```sh
docker run -d --name agentbus2 -p 127.0.0.1:8420:8420 -v agentbus2-data:/data \
  -e AGENTBUS2_SERVER_LISTEN=0.0.0.0:8420 \
  -e AGENTBUS2_SERVER_SQLITE=/data/agentbus2.db \
  -e AGENTBUS2_SERVER_ISSUER=https://agentbus2.example.com \
  agentbus2:local
```

A container listens on `0.0.0.0`, which is not local, so registration defaults to `enrollment`; the `-registration` flag, passed as the container command, is the override (`agentbus2:local -registration open`). A named volume takes the ownership of `/data`; a bind-mounted data directory must be writable by uid 65532 (`chown 65532:65532 <dir>`). The container exits 2 with a message when no store or no `listen` is configured.

`docker stop` waits 10 seconds by default before killing; pass `--time 60` (compose: `stop_grace_period: 60s`) to leave the full drain.

### The signing key

The server signs its bearer tokens with a key that it creates at first start and stores in the database (the `signing_keys` row), in clear. A second start loads it; two servers starting together on a fresh database end with one key. Consequently:

- read access to the database is equivalent to the administrator flag: anyone who can read it can mint a token for any identity. Protect the database, its backups and its volume like a private key;
- there is no key file and no signing-key setting;
- rotating the key is not implemented (issue 6), nor is storing it in a KMS or vault (issue 8).

## 3. Postgres and two replicas

`deploy/docker-compose.yml` runs `postgres:17` and two replicas of the image, `agentbus2-a` (127.0.0.1:8420) and `agentbus2-b` (127.0.0.1:8421).

```sh
cp deploy/example.env deploy/.env      # deploy/.env is git-ignored; set POSTGRES_PASSWORD and AGENTBUS2_ISSUER
docker compose -f deploy/docker-compose.yml up -d
```

Observed on a fresh start: the replicas started together, one printed `generated signing key <kid>`, and both reported the same `kid` and `registration enrollment`. `GET /v1/discover` answered on both ports with `"server_version":"dev"`.

What replicas must share:

- the database: it holds the data, the signing key and the single-use record of client assertions, so a token endpoint on any replica refuses an assertion another replica accepted;
- `issuer`: the one setting that must be identical;
- the Slack bridge settings, when it is used: the files named by `slack_app_token_file` and `slack_bot_token_file` must exist on every replica (any replica may serve a `bridges` call or archive a bridge), and `slack_archive_after_done_hours` and `slack_max_message_chars` should match. One replica at a time runs the Socket Mode connection, under the `slack-bridge` lock; if its host vanishes without closing the database connection, Postgres can keep that lock (and a `bridges create` lock) until the server notices the dead connection, so the TCP keepalive settings below bound the wait. See [slack-bridge.md](slack-bridge.md).

They need no session affinity: any replica serves any request.

Per replica:

- **Rate limits.** The send buckets (`send_messages_per_second`, `send_kib_per_second`) are per process, so N replicas each grant an identity the full rate.
- **Wake-ups.** Each replica holds one extra Postgres connection that `LISTEN`s, so a send on one replica wakes long-polls on the others. The wake is a hint only: if the connection drops, the replica reconnects with backoff and a lost hint costs latency, never a message. The server starts without it.
- **Maintenance** runs on one replica at a time. The holder keeps the lock across ticks and releases it at shutdown. A holder that is silently partitioned keeps the lock until the server ends its session, which pauses maintenance on every replica: cleanup is delayed, and no new tick starts on two replicas at once. The lock is checked when a tick starts, so a tick already running when its holder loses the lock finishes the job in hand (the tick stops starting jobs half an interval in) and can overlap the next holder's first tick; every job is idempotent, so the overlap repeats work and loses nothing. To bound the pause, set TCP keepalives on the Postgres server side, `tcp_keepalives_idle = 60`, `tcp_keepalives_interval = 10` and `tcp_keepalives_count = 3`, in `postgresql.conf` (or the managed service's parameter group), or per connection in the DSN:

  ```
  postgres://USER:PASSWORD@HOST:5432/agentbus2?options=-c%20tcp_keepalives_idle%3D60%20-c%20tcp_keepalives_interval%3D10%20-c%20tcp_keepalives_count%3D3
  ```

  pgx passes `options` to the server at session start. Checked against the compose Postgres with `psql` on that DSN: `SHOW tcp_keepalives_idle`, `..._interval` and `..._count` printed `60`, `10` and `3`, against `0` on a default connection; the server accepted the same DSN (`server admin -postgres ... enrollment list` exited 0).

### Postgres requirements

Create the database with a UTF-8 ctype. The `postgres` image's default is `en_US.utf8`. The server's start refuses any other with a `schema_meta is invalid` error that names the ctype (text-search normalization needs a UTF-8 character classification). On a managed service check it with:

```sql
SELECT datctype, pg_encoding_to_char(encoding), datlocprovider FROM pg_database WHERE datname = current_database();
```

(`SHOW lc_ctype` is not available on Postgres 17.) The compose database printed `en_US.utf8 | UTF8 | c`. With an ICU or builtin locale provider the encoding must be UTF8.

### Behind a reverse proxy

A watch (long-poll) holds a request open for up to `watch_max_seconds` (default 50). The proxy must pass it without buffering and its read timeout (nginx `proxy_read_timeout`) must exceed that value. Set `issuer` to the public URL.

## 4. Two servers over one SQLite file

Do not. Two server processes over one SQLite file both start and serve, and a token minted by one verifies on the other, but wake-ups are in process: a long-poll on one server is not woken by a send handled by the other, and it ends at its timeout. Use Postgres for more than one server.

## 5. Health

`GET /v1/discover` is unauthenticated (Decision 43) and returns 200 with a JSON body including `api_versions`, `capabilities` (`long_poll`) and `server_version`:

```sh
curl -fsS http://127.0.0.1:8420/v1/discover
```

The image has no shell or `curl`, so probe from the host or the orchestrator, not from inside the container.
