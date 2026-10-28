#!/usr/bin/env bash
# Milestone 5 exit evidence.
#   usage: m5-exit-checks.sh <auth.log>    counts, then import rule
#          m5-exit-checks.sh --deps-only   import rule alone (Docker-free)
# <auth.log> is a `go test -v -run '^TestAuthEndToEnd$' ./internal/conformance/bus/`
# log. It must exist and be non-empty (exit 2 naming the path). The script
# prints auth_sqlite=<n> and auth_postgres=<n> (the leaf `--- PASS:
# TestAuthEndToEnd/<engine>/<subtest>` lines) and fail=<n> (every `--- FAIL`
# line), and exits 1 unless fail=0 and both counts are exactly 12.
#
# The import rule is checked on production imports (`go list -deps` without
# -test), for every package a pattern matches:
#   ./internal/auth/...
#       must not import internal/store, internal/core, internal/server,
#       internal/gen or internal/bus, and imports nothing outside the
#       standard library, internal/auth/... and github.com/go-jose/go-jose/v4
#       (Global Constraints: "only the standard library and go-jose")
#   ./internal/bus/remote
#       must not import internal/auth/token (the server side of
#       authentication never ships in a client)
#   ./internal/core/... ./internal/bus ./internal/bus/local
#       must not import internal/server or internal/gen
# A forbidden in-module entry also forbids every package below it
# (internal/server covers internal/server/config).
# Prints deps_ok=1 when nothing matched; otherwise one
# deps_violation=<package> imports <forbidden> line per hit and exits 1.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MOD=github.com/ericfitz/agentbus2
WANT=12

# check_deps PATTERN FORBIDDEN...: each forbidden entry is an in-module path
# (relative to the module), matched as a whole line of `go list -deps` or as
# the prefix "<entry>/" of one. Sets the global violations.
violations=0
check_deps() {
	local pattern="$1" pkg dep f
	shift
	local pkgs deps
	pkgs="$(cd "$ROOT" && go list "$pattern")" || {
		echo "m5-exit-checks: go list $pattern failed" >&2
		exit 2
	}
	for pkg in $pkgs; do
		deps="$(cd "$ROOT" && go list -deps "$pkg")" || {
			echo "m5-exit-checks: go list -deps $pkg failed" >&2
			exit 2
		}
		for f in "$@"; do
			dep="$MOD/$f"
			if printf '%s\n' "$deps" | grep -F -x -q -- "$dep" || printf '%s\n' "$deps" | grep -F -q -- "$dep/"; then
				echo "deps_violation=$pkg imports $dep"
				violations=$((violations + 1))
			fi
		done
	done
}

# check_allowlist PATTERN ALLOWED...: every non-standard production dependency
# of each package PATTERN matches must equal an allowed entry or lie below it
# ("<entry>/..."). Entries are full import paths. Sets the global violations.
check_allowlist() {
	local pattern="$1" pkg dep a ok
	shift
	local pkgs deps
	pkgs="$(cd "$ROOT" && go list "$pattern")" || {
		echo "m5-exit-checks: go list $pattern failed" >&2
		exit 2
	}
	for pkg in $pkgs; do
		deps="$(cd "$ROOT" && go list -deps -f '{{if not .Standard}}{{.ImportPath}}{{end}}' "$pkg")" || {
			echo "m5-exit-checks: go list -deps $pkg failed" >&2
			exit 2
		}
		for dep in $deps; do
			ok=0
			for a in "$@"; do
				case "$dep" in
				"$a" | "$a"/*) ok=1 ;;
				esac
			done
			if [ "$ok" -eq 0 ]; then
				echo "deps_violation=$pkg imports $dep"
				violations=$((violations + 1))
			fi
		done
	done
}

import_check() {
	check_deps './internal/auth/...' internal/store internal/core internal/server internal/gen internal/bus
	check_allowlist './internal/auth/...' "$MOD/internal/auth" github.com/go-jose/go-jose/v4
	check_deps ./internal/bus/remote internal/auth/token
	check_deps './internal/core/...' internal/server internal/gen
	check_deps ./internal/bus internal/server internal/gen
	check_deps ./internal/bus/local internal/server internal/gen
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
	echo "usage: $0 <auth.log> | --deps-only" >&2
	exit 2
fi
log="$1"
if [ ! -s "$log" ]; then
	echo "m5-exit-checks: log '$log' is missing or empty" >&2
	exit 2
fi
sqlite="$(grep -c -E '^[[:space:]]+--- PASS: TestAuthEndToEnd/sqlite/' "$log" || true)"
postgres="$(grep -c -E '^[[:space:]]+--- PASS: TestAuthEndToEnd/postgres/' "$log" || true)"
fail="$(grep -c -E '^[[:space:]]*--- FAIL' "$log" || true)"
echo "auth_sqlite=$sqlite"
echo "auth_postgres=$postgres"
echo "fail=$fail"
rc=0
if [ "$fail" -ne 0 ] || [ "$sqlite" -ne "$WANT" ] || [ "$postgres" -ne "$WANT" ]; then
	echo "m5-exit-checks: want fail=0, auth_sqlite=$WANT and auth_postgres=$WANT" >&2
	rc=1
fi
import_check || rc=1
exit "$rc"
