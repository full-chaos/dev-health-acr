package contextfabric

import (
	"context"
	"errors"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// ORACLE O12 (design §13.11a) -- "the repair bound and the operand union
// are consistent (round 3 findings 4 and 9)".
//
// Both halves land in S7b-i. They are driven by a STUB repairer rather
// than a live model, deliberately: the repair BOUND is server-side
// arithmetic, and an oracle that needed a provider to run would be an
// oracle that does not run in CI.

// stubRepairer returns a fixed candidate and counts its calls, so "exactly
// one repair attempt" is observable rather than assumed.
type stubRepairer struct {
	candidate QuestionFrame
	err       error
	calls     int
	seen      FrameRepairRequest
}

func (s *stubRepairer) RepairFrame(_ context.Context, _ storage.Principal, request FrameRepairRequest) (QuestionFrame, error) {
	s.calls++
	s.seen = request
	if s.err != nil {
		return QuestionFrame{}, s.err
	}
	return s.candidate, nil
}

func discoveredTeamsEmphasisFrame(goals ...InvestigationGoal) QuestionFrame {
	return QuestionFrame{
		Goals: goals,
		SubjectExpression: SubjectExpression{
			Kind:       SubjectExpressionDiscoveredKind,
			Discovered: &DiscoveredSetExpression{MemberKind: contractsv1.ContextFabricSubjectTeam},
		},
		Emphasis: []AnswerEmphasis{EmphasisNegativeOutliers, EmphasisPositiveOutliers},
	}
}

// TestO12aRepairAddsRankOrSurveyAndNeverDropsEmphasis is O12(a) verbatim:
// "A frame {Goals={assess_state}, discovered_kind,
// Emphasis=[negative,positive]} fails I14, and the ONE permitted repair
// adds rank_or_survey and passes every invariant -- because I14 names
// Goals; a repair that drops Emphasis instead is refused as a narrowing."
//
// This is independent review R9's scenario. Under the design's FIRST
// drop-only repair rule the options were "silently discard Emphasis"
// (narrowing the answer the 12:42 08-31 paraphrase ruling says must be
// given) or "refuse" -- and WHICH one happened depended on the sampler's
// goal pick, reintroducing exactly the instability this design exists to
// remove. Because Goals is a SET, the repair can instead ADD
// rank_or_survey: a monotone widening under law L1 that satisfies I14
// without discarding anything the user asked for.
func TestO12aRepairAddsRankOrSurveyAndNeverDropsEmphasis(t *testing.T) {
	proposed := discoveredTeamsEmphasisFrame(GoalAssessState)

	t.Run("the widening repair is accepted", func(t *testing.T) {
		widened := discoveredTeamsEmphasisFrame(GoalAssessState, GoalRankOrSurvey)
		repairer := &stubRepairer{candidate: widened}

		result := ValidateAndRepairFrame(context.Background(), storage.Principal{OrgID: "org_test"}, repairer, proposed, nil, "", nil)

		if repairer.calls != 1 {
			t.Fatalf("repairer called %d times, want exactly 1 -- the bound is ONE attempt, not k", repairer.calls)
		}
		if repairer.seen.Failure.Invariant != FrameInvariantI14 {
			t.Fatalf("repairer was told invariant %q, want %q -- the repair is targeted at the FAILED invariant", repairer.seen.Failure.Invariant, FrameInvariantI14)
		}
		if result.Outcome != FrameValidationOutcomeRepaired {
			t.Fatalf("outcome = %q (invariant %q), want %q", result.Outcome, result.Failure.Invariant, FrameValidationOutcomeRepaired)
		}
		if !result.Frame.HasGoal(GoalRankOrSurvey) {
			t.Fatalf("repaired goals = %v, want rank_or_survey added", result.Frame.Goals)
		}
		if !result.Frame.HasGoal(GoalAssessState) {
			t.Fatalf("repaired goals = %v, want assess_state retained -- the repair may widen, never narrow", result.Frame.Goals)
		}
		if len(result.Frame.Emphasis) != 2 {
			t.Fatalf("repaired emphasis = %v, want both ends retained", result.Frame.Emphasis)
		}
		if !result.Frame.HasObligation(ObligationRanking) {
			t.Fatalf("repaired obligations = %v, want ranking derived so I14 is satisfied", result.Frame.Obligations)
		}
		// "and passes EVERY invariant" -- not merely the one that failed.
		if failure, bad := ValidateFramePhaseA1(result.Frame); bad {
			t.Fatalf("repaired frame fails A1 on %q", failure.Invariant)
		}
		if failure, bad := ValidateFramePhaseA2(result.Frame, ""); bad {
			t.Fatalf("repaired frame fails A2 on %q", failure.Invariant)
		}
	})

	t.Run("the emphasis-dropping repair is refused as a narrowing", func(t *testing.T) {
		narrowed := discoveredTeamsEmphasisFrame(GoalAssessState)
		narrowed.Emphasis = nil
		repairer := &stubRepairer{candidate: narrowed}

		result := ValidateAndRepairFrame(context.Background(), storage.Principal{OrgID: "org_test"}, repairer, proposed, nil, "", nil)

		if result.Outcome != FrameValidationOutcomeRefusedInvalid {
			t.Fatalf("outcome = %q, want %q -- discarding emphasis narrows the answer the paraphrase ruling requires", result.Outcome, FrameValidationOutcomeRefusedInvalid)
		}
		if result.ViolatedBound != FrameRepairBoundEmphasisNarrowed {
			t.Fatalf("violated bound = %q, want %q", result.ViolatedBound, FrameRepairBoundEmphasisNarrowed)
		}
		if result.Failure.Invariant != FrameInvariantI14 {
			t.Fatalf("refusal reports invariant %q, want the ORIGINAL failure %q", result.Failure.Invariant, FrameInvariantI14)
		}
	})
}

// TestO12bSubjectOperandUnionFailuresAreRecordedAsI19 is O12(b) verbatim:
// "A SubjectOperand with both pointers nil, both non-nil, or a pointer
// that disagrees with Kind fails I19 by name; a scoped operand with empty
// AnchorTerms fails I5 through I19; the failed invariant is recorded as
// i19."
//
// Round 3's finding 9 is why this exists: the frozen design added the
// operand TYPE without extending any invariant, so an operand with two
// optional pointers had no discriminator and no exactly-one rule, and I3
// (which validates only Named.Terms) could not be satisfied by a scoped
// operand at all.
func TestO12bSubjectOperandUnionFailuresAreRecordedAsI19(t *testing.T) {
	compareFrame := func(operands ...SubjectOperand) QuestionFrame {
		return QuestionFrame{
			Goals: []InvestigationGoal{GoalCompare},
			SubjectExpression: SubjectExpression{
				Kind:     SubjectExpressionExplicitSet,
				Explicit: &ExplicitSetExpression{Operands: operands},
			},
			Temporal: TemporalIntentCurrent,
		}
	}
	goodNamed := SubjectOperand{Kind: SubjectOperandNamed, Named: &NamedSubjectExpression{Terms: []string{"team a"}}}

	for _, testCase := range []struct {
		name    string
		operand SubjectOperand
		detail  FrameFailureDetail
	}{
		{
			name:    "both pointers nil",
			operand: SubjectOperand{Kind: SubjectOperandNamed},
			detail:  FrameFailureOperandNoVariant,
		},
		{
			name: "both pointers non-nil",
			operand: SubjectOperand{
				Kind:   SubjectOperandNamed,
				Named:  &NamedSubjectExpression{Terms: []string{"team b"}},
				Scoped: &ScopedSetExpression{AnchorTerms: []string{"team b"}, MemberKind: contractsv1.ContextFabricSubjectProject},
			},
			detail: FrameFailureOperandMultiVariant,
		},
		{
			name: "pointer disagrees with Kind",
			operand: SubjectOperand{
				Kind:   SubjectOperandNamed,
				Scoped: &ScopedSetExpression{AnchorTerms: []string{"team b"}, MemberKind: contractsv1.ContextFabricSubjectProject},
			},
			detail: FrameFailureOperandKindMismatch,
		},
		{
			name:    "operand Kind unset",
			operand: SubjectOperand{Named: &NamedSubjectExpression{Terms: []string{"team b"}}},
			detail:  FrameFailureOperandKindUnset,
		},
		{
			// "a scoped operand with empty AnchorTerms fails I5 through
			// I19; the failed invariant is recorded as i19."
			name: "scoped operand with empty AnchorTerms",
			operand: SubjectOperand{
				Kind:   SubjectOperandScoped,
				Scoped: &ScopedSetExpression{MemberKind: contractsv1.ContextFabricSubjectProject},
			},
			detail: FrameFailureOperandNoAnchor,
		},
		{
			name: "named operand with no terms",
			operand: SubjectOperand{
				Kind:  SubjectOperandNamed,
				Named: &NamedSubjectExpression{},
			},
			detail: FrameFailureOperandNoTerms,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			failure, bad := ValidateFramePhaseA1(compareFrame(goodNamed, testCase.operand))
			if !bad {
				t.Fatal("a malformed operand must fail phase A1")
			}
			if failure.Invariant != FrameInvariantI19 {
				t.Fatalf("failed invariant = %q, want %q -- operand failures are recorded as i19, never folded into i2 or i5",
					failure.Invariant, FrameInvariantI19)
			}
			if failure.Detail != testCase.detail {
				t.Fatalf("failure detail = %q, want %q", failure.Detail, testCase.detail)
			}
		})
	}

	// The negative control: a well-formed mixed-variant comparison passes,
	// which is the R10 closure the union was added for -- "compare team
	// A's PROJECTS with team B's PROJECTS" is expressible.
	mixed := compareFrame(
		goodNamed,
		SubjectOperand{Kind: SubjectOperandScoped, Scoped: &ScopedSetExpression{AnchorTerms: []string{"team b"}, MemberKind: contractsv1.ContextFabricSubjectProject}},
	)
	if failure, bad := ValidateFramePhaseA1(mixed); bad {
		t.Fatalf("a well-formed mixed-variant comparison must pass A1; failed on %q/%q", failure.Invariant, failure.Detail)
	}
}

// TestRepairBoundRefusesAnUnnamedKindChange pins the safety half of §13.6
// rule 2. The bound was LOOSENED after round 2 showed it made I7 and I9
// repairs structurally unreachable; this test is what stops the loosening
// from becoming "the repair may reinterpret the question".
func TestRepairBoundRefusesAnUnnamedKindChange(t *testing.T) {
	// I14 reads Goals, Emphasis and the derived obligations. It does NOT
	// name SubjectExpression.Kind, so a repair that changes the Kind is
	// out of bounds even though the frame really is invalid.
	proposed := discoveredTeamsEmphasisFrame(GoalAssessState)
	reinterpreted := QuestionFrame{
		Goals: []InvestigationGoal{GoalAssessState, GoalRankOrSurvey},
		SubjectExpression: SubjectExpression{
			Kind: SubjectExpressionGroupedMembers,
			Grouped: &GroupedSetExpression{
				GroupKind:  contractsv1.ContextFabricSubjectTeam,
				MemberKind: contractsv1.ContextFabricSubjectProject,
			},
		},
		Emphasis: []AnswerEmphasis{EmphasisNegativeOutliers, EmphasisPositiveOutliers},
	}
	repairer := &stubRepairer{candidate: reinterpreted}

	result := ValidateAndRepairFrame(context.Background(), storage.Principal{OrgID: "org_test"}, repairer, proposed, nil, "", nil)

	if result.Outcome != FrameValidationOutcomeRefusedKindChange {
		t.Fatalf("outcome = %q, want %q -- a Kind change on an invariant that does not name Kind is the repair talking itself into a different question",
			result.Outcome, FrameValidationOutcomeRefusedKindChange)
	}
	if result.ViolatedBound != FrameRepairBoundKindChanged {
		t.Fatalf("violated bound = %q, want %q", result.ViolatedBound, FrameRepairBoundKindChanged)
	}
}

// TestRepairBoundPermitsAKindChangeTheInvariantNames is the reachability
// half. Round 2's P2-1: pinning Kind absolutely made every I7 and I9
// failure unrepairable, so those frames always refused even when the
// misclassification was obviously repairable. The bound was TOO TIGHT,
// not too loose.
func TestRepairBoundPermitsAKindChangeTheInvariantNames(t *testing.T) {
	// I9 names Kind: "Goals ∋ count_or_aggregate ⇒ Kind ∈ {...}". A count
	// over a single named subject is repairable by moving the Kind to a
	// set-valued variant.
	proposed := QuestionFrame{
		Goals: []InvestigationGoal{GoalCountOrAggregate},
		SubjectExpression: SubjectExpression{
			Kind:  SubjectExpressionNamed,
			Named: &NamedSubjectExpression{Terms: []string{"fullchaos team"}},
		},
	}
	repaired := QuestionFrame{
		Goals: []InvestigationGoal{GoalCountOrAggregate},
		SubjectExpression: SubjectExpression{
			Kind: SubjectExpressionChildrenOfScope,
			Scoped: &ScopedSetExpression{
				AnchorTerms: []string{"fullchaos team"},
				MemberKind:  contractsv1.ContextFabricSubjectProject,
			},
		},
	}
	repairer := &stubRepairer{candidate: repaired}

	result := ValidateAndRepairFrame(context.Background(), storage.Principal{OrgID: "org_test"}, repairer, proposed, nil, "", nil)

	if result.Outcome != FrameValidationOutcomeRepaired {
		t.Fatalf("outcome = %q (bound %q), want repaired -- I9 names Kind, so the Kind repair is in bounds",
			result.Outcome, result.ViolatedBound)
	}
	if result.Frame.SubjectExpression.Kind != SubjectExpressionChildrenOfScope {
		t.Fatalf("repaired kind = %q, want children_of_scope", result.Frame.SubjectExpression.Kind)
	}
}

// TestRepairBoundRefusesAnUnnamedGoalRemoval pins the ASYMMETRY: adding a
// goal is permitted where the invariant names the goal axis; REMOVING one
// is permitted only when the invariant names that goal. Widening is safe;
// narrowing is the failure mode.
func TestRepairBoundRefusesAnUnnamedGoalRemoval(t *testing.T) {
	proposed := discoveredTeamsEmphasisFrame(GoalAssessState)
	// I14 reads Goals, so an ADD is in bounds -- but assess_state is not
	// a goal I14 NAMES, so removing it is not.
	stripped := discoveredTeamsEmphasisFrame(GoalRankOrSurvey)
	repairer := &stubRepairer{candidate: stripped}

	result := ValidateAndRepairFrame(context.Background(), storage.Principal{OrgID: "org_test"}, repairer, proposed, nil, "", nil)

	if result.ViolatedBound != FrameRepairBoundGoalRemoved {
		t.Fatalf("violated bound = %q, want %q -- dropping a goal the invariant does not name is a narrowing",
			result.ViolatedBound, FrameRepairBoundGoalRemoved)
	}
	if result.Outcome != FrameValidationOutcomeRefusedInvalid {
		t.Fatalf("outcome = %q, want %q", result.Outcome, FrameValidationOutcomeRefusedInvalid)
	}
}

// TestRepairBoundPermitsAGoalRemovalTheInvariantNames is its counterpart:
// I7's condition literally reads "Goals ∋ compare ⇒ Kind == explicit_set",
// so I7 NAMES compare and dropping it is the other legal repair for an I7
// failure.
func TestRepairBoundPermitsAGoalRemovalTheInvariantNames(t *testing.T) {
	proposed := QuestionFrame{
		Goals: []InvestigationGoal{GoalAssessState, GoalCompare},
		SubjectExpression: SubjectExpression{
			Kind:  SubjectExpressionNamed,
			Named: &NamedSubjectExpression{Terms: []string{"dev health ops"}},
		},
	}
	dropped := QuestionFrame{
		Goals: []InvestigationGoal{GoalAssessState},
		SubjectExpression: SubjectExpression{
			Kind:  SubjectExpressionNamed,
			Named: &NamedSubjectExpression{Terms: []string{"dev health ops"}},
		},
	}
	repairer := &stubRepairer{candidate: dropped}

	result := ValidateAndRepairFrame(context.Background(), storage.Principal{OrgID: "org_test"}, repairer, proposed, nil, "", nil)

	if result.Outcome != FrameValidationOutcomeRepaired {
		t.Fatalf("outcome = %q (bound %q), want repaired -- I7 names compare, so dropping it is in bounds",
			result.Outcome, result.ViolatedBound)
	}
	if result.Frame.HasGoal(GoalCompare) {
		t.Fatalf("repaired goals = %v, want compare dropped", result.Frame.Goals)
	}
}

// TestRepairBoundRefusesAKindChangeLicensedOnlyByAVariantRead is a
// SELF-FOUND defect, closed before review. It is recorded as such rather
// than quietly fixed.
//
// The first implementation computed "does this invariant name the Kind?"
// as `reads[subject_expression_kind] || reads[subject_expression_variant]`.
// Six phase-A1 invariants read the VARIANT (I2, I3, I4, I5, I6, I19), so
// every one of them silently licensed a WHOLESALE Kind change. I6's
// condition is about GroupKind and MemberKind -- fields INSIDE the grouped
// variant -- and says nothing about which variant the frame is; letting
// "you grouped a kind by itself" be repaired into a different topology is
// exactly the reinterpretation §13.6 rule 2 exists to forbid.
//
// The fix reads the discriminator alone. This test is RED against the
// pre-fix predicate.
func TestRepairBoundRefusesAKindChangeLicensedOnlyByAVariantRead(t *testing.T) {
	// I6: grouped_members with GroupKind == MemberKind.
	proposed := QuestionFrame{
		Goals: []InvestigationGoal{GoalAssessState},
		SubjectExpression: SubjectExpression{
			Kind: SubjectExpressionGroupedMembers,
			Grouped: &GroupedSetExpression{
				GroupKind:  contractsv1.ContextFabricSubjectTeam,
				MemberKind: contractsv1.ContextFabricSubjectTeam,
			},
		},
		Temporal: TemporalIntentCurrent,
	}
	failure, bad := ValidateFramePhaseA1(proposed)
	if !bad || failure.Invariant != FrameInvariantI6 {
		t.Fatalf("precondition: want an I6 failure, got %q/%v", failure.Invariant, bad)
	}

	// A "repair" that abandons the grouping entirely and answers a
	// single-subject question instead.
	reinterpreted := QuestionFrame{
		Goals: []InvestigationGoal{GoalAssessState},
		SubjectExpression: SubjectExpression{
			Kind:  SubjectExpressionNamed,
			Named: &NamedSubjectExpression{Terms: []string{"the team"}},
		},
		Temporal: TemporalIntentCurrent,
	}
	if violation := CheckFrameRepairBound(proposed, reinterpreted, failure); violation != FrameRepairBoundKindChanged {
		t.Fatalf("bound violation = %q, want %q -- I6 names GroupKind/MemberKind, not the union discriminator, so it may not license a topology change",
			violation, FrameRepairBoundKindChanged)
	}

	// The legitimate I6 repair -- correcting MemberKind inside the SAME
	// variant -- must still be accepted, or the bound is too tight again.
	corrected := QuestionFrame{
		Goals: []InvestigationGoal{GoalAssessState},
		SubjectExpression: SubjectExpression{
			Kind: SubjectExpressionGroupedMembers,
			Grouped: &GroupedSetExpression{
				GroupKind:  contractsv1.ContextFabricSubjectTeam,
				MemberKind: contractsv1.ContextFabricSubjectProject,
			},
		},
		Temporal: TemporalIntentCurrent,
	}
	if violation := CheckFrameRepairBound(proposed, corrected, failure); violation != FrameRepairBoundNone {
		t.Fatalf("bound violation = %q on the legitimate in-variant repair, want none", violation)
	}
}

// TestRepairBoundRefusesAnUnnamedVariantRewrite is the second half of the
// same self-found defect.
//
// §13.6 rule 2's FIRST sentence is the general rule -- "the repair call may
// only supply or correct the FIELDS THE FAILED INVARIANT NAMES" -- and the
// Goals/Kind clause is its most-cited instance, not its whole extent. The
// first implementation pinned only Goals, Kind, Emphasis, Dimensions and
// Temporal, which left the VARIANT'S OWN CONTENTS rewritable on any
// failure whose invariant does not read them. Rewriting the anchor terms
// of a question is talking yourself into a different question just as
// surely as changing the Kind is.
func TestRepairBoundRefusesAnUnnamedVariantRewrite(t *testing.T) {
	// I14 reads Goals, Emphasis and the derived obligations -- NOT the
	// subject expression.
	proposed := QuestionFrame{
		Goals: []InvestigationGoal{GoalAssessState},
		SubjectExpression: SubjectExpression{
			Kind: SubjectExpressionChildrenOfScope,
			Scoped: &ScopedSetExpression{
				AnchorTerms: []string{"fullchaos team"},
				MemberKind:  contractsv1.ContextFabricSubjectProject,
			},
		},
		Temporal: TemporalIntentCurrent,
		Emphasis: []AnswerEmphasis{EmphasisNegativeOutliers},
	}
	failure := FrameValidationFailure{Invariant: FrameInvariantI14, Phase: FrameValidationPhaseA2, Detail: FrameFailureEmphasisNeedsRanking}

	// Same Kind, same variant type -- a DIFFERENT team, and a different
	// member kind. A silently different question.
	rewritten := proposed
	rewritten.Goals = []InvestigationGoal{GoalAssessState, GoalRankOrSurvey}
	rewritten.SubjectExpression = SubjectExpression{
		Kind: SubjectExpressionChildrenOfScope,
		Scoped: &ScopedSetExpression{
			AnchorTerms: []string{"platform team"},
			MemberKind:  contractsv1.ContextFabricSubjectRepository,
		},
	}
	// The candidate both DROPS "fullchaos team" and ADDS "platform team".
	// The drop is reported, because it is the more serious direction and
	// is checked first: a repair that empties the subject leaves a
	// structurally valid frame pointing at nothing.
	if violation := CheckFrameRepairBound(proposed, rewritten, failure); violation != FrameRepairBoundSubjectPointerDropped {
		t.Fatalf("bound violation = %q, want %q -- rewriting an anchor drops the pointer the user actually wrote",
			violation, FrameRepairBoundSubjectPointerDropped)
	}

	// The same class with the pointers left ALONE, so the kind-valued
	// check is the one under test rather than the pointer rule: I14
	// constrains only the goal axis, so it may not touch the member kind.
	memberKindOnly := proposed
	memberKindOnly.Goals = []InvestigationGoal{GoalAssessState, GoalRankOrSurvey}
	memberKindOnly.SubjectExpression = SubjectExpression{
		Kind: SubjectExpressionChildrenOfScope,
		Scoped: &ScopedSetExpression{
			AnchorTerms: []string{"fullchaos team"},
			MemberKind:  contractsv1.ContextFabricSubjectRepository,
		},
	}
	if violation := CheckFrameRepairBound(proposed, memberKindOnly, failure); violation != FrameRepairBoundUnnamedFieldChanged {
		t.Fatalf("bound violation = %q, want %q -- I14 constrains the goal axis alone", violation, FrameRepairBoundUnnamedFieldChanged)
	}

	// The legitimate I14 repair -- add the goal, touch nothing else -- is
	// still accepted.
	widened := proposed
	widened.Goals = []InvestigationGoal{GoalAssessState, GoalRankOrSurvey}
	if violation := CheckFrameRepairBound(proposed, widened, failure); violation != FrameRepairBoundNone {
		t.Fatalf("bound violation = %q on the legitimate goal-widening repair, want none", violation)
	}
}

// TestRepairBoundRefusesAnUnnamedDimensionWidening. A repair that ADDS a
// dimension nobody asked about is not narrowing, but it is still answering
// a question the user did not ask -- and a dimension adds an obligation
// (§13.2.3 table 3), so it changes the plan.
func TestRepairBoundRefusesAnUnnamedDimensionWidening(t *testing.T) {
	proposed := discoveredTeamsEmphasisFrame(GoalAssessState)
	failure := FrameValidationFailure{Invariant: FrameInvariantI14, Phase: FrameValidationPhaseA2}

	widened := discoveredTeamsEmphasisFrame(GoalAssessState, GoalRankOrSurvey)
	widened.Dimensions = []HealthDimension{HealthDimensionInvestmentBalance}

	if violation := CheckFrameRepairBound(proposed, widened, failure); violation != FrameRepairBoundUnnamedFieldChanged {
		t.Fatalf("bound violation = %q, want %q", violation, FrameRepairBoundUnnamedFieldChanged)
	}
}

// TestRepairIsAttemptedExactlyOnce pins §13.6 rule 1. "Exactly one repair
// attempt. Not k. A second attempt is a refusal." A repairer that returns
// a still-invalid candidate must NOT be called again.
func TestRepairIsAttemptedExactlyOnce(t *testing.T) {
	proposed := discoveredTeamsEmphasisFrame(GoalAssessState)
	// The candidate keeps the same defect: still no ranking obligation.
	stillInvalid := discoveredTeamsEmphasisFrame(GoalAssessState)
	repairer := &stubRepairer{candidate: stillInvalid}

	result := ValidateAndRepairFrame(context.Background(), storage.Principal{OrgID: "org_test"}, repairer, proposed, nil, "", nil)

	if repairer.calls != 1 {
		t.Fatalf("repairer called %d times, want exactly 1", repairer.calls)
	}
	if result.Outcome != FrameValidationOutcomeRefusedInvalid {
		t.Fatalf("outcome = %q, want %q -- still invalid after one attempt is a refusal", result.Outcome, FrameValidationOutcomeRefusedInvalid)
	}
	if !result.RepairAttempted {
		t.Fatal("RepairAttempted = false after a repair ran -- B4's repair RATE would be unmeasurable")
	}
}

// TestRepairErrorRefusesRatherThanPassingThrough. A repairer error is a
// failure to repair, not a pass: the frame was and remains invalid.
func TestRepairErrorRefusesRatherThanPassingThrough(t *testing.T) {
	proposed := discoveredTeamsEmphasisFrame(GoalAssessState)
	repairer := &stubRepairer{err: errors.New("provider unavailable")}

	result := ValidateAndRepairFrame(context.Background(), storage.Principal{OrgID: "org_test"}, repairer, proposed, nil, "", nil)

	if result.Outcome != FrameValidationOutcomeRefusedInvalid {
		t.Fatalf("outcome = %q, want %q", result.Outcome, FrameValidationOutcomeRefusedInvalid)
	}
	if !result.RepairAttempted {
		t.Fatal("an errored repair still counts as attempted -- otherwise the repair rate's denominator is wrong")
	}
}

// TestRepairLatencyIsMeasured pins behaviour change B4's gate: "inside the
// reserved deadline; MEASURED repair rate + latency in the S7b-i gate". A
// latency that is never recorded is an extra model call nobody can bound.
func TestRepairLatencyIsMeasured(t *testing.T) {
	proposed := discoveredTeamsEmphasisFrame(GoalAssessState)
	repairer := &stubRepairer{candidate: discoveredTeamsEmphasisFrame(GoalAssessState, GoalRankOrSurvey)}

	base := time.Unix(1_700_000_000, 0)
	ticks := []time.Time{base, base.Add(250 * time.Millisecond)}
	index := 0
	clock := func() time.Time {
		value := ticks[index]
		if index < len(ticks)-1 {
			index++
		}
		return value
	}

	result := ValidateAndRepairFrame(context.Background(), storage.Principal{OrgID: "org_test"}, repairer, proposed, nil, "", clock)

	if result.RepairLatency != 250*time.Millisecond {
		t.Fatalf("repair latency = %s, want 250ms", result.RepairLatency)
	}
	event := FrameValidationEventFrom(proposed, result, "")
	if event.RepairLatencyMS != 250 {
		t.Fatalf("event repair_latency_ms = %d, want 250", event.RepairLatencyMS)
	}
	if !event.RepairAttempted {
		t.Fatal("event repair_attempted = false after a repair ran")
	}
}

// TestValidFrameEmitsTelemetryWithNoFailure is the "fires on EVERY frame
// reaching validation, INCLUDING valid ones" rule. An event that appears
// only on failure makes "the validator never rejects anything" and "the
// validator never ran" the same observation.
func TestValidFrameEmitsTelemetryWithNoFailure(t *testing.T) {
	proposed := namedFrame(GoalAssessState)
	result := ValidateAndRepairFrame(context.Background(), storage.Principal{OrgID: "org_test"}, nil, proposed, nil, "", nil)
	if result.Outcome != FrameValidationOutcomeValid {
		t.Fatalf("outcome = %q, want valid", result.Outcome)
	}
	event := FrameValidationEventFrom(proposed, result, "")
	if event.Outcome != FrameValidationOutcomeValid {
		t.Fatalf("event outcome = %q, want valid", event.Outcome)
	}
	if event.FailedInvariant != "" {
		t.Fatalf("event failed_invariant = %q on a valid frame, want empty", event.FailedInvariant)
	}
	if event.DerivedObligationCount == 0 {
		t.Fatal("event derived_obligation_count = 0 on a valid frame")
	}
	if event.FrameVersion != QuestionFrameVersion {
		t.Fatalf("event frame_version = %q, want %q", event.FrameVersion, QuestionFrameVersion)
	}
	if len(event.ProposedGoals) != 1 || event.ProposedGoals[0] != GoalAssessState {
		t.Fatalf("event proposed_goals = %v, want [assess_state] -- §13.2.4 rule 3's governance depends on this field", event.ProposedGoals)
	}
}

// TestWidenedObligationsAreAdvisoryAndNeverNarrow pins §13.2.4's two
// safe-direction rules at the type level.
func TestWidenedObligationsAreAdvisoryAndNeverNarrow(t *testing.T) {
	frame := NormalizeFrame(namedFrame(GoalAssessState))
	base := DeriveFrameObligations(frame, nil)

	// A model emits a spurious `ranking` obligation on a plain
	// current-state question, plus a member the server already derived.
	widened := DeriveFrameObligations(frame, []AnswerObligation{ObligationRanking, ObligationState})

	if len(widened.Obligations) != len(base.Obligations) {
		t.Fatalf("derived obligations moved under a widening: %v -> %v -- the model may never change the DERIVED set",
			base.Obligations, widened.Obligations)
	}
	requiredness, ok := widened.Requiredness(ObligationRanking)
	if !ok || requiredness != RequirednessAdvisory {
		t.Fatalf("widened ranking requiredness = %q/%v, want advisory", requiredness, ok)
	}
	// A member the server already derived stays REQUIRED and does not
	// also appear as advisory.
	requiredness, ok = widened.Requiredness(ObligationState)
	if !ok || requiredness != RequirednessRequired {
		t.Fatalf("state requiredness = %q/%v, want required", requiredness, ok)
	}
	for _, member := range widened.WidenedObligations {
		if member == ObligationState {
			t.Fatal("a derived obligation was also recorded as widened -- one member cannot be both required and advisory")
		}
	}
	// HasObligation must NOT see the advisory member: an advisory
	// obligation satisfying I14 would let a model emission discharge an
	// invariant that exists to check a DERIVED value.
	if widened.HasObligation(ObligationRanking) {
		t.Fatal("HasObligation returned true for a model-widened obligation")
	}
}

// TestRepairBoundRefusesARetargetedSubjectOnAPermittedKindMove is codex
// round 2's first finding, reproduced and closed.
//
// The previous rule skipped the variant comparison entirely whenever the
// Kind move itself was permitted, on the (correct) ground that changing
// the discriminator necessarily rewrites the variant. Correct, and too
// broad: it excused the whole payload. The executed counterexample was a
// count over `named_subject("team a")` failing I9 and coming back as
// `children_of_scope(anchor_terms=["platform team"], member_kind=project)`
// -- ACCEPTED. I9's condition is "a count needs a set-valued kind"; it
// names Goals and the Kind and says nothing about WHICH subject, so
// deciding that is not a repair, it is a different question.
//
// The rule now: a repair may RE-TYPE the structure, never RE-TARGET it.
// Every retrieval pointer in the repaired expression must already have
// been in the proposed one.
func TestRepairBoundRefusesARetargetedSubjectOnAPermittedKindMove(t *testing.T) {
	proposed := QuestionFrame{
		Goals: []InvestigationGoal{GoalCountOrAggregate},
		SubjectExpression: SubjectExpression{
			Kind:  SubjectExpressionNamed,
			Named: &NamedSubjectExpression{Terms: []string{"team a"}},
		},
	}
	failure, bad := ValidateFramePhaseA1(proposed)
	if !bad || failure.Invariant != FrameInvariantI9 {
		t.Fatalf("precondition: want an I9 failure, got %q/%v", failure.Invariant, bad)
	}

	retargeted := QuestionFrame{
		Goals: []InvestigationGoal{GoalCountOrAggregate},
		SubjectExpression: SubjectExpression{
			Kind: SubjectExpressionChildrenOfScope,
			Scoped: &ScopedSetExpression{
				AnchorTerms: []string{"platform team"},
				MemberKind:  contractsv1.ContextFabricSubjectProject,
			},
		},
	}
	// This candidate both DROPS "team a" and ADDS "platform team"; the
	// drop is the reported violation, and it is the one that matters most
	// -- review's executed repro for the deletion case returned a usable
	// repaired frame with no subject pointers at all.
	if violation := CheckFrameRepairBound(proposed, retargeted, failure); violation != FrameRepairBoundSubjectPointerDropped {
		t.Fatalf("bound violation = %q, want %q", violation, FrameRepairBoundSubjectPointerDropped)
	}

	// PURE ADDITION, so retargeting is isolated from dropping: the
	// original pointer is KEPT and a second one appears. I9 constrains the
	// goal axis and the Kind, neither of which carries pointers, so the
	// addition is refused.
	augmented := QuestionFrame{
		Goals: []InvestigationGoal{GoalCountOrAggregate},
		SubjectExpression: SubjectExpression{
			Kind: SubjectExpressionExplicitSet,
			Explicit: &ExplicitSetExpression{Operands: []SubjectOperand{
				{Kind: SubjectOperandNamed, Named: &NamedSubjectExpression{Terms: []string{"team a"}}},
				{Kind: SubjectOperandNamed, Named: &NamedSubjectExpression{Terms: []string{"platform team"}}},
			}},
		},
	}
	if violation := CheckFrameRepairBound(proposed, augmented, failure); violation != FrameRepairBoundSubjectRetargeted {
		t.Fatalf("bound violation = %q, want %q -- a repair for \"a count needs a set-valued kind\" may not decide WHICH subject is counted",
			violation, FrameRepairBoundSubjectRetargeted)
	}

	// And the DELETION case review executed: every pointer gone, returning
	// a structurally valid frame that points at nothing. A subset check
	// alone accepted this.
	emptied := QuestionFrame{
		Goals: []InvestigationGoal{GoalCountOrAggregate},
		SubjectExpression: SubjectExpression{
			Kind: SubjectExpressionGroupedMembers,
			Grouped: &GroupedSetExpression{
				GroupKind:  contractsv1.ContextFabricSubjectTeam,
				MemberKind: contractsv1.ContextFabricSubjectProject,
			},
		},
	}
	if violation := CheckFrameRepairBound(proposed, emptied, failure); violation != FrameRepairBoundSubjectPointerDropped {
		t.Fatalf("bound violation = %q, want %q -- a repair may not empty the subject", violation, FrameRepairBoundSubjectPointerDropped)
	}

	// The legitimate repair REDISTRIBUTES the pointer it was given: the
	// named subject's term becomes the scoped set's anchor, and the new
	// variant supplies the member kind the old one had nowhere to put.
	// Subset, not equality, is what keeps this reachable.
	redistributed := QuestionFrame{
		Goals: []InvestigationGoal{GoalCountOrAggregate},
		SubjectExpression: SubjectExpression{
			Kind: SubjectExpressionChildrenOfScope,
			Scoped: &ScopedSetExpression{
				AnchorTerms: []string{"team a"},
				MemberKind:  contractsv1.ContextFabricSubjectProject,
			},
		},
	}
	if violation := CheckFrameRepairBound(proposed, redistributed, failure); violation != FrameRepairBoundNone {
		t.Fatalf("bound violation = %q on the legitimate redistribution, want none -- pinning this too tight is how round 2 made I7 and I9 repairs unreachable", violation)
	}
	result := ValidateAndRepairFrame(context.Background(), storage.Principal{OrgID: "org_test"},
		&stubRepairer{candidate: redistributed}, proposed, nil, "", nil)
	if result.Outcome != FrameValidationOutcomeRepaired {
		t.Fatalf("outcome = %q (bound %q), want repaired", result.Outcome, result.ViolatedBound)
	}
}

// TestFrameVersionIsAlwaysTheServerConstant is codex round 2's second
// finding.
//
// NormalizeFrame defaulted the version only when absent, so a non-empty
// proposed version survived into the validated frame, the receipt and the
// `frame_version` log field. Two things wrong at once: free text reached a
// closed telemetry field, and a model or repairer could FALSIFY which
// derivation table produced a frame -- the one claim the version exists to
// make.
func TestFrameVersionIsAlwaysTheServerConstant(t *testing.T) {
	const forged = "zzz-confidential-frame-version"
	proposed := namedFrame(GoalAssessState)
	proposed.Version = forged

	result := ValidateAndRepairFrame(context.Background(), storage.Principal{OrgID: "org_test"}, nil, proposed, nil, "", nil)
	if result.Outcome != FrameValidationOutcomeValid {
		t.Fatalf("outcome = %q, want valid", result.Outcome)
	}
	if result.Frame.Version != QuestionFrameVersion {
		t.Fatalf("validated frame version = %q, want %q -- the version is SERVER-DERIVED and stamped unconditionally, never defaulted",
			result.Frame.Version, QuestionFrameVersion)
	}
	event := FrameValidationEventFrom(proposed, result, "")
	if event.FrameVersion != QuestionFrameVersion {
		t.Fatalf("event frame_version = %q, want %q", event.FrameVersion, QuestionFrameVersion)
	}
}

// TestGoalsAreCanonicalizedIntoASet is codex round 2's third finding.
//
// Goals are documented as a SET in vocabulary order, but only the emission
// boundary's sanitizer produced that shape: a frame built directly kept
// duplicates and emission order, validated as `valid`, and was persisted
// and logged verbatim. Beyond the representation inconsistency, the family
// derivation of the NEXT slice is specified as a pure function of the
// frame, and a function whose input can differ by order is not one.
func TestGoalsAreCanonicalizedIntoASet(t *testing.T) {
	proposed := QuestionFrame{
		Goals: []InvestigationGoal{GoalRankOrSurvey, GoalAssessState, GoalRankOrSurvey},
		SubjectExpression: SubjectExpression{
			Kind:       SubjectExpressionDiscoveredKind,
			Discovered: &DiscoveredSetExpression{MemberKind: contractsv1.ContextFabricSubjectTeam},
		},
	}
	result := ValidateAndRepairFrame(context.Background(), storage.Principal{OrgID: "org_test"}, nil, proposed, nil, "", nil)
	if result.Outcome != FrameValidationOutcomeValid {
		t.Fatalf("outcome = %q, want valid", result.Outcome)
	}
	assertExactGoals(t, result.Frame.Goals, []InvestigationGoal{GoalAssessState, GoalRankOrSurvey})

	// Two orderings of one goal set must produce the identical frame --
	// this is the property the family derivation will rest on.
	other := proposed
	other.Goals = []InvestigationGoal{GoalAssessState, GoalRankOrSurvey}
	otherResult := ValidateAndRepairFrame(context.Background(), storage.Principal{OrgID: "org_test"}, nil, other, nil, "", nil)
	assertExactGoals(t, otherResult.Frame.Goals, result.Frame.Goals)

	// Canonicalization must NOT swallow an out-of-vocabulary member --
	// that is I15's failure to name, and dropping it here would take the
	// failure away from the invariant that reports it.
	invalid := proposed
	invalid.Goals = []InvestigationGoal{GoalAssessState, InvestigationGoal("zzz-not-a-goal")}
	failure, bad := ValidateFramePhaseA1(NormalizeFrame(invalid))
	if !bad || failure.Invariant != FrameInvariantI15 {
		t.Fatalf("canonicalization hid an out-of-vocabulary goal: failure = %q/%v", failure.Invariant, bad)
	}
}

// TestRoundThreeShapeSweep is a table of concrete REPAIR SCENARIOS, kept
// as regression cases for the specific defects rounds 1-4 found.
//
// IT IS NOT THE SWEEP ANY MORE. The sweep is generated from the derived
// path set in chaos4452_frame_sweep_test.go, because a hand-listed axis
// table is exactly what let four rounds find one defect at four depths --
// this table's own I2 rows exercised term replacement and addition and
// never a nested discriminator change, which is how round 4 got in. These
// rows survive as worked examples with their negative controls; the
// coverage claim belongs to the generated sweep.
//
// THE CLASS, stated once: "the repair bound permits rewriting subject
// payload the failed invariant does not actually constrain." It was found
// three times in three disguises -- by the lane (a variant read licensing
// a discriminator change), by review round 2 (a permitted Kind move
// excusing the whole payload), and by review round 3 (I2 and I3 declaring
// the whole variant as read while constraining only the operand count and
// the terms). Every previous fix was per-instance, which is exactly the
// pattern that produces re-finds.
//
// The general fix is the Reads/Constrains split: the bound now quantifies
// over what each invariant's CONDITION constrains, not over what it reads.
// This test sweeps EVERY phase-A invariant against EVERY axis a repair
// could touch, so a future edit that widens one Constrains list has to
// justify itself here.
func TestRoundThreeShapeSweep(t *testing.T) {
	named := func(terms ...string) SubjectExpression {
		return SubjectExpression{Kind: SubjectExpressionNamed, Named: &NamedSubjectExpression{Terms: terms}}
	}

	for _, testCase := range []struct {
		name      string
		invariant FrameInvariant
		proposed  QuestionFrame
		repaired  QuestionFrame
		want      FrameRepairBoundViolation
	}{
		{
			// Round 3, finding 1a. I2's condition is len(Operands) >= 2 --
			// a COUNT. It may not license replacing an operand's subject.
			name:      "I2 may add an operand but not replace one",
			invariant: FrameInvariantI2,
			proposed: QuestionFrame{Goals: []InvestigationGoal{GoalCompare}, SubjectExpression: SubjectExpression{
				Kind:     SubjectExpressionExplicitSet,
				Explicit: &ExplicitSetExpression{Operands: []SubjectOperand{{Kind: SubjectOperandNamed, Named: &NamedSubjectExpression{Terms: []string{"team a"}}}}},
			}},
			repaired: QuestionFrame{Goals: []InvestigationGoal{GoalCompare}, SubjectExpression: SubjectExpression{
				Kind: SubjectExpressionExplicitSet,
				Explicit: &ExplicitSetExpression{Operands: []SubjectOperand{
					{Kind: SubjectOperandNamed, Named: &NamedSubjectExpression{Terms: []string{"platform"}}},
					{Kind: SubjectOperandNamed, Named: &NamedSubjectExpression{Terms: []string{"team b"}}},
				}},
			}},
			want: FrameRepairBoundSubjectPointerDropped,
		},
		{
			name:      "I2's legitimate repair adds a second operand and keeps the first",
			invariant: FrameInvariantI2,
			proposed: QuestionFrame{Goals: []InvestigationGoal{GoalCompare}, SubjectExpression: SubjectExpression{
				Kind:     SubjectExpressionExplicitSet,
				Explicit: &ExplicitSetExpression{Operands: []SubjectOperand{{Kind: SubjectOperandNamed, Named: &NamedSubjectExpression{Terms: []string{"team a"}}}}},
			}},
			repaired: QuestionFrame{Goals: []InvestigationGoal{GoalCompare}, SubjectExpression: SubjectExpression{
				Kind: SubjectExpressionExplicitSet,
				Explicit: &ExplicitSetExpression{Operands: []SubjectOperand{
					{Kind: SubjectOperandNamed, Named: &NamedSubjectExpression{Terms: []string{"team a"}}},
					{Kind: SubjectOperandNamed, Named: &NamedSubjectExpression{Terms: []string{"team b"}}},
				}},
			}},
			want: FrameRepairBoundNone,
		},
		{
			// Round 3, finding 1b. I3's condition is "the named subject has
			// terms". It says nothing about the expected KIND.
			name:      "I3 may supply terms but not change ExpectedKind",
			invariant: FrameInvariantI3,
			proposed: QuestionFrame{Goals: []InvestigationGoal{GoalAssessState}, SubjectExpression: SubjectExpression{
				Kind: SubjectExpressionNamed, Named: &NamedSubjectExpression{ExpectedKind: kindPtr(contractsv1.ContextFabricSubjectTeam)},
			}},
			repaired: QuestionFrame{Goals: []InvestigationGoal{GoalAssessState}, SubjectExpression: SubjectExpression{
				Kind: SubjectExpressionNamed, Named: &NamedSubjectExpression{
					Terms: []string{"dev health ops"}, ExpectedKind: kindPtr(contractsv1.ContextFabricSubjectProject),
				},
			}},
			want: FrameRepairBoundUnnamedFieldChanged,
		},
		{
			name:      "I3's legitimate repair supplies the missing terms and nothing else",
			invariant: FrameInvariantI3,
			proposed: QuestionFrame{Goals: []InvestigationGoal{GoalAssessState}, SubjectExpression: SubjectExpression{
				Kind: SubjectExpressionNamed, Named: &NamedSubjectExpression{ExpectedKind: kindPtr(contractsv1.ContextFabricSubjectTeam)},
			}},
			repaired: QuestionFrame{Goals: []InvestigationGoal{GoalAssessState}, SubjectExpression: SubjectExpression{
				Kind: SubjectExpressionNamed, Named: &NamedSubjectExpression{
					Terms: []string{"dev health ops"}, ExpectedKind: kindPtr(contractsv1.ContextFabricSubjectTeam),
				},
			}},
			want: FrameRepairBoundNone,
		},
		{
			name:      "I4 constrains the member kind alone, not the terms",
			invariant: FrameInvariantI4,
			proposed:  QuestionFrame{Goals: []InvestigationGoal{GoalAssessState}, SubjectExpression: named("team a")},
			repaired:  QuestionFrame{Goals: []InvestigationGoal{GoalAssessState}, SubjectExpression: named("team a", "platform")},
			want:      FrameRepairBoundSubjectRetargeted,
		},
		{
			name:      "I15 constrains the goal axis alone, not the subject",
			invariant: FrameInvariantI15,
			proposed:  QuestionFrame{SubjectExpression: named("team a")},
			repaired:  QuestionFrame{Goals: []InvestigationGoal{GoalAssessState}, SubjectExpression: named("platform")},
			want:      FrameRepairBoundSubjectPointerDropped,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			failure := FrameValidationFailure{Invariant: testCase.invariant, Phase: FrameValidationPhaseA1}
			if violation := CheckFrameRepairBound(testCase.proposed, testCase.repaired, failure); violation != testCase.want {
				t.Fatalf("bound violation = %q, want %q", violation, testCase.want)
			}
		})
	}
}

// TestEveryInvariantConstrainsOnlyDeclaredPaths is the structural guard on
// the Reads/Constrains split, now stated over the DERIVED path tree.
//
// Every constrained path must be a real path of the frame's type tree — a
// typo, or a path left behind by a renamed field, would silently constrain
// nothing and quietly widen the bound. And the derived-value and
// resolution-phase invariants must constrain NOTHING: an obligation set is
// an outcome of the frame rather than a field of it, and repair is never
// invoked for a phase-B or phase-C failure at all.
func TestEveryInvariantConstrainsOnlyDeclaredPaths(t *testing.T) {
	valid := map[FramePath]bool{}
	for _, path := range FramePaths() {
		valid[path] = true
	}
	for _, spec := range FrameInvariantSpecs() {
		for _, path := range spec.Constrains {
			if !valid[path] {
				t.Errorf("invariant %q constrains %q, which is not a path of the frame type tree", spec.ID, path)
			}
			if derivedFramePath(path) {
				t.Errorf("invariant %q constrains derived value %q", spec.ID, path)
			}
		}
		switch spec.Phase {
		case FrameValidationPhaseB, FrameValidationPhaseC:
			if len(spec.Constrains) != 0 {
				t.Errorf("phase-%s invariant %q constrains %v -- repair is never invoked for a resolution or evidence failure", spec.Phase, spec.ID, spec.Constrains)
			}
		}
	}
	for _, id := range []FrameInvariant{FrameInvariantI10, FrameInvariantI16, FrameInvariantI18} {
		spec, _ := frameInvariantSpec(id)
		if len(spec.Constrains) != 0 {
			t.Errorf("derived-value invariant %q constrains %v -- there is no field for a repair to write", id, spec.Constrains)
		}
	}
}

// TestEmphasisAndDimensionsAreCanonicalizedIntoSets is round 3's finding 3
// -- the same defect the goal axis had, one field over. All three
// set-valued axes are canonicalized in one place now, rather than each
// where it happened to be noticed.
func TestEmphasisAndDimensionsAreCanonicalizedIntoSets(t *testing.T) {
	frame := QuestionFrame{
		Goals: []InvestigationGoal{GoalRankOrSurvey},
		SubjectExpression: SubjectExpression{
			Kind:       SubjectExpressionDiscoveredKind,
			Discovered: &DiscoveredSetExpression{MemberKind: contractsv1.ContextFabricSubjectTeam},
		},
		Emphasis:   []AnswerEmphasis{EmphasisPositiveOutliers, EmphasisNegativeOutliers, EmphasisPositiveOutliers},
		Dimensions: []HealthDimension{HealthDimensionInvestmentBalance, HealthDimensionExecutionCompletion, HealthDimensionInvestmentBalance},
	}
	normalized := NormalizeFrame(frame)

	if len(normalized.Emphasis) != 2 || normalized.Emphasis[0] != EmphasisPositiveOutliers || normalized.Emphasis[1] != EmphasisNegativeOutliers {
		t.Errorf("emphasis = %v, want the deduplicated pair in vocabulary order", normalized.Emphasis)
	}
	if len(normalized.Dimensions) != 2 || normalized.Dimensions[0] != HealthDimensionExecutionCompletion || normalized.Dimensions[1] != HealthDimensionInvestmentBalance {
		t.Errorf("dimensions = %v, want the deduplicated pair in published order", normalized.Dimensions)
	}
	// A duplicate dimension produced a DUPLICATE AXIS DISCHARGE, so the
	// set property is not cosmetic here.
	discharges := FrameAxisDischarges(DeriveFrameObligations(normalized, nil))
	seen := map[string]int{}
	for _, discharge := range discharges {
		if discharge.Axis == AxisDimension {
			seen[discharge.Value]++
		}
	}
	for value, count := range seen {
		if count != 1 {
			t.Errorf("dimension %q produced %d discharges, want 1", value, count)
		}
	}
}

// TestRepairMayNotRewriteAWellFormedOperand is round 4's finding, and it
// is the SAME CLASS as rounds 2 and 3 one level further down: a blunt
// field token covering more than the invariant's condition does.
//
// `FrameFieldOperands` covered the operand COUNT (which I2 constrains) and
// every operand's discriminator, member kind and expected kind (which it
// does not). Review's executed repro changed an existing, well-formed
// `named_subject("team a")` operand into
// `children_of_scope(anchor "team a", member project)`. The TERM STRING is
// preserved, so the pointer rule let it through — and the question turned
// from "how is team A doing" into "how are team A's projects doing".
//
// Review also answered the sweep question the prompt asked: the sweep's I2
// rows exercised only term replacement and addition, never a nested
// discriminator change. That gap is closed by the rows below.
func TestRepairMayNotRewriteAWellFormedOperand(t *testing.T) {
	wellFormed := SubjectOperand{
		Kind:  SubjectOperandNamed,
		Named: &NamedSubjectExpression{Terms: []string{"team a"}, ExpectedKind: kindPtr(contractsv1.ContextFabricSubjectTeam)},
	}
	proposed := QuestionFrame{
		Goals: []InvestigationGoal{GoalCompare},
		SubjectExpression: SubjectExpression{
			Kind:     SubjectExpressionExplicitSet,
			Explicit: &ExplicitSetExpression{Operands: []SubjectOperand{wellFormed}},
		},
	}
	failure, bad := ValidateFramePhaseA1(proposed)
	if !bad || failure.Invariant != FrameInvariantI2 {
		t.Fatalf("precondition: want an I2 failure, got %q/%v", failure.Invariant, bad)
	}

	// THE DEFECT: the existing operand's TOPOLOGY changes while its term
	// survives, so the pointer rule is satisfied and the question is not.
	retyped := QuestionFrame{
		Goals: []InvestigationGoal{GoalCompare},
		SubjectExpression: SubjectExpression{
			Kind: SubjectExpressionExplicitSet,
			Explicit: &ExplicitSetExpression{Operands: []SubjectOperand{
				{Kind: SubjectOperandScoped, Scoped: &ScopedSetExpression{AnchorTerms: []string{"team a"}, MemberKind: contractsv1.ContextFabricSubjectProject}},
				{Kind: SubjectOperandNamed, Named: &NamedSubjectExpression{Terms: []string{"team b"}}},
			}},
		},
	}
	if violation := CheckFrameRepairBound(proposed, retyped, failure); violation != FrameRepairBoundOperandRewritten {
		t.Fatalf("bound violation = %q, want %q -- I2 constrains how MANY operands there are, never what an existing one IS",
			violation, FrameRepairBoundOperandRewritten)
	}

	// Changing only the expected KIND of an existing operand is the same
	// class with the discriminator left alone.
	kindSwapped := QuestionFrame{
		Goals: []InvestigationGoal{GoalCompare},
		SubjectExpression: SubjectExpression{
			Kind: SubjectExpressionExplicitSet,
			Explicit: &ExplicitSetExpression{Operands: []SubjectOperand{
				{Kind: SubjectOperandNamed, Named: &NamedSubjectExpression{Terms: []string{"team a"}, ExpectedKind: kindPtr(contractsv1.ContextFabricSubjectProject)}},
				{Kind: SubjectOperandNamed, Named: &NamedSubjectExpression{Terms: []string{"team b"}}},
			}},
		},
	}
	if violation := CheckFrameRepairBound(proposed, kindSwapped, failure); violation != FrameRepairBoundOperandRewritten {
		t.Fatalf("bound violation = %q, want %q", violation, FrameRepairBoundOperandRewritten)
	}

	// I2's LEGITIMATE repair: the existing operand is untouched and a
	// second one appears. Both must stay reachable, or the bound is too
	// tight again.
	augmented := QuestionFrame{
		Goals: []InvestigationGoal{GoalCompare},
		SubjectExpression: SubjectExpression{
			Kind: SubjectExpressionExplicitSet,
			Explicit: &ExplicitSetExpression{Operands: []SubjectOperand{
				wellFormed,
				{Kind: SubjectOperandNamed, Named: &NamedSubjectExpression{Terms: []string{"team b"}}},
			}},
		},
	}
	if violation := CheckFrameRepairBound(proposed, augmented, failure); violation != FrameRepairBoundNone {
		t.Fatalf("bound violation = %q on I2's legitimate repair, want none", violation)
	}

	// I19's LEGITIMATE repair: the MALFORMED operand is corrected, which
	// is exactly what a repair is for. A frozen-operand rule that also
	// froze the broken one would make I19 unrepairable.
	malformed := QuestionFrame{
		Goals: []InvestigationGoal{GoalCompare},
		SubjectExpression: SubjectExpression{
			Kind: SubjectExpressionExplicitSet,
			Explicit: &ExplicitSetExpression{Operands: []SubjectOperand{
				wellFormed,
				{Kind: SubjectOperandScoped, Scoped: &ScopedSetExpression{AnchorTerms: []string{"team b"}}}, // no member kind
			}},
		},
	}
	i19Failure, bad := ValidateFramePhaseA1(malformed)
	if !bad || i19Failure.Invariant != FrameInvariantI19 {
		t.Fatalf("precondition: want an I19 failure, got %q/%v", i19Failure.Invariant, bad)
	}
	corrected := QuestionFrame{
		Goals: []InvestigationGoal{GoalCompare},
		SubjectExpression: SubjectExpression{
			Kind: SubjectExpressionExplicitSet,
			Explicit: &ExplicitSetExpression{Operands: []SubjectOperand{
				wellFormed,
				{Kind: SubjectOperandScoped, Scoped: &ScopedSetExpression{AnchorTerms: []string{"team b"}, MemberKind: contractsv1.ContextFabricSubjectProject}},
			}},
		},
	}
	if violation := CheckFrameRepairBound(malformed, corrected, i19Failure); violation != FrameRepairBoundNone {
		t.Fatalf("bound violation = %q on I19's legitimate repair, want none -- the malformed operand is precisely what may be corrected", violation)
	}
}
