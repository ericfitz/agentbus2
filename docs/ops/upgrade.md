# Upgrading

## Version

- `agentbus2 version` prints the binary's version (`2.0.0-dev` for a plain `go build`, `dev` for an image built by `make docker-image`).
- `GET /v1/discover` reports it as `server_version`.
- For the Docker image the tag is the version: build with `--build-arg VERSION=<v>` and tag the image `agentbus2:<v>` (see `deploy.md`).

## What happens to the database at start (Decision 34)

The database carries a schema version in `schema_meta`. On open:

- a database whose schema is newer than the binary is refused with `database schema is newer than this binary: upgrade agentbus2` (exit 1): run the newer binary;
- an older schema is migrated at start, under a lock (Decision 53; on SQLite an immediate transaction, on Postgres an advisory lock per schema), so two servers starting together migrate once and the second waits and then sees the new version;
- a database that is not agentbus2's (a wrong or missing `app`, an agentbus 1.x database, another engine's) or whose `schema_meta` is otherwise invalid is refused. agentbus2 does not migrate from agentbus 1.x or between engines.

Each migration step is a transaction of its own that re-reads the version under the lock. A failed step leaves the previous version, and the next start retries from there.

## Schema versions

- Version 5 (federated identity) adds `identities.kind` (every existing identity becomes `agent`), `messages.sender_kind` (every existing message becomes `agent`), the `human_bindings` table and the `access_refs` table (the `users` and `admins` reference lists). Each identity that has the administrator flag also gets an `admins` entry of kind `identity`, so `admins list` shows it and `admins remove -identity NAME` clears the flag as before. Nothing else changes for existing agents. Stop every replica before starting a binary that migrates from version 4 (procedure step 2): an old replica does not know the new columns.

## Procedure

1. Back up the database (`backup.md`).
2. Read the release notes. If they say the schema version changes, stop every replica before starting the new version: an old replica keeps running against a migrated schema until it exits, and nothing checks for that. If the schema version does not change, replicas can be restarted one at a time.
3. Start the new version and check `GET /v1/discover` (`server_version`) on every replica.

A rollback to an older binary after a migration is refused by the rule above; restore the backup taken in step 1.
