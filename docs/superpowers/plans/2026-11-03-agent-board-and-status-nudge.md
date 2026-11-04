# Agent board, stale-status nudge and heading rule (issue #27) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Adopt agentbus v1's agents board interaction, a Stop hook nudge for a stale or missing status, and the rail heading rule in agentmesh.

**Architecture:** Two new settings (client `hooks.status_stale_minutes` in `config.json`, server `status_offline_hours` in the core), a status read added to the Stop hook beside its `Peek`, and a rework of the TUI board's opening, keys, rows and sort. No wire change, no schema change.

**Tech Stack:** Go, Bubble Tea (`internal/tui`), `modernc.org/sqlite` and pgx stores (untouched).

**Spec:** `docs/superpowers/specs/2026-11-03-agent-board-and-status-nudge-design.md`; decisions are ADR 0014 decisions 16 to 21 (`docs/adr/0014-agent-status.md`, addendum "issue #27").

## Global Constraints

- No change to `api/openapi.yaml`, `internal/gen`, the Store or the schema.
- `internal/tui` imports only `internal/bus` and `internal/version` of this module (`scripts/m8-exit-checks.sh --deps-only`).
- The Stop hook always exits 0 and falls open within `hookBudget` (500 ms); `stop_hook_active` returns before anything else.
- `hooks.status_stale_minutes`: default 30, range 0 to 1440, 0 off. `status_offline_hours`: default 24, range 0 to 720, 0 live only.
- Nudge texts, exactly: `Agentmesh: your status is "<tag>: <text>" (<age>); if that is no longer true, call set_status.` and `Agentmesh: you have no status; call set_status.`; `<text>` cut to 80 runes with `…`; age `41m`, `5h`, `2d` (whole units, floor).
- Working tags that can nudge: `planning`, `implementing`, `reviewing` only; stale means strictly greater than the threshold.
- American English; every commit message ends with the session's attribution lines; work lands on `main` (no push without Eric).
- Done gate: `make verify` (needs Docker).

## Review Focus

1. **A `config.json` save drops `hooks`.** `profile` rebuilds the file from `configFile` on save (`init`, the TUI's rail width); a user's `hooks` must survive. Test in Task 1.
2. **The status read is slow and eats the budget the message part needs.** `Peek` and the status read run concurrently; a status read that never returns must not suppress a `Peek` answer. Test in Task 3.
3. **The status list holds only other identities, or is empty.** The hook matches the bound identity exactly; no matching entry means "no status". Test in Task 3.
4. **`b` while composing or in a prompt types a letter, not the board.** `b` opens the board only from the normal keymap. Test in Task 4.
5. **The selected board row vanishes (agent leaves the window) while its history is open or the row is expanded.** Selection falls back to the first row; history closes only on another rail selection; no panic on an empty board. Test in Task 5.

---

### Task 1: `hooks` settings in `config.json`

**Files:**
- Modify: `internal/profile/config.go` (`Config`, `configFile`, `Load`, `Validate`, save path), `internal/client/options.go` (`Resolved`, `Resolve`), `README.md`
- Test: `internal/profile/config_test.go`, `internal/client/options_test.go`

**Interfaces:**
- Produces: `type HookSettings struct { StatusStaleMinutes *int \`json:"status_stale_minutes,omitempty"\` }`; `func (s HookSettings) Validate() error`; `func (s *HookSettings) StaleMinutes() int` (nil receiver or nil field: 30); `profile.Config.Hooks *HookSettings` and `configFile.Hooks` (`json:"hooks,omitempty"`); `client.Resolved.Hooks profile.HookSettings` (zero value when absent).

- [ ] **Step 1: Write failing tests** `TestHookSettings` (table: absent → `StaleMinutes()==30`; 0 → 0; 1440 → 1440; -1 and 1441 → error containing `hooks.status_stale_minutes must be between 0 and 1440`; `{"hooks":{"bogus":1}}` → `Load` error naming `bogus`), `TestSavePreservesHooks` (load a file with `hooks` 45, call `SetTUIRailWidth` and the save `init` uses, reload: 45), and in `internal/client` `TestResolveCarriesHooks` (file with 12 → `Resolved.Hooks.StaleMinutes()==12`).
- [ ] **Step 2: Run** `go test ./internal/profile/ ./internal/client/ -run 'Hook' -count=1` — FAIL (undefined).
- [ ] **Step 3: Implement.** Mirror `TUISettings`: validate in `Load` and `Config.Validate`; carry through `configFile` both ways. Unknown keys inside `hooks` are already rejected by `DisallowUnknownFields` (verify the test). Set `Resolved.Hooks` in `Resolve` from the loaded config.
- [ ] **Step 4: Run** the same command — PASS; then `go test ./internal/profile/ ./internal/client/ ./internal/cli/ -count=1`.
- [ ] **Step 5: README.** After the profile example, one paragraph and a JSON snippet for `"hooks": {"status_stale_minutes": 30}` (meaning, range, 0 off).
- [ ] **Step 6: Commit** `feat(profile): hooks.status_stale_minutes in config.json (ADR 0014 decision 17)`.

### Task 2: `status_offline_hours` in the core

**Files:**
- Modify: `internal/core/config.go` (field, default 24, bounds row `{"status_offline_hours", c.StatusOfflineHours, 0, 720}`), `internal/server/config/config.go` (field `StatusOfflineHours int \`json:"status_offline_hours"\`` and both copies at ~143 and ~202), `internal/core/statuses/statuses.go` (`List`), `docs/ops/configuration.md` (row after `status_history_count`, env `AGENTMESH_SERVER_STATUS_OFFLINE_HOURS`)
- Test: `internal/core/statuses/statuses_test.go`, `internal/core/config_test.go`, `internal/server/config/config_test.go`, `internal/core/rules/status_test.go` (or statuses_test)

**Interfaces:**
- Consumes: `core.Core.Now()`.
- Produces: `core.Config.StatusOfflineHours int`; `List` omits an entry with `!Live && SetAt < Now - hours` (ms); 0 omits every offline entry.

- [ ] **Step 1: Failing tests.** `TestListOfflineWindow` with an injected clock: offline at `24h-1ms` ago kept, exactly `24h` kept, `24h+1ms` dropped; hours 0 → only live entries; a live agent whose status is 30 days old kept. Config: 0 and 720 accepted, -1 and 721 rejected (`status_offline_hours must be between 0 and 720`); server config reads `AGENTMESH_SERVER_STATUS_OFFLINE_HOURS=5` → 5. `TestTagNotTrimmed`: `" idle"` and `"idle "` → `validation`.
- [ ] **Step 2: Run** `go test ./internal/core/... ./internal/server/config/ -count=1 -run 'Offline|TagNotTrimmed|Bounds'` — FAIL.
- [ ] **Step 3: Implement** the field, default, bound, server setting and the filter in `List` (after computing `isLive`).
- [ ] **Step 4: Run** — PASS; then `make test-conformance` is not required here, but `go test ./internal/core/... ./internal/server/... -count=1` must pass.
- [ ] **Step 5: Commit** `feat(statuses): status_offline_hours window on the agent list (ADR 0014 decision 18)`.

### Task 3: Stop hook nudge

**Files:**
- Modify: `internal/cli/stophook.go`; `internal/cli/skills/using-agentmesh/SKILL.md` ("Status" section); `internal/mcpserver` `Instructions` and `MCPOnlyInstructions` (one sentence each)
- Create: `internal/cli/stopnudge.go` (pure helpers)
- Test: `internal/cli/stopnudge_test.go`, `internal/cli/stophook_test.go`

**Interfaces:**
- Consumes: `client.Resolved.Hooks.StaleMinutes()` (Task 1); `bus.Bus.AgentStatuses(ctx) ([]bus.AgentStatusEntry, error)` (existing, `internal/bus/sessions.go:150`); `sessionstate` file's `Primary`.
- Produces: `func stopNudge(entry *bus.AgentStatusEntry, primary bool, staleMinutes int, now time.Time) string` ("" for no nudge; `entry` nil = no status; uses `entry.Status.Tag` as stored — use `LastTag` when `Tag` is `offline`, though a stopping agent is live); `func shortAge(d time.Duration) string` (`41m`, `5h`, `2d`; under a minute `0m`).

- [ ] **Step 1: Failing unit tests** `TestStopNudge` (threshold 30): `implementing` 31m → `Agentmesh: your status is "implementing: X" (31m); if that is no longer true, call set_status.`; 29m → ""; exactly 30m → ""; nil entry → `Agentmesh: you have no status; call set_status.`; `idle`, `waiting`, `blocked`, `needs-human` at 10h → ""; `planning` and `reviewing` at 31m → nudge; primary false → ""; threshold 0 → "" (also for nil entry); 120-rune text → cut to 80 runes ending `…`. `TestShortAge`: 59s→`0m`, 41m→`41m`, 5h10m→`5h`, 49h→`2d`.
- [ ] **Step 2: Failing hook tests** in `stophook_test.go`, using its existing harness: nudge only (no messages) → one `block` with the nudge as reason; nudge plus messages → reason starts with the nudge, then a space, then today's `Agentmesh: N new message(s)...`; parked, no state file and `stop_hook_active` → nothing; `Primary` false with messages → today's output, no nudge; status read that fails → no nudge, messages reported, `hook.log` line with stage `status`; status read that blocks past the budget while `Peek` answers → messages reported within `hookBudget`, no nudge (Review Focus 2); entries for other identities only → treated as no status (Review Focus 3).
- [ ] **Step 3: Run** `go test ./internal/cli/ -run 'StopNudge|ShortAge|StopHook' -count=1` — FAIL.
- [ ] **Step 4: Implement.** In `stopHookWork`, after `client.Open`, start the status read in a goroutine bounded by the hook's context; run `Peek` (and the lapsed resume) as today; then take the status result if it arrived before the context ends, else log stage `status` and skip the nudge. Compose the reason per spec section 3 step 6. Keep `stop_hook_active`, locate and parked handling first and unchanged.
- [ ] **Step 5: Run** — PASS; then `go test ./internal/cli/ ./internal/mcpserver/ -count=1`.
- [ ] **Step 6: Protocol text.** SKILL.md "Status": one sentence — the Stop hook reminds a top-level agent whose `planning`, `implementing` or `reviewing` status is older than `hooks.status_stale_minutes` (default 30), or that has none, to call `set_status`. Same sentence in both MCP `Instructions` (the MCP-only text must not name `agentmesh wait`, `watch`, `park`, `init` or `mcp`); update any golden test of those texts.
- [ ] **Step 7: Commit** `feat(stop-hook): nudge a stale or missing status (ADR 0014 decision 16)`.

### Task 4: Board opens on heading selection; heading keys and focus

**Files:**
- Modify: `internal/tui/agents.go` (`agentBoard`: replace `open` with focus state), `internal/tui/model.go` (rail selection, `updateNormal`, `b`), `internal/tui/view.go` (main pane chooses the board when the selection is the `sessions` heading)
- Test: `internal/tui/agents_test.go`

**Interfaces:**
- Produces: `func (m Model) boardShown() bool` (rail selection is the `sessions` heading); `agentBoard.focus bool`; `func (m *Model) selectSessionsHeading()`; history (`agentBoard.hist`) closes in the rail-selection change path when the new selection is not the `sessions` heading.

- [ ] **Step 1: Failing tests.** `TestBoardShownOnHeadingByArrows` (↓ onto the heading shows the board title `agents (`; ↓ off it shows that row's content); `TestBoardKeyB` (`b` from a channel row selects the heading and shows the board; `b` in compose and in a prompt inserts `b`: Review Focus 4); `TestSessionsHeadingEnterCollapses` (`enter`, `←`, `→` on the heading collapse/expand; the board still shows); `TestBoardFocus` (`tab` focuses the board, ↑/↓ move the board row; `esc` returns focus to the rail and the board stays shown); `TestBoardHistoryLifetime` (`enter` on a row opens history; `tab` away and back keeps it; selecting another rail row closes it; `esc` in history returns to the board with the same row). Remove or rewrite the tests of the old `enter`-opens/`esc`-closes behavior; keep `TestNoInboxView`'s intent.
- [ ] **Step 2: Run** `go test ./internal/tui/ -run 'Board|SessionsHeading|NoInbox' -count=1` — FAIL.
- [ ] **Step 3: Implement.** Remove the settled note 4 marker comments that no longer hold (replace with `ADR 0014 decision 20`).
- [ ] **Step 4: Run** — PASS; `go test ./internal/tui/ -count=1`; `bash scripts/m8-exit-checks.sh --deps-only`.
- [ ] **Step 5: Commit** `feat(tui): board shows on the sessions heading; b, tab and esc (ADR 0014 decision 20)`.

### Task 5: Board rows, expand, sort cycle, help and docs

**Files:**
- Modify: `internal/tui/agents.go` (`boardRows`, `boardLines`, new sort), `internal/tui/model.go` (keys `o`, `→`, `←` while the board is focused), help text file in `internal/tui`, `docs/tui.md` ("Agents board", key table, rail paragraph, heading rule paragraph)
- Test: `internal/tui/agents_test.go`

**Interfaces:**
- Consumes: Task 4's `boardShown`, `agentBoard.focus`.
- Produces: `type boardSort int` (`sortTime`, `sortName`, `sortTag`); `agentBoard.sort boardSort`, `agentBoard.expanded map[string]bool`; `func sortBoardRows(rows []boardRow, mode boardSort)`; `func ageText(d time.Duration) string` (same format as the hook's `shortAge`; duplicated because `internal/tui` may not import `internal/cli`).

- [ ] **Step 1: Failing tests.** `TestBoardSortCycle` (`o`: time → name → tag → time; title shows `sort: time|name|tag`; time = newest `SetAt` first, never-set last; ties by identity in every mode; tag mode sorts offline rows as `offline`; the selected identity stays selected across `o` and across a status round); `TestBoardRowColumns` (a row shows icon, identity, tag, `3m`/`5h`/`2d`, text and problem; on a narrow width text and problem end with `…`; offline rows render dim and show last text; a live agent with no status shows `-`); `TestBoardExpand` (`→` wraps full text and problem below the row, `←` collapses; per identity); `TestBoardEmpty` (`no agents`, keys do not panic); `TestBoardSelectedRowGone` (the selected agent leaves the list while expanded: selection falls back to the first row, no panic: Review Focus 5); `TestHelpListsBoardKeys` (`b`, `o`, `→`/`←`, `enter`, `esc`). Delete the tests of the old needs-human/blocked/offline-divider order.
- [ ] **Step 2: Run** `go test ./internal/tui/ -run 'Board|Help' -count=1` — FAIL.
- [ ] **Step 3: Implement.** Remove the offline divider and the attention-first order (settled note 6's order; the `-` row for a live agent with no status stays); the attention count is untouched.
- [ ] **Step 4: Run** — PASS; `go test ./internal/tui/ -count=1`; `bash scripts/m8-exit-checks.sh --deps-only`.
- [ ] **Step 5: Docs.** Rewrite `docs/tui.md` "Agents board" to the spec section 5; the key table rows for `b`, `o`, `enter` on a heading; the rail paragraph (no special `enter` on `sessions`); add the heading rule (decision 21).
- [ ] **Step 6: Commit** `feat(tui): board rows, expand and the o sort cycle (ADR 0014 decision 19)`.

### Task 6: Done gate

- [ ] **Step 1:** `make verify` (Docker running). Expected: exit 0, the Postgres suites ran (not skipped).
- [ ] **Step 2:** `graphify update .` if `graphify-out/` exists.
- [ ] **Step 3:** Report the `make verify` tail; do not push (Eric pushes after his word; then `ci.yml` and `windows.yml` must be green).
