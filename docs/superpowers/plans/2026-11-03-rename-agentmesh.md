# Rename agentbus2 to agentmesh: plan

**Decision (Eric, 2026-11-03):** a full product rename, from `agentbus2` to `agentmesh`. It covers the GitHub repository, the local directory, the Go module path, both binaries (`agentmesh`, `agentmesh-mcponly`), the `AGENTMESH_*` variables, the config and data directories, the MCP server name, the skill, the packaging names and every current document. Old specs and ADRs (`docs/superpowers/**`, `docs/adr/0001` to `0016`) keep the old name, and CLAUDE.md says that "agentbus2" in them means "agentmesh". There is no release yet, so there are no compatibility shims (Eric, 2026-10-25) and local data is recreated or moved by hand.

Order: the ADR 0016 commit lands first; then this plan, in a single commit sequence on `main`.

## Task 1: code and current docs (one commit)

Mechanical substitution with a checked-in script, `scripts/rename-agentmesh.sh` (deleted again in the last commit of the sequence), run once from the repository root:

- Case-preserving replacements, longest first: `AGENTBUS2` to `AGENTMESH`, `Agentbus2` to `Agentmesh`, `agentbus2` to `agentmesh`. A bare `agentbus` (v1) is never touched; the patterns all end in `2`.
- Scope: every tracked file except `docs/superpowers/**`, `docs/adr/0001`-`0016`, `internal/gen/**` (regenerated) and `go.sum`.
- `git mv` for the paths with the name in them: `cmd/agentbus2-mcponly`, `internal/cli/skills/using-agentbus2`, the `release/agentbus2*.tmpl` files and `release/agentbus2-release-ed25519.pub`.
- `go.mod` module path `github.com/ericfitz/agentmesh`; then `make generate` and `go mod tidy`.
- Exception: the private release signing key stays `~/.keys/agentbus2-release-ed25519.pem` (secret files keep their original names). The script restores that one reference, and the plan asks Eric whether to rename the key file himself.
- New `docs/adr/0017-rename-to-agentmesh.md` records the decision as Eric's, and `docs/adr/README.md` lists it.
- CLAUDE.md: the substitution plus the line "Before 2026-11-03 this product was named agentbus2; in older docs, specs, ADRs, commits and agentbus messages, read agentbus2 as agentmesh."
- Checks: `rg -n -i 'agentbus2' . -g '!docs/superpowers/**' -g '!docs/adr/**'` finds only the CLAUDE.md alias line, the ADR 0017 text and the signing key path; `make verify` passes; the gate scripts (`scripts/m*-exit-checks.sh`, `check-gates-selftest`) pass with the new names.

## Task 2: GitHub (outward-facing; needs `Approve: rename GitHub repo ericfitz/agentbus2 to ericfitz/agentmesh and push main`)

1. `gh repo rename agentmesh --repo ericfitz/agentbus2 --yes` (GitHub redirects the old URL).
2. `git remote set-url origin git@github.com:ericfitz/agentmesh.git`.
3. Push `main`; confirm `ci.yml` and `windows.yml` are green on the pushed commit.

## Task 3: this machine (after the push, at the end of the session)

Run by Eric or by a final session step, because the session's working directory moves:

- `mv ~/Projects/agentbus2 ~/Projects/agentmesh`, and move `~/.claude/projects/-Users-efitz-Projects-agentbus2` to `-Users-efitz-Projects-agentmesh` (auto-memory).
- `.local/repos.json`: key and `path`; `.local/agentbus.json` keeps identity `agentbus2` on v1 unless Eric renames it (v1 channels `general/agentbus2` and `memory/agentbus2` hold the history).
- `~/.config/agentbus2` to `~/.config/agentmesh`, `~/.local/share/agentbus2` to `~/.local/share/agentmesh` (with `agentbus2.sqlite` renamed to the new default file name), `~/.local/bin/agentbus2` to `agentmesh` (copy to a temp name, then `mv`: macOS kills a binary overwritten in place).
- `~/.claude/settings.json` global hooks (`agentbus2 session-start-hook`, `stop-hook`, `subagent-hook`, `unpark`) and the user MCP entry `agentbus2` become `agentmesh`; or `agentmesh init -global` rewrites them.
- The local Docker stack (`.local/docker`) is rebuilt with `make docker-image` under the new names.
- Tell the v1 `agentbus` agent by direct message.

## Second run, overlap and leftovers

- The script is idempotent: a second run finds no `agentbus2` in scope and changes nothing; `git mv` is skipped for paths already moved.
- A failed run leaves a dirty tree; `git checkout -- . && git clean -fd -- cmd internal release` restores it, and the script is run again.
- The GitHub rename is a single API call; a second call on the renamed repo fails with "not found" and changes nothing.
