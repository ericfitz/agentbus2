#!/usr/bin/env bash
# Self-test of the gate harness (Definition of Done C): one real mutation per
# check, applied in a throwaway worktree of HEAD, with the exact expected
# FAIL/ok vector asserted for scripts/check-gates.sh, plus proofs that
# scripts/ci-gate-negative.sh and scripts/ci-sim.sh fire.
#
# It tests HEAD, not the working tree: commit changes to scripts/, the Makefile
# or internal/dbtest before running it. The main tree is never written. Every
# block runs even after a failure so one run reports every broken check.
# Overlapping runs are unsupported (fixed container name and port in block 6);
# a killed run leaves a worktree under $TMPDIR and maybe one container:
# `git worktree prune` and `docker rm -f agentbus2-ci-sim-selftest` clear them.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SIM_NAME=agentbus2-ci-sim-selftest
SIM_PORT=5434
BAD_DSN='postgres://agentbus2:agentbus2@127.0.0.1:1/agentbus2?sslmode=disable&connect_timeout=2'
CHECKS=(postgres-unreachable-fails unit-tests-exclude-conformance no-skip-in-test-wiring generator-reacts-to-schema postgres-no-docker-fails transport-env-rejects-unknown)

tmp="$(mktemp -d)"
wt="$tmp/wt"
cleanup() {
	git -C "$ROOT" worktree remove --force "$wt" >/dev/null 2>&1 || true
	git -C "$ROOT" worktree prune
	docker rm -f "$SIM_NAME" >/dev/null 2>&1 || true
	rm -rf "$tmp"
}
trap cleanup EXIT

git -C "$ROOT" worktree add --detach "$wt" HEAD >/dev/null

bad=0
block_ok=1
status=0

# problem LABEL MESSAGE: record a failure of the current block.
problem() {
	echo "selftest: FAIL $1: $2"
	bad=1
	block_ok=0
}

begin() { block_ok=1; }
finish() { if [ "$block_ok" -eq 1 ]; then echo "selftest: ok $1"; fi; }

# revert: undo every mutation in the throwaway worktree.
revert() {
	git -C "$wt" checkout -- .
	git -C "$wt" clean -fd -- internal >/dev/null
}

# run_gates LOG [make args...]: run make check-gates in the worktree; sets $status.
run_gates() {
	local log="$1"
	shift
	if make -C "$wt" check-gates "$@" >"$log" 2>&1; then status=0; else status=$?; fi
}

# expect_checks LABEL LOG FAIL_NAMES...: every check named must have FAILed,
# every other check must be ok, and make check-gates must have exited non-zero.
expect_checks() {
	local label="$1" log="$2" name want f
	shift 2
	local -a fails=("$@")
	if [ "$status" -eq 0 ]; then problem "$label" "make check-gates exited 0, expected non-zero"; fi
	for name in "${CHECKS[@]}"; do
		want=ok
		for f in "${fails[@]}"; do
			if [ "$f" = "$name" ]; then want=FAIL; fi
		done
		if [ "$want" = FAIL ]; then
			grep -q "^FAIL $name" "$log" || problem "$label" "$name expected FAIL, log has: $(grep "$name" "$log" | head -n 1)"
		else
			grep -q "^ok  $name" "$log" || problem "$label" "$name expected ok, log has: $(grep "$name" "$log" | head -n 1)"
		fi
	done
}

# --- 1. skip-in-wiring ---------------------------------------------------------
block_skip_in_wiring() {
	local label=skip-in-wiring log="$tmp/b1.log" f="$wt/internal/dbtest/dbtest.go"
	begin
	sed 's/t.Fatalf("%v", err)/t.Skipf("%v", err)/' "$f" >"$tmp/x" && cp "$tmp/x" "$f"
	if ! grep -q 'Skipf' "$f"; then
		problem "$label" "mutation changed nothing (no t.Fatalf(\"%v\", err) line in dbtest.go)"
	else
		run_gates "$log"
		expect_checks "$label" "$log" postgres-unreachable-fails no-skip-in-test-wiring postgres-no-docker-fails
	fi
	finish "$label"
	revert
}

# --- 2. unit-set-leaks-conformance -----------------------------------------------
# GNU make exports command-line variable overrides through MAKEFLAGS, so the
# inner `make test` run by check-gates.sh inherits PKGS_UNIT=./...
block_unit_leak() {
	local label=unit-set-leaks-conformance log="$tmp/b2.log"
	begin
	run_gates "$log" 'PKGS_UNIT=./...'
	expect_checks "$label" "$log" unit-tests-exclude-conformance
	finish "$label"
	revert
}

# --- 3. gen-hand-edited ------------------------------------------------------------
block_gen_edited() {
	local label=gen-hand-edited log="$tmp/b3.log"
	begin
	printf '\n' >>"$wt/internal/gen/gen.go"
	run_gates "$log"
	expect_checks "$label" "$log" generator-reacts-to-schema
	finish "$label"
	revert
}

# --- 4. gen-stray-file -------------------------------------------------------------
block_gen_stray() {
	local label=gen-stray-file log="$tmp/b4.log"
	begin
	touch "$wt/internal/gen/stray.go"
	if make -C "$wt" generate-check >"$log" 2>&1; then
		problem "$label" "make generate-check passed with an untracked file in internal/gen"
	elif ! grep -qF '?? internal/gen/stray.go' "$log"; then
		problem "$label" "log lacks '?? internal/gen/stray.go': $(tail -n 3 "$log" | tr '\n' ' ')"
	fi
	finish "$label"
	revert
}

# --- gen-spec-hand-edited ------------------------------------------------------------
# make generate-check regenerates before it looks at git status, so it cannot
# be the negative proof for a hand edit of the embedded document; check 4 can.
block_gen_spec_edited() {
	local label=gen-spec-hand-edited log="$tmp/b4s.log"
	begin
	printf '\n' >>"$wt/internal/gen/spec/spec.go"
	run_gates "$log"
	expect_checks "$label" "$log" generator-reacts-to-schema
	finish "$label"
	revert
}

# --- 5. ci-gate-negative-fires ---------------------------------------------------------
block_ci_gate_negative() {
	local label=ci-gate-negative-fires log="$tmp/b5a.log" rc
	begin
	if AGENTBUS2_TEST_POSTGRES_DSN="$BAD_DSN" bash "$wt/scripts/ci-gate-negative.sh" 'postgres from AGENTBUS2_TEST_POSTGRES_DSN is unreachable' >"$log" 2>&1; then
		grep -qF 'gate-negative: ok' "$log" || problem "$label" "matching run exited 0 but did not print 'gate-negative: ok'"
	else
		problem "$label" "matching run should exit 0: $(tail -n 3 "$log" | tr '\n' ' ')"
	fi
	log="$tmp/b5b.log"
	if AGENTBUS2_TEST_POSTGRES_DSN="$BAD_DSN" bash "$wt/scripts/ci-gate-negative.sh" 'selftest: text that never appears' >"$log" 2>&1; then
		problem "$label" "non-matching run exited 0, expected 1"
	else
		rc=$?
		[ "$rc" -eq 1 ] || problem "$label" "non-matching run exited $rc, expected 1"
		grep -qF 'gate-negative: FAIL: log lacks the expected text' "$log" || problem "$label" "non-matching run did not print 'gate-negative: FAIL: log lacks the expected text'"
	fi
	finish "$label"
	revert
}

# --- 6. ci-sim-fails-and-cleans-up ---------------------------------------------------------
block_ci_sim() {
	local label=ci-sim-fails-and-cleans-up log="$tmp/b6.log" left
	begin
	touch "$wt/internal/gen/stray.go" # empty .go file: compile error, verify fails at build-all
	if CI_SIM_NAME="$SIM_NAME" CI_SIM_PORT="$SIM_PORT" bash "$wt/scripts/ci-sim.sh" >"$log" 2>&1; then
		problem "$label" "ci-sim.sh exited 0 on a tree that cannot build"
	fi
	left="$(docker ps -a --filter "name=$SIM_NAME" --format '{{.Names}}')"
	[ -z "$left" ] || problem "$label" "container left behind: $left"
	finish "$label"
	revert
}

# search_mutation_block LABEL FILE SED_EXPR PROOF_PATTERN BROKEN_ENGINE OTHER_ENGINE:
# mutate one engine's search translation in the worktree and require the
# corpus test to FAIL on that engine and PASS on the other (the suite is
# load-bearing per engine). Uses the ambient AGENTBUS2_TEST_POSTGRES_DSN
# (testcontainers when unset), like make test-conformance.
search_mutation_block() {
	local label="$1" file="$2" expr="$3" proof="$4" broken="$5" other="$6"
	local log="$tmp/$1.log" f="$wt/$2" t=TestSearchTextMatchesAgreeAcrossEngines
	begin
	sed "$expr" "$f" >"$tmp/x" && cp "$tmp/x" "$f"
	if ! grep -q "$proof" "$f"; then
		problem "$label" "mutation changed nothing ($proof absent from $file)"
	else
		(cd "$wt" && go test -count=1 -p 1 -v -run "^${t}\$" ./internal/conformance/store/ >"$log" 2>&1) || true
		grep -qF -- "--- FAIL: $t/$broken" "$log" || problem "$label" "log lacks '--- FAIL: $t/$broken': $(tail -n 3 "$log" | tr '\n' ' ')"
		grep -qF -- "--- PASS: $t/$other" "$log" || problem "$label" "log lacks '--- PASS: $t/$other'"
	fi
	finish "$label"
	revert
}

# --- 7. search-postgres-phrase ---------------------------------------------------------
# plainto_tsquery ANDs word terms anywhere within a column, so "rate limit" and
# commit:e159e8c match rows the phrase query rejects.
block_search_postgres_phrase() {
	search_mutation_block search-postgres-phrase internal/store/postgres/postgres.go \
		's/phraseto_tsquery/plainto_tsquery/g' plainto_tsquery postgres sqlite
}

# --- 8. search-sqlite-prefix ------------------------------------------------------------
# Without the FTS5 "*" suffix, e159* and commit:e159* return nothing.
block_search_sqlite_prefix() {
	search_mutation_block search-sqlite-prefix internal/store/sqlite/sqlite.go \
		's/ftsPrefix = "\*"/ftsPrefix = ""/' 'ftsPrefix = ""' sqlite postgres
}

# mutate_block LABEL FILE FROM TO TEST WANT...: replace the exact text FROM with
# TO in the worktree (through a temp file and cp), require TO to be present
# afterwards, run the named Bus conformance test and require every WANT line in
# its log. Runs against the ambient Postgres like block 7.
mutate_block() {
	local label="$1" file="$2" from="$3" to="$4" test="$5"
	shift 5
	local log="$tmp/$label.log" f="$wt/$file" want content
	begin
	if ! grep -qF -- "$from" "$f"; then
		problem "$label" "anchor text absent from $file: $from"
	else
		content="$(cat "$f")"
		printf '%s\n' "${content//"$from"/"$to"}" >"$tmp/x" && cp "$tmp/x" "$f"
		if ! grep -qF -- "$to" "$f"; then
			problem "$label" "mutation changed nothing ($to absent from $file)"
		else
			(cd "$wt" && go test -count=1 -p 1 -v -run "^${test}\$" ./internal/conformance/bus/ >"$log" 2>&1) || true
			for want in "$@"; do
				grep -qF -- "$want" "$log" || problem "$label" "log lacks '$want': $(tail -n 3 "$log" | tr '\n' ' ')"
			done
		fi
	fi
	finish "$label"
	revert
}

# --- 9. acl-check-removed -------------------------------------------------------------
# Receive without the readable-channel check must fail the ACL matrix on both engines.
block_acl_check_removed() {
	mutate_block acl-check-removed internal/core/delivery/receive.go \
		'if !readable[sub.ChannelID] {' 'if false {' TestACLMatrix \
		'--- FAIL: TestACLMatrix/sqlite' '--- FAIL: TestACLMatrix/postgres'
}

# --- 10. pending-batch-wakes ------------------------------------------------------------
# A watch floor that ignores the pending batch wakes on messages already delivered.
block_pending_batch_wakes() {
	mutate_block pending-batch-wakes internal/core/delivery/peek.go \
		'floor := max(c.CursorSeq, c.PendingEndSeq, after)' 'floor := max(c.CursorSeq, after)' \
		TestWatchIgnoresPendingBatch \
		'--- FAIL: TestWatchIgnoresPendingBatch/sqlite' '--- FAIL: TestWatchIgnoresPendingBatch/postgres'
}

# --- 11. inproc-crosses-the-wire -----------------------------------------------------
# Dropping the send's idempotency key in the remote Bus must break the replay
# test under inproc (two rows instead of one replay) and be invisible under
# local, which proves the inproc run goes through the wire.
block_inproc_crosses_the_wire() {
	local label=inproc-crosses-the-wire f="$wt/internal/bus/remote/messages.go" from='IdempotencyKey: key(in.IdempotencyKey)' log t=TestSendIdempotencyReplay
	begin
	if ! grep -qF -- "$from" "$f"; then
		problem "$label" "anchor text absent from internal/bus/remote/messages.go: $from"
	else
		sed 's/IdempotencyKey: key(in.IdempotencyKey)/IdempotencyKey: nil/' "$f" >"$tmp/x" && cp "$tmp/x" "$f"
		if [ "$(grep -c 'IdempotencyKey: nil' "$f")" -eq 0 ]; then
			problem "$label" "mutation changed nothing (IdempotencyKey: nil absent)"
		else
			log="$tmp/b11a.log"
			(cd "$wt" && AGENTBUS2_BUS_TRANSPORT=inproc go test -count=1 -p 1 -v -run "^${t}\$" ./internal/conformance/bus/ >"$log" 2>&1) || true
			grep -qF -- "--- FAIL: $t/sqlite" "$log" || problem "$label" "inproc log lacks '--- FAIL: $t/sqlite': $(tail -n 3 "$log" | tr '\n' ' ')"
			grep -qF -- "--- FAIL: $t/postgres" "$log" || problem "$label" "inproc log lacks '--- FAIL: $t/postgres'"
			log="$tmp/b11b.log"
			(cd "$wt" && AGENTBUS2_BUS_TRANSPORT=local go test -count=1 -p 1 -v -run "^${t}\$" ./internal/conformance/bus/ >"$log" 2>&1) || true
			grep -qF -- "--- PASS: $t/sqlite" "$log" || problem "$label" "local log lacks '--- PASS: $t/sqlite': $(tail -n 3 "$log" | tr '\n' ' ')"
			grep -qF -- "--- PASS: $t/postgres" "$log" || problem "$label" "local log lacks '--- PASS: $t/postgres'"
			if grep -qF -- '--- FAIL' "$log"; then problem "$label" "local run has a FAIL: the mutation must be invisible to the local path"; fi
		fi
	fi
	finish "$label"
	revert
}

# --- 12. deps-rule-fires -----------------------------------------------------------------
# Docker-free. Each probe file adds one forbidden production import; the exit
# script must exit 1 and name the package and the forbidden import. A
# third-party subpackage is caught only by the prefix match.
# deps_probe LABEL FILE IMPORT WANT [SCRIPT]: SCRIPT defaults to m4-exit-checks.sh.
deps_probe() {
	local label="$1" file="$2" import="$3" want="$4" script="${5:-m4-exit-checks.sh}" log="$tmp/b12.log" rc=0 pkgname
	pkgname="$(basename "$(dirname "$file")")"
	printf 'package %s\n\nimport _ "%s"\n' "$pkgname" "$import" >"$wt/$file"
	bash "$wt/scripts/$script" --deps-only >"$log" 2>&1 || rc=$?
	if [ "$rc" -ne 1 ]; then
		problem "$label" "$file importing $import: exit $rc, expected 1"
	fi
	grep -qF -- "$want" "$log" || problem "$label" "$file importing $import: log lacks '$want': $(tail -n 3 "$log" | tr '\n' ' ')"
	rm -f "$wt/$file"
}

block_deps_rule_fires() {
	local label=deps-rule-fires m=github.com/ericfitz/agentbus2 log="$tmp/b12z.log"
	begin
	deps_probe "$label" internal/core/probe_m4.go "$m/internal/gen" "deps_violation=$m/internal/core imports $m/internal/gen"
	deps_probe "$label" internal/bus/remote/probe_m4.go "$m/internal/server" "deps_violation=$m/internal/bus/remote imports $m/internal/server"
	deps_probe "$label" internal/bus/remote/probe_m4.go github.com/getkin/kin-openapi/openapi3 "deps_violation=$m/internal/bus/remote imports github.com/getkin/kin-openapi"
	if bash "$wt/scripts/m4-exit-checks.sh" --deps-only >"$log" 2>&1; then
		grep -qF 'deps_ok=1' "$log" || problem "$label" "clean tree: exit 0 without deps_ok=1"
	else
		problem "$label" "clean tree: expected exit 0: $(tail -n 3 "$log" | tr '\n' ' ')"
	fi
	finish "$label"
	revert
}

# --- 16. m5-deps-rule-fires ------------------------------------------------------------
# Docker-free. The Milestone 5 import rule: a forbidden in-module subpackage is
# caught only by the prefix match, a third-party import under internal/auth
# only by the allowlist.
block_m5_deps_rule_fires() {
	local label=m5-deps-rule-fires m=github.com/ericfitz/agentbus2 log="$tmp/b16z.log"
	begin
	deps_probe "$label" internal/core/maintenance/probe_m5.go "$m/internal/server/config" "deps_violation=$m/internal/core/maintenance imports $m/internal/server" m5-exit-checks.sh
	deps_probe "$label" internal/bus/probe_m5.go "$m/internal/gen" "deps_violation=$m/internal/bus imports $m/internal/gen" m5-exit-checks.sh
	deps_probe "$label" internal/auth/keys/probe_m5.go github.com/jmoiron/sqlx "deps_violation=$m/internal/auth/keys imports github.com/jmoiron/sqlx" m5-exit-checks.sh
	if bash "$wt/scripts/m5-exit-checks.sh" --deps-only >"$log" 2>&1; then
		grep -qF 'deps_ok=1' "$log" || problem "$label" "clean tree: exit 0 without deps_ok=1"
	else
		problem "$label" "clean tree: expected exit 0: $(tail -n 3 "$log" | tr '\n' ' ')"
	fi
	finish "$label"
	revert
}

# m7_probe LABEL FILE IMPORT WANT RULE [PKG [TAG]]: like deps_probe over the m7
# script, and every deps_violation line must carry "[rule RULE]": a probe
# trips exactly one rule. PKG is the package clause (default: the directory's
# name); a _test.go file is compiled by go list as a test file. TAG puts a
# "//go:build TAG" line first, so only the scan with that build tag sees it.
m7_probe() {
	local label="$1" file="$2" import="$3" want="$4" rule="$5" pkgname="${6:-}" tag="${7:-}" log="$tmp/b22.log" rc=0 other
	[ -n "$pkgname" ] || pkgname="$(basename "$(dirname "$file")")"
	{
		[ -z "$tag" ] || printf '//go:build %s\n\n' "$tag"
		printf 'package %s\n\nimport _ "%s"\n' "$pkgname" "$import"
	} >"$wt/$file"
	bash "$wt/scripts/m7-exit-checks.sh" --deps-only >"$log" 2>&1 || rc=$?
	if [ "$rc" -ne 1 ]; then
		problem "$label" "$file importing $import: exit $rc, expected 1: $(tail -n 3 "$log" | tr '\n' ' ')"
	fi
	grep -qF -- "$want" "$log" || problem "$label" "$file importing $import: log lacks '$want': $(tail -n 3 "$log" | tr '\n' ' ')"
	grep -qF -- "[rule $rule]" "$log" || problem "$label" "$file importing $import: log lacks '[rule $rule]'"
	other="$(grep '^deps_violation=' "$log" | grep -vF -- "[rule $rule]" | head -n 2 | tr '\n' ' ' || true)"
	[ -z "$other" ] || problem "$label" "$file importing $import: another rule fired too: $other"
	rm -f "$wt/$file"
}

# m7_stub_pkg BACKEND create|remove: a stub file that makes the package
# internal/keyprovider/BACKEND exist (a no-op beside the real package).
m7_stub_pkg() {
	local dir="$wt/internal/keyprovider/$1"
	if [ "$2" = create ]; then
		mkdir -p "$dir"
		printf 'package %s\n' "$1" >"$dir/probe_m7_pkg.go"
	else
		rm -f "$dir/probe_m7_pkg.go"
		rmdir "$dir" 2>/dev/null || true
	fi
}

# --- 22. m7-deps-rule-fires ------------------------------------------------------------
# Docker-free. The Milestone 7 import rule: store never imports core, notify
# stays a leaf (pgx/v5 is caught only by the prefix match), pgnotify never
# imports the server (a subpackage, likewise). The Slack bridge rules: slackapi
# imports no core, store, server or bus; slackfake may import only the standard
# library and slackapi (the allowlist); slack-go is imported only by
# slackapi/slackgo (the importer scan); core and the clients never import
# slackapi or slack-go. The signing key provider rules: keyprovider and kmsfake
# are allowlisted leaves, the consumers never import a backend package, and a
# tagged file cannot hide an import (the tagged pass). An import go list cannot resolve must exit 2, never
# pass as a package with no imports.
block_m7_deps_rule_fires() {
	local label=m7-deps-rule-fires m=github.com/ericfitz/agentbus2 log="$tmp/b22z.log" rc=0
	begin
	# Each probe must trip exactly one rule (the [rule NAME] tag on every
	# violation line). A probe host must be a package no other probe's rule
	# reaches transitively: internal/oidctest is imported by no production
	# code, so a slack-go import there trips the importer rule alone.
	# Probes that trip two rules by design (a slackapi file importing slack-go
	# is both an allowlist and an importer violation) are not used.
	m7_probe "$label" internal/store/sqlstore/probe_m7.go "$m/internal/core/notify" "deps_violation=$m/internal/store/sqlstore imports $m/internal/core" store-no-core
	m7_probe "$label" internal/core/notify/probe_m7.go github.com/jackc/pgx/v5 "deps_violation=$m/internal/core/notify imports github.com/jackc/pgx" notify-leaf
	m7_probe "$label" internal/core/notify/pgnotify/probe_m7.go "$m/internal/server/config" "deps_violation=$m/internal/core/notify/pgnotify imports $m/internal/server" pgnotify-no-server
	m7_probe "$label" internal/slackapi/probe_m7.go "$m/internal/store" "deps_violation=$m/internal/slackapi imports $m/internal/store" slackapi-allowlist
	m7_probe "$label" internal/slackapi/slackfake/probe_m7.go github.com/jmoiron/sqlx "deps_violation=$m/internal/slackapi/slackfake imports github.com/jmoiron/sqlx" slackapi-allowlist
	m7_probe "$label" internal/slackbridge/probe_m7.go "$m/internal/slackapi/slackgo" "deps_violation=$m/internal/slackbridge imports $m/internal/slackapi/slackgo" slackbridge-isolation
	m7_probe "$label" internal/slackbridge/probe_m7.go "$m/internal/server/config" "deps_violation=$m/internal/slackbridge imports $m/internal/server" slackbridge-isolation
	m7_probe "$label" internal/cli/probe_m7.go "$m/internal/slackapi" "deps_violation=$m/internal/cli imports $m/internal/slackapi" slack-consumers
	m7_probe "$label" internal/cli/probe_m7.go "$m/internal/slackapi/slackgo" "deps_violation=$m/internal/cli imports $m/internal/slackapi/slackgo" slack-consumers
	m7_probe "$label" internal/oidctest/probe_m7.go github.com/slack-go/slack "deps_violation=$m/internal/oidctest imports github.com/slack-go/slack" slack-go-importers
	m7_probe "$label" internal/oidctest/probe_m7.go github.com/slack-go/slack/socketmode "deps_violation=$m/internal/oidctest imports github.com/slack-go/slack" slack-go-importers
	# A test file (internal and external) and a GOOS-suffixed file of any
	# package other than slackgo count too.
	m7_probe "$label" internal/oidctest/probe_m7_test.go github.com/slack-go/slack "deps_violation=$m/internal/oidctest imports github.com/slack-go/slack" slack-go-importers
	m7_probe "$label" internal/oidctest/probe_m7_x_test.go github.com/slack-go/slack "deps_violation=$m/internal/oidctest imports github.com/slack-go/slack" slack-go-importers oidctest_test
	m7_probe "$label" internal/oidctest/probe_m7_windows.go github.com/slack-go/slack "deps_violation=$m/internal/oidctest imports github.com/slack-go/slack" slack-go-importers
	m7_probe "$label" internal/oidctest/probe_m7_darwin.go github.com/slack-go/slack "deps_violation=$m/internal/oidctest imports github.com/slack-go/slack" slack-go-importers
	# The signing key provider rules. The leaf probes import internal/oidctest
	# (it imports nothing of this module, so no cycle). A backend package does
	# not exist until its task, so a stub file creates it for the probe.
	m7_probe "$label" internal/keyprovider/probe_m7.go "$m/internal/oidctest" "deps_violation=$m/internal/keyprovider imports $m/internal/oidctest" keyprovider-leaf
	m7_probe "$label" internal/keyprovider/kmsfake/probe_m7.go "$m/internal/oidctest" "deps_violation=$m/internal/keyprovider/kmsfake imports $m/internal/oidctest" keyprovider-leaf
	local b
	for b in awskms gcpkms azurekms hashivault; do
		m7_stub_pkg "$b" create
		m7_probe "$label" internal/cli/probe_m7.go "$m/internal/keyprovider/$b" "deps_violation=$m/internal/cli imports $m/internal/keyprovider/$b" keyprovider-consumers
		m7_stub_pkg "$b" remove
	done
	# A tagged file is invisible to the default scan: the tagged pass finds it,
	# for the consumers rule and for the importer scan alike.
	m7_stub_pkg awskms create
	m7_probe "$label" internal/server/probe_m7_tagged.go "$m/internal/keyprovider/awskms" "deps_violation=$m/internal/server imports $m/internal/keyprovider/awskms" keyprovider-consumers "" awskms
	m7_stub_pkg awskms remove
	m7_probe "$label" internal/oidctest/probe_m7_tagged.go github.com/slack-go/slack "deps_violation=$m/internal/oidctest imports github.com/slack-go/slack" slack-go-importers "" gcpkms
	printf 'package notify\n\nimport _ "%s"\n' "$m/internal/no/such/package" >"$wt/internal/core/notify/probe_m7.go"
	bash "$wt/scripts/m7-exit-checks.sh" --deps-only >"$log" 2>&1 || rc=$?
	if [ "$rc" -ne 2 ]; then
		problem "$label" "an unresolvable import: exit $rc, expected 2: $(tail -n 3 "$log" | tr '\n' ' ')"
	fi
	rm -f "$wt/internal/core/notify/probe_m7.go"
	if bash "$wt/scripts/m7-exit-checks.sh" --deps-only >"$log" 2>&1; then
		grep -qF 'deps_ok=1' "$log" || problem "$label" "clean tree: exit 0 without deps_ok=1"
	else
		problem "$label" "clean tree: expected exit 0: $(tail -n 3 "$log" | tr '\n' ' ')"
	fi
	finish "$label"
	revert
}

# --- 13. transport-check-fires -----------------------------------------------------------
# Without the os.Exit(2) of the transport selection an unknown
# AGENTBUS2_BUS_TRANSPORT falls through to the suite; check 6 must FAIL.
block_transport_check_fires() {
	local label=transport-check-fires log="$tmp/b13.log" f="$wt/internal/conformance/bus/main_test.go"
	begin
	sed '/os.Exit(2)/d' "$f" >"$tmp/x" && cp "$tmp/x" "$f"
	if [ "$(grep -c 'os.Exit(2)' "$f")" -ne 0 ]; then
		problem "$label" "mutation changed nothing (os.Exit(2) still present)"
	else
		run_gates "$log"
		expect_checks "$label" "$log" transport-env-rejects-unknown
	fi
	finish "$label"
	revert
}

# --- 14 and 15. the Milestone 5 exit's negative halves -----------------------------------
# Docker-free: both run only the SQLite half of the exit test. The anchor line
# must stay on one line in the source, exactly once. A break must fail the
# named subtest.
mutate_auth_block() {
	local label="$1" file="$2" from="$3" to="$4" want="$5"
	local log="$tmp/$label.log" f="$wt/$file" content
	begin
	if [ "$(grep -cF -- "$from" "$f")" -ne 1 ]; then
		problem "$label" "anchor text not exactly once in $file: $from"
	else
		content="$(cat "$f")"
		printf '%s\n' "${content//"$from"/"$to"}" >"$tmp/x" && cp "$tmp/x" "$f"
		if ! grep -qF -- "$to" "$f"; then
			problem "$label" "mutation changed nothing ($to absent from $file)"
		else
			(cd "$wt" && go test -count=1 -p 1 -v -run '^TestAuthEndToEnd$/sqlite' ./internal/conformance/bus/ >"$log" 2>&1) || true
			grep -qF -- "--- FAIL: $want" "$log" || problem "$label" "log lacks '--- FAIL: $want': $(tail -n 3 "$log" | tr '\n' ' ')"
		fi
	fi
	finish "$label"
	revert
}

# 14. A bearer whose signature is not checked: only the forged-bearer subtest
# sends something whose signature matters (the no-token subtest sends nothing).
block_bearer_signature_ignored() {
	mutate_auth_block bearer-signature-ignored internal/auth/token/issuer.go \
		'if err := tok.Claims(key.Public, &c); err != nil {' \
		'if err := tok.UnsafeClaimsWithoutVerification(&c); err != nil || key.KID == "" {' \
		TestAuthEndToEnd/sqlite/forged-bearer-refused
}

# 15. An administrator check that lets everyone through. The one rule table
# lives in internal/core/access/decide.go (accesscheck plan Task 5 moved the
# anchor from the deleted internal/core/authz.go): the Administer clause of
# decideServer is the only place the administrator flag is read for the server.
block_admin_flag_ignored() {
	mutate_auth_block admin-flag-ignored internal/core/access/decide.go \
		'if !p.Admin {' 'if false {' \
		TestAuthEndToEnd/sqlite/admin-refused-as-non-admin
}

# --- 18 to 21. the Milestone 7 exit's negative halves ----------------------------------
# The anchor line must stay on one line in the source, exactly once. Each
# mutation must fail the named subtest of the replicas exit test; a block may
# also fail other leaves of the same run (NotifyControl shares notifySQL).
# Blocks 18 and 19 run the Postgres half under testcontainers (Docker, which
# the self-test job has); blocks 20 and 21 run the SQLite half only.
mutate_replicas_block() {
	local label="$1" file="$2" from="$3" to="$4" runre="$5" want="$6"
	local log="$tmp/$label.log" f="$wt/$file" content
	begin
	if [ "$(grep -cF -- "$from" "$f")" -ne 1 ]; then
		problem "$label" "anchor text not exactly once in $file: $from"
	else
		content="$(cat "$f")"
		printf '%s\n' "${content//"$from"/"$to"}" >"$tmp/x" && cp "$tmp/x" "$f"
		if ! grep -qF -- "$to" "$f"; then
			problem "$label" "mutation changed nothing ($to absent from $file)"
		else
			(cd "$wt" && go test -count=1 -p 1 -v -run "$runre" ./internal/conformance/bus/ >"$log" 2>&1) || true
			grep -qF -- "--- FAIL: $want" "$log" || problem "$label" "log lacks '--- FAIL: $want': $(tail -n 3 "$log" | tr '\n' ' ')"
		fi
	fi
	finish "$label"
	revert
}

# 18. An append whose NOTIFY carries nothing: the watch on the other replica
# times out at 3 s instead of waking inside the merge window plus 1 s.
block_notify_append_removed() {
	# shellcheck disable=SC2016 # the single-quoted arguments are literal Go source
	mutate_replicas_block notify-append-removed internal/store/postgres/notify.go \
		'const notifySQL = "SELECT pg_notify(" + channelExpr + ", $1)"' \
		'const notifySQL = "SELECT $1::text"' \
		'^TestReplicasWake$/postgres' TestReplicasWake/postgres/wake-crosses-servers
}

# 19. A Postgres advisory lock that is always granted: both replicas run the jobs.
block_advisory_lock_ignored() {
	# shellcheck disable=SC2016 # the single-quoted arguments are literal Go source
	mutate_replicas_block advisory-lock-ignored internal/store/postgres/lock.go \
		'const tryLockSQL = "SELECT pg_try_advisory_lock(hashtext(current_schema()), hashtext($1))"' \
		'const tryLockSQL = "SELECT true OR $1::text IS NULL"' \
		'^TestReplicasMaintenanceExclusive$/postgres' TestReplicasMaintenanceExclusive/postgres/one-holder
}

# 20. A SQLite lease that is always granted.
block_sqlite_lease_ignored() {
	mutate_replicas_block sqlite-lease-ignored internal/store/sqlstore/lock.go \
		'held, err = t.TryLease(ctx, name, owner, now, until)' \
		'held, err = true, error(nil)' \
		'^TestReplicasMaintenanceExclusive$/sqlite' TestReplicasMaintenanceExclusive/sqlite/one-holder
}

# 21. A token endpoint that discards the conflict on the shared assertion
# table: the second replica mints from the same assertion.
block_assertion_replay_ignored() {
	mutate_replicas_block assertion-replay-ignored internal/server/auth_handlers.go \
		'return tx.PutAssertionJTI(ctx, p.Identity, p.JTI, keepUntil)' \
		'_ = tx.PutAssertionJTI(ctx, p.Identity, p.JTI, keepUntil); return nil' \
		'^TestReplicasWake$/sqlite' TestReplicasWake/sqlite/assertion-single-use-across-replicas
}

# --- 27 and 28. the Slack bridge's duplicate guards (issue 16 item 86) -----------------
# Docker-free: the SQLite half of TestSlackBridgeState. Each mutation must fail
# the named subtest.
# 27. A record that writes the map row without asking whether an overlapping
# runner already did: the subtest's hook plants the other runner's row between
# the Slack post and the record, so the overwrite is what it sees.
block_slackbridge_overlap_row() {
	mutate_replicas_block slackbridge-overlap-row internal/slackbridge/outbound.go \
		'_, err := tx.BridgeMessageBySeq(ctx, b.ID, seq)' \
		'_, err := tx.BridgeMessageBySeq(ctx, b.ID, seq); err = store.ErrNotFound' \
		'^TestSlackBridgeState$/sqlite/overlap-row-already-written' TestSlackBridgeState/sqlite/overlap-row-already-written
}

# 28. A runner that drains before it catches up: the catch-up that follows
# the first drain would repair the row too late, after the second post. The
# subtest delays the socket's connect so the worker's catch-up cannot do the
# repair. The line is replaced by an awk pass because the same call also
# appears in the worker.
block_slackbridge_catchup_before_drain() {
	local label=slackbridge-catchup-before-drain log="$tmp/b28.log" f="$wt/internal/slackbridge/runner.go"
	local want=TestSlackBridgeState/sqlite/crash-repair-before-drain
	begin
	awk '/Catch-up first: it repairs/ { armed = 1 } armed && /s\.CatchUp\(ctx\)/ { sub(/s\.CatchUp\(ctx\)/, "error(nil)"); armed = 0 } { print }' "$f" >"$tmp/x" && cp "$tmp/x" "$f"
	if [ "$(grep -c 's\.CatchUp(ctx)' "$f" || true)" -ne 1 ] || [ "$(grep -c 'err := error(nil); err != nil' "$f" || true)" -ne 1 ]; then
		problem "$label" "mutation changed nothing (the runner's first CatchUp is still there)"
	else
		(cd "$wt" && go test -count=1 -p 1 -v -run '^TestSlackBridgeState$/sqlite/crash-repair-before-drain' ./internal/conformance/bus/ >"$log" 2>&1) || true
		grep -qF -- "--- FAIL: $want" "$log" || problem "$label" "log lacks '--- FAIL: $want': $(tail -n 3 "$log" | tr '\n' ' ')"
	fi
	finish "$label"
	revert
}

# --- 23. m6-exit-script-fires ----------------------------------------------------------
# Docker-free and without running the e2e test. The Milestone 6 exit script:
# the import rule per group (the leaf group's internal/core row, the consumer
# group's internal/gen row and internal/store row), the log form's refusal of a
# missing or empty log, its counts on a synthetic log (and a deleted or failed
# line), and the word check.
M6_SEQUENCE_STEPS=(0-server 1-register 2-token 3-open-session 4-watch 5-send-from-another-identity 6-wake 7-receive 8-ack 9-stale-bearer-refreshes 10-mcp-over-stdio 11-stop-hook 12-watch-ends-only-by-signal)
M6_CELLS=(wait-0-woke wait-1-timeout wait-2-error wait-3-replaced wait-3-signal wait-4-harness-gone watch-2-error watch-3-replaced watch-3-signal watch-4-harness-gone watch-never-0-or-1)

# m6_log FILE: a synthetic e2e log with thirteen sequence and eleven cell leaf lines.
m6_log() {
	local f="$1" n
	: >"$f"
	printf '=== RUN   TestSection24Sequence\n--- PASS: TestSection24Sequence (3.00s)\n' >>"$f"
	for n in "${M6_SEQUENCE_STEPS[@]}"; do printf '    --- PASS: TestSection24Sequence/%s (0.00s)\n' "$n" >>"$f"; done
	printf -- '--- PASS: TestExitCodeTable (20.00s)\n' >>"$f"
	for n in "${M6_CELLS[@]}"; do printf '    --- PASS: TestExitCodeTable/%s (0.00s)\n' "$n" >>"$f"; done
	printf 'PASS\nok  \tgithub.com/ericfitz/agentbus2/internal/e2e\t25.000s\n' >>"$f"
}

# m6_expect LABEL WHAT WANT_RC PRESENT [ABSENT]: runs the log form of the script
# on $tmp/m6.log, matching its output with grep -qF.
m6_expect() {
	local label="$1" what="$2" want_rc="$3" present="$4" absent="${5:-}" log="$tmp/b23.log" rc=0
	bash "$wt/scripts/m6-exit-checks.sh" "$tmp/m6.log" >"$log" 2>&1 || rc=$?
	if [ "$rc" -ne "$want_rc" ]; then problem "$label" "$what: exit $rc, expected $want_rc: $(tail -n 3 "$log" | tr '\n' ' ')"; fi
	grep -qF -- "$present" "$log" || problem "$label" "$what: output lacks '$present': $(tail -n 4 "$log" | tr '\n' ' ')"
	if [ -n "$absent" ] && grep -qF -- "$absent" "$log"; then problem "$label" "$what: output has '$absent'"; fi
}

block_m6_exit_script_fires() {
	local label=m6-exit-script-fires m=github.com/ericfitz/agentbus2 log="$tmp/b23z.log" rc
	begin
	if bash "$wt/scripts/m6-exit-checks.sh" --deps-only >"$log" 2>&1; then
		grep -qF 'deps_ok=1' "$log" || problem "$label" "clean tree: exit 0 without deps_ok=1"
	else
		problem "$label" "clean tree: expected exit 0: $(tail -n 3 "$log" | tr '\n' ' ')"
	fi
	deps_probe "$label" internal/profile/probe_m6.go "$m/internal/core" "deps_violation=$m/internal/profile imports $m/internal/core" m6-exit-checks.sh
	deps_probe "$label" internal/mcpserver/probe_m6.go "$m/internal/gen" "deps_violation=$m/internal/mcpserver imports $m/internal/gen" m6-exit-checks.sh
	deps_probe "$label" internal/cli/probe_m6.go "$m/internal/store/sqlite" "deps_violation=$m/internal/cli imports $m/internal/store/sqlite" m6-exit-checks.sh
	deps_probe "$label" internal/oidcclient/probe_m6.go "$m/internal/bus" "deps_violation=$m/internal/oidcclient imports $m/internal/bus" m6-exit-checks.sh
	deps_probe "$label" internal/oidctest/probe_m6.go "$m/internal/bus" "deps_violation=$m/internal/oidctest imports $m/internal/bus" m6-exit-checks.sh
	rc=0
	bash "$wt/scripts/m6-exit-checks.sh" "$tmp/does-not-exist.log" >"$log" 2>&1 || rc=$?
	if [ "$rc" -ne 2 ]; then problem "$label" "missing log: exit $rc, expected 2"; fi
	: >"$tmp/empty.log"
	rc=0
	bash "$wt/scripts/m6-exit-checks.sh" "$tmp/empty.log" >"$log" 2>&1 || rc=$?
	if [ "$rc" -ne 2 ]; then problem "$label" "empty log: exit $rc, expected 2"; fi
	m6_log "$tmp/m6.log"
	m6_expect "$label" "good log" 0 'words_ok=1' 'words_hit'
	grep -qF 'sequence=13' "$tmp/b23.log" || problem "$label" "good log: no sequence=13"
	grep -qF 'exitcodes=11' "$tmp/b23.log" || problem "$label" "good log: no exitcodes=11"
	grep -qF 'fail=0' "$tmp/b23.log" || problem "$label" "good log: no fail=0"
	grep -vF 'TestExitCodeTable/wait-3-replaced' "$tmp/m6.log" >"$tmp/m6x.log" && cp "$tmp/m6x.log" "$tmp/m6.log"
	m6_expect "$label" "deleted cell line" 1 'exitcodes=10'
	m6_log "$tmp/m6.log"
	sed 's/--- PASS: TestExitCodeTable\/wait-3-signal/--- FAIL: TestExitCodeTable\/wait-3-signal/' "$tmp/m6.log" >"$tmp/m6x.log" && cp "$tmp/m6x.log" "$tmp/m6.log"
	m6_expect "$label" "failed cell line" 1 'fail=1'
	m6_log "$tmp/m6.log"
	sed 's/TestSection24Sequence\/7-receive /TestSection24Sequence\/6-wake /' "$tmp/m6.log" >"$tmp/m6x.log" && cp "$tmp/m6x.log" "$tmp/m6.log"
	m6_expect "$label" "a repeated step line in place of a missing one" 1 'sequence=12'
	m6_log "$tmp/m6.log"
	sed 's/^ok  .*internal\/e2e.*$/FAIL\tgithub.com\/ericfitz\/agentbus2\/internal\/e2e\t25.000s/' "$tmp/m6.log" >"$tmp/m6x.log" && cp "$tmp/m6x.log" "$tmp/m6.log"
	m6_expect "$label" "a package FAIL with every leaf passing (TestMain teardown)" 1 'pkg_ok=0'
	m6_log "$tmp/m6.log"
	grep -vE '^(PASS|ok )' "$tmp/m6.log" >"$tmp/m6x.log" && cp "$tmp/m6x.log" "$tmp/m6.log"
	m6_expect "$label" "no package ok line" 1 'pkg_ok=0'
	m6_log "$tmp/m6.log"
	printf '\ndm/ is gone in v2\n' >>"$wt/internal/cli/skills/using-agentbus2/SKILL.md"
	m6_expect "$label" "word check" 1 'words_hit' 'words_ok'
	finish "$label"
	revert
}

# --- 24. replaced-exit-code-ignored ----------------------------------------------------
# The Milestone 6 exit's negative halves, Docker-free. Each mutation must fail
# the named test; TestMain of internal/e2e rebuilds the binary from the worktree.
# mutate_file LABEL FILE FROM TO: FROM must occur on exactly one line.
mutate_file() {
	local label="$1" file="$2" from="$3" to="$4" f="$wt/$2" content
	if [ "$(grep -cF -- "$from" "$f")" -ne 1 ]; then
		problem "$label" "anchor text not exactly once in $file: $from"
		return 1
	fi
	content="$(cat "$f")"
	printf '%s\n' "${content//"$from"/"$to"}" >"$tmp/x" && cp "$tmp/x" "$f"
	if ! grep -qF -- "$to" "$f"; then
		problem "$label" "mutation changed nothing ($to absent from $file)"
		return 1
	fi
}

block_replaced_exit_ignored() {
	local label=replaced-exit-code-ignored log="$tmp/b24.log" f="$wt/internal/cli/exit.go" before after
	begin
	# 1. The replaced exit: `return 3` on the line after the errReplaced case becomes `return 0`.
	before="$(grep -c 'return 0' "$f" || true)"
	awk '{ if (prev ~ /case errors.Is\(err, errReplaced\)/ && $0 ~ /return 3/) { sub(/return 3/, "return 0") } print; prev = $0 }' "$f" >"$tmp/x" && cp "$tmp/x" "$f"
	after="$(grep -c 'return 0' "$f" || true)"
	if [ "$after" -ne $((before + 1)) ]; then
		problem "$label" "exit.go mutation changed nothing (return 0 count $before -> $after)"
	else
		(cd "$wt" && go test -count=1 -v -run '^TestExitCodeTable$/(wait|watch)-3-replaced$' ./internal/e2e/ >"$log" 2>&1) || true
		grep -qF -- '--- FAIL: TestExitCodeTable/wait-3-replaced' "$log" || problem "$label" "log lacks '--- FAIL: TestExitCodeTable/wait-3-replaced': $(tail -n 3 "$log" | tr '\n' ' ')"
		grep -qF -- '--- FAIL: TestExitCodeTable/watch-3-replaced' "$log" || problem "$label" "log lacks '--- FAIL: TestExitCodeTable/watch-3-replaced'"
	fi
	revert
	# 2. A local Watch that cannot see another connection's send: the in-process
	# notifier replaces the polling one.
	if mutate_file "$label" internal/client/local.go 'n = notify.NewPolling(poll)' 'n, _ = notify.NewMemory(), poll'; then
		if [ "$(grep -c 'notify.NewMemory()' "$wt/internal/client/local.go")" -ne 1 ]; then
			problem "$label" "local.go: notify.NewMemory() count is not 1"
		else
			log="$tmp/b24b.log"
			(cd "$wt" && go test -count=1 -run '^TestLocalWatchWakesAcrossConns$' ./internal/client/ >"$log" 2>&1) || true
			grep -qF -- '--- FAIL' "$log" || problem "$label" "polling mutation: log lacks '--- FAIL': $(tail -n 3 "$log" | tr '\n' ' ')"
		fi
	fi
	revert
	# 3. The harness-gone cause: cancel(nil) maps through cancelCause to
	# errReplaced, so both 4 cells read 3.
	if mutate_file "$label" internal/procs/harness.go 'cancel(ErrHarnessGone)' 'cancel(nil)'; then
		if [ "$(grep -c 'cancel(nil)' "$wt/internal/procs/harness.go")" -ne 1 ] || [ "$(grep -c 'cancel(ErrHarnessGone)' "$wt/internal/procs/harness.go" || true)" -ne 0 ]; then
			problem "$label" "harness.go mutation is not exactly cancel(nil)"
		else
			log="$tmp/b24c.log"
			(cd "$wt" && go test -count=1 -v -run '^TestExitCodeTable$/(wait|watch)-4-harness-gone$' ./internal/e2e/ >"$log" 2>&1) || true
			grep -qF -- '--- FAIL: TestExitCodeTable/wait-4-harness-gone' "$log" || problem "$label" "log lacks '--- FAIL: TestExitCodeTable/wait-4-harness-gone': $(tail -n 3 "$log" | tr '\n' ' ')"
			grep -qF -- '--- FAIL: TestExitCodeTable/watch-4-harness-gone' "$log" || problem "$label" "log lacks '--- FAIL: TestExitCodeTable/watch-4-harness-gone'"
		fi
	fi
	finish "$label"
	revert
}

# --- 25. m8-exit-script-fires ----------------------------------------------------------
# Docker-free. The Milestone 8 exit script on the committed tree: the import
# allowlist, the log counts (derived from a real log, never a literal), the
# word check and its test-file exemption.
# m8_deps_probe LABEL FILE IMPORT WANT: deps_probe over the m8 script.
m8_deps_probe() {
	deps_probe "$1" "$2" "$3" "$4" m8-exit-checks.sh
}

# m8_expect LABEL WANT_RC LOG [WANT...]: run the m8 script on LOG in the
# worktree; the exit status must equal WANT_RC and every WANT line be present.
m8_expect() {
	local label="$1" want_rc="$2" in="$3" rc=0 want out="$tmp/b25.out"
	shift 3
	bash "$wt/scripts/m8-exit-checks.sh" "$in" >"$out" 2>&1 || rc=$?
	if [ "$rc" -ne "$want_rc" ]; then
		problem "$label" "$in: exit $rc, expected $want_rc: $(tail -n 4 "$out" | tr '\n' ' ')"
	fi
	for want in "$@"; do
		grep -qF -- "$want" "$out" || problem "$label" "$in: output lacks '$want': $(tail -n 4 "$out" | tr '\n' ' ')"
	done
}

block_m8_exit_script_fires() {
	local label=m8-exit-script-fires m=github.com/ericfitz/agentbus2 log="$tmp/b25z.log" rc=0 real="$tmp/b25-m8.log"
	begin
	if bash "$wt/scripts/m8-exit-checks.sh" --deps-only >"$log" 2>&1; then
		grep -qF 'deps_ok=1' "$log" || problem "$label" "clean tree: exit 0 without deps_ok=1"
	else
		problem "$label" "clean tree: expected exit 0: $(tail -n 3 "$log" | tr '\n' ' ')"
	fi
	m8_deps_probe "$label" internal/tui/probe_m8.go "$m/internal/bus/local" "deps_violation=$m/internal/tui imports $m/internal/bus/local"
	m8_deps_probe "$label" internal/tui/probe_m8.go "$m/internal/gen" "deps_violation=$m/internal/tui imports $m/internal/gen"
	# internal/filelock is named in no forbidden list anywhere: only an
	# allowlist catches it.
	m8_deps_probe "$label" internal/tui/probe_m8.go "$m/internal/filelock" "deps_violation=$m/internal/tui imports $m/internal/filelock"
	# internal/client is the package cli/tui.go uses and tui itself must not.
	m8_deps_probe "$label" internal/tui/probe_m8.go "$m/internal/client" "deps_violation=$m/internal/tui imports $m/internal/client"
	: >"$tmp/empty.log"
	m8_expect "$label" 2 "$tmp/missing.log"
	m8_expect "$label" 2 "$tmp/empty.log"
	bash "$wt/scripts/m8-exit-checks.sh" >"$log" 2>&1 || rc=$?
	[ "$rc" -eq 2 ] || problem "$label" "no argument: exit $rc, expected 2"
	(cd "$wt" && go test -count=1 -v -run '^(TestBothModes|TestNoInboxView|TestRunTUI.*)$' ./internal/tui/ ./internal/cli/ >"$real" 2>&1) || true
	m8_expect "$label" 0 "$real" modes=2 noinbox=1 cli=2 fail=0 words_ok=1 deps_ok=1
	sed '/TestRunTUIServerProfile/d' "$real" >"$tmp/b25-m8-cli.log"
	m8_expect "$label" 1 "$tmp/b25-m8-cli.log" cli=1
	sed '/TestBothModes\/server/d' "$real" >"$tmp/b25-m8-one.log"
	m8_expect "$label" 1 "$tmp/b25-m8-one.log" modes=1
	sed 's/^\(    --- \)PASS\(: TestBothModes\/local\)/\1FAIL\2/' "$real" >"$tmp/b25-m8-fail.log"
	m8_expect "$label" 1 "$tmp/b25-m8-fail.log" fail=1
	sed '/--- PASS: TestNoInboxView/d' "$real" >"$tmp/b25-m8-noinbox.log"
	m8_expect "$label" 1 "$tmp/b25-m8-noinbox.log" noinbox=0
	# The word check covers production files, comments included.
	printf '// Inbox\n' >>"$wt/internal/tui/doc.go"
	bash "$wt/scripts/m8-exit-checks.sh" "$real" >"$log" 2>&1 && rc=0 || rc=$?
	[ "$rc" -eq 1 ] || problem "$label" "word in doc.go: exit $rc, expected 1"
	if grep -qF 'words_ok=1' "$log"; then problem "$label" "word in doc.go: words_ok=1 printed"; fi
	grep -qF 'doc.go' "$log" || problem "$label" "word in doc.go: match not printed"
	# The word check needs no tool beyond grep: with an rg on PATH that
	# fails, the planted word must still fail the check, never pass it.
	mkdir -p "$tmp/norg"
	printf '#!/bin/sh\necho "rg: not installed" >&2\nexit 127\n' >"$tmp/norg/rg"
	chmod +x "$tmp/norg/rg"
	PATH="$tmp/norg:$PATH" bash "$wt/scripts/m8-exit-checks.sh" "$real" >"$log" 2>&1 && rc=0 || rc=$?
	[ "$rc" -eq 1 ] || problem "$label" "word in doc.go, rg failing: exit $rc, expected 1"
	if grep -qF 'words_ok=1' "$log"; then problem "$label" "word in doc.go, rg failing: words_ok=1 printed"; fi
	grep -qF 'doc.go' "$log" || problem "$label" "word in doc.go, rg failing: match not printed"
	revert
	# internal/cli/tui.go is the word check's second path.
	printf '// tui_name\n' >>"$wt/internal/cli/tui.go"
	bash "$wt/scripts/m8-exit-checks.sh" "$real" >"$log" 2>&1 && rc=0 || rc=$?
	[ "$rc" -eq 1 ] || problem "$label" "word in cli/tui.go: exit $rc, expected 1"
	grep -qF 'cli/tui.go' "$log" || problem "$label" "word in cli/tui.go: match not printed"
	revert
	# A word check that cannot run is exit 2, never a pass.
	mv "$wt/internal/tui" "$tmp/tui-moved"
	bash "$wt/scripts/m8-exit-checks.sh" "$real" >"$log" 2>&1 && rc=0 || rc=$?
	mv "$tmp/tui-moved" "$wt/internal/tui"
	[ "$rc" -eq 2 ] || problem "$label" "word check without internal/tui: exit $rc, expected 2"
	if grep -qF 'words_ok=1' "$log"; then problem "$label" "word check without internal/tui: words_ok=1 printed"; fi
	# Test files are out of scope.
	printf '// inbox\n' >>"$wt/internal/tui/run_test.go"
	m8_expect "$label" 0 "$real" words_ok=1
	finish "$label"
	revert
}

# --- 26. tui-hint-mutation ---------------------------------------------------------------
# Three mutations of the TUI's client, each of which a test must catch.
# Each mutation goes through a temp file and cp; the test runs under a 300 s alarm.
block_tui_hint_mutation() {
	local label=tui-hint-mutation log="$tmp/b26.log" f rc=0 before after
	begin
	# (1) the receive path
	f="$wt/internal/tui/run.go"
	before="$(grep -c 'go c\.receiveLoop(' "$f" || true)"
	sed '/go c\.receiveLoop(/d' "$f" >"$tmp/x" && cp "$tmp/x" "$f"
	if [ "$before" -ne 1 ] || [ "$(grep -c 'receiveLoop(' "$f" || true)" -ne 0 ]; then
		problem "$label" "mutation 1 changed nothing"
	else
		(cd "$wt" && perl -e 'alarm 300; exec @ARGV' go test -count=1 -v -run '^TestBothModes$' ./internal/tui/ >"$log" 2>&1) && rc=0 || rc=$?
		if [ "$rc" -eq 142 ] || [ "$rc" -eq 124 ]; then problem "$label" "mutation 1: test timed out"; fi
		grep -qF -- '--- FAIL: TestBothModes/local' "$log" || problem "$label" "mutation 1: lacks FAIL local: $(tail -n 3 "$log" | tr '\n' ' ')"
		grep -qF -- '--- FAIL: TestBothModes/server' "$log" || problem "$label" "mutation 1: lacks FAIL server: $(tail -n 3 "$log" | tr '\n' ' ')"
	fi
	revert
	# (2) the session hint on the wire
	f="$wt/internal/tui/client.go"
	sed 's/^const sessionHint = "tui"$/const sessionHint = ""/' "$f" >"$tmp/x" && cp "$tmp/x" "$f"
	if [ "$(grep -c 'sessionHint = ""' "$f" || true)" -ne 1 ]; then
		problem "$label" "mutation 2 changed nothing"
	else
		(cd "$wt" && perl -e 'alarm 300; exec @ARGV' go test -count=1 -v -run '^TestBothModes$' ./internal/tui/ >"$log" 2>&1) && rc=0 || rc=$?
		if [ "$rc" -eq 142 ] || [ "$rc" -eq 124 ]; then problem "$label" "mutation 2: test timed out"; fi
		grep -qF -- '--- FAIL: TestBothModes/local' "$log" || problem "$label" "mutation 2: lacks FAIL local: $(tail -n 3 "$log" | tr '\n' ' ')"
		grep -qF -- '--- FAIL: TestBothModes/server' "$log" || problem "$label" "mutation 2: lacks FAIL server: $(tail -n 3 "$log" | tr '\n' ' ')"
	fi
	revert
	# (3) own echo on Receive
	before="$(grep -c 'IncludeOwn: true' "$f" || true)"
	sed 's|IncludeOwn: true, // receive-own|IncludeOwn: false, // receive-own|' "$f" >"$tmp/x" && cp "$tmp/x" "$f"
	after="$(grep -c 'IncludeOwn: true' "$f" || true)"
	if [ "$((before - after))" -ne 1 ] || [ "$(grep -c 'IncludeOwn: false, // receive-own' "$f" || true)" -ne 1 ]; then
		problem "$label" "mutation 3 changed nothing"
	else
		(cd "$wt" && perl -e 'alarm 300; exec @ARGV' go test -count=1 -run '^TestReceiveLoopIncludesOwnSends$' ./internal/tui/ >"$log" 2>&1) && rc=0 || rc=$?
		grep -qF -- '--- FAIL' "$log" || problem "$label" "mutation 3: lacks '--- FAIL': $(tail -n 3 "$log" | tr '\n' ' ')"
	fi
	finish "$label"
	revert
}

# --- 29. access-rule-sites-fires ---------------------------------------------------------
# Docker-free. TestAccessRuleSites (internal/core/access/sites_test.go) must
# pass on the clean tree and fail, naming the site, for one mutation per
# clause (issue #19): a condition in an unlisted function (the accesscheck plan's
# channels.go Delete and stophook.go checks), one more and one fewer occurrence
# in a listed function, a flag read outside a condition, ACL( on a receiver not
# named tx, a three-argument ACL( call, a package-level initializer, and a flag
# filter in the sqlstore SQL.
# sites_probe LABEL N FILE FROM TO WANT: replace the exact text FROM (exactly
# once in FILE) with TO, or append TO when FROM is empty, then require FAIL and
# WANT in the gate's log.
sites_probe() {
	local label="$1" n="$2" file="$3" from="$4" to="$5" want="$6" log="$tmp/b29.log" f="$wt/$3" content
	if [ -z "$from" ]; then
		printf '\n%s\n' "$to" >>"$f"
	elif [ "$(grep -cF -- "$from" "$f")" -ne 1 ]; then
		problem "$label" "mutation $n: anchor text not exactly once in $file: $from"
		return
	else
		content="$(cat "$f")"
		printf '%s\n' "${content//"$from"/"$to"}" >"$tmp/x" && cp "$tmp/x" "$f"
	fi
	if ! grep -qF -- "${to%%$'\n'*}" "$f"; then
		problem "$label" "mutation $n changed nothing in $file"
	else
		(cd "$wt" && go test -count=1 -run '^TestAccessRuleSites$' ./internal/core/access/ >"$log" 2>&1) || true
		grep -qF -- '--- FAIL: TestAccessRuleSites' "$log" || problem "$label" "mutation $n: lacks '--- FAIL: TestAccessRuleSites': $(tail -n 3 "$log" | tr '\n' ' ')"
		grep -qF -- "$want" "$log" || problem "$label" "mutation $n: log lacks '$want': $(grep -F "$file" "$log" | head -n 3 | tr '\n' ' ')"
	fi
	revert
}

block_access_rule_sites_fires() {
	local label=access-rule-sites-fires log="$tmp/b29z.log" c=internal/core/channels/channels.go
	begin
	if ! (cd "$wt" && go test -count=1 -run '^TestAccessRuleSites$' ./internal/core/access/ >"$log" 2>&1); then
		problem "$label" "clean tree: expected PASS: $(tail -n 3 "$log" | tr '\n' ' ')"
	fi
	sites_probe "$label" 1 "$c" 'if err := protected(ch, "deleted"); err != nil {' \
		'if p.Admin { return nil }
		if err := protected(ch, "deleted"); err != nil {' \
		"$c Delete: mentions Admin (1, none allowed)"
	sites_probe "$label" 2 internal/cli/stophook.go 'names = append(names, c.Channel)' \
		'if c.Kind == bus.KindMemory { continue }
		names = append(names, c.Channel)' \
		'internal/cli/stophook.go stopHookWork: mentions MemoryKind (1, none allowed)'
	sites_probe "$label" 3 "$c" 'scope := p.Scope' \
		'scope := p.Scope
		if p.Scope == "probe" { return nil }' \
		"$c Create: mentions Scope 5 times, 4 allowed"
	sites_probe "$label" 4 "$c" '}{"create_channel", in.Name, kind, acl, in.Scope}' \
		'}{"create_channel", in.Name, kind, acl, ""}' \
		"$c Create: mentions Scope 3 times, the allowlist holds 4"
	sites_probe "$label" 5 "$c" '' \
		'func probeAdmin(p access.Principal) bool { ok := p.Admin; return ok }' \
		"$c probeAdmin: mentions Admin (1, none allowed)"
	sites_probe "$label" 6 "$c" '' \
		'func probeACL(ctx context.Context, t store.Tx) error { _, err := t.ACL(ctx, 1); return err }' \
		"$c probeACL: calls ACL( (1, none allowed)"
	sites_probe "$label" 10 "$c" '' \
		'func (s Service) probeServiceACL(ctx context.Context) error { _, err := s.ACL(ctx, "pat", "general"); return err }' \
		"$c probeServiceACL: calls ACL( (1, none allowed)"
	sites_probe "$label" 7 "$c" '' \
		'var probeInit = func(p access.Principal) bool { return p.Admin }' \
		"$c <init:probeInit>: mentions Admin (1, none allowed)"
	sites_probe "$label" 8 internal/store/sqlstore/acl.go \
		'FROM channel_acl WHERE channel_id = ? ORDER BY id' \
		'FROM channel_acl WHERE channel_id = ? AND can_read = TRUE ORDER BY id' \
		'internal/store/sqlstore/acl.go ACL: SQL filters on can_* (1, none allowed)'
	sites_probe "$label" 9 internal/store/sqlstore/identities.go '' \
		'const probeAdmins = "SELECT name FROM identities" +
	" WHERE is_admin"' \
		'internal/store/sqlstore/identities.go <init:probeAdmins>: SQL filters on is_admin (1, none allowed)'
	finish "$label"
	revert
}

block_skip_in_wiring
block_unit_leak
block_gen_edited
block_gen_stray
block_gen_spec_edited
block_ci_gate_negative
block_ci_sim
block_search_postgres_phrase
block_search_sqlite_prefix
block_acl_check_removed
block_pending_batch_wakes
block_inproc_crosses_the_wire
block_deps_rule_fires
block_transport_check_fires
block_bearer_signature_ignored
block_admin_flag_ignored
block_m5_deps_rule_fires
block_m7_deps_rule_fires
block_notify_append_removed
block_advisory_lock_ignored
block_sqlite_lease_ignored
block_assertion_replay_ignored
block_m6_exit_script_fires
block_replaced_exit_ignored
block_m8_exit_script_fires
block_tui_hint_mutation
block_slackbridge_overlap_row
block_slackbridge_catchup_before_drain
block_access_rule_sites_fires

if [ "$bad" -ne 0 ]; then
	echo "check-gates-selftest: FAILED"
	exit 1
fi
echo "check-gates-selftest: OK"
