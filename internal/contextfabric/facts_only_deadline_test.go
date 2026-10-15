package contextfabric

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// factsOnlySynth writes a model answer that overruns the item budget and
// composes the production degraded answer when asked.
type factsOnlySynth struct {
	modelCalls *int
	degraded   *int
	claims     int
}

func (s factsOnlySynth) Synthesize(ctx context.Context, principal storage.Principal, input SynthesisInput) (InvestigationResult, error) {
	*s.modelCalls++
	claims := []ClaimedFact{}
	if input.Graph.Cohort != nil {
		for _, member := range input.Graph.Cohort.Members {
			for claim := 0; claim < s.claims; claim++ {
				claims = append(claims, ClaimedFact{
					ClaimID: "claim_" + member.Subject.CanonicalID + "_" + string(rune('0'+claim)),
					Kind:    FactStatus, Subject: retryFixtureClaimSubject(member.Subject), Field: "status",
					Value: ScalarValue{String: ptrString("green")},
				})
			}
		}
	}
	return InvestigationResult{
		Status: InvestigationComplete, DirectJudgment: "Model prose.", CurrentState: "Model prose.",
		StrongestPressures: []string{}, Drivers: []DriverJudgment{}, RemainingWork: []Finding{},
		ReadinessGaps: []Finding{}, Paths: []RelationshipPath{}, Conflicts: []Finding{},
		Limitations: []string{}, EvidenceRefIDs: []string{}, ClaimedFacts: claims,
		Coverage:            Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
		DeterministicAnswer: "Model prose.", Warnings: []string{},
		Versions: VersionSet{Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1", InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1"},
	}, nil
}

func (s factsOnlySynth) ComposeDegraded(ctx context.Context, principal storage.Principal, input SynthesisInput, failure *SynthesisFailure) (InvestigationResult, error) {
	*s.degraded++
	return RuntimeAnswerSynthesizer{}.ComposeDegraded(ctx, principal, input, failure)
}

func factsOnlyEngine(t *testing.T, cohort *Cohort, options EngineOptions, modelCalls, degraded *int, claims int, telemetry *recordingTelemetry) *Engine {
	t.Helper()
	engine, err := NewEngine(EngineDependencies{
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return InterpretedQuestion{Shape: ShapeDiscoveredCohort, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: []FactRequirement{{Kind: FactStatus}}}, nil
		}),
		Graph: &capturingGraphReader{
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
			context: GraphContext{Cohort: cohort, Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{},
				FactRequirements: []FactRequirement{}, EvidenceRefIDs: []string{},
				Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}},
		},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			facts := []CanonicalFact{}
			for index, member := range cohort.Members {
				if index >= 2 {
					break
				}
				facts = append(facts, CanonicalFact{Kind: FactStatus, Subject: member.Subject, Fields: map[string]FactValue{"status": StringFactValue("green")},
					EvidenceRefIDs: []string{"ref_" + member.Subject.CanonicalID}, Source: "devhealthfacts.status", SourceVersion: "v1"})
			}
			return CanonicalFactBundle{Facts: facts, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}, Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{}}, nil
		}),
		Synthesizer: factsOnlySynth{modelCalls: modelCalls, degraded: degraded, claims: claims},
		Telemetry:   telemetry,
	}, options)
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	return engine
}

func TestAModelAnswerOverBudgetWithNoRoomToRetryIsServedAsFactsOnly(t *testing.T) {
	t.Parallel()
	const reserve = 20 * time.Second
	cases := []struct {
		name      string
		cohort    *Cohort
		items     int
		remaining time.Duration
		reserve   time.Duration
		claims    int
		declined  RetryDeclinedReason
		class     SynthesisFailureClass
		limit     string
	}{
		{"insufficient deadline", budgetStageCohort(6), 12, 10 * time.Second, reserve, 2, RetryDeclinedInsufficientDeadline, contractsv1.ContextFabricSynthesisFailureDeadline, contractsv1.ContextFabricSynthesisDeadlineLimitation},
		{"nothing to narrow", budgetStageCohort(1), 4, time.Minute, time.Second, 20, RetryDeclinedNothingToNarrow, contractsv1.ContextFabricSynthesisFailureNotNarrowable, contractsv1.ContextFabricSynthesisNotNarrowableLimitation},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var modelCalls, degraded int
			telemetry := &recordingTelemetry{}
			engine := factsOnlyEngine(t, tc.cohort, budgetStageOptions(tc.items, tc.reserve), &modelCalls, &degraded, tc.claims, telemetry)
			ctx, cancel := context.WithTimeout(context.Background(), tc.remaining)
			defer cancel()

			result, err := engine.Investigate(ctx, storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow())

			if err != nil {
				t.Fatalf("error = %v, want the facts-only answer served", err)
			}
			if modelCalls != 1 || degraded != 1 {
				t.Fatalf("model calls = %d, degraded compositions = %d, want the one model pass and one composition", modelCalls, degraded)
			}
			if result.Status != InvestigationDegraded || !slices.Contains(result.Limitations, tc.limit) {
				t.Fatalf("status = %q limitations = %q, want degraded and facts only", result.Status, result.Limitations)
			}
			if !slices.Contains(result.Warnings, synthesisFailureWarning(tc.class)) {
				t.Fatalf("warnings = %q, want the %s warning", result.Warnings, tc.class)
			}
			classes := []SynthesisFailureClass{}
			for _, detail := range result.Coverage.Details {
				if detail.Code == contractsv1.ContextFabricCoverageDetailSynthesisNotRun {
					classes = append(classes, detail.FailureClass)
				}
			}
			for _, detail := range result.Coverage.Details {
				if detail.Code == contractsv1.ContextFabricCoverageDetailSynthesisModelFailed {
					t.Fatalf("a facts-only answer filed %+v, which says a model call failed", detail)
				}
			}
			if !slices.Equal(classes, []SynthesisFailureClass{tc.class}) {
				t.Fatalf("coverage failure classes = %q, want [%s]", classes, tc.class)
			}
			if result.DirectJudgment == "Model prose." || result.DeterministicAnswer == "Model prose." {
				t.Fatalf("served model prose: %q / %q", result.DirectJudgment, result.DeterministicAnswer)
			}
			var served *PlanNarrowingEvent
			for index := range telemetry.planNarrowings {
				if telemetry.planNarrowings[index].FactsOnlyServed {
					served = &telemetry.planNarrowings[index]
				}
				if telemetry.planNarrowings[index].RefusalPlanned {
					t.Fatalf("a refusal was planned for a served answer: %+v", telemetry.planNarrowings[index])
				}
			}
			if served == nil || served.RetryDeclined != tc.declined {
				t.Fatalf("served event = %+v, want facts_only_served with retry_declined %q", served, tc.declined)
			}
		})
	}
}

func TestAFactsOnlyAnswerThatDoesNotFitLeavesTheRefusal(t *testing.T) {
	t.Parallel()
	var modelCalls, degraded int
	telemetry := &recordingTelemetry{}
	// The byte ceiling is under what any composition of six members weighs.
	options := budgetStageOptions(12, 20*time.Second)
	options.MaxSerializedBytes = 3000
	engine := factsOnlyEngine(t, budgetStageCohort(6), options, &modelCalls, &degraded, 2, telemetry)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := engine.Investigate(ctx, storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow())

	if !errors.Is(err, ErrAnswerExceedsBudget) {
		t.Fatalf("error = %v, want the planned refusal", err)
	}
	var refusal *PlanNarrowingEvent
	for index := range telemetry.planNarrowings {
		if telemetry.planNarrowings[index].RefusalPlanned {
			refusal = &telemetry.planNarrowings[index]
		}
	}
	if refusal == nil || !refusal.FactsOnlyAttempted || refusal.FactsOnlyServed || refusal.RetryDeclined != RetryDeclinedInsufficientDeadline {
		t.Fatalf("refusal event = %+v, want facts_only_attempted, not served, insufficient_deadline", refusal)
	}
}
