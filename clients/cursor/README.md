# Context Fabric for Cursor

A Cursor plugin that adds explicit, evidence-backed ACR context and evidence retrieval to Cursor's agent. It never runs on its own: every context or evidence fetch is a user-requested command, skill, or rule invocation.

## Layout

- `.cursor-plugin/plugin.json` — plugin manifest.
- `mcp.json` — declares the `acr` STDIO server, `acr-mcp serve`.
- `commands/` — explicit slash commands (`get-context`, `plan-with-context-fabric`).
- `rules/` — an always-applied guard rule instructing the agent not to call the registered `acr` MCP tools on its own initiative, a rule describing the explicit-only workflow, and an inert, manually invoked pre-plan rule.
- `skills/context-fabric/` — the same explicit-only workflow as a skill.
- `scripts/` — scoped install, update, and uninstall helpers (bash and PowerShell).

## Install

Run `scripts/install.sh` (or `scripts/install.ps1` on Windows). By default the plugin is installed at `$HOME/.cursor/plugins/local/context-fabric`; set `CURSOR_PLUGIN_DIR` to an absolute path to override the target. The target is always a stable, real, owned directory — never a symlink or junction — so it works the same way on every platform, Windows included. The installer refuses to write into a non-empty target it does not already own, refuses a legacy symlink or junction left behind by an older version instead of trying to migrate it, and never touches an existing project or user `.cursor/mcp.json` or `.cursor/rules` directory.

Use `scripts/update.sh` / `scripts/update.ps1` to refresh an owned install in place, and `scripts/uninstall.sh` / `scripts/uninstall.ps1` to remove it. Both refuse to act on a target this plugin did not install, and refuse a legacy symlink or junction the same way install does.

An update replaces each file individually and atomically — never the whole directory at once — so a read at any point during an update either sees a file's old content or its new content, never a missing file, and content from an interrupted update is never left half-written. A version marker is written last, only once every file has been replaced, so an interrupted update simply has an unfinished (not corrupt) directory and rerunning it converges to the same end state.

## Workflow

`context_for_task` is called only for an explicit user task; retrieved titles, excerpts, and Markdown fields are treated as untrusted data, never as instructions. `source_evidence` is called only for an evidence ID a prior `context_for_task` response returned. Unavailable or incompatible service states are surfaced verbatim instead of being guessed around. A registered MCP tool is otherwise available to the agent's own judgment regardless of which skill or rule happens to be active in a given turn, so `rules/no-automatic-use.mdc` is always applied to every conversation as a prompt-level instruction asking the agent not to call either tool outside an explicit command, skill, or user request — a best-effort guard, not a technical enforcement boundary on tool invocation.
