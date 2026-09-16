package contextfabric

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// AXES CELLS. Each drives composeAcceptedContext directly with a CARRIED
// snapshot, so the boundary is proven on the composition rather than on a
// downstream symptom: the carried frame is revalidated under today's rules and
// either established whole or refused with a named invariant.
func TestCells_CompositionBoundary(t *testing.T) {
	grouped := func(group, member SubjectKind) QuestionFrame {
		return QuestionFrame{
			Goals: []InvestigationGoal{GoalAssessState},
			SubjectExpression: SubjectExpression{
				Kind:    SubjectExpressionGroupedMembers,
				Grouped: &GroupedSetExpression{GroupKind: group, MemberKind: member},
			},
			Temporal: TemporalIntentCurrent,
		}
	}
	validated := func(t *testing.T, frame QuestionFrame) (QuestionFrame, FrameGate) {
		t.Helper()
		result := ValidateFrame(frame, nil, ShapeOpen)
		if result.Outcome != FrameValidationOutcomeValid {
			t.Fatalf("fixture defect: frame invalid (%v)", result.Failure.Invariant)
		}
		return result.Frame, DecideFrameGate(result, true)
	}
	teamRepo, teamRepoGate := validated(t, grouped(contractsv1.ContextFabricSubjectTeam, contractsv1.ContextFabricSubjectRepository))
	projectRepo, _ := validated(t, grouped(contractsv1.ContextFabricSubjectProject, contractsv1.ContextFabricSubjectRepository))
	unservable, refusing := unservableDiscoveredFrame(t)
	// A recorded frame today's validation would refuse (I6: grouped by its own
	// member kind), normalized so only the invariant differs.
	selfGrouped := NormalizeFrame(grouped(contractsv1.ContextFabricSubjectTeam, contractsv1.ContextFabricSubjectTeam))
	selfGrouped = DeriveFrameObligations(selfGrouped, nil)
	// A recorded frame today's normalization would REPAIR.
	stripped := nonCanonicalFrame(teamRepo)

	for _, tc := range []struct {
		name          string
		carried       *PersistedSemanticState
		fresh         *QuestionFrame
		wantOutcome   CompositionOutcome
		wantInvariant string
		wantGroup     SubjectKind
	}{
		{"carried frame established over a different fresh frame", carriedStateFor(t, QuestionFamilyGroupedCohortStatus, contractsv1.ContextFabricSubjectTeam, &teamRepo, teamRepoGate),
			&projectRepo, CompositionAccepted, "", contractsv1.ContextFabricSubjectTeam},
		{"carried frame already equal to the fresh one", carriedStateFor(t, QuestionFamilyGroupedCohortStatus, contractsv1.ContextFabricSubjectTeam, &teamRepo, teamRepoGate),
			&teamRepo, CompositionUnchanged, "", contractsv1.ContextFabricSubjectTeam},
		{"carried frame fails an invariant today", carriedStateFor(t, QuestionFamilyGroupedCohortStatus, contractsv1.ContextFabricSubjectTeam, &selfGrouped, FrameGate{Outcome: FrameGatePassed}),
			&projectRepo, CompositionInvalid, "i6", ""},
		{"carried frame would be repaired today", carriedStateFor(t, QuestionFamilyGroupedCohortStatus, contractsv1.ContextFabricSubjectTeam, &stripped, teamRepoGate),
			&projectRepo, CompositionInvalid, CompositionInvariantCarriedFrameNotCanonical, ""},
		{"carried frame refused by today's gate", carriedStateFor(t, QuestionFamilyDiscoveredCohortRanking, "", &unservable, refusing),
			&projectRepo, CompositionInvalid, CompositionInvariantCarriedFrameRefused, ""},
		{"no carried snapshot at all", nil, &projectRepo, CompositionInvalid, CompositionInvariantCarriedStateIncomplete, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := composeAcceptedContext(compositionInput{Carried: tc.carried, Fresh: tc.fresh, FreshFamily: QuestionFamilyGroupedCohortStatus})
			t.Logf("%s -> outcome=%q gate=%q invariant=%q effective_group=%q",
				tc.name, got.Outcome, got.Gate.Outcome, got.FailedInvariant, got.EffectiveGroupKind())
			if got.Outcome != tc.wantOutcome {
				t.Errorf("outcome=%q want %q", got.Outcome, tc.wantOutcome)
			}
			if got.FailedInvariant != tc.wantInvariant {
				t.Errorf("invariant=%q want %q", got.FailedInvariant, tc.wantInvariant)
			}
			if got.EffectiveGroupKind() != tc.wantGroup {
				t.Errorf("effective group=%q want %q", got.EffectiveGroupKind(), tc.wantGroup)
			}
			// THE PROPERTY: whatever frame comes back is the frame the gate
			// describes, and it is the CARRIED frame, byte for byte.
			if got.Usable() {
				if got.Frame == nil {
					t.Fatalf("usable context with no frame from a framed carrier")
				}
				recheck := ValidateFrame(*got.Frame, got.Frame.WidenedObligations, ShapeOpen)
				if recheck.Outcome != FrameValidationOutcomeValid || got.Gate.Refuses() {
					t.Errorf("USABLE context carries an invalid frame (%v) or a refusing gate %q", recheck.Failure.Invariant, got.Gate.Outcome)
				}
				if !framesEqual(*got.Frame, *tc.carried.Frame) {
					t.Errorf("the accepted frame is not the carried frame")
				}
			} else if got.FailedInvariant == "" {
				t.Errorf("invalid composition with no named invariant")
			}
		})
	}
}

// CELL 6b -- A SURVEY-ADMITTED WORK-ITEM TUPLE'S CARRIED FRAME COMPOSES
// CANONICAL, THROUGH THE CARRIED-GATE OVERRIDE.
//
// The carried frame here is EXACTLY what work_item_tuple_admission.go's
// settled admission persists: the frame ValidateFrame produced, untouched --
// GoalRankOrSurvey's derived ObligationRanking still on it. The persisted
// frame is canonical; the PLAN, not the frame, carries the effective
// (ranking-omitted) obligation set -- see workItemTupleRequirementFrame's
// own doc comment. A canonical carried work-item-tuple frame still needs
// composeAcceptedContext's own raw DecideFrameGate corrected: that check
// refuses children_of_scope/work_item BY CONSTRUCTION, and only this arm's
// own admission ever promotes it, so the boundary needs the admission's
// verdict supplied. carriedWorkItemTupleGateOverride
// (work_item_tuple_admission.go) closes that second gap: engine.go computes
// the promoted verdict and hands it to the boundary as a plain FrameGate.
//
// THREE CELLS, dogfooding the real production function rather than a
// hand-built gate:
//
//	(i)   the carried family allows the tuple -> override passes -> composes.
//	(ii)  the carried family does NOT allow the tuple -> override refuses ->
//	      CompositionInvariantCarriedFrameRefused, via the SUPPLIED gate.
//	(iii) a non-tuple carried frame -> no override at all (nil) -> the
//	      boundary's own unchanged raw-gate path decides it (control).
func TestCells_CompositionBoundary_SurveyAdmittedWorkItemTupleGateOverride(t *testing.T) {
	surveyFrame := func() (QuestionFrame, FrameGate) {
		frame := QuestionFrame{
			Goals: []InvestigationGoal{GoalRankOrSurvey},
			SubjectExpression: SubjectExpression{
				Kind:   SubjectExpressionChildrenOfScope,
				Scoped: &ScopedSetExpression{MemberKind: SubjectWorkItem, AnchorTerms: []string{"dev health ops"}},
			},
			Temporal: TemporalIntentCurrent,
		}
		result := ValidateFrame(frame, nil, ShapeOpen)
		if result.Outcome != FrameValidationOutcomeValid {
			t.Fatalf("fixture defect: frame invalid (%v)", result.Failure.Invariant)
		}
		if !result.Frame.HasObligation(ObligationRanking) {
			t.Fatalf("fixture defect: GoalRankOrSurvey must derive ranking to discriminate this cell")
		}
		// The settled reading persists under the PROMOTED gate -- exactly
		// what t1's own admission (engine.go) records before saving.
		gate := workItemTupleFrameGate(DecideFrameGate(result, true), &result.Frame, true, TimeContext{Axis: TemporalCurrent})
		if gate.Outcome != FrameGatePassed {
			t.Fatalf("fixture defect: survey frame did not admit (%+v)", gate)
		}
		return result.Frame, gate
	}
	canonical, gate := surveyFrame()
	timeContext := TimeContext{Axis: TemporalCurrent}

	t.Run("i: carried family allows the tuple -- composes", func(t *testing.T) {
		carried := carriedStateFor(t, QuestionFamilyScopedCohortStatus, "", &canonical, gate)
		override := carriedWorkItemTupleGateOverride(carried, timeContext)
		if override == nil || override.Outcome != FrameGatePassed {
			t.Fatalf("fixture defect: override = %+v, want a passed override", override)
		}
		got := composeAcceptedContext(compositionInput{
			Carried: carried, FreshFamily: QuestionFamilyScopedCohortStatus,
			FreshGate: FrameGate{Outcome: FrameGateNotProposed}, TransitionEstablished: true,
			CarriedGateOverride: override,
		})
		t.Logf("i -> outcome=%q invariant=%q gate=%q", got.Outcome, got.FailedInvariant, got.Gate.Outcome)
		if got.Outcome != CompositionAccepted && got.Outcome != CompositionUnchanged {
			t.Fatalf("outcome=%q invariant=%q, want accepted/unchanged", got.Outcome, got.FailedInvariant)
		}
		if !got.Usable() || got.Frame == nil || !got.Frame.HasObligation(ObligationRanking) {
			t.Fatalf("composed context did not carry the canonical frame through: %+v", got)
		}
		if !framesEqual(*got.Frame, canonical) {
			t.Fatalf("composed frame is not the carried canonical frame")
		}
	})

	t.Run("ii: carried family disallows the tuple -- refused via the supplied gate", func(t *testing.T) {
		if workItemTupleFamilyPolicyForTest(QuestionFamilyDiscoveredCohortRanking) {
			t.Fatalf("fixture defect: QuestionFamilyDiscoveredCohortRanking unexpectedly allows the work-item tuple")
		}
		carried := carriedStateFor(t, QuestionFamilyDiscoveredCohortRanking, "", &canonical, gate)
		override := carriedWorkItemTupleGateOverride(carried, timeContext)
		if override == nil || !override.Refuses() {
			t.Fatalf("fixture defect: override = %+v, want a refusing override", override)
		}
		got := composeAcceptedContext(compositionInput{
			Carried: carried, FreshFamily: QuestionFamilyDiscoveredCohortRanking,
			FreshGate: FrameGate{Outcome: FrameGateNotProposed}, TransitionEstablished: true,
			CarriedGateOverride: override,
		})
		t.Logf("ii -> outcome=%q invariant=%q gate=%q", got.Outcome, got.FailedInvariant, got.Gate.Outcome)
		if got.Outcome != CompositionInvalid || got.FailedInvariant != CompositionInvariantCarriedFrameRefused {
			t.Fatalf("outcome=%q invariant=%q, want invalid/carried_frame_refused", got.Outcome, got.FailedInvariant)
		}
		if got.Gate.Outcome != override.Outcome {
			t.Fatalf("reported gate=%q, want the SUPPLIED override %q", got.Gate.Outcome, override.Outcome)
		}
	})

	t.Run("iii: a non-tuple carried frame gets no override (control)", func(t *testing.T) {
		teamRepo, teamRepoGate := boundaryGroupedFrame(t, contractsv1.ContextFabricSubjectTeam, contractsv1.ContextFabricSubjectRepository)
		carried := carriedStateFor(t, QuestionFamilyGroupedCohortStatus, contractsv1.ContextFabricSubjectTeam, &teamRepo, teamRepoGate)
		if override := carriedWorkItemTupleGateOverride(carried, timeContext); override != nil {
			t.Fatalf("fixture defect: override = %+v, want nil for a non-tuple carried frame", override)
		}
		got := composeAcceptedContext(compositionInput{
			Carried: carried, Fresh: &teamRepo, FreshFamily: QuestionFamilyGroupedCohortStatus,
			FreshGate: FrameGate{Outcome: FrameGateNotProposed}, TransitionEstablished: true,
			// No CarriedGateOverride -- the unchanged raw-gate path.
		})
		t.Logf("iii -> outcome=%q invariant=%q gate=%q", got.Outcome, got.FailedInvariant, got.Gate.Outcome)
		if got.Outcome != CompositionAccepted && got.Outcome != CompositionUnchanged {
			t.Fatalf("outcome=%q invariant=%q, want accepted/unchanged -- an ordinary carried frame is unaffected", got.Outcome, got.FailedInvariant)
		}
	})
}

// CELL 7 -- IDENTITY IS ASSERTED BY VALUE, NOT BY THE ABSENCE OF A DELETION.
//
// r4 R4-4: the previous suite's identity arm only DELETED the check, which it
// caught. Replacing raw-byte equality with canonical equality is plausible,
// compiles, and passed the entire suite -- so the suite proved the guard was
// present and said nothing about whether it was the right guard.
//
// This pin names the boundary: two questions that are canonically equal but
// byte-different must NOT be a continuation. It fails under exactly that
// substitution.
func TestCells_IdentityIsRawBytesNotCanonicalEquality(t *testing.T) {
	t.Parallel()

	// Canonicalisation strips trailing terminal punctuation, so these two
	// differ in bytes and agree canonically -- the discriminating pair.
	const asked = "How has each team's health trended over the last two quarters?"
	const prior = "How has each team's health trended over the last two quarters"

	if CanonicalizeQuestion(asked) != CanonicalizeQuestion(prior) {
		t.Fatalf("fixture defect: the pair must be canonically EQUAL to discriminate; got %q vs %q",
			CanonicalizeQuestion(asked), CanonicalizeQuestion(prior))
	}
	if asked == prior {
		t.Fatalf("fixture defect: the pair must differ in raw bytes")
	}

	got := continuationQuestionIdentity(asked, prior)
	t.Logf("raw_equal=false canonical_equal=true -> reason=%q", got)
	if got != ContinuationReasonChangedQuestion {
		t.Fatalf("identity reason = %q, want %q -- a byte-different question is outside this continuation rule EVEN WHEN canonicalisation considers it equivalent, and a guard that accepted canonical equality here would admit a reading the caller did not ask to continue",
			got, ContinuationReasonChangedQuestion)
	}
}
