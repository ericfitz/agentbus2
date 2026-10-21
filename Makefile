.PHONY: build build-all vet fmt-check lint test test-conformance test-race tidy-check generate generate-check check-gates check-gates-selftest verify
export CGO_ENABLED=0
PKGS_UNIT = $$(go list ./... | grep -v '^github.com/ericfitz/agentbus2/internal/conformance')

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
	go tool oapi-codegen -config api/oapi-codegen-spec.yaml api/openapi.yaml
generate-check: generate  ## Fail when internal/gen is not exactly what api/ generates (modified, staged, deleted or untracked files after regeneration)
	@dirty=$$(git status --porcelain --untracked-files=all -- internal/gen); if [ -n "$$dirty" ]; then echo "internal/gen is stale or polluted (run make generate, then commit or remove):"; echo "$$dirty"; git --no-pager diff --stat -- internal/gen; exit 1; fi
test-conformance:  ## Store and Bus conformance suites on SQLite and Postgres; -count=1 so an unreachable Postgres can never be masked by the test cache
	go test -count=1 -p 1 ./internal/conformance/...
	@# the bus suite runs through local.New and through the generated client over the in-process transport (spec 10, Milestone 4 exit)
	AGENTBUS2_BUS_TRANSPORT=inproc go test -count=1 -p 1 ./internal/conformance/bus/
check-gates:  ## Prove the negative halves: an unreachable Postgres fails the suites, unit tests exclude the suites, nothing skips, the generator reacts to the schema, the suites fail with no Docker and no variable (needs Docker for the busybox run)
	bash scripts/check-gates.sh
check-gates-selftest:  ## Prove the gate scripts fire: one mutation per check in a throwaway worktree of HEAD (minutes; its own CI job, not part of verify)
	bash scripts/check-gates-selftest.sh
verify:      ## Done gate (CLAUDE.md): build, vet, gofmt, lint, generated-code freshness, unit tests, conformance suites on both engines, gate checks
	$(MAKE) build
	$(MAKE) build-all
	$(MAKE) vet
	$(MAKE) fmt-check
	$(MAKE) lint
	$(MAKE) generate-check
	$(MAKE) test
	$(MAKE) test-conformance
	$(MAKE) check-gates
	@echo "verify: OK"
