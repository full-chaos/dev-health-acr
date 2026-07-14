# Dev Health Agent Context Runtime

Private SVS repository for the Dev Health Agent Context Runtime (ACR).

ACR exposes Dev Health's evidence-backed diagnosis loop to coding, review, docs, and CI agents:

> **State → Pressure → Cause → Evidence → Action**

Everything in the ACR service family is implemented in Go except the Context Packet Explorer in `dev-health-web`.

This repository produces two binaries:

- `acr-api`: focused hosted API that assembles context packets, expands authorized provenance, and later records opt-in agent episodes.
- `acr-mcp`: local STDIO MCP sidecar that connects compatible agent clients to the hosted ACR API.

## Status

The contract-first Go service includes scoped credentials, deterministic context assembly, authorized evidence expansion, request controls, and the hosted read-route boundary. The stock API composes production database and entitlement adapters as one fail-closed runtime bundle. Episode HTTP writeback remains disabled unless explicitly enabled.

## Product boundary

- `dev-health-ops` remains the source of engineering evidence, work graph data, billing, and organization entitlements.
- `dev-health-acr` is a separate private hosted Go service and local Go MCP binary.
- `dev-health-web` remains the Next.js human inspection surface.
- External Push remains a separate source-fact ingestion API.
- ACR is not included in the default self-hosted Dev Health distribution.
- The product entitlement (`agent_context_runtime`) is separate from ACR API credentials and scopes.

## Repository layout

```text
cmd/acr-api/                  Hosted Go API entrypoint
cmd/acr-mcp/                  Local STDIO MCP entrypoint
cmd/contractcheck/            Go-only contract validation and artifact refresh
internal/contracts/v1/        Canonical Go DTOs and validation
internal/contractcheck/       Contract profile validator
contracts/jsonschema/v1/      JSON Schema 2020-12 wire contracts
contracts/openapi/            Canonical OpenAPI 3.1 JSON + JSON-compatible YAML mirror
contracts/mcp/                MCP tool contract bundle
contracts/examples/v1/        Golden request/response fixtures
docs/adr/                     Architecture decisions
docs/                         PRD, threat model, versioning, implementation notes
docs/implementation-backlog.md  Linear issue reinterpretation and critical path
docs/service-shell.md          `acr-api` configuration and operational behavior
```

## Contract versions

- `context_packet_request.v1`
- `context_packet.v1`
- `context_packet_item.v1`
- `evidence_ref.v1`
- `expanded_evidence.v1`
- `capabilities.v1`
- `acr_client_credential.v1`
- `agent_episode_create.v1`
- `agent_episode.v1`
- `error.v1`

MCP tool contract (`contracts/mcp/tools.v1.json`) and its wire schemas:

- `mcp_tools.v1`
- `mcp_context_for_task_request.v1`
- `mcp_context_for_task_response.v1`
- `mcp_source_evidence_request.v1`
- `mcp_source_evidence_response.v1`

JSON Schema is the wire-contract source of truth. Go DTOs, OpenAPI, MCP definitions, web types, examples, and compatibility tests must remain aligned. Contract checks are Go-only and require no Python runtime.

## Local verification

```bash
make contract-test
make verify
make hosted-integration
go build ./cmd/acr-api ./cmd/acr-mcp ./cmd/contractcheck
```

Private GitHub import instructions are in [`docs/repository-bootstrap.md`](docs/repository-bootstrap.md).

Run the hosted API locally:

```bash
ACR_ADDR=:8080 go run ./cmd/acr-api serve
curl http://127.0.0.1:8080/healthz
curl http://127.0.0.1:8080/readyz
```

The stock development binary reports not-ready and serves safe `503` read-route
stubs until a hosting build supplies the complete runtime adapter bundle.

Inspect sidecar metadata and local diagnostics:

```bash
go run ./cmd/acr-mcp version
go run ./cmd/acr-mcp metadata
ACR_API_URL=https://acr.fullchaos.dev \
ACR_API_TOKEN='redacted' \
go run ./cmd/acr-mcp doctor
```

## Initial hosted API shape

```http
GET  /healthz
GET  /readyz
GET  /api/v1/agent-context/capabilities
POST /api/v1/agent-context/context-packets
GET  /api/v1/agent-context/evidence/{evidence_ref_id}
POST /api/v1/agent-context/episodes
```

Read APIs require the `agent_context_runtime` product entitlement and the relevant `context:read` or `evidence:read` scope. Episode writeback is disabled by default in the MCP sidecar and additionally requires explicit local enablement plus `episode:write` server permission.

## Licensing posture

This repository is private during SVS. No public source license is granted by the repository at this stage. The intended license if the sidecar is later published as an ecosystem adapter is Apache 2.0. Commercial enforcement belongs to the hosted ACR API entitlement boundary, not hidden client-side logic.
