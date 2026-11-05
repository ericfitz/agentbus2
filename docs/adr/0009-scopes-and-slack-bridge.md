# ADR 0009: Scopes and the Slack bridge

Status: Accepted (Eric, 2026-10-23)

Covers issue #14. The design is `docs/superpowers/specs/2026-10-23-slack-bridge-design.md` (decisions of 2026-10-23) with part 1, `2026-10-23-federated-identity-design.md` (human identities, `sender_kind`, `admins`). The operator guide is `docs/ops/slack-bridge.md`.

## Context

An orchestrator (pai) runs jobs made of agents, and the people who steer a job live in Slack. Agents talk on agentbus2 channels; people talk in Slack. Without a bridge a person must read the bus to follow a job, and there is no way to scope a job's channels and agent identities so they can be retired together. This ADR records the scope model (a named group of channels and agent identities, retired as one unit), the bridge that mirrors one scoped chat channel into one private Slack channel, and the rules that make the bridge safe: only agentbus2 holds Slack credentials, Slack users count only when they are full workspace members bound to a human identity with `write` on the bus channel, and nothing echoes.

## Decisions (Eric, 2026-10-23)

Each of these is Eric's decision.

1. Bidirectional bridging of the scope's chat channel only. Memory and task channels later, one-way, informational.
2. Absolutely no echoes.
3. Only agentbus2 holds Slack credentials. pai asks; agentbus2 creates the private Slack channel, invites the humans and bridges.
4. A Slack human's message reaches the bus attributed to that human's identity, marked human, decorated as coming from Slack.
5. Socket Mode: the server dials out to Slack; no inbound access.
6. Lifecycle: pai says the scope is done; Slack is archived 7 days later (configurable), or at once after done when no humans remain in the Slack channel; a separate, configurable idle timeout archives forgotten channels whether or not done was said.
7. agentbus2 manages the lifecycle of its own channels; the "done" call carries a flag: retire the bus channels in step with Slack, or leave them to the normal rules.
8. Scopes. A channel belongs to zero or one scope. An agent identity has zero or one scope, set at creation. A channel created by a scoped identity takes that scope. pai itself is unscoped but may create scoped objects. pai creates fresh agent identities per job, so identities never change scope.
9. Only admins create scopes, create agent identities (optionally scoped) and create bridges. A bridge requires a scoped channel. Non-admins may create channels; those take the creator's scope, if any.
10. Slack message appearance: each agent posts under its own name and avatar; subject bold; tags as chips; bus replies are Slack thread replies.
11. The bridge's allowed humans are the human identities with `write` on the bridged channel. No separate list. (2026-10-25: evaluated by `Check(Post)` and `Holders(Post)` of `internal/core/access`, not by scanning stored rows; see "Decisions of 2026-10-25 (Eric)".)
12. A bridged channel's ACL may grant `read` or `write` only to named identities, never to `everyone` or `admins`. (2026-10-25: nor to `humans`. 2026-10-27: `everyone` and `humans` hold no entry at all; `admins` may hold `admin` alone.)
13. `reply_to` is validated: the parent must exist and be in the same channel at send time, and must not be a memory record. Otherwise the send is a `validation` error. This applies to every channel.
14. On a bridged channel, after validation, `reply_to` is rewritten to the root of the parent's thread (flat threads, Slack's model).
15. The message number to Slack message map is pruned when its scope is retired.
16. Human membership is fixed at bridge creation for now; may be revisited.
17. Scoped agent credentials. An admin API operation `identities create NAME -scope S` creates a scoped agent identity and returns a one-time enrollment token bound to NAME. The agent registers its own key with it, so the private key never leaves the agent's host. A scoped identity can be claimed only with its bound token, in every registration mode.
18. Slack trust. Only full members of the workspace count: Slack guests (restricted and ultra-restricted users) and external (Slack Connect) users never do, even with a matching email. The first verified message records the Slack user ID against the human identity; afterwards a different Slack account presenting the same email is refused.
19. Scoped identity footprint. Scoped agents get no `general` subscription. At retirement their ACL entries and subscriptions are removed everywhere and their task claims released; their names are blocked from reuse permanently. Messages they sent on other channels stay. (2026-10-25: moot as worded. Under the scope gate a scoped agent never writes outside its scope, so it has no messages on other channels; the retained-message rule now applies to the channels of its scope that are not retired with it.)
20. Retention fix (every deployment). A subscription does not keep a channel alive when its holder is the bridge, or an identity with no live or resumable session (none within `session_retention_hours`). This supersedes the empty-channel rule of ADR 0008 decision 8, which counted any subscription.
21. Registration modes (decided 2026-10-23, after the plan review; amends ADR 0002 decisions 5 and 8). The `closed` registration mode is removed; the modes are `open` (anyone claims a free name, trust on first use) and `enrollment` (an admin-minted token is required). Whether a token is bound to a name is the administrator's choice when minting it; no server setting forbids unbound tokens. Decision 17 applies in both modes. No release has shipped, so `closed` is an unknown value like any other.

## Claude's calls (ratified by Eric, 2026-10-23)

- Slack credentials are token files named by `slack_app_token_file` and `slack_bot_token_file`, so the values never sit in the process environment. `slack_max_message_chars` defaults to 3000 (longer bodies are cut with a note).
- With no Slack settings the bridge is off and bridge calls are `unavailable`, not retryable, "the Slack bridge is not configured" (Eric 2026-10-25; it was `forbidden`); `unavailable` stays the retryable store failure.
- One replica runs the bridge under the `slack-bridge` Store lock and acks each Socket Mode envelope before processing; processing is idempotent through the message map.
- The bridge reads each bridged channel as the reserved identity `slack-bridge` with its own acked mark; the name is reserved like `everyone` and `admins`, and the bridge adds `{slack-bridge, [read]}` to the channel's ACL.
- A bridge row in state `creating` is written before the Slack channel exists, so a retry resumes it; `already_in_channel` on invite is success. The Slack name is lowercased, restricted to `a-z0-9-_` and cut to 80 characters; `name_taken` is `conflict` with no automatic suffix.
- A bridged agent posts under its short name (the scope name and a following `-` removed).
- Accepted Slack subtypes are exactly plain messages and `thread_broadcast`; edits, deletions, joins, leaves and file shares are ignored.
- `bridges list`, `get` and `remove` are admin only; remove posts the final line, archives the Slack channel and deletes the row and its map.
- Protecting a live bridge: only a server admin changes the ACL of a channel with a live bridge, and the `{slack-bridge, [read]}` entry is pinned; such a channel cannot be deleted or renamed, and empty-channel reaping skips it.
- A `reply_to` parent that does not exist stays `not_found`; a parent in another channel is `validation`. On a memory or task channel "not a memory record" reduces to the same-channel rule.

## Corrections to the spec

- Spec 7 assumed `history` already had a thread filter. It had none, so Task 3 of the plan added `thread` to `history` on the bus, the wire, the MCP tool and the CLI; the agent text names it. The filter is complete only for replies stored after the channel's bridge went live: before that, a reply may point at another reply rather than the root, so a thread read of the root can miss it.
- Decision 21 was decided after the plan review and is restated above with the rest.

## Plan choices

Choices made while building, recorded because they change behavior:

- The Slack library is slack-go v0.30.1, imported only by the adapter `internal/slackapi/slackgo`; tests use `slackfake` or an `httptest` server.
- Inbound cursor rule (Task 13): catch-up resumes from the newest mapped `ts` and is capped at what Slack's history holds; a drop is final and recorded in `bridge_dropped` so it is not reconsidered.
- Invitations are per user. The invite error contract: channel-level and credential codes, rate limits and transport errors fail the call; per-user codes come back as per-user failures. Slack guests and external users are not invited.
- `History` page order is unspecified (real Slack answers newest first); catch-up reads all pages and sorts.
- Only `invalid_auth` on the bot token fails server start; any other failure to reach Slack leaves the server up and bridge calls `unavailable` until `auth.test` succeeds.
- Catch-up also runs periodically from the lifecycle loop, not only on reconnect, so an exhausted retry is picked up without a reconnect.
- Archiving goes through an `Archiver` port that the maintenance replica calls. A scope that is idle or done with a live bridge and no `Archiver` configured is held, never retired, so its Slack channel is never orphaned.
- `bridges create` takes a cross-replica lock; a create that hits a Slack rate limit fails with `rate_limited` and resumes on the next call.
- The create notice names identities, and a human identity's name is its email address: the not-invited notice therefore posts those emails into the private Slack channel (a bus message of type `notice` carries the same names). Everywhere else "never emails" holds: no other Slack-side text and no log line carries one.
- A posted Slack `ts` is recorded without reposting after a failed database write; the map row is repaired best effort from message metadata, whose readability with the documented scopes was verified on a real workspace on 2026-10-24 with the scopes above.
- Mention safety has two layers. `&`, `<` and `>` are escaped in subject, content and fallback text, including inside code spans and fences, so agent text cannot form Slack's `<!channel>`-style markup. Plain `@here`, `@channel`, `@everyone` and `#name` carry no markup and are not escaped; the Slack client sends every text object with `verbatim: true` and the fallback text with `parse=none` and `link_names=false`, so Slack does not auto-parse them. Verified on a real workspace on 2026-10-24: an agent's `@here`, `@channel`, `<!here>` and `#general` arrived as plain text with no notification.
- Link URLs are allowed only with the schemes `http`, `https` and `mailto`; other targets render as text.
- The scoped-agent enrollment token lives 24 hours and is single-use; issuing another for the same name revokes the earlier unused one.
- `reset-credential` on a human also clears the recorded Slack binding.
- `ResetData` keeps `bridge_dropped`: like `bridges`, `bridge_messages` and `slack_users`, so a dropped Slack message stays dropped across a data reset.
- Archive and create logs may name the bus channel and the Slack channel id; they never carry a token, message text or email.
- The agent icon is a stable emoji picked from a fixed list by hashing the identity name.
- Schema version 1 seeds the reserved `slack-bridge` identity. (It was a version 6 migration that reshaped any pre-existing `slack-bridge` identity; that reshape is gone with the flattening to version 1, see the ADR 0004 addendum "Migrations flattened to version 1".)

## Decisions (Eric, 2026-10-24)

- Every plan ruling recorded during execution (the "Plan choices" above) is ratified.
- Duplicate Slack posts: a posted-but-unrecorded message is remembered across a lost runner lock and recorded without reposting; after an uncertain post failure (transport error, timeout, 5xx/408, Slack `internal_error`/`fatal_error`/`service_unavailable`/`request_timeout`) the bridge searches the channel or thread for its own post (matching metadata seq and the app's bot id) before posting again, and posts nothing while the search fails.
- Channel mentions are neutral in both directions: Slack `<#C…|name>` reaches the bus as plain `#name` and a mention without a name as `#channel`; an agent's `#name` reaches Slack unlinked. Resolving a nameless mention to its channel name (which needs the `channels:read` scope) is deferred.
- A local OIDC provider for signing a human into the TUI against a local server is deferred (issue #17).

## Decisions of 2026-10-25 (Eric)

Recorded in ADR 0011; the changes to this record:

- **Decision 8 makes scope an access boundary.** The scope gate of `internal/core/access` denies a scoped agent everything outside its scope as `not_found` (the text of a thing that does not exist), and inside its scope the ACL decides. Humans and administrators are never scoped and are visible to every agent. Scoped agents create and rename channels only to `<scope>-*` names, and no one else may take such a name (decision 19 of ADR 0011; implemented by Task 11 of the accesscheck plan).
- **Decision 11 uses the access check.** The bridge asks `Check(Post)` (a human holding `write` through any additive row, a group row included) and invites the `Holders(Post)` list, in identity order. The decision itself is unchanged.
- **Decision 12 covers `humans`.** `CheckBridgeACL` refuses `read` and `write` for `humans` as for `everyone` and `admins`, so the invite list stays explicit.
- **Decision 19** is marked moot above.
- **Bridge not configured** is `unavailable`, not retryable (it was `forbidden`); see "Claude's calls".

## Decision of 2026-10-27 (Eric)

- **Decision 12: no `everyone` or `humans` entry on a bridged channel.** Under the channel-visibility `List` rule any stored row lists a channel to its holders, and `admin` reads its ACL, so an `everyone` or `humans` row holding `admin` alone would show the bridged channel's name and named members to the whole group. `CheckBridgeACL` now refuses any entry for `everyone` or `humans` (`forbidden`, `a bridged channel's ACL may not name <group> (Decision 12)`), at bridge creation, at go-live and on every ACL change. `admins` may still hold `admin` alone: administrators hold it implicitly and list every channel, so that row exposes nothing.

## Decisions of 2026-11-04 (Eric): the scope canvas

Each of these is Eric's decision; they deliver decision 1's "memory and task channels later, one-way, informational". Design: `docs/superpowers/specs/2026-11-04-slack-scope-canvas-design.md`.

1. Each live bridge's Slack channel carries a channel canvas showing the scope's task lists and memory channels, republished when a task or memory changes. One canvas per live bridge.
2. A canvas shows only the task and memory channels every allowed human of that bridge may read.
3. The canvas is read-only in Slack; changes go through agentmesh.
4. Changes are coalesced (default 30 s) and an unchanged rendering is not sent.
5. A server setting, on by default (`slack_canvas_enabled`), turns it on for every live bridge; no wire change.
6. Tasks render as the whole hierarchy, each root task a foldable h3 with its subtasks as a nested checklist, with status and assigned agent; not sorted by status.
7. Font glyphs, not emoji: `◌` pending, `▶` in progress, `✔` done, `⛔︎` blocked, `⚠︎` stalled or problem, `⚑` needs human.
8. A task has a problem when blocked, stalled (abandoned), its owner's status carries a problem, or its owner's status tag is `needs-human`.
9. Memories show in full.
10. The canvas opens with a flat "Needs attention" section: Blocked, Needs human, Stalled and Problem reported; a task appears in every subsection it matches.
11. The canvas tab is titled `Status`; Slack's own outline sidebar is the navigation, so there is no contents list.
12. slack-go moves from v0.30.1 to v0.34.0, whose `Tab.Data.FileID` gives the canvas ID of a channel's tab (plan review, 2026-11-04).
13. One `TestAccessRuleSites` allowlist entry is accepted: `internal/core/tasks/snapshot.go Snapshot` copies a task's `Owner` once into the canvas snapshot's assignee, a data copy and not an access decision (plan review, 2026-11-04).

## Decisions of 2026-11-05 (Eric): the reclaimed mark and the TUI task marks

Both are Eric's decisions. Context: the canvas spec's section 4.3 said a stalled task "stays `⚠︎` until an agent's read or claim reclaims it". It never did: the maintenance job `abandoned tasks` (every `maintenance_interval_seconds`, default 60) returns an abandoned task to `pending` with its owner and lease cleared (`internal/core/tasks/reclaim.go`, a `reclaimed` revision with no sender), and `tasks.List` and `tasks.Get` reclaim before serving, so the TUI's own read reset a stall. A stalled task was visible for at most one maintenance interval, on the canvas and everywhere else. Design: `docs/superpowers/specs/2026-11-04-slack-scope-canvas-design.md` section 2.1 and 4.3; plan: `docs/superpowers/plans/2026-11-05-reclaimed-stalled-and-tui-marks.md`.

1. **A reclaimed task stays stalled until it is claimed.** `reclaimAbandoned` writes `reclaimed_from` (the previous owner) and `reclaimed_at` (Unix ms) into the task document. Every later write copies the document forward, so the mark persists through edits. It clears when the task's status leaves `pending` (a claim, or an update to `in_progress` or `completed`); a delete removes the task. A release (`task_release` by the owner, or an update to `pending`) is not a reclaim and sets no mark. `bus.Task` and `bus.TaskSummary` gain `ReclaimedFrom` and `ReclaimedAt`; the `Task` and `TaskSummary` schemas of `api/openapi.yaml` gain `reclaimed_from` and `reclaimed_at` (the protocol stays `1.0.0` before the first release; `make generate`; `internal/bus/remote` and `internal/server` map them; the MCP `task_get` and `task_list` outputs carry them through the bus types). On the canvas such a task is `⚠︎` stalled, suffix `stalled: reclaimed from <owner> HH:MM UTC` in the tree and `⚠︎ #id subject · reclaimed from <owner> HH:MM UTC · <list>` under Needs attention → Stalled; the precedence is unchanged (blocked, then needs human, which cannot apply without an owner, then stalled). Pre-release rule (Eric, 2026-10-25): the document format changes in place, no migration; a document without the fields is unmarked. One `TestAccessRuleSites` count moves: `internal/core/tasks/reclaim.go reclaimAbandoned` reads `t.Owner` once more to copy it into `ReclaimedFrom` (`{"Owner": 1}` becomes `{"Owner": 2}`): a data copy, not an access decision, like item 13.
2. **The TUI task marks mirror the canvas states, with the canvas precedence.** `⬜` pending (U+2B1C), `⏱️` in progress (kept), `✅` done, `⛔` blocked (U+26D4 U+FE0F, the same glyph as the agent `blocked` status icon; Eric chose it over `🛑`), `⚠️` stalled (U+26A0 U+FE0F: the lease has expired by the client's clock, the owner is not live per the TUI's agent statuses, or the task carries the reclaimed mark) and also for an owner whose status has a problem, `🙋` needs human (the owner's status tag is `needs-human`). U+26A0 is Emoji_Presentation=No, so it gets an `appleTerminalIcons` variant like the gear and the stopwatch. New `Settings.Icons` keys (`task_blocked`, `task_stalled`, `task_needs_human`) and nerd-font glyphs; `docs/tui.md` documents them. Decision 7 of 2026-11-04 (font glyphs) stays a canvas rule: the TUI mirrors the states, not the glyphs.

## Consequences

- A new reserved identity name and, originally, schema version 6; the pre-release versions were flattened to version 1 (ADR 0004 addendum "Migrations flattened to version 1"), so no upgrade step applies before the first release.
- Every channel gets the `reply_to` same-channel check, a visible change for agents that replied across channels.
- The empty-channel reaper now ignores subscriptions held by `slack-bridge` or by identities without a live or resumable session.
- Registration has two modes, `open` and `enrollment`.
- `ListChannels` reports a channel whose bridge is `live` with the `bridged` capability (Eric's decisions 2026-10-25 and 2026-10-26; ADR 0008 "Channel visibility"); it is listed to the identities and groups its ACL names and to administrators (on a bridged channel a group row may hold only `admin`; the `slack-bridge` identity also holds a row).
- Operators run one more moving part (a Slack app with two tokens). A bridge failure never loses a bus message, because the bridge reads from its own acked mark; the worst case is one duplicate Slack post after a crash between a post and its map row.
- Memory and task channels, membership changes after creation and per-message Slack edits stay out of scope for now.
