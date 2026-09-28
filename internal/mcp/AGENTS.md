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
| Repository/scope | `context_scope.go`, `roots.go`, `hosted_scope.go` | Local: explicit input → MCP roots → cwd discovery. Hosted (`ProcessConfig.Hosted()`): explicit input only |
| Guide resources | `guide_resources.go`, `guide/` | Static `acr://guide/*` resources; text generated from registries by `guide/gen`; parity-tested in `guide/guidegen` |
| Investigate prompts | `investigate_prompts.go`, `guide/prompts.go` | Static prompts `investigate`, `continue_investigation`, `expand_evidence`; render from the `prompt_vocab.txt` snapshot in `guide/zz_generated.go`; argument completion from the same vocabulary |
| Data tools | `data_catalog.go`, `find_subjects.go`, `run_operation.go`, `data_tools.go` | Model-free reads of the hosted direct data routes; structured content is the API JSON verbatim, text is a bounded (4 KiB) untrusted-marked summary from `sidecar/render_data.go` |
| Safe errors | `toolerror.go`, `result.go` | Typed categories; no raw transport/body/path text |
| Embedded contracts | `schemas.go`, `schemas/` | Installed-binary schemas; parity-tested against canonical files |

## PROTOCOL INVARIANTS

- `NewBootstrap` must finish before `NewServer` accepts tool calls.
- `context_for_task` and `source_evidence` are always registered; both remain read-only, idempotent, non-destructive, and open-world.
- `investigate_question` and `investigation_result` register ONLY when the hosted capabilities response advertises them. Context Fabric is an optional hosted capability, so registering unconditionally would offer an agent tools that every call fails, and requiring them at the compatibility gate would refuse to start against a healthy hosted API with no graph backend.
- Never register `record_episode` in this package.
- `data_catalog`, `find_subjects` and `run_operation` register ONLY when the hosted capabilities response advertises the tool name (`run_operation` needs the `data:read` scope, so a credential without it never sees it). They are model-free on our side: no interpreter, synthesizer or embedder, pinned by `data_tools_static_test.go` (import and symbol scan, sidecar import closure) and by the end-to-end test that arms the API's model seam to panic. They add nothing to and drop nothing from the API JSON, and serve no person data (the T7 scan reads the operations catalogue, the response types and the six schemas).
- A `run_operation` refusal (`call: refused`), `operation_unavailable` and upstream errors are ANSWERS: `IsError` stays false and the model reads `call`, `completeness` and `result` itself. Only transport, auth, scope (`insufficient_scope` is category `entitlement`), rate-limit and validation failures are tool errors.
- Guide resources are read-only, embedded, and identical for every caller. Regenerate with `go generate ./internal/mcp/guide`; never hand-edit `guide/zz_generated.go`. The `guide` package must not import engine packages; only `guide/guidegen` and its tests do.
- Prompts are static and caller-independent: they read only their arguments and the embedded vocabulary, call no model, and add no tool. `investigate` and `continue_investigation` register only with `investigate_question`. Refusals are fixed-text invalid-params errors that never echo the argument.
- This package must never narrow an investigation result itself. `investigate_question` projects through `internal/contextfabric/answerprojection` and nothing else: that single choke point is what makes API/MCP answer parity structural rather than a convention. A second summariser here silently reopens consumer drift.
- The sidecar owns consumer surface identity and the time axis on an investigation request. Neither is caller-settable: surface identity is how the differential parity check tells the surfaces apart.
- Scope precedence is explicit request values, then compatible MCP file roots, then cwd Git discovery. When `ProcessConfig.Hosted()` is true, `context_for_task` resolves from the request alone: no roots, cwd, Git or local index, and a missing `repository.slug` is a typed validation refusal.
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
