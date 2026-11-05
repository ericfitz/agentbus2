# Slack scope canvas: design

Status: approved by Eric in chat, 2026-11-04 (decisions, architecture and glyphs); this written spec awaits his review. The decisions are recorded in ADR 0009's addendum "Decisions of 2026-11-04 (Eric): the scope canvas". Amended 2026-11-05 (section 2.1, "Decisions of 2026-11-05 (Eric)" in ADR 0009): the stalled state gained the reclaimed mark, after the implementation showed that section 4.3 as first written never held in practice.

## 1. Goal

ADR 0009 decision 1 deferred "memory and task channels later, one-way, informational". This delivers it: each live bridge's private Slack channel carries a channel canvas (the tab) that shows the scope's task lists and memory channels, readable and nicely formatted, and the bridge republishes it whenever a task or memory in the scope changes. The canvas is one-way: nothing a Slack user does to it reaches the bus.

## 2. Decisions (Eric, 2026-11-04)

1. **Visibility.** A canvas shows only the task and memory channels of the scope that every allowed human of that bridge may `Read` (the `access` check). A channel that one allowed human cannot read is left out entirely.
2. **Placement.** One canvas per live bridge, in that bridge's Slack channel, filtered for that bridge's humans. A scope with two bridges has two canvases.
3. **Read-only.** The bridge sets the canvas's access for its channel to `read`; changes go through agentmesh.
4. **Cadence.** Changes are coalesced: a change marks the scope dirty and the canvas is rebuilt after a window (default 30 s); an unchanged rendering is not sent.
5. **Enablement.** A server setting, on by default, for every live bridge; no wire or CLI change.
6. **Task layout.** The whole task hierarchy as a tree, not sorted by status; each task shows its status and its assigned agent, if any. Each root task is an h3 heading (Slack lets readers fold h3 sections, verified 2026-11-04), its subtasks a nested checklist under it. Completed tasks stay in place, checked.
7. **Glyphs.** Font glyphs, not emoji (verified in Slack 2026-11-04): `◌` pending, `▶` in progress, `✔` done, `⛔︎` blocked, `⚠︎` stalled or problem, `⚑` needs human. `⛔︎` and `⚠︎` carry U+FE0E (text presentation).
8. **Problems.** A task "has a problem" when it is blocked (an open blocker), stalled (abandoned: section 4.3), its owner's agent status carries a problem, or its owner's status tag is `needs-human`.
9. **Memories.** Full content of each live memory.
10. **Needs attention.** The canvas opens with a flat "Needs attention" section with four subsections, Blocked, Needs human, Stalled and Problem reported; a task appears in every subsection whose condition it matches.
11. **Title and navigation.** The canvas (its tab) is titled `Status`. No contents list: Slack builds the canvas's sidebar outline from its headings (verified 2026-11-04), so every list, memory channel and attention subsection is a heading.

### 2.1 Decisions (Eric, 2026-11-05): the reclaimed mark

Recorded in ADR 0009, "Decisions of 2026-11-05 (Eric)". Section 4.3 as first written said a stalled task "stays `⚠︎` until an agent's read or claim reclaims it". In practice it never did: the maintenance job `abandoned tasks` (`internal/core/maintenance/jobs.go`, every `maintenance_interval_seconds`, default 60) returns every abandoned task to `pending` with its owner and lease cleared (`internal/core/tasks/reclaim.go`, a revision of type `reclaimed` with no sender), and `tasks.List` and `tasks.Get` reclaim before they serve, so the TUI's own read reset the stall. A stalled task was `⚠︎` for at most one maintenance interval, then an unremarkable `◌`.

1. **A reclaimed task stays stalled until it is claimed.** The reclaim records who held the task and when: `reclaimAbandoned` writes `reclaimed_from` (the previous owner) and `reclaimed_at` (Unix ms) into the task document. Every later write copies the document forward, so the mark survives edits (subject, description, metadata, links, order). The mark clears when the task's status leaves `pending` (a claim, or an update to `in_progress` or `completed`) or when its owner is set to a non-empty value (an assignment; Eric, 2026-11-05), so a marked task never has an owner; a delete removes the task. A release (`task_release` by the owner, or an update to `pending` by anyone) is not a reclaim and sets no mark. `bus.Task` and `bus.TaskSummary` carry the two fields (`ReclaimedFrom`, `ReclaimedAt`, JSON `reclaimed_from` and `reclaimed_at`, omitted when unset), and the `Task` and `TaskSummary` schemas of `api/openapi.yaml` gain them (Eric, 2026-11-05; the protocol stays `1.0.0` before the first release). The `TestAccessRuleSites` count for `reclaimAbandoned` becomes `{"Owner": 2}` (Eric, 2026-11-05). Pre-release rule: the document format changes in place, no migration; a document without the fields parses as unmarked.
2. **The canvas shows a reclaimed task as stalled.** Glyph `⚠︎`, tree suffix `stalled: reclaimed from <owner> HH:MM UTC`, Needs attention → Stalled line `⚠︎ #id subject · reclaimed from <owner> HH:MM UTC · <list>`. The precedence of 4.2 is unchanged: blocked first, then needs human (which cannot hold, as a reclaimed task has no owner), then stalled.
3. **The TUI mirrors the canvas states** (ADR 0009, decision 2 of 2026-11-05; `docs/tui.md`): the task marks become `📥` pending (U+1F4E5; Eric changed this from `⬜` the same day), `⏱️` in progress, `✅` done, `⛔` blocked, `⚠️` stalled or problem, `🙋` needs human, with the canvas precedence. Decision 7 (font glyphs) stays a canvas rule; the TUI keeps emoji.

## 3. Verified Slack behavior (spike, 2026-11-04, workspace `efitz`, free plan)

| Call | Result |
|---|---|
| `conversations.canvases.create` (`channel_id`, `title`, markdown) | ok, returns `canvas_id` |
| same, a second time on the same channel | `free_team_canvas_tab_already_exists` (paid plans document `channel_canvas_already_exists`) |
| `canvases.access.set` `access_level: read`, `channel_ids: [channel]` on the channel canvas | ok |
| `canvases.edit`, one `replace` change with no `section_id` | ok; replaces the whole canvas |
| `conversations.info` (form parameters, not JSON) | `properties.canvas` is null; the canvas is in `properties.tabs[]` with `type: "canvas"`, `label` (the title at creation), `data.file_id` |
| `canvases.edit` `rename` (`title_content`) | ok, but the tab `label` keeps the creation title: the tab cannot be relied on to name the canvas |
| `canvases.sections.lookup` `criteria.section_types` (`h1`, `h2`, `h3`, `any_header`) | ok, section IDs in document order, no text |
| `canvases.sections.lookup` `criteria.contains_text` | ok, finds the section holding a given text |
| `canvases.delete` on the channel canvas | ok; the channel's tab is gone and a new one can be created |

Slack canvas markdown has no HTML, no color and no toggle element; h3 sections fold in the Slack client, and the client shows an outline sidebar built from the headings. Limits: 1 MiB of markdown per `document_content`; `canvases.edit` and `canvases.access.set` are tier 3, `conversations.canvases.create` tier 2.

## 4. Rendering (`internal/slackbridge/canvasrender.go`)

A pure function from a snapshot (section 5) to markdown, so it is tested with golden files and no Slack.

### 4.1 Document

```
# <scope> · tasks and memory
_Updated <YYYY-MM-DD HH:MM> UTC · read-only; changes go through agentmesh_

◌ pending · ▶ in progress · ✔ done · ⛔︎ blocked · ⚠︎ stalled or problem · ⚑ needs human

## Needs attention

### ⛔︎ Blocked
- ⛔︎ #11 Ship release · blocked by #7 · <task channel>

### ⚑ Needs human
- ⚑ #10 Pick the error format · claude-a · which style? · <task channel>

### ⚠︎ Stalled
- ⚠︎ #9 Fuzz the tokenizer · claude-b · lease expired 20:41 UTC · <task channel>
- ⚠︎ #12 Write docs · reclaimed from claude-d 19:02 UTC · <task channel>

### ⚠︎ Problem reported
- ⚠︎ #15 Run e2e · claude-c · docker daemon down · <task channel>

---

## Tasks: <task channel>
▶ 2 · ◌ 3 · ⛔︎ 1 · ⚠︎ 2 · ⚑ 1 · ✔ 12

### ▶ #7 Wire the parser · claude-a
- [x] ✔ #8 Define grammar · claude-a
- [ ] ⚠︎ #9 Fuzz the tokenizer · claude-b · stalled: lease expired 20:41 UTC
  - [ ] ◌ #13 Seed corpus
  - [x] ✔ #14 Corpus minimizer · claude-b
- [ ] ⚑ #10 Pick the error format · claude-a · needs human: which style?

### ⛔︎ #11 Ship release · blocked by #7
- [ ] ⚠︎ #12 Write docs · stalled: reclaimed from claude-d 19:02 UTC

---

## Memory: <memory channel>

### Postgres needs TCP keepalive
_fact · area:db · edited 2026-11-04 by claude-a_

<content>
```

- The canvas title (and tab label) is `Status`; the h1 names the scope.
- The italic line under the h1 is the bridge's marker (section 6.3); it always contains the exact text `changes go through agentmesh`.
- Needs attention first (section 4.5), then task channels, then memory channels, each group by channel name; a `---` divider between the groups. A task list with no live task shows `_No tasks._`; a memory channel with none shows `_No memories._`. When no channel passes the visibility filter, the body after the legend is `_Nothing in this scope is visible to every member of this channel._`
- The "Updated" time is the snapshot time of the rendering that was published. It is left out of the hash (section 6.3), so a rebuild with unchanged content publishes nothing and the canvas keeps showing the time of the last real change.
- Names, subjects and owner names are escaped so that `*`, `_`, `~`, `` ` ``, `#` at line start, `[`, `]`, `<`, `>` and `|` render literally; line breaks in a subject become spaces.

### 4.2 Tasks

- Order: depth-first tree order with siblings by rank (the order `tasks.List` returns). Root tasks (no parent, or a parent that is no longer live) are h3 headings; each root's descendants are a checklist nested by depth under it. Nesting is not capped.
- A task line is `<glyph> #<id> <subject>`, then ` · <owner>` when the task has one, then the problem suffix. A checklist item is `- [x]` when the task is completed, otherwise `- [ ]`; an h3 root has no checkbox and its glyph carries the state.
- The glyph, first match wins:

| Condition (task not completed) | Glyph | Suffix |
|---|---|---|
| open blockers (`OpenBlockers` non-empty) | `⛔︎` | `blocked by #a, #b` |
| owner's status tag is exactly `needs-human` | `⚑` | `needs human: <status text>` |
| stalled (section 4.3) | `⚠︎` | `stalled: lease expired HH:MM UTC`, `stalled: owner offline` or `stalled: reclaimed from <owner> HH:MM UTC` |
| owner's status has a problem | `⚠︎` | `<problem>` |
| `in_progress` | `▶` | none |
| `pending` | `◌` | none |

  A completed task is `✔` with no suffix, whatever its owner's status. The owner's status applies only to tasks the owner holds; an owner with no status entry, or an offline one (offline is derived at read, ADR 0014), contributes nothing.
- The count line under each list heading counts every live task by its glyph, in the order `▶ ◌ ⛔︎ ⚠︎ ⚑ ✔`, leaving out zero counts.
- Status and problem texts are cut at 120 characters with `…`.

### 4.3 Stalled

A task is stalled in either of two states (Eric, 2026-11-05, section 2.1):

- **Abandoned at the snapshot time:** the tasks core's `abandonedSet` rule holds: `in_progress` with an expired lease, or with an owner that has no live session. The suffix says `lease expired HH:MM UTC` when the lease has expired, otherwise `owner offline`. The canvas read never reclaims (section 5), so this state shows until something reclaims the task: the maintenance job `abandoned tasks` (every `maintenance_interval_seconds`, default 60), a `tasks.List` or `tasks.Get` by any reader, or an update of the task. In practice this state lasts at most about one maintenance interval.
- **Reclaimed:** the task is `pending` and carries the reclaimed mark (`reclaimed_from`, `reclaimed_at`) the reclaim wrote. The suffix says `reclaimed from <owner> HH:MM UTC` (the hour of `reclaimed_at`). The mark stays until the task's status leaves `pending` (a claim, or an update to `in_progress` or `completed`) or its owner is set (an assignment), so the task stays `⚠︎` for every reader until an agent takes it or someone hands it to one. A marked task never has an owner, so the needs-human and problem rows of 4.2 cannot apply to it; an open blocker still wins (`⛔︎`), and the Stalled subsection of 4.5 lists it all the same.

A release (`task_release` by the owner, or an update to `pending`) is not a reclaim: the task becomes an ordinary `◌` with no mark. The snapshot (section 5) reports both states (the abandonment flag and the mark separately); the renderer's stalled view is their union.

### 4.4 Memories

- Live memories only (the latest revision of each, deleted ones left out), newest edit first.
- Each memory is an h3: its subject, or when it has none the first line of its content cut at 80 characters. Under it an italic line of the type, the tags and `edited <YYYY-MM-DD> by <sender>`, omitting empty parts. Then the content as written, with one change: a content line that starts with `#` is prefixed with a backslash, so a memory never breaks the canvas's heading structure.

### 4.5 Needs attention

- An h2 `Needs attention`, then up to four h3 subsections in this order, each a flat bulleted list (no checkboxes, no nesting) of the matching non-completed tasks of the visible task lists:

| Subsection | Condition | Line |
|---|---|---|
| `⛔︎ Blocked` | open blockers | `⛔︎ #id subject · blocked by #a, #b · <list>` |
| `⚑ Needs human` | owner's status tag is exactly `needs-human` | `⚑ #id subject · owner · <status text> · <list>` |
| `⚠︎ Stalled` | stalled (section 4.3) | abandoned: `⚠︎ #id subject · owner · lease expired HH:MM UTC` or `owner offline` ` · <list>`; reclaimed: `⚠︎ #id subject · reclaimed from <owner> HH:MM UTC · <list>` (no owner segment: the task has none) |
| `⚠︎ Problem reported` | owner's status has a problem | `⚠︎ #id subject · owner · <problem> · <list>` |

- A task appears in every subsection whose condition it matches; the tree (4.2) still shows one glyph by precedence.
- Within a subsection: by list name, then the list's tree order.
- An empty subsection is left out. When all four are empty the section is the h2 and `_Nothing needs attention._`
- A `---` divider follows the section.

### 4.6 Size

When the rendering exceeds 1 MiB minus a 4 KiB margin, the oldest memories (by last edit, across all memory channels) are dropped one at a time until it fits, and each affected memory channel ends with `_<n> older memories not shown; search agentmesh for them._`. Tasks are never dropped; if the tasks alone exceed the limit, the canvas shows the header and `_The task lists are too large for a canvas._` and the server logs a warning naming the scope.

## 5. Snapshot (the read)

For one live bridge, in one `Store.Read`:

1. The allowed humans: `Access.Holders(Channel{bridged channel}, Post)`, the set `create.go` and the inbound trust check already use.
2. The candidate channels: the scope's channels of kind tasks or memory.
3. The visible channels: a candidate is kept when every allowed human is in `Access.Holders(Channel{candidate}, Read)`. Zero allowed humans keeps nothing. The decision stays in `internal/core/access` (ADR 0011): the bridge compares holder sets and reads no `Admin`, `Creator`, `Owner`, `Scope` or `Kind` selector of an identity or memory, and adds no entry to the `TestAccessRuleSites` allowlist. The tasks core adds exactly one (`internal/core/tasks/snapshot.go Snapshot {"Owner": 1}`: a data copy of the owner into the snapshot's assignee, Eric's decision 2026-11-04, ADR 0009 addendum item 13), and the scope name is passed to the snapshot as a parameter.
4. For each visible task channel, the live tasks in tree order with `OpenBlockers`, the abandoned set at the snapshot time and each task's reclaimed mark (`ReclaimedFrom`, `ReclaimedAt`, as stored), read without reclaiming and without recording a read mark: the canvas is not a reader and must not keep a list alive (`channels.last_read_at`) or write to it. The tasks core gains a read-only snapshot function for this, sharing `loadTasks` and `abandonedSet` with `List`.
5. For each visible memory channel, its live memories.
6. The agent statuses of the owners of the non-completed tasks (`internal/core/statuses`, offline derived at read).

The snapshot needs no principal: it is the bridge's read under the visibility rule above, like the bridge's existing reads of its own channels.

## 6. Publishing (`internal/slackbridge/canvas.go`)

### 6.1 Where it runs

In the bridge runner session (`startSession`), so only the replica holding the `slack-bridge` Store lock publishes, as a fifth goroutine. It stops with the session.

### 6.2 When

- A doorbell hint for a channel of kind tasks or memory whose scope has a live bridge marks that scope dirty. The runner keeps a channel-to-scope map of those channels, refreshed on each lifecycle pass and on a hint for an unknown channel.
- Every Socket Mode (re)connect and every lifecycle pass (60 s) mark every scope with a live bridge dirty. The lifecycle pass catches what makes no hint: a lease expiring, a session ending, an ACL change, an agent status change.
- A dirty scope is rebuilt when its window ends: `slack_canvas_coalesce_seconds` after it was first marked; marks during the window join the same rebuild. Each of the scope's live bridges is rendered and published in turn.

### 6.3 How

Per bridge, held in memory (a restart costs one republish per bridge, which a no-op `replace` makes harmless):

- `canvasID`: found or created on first use:
  1. Look in `conversations.info` `properties.tabs` for `type: "canvas"` tabs. A tab is the bridge's when `canvases.sections.lookup` with `criteria.contains_text` set to the marker text `changes go through agentmesh` finds a section in it; that canvas is adopted. The tab label is not used: a rename does not change it (section 3), and a human's canvas never contains the marker.
  2. No canvas tab: `conversations.canvases.create` with the title `Status` and the rendering, then `canvases.access.set` `read` for the bridge's channel.
  3. Create answers `channel_canvas_already_exists` or `free_team_canvas_tab_already_exists`: look again (step 1). A canvas tab without the marker belongs to someone else, so the canvas is disabled for the bridge (6.4) and never overwritten.
- `hash`: SHA-256 of the rendering without the "Updated" line. Equal to the last published hash: no call. Otherwise one `canvases.edit` with a single `replace` and no `section_id`, then the hash is stored.
- The access call is made after every create and after an adoption, so a canvas whose access a human widened returns to read-only on the next restart. It is idempotent.

### 6.4 Errors

| Slack answer | Effect |
|---|---|
| `team_tier_cannot_create_channel_canvases`, `canvas_disabled_user_team`, a foreign canvas tab (6.3) | warning once per bridge naming the Slack channel; the canvas is disabled for that bridge until the server restarts |
| `missing_scope` | warning once per bridge naming `canvases:read` and `canvases:write`; disabled until restart |
| `canvas_not_found`, `channel_not_found` on edit | forget `canvasID` and `hash`; the next rebuild finds or creates it again |
| `is_archived`, `not_in_channel` | the bridge is marked `archived` by the existing rule for a channel archived by hand in Slack (`docs/ops/slack-bridge.md`), the same as when a post finds it archived; the canvas state is dropped |
| rate limit | wait the `Retry-After` delay, then rebuild |
| `canvas_editing_locked`, server errors, transport errors | keep the scope dirty; rebuilt on the next window |
| a credential code (`invalid_auth`, `account_inactive`, `token_revoked`, ...) | logged; the socket's own handling decides |

A canvas failure never delays or blocks message bridging: the publisher has its own goroutine and its Slack calls carry their own context timeouts.

### 6.5 Slack port (`internal/slackapi`)

`Client` gains five methods, each with an `Error`/`RateLimitedError` mapping like the existing ones:

- `CreateChannelCanvas(ctx, channelID, title, markdown string) (canvasID string, err error)`
- `ChannelCanvases(ctx, channelID string) ([]CanvasTab, error)`: the `type: "canvas"` tabs of `conversations.info` (`CanvasTab{ID, Label string}`)
- `CanvasContains(ctx, canvasID, text string) (bool, error)`: `canvases.sections.lookup` with `criteria.contains_text`; true when a section is found
- `ReplaceCanvas(ctx, canvasID, markdown string) error`
- `SetCanvasChannelAccess(ctx, canvasID, channelID, level string) error`: `level` is `read` or `write`; any other value is an error before a call is made

`slackgo` implements them with slack-go v0.34.0 (Eric's decision 2026-11-04, ADR 0009 addendum item 12: `CreateChannelCanvas`, `GetConversationInfoContext` with `Tab.Data.FileID` as the canvas ID of a channel's tab, `LookupCanvasSections`, `EditCanvas`, `SetCanvasAccess`); `slackfake` implements them in memory, including the free-tier second-create code, a foreign tab without the marker, and a tab label that a rename does not change. The Slack import rules are unchanged.

## 7. Settings (`internal/server/config`)

| Setting | Default | Range |
|---|---|---|
| `slack_canvas_enabled` | `true` | `true` or `false`; any other value is refused at start-up |
| `slack_canvas_coalesce_seconds` | `30` | 5 to 600 |

Both apply only when the Slack bridge is configured; set without the token files they are accepted and ignored, like the other `slack_*` settings. `config.Settings()` lists them and `docs/ops/configuration.md` and `docs/ops/slack-bridge.md` document them. The Slack app needs the bot scopes `canvases:read` and `canvases:write` in addition to today's; `docs/ops/slack-bridge.md` adds them to its list.

## 8. Lifecycle

- Publishing starts with the first rebuild after a bridge goes `live` and stops when it is `archived` or removed: an archived or removed bridge is skipped and its in-memory state dropped. The canvas stays in the archived Slack channel with its last content.
- Removing a bridge leaves the canvas in place in the archived channel; nothing deletes a canvas.
- Two replicas: only the lock holder publishes. A replica that takes the lock over finds the canvas by its marker (6.3 step 1) and never creates a second one.
- A second server start finds the canvas by its marker and republishes once.
- A human who deletes the canvas: the next edit answers `canvas_not_found` and the next rebuild creates a new one.

## 9. Testing

- **Rendering:** golden files under `internal/slackbridge/testdata/canvas/` for: Needs attention with all four subsections, a task in two of them, two lists, and the empty case; a tree four levels deep with every glyph and suffix; completed subtrees; a root whose parent was deleted; empty list; no visible channels; memories with and without a subject, with a `#` line in the content; escaping of every listed character; the size cap dropping memories; the tasks-too-large case.
- **Snapshot:** on both engines (the Bus suite's store fixtures): the visibility rule (a channel readable by one of two humans is left out; `everyone` read counts; zero humans keeps nothing); a stalled task is reported and **not** reclaimed, and `last_read_at` is unchanged after the snapshot; owner status and `needs-human` mapping; offline owners contribute nothing.
- **Reclaimed mark (2026-11-05):** on both engines: the `abandoned tasks` job writes `reclaimed_from` and `reclaimed_at` and a second run writes nothing more; `Snapshot`, `Get` and `List` report the mark; an edit that keeps `pending` and the owner empty carries it forward, a claim, a status change or an owner assignment clears it, a release sets none; the bridge's canvas, with the maintenance job run against a lease-expired task, shows `stalled: reclaimed from <owner>` (the reproduction of the bug section 2.1 describes: it fails before the change, because the job's reclaim left an unmarked `◌`). Goldens: a reclaimed task in the tree and in Needs attention → Stalled, and a reclaimed task with an open blocker (`⛔︎` in the tree, listed under both Blocked and Stalled).
- **Publisher (slackfake):** first publish creates a canvas titled `Status`, sets access and stores the hash; an unchanged rendering makes no call; a change within the window makes one call; a restart adopts the tab holding the marker even when its label differs; the free-tier second-create code adopts the marked tab; a foreign tab (no marker) disables and never edits; `canvas_not_found` recreates; rate limit waits; `missing_scope` warns once; disabled setting makes no canvas call; only the lock holder publishes (`TestReplicas*` style); an archived bridge is skipped.
- **Config:** defaults, ranges, an unknown boolean value refused, environment and flag precedence.
- **Gates:** `make verify` (both engines), `TestAccessRuleSites` unchanged, the Slack import rules unchanged, `ci.yml` and `windows.yml` green.
- **Live check (Eric's workspace, local stack):** scope `canvas1`, bridge `canvas1-chat` (the spike canvas was deleted, so its Slack channel has no tab): create a task tree four levels deep with a blocked, a stalled, a needs-human and a problem-reported task, and two memories; check that a tab titled `Status` appears within the window, Needs attention lists each task where expected, the outline sidebar shows every heading, h3 sections fold, nesting renders four levels, and the canvas is read-only for Eric; restart the server and check that no second canvas appears.

## 10. Out of scope

- Anything from Slack to the bus through the canvas.
- Per-bridge enablement and any wire, CLI or MCP change.
- Unbridged scopes (no Slack channel to hold a canvas).
- A canvas for chat history.

## 11. Side finding (not part of this work)

On 2026-11-04 the server, holding refused Slack tokens, retried the Socket Mode connection every second indefinitely (`slack bridge: socket failed ... invalid_auth`), because the socket loop sleeps `RetryBase` (1 s) after every failure. A credential refusal should back off. To be filed as its own issue.
