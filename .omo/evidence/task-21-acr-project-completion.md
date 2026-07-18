# Task 21 evidence: private ACR developer and operator documentation

**Plan:** `acr-project-completion`, Todo 21 (`CHAOS-2912`)

## Deliverables

- `docs/operations.md` is the cohesive private developer/operator guide. It
  covers ownership, External Push separation, local TLS Compose, private Helm
  and Kustomize lifecycle, production configuration, Secrets/JWKS rotation,
  migrations, credential lifecycle, application-only rollback, restore
  ownership, observability, troubleshooting, and host-local MCP setup.
- `README.md` links the operations guide and its offline documentation gate.
- `scripts/docs/verify.sh` remains offline and now checks the new operations
  surface for real command paths, Make targets, environment names, Helm schema
  and JWKS terminology, local links, and unsafe claims.
- `scripts/docs/clean-room.sh` reuses the Compose, Kind/Helm, Kind/Kustomize,
  and real MCP STDIO helpers. Docker/Kind absence or a missing requested Kind
  cluster self-skips without claiming execution.
- `testdata/docs-invalid/` contains one focused fixture each for an
  Ops-packaged-ACR claim, production HTTP, a plaintext ACR token shape, a
  schema-rollback claim, and an absolute filesystem path.

## Offline verifier

```text
$ bash scripts/docs/verify.sh
ok: no forbidden publication claim found
ok: no Ops-packaged ACR claim found
ok: no production HTTP endpoint found
ok: no plaintext ACR credential found
ok: no supported schema rollback claim found
ok: no absolute filesystem path found
ok: all local Markdown links resolve
ok: all referenced make targets exist in Makefile
ok: operations command paths exist
ok: operations environment names are read by code or deployment artifacts
ok: operations schema and JWKS terminology is consistent

docs verification OK
EXIT=0
```

Each invalid fixture was run with `bash scripts/docs/verify.sh --root
testdata/docs-invalid/<fixture>` and exited `1`, naming exactly its expected
claim class:

| Fixture | Observed failure |
| --- | --- |
| `ops-packaged-acr` | `Ops-packaged ACR claim present in: README.md` |
| `production-http` | `production HTTP endpoint present in: README.md` |
| `plaintext-secret` | `plaintext ACR credential present in: README.md` |
| `schema-rollback` | `supported schema rollback claim present in: README.md` |
| `absolute-path` | `absolute filesystem path present in: README.md` |

## Clean-room driver

```text
$ bash scripts/docs/clean-room.sh --mode mcp
... "status":"incomplete_configuration" ...
... "transport":"stdio" ... "status":"read-only" ...
ok   github.com/full-chaos/dev-health-acr/internal/mcp
EXIT=0
```

The offline doctor intentionally reported incomplete local configuration while
the metadata and real TLS-backed command-transport test proved the STDIO path.

```text
$ bash scripts/docs/clean-room.sh --mode compose --compose ../compose.yml --overlay deploy/compose/acr.compose.yml
error: compose file is not a file: ../compose.yml
EXIT=2
```

Docker was available, but this isolated ACR worktree does not contain the
caller-owned root Compose file. No Compose resource was created. The command is
ready to execute when a caller supplies that required root input.

```text
$ bash scripts/docs/clean-room.sh --mode helm --cluster acr-docs-missing
No kind clusters found.
SKIP: Kind cluster is unavailable: acr-docs-missing
EXIT=0

$ bash scripts/docs/clean-room.sh --mode kustomize --cluster acr-docs-missing
No kind clusters found.
SKIP: Kind cluster is unavailable: acr-docs-missing
EXIT=0
```

No Kind fixture was available, so neither cluster lifecycle was claimed as run.

## Quality gates

```text
$ shellcheck scripts/docs/verify.sh scripts/docs/clean-room.sh
EXIT=0

$ make fmt-check vet
go vet ./...
EXIT=0

$ make verify
go vet ./...
go test ./...
go run ./cmd/contractcheck
OK   16 JSON Schemas compile with the supported Draft 2020-12 profile
OK   contracts/openapi/acr-v1.json + generated acr-v1.yaml
OK   contracts/mcp/tools.v1.json
go build -o .tmp/acr-api ./cmd/acr-api
go build -o .tmp/acr-mcp ./cmd/acr-mcp
go build -o .tmp/contractcheck ./cmd/contractcheck
go build -o .tmp/acr-migrate ./cmd/acr-migrate
EXIT=0
```

`bash -n` passed for both changed shell scripts. LSP diagnostics reported no
diagnostics for either shell script; no Markdown LSP is configured.

## Review

An independent read-only review found and the change addressed three
portability issues before final verification: portable environment extraction,
space-safe fixture paths, and MCP execution from the repository root.
