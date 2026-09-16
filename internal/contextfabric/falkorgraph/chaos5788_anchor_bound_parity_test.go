package falkorgraph

// frameAnchorBound is a thin delegate to contextfabric.AnchorBound -- ONE
// definition, consumed everywhere, never a second implementation that can
// drift from it the way an earlier, unswept copy already did once. Each cell
// below carries its own hand-written expected answer, checked against BOTH
// functions independently -- comparing the two functions' outputs to EACH
// OTHER instead would pass on any input for which both happen to agree,
// wrong or not, including the exact case an earlier, unswept
// frameAnchorBound copy got wrong for months: a call-through would have to
// know the right answer to fail, and a call-through never does.

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

func TestFrameAnchorBoundNeverDriftsFromTheSharedPredicate(t *testing.T) {
	t.Parallel()
	anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:PARITY_ANCHOR", Label: "parity anchor"}
	scopedFrame := &contextfabric.QuestionFrame{
		SubjectExpression: contextfabric.SubjectExpression{
			Kind:   contextfabric.SubjectExpressionChildrenOfScope,
			Scoped: &contextfabric.ScopedSetExpression{AnchorTerms: []string{"a"}, MemberKind: contextfabric.SubjectTeam},
		},
	}
	discoveredFrame := &contextfabric.QuestionFrame{
		SubjectExpression: contextfabric.SubjectExpression{Kind: contextfabric.SubjectExpressionDiscoveredKind},
	}
	matchedCandidate := contextfabric.SubjectCandidate{Subject: anchor, State: contextfabric.ResolutionCommitted, MatchedTerms: []string{"a"}, MatchReasons: []string{"matched"}, Confidence: 1}
	unmatchedCandidate := contextfabric.SubjectCandidate{Subject: anchor, State: contextfabric.ResolutionCommitted, MatchedTerms: []string{"other"}, MatchReasons: []string{"matched"}, Confidence: 1}

	for _, cell := range []struct {
		name       string
		frame      *contextfabric.QuestionFrame
		resolution contextfabric.SubjectResolution
		bases      contextfabric.CommitBasisSet
		want       bool
	}{
		{"nil frame", nil, contextfabric.SubjectResolution{Committed: []contextfabric.SubjectRef{anchor}}, nil, false},
		{"non-scoped expression", discoveredFrame, contextfabric.SubjectResolution{Committed: []contextfabric.SubjectRef{anchor}, Candidates: []contextfabric.SubjectCandidate{matchedCandidate}}, nil, false},
		{"caller canonical id basis, no candidates", scopedFrame, contextfabric.SubjectResolution{Committed: []contextfabric.SubjectRef{anchor}}, contextfabric.CommitBasisSet{contextfabric.SubjectMapKey(anchor): contextfabric.CommitBasisCallerCanonicalID}, true},
		{"authoritative identity basis with a matching term", scopedFrame, contextfabric.SubjectResolution{Committed: []contextfabric.SubjectRef{anchor}, Candidates: []contextfabric.SubjectCandidate{matchedCandidate}}, contextfabric.CommitBasisSet{contextfabric.SubjectMapKey(anchor): contextfabric.CommitBasisAuthoritativeIdentity}, true},
		{"authoritative identity basis with no matching term", scopedFrame, contextfabric.SubjectResolution{Committed: []contextfabric.SubjectRef{anchor}, Candidates: []contextfabric.SubjectCandidate{unmatchedCandidate}}, contextfabric.CommitBasisSet{contextfabric.SubjectMapKey(anchor): contextfabric.CommitBasisAuthoritativeIdentity}, false},
		// A statistical basis with a matching term must never bind, in
		// EITHER function.
		{"statistical basis with a matching term", scopedFrame, contextfabric.SubjectResolution{Committed: []contextfabric.SubjectRef{anchor}, Candidates: []contextfabric.SubjectCandidate{matchedCandidate}}, contextfabric.CommitBasisSet{contextfabric.SubjectMapKey(anchor): contextfabric.CommitBasisStatistical}, false},
		{"no basis recorded at all, with a matching term", scopedFrame, contextfabric.SubjectResolution{Committed: []contextfabric.SubjectRef{anchor}, Candidates: []contextfabric.SubjectCandidate{matchedCandidate}}, nil, false},
		{"scoped frame, no candidates at all", scopedFrame, contextfabric.SubjectResolution{Committed: []contextfabric.SubjectRef{anchor}}, nil, false},
	} {
		cell := cell
		t.Run(cell.name, func(t *testing.T) {
			t.Parallel()
			if got := contextfabric.AnchorBound(cell.frame, "", anchor, cell.resolution, cell.bases); got != cell.want {
				t.Fatalf("contextfabric.AnchorBound() = %t, want %t", got, cell.want)
			}
			if got := frameAnchorBound(cell.frame, anchor, cell.resolution, cell.bases); got != cell.want {
				t.Fatalf("frameAnchorBound() = %t, want %t", got, cell.want)
			}
		})
	}
}
