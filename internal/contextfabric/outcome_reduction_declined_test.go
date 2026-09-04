package contextfabric

import (
	"context"
	"errors"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The outcome layer's candidate reduction must never be published as a
// narrowing that helped when it did not, and a refusal it could not prevent
// must say WHY it could not.
//
// This file closes the one survivor of the mutation battery: with the
// `allowance >= declared` guard deleted, nothing failed. Reading that mutant
// carefully produced the real finding, which is NOT the guard.
//
//   1. The guard is UNREACHABLE from both live call sites. Each pairs an
//      `overrun` with the measurement it was derived from, and
//      `overrun == items` means that measurement's own `Budgeted() >
//      MaxItems`, which forces `allowance = MaxItems - (Budgeted - declared)
//      < declared` by arithmetic. No behavioural test can kill that mutant,
//      because no live input reaches the line. It is covered here DIRECTLY
//      instead, and its unreachability is stated rather than left as a
//      coverage gap somebody rediscovers.
//
//   2. The defect the mutant was pointing at is one boundary up. Every run
//      where the reduction did NOT save the answer emitted
//      `outcome_narrowed_instead_of_refused=false` -- the same value emitted
//      when the lever never applied at all. "The overrun was on the byte
//      axis", "there were no candidates to cut" and "the cut ran and the
//      document still did not fit" were one undifferentiated absence, with
//      three different fixes. That is the branch-reaches-a-default-in-silence
//      shape AGENTS.md forbids, and it was introduced by this seam.

// TestAnOverrunOnATermOtherThanCandidatesIsRefusedWithTheRealCauseNamed is
// the live-path half. The candidate list is not what pushed these answers
// over, so the refusal stands -- and it names which of the reasons it was.
func TestAnOverrunOnATermOtherThanCandidatesIsRefusedWithTheRealCauseNamed(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name       string
		candidates int
		facts      int
		options    EngineOptions
		want       OutcomeReductionDeclined
	}{
		{
			// 3 candidates + 40 claimed facts = 43 items against 30. Cutting
			// every candidate still leaves 40. The lever was PULLED and was
			// not enough, which is a different fact from it never applying.
			name: "the cut ran and was not enough", candidates: 3, facts: 40,
			options: budgetStageOptions(30, time.Second), want: OutcomeReductionInsufficient,
		},
		{
			// The resolver committed without leaving alternatives, so the one
			// collection this layer may cut is already empty.
			name: "there was nothing reducible", candidates: 0, facts: 40,
			options: budgetStageOptions(30, time.Second), want: OutcomeReductionNothingReducible,
		},
		{
			// A BYTE overrun. The reduction's arithmetic is exact on the
			// items axis and has no equivalent here, so the refusal stands by
			// design -- and the artifacts say so instead of leaving it to a
			// reader of this file.
			name: "the overrun was on the byte axis", candidates: 3, facts: 40,
			options: outcomeByteBudgetOptions(4096), want: OutcomeReductionNotItemsAxis,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			telemetry := &recordingTelemetry{}
			engine := outcomeAssemblySingleSubjectEngine(t, testCase.candidates, testCase.facts, testCase.options, &calls, telemetry)

			_, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow())
			if !errors.Is(err, ErrAnswerExceedsBudget) {
				t.Fatalf("error = %v, want a planned refusal -- this shape cannot be rescued by cutting candidates", err)
			}

			var refusal *PlanNarrowingEvent
			for index := range telemetry.planNarrowings {
				event := &telemetry.planNarrowings[index]
				if event.OutcomeNarrowedInsteadOfRefused {
					t.Fatalf("an event claims the answer was narrowed instead of refused, but the answer WAS refused; a narrowing that did not help must never be published as one")
				}
				if event.RefusalPlanned {
					refusal = event
				}
			}
			if refusal == nil {
				t.Fatal("no refusal event was recorded")
			}
			if refusal.OutcomeReductionDeclined != testCase.want {
				t.Fatalf("OutcomeReductionDeclined = %q, want %q -- an operator reading only the artifacts must be able to tell which lever failed and how", refusal.OutcomeReductionDeclined, testCase.want)
			}
			if !validOutcomeReductionDeclined(refusal.OutcomeReductionDeclined) {
				t.Fatalf("OutcomeReductionDeclined = %q is not a vocabulary member", refusal.OutcomeReductionDeclined)
			}
		})
	}
}

// outcomeByteBudgetOptions configures the BYTE ceiling and leaves the item
// ceiling off, so the only axis that can overrun is the one this layer
// declines to act on.
func outcomeByteBudgetOptions(maxBytes int64) EngineOptions {
	return EngineOptions{
		ServiceVersion: "acr-test", MaxSerializedBytes: maxBytes, SynthesisDeadlineReserve: time.Second,
		Now:         func() time.Time { return time.Unix(200, 0).UTC() },
		NewResultID: func() string { return "result_99999999" },
	}
}

// TestTheReductionRefusesToCutACandidateListTheCeilingAlreadyAdmits is the
// direct cover for the surviving mutant.
//
// It calls narrowCandidatesToBudget with a (measurement, budget) pair the
// live call sites cannot produce -- one whose allowance already admits every
// declared candidate -- because that is the ONLY way to reach the guard. A
// test that could reach it through Investigate would mean the invariant the
// unreachability rests on had been broken somewhere else.
func TestTheReductionRefusesToCutACandidateListTheCeilingAlreadyAdmits(t *testing.T) {
	t.Parallel()
	result := InvestigationResult{
		SubjectResolution: SubjectResolution{Candidates: outcomeAssemblyCandidates(4)},
	}
	budget := ResponseBudget{MaxItems: 30}
	// 10 budgeted items of which 4 are candidates: the fixed terms take 6, so
	// the ceiling admits 26 candidates and only 4 were declared.
	measurement := ResponseMeasurement{Items: contractsv1.ContextFabricResultItemCounts{Candidates: 4, ClaimedFacts: 6}}

	narrowedResult, narrowing, declined := narrowCandidatesToBudget(result, budget, measurement, contractsv1.ContextFabricBudgetOverrunItems)
	if narrowing.Narrowed {
		t.Fatal("the reduction reports a narrowing, but the ceiling already admits every declared candidate; publishing that as a narrowing states that dropping content fixed something it did not")
	}
	if declined != OutcomeReductionWouldNotReduce {
		t.Fatalf("declined = %q, want %q", declined, OutcomeReductionWouldNotReduce)
	}
	if got := len(narrowedResult.SubjectResolution.Candidates); got != 4 {
		t.Fatalf("the candidate list holds %d entries, want the 4 it was given untouched", got)
	}
	if narrowing.Served != narrowing.Declared || narrowing.Declared != 4 {
		t.Fatalf("served/declared = %d/%d, want 4/4", narrowing.Served, narrowing.Declared)
	}
	// The control the mutant needs: on an allowance that genuinely binds, the
	// same call DOES reduce. Without this the assertions above would also
	// pass against a function that never narrows anything.
	binding := ResponseMeasurement{Items: contractsv1.ContextFabricResultItemCounts{Candidates: 4, ClaimedFacts: 29}}
	reduced, reduction, declinedBinding := narrowCandidatesToBudget(result, budget, binding, contractsv1.ContextFabricBudgetOverrunItems)
	if !reduction.Narrowed || reduction.Served != 1 || reduction.Declared != 4 {
		t.Fatalf("binding allowance produced served/declared = %d/%d narrowed=%v, want 1/4 narrowed", reduction.Served, reduction.Declared, reduction.Narrowed)
	}
	if declinedBinding != OutcomeReductionNotApplicable {
		t.Fatalf("declined = %q on a reduction that served, want empty", declinedBinding)
	}
	if len(result.SubjectResolution.Candidates) != 4 {
		t.Fatalf("the CALLER's candidate list now holds %d entries; the reduction must build a new slice, never write through the one it was given", len(result.SubjectResolution.Candidates))
	}
	if len(reduced.SubjectResolution.Candidates) != 1 {
		t.Fatalf("the reduced result holds %d candidates, want 1", len(reduced.SubjectResolution.Candidates))
	}
}

// TestARetryThatIsRescuedByTheReductionPlansNoRefusal.
//
// The retry arm built and emitted its event BEFORE asking the outcome layer,
// so every investigation the reduction went on to serve with a 200 also
// published refusal_planned=true. A refusal counter that counts answers which
// were never refused is worse than no counter: it is the same
// telemetry-describes-a-different-artifact class the deferred emitters in
// this file were written to fix.
func TestARetryThatIsRescuedByTheReductionPlansNoRefusal(t *testing.T) {
	t.Parallel()
	calls := 0
	telemetry := &recordingTelemetry{}
	// 6 members x 2 claims + 6 members + 6 candidates = 24 items against 12.
	// The retry halves the cohort to 3: 6 claims + 3 members + 6 candidates
	// = 15, still over. The reduction then cuts the candidates to 3 -> 12,
	// which fits. So the retry does NOT fit and the answer is served anyway.
	engine := outcomeCohortEngineWithCandidates(t, budgetStageCohort(6), 2, 6, budgetStageOptions(12, time.Second), &calls, telemetry)

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow())
	if err != nil {
		t.Fatalf("Investigate() error = %v -- the reduction fits this answer inside the ceiling", err)
	}
	measurement, err := contractsv1.MeasureContextFabricResponse(result)
	if err != nil {
		t.Fatalf("MeasureContextFabricResponse() error = %v", err)
	}
	if measurement.Items.Budgeted() > 12 {
		t.Fatalf("served %d budgeted items against a 12-item ceiling", measurement.Items.Budgeted())
	}

	served := 0
	for index := range telemetry.planNarrowings {
		event := &telemetry.planNarrowings[index]
		if event.RefusalPlanned {
			t.Fatalf("an event plans a refusal for an investigation that was SERVED; refusal_planned must be decided after the outcome layer has been asked, not before")
		}
		if event.OutcomeNarrowedInsteadOfRefused {
			served++
			if event.OutcomeReductionDeclined != OutcomeReductionNotApplicable {
				t.Fatalf("a served narrowing names a decline reason %q; the reason field belongs to the runs that did NOT serve", event.OutcomeReductionDeclined)
			}
			if event.OutcomeItemsServed >= event.OutcomeItemsDeclared {
				t.Fatalf("served/declared = %d/%d is not a reduction", event.OutcomeItemsServed, event.OutcomeItemsDeclared)
			}
		}
	}
	if served != 1 {
		t.Fatalf("%d events report a narrowing instead of a refusal, want exactly 1", served)
	}

	// ONE stage-3 decision event per investigation, which is this file's own
	// stated contract: fitAssembledResult "returns the held telemetry
	// belonging to the result it ACTUALLY SERVES, so the caller emits each
	// per-investigation decision event exactly once."
	//
	// Counting only the narrowing events missed the defect: the retry arm
	// emitted its own assembled_result event AND then the reduction emitted
	// a second one, so a single investigation was counted twice in stage-3
	// telemetry. Every narrowing-rate denominator built on that counter was
	// wrong for exactly the runs this seam rescues -- and a test that counts
	// only the events it expects can never see an extra one.
	assembled := 0
	for _, event := range telemetry.planNarrowings {
		if event.Stage == contractsv1.ContextFabricPlanNarrowingAssembledResult {
			assembled++
		}
	}
	if assembled != 1 {
		t.Fatalf("%d assembled_result events for ONE investigation, want exactly 1 -- the served answer is right but the run is double-counted in stage-3 telemetry", assembled)
	}
	if calls != 2 {
		t.Fatalf("synthesizer called %d times, want 2 -- one bounded retry, and the candidate cut must not re-run synthesis", calls)
	}
}

// TestEveryOutcomeReductionDeclinedTokenIsProducedOrDeclaredUnreachable.
//
// The same rule this seam already applies to the outcome vocabulary: a
// member no producer can reach is a promise, not a member. Both unreachable
// tokens here are unreachable for a STATED reason, so the day one becomes
// reachable this line fails and a person decides.
func TestEveryOutcomeReductionDeclinedTokenIsProducedOrDeclaredUnreachable(t *testing.T) {
	t.Parallel()
	unreachable := map[OutcomeReductionDeclined]string{
		OutcomeReductionWouldNotReduce: "both live call sites pair an overrun with the measurement it came from, which forces allowance < declared; kept as a total-function guard and covered directly",
		OutcomeReductionUnmeasurable:   "the document was marshaled once already to measure the overrun that got us here, and appending outcome rows cannot make it unmarshalable",
		OutcomeReductionNoItemBudget:   "fitAssembledResult returns early when neither ceiling is configured, and an items overrun cannot be reported against an absent item ceiling",
	}
	produced := map[OutcomeReductionDeclined]bool{
		// The three the live path produces, each pinned by a case in
		// TestAnOverrunOnATermOtherThanCandidatesIsRefusedWithTheRealCauseNamed,
		// plus the empty token every served answer carries.
		OutcomeReductionNotApplicable:    true,
		OutcomeReductionInsufficient:     true,
		OutcomeReductionNothingReducible: true,
		OutcomeReductionNotItemsAxis:     true,
	}
	vocabulary := OutcomeReductionDeclinedVocabulary()
	if len(vocabulary) != len(produced)+len(unreachable) {
		t.Fatalf("the vocabulary has %d members but this test accounts for %d; a token was added without being produced or declared unreachable", len(vocabulary), len(produced)+len(unreachable))
	}
	for _, token := range vocabulary {
		reason, declared := unreachable[token]
		switch {
		case produced[token] && declared:
			t.Errorf("token %q is declared unreachable (%q) but is produced; remove the declaration", token, reason)
		case !produced[token] && !declared:
			t.Errorf("token %q is neither produced nor declared unreachable", token)
		}
	}
}

// validOutcomeReductionDeclined is membership in the closed vocabulary,
// asserted rather than assumed: non-emptiness is what a free-text field
// would also satisfy.
func validOutcomeReductionDeclined(value OutcomeReductionDeclined) bool {
	for _, member := range OutcomeReductionDeclinedVocabulary() {
		if member == value {
			return true
		}
	}
	return false
}

// outcomeCohortEngineWithCandidates is budgetStageEngine with a resolution
// candidate list.
//
// It is a separate fixture rather than a parameter on budgetStageEngine
// because the two exercise different arms and the shared helper's callers all
// assert against a zero candidate count; threading an optional list through it
// would let a future edit silently change what those tests measure.
func outcomeCohortEngineWithCandidates(t *testing.T, cohort *Cohort, claimsPerMember, candidates int, options EngineOptions, calls *int, telemetry *recordingTelemetry) *Engine {
	t.Helper()
	graphCohort := cohort
	engine, err := NewEngine(EngineDependencies{
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return InterpretedQuestion{
				Shape: ShapeDiscoveredCohort, RequestedJudgment: "status",
				TimeContext:      TimeContext{Axis: TemporalCurrent},
				FactRequirements: []FactRequirement{{Kind: FactStatus}},
			}, nil
		}),
		Graph: &capturingGraphReader{
			resolution: SubjectResolution{Candidates: outcomeAssemblyCandidates(candidates), Committed: []SubjectRef{}},
			context: GraphContext{
				Cohort: graphCohort,
				Paths:  []RelationshipPath{}, DriverCandidates: []DriverJudgment{},
				FactRequirements: []FactRequirement{}, EvidenceRefIDs: []string{},
				Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
			},
		},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{
				Facts: []CanonicalFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{},
			}, nil
		}),
		Synthesizer: synthesizerFunc(func(_ context.Context, _ storage.Principal, input SynthesisInput) (InvestigationResult, error) {
			*calls++
			claims := []ClaimedFact{}
			if input.Graph.Cohort != nil {
				for _, member := range input.Graph.Cohort.Members {
					for claim := 0; claim < claimsPerMember; claim++ {
						claims = append(claims, ClaimedFact{
							ClaimID: "claim_" + member.Subject.CanonicalID + "_" + string(rune('0'+claim)),
							Kind:    FactStatus, Subject: member.Subject, Field: "status",
							Value: ScalarValue{String: ptrString("green")},
						})
					}
				}
			}
			return InvestigationResult{
				Status: InvestigationComplete, DirectJudgment: "Fine.", CurrentState: "Nominal.",
				StrongestPressures: []string{}, Drivers: []DriverJudgment{}, RemainingWork: []Finding{},
				ReadinessGaps: []Finding{}, Paths: []RelationshipPath{}, Conflicts: []Finding{},
				Limitations: []string{}, EvidenceRefIDs: []string{}, ClaimedFacts: claims,
				Coverage:            Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				DeterministicAnswer: "Fine, based on available context.", Warnings: []string{},
				Versions: VersionSet{
					Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
					InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1",
				},
			}, nil
		}),
		Telemetry: telemetry,
	}, options)
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	return engine
}
