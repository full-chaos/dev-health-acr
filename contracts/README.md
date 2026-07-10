# ACR contracts

This directory is the cross-repository source of truth for the hosted Go API, the local Go MCP sidecar, and the Context Packet Explorer in `dev-health-web`.

```text
jsonschema/v1/  JSON Schema 2020-12 wire contracts
openapi/        Canonical OpenAPI 3.1 JSON and generated deterministic YAML artifact
mcp/            MCP tool names and input/output contract references
examples/v1/    Golden request/response examples
```

## Rules

1. Organization identity and authorization come from the authenticated credential, never request payload fields.
2. Repository authorization is rechecked for packet generation and every evidence expansion.
3. A product entitlement is not an API credential.
4. `commit_sha` is the strongest retrieval scope; branch-only and repository fallback are disclosed in `resolved_scope.resolution`.
5. Packet items distinguish `observed`, `inferred`, and `recommendation` claims. Observed claims require evidence.
6. Retrieved source text is untrusted data, never instructions.
7. `record_episode` is idempotent, opt-in, and separate from External Push.
8. A wire-contract change must update JSON Schema, Go DTOs, OpenAPI, MCP definitions, golden examples, tests, and compatibility metadata together.
9. `contracts/openapi/acr-v1.json` is canonical. Refresh its deterministic YAML mirror with `make contract-write`.
10. `make contract-test` is the required offline contract gate.

## Go-only verification

`cmd/contractcheck` validates the Draft 2020-12 assertion profile currently used by ACR, every golden example, OpenAPI references and generated-artifact parity, and the MCP tool manifest. It uses only the Go standard library, runs offline, and fails closed if a contract introduces an unsupported assertion keyword.

```bash
make contract-write  # refresh deterministic derived artifacts
make contract-test   # validate without writing
```

## Compatibility

Additive optional fields may ship in a compatible v1 release. Removing fields, changing requiredness, narrowing accepted values, or changing semantics requires a new schema version.
