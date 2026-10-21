#!/usr/bin/env bash
# Counts the Milestone 3 exit evidence in a `go test -v` log of
# ./internal/conformance/bus/ and exits 1 unless every count is as required.
#   usage: m3-exit-counts.sh <log>
# Prints fail=, matrix_sqlite=, matrix_postgres=, search_agree= and
# concurrent_send= lines. Required: fail=0, both matrix counts 52 (26 sub-cases
# x refusal and success per engine), search_agree=2, concurrent_send=2.
set -euo pipefail

if [ "$#" -ne 1 ]; then
	echo "usage: $0 <go test -v log>" >&2
	exit 2
fi
log="$1"
if [ ! -s "$log" ]; then
	echo "m3-exit-counts: log '$log' is missing or empty" >&2
	exit 2
fi
if ! grep -Eq '^(ok|FAIL)[[:space:]]' "$log"; then
	echo "m3-exit-counts: log '$log' has no 'ok' or 'FAIL' package line" >&2
	exit 2
fi

# count REGEX: matching lines, 0 when none (grep -c exits 1 on zero).
count() { grep -Ec -- "$1" "$log" || true; }

fail=$(count '^[[:space:]]+--- FAIL')
matrix_sqlite=$(count '^[[:space:]]+--- PASS: TestACLMatrix/sqlite/.*/(refusal|success) \([0-9.]+s\)$')
matrix_postgres=$(count '^[[:space:]]+--- PASS: TestACLMatrix/postgres/.*/(refusal|success) \([0-9.]+s\)$')
search_agree=$(count '^[[:space:]]+--- PASS: TestSearchMatchesAgreeAcrossEngines/(sqlite|postgres) \([0-9.]+s\)$')
concurrent_send=$(count '^[[:space:]]+--- PASS: TestSendConcurrentSeqOrder/(sqlite|postgres) \([0-9.]+s\)$')

echo "fail=$fail"
echo "matrix_sqlite=$matrix_sqlite"
echo "matrix_postgres=$matrix_postgres"
echo "search_agree=$search_agree"
echo "concurrent_send=$concurrent_send"

[ "$fail" -eq 0 ] && [ "$matrix_sqlite" -eq 52 ] && [ "$matrix_postgres" -eq 52 ] &&
	[ "$search_agree" -eq 2 ] && [ "$concurrent_send" -eq 2 ]
