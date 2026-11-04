# ADR 0017: Rename agentbus2 to agentmesh

Status: Accepted (Eric, 2026-11-03).

## Decision (Eric, 2026-11-03)

The product is renamed from `agentbus2` to `agentmesh`, in full (Eric chose the full rename over renaming the repository and module only, or the repository and directory only). The rename covers:

- the GitHub repository (`ericfitz/agentmesh`), the local checkout directory and the Go module path (`github.com/ericfitz/agentmesh`);
- both binaries (`agentmesh`, `agentmesh-mcponly`) and `cmd/agentmesh-mcponly`;
- every environment variable (`AGENTMESH_*`), the config and data directories (`~/.config/agentmesh`, `~/.local/share/agentmesh`), the repository file `.local/agentmesh.json`, the MCP server name and the `using-agentmesh` skill;
- the packaging names (Homebrew, Scoop, winget, the release archives and the Docker image) and every current document.

Earlier records keep the old name: the specs and plans under `docs/superpowers/`, ADRs 0001 to 0016 and their index rows, commit messages and agentbus messages. In them, "agentbus2" means "agentmesh".

No compatibility layer: there has been no release, so nothing reads the old names (Eric, 2026-10-25). Data and configuration on a machine that ran a pre-rename build are moved or recreated by hand.

## Exception (agent choice, not Eric's)

The private release signing key keeps its file name, `~/.keys/agentbus2-release-ed25519.pem`, because agents never rename or touch secret files. `release/release.sh` defaults to that path (`AGENTMESH_SIGNING_KEY` overrides it). Renaming the key file is Eric's call: rename the file, then change the default path in `release/release.sh`, `release/test-pipeline.sh` and `docs/release.md`. The public key in the repository is `release/agentmesh-release-ed25519.pub`; its contents did not change.

Resolved (Eric, 2026-11-03): Eric renamed the file to `~/.keys/agentmesh-release-ed25519.pem`, and `release/release.sh`, `release/test-pipeline.sh` and `docs/release.md` now default to that path. The key pair is unchanged.

## Consequences

- The Azure federated credential subject for Windows signing (`docs/release-signing.md`) names the repository, so it uses the new name. That credential has not been created yet.
- The v1 product, `agentbus`, is unaffected. agentmesh still installs side by side with it.
- The plan with the per-machine steps is `docs/superpowers/plans/2026-11-03-rename-agentmesh.md`.
