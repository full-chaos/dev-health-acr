package contextfabric

import "testing"

func unboundTestFrame() *QuestionFrame {
	return &QuestionFrame{SubjectExpression: SubjectExpression{
		Kind:   SubjectExpressionChildrenOfScope,
		Scoped: &ScopedSetExpression{AnchorTerms: []string{"platform"}, MemberKind: SubjectProject},
	}}
}

func unboundTestSubject(kind SubjectKind, id string) SubjectRef {
	return SubjectRef{Kind: kind, CanonicalID: string(kind) + ":" + id, Label: id}
}

func unboundTestCandidate(subject SubjectRef, term string, state ResolutionState) SubjectCandidate {
	return SubjectCandidate{Subject: subject, State: state, MatchedTerms: []string{term}}
}

func TestUnboundScopeAnchorDecisionTable(t *testing.T) {
	a, b := unboundTestSubject(SubjectTeam, "a"), unboundTestSubject(SubjectTeam, "b")
	repo := unboundTestSubject(SubjectRepository, "r")
	for _, tc := range []struct {
		name       string
		anchorKind SubjectKind
		resolution SubjectResolution
		want       bool
	}{
		{"two committed, no term match", SubjectTeam, SubjectResolution{
			Committed:  []SubjectRef{a, b},
			Candidates: []SubjectCandidate{unboundTestCandidate(a, "x", ResolutionCommitted), unboundTestCandidate(b, "y", ResolutionCommitted)},
		}, true},
		{"two committed, one term match", SubjectTeam, SubjectResolution{
			Committed:  []SubjectRef{a, b},
			Candidates: []SubjectCandidate{unboundTestCandidate(a, "platform", ResolutionCommitted), unboundTestCandidate(b, "y", ResolutionCommitted)},
		}, false},
		{"sole committed", SubjectTeam, SubjectResolution{
			Committed:  []SubjectRef{a},
			Candidates: []SubjectCandidate{unboundTestCandidate(a, "x", ResolutionCommitted)},
		}, false},
		{"none committed, candidate of the kind", SubjectTeam, SubjectResolution{
			Candidates: []SubjectCandidate{unboundTestCandidate(a, "platform", ResolutionAmbiguous)},
		}, true},
		{"none committed, candidate of another kind", SubjectTeam, SubjectResolution{
			Candidates: []SubjectCandidate{unboundTestCandidate(repo, "platform", ResolutionAmbiguous)},
		}, false},
		{"no candidate at all", SubjectTeam, SubjectResolution{}, false},
		{"anchor kind off the tree", SubjectIncident, SubjectResolution{
			Candidates: []SubjectCandidate{unboundTestCandidate(unboundTestSubject(SubjectIncident, "i"), "platform", ResolutionAmbiguous)},
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := unboundScopeAnchorNamed(unboundTestFrame(), tc.anchorKind, tc.resolution, CommitBasisSet{}); got != tc.want {
				t.Fatalf("unboundScopeAnchorNamed = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestUnboundScopeAnchorTurnReadsAsItsOwnTerminalReason(t *testing.T) {
	a, b := unboundTestSubject(SubjectTeam, "a"), unboundTestSubject(SubjectTeam, "b")
	frame := unboundTestFrame()
	resolution := SubjectResolution{
		Committed:  []SubjectRef{a, b},
		Candidates: []SubjectCandidate{unboundTestCandidate(a, "x", ResolutionCommitted), unboundTestCandidate(b, "y", ResolutionCommitted)},
	}
	served := unboundScopeAnchorResolution(frame, SubjectTeam, resolution)
	if len(served.Committed) != 0 || len(served.Candidates) != 2 || served.ClarificationPrompt == "" {
		t.Fatalf("served resolution = %+v, want two ambiguous candidates, none committed, a prompt", served)
	}
	for _, candidate := range served.Candidates {
		if candidate.State != ResolutionAmbiguous {
			t.Fatalf("candidate %+v is not ambiguous", candidate)
		}
	}
	if got := unboundAnchorAwareTerminalReason(frame, SubjectTeam, FrameGate{}, served, 0, declaredKindDecision{}); got != unboundScopeAnchorTerminalReason {
		t.Fatalf("terminal reason = %q, want %q", got, unboundScopeAnchorTerminalReason)
	}
	if got := subjectlessTerminalReason(FrameGate{}, served, 0, declaredKindDecision{}); got != "ambiguous" {
		t.Fatalf("generic reason = %q: the unbound class must be told apart from it", got)
	}
}
