package directread

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12299: the subject gate admits a project-only caller to a granted
// project by the node rule and reads no ownership reach for it; a team, a
// repository and a work item with no stated membership are not admitted.
func TestTheSubjectGateDecidesAProjectOnlyCallerByTheNodeRule(t *testing.T) {
	granted := subject(contractsv1.ContextFabricSubjectProject, "project.v2:github:P-1")
	other := subject(contractsv1.ContextFabricSubjectProject, "project.v2:github:P-2")
	team := subject(contractsv1.ContextFabricSubjectTeam, "team:t")
	repository := subject(contractsv1.ContextFabricSubjectRepository, "repository:acme/a")
	member := subject(contractsv1.ContextFabricSubjectWorkItem, "work_item.v2:m")
	unstated := subject(contractsv1.ContextFabricSubjectWorkItem, "work_item.v2:u")
	node := func(ref contextfabric.SubjectRef, extra map[string]interface{}) map[string]interface{} {
		attributes := map[string]interface{}{"subject_kind": string(ref.Kind), "canonical_id": ref.CanonicalID, "authorization_repositories": "*", "authorization_projects": "*"}
		for key, value := range extra {
			attributes[key] = value
		}
		return attributes
	}
	graph := &fakeGraph{
		org: orgA,
		nodes: map[string]map[string]interface{}{
			graphrank.SubjectKey(granted):    node(granted, nil),
			graphrank.SubjectKey(other):      node(other, nil),
			graphrank.SubjectKey(team):       node(team, nil),
			graphrank.SubjectKey(repository): node(repository, map[string]interface{}{"authorization_repositories": []string{"acme/a"}}),
			graphrank.SubjectKey(member):     node(member, map[string]interface{}{graphrank.AuthorizationProjectIdentitiesAttr: []string{"project.v2:github:P-1"}}),
			graphrank.SubjectKey(unstated):   node(unstated, nil),
		},
		reach: map[string][]string{graphrank.SubjectKey(granted): {"acme/a"}, graphrank.SubjectKey(team): {"acme/a"}},
	}
	principal := storage.Principal{OrgID: orgA, Subject: "u", CredentialID: "c", RepositoryScopes: []string{"acme/a"}, ProjectScopes: []string{"project.v2:github:P-1"}}
	_, decision := NewSubjectGate(graph, nil).Authorize(context.Background(), principal, []contextfabric.SubjectRef{granted, other, team, repository, member, unstated})
	if graph.reachCalls != 0 {
		t.Errorf("ownership reach read %d times for a project-only caller", graph.reachCalls)
	}
	for ref, want := range map[contextfabric.SubjectRef]bool{granted: true, member: true, other: false, team: false, repository: false, unstated: false} {
		if got := outcomeOf(t, decision, ref) == SubjectAdmitted; got != want {
			t.Errorf("%v: admitted = %v (%s), want %v", ref, got, outcomeOf(t, decision, ref), want)
		}
	}
}

// E2 at the direct edge gate: an attribution edge is withheld from a
// project-only caller even when both ends were admitted; a structural edge
// with a "*" project list is decided by its ends.
func TestTheEdgeGateDeniesAProjectOnlyCallerAnAttributionEdge(t *testing.T) {
	principal := storage.Principal{OrgID: orgA, ProjectScopes: []string{"project.v2:github:P-1"}}
	open := map[string]interface{}{"authorization_projects": "*", "authorization_repositories": "*"}
	for _, relation := range []string{string(contractsv1.ContextFabricRelationshipDocumentedBy), string(contractsv1.ContextFabricRelationshipHasEpisode)} {
		// CHAOS-12300: the basis is named, E2 (attribution_relation), not
		// lumped into the edge's attributes.
		if got := EdgeGate(principal, relation, open, true, true); got != EdgeWithheldAttributionRelation {
			t.Errorf("%s: %s, want withheld as an attribution relation", relation, got)
		}
	}
	if got := EdgeGate(principal, string(contractsv1.ContextFabricRelationshipBelongsToProject), open, true, true); got != EdgeVisible {
		t.Errorf("BELONGS_TO_PROJECT with admitted ends: %s, want visible", got)
	}
	if got := EdgeGate(principal, string(contractsv1.ContextFabricRelationshipBelongsToProject), open, true, false); got != EdgeWithheldTarget {
		t.Errorf("BELONGS_TO_PROJECT with a refused end: %s, want withheld on its target", got)
	}
}

// E2 in the relationships reader: the relation reaches the edge gate, so an
// attribution edge is withheld from a project-only caller even between two
// admitted ends (a real one ends at a document or episode node, which the node
// rule denies too). The structural control edge is served.
func TestTheRelationshipsReaderWithholdsAnAttributionEdgeFromAProjectOnlyCaller(t *testing.T) {
	granted := subject(contractsv1.ContextFabricSubjectProject, "project.v2:github:P-1")
	member := subject(contractsv1.ContextFabricSubjectWorkItem, "work_item.v2:m")
	attrs := func(ref contextfabric.SubjectRef, extra map[string]interface{}) map[string]interface{} {
		out := map[string]interface{}{"subject_kind": string(ref.Kind), "canonical_id": ref.CanonicalID, "authorization_repositories": "*", "authorization_projects": "*"}
		for key, value := range extra {
			out[key] = value
		}
		return out
	}
	graph := &fakeEdgeGraph{fakeGraph: &fakeGraph{org: orgA, nodes: map[string]map[string]interface{}{
		graphrank.SubjectKey(granted): attrs(granted, nil),
		graphrank.SubjectKey(member):  attrs(member, map[string]interface{}{graphrank.AuthorizationProjectIdentitiesAttr: []string{"project.v2:github:P-1"}}),
	}}}
	open := map[string]interface{}{"authorization_projects": "*", "authorization_repositories": "*"}
	graph.edges = []EdgeCandidate{
		edgeBetween("rel-documented", string(contractsv1.ContextFabricRelationshipDocumentedBy), member, granted, open),
		edgeBetween("rel-belongs", string(contractsv1.ContextFabricRelationshipBelongsToProject), member, granted, open),
	}
	reader, _ := newRelReader(graph, nil)
	principal := storage.Principal{OrgID: orgA, Subject: "u", CredentialID: "c", ProjectScopes: []string{"project.v2:github:P-1"}}
	_, ids := readAll(t, reader, principal, RelationshipsRequest{
		Subject: RelationshipsSubject{Kind: string(granted.Kind), CanonicalID: granted.CanonicalID}, Direction: "in", Limit: 100,
	})
	if len(ids) != 1 || ids[0] != "rel-belongs" {
		t.Fatalf("served %v, want only the structural edge", ids)
	}
}

// CHAOS-12330: read_facts names a project-only refusal, on a kind read directly
// and on one reached only through scope expansion. No provider and no expander
// runs, the read is partial, and the response is valid against the schema.
func TestReadFactsNamesAProjectOnlyRefusal(t *testing.T) {
	principal := storage.Principal{OrgID: orgA, Subject: "u", CredentialID: "c", ProjectScopes: []string{projectP.CanonicalID}}
	graph := graphOfOrgA()
	graph.nodes[graphrank.SubjectKey(projectP)] = map[string]interface{}{"subject_kind": "project", "canonical_id": projectP.CanonicalID, "authorization_repositories": "*", "authorization_projects": "*", "authorization_teams": "*"}
	direct := perSubjectProvider(contextfabric.FactHealth, contractsv1.ContextFabricSubjectProject)
	expanded := perSubjectProvider(contextfabric.FactBlockers, contractsv1.ContextFabricSubjectWorkItem)
	expander := &scriptedExpander{expand: expandTo(labelled(workA, "WI-A"))}
	reader := directExpandingReader(t, graph, expander, nil, direct, expanded)
	response, err := reader.Read(requestContext(), principal, FactsRequest{
		Kinds: []string{"health", "blockers"}, Subjects: []RequestSubject{{Kind: "project", CanonicalID: projectP.CanonicalID}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(direct.queries) != 0 || len(expanded.queries) != 0 || len(expander.requests) != 0 {
		t.Fatalf("provider queries %d and %d, expansions %d, want none", len(direct.queries), len(expanded.queries), len(expander.requests))
	}
	if response.Status != StatusPartial || len(response.Facts) != 0 {
		t.Fatalf("status %q, facts %d", response.Status, len(response.Facts))
	}
	for _, kind := range []string{"health", "blockers"} {
		row := coverageOf(t, response, kind, projectP)
		if row.Outcome != OutcomeWithheldProjectScope || row.KindState != string(contextfabric.SourceUnauthorized) {
			t.Fatalf("%s row = %+v", kind, row)
		}
		if want := contractsv1.ContextFabricProjectScopeWithheldFactLabel(contextfabric.FactKind(kind)); row.Reason != want {
			t.Fatalf("%s reason = %q, want %q", kind, row.Reason, want)
		}
	}
	validateAgainst(t, "mcp_read_facts_response.v1.schema.json", []byte(mustJSON(t, response)))

	// Another class reads the same kinds: the control that makes the zeros
	// above mean something.
	control := perSubjectProvider(contextfabric.FactHealth, contractsv1.ContextFabricSubjectProject)
	served, err := directExpandingReader(t, graphOfOrgA(), expander, nil, control).Read(requestContext(), unrestrictedA(), FactsRequest{
		Kinds: []string{"health"}, Subjects: []RequestSubject{{Kind: "project", CanonicalID: projectP.CanonicalID}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(control.queries) != 1 || coverageOf(t, served, "health", projectP).Outcome != OutcomeFactServed {
		t.Fatalf("control: queries %d, response %s", len(control.queries), mustJSON(t, served))
	}
}
