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
