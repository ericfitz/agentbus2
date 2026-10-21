# CLAUDE.md

agentbus2: a Go CLI, local stdio MCP server and API server for coordinating agents. It is a breaking redesign of agentbus v1 (`/Users/efitz/Projects/agentbus`), installed side by side with it under separate names and paths (see README.md and docs/).

## Commands

- Build: `make build` (`CGO_ENABLED=0`; SQLite is `modernc.org/sqlite`, Postgres is `pgx`).
- Tests: `make test`; `make test-race` for the race detector.
- Lint: `make lint` (golangci-lint v2), `make vet`, `make fmt-check`.
- Done gate: `make verify` (build, vet, gofmt, lint, `tidy-check`, `generate-check`, unit tests, the Store and Bus conformance suites against both SQLite and Postgres, and `check-gates`). Postgres for the suites comes from testcontainers unless `AGENTBUS2_TEST_POSTGRES_DSN` names a reachable server (an empty value counts as unset). `make verify` needs Docker either way, because `check-gates` runs the suite binaries in a `busybox` container to prove the no-Docker path. A run that skips the Postgres suites is not a pass.
- `make test` is Docker-free (every package except `internal/conformance`); `make test-conformance` runs the suites only.
- `make generate` regenerates `internal/gen` and `internal/gen/spec` from `api/openapi.yaml`.
- `make test-conformance` runs the bus suite twice: `go test ./internal/conformance/...` (the `local` transport), then the `bus` package with `AGENTBUS2_BUS_TRANSPORT=inproc` (the remote `Bus` over the generated client and the in-process transport). Any other value fails the suite.
- Run the server: `agentbus2 server [-config FILE] [-listen host:port|unix:PATH] [-registration open|enrollment|closed] (-sqlite <path> | -postgres <dsn>) [-set name=value]...`; settings come from the file, then `AGENTBUS2_SERVER_<NAME>`, then the flags (`internal/server/config`, `config.Settings()` lists them). The registration default is `open` on a loopback or unix listener and `enrollment` on any other.
- Host commands (no listener, no credential): `agentbus2 server admin [-config|-sqlite|-postgres|-set] make-admin NAME | reset-credential NAME | create-identity NAME | enrollment create|list|revoke`.
- `scripts/m4-exit-checks.sh <local.log> <inproc.log>` checks the Milestone 4 exit counts and the import rule; `--deps-only` runs the import rule alone.
- `scripts/m5-exit-checks.sh <auth.log>` checks the `TestAuthEndToEnd` counts (11 leaf subtests per engine) and the Milestone 5 import rule; `--deps-only` runs the import rule alone.
- `make check-gates-selftest` tests the gate scripts themselves (minutes; CI runs it as its own job).

## Design

- The design decisions (made by Eric) go in the spec under `docs/superpowers/specs/` and the ADRs in `docs/adr/`. Read them before changing an interface, the Store contract, auth or the wire protocol.
- The Store contract is `internal/store`; the engines are `internal/store/sqlite` and `internal/store/postgres` over `internal/store/sqlstore`; the Store suite is `internal/conformance/store` and isolates Postgres tests in a schema per test.
- `internal/bus` is the seam: the `Bus` interfaces and their domain types and errors; clients never import `internal/store`.
- `internal/core` holds the rules over the Store, one aggregate package each (`channels`, `sessions`, `messaging`, `memories`, `tasks`, `search`, `delivery`, `maintenance`) plus `rules` (pure ports) and `notify`; every mutation obeys the Decision 26 rules (see Global Constraints in `docs/superpowers/plans/2026-10-21-milestone-3-core-bus.md`).
- `internal/bus/local` is the in-process `Bus` bound to one identity; it delegates to the core.
- `internal/conformance/bus` is the Bus suite on both engines, including the ACL matrix (`TestACLMatrix`); `scripts/m3-exit-counts.sh` checks a `-v` log of it against the Milestone 3 exit counts.
- `api/openapi.yaml` is the wire protocol: read spec 3 and ADRs 0001 and 0007 before changing it, then `make generate` (it writes `internal/gen` and `internal/gen/spec`; never edit those by hand).
- `internal/server` holds the handlers over the core, one file per aggregate; every error goes through `WriteProblem` (problem+json), and `Authenticator` is the authentication seam (`RefuseAll` by default). `internal/server/cmd` is `agentbus2 server`.
- `internal/bus/remote` is the client side of Decision 47: a `Bus` over the generated client; it never imports `internal/gen/spec`, `internal/server`, `internal/core` or kin-openapi.
- Decision 47 import rule (checked by `scripts/m4-exit-checks.sh`): `internal/bus`, `internal/core/...` and `internal/bus/local` do not import `internal/gen` or `internal/server`; `internal/server/...` does not import `internal/bus/local` or `internal/bus/remote`.
- `internal/auth` (`keys`, `assertion`, `token`) is a leaf: it imports no `internal/store`, `internal/core`, `internal/server`, `internal/gen` or `internal/bus`, and `internal/bus/remote` never imports `internal/auth/token` (checked by `scripts/m5-exit-checks.sh`).
- `internal/core/identities` holds registration, enrollment tokens, the admin flag, reset and the signing-key rules; the host commands and the handlers both call it. `internal/server/config` is Decision 85's configuration.
- The server's token signing key is a Store row created at first start (`signing_keys`, plaintext PEM): read access to the database equals the admin flag. Rotation is issue #6, a key provider issue #8.
- `make verify` covers schema version 2 (`enrollment_tokens`, `signing_keys`) on both engines; `TestAuthEndToEnd` (`internal/conformance/bus/auth_test.go`) is the Milestone 5 exit over TCP.
- v1 code is ported deliberately: the commit message names the v1 source file. Do not port artifacts of decisions v2 changed (DMs, identity derived from the harness, `-filter`).

## Git

- Work lands directly on `main`; no PRs.
