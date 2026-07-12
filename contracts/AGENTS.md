# CONTRACT ARTIFACTS

## OVERVIEW

Cross-repository wire artifacts for hosted API, MCP sidecar, and web consumers. JSON Schema and canonical OpenAPI JSON are hand-maintained sources; OpenAPI YAML is deterministic generated output.

## WHERE TO LOOK

| Artifact | Location | Rule |
| --- | --- | --- |
| Wire schemas | `jsonschema/v1/*.schema.json` | Draft 2020-12 assertion profile |
| Golden payloads | `examples/v1/*.json` | Every file has an explicit schema pairing |
| Canonical OpenAPI | `openapi/acr-v1.json` | Hand-edit this document |
| OpenAPI mirror | `openapi/acr-v1.yaml` | Generated; never hand-edit |
| MCP tools | `mcp/tools.v1.json` | Tool names and input/output schema references |

## ARTIFACT RULES

- Contract and file names carry a major suffix such as `.v1`.
- Additive optional fields may remain v1. Removed fields, tighter requiredness, narrowed values, changed enum semantics, or changed meaning require a new major version.
- Golden examples are executable contract fixtures, not prose samples; update minimal and full variants deliberately.
- OpenAPI paths, operation IDs, schemas, and examples must describe the same wire behavior as JSON Schema and Go validation.
- MCP response schemas embed localized canonical `$defs`; contractcheck rejects drift.
- Go DTOs are hand-maintained in `internal/contracts/v1`; they are not generated from these files.

## WORKFLOW

1. Edit canonical schemas/examples/OpenAPI JSON/MCP manifest as required.
2. Update Go DTOs and semantic validation in the same change.
3. Run `make contract-write` to refresh the deterministic OpenAPI YAML mirror.
4. Run `make contract-test` to validate schemas, examples, OpenAPI refs/parity, MCP manifest, and embedded `$defs`.
5. Run `make verify` before delivery.

## ANTI-PATTERNS

- Do not hand-edit `openapi/acr-v1.yaml` or treat it as canonical.
- Do not add a fixture without registering its schema pairing in contractcheck.
- Do not loosen one representation while another remains stricter.
- Do not introduce remote schema references or validation that requires network access.
