# Upgrading

## Version

- `agentbus2 version` prints the binary's version (`2.0.0-dev` for a plain `go build`, `dev` for an image built by `make docker-image`).
- `GET /v1/discover` reports it as `server_version`.
- `GET /v1/discover` and `GET /v1/health` report `protocol_version`, the wire protocol, independent of the build version.
- For the Docker image the tag is the version: build with `--build-arg VERSION=<v>` and tag the image `agentbus2:<v>` (see `deploy.md`).

## What happens to the database at start (Decision 34)

The database carries a schema version in `schema_meta`. On open:

- a database whose schema is newer than the binary is refused with `database schema is newer than this binary: upgrade agentbus2, or, if an earlier pre-release build created this database, recreate it (pre-release databases are not converted)` (exit 1): run the newer binary, or, for a database from a pre-release build, recreate it (see Schema versions);
- an older schema is migrated at start, under a lock (Decision 53; on SQLite an immediate transaction, on Postgres an advisory lock per schema), so two servers starting together migrate once and the second waits and then sees the new version;
- a database that is not agentbus2's (a wrong or missing `app`, an agentbus 1.x database, another engine's) or whose `schema_meta` is otherwise invalid is refused. agentbus2 does not migrate from agentbus 1.x or between engines.

Each migration step is a transaction of its own that re-reads the version under the lock. A failed step leaves the previous version, and the next start retries from there.

## Schema versions

- Version 1 is the whole schema. Before the first release the migrations of the pre-release builds (their versions 1 to 6) were flattened into it (Eric's decision 2026-10-25, ADR 0004 addendum). A database created by one of those builds carries a version above 1 and is refused at start with the message above; nothing converts it. Recreate it: stop the server, remove the database (the SQLite file and its `-wal` and `-shm` files, or drop and recreate the Postgres database or schema, or remove the Docker volume that holds it), and start the server again. Its data is gone; back up first (`backup.md`) if anything in it matters.
- A pre-release database that misbehaves in any other way is recreated the same way, not repaired.
- Steps after the first release will be numbered 2 and up, each a file of its own per engine. Stop every replica before starting a binary that migrates the schema (procedure step 2): an old replica does not know the new columns.

## Access-rule changes before the first release

These are Eric's decisions of 2026-10-25 (ADR 0011). No release has shipped, so no deployment is upgraded across them, but a pre-release database that holds channels created under the old rule behaves differently.

- **The implicit creator entry is retired.** A channel created with an explicit ACL that omits its creator (including `acl: []`) used to leave the creator `admin` through an implicit entry. It no longer does: only a stored `admin` holder or a server administrator can manage, rename or delete such a channel, and the creator still lists it (the interim List action admits every channel in scope) but cannot manage it. A channel created without an ACL stores the default, whose `<creator>` row keeps `admin`, and `general` has no creator; neither is affected. To find the channels whose creator lost `admin`, run against the database (the columns are the same on SQLite and Postgres; Postgres stores the flags as booleans, SQLite as integers, and both accept this form):

  ```sql
  SELECT c.name FROM channels c
  WHERE c.creator <> ''
    AND NOT EXISTS (SELECT 1 FROM channel_acl a
                    WHERE a.channel_id = c.id AND a.identity = c.creator AND a.can_admin);
  ```

  The query over-reports a creator that still holds `admin` through an `everyone`, `admins` or `humans` row; check those rows before acting. An administrator restores a creator's access with `set_acl` (send the full list, since it replaces every entry).
- **A scoped agent sees only its own scope.** It gets `not_found` for a channel outside its scope, `general` included, and creates channels in its scope only. Administrators and humans are never scoped; a stored scope on either is cleared by maintenance and logged.
- **The task update body has no `force` member.** A client that still sends it is refused `validation` (HTTP 400).

## Procedure

1. Back up the database (`backup.md`).
2. Read the release notes. If they say the schema version changes, stop every replica before starting the new version: an old replica keeps running against a migrated schema until it exits, and nothing checks for that. If the schema version does not change, replicas can be restarted one at a time.
3. Start the new version and check `GET /v1/discover` (`server_version`) on every replica.

A rollback to an older binary after a migration is refused by the rule above; restore the backup taken in step 1.
