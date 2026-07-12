# CONTEXT PACKET ASSEMBLY

## OVERVIEW

Deterministic pipeline from authorized request scope to ranked, budgeted, validated packet. `(*Assembler).Assemble` is the orchestration hub; storage, ranking, budgets, status, observations, and snapshots remain separate seams.

## WHERE TO LOOK

| Task | Location | Notes |
| --- | --- | --- |
| Pipeline ordering | `assembler.go` | Validate → scope → evidence → ranking → budget → packet validation → snapshot |
| Ranking/dedup | `ranking.go`, `rules.go` | Stable ordering, category allocation, rule metadata |
| Item/token/byte limits | `budget.go` | Drop lowest-ranked tail deterministically; recompute ranks and sizes |
| Scope resolution | `scope_resolution.go`, `read_adapter.go` | Versioned plans and explicit fallback reasons |
| ClickHouse reads | `clickhouse*.go`, `source_*.go` | Parameterized, versioned query adapters only |
| Evidence expansion | `evidence_resolver.go`, `evidence_adapters.go` | Expand persisted evidence; never dereference source URIs |
| Packet states | `packet_state.go`, `packet_output_validation.go` | Complete/partial/degraded/empty and final bounds |
| Observability | `observation.go`, `trace.go` | Optional panic-isolated boundaries |

## PIPELINE INVARIANTS

- Validate the request before storage access.
- Resolve scope before retrieval; evidence scope must exactly match resolved scope.
- Validate evidence before ranking; unauthorized/non-displayable evidence becomes coverage information, not an item.
- Rank before applying budgets. Tie-breaking must be deterministic; no map-order or random selection.
- Record `QueryVersionV1` and `RankingVersionV1` on every packet. Algorithm/query changes require version review.
- Finalize after each truncation so `rank`, token estimates, serialized bytes, required checks, and next steps agree.
- Validate the final packet before saving a snapshot.

## FAILURE SEMANTICS

- Caller cancellation returns `context.Canceled`; do not manufacture a packet.
- Retrieval deadline/unavailability and scope mismatch produce a bounded degraded packet with explicit reason codes.
- Snapshot storage is optional. Degraded snapshot writes use a short bounded context; cancellation must not turn successful assembly into a storage hang.
- Observer/tracer failures are isolated from packet results.

## TESTING

- Shared builders and evaluation-corpus setup live beside `assembler_test.go`.
- `ranking_test.go`, `policy_test.go`, and budget tests lock deterministic ordering and truncation.
- `adversarial_test.go` and resolver adversarial tests cover malformed, hidden, and unauthorized evidence.
- `latency_test.go`, `snapshot_test.go`, and `trace_test.go` cover deadlines and optional boundaries.
- Run `go test ./internal/contextpacket`; use `make contract-test` when packet shape or bounds change.

## ANTI-PATTERNS

- Do not fetch evidence URLs or execute retrieved text.
- Do not embed raw ClickHouse SQL in handlers; add/update a versioned adapter.
- Do not reorder scope, ranking, budget, finalization, and validation stages casually.
- Do not mutate retrieved evidence in place when filtering or ranking.
- Do not add ranking criteria without deterministic tie behavior and version review.
