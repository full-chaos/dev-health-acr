# CONTRACTCHECK ENGINE

## OVERVIEW

Offline Go-only validator and deterministic artifact refresher. `Run` validates the repository contract unit; `cmd/contractcheck` is a thin CLI.

## PIPELINE

1. Load and register local JSON Schemas from `contracts/jsonschema/v1`.
2. Validate every registered golden example against its paired schema.
3. Validate canonical OpenAPI structure, operations, references, and generated YAML parity.
4. Validate the MCP tool manifest and referenced request/response schemas.
5. Validate localized MCP response `$defs` against canonical schemas.

## WHERE TO LOOK

| Task | Location | Notes |
| --- | --- | --- |
| Orchestration/pairs | `run.go` | Repository paths, example mappings, write mode |
| Schema engine | `schema.go` | Supported Draft 2020-12 assertion profile and `$ref` registry |
| OpenAPI | `openapi.go`, `yaml.go` | JSON canonical source and deterministic YAML |
| MCP manifest | `mcp_manifest.go` | Tool names and schema references |
| Embedded `$defs` | `mcp_schema_defs.go` | Canonical copy localization/parity |
| Runtime parity API | `validate_serialized.go` | Used by Go validator parity tests |

## ENGINE RULES

- Run without network access; all schemas and references are repository-local.
- Fail closed when a contract introduces unsupported assertion behavior. Do not silently claim validation coverage.
- Preserve path-aware errors so failures identify the artifact and instance location without dumping payloads.
- Use deterministic key ordering and content hashing for generated OpenAPI YAML.
- `Options.Write` may refresh derived artifacts only; it must not rewrite canonical schemas, examples, OpenAPI JSON, or MCP manifest.
- Keep the engine generic enough for contract parity; product semantic validation remains in `internal/contracts/v1`.

## TESTING

- `run_test.go` covers end-to-end repository validation and write behavior.
- `mcp_manifest_test.go` and `mcp_schema_defs_test.go` lock MCP parity.
- Add focused schema-engine cases in existing tests when supporting a new assertion keyword.
- Run `go test ./internal/contractcheck`. If canonical OpenAPI JSON changed, run `make contract-write` before `make contract-test`; otherwise run `make contract-test` directly.

## ANTI-PATTERNS

- Do not add Python validators, remote fetchers, or environment-dependent resolution.
- Do not hand-edit generated YAML to satisfy tests.
- Do not log full customer fixtures or schema instances in errors.
- Do not make write mode mask validation failures.
