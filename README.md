# agentmesh

A message bus, shared memory and shared task queue that let multiple agents work together. Agents can share a local database or connect to a server.

agentmesh is the successor to [agentbus](https://github.com/ericfitz/agentbus). It is a breaking redesign, not an upgrade: it uses its own binary, data directory, configuration and MCP server name, so it can be installed next to agentbus 1.x without touching 1.x data.

Status: Milestone 8 (TUI) done; `agentmesh tui` runs over the Bus in local and server mode; release tooling in place; 2.0.0 is cut by `docs/release.md`. Milestones 6 (client side) and 7 (server deployment) are done: `agentmesh init`, `register`, `mcp`, `watch` and the CLI work against a local profile and a server, a server runs from the Docker image over SQLite or Postgres, and replicas over one Postgres share wakes, maintenance and the assertion replay set.

Docker quickstart (details in `docs/ops/deploy.md`):

```sh
make docker-image
docker run --rm -p 8420:8420 -v agentmesh-data:/data agentmesh:local -listen 0.0.0.0:8420 -sqlite /data/agentmesh.db -registration enrollment
# or two replicas over Postgres: docker compose -f deploy/docker-compose.yml up
```

## Quick start

Install the MCP server, the Stop and UserPromptSubmit hooks (plus the SessionStart hook on Claude Code and Codex, and Claude Code's SubagentStart hook) and the skill into every harness on the machine (Claude Code, Codex, Grok):

```sh
agentmesh init --global
```

At the end of a session, `agentmesh park` stops anything waking the session (`wait`, `watch` and the Stop hook go quiet); the next prompt, `register` or any MCP tool call but `receive` unparks it (`agentmesh unpark` is the `UserPromptSubmit` hook that `init --global` installs).

Inside a repository, `agentmesh init` writes `.local/agentmesh.json` (`{"profile": "<repo>"}`), adds a profile named for the repository to `config.json` and git-ignores `.local/`. With no configuration the `default` profile is local: every process on the machine shares one SQLite file under the data directory (`~/.local/share/agentmesh`, or `$AGENTMESH_DATA_DIR`).

To use a server, a profile in `~/.config/agentmesh/config.json` (or `$AGENTMESH_CONFIG_DIR/config.json`) names it:

```json
{"profiles": {"lab": {"mode": "server", "server": "https://bus.example.com", "identity": "pat"}}}
```

A server profile used by a human also names the corporate provider (`oidc`: `issuer` an `https` URL, `client_id`, `redirect_port`, `scopes`; the setting is explained in `docs/tui.md`). Agents on the same profile ignore it.

The Stop hook nudges a top-level agent whose working status (`planning`, `implementing`, `reviewing`) has gone stale, and one that has no status. `hooks.status_stale_minutes` in the same file sets how old it may get, 0 to 1440 minutes, default 30; 0 turns the nudge off:

```json
{"profiles": {}, "hooks": {"status_stale_minutes": 30}}
```

`register` creates that profile when it does not exist yet, generates a key, writes it to a pending file (mode 0600, `credentials/<profile>/.pending/<identity>`), registers its public half and moves the file to the credential once the server accepts it; a run that fails leaves the pending file and the next run registers the same key. A server that listens beyond loopback needs an enrollment token from its operator (`agentmesh server admin enrollment create`):

```sh
agentmesh register -profile lab -server https://bus.example.com -identity pat -enrollment-token abe_...
agentmesh login -profile lab -identity pat
agentmesh session open -profile lab -identity pat
agentmesh send -profile lab -identity pat general "hello"
```

The idle wake on every harness except Codex is a background `agentmesh wait` (Bash `run_in_background`; exit 0: call `receive`, then start it again; exit 3 or 4: do nothing; exit 2: report). Codex has no idle wake and calls `receive` at the end of each task. `watch` stays but is deprecated in favor of the background wait (decision 17).

`watch` and `wait` end with spec section 3.5's exit codes: `wait` exits 0 woke, 1 timeout, 2 error, 3 replaced, 4 harness gone; `watch` never exits 0 or 1. `scripts/m6-exit-checks.sh` and `internal/e2e` prove the section 2.4 sequence and that table against the built binary and a real server.

An agent that only needs to reach a server can use the smaller `agentmesh-mcponly` instead (about 11 MB, no local mode, no TUI, no hooks): `serve` is its stdio MCP server, `register` creates and registers a key, `check` verifies the setup step by step, `config` prints the MCP client configuration, and `-credential FILE` reads a key from any path (mode 0600). It waits for messages with `receive` and `wait_seconds`, not a background command. Run `agentmesh-mcponly help` for a guided walk-through with examples, or `agentmesh-mcponly help <topic>` for `connect`, `register`, `configure`, `verify`, `protocol` or `troubleshoot`.

## Administrator commands

An administrator's client also has `agentmesh scopes create [-key K] NAME | list | get NAME | done -retire with_bridge|normal [-key K] NAME`, `agentmesh identities create [-scope S] NAME` (prints a one-time enrollment token for the new agent), `agentmesh channels create [-scope S] ...` and `agentmesh bridges create [-slack-name N] CHANNEL | list | get CHANNEL | remove CHANNEL` (Slack bridge; `forbidden` where none is configured) and `agentmesh signing-keys list | rotate [-revoke-previous] [-key K]` (the server's token signing key set; `unavailable` on a local profile). The walk-through is in `docs/ops/admin-bootstrap.md`.

## Terminal UI

`agentmesh tui [-profile P] [-identity I]` opens a dashboard for a human: channels in five rail sections, messages with threads, memories, task lists, search, an agents board (each agent's self-reported status and its history) and a health overlay. It runs in local and server mode alike and has no inbox view. On a server profile the human signs in through the corporate provider named by the profile's `oidc` setting (browser, PKCE); `-identity` does not apply there, and `agentmesh login -profile P -human` signs in ahead of time. Keys, settings (themes, icons, rail width) and the differences from v1 are in `docs/tui.md`; press `?` inside for the keymap.

## Install

macOS: `brew install ericfitz/tap/agentmesh`. Linux: `curl -fsSL https://github.com/ericfitz/agentmesh/releases/latest/download/install.sh | sh`. Windows: `irm https://github.com/ericfitz/agentmesh/releases/latest/download/install.ps1 | iex`, or Scoop (`scoop bucket add ericfitz https://github.com/ericfitz/scoop-bucket`, `scoop install agentmesh`). These work from the first published release (2.0.0); until then build from source with `make build`. agentmesh installs beside agentbus 1.x and touches nothing of it. The MCP-only client installs the same ways under the name `agentmesh-mcponly` (`brew install ericfitz/tap/agentmesh-mcponly`, `install.sh --mcp-only`, `install.ps1 -McpOnly`, Scoop, winget). Details, verification and the environment variables are in `docs/install.md`; cutting a release is `docs/release.md`.

## Development

Requirements: Go 1.27, Docker, golangci-lint v2.14.0. `make verify` needs Docker even when `AGENTMESH_TEST_POSTGRES_DSN` is set: the variable only replaces testcontainers as the suites' Postgres.

- `make verify`: the done gate (build, vet, gofmt, lint, tidy go.mod, generated-code freshness, unit tests, both conformance suites on SQLite and Postgres, gate checks).
- With the default key provider the server's token signing key is stored in clear in the database (the `signing_keys` rows), so read access to the database is equivalent to the admin flag. A key held by an external provider (`signing_key_uri`) never leaves the key service; the database holds only its public half.
- The Postgres database must use a UTF-8 ctype (text-search normalization relies on `[[:alnum:]]` matching non-ASCII letters).
- `make generate`: regenerate `internal/gen` from `api/openapi.yaml`.
- `make check-gates`: the negative halves of the gate (unreachable Postgres, no Docker and no variable, and others), runnable alone.
- `make check-gates-selftest`: the harness's own test; mutates a throwaway worktree of `HEAD` and proves each gate check fires.
- `scripts/ci-sim.sh`: the CI `verify` job locally against a Chainguard Postgres 18 container (`CI_SIM_NAME`, `CI_SIM_PORT`).
- `scripts/ci-gate-negative.sh <expected-text>`: the CI negative jobs' assertion; `make verify` must fail with that text and never print `verify: OK`.
