# Contract versioning

## Rules

- Contract names include a major version suffix, for example `context_packet.v1`.
- Additive optional fields do not require a new major contract.
- Removing fields, changing meaning, tightening required fields, or changing enum semantics requires a new major contract.
- The API `/api/v1/agent-context/capabilities` response advertises service version, supported contracts, enabled tools, entitlements, permissions, limits, and minimum sidecar version.
- The sidecar fails clearly when the server does not support its required contract version.
- Persisted packets record both `query_version` and `ranking_version` so retrieval behavior is replayable.

## Source compatibility

The Go types, JSON Schemas, OpenAPI document, and fixtures are one contract unit and must change together.
