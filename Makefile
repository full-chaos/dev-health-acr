.PHONY: fmt fmt-check test vet contract-write contract-test build verify

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

verify: fmt-check vet test contract-test build
