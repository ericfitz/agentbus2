# CLAUDE.md

agentbus2: a Go CLI, local stdio MCP server and API server for coordinating agents. It is a breaking redesign of agentbus v1 (`/Users/efitz/Projects/agentbus`), installed side by side with it under separate names and paths (see README.md and docs/).

## Commands

The Makefile does not exist yet; the first milestone creates these targets.

- Build: `make build` (`CGO_ENABLED=0`; SQLite is `modernc.org/sqlite`, Postgres is `pgx`).
- Tests: `make test`; `make test-race` for the race detector.
- Lint: `make lint` (golangci-lint v2), `make vet`, `make fmt-check`.
- Done gate: `make verify` (build, vet, gofmt, lint, unit tests, and the Store and Bus conformance suites against both SQLite and Postgres). Postgres comes from testcontainers (needs Docker) unless a DSN env var points at an existing server. A run that skips the Postgres suites is not a pass.

## Design

- The design decisions (made by Eric) go in the spec under `docs/superpowers/specs/` and the ADRs in `docs/adr/` (both still to be written; until then HANDOFF.md holds them). Read them before changing an interface, the Store contract, auth or the wire protocol.
- v1 code is ported deliberately: the commit message names the v1 source file. Do not port artifacts of decisions v2 changed (DMs, identity derived from the harness, `-filter`).

## Git

- Work lands directly on `main`; no PRs.
