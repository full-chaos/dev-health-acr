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
