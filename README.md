# agentbus2

A message bus, shared memory and shared task queue that let multiple agents work together. Agents can share a local database or connect to a server.

agentbus2 is the successor to [agentbus](https://github.com/ericfitz/agentbus). It is a breaking redesign, not an upgrade: it uses its own binary, data directory, configuration and MCP server name, so it can be installed next to agentbus 1.x without touching 1.x data.

Status: Milestone 4 (OpenAPI, server, client, local mode) done; the server refuses every authenticated call until Milestone 5.

## Development

Requirements: Go 1.27, Docker, golangci-lint v2.14.0. `make verify` needs Docker even when `AGENTBUS2_TEST_POSTGRES_DSN` is set: the variable only replaces testcontainers as the suites' Postgres.

- `make verify`: the done gate (build, vet, gofmt, lint, generated-code freshness, unit tests, both conformance suites on SQLite and Postgres, gate checks).
- The Postgres database must use a UTF-8 ctype (text-search normalization relies on `[[:alnum:]]` matching non-ASCII letters).
- `make generate`: regenerate `internal/gen` from `api/openapi.yaml`.
- `make check-gates`: the negative halves of the gate (unreachable Postgres, no Docker and no variable, and others), runnable alone.
- `make check-gates-selftest`: the harness's own test; mutates a throwaway worktree of `HEAD` and proves each gate check fires.
- `scripts/ci-sim.sh`: the CI `verify` job locally against a `postgres:17` container (`CI_SIM_NAME`, `CI_SIM_PORT`).
- `scripts/ci-gate-negative.sh <expected-text>`: the CI negative jobs' assertion; `make verify` must fail with that text and never print `verify: OK`.
