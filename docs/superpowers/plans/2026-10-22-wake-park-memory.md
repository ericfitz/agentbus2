# Wake, Park and Memory (Decisions 17-20) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Cut model turns that carry no message: background `wait` instead of Monitor `watch`, park after handoff, a short subagent hook, and no memory pushed to agents, matching agentbus v1 ADR 0025.

**Architecture:** All client side: protocol text (`internal/cli/install.go`, the using-agentbus2 skill), the hooks (`internal/cli/stophook.go`, `subagenthook.go`, a new `UserPromptSubmit` hook command `agentbus2 unpark`), the session state file (`internal/sessionstate`), `wait`/`watch` (`internal/cli`), and the MCP server (`internal/mcpserver`). No wire or Store change.

**Tech Stack:** Go.

**Spec:** `docs/superpowers/specs/2026-10-21-gate-0-decisions.md`, "Wake, park and memory decisions (Eric, 2026-10-22)", items 17-20. Reference for wording parity: `/Users/efitz/Projects/agentbus/docs/adr/0025-wake-park-and-session-names.md`.

## Global Constraints

- Import rules (`CLAUDE.md`, m6): `internal/mcpserver` and `internal/cli` never import `internal/gen` or `internal/store`; `internal/sessionstate` never imports `internal/core`, `internal/server`, `internal/bus/local` or the store engines.
- Hooks always exit 0 and fall open within the hook budget, leaving one line in `hook.log` on failure (`runHook`).
- `mcp` writes nothing to stdout but the protocol.
- `scripts/m6-exit-checks.sh` agent-text word check and e2e counts stay green; `TestExitCodeTable` cells unchanged.
- Docs updated with the code: `CLAUDE.md` command bullets, `README.md`, `docs/install.md`, the skill.
- Per task: `make lint`, `make test`, `bash scripts/m6-exit-checks.sh --deps-only`.

## Review Focus

1. A background `wait` that is running when the session parks: its next wake is ignored (re-armed silently), not delivered as exit 0.
2. `agentbus2 unpark` with no state file, an unreadable one, or no harness found: prints nothing, exit 0, within the hook budget.
3. Parking a session whose state file is shared by the MCP server: concurrent rewrite of the state file by the MCP server (unpark on a tool call) and by `park` must not lose other fields (atomic replace of a re-read file).
4. A tag subscription that matches a memory on a memory channel: `wait` does not wake for it and MCP `receive` does not return it, but the batch is still acked so it never comes back.
5. Codex: no idle-wake instruction appears for Codex anywhere; Claude Code and Grok get the same one.

---

### Task 1: Minimal subagent hook (decision 19)

**Files:** `internal/cli/subagenthook.go:52-58`, its test, `docs/` that quote it.

- [ ] Failing test: the additionalContext is under 300 characters, contains the register call with `hint=<suggested>` and the words `using-agentbus2` and `receive`, and says not to start `wait` or `watch`.
- [ ] Implement the text: register with the suggested hint (or the one your prompt names) only if your prompt asks you to use agentbus2; follow the using-agentbus2 skill; do not start `wait` or `watch`, use receive.
- [ ] Checks; commit `feat(hooks): shorten the subagent hook text (decision 19)`.

### Task 2: Background wait everywhere but Codex (decision 17)

**Files:** `internal/cli/install.go` (`InitPrompt` step 3, `grokRule`, `codexPrompt`), `internal/cli/stophook.go:150-157` (one tail for every harness; no Monitor), `internal/cli/skills/using-agentbus2/SKILL.md` (the idle-wake section: background `agentbus2 wait`; exit 0 receive then start again; 1 only with `-timeout`, start again; 2 report; 3 and 4 do nothing; Codex none), `internal/cli/watch.go` usage/help says deprecated, tests that pin the old text, `CLAUDE.md`, `README.md`, `docs/`.

- [ ] Failing tests: the stop-hook reason for a Claude Code session and for another harness both say to start a background `agentbus2 wait` and never mention Monitor; `InitPrompt` and the skill never mention Monitor or `watch` as the recommended wake; `agentbus2 watch -h` says deprecated.
- [ ] Implement, docs, checks (the m6 word check included); commit `feat(protocol): background wait on every harness except Codex; watch deprecated (decision 17)`.

### Task 3: No memory pushed to agents (decision 20)

**Files:** `internal/mcpserver` (subscribe tool refuses a memory-kind channel; register drops the identity's memory subscriptions; receive filters memory-kind messages from the returned batch while acking it), `internal/cli` (`channels subscribe` refuses memory; `wait`/`watch` treat a wake whose channels are all memory-kind as no wake and re-arm with the floor advanced; the stop hook's peek ignores memory-kind channels), a small shared helper that maps channel name to kind via `ListChannels` (cached per process run), tests in both packages, the skill text (memories: use search and get_memory; never subscribe), docs.

Channel kind lookup: `bus.Bus.ListChannels` gives `Kind`; `tasks` is its own kind (`bus.KindTasks`) and stays pushed.

- [ ] Failing tests: MCP subscribe to a memory channel → validation error naming search/get_memory; CLI `channels subscribe` likewise exit 1; MCP register with an existing memory subscription leaves none; a tag subscription matching a memory: MCP receive returns no memory message and the next receive does not redeliver it; `wait` with only a memory-channel message pending does not exit 0 (use `-timeout` and expect 1); the stop hook prints nothing for a memory-only backlog; the TUI path (bus directly) still sees memories.
- [ ] Implement, docs, checks; commit `feat(agents): agents never receive memory channels (decision 20)`.

### Task 4: Park and unpark (decision 18)

**Files:** `internal/sessionstate/state.go` (`File.Parked bool json:"parked,omitempty"`; `SetParked(dataDir, session string, parked bool) error` re-reads and atomically replaces), new `internal/cli/park.go` (`agentbus2 park [-session ID]`, `agentbus2 unpark [-session ID]`; session from `-session` or `locateSession` like the hooks; `unpark` prints nothing and always exits 0; `park` exits 0 on success, 2 when no session is found), `internal/cli/cli.go` (dispatch), `internal/cli/wait.go` and `watch.go` (on each wake re-read the state file; parked → advance the floor and re-arm silently), `internal/cli/stophook.go` (parked → print nothing), `internal/mcpserver` (every tool call other than receive, and register, clears `Parked` in its own state file when set), `internal/cli/install.go` (`init -global` installs `agentbus2 unpark` as a `UserPromptSubmit` hook for Claude Code, Codex and Grok, alongside the existing hooks; `-dry-run` lists it), tests, `CLAUDE.md` (park/unpark bullet), `README.md`, `docs/`, the skill (handoff: run `agentbus2 park` at session end).

- [ ] Failing tests: `park` then the stop hook prints nothing with messages pending; `unpark` restores it; a running `wait -timeout 3s` started before `park` does not exit 0 on a new message after `park` (exit 1); MCP `send` after `park` clears it; MCP `receive` does not; `unpark` with no state file prints nothing and exits 0; `SetParked` keeps every other field; `init -global -dry-run` lists the UserPromptSubmit hook for all three harnesses.
- [ ] Implement, docs, checks; commit `feat(cli): park and unpark (decision 18)`.

State (Definition of Done D): `park` twice and `unpark` twice are no-ops that succeed; `register` (MCP) clears a park left by a dead session; a crash between re-read and replace leaves the previous file (atomic replace).
