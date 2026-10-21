.PHONY: build build-all vet fmt-check lint test test-race tidy-check generate generate-check
export CGO_ENABLED=0
PKGS_UNIT = $$(go list ./... | grep -v '/internal/conformance')

build:       ## Build the agentbus2 binary (no cgo), as the release build will
	go build -trimpath -o agentbus2 .
build-all:   ## Compile every package
	go build ./...
vet:
	go vet ./...
fmt-check:   ## gofmt over this module's Go files (files, never directories: gofmt recurses into a directory argument)
	@out=$$(gofmt -l $$(go list -f '{{range .GoFiles}}{{$$.Dir}}/{{.}} {{end}}{{range .TestGoFiles}}{{$$.Dir}}/{{.}} {{end}}{{range .XTestGoFiles}}{{$$.Dir}}/{{.}} {{end}}' ./...)); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi
lint:        ## golangci-lint v2 default linters (no .golangci.yml)
	golangci-lint run ./...
test:        ## Unit tests: every package except internal/conformance (no Docker needed)
	go test $(PKGS_UNIT)
test-race:   ## Every package under the race detector, suites included (needs Postgres like verify)
	go test -race -count=1 ./...
tidy-check:  ## go.mod and go.sum are tidy (CI step; not in verify, pending Eric)
	go mod tidy -diff
generate:    ## Regenerate internal/gen from api/openapi.yaml with the pinned oapi-codegen
	go tool oapi-codegen -config api/oapi-codegen.yaml api/openapi.yaml
generate-check: generate  ## Fail when internal/gen is not exactly what api/ generates (modified, staged, deleted or untracked files after regeneration)
	@dirty=$$(git status --porcelain --untracked-files=all -- internal/gen); if [ -n "$$dirty" ]; then echo "internal/gen is stale or polluted (run make generate, then commit or remove):"; echo "$$dirty"; git --no-pager diff --stat -- internal/gen; exit 1; fi
