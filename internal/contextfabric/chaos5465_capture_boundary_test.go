package contextfabric

import (
	"context"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
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

// TestCaptureBoundary_TheTwoLateMutationsThroughTheEngine drives the two late
// mutations THROUGH Investigate, twice each: turn one saves, and turn two must
// not continue a reading turn one did not serve.
//
// IT HAS TO BE AN ENGINE RUN. Both mutations happen AFTER the point where the
// capture used to be taken -- the over-bound group read clears the plan's axis,
// the plan seam turns the frame gate refused -- so the only way to observe what
// a result actually persists is to run the turn and read the save. The seam
// cells above prove the builder reports the values it is given; only these can
// prove the exits reach it with the final ones, and that nothing carryable
// survives a turn that refused.
//
// NOT t.Parallel(): the over-bound fixture installs the process default logger.
func TestCaptureBoundary_TheTwoLateMutationsThroughTheEngine(t *testing.T) {
	logs := captureEngineLogger(t)
	facts := &groupReadRecorder{facts: func(CanonicalFactRequest) CanonicalFactBundle {
		bundle := emptyFactBundle()
		bundle.Facts = groupReadOverBoundMemberFacts()
		bundle.Coverage.Sources = []SourceObservation{{Source: "canonical_fact:metrics", State: SourceAvailable}}
		return bundle
	}}

	for _, tc := range []struct {
		name       string
		cohortKind SubjectKind
		members    []CohortMember
		why        string
	}{
		{
			name: "an over-bound group read drops the axis", cohortKind: SubjectProject,
			members: groupReadCohortMembers(251),
			why:     "251 proposed groups are refused, not sliced, and the plan's axis is cleared AFTER planning finished",
		},
		{
			name: "the plan seam collapses group onto member", cohortKind: SubjectTeam,
			members: groupReadCohortMembers(3),
			why:     "the cohort comes back as teams while the frame groups projects by team, so the plan's group kind equals its member kind and the gate is turned refused AFTER planning finished",
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			store := &staticResultStore{results: map[string]InvestigationResult{}}
			engine, request := groupReadEngineFixtureConfigured(t, logs.telemetry, facts, tc.members, nil, tc.cohortKind, nil, nil,
				func(c *groupReadFixtureConfig) { c.results = store })
			turnOne, err := engine.Investigate(canonicalRequestContext(), storage.Principal{OrgID: "org_1"}, request)
			if err != nil {
				t.Fatalf("turn one: %v", err)
			}
			saved := store.savedSemantic
			servedGroup := SubjectKind("")
			if turnOne.AnswerPlan != nil {
				servedGroup = turnOne.AnswerPlan.GroupKind
			}
			storedGroup, storedGate, hasState := SubjectKind(""), FrameGateOutcome(""), false
			absence := SemanticStateAbsence("")
			if saved != nil {
				absence = saved.Absence
				if saved.State != nil {
					hasState, storedGroup, storedGate = true, saved.State.GroupKind, saved.State.Validation.GateOutcome
				}
			}
			t.Logf("%s\n  served: status=%q group=%q\n  stored: state=%v group=%q gate=%q absence=%q\n  why: %s",
				tc.name, turnOne.Status, servedGroup, hasState, storedGroup, storedGate, absence, tc.why)

			// THE INVARIANT, whichever shape this cell is: what is stored
			// describes the turn that was served, or nothing is stored.
			if hasState && storedGroup != servedGroup {
				t.Errorf("stored group %q but served group %q -- the persisted reading is not this turn's", storedGroup, servedGroup)
			}
			if hasState && turnOne.Status == InvestigationNoMatch && storedGate == FrameGatePassed {
				t.Errorf("a refused turn persisted a PASSED reading, which a later turn would continue")
			}
			if !hasState && absence == "" {
				t.Errorf("no reading and no closed absence either -- the line cannot say why")
			}

			// TURN TWO: a window-only continuation of that row must not
			// resurrect anything turn one did not serve.
			//
			// THE RECEIPT HAS TO BE REDEEMABLE OR THE HALF IS VACUOUS. A
			// fabricated id makes turn two fail admission, and a turn that
			// never reached the carrier proves nothing about what it would
			// have carried -- so the receipt is taken from turn one's own
			// window offer, and the cell says so when there is none.
			if turnOne.ResultID == "" {
				t.Skipf("turn one minted no result id, so there is nothing to continue")
			}
			store.results[turnOne.ResultID] = turnOne
			receipt := ""
			if turnOne.WindowClarification != nil {
				for _, option := range turnOne.WindowClarification.Options {
					if option.ReceiptID != "" {
						receipt = option.ReceiptID
						break
					}
				}
			}
			if receipt == "" {
				t.Logf("  turn two: SKIPPED -- turn one offered no redeemable window receipt, so a continuation of it cannot be driven from this fixture. What the stored side proves stands: state=%v absence=%q, so there is no reading for a later turn to carry.", hasState, absence)
				return
			}
			second := request
			second.PriorWindowReceipts = []BoundSubjectReceipt{{ResultID: turnOne.ResultID, ReceiptID: receipt}}
			turnTwo, secondErr := engine.Investigate(canonicalRequestContext(), storage.Principal{OrgID: "org_1"}, second)
			secondGroup := SubjectKind("")
			source := QuestionFamilySource("")
			if turnTwo.AnswerPlan != nil {
				secondGroup, source = turnTwo.AnswerPlan.GroupKind, turnTwo.AnswerPlan.FamilySource
			}
			t.Logf("  turn two: receipt=%q err=%v status=%q group=%q plan_source=%q", receipt, secondErr != nil, turnTwo.Status, secondGroup, source)
			if secondErr != nil {
				t.Fatalf("turn two errored (%v) -- a cell that cannot reach the carrier cannot speak about it", secondErr)
			}
			if source == QuestionFamilySourceCarried && !hasState {
				t.Errorf("turn two served a CARRIED plan from a row that persisted no reading")
			}
			if source == QuestionFamilySourceCarried && secondGroup != servedGroup {
				t.Errorf("turn two resurrected group %q; turn one served %q", secondGroup, servedGroup)
			}
		})
	}
}
