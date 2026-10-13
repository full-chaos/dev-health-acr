package contextpacket

import contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"

// SQL spellings of the evidence provenance vocabulary. A source query that
// emits a provenance builds it from these, so no statement carries its own
// copy of a served value.
const (
	sqlProvenanceNative       = "'" + contractsv1.EvidenceProvenanceNative + "'"
	sqlProvenanceExplicitText = "'" + contractsv1.EvidenceProvenanceExplicitText + "'"
	sqlProvenanceHeuristic    = "'" + contractsv1.EvidenceProvenanceHeuristic + "'"
	sqlProvenanceDerived      = "'" + contractsv1.EvidenceProvenanceDerived + "'"
)

// provenanceRankSQL orders rows the way provenanceRank orders references.
const provenanceRankSQL = "multiIf(provenance = " + sqlProvenanceNative + ", 0, provenance = " + sqlProvenanceExplicitText + ", 1, provenance = " + sqlProvenanceDerived + ", 2, provenance = " + sqlProvenanceHeuristic + ", 3, 4)"
