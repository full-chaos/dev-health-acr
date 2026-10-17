package contextfabric

import (
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The closed vocabularies of the window binder, stated window axis, window
// canonicalization, window and kind carry, and confirmed need ledger window
// lines, exported so the event specification reads them from the producer's own
// constants.

// WindowBindReasonVocabulary is the closed set of reasons the temporal binder reports.
func WindowBindReasonVocabulary() []string {
	return []string{string(WindowBindNoSpan), string(WindowBindSpanUnbound), string(WindowBindSpanAmbiguous), string(WindowBindRoutedInferred)}
}

// WindowBindGrammarVocabulary is the closed set of grammar names a routed span carries; the empty name is a binder reason that routed no span.
func WindowBindGrammarVocabulary() []string {
	out := []string{""}
	for _, entry := range windowGrammarRegistry {
		out = append(out, entry.name)
	}
	return out
}

// RelativeWindowIDLogVocabulary is the closed set of relative window ids a window line carries; the empty id is a line whose window names none.
func RelativeWindowIDLogVocabulary() []string {
	out := []string{""}
	for _, id := range contractsv1.ContextFabricRelativeWindowIDVocabulary() {
		out = append(out, string(id))
	}
	return out
}

// WindowClassLogVocabulary is the closed set of window classes a window line carries; the empty class is an effective window that names none.
func WindowClassLogVocabulary() []string {
	out := []string{""}
	for _, class := range contractsv1.ContextFabricWindowClassVocabulary() {
		out = append(out, string(class))
	}
	return out
}

// WindowProvenanceLogVocabulary is the closed set of window provenances a window line carries.
func WindowProvenanceLogVocabulary() []string {
	return []string{
		string(contractsv1.ContextFabricWindowInferredDefault), string(contractsv1.ContextFabricWindowQuestionStated),
		string(contractsv1.ContextFabricWindowClarificationConfirmed),
	}
}

// TemporalAxisLogVocabulary is the closed set of temporal axes a stated window line carries; the empty axis is an interpretation that proposed no time bound.
func TemporalAxisLogVocabulary() []string {
	out := []string{""}
	for _, axis := range contractsv1.ContextFabricTemporalAxisVocabulary() {
		out = append(out, string(axis))
	}
	return out
}

// StatedWindowOriginVocabulary is the closed set of channels that supplied a stated window.
func StatedWindowOriginVocabulary() []string {
	return []string{StatedWindowOriginField, StatedWindowOriginQuestionPhrase, StatedWindowOriginInterpreterRange}
}

// StatedWindowAxisOutcomeVocabulary is the closed set of stated window axis outcomes.
func StatedWindowAxisOutcomeVocabulary() []string {
	return []string{
		string(StatedWindowAxisAgreed), string(StatedWindowAxisOverridden), string(StatedWindowAxisVetoed), string(StatedWindowAxisStatedRange),
		string(StatedWindowAxisWithdrawnPointInTime), string(StatedWindowAxisWithdrawnPeriodShape),
	}
}

// WindowCanonicalizationOutcomeVocabulary is the closed set of window canonicalization outcomes.
func WindowCanonicalizationOutcomeVocabulary() []string {
	return []string{
		string(WindowCanonicalizationNone), string(WindowCanonicalizationRequestStated), string(WindowCanonicalizationReceiptConfirmed),
		string(WindowCanonicalizationInferredDefault), string(WindowCanonicalizationVetoUnresolved), string(WindowCanonicalizationVetoConflict),
		string(WindowCanonicalizationVetoAxisConflict), string(WindowCanonicalizationVetoStaleSupersededOffer),
		string(WindowCanonicalizationGatedClassDefault), string(WindowCanonicalizationGatedRefusedNoClarification), string(WindowCanonicalizationCarried),
	}
}

// WindowGatedOriginVocabulary is the closed set of origins of a window gated for confirmation.
func WindowGatedOriginVocabulary() []string {
	return []string{string(WindowCanonicalizationGatedClassDefault), string(WindowCanonicalizationGatedRefusedNoClarification)}
}

// WindowCarryOutcomeVocabulary is the closed set of window carry outcomes a line records; a carry never attempted writes no line.
func WindowCarryOutcomeVocabulary() []string {
	return []string{
		string(WindowCarryHit), string(WindowCarryMissNoReference), string(WindowCarryMissUnloadable), string(WindowCarryMissStaleGraphEpoch),
		string(WindowCarryMissNoConfirmedWindow), string(WindowCarryMissDepthExceeded), string(WindowCarryMissConflictingWindows),
		string(WindowCarryMissQuestionDrift), string(WindowCarryMissQuestionIndeterminate),
	}
}

// KindCarryOutcomeVocabulary is the closed set of kind carry outcomes a line records; a carry never attempted writes no line.
func KindCarryOutcomeVocabulary() []string {
	return []string{
		string(KindCarryHit), string(KindCarryMissNoReference), string(KindCarryMissUnloadable), string(KindCarryMissStaleGraphEpoch),
		string(KindCarryMissNoConfirmedKind), string(KindCarryMissDepthExceeded), string(KindCarryMissVetoedCarrier),
		string(KindCarryDroppedRedeemedKindDiffers), string(KindCarryMissConflictingKinds), string(KindCarryMissQuestionDrift),
		string(KindCarryMissQuestionIndeterminate),
	}
}

// CarrySeedSourceVocabulary is the closed set of sources a carry was seeded from.
func CarrySeedSourceVocabulary() []string {
	return []string{string(CarrySeedNone), string(CarrySeedReceipt), string(CarrySeedParentField), string(CarrySeedBoth)}
}

// ConfirmedNeedLedgerWindowDecisionVocabulary is the closed set of decisions of the ledger window consumer.
func ConfirmedNeedLedgerWindowDecisionVocabulary() []string {
	out := make([]string, 0, 2)
	for _, decision := range confirmedNeedLedgerWindowDecisions() {
		out = append(out, string(decision))
	}
	return out
}

// ConfirmedNeedLedgerAppliedWindowVocabulary is the closed set of applied window values a ledger window line carries: a relative id, or the word absolute for a window with bounds.
func ConfirmedNeedLedgerAppliedWindowVocabulary() []string {
	out := []string{confirmedNeedLedgerWindowAbsolute}
	for _, id := range contractsv1.ContextFabricRelativeWindowIDVocabulary() {
		out = append(out, string(id))
	}
	return out
}
