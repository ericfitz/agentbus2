# Milestone 6: Client Side Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Everything an agent host runs: profiles in `~/.config/agentbus2/config.json` with the Decision 21 selection order and the Decision 71 identity selection; credentials as PKCS#8 PEM files under `credentials/<profile>/<identity>` replaced atomically (Decisions 22, 55) and the `credential_process` protocol with caching (Decisions 23, 56); a connect layer that builds one `bus.Bus` per profile, local mode being the Milestone 4 server called in process over the shared SQLite file with the polling notifier and no authentication (Decisions 35, 36, 68) and server mode the Milestone 5 token exchange with bearer refresh (Decisions 19, 70, 84); the `agentbus2` CLI (`init`, `login`, `register`, `session`, `send`, `receive`, `history`, `search`, `channels`, `memories`, `tasks`, `status`); the stdio MCP server `agentbus2 mcp` with hand-written tool schemas, `idempotency_key` mapped to the header (Decision 66), `wait_seconds` as `watch` then `receive` (Decision 83) and the per-session state file (Decision 78); the hooks `stop-hook` and `subagent-hook` that locate their session through the process tree and fail open within a few hundred milliseconds (Decision 41); `watch` and `wait` with the spec 3.5 exit-code table (Decision 42, v1 issue ericfitz/agentbus#38); resume within the retention window (Decision 79); the rewritten `using-agentbus2` skill and its installation. Exit (spec 10): a harness on one host with a server on another completes the section 2.4 sequence, simulated by the real binary against a server on a non-loopback-classified listener (register, token, open session, watch, send from another identity, wake, receive, ack), and `watch` and `wait` exit codes match the spec 3.5 table, each code tested, under `make verify`.

**Architecture:** Four leaf packages hold the host-side state: `internal/profile` (the settings file, the repo file through `internal/repoconfig`, the directories, the selection rules), `internal/credential` (credential files, `credential_process`, the token source and the refreshing transport), `internal/procs` (the process table and the harness walk, ported) and `internal/sessionstate` (the Decision 78 state file). `internal/client` composes them into `client.Open(ctx, Options) (*Conn, error)`: in local mode it opens the SQLite store, builds the core with `notify.NewPolling`, ensures `general`, registers the name locally, runs the maintenance loop when asked, wraps `server.New` in `server.InProcessTransport` and hands the generated client to `remote.New`; in server mode it builds a `credential.Source` (openid-configuration discovery, assertion signing or `credential_process`, `remote.Token`, a disk cache) and a client whose transport refreshes once on 401. `internal/mcpserver` and `internal/cli` only ever see `bus.Bus` through a `*client.Conn` (Decision 47); `internal/cli` holds one `Run<Command>` function per command, each with the Milestone 5 command signature, and `cli.Main` dispatches them from `main.go`. The MCP server writes the state file on every session it opens; the hooks and `watch` read it after `procs.FindHarness` and never derive identity from anything else (Decisions 14, 41, 78).

**Tech Stack:** Go 1.27, `CGO_ENABLED=0`; `github.com/modelcontextprotocol/go-sdk` v1.7.0 (the version spec section 9 names and v1 is proven on; choice 11); `golang.org/x/sys` v0.48.0 promoted from indirect to direct (procs on darwin and windows); the Milestone 5 `internal/auth/keys` and `internal/auth/assertion`, `internal/bus/remote` (`Register`, `Token`, `WithBearer`), `internal/server` (`New`, `InProcess`, `InProcessTransport`), `internal/core/identities` (`RegisterLocal`), `internal/core/maintenance`, `internal/core/notify`. No other new module.

**Spec:** `docs/superpowers/specs/2026-10-19-agentbus2-design.md` sections 1.3, 2.1 to 2.4, 3.3 to 3.5, 4.1, 4.3 to 4.5, 5.5, 6.1 to 6.4, 8, 9 and 10 (Milestone 6); ADR 0001 (Decisions 3, 10, 35, 42, 45, 46, 47, 66, 68, 83), ADR 0002 (Decisions 19, 21, 22, 23, 33, 36, 55, 56, 63, 70, 71, 74, 84), ADR 0003 (Decisions 12, 14, 15, 16, 17, 37, 38, 39, 40, 41, 44, 75, 78, 79), ADR 0005 (Decisions 31, 32, 33), ADR 0006 (no direct messages anywhere in the client, the skill or the hooks), ADR 0007 (the generated client), ADR 0008 (ACL tools); the codegen spike need 6 (the in-process transport this milestone wires for the local profile); the Milestone 3 plan (`core`, `notify`, `sessions`, `maintenance` names), the Milestone 4 plan revision 3 (`server`, `remote`, `gen`, `cmd` names; its Task 10 helper pattern) and the Milestone 5 plan revision 3 (`keys`, `assertion`, `identities`, `remote.Register`/`Token`/`WithBearer`, `cmd.Run` with `environ`, `cmd.RunAdmin`, the openid-configuration document). v1 (`/Users/efitz/Projects/agentbus`, `v1.15.0`): `main.go`, `internal/config/config.go`, `paths_unix.go`, `paths_windows.go`, `internal/repoconfig/repoconfig.go`, `internal/procs/*.go`, `internal/cli/harness.go`, `wait.go`, `watch.go`, `stophook.go`, `subagenthook.go`, `init.go`, `status.go`, `delete.go`, `internal/mcpserver/server.go`, `logfile.go`, `internal/cli/skills/using-agentbus/SKILL.md`, v1 ADR 0021.

**Evidence gathered while planning (2026-10-21):** `main` holds Milestones 1 and 2 and Milestone 3 through its Task 5 (`internal/core/sessions`, `internal/core/channels`, `internal/bus/local` with stubs); nothing of Milestones 4 and 5 is on `main`, so every `server`, `remote`, `gen`, `cmd`, `keys`, `assertion` and `identities` name below is assumed from those plans and diffed at the external gate. `internal/core/sessions/open.go` on `main`: `Open` with a resume token that names a live or lapsed retained row revives it, renews the lease, leaves cursors and the pending token untouched and returns `Pending` per subscription (live messages above the cursor, capped at `ReceiveMaxCount`; the unacked batch therefore counts), which is the side-effect-free peek the Stop hook uses (choice 7). `internal/core/notify` on `main`: `NewPolling(interval)` delivers `Hint{}` every interval and ignores `Publish`; `NewMemory` never crosses a process. `golang.org/x/sys` is `// indirect` in `go.mod`; the MCP SDK is absent. `go list -m -versions github.com/modelcontextprotocol/go-sdk` lists v1.7.0 and v1.8.0 (2026-10-21). v1 `FindHarness` skips shells by basename (`sh`, `bash`, `zsh`, `dash`, `ksh`, `fish`, `cmd`, `powershell`, `pwsh`, optional `.exe`), so a helper process with `sh -c` between it and the command is the harness of that command, which is how the exit-4 cases are driven. Claude Code's Monitor tool kills the command at expiry (at most 30 minutes) with SIGTERM, which v1 ADR 0021 maps to exit 3.

## Task dependencies and parallelism

Tasks with no dependency between them run in parallel in separate worktrees and are cherry-picked onto `main`; every task's `Files:` list is disjoint from every task it can run beside. Shared-file owners: `go.mod` and `go.sum` are a chain, Task 2 (`golang.org/x/sys` direct) then Task 7 (the MCP SDK), and the integrator cherry-picks Task 7 after Task 2 and after any Milestone 7 `go.mod` commit, keeping both requirement sets and re-running `go mod tidy` on a conflict (the Milestone 5 rule); `main.go`, `main_test.go` and `internal/cli/cli.go` (the dispatch table and usage) belong to Task 11 alone, so Tasks 6, 8, 9 and 10 each produce `Run<Command>` functions in their own files and nothing is reachable from the binary until Task 11 wires it (their tests call the functions directly); `internal/cli/common.go` (the shared flag and output helpers) belongs to Task 5; `Makefile`, `scripts/check-gates-selftest.sh`, `.github/workflows/ci.yml`, `CLAUDE.md`, `README.md` and the ADRs belong to Task 11. Milestone 7 runs concurrently: no task here touches `internal/store`, `internal/core/notify`, `internal/core/maintenance`, `internal/server/cmd`, `Dockerfile` or `docs/ops`; a Task 11 docs conflict with Milestone 7 is resolved by the integrator keeping both sections.

**External gate:** Tasks 1, 2 and 4 need only Milestone 2 on `main` (`store.NameRule`). Task 3 needs Milestone 5 Task 2 (`internal/auth/keys`, `internal/auth/assertion`) and Task 7 (`remote.Token`, `remote.Register`, `remote.WithBearer`) and Milestone 4 Task 1 (`internal/gen`). Task 5 and everything after it need Milestone 5 Task 10 on `main` (`make verify` green: `server.New` with `Options.Registration`, `server.InProcess`, `server.InProcessTransport`, `remote.New` with no stub, `identities.Service.RegisterLocal`, `maintenance.Service.Tick`, `cmd.Run(ctx, args, environ, stdout, stderr)`, `cmd.RunAdmin`). Before Wave 2 the integrator diffs every name in "Names assumed from Milestones 3, 4 and 5" against `main` and reports drift, and runs Milestone 4 Task 10's `TestWatchWakesAcrossProcesses`: the polling `Hint{}` must re-peek the sources, because local-mode `watch`, the hooks and the MCP wake all rest on it; if it does not, that is a Milestone 3 delivery defect to fix on `main` before Wave 2, not something Task 5 works around. Milestone 5 pending item 7 proceeds on B (the orchestrator's choice): an open-mode TOFU register on a keyless admin row is `forbidden`; nothing here depends on it.

**Gate 0** (before Wave 1): the items of "Claude proposal, pending Eric" go to Eric in one message with options, the recommendation (A) and the test that encodes each (`// pending Eric: Milestone 6 item N`); every wave proceeds on (A) with the markers in place (`rg -n 'pending Eric: Milestone 6' internal/`), and Task 11 records the answers in the ADR addenda. Items 1 (the resume token in the state file), 2 (the Stop hook peeks by resuming) and 3 (`wait_seconds` replaces an armed watch) read frozen text one way where another is possible, so the message names them first.

| Task | Depends on | Wave |
| --- | --- | --- |
| 1 Profiles, directories, repo file (Decisions 21, 33, 71, 74) | none | 1 |
| 2 Process table, harness watch, session state file (Decisions 41, 78); `go.mod` x/sys | none | 1 |
| 3 Credential files, `credential_process`, token source, refreshing transport (Decisions 22, 23, 55, 56, 70, 84) | none (external gate: Milestone 5 Tasks 2 and 7) | 1 |
| 4 The `using-agentbus2` skill | none | 1 |
| 5 Connect layer: local and server modes; CLI scaffolding | 1, 3 | 2 |
| 6 `init`, `register`, `login`, harness and skill installation | 4, 5 | 3 |
| 7 The stdio MCP server; `go.mod` MCP SDK | 2, 5 | 3 |
| 8 `watch`, `wait`, `stop-hook`, `subagent-hook` and the exit-code mapping | 2, 5 | 3 |
| 9 Operator commands: `session`, `channels`, `send`, `receive`, `history`, `search` | 5 | 3 |
| 10 Operator commands: `memories`, `tasks`, `status` | 5 | 3 |
| 11 Dispatch, milestone exit end to end, exit-code table, negative halves, docs | 6, 7, 8, 9, 10 | 4 |

Waves: 1 (Tasks 1 to 4, four disjoint trees); 2 (Task 5); 3 (Tasks 6 to 10 in parallel: `internal/cli/init*.go`, `register.go`, `login.go`, `install*.go` against `internal/mcpserver` against `internal/cli/watch*.go`, `wait*.go`, `hooks*.go`, `exit.go` against `internal/cli/session.go`, `channels.go`, `send.go`, `receive.go`, `history.go`, `search.go` against `internal/cli/memories.go`, `tasks.go`, `status.go`); 4 (Task 11). Eleven tasks; the critical path is 1 or 3, 5, any of 6 to 10, 11.

## Global Constraints

- Module `github.com/ericfitz/agentbus2`, `go 1.27`, `CGO_ENABLED=0`; every method takes a `context.Context` (Decision 6). Work lands on `main`, nothing is pushed, Conventional Commits with the session's attribution trailer; each task commits only its own files, listed by path; a porting commit names its v1 source file (Decision 31).
- Import rule (Decision 47 extended): `internal/profile`, `internal/repoconfig`, `internal/credential`, `internal/procs` and `internal/sessionstate` never import `internal/core/...`, `internal/store/...`, `internal/server/...` or `internal/bus/local` (`internal/credential` imports `internal/gen` and `internal/bus/remote` for the token exchange and nothing else of the server side); `internal/mcpserver` and `internal/cli` never import `internal/gen`, `internal/gen/spec` or `internal/store/...` directly (they see `bus.Bus` through `*client.Conn`); `internal/client` is the one package that composes `internal/server`, `internal/store/sqlite`, `internal/core/...` and `internal/bus/remote` for the local profile. Task 11's `scripts/m6-exit-checks.sh --deps-only` checks these with `go list -deps`.
- No direct messages, no `dm/` channel, no `-filter`, no `@mention`, no identity from the working directory or the repository name, no `-channel` wake filter (not on the wire), no `tags/`, `general/` or `memory/` name prefixes anywhere in code, tool descriptions, hook text or the skill (Decisions 12, 13, 14, 76, 82; ADR 0006). Task 11's exit script greps the embedded skill, the tool descriptions and the hook strings for `dm/`, `-filter`, `@<`, `memory/` and `tasks/`.
- Every host-side file holding a secret is mode 0600 and replaced atomically through `keys.WriteFile` (Decision 22): credential files, the token cache, the session state file (it carries the resume token, pending item 1). Directories holding them are created 0700. On Windows the owner-only ACL helper of Task 3 is applied; CI cannot prove it (no Windows job), which Task 3 states in its commit.
- `environ []string` everywhere (the Milestone 5 rule): every `Run<Command>` and `client.Open` reads `HOME`, `AGENTBUS2_PROFILE`, `AGENTBUS2_IDENTITY`, `AGENTBUS2_CONFIG_DIR`, `AGENTBUS2_DATA_DIR`, `XDG_*`, `APPDATA`, `LOCALAPPDATA` only from the slice it is given; `main.go` passes `os.Environ()`; tests pass a literal slice with `HOME=<tmp>` so no test reads or writes the developer's `~/.config/agentbus2` or `~/.local/share/agentbus2`.
- Unrecognized inputs are rejected with a test: an unknown key in `config.json` or a profile, an unknown `mode`, a `credential_process` on a local profile, any key but `profile` in `.local/agentbus2.json` (`credential_process` named in the error), an unknown `credential_process` output member or `version`, an output with none or two of the three results, an unknown MCP tool argument (the SDK's schema rejection wrapped in the `bus.Error` envelope, v1 `wrapSchemaErrorsInEnvelope`), an unknown subcommand or flag (exit 2 with usage), an unknown `-from`, `-mode` or `-kind` value (passed through to the core's `validation`, never filtered client-side: Decision 80 keeps the rules in the core).
- Hooks fail open: `stop-hook` and `subagent-hook` always exit 0, print nothing on any failure, and run under one context deadline of `hookBudget` 500 ms (Decision 41, choice 8); a slow server or a hanging process walk ends the hook silently.
- Timeouts: no `http.Client.Timeout` and no transport timeout under 60 s on any path a `watch` crosses (Milestone 4's rule); the token exchange and discovery requests use their own 10 s contexts.
- The Milestone 1, 3, 4 and 5 gate checks stay in force: `make test` is Docker-free (every test here opens SQLite on a temp path or an `httptest.Server`; the end-to-end test of Task 11 runs the real binary over SQLite); no `.Skip(` under `internal/conformance` or `internal/dbtest`.
- Definition of Done D applies to the state file (two MCP servers under one harness, a restart under the same harness, a crash leaving a file behind), the token cache (two processes refreshing at once, a stale cache after a server key rotation) and `client.Open` in local mode (two processes over one file, a start after a crash).

## Choices this milestone makes (delegated by the spec, recorded with rationale)

1. **Directories.** Config: `$AGENTBUS2_CONFIG_DIR`, else `$XDG_CONFIG_HOME/agentbus2`, else `$HOME/.config/agentbus2`; Windows: `%APPDATA%\agentbus2`. Data: `$AGENTBUS2_DATA_DIR`, else `$XDG_DATA_HOME/agentbus2`, else `$HOME/.local/share/agentbus2`; Windows: `%LOCALAPPDATA%\agentbus2`. `HOME` is read from `environ`, then `os.UserHomeDir`; never `os.UserConfigDir` (on macOS it is `~/Library/Application Support`, and spec 6.1 says `~/.config`). Ported from v1 `paths_unix.go` and `paths_windows.go` with the `agentbus2` names (Decisions 32, 33).
2. **`config.json` shape** (Decision 74): `{"profiles": {"<name>": {"mode": "local"|"server", "server": "<url>", "identity": "<name>", "credential_process": ["argv"...], "sqlite": "<path>"}}}`, strict as v1 `config.Load` (unknown keys, `null`, trailing content, an empty file rejected; an absent file is an empty profile map). `mode` required; `server` required in server mode (absolute `http` or `https` URL, no path, query or fragment) and forbidden in local mode; `credential_process` only in server mode; `sqlite` only in local mode (default `<data dir>/agentbus2.sqlite`). An undefined `default` profile is the built-in `{mode: local}`; any other undefined name selected explicitly is an error naming the file.
3. **Selection** (Decision 21): `-profile`, then `AGENTBUS2_PROFILE`, then the `profile` member of the nearest `.local/agentbus2.json` walking up from the working directory to the nearest `.git` (v1 `repoconfig.Find`), then `default`. The repo file is `{"profile": "<name>"}` and nothing else: every other key is rejected with an error naming the key, so `credential_process` in a cloned repository cannot run anything (Decision 33).
4. **Identity** (Decision 71): `-identity`, then `AGENTBUS2_IDENTITY`, then the profile's `identity`, then, in server mode only, the one file under `credentials/<profile>/` when exactly one exists; otherwise an error that names the three ways to set it. Local mode has no credential to infer from (Decision 36) and no working-directory name (Decision 14), so a local profile without `identity` needs the flag or the variable; `agentbus2 init` inside a repository writes a per-repository profile carrying one (choice 13, pending item 5).
5. **Credential files** (Decisions 22, 55): `credentials/<profile>/<identity>` is one PKCS#8 PEM block written by `keys.WriteFile` (temp file, 0600, fsync, rename); a file wider than 0600 on Unix is refused on load (as `keys.LoadOrCreateFile` does); on Windows an owner-only DACL is set after the rename (`golang.org/x/sys/windows`, build-tagged). `agentbus2 register` generates Ed25519 by default, `-algorithm ES256` on request.
6. **`credential_process` protocol** (Decision 56). Stdin, one JSON object: `{"version": 1, "identity": "<name>", "token_endpoint": "<url>", "audience": "<url>", "jti": "<16 random bytes base64url>"}` (`audience` equals the token endpoint for `private_key_jwt`, Milestone 5 choice 4, and is sent separately so a future flow can differ). Stdout, one JSON object with `"version": 1` and exactly one of `private_key_pem`, `client_assertion`, `access_token` plus `expires_at` (RFC 3339); any other shape, a non-zero exit, no output or more than 64 KiB is an error naming the process. The process runs with a 10 s context, inherits the environment, is named only by a profile in `config.json` (never the repo file) and its argv is executed without a shell. Caching: a returned access token is cached like any other token until `expires_at`; a client assertion is single-use (its `jti` enters the server's replay set), so it is exchanged at once and the minted token is what is cached; a private key PEM is held in memory for the process lifetime and never written to disk.
7. **Token source and cache** (Decisions 70, 84). Discovery reads `<server>/.well-known/openid-configuration` once per process and takes `token_endpoint` as the assertion `aud`; the request itself goes to the generated client's base URL (`<server>/v1/auth/token`), as Milestone 5's exit test does, because the issuer is a configured string that may differ from the address a client dials (Milestone 5 choice 7; a server bound to `0.0.0.0` advertises `http://0.0.0.0:<port>`). The bearer and the discovery document are cached at `<data dir>/tokens/<profile>/<identity>.json` (0600, atomic), used while more than 60 s remain before `exp`, so hooks and one-shot commands do not exchange on every call. The transport refreshes once on a 401 and replays the request (safe: a 401 is answered by the validator before any handler runs, Milestone 4 choice 5), and the request body is buffered for the replay. A refresh that fails surfaces as `unauthenticated` (Milestone 4 choice 13). Pending item 7 (the cache on disk).
8. **Hook peek** (Decisions 41, 78, 79; pending items 1 and 2). The Stop hook must not call `watch` (it would replace the Monitor's watch with 409, exit 3) and must not call `receive` (it would hand out a batch). It opens the session with the state file's resume token: `Open` revives the row, renews the lease, changes no cursor and returns `Pending` per subscription; the hook sums the counts. The state file therefore carries the resume token (0600). The hook's whole run is bounded by `hookBudget` 500 ms.
9. **`watch` and `wait` contract** (Decision 42, v1 ADR 0021, issue #38). Both need a session: `-session ID`, else the state file of the harness found by `procs.FindHarness` (the `Primary` file, choice 10), else exit 2 naming both. `watch`: `Watch{After: floor, Timeout: 0}` (the server cap) in a loop; a wake prints one line `agentbus2: <count> new message(s) for <display name> on <ch>, <ch>; call receive` (channels sorted, the display name from the state file or `Open`), sets `floor = Seq`, sleeps `watchMinGap` 5 s (lines at least that far apart, as v1), re-arms; a timeout re-arms silently; `replaced` exits 3 silently; SIGINT, SIGTERM and SIGHUP exit 3 silently; harness gone (polled every 2 s through `procs.Alive`, `-no-harness-watch` disables) exits 4 silently; any other error exits 2 with `agentbus2: <err>` on stderr; it never exits 0 or 1. `wait`: one wake prints `{"seq":N,"count":N,"channels":[...]}` as one JSON line and exits 0 (the wire carries no message, Decision 10; `receive` reads them); `-timeout D` (default forever) exits 1; 2, 3 and 4 as `watch`. `-include-own` is passed through. `-channel` and `-filter` are not offered: the first is not on the wire and the second is Decision 12 (pending item 4). The floor starts at 0, so the unreceived backlog is announced once (ADR 0021's floor rule).
10. **State file** (Decision 78): `<data dir>/sessions/<session id>.json`, 0600, atomic, `{session, identity, profile, display_name, resume_token, harness_pid, harness_start, harness_client, mcp_pid, primary, opened_at}`. The MCP server writes one per session it opens, marks the first session opened without a hint `primary`, removes a file on `close_session` and on exit, and at start sweeps files whose `mcp_pid` and harness are both dead. A hook or `watch` resolving by harness takes the `primary` file for that harness ref, else the earliest `opened_at`. Resume (Decision 79): a `register` without hint and without `resume_token` resumes the `primary` file of the same harness ref when its `mcp_pid` is dead (an MCP restart under the same harness), and the file is rewritten with the new pid; past retention the server answers `Resumed false` with a fresh session and the file is replaced.
11. **MCP server** (Decisions 3, 47, 66, 83; v1 `server.go`): go-sdk v1.7.0 (the version spec 9 names and v1 ships; v1.8.0 exists but nothing here needs it, and the tool API is unchanged), `mcp.Implementation{Name: "agentbus2"}`, tool input structs with `jsonschema` tags (hand-written: never a generated type). Tools: `register` (`hint?`, `resume_token?`; opens a session and returns `{session, identity, display_name, resume_token, resumed, pending, rejected_cursors, others}` where `others` is `Identities`), `close_session`, `create_channel` (`name`, `kind?`, `acl?`, `idempotency_key?`), `list_channels`, `delete_channel`, `rename_channel`, `get_acl`, `set_acl`, `subscribe` (`channel` or `tags`, `from?`), `unsubscribe`, `send`, `receive` (`session`, `ack?`, `count?`, `include_own?`, `wait_seconds?`), `history`, `search`, `get_memory`, `edit_memory`, `delete_memory`, `discover`, `task_create`, `task_claim`, `task_release`, `task_update`, `task_get`, `task_list`: twenty-four. Every tool but `register`, `close_session` and `receive` takes an optional `session` (attribution on `send`; ignored elsewhere); `receive` requires it. `wait_seconds` above 0 on an empty non-blocking receive runs `Watch{Timeout: wait_seconds}` then receives again; the tool description and the skill say this replaces an armed `agentbus2 watch` for that session (pending item 3). The MCP server starts even when the profile or identity cannot be resolved: it logs the error and every tool answers it as `validation` naming the fix, so the agent can tell the user (v1's "not configured yet" port). Logging goes to `<data dir>/logs/mcp-<pid>.log` (one file per process, `O_APPEND`, no rotation, removed on a clean exit; v1 `logfile.go`'s flock rotation is not ported: Decision 68 keeps file locks out and a per-process file needs none); nothing is written to stdout or stderr. The maintenance loop runs in the MCP process in local mode (`maintenance.Service.Tick` under the lease row, Decision 68); one-shot commands never run it.
12. **Operator CLI** (spec 2.2, v1 `main.go`): every command is `Run<Name>(ctx, args, environ, stdin, stdout, stderr) int` with `flag.ContinueOnError`, `-h` exit 0, a flag error exit 2, a `bus.Error` printed as `agentbus2: <code>: <message>` with exit 1 (2 for `watch` and `wait`, whose table is spec 3.5); results are one JSON document on stdout (`status` is human-readable). `receive` without `-session` opens a session with hint `cli`, receives, acks with a second receive unless `-no-ack`, and closes it; with `-session` it touches nothing but that session. `channels delete` keeps v1 `delete.go`'s y/N prompt and `-y`.
13. **`init`** (v1 `init.go`). `init --global` registers the MCP server (`claude mcp add -s user agentbus2 -- agentbus2 mcp`, the Codex and Grok equivalents), the `Stop` hook `agentbus2 stop-hook`, the Claude Code `SubagentStart` hook `agentbus2 subagent-hook`, installs the skill at `skills/using-agentbus2/SKILL.md` under each harness's skills directory, and writes `config.json` with `profiles.default = {mode: local}` when the file is absent. There is no `SessionStart` hook (v1's ran `agentbus identity`, Decision 14). `init` inside a repository writes `.local/agentbus2.json` `{"profile": "<name>"}` (`-profile`, default the repository basename), adds the profile to `config.json` when absent as a copy of `default`'s `mode` and `server` with `identity` set to the basename (which must pass `NameRule`, else `-identity` is required), git-ignores `.local/`, and prints the next step (`agentbus2 register` when the profile is server mode and holds no credential). Pending item 5.
14. **`register` and `login`.** `register [-profile P] [-identity I] [-server URL] [-enrollment-token T] [-algorithm EdDSA|ES256]`: when the profile is undefined and `-server` is given, the profile is created as `{mode: server, server: URL, identity: I}` (pending item 6); generates the key, calls `remote.Register`, saves the credential only after a 201, prints `registered <identity> at <server> (created|existing key)`; a same-key retry is idempotent (Milestone 5 choice 8). `login`: discovery, assertion, exchange, cache, prints `logged in as <identity> at <issuer> (token expires <RFC 3339>)`; on a local profile exit 2 `login applies to server profiles only`.
15. **Subagent hook text** (v1 `subagenthook.go`, ADR 0016): the parent's session comes from the state file; the context tells the subagent to call `register` with `hint` set to a slug of its agent type plus a random suffix, to pass the returned `session` on every call and never to arm `watch` or `wait`. Parent naming is by hint (Decision 15), there is no `parent` argument.
16. **Package layout.** `internal/profile`, `internal/repoconfig`, `internal/credential`, `internal/procs`, `internal/sessionstate`, `internal/client`, `internal/mcpserver`, `internal/cli` (one file per command), `internal/cli/skills/using-agentbus2/SKILL.md`, `internal/e2e` (the exit test), `scripts/m6-exit-checks.sh`.

## Claude proposal, pending Eric

Each proceeds on recommendation (A) with a `// pending Eric: Milestone 6 item N` marker on the test that encodes it; Task 11 records the answers in the ADR 0002 and 0003 addenda.

1. **The resume token lives in the state file** (choice 10). Decision 78 lists pid, start time, session id and profile. A) Add the resume token (0600) so hooks can peek and an MCP restart can resume (recommended). B) Keep the file as listed; the hook then has no peek and the restart cannot resume. Encoded in Task 2's `TestStateFileHoldsResumeToken`.
2. **The Stop hook peeks by resuming the session** (choice 8). A) `Open{ResumeToken}` and the `Pending` counts, which include an unacked batch (recommended; no API change). B) A new `peek` operation on the API (a Milestone 4 document change). C) `receive` without ack (hands out a batch the watch then ignores). Encoded in Task 8's `TestStopHookPeeksWithoutSideEffects`.
3. **`wait_seconds` replaces an armed watch** (choice 11; Decisions 42 and 83 together). A) Keep `wait_seconds` and say so in the tool description and the skill (recommended; Decision 83 as written). B) Drop `wait_seconds` from the tool. Encoded in Task 7's `TestReceiveWaitSecondsReplacesWatch`.
4. **`watch` and `wait` take no `-channel`** (choice 9). A) Dropped: the wake carries every subscribed channel and the agent calls `receive` anyway (recommended). B) A client-side filter on the wake's `channels` list. Encoded in Task 8's `TestWatchFlags`.
5. **Per-repository profiles from `init`** (choices 4 and 13). A) `init` in a repository creates profile `<repo>` with `identity: <repo>` copying `default`'s mode and server, and selects it in `.local/agentbus2.json` (recommended; keeps v1's one-identity-per-repository habit through Decision 21's mechanism). B) `init` writes only the repo file; profiles are always hand-written. Encoded in Task 6's `TestInitRepoCreatesProfile`.
6. **`register -server URL` creates the profile** (choice 14). A) As stated (recommended; one command enrolls a new host). B) `register` requires an existing profile. Encoded in Task 6's `TestRegisterCreatesServerProfile`.
7. **The bearer is cached on disk** (choice 7). A) `<data dir>/tokens/<profile>/<identity>.json`, 0600, reused until 60 s before `exp` (recommended; a one-shot command or a hook otherwise exchanges on every call, and a `credential_process` would be invoked every time against Decision 56's "cached until expiry"). B) Memory only; every process exchanges once. Encoded in Task 3's `TestSourceCachesToken`.

## Not ported (Decision 31)

v1 `internal/cli/identity.go` (`Identity`, `IdentityName`, `SessionIdentity`), `internal/cli/waitlock*.go` and `internal/filelock` (Decision 68; the watch registration is a server row), `wait.go`'s `waitMatch` and `-filter`, the `inbox` branches, `internal/mcpserver/server.go`'s `applyPersistent`, `persistFile`, `persistErr`, the `persistent` tool members and the `register` tool's `name`, `parent`, `context` and `resume` arguments, `internal/repoconfig`'s `channels`, `tags` and `tag_subscriptions` members (Decision 37), `internal/cli/subscribe.go` (it edited the repo file), `internal/cli/reset.go` (reset is an admin API call), `logfile.go`'s rotation under flock, v1 `config.go`'s server-side settings (Milestone 5), the `SessionStart` hook and the Grok rule file's `agentbus identity` line, the TUI (Milestone 8).

## Names assumed from Milestones 3, 4 and 5 (diffed against `main` before Wave 2)

From Milestone 3: `core.New(store, cfg, core.Options{Now, Notifier, Embedder, Logger})`, `core.Defaults`, `core.Config` (`MaintenanceIntervalSeconds`, `SessionRetentionHours`, `WatchMaxSeconds`), `notify.NewPolling`, `notify.NewMemory`, `channels.Service{*core.Core}.EnsureGeneral`, `maintenance.Service{*core.Core}.Tick(ctx) (bool, []JobResult)`, `sessions.Service.Open`, `bus.*` as on `main`, `store.NameRule`, `sqlite.Open(ctx, path, sqlite.Options{})`. From Milestone 4: `server.New(c, server.Options{Auth, Registration, ...})`, `server.InProcess{}`, `server.InProcessTransport(h, identity)`, `remote.New(*gen.ClientWithResponses) *remote.Bus`, `gen.NewClientWithResponses(url, gen.WithHTTPClient, gen.WithRequestEditorFn)`, `cmd.Run(ctx, args, environ, stdout, stderr)`, `cmd.RunAdmin(ctx, args, environ, stdout, stderr)`, `cmd.Config`, `cmd.Serve`, `scripts/m4-exit-checks.sh`, `scripts/check-gates-selftest.sh` blocks 1 to 15 and its `begin`/`finish`/`problem`/`revert`/`run_gates`/`expect_checks` helpers. From Milestone 5: `keys.Algorithm`, `keys.EdDSA`, `keys.ES256`, `keys.Generate`, `keys.AlgorithmOf`, `keys.KID`, `keys.EncodePrivatePEM`, `keys.ParsePrivatePEM`, `keys.WriteFile`, `assertion.Sign(assertion.Input{Identity, TokenEndpoint, Key, KID, Now, Lifetime})`, `assertion.Type`, `remote.Register(ctx, c, identity, pub, enrollmentToken) (remote.RegisterResult, error)`, `remote.Token(ctx, c, clientAssertion) (remote.AccessToken{Token, ExpiresIn}, error)`, `remote.WithBearer(token) gen.RequestEditorFn`, `identities.Service{*core.Core}.RegisterLocal(ctx, name)`, `identities.ModeOpen`, the `openid-configuration` document `{issuer, token_endpoint, jwks_uri, ...}`, `agentbus2 server admin enrollment create` printing `token: abe_...`, `agentbus2 server` printing `agentbus2 server listening on <addr> (<engine>, registration <mode>, issuer <url>)`.

## Review Focus

1. A cached bearer that the server no longer accepts (an ephemeral signing key after a server restart, a `reset-credential`): the first call is 401, the transport refreshes once; with a valid key the retry succeeds, with a revoked key the refresh fails and the command exits with `unauthenticated` instead of looping. (Task 3: `TestTransportRefreshesOnceOn401`; Task 11: subtest `stale-bearer-refreshes`.)
2. A cloned repository carrying `.local/agentbus2.json` with `credential_process` or any key but `profile`: every command exits 2 naming the key and runs nothing; a `profile` naming a profile that does not exist in the user's `config.json` is an error, never an implicit profile. (Task 1: `TestRepoFileRejectsEverythingButProfile`.)
3. The MCP server started where no identity resolves (no repo file, no `identity` in `default`, no flag): it serves, every tool answers `validation` with the three ways to set the identity, and the harness sees a running server, not a crash. (Task 7: `TestToolsReportUnresolvedIdentity`.)
4. Two harnesses on one host under one identity: each hook and `watch` finds its own session through its own harness ref; a stale state file from a crashed MCP server whose harness also died is swept and never resumed by a stranger. (Task 2: `TestForHarnessPicksPrimary`, `TestSweep`; Task 7: `TestRegisterResumesAfterRestart`.)
5. A server that hangs on the hook's request: the Stop hook prints nothing and exits 0 within `hookBudget` plus slack, so a stuck bus never keeps a harness from stopping. (Task 8: `TestStopHookFailsOpenWithinBudget`.)

---

### Task 1: Profiles, directories, repo file (Decisions 21, 33, 71, 74)

**Depends on:** none.

**Files:**
- Create: `internal/profile/doc.go`, `internal/profile/paths.go`, `internal/profile/paths_windows.go` (build-tagged; `paths.go` is `//go:build !windows`), `internal/profile/config.go`, `internal/profile/select.go`, `internal/repoconfig/repoconfig.go`
- Test: `internal/profile/paths_test.go`, `internal/profile/config_test.go`, `internal/profile/select_test.go`, `internal/repoconfig/repoconfig_test.go`

**Port from v1:** `internal/config/config.go` (`Load`'s strict JSON rules: `DisallowUnknownFields`, one value then EOF, `null` and an empty file refused, the error text naming the path), `internal/config/paths_unix.go`, `paths_windows.go` (the directory rules with the `agentbus2` names), `internal/repoconfig/repoconfig.go` (`Find`'s walk to the nearest `.git`, `write`'s temp-and-rename) reduced to the `profile` member (Decision 33).

**Interfaces (produces):**

```go
package profile // internal/profile
type Mode string
const ( ModeLocal Mode = "local"; ModeServer Mode = "server" )
type Profile struct { Name string `json:"-"`; Mode Mode `json:"mode"`; Server string `json:"server,omitempty"`; Identity string `json:"identity,omitempty"`; CredentialProcess []string `json:"credential_process,omitempty"`; SQLite string `json:"sqlite,omitempty"` }
func (p Profile) Validate() error // choice 2: mode in {local, server} else error naming the value; server: Server required, absolute http(s) URL with no path, query or fragment, trailing slash stripped; local: Server and CredentialProcess forbidden; server: SQLite forbidden; Identity "" or NameRule
type Config struct { Path string `json:"-"`; Profiles map[string]Profile `json:"profiles"` }
func ConfigDir(environ []string) (string, error) // choice 1
func DataDir(environ []string) (string, error)   // choice 1
func Getenv(environ []string, name string) (string, bool) // last wins, as os.Environ semantics
func Load(path string) (Config, error) // strict; absent file → Config{Path: path, Profiles: map[string]Profile{}} and nil; every profile Validate'd on load, its Name set
func (c Config) Save() error // MkdirAll(dir, 0o700); JSON indented; temp file 0o600 in the same directory, fsync, rename
func DefaultProfile() Profile // {Name: "default", Mode: local}
func (c Config) Resolve(name string) (Profile, error) // defined → it; "default" undefined → DefaultProfile(); else error `profile %q is not defined in %s`
func SQLitePath(p Profile, dataDir string) string // p.SQLite or dataDir/agentbus2.sqlite
func CredentialDir(configDir, profileName string) string // configDir/credentials/<profile>
type Selection struct { Flag, Env, Cwd string } // Env is the AGENTBUS2_PROFILE value
const ( SourceFlag = "flag"; SourceEnv = "env"; SourceRepo = "repo"; SourceDefault = "default" )
func SelectName(sel Selection) (name, source string, err error) // choice 3; a repo file error is returned, never ignored
type IdentitySelection struct { Flag, Env string; Credentials []string }
func (p Profile) ResolveIdentity(sel IdentitySelection) (identity, source string, err error) // choice 4; sources "flag", "env", "profile", "credential"; the error text: `identity is required: pass -identity, set AGENTBUS2_IDENTITY, or set identity in profile %q`
```

```go
package repoconfig // internal/repoconfig
type File struct { Path, Profile string }
func Find(dir string) (*File, error) // nearest .local/agentbus2.json walking up to the nearest .git; (nil, nil) when none; a malformed file, a non-string or empty profile, or any key but "profile" → error naming the path and the key (`.local/agentbus2.json may only select a profile: unexpected key %q`)
func Write(dir, profile string) (*File, error) // dir/.local/agentbus2.json, MkdirAll 0o755, temp-and-rename, 0o644 (no secret)
```

- [ ] **Step 1: Write the failing tests**

`paths_test.go`: `TestConfigDirPrecedence` (a table over `environ` slices: `AGENTBUS2_CONFIG_DIR` wins; then `XDG_CONFIG_HOME`; then `HOME/.config/agentbus2`; no `HOME` and no variable → `os.UserHomeDir` fallback asserted by non-empty), `TestDataDirPrecedence` (the same for data), both files build-tagged so the Windows table (`APPDATA`, `LOCALAPPDATA`) is in `paths_windows_test.go`. `config_test.go`: `TestLoadAbsentIsEmpty`, `TestLoadStrict` (unknown top-level key, unknown profile key `colour`, `null`, trailing `{}`, an empty file, `mode: "remote"`: each an error naming the path and the key or value), `TestProfileValidate` (a table: local with `server` → error; server without `server` → error; `server: "https://h:8443/api"` (a path) → error; `https://h:8443/` → stored without the slash; local with `credential_process` → error; server with `sqlite` → error; identity `a/b` → error), `TestSaveRoundTripAtomic` (0600, 0700 directory, no temp file left, a reload equals), `TestResolveDefault` (undefined `default` → `DefaultProfile()`; undefined `lab` → error naming the file). `select_test.go`: `TestSelectNameOrder` (flag over env over repo over default; sources asserted), `TestSelectNameRepoErrorSurfaces` (a repo file with `credential_process` → the error, not `default`), `TestResolveIdentityOrder` (flag, env, profile, the one credential; two credentials and no setting → the three-way error; local mode never infers from credentials even when `Credentials` has one). `repoconfig_test.go`: `TestFindWalksToGitRoot` (file in a parent below `.git` found; above `.git` not found; no `.git` at all walks to `/` and finds nothing), `TestRepoFileRejectsEverythingButProfile` (Review Focus 2: `{"profile":"lab","credential_process":["x"]}` → error naming `credential_process`; `{"identity":"x"}` → error naming `identity`; `{"profile":""}` → error; `{"profile":"lab"}` → `lab`), `TestWriteAndReload`.

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/profile/ ./internal/repoconfig/`
Expected: build failure (`undefined: profile.Load`).

- [ ] **Step 3: Implement** as in Interfaces.

- [ ] **Step 4: Run the tests, the import rule and the gate**

Run: `go test -race ./internal/profile/ ./internal/repoconfig/`, then `go list -deps ./internal/profile ./internal/repoconfig | grep -E 'agentbus2/internal/(core|server|bus/local|gen)' ; echo "exit=$?"` (expected `exit=1`), then `make verify` (600000 ms).
Expected: pass; `verify: OK`.

- [ ] **Step 5: Commit**

```bash
git add internal/profile/doc.go internal/profile/paths.go internal/profile/paths_windows.go internal/profile/config.go internal/profile/select.go internal/profile/paths_test.go internal/profile/config_test.go internal/profile/select_test.go internal/repoconfig/repoconfig.go internal/repoconfig/repoconfig_test.go
git commit -m "feat(profile): profiles in config.json, the selection order, identity selection and the profile-only repo file

Ports the strict JSON load of v1 internal/config/config.go, the directory
rules of paths_unix.go and paths_windows.go, and the walk of
internal/repoconfig/repoconfig.go reduced to profile selection (Decisions
21, 33, 71, 74). Any other key in .local/agentbus2.json is rejected."
```

---

### Task 2: Process table, harness watch, session state file (Decisions 41, 78)

**Depends on:** none.

**Files:**
- Create: `internal/procs/procs.go`, `procs_darwin.go`, `procs_linux.go`, `procs_windows.go`, `procs_other.go`, `procs_other_unix.go`, `procs_other_stub.go`, `harness.go` (`FindHarness`, `WatchHarness`, `ErrHarnessGone`), `internal/sessionstate/state.go`
- Modify: `go.mod`, `go.sum` (`golang.org/x/sys` becomes a direct requirement; `go mod tidy`)
- Test: `internal/procs/procs_test.go`, `harness_test.go`, `procs_linux_test.go`, `helper_test.go` (the fake `Table`), `internal/sessionstate/state_test.go`

**Port from v1:** `internal/procs/*.go` (every file, module path changed; `procs_test.go`, `harness_test.go`, `procs_linux_test.go`, `helper_test.go` ported with them), `internal/cli/harness.go` (`watchHarness` and `harnessPollInterval` become `procs.WatchHarness` and `procs.HarnessPollInterval`; `ErrHarnessGone` moves here so `cli` and `mcpserver` share it).

**Interfaces (produces):**

```go
package procs // as v1: Table, System, Ref, Self, Alive, FindHarness, ErrGone
var ErrHarnessGone = errors.New("harness gone")
var HarnessPollInterval = 2 * time.Second
func WatchHarness(ctx context.Context, t Table, h Ref, cancel context.CancelCauseFunc) // v1 watchHarness: cancel(ErrHarnessGone) when Alive is false
```

```go
package sessionstate // internal/sessionstate; imports internal/procs and internal/auth/keys (WriteFile) only
type File struct { Session string `json:"session"`; Identity string `json:"identity"`; Profile string `json:"profile"`; DisplayName string `json:"display_name"`; ResumeToken string `json:"resume_token"`; HarnessPID int `json:"harness_pid"`; HarnessStart int64 `json:"harness_start"`; HarnessClient string `json:"harness_client"`; MCPPID int `json:"mcp_pid"`; Primary bool `json:"primary"`; OpenedAt int64 `json:"opened_at"` }
func Dir(dataDir string) string                       // dataDir/sessions
func Write(dataDir string, f File) error              // Session required; MkdirAll 0o700; keys.WriteFile to Dir/<session>.json (0600, atomic)
func Read(dataDir, session string) (File, error)      // strict JSON
func Remove(dataDir, session string) error            // nil when absent
func List(dataDir string) ([]File, error)             // every parsable file by OpenedAt then Session; an unparsable file is skipped and reported through the returned error only when nothing parsed
func ForHarness(dataDir string, h procs.Ref) (File, bool, error) // files whose HarnessPID and HarnessStart equal h's (a zero HarnessStart matches any, as procs.Alive); the Primary one, else the earliest OpenedAt; false when none
func Sweep(dataDir string, t procs.Table) (removed int, err error) // choice 10: remove each file whose MCPPID is not Alive and whose harness Ref is not Alive
```

- [ ] **Step 1: Write the failing tests**

The v1 procs tests ported as they are (`TestFindHarnessSkipsShells`, the `ErrGone` cases, `TestAlive`, the Linux `parseStat` cases) plus `TestWatchHarnessCancelsWithCause` (a fake table whose `StartTime` starts answering `ErrGone` after the first poll; `HarnessPollInterval` 10 ms; the cause is `ErrHarnessGone` within 200 ms). `state_test.go` (`t.TempDir()` as the data dir): `TestStateFileHoldsResumeToken` (`// pending Eric: Milestone 6 item 1`; write, read back every field, mode 0600, directory 0700, no temp file), `TestWriteRequiresSession`, `TestForHarnessPicksPrimary` (Review Focus 4: three files for harness `{10, 5}`, the later one `Primary` → it; none primary → the earliest `OpenedAt`; another harness's file never returned; `{10, 0}` matches `{10, 5}`), `TestSweep` (a fake table: a file whose MCP and harness are both gone is removed; one whose harness lives but MCP is dead stays (the restart resume case); one whose MCP lives stays; `removed` counts), `TestListSkipsMalformed`, `TestRemoveAbsentIsNil`.

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/procs/ ./internal/sessionstate/`
Expected: build failure (`undefined: procs.WatchHarness`, `undefined: sessionstate.Write`).

- [ ] **Step 3: Implement**; `go get golang.org/x/sys@v0.48.0 && go mod tidy` (the version already in `go.sum`). State (Definition of Done D): two MCP servers under one harness write two files, one `Primary`; a restart finds the previous file with a dead `MCPPID`; a crash leaves a file that `Sweep` removes only once the harness is gone too.

- [ ] **Step 4: Run the tests, the cross-compile check and the gate**

Run: `go test -race ./internal/procs/ ./internal/sessionstate/`, then `GOOS=linux GOARCH=amd64 go vet ./internal/procs/ && GOOS=windows GOARCH=amd64 go vet ./internal/procs/ ./internal/sessionstate/` (v1's `vet-cross` for the build-tagged files), then `make verify tidy-check` (600000 ms).
Expected: pass; `verify: OK`; `tidy-check` clean.

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum internal/procs/procs.go internal/procs/procs_darwin.go internal/procs/procs_linux.go internal/procs/procs_windows.go internal/procs/procs_other.go internal/procs/procs_other_unix.go internal/procs/procs_other_stub.go internal/procs/harness.go internal/procs/procs_test.go internal/procs/harness_test.go internal/procs/procs_linux_test.go internal/procs/helper_test.go internal/sessionstate/state.go internal/sessionstate/state_test.go
git commit -m "feat(procs): process table and harness walk ported from v1 internal/procs; the per-session state file of Decision 78

Ports internal/procs/*.go and internal/cli/harness.go (watchHarness) as
the session locator of Decision 41; the state file under
~/.local/share/agentbus2/sessions holds the harness ref, session id,
profile and resume token (Milestone 6 pending item 1), mode 0600."
```

---
### Task 3: Credential files, `credential_process`, token source, refreshing transport (Decisions 22, 23, 55, 56, 70, 84)

**Depends on:** none (external gate: Milestone 5 Tasks 2 and 7 and Milestone 4 Task 1 on `main`: `keys`, `assertion`, `remote.Token`, `gen`).

**Files:**
- Create: `internal/credential/doc.go`, `internal/credential/store.go`, `internal/credential/acl_windows.go` (`ownerOnly(path)` with `golang.org/x/sys/windows`; `acl_other.go` is a no-op), `internal/credential/process.go`, `internal/credential/discover.go`, `internal/credential/source.go`, `internal/credential/transport.go`
- Test: `internal/credential/store_test.go`, `process_test.go`, `discover_test.go`, `source_test.go`, `transport_test.go`, `testdata/credproc.sh` (the fake process of Definition of Done C: reads stdin, prints the output named by `CREDPROC_MODE`: `pem`, `assertion`, `token`, `two`, `garbage`, `exit1`, `hang`)

**Port from v1:** nothing (v1 has no credentials). RFC 7523 and Milestone 5 choice 4 are the sources.

**Interfaces (produces):**

```go
package credential // internal/credential
type Store struct{ Dir string } // profile.CredentialDir(configDir, profile)
func (s Store) Path(identity string) string
func (s Store) List() ([]string, error)                               // file names, sorted; none when Dir is absent
func (s Store) Exists(identity string) bool
func (s Store) Load(identity string) (crypto.Signer, keys.Algorithm, error) // NameRule on identity; Unix: mode wider than 0600 → error "must be mode 0600"; keys.ParsePrivatePEM
func (s Store) Save(identity string, k crypto.Signer) error           // MkdirAll(Dir, 0o700); keys.EncodePrivatePEM; keys.WriteFile; ownerOnly(path) on Windows
// process.go (choice 6)
type ProcessInput struct { Version int `json:"version"`; Identity string `json:"identity"`; TokenEndpoint string `json:"token_endpoint"`; Audience string `json:"audience"`; JTI string `json:"jti"` }
type ProcessOutput struct { Version int `json:"version"`; PrivateKeyPEM string `json:"private_key_pem,omitempty"`; ClientAssertion string `json:"client_assertion,omitempty"`; AccessToken string `json:"access_token,omitempty"`; ExpiresAt string `json:"expires_at,omitempty"` }
const ProcessTimeout = 10 * time.Second; const processMaxOutput = 64 * 1024
func RunProcess(ctx context.Context, argv []string, in ProcessInput) (ProcessOutput, error) // exec.CommandContext(argv[0], argv[1:]...), stdin the JSON, stdout limited, stderr captured for the error; strict decode (unknown members refused); Version != 1 → error; exactly one of the three results else error "credential_process must return exactly one of private_key_pem, client_assertion or access_token"; access_token without a parsable RFC 3339 expires_at → error; every error names argv[0]
// discover.go
type Discovery struct { Issuer string `json:"issuer"`; TokenEndpoint string `json:"token_endpoint"`; JWKSURI string `json:"jwks_uri"` }
func Discover(ctx context.Context, hc *http.Client, server string) (Discovery, error) // GET server + "/.well-known/openid-configuration" under a 10 s context; non-200 or a missing token_endpoint → error naming the URL
// source.go (choice 7)
type Cache struct{ Dir string } // dataDir/tokens/<profile>
type Entry struct { AccessToken string `json:"access_token"`; ExpiresAt time.Time `json:"expires_at"`; Discovery Discovery `json:"discovery"` }
func (c Cache) Load(identity string) (Entry, bool, error); func (c Cache) Save(identity string, e Entry) error // 0600 atomic through keys.WriteFile; MkdirAll 0o700
func (c Cache) Remove(identity string) error
const Skew = 60 * time.Second
type Source struct { Server, Identity string; Signer crypto.Signer /* nil when Process is set */; Process []string; Client *gen.ClientWithResponses /* over Server, no editor */; HTTP *http.Client /* discovery; nil: a 10 s client */; Cache Cache; Now func() time.Time /* nil: time.Now */ }
func (s *Source) Token(ctx context.Context) (string, error)   // Cache.Load valid for more than Skew → it; else Refresh
func (s *Source) Refresh(ctx context.Context) (string, error) // Discover (from the cache entry when present, else the network); then: Process set → RunProcess with a fresh jti: access_token → cache and return; client_assertion → remote.Token → cache; private_key_pem → keys.ParsePrivatePEM once per process, then as Signer; Signer → assertion.Sign{Identity, TokenEndpoint: d.TokenEndpoint, Key, KID: keys.KID, Now} → remote.Token(ctx, s.Client, raw) → cache {Token, Now + ExpiresIn, d}; a *bus.Error from remote.Token is returned as is
func (s *Source) Editor() gen.RequestEditorFn                   // Authorization: Bearer <Token(ctx)>
// transport.go
func Transport(base http.RoundTripper, s *Source) http.RoundTripper // RoundTrip: buffer the body (io.ReadAll, bytes.Reader; GetBody set); send; on 401 exactly once: Cache.Remove, Refresh, set the header, replay with a new body reader; any other status or error passes through; a Refresh error returns the original 401 response (the remote maps it to unauthenticated)
```

- [ ] **Step 1: Write the failing tests**

`store_test.go`: `TestSaveLoadRoundTrip` (both algorithms; mode 0600; directory 0700; no temp file), `TestLoadRefusesWideMode` (Unix only), `TestLoadRejectsName` (`a/b`), `TestListSorted`. `process_test.go` (`testdata/credproc.sh` through `bash`, `CREDPROC_MODE` in the environment; the script echoes the stdin JSON to a file named by `CREDPROC_CAPTURE` so the test asserts the input): `TestProcessInputShape` (`version 1`, `identity`, `token_endpoint`, `audience`, a 22-character `jti`), `TestProcessEachResult` (`pem` → `PrivateKeyPEM` parses; `assertion`; `token` with `expires_at`), `TestProcessRejects` (`two` results, `garbage`, `exit1` (stderr quoted in the error), `version 2`, an unknown member, `token` without `expires_at`, a missing executable; each error names the program), `TestProcessTimeout` (`hang` with a 200 ms context → error within 1 s, the process reaped). `discover_test.go`: `TestDiscoverReadsDocument` (an `httptest.Server`), `TestDiscoverRejects` (404; a document without `token_endpoint`). `source_test.go` (an `httptest.Server` serving `/.well-known/openid-configuration` and a `POST /v1/auth/token` that verifies the assertion against the test key with `assertion.Parse` and `Verify`, counts exchanges, and answers `{"access_token": "t<n>", "token_type": "Bearer", "expires_in": 3600}`): `TestSourceSignsAndExchanges` (the assertion's `aud` is the document's `token_endpoint` and `iss`=`sub`=identity; `Token` returns `t1`), `TestSourceCachesToken` (`// pending Eric: Milestone 6 item 7`; a second `Source` over the same `Cache` returns `t1` without an exchange; the cache file is 0600; with `Now` advanced to 30 s before expiry → an exchange), `TestSourceProcessPaths` (the fake process in each mode: `token` → no exchange and the returned token cached; `assertion` → one exchange; `pem` → signs with the returned key, one exchange, nothing written under `Cache.Dir` but the token entry), `TestSourceDiscoveryCached` (after one `Token`, the discovery endpoint's counter stays at 1 across a new `Source` over the same cache), `TestSourceAudienceFromDocument` (the document advertises `token_endpoint` `http://0.0.0.0:1/v1/auth/token` while the request goes to the test server: the exchange succeeds and the assertion's `aud` is the advertised string). `transport_test.go`: `TestTransportRefreshesOnceOn401` (Review Focus 1: a handler that answers 401 to `t1` and 200 to `t2`; one request → 200 with two exchanges and the body replayed byte for byte (a POST with a 1 KiB body, asserted by the handler); a handler that always answers 401 → the 401 is returned after exactly one refresh, never a loop), `TestTransportPassesOtherStatuses` (403 and 500 untouched, no refresh).

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/credential/`
Expected: build failure (`undefined: credential.Store`).

- [ ] **Step 3: Implement** as in Interfaces. State (Definition of Done D): two processes refreshing at once each exchange and the last `Save` wins (both tokens are valid; the rename makes the file always whole); a cache entry whose token the server refuses is removed by the transport's 401 path, so a server key rotation costs one failed request.

- [ ] **Step 4: Run the tests, the import rule and the gate**

Run: `go test -race ./internal/credential/`, then `go list -deps ./internal/credential | grep -E 'agentbus2/internal/(core|server|store|bus/local|gen/spec|auth/token)' ; echo "exit=$?"` (expected `exit=1`), then `GOOS=windows GOARCH=amd64 go vet ./internal/credential/`, then `make verify` (600000 ms).
Expected: pass; `verify: OK`.

- [ ] **Step 5: Commit**

```bash
git add internal/credential/doc.go internal/credential/store.go internal/credential/acl_windows.go internal/credential/acl_other.go internal/credential/process.go internal/credential/discover.go internal/credential/source.go internal/credential/transport.go internal/credential/store_test.go internal/credential/process_test.go internal/credential/discover_test.go internal/credential/source_test.go internal/credential/transport_test.go internal/credential/testdata/credproc.sh
git commit -m "feat(credential): credential files, the credential_process protocol, the token source with a disk cache and the refreshing transport

Decisions 22, 23, 55, 56, 70, 84: PKCS#8 PEM at mode 0600 replaced
atomically, the versioned JSON protocol of Decision 56 with results cached
until expiry (Milestone 6 pending item 7), the assertion audience from
openid-configuration, one refresh on 401. The Windows owner-only ACL is
build-tagged and not proved in CI."
```

---

### Task 4: The `using-agentbus2` skill

**Depends on:** none.

**Files:**
- Create: `internal/cli/skills/using-agentbus2/SKILL.md`
- Test: none in Go (Task 6 embeds and installs it; Task 11's exit script greps it)

**Port from v1:** `internal/cli/skills/using-agentbus/SKILL.md`, rewritten (spec 9: "no direct-message wording").

- [ ] **Step 1: Write the skill**

Front matter `name: using-agentbus2`, the v1 description with `Agentbus2`. Sections, in this order, with these changes against v1: Overview (one bus per profile: local, or a server shared by several hosts; identity is the profile's credential, sessions share it). Channel scope (no `general/<repo>` or `memory/<repo>` prefixes: channels are plain names, kinds `ordinary`, `memory`, `tasks`; `general` exists on every bus; the project channel is whatever the team names it, for example `<repo>` and `<repo>-memory`; create with `create_channel` and `kind`). Private conversations (replaces "Direct messages", ADR 0006: a channel whose `acl` names the participants, `[{identity: pat, permissions: [read, write]}, {identity: sam, permissions: [read, write]}]`, created with `create_channel`; `set_acl` later; `everyone` and `admins` special names; the creator always holds `admin`). Task lists (a channel of kind `tasks`, the v1 lifecycle text otherwise). Subjects, Tags (v1 text minus the `dm` sentence and `persistent: true`; tag subscriptions belong to the identity and persist on the server). Searching (v1). Session protocol (`register` with an optional `hint`, pass the returned `session` on `receive` and on `send` for attribution; `receive` and ack; `discover`; `search` memory channels). Waiting for messages (Claude Code: `Monitor(timeout_ms: 1800000): agentbus2 watch`, no `-filter`, no `-as`; the session comes from the harness; exit 3 and 4 rules; "`wait_seconds` on `receive` replaces a running `agentbus2 watch` for your session, which then exits 3: on Claude Code never pass it while a watch is armed"; other harnesses: `agentbus2 wait` in a background shell; Codex: no idle wake). Idempotency (`idempotency_key` on every mutating tool, retries safe). What to post and where (v1 table with the `dm/` rows replaced by "a channel whose ACL names you both"). Subagents (same identity, `register` with `hint`, no `parent`). Checkpoints, What not to post, Common mistakes (the DM row replaced by the ACL channel; the "unsubscribed memory channels" row dropped). Every command in the file is `agentbus2`, every tool name is from choice 11, and the words `dm/`, `-filter`, `@<`, `memory/`, `tasks/`, `general/`, `persistent` do not occur.

- [ ] **Step 2: Check**

Run: `rg -n 'dm/|-filter|@<|memory/|tasks/|general/|persistent|agentbus ' internal/cli/skills/using-agentbus2/SKILL.md ; echo "exit=$?"` (expected `exit=1`), then `rg -c '`agentbus2 ' internal/cli/skills/using-agentbus2/SKILL.md` (at least 5).

- [ ] **Step 3: Commit**

```bash
git add internal/cli/skills/using-agentbus2/SKILL.md
git commit -m "docs(skill): the using-agentbus2 skill rewritten from v1 internal/cli/skills/using-agentbus/SKILL.md

No direct messages (ADR 0006): private conversations are channels with
an ACL; sessions share the profile's identity and register with a hint;
agentbus2 watch under Monitor without -filter (Decisions 12, 15, 42, 80)."
```

---
### Task 5: Connect layer: local and server modes; CLI scaffolding

**Depends on:** 1, 3 (and the external gate: Milestone 5 Task 10 on `main`).

**Files:**
- Create: `internal/client/doc.go`, `internal/client/options.go` (`Options`, `Flags`, `Resolve`), `internal/client/local.go` (the in-process composition), `internal/client/server.go` (the token source wiring), `internal/client/conn.go` (`Conn`, `Open`, `Close`), `internal/cli/doc.go`, `internal/cli/common.go`
- Test: `internal/client/resolve_test.go`, `internal/client/local_test.go`, `internal/client/server_test.go`, `internal/cli/common_test.go`

**Port from v1:** `internal/mcpserver/server.go` `StartBackgroundLoops` (the maintenance goroutine with a random first delay; the heartbeat loop is not ported: an armed watch renews the lease, Decision 42), `internal/bus/bus.go` `Open`'s data-directory creation. The in-process transport is the spike's need 6, wired here for the local profile (Decision 35).

**Interfaces (produces):**

```go
package client // internal/client
type Options struct { Environ []string; Cwd string /* "": os.Getwd */; Profile, Identity string /* the flags */; Maintenance bool /* local: run maintenance.Service.Tick on a ticker */; Logger *slog.Logger; Now func() time.Time /* tests only */; Notifier notify.Notifier /* tests only; nil: notify.NewPolling(PollInterval) */; PollInterval time.Duration /* 0: 1 s, v1 WaitPollInterval */; HTTP *http.Client /* server mode base client; nil: default with no Timeout */ }
func Flags(fs *flag.FlagSet, o *Options) // -profile "profile name (default: AGENTBUS2_PROFILE, then .local/agentbus2.json, then default)"; -identity "identity (default: AGENTBUS2_IDENTITY, then the profile's identity, then the profile's only credential)"
type Resolved struct { Profile profile.Profile; ProfileSource string; Identity, IdentitySource string; ConfigDir, DataDir string; Credentials credential.Store }
func Resolve(o Options) (Resolved, error) // ConfigDir, DataDir, profile.Load(ConfigDir/config.json), SelectName{Flag: o.Profile, Env: AGENTBUS2_PROFILE, Cwd}, Resolve, Store.List for the credential rule, ResolveIdentity{Flag: o.Identity, Env: AGENTBUS2_IDENTITY, Credentials}
type Conn struct { Bus bus.Bus; Resolved Resolved; Client *gen.ClientWithResponses; Core *core.Core /* local only */; Source *credential.Source /* server only */; close func() error }
func Open(ctx context.Context, o Options) (*Conn, error) // Resolve; by Mode: openLocal or openServer
func (c *Conn) Close() error
// local.go (Decisions 35, 36, 68; Milestone 5 choice 17): MkdirAll(DataDir, 0o700); sqlite.Open(ctx, profile.SQLitePath(p, DataDir), sqlite.Options{}); core.New(st, core.Defaults(), core.Options{Now, Notifier: o.Notifier or notify.NewPolling(PollInterval), Logger}); channels.Service{c}.EnsureGeneral(ctx); identities.Service{c}.RegisterLocal(ctx, identity); when o.Maintenance: a goroutine ticking maintenance.Service{c}.Tick every MaintenanceIntervalSeconds after a random first delay in (0, interval], stopped by Close; h := server.New(c, server.Options{Auth: server.InProcess{}, Registration: identities.ModeOpen, Logger}); gen.NewClientWithResponses("http://local", gen.WithHTTPClient(&http.Client{Transport: server.InProcessTransport(h, identity)})); Bus = remote.New(client); Close stops the ticker, waits for the goroutine, closes the store
// server.go (Decisions 19, 70, 84): Signer from Credentials.Load(identity) unless p.CredentialProcess is set (a missing credential file and no process → error `no credential for %q in profile %q: run agentbus2 register`); src := &credential.Source{Server, Identity, Signer, Process, Client: gen.NewClientWithResponses(p.Server, gen.WithHTTPClient(base)), HTTP: base, Cache: credential.Cache{Dir: DataDir/tokens/<profile>}, Now}; authed := gen.NewClientWithResponses(p.Server, gen.WithHTTPClient(&http.Client{Transport: credential.Transport(base.Transport or http.DefaultTransport, src)}), gen.WithRequestEditorFn(src.Editor())); Bus = remote.New(authed); nothing is exchanged until the first call
```

```go
package cli // internal/cli/common.go
// The command signature every Run<Name> implements (Milestone 5 cmd.Run's shape plus stdin):
//   func Run<Name>(ctx context.Context, args []string, environ []string, stdin io.Reader, stdout, stderr io.Writer) int
func newFlagSet(name string, stderr io.Writer) *flag.FlagSet // "agentbus2 "+name, flag.ContinueOnError, output stderr
func parseExit(err error) int                                   // flag.ErrHelp → 0, else 2 (v1 main.go)
func fail(stderr io.Writer, err error) int                      // "agentbus2: <code>: <message>" for a *bus.Error, "agentbus2: <err>" otherwise; returns 1
func printJSON(stdout io.Writer, v any) error                   // one indented document and a newline
func open(ctx context.Context, o client.Options, stderr io.Writer) (*client.Conn, int) // client.Open; on error prints through fail and returns (nil, 2) when the error is a profile, identity or credential resolution error (a usage error), (nil, 1) otherwise
```

- [ ] **Step 1: Write the failing tests**

`resolve_test.go` (`environ` with `HOME=<tmp>` and `AGENTBUS2_CONFIG_DIR`, a `config.json` written through `profile.Config.Save`): `TestResolveOrder` (flag profile over env over a repo file under `Cwd` over `default`; identity from the one credential file in server mode; the local default without identity → the three-way error), `TestResolveRepoFileError` (Review Focus 2 through `Resolve`). `local_test.go` (`Open` with `Environ` pointing at temp dirs, `PollInterval: 50ms`): `TestLocalOpenRegistersAndServes` (two `Open` calls as `pat` and `sam` on one profile: `ListChannels` shows `general`; `sam` sends on `general`, `pat` opens a session and receives it; `Close` both; the store file exists under the data dir; a third `Open` as `pat` is idempotent), `TestLocalWatchWakesAcrossConns` (the Decision 68 proof at this layer: `pat`'s `Conn` arms `Watch` with a 5 s timeout in a goroutine; a second `Conn` as `sam` over the same file sends; the watch wakes within 1 s; the negative half: the same test body with `Notifier: notify.NewMemory()` on both conns times out instead (asserted as `Woke false` after 1.5 s with `WatchMaxSeconds` left at the default and the test's `Timeout` 1 s), which is why production never passes a notifier), `TestLocalReplacedAcrossConns` (two conns as `pat` on the same session: the older `Watch` returns `replaced` within 500 ms), `TestLocalMaintenanceRuns` (`Maintenance: true`, `core.Defaults()` with `MaintenanceIntervalSeconds` 1 through a `Now`-driven fixture as Milestone 4's `TestServeStartsGeneralAndMaintenance`: a seeded expired receipt is gone within 3 s; a second `Conn` with `Maintenance: true` over the same file starts and only one holds the lease, observed through the Store), `TestLocalSecondOpenAfterCrash` (Definition of Done D: a `Conn` abandoned without `Close`; a new `Open` works). `server_test.go` (a real `cmd.Serve` on `127.0.0.1:0` with `Registration: identities.ModeOpen` and an issuer, as Milestone 5's `TestRunOverUnixSocket` builds one, or `cmd.Run` with `-listen 127.0.0.1:0`): `TestServerOpenExchangesAndCalls` (`remote.Register` through the test with a generated key saved by `credential.Store.Save`; `Open` with a server profile; `ListChannels` → `general`; the cache file exists afterwards; a second `Open` makes no exchange, asserted through the token endpoint's count captured by a wrapping `http.Client` transport counting `/v1/auth/token` requests), `TestServerOpenWithoutCredential` (the "run agentbus2 register" error), `TestServerOpenNeverExchangesBeforeFirstCall`. `common_test.go`: `TestFailFormatsBusError`, `TestParseExit`, `TestOpenUsageVersusRuntimeExit` (a bad profile → 2; a server profile pointing at a closed port → `ListChannels` fails later, but `open` itself returns a `Conn`: nothing is dialed at open).

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/client/ ./internal/cli/`
Expected: build failure (`undefined: client.Open`).

- [ ] **Step 3: Implement** as in Interfaces. State (Definition of Done D): two processes over one SQLite file are the Milestone 3 and 4 multi-process cases (lease row, watch row, polling); a start after a crash finds the lease expired after `2 * interval` (Milestone 4 Task 9); migrations run under the Decision 68 lock.

- [ ] **Step 4: Run the tests, the import rule and the gate**

Run: `go test -race ./internal/client/ ./internal/cli/`, then `go list -deps ./internal/cli | grep -E 'agentbus2/internal/(gen/spec|store/postgres)' ; echo "exit=$?"` (expected `exit=0`: `gen/spec` is linked through `internal/server` for the local profile, and this command only records the fact; `store/postgres` must not match: `grep -c store/postgres` prints 0), then `make verify` (600000 ms).
Expected: pass; no `store/postgres`; `verify: OK`.

- [ ] **Step 5: Commit**

```bash
git add internal/client/doc.go internal/client/options.go internal/client/local.go internal/client/server.go internal/client/conn.go internal/client/resolve_test.go internal/client/local_test.go internal/client/server_test.go internal/cli/doc.go internal/cli/common.go internal/cli/common_test.go
git commit -m "feat(client): one Bus per profile: the in-process server over the shared SQLite file in local mode, the token exchange with bearer refresh in server mode

Decisions 35, 36, 68 (local: polling notifier, RegisterLocal, the
maintenance loop ported from v1 internal/mcpserver/server.go
StartBackgroundLoops) and 19, 70, 84 (server). The CLI command signature
and shared helpers port v1 main.go's flag handling."
```

---

### Task 6: `init`, `register`, `login`, harness and skill installation

**Depends on:** 4, 5.

**Files:**
- Create: `internal/cli/init.go` (`RunInit`, `InitOptions`, the repo step), `internal/cli/install.go` (the harness configuration: MCP entry, hooks, skill, prompts; the `initer` port), `internal/cli/register.go` (`RunRegister`), `internal/cli/login.go` (`RunLogin`), `internal/cli/skill.go` (`//go:embed skills/using-agentbus2/SKILL.md`, `SkillMD`, `skillPath`)
- Test: `internal/cli/init_test.go`, `internal/cli/install_test.go`, `internal/cli/register_test.go`, `internal/cli/login_test.go`

**Port from v1:** `internal/cli/init.go` (`Init`, `initer`, `write` with `.bak`, `run`, `wants`, `global`, `claude`, `codex`, `grok`, `mcpEntry`, `codexTimeout`, `hook`, `hasHookCommand`, `gitignore`, `mcpConfigured`, `gitRoot`, `requireOnPath`, `InitPrompt`, `codexPrompt`, `grokRule`; the `agentbus2` names; no `SessionStart` hook, no `projectChannels`, no identity file), `internal/cli/subagenthook.go`'s `subagentHookCommand` constant moves here.

**Interfaces (produces):**

```go
package cli
const ( stopHookCommand = "agentbus2 stop-hook"; subagentHookCommand = "agentbus2 subagent-hook" )
var SkillMD []byte; var skillPath = filepath.Join("skills", "using-agentbus2", "SKILL.md")
const InitPrompt = `...` // the register-then-receive text of choice 11 without any file step
type InitOptions struct { Global bool; Harness string; DryRun bool; Profile, Identity string; Home, Cwd string; Environ []string; LookPath func(string) (string, error); Run func(string, ...string) error; OS string; Executable func() (string, error) }
func Init(o InitOptions, out io.Writer) error // choice 13: gitRoot(Cwd) != "" && !Global → repo(root); else global()
func RunInit(ctx context.Context, args []string, environ []string, stdin io.Reader, stdout, stderr io.Writer) int // flags -global, -harness, -dry-run, -profile, -identity
func RunRegister(ctx, args, environ, stdin, stdout, stderr) int // choice 14; flags from client.Flags plus -server, -enrollment-token, -algorithm (default EdDSA; "ES256"; anything else exit 2); a local profile → exit 2 "register applies to server profiles only; a local profile needs no registration"
func RunLogin(ctx, args, environ, stdin, stdout, stderr) int    // choice 14
```

- [ ] **Step 1: Write the failing tests**

`install_test.go`: the v1 `init_test.go` cases ported with the new names (`TestInitGlobalClaude` asserts `claude mcp add -s user agentbus2 -- agentbus2 mcp`, the `Stop` and `SubagentStart` hooks in `settings.json`, no `SessionStart`, the skill at `~/.claude/skills/using-agentbus2/SKILL.md` equal to `SkillMD`, `config.json` created with `profiles.default.mode == "local"` and left alone when present; `TestInitGlobalCodex`, `TestInitGlobalGrok` (no `SessionStart` hook, no rules file mentioning identity), `TestInitDryRun`, `TestHookIdempotent`, `TestRequireOnPath`). `init_test.go`: `TestInitRepoCreatesProfile` (`// pending Eric: Milestone 6 item 5`; a temp git repo `lab-repo` with `config.json` holding `default = {mode: server, server: "https://h:8443"}`: `.local/agentbus2.json` is `{"profile":"lab-repo"}`, `config.json` gains `lab-repo = {mode: server, server: https://h:8443, identity: lab-repo}`, `.gitignore` gains `.local/`, the output says to run `agentbus2 register`; a repo whose basename fails `NameRule` without `-identity` → an error naming `-identity`; an existing profile is left unchanged; `-profile x` selects `x`), `TestInitRepoLocalDefault` (no `config.json` → `lab-repo = {mode: local, identity: lab-repo}`). `register_test.go` (a `cmd.Serve` or `cmd.Run` server on `127.0.0.1:0` in open mode; `HOME` in `environ`): `TestRegisterCreatesServerProfile` (`// pending Eric: Milestone 6 item 6`; `-profile lab -server http://127.0.0.1:<port> -identity pat`: exit 0, the profile written, the credential file 0600 parsing to an Ed25519 key whose `KID` the server lists through the Store, the output line; a second run → exit 0 `existing key`; `-algorithm ES256` as `kim` → an ES256 key; `-algorithm RSA` → exit 2; a local profile → exit 2; a server in enrollment mode (Milestone 5's `-registration enrollment`) without `-enrollment-token` → exit 1 `forbidden` and no credential file written; with a token from `identities.Service.CreateEnrollmentToken` → exit 0). `login_test.go`: `TestLoginExchangesAndCaches` (after a register: exit 0, the line names the identity and the issuer, the cache file exists; a local profile → exit 2; a server profile without a credential → exit 2 naming `agentbus2 register`).

- [ ] **Step 2: Run to verify they fail**

Run: `go test -run 'Init|Register|Login|Hook|RequireOnPath' ./internal/cli/`
Expected: build failure (`undefined: RunInit`).

- [ ] **Step 3: Implement** as in Interfaces.

- [ ] **Step 4: Run the tests and the gate**

Run: `go test -race -run 'Init|Register|Login|Hook|RequireOnPath' ./internal/cli/`, then `make verify` (600000 ms).
Expected: pass; `verify: OK`.

- [ ] **Step 5: Commit**

```bash
git add internal/cli/init.go internal/cli/install.go internal/cli/register.go internal/cli/login.go internal/cli/skill.go internal/cli/init_test.go internal/cli/install_test.go internal/cli/register_test.go internal/cli/login_test.go
git commit -m "feat(cli): init, register and login: harness configuration, hooks and the skill ported from v1 internal/cli/init.go; per-repository profiles; key generation and TOFU or enrollment registration

Decisions 20, 21, 22, 33, 55 and Milestone 6 choices 13 and 14 (pending
items 5 and 6). No SessionStart hook and no identity file (Decision 14)."
```

---
### Task 7: The stdio MCP server

**Depends on:** 2, 5 (`go.mod` chain after Task 2).

**Files:**
- Create: `internal/mcpserver/doc.go`, `internal/mcpserver/inputs.go` (every tool input struct), `internal/mcpserver/server.go` (`NewServer`, the tools), `internal/mcpserver/sessions.go` (the session registry, the state file, resume), `internal/mcpserver/run.go` (`Run`, the log file, signals), `internal/mcpserver/envelope.go` (`result`, `wrapSchemaErrorsInEnvelope`)
- Modify: `go.mod`, `go.sum` (`github.com/modelcontextprotocol/go-sdk` v1.7.0)
- Test: `internal/mcpserver/server_test.go` (in-process over `mcp.NewInMemoryTransports`, local profile on a temp data dir), `internal/mcpserver/sessions_test.go`, `internal/mcpserver/run_test.go` (the stdio process through `os.Args[0]` re-exec and the SDK's `mcp.CommandTransport`, v1 `integration_test.go`'s pattern)

**Port from v1:** `internal/mcpserver/server.go` (`result`, `wrapSchemaErrorsInEnvelope`, `newServer`'s tool table and descriptions re-pointed at the `Bus` interfaces, `Run`'s signal handling and ordered shutdown, the `init` prompt), `logfile.go` (`OpenLog` reduced to one file per process, choice 11). Not ported: `applyPersistent`, `persistFile`, `persistErr`, `defaultContextFor`, `StartBackgroundLoops`' heartbeat, the `register` arguments `name`, `parent`, `context`, `resume`.

**Interfaces (produces):**

```go
package mcpserver // imports internal/client, internal/bus, internal/sessionstate, internal/procs, internal/profile; never internal/gen
type Options struct { Client client.Options; DataDir string /* "": from Client */; Table procs.Table /* nil: procs.System */; Self int /* 0: os.Getpid */; Log *slog.Logger }
func NewServer(ctx context.Context, o Options) (*mcp.Server, func() error, error) // builds the Conn lazily on the first tool call (so an unresolvable profile serves; choice 11); the returned func closes every open session's state file and the Conn
func Run(ctx context.Context, o Options) error // OpenLog, FindHarness(Table, Self) (ErrGone or an error: logged, the harness ref zero), sessionstate.Sweep, NewServer, server.Run(ctx, &mcp.StdioTransport{}), close; never writes stdout or stderr
func OpenLog(dataDir string, pid int) (*slog.Logger, func(), error) // dataDir/logs/mcp-<pid>.log, O_APPEND|O_CREATE 0o600; the func closes and removes it
// inputs.go: registerIn{Hint, ResumeToken string}, sessionIn{Session string}, createChannelIn{Name, Kind string; ACL []aclEntryIn; IdempotencyKey string}, aclEntryIn{Identity string; Permissions []string}, channelIn{Session, Name, IdempotencyKey}, renameIn{Name, NewName, IdempotencyKey}, setACLIn{Channel string; Entries []aclEntryIn; IdempotencyKey}, subscribeIn{Channel, From string; Tags []string; IdempotencyKey}, unsubscribeIn{Channel string; Tags []string; IdempotencyKey}, sendIn{Session string; bus.SendInput}, receiveIn{Session, Ack string; Count int; IncludeOwn bool; WaitSeconds float64}, historyIn{Channel; Before, After *int64; Count; Tags}, searchIn{bus.SearchInput}, memoryIn{ID int64}, editIn{bus.EditMemoryInput}, deleteIn{ID int64; IdempotencyKey}, taskCreateIn{bus.TaskCreateInput}, taskClaimIn{TaskID, LeasedUntil int64; IdempotencyKey}, taskReleaseIn{TaskID int64; IdempotencyKey}, taskUpdateIn{bus.TaskPatch}, taskGetIn{TaskID int64}, taskListIn{bus.TaskListInput}; every struct carries `json` snake_case and `jsonschema` descriptions (hand-written; the v1 descriptions minus DMs, prefixes and persistence)
// register result: {session, identity, display_name, resume_token, resumed, pending, rejected_cursors, others}
```

- [ ] **Step 1: Write the failing tests**

`server_test.go` (a `client.Options` over temp dirs with a local profile `identity: pat`; the SDK's in-memory client; a second `client.Open` as `sam` for the sender side): `TestToolListIsExact` (the twenty-four names of choice 11 and nothing else; every description free of `dm/`, `persistent`, `tasks/`), `TestRegisterOpensSession` (`hint` absent → `display_name pat`, a 43-character `resume_token`, `resumed false`, a state file `Primary` with `MCPPID` = `Self`; a second `register` with `hint impl` → `pat/impl`, not primary; `others` lists `pat`), `TestReceiveAckRoundTrip` (`sam` sends on `general`; `receive{session}` → the message and a batch token; `receive{session, ack}` → empty, `ack_ignored false`; `idempotency_key` on `send` twice → the same `seq`), `TestReceiveWaitSecondsReplacesWatch` (`// pending Eric: Milestone 6 item 3`; `receive{wait_seconds: 2}` with nothing pending, then `sam` sends after 300 ms → the message within 2 s; while a `Watch` on the same session is armed through a second `Conn` as `pat`, `receive{wait_seconds: 1}` makes that watch return `replaced`), `TestIdempotencyKeyReachesTheHeader` (an `httptest.Server` counting the `Idempotency-Key` header would need server mode; instead: `create_channel` twice with the same key and name → the second replays the first (no `conflict`), which only a receipt explains, and `delete_memory` with a key on a memory channel likewise), `TestSchemaErrorsWrapped` (a `send` with `content` of the wrong type → `{"code":"validation",...}` in the error content), `TestToolsReportUnresolvedIdentity` (Review Focus 3: a profile without identity and no flag → every tool answers `validation` with the three-way text; the server itself is up), `TestACLToolsRoundTrip` (`create_channel` with an `acl` naming `pat` only; `get_acl`; `set_acl` adding `sam`; `sam` reads through the second conn), `TestCloseSessionRemovesStateFile`. `sessions_test.go`: `TestRegisterResumesAfterRestart` (Decision 79, Review Focus 4: a state file for harness `{Self's harness ref}` with a dead `MCPPID` and a valid token (from a first `NewServer` whose closer did not run, simulating a crash) → `register` without hint answers `resumed true`, the same session id, the file rewritten with the new pid; with a hint → a new session; a file of another harness → ignored), `TestRegisterPastRetention` (`core.Defaults()` with `SessionRetentionHours: 1` through a `Now` the test advances two hours and a maintenance tick that deletes the row: `register` answers `resumed false` and the file holds the new session), `TestSessionLapsedResumesOnce` (the lease lapsed on the clock: `receive` first answers by resuming transparently with the token and then receiving; a closed session → `session_closed` surfaced). `run_test.go` (re-exec with `AGENTBUS2_MCP_HELPER=1`, the SDK `CommandTransport`; the child's `environ` from the test with `HOME=<tmp>`): `TestRunServesStdio` (`tools/list` succeeds; `register`; nothing on the child's stderr; the log file exists while it runs and is gone after a clean close), `TestRunSurvivesHarnessLookupFailure` (a `Table` that errors: the server still serves; the state file carries a zero harness ref).

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/mcpserver/`
Expected: build failure (`undefined: mcpserver.NewServer`).

- [ ] **Step 3: Implement**; `go get github.com/modelcontextprotocol/go-sdk@v1.7.0 && go mod tidy`. The session registry maps session id → `sessionstate.File`; `receive`, `send` and `close_session` look the id up and resume once on `session_lapsed` (choice 10). State (Definition of Done D): two MCP servers under one harness each write their own primary file and the hook picks the earliest; a restart under the same harness resumes; a crash's file is resumed by the next start under the same harness or swept once the harness dies.

- [ ] **Step 4: Run the tests, the import rule and the gate**

Run: `go test -race ./internal/mcpserver/`, then `go list -deps ./internal/mcpserver | grep -E 'agentbus2/internal/gen$' ; echo "exit=$?"` (expected `exit=1`), then `make verify tidy-check` (600000 ms).
Expected: pass; `verify: OK`; `tidy-check` clean.

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum internal/mcpserver/doc.go internal/mcpserver/inputs.go internal/mcpserver/server.go internal/mcpserver/sessions.go internal/mcpserver/run.go internal/mcpserver/envelope.go internal/mcpserver/server_test.go internal/mcpserver/sessions_test.go internal/mcpserver/run_test.go
git commit -m "feat(mcp): the stdio MCP server ported from v1 internal/mcpserver/server.go over the Bus interfaces, with the session state file and resume

Decisions 3, 47, 66, 78, 79, 83: hand-written tool schemas, register
opens a session with a hint and no cwd, idempotency_key travels to the
header through the remote Bus, wait_seconds is watch then receive
(Milestone 6 pending item 3), the state file resumes an MCP restart."
```

---

### Task 8: `watch`, `wait`, `stop-hook`, `subagent-hook` and the exit-code mapping

**Depends on:** 2, 5.

**Files:**
- Create: `internal/cli/exit.go` (`errReplaced`, `errHarnessGone`, `errTimeout`, `exitCode`), `internal/cli/session_locate.go` (`locateSession`), `internal/cli/watch.go` (`RunWatch`, `watchLine`, `watchMinGap`), `internal/cli/wait.go` (`RunWait`), `internal/cli/stophook.go` (`RunStopHook`, `hookBudget`), `internal/cli/subagenthook.go` (`RunSubagentHook`, `subagentName`)
- Test: `internal/cli/watch_test.go`, `internal/cli/wait_test.go`, `internal/cli/stophook_test.go`, `internal/cli/subagenthook_test.go`

**Port from v1:** `internal/cli/watch.go` (`Watch`'s loop, `watchLine`, `watchMinGap`), `internal/cli/wait.go` (`Wait`'s exit mapping, `startWaiter`'s harness watch, `waitCanceled`; not the lock, not `waitMatch`), `internal/cli/stophook.go` (`stopHookInput`, `claudeCodeClient`, the block reason text with `agentbus2 watch` and no `-filter`), `internal/cli/subagenthook.go` (`subagentName`, the context text with `hint`), v1 `main.go`'s `wait` and `watch` cases (the signal context and the exit switch).

**Interfaces (produces):**

```go
package cli
var ( errReplaced = errors.New("replaced"); errHarnessGone = procs.ErrHarnessGone; errTimeout = errors.New("timeout") )
func exitCode(err error) int // nil → 0; errTimeout → 1; errReplaced or a *bus.Error with CodeReplaced → 3; errHarnessGone → 4; anything else → 2 (the spec 3.5 table; Task 11's self-test block mutates this function)
type located struct { Session, DisplayName string; Harness procs.Ref; HarnessFound bool }
func locateSession(o client.Options, dataDir, flagSession string, t procs.Table, self int) (located, error) // flagSession != "" → it (the harness still looked up for the watch); else FindHarness then sessionstate.ForHarness; none → error `no session for this harness; pass -session or start agentbus2 mcp` (ErrGone from FindHarness → errHarnessGone)
func RunWatch(ctx context.Context, args []string, environ []string, stdin io.Reader, stdout, stderr io.Writer) int // choice 9; flags client.Flags, -session, -include-own, -no-harness-watch; signal.NotifyContext(SIGINT, SIGTERM, SIGHUP) → errReplaced; the harness watch goroutine (procs.WatchHarness) cancels with errHarnessGone; the loop: Watch{After: floor, Timeout: 0, IncludeOwn}; a wake → watchLine, floor = Seq, sleep watchMinGap; Woke false → re-arm; an error → exitCode; a failed stdout write → 2
func watchLine(display string, r bus.WatchResult) string // "agentbus2: %d new message(s) for %s on %s; call receive", channels sorted and deduplicated
var watchMinGap = 5 * time.Second
func RunWait(ctx, args, environ, stdin, stdout, stderr) int // flags as watch plus -timeout; Watch{Timeout: min(remaining, 0 → cap)} in a loop until a wake (print the JSON line, exit 0), the deadline (exit 1), or an error (as watch)
var hookBudget = 500 * time.Millisecond
func RunStopHook(ctx, args, environ, stdin, stdout, stderr) int // choice 8: always 0; decode v1's stopHookInput; stop_hook_active → nothing; context.WithTimeout(hookBudget); locateSession; client.Open with the file's profile and identity (Options.Profile/Identity from the file: never from flags or the repo file); Open{ResumeToken}; sum Pending; 0 → nothing; else the v1 block reason: "Agentbus2: <n> new message(s) for <display> on <channels>. Call receive (ack the previous batch) and handle what concerns you. " + (harness_client == "claude-code" ? "Then make sure an `agentbus2 watch` is running under the Monitor tool with timeout_ms 1800000 (start one if none is running; it replaces any running watch or wait) so the next message wakes you, and stop." : "Then, except on Codex, make sure a background `agentbus2 wait` is running (start one; it replaces any running wait) so the next message wakes you, and stop."); any error or the deadline → nothing, 0
func RunSubagentHook(ctx, args, environ, stdin, stdout, stderr) int // always 0; decode {cwd, agent_type}; locateSession (no bus call); the additionalContext text of choice 15 naming the register tool, `hint` = subagentName(agent_type), "pass the returned session on every later agentbus2 call", "do not start agentbus2 wait or watch"
```

- [ ] **Step 1: Write the failing tests**

Every test uses a local profile on temp dirs (`HOME` in `environ`), a fake `procs.Table` (the Task 2 helper, copied into `internal/cli` as `faketable_test.go`), `watchMinGap` 40 ms and `procs.HarnessPollInterval` 10 ms, `PollInterval` 50 ms, a session opened through a `client.Conn` as `pat` and its state file written by the test with the fake harness ref `{10, 5}`; a sender `Conn` as `sam`; the command run in a goroutine with a `syncBuf` stdout (v1 `watch_test.go`'s fixture). `watch_test.go`: `TestWatchFlags` (`// pending Eric: Milestone 6 item 4`; `-channel` and `-filter` are unknown flags → exit 2; `-h` → 0), `TestWatchPrintsOneLinePerWakeAndNeverExits` (three sends 100 ms apart → lines matching `^agentbus2: \d+ new message\(s\) for pat on general; call receive$`, the first naming the backlog once, at least `watchMinGap` apart, the process still running 500 ms after the last; cancel through SIGTERM's path (`ctx` cancel) → exit 3 and nothing more written), `TestWatchReplacedExits3Silent` (a second `RunWatch` on the same session → the first exits 3 within 1 s with empty stdout and stderr; the second keeps running), `TestWatchHarnessGoneExits4` (the fake table's `StartTime` for pid 10 starts answering `ErrGone` → exit 4 within 200 ms, nothing written; with `-no-harness-watch` it keeps running), `TestWatchErrorExits2` (a server profile at a closed port → exit 2 with `agentbus2:` on stderr; no session for the harness and no `-session` → exit 2 naming `-session`), `TestWatchNeverExits0Or1` (a test double `bus.Sessions` whose `Watch` answers `Woke true` at once, forever: `RunWatch` keeps printing lines `watchMinGap` apart and returns only when its context is canceled, with 3; the `0` and `1` rows of the table belong to `wait` alone). `wait_test.go`: `TestWaitWokeExits0` (a send → one JSON line `{"seq":..,"count":1,"channels":["general"]}` and 0 within 1 s), `TestWaitTimeoutExits1` (`-timeout 300ms` → 1, empty stdout), `TestWaitReplacedExits3`, `TestWaitHarnessGoneExits4`, `TestWaitErrorExits2`, `TestWaitSessionFromFlag` (`-session` with no state file and no harness in the fake table). `stophook_test.go`: `TestStopHookBlocksWhenPending` (two sends from `sam`; stdin `{"cwd":"","stop_hook_active":false}`; stdout one JSON object with `decision block` and a reason containing `2 new message(s) for pat on general` and `agentbus2 watch` when the file's `harness_client` is `claude-code`, the `wait` text otherwise), `TestStopHookPeeksWithoutSideEffects` (`// pending Eric: Milestone 6 item 2`; after the hook, a `Watch` on the session through the `Conn` still wakes for those messages (the cursor and pending token are untouched) and the Watch registration is not replaced (a watch armed before the hook is still live after it)), `TestStopHookSilentCases` (`stop_hook_active true`; nothing pending; no state file; a malformed stdin; a profile that no longer resolves: each prints nothing, exit 0), `TestStopHookFailsOpenWithinBudget` (Review Focus 5: a server profile at an `httptest.Server` whose handler blocks until the test ends: the hook returns 0 with empty stdout within `hookBudget + 300ms`; `hookBudget` lowered to 200 ms for the test). `subagenthook_test.go`: `TestSubagentHookContext` (a registered parent → `hookSpecificOutput.additionalContext` naming `register`, a `hint` matching `^code-reviewer-[0-9a-f]{4}$` for `agent_type "Code Reviewer"`, the word `session`, no `parent=`; no state file → nothing), `TestSubagentName` (v1's cases).

- [ ] **Step 2: Run to verify they fail**

Run: `go test -run 'Watch|Wait|StopHook|Subagent' ./internal/cli/`
Expected: build failure (`undefined: RunWatch`).

- [ ] **Step 3: Implement** as in Interfaces. `exitCode` holds the line `case errors.Is(err, errReplaced), bus.CodeOf(err) == bus.CodeReplaced:` followed by `return 3` on its own line, verbatim (Task 11's self-test block 16 rewrites `return 3` under that case).

- [ ] **Step 4: Run the tests and the gate**

Run: `go test -race -run 'Watch|Wait|StopHook|Subagent' ./internal/cli/`, then `make verify` (600000 ms).
Expected: pass; `verify: OK`.

- [ ] **Step 5: Commit**

```bash
git add internal/cli/exit.go internal/cli/session_locate.go internal/cli/watch.go internal/cli/wait.go internal/cli/stophook.go internal/cli/subagenthook.go internal/cli/faketable_test.go internal/cli/watch_test.go internal/cli/wait_test.go internal/cli/stophook_test.go internal/cli/subagenthook_test.go
git commit -m "feat(cli): watch and wait over the long-poll with the spec 3.5 exit codes, the Stop and SubagentStart hooks located through the process tree

Ports v1 internal/cli/watch.go, wait.go, stophook.go and subagenthook.go
(Decisions 10, 41, 42, 78; v1 ADR 0021, issue ericfitz/agentbus#38): no
-filter, no -channel, no wait lock; the session comes from the state
file; the hook peeks by resuming (Milestone 6 pending items 2 and 4) and
fails open within hookBudget."
```

---
### Task 9: Operator commands: `session`, `channels`, `send`, `receive`, `history`, `search`

**Depends on:** 5.

**Files:**
- Create: `internal/cli/session.go` (`RunSession`), `internal/cli/channels.go` (`RunChannels`), `internal/cli/send.go` (`RunSend`), `internal/cli/receive.go` (`RunReceive`), `internal/cli/history.go` (`RunHistory`), `internal/cli/search.go` (`RunSearch`)
- Test: `internal/cli/session_test.go`, `internal/cli/channels_test.go`, `internal/cli/messages_test.go` (send, receive, history, search)

**Port from v1:** `internal/cli/delete.go` (`DeleteChannel`'s y/N prompt and `-y` into `channels delete`), `main.go`'s `splitTags` and the `subscribe`/`unsubscribe` grammar (`-tags a,b` as a tag set).

**Interfaces (produces):** each `Run<Name>(ctx, args, environ, stdin, stdout, stderr) int` with `client.Flags` and these grammars (choice 12; every result through `printJSON`):

```
agentbus2 session open [-hint H] [-resume-token T]          → bus.Session as JSON (session, display_name, resume_token, resumed, pending)
agentbus2 session close [-key K] ID
agentbus2 channels list
agentbus2 channels create [-kind ordinary|memory|tasks] [-acl JSON] [-key K] NAME   (-acl: a JSON array of {identity, permissions}; absent → nil → the default ACL; "[]" → an empty list)
agentbus2 channels delete [-y] [-key K] NAME                  (the prompt names the channel; `delete cancelled` exit 1 on anything but y/yes)
agentbus2 channels rename [-key K] OLD NEW
agentbus2 channels acl NAME                                   → the entries
agentbus2 channels acl -set JSON [-key K] NAME
agentbus2 channels subscribe [-from now|oldest] [-key K] NAME
agentbus2 channels unsubscribe [-key K] NAME
agentbus2 channels tags                                       → the identity's tag sets
agentbus2 channels tags -subscribe a,b | -unsubscribe a,b [-key K]
agentbus2 send [-subject S] [-type T] [-tag T]... [-reply-to SEQ] [-metadata k=v]... [-session ID] [-key K] CHANNEL [CONTENT|-]   (- or no CONTENT: stdin)
agentbus2 receive [-session ID] [-ack TOKEN] [-count N] [-include-own] [-no-ack]
agentbus2 history [-before N] [-after N] [-count N] [-tag T]... CHANNEL
agentbus2 search [-mode text|semantic|both] [-channel C] [-sender S] [-since MS] [-until MS] [-thread SEQ] [-tag T]... [-count N] [-cursor C] QUERY
```

Values are passed to the `Bus` as given (`-kind bogus` reaches the core and comes back `validation`; the CLI validates only its own grammar: a `-metadata` without `=`, a `-acl` that is not a JSON array of the entry shape, a `-count` that is not an integer).

- [ ] **Step 1: Write the failing tests**

A local profile on temp dirs, `pat` and a sender `Conn` as `sam`. `session_test.go`: `TestSessionOpenClose` (open prints the JSON with a 43-character token; close → exit 0; close again → 0 (idempotent); `-hint a/b` → exit 1 `validation`). `channels_test.go`: `TestChannelsLifecycle` (list shows `general`; create `dev` ordinary; create `notes -kind memory`; create `-kind bogus` → exit 1 with `validation` on stderr; `acl dev` lists the default rows; `acl -set '[{"identity":"pat","permissions":["read","write","admin"]}]' dev` then `acl dev` equals it; `-acl 'nope'` → exit 2; rename `dev` `dev2`; subscribe `dev2 -from oldest`; unsubscribe; tags `-subscribe deploy,env:*` then `tags` lists it; `delete -y dev2`; delete of `notes` with stdin `n` → exit 1 `delete cancelled` and the channel kept; with stdin `y` → deleted), `TestChannelsUsage` (no subcommand → 2; unknown → 2; `-h` → 0). `messages_test.go`: `TestSendReceiveAck` (`send general hello -subject s -tag deploy -key k1` as `sam` → `{seq}`; the same again → the same seq; `receive` as `pat` (no `-session`) → the message with `subject s` and `tags [deploy]`, and the throwaway session is closed afterwards (the identities list shows no `pat` session); `receive -no-ack` then `receive` → redelivered; `-session S -ack T`), `TestSendFromStdin` (`send general -` with stdin), `TestHistoryAndSearch` (three sends; `history -count 2 general` → the last two; `-tag deploy` filters; `search hello` → one hit; `-mode bogus` → exit 1 `validation`), `TestSendAttribution` (`send -session S` from `pat`'s own session: `receive` on that session with `-include-own` absent does not return it; with `-include-own` it does).

- [ ] **Step 2: Run to verify they fail**

Run: `go test -run 'Session|Channels|Send|Receive|History|Search' ./internal/cli/`
Expected: build failure (`undefined: RunSession`).

- [ ] **Step 3: Implement** as in Interfaces.

- [ ] **Step 4: Run the tests and the gate**

Run: `go test -race -run 'Session|Channels|Send|Receive|History|Search' ./internal/cli/`, then `make verify` (600000 ms).
Expected: pass; `verify: OK`.

- [ ] **Step 5: Commit**

```bash
git add internal/cli/session.go internal/cli/channels.go internal/cli/send.go internal/cli/receive.go internal/cli/history.go internal/cli/search.go internal/cli/session_test.go internal/cli/channels_test.go internal/cli/messages_test.go
git commit -m "feat(cli): session, channels, send, receive, history and search commands over the Bus

Ports the confirmation prompt of v1 internal/cli/delete.go and the tag
grammar of v1 main.go; ACL get and set on the command line (Decision 80);
every value is judged by the core, never filtered client-side."
```

---

### Task 10: Operator commands: `memories`, `tasks`, `status`

**Depends on:** 5.

**Files:**
- Create: `internal/cli/memories.go` (`RunMemories`), `internal/cli/tasks.go` (`RunTasks`), `internal/cli/status.go` (`RunStatus`)
- Test: `internal/cli/memories_test.go`, `internal/cli/tasks_test.go`, `internal/cli/status_test.go`

**Port from v1:** `internal/cli/status.go` (`Status`'s report shape: the profile and mode replace `config` and `data`; sessions and channels as v1 prints them; the embedding lines from `bus.Status` when the identity is an administrator).

**Interfaces (produces):**

```
agentbus2 memories get ID
agentbus2 memories edit [-subject S] [-clear-subject] [-type T] [-tag T]... [-clear-tags] [-metadata k=v]... [-key K] ID [CONTENT|-]
agentbus2 memories delete [-key K] ID
agentbus2 memories revisions ID
agentbus2 tasks create [-description D] [-parent ID] [-before ID] [-after ID] [-blocked-by ID]... [-metadata k=v]... [-key K] CHANNEL SUBJECT
agentbus2 tasks list [-status S] [-owner O] CHANNEL
agentbus2 tasks get ID
agentbus2 tasks update [-status S] [-owner O] [-clear-owner] [-subject S] [-description D] [-parent ID] [-before ID] [-after ID] [-add-blocked-by ID]... [-remove-blocked-by ID]... [-leased-until MS] [-metadata k=v]... [-delete] [-force] [-key K] ID
agentbus2 tasks claim [-leased-until MS] [-key K] ID
agentbus2 tasks release [-key K] ID
agentbus2 status                                              (human-readable: "profile: <name> (<mode>, <server|sqlite path>)", "identity: <name>", the live identities and their sessions from Identities, every channel from ListChannels; when Admin.Status succeeds, its notices, the embedding backlog and rejections and the channels' creators; a forbidden Status is silent)
```

`-subject` absent keeps the subject and `-clear-subject` sends a pointer to `""` (`bus.EditMemoryInput.Subject` semantics); `-tag` absent keeps the tags and `-clear-tags` sends an empty list; `-owner` absent keeps the owner and `-clear-owner` sends a pointer to `""` (`bus.TaskPatch` semantics).

- [ ] **Step 1: Write the failing tests**

`memories_test.go`: `TestMemoriesLifecycle` (a `notes -kind memory` channel through the `Conn`; `send` through the `Conn` → a `memory_id`; `get`; `edit ID "v2" -tag kind:gotcha -key k` → `{revision, replaced}`, twice the same; `revisions` → two; `edit -clear-subject`; `delete`; `get` → exit 1 `not_found`). `tasks_test.go`: `TestTasksLifecycle` (a `todo -kind tasks` channel; `create todo "Add tags"` as `sam`; `create -parent 1`; `list todo` → two rows with `depth`; `claim 2` as `pat`; `claim 2` as `sam` → exit 1 `conflict`; `update -status completed 2`; `release 1`; `update -delete 1`; `get 1` → exit 1 `not_found`; `create` on an ordinary channel → exit 1 `validation`). `status_test.go`: `TestStatusReport` (as `pat` with a session open: the profile line names `local` and the SQLite path, `identity: pat`, the session listed, `general` listed; as an admin (`SetAdmin` through the Store on the local core) the embedding lines appear).

- [ ] **Step 2: Run to verify they fail**

Run: `go test -run 'Memories|Tasks|Status' ./internal/cli/`
Expected: build failure (`undefined: RunMemories`).

- [ ] **Step 3: Implement** as in Interfaces.

- [ ] **Step 4: Run the tests and the gate**

Run: `go test -race -run 'Memories|Tasks|Status' ./internal/cli/`, then `make verify` (600000 ms).
Expected: pass; `verify: OK`.

- [ ] **Step 5: Commit**

```bash
git add internal/cli/memories.go internal/cli/tasks.go internal/cli/status.go internal/cli/memories_test.go internal/cli/tasks_test.go internal/cli/status_test.go
git commit -m "feat(cli): memories, tasks and status commands over the Bus

Ports the report of v1 internal/cli/status.go; memory revisions and task
leases are the core's rules (spec section 9), reached through the Bus."
```

---

### Task 11: Dispatch, milestone exit end to end, exit-code table, negative halves, docs

**Depends on:** 6, 7, 8, 9, 10.

**Files:**
- Create: `internal/cli/cli.go` (`Main`, the dispatch table, `usage`), `internal/e2e/e2e_test.go`, `internal/e2e/helper_test.go` (`TestHelperHarness`), `scripts/m6-exit-checks.sh`
- Modify: `main.go` (`run` delegates every command but `server` to `cli.Main`; `server` stays `cmd.Run(..., os.Environ(), ...)`), `main_test.go` (`TestUsageListsEveryCommand`), `scripts/check-gates-selftest.sh` (block 16), `.github/workflows/ci.yml` (the `gates-selftest` job gains `- run: bash scripts/m6-exit-checks.sh --deps-only`), `CLAUDE.md`, `README.md`, `docs/adr/0002-authentication-and-credentials.md`, `docs/adr/0003-identity-sessions-and-delivery.md`, `docs/adr/0001-transport-and-api.md` (Milestone 6 addenda), `HANDOFF.md` (untracked)

**Port from v1:** `main.go` (`usage`, `run`'s dispatch; `help`, `version`), `internal/mcpserver/integration_test.go` (the built-binary pattern for the end-to-end test).

**Interfaces (produces):**

```go
package cli
func Main(ctx context.Context, args []string, environ []string, stdin io.Reader, stdout, stderr io.Writer) int // args[0] one of: init, register, login, mcp, watch, wait, stop-hook, subagent-hook, session, channels, send, receive, history, search, memories, tasks, status, help, version; "mcp" → mcpserver.Run(ctx, mcpserver.Options{Client: options parsed from client.Flags}) with exit 1 on error and nothing on stdout; unknown → 2 with the usage on stderr
const usage = "usage: agentbus2 <init|register|login|mcp|server|watch|wait|stop-hook|subagent-hook|session|channels|send|receive|history|search|memories|tasks|status|version|help> [flags]"
```

- [ ] **Step 1: Wire the dispatch**

`cli.go` and `main.go` as above; `main_test.go`: `TestUsageListsEveryCommand` (a literal list of twenty names, the nineteen `Main` cases plus `server`, each present in `usage` and each answered by `run` with something other than the unknown-command exit). `rg -n 'pending Eric: Milestone 6' internal/` lists exactly items 1 to 7, one test each.

- [ ] **Step 2: The exit test**

`internal/e2e/e2e_test.go` (`package e2e_test`): `TestMain` builds the binary once (`go build -trimpath -o <tmp>/agentbus2 .` from the module root with `CGO_ENABLED=0`; a build failure fails the package) and exposes its path; every subprocess gets an `environ` of `PATH`, `HOME=<tmp>/home-<role>` and nothing else of the developer's environment. `TestSection24Sequence` (the exit criterion, a harness on one host with a server on another, simulated with a non-loopback-classified listener; each step a subtest in this order): (0) `server`: `agentbus2 server -listen 0.0.0.0:0 -sqlite <tmp>/server.sqlite -set signing_key_file=<tmp>/key.pem -set session_retention_hours=1` as a subprocess, the `listening on` line parsed for `<port>` and asserted to say `registration enrollment` (so no client step below can register without a token, the "another host" condition); three tokens through `agentbus2 server admin enrollment create -sqlite <tmp>/server.sqlite`, each parsed from its `token: abe_...` line; (1) `register`: `agentbus2 register -profile lab -server http://127.0.0.1:<port> -identity pat -enrollment-token T1` under `HOME` A → exit 0, `config.json` holds `lab`, the credential file is 0600; `-identity sam` with `T2` under `HOME` B; `register` as `kim` with no token under `HOME` A → exit 1 `forbidden` (the negative half of the enrollment condition); (2) `token`: `agentbus2 login -profile lab -identity pat` → exit 0 and the cache file under `HOME` A's data dir; (3) `open-session`: `agentbus2 session open -profile lab -identity pat -hint e2e` → `S` and the resume token parsed from the JSON; (4) `watch`: `agentbus2 wait -profile lab -identity pat -session S -timeout 30s` started (stdout piped) and, in parallel, `agentbus2 watch -profile lab -identity pat -session S2` on a second session `S2` opened the same way; (5) `send-from-another-identity`: after 300 ms `agentbus2 send -profile lab -identity sam general "hello from sam" -subject e2e -key e2e-1` under `HOME` B → `{seq}`; (6) `wake`: the `wait` exits 0 within 5 s with one JSON line `{"seq":<seq>,"count":1,"channels":["general"]}` and nothing else on stdout; the `watch` printed one line matching `^agentbus2: 1 new message\(s\) for pat-2 on general; call receive$` (the second session's display name) and is still running 1 s later; (7) `receive`: `agentbus2 receive -profile lab -identity pat -session S -no-ack` → the message with `subject e2e` and a batch token `B`; (8) `ack`: `agentbus2 receive -session S -ack B` → no messages, `ack_ignored false`; a further `receive -no-ack` → nothing (the batch is consumed); (9) `stale-bearer-refreshes` (Review Focus 1): the cache file's `access_token` under `HOME` A is overwritten with a token minted by a second `token.NewIssuer` over a different key, and `agentbus2 channels list -profile lab -identity pat` still exits 0 (one refresh); (10) `mcp-over-stdio`: `agentbus2 mcp -profile lab -identity pat` under `HOME` A driven by the SDK's `CommandTransport`: `register` → a session with `display_name` `pat-3` and a state file whose `harness_pid` is the test process; `receive` after a second `send` from `sam` → the message; the process is closed and restarted with the same arguments; `register` → `resumed true` and the same session id (Decision 79 within the retention window); (11) `stop-hook`: while the restarted MCP process of step 10 is still open (it removes its state file on exit), `agentbus2 stop-hook` run from the test process (its parent and thus its harness is the test binary, the same ref the MCP server recorded) with stdin `{"stop_hook_active":false}` after a third `send` from `sam` → one JSON object with `decision block` and `1 new message(s) for pat-3`; after the MCP client receives and acks → the hook prints nothing; (12) `watch-ends-only-by-signal`: SIGTERM to the `watch` of step 4 → exit 3 with nothing further on stdout. The server is stopped with SIGTERM and exits 0. `TestExitCodeTable` (spec 3.5, one subtest per cell, all against the same server and `HOME` A with fresh sessions): `wait-0-woke` (step 6 again in isolation), `wait-1-timeout` (`-timeout 1s` → 1, empty stdout), `wait-2-error` (`-profile nosuch` → 2 with `agentbus2:` on stderr; no `-session` under a fresh `HOME` C with no state file, the test binary as harness → 2 naming `-session`), `wait-3-replaced` (two `wait`s on one session: the first exits 3 within 2 s with empty stdout and stderr), `wait-3-signal` (SIGINT → 3), `wait-4-harness-gone` (through `TestHelperHarness`: the test runs `os.Args[0] -test.run=^TestHelperHarness$` with `AGENTBUS2_E2E_HELPER=1`, the binary path, the arguments and two output file paths in its environment; the helper runs `sh -c '"$AGENTBUS2_BIN" wait ... > "$OUT"; echo $? > "$CODE"'` without waiting and then blocks on stdin; the test closes the helper's stdin so it exits; the orphaned `sh` keeps `wait` alive, `wait` finds its harness ref gone within 3 s, exits 4, and `CODE` reads `4` with `OUT` empty), `watch-2-error`, `watch-3-replaced`, `watch-3-signal` (SIGHUP), `watch-4-harness-gone` (the helper again), `watch-never-0-or-1` (after a wake line the process is still running after `watchMinGap` plus 1 s and must be ended by SIGTERM → 3). Expected: every subtest `--- PASS`, 13 under `TestSection24Sequence` (0 to 12) and 11 under `TestExitCodeTable`.

- [ ] **Step 3: The exit script**

`scripts/m6-exit-checks.sh` (`#!/usr/bin/env bash`, `set -euo pipefail`): usage `m6-exit-checks.sh <e2e.log>` or `--deps-only`. The log form refuses a missing or empty log (exit 2 naming the path), prints `sequence=<n>` counting `^\s+--- PASS: TestSection24Sequence/` leaf lines, `exitcodes=<n>` counting `^\s+--- PASS: TestExitCodeTable/` and `fail=<n>` for `^\s+--- FAIL`, exiting 1 unless `fail=0`, `sequence=13` and `exitcodes=11`; then the word check: `rg -n 'dm/|-filter|@<|memory/|tasks/|general/' internal/cli/skills/using-agentbus2/SKILL.md internal/mcpserver/inputs.go internal/mcpserver/server.go internal/cli/stophook.go internal/cli/subagenthook.go` must match nothing (`words_ok=1`); then the import check. `--deps-only`: `go list -deps` (no `-test`) for `./internal/profile ./internal/repoconfig ./internal/credential ./internal/procs ./internal/sessionstate` (forbidden: `internal/core`, `internal/store`, `internal/server`, `internal/bus/local`), `./internal/mcpserver ./internal/cli` (forbidden: `agentbus2/internal/gen` exactly, `internal/store/postgres`), matching in-module packages with `grep -F -x`; `deps_ok=1` or `deps_violation=<package> imports <forbidden>` and exit 1.

- [ ] **Step 4: The milestone exit, positive**

Run (600000 ms or in the background): `env -u AGENTBUS2_TEST_POSTGRES_DSN make verify 2>&1 | tee /tmp/agentbus2-m6-verify.log | tail -3`; `env -u AGENTBUS2_TEST_POSTGRES_DSN make test-race 2>&1 | tail -4`; `go test -count=1 -v ./internal/e2e/ > /tmp/agentbus2-m6-e2e.log 2>&1; bash scripts/m6-exit-checks.sh /tmp/agentbus2-m6-e2e.log`; `bash scripts/m5-exit-checks.sh --deps-only`; `bash scripts/m4-exit-checks.sh --deps-only`.
Expected, quoted verbatim in the report: `verify: OK`; `test-race` `ok` everywhere; `fail=0`, `sequence=13`, `exitcodes=11`, `words_ok=1`, `deps_ok=1`, exit 0; `deps_ok=1` from both earlier scripts.

- [ ] **Step 5a: Prove the exit script can fail** (Definition of Done C)

In a throwaway worktree of `HEAD`: (1) `internal/profile/probe_m6.go` importing `internal/core` → `--deps-only` exit 1 naming it; remove. (2) `internal/mcpserver/probe_m6.go` importing `internal/gen` → exit 1; remove. (3) the script on `/tmp/does-not-exist.log` and on an empty file → exit 2. (4) a copy of the e2e log with one `--- PASS: TestExitCodeTable/wait-3-replaced` line deleted → `exitcodes=10`, exit 1. (5) the word `dm/` appended to the skill → `words_ok` absent and exit 1; revert. Quote the five outputs.

- [ ] **Step 5b: The milestone exit, negative half** (block 16 of `scripts/check-gates-selftest.sh`, Docker-free)

Block 16 `replaced-exit-code-ignored`: in the worktree replace, in `internal/cli/exit.go`, the `return 3` on the line after `case errors.Is(err, errReplaced), bus.CodeOf(err) == bus.CodeReplaced:` with `return 0` through a temp file and `cp` (`problem` unless `grep -c 'return 0' internal/cli/exit.go` rose by exactly 1); run `go test -count=1 -v -run '^TestExitCodeTable$/(wait|watch)-3-replaced$' ./internal/e2e/` and expect `--- FAIL: TestExitCodeTable/wait-3-replaced` and `--- FAIL: TestExitCodeTable/watch-3-replaced`; revert. A second mutation in the same block swaps `notify.NewPolling` for `notify.NewMemory()` in `internal/client/local.go` (`problem` unless `grep -c 'notify.NewMemory()' internal/client/local.go` is 1 after the edit) and runs `go test -count=1 -run '^TestLocalWatchWakesAcrossConns$' ./internal/client/`, expecting `--- FAIL`; revert. Run after Step 7's commit: `make check-gates-selftest 2>&1 | tail -24`. Expected: `selftest: ok replaced-exit-code-ignored`, every earlier block `ok`.

- [ ] **Step 6: Docs**

- `CLAUDE.md` Commands: the nineteen commands in one line each with their flags of choices 12 to 14 and 9, `AGENTBUS2_PROFILE`, `AGENTBUS2_IDENTITY`, `AGENTBUS2_CONFIG_DIR`, `AGENTBUS2_DATA_DIR`, `scripts/m6-exit-checks.sh`. Design: one line each for `internal/profile` (the file shape and the selection order), `internal/credential` (files, the process protocol, the cache), `internal/client` (local is the server in process; the maintenance loop runs in `agentbus2 mcp`), `internal/sessionstate` (the state file and what reads it), `internal/mcpserver` (hand-written schemas, never `internal/gen`), the hook budget. Facts only, checked against the tree.
- `README.md`: `Status: Milestone 6 (client side) done; agentbus2 init, register, mcp, watch and the CLI work against a local profile and a server; the TUI is Milestone 8.` plus a "Quick start" with `agentbus2 init --global`, `agentbus2 init` in a repository, and the server profile JSON of choice 2 with `agentbus2 register -profile lab -server URL -identity NAME -enrollment-token TOKEN`.
- ADR 0002 addendum `## Milestone 6 addendum (date)`: choices 1 to 7 and 14 (the file shape, selection, identity, credential files and the Windows ACL, the process protocol, the token cache and the audience rule), pending items 5, 6 and 7 with their markers. ADR 0003 addendum: choices 8 to 10 and 15 (the state file's fields including the resume token, the hook peek, the resume rule, the subagent text), pending items 1, 2 and 4. ADR 0001 addendum: choice 9 (the `watch` and `wait` contract and exit codes over the long-poll; `-channel` and `-filter` gone), choice 11 (`wait_seconds` replaces an armed watch, pending item 3), the local-mode composition of Decision 35 as shipped.
- `HANDOFF.md` via `/session:handoff`: Milestone 6 done at the Step 7 commit, unpushed; the Step 4, 5a and 5b outputs; the pending items; Milestone 7's state; Milestone 8 next.

- [ ] **Step 7: Commit**

```bash
git add main.go main_test.go internal/cli/cli.go internal/e2e/e2e_test.go internal/e2e/helper_test.go scripts/m6-exit-checks.sh scripts/check-gates-selftest.sh .github/workflows/ci.yml CLAUDE.md README.md docs/adr/0001-transport-and-api.md docs/adr/0002-authentication-and-credentials.md docs/adr/0003-identity-sessions-and-delivery.md
git commit -m "feat(cli): Milestone 6 exit: the section 2.4 sequence driven by the real binary against an enrollment-mode server, the spec 3.5 exit-code table, the replaced-exit and polling mutation proofs; dispatch and docs

Ports the dispatch of v1 main.go and the built-binary test pattern of v1
internal/mcpserver/integration_test.go."
```

- [ ] **Step 8: Report**

End with the Step 4 outputs, the Step 5a and 5b outputs quoted verbatim, and the pending items still open. No push: the next push is Eric's (`Approve: push main to origin (git push origin main)`).

---

## Self-review notes

- Spec coverage, section 10 Milestone 6: profiles in `config.json` (Task 1, Decision 74), identity selection (Tasks 1 and 5, Decision 71), credentials (Task 3, Decisions 22, 55), `credential_process` (Task 3, Decisions 23, 56), the remote `Bus` wired with token exchange and refresh (Tasks 3 and 5, Decisions 19, 70, 84; the `Bus` itself is Milestone 4's), the CLI (Tasks 6, 9, 10, 11; spec 2.2), the stdio MCP server with the header mapping and the state file (Task 7, Decisions 3, 47, 66, 78, 83), hooks (Task 8, Decision 41), `watch` and `wait` (Task 8, Decision 42, issue #38), resume within the retention window (Tasks 7 and 11, Decision 79), the `using-agentbus2` skill and its installation (Tasks 4 and 6, spec 9), local mode as the in-process server over the shared SQLite file (Task 5, Decisions 35, 36, 68). Section 2.4: Task 11 step by step over the real binary. Section 3.5's table: `TestExitCodeTable`, one subtest per cell, plus the `never 0 or 1` row.
- Decisions traced: 3, 10, 35, 42, 45, 46, 47, 66, 68, 83 (ADR 0001); 19, 20, 21, 22, 23, 33, 36, 55, 56, 70, 71, 74, 84 (ADR 0002); 12, 14, 15, 16, 17, 37, 38, 39, 40, 41, 44, 75, 78, 79 (ADR 0003); 31, 32 (ADR 0005); 13 (ADR 0006); 80 (ADR 0008, the ACL tools and flags); 76, 82, 90 (channel kinds and no prefixes in the skill and tools).
- Names used across tasks: `profile.Mode`, `Profile`, `Config`, `Load`, `Save`, `Resolve`, `DefaultProfile`, `SelectName`, `Selection`, `ResolveIdentity`, `IdentitySelection`, `ConfigDir`, `DataDir`, `Getenv`, `SQLitePath`, `CredentialDir`; `repoconfig.Find`, `Write`, `File`; `procs.FindHarness`, `WatchHarness`, `ErrHarnessGone`, `HarnessPollInterval`, `Alive`, `Ref`, `Table`, `System`; `sessionstate.File`, `Write`, `Read`, `Remove`, `List`, `ForHarness`, `Sweep`, `Dir`; `credential.Store`, `RunProcess`, `ProcessInput`, `ProcessOutput`, `Discover`, `Discovery`, `Cache`, `Entry`, `Source`, `Transport`, `Skew`, `ProcessTimeout`; `client.Options`, `Flags`, `Resolve`, `Resolved`, `Conn`, `Open`; `cli.newFlagSet`, `parseExit`, `fail`, `printJSON`, `open`, `exitCode`, `locateSession`, `watchLine`, `watchMinGap`, `hookBudget`, `SkillMD`, `Init`, `InitOptions`, `InitPrompt`, every `Run<Name>`, `Main`, `usage`; `mcpserver.Options`, `NewServer`, `Run`, `OpenLog`; `scripts/m6-exit-checks.sh`; self-test block 16.
- Type consistency: the command signature `(ctx, args, environ, stdin, stdout, stderr) int` is the same for every `Run<Name>` and `Main`; `client.Options.Profile`/`Identity` are the flag values and `Resolved` carries their sources; the state file's `HarnessPID`/`HarnessStart` are a `procs.Ref`; `credential.Source.Client` is the unauthenticated generated client and `Conn.Client` the authenticated one; `ProcessOutput.ExpiresAt` is RFC 3339 text parsed into `Entry.ExpiresAt`; the MCP `session` member is `bus.ReceiveInput.Session`, `bus.SendInput.Session` and `bus.WatchInput.Session`.
- Review Focus: 1 in Tasks 3 and 11, 2 in Task 1, 3 in Task 7, 4 in Tasks 2 and 7, 5 in Task 8.
- Parallelism: Wave 1 holds four tasks with disjoint trees; Wave 3 holds five (`internal/cli/init*.go`, `install*.go`, `register*.go`, `login*.go`, `skill.go` / `internal/mcpserver` / `internal/cli/exit.go`, `session_locate.go`, `watch*.go`, `wait*.go`, `stophook*.go`, `subagenthook*.go`, `faketable_test.go` / `internal/cli/session*.go`, `channels*.go`, `send.go`, `receive.go`, `history.go`, `search.go`, `messages_test.go` / `internal/cli/memories*.go`, `tasks*.go`, `status*.go`); `go.mod` is a chain (Task 2 then Task 7); `main.go`, `cli.go`, the scripts, CI and docs are Task 11's alone.
- Proportion: the plan fixes the file shapes, the protocol, the selection rules, the command grammars, the tool surface, the exit-code contract, the hook texts and the exit mechanics; bodies are the implementer's.
- Checked against the tree on 2026-10-21: `internal/bus/*.go` (the interfaces and types every command maps to), `internal/core/sessions/open.go` (resume of a live row and `Pending`), `internal/core/notify` (`NewPolling`, `NewMemory`), `internal/core/config.go` (`Config` fields), `internal/store/sqlite/sqlite.go` (`Open(ctx, path, Options)`), `main.go` (`run` with `help` and `version` only; Milestone 4's `server` case is assumed), `Makefile`, `scripts/`, `go.mod`; v1 `main.go`, `internal/cli/{wait,watch,stophook,subagenthook,harness,init,subscribe,status,delete,identity}.go`, `internal/mcpserver/{server,logfile}.go`, `internal/procs/*.go`, `internal/repoconfig/repoconfig.go`, `internal/config/config.go`, the v1 skill, v1 ADR 0021 and issue #38. Milestones 4 and 5 are not on `main`, so every name of theirs above is assumed from their plans and diffed at the external gate.
