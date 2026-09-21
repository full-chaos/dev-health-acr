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

## Hosted end-to-end proof

`TestHostedEndToEndLive` (`internal/mcp/hosted_e2e_test.go`) drives the same deployed endpoint the way an external agent does: the go-sdk client over Streamable HTTP, one bearer per caller. It reuses `ACR_MCP_MATRIX_URL`, `ACR_MCP_MATRIX_BEARER_A`, `ACR_MCP_MATRIX_BEARER_C`, `ACR_MCP_MATRIX_REPO_A` and `ACR_MCP_MATRIX_REQUIRE_LIVE`, skips loudly when the endpoint is unset, and names every missing setting when it is set only in part. `TestHostedEndToEndRunnerAgainstAnInProcessEndpoint` runs the same steps in CI against the in-process endpoint above.

| Step | What it asserts |
| --- | --- |
| `01_discover_2026_07_28` | the client's first request is `server/discover`, it negotiates 2026-07-28, and it never falls back to `initialize` |
| `02_catalogue_per_credential` | each caller's `tools/list` equals its expected catalogue; `resources/list` carries `acr://guide/*`; `prompts/list` carries `investigate`, `continue_investigation`, `expand_evidence` |
| `03_context_for_task` | an explicit repository returns a packet with evidence references; no repository returns the typed refusal naming `repository.slug` |
| `04_investigate_question` | a real question, with every clarification answered from the latest answer's first option, ends in an answer carrying the completeness, coverage and budget contract fields |
| `05_investigation_result_by_id` | `include_full_result` under a byte budget declares `full_result_omitted` instead of attaching an oversized result; `investigation_result` returns every result id read back |
| `06_source_evidence_*` | a `context_for_task` reference and an `investigate_question` reference each expand, marked untrusted |
| `08_concurrent_callers_no_identity_bleed` | A and C, interleaved on the one workload, are each allowed exactly on their own grant, and every response echoes its own correlation id |

A rate-limited answer decides nothing about identity or scope: the runner retries it after 40 seconds and logs every retry (`E2E-RETRY`). The deployment's request budget is not relaxed for the proof.

| Variable | Meaning |
| --- | --- |
| `ACR_MCP_MATRIX_E2E_REPO_C` | a repository in C's grant; C's grant must not hold `ACR_MCP_MATRIX_REPO_A` (required) |
| `ACR_MCP_MATRIX_E2E_QUESTION` | a question answerable on A's data (required) |
| `ACR_MCP_MATRIX_E2E_GRANT_A` | comma-separated repositories in A's grant (default: `ACR_MCP_MATRIX_REPO_A`) |
| `ACR_MCP_MATRIX_E2E_CLARIFY_QUESTION` | a question expected to need clarification (optional) |
| `ACR_MCP_MATRIX_E2E_TOOLS_A`, `_C`, `_H` | expected catalogues (default: the four answer tools); `ACR_MCP_MATRIX_BEARER_H` adds a third caller |
| `ACR_MCP_MATRIX_E2E_SERVER_REVISION` | text the discovered `serverInfo.version` must carry |
| `ACR_MCP_MATRIX_E2E_BUDGET_BYTES`, `ACR_MCP_MATRIX_E2E_EXPECT_OMITTED=1` | byte budget of step 05 (default 8192), and whether it must drop the full result |
| `ACR_MCP_MATRIX_RESULT_A` | a stored result owned by A, also read back (optional) |
| `ACR_MCP_MATRIX_E2E_CONCURRENCY` | requests per caller in step 08 (default 24) |
| `ACR_MCP_MATRIX_E2E_OUT` | directory for the captured transcripts; a transcript that carries a bearer is refused, never written |

```bash
go test -count=1 -v -timeout 45m -run '^TestHostedEndToEndLive$' ./internal/mcp/
```
