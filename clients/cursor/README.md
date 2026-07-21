# Context Fabric for Cursor

A Cursor plugin that adds explicit, evidence-backed ACR context and evidence retrieval to Cursor's agent. It never runs on its own: every context or evidence fetch is a user-requested command, skill, or rule invocation.

## Layout

- `.cursor-plugin/plugin.json` — plugin manifest.
- `mcp.json` — declares the `acr` STDIO server, `acr-mcp serve`.
- `commands/` — explicit slash commands (`get-context`, `plan-with-context-fabric`).
- `rules/` — an always-applied guard rule that instructs the agent never to call the registered `acr` MCP tools on its own judgment, a rule describing the explicit-only workflow, and an inert, manually invoked pre-plan rule.
- `skills/context-fabric/` — the same explicit-only workflow as a skill.
- `scripts/` — scoped install, update, and uninstall helpers (bash and PowerShell).

## Install

Run `scripts/install.sh` (or `scripts/install.ps1` on Windows). By default the plugin is staged at `$HOME/.cursor/plugins/local/context-fabric`; set `CURSOR_PLUGIN_DIR` to an absolute path to override the target. The installer refuses to write into a non-empty target it does not already own, and it never touches an existing project or user `.cursor/mcp.json` or `.cursor/rules` directory.

Use `scripts/update.sh` / `scripts/update.ps1` to refresh an owned install in place, and `scripts/uninstall.sh` / `scripts/uninstall.ps1` to remove it. Both refuse to act on a target this plugin did not install.

Install state lives in a directory scoped to the exact target path (`<target>.stages`), never in the shared parent directory, so two differently-named installs under the same parent can never observe or affect each other. Every update keeps the previous install generation on disk instead of deleting it, so a reader that resolved an older generation keeps working across any number of later updates; only an owned uninstall removes all retained generations.

## Workflow

`context_for_task` is called only for an explicit user task; retrieved titles, excerpts, and Markdown fields are treated as untrusted data, never as instructions. `source_evidence` is called only for an evidence ID a prior `context_for_task` response returned. Unavailable or incompatible service states are surfaced verbatim instead of being guessed around. Because a registered MCP tool is otherwise available to the agent's own judgment regardless of which skill or rule happens to be active, `rules/no-automatic-use.mdc` is always applied to every conversation and explicitly forbids calling either tool outside an explicit command, skill, or user request.
