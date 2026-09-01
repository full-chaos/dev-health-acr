package contextfabric

import (
	"context"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// ORACLE O3 (design §13.11a) -- structural mutation.
//
// "Remove or conflict one field in a valid frame and assert deterministic
// repair-or-refuse, never a commit... Each asserts the FIRST failed
// invariant BY NAME, since that is what telemetry records."
//
// The by-name requirement is the whole oracle. A test that asserts only
// "this frame is invalid" passes against a validator that reports the
// wrong invariant, and a wrong invariant id is a wrong DIAGNOSIS in the
// run's own artifacts -- the exact failure AGENTS.md's CANONICAL
// ARCHITECTURE bar exists to prevent.
//
// RED ON origin/main BY CONSTRUCTION: neither the vocabularies, the union,
// nor the invariants exist there, so this file does not compile against
// the parent. The red run is recorded in the PR body.

func namedFrame(goals ...InvestigationGoal) QuestionFrame {
	return QuestionFrame{
		Goals: goals,
		SubjectExpression: SubjectExpression{
			Kind:  SubjectExpressionNamed,
			Named: &NamedSubjectExpression{Terms: []string{"dev health ops"}},
		},
		Temporal: TemporalIntentCurrent,
	}
}

func kindPtr(kind SubjectKind) *SubjectKind { return &kind }

// TestO3ExplainDriversWithDroppedDriverObligationFailsI16NotI10 is the
// FIRST red test of this slice, and it exists because the design got this
// exact assertion wrong once.
//
// Round 2's P2-4 corrected O3: the oracle originally said a dropped driver
// obligation fails I10. It does not. I10 checks only NON-EMPTINESS and
// VOCABULARY VALIDITY, so a frame whose explain_drivers goal has lost its
// principal_drivers obligation still carries {state, health, evidence,
// coverage} -- four valid members, a non-empty set -- and passes I10
// cleanly. What actually catches it is I16, the per-frame form of law L2:
// the explain_drivers goal AXIS is no longer discharged.
//
// The test asserts BOTH halves, because asserting only "I16 fires" would
// still pass on a validator that fired I10 first and never reached I16.
func TestO3ExplainDriversWithDroppedDriverObligationFailsI16NotI10(t *testing.T) {
	frame := namedFrame(GoalExplainDrivers)
	frame = NormalizeFrame(frame)
	frame = DeriveFrameObligations(frame, nil)

	if !frame.HasObligation(ObligationPrincipalDrivers) {
		t.Fatalf("precondition: explain_drivers must derive principal_drivers, got %v", frame.Obligations)
	}

	// The mutation: drop the driver obligation and nothing else.
	mutated := frame
	mutated.Obligations = nil
	for _, obligation := range frame.Obligations {
		if obligation == ObligationPrincipalDrivers {
			continue
		}
		mutated.Obligations = append(mutated.Obligations, obligation)
	}

	// HALF ONE -- I10 must PASS on the mutated set. This is the half that
	// makes the assertion falsifiable: if the surviving set were empty or
	// carried an out-of-vocabulary member, I16 would fire for a reason
	// that has nothing to do with an undischarged axis.
	if len(mutated.Obligations) == 0 {
		t.Fatalf("mutation must leave a non-empty set so I10 cannot fire; got %v", mutated.Obligations)
	}
	for _, obligation := range mutated.Obligations {
		if !ValidAnswerObligation(obligation) {
			t.Fatalf("mutation must leave only vocabulary members so I10 cannot fire; got %q", obligation)
		}
	}

	// HALF TWO -- the FIRST failure is I16, by name.
	failure, bad := ValidateFramePhaseA2(mutated, "")
	if !bad {
		t.Fatalf("dropping principal_drivers from an explain_drivers frame must fail phase A2; obligations=%v", mutated.Obligations)
	}
	if failure.Invariant != FrameInvariantI16 {
		t.Fatalf("first failed invariant = %q, want %q (I10 only checks non-emptiness and vocabulary validity, so it cannot catch this)",
			failure.Invariant, FrameInvariantI16)
	}
	if failure.Phase != FrameValidationPhaseA2 {
		t.Fatalf("failed phase = %q, want %q", failure.Phase, FrameValidationPhaseA2)
	}
	if failure.Detail != FrameFailureAxisUndischarged {
		t.Fatalf("failure detail = %q, want %q", failure.Detail, FrameFailureAxisUndischarged)
	}

	// And the axis reported is the goal axis that lost its discharge --
	// not the temporal axis, not a dimension.
	discharge, undischarged := UndischargedAxis(mutated)
	if !undischarged {
		t.Fatal("UndischargedAxis found nothing on a frame ValidateFramePhaseA2 rejected")
	}
	if discharge.Axis != AxisGoal || discharge.Value != string(GoalExplainDrivers) {
		t.Fatalf("undischarged axis = %s/%s, want goal/%s", discharge.Axis, discharge.Value, GoalExplainDrivers)
	}
}

// TestO3PhaseA1MutationsReportTheFirstFailedInvariantByName is the rest of
// O3's list, verbatim from §13.11a: "grouped without member kind (I6),
// scoped without anchor (I5), named subject without terms (I3 -- the
// omitted-subject-terms class), comparison with one operand (I2), trend
// without temporal mode (I8)... an all-unknown goal set (I15), and an
// organization-scope count with no MemberKind (I17)."
func TestO3PhaseA1MutationsReportTheFirstFailedInvariantByName(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		frame QuestionFrame
		want  FrameInvariant
	}{
		{
			name: "grouped without member kind (I6)",
			frame: QuestionFrame{
				Goals: []InvestigationGoal{GoalAssessState},
				SubjectExpression: SubjectExpression{
					Kind:    SubjectExpressionGroupedMembers,
					Grouped: &GroupedSetExpression{GroupKind: contractsv1.ContextFabricSubjectTeam},
				},
				Temporal: TemporalIntentCurrent,
			},
			want: FrameInvariantI6,
		},
		{
			name: "grouped by its own kind (I6)",
			frame: QuestionFrame{
				Goals: []InvestigationGoal{GoalAssessState},
				SubjectExpression: SubjectExpression{
					Kind: SubjectExpressionGroupedMembers,
					Grouped: &GroupedSetExpression{
						GroupKind:  contractsv1.ContextFabricSubjectTeam,
						MemberKind: contractsv1.ContextFabricSubjectTeam,
					},
				},
				Temporal: TemporalIntentCurrent,
			},
			want: FrameInvariantI6,
		},
		{
			name: "scoped without anchor (I5)",
			frame: QuestionFrame{
				Goals: []InvestigationGoal{GoalAssessState},
				SubjectExpression: SubjectExpression{
					Kind:   SubjectExpressionChildrenOfScope,
					Scoped: &ScopedSetExpression{MemberKind: contractsv1.ContextFabricSubjectProject},
				},
				Temporal: TemporalIntentCurrent,
			},
			want: FrameInvariantI5,
		},
		{
			// THE OMITTED-SUBJECT-TERMS CLASS. A named_subject with no
			// terms is the object that ticket measured the interpreter
			// producing -- correct shape, correct judgment, correct
			// group kind, subject_terms null -- and under the union it
			// cannot be constructed without failing here, BEFORE any
			// stage reads it.
			name: "named subject without terms (I3)",
			frame: QuestionFrame{
				Goals: []InvestigationGoal{GoalAssessState},
				SubjectExpression: SubjectExpression{
					Kind:  SubjectExpressionNamed,
					Named: &NamedSubjectExpression{},
				},
				Temporal: TemporalIntentCurrent,
			},
			want: FrameInvariantI3,
		},
		{
			name: "named subject with a whitespace-only term (I3)",
			frame: QuestionFrame{
				Goals: []InvestigationGoal{GoalAssessState},
				SubjectExpression: SubjectExpression{
					Kind:  SubjectExpressionNamed,
					Named: &NamedSubjectExpression{Terms: []string{"   "}},
				},
				Temporal: TemporalIntentCurrent,
			},
			want: FrameInvariantI3,
		},
		{
			name: "comparison with one operand (I2)",
			frame: QuestionFrame{
				Goals: []InvestigationGoal{GoalCompare},
				SubjectExpression: SubjectExpression{
					Kind: SubjectExpressionExplicitSet,
					Explicit: &ExplicitSetExpression{Operands: []SubjectOperand{{
						Kind:  SubjectOperandNamed,
						Named: &NamedSubjectExpression{Terms: []string{"team a"}},
					}}},
				},
				Temporal: TemporalIntentCurrent,
			},
			want: FrameInvariantI2,
		},
		{
			// I8 permits bounded_window, period_comparison and
			// time_series and forbids `current`. The frame below is the
			// one round 2's P1-2 was about, with the temporal axis left
			// at its normalized default.
			name: "trend without temporal mode (I8)",
			frame: QuestionFrame{
				Goals: []InvestigationGoal{GoalDescribeTrend},
				SubjectExpression: SubjectExpression{
					Kind:  SubjectExpressionNamed,
					Named: &NamedSubjectExpression{Terms: []string{"dev health ops"}},
				},
				Temporal: TemporalIntentCurrent,
			},
			want: FrameInvariantI8,
		},
		{
			// I15: an ALL-UNKNOWN goal set sanitizes to empty, and an
			// empty goal set is a FAILURE, never a default. Round 2's
			// P1-7 is the reason -- the old "unset defaults to
			// assess_state" rule silently turned "which teams are
			// struggling?" into a status question.
			name: "all-unknown goal set (I15)",
			frame: QuestionFrame{
				Goals: nil,
				SubjectExpression: SubjectExpression{
					Kind:  SubjectExpressionNamed,
					Named: &NamedSubjectExpression{Terms: []string{"dev health ops"}},
				},
				Temporal: TemporalIntentCurrent,
			},
			want: FrameInvariantI15,
		},
		{
			// I17: without MemberKind, "how many teams are in the
			// organization?" and "how many repositories are in the
			// organization?" are the IDENTICAL frame (round 2, P1-4).
			name: "organization-scope count with no MemberKind (I17)",
			frame: QuestionFrame{
				Goals: []InvestigationGoal{GoalCountOrAggregate},
				SubjectExpression: SubjectExpression{
					Kind: SubjectExpressionOrganizationScope,
					Org:  &OrganizationScopeExpression{},
				},
				Temporal: TemporalIntentCurrent,
			},
			want: FrameInvariantI17,
		},
		{
			name: "compare on a non-explicit-set kind (I7)",
			frame: QuestionFrame{
				Goals: []InvestigationGoal{GoalCompare},
				SubjectExpression: SubjectExpression{
					Kind:  SubjectExpressionNamed,
					Named: &NamedSubjectExpression{Terms: []string{"team a"}},
				},
				Temporal: TemporalIntentCurrent,
			},
			want: FrameInvariantI7,
		},
		{
			name: "count over a single named subject (I9)",
			frame: QuestionFrame{
				Goals: []InvestigationGoal{GoalCountOrAggregate},
				SubjectExpression: SubjectExpression{
					Kind:  SubjectExpressionNamed,
					Named: &NamedSubjectExpression{Terms: []string{"team a"}},
				},
				Temporal: TemporalIntentCurrent,
			},
			want: FrameInvariantI9,
		},
		{
			name: "no variant pointer set (I1)",
			frame: QuestionFrame{
				Goals:             []InvestigationGoal{GoalAssessState},
				SubjectExpression: SubjectExpression{Kind: SubjectExpressionNamed},
				Temporal:          TemporalIntentCurrent,
			},
			want: FrameInvariantI1,
		},
		{
			name: "variant pointer disagrees with Kind (I1)",
			frame: QuestionFrame{
				Goals: []InvestigationGoal{GoalAssessState},
				SubjectExpression: SubjectExpression{
					Kind:       SubjectExpressionNamed,
					Discovered: &DiscoveredSetExpression{MemberKind: contractsv1.ContextFabricSubjectTeam},
				},
				Temporal: TemporalIntentCurrent,
			},
			want: FrameInvariantI1,
		},
		{
			name: "discovered kind outside the subject vocabulary (I4)",
			frame: QuestionFrame{
				Goals: []InvestigationGoal{GoalRankOrSurvey},
				SubjectExpression: SubjectExpression{
					Kind:       SubjectExpressionDiscoveredKind,
					Discovered: &DiscoveredSetExpression{MemberKind: "squad"},
				},
				Temporal: TemporalIntentCurrent,
			},
			want: FrameInvariantI4,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			failure, bad := ValidateFramePhaseA1(testCase.frame)
			if !bad {
				t.Fatalf("frame must fail phase A1")
			}
			if failure.Invariant != testCase.want {
				t.Fatalf("first failed invariant = %q, want %q (detail %q)", failure.Invariant, testCase.want, failure.Detail)
			}
			if failure.Phase != FrameValidationPhaseA1 {
				t.Fatalf("failed phase = %q, want a1", failure.Phase)
			}
		})
	}
}

// TestO3EmphasisWithoutRankingFailsI14 is O3's "emphasis without ranking"
// row. It is a phase A2 row because I14 reads the DERIVED obligation set.
func TestO3EmphasisWithoutRankingFailsI14(t *testing.T) {
	frame := QuestionFrame{
		Goals: []InvestigationGoal{GoalAssessState},
		SubjectExpression: SubjectExpression{
			Kind:       SubjectExpressionDiscoveredKind,
			Discovered: &DiscoveredSetExpression{MemberKind: contractsv1.ContextFabricSubjectTeam},
		},
		Temporal: TemporalIntentCurrent,
		Emphasis: []AnswerEmphasis{EmphasisNegativeOutliers, EmphasisPositiveOutliers},
	}
	if failure, bad := ValidateFramePhaseA1(frame); bad {
		t.Fatalf("frame must pass A1 so the A2 failure is the one under test; got %q", failure.Invariant)
	}
	frame = NormalizeFrame(frame)
	frame = DeriveFrameObligations(frame, nil)

	if frame.HasObligation(ObligationRanking) {
		t.Fatalf("precondition: assess_state alone must not derive ranking; got %v", frame.Obligations)
	}
	failure, bad := ValidateFramePhaseA2(frame, "")
	if !bad {
		t.Fatal("emphasis with no derived ranking obligation must fail phase A2")
	}
	if failure.Invariant != FrameInvariantI14 {
		t.Fatalf("first failed invariant = %q, want %q", failure.Invariant, FrameInvariantI14)
	}
}

// TestO3MutationsRefuseAndNeverCommit is O3's other half -- "assert
// deterministic repair-or-refuse, NEVER A COMMIT".
//
// With no repairer configured, every mutation above must produce a
// REFUSAL carrying the failing invariant, and the returned frame must be
// the ZERO frame. Returning a partially-validated frame would be the
// "commit" the oracle forbids: a downstream stage handed a frame that
// failed validation cannot tell it from one that passed.
func TestO3MutationsRefuseAndNeverCommit(t *testing.T) {
	mutations := []QuestionFrame{
		{
			Goals: []InvestigationGoal{GoalAssessState},
			SubjectExpression: SubjectExpression{
				Kind:  SubjectExpressionNamed,
				Named: &NamedSubjectExpression{},
			},
		},
		{
			Goals: []InvestigationGoal{GoalCompare},
			SubjectExpression: SubjectExpression{
				Kind:  SubjectExpressionNamed,
				Named: &NamedSubjectExpression{Terms: []string{"team a"}},
			},
		},
		{
			Goals: nil,
			SubjectExpression: SubjectExpression{
				Kind:  SubjectExpressionNamed,
				Named: &NamedSubjectExpression{Terms: []string{"team a"}},
			},
		},
	}
	for i, frame := range mutations {
		result := ValidateAndRepairFrame(context.Background(), storage.Principal{OrgID: "org_test"}, nil, frame, nil, "", nil)
		if result.Outcome != FrameValidationOutcomeRefusedInvalid {
			t.Fatalf("mutation %d: outcome = %q, want %q", i, result.Outcome, FrameValidationOutcomeRefusedInvalid)
		}
		if result.Failure.Invariant == "" {
			t.Fatalf("mutation %d: refusal carried no invariant -- telemetry would record an unattributable failure", i)
		}
		if !ValidFrameInvariant(result.Failure.Invariant) {
			t.Fatalf("mutation %d: invariant %q is outside the closed telemetry vocabulary", i, result.Failure.Invariant)
		}
		var zero QuestionFrame
		if result.Frame.SubjectExpression.Kind != zero.SubjectExpression.Kind || len(result.Frame.Goals) != 0 || len(result.Frame.Obligations) != 0 {
			t.Fatalf("mutation %d: a refusal returned a non-zero frame (%+v) -- that is the commit O3 forbids", i, result.Frame)
		}
	}
}

// TestValidFramesPassBothPhases is the negative control. Without it the
// suite above is satisfied by a validator that rejects everything, which
// is a test that cannot fail for the right reason.
func TestValidFramesPassBothPhases(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		frame QuestionFrame
	}{
		{
			name:  "Q1 -- how is the project doing",
			frame: namedFrame(GoalAssessState),
		},
		{
			// BAR Q2, the design's governing question. Unrepresentable
			// under the singular-goal design.
			name: "Q2 -- which teams are struggling and why",
			frame: QuestionFrame{
				Goals: []InvestigationGoal{GoalRankOrSurvey, GoalExplainDrivers},
				SubjectExpression: SubjectExpression{
					Kind:       SubjectExpressionDiscoveredKind,
					Discovered: &DiscoveredSetExpression{MemberKind: contractsv1.ContextFabricSubjectTeam},
				},
			},
		},
		{
			name: "Q-A -- project statuses for each team",
			frame: QuestionFrame{
				Goals: []InvestigationGoal{GoalAssessState, GoalExplainDrivers},
				SubjectExpression: SubjectExpression{
					Kind: SubjectExpressionGroupedMembers,
					Grouped: &GroupedSetExpression{
						GroupKind:  contractsv1.ContextFabricSubjectTeam,
						MemberKind: contractsv1.ContextFabricSubjectProject,
					},
				},
			},
		},
		{
			name: "Q-B -- the fullchaos team's projects",
			frame: QuestionFrame{
				Goals: []InvestigationGoal{GoalAssessState},
				SubjectExpression: SubjectExpression{
					Kind: SubjectExpressionChildrenOfScope,
					Scoped: &ScopedSetExpression{
						AnchorTerms: []string{"fullchaos team"},
						MemberKind:  contractsv1.ContextFabricSubjectProject,
					},
				},
			},
		},
		{
			name: "how many repositories are in the organization",
			frame: QuestionFrame{
				Goals: []InvestigationGoal{GoalCountOrAggregate},
				SubjectExpression: SubjectExpression{
					Kind: SubjectExpressionOrganizationScope,
					Org:  &OrganizationScopeExpression{MemberKind: kindPtr(contractsv1.ContextFabricSubjectRepository)},
				},
			},
		},
		{
			name: "compare team A's projects with team B's projects",
			frame: QuestionFrame{
				Goals: []InvestigationGoal{GoalCompare},
				SubjectExpression: SubjectExpression{
					Kind: SubjectExpressionExplicitSet,
					Explicit: &ExplicitSetExpression{Operands: []SubjectOperand{
						{Kind: SubjectOperandScoped, Scoped: &ScopedSetExpression{AnchorTerms: []string{"team a"}, MemberKind: contractsv1.ContextFabricSubjectProject}},
						{Kind: SubjectOperandScoped, Scoped: &ScopedSetExpression{AnchorTerms: []string{"team b"}, MemberKind: contractsv1.ContextFabricSubjectProject}},
					}},
				},
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			result := ValidateAndRepairFrame(context.Background(), storage.Principal{OrgID: "org_test"}, nil, testCase.frame, nil, "", nil)
			if result.Outcome != FrameValidationOutcomeValid {
				t.Fatalf("outcome = %q (invariant %q, detail %q), want valid",
					result.Outcome, result.Failure.Invariant, result.Failure.Detail)
			}
			if len(result.Frame.Obligations) == 0 {
				t.Fatal("a valid frame must derive a non-empty obligation set")
			}
			if result.Frame.Version != QuestionFrameVersion {
				t.Fatalf("frame version = %q, want %q", result.Frame.Version, QuestionFrameVersion)
			}
		})
	}
}
