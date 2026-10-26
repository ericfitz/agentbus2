# Channel Visibility Implementation Plan (Eric's decisions of 2026-10-25)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `ListChannels` returns only the channels the caller holds an ACL entry on (an administrator: every channel), each with a required `capabilities` array of strings whose first value is `bridged` (a bridge in state `live`), on the Bus, the wire, the MCP tool, the CLI and the TUI; `GET /v1/discover` advertises the change; the TUI draws a bridged channel with its own overridable icon at every kind-icon site and keeps `(no access)` for the cases that remain.

**Architecture:** A new Store query `Tx.VisibleChannels(identity)` (ids with any ACL entry for the identity, `everyone`, `admins` for an administrator, or the implicit creator entry) joins `ReadableChannels` and `EffectivePermissions` in `internal/store` and `sqlstore`; `channels.Service.List` runs one read transaction (`ListChannels`, `VisibleChannels` unless the identity row is `Admin`, `ListBridges`) and no other rule changes. `bus.Channel` gains `Capabilities []ChannelCapability`; the OpenAPI `Channel` and `ChannelInfo` schemas gain a required `capabilities` string array; `server.Capabilities` gains one name. Clients copy the strings through and ignore unknown ones; the TUI gets one `channelIcon` helper and an `iconBridged` icon. No schema migration, no setting.

**Tech Stack:** Go, kin-openapi + oapi-codegen (`make generate`), modernc SQLite and pgx Postgres (one `sqlstore` implementation), testcontainers for the Postgres suites, Bubble Tea v2 (TUI), the MCP Go SDK.

**Spec:** `docs/superpowers/specs/2026-10-25-channel-visibility-design.md`. Section 1 and section 4 are Eric's decisions and are implemented as written; section 5 is the one pending item (the discovery capability name `channel_capabilities`, used by Task 2 and marked there).

## Global Constraints

- Done gate: `make verify` (needs Docker; a run that skips the Postgres suites is not a pass). Work is done only when `ci.yml` and `windows.yml` are green on the pushed commit; a red `windows.yml` is fixed first. Tasks 1, 2 and 5 run `make verify`; every task runs `make lint` and `make test`; Tasks 1 and 2 also run `make test-conformance` (both engines, both transports) and `bash scripts/m3-exit-counts.sh` on a `-v` log of `./internal/conformance/bus/`.
- Task 2 edits `api/openapi.yaml`, runs `make generate` and commits `internal/gen` (never edited by hand); `make generate-check` must then be clean.
- Import rules (CLAUDE.md), re-checked by `bash scripts/m4-exit-checks.sh --deps-only && bash scripts/m5-exit-checks.sh --deps-only && bash scripts/m6-exit-checks.sh --deps-only && bash scripts/m7-exit-checks.sh --deps-only && bash scripts/m8-exit-checks.sh --deps-only` in every task that adds an import. No task here needs a new import edge: `internal/store/...` never imports `internal/core/...`; `internal/tui` stays on `internal/bus` and `internal/version`.
- Pinned counts that stay: `TestACLMatrix` 26 sub-cases and 52 leaves per engine (`scripts/m3-exit-counts.sh`; Task 1 changes the `list channels` row's content, never its count, its `perm: ""` or its name in `section46Rows`); the e2e counts (`TestSection24Sequence` 13 steps, `TestExitCodeTable` 11 cells); `TestAuthEndToEnd` 11 leaves per engine; the Milestone 8 counts (`TestBothModes`, `TestNoInboxView`, `TestRunTUI*Profile`); the MCP tool count 25; the gate anchors of `scripts/check-gates-selftest.sh` (`if !readable[sub.ChannelID] {` in `internal/core/delivery/receive.go`, `if !id.Admin {` in `internal/core/authz.go`: never edit `RequireAdmin` or `Receive`'s filter). `ReadableChannels` and `EffectivePermissions` are not edited: the listing gets its own query.
- Counts that move on purpose, each in the task named: the Store suite gains `TestVisibleChannels` (Task 1); `TestChannelListNeedsNoRead` is replaced (Task 1); `TestCapabilitiesComputed` (Task 2); `TestNoAccessChannelInRail` and `TestNoAccessChannelIsMarkedOnce` are rewritten (Task 4); `TestLoadIconsNerdfontWidthParity` and `TestIconSetsCoverTheSameNames` iterate the icon maps, so `bridged` must be in `iconVars` and `nerdIcons` and 3 columns wide in the Nerd Font set (Task 4).
- Word checks: `scripts/m6-exit-checks.sh` applies to `internal/mcpserver/server.go`, `inputs.go` and `internal/cli/skills/using-agentbus2/SKILL.md` (none of `dm/`, `-filter`, `@<`, `memory/`, `tasks/`, `general/` in the text Task 3 adds); `scripts/m8-exit-checks.sh` applies to `internal/tui` non-test files and `internal/cli/tui.go` (no `inbox`, `dm/`, `DMPrefix`, `DMChannel`, `IsTaskChannel`, `tui_name`).
- Eric's capability principle (spec 3.2): `capabilities` is an array of strings, never a boolean, with no enum in the schema; every client copies unknown strings through and acts only on the ones it knows; an absent array (an older server) normalizes to empty and is never refused. JSON arrays are `[]` when empty, never null. Definition of Done B still applies to `kind`.
- Definition of Done F: anything beyond a one-liner goes in a script under `scripts/` or the scratchpad with `set -euo pipefail`.
- The gear-width fix landed as b19416e (`internal/tui/icons.go` has `LoadIconsFor` and `appleTerminalIcons`; `icons_unix.go` and `icons_windows.go` are gone). Task 4's line references are from that commit; re-read before editing all the same.
- Docs move with the code in the task that changes the behavior; Task 5 is the sweep. American English. Commits land on `main`, one per task, ending with the session's `Co-Authored-By` and `Claude-Session` lines. No push without Eric. `git add` only the files listed in the task, never `-A`.

## Review Focus

1. The filter is `Tx.VisibleChannels` and nothing else: no second definition appears in the core, the handler or a client, and `ReadableChannels` is untouched so `Receive` and `Search` still deliver only readable content (Task 1).
2. The administrator exception reads the `Admin` flag of the row `RequireIdentity` returned and never calls or edits `RequireAdmin` (Task 1; gate anchor).
3. `bridged` means `State == store.BridgeLive` exactly, and `channels.liveBridge` (`creating` or `live`) is untouched; `Status` reports the same `Capabilities` as `List` (Task 1).
4. No refusal code changes: `History`, `Subscribe`, `Search` with a channel, memory and task reads still answer `not_found` then `forbidden`; `create` of an existing hidden name is still `conflict` (Task 1's matrix run proves the 52/52 count).
5. `capabilities` is required and `[]` when empty on every wire response (`Channel` and `ChannelInfo`), unknown strings pass through every client, and no client refuses an absent array (Task 2 tests, Task 3 outputs).
6. The TUI's `(no access)` path still works for an administrator and a write-only holder; a non-administrator with no entry never sees the row (Task 4 tests).

---

### Task 1: `Tx.VisibleChannels`, the rule in the core, `bus.Channel.Capabilities`

**Files:**
- Modify: `internal/store/channels.go` (`Channels` interface: `VisibleChannels`), `internal/store/sqlstore/acl.go` (the query), `internal/store/doc.go` (one sentence), `docs/adr/0004-store-contract-and-storage.md` (new addendum "Channel visibility (2026-10-25)": the query, why `ReadableChannels` stays), `internal/bus/types.go` (`ChannelCapability`, `ChannelBridged`, `Channel.Capabilities`, `Channel.Has`; comments no longer cite Decision 94 as "to anyone"), `internal/core/channels/channels.go` (`List`; `liveBridge` comment says the pin rule is wider than the trait), `internal/core/channels/admin.go` (`Status` sets `Capabilities`), `internal/core/convert.go` (`ChannelToBus` and `ChannelInfoToBus` take the capabilities, or sibling constructors), `internal/core/channels/service.go` (`read` returns the identity row, or `List` runs `Store.Read` itself)
- Test: `internal/conformance/store/acl_test.go` or a new `visible_test.go` (`TestVisibleChannels`), `internal/conformance/bus/channels_test.go` (replace `TestChannelListNeedsNoRead` with `TestChannelListFiltersByEntry` and add `TestChannelListCapabilities`), `internal/conformance/bus/aclmatrix_test.go` (the `list channels` row)

**Interfaces:**
- Consumes: `Tx.ListChannels`, `Tx.ListBridges`, `Tx.ACL`/`SetACL` (`internal/store/channels.go`, `bridges.go`), `Core.RequireIdentity` (returns `store.Identity` with `Admin`), `t.sel`, `badText`, `store.Everyone`, `store.Admins` (`sqlstore/acl.go`).
- Produces: `VisibleChannels(ctx, identity string) ([]int64, error)` on `store.Channels`, doc comment "VisibleChannels lists the ids where identity holds any ACL entry: a stored row for identity, for everyone, or for admins when identity is an administrator, or the channel's creator is identity (the implicit entry, ADR 0008); ascending. An identity failing TextRule is bound as everyone, like ReadableChannels." SQL: `ReadableChannels`' query without `a.can_read = TRUE`, plus `OR c.creator = ?`. In `internal/bus`: `type ChannelCapability string`, `const ChannelBridged ChannelCapability = "bridged"`, `Channel.Capabilities []ChannelCapability` (never nil on the Bus), `func (c Channel) Has(cap ChannelCapability) bool`. In `internal/core/channels`: `func bridgedCaps(b store.Bridge, ok bool) []bus.ChannelCapability` (`[bridged]` when `ok && b.State == store.BridgeLive`, else empty); `Service.List(ctx, identity)` with the doc comment "List returns the channels identity holds an ACL entry on (Tx.VisibleChannels), every channel when its row is Admin (Eric's decision 2026-10-25 (1)), each with its capabilities (decision (2); bridged while the bridge is live)".

- [ ] **Step 1: Write the failing tests.** Store suite, `TestVisibleChannels` under `dbtest.ForEachEngine`: identities `pat`, `kim`, `root` (admin); channels `a` (default ACL, creator pat), `b` (`[{kim, [write]}]`, creator pat), `c` (`[]`, creator kim), `d` (`[{admins, [admin]}]`, creator ""), `e` (`[{pat, [read]}]`, creator ""); `VisibleChannels("kim")` = ids of `a` (everyone), `b` (write row), `c` (creator); `VisibleChannels("pat")` = `a`, `b`, `e`; `VisibleChannels("root")` = `a`, `d`; `VisibleChannels("nobody")` = `a`; `VisibleChannels("bad\x00text")` = `a`; ascending. Bus suite, `TestChannelListFiltersByEntry`: `h.ensureIdentity("pat")` and `h.ensureIdentity("kim")` first (Decision 96: the Store refuses an entry naming an unregistered identity); `h.channel("secret", KindMemory, "", [{pat, [read]}])`, `h.channel("alpha", KindOrdinary, "pat", nil)`, `h.channel("adminonly", KindOrdinary, "kim", [{kim, [admin]}])`, `h.channel("writeonly", KindOrdinary, "", [{kim, [write]}])`, `h.channel("mine", KindOrdinary, "kim", [])`; `h.as("kim").ListChannels` is exactly `[adminonly, alpha, mine, writeonly]` in byte order with `Capabilities` equal to `[]bus.ChannelCapability{}` on each (the bus harness creates no `general`); `h.as("pat").ListChannels` is `[alpha, secret]`; `h.admin("root").ListChannels` is all five. `TestChannelListCapabilities`: four ordinary channels with the default ACL; through `h.tx`, create the scope row first, then `CreateBridge` rows (a bridge needs a non-empty `Scope`; pattern: `scopes_test.go:131`, `bridgeguard_test.go:108`) for three of them: one `SetBridgeSlackChannel` + `SetBridgeLive`, one left `creating`, one `SetBridgeArchived`; `h.as("kim").ListChannels` reports `[bridged]` for the live one and `[]` for the other three; `h.admin("root").Status()` reports the same on its `Channels`. Matrix row `list channels` (keep `perm: ""`): `refuse` asserts `channelNames(t, b)` lacks `ch`; `allow` has `h.as("pat").SetACL(ch, [{pat, [read, write, admin]}, {kim, [write]}])` then asserts `channelNames(t, b)[ch] == KindOrdinary`. Do not touch `matrixSubcases` or `section46Rows`.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test -count=1 -p 1 -run 'TestVisibleChannels' ./internal/conformance/store/ ; go test -count=1 -p 1 -run 'TestChannelListFiltersByEntry|TestChannelListCapabilities|TestACLMatrix' ./internal/conformance/bus/`
Expected: FAIL to compile (`VisibleChannels`, `Capabilities` undefined), then the filter, capabilities and matrix assertions.

- [ ] **Step 3: Implement** the Store query (one `sqlstore` implementation serves both engines; `go vet ./...` finds any other `store.Tx` implementer, including the suites' fault wrappers), the Bus type, `bridgedCaps`, `List` as spec 3.1 (one `Store.Read`; `RequireIdentity`; `ListChannels`; `VisibleChannels` into a set when `!id.Admin`; `ListBridges` into `map[int64]store.Bridge` by `ChannelID`), `Status` through the same map, the converters. Keep the result in the Store's byte order of the name and `[]bus.Channel{}` when empty; every `Capabilities` slice non-nil.

- [ ] **Step 4: Run**

Run: `make lint && make test && make test-conformance && go test -count=1 -p 1 -v ./internal/conformance/bus/ > /tmp/ab2-m3.log 2>&1; bash scripts/m3-exit-counts.sh /tmp/ab2-m3.log && bash scripts/m7-exit-checks.sh --deps-only`
Expected: PASS on both engines and both transports; `fail=0`, `matrix_sqlite=52`, `matrix_postgres=52`; the Milestone 7 import rule holds.

- [ ] **Step 5: Run the done gate** `make verify` and keep the output for the report.

- [ ] **Step 6: Commit**

```bash
git add internal/store/channels.go internal/store/sqlstore/acl.go internal/store/doc.go docs/adr/0004-store-contract-and-storage.md internal/bus/types.go internal/core/channels/channels.go internal/core/channels/admin.go internal/core/channels/service.go internal/core/convert.go internal/conformance/store/visible_test.go internal/conformance/bus/channels_test.go internal/conformance/bus/aclmatrix_test.go
git commit -m "feat(core,store): ListChannels lists the channels the caller holds an ACL entry on, administrators unfiltered, with channel capabilities (bridged while live)"
```

---

### Task 2: The wire: `capabilities` on `Channel` and `ChannelInfo`, the discovery capability, server and remote

**Pending Eric (spec section 5):** the discovery capability name; this task uses `channel_capabilities`. Changing it later is the constant in `discovery.go`, `TestCapabilitiesComputed` and two docs lines.

**Files:**
- Modify: `api/openapi.yaml` (`Channel.required: [name, kind, capabilities]`, `ChannelInfo.required: [name, kind, creator, created_at, capabilities]`, `capabilities: {type: array, items: {type: string}, description: "Traits of the channel; a string present means the trait applies. Known: bridged (a live Slack bridge). Clients ignore unknown values."}` on both; `listChannels` 200 description "Channels the caller holds an ACL entry on; an administrator sees every channel."), `internal/gen/*` and `internal/gen/spec/*` (by `make generate`), `internal/server/channels.go` (`ListChannels` and `channelInfoToGen` map the strings; `[]string{}` when empty), `internal/server/discovery.go` (`Capabilities` adds `"channel_capabilities"`; comment names the pending item), `internal/bus/remote/channels.go` (`ListChannels`, `fromGenChannelInfo` copy the strings; nil normalizes to `[]ab2bus.ChannelCapability{}`), `docs/ops/deploy.md:101` and `docs/ops/health.md:121` (the capability list)
- Test: `internal/server/discovery_test.go` (`TestCapabilitiesComputed`: `["channel_capabilities", "health", "long_poll"]` and with the embedder), `internal/server/channels_test.go` (new `TestListChannelsFilteredOnTheWire`: two identities over `e.client`, one named in a channel's ACL, one not; the second's list lacks it; through `e.raw` the JSON of every channel has `"capabilities":[]` present, never null, on `GET /v1/channels` and on a `POST /v1/channels` answer), `internal/bus/remote/channels_test.go` (new: a canned 200 body with `"capabilities":["bridged","future"]` maps to both strings and `Has(ChannelBridged)` is true; a body without the member maps to an empty, non-nil slice; neither is an error)

**Interfaces:**
- Consumes: Task 1's `bus.Channel.Capabilities`; `gen.Channel.Capabilities []string` and `gen.ChannelInfo.Capabilities []string` after `make generate`; the `canned` transport of `remote_test.go:120`.
- Produces: the capability constant in `internal/server/discovery.go`; nothing new on the Bus.

- [ ] **Step 1: Write the failing tests.**
- [ ] **Step 2: Run them to verify they fail**

Run: `go test -count=1 -run 'TestCapabilitiesComputed|TestListChannelsFilteredOnTheWire' ./internal/server/ && go test -count=1 -run 'Capabilit' ./internal/bus/remote/`
Expected: FAIL (the capability list; `gen.Channel` has no `Capabilities`).

- [ ] **Step 3: Edit the document, generate, map.** `make generate`; the server and remote converters; the two ops docs lines.

- [ ] **Step 4: Run**

Run: `make generate-check && make lint && make test && make test-conformance && bash scripts/m4-exit-checks.sh --deps-only`
Expected: PASS; the inproc leg of the bus suite proves the remote `Bus` returns the filtered list with `Capabilities` (Task 1's `TestChannelListCapabilities` runs over the wire there).

- [ ] **Step 5: Run the done gate** `make verify`.

- [ ] **Step 6: Commit**

```bash
git add api/openapi.yaml internal/gen internal/server/channels.go internal/server/discovery.go internal/server/discovery_test.go internal/server/channels_test.go internal/bus/remote/channels.go internal/bus/remote/channels_test.go docs/ops/deploy.md docs/ops/health.md
git commit -m "feat(api): required channel capabilities on Channel and ChannelInfo, the channel_capabilities discovery capability, remote mapping"
```

---

### Task 3: MCP `list_channels`, CLI `channels list`, `status`, the `channels delete` prompt, the skill text

**Files:**
- Modify: `internal/mcpserver/server.go` (`channelOut` and `channelInfoOut` gain `Capabilities []string json:"capabilities"`, `[]string{}` when empty; the `list_channels` description: "Agentbus2: list the channels you hold an access entry on, with their kind and capabilities (bridged: people follow the channel from Slack and replies land in its thread; ignore capability values you do not know). An administrator sees every channel."; `create_channel`'s answer carries the member), `internal/cli/channels.go` (`opChannelOut` and `opChannelInfoOut` gain `Capabilities []string json:"capabilities"`, never null; `runChannelsDelete`: when the name is absent from the list and no `-key`, `confirmDelete(name, "", ...)` prompts without a kind and the Bus answers), `internal/cli/status.go` (append ` bridged` to a row whose capabilities include it), `internal/cli/skills/using-agentbus2/SKILL.md` (the four `list_channels` sentences: "the channels you hold an access entry on"; one sentence on `capabilities` and `bridged`; unknown values are ignored), `CLAUDE.md` (the agentview sentence: "and `ListChannels` lists only the channels the caller holds an ACL entry on, administrators excepted; each carries `capabilities`")
- Test: `internal/mcpserver/server_test.go` (the `list_channels` assertion at `:657` also checks `capabilities` is present and empty; a new case where an identity with no entry on a channel does not see it), `internal/cli/channels_test.go` (the `list` JSON carries `"capabilities":[]`; `delete` without `-y` of a channel absent from the caller's list, answered `y`, reaches the Bus and is answered by it), `internal/cli/status_test.go` (a row of a channel with a `live` bridge row prints ` bridged`)

**Interfaces:**
- Consumes: Task 1's `bus.Channel.Capabilities` and `Has`, `confirmDelete(name, kind, ...)` (`internal/cli/channels.go`).
- Produces: JSON member `capabilities` on the MCP and CLI outputs; no new flag.

- [ ] **Step 1: Write the failing tests.**
- [ ] **Step 2: Run them to verify they fail**

Run: `go test -count=1 -run 'TestTools|TestChannels|TestStatus' ./internal/mcpserver/ ./internal/cli/`
Expected: FAIL on the `capabilities` member and the delete prompt.

- [ ] **Step 3: Implement**, then the word check: `bash scripts/m6-exit-checks.sh --deps-only` plus `grep -nE 'dm/|-filter|@<|memory/|tasks/|general/' internal/mcpserver/server.go internal/cli/skills/using-agentbus2/SKILL.md` must print nothing new.

- [ ] **Step 4: Run** `make lint && make test`. The e2e test (`internal/e2e`, in `make test`) proves the CLI and MCP paths over a real server: its step and cell counts must not move.

- [ ] **Step 5: Commit**

```bash
git add internal/mcpserver/server.go internal/mcpserver/server_test.go internal/cli/channels.go internal/cli/channels_test.go internal/cli/status.go internal/cli/status_test.go internal/cli/skills/using-agentbus2/SKILL.md CLAUDE.md
git commit -m "feat(mcp,cli): channel capabilities in list_channels and channels list; the entry-filtered list in the tool and skill text; delete prompt without the kind"
```

---

### Task 4: TUI: the bridged icon, one icon helper, `(no access)` for what remains

**Files** (line numbers from b19416e; re-read before editing):
- Modify: `internal/tui/icons.go` (`iconBridged = "\U0001F517️ "` in the rail-icons `var` block at `:15`; `iconVars["bridged"]` at `:84`; `nerdIcons["bridged"] = ""` with the comment `// fa-slack` at `:104`; no `appleTerminalIcons` entry: U+1F517 is Emoji_Presentation=Yes), `internal/tui/view.go` (new `func channelIcon(c bus.Channel) string` and `func (m Model) channelOf(name string) bus.Channel`; `channelLabel` `:68`, the rail row `:547` and the compose prompt `:784` call `channelIcon`; the rail row's `(no access)` comment at `:543` names the administrator, the write-only or admin-only holder and the revocation window instead of Decision 94), `internal/tui/search.go` (`:134`: hits use `channelIcon(m.channelOf(hit.Channel))`, which falls back to a chat row when the channel is not in the rail), `internal/tui/model.go` (`setChannels` `:1303` keeps the full `bus.Channel` by name for `channelOf`, next to `m.kinds`; the `loadHistory` `:1399` and `markNoAccess` `:1480` comments), `docs/tui.md` ("Rail markers": `(no access)` appears for an administrator, who is listed every channel, for an identity whose only entry is `write` or `admin`, and for anyone in the moment between a revocation and the next 5 s refresh; the `icons` section and the icon-name list gain `bridged` with both glyphs; a sentence that a channel with the `bridged` capability carries the link icon wherever its kind icon is drawn)
- Test: `internal/tui/icons_test.go` (`TestLoadIconsNerdfontWidthParity` and `TestIconSetsCoverTheSameNames` pass unchanged with the new name; add `TestBridgedIconGlyphs`: emoji `iconBridged` starts with U+1F517, Nerd Font `nerdIcons["bridged"] == ""`, `icon_map` `{"bridged": "L"}` overrides it, `LoadIconsFor` with `TERM_PROGRAM=Apple_Terminal` leaves it as the emoji set has it), `internal/tui/rail_test.go` (`TestBridgedChannelRowIcon`: a `bus.Channel{Name: "job-chat", Kind: KindOrdinary, Capabilities: [bridged]}` row renders `iconBridged + "job-chat"` in the rail, the header label and the compose prompt; a search hit on it carries the icon; a channel with an unknown capability string renders as chat), `internal/tui/model_test.go` (`TestNoAccessChannelInRail` becomes three tests: `TestChannelWithoutEntryAbsentFromRail`, where `ann` (no entry, not an administrator) never sees `private` and `Subscribe`/`History` are never called for it (`countingBus`); `TestAdminSeesNoAccessRow`, where `setAdmin(t, f.core, "ann")` before the first status round and the existing assertions hold: `private (no access)`, dim style, `no read access to private`, no retry, at most one toast; `TestWriteOnlySeesNoAccessRow`, where `private`'s ACL adds `{ann, [write]}` and the same assertions hold), `internal/tui/client_test.go` (`TestNoAccessChannelIsMarkedOnce` with `setAdmin` on `ann`; its assertions hold)

**Interfaces:**
- Consumes: `bus.Channel.Capabilities`, `bus.Channel.Has`, `bus.ChannelBridged`; `setAdmin` (`internal/tui/fixture_test.go:103`); `padIcon`, `setIcons`, `emojiIcons`, `nerdIcons`, `LoadIconsFor`.
- Produces: `channelIcon`, `channelOf`, `iconBridged`; no Settings change (`icon_map` already accepts any `iconVars` name).

- [ ] **Step 1: Write the failing tests.**
- [ ] **Step 2: Run them to verify they fail**

Run: `go test -count=1 -race -run 'TestBridged|TestChannelWithoutEntryAbsentFromRail|TestAdminSeesNoAccessRow|TestWriteOnlySeesNoAccessRow|TestNoAccessChannelIsMarkedOnce|TestLoadIcons|TestIconSets' ./internal/tui/`
Expected: FAIL (`iconBridged` undefined; the admin and write-only fixtures are new).

- [ ] **Step 3: Implement**, then `GOOS=windows GOARCH=amd64 go vet ./internal/tui/` (cheap; `icons_windows_test.go` still exists).

- [ ] **Step 4: Run** `make lint && make test && go test -count=1 -v ./internal/tui/ ./internal/cli/ > /tmp/ab2-m8.log 2>&1; bash scripts/m8-exit-checks.sh /tmp/ab2-m8.log`
Expected: PASS; `modes=2`, `noinbox=1`, `cli=2`, `words_ok=1`.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/icons.go internal/tui/icons_test.go internal/tui/view.go internal/tui/search.go internal/tui/model.go internal/tui/model_test.go internal/tui/client_test.go internal/tui/rail_test.go docs/tui.md
git commit -m "feat(tui): the bridged icon (link, fa-slack) through one channelIcon helper; (no access) kept for administrators, write-only holders and the revocation window"
```

---

### Task 5: Records and the sweep

**Files:**
- Modify: `docs/adr/0008-channel-access-control-lists.md` (new section "Decisions of 2026-10-25 (Eric)" transcribing spec sections 1 and 4 with "Eric's decision 2026-10-25" labels, the pending discovery capability name marked "pending Eric"; mark Decision 94 "superseded by Eric's decision 2026-10-25 (1)" in the second-round list and in the Milestone 3 addendum's "Decision 94 in the types"; replace the Milestone 8 addendum's last sentence), `docs/adr/0009-scopes-and-slack-bridge.md` (Consequences: "`ListChannels` reports a channel whose bridge is `live` with the `bridged` capability; its named identities and the administrators are the only identities that list it"), `docs/adr/README.md` (no new row; the 0008 title row is unchanged), `docs/superpowers/specs/2026-10-19-agentbus2-design.md` (section 3.2 channels row, section 4.6.1 Decision 94 paragraph and the "list channels" table row, Question 30: each gains "Superseded by Eric's decision 2026-10-25 (1) (`2026-10-25-channel-visibility-design.md`)"; the text stands), `docs/superpowers/plans/2026-10-21-milestone-8-tui.md:36` (a parenthetical "superseded 2026-10-25" at the start of the line), `docs/ops/slack-bridge.md` (one sentence after the named-only ACL item: who lists a bridged channel, the `bridged` capability and the TUI icon), `docs/superpowers/specs/2026-10-25-channel-visibility-design.md` (section 5: record Eric's ruling on the capability name when given)
- Verify: every file named in spec section 7 was touched by Tasks 1 to 5 (`git log --stat` since Task 1's commit); `rg -n 'Decision 94' docs internal` shows only historical statements with a "superseded" marker or the ADR record.

- [ ] **Step 1: Edit the records** as listed; check every factual sentence against the code as committed by Tasks 1 to 4 (Definition of Done E), not against this plan.
- [ ] **Step 2: Run the full gate and the exit scripts**

Run: `make verify && go test -count=1 -p 1 -v ./internal/conformance/bus/ > /tmp/ab2-m3.log 2>&1 && bash scripts/m3-exit-counts.sh /tmp/ab2-m3.log && go test -count=1 -v ./internal/e2e/ > /tmp/ab2-e2e.log 2>&1 && bash scripts/m6-exit-checks.sh /tmp/ab2-e2e.log`
Expected: PASS; `matrix_*=52`; the e2e counts unchanged (13 steps, 11 cells).

- [ ] **Step 3: Commit**

```bash
git add docs/adr/0008-channel-access-control-lists.md docs/adr/0009-scopes-and-slack-bridge.md docs/superpowers/specs/2026-10-19-agentbus2-design.md docs/superpowers/plans/2026-10-21-milestone-8-tui.md docs/ops/slack-bridge.md docs/superpowers/specs/2026-10-25-channel-visibility-design.md
git commit -m "docs(adr,spec): record Eric's channel visibility decisions of 2026-10-25; Decision 94 superseded"
```

- [ ] **Step 4: Report** to Eric: the `make verify` output, the three exit-script lines, and the one pending item (the discovery capability name `channel_capabilities`).

## Constraint from the access-check plan (Task 2)

`channels.Service.Unsubscribe` calls `Access.Require(p, Channel{ID}, List)` as
a scope-only gate (Decision 95: unsubscribing needs no permission). When this
rebuild gives `List` its stored-row meaning, Unsubscribe must keep passing for
an identity with no stored row on the channel (scoped in-scope, and unscoped):
add a scope-only action or keep `List` true in that case.
`TestUnsubscribeNeedsNoRow` (`internal/conformance/bus/accesschannels_test.go`)
pins it.
