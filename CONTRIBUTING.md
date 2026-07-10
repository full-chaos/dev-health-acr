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
