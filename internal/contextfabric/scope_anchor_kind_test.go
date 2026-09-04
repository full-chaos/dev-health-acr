package contextfabric

import "testing"

func scopedFrameWith(memberKind SubjectKind, anchorTerms []string) *QuestionFrame {
	return &QuestionFrame{
		Goals: []InvestigationGoal{GoalAssessState},
		SubjectExpression: SubjectExpression{
			Kind:   SubjectExpressionChildrenOfScope,
			Scoped: &ScopedSetExpression{AnchorTerms: anchorTerms, MemberKind: memberKind},
		},
	}
}

// Every refusal path returns "" -- the disabled state that makes every
// widened signature downstream behave exactly as it did before this ticket.
// Each row is a DISTINCT reason, so a mutation that collapses one check into
// another still fails at least one row.
func TestScopeAnchorRetrievalKind_RefusalPaths(t *testing.T) {
	t.Parallel()
	grouped := &QuestionFrame{
		Goals: []InvestigationGoal{GoalAssessState},
		SubjectExpression: SubjectExpression{
			Kind:    SubjectExpressionGroupedMembers,
			Grouped: &GroupedSetExpression{GroupKind: SubjectTeam, MemberKind: SubjectProject},
		},
	}
	named := &QuestionFrame{
		Goals: []InvestigationGoal{GoalAssessState},
		SubjectExpression: SubjectExpression{
			Kind:  SubjectExpressionNamed,
			Named: &NamedSubjectExpression{Terms: []string{"platform"}},
		},
	}

	cases := []struct {
		name       string
		frame      *QuestionFrame
		anchorKind SubjectKind
		why        string
	}{
		{"nil frame", nil, SubjectTeam, "no frame at all"},
		{"grouped_members declares no scope anchor", grouped, SubjectTeam,
			"GroupKind is a grouping axis frameKindHints already contributes, not an anchor"},
		{"named_subject declares no scope anchor", named, SubjectTeam, "wrong variant"},
		// ISOLATES THE VARIANT CHECK. Every other wrong-variant fixture has a
		// nil Scoped, so the nil guard subsumes the variant guard and a
		// mutation deleting the variant check survives them all (it did).
		// This one is a MALFORMED union -- a grouped kind carrying a populated
		// Scoped -- which only the variant check can refuse. Invariant I1
		// forbids this shape upstream; the check is what makes that refusal
		// local rather than assumed.
		{"grouped kind carrying a populated Scoped", &QuestionFrame{
			Goals: []InvestigationGoal{GoalAssessState},
			SubjectExpression: SubjectExpression{
				Kind:    SubjectExpressionGroupedMembers,
				Grouped: &GroupedSetExpression{GroupKind: SubjectTeam, MemberKind: SubjectProject},
				Scoped:  &ScopedSetExpression{AnchorTerms: []string{"platform"}, MemberKind: SubjectRepository},
			},
		}, SubjectTeam, "only the variant check can refuse a malformed union"},
		{"no anchor terms", scopedFrameWith(SubjectRepository, nil), SubjectTeam,
			"a kind with nothing to search for cannot seed retrieval"},
		{"empty anchor kind", scopedFrameWith(SubjectRepository, []string{"platform"}), "",
			"the receipt carried none"},
		{"out-of-vocabulary anchor kind", scopedFrameWith(SubjectRepository, []string{"platform"}), SubjectKind("squad"),
			"not a member of the closed registry"},
		{"anchor kind equals member kind", scopedFrameWith(SubjectTeam, []string{"platform"}), SubjectTeam,
			"an anchor of the members' own kind is not a scope relationship"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ScopeAnchorRetrievalKind(tc.frame, tc.anchorKind); got != "" {
				t.Errorf("ScopeAnchorRetrievalKind() = %q, want \"\" (%s)", got, tc.why)
			}
		})
	}
}

// The admitted case, and the POSITIVE CONTROL for the table above: if this
// failed, every refusal row would pass vacuously.
func TestScopeAnchorRetrievalKind_AdmitsADistinctAnchorKind(t *testing.T) {
	t.Parallel()
	frame := scopedFrameWith(SubjectRepository, []string{"platform"})
	if got := ScopeAnchorRetrievalKind(frame, SubjectTeam); got != SubjectTeam {
		t.Fatalf("ScopeAnchorRetrievalKind() = %q, want %q", got, SubjectTeam)
	}
	// A second, different pairing, so the result cannot be a constant.
	other := scopedFrameWith(SubjectProject, []string{"fullchaos"})
	if got := ScopeAnchorRetrievalKind(other, SubjectTeam); got != SubjectTeam {
		t.Fatalf("ScopeAnchorRetrievalKind() = %q, want %q", got, SubjectTeam)
	}
	if got := ScopeAnchorRetrievalKind(scopedFrameWith(SubjectTeam, []string{"x"}), SubjectProject); got != SubjectProject {
		t.Fatalf("ScopeAnchorRetrievalKind() = %q, want %q (member/anchor kinds swapped)", got, SubjectProject)
	}
}
