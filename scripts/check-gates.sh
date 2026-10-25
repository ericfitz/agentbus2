#!/usr/bin/env bash
# Proves the negative halves of the done gate. Each check prints exactly
# "ok  <name>" or "FAIL <name>: <why>" (scripts/check-gates-selftest.sh greps
# these lines, so the format is a contract). Exit 1 when any check failed.
#
# The script never reads the ambient AGENTBUS2_TEST_POSTGRES_DSN: checks 1 and
# 2 set their own value, checks 3 and 4 touch no database, check 5 runs in a
# container that inherits no host environment. Only check 5 needs Docker on the
# host (checks 3 and 4 need none, nor does check 6: the bus suite exits on an
# unknown AGENTBUS2_BUS_TRANSPORT before dbtest.Run starts anything). All scratch files
# live in a temp dir removed on exit; nothing is written under the repository.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

BAD_DSN='postgres://agentbus2:agentbus2@127.0.0.1:1/agentbus2?sslmode=disable&connect_timeout=2'
failed=0

pass() { echo "ok  $1"; }
fail() {
	echo "FAIL $1: $2"
	failed=1
}

# has FILE TEXT: fixed-string search.
has() { grep -qF -- "$2" "$1"; }

# --- 1. postgres-unreachable-fails ---------------------------------------
check_unreachable() {
	local name=postgres-unreachable-fails log="$tmp/conf.log"
	if AGENTBUS2_TEST_POSTGRES_DSN="$BAD_DSN" go test -v -count=1 -p 1 -timeout 120s ./internal/conformance/... >"$log" 2>&1; then
		fail "$name" "suites passed against an unreachable Postgres"
		return
	fi
	has "$log" '--- FAIL: TestScaffold/postgres' || { fail "$name" "log lacks '--- FAIL: TestScaffold/postgres'"; return; }
	has "$log" '--- PASS: TestScaffold/sqlite' || { fail "$name" "log lacks '--- PASS: TestScaffold/sqlite'"; return; }
	has "$log" 'postgres from AGENTBUS2_TEST_POSTGRES_DSN is unreachable' || { fail "$name" "log lacks the unreachable text"; return; }
	if has "$log" '--- SKIP'; then
		fail "$name" "log contains '--- SKIP'"
		return
	fi
	pass "$name"
}

# --- 2. unit-tests-exclude-conformance -----------------------------------
check_unit_excludes() {
	local name=unit-tests-exclude-conformance log="$tmp/unit.log"
	if AGENTBUS2_TEST_POSTGRES_DSN="$BAD_DSN" make test >"$log" 2>&1; then
		pass "$name"
	else
		fail "$name" "make test failed with an unreachable Postgres (conformance leaked into the unit set?): $({ grep -E '^\s*--- FAIL|^FAIL|^panic' "$log" || true; tail -n 5 "$log"; } | tr '\n' ' ')"
	fi
}

# --- 3. no-skip-in-test-wiring -------------------------------------------
# Call sites only, on any receiver. Limits: /* */ comments are not stripped
# (the two trees use none) and a // inside a string literal truncates its line.
check_no_skip() {
	local name=no-skip-in-test-wiring hits="" f out
	while IFS= read -r f; do
		out="$(sed -E 's#//.*$##' "$f" | grep -nE '\.Skip(f|Now)?\(' || true)"
		if [ -n "$out" ]; then
			hits="$hits$(printf '%s\n' "$out" | sed "s#^#$f:#")"$'\n'
		fi
	done < <(find internal/dbtest internal/conformance -name '*.go' -type f | sort)
	if [ -n "$hits" ]; then
		fail "$name" "Skip call in test wiring: $(printf '%s' "$hits" | tr '\n' ' ')"
	else
		pass "$name"
	fi
}

# --- 4. generator-reacts-to-schema ---------------------------------------
# Plain sed to a new file, never sed -i (BSD and GNU differ).
check_generator() {
	local name=generator-reacts-to-schema
	sed 's/^        discover:$/        discover_probe:/' api/openapi.yaml >"$tmp/openapi.yaml"
	if diff -q "$tmp/openapi.yaml" api/openapi.yaml >/dev/null; then
		fail "$name" "the schema mutation changed nothing (pattern stale?)"
		return
	fi
	sed "s#^output:.*#output: $tmp/gen.go#" api/oapi-codegen.yaml >"$tmp/cfg.yaml"
	sed "s#^output:.*#output: $tmp/same.go#" api/oapi-codegen.yaml >"$tmp/cfg-same.yaml"
	go tool oapi-codegen -config "$tmp/cfg.yaml" "$tmp/openapi.yaml" >"$tmp/gen.out" 2>&1 || { fail "$name" "oapi-codegen failed on the mutated schema: $(tail -n 3 "$tmp/gen.out" | tr '\n' ' ')"; return; }
	if diff -q "$tmp/gen.go" internal/gen/gen.go >/dev/null; then
		fail "$name" "a changed schema produced identical generated code"
		return
	fi
	go tool oapi-codegen -config "$tmp/cfg-same.yaml" api/openapi.yaml >"$tmp/same.out" 2>&1 || { fail "$name" "oapi-codegen failed on the real schema: $(tail -n 3 "$tmp/same.out" | tr '\n' ' ')"; return; }
	if ! diff -q "$tmp/same.go" internal/gen/gen.go >/dev/null; then
		fail "$name" "regenerating the unmodified schema differs from internal/gen/gen.go (hand-edited or stale)"
		return
	fi
	sed "s#^output:.*#output: $tmp/same-spec.go#" api/oapi-codegen-spec.yaml >"$tmp/cfg-spec.yaml"
	go tool oapi-codegen -config "$tmp/cfg-spec.yaml" api/openapi.yaml >"$tmp/spec.out" 2>&1 || { fail "$name" "oapi-codegen failed on the real schema (embedded spec): $(tail -n 3 "$tmp/spec.out" | tr '\n' ' ')"; return; }
	if ! diff -q "$tmp/same-spec.go" internal/gen/spec/spec.go >/dev/null; then
		fail "$name" "regenerating the unmodified schema differs from internal/gen/spec/spec.go (hand-edited or stale)"
		return
	fi
	pass "$name"
}

# --- 5. postgres-no-docker-fails -----------------------------------------
# Needs Docker on the host. Suite binaries are cross-compiled static and run
# in busybox with no socket, no network and no host environment, so the
# variable is unset and testcontainers finds no Docker.
check_no_docker() {
	local name=postgres-no-docker-fails pkg arch log
	arch="$(go env GOARCH)"
	for pkg in store bus; do
		log="$tmp/nodocker-$pkg.log"
		if ! CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go test -c -o "$tmp/$pkg.test" "./internal/conformance/$pkg" >"$tmp/build-$pkg.log" 2>&1; then
			fail "$name" "cross-compile of $pkg suite failed: $(tail -n 3 "$tmp/build-$pkg.log" | tr '\n' ' ')"
			return
		fi
		if docker run --rm --network none --platform "linux/$arch" -v "$tmp:/t:ro" busybox:1.37.0 "/t/$pkg.test" -test.v -test.count=1 -test.timeout 180s >"$log" 2>&1; then
			fail "$name" "$pkg suite passed with no Docker and no AGENTBUS2_TEST_POSTGRES_DSN"
			return
		fi
		has "$log" 'AGENTBUS2_TEST_POSTGRES_DSN is unset and testcontainers could not start cgr.dev/chainguard/postgres' || { fail "$name" "$pkg log lacks the unset-and-could-not-start text; tail: $({ grep -E '^\s*--- FAIL|^FAIL|^panic' "$log" || true; tail -n 5 "$log"; } | tr '\n' ' ')"; return; }
		has "$log" '--- FAIL: TestScaffold/postgres' || { fail "$name" "$pkg log lacks '--- FAIL: TestScaffold/postgres'"; return; }
		has "$log" '--- PASS: TestScaffold/sqlite' || { fail "$name" "$pkg log lacks '--- PASS: TestScaffold/sqlite'"; return; }
		if has "$log" '--- SKIP'; then
			fail "$name" "$pkg log contains '--- SKIP'"
			return
		fi
	done
	pass "$name"
}

# --- 6. transport-env-rejects-unknown ------------------------------------
# Docker-free: the suite's TestMain refuses an unknown value before dbtest.Run.
check_transport_env() {
	local name=transport-env-rejects-unknown log="$tmp/transport.log"
	if AGENTBUS2_BUS_TRANSPORT=bogus go test -count=1 -run '^TestScaffold$' ./internal/conformance/bus/ >"$log" 2>&1; then
		fail "$name" "the bus suite passed with AGENTBUS2_BUS_TRANSPORT=bogus"
		return
	fi
	has "$log" 'AGENTBUS2_BUS_TRANSPORT must be local or inproc' || { fail "$name" "log lacks 'AGENTBUS2_BUS_TRANSPORT must be local or inproc'; tail: $(tail -n 3 "$log" | tr '\n' ' ')"; return; }
	pass "$name"
}

check_unreachable
check_unit_excludes
check_no_skip
check_generator
check_no_docker
check_transport_env

exit "$failed"
