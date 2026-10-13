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

## Output vocabularies: additive or frozen

A client that cached `tools/list` output schemas validates every later response against that old copy, so a value added to a closed output `enum` breaks the whole response for it. Every enum-like field in an MCP output schema is therefore one of two kinds, and `internal/mcp/output_vocabulary_*_test.go` fails for any enum that is not classified.

- **Additive** (the default): `"type": "string"` with no `enum`, a `description` that says `Open vocabulary; documented values: ...`, and the same list in `x-documented-values`. The server may add a value without a schema version bump or client update; clients must tolerate values they do not know. To add a value: add the Go constant, add the value to `x-documented-values` and the description in `contracts/jsonschema/v1` and `internal/mcp/schemas`, and add it to the Go set of the vocabulary in the registry. The registry test fails if a Go constant is missing from the documented list.
- **Frozen**: the `enum` stays. Only a protocol, state-machine, request-echo or scoring set that a client branches on and that cannot grow qualifies, and the registry entry names which. Adding a value is a new schema version. The registry test pins the enum to its Go constant set exactly.

An enum used as a request input, or inside an `if`/`then`/`not` condition, is not an output vocabulary and stays as written.

## Compatibility

Additive optional fields may ship in a compatible v1 release. Removing fields, changing requiredness, narrowing accepted values, or changing semantics requires a new schema version.
