# Operating agentbus2 server

One binary, `agentbus2`, provides the server (`agentbus2 server`) and its host-side admin commands (`agentbus2 server admin`). The server keeps everything in one database: SQLite for a single host, Postgres when several replicas serve the same data. Registration, the first administrator, backups and upgrades all work against that database.

- [deploy.md](deploy.md): one host over SQLite, the Docker image, Postgres with two replicas, proxies, health.
- [configuration.md](configuration.md): precedence, flags, and every setting with its environment variable, default and bounds. Checked against the code by `go test ./internal/server/config/`.
- [admin-bootstrap.md](admin-bootstrap.md): `server admin` commands, enrollment tokens, the first administrator, the admin API.
- [backup.md](backup.md): what to back up, how, and what a restore loses.
- [upgrade.md](upgrade.md): versions and what happens to the database at start.

The server's signing key is stored in clear in the database, so read access to the database is equivalent to the administrator flag.
