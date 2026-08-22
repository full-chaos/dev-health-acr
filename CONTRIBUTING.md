# Contributing

This is a private SVS repository. Changes should be tied to Linear issues in the Dev Health Agent Context Runtime project.

## Pull request checklist

- Contract-first files updated together when the API shape changes.
- `make contract-test` and `make verify` pass.
- OpenAPI YAML is regenerated from canonical JSON with `make contract-write` when the service contract changes.
- No credentials or private evidence in fixtures.
- Read and write authorization paths remain separate.
- New queries include org/repo scoping and bounded limits.
- Public behavior includes a compatibility/versioning note.
- Any new decision branch (commit gate, veto/refusal reason, classification outcome, candidate elimination, error classification) reachable by this change emits corpus-safe, closed-vocabulary decision-basis telemetry sufficient to diagnose a defect at that branch from the run's own artifacts alone -- no source reading, no re-run. A branch without it is a blocking finding regardless of severity elsewhere.
