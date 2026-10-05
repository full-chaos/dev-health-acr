package directread

import (
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// Every edge page of a read without as_of (hop 1, the depth-2 frontier scan
// and hop 2) asks the graph for the current-axis rule; every page of an as_of
// read asks for the strict window.
func TestRelationshipsCurrentAxisRuleOnEveryPage(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name    string
		asOf    string
		current bool
	}{
		{"current axis", "", true},
		{"as_of axis", now.Add(-48 * time.Hour).Format(time.RFC3339Nano), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			graph := &fakeEdgeGraph{fakeGraph: graphOfOrgA()}
			graph.nodes[graphrank.SubjectKey(workA)] = repos("acme/a")
			graph.edges = []EdgeCandidate{
				edgeBetween("e1", "OWNED_BY_TEAM", repoA, teamT, nil),
				edgeBetween("e2", "BELONGS_TO_REPOSITORY", workA, repoA, nil),
			}
			reader, _ := newRelReader(graph, &now)
			_, ids := readAll(t, reader, unrestricted, RelationshipsRequest{
				Subject: RelationshipsSubject{Kind: string(contractsv1.ContextFabricSubjectTeam), CanonicalID: teamT.CanonicalID}, Depth: 2, AsOf: tc.asOf,
			})
			if len(ids) != 2 {
				t.Fatalf("ids = %v, want the hop-1 and the hop-2 edge", ids)
			}
			if len(graph.queries) < 3 {
				t.Fatalf("%d edge pages read, want hop 1, the frontier scan and hop 2", len(graph.queries))
			}
			for i, q := range graph.queries {
				if q.Current != tc.current {
					t.Fatalf("page %d (origins %v): Current = %v, want %v", i, q.Origins, q.Current, tc.current)
				}
			}
		})
	}
}
