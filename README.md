# agentbus2

A message bus, shared memory and shared task queue that let multiple agents work together. Agents can share a local database or connect to a server.

agentbus2 is the successor to [agentbus](https://github.com/ericfitz/agentbus). It is a breaking redesign, not an upgrade: it uses its own binary, data directory, configuration and MCP server name, so it can be installed next to agentbus 1.x without touching 1.x data.

Status: Milestone 8 (TUI) done; `agentbus2 tui` runs over the Bus in local and server mode; release tooling is Milestone 9. Milestones 6 (client side) and 7 (server deployment) are done: `agentbus2 init`, `register`, `mcp`, `watch` and the CLI work against a local profile and a server, a server runs from the Docker image over SQLite or Postgres, and replicas over one Postgres share wakes, maintenance and the assertion replay set.

Docker quickstart (details in `docs/ops/deploy.md`):

```sh
make docker-image
docker run --rm -p 8420:8420 -v agentbus2-data:/data agentbus2:local -listen 0.0.0.0:8420 -sqlite /data/agentbus2.db -registration enrollment
# or two replicas over Postgres: docker compose -f deploy/docker-compose.yml up
```

## Quick start

Install the MCP server, the Stop hook (and Claude Code's SubagentStart hook) and the skill into every harness on the machine (Claude Code, Codex, Grok):

```sh
agentbus2 init --global
```

Inside a repository, `agentbus2 init` writes `.local/agentbus2.json` (`{"profile": "<repo>"}`), adds a profile named for the repository to `config.json` and git-ignores `.local/`. With no configuration the `default` profile is local: every process on the machine shares one SQLite file under the data directory (`~/.local/share/agentbus2`, or `$AGENTBUS2_DATA_DIR`).

To use a server, a profile in `~/.config/agentbus2/config.json` (or `$AGENTBUS2_CONFIG_DIR/config.json`) names it:

```json
{"profiles": {"lab": {"mode": "server", "server": "https://bus.example.com", "identity": "pat"}}}
```

`register` creates that profile when it does not exist yet, generates a key, registers its public half and saves the credential (mode 0600) only after the server accepts it. A server that listens beyond loopback needs an enrollment token from its operator (`agentbus2 server admin enrollment create`):

```sh
agentbus2 register -profile lab -server https://bus.example.com -identity pat -enrollment-token abe_...
agentbus2 login -profile lab -identity pat
agentbus2 session open -profile lab -identity pat
agentbus2 send -profile lab -identity pat general "hello"
```

`watch` and `wait` end with spec section 3.5's exit codes: `wait` exits 0 woke, 1 timeout, 2 error, 3 replaced, 4 harness gone; `watch` never exits 0 or 1. `scripts/m6-exit-checks.sh` and `internal/e2e` prove the section 2.4 sequence and that table against the built binary and a real server.

## Terminal UI

`agentbus2 tui [-profile P] [-identity I]` opens a dashboard for a human: channels in five rail sections, messages with threads, memories, task lists, search and a health overlay. It runs in local and server mode alike and has no inbox view. Keys, settings (themes, icons, rail width) and the differences from v1 are in `docs/tui.md`; press `?` inside for the keymap.

## Development

Requirements: Go 1.27, Docker, golangci-lint v2.14.0. `make verify` needs Docker even when `AGENTBUS2_TEST_POSTGRES_DSN` is set: the variable only replaces testcontainers as the suites' Postgres.

- `make verify`: the done gate (build, vet, gofmt, lint, tidy go.mod, generated-code freshness, unit tests, both conformance suites on SQLite and Postgres, gate checks).
- The server's token signing key is stored in clear in the database (the `signing_keys` row), so read access to the database is equivalent to the admin flag.
- The Postgres database must use a UTF-8 ctype (text-search normalization relies on `[[:alnum:]]` matching non-ASCII letters).
- `make generate`: regenerate `internal/gen` from `api/openapi.yaml`.
- `make check-gates`: the negative halves of the gate (unreachable Postgres, no Docker and no variable, and others), runnable alone.
- `make check-gates-selftest`: the harness's own test; mutates a throwaway worktree of `HEAD` and proves each gate check fires.
- `scripts/ci-sim.sh`: the CI `verify` job locally against a `postgres:17` container (`CI_SIM_NAME`, `CI_SIM_PORT`).
- `scripts/ci-gate-negative.sh <expected-text>`: the CI negative jobs' assertion; `make verify` must fail with that text and never print `verify: OK`.
