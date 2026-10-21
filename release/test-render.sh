#!/usr/bin/env bash
# Renders every release template with fake values, asserts no __PLACEHOLDER__
# survives, and syntax-checks the result. No network. When brew is installed it
# also runs a stubbed OS/CPU URL-selection check on the formula, and `brew style`
# when AGENTBUS2_BREW_STYLE=1 (that one needs the network).
#   release/test-render.sh
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
# shellcheck source=release/release.sh
source "$HERE/release.sh"
FAIL=0

# render_check <template> <out-name> <syntax-check command...> -- KEY=VALUE...
# Each render runs in a subshell: render_template exits on an unfilled
# placeholder, and one bad template must not abort the checks after it.
render_check() {
    local tmpl="$1" out="$WORK/$2"; shift 2
    local syntax=(); while [[ "$1" != "--" ]]; do syntax+=("$1"); shift; done; shift
    if (render_template "$tmpl" "$out" "$@" && "${syntax[@]}" "$out" >/dev/null 2>&1); then
        echo "PASS $(basename "$tmpl")"
    else
        echo "FAIL $(basename "$tmpl")"; FAIL=1
    fi
}

# has_line <name> <file> <line>: the file holds exactly this line.
has_line() {
    if grep -qxF -- "$3" "$2"; then echo "PASS $1"; else echo "FAIL $1: $2 lacks the line: $3"; FAIL=1; fi
}

H64() { printf "$1%.0s" {1..64}; }
BASE=https://example.invalid
render_check "$HERE/agentbus2.rb.tmpl" agentbus2.rb ruby -c -- \
    MACOS_URL=$BASE/agentbus2-v1.2.3-macos-universal.tar.gz MACOS_SHA256="$(H64 1)" \
    LINUX_AMD64_URL=$BASE/agentbus2-v1.2.3-linux-amd64.tar.gz LINUX_AMD64_SHA256="$(H64 2)" \
    LINUX_ARM64_URL=$BASE/agentbus2-v1.2.3-linux-arm64.tar.gz LINUX_ARM64_SHA256="$(H64 3)"
for want in 'class Agentbus2 < Formula' 'elsif OS.linux? && Hardware::CPU.intel?' 'elsif OS.linux? && Hardware::CPU.arm?' \
    'macos-universal.tar.gz' 'linux-amd64.tar.gz' 'linux-arm64.tar.gz' 'bin.install "agentbus2"' \
    'agentbus2 init --global' 'shell_output("#{bin}/agentbus2 version")'; do
    grep -qF "$want" "$WORK/agentbus2.rb" || { echo "FAIL formula lacks: $want"; FAIL=1; }
done
# on_macos/on_linux blocks may not contain url/sha256 (brew style rejects them).
if grep -qE '^[[:space:]]*on_(macos|linux|intel|arm)[[:space:]]+do' "$WORK/agentbus2.rb"; then echo "FAIL formula uses on_* blocks for url"; FAIL=1; fi
if grep -q 'depends_on :macos' "$WORK/agentbus2.rb"; then echo "FAIL formula still macOS only"; FAIL=1; fi
# Decision 32: nothing of v1 leaks into the v2 templates.
if grep -hE '(^|[^a-z0-9])agentbus([^2a-z]|$)' "$HERE"/agentbus2.rb.tmpl "$HERE"/agentbus2.scoop.json.tmpl "$HERE"/agentbus2.winget.*.yaml.tmpl | grep -vE 'agentbus2' >/dev/null; then
    echo "FAIL a template names the v1 product (agentbus without the 2)"; FAIL=1
else
    echo "PASS templates name only agentbus2"
fi

# Optional: when brew is installed, the URL-selection checks below run offline.
# brew style needs the network (its first run installs RuboCop gems from
# rubygems.org), so it runs only with AGENTBUS2_BREW_STYLE=1; make verify must
# not depend on the network. brew only treats the file as a formula under a
# Formula/ directory.
if command -v brew >/dev/null 2>&1; then
    mkdir -p "$WORK/Formula" && cp "$WORK/agentbus2.rb" "$WORK/Formula/agentbus2.rb"
    case "${AGENTBUS2_BREW_STYLE:-}" in
    1)
        if brew style "$WORK/Formula/agentbus2.rb" >"$WORK/style.out" 2>&1; then echo "PASS brew style"; else echo "FAIL brew style"; cat "$WORK/style.out"; FAIL=1; fi ;;
    "") echo "SKIP brew style (set AGENTBUS2_BREW_STYLE=1; it needs the network)" ;;
    *) echo "FAIL AGENTBUS2_BREW_STYLE must be 1 or unset, got '${AGENTBUS2_BREW_STYLE}'"; FAIL=1 ;;
    esac
    # Host-conditional URL selection with OS/CPU stubbed (SimulateSystem cannot
    # see host conditionals, so redefine the predicates themselves).
    cat > "$WORK/select.rb" <<'RB'
file, os, cpu = ARGV
mac = os == "mac"
arm = cpu == "arm"
OS.define_singleton_method(:mac?) { mac }
OS.define_singleton_method(:linux?) { !mac }
Hardware::CPU.define_singleton_method(:arm?) { arm }
Hardware::CPU.define_singleton_method(:intel?) { !arm }
require "formula"
puts Formulary.from_contents("agentbus2", Pathname(file), File.read(file)).stable.url
RB
    for combo in "mac arm macos-universal" "mac intel macos-universal" "linux intel linux-amd64" "linux arm linux-arm64"; do
        read -r -a c <<<"$combo"
        got="$(brew ruby "$WORK/select.rb" "$WORK/Formula/agentbus2.rb" "${c[0]}" "${c[1]}" 2>&1 | tail -1)"
        if [[ "$got" == *"-${c[2]}.tar.gz" ]]; then echo "PASS url select ${c[0]}/${c[1]}"; else echo "FAIL url select ${c[0]}/${c[1]}: $got"; FAIL=1; fi
    done
else
    echo "SKIP brew checks (brew not installed)"
fi

# Scoop manifest.
render_check "$HERE/agentbus2.scoop.json.tmpl" agentbus2.json python3 -I -m json.tool -- \
    VERSION=1.2.3 AMD64_URL=$BASE/a.zip AMD64_SHA256=aaaa ARM64_URL=$BASE/r.zip ARM64_SHA256=bbbb
if python3 -I -c "
import json, sys
m = json.load(open(sys.argv[1]))
assert m['version'] == '1.2.3'
assert m['bin'] == 'agentbus2.exe'
assert m['architecture']['64bit']['hash'] == 'aaaa'
assert m['architecture']['arm64']['hash'] == 'bbbb'
assert m['architecture']['arm64']['url'] == '$BASE/r.zip'
assert m['checkver']['github'] == 'https://github.com/ericfitz/agentbus2'
a = m['autoupdate']
assert a['architecture']['64bit']['url'] == 'https://github.com/ericfitz/agentbus2/releases/download/v\$version/agentbus2-v\$version-windows-amd64.zip'
assert a['architecture']['arm64']['url'].endswith('agentbus2-v\$version-windows-arm64.zip')
assert a['hash']['url'].endswith('/SHA256SUMS')
" "$WORK/agentbus2.json"; then echo "PASS scoop fields"; else echo "FAIL scoop fields"; FAIL=1; fi

# winget manifests: no YAML library, so the key lines are matched literally.
VER="$WORK/v.yaml"; INS="$WORK/i.yaml"; LOC="$WORK/l.yaml"
render_check "$HERE/agentbus2.winget.version.yaml.tmpl" v.yaml cat -- VERSION=1.2.3
render_check "$HERE/agentbus2.winget.installer.yaml.tmpl" i.yaml cat -- \
    VERSION=1.2.3 AMD64_URL=$BASE/a.zip AMD64_SHA256=AAAA ARM64_URL=$BASE/r.zip ARM64_SHA256=BBBB
render_check "$HERE/agentbus2.winget.locale.en-US.yaml.tmpl" l.yaml cat -- VERSION=1.2.3
for f in "$VER" "$INS" "$LOC"; do
    has_line "winget $(basename "$f") identifier" "$f" 'PackageIdentifier: ericfitz.agentbus2'
    has_line "winget $(basename "$f") version" "$f" 'PackageVersion: 1.2.3'
    if grep -q '__' "$f"; then echo "FAIL winget $(basename "$f") holds a __ placeholder"; FAIL=1; fi
    has_line "winget $(basename "$f") manifest version" "$f" 'ManifestVersion: 1.12.0'
done
has_line "winget version DefaultLocale" "$VER" 'DefaultLocale: en-US'
has_line "winget version ManifestType" "$VER" 'ManifestType: version'
has_line "winget installer type" "$INS" 'InstallerType: zip'
has_line "winget installer nested type" "$INS" 'NestedInstallerType: portable'
has_line "winget installer relative path" "$INS" '- RelativeFilePath: agentbus2.exe'
has_line "winget installer alias" "$INS" '  PortableCommandAlias: agentbus2'
has_line "winget installer x64" "$INS" '- Architecture: x64'
has_line "winget installer x64 url" "$INS" "  InstallerUrl: $BASE/a.zip"
has_line "winget installer x64 hash" "$INS" '  InstallerSha256: AAAA'
has_line "winget installer arm64" "$INS" '- Architecture: arm64'
has_line "winget installer arm64 url" "$INS" "  InstallerUrl: $BASE/r.zip"
has_line "winget installer arm64 hash" "$INS" '  InstallerSha256: BBBB'
has_line "winget installer ManifestType" "$INS" 'ManifestType: installer'
if [[ "$(grep -c 'InstallerSha256:' "$INS")" == 2 ]]; then echo "PASS winget installer has two hashes"; else echo "FAIL winget installer hash count"; FAIL=1; fi
has_line "winget locale PackageLocale" "$LOC" 'PackageLocale: en-US'
has_line "winget locale Publisher" "$LOC" 'Publisher: ericfitz'
has_line "winget locale PackageName" "$LOC" 'PackageName: agentbus2'
has_line "winget locale License" "$LOC" 'License: Apache-2.0'
has_line "winget locale PackageUrl" "$LOC" 'PackageUrl: https://github.com/ericfitz/agentbus2'
has_line "winget locale ManifestType" "$LOC" 'ManifestType: defaultLocale'
desc="$(sed -n 's/^  desc "\(.*\)"$/\1/p' "$WORK/agentbus2.rb")"
has_line "winget ShortDescription equals the formula desc" "$LOC" "ShortDescription: $desc"

# A template with an extra placeholder must fail (render_template's guard).
printf 'x __UNFILLED__\n' > "$WORK/bad.tmpl"
if (render_template "$WORK/bad.tmpl" "$WORK/bad.out" A=1) 2>/dev/null; then echo "FAIL unfilled placeholder accepted"; FAIL=1; else echo "PASS unfilled placeholder rejected"; fi

[[ "$FAIL" -eq 0 ]] && echo "test-render: OK"
exit "$FAIL"
