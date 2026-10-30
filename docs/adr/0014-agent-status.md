# ADR 0014: Agent status and history

Status: Accepted (Eric, 2026-10-27, decisions 1 to 15 below). The "Settled open notes" are settled by the agents that wrote and implemented the plan and are not Eric's decisions; each carries an `open note: agent-status N` marker at its code site so Eric can reverse it with one findable edit (`rg -n 'open note: agent-status' .`).

Covers issue #10 (ericfitz/agentbus2#10, aligned with ericfitz/agentbus#43). The design is `docs/superpowers/specs/2026-10-27-agent-status-design.md` (section 5 holds Eric's decisions); the plan is `docs/superpowers/plans/2026-10-27-agent-status.md`; the operator setting is in `docs/ops/configuration.md`; the agent-facing text is the "Status" section of `internal/cli/skills/using-agentbus2/SKILL.md`.

Changes no earlier ADR's decision. It adds the `Identity/SetStatus` action to ADR 0011's table, three operations to ADR 0001's count (ADR 0010 decision 12 and ADR 0001's addendum carry the new totals) and one clause to the runtime capacity notice in `internal/core/maintenance/budget.go` ("agent status history" joins the list of data that is never evicted).

## Context

Eric asked (agentbus#43) for one status line per agent: what it is doing now plus an optional problem, versioned so that earlier statuses become history, with exactly one tag from a fixed set that the TUI maps to an icon. Before this, an agent's work was visible only through the messages it sent. The brief weighed three storage options, three offline rules and three TUI layouts; Eric ratified the recommendation for every one on 2026-10-27.

## Decisions (Eric, 2026-10-27)

Each of these is Eric's decision. The text is the option chosen in section 5 of the design brief.

1. **Storage: a dedicated `agent_statuses` table with the revision pattern** (option B). It avoids the 72-hour tombstone purge, memory expiry, search pollution, the scope-prefix naming rule and two new ACL rules, and changes no earlier ADR. Not chosen: memory rows on a per-agent memory channel (A), a new `status` channel kind (C).
2. **Key: per identity**, recording the setting session and its display name (option A). One status line per identity; hinted subagents are told not to set status. Not chosen: per display name, per session id.
3. **Offline: derived at read** from "no live session"; agents cannot set it (option A). Not chosen: stored by the bus at close and by a maintenance job at lapse (B), set by the agent (C).
4. **Who may set: a new `Identity/SetStatus` action, own identity and agents only, plus `Session/Use`** (option A). Reading rides `Identity/See`. Not chosen: `Session/Use` alone (B); humans and administrators setting any visible agent's status (C).
5. **TUI layout: Layout 2, the agents board, with the status-bar attention count and an icon-only tag on each session row** (option B). Not chosen: inline rail rows (A), the overlay (C).
6. **Tag set: the eight tags in the table below**, seven settable and `offline` bus-only (option A). Recorded in both repositories' ADRs. Not chosen: Eric's seven examples only, without `reviewing` (B); another list (C).
7. **Wire read side: three operations plus `Identity.status`** (option A). Not chosen: two operations with offline agents invisible (B); three operations and no `Identity` field (C).
8. **Retention: prune to N at write**, N a setting with default 20 (option A). Not chosen: a maintenance job (B), a time limit (C).
9. **MCP: one `set_status` tool** (option A, 26 tools). Not chosen: `set_status` plus `get_status` (B).
10. **CLI name: `agent-status set|list|history`** (option A). Not chosen: `report` and `statuses` (B), renaming the administrator's `status` (C).
11. **Park and idle: the protocol text sets `idle` before `park`** (option A). Not chosen: `park` sends `idle` best effort (B, which would change Decision 18's local-only park), no mapping (C).
12. **Stop hook: unchanged; the protocol text carries the task-boundary rule** (option A). Not chosen: the hook writes a status (B), the hook blocks with a reminder (C).
13. **Push: pull only now**, with wake-on-change a follow-up through issue #13 (options A, then C). Not chosen now: wake on change (B).
14. **Repeated identical status: a new revision every time** (option A), so "every call is one revision". Not chosen: a no-op when nothing changed (B).
15. **`problem` with `blocked` or `needs-human`: optional** (option A), as Eric said; the protocol text asks for one. Not chosen: required for those two tags (B).

The protocol stays `1.0.0`: the additions ship in `1.0.0` (Eric, 2026-10-27: "Protocol version will be 1.0.0, end of discussion.").

## The tag table

| Tag | Settable | Meaning | Emoji | Nerd Font |
| --- | --- | --- | --- | --- |
| `idle` | yes | nothing in hand; parked or between tasks | U+1F4A4 (the existing idle icon) | U+F04B2 |
| `planning` | yes | reading, designing, writing a plan | U+1F4DD memo | U+F0CA |
| `implementing` | yes | changing code or docs | U+1F528 hammer | U+F121 |
| `reviewing` | yes | reviewing someone's work or its own | U+1F50D magnifier | U+F002 |
| `waiting` | yes | waiting on another agent, CI or a long run, and expects it to resolve | U+23F3 hourglass | U+F252 |
| `blocked` | yes | cannot proceed; needs something that will not come by itself | U+26D4 no entry | U+F05E |
| `needs-human` | yes | only the human can unblock it | U+1F64B raised hand | U+F256 |
| `offline` | no (bus only) | no live session | U+26AB black circle | U+F111 |

`planning` and `implementing` use the memo and the hammer rather than the brief's map and hammer-and-wrench (settled note 3). `text` is required, one line, at most 200 characters after trimming; `problem` is optional with the same limits; a missing tag, an unknown tag and `offline` are each `validation`.

## Behavior

- **Retention.** `status_history_count` (environment `AGENTBUS2_SERVER_STATUS_HISTORY_COUNT`), default 20, range 1 to 1000, counts the current status. Each write inserts the next revision and prunes to N in the same transaction, so history is bounded without a job and a failure between the two rolls back whole. The bound is identities times N rows of about 2 KB each at most (the 200-character limit counts runes, `rules.MaxLineRunes`, so text and problem can take 4 bytes per rune, plus the identity, tag, session id, display name and two timestamps). Status text and problem are not passed to the write-inspection webhook (ADR 0013); inspecting them is Eric's decision, not made here.
- **Offline.** A read reports `offline` for an identity with no live session and keeps the last stored status: `listAgentStatuses` entries carry `live`, and `last_tag` (the stored tag). `since` is the stored status's `set_at`. Nothing is written at close or lapse.
- **Pull only.** No wake and no push on a change; the TUI polls once per 5-second round. A push is a follow-up through issue #13.
- **Rows go with the identity.** The `agent_statuses` rows are deleted when the identity is retired (`RetireIdentity` deletes the identity row, which cascades) and by `ResetRegistry` and `ResetData`; never by time and never at session close.
- **Capacity.** Agent status history is never evicted for space; the capacity text names it beside tombstones.
- **State (second run, overlap, leftovers).** A replayed `Idempotency-Key` returns the stored result and adds no revision; two unkeyed calls add two revisions in commit order and the last wins; a failed call leaves nothing; a second prune deletes nothing.

## Access

`Identity/SetStatus` is one action of `internal/core/access` (ADR 0011): the scope gate first, then the principal must be the identity and an agent. Denials:

- Out of scope: `not_found`, `identity "X" is not registered` (Reason `out_of_scope`).
- Another identity, administrators included: `forbidden`, `X may set only its own status` (Reason `not_owner`).
- A human or any non-agent: `forbidden`, `X is not an agent; only an agent sets a status` (Reason `not_agent`); an unknown kind fails closed.

Reading a status or its history needs `Identity/See`: a scoped agent sees its scope's agents and the administrators and humans visible to every agent, and its history read of an out-of-scope agent is `not_found`.

## Wire

Three protected operations, so `api/openapi.yaml` declares 57 operations, 8 open and 49 protected (it was 54: 46 protected and 8 open after ADR 0012):

- `setAgentStatus`: `POST /v1/sessions/{session}/status`.
- `listAgentStatuses`: `GET /v1/agent-statuses`.
- `agentStatusHistory`: `GET /v1/identities/{identity}/statuses`.

`Identity.status` (the current status, absent when none) rides `listIdentities`, which feeds `discover` and `register.others`. The tag is a plain string on the wire (settled note 1). MCP gains `set_status` (26 tools); the CLI gains `agent-status set|list|history`.

## Settled open notes (agent choices, not Eric's)

These are not Eric's decisions. The agents that wrote and implemented the plan settled them; each marker is at the named site and Eric can reverse any with one edit.

1. **Tag on the wire is a plain string, no enum, request or response.** The core is the one validator, so the rejection text is the same on every transport; the remote client and the TUI pass an unknown tag through (TUI: the agent icon). Marker: `internal/bus/remote/agentstatus_test.go`, `internal/bus/agentstatus.go`, `internal/bus/remote/agentstatus.go`, `internal/tui/agents.go`.
2. **`last_tag`** is set on `listAgentStatuses` entries only, always (equal to `tag` when `live`); `tag` there reads `offline` when not live. History and `Identity.status` carry the stored tag and no `last_tag`. Marker: `internal/core/statuses/statuses_test.go` `TestListDerivesOffline`.
3. **Emoji:** `planning` is U+1F4DD and `implementing` U+1F528, replacing the brief's U+1F5FA and U+1F6E0, which are `Emoji_Presentation=No` and would need Terminal.app variants. Marker: `internal/tui/icons.go`.
4. **Board key:** `enter` on the `sessions` heading opens the board; the arrow keys still collapse and expand it; `w` stays free. Marker: `internal/tui/agents_test.go`, `internal/tui/model.go`. Consequence (controller ruling 9): `TestNoInboxView` was edited, its no-inbox intent kept (the board makes no bus call).
5. **Session-row icon:** on a live agent row whose identity has a status, the status icon replaces the gear; no status keeps the gear; the TUI's own row and idle rows are unchanged. Marker: `internal/tui/session_rows_test.go`, `internal/tui/view.go`.
6. **Live agents with no status** appear on the board in the live group with `-` for tag, since and text. Refinement (controller ruling 10): only with at least one session, which keeps the seeded `slack-bridge` off the board. Marker: `internal/tui/agents.go`.
7. **Attention count** counts live entries with tag `needs-human` or `blocked` only; it shows as `N need you` (warn color) in the status bar and as `(N need you)` after the `sessions` heading, which is uncolored (controller ruling 10); when the heading is collapsed it reads `(count, N need you)`, with the unread total between them when there is one: `(count, U unread, N need you)`. Marker: `internal/tui/agents_test.go`, `internal/tui/view.go`.
8. **Rate limit:** a set is checked and charged as one operation of `len(tag)+len(text)+len(problem)` bytes against `send_messages_per_second` and `send_kib_per_second`, with the `Edit` text. Marker: `internal/core/statuses/statuses.go` at the `Limits.Check` call.
9. **Default session:** MCP `set_status` without `session` uses the MCP server's own primary session, else `validation`; CLI `agent-status set` without `-session` locates the harness session (the `park` pattern), exit 2 when none; never a throwaway session. Marker: `internal/mcpserver/server.go`, `internal/mcpserver/agentstatus_test.go`, `internal/cli/agentstatus_test.go`.
10. **Denials of `SetStatus`:** the texts and Reasons in the Access section above; the kind test fails closed. Marker: `internal/core/access/decide_test.go` `TestDecideSetStatus`.
11. **Package shape:** the aggregate is `internal/core/statuses` (the `delivery` shape); `sessions.Service.Identities` reads `tx.LatestAgentStatuses`. Marker: `internal/core/statuses/service.go`.
12. **Schema:** the primary key `(identity, revision)` is the identity index; no `CHECK` on `tag` (the core validates). Marker: SQL comment in both `0001_initial.sql` files.
13. **History read** takes no `limit`: it returns the stored rows, oldest first, capped at the current N. Marker: `internal/server/agentstatus.go`, `internal/server/agentstatus_test.go`.
14. **Board history refresh:** while an agent's history is open the TUI refetches it once per 5-second status round. Marker: `internal/tui/agents_test.go`, `internal/tui/model.go`.
15. **`Status` on `IdentityInfo` only**, not on `SessionInfo`: the status is keyed per identity, so a per-session copy would repeat it. Marker: `internal/bus/sessions.go`.

Further agent choice (controller ruling 4, no marker): the capacity text lists "agent status history" with tombstones as never evicted, and the Postgres logical usage sum is not extended to `agent_statuses` (no `size_bytes` column; bounded by identities times N), so the Postgres budget undercounts at most about 2 KB times N per identity.

## Shared with agentbus v1 ADR 0026

Decisions 3 (offline derived at read), 6 (the tag set), 8 (retention, N default 20), 13 (pull only, push a follow-up) and 15 (`problem` optional), and the protocol text, are shared with the v1 ADR. The protocol text, verbatim from the "Status" section of `internal/cli/skills/using-agentbus2/SKILL.md`:

>
> Your status is one line other agents and people see in `discover`'s
> `others` and on the board: what you are doing now. `set_status` takes a
> `tag`, a one-line `text` and an optional one-line `problem`.
>
> | Tag | Meaning |
> | --- | --- |
> | `idle` | nothing in hand; parked or between tasks |
> | `planning` | reading, designing, writing a plan |
> | `implementing` | changing code or docs |
> | `reviewing` | reviewing someone's work or its own |
> | `waiting` | waiting on another agent, CI or a long run, and expects it to resolve |
> | `blocked` | cannot proceed; needs something that will not come by itself |
> | `needs-human` | only the human can unblock it |
>
> You cannot set `offline`: the bus shows it for an identity with no live
> session, and keeps the last status you set.
>
> Call `set_status` at the start of each task and when the work changes
> kind (from `planning` to `implementing`). When you are `blocked` or
> `needs-human`, pass `problem` with what is in your way. Set `idle` when
> you finish and have nothing in hand.
>
> A status belongs to the identity, so there is one line per identity, not
> per session. A session registered with a `hint` does not set a status; the
> top-level session's status covers it. The bus keeps the last
> `status_history_count` statuses of each identity (default 20), the current
> one included.

The park rule, verbatim from the "Park" section of the same file: "Before ending a session with a handoff, set `idle` with `set_status` first, then run `agentbus2 park` as your last agentbus2 action" and "Set `idle` before `agentbus2 park`, never after, because `set_status` unparks."
