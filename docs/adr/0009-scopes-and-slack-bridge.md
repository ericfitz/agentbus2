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
11. The bridge's allowed humans are the human identities with `write` on the bridged channel. No separate list.
12. A bridged channel's ACL may grant `read` or `write` only to named identities, never to `everyone` or `admins`.
13. `reply_to` is validated: the parent must exist and be in the same channel at send time, and must not be a memory record. Otherwise the send is a `validation` error. This applies to every channel.
14. On a bridged channel, after validation, `reply_to` is rewritten to the root of the parent's thread (flat threads, Slack's model).
15. The message number to Slack message map is pruned when its scope is retired.
16. Human membership is fixed at bridge creation for now; may be revisited.
17. Scoped agent credentials. An admin API operation `identities create NAME -scope S` creates a scoped agent identity and returns a one-time enrollment token bound to NAME. The agent registers its own key with it, so the private key never leaves the agent's host. A scoped identity can be claimed only with its bound token, in every registration mode.
18. Slack trust. Only full members of the workspace count: Slack guests (restricted and ultra-restricted users) and external (Slack Connect) users never do, even with a matching email. The first verified message records the Slack user ID against the human identity; afterwards a different Slack account presenting the same email is refused.
19. Scoped identity footprint. Scoped agents get no `general` subscription. At retirement their ACL entries and subscriptions are removed everywhere and their task claims released; their names are blocked from reuse permanently. Messages they sent on other channels stay.
20. Retention fix (every deployment). A subscription does not keep a channel alive when its holder is the bridge, or an identity with no live or resumable session (none within `session_retention_hours`). This supersedes the empty-channel rule of ADR 0008 decision 8, which counted any subscription.
21. Registration modes (decided 2026-10-23, after the plan review; amends ADR 0002 decisions 5 and 8). The `closed` registration mode is removed; the modes are `open` (anyone claims a free name, trust on first use) and `enrollment` (an admin-minted token is required). Whether a token is bound to a name is the administrator's choice when minting it; no server setting forbids unbound tokens. Decision 17 applies in both modes. No release has shipped, so `closed` is an unknown value like any other.

## Claude's calls (ratified by Eric, 2026-10-23)

- Slack credentials are token files named by `slack_app_token_file` and `slack_bot_token_file`, so the values never sit in the process environment. `slack_max_message_chars` defaults to 3000 (longer bodies are cut with a note).
- With no Slack settings the bridge is off and bridge calls are `forbidden` "the Slack bridge is not configured"; `unavailable` stays the retryable store failure.
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

## Consequences

- A new reserved identity name and, originally, schema version 6; the pre-release versions were flattened to version 1 (ADR 0004 addendum "Migrations flattened to version 1"), so no upgrade step applies before the first release.
- Every channel gets the `reply_to` same-channel check, a visible change for agents that replied across channels.
- The empty-channel reaper now ignores subscriptions held by `slack-bridge` or by identities without a live or resumable session.
- Registration has two modes, `open` and `enrollment`.
- Operators run one more moving part (a Slack app with two tokens). A bridge failure never loses a bus message, because the bridge reads from its own acked mark; the worst case is one duplicate Slack post after a crash between a post and its map row.
- Memory and task channels, membership changes after creation and per-message Slack edits stay out of scope for now.
