#!/usr/bin/env bash
# agentbus2 code-generator spike: oapi-codegen vs ogen.
# Re-runnable from scratch; every path derives from this file's location.
# Exit status reflects the main matrix only (generate, tidy, build, vet, test
# per generator). Probes are informational and recorded in the summary.
set -euo pipefail

SPIKE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SPEC="$SPIKE/openapi.yaml"
LOGS="$SPIKE/logs"
OAPI_VER="v2.8.0"
OGEN_VER="v1.24.0"
export CGO_ENABLED=0

rm -rf "$LOGS"
mkdir -p "$LOGS"
RESULTS=()
FAIL=0

# step NAME CMD...: main matrix; a failure flips the exit status.
step() {
  local name="$1"; shift
  local log="$LOGS/$name.log"
  if "$@" >"$log" 2>&1; then
    RESULTS+=("PASS  $name")
  else
    RESULTS+=("FAIL  $name (exit $?) -> $log")
    FAIL=1
  fi
}

# probe NAME CMD...: informational; result recorded, exit status untouched.
probe() {
  local name="$1"; shift
  local log="$LOGS/$name.log"
  if "$@" >"$log" 2>&1; then
    RESULTS+=("ok    $name")
  else
    RESULTS+=("err   $name (exit $?) -> $log")
  fi
}

metrics() {
  local dir="$1" pkg="$2"
  cd "$dir"
  echo "generated lines: $(cat "$pkg"/*.go | wc -l | tr -d ' ') in $(find "$pkg" -maxdepth 1 -name '*.go' | wc -l | tr -d ' ') files"
  echo "non-std packages linked by ./$pkg: $(go list -deps "./$pkg" | grep '\.' | grep -v '^spike' | grep -c -v '^vendor/')"
  echo "non-std modules linked by ./$pkg:"
  go list -deps -f '{{if and .Module (not .Standard)}}{{.Module.Path}}{{end}}' "./$pkg" | grep -v '^spike' | sort -u | sed 's/^/  /'
  echo "modules in go.mod graph (go list -m all): $(go list -m all | wc -l | tr -d ' ')"
  echo "tool directive pins:"; grep -A1 '^tool' go.mod | sed 's/^/  /'
}

cleanup() {
  rm -rf "$SPIKE/oapi/gen" "$SPIKE/oapi/probes" "$SPIKE/ogen/gen" "$SPIKE/ogen/probes"
}
cleanup

echo "== versions"
{
  go version
  echo "go list -m -versions github.com/oapi-codegen/oapi-codegen/v2 | tail: $(go list -m -versions github.com/oapi-codegen/oapi-codegen/v2 | tr ' ' '\n' | tail -1)"
  echo "go list -m -versions github.com/ogen-go/ogen | tail: $(go list -m -versions github.com/ogen-go/ogen | tr ' ' '\n' | tail -1)"
  (cd "$SPIKE/oapi" && echo "pinned oapi-codegen: $(go list -m -f '{{.Version}}' github.com/oapi-codegen/oapi-codegen/v2) (go tool oapi-codegen -version: $(go tool oapi-codegen -version | tail -1))")
  (cd "$SPIKE/ogen" && echo "pinned ogen: $(go list -m -f '{{.Version}}' github.com/ogen-go/ogen) (go tool ogen -version: $(go tool ogen -version | head -1))")
} | tee "$LOGS/versions.log"
[ "$(cd "$SPIKE/oapi" && go list -m -f '{{.Version}}' github.com/oapi-codegen/oapi-codegen/v2)" = "$OAPI_VER" ] || { echo "oapi-codegen pin drifted"; exit 2; }
[ "$(cd "$SPIKE/ogen" && go list -m -f '{{.Version}}' github.com/ogen-go/ogen)" = "$OGEN_VER" ] || { echo "ogen pin drifted"; exit 2; }

echo "== oapi-codegen $OAPI_VER: main matrix"
cd "$SPIKE/oapi"
mkdir -p gen
step oapi-generate go tool oapi-codegen -config cfg.yaml "$SPEC"
step oapi-tidy go mod tidy
step oapi-build go build ./...
step oapi-vet go vet ./...
step oapi-test go test -count=1 -timeout 120s -v ./...
metrics "$SPIKE/oapi" gen >"$LOGS/oapi-metrics.log" 2>&1 || true

echo "== ogen $OGEN_VER: main matrix"
cd "$SPIKE/ogen"
step ogen-generate go tool ogen -config ogen.yml -target gen -package gen -clean "$SPEC"
step ogen-tidy go mod tidy
step ogen-build go build ./...
step ogen-vet go vet ./...
step ogen-test go test -count=1 -timeout 120s -v ./...
metrics "$SPIKE/ogen" gen >"$LOGS/ogen-metrics.log" 2>&1 || true

echo "== probes"
# P1: ogen with the openIdConnect scheme and no escape hatch (expected: hard error).
# ogen auto-loads ./ogen.yml from the cwd, so run from a directory without one.
mkdir -p "$SPIKE/ogen/probes/noignore"
cd "$SPIKE/ogen/probes"
probe ogen-oidc-no-escape-hatch go tool ogen -target noignore -package noignore -clean "$SPEC"

# P2: oapi-codegen legacy scopes-on-context bridge flag.
cd "$SPIKE/oapi"
mkdir -p probes/scopes
cat >probes/scopes/cfg.yaml <<EOF
package: scopes
output: probes/scopes/gen.go
generate:
  std-http-server: true
  strict-server: true
  client: true
  models: true
compatibility:
  enable-auth-scopes-on-context: true
EOF
probe oapi-scopes-flag-generate go tool oapi-codegen -config probes/scopes/cfg.yaml "$SPEC"
probe oapi-scopes-flag-build go build ./probes/scopes
grep -n -i 'scopes\|ContextKey' probes/scopes/gen.go >"$LOGS/oapi-scopes-flag-grep.log" 2>&1 || true

# P3: oapi-codegen without embedded-spec (how small does the runtime get?).
mkdir -p probes/min
cat >probes/min/cfg.yaml <<EOF
package: min
output: probes/min/gen.go
generate:
  std-http-server: true
  strict-server: true
  client: true
  models: true
EOF
probe oapi-min-generate go tool oapi-codegen -config probes/min/cfg.yaml "$SPEC"
probe oapi-min-build go build ./probes/min
metrics "$SPIKE/oapi" probes/min >"$LOGS/oapi-min-metrics.log" 2>&1 || true

# P4: OpenAPI 3.1 on both (only the version string changes).
sed 's/^openapi: 3.0.3$/openapi: 3.1.0/' "$SPEC" >"$LOGS/openapi-3.1.yaml"
cd "$SPIKE/oapi"
mkdir -p probes/v31
printf 'package: v31\noutput: probes/v31/gen.go\ngenerate:\n  std-http-server: true\n  strict-server: true\n  client: true\n  models: true\n' >probes/v31/cfg.yaml
probe oapi-openapi31-generate go tool oapi-codegen -config probes/v31/cfg.yaml "$LOGS/openapi-3.1.yaml"
probe oapi-openapi31-build go build ./probes/v31
cd "$SPIKE/ogen"
mkdir -p probes/v31
probe ogen-openapi31-generate go tool ogen -config ogen.yml -target probes/v31 -package v31 -clean "$LOGS/openapi-3.1.yaml"
probe ogen-openapi31-build go build ./probes/v31

# P5: freshness check (what CI would run): regenerate and diff against the committed output.
cd "$SPIKE/oapi"
cp -R gen "$LOGS/oapi-gen-before"
probe oapi-fresh-regenerate go tool oapi-codegen -config cfg.yaml "$SPEC"
probe oapi-fresh-diff diff -r "$LOGS/oapi-gen-before" gen
cd "$SPIKE/ogen"
cp -R gen "$LOGS/ogen-gen-before"
probe ogen-fresh-regenerate go tool ogen -config ogen.yml -target gen -package gen -clean "$SPEC"
probe ogen-fresh-diff diff -r "$LOGS/ogen-gen-before" gen

# P6: whole module still builds and vets with the probe packages present.
cd "$SPIKE/oapi"; probe oapi-build-with-probes go build ./...
cd "$SPIKE/ogen"; probe ogen-build-with-probes go build ./...

echo
echo "== summary (logs in $LOGS)"
printf '%s\n' "${RESULTS[@]}" | tee "$LOGS/summary.log"
echo
echo "== metrics"
for g in oapi ogen oapi-min; do echo "-- $g"; cat "$LOGS/$g-metrics.log"; done
echo
if [ "$FAIL" -ne 0 ]; then echo "RESULT: FAIL"; exit 1; fi
echo "RESULT: PASS"
