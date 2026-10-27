# MCP-only client build, simplified CLI and guided help: draft design brief

## 1. Status

- Draft of 2026-10-27, written by an agent.
- Nothing in it is a decision until Eric marks it so.
- **Eric reviewed this brief on 2026-10-27:** all 6 of its decisions are **decided** and none is open. Decisions 1, 2, 3, 5 and 6 are ratified as recommended; decision 4 (the binary name) was changed by Eric to `agentbus2-mcponly`, and the brief is renamed to match throughout.
- Issue: #12 ("feat: mcp-only client build with simplified CLI, guided help and sidecar mode"). Related: #13 (MCP events).
- **Eric's decision 2026-10-27:** the sidecar implementation is a separate issue with a separate spec that depends on this one, and it is postponed. Everything sidecar-specific moved to `2026-10-27-mcp-sidecar-design.md`; this brief covers the MCP-only binary, the simplified flags and the guided help.
- Baseline: `main` at 9fc48e7. The measurements in section 3.6 were made on a copy of the tree in the scratchpad. The repository was not changed.

## 2. Problem and scope

Issue #12 asks for a second, small binary whose only job is to connect an agent to an agentbus2 server over MCP. Unknown or custom agents (not Claude Code, Codex or Grok) should be able to set it up correctly. (The sidecar part of the issue moved to `2026-10-27-mcp-sidecar-design.md`.)

Acceptance criteria, restated from the issue:

1. A separate build target produces the MCP-only binary, and the release tooling packages it. The binary has no server, no TUI, no host commands and no harness hooks.
2. The command-line switches cover only what an MCP client needs: server URL, identity, credential and enrollment.
3. The help is simpler but more complete. It walks an unknown agent through register, configure and verify, with examples.
4. Sidecar mode and its tests (criteria 4 and 5 of the issue): moved to `2026-10-27-mcp-sidecar-design.md`, section 2.

The issue's notes on the non-stdio MCP transport, mutual TLS and the sidecar's upstream identity also moved to `2026-10-27-mcp-sidecar-design.md`, section 2.

Out of scope (proposed):

- Local (SQLite) mode in the MCP-only binary. The issue says "connect ... to an agentbus2 server".
- Human sign-in (`login -human`) and the TUI.
- The idle-wake hooks (`wait`, `watch`, `stop-hook`, `park`). Their replacement for unknown agents is section 4.7.
- Server-side changes.
- MCP events (#13). Section 4.7 only notes where they would fit.

## 3. What exists today

### 3.1 One binary, two dispatchers

- `main.go:33-38` sends `server` to `internal/server/cmd.Run` and everything else to `cli.Main`. There is no `cmd/` directory.
- `make build` is `go build -trimpath -o agentbus2 .` (`Makefile:5-6`).
- `cli.Main`'s table holds 26 commands, including `init`, `register`, `login`, `mcp`, the hooks, the admin groups and `tui` (`internal/cli/cli.go:24-50`). The usage text is one line followed by `OpGrammar` (`internal/cli/cli.go:17`).
- `runMCP` takes only `-profile` and `-identity` (`internal/cli/cli.go:76-95`). It refuses an identity equal to the OS login name, Decision 16 as CLAUDE.md numbers it (`internal/cli/cli.go:101-111`).

### 3.2 The client pulls in the whole server for local mode

- `client.Open` switches on the profile mode and calls `openLocal` or `openServer` (`internal/client/conn.go:30-42`).
- `openLocal` imports `internal/core/...`, `internal/server`, `internal/store/sqlite` and `internal/store/sqlstore` (`internal/client/local.go:15-25`).
- `client.Options.Config` is a `*core.Config` (`internal/client/options.go:58`) and `client.Conn.Core` is a `*core.Core` (`internal/client/conn.go:19`). So even a server-only caller links the core.
- `openServer` builds the agent's path: the credential file or `credential_process`, `credential.Source`, and the refreshing transport over the generated client (`internal/client/server.go:22-76`).
- `internal/credential` imports `internal/store` (the contract package, not an engine) only for `store.FileNameRule` (`internal/credential/store.go:16,32`).

### 3.3 The MCP server is stdio only and assumes a parent harness

- `mcpserver.Run` serves `&mcp.StdioTransport{}` (`internal/mcpserver/run.go:73`).
- `NewServer` looks up the harness through the process tree (`internal/mcpserver/server.go:68-72`). It opens one `client.Conn` (`internal/mcpserver/server.go:122`), so it serves one profile and one identity.
- A hintless `register` resumes the harness's main session only when a harness was found (`internal/mcpserver/sessions.go:40-43`).
- The initialize `Instructions` and `RegisterSteps` tell the agent to "run agentbus2 wait with Bash run_in_background" (`internal/mcpserver/server.go:75,88`). That text is wrong for a binary without `wait`, and wrong for an agent with no shell.
- `receive` already takes `wait_seconds`, a long poll inside the tool call (`internal/mcpserver/inputs.go:106`).

### 3.4 Authentication (ADR 0002)

- Agents use Ed25519 or ES256 key pairs with `private_key_jwt` (RFC 7523) and receive a short-lived bearer from `POST /v1/auth/token` (ADR 0002, Decision items 1-2, lines 28-34; Decisions 70 and 84, lines 122-141).
- Registration is trust on first use in `open` mode and requires an enrollment token in `enrollment` mode. The token is `abe_` plus 32 bytes, single use by default and optionally bound to a name (ADR 0002 lines 39-43 and Milestone 5 choice 9, lines 307-310).
- Credentials live at `credentials/<profile>/<identity>`, PKCS#8, mode 0600, with an owner-only ACL on Windows (ADR 0002 item 8, lines 60-64; Milestone 6 choice 5, lines 406-410). The ACL is set by `fileacl.OwnerOnly` (`internal/fileacl/acl_windows.go:11`).
- `credential_process` is version 1 JSON over stdin and stdout and is never read from the repository file (ADR 0002 lines 66-71 and 411-419; `internal/credential/process.go:74`).
- `register` takes `-server`, `-enrollment-token` and `-algorithm EdDSA|ES256` (`internal/cli/register.go:79-81`). It writes a pending key before the request and commits it after the 201, so a rerun is idempotent (`internal/cli/register.go:136-190`).
- When the profile is undefined, `register -server URL` creates it (`internal/cli/register.go:110-112`).
**3.5 Decisions a sidecar runs into:** moved to `2026-10-27-mcp-sidecar-design.md`, section 3.1. The fact in section 3.4 that the server terminates no TLS is its section 3.2, and the SDK transport fact from section 3.3 is its section 3.3.

### 3.6 Measured binary size and dependencies (go1.27.3)

Method:

- The full binary was built from `main` with `CGO_ENABLED=0 -trimpath`, unstripped as `make build` does it, and stripped (`-s -w`) as `release/build-archive.sh:41-42` does it.
- The prototypes lived only in the scratchpad copy:
  - **CLI-only main**: `cli.Main` without `server/cmd`.
  - **MCP main, no split**: `mcpserver.Run`, `client.Register` and `client.Open` only.
  - **MCP main with local mode split out**: the same main, with a build tag that removes `internal/client/local.go` and the `core` field types.
- Module counts are the `dep` lines of `go version -m`.

| Build | darwin/arm64 stripped | linux/amd64 stripped | windows/amd64 stripped | linux/amd64 gzip -9 | Modules |
|---|---|---|---|---|---|
| Full `agentbus2` (unstripped: 37,262,066 B) | 25,809,266 | 26,984,608 | 27,385,344 | 9,653,717 | 53 |
| CLI-only main (drops `server/cmd`) | 21,225,602 | 22,372,512 | 22,653,440 | 8,371,420 | 50 |
| MCP main, no package split | 18,152,018 | 19,181,728 | 19,409,408 | 7,512,109 | 33 |
| MCP main, local mode split out | 10,470,226 | 10,997,920 | 11,362,304 | 4,254,756 | 13 |

What the numbers show:

- Stripping, the "link-time trimming" option, is already done by the release build. On its own, without a new main and a package split, it saves nothing more.
- The linker cannot drop the core, the server, kin-openapi or the SQLite engine while `client.Open` can still reach `openLocal` at run time. The MCP main without the split still links 33 modules, including `modernc.org/sqlite`, `jackc/pgx/v5` (pulled in through `sqlstore`), `getkin/kin-openapi` and `gorilla/mux`.
- With local mode split out, the binary is about 59% smaller than the full one (linux/amd64 stripped: 10,997,920 against 26,984,608 bytes) and 56% smaller gzipped. It has 13 modules: `go-jose`, `mcp go-sdk`, `jsonschema-go`, `oapi-codegen/runtime`, `go-jsonmerge`, `google/uuid`, `segmentio/{asm,encoding}`, `uritemplate`, `x/oauth2`, `x/sync`, `x/sys` and `x/time`.
- The cost of the sidecar transport (streamable HTTP plus mutual TLS) is in `2026-10-27-mcp-sidecar-design.md`, section 3.4.

### 3.7 Release tooling

- One command builds each archive: `release/build-archive.sh <tag> <goos> <goarch> <outdir>`. It builds `.` and only that (`release/build-archive.sh:39-42`), with the binary name fixed as `agentbus2$ext`.
- `release.sh` has `BIN_NAME="agentbus2"` (`release/release.sh:22`). It joins the two macOS builds with `lipo` and then codesigns and notarizes them (`release/release.sh:210-229`). `SHA256SUMS` covers the archives plus the two installers (`release/release.sh:273`).
- A release has nine assets (`docs/release.md`, "What a release is"). Windows zips are Authenticode-signed through the Azure Artifact Signing action (`.github/workflows/release-build.yml:142`) and attested (`:223-224`).
- `install.ps1` replaces a running `agentbus2.exe` by renaming it to `.old-*` (`install.ps1:199-227`). That matters for an MCP binary, which is usually running during an upgrade.
- The Docker image contains only the server: `ENTRYPOINT ["/agentbus2", "server"]` (`Dockerfile`).
- `scripts/m9-exit-checks.sh` checks the release tooling's invariants and four mutations.
- `windows.yml` runs `go vet` and every unit package on windows-latest and windows-11-arm (`.github/workflows/windows.yml:16-28`).

## 4. Options

### 4.1 How to build the binary

- **A. A separate main at `cmd/agentbus2-mcponly`, with local mode moved out of `internal/client` (Recommended).**
  - Local mode moves into its own package, for example `internal/client/localmode`. `client.Open` takes local mode as an injected opener (an `Options.Local` func, or an opener the full binary passes in). With no opener, a local profile is a resolution error ("this binary has no local mode; use agentbus2").
  - `Options.Config` and `Conn.Core` move to that package.
  - Pros: no build tags, so every file is compiled, vetted, linted and tested in every configuration. It is the measured 13-module result. `go install .../cmd/agentbus2-mcponly` works.
  - Cons: about ten call sites in `internal/cli` and `internal/tui`'s entry need the opener. The M6 import rule's consumer group gains a package.
- **B. A build tag (`mcponly`) on the existing main.**
  - Pros: the smallest diff. The measurement above used exactly this.
  - Cons: two configurations of `internal/client` and `internal/cli`. `make verify`, golangci-lint, `vet-cross` and `windows.yml` would each have to run twice, or tagged code would go unchecked. CLAUDE.md's done gate warns against "default build tags only".
- **C. Link-time trimming only** (`-s -w`, `-trimpath`, dead-code elimination).
  - Already in the release build. It cannot remove what `client.Open` can reach (section 3.6). Rejected as the whole answer. It remains part of A and B.
- **D. A separate Go module** (`agentbus2-mcponly/go.mod`).
  - Pros: hard dependency isolation.
  - Cons: `internal/` packages cannot be imported across modules, so the client code would have to move to a non-internal path or be copied. Rejected.

### 4.2 The simplified flag set

The proposed commands for `agentbus2-mcponly`:

| Command | Purpose |
|---|---|
| `serve` | stdio MCP server; today's `agentbus2 mcp` |
| `register` | create a key and register it |
| `check` | verify the setup end to end |
| `config` | print an MCP client configuration snippet; writes nothing |
| `help [topic]` | guided help (section 4.3) |
| `version` | print the version |

With no command, the binary prints help to stderr and exits 2. It does not start serving: an unknown agent that runs it in a terminal should not hang on stdin. `help`, `version` and the exit codes keep today's contract: 0 ok, 1 runtime error with `agentbus2-mcponly: <code>: <message>`, 2 usage.

Common flags:

| Flag | Meaning | Environment variable |
|---|---|---|
| `-server URL` | server base URL | `AGENTBUS2_SERVER` (new) |
| `-identity NAME` | identity | `AGENTBUS2_IDENTITY` (exists) |
| `-credential FILE` | an existing PKCS#8 key file, such as a mounted secret | `AGENTBUS2_CREDENTIAL` (new) |
| `-enrollment-token-file FILE` (also `-enrollment-token T`) | the token, `register` only | `AGENTBUS2_ENROLLMENT_TOKEN` (new) |
| `-algorithm EdDSA\|ES256` | key algorithm, `register` only | none |

A token on the command line shows up in process listings and shell history, so the file and the variable are the preferred forms.

Where the flags store state:

- **A. Flags map onto today's files (Recommended).**
  - `-server` plus `-identity` select or create a server profile in `config.json`, the way `register -server` already does (`internal/cli/register.go:110-112`). The profile name defaults to `default`, which `register -server` already turns into a server profile; `-profile` stays as an advanced flag.
  - Without `-credential`, credentials and the bearer cache use the existing paths.
  - Pros: Decisions 21, 22, 71 and 74 hold unchanged, and the full binary can use the same profile later.
  - Cons: a machine where the human uses the full binary's built-in local `default` and the MCP-only binary at once must pass `-profile`.
- **B. Profile-free.** Flags and environment variables fully specify the connection. Nothing is read from `config.json`, and the token cache is keyed by server and identity.
  - Pros: the simplest model for containers.
  - Cons: a second resolution order next to Decision 21's. Changes ADR 0002 item 6 for this binary.
- **C. Keep the full CLI's flags** (`-profile`, `-identity` only).
  - Fails acceptance criterion 2.

`-credential FILE` is an addition under any option. ADR 0002 item 8 and Milestone 6 choice 5 put every credential under `credentials/<profile>/<identity>`. Reading a key from an arbitrary path adds a source (still mode 0600 on Unix and owner-only on Windows, and still never from the repository file), so Eric should approve it (Decision 2). `credential_process` stays a `config.json` setting. A `-credential-process` flag is possible but not proposed.

### 4.3 Guided help for an unknown agent

The reader may be a person or a language model with no prior knowledge. The help should be deterministic plain text that can be acted on line by line.

- `help` prints the four steps with copy-paste examples, each with its expected output and the next command:
  1. Reach the server: `check -server URL` (discover and health, no identity).
  2. Register: `register -server URL -identity NAME [-enrollment-token-file F]`.
  3. Configure the agent: `config -server URL -identity NAME` prints the binary's absolute path and a generic `{"mcpServers": {"agentbus2": {"command": ..., "args": ["serve", ...]}}}` block, plus the bare command line for clients with another format.
  4. Verify: `check` exchanges a token and reads `health` at the identity tier.
- `help <topic>` covers `connect`, `register`, `configure`, `verify`, `protocol` and `troubleshoot`.
  - `protocol` is the agent-side contract: register, then receive and ack. It says to use `receive` with `wait_seconds` to wait when the agent has no background shell. It is a short form of the `using-agentbus2` skill, rewritten for this binary.
  - `troubleshoot` maps every refusal code `check` can report (`unauthenticated`, `forbidden` "requires an enrollment token", `not_registered`, an unreachable server, a 0600 violation) to its fix.
- `check` prints one line per step (`ok` or `FAIL`), and every `FAIL` names the command that fixes it.
- `mcpserver.Instructions` and `RegisterSteps` take the binary's name and capabilities as parameters. The MCP-only build then never tells an agent to run `agentbus2 wait` (section 3.3).

Options for where the text lives:

- **A. Go constants with golden-file tests (Recommended).** The text and the code change in the same commit. The m6 word check is extended to it.
- **B. An embedded Markdown file rendered to text.** Easier to edit, but it adds a renderer.
- **C. Only per-flag `-h` output.** Fails acceptance criterion 3.

A test runs every example command in the help against the e2e server. That keeps the examples true and matches Definition of Done E.

**4.4 Sidecar mode: transport, mutual TLS and what the server accepts:** moved to `2026-10-27-mcp-sidecar-design.md`, section 4.1 (the sidecar commands are its section 4.2).

### 4.5 Packaging and signing of the second binary

- **A. Both binaries in every existing archive.**
  - The asset count stays nine.
  - Every download carries both binaries (about 4.3 MB gzipped more on linux/amd64), and the installers, the formula, the Scoop manifest and winget gain a second binary.
- **B. Separate archives `agentbus2-mcponly-vX.Y.Z-<platform>`, five more assets for fourteen in all (Recommended).**
  - `SHA256SUMS` covers ten archives and keeps one signature.
  - `build-archive.sh` takes a binary argument (`agentbus2` or `agentbus2-mcponly`) and builds `.` or `./cmd/agentbus2-mcponly`.
  - `install.sh` and `install.ps1` take `--mcp-only` / `-McpOnly`.
  - Homebrew gets a second formula, `agentbus2-mcponly`, in the same tap; Scoop gets `bucket/agentbus2-mcponly.json`; winget gets `ericfitz.agentbus2-mcponly`.
  - Unknown agents download about 4 MB instead of about 10 MB.
- The sidecar container image option moved to `2026-10-27-mcp-sidecar-design.md`, section 4.3.

Signing is the same under every option:

- **macOS.** `release.sh` loops over the binaries: `lipo`, `codesign --options runtime`, and one notarization submission per archive. `BIN_NAME` (`release/release.sh:22`) becomes a list.
- **Windows.** The `package-windows` job signs both executables through Azure Artifact Signing and attests both. The certificate profile still waits on organization validation (`docs/release.md`, "Before 2.0.0").
- **Linux.** Attested like the existing archives.
- `scripts/m9-exit-checks.sh` gains the new asset names and a mutation: an archive that holds the wrong binary.

### 4.6 Windows support

- **Stdio `serve`.** `procs_windows.go` already finds the harness. Credentials use `fileacl.OwnerOnly`. The `%APPDATA%` and `%LOCALAPPDATA%` paths apply (ADR 0002 lines 382-388).
- **Sidecar on Windows:** moved to `2026-10-27-mcp-sidecar-design.md`, section 4.4.
- **Upgrades.** A running `agentbus2-mcponly.exe` is locked, so `install.ps1`'s `.old-*` rename (`install.ps1:227`) must also apply to the new binary name.
- **CI.** `windows.yml` already runs every unit package, so the new tests run there on amd64 and arm64.

### 4.7 Waking an agent that has no `wait`

- **A. `receive` with `wait_seconds` (exists today, Recommended for this issue).** The help's `protocol` topic and the parameterized `Instructions` say so.
- **B. MCP server-to-client notifications over the streamable HTTP SSE stream.** That is issue #13. It is left there. (The note on keeping the sidecar transport stateful is in `2026-10-27-mcp-sidecar-design.md`, section 4.5.)

## 5. Decisions for Eric

1. **How is the binary built?** **DECIDED: A (Eric's decision 2026-10-27)**
   - A. `cmd/agentbus2-mcponly` plus local mode moved out of `internal/client`. Measured at 11.0 MB stripped and 13 modules, against 27.0 MB and 53 modules (linux/amd64).
   - B. A build tag.
   - C. Stripping only.
   - Recommendation: A.
2. **Where do the simplified flags keep state, and may `-credential FILE` read a key from any path?** **DECIDED: A (Eric's decision 2026-10-27)** The second half adds a credential source next to ADR 0002 item 8.
   - A. Map onto today's profiles and files, and allow `-credential FILE` (0600 / owner-only, never from the repository file).
   - B. Profile-free flags and environment variables.
   - C. Today's paths only, no `-credential`.
   - Recommendation: A.
3. **How is it packaged?** **DECIDED: B (Eric's decision 2026-10-27)**
   - A. Both binaries in every archive.
   - B. Separate `agentbus2-mcponly` archives (fourteen assets) and separate brew, Scoop and winget packages.
   - Recommendation: B. (The sidecar container image, the old option C, is Decision 7 of `2026-10-27-mcp-sidecar-design.md`.)
4. **What is the binary called?** **DECIDED: `agentbus2-mcponly` (Eric's decision 2026-10-27), changed from the recommendation.** The name is used for `cmd/agentbus2-mcponly`, the archives, the Homebrew, Scoop and winget packages and the help text.
    - A. The name without the `only` suffix (`agentbus2` plus `-mcp`): superseded by Eric's decision 2026-10-27 (it was the recommendation; the brief's text now says `agentbus2-mcponly` everywhere).
    - B. `agentbus2-agent`: not chosen.
    - C. `ab2mcp`: not chosen.
    - Eric's choice: `agentbus2-mcponly`. The MCP server's name stays `agentbus2`, so tool names do not change.
5. **How does an agent without `wait` learn that messages arrived?** **DECIDED: A (Eric's decision 2026-10-27)**
    - A. `receive` with `wait_seconds`, documented in the help and `Instructions`.
    - B. Wait for #13.
    - Recommendation: A.
6. **What does running the binary with no command do?** **DECIDED: A (Eric's decision 2026-10-27)**
    - A. Help on stderr, exit 2.
    - B. Serve stdio.
    - Recommendation: A. It prevents a silent hang, and MCP configs pass `serve`.

Decisions 1, 2, 4, 5, 7 and 8 of the earlier draft, and the container image half of Decision 9, moved to `2026-10-27-mcp-sidecar-design.md`, section 5. The remaining decisions were renumbered 1 to 6.

## 6. Dependencies

- **Libraries.** None new.
  - `go-jose/v4` v4.1.6 stays the only JOSE library.
- **Issues.** None. #13, #6 and #8 relate only to the sidecar; see `2026-10-27-mcp-sidecar-design.md`, section 6.
- **External.**
  - Azure Artifact Signing organization validation and the Apple Developer conversion gate signed releases, not the build (`docs/release.md`, "Before 2.0.0").
- **CI.**
  - `ci.yml` builds `./cmd/agentbus2-mcponly` and runs the new import and dependency check.
  - `release-check`, `release-lint`, `release-install-check` and `m9-exit-selftest` cover the second binary.
  - `windows.yml` runs the new unit tests.
  - No new Docker requirement beyond `make verify`'s.

## 7. Implementation outline (provisional)

Tasks, in order. Each starts with failing tests.

1. **ADR and spec.**
   - New ADR 0012 "MCP-only client".
   - Addendum to ADR 0002 (the `-credential` source, if Decision 2 is A).
   - Gate: Eric's approval.
2. **Move local mode out of `internal/client`.**
   - Files: `internal/client/{conn,options,local}.go` move to a new `internal/client/localmode`, plus the `client.Open` callers in `internal/cli`, `internal/tui`'s entry and `internal/mcpserver`.
   - Tests:
     - existing client, CLI, TUI and e2e tests unchanged and green;
     - a local profile with no opener is a `ResolveError` (rejecting an unrecognized state, Definition of Done B).
   - Gates: `make verify`; `scripts/m6-exit-checks.sh` and `scripts/m8-exit-checks.sh` (the TUI allowlist must not grow).
3. **`cmd/agentbus2-mcponly` with `serve`, `register`, `version`, help stubs; a `make build-mcp` target.**
   - Tests: `main_test.go`-style dispatch tests.
   - A new transitive check: `go list -deps ./cmd/agentbus2-mcponly` must not contain `internal/core` (only the `core/notify` leaf is allowed), `internal/server`, `internal/store/{sqlite,postgres,sqlstore}`, `internal/tui`, `internal/slack*`, `internal/gen/spec`, pgx, modernc or kin-openapi. A module allowlist is checked against `go version -m` of the built binary.
   - Gate: a new `scripts/m10-exit-checks.sh --deps-only` in `make verify`, with a mutation in `check-gates-selftest.sh`.
4. **Simplified flags and `-credential`.**
   - Files: the new command files under `cmd/agentbus2-mcponly` (or `internal/mcpcli`), and `internal/client/options.go` for the credential path.
   - Tests:
     - flag resolution order;
     - a 0644 key file refused on Unix, owner-only ACL on Windows;
     - an enrollment token read from a file and from the variable, never echoed.
5. **`check` and `config`.**
   - Tests against the e2e server: each `FAIL` line and its fix.
   - Gate: the `internal/e2e` counts in `scripts/m6-exit-checks.sh` (add steps).
6. **Guided help and parameterized `Instructions`.**
   - Golden files for every topic.
   - Every example command is executed against the e2e server.
   - The m6 word check is extended to the new texts.
   - `mcpserver` tests assert that the MCP-only `Instructions` never mention `agentbus2 wait`.
7. **Release tooling.**
    - `build-archive.sh <binary>` and its self-test, `release.sh` binary loop, `release-build.yml`, `install.sh` and `install.ps1` flags and harnesses, the brew, Scoop and winget templates, and `scripts/m9-exit-checks.sh`.
    - Gates: `make release-check`, `make release-lint`, `make release-install-check`, `m9-exit-selftest`.
8. **Docs.**
    - `docs/install.md` and `docs/release.md` (fourteen assets).
    - README.
    - The CLAUDE.md command list.

Import edges:

- `cmd/agentbus2-mcponly` imports `internal/client`, `internal/credential`, `internal/auth/keys`, `internal/mcpserver`, `internal/profile` and `internal/version`. None is forbidden by the Decision 47, Milestone 5, 6 or 7 rules.
- `internal/client/localmode` takes the core, server and SQLite imports that `internal/client` has today. That is allowed: it is not in the Milestone 6 leaf group and is not `internal/bus/remote`.

Wire changes: none. No `api/openapi.yaml` change, no `version.Protocol` bump (it stays `1.0.0`) and no `make generate`.

Docs and ADRs: ADR 0012 (new); an addendum to ADR 0002; `docs/install.md`, `docs/release.md`, README and CLAUDE.md.

## 8. Risks and test strategy

- **Replicas.** Not affected. The MCP-only binary is a client, and server replicas are unchanged.
- **Both engines and both transports.** The Store and Bus conformance suites are unchanged and still run on SQLite and Postgres, `local` and `inproc`. The binary's tests use the e2e server (SQLite). Postgres adds nothing on the client side, and the brief states that rather than claiming coverage.
- **Second run, overlapping runs, leftovers (Definition of Done D).** `register` already handles a second run and overlap through its pending file.
- **Windows CI.** Owner-only ACL assertions reuse `acl_windows_test.go`'s helpers. `windows.yml` must be green on amd64 and arm64.
- **Help drift.** Prevented by executing every example (section 4.3).
- The sidecar risks (client-certificate support, concurrency, security, session resumption) moved to `2026-10-27-mcp-sidecar-design.md`, section 8.

## 9. Size estimate

**M** (was **L**): a package split across `internal/client` callers, a new binary and release-tooling changes on three platforms. The TLS sidecar, with its certificate lifecycle and state tests, was the reason for L and moved to `2026-10-27-mcp-sidecar-design.md`.
