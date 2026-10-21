# ADR 0008: Channel access control lists

Status: Accepted (Eric, 2026-10-20)

Covers Decisions 76, 80, 82 and 88, made by Eric on 2026-10-20
(`.local/review-answers-2026-10-20.md`, answers to Questions 13, 17 and 19
of the design spec and the ACL follow-up), and the second-round
Decisions 89, 90, 92, 93, 94, 95 and 96 (Eric, 2026-10-20, answers to
Questions 25, 26 and 28 to 32; see "Decisions of 2026-10-20, second
round" below); consequences drawn from Decisions 8, 13, 17, 24, 37, 38,
44, 48, 59 and 73.

## Context

ADR 0003 ships a default authorization policy (Decision 24) under which
any identity may read and post on any channel and may create channels
"under its own name" (Decision 59), and defers the policy shape to a later
milestone. ADR 0006 drops direct messages (Decision 13) and records, as
its one open question, that a private two-party conversation then has no
v2 equivalent until that milestone: every message would be readable by
every identity on the server for all of 2.0.0.

v1 derives meaning from channel names: `ChannelNameRule`
(`internal/bus/channels.go:33`) allows one `/` only after a `general/`,
`memory/` or `tasks/` prefix, `prefixKinds` (`channels.go:15`) infers the
kind from the prefix, `IsTaskChannel` (`internal/bus/tasks.go:20`)
recognizes a task list by `tasks/`, and `dm/<identity>` was readable only
by its owner. The design spec's Question 13 asked what shape "under its
own name" should take, and Question 19 whether the kinds and prefixes
survive.

## Decision

Eric decided:

1. **Per-channel authorization ships in 2.0.0**, not in a later
   milestone, and there are no direct messages. Each channel has an ACL
   of `(identity, [permissions])` entries, where `permissions` is a list
   of one or more of `read`, `write`, `admin`; an entry with an empty
   list is invalid and the server rejects it (Eric's refinement, later on
   2026-10-20). Two special identities: `everyone` (any authenticated
   identity) and `admins` (any identity with administrator privilege). The
   channel's creator automatically gets `admin`. Administrators
   automatically have `admin` on every channel. The three permissions are
   discrete, non-overlapping action sets: `read` is subscribe, read,
   receive, filter, query, search, history and the like; `write` is
   publish, send, post, update, tag and the like; `admin` is permission
   management and channel metadata management (for example a future
   rename). Eric's example of an expected ACL:
   `[{everyone, [read, write]}, {<creator>, [read, write, admin]},
   {admins, [admin]}]`. Second refinement, the same day: every ACL is
   evaluated as if it also contained the implicit entries
   `[{admins, [admin]}, {<creator>, [admin]}]`, regardless of what is
   stored; multiple entries for one identity are additive (the union of
   their permissions); any admin may remove the stored entries for
   `admins` or the creator, and the implicit entries still apply, so a
   channel can never become unmanageable. The example's creator entry
   `[read, write, admin]` is a stored entry; the implicit creator entry
   grants `admin` only. (Decision 80)
2. **The create-channel call takes an ACL.** When it is omitted, the
   server-configured default applies, and the shipped default is
   `everyone` read and `everyone` write; read with the example, the
   stored default is `[{everyone, [read, write]},
   {<creator>, [read, write, admin]}, {admins, [admin]}]`. The creator's
   `admin` is always present through the implicit entry. (Decision 88)
3. **Channel names have no reserved shapes.** Ownership is a stored
   attribute, the channel ACL, not part of the name. This supersedes
   Eric's first answer to Question 13 (`<identity>/<name>`). (Decision 76)
4. **Channel kinds stay (`ordinary`, `memory`); the name prefixes go.**
   `general/`, `memory/` and `tasks/` carry no meaning in v2.
   (Decision 82; superseded in part by Decision 90, Eric, 2026-10-20,
   which adds a third kind, `tasks`)

Earlier decisions these change, each left standing and marked in the spec
"superseded in part by Decision N (Eric, 2026-10-20)":

- Decision 24: the policy shape is no longer wholly a future milestone;
  channel ACLs ship in 2.0.0 (Decision 80).
- Decision 59: "may create channels under its own name" loses its name
  constraint (Decision 76); "may read and post on any channel" and "may
  subscribe to any existing channel" hold only as far as each channel's
  ACL allows, which the shipped default ACL grants (Decisions 80, 88).
- Decision 48: channel metadata management moves under the channel's
  `admin` permission (Decision 80); where delete and rename sit between
  the admin API section and the channel permission is open question 1.

## Consequences

Positive:

- A private conversation is a channel whose ACL names its participants;
  ADR 0006's missing property is back without direct messages, and
  without a second kind of channel.
- One authorization subject (the identity, Decision 8) and one mechanism
  for every channel operation; policy, the default ACL and an
  administrator's `admin` everywhere are all expressed as entries, so the
  check in the core is one function.
- No name-derived rules: nothing in delivery, search, tasks, hooks or the
  TUI inspects a prefix; a task list is a channel of kind `tasks`
  (Decision 90), and a name with `/` is simply invalid (Decision 93).
- The default ACL keeps v1's behavior for everyone who never passes an
  ACL, so the 2.0.0 skill text for an ordinary agent does not change.
- Discrete permissions mean an auditor can hold `read` and nothing else,
  and a bot can hold `write` without seeing the channel.

Negative:

- Discrete permissions also mean `admin` grants no reading: an
  administrator or creator whose stored `read` and `write` entries were
  removed, or never written, has admin actions only and must grant itself
  `read` before it can see the channel. That is by design (the second
  refinement says so), and the implicit entries guarantee it can.
- The Store must record each channel's creator durably, since the
  implicit creator entry is evaluated from it and not from the ACL rows.
- An entry must name a registered identity, `everyone` or `admins`
  (Decision 96), so an ACL cannot be prepared for a member that has not
  registered yet.
- Channel creation can be closed to administrators by a server switch
  (Decision 93); when it is, the default policy's "may create channels"
  (Decision 59) does not apply.
- Every channel operation now carries an ACL lookup; the check is in the
  core (Decision 5) and cached or joined as the Store allows, but it is on
  the path of `receive`, `watch` and `search`.
- Tag subscriptions (Decision 38), search (Decision 51), the client-offered
  cursor rule (Decision 44) and the channel list each need an ACL-aware
  scoping rule (Decisions 94 and 95 give them: names and kinds are
  public, content and matches need `read`).
- Without prefixes, the kind is given or defaulted at creation (`ordinary`,
  Decision 93), and a task list is the stored kind `tasks` (Decision 90).
- The v1 `ChannelNameRule`, `prefixKinds`, the prefix branch of
  `resolveKind`, `IsTaskChannel` and `TaskPrefix` are not ported; the
  `NameRule` character rule is.
- The Store gains an aggregate member (ACL entries on channels) and a
  creator column, and the conformance suites gain cases for the implicit
  entries, additive duplicates, empty-list rejection and removal of the
  stored `admins` and creator entries (spec section 5.8).

## Alternatives considered

- **Per-channel read restrictions in a later policy milestone** (the
  spec's Question 17 option A, Claude's recommendation): rejected by Eric;
  2.0.0 ships with ACLs.
- **Private conversations out of scope** (Question 17 option B): rejected.
- **`<identity>/<name>` as a reserved owner prefix** (Question 13 option
  A): chosen first, then withdrawn in favor of no reserved shapes once the
  ACL made ownership a stored attribute.
- **Keeping v1's prefixes** (Question 19 option A, Claude's
  recommendation) and **dropping kinds** (option B): rejected; kinds
  stay, prefixes go.
- **A default ACL of `everyone` read only, or creator only**: the shipped
  default is `everyone` read and write (Decision 88, option D of the
  follow-up).

## Decisions of 2026-10-20, second round (Eric)

Answers to the spec's Questions 25 to 32 (Question 27 is Decision 91,
ADR 0007):

- **Decision 89.** Channel delete and rename are authorized by `admin` on
  the channel; server administrators satisfy it everywhere through the
  implicit `admins` entry; the admin API keeps both operations and a
  channel admin may call them too. (Question 25)
- **Decision 90.** A third stored kind, `tasks`, given at creation,
  identifies a task list; task operations accept only that kind; the v1
  task-channel expiry rules apply to it. (Question 26)
- **Decision 92.** The server creates `general` at first start with the
  configured default ACL and no creator entry; administrators manage it
  through the implicit `admins` entry. (Question 28)
- **Decision 93.** A server configuration switch, enabled by default,
  allows open channel creation: enabled, any authenticated identity may
  create any channel with a free name passing v1 `NameRule`; disabled,
  only administrators may create channels. `NameRule` applies exactly, so
  `/` is banned and v1-style names such as `memory/agentbus2` are
  invalid. `kind` defaults to `ordinary`; the create call takes an
  optional `kind`. (Question 29; departs from Claude's proposal, which
  had no switch and allowed `/`)
- **Decision 94.** Every channel's name and kind are visible to any
  authenticated identity; content needs `read`. (Question 30)
- **Decision 95.** The spec's section 4.6 mapping is accepted as written:
  unsubscribe needs nothing; tag matches, search, history, memory get and
  revisions, task get and list, and offered cursors need `read`; memory
  edit and delete and task create, claim, release and update need
  `write`. (Question 31)
- **Decision 96.** An ACL entry must name a registered identity,
  `everyone` or `admins`; otherwise it is rejected. (Question 32)

Eric's second refinement of Decision 80 closed, before they were asked,
whether `admin` implies `read` (no), whether one identity may appear in
several entries (yes, additive), and whether the `admins` and creator
entries may be removed (yes; the implicit entries remain).

## Open questions for Eric

None. Questions 25 to 32 were decided on 2026-10-20 as above.

## Milestone 3 addendum (2026-10-21)

- **The core check.** `Core.Authorize(ctx, tx, identity, channelID, p)` in
  `internal/core/authz.go` is the one place a channel permission is
  decided. Reserved names are refused by `Authorize` and
  `ReadableChannelIDs`.
- **The matrix.** `TestACLMatrix` in `internal/conformance/bus` has 26
  sub-cases, one per spec section 4.6 row and variant, each with a refusal
  and a success leaf on both engines: 52 leaves per engine, counted by
  `scripts/m3-exit-counts.sh`. Gate self-test block `acl-check-removed`
  proves the matrix fails on both engines when the readable-channel check in
  `Receive` is removed.
- **Decision 94 in the types.** `bus.Channel` carries name and kind only,
  which is what `ListChannels` returns to anyone. `bus.ChannelInfo` embeds it
  with `Creator` and `CreatedAt` and is returned only to the creator on
  `CreateChannel` and to an administrator on `Status`.

### Pending Eric

Each proceeds on the plan's recommendation (option A) with a
`// pending Eric: Milestone 3 item N` marker.

6. **`general` cannot be deleted or renamed**, by anyone including
   administrators: the server recreates it at start and every identity is
   subscribed to it (Decision 92). Option B: allow holders of `admin`.
   Marker: `TestChannelGeneralIsProtected`.
7. **No live-subscriber refusal on delete** (v1 `deleteChannel`): `admin` on
   the channel decides (Decision 89) and subscriptions and cursors cascade.
   Option B: port it with `except` for the caller. No marker.
8. **No empty-channel reaping** (v1 `reapEmptyChannels`): a channel with an
   ACL and a creator is configuration; only task lists expire. Option B:
   port it. No marker.
