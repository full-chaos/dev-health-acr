# SVS threat model, privacy, and retention

## Assets

- Engineering evidence and repository metadata.
- Organization and repository authorization boundaries.
- ACR client credentials.
- Context packet snapshots.
- Agent episode summaries and artifact references.

## Primary threats and controls

### Cross-tenant or cross-repository disclosure

- Organization and repository scope are derived from authenticated server context.
- Client-supplied org identifiers are ignored.
- Evidence IDs are opaque and checked against org/repo scope on every expansion.
- Database access uses row/org predicates and a read-only ClickHouse principal.

### Prompt injection through evidence

- Evidence text is labeled as untrusted data.
- The sidecar and API never execute retrieved instructions.
- The API does not follow arbitrary URLs from source data.
- The web inspector sanitizes markdown and external links.

### Credential theft

- ACR credentials are never command-line flags or query parameters.
- Environment variables are supported as a fallback; OS keychain/config integrations are preferred.
- Diagnostics redact secrets and bearer headers.
- Server stores only secure credential hashes.

### Excessive data collection

- Raw transcripts are not accepted in SVS.
- Transcript mode defaults to `none`.
- Optional references are opaque and never dereferenced by ACR.
- Evidence excerpts are capped at 1,000 characters and should be omitted unless necessary.

### Duplicate or replayed writes

- Episode writes require `Idempotency-Key` and `client_episode_id`.
- Repeated identical writes return the existing episode.
- Conflicting reuse returns `409`.

### Resource exhaustion

- Request goals, file lists, packet item counts, excerpts, and token budgets are bounded by schema.
- Per-org and per-credential rate limits apply.
- Query timeouts return partial/degraded packets where safe.

## Default retention

- Context packet snapshots: 30 days.
- Agent episode metadata: 90 days.
- Raw transcripts: never stored in SVS.
- Retention is configurable only within licensed policy bounds.
- Administrative purge and redaction produce an audit tombstone rather than silently mutating history.

## Audit events

- Credential created, rotated, revoked, and used.
- Context packet requested and denied.
- Evidence expanded and denied.
- Episode created, deduplicated, redacted, and purged.
- Entitlement denial and rate-limit denial.
