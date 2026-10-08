package falkorgraph

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// endpointFake serves a project -OWNED_BY_TEAM-> team edge whose own
// attributes and whose two endpoints carry the stated grants.
func endpointFake(edgeAuthz, projectAuthz, teamAuthz []string) *fakeConn {
	return &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "UNION"):
			return []row{{
				"r": &edge{Properties: map[string]interface{}{
					propRelationType: "OWNED_BY_TEAM", propRelationshipID: "rel_x", "authorization_repositories": edgeAuthz,
				}},
				"srcKind": "project", "srcId": "project:x", "dstKind": "team", "dstId": "team:x",
			}}, nil
		default:
			id, _ := params["id"].(string)
			kind, _ := params["kind"].(string)
			switch id {
			case "project:x":
				r := fakeSubjectNodeRow("project", id, "X")
				r["n"].(*node).Properties["authorization_repositories"] = projectAuthz
				return []row{r}, nil
			case "team:x":
				r := fakeSubjectNodeRow("team", id, "X")
				r["n"].(*node).Properties["authorization_repositories"] = teamAuthz
				return []row{r}, nil
			}
			_ = kind
			return nil, nil
		}
	}}
}

var (
	endpointGranted = []string{"full-chaos/dev-health-acr"}
	endpointDenied  = []string{"other/private"}
)

// resolveEdge admits an edge only when the edge and BOTH endpoints are
// visible: each clause alone decides.
func TestResolveEdgeAdmitsOnlyWhenTheEdgeAndBothEndpointsAreVisible(t *testing.T) {
	principal := storage.Principal{OrgID: "org-1", RepositoryScopes: endpointGranted}
	ce := graphrank.CandidateEdge{
		UUID: "rel_x", Name: "OWNED_BY_TEAM",
		SourceNodeUUID: subjectUUID("project", "project:x"), TargetNodeUUID: subjectUUID("team", "team:x"),
	}
	for _, c := range []struct {
		name                    string
		edge, project, team     []string
		wantAdmitted, wantAuthz bool
	}{
		{"all visible", endpointGranted, endpointGranted, endpointGranted, true, false},
		{"edge denied", endpointDenied, endpointGranted, endpointGranted, false, true},
		{"from endpoint denied", endpointGranted, endpointDenied, endpointGranted, false, true},
		{"to endpoint denied", endpointGranted, endpointGranted, endpointDenied, false, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			a := newFakeAdapter(t, endpointFake(c.edge, c.project, c.team))
			edgeCandidate := ce
			edgeCandidate.Attributes = map[string]interface{}{"authorization_repositories": c.edge}
			_, resolution, reason, err := a.resolveEdge(context.Background(), "k", "org-1", principal, contextfabric.RequestedScope{}, edgeCandidate, temporalFilter{})
			if err != nil {
				t.Fatal(err)
			}
			if (resolution == edgeAdmitted) != c.wantAdmitted || (reason == edgeFilterReasonAuthz) != c.wantAuthz {
				t.Fatalf("resolution %v reason %v, want admitted=%v authz-filtered=%v", resolution, reason, c.wantAdmitted, c.wantAuthz)
			}
		})
	}
}

// The generic walk from a visible project does not cross an ownership edge to
// a team the caller cannot see.
func TestHopWalkDoesNotCrossAnEdgeToAnInvisibleEndpoint(t *testing.T) {
	principal := storage.Principal{OrgID: "org-1", RepositoryScopes: endpointGranted}
	a := newFakeAdapter(t, endpointFake(endpointGranted, endpointGranted, endpointDenied))
	origin := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project:x", Label: "X"}
	nodes, edges, _, filters, _, err := a.hopWalk(context.Background(), "k", "org-1", principal, contextfabric.RequestedScope{}, origin, 2, 25, temporalFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 0 || len(edges) != 0 || filters.Authz != 1 {
		t.Fatalf("nodes %d edges %d authz-filtered %d, want 0 0 1", len(nodes), len(edges), filters.Authz)
	}
}
