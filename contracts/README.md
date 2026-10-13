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

A shared `$defs` entry that a request schema reaches is frozen (request-echo): the service validates requests against the closed Go set, so an open schema would admit requests it rejects. `TestNoAdditiveVocabularyIsReachableFromARequest` enforces it.

On the Go side, `Validate` stays strict for producers and write paths; `ValidateRead` (and the read-path bounds of the stored-result tree) accepts well-formed unknown tokens for additive vocabularies and is what clients use on a hosted response. Frozen enums are strict in both.

An array of an additive vocabulary is capped by a fixed protocol bound (`ContextFabricFactKindListMaxItems`, 64), never by the current vocabulary size, so a grown vocabulary does not break a cached schema.

An enum used as a request input, or inside an `if`/`then`/`not` condition, is not an output vocabulary and stays as written.

## Adding an output field

A client that cached `tools/list` validates every later response against the schema it cached, so an MCP output object that sets `additionalProperties: false` rejects any field the server adds. Rule: strict producer, tolerant consumer.

- Every MCP output object (`contracts/jsonschema/v1/mcp_*_response.v1.schema.json` and its copy in `internal/mcp/schemas`) is open: it does not set `additionalProperties: false`. `TestEveryOutputObjectIsOpenUnlessListedClosed` fails for a closed output object that is not in `closedOutputObjects` with a reason it can never grow. The list is empty; only a fixed protocol envelope qualifies. Input (request) schemas stay closed.
- Add a field as optional, never in `required`, and never rename or remove one without a deprecation path. Update in the same PR: both schema copies, the Go DTO, the golden example in `examples/v1`, and the required-field baseline check (`make contract-test`). The ask-dev pin bump follows separately.
- The producer stays strict: `contractcheck` examples and the served-payload tests use `ValidateSerializedStrict`, which fails on any key no schema branch declares. So a new served field must be declared in the schema first.
- The consumer is tolerant: Go response decoders ignore unknown keys (`tolerantUnmarshal`); request decoders stay strict (`strictUnmarshal`).
- Canonical schemas that an MCP response embeds (`context_packet`, `evidence_ref`, `context_fabric_*`) keep their own closure for the HTTP API; the embedded MCP copy is the same document with `additionalProperties: false` removed, and the `$defs` sync check compares on that basis.

## Renaming a key

Never rename a served key in place. Add the new key, serve both with equal values, mark the old one deprecated in the schema description ("use <new key>; removed only after the ask-dev pin and the cfa client read the new keys"), move internal consumers to the new key (falling back to the old one for results stored before the rename), and remove the old key only after the ask-dev pin and the cfa client have moved. Example: ranking-table rows serve `subject_canonical_id` / `subject_label` and the deprecated `team_canonical_id` / `team_label`.

## Compatibility

Additive optional fields may ship in a compatible v1 release. Removing fields, changing requiredness, narrowing accepted values, or changing semantics requires a new schema version.
