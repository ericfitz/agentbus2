# ADR 0016: MCP events (server-initiated notifications)

Status: Accepted (Eric: decision 1 on 2026-10-27, decisions 2 to 4 on 2026-11-03). **The build is deferred**: this record holds the research and the decisions made so far. The other questions of the design brief stay open (see "Open questions"); none of them is decided here, and the brief's recommendations are agent proposals, not decisions.

Covers issue #13 (ericfitz/agentbus2#13), acceptance criteria 1 (choose the mechanism and the events, and record them) and 5 (which harnesses act on notifications, and how that interacts with the idle wake). Criteria 2 to 4 (emit, fall back, filter, test) wait for the build. The design brief is `docs/superpowers/specs/2026-10-27-mcp-events-design.md`. Section 3.3 holds the harness research, section 4 the options and section 5 the decision list. The brief numbers its decisions 0 to 10, and this record's decisions map onto them as follows: 1 is the brief's 0, 2 is 4, 3 is 8 and 4 is 10.

Changes no earlier ADR's decision. A build would amend gate-0 decision 17 (the idle wake) and spec 3.5 Decision 68 (one watcher per session), depending on open questions 1 and 2; both places carry a pointer to this record.

## Context

Issue #13 asks that `agentbus2 mcp` push bus changes to MCP clients that support it, instead of relying only on `receive` and the background `agentbus2 wait` idle wake (gate-0 decision 17). The brief (research of 2026-10-27, against Go SDK `github.com/modelcontextprotocol/go-sdk` v1.7.0 and the MCP specification of 2026-07-28) found:

- **Only one mechanism wakes an idle agent in any target harness today**: Claude Code's channel extension, `notifications/claude/channel`. It starts a turn in the running session. It is a research preview: a local server needs `claude --dangerously-load-development-channels server:agentbus2`, it works in interactive sessions only, Claude Code acknowledges nothing, and events are dropped silently when the session did not load the server as a channel. On Claude Code's v2 MCP client runtime, a channel server that negotiates protocol revision 2026-07-28 is not registered as a channel.
- **The standard notifications move no agent.** Claude Code silently drops `notifications/resources/updated`, and Codex only logs it. Grok Build's handling is undocumented.
- **Logging, progress and elicitation do not fit.** `notifications/message` is deprecated (SEP-2577) and request-scoped. `notifications/progress` needs a call in flight. Elicitation targets the human. `list_changed` refetches lists and starts no turn.
- **The Go SDK has no public send path for a vendor notification.** It can declare the experimental capability, but sending `notifications/claude/channel` needs a wrapping `mcp.Transport` (brief section 3.3, "What the Go SDK allows").

The harness table with sources is in brief section 3.3. Re-verify it before the build: the channels contract "may change based on feedback".

## Decisions (Eric)

1. **Go or no-go: an ADR only; implementation deferred** (brief decision 0, option B; Eric's decision 2026-10-27). The build waits until the channel mechanism leaves the research preview or the allowlist admits local servers, or a target harness acts on the standard notifications. Not chosen: build it now (A).
2. **Mechanism: both, for the broadest compatibility, with the best experience on Claude Code** (brief decision 4, option C; Eric's decision 2026-11-03). In Eric's words: "We need to maximize compatibility with agents/harnesses. I use claude and would love to have the best experience on claude, but we need broad compatibility." A build emits `notifications/claude/channel` to Claude Code when the operator enables it, and standard resources with `notifications/resources/updated` for every client that subscribes. The resource would be a template such as `agentbus2://sessions/{session}/pending`; the SDK serves both `resources/subscribe` and `subscriptions/listen`. Logging, progress and elicitation are not used (see Context). Not chosen: the channel extension only (A), standard resources only (B).
3. **Scope: messages only** (brief decision 8, option A; Eric's decision 2026-11-03). Task updates need no separate event: task lists are channels whose changes arrive as messages, and task lists stay pushed (gate-0 decision 20). Health stays pull-only (`GET /v1/health`, ADR 0010), because `Watch` and `Peek` never report it. Not chosen: a health event as well (B).
4. **Record: this ADR, plus pointers in gate-0 decision 17 and spec 3.5** (brief decision 10, option A; Eric's decision 2026-11-03). The brief proposed the number 0012, which was taken in the meantime. Not chosen: a spec section only (B).

## Filters any build keeps

These follow from earlier decisions and need no new one:

- Agents are never woken for memory channels (gate-0 decision 20). `Watch` and `Peek` already take the readable set from `access.Deliver`, so an event built on either inherits the rule on both transports.
- Nothing is emitted for a parked session (gate-0 decision 18). The emitter reads the state file on every wake, as `agentbus2 wait` does; a parked wake advances the floor and emits nothing, and the next `receive` after an unpark catches up.
- The event is a doorbell: it says that something arrived and never carries message content (spec 3.5, Decision 10). Delivery, ack and redelivery stay with `receive` (Decision 16).
- Channel names are untrusted text. Any `content` or `meta` that names a channel must be escaped so a name such as `x</channel>` cannot close Claude Code's `<channel>` tag (brief section 8, "Prompt injection").

## Open questions (not decided)

The brief's decisions 1, 2, 3, 5, 6, 7 and 9 were moot under decision 1 and stay open until the build is planned. The brief's recommendation follows each one; none of them is a decision.

- **Event source under Decision 68** (brief 1). Recommendation: a non-replacing observer watch (`WatchInput.Observe`, an `observe` query parameter), which amends Decision 68 from "one watcher per session" to "one replacing watcher plus any number of observers". That amendment is architectural and needs Eric's approval.
- **Replace or supplement `agentbus2 wait` on Claude Code** (brief 2). Recommendation: replace only in operator-enabled event mode, which changes gate-0 decision 17 for Claude Code. Under decision 2 above, a client served only by `resources/updated` keeps its `wait` (or `receive wait_seconds`): no target harness wakes on that notification today.
- **How events are enabled** (brief 3). Recommendation: an explicit flag on `agentbus2 mcp` that `init` writes on request, replacing the issue's "when the client declares support", since Claude Code declares nothing for channels. Resource subscriptions are self-declaring: a client that calls `resources/subscribe` or `subscriptions/listen` has asked for them.
- **Protocol version cap** (brief 5). Recommendation: cap at 2025-11-25 in channel mode only.
- **Payload** (brief 6). Recommendation: a doorbell with counts and channel names. The filters above already require a doorbell, so this question only settles the text.
- **Emitting sessions** (brief 7). Recommendation: the server's primary session only.
- **Own messages** (brief 9). Recommendation: an `-include-own` flag on `agentbus2 mcp`, default false.

Also unverified, and needs a manual probe before the build: whether Claude Code's v2 runtime falls back to the legacy handshake when `server/discover` omits 2026-07-28, what Claude Code puts in its client capabilities, and how Grok Build handles `resources/updated`. The probe (brief section 6) is a temporary log line with the negotiated protocol version and `ClientCapabilities`. Eric runs it after the harness self-test.

## Consequences

- Nothing changes in the code, the wire or the agent-facing text now. The idle wake stays a background `agentbus2 wait` on every harness except Codex (gate-0 decision 17), and `agentbus2-mcponly` keeps `receive wait_seconds` (ADR 0015 decision 5).
- Choosing both mechanisms raises the brief's size estimate from M to **L** (brief section 9). The standard path adds a resource template, the subscribe handlers and tests, though no target harness consumes it today.
- Issue #13 stays open for the build. Agent status pushes (ADR 0014 decision 13) would ride the same events if Eric wants them, which is not decided here.
