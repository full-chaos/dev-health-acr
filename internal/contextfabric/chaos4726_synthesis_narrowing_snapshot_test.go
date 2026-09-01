package contextfabric

import (
	"context"
	"errors"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestWithSynthesisNarrowingSnapshotCarriesTheLastPreSynthesisStep is the
// RED-on-parent proof: before withSynthesisNarrowingSnapshot existed, a
// synthesis error returned unchanged and SynthesisNarrowingSnapshotOf had no
// way to recover any narrowing state from it.
func TestWithSynthesisNarrowingSnapshotCarriesTheLastPreSynthesisStep(t *testing.T) {
	plan := AnswerPlan{
		Family:        QuestionFamilyScopedCohortStatus,
		FamilyVersion: "v1",
		Budget: AnswerPlanBudget{
			NarrowingBasis: contractsv1.ContextFabricNarrowingBasisCanonicalIDLexical,
		},
		Narrowing: []PlanNarrowing{
			{Stage: contractsv1.ContextFabricPlanNarrowingCardinality, Basis: contractsv1.ContextFabricNarrowingBasisCanonicalIDLexical, Before: 40, After: 20},
			{Stage: contractsv1.ContextFabricPlanNarrowingSynthesisInput, Basis: contractsv1.ContextFabricNarrowingBasisOverlapAwareSetCover, Before: 20, After: 10, Groups: true},
		},
	}
	cause := errors.New("synthesize investigation: rejected")

	wrapped := withSynthesisNarrowingSnapshot(cause, plan)

	snapshot, ok := SynthesisNarrowingSnapshotOf(wrapped)
	if !ok {
		t.Fatalf("SynthesisNarrowingSnapshotOf(wrapped) ok = false, want true")
	}
	if snapshot.DeclaredBasis != contractsv1.ContextFabricNarrowingBasisCanonicalIDLexical {
		t.Fatalf("DeclaredBasis = %q, want canonical_id_lexical", snapshot.DeclaredBasis)
	}
	// The LAST step is stage 2 (synthesis_input), not stage 1 -- the whole
	// point of this snapshot is "what was in effect right before synthesis
	// ran", and stage 2 ran after stage 1.
	if snapshot.LastStage != contractsv1.ContextFabricPlanNarrowingSynthesisInput {
		t.Fatalf("LastStage = %q, want synthesis_input", snapshot.LastStage)
	}
	if snapshot.LastBasis != contractsv1.ContextFabricNarrowingBasisOverlapAwareSetCover {
		t.Fatalf("LastBasis = %q, want overlap_aware_set_cover", snapshot.LastBasis)
	}
	if !errors.Is(wrapped, cause) {
		t.Fatalf("errors.Is(wrapped, cause) = false, want true -- the snapshot must wrap, not replace")
	}
}

// TestWithSynthesisNarrowingSnapshotNoStepsYieldsZeroValueStageAndBasis pins
// the "nothing narrowed before synthesis ran" case: plan.Narrowing is empty
// (stage 1's cardinality clamp never triggered and stage 2 never ran), so
// LastStage/LastBasis must be the empty string, not a guessed default.
func TestWithSynthesisNarrowingSnapshotNoStepsYieldsZeroValueStageAndBasis(t *testing.T) {
	plan := AnswerPlan{
		Budget: AnswerPlanBudget{NarrowingBasis: contractsv1.ContextFabricNarrowingBasisCanonicalIDLexical},
	}
	wrapped := withSynthesisNarrowingSnapshot(errors.New("synthesize investigation: rejected"), plan)

	snapshot, ok := SynthesisNarrowingSnapshotOf(wrapped)
	if !ok {
		t.Fatalf("SynthesisNarrowingSnapshotOf(wrapped) ok = false, want true")
	}
	if snapshot.LastStage != "" {
		t.Fatalf("LastStage = %q, want empty -- no stage acted before synthesis", snapshot.LastStage)
	}
	if snapshot.LastBasis != "" {
		t.Fatalf("LastBasis = %q, want empty -- no stage acted before synthesis", snapshot.LastBasis)
	}
	if snapshot.DeclaredBasis != contractsv1.ContextFabricNarrowingBasisCanonicalIDLexical {
		t.Fatalf("DeclaredBasis = %q, want canonical_id_lexical -- stage 1 always declares this even when it does not act", snapshot.DeclaredBasis)
	}
}

// TestWithSynthesisNarrowingSnapshotNilErrorStaysNil matches stageError's own
// nil-safety contract: wrapping must never turn a nil error into a non-nil
// one.
func TestWithSynthesisNarrowingSnapshotNilErrorStaysNil(t *testing.T) {
	if got := withSynthesisNarrowingSnapshot(nil, AnswerPlan{}); got != nil {
		t.Fatalf("withSynthesisNarrowingSnapshot(nil, ...) = %v, want nil", got)
	}
}

// TestSynthesisNarrowingSnapshotOfAbsentReportsFalse pins the "no snapshot
// attached" case distinctly from a snapshot carrying zero-value fields --
// SynthesisNarrowingSnapshotOf's own doc comment calls this out as the
// distinction the bool exists to make.
func TestSynthesisNarrowingSnapshotOfAbsentReportsFalse(t *testing.T) {
	_, ok := SynthesisNarrowingSnapshotOf(errors.New("unrelated"))
	if ok {
		t.Fatalf("SynthesisNarrowingSnapshotOf(unrelated error) ok = true, want false")
	}
}

// TestInvestigateAttachesNarrowingSnapshotOnSynthesisRejection is the
// end-to-end wiring proof: Engine.Investigate itself, with a synthesizer
// that rejects, must return an error SynthesisNarrowingSnapshotOf can read
// -- not just the standalone withSynthesisNarrowingSnapshot helper in
// isolation. A cohort of 3 against a 2-item budget forces BOTH stage 1 (the
// requested-cardinality clamp) and stage 2 (the post-read synthesis-input
// clamp, since the fixture graph reader ignores the clamped request and
// always returns all 3) to narrow before synthesis is ever invoked -- so
// the snapshot's LastStage must name stage 2, the one that ran LAST, not
// stage 1.
func TestInvestigateAttachesNarrowingSnapshotOnSynthesisRejection(t *testing.T) {
	t.Parallel()
	cohort := budgetStageCohort(3)
	engine, err := NewEngine(EngineDependencies{
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return InterpretedQuestion{
				Shape: ShapeDiscoveredCohort, RequestedJudgment: "status",
				TimeContext:      TimeContext{Axis: TemporalCurrent},
				FactRequirements: []FactRequirement{{Kind: FactStatus}},
			}, nil
		}),
		Graph: &capturingGraphReader{
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
			context: GraphContext{
				Cohort: cohort,
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
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return InvestigationResult{}, rejectSynthesis(RejectionReasonDirectJudgmentMissing, "no direct judgment")
		}),
	}, budgetStageOptions(2, 0))
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}

	_, investigateErr := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow())
	if investigateErr == nil {
		t.Fatal("Investigate() error = nil, want a synthesis rejection")
	}

	snapshot, ok := SynthesisNarrowingSnapshotOf(investigateErr)
	if !ok {
		t.Fatalf("SynthesisNarrowingSnapshotOf(Investigate error) ok = false -- engine.go's synthesizeAndAssemble error path must attach the pre-synthesis narrowing state; got %v", investigateErr)
	}
	if snapshot.LastStage != contractsv1.ContextFabricPlanNarrowingSynthesisInput {
		t.Fatalf("LastStage = %q, want synthesis_input -- stage 2 runs after stage 1 and must be the LAST recorded step", snapshot.LastStage)
	}
	if snapshot.LastBasis != contractsv1.ContextFabricNarrowingBasisCanonicalIDLexical {
		t.Fatalf("LastBasis = %q, want canonical_id_lexical", snapshot.LastBasis)
	}
	if !errors.As(investigateErr, new(*SynthesisRejection)) {
		t.Fatal("errors.As(Investigate error, *SynthesisRejection) = false -- the snapshot must wrap the rejection, not replace it")
	}
}

// TestInvestigateAttachesNarrowingSnapshotOnStage3RetrySynthesisFailure is
// codex round 1's finding on this ticket: the FIRST synthesis call's
// rejection is caught at the Investigate call site, but stage 3's retry
// (chaos4636_budget_stage3.go's fitAssembledResult) is a SECOND, separate
// synthesis invocation that can itself fail, and that path bypassed the
// snapshot entirely -- the retry's own error was returned unwrapped. This
// pins that the retry-failure path now carries a snapshot too, and that its
// LastStage is assembled_result (stage 3's own narrowing step, appended to
// plan.Narrowing immediately before the retry runs) -- not stale state left
// over from the first, failed attempt.
func TestInvestigateAttachesNarrowingSnapshotOnStage3RetrySynthesisFailure(t *testing.T) {
	t.Parallel()
	calls := 0
	// 6 members x 3 claims = 18 claims + 6 members = 24 items over a 12-item
	// budget -- the SAME fixture shape as
	// TestStage3PropagatesARetrySynthesisFailureRatherThanCallingItABudgetRefusal,
	// which pins that the retry's error survives at all; this test pins
	// that it now also carries the narrowing snapshot.
	engine := budgetStageEngine(t, budgetStageCohort(6), 3, budgetStageOptions(12, time.Second), &calls, &recordingTelemetry{})
	upstream := errors.New("model runtime unavailable")
	engine.synthesizer = failingRetrySynthesizer{calls: &calls, first: engine.synthesizer, failWith: upstream}

	_, investigateErr := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow())
	if investigateErr == nil {
		t.Fatal("Investigate() error = nil, want the retry's propagated failure")
	}
	if !errors.Is(investigateErr, upstream) {
		t.Fatalf("errors.Is(Investigate error, upstream) = false -- want %v, got %v", upstream, investigateErr)
	}

	snapshot, ok := SynthesisNarrowingSnapshotOf(investigateErr)
	if !ok {
		t.Fatalf("SynthesisNarrowingSnapshotOf(Investigate error) ok = false -- the stage-3 retry failure path must attach the pre-retry narrowing state too; got %v", investigateErr)
	}
	if snapshot.LastStage != contractsv1.ContextFabricPlanNarrowingAssembledResult {
		t.Fatalf("LastStage = %q, want assembled_result -- stage 3's own narrowing step runs immediately before the retry call", snapshot.LastStage)
	}
}
