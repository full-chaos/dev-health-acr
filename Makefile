.PHONY: fmt fmt-check test test-race test-shuffle-random test-coverage crosscompile hosted-integration clients-real vet contract-write contract-test codegraph-contract canonical-receipts build verify release-local release-verify container-contract container-pins container-test container-reproducible container-oci container-scan fullstack-opencode-e2e fullstack-contract

RELEASE_OUTPUT ?= .tmp/release
RELEASE_VERSION ?=
GOTEST_SHUFFLE_SEED ?= 20260727
GOTEST_TIMEOUT ?= 300s
VERSION_PKG := github.com/full-chaos/dev-health-acr/internal/version

# Pinned exact versions (not @latest) so the coverage/JUnit toolchain is
# reproducible across CI and local runs. gotestsum wraps `go test`, so the
# JUnit report and the coverage profile come from a single test run rather
# than running the suite twice; gocover-cobertura then converts the Go
# coverage profile into the Cobertura XML the TestOps ingester's coverage
# sniffer accepts.
GOTESTSUM_VERSION := v1.13.0
GOCOVER_COBERTURA_VERSION := v1.5.0
COVERAGE_DIR ?= .tmp/coverage
COVERAGE_PROFILE := $(COVERAGE_DIR)/cover.out
COVERAGE_COBERTURA := $(COVERAGE_DIR)/coverage.xml
COVERAGE_JUNIT := $(COVERAGE_DIR)/junit.xml
LOCAL_BUILD_VERSION := $(shell awk -F'"' '/^const localBuildVersion = / { print $$2; exit }' internal/version/version.go)
LOCAL_BUILD_COMMIT := $(shell git rev-parse HEAD)
LOCAL_BUILD_DATE := $(shell TZ=UTC0 git show -s --format=%cd --date=format-local:%Y-%m-%dT%H:%M:%SZ HEAD)
LOCAL_BUILD_LDFLAGS := -buildid= -X $(VERSION_PKG).Version=$(LOCAL_BUILD_VERSION) -X $(VERSION_PKG).Commit=$(LOCAL_BUILD_COMMIT) -X $(VERSION_PKG).Date=$(LOCAL_BUILD_DATE)

# Full-stack Context Fabric acceptance (CHAOS-3065). See docs/fullstack-acceptance.md.
# The product repo is the parent of this checkout for a plain clone, but a git worktree lives
# at <root>/acr/worktrees/<name>, where the parent is the worktrees directory. Pick the
# nearest ancestor that actually holds the product compose file, so the documented one-command
# entry point works from either layout; an explicit DEV_HEALTH_ROOT always wins.
DEV_HEALTH_ROOT ?= $(firstword $(foreach d,$(abspath ..) $(abspath ../..) $(abspath ../../..) $(abspath ../../../..),$(if $(wildcard $(d)/compose.yml),$(d))))
E2E_COMPOSE ?= $(DEV_HEALTH_ROOT)/compose.yml
E2E_WEB_ROOT ?= $(DEV_HEALTH_ROOT)/web
E2E_SCENARIO ?= smoke
E2E_MODEL ?= scripted
E2E_WEB ?= auto
E2E_PROJECT ?= acr-fs-$(shell date -u +%Y%m%d)-$(shell echo $$$$)

fmt:
	gofmt -w $$(find . -name '*.go' -not -path './vendor/*')

fmt-check:
	@files="$$(gofmt -l $$(find . -name '*.go' -not -path './vendor/*'))"; \
	if [ -n "$$files" ]; then echo "Go files need formatting:"; echo "$$files"; exit 1; fi

test:
	go test -count=1 ./...

# test-race runs the same suite with the race detector and randomized test
# order. -count=1 defeats the build cache so a warm-cache result cannot stand
# in for a fresh run, and the pinned shuffle seed catches order-coupled state
# that only passes because an earlier test happened to leave a seam in the
# right position) that a fixed run order can hide indefinitely. This is
# intentionally separate from `test` rather than folded into it: the race
# detector and shuffled order both add real wall-clock time, and the two
# variants catch different classes of defect -- a cold non-race run still
# matters on its own for the timing-sensitive opener/reap tests.
test-race:
	go test -count=1 -race -shuffle=$(GOTEST_SHUFFLE_SEED) -timeout $(GOTEST_TIMEOUT) ./...

# Keep randomized order discovery out of the deterministic verification gate.
test-shuffle-random:
	go test -count=1 -race -shuffle=on -timeout $(GOTEST_TIMEOUT) ./...

# test-coverage produces machine-readable CI artifacts: JUnit XML for test
# results and Cobertura XML for coverage. gotestsum wraps a single `go test`
# invocation (forwarding everything after `--`) so both reports come from one
# run of the suite, not two. This is additive to `test`/`test-race`, not a
# replacement -- those stay the plain, dependency-free gate; this target adds
# reporting on top for CI to publish.
#
# CI uploads both reports with `if: always()` specifically so a failing run
# still leaves a JUnit file showing what failed and a coverage file for the
# lines that did execute. That only works if this target itself keeps going
# past a failing `go test`: the Cobertura conversion below runs whenever a
# coverage profile exists, and the target still exits with the original test
# status afterward, rather than aborting (make's default) at the first
# non-zero command and leaving coverage.xml missing on the one path where
# CI's `if: always()` upload was meant to catch it.
test-coverage:
	mkdir -p $(COVERAGE_DIR)
	status=0; \
	go run gotest.tools/gotestsum@$(GOTESTSUM_VERSION) --junitfile $(COVERAGE_JUNIT) -- \
		-count=1 -coverprofile=$(COVERAGE_PROFILE) ./... || status=$$?; \
	if [ -f $(COVERAGE_PROFILE) ]; then \
		go run github.com/boumenot/gocover-cobertura@$(GOCOVER_COBERTURA_VERSION) < $(COVERAGE_PROFILE) > $(COVERAGE_COBERTURA) || status=$$?; \
	fi; \
	exit $$status

crosscompile:
	GOOS=windows GOARCH=amd64 go build ./...
	GOOS=windows GOARCH=amd64 go vet ./...
	GOOS=darwin GOARCH=arm64 go vet ./...
	GOOS=linux GOARCH=amd64 go vet ./...

hosted-integration:
	ACR_HOSTED_INTEGRATION=1 go test ./cmd/acr-api -run '^TestHostedRuntime_real_binary_serves_and_fails_readiness_safely$$' -count=1 -v

clients-real:
	bash scripts/clients/test-conformance.sh --clients opencode,claude-code,codex,cursor
	bash scripts/clients/test-real-clients.sh --self-test
	bash scripts/clients/test-real-clients.sh --self-test-release-dir
	bash scripts/clients/test-real-clients.sh --self-test-leaked-home

vet:
	go vet ./...

contract-write:
	go run ./cmd/contractcheck -write

contract-test:
	go run ./cmd/contractcheck

codegraph-contract:
	bash scripts/codegraph/verify-contract.sh --fixtures testdata/codegraph/v1.2.0

canonical-receipts:
	bash scripts/e2e/test-canonical-receipts.sh

fullstack-contract:
	bash scripts/e2e/test-fullstack-opencode.sh
	bash scripts/e2e/test-opencode-runtime-fixture.sh

fullstack-opencode-e2e:
	bash scripts/e2e/fullstack-opencode.sh \
		--compose "$(E2E_COMPOSE)" \
		--overlay deploy/compose/acr.compose.yml \
		--web-root "$(E2E_WEB_ROOT)" \
		--project "$(E2E_PROJECT)" \
		--scenario "$(E2E_SCENARIO)" \
		--model "$(E2E_MODEL)" \
		--web "$(E2E_WEB)"

build:
	go build -ldflags "$(LOCAL_BUILD_LDFLAGS)" -o .tmp/acr-api ./cmd/acr-api
	go build -ldflags "$(LOCAL_BUILD_LDFLAGS)" -o .tmp/acr-mcp ./cmd/acr-mcp
	go build -o .tmp/contractcheck ./cmd/contractcheck
	go build -o .tmp/acr-migrate ./cmd/acr-migrate
	go build -ldflags "$(LOCAL_BUILD_LDFLAGS)" -o .tmp/acr-projector ./cmd/acr-projector

verify: fmt-check vet test test-race crosscompile contract-test codegraph-contract canonical-receipts fullstack-contract build

container-contract:
	bash scripts/container/test-contract.sh

container-pins:
	bash scripts/container/verify-pins.sh

container-test: container-contract
	bash scripts/container/smoke.sh

container-reproducible:
	bash scripts/container/reproducible.sh

container-oci:
	bash scripts/container/oci.sh

container-scan:
	bash scripts/container/scan.sh

release-local:
	@test -n "$(RELEASE_VERSION)" || (echo "RELEASE_VERSION must be a canonical version such as 1.2.3"; exit 1)
	go run ./cmd/releasebuild build --root . --out "$(RELEASE_OUTPUT)" --version "$(RELEASE_VERSION)" --commit "$$(git rev-parse HEAD)" --date "$$(TZ=UTC0 git show -s --format=%cd --date=format-local:%Y-%m-%dT%H:%M:%SZ HEAD)"

release-verify:
	go run ./cmd/releasebuild verify --dir "$(RELEASE_OUTPUT)"
