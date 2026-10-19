package contextfabric

import (
	"context"
	"errors"
	"testing"
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

// SynthesizeWithTelemetryForTest runs the real synthesizer over the closure
// fixture, with the draft edited by variant, and writes its telemetry to sink.
// Variants: "clean", "collision" (two drivers share an id), "stripped" (a claim
// carries a model authored table), "source_state" (a driver claims a source
// state its grounded fact does not have).
func SynthesizeWithTelemetryForTest(ctx context.Context, sink EngineTelemetry, variant string) error {
	input, draft := closureFixture()
	switch variant {
	case "collision":
		second := draft.Drivers[0]
		second.Standing = DriverContributing
		second.Category = string(contractsv1.ContextFabricDriverCategoryNarrative)
		second.ClaimedFactIDs = nil
		second.Title = "A second, different contribution"
		draft.Drivers = append(draft.Drivers, second)
	case "stripped":
		draft.ClaimedFacts[0].Rows = []ClaimedFactRow{{}}
	case "source_state":
		draft.Drivers[0].SourceState = SourceTruncated
	case "clean":
	default:
		panic("unknown variant " + variant)
	}
	synthesizer := RuntimeAnswerSynthesizer{
		Runtime:   fakeModelRuntime{draft: draft, receipt: validModelReceiptFixture(ModelOperationSynthesize)},
		Sink:      &fakeReceiptSink{},
		Telemetry: sink,
	}
	_, err := synthesizer.Synthesize(ctx, storage.Principal{OrgID: "org_1"}, input)
	return err
}

// DualTableCountsForTest runs the row attachment the synthesizer runs over a
// claim on a fact that carries a legacy table and a time series table, and
// returns the dual table claims and secondary bytes it reports.
func DualTableCountsForTest() (claims, bytes int) {
	subject := SubjectRef{Kind: SubjectProject, CanonicalID: "project:1", Label: "p"}
	fact := dualTableProjectWorkloadFact(subject)
	claim := ClaimedFact{ClaimID: "claim_backlog", Kind: FactWorkload, Subject: subject, Field: "backlog_size", Value: ScalarValue{}}
	attachCanonicalRowsWithDualTableTelemetry([]ClaimedFact{claim}, []CanonicalFact{fact}, &claims, &bytes)
	return claims, bytes
}

// CoverageCapAndLabelsForTest runs the coverage entry cap and the display
// label stamp the engine runs on a served result that carries more coverage
// entries than the write bound and an evidence ref no label is known for, and
// returns the served entries, the omitted entries and the label fallbacks.
func CoverageCapAndLabelsForTest() (served, omitted, fallbacks int) {
	var result InvestigationResult
	for i := 0; i < contractsv1.ContextFabricCoverageEntriesMaxCount+5; i++ {
		result.Coverage.Details = append(result.Coverage.Details, CoverageDetail{DetailID: "cov-x", Source: "canonical_fact:readiness", Code: contractsv1.ContextFabricCoverageDetailFactPruned})
	}
	result.EvidenceRefIDs = []string{"evidence_without_a_known_label_0001"}
	omitted = capCoverageEntriesToWriteBound(&result)
	fallbacks = applyCoverageDisplayLabels(&result)
	return len(result.Coverage.Details), omitted, fallbacks
}

// CoverageDisclosureVerdict is what the disclosure guard decides for one set.
type CoverageDisclosureVerdict struct {
	Outcome   CoverageDisclosureOutcome
	Violation CoverageDisclosureViolation
	Phrased   int
	Total     int
}

// CoverageDisclosureVerdictsForTest runs the guard and the phrased count the
// synthesizer runs, over a result with two coverage details, for a set of
// every shape the guard distinguishes.
func CoverageDisclosureVerdictsForTest() []CoverageDisclosureVerdict {
	sets := map[string]struct {
		disclosures []CoverageDisclosure
		undecodable bool
		shown       map[string]struct{}
	}{
		"absent":      {},
		"undecodable": {undecodable: true},
		"phrased":     {disclosures: []CoverageDisclosure{{DetailID: "cov-01", Text: "No data."}, {DetailID: "cov-02", Text: "Not read."}}},
		"partial":     {disclosures: []CoverageDisclosure{{DetailID: "cov-01", Text: "No data."}}},
		"unknown":     {disclosures: []CoverageDisclosure{{DetailID: "cov-99", Text: "x"}}},
		"unshown":     {disclosures: []CoverageDisclosure{{DetailID: "cov-02", Text: "x"}}, shown: map[string]struct{}{"cov-01": {}}},
		"duplicate":   {disclosures: []CoverageDisclosure{{DetailID: "cov-01", Text: "x"}, {DetailID: "cov-01", Text: "y"}}},
		"text bound":  {disclosures: []CoverageDisclosure{{DetailID: "cov-01", Text: " padded "}}},
		"digits":      {disclosures: []CoverageDisclosure{{DetailID: "cov-01", Text: "7 gaps"}}},
	}
	var verdicts []CoverageDisclosureVerdict
	for _, set := range sets {
		var result InvestigationResult
		result.Coverage.Details = []CoverageDetail{
			{DetailID: "cov-01", Source: "canonical_fact:readiness", Code: contractsv1.ContextFabricCoverageDetailFactPruned},
			{DetailID: "cov-02", Source: "canonical_fact:status", Code: contractsv1.ContextFabricCoverageDetailFactPruned},
		}
		shown := set.shown
		if shown == nil {
			shown = coverageDetailIDSet(result.Coverage.Details)
		}
		draft := SynthesisDraft{CoverageDisclosures: set.disclosures, CoverageDisclosuresUndecodable: set.undecodable}
		outcome, violation := classifyCoverageDisclosures(draft, &result, shown)
		phrased, total := coverageDisclosurePhrasedCount(result.Coverage.Details)
		verdicts = append(verdicts, CoverageDisclosureVerdict{outcome, violation, phrased, total})
	}
	return verdicts
}

// E6StatusCategoryCompositionForTest runs the composition the engine runs over a
// bare status requirement and subjects of every kind, writing its lines to sink.
func E6StatusCategoryCompositionForTest(ctx context.Context, sink EngineTelemetry) {
	engine := &Engine{telemetry: sink}
	subjects := []SubjectRef{
		{Kind: SubjectRepository, CanonicalID: "repository:1"}, {Kind: SubjectTeam, CanonicalID: "team:1"},
		{Kind: SubjectProject, CanonicalID: "project:1"}, {Kind: SubjectWorkItem, CanonicalID: "work_item:1"},
	}
	engine.composeStatusCategoryRequirements(ctx, storage.Principal{OrgID: "org_1"}, []FactRequirement{{Kind: FactStatus}}, subjects)
}

// PeriodDeltaForTest runs the period delta comparison the engine runs for a team and a project subject under a scenario of the prior read, writing its lines to sink.
func PeriodDeltaForTest(ctx context.Context, sink EngineTelemetry, scenario string) {
	team := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:T1"}
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project:P1"}
	reader := &recordingFactReader{}
	anchored := true
	switch scenario {
	case "compared":
		reader.bundle = CanonicalFactBundle{Facts: []CanonicalFact{
			periodDeltaHealthFact(team, map[string]FactValue{"severity": StringFactValue("high"), "severity_as_of": StringFactValue("2026-08-19")}),
		}}
	case "deadline":
		reader.err = context.DeadlineExceeded
	case "canceled":
		reader.err = context.Canceled
	case "read_failed":
		reader.err = errors.New("provider failed")
	case "anchor":
		anchored = false
	default:
		panic("unknown scenario " + scenario)
	}
	engine := &Engine{facts: reader, telemetry: sink}
	current := []CanonicalFact{
		periodDeltaHealthFact(team, map[string]FactValue{"severity": StringFactValue("elevated"), "severity_as_of": StringFactValue("2026-09-18")}),
		periodDeltaHealthFact(project, map[string]FactValue{"severity": StringFactValue("low")}),
	}
	frame := QuestionFrame{Obligations: []AnswerObligation{ObligationPeriodDelta}}
	engine.applyPeriodDelta(ctx, storage.Principal{OrgID: "org_1"}, frame, []SubjectRef{team, project}, current, time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC), anchored)
}

// PlanEventsForTest is every plan narrowing, retry selection and cohort
// narrowing disclosure event the engine produced across its budget scenarios.
type PlanEventsForTest struct {
	Narrowings  []PlanNarrowingEvent
	Retries     []PlanNarrowingEvent
	Disclosures []CohortNarrowingDisclosureEvent
}

// PlanEventsFromEngineForTest drives the engine through the scenarios that
// write the plan lines: an answer that fits, an answer narrowed by one retry,
// refusals (no reserve, nothing to narrow, a retry that still does not fit)
// and a cohort cut under the byte ceiling, and returns the events it recorded.
func PlanEventsFromEngineForTest(t *testing.T) PlanEventsForTest {
	t.Helper()
	var out PlanEventsForTest
	collect := func(rec *recordingTelemetry) {
		out.Narrowings = append(out.Narrowings, rec.planNarrowings...)
		out.Retries = append(out.Retries, rec.synthesisRetrySelections...)
		out.Disclosures = append(out.Disclosures, rec.cohortNarrowingDisclosures...)
	}
	run := func(cohort *Cohort, claims int, options EngineOptions) {
		calls := 0
		rec := &recordingTelemetry{}
		engine := budgetStageEngine(t, cohort, claims, options, &calls, rec)
		_, _ = engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow())
		collect(rec)
	}
	run(budgetStageCohort(2), 1, budgetStageOptions(40, time.Second))
	run(budgetStageCohort(6), 2, budgetStageOptions(12, time.Second))
	run(budgetStageCohort(4), 20, budgetStageOptions(4, 0))
	run(budgetStageCohort(1), 20, budgetStageOptions(4, time.Second))
	run(budgetStageCohort(4), 20, budgetStageOptions(4, time.Second))
	rec := &recordingTelemetry{}
	_, _, _ = chaos6561InvestigateWithin(t, 11, 0, chaos6561HeadroomItems(t, 10), 0, rec)
	collect(rec)
	return out
}

// GroupedCohortEventsForTest drives the engine through a grouped question
// whose plan agrees with its facts, whose plan disagrees with them, and whose
// facts name no group axis, and returns the grouped cohort completeness events
// it recorded.
func GroupedCohortEventsForTest(t *testing.T) []GroupedCohortCompletenessEvent {
	t.Helper()
	var events []GroupedCohortCompletenessEvent
	for _, run := range []struct {
		kind  SubjectKind
		facts []CanonicalFact
	}{
		{SubjectTeam, []CanonicalFact{teamScopedFact("project_a", "team_security", "Security"), teamScopedFact("project_b", "team_security", "Security")}},
		{SubjectRepository, []CanonicalFact{teamScopedFact("project_a", "team_security", "Security"), teamScopedFact("project_b", "team_security", "Security")}},
		{SubjectTeam, []CanonicalFact{ungroupableFact("project_a"), ungroupableFact("project_b")}},
	} {
		rec := &recordingTelemetry{}
		engine, request := groupingEngineFixture(t, rec, run.kind, run.facts)
		if _, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request); err != nil {
			t.Fatalf("Investigate() error = %v", err)
		}
		events = append(events, rec.groupedCohortCompletenesses...)
	}
	return events
}

// RequirementLineEventsForTest is every read requirement population,
// observation cover, membership cardinality, count population scope, budget
// assertion and item accounting event the engine produced across its counting,
// scope and budget scenarios.
type RequirementLineEventsForTest struct {
	Populations   []ReadRequirementPopulationEvent
	Covers        []ReadRequirementObservationCoverEvent
	Cardinalities []MembershipCardinalityEvent
	CountScopes   []CountPopulationScopeEvent
	Assertions    []BudgetAssertionEvent
	Accountings   []ItemAccountingEvent
}

// RequirementLineEventsFromEngineForTest drives the engine through counting
// investigations (an exact count, a narrowed count, a ceiling), every count
// population scope cell, and the budget stage scenarios, and returns the events
// it recorded.
func RequirementLineEventsFromEngineForTest(t *testing.T) RequirementLineEventsForTest {
	t.Helper()
	var out RequirementLineEventsForTest
	collect := func(rec *recordingTelemetry) {
		out.Populations = append(out.Populations, rec.readRequirementPopulations...)
		out.Covers = append(out.Covers, rec.readRequirementObservationCovers...)
		out.Cardinalities = append(out.Cardinalities, rec.membershipCardinalities...)
		out.CountScopes = append(out.CountScopes, rec.countPopulationScopes...)
		out.Assertions = append(out.Assertions, rec.budgetAssertions...)
		out.Accountings = append(out.Accountings, rec.itemAccountings...)
	}
	for _, run := range []struct{ cohort, ceiling int }{{3, 0}, {30, 5}, {12, 4}} {
		_, rec := runCountingInvestigation(t, run.cohort, run.ceiling)
		collect(rec)
	}
	for _, cell := range scopeCells() {
		rec := &recordingTelemetry{}
		engine := newScopeEngine(t, cell, rec)
		runScopeCell(t, context.Background(), engine)
		collect(rec)
	}
	for _, scenario := range []struct {
		cohort  *Cohort
		claims  int
		options EngineOptions
	}{
		{budgetStageCohort(2), 1, budgetStageOptions(40, time.Second)},
		{budgetStageCohort(6), 2, budgetStageOptions(12, time.Second)},
		{budgetStageCohort(4), 20, budgetStageOptions(4, 0)},
	} {
		calls := 0
		rec := &recordingTelemetry{}
		engine := budgetStageEngine(t, scenario.cohort, scenario.claims, scenario.options, &calls, rec)
		_, _ = engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow())
		collect(rec)
	}
	maxItems := chaos6561HeadroomItems(t, 10)
	claims := -1
	for c := 0; c < 50; c++ {
		if 10*(1+c) > maxItems && 5*(1+c) <= maxItems {
			claims = c
			break
		}
	}
	for _, run := range []struct{ population, claims, maxItems int }{{11, claims, maxItems}, {11, 0, maxItems}, {3, 0, maxItems}} {
		rec := &recordingTelemetry{}
		chaos6561Investigate(t, run.population, run.claims, run.maxItems, rec)
		collect(rec)
	}
	ledger := unreconciledLedger()
	ledger.Disagreement = string(contractsv1.ContextFabricChargedDrivers)
	for _, stage := range ItemAccountingStagesForTest() {
		out.Accountings = append(out.Accountings, itemAccountingEventFor(stage, ledger, 30))
	}
	allocation := itemAccountingEventFor("assembled_result", contractsv1.ContextFabricItemLedger{Status: contractsv1.ContextFabricLedgerReconciled}, 30)
	for _, disagreement := range AllocationDisagreementVocabulary() {
		allocation.AllocationDisagreement = disagreement
		out.Accountings = append(out.Accountings, allocation)
	}
	return out
}

// ItemAccountingStagesForTest returns the stage names the engine measures an
// attempt under: the literals passed to measureAssembledAttempt and the budget
// assertion stages.
func ItemAccountingStagesForTest() []string {
	stages := []string{
		"budget_trim", "assembled_result", "re_synthesized_result", "facts_only_result", "path_drop", "candidate_narrowing",
		"claim_depth_narrowing", "walk_list_fit", "fact_row_truncation",
	}
	for _, stage := range BudgetAssertStageVocabulary() {
		stages = append(stages, string(stage))
	}
	return stages
}
