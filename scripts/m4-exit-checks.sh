#!/usr/bin/env bash
# Milestone 4 exit evidence.
#   usage: m4-exit-checks.sh <local.log> <inproc.log>   counts, then import rule
#          m4-exit-checks.sh --deps-only                import rule alone (Docker-free)
# The two logs are `go test -v` logs of ./internal/conformance/bus/ run with
# AGENTBUS2_BUS_TRANSPORT=local and =inproc. Each must exist and be non-empty
# (exit 2 naming the path) and pass scripts/m3-exit-counts.sh (fail=0,
# matrix_sqlite=52, matrix_postgres=52, search_agree=2, concurrent_send=2).
#
# The import rule (Decision 47) is checked on production imports (`go list
# -deps` without -test), for every package a pattern matches:
#   ./internal/bus ./internal/core/... ./internal/bus/local
#       must not import internal/gen, internal/gen/spec, internal/server
#   ./internal/bus/remote
#       must not import internal/server, internal/core, internal/gen/spec,
#       or any package of github.com/getkin/kin-openapi
#   ./internal/server/...
#       must not import internal/bus/local or internal/bus/remote
# Prints deps_ok=1 when nothing matched; otherwise one
# deps_violation=<package> imports <forbidden> line per hit and exits 1.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MOD=github.com/ericfitz/agentbus2

# check_deps PATTERN FORBIDDEN...: a forbidden entry is an in-module path
# (relative to the module, matched as a whole line) or, when it starts with
# "github.com/", a third-party module (the module path itself or any package
# below it, because go list -deps lists packages, never the bare module).
# Sets the global violations.
violations=0
check_deps() {
	local pattern="$1" pkg dep f
	shift
	local pkgs deps
	pkgs="$(cd "$ROOT" && go list "$pattern")"
	for pkg in $pkgs; do
		deps="$(cd "$ROOT" && go list -deps "$pkg")"
		for f in "$@"; do
			if [[ "$f" == github.com/* ]]; then
				dep="$f"
				if printf '%s\n' "$deps" | grep -F -x -q -- "$dep" || printf '%s\n' "$deps" | grep -F -q -- "$dep/"; then
					echo "deps_violation=$pkg imports $dep"
					violations=$((violations + 1))
				fi
			else
				dep="$MOD/$f"
				if printf '%s\n' "$deps" | grep -F -x -q -- "$dep"; then
					echo "deps_violation=$pkg imports $dep"
					violations=$((violations + 1))
				fi
			fi
		done
	done
}

import_check() {
	local p
	for p in ./internal/bus ./internal/core/... ./internal/bus/local; do
		check_deps "$p" internal/gen internal/gen/spec internal/server
	done
	check_deps ./internal/bus/remote internal/server internal/core internal/gen/spec github.com/getkin/kin-openapi
	check_deps './internal/server/...' internal/bus/local internal/bus/remote
	if [ "$violations" -ne 0 ]; then
		return 1
	fi
	echo "deps_ok=1"
}

if [ "$#" -eq 1 ] && [ "$1" = "--deps-only" ]; then
	import_check
	exit $?
fi
if [ "$#" -ne 2 ]; then
	echo "usage: $0 <local.log> <inproc.log> | --deps-only" >&2
	exit 2
fi
for log in "$1" "$2"; do
	if [ ! -s "$log" ]; then
		echo "m4-exit-checks: log '$log' is missing or empty" >&2
		exit 2
	fi
done
for log in "$1" "$2"; do
	echo "== $log"
	bash "$ROOT/scripts/m3-exit-counts.sh" "$log"
done
import_check
