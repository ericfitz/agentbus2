# Scopes and the Slack bridge (part 2)

Status: open questions resolved by Eric, 2026-10-23. Issue #14. Depends on part 1,
`2026-10-23-federated-identity-design.md` (human identities, `sender_kind`,
`createIdentityByEmail`, `admins`).

Labels: **Decision (Eric, 2026-10-23)** marks Eric's choice in the
brainstorm. **Claude's call** marks a choice the builder may revisit without
asking, as long as the decisions hold; Eric ratified every Claude's call
in this document on 2026-10-23.

## 1. Intent

pai runs agentic jobs. For each job, the pai orchestrator spawns agents and
knows which humans own the job. It creates an agentbus2 channel for the job
(`pai-job-12345`), gives the agents access, and wants the job's humans to
follow and steer the agents from Slack: an agent's message appears in a
private Slack channel, a human's thread reply appears on the bus as a reply,
a human's new Slack message appears as a new bus message.

Only agentbus2 talks to Slack. pai tells agentbus2 what it wants; agentbus2
creates the Slack channel, invites the humans, bridges both ways and
cleans up. The server has no inbound internet access.

To clean up reliably, agentbus2 needs to know which channels and agent
identities belong together. That is a **scope**, and it is useful beyond
Slack: agentbus2 must manage the lifecycle of its own channels without
depending on pai, and other users have other patterns.

## 2. Goals, non-goals, anti-goals

Goals:
- Scopes: a named group of channels and agent identities with a lifecycle,
  retired as a unit.
- A bridge between one scoped bus channel and one private Slack channel,
  both directions, with no echoes.
- Agents see a Slack human as that human (part 1's human identity and
  `sender_kind: human`), with enough context to understand a thread reply.
- Slack channels are archived and bus resources reclaimed without pai's
  help when pai forgets.
- `reply_to` is validated on every channel.

Non-goals (this version):
- One-way bridging of memory and task channels (planned next, after the
  concept is proven; the design leaves room).
- Changing a bridge's human membership after creation in Slack (invites
  happen once; see 6.4). Reserved for later, not a one-way door.
- Slack edits, deletes, reactions and files.
- More than one Slack workspace.

Anti-goals:
- A message from anyone pai did not authorize reaches the bus.
- An echo: a bridged message comes back across the bridge.
- A broad ACL entry opens a private job channel to the company.
- agentbus2 learns what a "job" is.

## 3. Decisions (Eric, 2026-10-23)

1. Bidirectional bridging of the scope's chat channel only. Memory and task
   channels later, one-way, informational.
2. Absolutely no echoes.
3. Only agentbus2 holds Slack credentials. pai asks; agentbus2 creates the
   private Slack channel, invites the humans and bridges.
4. A Slack human's message reaches the bus attributed to that human's
   identity, marked human, decorated as coming from Slack.
5. Socket Mode: the server dials out to Slack; no inbound access.
6. Lifecycle: pai says the scope is done; Slack is archived 7 days later
   (configurable), or at once after done when no humans remain in the
   Slack channel; a separate, configurable idle timeout archives forgotten
   channels whether or not done was said.
7. agentbus2 manages the lifecycle of its own channels; the "done" call
   carries a flag: retire the bus channels in step with Slack, or leave them
   to the normal rules.
8. **Scopes.** A channel belongs to zero or one scope. An agent identity
   has zero or one scope, set at creation. A channel created by a scoped
   identity takes that scope. pai itself is unscoped but may create scoped
   objects. pai creates fresh agent identities per job, so identities never
   change scope.
9. Only admins create scopes, create agent identities (optionally scoped)
   and create bridges. A bridge requires a scoped channel. Non-admins may
   create channels; those take the creator's scope, if any.
10. Slack message appearance: each agent posts under its own name and
    avatar; subject bold; tags as chips; bus replies are Slack thread
    replies (option A of the mockup).
11. The bridge's allowed humans are the human identities with `write` on
    the bridged channel. No separate list.
12. A bridged channel's ACL may grant `read` or `write` only to named
    identities, never to `everyone` or `admins`.
13. `reply_to` is validated: the parent must exist and be in the same
    channel at send time, and must not be a memory record. Otherwise the
    send is a `validation` error. This applies to every channel.
14. On a bridged channel, after validation, `reply_to` is rewritten to the
    root of the parent's thread (flat threads, Slack's model).
15. The message number to Slack message map is pruned when its scope is
    retired.
16. Human membership is fixed at bridge creation for now; may be revisited.
17. **Scoped agent credentials.** An admin API operation `identities create
    NAME -scope S` creates a scoped agent identity and returns a one-time
    enrollment token bound to NAME. The agent registers its own key with
    it, so the private key never leaves the agent's host. A scoped
    identity can be claimed only with its bound token, in every
    registration mode (closing the keyless-row takeover of
    `internal/core/identities/register.go` for scoped rows).
18. **Slack trust.** Only full members of the workspace count: Slack guests
    (restricted and ultra-restricted users) and external (Slack Connect)
    users never do, even with a matching email. The first verified message
    records the Slack user ID against the human identity; afterwards a
    different Slack account presenting the same email is refused.
19. **Scoped identity footprint.** Scoped agents get no `general`
    subscription. At retirement their ACL entries and subscriptions are
    removed everywhere and their task claims released; their names are
    blocked from reuse permanently. Messages they sent on other channels
    stay.
20. **Retention fix (every deployment).** A subscription does not keep a
    channel alive when its holder is the bridge, or an identity with no
    live or resumable session (none within `session_retention_hours`).
21. **Registration modes (decided 2026-10-23, after the plan review;
    amends ADR 0002 decisions 5 and 8).** The `closed` registration mode
    is removed; the modes are `open` (anyone claims a free name, trust on
    first use) and `enrollment` (an admin-minted token is required).
    Whether a token is bound to a name is the administrator's choice when
    minting it (`create-identity`, `enrollment create -identity`); there
    is no server setting that forbids unbound tokens. Decision 17 applies
    in both modes. No release has shipped, so `closed` needs no
    compatibility handling: it is an unknown value like any other.

## 4. Scopes

### 4.1 The object

A scope has a name (the channel name rule), a state and timestamps:
- `open`: created by an admin.
- `done`: an admin called `done` with a retirement mode, `with_bridge` or
  `normal` (Decision 7).
- `retired`: its resources are gone; the row is kept for
  `session_retention_hours` so a late call gets a clear `not_found`
  "scope %q is retired", then deleted (Claude's call; the error code set is
  closed, `internal/bus/errors.go`, so no new code).

A scope records `last_activity`, updated on every message on its channels
and every bridged Slack message, so the idle clock does not fall back to
creation time when messages age out (Claude's call).

API (admin only): `scopes create NAME`, `scopes list`, `scopes get NAME`,
`scopes done NAME -retire with_bridge|normal`. `-retire with_bridge` on a
scope with no live bridge is `conflict` (Claude's call). CLI, MCP is not
needed (Claude's call: pai uses the API or CLI; agents never manage
scopes).

### 4.2 Membership

- `channels create` takes an optional `scope`. An admin may name any open
  scope. A non-admin may not name one; a scoped creator's channel takes
  the creator's scope; an unscoped non-admin's channel is unscoped.
- An admin-created agent identity takes an optional `scope`
  (`create-identity -scope S`, and the admin API operation of Decision
  17). Self registration (open, enrollment) must never
  yield or claim a scoped identity.
- A channel's or identity's scope never changes. Human identities are
  never scoped.
- Creating anything in a scope that is not `open` is `conflict`.

### 4.3 Retirement

A scope is retired when:
1. `with_bridge`: its Slack channel is archived (by any rule in 6.5).
2. `normal`: its last channel has been removed by the normal rules (4.4);
   until then its Slack channel follows 6.5 and its channels follow 4.4.
   Memory channels are never reaped by the normal rules and task lists
   expire after 30 days, so in practice a scope with either retires
   through rule 3 (idle). That is intended (Claude's call).
3. Idle: no message on any of its channels, in either direction, for
   `scope_idle_hours` (default 336, 14 days), whatever its state. The
   scope is abandoned: everything goes (Claude's call; Eric decided the
   Slack side of this).

Retiring a scope, in one maintenance job under the maintenance lock:
archives its Slack channel if not already archived (6.5); deletes its
channels of every kind (chat, memory, tasks) with their messages,
subscriptions and ACLs; deletes its scoped agent identities with their keys
and sessions (the client-side session state file is removed by the
client's existing start-up sweep, not by the server); deletes its bridge
rows and message map rows. Outside the scope (Decision 19): removes the
retired identities' ACL entries and subscriptions on every channel,
releases their task claims, and records their names as permanently
reserved; their messages stay.
Human identities and unscoped channels are never touched. A deleted
identity's bearer is refused with `not_registered` (403) at its next call,
as today.

### 4.4 Normal rules for channels (a fix for every deployment)

Today an ordinary channel is reaped only when it has no messages and no
subscriptions, and subscriptions belong to identities that never expire. A
channel any agent ever subscribed to lives forever. Decision 20: a
subscription does not keep a channel alive when its holder
is the bridge, or an identity with no live or resumable session (none
within `session_retention_hours`).

## 5. Slack transport

### 5.1 Credentials and configuration

New server settings (Decision 85 configuration; secrets follow the
existing secret pattern of the server config, file paths preferred so the
values never sit in the process environment; Claude's call):
- `slack_app_token_file`: app-level token (`xapp-`), scope
  `connections:write`, for Socket Mode.
- `slack_bot_token_file`: bot token (`xoxb-`) for the Web API.
- `slack_archive_after_done_hours` (default 168).
- `scope_idle_hours` (default 336).
- `slack_max_message_chars` (Claude's call, default 3000; longer bodies are
  truncated with a note pointing to agentbus2).

Bot scopes (fact-checked 2026-10-23): `groups:write` (create, archive),
`groups:write.invites` (invite), `groups:history` (history and the
`message.groups` event), `groups:read` (`member_left_channel`),
`users:read` and `users:read.email` (`users.info` with its email field;
`users.lookupByEmail` needs only the latter),
`chat:write` and `chat:write.customize` (post as an agent name and icon).
The app is internal to one workspace; Socket Mode apps cannot be listed in
the public Slack Marketplace, which does not matter here, and Slack's 2025
history rate cuts do not apply to internal apps.

No Slack settings: the bridge is off and bridge calls are `forbidden`
"the Slack bridge is not configured" (Claude's call: `unavailable` means a
retryable store failure in `internal/bus/errors.go`).

### 5.2 One bridge runner

Exactly one replica runs the bridge, holding a `Store.Locks` lock
(`slack-bridge`), like maintenance. If it dies, another replica takes the
lock and the bridge resumes from stored state. The runner keeps one Socket
Mode connection and acks each envelope immediately, before processing
(Claude's call; processing is idempotent via the map, 6.3).

### 5.3 Catch-up

- Slack to bus: Socket Mode does not promise replay of events missed while
  disconnected. On every (re)connect, and on lock takeover, the runner
  reads each live bridge's Slack history (and thread replies) since the
  newest Slack `ts` it has mapped, and processes unseen messages as if they
  were events.
- Bus to Slack: the bridge reads each bridged channel as a subscriber with
  its own acked mark (a reserved internal identity, `slack-bridge`, kind
  agent; its name is reserved like `everyone` and `admins`
  (`internal/store/identities.go`), so no registration can claim it or
  attach a key; the bridge adds `{slack-bridge, [read]}` to the
  channel's ACL at creation; Claude's call). Unacked messages are posted
  after an outage.
- Rate limits: `chat.postMessage` is about one message per second per
  channel. The runner queues per channel and honors `Retry-After`.

## 6. The bridge

### 6.1 Create

`bridges create CHANNEL [-slack-name N]`, admin only. Preconditions:
CHANNEL exists, is scoped, its scope is `open`, it has no bridge, its ACL
satisfies Decision 12. Steps:
1. Slack channel name: N, or CHANNEL lowercased with every character
   outside `a-z0-9-_` replaced by `-`, cut to 80 characters (Claude's call).
   `name_taken` is `conflict` (no automatic suffix; Claude's call: pai
   names jobs uniquely, and a silent rename would surprise humans).
2. `conversations.create` with `is_private=true`.
3. For each human identity with explicit `write` on CHANNEL: resolve its
   email binding to a Slack user (`users.lookupByEmail`), then
   `conversations.invite` (with `force=true`, so one bad user does not fail
   the batch). Humans not found in Slack are reported in the result and in
   a notice posted to both channels.
4. Store the bridge row (bus channel, Slack channel ID, state `live`).

Second run of create on the same channel: `conflict` "already bridged".
Leftovers from a failed run (Slack channel created, row not stored): the
bridge row is written before step 2 in state `creating` with the intended
name; a retry finds it, looks up the Slack channel by name, and resumes
(Claude's call). `already_in_channel` on invite is success.

### 6.2 Bus to Slack (option A)

- Each bus message on a live bridge is posted with `chat.postMessage`,
  `username` = the sender's short name (the identity name with the scope
  name and a following `-` removed when it starts with it; Claude's call),
  `icon_url` or `icon_emoji` derived from the identity (builder's choice of
  scheme).
- Body: subject in bold on the first line; content converted from Markdown
  to Slack mrkdwn; tags as a context block of chips; truncated per
  `slack_max_message_chars`.
- A message with `reply_to` posts into the thread of the mapped Slack root
  (`thread_ts`). A parent with no mapping (sent before the bridge, or its
  map row is gone) is posted top-level with a quote of the parent if it
  still exists.
- Messages sent by the bridge itself on a human's behalf are never posted
  back (map lookup by bus seq; Decision 2).
- Memory-kind messages are never posted (Decision 1).

### 6.3 Slack to bus

For each `message` event (and catch-up message) on a bridged Slack channel:
1. Ignore: messages with a `bot_id` belonging to this app, every subtype
   other than plain messages and `thread_broadcast` (`message_changed`,
   `message_deleted`, `channel_join`, `channel_leave`, file shares are
   ignored; Claude's call per non-goals), and any `ts` already in the map
   (idempotence, no echo).
2. Resolve the Slack user (`users.info`, cached) and refuse guests and
   external users (Decision 18). Resolve its email to a human identity
   through part 1's binding; if the identity already records a different
   Slack user ID, refuse; otherwise record this one on first success. Drop and log (server log, with
   the Slack user ID and channel, never the content) unless that identity
   holds explicit `write` on the bus channel now (Decision 11).
3. Send on the bus as that identity, in a session with hint `slack` (one
   per human per server; display `alice@corp.com/slack`), with metadata
   `slack_display_name` (decoration only) and, for a thread reply,
   `reply_to` = the bus seq of the thread root and `slack_after` = the bus
   seq of the newest bridged message in that thread when the human posted.
4. Permalink targeting: when the text contains a Slack permalink to a
   bridged message in the same channel, that message is the parent (then
   rewritten to its root by Decision 14) and `slack_after` names it.
5. Store the map row (bus seq, Slack ts, thread root ts) in the same
   transaction as the send.

A thread reply whose root has no map row but was posted by this app (its
bus message aged out of retention) is sent top-level, quoting the root's
Slack text.

### 6.1a List, get, remove

`bridges list`, `bridges get CHANNEL`, `bridges remove CHANNEL` (admin
only; Claude's call). Remove posts the final line, archives the Slack
channel, and deletes the bridge row and its map rows; the scope and its
bus channels are untouched. A removed bridge cannot be re-created on the
same channel while the archived Slack channel holds the name (`conflict`
unless `-slack-name` differs).

### 6.1b Protecting a live bridge (Claude's calls)

- Only a server admin may change the ACL of a channel with a live bridge,
  because that ACL is the bridge's allow list (Decision 11); the channel's
  own `admin` holders (for example a scoped agent that created it) are
  refused `forbidden`. The `{slack-bridge, [read]}` entry is pinned: an
  ACL change that drops it is refused.
- A channel with a live bridge cannot be deleted or renamed except by
  `bridges remove` first; empty-channel reaping skips it.
- Message retention still applies; aged-out parents are handled by the
  quoting fallbacks in 6.2 and 6.3.
- If the bus channel disappears anyway, the runner removes the bridge as
  in 6.1a.

### 6.4 Membership

Decision 16: invitations happen once, at create. Later ACL changes still
take effect for Slack-to-bus (step 2 above reads the ACL each time): a
human removed from the ACL is dropped from then on, though still in the
Slack channel; a human added later is accepted if an admin invites them in
Slack by hand. The ACL guard of Decision 12 holds while the bridge is live:
an ACL change granting `read` or `write` to `everyone` or `admins` on a
bridged channel is `forbidden`.

### 6.5 Archiving

The Slack channel is archived, and the bridge stops in both directions, at
the earliest of:
- `slack_archive_after_done_hours` after `done`;
- after `done`, when no human remains in the Slack channel
  (`member_left_channel` events, checked against the members list; never
  joined counts);
- `scope_idle_hours` without traffic (4.3, idle).

Before archiving, the bridge posts a final line ("This channel is archived;
the record stays in agentbus2 until its scope is retired."). Then
`conversations.archive`; `already_archived` is success, so a second run or
an overlapping replica is harmless.

## 7. `reply_to` rules (all channels)

- Validation (Decision 13) in `core.Append`
  (`internal/core/append.go`): a parent that does not exist stays
  `not_found` "reply_to %d does not exist", as today (Claude's call: keeps
  the existing code and its test); a parent in another channel is
  `validation` "reply_to %d is in another channel". On memory and task
  channels every row is a memory record, so "not a memory record" reduces
  to the same-channel rule there and is not checked separately (Claude's
  reading of Decision 13: its purpose is that an ordinary message never
  points into a memory or task channel). The conformance suite gets a case
  for each refusal.
- Rewrite (Decision 14): on a channel with a live bridge, a validated
  `reply_to` whose parent itself has a `reply_to` is replaced by the
  parent's `reply_to` (one hop suffices, because every stored reply on a
  bridged channel already points at a root). Messages sent before the
  bridge existed may be deeper; the rewrite then walks to the root
  (Claude's call).
- The MCP `receive` description and the using-agentbus skill tell agents:
  on a message from a `human` with a `reply_to`, read the thread (`history`
  with the thread filter) before acting; `slack_after` names what the human
  last saw.

## 8. Storage

New tables (one schema version, both engines): `scopes`; scope columns on
`channels` and `identities`; `bridges` (bus channel, Slack channel ID,
state, cursor of the newest mapped Slack `ts`); `bridge_messages` (bridge,
bus seq, Slack ts, root ts; unique on each side). Pruned with the scope
(Decision 15) and when a bridge is removed.

## 9. Testing

Every Slack call goes through an interface; the conformance suite and unit
tests use a fake Slack (Socket Mode envelopes and Web API) that records
calls and can drop, repeat and reorder events. Required cases: no echo in
either direction; an unauthorized Slack user's message never reaches the
bus; catch-up after a disconnect posts each missed message once; two
runners never both hold the lock; create resumed after a failed run;
archive and retire run twice without error; the ACL guard; each `reply_to`
refusal and the rewrite. No test reaches Slack.

## 10. Open questions for Eric

None. The builder check's questions were answered on 2026-10-23
(Decisions 17 to 20). Decision 21 was decided after the plan review.

## 11. Later (room left)

- One-way bridging of a scope's memory and task channels to Slack
  (informational; a bridge row with a direction).
- Changing a bridge's human membership (invite or kick on ACL change).
- User-ID bindings (part 1).
