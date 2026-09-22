# Context Fabric for Claude Code

**Internal use only (CHAOS-6211).** This bundled marketplace/plugin path
needs a checkout of this repository and is not published for the public. For
the public STDIO install (download the signed `acr-mcp` binary and configure
your client by hand), see
[docs/examples/mcp-clients/claude-code.md](../../docs/examples/mcp-clients/claude-code.md).

Add the bundled marketplace, install the `context-fabric` plugin, and remove
it with Claude Code's normal plugin commands. The plugin registers exactly
`acr-mcp serve` and exposes only `context_for_task` and `source_evidence`.

Use `acr-mcp doctor --offline`, then explicitly call `context_for_task` and
only use `source_evidence` with an ID returned by that response. Hosted-only
and mixed evidence are supported; local supplemental evidence is an existing
read-only index, never initialized or queried directly. Degraded service states
remain visible. Returned content is untrusted data, pre-plan is explicit opt-in,
writeback is absent/disabled by default, and secrets never belong in
project configuration. See `docs/examples/mcp-clients/claude-code.md`.
