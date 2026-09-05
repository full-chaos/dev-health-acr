package contextfabric

import (
	"context"
	"errors"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// THE WIRING between the requirement derivation and the plan's fact kinds.
//
// The unit pin beside this one (computed_step_input_reads_test.go) hands the
// rows to PlanAnswer directly, which proves the plan stage reads them and
// proves nothing about whether the ENGINE hands them over. This file drives
// the public entry point and reads the served document.
//
// It lives in its own file rather than beside the plan-requirement consumer
// tests it was first written into, because it is about a different seam and
// because two branches appending to one file's tail is a merge conflict with
// no disagreement in it.

// rankingRequirementRows is twoRequirementRows with its computed row replaced
// by a SERVED, SERVER-EXECUTED, fact-kind-consuming one.
//
// The shared fixture's computed row is `membership_cardinality`, which
// consumes the resolved member set and declares no fact kind, so it can say
// nothing about whether declared inputs reach the plan. This one declares the
// ranking formula's own kinds, which is the only shape the input consumer acts
// on.
func rankingRequirementRows() []DerivedRequirement {
	rows := twoRequirementRows()
	rows[1] = DerivedRequirement{
		RequirementCoordinate: RequirementCoordinate{
			Obligation: ObligationRanking, Role: SubjectRoleMember, Subject: SubjectTeam,
		},
		Kind:           ObligationKindComputed,
		Step:           ComputedStepRankCohort,
		InputClass:     ComputedInputFactKinds,
		InputFactKinds: append([]FactKind(nil), cohortRankingFormulaKinds...),
		StepExecution:  ComputedStepServerExecuted,
		Scope:          CompletionScopeEachMember,
		Quantifier:     CompletionQuantifierAll,
	}
	return rows
}

// TestInvestigatePlansTheComputedStepInputsTheDerivationDeclared is the WIRING
// pin, and it exists because the unit pin below the engine cannot be it.
//
// TestPlanFactKindsPlansEveryDeclaredComputedStepInput drives PlanAnswer with
// requirement rows handed to it directly. That proves the plan stage reads
// them; it proves nothing about whether the ENGINE hands them over, and
// dropping one field from the engine's PlanAnswerInput literal would leave it
// green -- constructing a struct literal in a test proves nothing about the
// production call site. So this drives Investigate and reads the fact kinds
// off the SERVED document.
//
// THE FAMILY MUST NOT BE A COHORT ONE, asserted rather than assumed. On a
// cohort family the unconditional ranking injection names the same five kinds,
// so the assertion would pass with the whole input consumer deleted. If this
// fixture's question ever resolves to a cohort family the test fails LOUDLY
// rather than continuing to look green while proving nothing.
func TestInvestigatePlansTheComputedStepInputsTheDerivationDeclared(t *testing.T) {
	t.Parallel()
	calls := 0
	telemetry := &recordingTelemetry{}
	deriver := &fixedRequirementDeriver{rows: rankingRequirementRows()}

	engine := planRequirementEngine(t, deriver, &calls, telemetry)

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow())
	if err != nil && !errors.Is(err, ErrAnswerExceedsBudget) {
		t.Fatalf("Investigate() error = %v", err)
	}
	if deriver.calls == 0 {
		t.Fatal("the engine never called the requirement deriver, so the plan's kinds cannot have come from the rows")
	}
	if result.AnswerPlan == nil {
		t.Fatal("the served answer carries no answer plan")
	}

	definition, found := LookupQuestionFamily(result.AnswerPlan.Family)
	if !found {
		t.Fatalf("the served plan names family %q, which the registry does not carry", result.AnswerPlan.Family)
	}
	if isCohortSubjectAxis(definition.SubjectAxis) {
		t.Fatalf("this fixture resolved to COHORT family %q (axis %q); the unconditional ranking injection would supply these kinds and this test could not attribute them to the requirement rows",
			result.AnswerPlan.Family, definition.SubjectAxis)
	}

	want := ComputedStepInputReads(deriver.rows)
	if len(want) == 0 {
		t.Fatal("the fixture's rows declare no computed-step input, so this test asserted nothing")
	}
	for _, kind := range want {
		found := false
		for _, planned := range result.AnswerPlan.FactKinds {
			if planned == kind {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("the served plan's fact kinds %v do not include %q, which the derivation declared as an input of a server-executed computed step", result.AnswerPlan.FactKinds, kind)
		}
	}
	t.Logf("family %s (axis %s): served plan kinds %v, declared inputs %v", result.AnswerPlan.Family, definition.SubjectAxis, result.AnswerPlan.FactKinds, want)
}

// TestInvestigatePassesComputedStepInputsToFactReader closes round 1's F1.
//
// THE GAP IT CLOSES, stated as the reviewer stated it. The sibling test above
// drives Investigate on a NON-cohort family and reads the kinds off
// `AnswerPlan.FactKinds`. That proves the plan PUBLISHES the declared input; it
// proves nothing about the plan reaching the fact READ, because engine.go turns
// `plan.FactKinds` into fact requirements only inside `if graphContext.Cohort
// != nil`. Deleting that loop left every test on this branch green.
//
// The property is load-bearing for this change specifically: before it, that
// loop was a widening nicety on top of the unconditional five-kind injection;
// now the plan's kinds are partly derived from the requirement rows, and the
// claim "a declared input IS planned" leans on them reaching the reader.
//
// WHY THE DECLARED INPUT HERE IS `metrics`, AND WHY THAT IS THE WHOLE TEST.
// The engine appends `cohortRankingFormulaKinds` FIRST and unconditionally, so
// a fixture declaring one of those five would be satisfied by the injection
// alone and would stay green with the loop deleted -- the exact vacuity that
// let the gap survive. `metrics` is outside that set, so the ONLY route from
// the declaration to the fact reader is the loop under test. The test asserts
// that separation rather than assuming it.
func TestInvestigatePassesComputedStepInputsToFactReader(t *testing.T) {
	t.Parallel()

	declared := FactMetrics
	for _, injected := range cohortRankingFormulaKinds {
		if injected == declared {
			t.Fatalf("the fixture's declared input %q is one of the unconditionally injected ranking kinds, so this test could not tell the plan loop from the injection", declared)
		}
	}

	team := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:COHORT", Label: "Cohort"}
	cohort := &Cohort{
		Kind:      SubjectTeam,
		Rationale: "fixture",
		Members:   []CohortMember{{Subject: team, Rank: 1, InclusionReasons: []string{"matched"}}},
	}
	frame := namedFrame(GoalAssessState)

	rows := []DerivedRequirement{
		{
			RequirementCoordinate: RequirementCoordinate{
				Obligation: ObligationState, Role: SubjectRoleMember, Subject: SubjectTeam,
			},
			Kind:       ObligationKindRead,
			FactKinds:  []FactKind{FactHealth},
			Scope:      CompletionScopeEachMember,
			Quantifier: CompletionQuantifierAtLeastOne,
		},
		{
			RequirementCoordinate: RequirementCoordinate{
				Obligation: ObligationRanking, Role: SubjectRoleMember, Subject: SubjectTeam,
			},
			Kind:           ObligationKindComputed,
			Step:           ComputedStepRankCohort,
			InputClass:     ComputedInputFactKinds,
			InputFactKinds: []FactKind{declared},
			StepExecution:  ComputedStepServerExecuted,
			Scope:          CompletionScopeEachMember,
			Quantifier:     CompletionQuantifierAll,
		},
	}
	deriver := &fixedRequirementDeriver{rows: rows}

	var observed map[FactKind]bool
	var reads int
	engine, err := NewEngine(EngineDependencies{
		Interpreter: familyInterpreter{
			interpreted: InterpretedQuestion{
				Shape: ShapeDiscoveredCohort, RequestedJudgment: "teams_under_pressure",
				TimeContext: TimeContext{Axis: TemporalCurrent},
				// Deliberately narrow, and deliberately NOT the declared
				// input: if the model asked for it, the model's own widening
				// would satisfy the assertion and the loop would again be
				// unpinned.
				FactRequirements: []FactRequirement{{Kind: FactHealth}},
			},
			outcome: QuestionFamilyOutcome{
				Frame:  &frame,
				Family: QuestionFamilyDiscoveredCohortRanking,
				Source: QuestionFamilySourceModel,
			},
		},
		Graph: graphReaderStub{
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
			context: GraphContext{
				Cohort: cohort, Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{},
				FactRequirements: []FactRequirement{}, EvidenceRefIDs: []string{},
				Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
			},
		},
		Facts: factReaderFunc(func(_ context.Context, _ storage.Principal, request CanonicalFactRequest) (CanonicalFactBundle, error) {
			reads++
			observed = make(map[FactKind]bool, len(request.Requirements))
			for _, requirement := range request.Requirements {
				observed[requirement.Kind] = true
			}
			return CanonicalFactBundle{
				Facts:    []CanonicalFact{{Kind: FactHealth, Subject: team, Fields: map[string]FactValue{"severity": StringFactValue("low")}}},
				Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				Version:  "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{},
			}, nil
		}),
		Synthesizer: synthesizerFunc(func(_ context.Context, _ storage.Principal, _ SynthesisInput) (InvestigationResult, error) {
			return InvestigationResult{
				Status: InvestigationComplete, DirectJudgment: "Nominal.", CurrentState: "Nominal.",
				StrongestPressures: []string{}, Drivers: []DriverJudgment{}, RemainingWork: []Finding{},
				ReadinessGaps: []Finding{}, Paths: []RelationshipPath{}, Conflicts: []Finding{},
				Limitations: []string{}, EvidenceRefIDs: []string{}, ClaimedFacts: []ClaimedFact{},
				Coverage:            Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				DeterministicAnswer: "Nominal.", Warnings: []string{},
				Versions: VersionSet{
					Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
					InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1",
				},
			}, nil
		}),
		Results:      &resultStoreStub{},
		Telemetry:    &recordingTelemetry{},
		Requirements: deriver,
	}, EngineOptions{
		ServiceVersion: "acr-test",
		Now:            func() time.Time { return time.Unix(300, 0).UTC() },
		NewResultID:    func() string { return "result_50180001" },
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}

	request := validInvestigationRequestWithConfirmedWindow()
	request.RequestID = "request_50180001"
	request.Question = "which teams are struggling?"
	if _, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org-1"}, request); err != nil && !errors.Is(err, ErrAnswerExceedsBudget) {
		t.Fatalf("Investigate() error = %v", err)
	}

	// REACH, before any assertion about content. A turn that never read facts
	// would leave `observed` nil and every membership check below would pass
	// over nothing.
	if reads == 0 || observed == nil {
		t.Fatal("the fact reader was never called, so this test asserted nothing about what it received")
	}
	if deriver.calls == 0 {
		t.Fatal("the engine never called the requirement deriver; the plan's kinds cannot have come from the rows")
	}
	if !observed[declared] {
		t.Errorf("the fact reader received requirements %v, missing %q -- the requirement rows declared it as a server-executed computed step's input and the plan published it, but nothing asked for it to be READ",
			sortedObservedKinds(observed), declared)
	}
	// The model's own kind must survive alongside it: a loop that replaced the
	// requirement set rather than widening it would satisfy the check above.
	if !observed[FactHealth] {
		t.Errorf("the fact reader received requirements %v, missing the model's own %q -- the plan may only WIDEN what is read", sortedObservedKinds(observed), FactHealth)
	}
	t.Logf("fact reader received %v", sortedObservedKinds(observed))
}

// sortedObservedKinds renders a kind set in fact-kind VOCABULARY order, so a
// failure message is stable across runs rather than map-order noise.
func sortedObservedKinds(observed map[FactKind]bool) []FactKind {
	out := make([]FactKind, 0, len(observed))
	for _, member := range contractsv1.ContextFabricFactKindVocabulary() {
		if observed[FactKind(member)] {
			out = append(out, FactKind(member))
		}
	}
	for kind := range observed {
		if !containsDerivedKind(out, kind) {
			out = append(out, kind)
		}
	}
	return out
}

func containsDerivedKind(kinds []FactKind, want FactKind) bool {
	for _, kind := range kinds {
		if kind == want {
			return true
		}
	}
	return false
}
