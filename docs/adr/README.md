# Architecture decision records

Decisions for agentbus2, numbered from 0001. Each record labels the
decisions as Eric's and cites the numbered decisions it transcribes:
1 to 63 from `HANDOFF.md` (2026-10-19), 64 to 96 from Eric's review
answers (`.local/review-answers-2026-10-20.md`, 2026-10-20; records
0001 to 0004, 0006 and 0007 carry them in a "Decisions of 2026-10-20
(Eric)" section, and 0008 is made of them).
The v1 records live in the agentbus repository
(`/Users/efitz/Projects/agentbus/docs/adr/`) and are cited as "v1 ADR
NNNN" to keep the two series apart.

| Number | Title | Status |
| --- | --- | --- |
| [0001](0001-transport-and-api.md) | Transport and API | Accepted (Eric, 2026-10-19) |
| [0002](0002-authentication-and-credentials.md) | Authentication and credentials | Accepted (Eric, 2026-10-19) |
| [0003](0003-identity-sessions-and-delivery.md) | Identity, sessions and delivery | Accepted (Eric, 2026-10-19) |
| [0004](0004-store-contract-and-storage.md) | Store contract and storage | Accepted (Eric, 2026-10-19) |
| [0005](0005-no-migration-from-v1-and-separate-product.md) | No migration from v1; agentbus2 is a separate product | Accepted (Eric, 2026-10-19) |
| [0006](0006-no-direct-messages.md) | No direct messages in v2 | Accepted (Eric, 2026-10-19) |
| [0007](0007-openapi-code-generator.md) | OpenAPI code generator | Accepted (Eric, 2026-10-20) |
| [0008](0008-channel-access-control-lists.md) | Channel access control lists | Accepted (Eric, 2026-10-20) |
| [0009](0009-scopes-and-slack-bridge.md) | Scopes and the Slack bridge | Accepted (Eric, 2026-10-23) |
| [0010](0010-server-health.md) | Server health | Accepted (agent proposal, ratified by Eric 2026-10-25) |
| [0011](0011-one-access-check.md) | One access check | Accepted (Eric, 2026-10-25) |
| [0012](0012-signing-key-rotation-and-key-providers.md) | Signing key rotation and key providers | Accepted (Eric, 2026-10-27); controller rulings pending his review |
| [0013](0013-write-inspection.md) | Write inspection | Accepted (Eric, 2026-10-27) |

Decisions 60 (TUI in a later milestone), 61 (release tooling in the
final milestone) and 87 (Scoop and winget in that tooling) are scheduling
decisions transcribed by no record; ADR 0005 cites Decision 61 only for
the Homebrew formula name and ADR 0006 cites Decision 60 only to place
the TUI inbox view out of scope.
