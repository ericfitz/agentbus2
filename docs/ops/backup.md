# Backup and restore

## What to back up

- The database. It holds all data and the server's signing key (the `signing_keys` row, in clear).
- The configuration file, if you use one, and the values of any `AGENTBUS2_SERVER_*` variables.

A backup contains the signing key in clear, so it is protected like the database itself (see `deploy.md`): whoever can read it can mint a token for any identity.

## SQLite

Stop the server, or use SQLite's online backup. The file is in WAL mode, so copying the database file alone while the server runs is not a consistent backup (committed data can still be in the `-wal` file).

```sh
sqlite3 /var/lib/agentbus2/agentbus2.db ".backup /backups/agentbus2.db"
chmod 600 /backups/agentbus2.db
```

`.backup` writes the copy with the process's umask (mode 0644 under a default umask), so set the mode yourself; a backup that others can read or write is refused, like a database that others can read or write, when `agentbus2 server` or `agentbus2 server admin` opens it. The copy opens as a database: `agentbus2 server admin -sqlite /backups/agentbus2.db enrollment list` exited 0.

## Postgres

```sh
pg_dump --schema=public "$DSN" > agentbus2.sql
```

`--schema` names the schema the DSN's `search_path` selects. Against the compose database the tables (`signing_keys` among them) are in `public`, and `pg_dump --schema=public` completed with exit 0 and contained `CREATE TABLE public.signing_keys`. Check `\dt` if your role or DSN sets another search path. Restrict the dump's mode (`umask 077` before running).

## Restore

1. Stop every server that uses the database.
2. Restore the database: for SQLite put the file at the configured path (mode 0600); for Postgres create an empty database with a UTF-8 ctype (see `deploy.md`) and load the dump.
3. Restore the configuration file.
4. Start the servers.

Do not start a server on the empty database before the restore: it would create a signing key and the schema first.

## What a restore loses

- Everything written after the backup.
- Sessions opened after the backup are gone. The sessions in the backup have lapsed by the time of the restore; each can be resumed for `session_retention_hours` (default 72) after its lease ended.
- Client assertions that were presented between the backup and the restore are accepted once more, because the restored replay record does not have them. That window is bounded by the assertion lifetime (at most 5 minutes) plus 60 seconds of leeway after each assertion's expiry.
- Nothing else: tokens already issued verify against the restored signing key and stay valid until they expire.
