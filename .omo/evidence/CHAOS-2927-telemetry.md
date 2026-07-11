# CHAOS-2927 telemetry evidence

## Scope

Added the dependency-free `internal/observability` core,
`docs/observability.md`, and a neutral bounded observer contract in
`internal/contextpacket`. The context-packet package does not depend on telemetry,
and no external contract fields or routes changed.

## Manual deterministic driver

`TestHooksEmitCorrelatedSafeSnapshots` is the reproducible in-memory driver. It
seeds the request ID generator with `0x42`, calls request, store, ranking, evidence,
and episode hooks, and verifies all five snapshots share the generated request ID.
The captured output contains only typed support dimensions; it has no raw request
context, evidence, transcript, debug payload, or arbitrary attributes.

Run it with:

```bash
go test -race -shuffle=on -count=1 ./internal/observability
```

Result: PASS.

`TestHooksUseSemanticDefaults` verifies that absent dimensions use explicit
semantic values (`unknown` or `none`) rather than empty metric labels.

## Reviewer P0 closure

- Generated IDs now use the canonical `req_` plus 32 lowercase hexadecimal format.
- Snapshots include bounded endpoint operation, HTTP status class, packet lifecycle,
  packet/baseline schema versions, compatibility, source coverage, and store query
class dimensions.

## Production-semantic closure

Canonical versions are imported from context-packet (`context-query.v1` and
`ranker.v2`). Packet lifecycle covers complete, partial, degraded, and empty; safe
snapshots additionally carry bounded item/stale/unavailable counts, compatibility,
version mismatch, store backend/query timeout, episode outcome, and audit delivery.
`MemorySink` is bounded single-replica diagnostics and resets on restart; it is not
durable audit or billing truth. CHAOS-2907 HTTP export remains explicitly deferred.
- `MemorySink` is bounded and concurrency safe; `SlogSink` emits only allowlisted
  snapshot fields.
- `NewEvidenceExpansionObserver` adapts context-packet evidence expansion without
  retaining source-system text, evidence IDs, excerpts, or payloads.
- Trace boundaries exist for request, store, and ranking work and carry only bounded
  correlation/operation metadata.
- The real assembler emits bounded terminal store-query, ranking, evidence, and packet
  observations. Evidence expansion emits exactly one terminal success, failure,
  cancellation, timeout, or denial observation.
- Packet schema versions use a dedicated schema-version type; query and ranking
  versions retain their own canonical allowlists.

## Adversarial probes

| Probe | Test | Result |
| --- | --- | --- |
| Secret injection | `TestHooksScrubUnknownHighCardinalityDimensions`, `TestHooksReplaceUnsafeRequestID` | Untrusted values normalize to `unknown`; bearer-like request ID is replaced and never reaches a snapshot. |
| High cardinality | `TestHooksScrubUnknownHighCardinalityDimensions` | Arbitrary outcome/query/ranking strings are not emitted. |
| Partial state | `TestHooksKeepPartialStateWithoutNegativeSizes` | `partial` survives; negative bytes/tokens clamp to zero. |
| Cancellation | `TestHooksPreserveCorrelationWhenContextCanceled` | Terminal canceled observation retains correlation. |
| Concurrency | `TestHooksRecordConcurrently` under `-race` | 64 concurrent completions are preserved without race reports. |
| Misleading logs | Snapshot-only `Sink` signature | Sinks receive no `context.Context`; callers cannot accidentally extract request bodies, principals, or debug values through this boundary. |

## SLO contract

`docs/observability.md` defines bounded metric mapping, 30-day availability,
latency, freshness, fallback, and completeness objectives, plus explicit page and
ticket thresholds. `request_id` is a correlation field only, never a label.

## Repository gate status

The focused telemetry race/shuffle gate, LSP diagnostics, `git diff --check`, full
repository race/shuffle suite, contract tests, and `make verify` pass on the combined
tree. Production IDs and the telemetry contract require `req_` followed by 32
lowercase hexadecimal characters; uppercase or secret-shaped caller IDs are replaced.
