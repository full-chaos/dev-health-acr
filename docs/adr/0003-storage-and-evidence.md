# ADR-0003: ClickHouse evidence reads, ACR-owned Postgres state, no graph database in SVS

**Status:** Accepted  
**Date:** 2026-07-10

## Decision

SVS storage is split by responsibility:

- **Existing Dev Health ClickHouse:** read-only engineering evidence and work graph.
- **ACR-owned Postgres:** packet snapshots, agent episodes, ACR client credentials, retention metadata, and audit records.
- **No context graph database in SVS.**

## Required interfaces

```go
type EvidenceStore interface {
    ResolveScope(...)
    FindTaskEvidence(...)
    FindFileEvidence(...)
    FindAIWorkflowEvidence(...)
    ExpandEvidence(...)
    Watermarks(...)
}

type PacketStore interface {
    SaveSnapshot(...)
    GetSnapshot(...)
    PurgeExpired(...)
}

type EpisodeStore interface {
    CreateIdempotent(...)
    GetByClientEpisodeID(...)
    Redact(...)
    PurgeExpired(...)
}
```

## Context graph compatibility

Every packet item has a stable ID, claim kind, rule ID, validity scope, related entities, and evidence references. These can later map to temporal graph nodes/edges. Episode data remains evidence and is never automatically promoted into durable truth.

## Query adapter rule

ACR must not scatter raw ClickHouse SQL across handlers. Queries are grouped into versioned adapters. Each packet records `query_version`, `ranking_version`, source watermarks, coverage, and degradation state.
