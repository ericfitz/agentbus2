# Inspection Notice Expiry and Shared Test Helpers Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** An `inspection` notice that no replica refreshes stops counting, and any replica's successful inspection clears it. The duplicated test helpers move into one test-only package.

**Architecture:** Expiry happens at read time. A new `Core.Notices` reads every notice kind and leaves out an `inspection` notice older than `inspection_notice_ttl_seconds`. Health and Status both call it, so neither needs a new job. `noteSuccess` loses its 60 s age guard, and a process in state ok re-checks the notice at most once per `noticeResetAge`. The test helpers move to `internal/testkit`, which only `_test.go` files import.

**Tech Stack:** Go, the existing Store `Tx.Notice`/`SetNotice`/`ClearNotice`, `internal/server/config` settings.

**Spec:** `docs/adr/0013-write-inspection.md` (addendum "stale inspection notice", Eric 2026-11-03) and `docs/adr/0015-mcp-only-client.md` (addendum "shared test helpers", Eric 2026-11-03).

## Global Constraints

- No schema change and no new migration: the notice stays a row of the existing `notices` table (pre-release rule, CLAUDE.md "Design").
- The protocol version stays `1.0.0`, and `api/openapi.yaml` is not touched: the health and status documents keep their shape.
- Every new setting row goes in `docs/ops/configuration.md` in the table's existing form, since the `docs_test.go` drift test checks those rows.
- Out-of-range setting values are refused at start (Definition of Done B). NaN does not apply because the setting is an integer.
- `internal/testkit` imports only the standard library and `internal/profile`. Only `_test.go` files import it, which `TestTestkitTestOnly` checks.
- Done gate: `make verify`, then `ci.yml` and `windows.yml` green on the pushed commit.

## Review Focus

1. **Two replicas, one still failing.** Replica B succeeds and clears the notice while replica A keeps failing. A rewrites the notice on its next failure at least `noticeResetAge` after its last write, so health flaps between `degraded` and `ok` for up to a minute. That's accepted under Eric's rule 2. Task 2's test `TestNoticeClearedByOtherReplicaReturns` pins that A's next failure after the window sets the notice again.
2. **A clock skewed backwards.** A notice whose `SetAt` is in the future must still count, not expire and not panic. Task 1's `TestNoticesFutureSetAt` covers this.
3. **The TTL boundary.** A notice exactly TTL old is expired, and one 1 ms younger is live. Task 1's `TestNoticesInspectionExpiry` covers this.
4. **Expiry applies only to the inspection kind.** A `capacity` or `embedding` notice of any age still counts. Task 1's `TestNoticesOtherKindsNeverExpire` covers this.
5. **State ok, no notice.** An ok process makes no Store call on a success within `noticeResetAge` of its last check, which keeps the hot path cheap. Task 2's `TestNoteSuccessOKThrottled` covers this.

---

### Task 1: Expire an unrefreshed inspection notice at read time

**Files:**
- Modify: `internal/core/config.go` (field, default, bounds), `internal/core/notices.go` (`Core.Notices`), `internal/core/health/health.go:74-81`, `internal/core/channels/admin.go:56-63`, `internal/server/config/config.go` (setting, `Default`, `Core()` mapping), `docs/ops/configuration.md` (row after `inspection_on_failure`), `docs/ops/health.md:66`, `docs/ops/inspection.md:77`
- Test: `internal/core/notices_test.go` (new), `internal/core/config_test.go` or the existing config bounds test, `internal/server/config/config_test.go`

**Interfaces:**
- Produces: `core.Config.InspectionNoticeTTLSeconds int` (default `300`, bounds 120 to 86400, error `inspection_notice_ttl_seconds must be between 120 and 86400, got %d`); setting `inspection_notice_ttl_seconds` (`AGENTBUS2_SERVER_INSPECTION_NOTICE_TTL_SECONDS`); `func (c *Core) Notices(ctx context.Context, tx store.Tx) ([]bus.Notice, error)`, which returns the notices in `NoticeKinds()` order and leaves out an `inspection` notice when `c.Now().UnixMilli() - SetAt >= TTL*1000`.

- [ ] **Step 1: Write the failing tests** in `internal/core/notices_test.go`, on a SQLite core with a `testClock` (as in `inspect_test.go`):
  - `TestNoticesInspectionExpiry`: TTL 300, the inspection notice set at `t0`. At `t0+299_999` ms `Notices` returns it, and at `t0+300_000` it returns `[]` (empty, not nil-vs-empty sensitive: assert `len == 0`).
  - `TestNoticesOtherKindsNeverExpire`: `capacity` and `embedding` set at `t0`. At `t0+48h` both are returned, in that order.
  - `TestNoticesFutureSetAt`: the inspection notice `SetAt = t0+10_000`, clock at `t0`. It is returned.
  - Config: `InspectionNoticeTTLSeconds` values `119` and `86401` are refused with the message above, and `120` and `86400` are accepted. The default is `300`.
- [ ] **Step 2: Run** `go test ./internal/core/ -run 'TestNotices|TestConfig' -count=1`. Expected: FAIL (undefined `Notices` / field).
- [ ] **Step 3: Implement** the field, default and bounds; `Core.Notices`; and replace the two `for _, kind := range core.NoticeKinds()` loops in `health.go` and `admin.go` with `s.Core.Notices(ctx, tx)` (or the receiver each file has). Then add the server setting next to `inspection_on_failure`, with `Default` copying `core.Defaults()` and `Core()` mapping it.
- [ ] **Step 4: Server config tests.** `TestDefaultMatchesCore` passes, and `TestValidateRejects` gains `inspection_notice_ttl_seconds=119`.
- [ ] **Step 5: Docs.** Add the configuration row: int, default `300`, "120 to 86400. An `inspection` notice that no failing replica has rewritten for this long no longer counts toward `degraded`." In `health.md:66` and `inspection.md:77`, replace the "stays until any replica restarts" text with the new rules: a failing replica rewrites the notice at most once a minute; the next successful inspection on any replica clears it; one nobody rewrote for `inspection_notice_ttl_seconds` stops counting.
- [ ] **Step 6: Run** `go test ./internal/core/... ./internal/server/... -count=1` and `go test ./internal/conformance/bus/ -run Health -count=1` (SQLite; Postgres runs in `make verify`). Expected: PASS.
- [ ] **Step 7: Commit** `feat(inspection): expire an unrefreshed inspection notice (inspection_notice_ttl_seconds)`.

### Task 2: Any replica's success clears the notice

**Files:**
- Modify: `internal/core/inspect.go:78-80` (comment), `:457-520` (`noteSuccess`), `internal/core/core.go:60-64` (one field), `docs/adr/0013-write-inspection.md` (agent choice 4: mark it superseded by the addendum)
- Test: `internal/core/inspect_test.go` (rewrite the assertions at `:600-655`; add the tests below)

**Interfaces:**
- Consumes: Task 1's `Core.Notices` isn't needed here. `noteSuccess` keeps reading `tx.Notice(ctx, NoticeInspection)` directly.
- Produces: `Core.noticeCheckedAt time.Time` (guarded by `inspectMu`), set each time an ok-state success reads the notice.

New rule for `noteSuccess`:
- **State ok:** return at once if `now - noticeCheckedAt < noticeResetAge`. Otherwise set `noticeCheckedAt = now` and read the notice. If one is present, clear it, whatever its age.
- **State unknown or failing:** read, and clear any present notice whatever its age. Drop the `>= noticeResetAge` checks in both the read and the Tx. The `failGen` race handling stays as it is.
- The read-then-Tx split stays, so a success that finds no notice opens no write Tx.

- [ ] **Step 1: Write the failing tests** in `internal/core/inspect_test.go`:
  - Change `:603-607`: a success with a 29 s old notice now **clears** it (`setAt() == -1`). Delete the 64 s step, which is now redundant.
  - Change `:633-655`: the ok-state core `c2`, 5 minutes later, **clears** the "another replica" notice (`setAt() == -1`) and makes exactly one `Store.Read` plus one `Store.Tx`.
  - `TestNoteSuccessOKThrottled`: in state ok, a second success within 59 s makes 0 Store calls (`countingStore`). One at 60 s makes one Read.
  - `TestNoticeClearedByOtherReplicaReturns`: core A fails at `t0` and sets the notice. Core B (same store) succeeds at `t0+1s` and clears it. A fails at `t0+30s`, which writes nothing (the per-process window). A fails at `t0+61s`, and the notice is set again with `SetAt == t0+61_000`.
- [ ] **Step 2: Run** `go test ./internal/core/ -run 'TestInspect|TestNote|TestNotice' -count=1`. Expected: FAIL on the changed assertions.
- [ ] **Step 3: Implement** the rule above. Update the doc comments on `noticeResetAge` and `noteSuccess`, and change the `open note: write-inspection 4` marker comment to name the addendum.
- [ ] **Step 4: Run** the same command plus `go test -race ./internal/core/ -count=1` and `go test ./internal/conformance/bus/ -run Inspect -count=1`. Expected: PASS. The conformance test at `inspect_send_test.go:349` may assert the old age rule. If it does, update it to the new rule and say so in the commit.
- [ ] **Step 5: ADR.** Under agent choice 4 in `docs/adr/0013-write-inspection.md`, add "Superseded by the 2026-11-03 addendum: any success clears, and an unrefreshed notice expires." In "Consequences, `degraded` on failure", replace "A success clears the notice once it is at least 60 s old" with the new rule.
- [ ] **Step 6: Commit** `feat(inspection): any replica's success clears the inspection notice`.

### Task 3: Shared test helpers in `internal/testkit`

**Files:**
- Create: `internal/testkit/testkit.go` and `internal/testkit/testkit_test.go`
- Modify: `internal/tui/run_test.go:41-56`, `internal/tui/termapp_test.go:350`, `internal/mcpcli/server_test.go:15-30`, `internal/client/server_test.go:31-46`, `internal/client/human_test.go:61`, `internal/client/localmode/storelog_test.go:18-33`, `internal/client/resolve_test.go:16-44`, `internal/client/localmode/env_test.go` (delete the copy; the file goes if nothing remains)

**Interfaces:**
- Produces:
  - `type LockedBuffer struct` with `Write([]byte) (int, error)` and `String() string`, mutex-guarded. Its zero value is ready to use.
  - `type ProfileEnv struct { Home, Config, Data string; Environ []string }`
  - `func NewProfileEnv(t testing.TB, extra ...string) *ProfileEnv`, with the same directories and `Environ` contents as today's `newEnv`.
  - `func (e *ProfileEnv) Save(t testing.TB, ps ...profile.Profile)`, the same as today's `save`.
  - `resolve_test.go`'s `credential` helper stays in `internal/client`, as a function over `*testkit.ProfileEnv`, because only that package uses it.

- [ ] **Step 1: Write the failing tests** in `internal/testkit/testkit_test.go`:
  - `TestLockedBufferConcurrent`: 8 goroutines each write `"x"` 1000 times, and `len(String()) == 8000` under `-race`.
  - `TestProfileEnvLayout`: the three directories exist under one `t.TempDir()`. `Environ` contains `AGENTBUS2_CONFIG_DIR=<Config>`, `AGENTBUS2_DATA_DIR=<Data>` and `HOME=<Home>`, plus each `extra` entry. Copy the exact keys from today's `newEnv`.
  - `TestTestkitTestOnly`: runs `go list -f '{{.ImportPath}} {{join .Imports " "}}' ./...` from the module root and fails if any non-test import list names `internal/testkit`.
- [ ] **Step 2: Run** `go test ./internal/testkit/ -count=1`. Expected: FAIL (package has no code).
- [ ] **Step 3: Implement** `testkit.go` from the existing `lockedBuf` and `newEnv`/`save` bodies, with the package comment "imported by test code only".
- [ ] **Step 4: Migrate** each file in the list to `testkit.LockedBuffer` and `testkit.NewProfileEnv`, and delete the local copies. Rename the call sites only; change no test logic.
- [ ] **Step 5: Gates.** Run `scripts/m6-exit-checks.sh --deps-only`, `scripts/m8-exit-checks.sh --deps-only` and `scripts/m10-exit-checks.sh --deps-only`. Expected: all pass with **no allowlist edit**, since each gate reads non-test `.Imports` only. Record this in the ADR 0015 addendum: "No allowlist change was needed: the gates check non-test imports only, and `TestTestkitTestOnly` keeps `internal/testkit` out of them."
- [ ] **Step 6: Run** `go test -race ./internal/testkit/ ./internal/tui/ ./internal/mcpcli/ ./internal/client/... -count=1`. Expected: PASS.
- [ ] **Step 7: Commit** `test: share lockedBuf and the profile env helper through internal/testkit (ADR 0015 addendum)`.

### Task 4: Done gate

- [ ] Run `make verify` (needs Docker) and show the tail. Run `graphify update .` if `graphify-out/` exists. After Eric's push, watch `ci.yml` and `windows.yml` on the pushed commit.
