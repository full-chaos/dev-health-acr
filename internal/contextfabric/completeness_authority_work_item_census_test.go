package contextfabric

// The outcome-derivation completeness authority is applied exactly once,
// inside finalizeServed -- see engine.go's fresh-dispatch tupleCensus branch
// for the invariant this depends on. This file pins the observable
// consequence: the certified completeness authority telemetry line must
// report the ORIGINAL model/server disagreement, never the served status
// after any correction has already applied -- on the fresh work-item census
// path exactly as it already does on the reuse-hit and by-id read paths.

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestWorkItemCensusFreshDispatchTelemetryReportsTheOriginalDisagreement
// exercises the real work-item tuple census dispatch (the shape
// TestWorkItemFreshDispatchMeasuredAndUnmeasured's "members" case builds):
// the fixture's own frame naturally derives two planning-only unavailable
// requirements (health, state) that DeriveCompletenessAuthority reads as
// degraded, so a model-claimed partial genuinely disagrees with the server.
// With the symmetric flag on, the served status is corrected to degraded --
// and the certified telemetry line must still name the ORIGINAL partial
// claim, not the corrected one.
func TestWorkItemCensusFreshDispatchTelemetryReportsTheOriginalDisagreement(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	frame := ValidateFrame(prospectiveTupleFrame(GoalAssessState, GoalCountOrAggregate), nil, "").Frame
	payload := workItemTuplePayloadFixture(t)
	graph := &dispatchGraphProbe{graphReaderStub: graphReaderStub{resolution: payload.SubjectResolution, bases: provenCommitBases(payload.SubjectResolution.Committed...)}}
	gate, _ := NewWorkItemMembershipGate(1, 0)
	store := &staticResultStore{results: map[string]InvestigationResult{}}
	telemetry := &recordingTelemetry{}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: familyInterpreter{interpreted: InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: []FactRequirement{{Kind: FactHealth}}}, outcome: QuestionFamilyOutcome{Family: QuestionFamilyScopedCohortStatus, Source: QuestionFamilySourceModel, Frame: &frame, FrameObligations: frame.Obligations, Gate: workItemTupleFrameGate(DecideFrameGate(ValidateFrame(frame, nil, ""), true), &frame, workItemTupleFamilyPolicyForTest(QuestionFamilyScopedCohortStatus), TimeContext{Axis: TemporalCurrent}), WinningSample: FamilySample{ScopeAnchorKind: SubjectProject, ScopeAnchorTerm: "Project"}}},
		Graph:       graph,
		CandidateVerifier: func(context.Context, storage.Principal, RequestedScope, ResolvedGraphBinding, SubjectKind, string) (bool, CandidateVerificationReason) {
			return true, ""
		},
		WorkItemMembership: tupleMembershipFunc(func(ctx context.Context, _ storage.Principal, _ WorkItemMembershipRequest) (*WorkItemMembershipLease, WorkItemMembershipResult, error) {
			lease, err := gate.Acquire(ctx)
			m := WorkItemMembershipResult{
				Census:  WorkItemMembershipCensus{State: WorkItemMembershipCensusExact, PopulationMeasured: true, AuthorizedPopulation: 1},
				Members: []WorkItemMembershipMember{{CanonicalID: payload.Cohort.Members[0].Subject.CanonicalID, WorkItemID: "work-1"}},
			}
			return lease, m, err
		}),
		Facts: factReaderFunc(func(_ context.Context, _ storage.Principal, r CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{Facts: []CanonicalFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}, Version: "ops-v1"}, nil
		}),
		// Model claims partial. This fixture's frame declares health/state
		// requirements over the work_item member that nothing ever serves
		// (no assembled_result row for either), which DeriveCompletenessAuthority
		// reads as degraded -- a genuine, natural partial-vs-degraded
		// disagreement, not an injected outcome row.
		Synthesizer: synthesizerFunc(func(_ context.Context, _ storage.Principal, input SynthesisInput) (InvestigationResult, error) {
			return InvestigationResult{
				Status: InvestigationPartial, DirectJudgment: "Available work items.", CurrentState: "Available work items.",
				DeterministicAnswer: "Available work items.", StrongestPressures: []string{}, Drivers: []DriverJudgment{},
				RemainingWork: []Finding{}, ReadinessGaps: []Finding{}, Paths: []RelationshipPath{}, Conflicts: []Finding{},
				Limitations: []string{}, EvidenceRefIDs: []string{}, ClaimedFacts: []ClaimedFact{}, Warnings: []string{},
				Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				Versions: VersionSet{Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1", InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1"},
			}, nil
		}),
		Results: store, Requirements: registryDeriver{}, Telemetry: telemetry,
	}, EngineOptions{ServiceVersion: "test", NewResultID: func() string { return "result_tuple_fresh_003" }, ServerCompletenessAuthoritySymmetricEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org-1"}, validInvestigationRequestWithConfirmedWindow())
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != InvestigationDegraded {
		t.Fatalf("served Status = %q, want degraded (the symmetric flag must still correct a fresh work-item census result)", result.Status)
	}
	if len(telemetry.completenessAuthorities) != 1 {
		t.Fatalf("completenessAuthorities recorded = %d, want 1", len(telemetry.completenessAuthorities))
	}
	observation := telemetry.completenessAuthorities[0]
	if observation.ModelStatus != InvestigationPartial {
		t.Fatalf("telemetry ModelStatus = %q, want partial -- the certified line must name the model's ORIGINAL claim, never the corrected served status", observation.ModelStatus)
	}
	if !observation.Disagreed || !observation.WouldFlip {
		t.Fatalf("telemetry Disagreed=%v WouldFlip=%v, want both true: the fresh work-item census path must not go blind to a disagreement its own correction just resolved", observation.Disagreed, observation.WouldFlip)
	}
}
