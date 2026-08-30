[COMPRESSED] agent_type: reviewer

Warning

- P3 — Missing production-sink regression oracle for gate telemetry. The sink emits `shape` at [telemetry.go:244](/Users/chris/projects/full-chaos/dev-health/worktrees/acr/lane-4579/internal/contextfabric/telemetry.go:244), but new tests inspect only the in-memory double at [engine_test.go:491](/Users/chris/projects/full-chaos/dev-health/worktrees/acr/lane-4579/internal/contextfabric/engine_test.go:491). Removing `shape` from the slog arguments would leave tests green while production logs could no longer distinguish `applied` for `discovered_cohort`; existing sink-level precedent is [chaos4085_telemetry_sink_test.go:14](/Users/chris/projects/full-chaos/dev-health/worktrees/acr/lane-4579/internal/contextfabric/chaos4085_telemetry_sink_test.go:14).

No findings:

- Correctness: exact `ShapeDiscoveredCohort` gate; subject-bearing shapes remain unchanged. Both production call sites, prior consultation, terminal/window-adjacent paths, turn-2 redemption, and replay use the correct ordering.
- Contract/receipt safety: Missing rows and corresponding options are removed together; post-gate v1/v2 dispatch is correct; no invalid receipts; contract tests pass.
- Silent failure/runtime telemetry: gate-1 remains pre-interpretation and does not use synthesized `ShapeOpen`; applied/no-op/empty outcomes remain distinguishable.
- Test discrimination: single-subject control, terminal v1 dispatch, replay, and round-1 mutation evidence are sufficient.

Verification: relevant Go packages and `make contract-test` pass.

Verdict: NOT CLEAN

