# Milestone 8: TUI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `agentbus2 tui`, the v1 dashboard ported over the `bus.Bus` interfaces so that it runs unchanged in local mode and in server mode, with no inbox view (spec 1.1, 9, 10: Decision 60, Eric; ADR 0006; Decision (Eric, 2026-10-21), Milestone 6 item 4: "the Milestone 8 TUI runs over the `Bus` directly and filters for display"). Exit (spec 10): the TUI runs over the `Bus` interfaces in both modes with no inbox view, proven under `make verify` by a Docker-free test that drives the same TUI program over the remote `Bus` in each of the two modes of spec 2.1 as they are wired on the wire side (`local`: the generated client over `server.InProcessTransport` over a SQLite core, exactly Decision 35's local mode; `server`: the generated client over a real TCP listener (`httptest`) with a bearer minted by the server's own `token.Issuer`, server mode's HTTP long poll), by `RunTUI` tests over a local profile and a server profile through `client.Open`, by `TestNoInboxView` (the behavioral negative half: nothing in the rail selects a session or an identity, and every channel the TUI reads is one `ListChannels` listed), and by an exit script whose negative halves are proven in `check-gates-selftest.sh`. (`internal/bus/local`, the core-direct `Bus`, is not a product mode: Milestone 6's `client.Open` uses `remote.New` in both modes, so the unit tests of Tasks 2 to 7 use it as a fixture and the exit test does not count it.)

**Architecture:** One package `internal/tui`, ported from v1 `internal/tui` at one pinned tree: v1 commit `906dec8` (`v1.15.0-11-g906dec8`, HEAD of `/Users/efitz/Projects/agentbus` at the first planning round; the checkout has since moved to `6dde719`, which is why the pin matters; every "Port from v1" list, line range and stub signature below is read with `git -C /Users/efitz/Projects/agentbus show 906dec8:internal/tui/<file>`, never from the working tree, which may move). That tree holds the rail-sections design (v1 ADR 0024: five collapsible sections `channels`, `memories`, `tasks`, `tags`, `sessions` under headings, `headSel`, `collapsed`, `railLayout`), and it is ported with the sections keyed on `Kind` (ADR 0008): `channelSection` switches on `bus.KindTasks` and `bus.KindMemory` instead of `bus.IsTaskChannel` and the `"memory"` string, the `sessions` section is display-only (choice 4), and v1's `shortName` (which strips `general/`, `memory/`, `tasks/` from a row) is not ported: v2 names carry no kind prefix and rows show the full name. The model sees the bus only as `bus.Bus` plus the `bus.Session` it opened (a small `client` struct, as v1 `client.go`); nothing in `internal/tui` knows which transport is behind it: the entry `tui.Run` takes a `bus.Bus` the caller opened, and only `cli.RunTUI` (Task 10) touches `internal/client` and `internal/profile`. The data path follows Decision 83: a goroutine arms `Watch` (the long poll, which renews the lease) and drains `Receive` with `ack`, instead of v1's blocking `Receive`. Every task but Tasks 8 and 10 builds and tests against `main` as it is now (SQLite, `local.New`, `remote.New` over the in-process transport and over `httptest` with `token.Issuer` bearers, Milestone 5's auth being on `main`); only Tasks 8 and 10 wait on Milestone 6. Bubble Tea's model is one `Model` with a mode switch, as v1; the feature files (`thread.go`, `tasks.go`, `search.go`, `memories.go`, `tags.go`, `health.go`, `help.go`) are created as typed stubs by Task 3 so the feature tasks run in parallel without editing `model.go` or `view.go`.

**Tech Stack:** Go 1.27, `CGO_ENABLED=0`; `github.com/charmbracelet/bubbletea v1.3.10`, `bubbles v1.0.0`, `lipgloss v1.1.0`, `charmbracelet/x/ansi v0.11.6` (v1's pins; see choice 1); `internal/bus` and `internal/version` (all `internal/tui` imports of the module); `internal/client`, `internal/profile`, `internal/cli` from Milestone 6 in `internal/cli/tui.go` only.

**Spec:** `docs/superpowers/specs/2026-10-19-agentbus2-design.md` sections 1.1 (TUI in scope, Decision 60), 1.3 (no DMs, Decision 13), 2.2 and 2.3 (the TUI over the `Bus` interfaces), 3.2 (Identities, Admin rows), 3.5 (watch), 4.3 (display names, Decisions 15, 39, 40, 79), 4.4 (Decisions 37, 75), 4.6.1 (ACLs, Decision 80; `ListChannels` shows every channel, Decision 94), 4.7 (admin, Decision 73), 6.2 (profiles, Decisions 21, 71, 74), 9 (porting map row "TUI"; the not-ported list), 10 (Milestone 8); ADR 0003 (display names are server-assigned: "logs and the TUI must use the server-assigned name"), ADR 0006 (no inbox view), ADR 0008 (no name-derived rules: "nothing in ... the TUI inspects a prefix"; kinds, Decision 90), ADR 0005 (Decision 31). v1 design specs for intent: `docs/superpowers/specs/2026-09-08-agentbus-tui-design.md`, `2026-09-24-tui-task-editing-design.md`, v1 ADR 0011. Gate 0: `docs/superpowers/specs/2026-10-21-gate-0-decisions.md` (Milestone 6 item 4). Milestone plans whose interfaces this plan consumes: `2026-10-21-milestone-6-client-side.md` (Tasks 1, 5, 11), `2026-10-21-milestone-7-server-deployment.md` (Task 6).

## Task dependencies and parallelism

Tasks with no dependency between them run in parallel in separate worktrees and are cherry-picked onto `main`; every task's `Files:` list is disjoint from every task it can run beside.

| Wave | Tasks | External gate (on `main` before the task starts) |
| --- | --- | --- |
| 1 | 1 (deps, theme, icons, format helpers), 2 (session client, messages, fixture), 8 (TUI settings in `profile.Config`) | Task 8: Milestone 6 Task 1 (`internal/profile`) |
| 2 | 3 (model, view, compose, stubs) | none beyond Tasks 1 and 2 |
| 3 | 4 (threads, bodies, tag panes, memory versions), 5 (task lists and editing), 6 (search), 7 (health, help, editor) | Task 7: Milestone 7 Task 6 for the storage line (see Names assumed; omitted when absent) |
| 4 | 9 (entry over a caller-opened `Bus`, `TestBothModes`, the exit script and its selftest blocks) | none: `remote`, `server`, `gen`, `auth/token` and `auth/keys` are on `main` |
| 5 | 10 (`agentbus2 tui` through `client.Open`, the `TestRunTUI*` profile tests, docs, ADR addenda) | Milestone 6 Tasks 5 and 11 (`client.Open`, `cli.open`, `cli.Main`), Task 8 |

Shared-file owners: `go.mod` and `go.sum` belong to Task 1 alone (integrator rule against Milestone 6 Task 7 and any Milestone 7 `go.mod` commit: keep both requirement sets and re-run `go mod tidy` on a conflict, the Milestone 5 and 6 rule). `internal/profile/config.go` and `config_test.go` are edited by Task 8 only (an append to Milestone 6 Task 1's file, after it is on `main`). `scripts/m8-exit-checks.sh` and `scripts/check-gates-selftest.sh` are created or appended by Task 9 and then extended by Task 10 (sequential: Task 10 depends on Task 9); `.github/workflows/ci.yml` is appended by Task 9 only; Milestones 6 and 7 also append blocks to `check-gates-selftest.sh` and steps to `ci.yml`, so the integrator rule for both files is the `go.mod` one: keep every side's blocks and steps, each identified by its label, and re-run `make check-gates-selftest` after a conflict; `internal/cli/cli.go`, `main_test.go`, `CLAUDE.md`, `README.md` and the ADRs are edited by Task 10 only, each as an append after whatever is on `main` at landing time, identified by label or by name, never by a count. Within `internal/tui`: Task 3 creates `model.go`, `view.go`, `compose.go` and the seven feature files as stubs; Tasks 4 to 7 each rewrite only their own feature files (whole-file replacement of a stub Task 3 created) and add their own `_test.go` files; none of them edits `model.go`, `view.go`, `msgs.go` or `fixture_test.go`. A feature task that needs a `Model` field or a message type Task 3 did not declare has found a plan gap: it reports it instead of editing the shared file. Test helpers: a test helper used by the test files of two tasks that can run in parallel, or by a task that lands before the one whose file v1 defines it in, lives in Task 3's `model_test.go` (or Task 2's `fixture_test.go` when it never names `Model`), enumerated in those tasks' Interfaces blocks (a helper of an earlier task's file used by a later dependent task, `widths` in Task 1's `icons_test.go` used by Task 5, `headLine` in Task 3's `rail_test.go` used by Task 4, stays where it is); v1 defines several of them in one feature's test file and uses them in another's (`selectTaskChannel` in `tasks_test.go`, used by `search_test.go`; `followTags` in `tags_test.go`, used by `rail_test.go`; `agentReply` in `thread_test.go`, used by `view_test.go`; `stream` in `body_test.go`, used by `tasktree_test.go`; `drainAndAck` in `model_test.go`, used by `search_test.go`), and a port that left them where v1 has them would not compile in Wave 2 or would collide between parallel Wave 3 tasks. A helper used only by one task's own test files stays in that task's files (`contents` and `eq` in `thread_test.go`, `ids` and `sameIDs` in `tasktree_test.go`, `onMemory` and `streamHas` in `memories_test.go`, `ansiProfile`, `openTasks` and `task` in `taskedit_test.go`, `taskTree` in `tasks_test.go`, `sendSubject` and `onChannel` in `body_test.go`, `selectTagPane` in `tags_test.go`, `railLines`, `setRailChannels`, `toHeading`, `sectionedFixture`, `isArrowRow`, `isHeadingRow`, `selectedRailRow`, `spans`, `invisibleText`, `headLine` in `rail_test.go`, `msgAt` in `view_test.go`); a name used by two tasks' files and not in the enumerated lists is a plan gap, reported. Word rule for Tasks 3 to 10: a commit message may say `inbox` or `direct message` when it says what was not ported, but no non-test Go file under `internal/tui` (or `internal/cli/tui.go`) may contain `inbox` (any case), `dm/`, `DMPrefix`, `DMChannel`, `IsTaskChannel` or `tui_name`, comments included (the exit script's word check, Task 9 Step 4); a test that asserts their absence may spell them. Every `rg` check in this plan that must skip test files passes the **directory** (`internal/tui`) with `--glob '!*_test.go'`: a shell-expanded file list (`internal/tui/*.go`) bypasses ripgrep's globs and searches the test files too (checked: `rg --glob '!*_test.go' d/*.go` matches `d/a_test.go`). Task 3 is the largest task of the milestone (the model, the view, compose, seven stubs and seven test files, about half the ported lines): dispatch it with that in mind, and apply the escalation rule (a failed attempt retries once at higher effort, then on the next stronger model with the failure named) rather than splitting it, because its files are the ones every other task builds on.

## Global Constraints

- Module `github.com/ericfitz/agentbus2`, `go 1.27`, `CGO_ENABLED=0`; every bus method takes a `context.Context` (Decision 6). Work lands on `main`, nothing is pushed, Conventional Commits with the session's attribution trailer; each task commits only its own files, listed by path; a porting commit names its v1 source file (Decision 31). American English throughout.
- Import rule for `internal/tui` non-test code, `run.go` included: of this module, `internal/bus` and `internal/version` only, so never `internal/client`, `internal/profile`, `internal/gen`, `internal/gen/spec`, `internal/core/...`, `internal/server/...`, `internal/store/...`, `internal/bus/local`, `internal/bus/remote`, `internal/auth/...` or any other package of the module; and never kin-openapi (spec 2.3 `tui --> busif`). Enforced by `scripts/m8-exit-checks.sh` (Task 9) as an **allowlist**: any direct import with the prefix `github.com/ericfitz/agentbus2/` other than exactly `.../internal/bus` and `.../internal/version` is a violation, so a package added later (Milestone 6's `credential`, `procs`, `sessionstate`, `mcpserver`, `cli`) is caught without editing the script. Checked as **direct imports** (`go list -f '{{.Imports}}'`, never `-deps`: `client.Conn` links the server, so a transitive check proves nothing; Milestone 6's reasoning). Test files may import `internal/core`, `internal/core/notify`, `internal/core/channels`, `internal/store`, `internal/store/sqlite`, `internal/bus/local`, `internal/bus/remote`, `internal/server`, `internal/gen`, `internal/auth/token`, `internal/auth/keys` and `net/http/httptest` for fixtures. `internal/cli/tui.go` follows Milestone 6's rule for `internal/cli`.
- No name-derived rules (ADR 0008): a task list is a channel whose `Kind == bus.KindTasks`, a memory channel `bus.KindMemory`; v1's `bus.IsTaskChannel(name)`, `DMPrefix`, `DMChannel`, `shortName` (v1 `view.go:383`, the `general/`, `memory/`, `tasks/` stripping) and every `strings.HasPrefix(name, ...)` on a channel name are gone; rows show the full name. The one synthetic prefix that stays is the rail's own `tags:` entry (`tagPanePrefix`), which never reaches the bus. `format.go`'s `cut` keeps v1's `strings.HasPrefix(seq, "\x1b")` (escape-sequence parsing, not a channel name).
- Display names are the server's (ADR 0003): a sender renders as `Message.Display`, falling back to `Message.Sender` when empty; the TUI's own name is `Session.DisplayName`; a message is "yours" when `Message.Sender == identity`. The server sets `Message.Display` only when the `Send` names a session (`messaging/send.go`: `in.Session == ""` leaves it the bare identity), so every operation of the TUI that carries a session id passes `c.sessionID()`: `Send`, `Receive`, `Watch`, `Close` (`bus.SendInput.Session`, `ReceiveInput.Session`, `WatchInput.Session`, `CloseSessionInput.Session`: the four inputs on `main` with the field).
- Headless runs: Bubble Tea asks only a tty for its size, so a program over a buffer `Output` never receives a `WindowSizeMsg`, v1 `View()` returns `""` at width 0 and `showLeft()` needs width ≥ 60. Model tests set `width`/`height` on the model directly (v1 `newFixture`); runs through `Run` pass `Options.Width`/`Height` (Task 9).
- Subscriptions belong to the identity (Decision 37) and survive the TUI: subscribing from the TUI subscribes every session of that identity, and `s` (unsubscribe) is seen by them too. `Subscribe` on an existing subscription is a no-op (`ON CONFLICT DO NOTHING`, `internal/store/sqlstore/subscriptions.go`), so subscribing every channel at start never moves an existing cursor or acked mark. The TUI's acks advance the identity's acked mark (Decision 75). The docs (Task 10) therefore recommend a human identity distinct from the agents' (see pending item 2).
- The TUI never calls an admin operation unless its own identity is flagged `Admin` in `Identities()`; `Status` (and `Usage`) are `forbidden` otherwise (`core.RequireAdmin`), and a `forbidden` there is never a toast.
- (superseded 2026-10-25) `ListChannels` lists every channel, readable or not (Decision 94; `channels.Service.List` applies no ACL). A channel whose ACL denies `read` answers `forbidden` on `Subscribe` and `History`: the rail marks it `(no access)`, nothing retries it on the tick, and no toast repeats.
- Tests of `internal/tui` are Docker-free, under `make test`, and run with `-race`; every test that waits on a wake pins `core.Config.WatchMergeSeconds = 0` (else a wake sleeps 2 s) and uses `notify.NewMemory()`; no test sleeps for a fixed interval to wait, it polls with a deadline. Timers (`tea.Tick`) are swapped out through the package-level `tick` variable, as v1 `model_test.go` does, so no test command sleeps.
- `make verify` is the done gate for every task (Definition of Done A), run as `env -u AGENTBUS2_TEST_POSTGRES_DSN make verify` or with the variable naming a reachable server; `GOOS=windows GOARCH=amd64 go vet ./internal/tui/` whenever a build-tagged file is touched.

## Choices this milestone makes (delegated by the spec: section 10 says "Outline only"; recorded with rationale)

1. **Libraries and versions.** Bubble Tea v1.3.10, Bubbles v1.0.0, Lip Gloss v1.1.0, `x/ansi` v0.11.6: exactly v1's pins, so the port is line for line and every v1 test ports unchanged. A move to Bubble Tea v2 is a later `deps:bump`, not a porting commit. ANSI-only theme values as v1 (`#RRGGBB` rejected with the accepted forms named), the sixteen names plus `0`-`15` and `default`.
2. **Identity and session.** `agentbus2 tui` takes `client.Flags` (`-profile`, `-identity`) and resolves identity by Decision 71 and Milestone 6 choice 4, like every command; there is no `tui_name` and no `--as` (v1 derived the human's name from the OS user, which Decision 14 and 71 replace with the credential). The session is opened with `Hint: "tui"`, so the human shows as `<identity>/tui` (`<identity>/tui-2` for a second TUI; Decision 39) and agents can tell the human's session from the identity's agent sessions. `Close` runs on quit so the name is freed at once (v1 `EndSessions`). Local mode runs the maintenance loop (`client.Options.Maintenance: true`, as v1's TUI ran `StartBackgroundLoops`; Milestone 6 Task 5 bounds concurrent runners by the lease row).
3. **Data path** (Decision 83). The receive goroutine loops: `Watch{Session, After: floor, Timeout: 0, IncludeOwn: true}` (`Timeout: 0` is the server's cap, `WatchMaxSeconds`, 50 s by default, `delivery/watch.go`); on a wake (`Woke true`), `floor = WatchResult.Seq` and then `Receive{Session, Ack: prev, IncludeOwn: true}` until a result carries no messages and no gaps (a gaps-only result is sent to the model as a `batchMsg` like any other and the drain goes on: `receive.go` defers delivery until every gap is reported); a timeout (`Woke false`, a zero `WatchResult`) re-arms silently with `floor` unchanged, never set to the zero `Seq`. `AckIgnored` is not an error (the batch comes back `Redelivered` on the next call and `onBatch` dedupes by seq). `prev` is always the last result's `Batch`: a non-empty batch's token, or `""` after the empty call that ends a drain. That empty call carries the last batch's token, and `receive.go` honors an ack whether or not new messages exist (`matched := in.Ack != "" && in.Ack == sess.PendingToken` settles every cursor), so a completed drain leaves nothing pending and the identity's acked mark (Decision 75) is current as soon as the batch is on screen; only a TUI killed between a batch and its empty call leaves that batch pending, and a later session of the identity sees it as `Redelivered` (`onBatch` dedupes by seq). `session_lapsed` (the laptop slept) → `Open{ResumeToken}` once, then the loop continues; a second lapse before any successful call is an error. A real resume returns the **same** session `ID` (`sessions/open.go`: `ID: row.ID`) and a resume token that `resumeLapsed` stores for a later lapse; `prev` is reset to `""` after a resume (the pending batch, if any, comes back `Redelivered`). `replaced` or `session_closed` → the program quits with the message on stderr (nobody else may arm this session's watch; a retry would fight it). Any other error → one toast, `retryBackoff` (a package variable, `time.Second`; tests pin it lower) backoff, retry (v1's loop). Own messages are included so the human's sends arrive through the same path as everyone else's (v1). `Receive.Count` is 0 (the server default). The session is shared between the loop goroutine (which replaces it on a resume) and Bubble Tea's goroutine (which sends with it), so `client` reads and writes `session` under `mu` through `sessionID()` and `displayName()`; `close()` cancels the loop's context first and then issues `Close` on a fresh `context.WithTimeout(context.Background(), 2*time.Second)`, never on the canceled one.
4. **Rail sources and sections.** Every 5 s (`statusEvery`, v1) the tick fetches `ListChannels`, `Identities` and `TagSubscriptions`; when the own identity is `Admin`, also `Status` (notices, embedding state) and, once Milestone 7 Task 6 is on `main`, `Usage` (storage). The rail is v1 `906dec8`'s five sections under headings (`channels`, `memories`, `tasks`, `tags`, `sessions`; v1 ADR 0024), each collapsible from its heading (`enter`, `←`/`→`), `home` selecting the channels heading, a collapsed heading showing `(count[, N unread])`, collapsing not persisted; `channelSection` is keyed on `Kind` (`bus.KindTasks` → tasks, `bus.KindMemory` → memories, `isTagPane` → tags, else channels). The `sessions` section is display-only (every live session's display name, grouped under its identity; the TUI's own in the user color; sessions seen within the last hour shown idle with their age, v1 `idleSessionTTL`): with no inbox there is nothing to select there (ADR 0006), so `railPositions` lists the `sessions` heading and not its items (`↓` from the last tag row lands on the heading and stops), and v1's `paneSessions`, `sessSel`, `selectSession` and `toSessions` go; `tab` cycles rail, stream, compose. `Channel` carries no counts in v2 (Decision 94): the header reads `<channel> · N unread · N loaded` and v1's task-channel idle marker (`LastActivity`, `taskChannelIdle`) is not ported.
5. **Channel create prompt** `c`: `<name> [memory|tasks]` (kinds, Decision 90; v1 took `[memory]` and inferred tasks from the prefix). No ACL editing in the TUI: the ACL is set at creation by agents or the CLI; adding an editor is new scope.
6. **Task editing** (v1 ADR 0011): a task is editable when `Owner == ""` or `Owner == identity`; `space` cycles the draft status, `enter` saves through `Tasks.Update`, `esc` cancels, `t` takes through `Tasks.Claim`, `u` releases through `Tasks.Release` (v1 wrote `owner ""` through `TaskUpdate`; v2 has the operation), `a` assigns to an identity chosen from `Identities()` (owners are identities, Decision 81) through `Update{Owner}`; the bus's owner rules stay authoritative and a refusal is a toast.
7. **Settings** (pending item 1 for the location): a top-level `tui` object in `config.json`, `{"theme": "<name>", "themes": [...], "icons": "emoji|nerdfont|custom", "icon_map": {...}, "rail_width": N}`, loaded strictly with the rest of the file; `rail_width` validated `0`-`1000`, `icons` validated as one of `emoji`, `nerdfont`, `custom`, `theme` validated as `""`, `default` or the name of an entry of `themes`, and every `themes` name non-empty and unique, all at load (Definition of Done B: an unknown enum value or a dangling reference is rejected, not warned about; `LoadTheme`'s unknown-name warning stays as the fallback for a `Settings` built in code), color values and `icon_map` names validated by the TUI at start with one warning line each (v1 `LoadTheme`, `LoadIcons`). `<` and `>` save `rail_width` by re-reading `config.json`, setting the field and `Save`, so a profile written meanwhile is kept; two TUIs saving at once are last-writer-wins, each write a whole file through `Save`'s rename.
8. **Health overlay** shows what a client can know: version, the mode line (`local · <sqlite path>` or `server · <url>`, built by `RunTUI` from `client.Resolved`), identity, display name and session id, live sessions and channels by kind, the receive state (last batch, gaps, last error), the admin block when admin (notices, embedding backlog and errors, storage when `Usage` exists), theme and icons as resolved, the config path and the `tui` settings as JSON; `o` opens `config.json` in `$VISUAL`/`$EDITOR` (v1 `health.go`, `editor_unix.go`, `editor_windows.go`, `editor_cmdline.go`) and re-validates it on return. v1's server settings lines (task idle, task expiry, memory expiry, query timeout) are server configuration a client cannot read and are not shown.
9. **Package layout.** `internal/tui` (`doc.go`, `settings.go`, `theme.go`, `icons.go`, `icons_unix.go`, `icons_windows.go`, `format.go`, `client.go`, `msgs.go`, `model.go`, `view.go`, `compose.go`, `thread.go`, `memories.go`, `tags.go`, `tasks.go`, `search.go`, `health.go`, `help.go`, `editor_unix.go`, `editor_windows.go`, `editor_cmdline.go`, `run.go` (Task 9, over `bus.Bus`)), `internal/cli/tui.go` (`RunTUI`, Task 10), `docs/tui.md`, `scripts/m8-exit-checks.sh`.

## Claude proposals, pending Eric

Each is implemented the way stated (reversible) and marked in code with `// pending Eric: Milestone 8 item N`; none blocks the milestone.

1. **Where TUI settings live.** Decision 74 fixes `config.json` with a `profiles` map and says nothing about host-wide settings. Implemented: a top-level `tui` member of `config.json` (choice 7). Alternative: a separate `~/.config/agentbus2/tui.json`. Option A (as implemented) keeps one file and one strict loader; option B keeps Decision 74's file to profiles only.
2. **The TUI's identity.** Milestone 6 choice 4 makes a local profile without `identity` a three-way error, and a human who runs the TUI under the same identity as the agents shares their subscriptions and acked mark (Decisions 37, 75). Implemented: Decision 71 as is, plus one stderr line before the alternate screen opens and a health line when `Identities()` shows other live sessions of the TUI's identity: `agentbus2 tui: identity <i> has <n> other live session(s); subscriptions and acks are shared with them (pass -identity for a separate name)`. Proposal for Eric: should `agentbus2 tui` fall back to the OS user name (v1's `tui_name` default) when nothing selects an identity in local mode?
3. **Resume across TUI restarts.** A TUI that crashes leaves `<identity>/tui` on a lapsed row until retention (Gate 0 Milestone 3 item 11), so the next start is `<identity>/tui-2` for up to 72 h; a fresh session still catches up from the identity's acked mark (Decision 75), so nothing is lost. Implemented: no resume token is persisted. Proposal: persist it at `<data dir>/tui/<profile>/<identity>.json` (0600) and resume once, as the MCP server does (Decision 79).

(An earlier revision listed a fourth item, rail sections, on the premise that v1's rail-sections design was unimplemented at the pinned commit; v1 HEAD `906dec8` implements it, so it is ported in Task 3 (choice 4) and the item is withdrawn.)

## Not ported (Decision 31; spec 9's list and the consequences of ADR 0006 and 0008)

v1 `internal/tui/dm_test.go`, `dm_self_test.go`, every `bus.DMPrefix`/`bus.DMChannel` branch (`paneMsgs`'s merge, `jumpTo`'s session branch, `submitCompose`'s reply redirection, `chanStyle`'s inbox case, `renderHeader`'s `direct` case, `renderRails`' inbox unread counts, `setChannels`'s `dms` split, `replyAllowed`, the help rows naming direct messages), `paneSessions`, `sessSel`, `selectSession`, `sessionNames` as a selection source and the `secSessions` item branches of `railPositions`, `railPos`, `selectRailPos` and `sectionUnread` (choice 4: the heading stays, the items are drawn and not selectable), `shortName`, `railLabels`' use of it and `rail_test.go`'s `TestShortName`, `TestShortNameCollision`, `TestSelectedIdleSessionKeepsName` (ADR 0008; rows show the full name), `taskChannelIdle`, `bus.IsTaskChannel` in `channelSection` (kinds instead), `tui_name`, `defaultTUIName`, `--as` and `config.Load` in `Run` (choice 2), `StatusReport` as the rail's source for everyone (choice 4), `Session.Harness`, `HarnessVersion` and `Context` in the rail and health (v2 sessions carry none), `Channel.Messages` and `LastActivity` (Decision 94), `config.SaveTUIRailWidth`'s raw-JSON rewrite (the strict loader holds every key), `mcpserver.StartBackgroundLoops`' heartbeat (an armed watch renews the lease, Decision 42), `ReceiveResult.Expired` handling (idle expiry not ported, Gate 0 Milestone 3 item 4), `NoPollGuard` (Gate 0 item 9).

## Names assumed from Milestones 5, 6 and 7 (diffed against `main` before Task 8 and Task 10 start; a difference is reported, not papered over)

- Milestone 5 choice 17, **now on `main`** (`internal/core/identities`, commit `589d20a`): `identities.Service.RegisterLocal(ctx, name)` and `CreateIdentity` create the identity row **and its `general` subscription at the then `MaxSeq`** (`register.go` `createWithGeneral`), and the wire `Register` path (`registerTx`) uses the same helper. Task 10's sender `sam` therefore has `general` from registration in both profile tests, before any message of the test is sent, and needs no explicit subscribe. Nothing on `main` subscribes a creator to a channel it creates (`channels.Service.Create`) or an identity created by the raw `store.Tx.CreateIdentity(ctx, name, now)` to anything; Task 2's fixture and Task 9's test use the raw path on purpose (no `general` subscription, so every mark in those tests is set by the test itself) and subscribe `sam` by hand.
- Milestone 6 Task 1: `profile.Config{Path string; Profiles map[string]Profile}`, `profile.Load(path) (Config, error)` (strict, `DisallowUnknownFields`), `(Config).Save() error`, `profile.ConfigDir(environ)`, `profile.DataDir(environ)`, `profile.SQLitePath(p, dataDir)`.
- Milestone 6 Task 5: `client.Options{Environ, Cwd, Profile, Identity, Maintenance, Logger, ...}`, `client.Flags(fs, &o)`, `client.Open(ctx, o) (*Conn, error)`, `Conn.Bus bus.Bus`, `Conn.Resolved client.Resolved{Profile profile.Profile; Identity string; ConfigDir, DataDir string; ...}`, `Conn.Close() error`; `cli.newFlagSet`, `cli.parseExit`, `cli.fail`, `cli.open(ctx, o, stderr) (*client.Conn, int)` (applies `testClientOptions` last), `var cli.testClientOptions func(*client.Options)` (set by `newTestEnv` to pin `PollInterval` 50 ms and `WatchMergeSeconds` 0: the pins every 2 s bound in Task 10 rests on), the fixture `newTestEnv(t)`, `testEnv.Environ`, `testEnv.conn(t, identity)`, `testEnv.writeProfile(t, p)`.
- Milestone 6 Task 11: `cli.Main(ctx, args, environ, stdin, stdout, stderr) int` with its dispatch table and `usage` constant; `main_test.go`'s `TestUsageListsEveryCommand` literal list.
- Milestone 6 `server_test.go` pattern (Task 5): a real server on `127.0.0.1:0` in `open` registration mode, `remote.Register` with a key saved through `credential.Store.Save`, a server profile. Task 10 copies that setup; the names come from `main` at the time.
- Milestone 7 Task 6: `bus.Admin.Usage(ctx) (bus.Usage, error)`, `bus.Usage{Bytes, BudgetBytes int64; Evictable bool; Notice string}`. **Rule:** if `Usage` is not on `main` when Task 7 starts, Task 7 omits the storage line and the `usage` member of `statusMsg` stays unused; Task 7's report says so, and the line is added by a one-file follow-up to `health.go` later.

## Review Focus

1. A non-admin's 5 s tick must never toast `forbidden`: pinned by `TestStatusTickNonAdminIsQuiet` (Task 3).
2. A channel whose ACL denies `read` to the TUI's identity: the rail shows it `(no access)`, no toast repeats on every tick, selecting it shows one dim line: `TestNoAccessChannelIsMarkedOnce` (Task 2, the client state) and `TestNoAccessChannelInRail` (Task 3, the rail and the stream).
3. The laptop sleeps past the lease (90 s plus skew): the next `Watch` answers `session_lapsed`; the loop resumes once with the token and keeps the same display name: `TestReceiveLoopResumesLapsedSessionOnce` (Task 2).
4. The server is unreachable for a while in server mode: the loop toasts once per failure, backs off `retryBackoff` (1 s in production) per attempt and recovers without duplicating messages when the server returns: `TestReceiveLoopBacksOffAndRecovers` (Task 2, through a `bus.Sessions` double).
5. An 80x24 terminal with long server-assigned names (`pat/impl-plan-the-release-12`): every rail row and the status bar render as exactly one line, nothing wraps the status bar off screen: `TestNarrowTerminalKeepsOneLineRows` (Task 3, v1 `rail_test.go`'s selected-row test extended with long display names).
6. A `Send` without a session id renders as the bare identity (`messaging/send.go` sets `Display` only when `in.Session != ""`), which would silently break ADR 0003's "the TUI must use the server-assigned name" while every `Sender` assertion still passed: `submitCompose` passes `Session: m.c.sessionID()`, and `TestEnterSendsToSelectedChannel` (Task 3), `TestBothModes` (Task 9) and `TestRunTUILocalProfile`/`ServerProfile` (Task 10) assert `Display == "<identity>/tui"`, never `Sender`.
7. The session is replaced by `resumeLapsed` on the loop goroutine while `submitCompose` reads it on Bubble Tea's: every read goes through `sessionID()`/`displayName()` under `mu`, and `TestReceiveLoopResumesLapsedSessionOnce` calls `sessionID()` from the test goroutine while the stub's `open` is replacing the session, under `-race` (Task 2).
8. No inbox view, behaviorally and not only by spelling: `TestNoInboxView` (Task 3) walks every rail position and presses `enter`/`right` on each over a `countingBus`; no position is a session or an identity, and every channel passed to `History` or `Subscribe` is a name `ListChannels` returned (a `tags:` pane never reaches the bus). The exit script requires its `PASS` line (Task 9).

---

### Task 1: Dependencies, theme, icons, format helpers

**Depends on:** none.

**Files:**
- Modify: `go.mod`, `go.sum` (the four charm modules of choice 1 and their graph; `go mod tidy`)
- Create: `internal/tui/doc.go`, `internal/tui/settings.go`, `internal/tui/theme.go`, `internal/tui/icons.go`, `internal/tui/icons_unix.go` (`//go:build !windows`), `internal/tui/icons_windows.go`, `internal/tui/format.go`
- Test: `internal/tui/theme_test.go`, `internal/tui/icons_test.go`, `internal/tui/icons_windows_test.go` (`//go:build windows`), `internal/tui/format_test.go`

**Port from v1:** `internal/tui/theme.go` (`Theme`, `ParseColor`, `LoadTheme`, `set`, `Style`, `Highlight`, `sgrPrefix`, `SelBG`, `RailSel`), `internal/config/config.go` (`Theme` as `ColorTheme`, `DefaultTheme`, `Colors`, `FindTheme`, `ansiNames`, `ColorForms`, `ColorIndex`), `internal/tui/icons.go`, `icons_unix.go`, `icons_windows.go` (`iconVars`, `emojiIcons`, `nerdIcons`, `padIcon`, `setIcons`, `LoadIcons`; the gear icon's per-OS CSI treatment stays as v1 on both platforms), `internal/tui/view.go` at `906dec8` lines 17 to 21 (`itoa`), 29 to 62 (`fmtBytes`, `clock`, `stamp`), 168 to 201 (`cut`, whose `strings.HasPrefix(seq, "\x1b")` escape parsing stays), 723 to 735 (`shortDur`) and 985 to 991 (`window`); v1's `senderName(s string)` at line 22 (name mangling) is not ported, Task 3's `senderName(bus.Message)` replaces it; and the icon variables (`iconChat`, `iconMem`, `iconAgent`, `iconUser`, `iconIdle`, `iconTasks`, `iconArrow`, `iconError`, `taskPending`, `taskInProgress`, `taskCompleted`, `markSel`, `markOpen`), with their tests `theme_test.go`, `icons_test.go`, `icons_windows_test.go` and the helper cases of `view_test.go`.

**Interfaces (produces):**

```go
package tui // internal/tui
type Settings struct { Theme string; Themes []ColorTheme; Icons string; IconMap map[string]string; RailWidth int } // the tui object of config.json, already loaded; no json tags here (internal/profile owns the file shape, Task 8)
type ColorTheme struct { Name, Background, Text, Dim, Timestamp, Tag, Agent, User, Memory, Tasks, Health, Warn, Error, Selection, SelectionInactive, SelectionText, Unsaved string }
func DefaultTheme() ColorTheme            // v1 config.DefaultTheme values
func (t ColorTheme) Colors() [][2]string  // v1 order, keyed by the json names (background, text, dim, timestamp, tag, agent, user, memory, tasks, health, warn, error, selection, selection_inactive, selection_text, unsaved)
func (s Settings) FindTheme(name string) (ColorTheme, bool)
const ColorForms = "black, red, green, yellow, blue, magenta, cyan, white, their bright forms such as brightblack, 0-15, or default"
func ColorIndex(s string) (int, error)    // v1 config.ColorIndex: -1 for default, 0-15, errors "index out of range" / "unknown color"
type Theme struct { BG, Text, Dim, Stamp, Tag, Agent, User, Mem, Tasks, Health, Warn, Error, Sel, SelInactive, SelText, Unsaved lipgloss.TerminalColor; Name string; Sources map[string]string; IconSet string }
func ParseColor(s string) (lipgloss.TerminalColor, error)
func LoadTheme(s Settings, warn io.Writer) Theme // theme "" means "default"; an unknown name warns `agentbus2 tui: theme %q is not in themes; using default`; a bad value warns `agentbus2 tui: theme %q %s=%q is not a color (%v); accepted: %s; using %s`; always returns a usable Theme
func (t Theme) Style(c lipgloss.TerminalColor) lipgloss.Style
func (t Theme) Highlight(bg lipgloss.TerminalColor, line string, width int) string
func (t Theme) SelBG(focused bool) lipgloss.TerminalColor
func (t Theme) RailSel(focused bool, line string, width int) string
func LoadIcons(s Settings, warn io.Writer) string // emoji (default), nerdfont, custom; icon_map overrides; warnings as v1 with the agentbus2 prefix
// format.go: itoa, fmtBytes, clock, stamp, cut, shortDur, window, unchanged from v1
```

- [ ] **Step 1: Add the dependencies**

Run: `go get github.com/charmbracelet/bubbletea@v1.3.10 github.com/charmbracelet/bubbles@v1.0.0 github.com/charmbracelet/lipgloss@v1.1.0 github.com/charmbracelet/x/ansi@v0.11.6` (no `go mod tidy` here: nothing imports them yet, so `tidy` would drop them again and Step 3 would fail with `no required module provides package` instead of the intended error; Step 4 runs `tidy` once the imports exist).
Expected: the four requirements in `go.mod` (possibly marked `// indirect` until Step 4); `go build ./...` still passes.

- [ ] **Step 2: Write the failing tests**

`theme_test.go` (v1 `theme_test.go` ported, `config.Config` replaced by `Settings`): `TestColorIndexForms` (table: `cyan`→6, `BrightBlack`→8, ` 15 `→15, `default`→-1, `16`→error, `#fff`→error "unknown color"), `TestLoadThemeFallsBackPerValue` (a theme with `agent: "#00f"` warns one line containing `agent="#00f"` and `accepted:` and uses `cyan`; `Sources["agent"] == "cyan"`), `TestLoadThemeUnknownName` (warns `is not in themes`; `Name == "default"`), `TestLoadThemeEmptyIsDefault` (`Settings{}` → no warning), `TestRailSelNeverWraps` (a 40-rune line at width 16 renders one line). `icons_test.go`: v1's cases with `Settings{Icons, IconMap}`, except the second half of v1 `TestLoadIconsNerdfontWidthParity` (from `f := newFixture(t)` on: it renders a task list and the rail, needs `Model`, and moves to Task 5's `tasks_test.go` as `TestNerdfontTaskRowWidthParity`); its icon-value half (up to the `iconError` check) stays here, with `widths` defined in this file. `format_test.go`: v1 `view_test.go`'s `cut`, `fmtBytes`, `shortDur`, `window` cases, `TestFmtBytes` and `TestStampTodayAndPast`.

- [ ] **Step 3: Run to verify they fail**

Run: `go test ./internal/tui/`
Expected: build failure (`undefined: LoadTheme`; the charm imports resolve because Step 1 left the requirements in `go.mod`).

- [ ] **Step 4: Implement** as in Interfaces; `go mod tidy` (now that the imports exist it keeps the four modules and drops any `// indirect` marker); `gofmt`.

- [ ] **Step 5: Run the tests, the Windows vet and the gate**

Run: `go test -race ./internal/tui/ && GOOS=windows GOARCH=amd64 go vet ./internal/tui/ && go list -f '{{join .Imports "\n"}}' ./internal/tui | grep -E 'agentbus2/internal/' ; echo "exit=$?"` (expected `exit=1`: nothing of the module is imported yet), then `make verify` (600000 ms).
Expected: pass; `verify: OK`.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/tui/doc.go internal/tui/settings.go internal/tui/theme.go internal/tui/icons.go internal/tui/icons_unix.go internal/tui/icons_windows.go internal/tui/format.go internal/tui/theme_test.go internal/tui/icons_test.go internal/tui/icons_windows_test.go internal/tui/format_test.go
git commit -m "feat(tui): theme, icons and format helpers ported from v1 internal/tui/theme.go, icons.go and view.go, with Bubble Tea pinned at v1's versions

Decision 60 (Milestone 8). ColorIndex, DefaultTheme and Colors come from
v1 internal/config/config.go; the settings are a plain struct the entry
fills from config.json (Milestone 8 choice 7)."
```

---

### Task 2: Session client, messages and the package test fixture

**Depends on:** none (imports `internal/bus` only; the fixture builds a core over SQLite in tests).

**Files:**
- Create: `internal/tui/client.go`, `internal/tui/msgs.go`
- Test: `internal/tui/fixture_test.go` (the package-level fixture consumed by Tasks 3 to 7 and 9; Task 10's tests are in `internal/cli` and use Milestone 6's `newTestEnv` instead; cross-task helpers follow the test-helper rule of Task dependencies), `internal/tui/client_test.go`

**Port from v1** (at `906dec8`): `internal/tui/client.go` (`client`, `newClient`, `subscribe`, `isSubscribed`, `forget`, `known`, `receiveLoop`, `close`) re-keyed to a `bus.Session` and the Decision 83 loop; `internal/tui/msgs.go`; `internal/tui/client_test.go` and the fixture of `model_test.go` lines 1 to 170 (`fixture`, `newFixture`, `run`, `send`, `key`, `agentSend`, `receive`) over `local.New`.

**Interfaces (produces):**

```go
package tui
type subState int
const ( subUnknown subState = iota; subOn; subOff; subNoAccess ) // subOff: the user unsubscribed this run; subNoAccess: Subscribe answered forbidden
type client struct { b bus.Bus; identity string; session bus.Session; ctx context.Context; cancel context.CancelFunc; mu sync.Mutex; subscribed map[string]subState; floor int64; lapsedOnce bool }
const sessionHint = "tui"
func newClient(ctx context.Context, b bus.Bus, identity string) (*client, error) // Open{Hint: sessionHint}; ListChannels; subscribe(ch, "now") for each; returns the first non-forbidden error after Close
func (c *client) subscribe(ch bus.Channel, from string) error // idempotent per name; forbidden → subNoAccess and nil; any other error returned, state unchanged
func (c *client) isSubscribed(name string) bool
func (c *client) noAccess(name string) bool
func (c *client) forget(name string)   // subOff (after Unsubscribe)
func (c *client) known(name string) bool
func (c *client) sessionID() string    // c.session.ID under mu: the one way the model reads the session (Send, Close); never read the field directly off the loop goroutine
func (c *client) displayName() string  // c.session.DisplayName under mu
func (c *client) receiveLoop(send func(any)) // choice 3; send takes a tea.Msg but is typed any so this file imports no charm package. The Watch call's IncludeOwn line ends in `// watch-own` and the Receive call's in `// receive-own` (markers Task 9 Step 6b mutates by sed); prev = res.Batch after every Receive ("" after the empty call that ends a drain, so the drain's last call acks the last batch)
func (c *client) resumeLapsed() error  // Open{ResumeToken}; replaces c.session under mu; once per lapse (lapsedOnce reset by any successful call)
func (c *client) close() error         // cancel() first (a Watch in flight ends when its context is canceled; never blocks on the loop), then Close{Session: c.sessionID()} on a fresh context.WithTimeout(context.Background(), 2*time.Second): a Close on the canceled ctx would fail at once and leave the name taken
var errSessionLost = errors.New("session lost") // wraps replaced and session_closed; the loop sends sessionLostMsg and returns
var retryBackoff = time.Second                  // the wait after a failed call (choice 3); TestRetryBackoffIsOneSecond pins the production value, the loop tests set it to 100 ms and restore it in t.Cleanup (no test in the package calls t.Parallel)
// msgs.go, every message Update handles (one place, so feature tasks never add types):
type ( batchMsg struct{ res bus.ReceiveResult }; receiveErrMsg struct{ err error }; sessionLostMsg struct{ err error }; statusTickMsg struct{}
       statusMsg struct { channels []bus.Channel; identities []bus.IdentityInfo; tags [][]string; admin bool; status bus.Status; statusOK bool; err error }
       usageMsg struct { usage any /* *bus.Usage once Milestone 7 Task 6 is on main; sent by Task 7's usageCmd from the health overlay only */; err error }
       historyMsg struct { channel string; msgs []bus.Message; prepend bool; err error }; sentMsg struct { channel string; err error }
       searchMsg struct { query string; res bus.SearchResult; err error }; revisionsMsg struct { seq int64; revs []bus.Message; err error }
       configEditedMsg struct{ err error }; toastClearMsg struct{ seq int }; subscribedMsg struct { channel string; err error }
       tasksMsg struct { ch string; tasks []bus.TaskSummary; err error }; taskMsg struct { task bus.Task; err error } )
```

```go
package tui // fixture_test.go: the only package-level test fixture; it never names Model (Task 3), so it compiles in Wave 1
type busFixture struct { t *testing.T; core *core.Core; c *client; bus bus.Bus /* the TUI's own bus as eric */; sam bus.Bus; samSession bus.Session; ack string }
func newCore(t *testing.T) *core.Core // sqlite.Open on t.TempDir()/bus.sqlite; core.New(st, cfg, core.Options{Notifier: notify.NewMemory()}) with cfg = core.Defaults(), WatchMergeSeconds 0; channels.Service{Core: c}.EnsureGeneral; closed in t.Cleanup
func ensureIdentity(t *testing.T, c *core.Core, name string)      // c.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error { _, err := tx.Identity(ctx, name); if errors.Is(err, store.ErrNotFound) { return tx.CreateIdentity(ctx, name, c.NowMs()) }; return err }): the raw store path, which creates no general subscription (unlike identities.Service.CreateIdentity); t.Fatal on error
func identityBus(t *testing.T, c *core.Core, name string) bus.Bus // ensureIdentity, then local.New(c, name)
func pendingFor(t *testing.T, b bus.Bus, ch string) int64          // probe: Open{} (no hint, no token), Close it at once, and return the Pending entry for ch; t.Fatal when ch is absent from Session.Pending (an absent entry means "not subscribed", and reading it as 0 would let an ack assertion pass vacuously)
func setAdmin(t *testing.T, c *core.Core, name string)           // store.Tx.SetAdmin, for the admin cases
func newBusFixture(t *testing.T) *busFixture // newCore; sam = identityBus("sam"), sam creates channels dev (ordinary) and dev-notes (memory), then subscribes to dev, dev-notes and general with From: "oldest" (nothing on main subscribes a creator or a new identity to anything: channels.Service.Create does not, tx.CreateIdentity does not; without this every "sam receives" assertion in Tasks 3 to 7 fails) and opens a session; c = newClient(ctx, identityBus("eric"), "eric"); c.close in t.Cleanup
func (f *busFixture) samSend(ch, content string, tags ...string) bus.SendResult // with Session: f.samSession.ID
func (f *busFixture) samReply(ch string, to int64, content string) bus.SendResult // v1 thread_test.go agentReply, hoisted: view_test.go (Task 3) and thread_test.go, body_test.go (Task 4) use it
func (f *busFixture) receiveRaw() bus.ReceiveResult // one Receive{Ack: f.ack, IncludeOwn: true} as the loop would; records the new batch token in f.ack (mirrors the loop's ack variable)
func waitFor(t *testing.T, d time.Duration, cond func() bool) // polls every 10 ms, fails after d
type stubSessions struct { bus.Sessions; open func(context.Context, bus.OpenSessionInput) (bus.Session, error); watch func(context.Context, bus.WatchInput) (bus.WatchResult, error); receive func(context.Context, bus.ReceiveInput) (bus.ReceiveResult, error); closeFn func(context.Context, bus.CloseSessionInput) error }
type stubBus struct { bus.Bus; stubSessions } // a bus.Bus double for the loop tests: Sessions methods go to the stubs, everything else panics (never reached)
type countingBus struct { bus.Bus; mu sync.Mutex; calls map[string]int; channels map[string][]string } // wraps a real bus; overrides ListChannels, Identities, CreateChannel, Subscribe, History and Status (and Usage once it exists), each counting its name under mu (Subscribe and History also append in.Channel to channels[name]) and then delegating; count(name) int and channelsOf(name) []string read them
// Task 3's model_test.go defines `type fixture struct { *busFixture; m Model }`, newFixture, run, send, key, receive (receiveRaw fed to the model as batchMsg) and the cross-task helpers enumerated in Task 3's Interfaces on top of this file
```

- [ ] **Step 1: Write the failing tests**

`client_test.go`: `TestNewClientOpensTuiSessionAndSubscribesAll` (`c.session.DisplayName == "eric/tui"`; `Identities()` through sam lists `eric` with one session `eric/tui`; `isSubscribed` true for `general`, `dev`, `dev-notes`; a second `newClient` as `eric` gets `eric/tui-2`), `TestSubscribeIsIdempotentAndKeepsOff` (`subscribe` twice makes one call: assert through `countingBus.count("Subscribe")`; after `forget`, `known` true and `isSubscribed` false), `TestNoAccessChannelIsMarkedOnce` (Review Focus 2: sam creates `private` with `ACL: []bus.ACLEntry{{Identity: "sam", Permissions: []bus.Permission{bus.PermRead, bus.PermWrite, bus.PermAdmin}}}`; `newClient` succeeds; `noAccess("private")` true; `subscribe` again returns nil and makes no second call), `TestReceiveLoopWatchesThenReceivesAndAcks` (the real core: the loop runs in a goroutine with a recording `send`; sam sends on `dev`; a `batchMsg` with the message arrives within 1 s; a second send arrives as a second batch; the first batch was acked (the second `Receive` carried its token: assert `Redelivered == false` and `waitFor` until `pendingFor(t, f.bus, "dev")` is 0, the identity-level acked mark of Decision 75, after first asserting it was 1 while the first batch was unacked: the test drives this case with a `send` that blocks on the first `batchMsg` until the test has probed)), `TestReceiveLoopIncludesOwnSends` (eric sends; the batch carries it), `TestReceiveLoopResumesLapsedSessionOnce` (Review Focus 3 and 7: `stubBus` whose `watch` answers `session_lapsed` once then a wake, `open` records the `ResumeToken`, blocks on a channel the test releases, and returns a session with the same `DisplayName`, a new `ResumeToken` and (a stub artifact, so the replacement is observable; a real resume keeps the `ID`, choice 3) a new `ID`; a second lapse later in the same test must present the new token, not the first; while `open` is blocked the test calls `c.sessionID()` from its own goroutine (the old id) and after the release `waitFor` sees the new id: the read is under `mu`, so `-race` is quiet; the loop resumed once and continued; a `watch` that answers `session_lapsed` twice in a row makes the loop send `receiveErrMsg` and keep retrying, never `Open` twice without a success between), `TestReceiveLoopStopsOnReplaced` (`watch` answers `replaced` → one `sessionLostMsg` wrapping `errSessionLost`, the loop returns; `session_closed` likewise), `TestReceiveLoopBacksOffAndRecovers` (Review Focus 4: `retryBackoff` pinned to 100 ms; `watch` fails three times with a transport error then wakes; `receiveErrMsg` sent once per failure, consecutive ones at least 100 ms apart, then the batch arrives), `TestRetryBackoffIsOneSecond` (`retryBackoff == time.Second` before any pin: the production value of choice 3), `TestWatchTimeoutKeepsFloor` (`stubBus` whose `watch` answers `WatchResult{Woke: false}` once, then a wake with `Seq` 7: the second `WatchInput.After` equals the first, and the third is 7), `TestCloseFreesTheName` (`close` while a `Watch` is in flight; `Identities()` through sam no longer lists `eric` (the `Close` went out on its own context after the cancel); the `Watch` returned within 500 ms of `close`), `TestDrainAcksLastBatch` (choice 3: sam sends two messages; the loop delivers them; `waitFor` until `pendingFor(t, f.bus, "dev")` is 0 with no further send: the empty call that ended the drain carried the last token, so nothing stays pending).

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/tui/ -run 'TestNewClient|TestSubscribe|TestNoAccess|TestReceiveLoop|TestClose|TestDrain|TestRetryBackoff|TestWatchTimeout'`
Expected: build failure (`undefined: newClient`).

- [ ] **Step 3: Implement** as in Interfaces. State (Definition of Done D): two TUIs on one identity are two sessions (`/tui`, `/tui-2`) with their own cursors; a TUI killed without `close` leaves a lapsed row the server retains (pending item 3); a restart opens a new session that starts from the identity's acked mark; a drain ends with an empty `Receive` carrying the last batch's token, so a completed drain leaves nothing pending and `prev` is `""`; a TUI killed between a batch and that call leaves the batch pending and the identity's next session sees it `Redelivered`, deduped by seq in `addMessages`.

- [ ] **Step 4: Run the tests, the import rule and the gate**

Run: `go test -race -count=3 ./internal/tui/ -run 'TestNewClient|TestSubscribe|TestNoAccess|TestReceiveLoop|TestClose|TestDrain|TestRetryBackoff|TestWatchTimeout'`, then the allowlist import rule `go list -f '{{join .Imports "\n"}}' ./internal/tui | grep '^github.com/ericfitz/agentbus2/' | grep -vxE 'github.com/ericfitz/agentbus2/internal/(bus|version)' ; echo "exit=$?"` (expected `exit=1`: no module import outside the two allowed), then `make verify` (600000 ms).
Expected: pass; `exit=1`; `verify: OK`.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/client.go internal/tui/msgs.go internal/tui/fixture_test.go internal/tui/client_test.go
git commit -m "feat(tui): the TUI's bus session over bus.Bus: open with hint tui, subscribe every channel, watch then receive with ack

Ports v1 internal/tui/client.go and msgs.go. Decision 83 replaces the
blocking Receive with Watch then Receive; session_lapsed resumes once
(Decision 79); replaced and session_closed end the program; a channel
whose ACL denies read is marked, never retried (Decisions 80, 94)."
```

---

### Task 3: Model, view, compose, the stubbed feature files

**Depends on:** 1, 2.

**Files:**
- Create: `internal/tui/model.go`, `internal/tui/view.go`, `internal/tui/compose.go`, and the stubs `internal/tui/thread.go`, `internal/tui/memories.go`, `internal/tui/tags.go`, `internal/tui/tasks.go`, `internal/tui/search.go`, `internal/tui/health.go`, `internal/tui/help.go` (each holding exactly the names in its owning task's Interfaces block, with the minimal body stated there; Tasks 4 to 7 give names, not signatures, so each stub's signature is derived mechanically from v1's call sites in `model.go`, `view.go` and `compose.go` at `906dec8` (`git -C /Users/efitz/Projects/agentbus show 906dec8:internal/tui/model.go`, likewise `view.go`, `compose.go`), with the v2 type substitutions of this plan (`bus.Message`, `bus.TaskSummary`, `bus.Task`, `bus.Kind`); a stub file's header comment reads `// Stub created by Milestone 8 Task 3 from the call sites in v1 906dec8 model.go, view.go and compose.go; Task N replaces this file.` and lists each stub's signature with the v1 call site it came from (`model.go:1133 showSelected`); `go vet ./internal/tui/` passes before the commit, so a feature task never finds a signature its callers disagree with)
- Test: `internal/tui/model_test.go` (`fixture` and `newFixture` on top of Task 2's `busFixture`, plus v1 `model_test.go`'s cases), `internal/tui/view_test.go`, `internal/tui/compose_test.go`, `internal/tui/rail_test.go`, `internal/tui/header_icon_test.go`, `internal/tui/sendericon_test.go`, `internal/tui/embed_status_test.go`

**Port from v1** (every list at `906dec8`): `internal/tui/model.go` (1459 lines; everything but the `sessSel`, `selectSession`, `paneSessions`, `dms` and `replyAllowed` paths and the `secSessions` item branches; it includes the rail-section code `type railPos` (line 756), `railPositions` (760), the `railPos()` method (783), `shownPos` (792), `headingFor` (805), `stepRail` (814), `selectRailPos` (831), `toggleSection` (848), `sectionUnread` (856), plus `paneLen` (875), `placeTaskCursor` (927), `subscribeCmd` (1365), `composeHeight` (1450)), `view.go` (lines 63 to 167: `agentLabel`, `senderIcon`, `channelLabel`, `tagChips`, `chipText`, `header`; 202 to 722: `rowLine`, `showLeft`, `railWidth`, `View`, `toastLine`, `chanStyle`, `renderHeader`, the section constants `secChannels` to `railSections` (344 to 351), `railFixed` (356), `railHeadings` (364), `channelSection` (368, re-keyed on `Kind`), `railLabels` (403, without `shortName`), `sectionRange` (427), `railLayout` (449), `railWindow` (495), `railSpans` (537), `scrollRail` (565), `renderRails` (583); 736 to 984: `renderStream`, `renderReplyBanner`, `renderCompose`, `renderStatusBar`, `hints`, `overlaySize`, `overlay`; there is no `railBudgets` in this tree, and `shortName` (383) and `taskChannelIdle` (715) are not ported), `compose.go` (`promptState`, `submitCompose` (27), `openPrompt` (59), `updatePrompt` (71), `createChannelPrompt` (92), `toggleSubscribe` (113), `updateConfirmChannel` (139), `confirmChannelLine` (161)), and their tests.

**Interfaces (produces):**

```go
package tui
type mode int
const ( modeInsert mode = iota; modeNormal; modeSearch; modeHealth; modeHelp; modeConfirmChannel; modeAssign )
type pane int
const ( paneChannels pane = iota; paneStream; paneCompose ) // no paneSessions (choice 4)
const ( historyPage = 200; statusEvery = 5 * time.Second; toastFor = 5 * time.Second; idleSessionTTL = time.Hour; railMin = 16; railStep = 2; railMax = 1000; streamMin = 40 )
var tick = tea.Tick
type Host struct { ConfigPath string; Describe string /* "local · <path>" or "server · <url>" */; RailWidth int; SaveRailWidth func(int) error /* nil: not saved, no toast */; ReloadConfig func() error /* re-validates config.json after the editor closes (Task 7); nil: not checked */; Warnings []string /* lines the entry printed before the alternate screen; shown in health */ }
type Model struct { c *client; theme Theme; host Host; width, height int; mode mode; lost error /* set by sessionLostMsg; the entry returns it */
    channels []bus.Channel /* sorted by section then name, sections secChannels..secTags contiguous (v1 setChannels); tag panes appended as Kind "tags" */; kinds map[string]bus.Kind; sel int
    railTop [railSections]int /* each section's scroll offset */; headSel int /* the selected rail heading (a section index), -1 when an item is selected */; collapsed [railSections]bool /* sections drawn as their heading alone; this run only */
    identities []bus.IdentityInfo; sessionsSeen map[string]time.Time /* display name → last listed */; admin bool; status bus.Status; statusOK bool; usage any; statusErr error
    msgs map[string][]bus.Message; gaps map[string][]bus.Gap; loaded map[string]bool; seen map[string]int64; divider int64
    cursor, cursorLine, cursorEnd int; expanded map[int64]bool; peek map[int64]int64; bodyOpen map[int64]bool
    tasks map[string][]bus.TaskSummary; taskOpen, taskExpanded map[int64]bool; taskDetail map[int64]bus.Task; pendingTaskCursorCh string; pendingTaskCursorID int64; draft taskDraft; assign assignPicker
    stream viewport.Model; follow bool; compose textarea.Model; replyTo *bus.Message; lastSent string; prompt promptState
    lastBatchAt time.Time; gapCount int; receiveErr error; toast string; toastSeq int; toastHint bool; lastNotice string
    search searchState; mem memView; health healthState; helpScroll int }
func New(c *client, th Theme, h Host) Model
func (m Model) Init() tea.Cmd                                   // statusCmd, statusTick
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd)        // v1 update: every msgs.go type handled here; sessionLostMsg → m.lost = err and tea.Quit (the entry prints it); usageMsg → m.usage = msg.usage when msg.err == nil, else left as is, never a toast
func (m Model) View() string
func (m *Model) kind(name string) bus.Kind                      // m.kinds[name]; "tags" panes answer Kind("tags")
func (m *Model) isTasks(name string) bool                      // kind == bus.KindTasks: the one replacement for v1 bus.IsTaskChannel
func (m *Model) selName() string; func (m *Model) selected() *bus.Channel
func (m *Model) unread(ch string) int; func (m *Model) markSeen(ch string); func (m *Model) dividerFor(ch string) int64
func (m *Model) selectChannel(i int) tea.Cmd; func (m *Model) showSelected() tea.Cmd
func (m *Model) addMessages(ch string, in []bus.Message); func (m *Model) onBatch(res bus.ReceiveResult) tea.Cmd
func (m *Model) onStatus(msg statusMsg) tea.Cmd                  // channels, identities, tags; sessionsSeen by display name; admin from the own IdentityInfo
func (m *Model) setChannels(chans []bus.Channel, tags [][]string, from string) tea.Cmd
func (m *Model) statusCmd() tea.Cmd                             // one command: ListChannels, Identities, TagSubscriptions, and then, in the same command, Status when the own row of that Identities result has Admin set (not m.admin, which is last round's answer and would make the first admin round skip Status; statusOK false on any error, never a toast). Usage is not fetched here: Task 7's usageCmd (health.go) sends usageMsg when the overlay opens, and Update stores msg.usage in m.usage (an error leaves it nil, never a toast)
func (m *Model) loadHistory(ch string, before *int64) tea.Cmd   // History{Channel, Before, Count: historyPage}; forbidden → the channel is marked no access through c, one toast
func (m *Model) showToast(s string) tea.Cmd; func (m *Model) showHint(s string) tea.Cmd; func errText(err error) string // a *bus.Error renders its Message
func (m *Model) pane() pane; func (m *Model) paneKey(msg tea.Msg) tea.Cmd; func (m *Model) focusPane(p pane) tea.Cmd
func (m *Model) moveCursor(d int) tea.Cmd; func (m *Model) placeCursor(seq int64); func (m *Model) scrollCursorIntoView(); func (m *Model) scrollOpenedIntoView(); func (m *Model) scrollStream(msg tea.Msg) tea.Cmd; func (m *Model) loadOlder() tea.Cmd
func (m *Model) layout(); func (m *Model) fitCompose(); func (m *Model) refreshStream(); func (m *Model) resizeRail(delta int) tea.Cmd // saves through host.SaveRailWidth
func (m *Model) updateInsert(msg tea.Msg) tea.Cmd; func (m *Model) updateNormal(msg tea.Msg) tea.Cmd // the full v1 906dec8 keymap minus r/i inbox cases: q, i, enter, esc, up, down, right, left, tab, shift+tab, home, pgup, pgdown, g, G, space, r, c, s, t, u, a, d, /, ., ,, <, >, h, ?; on a rail heading enter toggles its section and left/right collapse/expand it, home selects the channels heading, tab from the stream returns to a selected heading (v1 ADR 0024)
// view.go: senderName(x bus.Message) string (Display, else Sender); (m Model) isOwn(x bus.Message) bool (Sender == identity); renderRails draws channels (icon by kind, unread, "(off)", "(no access)"), tags, sessions (one row per live session from m.identities: display name, "(you)" for the own session id, identity dim when it differs from the display prefix; idle rows from sessionsSeen with shortDur); renderHeader: "<label> · N unread · N loaded"; renderStatusBar: "agentbus2 v<version>  <describe>  as <display name>" plus the admin embed state when statusOK, else nothing
// compose.go: submitCompose (Send{Channel, Content, ReplyTo, Session: m.c.sessionID()}: the session id is what makes the server stamp Message.Display with the server-assigned name (Review Focus 6), and sessionID() stays current after resumeLapsed; a session_lapsed answer is one toast, the loop's resume fixes the next send; a memory channel's send is a memory, as the server decides), createChannelPrompt "<name> [memory|tasks]" → CreateChannel{Name, Kind} then subscribe(ch, "oldest"), toggleSubscribe (Unsubscribe/Subscribe on the identity), updateConfirmChannel (y → DeleteChannel; forbidden toasts)
// stubs created here (bodies stated in the owning task): thread.go (row type; rows flat: one row per message, depth 0; paneMsgs = msgs[ch] or tagPaneMsgs; cursorRow; openCursor/closeCursor/collapseCursor/peekReply/rootSeq/rowAvail/setExpanded/nextParent as no-ops returning zero values), memories.go (memView{seq int64; revs []bus.Message; idx int}; version(x) (x, false); isVersionKey; stepVersion nil; applyRevisions nil), tags.go (v1's real bodies of the pure helpers the rail and `setChannels` need: tagPanePrefix, tagReadOnlyToast, isTagPane, tagPaneSet, splitTags; stubs: tagPaneSources = nil, tagPaneMsgs = nil, tagPrompt nil, unfollowTagPane nil; Task 4 still replaces the whole file), tasks.go (taskDraft, assignPicker, taskRow; loadTasks/loadTask return nil; renderTasks returns ""; every other method a no-op), search.go (searchState with input textinput.Model, mode, query, hits, cursor, ran, textOnly, semanticDown, err; openSearch sets modeSearch; updateSearch esc → modeNormal; runSearch nil; jumpTo nil; viewSearch = overlay("search", ...)), health.go (healthState{lastQueryAt time.Time; lastQueryOK bool; scroll int}; recordQuery; openHealth sets modeHealth and returns m.statusCmd(); usageCmd returns nil; updateHealth esc/h → modeNormal; healthLines returns the version line only; viewHealth), help.go (openHelp, updateHelp, helpLines = the v1 keymap rows minus direct-message rows, viewHelp)
```

- [ ] **Step 1: Write the failing tests**

`model_test.go`: `type fixture struct { *busFixture; m Model }`; `newFixture(t)` = `newBusFixture` plus `f.m = New(f.c, LoadTheme(Settings{}, io.Discard), Host{})`, `width 100, height 32`, `f.run(f.m.Init())`, `f.key("i")`; `run`, `send`, `key`, `receive` (`receiveRaw` fed as `batchMsg`), `moveTo` from v1 (`model_test.go:97`), the `init()` that nils `tick`, and the cross-task helpers hoisted here (Global rule on test helpers): `drainAndAck` (v1 `model_test.go:193`: `Receive{Session: f.c.sessionID(), IncludeOwn: true}` then, when `Batch != ""`, the same with `Ack: Batch`), `selectTaskChannel` (v1 `tasks_test.go:17`, bounded by `len(f.m.channels)`, no `dms` term), `followTags` (v1 `tags_test.go:86`: `f.c.b.SubscribeTags(ctx, bus.TagSubscribeInput{Patterns: s})` per set, then `f.run(f.m.statusCmd())`), `stream` (v1 `body_test.go:22`: `ansi.Strip(f.m.renderStream())`). `newFixtureWith` is not ported (its only use was a v1 `config.Config`). Cases ported from v1 `model_test.go` without their DM/session-pane cases: `TestStartsOnChannelList`, `TestEnterSendsToSelectedChannel` (sam, subscribed to `dev` by the fixture, receives `hello` with `Display == "eric/tui"`: assert `Display`, not `Sender`, because a `Send` without the session id would still arrive with `Sender == "eric"` and `Display == "eric"`, Review Focus 6), `TestReceiveShowsMessageAndUnread`, `TestTabCyclesRailStreamCompose` (three panes, wrapping; `home` selects the channels heading, v1 906dec8's `TestHomeSelectsChannelsHeading`; `tab` from the stream returns to a selected heading), `TestUnreadDividerPlacedOnce`, `TestGapRendersEvictedDivider`, `TestToastClearsOnKey`, `TestSubscribeToggle` (`s` → `(off)` and the server's subscription gone: a probe `Open{}` for `eric`, closed at once, lists `dev` in `Pending` no more, and before the toggle it did (so the absence is the toggle's doing); `s` again resubscribes), `TestCreateChannelPromptKinds` (`c`, `notes memory` → a channel of `bus.KindMemory` in the memories section with the memory icon; `todo tasks` → `bus.KindTasks` in the tasks section; `bad/name` → a validation toast; `x foo` → the core's `KindFromBus` validation toast and no channel `x`; `x memory extra` → the prompt's own usage toast `usage: <name> [memory|tasks]` and no `CreateChannel` call (`countingBus`); an empty line → the same usage toast; Definition of Done B), `TestDeleteChannelConfirm` (`d`, `y` → gone; `d` on `general` → `forbidden` toast), `TestStatusTickNonAdminIsQuiet` (Review Focus 1: three `statusCmd` rounds as a non-admin over a `countingBus`, `m.toast == ""`, `statusOK false`, `count("Status") == 0`, and `count("Identities") == 3` so the three rounds provably ran), `TestStatusTickAdminShowsNotices` (`setAdmin(t, core, "eric")` and a notice planted through `store.Tx.SetNotice(ctx, store.Notice{Kind: "capacity", Message: "store at 95%", SetAt: now})` (`Status` reports the kinds `capacity` and `embedding` only, `channels/admin.go`); one `statusCmd` round suffices because the command calls `Status` on the own row's `Admin` flag it just fetched; run two rounds anyway and assert after each: `statusOK true`, `m.admin true`, the notice text in the status bar, `count("Status")` 1 then 2), `TestNoAccessChannelInRail` (Review Focus 2: sam's `private` channel with an ACL naming only `sam`; the rail row reads `private (no access)` in the dim style; selecting it renders one dim line `no read access to private`; three `statusCmd` rounds and one `loadHistory` produce one toast at most and `count("Subscribe")` does not grow), `TestUsageMsgStoredQuietly` (`usageMsg{err}` leaves `m.usage` nil and no toast), `TestOwnSessionIsYou` (the sessions rail shows `eric/tui (you)` in the user color and `sam` in the agent color), `TestSharedIdentityWarningLine` (pending item 2: `Host.Warnings` rendered in health; the line itself is built by `cli.RunTUI`, Task 10), `TestSessionLostQuits` (`sessionLostMsg` → the returned command, when run, yields a `tea.QuitMsg` (`_, ok := cmd().(tea.QuitMsg)`; funcs are not comparable, so never compare against `tea.Quit`) and `m.lost` holds the error). `view_test.go`: v1's `TestViewShowsRailsStreamComposeAndStatus` (expected strings: `channels`, `memories`, `tasks`, `tags`, `sessions` (the five headings), `dev`, `iconMem+"dev-notes"` under `memories`, `hello from sam`, `sam`, `eric/tui`, `as eric/tui`, `esc commands`, `dev ›`), `assertStatusBarIsLastLine` (the last line contains `agentbus2 v`), `TestNarrowTerminalKeepsOneLineRows` (Review Focus 5: width 80, height 24, a sam session opened with `Hint: "impl-plan-the-release-12"`; every rail row and the status bar is one line with the default and the nerdfont icon sets; selected and unselected), `TestStatusBarShowsVersion`, and every other v1 `view_test.go` case at `906dec8` except: `TestTaskChannelIdleMarker` (not ported), `TestSessionRowShowsRepoOnlyWhenItDiffers` (v2 sessions carry no repo or context), the direct-message half of `TestHeaderShowsSenderArrowChannelAndDM` (ported as `TestHeaderShowsSenderArrowChannel`), `TestFmtBytes` and `TestStampTodayAndPast` (Task 1's `format_test.go`), and `TestReplyIndentAppliesToWrappedLines` (it opens a reply thread, which the `thread.go` stub flattens: it moves to Task 4's `thread_test.go`); `setChannelLastActivity` is not ported. `TestRailKeysResizeAndPersist` and its siblings assert through a `Host.SaveRailWidth` that records the width (v1 wrote the config file). The rule for any other v1 case of Task 3's files that turns out to exercise a stub's real behavior: it moves to the owning feature task's test file and the move is named in the commit message; it is never weakened to pass against the stub. `TestNoInboxView` (`model_test.go`; Review Focus 8): sam creates `dev`, `todo` (tasks) and `dev-notes` (memory), sam and a second sam session (`Hint: "impl"`) are live, the TUI follows one tag set; over a `countingBus`, walk the rail with `home` then `down` until the selection stops moving (bounded by 4 × the rail's row count), pressing `enter` and `right` at each stop and `esc` back to the rail; assert (a) no stop has `m.selName()` equal to a session display name or an identity name that is not also a channel name, (b) on the `sessions` heading `enter` only toggles `collapsed[secSessions]` and makes no bus call (the counts before and after are equal), (c) every name in `channelsOf("History")` and `channelsOf("Subscribe")` is in the last `ListChannels` result (no channel is synthesized from an identity or a session), (d) `mode` never takes a value outside the seven declared constants; and, so that (a) and (c) are not true of an empty walk, (e) the set of `m.selName()` values seen at item stops equals the `ListChannels` names plus the one `tags:` pane, and `count("History") >= 1` after the walk. `rail_test.go`: v1 906dec8's cases except the three not ported (Not ported): `TestRailWindowScrollsByOneWithArrows`, `TestRailLayoutFitsAll`, `TestRailLayoutKeepsFourRowsPerSection`, `TestRailLayoutDropsBlanksFirst`, `TestRailLayoutCompact`, `TestRailLayoutTooShortStillFloors`, `TestRailLayoutCollapsedGetsNothing`, `TestRailWindowCompactCombinedArrow`, `TestRailCompactArrowRow`, `TestRail24LinesShowsEverySection`, `TestRailShowsEverySectionHeading`, `TestRailSelectedRowStaysOneLine`, `TestRailScrollsLongChannelList`, `TestSelectedRowsStayOneLine`, `TestChannelSection` (table over `bus.KindTasks`, `bus.KindMemory`, ordinary and a `tags:` pane: the one place the section rule is pinned), `TestRailSectionsOrder`, `TestEmptySectionHeadingOnly`, `TestRailDownWalksHeadings` (ending on the `sessions` heading: its items are not positions), `TestHeadingLeftCollapsesRightExpands`, `TestHeadingEnterToggles`, `TestCollapsedHeadingSummary`, `TestCollapsedSectionRowsGoElsewhere`, `TestCollapseShownSectionMovesToHeading`, `TestHeadingSelectedSingleHighlight`, `TestHeadingSurvivesRefresh`, `TestHomeSelectsChannelsHeading`, `TestTabReturnsToHeading`, `TestCollapseNotPersisted`, plus `TestFullNamesInRail` (a channel named `memory-plans` of ordinary kind sits under `channels` with its full name: no prefix is read or stripped). `header_icon_test.go`, `sendericon_test.go`, `embed_status_test.go`, `compose_test.go`: v1's cases with `IsTaskChannel` replaced by kinds and the DM cases dropped.

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/tui/`
Expected: build failure (`undefined: New`).

- [ ] **Step 3: Implement** `model.go`, `view.go`, `compose.go` and the stubs as in Interfaces. The stubs compile and the tests of this task pass against them; nothing in this task's tests depends on a stub's real behavior.

- [ ] **Step 4: Run the tests, the import rule and the gate**

Run: `go test -race ./internal/tui/`, `GOOS=windows GOARCH=amd64 go vet ./internal/tui/`, the import rule of Task 2 Step 4, `rg -n 'IsTaskChannel|DMPrefix|DMChannel|shortName|(HasPrefix|TrimPrefix|CutPrefix)\((c|ch|name|x\.Channel|c\.Name|sel|label)\b' --glob '!*_test.go' internal/tui` (the directory form, Global rule; expected: the one `tagPanePrefix` match in `tags.go` and nothing else; `format.go`'s `strings.HasPrefix(seq, "\x1b")` is escape parsing and is outside this pattern on purpose), then `make verify` (600000 ms).
Expected: pass; `exit=1`; `verify: OK`.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/model.go internal/tui/view.go internal/tui/compose.go internal/tui/thread.go internal/tui/memories.go internal/tui/tags.go internal/tui/tasks.go internal/tui/search.go internal/tui/health.go internal/tui/help.go internal/tui/model_test.go internal/tui/view_test.go internal/tui/compose_test.go internal/tui/rail_test.go internal/tui/header_icon_test.go internal/tui/sendericon_test.go internal/tui/embed_status_test.go
git commit -m "feat(tui): the dashboard model and view over the Bus: rails, stream, compose, status bar, channel prompts

Ports v1 internal/tui/model.go, view.go and compose.go. Kinds replace
name prefixes (ADR 0008, Decision 90), display names are the server's
(ADR 0003), the sessions rail is display-only and there is no inbox
view (ADR 0006), Status is called only by an administrator (Decision 73)."
```

---

### Task 4: Threads, message bodies, tag panes, memory versions

**Depends on:** 3.

**Files:**
- Modify (whole-file replacement of Task 3's stubs): `internal/tui/thread.go`, `internal/tui/memories.go`, `internal/tui/tags.go`
- Test: `internal/tui/thread_test.go`, `internal/tui/body_test.go`, `internal/tui/memories_test.go`, `internal/tui/tags_test.go`

**Port from v1:** `internal/tui/thread.go` (`paneMsgs` without the inbox merge, `row`, `rows`, `nextParent`, `rootSeq`, `peekReply`, `cursorRow`, `rowAvail`, `openCursor`, `closeCursor`, `collapseCursor`, `setExpanded`), `memories.go` (`memView`, `version`, `isVersionKey`, `stepVersion` over `Memories.Revisions`, `applyRevisions`), `tags.go` (`tagPanePrefix`, `isTagPane`, `tagPaneSet`, `tagPaneSources` over ordinary and memory kinds, `tagPaneMsgs`, `splitTags`, `tagPrompt` over `SubscribeTags{Patterns}`, `unfollowTagPane` over `UnsubscribeTags`), and the four test files.

**Interfaces:** consumes Task 3's `Model` fields and the stub signatures; produces the same names with v1's behavior. `Revisions(ctx, *x.MemoryID)` replaces v1 `MemoryRevisions(as, id)`.

- [ ] **Step 1: Write the failing tests** (v1's cases over the Task 3 fixture): `thread_test.go` `TestRepliesCollapseUnderRoot`, `TestRightOpensBodyThenReplies`, `TestLeftHidesRepliesThenBody`, `TestPeekShowsNewestReplyWhileCollapsed`, `TestReplyToUnloadedParentIsRoot`, and `TestReplyIndentAppliesToWrappedLines` (moved from v1 `view_test.go`, Task 3); v1's `agentReply` calls become Task 2's `samReply`, `stream()` is Task 3's; `body_test.go` v1's subject and body cases; `memories_test.go` `TestDotStepsToOlderRevision` (sam edits a memory twice through `Memories.Edit`; `.` shows revision 1 with the `v1 of 3` label; `,` back), `TestVersionResetsOnOtherKey`; `tags_test.go` `TestTFollowsTagSet` (`t`, `env:prod,failed` → `SubscribeTags` called with the two patterns, a `tags:` rail row with chips), `TestTagPaneShowsMatchingLoadedMessages`, `TestDUnfollowsTagSet` (`UnsubscribeTags`; the selection moves as v1 ADR 0011 item 7), `TestTagPaneIsReadOnly`.

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/tui/ -run 'Thread|Replies|ReplyIndent|Right|Left|Peek|Body|Subject|Dot|Version|Tag'`
Expected: FAIL (the stubs render flat rows and ignore the keys).

- [ ] **Step 3: Implement** by replacing the three stub files with the ports.

- [ ] **Step 4: Run the tests and the gate**

Run: `go test -race ./internal/tui/`, then `make verify` (600000 ms).
Expected: pass; `verify: OK`.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/thread.go internal/tui/memories.go internal/tui/tags.go internal/tui/thread_test.go internal/tui/body_test.go internal/tui/memories_test.go internal/tui/tags_test.go
git commit -m "feat(tui): threads, bodies, tag panes and memory versions ported from v1 internal/tui/thread.go, memories.go and tags.go

No inbox merge in paneMsgs (ADR 0006); tag panes draw from ordinary and
memory kinds (Decision 90); revisions through Memories.Revisions."
```

---

### Task 5: Task lists and task editing

**Depends on:** 3.

**Files:**
- Modify (whole-file replacement of Task 3's stub): `internal/tui/tasks.go`
- Test: `internal/tui/tasks_test.go`, `internal/tui/taskedit_test.go`, `internal/tui/tasktree_test.go`, `internal/tui/tasks_reload_test.go`

**Port from v1:** `internal/tui/tasks.go` (`loadTasks` over `Tasks.List{Channel}`, `loadTask` over `GetTask`, `taskDraft`, the toasts, `ownedToast`, `taskEditable`, `shownTask`, `cycleTaskState`, `saveDraft` over `Update{ID, Status}`, `dropDraft`, `dropStaleDraft`, `takeTask` over `Claim{ID}`, `unassignTask` over `Release{ID}`, `assignPicker`, `openAssign` over `m.identities`, `updateAssign` over `Update{ID, Owner}`, `viewAssign`, `taskRow`, `taskRows`, `taskIndex`, `revealTask`, `cursorTaskRow`, `cursorTask`, `openTask`, `closeTask`, `expandTask`, `collapseTask`, `renderTasks`, `renderTaskBlock`) and the four test files; v1 ADR 0011 decisions 1 to 7 and the task-editing design.

**Interfaces:** consumes Task 3's `isTasks`, `draft`, `assign`, `identities`; produces the stub names with v1's behavior. `taskEditable(t) = t.Owner == "" || t.Owner == m.c.identity`. The assign picker lists `m.c.identity` first, then every other identity of `m.identities` (not display names: owners are identities, Decision 81).

- [ ] **Step 1: Write the failing tests** (v1's cases; the task channel is `todo` of `bus.KindTasks` created by sam; tasks through sam's `Tasks.Create`): `tasks_test.go` `TestTaskChannelRendersTree`, `TestRightOpensDetailsThenChildren`, `TestLeftHidesChildrenThenDetails`, `TestChildrenStartCollapsed`, `TestNerdfontTaskRowWidthParity` (the rendered half of v1 `icons_test.go`'s `TestLoadIconsNerdfontWidthParity`, Task 1: a `todo` task list with one task sam owns and one sam claimed; the task stream and the rail have the same per-line widths under the emoji and the nerdfont sets, through Task 1's `widths`; v1's idle-marker lines, `TaskIdleHours` and `setChannelLastActivity`, are not ported); `taskedit_test.go` `TestSpaceCyclesDraftEnterSaves` (the revision in the store moves; `Update` was called with `Status`), `TestEscDiscardsDraft`, `TestMovingCursorDiscardsDraftWithToast`, `TestTTakesUnassignedTask` (`Claim`; owner `eric`), `TestUUnassignsOwnNotStartedTask` (`Release`), `TestOwnedByAgentIsRefused` (sam claims; `space` → `owned by sam; only unassigned tasks or yours can be changed here`), `TestAAssignsToIdentity` (`a`, pick `sam` → `Update{Owner: sam}`; the picker lists identities, `eric (you)` first); `tasktree_test.go` v1's ordering cases; `tasks_reload_test.go` `TestNewerRevisionDropsDraft` (a task message with `Revision > draft.rev` on the batch discards the draft), `TestSearchJumpIntoUnloadedTaskList` (the pending task cursor).

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/tui/ -run 'Task|Space|Esc|Moving|Owned|Assign|Children|Revision'`
Expected: FAIL (the stub renders nothing).

- [ ] **Step 3: Implement** by replacing the stub.

- [ ] **Step 4: Run the tests and the gate**

Run: `go test -race ./internal/tui/`, then `make verify` (600000 ms).
Expected: pass; `verify: OK`.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/tasks.go internal/tui/tasks_test.go internal/tui/taskedit_test.go internal/tui/tasktree_test.go internal/tui/tasks_reload_test.go
git commit -m "feat(tui): task trees and task editing ported from v1 internal/tui/tasks.go

v1 ADR 0011: space cycles a draft, enter saves, esc cancels, t takes
through Claim, u releases through Release, a assigns an identity
(Decision 81); only unassigned tasks or the identity's own are editable."
```

---

### Task 6: Search

**Depends on:** 3.

**Files:**
- Modify (whole-file replacement of Task 3's stub): `internal/tui/search.go`
- Test: `internal/tui/search_test.go`

**Port from v1:** `internal/tui/search.go` (`searchCount`, `searchState`, `openSearch`, `updateSearch` with `tab` cycling `text`/`semantic`/`both`, `runSearch` over `Messaging.Search{Query, Mode, Count: searchCount}`, `jumpTo` without the inbox branch, `viewSearch` with the amber `text only` badge on `SemanticUnavailable`) and `search_test.go`.

**Interfaces:** consumes Task 3's `recordQuery` (health stub keeps it), `placeCursor`, `loadHistory`, `selectChannel`; produces the stub names with v1's behavior. A hit is a `bus.SearchHit` (embedded `Message`, `Score`).

- [ ] **Step 1: Write the failing tests** (v1's cases): `TestSlashOpensSearchAndEnterRuns` (sam sent `deploy failed`; `/`, `deploy`, enter → one hit; the stale-response guard: a `searchMsg` for an older query is ignored), `TestTabCyclesModes`, `TestSemanticUnavailableShowsTextOnlyBadge` (the core has no embedder, so `Mode: "both"` answers `SemanticUnavailable true`; the overlay shows `text only`, never an error), `TestEnterOnHitJumpsAndLoadsAround` (the cursor lands on the hit's seq; `History{Before: seq}` was requested), `TestSearchErrorIsShownInOverlay`.

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/tui/ -run 'Search|Slash|Semantic|Hit|Modes'`
Expected: FAIL.

- [ ] **Step 3: Implement** by replacing the stub.

- [ ] **Step 4: Run the tests and the gate**

Run: `go test -race ./internal/tui/`, then `make verify` (600000 ms).
Expected: pass; `verify: OK`.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/search.go internal/tui/search_test.go
git commit -m "feat(tui): search overlay ported from v1 internal/tui/search.go over Messaging.Search

Modes text, semantic and both; semantic_unavailable renders as the text-only
badge; a hit jumps to its channel and loads the page before it."
```

---

### Task 7: Health, help, config editor

**Depends on:** 3. External: Milestone 7 Task 6 for the storage line (see Names assumed; omitted when absent, stated in the report).

**Files:**
- Modify (whole-file replacement of Task 3's stubs): `internal/tui/health.go`, `internal/tui/help.go`
- Create: `internal/tui/editor_unix.go` (`//go:build !windows`), `internal/tui/editor_windows.go`, `internal/tui/editor_cmdline.go`
- Test: `internal/tui/health_test.go`, `internal/tui/help_test.go`, `internal/tui/editor_unix_test.go`, `internal/tui/editor_windows_test.go` (`//go:build windows`), `internal/tui/editor_cmdline_test.go`

**Port from v1:** `internal/tui/health.go` (`healthState`, `recordQuery`, `openHealth` returning `tea.Batch(m.statusCmd(), m.usageCmd())`, `usageCmd` (nil unless `m.admin`; when `bus.Admin.Usage` is on `main`, `Usage(ctx)` → `usageMsg{usage: &u}`; see Names assumed), `updateHealth` with `o`, `onConfigEdited` re-validating through `Host.ReloadConfig` (set by `cli.RunTUI`, Task 10; nil in tests → the toast reads `config saved; restart agentbus2 tui to apply`; an error → `config: <err>`; the model never imports `internal/profile`), `healthLines` per choice 8, `viewHealth`, `editorCommand`, `backgroundEditor`, `editorProgram`, `editorSetting`, `editorErrText`), `help.go` at `906dec8` (`openHelp`, `updateHelp`, `helpLines` with its rail-section rows (`enter (heading)`, the `(rail)` row, `home` as "rail: select the channels heading") but without direct-message rows, "the sessions pane" phrases and the `(rail)` row's `general/, memory/ and tasks/ are left off` clause (names are shown in full), and with `c` reading `new channel: <name> [memory|tasks]`, `viewHelp`), `editor_unix.go`, `editor_windows.go`, `editor_cmdline.go` and their tests.

**Interfaces:** consumes Task 3's `Host.ReloadConfig`, `Host.Warnings`, `Host.ConfigPath`, `m.admin`, `m.status`, `m.statusOK`, `m.usage`, `m.identities` and the `usageMsg` type of Task 2; produces the stub names with the behavior below. Health lines, in order: `version agentbus2 v<version>`; `mode <Describe>`; `identity <identity> · <display name> · session <id>`; `sessions N live · channels N (N memory, N tasks)`; one line per live session `  <display> <identity dim>`; `receive <long-poll connected|err> · last batch <time> · N gaps`; when `admin`: `notice` lines (`status.Notices`, `none` when empty), `embeddings <ok|failing> · backlog N · rejected N` with the wrapped last error (v1), `storage <used> of <budget> <bar> N%` when `usage != nil`; otherwise `admin no (status and storage are administrator views)`; `warnings` (`Host.Warnings`); `theme · <name>` and the sources; `icons · <set>`; `config · <path>` and the `tui` settings as indented JSON (`Settings` marshaled with lowercase keys through a local struct).

- [ ] **Step 1: Write the failing tests** (v1's cases): `health_test.go` `TestHOpensHealthWithLines` (the lines above as a non-admin: `admin no`, no `notice` line), `TestHealthAdminLines` (admin fixture: `notice none`, `embeddings`), `TestHealthScrollStopsAtEnd`, `TestOOpensEditorAndToastsOnReturn` (`Host.ConfigPath` a temp file, `$VISUAL` unset, `$EDITOR=true` → `configEditedMsg{nil}` → the toast; with `Host.ReloadConfig` returning an error → `config: <err>`), `TestBackgroundEditorForGUI` (v1); `help_test.go` `TestHelpListsEveryKey` (every key of `updateNormal`'s switch appears, the heading rows included; the help text contains none of `direct`, `inbox`, `sessions pane` and no match of `\bdm\b` (a plain substring `dm` would match `admin`): the test may spell these words, since the exit script's word check covers non-test files only, Task 9 Step 4, but `help.go` itself must not); the editor tests as v1.

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/tui/ -run 'Health|Help|Editor|Opens'`
Expected: FAIL.

- [ ] **Step 3: Implement** by replacing the stubs and adding the editor files.

- [ ] **Step 4: Run the tests, the Windows vet and the gate**

Run: `go test -race ./internal/tui/`, `GOOS=windows GOARCH=amd64 go vet ./internal/tui/`, then `make verify` (600000 ms).
Expected: pass; `verify: OK`.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/health.go internal/tui/help.go internal/tui/editor_unix.go internal/tui/editor_windows.go internal/tui/editor_cmdline.go internal/tui/health_test.go internal/tui/help_test.go internal/tui/editor_unix_test.go internal/tui/editor_windows_test.go internal/tui/editor_cmdline_test.go
git commit -m "feat(tui): health and help overlays and the config editor ported from v1 internal/tui/health.go, help.go and editor_*.go

Health shows what a client can know; the administrator block appears only
for an admin-flagged identity (Decision 73); the keymap has no
direct-message rows (ADR 0006)."
```

---

### Task 8: TUI settings in `profile.Config`

**Depends on:** none in this plan. External gate: Milestone 6 Task 1 on `main` (`internal/profile`).

**Files:**
- Modify: `internal/profile/config.go` (append: `TUISettings`, `TUIColorTheme`, the `TUI` member, its validation in `Load`, `SetTUIRailWidth`), `internal/profile/config_test.go` (append cases)

**Port from v1:** `internal/config/config.go` (`Theme` struct and its json names, `tui_rail_width`'s `0`-`1000` bound in `validate`, `SaveTUIRailWidth` re-expressed over the strict loader).

**Interfaces (produces):**

```go
package profile
type TUIColorTheme struct { Name string `json:"name"`; Background string `json:"background"`; Text string `json:"text"`; Dim string `json:"dim"`; Timestamp string `json:"timestamp"`; Tag string `json:"tag"`; Agent string `json:"agent"`; User string `json:"user"`; Memory string `json:"memory"`; Tasks string `json:"tasks"`; Health string `json:"health"`; Warn string `json:"warn"`; Error string `json:"error"`; Selection string `json:"selection"`; SelectionInactive string `json:"selection_inactive"`; SelectionText string `json:"selection_text"`; Unsaved string `json:"unsaved"` }
type TUISettings struct { Theme string `json:"theme,omitempty"`; Themes []TUIColorTheme `json:"themes,omitempty"`; Icons string `json:"icons,omitempty"`; IconMap map[string]string `json:"icon_map,omitempty"`; RailWidth int `json:"rail_width,omitempty"` }
// Config gains: TUI *TUISettings `json:"tui,omitempty"`  // pending Eric: Milestone 8 item 1
func (s TUISettings) Validate() error // rail_width 0..1000: "tui.rail_width must be between 0 and 1000, got %d"; icons "" or one of emoji, nerdfont, custom: "tui.icons must be emoji, nerdfont or custom, got %q" (Definition of Done B: an enum value outside the set is rejected at load, not warned about at TUI start); a theme entry with an empty name: "tui.themes[%d].name must not be empty"; a repeated name: "tui.themes[%d].name %q repeats tui.themes[%d]"; theme not "", "default" or a themes name: "tui.theme %q is not default or the name of an entry of tui.themes"; nothing else (color values and icon_map names are the TUI's to warn about)
func SetTUIRailWidth(path string, width int) error // Load(path), set TUI.RailWidth (creating TUI), Save: the file re-read so profiles written meanwhile are kept
```

- [ ] **Step 1: Write the failing tests** (appended to `config_test.go`): `TestLoadTUISettings` (a file with `tui: {theme: "dark", themes: [{name: "dark", agent: "cyan"}], icons: "nerdfont", icon_map: {"idle": "z"}, rail_width: 24}` round-trips; an unknown key `tui.colour` is rejected naming the key; `rail_width: 1001` → the bound message, `-1` likewise, `1000` and `0` accepted; `icons: "ascii"` → `tui.icons must be emoji, nerdfont or custom, got "ascii"`; a theme without a name → the message; two themes named `dark` → the repeat message; `theme: "light"` with no `light` entry → the dangling-reference message; `theme: "default"` with no themes → accepted; a file without `tui` loads with `TUI == nil`), `TestSetTUIRailWidthTwice` (Definition of Done D, a second run: `SetTUIRailWidth(path, 30)` twice leaves one `tui` object with 30 and the file byte-identical between the two runs; on a missing `config.json` it does what `profile.Load` does with one (Milestone 6 Task 1, read from `main`: when `Load` answers an empty `Config` the file is created through `Save` holding only `tui`, mode 0600; when `Load` errors, that error is returned and nothing is written), and the test pins whichever it is), `TestSetTUIRailWidthKeepsProfiles` (a config with profile `lab`; `SetTUIRailWidth(path, 30)`; reload shows `lab` intact and `TUI.RailWidth == 30`; the file stays 0600).

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/profile/ -run 'TUI'`
Expected: build failure (`undefined: SetTUIRailWidth`).

- [ ] **Step 3: Implement** as in Interfaces (`Load` calls `TUI.Validate()` when `TUI != nil`).

- [ ] **Step 4: Run the tests and the gate**

Run: `go test -race ./internal/profile/`, then `make verify` (600000 ms).
Expected: pass; `verify: OK`.

- [ ] **Step 5: Commit**

```bash
git add internal/profile/config.go internal/profile/config_test.go
git commit -m "feat(profile): the tui settings object in config.json (theme, themes, icons, icon_map, rail_width)

Ports the tui_rail_width bound and the Theme shape of v1
internal/config/config.go; rail width is saved by re-reading the strict
file (Milestone 8 choice 7; pending Eric: Milestone 8 item 1)."
```

---

### Task 9: Entry, the both-modes exit test, the exit script and its negative halves

**Depends on:** 4, 5, 6, 7 (so the word check and the selftest run over every production file of the package). No external gate: everything this task imports (`internal/bus`, `internal/version`; in tests `internal/bus/local`, `internal/bus/remote`, `internal/server`, `internal/gen`, `internal/auth/token`, `internal/auth/keys`, `internal/core/...`, `internal/store/sqlite`) is on `main` now.

**Files:**
- Create: `internal/tui/run.go` (`Options`, `Run`, `run`), `internal/tui/run_test.go` (`TestBothModes`, `TestRunQuitsOnCanceledContext`, `TestRunHeadlessSizeRenders`), `scripts/m8-exit-checks.sh`
- Modify: `scripts/check-gates-selftest.sh` (two new functions `block_m8_exit_script_fires` and `block_tui_hint_mutation` and a local helper `m8_deps_probe`, defined after the last `block_*` function on `main` at landing time, and their two calls appended to the call list after its last call, before the closing `if [ "$bad" -ne 0 ]`; the shared `deps_probe` is not edited, it is wired to `m4-exit-checks.sh`), `.github/workflows/ci.yml` (`gates-selftest` gains `- run: bash scripts/m8-exit-checks.sh --deps-only` after the `m4` step)

**Port from v1:** `internal/tui/tui.go` (`Run`, at `906dec8`) reduced to a program over a `bus.Bus` the caller opened: no `config.Load`, no `--as`, no `tui_name` (choice 2).

**Interfaces (produces):**

```go
package tui // run.go imports internal/bus and internal/version only (Global Constraints); it never opens a connection or reads a profile: internal/cli/tui.go (Task 10) does that and hands the Bus in
type Options struct { Bus bus.Bus; Identity string; Settings Settings; Host Host; Input io.Reader /* nil: os.Stdin */; Output io.Writer /* nil: os.Stdout */; Stderr io.Writer /* nil: os.Stderr */; Width, Height int /* tests only; 0: left to the terminal */ }
func Run(ctx context.Context, o Options) error // o.Bus nil or o.Identity "" → an error naming the field, before anything is opened; LoadTheme and LoadIcons (warnings to Stderr before the alternate screen); newClient; p := tea.NewProgram(New(c, theme, host), tea.WithAltScreen(), tea.WithContext(ctx), and tea.WithInput(o.Input) / tea.WithOutput(o.Output) only when the field is non-nil (tea.WithInput(nil) would disable input; absent, Bubble Tea uses stdin and stdout)); when o.Width > 0 && o.Height > 0: `go p.Send(tea.WindowSizeMsg{Width: o.Width, Height: o.Height})` before p.Run (Send blocks until the event loop reads it, so the goroutine delivers it once the program is running; Bubble Tea asks only a tty for its size, so a buffer-backed Output never produces a WindowSizeMsg and View would stay "" at width 0); go c.receiveLoop(p.Send); p.Run; c.close; tea.ErrProgramKilled is mapped to nil when ctx.Err() != nil (a canceled context is a clean quit); a model that quit on sessionLostMsg returns that error
func run(ctx context.Context, o Options) (Model, error) // Run's body returning the final model, for tests
```

- [ ] **Step 1: Write the failing tests**

`run_test.go` (package `tui`, Docker-free). `TestBothModes`: a table of two subtests named `local` and `server`, each over its **own** `newCore(t)` (a shared core would let one leg's acks advance `pat`'s acked mark, Decision 75, and leave the other nothing to wake on) and each driving the TUI over a `remote.New` `Bus`, because Milestone 6's `client.Open` builds `remote.New` in both modes (spec 2.1, 2.3; Decision 35):

- `local` (Decision 35's local mode, as `client.Open` wires it): `h := &countingHandler{Handler: server.New(core, server.Options{Auth: server.InProcess{}})}` (a test-local copy of the conformance suite's `countingHandler`, `transport_test.go`, counting requests whose path ends in `/watch` under an `atomic.Int64`), then `gc, err := gen.NewClientWithResponses("http://inproc", gen.WithHTTPClient(&http.Client{Transport: server.InProcessTransport(h, "pat")}))`, `if err != nil { t.Fatal(err) }`, `remote.New(gc)`. (`client.Open` uses `notify.NewPolling`; the test keeps `newCore`'s `notify.NewMemory()`, which changes how fast a wake arrives, not the path.)
- `server` (server mode's wire: real TCP, HTTP long poll, a bearer on every request): `srv := httptest.NewUnstartedServer(nil)`; `url := "http://" + srv.Listener.Addr().String()`; `key, err := keys.Generate(keys.EdDSA)`; `iss, err := token.NewIssuer(url, key, time.Hour, time.Now)`; `srv.Config.Handler = &countingHandler{Handler: server.New(core, server.Options{Auth: server.Bearer{Verifier: token.Verifier{Own: iss}}, Issuer: iss})}`; `srv.Start()`; `t.Cleanup(srv.Close)`; `tok, _, err := iss.Mint("pat")`; `gc, err := gen.NewClientWithResponses(url, gen.WithRequestEditorFn(remote.WithBearer(tok)))`; every `err` checked with `t.Fatal`; `remote.New(gc)`. (Registration and the token exchange are Milestone 6's and are covered by Task 10's `TestRunTUIServerProfile`; this leg proves the program over the server-mode wire.)

In each leg: `ensureIdentity(t, core, "pat")` (the raw path: no `general` subscription yet); `sam := identityBus(t, core, "sam")`; sam subscribes `general` (`From: "oldest"`) and, in a goroutine, arms `Watch` then `Receive` on a session of its own; before `run` starts, the probe bus `probe := identityBus(t, core, "pat")` subscribes `pat` to `general` with `From: "oldest"`, so the TUI's own `subscribe(general, "now")` is the `ON CONFLICT DO NOTHING` no-op (Global Constraints) and the acked mark stays at the floor (without this, `newClient`'s `now` subscribe sets the mark to `tx.MaxSeq()`, `channels/subscriptions.go`, and a send that lands between `Open` and that subscribe is behind the mark: the probe would answer `Pending` 0 with no loop involved and the message would reach the model through `loadHistory`); `ctx, cancel := context.WithCancel(context.Background())` with `t.Cleanup(cancel)` registered **before** `run` starts, so a `t.Fatal` in a later step (the Step 6b mutation runs exactly that path) unblocks the `run` goroutine instead of hanging the package; `Options{Bus, Identity: "pat", Input: an io.Pipe (writer closed in t.Cleanup), Output: a bytes.Buffer under a mutex, Width: 80, Height: 24}`; `run` in a goroutine reporting `(Model, error)` on a channel. Then, in this order, each wait a `waitFor` with the stated bound: (1) within 2 s, sam's `Identities()` lists `pat/tui`; (2) sam sends `hello from sam` on `general`; (3) within 5 s, `pendingFor(t, probe, "general")` is 0 (the helper fails on an absent entry, so an unsubscribed probe cannot pass): a fresh session takes its cursors from the identity's `AckedSeq` (`sessions/open.go`), and with the mark at the floor only `Receive` plus its ack over this leg's `Bus` can clear it, so the loop ran before any key was typed and `loadHistory` could not have done it; (4) write `i`, `hello from the tui\r` (the rail's selection is `general`, the core's only channel: v1 `New` starts with `sel -1` and the first status round's `setChannels` selects channel 0, `model.go:1359` at `906dec8`; before writing `i` the test `waitFor`s (2 s) the Output buffer to contain `loaded` after `general` on one line (`renderHeader`'s `<channel> · N unread · N loaded`, drawn only for the selected channel; matched on the ANSI-stripped buffer), so the key never races `Init`'s first status round); within 2 s sam's loop has a message on `general` with `Content == "hello from the tui"` and `Display == "pat/tui"` (the `Send` carried the session id: Review Focus 6); (5) write `\x03` (ctrl+c); `run` returns `(m, nil)` within 2 s; `m.msgs["general"]` contains `hello from sam` and `!m.lastBatchAt.IsZero()` (set only by `onBatch`: the message came through the batch); sam's `Identities()` no longer lists `pat/tui`; the leg's `countingHandler` counted at least one `/watch` request (the TUI's long poll crossed this leg's handler, not some other path). Nothing is asserted about pending counts after the quit (the own echo may be in flight). `TestRunQuitsOnCanceledContext` (cancel the context → `Run` returns within 1 s with no error and the session closed: sam's `Identities()` no longer lists it). `TestRunRejectsMissingOptions` (`Options{}` → an error naming `Bus`; `Options{Bus: b}` → an error naming `Identity`; no session opened: `Identities()` through sam is unchanged). `TestRunHeadlessSizeRenders` (`Width: 80, Height: 24` → the Output buffer contains `agentbus2 v` within 1 s; the negative half with `Width: 0`: `waitFor` the session to be listed, write `\x03`, and after `run` returns the Output never contained the status bar: the size seam is what makes a headless run render, no fixed sleep). Tasks 2 to 7's tests use `local.New` freely; this file is the only one whose legs stand for the product's modes.

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/tui/ -run 'TestBothModes|TestRunQuits|TestRunRejects|TestRunHeadless'`
Expected: build failure (`undefined: Run`).

- [ ] **Step 3: Implement** `run.go`. State (Definition of Done D): a second `Run` on the same identity is `<identity>/tui-2` with its own cursors; two `Run`s overlapping on one identity share the identity's subscriptions and acked mark (Decisions 37, 75; pending item 2) and each acks only its own batches; a killed TUI leaves a lapsed row until retention (pending item 3); the loop's drain ends with an empty `Receive` that carries the last batch's token, so no displayed batch stays pending across a wake, and `prev` is `""` after it (choice 3); a TUI killed between a batch and that empty call leaves the batch pending, and the next start of a session on the identity sees it redelivered (`Redelivered true`), which `onBatch` merges by seq (`addMessages` dedupes).

- [ ] **Step 4: The exit script**

`scripts/m8-exit-checks.sh` (`#!/usr/bin/env bash`, `set -euo pipefail`, absolute paths from `ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"`, the `m4` script's shape): usage `m8-exit-checks.sh <tui.log>` or `--deps-only`; any other argument, or none, exits 2 with the usage line (Definition of Done B). The log form refuses a missing or empty log (exit 2 naming the path), prints `modes=<n>` counting `^[[:space:]]+--- PASS: TestBothModes/(local|server) \([0-9.]+s\)$`, `noinbox=<n>` counting `^--- PASS: TestNoInboxView \([0-9.]+s\)$`, `cli=<n>` counting `^--- PASS: TestRunTUI(LocalProfile|ServerProfile) \([0-9.]+s\)$` and `fail=<n>` for `^[[:space:]]*--- FAIL` (the `m3` shapes: a `go test -v` result line ends with the duration in parentheses and a subtest line is indented, so a regex anchored right after the name counts nothing), exiting 1 unless `fail=0`, `modes=2` and `noinbox=1` (Task 10 makes `cli=2` required too, once the `TestRunTUI*` tests exist; until then `cli` is printed and not required, and the script says so in a comment Task 10 removes); then the word check `rg -n -e '\bdm/' -e 'DMPrefix' -e 'DMChannel' -e '(?i)inbox' -e 'IsTaskChannel' -e 'tui_name' --glob '*.go' --glob '!*_test.go' "$ROOT/internal/tui"` must match nothing (`words_ok=1`, else the matches and exit 1; the **directory** argument is what makes the test-file glob apply, Global rule; production files only, comments included, so no non-test Go file under `internal/tui` may use these words even in prose: an ADR citation reads `ADR 0006`, not the word; test files may spell them when asserting their absence, Task 7's `TestHelpListsEveryKey`; `docs/tui.md` may name them when it says what changed from v1; Task 10 adds the file `internal/cli/tui.go` as a second path, a single non-test file); then the import check (`--deps-only` runs it alone): the **direct** imports of `./internal/tui` (`go list -f '{{join .Imports "\n"}}'`), as an allowlist: every import beginning `github.com/ericfitz/agentbus2/` other than exactly `github.com/ericfitz/agentbus2/internal/bus` and `github.com/ericfitz/agentbus2/internal/version`, and every import beginning `github.com/getkin/kin-openapi`, prints `deps_violation=github.com/ericfitz/agentbus2/internal/tui imports <import>` and the script exits 1; none → `deps_ok=1` (spec 2.3's `tui --> busif`: nothing in `internal/tui` knows which transport is behind it, and no exception for `run.go`); a `go list` failure exits 2 with its output, so a probe naming a missing package can never pass as a violation; `./internal/cli` is checked by `m6`'s script, not here.

- [ ] **Step 5: The milestone exit, positive (this task's half)**

Run (600000 ms or in the background): `env -u AGENTBUS2_TEST_POSTGRES_DSN make verify 2>&1 | tee /tmp/agentbus2-m8-verify.log | tail -3`; `env -u AGENTBUS2_TEST_POSTGRES_DSN make test-race 2>&1 | tail -4`; `go test -count=1 -v -run '^(TestBothModes|TestNoInboxView)$' ./internal/tui/ > /tmp/agentbus2-m8-exit.log 2>&1; bash scripts/m8-exit-checks.sh /tmp/agentbus2-m8-exit.log`; `bash scripts/m4-exit-checks.sh --deps-only` (`m6-exit-checks.sh` is Milestone 6's and runs in Task 10, once it is on `main`).
Expected, quoted verbatim in the report: `verify: OK`; `test-race` `ok` everywhere; `fail=0`, `modes=2`, `noinbox=1`, `cli=0`, `words_ok=1`, `deps_ok=1`, exit 0; `deps_ok=1` from `m4`.

- [ ] **Step 6a: Prove the exit script can fail** (function `block_m8_exit_script_fires`, label `m8-exit-script-fires`, Docker-free, using the selftest's `begin`/`finish "$label"`/`problem`/`revert` helpers, its worktree `$wt` of `HEAD` and its `$tmp`; it therefore tests the **committed** tree and is run after Step 7's commit): `--deps-only` in `$wt` prints `deps_ok=1` and exits 0 first; then probes through `m8_deps_probe` (as `deps_probe`, but running `$wt/scripts/m8-exit-checks.sh --deps-only`), each probe file removed afterwards: (1) `internal/tui/probe_m8.go` with `import _ "github.com/ericfitz/agentbus2/internal/bus/local"` → exit 1 and `deps_violation=github.com/ericfitz/agentbus2/internal/tui imports github.com/ericfitz/agentbus2/internal/bus/local`; (2) the same with `internal/gen` → its `deps_violation` line; (3) the same with `internal/filelock` (on `main`, named in no forbidden list anywhere: proves the check is an allowlist, not a denylist); (4) reserved: the `internal/client` probe is added by Task 10, once that package is on `main`; (5) the log form on a missing path, on an empty file and with no argument → exit 2; (6) a **real** log: `go test -count=1 -v -run '^(TestBothModes|TestNoInboxView)$' ./internal/tui/ > "$tmp/m8.log" 2>&1` in `$wt` → the script prints `modes=2`, `noinbox=1`, `fail=0`, exit 0; then derived logs, never literals: `sed '/TestBothModes\/server/d' "$tmp/m8.log"` → `modes=1`, exit 1; `sed 's/^\(    --- \)PASS\(: TestBothModes\/local\)/\1FAIL\2/' "$tmp/m8.log"` → `fail=1`, exit 1; `sed '/--- PASS: TestNoInboxView/d' "$tmp/m8.log"` → `noinbox=0`, exit 1 (a hand-written log would only prove the script matches the shape its author imagined); (7) the word `Inbox` (capitalized) appended in a comment to `internal/tui/doc.go` → `words_ok` absent, exit 1; (8) the word `inbox` appended in a comment to `internal/tui/run_test.go` → `words_ok=1` still (test files are out of scope; with the shell-glob form this probe fails, which is what it is for); `revert` after (7) and (8). Each expectation matched with `grep -qF`; a miss calls `problem "$label"` with the tail of the output. Prints `selftest: ok m8-exit-script-fires`.

- [ ] **Step 6b: The milestone exit, negative half** (function `block_tui_hint_mutation`, label `tui-hint-mutation`, Docker-free, in `$wt` after Step 7's commit; three mutations, each followed by `revert`): (1) **the receive path**: in `internal/tui/run.go` delete the line `go c.receiveLoop(p.Send)` through a temp file and `cp` (`problem` unless `grep -c 'receiveLoop(p.Send)' internal/tui/run.go` is 0 after the edit); `go test -count=1 -v -run '^TestBothModes$' ./internal/tui/` must print `--- FAIL: TestBothModes/local` and `--- FAIL: TestBothModes/server` (step (3)'s 5 s bound expires: the probe's `Pending` never clears; both legs prove the loop ran over their `Bus`, and both fail together when it does not; this is the negative half of "runs over the Bus interfaces in both modes", spec 10) and must finish (the `t.Cleanup(cancel)` of Step 1 ends the `run` goroutine; the block runs the command under `perl -e 'alarm 300; exec @ARGV'` and calls `problem` on a timeout); (2) **the session on the wire**: replace `const sessionHint = "tui"` in `internal/tui/client.go` with `const sessionHint = ""` (`problem` unless `grep -c 'sessionHint = ""' internal/tui/client.go` is 1); the same test prints both `--- FAIL` lines (sam's `Identities()` never lists `pat/tui`, so step (1) of the test times out: both transports carry the same `Open{Hint}` the same way); (3) **own echo**: in `internal/tui/client.go` the `Receive` call's `IncludeOwn: true, // receive-own` becomes `IncludeOwn: false, // receive-own` by `sed` on that marker (the `Watch` call's line carries `// watch-own`; `problem` unless `grep -c 'IncludeOwn: true' internal/tui/client.go` fell by exactly 1 and `grep -c 'IncludeOwn: false, // receive-own'` is 1) → `go test -count=1 -run '^TestReceiveLoopIncludesOwnSends$' ./internal/tui/` prints `--- FAIL`; a `Watch` flip would fail the same test (no wake on an own send) and is not run. Prints `selftest: ok tui-hint-mutation`. Run after Step 7's commit: `make check-gates-selftest 2>&1 | rg -n '^selftest:'`. Expected: `selftest: ok m8-exit-script-fires`, `selftest: ok tui-hint-mutation`, every other block (Milestones 1 to 7's) `ok`. A failure here after the commit is fixed by a follow-up commit, never by amending.

- [ ] **Step 7: Commit**

```bash
git add internal/tui/run.go internal/tui/run_test.go scripts/m8-exit-checks.sh scripts/check-gates-selftest.sh .github/workflows/ci.yml
git commit -m "feat(tui): tui.Run over a caller-opened Bus, the same program over the remote Bus in local and in server mode, the exit script and its negative halves

Ports v1 internal/tui/tui.go (906dec8) without config.Load, --as or
tui_name (Milestone 8 choice 2). Decision 60: TestBothModes drives one
program over remote.New on the in-process transport (Decision 35) and
over a TCP listener with a server-minted bearer; the selftest proves
both legs fail without the receive loop."
```

- [ ] **Step 8: Report**

End with the Step 5 outputs and the two `selftest: ok` lines of Steps 6a and 6b quoted verbatim. No push.

---

### Task 10: `agentbus2 tui`: the command, its profile tests, docs

**Depends on:** 8, 9. External gate: Milestone 6 Tasks 5 and 11 on `main` (`client.Open`, `cli.open`, `testClientOptions`, `cli.Main`, `newTestEnv`).

**Files:**
- Create: `internal/cli/tui.go` (`RunTUI`, `testTUIOptions`), `internal/cli/tui_test.go`, `docs/tui.md`
- Modify: `internal/cli/cli.go` (the `tui` case and `usage`), `main_test.go` (`TestUsageListsEveryCommand`'s literal list gains `tui`), `scripts/m8-exit-checks.sh` (`cli=2` required; `internal/cli/tui.go` in the word check), `scripts/check-gates-selftest.sh` (the `m8-exit-script-fires` block's real log gains the cli tests and a `cli=1` negative), `CLAUDE.md`, `README.md`, `docs/adr/0006-no-direct-messages.md`, `docs/adr/0003-identity-sessions-and-delivery.md` and `docs/adr/0008-channel-access-control-lists.md` (Milestone 8 addenda), `HANDOFF.md` (untracked, never staged)

**Port from v1:** `main.go`'s `tui` case at `906dec8` (`-config` and `-as` dropped: choice 2), `docs/install.md` sections "Configuration" (the `tui_*` keys), "TUI theme", "TUI icons", "Using a Nerd Font" and the README's TUI paragraph and key list, rewritten for `docs/tui.md`.

**Interfaces (produces):**

```go
package cli // internal/cli/tui.go; follows Milestone 6's import rule for internal/cli
var testTUIOptions func(*tui.Options) // nil in production; RunTUI applies it last, so a test can pin Width and Height (a buffer-backed Output never gets a WindowSizeMsg) exactly as testClientOptions pins the Conn
func RunTUI(ctx context.Context, args []string, environ []string, stdin io.Reader, stdout, stderr io.Writer) int // client.Flags; o.Maintenance = true; conn, code := open(ctx, o, stderr) (Milestone 6's cli.open, so testClientOptions' pins apply: PollInterval 50 ms and WatchMergeSeconds 0 are what make the 2 s bounds below hold; a direct client.Open would poll at 1 s and merge for 2 s); Settings from profile.Load(Resolved.ConfigDir/config.json).TUI; Host{ConfigPath, Describe: "local · <SQLitePath>" | "server · <Server>", RailWidth, SaveRailWidth: profile.SetTUIRailWidth(path, w), ReloadConfig: profile.Load(path) error only, Warnings: the pending-item-2 line when Identities() shows other live sessions of the identity}; tui.Run(ctx, opts); exit 0, or 1 with `agentbus2: <err>` on stderr; usage errors exit 2
```

- [ ] **Step 1: Write the failing tests**

`internal/cli/tui_test.go` (every test sets `testTUIOptions` to pin `Width: 80, Height: 24` unless stated, restored in `t.Cleanup`; no test in the package calls `t.Parallel()`, Milestone 6's rule): `TestRunTUILocalProfile` (Milestone 6's `newTestEnv`; `sam := e.conn(t, "sam")`; sam is subscribed to `general` by `identities.Service.RegisterLocal` (on `main`: `createWithGeneral` subscribes at the then `MaxSeq`, before any message of the test), which `client.Open` calls in local mode; sam arms `Watch` then `Receive`; `RunTUI(ctx, []string{"-identity", "eric"}, e.Environ, pipeReader, io.Discard, &stderr)` in a goroutine; sam's `Identities()` shows `eric/tui`; typed `i`, `from the tui\r` reaches sam with `Display "eric/tui"` within 2 s; `\x03` → exit 0; `stderr` empty), `TestRunTUIServerProfile` (the Milestone 6 `server_test.go` setup: a real server on `127.0.0.1:0` in `open` registration mode, `remote.Register` for `eric` and `sam` with keys saved through `credential.Store.Save`, a server profile `lab` written with `e.writeProfile`; `Register` subscribes `general` through the same `createWithGeneral` (on `main`), so sam needs no explicit subscribe; the same script with `-profile lab -identity eric`; the token exchange happened: the cache file exists under the data dir), `TestRunTUIUsageErrors` (`-profile nosuch` → 2; `-h` → 0; v1's `-as eric` and `-config x` → 2 naming the flag, Definition of Done B; a positional argument → 2), `TestRunTUIWarnsOnSharedIdentity` (pending item 2: sam's conn opens a session as `eric` first; `RunTUI` as `eric` writes the warning line to stderr before anything else and still runs), `TestRunTUIRailWidthSaved` (`Width: 100, Height: 32`, the rail needs width ≥ 60; `>` pressed through the pipe → `config.json`'s `tui.rail_width` is written and the profiles kept; two TUIs saving at once are last-writer-wins through `Save`'s rename, each a whole file: stated, not tested).

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/cli/ -run 'TestRunTUI'`
Expected: build failure (`undefined: RunTUI`).

- [ ] **Step 3: Implement** `cli/tui.go`, the `tui` case in `cli.go` (`usage` gains `tui`), `main_test.go`'s list; in `scripts/m8-exit-checks.sh` make `cli=2` required (removing the comment Task 9 left) and add `"$ROOT/internal/cli/tui.go"` as a second path of the word check; in the `m8-exit-script-fires` block the real log becomes `go test -count=1 -v -run '^(TestBothModes|TestNoInboxView|TestRunTUI.*)$' ./internal/tui/ ./internal/cli/ > "$tmp/m8.log" 2>&1` (expected `modes=2`, `noinbox=1`, `cli=2`, exit 0), gains `sed '/TestRunTUIServerProfile/d'` → `cli=1`, exit 1, and fills probe (4): `internal/tui/probe_m8.go` with `import _ "github.com/ericfitz/agentbus2/internal/client"` → exit 1 and `deps_violation=github.com/ericfitz/agentbus2/internal/tui imports github.com/ericfitz/agentbus2/internal/client` (the package exists now, so the line comes from the check, not from a `go list` error). State (Definition of Done D): two TUIs on one local file both run the maintenance loop and the lease row lets one tick at a time (Milestone 6 Task 5); the state lines of Task 9 Step 3 hold here too.

- [ ] **Step 4: The milestone exit, positive**

Run (600000 ms or in the background): `env -u AGENTBUS2_TEST_POSTGRES_DSN make verify 2>&1 | tee /tmp/agentbus2-m8-verify.log | tail -3`; `env -u AGENTBUS2_TEST_POSTGRES_DSN make test-race 2>&1 | tail -4`; `go test -count=1 -v -run '^(TestBothModes|TestNoInboxView|TestRunTUI.*)$' ./internal/tui/ ./internal/cli/ > /tmp/agentbus2-m8-exit.log 2>&1; bash scripts/m8-exit-checks.sh /tmp/agentbus2-m8-exit.log`; `bash scripts/m6-exit-checks.sh --deps-only`; `bash scripts/m4-exit-checks.sh --deps-only`; after the commit, `make check-gates-selftest 2>&1 | rg -n '^selftest:'`.
Expected, quoted verbatim in the report: `verify: OK`; `test-race` `ok` everywhere; `fail=0`, `modes=2`, `noinbox=1`, `cli=2`, `words_ok=1`, `deps_ok=1`, exit 0; `deps_ok=1` from both earlier scripts; `selftest: ok m8-exit-script-fires`, `selftest: ok tui-hint-mutation` and every other block `ok`.

- [ ] **Step 5: Docs** (facts checked against the tree, Definition of Done E)

- `docs/tui.md`: start, identity (choice 2 and the shared-identity note), the keymap (from `help.go`), panes, the rail's five sections and collapsing (choice 4), `(off)`, `(no access)`, task editing, search modes, health, the `tui` settings object with the theme table (v1's roles and defaults), icons and the Nerd Font notes, what differs from v1 (no DMs, no session selection, kinds instead of prefixes and names shown in full, `-profile`/`-identity` instead of `tui_name`).
- `README.md`: `Status: Milestone 8 (TUI) done; agentbus2 tui runs over the Bus in local and server mode; release tooling is Milestone 9.` and a TUI paragraph pointing at `docs/tui.md`.
- `CLAUDE.md` Commands: one line `agentbus2 tui [-profile P] [-identity I]`; `scripts/m8-exit-checks.sh`. Design: one line for `internal/tui` (over `bus.Bus` only, the entry included; the stub-then-replace layout is history, not a rule; `tui` settings in `config.json`; `tags:` is the one synthetic rail prefix).
- ADR 0006 addendum `## Milestone 8 addendum (date)`: the TUI shipped with no inbox view and no session selection; choices 2 to 6; pending items 1 to 3 as `Claude proposal, pending Eric` with a pointer to this plan. ADR 0003 addendum: the TUI renders `Message.Display` and `Session.DisplayName`, opens with hint `tui`, sends with its session id, closes on quit, resumes a lapsed lease once; pending item 3. ADR 0008 addendum: the rail's five sections are keyed on `Kind`; v1's `shortName` (kind prefixes stripped from names) is not ported.
- `HANDOFF.md` via `/session:handoff`: Milestone 8 done at the Step 6 commit, unpushed; the Step 4 outputs; the three pending items; Milestone 9 next.

- [ ] **Step 6: Commit**

```bash
git add internal/cli/tui.go internal/cli/tui_test.go internal/cli/cli.go main_test.go scripts/m8-exit-checks.sh scripts/check-gates-selftest.sh docs/tui.md CLAUDE.md README.md docs/adr/0006-no-direct-messages.md docs/adr/0003-identity-sessions-and-delivery.md docs/adr/0008-channel-access-control-lists.md
git commit -m "feat(cli): agentbus2 tui over client.Open in local and server mode; Milestone 8 exit complete; docs

Ports v1 main.go's tui case (906dec8): identity by Decision 71, no
tui_name or --as. The TUI runs over the Bus interfaces in both modes
with no inbox view (spec 10; ADR 0006)."
```

- [ ] **Step 7: Report**

End with the Step 4 outputs quoted verbatim. No push: the next push is Eric's (`Approve: push main to origin (git push origin main)`).

---

## Self-review notes

- Spec coverage: section 10's Milestone 8 line ("the TUI runs over the `Bus` interfaces in both modes with no inbox view") is Task 9's `TestBothModes` (one program over `remote.New` in local mode, Decision 35's in-process transport, and in server mode, a TCP listener with a server-minted bearer; the probe ordered so only the loop's `Receive` plus ack can satisfy it, and a `/watch` request counted on each leg's handler) and Task 10's `TestRunTUILocalProfile`/`TestRunTUIServerProfile` (both profiles through `client.Open`); its negative half is Task 9 Step 6b's receive-loop mutation (both legs fail without the loop), with the hint mutation as a second check; the negative half of "no inbox view" is `TestNoInboxView` (behavioral: no rail position is a session or an identity, every channel read is a listed one), required by the exit script, plus the word check over production files; spec 9's porting row ("`internal/tui`, Milestone 8; no inbox view") is Tasks 1 to 7 with their v1 sources named at `906dec8`; spec 2.3's `tui --> busif` is the import rule with `internal/client` and `internal/profile` in the exit script's forbidden set and no exception for `run.go`; ADR 0003's "the TUI must use the server-assigned name" is `Session` on every `Send` (Review Focus 6), Task 3's `senderName` and the `Display` assertions in Tasks 3, 9 and 10; ADR 0008's "nothing in the TUI inspects a prefix" is `channelSection` on `Kind`, `shortName` not ported, the scoped `HasPrefix` check in Task 3 Step 4 and the exit script's word list; Decision 73 is `TestStatusTickNonAdminIsQuiet`; Decision 94 is `TestNoAccessChannelIsMarkedOnce`; Decision 83 is Task 2's loop with `TestDrainAcksLastBatch`; Decision 71 is choice 2 and `TestRunTUIUsageErrors`.
- Facts checked on `main` for this revision: `messaging/send.go` sets `Display` only when `in.Session != ""`; `channels/subscriptions.go` sets a `now` subscription's acked mark to `tx.MaxSeq()`; `channels.Service.Create` does not subscribe the creator; `sessions/open.go` seeds a fresh session's cursors from `AckedSeq`; `delivery/receive.go` honors an ack on an empty call and returns `Batch ""` then; `store.Tx.SetNotice` exists and `Status` reports kinds `capacity` and `embedding`; `scripts/m3-exit-counts.sh` matches `\([0-9.]+s\)$`; `internal/client`, `internal/profile` and `internal/cli` are not on `main` yet. Checked in the second review round: `internal/core/identities` **is** on `main` and `createWithGeneral` subscribes `general` at `MaxSeq`; Decision 35 makes local mode the generated client over the in-process transport (so `local.New` is not a product mode); `server.Bearer`, `token.NewIssuer`, `Issuer.Mint`, `keys.Generate(keys.EdDSA)` and `remote.WithBearer` exist; `delivery/watch.go` returns a zero `WatchResult` on a timeout and treats `Timeout: 0` as the 50 s cap; `sessions/open.go` keeps the session `ID` on a resume and lists every readable subscription in `Pending`, zeros included; `receive.go` sets `Batch` only when it delivers; `rg --glob '!*_test.go'` over a shell-expanded file list still searches `_test.go` files; `scripts/m6-exit-checks.sh` is not on `main`; the selftest's blocks are functions called from a list at the end of the file; v1 test helpers cross feature files (`selectTaskChannel`, `followTags`, `agentReply`, `stream`, `drainAndAck`), and v1's fixture helper is `moveTo`. Facts checked in v1 at `906dec8`: `view.go` has `railSections`, `railHeadings`, `channelSection`, `railLayout` and no `railBudgets`; `Model` has `headSel`, `collapsed`, `railTop [railSections]int`; `shortName` strips kind prefixes; `cut` uses `HasPrefix` on escape sequences.
- Names used across tasks: `Settings`, `ColorTheme`, `DefaultTheme`, `Colors`, `FindTheme`, `ColorForms`, `ColorIndex`, `Theme`, `ParseColor`, `LoadTheme`, `LoadIcons`; `client`, `newClient`, `sessionHint`, `subState` (`subOn`, `subOff`, `subNoAccess`), `subscribe`, `isSubscribed`, `noAccess`, `forget`, `known`, `sessionID`, `displayName`, `receiveLoop`, `resumeLapsed`, `close`, `errSessionLost`, the `// watch-own` and `// receive-own` markers; every `msgs.go` type (`usageMsg` included); `retryBackoff`; `busFixture`, `newCore`, `ensureIdentity`, `identityBus`, `pendingFor`, `setAdmin`, `newBusFixture`, `samSend`, `samReply`, `receiveRaw`, `waitFor`, `stubSessions`, `stubBus`, `countingBus` (`count`, `channelsOf`), and Task 3's `fixture`, `newFixture`, `receive`, `moveTo`, `drainAndAck`, `selectTaskChannel`, `followTags`, `stream`, `TestNoInboxView`; `usageCmd`; `Model` (with `railTop`, `headSel`, `collapsed`), `Host` (`ConfigPath`, `Describe`, `RailWidth`, `SaveRailWidth`, `ReloadConfig`, `Warnings`), `New`, `kind`, `isTasks`, `senderName`, `isOwn`, `tick`, `secChannels` to `railSections`, `channelSection`; the stub names of Tasks 4 to 7; `profile.TUISettings`, `TUIColorTheme`, `Config.TUI`, `SetTUIRailWidth`; `tui.Options` (`Width`, `Height`, `Stderr`), `Run`, `run`, `countingHandler` (Task 9's test-local copy); `cli.RunTUI`, `cli.testTUIOptions`; `scripts/m8-exit-checks.sh`; the self-test blocks `m8-exit-script-fires` (`block_m8_exit_script_fires`, `m8_deps_probe`) and `tui-hint-mutation` (`block_tui_hint_mutation`).
- Proportion: the v1 package is 10.5k lines; this plan fixes the seams, the names, the behavior that changed (kinds, display names, the watch loop, admin gating, no session selection, full names in the rail) and the tests; bodies are v1's at `906dec8`.
