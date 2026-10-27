# Write inspection: design brief

## 1. Status

- Draft of 2026-10-27, written by an agent (Claude). Checked against `main` at 9fc48e7.
- Nothing in it is a decision until Eric marks it so. Where the text says "recommended", that is the agent's proposal.
- Issue: #5, "feat: inspection hook and response mechanism for writes".
- **Eric's decision 2026-10-27**: Decision 13 is decided **yes**, a refused write is charged to the sender's rate limit. This amends Milestone 3 rule 1.
- **Eric reviewed the other decisions on 2026-10-27 and ratified decisions 1 to 12 as recommended.** All 13 decisions are decided and none is open.
- **Eric's decision 2026-10-27**: "Protocol version will be 1.0.0, end of discussion." Any addition in this brief ships in protocol 1.0.0, and `info.version` and `version.Protocol` stay `1.0.0` before the first release.

## 2. Problem and scope

v1 ran an operator-supplied command before each write and refused the write when the command said so. v2 2.0.0 did not port it. Eric decided this on 2026-10-21 (Milestone 3 item 5, `docs/superpowers/plans/2026-10-21-milestone-3-core-bus.md:79`; recorded in `docs/superpowers/specs/2026-10-21-gate-0-decisions.md:52` and ADR 0003 addendum item 5, `docs/adr/0003-identity-sessions-and-delivery.md:239-242`). Issue #5 asks for a v2 inspection hook plus a response mechanism: what the bus does with the inspector's verdict (allow, refuse with a code, redact or quarantine, notify the sender or an administrator).

The issue's acceptance criteria, restated:

1. A design (spec section or ADR) that says where inspection runs (in the core, before the write transaction), what the inspector sees, the timeout and failure mode (fail open or fail closed), and how it is configured (Decision 85 configuration).
2. A defined set of responses, and for each one how it reaches the sender and operators (problem code, notice, audit).
3. Identical behavior on SQLite and Postgres and on both transports, proven by the Bus conformance suite.

Out of scope for this brief:

- Inspecting reads (receive, history, search). This is a write-side control only.
- Inspecting channel, ACL, scope, identity, enrollment and bridge administration. These writes carry no message content (Decisions for Eric, item 8).
- Content moderation of Slack itself. A Slack message the bus refuses is still in Slack, and the bridge cannot remove it.
- Building an actual inspector (a PII scanner, a prompt-injection classifier). The bus defines the protocol; operators bring the policy.
- Quarantine as a working feature in the first delivery. It is designed in section 4.6 and recommended as a later phase with its own plan.

The issue says "not a 2.0.0 blocker".

## 3. What exists today

### 3.1 v1's hook (read-only reference)

- `/Users/efitz/Projects/agentbus/internal/bus/inspect.go`: `inspect(kind, as, payload)` runs `cfg.InspectionCommand` (an argv list, no shell). The hook reads `{"operation","sender","payload"}` on stdin and writes `{"allow": bool, "reason": string}` on stdout. Limits: a 64 KiB cap on stdout and stderr, `WaitDelay` of 1 s, and the timeout `inspection_timeout_seconds` (default 5, bounds 0.1 to 60: `internal/config/config.go:42-43,82-83,359-373`). A denial is `inspection_rejected` (not retryable) with the hook's reason. A start failure, timeout, crash, or malformed or oversized output is `inspection_unavailable` (retryable), so v1 failed closed. The log line carries a reason bounded to 256 bytes and never the payload. Hooks were serialized within a process (`hookMu`).
- Call sites: `send` (`messages.go:350`), `task_create` (`tasks.go:557`), `task_update` (`tasks_update.go:373`), `edit_memory` (`memories.go:192`) and `delete_memory` (`memories.go:333`). Every one ran after the receipt check, the reference validation and the envelope-size gate, and before `Begin` (`messages.go:331-359`). A receipt replay never re-ran the hook. Task reclaim bypassed it (`tasks_reclaim.go:99`).
- v1's Windows plan skipped the hook's `/bin/sh` fixtures on Windows (`docs/superpowers/plans/2026-10-06-windows-support.md:52,1273,1290`).

### 3.2 v2's write paths

Every v2 content write does all of its checks inside the write transaction. This is the main difference from v1 and the core of the design problem.

- `messaging.SendWith` (`internal/core/messaging/send.go:47-148`) is the only message path. `Send` calls it (`:38-40`), and so does the Slack bridge's inbound path (`internal/slackbridge/inbound.go:395`). Inside `Store.Append` (`:55`) it resolves the principal (`:57`), normalizes the subject and tags (`:65-72`), checks the session (`:78-93`) and the receipt (`:98`), looks up the channel and runs `Access.Require(... Write)` (`:99-105`), sizes the envelope (`:114`), runs `Limits.Check` (`:118`), appends (`:121`), and runs the caller's `AppendHook` inside the same transaction (`:125-129`). After commit come `Limits.Charge`, the notifier publish and the embedding kick (`:142-146`).
- `memories.Edit` (`internal/core/memories/memories.go:90-215`) normalizes before the transaction (`:91-116`). Inside `Store.Append` (`:126`) it checks the receipt (`:132`), loads the live revision (`:133`), runs `Access.Require(... Write)` (`:137`), merges the patch over the old revision (`:150-168`), and then does the size gate, `Limits.Check` (`:175`), the tombstone and the append. The content that ends up stored therefore depends on rows read inside the transaction.
- `memories.Delete` (`:222`) runs a `Store.Tx` and carries no content.
- `tasks.Create` (`internal/core/tasks/create.go:19-53`) and `tasks.Update` (`internal/core/tasks/update.go:213-241`) both run inside `Store.Append`. `Claim` and `Release` are fixed patches through `Update` (`update.go:313-326`). The stored task document is computed from the current task plus the patch, and a create or move can rerank siblings, which writes revisions of other tasks (`create.go:118`).
- System writes that are not user content, and so stay outside inspection: the task reclaim (`internal/core/tasks/reclaim.go:101`), the bridge's lifecycle notice (`internal/slackbridge/create.go:471-473`, a direct `core.Append`) and `channels.Service.EnsureGeneral` (`internal/core/channels/service.go:31`).
- `rg -n 'inspect' --type go internal` finds nothing. v2 has no inspection code today.

### 3.3 Decision 26 and its rules

Decision 26 (spec `docs/superpowers/specs/2026-10-19-agentbus2-design.md:664-670`): `Store.Tx` is serializable, may run `fn` more than once, and `fn` must have no side effects. Anything non-transactional moves outside `Tx`. Decision 69 (`:672-676`) adds that Postgres retries SQLSTATE 40001 a small fixed number of times, and SQLite relies on an immediate transaction with a 5 s busy timeout. The Milestone 3 Global Constraints (`plan:44`) set three rules:

1. `Limits.Check` may run inside `fn` and `Limits.Charge` runs only after commit.
2. Random values are generated once, before `fn`.
3. Publishes, the embedding kick and log lines run after `Tx` returns nil.

Search follows the same pattern: the query embedding's HTTP call runs before any transaction, because "HTTP never runs inside fn" (`plan:815`).

These rules rule out an inspector call inside `fn` on two counts. First, it is a side effect that a Postgres retry would repeat. Second, on SQLite it would hold the single write lock for as long as the inspector takes, up to the timeout. Every other writer in every process waits, and those that wait past the 5 s busy timeout fail with `unavailable`.

### 3.4 One access check (ADR 0011)

- `internal/core/access/decide.go` `decide` is the only code that decides who may do what (ADR 0011 decision 3, `docs/adr/0011-one-access-check.md`; `internal/core/access/doc.go`).
- `TestAccessRuleSites` (`internal/core/access/sites_test.go:17-56`) fails when server-side code under `internal/core`, `internal/server`, `internal/slackbridge` or `internal/bus/local` (`:60-65`) reads a selector named `Admin`, `Creator`, `Owner` or `Scope`, or uses `store.IdentityHuman`, `store.IdentityAgent` or `KindMemory`, outside an exact-count allowlist (`:75-131`). An inspection request builder that copies the sender's scope or admin flag would therefore need an allowlist entry. The memory-kind branch of `SendWith` is already listed (`:108`).
- What follows for inspection: it must not become a second place that decides access. It runs only after the access check has allowed the write. It can only add a refusal and can never grant anything. It must not be handed the facts an operator would need to rebuild access rules in a script (the ACL, the administrator flag, the scope). An exemption ("administrators are not inspected") would be an access rule, so it would belong in `decide` as an action (section 4.4).

### 3.5 Configuration (Decision 85)

- Spec `:887-902`: a configuration file, then `AGENTBUS2_SERVER_*`, then flags. `internal/server/config/config.go:39-88` is the `Config` struct, and `Settings()` (`:141`) lists the names. The embedding settings (`:75-79`: endpoint, model, API key file or environment variable, query timeout) are the precedent for an outbound HTTP collaborator. `cmd.Run` builds the embedder from them (`internal/server/cmd/cmd.go:150-158`) and passes it through `core.Options.Embedder`. The port is `core.Embedder` (`internal/core/core.go:20-33`).
- `internal/server/config/docs_test.go:15-30` checks `docs/ops/configuration.md` against `Settings()` and `Default()`. A new setting must land in that document in the same change.
- Local profiles build the core from `core.Defaults()` (`internal/client/local.go:69`). There is no server configuration file in local mode. ADR 0002 (`docs/adr/0002-authentication-and-credentials.md:91-93`) says a repository can select a profile but cannot name a program to run.
- The shipped container image's runtime stage is `cgr.dev/chainguard/static` (`Dockerfile:11`): no shell and no interpreter.

### 3.6 Errors, wire and health

- The code set is closed in three places: `internal/bus/errors.go:15-49` (`validCodes`), the OpenAPI `Problem.code` enum (`api/openapi.yaml:1141-1153`), and `internal/server/problem.go:17-41` (`StatusFor`, where an unknown code maps to 500). `CodeUnavailable` is documented as "the store reported a retryable failure (Decision 69)" (`errors.go:35-36`). `internal/bus/remote/remote.go:64` passes any problem code through unchanged. Spec 3.3 (`:261-264`) says a client treats an unknown code by its `retryable` member and HTTP status.
- `info.version` is `1.0.0` (`api/openapi.yaml:4`), as is `version.Protocol` (`internal/version/version.go:12`). Before the first release an addition does not bump it (Eric's decision 2026-10-27).
- Server notices are keyed by kind (`tx.SetNotice`, `tx.ClearNotice`; used in `internal/core/maintenance/budget.go:82-84` and `internal/core/embed/batch.go:219-227`). Health is `degraded` whenever any notice is set (`internal/core/health/health.go:104`), so a new notice kind degrades health with no change to the rule. Notices are shown at the administrator tier.
- v2 has no audit table. The operator's record is the server log.

### 3.7 The Slack bridge's inbound path

- `handleMessage` (`internal/slackbridge/inbound.go:252`) serves both live events and catch-up (`internal/slackbridge/catchup.go:126`). It runs the trust checks in `Store.Read` snapshots (`:333`), then calls `SendWith` with a hook that re-checks trust and writes the map row inside the append transaction (`:395-423`).
- Outcome mapping (`:432-461`): `conflict` is a duplicate; `validation` records a drop and is ignored; `forbidden` is a drop; **any other code returns `OutcomeRetry`**. A new refusal code would therefore be retried forever unless this switch gains a case for it.

### 3.8 The conformance suite

- `internal/conformance/bus` runs on both engines. `newBus` (`fixtures_test.go:201`) defaults to `local.New` and is reassigned for `AGENTBUS2_BUS_TRANSPORT=inproc` (`main_test.go:24`). `harnessOptions` (`fixtures_test.go:150-156`) already injects a fake embedder through `core.Options` (`:187-191`). `faultStore` (`:98`) fails the first transaction with a chosen `PgError`, which forces a Decision 69 retry on Postgres. `sibling` (`:206`) builds a second core over the same database, and `replicas_test.go` starts real `cmd.Run` servers.

## 4. Options

### 4.1 Where inspection runs relative to the write transaction

**A. Before `Store.Append`, on the raw input (v1's shape).**
- Simplest change.
- In v2 the inspector would see input that has not been normalized, with no channel kind and no sender kind.
- It would run for writes the access check then refuses. This makes the inspector a free target for unauthorized callers, and its verdict could arrive before an out-of-scope `not_found`.
- It would re-run on every keyed retry, because v2 checks the receipt inside `fn`.
- For `edit_memory` and `task_update` it would see only the patch, never the record that gets stored.

**B. A read-only pre-pass, then the inspector, then the authoritative write (recommended).**
- How it works:
  1. Each inspected write gets a `prepare(ctx, tx)` step: everything its `fn` does today up to the first write.
     - `SendWith`: principal, normalization, session, receipt, channel, `Access.Require`, `EnvelopeSize`, `Limits.Check`.
     - `Edit`: receipt, live revision, `Require`, merge, size, rate.
     - `tasks.Create` and `tasks.Update`: the same, up to the document they would write.
  2. The write runs `prepare` in a `Store.Read` snapshot first. A receipt hit replays with no inspection, as in v1. A refusal from access, validation or rate limit answers with no inspection. This needs `WithReceipt` (`internal/core/receipts.go:21-55`) split into a check-only lookup and the store step: today it calls `fn` and then `PutReceipt` on a miss, and that write fails with `ErrReadOnly` inside `Store.Read`. v1 had the two halves as `checkReceipt` and `storeReceipt`.
  3. Otherwise the inspector runs outside any transaction, on the prepared record.
  4. `Store.Append` then runs `prepare` again (the authoritative pass) and writes only if the prepared record matches what was inspected: the same digest over the channel id and the content fields, and for an edit or task update the same base revision.
  5. If the record no longer matches (a concurrent edit, a channel deleted and recreated under the same name), the call runs the pre-pass and the inspection once more. A second mismatch is a retryable `conflict`.
- Cost: one extra read snapshot and one inspector round trip per inspected write, and nothing at all when no inspector is configured (the pre-pass is skipped).
- The bridge already uses this read-then-write pattern (`inbound.go:333`, then `:395`).
- Security: access is decided before inspection, so a scoped agent still gets `not_found` for an out-of-scope channel and cannot use the inspector as an oracle. Rate limiting is checked before inspection, so a sender cannot flood the inspector.
- Testability: identical on both engines, because the core does it. A `faultStore` retry on Postgres proves the inspector runs once per call.

**C. Inside `fn`.** Ruled out by Decision 26 and by SQLite's single writer (section 3.3). Listed only to record why it is not offered.

**D. After commit (asynchronous review).** The write commits and is delivered. The inspector reviews it afterwards and can only notify, or tombstone the message after it has been read. It prevents nothing, so it does not meet the issue's "before the write transaction". It could be added later as a notify-only mode.

### 4.2 What the inspector sees

**A. The prepared record plus attribution, no access facts (recommended).**

The request is one JSON object, `{"version": 1, ...}`, with these members:

- `request_id`: random, generated before any transaction (Decision 26 rule 2), and also used in the log line.
- `operation`: one of `send`, `edit_memory`, `task_create`, `task_update` (and `delete_memory` if item 8 says so).
- `source`: `api` or `slack`. The bridge passes this through a new `SendWith` option; it is never guessed from the session hint.
- `sender` and `sender_kind`: `agent` or `human`. The kind is already resolved as `p.Kind`, which the gate does not count.
- `session`: the session's display name, when there is one.
- `channel`: name and kind (`ordinary`, `memory` or `tasks`).
- `record`: the normalized fields to be stored: subject, content, type, tags, metadata, `reply_to`, and for tasks the task document's subject, description and metadata. For an edit or task update, `base_revision`, so the inspector sees the whole record, not the patch.

The request does not carry the ACL, the administrator flag, the scope, or the channel creator. That keeps the inspector a content policy, not a second access check, and needs no new `TestAccessRuleSites` allowlist entries. Its size is bounded by `max_message_kib`, because the envelope gate runs first.

**B. As A, plus the sender's scope and administrator flag.** This lets an operator write sender-based policy, which is a second access check under another name (ADR 0011 decision 1). Each copied selector also needs an allowlist entry (`sites_test.go`, `Scope` and `Admin` clauses), plus a self-test mutation in `scripts/check-gates-selftest.sh`. **This changes the spirit of ADR 0011 and needs Eric's approval.** Not recommended.

**C. The raw request body.** This is simpler, but it hands the inspector input that may be refused anyway, and gives it patches rather than records. Not recommended, for the reasons in 4.1 A.

### 4.3 The transport

**A. Webhook: an HTTPS POST of the request, answered with a JSON verdict (recommended for the server).**
- Settings:
  - `inspection_endpoint`: https, or http on loopback only. The rule matches the profile `oidc.issuer` rule.
  - `inspection_api_key_file` or `inspection_api_key_env`: sent as a bearer token. The pattern copies `embedding_api_key_*` (`cmd.go:266-280`).
  - `inspection_timeout_seconds`.
  - `inspection_on_failure`.
- Behavior: no redirects are followed, the response is capped at 64 KiB, and it must be `application/json`.
- It suits the deployment model: every replica calls one inspector service, the static container image needs nothing added, and concurrency is plain HTTP.
- Security: all inspected content leaves the process for the inspector. That is the operator's choice, and it is documented in `docs/ops/configuration.md`. The adapter follows `internal/core/embed/http.go`.

**B. Command: v1's `inspection_command` (argv, JSON on stdin and stdout).**
- This is a familiar port of v1 and needs no service to run.
- In the shipped image the operator must add a statically linked inspector binary, because there is no shell (`Dockerfile:11`).
- Every replica runs its own copy. A process per write is costly under server load, so v1's serialization or a concurrency cap is needed.
- Windows works if the tests re-execute the test binary as the hook instead of using `/bin/sh` scripts.
- Offer it as a second adapter if Eric wants local-style deployments.

**C. In-process plugin.** Go's `plugin` package needs cgo and does not support Windows, so it is incompatible with `CGO_ENABLED=0` (Decision 53). The only in-process option is a compiled-in Go implementation of the port, which means a fork of agentbus2. Not offered as an operator transport.

Under every option, the seam is a `core.Inspector` interface on `core.Options`, after the `Embedder` precedent (`core.go:20-33`), with adapters in `internal/core/inspect/` (`webhook.go`, and `command.go` if item 3 says so). The conformance suite injects a scripted fake through `harnessOptions`.

### 4.4 Timeout and failure mode

- **Timeout:** `inspection_timeout_seconds`, default 5, bounds 0.1 to 60 (v1's values). It covers the whole call, including any wait for a concurrency slot. The bound that matters is the sender's latency, which v1 measured differently.
- **Failure mode.** The discriminator is whether the inspector is a security control (data-loss prevention, prompt-injection defense), which must fail closed, or an advisory filter, which should fail open so it never stops work.
  - **A. A setting, `inspection_on_failure`, default `closed` (recommended).** The default matches v1.
  - **B. Always closed.**
  - **C. Always open.**
- A failure is any of: a timeout, a transport error, a non-2xx status, malformed JSON, an unknown `verdict`, or a reply over 64 KiB. Unknown verdicts and unknown members are rejected, so a placeholder can never mean "allow" (Definition of Done B).
- Fail closed: the sender gets a retryable error (item 5).
- Fail open: the write proceeds and the log line says so.
- In both modes, a failure sets the server notice of kind `inspection` (outside any transaction, Decision 26 rule 3, rate-limited to one write per 60 s per process) and health goes `degraded` with no change to the rule (`health.go:104`). Each process keeps its inspector state (ok or failing) in memory. Only the transition from failing to ok clears the notice, and only when the notice is older than 60 s, so a healthy replica costs no Store access per write and two replicas that disagree cannot make it flap faster than that.
- **Exemptions.** A: none; the inspector sees `sender_kind` and decides (recommended). B: an access action, for example `Channel/SkipInspection` in `decide.go`, for operators who want administrators exempt. This keeps ADR 0011 intact, but it is a new access rule and needs Eric's approval.

### 4.5 The verdict protocol

The reply is `{"verdict": "allow"|"refuse"|"redact"|"hold", "code": "<[a-z0-9_]{1,32}>", "reason": "<text>", "notify": bool, "redact": {"subject"?: s, "content"?: s}}`. Rules:

- `code` and `reason` are required on `refuse` and `hold`.
- `redact` is required on `redact` and forbidden otherwise.
- `notify` is optional on every verdict.
- A verdict this server does not implement yet (`hold` before quarantine ships, `redact` on an operation that does not support it) is treated as an inspector failure, never as `allow`.

### 4.6 The responses: what the sender, operators and audit see

| Verdict | Sender | Operators | Audit (log line, never the payload) |
| --- | --- | --- | --- |
| allow | normal result | nothing | debug level only |
| refuse | error `inspection_rejected`, not retryable; detail `rejected by inspection (<code>): <reason>`, bounded by `MaxErrorMessageBytes` | counted in the log; notify if set | info: request id, operation, sender, channel, verdict, code, reason (256 bytes), latency |
| redact | normal result plus `redacted: true` (a new `SendResult` and `EditResult` member); the stored row holds the replacement | log; notify if set | info, as above; the original content is not stored anywhere |
| hold (quarantine, later phase) | error `inspection_held`, not retryable; detail names the hold id | an administrator lists, releases or discards held writes | info, as above, plus the held row itself |
| inspector failure | closed: retryable `unavailable` (or a new code, item 5); open: normal result | the `inspection` notice; health `degraded` | warn: request id, operation, failure class |

Notes per response:

- **Refuse.** The reason is the inspector's text. Agents treat it as data, not as an instruction (v1 spec 2026-09-06 "Inspection and diagnostics"). Status options:
  - A. 422, since the request is well formed and authorized and only its content is refused (recommended).
  - B. 403, which clients could confuse with an access refusal.
- **Redact.** Allowed on `send` and `edit_memory` only, and only on `subject` and `content`. Task writes treat `redact` as a failure, because a task's content is a structured document. The replacement goes through `NormalizeSubject` and the envelope gate again, so a replacement that breaks a rule is a `validation` error. The receipt's fingerprint is over the input, so a keyed replay returns the stored result, `redacted` included. Receivers are not told anything beyond the replacement text the inspector writes, for example `[redacted]`.
- **Hold (quarantine).** The held write is stored outside the message sequence, in a `held_writes` table (operation, sender, session, channel id, record, code, reason, created, expires). `Send` is the only operation that can be held; for edits and task writes, `hold` is a failure.
  - Why outside the sequence: a held message given a `seq` and hidden would be skipped by every cursor that passed it before release. Decision 27 guarantees commit order equals `seq` order. A hidden-row filter would also be a visibility rule outside `decide`, which ADR 0011 forbids.
  - Release appends a new message as the original sender through the normal path, with a new `seq`. It re-checks the sender's `Write` through `access.Require` at release time and does not inspect again.
  - The operations are new administrator calls (`Server/Administer`): `holds list|release|discard`.
  - A maintenance job expires holds after `inspection_hold_hours`, under the maintenance lock.
  - This is XL work with its own wire, CLI, MCP and TUI surface, so the recommendation is a later phase with its own brief.
- **Notify.** `notify: true` marks a verdict for operator attention. The sender is never told, so a tripwire stays hidden.
  - **A. Log at warn plus a counter in the log (recommended for phase 1).**
  - **B. Also set an `inspection` notice.** This conflates notify with outages, because there is one notice per kind.
  - **C. A post-commit message to a configured `inspection_notify_channel`, by a reserved `inspection` identity seeded in `0001_initial.sql` (precedent: the `slack-bridge` row, `internal/store/sqlite/schema/0001_initial.sql:248-250`).** The message holds the request id, operation, sender, channel, seq, verdict, code and reason. It is durable and reaches humans in the TUI. It needs a schema edit in place (allowed before the release) and a `sites_test.go` allowlist entry for the notice's sender kind (the `slackbridge/create.go notice` precedent, `:112`). It is a good second step.
- **Audit form.** A. Structured log lines only, as v1 did (recommended). B. An `inspection_events` table with retention and an administrator read operation. This is a schema edit in `0001_initial.sql` with no migration, plus a new wire operation (L).

### 4.7 Bridged inbound messages

**A. Inspect them (recommended).** Slack content comes from outside the bus and is exactly the input an operator most wants filtered before agents read it. `SendWith` is the inspected path already, so the only change is `source: "slack"` and new cases in `inbound.go`'s switch:

- `inspection_rejected`: a drop recorded with `PutBridgeDrop`, logged with the code, and ignored on later catch-up passes.
- An inspector failure: retry, the existing path.
- `inspection_held`: dropped from the bridge's view, since the hold row owns the message.

The bridge does not reply in Slack (recommended; a Slack-side notice is a later option). Outbound posts are not inspected again, because they were inspected when they were sent to the bus.

**B. Exempt `source: slack`.** Simpler, but the external input stays unfiltered.

**C. Inspect, but always fail open for the bridge.** This avoids stalling catch-up during an inspector outage, at the cost of a mode-specific rule.

### 4.8 Replicas

The inspector is stateless from the bus's point of view. Each replica calls it independently, and there is no lock and no coordination. Ordering: two writes inspected in one order may commit in the other. Commit order is still `seq` order (Decision 27), and inspection makes no ordering promise (v1 made none either). v1's per-process `hookMu` is dropped for the webhook. The command adapter (option 4.3 B only) keeps a per-process concurrency cap, `inspection_max_concurrent` (default 4), one more setting that exists only if B is chosen. The notice is shared through the Store (section 4.4). Quarantine's expiry job runs under the maintenance lock (`Store.Locks`), so one replica runs it.

### 4.9 Local mode

**A. Server only (recommended).** A local profile's core uses `core.Defaults()` (`client/local.go:69`), and the local user is their own operator. v1 users who relied on a local hook lose it; agentbus2 is a separate product with no compatibility promise to v1 (ADR 0005).

**B. A `config.json` profile setting (`inspection`), never one from the repository file (ADR 0002:91-93).** Every `agentbus2 mcp` process over the shared SQLite file would then call the inspector, which behaves like the replica case.

## 5. Decisions for Eric

1. **What responses the first delivery includes.** **DECIDED: A, the recommendation as written (Eric's decision 2026-10-27)** A: allow and refuse, plus notify through the log. B: A plus redact. C: B plus quarantine. **Recommended: A** now and B as a follow-up. Quarantine (C) gets its own brief, because it adds a table, administrator operations, a maintenance job and client surface.
2. **Where inspection runs.** **DECIDED: B, the recommendation as written (Eric's decision 2026-10-27)** A: before `Store.Append` on the raw input (v1's shape). B: a read-only pre-pass, then the inspector, then the authoritative write with a record digest check; receipt replays and access, validation and rate refusals never reach the inspector. **Recommended: B.** (Inside the transaction is ruled out by Decision 26.)
3. **Transport.** **DECIDED: A, the recommendation as written (Eric's decision 2026-10-27)** A: webhook only. B: webhook and command. C: command only (v1 port). **Recommended: A**, with the `core.Inspector` port leaving room for B. The shipped static image makes a command hook awkward.
4. **Failure mode.** **DECIDED: A, the recommendation as written (Eric's decision 2026-10-27)** A: the `inspection_on_failure` setting, default `closed`. B: always closed. C: always open. **Recommended: A.**
5. **Error codes.** **DECIDED: A, the recommendation as written (Eric's decision 2026-10-27)** A: new `inspection_rejected` (422, not retryable), and inspector failure reuses `unavailable` (503, retryable), widening its doc comment beyond "the store". B: new `inspection_rejected` and new `inspection_unavailable` (503, retryable), as v1 had. C: reuse `validation` for refusals. **Recommended: A**, because clients and the bridge already retry `unavailable` correctly and the log tells operators which kind of failure it was. A and B are wire changes (enum, statuses) that ship in protocol 1.0.0 with no version bump (Eric's decision 2026-10-27). C adds no code and no status, so it needs no wire change, but it mixes content refusals with malformed input.
6. **Bridged inbound messages.** **DECIDED: A, the recommendation as written (Eric's decision 2026-10-27)** A: inspected; a refusal becomes a drop. B: exempt. C: inspected, but always fail open. **Recommended: A.**
7. **What the inspector sees.** **DECIDED: A, the recommendation as written (Eric's decision 2026-10-27)** A: the prepared record, sender name and kind, session, channel name and kind, and source, with no ACL, administrator flag or scope. B: A plus scope and administrator flag (needs allowlist entries; strains ADR 0011). **Recommended: A.**
8. **Which operations.** **DECIDED: A, the recommendation as written (Eric's decision 2026-10-27)** A: content-bearing writes only: `send`, `edit_memory`, `task_create`, and `task_update` when the patch changes subject, description or metadata (so not `claim`, `release` or status-only patches). B: A plus `delete_memory` and every task patch (v1's set). **Recommended: A.**
9. **Exemptions.** **DECIDED: A, the recommendation as written (Eric's decision 2026-10-27)** A: none; the inspector decides using `sender_kind`. B: a new access action in `decide.go` (for example, administrators skip inspection). **Recommended: A.** B is an ADR 0011 amendment.
10. **Operator notification.** **DECIDED: A, the recommendation as written (Eric's decision 2026-10-27)** A: a warn log line. B: an `inspection` notice. C: a message to a configured channel from a reserved `inspection` identity. **Recommended: A** for phase 1 and C as a follow-up. The `inspection` notice is kept for outages either way.
11. **Audit.** **DECIDED: A, the recommendation as written (Eric's decision 2026-10-27)** A: structured log lines only, never the payload. B: an `inspection_events` table and an administrator read operation. **Recommended: A.**
12. **Local mode.** **DECIDED: A, the recommendation as written (Eric's decision 2026-10-27)** A: server only. B: a user-level `config.json` profile setting. **Recommended: A.**
13. **Rate charge for a refused write. DECIDED: A, yes (Eric's decision 2026-10-27); this amends Milestone 3 rule 1 (Global Constraints), which charged only committed writes.** A: charge a write the inspector refused. B: charge only committed writes, as v1 did and as Milestone 3 Global Constraints rule 1 says today (`Limits.Charge` only after a successful commit, `plan:44`). **Recommended: A**: without it a sender whose writes are all refused never drains its bucket, so the pre-pass `Limits.Check` never bounds that sender's inspector calls. A changes the Decision 26 rule 1 elaboration, an architectural change that needs Eric's approval.

## 6. Dependencies

- Issues: none blocking. ADR 0011 (#19, landed) fixes where any exemption would go. If quarantine is chosen, it depends on the maintenance lock (landed in Milestone 7).
- Libraries: none new. The webhook uses `net/http` and `encoding/json`. The command adapter, if chosen, uses `os/exec` with `Cmd.WaitDelay` (Go 1.20 and later; the module is `go 1.27`).
- External services: the operator's inspector. CI needs none, because the conformance suite uses an in-process fake and the adapter tests use `httptest` (plus a re-executed test binary for the command adapter).
- CI: `ci.yml` (`make verify`, `release-check`) and `windows.yml` must be green. The command adapter's tests must not use `/bin/sh` and must not `.Skip(` under `internal/conformance` (Milestone 3 Global Constraints).

## 7. Implementation outline (provisional; assumes the recommendations)

Every task follows TDD, and every task's gate is `make verify`: both engines, both transports through `make test-conformance`, and `check-gates`.

1. **Port and request types.**
   - Files: `internal/core/inspect.go` (the `Inspector` interface, `Request`, `Verdict`, verdict validation), `internal/core/core.go` (`Options.Inspector`, `Core.inspector`), `internal/core/config.go` (`InspectionTimeoutSeconds`, `InspectionOnFailure` and their bounds).
   - Tests: verdict parsing rejects unknown verdicts, missing `code` or `reason`, `redact` on a refuse, and replies over 64 KiB; config bounds; an unknown `inspection_on_failure` is rejected.
   - Gates: `make verify`.
2. **Pre-pass refactor of `SendWith`.**
   - Files: `internal/core/messaging/send.go`.
   - Change: split `WithReceipt` into a check-only lookup and a store step (`internal/core/receipts.go`), split out `prepare`, add the `Store.Read` pre-pass when an inspector is set, add the digest check in the write, and run the pre-pass and inspection once more on a mismatch. Add a `Source` option so the bridge can pass `slack`.
   - Tests (in `internal/conformance/bus/inspect_test.go`, prefix `TestInspect`):
     - an allow writes once;
     - a refuse writes nothing, stores no receipt and charges the limiter;
     - a keyed replay does not call the inspector;
     - a `forbidden` or out-of-scope `not_found` does not call it;
     - a `rate_limited` does not call it;
     - a `faultStore` 40001 retry on Postgres calls it exactly once;
     - a slow inspector does not block another identity's send (no write lock held);
     - an unknown verdict, a timeout, fail closed and fail open, and the notice set and cleared.
   - Gates: `make verify`, `scripts/m4-exit-checks.sh` (import rule).
3. **The same for `memories.Edit`, `tasks.Create` and `tasks.Update`.**
   - Files: `internal/core/memories/memories.go`, `internal/core/tasks/create.go`, `internal/core/tasks/update.go`, `internal/core/tasks/service.go`.
   - Tests: the inspector sees the merged record and the base revision; a concurrent edit between the pre-pass and the write re-inspects and then answers `conflict`; claim, release and status-only patches are not inspected.
   - Gates: `make verify`.
4. **Codes and wire.**
   - Files: `internal/bus/errors.go` (`CodeInspectionRejected`; the `unavailable` doc comment), `internal/server/problem.go` (422), `api/openapi.yaml` (the enum, 422 on `sendMessage`, `editMemory`, `createTask`, `updateTask`; `info.version` stays `1.0.0`), `internal/version/version.go` (stays `1.0.0`), then `make generate`. Also check that `internal/mcpserver`, `internal/cli` and `internal/tui` render the new code generically: their per-code branches today are `replaced`, `forbidden`, `session_lapsed`, `session_closed` and `not_found` (`internal/cli/exit.go:26`, `internal/cli/status.go:45`, `internal/mcpserver/server.go:454,811`, `internal/tui/model.go:1391,1413`, `internal/tui/client.go:146,230-242`), none on a write path. Any new TUI text must pass the `scripts/m8-exit-checks.sh` word check.
   - Tests: the inproc run of `TestInspect*` sees the 422 and the code through `remote`.
   - Gates: `make verify` (`generate-check`).
5. **Webhook adapter.**
   - Files: `internal/core/inspect/webhook.go`. It imports only the standard library and `internal/core` and `internal/bus`; it must not import `internal/gen` or `internal/server` (Decision 47 rule, checked by `scripts/m4-exit-checks.sh`).
   - Tests (`httptest`): the bearer header, no redirects, the size cap, the content type, and the deadline.
   - Gates: `make verify`.
6. **Configuration and wiring.**
   - Files: `internal/server/config/config.go` (`inspection_endpoint`, `inspection_api_key_file`, `inspection_api_key_env`, `inspection_timeout_seconds`, `inspection_on_failure`; https or loopback http validation), `internal/server/cmd/cmd.go` (build the adapter, pass `Options.Inspector`), `docs/ops/configuration.md` (rows the `docs_test.go` drift test checks).
   - Tests: `config_test.go` bounds and the key file versus environment variable conflict; `TestReplicas*` gains a case where two replicas call one `httptest` inspector.
   - Gates: `make verify`, `scripts/m7-exit-checks.sh`.
7. **Bridge inbound.**
   - Files: `internal/slackbridge/inbound.go` (pass `Source: slack`; `inspection_rejected` becomes a drop).
   - Tests: `slackbridge_inbound_test.go` checks that a refusal is dropped once and ignored on catch-up, and that an inspector failure retries.
   - Gates: `make verify`, `scripts/m7-exit-checks.sh` (Slack import rule).
8. **Docs and the ADR.** A new ADR (`docs/adr/0012-write-inspection.md`) records Eric's answers. Add a pointer to ADR 0003 item 5 ("superseded by ADR 0012"). Update `docs/ops/configuration.md` and a new `docs/ops/inspection.md` (the request and verdict schema, failure modes, the confidentiality note), `docs/ops/health.md` (the `inspection` notice), and the spec (a new subsection under section 5 or 6). Add one line to the CLAUDE.md Design section.

New import edges: `internal/server/cmd` → `internal/core/inspect`; `internal/core/inspect` → `internal/core` and `internal/bus`. Neither is forbidden by the Decision 47, Milestone 5, 6, 7 or 8 rules as written in CLAUDE.md. `internal/core` itself gains no import.

Wire changes: the `Problem.code` enum gains `inspection_rejected`; the four write operations gain a 422 response; the addition ships in protocol 1.0.0, and `info.version` and `version.Protocol` stay `1.0.0` before the first release (Eric's decision 2026-10-27); then `make generate`. Redaction, if chosen, adds `redacted` to the send and edit results (same rule). Quarantine adds operations and a code.

## 8. Risks and test strategy

- **Concurrency.** Between the pre-pass and the write, a channel can be renamed or deleted, a memory edited, or a task moved. The digest check turns every such race into one re-inspection and then a retryable `conflict`, never a write of content the inspector did not see. A test drives each race with a hook between the two passes.
- **SQLite writer lock.** Inspection never holds a transaction. A test runs a blocked inspector next to a concurrent send from a second identity and asserts the second send commits.
- **Postgres retries.** `faultStore` forces a 40001 error on the first `Append`. The test asserts one inspector call and one stored row.
- **Replicas.** Each replica inspects independently. The `inspection` notice is a single row, written by whichever replica fails last, and cleared only once it is older than 60 s. A `TestReplicas` case covers one shared inspector.
- **Second run, overlapping runs, a failed run's leftovers (Definition of Done D).** In phase 1 the inspection itself keeps no state. A keyed retry after a refusal re-inspects, because no receipt was stored. A keyed retry after success replays with no inspection. Two overlapping keyed calls both inspect, and the serializable write makes one store the receipt while the other replays it. A crash between inspection and commit leaves nothing. The only stored state is the notice, and a second failure rewrites it. Quarantine, if built, adds held rows: release must be idempotent (a released hold is deleted in the same transaction as the append), and the expiry job is guarded by the maintenance lock. A held write must also store its receipt (unlike a refusal), or a keyed retry after `inspection_held` creates a second hold; the quarantine brief must settle this.
- **Inspector as an attack surface.** The reason text reaches agents; it is bounded and documented as data. The request carries content to a third party, so the docs warn about it and https is required off loopback. An inspector that always fails closes the bus for writes, which is the point of closed mode; health shows it.
- **Windows CI.** The fake inspector is in process. The webhook tests use `httptest`. A command adapter, if chosen, re-executes the test binary as its hook instead of using shell fixtures. Nothing skips.
- **Gate scripts.** Phase 1 adds no `sites_test.go` allowlist entries under recommendation 7 A. Options 7 B and 10 C each add one, plus a `check-gates-selftest.sh` mutation. The anchored counts in `scripts/m3-exit-counts.sh` (the ACL matrix, the concurrent send) are untouched by new `TestInspect*` tests.

## 9. Size estimate

**L** for phase 1 as recommended (allow, refuse and log notification, the webhook, configuration, the bridge and the wire code). Most of the work is the pre-pass refactor of four write paths and the tests on both engines and both transports. Adding redaction stays L. Quarantine is **XL** on its own.
