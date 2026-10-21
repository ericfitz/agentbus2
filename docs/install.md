# Installing agentbus2

agentbus2 installs next to agentbus 1.x and shares nothing with it: its own binary (`agentbus2`), configuration, data directory, MCP server name and environment prefix (`AGENTBUS2_`). No installer here reads, replaces or removes an `agentbus` binary, a v1 directory or a v1 `PATH` entry (ADR 0005, Decision 32). There is no migration from v1.

Every release carries a macOS universal binary, Linux (amd64, arm64) and Windows (amd64, arm64) archives, `install.sh`, `install.ps1`, `SHA256SUMS` and `SHA256SUMS.sig`. `docs/release.md` says how they are made.

## macOS: Homebrew

```sh
brew install ericfitz/tap/agentbus2
```

The formula installs the signed, notarized universal binary from the matching [GitHub release](https://github.com/ericfitz/agentbus2/releases).

## Linux: install script

```sh
curl -fsSL https://github.com/ericfitz/agentbus2/releases/latest/download/install.sh | sh
```

Installs `agentbus2` into `~/.local/bin` without root, for x86_64 and aarch64. The script downloads the release tarball with `SHA256SUMS` and `SHA256SUMS.sig`, checks the Ed25519 signature with the public key built into the script (OpenSSL 3 is required: `openssl` or `openssl3` on `PATH`), checks the tarball's checksum, then replaces the binary atomically. Rerun it to upgrade; restart harness sessions and the TUI afterwards. It runs on Linux only: on macOS it refuses and names `brew install ericfitz/tap/agentbus2`.

| Variable | Meaning |
|---|---|
| `AGENTBUS2_VERSION` | Install this tag (`vX.Y.Z`) instead of the latest release. |
| `AGENTBUS2_INSTALL_DIR` | Absolute target directory (default `$HOME/.local/bin`). For a system-wide install: `curl -fsSL <url> \| sudo env AGENTBUS2_INSTALL_DIR=/usr/local/bin sh`. The script never runs `sudo` itself. |
| `AGENTBUS2_BASE_URL` | A mirror of `https://github.com/ericfitz/agentbus2`. |
| `AGENTBUS2_SKIP_SIGNATURE` | `1` skips only the signature check (for hosts with OpenSSL 1.1); the checksum is still verified. Any other value is rejected. |

The script stops with a message instead of guessing when it is given positional arguments, an `AGENTBUS2_VERSION` that is not exactly `vX.Y.Z`, a relative `AGENTBUS2_INSTALL_DIR`, no `HOME` and no `AGENTBUS2_INSTALL_DIR`, a copy without an embedded release key, or when `<dir>/agentbus2` is a directory. It is also safe against a truncated download: a partial script does nothing.

Verifying a download by hand, with the public key from [`release/agentbus2-release-ed25519.pub`](../release/agentbus2-release-ed25519.pub) (committed at the 2.0.0 release; see `docs/release.md`):

```sh
openssl pkeyutl -verify -rawin -pubin -inkey agentbus2-release-ed25519.pub -in SHA256SUMS -sigfile SHA256SUMS.sig
grep agentbus2-vX.Y.Z-linux-amd64.tar.gz SHA256SUMS | sha256sum -c
```

The Linux and Windows archives are built in GitHub Actions on runners of their own OS and architecture and carry a build provenance attestation. With the GitHub CLI you can check that an archive was built from this repository by that workflow, in addition to the signature check:

```sh
gh attestation verify agentbus2-vX.Y.Z-linux-amd64.tar.gz --repo ericfitz/agentbus2 \
  --signer-workflow ericfitz/agentbus2/.github/workflows/release-build.yml
```

Homebrew on Linux works too: `brew install ericfitz/tap/agentbus2` installs the static amd64 or arm64 binary. Inside WSL use the Linux installer.

## Windows: install script, Scoop, winget

```powershell
irm https://github.com/ericfitz/agentbus2/releases/latest/download/install.ps1 | iex
```

Installs `agentbus2.exe` into `%LocalAppData%\Programs\agentbus2` (amd64 or arm64) and adds that directory to your user `PATH`; open a new terminal afterwards. Windows PowerShell 5.1 and PowerShell 7 both work. The script verifies the release's Ed25519-signed `SHA256SUMS` with OpenSSL 3 and then the zip's hash. It looks for OpenSSL 3 on `PATH` first, then in Git for Windows (`usr\bin\openssl.exe`, `mingw64\bin\openssl.exe`, then, on ARM64, `clangarm64\bin\openssl.exe`); `winget install --id Git.Git -e` provides one, and without OpenSSL 3 the script refuses. Rerunning upgrades: a running `agentbus2.exe` is renamed (`agentbus2.exe.old-*`), not overwritten, so restart harness sessions and the TUI afterwards. The script never runs `agentbus2 init`; it prints the next steps.

The release zips also carry a `README.txt`, but unzipping one does not install agentbus2: the harnesses start `agentbus2.exe` by name, so it must be on your `PATH`.

| Variable | Meaning |
|---|---|
| `AGENTBUS2_VERSION` | Install this tag (`vX.Y.Z`) instead of the latest release. |
| `AGENTBUS2_INSTALL_DIR` | Absolute target directory (default `%LocalAppData%\Programs\agentbus2`). |
| `AGENTBUS2_BASE_URL` | A mirror of `https://github.com/ericfitz/agentbus2`. |
| `AGENTBUS2_SKIP_SIGNATURE` | `1` skips only the signature check; the hash is still verified. Unset is the default; any other value is rejected. |

As on Linux, the script stops with a message instead of guessing when it is given positional arguments, an `AGENTBUS2_VERSION` that is not exactly `vX.Y.Z`, a relative `AGENTBUS2_INSTALL_DIR`, no `LocalAppData` and no `AGENTBUS2_INSTALL_DIR`, a copy without an embedded release key, or when `<dir>\agentbus2.exe` is a directory. It also stops when the target directory is not writable, and it runs the downloaded `agentbus2.exe version` before replacing anything, so a binary that does not run never replaces a working install. A refusal throws, so a piped `iex` leaves your session open.

Package managers:

```powershell
scoop bucket add ericfitz https://github.com/ericfitz/scoop-bucket
scoop install agentbus2
winget install ericfitz.agentbus2
```

The winget command works once the winget package is published (the first submission follows the first release; `docs/release.md`).

Stop agentbus2 sessions (MCP servers, `agentbus2 wait`, `agentbus2 watch`, the TUI) before `scoop update agentbus2` or `winget upgrade ericfitz.agentbus2`: neither can replace the files of a running program.

To tell whether a Windows binary is signed, run `Get-AuthenticodeSignature .\agentbus2.exe`: a signed binary reports `Status` `Valid` and the expected signer (the maintainer, through Microsoft Artifact Signing). A release whose notes say its Windows binaries are not Authenticode-signed is unsigned; it is still covered by the signed `SHA256SUMS` and the build attestations. See [release-signing.md](release-signing.md).

## Where agentbus2 keeps things

| | macOS and Linux | Windows |
|---|---|---|
| Configuration (`config.json`) | `~/.config/agentbus2` (`$XDG_CONFIG_HOME/agentbus2` when set) | `%AppData%\agentbus2` |
| Data (the local SQLite database) | `~/.local/share/agentbus2` (`$XDG_DATA_HOME/agentbus2` when set) | `%LocalAppData%\agentbus2` (Local, so the database does not roam with a domain profile) |

`AGENTBUS2_CONFIG_DIR` and `AGENTBUS2_DATA_DIR` override both (`internal/profile/paths.go`, `internal/profile/paths_windows.go`). Nothing here touches the directories or binary of agentbus 1.x.

## After installing

```sh
agentbus2 init --global   # once per machine: MCP server, hooks and skill for each harness
agentbus2 init            # inside each repository
```

The README's Quick start covers profiles, servers and registration; `docs/ops/` covers running a server.

## Or, build from source

Requirements: Go 1.27.

```sh
git clone git@github.com:ericfitz/agentbus2.git
cd agentbus2
make build
```

`make build` writes `./agentbus2` (no cgo). Put it on your `PATH`.
