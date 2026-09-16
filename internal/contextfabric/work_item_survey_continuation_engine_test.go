package contextfabric

import (
	"context"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestWorkItemSurveyTwoTurnShapeComposesAndServesWithoutRanking is the
// CHAOS-5787 end-to-end proof through the real Engine.Investigate, both
// turns: a work-item survey tuple's t1 asks to confirm the evidence window
// (this turn's own admission is unrelated to that gate), and t2 -- a
// window-only continuation redeeming t1's own offered receipt -- COMPOSES
// the carried frame (canonical, never stripped) and SERVES, with no ranking
// requirement in the plan. The carried frame is byte-identical to what
// validation produced and to what the composition boundary revalidates, so
// the continuation composes rather than refusing with a non-canonical-frame
// invariant.
func TestWorkItemSurveyTwoTurnShapeComposesAndServesWithoutRanking(t *testing.T) {
	defer reportWorkItemMutationPanic(t)

	frame := frameWith([]InvestigationGoal{GoalRankOrSurvey}, SubjectExpression{
		Kind:   SubjectExpressionChildrenOfScope,
		Scoped: &ScopedSetExpression{AnchorTerms: []string{"project"}, MemberKind: SubjectWorkItem},
	}, TemporalIntentCurrent, nil)
	if !frame.HasObligation(ObligationRanking) {
		t.Fatalf("fixture defect: frame does not carry ranking")
	}
	outcome := QuestionFamilyOutcome{
		Family: QuestionFamilyScopedCohortStatus, Source: QuestionFamilySourceModel,
		Frame: &frame, FrameObligations: frame.Obligations,
		Gate:          workItemTupleFrameGate(DecideFrameGate(ValidateFrame(frame, nil, ""), true), &frame, workItemTupleFamilyPolicyForTest(QuestionFamilyScopedCohortStatus), TimeContext{Axis: TemporalCurrent}),
		WinningSample: FamilySample{ScopeAnchorKind: SubjectProject, ScopeAnchorTerm: "Project"},
	}
	if outcome.Gate.Outcome != FrameGatePassed {
		t.Fatalf("fixture failed to produce an admitted survey gate: %+v", outcome.Gate)
	}
	payload := workItemTuplePayloadFixture(t)
	graph := &dispatchGraphProbe{graphReaderStub: graphReaderStub{resolution: payload.SubjectResolution, bases: provenCommitBases(payload.SubjectResolution.Committed...)}}
	gate, err := NewWorkItemMembershipGate(1, 0)
	if err != nil {
		t.Fatal(err)
	}
	membershipReads, factReads := 0, 0
	store := &staticResultStore{results: map[string]InvestigationResult{}, states: map[string]*PersistedSemanticState{}}
	telemetry := &recordingTelemetry{}
	principal := storage.Principal{OrgID: "org-1"}
	nextID := 0
	engine, err := NewEngine(EngineDependencies{
		// WindowClass is required for the evidence-window confirmation gate
		// to have anything to classify: with none, ClassifyWindow/DefaultRelativeID cannot
		// determine a class default and effectiveWindow comes back nil,
		// which never asks for confirmation at all -- "refuse to guess",
		// not "ask". A class-bearing question (survey is one) gets a class
		// default it CAN ask about.
		Interpreter: familyInterpreter{interpreted: InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "survey", TimeContext: TimeContext{Axis: TemporalCurrent}, WindowClass: WindowClassTrendAssessment, FactRequirements: []FactRequirement{{Kind: FactStatus}}}, outcome: outcome},
		Graph:       graph,
		CandidateVerifier: func(context.Context, storage.Principal, RequestedScope, ResolvedGraphBinding, SubjectKind, string) (bool, CandidateVerificationReason) {
			return true, ""
		},
		WorkItemMembership: tupleMembershipFunc(func(ctx context.Context, _ storage.Principal, _ WorkItemMembershipRequest) (*WorkItemMembershipLease, WorkItemMembershipResult, error) {
			membershipReads++
			lease, err := gate.Acquire(ctx)
			return lease, WorkItemMembershipResult{
				Census:  WorkItemMembershipCensus{State: WorkItemMembershipCensusExact, PopulationMeasured: true, AuthorizedPopulation: 1},
				Members: []WorkItemMembershipMember{{CanonicalID: payload.Cohort.Members[0].Subject.CanonicalID, WorkItemID: "work-1"}},
			}, err
		}),
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			factReads++
			return CanonicalFactBundle{Facts: []CanonicalFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}, Version: "ops-v1"}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return InvestigationResult{Status: InvestigationComplete, DirectJudgment: "Available work items.", CurrentState: "Available work items.", DeterministicAnswer: "Available work items.", StrongestPressures: []string{}, Drivers: []DriverJudgment{}, RemainingWork: []Finding{}, ReadinessGaps: []Finding{}, Paths: []RelationshipPath{}, Conflicts: []Finding{}, Limitations: []string{}, EvidenceRefIDs: []string{}, ClaimedFacts: []ClaimedFact{}, Warnings: []string{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}, Versions: VersionSet{Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1", InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1"}}, nil
		}),
		Results:      store,
		Requirements: registryDeriver{},
		Telemetry:    telemetry,
	}, EngineOptions{ServiceVersion: "test", NewResultID: func() string {
		nextID++
		if nextID == 1 {
			return "result_5787_t1"
		}
		return "result_5787_t2"
	}})
	if err != nil {
		t.Fatal(err)
	}

	// t1: no confirmed window at all -- the evidence-window confirmation
	// gate must intercept before dispatch, independent of this arm's own
	// (passed) admission.
	t1Request := validInvestigationRequest()
	t1Request.RequestID = "request_5787_t1"
	t1, err := engine.Investigate(context.Background(), principal, t1Request)
	if err != nil {
		t.Fatalf("t1 Investigate() error = %v", err)
	}
	if t1.Status != InvestigationClarificationRequired {
		t.Fatalf("t1 status = %s, want clarification_required (window confirmation)", t1.Status)
	}
	if t1.WindowClarification == nil || len(t1.WindowClarification.Options) == 0 {
		t.Fatalf("t1 offered no window options: %+v", t1.WindowClarification)
	}
	if membershipReads != 0 || factReads != 0 {
		t.Fatalf("t1 dispatched downstream work: membership=%d facts=%d, want 0,0 (window confirmation precedes dispatch)", membershipReads, factReads)
	}
	if store.saved == nil || store.saved.ResultID != t1.ResultID {
		t.Fatalf("t1 did not save under its own result id: saved=%+v", store.saved)
	}
	// Make t1's saved reading loadable the way a real store would for turn
	// two -- staticResultStore only remembers the LAST save.
	store.results[t1.ResultID] = *store.saved
	if store.savedSemantic == nil || store.savedSemantic.State == nil {
		t.Fatalf("t1 saved no semantic state")
	}
	store.states[t1.ResultID] = store.savedSemantic.State
	if !store.savedSemantic.State.FramePresent || store.savedSemantic.State.Frame == nil || !store.savedSemantic.State.Frame.HasObligation(ObligationRanking) {
		t.Fatalf("t1's persisted frame is not canonical: %+v", store.savedSemantic.State.Frame)
	}

	var option contractsv1.ContextFabricWindowOption
	for _, candidate := range t1.WindowClarification.Options {
		if candidate.RelativeID == RelativeWindowTrailing90D {
			option = candidate
			break
		}
	}
	if option.ReceiptID == "" {
		option = t1.WindowClarification.Options[0]
	}

	// t2: the SAME question, no explicit window, exactly one window
	// receipt naming t1 -- the window-only continuation shape.
	t2Request := validInvestigationRequest()
	t2Request.RequestID = "request_5787_t2"
	t2Request.PriorWindowReceipts = []BoundSubjectReceipt{{ResultID: t1.ResultID, ReceiptID: option.ReceiptID}}
	t2, err := engine.Investigate(context.Background(), principal, t2Request)
	if err != nil {
		t.Fatalf("t2 Investigate() error = %v", err)
	}
	t.Logf("t2 status=%s refusal_basis=%s", t2.Status, t2.RefusalBasis)
	if t2.RefusalBasis == contractsv1.ContextFabricRefusalBasisContinuationContextUnverifiable {
		t.Fatalf("BUG: t2 refused with continuation_context_unverifiable -- the composition boundary rejected the carried frame as non-canonical")
	}
	if t2.RefusalBasis != "" {
		t.Fatalf("t2 refusal_basis = %q, want none", t2.RefusalBasis)
	}
	if t2.Status == InvestigationClarificationRequired {
		t.Fatalf("t2 status = %s, want a served answer (the window was just redeemed)", t2.Status)
	}
	if membershipReads != 1 || factReads != 1 {
		t.Fatalf("t2 did not dispatch: membership=%d facts=%d, want 1,1", membershipReads, factReads)
	}

	// THE PROPERTY: the served turn's plan carries no ranking requirement --
	// this arm's whole reason for existing -- and it got there via the
	// composed CANONICAL carried frame, not a stripped one.
	if store.savedSemantic == nil || store.savedSemantic.State == nil {
		t.Fatalf("t2 saved no semantic state")
	}
	if !store.savedSemantic.State.FramePresent || store.savedSemantic.State.Frame == nil || !store.savedSemantic.State.Frame.HasObligation(ObligationRanking) {
		t.Fatalf("t2's own persisted frame is not canonical: %+v", store.savedSemantic.State.Frame)
	}
	for _, row := range store.savedSemantic.State.DerivedRequirements() {
		if row.Obligation == ObligationRanking {
			t.Fatalf("t2 plan requirements = %+v, want no ranking coordinate", store.savedSemantic.State.DerivedRequirements())
		}
	}
}

// TestWorkItemSurveyLegacyPersistedFrameStillComposes pins backward
// compatibility for a row this arm's settled admission wrote directly onto
// the persisted frame instead of the plan alone -- a legitimate shape this
// arm's own rule can produce, distinct from a canonical row and carrying no
// format-version marker of its own. A turn continuing such a row must still
// compose and serve: the composition boundary's second candidate
// (carriedWorkItemTupleLegacyFrame) reconstructs the one obligation this
// arm's rule can omit and revalidates the reconstruction through the same
// path as any other carried frame.
func TestWorkItemSurveyLegacyPersistedFrameStillComposes(t *testing.T) {
	defer reportWorkItemMutationPanic(t)

	frame := frameWith([]InvestigationGoal{GoalRankOrSurvey}, SubjectExpression{
		Kind:   SubjectExpressionChildrenOfScope,
		Scoped: &ScopedSetExpression{AnchorTerms: []string{"project"}, MemberKind: SubjectWorkItem},
	}, TemporalIntentCurrent, nil)
	if !frame.HasObligation(ObligationRanking) {
		t.Fatalf("fixture defect: frame does not carry ranking")
	}
	outcome := QuestionFamilyOutcome{
		Family: QuestionFamilyScopedCohortStatus, Source: QuestionFamilySourceModel,
		Frame: &frame, FrameObligations: frame.Obligations,
		Gate:          workItemTupleFrameGate(DecideFrameGate(ValidateFrame(frame, nil, ""), true), &frame, workItemTupleFamilyPolicyForTest(QuestionFamilyScopedCohortStatus), TimeContext{Axis: TemporalCurrent}),
		WinningSample: FamilySample{ScopeAnchorKind: SubjectProject, ScopeAnchorTerm: "Project"},
	}
	if outcome.Gate.Outcome != FrameGatePassed {
		t.Fatalf("fixture failed to produce an admitted survey gate: %+v", outcome.Gate)
	}
	payload := workItemTuplePayloadFixture(t)
	graph := &dispatchGraphProbe{graphReaderStub: graphReaderStub{resolution: payload.SubjectResolution, bases: provenCommitBases(payload.SubjectResolution.Committed...)}}
	gate, err := NewWorkItemMembershipGate(1, 0)
	if err != nil {
		t.Fatal(err)
	}
	membershipReads, factReads := 0, 0
	store := &staticResultStore{results: map[string]InvestigationResult{}, states: map[string]*PersistedSemanticState{}}
	principal := storage.Principal{OrgID: "org-1"}
	nextID := 0
	engine, err := NewEngine(EngineDependencies{
		Interpreter: familyInterpreter{interpreted: InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "survey", TimeContext: TimeContext{Axis: TemporalCurrent}, WindowClass: WindowClassTrendAssessment, FactRequirements: []FactRequirement{{Kind: FactStatus}}}, outcome: outcome},
		Graph:       graph,
		CandidateVerifier: func(context.Context, storage.Principal, RequestedScope, ResolvedGraphBinding, SubjectKind, string) (bool, CandidateVerificationReason) {
			return true, ""
		},
		WorkItemMembership: tupleMembershipFunc(func(ctx context.Context, _ storage.Principal, _ WorkItemMembershipRequest) (*WorkItemMembershipLease, WorkItemMembershipResult, error) {
			membershipReads++
			lease, err := gate.Acquire(ctx)
			return lease, WorkItemMembershipResult{
				Census:  WorkItemMembershipCensus{State: WorkItemMembershipCensusExact, PopulationMeasured: true, AuthorizedPopulation: 1},
				Members: []WorkItemMembershipMember{{CanonicalID: payload.Cohort.Members[0].Subject.CanonicalID, WorkItemID: "work-1"}},
			}, err
		}),
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			factReads++
			return CanonicalFactBundle{Facts: []CanonicalFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}, Version: "ops-v1"}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return InvestigationResult{Status: InvestigationComplete, DirectJudgment: "Available work items.", CurrentState: "Available work items.", DeterministicAnswer: "Available work items.", StrongestPressures: []string{}, Drivers: []DriverJudgment{}, RemainingWork: []Finding{}, ReadinessGaps: []Finding{}, Paths: []RelationshipPath{}, Conflicts: []Finding{}, Limitations: []string{}, EvidenceRefIDs: []string{}, ClaimedFacts: []ClaimedFact{}, Warnings: []string{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}, Versions: VersionSet{Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1", InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1"}}, nil
		}),
		Results:      store,
		Requirements: registryDeriver{},
	}, EngineOptions{ServiceVersion: "test", NewResultID: func() string {
		nextID++
		if nextID == 1 {
			return "result_5787_legacy_t1"
		}
		return "result_5787_legacy_t2"
	}})
	if err != nil {
		t.Fatal(err)
	}

	t1Request := validInvestigationRequest()
	t1Request.RequestID = "request_5787_legacy_t1"
	t1, err := engine.Investigate(context.Background(), principal, t1Request)
	if err != nil {
		t.Fatalf("t1 Investigate() error = %v", err)
	}
	if t1.Status != InvestigationClarificationRequired || t1.WindowClarification == nil || len(t1.WindowClarification.Options) == 0 {
		t.Fatalf("fixture defect: t1 did not raise the window need: status=%s", t1.Status)
	}
	if store.saved == nil || store.savedSemantic == nil || store.savedSemantic.State == nil {
		t.Fatalf("fixture defect: t1 saved no result/semantic state")
	}
	store.results[t1.ResultID] = *store.saved
	store.states[t1.ResultID] = store.savedSemantic.State

	// SIMULATE A ROW THIS ARM'S OWN RULE WROTE DIRECTLY ONTO THE PERSISTED
	// FRAME: strip ranking from the stored frame's own Obligations, exactly
	// what the settled admission's obligation-omission rule removes from
	// the plan today -- applied here to the FRAME instead, the one other
	// legitimate shape carriedWorkItemTupleLegacyFrame exists to recognize.
	legacyFrame := store.states[t1.ResultID].Frame
	kept := legacyFrame.Obligations[:0:0]
	for _, obligation := range legacyFrame.Obligations {
		if obligation != ObligationRanking {
			kept = append(kept, obligation)
		}
	}
	legacyFrame.Obligations = kept
	if legacyFrame.HasObligation(ObligationRanking) {
		t.Fatalf("fixture defect: legacy frame still carries ranking")
	}

	var option contractsv1.ContextFabricWindowOption
	for _, candidate := range t1.WindowClarification.Options {
		if candidate.RelativeID == RelativeWindowTrailing90D {
			option = candidate
			break
		}
	}
	if option.ReceiptID == "" {
		option = t1.WindowClarification.Options[0]
	}

	t2Request := validInvestigationRequest()
	t2Request.RequestID = "request_5787_legacy_t2"
	t2Request.PriorWindowReceipts = []BoundSubjectReceipt{{ResultID: t1.ResultID, ReceiptID: option.ReceiptID}}
	t2, err := engine.Investigate(context.Background(), principal, t2Request)
	if err != nil {
		t.Fatalf("t2 Investigate() error = %v", err)
	}
	t.Logf("t2 status=%s refusal_basis=%s", t2.Status, t2.RefusalBasis)
	if t2.RefusalBasis != "" {
		t.Fatalf("t2 refusal_basis = %q, want none -- a legacy persisted frame must still compose", t2.RefusalBasis)
	}
	if t2.Status == InvestigationClarificationRequired {
		t.Fatalf("t2 status = %s, want a served answer", t2.Status)
	}
	if membershipReads != 1 || factReads != 1 {
		t.Fatalf("t2 did not dispatch: membership=%d facts=%d, want 1,1", membershipReads, factReads)
	}
}
