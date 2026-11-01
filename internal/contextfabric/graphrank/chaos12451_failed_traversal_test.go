package graphrank

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestAFailedObservationTraversalDegradesRetrieval (CHAOS-12451, lead ruling): a
// traversal that failed cannot say the observation has no parent, whether a read
// was cut or the backend failed. Telemetry alone left the answer silent about a
// parent it lost, so the resolution carries it to the answer's limitations.
func TestAFailedObservationTraversalDegradesRetrieval(t *testing.T) {
	t.Parallel()
	document := observationNode("node-document-erroring", "document_error", "Ask Dev readiness review", 0.9)
	for name, outcome := range map[string]struct {
		traversal ObservationTraversal
		want      bool
	}{
		"failed":    {ObservationTraversalErrored, true},
		"no parent": {ObservationNoParent, false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			backend := &fakeGraphBackend{
				searchResults: map[string][]CandidateNode{"readiness review": {document}},
				traverse: func(context.Context, string, CandidateNode, bool) (contextfabric.SubjectCandidate, ObservationTraversal) {
					return contextfabric.SubjectCandidate{}, outcome.traversal
				},
			}
			resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted("readiness review"), backend.deps(), nil, nil)
			if err != nil {
				t.Fatalf("ResolveSubjects() error = %v", err)
			}
			if resolution.RetrievalDegraded != outcome.want {
				t.Fatalf("RetrievalDegraded = %v, want %v", resolution.RetrievalDegraded, outcome.want)
			}
		})
	}
}

// TestAFailedOperandTraversalDegradesTheComparison (CHAOS-12451, lead ruling):
// the comparison resolver published no retrieval signal, so a failed traversal in
// one operand reached no answer, whether the comparison published or held.
func TestAFailedOperandTraversalDegradesTheComparison(t *testing.T) {
	t.Parallel()
	alpha := exactMatchNode(contextfabric.SubjectTeam, "team_alpha", "alpha")
	beta := exactMatchNode(contextfabric.SubjectTeam, "team_beta", "beta")
	document := observationNode("node-document-erroring", "document_error", "alpha review", 0.9)
	for name, tc := range map[string]struct {
		pool      map[string][]CandidateNode
		traversal ObservationTraversal
		committed int
		want      bool
	}{
		"published, failed":    {map[string][]CandidateNode{"alpha": {alpha, document}, "beta": {beta}}, ObservationTraversalErrored, 2, true},
		"published, no parent": {map[string][]CandidateNode{"alpha": {alpha, document}, "beta": {beta}}, ObservationNoParent, 2, false},
		"held, failed":         {map[string][]CandidateNode{"alpha": {document}, "beta": {beta}}, ObservationTraversalErrored, 0, true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			backend := &fakeGraphBackend{
				searchResults: tc.pool,
				traverse: func(context.Context, string, CandidateNode, bool) (contextfabric.SubjectCandidate, ObservationTraversal) {
					return contextfabric.SubjectCandidate{}, tc.traversal
				},
			}
			resolution, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(), storage.Principal{OrgID: "org-1"},
				slotGateRequest(), testInterpreted("alpha", "beta"), backend.deps(), nil, nil, twoNamedSlotGateFrame(), "")
			if err != nil {
				t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
			}
			if len(resolution.Committed) != tc.committed {
				t.Fatalf("committed = %v, want %d: this case does not reach the path it names", resolution.Committed, tc.committed)
			}
			if resolution.RetrievalDegraded != tc.want {
				t.Fatalf("RetrievalDegraded = %v, want %v", resolution.RetrievalDegraded, tc.want)
			}
		})
	}
}
