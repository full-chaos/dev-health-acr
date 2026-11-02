package directread

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-12619: read_relationships serves an edge's stored fact, and a stored fact
// names a work item end by its raw source id. The served fact names it by its key.
func TestAServedEdgeFactNeverNamesARawWorkItemID(t *testing.T) {
	t.Parallel()
	id, _, err := identity.Derive(identity.KindWorkItem, []string{"00000000-0000-0000-0000-000000000000", "linear:CHAOS-7"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	item := subject(contractsv1.ContextFabricSubjectWorkItem, id)
	graph := &fakeEdgeGraph{fakeGraph: graphOfOrgA()}
	graph.nodes[graphrank.SubjectKey(item)] = repos("acme/a")
	graph.edges = []EdgeCandidate{edgeBetween("e1", "BELONGS_TO_REPOSITORY", item, repoA, map[string]interface{}{"fact": "linear:CHAOS-7 belongs to repository acme/a"})}
	reader, _ := newRelReader(graph, nil)
	response, err := reader.Read(relCtx("raw-fact"), unrestricted, RelationshipsRequest{Subject: RelationshipsSubject{Kind: "repository", CanonicalID: repoA.CanonicalID}})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Edges) != 1 || response.Edges[0].Fact != "CHAOS-7 belongs to repository acme/a" {
		t.Fatalf("edges = %+v, want the fact in reader words", response.Edges)
	}
	if response.Edges[0].From.CanonicalID != id {
		t.Fatalf("the raw id left the data: from = %+v", response.Edges[0].From)
	}
	// A custom fact is not the writer's composition and stays (codex r1).
	graph.edges = []EdgeCandidate{edgeBetween("e1", "BELONGS_TO_REPOSITORY", item, repoA, map[string]interface{}{"fact": "linear:CHAOS-7 is a human-authored escalation note"})}
	response, err = reader.Read(relCtx("custom-fact"), unrestricted, RelationshipsRequest{Subject: RelationshipsSubject{Kind: "repository", CanonicalID: repoA.CanonicalID}})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Edges) != 1 || response.Edges[0].Fact != "linear:CHAOS-7 is a human-authored escalation note" {
		t.Fatalf("edges = %+v, want the custom fact as stored", response.Edges)
	}
}
