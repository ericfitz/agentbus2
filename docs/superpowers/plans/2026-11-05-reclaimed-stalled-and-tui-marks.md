# Reclaimed-Stalled Mark and TUI Task Marks Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A task the abandoned-task reclaim returned to `pending` keeps a `reclaimed_from`/`reclaimed_at` mark until it is claimed, the Slack canvas shows it as stalled, and the TUI's task marks mirror the canvas states (pending, in progress, done, blocked, stalled or problem, needs human) with the canvas precedence.

**Architecture:** The mark is two fields of the task document (`internal/core/tasks/doc.go taskDoc`) that `reclaimAbandoned` writes and `applyPatch` clears when the status leaves `pending`; every reader gets it through `bus.Task` and `bus.TaskSummary` (`loadTasks`, `summaries`, `Snapshot`), the wire (`api/openapi.yaml`, `internal/server/tasks.go`, `internal/bus/remote/convert.go`) and the renderers (`internal/slackbridge/canvasrender.go`, `internal/tui/tasks.go`). The TUI computes its marks client-side from `bus.TaskSummary`, its clock and the agent status round it already holds (`Model.agents`), in a pure helper tested without a model.

**Tech Stack:** Go (`CGO_ENABLED=0`), oapi-codegen through `make generate`, SQLite and Postgres through `internal/conformance/bus` (testcontainers), Bubble Tea v2 and Lip Gloss v2 in `internal/tui`.

**Spec:** `docs/superpowers/specs/2026-11-04-slack-scope-canvas-design.md` (sections 2.1, 4.1 to 4.5, 5, 9) and ADR 0009 `docs/adr/0009-scopes-and-slack-bridge.md`, "Decisions of 2026-11-05 (Eric)" (both decisions are Eric's; the TUI decision's glyphs and keys are only there and in `docs/tui.md`).

## Global Constraints

- Work lands directly on `main`; no PRs. One commit per task in the repo's `type(scope): subject` style, ending with the attribution lines the session reminder gives.
- Pre-release (Eric, 2026-10-25): the task document format changes in place; no migration, shim or converter. A document without the two fields parses as unmarked (`omitempty` both ways).
- The protocol version stays `1.0.0`: `api/openapi.yaml` `info.version` and `internal/version.Protocol` are not bumped (Eric, 2026-10-27). Never edit `internal/gen` by hand; run `make generate`.
- Done gate: `make verify` (needs Docker; a run that skips the Postgres suites is not a pass), then `ci.yml` and `windows.yml` green on the pushed commit. Each task's verify step runs its `go test` commands, `go vet` on the packages it touched, `make fmt-check` and `make lint`; Task 6 runs `make verify`. No task pushes: the push and the CI check wait for Eric's word.
- `TestAccessRuleSites` (`internal/core/access/sites_test.go`) counts every `Owner` selector in non-test code under `internal/core`, `internal/server`, `internal/slackbridge` and `internal/bus/local`. `ReclaimedFrom` and `ReclaimedAt` are not counted selectors, so the only count that moves is `reclaim.go reclaimAbandoned` (`{"Owner": 1}` to `{"Owner": 2}`: ADR 0009 decision 1 of 2026-11-05 records Eric's acceptance). No new struct field may be named `Owner`, `Scope`, `Admin` or `Creator`. `make check-gates-selftest` is CI's job (minutes); the tasks run `go test ./internal/core/access/ -run TestAccessRuleSites`.
- Inspection (ADR 0013) is untouched: `taskPrepared` and `contentChanged` cover subject, description and metadata only; the probe path (`planUpdate(probe=true)`) skips the reclaim as it does today. Do not widen the digest.
- The reclaim stays a system write: type `reclaimed`, no sender, `store.IdentityAgent`, not rate-charged, outside inspection (`docs/superpowers/specs/2026-10-27-write-inspection-design.md`).
- Import rules unchanged: `internal/tui` imports only `internal/bus` and `internal/version` of this module (`scripts/m8-exit-checks.sh --deps-only`); `internal/bus/remote` never imports `internal/gen/spec`; the m4, m6 and m10 rules hold.
- Glyphs. Canvas (unchanged): `⚠︎` U+26A0 U+FE0E. TUI emoji set: `📥` U+1F4E5 inbox tray (Eric's change of 2026-11-05 from `⬜`; Emoji_Presentation=Yes, no Terminal.app variant), `⏱️` U+23F1 U+FE0F (kept, with its variant), `✅` U+2705 (kept), `⛔` U+26D4 U+FE0F (Emoji_Presentation=Yes), `⚠️` U+26A0 U+FE0F (Emoji_Presentation=No: Terminal.app variant with `narrowVS16Pad`), `🙋` U+1F64B U+FE0F. Every TUI icon string ends with one space, as today.
- Times on the canvas are `HH:MM UTC` from `time.UnixMilli(ms).UTC().Format("15:04")`, as `stalledText` does today.
- Never log a task subject or owner from the reclaim beyond what `reclaim.go` logs today (nothing).

## Design decisions made by this plan (not in the brief; Eric may overrule)

1. **`bus.TaskSummary` carries the mark too.** The brief names `bus.Task`; the TUI draws from `List`'s summaries and the MCP `task_list` is the agent's view of a list, so without it decision 2 cannot show the reclaimed mark. Both schemas gain the fields.
2. **An owner assignment that keeps `pending` leaves the mark** (the brief's rule, applied literally: the mark clears when the status leaves `pending`). The canvas then reads `⚠︎ #id subject · <assignee> · stalled: reclaimed from <old> HH:MM UTC` until the assignee claims. Listed as an open question in the hand-back.
3. **TUI stalled rule** mirrors `abandonedSet`: an `in_progress` task whose lease has passed by the client's clock, or whose owner has no entry with `Live` in `Model.agents` (the server's `AgentStatuses` lists every identity with a live session, status or not, so an absent owner is not live), or a `pending` task with the mark. A `pending` task with an offline assignee and no mark is not stalled. `Model.agents` is the last 5 s status round; a stall or recovery shows within one round.
4. **TUI suffixes.** Today's dim suffixes stay (`blocked by #a #b`, `lease 4m`, `lease expired`, `→ owner` for an assigned pending task). New: ` reclaimed from <owner>` (dim) on a marked task; ` offline` (dim) on an in_progress task whose owner is not live; ` needs human` and the problem text are not repeated on the row (the agents board shows them) beyond the mark. The expanded detail block (`renderTaskBlock`) gets one dim line `reclaimed from <owner> · <stamp(ReclaimedAt)>` when the task is marked.
5. **Nerd-font glyphs**: `task_blocked` U+F05E (fa-ban, as `status_blocked`), `task_stalled` U+F071 (fa-triangle-exclamation), `task_needs_human` U+F256 (fa-hand, as `status_needs_human`).

## Review Focus

1. A task the job reclaimed and an agent then edits (subject only) or re-parents: the mark must survive the write, and the `tasks list` row must still carry it. Test: Task 1 (`TestTaskReclaimedMarkPersists`, the edit and move cases).
2. A reclaimed task that is then released by a `task_release` from a non-owner, or updated with `status: pending`: no transition out of `pending`, so the mark stays; nothing may crash on an empty owner. Test: Task 1 (`TestTaskReclaimedMarkPersists`, the pending-to-pending case).
3. A reclaimed task with an open blocker: the tree shows `⛔︎` and the task is listed under both Blocked and Stalled; the TUI shows `⛔`. Tests: Task 3 (golden `reclaimed.md`), Task 5 (`TestTaskMarkPrecedence`).
4. A mark whose `reclaimed_at` is 0 but `reclaimed_from` is set (a hand-edited document): readers treat the task as marked (`ReclaimedFrom != ""` is the test) and the canvas prints the suffix without a time. Test: Task 3 (golden `reclaimed.md`, task with `ReclaimedAt` zero).
5. The TUI in Terminal.app: `⚠️` advances one column, so a stalled row without the U+200B variant would shift the rest of the row left. Test: Task 4 (`TestLoadIconsAppleTerminal` covers `task_stalled`).

---

### Task 1: The reclaimed mark in the task document, the reclaim and the patch

**Files:**
- Modify: `internal/bus/tasks.go:7-37` (`Task`, `TaskSummary`)
- Modify: `internal/core/tasks/doc.go:29-51` (`taskDoc`, `taskContent`), `:97-123` (`loadTasks`)
- Modify: `internal/core/tasks/reclaim.go:74-80` (`reclaimAbandoned` loop)
- Modify: `internal/core/tasks/update.go:119-148` (`applyPatch` Rule 7)
- Modify: `internal/core/tasks/read.go:132-146` (`summaries`)
- Modify: `internal/core/tasks/snapshot.go:14-25,44-50` (`SnapshotTask`, `Snapshot`)
- Modify: `internal/core/access/sites_test.go:111`
- Test: `internal/core/tasks/doc_test.go` (`TestApplyPatch` cases, new `TestTaskDocReclaimedRoundTrip`), `internal/conformance/bus/tasks_test.go` (new `TestTaskReclaimWritesMark`, `TestTaskReclaimedMarkPersists`, `TestTaskReleaseSetsNoMark`), `internal/conformance/bus/canvas_core_test.go` (`TestTasksSnapshot`: the mark)

**Interfaces:**
- Produces: `bus.Task.ReclaimedFrom string` (json `reclaimed_from,omitempty`), `bus.Task.ReclaimedAt int64` (json `reclaimed_at,omitempty`, Unix ms); the same two fields on `bus.TaskSummary`; `taskDoc.ReclaimedFrom`/`ReclaimedAt` with the same json tags; `tasks.SnapshotTask.ReclaimedFrom string`, `tasks.SnapshotTask.ReclaimedAt int64`.
- Rule: `reclaimAbandoned` sets `ReclaimedFrom = t.Owner` (before clearing it) and `ReclaimedAt = now` on every task it reclaims (a corrupt in_progress task with an empty owner gets `ReclaimedFrom == ""` and `ReclaimedAt == now`); `applyPatch` sets both to their zero values whenever `patched.Status != "pending"` after Rule 7, before the Rule 11 no-op check.

- [ ] **Step 1: Write the failing tests**

`doc_test.go`, `TestTaskDocReclaimedRoundTrip`: `taskContent(bus.Task{Subject: "s", Status: "pending", Rank: "m", ReclaimedFrom: "pat", ReclaimedAt: 1_700_000_000_000})` contains `"reclaimed_from":"pat"` and `"reclaimed_at":1700000000000`; with both zero the output contains neither key. Add to `TestApplyPatch` (its table) a `cur` with `Status: "pending", ReclaimedFrom: "pat", ReclaimedAt: 5`: patch `{Subject: "new"}` keeps both; patch `{Owner: "sam"}` keeps both; patch `{Status: "in_progress", Owner: "sam"}` clears both; patch `{Status: "completed"}` clears both.

`tasks_test.go`, `TestTaskReclaimWritesMark` (the shape of `TestTaskAbandonedByLease`: `h.taskList("work", "pat")`, pat claims with a 1 s lease, `h.clock.Advance(2 s)`, then `h.runJob("abandoned tasks")` returns 1): the live revision's `Content` (`h.taskRevisions(id)`, the row with the highest `*Revision` and `!Tombstone`) contains `"reclaimed_from":"pat"` and `"reclaimed_at":` followed by the digits of `h.core.NowMs()` at the job's run; `h.runJob("abandoned tasks")` a second time returns 0 and the live revision is unchanged (Definition of Done D: a second run writes nothing, so the mark always names the latest owner). This test compiles and fails before the change (no key in the content).

`TestTaskReclaimedMarkPersists`: after the same reclaim, as `alice` (a writer with a live session): `Update{Subject: "renamed"}` keeps the mark in the live content; `Update{Parent: &root}` keeps it; `Update{Owner: "sam"}` (sam registered, status still pending) keeps it; `Release` by alice (pending to pending) keeps it; then `Claim` by sam clears it (no `reclaimed_from` key in the live content) and `tasks.Service{Core: h.core}.Snapshot` reports `ReclaimedFrom == ""`. A separate task: reclaimed, then `Update{Status: "completed"}` clears it.

`TestTaskReleaseSetsNoMark`: pat claims (live session, no lease) then `Release`s: the live content has no `reclaimed_from` key and no `reclaimed` revision exists.

`canvas_core_test.go` `TestTasksSnapshot`: after the existing assertions, `h.runJob("abandoned tasks")` reclaims `id2` and `id3`; a second `Snapshot` reports `by[id2].ReclaimedFrom == "bob"`, `by[id2].ReclaimedAt == h.core.NowMs()` at the job, `!by[id2].Abandoned`, `by[id2].Status == "pending"`, `by[id2].Assignee == ""`; `by[id3].ReclaimedFrom == "carl"`.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/core/tasks/ -run 'TestTaskDocReclaimedRoundTrip|TestApplyPatch' -count=1; go test ./internal/conformance/bus/ -run 'TestTaskReclaimWritesMark' -count=1`
Expected: the unit tests fail to compile (unknown fields); `TestTaskReclaimWritesMark` FAILs on both engines with the content lacking `reclaimed_from`.

- [ ] **Step 3: Implement**

`bus/tasks.go`: the two fields on `Task` (after `Metadata`) and on `TaskSummary` (after `LeasedUntil`). `doc.go`: the two fields on `taskDoc`, copied in `taskContent` and `loadTasks`. `reclaim.go`: in the reclaim loop `t.ReclaimedFrom, t.ReclaimedAt = t.Owner, now` before `t.Status, t.Owner, t.LeasedUntil = "pending", "", 0`. `update.go`: outside the `if p.Status != nil` block, immediately before the "An in_progress task always needs an owner" check, `if patched.Status != "pending" { patched.ReclaimedFrom, patched.ReclaimedAt = "", 0 }`, so a marked document that is not pending (hand-edited) clears on any write. `read.go summaries`: copy both. `snapshot.go`: the two `SnapshotTask` fields, copied from `t`. `sites_test.go:111`: `{"IdentityKind": 1, "Owner": 2}` with the comment extended: "and the owner it copies into ReclaimedFrom".

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/core/tasks/ ./internal/core/access/ -count=1 && go test ./internal/conformance/bus/ -run 'TestTask|TestTasksSnapshot|TestMaintenanceAbandonedTasks' -count=1 && AGENTMESH_BUS_TRANSPORT=inproc go test ./internal/conformance/bus/ -run 'TestTask|TestMaintenanceAbandonedTasks' -count=1 && go vet ./internal/bus/ ./internal/core/... && make fmt-check && make lint`
Expected: PASS on both engines and both transports (the new tests read raw revision content and the core `Snapshot`, not the wire, so `inproc` passes before Task 2).

- [ ] **Step 5: Commit**

```bash
git add internal/bus/tasks.go internal/core/tasks internal/core/access/sites_test.go internal/conformance/bus/tasks_test.go internal/conformance/bus/canvas_core_test.go
git commit -m "feat(tasks): keep a reclaimed_from/reclaimed_at mark on a reclaimed task until it is claimed"
```

---

### Task 2: The mark on the wire, the clients and the agent-facing text

**Files:**
- Modify: `api/openapi.yaml:1819-1895` (`Task`, `TaskSummary` schemas)
- Regenerate: `internal/gen`, `internal/gen/spec` (`make generate`)
- Modify: `internal/server/tasks.go:24-37` (`taskToGen`), `:66-88` (`ListTasks` summary copy)
- Modify: `internal/bus/remote/convert.go:108-143` (the four task converters)
- Modify: `internal/mcpserver/server.go:819,842,846` (tool descriptions), `internal/cli/skills/using-agentmesh/SKILL.md:164-169` (item 4)
- Test: `internal/conformance/bus/tasks_test.go` (`TestTaskReclaimWritesMark`: Bus-level assertions), `internal/mcpserver/server_test.go:315-325` (the description word check: the new sentences must contain none of its banned words)

**Interfaces:**
- Produces: `gen.Task.ReclaimedFrom *string`, `gen.Task.ReclaimedAt *int64`; the same on `gen.TaskSummary` (names as oapi-codegen derives them from `reclaimed_from` and `reclaimed_at`: `type: string` and `type: integer, format: int64`, not required).

- [ ] **Step 1: Write the failing tests**

Extend `TestTaskReclaimWritesMark`: after the job, `h.as("alice").GetTask(ctx, id)` returns `ReclaimedFrom == "pat"` and `ReclaimedAt` equal to the job's `NowMs`; `h.as("alice").List(ctx, TaskListInput{Channel: "work"})` has the row for `id` with the same two values; after sam's `Claim` both are zero on `GetTask` and `List`. These assertions pass on the `local` transport after Task 1 and fail on `inproc` until the converters copy the fields.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `AGENTMESH_BUS_TRANSPORT=inproc go test ./internal/conformance/bus/ -run TestTaskReclaimWritesMark -count=1`
Expected: FAIL (`ReclaimedFrom` empty over the generated client).

- [ ] **Step 3: Implement**

`openapi.yaml`: under `Task.properties` after `leased_until`: `reclaimed_from: {type: string, description: "the owner the abandoned-task reclaim took the task from; present until the task leaves pending"}` and `reclaimed_at: {type: integer, format: int64, description: "when the reclaim happened, unix ms"}`; the same two under `TaskSummary.properties`. `make generate`. `server/tasks.go`: `ReclaimedFrom: optStr(t.ReclaimedFrom)` (`internal/server/convert.go:42`, nil for `""`), `ReclaimedAt: optInt64(t.ReclaimedAt)`, in `taskToGen` and the `ListTasks` summary copy. `remote/convert.go`: `deref` on the way in (`fromGenTask`, `fromGenTaskSummary`) and `strPtr`/`int64Ptr` on the way out (`toGenTask`, `toGenTaskSummary`), `convert.go:12-20`. `mcpserver/server.go`: `task_claim` description gains "A task the server reclaimed from an owner whose session or lease ended reads stalled (reclaimed_from, reclaimed_at) until someone claims it."; `task_get` and `task_list` descriptions gain "reclaimed_from and reclaimed_at mark a task the server took back from a vanished owner; a claim clears them." `SKILL.md` item 4 gains one sentence: "A task the server took back carries `reclaimed_from` and `reclaimed_at` and shows as stalled to humans until it is claimed again; claim it or hand it on."

- [ ] **Step 4: Run the tests to verify they pass**

Run: `make generate && make generate-check && go test ./internal/conformance/bus/ -run TestTaskReclaimWritesMark -count=1 && AGENTMESH_BUS_TRANSPORT=inproc go test ./internal/conformance/bus/ -run 'TestTask' -count=1 && go test ./internal/server/... ./internal/bus/remote/ ./internal/mcpserver/ ./internal/e2e/ -count=1 && go vet ./internal/server/... ./internal/bus/remote/ ./internal/mcpserver/ && make fmt-check && make lint && scripts/m4-exit-checks.sh --deps-only`
Expected: PASS; `generate-check` clean.

- [ ] **Step 5: Commit**

```bash
git add api/openapi.yaml internal/gen internal/server/tasks.go internal/bus/remote/convert.go internal/mcpserver/server.go internal/cli/skills/using-agentmesh/SKILL.md internal/conformance/bus/tasks_test.go
git commit -m "feat(api): carry reclaimed_from and reclaimed_at on Task and TaskSummary"
```

---

### Task 3: The canvas shows a reclaimed task as stalled (the bug reproduction)

**Files:**
- Modify: `internal/slackbridge/canvas_snapshot.go:34-45` (`CanvasTask`), `:140-152` (the `SnapshotTask` copy)
- Modify: `internal/slackbridge/canvasrender.go:167-210` (`viewOf`, `stalledText`), `:263-300` (`attentionBlocks` Stalled line)
- Test: `internal/slackbridge/canvasrender_test.go` (`canvasCases`: new case `reclaimed`, the `tree` and `attention` cases gain a reclaimed task), `internal/slackbridge/testdata/canvas/{reclaimed,tree,attention}.md`, `internal/conformance/bus/slackbridge_canvas_test.go:806-870` (`TestSlackBridgeCanvasRunner`, new subtest `maintenance-reclaim-shows-stalled`), `:128-215` (`TestCanvasSnapshotTasksAndStatuses`: the mark reaches `CanvasTask`)

**Interfaces:**
- Consumes: `tasks.SnapshotTask.ReclaimedFrom`, `.ReclaimedAt` (Task 1).
- Produces: `slackbridge.CanvasTask.ReclaimedFrom string`, `slackbridge.CanvasTask.ReclaimedAt time.Time` (zero when unset). Rule: `viewOf` sets `stalled` when `t.Stalled || t.ReclaimedFrom != ""`; `stalledText` returns, in order, `"lease expired HH:MM UTC"` when `LeaseExpiredAt` is set, `"reclaimed from <escaped owner> HH:MM UTC"` when `ReclaimedFrom` is set (without the time when `ReclaimedAt` is zero), else `"owner offline"`. The Stalled subsection line for a reclaimed task is `- ⚠︎ #id subject · reclaimed from <owner> HH:MM UTC · <list>` (the owner segment is absent because `Assignee` is empty; `attentionBlocks` already omits an empty assignee).

- [ ] **Step 1: Write the failing tests**

`slackbridge_canvas_test.go`, subtest `maintenance-reclaim-shows-stalled` next to `lifecycle-catches-lease-expiry`: the same fixture and claim with a 1 s lease; `waitFor` the claimed task; `f.h.clock.Advance(2 s)`; `f.h.runJob("abandoned tasks")` returns 1 (the reclaim before any canvas pass sees the expired lease, the production sequence); `waitFor(t, 10*time.Second, "the reclaimed mark", canvasMarkdownHas(f, "stalled: reclaimed from job1-coder"))`; then the markdown contains `"### ⚠︎ Stalled"` and a line starting `"- ⚠︎ #"` that contains `"reclaimed from job1-coder"` and `" · job1-tasks"`, and does not contain `"owner offline"`. **This is the reproduction:** it compiles today and fails today (the job clears the owner and lease, `Snapshot` reports neither `Abandoned` nor a mark, and the task renders `◌`).

`canvasrender_test.go`: case `reclaimed`: list `work` with root `#1 Reclaimed root` pending, `ReclaimedFrom: "claude-d"`, `ReclaimedAt: 2026-11-04T19:02:00Z`; child `#2 Blocked and reclaimed` pending, `OpenBlockers: []int64{3}`, `ReclaimedFrom: "claude-e"`, same time; `#3 Blocker` in_progress owner `claude-a`; `#4 No time` pending `ReclaimedFrom: "claude-f"`, `ReclaimedAt` zero. The `tree` case gains one pending leaf with the mark; the `attention` case gains one marked task so the Stalled subsection shows both forms. Goldens regenerated with `-update` and read: `#1` line `### ⚠︎ #1 Reclaimed root · stalled: reclaimed from claude-d 19:02 UTC`; `#2` line `- [ ] ⛔︎ #2 Blocked and reclaimed · blocked by #3`; `#4` line `- [ ] ⚠︎ #4 No time · stalled: reclaimed from claude-f`; Needs attention lists `#2` under Blocked and under Stalled (`- ⚠︎ #2 Blocked and reclaimed · reclaimed from claude-e 19:02 UTC · work`); the count line reads `▶ 1 · ⛔︎ 1 · ⚠︎ 2`.

`TestCanvasSnapshotTasksAndStatuses` (`:183`): after `f.h.runJob("abandoned tasks")`, the `CanvasTask` for the lease-expired task has `ReclaimedFrom == "job1-b"`, `ReclaimedAt.Equal(time.UnixMilli(jobNow).UTC())`, `!Stalled`, `Assignee == ""`.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/conformance/bus/ -run 'TestSlackBridgeCanvasRunner/.*/maintenance-reclaim-shows-stalled' -count=1` and `go test ./internal/slackbridge/ -run TestRenderCanvasGolden -count=1`
Expected: the bridge subtest FAILs on both engines waiting for `stalled: reclaimed from`; the renderer test fails to compile (unknown fields).

- [ ] **Step 3: Implement**

`canvas_snapshot.go`: the two `CanvasTask` fields; in the copy `ReclaimedFrom: t.ReclaimedFrom` and `if t.ReclaimedAt != 0 { ct.ReclaimedAt = time.UnixMilli(t.ReclaimedAt).UTC() }`. `canvasrender.go`: `viewOf` `stalled: t.Stalled || t.ReclaimedFrom != ""`; `stalledText` as the Interfaces block orders it. `attentionBlocks` (`:292-294`) already builds the Stalled line as `base+owner+canvasSep+stalledText(t)` with `owner` empty for an empty `Assignee`, so it needs no change. Regenerate the goldens: `go test ./internal/slackbridge -run TestRenderCanvasGolden -update`, then read the three diffs against Step 1's expected lines before committing.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/slackbridge/ -count=1 && go test ./internal/conformance/bus/ -run 'TestSlackBridgeCanvasRunner|TestTasksSnapshot|TestCanvasSnapshot' -count=1 && go vet ./internal/slackbridge/ && make fmt-check && make lint && scripts/m7-exit-checks.sh --deps-only`
Expected: PASS on both engines; `lifecycle-catches-lease-expiry` still passes (that fixture runs no maintenance loop, so its task stays abandoned, not reclaimed).

- [ ] **Step 5: Commit**

```bash
git add internal/slackbridge/canvas_snapshot.go internal/slackbridge/canvasrender.go internal/slackbridge/canvasrender_test.go internal/slackbridge/testdata/canvas internal/conformance/bus/slackbridge_canvas_test.go
git commit -m "fix(slackbridge): show a reclaimed task as stalled on the canvas until it is claimed"
```

---

### Task 4: TUI icons for the new task marks

**Files:**
- Modify: `internal/tui/icons.go:43-49` (task marks), `:72-75` (`appleTerminalIcons`), `:95-103` (`iconVars`), `:116-140` (`nerdIcons`)
- Modify: `internal/tui/termapp_test.go:45-50` (the `appleTerminalWidth` comment names the clusters measured)
- Modify: `docs/tui.md:58` (the Terminal.app paragraph), `:231-233` (the icon table)
- Test: `internal/tui/icons_test.go:163-203` (`TestLoadIconsForAppleTerminal`), `:147` (`TestIconSetsCoverTheSameNames`, which already requires every name in both sets), `internal/tui/tasks_test.go:531-556` (`TestTaskChannelNeverShowsMessageHeaders`, the task-row prefix set at `:548`)

**Interfaces:**
- Produces: package vars `taskPending = "\U0001F4E5\uFE0F "` (inbox tray), `taskBlocked = "\u26D4\uFE0F "`, `taskStalled = warningIcon` with `const warningIcon = "\u26A0\uFE0F "`, `taskNeedsHuman = "\U0001F64B\uFE0F "`; `taskInProgress` and `taskCompleted` unchanged. `iconVars` keys `task_blocked`, `task_stalled`, `task_needs_human`. `appleTerminalIcons["task_stalled"] = "\u26A0\uFE0F" + narrowVS16Pad + " "`. `nerdIcons`: `task_blocked` `"\uf05e"`, `task_stalled` `"\uf071"`, `task_needs_human` `"\uf256"`. (Go source escapes, as `icons.go` writes them.)

- [ ] **Step 1: Write the failing tests**

`icons_test.go`: the Terminal.app assertion at `:170` also requires `taskStalled == "\u26A0\uFE0F\u200b "`; the plain-env assertion at `:189` also requires `taskStalled == warningIcon`; the icon_map-wins assertion maps `task_stalled` to `"S"` and expects `taskStalled == "S  "`; the nerdfont assertion requires `!strings.Contains(taskStalled, "\u200b")` and `taskBlocked == padIcon("task_blocked", "\uf05e")`. A new `TestTaskIconKeysKnown`: every key `task_pending`, `task_in_progress`, `task_completed`, `task_blocked`, `task_stalled`, `task_needs_human` is in `emojiIcons` and `nerdIcons`, and `emojiIcons["task_pending"] == "\U0001F4E5\uFE0F "`. `tasks_test.go:548`: the allowed prefixes become the six marks (a helper `taskMarks() []string` in the test file).

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/tui/ -run 'TestLoadIconsForAppleTerminal|TestIconSetsCoverTheSameNames|TestTaskIconKeysKnown|TestTaskChannelNeverShowsMessageHeaders' -count=1`
Expected: compile failure on the new vars.

- [ ] **Step 3: Implement**

As the Interfaces block says; update the comment above the task marks (U+1F4E5 and U+26D4 are Emoji_Presentation=Yes, U+26A0 is No and takes the Terminal.app variant). `termapp_test.go:45-50` comment only (`appleTerminalWidth` is `WcWidth` per rune, not a rune table): replace `❎` with `📥` and add `⚠️` to the one-column list. `docs/tui.md:58`: add `task_stalled` (`"\u26a0\ufe0f\u200b"`) to the icon_map hint for a remote TUI viewed in Terminal.app; `:231-233` table: `task_pending` row text "a pending task (emoji U+1F4E5)", add rows `task_blocked` "a task with an open blocker (emoji U+26D4) | U+F05E", `task_stalled` "a stalled task, or one whose owner reports a problem (emoji U+26A0) | U+F071", `task_needs_human` "a task whose owner's status tag is `needs-human` (emoji U+1F64B) | U+F256".

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/tui/ -count=1 && go vet ./internal/tui/ && make fmt-check && make lint && scripts/m8-exit-checks.sh --deps-only`
Expected: PASS (Task 5 wires the marks into rows; this task only defines them, so the row tests still see `taskPending` on pending rows).

- [ ] **Step 5: Commit**

```bash
git add internal/tui/icons.go internal/tui/icons_test.go internal/tui/termapp_test.go internal/tui/tasks_test.go docs/tui.md
git commit -m "feat(tui): add the blocked, stalled and needs-human task marks to the icon sets"
```

---

### Task 5: TUI task rows and detail block use the canvas states

**Files:**
- Create: `internal/tui/taskmark.go`
- Modify: `internal/tui/tasks.go:464-516` (`renderTasks` mark and suffix), `:558-600` (`renderTaskBlock`)
- Modify: `docs/tui.md:133` (the Task lists section)
- Test: create `internal/tui/taskmark_test.go`; modify `internal/tui/tasks_test.go` (row rendering with statuses and the mark)

**Interfaces:**
- Consumes: `bus.TaskSummary.ReclaimedFrom/ReclaimedAt`, `bus.Task.ReclaimedFrom/ReclaimedAt` (Task 1), `Model.agents []bus.AgentStatusEntry` (`model.go:98`), the icon vars of Task 4.
- Produces, in `taskmark.go`:
  - `type taskState int` with constants `statePending, stateInProgress, stateDone, stateBlocked, stateStalled, stateNeedsHuman, stateProblem`.
  - `func taskStateOf(t bus.TaskSummary, agents []bus.AgentStatusEntry, now time.Time) (st taskState, suffix string)`: `completed` is `stateDone`; otherwise first match wins: `len(OpenBlockers) > 0` → `stateBlocked`; owner's live entry has `Status.Tag == bus.StatusNeedsHuman` → `stateNeedsHuman`; stalled (`ReclaimedFrom != ""`, or `in_progress` with `LeasedUntil != 0 && LeasedUntil <= now.UnixMilli()`, or `in_progress` with no live entry for the owner) → `stateStalled`; owner's live entry has `Status.Problem != ""` → `stateProblem`; `in_progress` → `stateInProgress`; else `statePending`. `suffix` is the dim text: `" reclaimed from <owner>"` when marked, `" offline"` for the no-live-owner stall, `""` otherwise (the lease and blocked suffixes stay where `renderTasks` builds them today).
  - `func taskMark(st taskState) string`: `statePending → taskPending`, `stateInProgress → taskInProgress`, `stateDone → taskCompleted`, `stateBlocked → taskBlocked`, `stateStalled, stateProblem → taskStalled`, `stateNeedsHuman → taskNeedsHuman`.

- [ ] **Step 1: Write the failing tests**

`taskmark_test.go`, `TestTaskStatePrecedence` (table, `now` fixed, `agents` holding `sam` live with tag `implementing`, `pat` live with tag `needs-human`, `ann` live with `Problem: "disk full"`, `bob` offline (`Live: false`)): completed with every other condition → `stateDone`; pending with `OpenBlockers` and `ReclaimedFrom` → `stateBlocked`; in_progress owner `pat` with an expired lease → `stateNeedsHuman`; in_progress owner `sam` lease expired → `stateStalled`, suffix `""`; in_progress owner `bob` → `stateStalled`, suffix `" offline"`; in_progress owner `nobody` (no entry) → `stateStalled`; pending `ReclaimedFrom: "sam"` → `stateStalled`, suffix `" reclaimed from sam"`; in_progress owner `ann` live lease → `stateProblem`; in_progress owner `sam` live lease → `stateInProgress`; pending assigned to `bob` (offline, no mark) → `statePending`. `TestTaskMarkGlyphs`: each state maps to its var.

`tasks_test.go`: a new `TestTaskRowMarksFollowStates` in the style of `TestTaskChannelRendersTree` (`:20-67`) and `TestTaskRowSuffixes` (`:474`): with `f.agentWithStatus(t, "pat", true, statusSet{tag: "needs-human", text: "which?"})` and a task claimed by pat, the row starts with `taskNeedsHuman`; with the summary in `f.m.tasks[ch]` patched to `Status: "pending", Owner: "", ReclaimedFrom: "pat", ReclaimedAt: ms` (as `agents_test.go:232` patches `m.agents`; no maintenance plumbing in a TUI test), the row starts with `taskStalled` and contains `reclaimed from pat`; a blocked pending row starts with `taskBlocked` and still contains `blocked by #`. Detail block: with `f.m.taskDetail[id]` holding `ReclaimedFrom: "pat", ReclaimedAt: ms`, the rendered block contains `reclaimed from pat · ` + `stamp(ms)`.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/tui/ -run 'TestTaskStatePrecedence|TestTaskMarkGlyphs|TestTaskRowMarksFollowStates' -count=1`
Expected: compile failure (`taskStateOf` undefined), row tests fail on `taskPending` prefixes.

- [ ] **Step 3: Implement**

`taskmark.go` as the Interfaces block; `time.Now()` is passed by the caller, never read inside. `renderTasks`: replace the `switch t.Status` mark block with `st, extra := taskStateOf(t, m.agents, time.Now()); mark := taskMark(st)`; append `extra` to `suffix` before the blockers text; keep the owner rendering, the `lease`/`lease expired` and `blocked by` suffixes as they are. `renderTaskBlock`: after the metadata lines and before the `updated by` line, `if detail.ReclaimedFrom != "" { lines = append(lines, dim.Render("reclaimed from "+detail.ReclaimedFrom+" · "+stamp(detail.ReclaimedAt))) }` (omit the ` · stamp` when `ReclaimedAt` is 0). `docs/tui.md` Task lists section: a sentence and a table: "Each row's mark follows the Slack canvas states with the same precedence: `📥` pending, `⏱️` in progress, `✅` done, `⛔` an open blocker, `🙋` the owner's status tag is `needs-human`, `⚠️` stalled (the lease has passed, the owner has no live session, or the server reclaimed the task and no one has claimed it since: the row reads `reclaimed from <owner>`) or the owner's status reports a problem. The TUI judges stalls by its own clock and the last agent-status round (5 s)."

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/tui/ -count=1 && go vet ./internal/tui/ && make fmt-check && make lint && scripts/m8-exit-checks.sh --deps-only`
Expected: PASS; the m8 import allowlist unchanged.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/taskmark.go internal/tui/taskmark_test.go internal/tui/tasks.go internal/tui/tasks_test.go docs/tui.md
git commit -m "feat(tui): task marks mirror the canvas states (blocked, stalled, needs human, problem)"
```

---

### Task 6: Done gate and the Definition of Done B sweep

**Files:**
- Test: everything (`make verify`)
- Modify: `CLAUDE.md` Design section, a new bullet after the "Agent status (ADR 0014 ...)" bullet: "- Reclaimed tasks (ADR 0009, decisions of 2026-11-05): the abandoned-task reclaim writes `reclaimed_from` and `reclaimed_at` into the task document (`internal/core/tasks/reclaim.go`); they stay until the status leaves `pending` (`applyPatch`); `bus.Task`, `bus.TaskSummary` and the wire carry them; the canvas and the TUI show such a task as stalled."

- [ ] **Step 1: Sweep the other readers of task documents and record the result in the commit message**

`rg -n 'leased_until|open_blockers' internal --glob '!internal/gen/**' -l` and `rg -n 'bus\.Task\b|TaskSummary' internal --glob '!*_test.go' -l`. Expected findings, each already handled or needing nothing: `internal/core/tasks` (Task 1), `internal/server/tasks.go` and `internal/bus/remote/convert.go` (Task 2), `internal/mcpserver` (JSON passthrough of the bus types; descriptions in Task 2), `internal/cli/tasks.go` (prints the bus type as JSON; no code), `internal/tui` (Task 5), `internal/slackbridge` (Task 3), `internal/core/search/search.go:142` and channel `history` (raw revisions: the `reclaimed` revision type was already visible; the new keys appear in the raw content; no change). Anything else found is fixed in this task or listed in the hand-back.

- [ ] **Step 2: Run the done gate**

Run: `make verify`
Expected: PASS, with the Postgres suites run (not skipped) and `check-gates` green; `TestAccessRuleSites` passes with the one count change of Task 1.

- [ ] **Step 3: Commit the CLAUDE.md line and anything the sweep changed, then report**

```bash
git add CLAUDE.md
git add -p
git commit -m "docs: note the reclaimed-task mark in CLAUDE.md; definition-of-done sweep"
```

Report to Eric: the `make verify` output tail, the commit list, and the open questions from the plan's "Design decisions" section. The push and the `ci.yml`/`windows.yml` check wait for his word.

---

## Spec coverage

| Spec item | Task |
|---|---|
| 2.1 decision 1: the mark written, carried, cleared; release sets none; `Task`/`TaskSummary` fields; schemas | 1, 2 |
| 2.1 decision 2 and 4.2/4.3/4.5: canvas tree suffix, Stalled line, precedence | 3 |
| 2.1 decision 3: TUI marks mirror the states | 4, 5 |
| 5 step 4: the snapshot reports the mark | 1 (core), 3 (bridge) |
| 9 "Reclaimed mark" tests: job writes and second run writes nothing; `Snapshot`/`Get`/`List` report; edit carries, claim clears, release none; canvas repro; goldens | 1, 2, 3 |
| ADR 0009 decision 1: allowlist count `reclaimAbandoned` `{"Owner": 2}` | 1 |
| ADR 0009 decision 2: icon keys, Terminal.app variant, nerd set, `docs/tui.md` | 4, 5 |
| Done gate `make verify`, CI | 6 |
