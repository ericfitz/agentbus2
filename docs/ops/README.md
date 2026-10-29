# Operating agentbus2 server

One binary, `agentbus2`, provides the server (`agentbus2 server`) and its host-side admin commands (`agentbus2 server admin`). The server keeps everything in one database: SQLite for a single host, Postgres when several replicas serve the same data. Registration, the first administrator, backups and upgrades all work against that database.

- [deploy.md](deploy.md): one host over SQLite, the Docker image, Postgres with two replicas, proxies, health.
- [health.md](health.md): the liveness and readiness probes, the tiered health document, reading the maintenance block on replicas.
- [configuration.md](configuration.md): precedence, flags, and every setting with its environment variable, default and bounds. Checked against the code by `go test ./internal/server/config/`.
- [admin-bootstrap.md](admin-bootstrap.md): `server admin` commands, enrollment tokens, the first administrator, the admin API.
- [inspection.md](inspection.md): the write-inspection webhook: what is sent, the verdict, failure modes, health and logs.
- [slack-bridge.md](slack-bridge.md): the Slack app, the token files, creating, removing and archiving a bridge, and what happens when a create fails.
- [signing-keys.md](signing-keys.md): the key providers, rotation per provider, the trust boundary and what happens when the key service is down.
- [backup.md](backup.md): what to back up, how, and what a restore loses.
- [upgrade.md](upgrade.md): versions and what happens to the database at start.

With the Store-row provider (the default) the server's signing key is stored in clear in the database, so read access to the database is equivalent to the administrator flag. An external key provider keeps the private key out of the database; see [signing-keys.md](signing-keys.md).
