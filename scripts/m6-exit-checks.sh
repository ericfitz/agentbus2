#!/usr/bin/env bash
# Milestone 6 exit evidence.
#   usage: m6-exit-checks.sh <e2e.log>     counts, word check, import rule
#          m6-exit-checks.sh --deps-only   import rule alone (Docker-free)
# <e2e.log> is a `go test -count=1 -v ./internal/e2e/` log. It must exist and be
# non-empty (exit 2 naming the path). The script prints
#   sequence=<n>   the expected steps with a leaf `--- PASS: TestSection24Sequence/<step>` line
#   exitcodes=<n>  the expected cells with a leaf `--- PASS: TestExitCodeTable/<cell>` line
#   fail=<n>       every `--- FAIL` line
#   pkg_ok=<0|1>   1 when the package's `ok` line is present and no `FAIL` line is
#                  (TestMain's teardown checks, such as the server's exit 0 after
#                  SIGTERM, fail the package without a `--- FAIL` line)
# and exits 1 unless fail=0, sequence=13, exitcodes=11 and pkg_ok=1. Each step
# and cell is counted once, by name, so a repeated line cannot stand in for a
# missing one. Then the word check:
# the agent-facing texts (the skill, the MCP tool inputs and server, the hooks)
# must not mention a v1 concept v2 dropped (DMs, the -filter flag, a mention
# marker) or a bare channel-kind path; it prints words_ok=1 or each hit.
#
# The import rule is checked on the DIRECT imports of non-test code (`go list`
# .Imports), each output line "<package> <import>" matched against the group's
# forbidden list. A transitive check (`go list -deps`) is deliberately not used:
# it would always find internal/gen and internal/store/sqlite under
# internal/client and prove nothing.
#   leaf group ./internal/profile ./internal/repoconfig ./internal/credential
#              ./internal/procs ./internal/sessionstate
#       must not import internal/core, internal/server, internal/bus/local or
#       internal/store/{sqlite,postgres,sqlstore}
#   consumer group ./internal/mcpserver ./internal/cli
#       must not import internal/gen or internal/store (and below)
#   sign-in group ./internal/oidcclient ./internal/oidctest
#       import no package of this module (oidcclient is stdlib-only and oidctest
#       stdlib plus go-jose by convention: the direct-import check cannot tell a
#       stdlib path from a third-party one cheaply)
# Prints deps_ok=1 when nothing matched; otherwise one
# deps_violation=<package> imports <forbidden> line per hit and exits 1.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MOD=github.com/ericfitz/agentbus2
WANT_SEQUENCE=13
WANT_EXITCODES=11
# The names of internal/e2e's TestSection24Sequence steps and TestExitCodeTable
# cells (scripts/check-gates-selftest.sh keeps the same lists).
STEPS=(0-server 1-register 2-token 3-open-session 4-watch 5-send-from-another-identity 6-wake 7-receive 8-ack 9-stale-bearer-refreshes 10-mcp-over-stdio 11-stop-hook 12-watch-ends-only-by-signal)
CELLS=(wait-0-woke wait-1-timeout wait-2-error wait-3-replaced wait-3-signal wait-4-harness-gone watch-2-error watch-3-replaced watch-3-signal watch-4-harness-gone watch-never-0-or-1)

violations=0

# check_group "PACKAGES" "FORBIDDEN-ERE": lists every direct import of every
# package and reports each import column that matches the anchored expression.
check_group() {
	local patterns="$1" forbidden="$2" listing line pkg imp
	# shellcheck disable=SC2086,SC2016 # patterns is a space-separated package list; the template is for go list
	listing="$(cd "$ROOT" && go list -f '{{$p := .ImportPath}}{{range .Imports}}{{$p}} {{.}}{{"\n"}}{{end}}' $patterns)"
	while IFS= read -r line; do
		[ -n "$line" ] || continue
		pkg="${line%% *}"
		imp="${line#* }"
		if printf '%s\n' "$imp" | grep -E -q -- "$forbidden"; then
			echo "deps_violation=$pkg imports $imp"
			violations=$((violations + 1))
		fi
	done <<<"$listing"
}

import_check() {
	check_group './internal/profile ./internal/repoconfig ./internal/credential ./internal/procs ./internal/sessionstate' \
		"^$MOD/internal/(core|server)(/|\$)|^$MOD/internal/bus/local\$|^$MOD/internal/store/(sqlite|postgres|sqlstore)(/|\$)"
	check_group './internal/mcpserver ./internal/cli' \
		"^$MOD/internal/(gen|store)(/|\$)"
	check_group './internal/oidcclient ./internal/oidctest' "^$MOD/"
	if [ "$violations" -ne 0 ]; then
		return 1
	fi
	echo "deps_ok=1"
}

if [ "$#" -eq 1 ] && [ "$1" = "--deps-only" ]; then
	import_check
	exit $?
fi
if [ "$#" -ne 1 ]; then
	echo "usage: $0 <e2e.log> | --deps-only" >&2
	exit 2
fi
log="$1"
if [ ! -s "$log" ]; then
	echo "m6-exit-checks: log '$log' is missing or empty" >&2
	exit 2
fi
# passed TEST NAME...: how many of the names have a leaf PASS line under TEST.
passed() {
	local test="$1" n=0 name
	shift
	for name in "$@"; do
		if grep -qE "^[[:space:]]+--- PASS: $test/$name \\(" "$log"; then
			n=$((n + 1))
		fi
	done
	echo "$n"
}
sequence="$(passed TestSection24Sequence "${STEPS[@]}")"
exitcodes="$(passed TestExitCodeTable "${CELLS[@]}")"
fail="$(grep -c -E '^[[:space:]]*--- FAIL' "$log" || true)"
pkg_ok=0
if grep -qE '^ok[[:space:]]+github\.com/ericfitz/agentbus2/internal/e2e([[:space:]]|$)' "$log" && ! grep -qE '^FAIL([[:space:]]|$)' "$log"; then
	pkg_ok=1
fi
echo "sequence=$sequence"
echo "exitcodes=$exitcodes"
echo "fail=$fail"
echo "pkg_ok=$pkg_ok"
rc=0
if [ "$fail" -ne 0 ] || [ "$sequence" -ne "$WANT_SEQUENCE" ] || [ "$exitcodes" -ne "$WANT_EXITCODES" ] || [ "$pkg_ok" -ne 1 ]; then
	echo "m6-exit-checks: want fail=0, sequence=$WANT_SEQUENCE, exitcodes=$WANT_EXITCODES and pkg_ok=1" >&2
	rc=1
fi

words=(
	internal/cli/skills/using-agentbus2/SKILL.md
	internal/mcpserver/inputs.go
	internal/mcpserver/server.go
	internal/cli/stophook.go
	internal/cli/subagenthook.go
)
hits=""
for f in "${words[@]}"; do
	if [ ! -f "$ROOT/$f" ]; then
		echo "m6-exit-checks: word-check file '$f' is missing" >&2
		rc=1
		continue
	fi
	# 0 is a hit, 1 none; any other grep exit means the check could not run.
	grc=0
	out="$(grep -n -E 'dm/|-filter|@<|memory/|tasks/|general/' "$ROOT/$f" 2>&1)" || grc=$?
	if [ "$grc" -gt 1 ]; then
		echo "m6-exit-checks: grep failed on '$f' (exit $grc): $out" >&2
		rc=2
		continue
	fi
	if [ "$grc" -eq 0 ]; then
		hits="$hits$(printf '%s\n' "$out" | sed "s|^|$f:|")"$'\n'
	fi
done
if [ -n "$hits" ]; then
	printf 'words_hit:\n%s' "$hits"
	rc=1
else
	echo "words_ok=1"
fi

import_check || rc=1
exit "$rc"
