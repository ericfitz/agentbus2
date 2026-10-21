# CLAUDE.md

agentbus2: a Go CLI, local stdio MCP server and API server for coordinating agents. It is a breaking redesign of agentbus v1 (`/Users/efitz/Projects/agentbus`), installed side by side with it under separate names and paths (see README.md and docs/).

## Commands

- Build: `make build` (`CGO_ENABLED=0`; SQLite is `modernc.org/sqlite`, Postgres is `pgx`).
- Tests: `make test`; `make test-race` for the race detector.
- Lint: `make lint` (golangci-lint v2), `make vet`, `make fmt-check`.
- Done gate: `make verify` (build, vet, gofmt, lint, `generate-check`, unit tests, the Store and Bus conformance suites against both SQLite and Postgres, and `check-gates`). Postgres for the suites comes from testcontainers unless `AGENTBUS2_TEST_POSTGRES_DSN` names a reachable server (an empty value counts as unset). `make verify` needs Docker either way, because `check-gates` runs the suite binaries in a `busybox` container to prove the no-Docker path. A run that skips the Postgres suites is not a pass.
- `make test` is Docker-free (every package except `internal/conformance`); `make test-conformance` runs the suites only.
- `make generate` regenerates `internal/gen` from `api/openapi.yaml`.
- `make check-gates-selftest` tests the gate scripts themselves (minutes; CI runs it as its own job).

## Design

- The design decisions (made by Eric) go in the spec under `docs/superpowers/specs/` and the ADRs in `docs/adr/` (both still to be written; until then HANDOFF.md holds them). Read them before changing an interface, the Store contract, auth or the wire protocol.
- The Store contract is `internal/store`; the engines are `internal/store/sqlite` and `internal/store/postgres` over `internal/store/sqlstore`; the Store suite is `internal/conformance/store` and isolates Postgres tests in a schema per test.
- v1 code is ported deliberately: the commit message names the v1 source file. Do not port artifacts of decisions v2 changed (DMs, identity derived from the harness, `-filter`).

## Git

- Work lands directly on `main`; no PRs.
