# ADR-0001: ACR is a private Go service and Go MCP sidecar

**Status:** Accepted  
**Date:** 2026-07-10

## Decision

ACR is implemented in a new private repository as two Go binaries:

- `acr-api`: focused hosted service.
- `acr-mcp`: local STDIO MCP sidecar.

The Context Packet Explorer remains in `dev-health-web` and is the only non-Go ACR component.

## Rationale

- One language for service, contract, API client, and sidecar behavior.
- Single-binary distribution for local agent integrations.
- Lower runtime overhead than adding more Python async workers.
- A clean commercial boundary: the ACR service is not shipped in the default self-hosted distribution.
- Shared Go contract types reduce API/sidecar drift.

## Boundaries

- `dev-health-ops` continues to ingest and normalize engineering evidence and maintains billing/entitlement state.
- ACR reads engineering evidence through a read-only `EvidenceStore` implementation backed by existing ClickHouse tables.
- ACR owns packet snapshots, episode metadata, client credentials, and ACR audit records in an ACR-owned Postgres database/schema.
- `dev-health-web` calls ACR through a server-side integration. Browser code never receives a service credential.
- External Push remains `/api/v1/external-ingest/*` and is not reused as ACR transport.

## Consequences

- Raw ClickHouse schema coupling must be isolated behind versioned query adapters.
- ACR deployment needs read-only ClickHouse access and controlled entitlement/auth integration.
- Cross-service tracing and compatibility metadata are required.
- A future graph store can replace or supplement evidence adapters without changing external contracts.
