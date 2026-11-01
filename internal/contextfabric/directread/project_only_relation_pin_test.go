package directread

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12299 E2: the relation reaches EdgeGate at the depth-2 frontier scan
// (read_relationships.go, frontier). A DOCUMENTED_BY hop-1 edge between two
// admitted ends must not extend the walk for a project-only caller, so the
// hop-2 edge beyond it is never read. Only the relation decides here: both
// ends pass the node rule and the edge's lists are open.
func TestTheFrontierScanDoesNotExtendAProjectOnlyWalkThroughAnAttributionEdge(t *testing.T) {
	granted := subject(contractsv1.ContextFabricSubjectProject, "project.v2:github:P-1")
	near := subject(contractsv1.ContextFabricSubjectWorkItem, "work_item.v2:near")
	far := subject(contractsv1.ContextFabricSubjectWorkItem, "work_item.v2:far")
	member := map[string]interface{}{graphrank.AuthorizationProjectIdentitiesAttr: []string{"project.v2:github:P-1"}}
	node := func(ref contextfabric.SubjectRef, extra map[string]interface{}) map[string]interface{} {
		out := map[string]interface{}{"subject_kind": string(ref.Kind), "canonical_id": ref.CanonicalID, "authorization_repositories": "*", "authorization_projects": "*"}
		for key, value := range extra {
			out[key] = value
		}
		return out
	}
	open := map[string]interface{}{"authorization_projects": "*", "authorization_repositories": "*"}
	graph := &fakeEdgeGraph{fakeGraph: &fakeGraph{org: orgA, nodes: map[string]map[string]interface{}{
		graphrank.SubjectKey(granted): node(granted, nil),
		graphrank.SubjectKey(near):    node(near, member),
		graphrank.SubjectKey(far):     node(far, member),
	}}}
	graph.edges = []EdgeCandidate{
		edgeBetween("rel-documented", string(contractsv1.ContextFabricRelationshipDocumentedBy), near, granted, open),
		edgeBetween("rel-beyond", string(contractsv1.ContextFabricRelationshipBlocks), far, near, open),
	}
	reader, _ := newRelReader(graph, nil)
	principal := storage.Principal{OrgID: orgA, Subject: "u", CredentialID: "c", ProjectScopes: []string{"project.v2:github:P-1"}}
	_, ids := readAll(t, reader, principal, RelationshipsRequest{
		Subject: RelationshipsSubject{Kind: string(granted.Kind), CanonicalID: granted.CanonicalID}, Direction: "in", Depth: 2, Limit: 100,
	})
	for _, id := range ids {
		if id == "rel-beyond" {
			t.Fatalf("served %v: the walk was extended through a DOCUMENTED_BY edge", ids)
		}
	}
}

// admitAllGraph admits every subject it holds a node for, whatever its kind,
// and returns every edge whatever type was asked: a graph where the ends and
// the type filter never decide, so a test over it sees only the relation.
type admitAllGraph struct{ *fakeEdgeGraph }

func (g admitAllGraph) AuthorizeStoredSubjects(_ context.Context, _ storage.Principal, _ contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([]contextfabric.StoredSubjectOutcome, error) {
	out := make([]contextfabric.StoredSubjectOutcome, len(subjects))
	for index, ref := range subjects {
		out[index] = contextfabric.StoredSubjectAbsent
		if _, ok := g.nodes[graphrank.SubjectKey(ref)]; ok {
			out[index] = contextfabric.StoredSubjectAdmitted
		}
	}
	return out, nil
}

func (g admitAllGraph) DirectEdgePage(_ context.Context, _ storage.Principal, _ contextfabric.ResolvedGraphBinding, _ EdgePageQuery) (EdgePage, error) {
	return EdgePage{Edges: g.edges}, nil
}

// CHAOS-12299 E2: the relation reaches EdgeGate in the owned_by scan
// (subject_lookup_modes.go, scanOwnedBy). Today a project-only caller never
// gets there (its team root is denied, and the read asks only OWNED_BY_TEAM),
// so the graph here admits every end and ignores the type: an attribution
// edge must still be withheld on its relation alone.
func TestTheOwnedByScanWithholdsAnAttributionEdgeFromAProjectOnlyCaller(t *testing.T) {
	team := subject(contractsv1.ContextFabricSubjectTeam, "team:t")
	project := subject(contractsv1.ContextFabricSubjectProject, "project.v2:github:P-1")
	open := map[string]interface{}{"authorization_projects": "*", "authorization_repositories": "*"}
	for _, tc := range []struct {
		relation string
		want     int
	}{
		{relation: string(contractsv1.ContextFabricRelationshipDocumentedBy), want: 0},
		{relation: string(contractsv1.ContextFabricRelationshipHasEpisode), want: 0},
		{relation: string(contractsv1.ContextFabricRelationshipOwnedByTeam), want: 1},
	} {
		graph := admitAllGraph{&fakeEdgeGraph{fakeGraph: &fakeGraph{org: orgA, nodes: map[string]map[string]interface{}{
			graphrank.SubjectKey(team):    {},
			graphrank.SubjectKey(project): {},
		}}}}
		graph.edges = []EdgeCandidate{edgeBetween("rel-1", tc.relation, project, team, open)}
		lookup := &SubjectLookup{gate: NewSubjectGate(graph, nil), edges: graph}
		principal := storage.Principal{OrgID: orgA, Subject: "u", CredentialID: "c", ProjectScopes: []string{"project.v2:github:P-1"}}
		found, _, err := lookup.scanOwnedBy(relCtx("owned-by-"+tc.relation), principal, contextfabric.ResolvedGraphBinding{}, findPlan{owner: team.CanonicalID, kinds: []string{string(project.Kind)}})
		if err != nil {
			t.Fatalf("%s: %v", tc.relation, err)
		}
		if len(found) != tc.want {
			t.Errorf("%s: found %d subjects, want %d", tc.relation, len(found), tc.want)
		}
	}
}

// CHAOS-12299 r1 F3, no change: withheld.edges_not_visible and page.examined
// are the existing disclosure contract (missing is not healthy), the same for
// a restricted caller. This pins today's counts for a project-only caller so
// a change to them is deliberate: a granted project read with one
// OWNED_BY_TEAM edge whose team end is denied serves no edge, examines one and
// withholds one.
func TestAProjectOnlyCallerSeesTheWithheldEdgeCountsOfTheDisclosureContract(t *testing.T) {
	granted := subject(contractsv1.ContextFabricSubjectProject, "project.v2:github:P-1")
	team := subject(contractsv1.ContextFabricSubjectTeam, "team:t")
	node := func(ref contextfabric.SubjectRef) map[string]interface{} {
		return map[string]interface{}{"subject_kind": string(ref.Kind), "canonical_id": ref.CanonicalID, "authorization_repositories": "*", "authorization_projects": "*"}
	}
	graph := &fakeEdgeGraph{fakeGraph: &fakeGraph{org: orgA, nodes: map[string]map[string]interface{}{
		graphrank.SubjectKey(granted): node(granted),
		graphrank.SubjectKey(team):    node(team),
	}}}
	graph.edges = []EdgeCandidate{edgeBetween("rel-owned", string(contractsv1.ContextFabricRelationshipOwnedByTeam), granted, team, map[string]interface{}{"authorization_projects": "*", "authorization_repositories": "*"})}
	reader, _ := newRelReader(graph, nil)
	principal := storage.Principal{OrgID: orgA, Subject: "u", CredentialID: "c", ProjectScopes: []string{"project.v2:github:P-1"}}
	response, err := reader.Read(relCtx("project-only-counts"), principal, RelationshipsRequest{
		Subject: RelationshipsSubject{Kind: string(granted.Kind), CanonicalID: granted.CanonicalID}, Direction: "out", Limit: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Edges) != 0 || response.Page.Examined != 1 || response.Withheld.EdgesNotVisible != 1 {
		t.Fatalf("edges %d, examined %d, edges_not_visible %d; want 0, 1, 1 (the disclosure contract)", len(response.Edges), response.Page.Examined, response.Withheld.EdgesNotVisible)
	}
}

// #1850 r1 P3: each new withheld reason reaches the public total and its own
// telemetry count. E2 is withheld on the relation, E1 on the edge's project
// list; both ends are admitted, so only the edge decides.
func TestEachProjectOnlyWithheldReasonReachesThePublicTotal(t *testing.T) {
	const grant = "project.v2:github:P-1"
	granted := subject(contractsv1.ContextFabricSubjectProject, grant)
	other := subject(contractsv1.ContextFabricSubjectProject, "project.v2:github:P-2")
	node := func(ref contextfabric.SubjectRef) map[string]interface{} {
		return map[string]interface{}{"subject_kind": string(ref.Kind), "canonical_id": ref.CanonicalID}
	}
	for _, tc := range []struct {
		name     string
		relation contractsv1.ContextFabricRelationshipType
		attrs    map[string]interface{}
		reason   EdgeWithheldReason
	}{
		{"attribution relation", contractsv1.ContextFabricRelationshipDocumentedBy, map[string]interface{}{"authorization_projects": "*"}, EdgeWithheldAttributionRelation},
		{"project list", contractsv1.ContextFabricRelationshipBlocks, map[string]interface{}{"authorization_projects": []string{"project.v2:github:OTHER"}}, EdgeWithheldProjectList},
	} {
		t.Run(tc.name, func(t *testing.T) {
			graph := &fakeEdgeGraph{fakeGraph: &fakeGraph{org: orgA, nodes: map[string]map[string]interface{}{
				graphrank.SubjectKey(granted): node(granted),
				graphrank.SubjectKey(other):   node(other),
			}}}
			graph.edges = []EdgeCandidate{edgeBetween("rel-1", string(tc.relation), granted, other, tc.attrs)}
			reader, recorder := newRelReader(graph, nil)
			principal := storage.Principal{OrgID: orgA, Subject: "u", CredentialID: "c", ProjectScopes: []string{grant, other.CanonicalID}}
			response, err := reader.Read(relCtx("project-only-reasons"), principal, RelationshipsRequest{
				Subject: RelationshipsSubject{Kind: string(granted.Kind), CanonicalID: granted.CanonicalID}, Direction: "out", Limit: 100,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(response.Edges) != 0 || response.Withheld.EdgesNotVisible != 1 {
				t.Fatalf("edges %d, edges_not_visible %d; want 0 and 1", len(response.Edges), response.Withheld.EdgesNotVisible)
			}
			if len(recorder.reads) != 1 || recorder.reads[0].EdgesWithheld[tc.reason] != 1 {
				t.Fatalf("reads %+v, want one record counting %q once", recorder.reads, tc.reason)
			}
		})
	}
}
