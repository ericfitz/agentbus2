# Channel visibility design (ACL-filtered listing, channel `capabilities`, the TUI's bridged icon)

Status: Agent design of 2026-10-25 for Eric's decisions of the same day.
Section 1 records the four decisions Eric made first; section 4 records his
answers to the ten questions the design raised, each labeled **Eric's
decision 2026-10-25**. One item is pending (section 5). Nothing else in
this document is a decision. The implementation plan is
`docs/superpowers/plans/2026-10-25-channel-visibility.md`.

Related: `docs/superpowers/specs/2026-10-19-agentbus2-design.md` sections
3.1 (Decision 43, discovery and capabilities), 3.2 (the channels row of the
operation outline), 4.6 and 4.6.1 (Decisions 80, 94, 95 and the
operation-to-permission table), 12 (Question 30); ADR 0001 (transport),
ADR 0007 (oapi-codegen; `make generate`), ADR 0008 (channel ACLs; Decision
94 and its Milestone 3 and Milestone 8 addenda), ADR 0009 (scopes and the
Slack bridge; decisions 11, 12, 19 and "Protecting a live bridge");
`docs/tui.md` ("Rail markers", `icons`, `icon_map`);
`docs/ops/slack-bridge.md`.

## 1. Eric's decisions (2026-10-25)

1. **Eric's decision 2026-10-25 (1): `ListChannels` is filtered by the
   caller's ACL standing.** `ListChannels` returns only the channels on
   which the caller holds an ACL entry (refined in section 4, question 1:
   any entry, whatever its permissions), everywhere the listing is
   exposed: the REST API (`GET /v1/channels`), `internal/core`
   (`channels.Service.List`), the MCP `list_channels` tool, the CLI
   `channels list` and the TUI. Exception: an admin-flagged identity
   (Decision 73, the `Admin` flag of its identity row) gets the unfiltered
   list. This supersedes Decision 94 ("every channel's name and kind are
   visible to any authenticated identity"; ADR 0008 "Decisions of
   2026-10-20, second round"; spec section 4.6.1 and Question 30; the
   Milestone 8 plan line "`ListChannels` lists every channel, readable or
   not", `docs/superpowers/plans/2026-10-21-milestone-8-tui.md:36`).
2. **Eric's decision 2026-10-25 (2): each listed channel says whether it
   is bridged.** True when the channel has a live Slack bridge (ADR 0009;
   `internal/slackbridge`; the `bridges` rows of the Store), visible to
   every caller who can see the channel. Refined in section 4, questions
   3 to 5: the trait travels as the string `bridged` in a required
   `capabilities` array, never as a boolean, and "live" means the bridge
   row's state is exactly `live`. A new discovery capability (Decision
   43, `GET /v1/discover` `capabilities`) advertises the change (its name
   is the pending item, section 5).
3. **Eric's decision 2026-10-25 (3): the TUI draws a bridged channel with
   its own icon.** Emoji set: 🔗 (U+1F517 LINK SYMBOL, Wide). Nerd Font
   set: the Slack logo (Font Awesome `slack`, U+F198 in Nerd Fonts). A new
   icon name, `bridged`, that `tui.icon_map` may override.
4. **Eric's decision 2026-10-25 (4): the TUI's `(no access)` rail marking
   becomes mostly moot**; what remains of it is question 6 (kept as
   built, for the two cases that remain).

## 2. What exists today (checked against source, 2026-10-25, after b19416e)

- `channels.Service.List` (`internal/core/channels/channels.go:20`)
  returns every channel's name and kind, no permission needed (Decision
  94); `s.read` runs `RequireIdentity`, which returns the `store.Identity`
  with its `Admin` flag (`internal/core/authz.go:58`).
- The Store knows two ACL queries (`internal/store/channels.go:68-74`,
  `internal/store/sqlstore/acl.go:139,178`): `EffectivePermissions(id,
  identity)` (the union of the stored rows for the identity, `everyone`
  and, for an administrator, `admins`, plus the implicit `admin` of the
  creator and of every administrator) and `ReadableChannels(identity)`
  (the ids whose stored rows grant `read`). Nothing lists the channels on
  which an identity holds *any* permission; `Receive`
  (`internal/core/delivery/receive.go:54`), `Search` without a channel
  (`internal/core/search/service.go:68`), the offered-cursor rule and the
  pending counts (`internal/core/sessions/open.go:187,221`) scope by
  `ReadableChannels` and keep doing so. `History`, `Subscribe`, `Search`
  with a channel, the memory and task reads resolve the name with
  `ChannelByName` (`not_found` for a missing name) and then `Authorize`
  (`forbidden`).
- `bus.Channel` (`internal/bus/types.go:31`) is `Name` and `Kind`;
  `bus.ChannelInfo` embeds it with `Creator`, `Scope`, `CreatedAt`, and is
  returned by `CreateChannel` and the administrator's `Status`
  (`internal/core/channels/admin.go:42` lists every channel).
- Wire: `Channel` schema `required: [name, kind]`
  (`api/openapi.yaml:1454`); `ChannelInfo` `required: [name, kind,
  creator, created_at]` (`:1463`); `listChannels` describes its 200 as
  "Channels visible to the caller." Handler `internal/server/channels.go:51`,
  remote client `internal/bus/remote/channels.go:13`, local
  `internal/bus/local/channels.go:12`.
- Discovery: `server.Capabilities` (`internal/server/discovery.go:28`)
  returns `health`, `long_poll` and, with an embedder, `semantic_search`,
  sorted; `TestCapabilitiesComputed` (`discovery_test.go:70`) pins the
  list; `docs/ops/deploy.md:101` and `docs/ops/health.md:121` name it. No
  client of this module reads `capabilities` today.
- Bridges: `store.Bridge` has `ChannelID`, `State` (`creating`, `live`,
  `archived`; `internal/store/bridges.go`), `Tx.ListBridges` (by channel
  name) and `Tx.BridgeByChannel`. `channels.liveBridge`
  (`channels.go:118`) treats `creating` and `live` as the states that pin a
  channel's ACL, name and existence (ADR 0009 "Protecting a live bridge");
  that pin rule is deliberately wider than the `bridged` trait (question
  3). `bridges create` requires an ordinary channel
  (`internal/slackbridge/create.go:200`), so a bridged channel is always
  of kind `ordinary`.
- The `slack-bridge` identity never calls the Bus: `store.ReservedIdentity`
  (`internal/store/identities.go:59`) names it, `core.reservedIdentity`
  refuses it in `RequireIdentity`, `Authorize` and `ReadableChannelIDs`,
  and every read of `internal/slackbridge` is a direct `tx.*` call
  (`ListBridges`, `Channel`, `Subscriptions(store.SlackBridge)`,
  `MessagesAfter`). Decision (1) does not touch the bridge.
- Clients: MCP `list_channels` (`internal/mcpserver/server.go:469`)
  returns `{channels: [{name, kind}]}` (`channelOut`, `server.go:167`);
  the skill text names it four times
  (`internal/cli/skills/using-agentbus2/SKILL.md:39,100,111,292`), once as
  the way to find memory channels to `search`. `agentview.KindIndex`
  (`internal/agentview/kinds.go:52`) loads the kind map from
  `ListChannels` to drop memory-channel messages from an agent's receive
  and wake. CLI `channels list` (`internal/cli/channels.go:50`) prints
  `[{name, kind}]`; `channels delete` without `-y` (`channels.go:118`)
  looks the kind up in `ListChannels` and answers `not_found` itself when
  the name is absent; `status` (`internal/cli/status.go:40`) prints the
  list with creators from `Status` when the caller is an administrator.
- TUI (after b19416e, which removed `icons_unix.go` and
  `icons_windows.go` and added `LoadIconsFor` and `appleTerminalIcons`):
  `newClient` (`internal/tui/client.go:78`) lists and subscribes to every
  channel; a `forbidden` `Subscribe` marks the channel `subNoAccess`
  (`client.go:147`), and so does a `forbidden` `History`
  (`model.go:1410`, `markNoAccess`). The rail draws such a row dim with
  `(no access)` (`view.go:555`), the stream shows `no read access to
  <name>` (`view.go:647`), and nothing retries it. `statusCmd`
  (`model.go:1369`) relists every `statusEvery` = 5 s (`model.go:47`) and
  `setChannels` (`model.go:1303`) replaces the rail from the answer. The
  kind icon is chosen by a `switch` on `Kind` in four places:
  `view.go:68` (`channelLabel`), `view.go:547` (the rail row),
  `view.go:784` (the compose prompt) and `search.go:134` (search hits).
  Icons: `iconVars` (`icons.go:84`), `emojiIcons` (`:92`), `nerdIcons`
  (`:104`), `padIcon` (`:124`), `LoadIcons`/`LoadIconsFor` (`:149,156`).
  `TestLoadIconsNerdfontWidthParity` (`icons_test.go:30`) requires every
  wide icon to be 3 columns in the Nerd Font set and
  `TestIconSetsCoverTheSameNames` (`icons_test.go:144`) requires
  `nerdIcons` and `iconVars` to name the same icons; `docs/tui.md`
  documents the sets, `icon_map` and the Terminal.app padding.
- Tests that pin today's behavior: `TestChannelListNeedsNoRead`
  (`internal/conformance/bus/channels_test.go:154`), the `list channels`
  row of `TestACLMatrix` (`aclmatrix_test.go:357`: `perm: ""`, both legs
  run with an ACL naming `pat` only, refusal uses `History` as a proxy,
  success finds the channel in the list; `matrixSubcases = 26`,
  `scripts/m3-exit-counts.sh` requires 52 leaves per engine),
  `TestNoAccessChannelInRail` (`internal/tui/model_test.go:863`) and
  `TestNoAccessChannelIsMarkedOnce` (`client_test.go:191`), both with a
  non-administrator who sees a channel it cannot read.
- Gate anchors that must not move: `if !readable[sub.ChannelID] {` in
  `receive.go` (block `acl-check-removed` of
  `scripts/check-gates-selftest.sh:246`) and `if !id.Admin {` in
  `authz.go` (`RequireAdmin`).

## 3. Design

### 3.1 The rule, in the Store and the core

A new Store query, `Tx.VisibleChannels(ctx, identity) ([]int64, error)`,
lists the ids of the channels on which the identity holds any ACL entry,
under ADR 0008's own definition of the ACL (every ACL is evaluated as if it
also contained the implicit entries): a stored row for the identity, a
stored row for `everyone`, a stored row for `admins` when the identity is
an administrator, or the implicit creator entry (`channels.creator =
identity`). A row with `can_read`, `can_write` or `can_admin` counts; the
Store rejects an entry with no permission (Decision 80), so "any row" and
"any permission" agree. It mirrors `ReadableChannels`
(`sqlstore/acl.go:178`) with the permission test dropped and the creator
clause added, and is specified in the Store conformance suite on both
engines. `ReadableChannels` and `EffectivePermissions` are unchanged: they
remain the definition of "may read" for `Receive`, `Search` and the cursor
rules, which the listing no longer follows (Eric's decision, question 1).

`channels.Service.List(ctx, identity)` becomes, in one read transaction:

1. `RequireIdentity` (unchanged: `""`, `everyone`, `admins` and
   `slack-bridge` are `not_registered`).
2. `tx.ListChannels` (every channel, by name).
3. When the identity row's `Admin` is false: `tx.VisibleChannels(identity)`
   and keep only those ids. When it is true: keep every channel (decision
   (1), the exception). The admin flag is read from the row
   `RequireIdentity` returned; `RequireAdmin` is not called and not edited.
4. `tx.ListBridges` once; a channel carries the `bridged` capability when
   its bridge row's `State == store.BridgeLive` (question 3). The pin
   rule `liveBridge` (`creating` or `live`) stays as it is; the two
   predicates differ on purpose and each says so in its comment.

The administrator's `Status` computes the same `Capabilities` for its
`ChannelInfo` rows (`bus.ChannelInfo` embeds `bus.Channel`, so the member
is there and must be honest). `CreateChannel` returns an empty array (a
bridge cannot exist yet).

### 3.2 Channel capabilities on the Bus and on the wire

Eric's principle (question 4): a channel's traits travel as a
`capabilities` array of strings, never as booleans, so a future trait needs
no protocol change; a string present means the trait applies; a client
ignores strings it does not know.

- `bus.Channel` gains `Capabilities []ChannelCapability`
  (`type ChannelCapability string`; `const ChannelBridged ChannelCapability
  = "bridged"`; `func (c Channel) Has(cap ChannelCapability) bool`). The
  slice is never nil on the Bus (`[]ChannelCapability{}` when empty).
  `bus.ChannelInfo` inherits it.
- OpenAPI: `Channel` and `ChannelInfo` gain `capabilities: {type: array,
  items: {type: string}}`, listed in `required` on both (empty allowed:
  `[]`, never null). The schema does not enumerate the values (an enum
  would make a new trait a protocol change, which the principle forbids);
  the description names `bridged` and says clients ignore unknown values.
  `make generate`; `internal/server/channels.go` `ListChannels`,
  `channelInfoToGen`; `internal/bus/remote/channels.go` `ListChannels`,
  `fromGenChannelInfo` copy the strings through, refusing nothing
  (Definition of Done B applies to values a client acts on; an unknown
  trait is one it does not act on, by the principle).
- `GET /v1/discover` `capabilities` gains one name so a client can tell a
  server with channel capabilities from an older one; the name,
  `channel_capabilities`, is proposed and pending (section 5).
  `server.Capabilities` is the one place; sorted output.

### 3.3 Clients

- `internal/bus/remote`: maps `capabilities`. Against a server that
  predates the member, the generated client leaves it nil; the Bus
  normalizes to an empty slice and does not refuse (section 3.8).
- MCP `list_channels`: `channels[].capabilities`; the description says the
  list is the channels you hold an ACL entry on (an administrator: every
  channel), that `capabilities` holds `bridged` when people follow the
  channel from Slack, and that unknown strings are to be ignored.
  `channelInfoOut` (the `create_channel` answer) gains the member too.
  `agentview.KindIndex` is unchanged in effect: a channel the agent holds
  no entry on is absent from its map, and no message from such a channel
  reaches the agent anyway (`Receive` scopes by `ReadableChannels`; tag
  matches need `read`, Decision 95). Memory channels the agent holds an
  entry on stay listed (the skill tells agents to `search` them).
- CLI `channels list`: `[{name, kind, capabilities}]`; `channels create`
  prints the member on its `ChannelInfo`. `status`: a row whose
  capabilities include `bridged` is suffixed ` bridged`. `channels delete`
  without `-y`: when the list lacks the name (and no `-key`), the prompt
  names the channel without a kind and the Bus answers (`not_found`,
  `forbidden` or success); the command no longer invents `not_found`
  (question 7).
- TUI: section 3.4.
- The skill text (`SKILL.md`) says `list_channels` shows the channels you
  hold an ACL entry on; the sentence about another agent finding a private
  channel with `list_channels` holds because that agent is named in the
  ACL (the surrounding text already says so).

### 3.4 TUI

- `bus.Channel.Capabilities` flows through `statusMsg` and `setChannels`
  as the kind does; the model keeps the full `bus.Channel` by name next
  to `m.kinds` (`channelOf(name)`) so `search.go` can find a hit's channel.
- One helper, `channelIcon(c bus.Channel) string`, replaces the four
  `switch` statements: tasks, memory, then `c.Has(bus.ChannelBridged)`
  (only an ordinary channel can be bridged), else chat; it is used at
  every site that draws the kind icon (question 10): the rail row, the
  header and stream label (`channelLabel`), the compose prompt and the
  search hits.
- Icons: a new `iconBridged` variable in the emoji `var` block
  (`"\U0001F517️ "`, the file's convention of U+FE0F on every emoji
  icon; U+1F517 is Emoji_Presentation=Yes, so it needs no Terminal.app
  variant in `appleTerminalIcons`), `nerdIcons["bridged"] = ""`
  (fa-slack; `padIcon` pads it to 3 columns), `iconVars["bridged"]`.
  `TestLoadIconsNerdfontWidthParity` and `TestIconSetsCoverTheSameNames`
  cover it by construction; `docs/tui.md` names it.
- `(no access)` (question 6, decision (4)): kept as built. It now appears
  for an administrator (listed unfiltered; `Subscribe` and `History` are
  `forbidden` on a channel whose stored rows give it no `read`; this is
  the one steady state the marking describes from now on), for an
  identity whose only entry is `write` or `admin` (listed by decision (1),
  unreadable by Decision 80), and, for anyone, during the window between a
  revocation and the next status tick (at most `statusEvery` = 5 s, after
  which the channel leaves the rail; question 9: the tick is the only
  refresh). The stale `client.subscribed` entry for a vanished name stays
  (a re-granted channel comes back `subOn` when its server-side
  subscription survived, `(no access)` when it was marked in that window;
  both are what happens today).

### 3.5 The Slack bridge

No change. The bridge reads through the Store as `slack-bridge` and never
lists channels through the Bus. `bridges list|get|remove` stay
administrator-only (ADR 0009). A bridged channel's ACL names only
identities (ADR 0009 decision 12), so under decision (1) a bridged channel
is listed to exactly its named identities (its humans with `write`
included, ADR 0009 decision 11) and the administrators.

### 3.6 Other enumerations (no change; Eric's decision, question 2)

- `Search` without a channel: readable channels only
  (`search/service.go:68`). With a channel: `not_found` for a missing name,
  `forbidden` without `read`.
- Tag subscription matches: `read` on the channel (Decision 95).
- `History`, `Subscribe`, memory and task reads: `not_found` then
  `forbidden`. `create` of an existing name the caller cannot list is
  `conflict`; `rename` onto one is `conflict`. Names are not secrets; the
  list is a filter for noise and content privacy, not a secrecy guarantee.
- `Receive`, `Watch`, `Peek`: readable subscriptions only.
- `Identities` (the MCP `discover` tool): identities and sessions, no
  channels. `Status`, `Usage`: administrators only, unfiltered.

### 3.7 Consequences

- A write-only identity sees the channel in the list (it holds an entry)
  but cannot read it: in the TUI it is a `(no access)` row; `subscribe`
  and `history` answer `forbidden` as before. A channel administrator
  without a stored `read` row likewise lists the channel it manages. A
  creator lists a channel it created with `acl: []` (the implicit creator
  entry is an ACL entry under ADR 0008's definition); if Eric meant stored
  entries only, the creator clause of `VisibleChannels` is one line to
  drop.
- Scoped identities (ADR 0009 decision 8) get no further restriction: the
  ACL is the one mechanism. A scoped agent sees `general` (its default ACL
  grants `everyone` read and write) although it is not subscribed to it
  (decision 19). Retiring a scope removes the retired identities' ACL
  entries (`scopes/retire.go:156`), so their channels leave other
  identities' lists only where those entries were the grant.
- `general` and a channel with the shipped default ACL are listed to
  everyone, as today. A channel created with `acl: []` is listed to its
  creator and the administrators.
- Human identities (`kind: human`) and agents are treated alike; the TUI's
  human is the common case of an administrator seeing `(no access)`.
- `SetACL` already publishes a zero-seq hint so an armed watch re-checks
  readability; it does not re-run the TUI's `statusCmd`. The rail follows
  an ACL change on the next tick (question 9).

### 3.8 Backward compatibility

- Older client, newer server: the client ignores `capabilities` (the
  generated client decodes into known members) and gets a shorter list;
  the TUI of such a client shows `(no access)` rows only where it still
  would, and the discovery capability tells an operator why.
- Newer client, older server: `capabilities` is absent and normalizes to
  empty; the list is unfiltered and the client's `(no access)` path still
  works; no bridged icon is shown. The remote Bus does not refuse the
  response (an absent array is not an unrecognized value); the discovery
  capability is how a client tells the two servers apart.
- No schema change (the new Store query reads existing tables), no
  setting, no migration, no change to the MCP tool count (25).

## 4. Eric's decisions on the open questions (2026-10-25)

Each is **Eric's decision 2026-10-25**, answering the ten questions the
first draft of this document put to him (the option letters refer to that
draft, which was not committed; each item below restates the question in
its answer).

1. **Any ACL entry lists the channel**, not `read` alone: an identity
   holding `read`, `write` or `admin` (stored, or the implicit creator
   entry under ADR 0008's definition) on a channel sees it in the list.
   Administrators get the unfiltered list. (Option C.)
2. **No name secrecy**: `forbidden` and `conflict` stay as they are;
   nothing becomes `not_found` to hide a channel's existence. (Option A.)
3. **`bridged` only while the bridge is `live`**, not `creating`; the pin
   rule of ADR 0009 (`creating` or `live`) is unchanged and separate.
   (Option A.)
4. and 5. **No `bridged` boolean.** `Channel` and `ChannelInfo` carry a
   required `capabilities` array of strings (empty allowed); a string
   present means the trait applies; `bridged` is the first value. Eric's
   principle: capability arrays are used in lieu of booleans so future
   traits need no protocol change; clients must ignore unknown capability
   strings. The server-level discovery capability's name is pending
   (section 5).
6. **`(no access)` is kept as built**: it describes the administrator's
   view, the write-only or admin-only holder's view, and the revocation
   window. (Option A.)
7. **`channels delete` prompts by name without the kind** when the list
   lacks the name, and lets the Bus answer. (Option B.)
8. **Recorded in ADR 0008** (a new "Decisions of 2026-10-25 (Eric)"
   section), with a note in ADR 0009 and "superseded" markers where
   Decision 94 is stated. (Option A.)
9. **The TUI follows an ACL change on the existing status tick** (about
   5 s). (Option A.)
10. **The bridged icon is drawn at every kind-icon site** through one
    helper. (Option A.)

## 5. Pending Eric

- **The discovery capability name.** `GET /v1/discover` `capabilities`
  gains one string so a client can tell a server that sends channel
  `capabilities` (and filters the list) from an older one. Proposed:
  `channel_capabilities`. Eric did not rule on the name; the plan uses it
  (Task 2) and changing it later is one constant, one test and two docs
  lines.

## 6. Tests and pinned counts

Stays as counted:

- `TestACLMatrix`: 26 sub-cases, 52 leaves per engine
  (`scripts/m3-exit-counts.sh`). The `list channels` row keeps `perm: ""`
  (both legs start with an ACL naming `pat` only) and changes content:
  refusal asserts the channel is absent from kim's list; success has pat
  add `{kim, [write]}` through `SetACL` and asserts the channel is present
  with its kind (proving any entry, not `read`, lists it). The row name
  stays in `section46Rows`.
- e2e: `TestSection24Sequence` 13 steps, `TestExitCodeTable` 11 cells
  (step 9's `channels list` is by `pat`, who holds entries everywhere
  there).
- `TestAuthEndToEnd` 11 leaves per engine; Milestone 8 counts; MCP tool
  count 25; the gate anchors of `scripts/check-gates-selftest.sh`.

Moves on purpose, each in its task:

- Store suite: new `TestVisibleChannels` on both engines (stored rows for
  the identity, `everyone`, `admins` for an administrator, the creator
  clause, a write-only row, a channel with no entry).
- `TestChannelListNeedsNoRead` becomes `TestChannelListFiltersByEntry` (a
  non-administrator sees the channels it holds any entry on, including a
  write-only one and one it created with `acl: []`; an administrator sees
  all; both engines) plus `TestChannelListCapabilities` (a `live` bridge
  row yields `["bridged"]`; `creating`, `archived` and no bridge yield
  `[]`; both engines, both transports since the bus suite runs under
  `inproc` too).
- `TestCapabilitiesComputed` gains the new name.
- `TestNoAccessChannelInRail` and `TestNoAccessChannelIsMarkedOnce` are
  rewritten: the non-administrator with no entry never sees `private`; an
  administrator fixture, and a write-only fixture, see `private (no
  access)`.
- `TestLoadIconsNerdfontWidthParity` and `TestIconSetsCoverTheSameNames`
  cover `bridged` by iterating the icon maps.
- MCP `list_channels` test asserts `capabilities`; CLI `channels list`
  test asserts the member and `channels delete` the prompt without a
  kind; a server handler test asserts `capabilities` on the wire (`[]`
  present, never null) and the filtered list over HTTP; a remote test
  asserts the mapping, including an unknown string passing through and an
  absent member normalizing to empty.

## 7. Documents that change

ADR 0008 (new decisions section; the Milestone 3 addendum's "Decision 94
in the types" and the Milestone 8 addendum's last sentence); ADR 0009 (a
`bridged` capability sentence under consequences); ADR 0004 (the
`VisibleChannels` query, one sentence in a new addendum); the design spec
sections 3.2 (channels row), 4.6.1 (Decision 94 and the "list channels"
table row) and Question 30; `docs/superpowers/plans/2026-10-21-milestone-8-tui.md:36`
(a "superseded" marker, history left in place); `docs/tui.md` ("Rail
markers", the `icons` section and any icon-name list);
`docs/ops/slack-bridge.md` (one sentence: who lists a bridged channel and
how it shows); `docs/ops/deploy.md:101` and `docs/ops/health.md:121` (the
capability list); `SKILL.md` (the four `list_channels` sentences; the m6
word check applies); the MCP tool descriptions; code comments that cite
Decision 94 (`internal/bus/types.go`, `internal/core/convert.go`,
`internal/core/channels/channels.go`, `internal/tui/view.go`); `CLAUDE.md`
(the agentview sentence gains "and `ListChannels` lists only the channels
the caller holds an ACL entry on, administrators excepted").
