# Gate 0 decisions, Milestones 1 to 7

Decision (Eric, 2026-10-21) on every item the milestone plans marked
"Claude proposal, pending Eric". Each line names the plan item, the answer
and, where the answer differs from what is built or planned, the work it
creates. Items answered "as built" or "as planned" only lose their
`pending Eric` markers; the ADR addenda that list them as pending are
updated to cite this file.

## Spec departures (ratified)

- Milestone 3 item 3: budget eviction, capacity notices and usage move to
  Milestone 7 (spec 5.7). Ratified (A).
- Milestone 3 item 12: `Open`, `Receive` and `Watch` take no
  `Idempotency-Key` (spec 3.4, Decision 46). Ratified (A); spec 3.4 should
  list the exemptions.
- Milestone 3 item 13: an abandoned task is reclaimed by session lease
  liveness alone (about 95 s); an explicit `LeasedUntil` is reclaimed at the
  earlier of the two. Ratified (A).
- Milestone 4 item 1: `Idempotency-Key` is optional on the wire; absent
  means no receipt. Ratified (A).
- Milestone 5 item 7: `Register` refuses trust-on-first-use (no enrollment
  token) on a keyless identity row whose admin flag is set: `forbidden`
  "identity %q requires an enrollment token". Non-admin keyless rows can
  still be claimed in `open` mode. Decided (B).

## Milestone 1

- `tidy-check`: **changed**. `make verify` runs `tidy-check` (A). Work: add
  it to the `verify` target and the CLAUDE.md done-gate line.
- `make verify` needs Docker even with `AGENTBUS2_TEST_POSTGRES_DSN` set:
  accepted (A).

## Milestone 2 (Gate 0, ADR 0004 addendum)

1. `CreateIdentity` refuses `everyone` and `admins`: as built (A).
2. and 3. Postgres retries `40001` and `40P01`, `TxAttempts = 5`, jittered
   backoff `[0, 10ms * 2^(n-1))` capped at 200 ms: as built (A).
4. v1 `meta` not ported; `notices` ported as is: as built (A).
5. Rate-limit buckets in memory per server process: as built (A); the
   Milestone 7 ops doc states the per-replica bound.
6. **Changed** (B): Postgres `Open` reads `pg_database.datctype` and refuses
   a non-UTF-8 database with `ErrSchemaInvalid`. Work: Store change plus a
   conformance test (extends the Decision 34 open rules).

## Milestone 3

1. Own-message exclusion per session: as built (A).
2. Lapsed sessions refused with `session_lapsed`, resumed by `Open` with the
   token: as built (A).
4. Idle subscription expiry not ported: as built (A).
5. Inspection hook not ported in 2.0.0 (A); tracked as a future feature with
   a response mechanism in issue #5.
6. `general` cannot be deleted or renamed by anyone: as built (A).
7. No live-subscriber refusal on channel delete: as built (A).
8. **Changed** (B, rule A of the follow-up): empty-channel reaping is
   ported. A maintenance job deletes an ordinary channel (not `general`,
   not a task list) that has no messages and no subscriptions at all and
   was created more than N hours ago; N is the setting
   `empty_channel_reap_hours`, default 24 (job `"empty channels"`, landed
   in 689943c).
   Work: a maintenance job, its setting and Store support, conformance
   tests on both engines.
9. Polling guard not ported: as built (A).
10. Wake grace and swept-row restore not ported: as built (A).
11. Lapsed rows hold their display name: as built (A).

## Milestone 4

2. Auth paths declared in Milestone 5: as planned (A).
3. Interim server flags replaced by Milestone 5's configuration: as planned
   (A).
4. Watch `timeout` is fractional seconds: as built (A).
6. **Changed** (A plus C): keep the three empty-name guards in the remote
   `Bus`, and move `unsubscribeTags`'s patterns out of the path segment into
   a body or query so a comma inside a pattern no longer splits it. Work: an
   `api/openapi.yaml` change, `make generate`, server handler, remote `Bus`,
   and `TestUnsubscribeTagsCommaSplitsOnTheWire` inverted to assert
   agreement across transports. Landed in 85eea7a as
   `DELETE /v1/tag-subscriptions?pattern=...` (repeated query value); the
   test is now `TestUnsubscribeTagsCommaAgrees`.

## Milestone 5

1. **Changed**: admin reset takes a scope parameter, `data` (default) or
   `everything`. `data` keeps identities, keys, admin flags and enrollment
   tokens and wipes the rest. `everything` also wipes identities, keys and
   enrollment tokens, except the calling administrator's row and keys.
2. **Changed**: the server signing key lives in a Store row, created at
   startup when absent, so replicas share it without a volume. Stored as
   plaintext PEM for now; database read access is therefore equivalent to
   admin, and the deployment docs say so. Rotation (including rotate now)
   is issue #6; a KMS or vault key provider is issue #8.
3. A configured `issuer` URL whose default is the bound address: as planned
   (A).
4. `reset-credential` leaves minted tokens valid until `exp`: as planned (A).
5. **Changed**: both mechanisms. The host command
   `agentbus2 server admin create-identity NAME` creates the row and its
   `general` subscription, and the first valid external token for an
   unmapped name auto-creates the row, but only when the registration mode
   is `open`. An auto-created row is never admin.
6. `register` takes no `Idempotency-Key`: as planned (A).
7. See the spec departures above (B).

## Milestone 6

1. The state file holds the full planned shape, the resume token included,
   mode 0600: as planned (A).
2. **Changed** (B): a new read-only `peek` API operation returns pending
   counts computed with the watch floor, so the Stop hook does not count a
   batch the agent already holds. Work: an `api/openapi.yaml` change, a core
   and `Bus` method, server handler, remote `Bus`, the Stop hook using it.
3. `receive` keeps `wait_seconds`, documented as replacing an armed watch:
   as planned (A).
4. `watch` and `wait` take no `-channel`: as planned (A). Eric asked about
   human monitoring: the Milestone 8 TUI runs over the `Bus` directly and
   filters for display, and the wake already lists its channels, so a
   channel-scoped watch can be added later without an API change.
5. `init` in a repository creates and selects profile `<repo>`: as planned
   (A).
6. `register -server URL` creates the profile: as planned (A).
7. The bearer is cached on disk, 0600, until 60 s before `exp`: as planned
   (A).

## Milestone 7

1. `RegisterWatch` sends a `pg_notify` control hint: as planned (A).
2. Budget eviction and capacity notices on SQLite only: as planned (A);
   Postgres eviction is issue #7.
3. Tick-only eviction; writes are never refused: as planned (A).
4. **Changed** (B): the assertion `jti` replay set is a shared Store table,
   consulted and written in the token transaction, so an assertion is
   single-use across replicas. Work: a schema migration, a Store method,
   conformance tests, and the expiry of old rows in maintenance.

## Post-build decisions (Eric, 2026-10-21)

Made after the Milestone 5 to 9 build, on findings from the whole-milestone
reviews.

1. **Changed**: an enrollment token claims a keyless administrator row only
   when the token is bound to that identity name. An unbound token (any free
   name) presented for a keyless row whose admin flag is set is refused
   `forbidden` and is not consumed, so a token issued for new agents cannot
   take over `root` after `agentbus2 server admin reset-credential`. This
   extends Milestone 5 item 7 (which refuses trust-on-first-use on such a
   row) to unbound tokens.
2. **Changed**: the TUI moves to Bubble Tea v2. Bubble Tea v1's package
   `init` queries the terminal (background color and cursor position) in
   every process that links it, which stalls every `agentbus2` command for up
   to 5 s on a pty that never answers; v2 removes the init-time query.
3. Milestone 9 Tasks 9 and 10 (the release key, signing, notarization,
   publishing and the tagged 2.0.0 release) are deferred: a tested build
   from source is the deliverable for now.
4. **Changed**: the `assertion_jti` table (schema version 3) is keyed on
   (identity, jti), not jti alone, so replay protection is per identity and
   one identity cannot consume another's jti.
5. **Kept, departing from plan choice 11** ("no file locks"): `register` in
   the MCP server holds the host file lock `sessions/.register.lock`, because
   without it two MCP servers restarting at once resume the same session.
6. **Changed**: `agentbus2 register` writes the new private key (0600) to a
   pending file before the registration request, reuses that key on a retry,
   and renames it into place after the 201, so a crash or a lost response
   after the server commits no longer loses the key and the enrollment token.
7. **Changed**: a TUI whose session row was swept (asleep past retention) and
   gets `not_found` treats it like `session_lapsed`: resume, else open a fresh
   TUI session, instead of retrying forever.
8. **Changed**: the server refuses to start when an existing SQLite database
   file is readable by group or others (it holds the token signing key, so
   read access equals admin), with a message naming the file and the fix;
   previously it warned.
9. **Changed**: on Windows the unix-socket listener gets the same owner-only
   DACL as the credential files, so Decision 58's who-may-self-register rule
   holds there as on Unix.
10. **Changed**: editing a memory keeps every field not given (content, type,
    metadata) instead of clearing it, with a revision check so a concurrent
    edit fails instead of being lost. Omitted and empty differ: a field given
    explicitly as empty (`""`, or empty metadata) clears that field; only an
    omitted field is kept. Content is the exception: it cannot be cleared
    (an empty content is a validation error naming delete instead).
    Decision 8's refusal applies to the server (`agentbus2 server` and
    `server admin`); a local-mode database holds no signing key and keeps
    the warning.
11. **Changed (Decision 71, TUI only)**: when no identity is given by flag,
    `AGENTBUS2_IDENTITY` or the profile, `agentbus2 tui` uses the OS login
    name (domain stripped on Windows), as v1 did. Every other command keeps
    requiring an explicit identity on a local profile, so an agent never runs
    as the human by accident.

## TUI decisions (Eric, 2026-10-22)

Made after Eric ran the post-build TUI.

12. **Changed**: the TUI opens its session with the host's short name as the
    hint (the hostname up to the first `.`), so the same human on two hosts
    shows as `efitz/macmini` and `efitz/laptop` and a second TUI on one host
    as `efitz/macmini-2`. A hostname that is empty or fails the name rule
    falls back to the hint `tui`. The status bar says `as <identity>`; the
    health overlay keeps the session's display name.
13. **Changed**: the status bar no longer shows the mode line (`local ·
    <sqlite path>` or `server · <url>`); the health overlay already shows it.
14. **Changed**: the rail's sessions rows are selectable. Selecting one shows
    that session's details in the main pane (display name, identity, session
    id, live or idle with the last time it was seen). There is still no inbox
    view and no messaging from a session row, and nothing new on the wire.
15. **Changed (shared with agentbus v1)**: a name derived from the home
    directory never equals the human's. When `agentbus2 init` defaults a
    repository profile's identity from a repository whose root is the user's
    home directory (compared with `os.UserHomeDir` after resolving symlinks on
    both), the identity is `<user>-home` (`efitz-home`), where `<user>` is the
    same sanitized login name the TUI falls back to. The human keeps the plain
    login name, an explicit name (flag, environment, profile) is never
    rewritten, and any other directory-derived name is left as it is.
16. **Changed (v2 analog of v1's register guard)**: `agentbus2 mcp`, the
    agent's server, refuses to start when its identity equals the OS login
    name (the TUI's fallback), with a message saying to choose another
    identity. The TUI and the other CLI commands are unaffected.
