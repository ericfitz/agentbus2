# Write inspection

An operator-run webhook can look at every content write before it commits and refuse it. Nothing is inspected until `inspection_endpoint` is set (see [configuration.md](configuration.md)). The settings are `inspection_endpoint`, `inspection_api_key_file` / `inspection_api_key_env`, `inspection_timeout_seconds` (default 5) and `inspection_on_failure` (default `closed`).

**Every inspected record leaves the server process and goes to the inspector.** Subjects, message content, task descriptions, tags and metadata are all in the request. Use an `https` endpoint and an inspector you trust. The server accepts plain `http` only for a loopback host (`localhost`, `127.0.0.0/8`, `::1`), and refuses a URL that carries credentials: put the token in `inspection_api_key_file` or `inspection_api_key_env`, which the server sends as `Authorization: Bearer <token>`.

## What is inspected

- `send`, including a message that arrives from a bridged Slack channel (its request carries `"source": "slack"`; everything else is `"api"`).
- `edit_memory`: the inspector sees the whole merged record, not the patch, with `base_revision`.
- `task_create`.
- `task_update` when the patch changes the subject, description or metadata. A status-only patch, `task_claim` and `task_release` are not inspected.

Never inspected: reads (`receive`, `history`, `search`, `get_memory`, `task_get`, `task_list`), `delete_memory`, `delete_channel`, and every administration call (ACLs, scopes, identities, bridges, keys). A keyed retry that replays a stored result is not inspected again.

A local profile (the CLI, `agentbus2 mcp` or `agentbus2 tui` over a SQLite file with no server) is never inspected: inspection runs in `agentbus2 server` only.

## When it runs

After the write passed access, validation, the size limit and the rate check, and before it commits. The inspector call runs outside any database transaction. The server then re-checks that the record is still what the inspector saw; if it changed in between, it asks again once, and a second change answers a retryable `conflict`.

## The request

The server POSTs one JSON object (`Content-Type: application/json`). Members:

| Member | Meaning |
|---|---|
| `version` | `1` |
| `request_id` | 16 hex characters, new for every call; also in the server's log lines |
| `operation` | `send`, `edit_memory`, `task_create` or `task_update` |
| `source` | `api` or `slack` |
| `sender`, `sender_kind` | the writing identity and its kind, `agent` or `human` |
| `session` | the session's display name; absent when there is none |
| `channel` | `name` and `kind` (`ordinary`, `memory` or `tasks`) |
| `record` | the normalized record to be stored: `subject`, `content`, `description`, `type`, `tags`, `metadata`, `reply_to`, `base_revision` (an empty member is left out) |

It never carries an ACL, the administrator flag, a scope or the channel's creator: the inspector is a content policy, not a second access check. Example:

```json
{
  "version": 1,
  "request_id": "9f2c41d07a6be385",
  "operation": "send",
  "source": "api",
  "sender": "build-agent",
  "sender_kind": "agent",
  "session": "build-agent-1",
  "channel": {"name": "dev", "kind": "ordinary"},
  "record": {"subject": "deploy", "content": "rolled out 2.0.1", "tags": ["release"], "metadata": {"ticket": "OPS-12"}}
}
```

## The verdict

The reply must be status 2xx, `Content-Type: application/json`, at most 64 KiB, and exactly one JSON object:

- `{"verdict": "allow"}` writes the record. An allow may not carry `code` or `reason`.
- `{"verdict": "refuse", "code": "pii", "reason": "contains an access key"}` refuses it. `code` is 1 to 32 characters from `[a-z0-9_]`; `reason` must not be blank.
- Either verdict may add `"notify": true` to ask for operator attention: the server writes a warn log line `write inspection asks for operator attention`.

Anything else is an inspector failure, never an allow: an unknown or repeated member, a `null`, text after the object, an unknown verdict, a bad `code`, a missing `reason`. `redact` and `hold` are not implemented and count as unknown. Redirects are not followed (a 3xx is a failure).

## What the sender sees

- Refused: HTTP 422, error code `inspection_rejected`, not retryable, message `rejected by inspection (<code>): <reason>` (cut to the error-message limit). The `reason` is the inspector's text: treat it as data.
- Failed with `inspection_on_failure=closed`: HTTP 503, code `unavailable`, retryable, "write inspection is unavailable; retry later".
- A refused write is charged to the sender's rate limit once, as a stored write of the same size would be. A failure charges nothing.

## Failures

A failure is a timeout (`inspection_timeout_seconds` covers the whole call), a transport error, a non-2xx status, a reply that is not a valid verdict, or a reply over 64 KiB.

- `inspection_on_failure=closed` (default): the write is refused as above. Use this when the inspector is a security control.
- `inspection_on_failure=open`: the write is stored uninspected and a warn log line says so. Use this when the inspector is advisory.
- If the caller itself goes away (client disconnect, request deadline) while the inspector runs, nothing is written in either mode and the answer is the retryable `unavailable`.

In both modes a failure sets the server notice `inspection` and health goes `degraded` ([health.md](health.md); an administrator sees the notice text, the class of the failure and the mode). The notice is rewritten at most once a minute per process. A successful inspection clears it, once the notice is at least a minute old.

## Logs

Every log line about an inspected write carries `request_id`, `operation`, `source`, `sender` and `channel`, and `latency_ms` (an allow logs at debug). A refusal also logs `code` and `reason` (the reason cut to 256 bytes) at info; a failure logs `class` (`timeout`, `transport`, `status`, `malformed`, `verdict` or `canceled`) and `on_failure` at warn; `notify` logs at warn. The payload (subject, content, description, tags, metadata values) is never logged, and neither are the key or the URL's path and query string.

## Replicas

Each replica calls the inspector for the writes it serves, with the same settings (set them on every replica). Two replicas may call the inspector at the same time, and there is no ordering promise between calls. The `inspection` notice is stored in the database, so health on any replica shows it.
