package falkorgraph

// frameAnchorBound is a thin delegate to contextfabric.AnchorBound -- ONE
// definition, consumed everywhere, never a second implementation that can
// drift from it the way an earlier, unswept copy already did once. This
// file pins that equality across a real input domain rather than trusting
// the delegation to stay a delegation: a future edit that re-inlines logic
// into frameAnchorBound fails HERE, against the shared source of truth,
// without this test needing to know which answer is "right" for any given
// cell.

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
	}{
		{"nil frame", nil, contextfabric.SubjectResolution{Committed: []contextfabric.SubjectRef{anchor}}, nil},
		{"non-scoped expression", discoveredFrame, contextfabric.SubjectResolution{Committed: []contextfabric.SubjectRef{anchor}, Candidates: []contextfabric.SubjectCandidate{matchedCandidate}}, nil},
		{"caller canonical id basis, no candidates", scopedFrame, contextfabric.SubjectResolution{Committed: []contextfabric.SubjectRef{anchor}}, contextfabric.CommitBasisSet{contextfabric.SubjectMapKey(anchor): contextfabric.CommitBasisCallerCanonicalID}},
		{"authoritative identity basis with a matching term", scopedFrame, contextfabric.SubjectResolution{Committed: []contextfabric.SubjectRef{anchor}, Candidates: []contextfabric.SubjectCandidate{matchedCandidate}}, contextfabric.CommitBasisSet{contextfabric.SubjectMapKey(anchor): contextfabric.CommitBasisAuthoritativeIdentity}},
		{"authoritative identity basis with no matching term", scopedFrame, contextfabric.SubjectResolution{Committed: []contextfabric.SubjectRef{anchor}, Candidates: []contextfabric.SubjectCandidate{unmatchedCandidate}}, contextfabric.CommitBasisSet{contextfabric.SubjectMapKey(anchor): contextfabric.CommitBasisAuthoritativeIdentity}},
		// The exact gap the finding named: a statistical basis with a
		// matching term must never bind, in EITHER function.
		{"statistical basis with a matching term", scopedFrame, contextfabric.SubjectResolution{Committed: []contextfabric.SubjectRef{anchor}, Candidates: []contextfabric.SubjectCandidate{matchedCandidate}}, contextfabric.CommitBasisSet{contextfabric.SubjectMapKey(anchor): contextfabric.CommitBasisStatistical}},
		{"no basis recorded at all, with a matching term", scopedFrame, contextfabric.SubjectResolution{Committed: []contextfabric.SubjectRef{anchor}, Candidates: []contextfabric.SubjectCandidate{matchedCandidate}}, nil},
		{"scoped frame, no candidates at all", scopedFrame, contextfabric.SubjectResolution{Committed: []contextfabric.SubjectRef{anchor}}, nil},
	} {
		cell := cell
		t.Run(cell.name, func(t *testing.T) {
			t.Parallel()
			want := contextfabric.AnchorBound(cell.frame, "", anchor, cell.resolution, cell.bases)
			got := frameAnchorBound(cell.frame, anchor, cell.resolution, cell.bases)
			if got != want {
				t.Fatalf("frameAnchorBound() = %t, contextfabric.AnchorBound() = %t -- the two must never disagree", got, want)
			}
		})
	}
}
