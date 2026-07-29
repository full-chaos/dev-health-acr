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

## Releases

Every successful push to `main` runs the complete release matrix. The workflow
publishes:

- `acr-api` and `acr-mcp` archives for Linux AMD64/ARM64, macOS AMD64/ARM64,
  and Windows AMD64 in a GitHub Release tagged `main-<full-sha>` and targeted at the exact commit;
- multi-platform Linux container images for both products under the immutable
  full commit SHA and the Dev Health standard `sha-<7-character-sha>` alias;
- the current tip of `main` under both the `main` and `latest` GHCR aliases;
- OCI archives, SPDX SBOMs, manifests, checksums, and a Sigstore verification
  bundle as GitHub Release assets.

For example, the current API image is available as:

```text
ghcr.io/full-chaos/dev-health-acr/acr-api:<full-sha>
ghcr.io/full-chaos/dev-health-acr/acr-api:sha-<7-character-sha>
ghcr.io/full-chaos/dev-health-acr/acr-api:main
ghcr.io/full-chaos/dev-health-acr/acr-api:latest
```

The same aliases are published for `acr-mcp`. The `main-<full-sha>` GitHub Release is
marked **Latest** only after the publisher rechecks that the commit is still the
current tip of `main`. A completed older build keeps its immutable full-SHA and
short-SHA references, but cannot move either `main` or `latest` backward.

A canonical `vMAJOR.MINOR.PATCH` tag, optionally followed by `-dev.N` or
`-beta.N`, publishes the same verified matrix under immutable version tags. A
versioned release never replaces the main channel's Latest marker. Container
images and `SHA256SUMS` are signed keylessly by the release workflow, and
production deployments should continue to use the manifest-recorded
`@sha256:` digest. A failed version-tag run can be recovered without moving the
tag by running the **Release** workflow from `main` and supplying the existing
tag. See [`docs/release-policy.md`](docs/release-policy.md).

## Optional local CodeGraph evidence

`acr-mcp` can supplement, but never replace, an authoritative hosted context
packet with bounded evidence from an **existing** local CodeGraph index. The
sidecar owns neither CodeGraph installation nor index creation, refresh, or
storage. Its direct/managed guard accepts only the supported read-only JSON
commands (`status`, `query`, `callers`, `callees`, `impact`, `affected`, and
`files`) from CodeGraph `>=1.2.0,<2.0.0`; it never runs `init`, `index`, or
`sync`.

The optional local configuration is isolated from hosted sidecar configuration:
invalid, unavailable, or incompatible local state degrades to hosted-only
operation rather than blocking hosted bootstrap. With the default `graceful`
policy, usable stale local evidence remains labeled stale beside the hosted
packet; `strict` omits stale or mismatched local evidence. See
[`docs/mcp-sidecar.md`](docs/mcp-sidecar.md) for exact settings and
[`docs/operations.md`](docs/operations.md) for diagnostic and verification
limits.

## Deployment and operations

The private ACR developer and operator lifecycle, including ownership,
TLS-local Compose, private Helm/Kustomize, migration/rollback boundaries,
credential rotation, backup/restore responsibilities, observability,
troubleshooting, and sidecar setup is in
[`docs/operations.md`](docs/operations.md). The offline documentation gate is:

```bash
bash scripts/docs/verify.sh
```

## Container images

Reproducible, hardened container images for `acr-api` (plus the separate
`acr-migrate` command) and `acr-mcp` are documented in
[`docs/container-images.md`](docs/container-images.md): pinned build inputs,
non-root numeric UID/GID `65532:65532`, read-only root filesystem, the
reviewed local build allowlist, hardened release-context wrapper, and SBOM/scan
gates. Local targets keep outputs under `.tmp/`; the release workflow publishes
the exact verified OCI archives to GHCR without rebuilding them and attaches the
same archives to the GitHub Release.

```bash
make container-contract
make container-pins
make container-test
make container-oci
make container-scan
make container-reproducible
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
