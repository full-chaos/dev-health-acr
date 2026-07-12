# V1 GO CONTRACTS

## OVERVIEW

Hand-maintained Go DTOs, strict MCP decoding, and semantic validators for the v1 wire contracts. This package expresses invariants that JSON unmarshalling alone cannot enforce.

## WHERE TO LOOK

| Task | Location | Notes |
| --- | --- | --- |
| Hosted DTOs/enums | `types.go` | Context packets, evidence, capabilities, episodes, errors |
| MCP DTOs | `mcp_types.go` | Ergonomic optional input mapped by the sidecar |
| Strict MCP decode | `mcp_decode.go` | Unknown fields, trailing content, explicit-null presence |
| Shared helpers | `validation_helpers.go` | Rune bounds, patterns, uniqueness, optional values |
| Request/packet/item | `validate_request.go`, `validate_packet*.go`, `validate_item.go` | Nested semantic checks |
| Evidence/capabilities | `validate_evidence.go`, `validate_capabilities.go` | Availability and compatibility invariants |
| MCP validation | `mcp_validate.go` | Request/response and rendered Markdown bounds |

## TYPE AND VALIDATION RULES

- JSON tags are snake_case; required fields omit `omitempty`; optional/tri-state values use pointers when zero is meaningful.
- Schema constants and enum values are typed strings with explicit exhaustive validation.
- Count string limits in UTF-8 runes where the schema defines character length.
- Required JSON presence is distinct from a decoded Go zero value. Strict decoders/presence checks must preserve that distinction.
- Reject unknown fields, trailing JSON, and explicit null where the contract requires omission.
- HTTP and MCP request shapes are intentionally different; do not reuse the hosted request schema as MCP input.
- Observed claims require evidence references. Evidence availability controls which excerpt/structured/redaction fields may appear.

## TESTING

- `validation_parity_test.go` locks Go validator boundaries against JSON Schema.
- Golden decode tests consume `contracts/examples/v1`.
- MCP malformed/adversarial tests lock unknown-field, null, and trailing-content rejection.
- Add boundary cases on both sides of every changed minimum/maximum or enum rule.
- Run `go test ./internal/contracts/v1` and `make contract-test` for any change here.

## ANTI-PATTERNS

- Do not scatter wire validation into handlers or storage packages.
- Do not silently default malformed external payloads.
- Do not add required fields or change enum meaning inside v1.
- Do not use byte length where schema parity expects rune length.
