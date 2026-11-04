# Releasing agentmesh

The runbook for cutting a release. The tooling is a port of agentbus 1.x's, under the agentmesh names (spec 6.1, Decisions 32, 33, 61 and 87), and everything the automated checks can prove is under `make verify`. The steps that need the maintainer's keys, accounts and Mac are marked below; they are Milestone 9 Tasks 9 and 10.

## What a release is

A release of `vX.Y.Z` holds fourteen assets: two binaries, `agentmesh` (the full client, server and TUI) and `agentmesh-mcponly` (the small MCP-only client), each with five archives, plus the installers and the signed sums.

| Asset | Built by |
|---|---|
| `agentmesh-vX.Y.Z-macos-universal.tar.gz`, `agentmesh-mcponly-vX.Y.Z-macos-universal.tar.gz` (hold `agentmesh` and `agentmesh-mcponly`) | `release/release.sh` on the maintainer's Mac: per binary, arm64 and amd64 builds from the tag, joined with `lipo`, codesigned, notarized (one `notarytool` submission each; logs `dist/notary-agentmesh.log` and `dist/notary-agentmesh-mcponly.log`) |
| `<binary>-vX.Y.Z-linux-amd64.tar.gz`, `<binary>-vX.Y.Z-linux-arm64.tar.gz` (hold `<binary>`) | `.github/workflows/release-build.yml` on native runners, attested |
| `<binary>-vX.Y.Z-windows-amd64.zip`, `<binary>-vX.Y.Z-windows-arm64.zip` (hold `<binary>.exe` and `README.txt`) | the same workflow's `package-windows` job, Authenticode-signed (both executables) when the release is made with `--windows-signing=on`, attested |
| `install.sh`, `install.ps1` | copied from the tag |
| `SHA256SUMS`, `SHA256SUMS.sig` | `release.sh`: the sums cover the ten archives and the two installers, twelve lines in this order: the two macOS archives (`agentmesh` first), then `agentmesh`'s linux-amd64, linux-arm64, windows-amd64 and windows-arm64, then the same four of `agentmesh-mcponly`, then `install.sh` and `install.ps1`; the signature is Ed25519 |

`<binary>` is `agentmesh` or `agentmesh-mcponly`. Every archive is built by one command, `release/build-archive.sh <binary> <tag> <goos> <goarch> <outdir>` (`CGO_ENABLED=0 -trimpath`, version set through `-X github.com/ericfitz/agentmesh/internal/version.Version`, then the build info must name the package asked for, `github.com/ericfitz/agentmesh` or `github.com/ericfitz/agentmesh/cmd/agentmesh-mcponly`, and a host-native binary is run and must print the tag's version). The workflow, `release.sh`'s macOS build and the local dry run all call it.

After publishing, `release.sh` renders and pushes, for each binary, the Homebrew formula (`Formula/agentmesh.rb` and `Formula/agentmesh-mcponly.rb`, tap `ericfitz/homebrew-tap`) and the Scoop manifest (`bucket/agentmesh.json` and `bucket/agentmesh-mcponly.json`, bucket `ericfitz/scoop-bucket`; one commit per file, `<binary> <version>`), and submits the winget packages `ericfitz.agentmesh` and `ericfitz.agentmesh-mcponly` with `komac`. The tap and the bucket already exist for v1; each gains two files. `TAP_DIR` and `SCOOP_DIR` name the local clones (defaults `~/Projects/homebrew-tap` and `~/Projects/scoop-bucket`).

The Docker image is separate: `.github/workflows/publish-image.yml` builds and pushes it to `ghcr.io` when a release is published, and does nothing unless the repository variable `IMAGE_REPOSITORY` is set.

## One-time setup (needs the maintainer)

1. **Release key pair.** A new pair for agentmesh, not v1's (Milestone 9 decisions 1 and 6 in `docs/superpowers/specs/2026-10-21-gate-0-decisions.md`). No agent reads the `.pem`; only `release.sh` passes its path to `openssl`. Override the path with `AGENTMESH_SIGNING_KEY`. CI never holds the key.

   ```sh
   umask 077
   "$(brew --prefix openssl@3)/bin/openssl" genpkey -algorithm ed25519 -out ~/.keys/agentmesh-release-ed25519.pem
   "$(brew --prefix openssl@3)/bin/openssl" pkey -in ~/.keys/agentmesh-release-ed25519.pem -pubout -out release/agentmesh-release-ed25519.pub
   release/embed-key.sh release/agentmesh-release-ed25519.pub install.sh install.ps1
   ```

   Then check and commit the public half:

   ```sh
   bash release/test-install.sh --self-test && pwsh -NoProfile -File release/test-install.ps1 -SelfTest && bash scripts/m9-exit-checks.sh --real-only
   git add release/agentmesh-release-ed25519.pub install.sh install.ps1
   git commit -m "chore(release): the agentmesh release public key, embedded in install.sh and install.ps1"
   ```

   Until this is done the installers hold a placeholder key and `release.sh` refuses to run (`install.sh has no embedded release key`).

   Then copy `~/.keys/agentmesh-release-ed25519.pem` into your password manager; that copy is the only backup. The key's fingerprint, quoted in release notes and advisories, is `"$(brew --prefix openssl@3)/bin/openssl" pkey -pubin -in release/agentmesh-release-ed25519.pub -outform DER | shasum -a 256`.

   - **Rotation** (planned, or the key and its backup both lost): generate a new pair with the commands above, commit the new public key and installers, and give the new fingerprint in the next release's notes. There is no cross-signing; earlier releases stay verifiable with the key in their own `install.sh`.
   - **Compromise**: rotate as above, re-sign the `SHA256SUMS` of every release still supported with the new key (replacing each `SHA256SUMS.sig` asset), and publish a GitHub security advisory naming the old key's fingerprint.
2. **GitHub, in `ericfitz/agentmesh`.** Create the `release` environment with the deployment tag rule `v*`. Push `main`, then confirm `gh workflow view release-build.yml --repo ericfitz/agentmesh` sees the workflow (`release.sh` requires it on the default branch). For winget, a fine-grained personal access token with `public_repo` in `~/.keys/KOMAC_GITHUB_KEY`, as one line `export KOMAC_GITHUB_KEY='<value>'` (v1's file serves if it exists).
3. **Azure** (only for `--windows-signing=on`): follow `docs/release-signing.md`: a federated credential with the subject `repo:ericfitz@2677124/agentmesh@1426457945:environment:release`, the Signer role on the certificate profile, and the seven repository variables (`AZURE_CLIENT_ID`, `AZURE_TENANT_ID`, `AZURE_SUBSCRIPTION_ID`, `ARTIFACT_SIGNING_ENDPOINT`, `ARTIFACT_SIGNING_ACCOUNT`, `ARTIFACT_SIGNING_PROFILE`, `AUTHENTICODE_SIGNER`). `gh variable list --repo ericfitz/agentmesh` must list all seven. Skip this for a release made with `--windows-signing=off`.
4. **Apple.** `xcrun notarytool history --keychain-profile sqdist-notary` answers (the profile name is `NOTARY_PROFILE` in `release.sh`; the profile is team-level and shared with v1), and `security find-identity -v -p codesigning` lists the Developer ID Application identity named by `SIGN_IDENTITY` in `release.sh`.
5. **Image publishing** (optional). To ship the image with a release: `gh variable set IMAGE_REPOSITORY --repo ericfitz/agentmesh --body ghcr.io/ericfitz/agentbus`, and confirm the repository's Actions settings let `GITHUB_TOKEN` hold `packages: write` (the job asks for it). The workflow is `ghcr.io` only (it logs in with `GITHUB_TOKEN`; another registry needs its own credential and a change to the workflow). To ship it later, leave the variable unset: the job is skipped on every release, and a later `workflow_dispatch` of `publish-image.yml` with the tag publishes the image retroactively. The package's visibility is set once, after its first push.
6. **First winget submission, once per package.** `komac update` cannot add a package that is not in winget-pkgs yet, and a new package goes through winget-pkgs moderator review once (Eric, 2026-10-27), so the first submission of each package stays manual and `release.sh` never creates a package. `release.sh` renders one directory per package, so after the first published release submit each from the files the run rendered: `komac submit dist/winget/ericfitz.agentmesh --dry-run`, review, then `komac submit dist/winget/ericfitz.agentmesh --yes`, and the same for `dist/winget/ericfitz.agentmesh-mcponly`. Later releases go through `release.sh` (`komac update` per id).

## Releasing

1. **Pre-release checks on `main`:**

   ```sh
   make verify && make release-lint && make release-install-check && bash scripts/m9-exit-checks.sh
   ```

   and in the Windows VM `powershell -ExecutionPolicy Bypass -File release\test-windows.ps1` (Go and Git for Windows on `PATH`; `SKIP (pwsh not installed)` is allowed for the PowerShell 7 row). Optionally `AGENTMESH_BREW_STYLE=1 bash release/test-render.sh` also runs `brew style` over the rendered formula (needs the network; unset skips it, any other value fails).
2. **Notes.** Write `release/notes-vX.Y.Z.md`. `release.sh` uses it as the release body when present (else GitHub's generated notes) and, for `--windows-signing=off`, appends the unsigned-Windows line itself.
3. **Tag and dry run.** There are no release candidates and only `vX.Y.Z` tags are accepted; the dry run uses the real next tag with `--no-publish`:

   ```sh
   git tag vX.Y.Z && git push origin vX.Y.Z
   release/release.sh vX.Y.Z --windows-signing=on|off --no-publish
   ```

   `release.sh` checks its tools, the signing key against the committed public key, that the tag exists locally and on `origin` and names the same object in both, that the workflow and `release/build-archive.sh` are in the tag, that the installers have an embedded key, and (for `on`) the seven variables. It builds from the tag in a throwaway `git worktree`, whatever `HEAD` is. `--windows-signing=on|off` is required: signing is billed monthly, so it is chosen per release.

   Inspect: the draft at `https://github.com/ericfitz/agentmesh/releases/tag/vX.Y.Z` holds the fourteen assets; `openssl pkeyutl -verify -rawin -pubin -inkey release/agentmesh-release-ed25519.pub -in dist/SHA256SUMS -sigfile dist/SHA256SUMS.sig` passes; the ten `rendered:` lines name, for each binary, `dist/<binary>.rb`, `dist/<binary>.scoop.json` and the three files under `dist/winget/ericfitz.<binary>/` (`ericfitz.<binary>.yaml`, `ericfitz.<binary>.installer.yaml`, `ericfitz.<binary>.locale.en-US.yaml`). Check them, for each binary: `ruby -c dist/<binary>.rb` and its three `sha256` values against `shasum -a 256` of the Unix archives in `dist/`; the Scoop manifest's `version`, `bin` (`<binary>.exe`) and two hashes against `dist/SHA256SUMS`; the winget files' `PackageVersion`, `InstallerType: zip`, `NestedInstallerType: portable`, `RelativeFilePath: <binary>.exe` with `PortableCommandAlias: <binary>`, and the two `InstallerSha256` values against the windows lines of `dist/SHA256SUMS`; optionally `winget validate --manifest dist\winget\ericfitz.<binary>` in the Windows VM. A draft's assets are not downloadable anonymously, so the installers are checked after publishing.

   **If the dry run fails after the tag is public:** while the release is still a draft, fix on `main`, delete the draft (`gh release delete vX.Y.Z --repo ericfitz/agentmesh --yes`), move the tag (`git tag -f vX.Y.Z && git push -f origin vX.Y.Z`) and rerun. Once the release is published, never move the tag: cut the next patch version.
4. **Publish.** `release/release.sh vX.Y.Z --windows-signing=on|off` (the same flag as the dry run). It prints `Reusing draft release vX.Y.Z`, publishes, pushes the formulas to the tap and the manifests to the bucket, and submits both packages to winget with `komac update`. A `komac` failure does not fail the release: it prints the exact rerun command (expected for the first release only, see setup step 6).
5. **After publishing:**
   - `release/check-linux-brew.sh [amd64|arm64]` installs both published formulas (`agentmesh` and `agentmesh-mcponly`) with Homebrew on Linux in Docker (both architectures by default), runs each test block and prints each `version`.
   - `docker run --rm debian:12 sh -c 'apt-get update -qq && apt-get install -y -qq curl ca-certificates openssl >/dev/null && curl -fsSL https://github.com/ericfitz/agentmesh/releases/latest/download/install.sh | sh && ~/.local/bin/agentmesh version'`.
   - On the Mac: `brew install ericfitz/tap/agentmesh && brew test agentmesh && agentmesh version`, and the same with `agentmesh-mcponly`.
   - In the Windows VM: `irm https://github.com/ericfitz/agentmesh/releases/latest/download/install.ps1 | iex`, then `agentmesh version` in a new terminal; `scoop bucket add ericfitz https://github.com/ericfitz/scoop-bucket && scoop install agentmesh agentmesh-mcponly`; on a `--windows-signing=on` release, `Get-AuthenticodeSignature .\agentmesh.exe` and `.\agentmesh-mcponly.exe` report `Valid` for both architectures, and SmartScreen may warn until the signing identity has reputation. `winget install ericfitz.agentmesh` and `winget install ericfitz.agentmesh-mcponly` work once the winget-pkgs pull requests have merged.
   - When `publish-image.yml` ran: `gh run list --repo ericfitz/agentmesh --workflow publish-image.yml` shows a successful run, and `docker run --rm --entrypoint /agentmesh <IMAGE_REPOSITORY>:X.Y.Z version` prints the version.

## State: second runs, overlapping runs, leftovers

- **Rerun after a failure** (before publishing): `release.sh` reuses the draft, redispatches the workflow, which re-uploads with `--clobber`, and repeats the attestation checks, signing, upload and rendering. The notes line follows the current `--windows-signing` value.
- **Rerun on a published release:** allowed after a warning that it replaces public assets; the publish step is skipped, the tap and bucket are updated (a stranded commit from an earlier failed push is pushed by the rerun) and winget is not resubmitted while its pull request is open.
- **Overlapping dispatches** for one tag queue on the workflow's concurrency group (`release-<tag>`, `cancel-in-progress: false`). `release.sh` is run by one person at a time. It excludes earlier runs of the tag when it looks for its own.
- **Leftovers of a killed run:** a `git worktree` of the tag in a temp directory under `$TMPDIR` (it does not block a rerun, which adds its own; `git worktree prune`, which `checkout_tag` runs first, drops the registration once that directory is gone, or remove it with `git worktree remove -f <path>` from `git worktree list`), and a partial `dist/`, recreated by the next run. A leftover draft is what a rerun resumes.
- **A `komac` failure** leaves the release standing and prints the rerun command.

## What the automated checks cover

`make release-check` (its own CI job, `release-check`; not part of `make verify`) runs: `release/check-pins.sh` (every `uses:` pinned to a full commit SHA with a version comment), `release/test-install.sh --self-test`, `release/test-release.sh`, `release/test-render.sh`, `release/build-archive.sh --self-test`, `release/test-pipeline.sh` (the local unsigned dry run of the whole pipeline with `gh`, `codesign`, `xcrun`, `komac` and the signing key stubbed) and `make vet-cross`.

Other separate CI jobs and workflows, not part of `verify`: `release-lint` (`make release-lint`: `actionlint` and `shellcheck`), `release-install-check` (`make release-install-check`: the installer matrix in Docker, Ubuntu, Debian and Alpine on amd64 and arm64; `RELEASE_INSTALL_ARGS=--quick` runs one image), the `windows` workflow (`.github/workflows/windows.yml`: Go vet and unit tests, and the `install.ps1` harness under both PowerShell hosts; required: work is done only when it is green on the pushed commit) and `m9-exit-selftest` (`scripts/m9-exit-checks.sh`: the real-repo invariants, the dry run's own self-test and six mutations proving that the pin check, the template render, the installer key-block check, the dry run's version check, the wrong-binary check of `build-archive.sh` and the `BIN_NAMES` anchor of `release.sh` fire; `--real-only` skips the mutations). Locally, `make release-lint` needs `actionlint` and `shellcheck`.

What only a tagged run proves, and the automated checks do not: macOS codesigning and notarization, Authenticode signing, the build attestations, the signature with the real key, the real pushes to the tap and the bucket, the winget submission and the image push. `scripts/m9-exit-checks.sh` prints these as `pending`, never as `ok`.

## Before 2.0.0

Milestones 1 to 8 and the automated half of Milestone 9 are on `main`. Milestone 9 Task 9 (the one-time setup above) is done: the release key is embedded, the `release` environment and `IMAGE_REPOSITORY` are set, and the Azure account and federated credential exist (the certificate profile waits for the organization validation). 2.0.0 waits for the Apple Developer account's conversion to the company (Milestone 9 decision 7) and a new Developer ID Application certificate, then Task 10 (the pre-release checks, the tagged dry run with `--no-publish`, publishing, the first winget submission and the exit evidence). `release/notes-v2.0.0.md` is the text of the 2.0.0 release body; it states the winget command as available once published, so review it against what actually shipped before publishing.
