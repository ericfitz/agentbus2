# ADR 0005: No migration from v1; agentbus2 is a separate product

Status: Accepted (Eric, 2026-10-19)

Covers Decisions 29, 31, 32, 33 and 62 of `HANDOFF.md`, all made by Eric
on 2026-10-19.

## Context

agentbus v1 (`/Users/efitz/Projects/agentbus`, v1.14.3) is installed and in
daily use: an `agentbus` binary, an `agentbus` MCP server in each harness,
Stop and SubagentStart hooks, a `using-agentbus` skill, and a shared SQLite
database holding messages, tasks and memories. Its schema has been
migrated in place to version 11 (`internal/bus/schema.go:3`,
`internal/bus/migrate.go`).

agentbus2 changes identity (ADR 0003), drops direct messages (ADR 0006),
drops `-filter`, adds a server and authentication (ADR 0001, ADR 0002) and
defines a new storage contract with two engines (ADR 0004). Eric's goal is
simple, clean code at the end, not a v1 that grows a server.

## Decision

Eric decided:

1. **A separate product: the `agentbus2` repository** (public,
   `git@github.com:ericfitz/agentbus2.git`), starting from scratch. v1 logic
   is ported deliberately, and each porting commit names the v1
   source. No v1 artifacts from decisions that changed are ported: direct
   messages, identity derived from the harness, `-filter`. (Decision 31)
2. **Separate names and paths throughout**, so v2 never touches v1 data.
   (Decision 32) The names:
   - binary `agentbus2`;
   - data in `~/.local/share/agentbus2/` (and the Windows equivalent);
   - config in `~/.config/agentbus2/`, credentials at
     `credentials/<profile>/<identity>`;
   - repo file `.local/agentbus2.json`, which may only select a profile;
   - MCP server name `agentbus2`;
   - environment prefix `AGENTBUS2_`;
   - hook `agentbus2 stop-hook`; skill `using-agentbus2`. (Decision 33;
     Decision 61, release tooling, names the Homebrew formula `agentbus2`
     and is out of scope here)
3. **No migration path** from the v1 schema, and none between SQLite and
   Postgres. No utility for either. (Decision 29)
4. **v1 memories: accept the loss.** v2 starts empty; v1 keeps running for
   as long as it is installed. (Decision 62)

## Consequences

Positive:

- v1 `main` is untouched. Nothing in agentbus2 can corrupt a v1 database:
  different binary, different data directory, different config directory,
  different MCP server name, and ADR 0004's `schema_meta.app` and
  `PRAGMA application_id` refuse a database that is not agentbus2's.
- Both products run side by side on one host during the transition; a
  harness can have both MCP servers configured and both Stop hooks
  installed.
- No migration code means no v1 schema knowledge in the v2 tree and no
  compatibility shims for the decisions that changed.
- Choosing an engine is a one-time choice per deployment, so the store
  never has to express a cross-engine export format.

Negative:

- Memories, tasks and message history accumulated in v1 do not carry over.
  Anything worth keeping has to be re-recorded in v2 by hand or by the
  agents that use it.
- A deployment that starts on SQLite and later needs Postgres starts
  empty on Postgres.
- Porting "deliberately" is slower than copying: each piece of v1 logic is
  re-read against the v2 decisions, and the commit has to cite its source.
- Two installs, two skills and two Stop hooks on one host for as long as
  both are in use; a harness may run both hooks at every turn end.

## Alternatives considered

HANDOFF records no alternative to a separate repository and product; the
decision was to start from scratch rather than evolve v1 in place.

## Open questions for Eric

None recorded. Decision 62's wording ("v1 keeps running for as long as it
is installed") leaves the end of v1 support to Eric and is not a gap in
this design.
