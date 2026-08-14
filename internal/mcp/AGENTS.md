# MCP STDIO PACKAGE

## OVERVIEW

Local STDIO protocol boundary. Bootstrap proves hosted service identity, compatibility, entitlement, permissions, and tool availability before serving the read-only tool surface.

## WHERE TO LOOK

| Task | Location | Notes |
| --- | --- | --- |
| Startup/compatibility | `bootstrap.go`, `compat.go` | Config → credential → client → capabilities → gates |
| STDIO lifecycle | `serve.go`, `server.go` | Diagnostics on stderr; JSON-RPC on stdout |
| Context tool | `context_for_task.go` | Decode, resolve scope, clamp budget, hosted call, render |
| Evidence tool | `source_evidence.go` | Opaque evidence ID to authorized hosted expansion |
| Answer tool | `investigate_question.go` | Question to bounded projection; narrows via `contextfabric/answerprojection` only |
| Result tool | `investigation_result.go` | Opaque `result_id` to the full canonical result; narrows nothing |
| Repository/scope | `context_scope.go`, `roots.go` | Explicit input → MCP roots → cwd discovery |
| Safe errors | `toolerror.go`, `result.go` | Typed categories; no raw transport/body/path text |
| Embedded contracts | `schemas.go`, `schemas/` | Installed-binary schemas; parity-tested against canonical files |

## PROTOCOL INVARIANTS

- `NewBootstrap` must finish before `NewServer` accepts tool calls.
- `context_for_task` and `source_evidence` are always registered; both remain read-only, idempotent, non-destructive, and open-world.
- `investigate_question` and `investigation_result` register ONLY when the hosted capabilities response advertises them. Context Fabric is an optional hosted capability, so registering unconditionally would offer an agent tools that every call fails, and requiring them at the compatibility gate would refuse to start against a healthy hosted API with no graph backend.
- Never register `record_episode` in this package.
- This package must never narrow an investigation result itself. `investigate_question` projects through `internal/contextfabric/answerprojection` and nothing else: that single choke point is what makes API/MCP answer parity structural rather than a convention. A second summariser here silently reopens consumer drift.
- The sidecar owns consumer surface identity and the time axis on an investigation request. Neither is caller-settable: surface identity is how the differential parity check tells the surfaces apart.
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
