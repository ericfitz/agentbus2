# ADR 0013: Write inspection

<!-- open note: write-inspection 13 -->

Status: Accepted (Eric, 2026-10-27, decisions 1 to 13 below). The "Agent choices" are settled by the agent that wrote the plan and are not Eric's decisions; each carries an `open note: write-inspection N` marker at its code site so Eric can reverse it with one findable edit.

Covers issue #5. The design is `docs/superpowers/specs/2026-10-27-write-inspection-design.md` (section 5 holds Eric's decisions); the plan is `docs/superpowers/plans/2026-10-27-write-inspection.md`; the operator guide is `docs/ops/inspection.md`; the summary is spec section 4.8 of `docs/superpowers/specs/2026-10-19-agentbus2-design.md`.

Supersedes item 5 of the ADR 0003 addendum ("The inspection hook is not ported") and Gate 0 decision 5; both texts stay as history and point here. Amends Milestone 3 rule 1 (decision 13).

## Context

v1 ran an operator-supplied command before each write (`inspect.go`, `inspection_command`) and refused the write when the command said so; a failure of the command also refused it (fail closed). Eric decided on 2026-10-21 not to port it in 2.0.0 and tracked a v2 feature, with a response mechanism, as issue #5.

v2 cannot copy v1's placement. Every content write does its checks inside `Store.Append`, and Decision 26 forbids a side effect or an external call inside the transaction function (SQLite has one writer, and Postgres retries the function). The inspector call, the request id, the limiter charge, the notice write and every log line therefore run outside any `fn`. ADR 0011 makes `internal/core/access` the one access check: inspection runs only after `access.Require` allowed the write, can only add refusals, and is never handed the administrator flag, the scope, the creator or an ACL.

## Decisions (Eric, 2026-10-27)

Each of these is Eric's decision.

1. **Responses are `allow` and `refuse`.** The sender is told through the error; operators are told through the log. There is no redact and no hold.
2. **A read-only pre-pass, the inspector, then the authoritative write with a digest check.** The write's `plan*` step runs in a `Store.Read`; the inspector runs outside any transaction; `Store.Append` runs the same step again and writes only when the record digest matches what was inspected.
3. **The transport is a webhook only.** No command adapter.
4. **`inspection_on_failure`, default `closed`.** `open` stores the write uninspected.
5. **A refusal is `inspection_rejected`, HTTP 422, not retryable. A failure reuses `unavailable`, HTTP 503, retryable.**
6. **A bridged Slack message is inspected; a refusal is a drop.** The bridge counts it dropped, as for a trust refusal.
7. **The request carries the record, the sender and its kind, the session, the channel name and kind, and the source.** It carries no ACL, administrator flag or scope.
8. **Content writes only:** `send`, `edit_memory`, `task_create` and `task_update`. Reads and administration are not inspected.
9. **No exemptions:** no identity or channel is exempt. Operations are scoped by decision 8.
10. **Operator attention is a warn log line**: a verdict that sets `notify` writes `write inspection asks for operator attention`; a failure also logs at warn.
11. **Log lines only, never the payload.**
12. **Server mode only.** A local profile is never inspected.
13. **A refused write is charged to the sender's rate limit.** This amends Milestone 3 rule 1: a write the inspector refused is charged once, after the refusal, with the pre-pass envelope size; nothing else changes.

The protocol stays `1.0.0`: the new `inspection_rejected` enum value ships in `1.0.0` (Eric, 2026-10-27: "Protocol version will be 1.0.0, end of discussion.").

## Agent choices

These are not Eric's decisions. The agent that wrote the plan settled them, and the code carries the marker named.

1. A second record mismatch answers `conflict` with `retryable: true`, built with `bus.Errorf` (`core.E` makes `conflict` non-retryable); the bridge retries a retryable `conflict` and does not count it a duplicate. Markers: `RunInspected` (`internal/core/inspect.go`), `internal/slackbridge/inbound.go`.
2. Only a `refuse` verdict charges the limiter (pre-pass envelope size); an inspector failure charges nothing in either mode. Marker: `inspect`.
3. When the caller's context ends during inspection, nothing is written in either mode; the answer is the retryable `unavailable`, with no notice and a warn line with class `canceled`. Marker: `inspect`.
4. A process starts in the inspector state `unknown`; its first successful inspection clears an `inspection` notice older than 60 s, as a failing-to-ok transition does, so a replica that died failing does not leave `degraded` behind. Marker: `noteSuccess`.
5. No per-operation `422` response in `api/openapi.yaml`: every operation declares its success status and `default`, and the remote client decodes the default problem at any status. Markers: `StatusFor` (`internal/server/problem.go`), the `Problem.code` enum (`api/openapi.yaml`).
6. `inspection_api_key_file` or `inspection_api_key_env` set without `inspection_endpoint` is refused at start. Marker: `Validate` (`internal/server/config/config.go`).
7. An `allow` verdict that carries `code` or `reason`, even an empty one, is an inspector failure. Marker: `Verdict.validate`.
8. A `task_update` is inspected only when the patch is not a delete and the patched subject, description or metadata differs from the live revision's; claim, release, status, owner and move patches never reach the inspector. Marker: `Update` (`internal/core/tasks/update.go`).
9. A task request carries no `base_revision`, and a task digest covers the channel, the task id and the merged subject, description and metadata only (never the revision or rank). Marker: `taskPrepared` (`internal/core/tasks/inspect.go`).
10. The task pre-pass skips the reclaim and treats a rank collision as a provisional rank. The pre-pass can therefore refuse where the write would succeed only for a corrupt in-progress task with an empty owner. Marker: `planUpdate`.
11. A fail-open write runs with no digest check (`want == ""`): nothing was inspected, so there is nothing to match. Marker: `RunInspected`.
12. The bridge's drop reason for a refusal is the fixed text `rejected by inspection`; the inspector's code and reason appear only in the core's log line, with the request id. Marker: `handleMessage`.
13. This ADR is number 0013 (the 2026-10-27 plans take 0012 signing keys, 0013 write inspection, 0014 agent status, 0015 MCP-only client). Marker: the comment under the title.
14. `request_id` is 16 hex characters (`core.RandomHex(8)`), new for every inspector call; a re-inspection gets a new one. Marker: `inspect`.

## Consequences

- **Latency.** An inspected write costs one extra read snapshot and one inspector round trip, bounded by `inspection_timeout_seconds` (default 5). With no `inspection_endpoint` the pre-pass is skipped and nothing changes.
- **Confidentiality.** Every inspected record leaves the server process for the inspector. The settings accept `https`, or `http` on a loopback host only, refuse userinfo in the URL (`inspection_endpoint` may carry a query string), and send the key as a bearer token. Logs and errors name the scheme and host only; they never show the path or query, the key, or the payload.
- **`degraded` on failure.** In both modes a failure writes the server notice `inspection` and health goes `degraded`. `noteFailure` re-asserts the failing state after its notice write, and the write is bounded at 5 s. A success clears the notice once it is at least 60 s old.
- **Access.** A scoped agent gets `not_found` for an out-of-scope channel before any inspection, so the inspector is no oracle. The rate check also runs first, so a sender cannot flood the inspector. No `TestAccessRuleSites` entry is added; one key is renamed (`memories.go Edit` to `planEdit`).
- **Strict inputs.** `ParseVerdict` rejects as malformed a duplicate member, a `null` member, a member in the wrong case, and an `allow` carrying `code` or `reason`. An unknown verdict, unknown `inspection_on_failure` value, unknown `Source` or `Operation` never means allow: an unknown `Source` or `Operation` answers `internal`, and a failure class outside timeout, transport, status, malformed and verdict is logged as `transport`. Float settings reject NaN and Inf, and a blank key file is refused.
- **Bridge.** The bridge's trust check runs in a `Store.Read` before the send, so an untrusted message reaches the inspector only if trust changes between that check and the append.
- **State.** The inspection keeps no stored state except the `inspection` notice. A keyed retry after a refusal re-inspects (no receipt was stored); a keyed retry after success replays without inspection; two overlapping keyed calls both inspect and one receipt wins.
- **Pre-pass content decision.** A task update the pre-pass judged content-neutral is not rechecked in the write pass; only content already in the store can land uninspected that way, the same cost as the receipt-expiry window.
- **No schema change and no new migration.** The notice uses the existing `notices` table.

## Deferred

Redact, hold and quarantine, a notify channel (or an events table), inspection in local mode, and a command adapter. Each needs its own design and Eric's decision.
