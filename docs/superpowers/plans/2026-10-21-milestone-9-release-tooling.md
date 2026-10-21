# Milestone 9: Release Tooling Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Everything that turns a `vX.Y.Z` tag of agentbus2 into a release: the installers `install.sh` and `install.ps1`, the Actions build workflow for the Linux and Windows archives with build attestations, `release/release.sh` (macOS universal binary signed and notarized, the Ed25519-signed `SHA256SUMS`, the draft release, the Homebrew formula `agentbus2`, the Scoop manifest, the three winget manifests and the winget submission), the gated-off image publishing workflow, their test harnesses, a local unsigned dry run of the whole pipeline with signing stubbed that runs under `make verify`, the install and release docs, and the two steps only Eric can run (one-time signing setup, the tagged dry run and 2.0.0).

**Architecture:** A deliberate port of v1's release tooling (`/Users/efitz/Projects/agentbus`: `release/`, `.github/workflows/release-build.yml`, `windows.yml`, `install.sh`, `install.ps1`) under the v2 names of spec 6.1 (Decisions 32, 33, 61, 87). The pipeline keeps v1's split: macOS is built, codesigned and notarized on Eric's Mac by `release.sh`; Linux and Windows are built on native runners by `release-build.yml` dispatched on the tag, attested, and uploaded to the draft; `release.sh` verifies the attestations, writes and signs `SHA256SUMS`, publishes, then updates the tap, the bucket and winget. New in v2: the build step is one script, `release/build-archive.sh`, used by the workflow and by the dry run, so the dry run exercises the exact build; `release.sh` is portable to Linux (CI runs the dry run on `ubuntu-latest`) and every tool it needs that only Eric has (`gh`, `codesign`, `xcrun notarytool`, `komac`, the signing key) is a seam the harness stubs.

**Tech Stack:** bash (`set -euo pipefail`), POSIX `sh` for `install.sh`, PowerShell 5.1 and 7 for `install.ps1`, OpenSSL 3 (`pkeyutl -rawin` Ed25519), `gh`, `lipo`/`codesign`/`notarytool`, GitHub Actions (`workflow_dispatch`, `actions/attest-build-provenance`, Microsoft Artifact Signing over OIDC), Homebrew formula Ruby, Scoop JSON, `komac`, Docker for the installer matrix, Go `CGO_ENABLED=0 -trimpath -ldflags "-s -w -X github.com/ericfitz/agentbus2/internal/version.Version=<v>"`.

**Spec:** `docs/superpowers/specs/2026-10-19-agentbus2-design.md` sections 1.1, 1.3, 6.1, 8, 9 (the "Release" row), 10 (Milestone 9); ADR 0005 (separate product and names); Decisions 31, 32, 33, 53, 61, 87. Gate 0 decisions (`docs/superpowers/specs/2026-10-21-gate-0-decisions.md`) name nothing for this milestone. v1 design intent: `agentbus/docs/superpowers/specs/2026-10-06-{linux-releases,windows-support,actions-release-builds,authenticode}-design.md`.

**State of `main` when this plan was written:** Milestone 4 is landed; Milestones 5 to 8 are in flight. The only subcommand any automated step here executes is `agentbus2 version` (`internal/version.Version`, default `2.0.0-dev`), which exists. Text that names post-install commands (`agentbus2 init --global`, `agentbus2 init`, `agentbus2 mcp`, `watch`, the TUI) in the formula caveats, the zip README, the Scoop `notes` and the docs uses the names Milestone 6 and 8 plan (Decision 33 and the Milestone 6 plan item 13); Task 8's Definition of Done E check runs against the tree at integration time. Every target cross-compiles and vets today: `GOOS={linux,windows} GOARCH={amd64,arm64} go build ./...` and `go vet ./...` passed at `9305855`.

## Parallelism summary

| Wave | Tasks | Trees |
| --- | --- | --- |
| 1 | 1, 2, 3, 4 (no dependencies; four worktrees) | 1: `install.sh`, `release/embed-key.sh`, `release/test-install.sh`, `release/testdata/{Dockerfile.*,fixture-server.py}`. 2: `install.ps1`, `release/test-install.ps1`, `release/test-windows.ps1`, `release/windows-zip-README.txt`, `release/testdata/fixture-server.ps1`, `release/testdata/stub/`, `.github/workflows/windows.yml`. 3: `release/release.sh`, `release/agentbus2.rb.tmpl`, `release/agentbus2.scoop.json.tmpl`, `release/agentbus2.winget.version.yaml.tmpl`, `release/agentbus2.winget.installer.yaml.tmpl`, `release/agentbus2.winget.locale.en-US.yaml.tmpl`, `release/check-pins.sh`, `release/check-linux-brew.sh`, `release/test-release.sh`, `release/test-render.sh`. 4: `release/build-archive.sh`, `.github/workflows/release-build.yml`, `.github/workflows/publish-image.yml`. |
| 2 | 5 (depends on 1, 2, 3, 4) | `release/test-pipeline.sh` |
| 3 | 6 (depends on 5) | `Makefile`, `.github/workflows/ci.yml`, `CLAUDE.md` (the `Done gate:` line only) |
| 4 | 7 (depends on 6) | `scripts/m9-exit-checks.sh`, `.github/workflows/ci.yml` (its own job, after Task 6's edit) |
| 5 | 8 (depends on 6, 7: it documents Task 7's script and job and its Step 2 asserts every documented path exists) | `docs/install.md`, `docs/release.md`, `docs/release-signing.md`, `release/notes-v2.0.0.md`, `README.md`, `CLAUDE.md` (Commands and Design lines) |
| Eric | 9 (depends on 8), then 10 (depends on 7, 9) | needs Eric; the automated run skips both |

`Makefile`, `.github/workflows/ci.yml`, `README.md` and `CLAUDE.md` are also edited by the Milestone 6 and 7 plans (`docker-image`, `docker-smoke`, the `docker-smoke` CI job, the M6 and M7 command lines); within this milestone `Makefile` belongs to Task 6, `README.md` to Task 8, `ci.yml` to Task 6 and then Task 7, and `CLAUDE.md` to Task 6 (the one `Done gate:` line, in the same commit that changes what `verify` runs, because Definition of Done A reads that line) and then Task 8 (the Commands and Design lines); every pair is sequential by the dependency graph, never parallel, and each is rebased against those milestones' commits at integration. `go.mod` is untouched (the Windows stub under `release/testdata/stub/` is its own module, as in v1, and is never built by `go build ./...`; confirm with `go list ./...` in Task 2).

## Global Constraints

- Spec 1.1: "Release tooling (signing and notarization, Actions release builds, Homebrew formula `agentbus2`, `install.sh` and `install.ps1`), ported in the final milestone before 2.0.0 (Decision 61, Eric; Milestone 9)."
- Spec 9, Decision 87: "`release/release.sh`, `release-build.yml`, `windows.yml`, `install.sh`, `install.ps1`, formula template, Scoop and winget publishing | Milestone 9; Scoop and winget included (Decision 87, extending Decision 61)."
- Spec 10, Milestone 9 exit: "a tagged dry-run release produces signed, notarized macOS binaries, Actions-built Linux and Windows archives, the `agentbus2` formula, Scoop and winget manifests, and working `install.sh` and `install.ps1`; then 2.0.0."
- Spec 6.1 (Decision 33, Decision 32 as the reason): binary `agentbus2`; Homebrew formula `agentbus2`; environment prefix `AGENTBUS2_`; v2 never touches v1 data. A v1 `agentbus` installed next to v2 is never read, replaced or removed by anything here.
- Spec 1.3 and 9 (Decision 31): "v1 logic is ported deliberately and each commit names its v1 source." Every porting commit message in this plan names the v1 file.
- Spec 5.3 (Decision 53): the build "keeps `CGO_ENABLED=0`, so release builds stay plain Go" (static binaries; the Makefile already exports `CGO_ENABLED=0`).
- Spec 8 (Decision 28): `make verify` is the done gate; "A run that skips the Postgres suites is not a pass." This milestone adds `release-check` to `verify` (Task 6); it adds no skip anywhere (the one `SKIP` line under it is v1's `SKIP brew checks` in `test-render.sh` when `brew` is absent, an optional extra over checks that always run; Task 5's install check runs on both hosts).
- Project `CLAUDE.md`: work lands directly on `main`; the version variable is `github.com/ericfitz/agentbus2/internal/version.Version`.
- Task constraints from the dispatch: nothing automated signs, notarizes, pushes a tag, publishes to Homebrew, Scoop or winget, or uses Apple or GitHub secrets; those steps are Tasks 9 and 10, marked **needs Eric**. Nothing in this plan pushes to `origin`.
- Definition of Done (global `CLAUDE.md`): harnesses are code and test themselves against recorded output (C); stateful steps say what a second run, an overlapping run and a failed run's leftovers do (D); docs are checked against source (E); anything beyond a one-liner is a script with `set -euo pipefail` and absolute paths (F).
- Tools the done gate gains with Task 6 (all present on `ubuntu-latest` and on Eric's Mac): `python3`, `jq`, `ruby` (`ruby -c` only), `shasum`, OpenSSL 3 or newer (`openssl` on `PATH`, or Homebrew `openssl@3`), `git`, `zip` (Linux only, for the notarization archive when `ditto` is absent), `bash` 3.2 or newer (`#!/usr/bin/env bash`; macOS's `/bin/bash` suffices, as for v1's scripts), `curl` (`test-install.sh --self-test`'s server probes and Task 5's install check on Linux; on Darwin that check runs inside a container that has it), and Docker on Darwin only, for Task 5's install check (`verify` already requires Docker on every host, because `check-gates` runs the suite binaries in `busybox`). `shellcheck`, `actionlint`, Docker's installer matrix and PowerShell are needed only by the separate CI jobs and the local pre-release checks, never by `verify`.
- winget: the dry run (`--no-publish`) renders the formula, the Scoop manifest and the three winget manifest files locally from templates (`render_formula`, `render_scoop`, `render_winget`, Task 3; `sum_of` over the local archives gives every hash) and the harness asserts them (Task 5), so the exit criterion's "formula, Scoop and winget manifests" are all produced by the tagged dry run. The winget files are komac's three-file layout under `$DIST/winget/`: `ericfitz.agentbus2.yaml` (version manifest), `ericfitz.agentbus2.installer.yaml`, `ericfitz.agentbus2.locale.en-US.yaml`. Submission is still komac's: the first release is submitted from those rendered files with `komac submit dist/winget` (Task 10 Step 3, after publish, because the URLs must be public; komac 2.17.1 has `submit <PATH> [--dry-run|--yes]`, confirmed on this Mac), later releases through `release.sh`'s `submit_winget` (`komac update ... --urls ... --submit`, v1's body), which downloads the public zips itself.
- Shared helpers: one text, copied byte for byte into `release/release.sh`, `release/test-install.sh` and `release/test-release.sh` between the comment lines `# BEGIN shared release helpers` and `# END shared release helpers` (`release/test-pipeline.sh` sources `release.sh` and uses its copy; Task 7's `shared-helpers-identical` extracts the three blocks and `cmp`s them). A bug found in it is fixed here and in all three copies, never in one:

```bash
# BEGIN shared release helpers
# release_openssl prints the path of an OpenSSL 3 binary: $AGENTBUS2_RELEASE_OPENSSL,
# else Homebrew openssl@3, else the first of openssl, openssl3 on PATH whose
# "version" major is 3 or more. It dies naming the places it looked.
release_openssl() {
    local cand major
    local tried=()
    if [[ -n "${AGENTBUS2_RELEASE_OPENSSL:-}" ]]; then
        cand="$AGENTBUS2_RELEASE_OPENSSL"
        major="$("$cand" version 2>/dev/null | sed -n 's/^OpenSSL \([0-9][0-9]*\)\..*/\1/p')"
        if [[ -n "$major" && "$major" -ge 3 ]]; then printf '%s\n' "$cand"; return 0; fi
        echo "error: AGENTBUS2_RELEASE_OPENSSL=$cand is not OpenSSL 3 or newer ($("$cand" version 2>&1 | head -n1))" >&2
        return 1
    fi
    if command -v brew >/dev/null 2>&1; then
        cand="$(brew --prefix openssl@3 2>/dev/null)/bin/openssl"
        if [[ -x "$cand" ]]; then printf '%s\n' "$cand"; return 0; fi
        tried+=("$cand")
    fi
    for cand in openssl openssl3; do
        if ! command -v "$cand" >/dev/null 2>&1; then tried+=("$cand (not on PATH)"); continue; fi
        major="$("$cand" version 2>/dev/null | sed -n 's/^OpenSSL \([0-9][0-9]*\)\..*/\1/p')"
        if [[ -n "$major" && "$major" -ge 3 ]]; then command -v "$cand"; return 0; fi
        tried+=("$cand ($("$cand" version 2>&1 | head -n1))")
    done
    echo "error: no OpenSSL 3 found; set AGENTBUS2_RELEASE_OPENSSL; tried: ${tried[*]}" >&2
    return 1
}
# utc_minus_60s prints the UTC time one minute ago as YYYY-MM-DDTHH:MM:SSZ (BSD date, then GNU date).
utc_minus_60s() {
    date -u -v-60S +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -d '-60 seconds' +%Y-%m-%dT%H:%M:%SZ
}
# END shared release helpers
```

## Delegated choices made here

The spec fixes the product names and leaves the artifact names to this milestone. Each choice is the Decision 32/33 analog of the v1 name, so v1 and v2 assets, formulas, manifests, keys and environment variables can never collide:

1. Asset names: `agentbus2-vX.Y.Z-macos-universal.tar.gz` (holding `agentbus2`), `agentbus2-vX.Y.Z-linux-{amd64,arm64}.tar.gz` (holding `agentbus2`), `agentbus2-vX.Y.Z-windows-{amd64,arm64}.zip` (holding `agentbus2.exe` and `README.txt`), plus `install.sh`, `install.ps1`, `SHA256SUMS`, `SHA256SUMS.sig` on every release. `SHA256SUMS` lists the five archives in the order macOS, linux-amd64, linux-arm64, windows-amd64, windows-arm64, then `install.sh`, `install.ps1`.
2. Repository and ids: `GH_REPO=ericfitz/agentbus2`; formula class `Agentbus2`, file `Formula/agentbus2.rb` in the existing tap `ericfitz/homebrew-tap`; Scoop manifest `bucket/agentbus2.json` in the existing `ericfitz/scoop-bucket`; winget id `ericfitz.agentbus2`. The tap and bucket already exist for v1 and gain one file each; no new repository.
3. Release key: `~/.keys/agentbus2-release-ed25519.pem` (override `AGENTBUS2_SIGNING_KEY`), public key committed as `release/agentbus2-release-ed25519.pub`, embedded between the marker lines `# BEGIN agentbus2 release public key` and `# END agentbus2 release public key`; the placeholder middle line stays `REPLACED-BY-release/embed-key.sh` (a path, unchanged). Whether the pair is new or v1's is pending Eric (proposal 1); the tooling is the same either way.
4. Installer environment: `AGENTBUS2_VERSION`, `AGENTBUS2_INSTALL_DIR`, `AGENTBUS2_BASE_URL`, `AGENTBUS2_SKIP_SIGNATURE` in both installers, and `AGENTBUS2_TEST_OSARCH` in `install.ps1` only (v1's test hook, `install.ps1` lines 8 and 41; `install.sh` has no such hook and Task 1 adds none). `AGENTBUS2_SELFTEST_FAIL` is `test-install.ps1`'s own variable, not an installer hook. Task 8's table documents the four user-facing variables only (Decision 33's prefix). Defaults: Linux `$HOME/.local/bin/agentbus2`, Windows `%LocalAppData%\Programs\agentbus2\agentbus2.exe`, user `PATH` entry for that directory. The temp name is `.agentbus2.tmp.$$`, the Windows rename-aside suffix `agentbus2.exe.old-*`, the PATH broadcast variable `AGENTBUS2_PATH_REFRESH`.
5. Dropped v1 text: the "Linux builds start at v1.14.1" and "Windows builds start at v1.14.1" advice. Every v2 release has every archive, so a 404 on the archive says `release <tag> has no Linux build: <asset> was not found at <url>` (Windows: `no Windows build`) and nothing more.
6. One build script. `release/build-archive.sh` is the only place the Go build command for the release archives lives; `release-build.yml` calls it on each runner and the dry run calls it through the `gh` stub. `release.sh`'s macOS build calls it twice (arm64, amd64) from the tag's checkout and runs `lipo` over the results. The Milestone 7 `Dockerfile` carries its own `go build -ldflags "... -X github.com/ericfitz/agentbus2/internal/version.Version=..."` line for the image; it is not a release archive, and Task 7's `version-var-exists` checks its `-X` path too when the file exists.
7. Where the checks run. `make release-check` (under `verify`): `check-pins.sh` over the real workflows, `test-install.sh --self-test`, `test-release.sh`, `test-render.sh`, `build-archive.sh --self-test`, `test-pipeline.sh`, `vet-cross` (the same list as Task 6's target). Separate CI jobs, not under `verify`: `release-lint` (`actionlint`, `shellcheck`), `release-install-check` (the Docker matrix of `test-install.sh`), `windows` (`windows.yml`: Go vet and unit tests, the `install.ps1` harness under both PowerShell hosts), `m9-exit-selftest` (`scripts/m9-exit-checks.sh`, whose real-repo case `pipeline-selftest` runs `test-pipeline.sh --self-test`, so the dry run's own harness self-test (Definition of Done C) is wired into CI and into Task 10 Step 1). Reason: `verify` already needs Docker and minutes; the matrix is six images times about forty cases, the Windows harness needs Windows, and `actionlint` is not preinstalled anywhere. The dry run is under `verify` because it is the exit criterion's automated half.
8. Portability changes to v1 scripts, because the dry run runs on `ubuntu-latest`: OpenSSL is resolved by `release_openssl` and the one-minute clock slack by `utc_minus_60s`, both exactly the shared helpers block of Global Constraints (`$AGENTBUS2_RELEASE_OPENSSL`, else Homebrew `openssl@3`, else the first of `openssl`, `openssl3` on `PATH` whose `version` major is 3 or more; `date -u -v-60S` when that works, else `date -u -d '-60 seconds'`); the universal binary's `agentbus2 version` check runs only when `uname -s` is `Darwin` (the real release always runs there; on Linux the dry run checks the linux archive instead); `sed -i ''` becomes write-then-`mv`; `ditto` is used when present, else `zip -j`.

## Claude proposal, pending Eric

Nothing here blocks the automated run; each is decided when Eric runs Task 9.

1. **Release key pair.** Proposal: generate a new pair for agentbus2 (`~/.keys/agentbus2-release-ed25519.pem`) rather than reusing v1's `agentbus-release-ed25519.pem`. Each installer verifies with the key embedded in it, so the choice has no effect on v1 releases; a new pair keeps a compromise of one product's key from affecting the other and matches Decision 32's "separate throughout". Reuse is one `cp` and the same `embed-key.sh` run.
2. **Docker image publishing.** The Milestone 7 plan hands "image publishing" to this milestone (its choice 7: "Image publishing is Milestone 9's"); spec 10's Milestone 9 list does not name an image. Task 4 writes the workflow, `.github/workflows/publish-image.yml`, gated off by construction: it runs on `release: published` (and `workflow_dispatch` with a `tag` input for reruns) and its one job has `if: ${{ vars.IMAGE_REPOSITORY != '' }}`, so with the repository variable unset nothing is built or pushed, and nothing runs during the `--no-publish` dry run, which only dispatches `release-build.yml`. The job checks out the tag, builds with `docker build --build-arg VERSION=<tag without v>` (the Milestone 7 `Dockerfile`'s `ARG VERSION`), runs `docker run --rm --entrypoint /agentbus2 <image> version` and fails unless it prints that version, then pushes `<repo>:<version>` and `<repo>:latest`. Three questions for Eric, answered in Task 9 Step 1 and recorded in the Gate 0 file (Task 9 Step 7 then sets the variable and the permission): (a) the registry and repository (proposal: `ghcr.io/ericfitz/agentbus2`, the value of `vars.IMAGE_REPOSITORY`), (b) the package's visibility (proposal: public, set once after the first push), (c) whether the image ships with 2.0.0 or after it (setting the variable before Task 10 Step 3 ships it with 2.0.0; leaving it unset ships nothing, and a later `workflow_dispatch` on `v2.0.0` publishes the image retroactively). No decision is made by the automated run.
3. **Authenticode for 2.0.0.** `--windows-signing=on|off` is chosen per release (v1 Authenticode design); the Artifact Signing account bills monthly and must be re-set-up for `repo:ericfitz/agentbus2` (Task 9). Proposal: 2.0.0 with `off` unless the account is already live when Task 10 runs.
4. **`windows.yml` stays informational** (as v1: it does not gate). Confirm, or make it required once Milestone 6's Windows-specific code is on `main`.
5. **`release-check` under `verify`** (choice 7) adds the tools listed in Global Constraints to the done gate. Ratify or move the dry run to its own CI job.

(An earlier draft's proposal 6, that the winget manifest be proven only at publish, is withdrawn: the dry run renders the winget manifests from templates, which is what spec 10 says, so nothing is pending there.)

## Not ported

The macOS-only assumptions listed in choice 8; the v1 `notes-v1.*.md` files; `release/test-install.sh`'s `key_matches_pub` (v1 lines 128 to 139 and its matrix-mode call at line 258), which emits `FAIL` when the `.pub` is missing and is replaced in both modes by the `key-block` case of Task 1, which passes on the placeholder; `docs/windows-checklist.md`'s harness-integration and WSL sections (Milestone 6 and 8 concerns; the packaging lines move into `docs/release.md`'s pre-release list); `release/test-release.sh`'s `removed-*` cases that assert v1 refactors (`build_linux`, `smoke_linux`, `ensure_release` never existed here); the `AGENTBUS_*` names; the "builds start at v1.14.1" advice (choice 5).

## Review Focus

Inputs the spec implies but no v1 test exercises, each pinned to a task:

1. **`curl | sh` of a truncated `install.sh`** (a dropped connection mid-download): the script must do nothing, not run a partial `main`. Task 1 self-test case `truncated-script-does-nothing` pipes three prefixes of the script to `sh` with `AGENTBUS2_BASE_URL=http://127.0.0.1:1` and a temp `HOME`: the first `N/2` lines, a prefix that ends inside a function body (the line after the first `download()` line, or whichever function the implementer picks; the case asserts the cut line is indented, so it is inside a body), and all lines but the last (`main "$@"`). For every prefix: no file is created under the temp `HOME` and the output holds no `agentbus2 install:` line (every installer message carries that prefix, so its absence proves `main` never ran and nothing was fetched); for the two mid-script prefixes `sh`'s own `syntax error` with a non-zero exit is the expected, safe outcome; only the last-line prefix must exit 0 and print nothing.
2. **A v1 `agentbus` next to v2** (Decision 32): `install.sh` into a `~/.local/bin` that holds `agentbus` leaves that file byte-identical; `install.ps1` into a machine with `%LocalAppData%\Programs\agentbus` on the user `PATH` leaves that directory, its `agentbus.exe` and its `PATH` entry untouched. Task 1 case `v1-untouched`; Task 2 case `v1 sibling untouched`.
3. **`install.sh` on macOS**: refuses naming `brew install ericfitz/tap/agentbus2`. Task 1 self-test case `darwin-refused` (a fake `uname` answering `Darwin`).
4. **A tag cut before `release/build-archive.sh` landed**: the workflow would fail after the macOS build. Task 3 extends `check_workflow_at_tag` to require both `.github/workflows/release-build.yml` and `release/build-archive.sh` at the tag; test `workflow-at-tag-missing-build-script`.
5. **A version that is not what the tag says** (a stale `-X` path after a refactor): every binary the pipeline ships must print the tag's version. Task 4's `build-archive.sh` checks it on the native runner; Task 5's dry run checks the host-native archive from `dist/`; Task 7's mutation 4 breaks the `-X` path in `release/build-archive.sh` and asserts the dry run fails with `version mismatch`.

---

### Task 1: `install.sh`, `embed-key.sh` and the Linux installer harness

**Depends on:** none

**Files:**
- Create: `install.sh`, `release/embed-key.sh`, `release/test-install.sh`, `release/testdata/Dockerfile.ubuntu`, `release/testdata/Dockerfile.debian`, `release/testdata/Dockerfile.alpine`, `release/testdata/fixture-server.py`
- Test: `release/test-install.sh` (`--self-test` is Docker-free; the matrix needs Docker)

**Port from v1:** `install.sh`, `release/embed-key.sh`, `release/test-install.sh`, `release/testdata/Dockerfile.{ubuntu,debian,alpine}`, `release/testdata/fixture-server.py`.

**Interfaces:**
- Produces: `install.sh` (POSIX `sh`; env of choice 4; the key block between the choice 3 markers holding the placeholder `PUBKEY_PEM='-----BEGIN PUBLIC KEY-----\nREPLACED-BY-release/embed-key.sh\n-----END PUBLIC KEY-----'` until Task 9 embeds the key; messages prefixed `agentbus2 install:`; `main "$@"` is the last line). `release/embed-key.sh <pubkey.pem> <script>...` (idempotent; `.ps1` gets the here-string form, anything else `PUBKEY_PEM='<pem>'`; exit 2 usage, 1 on a missing or non-PEM key, missing markers, or `END` before `BEGIN`). `release/test-install.sh [--quick] [--self-test]` printing `PASS <name>`/`FAIL <name>` lines and `test-install: N passed, M failed`. `release/testdata/fixture-server.py <root> <port>` (unchanged protocol: `/<variant>/releases/latest` 302, `/<variant>/releases/download/<tag>/<asset>`, 404 otherwise, 400 on `..`).
- The `--self-test` mode is what `make release-check` runs (Task 6) and what `scripts/m9-exit-checks.sh` mutates against (Task 7): besides v1's fixture-builder and server checks it holds the `embed-key.sh` unit cases (v1 `test-release.sh` lines 27 to 70, moved here because this task owns the script) and a `key-block` check of the real `install.sh`: both markers present exactly once, in order, and the block holds either the placeholder or a PEM equal to `release/agentbus2-release-ed25519.pub` when that file exists (`FAIL key-block: ...` otherwise; the placeholder is a pass, so the automated run passes before Task 9). `key-block` replaces v1's `key_matches_pub` (v1 `test-install.sh` lines 128 to 139) in **both** modes: v1's matrix mode calls `key_matches_pub` at line 258 and emits `FAIL key-matches-pub: ... missing` when the `.pub` is absent, which would make `--quick`, the full matrix and the `release-install-check` CI job red on `main` until Task 9; here the matrix mode runs `key-block` instead, first, with the same placeholder-passes rule, and a committed `.pub` that differs from the block is a `FAIL` in both modes (Task 7's `installers-key-block` repeats the invariant from the exit checks). **Order:** `key-block` is the first case in both modes and runs before `build_fixture`; v1's `self_test` (line 143) calls `build_fixture` first, which runs `embed-key.sh` over the real `install.sh` and dies under `set -e` on a missing marker before any `FAIL` line prints, and Task 7's mutation 3 asserts the `FAIL key-block` line plus exit 1, so the order matters. An unrecognized argument (anything but `--quick` and `--self-test`) exits 2 with `usage: test-install.sh [--quick] [--self-test]` (v1 line 31); self-test case `unknown-arg` runs `bash "$0" --bogus` and asserts that.

- [ ] **Step 1: Write `release/test-install.sh` with the v1 cases renamed and the new ones**

v1's case list (`valid-piped` ... `killed-mid-install`) with `agentbus` → `agentbus2`, `AGENTBUS_` → `AGENTBUS2_`, `~/.local/bin/agentbus2`, the stub binary printing the version, `installed agentbus2 9.0.1`, `agentbus2 init --global` in the next-step line. Drop `release-without-linux-build-advice`; `release-without-linux-build` wants `release v9.0.1 has no Linux build: agentbus2-v9.0.1-linux-<arch>.tar.gz`. Add: `v1-untouched` (Review Focus 2: `mkdir -p ~/.local/bin && printf 'v1\n' > ~/.local/bin/agentbus; sh /fixture/install.sh && [ "$(cat ~/.local/bin/agentbus)" = v1 ] && ~/.local/bin/agentbus2 version`, want exit 0 and `9.0.1`). Self-test additions: `darwin-refused` (Review Focus 3: a fake `uname` on `PATH` answering `Darwin` to `-s`; want exit 1 and `brew install ericfitz/tap/agentbus2`), `truncated-script-does-nothing` (Review Focus 1: three prefixes, `head -n $((N/2))`, a cut inside a function body, and `head -n $((N-1))`; each run as `head -n <k> install.sh | env HOME=<tmp> AGENTBUS2_BASE_URL=http://127.0.0.1:1 sh` and judged as Review Focus 1 states), `key-block` (Interfaces; in matrix mode too, in place of v1's `key_matches_pub` call), the `embed-key.sh` cases (`embed-sh`, `embed-sh-has-key`, `embed-sh-keeps-rest`, `embed-sh-valid-sh`, `embed-sh-key-roundtrip`, `embed-ps1`, `embed-ps1-has-key`, `embed-idempotent`, `embed-missing-marker`, `embed-marker-near-miss`, `embed-marker-order`, `embed-marker-order-untouched`, `embed-keeps-mode`, `embed-readonly-input`, `embed-missing-pub`, `embed-bad-pub`, `embed-usage`, `unknown-arg`), with `key-block` first (Interfaces). The harness resolves OpenSSL with the shared helpers block of Global Constraints, pasted byte for byte between its marker comments (Task 7 checks the copies agree), replaces `sed -i ''` with write-then-`mv`, and passes `--add-host host.docker.internal:host-gateway` on every `docker run` (a no-op on Docker Desktop, required on Linux). Fail fast with one message naming every missing tool (`docker` only in matrix mode; `python3`, `curl`, `shasum`, `tar`, OpenSSL 3 in both modes, as Global Constraints lists them under the `verify` tools).

- [ ] **Step 2: Run the self-test to see it fail**

Run: `bash release/test-install.sh --self-test`
Expected: fails at the first line that needs `install.sh` or `embed-key.sh` (`No such file`).

- [ ] **Step 3: Write `release/embed-key.sh` and `install.sh`**

`embed-key.sh`: v1 with the choice 3 markers. `install.sh`: v1 with choice 4 and 5 applied and the Darwin branch's advice `brew install ericfitz/tap/agentbus2`; `report` prints `installed agentbus2 <v> to <dir>/agentbus2` and `next: agentbus2 init --global (once per machine), then agentbus2 init inside each repository`. The Dockerfiles are v1's (`alpine:3.20`, `debian:12`, `ubuntu`), `fixture-server.py` unchanged but for its docstring.

- [ ] **Step 4: Run the self-test and the quick matrix**

Run: `bash release/test-install.sh --self-test && bash release/test-install.sh --quick`
Expected: every line `PASS`, `test-install: N passed, 0 failed` twice; `shellcheck -s sh install.sh && shellcheck -x release/embed-key.sh release/test-install.sh` clean (local only; CI's `release-lint` job repeats it).

- [ ] **Step 5: Run the full matrix once** (Docker Desktop runs arm64 under emulation)

Run: `bash release/test-install.sh`
Expected: `test-install: N passed, 0 failed` over ubuntu, debian, alpine × amd64, arm64.

- [ ] **Step 6: Commit**

```bash
git add install.sh release/embed-key.sh release/test-install.sh release/testdata/Dockerfile.ubuntu release/testdata/Dockerfile.debian release/testdata/Dockerfile.alpine release/testdata/fixture-server.py
git commit -m "feat(release): install.sh, embed-key.sh and the Linux installer harness ported from v1 install.sh, release/embed-key.sh, release/test-install.sh and release/testdata (AGENTBUS2_ names, agentbus2 paths, no v1.14.1 advice)"
```

---

### Task 2: `install.ps1`, the Windows harness, the zip README and `windows.yml`

**Depends on:** none

**Files:**
- Create: `install.ps1`, `release/test-install.ps1`, `release/test-windows.ps1`, `release/windows-zip-README.txt`, `release/testdata/fixture-server.ps1`, `release/testdata/stub/go.mod`, `release/testdata/stub/main.go`, `.github/workflows/windows.yml`
- Test: `release/test-install.ps1` (`-SelfTest` runs anywhere with Go and OpenSSL 3; the cases need Windows and run in `windows.yml` and in Eric's VM through `test-windows.ps1`)

**Port from v1:** `install.ps1`, `release/test-install.ps1`, `release/test-windows.ps1`, `release/windows-zip-README.txt`, `release/testdata/fixture-server.ps1`, `release/testdata/stub/{go.mod,main.go}`, `.github/workflows/windows.yml`.

**Interfaces:**
- Produces: `install.ps1` (choice 4 env and paths; messages prefixed `agentbus2 install:`; the here-string key block between the choice 3 markers with the placeholder assembled from parts as v1 does; `Main @args` guarded by the dot-source check). `release/windows-zip-README.txt` (the v2 commands: `irm https://github.com/ericfitz/agentbus2/releases/latest/download/install.ps1 | iex`, `scoop bucket add ericfitz https://github.com/ericfitz/scoop-bucket`, `scoop install agentbus2`, the by-hand copy into `$env:LOCALAPPDATA\Programs\agentbus2`, `agentbus2 init --global` and `agentbus2 init`, docs link `https://github.com/ericfitz/agentbus2/blob/main/docs/install.md`). `windows.yml`: informational (proposal 4), `push` to `main` and `workflow_dispatch`, matrix `windows-latest`, `windows-11-arm`, steps `go vet ./...`, the unit tests as `go test $(go list ./... | grep -v internal/conformance)` under `shell: bash` (the suites need Docker, which the Windows runners lack; `make test` is not assumed on Windows), `test-install.ps1 -SelfTest` and `test-install.ps1` under `powershell`, `test-install.ps1` under `pwsh`; every `uses:` pinned `@<40-hex sha> # vX.Y.Z` (Task 3's `check-pins.sh` enforces it over the real directory from Task 6 on).
- `release/testdata/fixture-server.ps1 -Root <dir> -Port <n>` is the PowerShell twin of `fixture-server.py` and `test-install.ps1` starts it for every case (v1 `test-install.ps1` line 267), so it ships with the harness: `GET`/`HEAD /<variant>/releases/latest` answers 302 to `/<variant>/releases/tag/<tag>`, `/<variant>/releases/download/<tag>/<asset>` serves the file, 404 otherwise, 400 on a segment that is empty, `.`, `..` or holds a path or drive character, 405 on any other method; the `slow` variant waits 3 s before a download. Unchanged from v1 but for its header comment.
- The stub module stays a separate Go module (`module stub`, `go 1.27`) under `release/testdata/stub/`, so `go build ./...` of the main module never sees it; its `main.go` prints `version` and sleeps on `sleep`, as v1.

- [ ] **Step 1: Write `release/test-install.ps1` with v1's cases renamed and the new one**

Rename `agentbus` → `agentbus2` in asset names, install directory (`Programs\agentbus2`), binary name, messages and env (`AGENTBUS2_*`); drop the "Windows builds start at v1.14.1" expectation (the 404 case wants `release <tag> has no Windows build`). Add case `v1 sibling untouched` (Review Focus 2): before the run create `<TestRoot>\Programs\agentbus` holding a stub `agentbus.exe` and append that directory to the (saved and restored) user `PATH`; after a valid install assert the file's hash is unchanged, the directory still holds exactly that file and the `PATH` still holds that entry once. The `-SelfTest` additions: the key block of the real `install.ps1` holds the markers once, in order, and the placeholder or the committed `.pub` (as Task 1's `key-block`).

- [ ] **Step 2: Run the self-test to see it fail**

Precondition: `pwsh` is not installed on this Mac (`command -v pwsh` printed nothing when this plan was written). Either run `brew install --cask powershell` first and verify Steps 2 and 4 locally, or state in the commit body that Steps 2 and 4 are verified only by `windows.yml` on the first push to `main`; one of the two, named.

Run: `pwsh -NoProfile -File release/test-install.ps1 -SelfTest`
Expected: fails for want of `install.ps1`.

- [ ] **Step 3: Write `install.ps1`, `windows-zip-README.txt`, `fixture-server.ps1`, the stub module, `test-windows.ps1`, `windows.yml`**

As in Interfaces; `install.ps1` keeps every v1 mechanism (registry architecture, Git for Windows OpenSSL search, rename-aside swap loop, `ExpandString` PATH write, the `WM_SETTINGCHANGE` broadcast through `AGENTBUS2_PATH_REFRESH`).

- [ ] **Step 4: Run the self-test; confirm the stub module is invisible to the main module**

Run: `pwsh -NoProfile -File release/test-install.ps1 -SelfTest; go list ./... | grep -c testdata/stub`
Expected: every self-test line `PASS`; the grep count is `0`.

- [ ] **Step 5: Commit**

```bash
git add install.ps1 release/test-install.ps1 release/test-windows.ps1 release/windows-zip-README.txt release/testdata/fixture-server.ps1 release/testdata/stub/go.mod release/testdata/stub/main.go .github/workflows/windows.yml
git commit -m "feat(release): install.ps1, the Windows installer harness, the zip README and windows.yml ported from v1 install.ps1, release/test-install.ps1, release/test-windows.ps1, release/windows-zip-README.txt, release/testdata/fixture-server.ps1, release/testdata/stub and .github/workflows/windows.yml"
```

---

### Task 3: `release.sh`, the formula, Scoop and winget templates, `check-pins.sh`, `check-linux-brew.sh` and their tests

**Depends on:** none

**Files:**
- Create: `release/release.sh`, `release/agentbus2.rb.tmpl`, `release/agentbus2.scoop.json.tmpl`, `release/agentbus2.winget.version.yaml.tmpl`, `release/agentbus2.winget.installer.yaml.tmpl`, `release/agentbus2.winget.locale.en-US.yaml.tmpl`, `release/check-pins.sh`, `release/check-linux-brew.sh`, `release/test-release.sh`, `release/test-render.sh`
- Test: `release/test-release.sh`, `release/test-render.sh`

**Port from v1:** `release/release.sh`, `release/agentbus.rb.tmpl`, `release/agentbus.scoop.json.tmpl`, `release/check-pins.sh`, `release/check-linux-brew.sh`, `release/test-release.sh`, `release/test-render.sh`.

**Interfaces:**
- Produces `release/release.sh <tag> --windows-signing=on|off [--no-publish]` with v1's function set and order (`parse_args`, `require_tools`, `check_signing_key`, `check_tag_exists`, `check_tag_pushed`, `check_gh`, `check_workflow_at_tag`, `check_signing_vars`, `check_installers_embedded`, `checkout_tag`, `render_notes`, `build_macos`, `ensure_draft`, `dispatch_build`, `watch_run`, `download_archives`, `verify_attestations`, `copy_scripts`, `write_sums`, `sign_sums`, `upload_assets`, `publish_release`; helpers `render_template`, `sum_of`, `check_script_embedded`, `render_notes_text`, `list_run_ids`, `find_run`, `verify_sums`, `update_tap`, `update_scoop`, `submit_winget`, the new `render_formula`, `render_scoop` and `render_winget`, and the shared helpers block of Global Constraints, `release_openssl` and `utc_minus_60s`, pasted byte for byte between its marker comments), the constants of choice 2 and 3 (`BIN_NAME=agentbus2`, `GH_REPO=ericfitz/agentbus2`, `WINGET_ID=ericfitz.agentbus2`, no `VERSION_VAR`: the `-X` path lives only in `release/build-archive.sh` (choice 6), `SIGNING_KEY="${AGENTBUS2_SIGNING_KEY:-$HOME/.keys/agentbus2-release-ed25519.pem}"`, `PUBKEY=$REPO_ROOT/release/agentbus2-release-ed25519.pub`, `SIGN_IDENTITY` and `NOTARY_PROFILE` as v1, `KOMAC_KEY_FILE=$HOME/.keys/KOMAC_GITHUB_KEY`), sourceable without running (`if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then main "$@"; fi`). `REPO_ROOT`, `PUBKEY`, `DIST`, `SIGNING_KEY`, `KOMAC_KEY_FILE`, `TAP_DIR` and `SCOOP_DIR` are computed at source time, so a harness that sources the file assigns its own values **after** `source` (as v1's tests do). `build_macos` runs the **tag's** copy of Task 4's script from the worktree `checkout_tag` made, `(cd "$SRC" && bash release/build-archive.sh "$TAG" darwin <arch> "$DIST/macos-<arch>")` for `arm64` and `amd64` (not `$REPO_ROOT`'s copy: `check_workflow_at_tag` requires the script at the tag for this reason; this task's tests never call `build_macos`), `lipo -create`s the two binaries into `$DIST/macos/agentbus2`, runs the universal binary's `version` check only on Darwin (choice 8), codesigns, notarizes (`notarize.zip` is removed afterwards, `notary.log` stays), and writes `$DIST/agentbus2-<tag>-macos-universal.tar.gz`. `check_workflow_at_tag` requires both `.github/workflows/release-build.yml` and `release/build-archive.sh` at the tag (Review Focus 4; message `<path> is not in tag <tag>; tag a commit that has it`). `checkout_tag` runs `git -C "$REPO_ROOT" worktree prune` before `worktree add` (v1 never prunes, so a killed run's stale registration under a removed `$TMPDIR` would break the next `worktree add`), and `main` recreates `$DIST` (`rm -rf` then `mkdir`) as v1 does, so a killed run's partial `dist/` is replaced. **Render split from push:** `render_formula` renders `release/agentbus2.rb.tmpl` to `$DIST/agentbus2.rb` (the three URLs and `sum_of` of the macOS, linux-amd64 and linux-arm64 archives), `render_scoop` renders `release/agentbus2.scoop.json.tmpl` to `$DIST/agentbus2.scoop.json` (the version, the two Windows URLs and hashes), and `render_winget` renders the three winget templates to `$DIST/winget/ericfitz.agentbus2.yaml`, `$DIST/winget/ericfitz.agentbus2.installer.yaml` and `$DIST/winget/ericfitz.agentbus2.locale.en-US.yaml` (komac's file names, so `komac submit "$DIST/winget"` takes the directory as is; the same version, URLs and hashes as the Scoop manifest); `publish_release` calls the three first and prints `rendered: <path>` for each of the five files, then under `--no-publish` stops as v1 does (`stopping before publish; draft: <url>`, or `release:` on a rerun); otherwise `gh release edit --draft=false` unless `PUBLISHED`, then `update_tap` and `update_scoop`, which are v1's bodies less the render (clone or pull, `cmp` the rendered file against `Formula/agentbus2.rb` or `bucket/agentbus2.json`, copy, commit `agentbus2 <version>`, always push), then `submit_winget`. Everything that touches the network or Eric's tools is behind a command name (`gh`, `codesign`, `xcrun`, `lipo`, `ditto`, `komac`, `git push`), which is the seam Task 5 stubs with shell functions.
- `render_template <tmpl> <out> KEY=VALUE...` and `sum_of <file>` are unchanged; `release/agentbus2.rb.tmpl` (class `Agentbus2`, `desc "Message bus, shared memory and shared task queue for coding agents"`, `homepage "https://github.com/ericfitz/agentbus2"`, `license "Apache-2.0"`, the three `url`/`sha256` host branches with `__MACOS_URL__`, `__MACOS_SHA256__`, `__LINUX_AMD64_URL__`, `__LINUX_AMD64_SHA256__`, `__LINUX_ARM64_URL__`, `__LINUX_ARM64_SHA256__`, `bin.install "agentbus2"`, caveats naming `agentbus2 init --global` and `agentbus2 init`, `test do assert_equal version.to_s, shell_output("#{bin}/agentbus2 version").strip end`); `release/agentbus2.scoop.json.tmpl` (`__VERSION__`, `__AMD64_URL__`, `__AMD64_SHA256__`, `__ARM64_URL__`, `__ARM64_SHA256__`, `"bin": "agentbus2.exe"`, `checkver` and `autoupdate` URLs under `ericfitz/agentbus2` with `agentbus2-v$version-windows-<arch>.zip` and `SHA256SUMS`, `notes` about stopping agentbus2 sessions before `scoop update agentbus2`). The three winget templates follow the winget-pkgs schema of the `ManifestVersion` komac 2.17.1 writes (the implementer reads it from `komac` or a current winget-pkgs manifest once, and pins it as a literal; no network at test time): `release/agentbus2.winget.version.yaml.tmpl` (`PackageIdentifier: ericfitz.agentbus2`, `PackageVersion: __VERSION__`, `DefaultLocale: en-US`, `ManifestType: version`), `release/agentbus2.winget.installer.yaml.tmpl` (`PackageIdentifier`, `PackageVersion: __VERSION__`, `InstallerType: zip`, `NestedInstallerType: portable`, `NestedInstallerFiles` with `RelativeFilePath: agentbus2.exe` and `PortableCommandAlias: agentbus2`, `Installers` with `Architecture: x64` / `InstallerUrl: __AMD64_URL__` / `InstallerSha256: __AMD64_SHA256__` and `Architecture: arm64` / `__ARM64_URL__` / `__ARM64_SHA256__`, `ManifestType: installer`), `release/agentbus2.winget.locale.en-US.yaml.tmpl` (`PackageLocale: en-US`, `Publisher: ericfitz`, `PackageName: agentbus2`, `License: Apache-2.0`, `PackageUrl: https://github.com/ericfitz/agentbus2`, `ShortDescription` equal to the formula's `desc`, `ManifestType: defaultLocale`). `render_winget` passes the hashes upper-cased (winget's convention); every test compares them to `SHA256SUMS` case-insensitively.
- `release/check-pins.sh [workflows-dir]` and `release/check-linux-brew.sh [amd64|arm64...]` (`brew install ericfitz/tap/agentbus2 && brew test agentbus2 && agentbus2 version`) are v1's.
- `release/test-release.sh` prints `PASS`/`FAIL` lines and exits 1 on any failure. It writes every fixture itself and never reads the repository's `install.sh`, `install.ps1` or `.github/workflows`, nor runs `release/embed-key.sh` (Task 1's file), so this task depends on nothing in Wave 1. The `check_script_embedded` and `check_installers_embedded` cases **stay here** with synthetic installers: `check_script_embedded` greps only for the literal placeholder line `REPLACED-BY-release/embed-key.sh`, so an "embedded" fixture is the two choice 3 marker lines around `PUBKEY_PEM='<the PEM lines of the throwaway key.pub>'` (for `.ps1`: `$PubKeyPem = @'` ... `'@`), and an "unembedded" one the same block with the placeholder as its middle line, both written by `printf`. The cases, with the fixture repository tagged as v1 `test-release.sh` lines 96 to 133 do: `embedded-placeholder`, `embedded-placeholder-hint`, `embedded-ok`, `preflight-no-installer` (`v0.0.1`: no `install.sh`), `preflight-placeholder` (`v0.0.3`), `preflight-no-ps1` (`v0.0.4`), `preflight-ps1-placeholder` (`v0.0.90`), `preflight-embedded` (`v0.0.91`), so every one of `check_installers_embedded`'s four messages keeps a test. Only the cases that read the repository's own files move to Task 7, by name: `pins-real-repo`, `real-unembedded`, `real-embedded`, `ps1-unembedded`, `ps1-embedded`. `release/test-render.sh` renders all five templates with fake values, `ruby -c`s the formula, `python3 -I -m json.tool`s the manifest, asserts the Scoop fields (`version`, `bin == agentbus2.exe`, `architecture.64bit.hash`, `architecture.arm64.url`, `checkver`, `autoupdate`), asserts the three winget files without a YAML library (`grep -Fx` of each key line named above with the fake values filled, `grep -c '__'` is 0, and the installer file holds exactly two `InstallerSha256:` lines), and runs `brew style` plus the stubbed URL-selection check when `brew` is installed, `SKIP brew checks` otherwise.

- [ ] **Step 1: Write `release/test-release.sh` and `release/test-render.sh`**

v1's cases renamed, less the moved "real repo" cases and the `removed-*` cases, plus: `workflow-at-tag-missing-build-script` (a repo fixture with the workflow but no `release/build-archive.sh` at the tag → exit 1, `release/build-archive.sh is not in tag`), `openssl-resolves-path` (`release_openssl` with `AGENTBUS2_RELEASE_OPENSSL` pointing at a fake printing `OpenSSL 3.0.0` → that path; pointing at one printing `OpenSSL 1.1.1w` → exit 1 naming it; `LibreSSL 3.3.6` → not accepted), `utc-minus-60s-format` (matches `^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$` and is at most 90 s before `date -u +%s`), `no-publish-renders-formula` (`NO_PUBLISH=1 publish_release` → `$DIST/agentbus2.rb` exists, `ruby -c` passes, holds the three `sha256` values of the fixture `SHA256SUMS`, and the output holds `rendered: `), `no-publish-renders-scoop` (`$DIST/agentbus2.scoop.json` parses with `python3 -I -m json.tool`, `version == "1.2.3"`, the two hashes), `no-publish-renders-winget` (the three files under `$DIST/winget/` exist with komac's names, `PackageVersion: 1.2.3` in each, the two `InstallerSha256` values equal the fixture `SHA256SUMS` windows hashes case-insensitively, and the output holds five `rendered: ` lines), while v1's `no-publish-skips-packagers` keeps asserting that `update_tap`, `update_scoop` and `submit_winget` are not called. For those the `with_stub` `$WORK/dist` fixture holds a `SHA256SUMS` naming the five `agentbus2-v1.2.3-*` archives and `install.sh`, `install.ps1`, and the fixture repository holds copies of all five templates under `release/` (the renderers read `$REPO_ROOT/release/`). The `gh` stub, the runs JSON fixtures, `with_stub`, `with_dist`, `with_vars`, `rerun_push_case` (local bare remotes with a failing `pre-receive` hook, then without: the stranded commit is pushed on the rerun) are v1's with `ericfitz/agentbus2`, `release-build v1.2.3` titles and `agentbus2-v1.2.3-*` names; every case runs `release.sh` in its own `bash -c` (v1's `with_stub` pattern), never two `main`s or two mutating functions in one shell.

- [ ] **Step 2: Run both to see them fail**

Run: `bash release/test-release.sh; bash release/test-render.sh`
Expected: both fail at `source release.sh` (no such file).

- [ ] **Step 3: Write `release/release.sh`, the five templates, `check-pins.sh`, `check-linux-brew.sh`**

As in Interfaces and choice 8. `dispatch_build` passes `-f tag=$TAG -f windows_signing=$WINDOWS_SIGNING` and dispatches `--ref "$TAG"`. `download_archives` requires the four names of choice 1. `publish_release`'s closing line prints the three install commands with the v2 names.

- [ ] **Step 4: Run the tests**

Run: `bash release/test-release.sh && bash release/test-render.sh && shellcheck -x release/release.sh release/check-pins.sh release/check-linux-brew.sh release/test-release.sh release/test-render.sh`
Expected: every line `PASS` (or `SKIP brew checks` when `brew` is absent), `test-render: OK`, shellcheck clean.

- [ ] **Step 5: Commit**

```bash
git add release/release.sh release/agentbus2.rb.tmpl release/agentbus2.scoop.json.tmpl release/agentbus2.winget.version.yaml.tmpl release/agentbus2.winget.installer.yaml.tmpl release/agentbus2.winget.locale.en-US.yaml.tmpl release/check-pins.sh release/check-linux-brew.sh release/test-release.sh release/test-render.sh
git commit -m "feat(release): release.sh, the agentbus2 formula, Scoop and winget templates, check-pins.sh and check-linux-brew.sh ported from v1 release/release.sh, release/agentbus.rb.tmpl, release/agentbus.scoop.json.tmpl, release/check-pins.sh, release/check-linux-brew.sh, release/test-release.sh and release/test-render.sh; portable to Linux, build-archive.sh required at the tag"
```

---

### Task 4: `build-archive.sh`, `release-build.yml` and the gated `publish-image.yml`

**Depends on:** none

**Files:**
- Create: `release/build-archive.sh`, `.github/workflows/release-build.yml`, `.github/workflows/publish-image.yml`
- Test: `release/build-archive.sh --self-test` (below), `actionlint`, and an `rg` of the `uses:` lines (Task 3's `check-pins.sh` is not in this tree; from Task 6 on `verify` runs it over both workflows)

**Port from v1:** `.github/workflows/release-build.yml` (the `validate`, `build`, `package-windows`, `publish` jobs; the build step's inline `go build` becomes the script).

**Interfaces:**
- Produces `release/build-archive.sh <tag> <goos> <goarch> <outdir>`: `set -euo pipefail`; the tag must match `^v[0-9]+\.[0-9]+\.[0-9]+$` (exit 2 naming it), `<goos>` one of `linux`, `windows`, `darwin`, `<goarch>` one of `amd64`, `arm64` (exit 2 naming the pair otherwise); runs, from the current directory's module, `CGO_ENABLED=0 GOOS=<goos> GOARCH=<goarch> go build -trimpath -ldflags "-s -w -X github.com/ericfitz/agentbus2/internal/version.Version=<version>" -o <outdir>/agentbus2[.exe] .`; when `go env GOHOSTOS`/`GOHOSTARCH` equal the target it runs the binary's `version` and exits 1 with `version mismatch: <got> != <want>` on a difference (Review Focus 5), else prints `note: not run (host is <os>/<arch>)`; for `linux` it also writes `<outdir>/agentbus2-<tag>-linux-<goarch>.tar.gz` holding `agentbus2`; for `windows` and `darwin` the binary alone (the zip is packaged after signing by `package-windows`; `lipo` runs in `release.sh`). Any argument that starts with `-` other than `--self-test`, and any argument count other than four, exits 2 with the usage line (`usage: build-archive.sh <tag> <goos> <goarch> <outdir> | --self-test`). `--self-test` runs the argument refusals and one native build of the current module into a temp dir, asserting the binary prints the version and, on Linux, that the tarball lists exactly `agentbus2`; prints `build-archive self-test: OK`.
- `release-build.yml`: v1's four jobs with `agentbus2` names, `run-name: release-build ${{ inputs.tag }}` (Task 3's `find_run` matches that title), inputs `tag` and `windows_signing` (`choice`, `['on', 'off']` quoted), `concurrency: release-${{ inputs.tag }}`, the build step `bash release/build-archive.sh "$TAG" "$GOOS" "$GOARCH" out` then the uploads (`archive-linux-<arch>` holding the tarball, `exe-windows-<arch>` holding `out/agentbus2.exe`), `package-windows` with `environment: release`, the seven `vars.*`, `azure/login` and `Azure/artifact-signing-action` gated on `on`, the Authenticode verification (`Status Valid`, signer CN equals `AUTHENTICODE_SIGNER`, a timestamp countersignature, the signed amd64 binary prints the version), `Compress-Archive` of `agentbus2.exe` plus `release/windows-zip-README.txt` as `README.txt` into `agentbus2-<tag>-windows-<arch>.zip`, `publish` requiring the four archives, `actions/attest-build-provenance`, `gh release upload "$TAG" archives/* --clobber`. Every `uses:` pinned by full SHA with a version comment (the v1 pins are current as of 2026-10-07; the implementer re-resolves each with `gh api repos/<owner>/<repo>/git/ref/tags/<version> --jq .object.sha` and keeps the comment). Literal anchors Task 7's `workflow-names-agree` greps for (`grep -F`), so they are written exactly so: `run-name: release-build ${{ inputs.tag }}`; in `package-windows` the two upload steps `name: archive-windows-amd64` and `name: archive-windows-arm64` and the two `Compress-Archive` lines ending in `-windows-amd64.zip"` and `-windows-arm64.zip"`; in `publish` `pattern: archive-*` and the loop `for a in linux-amd64.tar.gz linux-arm64.tar.gz windows-amd64.zip windows-arm64.zip; do` (v1 line 225); the Linux upload's `name: archive-${{ matrix.goos }}-${{ matrix.goarch }}` (a template, v1 line 90). `release/build-archive.sh` holds the literal `-linux-${goarch}.tar.gz`.
- `publish-image.yml` (proposal 2, gated off; new, no v1 source): triggers `release: types: [published]` and `workflow_dispatch` with input `tag`; one job `publish` with `if: ${{ vars.IMAGE_REPOSITORY != '' }}`, `permissions: contents: read, packages: write`, `concurrency: publish-image-<tag>`; steps: resolve the tag (`github.event.release.tag_name` or `inputs.tag`, which must match `^v[0-9]+\.[0-9]+\.[0-9]+$`), `actions/checkout` at `refs/tags/<tag>`, fail with `Dockerfile is not in tag <tag>` when the Milestone 7 `Dockerfile` is absent, `docker/setup-buildx-action`, `docker/login-action` to `ghcr.io` with `GITHUB_TOKEN` (the job fails with `IMAGE_REPOSITORY must be under ghcr.io` otherwise: the workflow is ghcr-only by construction, and another registry needs its own credential secret, which is part of Eric's answer to proposal 2(a), not something the automated run decides), `docker build --build-arg VERSION=<tag without v> -t "$IMAGE_REPOSITORY:<version>" .`, `test "$(docker run --rm --entrypoint /agentbus2 "$IMAGE_REPOSITORY:<version>" version)" = "<version>"`, then `docker tag` to `:latest` and push both. Every `uses:` pinned by SHA with a version comment. No step of the automated run triggers it: the dry run dispatches `release-build.yml` only and never publishes.

- [ ] **Step 1: Write the self-test cases first** (inside `build-archive.sh`, a `--self-test` branch, so the script is its own harness; the cases: `usage` exit 2, `bad-flag` (`--bogus` exit 2 with the usage line), `bad-tag v1.2` exit 2 `tag must look like vX.Y.Z`, `bad-pair plan9 amd64` exit 2, `native-build` prints the version, `linux-tar-lists-binary` when the host is Linux)

- [ ] **Step 2: Run it to see it fail**

Run: `bash release/build-archive.sh --self-test`
Expected: fails because the build branch does not exist yet (or the file does not exist).

- [ ] **Step 3: Write the script and the workflow**

- [ ] **Step 4: Verify**

Run: `bash release/build-archive.sh --self-test && out="$(mktemp -d)" && bash release/build-archive.sh v0.0.1 windows arm64 "$out" && ls "$out" && rm -rf "$out" && actionlint .github/workflows/release-build.yml .github/workflows/publish-image.yml && shellcheck -x release/build-archive.sh && rg -n 'uses:' .github/workflows/publish-image.yml .github/workflows/release-build.yml`
Expected: `build-archive self-test: OK`; `note: not run (host is darwin/arm64)` and `agentbus2.exe` listed; actionlint and shellcheck silent; every `uses:` line is `@<40 hex> # vX.Y.Z`.

- [ ] **Step 5: Commit**

```bash
git add release/build-archive.sh .github/workflows/release-build.yml .github/workflows/publish-image.yml
git commit -m "feat(release): build-archive.sh and release-build.yml ported from v1 .github/workflows/release-build.yml (the build step is one script shared with the local dry run), and publish-image.yml gated off until vars.IMAGE_REPOSITORY is set"
```

---

### Task 5: The local unsigned dry run of the whole pipeline

**Depends on:** 1, 2, 3, 4

**Files:**
- Create: `release/test-pipeline.sh`
- Test: itself (`--self-test`), and it is run by `make release-check` (Task 6) and mutated against by Task 7

**Interfaces:**
- Consumes: `release/release.sh` sourced (its functions and `main`; Task 3), `release/build-archive.sh` (Task 4), `install.sh` and `release/embed-key.sh` (Task 1), `install.ps1` (Task 2), `release/agentbus2.rb.tmpl`, `release/agentbus2.scoop.json.tmpl` and the three `release/agentbus2.winget.*.yaml.tmpl` (Task 3).
- Produces `release/test-pipeline.sh [--self-test]`: `PASS`/`FAIL` lines, a last line `test-pipeline: N passed, M failed`, exit 1 on any failure. No network beyond the Go module and toolchain cache `go build` already uses (and, on Darwin, the first build of Task 1's `Dockerfile.debian` image for the install check, item 4), no `~/.keys`, no Docker on Linux, nothing written under the repository; a temp dir removed on every exit path. It tests the **working tree** (tracked plus untracked-not-ignored files that still exist, copied into a fresh `git init` repository, committed and tagged there), not HEAD: Task 7's mutations are uncommitted edits in a throwaway worktree and this task's own Step 4 runs before its commit, so a clone of HEAD would test the wrong thing; and because `make verify` runs it in every later task's implementer worktree, which routinely holds new unstaged files (a new package, a new `_test.go`) and sometimes a deleted tracked file, a tracked-only copy would fail the fixture's Go build for reasons unrelated to the release tooling (say both in the header). It sources the fixture's `release/release.sh`, not `$ROOT`'s, and uses that copy's `release_openssl`; because `release.sh`'s `fail` calls `exit`, the harness calls its functions (`sum_of`, `release_openssl`, `main`) only inside `$( )` or `bash -c`, never bare in its own shell, so a missing entry prints a `FAIL` line instead of killing the run. An unrecognized argument exits 2 with `usage: test-pipeline.sh [--self-test]`. Tools: `git`, `go`, `python3`, `jq`, `shasum`, OpenSSL 3, `ruby`, `tar`, `zip` where `ditto` is absent, `curl` on Linux (the install check), `docker` on Darwin (the install check runs in a container there), and `lipo` when present.

**Design** (the implementer writes the bodies):

1. Fixture: `git init --bare "$WORK/origin.git"`; `mkdir "$WORK/repo"` and copy `$ROOT`'s working tree into it: `git -C "$ROOT" ls-files -z --cached --others --exclude-standard` (tracked plus untracked-not-ignored; `dist/` is ignored), filtered by a `while IFS= read -r -d '' p; do [[ -e "$ROOT/$p" || -L "$ROOT/$p" ]] && printf '%s\0' "$p"; done` loop (a deleted tracked file is skipped, not an error) into `tar -C "$ROOT" --null -T - -cf - | tar -C "$WORK/repo" -xf -` (both `tar`s accept `--null -T -`: bsdtar and GNU tar); `git init -q`, `git remote add origin "$WORK/origin.git"`; a throwaway Ed25519 pair (`key.pem`, `key.pub`) and a second one, `$WORK/other.pem`, for Run D; `release/embed-key.sh key.pub install.sh install.ps1` in the clone; `cp key.pub release/agentbus2-release-ed25519.pub` in the clone (so `PUBKEY` resolves whether or not Task 9 has run on `main`); `release/notes-v0.0.1.md` with one line; commit (`-c user.name=t -c user.email=t@t`), `git tag v0.0.1`, `git push -q origin v0.0.1`. Tap and bucket: two more bare remotes seeded with `Formula/.keep` and `bucket/.keep`, cloned to `$WORK/tap` and `$WORK/scoop`.
2. Stubs: one file, `$WORK/stubs.sh`, holding shell functions that shadow the commands (`require_tools` uses `command -v`, which a function satisfies); every run sources it **after** `release.sh`, so the functions win. State a stub keeps between calls and between runs lives in files under `$WORK` (`$WORK/state/created`, `$WORK/state/published`, `$WORK/runs.json`, `GH_LOG=$WORK/gh.log`, `KOMAC_LOG=$WORK/komac.log`), never in shell variables, because each run is a new process (item 3). `gh`: appends one line `gh $*` to `$GH_LOG` for every call; `auth status`, `workflow view`, `attestation verify --help`, `variable list` exit 0; `release view` follows v1 `test-release.sh` line 265 exactly: before a `release create` was recorded, `echo 'release not found' >&2; return 1` (that stderr text with a non-zero exit is what `ensure_draft` requires; stdout text or exit 0 is `unexpected output from gh`), after it `echo true` on stdout with exit 0, and `echo false` once a `release edit ... --draft=false` was recorded; `release create`, `release edit`, `release upload` record and touch the state files; `workflow run` records and writes `$WORK/runs.json` holding one run `{databaseId: 7, displayTitle: "release-build v0.0.1", createdAt: <now>}`; `run list --jq <expr>` answers `jq -r <expr>` over that file; `run watch` exits 0; `release download` runs the **tag's** build script from the worktree, `(cd "$SRC" && bash release/build-archive.sh v0.0.1 linux <arch> "$WORK/linux-<arch>")` for `amd64` and `arm64` and moves only the tarball into `$DIST` (the script also leaves the bare binary in its outdir, which must not land in `$DIST`), then `(cd "$SRC" && bash release/build-archive.sh v0.0.1 windows <arch> "$WORK/win-<arch>")` and zips `agentbus2.exe` plus `$SRC/release/windows-zip-README.txt` as `README.txt` into `$DIST/agentbus2-v0.0.1-windows-<arch>.zip` with `python3 -I -m zipfile -c`; the stub never redirects the script's output, so a `version mismatch` from `build-archive.sh` reaches the run log (Task 7 mutation 4); `attestation verify <file> ...` exits 0; `pr list` prints nothing; anything unlisted prints `gh stub: unexpected call: $*` to stderr and exits 99 (`api .../generate-notes` is never reached because the notes file exists). `codesign` (records; `--verify` exits 0), `xcrun` (`notarytool submit` prints `status: Accepted`), `komac` (appends its argv to `$KOMAC_LOG`), `lipo` only when `command -v lipo` finds none (copies the arm64 input to the output and prints a fake `-info` line); `release.sh` falls back from `ditto` to `zip -j` itself (choice 8), so `ditto` needs no stub; `git` is **not** stubbed (the tap and bucket pushes go to the local bares; `check_tag_pushed` queries `origin.git`). Overrides, assigned after both `source`s because `release.sh` computes them at source time (`$OVERRIDES`, one string built once): `REPO_ROOT=$WORK/repo`, `PUBKEY=$WORK/repo/release/agentbus2-release-ed25519.pub`, `DIST=$WORK/dist` (Run D names its own), `SIGNING_KEY=$WORK/key.pem`, `KOMAC_KEY_FILE=$WORK/komac.key` (a file holding `export KOMAC_GITHUB_KEY='x'`), `TAP_DIR=$WORK/tap`, `SCOOP_DIR=$WORK/scoop`, `FIND_RUN_TRIES=1`, `FIND_RUN_SLEEP=0`; exported `TMPDIR=$WORK` (so `checkout_tag`'s `mktemp -d` worktree lands inside the temp dir) and `GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t` (so `update_tap`'s and `update_scoop`'s commits succeed on a runner with no git identity, as v1's `rerun_push_case` does); `HOME` untouched (nothing reads it after the overrides). `checkout_tag` adds a worktree of the fixture repository at the tag and builds there.
3. One process per run. `release.sh` keeps state in shell variables (`ARCHIVES`, appended by `build_macos` and `download_archives`; `MACOS_ARCHIVE`, `PUBLISHED`, `RUN_ID`, `SRC`), `checkout_tag` installs an `EXIT` trap that removes the worktree, and `fail` calls `exit`, so a second `main` in one shell would write a twelve-line `SHA256SUMS` and a negative run would exit the harness. Every run is therefore

   ```bash
   bash -c "source '$WORK/repo/release/release.sh'; source '$WORK/stubs.sh'; $OVERRIDES; main $ARGS" > "$WORK/run-<X>.log" 2>&1
   ```

   (v1's `with_stub` pattern; combined output, so `version mismatch` and every `==>` line are grep-able), after which the harness reads the exit status and asserts over the log, `$DIST`, the bares and the state files. Only `$WORK` carries between runs: the origin, tap and bucket bares, `$WORK/state`, `$WORK/runs.json`, the logs and `$DIST`; nothing else does, and no stub is defined in the harness's own shell.
4. Run A: `main v0.0.1 --windows-signing=off --no-publish`. Assert: exit 0; the top-level files of `$DIST` (`find "$DIST" -maxdepth 1 -type f -exec basename {} \; | sort`; subdirectories such as `macos/` and `macos-<arch>/` are allowed) are **exactly** `agentbus2-v0.0.1-macos-universal.tar.gz`, `agentbus2-v0.0.1-linux-amd64.tar.gz`, `agentbus2-v0.0.1-linux-arm64.tar.gz`, `agentbus2-v0.0.1-windows-amd64.zip`, `agentbus2-v0.0.1-windows-arm64.zip`, `install.sh`, `install.ps1`, `SHA256SUMS`, `SHA256SUMS.sig`, `notes.md`, `notary.log`, `agentbus2.rb`, `agentbus2.scoop.json` (no stray binary, no `notarize.zip`), and `$DIST/winget/` holds exactly `ericfitz.agentbus2.yaml`, `ericfitz.agentbus2.installer.yaml`, `ericfitz.agentbus2.locale.en-US.yaml`; `SHA256SUMS` has seven lines in choice 1's order (`awk '{print $2}'` equals the expected list) and `SHA256SUMS.sig` verifies with `key.pub`; `install.sh` in `$DIST` holds the throwaway key, not the placeholder; the host-native archive (macOS: the universal tarball; Linux: `linux-$(go env GOHOSTARCH)`) extracts to a binary printing `0.0.1` (Review Focus 5); `$DIST/agentbus2.rb` passes `ruby -c`, has `class Agentbus2` and three `sha256` values equal to `sum_of` of the macOS, linux-amd64 and linux-arm64 archives; `$DIST/agentbus2.scoop.json` parses with `python3 -I -m json.tool` and has `version == "0.0.1"`, `bin == "agentbus2.exe"` and the two hashes of `SHA256SUMS`; each winget file holds `PackageIdentifier: ericfitz.agentbus2` and `PackageVersion: 0.0.1`, the installer file holds `InstallerType: zip`, `NestedInstallerType: portable`, `RelativeFilePath: agentbus2.exe`, the two `InstallerUrl` values `https://github.com/ericfitz/agentbus2/releases/download/v0.0.1/agentbus2-v0.0.1-windows-{amd64,arm64}.zip`, and two `InstallerSha256` values equal (case-insensitively) to the windows-amd64 and windows-arm64 lines of `SHA256SUMS`, and no file holds `__`; the log holds five `rendered:` lines and `stopping before publish; draft:`; `$GH_LOG` holds `release create v0.0.1 ... --verify-tag ... --draft`, `workflow run release-build.yml --repo ericfitz/agentbus2 --ref v0.0.1 -f tag=v0.0.1 -f windows_signing=off`, four `attestation verify ... --signer-workflow ericfitz/agentbus2/.github/workflows/release-build.yml --source-ref refs/tags/v0.0.1` lines, one `release upload v0.0.1 ... --clobber` naming the macOS tarball, both installers, `SHA256SUMS` and `SHA256SUMS.sig`, and **no** `release edit v0.0.1 --repo ericfitz/agentbus2 --draft=false`; the tap and bucket bares have no commit beyond `seed`; `$KOMAC_LOG` is absent or empty; `$DIST/notes.md` ends with the unsigned-Windows line. **Install check** (the real pipeline output installed by the real installer, which no other automated step proves: `test-install.sh` verifies against its own hand-built `SHA256SUMS`): on both hosts lay out `$WORK/serve/valid/latest` holding `v0.0.1` and `$WORK/serve/valid/v0.0.1/` holding copies of `$DIST`'s top-level files, start `python3 -I "$WORK/repo/release/testdata/fixture-server.py" "$WORK/serve" <port>` on a free port (bind port 0 from `python3 -c` to pick it, retry on a race as `test-install.sh` does; its pid goes to `$WORK/server.pid` and the `EXIT` trap kills that pid, never a `pkill` pattern, so an overlapping run's server survives). On Linux run `HOME=$WORK/home AGENTBUS2_BASE_URL=http://127.0.0.1:<port>/valid AGENTBUS2_INSTALL_DIR=$WORK/home/bin sh "$DIST/install.sh"` and assert exit 0, `installed agentbus2 0.0.1` in its output, and that `"$WORK/home/bin/agentbus2" version` prints `0.0.1`. On Darwin `install.sh` refuses the host (Review Focus 3), so the same install runs in a Linux container against the host's server, the host-gateway trick `test-install.sh` uses (the host architecture's linux archive is in `$DIST` on both hosts): `docker build -q -t agentbus2-itest-debian-<host arch> -f "$WORK/repo/release/testdata/Dockerfile.debian" "$WORK/repo/release/testdata"` (Task 1's image, `debian:12` plus `curl ca-certificates openssl`; the layer cache makes reruns instant), then `docker run --rm --add-host host.docker.internal:host-gateway -v "$DIST:/dist:ro" agentbus2-itest-debian-<host arch> sh -c 'AGENTBUS2_BASE_URL=http://host.docker.internal:<port>/valid sh /dist/install.sh && ~/.local/bin/agentbus2 version'`, with the same assertions on its output (the container reaches the server because `fixture-server.py` binds `0.0.0.0`, v1 line 76). Either way the case is `PASS install-check` or `FAIL install-check`; there is no skip.
5. Run B (same `$WORK`, a new process): `main v0.0.1 --windows-signing=off`. Assert: exit 0; the log holds `Reusing draft release v0.0.1`; `$GH_LOG` now holds `--draft=false`; `$WORK/tap-origin.git` has a commit `agentbus2 0.0.1` whose `Formula/agentbus2.rb` is byte-identical to `$DIST/agentbus2.rb`; `$WORK/scoop-origin.git` has `bucket/agentbus2.json` byte-identical to `$DIST/agentbus2.scoop.json`; `$KOMAC_LOG` is exactly `komac update ericfitz.agentbus2 --version 0.0.1 --urls https://github.com/ericfitz/agentbus2/releases/download/v0.0.1/agentbus2-v0.0.1-windows-amd64.zip https://github.com/ericfitz/agentbus2/releases/download/v0.0.1/agentbus2-v0.0.1-windows-arm64.zip --submit`; the log's last line names `brew install ericfitz/tap/agentbus2`.
6. Run C (negative, preflight only, no build): a second fixture repository `$WORK/repo2` with its own bare, built the same way but with its `install.sh` key block rewritten to the placeholder explicitly (v1 `build_fixture`'s `install-nokey.sh` awk, so the case holds whether or not Task 9 has embedded the real key on `main`), committed, tagged `v0.0.2` and pushed → `main v0.0.2 --windows-signing=off --no-publish` with `REPO_ROOT=$WORK/repo2` and `DIST=$WORK/dist-c` exits 1, the log holds `install.sh has no embedded release key`, and `$WORK/dist-c` is absent. Run D (negative, before any build): against the first fixture, in its own process with `SIGNING_KEY=$WORK/other.pem` (the second throwaway key of item 1) and `DIST=$WORK/dist-d` assigned after the `source`s; setting `AGENTBUS2_SIGNING_KEY` in the environment would do nothing, because `release.sh` reads it only at source time → exit 1, the log holds `does not match`, `$WORK/dist-d` is absent and Run A's `$DIST` is untouched. Order: A, B, C, D.
7. `--self-test`: it builds the item 1 fixture, adds a worktree of it at `v0.0.1`, sets `SRC` to that worktree (the `release download` stub reads `$SRC`), and runs each stub probe below in its own `bash -c "source '$WORK/stubs.sh'; SRC=...; <probe>"` (item 3), never in the harness's shell. Cases: `copy-untracked` (a scratch tree, made with `git init` in the temp dir, holding a tracked `main.go` that calls a function defined only in an untracked `extra.go`, plus a tracked file deleted from disk: item 1's copy succeeds, skips the deleted path, and `go build ./...` in the copy succeeds), the `gh` stub's state machine (`release view` prints `release not found` on stderr with exit 1 before `release create`, `true` on stdout with exit 0 after it, `false` after `--draft=false`), the `release download` stub producing exactly four top-level files in an empty `$DIST` for a tag (no stray binary), `python3 -I -m zipfile -l` listing `agentbus2.exe` and `README.txt`, the serve layout plus `fixture-server.py` answering 302 for `/valid/releases/latest` and serving `SHA256SUMS` (on both hosts; the self-test never runs the install itself, which Run A does on both hosts), and `unknown-arg` (`bash "$0" --bogus` exits 2 with `usage: test-pipeline.sh [--self-test]`); prints `test-pipeline self-test: OK`.

State (Definition of Done D): a second run of the harness starts from a fresh `$WORK`; two overlapping runs do not share anything (each fixture repository, bare remote, serve directory and server port is under its own temp dir or chosen free; `$ROOT` is only read); a killed run leaves a temp dir, possibly a `git worktree` registered in the fixture repository (both inside that temp dir, `TMPDIR=$WORK`) and possibly a `fixture-server.py` process bound to a random port, whose pid is in `$WORK/server.pid` (`kill "$(cat <that file>)"` clears it; never a `pkill -f fixture-server.py`, which would kill an overlapping run's server); nothing in the repository needs cleaning.

- [ ] **Step 1: Write the self-test and the assertions of runs A to D** (they are the test)

- [ ] **Step 2: Run to see it fail**

Run: `bash release/test-pipeline.sh --self-test`
Expected: fails at the first undefined stub.

- [ ] **Step 3: Write the fixture builder, the stubs and the runs**

- [ ] **Step 4: Run it on macOS, then once inside a Linux container** (the CI path)

Run: `bash release/test-pipeline.sh --self-test && bash release/test-pipeline.sh` then `docker run --rm -v "$PWD:/src:ro" -w /src golang:1.27 bash -c 'apt-get update -qq && apt-get install -y -qq jq ruby zip python3 perl openssl curl >/dev/null && command -v shasum && git config --global safe.directory /src && bash release/test-pipeline.sh'` (the container needs network for `apt-get`, the Go module cache and possibly the `go.mod` toolchain; `perl` provides `shasum`; the harness only reads `/src`; everything it writes is under its temp dir)
Expected: `test-pipeline: N passed, 0 failed` on both, each with `PASS install-check` (on macOS through the `debian:12` container of item 4); on Linux the `lipo`/`ditto` stubs are used and the version check runs on the linux archive. Record both last lines in the commit message body.

- [ ] **Step 5: Commit**

```bash
git add release/test-pipeline.sh
git commit -m "test(release): the local unsigned dry run: release.sh end to end against stubbed gh, codesign, notarytool and komac, local tap and bucket remotes, the shared build script and a throwaway key"
```

---

### Task 6: Makefile targets, `verify`, the CI jobs

**Depends on:** 5

**Files:**
- Modify: `Makefile` (new targets `vet-cross`, `release-check`, `release-lint`, `release-install-check`; `verify` gains `release-check` after `check-gates`; `.PHONY`), `.github/workflows/ci.yml` (new jobs `release-lint`, `release-install-check`), `CLAUDE.md` (the `Done gate:` line only: its enumeration gains `release-check`, in this commit because Definition of Done A reads that line; Task 8 owns the Commands and Design lines)
- Test: `make release-check`, `make verify`
- Note: `/dist/` is already in `.gitignore`; nothing to add there.

**Interfaces:**
- `vet-cross`: `GOOS=linux GOARCH=amd64 go vet ./...`, `GOOS=linux GOARCH=arm64 go vet ./...`, `GOOS=windows GOARCH=amd64 go vet ./...`, `GOOS=windows GOARCH=arm64 go vet ./...`, `GOOS=windows golangci-lint run ./...` (v1 `Makefile` `vet-cross`). `release-check`: `bash release/check-pins.sh`, `bash release/test-install.sh --self-test`, `bash release/test-release.sh`, `bash release/test-render.sh`, `bash release/build-archive.sh --self-test`, `bash release/test-pipeline.sh`, then `$(MAKE) vet-cross`; the help comment says it is part of `verify` and names the tools (Global Constraints). `release-lint`: `actionlint .github/workflows/*.yml`, `shellcheck -x release/*.sh scripts/*.sh`, `shellcheck -s sh install.sh` (fails fast naming a missing tool). `release-install-check`: `bash release/test-install.sh $(RELEASE_INSTALL_ARGS)` (`make release-install-check RELEASE_INSTALL_ARGS=--quick` for the one-image run).
- `ci.yml`: `release-lint` (ubuntu; `shellcheck` is preinstalled; `actionlint` through `rhysd/actionlint`'s pinned action or its `download-actionlint.bash` pinned to a version, the implementer's choice, pinned by SHA either way because `check-pins.sh` now runs over the real directory in `verify`), `release-install-check` (ubuntu; `docker/setup-qemu-action` pinned, then `make release-install-check` for the full matrix). The `verify` job is unchanged: `make verify` now covers `release-check`. The `m9-exit-selftest` job is Task 7's, added with the script it runs.

- [ ] **Step 1: Make the gate fail first**

Run: `make release-check`
Expected: `make: *** No rule to make target 'release-check'`.

- [ ] **Step 2: Edit the Makefile and `ci.yml`**

- [ ] **Step 3: Run the gate**

Run: `make release-check` then `make verify` (600000 ms)
Expected: every harness's last line reports 0 failed, `check-pins: OK`, `build-archive self-test: OK`, `vet-cross` silent, then `verify: OK`. `actionlint .github/workflows/ci.yml` clean.

- [ ] **Step 4: Commit**

```bash
git add Makefile .github/workflows/ci.yml CLAUDE.md
git commit -m "build: release-check under verify (pins, installer and release harnesses, the dry run, vet-cross), release-lint and release-install-check targets, and the CI jobs; vet-cross ported from v1 Makefile"
```

---

### Task 7: Milestone 9 exit checks

**Depends on:** 6

**Files:**
- Create: `scripts/m9-exit-checks.sh`
- Modify: `.github/workflows/ci.yml` (job `m9-exit-selftest`: ubuntu; `actions/checkout` and `actions/setup-go` with `go-version-file: go.mod`, both pinned by full SHA with a version comment as the other jobs are, because mutation 4 and `pipeline-selftest` build Go; then `bash scripts/m9-exit-checks.sh`, which also runs `test-pipeline.sh --self-test` as its case `pipeline-selftest`; its own job because the mutations take minutes, as `gates-selftest` is; after Task 6's edit of the same file, which is why this task depends on 6)
- Test: itself (it is the harness's self-test, Definition of Done C), run by the `m9-exit-selftest` CI job

**Interfaces:**
- `scripts/m9-exit-checks.sh [--real-only]`: `set -euo pipefail`; prints `ok  <name>` / `FAIL <name>: <why>` lines (the `check-gates.sh` contract) and exits 1 on any `FAIL`; any other argument exits 2 with `usage: m9-exit-checks.sh [--real-only]`. Part 1, **real repo**: `pins-real-repo` (`release/check-pins.sh` over `.github/workflows` prints `check-pins: OK`), the four cases moved out of Task 3 that read the repository's own installers (v1 `test-release.sh` lines 101 to 128): `real-unembedded` and `ps1-unembedded` (`release/release.sh` sourced, `check_script_embedded` over a copy of `install.sh` or `install.ps1` whose key block the v1 awk reset to the placeholder → exit 1, `install.sh has no embedded release key` or `install.ps1 has no embedded release key`), `real-embedded` and `ps1-embedded` (the same copies after `release/embed-key.sh <throwaway.pub>` → exit 0; the throwaway pair comes from `release_openssl` of the sourced `release.sh`, called inside `$( )`); `installers-key-block` (`install.sh` and `install.ps1` each hold the markers once, in order, with the placeholder or the committed `.pub`'s PEM; the placeholder passes; lint is the `release-lint` job, not a check here), `shared-helpers-identical` (`awk` extracts the lines between `# BEGIN shared release helpers` and `# END shared release helpers` from `release/release.sh`, `release/test-install.sh` and `release/test-release.sh`; each file holds exactly one block and the three extracts `cmp` equal), `templates-render` (`release/test-render.sh` exits 0), `pipeline-selftest` (`bash release/test-pipeline.sh --self-test` exits 0 and prints `test-pipeline self-test: OK`; this wires the dry run's harness self-test into CI), `workflow-names-agree` (`grep -F` of each literal anchor Task 4 lists: in `release-build.yml` `run-name: release-build ${{ inputs.tag }}`, `pattern: archive-*`, `name: archive-windows-amd64`, `name: archive-windows-arm64`, `name: archive-${{ matrix.goos }}-${{ matrix.goarch }}`, `-windows-amd64.zip"`, `-windows-arm64.zip"` and the line `for a in linux-amd64.tar.gz linux-arm64.tar.gz windows-amd64.zip windows-arm64.zip; do`; in `release/build-archive.sh` `-linux-${goarch}.tar.gz`; in `release/release.sh`'s `download_archives` the same `for a in ...` line; a self-check first runs the same assertions over a temp copy of `release-build.yml` with `windows-arm64.zip` changed to `windows-arm46.zip` and fails with `anchor check misses a planted wrong name` unless that copy fails), `no-v1-names` (`rg -n 'AGENTBUS_[A-Z]|agentbus-v|ericfitz/agentbus([^2]|$)|Programs\\agentbus([^2]|$)|/\.agentbus\.tmp' install.sh install.ps1 release/*.sh release/*.tmpl release/windows-zip-README.txt .github/workflows/release-build.yml .github/workflows/windows.yml .github/workflows/publish-image.yml` finds nothing (`release/*.tmpl` covers the winget templates); the one allowed mention is the "successor to agentbus" README line, which is not in that file list; a self-check first runs the same pattern over a temp file holding the lines `url: https://github.com/ericfitz/agentbus` and `"$env:LOCALAPPDATA\Programs\agentbus"` and fails with `regex misses the planted v1 line` unless both hit), `version-var-exists` (`release/build-archive.sh`'s `-X` path is `github.com/ericfitz/agentbus2/internal/version.Version`, and so is `Dockerfile`'s when that file exists (Milestone 7); `go list -f '{{.ImportPath}}' ./internal/version` prints that package and `rg -n '^var Version' internal/version/version.go` hits), `usage-rejects-unknown` (`bash "$0" --bogus` in a subprocess exits 2 and prints `usage:`). Part 2, **mutations** in a throwaway worktree of HEAD (`git worktree prune` first, then `git worktree add` under `mktemp -d`, removed on exit; `--real-only` skips this part): (1) `release-build.yml`: replace one pinned `uses:` SHA with `@v4` → `release/check-pins.sh` exits 1 naming the line; (2) `release/agentbus2.rb.tmpl`: add a line `extra "__UNFILLED__"` → `release/test-render.sh` exits 1 with `unfilled placeholders`; (3) `install.sh`: delete the `# END agentbus2 release public key` line → `release/test-install.sh --self-test` exits 1 **and** prints `FAIL key-block` (Task 1 runs `key-block` before `build_fixture`, so the line prints before `embed-key.sh` could die on the missing marker); (4) `release/build-archive.sh`: change the `-X` path to `github.com/ericfitz/agentbus2/internal/version.Nope` → `release/test-pipeline.sh` exits 1 and its output holds `version mismatch`: the binary prints `2.0.0-dev`, and the check fails inside `build-archive.sh` on both hosts (macOS: `build_macos`'s darwin build of the host architecture; Linux: the `release download` stub's linux build of the host architecture), which the harness sees because it captures `main`'s combined output and the stub does not redirect the script's output (Review Focus 5; this mutation builds Go, so it is the slow one and the CI job needs Go). The mutations are applied in the worktree and `test-pipeline.sh` is run from that worktree, which is why Task 5 tests tracked working-tree files rather than HEAD. Each block first asserts the mutation changed the file (`mutation changed nothing` otherwise), runs the check, and asserts the exact expected exit status and failure text; every block runs even after a failure.
- State (Definition of Done D, the sentence of `scripts/check-gates-selftest.sh` lines 10 to 12 applied here): a second run starts clean, since the worktree and the temp dir are removed on exit; overlapping runs use separate temp dirs and separate worktree paths, so they do not interfere; a killed run leaves a worktree registered in the real repository and a temp dir under `$TMPDIR`: `git worktree prune` clears the registration, and the script runs it before adding its own worktree.

- [ ] **Step 1: Write the script and add the CI job**

- [ ] **Step 2: Run the real-repo part, then the whole script**

Run: `bash scripts/m9-exit-checks.sh --real-only && bash scripts/m9-exit-checks.sh && actionlint .github/workflows/ci.yml`
Expected: every line `ok`; the last line `m9-exit-checks: OK`; actionlint silent. Record the output in the commit body.

- [ ] **Step 3: Commit**

```bash
git add scripts/m9-exit-checks.sh .github/workflows/ci.yml
git commit -m "test(release): Milestone 9 exit checks: the real-repo invariants and four mutations proving check-pins, the template render, the installer key-block check and the dry run's version check fire; the m9-exit-selftest CI job"
```

---

### Task 8: Install and release docs, release notes, README and CLAUDE.md

**Depends on:** 6, 7 (it documents Task 7's `scripts/m9-exit-checks.sh` and `m9-exit-selftest` job, and Step 2 asserts every documented path exists, so it runs after Task 7 is integrated)

**Files:**
- Create: `docs/install.md`, `docs/release.md`, `docs/release-signing.md`, `release/notes-v2.0.0.md`
- Modify: `README.md` (an `## Install` section after the intro, before `## Development`; the `Status:` line gains "release tooling in place; 2.0.0 is cut by `docs/release.md`"), `CLAUDE.md` (Commands and Design lines only; the `Done gate:` line is Task 6's. Commands: `make release-check` is part of `verify`; `make release-lint`, `make release-install-check` are not; `release/release.sh <tag> --windows-signing=on|off [--no-publish]` is Eric's; `scripts/m9-exit-checks.sh`. Design: one line that `release/` is the release tooling, ported from v1 `release/`, and that `release/build-archive.sh` is the one build command)

**Port from v1:** `docs/install.md`, `docs/release-signing.md`, the "Key setup", "State" and "Release flow" sections of `docs/superpowers/specs/2026-10-06-{linux-releases,actions-release-builds}-design.md` (into `docs/release.md`; v1 had no runbook file), `docs/windows-checklist.md` (its Packaging lines only).

**Interfaces:**
- `docs/install.md`: macOS `brew install ericfitz/tap/agentbus2`; Linux `curl -fsSL https://github.com/ericfitz/agentbus2/releases/latest/download/install.sh | sh` with the `AGENTBUS2_*` table (choice 4), the refusal list, verifying by hand with `release/agentbus2-release-ed25519.pub` and `gh attestation verify ... --repo ericfitz/agentbus2 --signer-workflow ericfitz/agentbus2/.github/workflows/release-build.yml`; Homebrew on Linux; Windows `irm https://github.com/ericfitz/agentbus2/releases/latest/download/install.ps1 | iex`, Scoop (`scoop bucket add ericfitz https://github.com/ericfitz/scoop-bucket`, `scoop install agentbus2`), `winget install ericfitz.agentbus2` ("once the winget package is published"), the OpenSSL 3 / Git for Windows requirement, stopping sessions before Scoop or winget upgrades, the Authenticode check; the v2 paths of spec 6.1 (`~/.config/agentbus2/config.json`, `~/.local/share/agentbus2/`, the Windows equivalents the Milestone 6 plan fixes; cite the plan where the code is not on `main` yet); side by side with v1: nothing here touches `agentbus` (Decision 32). Build from source: `make build`.
- `docs/release.md` (the runbook): what a release is (choice 1's asset list, who builds what); one-time setup, which is Task 9's checklist verbatim (key pair, embedding and committing the `.pub`, `notarytool` profile `sqdist-notary`, the `release` environment with the `v*` tag rule, the Azure federated credential subject `repo:ericfitz/agentbus2:environment:release` and the seven repository variables, the first winget submission `komac submit dist/winget` after the first published release, `~/.keys/KOMAC_GITHUB_KEY`, and the image publishing switch `vars.IMAGE_REPOSITORY` of proposal 2); the per-release flow (`release/notes-vX.Y.Z.md`, `git tag vX.Y.Z && git push origin vX.Y.Z`, `release/release.sh vX.Y.Z --windows-signing=on|off --no-publish`, inspect the draft and the rendered `dist/agentbus2.rb`, `dist/agentbus2.scoop.json` and `dist/winget/`, the draft-recovery rule of Task 10 Step 2, rerun without `--no-publish`, `release/check-linux-brew.sh`, the Windows VM checks of `release/test-windows.ps1`, and the image check when `publish-image.yml` ran); state (v1 Actions design "State": rerun reuses the draft and clobbers, rerun on a published release warns and replaces public assets, overlapping dispatches queue on the workflow's concurrency group, `dist/` is recreated per run, a stranded tap or bucket commit is pushed by the rerun, a komac failure leaves the release standing and prints the rerun command; and a killed run leaves a `git worktree` registration under `$TMPDIR`, pruned by the next run's `checkout_tag`, and a partial `dist/`, recreated by the next run); what the automated checks cover (`make release-check`, the CI jobs `release-lint`, `release-install-check`, `windows` and `m9-exit-selftest`) and what only the tagged run proves (signing, notarization, attestations, the real tap and bucket pushes, the winget submission, the image push).
- `docs/release-signing.md`: v1's text with `ericfitz/agentbus2` in the federated subject, the variables table, pausing and resuming, the per-release flag.
- `release/notes-v2.0.0.md`: the 2.0.0 notes as Eric will publish them: what agentbus2 is (one paragraph from the README), that it installs beside v1 and shares nothing with it (ADR 0005), the install lines, the one known limitation list the operations docs state (the SQLite two-server limitation and the per-replica rate-limit bound, from the Milestone 7 plan; cite `docs/ops/` once it exists on `main`). `release.sh` appends the unsigned-Windows line itself when `off`; the notes file never holds it.
- Every command, path, variable and message quoted in these files is checked against the tree (Definition of Done E) with `rg` before the commit, and the commit body lists the checks run.

- [ ] **Step 1: Write the four files and the two edits**

- [ ] **Step 2: Check every claim**

Run: a script in the scratchpad that, for each code span in the four docs that names a file path under the repository, asserts the path exists, and for each `AGENTBUS2_*` variable and each quoted installer message, greps it in `install.sh` or `install.ps1`; then `rg -n 'agentbus[^2]' docs/install.md docs/release.md docs/release-signing.md release/notes-v2.0.0.md` (expected: only the lines that deliberately contrast with v1).
Expected: no missing path, no unmatched message or variable.

- [ ] **Step 3: Commit**

```bash
git add docs/install.md docs/release.md docs/release-signing.md release/notes-v2.0.0.md README.md CLAUDE.md
git commit -m "docs(release): install and release runbooks, Authenticode notes and the 2.0.0 release notes, ported from v1 docs/install.md and docs/release-signing.md; README install section and CLAUDE.md commands"
```

---

### Task 9: One-time signing and publishing setup — **needs Eric**

**Depends on:** 8

**Automated run:** skip. Every step uses Apple, Azure or GitHub credentials or a private key.

**Files:**
- Create: `release/agentbus2-release-ed25519.pub`
- Modify: `install.sh`, `install.ps1` (the key block, through `release/embed-key.sh`)

- [ ] **Step 1: Decide proposals 1 to 5** and record the answers in `docs/superpowers/specs/2026-10-21-gate-0-decisions.md` under a Milestone 9 heading, and retitle that file from "Milestones 1 to 7" to "Milestones 1 to 9" (the Gate 0 file is the record for these answers; the spec's section 12.1 gets a one-line pointer).

- [ ] **Step 2: Release key pair** (proposal 1). New pair:

```bash
umask 077
"$(brew --prefix openssl@3)/bin/openssl" genpkey -algorithm ed25519 -out ~/.keys/agentbus2-release-ed25519.pem
"$(brew --prefix openssl@3)/bin/openssl" pkey -in ~/.keys/agentbus2-release-ed25519.pem -pubout -out release/agentbus2-release-ed25519.pub
release/embed-key.sh release/agentbus2-release-ed25519.pub install.sh install.ps1
```

(or `cp ~/.keys/agentbus-release-ed25519.pem ~/.keys/agentbus2-release-ed25519.pem` first, to reuse v1's). No agent reads the `.pem` (global `CLAUDE.md`); `release.sh` passes its path to `openssl` only.

- [ ] **Step 3: Verify and commit the public half**

Run: `bash release/test-install.sh --self-test && pwsh -NoProfile -File release/test-install.ps1 -SelfTest && bash scripts/m9-exit-checks.sh --real-only`
Expected: `key-block` passes against the committed `.pub` (not the placeholder); every line `ok`.

```bash
git add release/agentbus2-release-ed25519.pub install.sh install.ps1
git commit -m "chore(release): the agentbus2 release public key, embedded in install.sh and install.ps1"
```

- [ ] **Step 4: GitHub** — in `ericfitz/agentbus2`: create the `release` environment with the deployment tag rule `v*`; confirm `gh workflow view release-build.yml --repo ericfitz/agentbus2` sees the workflow on `main` (push `main` first; `release.sh`'s `check_gh` requires it); a fine-grained PAT with `public_repo` at `~/.keys/KOMAC_GITHUB_KEY` as `export KOMAC_GITHUB_KEY='<value>'` (v1's file serves if it exists).

- [ ] **Step 5: Azure** (only for `--windows-signing=on`): per `docs/release-signing.md`, add a federated credential with subject `repo:ericfitz/agentbus2:environment:release` to the existing Entra application (or a new one), the Signer role on the certificate profile, and the seven repository variables; `gh variable list --repo ericfitz/agentbus2` lists all seven. Skip for a 2.0.0 with `off` (proposal 3).

- [ ] **Step 6: Apple**: `xcrun notarytool history --keychain-profile sqdist-notary` answers (the profile is team-level and shared with v1); `security find-identity -v -p codesigning` lists `Developer ID Application: Robert Fitzgerald (796T45968D)`.

- [ ] **Step 7: Image publishing** (proposal 2, as answered in Step 1). To ship the image with 2.0.0: `gh variable set IMAGE_REPOSITORY --repo ericfitz/agentbus2 --body ghcr.io/ericfitz/agentbus2` (or the registry chosen), and confirm the repository's Actions settings let `GITHUB_TOKEN` hold `packages: write` (the job asks for it). To ship it later: leave the variable unset; `publish-image.yml` then skips its job on every release. The package's visibility is set once after its first push (Task 10 Step 4).

---

### Task 10: The tagged dry run, then 2.0.0 — **needs Eric**

**Depends on:** 7, 9 (and Milestones 5 to 8 on `main`, with `make verify` green, since 2.0.0 is the release of the whole product)

**Automated run:** skip. Pushes a tag, signs, notarizes, publishes.

- [ ] **Step 1: Pre-release checks on `main`**

Run: `make verify && make release-lint && make release-install-check && bash scripts/m9-exit-checks.sh`, and in the Windows VM `powershell -ExecutionPolicy Bypass -File release\test-windows.ps1`.
Expected: all green; the VM table all `PASS` (`SKIP (pwsh not installed)` allowed).

- [ ] **Step 2: Tag and dry-run** (spec 10's "tagged dry-run release"; v1 decision 3 of the Actions design: the dry run uses the real next tag with `--no-publish`, there are no release candidates)

```bash
git tag v2.0.0 && git push origin v2.0.0
release/release.sh v2.0.0 --windows-signing=off --no-publish   # or =on after Task 9 step 5
```

Expected: the macOS build is codesigned and `notarytool` answers `status: Accepted`; the run URL is printed and the workflow succeeds; four attestations verify; the draft at `https://github.com/ericfitz/agentbus2/releases/tag/v2.0.0` holds the nine assets of choice 1; `openssl pkeyutl -verify -rawin -pubin -inkey release/agentbus2-release-ed25519.pub -in dist/SHA256SUMS -sigfile dist/SHA256SUMS.sig` passes; the five `rendered:` lines name `dist/agentbus2.rb`, `dist/agentbus2.scoop.json` and the three files under `dist/winget/`, and Eric inspects them: `ruby -c dist/agentbus2.rb`, its three `sha256` values equal `shasum -a 256` of the three Unix archives in `dist/`, the Scoop manifest's `version` is `2.0.0`, its `bin` is `agentbus2.exe` and its two hashes match `dist/SHA256SUMS`; the winget files have `PackageVersion: 2.0.0`, `InstallerType: zip`, `NestedInstallerType: portable`, `RelativeFilePath: agentbus2.exe` with `PortableCommandAlias: agentbus2`, and two `InstallerSha256` values equal (case-insensitively) to the windows lines of `dist/SHA256SUMS`; optionally `winget validate --manifest dist\winget` in the Windows VM (this is the exit criterion's "formula, Scoop and winget manifests" produced by the tagged dry run). A draft's assets are not downloadable anonymously, so the installer and package-manager checks are Step 4. **If the dry run fails after the tag is public:** while the release is still a draft, fix on `main`, delete the draft (`gh release delete v2.0.0 --repo ericfitz/agentbus2 --yes`), move the tag (`git tag -f v2.0.0 && git push -f origin v2.0.0`) and rerun this step; once Step 3 has published, never move the tag: cut the next patch version instead.

- [ ] **Step 3: Publish**

```bash
release/release.sh v2.0.0 --windows-signing=off
```

Expected: `Reusing draft release v2.0.0`, publish, `Formula/agentbus2.rb` pushed to the tap, `bucket/agentbus2.json` to the bucket; `komac update` fails because `ericfitz.agentbus2` is not yet in winget-pkgs and `release.sh` prints the rerun command (a komac failure does not fail the release; expected for 2.0.0 only). Then the first winget submission, from the files the run rendered (the URLs are public now and the hashes are the published ones): `komac submit dist/winget --dry-run`, review, then `komac submit dist/winget --yes`; record the pull request URL. Later releases go through `release.sh`'s `komac update`. If Task 9 Step 7 set `IMAGE_REPOSITORY`, `gh run list --repo ericfitz/agentbus2 --workflow publish-image.yml` shows a successful run for `v2.0.0`.

- [ ] **Step 4: Exit evidence** (spec 10: "working `install.sh` and `install.ps1`", the formula, the manifests)

Run: `release/check-linux-brew.sh` (both architectures); `docker run --rm debian:12 sh -c 'apt-get update -qq && apt-get install -y -qq curl ca-certificates openssl >/dev/null && curl -fsSL https://github.com/ericfitz/agentbus2/releases/latest/download/install.sh | sh && ~/.local/bin/agentbus2 version'`; `brew install ericfitz/tap/agentbus2 && brew test agentbus2 && agentbus2 version` on the Mac; in the Windows VM `irm https://github.com/ericfitz/agentbus2/releases/latest/download/install.ps1 | iex` then `agentbus2 version` in a new terminal, and `scoop bucket add ericfitz https://github.com/ericfitz/scoop-bucket && scoop install agentbus2`; `winget install ericfitz.agentbus2` once the winget pull request has merged (not required to close the milestone: the manifests are the exit criterion, the merge is winget-pkgs' review); when the image was published, `docker run --rm --entrypoint /agentbus2 <IMAGE_REPOSITORY>:2.0.0 version`, then set the package's visibility as decided in Task 9 Step 1.
Expected: every command prints `2.0.0`; `brew test` passes on macOS and both Linux architectures. Paste the outputs into `HANDOFF.md`'s checkpoint and close the milestone.

---

## Self-review

- **Spec coverage, section 10 Milestone 9 and Decisions 61, 87:** signing and notarization (Task 3 `build_macos`; the real run is Task 10), Actions release builds with attestations (Task 4; verified in Task 10), the `agentbus2` formula (Task 3 template and `render_formula`; Task 5 Run A asserts the dry run's rendered `dist/agentbus2.rb` and Run B its push to a local tap; Task 10 Step 2 inspects the real dry run's copy, Step 3 the real tap), Scoop (Task 3 template and `render_scoop`; Task 5 asserts the rendered manifest in Run A and the bucket push in Run B; Task 10 likewise) and winget (Task 3's three templates and `render_winget`; Task 5 Run A asserts the rendered `dist/winget/` files and Run B the `komac update` argv; Task 10 Step 2 inspects the real dry run's copies and Step 3 submits them with `komac submit dist/winget`), `install.sh` (Task 1), `install.ps1` (Task 2), the tagged dry run (Task 10 step 2) and 2.0.0 (Task 10 step 3). Spec 9's "Release" row names every v1 file this plan ports; each porting commit names its source. Spec 6.1's names: choices 1 to 4. Spec 8's done gate: Task 6 puts the automated half under `verify`; Task 7 is its negative half. Nothing of spec section 3 to 5 is touched.
- **Exit criterion under `make verify`:** the positive half is `release/test-pipeline.sh` (Task 5) in `release-check` (Task 6): every step of `release.sh` from preflight to the tap, bucket and winget calls runs on the working tree with only the credentials stubbed, each run in its own process; the dry run's rendered formula, Scoop manifest and three winget files are asserted; the shipped binary's version is checked; and on both hosts (on Darwin inside a `debian:12` container) the real `install.sh` from `dist/` installs the real `dist/` served by the fixture server, so `write_sums` and the installer's parser are proven to agree before Task 10. The negative half is `scripts/m9-exit-checks.sh` (Task 7): the real-repo invariants (including the five cases moved from v1's `test-release.sh`) and four mutations, each proving one check fires with the named exit status and text. What only Eric can prove (a real signature, notarization ticket, attestation, tap and bucket push, the winget submission, the image push) is Tasks 9 and 10, whose evidence is listed.
- **Review issues resolved in this revision:** `fixture-server.ps1` in Task 2; the `preflight-*` cases kept in Task 3 with synthetic installers and the five real-repo cases named in Task 7; render split from push (`render_formula`, `render_scoop`) with the winget clause stated in Global Constraints and proposal 6; one process per `main` in Task 5 and the stubs in one file; the Linux install check in Run A; the exact `$DIST` file list; Run D's `SIGNING_KEY` and `DIST`; the `release view` stub contract; `build_macos` from `$SRC`; choice 6 narrowed and `version-var-exists` covering the `Dockerfile`; the `m9-exit-selftest` job's checkout and Go setup, `key-block` ordered first, mutation 3's exit status, the worktree-prune sentence; unknown-argument refusals and cases in `test-install.sh`, `build-archive.sh`, `test-pipeline.sh` and `m9-exit-checks.sh`; the shared helpers block and `shared-helpers-identical`; the `no-v1-names` regex and its planted-line self-check; the draft-recovery sentence in Task 10; `AGENTBUS2_TEST_OSARCH` marked `install.ps1`-only; mutation 4 described as failing inside `build-archive.sh` with combined output captured. Second revision: the winget manifests rendered from three templates by the dry run and asserted (proposal 6 withdrawn); `key_matches_pub` replaced by `key-block` in both `test-install.sh` modes; the dry run copies tracked plus untracked-not-ignored existing files, with the `copy-untracked` self-test case; Task 8 after Task 7; `publish-image.yml` written gated off in Task 4 with Eric's switch in Task 9 Step 7; the Darwin install check in a container instead of a skip; `workflow-names-agree`'s literal anchors and planted-name self-check; `test-pipeline.sh --self-test` wired in through `pipeline-selftest`; three truncation points; choice 7 aligned with Task 6, `curl` among the `verify` tools, the `Done gate:` line moved to Task 6; `worktree prune` in `checkout_tag`, the killed-run leftovers in `docs/release.md`, the server killed by pid; the `pwsh` precondition, `mktemp -d` in Task 4, the container's packages and network named, the Gate 0 file retitled.
- **Step scan:** every step names a file, a command and an expected output or a commit; bodies are the implementer's except the ldflags and the exact names tasks share (asset names, function names, marker lines, messages the tests grep).
- **Type and name consistency:** `release_openssl`, `utc_minus_60s`, `check_workflow_at_tag` (Tasks 3, 5, 7); `release/build-archive.sh <tag> <goos> <goarch> <outdir>` (Tasks 3, 4, 5, 7); the marker lines and the placeholder (Tasks 1, 2, 3, 5, 7, 9); `release-build <tag>` as the run title (Tasks 3, 4); the four `archive-*` names and the five asset names (Tasks 3, 4, 5, 7); `PASS`/`FAIL` lines in the `release/` harnesses and `ok  `/`FAIL ` lines in `scripts/m9-exit-checks.sh` (the `check-gates.sh` contract).
- **Review Focus:** five lines, each pinned (Tasks 1, 1 and 2, 1, 3, and 4 with 5 and 7).
- **Parallelism:** Wave 1 is four disjoint trees with no shared file; the "real repo" test cases were moved out of Task 3 so it does not read Tasks 1 and 2's files; `Makefile` and `README.md` each have one owner (Tasks 6 and 8); `CLAUDE.md` is edited by Task 6 (the `Done gate:` line) and then Task 8 (Commands and Design), and `ci.yml` by Task 6 and then Task 7, each pair sequential by the dependency graph; Task 8 depends on Task 7 because it documents and path-checks Task 7's script, so Waves 4 and 5 are one task each.
- **Proportion:** the v1 scripts total about 150 KB; this plan fixes names, seams, cases and assertions and leaves the bodies to the port.
