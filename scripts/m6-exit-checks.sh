#!/usr/bin/env bash
# Milestone 6 exit evidence.
#   usage: m6-exit-checks.sh <e2e.log>     counts, word check, import rule
#          m6-exit-checks.sh --deps-only   import rule alone (Docker-free)
# <e2e.log> is a `go test -count=1 -v ./internal/e2e/` log. It must exist and be
# non-empty (exit 2 naming the path). The script prints
#   sequence=<n>   the leaf `--- PASS: TestSection24Sequence/<step>` lines
#   exitcodes=<n>  the leaf `--- PASS: TestExitCodeTable/<cell>` lines
#   fail=<n>       every `--- FAIL` line
# and exits 1 unless fail=0, sequence=13 and exitcodes=11. Then the word check:
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
# Prints deps_ok=1 when nothing matched; otherwise one
# deps_violation=<package> imports <forbidden> line per hit and exits 1.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MOD=github.com/ericfitz/agentbus2
WANT_SEQUENCE=13
WANT_EXITCODES=11

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
sequence="$(grep -c -E '^[[:space:]]+--- PASS: TestSection24Sequence/' "$log" || true)"
exitcodes="$(grep -c -E '^[[:space:]]+--- PASS: TestExitCodeTable/' "$log" || true)"
fail="$(grep -c -E '^[[:space:]]*--- FAIL' "$log" || true)"
echo "sequence=$sequence"
echo "exitcodes=$exitcodes"
echo "fail=$fail"
rc=0
if [ "$fail" -ne 0 ] || [ "$sequence" -ne "$WANT_SEQUENCE" ] || [ "$exitcodes" -ne "$WANT_EXITCODES" ]; then
	echo "m6-exit-checks: want fail=0, sequence=$WANT_SEQUENCE and exitcodes=$WANT_EXITCODES" >&2
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
	out="$(cd "$ROOT" && grep -n -E 'dm/|-filter|@<|memory/|tasks/|general/' "$f" || true)"
	if [ -n "$out" ]; then
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
