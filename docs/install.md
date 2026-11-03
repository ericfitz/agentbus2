# Installing agentmesh

agentmesh installs next to agentbus 1.x and shares nothing with it: its own binary (`agentmesh`), configuration, data directory, MCP server name and environment prefix (`AGENTMESH_`). No installer here reads, replaces or removes an `agentbus` binary, a v1 directory or a v1 `PATH` entry (ADR 0005, Decision 32). There is no migration from v1.

Every release carries a macOS universal binary, Linux (amd64, arm64) and Windows (amd64, arm64) archives, `install.sh`, `install.ps1`, `SHA256SUMS` and `SHA256SUMS.sig`. `docs/release.md` says how they are made.

## macOS: Homebrew

```sh
brew install ericfitz/tap/agentmesh
```

The formula installs the signed, notarized universal binary from the matching [GitHub release](https://github.com/ericfitz/agentmesh/releases).

## Linux: install script

```sh
curl -fsSL https://github.com/ericfitz/agentmesh/releases/latest/download/install.sh | sh
```

Installs `agentmesh` into `~/.local/bin` without root, for x86_64 and aarch64. The script downloads the release tarball with `SHA256SUMS` and `SHA256SUMS.sig`, checks the Ed25519 signature with the public key built into the script (OpenSSL 3 is required: `openssl` or `openssl3` on `PATH`), checks the tarball's checksum, then replaces the binary atomically. Rerun it to upgrade; restart harness sessions and the TUI afterwards. It runs on Linux only: on macOS it refuses and names `brew install ericfitz/tap/agentmesh`.

| Variable | Meaning |
|---|---|
| `AGENTMESH_VERSION` | Install this tag (`vX.Y.Z`) instead of the latest release. |
| `AGENTMESH_INSTALL_DIR` | Absolute target directory (default `$HOME/.local/bin`). For a system-wide install: `curl -fsSL <url> \| sudo env AGENTMESH_INSTALL_DIR=/usr/local/bin sh`. The script never runs `sudo` itself. |
| `AGENTMESH_BASE_URL` | A mirror of `https://github.com/ericfitz/agentmesh`. |
| `AGENTMESH_SKIP_SIGNATURE` | `1` skips only the signature check (for hosts with OpenSSL 1.1); the checksum is still verified. Any other value is rejected. |

The script stops with a message instead of guessing when it is given any argument other than `--mcp-only` (a repeated or extra one included; it prints `usage: install.sh [--mcp-only]`), an `AGENTMESH_VERSION` that is not exactly `vX.Y.Z`, a relative `AGENTMESH_INSTALL_DIR`, no `HOME` and no `AGENTMESH_INSTALL_DIR`, a copy without an embedded release key, or when `<dir>/agentmesh` (`<dir>/agentmesh-mcponly` with `--mcp-only`) is a directory. It is also safe against a truncated download: a partial script does nothing.

Verifying a download by hand, with the public key from [`release/agentmesh-release-ed25519.pub`](../release/agentmesh-release-ed25519.pub) (committed at the 2.0.0 release; see `docs/release.md`):

```sh
openssl pkeyutl -verify -rawin -pubin -inkey agentmesh-release-ed25519.pub -in SHA256SUMS -sigfile SHA256SUMS.sig
grep agentmesh-vX.Y.Z-linux-amd64.tar.gz SHA256SUMS | sha256sum -c
```

The archives hold no key-provider backend (no AWS, Google, Azure or Vault client is linked): they run agents, the CLI and the TUI, and a server that keeps its signing key in a key service is built from source with `-tags` or runs from the Docker image, which carries every backend (`ops/deploy.md`, `ops/signing-keys.md`).

The Linux and Windows archives are built in GitHub Actions on runners of their own OS and architecture and carry a build provenance attestation. With the GitHub CLI you can check that an archive was built from this repository by that workflow, in addition to the signature check:

```sh
gh attestation verify agentmesh-vX.Y.Z-linux-amd64.tar.gz --repo ericfitz/agentmesh \
  --signer-workflow ericfitz/agentmesh/.github/workflows/release-build.yml
```

Homebrew on Linux works too: `brew install ericfitz/tap/agentmesh` installs the static amd64 or arm64 binary. Inside WSL use the Linux installer.

## Windows: install script, Scoop, winget

```powershell
irm https://github.com/ericfitz/agentmesh/releases/latest/download/install.ps1 | iex
```

Installs `agentmesh.exe` into `%LocalAppData%\Programs\agentmesh` (amd64 or arm64) and adds that directory to your user `PATH`; open a new terminal afterwards. Windows PowerShell 5.1 and PowerShell 7 both work. The script verifies the release's Ed25519-signed `SHA256SUMS` with OpenSSL 3 and then the zip's hash. It looks for OpenSSL 3 on `PATH` first, then in Git for Windows (`usr\bin\openssl.exe`, `mingw64\bin\openssl.exe`, then, on ARM64, `clangarm64\bin\openssl.exe`); `winget install --id Git.Git -e` provides one, and without OpenSSL 3 the script refuses. Rerunning upgrades: a running `agentmesh.exe` is renamed (`agentmesh.exe.old-*`), not overwritten, so restart harness sessions and the TUI afterwards. The script never runs `agentmesh init`; it prints the next steps.

The release zips also carry a `README.txt`, but unzipping one does not install agentmesh: the harnesses start `agentmesh.exe` by name, so it must be on your `PATH`.

| Variable | Meaning |
|---|---|
| `AGENTMESH_VERSION` | Install this tag (`vX.Y.Z`) instead of the latest release. |
| `AGENTMESH_INSTALL_DIR` | Absolute target directory (default `%LocalAppData%\Programs\agentmesh`). |
| `AGENTMESH_BASE_URL` | A mirror of `https://github.com/ericfitz/agentmesh`. |
| `AGENTMESH_SKIP_SIGNATURE` | `1` skips only the signature check; the hash is still verified. Unset is the default; any other value is rejected. |

As on Linux, the script stops with a message instead of guessing when it is given any argument other than `-McpOnly` (a repeated or extra one included; it prints `usage: install.ps1 [-McpOnly]`), an `AGENTMESH_VERSION` that is not exactly `vX.Y.Z`, a relative `AGENTMESH_INSTALL_DIR`, no `LocalAppData` and no `AGENTMESH_INSTALL_DIR`, a copy without an embedded release key, or when `<dir>\agentmesh.exe` (`<dir>\agentmesh-mcponly.exe` with `-McpOnly`) is a directory. It also stops when the target directory is not writable, and it runs the downloaded `agentmesh.exe version` before replacing anything, so a binary that does not run never replaces a working install. A refusal throws, so a piped `iex` leaves your session open.

Package managers:

```powershell
scoop bucket add ericfitz https://github.com/ericfitz/scoop-bucket
scoop install agentmesh
winget install ericfitz.agentmesh
```

The winget command works once the winget package is published (the first submission follows the first release; `docs/release.md`).

Stop agentmesh sessions (MCP servers, `agentmesh wait`, `agentmesh watch`, the TUI) before `scoop update agentmesh` or `winget upgrade ericfitz.agentmesh`: neither can replace the files of a running program.

To tell whether a Windows binary is signed, run `Get-AuthenticodeSignature .\agentmesh.exe`: a signed binary reports `Status` `Valid` and the expected signer (the maintainer, through Microsoft Artifact Signing). A release whose notes say its Windows binaries are not Authenticode-signed is unsigned; it is still covered by the signed `SHA256SUMS` and the build attestations. See [release-signing.md](release-signing.md).

## The MCP-only client

`agentmesh-mcponly` is a smaller binary for a machine whose agents only connect to an agentmesh server: its commands are `serve` (the stdio MCP server), `register`, `check`, `config`, `help` and `version`, and it has no local mode. It installs beside `agentmesh` (same directory, same `PATH` entry) and replaces nothing of it; each installer touches only its own binary, and the Windows installer sweeps only its own `.old-*` leftovers.

| Platform | Command |
|---|---|
| macOS | `brew install ericfitz/tap/agentmesh-mcponly` |
| Linux | `curl -fsSL https://github.com/ericfitz/agentmesh/releases/latest/download/install.sh \| sh -s -- --mcp-only` |
| Windows | `& ([scriptblock]::Create((irm https://github.com/ericfitz/agentmesh/releases/latest/download/install.ps1))) -McpOnly` |
| Windows (Scoop) | `scoop install agentmesh-mcponly` |
| Windows (winget) | `winget install ericfitz.agentmesh-mcponly` |

The scripts take the same environment variables as above and do the same checks on `agentmesh-mcponly-<tag>-linux-<arch>.tar.gz` or `agentmesh-mcponly-<tag>-windows-<arch>.zip`. Rerunning upgrades; a second run at the same version prints `reinstalled: agentmesh-mcponly <version> was already installed`. The scripts print `next: agentmesh-mcponly help (connect this agent to an agentmesh server)`; run `agentmesh-mcponly help` after installing.

`agentmesh-mcponly config` prints the agent's MCP entry with the path of the binary as the operating system reports it, with no symlink resolution of its own: on macOS and Windows that is the path the binary was started through, and on Linux (`/proc/self/exe`) the resolved file, so a Linuxbrew install pins its versioned Cellar path. Rerun it and replace the entry after an upgrade or after moving the binary, as `agentmesh init --global` is rerun for the full binary; on macOS and Windows, start the binary through a package manager's stable link (Homebrew's `$(brew --prefix)/bin`) when you run `config` to keep the link in the entry. On Windows the `command line:` it prints is a PowerShell line; the JSON entry is the same everywhere.

## Where agentmesh keeps things

| | macOS and Linux | Windows |
|---|---|---|
| Configuration (`config.json`) | `~/.config/agentmesh` (`$XDG_CONFIG_HOME/agentmesh` when set) | `%AppData%\agentmesh` |
| Data (the local SQLite database) | `~/.local/share/agentmesh` (`$XDG_DATA_HOME/agentmesh` when set) | `%LocalAppData%\agentmesh` (Local, so the database does not roam with a domain profile) |

`AGENTMESH_CONFIG_DIR` and `AGENTMESH_DATA_DIR` override both (`internal/profile/paths.go`, `internal/profile/paths_windows.go`). Nothing here touches the directories or binary of agentbus 1.x.

## After installing

```sh
agentmesh init --global   # once per machine, and again after an upgrade: MCP server, hooks and skill for each harness
agentmesh init            # inside each repository (the profile's identity is the repository name, or <user>-home for the home directory)
```

The README's Quick start covers profiles, servers and registration; a human on a server profile adds an `oidc` setting and signs in through the browser (`agentmesh login -human`, `docs/tui.md`); `docs/ops/` covers running a server.

## Or, build from source

Requirements: Go 1.27.

```sh
git clone git@github.com:ericfitz/agentmesh.git
cd agentmesh
make build
```

`make build` writes `./agentmesh` (no cgo). Put it on your `PATH`.
