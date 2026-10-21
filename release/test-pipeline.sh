#!/usr/bin/env bash
# The local, unsigned dry run of the whole release pipeline: release.sh end to
# end against stubbed gh, codesign, notarytool, komac and lipo (when absent),
# local bare repositories for the tag, the Homebrew tap and the Scoop bucket,
# the shared build script, and a throwaway Ed25519 release key.
#   release/test-pipeline.sh [--self-test]
#
# It tests the WORKING TREE, not HEAD: tracked plus untracked-not-ignored
# files that still exist are copied into a fresh `git init` repository,
# committed and tagged there. Task 7's mutations are uncommitted edits in a
# throwaway worktree, and `make verify` runs this in implementer worktrees that
# routinely hold new unstaged files (a clone of HEAD would test the wrong
# tree, and a tracked-only copy would fail the fixture's Go build for reasons
# unrelated to the release tooling). A tracked file deleted from disk is
# skipped. It sources the fixture's release/release.sh, not $ROOT's.
#
# Nothing leaves the machine and no real secret is read:
#   - gh, codesign, xcrun, komac and git are shell-function stubs, and curl,
#     wget, ssh, scp, sftp, nc, ncat, telnet, ftp, rsync, gh and komac are also
#     guard scripts first on PATH, so a child process cannot reach them either;
#     any call appends to $WORK/guard.log and exits 98, and a run whose
#     guard.log exists FAILS. git refuses (exit 98) any ssh/http(s)/git URL or
#     scp-style address; GIT_ALLOW_PROTOCOL=file and a failing GIT_SSH_COMMAND
#     back that up, and the user's git config is ignored.
#   - every path release.sh reads or writes (REPO_ROOT, PUBKEY, DIST,
#     SIGNING_KEY, KOMAC_KEY_FILE, TAP_DIR, SCOOP_DIR) must live under $WORK,
#     else assert_hermetic exits 97 before main runs; the signing key is a
#     throwaway generated in $WORK and nothing under ~/.keys is touched.
#   - the only network use is what `go build` already does (module and
#     toolchain cache) and, on Darwin, the first build of the debian:12 image
#     for the install check.
#
# State: a second run starts from a fresh $WORK; overlapping runs share nothing
# ($ROOT is only read; each run has its own fixture repositories, serve
# directory and server port). A run killed with SIGKILL leaves $WORK (a temp
# directory, with any git worktree registered in the fixture inside it) and
# possibly a fixture-server.py process whose pid is in $WORK/server.pid:
# `kill "$(cat $WORK/server.pid)"; rm -rf $WORK` clears it (never a pkill by
# name, which would kill an overlapping run's server). Nothing in the
# repository needs cleaning.
#
# Tools: git, go, python3, jq, shasum, OpenSSL 3, ruby, tar, zip where ditto
# is absent, curl on Linux (the install check), docker on Darwin (the install
# check runs in a container there), lipo when present.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/.." && pwd)"
USAGE='usage: test-pipeline.sh [--self-test]'
TAG=v0.0.1
VERSION=0.0.1
UNSIGNED_NOTE="The Windows binaries in this release are not Authenticode-signed; they are covered by the signed SHA256SUMS and by build attestations."
GUARDED=(curl wget ssh scp sftp nc ncat telnet ftp rsync gh komac)
PASS=0
FAILN=0
WORK=""
OSSL=""
SERVER_PORT=""

pass() {
    PASS=$((PASS + 1))
    echo "PASS $1"
}
bad() {
    FAILN=$((FAILN + 1))
    echo "FAIL $1${2:+: $2}"
}
# assert <name> <command...>: PASS when the command succeeds (quietly).
assert() {
    local n="$1"
    shift
    if "$@" >/dev/null 2>&1; then pass "$n"; else bad "$n" "command failed: $*"; fi
}

# fgit runs git with the user's configuration and hooks out of the way.
fgit() {
    GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 git -c commit.gpgsign=false -c tag.gpgsign=false "$@"
}

# --- work directory, cleanup ------------------------------------------------

stop_server_in() { # <dir>: kill the server whose pid file is <dir>/server.pid
    local f="$1/server.pid" pid
    [[ -f "$f" ]] || return 0
    pid="$(cat "$f")"
    if [[ "$pid" =~ ^[0-9]+$ ]]; then
        kill "$pid" 2>/dev/null || true
        wait "$pid" 2>/dev/null || true
    fi
    rm -f "$f"
}

cleanup() {
    local f d
    [[ -n "$WORK" && -d "$WORK" && "$WORK" == *agentbus2-pipeline.* ]] || return 0
    # Servers of this run and of any nested probe run live under $WORK; an
    # overlapping run's server lives elsewhere and is never touched.
    while IFS= read -r f; do stop_server_in "$(dirname "$f")"; done < <(find "$WORK" -name server.pid 2>/dev/null)
    d="$WORK"
    WORK=""
    rm -rf "$d"
}

setup_work() {
    WORK="$(mktemp -d "${TMPDIR:-/tmp}/agentbus2-pipeline.XXXXXX")"
    WORK="$(cd "$WORK" && pwd -P)"
    trap cleanup EXIT
    trap 'exit 130' INT
    trap 'exit 143' TERM
    export TMPDIR="$WORK"
    export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t
    mkdir -p "$WORK/state" "$WORK/guardbin"
}

# --- fixture ----------------------------------------------------------------

# copy_tree <src> <dst>: tracked plus untracked-not-ignored files of <src>
# that still exist, into <dst> (a deleted tracked file is skipped).
copy_tree() {
    local src="$1" dst="$2" p
    mkdir -p "$dst"
    {
        while IFS= read -r -d '' p; do
            if [[ -e "$src/$p" || -L "$src/$p" ]]; then printf '%s\0' "$p"; fi
        done < <(git -C "$src" ls-files -z --cached --others --exclude-standard)
    } | tar -C "$src" --null -T - -cf - | tar -C "$dst" -xf -
}

# fixture_openssl <repo>: the OpenSSL 3 the fixture's own release_openssl finds.
fixture_openssl() {
    bash -c "source '$1/release/release.sh'; release_openssl"
}

gen_key() { # <name>: $WORK/<name>.pem and (for key) $WORK/<name>.pub
    "$OSSL" genpkey -algorithm ed25519 -out "$WORK/$1.pem" 2>/dev/null
    "$OSSL" pkey -in "$WORK/$1.pem" -pubout -out "$WORK/$1.pub" 2>/dev/null
    chmod 600 "$WORK/$1.pem"
}

# strip_key <install.sh>: put the placeholder key block back.
strip_key() {
    local f="$1" tmp
    tmp="$(mktemp)"
    BLOCK="PUBKEY_PEM='-----BEGIN PUBLIC KEY-----"$'\n'"REPLACED-BY-release/embed-key.sh"$'\n'"-----END PUBLIC KEY-----'" awk '
        $0 == "# BEGIN agentbus2 release public key" { print; print ENVIRON["BLOCK"]; skip = 1; next }
        $0 == "# END agentbus2 release public key" { skip = 0 }
        !skip { print }
    ' "$f" >"$tmp"
    cat "$tmp" >"$f"
    rm -f "$tmp"
}

# build_fixture <repo-dir> <bare-dir> <tag> <embedded|placeholder>
build_fixture() {
    local dir="$1" bare="$2" tag="$3" mode="$4"
    fgit init -q --bare -b main "$bare"
    if [[ ! -d "$dir" ]]; then copy_tree "$ROOT" "$dir"; fi
    fgit -C "$dir" init -q -b main
    fgit -C "$dir" remote add origin "$bare"
    cp "$WORK/key.pub" "$dir/release/agentbus2-release-ed25519.pub"
    bash "$dir/release/embed-key.sh" "$WORK/key.pub" "$dir/install.sh" "$dir/install.ps1" >/dev/null
    if [[ "$mode" == placeholder ]]; then strip_key "$dir/install.sh"; fi
    echo "Release notes for $tag." >"$dir/release/notes-$tag.md"
    fgit -C "$dir" add -A
    fgit -C "$dir" commit -q -m "fixture $tag"
    fgit -C "$dir" tag "$tag"
    fgit -C "$dir" push -q origin "$tag"
}

# make_remote <name> <subdir>: bare remote $WORK/<name>-origin.git seeded with
# <subdir>/.keep ("seed"), cloned to $WORK/<name>.
make_remote() {
    local name="$1" sub="$2"
    fgit init -q --bare -b main "$WORK/$name-origin.git"
    fgit clone -q "$WORK/$name-origin.git" "$WORK/$name" 2>/dev/null
    mkdir -p "$WORK/$name/$sub"
    : >"$WORK/$name/$sub/.keep"
    fgit -C "$WORK/$name" add "$sub/.keep"
    fgit -C "$WORK/$name" commit -q -m seed
    fgit -C "$WORK/$name" push -q -u origin main 2>/dev/null
}

# build_first_fixture: keys, the v0.0.1 repository, tap and bucket.
build_first_fixture() {
    copy_tree "$ROOT" "$WORK/repo"
    OSSL="$(fixture_openssl "$WORK/repo")" || return 1
    gen_key key
    gen_key other
    build_fixture "$WORK/repo" "$WORK/origin.git" "$TAG" embedded
    printf "export KOMAC_GITHUB_KEY='x'\n" >"$WORK/komac.key"
    make_remote tap Formula
    make_remote scoop bucket
}

# --- stubs ------------------------------------------------------------------

write_guardbin() {
    local n
    for n in "${GUARDED[@]}"; do
        cat >"$WORK/guardbin/$n" <<EOF
#!/bin/sh
printf '%s %s\n' '$n' "\$*" >> '$WORK/guard.log'
echo 'pipeline guard: real network tool reached: $n' "\$*" >&2
exit 98
EOF
        chmod +x "$WORK/guardbin/$n"
    done
}

write_stubs() {
    write_guardbin
    local n
    {
        cat <<'EOF'
# Sourced by every pipeline run after release.sh, so these functions win.
# State lives in files under $WORK (each run is a new process). Nothing here
# talks to a network.
_guard() {
    printf '%s\n' "$*" >> "$WORK/guard.log"
    echo "pipeline guard: real network tool reached: $*" >&2
    exit 98
}
EOF
        for n in "${GUARDED[@]}"; do
            [[ "$n" == gh ]] && continue
            printf '%s() { _guard %s "$@"; }\n' "$n" "$n"
        done
        cat <<'EOF'
git() {
    local a
    for a in "$@"; do
        if [[ "$a" =~ ^[A-Za-z0-9_.-]+@[A-Za-z0-9.-]+: || "$a" =~ ^(ssh|https?|git|ftps?):// ]]; then _guard git "$@"; fi
    done
    command git "$@"
}
assert_hermetic() {
    local v
    for v in REPO_ROOT PUBKEY DIST SIGNING_KEY KOMAC_KEY_FILE TAP_DIR SCOOP_DIR; do
        if [[ "${!v}" != "$WORK"/* ]]; then
            echo "hermetic guard: $v=${!v} is outside $WORK" >&2
            exit 97
        fi
    done
}
gh() {
    printf 'gh %s\n' "$*" >> "$GH_LOG"
    local a="${1:-}" b="${2:-}" arg expr="" prev="" tag="" dir="" n id
    case "$a $b" in
        "auth status" | "workflow view" | "attestation verify" | "variable list" | "pr list") return 0 ;;
        "release view")
            if [[ ! -e "$WORK/state/created" ]]; then echo 'release not found' >&2; return 1; fi
            if [[ -e "$WORK/state/published" ]]; then echo false; else echo true; fi
            return 0 ;;
        "release create") : > "$WORK/state/created"; return 0 ;;
        "release upload") : > "$WORK/state/uploaded"; return 0 ;;
        "release edit")
            for arg in "$@"; do [[ "$arg" == --draft=false ]] && : > "$WORK/state/published"; done
            return 0 ;;
        "workflow run")
            for arg in "$@"; do [[ "$arg" == tag=* ]] && tag="${arg#tag=}"; done
            [[ -f "$WORK/runs.json" ]] || echo '[]' > "$WORK/runs.json"
            n="$(jq length "$WORK/runs.json")"
            jq --arg t "release-build $tag" --argjson id "$((7 + n))" --arg now "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
                '. + [{databaseId: $id, displayTitle: $t, createdAt: $now}]' "$WORK/runs.json" > "$WORK/runs.json.new"
            mv "$WORK/runs.json.new" "$WORK/runs.json"
            return 0 ;;
        "run list")
            for arg in "$@"; do
                [[ "$prev" == --jq ]] && expr="$arg"
                prev="$arg"
            done
            [[ -f "$WORK/runs.json" ]] || echo '[]' > "$WORK/runs.json"
            jq -r "$expr" "$WORK/runs.json"
            return ;;
        "run watch") return 0 ;;
        "release download")
            tag="${3:-}"
            for arg in "$@"; do
                [[ "$prev" == --dir ]] && dir="$arg"
                prev="$arg"
            done
            [[ -n "$dir" ]] || { echo "gh stub: release download without --dir" >&2; return 99; }
            for id in amd64 arm64; do
                (cd "$SRC" && bash release/build-archive.sh "$tag" linux "$id" "$WORK/linux-$id") >&2 || return 1
                mv "$WORK/linux-$id/agentbus2-$tag-linux-$id.tar.gz" "$dir/"
            done
            for id in amd64 arm64; do
                (cd "$SRC" && bash release/build-archive.sh "$tag" windows "$id" "$WORK/win-$id") >&2 || return 1
                rm -rf "$WORK/win-$id/stage"; mkdir -p "$WORK/win-$id/stage"
                cp "$WORK/win-$id/agentbus2.exe" "$WORK/win-$id/stage/agentbus2.exe"
                cp "$SRC/release/windows-zip-README.txt" "$WORK/win-$id/stage/README.txt"
                (cd "$WORK/win-$id/stage" && python3 -I -m zipfile -c "$dir/agentbus2-$tag-windows-$id.zip" agentbus2.exe README.txt)
            done
            return 0 ;;
    esac
    echo "gh stub: unexpected call: $*" >&2
    printf 'gh %s\n' "$*" >> "$WORK/guard.log"
    return 99
}
codesign() { printf 'codesign %s\n' "$*" >> "$WORK/codesign.log"; }
xcrun() {
    printf 'xcrun %s\n' "$*" >> "$WORK/xcrun.log"
    if [[ "${1:-} ${2:-}" == "notarytool submit" ]]; then echo "  status: Accepted"; return 0; fi
    echo "xcrun stub: unexpected call: $*" >&2
    return 99
}
komac() { printf 'komac %s\n' "$*" >> "$KOMAC_LOG"; }
if ! command -v lipo >/dev/null 2>&1; then
    lipo() {
        case "${1:-}" in
            -create) cp "$4" "$3" ;;
            -info) echo "Architectures in the fat file: $2 are: x86_64 arm64" ;;
            *) echo "lipo stub: unexpected call: $*" >&2; return 99 ;;
        esac
    }
fi
EOF
    } >"$WORK/stubs.sh"
}

# overrides <repo> <dist> <signing-key>: assigned after both `source`s.
overrides() {
    printf 'REPO_ROOT=%q PUBKEY=%q DIST=%q SIGNING_KEY=%q KOMAC_KEY_FILE=%q TAP_DIR=%q SCOOP_DIR=%q FIND_RUN_TRIES=1 FIND_RUN_SLEEP=0' \
        "$1" "$WORK/repo/release/agentbus2-release-ed25519.pub" "$2" "$3" "$WORK/komac.key" "$WORK/tap" "$WORK/scoop"
}

# prelude: environment common to a stubbed process.
prelude() {
    printf 'export WORK=%q TMPDIR=%q GH_LOG=%q KOMAC_LOG=%q PATH=%q GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 GIT_TERMINAL_PROMPT=0 GIT_ALLOW_PROTOCOL=file GIT_SSH_COMMAND=%q; ' \
        "$WORK" "$WORK" "$WORK/gh.log" "$WORK/komac.log" "$WORK/guardbin:$PATH" "$WORK/guardbin/ssh"
}

# run_main <name> <repo> <dist> <signing-key> <args...>: one run in its own
# process, combined output in $WORK/run-<name>.log; sets RC.
RC=0
run_main() {
    local name="$1" repo="$2" dist="$3" key="$4" script
    shift 4
    script="$(prelude)source '$WORK/repo/release/release.sh'; source '$WORK/stubs.sh'; $(overrides "$repo" "$dist" "$key"); assert_hermetic; main \"\$@\""
    RC=0
    bash -c "$script" "run-$name" "$@" >"$WORK/run-$name.log" 2>&1 || RC=$?
}

# probe <bash-snippet>: a stub probe in its own process, output on stdout.
probe() {
    bash -c "$(prelude)source '$WORK/stubs.sh'; $1"
}

# --- server -----------------------------------------------------------------

# serve_layout <dist>: $WORK/serve/valid/{latest,<tag>/...} from <dist>'s
# top-level files.
serve_layout() {
    rm -rf "$WORK/serve"
    mkdir -p "$WORK/serve/valid/$TAG"
    printf '%s\n' "$TAG" >"$WORK/serve/valid/latest"
    find "$1" -maxdepth 1 -type f -exec cp {} "$WORK/serve/valid/$TAG/" \;
}

# start_server: fixture-server.py on a free port (retried on a bind race); the
# pid goes to $WORK/server.pid and the port to SERVER_PORT.
start_server() {
    local port pid i
    for _ in 1 2 3 4 5; do
        port="$(python3 -I -c 'import socket; s = socket.socket(); s.bind(("0.0.0.0", 0)); print(s.getsockname()[1])')"
        python3 -I "$HERE/testdata/fixture-server.py" "$WORK/serve" "$port" >"$WORK/server.log" 2>&1 &
        pid=$!
        echo "$pid" >"$WORK/server.pid"
        for ((i = 0; i < 50; i++)); do
            if ! kill -0 "$pid" 2>/dev/null; then break; fi
            if (exec 3<>"/dev/tcp/127.0.0.1/$port") 2>/dev/null; then
                SERVER_PORT="$port"
                return 0
            fi
            sleep 0.1
        done
        kill "$pid" 2>/dev/null || true
        wait "$pid" 2>/dev/null || true
        rm -f "$WORK/server.pid"
    done
    return 1
}

# http_status <port> <path>: prints "<status> <location-or-empty>" without following redirects.
http_status() {
    python3 -I -c '
import http.client, sys
c = http.client.HTTPConnection("127.0.0.1", int(sys.argv[1]), timeout=10)
c.request("GET", sys.argv[2])
r = c.getresponse()
print(r.status, r.getheader("Location") or "")
' "$1" "$2"
}

# --- self-test --------------------------------------------------------------

self_test_cases() {
    setup_work
    local out rc

    # unknown-arg
    rc=0
    out="$(bash "${BASH_SOURCE[0]}" --bogus 2>&1)" || rc=$?
    if [[ "$rc" == 2 && "$out" == *"$USAGE"* ]]; then pass unknown-arg; else bad unknown-arg "exit $rc: $out"; fi
    rc=0
    out="$(bash "${BASH_SOURCE[0]}" --self-test extra 2>&1)" || rc=$?
    if [[ "$rc" == 2 && "$out" == *"$USAGE"* ]]; then pass extra-arg; else bad extra-arg "exit $rc: $out"; fi

    # copy-untracked
    local s="$WORK/scratch"
    mkdir -p "$s/src"
    fgit -C "$s/src" init -q -b main
    printf 'module scratch\n\ngo 1.21\n' >"$s/src/go.mod"
    printf 'package main\n\nfunc main() { extra() }\n' >"$s/src/main.go"
    printf 'package main\n\nfunc gone() {}\n' >"$s/src/gone.go"
    fgit -C "$s/src" add go.mod main.go gone.go
    fgit -C "$s/src" commit -q -m scratch
    printf 'package main\n\nfunc extra() {}\n' >"$s/src/extra.go"
    rm "$s/src/gone.go"
    rc=0
    (copy_tree "$s/src" "$s/dst" && [[ -f "$s/dst/extra.go" && ! -e "$s/dst/gone.go" && -f "$s/dst/main.go" ]] && cd "$s/dst" && go build ./...) >"$s/copy.log" 2>&1 || rc=$?
    if [[ "$rc" == 0 ]]; then pass copy-untracked; else bad copy-untracked "$(cat "$s/copy.log")"; fi

    # fixture
    if ! build_first_fixture >"$WORK/fixture.log" 2>&1; then
        bad fixture "$(cat "$WORK/fixture.log")"
        return
    fi
    pass fixture
    write_stubs
    fgit -C "$WORK/repo" worktree add -q "$WORK/src" "$TAG"

    # embedded keys
    if grep -qxF "$(sed -n 2p "$WORK/key.pub")" "$WORK/repo/install.sh" && ! grep -qxF 'REPLACED-BY-release/embed-key.sh' "$WORK/repo/install.sh"; then pass fixture-embeds-key; else bad fixture-embeds-key; fi

    # gh state machine
    out="$(probe "SRC='$WORK/src'; GH_LOG='$WORK/gh.log';
        gh release view v0.0.1 --json isDraft --jq .isDraft 2>&1 >/dev/null; echo \"rc=\$?\";
        gh release create v0.0.1 --draft; echo \"viewed=\$(gh release view v0.0.1 --json isDraft --jq .isDraft)\";
        gh release edit v0.0.1 --draft=false; echo \"viewed=\$(gh release view v0.0.1 --json isDraft --jq .isDraft)\"" 2>&1)" || true
    if [[ "$out" == *"release not found"*"rc=1"*"viewed=true"*"viewed=false"* ]]; then pass gh-state-machine; else bad gh-state-machine "$out"; fi
    rm -f "$WORK/state/created" "$WORK/state/published"
    rc=0
    out="$(probe "gh frobnicate 2>&1" 2>&1)" || rc=$?
    if [[ "$rc" == 99 && "$out" == *"gh stub: unexpected call: frobnicate"* && -s "$WORK/guard.log" ]]; then pass gh-unexpected-call; else bad gh-unexpected-call "exit $rc: $out"; fi
    rm -f "$WORK/guard.log"

    # release download
    mkdir -p "$WORK/dl"
    rc=0
    out="$(probe "SRC='$WORK/src'; GH_LOG='$WORK/gh.log'; gh release download v0.0.1 --repo x/y --dir '$WORK/dl' --clobber --pattern a 2>&1")" || rc=$?
    local listing
    listing="$(find "$WORK/dl" -maxdepth 1 -type f -exec basename {} \; | LC_ALL=C sort | tr '\n' ' ')"
    if [[ "$rc" == 0 && "$listing" == "agentbus2-v0.0.1-linux-amd64.tar.gz agentbus2-v0.0.1-linux-arm64.tar.gz agentbus2-v0.0.1-windows-amd64.zip agentbus2-v0.0.1-windows-arm64.zip " && -z "$(find "$WORK/dl" -mindepth 1 -type d)" ]]; then
        pass release-download-four-files
    else
        bad release-download-four-files "exit $rc: $listing $out"
    fi
    local zl
    zl="$(python3 -I -m zipfile -l "$WORK/dl/agentbus2-v0.0.1-windows-arm64.zip" 2>&1)"
    if [[ "$zl" == *agentbus2.exe* && "$zl" == *README.txt* ]]; then pass zip-listing; else bad zip-listing "$zl"; fi

    # serve layout and fixture server
    printf 'sums\n' >"$WORK/dl/SHA256SUMS"
    serve_layout "$WORK/dl"
    if start_server; then
        local st
        st="$(http_status "$SERVER_PORT" /valid/releases/latest)"
        if [[ "$st" == "302 /valid/releases/tag/v0.0.1" ]]; then pass serve-302; else bad serve-302 "$st"; fi
        st="$(http_status "$SERVER_PORT" /valid/releases/download/v0.0.1/SHA256SUMS)"
        if [[ "$st" == "200 " ]]; then pass serve-sums; else bad serve-sums "$st"; fi
        stop_server_in "$WORK"
        assert server-stops bash -c "! (exec 3<>/dev/tcp/127.0.0.1/$SERVER_PORT)"
    else
        bad serve-302 "server did not start: $(cat "$WORK/server.log")"
    fi

    # guard: guard scripts, git function and hermetic check
    rm -f "$WORK/guard.log"
    local g ok=1
    for g in "${GUARDED[@]}"; do
        rc=0
        bash -c "$(prelude)bash -c '$g http://example.invalid/x'" >/dev/null 2>&1 || rc=$?
        if [[ "$rc" != 98 ]] || ! grep -q "^$g " "$WORK/guard.log"; then ok=0; bad "guard-script-$g" "exit $rc"; fi
    done
    if [[ "$ok" == 1 ]]; then pass guard-scripts; fi
    for g in 'git clone git@github.com:ericfitz/homebrew-tap.git /nonexistent' 'git ls-remote https://example.invalid/r.git' 'curl http://example.invalid' 'gh api repos/x/y'; do
        rc=0
        out="$(probe "$g" 2>&1)" || rc=$?
        if [[ "$rc" == 98 || "$rc" == 99 ]]; then pass "guard-function: $g"; else bad "guard-function: $g" "exit $rc: $out"; fi
    done
    rc=0
    out="$(bash -c "$(prelude)git -C '$WORK/repo' ls-remote --exit-code --tags origin refs/tags/$TAG" 2>&1)" || rc=$?
    if [[ "$rc" == 0 ]]; then pass guard-allows-local-git; else bad guard-allows-local-git "exit $rc: $out"; fi
    # a bare child process calling git over ssh is stopped by the environment
    rc=0
    out="$(bash -c "$(prelude)command git ls-remote git@example.invalid:x/y.git" 2>&1)" || rc=$?
    if [[ "$rc" != 0 ]]; then pass guard-env-blocks-ssh-child; else bad guard-env-blocks-ssh-child "exit 0: $out"; fi
    rc=0
    out="$(bash -c "$(prelude)export HOME=/nonexistent; source '$WORK/stubs.sh'; REPO_ROOT='$WORK/repo' PUBKEY='$WORK/repo/x' DIST='$WORK/d' SIGNING_KEY='$HOME/.keys/agentbus2-release-ed25519.pem' KOMAC_KEY_FILE='$WORK/k' TAP_DIR='$WORK/t' SCOOP_DIR='$WORK/s'; assert_hermetic" 2>&1)" || rc=$?
    if [[ "$rc" == 97 && "$out" == *"SIGNING_KEY"* ]]; then pass hermetic-rejects-keys-dir; else bad hermetic-rejects-keys-dir "exit $rc: $out"; fi
    rm -f "$WORK/guard.log"

    # state: cleanup, second run, killed run, overlapping runs
    self_test_state
}

# probe_run <label> <mode>: a nested harness setup in a child process (sourcing
# this file): creates its work directory, writes the work path to
# $WORK/<label>.work, starts a server, then exits cleanly (mode=exit) or waits
# to be killed (mode=hang).
PROBE_PID=""
probe_run() {
    local label="$1" mode="$2"
    bash -c "
        source '${BASH_SOURCE[0]}'
        setup_work
        mkdir -p \"\$WORK/serve/valid\"
        start_server
        echo \"\$WORK\" > '$WORK/$label.work'
        echo \"\$SERVER_PORT\" > '$WORK/$label.port'
        echo ready > '$WORK/$label.ready'
        if [ '$mode' = hang ]; then while :; do sleep 1; done; fi
    " >"$WORK/$label.out" 2>&1 &
    PROBE_PID=$!
}

wait_ready() { # <label>
    local i
    for ((i = 0; i < 100; i++)); do
        [[ -f "$WORK/$1.ready" ]] && return 0
        sleep 0.1
    done
    return 1
}

port_open() { (exec 3<>"/dev/tcp/127.0.0.1/$1") 2>/dev/null; }

self_test_state() {
    local pid w1 w2 p1 p2

    # cleanup on a clean exit: the work directory and the server are gone
    probe_run clean exit
    pid="$PROBE_PID"
    wait "$pid" 2>/dev/null || true
    w1="$(cat "$WORK/clean.work" 2>/dev/null || true)"
    p1="$(cat "$WORK/clean.port" 2>/dev/null || true)"
    if [[ -n "$w1" && ! -e "$w1" ]] && ! port_open "$p1"; then pass cleanup-removes-work-and-server; else bad cleanup-removes-work-and-server "work=$w1 port=$p1"; fi

    # killed run: SIGKILL leaves the directory and the server; the documented
    # recovery clears both; a fresh run is not poisoned by the leftovers
    probe_run killed hang
    pid="$PROBE_PID"
    if wait_ready killed; then
        w1="$(cat "$WORK/killed.work")"
        p1="$(cat "$WORK/killed.port")"
        kill -KILL "$pid" 2>/dev/null || true
        wait "$pid" 2>/dev/null || true
        if [[ -d "$w1" ]] && port_open "$p1"; then pass killed-run-leaves-leftovers; else bad killed-run-leaves-leftovers "work=$w1 port=$p1"; fi

        # a fresh run (second run) while the leftovers exist, then its cleanup
        probe_run second exit
        pid="$PROBE_PID"
        wait "$pid" 2>/dev/null || true
        w2="$(cat "$WORK/second.work" 2>/dev/null || true)"
        p2="$(cat "$WORK/second.port" 2>/dev/null || true)"
        if [[ -n "$w2" && "$w2" != "$w1" && ! -e "$w2" && -d "$w1" ]] && port_open "$p1" && [[ "$p1" != "$p2" ]]; then
            pass second-run-fresh-and-overlap-safe
        else
            bad second-run-fresh-and-overlap-safe "w1=$w1 w2=$w2 p1=$p1 p2=$p2"
        fi

        # recovery
        kill "$(cat "$w1/server.pid")" 2>/dev/null || true
        rm -rf "$w1"
        local i gone=0
        for ((i = 0; i < 50; i++)); do
            if ! port_open "$p1"; then
                gone=1
                break
            fi
            sleep 0.1
        done
        if [[ "$gone" == 1 && ! -e "$w1" ]]; then pass killed-run-recovery; else bad killed-run-recovery "work=$w1 port=$p1"; fi
    else
        bad killed-run-leaves-leftovers "probe did not become ready: $(cat "$WORK/killed.out" 2>/dev/null)"
        kill -KILL "$pid" 2>/dev/null || true
    fi
}

self_test() {
    self_test_cases
    echo "test-pipeline self-test cases: $PASS passed, $FAILN failed"
    if ((FAILN > 0)); then
        echo "test-pipeline self-test: FAILED" >&2
        return 1
    fi
    echo "test-pipeline self-test: OK"
}

# --- the pipeline -----------------------------------------------------------

# dist_files <dir>: sorted top-level file names.
dist_files() { find "$1" -maxdepth 1 -type f -exec basename {} \; | LC_ALL=C sort; }

# dist_digest <dir>: a digest of every file under <dir>, to prove it untouched.
dist_digest() { (cd "$1" && find . -type f -exec shasum -a 256 {} + | LC_ALL=C sort | shasum -a 256); }

run_guard_check() { # <name>
    if [[ -e "$WORK/guard.log" ]]; then
        bad "$1-guard" "a real network tool was reached: $(cat "$WORK/guard.log")"
    else
        pass "$1-guard"
    fi
}

install_check() {
    local dist="$1" arch out rc=0
    arch="$(go env GOHOSTARCH)"
    serve_layout "$dist"
    if ! start_server; then
        bad install-check "server did not start: $(cat "$WORK/server.log" 2>/dev/null)"
        return
    fi
    if [[ "$(uname -s)" == Darwin ]]; then
        if ! command -v docker >/dev/null 2>&1; then
            bad install-check "docker is required on Darwin"
            return
        fi
        out="$(docker build -q -t "agentbus2-itest-debian-$arch" -f "$WORK/repo/release/testdata/Dockerfile.debian" "$WORK/repo/release/testdata" 2>&1)" \
            || { bad install-check "docker build failed: $out"; return; }
        out="$(docker run --rm --add-host host.docker.internal:host-gateway -v "$dist:/dist:ro" "agentbus2-itest-debian-$arch" \
            sh -c "AGENTBUS2_BASE_URL=http://host.docker.internal:$SERVER_PORT/valid sh /dist/install.sh && ~/.local/bin/agentbus2 version" 2>&1)" || rc=$?
    else
        mkdir -p "$WORK/home"
        out="$(HOME="$WORK/home" AGENTBUS2_BASE_URL="http://127.0.0.1:$SERVER_PORT/valid" AGENTBUS2_INSTALL_DIR="$WORK/home/bin" sh "$dist/install.sh" 2>&1 \
            && "$WORK/home/bin/agentbus2" version 2>&1)" || rc=$?
    fi
    if [[ "$rc" == 0 && "$out" == *"installed agentbus2 $VERSION"* && "$(tail -n1 <<<"$out")" == "$VERSION" ]]; then
        pass install-check
    else
        bad install-check "exit $rc: $out"
    fi
    stop_server_in "$WORK"
}

host_archive() { # <dist>: the archive that runs on this host
    case "$(uname -s)" in
        Darwin) echo "$1/agentbus2-$TAG-macos-universal.tar.gz" ;;
        *) echo "$1/agentbus2-$TAG-linux-$(go env GOHOSTARCH).tar.gz" ;;
    esac
}

run_a() {
    local dist="$WORK/dist" want got sums f h
    run_main A "$WORK/repo" "$dist" "$WORK/key.pem" "$TAG" --windows-signing=off --no-publish
    local log="$WORK/run-A.log"
    if [[ "$RC" == 0 ]]; then pass A-exit; else bad A-exit "exit $RC: $(tail -n 20 "$log")"; return; fi
    run_guard_check A

    want="$(printf '%s\n' "agentbus2-$TAG-macos-universal.tar.gz" "agentbus2-$TAG-linux-amd64.tar.gz" "agentbus2-$TAG-linux-arm64.tar.gz" \
        "agentbus2-$TAG-windows-amd64.zip" "agentbus2-$TAG-windows-arm64.zip" install.sh install.ps1 SHA256SUMS SHA256SUMS.sig notes.md notary.log \
        agentbus2.rb agentbus2.scoop.json | LC_ALL=C sort)"
    got="$(dist_files "$dist")"
    if [[ "$got" == "$want" ]]; then pass A-dist-files; else bad A-dist-files "got: $(tr '\n' ' ' <<<"$got")"; fi
    got="$(dist_files "$dist/winget")"
    want="$(printf '%s\n' ericfitz.agentbus2.yaml ericfitz.agentbus2.installer.yaml ericfitz.agentbus2.locale.en-US.yaml | LC_ALL=C sort)"
    if [[ "$got" == "$want" && -z "$(find "$dist/winget" -mindepth 1 -type d)" ]]; then pass A-winget-files; else bad A-winget-files "got: $(tr '\n' ' ' <<<"$got")"; fi

    sums="$dist/SHA256SUMS"
    want="$(printf '%s\n' "agentbus2-$TAG-macos-universal.tar.gz" "agentbus2-$TAG-linux-amd64.tar.gz" "agentbus2-$TAG-linux-arm64.tar.gz" \
        "agentbus2-$TAG-windows-amd64.zip" "agentbus2-$TAG-windows-arm64.zip" install.sh install.ps1)"
    if [[ "$(wc -l <"$sums" | tr -d ' ')" == 7 && "$(awk '{print $2}' "$sums")" == "$want" ]]; then pass A-sums-order; else bad A-sums-order "$(cat "$sums")"; fi
    local sumsok=1
    while read -r h f; do
        [[ "$(shasum -a 256 "$dist/$f" | awk '{print $1}')" == "$h" ]] || sumsok=0
    done <"$sums"
    if [[ "$sumsok" == 1 ]]; then pass A-sums-match-files; else bad A-sums-match-files; fi
    assert A-sig-verifies "$OSSL" pkeyutl -verify -rawin -pubin -inkey "$WORK/key.pub" -in "$sums" -sigfile "$dist/SHA256SUMS.sig"

    local keyline
    keyline="$(sed -n 2p "$WORK/key.pub")"
    if grep -qxF "$keyline" "$dist/install.sh" && grep -qxF "$keyline" "$dist/install.ps1" && ! grep -qxF 'REPLACED-BY-release/embed-key.sh' "$dist/install.sh" "$dist/install.ps1"; then
        pass A-installers-hold-throwaway-key
    else
        bad A-installers-hold-throwaway-key
    fi

    # host-native binary
    mkdir -p "$WORK/extract"
    tar -xzf "$(host_archive "$dist")" -C "$WORK/extract"
    got="$("$WORK/extract/agentbus2" version 2>&1 || true)"
    if [[ "$got" == "$VERSION" ]]; then pass A-host-binary-version; else bad A-host-binary-version "got '$got'"; fi

    # formula
    local sm sla slr
    sm="$(shasum -a 256 "$dist/agentbus2-$TAG-macos-universal.tar.gz" | awk '{print $1}')"
    sla="$(shasum -a 256 "$dist/agentbus2-$TAG-linux-amd64.tar.gz" | awk '{print $1}')"
    slr="$(shasum -a 256 "$dist/agentbus2-$TAG-linux-arm64.tar.gz" | awk '{print $1}')"
    if ruby -c "$dist/agentbus2.rb" >/dev/null 2>&1 && grep -q '^class Agentbus2' "$dist/agentbus2.rb" \
        && [[ "$(grep -c '^ *sha256 "' "$dist/agentbus2.rb")" == 3 ]] \
        && grep -qF "sha256 \"$sm\"" "$dist/agentbus2.rb" && grep -qF "sha256 \"$sla\"" "$dist/agentbus2.rb" && grep -qF "sha256 \"$slr\"" "$dist/agentbus2.rb"; then
        pass A-formula
    else
        bad A-formula
    fi

    # scoop
    local wa wr
    wa="$(awk -v f="agentbus2-$TAG-windows-amd64.zip" '$2 == f {print $1}' "$sums")"
    wr="$(awk -v f="agentbus2-$TAG-windows-arm64.zip" '$2 == f {print $1}' "$sums")"
    if python3 -I -m json.tool "$dist/agentbus2.scoop.json" >/dev/null 2>&1 \
        && [[ "$(jq -r '.version' "$dist/agentbus2.scoop.json")" == "$VERSION" && "$(jq -r '.bin' "$dist/agentbus2.scoop.json")" == agentbus2.exe \
        && "$(jq -r '.architecture."64bit".hash' "$dist/agentbus2.scoop.json")" == "$wa" && "$(jq -r '.architecture.arm64.hash' "$dist/agentbus2.scoop.json")" == "$wr" ]]; then
        pass A-scoop
    else
        bad A-scoop
    fi

    # winget
    local wok=1 base="https://github.com/ericfitz/agentbus2/releases/download/$TAG" up lo
    for f in "$dist"/winget/*; do
        grep -qxF 'PackageIdentifier: ericfitz.agentbus2' "$f" && grep -qxF "PackageVersion: $VERSION" "$f" && ! grep -q '__' "$f" || wok=0
    done
    f="$dist/winget/ericfitz.agentbus2.installer.yaml"
    grep -qxF 'InstallerType: zip' "$f" && grep -qxF 'NestedInstallerType: portable' "$f" && grep -qF 'RelativeFilePath: agentbus2.exe' "$f" \
        && grep -qxF "  InstallerUrl: $base/agentbus2-$TAG-windows-amd64.zip" "$f" && grep -qxF "  InstallerUrl: $base/agentbus2-$TAG-windows-arm64.zip" "$f" || wok=0
    up="$(tr 'a-f' 'A-F' <<<"$wa")"
    lo="$(tr 'a-f' 'A-F' <<<"$wr")"
    [[ "$(grep -c 'InstallerSha256:' "$f")" == 2 ]] && grep -qiF "InstallerSha256: $up" "$f" && grep -qiF "InstallerSha256: $lo" "$f" || wok=0
    if [[ "$wok" == 1 ]]; then pass A-winget; else bad A-winget; fi

    # log
    if [[ "$(grep -c '^rendered:' "$log")" == 5 ]] && grep -qF 'stopping before publish; draft:' "$log"; then pass A-log; else bad A-log; fi

    # gh log
    local gl="$WORK/gh.log"
    if grep -Eq '^gh release create v0\.0\.1 .*--verify-tag.* --draft' "$gl" \
        && grep -qxF "gh workflow run release-build.yml --repo ericfitz/agentbus2 --ref $TAG -f tag=$TAG -f windows_signing=off" "$gl" \
        && [[ "$(grep -Ec '^gh attestation verify .* --signer-workflow ericfitz/agentbus2/\.github/workflows/release-build\.yml --source-ref refs/tags/v0\.0\.1' "$gl")" == 4 ]] \
        && [[ "$(grep -c '^gh release upload v0\.0\.1 .*--clobber .*agentbus2-v0\.0\.1-macos-universal\.tar\.gz install\.sh install\.ps1 SHA256SUMS SHA256SUMS\.sig' "$gl")" == 1 ]] \
        && ! grep -qF "gh release edit $TAG --repo ericfitz/agentbus2 --draft=false" "$gl"; then
        pass A-gh-log
    else
        bad A-gh-log "$(cat "$gl")"
    fi

    # untouched remotes
    if [[ "$(fgit -C "$WORK/tap-origin.git" rev-list --count main)" == 1 && "$(fgit -C "$WORK/scoop-origin.git" rev-list --count main)" == 1 && ! -s "$WORK/komac.log" ]]; then
        pass A-no-publish-side-effects
    else
        bad A-no-publish-side-effects
    fi

    if [[ "$(tail -n1 "$dist/notes.md")" == "$UNSIGNED_NOTE" ]]; then pass A-notes; else bad A-notes "$(tail -n 3 "$dist/notes.md")"; fi

    install_check "$dist"
}

run_b() {
    local dist="$WORK/dist" log="$WORK/run-B.log" url="https://github.com/ericfitz/agentbus2/releases/download/$TAG" want
    run_main B "$WORK/repo" "$dist" "$WORK/key.pem" "$TAG" --windows-signing=off
    if [[ "$RC" == 0 ]]; then pass B-exit; else bad B-exit "exit $RC: $(tail -n 20 "$log")"; return; fi
    run_guard_check B
    if grep -qF "Reusing draft release $TAG" "$log" && grep -qF -- '--draft=false' "$WORK/gh.log"; then pass B-reuses-draft-and-publishes; else bad B-reuses-draft-and-publishes; fi
    if [[ "$(fgit -C "$WORK/tap-origin.git" log -1 --format=%s main)" == "agentbus2 $VERSION" ]] \
        && fgit -C "$WORK/tap-origin.git" show main:Formula/agentbus2.rb | cmp -s - "$dist/agentbus2.rb"; then
        pass B-tap
    else
        bad B-tap
    fi
    if [[ "$(fgit -C "$WORK/scoop-origin.git" log -1 --format=%s main)" == "agentbus2 $VERSION" ]] \
        && fgit -C "$WORK/scoop-origin.git" show main:bucket/agentbus2.json | cmp -s - "$dist/agentbus2.scoop.json"; then
        pass B-bucket
    else
        bad B-bucket
    fi
    want="komac update ericfitz.agentbus2 --version $VERSION --urls $url/agentbus2-$TAG-windows-amd64.zip $url/agentbus2-$TAG-windows-arm64.zip --submit"
    if [[ "$(cat "$WORK/komac.log" 2>/dev/null)" == "$want" ]]; then pass B-komac; else bad B-komac "$(cat "$WORK/komac.log" 2>/dev/null)"; fi
    if [[ "$(tail -n1 "$log")" == *"brew install ericfitz/tap/agentbus2"* ]]; then pass B-last-line; else bad B-last-line "$(tail -n1 "$log")"; fi
}

run_c() {
    build_fixture "$WORK/repo2" "$WORK/origin2.git" v0.0.2 placeholder
    run_main C "$WORK/repo2" "$WORK/dist-c" "$WORK/key.pem" v0.0.2 --windows-signing=off --no-publish
    if [[ "$RC" == 1 ]] && grep -qF 'install.sh has no embedded release key' "$WORK/run-C.log" && [[ ! -e "$WORK/dist-c" ]]; then pass C-placeholder-refused; else bad C-placeholder-refused "exit $RC: $(tail -n 5 "$WORK/run-C.log")"; fi
    run_guard_check C
}

run_d() {
    local before after
    before="$(dist_digest "$WORK/dist")"
    run_main D "$WORK/repo" "$WORK/dist-d" "$WORK/other.pem" "$TAG" --windows-signing=off --no-publish
    after="$(dist_digest "$WORK/dist")"
    if [[ "$RC" == 1 ]] && grep -qF 'does not match' "$WORK/run-D.log" && [[ ! -e "$WORK/dist-d" && "$before" == "$after" ]]; then
        pass D-wrong-key-refused
    else
        bad D-wrong-key-refused "exit $RC: $(tail -n 5 "$WORK/run-D.log")"
    fi
    run_guard_check D
}

pipeline() {
    setup_work
    if ! build_first_fixture >"$WORK/fixture.log" 2>&1; then
        bad fixture "$(cat "$WORK/fixture.log")"
        return
    fi
    pass fixture
    write_stubs
    run_a
    run_b
    run_c
    run_d
}

entry() {
    case $# in
        0) pipeline ;;
        1) if [[ "$1" == --self-test ]]; then self_test || true; else echo "$USAGE" >&2; exit 2; fi ;;
        *) echo "$USAGE" >&2; exit 2 ;;
    esac
    echo "test-pipeline: $PASS passed, $FAILN failed"
    ((FAILN == 0))
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then entry "$@"; fi
