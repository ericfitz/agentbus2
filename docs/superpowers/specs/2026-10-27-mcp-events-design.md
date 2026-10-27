# MCP events (server-initiated notifications): design brief

## 1. Status

- Draft of 2026-10-27, written by an agent.
- Nothing in it is a decision until Eric marks it so.
- Issue: #13, "feat: support MCP events (server-initiated notifications)".
- **Eric's decision 2026-10-27**: Decision 0 is **B**, an ADR only; implementation is deferred. The other decisions stay open and are moot until the issue is revisited. The ADR is not written yet.
- **Eric's decision 2026-10-27**: "Protocol version will be 1.0.0, end of discussion." Any addition in this brief ships in protocol 1.0.0, and `info.version` and `version.Protocol` stay `1.0.0` before the first release.

## 2. Problem and scope

Issue #13 asks that `agentbus2 mcp` push bus changes (new messages, task updates, health changes) to MCP clients that support it, instead of relying only on `receive` plus the background `agentbus2 wait` idle wake (gate-0 decision 17).

Acceptance criteria, restated:

1. Choose the MCP mechanism or mechanisms and the bus events that map to them, and record the choice in the spec or an ADR.
2. `agentbus2 mcp` emits the chosen notifications for subscribed channels when the client supports them, and behaves exactly as today when it does not.
3. Notifications respect the existing filters:
   - no memory channels for agents (gate-0 decision 20);
   - no own messages unless requested;
   - nothing while a session is parked (gate-0 decision 18).
4. Tests cover emission, the capability fallback and the filters.
5. The docs say which harnesses act on the notifications, and how this interacts with the idle wake (decision 17).

**Proposed scope cuts (Eric to confirm, Decision 8 below):**

- **Task updates need no separate event.** Task lists are channels whose changes arrive as messages, and "Task lists stay pushed" (gate-0 decision 20, `docs/superpowers/specs/2026-10-21-gate-0-decisions.md:249`). The message event covers them.
- **Health changes are out of scope.** `Watch` and `Peek` never report health, and health is a pull operation (`GET /v1/health`, ADR 0010). An event for it would need a new source of change.
- **The event is a doorbell.** It never carries message content, consistent with Decision 10 (spec 3.5): "The wake only says that something arrived; the client then calls `receive`."

**The central finding.** Exactly one mechanism wakes an idle agent in any target harness today: Claude Code's channel extension, `notifications/claude/channel` (section 3.3).

- That mechanism has preconditions the server cannot observe.
- The standard MCP notifications are dropped or only logged by every target harness.
- So events can replace the background `wait` only on Claude Code, and only when the operator opts in. Everywhere else they are no help, and the `wait` stays.

Acceptance criterion 2's "when the client declares support" cannot be met literally for the one mechanism that works, because Claude Code declares nothing (section 3.3). This brief proposes "when the operator opts in" in its place (Decision 3).

## 3. What exists today

### 3.1 The wake path in agentbus2

**`Bus.Watch`: one waiter per session.**

- `Watch` registers a fresh watch id, which replaces any older watch on the session (`internal/core/delivery/watch.go:14-21`, `RegisterWatch` at `:45`). It wakes older in-process watchers so they see the change (`:51`).
- A replaced watcher gets `replaced` (`:108`), which is exit 3 for `wait`.
- This is Decision 68 (spec 3.5, `docs/superpowers/specs/2026-10-19-agentbus2-design.md:316-318`): "the newer watch marks the older one `replaced`".
- An armed watch renews the session lease (spec 3.5; `RegisterWatch` "sets watch_id and renews the lease", `internal/store/sessions.go:38-39`). The default lease is 90 s and the watch cap is 50 s (`internal/core/config_test.go:13`).

**`Bus.Peek`: read-only.** Peek "writes nothing: no lease renewal, no cursor, no watch row, no receipt" (`internal/core/delivery/peek_counts.go:14-20`). It answers `session_lapsed` for a session whose lease ran out (`:38-39`). Its result has a count and per-channel counts but no sequence number (`internal/bus/sessions.go:92-110`).

**The wire.**

- `GET /v1/sessions/{session}/watch` takes `after`, `timeout` and `include_own` (`api/openapi.yaml:155-198`).
- `GET /v1/sessions/{session}/peek` takes `include_own` (`api/openapi.yaml:199-215`).
- The protocol version is 1.0.0 (`internal/version/version.go:12`, `api/openapi.yaml:4`).

**Memory channels (decision 20).** `Watch` and `Peek` both go through `load`, which takes the readable set from `access.Deliver` (`internal/core/delivery/receive.go:46-56`). An agent principal is therefore never woken for a memory channel, on the server and on both transports. Any event built on `Watch` or `Peek` inherits this.

**Own messages.** `include_own` false excludes the *session's* own messages (`ExcludeSessionID`, `internal/core/delivery/service.go:58-63`), not the identity's. A subagent's message, sent from a hinted session of the same identity, still wakes the primary session.

**`agentbus2 wait`** (`internal/cli/wait.go`).

- `waitOnce` arms `Watch` in a loop (`:35-72`).
- A wake while parked advances the floor and re-arms silently (`:57-59`).
- The parked test reads the state file on every wake (`newWakeFilter`, `:157-169`).

**`agentbus2 watch`** re-arms at once instead of sleeping out its 5 s gap, because a sleeping watcher that re-armed would replace a newer `wait` or `receive wait_seconds` (`internal/cli/watch.go:34-44`).

**The Stop hook.**

- It stays silent for a parked session (`internal/cli/stophook.go:107`).
- It counts through `Peek` (`:125`).
- Outside Codex, its text tells the agent to make sure a background `agentbus2 wait` is running, and says it "replaces any running wait or watch" (`:152-156`).

**`receive wait_seconds`** also replaces an armed `wait` or `watch` (`internal/mcpserver/inputs.go:106`).

### 3.2 The MCP server

**Construction.**

- `NewServer` builds an `mcp.Server` with only `Instructions` set (`internal/mcpserver/server.go:75`). That means the SDK's default capabilities apply, including `{"logging":{}}`, which agentbus2 never uses.
- It adds the prompt, the middleware (`:80`) and 25 tools.
- `Run` serves `&mcp.StdioTransport{}` (`internal/mcpserver/run.go:73`).

**Agent-facing text.** `RegisterSteps` and `Instructions` (`server.go:88-97`) tell every non-Codex agent to run `agentbus2 wait` with `run_in_background`.

**Sessions.**

- The server holds a registry of sessions (`server.smu`, `files`, `havePrimary`; `server.go:44-46`).
- A hintless `register` is the harness's primary session. A hinted one is a subagent or second session (`server.go:766-879`).
- `register` records the client's `ClientInfo` name and version in the state file (`:780-782`, `:846-848`).

**Park (decision 18).** `unparkOnToolCall` unparks on every tool call except `receive` and `register` (`internal/mcpserver/park.go:17-27`). `register` clears the park itself (`server.go:842`).

**The session state file** (`internal/sessionstate/state.go:30-45`) holds the harness reference, `harness_client`, `primary` and `parked`. It has no field for an event mode.

**Go SDK.** `github.com/modelcontextprotocol/go-sdk v1.7.0` (`go.mod:20`). Its latest protocol is 2026-07-28; it also speaks 2025-11-25, 2025-06-18, 2025-03-26 and 2024-11-05 (`mcp/shared.go:44-64` in the module cache).

### 3.3 MCP mechanisms, the SDK, and what each harness does with them

| Mechanism | Spec status (2026-07-28) | Go SDK v1.7.0 | Claude Code | Codex | Grok Build |
| --- | --- | --- | --- | --- | --- |
| Resources plus `notifications/resources/updated` | `resources/subscribe` replaced by `subscriptions/listen` (SEP-2575) | `Server.ResourceUpdated` serves both protocols (`mcp/server.go:1114-1140`); `SubscribeHandler` (`:104-107`) | Silently dropped ([issue 47823](https://github.com/anthropics/claude-code/issues/47823), closed as not planned 2026-05-24) | `info!` log only | Not documented |
| `notifications/message` (logging) | Deprecated (SEP-2577); request-scoped; MUST NOT be sent without a per-request log level | `ServerSession.Log`, deprecated (`mcp/server.go:1720-1745`) | Not documented as reaching the model | Logged at the matching level | Not documented |
| `notifications/progress` | Request-scoped, needs a progress token from a call in flight | `ServerSession.NotifyProgress` (`mcp/server.go:1451`) | Does not extend the tool timeout; resets the idle window ([MCP docs](https://code.claude.com/docs/en/mcp)) | `info!` log only | Not documented |
| Elicitation, multi round-trip | Server-initiated requests replaced by in-request `InputRequiredResult` (SEP-2322) | `ServerSession.Elicit`, MRTR (`mcp/mrtr.go`) | A dialog for the human, interactive sessions only ([MCP docs](https://code.claude.com/docs/en/mcp)) | Forwarded as an elicitation | Not documented |
| `list_changed` | Moves to `subscriptions/listen` | Automatic | Refetches the lists; no turn | `info!` log only | Not documented |
| `notifications/claude/channel` (vendor extension) | Not MCP core | No public send path (below) | **Injects a `<channel>` event into the running session and starts a turn** ([channels reference](https://code.claude.com/docs/en/channels-reference)) | Not applicable | Not applicable |

Sources:

- Spec: the [2026-07-28 changelog](https://modelcontextprotocol.io/specification/2026-07-28/changelog) and the [logging page](https://modelcontextprotocol.io/specification/2026-07-28/server/utilities/logging).
- Codex: `codex-rs/rmcp-client/src/logging_client_handler.rs` at [de8fab6](https://github.com/openai/codex/blob/de8fab6d7adfcef8b4ce6f02f3b5c8be4092015a/codex-rs/rmcp-client/src/logging_client_handler.rs), last changed 2026-10-10. Every `on_*` notification handler is a tracing call.
- Grok Build: [MCP servers page](https://docs.x.ai/build/features/mcp-servers), which documents tools and transports only.

**Claude Code channels: the facts that matter here.**

- **Research preview.** Custom channels are not on the allowlist. A local server needs `claude --dangerously-load-development-channels server:agentbus2`, which shows a confirmation dialog at every start. The other route is an organization's `allowedChannelPlugins`, which requires packaging agentbus2 as a plugin.
- **Interactive only.** The flag is ignored with `-p` and in the Agent SDK.
- **Auth and policy.** It needs claude.ai or Console authentication; it is not available on Bedrock, Vertex or Foundry. `channelsEnabled` gates Team and Enterprise organizations. ([channels](https://code.claude.com/docs/en/channels))
- **No acknowledgement.** "If the session hasn't loaded your server as a channel, or the organization policy blocks it, Claude Code drops the events silently and returns no error to your server." Events that arrive while Claude is busy are delivered together on the next turn. ([channels reference](https://code.claude.com/docs/en/channels-reference))
- **Protocol revision.** On the v2 MCP client runtime, "a channel server that negotiates MCP protocol revision 2026-07-28 can't deliver channel messages, so Claude Code doesn't register it as a channel". `MCP_PROTOCOL_NEGOTIATION=legacy` keeps every server on the earlier handshake. ([MCP docs](https://code.claude.com/docs/en/mcp))
- **Capabilities.** The server declares `capabilities.experimental["claude/channel"] = {}`. Params are `content` (string) and `meta` (string map). Meta keys are letters, digits and underscores; other keys are silently dropped. The documentation names no client capability that would tell the server it was registered as a channel.

**What the Go SDK allows.**

- **Declaring the capability:** yes. `ServerOptions.Capabilities` (`mcp/server.go:137`) with `ServerCapabilities.Experimental` (`mcp/protocol.go:2269-2271`). Setting `Capabilities` replaces the default `{"logging":{}}`.
- **Sending `notifications/claude/channel`: not through the public API.** `defaultSendingMethodHandler` returns `ErrNotHandled` for a method missing from its method table (`mcp/shared.go:119-124`), and the session's connection is unexported (`mcp/server.go:1849`).
- **The route that works:** a wrapping `mcp.Transport` whose `Connection` (`Read`, `Write`, `Close`, `SessionID`; `mcp/transport.go:65-86`) delegates to the stdio connection. Our code then calls `Write(ctx, &jsonrpc.Request{Method: "notifications/claude/channel", Params: raw})` with a zero ID, which is a JSON-RPC notification. The `go-sdk/jsonrpc` package exports `Request` for transport authors (`jsonrpc/jsonrpc.go:11-21`). `Write` must be safe for concurrent use (`transport.go:72-76`); the io connection serializes on `writeMu` (`:421`, `:681`).
- **Capping the protocol version:** a transport that implements `ProtocolVersionSupporter` (`mcp/transport.go:58-62`) filters the versions that `server/discover` advertises (`mcp/server.go:884-926`). The SDK's own client then falls back to the legacy `initialize` at 2025-11-25 (`mcp/client.go:318-376`, `:437-445`).
  - Unverified: that Claude Code's TypeScript SDK 2.0 client falls back the same way.
  - The server still accepts a request that names 2026-07-28 in `_meta` (`mcp/server.go:1866-1877`). The cap only steers negotiation; it does not refuse.

## 4. Options

### Question 1: the event source, given Decision 68 (the consequential one)

Any event needs a loop in `agentbus2 mcp` that learns "something arrived for this session". The obvious source, `Bus.Watch`, replaces the session's `wait` (section 3.1). The Stop hook then tells the agent to start another `wait`, which replaces the server's watch. Each wake costs a model turn, which is what decision 17 set out to cut.

**Option A: the MCP server owns the session's watch.** The server arms `Bus.Watch` for its primary session. In event mode, the agent-facing text tells the agent not to start `wait`.

- Pros: no wire change. The lease is renewed by the armed watch. The core is untouched.
- Cons:
  - `receive wait_seconds` and any manual `wait` still replace it. After `replaced`, Decision 68 says the old watcher must not retry, so the server either breaks that rule or stops emitting.
  - If the harness drops the events silently (section 3.3), the agent is never woken. Nothing tells the server, and the `wait` it would otherwise run was suppressed.
  - It changes the meaning of decision 17 for Claude Code.

**Option B: a non-replacing observer watch (Recommended).** Add `WatchInput.Observe bool` and an `observe` query parameter on the watch operation. An observer:

- does not write `watch_id`, so it neither replaces nor is replaced, and `check` skips the watch-id comparison (`watch.go:107-108`);
- renews the lease through the existing `Store.RenewLease` (`internal/store/sessions.go:32`) instead of `RegisterWatch`;
- otherwise runs the same peek, the merge window and the notifier wait.

Pros:

- No conflict with `wait`, `watch` or `receive wait_seconds`.
- It has the sequence floor, which `Peek` lacks, so no wake is lost or repeated.
- Hint-driven and immediate on both engines and both transports. Replicas already wake through `pgnotify` (`TestReplicasWake`).
- Memory and own-message filtering are inherited.

Cons:

- A Bus, wire and core change: the addition ships in protocol 1.0.0 with no version bump (Eric's decision 2026-10-27), `make generate`, and conformance cases on both transports.
- It amends Decision 68, from "one watcher per session" to "one replacing watcher plus any number of observers". **That is an architectural change and needs Eric's approval.**

**Option C: poll `Peek` on an interval.** The server calls `Peek` every few seconds and emits when the count grows.

- Pros: no wire, Bus or Store change, and no conflict with `wait`.
- Cons:
  - Latency is the poll interval. Each poll is an HTTP round trip on a server profile, or a SQLite read locally.
  - `Peek` has no sequence number, so "receive 3, then 3 more arrive" between two polls is invisible unless `PeekResult` gains a max-seq field, which is itself a wire addition.
  - `Peek` does not renew the lease. With no `wait` running (Option A's mode) the session lapses after 90 s and every poll answers `session_lapsed`.

**Option D: subscribe to the notifier directly.**

- Rejected. `internal/mcpserver` would need `internal/core/notify`, which bypasses the Bus seam. It works only for a local profile, where the notifier is in-process; it is blind on a server profile.

### Question 2: which MCP mechanism

**Option A: `notifications/claude/channel` only, opt-in (Recommended).** It is the only mechanism that wakes an idle agent in any target harness (section 3.3).

- Needs the wrapping transport and the experimental capability.
- Claude Code only, behind the preview flag.

**Option B: standard resources plus `notifications/resources/updated` only.**

- A resource template such as `agentbus2://sessions/{session}/pending`, whose read returns the `Peek` result. The SDK serves both `resources/subscribe` and `subscriptions/listen`.
- Spec-standard and testable with the stock SDK client.
- But no target harness acts on it: Claude Code drops it, Codex logs it, and Grok Build is unknown. It meets the issue's letter without moving any agent.

**Option C: both.** Option A for Claude Code, Option B as the standard path for future clients. More code and tests for a path nobody consumes today.

**Not recommended for any option:**

- logging, which is deprecated (SEP-2577) and request-scoped on 2026-07-28;
- progress, which is request-scoped and only during a call in flight;
- elicitation, which targets the human and is request-scoped under MRTR.

### Question 3: the capability fallback

The fallback has two layers.

**Layer 1: the protocol version, which the server controls.** Claude Code's v2 runtime refuses a channel server that negotiates 2026-07-28.

- **Option A (Recommended):** when channel events are on, the wrapping transport implements `ProtocolVersionSupporter` and advertises 2025-11-25 and older only.
- **Option B:** always cap.
- **Option C:** never cap, and document `MCP_PROTOCOL_NEGOTIATION=legacy`. That variable moves every MCP server of the harness to the legacy handshake, not just agentbus2.

**Layer 2: the harness opt-in, which the server cannot see.** Claude Code acknowledges nothing (section 3.3).

- **Option A (Recommended):** explicit operator opt-in. The server emits only when started with an events flag. The flag is written into the harness's MCP entry by `init`, and the agent-facing text changes with it.
- **Option B:** infer from `clientInfo.name == "claude-code"` and emit always. Events dropped by a session without `--channels` cost nothing. But that only works as a *supplement*: the `wait` must stay (Question 4, Option A).
- **Option C:** inspect the harness's command line for `--channels` or `--dangerously-load-development-channels` through `procs`.
  - Detects the opt-in on macOS and Linux.
  - Fragile: the flag syntax is in preview and "may change". It would need Windows support in `procs`, and it widens what the MCP server reads about its parent process.

### Question 4: replace or supplement the background `wait`

**Option A: supplement.** The `wait` keeps running and channel events are extra. Two wakes per arrival: the event's turn, then the `wait` exit's turn (or the reverse). That undoes decision 17's turn saving.

**Option B: replace, when the operator opts in (Recommended, with Question 3 Layer 2 Option A).**

- In event mode, `Instructions`, `RegisterSteps`, the Stop hook tail (`stophook.go:152-156`) and the skill say not to start `wait`.
- The state file gains `events` (for example `"claude-channel"`) so the Stop hook, which reads the state file (`stophook.go:103-110`), can pick its text.
- The risk is a session started without the Claude Code flag: it gets no idle wake at all. Mitigations:
  - `init` prints the exact `claude` command line;
  - the Instructions tell the agent that a session without a `<channel source="agentbus2">` line after its first `register` should fall back to the `wait`.
- This **changes gate-0 decision 17 for Claude Code**, from "the idle wake is a background `agentbus2 wait`" to "in event mode, a channel event". That needs Eric's approval.

**Option C: replace automatically** whenever the client is Claude Code. Unsafe: a silently dropped event means a dead session.

### Question 5: the event payload

**Option A: a doorbell (Recommended).** Decision 10.

- `content`: for example `Agentbus2: 3 new message(s) for <display name> on <channels>. Call receive (ack the previous batch) and handle what concerns you.` This is the Stop hook's head (`stophook.go:149-150`).
- `meta`: `{session, seq, count}` (identifier keys only).

**Option B: message content in the event** (sender, subject, body).

- It creates a second delivery path that bypasses ack and redelivery (Decision 16), and duplicates `receive`.
- It also puts sender-controlled text into the session outside the `sender_kind` framing that `receive` gives (`server.go:593`).

### Question 6: which sessions emit

**Option A: the server's primary session only (Recommended).**

- An event for a hinted session would land in the main conversation, because Claude Code subagents share the parent's MCP connection. That is not verified against Claude Code's documentation. It is the standing assumption of the hinted-register design: one MCP server per harness holds both the primary and the hinted sessions (`server.go:766-879`), and the SubagentStart hook only suggests a hint (`internal/cli/subagenthook.go:16-27`).
- Gate-0 decision 19 already tells subagents not to start a `wait`.

**Option B: every session the server holds.** That confuses the main conversation with a subagent's traffic.

### Question 7: own messages

**Option A: an `-include-own` flag on `agentbus2 mcp`, default false (Recommended).** It mirrors `wait -include-own` (`wait.go:85`) and maps to `WatchInput.IncludeOwn`. "Own" stays per session (section 3.1), so a subagent's messages still ring the main session.

**Option B: a per-`register` argument.** That is a tool-schema change for a setting the operator, not the agent, should own.

### Parked sessions (no option needed)

- On every wake, before emitting, the server reads the state file as `newWakeFilter` does (`wait.go:157-169`).
- A parked wake advances the observer's floor and emits nothing.
- After an unpark (a user prompt, or a tool call other than `receive`: `park.go:17-27`) the agent is active again. Decision 18's "the next receive catches up on everything" holds, and no catch-up event is needed.

## 5. Decisions for Eric

Decision 0 comes first because it gates the rest: with answer B, only Decisions 4, 8 and 10 need an answer, for the ADR.

0. **Go or no-go. DECIDED: B (Eric's decision 2026-10-27): an ADR only; implementation deferred.** The mechanism is a research preview, Claude Code only, and gated by a flag.
   - A: build it now.
   - B: record the research in the ADR and defer the build until channels leave the preview or the allowlist admits local servers.

   Recommendation: **A** if Eric runs Claude Code with the development flag day to day; otherwise **B**, which also satisfies the issue's first and last acceptance criteria.
1. **Event source under Decision 68.**
   - A: the MCP server owns the session's watch.
   - B: a non-replacing observer watch, which amends Decision 68; it ships in protocol 1.0.0 with no version bump (Eric's decision 2026-10-27).
   - C: poll `Peek`.

   Recommendation: **B**. It is the only option that neither fights `wait` and `receive wait_seconds` nor loses wakes, and it keeps the lease alive when `wait` is not running.
2. **Replace or supplement `wait` on Claude Code.**
   - A: supplement (double wakes).
   - B: replace, but only in operator-enabled event mode; this changes decision 17 for Claude Code.
   - C: replace automatically.

   Recommendation: **B**.
3. **How events are enabled** (the fallback's second layer).
   - A: an explicit `agentbus2 mcp -events claude-channel` flag that `init` writes on request, and this replaces the issue's "when the client declares support".
   - B: infer from `clientInfo`.
   - C: inspect the harness's command line.

   Recommendation: **A**.
4. **Mechanism.**
   - A: `notifications/claude/channel` only.
   - B: standard `resources/updated` only.
   - C: both.

   Recommendation: **A**. Record in the ADR that resources, logging, progress and elicitation were examined and rejected, with the harness table.
5. **Protocol version cap.**
   - A: cap at 2025-11-25 in event mode only.
   - B: always cap.
   - C: never cap; document `MCP_PROTOCOL_NEGOTIATION=legacy`.

   Recommendation: **A**.
6. **Payload.**
   - A: doorbell (counts, channels, "call receive").
   - B: message content.

   Recommendation: **A** (Decision 10).
7. **Emitting sessions.**
   - A: primary only.
   - B: all held sessions.

   Recommendation: **A**.
8. **Scope.**
   - A: messages only (tasks covered as messages, health out of scope).
   - B: also a health event.

   Recommendation: **A**.
9. **Own messages.**
   - A: `-include-own` on `agentbus2 mcp`, default false.
   - B: a `register` argument.

   Recommendation: **A**.
10. **Where to record it.**
    - A: a new ADR 0012 "MCP events", with addenda to gate-0 decision 17 and to spec 3.5 (Decision 68).
    - B: a spec section only.

    Recommendation: **A**.

## 6. Dependencies

- **Go SDK `github.com/modelcontextprotocol/go-sdk` v1.7.0** (`go.mod:20`). The plan relies on `ServerOptions.Capabilities.Experimental`, `ProtocolVersionSupporter`, the exported `jsonrpc.Request` and `Connection.Write` concurrency. A later SDK that exposes a generic notify would let the transport wrapper go; check at each bump. The SDK's MCPGODEBUG flags added in 1.7.0 are removed in 1.9.0 (`docs/mcpgodebug.md` in the module); none is needed here.
- **Claude Code** with the channels research preview: `--dangerously-load-development-channels server:agentbus2`, claude.ai or Console auth, interactive mode only, and legacy protocol negotiation on the v2 runtime. The contract "may change based on feedback" ([channels](https://code.claude.com/docs/en/channels)).
- **Unverified, needs a manual probe before the build:**
  - that Claude Code's v2 runtime falls back to legacy when `server/discover` omits 2026-07-28;
  - whether Claude Code puts anything channel-related in its client capabilities.

  Proposed probe: a temporary log line in `agentbus2 mcp` with the negotiated protocol version and `ClientCapabilities` at `initialize`, run under Claude Code, Codex and Grok Build. Eric runs it, after the harness self-test (Definition of Done C).
- **Grok Build:** its MCP client's handling of notifications is undocumented. The same probe plus one `resources/updated` emission settles it.
- **Issues:** none of the open issues blocks this. Issue #6 (key rotation) and issue #8 (key provider) are unrelated.
- **CI:** no new job. The observer cases run in the existing bus suite on both engines and both transports (`make test-conformance`). The MCP tests are in `make test` and run on `windows.yml`.

## 7. Implementation outline (provisional; assumes recommendations 1B, 2B, 3A, 4A, 5A, 6A, 7A, 9A)

**Task 1: observer watch in the core and Bus (TDD).**

- Files:
  - `internal/bus/sessions.go`: `WatchInput.Observe`.
  - `internal/core/delivery/watch.go`: for an observer, `RenewLease` instead of `RegisterWatch`, and no watch-id check.
  - `internal/bus/local`.
- Tests in `internal/conformance/bus`:
  - an observer wakes like a watch;
  - an observer and a watch on one session both wake and neither is `replaced`;
  - a newer watch still replaces an older watch with an observer present;
  - the observer renews the lease;
  - memory channels are not delivered to an agent observer (Deliver);
  - `include_own` false excludes the session's own messages.
- Gates: `make verify`; `scripts/m3-exit-counts.sh` if the counts change (update the expected counts deliberately).

**Task 2: wire.**

- Files:
  - `api/openapi.yaml`: an `observe` boolean query parameter on `watch`; `info.version` stays `1.0.0` (Eric's decision 2026-10-27);
  - `internal/version/version.go`: `Protocol` stays `"1.0.0"` (Eric's decision 2026-10-27);
  - `make generate`;
  - `internal/server` (watch handler);
  - `internal/bus/remote`.
- Tests: the bus suite under `AGENTBUS2_BUS_TRANSPORT=inproc`; a health and discover `protocol_version` test.
- Gates: `make verify`, `scripts/m4-exit-checks.sh` (the Decision 47 import rule is unchanged).

**Task 3: the event transport in `internal/mcpserver`.**

- New file `events.go`:
  - a `Transport` wrapper whose `Connection` delegates to the inner one, exposes `notify(ctx, method, params)`, and implements `ProtocolVersionSupporter` (versions up to 2025-11-25);
  - the experimental capability, set through `ServerOptions.Capabilities` with tools and prompts kept;
  - drop the unused `logging` default at the same time, or keep it explicitly: an agent choice to confirm.
- New import edge: `internal/mcpserver` imports `github.com/modelcontextprotocol/go-sdk/jsonrpc`. That is allowed: Milestone 6 forbids only `internal/gen` and `internal/store` (`scripts/m6-exit-checks.sh`).
- Tests (TDD, in-memory transport; section 8).

**Task 4: the emitter loop.**

- Files: `events.go`, `server.go`, `sessions.go`, `run.go`.
- Behavior:
  - On a primary `register`, start one observer loop for that session.
  - Stop it on `close_session`, on server close, and when the primary changes (`/clear` adopts the same session, so no change there).
  - On each wake: read the state file, and drop the wake if parked (the floor still advances). Apply the 5 s minimum gap and merge, as `watchLoop` does (`watch.go:45-77`). Then send the doorbell.
  - On `session_lapsed`, resume once through `resumeLapsed` (`sessions.go:120`).
  - Flags on `agentbus2 mcp`: `-events claude-channel` (anything else is exit 2, and a test covers that) and `-include-own`.
- Tests: section 8.

**Task 5: event mode in the state file and the agent texts.**

- Files:
  - `internal/sessionstate/state.go`: `events` field, changed in place, no migration (pre-release rule);
  - `internal/cli/stophook.go`: no `wait` sentence in event mode;
  - `server.go`: `Instructions` and `RegisterSteps` in event mode;
  - `internal/cli/skills/using-agentbus2/SKILL.md`;
  - `internal/cli/install.go` and `init.go`: `init -global -events claude-channel` writes the flag into the Claude Code MCP entry and prints the `claude --dangerously-load-development-channels server:agentbus2` line. It refuses `-events` for Codex and Grok with exit 2.
- Gates: `scripts/m6-exit-checks.sh` (the word check covers `server.go`, `inputs.go`, `stophook.go`, `subagenthook.go` and the skill).

**Task 6: docs and decisions.**

- New ADR `docs/adr/0012-mcp-events.md`.
- Addenda to gate-0 decision 17 (`docs/superpowers/specs/2026-10-21-gate-0-decisions.md:224-231`) and to spec 3.5 (Decision 68).
- `docs/adr/README.md`, `README.md`, `docs/install.md` (event mode setup), and CLAUDE.md's `mcp` and `init` bullets.

**Gates for the whole change:** `make verify` (both engines; the bus suite on both transports; `check-gates`), with `ci.yml` and `windows.yml` green on the pushed commit, and `scripts/m6-exit-checks.sh`.

## 8. Risks and test strategy

**Testing with the SDK's in-memory transport.** `mcp.NewInMemoryTransports` is a `net.Pipe` pair (`mcp/transport.go:160-163`). It is already used by `startServer` (`internal/mcpserver/server_test.go:144-170`).

- Server side: wrap the server transport with the event transport, exactly as `Run` will wrap stdio, so tests and production share the code path.
- Client side: a recording client `Transport` wraps the in-memory one. Its `Read` *consumes* every inbound `jsonrpc.Request` whose method is `notifications/claude/channel`: it records it and reads on, and never returns it to the SDK client. So the client's unknown-method path (`handleReceive` to `checkRequest`, `mcp/shared.go:186-188`) never sees it. That path's behavior for an unknown notification was not checked, and the tests do not depend on it.
- The protocol path: `ClientSessionOptions.protocolVersion` is unexported (`mcp/client.go:249-253`), so a test cannot force the client's version. But the server-side cap makes the stock client fall back to 2025-11-25 (`client.go:437-445`). Assert the negotiated version through `cs.InitializeResult().ProtocolVersion`, and assert 2026-07-28 when events are off.

Test cases:

1. **Emission:** a message from another session produces exactly one doorbell with the right count and channels. A burst within the merge window produces one.
2. **Capability fallback:**
   - without `-events`, no experimental capability, the protocol is uncapped and nothing is emitted (today's behavior, byte for byte in `tools/list`);
   - with `-events`, the capability is present and the version is capped;
   - an unknown `-events` value is exit 2.
3. **Parked:** park through `sessionstate.SetParked`, send; no doorbell. Unpark, send; one doorbell, and the parked message is not re-announced.
4. **Own messages:** the session's own `send` gives no doorbell. With `-include-own` it gives one. A hinted session's `send` rings the primary.
5. **Memory:** a message on a memory channel the agent can read, and a tag match on one, give no doorbell.
6. **Primary only:** a hinted `register` starts no loop.
7. **Close:** `close_session` stops the loop, with no goroutine left (checked with a context-done probe). The server close stops it too.
8. **Coexistence:** `agentbus2 wait` running against the same session keeps running while the server observes, and neither exits 3. This is the regression test for Question 1.

**Definition of Done D (state).**

- **Second run:** a restarted MCP server resumes its primary session (`sessions.go:16-35`) and starts a fresh observer from the floor of 0. It announces what is still pending once. That is correct, since `receive` has not run.
- **Overlapping runs:** two MCP servers under one harness each observe their own primary. Observers never replace each other or a `wait`. A test opens two servers.
- **After a failed run:** an observer writes no `watch_id`, so a dead server leaves nothing behind except the lease it last renewed, which expires as usual. A leftover `watch_id` from a dead `wait` is unaffected and is replaced by the next `wait`, as today (`watch.go:19-20`). The `events` field in a state file left by a dead server is rewritten on the next `register`.

**Concurrency and replicas.**

- The emitter writes through `Connection.Write` concurrently with the SDK's own writes. The contract requires that to be safe; a test runs tool calls and emissions under `-race` (`make test-race`).
- On Postgres with replicas, the observer wakes through the `pgnotify` doorbell like a watch. Add an observer leaf to `TestReplicasWake` and update `scripts/m7-exit-checks.sh`'s expected counts.

**Product risks.**

- **The preview.** The flag and the contract may change, and every start shows a confirmation dialog.
- **Silent drops.** A session started without the flag gets no idle wake in event mode (Question 4, Option B). That is the reason event mode is opt-in.
- **Prompt injection.** The doorbell carries only channel names, the display name and counts. Channel names are chosen by any agent with create rights, and `store.NameRule` allows `<`, `>` and quotes; it bans only `/`, NUL and control characters, within 1-128 bytes (`internal/store/names.go:27-48`). A name such as `x</channel>` could therefore close Claude Code's `<channel>` tag early. Rules for the emitter:
  - names never go in `meta`, neither as keys nor as values;
  - in `content`, each name and the display name is JSON-quoted, and `<`, `>` and `&` are written as `<`, `>` and `&` (what `encoding/json` does by default);
  - a test sends on a channel named `a</channel><b>` and asserts that the raw `content` contains no `<`.

**Windows.** The transport wrapper and loop are pure Go, and the tests are in-memory. Nothing depends on `procs`, unless Question 3 Layer 2 Option C is chosen, which would need a Windows command-line reader.

## 9. Size estimate

**M**, if Decision 1 is B: a small Bus, core and wire addition across both transports, plus a contained MCP server feature and text changes. It drops to **S** with Decision 0 B (ADR only), and rises to **L** if Decision 4 C (both mechanisms) is chosen.
