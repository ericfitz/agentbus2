#!/usr/bin/env bash
# Tests install.sh and embed-key.sh against a fake release served locally,
# inside Linux containers (matrix) or with no Docker (--self-test). Needs
# python3, curl, shasum, tar and OpenSSL 3 in both modes, plus Docker (with
# host.docker.internal) in matrix mode. Never touches ~/.keys: it signs with a
# throwaway key and rewrites install.sh's key block in test copies only.
#   release/test-install.sh             full matrix (ubuntu, debian, alpine x amd64, arm64)
#   release/test-install.sh --quick     ubuntu/amd64 only
#   release/test-install.sh --self-test fixture builder, server, judging, embed-key.sh and
#                                       truncation checks, no Docker
# Exit 0 only when every case passes. Every container is labeled with this
# run's id and removed on any exit path; the server and temp dir go with it.
# SC2016: the scripts handed to containers are single-quoted on purpose; the
# container's shell expands them, not this one.
# shellcheck disable=SC2016
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$HERE/.." && pwd)"
PUB="$REPO_ROOT/release/agentmesh-release-ed25519.pub"
WORK=""
FX=""
OPENSSL=""
PORT=$(( 20000 + RANDOM % 20000 ))
RUN_ID="$$-$RANDOM"
SERVER_PID=""
DOCKER_USED=0
PASS=0; FAIL=0
DISTROS=(ubuntu debian alpine); ARCHES=(amd64 arm64)
MODE=matrix
for a in "$@"; do
    case "$a" in
        --quick) DISTROS=(ubuntu); ARCHES=(amd64) ;;
        --self-test) MODE=self ;;
        *) echo "usage: test-install.sh [--quick] [--self-test]" >&2; exit 2 ;;
    esac
done

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

# need_tools: one message naming every missing tool.
need_tools() {
    local missing=() t
    for t in python3 curl shasum tar; do command -v "$t" >/dev/null 2>&1 || missing+=("$t"); done
    if [[ "$MODE" != self ]]; then command -v docker >/dev/null 2>&1 || missing+=(docker); fi
    OPENSSL="$(release_openssl 2>/dev/null)" || missing+=("OpenSSL 3 (set AGENTMESH_RELEASE_OPENSSL or brew install openssl@3)")
    if [[ ${#missing[@]} -gt 0 ]]; then
        echo "test-install: missing required tools: ${missing[*]}" >&2
        exit 1
    fi
}
need_tools
WORK="$(mktemp -d)"
FX="$WORK/fixture"

remove_containers() {
    local c
    [[ "$DOCKER_USED" -eq 1 ]] || return 0
    for c in $(docker ps -aq --filter "label=agentmesh-itest-run=$RUN_ID" 2>/dev/null || true); do
        docker rm -f "$c" >/dev/null 2>&1 || true
    done
}
cleanup() {
    if [[ -n "$SERVER_PID" ]]; then kill "$SERVER_PID" 2>/dev/null || true; fi
    remove_containers
    [[ -z "$WORK" ]] || rm -rf "${WORK:?}"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# --- fixture ------------------------------------------------------------------
# build_fixture lays out $FX/<variant>/{latest,<tag>/...} for the variants
# valid (v9.0.0 and v9.0.1), tampered-tarball, tampered-sums, tampered-sig,
# missing-entry and no-archive (v9.0.1 with SHA256SUMS but no tarballs, so
# the archive download is a 404), plus $FX/install.sh with the throwaway key embedded and
# $FX/install-nokey.sh with the placeholder key block.
build_fixture() {
    "$OPENSSL" genpkey -algorithm ed25519 -out "$WORK/key.pem" 2>/dev/null
    "$OPENSSL" pkey -in "$WORK/key.pem" -pubout -out "$WORK/key.pub" 2>/dev/null
    mkdir -p "$FX/fakes"
    cp "$REPO_ROOT/install.sh" "$FX/install.sh"
    "$HERE/embed-key.sh" "$WORK/key.pub" "$FX/install.sh" >/dev/null
    # A copy with the placeholder block, whatever the repo's install.sh carries.
    awk '
        /^# BEGIN agentmesh release public key$/ { print; print "PUBKEY_PEM=\047-----BEGIN PUBLIC KEY-----"; print "REPLACED-BY-release/embed-key.sh"; print "-----END PUBLIC KEY-----\047"; skip = 1; next }
        /^# END agentmesh release public key$/ { skip = 0 }
        !skip { print }
    ' "$REPO_ROOT/install.sh" > "$FX/install-nokey.sh"
    # Fake commands the cases copy onto the container's PATH.
    printf '#!/bin/sh\nif [ "$1" = -m ]; then echo riscv64; else exec /bin/uname "$@"; fi\n' > "$FX/fakes/uname"
    printf '#!/bin/sh\necho "OpenSSL 1.1.1w  11 Sep 2023"\n' > "$FX/fakes/openssl"
    printf '#!/bin/sh\nsleep 5\nexec /bin/tar "$@"\n' > "$FX/fakes/tar"
    chmod 755 "$FX"/fakes/*
    local tag
    for tag in v9.0.0 v9.0.1; do
        make_release valid "$tag" "${tag#v}"
    done
    printf 'v9.0.1\n' > "$FX/valid/latest"
    local v
    for v in tampered-tarball tampered-sums tampered-sig missing-entry; do
        mkdir -p "$FX/$v"; cp -R "$FX/valid/v9.0.1" "$FX/$v/v9.0.1"; printf 'v9.0.1\n' > "$FX/$v/latest"
    done
    mkdir -p "$FX/no-archive"; cp -R "$FX/valid/v9.0.1" "$FX/no-archive/v9.0.1"; printf 'v9.0.1\n' > "$FX/no-archive/latest"
    rm -f "$FX"/no-archive/v9.0.1/agentmesh-*.tar.gz
    append_byte "$FX/tampered-tarball/v9.0.1/agentmesh-v9.0.1-linux-amd64.tar.gz"
    append_byte "$FX/tampered-tarball/v9.0.1/agentmesh-v9.0.1-linux-arm64.tar.gz"
    append_byte "$FX/tampered-tarball/v9.0.1/agentmesh-mcponly-v9.0.1-linux-amd64.tar.gz"
    append_byte "$FX/tampered-tarball/v9.0.1/agentmesh-mcponly-v9.0.1-linux-arm64.tar.gz"
    # First hex digit of the first hash changed (f -> E, anything else -> f), so it always differs.
    sed '1s/^f/E/; 1s/^[0-9a-e]/f/' "$FX/tampered-sums/v9.0.1/SHA256SUMS" > "$FX/tampered-sums/v9.0.1/SHA256SUMS.new"
    mv "$FX/tampered-sums/v9.0.1/SHA256SUMS.new" "$FX/tampered-sums/v9.0.1/SHA256SUMS"
    append_byte "$FX/tampered-sig/v9.0.1/SHA256SUMS.sig"
    # missing-entry: SHA256SUMS lists only install.sh, re-signed so the signature is valid.
    grep ' install.sh$' "$FX/missing-entry/v9.0.1/SHA256SUMS" > "$FX/missing-entry/v9.0.1/SHA256SUMS.new"
    mv "$FX/missing-entry/v9.0.1/SHA256SUMS.new" "$FX/missing-entry/v9.0.1/SHA256SUMS"
    "$OPENSSL" pkeyutl -sign -rawin -inkey "$WORK/key.pem" -in "$FX/missing-entry/v9.0.1/SHA256SUMS" -out "$FX/missing-entry/v9.0.1/SHA256SUMS.sig"
}

# make_release <variant> <tag> <version>: stubs agentmesh and agentmesh-mcponly
# (sh scripts printing the version), the tarballs of both per architecture,
# install.sh, SHA256SUMS and its signature.
make_release() {
    local tag="$2" version="$3" d="$FX/$1/$2" arch
    mkdir -p "$d/bin"
    printf '#!/bin/sh\necho %s\n' "$version" > "$d/bin/agentmesh"; chmod 755 "$d/bin/agentmesh"
    printf '#!/bin/sh\necho %s\n' "$version" > "$d/bin/agentmesh-mcponly"; chmod 755 "$d/bin/agentmesh-mcponly"
    for arch in amd64 arm64; do
        tar -C "$d/bin" -czf "$d/agentmesh-$tag-linux-$arch.tar.gz" agentmesh
        tar -C "$d/bin" -czf "$d/agentmesh-mcponly-$tag-linux-$arch.tar.gz" agentmesh-mcponly
    done
    rm -r "${d:?}/bin"
    cp "$FX/install.sh" "$d/install.sh"
    (cd "$d" && shasum -a 256 "agentmesh-$tag-linux-amd64.tar.gz" "agentmesh-$tag-linux-arm64.tar.gz" "agentmesh-mcponly-$tag-linux-amd64.tar.gz" "agentmesh-mcponly-$tag-linux-arm64.tar.gz" install.sh > SHA256SUMS)
    "$OPENSSL" pkeyutl -sign -rawin -inkey "$WORK/key.pem" -in "$d/SHA256SUMS" -out "$d/SHA256SUMS.sig"
}

# append_byte makes a file differ from its recorded checksum (or makes a
# signature the wrong length) without depending on the file's content.
append_byte() { printf '\0' >> "$1"; }

start_server() {
    python3 -I "$HERE/testdata/fixture-server.py" "$FX" "$PORT" & SERVER_PID=$!
    for _ in $(seq 1 50); do curl -s -o /dev/null "http://127.0.0.1:$PORT/" && return 0; sleep 0.1; done
    echo "error: fixture server did not start" >&2; exit 1
}

# judge <exit> <output> <want-exit> <want-substring>: true when both match.
judge() { [[ "$1" == "$3" && "$2" == *"$4"* ]]; }

# key_block <install.sh> <pub>: prints one PASS or FAIL line. Both marker lines
# must be present exactly once and in order, and the block between them must
# be the placeholder, or a PEM equal to <pub> when that file exists. Extracts
# textually: sourcing install.sh would run main. The placeholder passes, so
# the automated run is green before Eric embeds the real key (plan Task 9).
key_block() {
    local script="$1" pub="$2" begin='# BEGIN agentmesh release public key' end='# END agentmesh release public key'
    local nb ne lb le block placeholder
    [[ -r "$script" ]] || { echo "FAIL key-block: $script is not readable"; return 1; }
    nb="$(grep -cxF "$begin" "$script" || true)"
    ne="$(grep -cxF "$end" "$script" || true)"
    if [[ "$nb" != 1 || "$ne" != 1 ]]; then
        echo "FAIL key-block: $script holds the BEGIN marker $nb time(s) and the END marker $ne time(s), want exactly once each"; return 1
    fi
    lb="$(grep -nxF "$begin" "$script" | cut -d: -f1)"
    le="$(grep -nxF "$end" "$script" | cut -d: -f1)"
    if [[ "$lb" -ge "$le" ]]; then
        echo "FAIL key-block: $script has the END marker before the BEGIN marker"; return 1
    fi
    block="$(sed -n "$((lb + 1)),$((le - 1))p" "$script")"
    placeholder="$(printf '%s\n%s\n%s' "PUBKEY_PEM='-----BEGIN PUBLIC KEY-----" 'REPLACED-BY-release/embed-key.sh' "-----END PUBLIC KEY-----'")"
    if [[ "$block" == "$placeholder" ]]; then echo "PASS key-block (placeholder)"; return 0; fi
    if [[ ! -r "$pub" ]]; then
        echo "FAIL key-block: $script holds neither the placeholder nor a key block, and $pub does not exist to compare it with"; return 1
    fi
    if [[ "$block" == "PUBKEY_PEM='$(cat "$pub")'" ]]; then echo "PASS key-block"; return 0; fi
    echo "FAIL key-block: the key block of $script differs from $pub (and is not the placeholder); run release/embed-key.sh $pub $script"; return 1
}

# --- self-test ----------------------------------------------------------------
# assert <name> <shell text>: evals the text, counts and prints one PASS or FAIL line.
assert() {
    if eval "$2"; then PASS=$((PASS + 1)); echo "PASS self: $1"; else FAIL=$((FAIL + 1)); echo "FAIL self: $1"; fi
}

# count_line <line>: prints a key_block PASS/FAIL line and counts it.
count_line() {
    echo "$1"
    case "$1" in PASS*) PASS=$((PASS + 1)) ;; *) FAIL=$((FAIL + 1)) ;; esac
}

# check <name> <expected-exit> <required-substring> -- <command...>
# Runs the command, compares its exit code and looks for the substring in its
# combined output (an empty substring skips the output check).
check() {
    local name="$1" want_exit="$2" want_out="$3"; shift 3
    [[ "$1" == "--" ]] && shift
    local out exit=0
    out="$("$@" 2>&1)" || exit=$?
    if [[ "$exit" == "$want_exit" && ( -z "$want_out" || "$out" == *"$want_out"* ) ]]; then
        PASS=$((PASS + 1)); echo "PASS $name"
    else
        FAIL=$((FAIL + 1)); echo "FAIL $name: exit=$exit want=$want_exit; output:"
        # shellcheck disable=SC2001
        echo "$out" | sed 's/^/    /'
    fi
}

# embed_cases: embed-key.sh against synthetic scripts and the fixture's throwaway key.
embed_cases() {
    local e="$WORK/embed" pub="$WORK/key.pub"
    mkdir -p "$e/tmp"
    cat > "$e/script.sh" <<'EOF'
#!/bin/sh
echo before
# BEGIN agentmesh release public key
PUBKEY_PEM='placeholder'
# END agentmesh release public key
echo after
EOF
    cat > "$e/script.ps1" <<'EOF'
# BEGIN agentmesh release public key
$PubKeyPem = @'
placeholder
'@
# END agentmesh release public key
Write-Host after
EOF
    check embed-sh 0 "key embedded" -- "$HERE/embed-key.sh" "$pub" "$e/script.sh"
    check embed-sh-has-key 0 "" -- grep -q "$(sed -n 2p "$pub")" "$e/script.sh"
    check embed-sh-keeps-rest 0 "" -- bash -c "sed -n '1,2p;\$p' '$e/script.sh' | diff - <(printf '#!/bin/sh\necho before\necho after\n')"
    check embed-sh-valid-sh 0 "" -- sh -n "$e/script.sh"
    check embed-sh-key-roundtrip 0 "" -- bash -c "sh -c '. \"$e/script.sh\" >/dev/null; printf \"%s\\n\" \"\$PUBKEY_PEM\"' | diff - '$pub'"
    check embed-ps1 0 "key embedded" -- "$HERE/embed-key.sh" "$pub" "$e/script.ps1"
    check embed-ps1-has-key 0 "" -- grep -q "$(sed -n 2p "$pub")" "$e/script.ps1"
    check embed-idempotent 0 "unchanged" -- bash -c "cp '$e/script.sh' '$e/again.sh' && '$HERE/embed-key.sh' '$pub' '$e/again.sh' && cmp '$e/script.sh' '$e/again.sh'"
    printf 'no markers\n' > "$e/bare.sh"
    check embed-missing-marker 1 "bare.sh" -- "$HERE/embed-key.sh" "$pub" "$e/bare.sh"
    printf '# BEGIN agentmesh release public key \nx\n# END agentmesh release public key\n' > "$e/trail.sh"
    check embed-marker-near-miss 1 "trail.sh" -- "$HERE/embed-key.sh" "$pub" "$e/trail.sh"
    printf '# END agentmesh release public key\nkeep\n# BEGIN agentmesh release public key\nmore\n' > "$e/swapped.sh"
    check embed-marker-order 1 "swapped.sh" -- env TMPDIR="$e/tmp" "$HERE/embed-key.sh" "$pub" "$e/swapped.sh"
    check embed-marker-order-untouched 0 "" -- grep -q '^keep$' "$e/swapped.sh"
    check embed-failure-leaves-no-temp-file 0 "" -- bash -c "[[ -z \"\$(ls -A '$e/tmp')\" && -z \"\$(find '$e' -name '.embed-key.*')\" ]]"
    printf '# BEGIN agentmesh release public key\nPUBKEY_PEM=placeholder\n# END agentmesh release public key\n' > "$e/mode.sh"
    chmod 750 "$e/mode.sh"
    check embed-keeps-mode 0 "key embedded" -- bash -c "'$HERE/embed-key.sh' '$pub' '$e/mode.sh' && [[ \"\$(ls -l '$e/mode.sh' | cut -c1-10)\" == -rwxr-x--- ]]"
    printf '# BEGIN agentmesh release public key\nPUBKEY_PEM=placeholder\n# END agentmesh release public key\n' > "$e/ro.sh"
    chmod 444 "$e/ro.sh"
    check embed-readonly-input 0 "key embedded" -- env TMPDIR="$e/tmp" "$HERE/embed-key.sh" "$pub" "$e/ro.sh"
    check embed-readonly-keeps-mode 0 "" -- bash -c "[[ \"\$(ls -l '$e/ro.sh' | cut -c1-10)\" == -r--r--r-- ]]"
    check embed-success-leaves-no-temp-file 0 "" -- bash -c "[[ -z \"\$(ls -A '$e/tmp')\" && -z \"\$(find '$e' -name '.embed-key.*')\" ]]"
    # The temp file sits next to the script (one filesystem, so the final mv
    # is a rename), never in TMPDIR: a mktemp shim records what it created.
    # (macOS mktemp silently ignores a bad TMPDIR, so only a shim can tell.)
    mkdir -p "$e/shim" "$e/sub"
    printf '#!/bin/sh\nout="$(%s "$@")" || exit $?\nprintf "%%s\\n" "$out" >> "%s"\nprintf "%%s\\n" "$out"\n' "$(command -v mktemp)" "$e/mktemp.log" > "$e/shim/mktemp"
    chmod 755 "$e/shim/mktemp"
    printf '# BEGIN agentmesh release public key\nPUBKEY_PEM=placeholder\n# END agentmesh release public key\n' > "$e/sub/notmp.sh"
    check embed-temp-beside-script 0 "key embedded" -- bash -c "PATH='$e/shim:'\"\$PATH\" TMPDIR='$e/tmp' '$HERE/embed-key.sh' '$pub' '$e/sub/notmp.sh' && [[ -s '$e/mktemp.log' ]] && while IFS= read -r t; do [[ \"\$(dirname \"\$t\")\" == '$e/sub' ]] || { echo \"temp file outside the script's directory: \$t\"; exit 1; }; done < '$e/mktemp.log'"
    check embed-missing-pub 1 "no such public key" -- "$HERE/embed-key.sh" "$e/nope.pub" "$e/script.sh"
    printf 'junk\n' > "$e/junk.pub"
    check embed-bad-pub 1 "not a PEM public key" -- "$HERE/embed-key.sh" "$e/junk.pub" "$e/script.sh"
    check embed-missing-script 1 "no such script" -- "$HERE/embed-key.sh" "$pub" "$e/nope.sh"
    check embed-usage 2 "usage" -- "$HERE/embed-key.sh"
    check unknown-arg 2 "usage: test-install.sh [--quick] [--self-test]" -- bash "${BASH_SOURCE[0]}" --bogus
}

# truncated_cases: a `curl | sh` cut short must run nothing (Review Focus 1).
# The judged values go in globals so assert's eval text holds no pasted output.
# shellcheck disable=SC2034 # the T_ globals are read by assert's eval
truncated_cases() {
    local script="$REPO_ROOT/install.sh" n k home name spec
    n="$(wc -l < "$script" | tr -d ' ')"
    # The line after the first `download()` line is indented, so a cut there is inside a function body.
    k="$(( $(grep -n '^download()' "$script" | head -n1 | cut -d: -f1) + 1 ))"
    T_LINE="$(sed -n "${k}p" "$script")"
    assert "truncation point inside a function body is indented" '[[ "$T_LINE" == " "* ]]'
    for spec in half:$((n / 2)) in-function:$k all-but-last:$((n - 1)); do
        k="${spec#*:}"; name="${spec%%:*}"
        home="$WORK/trunc-$name"; mkdir -p "$home"
        T_RC=0
        T_OUT="$( (set +o pipefail; head -n "$k" "$script" | env HOME="$home" AGENTMESH_BASE_URL=http://127.0.0.1:1 sh 2>&1) )" || T_RC=$?
        T_LOWER="$(printf '%s' "$T_OUT" | tr '[:upper:]' '[:lower:]')"
        T_HOME="$home"
        assert "truncated-script-does-nothing $name: no file created" '[[ -z "$(find "$T_HOME" -mindepth 1)" ]]'
        assert "truncated-script-does-nothing $name: no installer message" '[[ "$T_OUT" != *"agentmesh install:"* ]]'
        if [[ "$name" == all-but-last ]]; then
            assert "truncated-script-does-nothing $name: exit 0 and no output" '[[ "$T_RC" -eq 0 && -z "$T_OUT" ]]'
        else
            # Either a clean no-op or sh's own syntax error. The exit status is not
            # judged for the syntax error: dash exits 2, but bash 3.2 as sh reading
            # a script from stdin exits 0 after "unexpected end of file". Nothing
            # ran either way (the checks above).
            assert "truncated-script-does-nothing $name: no-op or syntax error" '[[ ( "$T_RC" -eq 0 && -z "$T_OUT" ) || "$T_LOWER" == *"syntax error"* || "$T_LOWER" == *unexpected* ]]'
        fi
    done
}

# redirect_cases: a download redirected to plain http is refused (curl
# --proto-redir =https), with a control that the same host and release install
# when nothing redirects. A fake uname makes any host look like Linux x86_64.
redirect_cases() {
    local d="$WORK/linux-fake" real
    mkdir -p "$d" "$FX/redirect-http"
    real="$(command -v uname)"
    printf '#!/bin/sh\ncase "$1" in -s) echo Linux ;; -m) echo x86_64 ;; *) exec %s "$@" ;; esac\n' "$real" > "$d/uname"
    chmod 755 "$d/uname"
    printf 'http://127.0.0.1:%s/valid\n' "$PORT" > "$FX/redirect-http/redirect"
    mkdir -p "$WORK/redir-ok" "$WORK/redir-http"
    check redirect-control-installs 0 "installed agentmesh 9.0.1" -- env PATH="$d:$PATH" HOME="$WORK/redir-ok" AGENTMESH_SKIP_SIGNATURE=1 AGENTMESH_VERSION=v9.0.1 AGENTMESH_BASE_URL="http://127.0.0.1:$PORT/valid" sh "$FX/install.sh"
    check redirect-to-http-refused 1 "download failed: http://127.0.0.1:$PORT/redirect-http/releases/download/v9.0.1/agentmesh-v9.0.1-linux-amd64.tar.gz" -- env PATH="$d:$PATH" HOME="$WORK/redir-http" AGENTMESH_SKIP_SIGNATURE=1 AGENTMESH_VERSION=v9.0.1 AGENTMESH_BASE_URL="http://127.0.0.1:$PORT/redirect-http" sh "$FX/install.sh"
    assert "redirect-to-http-refused: nothing installed" "[[ ! -e '$WORK/redir-http/.local/bin/agentmesh' ]]"
}

# darwin_case: install.sh refuses macOS and names the brew command (Review Focus 3).
darwin_case() {
    local d="$WORK/darwin-fake" real
    mkdir -p "$d"
    real="$(command -v uname)"
    printf '#!/bin/sh\nif [ "$1" = -s ]; then echo Darwin; else exec %s "$@"; fi\n' "$real" > "$d/uname"
    chmod 755 "$d/uname"
    check darwin-refused 1 "brew install ericfitz/tap/agentmesh" -- env PATH="$d:$PATH" HOME="$WORK" AGENTMESH_BASE_URL=http://127.0.0.1:1 sh "$REPO_ROOT/install.sh"
    check darwin-refused-mcponly 1 "brew install ericfitz/tap/agentmesh-mcponly" -- env PATH="$d:$PATH" HOME="$WORK" AGENTMESH_BASE_URL=http://127.0.0.1:1 sh "$REPO_ROOT/install.sh" --mcp-only
}

# argument_cases: install.sh accepts no argument or exactly --mcp-only; anything
# else dies with the usage text before it touches the network or HOME. The
# refusal is the first thing main does, so these run on any OS.
argument_cases() {
    local usage='usage: install.sh [--mcp-only]; configure it with AGENTMESH_VERSION, AGENTMESH_INSTALL_DIR, AGENTMESH_BASE_URL, AGENTMESH_SKIP_SIGNATURE' arg
    mkdir -p "$WORK/args-home"
    for arg in --bogus extra -mcp-only --MCP-ONLY --mcp-only=1 ''; do
        check "bad-argument [$arg]" 1 "$usage" -- env HOME="$WORK/args-home" AGENTMESH_BASE_URL=http://127.0.0.1:1 sh "$REPO_ROOT/install.sh" "$arg"
    done
    check "bad-argument [--mcp-only --mcp-only]" 1 "$usage" -- env HOME="$WORK/args-home" AGENTMESH_BASE_URL=http://127.0.0.1:1 sh "$REPO_ROOT/install.sh" --mcp-only --mcp-only
    check "bad-argument [--mcp-only x]" 1 "$usage" -- env HOME="$WORK/args-home" AGENTMESH_BASE_URL=http://127.0.0.1:1 sh "$REPO_ROOT/install.sh" --mcp-only x
    check "bad-argument [x --mcp-only]" 1 "$usage" -- env HOME="$WORK/args-home" AGENTMESH_BASE_URL=http://127.0.0.1:1 sh "$REPO_ROOT/install.sh" x --mcp-only
    assert "bad-argument: nothing created" '[[ -z "$(find "$WORK/args-home" -mindepth 1)" ]]'
}

self_test() {
    # key-block first: build_fixture runs embed-key.sh over the real install.sh
    # and dies on a missing marker before any FAIL line could print.
    count_line "$(key_block "$REPO_ROOT/install.sh" "$PUB" || true)"
    if [[ "$FAIL" -ne 0 ]]; then
        echo "test-install: $PASS passed, $FAIL failed"
        return 1
    fi
    build_fixture
    assert "sums list five files" "[[ \$(wc -l < '$FX/valid/v9.0.1/SHA256SUMS') -eq 5 ]]"
    assert "sums name both tarballs" "grep -q ' agentmesh-v9.0.1-linux-amd64.tar.gz$' '$FX/valid/v9.0.1/SHA256SUMS' && grep -q ' agentmesh-v9.0.1-linux-arm64.tar.gz$' '$FX/valid/v9.0.1/SHA256SUMS'"
    assert "sums name both mcponly tarballs" "grep -q ' agentmesh-mcponly-v9.0.1-linux-amd64.tar.gz$' '$FX/valid/v9.0.1/SHA256SUMS' && grep -q ' agentmesh-mcponly-v9.0.1-linux-arm64.tar.gz$' '$FX/valid/v9.0.1/SHA256SUMS'"
    assert "mcponly stub prints its version" "[[ \$(tar -xzOf '$FX/valid/v9.0.0/agentmesh-mcponly-v9.0.0-linux-amd64.tar.gz' agentmesh-mcponly | sh) == 9.0.0 ]]"
    assert "mcponly tarball holds only agentmesh-mcponly" "[[ \$(tar -tzf '$FX/valid/v9.0.0/agentmesh-mcponly-v9.0.0-linux-amd64.tar.gz') == agentmesh-mcponly ]]"
    assert "tampered-tarball differs in the mcponly tarballs too" "! cmp -s '$FX/valid/v9.0.1/agentmesh-mcponly-v9.0.1-linux-amd64.tar.gz' '$FX/tampered-tarball/v9.0.1/agentmesh-mcponly-v9.0.1-linux-amd64.tar.gz'"
    assert "valid signature verifies" "'$OPENSSL' pkeyutl -verify -rawin -pubin -inkey '$WORK/key.pub' -in '$FX/valid/v9.0.1/SHA256SUMS' -sigfile '$FX/valid/v9.0.1/SHA256SUMS.sig' >/dev/null 2>&1"
    assert "tampered-sig does not verify" "! '$OPENSSL' pkeyutl -verify -rawin -pubin -inkey '$WORK/key.pub' -in '$FX/tampered-sig/v9.0.1/SHA256SUMS' -sigfile '$FX/tampered-sig/v9.0.1/SHA256SUMS.sig' >/dev/null 2>&1"
    assert "tampered-sums does not verify" "! '$OPENSSL' pkeyutl -verify -rawin -pubin -inkey '$WORK/key.pub' -in '$FX/tampered-sums/v9.0.1/SHA256SUMS' -sigfile '$FX/tampered-sums/v9.0.1/SHA256SUMS.sig' >/dev/null 2>&1"
    assert "tampered-tarball differs only in the tarballs" "cmp -s '$FX/valid/v9.0.1/SHA256SUMS' '$FX/tampered-tarball/v9.0.1/SHA256SUMS' && ! cmp -s '$FX/valid/v9.0.1/agentmesh-v9.0.1-linux-amd64.tar.gz' '$FX/tampered-tarball/v9.0.1/agentmesh-v9.0.1-linux-amd64.tar.gz'"
    assert "missing-entry verifies but lacks tarballs" "'$OPENSSL' pkeyutl -verify -rawin -pubin -inkey '$WORK/key.pub' -in '$FX/missing-entry/v9.0.1/SHA256SUMS' -sigfile '$FX/missing-entry/v9.0.1/SHA256SUMS.sig' >/dev/null 2>&1 && ! grep -q linux '$FX/missing-entry/v9.0.1/SHA256SUMS'"
    assert "no-archive has sums but no tarballs" "[[ -f '$FX/no-archive/v9.0.1/SHA256SUMS' && -z \$(ls '$FX'/no-archive/v9.0.1/agentmesh-*.tar.gz 2>/dev/null) ]]"
    assert "stub prints its version" "[[ \$(tar -xzOf '$FX/valid/v9.0.0/agentmesh-v9.0.0-linux-amd64.tar.gz' agentmesh | sh) == 9.0.0 ]]"
    assert "install.sh copy carries the throwaway key" "grep -q \"\$(sed -n 2p '$WORK/key.pub')\" '$FX/install.sh'"
    assert "install.sh copy differs from the repo only in the key block" "diff <(sed '/BEGIN agentmesh release public key/,/END agentmesh release public key/d' '$REPO_ROOT/install.sh') <(sed '/BEGIN agentmesh release public key/,/END agentmesh release public key/d' '$FX/install.sh') >/dev/null"
    assert "install-nokey.sh has the placeholder and differs only in the key block" "grep -q '^REPLACED-BY-release/embed-key.sh\$' '$FX/install-nokey.sh' && ! grep -qF \"\$(sed -n 2p '$WORK/key.pub')\" '$FX/install-nokey.sh' && diff <(sed '/BEGIN agentmesh release public key/,/END agentmesh release public key/d' '$REPO_ROOT/install.sh') <(sed '/BEGIN agentmesh release public key/,/END agentmesh release public key/d' '$FX/install-nokey.sh') >/dev/null"
    # Recorded outputs for the judging and key-check logic.
    assert "judge accepts matching exit and substring" "judge 1 'agentmesh install: checksum mismatch for x' 1 'checksum mismatch'"
    assert "judge rejects a wrong exit" "! judge 0 'checksum mismatch' 1 'checksum mismatch'"
    assert "judge rejects a missing substring" "! judge 1 'something else' 1 'checksum mismatch'"
    # key_block against recorded inputs.
    local kb="$WORK/kb"; mkdir -p "$kb"
    "$OPENSSL" genpkey -algorithm ed25519 -out "$kb/other.pem" 2>/dev/null
    "$OPENSSL" pkey -in "$kb/other.pem" -pubout -out "$kb/other.pub" 2>/dev/null
    printf '# END agentmesh release public key\n# BEGIN agentmesh release public key\n' > "$kb/swapped.sh"
    printf '# BEGIN agentmesh release public key\n# BEGIN agentmesh release public key\n# END agentmesh release public key\n' > "$kb/dup.sh"
    printf 'echo no markers\n' > "$kb/none.sh"
    printf '# BEGIN agentmesh release public key\nPUBKEY_PEM=junk\n# END agentmesh release public key\n' > "$kb/junk.sh"
    assert "key_block passes for the embedded throwaway key" "[[ \$(key_block '$FX/install.sh' '$WORK/key.pub') == 'PASS key-block' ]]"
    assert "key_block passes for the placeholder with a missing pub" "[[ \$(key_block '$FX/install-nokey.sh' '$WORK/nope.pub') == 'PASS key-block (placeholder)' ]]"
    assert "key_block passes for the placeholder with a pub" "[[ \$(key_block '$FX/install-nokey.sh' '$WORK/key.pub') == 'PASS key-block (placeholder)' ]]"
    assert "key_block fails for a key that differs from the pub" "[[ \$(key_block '$FX/install.sh' '$kb/other.pub') == 'FAIL key-block: the key block'*differs* ]]"
    assert "key_block fails for an embedded key when the pub is missing" "[[ \$(key_block '$FX/install.sh' '$WORK/nope.pub') == 'FAIL key-block:'*'does not exist'* ]]"
    assert "key_block fails for markers in the wrong order" "[[ \$(key_block '$kb/swapped.sh' '$WORK/key.pub') == 'FAIL key-block:'*'before'* ]]"
    assert "key_block fails for a repeated marker" "[[ \$(key_block '$kb/dup.sh' '$WORK/key.pub') == 'FAIL key-block:'*'exactly once'* ]]"
    assert "key_block fails for missing markers" "[[ \$(key_block '$kb/none.sh' '$WORK/key.pub') == 'FAIL key-block:'*'exactly once'* ]]"
    assert "key_block fails for a junk block" "[[ \$(key_block '$kb/junk.sh' '$WORK/key.pub') == 'FAIL key-block:'*differs* ]]"
    embed_cases
    truncated_cases
    darwin_case
    argument_cases
    start_server
    assert "server redirects latest" "[[ \$(curl -sI -o /dev/null -w '%{redirect_url}' http://127.0.0.1:$PORT/valid/releases/latest) == */valid/releases/tag/v9.0.1 ]]"
    assert "server serves SHA256SUMS" "curl -fsS http://127.0.0.1:$PORT/valid/releases/download/v9.0.1/SHA256SUMS | cmp -s - '$FX/valid/v9.0.1/SHA256SUMS'"
    redirect_cases
    local pid="$SERVER_PID"
    kill "$pid" 2>/dev/null || true; wait "$pid" 2>/dev/null || true
    assert "server stopped" "! kill -0 $pid 2>/dev/null"
    SERVER_PID=""
    local w="$WORK"; cleanup; trap - EXIT
    if [[ -d "$w" ]]; then FAIL=$((FAIL + 1)); echo "FAIL self: cleanup left $w"; else PASS=$((PASS + 1)); echo "PASS self: cleanup removed the work directory"; fi
    echo "test-install: $PASS passed, $FAIL failed"
    [[ "$FAIL" -eq 0 ]]
}

# --- container cases ----------------------------------------------------------
BASE="http://host.docker.internal:$PORT"

# run_case <name> <distro> <arch> <expected-exit> <required-substring> <shell script>
# The script runs as root in the container with /fixture mounted read-only and
# AGENTMESH_BASE_URL pointing at the valid variant (cases override it).
run_case() {
    local name="$1" distro="$2" arch="$3" want_exit="$4" want_out="$5" script="$6"
    local out exit=0
    out="$(docker run --rm --add-host host.docker.internal:host-gateway --label "agentmesh-itest-run=$RUN_ID" --platform "linux/$arch" -v "$FX:/fixture:ro" \
        -e "AGENTMESH_BASE_URL=$BASE/valid" -e "BASE=$BASE" -e HOME=/root \
        "agentmesh-itest-$distro-$arch" sh -c "$script" 2>&1)" || exit=$?
    if judge "$exit" "$out" "$want_exit" "$want_out"; then
        PASS=$((PASS + 1)); echo "PASS $distro/$arch $name"
    else
        FAIL=$((FAIL + 1)); echo "FAIL $distro/$arch $name: exit=$exit want=$want_exit; output:"
        # shellcheck disable=SC2001
        echo "$out" | sed 's/^/    /'
    fi
}

# fake <name>: shell text that puts /fixture/fakes/<name> first on PATH.
fake() { printf 'cp /fixture/fakes/%s /usr/local/bin/%s && chmod 755 /usr/local/bin/%s && ' "$1" "$1" "$1"; }

cases() {
    local d="$1" a="$2" rm_openssl
    rm_openssl='rm -f "$(command -v openssl)"'
    run_case valid-piped "$d" "$a" 0 "installed agentmesh 9.0.1" 'cat /fixture/install.sh | sh && ~/.local/bin/agentmesh version'
    run_case valid-prints-next-step "$d" "$a" 0 "agentmesh init --global" 'sh /fixture/install.sh'
    run_case path-warning "$d" "$a" 0 "not on your PATH" 'sh /fixture/install.sh'
    run_case pinned-version "$d" "$a" 0 "installed agentmesh 9.0.0" 'AGENTMESH_VERSION=v9.0.0 sh /fixture/install.sh'
    run_case version-no-v "$d" "$a" 1 "version must look like vX.Y.Z" 'AGENTMESH_VERSION=9.0.0 sh /fixture/install.sh'
    run_case version-prerelease "$d" "$a" 1 "version must look like vX.Y.Z" 'AGENTMESH_VERSION=v1.2.3-rc1 sh /fixture/install.sh'
    run_case version-newline "$d" "$a" 1 "containing a newline" 'AGENTMESH_VERSION="v9.0.0
v9.0.1" sh /fixture/install.sh'
    run_case positional-argument "$d" "$a" 1 "usage: install.sh [--mcp-only]" 'sh /fixture/install.sh extra'
    run_case bad-argument "$d" "$a" 1 "usage: install.sh [--mcp-only]" 'sh /fixture/install.sh --bogus'
    run_case bad-argument-repeated "$d" "$a" 1 "usage: install.sh [--mcp-only]" 'sh /fixture/install.sh --mcp-only --mcp-only'
    run_case bad-argument-extra "$d" "$a" 1 "usage: install.sh [--mcp-only]" 'sh /fixture/install.sh --mcp-only x'
    run_case no-embedded-key "$d" "$a" 1 "no embedded release key" 'sh /fixture/install-nokey.sh'
    run_case latest-unparsable "$d" "$a" 1 "could not determine the latest release" 'AGENTMESH_BASE_URL=$BASE/nope sh /fixture/install.sh'
    run_case tampered-tarball "$d" "$a" 1 "checksum mismatch" 'AGENTMESH_BASE_URL=$BASE/tampered-tarball sh /fixture/install.sh'
    run_case tampered-sums "$d" "$a" 1 "signature check of SHA256SUMS failed" 'AGENTMESH_BASE_URL=$BASE/tampered-sums sh /fixture/install.sh'
    run_case tampered-sig "$d" "$a" 1 "signature check of SHA256SUMS failed" 'AGENTMESH_BASE_URL=$BASE/tampered-sig sh /fixture/install.sh'
    run_case sums-missing-entry "$d" "$a" 1 "SHA256SUMS has no entry for" 'AGENTMESH_BASE_URL=$BASE/missing-entry sh /fixture/install.sh'
    case "$d" in
        ubuntu | debian) run_case no-openssl-suggestion "$d" "$a" 1 "sudo apt-get install openssl" "$rm_openssl; sh /fixture/install.sh" ;;
        alpine) run_case no-openssl-suggestion "$d" "$a" 1 "apk add openssl" "$rm_openssl; sh /fixture/install.sh" ;;
    esac
    run_case openssl-too-old "$d" "$a" 1 "found openssl (OpenSSL 1.1.1w" "$(fake openssl)sh /fixture/install.sh"
    run_case openssl3-name "$d" "$a" 0 "installed agentmesh 9.0.1" 'mv "$(command -v openssl)" /usr/local/bin/openssl3 && sh /fixture/install.sh'
    run_case skip-signature-valid "$d" "$a" 0 "skipping the signature check" "$rm_openssl; AGENTMESH_SKIP_SIGNATURE=1 sh /fixture/install.sh"
    run_case skip-signature-tampered "$d" "$a" 1 "checksum mismatch" 'AGENTMESH_SKIP_SIGNATURE=1 AGENTMESH_BASE_URL=$BASE/tampered-tarball sh /fixture/install.sh'
    run_case skip-signature-bad-value "$d" "$a" 1 "AGENTMESH_SKIP_SIGNATURE must be 1 or unset" 'AGENTMESH_SKIP_SIGNATURE=yes sh /fixture/install.sh'
    run_case unknown-arch "$d" "$a" 1 "unsupported architecture: riscv64" "$(fake uname)sh /fixture/install.sh"
    run_case unwritable-dir "$d" "$a" 1 "Rerun with AGENTMESH_INSTALL_DIR" 'AGENTMESH_INSTALL_DIR=/proc/agentmesh sh /fixture/install.sh'
    run_case install-dir-relative "$d" "$a" 1 "must be an absolute path" 'AGENTMESH_INSTALL_DIR=bin sh /fixture/install.sh'
    run_case install-dir-is-directory "$d" "$a" 1 "refused-clean" 'mkdir -p /tmp/t /opt/x/agentmesh; if out=$(TMPDIR=/tmp/t AGENTMESH_INSTALL_DIR=/opt/x sh /fixture/install.sh 2>&1); then echo "unexpected success"; exit 9; fi; case "$out" in *"/opt/x/agentmesh is a directory"*) ;; *) echo "$out"; exit 8 ;; esac; [ -z "$(ls -A /tmp/t)" ] && [ "$(ls -A /opt/x)" = agentmesh ] && [ -z "$(ls -A /opt/x/agentmesh)" ] && echo refused-clean && exit 1'
    run_case trailing-slashes "$d" "$a" 0 "installed agentmesh 9.0.1 to /opt/y/agentmesh" 'AGENTMESH_BASE_URL=$BASE/valid// AGENTMESH_INSTALL_DIR=/opt/y// sh /fixture/install.sh && [ "$(/opt/y/agentmesh version)" = 9.0.1 ]'
    run_case home-unset "$d" "$a" 1 "HOME is not set" 'env -u HOME sh /fixture/install.sh'
    run_case upgrade "$d" "$a" 0 "upgraded from 9.0.0: restart" 'AGENTMESH_VERSION=v9.0.0 sh /fixture/install.sh >/dev/null && sh /fixture/install.sh && [ "$(~/.local/bin/agentmesh version)" = 9.0.1 ]'
    run_case reinstall-same-version "$d" "$a" 0 "reinstalled: agentmesh 9.0.1 was already installed" 'sh /fixture/install.sh >/dev/null && out=$(sh /fixture/install.sh 2>&1) && echo "$out" && case "$out" in *upgraded*) echo "unexpected upgraded line"; exit 9 ;; esac'
    run_case reinstall-broken-old-binary "$d" "$a" 0 "upgraded: restart" 'mkdir -p ~/.local/bin && printf "#!/bin/sh\nexit 3\n" > ~/.local/bin/agentmesh && chmod 755 ~/.local/bin/agentmesh && sh /fixture/install.sh && [ "$(~/.local/bin/agentmesh version)" = 9.0.1 ]'
    run_case release-without-linux-build "$d" "$a" 1 "release v9.0.1 has no Linux build: agentmesh-v9.0.1-linux-$a.tar.gz" 'AGENTMESH_BASE_URL=$BASE/no-archive sh /fixture/install.sh'
    run_case download-network-failure "$d" "$a" 1 "download failed: http://host.docker.internal:1/releases/download/v9.0.1/" 'AGENTMESH_VERSION=v9.0.1 AGENTMESH_BASE_URL=http://host.docker.internal:1 sh /fixture/install.sh'
    run_case v1-untouched "$d" "$a" 0 "9.0.1" 'mkdir -p ~/.local/bin && printf "v1\n" > ~/.local/bin/agentbus && sh /fixture/install.sh >/dev/null && [ "$(cat ~/.local/bin/agentbus)" = v1 ] && ~/.local/bin/agentmesh version'
    run_case concurrent "$d" "$a" 0 "9.0.1" 'sh /fixture/install.sh >/dev/null & p1=$!; sh /fixture/install.sh >/dev/null & p2=$!; wait $p1 && wait $p2 && ~/.local/bin/agentmesh version'
    run_case leftover-tmp "$d" "$a" 0 "installed agentmesh 9.0.1" 'mkdir -p ~/.local/bin && echo junk > ~/.local/bin/.agentmesh.tmp.12345 && sh /fixture/install.sh && [ -f ~/.local/bin/.agentmesh.tmp.12345 ]'
    # The fake tar is installed in its own statement so that `&` backgrounds
    # install.sh itself and $! is its pid (not a wrapper list's).
    run_case mcponly-install "$d" "$a" 0 "installed agentmesh-mcponly 9.0.1 to /root/.local/bin/agentmesh-mcponly" 'sh /fixture/install.sh --mcp-only && [ "$(~/.local/bin/agentmesh-mcponly version)" = 9.0.1 ] && [ ! -e ~/.local/bin/agentmesh ]'
    run_case mcponly-prints-next-step "$d" "$a" 0 "next: agentmesh-mcponly help (connect this agent to an agentmesh server)" 'sh /fixture/install.sh --mcp-only'
    run_case mcponly-no-init-hint "$d" "$a" 0 "installed agentmesh-mcponly" 'out=$(sh /fixture/install.sh --mcp-only 2>&1) && echo "$out" && case "$out" in *"agentmesh init"*) echo "unexpected init hint"; exit 9 ;; esac'
    run_case mcponly-piped "$d" "$a" 0 "installed agentmesh-mcponly 9.0.1" 'cat /fixture/install.sh | sh -s -- --mcp-only && ~/.local/bin/agentmesh-mcponly version'
    run_case mcponly-pinned-version "$d" "$a" 0 "installed agentmesh-mcponly 9.0.0" 'AGENTMESH_VERSION=v9.0.0 sh /fixture/install.sh --mcp-only'
    run_case mcponly-beside-full "$d" "$a" 0 "installed agentmesh-mcponly 9.0.1" 'sh /fixture/install.sh >/dev/null && before=$(cksum < ~/.local/bin/agentmesh) && out=$(sh /fixture/install.sh --mcp-only 2>&1) && echo "$out" && case "$out" in *upgraded*|*reinstalled*) echo "unexpected upgrade line"; exit 9 ;; esac; [ "$(cksum < ~/.local/bin/agentmesh)" = "$before" ] && [ "$(~/.local/bin/agentmesh version)" = 9.0.1 ] && [ "$(~/.local/bin/agentmesh-mcponly version)" = 9.0.1 ]'
    run_case full-beside-mcponly "$d" "$a" 0 "installed agentmesh 9.0.1" 'sh /fixture/install.sh --mcp-only >/dev/null && before=$(cksum < ~/.local/bin/agentmesh-mcponly) && out=$(sh /fixture/install.sh 2>&1) && echo "$out" && case "$out" in *upgraded*|*reinstalled*) echo "unexpected upgrade line"; exit 9 ;; esac; [ "$(cksum < ~/.local/bin/agentmesh-mcponly)" = "$before" ]'
    run_case mcponly-rerun "$d" "$a" 0 "reinstalled: agentmesh-mcponly 9.0.1 was already installed" 'sh /fixture/install.sh --mcp-only >/dev/null && out=$(sh /fixture/install.sh --mcp-only 2>&1) && echo "$out" && case "$out" in *upgraded*) echo "unexpected upgraded line"; exit 9 ;; esac'
    run_case mcponly-upgrade "$d" "$a" 0 "upgraded from 9.0.0: restart" 'AGENTMESH_VERSION=v9.0.0 sh /fixture/install.sh --mcp-only >/dev/null && sh /fixture/install.sh --mcp-only && [ "$(~/.local/bin/agentmesh-mcponly version)" = 9.0.1 ]'
    run_case mcponly-broken-old-binary "$d" "$a" 0 "upgraded: restart" 'mkdir -p ~/.local/bin && printf "#!/bin/sh\nexit 3\n" > ~/.local/bin/agentmesh-mcponly && chmod 755 ~/.local/bin/agentmesh-mcponly && sh /fixture/install.sh --mcp-only && [ "$(~/.local/bin/agentmesh-mcponly version)" = 9.0.1 ]'
    run_case mcponly-tampered-tarball "$d" "$a" 1 "checksum mismatch for agentmesh-mcponly-v9.0.1-linux-$a.tar.gz" 'AGENTMESH_BASE_URL=$BASE/tampered-tarball sh /fixture/install.sh --mcp-only'
    run_case mcponly-sums-missing-entry "$d" "$a" 1 "SHA256SUMS has no entry for agentmesh-mcponly-v9.0.1-linux-$a.tar.gz" 'AGENTMESH_BASE_URL=$BASE/missing-entry sh /fixture/install.sh --mcp-only'
    run_case mcponly-without-linux-build "$d" "$a" 1 "release v9.0.1 has no Linux build: agentmesh-mcponly-v9.0.1-linux-$a.tar.gz" 'AGENTMESH_BASE_URL=$BASE/no-archive sh /fixture/install.sh --mcp-only'
    run_case mcponly-unwritable-dir-hint "$d" "$a" 1 "sudo env AGENTMESH_INSTALL_DIR=/usr/local/bin sh -s -- --mcp-only" 'AGENTMESH_INSTALL_DIR=/proc/agentmesh sh /fixture/install.sh --mcp-only'
    run_case mcponly-install-dir-is-directory "$d" "$a" 1 "/opt/x/agentmesh-mcponly is a directory" 'mkdir -p /opt/x/agentmesh-mcponly; AGENTMESH_INSTALL_DIR=/opt/x sh /fixture/install.sh --mcp-only'
    run_case mcponly-leftover-tmp "$d" "$a" 0 "installed agentmesh-mcponly 9.0.1" 'mkdir -p ~/.local/bin && echo junk > ~/.local/bin/.agentmesh-mcponly.tmp.12345 && sh /fixture/install.sh --mcp-only && [ -f ~/.local/bin/.agentmesh-mcponly.tmp.12345 ]'
    run_case mcponly-concurrent "$d" "$a" 0 "9.0.1" 'sh /fixture/install.sh --mcp-only >/dev/null & p1=$!; sh /fixture/install.sh --mcp-only >/dev/null & p2=$!; wait $p1 && wait $p2 && ~/.local/bin/agentmesh-mcponly version'
    run_case mcponly-killed-mid-install "$d" "$a" 0 "clean" "mkdir -p /tmp/t && $(fake tar)true; TMPDIR=/tmp/t sh /fixture/install.sh --mcp-only & p=\$!; sleep 2; kill -TERM \$p; wait \$p; [ -z \"\$(ls -A /tmp/t)\" ] && [ -z \"\$(ls -A ~/.local/bin 2>/dev/null)\" ] && echo clean"
    run_case killed-mid-install "$d" "$a" 0 "clean" "mkdir -p /tmp/t && $(fake tar)true; TMPDIR=/tmp/t sh /fixture/install.sh & p=\$!; sleep 2; kill -TERM \$p; wait \$p; [ -z \"\$(ls -A /tmp/t)\" ] && [ -z \"\$(ls -A ~/.local/bin 2>/dev/null)\" ] && echo clean"
}

main() {
    if [[ "$MODE" == self ]]; then self_test; exit; fi
    count_line "$(key_block "$REPO_ROOT/install.sh" "$PUB" || true)"
    if [[ "$FAIL" -ne 0 ]]; then
        echo "test-install: $PASS passed, $FAIL failed"
        exit 1
    fi
    DOCKER_USED=1
    build_fixture
    start_server
    local d a
    for d in "${DISTROS[@]}"; do for a in "${ARCHES[@]}"; do
        docker build -q --platform "linux/$a" -t "agentmesh-itest-$d-$a" -f "$HERE/testdata/Dockerfile.$d" "$HERE/testdata" >/dev/null
        cases "$d" "$a"
    done; done
    echo "test-install: $PASS passed, $FAIL failed"
    [[ "$FAIL" -eq 0 ]]
}
main
