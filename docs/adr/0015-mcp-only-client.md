# ADR 0015: The MCP-only client

Status: Accepted (Eric, 2026-10-27, decisions 1 to 6 below). The "Settled open notes" and the "Controller rulings" are settled by the agents that wrote and implemented the plan and are not Eric's decisions; each open note carries an `open note: mcp-only-client N` marker at its code site so Eric can reverse it with one findable edit (`rg -n 'open note: mcp-only-client' .`).

Covers issue #12 (ericfitz/agentbus2#12). The design is `docs/superpowers/specs/2026-10-27-mcp-only-client-design.md` (section 4 holds Eric's decisions); the plan is `docs/superpowers/plans/2026-10-27-mcp-only-client.md`; the user-facing text is `agentbus2-mcponly help`, `docs/install.md` ("The MCP-only client") and `docs/release.md`.

Adds one source to ADR 0002 (the `-credential` file; see that record's addendum "The `-credential` source (Eric's decision 2026-10-27)"). Changes no other earlier ADR's decision. The sidecar mode of issue #12 is a separate issue (#21) with its own spec (`2026-10-27-mcp-sidecar-design.md`); it is postponed and nothing here implements it.

## Context

Issue #12 asked for a second, small binary whose only job is to connect an agent to an agentbus2 server over stdio MCP, so that unknown or custom agents (not Claude Code, Codex or Grok) can set it up correctly. The full binary links the core, the server, the SQLite engine, kin-openapi and the TUI because `client.Open` could reach local mode at run time: 27.0 MB and 53 modules (linux/amd64, stripped). The measured MCP-only build is 11.0 MB and 13 modules (spec section 3.6).

## Decisions (Eric, 2026-10-27)

Each of these is Eric's decision. The text is the option chosen in the design brief.

1. **Build: a separate main, `cmd/agentbus2-mcponly`, and local mode moved out of `internal/client`** (option A; Eric's decision 2026-10-27). No build tags: every file is compiled, vetted, linted and tested in every configuration. Not chosen: a build tag on the existing main (B), link-time trimming only (C), a separate Go module (D).
2. **State: the flags map onto today's profiles and files, and `-credential FILE` may read a key from any path** (option A; Eric's decision 2026-10-27). `-server` plus `-identity` select or create a server profile in `config.json`; without `-credential`, credentials and the bearer cache use the existing paths. The key file must be mode 0600 on Unix and owner-only on Windows, and is never named by the repository file. Not chosen: a profile-free model (B), the full CLI's flags only (C).
3. **Packaging: separate archives** (option B; Eric's decision 2026-10-27). `agentbus2-mcponly-vX.Y.Z-<platform>` archives (fourteen assets in all), the Homebrew formula `agentbus2-mcponly`, Scoop `bucket/agentbus2-mcponly.json`, winget `ericfitz.agentbus2-mcponly`, and `install.sh --mcp-only` and `install.ps1 -McpOnly`. Not chosen: both binaries in every archive (A).
4. **Name: `agentbus2-mcponly`** (Eric's decision 2026-10-27; he changed the brief's proposed name). The MCP server's implementation name stays `agentbus2`, so tool names do not change.
5. **Waking: `receive` with `wait_seconds`** (option A; Eric's decision 2026-10-27), documented in the help and in the MCP-only `Instructions`. The binary has no `wait`, `watch`, `park` or hooks. Not chosen: MCP server-to-client notifications (B), which is issue #13.
6. **No command: help on stderr, exit 2** (Eric's decision 2026-10-27). The binary never starts serving by itself, so an unknown agent that runs it in a terminal does not hang on stdin.

Eric's separate decision of the same day: the sidecar implementation is a separate issue (#21) and is postponed.

## What was built

- **Commands.** `agentbus2-mcponly serve|register|check|config|help [topic]|version` (`internal/mcpcli`; `cmd/agentbus2-mcponly/main.go` holds only the signal context and `os.Exit`). Flags `-server`, `-identity`, `-credential`, `-profile` on `serve`, `register`, `check` and `config`; `register` adds `-enrollment-token`, `-enrollment-token-file` and `-algorithm EdDSA|ES256`. Variables `AGENTBUS2_SERVER`, `AGENTBUS2_IDENTITY`, `AGENTBUS2_CREDENTIAL`, `AGENTBUS2_ENROLLMENT_TOKEN`, `AGENTBUS2_PROFILE`, `AGENTBUS2_CONFIG_DIR`, `AGENTBUS2_DATA_DIR`. Exit codes: 0 ok, 1 runtime (`agentbus2-mcponly: <code>: <message>` for a `bus.Error`), 2 usage or resolution.
- **Local mode is injected.** It lives in `internal/client/localmode`; `client.Options.Local` is the opener only the full binary passes, so `internal/client` imports no core, server or Store engine. With no opener, a local profile is a `ResolveError` ("this binary has no local mode"): `check` and `config` with no `-server` exit 2, and `serve`'s tools answer `validation` with that text.
- **The agent text has two editions.** `mcpserver.Edition` (`EditionFull`, `EditionMCPOnly`) selects `Instructions`, the register steps and the `init` prompt; the MCP-only text never tells an agent to run `agentbus2 wait`, `watch`, `park`, `init` or `mcp`, or to use `run_in_background`, and says to wait with `receive` and `wait_seconds`. Both editions serve the same 26 tools.
- **Guided help.** `help` prints four steps (reach, register, configure, verify); `help <topic>` covers `connect`, `register`, `configure`, `verify`, `protocol` and `troubleshoot`. The text is Go constants (`internal/mcpcli/help.go`), and `TestHelpExamples` (`internal/e2e`) runs every example against a real server.
- **Release.** `release/build-archive.sh <binary> <tag> <goos> <goarch> <outdir>` builds either binary; `release/release.sh`, `release-build.yml`, the Homebrew, Scoop and winget packages and both installers carry the second binary.

## Import rules

- **The m6 client group** (`scripts/m6-exit-checks.sh`): `./internal/client` (the package alone, not `internal/client/localmode`) imports no `internal/core/...` except `internal/core/notify` (a leaf), no `internal/server`, `internal/bus/local` or `internal/store/{sqlite,postgres,sqlstore}`. `internal/mcpserver` and `internal/cli` keep the consumer rule (no `internal/gen`, no `internal/store`).
- **The m10 rule** (`scripts/m10-exit-checks.sh`): the transitive set of `./cmd/agentbus2-mcponly` contains no `internal/core/...` (except `internal/core/notify`), `internal/{server,tui,cli,slackapi,slackbridge,bus/local,client/localmode}`, `internal/store/{sqlite,postgres,sqlstore}`, `internal/gen/spec`, pgx, kin-openapi, gorilla/mux, slack-go, `modernc.org/` or `charm.land/`; `internal/mcpcli` and the main import no `internal/{cli,gen,store}/...`; the built binary lists exactly the 13 allowlisted modules (extending the list is Eric's decision). `--deps-only` runs inside `make verify`; given an e2e log the script also counts `TestMCPOnly` (9 leaves) and `TestHelpExamples` (6 leaves). `scripts/check-gates-selftest.sh` block 33 holds the mutations, block 23 mutates the m6 client group, and block 24 mutates `internal/client/localmode/localmode.go` after the move.

## Settled open notes (agent choices, not Eric's decisions)

Each is the agents' choice where the brief left one open. Eric may reverse any of them.

1. **Opener shape.** `client.Options.Local client.LocalOpener` (a func); `localmode.Open` and `localmode.Opener`; `client.Conn.Local any`, read by `localmode.Core`. Only `Options.Config` and `Conn.Core` left `client`. Rationale: the gate forbids exactly what links the core, with the least test churn. <!-- open note: mcp-only-client 1 -->
2. **Command package.** `internal/mcpcli` holds every command, so it is vetted, linted and unit-tested like `internal/cli`. <!-- open note: mcp-only-client 2 -->
3. **`-server` on an undefined profile.** `serve`, `check` and `config` use an in-memory server profile and write nothing; only `register` creates the profile in `config.json`, as `register -server` did before. This narrows decision 2 (option A says the flags "select or create a profile"): a read-only command never edits configuration. Eric may reverse it. <!-- open note: mcp-only-client 3 -->
4. **`-credential` is read-only.** `register -credential FILE` registers that key's public half and writes neither a credential file nor a pending file; `config.json` never records the path; it is a `ResolveError` with a `credential_process` profile, a local profile or a human sign-in. Rationale: a mounted secret is owned by its mount. <!-- open note: mcp-only-client 4 -->
5. **Windows owner-only read check.** `fileacl.CheckOwnerOnly` refuses a NULL DACL and any access-allowed entry for a SID other than the current user, LocalSystem (`S-1-5-18`) and BUILTIN\Administrators (`S-1-5-32-544`); it runs for `-credential` files only. Rationale: those two SIDs are Windows' root, which reads a 0600 file on Unix too. <!-- open note: mcp-only-client 5 -->
6. **Decision 16 applies to `serve`.** The OS-login guard moved to `mcpserver.IdentityGuard`, and both binaries call it; the message names the command (`agentbus2 mcp` or `agentbus2-mcponly serve`). <!-- open note: mcp-only-client 6 -->
7. **Tool schemas are shared.** Both editions serve the same 26 tools with the same descriptions (the `wait_seconds` and `set_status` descriptions keep their sentences about `agentbus2 wait` and `agentbus2 park`, which say what they replace, not what to run); only `Instructions`, the register steps and the `init` prompt differ by edition. An edition-aware description is the alternative Eric may choose. <!-- open note: mcp-only-client 7 -->
8. **`check` without an identity** runs `reach` and `health`, prints `next: agentbus2-mcponly register -server URL -identity NAME` and exits 0; with one it runs six steps (`reach`, `health`, `identity`, `credential`, `token`, `access`) and stops at the first `FAIL` (exit 1), each `FAIL` naming the command that fixes it. The `token` step always exchanges a fresh bearer and caches it as `serve` does. <!-- open note: mcp-only-client 8 -->
9. **Gates.** `scripts/m10-exit-checks.sh --deps-only` runs inside `make verify` (Docker-free, seconds), unlike the m4 to m8 `--deps-only` runs, which stay in the `gates-selftest` job; the e2e additions are new tests, so the m6 counts (13 sequence steps, 11 exit-code cells) do not change. <!-- open note: mcp-only-client 9 -->
10. **Installers.** `--mcp-only` and `-McpOnly` stay flags (decision 3). Both binaries install into the same directory (`~/.local/bin`, `%LocalAppData%\Programs\agentbus2`); the Windows `.old-*` sweep is per binary name. <!-- open note: mcp-only-client 10 -->
11. **Second Windows README.** `release/windows-zip-README-mcponly.txt` ships in the MCP-only zips; the existing one tells the reader to run `agentbus2 init`. The marker is the comment on `package-windows`'s sparse checkout in `.github/workflows/release-build.yml`, because a README is shipped text. <!-- open note: mcp-only-client 11 -->
12. **Help example format.** Example lines are `  $ agentbus2-mcponly ...`, expected output lines `    > ...` (a `<...>` token matches any non-empty text), an expected non-zero exit `    (exit N)`; one grammar that the e2e test parses. <!-- open note: mcp-only-client 12 -->
13. **Enrollment token sources.** Precedence: `-enrollment-token`, `-enrollment-token-file`, `AGENTBUS2_ENROLLMENT_TOKEN`; both flags at once is exit 2; the file is trimmed of surrounding white space and must be a regular file (checked before it is opened, so a FIFO is refused), non-empty and at most 4 KiB; the token never appears in any output or log. <!-- open note: mcp-only-client 13 -->
14. **Scope.** The brief's "Out of scope (proposed)" list is adopted: no local mode, no `login -human`, no TUI, no hooks, no `wait`, `watch` or `park`, no server change, no #13. `TestOnlyMCPCommands` pins the command set. <!-- open note: mcp-only-client 14 -->

## Controller rulings that changed behavior (agent choices)

The full ledger is not part of the repository; these are the rulings that differ from the plan as first written.

- The tool count is 26, not 25 (`set_status` came with ADR 0014). The MCP-only register steps carry a `set_status` sentence without the park clause.
- The MCP-only help `protocol` topic includes a short Status section and the `inspection_rejected` paragraph, since the skill it abbreviates gained both.
- `help [topic]` takes at most one topic: an unknown topic or a second argument is exit 2; `version` rejects any argument.
- `internal/e2e` carries `//go:build !windows`, so `TestMCPOnly` and `TestHelpExamples` do not run on Windows; the unit tests do. Windows `-credential` tests call `fileacl.OwnerOnly` after writing the key, because `keys.WriteFile` sets no DACL.
- Where the provisional spec section 7 differs from the plan (the ADR number, the Make target name, extending the m6 counts), the plan stands.

## Consequences

- Two binaries, `agentbus2` and `agentbus2-mcponly`, installed side by side; a release holds fourteen assets and `SHA256SUMS` covers ten archives and the two installers. The archives are built without a key-provider tag.
- A server-only agent machine downloads about 4 MB instead of about 10 MB (gzip) and links 13 modules.
- `internal/client` can no longer open local mode by itself; every caller that wants it passes `client.Options.Local` (`internal/client/localmode`), and a test that forgets gets a `ResolveError`.
- Without a background shell an agent waits with `receive` and `wait_seconds`; there is no idle wake and no Stop hook in this binary.
- The sidecar (issue #21) is postponed; this ADR builds nothing toward it.
- Verification on Windows (the `windows.yml` run and the PowerShell installer cases) happens after the push, because the development host has no PowerShell and no Windows.
