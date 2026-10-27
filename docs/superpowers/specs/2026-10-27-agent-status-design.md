# Agent status and history: design brief

## 1. Status

- Draft of 2026-10-27, written by an agent.
- Nothing in it is a decision until Eric marks it so. Where it says "Recommended" it is a proposal.
- **Eric reviewed this brief on 2026-10-27:** all 15 of its decisions are **decided** and none is open. Decisions 1 to 4 and 6 to 15 are ratified as recommended; decision 5 (the TUI layout) is Layout 2, the agents board, ratified as written, including the status-bar attention count and an icon-only tag on each session row.
- **Eric's decision 2026-10-27**: "Protocol version will be 1.0.0, end of discussion." Any addition in this brief ships in protocol 1.0.0, and `info.version` and `version.Protocol` stay `1.0.0` before the first release.
- Issues: ericfitz/agentbus2#10, "feat: Status & History for agents (aligned with agentbus#43)", and its v1 counterpart ericfitz/agentbus#43, "feat: Status & History for agents". Both are open, unlabeled beyond `enhancement`, with no comments. #10 was left out of the implementation queue because it needs a TUI design (section 4.10).
- Alignment: #10 says to settle the design once, in whichever repository takes it up first, and record the result in both. This brief is written for agentbus2. Every answer in section 5 that is not v2-specific (the tag set, N, offline, push or pull, the protocol text) should be copied into the v1 ADR as well.

Eric's direction in agentbus#43 is taken as given, not as an option:

1. Status is versioned. A new status from an agent replaces its current one, and the earlier versions become its history. Reuse the memory revision pattern (latest revision wins, earlier revisions kept).
2. Each agent has one status line: what it is doing now, plus an optional problem.
3. Every status carries exactly one tag from a fixed set (examples: `offline`, `idle`, `needs-human`, `blocked`, `planning`, `implementing`, `waiting`). The TUI maps each tag to an icon.
4. History is an agent's last N statuses, shown in chronological order in the TUI.

## 2. Problem and scope

Eric runs many agents at once and wants to see what each is doing without opening each one: one line per agent, with an icon for its state, and on demand the agent's recent statuses.

In scope:

- Setting a status (one line, optional problem, one tag) through MCP, the CLI and the Bus, and rejecting an unknown or missing tag.
- Keeping the previous statuses as history, bounded at N.
- Reading every visible agent's current status, and one agent's history.
- The TUI views for both, with tag icons.
- The wire addition (`api/openapi.yaml`, in protocol 1.0.0 with no version bump: Eric's decision 2026-10-27), the access rule, the protocol text agents follow, and what a session close or lapse does to a status.
- An ADR here, and the matching ADR in agentbus.

Out of scope:

- Pushing status changes to agents, which waits on issue #13 (MCP events) if it is wanted at all (section 4.9).
- A human setting or clearing an agent's status from the TUI (raised as a decision, recommended "not now").
- Status for human identities (a human's TUI session is not an agent; section 4.5).
- Any change to park (Decision 18) beyond protocol text, unless Eric picks option B in 4.7.3.
- v1's implementation. Only the shared decisions cross over.

Acceptance criteria, restated from #10:

1. An agent can set its status (one line, an optional problem and one tag from the fixed set) through MCP. A status with an unknown or missing tag is rejected.
2. A new status supersedes the agent's previous one, and the previous one is kept as history.
3. The TUI shows every agent's current status with an icon for its tag.
4. The TUI can show an agent's last N statuses in chronological order.
5. The full tag set and N are defined and documented in an ADR here and in agentbus.

The Definition of Done adds: an unknown tag, an empty line and a bus-only tag are each rejected with a test (rule B); the second-run, overlap and failed-run behavior of every write is stated and tested (rule D).

## 3. What exists today

### 3.1 The memory revision pattern

- A memory is a message on a channel of kind `memory`. The first revision is an ordinary append followed by `SetMemory(seq, seq, 1)` and an access-time touch (`internal/core/append.go:63-103`).
- `memories.Service.Edit` tombstones the live revision, drops its embedding, appends the next revision through the ordinary path, then links it with `SetMemory(seq, memoryID, rev)` (`internal/core/memories/memories.go:90-215`). It checks `Write` on the memory's channel, honors `if_revision`, replays an `idempotency_key` through `WithReceipt` (Decision 46), and charges the rate limiter (`memories.go:117-213`).
- `Revisions` returns every revision, tombstoned ones included, under `Read` on the channel (`memories.go:56-80`). The Store side is `Memories.LiveMemory`, `MemoryRevisions` (by revision), `SetMemory` and `TouchMemoryAccess` (`internal/store/memories.go:8-17`).
- Every revision is a full message, so it is embedded for semantic search, indexed for text search, charged against the envelope size, and publishes a notifier hint (`memories.go:171-212`).
- Maintenance acts on memory rows twice:
  - `tombstonePurge` deletes tombstoned rows older than `tombstone_min_hours` (default 72, minimum 72; `internal/core/maintenance/jobs.go:142-161`, `internal/core/config.go:87,120`). Old revisions therefore vanish after 72 hours, whatever their count.
  - `memoryExpiry` tombstones a live memory not read for `memory_expiry_hours` (default 8760; `jobs.go:276-312`, `config.go:89,133`).
- Budget eviction deletes ordinary messages only (`internal/core/maintenance/budget.go:23-64`). Live memories and protected tombstones are never evicted automatically, which the capacity notice text says (`budget.go:93-95`).

### 3.2 Sessions, identities and the `others` list

- Several sessions may share one identity (Decision 15). Display names are `<identity>`, `<identity>-2`, ..., or `<identity>/<hint>` for a hinted session (Decision 39; `internal/core/sessions/service.go:28-50`). A subagent registers with a hint, so it is a second session of its parent's identity.
- `sessions.Service.Identities` lists only the identities with at least one live session, filtered through `Access.Identities`, each with its live sessions (`internal/core/sessions/identities.go:12-49`). An identity with no live session is absent.
- MCP `register` returns `others`, the result of `Bus.Identities` (`internal/mcpserver/server.go:232-241, 873-878`). MCP `discover` returns the same list (`server.go:683-690`). Both use `identityOut` (name, admin, kind, sessions; `server.go:209-214, 341-351`).
- `Close` closes the caller's own session and sets its lease to now, so retention removes it (`internal/core/sessions/close.go:12-44`). Only `close_session` closes a session on purpose: an MCP server exit, clean or not, leaves the session to lapse when its lease runs out (`internal/mcpserver/sessions.go:31-35`). The TUI closes its session on quit (`internal/tui/client.go:308`). In practice an agent's session lapses far more often than it closes.
- The lease is `session_lease_seconds` (default 90) and a lapsed row is kept `session_retention_hours` (default 72), then deleted by the `lapsed sessions` job (`config.go:79-81`, `jobs.go:16-29`).
- `RetireIdentity` deletes the identity row, and sessions, keys and subscriptions cascade (`internal/store/scopes.go:60-65`; `sessions.identity ... REFERENCES identities(name) ON DELETE CASCADE`, `internal/store/sqlite/schema/0001_initial.sql:84-99`). `ResetData` lists every table it clears (`internal/store/meta.go:18-29`).

### 3.3 Park, the Stop hook and memory delivery

- Decision 18 (Eric, 2026-10-22; `docs/superpowers/specs/2026-10-21-gate-0-decisions.md:232-240`): park is a flag in the client's session state file. The server never sees it. `register` and every MCP tool but `receive` unpark.
- Decision 20 (`gate-0-decisions.md:244-249`): agents never receive memory channels. It is enforced as the `Deliver` action (`internal/core/access/decide.go:338-345`).
- The Stop hook peeks for pending messages and blocks the stop with a reason when there are any. It is silent while parked (`internal/cli/stophook.go:107-109`), works within a 500 ms budget (`stophook.go:20`), and its only write is the resume of a lapsed session (`stophook.go:166-171`).
- Decision 17 and the wake-park plan aim to cut model turns that carry no message (`gate-0-decisions.md:219-231`).

### 3.4 Access (ADR 0011)

- `decide` is the one rule (`internal/core/access/decide.go:153-189`). The two clauses that matter here are:
  - `Session/Use` holds when the session's identity is the principal (`decide.go:171-178`).
  - `Identity/See` is the scope gate, which admits every human and every administrator (`decide.go:179-185`, `inScope` at `decide.go:111-114`).
- `Checker.Identities` is the set form of `See` (`internal/core/access/checker.go:218-231`). The actions are a closed list (`internal/core/access/objects.go:8-23`).
- `TestAccessRuleSites` fails when code outside `access` reads `Admin`, `Scope` or an identity `Kind`, against an exact-count allowlist (`internal/core/access/sites_test.go`; ADR 0011 item 8). `scripts/check-gates-selftest.sh` block 29 holds one mutation per clause.
- A scoped agent's channel must be named `<scope>-<rest>` (`decideChannelName`, `decide.go:196-208`).

### 3.5 Wire, MCP and CLI pins

- `api/openapi.yaml` `info.version` is `1.0.0` (`api/openapi.yaml:4`), equal to `version.Protocol` (`internal/version/version.go:12`). Before the first release an addition does not bump it (Eric's decision 2026-10-27; ADR 0010 covers the rule after the release).
- There are 52 operations: 44 protected and 8 open. They are pinned in `internal/server/cmd/cmd_test.go:432-433` and `internal/gen/openapi31_test.go:173-174`, stated in ADR 0001's addendum (`docs/adr/0001-transport-and-api.md:190`) and ADR 0010 decision 12, and repeated in CLAUDE.md.
- `GET /v1/identities` (`listIdentities`) returns `Identity` items (`api/openapi.yaml:220-234`).
- There are 25 MCP tools. They are pinned by `TestToolListIsExact` (a name table plus `len(want) != 25`, `internal/mcpserver/server_test.go:234-275`) and `run_test.go:111`, and stated in CLAUDE.md. The m6 word check scans `internal/mcpserver/inputs.go`, `server.go`, the skill and the two hooks for `dm/|-filter|@<|memory/|tasks/|general/` (`scripts/m6-exit-checks.sh:120-151`).
- Name collisions:
  - `agentbus2 status` already exists and is the administrator's `Bus.Status` (`internal/cli/cli.go:48`, `internal/bus/admin.go:7-31`).
  - Tasks have a `status` field (`pending`, `in_progress`, `completed`).
  - "Status" needs a qualified name in code, on the wire and in the CLI.
- The Bus is six interfaces (`internal/bus/bus.go:3-13`). `Sessions` holds `Identities` (`internal/bus/sessions.go:129-141`).

### 3.6 The TUI

- The TUI imports only `internal/bus` and `internal/version` of this module (allowlist in `scripts/m8-exit-checks.sh`).
- The rail has five sections. Each comes from a channel `Kind`, except `tags` (synthetic) and `sessions` (`docs/tui.md:72-91`; `internal/tui/view.go:262-269`).
- The `sessions` section lists live sessions from `Identities`, then display names seen live within `idleSessionTTL` (1 hour) (`view.go:447-478`; `internal/tui/model.go:47-49, 1286-1297`).
- Selecting a session row shows its details in the main pane without a bus call (`view.go:614-641`, TUI decision 14). There is no inbox or message view on a session (ADR 0006; `TestNoInboxView` is counted by `scripts/m8-exit-checks.sh`).
- The status round runs every 5 seconds: `ListChannels`, `Identities`, `TagSubscriptions`, then `Status` (`model.go:1365-1395`). A status read added to this round needs no notifier work.
- `,` and `.` already step through the versions of the cursor memory (`docs/tui.md:112`). The health overlay is a centered bordered box opened by `h` (`view.go:861-882`, `docs/tui.md:137-141`).
- Icons are vars per set (emoji, nerdfont, custom with `icon_map`). `idle` already exists (U+F04B2 in nerdfont, the sleeping sign in emoji; `internal/tui/icons.go:12-24`, `docs/tui.md:195-216`). Emoji with `Emoji_Presentation=No` need the Terminal.app padding variant (`icons.go:36-58`).
- Taken keys: `tab`, `i`, `enter`, `esc`, arrows, `pgup`, `pgdn`, `g`, `G`, `r`, `,`, `.`, `c`, `s`, `t`, `u`, `a`, `space`, `d`, `/`, `h`, `?`, `q`, `<`, `>`, `home`, and `o` inside the health overlay (`internal/tui/help.go:35-58`, `model.go`). `w`, `b`, `p`, `x` and the capitals are free.

### 3.7 Store and pre-release rule

- Schema version 1 is the whole schema, one `0001_initial.sql` per engine (`internal/store/sqlite/schema/0001_initial.sql`, `internal/store/postgres/schema/0001_initial.sql`). Until the first release a schema change edits these files in place. There is no `0002` and no migration (CLAUDE.md, ADR 0004 addendum).
- `Store.Tx` and `Store.Append` run serializable transactions, and the Store retries a serialization failure (`internal/store/sqlstore/store.go:50-93`). So a `max(revision)+1` insert is safe on both engines.

## 4. Options

### 4.1 Storage

**A. Memory rows on a memory-kind channel per agent** (the issue's `status/<name>` example).

Each agent gets a channel holding one memory. `set_status` is `edit_memory` on it, and history is `Revisions`.

- It reuses the code literally. That is the only point in its favor.
- `status/<name>` uses a name prefix as a kind, which v2 dropped (ADR 0008, `docs/tui.md` "Kinds, not prefixes"). The name also breaks for scoped agents, whose channels must be named `<scope>-...` (`decide.go:196-208`).
- `tombstonePurge` deletes old revisions after 72 hours (`jobs.go:142-161`), so "the last N" becomes "the last N that are younger than 72 hours". `memoryExpiry` would also tombstone a quiet agent's status. Both jobs would need a status exclusion, as tasks have one.
- Each status would be embedded and indexed, so status lines pollute `search`.
- The channel would need an ACL that only its agent may write. CLAUDE.md says there is no implicit creator entry, so first use would need a channel-creation rule plus an ACL rule: two new access rules.
- Status rows would be non-evictable protected rows under the SQLite budget.
- Decision 20 makes it TUI-only by construction.
- Every status channel appears in the `memories` rail section unless the TUI filters by name, and the TUI is told never to read names for sections.

**B. A dedicated `agent_statuses` table that copies the pattern, not the rows** (Recommended).

One row per revision: `identity` (FK to `identities`, `ON DELETE CASCADE`), `revision`, `tag`, `text`, `problem`, `session_id` and `display_name` (plain text, no FK, so history outlives the session row), and `set_at`. The primary key is `(identity, revision)`. The current status is the highest revision; earlier rows are history. This is "latest revision wins, earlier revisions kept" without tombstones.

- Outside search, embeddings, purge, expiry and budget eviction by construction.
- Identity retirement and deletion clean it up for free, through the cascade (as `sessions` does, `0001_initial.sql:86`).
- One statement prunes beyond N (section 4.8).
- New Store methods go into the Store suite on both engines.
- Cost: a table on both engines, Store methods, one core aggregate and the wire. `ResetData` gains the table (`meta.go:18-29`).

**C. A new channel kind `status`, one channel for all statuses, one memory per agent.**

- It avoids the per-agent channel and the name prefix.
- It keeps every other problem of A: purge, expiry, search and ACL.
- It adds a kind that the rail rule turns into a section (`docs/tui.md:86`), and ADR 0008's kind list changes.
- A shared channel's `Write` lets any writer edit any agent's memory. The "own status only" rule would have to be a new memory-level check, which v2 does not have (no owner guard).

Option A collides with the channel-visibility and scope-naming decisions (ADR 0008, ADR 0011). Option C changes ADR 0008 and the rail-section rule. Both are architectural changes that need Eric's approval. Option B changes no existing decision. It only adds a table, which is allowed in place before the release.

### 4.2 What "an agent" is: the key

**A. Key on the identity** (Recommended, per Eric's "each agent has one status line").

- The row records the `session_id` and `display_name` that set it, so the TUI can say "set by agentbus2/reviewer".
- A hinted subagent shares its parent's identity (Decision 15). If both set statuses, they overwrite each other. The protocol text tells hinted sessions not to set status (section 4.7.4). The server does not enforce this, because the hint is not an access fact.
- History survives session churn and restarts. Offline is a clean per-identity fact (section 4.4).

**B. Key on the display name** (identity plus display name).

- A subagent gets its own line, matching the TUI's per-session rows.
- Display names are reused (`identity-2` frees on close), and every hinted subagent leaves a key behind, so stale keys accumulate and need a reaper job.

**C. Key on the session id.**

- Exact, but history dies with the session row after 72 hours.
- Each restart that does not resume starts an empty history.

### 4.3 The tag set and validation

Proposed fixed set. Agents may set the first seven; `offline` belongs to the bus.

| Tag | Meaning | Emoji (proposal) | Nerd Font (proposal) |
| --- | --- | --- | --- |
| `idle` | nothing in hand; parked or between tasks | the existing `idle` icon | existing U+F04B2 |
| `planning` | reading, designing, writing a plan | U+1F5FA world map | U+F0CA list |
| `implementing` | changing code or docs | U+1F6E0 hammer and wrench | U+F121 code |
| `reviewing` | reviewing someone's work or its own | U+1F50D magnifier | U+F002 search |
| `waiting` | waiting on another agent, CI or a long run, and expects it to resolve | U+23F3 hourglass | U+F252 hourglass |
| `blocked` | cannot proceed; needs something that will not come by itself | U+26D4 no entry | U+F05E ban |
| `needs-human` | only the human can unblock it | U+1F64B raised hand | U+F256 hand |
| `offline` | no live session (set by the bus, section 4.4) | U+26AB black circle | U+F111 circle |

Codepoints are illustrative. Each needs the width check that the gear and the stopwatch needed (`icons.go:36-58`). Prefer `Emoji_Presentation=Yes` emoji so that no Terminal.app variant is needed.

Validation (Definition of Done B):

- `tag` is required and must be one of the seven settable tags. A missing tag, an unknown tag and `offline` are each `validation`, each with a test.
- `text` is required, one line, at most 200 characters after trimming, the `NormalizeSubject` shape (`internal/core/rules/subject.go:10-27`).
- `problem` is optional, one line, at most 200 characters.
- Optional variant for Eric: require `problem` with `blocked` and `needs-human`. The recommendation is no, because Eric said "optional"; the protocol text asks for one instead.
- `idempotency_key` replays through `WithReceipt`. The write is charged to the rate limiter like `Edit` (`memories.go:117-213`).
- A status equal to the current one (same tag, text and problem) still adds a revision. The alternative is a no-op. The recommendation is to add the revision, which keeps "every call is one revision" simple to test. Eric may prefer the no-op so that agents re-reporting the same status do not flood the history.

### 4.4 Offline, and what a session close or lapse does

The common case is a lapse, not a close: an MCP server exit leaves the session to lapse after 90 seconds (section 3.2).

**A. Derived at read** (Recommended).

- A read reports `offline` when the identity has no live session (`Core.LiveSessions`, `internal/core/sessions.go:15-20`). The stored last status is kept and shown as "last said".
- Nothing is written at close or lapse, so a lapse without a close needs nothing, and there is no write for replicas to race.
- `offline` is a display state that agents cannot set.
- The read carries `live: false`. The `since` time is the last status's `set_at`, because the session row may already be gone.
- Cost: history does not record when the agent went away.

**B. Stored by the bus.**

- `Close` appends an `offline` revision in its own transaction. A new maintenance job appends one for each identity whose sessions have all lapsed and whose latest revision is not `offline`. The read also derives offline, to cover the gap until the next tick.
- History shows the time an agent went away.
- Costs: a job with an idempotency rule ("only when the latest revision is not offline"), interaction with the maintenance lock across replicas, and `offline` revisions that use up the N slots of an agent that comes and goes.

**C. The agent sets `offline` itself.**

- Unreliable: a crash or a killed harness never sends it, which is the case the TUI most needs to show.

What happens at session close, under each option:

- Under A, a close changes no stored row. The next read reports `offline` if no other session of the identity is live.
- Under B, a close appends an `offline` revision in the `Close` transaction.
- Under every option, the status rows outlive the session row and its retention. They go only with the identity (retirement, or `ResetRegistry`) or with `ResetData`.

### 4.5 Access (ADR 0011)

Reading the statuses (list and history): ride `Identity/See`. A scoped agent sees the statuses of its own scope's agents, plus any human's or administrator's. An out-of-scope identity's history is `not_found` with the existing `identity %q is not registered` text (`decide.go:179-185`). The list uses `Checker.Identities`. No new clause is needed.

Setting a status:

**A. A new action `Identity/SetStatus`** (Recommended). The rule is "`f.Name == p.Name` and `p.Kind` is agent". The setter also passes `Session/Use` for the session it names, which proves the session is its own and renews its lease (`sessions.Service.RequireAs`, `service.go:91-110`).

- One new clause in `decide.go`, one entry in `objects.go`, rows in `decide_test.go`, and a new mutation in `check-gates-selftest.sh`.
- The human refusal sits where ADR 0011 wants it, not in the status code. Reading `p.Kind` outside `access` would trip `TestAccessRuleSites`.
- A scoped agent is always in scope of itself.

**B. Reuse `Session/Use` alone.**

- No new clause.
- A human (the TUI's identity) could set a status, which nothing asks for.

**C. Option A, plus humans and administrators may set any visible agent's status** (for example, a human clearing `needs-human` from the TUI).

- Useful later. It is a new protected-target rule and a TUI input. Recommended: not now.

### 4.6 Wire

All new operations are protected (bearer). Writes take `Idempotency-Key` (Decision 66).

Recommended shape (option A):

| Operation | Method and path | Body or result |
| --- | --- | --- |
| `setAgentStatus` | `POST /v1/sessions/{session}/status` | body `{tag, text, problem?}`; 200 `AgentStatus` |
| `listAgentStatuses` | `GET /v1/agent-statuses` | every visible identity with a stored status: `[{identity, kind, live, status: AgentStatus}]`, by identity |
| `agentStatusHistory` | `GET /v1/identities/{identity}/statuses` | `[AgentStatus]`, oldest first, at most N |

- `AgentStatus` is `{identity, revision, tag, text, problem?, session, display_name, set_at}`. On the list only, `tag` reads `offline` when `live` is false; `last_tag` keeps the stored tag.
- Also add an optional `status` (`AgentStatus`) to each `Identity` item of `listIdentities`. Then `register.others`, `discover` and the TUI's `Identities` call carry the current status with no extra call for agents.
- Totals become 47 protected, 8 open, 55 in all.
- `info.version` and `version.Protocol` stay `1.0.0` (Eric's decision 2026-10-27).

Options for the read side:

- **A.** Three operations plus the `Identity.status` field (Recommended). The TUI reads `listAgentStatuses`, which includes offline agents. Agents get statuses for free in `others` and `discover`.
- **B.** Two operations (set and history), with the current status only on `Identity`. Offline agents drop out of `listIdentities`, so the TUI can show them only from its one-hour `sessionsSeen` memory. That fails "every agent" for an agent that went away before the TUI started.
- **C.** Three operations, no `Identity` field. Agents would need a read tool (a 27th MCP tool) to see others' statuses.

Bus side: add `SetAgentStatus`, `AgentStatuses` and `AgentStatusHistory` to the `Sessions` interface (status is set through a session and listed beside `Identities`). The `Bus` stays six interfaces. `bus.SessionInfo` and `IdentityInfo` gain an optional `Status *AgentStatus`. The names avoid `Admin.Status`.

### 4.7 MCP, the CLI, the Stop hook, park and protocol text

**4.7.1 MCP.**

- One new tool, `set_status`, with inputs `tag`, `text`, `problem`, `session` (optional, as the `optSessionIn` tools take it) and `idempotency_key`. The tool count goes from 25 to 26.
- Reading needs no new tool under wire option A: `register.others` and `discover` carry `status`. History stays in the TUI and the CLI.
- The alternative is a `get_status` tool with a `history` flag, which makes 27 tools.
- `set_status` is a tool other than `receive`, so it unparks (Decision 18). The protocol text must say to set `idle` before `agentbus2 park`, never after.
- Pins to update: `TestToolListIsExact` (name table and count, `server_test.go:241-262`), `run_test.go:111`, CLAUDE.md "25 tools".
- The description text is scanned by the m6 word check: no `memory/`, `tasks/` or `general/`.

**4.7.2 CLI.** `agentbus2 status` is taken.

- **A.** `agentbus2 agent-status set [-session ID] [-problem P] [-key K] TAG TEXT`, `agent-status list`, and `agent-status history IDENTITY` (Recommended). Each prints one JSON document; a `bus.Error` exits 1.
- **B.** `agentbus2 report TAG TEXT` and `agentbus2 statuses [IDENTITY]`.
- **C.** Rename the administrator's `status` to `admin-status` and use `status` for agents. This is allowed before the release, but it changes an established command, the docs and the TUI's wording.

**4.7.3 Park and `idle`.** Park is client-only, so the server can map "parked" to `idle` only if something sends it.

- **A.** Protocol text only: call `set_status tag=idle` before `agentbus2 park` (Recommended). Park stays local and fast.
- **B.** `agentbus2 park` also sends `idle`, best effort, within a short budget. The park succeeds even if the bus is down. This adds a network write to a command Decision 18 describes as a state-file change, so it needs Eric's approval.
- **C.** No mapping at all.

**4.7.4 The Stop hook.**

- **A.** No change (Recommended). The protocol text, the skill and `mcpserver.RegisterSteps` tell the agent to set its status at register, at each task boundary, when blocked or needing the human, and to set `idle` before parking. Hinted subagents do not set status.
- **B.** The hook writes `waiting` or `idle` itself when nothing is pending. That is a new write class in a 500 ms fail-open hook (`stophook.go:166-171`), and it would overwrite `needs-human`, which is exactly when an agent stops.
- **C.** The hook blocks with a "set your status" reminder when the status is stale. That costs a model turn per stop, against Decision 17's aim.

### 4.8 Retention: N

**A. Prune at write time** (Recommended).

- In the `setAgentStatus` transaction, after the insert, delete that identity's rows with `revision <= latest - N`.
- No maintenance job and no replica question. Idempotent: a second run deletes nothing.
- Engine-neutral SQL.
- N is a `core.Config` setting `status_history_count`, default 20, range 1 to 1000, listed with the others (`config.go:113-133`) and by `config.Settings()`. The ADR states the default.
- Lowering N takes effect at each identity's next write. That is acceptable: no data is wrong, just longer than N until then. Reads return at most N regardless.

**B. A maintenance job** that trims every identity to N.

- Same result, plus a job and its lock.
- The only gain is that a lowered N applies without a write.

**C. A time limit** (for example 72 hours, like tombstones).

- Simpler to explain, but an agent quiet for a weekend loses its whole history, and Eric asked for "last N".

Under every option there is no time-based expiry of the current status: rows live as long as the identity. The bound is the number of identities × N rows, each at most about 600 bytes. Under the SQLite budget the rows are non-evictable like memories, so the capacity notice text (`budget.go:93-95`) should name them.

### 4.9 Pushed to agents, or not

- **A.** Pull only (Recommended). The TUI polls in its 5-second round. Agents read statuses in `register.others` and `discover`. A status change wakes no one: waking every agent on every status change is exactly the noise Decisions 17 and 45 remove.
- **B.** A status change publishes a notifier hint and wakes subscribers. That needs a delivery path that does not exist for a table, and per-agent filtering.
- **C.** Later, through issue #13 (MCP events), as an opt-in notification. Recorded as a follow-up, not designed here.

### 4.10 TUI layout (for Eric to choose)

Common to every option:

- The data comes from `Bus.AgentStatuses` in the 5-second status round, plus `Bus.AgentStatusHistory` when one agent's history is opened, so only the opened row costs a call.
- Imports stay `internal/bus` and `internal/version`. The tag-to-icon map lives in `icons.go`, with new `icon_map` names `status_idle`, `status_planning`, and so on (`idle` already exists and can be reused for `status_idle`).
- The status bar gains an attention count when any agent is `needs-human` or `blocked`, for example `2 need you`, in the `warn` color.
- No messages are shown on a session or agent row (ADR 0006, TUI decision 14). `TestNoInboxView` stays.

**Layout 1: inline in the `sessions` rail section, with history in the details pane.**

```
+-- rail ------------------------+-- main pane ----------------------------------+
| channels                       | name      agentbus2                           |
|  # general                     | identity  agentbus2                           |
|  # agentbus2-dev               | state     live                                |
| memories (3)                   | status    [hammer] implementing               |
| tasks                          |           Task 3: Store methods for statuses  |
|  [clip] plan-10                | problem   -                                   |
| tags                           | set       14:02 by agentbus2/reviewer         |
| sessions                       |                                               |
|  [hammer] agentbus2  Task 3... | history (oldest first, , and . to step)       |
|  [hand] tmi  needs approval    |  13:10 [map]    planning   read issue #10     |
|  [glass] tmi-ux  waiting on CI |  13:40 [hammer] implementing Task 1           |
|  [ok] eric/macmini (you)       |  13:55 [glass]  waiting  CI on 3ead0e0        |
|  [off] agentbus  offline 2h    |  14:02 [hammer] implementing Task 3           |
+--------------------------------+-----------------------------------------------+
| > compose                                                                       |
| v2.0.0  as eric  2 need you                              ? help  h health  q quit|
+--------------------------------------------------------------------------------+
```

- The smallest change. It extends `sessionRows` and `renderSession`, which TUI decision 14 already allows.
- Offline agents come from `listAgentStatuses`, not only the one-hour `sessionsSeen`.
- With per-identity keys (4.2 A), the line goes on the identity's first live session row. The other rows of that identity get only the icon.
- Weak at a glance: the rail is narrow (16 to about 32 columns), so the text is cut to a few words.

**Layout 2: an `agents` board in the main pane, opened from the `sessions` heading.**

```
+-- rail -----------------+-- agents (8) -- enter: history  esc: back -------------+
| channels                |   AGENT        TAG           SINCE  NOW                 |
|  # general              | > tmi          [hand] needs-human 3m  approve EIP change|
| memories (3)            |   tmi-ux       [no] blocked      12m  pnpm build fails  |
| tasks                   |   agentbus2    [hammer] implementing 1m Task 3: Store...|
| tags                    |   agentbus     [glass] waiting    8m  CI on dc87166     |
|>sessions (5, 2 need you)|   prime-agent  [map] planning     20m read the spec     |
|  ...                    |   docs-bot     [zzz] idle          1h  parked           |
|                         |   ---- offline ----                                     |
|                         |   old-agent    [off] offline       2d  last: Task 9 done|
+-------------------------+---------------------------------------------------------+
| > compose                                                                         |
| v2.0.0  as eric  2 need you                                ? help  h health  q quit|
+----------------------------------------------------------------------------------+
```

- `enter` on the `sessions` heading opens the board. Today `enter` there collapses or expands, so that would move to `←` and `→` only, which the heading already supports. Alternatively the board gets its own key, such as `w`.
- `↑` and `↓` move through the agents; `enter` replaces the board with that agent's history, oldest first.
- Sort: attention tags first (`needs-human`, `blocked`), then live agents by name, then offline agents.
- Best at a glance for many agents, and it uses the full width.
- A new main-pane mode beside the stream, the task tree and the session details. It is not a channel, so no rail section and no kind are involved.

**Layout 3: an overlay like health, on a free key (`w`, "who").**

```
            +-- agents ----------------------------------------------- w ---+
            |  [hand]   tmi          needs-human  3m  approve EIP change    |
            |           problem: needs Eric's Approve: line                 |
            |  [no]     tmi-ux       blocked     12m  pnpm build fails      |
            |  [hammer] agentbus2    implementing 1m  Task 3: Store methods |
            | >[glass]  agentbus     waiting      8m  CI on dc87166         |
            |    history: 13:10 planning, 13:40 implementing,              |
            |             13:55 waiting  (enter: full history)              |
            |  [off]    old-agent    offline      2d  last: Task 9 done     |
            |                                       esc close  enter history|
            +---------------------------------------------------------------+
```

- No change to the rail or the main pane, and it reuses the `overlay` helper (`view.go:861-882`).
- One key away from anywhere.
- It hides the conversation while open, so it suits a check-in, not continuous monitoring.

Recommended: Layout 2, plus the status-bar attention count, plus the icon alone on each session row from Layout 1 (no text, so the narrow rail stays readable). This is the one design question Eric excluded #10 for, so it is his call outright.

## 5. Decisions for Eric

1. **Storage.** **DECIDED: the recommendation as written (Eric's decision 2026-10-27)**
   - A: memory rows on a per-agent memory channel.
   - B: a dedicated `agent_statuses` table with the revision pattern.
   - C: a new `status` channel kind.

   Recommend B: it avoids the 72-hour tombstone purge, memory expiry, search pollution, the scope-prefix naming rule and two new ACL rules, and changes no ADR.
2. **Key.** **DECIDED: the recommendation as written (Eric's decision 2026-10-27)**
   - A: per identity, recording the setting session and display name.
   - B: per display name.
   - C: per session id.

   Recommend A, per "each agent has one status line". Hinted subagents are told not to set status.
3. **Offline.** **DECIDED: the recommendation as written (Eric's decision 2026-10-27)**
   - A: derived at read from "no live session"; agents cannot set it.
   - B: stored by the bus at `Close` and by a maintenance job at lapse.
   - C: set by the agent.

   Recommend A.
4. **Who may set.** **DECIDED: the recommendation as written (Eric's decision 2026-10-27)**
   - A: a new `Identity/SetStatus` action, own identity and agents only, plus `Session/Use`.
   - B: `Session/Use` alone.
   - C: A, plus humans and administrators on any visible agent.

   Recommend A. Reading rides `Identity/See` under every option.
5. **TUI layout.** **DECIDED: the recommendation as written (layout 2, the agents board, with the status-bar attention count and an icon-only tag on each session row) (Eric's decision 2026-10-27)**
   - A: Layout 1, inline rail rows.
   - B: Layout 2, the agents board.
   - C: Layout 3, the overlay.

   Recommend B, plus the attention count and rail icons.
6. **Tag set.** **DECIDED: the recommendation as written (Eric's decision 2026-10-27)**
   - A: the eight in 4.3: seven settable, `offline` bus-only.
   - B: Eric's seven examples only, without `reviewing`.
   - C: another list Eric names.

   Recommend A. Recorded in both repositories' ADRs.
7. **Wire read side.** **DECIDED: the recommendation as written (Eric's decision 2026-10-27)**
   - A: three operations plus `Identity.status`.
   - B: two operations, with offline agents invisible.
   - C: three operations, no `Identity` field.

   Recommend A: 47 protected, 55 in all, protocol stays 1.0.0 (Eric's decision 2026-10-27).
8. **Retention.** **DECIDED: the recommendation as written (Eric's decision 2026-10-27)**
   - A: prune to N at write, N a setting with default 20.
   - B: a maintenance job.
   - C: a time limit.

   Recommend A.
9. **MCP.** **DECIDED: the recommendation as written (Eric's decision 2026-10-27)**
   - A: one `set_status` tool (26 tools).
   - B: `set_status` plus `get_status` (27).

   Recommend A.
10. **CLI name.** **DECIDED: the recommendation as written (Eric's decision 2026-10-27)**
    - A: `agent-status set|list|history`.
    - B: `report` and `statuses`.
    - C: rename the administrator's `status`.

    Recommend A.
11. **Park and idle.** **DECIDED: the recommendation as written (Eric's decision 2026-10-27)**
    - A: protocol text sets `idle` before `park`.
    - B: `park` sends `idle` best effort (changes Decision 18's local-only park).
    - C: no mapping.

    Recommend A.
12. **Stop hook.** **DECIDED: the recommendation as written (Eric's decision 2026-10-27)**
    - A: unchanged; the protocol text carries the task-boundary rule.
    - B: the hook writes a status.
    - C: the hook blocks with a reminder.

    Recommend A.
13. **Push.** **DECIDED: the recommendation as written (Eric's decision 2026-10-27)**
    - A: pull only.
    - B: wake on change.
    - C: later through issue #13.

    Recommend A now, C as a follow-up.
14. **Repeated identical status.** **DECIDED: the recommendation as written (Eric's decision 2026-10-27)**
    - A: a new revision every time.
    - B: a no-op when nothing changed.

    Recommend A, unless Eric wants agents' re-reports kept out of the history.
15. **`problem` with `blocked` or `needs-human`.** **DECIDED: the recommendation as written (Eric's decision 2026-10-27)**
    - A: optional, as Eric said.
    - B: required for those two tags.

    Recommend A.

## 6. Dependencies

- Issues:
  - ericfitz/agentbus#43 (the shared answers must land in its ADR too; v1's newest ADR is 0025).
  - #13 (MCP events) only for any later push, not a dependency.
  - #7 (Postgres budget eviction) only for the note that status rows are non-evictable.
- Libraries: none new. `charm.land/lipgloss/v2` and `bubbletea/v2` (already used) for the TUI, `modelcontextprotocol/go-sdk` (already v1.7.0, per spec section 9) for the tool, and `oapi-codegen` through `make generate`.
- CI: `make verify` (both engines; Docker for testcontainers and `check-gates`), `ci.yml` and `windows.yml` green on the pushed commit, and the `check-gates-selftest` job (the new `decide` clause adds a mutation to block 29 or a new block).

## 7. Implementation outline (provisional, assuming the recommendations)

Each task is TDD: a failing test first. The per-task gates are `make lint`, `make test` and the named exit script. The last task runs `make verify`.

1. **Store schema and contract.**
   - Files: `internal/store/sqlite/schema/0001_initial.sql`, `internal/store/postgres/schema/0001_initial.sql` (the `agent_statuses` table, FK cascade on `identities`, PK `(identity, revision)`, index on `identity`), and `internal/store/statuses.go`. The interface `AgentStatuses` holds `AppendAgentStatus(ctx, row) (revision, error)` (computes `max+1`), `PruneAgentStatuses(ctx, identity, keep int) (int, error)`, `LatestAgentStatuses(ctx) ([]AgentStatusRow, error)` and `AgentStatusHistory(ctx, identity, limit) ([]AgentStatusRow, error)`. Also `internal/store/sqlstore/statuses.go`, and the table in `ResetData` (`meta.go`).
   - Tests: the Store suite in `internal/conformance/store`, on both engines: append, revision order, prune boundary (N-1, N, N+1), cascade on `RetireIdentity`, `ResetData` clears the table, and two concurrent appends get distinct revisions.
   - Gates: `make test-conformance`, `scripts/m7-exit-checks.sh --deps-only` (`internal/store/...` never imports `internal/core/...`).
2. **Access clause.**
   - Files: `internal/core/access/objects.go` (`SetStatus`), `decide.go` (identity facts: `f.Name == p.Name` and agent kind), `decide_test.go`, the `sites_test.go` allowlist if counts move, and `scripts/check-gates-selftest.sh` (a mutation that deletes the clause must fail a test).
   - Gates: `make check-gates-selftest`, `TestAccessRuleSites`.
3. **Core aggregate.**
   - Files: `internal/core/statuses` (new package) with `Set` (validate the tag set, the text and the problem; `access.Require` for `SetStatus` and `Session/Use` via `sessions.RequireAs`; `WithReceipt`; `Limits.Check` and `Charge`; append and prune in one `Store.Tx`), `List` (`Access.Identities`, then the latest rows, `live` from `LiveSessions`, derived `offline`) and `History`. Also `bus` types and the `Sessions` methods, `sessions.Service.Identities` filling `Status`, `internal/core/config.go` (`StatusHistoryCount`) and `internal/server/config` (the setting).
   - Tests: unit tests per validation case (missing, unknown and `offline` tag; empty, multiline and over-length text and problem), the replay of an idempotency key, and the read derivation at the lease boundary with an injected clock.
   - Gates: `make test`, `scripts/m4-exit-checks.sh --deps-only`.
4. **Bus suite.**
   - Files: `internal/conformance/bus` (a status test across both engines and both transports): set and read, history of N+2 writes returns N oldest first, a scoped agent cannot read an out-of-scope agent's history (`not_found`) but sees a human's and an administrator's, a human's set is `forbidden`, offline after the lease lapses with the fake clock, a replayed key adds no revision.
   - Gates: `make test-conformance` (local and `AGENTBUS2_BUS_TRANSPORT=inproc`).
5. **Wire.**
   - Files: `api/openapi.yaml` (three operations, `AgentStatus`, `Identity.status`; `info.version` stays `1.0.0`), `internal/version/version.go` (`Protocol` stays `"1.0.0"`), `make generate`, `internal/server/statuses.go` (handlers through `WriteProblem`), `internal/bus/remote`, and the pins `internal/server/cmd/cmd_test.go:432` and `internal/gen/openapi31_test.go:173` (47 and 8).
   - Gates: `make generate-check`, `scripts/m4-exit-checks.sh`, `scripts/m5-exit-checks.sh --deps-only`.
6. **MCP and CLI.**
   - Files: `internal/mcpserver/server.go` and `inputs.go` (`set_status`; `status` in `identityOut`), `server_test.go` and `run_test.go` (26), `internal/cli/agentstatus.go` and `cli.go` (the `agent-status` command), and their tests (exit 1 on a `bus.Error`, exit 2 on a flag error, an unknown tag rejected by the server, not the client).
   - Gates: `scripts/m6-exit-checks.sh` (word check), `make test`.
7. **Protocol text.**
   - Files: `internal/cli/skills/using-agentbus2/SKILL.md`, `mcpserver.RegisterSteps`, and `internal/cli/install.go`: set status at register and at task boundaries, `idle` before `park`, hinted sessions do not set status, `needs-human` with a problem when blocked on the user.
   - Tests: `internal/cli/skill_test.go` pins the words.
   - Gates: the m6 word check.
8. **TUI.**
   - Files: `internal/tui/model.go` (the status round adds `AgentStatuses`; history on demand), `view.go` (the chosen layout), `icons.go` (the tag icons in each set, with width tests like `icons_test.go` and `icons_windows_test.go`), `help.go`, and `docs/tui.md`.
   - Tests: view tests on the fake terminal, the attention count, offline rows, history order, `TestNoInboxView` unchanged.
   - Gates: `scripts/m8-exit-checks.sh` (allowlist unchanged), `make test`.
9. **Docs and ADRs.**
   - New `docs/adr/0012-agent-status.md` (Eric's decisions labeled as his: the tag set, N, offline, the key, push or pull) and the matching ADR in agentbus.
   - Updates: ADR 0001 addendum and ADR 0010 decision 12 (55 operations), CLAUDE.md (26 tools, operation counts, the command list), `README.md`, `docs/ops/` configuration (`status_history_count`), and the capacity notice text.
   - Final gate: `make verify`.

New import edges:

- `internal/core/statuses` imports `internal/bus`, `internal/core`, `internal/core/access`, `internal/core/rules` and `internal/store`. The memories aggregate has the same shape, and the Decision 47 rule is respected.
- `internal/server` adds a handler file. It does not import `bus/local` or `bus/remote`.
- `internal/tui` adds nothing.
- `internal/mcpserver` and `internal/cli` reach statuses through `internal/bus` only.

## 8. Risks and test strategy

- **Concurrency.** Two sessions of one identity set a status at once. The serializable `Tx` and the Store's retry (`sqlstore/store.go:50-93`) make `max+1` safe, and the PK `(identity, revision)` makes a lost race a retryable conflict, never a duplicate. The Store suite runs the race on both engines.
- **Replicas.**
  - Under offline option A and retention option A, nothing runs in maintenance, so there is no lock interaction.
  - Under offline option B, the job is idempotent ("latest is not offline"), and two replicas are serialized by the maintenance lock.
- **Second run.** A replayed `idempotency_key` returns the stored result and adds no revision. A second prune deletes nothing.
- **Overlapping runs.** Two `set_status` calls without keys produce two revisions in commit order, and the last one wins. That is the intended behavior.
- **Failed-run leftovers.** A transaction that fails after the insert and before the prune rolls back whole, because both are in one `Tx`. A client that timed out and retries with its key gets the replay.
- **Lease edge.** `live` is computed with the skew rule of `Core.IsLive` (`sessions.go:9-14`). Tests pin the exact boundary with the injected clock.
- **TUI width.** New emoji risk the Terminal.app advance problem (`icons.go:36-58`). The icon tests must cover each tag icon in the emoji, nerdfont and Windows sets.
- **Windows CI.** No new process or path code. The TUI icon tests already have a Windows variant (`icons_windows_test.go`), and the new icons go in it.
- **Alignment drift with v1.** If v1 implements first with a different tag set, the ADRs diverge. Mitigation: settle decisions 3, 6, 8, 13 and 15 once and copy them verbatim into both ADRs.
- **Protocol text compliance.** Agents may not set statuses reliably. Derived `offline` makes the most important state correct without them, and the board shows "last said" with its age.

## 9. Size estimate

L. It touches every layer (schema on two engines, Store suite, access clause and gate, core, wire with three operations, MCP, CLI, protocol text, TUI view, two ADRs), though each piece follows an existing pattern.
