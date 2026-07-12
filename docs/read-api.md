# Hosted read API

The release-critical hosted read boundary exposes:

```http
GET  /api/v1/agent-context/capabilities
POST /api/v1/agent-context/context-packets
GET  /api/v1/agent-context/evidence/{evidence_ref_id}
POST /api/v1/agent-context/episodes
```

Packet replay remains separate work.

## Runtime composition

`internal/api.RuntimeDependencies` is an all-or-nothing boundary supplied by the
hosting process. It requires a credential store, audit store, organization
entitlement provider, context assembler, and independently authorized evidence
store. A partial bundle is rejected at construction. Without the bundle, read
routes return a safe retryable `503` and readiness must report not-ready.

The API package does not open databases or choose drivers. Production hosting
must construct PostgreSQL credential/audit adapters, one ClickHouse-backed
evidence store, and the organization-scoped Dev Health entitlement adapter. The
same evidence store is passed to both the assembler and evidence route. Merely
setting `ACR_POSTGRES_DSN` or `ACR_CLICKHOUSE_DSN` is not readiness evidence.
This repository's default binary therefore refuses staging/production startup
until those hosting adapters are linked; development starts with fail-closed
read stubs for health and deployment diagnostics.

## Request policy

Every read route accepts only a validated `fcacr_` client credential. Trusted web
assertions remain future work until issuer, audience, key, claims, and revocation
contracts are defined. License keys are never bearer credentials.

Authentication attempt limiting uses the direct peer address by default. A
deployment behind shared reverse proxies must set `ACR_TRUSTED_PROXY_CIDRS`;
only peers in those networks may supply `X-Forwarded-For`, and the resolver
selects the nearest untrusted address from the right side of the proxy chain.

Authentication derives the organization, repository allowlist, and credential
permissions. The API then enforces the route scope, per-organization controls,
and the independent `agent_context_runtime` entitlement. Context requests are
strictly decoded with unknown fields rejected, bounded before allocation, and
repository-authorized after decoding. Caller-supplied repository IDs and remote
URLs are discarded. Evidence IDs are opaque; malformed, unknown, foreign,
deleted, and unauthorized references all return the same `404 not_found`.
Their keyed repository-routing tag prevents an invalid handle from fanning out
reference scans across every authorized repository without disclosing a repo ID.

Every protected agent-context route requires `X-ACR-Client-Version`. It is a
SemVer compatibility signal, not an identity or authorization credential:
authorization derives only from the authenticated `fcacr_` credential, its
scopes, repository allowance, and organization entitlement. Missing,
malformed, older, or explicitly revoked versions return `426 version_mismatch`
with the canonical `minimum_client_version` detail. Security incidents revoke
credentials; `ACR_REVOKED_CLIENT_VERSIONS` is an exact-version denylist for
known incompatible client releases, not a security principal mechanism.

## Error and observability policy

Errors use `error.v1` and never include dependency errors, bearer values, goals,
excerpts, repository candidates, or evidence handles. Server deadlines return
retryable `504 upstream_unavailable`; assembler-owned evidence timeouts may
instead produce a valid `200` degraded packet. Quota denials return `429` with a
bounded `Retry-After` hint.

Successful reads complete resource accounting before response commitment and
record bounded audit actions. Request IDs correlate authentication, limits,
store/ranking observations, audit events, and responses without becoming metric
labels.
