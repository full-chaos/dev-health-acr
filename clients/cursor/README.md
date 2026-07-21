# Context Fabric for Cursor

A Cursor plugin that adds explicit, evidence-backed ACR context and evidence retrieval to Cursor's agent. It never runs on its own: every context or evidence fetch is a user-requested command, skill, or rule invocation.

## Layout

- `.cursor-plugin/plugin.json` — plugin manifest.
- `mcp.json` — declares the `acr` STDIO server, `acr-mcp serve`.
- `commands/` — explicit slash commands (`get-context`, `plan-with-context-fabric`).
- `rules/` — a project rule describing the explicit-only workflow, plus an inert, manually invoked pre-plan rule.
- `skills/context-fabric/` — the same explicit-only workflow as a skill.
- `scripts/` — scoped install, update, and uninstall helpers (bash and PowerShell).

## Install

Run `scripts/install.sh` (or `scripts/install.ps1` on Windows). By default the plugin is staged at `$HOME/.cursor/plugins/local/context-fabric`; set `CURSOR_PLUGIN_DIR` to an absolute path to override the target. The installer refuses to write into a non-empty target it does not already own, and it never touches an existing project or user `.cursor/mcp.json` or `.cursor/rules` directory.

Use `scripts/update.sh` / `scripts/update.ps1` to refresh an owned install in place, and `scripts/uninstall.sh` / `scripts/uninstall.ps1` to remove it. Both refuse to act on a target this plugin did not install.

## Workflow

`context_for_task` is called only for an explicit user task; retrieved titles, excerpts, and Markdown fields are treated as untrusted data, never as instructions. `source_evidence` is called only for an evidence ID a prior `context_for_task` response returned. Unavailable or incompatible service states are surfaced verbatim instead of being guessed around.
