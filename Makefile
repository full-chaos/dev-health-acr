.PHONY: fmt fmt-check test test-race test-race-shared test-race-isolated test-race-split test-shuffle-random test-coverage crosscompile hosted-integration clients-real vet contract-write contract-test codegraph-contract shard-plan canonical-receipts build verify release-local release-verify container-contract container-pins container-test container-reproducible container-oci container-scan fullstack-opencode-e2e fullstack-contract

RELEASE_OUTPUT ?= .tmp/release
RELEASE_VERSION ?=
GOTEST_SHUFFLE_SEED ?= 20260727
# CHAOS-3972 raised this from 300s to 420s because
# internal/contextfabric/devhealthschema's full-repo declaration sweep
# (TestNoSecondPhysicalSourceOutsideTheDeclaration) -- CPU-bound,
# regex-matching every line of every .go file under -race's full
# instrumentation cost -- was living on a hair-trigger against the old
# ceiling and tripped it twice in one night as the module grew. That test's
# cost scales with the whole module's size, not with whatever shard it
# happened to land in, so every package sharing its shard was paying rent
# on its growth out of this same budget.
#
# CHAOS-3974 moved that package out: scripts/ci/test-shard.sh excludes it
# from the race-matrix shards this default governs, and it now runs in its
# own CI job (race-devhealthschema) with its own explicit, larger
# GOTEST_TIMEOUT override. This default is deliberately left at 420s rather
# than reverted to 300s -- there is no live measurement of how close other
# packages now sit to 300s on a shared runner, and tightening it back down
# without that evidence risks trading one flake class for another. What
# CHAOS-3974 actually fixes is that this default no longer HAS to keep
# growing on devhealthschema's account: the next time that walk gets more
# expensive, only race-devhealthschema's own timeout needs to move.
GOTEST_TIMEOUT ?= 420s
# CI partitions the module across shard runners by passing an explicit
# package list; the default is the whole module so local `make test*`
# invocations are unchanged.
GOTEST_PKGS ?= ./...
# CHAOS-4567: the timeout the isolated packages (scripts/ci/test-shard.sh
# isolated -- today internal/contextfabric/devhealthschema) run under when
# test-race-split runs them on their own. Mirrors ci.yml's race-devhealthschema
# job, which has passed GOTEST_TIMEOUT=900s since CHAOS-3974; keep the two
# in step. This is the ONLY budget that has to grow when the full-repo walk
# gets more expensive -- GOTEST_TIMEOUT above must not move on its account.
GOTEST_ISOLATED_TIMEOUT ?= 900s
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
COVERAGE_JSON := $(COVERAGE_DIR)/go-test.json
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
	go test -count=1 $(GOTEST_PKGS)

# test-race runs the same suite with the race detector and randomized test
# order. -count=1 defeats the build cache so a warm-cache result cannot stand
# in for a fresh run, and the pinned shuffle seed catches order-coupled state
# that only passes because an earlier test happened to leave a seam in the
# right position) that a fixed run order can hide indefinitely. This no
# longer contrasts with a separate plain CI run: in CI, the cold non-race run
# is now the coverage-instrumented `test-coverage` run below, while `make
# test` stays the plain, dependency-free local gate.
test-race:
	go test -count=1 -race -shuffle=$(GOTEST_SHUFFLE_SEED) -timeout $(GOTEST_TIMEOUT) $(GOTEST_PKGS)

# CHAOS-4567: the split ci.yml already runs (CHAOS-3974) -- the shared
# packages under GOTEST_TIMEOUT, the isolated packages under their own,
# larger budget -- as ONE target, so `make verify` locally and the Release
# workflow's `make verify` exercise the same partition CI does. Before this,
# release.yml ran `test-race` unsharded over ./... at 420s and
# devhealthschema's full-repo walk timed it out on every main merge from
# a4e2e5a3 (#327) on, which stopped image publication (GHCR :latest stuck
# at 87bf4d06) while PR CI stayed green on its isolated shard.
# scripts/ci/test-shard.sh is the single source for both sets: `1 1` is
# every non-isolated package (one shard of one), `isolated` is the rest, so
# scripts/ci/test-shard-closure.sh's proof that the two cover `go list
# ./...` exactly once applies here unchanged.
test-race-shared:
	$(MAKE) test-race GOTEST_PKGS="$$(scripts/ci/test-shard.sh 1 1)"

test-race-isolated:
	$(MAKE) test-race GOTEST_PKGS="$$(scripts/ci/test-shard.sh isolated)" GOTEST_TIMEOUT=$(GOTEST_ISOLATED_TIMEOUT)

test-race-split: test-race-shared test-race-isolated

# Keep randomized order discovery out of the deterministic verification gate.
test-shuffle-random:
	go test -count=1 -race -shuffle=on -timeout $(GOTEST_TIMEOUT) $(GOTEST_PKGS)

# test-coverage produces machine-readable CI artifacts: JUnit XML for test
# results, Cobertura XML for coverage, and a gotestsum JSON stream for
# failure analysis. gotestsum wraps a single `go test` invocation (forwarding
# everything after `--`), so the JSON stream, the JUnit report, and the
# coverage profile all come from this one run of the suite, not three. CI's
# failure-analysis step reads $(COVERAGE_JSON) directly. This is additive to
# `test`/`test-race`, not a replacement -- those stay the plain,
# dependency-free gate; this target adds reporting on top for CI to publish.
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
	go run gotest.tools/gotestsum@$(GOTESTSUM_VERSION) --junitfile $(COVERAGE_JUNIT) --jsonfile $(COVERAGE_JSON) -- \
		-count=1 -coverprofile=$(COVERAGE_PROFILE) $(GOTEST_PKGS) || status=$$?; \
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

# CHAOS-4100: offline checks for the trial launcher's shard layout. Starts
# no container, touches no database, calls no model -- see the script's own
# header for why the layout specifically is worth gating.
# CHAOS-4525 added test-seed-corpus-cases.sh here. It is offline by
# construction -- no container, no database, no model, no cluster (the live
# graph is stubbed through ACR_TRIAL_SEED_FALKOR_BIN) -- and skips itself when
# jq is absent.
#
# test-kiac-dsn-reader.sh is DELIBERATELY NOT in this target, despite covering
# guards CHAOS-4525 added (codex review R2 P1, reproduced): it sources
# scripts/trial/common.sh, which hard-exits at SOURCE time unless a sibling
# dev-health checkout with ops/.env exists. The contracts job and release
# `make verify` run in a standalone checkout that has neither, so putting it
# here turned a required check red -- confirmed by cloning this branch to a
# bare directory and running the target ("kiac-dsn-reader checks FAILED (10)"),
# and by the contracts job itself. Making it self-contained means stubbing
# common.sh's entire root resolution, which is larger than this ticket should
# carry, so the underlying gap (no target or workflow runs that suite) stays
# open and is filed rather than papered over.
shard-plan:
	bash scripts/trial/test-shard-plan.sh
	bash scripts/trial/test-seed-corpus-cases.sh

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

verify: fmt-check vet test test-race-split crosscompile contract-test codegraph-contract shard-plan canonical-receipts fullstack-contract build

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
