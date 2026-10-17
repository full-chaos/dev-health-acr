package contextfabric

import (
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The closed vocabularies of the structure, offer and interpreted time bound
// lines, exported so the event specification reads them from the producer's
// own constants.

// StructureNeedKindLogVocabulary is the closed set of structure members a structure line names.
func StructureNeedKindLogVocabulary() []string {
	all := contractsv1.ContextFabricStructureNeedKindVocabulary()
	out := make([]string, 0, len(all))
	for _, kind := range all {
		out = append(out, string(kind))
	}
	return out
}

// StructureOfferSourceLogVocabulary is the closed set of offer sources a structure offer count line names.
func StructureOfferSourceLogVocabulary() []string {
	return []string{string(contractsv1.ContextFabricStructureOfferEngine), string(contractsv1.ContextFabricStructureOfferPrior)}
}

// GatedOfferResolutionOutcomeVocabulary is the closed set of outcomes of the offers-only resolution under a window gate.
func GatedOfferResolutionOutcomeVocabulary() []string {
	return []string{
		string(GatedOfferResolutionComposed), string(GatedOfferResolutionEmpty), string(GatedOfferResolutionFailed),
		string(GatedOfferResolutionDisabled), string(GatedOfferResolutionRefused), string(GatedOfferResolutionNotProjected),
		string(GatedOfferResolutionFrameRefused),
	}
}

// CohortStructureGateOutcomeVocabulary is the closed set of outcomes of the cohort structure gate.
func CohortStructureGateOutcomeVocabulary() []string {
	return []string{string(CohortStructureGateApplied), string(CohortStructureGateNoOp), string(CohortStructureGateSubjectBearing)}
}

// InvestigationShapeLogVocabulary is the closed set of investigation shapes a cohort structure gate line names.
func InvestigationShapeLogVocabulary() []string {
	all := contractsv1.ContextFabricInvestigationShapeVocabulary()
	out := make([]string, 0, len(all)+1)
	for _, shape := range all {
		out = append(out, string(shape))
	}
	return append(out, structureLineUnrecognised)
}

// StructureReceiptOutcomeVocabulary is the closed set of outcomes a structure receipt line names.
func StructureReceiptOutcomeVocabulary() []string {
	return []string{string(StructureReceiptApplied), string(StructureReceiptUnresolved), string(StructureReceiptConflict), string(StructureReceiptStale)}
}

// StructureExplicitOutcomeVocabulary is the closed set of outcomes a structure explicit line names.
func StructureExplicitOutcomeVocabulary() []string {
	return []string{string(StructureExplicitApplied), string(StructureExplicitConflict)}
}

// StructureVetoDetailVocabulary is the closed set of exits a structure receipt veto line names: the receipt loop exits, and the denial reasons of a prior result the caller may not read.
func StructureVetoDetailVocabulary() []string {
	out := []string{
		string(structureDetailNilStore), string(structureDetailEmptyIDs), string(structureDetailStoredResultGet), string(structureDetailStoredNotFound),
		string(structureDetailStoredUnavailable), string(structureDetailOptionNotFound), string(structureDetailReverifyNoVerifier),
		string(structureDetailReverifyUnverif), string(structureDetailReverifyClaimLost), string(structureDetailReverifyRejected),
	}
	for _, reason := range storedResultDeniedReasons() {
		out = append(out, storedResultDeniedDetailPrefix+string(reason))
	}
	return out
}

// storedResultDeniedDetailPrefix starts the veto detail of a prior result the gate denied.
const storedResultDeniedDetailPrefix = "stored_result_denied_"

// storedResultDeniedReasons are the reasons the stored result gate writes beside a denied decision.
func storedResultDeniedReasons() []StoredResultAuthorizationReason {
	return []StoredResultAuthorizationReason{
		StoredResultReasonOrganizationMismatch, StoredResultReasonSubjectDenied, StoredResultReasonSubjectAbsent, StoredResultReasonGroupUnproven,
		StoredResultReasonGraphNotProjected, StoredResultReasonGrantMismatch, StoredResultReasonGrantUnrecorded,
	}
}

// InterpretedTimeBoundOutcomeLogVocabulary is the closed set of verdicts the interpreted time bound line names.
func InterpretedTimeBoundOutcomeLogVocabulary() []string {
	all := InterpretedTimeBoundOutcomeVocabulary()
	out := make([]string, 0, len(all))
	for _, outcome := range all {
		out = append(out, string(outcome))
	}
	return out
}

// InterpretedTimeBoundAxisLogVocabulary is the closed set of axes the interpreted time bound line names: the contract axes, the empty axis of an interpretation that named none, and the class an interpreter axis outside the contract is logged as.
func InterpretedTimeBoundAxisLogVocabulary() []string {
	return append(TemporalAxisLogVocabulary(), structureLineUnrecognised)
}

// structureLineUnrecognised is the class a value outside its closed set is logged as.
const structureLineUnrecognised = continuationTelemetryUnrecognised

// interpretedTimeBoundAxisLogValue logs the axis the interpreter named when it is a contract axis or empty, and the class otherwise: the axis is interpreter output and rides an Info key.
func interpretedTimeBoundAxisLogValue(axis TemporalAxis) string {
	if axis == "" || contractsv1.ValidContextFabricTemporalAxis(axis) {
		return string(axis)
	}
	return structureLineUnrecognised
}

// cohortStructureGateShapeLogValue logs the shape when it is a contract shape and the class otherwise.
func cohortStructureGateShapeLogValue(shape InvestigationShape) string {
	if contractsv1.ValidContextFabricInvestigationShape(shape) {
		return string(shape)
	}
	return structureLineUnrecognised
}
