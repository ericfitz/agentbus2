#!/usr/bin/env bash
# Milestone 10 exit evidence: the MCP-only binary (issue #12).
#   usage: m10-exit-checks.sh <e2e.log>     (Task 5 adds the e2e counts)
#          m10-exit-checks.sh --deps-only   dependency rules alone (Docker-free)
# open note: mcp-only-client 9: --deps-only runs inside `make verify` (seconds,
# no Docker); the m4 to m8 --deps-only runs stay in the gates-selftest job.
#
# --deps-only checks three things and prints deps_ok=1 and modules=13 when all
# hold; otherwise one line per hit and exit 1:
#   (a) transitive: every package of `go list -deps ./cmd/agentbus2-mcponly`
#       that is internal/core/... (except internal/core/notify, a leaf),
#       internal/{server,tui,cli,slackapi,slackbridge,bus/local,client/localmode},
#       internal/store/{sqlite,postgres,sqlstore}, internal/gen/spec, pgx,
#       kin-openapi, gorilla/mux, slack-go, modernc.org/ or charm.land/ prints
#       deps_violation=<pkg> [rule transitive]
#   (b) direct: the imports of ./internal/mcpcli and ./cmd/agentbus2-mcponly
#       that are internal/{cli,gen,store}/... print
#       deps_violation=<pkg> imports <imp> [rule direct]
#   (c) modules: the built binary's `go version -m` dep lines name only the
#       allowlisted modules; any other prints module_violation=<module>
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MOD=github.com/ericfitz/agentbus2
BIN=./cmd/agentbus2-mcponly
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

if [ "$#" -ne 1 ]; then
	usage
	exit 2
fi
if [ "$1" != "--deps-only" ]; then
	echo "m10-exit-checks: only --deps-only is implemented in this task" >&2
	usage
	exit 2
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
go build -trimpath -o "$tmp/agentbus2-mcponly" "$BIN"
go version -m "$tmp/agentbus2-mcponly" | awk '$1 == "dep" { print $2 }' >"$tmp/modules.txt"
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
