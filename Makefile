.PHONY: fmt fmt-check test vet contract-write contract-test build verify release-local release-verify

RELEASE_OUTPUT ?= .tmp/release
RELEASE_VERSION ?=

fmt:
	gofmt -w $$(find . -name '*.go' -not -path './vendor/*')

fmt-check:
	@files="$$(gofmt -l $$(find . -name '*.go' -not -path './vendor/*'))"; \
	if [ -n "$$files" ]; then echo "Go files need formatting:"; echo "$$files"; exit 1; fi

test:
	go test ./...

vet:
	go vet ./...

contract-write:
	go run ./cmd/contractcheck -write

contract-test:
	go run ./cmd/contractcheck

build:
	go build -o .tmp/acr-api ./cmd/acr-api
	go build -o .tmp/acr-mcp ./cmd/acr-mcp
	go build -o .tmp/contractcheck ./cmd/contractcheck
	go build -o .tmp/acr-migrate ./cmd/acr-migrate

verify: fmt-check vet test contract-test build

release-local:
	@test -n "$(RELEASE_VERSION)" || (echo "RELEASE_VERSION must be a canonical version such as 1.2.3"; exit 1)
	go run ./cmd/releasebuild build --root . --out "$(RELEASE_OUTPUT)" --version "$(RELEASE_VERSION)" --commit "$$(git rev-parse HEAD)" --date "$$(TZ=UTC0 git show -s --format=%cd --date=format-local:%Y-%m-%dT%H:%M:%SZ HEAD)"

release-verify:
	go run ./cmd/releasebuild verify --dir "$(RELEASE_OUTPUT)"
