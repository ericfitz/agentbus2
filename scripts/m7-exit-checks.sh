#!/usr/bin/env bash
# Milestone 7 exit evidence and the Slack bridge import rules.
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
# deps_violation=<package> imports <forbidden> [rule <name>] line per hit and
# exits 1. A go list failure exits 2. <name> is the rule in the lists here
# (store-no-core, notify-leaf, pgnotify-no-server, slackapi-allowlist,
# slack-go-importers, slackbridge-isolation, slack-consumers); the self-test
# (scripts/check-gates-selftest.sh) requires each probe to trip exactly one.
#
# The Slack bridge rules (same output):
#   ./internal/slackapi              imports only the standard library and
#                                    itself (allowlist)
#   ./internal/slackapi/slackfake    imports only the standard library and
#                                    internal/slackapi (allowlist)
#   github.com/slack-go/slack        is imported (directly) only by
#                                    internal/slackapi/slackgo, in any file:
#                                    production, _test.go (internal and
#                                    external) and every GOOS (linux, darwin,
#                                    windows). The allowlists above already
#                                    bar slackapi and slackfake from core,
#                                    store, server, bus and slack-go, so no
#                                    separate rule repeats that.
#   ./internal/slackbridge           never imports internal/server,
#                                    internal/gen, internal/slackapi/slackgo
#                                    or slack-go
#   ./internal/core/..., ./internal/bus/..., ./internal/cli,
#   ./internal/mcpserver, ./internal/client, ./internal/server
#                                    never import internal/slackapi,
#                                    internal/slackbridge or slackapi/slackgo
#                                    (slack-go itself: the importer rule)
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MOD=github.com/ericfitz/agentbus2

# check_deps RULE PATTERN FORBIDDEN...: an entry is an in-module path (relative to
# the module) or, when it starts with "github.com/", a third-party path; each
# is matched as a whole line of `go list -deps` or as the prefix "<entry>/" of
# one. Sets the global violations.
violations=0
check_deps() {
	local rule="$1" pattern="$2" pkg dep f
	shift 2
	local pkgs deps
	# Explicit exits: set -e is off inside `import_check || rc=1`, and a go
	# list failure must never read as a package with no imports.
	pkgs="$(cd "$ROOT" && go list "$pattern")" || {
		echo "m7-exit-checks: go list $pattern failed" >&2
		exit 2
	}
	for pkg in $pkgs; do
		deps="$(cd "$ROOT" && go list -deps "$pkg")" || {
			echo "m7-exit-checks: go list -deps $pkg failed" >&2
			exit 2
		}
		for f in "$@"; do
			if [[ "$f" == github.com/* ]]; then
				dep="$f"
			else
				dep="$MOD/$f"
			fi
			if printf '%s\n' "$deps" | grep -F -x -q -- "$dep" || printf '%s\n' "$deps" | grep -F -q -- "$dep/"; then
				echo "deps_violation=$pkg imports $dep [rule $rule]"
				violations=$((violations + 1))
			fi
		done
	done
}

# check_allowlist RULE PATTERN ALLOWED...: every non-standard production dependency
# of each package PATTERN matches must equal an allowed entry or lie below it
# ("<entry>/..."). Entries are full import paths. Sets the global violations.
check_allowlist() {
	local rule="$1" pattern="$2" pkg dep a ok
	shift 2
	local pkgs deps
	pkgs="$(cd "$ROOT" && go list "$pattern")" || {
		echo "m7-exit-checks: go list $pattern failed" >&2
		exit 2
	}
	for pkg in $pkgs; do
		deps="$(cd "$ROOT" && go list -deps -f '{{if not .Standard}}{{.ImportPath}}{{end}}' "$pkg")" || {
			echo "m7-exit-checks: go list -deps $pkg failed" >&2
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
				echo "deps_violation=$pkg imports $dep [rule $rule]"
				violations=$((violations + 1))
			fi
		done
	done
}

# check_importers RULE IMPORT ALLOWED_PKG: only ALLOWED_PKG (a full import
# path) may import IMPORT (or a package below it) directly; every package of
# ./... is scanned for each of GOOS linux, darwin and windows (CGO off), and
# for its production, test and external-test files alike. A package is
# reported once however many GOOS values show it. Sets the global violations.
check_importers() {
	local rule="$1" target="$2" allowed="$3" goos lines line pkg imp rest
	local seen=" "
	for goos in linux darwin windows; do
		lines="$(cd "$ROOT" && GOOS="$goos" CGO_ENABLED=0 go list -f '{{.ImportPath}} {{join .Imports " "}} {{join .TestImports " "}} {{join .XTestImports " "}}' ./...)" || {
			echo "m7-exit-checks: GOOS=$goos go list ./... failed" >&2
			exit 2
		}
		while IFS= read -r line; do
			pkg="${line%% *}"
			rest="${line#"$pkg"}"
			[ "$pkg" = "$allowed" ] && continue
			case "$seen" in *" $pkg "*) continue ;; esac
			for imp in $rest; do
				case "$imp" in
				"$target" | "$target"/*)
					echo "deps_violation=$pkg imports $target [rule $rule]"
					violations=$((violations + 1))
					seen="$seen$pkg "
					break
					;;
				esac
			done
		done <<<"$lines"
	done
}

import_check() {
	check_deps store-no-core './internal/store/...' internal/core
	check_deps notify-leaf ./internal/core/notify internal/store github.com/jackc/pgx
	check_deps pgnotify-no-server ./internal/core/notify/pgnotify internal/server internal/gen
	check_allowlist slackapi-allowlist ./internal/slackapi "$MOD/internal/slackapi"
	check_allowlist slackapi-allowlist ./internal/slackapi/slackfake "$MOD/internal/slackapi"
	check_importers slack-go-importers github.com/slack-go/slack "$MOD/internal/slackapi/slackgo"
	check_deps slackbridge-isolation ./internal/slackbridge internal/server internal/gen internal/slackapi/slackgo github.com/slack-go/slack
	local p
	for p in './internal/core/...' './internal/bus/...' ./internal/cli ./internal/mcpserver ./internal/client ./internal/server; do
		check_deps slack-consumers "$p" internal/slackapi internal/slackbridge internal/slackapi/slackgo
	done
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
