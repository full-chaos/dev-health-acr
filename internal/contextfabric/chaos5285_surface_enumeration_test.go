package contextfabric

import (
	"fmt"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// TestEveryGroupKindByMemberKindPairIsDecidedAtTheFrameGate sweeps the WHOLE
// input surface of the self-group invariant rather than the pairs an author
// happened to think of.
//
// The surface is every ordered pair of the closed subject-kind vocabulary,
// enumerated FROM THE PRODUCER (ContextFabricSubjectKindVocabulary) so a kind
// added to the vocabulary tomorrow is swept the day it lands. A hand list of
// interesting pairs is exactly the instrument this repository's rules refuse:
// it can only ever cover inputs its author chose, and the pair that breaks is
// by definition the one nobody thought of.
//
// The property is an EQUIVALENCE, not an implication, and both directions
// matter. Refusing when the kinds differ deletes the grouped family; failing
// to refuse when they match is the defect this ticket exists to close. A pin
// asserting only one direction is satisfied by a mutant that always refuses.
func TestEveryGroupKindByMemberKindPairIsDecidedAtTheFrameGate(t *testing.T) {
	t.Parallel()

	kinds := contractsv1.ContextFabricSubjectKindVocabulary()
	if len(kinds) == 0 {
		t.Fatal("the subject-kind vocabulary is empty, so this sweep covers nothing")
	}

	var swept, refused, admitted int
	for _, groupKind := range kinds {
		for _, memberKind := range kinds {
			groupKind, memberKind := groupKind, memberKind
			t.Run(fmt.Sprintf("%s_by_%s", memberKind, groupKind), func(t *testing.T) {
				frame := QuestionFrame{
					Version: QuestionFrameVersion,
					SubjectExpression: SubjectExpression{
						Kind:    SubjectExpressionGroupedMembers,
						Grouped: &GroupedSetExpression{GroupKind: groupKind, MemberKind: memberKind},
					},
					Obligations: []AnswerObligation{ObligationState},
					Goals:       []InvestigationGoal{GoalAssessState},
					Temporal:    TemporalIntentCurrent,
				}
				result := ValidateFrame(frame, nil, ShapeDiscoveredCohort)
				selfGroup := groupKind == memberKind
				i6Refused := result.Outcome != FrameValidationOutcomeValid &&
					result.Failure.Invariant == FrameInvariantI6 &&
					result.Failure.Detail == FrameFailureGroupEqualsMember

				if selfGroup && !i6Refused {
					t.Errorf("group=%q member=%q is a self-group and was NOT refused by i6 (outcome=%q invariant=%q detail=%q) -- the invariant has a hole at this pair",
						groupKind, memberKind, result.Outcome, result.Failure.Invariant, result.Failure.Detail)
				}
				if !selfGroup && i6Refused {
					t.Errorf("group=%q member=%q names two DIFFERENT kinds and was refused by i6 -- the invariant is over-firing and this pair's grouped answers are unreachable",
						groupKind, memberKind)
				}
			})
			swept++
			if groupKind == memberKind {
				refused++
			} else {
				admitted++
			}
		}
	}
	// The sweep's own census, so a future edit that silently stops
	// enumerating cannot look like a pass.
	t.Logf("swept %d ordered pairs from a %d-member vocabulary: %d self-group, %d cross-kind",
		swept, len(kinds), refused, admitted)
	if swept != len(kinds)*len(kinds) {
		t.Fatalf("swept %d pairs, want %d -- the enumeration is not covering the vocabulary it reads", swept, len(kinds)*len(kinds))
	}
	if refused == 0 || admitted == 0 {
		t.Fatalf("the sweep produced %d self-group and %d cross-kind pairs -- a sweep with an empty arm cannot discriminate", refused, admitted)
	}
}

// TestTheGroupBoundIsDecidedAtEveryBoundary enumerates the boundary of
// ContextFabricCohortGroupsMaxCount rather than testing one comfortable value
// on each side.
//
// A bound tested far from its edge cannot tell `>` from `>=`, and the two
// differ by exactly one group for every caller. The cases are derived FROM THE
// CONSTANT, so the sweep follows it if it ever moves.
func TestTheGroupBoundIsDecidedAtEveryBoundary(t *testing.T) {
	t.Parallel()

	bound := contractsv1.ContextFabricCohortGroupsMaxCount
	if bound < 2 {
		t.Fatalf("the bound is %d, so there is no boundary to enumerate", bound)
	}
	cases := []struct {
		groups    int
		wantValid bool
	}{
		{0, true},
		{1, true},
		{bound - 1, true},
		{bound, true},
		{bound + 1, false},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(fmt.Sprintf("%d_groups", testCase.groups), func(t *testing.T) {
			t.Parallel()
			cohort := contractsv1.ContextFabricCohort{
				Kind: contractsv1.ContextFabricSubjectProject, Rationale: "bound sweep", Complete: true,
			}
			for index := 0; index < testCase.groups; index++ {
				member := fmt.Sprintf("project_%03d", index)
				cohort.Members = append(cohort.Members, contractsv1.ContextFabricCohortMember{
					Subject:          contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectProject, CanonicalID: member, Label: member},
					Rank:             index + 1,
					InclusionReasons: []string{"matched"},
				})
				cohort.Groups = append(cohort.Groups, contractsv1.ContextFabricCohortGroup{
					Subject:            contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectTeam, CanonicalID: TeamCanonicalID(fmt.Sprintf("team_%03d", index)), Label: fmt.Sprintf("team_%03d", index)},
					MemberCanonicalIDs: []string{member},
					Total:              1,
					Complete:           true,
				})
			}
			err := contractsv1.ValidateCohortGroups(cohort.Groups, cohort.Members)
			// The PRODUCTION predicate, not a restatement of it beside the
			// guard (the class the input-domain table was swept for).
			overBound := groupListOverContractBound(testCase.groups)
			t.Logf("groups=%d bound=%d over=%v crosscheck_err=%v", testCase.groups, bound, overBound, err)
			if err != nil {
				t.Errorf("the group cross-check rejected %d groups: %v -- the bound is enforced by the cohort validator, never by this helper, so this must pass at every size",
					testCase.groups, err)
			}
			if overBound == testCase.wantValid {
				t.Fatalf("fixture defect: %d groups against bound %d classified as over=%v while the case expects valid=%v",
					testCase.groups, bound, overBound, testCase.wantValid)
			}
		})
	}
}

// TestThePreFoldLineCanCarryEverySourceState enumerates the states the
// coverage-state disclosure must be able to publish, from the producer's own
// validity predicate rather than from a list.
//
// The line routes its state through a membership check and publishes
// `unclassified` for anything outside the vocabulary. That is the right
// fail-closed posture and it is also a trap: a state the emitter cannot
// recognise becomes indistinguishable from every other unrecognised one, so a
// real coverage state silently losing its identity would look like a
// deliberate sentinel. This asserts every member survives, and that the
// sentinel is reachable only by a non-member.
func TestThePreFoldLineCanCarryEverySourceState(t *testing.T) {
	t.Parallel()

	states := []SourceState{
		SourceAvailable, SourceStale, SourceUnavailable, SourceUnconfigured,
		SourceUnauthorized, SourceNoData, SourceTruncated, SourceConflicted, SourceNotApplicable,
	}
	// Enumerated against the PRODUCER's predicate, both directions: a member
	// this list forgot fails here rather than silently going untested.
	for _, state := range states {
		if !validFactSourceState(state) {
			t.Errorf("this sweep names %q, which the producer does not consider a valid source state -- the list has drifted from the vocabulary", state)
		}
	}
	for _, outsider := range []SourceState{"", "AVAILABLE", " available ", "no-data", "unclassified"} {
		if validFactSourceState(outsider) {
			t.Errorf("the producer admits %q as a source state, so the sweep above is not enumerating a closed vocabulary", outsider)
		}
	}

	for _, state := range states {
		state := state
		t.Run(string(state), func(t *testing.T) {
			t.Parallel()
			event := GroupReadCoverageStateEvent{
				Family: QuestionFamilyGroupedCohortStatus, GroupKind: SubjectTeam,
				Read: GroupReadArmGroup, Source: "canonical_fact:health", State: state,
			}
			if !validFactSourceState(event.State) {
				t.Fatalf("state %q would publish as `unclassified` -- a real coverage state that loses its identity on the line is indistinguishable from a deliberate sentinel", state)
			}
		})
	}

	// AND BOTH ARMS, enumerated the same way.
	for _, arm := range []GroupReadArm{GroupReadArmMember, GroupReadArmGroup} {
		if !ValidGroupReadArm(arm) {
			t.Errorf("arm %q is not a vocabulary member, so the line would publish it as `unclassified`", arm)
		}
	}
	for _, outsider := range []GroupReadArm{"", "MEMBER", "groups", "unclassified"} {
		if ValidGroupReadArm(outsider) {
			t.Errorf("the arm vocabulary admits %q, which no producer writes", outsider)
		}
	}
}
