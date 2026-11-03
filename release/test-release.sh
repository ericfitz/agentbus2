#!/usr/bin/env bash
# Unit tests for the release tooling: release.sh's functions (sourced, never
# run as a whole), check-pins.sh and check-linux-brew.sh. Every external tool
# (gh, git push, codesign, xcrun, lipo, ditto, komac) is a shell-function stub
# or a fake on a temp PATH: nothing here signs, notarizes, pushes a tag,
# publishes or reads ~/.keys. No network, no Docker.
#   release/test-release.sh
# Writes every fixture itself; reads only release.sh, the templates and the two
# scripts under test.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
PASS=0; FAIL=0

# check <name> <expected-exit> <required-substring> -- <command...>
# Runs the command, compares its exit code and looks for the substring in its
# combined output. An empty substring skips the output check.
check() {
    local name="$1" want_exit="$2" want_out="$3"; shift 3
    [[ "$1" == "--" ]] && shift
    local out exit=0
    out="$("$@" 2>&1)" || exit=$?
    # shellcheck disable=SC2001 # prefixing every line is clearer with sed than ${//}
    if [[ "$exit" == "$want_exit" && ( -z "$want_out" || "$out" == *"$want_out"* ) ]]; then
        PASS=$((PASS + 1)); echo "PASS $name"
    else
        FAIL=$((FAIL + 1)); echo "FAIL $name: exit=$exit want=$want_exit; output:"; echo "$out" | sed 's/^/    /'
    fi
}

# BEGIN shared release helpers
# release_openssl prints the path of an OpenSSL 3 binary: $AGENTMESH_RELEASE_OPENSSL,
# else Homebrew openssl@3, else the first of openssl, openssl3 on PATH whose
# "version" major is 3 or more. It dies naming the places it looked.
release_openssl() {
    local cand major
    local tried=()
    if [[ -n "${AGENTMESH_RELEASE_OPENSSL:-}" ]]; then
        cand="$AGENTMESH_RELEASE_OPENSSL"
        major="$("$cand" version 2>/dev/null | sed -n 's/^OpenSSL \([0-9][0-9]*\)\..*/\1/p')"
        if [[ -n "$major" && "$major" -ge 3 ]]; then printf '%s\n' "$cand"; return 0; fi
        echo "error: AGENTMESH_RELEASE_OPENSSL=$cand is not OpenSSL 3 or newer ($("$cand" version 2>&1 | head -n1))" >&2
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
    echo "error: no OpenSSL 3 found; set AGENTMESH_RELEASE_OPENSSL; tried: ${tried[*]}" >&2
    return 1
}
# utc_minus_60s prints the UTC time one minute ago as YYYY-MM-DDTHH:MM:SSZ (BSD date, then GNU date).
utc_minus_60s() {
    date -u -v-60S +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -d '-60 seconds' +%Y-%m-%dT%H:%M:%SZ
}
# END shared release helpers
utc_minus_60s >/dev/null # the format is asserted in utc-minus-60s-format below

# --- throwaway keys and synthetic installers ----------------------------------
openssl_bin="$(release_openssl)"
"$openssl_bin" genpkey -algorithm ed25519 -out "$WORK/throwaway.pem" 2>/dev/null
"$openssl_bin" pkey -in "$WORK/throwaway.pem" -pubout -out "$WORK/throwaway.pub" 2>/dev/null
"$openssl_bin" genpkey -algorithm ed25519 -out "$WORK/other.pem" 2>/dev/null
PEM="$(cat "$WORK/throwaway.pub")"
# unembedded: the key block holds the placeholder; embedded: it holds a PEM.
printf "# BEGIN agentmesh release public key\nPUBKEY_PEM='-----BEGIN PUBLIC KEY-----\nREPLACED-BY-release/embed-key.sh\n-----END PUBLIC KEY-----'\n# END agentmesh release public key\n" > "$WORK/unembedded.sh"
printf "# BEGIN agentmesh release public key\nPUBKEY_PEM='%s'\n# END agentmesh release public key\n" "$PEM" > "$WORK/embedded.sh"
printf "# BEGIN agentmesh release public key\n\$PubKeyPem = @'\n-----BEGIN PUBLIC KEY-----\nREPLACED-BY-release/embed-key.sh\n-----END PUBLIC KEY-----\n'@\n# END agentmesh release public key\n" > "$WORK/unembedded.ps1"
printf "# BEGIN agentmesh release public key\n\$PubKeyPem = @'\n%s\n'@\n# END agentmesh release public key\n" "$PEM" > "$WORK/embedded.ps1"

# --- release.sh pure functions ----------------------------------------------
# shellcheck source=release/release.sh
source "$HERE/release.sh"   # the main guard keeps it from running

check args-none 2 "usage" -- parse_args
check args-bad-tag 2 "tag must look like vX.Y.Z" -- parse_args 1.2.3
check args-two 2 "usage" -- parse_args v1.2.3 extra
check args-ok 0 "" -- bash -c "source '$HERE/release.sh'; parse_args v1.2.3 --windows-signing=off && [[ \$VERSION == 1.2.3 ]]"
check args-prerelease 2 "tag must look like vX.Y.Z, got 'v1.2.3-rc.1'" -- parse_args v1.2.3-rc.1

git init -q "$WORK/repo" && git -C "$WORK/repo" -c user.email=t@t -c user.name=t commit -q --allow-empty -m init && git -C "$WORK/repo" tag v0.0.1
check tag-exists 0 "" -- bash -c "source '$HERE/release.sh'; REPO_ROOT='$WORK/repo'; TAG=v0.0.1; check_tag_exists"
check tag-missing 1 "tag v0.0.2 not found" -- bash -c "source '$HERE/release.sh'; REPO_ROOT='$WORK/repo'; TAG=v0.0.2; check_tag_exists"

printf 'url "__URL__"\nsha "__SHA__"\n' > "$WORK/t.tmpl"
check render-ok 0 "" -- bash -c "source '$HERE/release.sh'; render_template '$WORK/t.tmpl' '$WORK/t.out' URL=https://x/y SHA=abc && diff '$WORK/t.out' <(printf 'url \"https://x/y\"\nsha \"abc\"\n')"
check render-leftover 1 "__SHA__" -- bash -c "source '$HERE/release.sh'; render_template '$WORK/t.tmpl' '$WORK/t.out' URL=https://x/y"
check render-bad-arg 1 "expected KEY=VALUE" -- bash -c "source '$HERE/release.sh'; render_template '$WORK/t.tmpl' '$WORK/t.out' URL"

mkdir -p "$WORK/dist"
printf 'aaaa  agentmesh-v1.0.0-linux-amd64.tar.gz\nbbbb  install.sh\n' > "$WORK/dist/SHA256SUMS"
check sum-of-ok 0 "aaaa" -- bash -c "source '$HERE/release.sh'; DIST='$WORK/dist'; sum_of agentmesh-v1.0.0-linux-amd64.tar.gz"
check sum-of-exact 0 "bbbb" -- bash -c "source '$HERE/release.sh'; DIST='$WORK/dist'; [[ \$(sum_of install.sh) == bbbb ]] && echo bbbb"
check sum-of-missing 1 "no SHA256SUMS entry for nope.tar.gz" -- bash -c "source '$HERE/release.sh'; DIST='$WORK/dist'; sum_of nope.tar.gz"

# --- the shared helpers ------------------------------------------------------
mkdir -p "$WORK/fakebin" "$WORK/emptybin"
printf '#!/bin/sh\necho "OpenSSL 3.0.0 7 Sep 2023"\n' > "$WORK/fake-ossl3"
printf '#!/bin/sh\necho "OpenSSL 1.1.1w  11 Sep 2023"\n' > "$WORK/fake-ossl1"
printf '#!/bin/sh\necho "LibreSSL 3.3.6"\n' > "$WORK/fake-libressl"
chmod +x "$WORK/fake-ossl3" "$WORK/fake-ossl1" "$WORK/fake-libressl"
check openssl-resolves-path 0 "$WORK/fake-ossl3" -- bash -c "source '$HERE/release.sh'; AGENTMESH_RELEASE_OPENSSL='$WORK/fake-ossl3' release_openssl"
check openssl-rejects-v1 1 "$WORK/fake-ossl1 is not OpenSSL 3 or newer" -- bash -c "source '$HERE/release.sh'; AGENTMESH_RELEASE_OPENSSL='$WORK/fake-ossl1' release_openssl"
check openssl-rejects-libressl 1 "is not OpenSSL 3 or newer" -- bash -c "source '$HERE/release.sh'; AGENTMESH_RELEASE_OPENSSL='$WORK/fake-libressl' release_openssl"
cp "$WORK/fake-ossl3" "$WORK/fakebin/openssl"
check openssl-from-path 0 "$WORK/fakebin/openssl" -- bash -c "source '$HERE/release.sh'; unset AGENTMESH_RELEASE_OPENSSL; PATH='$WORK/fakebin:/usr/bin:/bin'; release_openssl"
check openssl-none-found 1 "no OpenSSL 3 found; set AGENTMESH_RELEASE_OPENSSL" -- bash -c "source '$HERE/release.sh'; unset AGENTMESH_RELEASE_OPENSSL; PATH='$WORK/emptybin'; release_openssl"
check utc-minus-60s-format 0 "" -- bash -c "source '$HERE/release.sh'; t=\$(utc_minus_60s); [[ \$t =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z\$ ]] || { echo \"bad format: \$t\"; exit 1; }
    now=\$(date -u +%s); then=\$(date -u -j -f %Y-%m-%dT%H:%M:%SZ \"\$t\" +%s 2>/dev/null || date -u -d \"\$t\" +%s); d=\$((now - then)); (( d >= 0 && d <= 90 )) || { echo \"not about 60 s ago: \$d\"; exit 1; }"

# --- check_signing_key, sign_sums, verify_sums (throwaway keys only) ---------
sk_run() { bash -c "source '$HERE/release.sh'; export AGENTMESH_RELEASE_OPENSSL='$openssl_bin'; OPENSSL='$openssl_bin'; PUBKEY='$WORK/throwaway.pub'; DIST='$WORK/skdist'; mkdir -p '$WORK/skdist'; $1"; }
check signing-key-ok 0 "" -- sk_run "SIGNING_KEY='$WORK/throwaway.pem'; check_signing_key"
check signing-key-mismatch 1 "does not match" -- sk_run "SIGNING_KEY='$WORK/other.pem'; check_signing_key"
printf 'sums\n' > "$WORK/skdist-msg"
check sign-and-verify-sums 0 "" -- sk_run "cp '$WORK/skdist-msg' \"\$DIST/SHA256SUMS\"; SIGNING_KEY='$WORK/throwaway.pem'; sign_sums >/dev/null && verify_sums"
check verify-sums-tampered 1 "SHA256SUMS.sig does not verify" -- sk_run "cp '$WORK/skdist-msg' \"\$DIST/SHA256SUMS\"; SIGNING_KEY='$WORK/throwaway.pem'; sign_sums >/dev/null; echo tamper >> \"\$DIST/SHA256SUMS\"; verify_sums"
check sign-sums-wrong-key 1 "SHA256SUMS.sig does not verify" -- sk_run "cp '$WORK/skdist-msg' \"\$DIST/SHA256SUMS\"; SIGNING_KEY='$WORK/other.pem'; sign_sums"

# --- embedded-key preflight (synthetic installers; embed-key.sh is not used) -
check embedded-placeholder 1 "install.sh has no embedded release key" -- bash -c "source '$HERE/release.sh'; check_script_embedded '$WORK/unembedded.sh' install.sh"
check embedded-placeholder-hint 1 "embed-key.sh" -- bash -c "source '$HERE/release.sh'; check_script_embedded '$WORK/unembedded.sh' install.sh"
check embedded-ok 0 "" -- bash -c "source '$HERE/release.sh'; check_script_embedded '$WORK/embedded.sh' install.sh"
check ps1-synthetic-unembedded 1 "install.ps1 has no embedded release key" -- bash -c "source '$HERE/release.sh'; check_script_embedded '$WORK/unembedded.ps1' install.ps1"
check ps1-synthetic-embedded 0 "" -- bash -c "source '$HERE/release.sh'; check_script_embedded '$WORK/embedded.ps1' install.ps1"
commit_file() { # <src> <dest-in-repo> <message> <tag>
    cp "$1" "$WORK/repo/$2" && git -C "$WORK/repo" add "$2" && git -C "$WORK/repo" -c user.email=t@t -c user.name=t commit -q -m "$3" && git -C "$WORK/repo" tag "$4"
}
commit_file "$WORK/unembedded.sh" install.sh inst v0.0.3
check preflight-placeholder 1 "no embedded release key" -- bash -c "source '$HERE/release.sh'; REPO_ROOT='$WORK/repo'; TAG=v0.0.3; check_installers_embedded"
commit_file "$WORK/embedded.sh" install.sh embedded v0.0.4
check preflight-no-ps1 1 "v0.0.4 has no install.ps1; the Windows installer ships with every release, so tag a commit that includes it" -- bash -c "source '$HERE/release.sh'; REPO_ROOT='$WORK/repo'; TAG=v0.0.4; check_installers_embedded"
commit_file "$WORK/unembedded.ps1" install.ps1 ps1 v0.0.90
check preflight-ps1-placeholder 1 "install.ps1 has no embedded release key" -- bash -c "source '$HERE/release.sh'; REPO_ROOT='$WORK/repo'; TAG=v0.0.90; check_installers_embedded"
commit_file "$WORK/embedded.ps1" install.ps1 ps1e v0.0.91
check preflight-embedded 0 "" -- bash -c "source '$HERE/release.sh'; REPO_ROOT='$WORK/repo'; TAG=v0.0.91; check_installers_embedded"
check preflight-no-installer 1 "v0.0.1 has no install.sh; the installer ships with every release, so tag a commit that includes it" -- bash -c "source '$HERE/release.sh'; REPO_ROOT='$WORK/repo'; TAG=v0.0.1; check_installers_embedded"

# check_tag_pushed against a local bare "origin" (no network). v0.0.91 is in the
# clone; v0.0.5 is tagged afterwards and so is only local.
git clone -q --bare "$WORK/repo" "$WORK/origin.git"
git -C "$WORK/repo" remote add origin "$WORK/origin.git"
git -C "$WORK/repo" tag v0.0.5
check tag-pushed-ok 0 "" -- bash -c "source '$HERE/release.sh'; REPO_ROOT='$WORK/repo'; TAG=v0.0.91; check_tag_pushed"
check tag-pushed-missing 1 "tag v0.0.5 is not on origin; push it first (git push origin v0.0.5)" -- bash -c "source '$HERE/release.sh'; REPO_ROOT='$WORK/repo'; TAG=v0.0.5; check_tag_pushed"
# A tag moved locally (git tag -f) but not force-pushed: the macOS build would
# use the local tag and the workflow origin's, so the release would mix commits.
git -C "$WORK/repo" tag v0.0.95 v0.0.91
git -C "$WORK/repo" push -q origin v0.0.95
git -C "$WORK/repo" tag -f v0.0.95 v0.0.90 >/dev/null
check tag-pushed-moved 1 "tag v0.0.95 differs from origin's; push the moved tag (git push -f origin v0.0.95) or reset it" -- bash -c "source '$HERE/release.sh'; REPO_ROOT='$WORK/repo'; TAG=v0.0.95; check_tag_pushed"
git -C "$WORK/repo" -c user.email=t@t -c user.name=t -c tag.gpgsign=false tag -a -m annotated v0.0.96 v0.0.91
git -C "$WORK/repo" push -q origin v0.0.96
check tag-pushed-annotated-ok 0 "" -- bash -c "source '$HERE/release.sh'; REPO_ROOT='$WORK/repo'; TAG=v0.0.96; check_tag_pushed"

# check-linux-brew.sh validates every argument before starting any container
check brew-check-bad-arch 2 "unknown architecture 'bogus' (amd64 or arm64)" -- "$HERE/check-linux-brew.sh" bogus
mkdir -p "$WORK/stubbin"
printf '#!/bin/sh\necho called >> "%s/docker-called"\nexit 1\n' "$WORK" > "$WORK/stubbin/docker"
chmod +x "$WORK/stubbin/docker"
check brew-check-bad-arch-second 2 "unknown architecture 'bogus' (amd64 or arm64)" -- env PATH="$WORK/stubbin:$PATH" "$HERE/check-linux-brew.sh" amd64 bogus
check brew-check-no-container-started 1 "" -- test -e "$WORK/docker-called"
printf '#!/bin/sh\necho "$*" >> "%s/docker-args"\nexit 0\n' "$WORK" > "$WORK/stubbin/docker"
check brew-check-installs-v2-formula 0 "linux/arm64: OK" -- env PATH="$WORK/stubbin:$PATH" "$HERE/check-linux-brew.sh" arm64
# shellcheck disable=SC2016 # the formula loop is matched literally
check brew-check-both-formulas 0 "" -- grep -qF 'for f in agentmesh agentmesh-mcponly; do brew install "ericfitz/tap/$f" && brew test "$f" && "$f" version; done' "$WORK/docker-args"

# --- copy_scripts and write_sums ---------------------------------------------
mkdir -p "$WORK/cs/src" "$WORK/cs/dist"
cp "$WORK/embedded.sh" "$WORK/cs/src/install.sh"
cp "$WORK/embedded.ps1" "$WORK/cs/src/install.ps1"
check copy-scripts-ok 0 "" -- bash -c "source '$HERE/release.sh'; SRC='$WORK/cs/src'; DIST='$WORK/cs/dist'; copy_scripts && cmp '$WORK/cs/src/install.ps1' '$WORK/cs/dist/install.ps1' && cmp '$WORK/cs/src/install.sh' '$WORK/cs/dist/install.sh'"
mkdir -p "$WORK/cs/dist2"
cp "$WORK/unembedded.ps1" "$WORK/cs/src/install.ps1"
check copy-scripts-ps1-unembedded 1 "install.ps1 has no embedded release key" -- bash -c "source '$HERE/release.sh'; SRC='$WORK/cs/src'; DIST='$WORK/cs/dist2'; copy_scripts"
check copy-scripts-ps1-not-copied 1 "" -- test -e "$WORK/cs/dist2/install.ps1"
check sums-list-ps1 0 "" -- bash -c "source '$HERE/release.sh'; DIST='$WORK/cs/dist'; ARCHIVES=(); cp '$WORK/embedded.ps1' '$WORK/cs/dist/install.ps1'; write_sums >/dev/null && awk '{print \$2}' '$WORK/cs/dist/SHA256SUMS' | diff - <(printf 'install.sh\ninstall.ps1\n')"

# --- check-pins.sh ----------------------------------------------------------
SHA=3d3c42e5aac5ba805825da76410c181273ba90b1
pin_fixture() { # <name> <uses-line>
    mkdir -p "$WORK/pins-$1"; printf 'jobs:\n  j:\n    steps:\n      - %s\n' "$2" > "$WORK/pins-$1/w.yml"
}
pin_fixture ok "uses: actions/checkout@$SHA # v7.0.1"
pin_fixture local "uses: ./.github/actions/x"
pin_fixture v4 "uses: actions/checkout@v4"
pin_fixture main "uses: actions/checkout@main"
pin_fixture short "uses: actions/checkout@${SHA:0:7} # v7"
pin_fixture noref "uses: actions/checkout"
pin_fixture docker "uses: docker://alpine:3.20"
pin_fixture nocomment "uses: actions/checkout@$SHA"
pin_fixture flow-v4 "{ uses: actions/checkout@v4 }"
pin_fixture flow-ok "{ uses: actions/checkout@$SHA } # v7.0.1"
check pins-sha-comment-ok 0 "OK" -- "$HERE/check-pins.sh" "$WORK/pins-ok"
check pins-local-ok 0 "OK" -- "$HERE/check-pins.sh" "$WORK/pins-local"
check pins-flow-map-ok 0 "OK" -- "$HERE/check-pins.sh" "$WORK/pins-flow-ok"
for n in v4 main short noref docker nocomment flow-v4; do
    check "pins-$n-fails" 1 "w.yml:4" -- "$HERE/check-pins.sh" "$WORK/pins-$n"
done
# A grep that errors (exit 2) must fail the check, not read as "no uses: lines".
mkdir -p "$WORK/badgrep" && printf '#!/bin/sh\nexit 2\n' > "$WORK/badgrep/grep" && chmod +x "$WORK/badgrep/grep"
check pins-grep-error 1 "grep failed" -- env PATH="$WORK/badgrep:$PATH" "$HERE/check-pins.sh" "$WORK/pins-ok"
check pins-missing-dir 1 "not found" -- "$HERE/check-pins.sh" "$WORK/pins-nonexistent"
mkdir -p "$WORK/pins-empty"
check pins-empty-dir 1 "no workflow files" -- "$HERE/check-pins.sh" "$WORK/pins-empty"

# --- Actions release flow ----------------------------------------------------
check no-docker-in-release-sh 1 "" -- grep -qi docker "$HERE/release.sh"
check args-no-publish-before 0 "" -- bash -c "source '$HERE/release.sh'; parse_args --no-publish v1.2.3 --windows-signing=off && [[ \$NO_PUBLISH == 1 && \$TAG == v1.2.3 ]]"
check args-no-publish-after 0 "" -- bash -c "source '$HERE/release.sh'; parse_args v1.2.3 --no-publish --windows-signing=off && [[ \$NO_PUBLISH == 1 ]]"
check args-default-publish 0 "" -- bash -c "source '$HERE/release.sh'; parse_args v1.2.3 --windows-signing=off && [[ \$NO_PUBLISH == 0 ]]"
check args-no-publish-twice 2 "usage" -- bash -c "source '$HERE/release.sh'; parse_args --no-publish --no-publish v1.2.3"
check args-unknown-flag 2 "unknown option --foo" -- bash -c "source '$HERE/release.sh'; parse_args --foo v1.2.3"
check args-flag-only 2 "usage" -- bash -c "source '$HERE/release.sh'; parse_args --no-publish"
check args-flag-prerelease 2 "tag must look like vX.Y.Z" -- bash -c "source '$HERE/release.sh'; parse_args --no-publish v1.2.3-rc.1"

# check_workflow_at_tag: v0.0.1 predates the workflow file; v0.0.93 has the
# workflow but not the build script; v0.0.92 has both.
mkdir -p "$WORK/repo/.github/workflows" && printf 'name: x\n' > "$WORK/repo/.github/workflows/release-build.yml"
git -C "$WORK/repo" add .github && git -C "$WORK/repo" -c user.email=t@t -c user.name=t commit -q -m wf && git -C "$WORK/repo" tag v0.0.93
mkdir -p "$WORK/repo/release" && printf '#!/usr/bin/env bash\n' > "$WORK/repo/release/build-archive.sh"
git -C "$WORK/repo" add release && git -C "$WORK/repo" -c user.email=t@t -c user.name=t commit -q -m ba && git -C "$WORK/repo" tag v0.0.92
check workflow-at-tag-ok 0 "" -- bash -c "source '$HERE/release.sh'; REPO_ROOT='$WORK/repo'; TAG=v0.0.92; check_workflow_at_tag"
check workflow-at-tag-missing 1 ".github/workflows/release-build.yml is not in tag v0.0.1; tag a commit that has it" -- bash -c "source '$HERE/release.sh'; REPO_ROOT='$WORK/repo'; TAG=v0.0.1; check_workflow_at_tag"
check workflow-at-tag-missing-build-script 1 "release/build-archive.sh is not in tag v0.0.93; tag a commit that has it" -- bash -c "source '$HERE/release.sh'; REPO_ROOT='$WORK/repo'; TAG=v0.0.93; check_workflow_at_tag"

# A stub gh records its arguments and answers from recorded output. A
# "workflow run" swaps in $GH_RUNS_AFTER (when set) as the new run list, which
# models the run that appears after the dispatch.
cat > "$WORK/gh-stub.sh" <<'EOF'
gh() {
    printf '%s\n' "gh $*" >> "$GH_LOG"
    case "$1 $2" in
        "run list") local expr=""; while (($#)); do [[ "$1" == --jq ]] && expr="$2"; shift; done; jq -r "$expr" "$GH_RUNS" ;;
        "release view") [[ -n "${GH_VIEW:-}" ]] && { [[ -n "${GH_VIEW_STDERR:-}" ]] && echo "$GH_VIEW_STDERR" >&2; printf '%s\n' "$GH_VIEW"; return 0; }; echo "${GH_VIEW_ERR:-release not found}" >&2; return 1 ;;
        "workflow view") return "${GH_RC_WORKFLOW:-${GH_RC:-0}}" ;;
        "workflow run") [[ -n "${GH_RUNS_AFTER:-}" ]] && cp "$GH_RUNS_AFTER" "$GH_RUNS"; return "${GH_RC:-0}" ;;
        "release create"|"release edit"|"release download"|"run watch"|"auth status"|"release upload") return "${GH_RC:-0}" ;;
        "attestation verify") return "${GH_RC_ATTEST:-${GH_RC:-0}}" ;;
        "pr list") [[ -z "${GH_PR_FAIL:-}" ]] || return 1; printf '%s' "${GH_PR_OPEN:-}" ;;
        *) echo "gh stub: unexpected call: $*" >&2; return 99 ;;
    esac
}
EOF
cat > "$WORK/runs.json" <<'EOF'
[
  {"databaseId": 101, "displayTitle": "release-build v1.2.3", "createdAt": "2026-10-07T10:00:00Z"},
  {"databaseId": 102, "displayTitle": "release-build v1.2.3", "createdAt": "2026-10-07T10:05:00Z"},
  {"databaseId": 103, "displayTitle": "release-build v9.9.9", "createdAt": "2026-10-07T10:06:00Z"}
]
EOF
# Two stale runs for the tag dated far in the future, so the since bound in
# dispatch_build (now minus a minute) cannot be what excludes them; the
# recorded pre-dispatch ids must.
cat > "$WORK/runs-stale.json" <<'EOF'
[
  {"databaseId": 201, "displayTitle": "release-build v1.2.3", "createdAt": "2099-01-01T00:00:00Z"},
  {"databaseId": 202, "displayTitle": "release-build v1.2.3", "createdAt": "2099-01-01T00:01:00Z"}
]
EOF
cat > "$WORK/runs-fresh.json" <<'EOF'
[
  {"databaseId": 201, "displayTitle": "release-build v1.2.3", "createdAt": "2099-01-01T00:00:00Z"},
  {"databaseId": 202, "displayTitle": "release-build v1.2.3", "createdAt": "2099-01-01T00:01:00Z"},
  {"databaseId": 203, "displayTitle": "release-build v1.2.3", "createdAt": "2099-01-01T00:02:00Z"}
]
EOF
with_stub() { bash -c "source '$HERE/release.sh'; source '$WORK/gh-stub.sh'; export GH_LOG='$WORK/gh.log' GH_RUNS='$WORK/runs.json' FIND_RUN_TRIES=1 FIND_RUN_SLEEP=0; TAG=v1.2.3; VERSION=1.2.3; DIST='$WORK/dist'; REPO_ROOT='$WORK/repo'; $1"; }
check find-run-newest 0 "102" -- with_stub "find_run 2026-10-07T09:00:00Z"
check find-run-since 0 "102" -- with_stub "find_run 2026-10-07T10:04:00Z"
check find-run-none 1 "could not find the dispatched run" -- with_stub "find_run 2026-10-07T10:30:00Z"
check find-run-other-tag-ignored 1 "could not find the dispatched run" -- with_stub "TAG=v9.9.8; find_run 2026-10-07T09:00:00Z"
check find-run-excludes-stale 0 "101" -- with_stub "find_run 2026-10-07T09:00:00Z 102"
check find-run-excludes-all-stale 1 "could not find the dispatched run" -- with_stub "find_run 2026-10-07T09:00:00Z 101 102"
check find-run-bad-exclude 1 "bad run id" -- with_stub "find_run 2026-10-07T09:00:00Z '1; drop'"
check list-runs-for-tag 0 "101 102" -- with_stub "echo \$(list_run_ids | sort)"
check list-runs-other-tag 0 "" -- with_stub "TAG=v8.8.8; [[ -z \$(list_run_ids) ]]"
check dispatch-ignores-stale-run 1 "could not find the dispatched run" -- with_stub "export GH_RUNS='$WORK/runs-stale.json'; dispatch_build"
check dispatch-adopts-new-run 0 "actions/runs/203" -- with_stub "cp '$WORK/runs-stale.json' '$WORK/runs-live.json'; export GH_RUNS='$WORK/runs-live.json' GH_RUNS_AFTER='$WORK/runs-fresh.json'; : > '$WORK/gh.log'; WINDOWS_SIGNING=off; dispatch_build && [[ \$RUN_ID == 203 ]] && grep -q 'gh workflow run release-build.yml --repo ericfitz/agentmesh --ref v1.2.3 -f tag=v1.2.3' '$WORK/gh.log'"
check dispatch-failure-stops 1 "could not dispatch release-build.yml on v1.2.3" -- with_stub "GH_RC=1 dispatch_build"
check draft-created 0 "Creating draft release" -- with_stub "mkdir -p '$WORK/dist'; : > '$WORK/dist/notes.md'; : > '$WORK/gh.log'; ensure_draft && grep -q 'gh release create v1.2.3 .*--verify-tag.*--draft --notes-file' '$WORK/gh.log'"
check draft-other-error-fails 1 "could not query release v1.2.3: HTTP 502" -- with_stub ": > '$WORK/gh.log'; GH_VIEW_ERR='HTTP 502: bad gateway' ensure_draft; rc=\$?; ! grep -q 'release create' '$WORK/gh.log' && exit \$rc"
check draft-reused 0 "Reusing draft release" -- with_stub "GH_VIEW=true ensure_draft && [[ \$PUBLISHED == 0 ]]"
check draft-reused-despite-stderr-noise 0 "Reusing draft release" -- with_stub "GH_VIEW=true GH_VIEW_STDERR='warning: x' ensure_draft && [[ \$PUBLISHED == 0 ]]"
check draft-published-despite-stderr-noise 0 "already published" -- with_stub "GH_VIEW=false GH_VIEW_STDERR='warning: x' ensure_draft && [[ \$PUBLISHED == 1 ]]"
check draft-garbage-stdout-fails 1 "unexpected output" -- with_stub ": > '$WORK/gh.log'; GH_VIEW=\$'true\nwarning: x' ensure_draft; rc=\$?; ! grep -q 'release create' '$WORK/gh.log' && exit \$rc"
check draft-reuse-no-create 0 "" -- with_stub ": > '$WORK/gh.log'; GH_VIEW=true ensure_draft; ! grep -q 'release create' '$WORK/gh.log'"
check draft-already-published 0 "already published" -- with_stub "GH_VIEW=false ensure_draft && [[ \$PUBLISHED == 1 ]]"
check watch-fails-with-url 1 "actions/runs/102" -- with_stub "RUN_ID=102 GH_RC=1 watch_run"
mkdir -p "$WORK/adist" && for b in agentmesh agentmesh-mcponly; do for a in linux-amd64.tar.gz linux-arm64.tar.gz windows-amd64.zip windows-arm64.zip; do : > "$WORK/adist/$b-v1.2.3-$a"; done; done; rm "$WORK/adist/agentmesh-mcponly-v1.2.3-windows-arm64.zip"
with_dist() { with_stub "DIST='$WORK/adist'; $1"; }
check download-requires-eight 1 "agentmesh-mcponly-v1.2.3-windows-arm64.zip" -- with_dist "download_archives"
check download-requires-agentmesh-too 1 "agentmesh-v1.2.3-linux-arm64.tar.gz" -- with_dist "rm -rf '$WORK/adist2'; cp -R '$WORK/adist' '$WORK/adist2'; rm '$WORK/adist2/agentmesh-v1.2.3-linux-arm64.tar.gz'; DIST='$WORK/adist2'; download_archives"
check bin-names-pinned 0 "" -- bash -c "source '$HERE/release.sh'; [[ \"\${BIN_NAMES[*]}\" == 'agentmesh agentmesh-mcponly' && \${#MACOS_ARCHIVES[@]} == 0 ]]"
check download-failure-message 1 "could not download the archives of v1.2.3; rerun the workflow" -- with_dist "GH_RC=1 download_archives"
: > "$WORK/adist/agentmesh-mcponly-v1.2.3-windows-arm64.zip"
check download-ok 0 "" -- with_dist "download_archives && [[ \${#ARCHIVES[@]} == 8 && \${ARCHIVES[0]} == agentmesh-v1.2.3-linux-amd64.tar.gz && \${ARCHIVES[3]} == agentmesh-v1.2.3-windows-arm64.zip && \${ARCHIVES[4]} == agentmesh-mcponly-v1.2.3-linux-amd64.tar.gz && \${ARCHIVES[7]} == agentmesh-mcponly-v1.2.3-windows-arm64.zip ]]"
check download-patterns 0 "" -- with_dist ": > '$WORK/gh.log'; download_archives && grep -q -- '--pattern agentmesh-v1.2.3-linux-\*.tar.gz --pattern agentmesh-v1.2.3-windows-\*.zip --pattern agentmesh-mcponly-v1.2.3-linux-\*.tar.gz --pattern agentmesh-mcponly-v1.2.3-windows-\*.zip' '$WORK/gh.log'"
check download-keeps-macos-first 0 "" -- with_dist "ARCHIVES=(agentmesh-v1.2.3-macos-universal.tar.gz agentmesh-mcponly-v1.2.3-macos-universal.tar.gz); download_archives && [[ \${#ARCHIVES[@]} == 10 && \${ARCHIVES[0]} == agentmesh-v1.2.3-macos-universal.tar.gz && \${ARCHIVES[1]} == agentmesh-mcponly-v1.2.3-macos-universal.tar.gz ]]"
check attest-ok 0 "" -- with_dist ": > '$WORK/gh.log'; download_archives && verify_attestations && [[ \$(grep -c 'gh attestation verify' '$WORK/gh.log') == 8 ]] && grep -q -- '--repo ericfitz/agentmesh' '$WORK/gh.log' && [[ \$(grep -c -- '--signer-workflow ericfitz/agentmesh/.github/workflows/release-build.yml --source-ref refs/tags/v1.2.3' '$WORK/gh.log') == 8 ]]"
check attest-fails 1 "attestation" -- with_dist "download_archives; GH_RC=1 verify_attestations"
check attest-skips-macos 0 "" -- with_dist ": > '$WORK/gh.log'; MACOS_ARCHIVES=(agentmesh-v1.2.3-macos-universal.tar.gz agentmesh-mcponly-v1.2.3-macos-universal.tar.gz); ARCHIVES=(\"\${MACOS_ARCHIVES[@]}\"); download_archives; verify_attestations && ! grep -q macos '$WORK/gh.log' && [[ \$(grep -c 'gh attestation verify' '$WORK/gh.log') == 8 ]]"

# --- render_formula, render_scoop, render_winget and --no-publish ------------
h() { printf '%s%0*d' "$1" $((64 - ${#1})) "$2"; } # h <hex-prefix> <n>: 64 hex chars
M="$(h 1a 1)"; LA="$(h 2b 2)"; LR="$(h 3c 3)"; WA="$(h abcdef 4)"; WR="$(h fedcba 5)"; S1="$(h 6d 6)"; S2="$(h 7e 7)"
M2="$(h 1b 11)"; LA2="$(h 2c 12)"; LR2="$(h 3d 13)"; WA2="$(h abcdee 14)"; WR2="$(h fedcbb 15)"
WA2_UPPER="$(printf '%s' "$WA2" | tr 'a-f' 'A-F')"
WA_UPPER="$(printf '%s' "$WA" | tr 'a-f' 'A-F')" # not ${WA^^}: the harness runs under macOS bash 3.2 too
mkdir -p "$WORK/rdist" "$WORK/rroot/release"
printf '%s  agentmesh-v1.2.3-macos-universal.tar.gz\n%s  agentmesh-mcponly-v1.2.3-macos-universal.tar.gz\n%s  agentmesh-v1.2.3-linux-amd64.tar.gz\n%s  agentmesh-v1.2.3-linux-arm64.tar.gz\n%s  agentmesh-v1.2.3-windows-amd64.zip\n%s  agentmesh-v1.2.3-windows-arm64.zip\n%s  agentmesh-mcponly-v1.2.3-linux-amd64.tar.gz\n%s  agentmesh-mcponly-v1.2.3-linux-arm64.tar.gz\n%s  agentmesh-mcponly-v1.2.3-windows-amd64.zip\n%s  agentmesh-mcponly-v1.2.3-windows-arm64.zip\n%s  install.sh\n%s  install.ps1\n' "$M" "$M2" "$LA" "$LR" "$WA" "$WR" "$LA2" "$LR2" "$WA2" "$WR2" "$S1" "$S2" > "$WORK/rdist/SHA256SUMS"
cp "$HERE"/agentmesh.rb.tmpl "$HERE"/agentmesh.scoop.json.tmpl "$HERE"/agentmesh.winget.*.yaml.tmpl "$HERE"/agentmesh-mcponly.rb.tmpl "$HERE"/agentmesh-mcponly.scoop.json.tmpl "$HERE"/agentmesh-mcponly.winget.*.yaml.tmpl "$WORK/rroot/release/"
# The tap and bucket paths do not exist, so a regression past the --no-publish
# gate would clone from GitHub: git clone, pull, push and fetch, and komac,
# refuse here instead.
cat > "$WORK/net-guard.sh" <<'EOF'
git() { local a; for a in "$@"; do case "$a" in clone | pull | push | fetch) echo "test: refused git $a (network)" >&2; return 97 ;; esac; done; command git "$@"; }
komac() { echo "test: refused komac (network)" >&2; return 97; }
EOF
with_render() { with_stub "source '$WORK/net-guard.sh'; DIST='$WORK/rdist'; REPO_ROOT='$WORK/rroot'; TAP_DIR='$WORK/no-tap'; SCOOP_DIR='$WORK/no-scoop'; KOMAC_KEY_FILE='$WORK/no-komac-key'; $1"; }
# formula_check <binary> <class> <macos-sha> <amd64-sha> <arm64-sha>
formula_check() {
    local b="$1" c="$2" f="\$DIST/$1.rb"
    echo "ruby -c \"$f\" >/dev/null && grep -q '^class $c < Formula' \"$f\" && grep -q 'bin.install \"$b\"' \"$f\" && [[ \$(grep -c 'sha256 \"' \"$f\") == 3 ]] && grep -q 'sha256 \"$3\"' \"$f\" && grep -q 'sha256 \"$4\"' \"$f\" && grep -q 'sha256 \"$5\"' \"$f\" && grep -q 'url \"https://github.com/ericfitz/agentmesh/releases/download/v1.2.3/$b-v1.2.3-macos-universal.tar.gz\"' \"$f\""
}
scoop_check() { # <binary> <amd64-sha> <arm64-sha>
    echo "python3 -I -m json.tool \"\$DIST/$1.scoop.json\" >/dev/null && python3 -I -c \"import json,sys; m=json.load(open(sys.argv[1])); assert m['version']=='1.2.3' and m['bin']=='$1.exe' and m['architecture']['64bit']['hash']=='$2' and m['architecture']['arm64']['hash']=='$3' and m['architecture']['arm64']['url']=='https://github.com/ericfitz/agentmesh/releases/download/v1.2.3/$1-v1.2.3-windows-arm64.zip' and 'checkver' in m and 'autoupdate' in m\" \"\$DIST/$1.scoop.json\""
}
winget_check() { # <binary> <amd64-sha> <arm64-sha> <amd64-sha-upper>
    local id="ericfitz.$1"
    printf '%s\n' "w=\"\$DIST/winget/$id\"; for f in $id.yaml $id.installer.yaml $id.locale.en-US.yaml; do grep -qx 'PackageVersion: 1.2.3' \"\$w/\$f\" || { echo \"no PackageVersion in \$f\"; exit 1; }; grep -qx 'PackageIdentifier: $id' \"\$w/\$f\" || { echo \"wrong PackageIdentifier in \$f\"; exit 1; }; done
    [[ \$(ls \"\$w\" | wc -l | tr -d ' ') == 3 ]] || { echo 'winget dir is not exactly three files'; exit 1; }
    grep -qF 'RelativeFilePath: $1.exe' \"\$w/$id.installer.yaml\" || { echo 'wrong RelativeFilePath'; exit 1; }
    grep -qx '  PortableCommandAlias: $1' \"\$w/$id.installer.yaml\" || { echo 'wrong PortableCommandAlias'; exit 1; }
    grep -qx '  InstallerUrl: https://github.com/ericfitz/agentmesh/releases/download/v1.2.3/$1-v1.2.3-windows-amd64.zip' \"\$w/$id.installer.yaml\" || { echo 'wrong amd64 url'; exit 1; }
    got=\$(grep '^  InstallerSha256:' \"\$w/$id.installer.yaml\" | awk '{print \$2}' | tr 'A-F' 'a-f' | tr '\n' ' ')
    [[ \"\$got\" == '$2 $3 ' ]] || { echo \"installer hashes: \$got\"; exit 1; }
    grep -q '^  InstallerSha256: $4\$' \"\$w/$id.installer.yaml\" || { echo 'hash not upper-cased'; exit 1; }"
}
check no-publish-renders-formula 0 "rendered: $WORK/rdist/agentmesh.rb" -- with_render "NO_PUBLISH=1 publish_release && $(formula_check agentmesh Agentmesh "$M" "$LA" "$LR")"
check no-publish-renders-formula-mcponly 0 "rendered: $WORK/rdist/agentmesh-mcponly.rb" -- with_render "NO_PUBLISH=1 publish_release && $(formula_check agentmesh-mcponly AgentmeshMcponly "$M2" "$LA2" "$LR2")"
check no-publish-renders-scoop 0 "rendered: $WORK/rdist/agentmesh.scoop.json" -- with_render "NO_PUBLISH=1 publish_release && $(scoop_check agentmesh "$WA" "$WR")"
check no-publish-renders-scoop-mcponly 0 "rendered: $WORK/rdist/agentmesh-mcponly.scoop.json" -- with_render "NO_PUBLISH=1 publish_release && $(scoop_check agentmesh-mcponly "$WA2" "$WR2")"
check no-publish-renders-winget 0 "rendered: $WORK/rdist/winget/ericfitz.agentmesh/ericfitz.agentmesh.locale.en-US.yaml" -- with_render "NO_PUBLISH=1 publish_release && $(winget_check agentmesh "$WA" "$WR" "$WA_UPPER")"
check no-publish-renders-winget-mcponly 0 "rendered: $WORK/rdist/winget/ericfitz.agentmesh-mcponly/ericfitz.agentmesh-mcponly.locale.en-US.yaml" -- with_render "NO_PUBLISH=1 publish_release && $(winget_check agentmesh-mcponly "$WA2" "$WR2" "$WA2_UPPER")"
# komac submit takes one directory per package: the tree is exactly the two id directories.
check winget-tree-two-id-directories 0 "" -- with_render "NO_PUBLISH=1 publish_release >/dev/null && [[ \$(ls \"\$DIST/winget\" | tr '\n' ' ') == 'ericfitz.agentmesh ericfitz.agentmesh-mcponly ' && -z \$(find \"\$DIST/winget\" -maxdepth 1 -type f) && \$(find \"\$DIST/winget\" -type f | wc -l | tr -d ' ') == 6 ]]"
check no-publish-ten-rendered-lines 0 "10" -- with_render "NO_PUBLISH=1 publish_release | grep -c '^rendered: '"
check no-publish-stops 0 "stopping before publish" -- with_render ": > '$WORK/gh.log'; update_tap() { echo TAP; }; NO_PUBLISH=1 publish_release && ! grep -q 'release edit' '$WORK/gh.log'"
check no-publish-says-draft 0 "draft: https://github.com/ericfitz/agentmesh/releases/tag/v1.2.3" -- with_render "NO_PUBLISH=1 publish_release"
check no-publish-rerun-says-release 0 "release: https://github.com/ericfitz/agentmesh/releases/tag/v1.2.3" -- with_render "NO_PUBLISH=1 PUBLISHED=1 publish_release"
check no-publish-skips-packagers 0 "" -- with_render "update_tap() { echo TAP; }; update_scoop() { echo SCOOP; }; submit_winget() { echo WINGET; }; [[ -z \$(NO_PUBLISH=1 publish_release | grep -E '^(TAP|SCOOP|WINGET)\$') ]]"
# --no-publish touches nothing outside the repository and its dist: no gh call,
# no tap or bucket clone, no komac, nothing created at the paths it would use.
check no-publish-no-side-effects 0 "" -- with_render ": > '$WORK/gh.log'; NO_PUBLISH=1 publish_release >/dev/null && [[ ! -s '$WORK/gh.log' && ! -e '$WORK/no-tap' && ! -e '$WORK/no-scoop' ]]"
mkdir -p "$WORK/rdist-short"; grep -v 'agentmesh-v1.2.3-linux-arm64' "$WORK/rdist/SHA256SUMS" > "$WORK/rdist-short/SHA256SUMS"
check render-formula-missing-sum 1 "no SHA256SUMS entry for agentmesh-v1.2.3-linux-arm64.tar.gz" -- with_render "DIST='$WORK/rdist-short'; render_formula agentmesh"
mkdir -p "$WORK/rdist-short2"
check render-formula-mcponly-missing-sum 1 "no SHA256SUMS entry for agentmesh-mcponly-v1.2.3-linux-arm64.tar.gz" -- with_render "grep -v 'agentmesh-mcponly-v1.2.3-linux-arm64' '$WORK/rdist/SHA256SUMS' > '$WORK/rdist-short2/SHA256SUMS'; DIST='$WORK/rdist-short2'; render_formula agentmesh-mcponly"
check render-formula-unknown-binary 1 "unknown binary 'bogus'" -- with_render "render_formula bogus"
check render-scoop-unknown-binary 1 "unknown binary 'agentmesh.exe'" -- with_render "render_scoop agentmesh.exe"
check render-winget-unknown-binary 1 "unknown binary ''" -- with_render "render_winget"
check render-formula-missing-sum-renders-nothing 0 "" -- with_render "DIST='$WORK/rdist-short'; (render_formula agentmesh) >/dev/null 2>&1 || true; [[ ! -f \"\$DIST/agentmesh.rb\" ]]"
check render-scoop-missing-sum 1 "no SHA256SUMS entry for agentmesh-v1.2.3-windows-arm64.zip" -- with_render "grep -v 'agentmesh-v1.2.3-windows-arm64' '$WORK/rdist/SHA256SUMS' > '$WORK/rdist-short/SHA256SUMS'; DIST='$WORK/rdist-short'; render_scoop agentmesh"
check render-scoop-mcponly-missing-sum 1 "no SHA256SUMS entry for agentmesh-mcponly-v1.2.3-windows-arm64.zip" -- with_render "grep -v 'agentmesh-mcponly-v1.2.3-windows-arm64' '$WORK/rdist/SHA256SUMS' > '$WORK/rdist-short/SHA256SUMS'; DIST='$WORK/rdist-short'; render_scoop agentmesh-mcponly"
check render-winget-missing-sum 1 "no SHA256SUMS entry for agentmesh-v1.2.3-windows-amd64.zip" -- with_render "grep -v 'agentmesh-v1.2.3-windows-amd64' '$WORK/rdist/SHA256SUMS' > '$WORK/rdist-short/SHA256SUMS'; DIST='$WORK/rdist-short'; render_winget agentmesh"
check render-winget-mcponly-missing-sum 1 "no SHA256SUMS entry for agentmesh-mcponly-v1.2.3-windows-amd64.zip" -- with_render "grep -v 'agentmesh-mcponly-v1.2.3-windows-amd64' '$WORK/rdist/SHA256SUMS' > '$WORK/rdist-short/SHA256SUMS'; DIST='$WORK/rdist-short'; render_winget agentmesh-mcponly"
check publish-undrafts-then-packagers 0 "TAP SCOOP WINGET:agentmesh WINGET:agentmesh-mcponly" -- with_render ": > '$WORK/gh.log'; update_tap() { echo TAP >> '$WORK/order'; }; update_scoop() { echo SCOOP >> '$WORK/order'; }; submit_winget() { echo WINGET:\$1 >> '$WORK/order'; }; : > '$WORK/order'; publish_release >/dev/null && grep -q 'gh release edit v1.2.3 --repo ericfitz/agentmesh --draft=false' '$WORK/gh.log' && echo \$(cat '$WORK/order')"
check publish-renders-before-packagers 0 "" -- with_render "rm -f \"\$DIST/agentmesh.rb\" \"\$DIST/agentmesh-mcponly.rb\" \"\$DIST/agentmesh.scoop.json\" \"\$DIST/agentmesh-mcponly.scoop.json\"; update_tap() { [[ -f \"\$DIST/agentmesh.rb\" && -f \"\$DIST/agentmesh-mcponly.rb\" ]] || exit 9; }; update_scoop() { [[ -f \"\$DIST/agentmesh.scoop.json\" && -f \"\$DIST/agentmesh-mcponly.scoop.json\" ]] || exit 9; }; submit_winget() { [[ -d \"\$DIST/winget/ericfitz.\$1\" ]] || exit 9; }; publish_release >/dev/null"
# upload_assets with no macOS tarball listed does not trip set -u (bash 3.2 empty array).
check upload-assets-empty-macos-list 0 "" -- with_render ": > '$WORK/gh.log'; MACOS_ARCHIVES=(); upload_assets >/dev/null && grep -q 'release upload' '$WORK/gh.log'"
# A winget failure for one package (gh could not list pull requests) does not skip the other, and the run exits 1 naming the failed one.
check publish-winget-failure-tries-second 1 "WINGET:agentmesh-mcponly" -- with_render ": > '$WORK/gh.log'; update_tap() { :; }; update_scoop() { :; }; submit_winget() { echo WINGET:\$1; [[ \$1 != agentmesh ]]; }; publish_release"
check publish-winget-failure-exits-1 1 "the winget submission did not run for: agentmesh;" -- with_render ": > '$WORK/gh.log'; update_tap() { :; }; update_scoop() { :; }; submit_winget() { echo WINGET:\$1; [[ \$1 != agentmesh ]]; }; publish_release"
check publish-winget-ok-exits-0 0 "" -- with_render ": > '$WORK/gh.log'; update_tap() { :; }; update_scoop() { :; }; submit_winget() { :; }; publish_release >/dev/null"
check publish-rerun-skips-edit 0 "" -- with_render ": > '$WORK/gh.log'; update_tap() { :; }; update_scoop() { :; }; submit_winget() { :; }; PUBLISHED=1 publish_release >/dev/null && ! grep -q 'release edit' '$WORK/gh.log'"
check publish-edit-failure-stops 1 "could not publish v1.2.3; the draft stays" -- with_render "update_tap() { echo TAP; }; GH_RC=1 publish_release"
check publish-closing-line 0 "brew install ericfitz/tap/agentmesh; brew install ericfitz/tap/agentmesh-mcponly; curl -fsSL https://github.com/ericfitz/agentmesh/releases/latest/download/install.sh | sh; irm https://github.com/ericfitz/agentmesh/releases/latest/download/install.ps1 | iex" -- with_render "update_tap() { :; }; update_scoop() { :; }; submit_winget() { :; }; publish_release"
check update-tap-needs-render 1 "is not rendered" -- with_render "rm -f \"\$DIST/agentmesh.rb\"; TAP_DIR='$WORK/rp-seed'; update_tap"
check update-tap-needs-render-mcponly 1 "agentmesh-mcponly.rb is not rendered" -- with_render "render_formula agentmesh; render_formula agentmesh-mcponly; rm -f \"\$DIST/agentmesh-mcponly.rb\"; TAP_DIR='$WORK/rp-seed'; update_tap"
check update-scoop-needs-render 1 "is not rendered" -- with_render "rm -f \"\$DIST/agentmesh.scoop.json\"; SCOOP_DIR='$WORK/rp-seed'; update_scoop"
check update-scoop-needs-render-mcponly 1 "agentmesh-mcponly.scoop.json is not rendered" -- with_render "render_scoop agentmesh; render_scoop agentmesh-mcponly; rm -f \"\$DIST/agentmesh-mcponly.scoop.json\"; SCOOP_DIR='$WORK/rp-seed'; update_scoop"

check upload-assets 0 "" -- with_stub ": > '$WORK/gh.log'; mkdir -p '$WORK/udist'; MACOS_ARCHIVES=(agentmesh-v1.2.3-macos-universal.tar.gz agentmesh-mcponly-v1.2.3-macos-universal.tar.gz); DIST='$WORK/udist'; upload_assets && grep -q 'gh release upload v1.2.3 --repo ericfitz/agentmesh --clobber agentmesh-v1.2.3-macos-universal.tar.gz agentmesh-mcponly-v1.2.3-macos-universal.tar.gz install.sh install.ps1 SHA256SUMS SHA256SUMS.sig' '$WORK/gh.log'"
check gh-auth-fails 1 "gh is not authenticated" -- with_stub "GH_RC=1 check_gh"
check gh-attest-missing 1 "cannot verify attestations" -- with_stub "GH_RC_ATTEST=1 check_gh"
check gh-workflow-not-on-default 1 "release-build.yml is not on the default branch of ericfitz/agentmesh; merge it to main first" -- with_stub "GH_RC_WORKFLOW=1 check_gh"
check gh-ok 0 "" -- with_stub "check_gh"

# A rerun after "commit ok, push failed" must push (tap and Scoop bucket).
# Local bare remote; a pre-receive hook makes the first push fail. The git
# remote is a local bare repository: nothing leaves this machine.
git init -q --bare "$WORK/rp-origin.git"
git clone -q "$WORK/rp-origin.git" "$WORK/rp-seed" 2>/dev/null
mkdir -p "$WORK/rp-seed/Formula" "$WORK/rp-seed/bucket"
touch "$WORK/rp-seed/Formula/.keep" "$WORK/rp-seed/bucket/.keep"
git -C "$WORK/rp-seed" add Formula/.keep bucket/.keep && git -C "$WORK/rp-seed" -c user.email=t@t -c user.name=t commit -q -m seed && git -C "$WORK/rp-seed" push -q origin HEAD
mkdir -p "$WORK/rp-dist"
printf '1111  agentmesh-v1.0.0-macos-universal.tar.gz\n2222  agentmesh-v1.0.0-linux-amd64.tar.gz\n3333  agentmesh-v1.0.0-linux-arm64.tar.gz\n4444  agentmesh-v1.0.0-windows-amd64.zip\n5555  agentmesh-v1.0.0-windows-arm64.zip\n6666  agentmesh-mcponly-v1.0.0-macos-universal.tar.gz\n7777  agentmesh-mcponly-v1.0.0-linux-amd64.tar.gz\n8888  agentmesh-mcponly-v1.0.0-linux-arm64.tar.gz\n9999  agentmesh-mcponly-v1.0.0-windows-amd64.zip\naaaa  agentmesh-mcponly-v1.0.0-windows-arm64.zip\n' > "$WORK/rp-dist/SHA256SUMS"
rerun_push_case() { # <render-function> <function> <dir-var> <name>
    local render="$1" fn="$2" var="$3" name="$4" clone="$WORK/rp-$4"
    git clone -q "$WORK/rp-origin.git" "$clone" 2>/dev/null
    printf '#!/bin/sh\nexit 1\n' > "$WORK/rp-origin.git/hooks/pre-receive"; chmod +x "$WORK/rp-origin.git/hooks/pre-receive"
    local run="source '$HERE/release.sh'; REPO_ROOT='$HERE/..'; DIST='$WORK/rp-dist'; TAG=v1.0.0; VERSION=1.0.0; $var='$clone'; export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t; $render; $fn"
    check "$name-first-push-fails" 1 "" -- bash -c "$run"
    check "$name-commit-stranded" 0 "" -- bash -c "[[ \$(git -C '$clone' rev-list --count @{u}..HEAD) == 2 ]]"
    rm -f "$WORK/rp-origin.git/hooks/pre-receive"
    check "$name-rerun-pushes" 0 "" -- bash -c "$run"
    check "$name-pushed" 0 "" -- bash -c "[[ \$(git -C '$clone' rev-list --count @{u}..HEAD) == 0 && \$(git -C '$WORK/rp-origin.git' log --oneline | wc -l) -ge 3 ]]"
    check "$name-commit-per-binary" 0 "" -- bash -c "[[ \$(git -C '$WORK/rp-origin.git' log -n2 --format=%s | tr '\n' ' ') == 'agentmesh-mcponly 1.0.0 agentmesh 1.0.0 ' ]]"
    check "$name-second-run-no-new-commits" 0 "" -- bash -c "$run && [[ \$(git -C '$clone' rev-list --count @{u}..HEAD) == 0 && \$(git -C '$WORK/rp-origin.git' log --oneline | wc -l) == \$(git -C '$clone' log --oneline | wc -l) ]]"
}
rerun_push_case "render_formula agentmesh; render_formula agentmesh-mcponly" update_tap TAP_DIR tap
rerun_push_case "render_scoop agentmesh; render_scoop agentmesh-mcponly" update_scoop SCOOP_DIR scoop

# --- submit_winget (gh and komac stubbed; the key file is a temp file) -------
printf "export KOMAC_GITHUB_KEY='not-a-real-token'\n" > "$WORK/komac-key"
cat > "$WORK/komac-stub.sh" <<'EOF'
komac() { printf 'komac %s token=%s\n' "$*" "${GITHUB_TOKEN:-}" >> "$KOMAC_LOG"; return "${KOMAC_RC:-0}"; }
EOF
with_komac() { with_stub "source '$WORK/komac-stub.sh'; export KOMAC_LOG='$WORK/komac.log'; : > '$WORK/komac.log'; KOMAC_KEY_FILE='$WORK/komac-key'; $1"; }
wurls() { # <binary>
    echo "https://github.com/ericfitz/agentmesh/releases/download/v1.2.3/$1-v1.2.3-windows-amd64.zip https://github.com/ericfitz/agentmesh/releases/download/v1.2.3/$1-v1.2.3-windows-arm64.zip"
}
for b in agentmesh agentmesh-mcponly; do
check "winget-runs-komac-$b" 0 "" -- with_komac "submit_winget $b >/dev/null && [[ \$(cat '$WORK/komac.log') == 'komac update ericfitz.$b --version 1.2.3 --urls $(wurls $b) --submit token=not-a-real-token' ]]"
check "winget-token-not-exported-$b" 0 "" -- with_komac "submit_winget $b >/dev/null; [[ -z \${GITHUB_TOKEN:-} ]]"
check "winget-open-pr-skips-$b" 0 "an open pull request for 1.2.3 exists" -- with_komac "GH_PR_OPEN=42 submit_winget $b && [[ ! -s '$WORK/komac.log' ]]"
check "winget-searches-its-own-id-$b" 0 "" -- with_komac ": > '$WORK/gh.log'; submit_winget $b >/dev/null && grep -qF '\"ericfitz.$b version 1.2.3\" in:title' '$WORK/gh.log'"
check "winget-pr-list-fails-$b" 1 "could not list open pull requests" -- with_komac "GH_PR_FAIL=1 submit_winget $b"
check "winget-komac-failure-keeps-release-$b" 0 "komac failed; the release stands. Rerun:" -- with_komac "KOMAC_RC=1 submit_winget $b"
done
check winget-unknown-binary 1 "unknown binary 'bogus'" -- with_komac "submit_winget bogus"
check winget-no-binary 1 "unknown binary ''" -- with_komac "submit_winget"
check winget-never-komac-new 1 "" -- grep -q 'komac new' "$HERE/release.sh"

# --- build_macos with every signing tool stubbed -----------------------------
# The fake build script writes a shell-script "binary" that prints the version;
# the stubs log their calls. Nothing is signed or notarized.
mkdir -p "$WORK/bm/src/release"
cat > "$WORK/bm/src/release/build-archive.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
binary="$1" tag="$2" goos="$3" goarch="$4" out="$5"
mkdir -p "$out"
printf '#!/bin/sh\necho %s\n' "${tag#v}" > "$out/$binary"
chmod +x "$out/$binary"
printf 'build-archive %s %s %s %s\n' "$binary" "$tag" "$goos" "$goarch" >> "$BM_LOG"
EOF
cat > "$WORK/bm-stubs.sh" <<'EOF'
lipo() { printf 'lipo %s\n' "$*" >> "$BM_LOG"; if [[ "$1" == -create ]]; then cp "$4" "$3"; fi; }
codesign() { printf 'codesign %s\n' "$*" >> "$BM_LOG"; }
ditto() { printf 'ditto %s\n' "$*" >> "$BM_LOG"; : > "${!#}"; }
xcrun() { printf 'xcrun %s\n' "$*" >> "$BM_LOG"; echo "status: ${BM_NOTARY_STATUS:-Accepted}"; }
EOF
bm_run() { bash -c "source '$HERE/release.sh'; source '$WORK/bm-stubs.sh'; export BM_LOG='$WORK/bm.log'; : > '$WORK/bm.log'; TAG=v1.2.3; VERSION=1.2.3; SRC='$WORK/bm/src'; DIST='$WORK/bm/dist'; rm -rf \"\$DIST\"; mkdir -p \"\$DIST\"; $1"; }
check build-macos-ok 0 "" -- bm_run "build_macos >/dev/null && [[ -f \"\$DIST/agentmesh-v1.2.3-macos-universal.tar.gz\" && -f \"\$DIST/agentmesh-mcponly-v1.2.3-macos-universal.tar.gz\" && -f \"\$DIST/notary-agentmesh.log\" && -f \"\$DIST/notary-agentmesh-mcponly.log\" && ! -e \"\$DIST/notary.log\" && ! -e \"\$DIST/notarize.zip\" && \${#MACOS_ARCHIVES[@]} == 2 && \${MACOS_ARCHIVES[0]} == agentmesh-v1.2.3-macos-universal.tar.gz && \${MACOS_ARCHIVES[1]} == agentmesh-mcponly-v1.2.3-macos-universal.tar.gz && \${ARCHIVES[0]} == \${MACOS_ARCHIVES[0]} && \${ARCHIVES[1]} == \${MACOS_ARCHIVES[1]} && \${#ARCHIVES[@]} == 2 ]] && tar -tzf \"\$DIST/agentmesh-v1.2.3-macos-universal.tar.gz\" | grep -qx agentmesh && tar -tzf \"\$DIST/agentmesh-mcponly-v1.2.3-macos-universal.tar.gz\" | grep -qx agentmesh-mcponly"
check build-macos-calls 0 "" -- bm_run "build_macos >/dev/null; for b in agentmesh agentmesh-mcponly; do grep -q \"^build-archive \$b v1.2.3 darwin arm64\\$\" \$BM_LOG && grep -q \"^build-archive \$b v1.2.3 darwin amd64\\$\" \$BM_LOG || exit 1; done; [[ \$(grep -c '^codesign --force --timestamp --options runtime --sign ' \$BM_LOG) == 2 && \$(grep -c '^xcrun notarytool submit .* --keychain-profile sqdist-notary --wait' \$BM_LOG) == 2 && \$(grep -c '^lipo -create' \$BM_LOG) == 2 ]]"
check build-macos-codesigns-each-binary 0 "" -- bm_run "build_macos >/dev/null; grep '^codesign --force' \$BM_LOG | grep -q 'macos/agentmesh\$' && grep '^codesign --force' \$BM_LOG | grep -q 'macos/agentmesh-mcponly\$'"
check build-macos-rerun-over-leftovers 0 "" -- bm_run "build_macos >/dev/null; ls \"\$DIST\" | tr '\\n' ' ' > '$WORK/bm-first.ls'; ARCHIVES=(); MACOS_ARCHIVES=(); build_macos >/dev/null; [[ \$(ls \"\$DIST\" | tr '\\n' ' ') == \$(cat '$WORK/bm-first.ls') && \${#ARCHIVES[@]} == 2 ]]"
check build-macos-notarization-rejected 1 "notarization of agentmesh not accepted" -- bm_run "BM_NOTARY_STATUS=Invalid build_macos"

# main runs the steps in the order the spec pins; every step is replaced by a
# recorder so nothing builds, dispatches or publishes.
check main-order 0 "" -- bash -c "source '$HERE/release.sh'; : > '$WORK/steps'
for f in require_tools check_signing_key check_tag_exists check_tag_pushed check_gh check_workflow_at_tag check_signing_vars check_installers_embedded checkout_tag render_notes build_macos ensure_draft dispatch_build watch_run download_archives verify_attestations copy_scripts write_sums sign_sums upload_assets publish_release; do eval \"\$f() { echo \$f >> '$WORK/steps'; }\"; done
main v1.2.3 --windows-signing=off --no-publish && diff '$WORK/steps' <(printf '%s\n' require_tools check_signing_key check_tag_exists check_tag_pushed check_gh check_workflow_at_tag check_signing_vars check_installers_embedded checkout_tag render_notes build_macos ensure_draft dispatch_build watch_run download_archives verify_attestations copy_scripts write_sums sign_sums upload_assets publish_release)"

# --- require_tools: a missing tool is named; --no-publish needs no komac -----
mkdir -p "$WORK/rt/bin"
printf 'k\n' > "$WORK/rt/key.pem"; cp "$WORK/throwaway.pub" "$WORK/rt/pub"; printf 'x\n' > "$WORK/rt/komac-key"
rt_run() { bash -c "source '$HERE/release.sh'
for t in gh git go lipo codesign xcrun shasum ditto; do eval \"\$t() { :; }\"; done
export AGENTMESH_RELEASE_OPENSSL='$openssl_bin'
SIGNING_KEY='$WORK/rt/key.pem'; PUBKEY='$WORK/rt/pub'; KOMAC_KEY_FILE='$WORK/rt/komac-key'
PATH='$WORK/rt/bin:/usr/bin:/bin'
$1"; }
check require-tools-names-komac 1 "komac" -- rt_run "require_tools"
check require-tools-no-publish-no-komac 0 "" -- rt_run "NO_PUBLISH=1; require_tools"
check require-tools-names-signing-key 1 "signing key $WORK/rt/nokey.pem" -- rt_run "NO_PUBLISH=1; SIGNING_KEY='$WORK/rt/nokey.pem'; require_tools"
check require-tools-names-all 1 "public key $WORK/rt/nopub" -- rt_run "unset -f gh; PUBKEY='$WORK/rt/nopub'; SIGNING_KEY='$WORK/rt/nokey.pem'; NO_PUBLISH=1; require_tools"

# --- Authenticode choice ---------------------------------------------------
check removed-release-notes-args 1 "" -- bash -c "source '$HERE/release.sh'; declare -F release_notes_args"
check ws-missing 2 "--windows-signing=on|off is required" -- parse_args v1.2.3
check ws-on 0 "" -- bash -c "source '$HERE/release.sh'; parse_args v1.2.3 --windows-signing=on && [[ \$WINDOWS_SIGNING == on ]]"
check ws-off-first 0 "" -- bash -c "source '$HERE/release.sh'; parse_args --windows-signing=off v1.2.3 --no-publish && [[ \$WINDOWS_SIGNING == off && \$NO_PUBLISH == 1 ]]"
check ws-bad-value 2 "--windows-signing must be on or off, got 'maybe'" -- parse_args v1.2.3 --windows-signing=maybe
check ws-uppercase 2 "got 'ON'" -- parse_args v1.2.3 --windows-signing=ON
check ws-space-form 2 "unknown option --windows-signing" -- parse_args v1.2.3 --windows-signing on
check ws-twice 2 "usage" -- parse_args v1.2.3 --windows-signing=on --windows-signing=off
check ws-empty 2 "got ''" -- parse_args v1.2.3 --windows-signing=

base=$'## Changes\n\n- something\n'
line='The Windows binaries in this release are not Authenticode-signed; they are covered by the signed SHA256SUMS and by build attestations.'
check notes-off-appends 0 "" -- bash -c "source '$HERE/release.sh'; [[ \$(render_notes_text \"\$1\" off) == \$'## Changes\n\n- something\n\n$line' ]]" _ "$base"
check notes-off-idempotent 0 "1" -- bash -c "source '$HERE/release.sh'; render_notes_text \"\$(render_notes_text \"\$1\" off)\" off | grep -c 'not Authenticode-signed'" _ "$base"
check notes-on-plain 0 "" -- bash -c "source '$HERE/release.sh'; [[ \$(render_notes_text \"\$1\" on) == \$'## Changes\n\n- something' ]]" _ "$base"
check notes-on-removes 0 "0" -- bash -c "source '$HERE/release.sh'; render_notes_text \"\$(render_notes_text \"\$1\" off)\" on | grep -c 'not Authenticode-signed' || true" _ "$base"
check notes-bad-mode 1 "render_notes_text: mode must be on or off" -- bash -c "source '$HERE/release.sh'; render_notes_text x sideways"

cat > "$WORK/gh-vars.sh" <<'EOS'
gh() {
    case "$1 $2" in
        "variable list") [[ -z "${GH_VARS_FAIL:-}" ]] || return 1; printf '%s\n' $GH_VARS ;;
        "api repos/ericfitz/agentmesh/releases/generate-notes") [[ -z "${GH_API_FAIL:-}" ]] || return 1; printf 'generated notes\n' ;;
        *) return 0 ;;
    esac
}
EOS
all_vars="AZURE_CLIENT_ID AZURE_TENANT_ID AZURE_SUBSCRIPTION_ID ARTIFACT_SIGNING_ENDPOINT ARTIFACT_SIGNING_ACCOUNT ARTIFACT_SIGNING_PROFILE AUTHENTICODE_SIGNER"
with_vars() { bash -c "source '$HERE/release.sh'; source '$WORK/gh-vars.sh'; export GH_VARS='$1'; WINDOWS_SIGNING=$2; TAG=v1.2.3; DIST='$WORK/dist'; REPO_ROOT='$WORK/repo'; mkdir -p '$WORK/dist'; $3"; }
check vars-all-present 0 "" -- with_vars "$all_vars" on check_signing_vars
check vars-missing-named 1 "ARTIFACT_SIGNING_PROFILE, AUTHENTICODE_SIGNER" -- with_vars "AZURE_CLIENT_ID AZURE_TENANT_ID AZURE_SUBSCRIPTION_ID ARTIFACT_SIGNING_ENDPOINT ARTIFACT_SIGNING_ACCOUNT" on check_signing_vars
check vars-exact-name 1 "AZURE_CLIENT_ID (docs" -- with_vars "AZURE_CLIENT_ID_OLD AZURE_TENANT_ID AZURE_SUBSCRIPTION_ID ARTIFACT_SIGNING_ENDPOINT ARTIFACT_SIGNING_ACCOUNT ARTIFACT_SIGNING_PROFILE AUTHENTICODE_SIGNER" on check_signing_vars
check vars-list-fails 1 "could not list the repository variables of ericfitz/agentmesh" -- with_vars "" on "GH_VARS_FAIL=1 check_signing_vars"
check render-notes-generate-fails 1 "could not generate the notes of v1.2.3" -- with_vars "" off "GH_API_FAIL=1 render_notes"
check vars-skipped-when-off 0 "" -- with_vars "" off check_signing_vars
check render-notes-generated-off 0 "" -- with_vars "" off "render_notes && grep -q 'generated notes' '$WORK/dist/notes.md' && grep -q 'not Authenticode-signed' '$WORK/dist/notes.md'"
mkdir -p "$WORK/repo/release" && printf 'from file\n' > "$WORK/repo/release/notes-v1.2.3.md"
check render-notes-file-on 0 "" -- with_vars "" on "render_notes && grep -q 'from file' '$WORK/dist/notes.md' && ! grep -q 'not Authenticode-signed' '$WORK/dist/notes.md'"

# ensure_draft and dispatch_build carry the choice (stub gh).
check dispatch-passes-signing 0 "" -- with_stub "WINDOWS_SIGNING=on; cp '$WORK/runs-stale.json' '$WORK/runs-live2.json'; export GH_RUNS='$WORK/runs-live2.json' GH_RUNS_AFTER='$WORK/runs-fresh.json'; : > '$WORK/gh.log'; dispatch_build >/dev/null && grep -q 'gh workflow run release-build.yml --repo ericfitz/agentmesh --ref v1.2.3 -f tag=v1.2.3 -f windows_signing=on\$' '$WORK/gh.log'"
check draft-reuse-edits-notes 0 "" -- with_stub ": > '$WORK/gh.log'; GH_VIEW=true ensure_draft >/dev/null && grep -q 'gh release edit v1.2.3 --repo ericfitz/agentmesh --notes-file $WORK/dist/notes.md' '$WORK/gh.log'"
check draft-published-edits-notes 0 "" -- with_stub ": > '$WORK/gh.log'; GH_VIEW=false ensure_draft >/dev/null 2>&1 && grep -q 'gh release edit v1.2.3 --repo ericfitz/agentmesh --notes-file $WORK/dist/notes.md' '$WORK/gh.log'"
check draft-reuse-edit-failure-stops 1 "could not update the notes of release v1.2.3" -- with_stub "GH_VIEW=true GH_RC=1 ensure_draft"

# checkout_tag prunes stale worktree registrations (a killed run's leftovers).
git -C "$WORK/repo" worktree add -q "$WORK/stale-wt" v0.0.92
rm -rf "$WORK/stale-wt"
check checkout-tag-after-killed-run 0 "" -- bash -c "source '$HERE/release.sh'; REPO_ROOT='$WORK/repo'; DIST='$WORK/co-dist'; TAG=v0.0.92; checkout_tag && [[ -f \"\$SRC/release/build-archive.sh\" && -d '$WORK/co-dist' ]]"
check checkout-tag-prunes-stale-registration 0 "" -- bash -c "source '$HERE/release.sh'; REPO_ROOT='$WORK/repo'; DIST='$WORK/co-dist'; TAG=v0.0.92; checkout_tag && ! git -C '$WORK/repo' worktree list --porcelain | grep -q stale-wt"
# A killed run's partial dist/ is replaced, not reused.
mkdir -p "$WORK/co-dist2" && : > "$WORK/co-dist2/leftover"
check checkout-tag-replaces-dist 0 "" -- bash -c "source '$HERE/release.sh'; REPO_ROOT='$WORK/repo'; DIST='$WORK/co-dist2'; TAG=v0.0.92; checkout_tag && [[ -d '$WORK/co-dist2' && ! -e '$WORK/co-dist2/leftover' ]]"

echo "test-release: $PASS passed, $FAIL failed"
[[ "$FAIL" -eq 0 ]]
