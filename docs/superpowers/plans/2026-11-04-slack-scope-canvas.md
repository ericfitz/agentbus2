# Slack Scope Canvas Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Each live Slack bridge's channel carries a read-only channel canvas titled `Status` showing the scope's task lists and memory channels that every allowed human may read, rebuilt (coalesced, default 30 s) whenever a task or memory in the scope changes.

**Architecture:** A read-only snapshot (one `Store.Read` per bridge: `Access.Holders` for the visibility rule, a new non-reclaiming `tasks.Service.Snapshot`, `tx.LiveMemories`, a new `statuses.Service.LiveStatuses`) feeds a pure renderer (`RenderCanvas`, golden-tested) whose markdown a publisher sends through five new `slackapi.Client` methods (find-by-marker, create, access, replace) from a fifth goroutine of the bridge runner session, so only the Store-lock holder publishes. Nothing from Slack reaches the bus.

**Tech Stack:** Go (`CGO_ENABLED=0`), slack-go v0.34.0 (Web API; bumped in Task 2), `internal/slackapi` port with `slackfake`, SQLite and Postgres through `internal/conformance/bus` (testcontainers), `crypto/sha256`.

**Spec:** `docs/superpowers/specs/2026-11-04-slack-scope-canvas-design.md` (decisions in `docs/adr/0009-scopes-and-slack-bridge.md`, "Decisions of 2026-11-04 (Eric): the scope canvas").

## Global Constraints

- Work lands directly on `main`; no PRs. One commit per task, message in the repo's `type(scope): subject` style.
- Pre-release (Eric, 2026-10-25): no migrations, compatibility shims or deprecation paths; no schema change is needed here at all.
- American English in code, comments, docs and commit messages.
- Done gate: `make verify` (needs Docker; a run that skips the Postgres suites is not a pass), then `ci.yml` and `windows.yml` green on the pushed commit. Each task runs the task's own `go test` commands plus `make lint`, `make vet`, `make fmt-check`; the last task runs `make verify`.
- Slack import rules (`scripts/m7-exit-checks.sh --deps-only`): `internal/slackapi` and `slackfake` import only the standard library (and `internal/slackapi`); slack-go is imported only by `internal/slackapi/slackgo`; `internal/slackbridge` never imports `internal/server`, `internal/gen`, `slackgo` or slack-go; `internal/core/...`, `internal/bus/...`, `internal/cli`, `internal/mcpserver`, `internal/client` and `internal/server` never import `internal/slackapi` or `internal/slackbridge`. The m10 dependency rule (`scripts/m10-exit-checks.sh --deps-only`) is unaffected: nothing here is imported by `internal/mcpcli`.
- `TestAccessRuleSites` (`internal/core/access/sites_test.go`) counts, in non-test code under `internal/core`, `internal/server`, `internal/slackbridge` and `internal/bus/local`, every selector named `Admin`, `Creator`, `Owner` or `Scope`, and every `store.IdentityHuman`, `store.IdentityAgent` and `store.KindMemory`. No new struct field may be named `Owner` or `Scope` (use `Assignee` and `ScopeName`); the tasks-or-memory filter is `ch.Kind != store.KindOrdinary` (then `== store.KindTasks`), exactly as `drainOne` does; `tx.Scope(ctx, name)` and `tx.ScopeMembers(ctx, name)` are method calls and do not count. The one new entry this plan adds to `ruleAllowed` is in Task 3 and is a design change for Eric (see "Design changes" below).
- Never log a token, a message, a task subject, a memory or canvas content. Log lines name the scope, the bus channel, the Slack channel ID, the canvas ID and Slack's error code only.
- Glyphs are font glyphs with U+FE0E on two of them: `◌` U+25CC, `▶` U+25B6, `✔` U+2714, `⛔︎` U+26D4 U+FE0E, `⚠︎` U+26A0 U+FE0E, `⚑` U+2691. The separator is ` · ` (U+00B7 with a space on each side).
- Settings: `slack_canvas_enabled` (default `true`; `true` or `false` only) and `slack_canvas_coalesce_seconds` (default `30`; 5 to 600). Bot scopes `canvases:read` and `canvases:write` are added to the operator doc.
- Size cap: 1,048,576 − 4,096 = 1,044,480 bytes of markdown.

## Design decisions (Eric, 2026-11-04: all nine approved as written below)

1. **Canvas ID discovery (Task 2): bump slack-go to v0.34.0 (Eric's decision).** slack-go v0.30.1's `Tab` is `{ID, Label, Type}`; the canvas file ID lives in `properties.tabs[].data.file_id`, which slack-go models only from v0.34.0 (`Tab.Data *TabData{FileID}`). Task 2 Step 0 runs `go get github.com/slack-go/slack@v0.34.0` (dry run 2026-11-04: one go.mod line, four go.sum lines; build, vet and `go test ./internal/slackapi/...` pass). `ChannelCanvases` uses `api.GetConversationInfoContext(ctx, &slack.GetConversationInfoInput{ChannelID: channelID})` and keeps the tabs with `Type == "canvas"` and `Data != nil && Data.FileID != ""`. There is no raw HTTP call and `slackgo.Client` gains no fields.
2. **One allowlist entry (Task 3), approved by Eric.** Spec 5.3 says the bridge "adds no entry to the `TestAccessRuleSites` allowlist". The bridge itself adds none: it never reads `b.Scope`; the scope of a bridge is derived from the store's membership facts (`ListScopes` then `ScopeMembers`, the scope whose channels include the bridge's channel ID: Task 7 `refresh`) and passed to `CanvasSnapshot` and `PublishCanvas` as a parameter. But the gate also counts kind-N copies in `internal/core`, and the tasks snapshot must copy `t.Owner` into `SnapshotTask.Assignee` once, like `summaries` (allowlisted `{"Owner": 4}`). One entry, exact count, a copy and not a decision: `"internal/core/tasks/snapshot.go Snapshot": {"Owner": 1}`. Zero entries is possible only by hiding the read behind another name, which the gate's own comment calls evasion.
3. **Canvas error mapping (Task 2).** `slackgo.mapErr` collapses `channel_not_found` and `is_archived` into `ErrChannelGone`, but spec 6.4 treats them differently (forget the canvas vs archive the bridge). The five canvas methods use `mapCanvasErr`: every Slack code is `*APIError{Method, Code}` (no sentinel), a 429 is `*RateLimitedError`, an HTTP status is `http_NNN`, a transport error is wrapped. `slackfake` returns the same shapes.
4. **Unknown Slack code on a canvas call (Task 6).** Not in spec 6.4. Decision: the canvas is disabled for that bridge until restart, with one warning naming the Slack channel and the code (Definition of Done B: fail closed, no log spam).
5. **Rate limit (Task 6).** "Wait the `Retry-After` delay, then rebuild" is implemented as re-marking the scope with deadline `now + min(RetryAfter, rateLimitWaitMax)` (no sleep in the publisher loop).
6. **Credential codes (Task 6).** Logged at warn once per bridge and code, scope re-marked with the window; the socket's handling decides the rest.
7. **Where "on by default" lives (Task 1).** `config.Default()` sets `SlackCanvasEnabled: true`; `slackbridge.Config.CanvasEnabled` has zero value `false` (the library default is off, so every existing bridge test makes no canvas call) and `cmd.go` copies the setting.
8. **Boolean settings (Task 1).** `config.set` uses `strconv.ParseBool`, which also accepts `1`, `t`, `T`, `TRUE`, `True` and the `false` forms. No boolean setting exists yet, so the `reflect.Bool` case is tightened to exactly `true` or `false` (environment and flag layers); the file layer is JSON, where only the literals `true`/`false` decode into a bool.
9. **First publish after `goLive` (Task 7)** happens at the next lifecycle pass (≤ `LifecycleInterval`, 60 s) or at the first hint for one of the scope's task or memory channels; `goLive` itself marks nothing.

## Review Focus

1. A memory whose content contains the marker text `changes go through agentmesh`, or a human's canvas quoting it: the lookup by `contains_text` would adopt a human's canvas and overwrite it. Test (Task 6): a foreign tab whose markdown contains the marker is adopted (documented limitation, asserted so a change is visible), and the operator doc names it.
2. An `in_progress` task with an empty owner (a corrupt write; `abandonedSet` reports it abandoned): the line must read `⚠︎ #id subject · stalled: owner offline` with no owner segment and no panic. Test (Task 5 golden `tree.md`).
3. A bridge row in state `creating` (empty `SlackChannelID`) or `archived` in a scope that also has a live bridge: the publisher must skip it and drop its state without touching the live one. Test (Task 7).
4. A task channel the humans can read but whose scope has retired, or a bridge removed between the snapshot and the publish: the publish must stop with no Slack call and no error log storm. Test (Task 7: remove the bridge, then mark; `Calls` unchanged).
5. Memory content whose last line is `#…` without a trailing newline, and content containing `---`: the heading guard must still prefix the `#` line (a divider line is rendered as written, accepted). Test (Task 5 golden `memories.md`).

## Spec section 9 coverage

| Spec 9 test | Task |
|---|---|
| Rendering goldens (attention, tree, completed subtrees, parent deleted, empty list, no visible channels, memories, escaping, size cap, tasks too large) | 5 |
| Snapshot on both engines: visibility rule, `everyone` read, zero humans; stalled reported and not reclaimed; `last_read_at` unchanged; owner status and `needs-human`; offline owners | 3 (tasks core), 4 (bridge) |
| Publisher: first publish creates `Status`, sets access, stores the hash; unchanged makes no call; change within the window makes one call; restart adopts by marker with another label; free-tier second-create adopts; foreign tab disables; `canvas_not_found` recreates; rate limit waits; `missing_scope` warns once | 6 (direct calls), 7 (window) |
| Disabled setting makes no call; only the lock holder publishes; archived bridge skipped | 7 |
| Config: defaults, ranges, unknown boolean refused, environment and flag precedence | 1 |
| Gates: `make verify`, `TestAccessRuleSites`, Slack import rules, CI | 8 |
| Live check (Eric's workspace) | Eric, after Task 8 |

---

### Task 1: Server settings and bridge Config fields

**Files:**
- Modify: `internal/server/config/config.go` (struct after `SlackMaxMessageChars`, `Default`, `Validate` after the `slack_max_message_chars` check)
- Modify: `internal/server/config/load.go:144-149` (the `reflect.Bool` case of `set`)
- Modify: `internal/server/config/docs_test.go:52-77` (`defaultCell`)
- Modify: `docs/ops/configuration.md:108` (two rows after `slack_max_message_chars`)
- Modify: `internal/slackbridge/service.go:25-33,40-66` (`Config`, `withDefaults`)
- Modify: `internal/server/cmd/cmd.go:214-218`
- Test: `internal/server/config/config_test.go` (`TestSlackSettings`), `internal/server/config/load_test.go` (`TestPrecedenceTable`, new `TestBoolSettingExact`), `internal/slackbridge/service_test.go` (`TestConfigWithDefaults`), `internal/server/cmd/cmd_test.go:741-752` ("valid files")

**Interfaces:**
- Produces: `config.Config.SlackCanvasEnabled bool` (json `slack_canvas_enabled`), `config.Config.SlackCanvasCoalesceSeconds int` (json `slack_canvas_coalesce_seconds`); `slackbridge.Config.CanvasEnabled bool`, `slackbridge.Config.CanvasCoalesce time.Duration` (0 means 30 s; negative refused by `withDefaults`).

- [ ] **Step 1: Write the failing tests**

In `TestSlackSettings`: the defaults line also checks `d.SlackCanvasEnabled != true || d.SlackCanvasCoalesceSeconds != 30`; cases `{"coalesce 4", c.SlackCanvasCoalesceSeconds = 4, "slack_canvas_coalesce_seconds"}`, `{"coalesce 5", ..., ""}`, `{"coalesce 600", ..., ""}`, `{"coalesce 601", ..., "slack_canvas_coalesce_seconds"}`. Error text: `slack_canvas_coalesce_seconds must be between 5 and 600, got %d`.

In `TestPrecedenceTable` add rows `{"slack_canvas_enabled", "false", "true", "false", func(c Config) string { return strconv.FormatBool(c.SlackCanvasEnabled) }}` and `{"slack_canvas_coalesce_seconds", "10", "20", "30", ...Itoa...}`.

New `TestBoolSettingExact` in `load_test.go`: for each of `"1"`, `"t"`, `"TRUE"`, `"True"`, `"yes"`, `""` the env `AGENTMESH_SERVER_SLACK_CANVAS_ENABLED=<v>` and the flag `Overrides{"slack_canvas_enabled": v}` both make `Load` fail with an error containing `setting slack_canvas_enabled from environment: invalid value` (or `from flag`) and `must be true or false`; `"true"` and `"false"` load. A file `{"sqlite":"/x.db","slack_canvas_enabled":"true"}` (a JSON string) fails to load.

`TestConfigWithDefaults` (slackbridge): `Config{...valid..., CanvasCoalesce: -time.Second}` is refused with `slackbridge: durations must not be negative`; a zero `CanvasCoalesce` becomes `30 * time.Second`; `CanvasEnabled` keeps its value.

`cmd_test.go` "valid files": add `"slack_canvas_enabled=false", "slack_canvas_coalesce_seconds=7"` to `set(...)` and assert `got.SlackCfg.CanvasEnabled != false || got.SlackCfg.CanvasCoalesce != 7*time.Second` fails the test.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/server/config/ ./internal/slackbridge/ ./internal/server/cmd/ -run 'TestSlackSettings|TestPrecedenceTable|TestBoolSettingExact|TestConfigWithDefaults|TestRunSlackTokenFiles|TestConfigurationReferenceCoversEverySetting' -count=1`
Expected: FAIL (unknown fields, `defaultCell` has no rule for kind bool, missing rows).

- [ ] **Step 3: Implement**

`config.go`: the two fields with json tags, defaults `true` and `30`, the range check. `load.go` `reflect.Bool`: `switch text { case "true": f.SetBool(true); case "false": f.SetBool(false); default: return bad(errors.New("must be true or false")) }`. `docs_test.go` `defaultCell`: `case reflect.Bool: return "bool", strconv.FormatBool(v.Bool())`. `configuration.md` rows (type `bool`/`int`, defaults `` `true` ``/`` `30` ``): `slack_canvas_enabled`: "`true` or `false`. Whether every live bridge's Slack channel carries the read-only scope canvas (tasks and memories). Ignored without the token files. See `slack-bridge.md`."; `slack_canvas_coalesce_seconds`: "5 to 600. How long after the first change the canvas of a scope is rebuilt; changes in the window join one rebuild." `slackbridge.Config`: the two fields, `CanvasCoalesce` joins the negative-duration loop and the zero default `30 * time.Second`. `cmd.go`: `CanvasEnabled: cfg.SlackCanvasEnabled, CanvasCoalesce: time.Duration(cfg.SlackCanvasCoalesceSeconds) * time.Second`.

- [ ] **Step 4: Run the tests to verify they pass**

Run: the Step 2 command, then `go test ./internal/server/config/ ./internal/slackbridge/ ./internal/server/cmd/ -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/server/config internal/slackbridge/service.go internal/slackbridge/service_test.go internal/server/cmd/cmd.go internal/server/cmd/cmd_test.go docs/ops/configuration.md
git commit -m "feat(config): add slack_canvas_enabled and slack_canvas_coalesce_seconds"
```

---

### Task 2: Slack port: five canvas methods in `slackapi`, `slackfake` and `slackgo`

**Files:**
- Modify: `internal/slackapi/slackapi.go` (types, error, `Client`)
- Modify: `internal/slackapi/slackfake/fake.go` (`methods`, state, five methods, helpers)
- Create: `internal/slackapi/slackgo/canvas.go`
- Modify: `go.mod`, `go.sum` (slack-go v0.30.1 → v0.34.0, design decision 1)
- Test: `internal/slackapi/slackfake/fake_test.go`, `internal/slackapi/slackgo/canvas_test.go`

**Interfaces:**
- Produces, in `slackapi`:
  - `type CanvasTab struct{ ID, Label string }` (ID is the canvas file ID `data.file_id`)
  - `var ErrCanvasAccessLevel = errors.New("slack: canvas access level must be read or write")`
  - `Client` gains: `CreateChannelCanvas(ctx context.Context, channelID, title, markdown string) (string, error)`; `ChannelCanvases(ctx context.Context, channelID string) ([]CanvasTab, error)`; `CanvasContains(ctx context.Context, canvasID, text string) (bool, error)`; `ReplaceCanvas(ctx context.Context, canvasID, markdown string) error`; `SetCanvasChannelAccess(ctx context.Context, canvasID, channelID, level string) error`.
  - Error contract (doc comment on `Client`): the canvas methods never return `ErrChannelGone` or another sentinel; every Slack code is `*APIError{Method, Code}` with the Web API method name (`conversations.canvases.create`, `conversations.info`, `canvases.sections.lookup`, `canvases.edit`, `canvases.access.set`), a 429 is `*RateLimitedError`.
- Produces, in `slackfake`: `(*Fake).AddCanvas(channelID, label, markdown string) string` (a canvas made outside the bridge: a foreign tab, or one whose label differs from its title); `(*Fake).Canvas(id string) (FakeCanvas, bool)` with `type FakeCanvas struct{ ID, ChannelID, Label, Markdown string; Access map[string]string }` (channel ID to level); `(*Fake).DeleteCanvasByHand(id string)`; `(*Fake).EmptyTabsNext(n int)` (the next `n` `ChannelCanvases` calls answer an empty list without touching the canvases, for the lookup-then-create race). `methods` gains the five names, so `Calls`, `FailNext` and `RateLimit` accept them. Rules: one canvas per channel (a second `CreateChannelCanvas` answers `&APIError{"conversations.canvases.create", "free_team_canvas_tab_already_exists"}`); an unknown canvas ID answers `canvas_not_found`; an archived channel answers `is_archived` on create, access set and replace; `CanvasContains` is `strings.Contains` on the markdown; `ChannelCanvases` lists the channel's canvases in creation order; the label is the title at creation and never changes.

- [ ] **Step 0: Bump slack-go**

Run: `go get github.com/slack-go/slack@v0.34.0 && go mod tidy && go build ./... && go test ./internal/slackapi/... -count=1`
Expected: go.mod shows `github.com/slack-go/slack v0.34.0`; build and tests PASS. `make tidy-check` stays clean.

- [ ] **Step 1: Write the failing fake tests** (`fake_test.go`)

`TestFakeCanvasLifecycle`: create on `C1` with title `Status` returns an ID; `Canvas(id)` has `Label == "Status"`, the markdown and empty `Access`; `ChannelCanvases(C1)` is `[{id, "Status"}]`; a second create returns `*APIError` with `Code == "free_team_canvas_tab_already_exists"`; `CanvasContains(id, "agentmesh")` true, `"zzz"` false; `ReplaceCanvas` changes the markdown; `SetCanvasChannelAccess(id, C1, "read")` sets `Access[C1] == "read"`; `SetCanvasChannelAccess(id, C1, "owner")` is `errors.Is(err, slackapi.ErrCanvasAccessLevel)` and `Calls("SetCanvasChannelAccess")` does not grow; `DeleteCanvasByHand(id)` then `ReplaceCanvas` and `CanvasContains` answer `canvas_not_found`; `ArchiveByHand(C1)` then create answers `is_archived`. `TestFakeCanvasFailAndRateLimit`: `FailNext("ReplaceCanvas", err)` and `RateLimit("CreateChannelCanvas", 1, time.Second)` are honored; `AddCanvas` makes a tab with its label without counting a call.

- [ ] **Step 2: Run the fake tests to verify they fail**

Run: `go test ./internal/slackapi/slackfake/ -run 'TestFakeCanvas' -count=1`
Expected: FAIL (methods undefined).

- [ ] **Step 3: Implement the port and the fake**

`slackapi.go`: the type, the error and the five interface methods with the error contract. `fake.go`: `canvases map[string]*FakeCanvas`, `nextCanvas int` (IDs `F000001`...), the methods under `f.mu` with `begin(method)` first, the helpers.

- [ ] **Step 4: Run the fake tests to verify they pass**

Run: `go test ./internal/slackapi/slackfake/ -count=1`
Expected: PASS

- [ ] **Step 5: Write the failing slackgo tests** (`canvas_test.go`, through `newFake`/`fakeSlack`)

`TestCreateChannelCanvasParams`: reply `{"ok":true,"canvas_id":"F1"}`; the recorded form has `channel_id=C1`, `title=Status` and `document_content` whose JSON is `{"type":"markdown","markdown":"# x"}`; result `"F1"`. `TestChannelCanvasesReadsTabData`: reply `{"ok":true,"channel":{"id":"C1","properties":{"canvas":null,"tabs":[{"id":"T1","label":"Status","type":"canvas","data":{"file_id":"F1"}},{"id":"T2","label":"Files","type":"files"}]}}}`; result `[{ID:"F1", Label:"Status"}]`; the recorded call method is `conversations.info` with form `channel=C1`. `TestCanvasContains`: reply `{"ok":true,"sections":[{"id":"S1"}]}` → true, the form's `criteria` JSON is `{"contains_text":"changes go through agentmesh"}`; `{"ok":true,"sections":[]}` → false. `TestReplaceCanvasParams`: the form's `changes` JSON is `[{"operation":"replace","document_content":{"type":"markdown","markdown":"# y"}}]` (no `section_id`). `TestSetCanvasAccessParams`: `access_level=read`, `channel_ids` JSON `["C1"]`; level `"admin"` returns `ErrCanvasAccessLevel` with no call recorded. `TestCanvasErrorsKeepCodes`: for each method, `slackErr("is_archived")` gives `*APIError{Code:"is_archived"}` (not `ErrChannelGone`), `slackErr("channel_not_found")` gives `Code:"channel_not_found"`, a 429 with `Retry-After: 3` gives `*RateLimitedError{RetryAfter: 3*time.Second}`, HTTP 500 gives `Code:"http_500"`; no error text contains `xoxb`.

- [ ] **Step 6: Run the slackgo tests to verify they fail**

Run: `go test ./internal/slackapi/slackgo/ -run 'Canvas|Canvases' -count=1`
Expected: FAIL.

- [ ] **Step 7: Implement `slackgo/canvas.go`**

`mapCanvasErr(method string, err error) error` (rate limit, `SlackErrorResponse` → `APIError{method, se.Err}`, `StatusCodeError` → `http_N`, else wrapped). `CreateChannelCanvas` via `api.CreateChannelCanvasContext(ctx, channelID, slack.DocumentContent{Type: "markdown", Markdown: markdown}, slack.CreateChannelCanvasOptionTitle(title))`; `CanvasContains` via `LookupCanvasSectionsContext` with `Criteria{ContainsText: text}`; `ReplaceCanvas` via `EditCanvasContext` with one `CanvasChange{Operation: "replace"}`; `SetCanvasChannelAccess` via `SetCanvasAccessContext{ChannelIDs: []string{channelID}}` after the level check. `ChannelCanvases` via `api.GetConversationInfoContext(ctx, &slack.GetConversationInfoInput{ChannelID: channelID})` (design decision 1), errors through `mapCanvasErr("conversations.info", err)`; keep the tabs with `Type == "canvas"` and `Data != nil && Data.FileID != ""`, as `CanvasTab{ID: Data.FileID, Label: Label}`.

- [ ] **Step 8: Run the tests and the import rule**

Run: `go test ./internal/slackapi/... -count=1 && scripts/m7-exit-checks.sh --deps-only`
Expected: PASS; the script prints its rules as passed.

- [ ] **Step 9: Commit**

```bash
git add internal/slackapi
git commit -m "feat(slackapi): canvas methods in the port, the fake and the slack-go adapter"
```

---

### Task 3: Core read-only helpers: `tasks.Service.Snapshot` and `statuses.Service.LiveStatuses`

**Files:**
- Create: `internal/core/tasks/snapshot.go`
- Create: `internal/core/statuses/live.go`
- Modify: `internal/core/access/sites_test.go:123-127` (one entry: `"internal/core/tasks/snapshot.go Snapshot": {"Owner": 1}, // copies the task's owner into the snapshot's assignee: a converter (kind N)`)
- Test: `internal/conformance/bus/canvas_core_test.go` (both engines through `newHarness`)

**Interfaces:**
- Produces:
  - `tasks.SnapshotTask struct{ ID int64; Subject, Status, Assignee string; Parent int64; Depth int; OpenBlockers []int64; LeasedUntil int64; Abandoned, LeaseExpired bool }`
  - `func (s Service) Snapshot(ctx context.Context, tx store.Tx, ch store.Channel, now int64) ([]SnapshotTask, error)`: the live tasks in the order `loadTasks` returns (depth-first, siblings by rank), `Depth` from `taskTree`, `Parent` the effective parent (0 for a root whose parent is gone), `Abandoned` from `abandonedSet(ctx, tx, ts, now)`, `LeaseExpired` from `leaseExpired(t, now)`. It never calls `reclaim`, `markRead` or any Tx write; a channel whose `Kind != store.KindTasks` answers `core.E(bus.CodeValidation, "%q is not a task list", ch.Name)`.
  - `func (s statuses.Service) LiveStatuses(ctx context.Context, tx store.Tx, identities []string, now int64) (map[string]bus.AgentStatus, error)`: for each name in `identities` with a row in `tx.LatestAgentStatuses` and a live session (`s.LiveSessions(ctx, tx, name)` non-empty), `core.AgentStatusToBus(row)`; offline or unknown names are absent. Duplicate names are read once. The result is never nil.

- [ ] **Step 1: Write the failing tests** (`canvas_core_test.go`, package `bus`, `dbtest.ForEachEngine`)

`TestTaskSnapshotReadsWithoutWriting`: a tasks channel `h.channel("job1-tasks", KindTasks, "alice", acl)`; through `h.as("alice")`: root `#1 "Root"`, child `#2` (`Parent: 1`), grandchild `#3` (`Parent: 2`), great-grandchild `#4` (`Parent: 3`), sibling root `#5` with `BlockedBy: []int64{1}`; `#2` claimed by `bob` with `LeasedUntil: h.taskLeaseIn(time.Minute)` from a live session (`h.session("bob", "")` then Claim), `#3` claimed by `carl` with a lease of an hour. Then `h.clock.Advance(2 * time.Minute)` (bob's lease expired; carl's session lease lapses too, so carl is offline: the harness session lease is `core.Defaults().SessionLeaseSeconds` 90 s plus `LeaseSkewSeconds` 5 s). Call `(tasks.Service{Core: h.core}).Snapshot` in `h.tx` (a `Store.Read`); assert: order `[1 2 3 4 5]`, depths `[0 1 2 3 0]`, `#5.OpenBlockers == [1]`, `#2 {Abandoned: true, LeaseExpired: true, Assignee: "bob"}`, `#3 {Abandoned: true, LeaseExpired: false, Assignee: "carl"}`, `#1 {Abandoned: false}`. Then, in a second read: `tx.LiveMemories(ctx, ch.ID)` has the same length and the same max `Revision` as before the snapshot (nothing reclaimed: a `List` by alice afterwards does reclaim and the revision grows, proving the probe works), and `tx.Channel(ctx, ch.ID).LastReadAt == 0`. `TestTaskSnapshotParentGoneIsRoot`: a task is never deleted alone through the Bus (task expiry drops whole lists and the memory delete refuses a task list), so the orphan is written as a raw task row: `h.append(ch, "alice", "", `+"`"+`{"subject":"orphan","status":"pending","parent":999,"rank":"n"}`+"`"+`)` (the harness `append` sets `TaskRow` for a tasks channel); the snapshot lists it with `Depth 0`, `Parent 0`, after the tree of the other roots. `TestTaskSnapshotKind`: on an ordinary channel the error code is `validation`.

`TestLiveStatusesDerivesOffline`: `h.session("bob", "")` live; `h.as("bob").SetAgentStatus(ctx, SetAgentStatusInput{Session: <id>, Tag: bus.StatusNeedsHuman, Text: "which style?", Problem: ""})`; `carl` sets `Tag: bus.StatusImplementing, Problem: "docker daemon down"` then his session is closed (`h.raw` `CloseSession`); `LiveStatuses(ctx, tx, []string{"bob", "carl", "nobody", "bob"}, h.core.NowMs())` has exactly `bob` with `Tag == "needs-human"` and `Text == "which style?"`; `carl` absent; advancing the clock past the session lease removes `bob` too.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/conformance/bus/ -run 'TestTaskSnapshot|TestLiveStatuses' -count=1`
Expected: FAIL (undefined `Snapshot`, `LiveStatuses`).

- [ ] **Step 3: Implement `snapshot.go` and `live.go`**, and add the allowlist entry.

- [ ] **Step 4: Run the tests and the gate**

Run: `go test ./internal/conformance/bus/ -run 'TestTaskSnapshot|TestLiveStatuses' -count=1 && go test ./internal/core/access/ -run TestAccessRuleSites -count=1 && go test ./internal/core/... -count=1`
Expected: PASS on `sqlite` and `postgres` subtests.

- [ ] **Step 5: Commit**

```bash
git add internal/core/tasks/snapshot.go internal/core/statuses/live.go internal/core/access/sites_test.go internal/conformance/bus/canvas_core_test.go
git commit -m "feat(core): read-only task snapshot and live statuses for the scope canvas"
```

---

### Task 4: Bridge snapshot with the visibility rule

**Files:**
- Create: `internal/slackbridge/canvas_snapshot.go`
- Test: `internal/conformance/bus/slackbridge_canvas_test.go` (new; uses `newSlackFixOpts`/`inFix` helpers), plus a new fixture helper `(x *slackFix) slKindChannel(name, scope string, kind ab2store.Kind, acl []ab2store.ACLEntry) ab2store.Channel` in `slackbridge_create_test.go` next to `slChannel` (same body with `Kind: kind`)

**Interfaces:**
- Consumes: `tasks.Service.Snapshot`, `statuses.Service.LiveStatuses` (Task 3); `s.core.Access.Holders(ctx, tx, access.Channel{ID}, access.Post|access.Read)`; `tx.ScopeMembers(ctx, scope)`; `tx.LiveMemories(ctx, channelID)`.
- Produces (exported for the tests and the renderer):
  - `type CanvasSnapshot struct{ ScopeName string; At time.Time; TaskLists []CanvasTaskList; MemoryChannels []CanvasMemoryChannel }` (`At` is `s.core.Now().UTC()`; both slices by channel name, byte order, as `ScopeMembers` lists them)
  - `type CanvasTaskList struct{ Channel string; Tasks []CanvasTask }`
  - `type CanvasTask struct{ ID int64; Subject, Status, Assignee string; Depth int; OpenBlockers []int64; Stalled bool; LeaseExpiredAt time.Time; OwnerStatus *CanvasOwnerStatus }` (`Stalled` = `Abandoned`; `LeaseExpiredAt` is `LeasedUntil` as UTC time when `LeaseExpired`, else zero; `OwnerStatus` nil when the task is completed, has no assignee, or the assignee is absent from `LiveStatuses`)
  - `type CanvasOwnerStatus struct{ Tag, Text, Problem string }`
  - `type CanvasMemoryChannel struct{ Channel string; Memories []CanvasMemory }` (newest `EditedAt` first, ties by `ID` descending)
  - `type CanvasMemory struct{ ID int64; Subject, Type, Content, Sender string; Tags []string; EditedAt time.Time }` (from `store.Message`: `*MemoryID`, `Subject`, `Type`, `Content`, `Sender`, `Tags`, `CreatedAt`)
  - `func (s *Service) CanvasSnapshot(ctx context.Context, b store.Bridge, scope string) (CanvasSnapshot, error)` (`scope` is the bridge's scope as the caller derived it from `ScopeMembers`, Task 7; the function never reads `b.Scope`, so no allowlist entry): one `s.core.Store.Read`; humans = names of `Holders(Channel{b.ChannelID}, Post)`; candidates = `ScopeMembers(scope)` channels with `Kind != store.KindOrdinary`; a candidate is visible when `len(humans) > 0` and every human name is in the names of `Holders(Channel{candidate.ID}, Read)` (Holders already expands `everyone`, `humans` and the admin flag to identities, so this is set containment by name); tasks channels (`Kind == store.KindTasks`) through `Snapshot`, memory channels through `LiveMemories`; statuses for the distinct assignees of non-completed tasks through `LiveStatuses`. `store.ErrNotFound` from the Store becomes `core.FromStore(err)`.

- [ ] **Step 1: Write the failing tests** (`slackbridge_canvas_test.go`, `dbtest.ForEachEngine`, fixture `newInFixOpts(t, e, harnessOptions{}, nil)`; the bridged channel `job1-chat` has humans `slAlice`, `slGina`, `slXav`, `slNone`, `siKim`, `siRon`, `siCat`, `siDee`, `siBotU`, `siUla` as Post holders)

`TestCanvasSnapshotVisibility`: four scoped channels in `job1`: `job1-tasks` (tasks, ACL: read for every one of those humans by name plus `job1-coder` R/W/A), `job1-notes` (memory, ACL `everyone` read), `job1-private` (tasks, read for `slAlice` only), `job1-other` (memory, ACL that names no human). `CanvasSnapshot(ctx, f.bridge(), "job1")` has `TaskLists == [job1-tasks]` and `MemoryChannels == [job1-notes]`, `ScopeName == "job1"`; an unknown scope name answers `not_found`. Subtest `zero-humans`: a second ordinary channel `job1-solo` in `job1` with ACL `{job1-coder R W A, Admins A}` bridged with `-slack-name job1-solo`; its snapshot has no lists and no memory channels. Subtest `ordinary-ignored`: `job1-chat` never appears.

`TestCanvasSnapshotTasksAndStatuses`: on `job1-tasks`, through `f.h.localAs("job1-coder")`: tasks `#1 Root`, `#2 child of 1`, `#3 child of 2`, `#4 root blocked by 1`; `job1-coder` claims `#2` from a live session (`f.h.session("job1-coder", "")`) and sets status `needs-human`/`which style?`; a second scoped identity `job1-b` (`f.h.scopedIdentity("job1-b", "job1")`, ACL write) claims `#3` with a one-minute lease and sets `Tag: implementing, Problem: "docker daemon down"`; then `h.clock.Advance(2 * time.Minute)` (both session leases lapse) and `f.h.session("job1-coder", "")` opens a fresh live session for `job1-coder` alone, so `job1-coder` is live and `job1-b` is offline at the snapshot time. Assert: `#2.OwnerStatus == &{Tag:"needs-human", Text:"which style?"}`, `#3 {Stalled: true, LeaseExpiredAt == lease time, OwnerStatus == nil}` (offline contributes nothing), `#4.OpenBlockers == [1]`, `#1.OwnerStatus == nil` (no assignee). Completed: mark `#1`'s... no, `#1` has a blocked dependent; complete `#2` (`Update{Status: "completed"}`) and assert `OwnerStatus == nil` for it even though `job1-coder` is `needs-human`. The snapshot writes nothing: `tx.Channel(job1-tasks).LastReadAt == 0` and the tasks channel's `LiveMemories` max `Revision` is unchanged across two snapshots with the clock advanced.

`TestCanvasSnapshotMemories`: on `job1-notes`, two sends by `job1-coder` (`Subject: "Postgres needs TCP keepalive"`, `Type: "fact"`, `Tags: ["area:db"]`, then a second with no subject), the first edited after `h.clock.Advance(time.Minute)`; one memory deleted; the snapshot lists the two live ones with the edited one first and `EditedAt == edit time`, `Sender == "job1-coder"`, `Tags == ["area:db"]`.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/conformance/bus/ -run 'TestCanvasSnapshot' -count=1`
Expected: FAIL (undefined `CanvasSnapshot`).

- [ ] **Step 3: Implement `canvas_snapshot.go`.**

- [ ] **Step 4: Run the tests and the gates**

Run: `go test ./internal/conformance/bus/ -run 'TestCanvasSnapshot' -count=1 && go test ./internal/core/access/ -run TestAccessRuleSites -count=1 && scripts/m7-exit-checks.sh --deps-only`
Expected: PASS on both engines; `TestAccessRuleSites` passes with no allowlist change in this task.

- [ ] **Step 5: Commit**

```bash
git add internal/slackbridge/canvas_snapshot.go internal/conformance/bus/slackbridge_canvas_test.go internal/conformance/bus/slackbridge_create_test.go
git commit -m "feat(slackbridge): the scope canvas snapshot under the every-human-reads rule"
```

---

### Task 5: Renderer, escaping, hash and golden files

**Files:**
- Create: `internal/slackbridge/canvasrender.go`
- Create: `internal/slackbridge/testdata/canvas/*.md` (goldens, written by `-update`, reviewed by eye against spec 4.1)
- Modify: `.gitattributes` (add `internal/slackbridge/testdata/** text eol=lf`)
- Test: `internal/slackbridge/canvasrender_test.go`

**Interfaces:**
- Consumes: the `CanvasSnapshot` types (Task 4).
- Produces:
  - `const CanvasTitle = "Status"`, `const CanvasMarker = "changes go through agentmesh"`, `const canvasMaxBytes = 1_044_480`
  - `type CanvasRendering struct{ Markdown string; DroppedMemories int; TasksTooLarge bool }`
  - `func RenderCanvas(s CanvasSnapshot) CanvasRendering`: pure; the layout of spec 4.1 verbatim (h1 `# <scope> · tasks and memory`, the italic `_Updated <YYYY-MM-DD HH:MM> UTC · read-only; changes go through agentmesh_` line, the legend line, `## Needs attention` with the four h3 subsections or `_Nothing needs attention._`, `---`, task lists, `---`, memory channels). Spec 4.2 to 4.6 rules: glyph precedence (blocked, needs-human, stalled, owner problem, in_progress, pending; completed is `✔` with no suffix); root (`Depth 0`) as `### <line>`, descendants as `- [x]`/`- [ ]` items indented by `"  "` per depth above 1; the count line in the order `▶ ◌ ⛔︎ ⚠︎ ⚑ ✔` without zero counts; status and problem texts cut to 120 characters plus `…`; memory headings (subject, else the first content line cut to 80 characters), the italic line `_<type> · <tags joined by " · "> · edited <YYYY-MM-DD> by <sender>_` omitting empty parts, content as written except a leading `#` on any line gets a `\` prefix; the size cap dropping the oldest memories across channels and the `_<n> older memories not shown; search agentmesh for them._` trailer per affected channel; `TasksTooLarge` when the rendering with zero memories still exceeds the cap, the body then being the header lines and `_The task lists are too large for a canvas._`; no visible channel: the body after the legend is `_Nothing in this scope is visible to every member of this channel._`.
  - `func escapeCanvas(s string) string`: `\n` and `\r` become spaces; each of `*`, `_`, `~`, `` ` ``, `[`, `]`, `<`, `>`, `|` gets a `\` prefix; a `#` at index 0 gets a `\` prefix.
  - `func CanvasHash(markdown string) string`: SHA-256 hex of the markdown with its second line (the `_Updated ..._` line, whose position the renderer fixes) removed; a memory line that happens to start with `_Updated ` is never stripped.

- [ ] **Step 1: Write the failing tests**

`var update = flag.Bool("update", false, "rewrite the goldens in testdata/canvas")` and `TestRenderCanvasGolden` over cases built in the test file, each a `CanvasSnapshot` with `At: time.Date(2026, 11, 4, 20, 45, 0, 0, time.UTC)` and `ScopeName: "job1"`: `attention` (all four subsections populated, one task both blocked and needs-human, two lists `job1-tasks` and `job1-tasks-2`), `attention-empty` (lists with only pending tasks: `_Nothing needs attention._`), `tree` (four levels deep: a root in progress by `claude-a`; a done child; a stalled child `lease expired 20:41 UTC` with `OwnerStatus` nil; a stalled grandchild `owner offline` with an empty `Assignee`; a needs-human child; a blocked root with a pending child; a root with an owner problem text of 130 characters cut to 120 plus `…`), `completed-subtrees` (a done root whose children are done, and a done task whose owner is `needs-human`: `✔` with no suffix, counted under `✔`), `parent-gone` (a `Depth 0` task with `ID 3` after `1` and `2`), `empty-list` (`_No tasks._`, no count line), `nothing-visible` (no lists, no memory channels), `memories` (one memory with a subject, one without whose first line is 100 characters, a content with a line `# not a heading` and a last line `#tail` without a trailing newline, a `---` line, an empty memory channel `_No memories._`), `escaping` (a subject holding every listed character and a `\r\n`, an owner `a*b`, a channel name `job1_tasks`, a memory subject starting with `#`), `size-cap` (three memories of 400,000 bytes each, newest first: the oldest two dropped, trailer `_2 older memories not shown; search agentmesh for them._`, `DroppedMemories == 2`), `tasks-too-large` (5,000 tasks with 256-byte subjects: `TasksTooLarge == true`, the body line). The test compares `Markdown` to `testdata/canvas/<name>.md` (the `mcpcli/help_test.go` pattern and error text) and, for `size-cap` and `tasks-too-large`, asserts the flags and `len(Markdown) <= canvasMaxBytes`.

`TestCanvasGlyphBytes`: the rendered legend contains `"⛔︎"` and `"⚠︎"` and `"◌"`, `"▶"`, `"✔"`, `"⚑"`. `TestEscapeCanvas`: table of the listed characters, `"#x"` → `"\\#x"`, `"a#x"` unchanged, `"a\r\nb"` → `"a  b"`. `TestCanvasHashIgnoresUpdated`: two renderings of the same snapshot with `At` an hour apart hash equal; a changed subject hashes differently; the hash is 64 hex characters. `TestRenderCanvasIsPure`: rendering the same snapshot twice yields equal markdown and does not modify the snapshot's slices (compare a deep copy).

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/slackbridge/ -run 'TestRenderCanvas|TestCanvasGlyph|TestEscapeCanvas|TestCanvasHash' -count=1`
Expected: FAIL (undefined).

- [ ] **Step 3: Implement `canvasrender.go`**, then `go test ./internal/slackbridge/ -run TestRenderCanvasGolden -update`, and read every golden against spec 4.1 (the implementer records in the commit message that the goldens were reviewed by eye).

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/slackbridge/ -count=1 && make lint`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/slackbridge/canvasrender.go internal/slackbridge/canvasrender_test.go internal/slackbridge/testdata/canvas .gitattributes
git commit -m "feat(slackbridge): render the scope canvas markdown (golden-tested)"
```

---

### Task 6: Coalescer and publisher

**Files:**
- Create: `internal/slackbridge/canvas.go` (publisher state, find/adopt/create/replace/access, error table), `internal/slackbridge/canvasdirty.go` (the pure coalescer)
- Modify: `internal/slackbridge/service.go` (`Service.canvas *canvasPublisher`, built in `New`)
- Test: `internal/slackbridge/canvasdirty_test.go` (pure), `internal/conformance/bus/slackbridge_canvas_test.go` (publisher through `PublishCanvas`, no runner)

**Interfaces:**
- Consumes: Task 2 methods, Task 4 `CanvasSnapshot`, Task 5 `RenderCanvas`/`CanvasHash`/`CanvasMarker`/`CanvasTitle`, `ArchiveBridge(ctx, b, reasonSlackGone)`, `rateLimitWaitMax`.
- Produces:
  - Clocks: deadlines, `retryAt` and the loop's timers are wall-clock (`time.Now()`), like the worker's retry timers; the harness `fakeClock` drives `core.Now()` only and is frozen, so it is used for the snapshot's `At` and the lease and session comparisons alone.
  - `type canvasDirty struct{ due map[string]time.Time }` with `func (d *canvasDirty) mark(scope string, at time.Time)` (keeps an earlier deadline, so marks within a window join it; the refresh request is the key `""`), `func (d *canvasDirty) next() (time.Time, bool)` (the earliest deadline), `func (d *canvasDirty) take(now time.Time) []string` (removes and returns, sorted, every scope whose deadline is not after `now`), `func (d *canvasDirty) len() int`.
  - `type canvasState struct{ canvasID, hash string; disabled bool; warned map[string]bool }` per bridge ID.
  - `type canvasPublisher struct{ s *Service; mu sync.Mutex; dirty canvasDirty; chanScope map[int64]string; unknown map[int64]bool; state map[int64]*canvasState; wake chan struct{} }`
  - `func (s *Service) PublishCanvas(ctx context.Context, b store.Bridge, scope string) (retryAt time.Time, err error)` (exported for tests; `scope` as in `CanvasSnapshot`; a zero `retryAt` means nothing to retry, the loop of Task 7 re-marks the scope at a non-zero one): skips with no call when `!s.cfg.CanvasEnabled`, `b.State != store.BridgeLive`, `b.SlackChannelID == ""` or the bridge's state is `disabled`; otherwise `CanvasSnapshot`, `RenderCanvas` (a `TasksTooLarge` rendering logs `slack bridge: task lists too large for a canvas` with the scope and is still published), `CanvasHash`; equal to `state.hash`: return nil with no Slack call; else `ensureCanvas` then `ReplaceCanvas`, then store the hash. `ensureCanvas` (spec 6.3): with no `canvasID`, `ChannelCanvases`; for each tab `CanvasContains(tab.ID, CanvasMarker)`; the first hit is adopted and `SetCanvasChannelAccess(id, channel, "read")` is called; no tab: `CreateChannelCanvas(channel, CanvasTitle, markdown)` then the access call, and the create's markdown counts as published (hash stored, no replace); the create answering `channel_canvas_already_exists` or `free_team_canvas_tab_already_exists` looks again, and a tab without the marker disables the bridge (`slack bridge: canvas belongs to someone else; canvas disabled` with the Slack channel ID).
  - The error table of spec 6.4 as `func (p *canvasPublisher) handle(b store.Bridge, st *canvasState, err error) (retryAt time.Time, archive bool)`: `team_tier_cannot_create_channel_canvases`, `canvas_disabled_user_team` → disable, warn once (`slack bridge: canvas unavailable on this Slack plan` naming the channel); `missing_scope` → disable, warn once naming `canvases:read` and `canvases:write`; `canvas_not_found`, `channel_not_found` → clear `canvasID` and `hash`, retry at the next window; `is_archived`, `not_in_channel` → `archive == true` (the caller runs `ArchiveBridge(ctx, b, reasonSlackGone)` and deletes the state); `*RateLimitedError` → retry at `now + min(RetryAfter, rateLimitWaitMax)`; `canvas_editing_locked`, `fatal_error`, `internal_error`, `service_unavailable`, `request_timeout`, any `http_` code, a non-`APIError` (transport, context) → retry at the next window; `invalid_auth`, `not_authed`, `account_inactive`, `token_revoked`, `token_expired`, `access_denied` → warn once per bridge and code, retry at the next window; any other code → disable, warn once naming the code (design decision 4).
  - `PublishCanvas` applies `handle` itself: it runs `ArchiveBridge` and drops the state on `archive`, and returns the handled error (for the tests and the log) with `retryAt`, which the loop (Task 7) turns into a mark.

- [ ] **Step 1: Write the failing coalescer tests** (`canvasdirty_test.go`)

`TestCanvasDirtyCoalesces`: `t0 := time.Unix(0,0)`; `mark("job1", t0.Add(30s))` then `mark("job1", t0.Add(5s))` leaves the deadline at 5 s, and `mark("job1", t0.Add(40s))` leaves it at 5 s (`mark` keeps the earlier deadline: a rate-limit retry can shorten a window, a mark during a window never lengthens it); `mark("job2", t0.Add(30s))`; `next()` is `t0+5s`; `take(t0+4s)` is empty; `take(t0+5s)` is `["job1"]`; `take(t0+30s)` is `["job2"]`; `len() == 0`. `TestCanvasDirtyTakeSorted`: marks `b`, `a`, `""` all due → `["", "a", "b"]`.

- [ ] **Step 2: Run them to verify they fail**, then implement `canvasdirty.go`, then run: `go test ./internal/slackbridge/ -run TestCanvasDirty -count=1`. Expected: FAIL then PASS.

- [ ] **Step 3: Write the failing publisher tests** (in `slackbridge_canvas_test.go`; fixture `newInFixOpts(t, e, harnessOptions{}, func(c *slackbridge.Config) { c.CanvasEnabled = true })` plus a visible tasks channel with one task; `pub := func() (time.Time, error) { return f.svc.PublishCanvas(ctx, f.bridge(), "job1") }`)

`TestCanvasPublishCreatesAndReplaces`: first `pub()`: `Calls("CreateChannelCanvas") == 1`, `Calls("SetCanvasChannelAccess") == 1`, the fake canvas has `Label == "Status"`, `Access[f.sl] == "read"`, markdown containing `# job1 · tasks and memory` and `changes go through agentmesh`; second `pub()` with nothing changed and the clock advanced an hour: no new call of any of the five methods; add a task, `pub()`: `Calls("ReplaceCanvas") == 1` and the markdown shows the new subject. `TestCanvasAdoptsByMarker`: `f.fake.AddCanvas(f.sl, "Old title", "# x\n_Updated ... changes go through agentmesh_\n")` before the first `pub()`: no create, `CanvasContains` called, the adopted canvas replaced, access set to `read`. `TestCanvasFreeTierSecondCreateAdopts` (the race where a canvas appears between the lookup and the create, through the fake's `EmptyTabsNext` of Task 2): a marked canvas is added with `AddCanvas`, `EmptyTabsNext(1)`; `pub()`: the lookup sees nothing, the create answers `free_team_canvas_tab_already_exists` (the fake's one-canvas rule), the second lookup finds the marked tab and adopts it; `Calls("CreateChannelCanvas") == 1`, `Calls("ChannelCanvases") == 2`, `Calls("ReplaceCanvas") == 1`. `TestCanvasRestartAdopts`: after a create through `pub()`, a fresh `Service` over the same store and fake (`f.newService(f.h, mod)`, no memory) whose first `pub()` adopts (`Calls("CreateChannelCanvas")` stays 1, access set again). `TestCanvasLookupFailureRetries`: `FailNext("ChannelCanvases", &APIError{"conversations.info", "internal_error"})`: `pub()` returns a `retryAt` equal to the window and no create; the next `pub()` creates. `TestCanvasForeignTabDisables`: `AddCanvas(f.sl, "Notes", "# a human's canvas")`; `pub()` returns an error whose text contains `belongs to someone else`, `Calls("ReplaceCanvas") == 0`, the log holds the warning once after three `pub()` calls, and `Calls("ChannelCanvases")` does not grow after the first. `TestCanvasRecreatesAfterNotFound`: after a create, `f.fake.DeleteCanvasByHand(id)`; `pub()` with a change answers `retryAt` within the window and the error code `canvas_not_found`; the next `pub()` creates again (`Calls("CreateChannelCanvas") == 2`). `TestCanvasRateLimitWaits`: `RateLimit("ReplaceCanvas", 1, 3*time.Second)`; `pub()` with a change returns `retryAt` ≈ `now + 3s` (within 100 ms) and `*RateLimitedError`; `pub()` again replaces. `TestCanvasMissingScopeWarnsOnce`: `FailNext("CreateChannelCanvas", &APIError{..., "missing_scope"})`; three `pub()` calls: one warning containing `canvases:read` and `canvases:write`, one call of create. `TestCanvasArchivedInSlack`: `FailNext("ReplaceCanvas", &APIError{"canvases.edit", "is_archived"})`: `pub()` reports `archive` through the bridge row (`f.bridge().State == store.BridgeArchived`) and the state is gone (the next `pub()` makes no call). `TestCanvasUnknownCodeDisables`: `FailNext("ReplaceCanvas", &APIError{"canvases.edit", "invalid_arguments"})`: a warning naming `invalid_arguments` once, and no later call. `TestCanvasMarkerInMemoryIsAdopted` (Review Focus 1): a foreign canvas whose markdown quotes the marker is adopted. `TestCanvasDisabledMakesNoCall`: `CanvasEnabled` false: `pub()` is a no-op, every canvas `Calls` is 0. Every test ends with `f.noSecretsLogged(t)` extended with the task subject `SECRET-SUBJECT` and memory content `SECRET-CONTENT` used in the fixtures.

- [ ] **Step 4: Run the publisher tests to verify they fail**

Run: `go test ./internal/conformance/bus/ -run 'TestCanvas(Publish|Adopts|FreeTier|Restart|LookupFailure|Foreign|Recreates|RateLimit|MissingScope|ArchivedInSlack|UnknownCode|MarkerInMemory|Disabled)' -count=1`
Expected: FAIL (undefined `PublishCanvas`).

- [ ] **Step 5: Implement `canvas.go`** and wire `s.canvas = newCanvasPublisher(s)` in `New`.

- [ ] **Step 6: Run the tests and the gates**

Run: the Step 4 command, then `go test ./internal/slackbridge/ -count=1 && go test ./internal/core/access/ -run TestAccessRuleSites -count=1 && make lint`
Expected: PASS on both engines; no new allowlist entry needed (the publisher reads `b.ChannelName`, `b.SlackChannelID`, `b.State`, `b.ID` only; the scope name comes from `CanvasSnapshot.ScopeName`).

- [ ] **Step 7: Commit**

```bash
git add internal/slackbridge/canvas.go internal/slackbridge/canvasdirty.go internal/slackbridge/canvasdirty_test.go internal/slackbridge/service.go internal/conformance/bus/slackbridge_canvas_test.go
git commit -m "feat(slackbridge): publish the scope canvas (find by marker, create, replace, access, error table)"
```

---

### Task 7: Runner wiring: fifth goroutine, hints, lifecycle and connect marks

**Files:**
- Modify: `internal/slackbridge/runner.go:124-216` (`startSession`: `rs.wg.Add(5)`, the canvas goroutine with its own `s.core.Notifier().Subscribe()`, and `s.canvas.markAll()` after every `s.Lifecycle(ctx)` in the lifecycle goroutine)
- Modify: `internal/slackbridge/lifecycle.go:19-23` (`markConnected` also calls `s.canvas.markAll()`)
- Modify: `internal/slackbridge/canvas.go` (the loop and the marks)
- Test: `internal/conformance/bus/slackbridge_canvas_test.go` (runner subtests, `runBridge`, sibling harnesses)

**Interfaces:**
- Consumes: Task 6 publisher, `canvasDirty`, `notify.Hint{ChannelID, Seq}`.
- Produces:
  - `func (p *canvasPublisher) markAll()`: sets `pendingAll = true`, marks the refresh key `""` at `now + window` and wakes the loop (non-blocking send on `wake`); `refresh` then marks every scope present in `scopeBridges` at that same deadline and clears the flag, so the first publish after a session start or a bridge going live needs one pass, not two. A scope marked at or before `now` during `refresh` is published on the loop's next iteration (the timer fires at once), never folded into the current `take`. A no-op when `!CanvasEnabled`.
  - `func (p *canvasPublisher) markHint(h notify.Hint)`: `ChannelID == 0` → `markAll`; a known channel → `mark(scope, now + window)`; unknown → `unknown[ChannelID] = true` and `mark("", now + window)`.
  - `func (p *canvasPublisher) refresh(ctx) error`: one `Store.Read`: `ListScopes`, then for every scope whose `State != store.ScopeRetired` `ScopeMembers(sc.Name)`, building `channelScope map[int64]string` over every listed channel (any kind); then `ListBridges`, keeping each live bridge with a non-empty `SlackChannelID` under `scopeBridges[channelScope[b.ChannelID]]` (a bridge whose channel is in no scope is skipped); `chanScope` becomes the entries of `channelScope` whose channel has `Kind != store.KindOrdinary` and whose scope has at least one bridge in `scopeBridges`; the state of every bridge absent from `scopeBridges` is deleted; finally every `unknown` channel now in `chanScope` marks its scope at `now` and `unknown` is cleared. No `b.Scope` or `ch.Scope` read: the scope of a bridge is the scope whose members include its channel.
  - `func (p *canvasPublisher) run(ctx context.Context, hints <-chan notify.Hint)`: loop: `next()` → a timer (none when nothing is due); `select` on `ctx.Done()`, `hints` (→ `markHint`), `wake`, the timer; on the timer: `take(now)`; when `""` is among the taken keys, `refresh` first (a failed refresh re-marks `""` at `now + window` and logs at warn); then for each taken scope, for each bridge in `scopeBridges[scope]`, `PublishCanvas(ctx, b, scope)`; a non-zero `retryAt` re-marks the scope at `retryAt`; an error is logged at warn with the scope, the channel and Slack's code (the `disabled` warnings are logged once by `handle`, not here).

- [ ] **Step 1: Write the failing runner tests** (subtests of `TestSlackBridgeCanvasRunner`, `dbtest.ForEachEngine`; fixture `newOutFix(t, e, func(c *slackbridge.Config) { c.CanvasEnabled = true; c.CanvasCoalesce = 20 * time.Millisecond })` plus a visible tasks channel)

`publishes-after-start`: `runBridge`; `waitFor(t, 10*time.Second, "the canvas", func() bool { return f.fake.Calls("CreateChannelCanvas") == 1 })`; the canvas label is `Status`. `hint-rebuilds-within-window`: with `CanvasCoalesce = 300 * time.Millisecond` and `LifecycleInterval = 50 * time.Millisecond`: after the create, add two tasks 10 ms apart; `waitFor` `Calls("ReplaceCanvas") == 1`; `time.Sleep(700 * time.Millisecond)`; still `== 1` (the lifecycle marks rebuild but the rendering is unchanged, so no call). `unknown-channel-hint`: create a new tasks channel in `job1` visible to the humans after the runner started, add a task: `waitFor` the canvas markdown to contain the new channel's name. `lifecycle-catches-lease-expiry`: claim a task with a one-second lease from a live session, `h.clock.Advance(2 * time.Second)` (no hint): `waitFor` the markdown to contain `stalled: lease expired`. `reconnect-marks-all`: `f.fake.Disconnect()` after the canvas exists and a task changed through `h.raw` with no hint (direct `Append`): `waitFor` the markdown change. `only-lock-holder-publishes`: `h2 := f.h.sibling(...)`, `svc2 := f.newService(h2, mod)`, both run; after `time.Sleep(time.Second)` `Calls("CreateChannelCanvas") == 1` and `len(ChannelCanvases(f.sl)) == 1`. `archived-bridge-skipped`: after the canvas exists, `f.fake.ArchiveByHand(f.sl)`, send a bus message so the drain archives the bridge; `waitFor` `f.bridge().State == BridgeArchived`; record the calls; add a task; `time.Sleep(500 * time.Millisecond)`; no canvas call grew. `creating-bridge-skipped` (Review Focus 3): a bridge row in state `creating` with an empty Slack channel in `job1` (inserted through `tx.CreateBridge` for a second scoped ordinary channel) never causes a canvas call for it. `removed-between-snapshot-and-publish` (Review Focus 4): `Remove` the bridge, then `markAll`; no call and no `slack bridge: canvas` error line. `disabled-setting`: `CanvasEnabled` false with the runner: after a second, every canvas `Calls` is 0.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/conformance/bus/ -run TestSlackBridgeCanvasRunner -count=1`
Expected: FAIL (no goroutine publishes).

- [ ] **Step 3: Implement the loop, the marks and the wiring.**

- [ ] **Step 4: Run the tests, the race detector on the package and the existing runner tests**

Run: `go test ./internal/conformance/bus/ -run 'TestSlackBridge' -count=1 && go test -race ./internal/slackbridge/ -count=1 && go test -race ./internal/conformance/bus/ -run 'TestSlackBridgeCanvasRunner|TestSlackBridgeRunner' -count=1 && go test ./internal/core/access/ -run TestAccessRuleSites -count=1`
Expected: PASS on both engines, no race.

- [ ] **Step 5: Commit**

```bash
git add internal/slackbridge/runner.go internal/slackbridge/lifecycle.go internal/slackbridge/canvas.go internal/conformance/bus/slackbridge_canvas_test.go
git commit -m "feat(slackbridge): publish the scope canvas from the runner session, coalesced"
```

---

### Task 8: Operator docs and the done gate

**Files:**
- Modify: `docs/ops/slack-bridge.md:11` (bot scopes: add `canvases:read` and `canvases:write`), `:17` (name the two settings), new section `## The scope canvas` before `## Running it`
- Modify: `docs/ops/configuration.md` (the two rows landed in Task 1; verify the `slack_canvas_enabled` row says "Ignored without the token files")
- Test: `internal/server/config/docs_test.go` (already runs), `make verify`

**Interfaces:** none.

- [ ] **Step 1: Write the section** `## The scope canvas` (facts only, each checked against the code): one canvas per live bridge in its Slack channel, tab `Status`, what it shows (the scope's task lists and memory channels every human who may post to the bridged channel may read; a channel one of them cannot read is left out entirely), the glyphs and the Needs attention section, read-only (`canvases.access.set` `read`, restored on every restart), the cadence (`slack_canvas_coalesce_seconds`, the 60 s lifecycle pass and every reconnect), the marker text and how a restart or a second replica finds the canvas by it, the Slack answers that disable the canvas until restart (plan tier, `canvas_disabled_user_team`, `missing_scope`, a tab without the marker, an unknown code) and the warning each logs once, a deleted canvas being recreated, an archived channel marking the bridge archived, the size cap behavior, that nothing deletes a canvas, and the limitation that a human's canvas quoting the marker text is adopted. State what a second run and two replicas do (Definition of Done D): the lock holder alone publishes; a replica that takes over republishes once.

- [ ] **Step 2: Run the docs tests**

Run: `go test ./internal/server/config/ -run 'TestConfigurationReference|TestComposeExample' -count=1`
Expected: PASS

- [ ] **Step 3: Run the done gate**

Run: `make verify`
Expected: every step passes, both engines; then push and watch `ci.yml` and `windows.yml` (`gh run watch`).

- [ ] **Step 4: Commit**

```bash
git add docs/ops/slack-bridge.md docs/ops/configuration.md
git commit -m "docs(ops): the Slack scope canvas"
```

---

## Self-review notes

- Spec 2.1 to 2.11: Tasks 4 (visibility), 6 and 7 (one canvas per bridge, read-only access, cadence, enablement), 5 (layout, glyphs, problems, memories, needs attention, title). Spec 3 facts drive Task 2's fake rules and the `conversations.info` tab decode. Spec 4 is Task 5; spec 5 is Tasks 3 and 4; spec 6.1 and 6.2 are Task 7; 6.3 and 6.4 are Task 6; 6.5 is Task 2; 7 is Task 1 (and Task 8 for the docs); 8 is Tasks 6 and 7; 10 and 11 are out of scope.
- Names used across tasks: `CanvasSnapshot`, `CanvasTaskList`, `CanvasTask`, `CanvasOwnerStatus`, `CanvasMemoryChannel`, `CanvasMemory` (Task 4 → 5, 6); `RenderCanvas`, `CanvasRendering`, `CanvasHash`, `CanvasMarker`, `CanvasTitle`, `canvasMaxBytes` (Task 5 → 6); `CanvasSnapshot(ctx, b, scope)` (Task 4 → 6); `PublishCanvas(ctx, b, scope) (time.Time, error)`, `canvasDirty.mark/next/take/len`, `canvasPublisher.markAll/markHint/refresh/run` (Task 6 → 7); `slackbridge.Config.CanvasEnabled/CanvasCoalesce` (Task 1 → 6, 7); the five `slackapi.Client` methods and `slackfake.AddCanvas/Canvas/DeleteCanvasByHand/EmptyTabsNext` (Task 2 → 6, 7).
- Allowlist: one entry (Task 3). `TestAccessRuleSites` runs in Tasks 3, 4, 6 and 7.
