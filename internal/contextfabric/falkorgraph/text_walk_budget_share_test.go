package falkorgraph

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// Two subjects the question text matches share the full-text arm's edge
// budget (MaxResults). The first one's relationship ids all sort before the
// second one's, and it has enough edges to fill the budget alone. The second
// subject's edge must still be read: which subject's evidence fits the budget
// may not depend on how relationship ids are spelled.
func TestTextWalkSharesTheEdgeBudgetAcrossMatchedSubjects(t *testing.T) {
	const budget = 25
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "db.idx.fulltext.queryNodes"):
			return []row{
				{"node": &node{Properties: map[string]interface{}{propKind: "repository", propCanonicalID: "repository:checkout", propLabel: "acme/checkout", propSearchText: "checkout"}}, "score": 1.0},
				{"node": &node{Properties: map[string]interface{}{propKind: "project", propCanonicalID: "project:checkout", propLabel: "checkout", propSearchText: "checkout"}}, "score": 1.0},
			}, nil
		case strings.Contains(cypher, "UNION"):
			switch params["id"] {
			case "repository:checkout":
				rows := make([]row, 0, budget)
				for i := 0; i < budget; i++ {
					rows = append(rows, row{
						"r":       &edge{Properties: map[string]interface{}{propRelationType: "BELONGS_TO_REPOSITORY", propRelationshipID: fmt.Sprintf("relationship.v2:a:%02d", i), propEvidenceRefs: []string{fmt.Sprintf("evidence_incident_%02d", i)}}},
						"srcKind": "incident", "srcId": fmt.Sprintf("incident:%02d", i), "dstKind": "repository", "dstId": "repository:checkout",
					})
				}
				return rows, nil
			case "project:checkout":
				return []row{{
					"r":       &edge{Properties: map[string]interface{}{propRelationType: "OWNED_BY_TEAM", propRelationshipID: "relationship.v2:z:owner", propEvidenceRefs: []string{"evidence_owner"}}},
					"srcKind": "project", "srcId": "project:checkout", "dstKind": "team", "dstId": "team:core",
				}}, nil
			}
			return nil, nil
		default:
			kind, _ := params["kind"].(string)
			id, _ := params["id"].(string)
			if kind == "" || id == "" {
				return nil, nil
			}
			return []row{fakeSubjectNodeRow(kind, id, id)}, nil
		}
	}}
	adapter := newFakeAdapter(t, fake)
	request := contextfabric.GraphDiscoveryRequest{
		Request: contextfabric.InvestigationRequest{
			Question: "which team owns checkout",
			Options: contextfabric.InvestigationOptions{
				MaxSubjectCandidates: 10, MaxCohortMembers: 10, MaxRelationshipPaths: 100,
				MaxDrivers: 10, MaxEvidenceRefs: 500, MaxSerializedBytes: 262144,
			},
		},
		Interpretation: contextfabric.InterpretedQuestion{Shape: contextfabric.ShapeSingleSubject},
	}
	result, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	owner := false
	repositoryEdges := 0
	for _, path := range result.Paths {
		for _, e := range path.Edges {
			switch {
			case e.From.CanonicalID == "project:checkout" && e.To.CanonicalID == "team:core":
				owner = true
			case e.To.CanonicalID == "repository:checkout":
				repositoryEdges++
			}
		}
	}
	if !owner {
		t.Fatalf("the project's owner edge was not read: %d paths, %d of them the repository's; the repository's ids sorted first and took the whole budget", len(result.Paths), repositoryEdges)
	}
	if repositoryEdges != budget-1 {
		t.Fatalf("the repository kept %d edges, want %d: the budget is shared, not grown", repositoryEdges, budget-1)
	}
}
