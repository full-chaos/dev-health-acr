package contextfabric

import (
	"context"
	"errors"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The acceptance for outcome-driven assembly, at the request shape that
// refuses today: a SINGLE SUBJECT, no cohort, a single-table time_series
// read, measured over the item ceiling.
//
// The mechanism this pins is narrow and deterministic. Stage 3's only
// narrowing lever is the cohort (narrowSynthesisInput returns Narrow:false
// when graph.Cohort is nil), so a single-subject question reaches the
// terminal with its entire narrowing repertoire empty -- no content reduction
// is ever attempted -- while the resolution candidate list, which IS charged
// against the item budget, sits untouched. Decision D5's own header records
// that candidates are the term re-synthesis cannot reach; this is that
// paragraph happening to a real question.
//
// RED at the fix parent: Investigate returns ErrAnswerExceedsBudget and the
// route serves 413. GREEN here: the answer is served, the reduction is a
// NAMED outcome row, and the served completeness says the answer is partial.

// outcomeAssemblyCandidates builds n resolution candidates for a single
// named subject. They are alternatives the resolver did not commit to, so
// nothing in the composed answer cites them -- which is what makes them the
// one term assembly may reduce without orphaning prose.
func outcomeAssemblyCandidates(n int) []SubjectCandidate {
	candidates := make([]SubjectCandidate, 0, n)
	for index := 0; index < n; index++ {
		suffix := string(rune('a'+index/26)) + string(rune('a'+index%26))
		candidates = append(candidates, SubjectCandidate{
			ReceiptID: "receipt_cand_" + suffix,
			Subject: SubjectRef{
				Kind: SubjectTeam, CanonicalID: "org:linear:TEAM" + suffix, Label: "Team " + suffix,
			},
			State:        ResolutionProposed,
			MatchReasons: []string{"Name matched the requested subject term."},
			Confidence:   0.5,
		})
	}
	return candidates
}

// outcomeAssemblySingleSubjectEngine reproduces the 4754 shape: one committed
// team, NO cohort, `candidates` unresolved alternatives, and `facts`
// single-table time_series claimed facts about that one team.
func outcomeAssemblySingleSubjectEngine(t *testing.T, candidates, facts int, options EngineOptions, calls *int, telemetry *recordingTelemetry) *Engine {
	t.Helper()
	team := SubjectRef{Kind: SubjectTeam, CanonicalID: "org:linear:CHAOS", Label: "CHAOS"}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return InterpretedQuestion{
				Shape: ShapeOpen, RequestedJudgment: "status",
				TimeContext:      TimeContext{Axis: TemporalCurrent},
				FactRequirements: []FactRequirement{{Kind: FactStatus}},
			}, nil
		}),
		Graph: &capturingGraphReader{
			resolution: SubjectResolution{
				Candidates: outcomeAssemblyCandidates(candidates),
				Committed:  []SubjectRef{team},
			},
			context: GraphContext{
				// Cohort is nil. This is the whole point: a single-subject
				// question has nothing for the cohort lever to act on.
				Cohort: nil,
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
		Synthesizer: synthesizerFunc(func(_ context.Context, _ storage.Principal, _ SynthesisInput) (InvestigationResult, error) {
			*calls++
			claims := make([]ClaimedFact, 0, facts)
			for index := 0; index < facts; index++ {
				claims = append(claims, ClaimedFact{
					ClaimID: "claim_workload_" + string(rune('a'+index%26)),
					Kind:    FactStatus, Subject: team, Field: "status",
					Value: ScalarValue{String: ptrString("green")},
				})
			}
			return InvestigationResult{
				Status: InvestigationComplete, DirectJudgment: "Throughput held roughly flat.",
				CurrentState:       "Within the band observed over the window.",
				StrongestPressures: []string{}, Drivers: []DriverJudgment{}, RemainingWork: []Finding{},
				ReadinessGaps: []Finding{}, Paths: []RelationshipPath{}, Conflicts: []Finding{},
				Limitations: []string{}, EvidenceRefIDs: []string{}, ClaimedFacts: claims,
				Coverage:            Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				DeterministicAnswer: "Throughput held roughly flat over the requested window.",
				Warnings:            []string{},
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

func TestSingleSubjectOverBudgetIsNarrowedAndDisclosedNotRefused(t *testing.T) {
	t.Parallel()
	calls := 0
	telemetry := &recordingTelemetry{}
	// 23 candidates + 12 claimed facts = 35 charged items against a 30-item
	// ceiling -- the measured arithmetic of the filed shape, whose own split
	// (18 candidates + 12 facts + 5 drivers) is recorded by the measurement
	// probe beside this file.
	engine := outcomeAssemblySingleSubjectEngine(t, 23, 12, budgetStageOptions(30, time.Second), &calls, telemetry)

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow())
	if errors.Is(err, ErrAnswerExceedsBudget) {
		t.Fatalf("Investigate() refused with a budget refusal; a fresh investigation must be narrowed and disclosed, never refused, against the effective budget: %v", err)
	}
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}

	measurement, err := contractsv1.MeasureContextFabricResponse(result)
	if err != nil {
		t.Fatalf("MeasureContextFabricResponse() error = %v", err)
	}
	t.Logf("served item split: candidates=%d facts=%d drivers=%d budgeted=%d bytes=%d",
		measurement.Items.Candidates, measurement.Items.ClaimedFacts, measurement.Items.Drivers,
		measurement.Items.Budgeted(), measurement.Bytes)
	if measurement.Items.Budgeted() > 30 {
		t.Fatalf("served %d budgeted items against a 30-item ceiling", measurement.Items.Budgeted())
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("the served result does not validate: %v", err)
	}

	// The reduction is DISCLOSED BY NAME, not by a count a reader has to
	// infer a class from.
	if len(result.Completeness.Outcomes) == 0 {
		t.Fatal("the answer was narrowed and carries no outcome row; a narrowing the caller is not told about is the silent truncation this seam removes")
	}
	var narrowed *contractsv1.ContextFabricPlanRequirementOutcomeRow
	for index := range result.Completeness.Outcomes {
		row := &result.Completeness.Outcomes[index]
		if row.Outcome == contractsv1.ContextFabricRequirementNarrowed {
			narrowed = row
		}
		if !contractsv1.ValidContextFabricPlanRequirementOutcome(row.Outcome) {
			t.Fatalf("outcome %q is not a vocabulary member", row.Outcome)
		}
		if !contractsv1.ValidContextFabricAnswerImpactKind(row.Impact) {
			t.Fatalf("impact %q is not a vocabulary member", row.Impact)
		}
	}
	if narrowed == nil {
		t.Fatal("no outcome row reports a narrowing, but the served answer is smaller than the assembled one")
	}
	if narrowed.Impact != contractsv1.ContextFabricAnswerImpactScope {
		t.Fatalf("Impact = %q, want scope -- fewer subjects reached the caller than the resolver found", narrowed.Impact)
	}
	if narrowed.CauseOverrun != contractsv1.ContextFabricBudgetOverrunItems {
		t.Fatalf("CauseOverrun = %q, want items", narrowed.CauseOverrun)
	}
	if narrowed.Stage != contractsv1.ContextFabricPlanNarrowingAssembledResult {
		t.Fatalf("Stage = %q, want assembled_result", narrowed.Stage)
	}
	if narrowed.Declared != 23 || narrowed.Served >= narrowed.Declared {
		t.Fatalf("served/declared = %d/%d, want a real reduction out of the 23 candidates the resolver found", narrowed.Served, narrowed.Declared)
	}
	if !narrowed.CauseObserved {
		t.Fatal("CauseObserved is false on a cause this stage itself computed; a defaulted cause must never read as an observed one")
	}

	// Completeness is DERIVED from the outcome set, at the surface that
	// serves the answer -- never copied from a census taken before the
	// document was cut.
	if result.Completeness.State != contractsv1.ContextFabricAnswerCompletenessPartial {
		t.Fatalf("Completeness.State = %q, want partial", result.Completeness.State)
	}
	if calls != 1 {
		t.Fatalf("synthesizer called %d times; narrowing the candidate list must not re-run synthesis", calls)
	}
}
