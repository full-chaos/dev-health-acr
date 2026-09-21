# Hosted MCP auth and isolation matrix

The matrix proves, by execution, that a hosted `acr-mcp --transport=http` endpoint decides every request on the caller's own bearer, and that one caller can never read another caller's stored result, evidence or repository scope. It sits next to the [MCP sidecar](mcp-sidecar.md) document and the [threat model](threat-model.md).

One table of rows (`internal/mcp/authmatrix_test.go`, `matrixRows`) is executed by one runner (`runMatrix`) against a **target**: an MCP endpoint URL plus one bearer per caller. The runner speaks only MCP 2026-07-28 over HTTP, so the same table runs in two places.

| Where | Test | Target | Extra evidence per row |
| --- | --- | --- | --- |
| CI | `TestAuthMatrixInProcess` | `acr-mcp` HTTP handler in front of a real in-process acr-api (`api.NewApp`: real `Authenticator`, stored-result route and live-grant gate, evidence store, context-packet assembler) | the SDK handler never ran for a refused request, which stores were consulted, the acr-api calls it made, one request line and one stored-result decision line per row |
| CI | `TestAuthMatrixConcurrentCallersNeverBleedIdentity` | same | 50 parallel callers each for A, B and H, interleaved: every answer belongs to its own bearer, every bearer maps to one `principal_ref`, no two bearers share one, acr-api decided each stored-result read on the caller's own organization |
| CI | `TestAuthMatrixLiveRunnerAgainstAnInProcessEndpoint`, `TestAuthMatrixLiveThroughTheRealTestBinary` | the same in-process endpoint, configured only through the environment variables below, the second one through a child of the real test binary (correct deployment passes, nothing configured skips loudly, `REQUIRE_LIVE` and a partial configuration fail, a deployment that leaks the owner's data to another organization fails) | wire only (proves the live runner) |
| A deployment | `TestAuthMatrixLive` | the endpoint named by `ACR_MCP_MATRIX_URL` | wire only |

## Callers

| Caller | Credential | What it isolates |
| --- | --- | --- |
| A | org 1, `context:read` + `evidence:read`, grant `[R]` | owner of every fixture |
| B | org 2, same scopes, grant `[R]` (the same repository slug) | only the organization scope of a store can refuse it |
| C | org 1, same scopes, grant `[other]` | only the repository grant can refuse it |
| D | A's grant, revoked | revocation |
| E | A's grant, expired | expiry |
| F | no `Authorization` header | bearer required |
| G | a malformed bearer | bearer shape |
| H | org 1, A's grant plus `episode:write` | the only caller offered `record_episode` |

## Rows

Every caller is exercised on every tool it may reach, and D–G on every tool at all.

- `tools/list` per caller: exact catalogue (A, B, C list four tools, H lists five).
- `investigate_question`: A and B are answered on their own identity.
- `investigation_result`: A and B read their own result. B and C reading A's result id, and A reading B's, are denied. The denial must equal the denial of an id that never existed, and carry none of the owner's data. A denial by the organization scope writes no stored-result decision line; a denial by the live grant writes exactly one, `denied`.
- `source_evidence`: A reads the evidence; B and C are denied like an unknown reference.
- `context_for_task` with an explicit scope: an out-of-grant repository is refused with the public `repo_forbidden` code before any store is consulted; another organization naming the same repository is refused or answered without the owner's data.
- `record_episode`: not listed and not callable for A, B and C; the episode sink is never reached.
- D, E, F, G on `tools/list` and all five tools: HTTP 401 with the fixed body and challenge, before the SDK handler runs. D and E reach acr-api once (its own 401); F and G never reach it.

## Running the matrix against a deployment

Nothing is committed: the endpoint and every bearer come from the environment. The test binary captures the `ACR_MCP_MATRIX_*` variables before it removes every other `ACR_` variable from the process, so they reach the live test and nothing else. With `ACR_MCP_MATRIX_URL` unset the live test **skips loudly** ("LIVE MATRIX NOT EXECUTED") and never passes. Set `ACR_MCP_MATRIX_REQUIRE_LIVE=1` in a job that must execute it and an unset endpoint fails instead. A configuration that names an endpoint but misses a required setting fails and lists the missing names.

Required:

| Variable | Meaning |
| --- | --- |
| `ACR_MCP_MATRIX_URL` | the MCP endpoint, e.g. `https://acr-mcp.example/mcp` |
| `ACR_MCP_MATRIX_BEARER_A` | caller A |
| `ACR_MCP_MATRIX_BEARER_B` | caller B (another organization) |
| `ACR_MCP_MATRIX_BEARER_C` | caller C (A's organization, narrower repository grant) |
| `ACR_MCP_MATRIX_BEARER_D` | a **revoked** credential |
| `ACR_MCP_MATRIX_BEARER_E` | an **expired** credential |
| `ACR_MCP_MATRIX_RESULT_A` | a stored investigation result id owned by A |
| `ACR_MCP_MATRIX_EVIDENCE_A` | an evidence reference id readable by A |
| `ACR_MCP_MATRIX_REPO_A` | a repository slug in A's grant, and in neither C's grant nor B's organization |
| `ACR_MCP_MATRIX_REPO_OUT` | a repository slug outside every grant |
| `ACR_MCP_MATRIX_CONTEXT_MARKER` | text a `context_for_task` packet for A carries; asserted present for A and absent for B, so a deployment that leaks A's data to B cannot pass |

Optional (a row that needs an absent setting is reported `NOT EXECUTED` in the test output):

| Variable | Meaning |
| --- | --- |
| `ACR_MCP_MATRIX_BEARER_H` | a caller with `episode:write`; enables the five-tool catalogue row |
| `ACR_MCP_MATRIX_RESULT_B` | a stored result id owned by B; enables B's own-read and A's foreign-read rows |
| `ACR_MCP_MATRIX_BRANCH`, `ACR_MCP_MATRIX_COMMIT` | scope of the `context_for_task` rows |
| `ACR_MCP_MATRIX_INVESTIGATE=1` | also run the `investigate_question` rows (they cost a model call) |
| `ACR_MCP_MATRIX_FORBID` | comma-separated text that must never appear in a denial (a judgment, a label) |
| `ACR_MCP_MATRIX_CA_FILE` | PEM bundle that signs the endpoint's certificate |

```bash
export ACR_MCP_MATRIX_URL=https://acr-mcp.example/mcp
export ACR_MCP_MATRIX_REQUIRE_LIVE=1
# ACR_MCP_MATRIX_BEARER_* and the fixture ids: from the deployment's secret store, never from the repository
go test -count=1 -v -run '^TestAuthMatrixLive$' ./internal/mcp/
```

Read the result per row: `--- PASS` and `--- SKIP` lines are rows; a `NOT EXECUTED` skip is a row that did not run, not a pass.

## What the in-process target stands in for

The evidence store and the stored-result store are the in-memory production twins (`contextpacket.EvaluationStore` over the fixed evaluation corpus, `memoryinvestigation.Store`); the ClickHouse and Postgres adapters carry the same guards behind the same ports and are covered by their own suites and by the live run. The graph the stored-result gate reads, the investigator and the episode sink are test stand-ins. The graph is deliberately organization-agnostic so that the result store's organization scope is observable on its own.
