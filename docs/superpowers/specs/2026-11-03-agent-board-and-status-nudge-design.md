# Agent board, stale-status nudge and heading rule (issue #27): design

Status: approved by Eric, 2026-11-03 (both sections, in chat). The decisions are recorded in ADR 0014's addendum "Issue #27 (Eric, 2026-11-03)" as decisions 16 to 21.

Source: agentbus v1's agent status design (ericfitz/agentbus#43, `docs/superpowers/specs/2026-11-03-agent-status-design.md` in the agentbus repository, with its "implementation ruling, 2026-11-03" refinements), adopted for agentmesh where this document says so.

## 1. Goal

Bring agentmesh's agents board and Stop hook in line with v1: the board's interaction model, a Stop hook nudge when an agent's working status is stale or missing, and the rail heading rule. agentmesh already has the status data, the wire operations and a board (ADR 0014, issue #10); this changes behavior, not storage or the wire.

## 2. Decisions (Eric, 2026-11-03)

Recorded in ADR 0014 as decisions 16 to 21:

16. **Stop hook nudge** (reverses decision 12; option C of the original brief): the hook blocks once with a reminder that quotes the current status.
17. **`status_stale_minutes` is client configuration**: a new top-level `hooks` object in `config.json` (option A). Not chosen: a server setting sent over the wire (B), a fixed constant (C).
18. **`status_offline_hours` is a server setting applied in the core** (option A), so the board and `agent-status list` agree. Not chosen: a TUI-only filter (B).
19. **Board sort is v1's exactly** (option A): `o` cycles time, name, tag; no pinned attention group, no offline divider. Not chosen: attention pinned (B), offline divider kept (C).
20. **Board keys are v1's exactly** (option A): the board shows while the `sessions` heading is selected; `enter` on that heading collapses or expands it like any heading; `tab` focuses the board; `esc` returns focus to the rail. Not chosen: `enter` focuses the board (B).
21. **Rail heading rule** (v1 ADR 0024 amendment): headings that exist today keep showing the last selected content; a heading added or changed from now on shows a blank main pane or a board for its section. `sessions` is the first.

Already true, no change: the tag is compared exactly, never trimmed (`rules.CheckStatusTag`; `" idle"` is `validation`). A test pins it (section 6).

## 3. Stop hook nudge (`internal/cli/stophook.go`)

Order of checks:

1. `stop_hook_active` (or Grok's form) returns before anything else, as now, so the hook never keeps an agent awake in a loop.
2. Locate the session; none, or `Parked`, returns as now.
3. Open the Bus as now. Run the existing `Peek` and one `AgentStatuses` call concurrently inside the existing `hookBudget` (500 ms); keep only the entry whose identity is the bound identity. No wire change.
4. Build a nudge when all of these hold:
   - the located session's state file has `Primary` true (top-level, registered without a hint);
   - `status_stale_minutes` (from `config.json` `hooks`, default 30) is greater than 0;
   - and either the entry's stored tag is `planning`, `implementing` or `reviewing` and `now - set_at` is strictly greater than the threshold, or the identity has no status.
   An `idle`, `waiting`, `blocked` or `needs-human` status never nudges, at any age.
5. Text (the `text` is cut to 80 runes with `…`; age is `41m`, `5h`, `2d`):
   - with a status: `Agentmesh: your status is "<tag>: <text>" (<age>); if that is no longer true, call set_status.`
   - with none: `Agentmesh: you have no status; call set_status.`
6. Output:
   - messages waiting and a nudge: one `block` whose reason is the nudge, a space, then today's message reason;
   - messages waiting only: today's output, unchanged;
   - a nudge only: one `block` whose reason is the nudge;
   - neither: nothing.
7. Failure: if the status read fails or does not finish in the budget, there is no nudge, the message part still goes out when `Peek` answered, and `hook.log` gets one line (stage `status`). A `Peek` failure behaves as today (nothing printed, one log line). The hook still always exits 0 and falls open within 500 ms.

The lapsed-session resume path stays as it is and runs before the status read is used.

## 4. Settings

### `hooks.status_stale_minutes` (client, `config.json`)

```json
{
  "profiles": {"default": {"mode": "local"}},
  "hooks": {"status_stale_minutes": 30}
}
```

- `profile.Config.Hooks *HookSettings` beside `TUI`; `HookSettings.StatusStaleMinutes *int` (absent means 30); range 0 to 1440, 0 turns the nudge off.
- `HookSettings.Validate` runs in `profile.Load` and `Config.Validate` like `TUISettings.Validate`: a value out of range, or an unknown key in `hooks`, is an error naming the key and the range. Like a bad `tui` value, it makes every command that loads `config.json` fail with that error; the Stop hook fails open and logs it.
- Documented in `README.md` beside the profile example (the one user doc for `config.json`) and in the `using-agentmesh` skill's Status section.

### `status_offline_hours` (server)

- `core.Config.StatusOfflineHours`, default 24, range 0 to 720 (checked with the other ranges in `internal/core/config.go`); `internal/server/config` field `status_offline_hours`, environment `AGENTMESH_SERVER_STATUS_OFFLINE_HOURS`; a row in `docs/ops/configuration.md`.
- `statuses.Service.List` drops an entry that is not live and whose stored `set_at` is older than `now - StatusOfflineHours` (an entry exactly at the edge stays). 0 means live entries only. A live agent with no status is unaffected.
- History reads (`agentStatusHistory`) and `Identity.status` are unchanged: the window filters the list only.
- Local mode uses the core default, as it does for `status_history_count`.

## 5. TUI (`internal/tui`)

- **Opening.** The board replaces the main pane whenever the rail selection is the `sessions` heading: by `↑`/`↓` onto it, or by `b` from anywhere outside a modal or the composer, which selects that heading (expanding nothing, collapsing nothing). Selecting any other rail row shows that row's content as today.
- **Heading keys.** `enter` and `←`/`→` on the `sessions` heading collapse or expand it, like every other heading.
- **Focus.** `tab` moves focus to the board (as it does to a stream); `esc` in the board returns focus to the rail. While the board has focus, `q`, `?`, `h` and `b` work as everywhere.
- **Rows.** Icon, identity, tag, age (`3m`, `5h`, `2d`), text, then problem in the warning color. Text and problem share the remaining width, each cut with `…`. A live agent with no status shows `-` for tag, age and text. Offline rows are dimmed and show their last text and the `offline` icon. An empty board reads `no agents`.
- **Expand.** `→` on a row expands it: its full text and problem wrap on the lines below it. `←` collapses it. Expansion is per identity.
- **Sort.** `o` cycles time (newest `set_at` first; never-set entries last), then name, then tag (by the displayed tag, so offline rows sort as `offline`). Every mode breaks ties by identity. The selection follows its agent across a re-sort and a status round. No pinned groups, no offline divider. The title line shows the mode: `agents (N) · sort: time`.
- **History.** `enter` on a row replaces the board with that agent's history, oldest first (`<time> <icon> <tag> <text>[ · <problem>] by <display name>`, as today), scrolling with `pgup`/`pgdn` and `g`/`G`; `esc` returns to the board with the same row selected. The history closes when another rail row is selected; a `tab` focus change keeps it open. An open history is refetched once per status round, as today.
- **State.** Sort, expansion and the selected row last until the TUI quits.
- **Unchanged.** One `AgentStatuses` call per 5-second round, no new polling; the attention count in the status bar and on the `sessions` heading; the session-row icons; the import allowlist (`internal/bus`, `internal/version`).
- **Help.** `?` documents `b`, `o`, `→`/`←` on the board, `enter` for history and `esc` from history and from the board.
- **Heading rule.** `channels`, `memories`, `tasks` and `tags` keep showing the last selected content when selected. `docs/tui.md` states the rule for future headings.

## 6. Tests

- `internal/cli` (`stophook_test.go`): stale (31m with threshold 30: nudge), fresh (29m: none), exactly at the threshold (30m: none), no status (nudge), each non-working tag at any age (none), parked (nothing), `Primary` false (no nudge, messages still reported), no state file (nothing), `stop_hook_active` (nothing), threshold 0 (none), the status read failing or timing out (no nudge, messages still reported, a `status` line in `hook.log`), nudge plus messages (one block, nudge first), long text cut at 80 runes.
- `internal/profile`: `hooks` absent (30), 0, 1440, -1 and 1441 rejected, an unknown key rejected.
- `internal/core/statuses`: offline entry just inside, exactly at and just outside the window; window 0 (live only); a live agent with an old status stays. `internal/core` config: range checks for `status_offline_hours`. `internal/server/config`: the setting and its environment variable.
- `internal/core/rules` (or statuses): `" idle"` and `"idle "` are `validation`.
- `internal/tui`: the board shows on heading selection by arrows and by `b`, and other rows restore their content; `enter`/`←`/`→` on the heading collapse and expand; `tab` focus and `esc` back to the rail; row expand and collapse; the `o` cycle with ties and never-set last, selection kept; history open, `esc` back to the same row, closed by another rail selection, kept across `tab`; offline rows dimmed; `no agents`; the help text. `TestNoInboxView` keeps its intent.

## 7. Docs

`docs/tui.md` ("Agents board", the key table, the rail paragraph, the heading rule), `docs/ops/configuration.md` (`status_offline_hours`), `README.md` (`hooks`), the "Status" section of `internal/cli/skills/using-agentmesh/SKILL.md` and the MCP `Instructions` (one sentence on the nudge), ADR 0014 (addendum).

## 8. Out of scope

- The CLI `agent-status list` sort (v1 sorts its CLI too; not part of issue #27).
- v1's status in the stream header and offline rail rows.
- Push or wake on a status change (issue #13).

## 9. State (Definition of Done D)

Nothing new is stored. The nudge is stateless: a second stop after a block carries `stop_hook_active` and is never nudged; two overlapping hooks each read and print independently. The offline window is applied at read, so changing it takes effect at the next list with no leftovers.
