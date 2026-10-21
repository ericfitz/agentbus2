#!/usr/bin/env bash
# Milestone 8 exit evidence.
#   usage: m8-exit-checks.sh <tui.log>   counts, word check, then import rule
#          m8-exit-checks.sh --deps-only import rule alone (Docker-free)
# <tui.log> is a `go test -v -run '^(TestBothModes|TestNoInboxView|TestRunTUI.*)$'
# ./internal/tui/ ./internal/cli/` log. It must exist and be non-empty (exit 2 naming the
# path). The script prints modes=<n> (the TestBothModes local and server
# subtests), noinbox=<n>, cli=<n> (TestRunTUILocalProfile and
# TestRunTUIServerProfile) and fail=<n> (every --- FAIL line), and exits 1
# unless fail=0, modes=2, noinbox=1 and cli=2.
#
# The word check: no non-test Go file under internal/tui may use the words
# of v1's direct-message and merged-view design (comments included), and
# neither may internal/cli/tui.go. Test files may spell them when they assert
# their absence. words_ok=1 when nothing matches; otherwise the matches are
# printed and the exit is 1. A word check that cannot run (a path missing,
# grep failing) exits 2.
#
# The import rule (spec 2.3, tui --> busif): the direct imports of
# ./internal/tui are an allowlist: of the module's own packages only
# internal/bus and internal/version, and nothing from kin-openapi. A hit
# prints deps_violation=<package> imports <import> and exits 1; none prints
# deps_ok=1. A go list failure exits 2.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MOD=github.com/ericfitz/agentbus2
PKG="$MOD/internal/tui"

import_check() {
	local imports imp violations=0
	if ! imports="$(cd "$ROOT" && go list -f '{{join .Imports "\n"}}' ./internal/tui 2>&1)"; then
		echo "m8-exit-checks: go list failed: $imports" >&2
		return 2
	fi
	for imp in $imports; do
		case "$imp" in
		"$MOD/internal/bus" | "$MOD/internal/version") ;;
		"$MOD"/* | github.com/getkin/kin-openapi*)
			echo "deps_violation=$PKG imports $imp"
			violations=$((violations + 1))
			;;
		esac
	done
	if [ "$violations" -ne 0 ]; then
		return 1
	fi
	echo "deps_ok=1"
}

if [ "$#" -eq 1 ] && [ "$1" = "--deps-only" ]; then
	rc=0
	import_check || rc=$?
	exit "$rc"
fi
if [ "$#" -ne 1 ] || [ "$1" = "--deps-only" ]; then
	echo "usage: $0 <tui.log> | --deps-only" >&2
	exit 2
fi
log="$1"
if [ ! -s "$log" ]; then
	echo "m8-exit-checks: log '$log' is missing or empty" >&2
	exit 2
fi

count() { grep -c -E "$1" "$log" || true; }
modes="$(count '^[[:space:]]+--- PASS: TestBothModes/(local|server) \([0-9.]+s\)$')"
noinbox="$(count '^--- PASS: TestNoInboxView \([0-9.]+s\)$')"
cli="$(count '^--- PASS: TestRunTUI(LocalProfile|ServerProfile) \([0-9.]+s\)$')"
fail="$(count '^[[:space:]]*--- FAIL')"
echo "modes=$modes"
echo "noinbox=$noinbox"
echo "cli=$cli"
echo "fail=$fail"
rc=0
if [ "$fail" -ne 0 ] || [ "$modes" -ne 2 ] || [ "$noinbox" -ne 1 ] || [ "$cli" -ne 2 ]; then
	rc=1
fi

# Portable ERE through grep alone (no rg, no \b): a tool that is missing or
# fails, or a path that is gone, is exit 2, never words_ok=1.
word_re='(^|[^A-Za-z0-9_])dm/|DMPrefix|DMChannel|[Ii][Nn][Bb][Oo][Xx]|IsTaskChannel|tui_name'
word_files=()
for p in "$ROOT/internal/tui" "$ROOT/internal/cli/tui.go"; do
	if [ ! -e "$p" ]; then
		echo "m8-exit-checks: word check path $p is missing" >&2
		exit 2
	fi
done
while IFS= read -r f; do
	word_files+=("$f")
done < <(find "$ROOT/internal/tui" -type f -name '*.go' ! -name '*_test.go' | sort)
if [ "${#word_files[@]}" -eq 0 ]; then
	echo "m8-exit-checks: no non-test Go file under $ROOT/internal/tui" >&2
	exit 2
fi
word_files+=("$ROOT/internal/cli/tui.go")
words_rc=0
words="$(grep -n -H -E -- "$word_re" "${word_files[@]}")" || words_rc=$?
case "$words_rc" in
0)
	echo "$words"
	rc=1
	;;
1) echo "words_ok=1" ;;
*)
	echo "m8-exit-checks: the word check failed (grep exit $words_rc)" >&2
	exit 2
	;;
esac

imp_rc=0
import_check || imp_rc=$?
if [ "$imp_rc" -ne 0 ]; then
	rc="$imp_rc"
fi
exit "$rc"
