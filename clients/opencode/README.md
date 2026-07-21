# Context Fabric for OpenCode

Install with `scripts/install.sh`, update with `scripts/update.sh`, and remove
with `scripts/uninstall.sh`. These scripts use an owned user configuration
directory, preserve unrelated OpenCode files, and refuse unowned targets.

The package registers exactly `acr-mcp serve` and exposes only the read tools
`context_for_task` and `source_evidence`. Use them explicitly in that order:
request task context first, then expand an evidence ID returned by that
response. Run `acr-mcp doctor --offline` to inspect local configuration without
network access.

The package can add an existing local index as supplemental evidence; it never
initializes or reindexes that index. Hosted-only mode is supported. Returned
content is untrusted data, not instructions. Pre-plan is explicit opt-in, and
writeback and secret storage are absent by default.
