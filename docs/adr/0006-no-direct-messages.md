# ADR 0006: No direct messages in v2

Status: Accepted (Eric, 2026-10-19)

Covers Decision 13 of `HANDOFF.md`, made by Eric on 2026-10-19, and
Decisions 75 and 81 (Eric, 2026-10-20; see "Decisions of 2026-10-20"
below); consequences drawn from Decisions 12, 17, 24, 31, 38, 59, 80 and
88.

## Context

v1 added direct messages in v1 ADR 0004 (2026-09-17): every identity has an
inbox channel `dm/<identity>` (`internal/bus/dm.go`), readable only by its
owner and the TUI observer, written by naming the channel in `send`.
Direct messages then grew into the wake mechanism. v1 ADR 0012 made the
idle wake `agentbus wait -filter @<name>`, and `waitMatch` (`internal/cli/wait.go:108`)
wakes the waiter on its inbox, a tag match or a regex hit on the message,
so the protocol told agents that waking an idle agent takes a direct
message. Other v1 code leans on the inbox too: task assignment checks that
an owner exists by looking for its inbox channel
(`internal/bus/tasks_update.go:420`), search adds the caller's inbox
(`internal/bus/search.go:115`), idle unsubscribe exempts it
(`internal/bus/sessions.go:279`, `internal/bus/receive.go:286`), and the
TUI shows a session's inbox from its session list.

In agentbus2 identity is a credential shared by several sessions
(ADR 0003), and agents wake on any message on any subscribed channel
(Decision 12). The reason direct messages became the wake path no longer
exists.

## Decision

Eric decided:

1. **Direct messages are dropped in v2.** They stay in v1; nothing changes
   on v1 `main`. (Decision 13)

## Consequences

Follows from Decision 31 (ADR 0005): no v1 direct-message artifact is
ported, not `dm.go`, not the inbox match in `cli/wait.go`, not the inbox
special cases in search, idle unsubscribe or task assignment, and not the
TUI inbox view (the TUI itself is a later milestone, Decision 60).

What replaces each v1 use of a direct message, given the rest of the
design:

- **Waking a specific idle agent.** Not needed as a separate path: an
  agent wakes on any message on any channel it subscribes to (Decision 12)
  and on any tag-subscription match (Decision 38). A message on a channel
  the target subscribes to wakes it; `-filter` and @mention addressing are
  gone.
- **A conversation between two agents.** A channel both subscribe to. Under
  the default policy a self-registered identity may create channels under
  its own name and may subscribe to any existing channel (Decision 59), and
  in enterprise deployments policy may subscribe agents to channels
  (Decision 17). A private conversation is a channel whose ACL names only
  its participants (Decision 80, ADR 0008).
- **Reaching a particular identity whichever session is live.** Channel
  subscriptions belong to the identity and every session inherits them
  (Decision 37), so a message on a channel the identity subscribes to
  reaches all of its live sessions, each with its own cursor.
- **"Queue if known" offline delivery** (v1 ADR 0004, decision 6). A new
  session without a resume token starts from the identity-level acked
  mark per subscription (Decision 75, Eric, 2026-10-20, resolving ADR
  0003's open question 1), so an identity with no live session catches up
  on what was posted while it was away; a session that resumes with a
  token catches up from its own cursors (Decision 40).

Positive:

- One kind of channel, one authorization path (identity level, Decision 8)
  and no name-derived special cases (`dm/` prefix, owner-only read, inbox
  exemptions) spread through delivery, search, sessions, tasks, hooks and
  the TUI.
- The Stop hook reason no longer needs the v1 rule that hid direct-message
  content from hook logs; channel names and counts are all it reports.
- Subagents that share a credential cannot be told apart by an inbox
  anyway (one identity, ADR 0003), so an inbox per identity would have
  been the wrong unit in v2.

Negative:

- **Confidentiality is opt-in.** A v1 direct message was readable only by
  its owner. In v2 a channel created without an ACL gets the shipped
  default, `everyone` read and write (Decision 88), so it is readable by
  every identity on the server; a channel meant for two parties has to be
  created with an ACL naming them (Decision 80, ADR 0008). The sentence
  this replaces said no confidentiality existed in 2.0.0 because the
  policy shape was a future milestone (Decision 24); Decision 80
  supersedes that in part.
- A sender can no longer address an identity without knowing a channel it
  subscribes to; discovering that takes the channel list, a convention
  (for example a channel the identity creates and whose ACL it controls)
  or policy.
- v1 code that used the inbox as an existence check for an identity
  (`tasks_update.go:420`) is replaced by a check against the identity
  registry (Decision 81, Eric, 2026-10-20): credentials in server mode,
  registered names in the local profile.
- Anything in v1's skill and protocol text that says "send a direct message
  to wake" or "reply by direct message" is rewritten for `using-agentbus2`,
  and agents moving between v1 and v2 hosts see two protocols.

## Alternatives considered

HANDOFF records no alternative; the choice was to drop direct messages
rather than port them. MCP Events was rejected as a wake replacement for
other reasons (ADR 0001).

## Decisions of 2026-10-20 (Eric)

From Eric's answers to the design spec's section 12
(`.local/review-answers-2026-10-20.md`):

- **Decision 75.** A new session without a resume token starts from the
  identity-level acked mark per subscription, which restores v1's "queue
  if known" (Question 12; also ADR 0003).
- **Decision 80.** Per-channel ACLs ship in 2.0.0, with no direct
  messages; a private conversation is a channel with a restricted ACL
  (Question 17; ADR 0008).
- **Decision 81.** The task owner is checked against the identity
  registry (Question 18).

## Open questions for Eric

Both resolved on 2026-10-20:

1. Private two-party conversations: covered in 2.0.0 by channel ACLs
   (Decision 80, ADR 0008), neither a later milestone nor out of scope.
2. Task owner existence: checked against the identity registry
   (Decision 81).

A per-identity contact channel needs no naming convention: channel names
have no reserved shapes (Decision 76, ADR 0008), so it is any channel the
identity creates and whose ACL it controls.

## Milestone 8 addendum (2026-10-21)

`agentbus2 tui` shipped with no inbox view and no session selection: the rail
has five sections (`channels`, `memories`, `tasks`, `tags`, `sessions`), and
the `sessions` section is a display-only list, so nothing in the TUI selects a
session or an identity. `TestNoInboxView` proves it behaviorally (every channel
the TUI reads is one `ListChannels` listed) and `scripts/m8-exit-checks.sh`
adds a word check over the production files. The milestone's choices 2 to 6
(identity and session by Decision 71 with hint `tui`; the Decision 83 watch and
receive loop; rail sources and sections; the channel create prompt with kinds;
task editing) are recorded in
`docs/superpowers/plans/2026-10-21-milestone-8-tui.md`.

Three Claude proposals are pending Eric, each implemented the way the plan
states and marked `pending Eric: Milestone 8 item N` in code:

1. TUI settings live in a top-level `tui` object of `config.json`
   (alternative: a separate `tui.json`).
2. The TUI's identity is Decision 71 as is, with a stderr and health warning
   when the identity has other live sessions (alternative: fall back to the OS
   user name, as v1's `tui_name` did).
3. No resume token is persisted across TUI restarts, so a crashed TUI's next
   start is `<identity>/tui-2` until retention removes the lapsed row
   (alternative: persist it, as the MCP server does).

Post-build decision 2 (Eric, 2026-10-21,
`docs/superpowers/specs/2026-10-21-gate-0-decisions.md`): the TUI moved from
Bubble Tea v1.3.10, Bubbles v1.0.0 and Lip Gloss v1.1.0 to Bubble Tea v2
(`charm.land/bubbletea/v2` v2.0.10, `charm.land/bubbles/v2` v2.2.1,
`charm.land/lipgloss/v2` v2.0.6), because v1's package `init` queried the
terminal in every `agentbus2` process. `TestVersionOnSilentPTY` (repository
root) runs `agentbus2 version` on a pty that never answers: 5.02 s on v1,
about 28 ms on v2. The keymap, theme, icons, layout widths, alternate screen,
bounded bus calls and receive loop are unchanged; the background query now
runs only when `agentbus2 tui` starts, and the gear icon's cursor-forward
escape no longer reaches the terminal (`docs/tui.md`, Terminal).
