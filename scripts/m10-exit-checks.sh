#!/usr/bin/env bash
# Milestone 10 exit evidence: the MCP-only binary (issue #12).
#   usage: m10-exit-checks.sh <e2e.log>     the TestMCPOnly counts, then --deps-only
#          m10-exit-checks.sh --deps-only   dependency rules alone (Docker-free)
# <e2e.log> is a `go test -count=1 -v ./internal/e2e/` log. It must exist and be
# non-empty (exit 2 naming the path). The script prints
#   mcponly=<n>  the nine leaves with a `--- PASS: TestMCPOnly/<name> (` line,
#                each name counted once
#   help=<n>     the six leaves with a `--- PASS: TestHelpExamples/<name> (` line,
#                each name counted once
#   fail=<n>     every `--- FAIL` line
#   pkg_ok=<0|1> 1 when the package's `ok` line is present and no `FAIL` line is
# then runs --deps-only, and exits 1 unless mcponly=9, help=6, fail=0, pkg_ok=1 and the
# dependency rules hold.
# open note: mcp-only-client 9: --deps-only runs inside `make verify` (seconds,
# no Docker); the m4 to m8 --deps-only runs stay in the gates-selftest job.
#
# --deps-only checks three things and prints deps_ok=1 and modules=13 when all
# hold; otherwise one line per hit and exit 1:
#   (a) transitive: every package of `go list -deps ./cmd/agentmesh-mcponly`
#       that is internal/core/... (except internal/core/notify, a leaf),
#       internal/{server,tui,cli,slackapi,slackbridge,bus/local,client/localmode},
#       internal/store/{sqlite,postgres,sqlstore}, internal/gen/spec, pgx,
#       kin-openapi, gorilla/mux, slack-go, modernc.org/ or charm.land/ prints
#       deps_violation=<pkg> [rule transitive]
#   (b) direct: the imports of ./internal/mcpcli and ./cmd/agentmesh-mcponly
#       that are internal/{cli,gen,store}/... print
#       deps_violation=<pkg> imports <imp> [rule direct]
#   (c) modules: the built binary's `go version -m` dep lines name only the
#       allowlisted modules; any other prints module_violation=<module>
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MOD=github.com/ericfitz/agentmesh
BIN=./cmd/agentmesh-mcponly
# The 13 modules the binary may link (plan Global Constraints; extending the
# list is Eric's decision, never an implementer's).
ALLOWED_MODULES=(
	github.com/apapsch/go-jsonmerge/v2
	github.com/go-jose/go-jose/v4
	github.com/google/jsonschema-go
	github.com/google/uuid
	github.com/modelcontextprotocol/go-sdk
	github.com/oapi-codegen/runtime
	github.com/segmentio/asm
	github.com/segmentio/encoding
	github.com/yosida95/uritemplate/v3
	golang.org/x/oauth2
	golang.org/x/sync
	golang.org/x/sys
	golang.org/x/time
)

usage() { echo "usage: $0 <e2e.log> | --deps-only" >&2; }

# The leaf names of internal/e2e's TestHelpExamples, one per help text with an
# example (scripts/check-gates-selftest.sh block 33 keeps the same list).
HELP_LEAVES=(overview connect register configure verify troubleshoot)

# The leaf names of internal/e2e's TestMCPOnly (scripts/check-gates-selftest.sh
# block 33 keeps the same list).
LEAVES=(1-check-reach 2-register-from-token-file 3-register-again 4-check-identity 5-config 6-serve-stdio 7-register-without-token 8-check-unregistered-key 9-no-local-mode)

if [ "$#" -ne 1 ]; then
	usage
	exit 2
fi
if [ "$1" != "--deps-only" ]; then
	log="$1"
	if [ ! -s "$log" ]; then
		echo "m10-exit-checks: log '$log' is missing or empty" >&2
		exit 2
	fi
	mcponly=0
	for name in "${LEAVES[@]}"; do
		if grep -qE "^[[:space:]]+--- PASS: TestMCPOnly/$name \\(" "$log"; then
			mcponly=$((mcponly + 1))
		fi
	done
	help=0
	for name in "${HELP_LEAVES[@]}"; do
		if grep -qE "^[[:space:]]+--- PASS: TestHelpExamples/$name \\(" "$log"; then
			help=$((help + 1))
		fi
	done
	fail="$(grep -c -E '^[[:space:]]*--- FAIL' "$log" || true)"
	pkg_ok=0
	if grep -qE '^ok[[:space:]]+github\.com/ericfitz/agentmesh/internal/e2e([[:space:]]|$)' "$log" && ! grep -qE '^FAIL([[:space:]]|$)' "$log"; then
		pkg_ok=1
	fi
	echo "mcponly=$mcponly"
	echo "help=$help"
	echo "fail=$fail"
	echo "pkg_ok=$pkg_ok"
	rc=0
	if [ "$mcponly" -ne "${#LEAVES[@]}" ] || [ "$help" -ne "${#HELP_LEAVES[@]}" ] || [ "$fail" -ne 0 ] || [ "$pkg_ok" -ne 1 ]; then
		echo "m10-exit-checks: want mcponly=${#LEAVES[@]}, help=${#HELP_LEAVES[@]}, fail=0 and pkg_ok=1" >&2
		rc=1
	fi
	bash "${BASH_SOURCE[0]}" --deps-only || rc=1
	exit "$rc"
fi

violations=0
tmp="$(mktemp -d "${TMPDIR:-/tmp}/m10.XXXXXX")"
trap 'rm -rf "$tmp"' EXIT

cd "$ROOT"

# (a) the transitive set.
transitive="^$MOD/internal/core(/|\$)|^$MOD/internal/(server|tui|cli|slackapi|slackbridge|bus/local|client/localmode)(/|\$)|^$MOD/internal/store/(sqlite|postgres|sqlstore)(/|\$)|^$MOD/internal/gen/spec\$|^github.com/(jackc/pgx|getkin/kin-openapi|gorilla/mux|slack-go/slack)|^modernc.org/|^charm.land/"
go list -deps "$BIN" >"$tmp/deps.txt"
while IFS= read -r pkg; do
	[ -n "$pkg" ] || continue
	[ "$pkg" = "$MOD/internal/core/notify" ] && continue
	if printf '%s\n' "$pkg" | grep -E -q -- "$transitive"; then
		echo "deps_violation=$pkg [rule transitive]"
		violations=$((violations + 1))
	fi
done <"$tmp/deps.txt"

# (b) the direct imports of the command package and main.
# shellcheck disable=SC2016 # the template is for go list
go list -f '{{$p := .ImportPath}}{{range .Imports}}{{$p}} {{.}}{{"\n"}}{{end}}' ./internal/mcpcli "$BIN" >"$tmp/direct.txt"
while IFS= read -r line; do
	[ -n "$line" ] || continue
	pkg="${line%% *}"
	imp="${line#* }"
	if printf '%s\n' "$imp" | grep -E -q -- "^$MOD/internal/(cli|gen|store)(/|\$)"; then
		echo "deps_violation=$pkg imports $imp [rule direct]"
		violations=$((violations + 1))
	fi
done <"$tmp/direct.txt"

# (c) the linked modules of the built binary.
go build -trimpath -o "$tmp/agentmesh-mcponly" "$BIN"
go version -m "$tmp/agentmesh-mcponly" | awk '$1 == "dep" { print $2 }' >"$tmp/modules.txt"
while IFS= read -r m; do
	[ -n "$m" ] || continue
	ok=0
	for a in "${ALLOWED_MODULES[@]}"; do
		if [ "$m" = "$a" ]; then ok=1; break; fi
	done
	if [ "$ok" -eq 0 ]; then
		echo "module_violation=$m"
		violations=$((violations + 1))
	fi
done <"$tmp/modules.txt"

count="$(grep -c . "$tmp/modules.txt" || true)"
if [ "$count" -ne "${#ALLOWED_MODULES[@]}" ]; then
	echo "m10-exit-checks: the binary links $count modules, want ${#ALLOWED_MODULES[@]}" >&2
	violations=$((violations + 1))
fi
if [ "$violations" -ne 0 ]; then
	exit 1
fi
echo "deps_ok=1"
echo "modules=$count"
