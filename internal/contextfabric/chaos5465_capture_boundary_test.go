package contextfabric

import (
	"context"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// TestCaptureBoundary_TheStoredReadingIsTheServedTurnsOwn holds the rule that a
// result persists the reading THAT RESULT serves, and the identity of the
// request THAT RESULT answered.
//
// The capture used to be taken once, where planning finished, and carried down
// to whichever exit fired. Three things settle after that point: an over-bound
// group read clears the plan's group axis, the plan-seam collapse turns the
// frame gate refused, and a turn whose continuation was rejected is answered as
// a request in its own right. A snapshot taken upstream describes the turn that
// WOULD have happened, and because the snapshot is carryable the next turn
// continues that fiction -- so each exit now builds its own from the values as
// they stand there.
func TestCaptureBoundary_TheStoredReadingIsTheServedTurnsOwn(t *testing.T) {
	t.Parallel()

	// THROUGH THE ENGINE: a turn whose continuation is rejected for a changed
	// answer-shaping option must store ITS OWN request identity, not the
	// continuation projection (which drops the referenced exchange).
	t.Run("a rejected continuation stores the whole request's identity", func(t *testing.T) {
		t.Parallel()
		base := validInvestigationRequest().Question
		prior := continuationPrior(t, continuationPriorID, base, QuestionFamilyDiscoveredCohortRanking, "")
		inner := &staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}}
		engine, _ := newRefusalEngine(t, newRefusalStore(inner), forcedFamilyInterpreter{family: QuestionFamilyGroupedCohortStatus, groupKind: contractsv1.ContextFabricSubjectTeam}, &recordingTelemetry{})
		request := continuationRequest(base)
		// THE DISCRIMINATING INPUT: the conversation CARRIES the referenced
		// exchange, so the projection and the whole-request identity are
		// different digests. Without it the cell cannot tell them apart.
		request.Conversation = []contractsv1.ContextFabricConversationTurn{
			{TurnID: "turn_0001", Role: contractsv1.ContextFabricConversationUser, Content: base, CreatedAt: prior.GeneratedAt},
			{TurnID: "turn_0002", Role: contractsv1.ContextFabricConversationAssistant, Content: "an earlier answer", CreatedAt: prior.GeneratedAt},
		}
		request.Options.MaxDrivers = request.Options.MaxDrivers + 1
		if _, err := engine.Investigate(context.Background(), acceptancePrincipal(), request); err != nil {
			t.Fatalf("Investigate: %v", err)
		}
		saved := inner.savedSemantic
		if saved == nil || saved.State == nil {
			t.Fatalf("the turn saved no reading: %+v", saved)
		}
		whole := SemanticRequestIdentityOf(request, "")
		projection := SemanticRequestIdentityOf(request, prior.Question)
		if whole.Equal(projection) {
			t.Fatalf("fixture defect: the two identities are the same digest, so the cell proves nothing")
		}
		t.Logf("stored=%s… whole=%s… projection=%s…", saved.State.RequestIdentity.Digest[:12], whole.Digest[:12], projection.Digest[:12])
		if !saved.State.RequestIdentity.Equal(whole) {
			t.Errorf("stored identity is not this request's own; equal to the projection = %v", saved.State.RequestIdentity.Equal(projection))
		}
	})

	// THE CONTROL: an APPLIED continuation still reuses admission's own digest,
	// so the value compared and the value stored remain one computation.
	t.Run("an applied continuation reuses admission's identity", func(t *testing.T) {
		t.Parallel()
		base := validInvestigationRequest().Question
		prior := continuationPrior(t, continuationPriorID, base, QuestionFamilyGroupedCohortStatus, contractsv1.ContextFabricSubjectTeam)
		inner := &staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}}
		h := newContinuationHarness(t, inner, forcedFamilyInterpreter{family: QuestionFamilyGroupedCohortStatus, groupKind: contractsv1.ContextFabricSubjectTeam})
		request := continuationRequest(base)
		request.Conversation = []contractsv1.ContextFabricConversationTurn{
			{TurnID: "turn_0001", Role: contractsv1.ContextFabricConversationUser, Content: base, CreatedAt: prior.GeneratedAt},
			{TurnID: "turn_0002", Role: contractsv1.ContextFabricConversationAssistant, Content: "an earlier answer", CreatedAt: prior.GeneratedAt},
		}
		h.investigate(t, request)
		d := h.soleDecision(t)
		saved := inner.savedSemantic
		if d.Disposition != ContinuationApplied {
			t.Skipf("the fixture did not apply (%s/%s), so the control cannot speak", d.Disposition, d.Reason)
		}
		if saved == nil || saved.State == nil {
			t.Fatalf("an applied continuation saved no reading")
		}
		projection := SemanticRequestIdentityOf(request, prior.Question)
		t.Logf("applied: stored=%s… admission=%s… projection=%s…",
			saved.State.RequestIdentity.Digest[:12], d.RequestIdentity.Digest[:12], projection.Digest[:12])
		if !saved.State.RequestIdentity.Equal(d.RequestIdentity) {
			t.Errorf("an applied continuation stored %v, not admission's own digest -- the value compared and the value stored must be one computation", saved.State.RequestIdentity.Digest[:12])
		}
	})

	// AT THE SEAM: the two late mutations, executed against the builder every
	// exit calls. Driving the over-bound group read and the plan-seam collapse
	// through the engine needs graph fixtures this pin does not build; what it
	// does prove is that the builder reports the values AS THEY STAND when the
	// exit calls it, which is the whole of the fix.
	t.Run("the builder reports the plan and gate it is given", func(t *testing.T) {
		t.Parallel()
		engine, _ := newRefusalEngine(t, &staticResultStore{results: map[string]InvestigationResult{}}, forcedFamilyInterpreter{family: QuestionFamilyGroupedCohortStatus}, &recordingTelemetry{})
		request := validInvestigationRequest()
		frame := semanticFixture(t).Frame
		grouped := QuestionFamilyOutcome{Family: QuestionFamilyGroupedCohortStatus, Source: QuestionFamilySourceModel, Frame: frame, Gate: FrameGate{Outcome: FrameGatePassed}}

		// The group axis the plan carries at THIS exit, not the one planning proposed.
		planWithAxis := &AnswerPlan{Family: QuestionFamilyGroupedCohortStatus, GroupKind: contractsv1.ContextFabricSubjectTeam, FamilyVersion: QuestionFamilyTableVersion}
		planCleared := &AnswerPlan{Family: QuestionFamilyGroupedCohortStatus, GroupKind: "", FamilyVersion: QuestionFamilyTableVersion}
		// The derivation the frame actually produces: a capture over a frame
		// with no declarations is refused as an incomplete reading, which
		// would make every cell below read an absence instead of a value.
		derived := DeriveRequirements(*frame, ObligationSeed{}, nil)
		withAxis := engine.captureAcceptedReading(request, windowContinuationDecision{}, grouped, ShapeOpen, planWithAxis, derived)
		cleared := engine.captureAcceptedReading(request, windowContinuationDecision{}, grouped, ShapeOpen, planCleared, derived)
		// ASSERTED BEFORE LOGGED, so an absence fails a cell instead of
		// aborting the package.
		if withAxis.Write.State == nil {
			t.Fatalf("a grouped plan under a grouped frame was refused: %q", withAxis.Write.Absence)
		}
		t.Logf("group axis: plan=team -> stored=%q | plan=\"\" -> state=%v absence=%q",
			withAxis.Write.State.GroupKind, cleared.Write.State != nil, cleared.Write.Absence)
		if withAxis.Write.State.GroupKind != contractsv1.ContextFabricSubjectTeam {
			t.Errorf("a grouped plan stored group %q", withAxis.Write.State.GroupKind)
		}
		// AND THE CLEARED AXIS IS NOT A STORABLE READING AT ALL. The frame is
		// grouped; a plan that dropped its axis no longer agrees with it, and
		// the snapshot's own cross-field rule refuses the pair. That is the
		// honest outcome for a turn whose group read was over bound: it has no
		// coherent grouped reading to carry, so it carries the closed absence
		// rather than a reading claiming an axis the served plan lacks. What it
		// must NOT do -- and did, while the capture was taken upstream -- is
		// store the pre-clearing axis and let the next turn resurrect it.
		if cleared.Write.State != nil {
			t.Errorf("a cleared axis stored a reading (group=%q) instead of the closed absence", cleared.Write.State.GroupKind)
		}
		if cleared.Write.Absence != SemanticStateAbsenceSnapshotInvalid {
			t.Errorf("a cleared axis recorded absence %q, want %q", cleared.Write.Absence, SemanticStateAbsenceSnapshotInvalid)
		}

		// The gate the outcome carries at THIS exit: a refused evaluation must
		// not persist as a passed reading.
		refused := grouped
		refused.Gate = FrameGate{Outcome: FrameGateRefusedBasis, RefuseBasis: CohortMemberKindUnservable}
		refusedCapture := engine.captureAcceptedReading(request, windowContinuationDecision{}, refused, ShapeOpen, planWithAxis, derived)
		gate := FrameGateOutcome("")
		if refusedCapture.Write.State != nil {
			gate = refusedCapture.Write.State.Validation.GateOutcome
		}
		t.Logf("refused gate -> stored gate=%q absence=%q", gate, refusedCapture.Write.Absence)
		if gate == FrameGatePassed {
			t.Errorf("a refused evaluation persisted a PASSED reading, which the next turn would continue")
		}
	})
}
