package graphrank

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
)

// CHAOS-12619: a path's why_relevant is its edge's fact, and a stored fact names
// a work item end by its raw id. The reader reads the key, for every provider.
func TestAPathNeverServesARawWorkItemID(t *testing.T) {
	options := contextfabric.InvestigationOptions{
		MaxSubjectCandidates: 10, MaxCohortMembers: 10, MaxRelationshipPaths: 10,
		MaxDrivers: 10, MaxEvidenceRefs: 50, MaxSerializedBytes: 262144,
	}
	pr := contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: "pull_request:920f9442-07df-4217-4dc4-c5833c0b8268:5063", Label: "PR #5063"}
	for raw, want := range map[string]string{
		"linear:CHAOS-11405":                "CHAOS-11405 links pull request PR #5063",
		"jira:OPS-11":                       "OPS-11 links pull request PR #5063",
		"gh:full-chaos/dev-health-ops#5035": "full-chaos/dev-health-ops#5035 links pull request PR #5063",
		"gitlab:group/project#9":            "group/project#9 links pull request PR #5063",
	} {
		id, _, err := identity.Derive(identity.KindWorkItem, []string{"00000000-0000-0000-0000-000000000000", raw}, nil)
		if err != nil {
			t.Fatal(err)
		}
		from := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: id, Label: "a title"}
		edges := []ResolvedEdge{{
			UUID: "edge_" + raw, Name: "LINKS_PULL_REQUEST", Fact: raw + " links pull request PR #5063",
			From: from, To: pr, Attributes: map[string]interface{}{"evidence_refs": []string{"evidence_1"}},
			CreatedAt: "2026-01-01T00:00:00Z",
		}}
		result := AdmitEdges("org-1", edges, options, func(contextfabric.SubjectRef) bool { return false })
		if len(result.Paths) != 1 || result.Paths[0].WhyRelevant != want {
			t.Errorf("%s: paths = %+v, want why_relevant %q", raw, result.Paths, want)
		}
	}
}
