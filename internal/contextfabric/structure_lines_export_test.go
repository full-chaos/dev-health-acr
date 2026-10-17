package contextfabric

import (
	"context"
	"errors"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// RecordStructureNeedsForTest runs the producer that writes the needs
// disclosed and offer count lines for one composed needs block.
func RecordStructureNeedsForTest(ctx context.Context, telemetry EngineTelemetry, principal storage.Principal, needs *contractsv1.ContextFabricStructureNeeds) {
	recordStructureNeedsTelemetry(ctx, telemetry, principal, needs)
}

// StructureVetoKindsForTest are the three veto reasons a receipt batch ends in, by the outcome name each logs.
func StructureVetoKindsForTest() map[string]string {
	return map[string]string{
		"unresolved": string(structureVetoConfirmationUnresolved),
		"conflict":   string(structureVetoConfirmationConflict),
		"stale":      string(structureVetoStaleSupersededOffer),
		"applied":    string(structureVetoNone),
	}
}

// RecordStructureReceiptsForTest runs the producers that write the receipt and explicit lines for a request that carries a receipt for every receipt bearing member and a value for every explicit member, under the given veto.
func RecordStructureReceiptsForTest(ctx context.Context, telemetry EngineTelemetry, principal storage.Principal, veto string) {
	receipt := []contractsv1.ContextFabricBoundSubjectReceipt{{ResultID: "result_1", ReceiptID: "receipt_1"}}
	request := InvestigationRequest{
		PriorKindReceipts: receipt, PriorAnchorReceipts: receipt, PriorHandleReceipts: receipt, PriorCandidateReceipts: receipt,
		ExpectedKinds:  []contractsv1.ContextFabricSubjectKind{contractsv1.ContextFabricSubjectKindVocabulary()[0]},
		SubjectHandles: []contractsv1.ContextFabricRequestedHandle{{}},
	}
	canon := requestStructureCanonicalization{Veto: structureVetoReason(veto)}
	recordStructureReceiptTelemetry(ctx, telemetry, principal, request, canon)
	recordStructureExplicitTelemetry(ctx, telemetry, principal, request, canon)
}

// CohortStructureGateOutcomesForTest runs the gate the engine runs, for a
// family that restricts axes over material that carries an anchor option, over
// empty material, and for a family that restricts none, and returns what the
// gate reports for each.
func CohortStructureGateOutcomesForTest() []CohortStructureGateOutcome {
	material := StructureOfferMaterial{
		Missing:       []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedWindow, contractsv1.ContextFabricStructureNeedSubjectAnchor},
		AnchorOptions: []AnchorOption{{Kind: SubjectTeam, CanonicalID: "team:fullchaos", Label: "Fullchaos"}},
	}
	_, applied := GateOffersByFamily(material, QuestionFamilyOutcome{Family: QuestionFamilyDiscoveredCohortRanking})
	_, noOp := GateOffersByFamily(StructureOfferMaterial{}, QuestionFamilyOutcome{Family: QuestionFamilyDiscoveredCohortRanking})
	_, bearing := GateOffersByFamily(material, QuestionFamilyOutcome{Family: QuestionFamilySubjectInvestigation})
	return []CohortStructureGateOutcome{applied, noOp, bearing}
}

// RecordCohortStructureGateForTest runs the producer that writes the cohort structure gate line.
func RecordCohortStructureGateForTest(ctx context.Context, telemetry EngineTelemetry, principal storage.Principal, outcome CohortStructureGateOutcome, shape InvestigationShape) {
	recordCohortStructureGate(ctx, telemetry, principal, outcome, shape)
}

// StructureVetoDetailsForTest returns every detail the receipt loop and the
// prior result read classifier produce, each driven through its producer.
func StructureVetoDetailsForTest() []structureVetoDetail {
	details := []structureVetoDetail{
		structureDetailNilStore, structureDetailEmptyIDs, structureDetailOptionNotFound, structureDetailReverifyNoVerifier,
		structureDetailReverifyUnverif, structureDetailReverifyClaimLost, structureDetailReverifyRejected,
		storedResultGetDetail(ErrInvestigationResultNotFound), storedResultGetDetail(ErrUnavailable), storedResultGetDetail(errors.New("read failed")),
	}
	for _, reason := range storedResultDeniedReasons() {
		details = append(details, storedResultGetDetail(&storedResultDeniedError{reason: reason}))
	}
	return details
}

// RecordStructureVetoDetailForTest writes the receipt veto line for a detail.
func RecordStructureVetoDetailForTest(ctx context.Context, telemetry SlogEngineTelemetry, principal storage.Principal, detail structureVetoDetail) {
	telemetry.RecordStructureVetoDetail(ctx, principal, detail)
}

// InterpretedTimeBoundDecisionsForTest returns the verdict the engine reaches for each shape of interpreter output, including every refusing arm and an axis outside the contract.
func InterpretedTimeBoundDecisionsForTest(unknownAxis TemporalAxis) []InterpretedTimeBoundDecision {
	now := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	past := now.Add(-48 * time.Hour)
	future := now.Add(48 * time.Hour)
	wide := now.Add(-100 * 365 * 24 * time.Hour)
	zero := time.Time{}
	return []InterpretedTimeBoundDecision{
		resolveInterpretedTimeContext(TimeContext{Axis: TemporalCurrent}, now),
		resolveInterpretedTimeContext(TimeContext{Axis: TemporalValidTime, AsOf: &past}, now),
		resolveInterpretedTimeContext(TimeContext{Axis: TemporalObservedTime, AsOf: &future}, now),
		resolveInterpretedTimeContext(TimeContext{Axis: TemporalRange, Start: &past, End: &now}, now),
		resolveInterpretedTimeContext(TimeContext{Axis: TemporalRange, Start: &past, End: &future}, now),
		resolveInterpretedTimeContext(TimeContext{Axis: TemporalRange, Start: &wide, End: &now}, now),
		resolveInterpretedTimeContext(TimeContext{Axis: TemporalRange, Start: &now, End: &past}, now),
		resolveInterpretedTimeContext(TimeContext{Axis: TemporalRange}, now),
		resolveInterpretedTimeContext(TimeContext{Axis: TemporalValidTime, AsOf: &zero}, now),
		resolveInterpretedTimeContext(TimeContext{Axis: unknownAxis}, now),
	}
}

// RecordPriorConsultOutcomeForTest runs the engine's producer of the prior consulted line.
func RecordPriorConsultOutcomeForTest(ctx context.Context, telemetry EngineTelemetry, principal storage.Principal, member contractsv1.ContextFabricStructureNeedKind, sawCandidate, offered, revoked bool) {
	(&Engine{telemetry: telemetry}).recordPriorConsultOutcome(ctx, principal, member, sawCandidate, offered, revoked)
}

// RecordPriorDegradationForTest runs the engine's producer of the prior degradation lines.
func RecordPriorDegradationForTest(ctx context.Context, telemetry EngineTelemetry, principal storage.Principal, state PriorDegradationState) {
	(&Engine{telemetry: telemetry}).recordPriorDegradation(ctx, principal, state)
}

// ProjectedRowsOfClaimsForTest runs the row attachment the synthesizer runs over a claim of every kind in kinds and returns its count, per kind counts and truncation.
func ProjectedRowsOfClaimsForTest(kinds []string) (int, map[FactKind]int, bool) {
	claims := make([]ClaimedFact, 0, len(kinds))
	for _, kind := range kinds {
		claims = append(claims, ClaimedFact{Kind: FactKind(kind)})
	}
	_, rows, byKind, truncated := attachCanonicalRows(claims, nil)
	return rows, byKind, truncated
}
