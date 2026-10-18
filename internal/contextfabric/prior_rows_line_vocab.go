package contextfabric

import (
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The closed vocabularies of the prior consultation and projected rows lines,
// exported so the event specification reads them from the producer's own
// constants.

// PriorConsultedOutcomeVocabulary is the closed set of outcomes a prior consulted line names.
func PriorConsultedOutcomeVocabulary() []string {
	return []string{
		string(PriorConsultedOffered), string(PriorConsultedSuppressedVerification), string(PriorConsultedSuppressedRevoked), string(PriorConsultedDegraded),
	}
}

// PriorDegradationPointerDanglingVocabulary is the one state the Warn prior degradation line names.
func PriorDegradationPointerDanglingVocabulary() []string {
	return []string{string(PriorDegradationPointerDangling)}
}

// PriorDegradationInfoVocabulary is the closed set of states the Info prior degradation line names: every state but none and the dangling pointer, which logs at Warn.
func PriorDegradationInfoVocabulary() []string {
	return []string{string(PriorDegradationStoreUnavailable), string(PriorDegradationEntryRevoked), string(PriorDegradationFlipCASConflict)}
}

// ProjectedRowsFactKindLogVocabulary is the closed set of fact kinds a projected rows by fact kind line names: the requestable kinds, the cardinality claim kind, and the class a kind outside the contract is logged as.
func ProjectedRowsFactKindLogVocabulary() []string {
	all := contractsv1.ContextFabricFactKindVocabulary()
	out := make([]string, 0, len(all)+2)
	for _, kind := range all {
		out = append(out, string(kind))
	}
	return append(out, string(contractsv1.ContextFabricFactCardinality), structureLineUnrecognised)
}

// projectedRowsFactKindLogValue logs a fact kind when it is a contract kind and the class otherwise.
func projectedRowsFactKindLogValue(kind FactKind) string {
	if kind == contractsv1.ContextFabricFactCardinality {
		return string(kind)
	}
	for _, member := range contractsv1.ContextFabricFactKindVocabulary() {
		if kind == member {
			return string(kind)
		}
	}
	return structureLineUnrecognised
}
