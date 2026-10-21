#!/usr/bin/env bash
# Milestone 7 exit evidence.
#   usage: m7-exit-checks.sh <replicas.log>   counts, then import rule
#          m7-exit-checks.sh --deps-only      import rule alone (Docker-free)
# <replicas.log> is a `go test -v -run '^TestReplicas'
# ./internal/conformance/bus/` log. It must exist and be non-empty (exit 2
# naming the path). The script prints fail=<n> (every `--- FAIL` line),
# wake_sqlite=<n> and wake_postgres=<n> (the leaf `--- PASS:
# TestReplicasWake/<engine>/<leaf>` lines) and maint_sqlite=<n> and
# maint_postgres=<n> (likewise for TestReplicasMaintenanceExclusive), and
# exits 1 unless fail=0, wake_postgres=6, wake_sqlite=4 (the two cross-server
# leaves exist on postgres only: two servers over one SQLite file share no
# hints) and both maint counts are 2.
#
# The import rule is checked on production imports (`go list -deps` without
# -test), for every package a pattern matches:
#   ./internal/store/...             must not import internal/core
#   ./internal/core/notify           must not import internal/store or
#                                    github.com/jackc/pgx
#   ./internal/core/notify/pgnotify  must not import internal/server or
#                                    internal/gen
# A forbidden entry also forbids every package below it (internal/core covers
# internal/core/notify; github.com/jackc/pgx covers github.com/jackc/pgx/v5).
# Prints deps_ok=1 when nothing matched; otherwise one
# deps_violation=<package> imports <forbidden> line per hit and exits 1.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MOD=github.com/ericfitz/agentbus2

# check_deps PATTERN FORBIDDEN...: an entry is an in-module path (relative to
# the module) or, when it starts with "github.com/", a third-party path; each
# is matched as a whole line of `go list -deps` or as the prefix "<entry>/" of
# one. Sets the global violations.
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
			else
				dep="$MOD/$f"
			fi
			if printf '%s\n' "$deps" | grep -F -x -q -- "$dep" || printf '%s\n' "$deps" | grep -F -q -- "$dep/"; then
				echo "deps_violation=$pkg imports $dep"
				violations=$((violations + 1))
			fi
		done
	done
}

import_check() {
	check_deps './internal/store/...' internal/core
	check_deps ./internal/core/notify internal/store github.com/jackc/pgx
	check_deps ./internal/core/notify/pgnotify internal/server internal/gen
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
	echo "usage: $0 <replicas.log> | --deps-only" >&2
	exit 2
fi
log="$1"
if [ ! -s "$log" ]; then
	echo "m7-exit-checks: log '$log' is missing or empty" >&2
	exit 2
fi
count() { grep -c -E "^[[:space:]]+--- PASS: $1/$2/" "$log" || true; }
fail="$(grep -c -E '^[[:space:]]+--- FAIL' "$log" || true)"
wake_sqlite="$(count TestReplicasWake sqlite)"
wake_postgres="$(count TestReplicasWake postgres)"
maint_sqlite="$(count TestReplicasMaintenanceExclusive sqlite)"
maint_postgres="$(count TestReplicasMaintenanceExclusive postgres)"
echo "fail=$fail"
echo "wake_sqlite=$wake_sqlite"
echo "wake_postgres=$wake_postgres"
echo "maint_sqlite=$maint_sqlite"
echo "maint_postgres=$maint_postgres"
rc=0
if [ "$fail" -ne 0 ] || [ "$wake_sqlite" -ne 4 ] || [ "$wake_postgres" -ne 6 ] || [ "$maint_sqlite" -ne 2 ] || [ "$maint_postgres" -ne 2 ]; then
	echo "m7-exit-checks: want fail=0, wake_sqlite=4, wake_postgres=6, maint_sqlite=2 and maint_postgres=2" >&2
	rc=1
fi
import_check || rc=1
exit "$rc"
