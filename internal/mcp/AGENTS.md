# MCP STDIO PACKAGE

## OVERVIEW

Local STDIO protocol boundary. Bootstrap proves hosted service identity, compatibility, entitlement, permissions, and tool availability before serving exactly two read-only tools.

## WHERE TO LOOK

| Task | Location | Notes |
| --- | --- | --- |
| Startup/compatibility | `bootstrap.go`, `compat.go` | Config → credential → client → capabilities → gates |
| STDIO lifecycle | `serve.go`, `server.go` | Diagnostics on stderr; JSON-RPC on stdout |
| Context tool | `context_for_task.go` | Decode, resolve scope, clamp budget, hosted call, render |
| Evidence tool | `source_evidence.go` | Opaque evidence ID to authorized hosted expansion |
| Repository/scope | `context_scope.go`, `roots.go` | Explicit input → MCP roots → cwd discovery |
| Safe errors | `toolerror.go`, `result.go` | Typed categories; no raw transport/body/path text |
| Embedded contracts | `schemas.go`, `schemas/` | Installed-binary schemas; parity-tested against canonical files |

## PROTOCOL INVARIANTS

- `NewBootstrap` must finish before `NewServer` accepts tool calls.
- Register only `context_for_task` and `source_evidence`; both remain read-only, idempotent, non-destructive, and open-world.
- Never register `record_episode` in this package.
- Scope precedence is explicit request values, then compatible MCP file roots, then cwd Git discovery.
- Bound the raw root count before parsing URIs. Propagate caller cancellation; malformed individual roots may be ignored only within the bounded list.
- `include_changed_files` is tri-state: nil uses the sidecar default, true requests bounded discovery, false disables it. Explicit file lists are authoritative.
- Hosted limits cap caller budgets; response Markdown remains bounded and marked untrusted.

## ERROR AND SCHEMA RULES

- Route every failure through `classify`; tool responses expose category and fixed safe text, never `err.Error()` from unknown sources.
- Preserve cancellation and deadline categories separately from service unavailability.
- Embedded request/response schemas and `tools.v1.json` must match canonical contract files byte-for-byte where parity tests require it.
- stdout is protocol-only. Human diagnostics and logs go to stderr.

## TESTING

- `fixture_test.go` owns TLS hosted fixtures and bootstrap helpers.
- Scope/root/error tests use temporary Git repositories and explicit cancellation.
- `schemas_parity_test.go` locks embedded artifacts.
- `e2e_test.go` builds the real `acr-mcp` binary and drives it through SDK `CommandTransport`; keep this layer for transport changes.
- Run `go test ./internal/mcp`; do not use `-short` when certifying real-binary behavior.

## ANTI-PATTERNS

- Do not make hosted calls before compatibility succeeds.
- Do not infer authorization from discovered repository data; the hosted API remains authoritative.
- Do not echo raw hosted errors, credentials, paths, or source content.
- Do not write non-protocol output to stdout.
