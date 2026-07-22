.PHONY: fmt fmt-check test hosted-integration vet contract-write contract-test codegraph-contract canonical-receipts build verify release-local release-verify container-contract container-pins container-test container-reproducible container-oci container-scan

RELEASE_OUTPUT ?= .tmp/release
RELEASE_VERSION ?=

fmt:
	gofmt -w $$(find . -name '*.go' -not -path './vendor/*')

fmt-check:
	@files="$$(gofmt -l $$(find . -name '*.go' -not -path './vendor/*'))"; \
	if [ -n "$$files" ]; then echo "Go files need formatting:"; echo "$$files"; exit 1; fi

test:
	go test ./...

hosted-integration:
	ACR_HOSTED_INTEGRATION=1 go test ./cmd/acr-api -run '^TestHostedRuntime_real_binary_serves_and_fails_readiness_safely$$' -count=1 -v

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

build:
	go build -o .tmp/acr-api ./cmd/acr-api
	go build -o .tmp/acr-mcp ./cmd/acr-mcp
	go build -o .tmp/contractcheck ./cmd/contractcheck
	go build -o .tmp/acr-migrate ./cmd/acr-migrate

verify: fmt-check vet test contract-test codegraph-contract canonical-receipts build

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
