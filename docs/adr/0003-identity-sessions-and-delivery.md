# ADR 0003: Identity, sessions and delivery

Status: Accepted (Eric, 2026-10-19)

Covers Decisions 8, 12, 14, 15, 16, 17, 18, 24, 37, 38, 39, 40, 41, 44 and
59 of `HANDOFF.md`, all made by Eric on 2026-10-19, and Decisions 73, 75,
77, 78, 79 and 86 (Eric, 2026-10-20; see "Decisions of 2026-10-20"
below).

## Context

In v1, identity is derived from where and how the agent runs. The name
comes from the working directory (`internal/cli/identity.go`), a second
session in the same repository registers as `<name>2`, and hooks and
`agentbus wait` find "their" session by walking the process tree to the
harness (`procs.FindHarness`, recorded on the session by
`internal/mcpserver/server.go:547`; v1 ADR 0017). Delivery state (cursors,
the pending batch in `internal/bus/receive.go`) hangs off the registered
name, and channel membership is a row the client writes.

Idle wake in v1 is `agentbus wait -filter @<name>` (v1 ADR 0012): the
waiter only wakes on a direct message, a tag match or a regex hit on the
message text (`waitMatch`, `internal/cli/wait.go:108`), so agents were told that
waking an idle agent takes a direct message.

With a server (ADR 0001) and real authentication (ADR 0002), none of this
holds: the server cannot see the client's process tree, a credential is the
only thing it can trust, and the rules about who may read or post have to
be enforced where the data is.

## Decision

Eric decided:

1. **Identity is authenticated, per-agent configuration**, no longer
   derived from the harness process. **One credential equals one
   identity.** (Decision 14; in the local SQLite profile identity is just
   the registered name, ADR 0002)
2. **Several sessions may share one identity.** The client may send a
   display hint, to tell subagents from their parent. The server assigns
   the display name, honoring the hint when practical. The hint never
   affects authorization. (Decision 15)
3. **Display names.** The first live session gets `<identity>`, later ones
   `<identity>-2`, `-3` and so on. A hint becomes `<identity>/<hint>`, made
   unique by the server. (Decision 39)
4. **Delivery state is scoped to the session.** (Decision 16)
5. **Channel membership is held only on the server.** Clients may ask to
   create or subscribe to a channel, subject to the server's authorization
   policy. The server keeps a cursor for every subscription. A client MAY
   keep a local cursor and offer it when connecting; the server MAY honor
   it but MUST fall back to its own known-good cursor on any error. In
   enterprise deployments, policy will mostly decide membership (for
   example, an ops agent for service foo is auto-subscribed to `ops` and
   `foo`). (Decision 17)
6. **Channel subscriptions belong to the identity.** Sessions inherit them
   and get their own cursors. (Decision 37)
7. **Client-offered cursors are rejected** if before retention, ahead of
   the high-water mark, or on a channel the session cannot read. The server
   then uses its own cursor. (Decision 44)
8. **Where a session starts reading:** from a resume token if one is
   given, valid and usable; otherwise from the server's high-water mark for
   the subscription; otherwise from now. (Decision 18)
9. **The session resume token restores cursors, the display name and the
   pending (handed-out, unacked) batch.** (Decision 40)
10. **Agents wake on any message on any channel they subscribe to.** There
    is no `-filter` and no @mention addressing. (Decision 12)
11. **Tag-subscription matches wake the agent**, the same as channel
    messages. (Decision 38)
12. **The server enforces authorization at the identity level.**
    (Decision 8)
13. **A default authorization policy ships now; the policy shape is a
    future milestone.** (Decision 24) The default policy for
    self-registered identities:
    - subscribed to `general`;
    - may read and post on any channel;
    - may create channels under its own name;
    - may subscribe to any existing channel. (Decision 59)

    Decision 24 is superseded in part by Decision 80 (Eric, 2026-10-20):
    per-channel ACLs ship in 2.0.0 (ADR 0008). Decision 59 is superseded
    in part by Decisions 76 and 82 (no name shapes; ownership is the ACL)
    and by Decisions 80 and 88 (reading, posting and subscribing hold as
    far as each channel's ACL allows; the shipped default ACL grants
    them).
14. **The Stop hook and `watch` find their session by walking the process
    tree** to the harness, as v1 does. This only locates the session; it
    never proves identity. Hooks fail open within a few hundred
    milliseconds. (Decision 41)

## Consequences

Positive:

- Authorization has one subject, the identity, and the server is the only
  place that enforces it; a subagent that shares its parent's credential
  has exactly its parent's rights, whatever hint it sends.
- Delivery per session means a parent and its subagents, or two harnesses
  under one identity, each have their own cursors and pending batch.
- The resume token carries the pending batch, so a restarted client
  re-reads the messages it had been handed and not acked, instead of
  skipping or duplicating them.
- Server-held membership lets an enterprise deployment subscribe agents by
  policy, with no client-side configuration to drift.
- The server's cursor is always the fallback, so a client with a stale or
  corrupt local cursor can never skip past retention or ahead of the
  high-water mark.
- Waking on any subscribed channel removes the v1 rule that waking an idle
  agent takes a direct message (v1 ADR 0012) and the `-filter` regex; see
  ADR 0006.

Negative:

- The process-tree lookup is a locator only. A hook that cannot find its
  harness (unsupported platform, a non-shell intermediary, as in v1 ADR
  0017) fails open and the agent is simply not woken at the end of the
  turn.
- Every message on every subscribed channel wakes the agent. Noise is only
  bounded by the wake merging and `include_own=false` default of
  Decision 45 (ADR 0001) and by what the agent chooses to subscribe to.
- Under the default policy any identity may read and post on any channel
  created without an ACL (the shipped default ACL, Decision 88);
  confidentiality is available in 2.0.0 by passing an ACL at creation or
  editing it afterwards (Decision 80, ADR 0008). The sentence this
  replaces said confidentiality waited for the policy milestone
  (Decision 24); Decision 80 supersedes that in part.
- Display names are assigned, not chosen, so an agent cannot predict the
  name another session will show as; logs and the TUI must use the
  server-assigned name.

## Alternatives considered

HANDOFF records no alternatives for these decisions beyond the v1 model
they replace (identity from the harness process, v1 ADR 0017; `-filter`
and direct-message wake, v1 ADR 0012).

## Decisions of 2026-10-20 (Eric)

From Eric's answers to the design spec's section 12
(`.local/review-answers-2026-10-20.md`):

- **Decision 73.** An identity becomes an administrator by the server-host
  command `agentbus2 server admin ...`, which works against the store
  without a credential; the admin API section requires that flag.
  (Question 10; also ADR 0002)
- **Decision 75.** The high-water mark of Decision 18 is the
  identity-level acked mark per subscription: the furthest seq any session
  of the identity has acked, advanced on every ack. A new session without
  a token continues where the identity left off. (Question 12; also ADR
  0006)
- **Decision 77.** The default policy applies to every identity however it
  was created (local profile, enrollment token, administrator) unless
  policy says otherwise. (Question 14)
- **Decision 78.** The harness-to-session mapping is a local state file
  per session under `~/.local/share/agentbus2/` (harness pid and start
  time, session id, profile), written by the MCP server and the only
  thing the hook reads. (Question 15)
- **Decision 79.** A resume token is valid while the server retains the
  lapsed session row, a retention window that is a server setting; the
  pending batch stays attached to the session and is dropped with the
  row. (Question 16)
- **Decision 86.** Rate limits are keyed per identity, so sessions of one
  credential share a bucket. (Question 23)

Channel naming (open question 2 below) is settled by Decisions 76 and 82
in ADR 0008: no reserved name shapes, ownership is the channel ACL, kinds
stay and prefixes go.

## Open questions for Eric

All six resolved on 2026-10-20:

1. The high-water mark: the identity-level acked mark (Decision 75).
2. Channel naming syntax for "under its own name": there is none; names
   have no reserved shapes and ownership is the ACL (Decisions 76, 82,
   ADR 0008). The per-identity contact channel ADR 0006 mentioned is
   simply a channel whose ACL the identity controls.
3. Scope of the default policy: every identity (Decision 77).
4. Admin bootstrap: a server-host command sets the flag (Decision 73).
5. Harness-to-session mapping: a local state file (Decision 78).
6. Resume token validity and the pending batch: the session retention
   window; the batch is dropped with the row (Decision 79).
