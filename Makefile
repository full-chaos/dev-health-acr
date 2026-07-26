.PHONY: fmt fmt-check test hosted-integration clients-real vet contract-write contract-test codegraph-contract canonical-receipts build verify release-local release-verify container-contract container-pins container-test container-reproducible container-oci container-scan fullstack-opencode-e2e fullstack-contract

RELEASE_OUTPUT ?= .tmp/release
RELEASE_VERSION ?=

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
	go test ./...

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
	go build -o .tmp/acr-api ./cmd/acr-api
	go build -o .tmp/acr-mcp ./cmd/acr-mcp
	go build -o .tmp/contractcheck ./cmd/contractcheck
	go build -o .tmp/acr-migrate ./cmd/acr-migrate

verify: fmt-check vet test contract-test codegraph-contract canonical-receipts fullstack-contract build

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
