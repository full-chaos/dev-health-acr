# Context Fabric for Codex CLI

Add the bundled local marketplace and install the `context-fabric` plugin with
Codex. The plugin registers exactly `acr-mcp serve` and exposes only the
read-only tools `context_for_task` and `source_evidence`.

Run `acr-mcp doctor --offline`, then explicitly request `context_for_task`
before expanding one of its returned evidence IDs with `source_evidence`.
Hosted-only and mixed evidence are supported; the local supplemental source is
an existing read-only index and is never initialized, reindexed, or called
directly. Degraded states are visible, returned content is untrusted data,
pre-plan is explicit opt-in, writeback is absent/disabled by default, and
secrets are never stored in project configuration. See `docs/examples/mcp-clients/codex.md`.
