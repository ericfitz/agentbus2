## agentbus2 2.0.0

agentbus2 is a message bus, shared memory and shared task queue that let multiple agents work together. Agents can share a local database or connect to a server.

agentbus2 is the successor to [agentbus](https://github.com/ericfitz/agentbus). It is a breaking redesign, not an upgrade. It has its own binary (`agentbus2`), data directory, configuration, MCP server name and environment prefix (`AGENTBUS2_`), so it installs beside agentbus 1.x and shares nothing with it. There is no migration from 1.x, and nothing in agentbus2 or its installers reads, replaces or removes a 1.x binary or database.

### What is in 2.0.0

- A server (`agentbus2 server`) over SQLite or Postgres, defined by an OpenAPI 3.1 schema. Several replicas can serve one Postgres database. The image is published as `ghcr.io/ericfitz/agentbus:2.0.0` (and `:latest`); the Dockerfile and a Compose file (`deploy/docker-compose.yml`) are in the repository.
- Authentication with Ed25519 key pairs (OAuth `private_key_jwt`), trust-on-first-use registration, enrollment tokens for servers that listen beyond loopback, a default authorization policy and per-channel access control lists, and `agentbus2 server admin` for the operator.
- A client side that works in two modes: a local profile over a shared SQLite file, and a server profile. The CLI, the stdio MCP server (`agentbus2 mcp`), the Stop and SubagentStart hooks, `watch` and `wait`, and `agentbus2 init --global` for Claude Code, Codex and Grok.
- A terminal UI (`agentbus2 tui`) that works in both modes.
- Release tooling: a signed, notarized macOS universal binary, Linux and Windows archives built in GitHub Actions with build attestations, an Ed25519-signed `SHA256SUMS`, the installers below, a Homebrew formula, a Scoop manifest and a winget package.

### Install

macOS (Homebrew), Linux and Homebrew on Linux:

```sh
brew install ericfitz/tap/agentbus2
curl -fsSL https://github.com/ericfitz/agentbus2/releases/latest/download/install.sh | sh
```

Windows:

```powershell
irm https://github.com/ericfitz/agentbus2/releases/latest/download/install.ps1 | iex
scoop bucket add ericfitz https://github.com/ericfitz/scoop-bucket
scoop install agentbus2
```

`winget install ericfitz.agentbus2` works once the winget package is published. The installers verify the Ed25519-signed `SHA256SUMS` and the archive's hash; the Linux and Windows archives also carry build attestations (`gh attestation verify`). Details, verifying by hand and the environment variables are in [docs/install.md](https://github.com/ericfitz/agentbus2/blob/v2.0.0/docs/install.md).

Then, once per machine, `agentbus2 init --global`, and `agentbus2 init` inside each repository.

### Known limitations

Both are described in [docs/ops/deploy.md](https://github.com/ericfitz/agentbus2/blob/v2.0.0/docs/ops/deploy.md).

- **Two servers over one SQLite file.** Do not. Both start and serve, but wake-ups are in process: a long-poll on one server is not woken by a send handled by the other, and it ends at its timeout. Use Postgres for more than one server.
- **Rate limits are per replica.** The send buckets (`send_messages_per_second`, `send_kib_per_second`) are per process, so N replicas each grant an identity the full rate.
