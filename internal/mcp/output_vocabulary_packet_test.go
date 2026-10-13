package mcp

import (
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func init() {
	registerOutputVocabularies(
		outputVocabulary{
			name:  "entries.status",
			kind:  vocabFrozen,
			why:   "state-machine",
			sites: []string{"source_evidence:entries.items.status"},
			// schema: "served", "refused"
			goSet:      strs(contractsv1.MCPSourceEvidenceEntryServed, contractsv1.MCPSourceEvidenceEntryRefused),
			exhaustive: true,
		},
		outputVocabulary{
			name:  "v1.status",
			kind:  vocabFrozen,
			why:   "state-machine",
			sites: []string{"context_packet.v1.status"},
			// schema: "complete", "partial", "degraded", "empty"
			goSet:      strs(contractsv1.PacketComplete, contractsv1.PacketPartial, contractsv1.PacketDegraded, contractsv1.PacketEmpty),
			exhaustive: true,
		},
		outputVocabulary{
			name:           "resolved_scope.resolution",
			kind:           vocabAdditive,
			clientValidate: "contractsv1.ContextPacket.ValidateRead",
			sites:          []string{"context_packet.v1.resolved_scope.resolution"},
			// schema: "exact_commit", "branch_filtered", "repo_fallback", "unresolved"
			goSet:      strs(contractsv1.ScopeExactCommit, contractsv1.ScopeBranchFiltered, contractsv1.ScopeRepoFallback, contractsv1.ScopeUnresolved),
			exhaustive: true,
		},
		outputVocabulary{
			name:           "items.status",
			kind:           vocabAdditive,
			clientValidate: "contractsv1.ContextPacket.ValidateRead",
			sites:          []string{"context_packet.v1.freshness.watermarks.items.status"},
			// schema: "fresh", "stale", "missing", "unavailable"
			goSet:      contractsv1.SourceWatermarkStatusVocabulary(),
			exhaustive: true,
		},
		outputVocabulary{
			name:           "v1.category",
			kind:           vocabAdditive,
			clientValidate: "contractsv1.ContextPacket.ValidateRead",
			sites:          []string{"context_packet_item.v1.category"},
			// schema: "state", "pressure", "cause", "evidence", "action"
			goSet:      strs(contractsv1.CategoryState, contractsv1.CategoryPressure, contractsv1.CategoryCause, contractsv1.CategoryEvidence, contractsv1.CategoryAction),
			exhaustive: true,
		},
		outputVocabulary{
			name:           "v1.claim_kind",
			kind:           vocabAdditive,
			clientValidate: "contractsv1.ContextPacket.ValidateRead",
			sites:          []string{"context_packet_item.v1.claim_kind"},
			// schema: "observed", "inferred", "recommendation"
			goSet:      strs(contractsv1.ClaimObserved, contractsv1.ClaimInferred, contractsv1.ClaimRecommendation),
			exhaustive: true,
		},
		outputVocabulary{
			name:           "v1.severity",
			kind:           vocabAdditive,
			clientValidate: "contractsv1.ContextPacket.ValidateRead",
			sites:          []string{"context_packet_item.v1.severity"},
			// schema: "info", "warning", "high", "critical"
			goSet:      strs(contractsv1.SeverityInfo, contractsv1.SeverityWarning, contractsv1.SeverityHigh, contractsv1.SeverityCritical),
			exhaustive: true,
		},
		outputVocabulary{
			name:           "v1.status#2",
			kind:           vocabAdditive,
			clientValidate: "none: minted by the local sidecar federation, never read from a server",
			sites:          []string{"mcp_local_context.v1.status"},
			// schema: "available", "degraded", "unavailable"
			goSet:      strs(contractsv1.MCPLocalContextAvailable, contractsv1.MCPLocalContextDegraded, contractsv1.MCPLocalContextUnavailable),
			exhaustive: true,
		},
		outputVocabulary{
			name:           "v1.freshness",
			kind:           vocabAdditive,
			clientValidate: "none: minted by the local sidecar federation, never read from a server",
			sites:          []string{"mcp_local_context.v1.freshness"},
			// schema: "fresh", "stale", "unknown"
			goSet:      strs(contractsv1.MCPLocalFreshnessFresh, contractsv1.MCPLocalFreshnessStale, contractsv1.MCPLocalFreshnessUnknown),
			exhaustive: true,
		},
		outputVocabulary{
			name:           "v1.provenance",
			kind:           vocabAdditive,
			clientValidate: "contractsv1.EvidenceRef.ValidateRead",
			sites:          []string{"evidence_ref.v1.provenance"},
			// schema: "native", "explicit_text", "heuristic", "derived"
			goSet:      contractsv1.EvidenceProvenanceVocabulary(),
			exhaustive: true,
		},
		outputVocabulary{
			name:           "v1.availability",
			kind:           vocabAdditive,
			clientValidate: "contractsv1.ExpandedEvidence.ValidateRead",
			sites:          []string{"evidence_ref.v1.availability", "expanded_evidence.v1.availability"},
			// schema: "available", "stale", "redacted", "deleted", "unauthorized"
			goSet:      strs(contractsv1.EvidenceAvailable, contractsv1.EvidenceStale, contractsv1.EvidenceRedacted, contractsv1.EvidenceDeleted, contractsv1.EvidenceUnauthorized),
			exhaustive: true,
		},
	)
}
