package v1

// Served value sets of the context packet and the stored commit decision.
// Producers and validators use only these constants.
const (
	SourceWatermarkStatusFresh       = "fresh"
	SourceWatermarkStatusStale       = "stale"
	SourceWatermarkStatusMissing     = "missing"
	SourceWatermarkStatusUnavailable = "unavailable"
	SourceWatermarkStatusUnknown     = "unknown"

	EvidenceProvenanceNative       = "native"
	EvidenceProvenanceExplicitText = "explicit_text"
	EvidenceProvenanceHeuristic    = "heuristic"
	EvidenceProvenanceDerived      = "derived"

	CommitGateCallerHintShortCircuit = "caller_hint_short_circuit"
	CommitGatePreCommittedExactHint  = "pre_committed_exact_hint"
	CommitGateExactIndex             = "exact_index"
	CommitGateIdentityFastPath       = "identity_fast_path"
	CommitGateLoneFloor              = "lone_floor"
	CommitGateTopOfTwo               = "top_of_two"
	CommitGateVectorMarginRescue     = "vector_margin_rescue"
	CommitGateEvidenceCensus         = "evidence_census"
)

func SourceWatermarkStatusVocabulary() []string {
	return []string{SourceWatermarkStatusFresh, SourceWatermarkStatusStale, SourceWatermarkStatusMissing, SourceWatermarkStatusUnavailable, SourceWatermarkStatusUnknown}
}

func EvidenceProvenanceVocabulary() []string {
	return []string{EvidenceProvenanceNative, EvidenceProvenanceExplicitText, EvidenceProvenanceHeuristic, EvidenceProvenanceDerived}
}

// CommitGateVocabulary lists the recorded gates; "" (nothing recorded) is valid
// on the wire but is not a gate.
func CommitGateVocabulary() []string {
	return []string{
		CommitGateCallerHintShortCircuit, CommitGatePreCommittedExactHint, CommitGateExactIndex, CommitGateIdentityFastPath,
		CommitGateLoneFloor, CommitGateTopOfTwo, CommitGateVectorMarginRescue, CommitGateEvidenceCensus,
	}
}
