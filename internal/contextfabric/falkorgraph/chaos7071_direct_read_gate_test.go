package falkorgraph

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// chaos7071Graph is a tiny in-memory graph behind the REAL Adapter: node
// attributes come from the real projection conversion
// (subjectAuthorizationAttrsForTest -> authorizationValue), so a project
// carries the "*" repository list production writes for every project.
type chaos7071Graph struct {
	nodes []*node
	// ownedBy: project canonical id -> OWNED_BY_TEAM edges to teams.
	ownedBy map[string][]chaos7071Edge
	queries []string
}

type chaos7071Edge struct {
	teamID    string
	validToNs interface{} // nil = open-ended
}

func chaos7071Node(org string, kind contextfabric.SubjectKind, id string, scope contextfabric.AuthorizationScope) *node {
	props := map[string]interface{}{propOrgID: org, propKind: string(kind), propCanonicalID: id, propLabel: id}
	for key, value := range subjectAuthorizationAttrsForTest(scope) {
		props[key] = value
	}
	return &node{Properties: props}
}

func (g *chaos7071Graph) find(org, kind, id string) *node {
	for _, n := range g.nodes {
		if n.Properties[propOrgID] == org && n.Properties[propKind] == kind && n.Properties[propCanonicalID] == id {
			return n
		}
	}
	return nil
}

func (g *chaos7071Graph) query(_ context.Context, _ string, cypher string, params map[string]interface{}, _ bool) ([]row, error) {
	g.queries = append(g.queries, cypher)
	org, _ := params["org"].(string)
	switch {
	case strings.Contains(cypher, "UNWIND $targets"):
		var rows []row
		targets, _ := params["targets"].([]interface{})
		for _, raw := range targets {
			target := raw.(map[string]interface{})
			kind, _ := target["kind"].(string)
			if n := g.find(org, kind, target["id"].(string)); n != nil {
				rows = append(rows, row{"n": n})
			}
		}
		return rows, nil
	case strings.Contains(cypher, "UNWIND $ids") && strings.Contains(cypher, "-[r:"):
		now, _ := params[temporalParamStart].(int64)
		var rows []row
		for _, raw := range params["ids"].([]interface{}) {
			id := raw.(string)
			if g.find(org, "project", id) == nil {
				continue
			}
			for _, edge := range g.ownedBy[id] {
				if end, ok := edge.validToNs.(int64); ok && end <= now {
					continue
				}
				if team := g.find(org, "team", edge.teamID); team != nil {
					rows = append(rows, row{"id": id, "t": team})
				}
			}
		}
		return rows, nil
	case strings.Contains(cypher, "UNWIND $ids"):
		var rows []row
		for _, raw := range params["ids"].([]interface{}) {
			id := raw.(string)
			if team := g.find(org, "team", id); team != nil {
				rows = append(rows, row{"id": id, "t": team})
			}
		}
		return rows, nil
	}
	return nil, nil
}

// TestChaos7071DirectReadGateOverTheRealAdapter is BUILD ENTRY S0's T1
// scenario end to end through falkorgraph: restricted principal granted
// repository A; subjects A, B, a guessed id, project P that reaches only B
// (its OWNED_BY_TEAM edge to T has ENDED), project Q that reaches A, team T
// that owns A and B, team U that owns only B, the caller's org, another
// org's repository.
func TestChaos7071DirectReadGateOverTheRealAdapter(t *testing.T) {
	const org, other = "org-a", "org-b"
	ended := time.Now().Add(-24 * time.Hour).UnixNano()
	graph := &chaos7071Graph{
		nodes: []*node{
			chaos7071Node(org, "repository", "repository:a", contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/a"}}),
			chaos7071Node(org, "repository", "repository:b", contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/b"}}),
			chaos7071Node(org, "team", "team:t", contextfabric.AuthorizationScope{TeamIDs: []string{"team:t"}, RepositorySlugs: []string{"acme/a", "acme/b"}}),
			chaos7071Node(org, "team", "team:u", contextfabric.AuthorizationScope{TeamIDs: []string{"team:u"}, RepositorySlugs: []string{"acme/b"}}),
			chaos7071Node(org, "project", "project:p", contextfabric.AuthorizationScope{ProjectIDs: []string{"project:p"}}),
			chaos7071Node(org, "project", "project:q", contextfabric.AuthorizationScope{ProjectIDs: []string{"project:q"}}),
			chaos7071Node(other, "repository", "repository:x", contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/a"}}),
		},
		ownedBy: map[string][]chaos7071Edge{
			"project:p": {{teamID: "team:u"}, {teamID: "team:t", validToNs: ended}},
			"project:q": {{teamID: "team:t"}},
		},
	}
	adapter := newFakeAdapter(t, &fakeConn{queryFunc: graph.query})
	if got := graph.find(org, "project", "project:p").Properties[propAuthzRepos]; got != "*" {
		t.Fatalf("precondition: project node repository list %v, want the projection's \"*\"", got)
	}

	principal := storage.Principal{OrgID: org, Subject: "user-1", CredentialID: "cred-1", RepositoryScopes: []string{"acme/a"}}
	ref := func(kind contextfabric.SubjectKind, id string) contextfabric.SubjectRef {
		return contextfabric.SubjectRef{Kind: kind, CanonicalID: id}
	}
	want := []struct {
		subject contextfabric.SubjectRef
		outcome directread.SubjectOutcome
	}{
		{ref(contractsv1.ContextFabricSubjectRepository, "repository:a"), directread.SubjectAdmitted},
		{ref(contractsv1.ContextFabricSubjectRepository, "repository:b"), directread.SubjectDenied},
		{ref(contractsv1.ContextFabricSubjectRepository, "repository:guessed"), directread.SubjectAbsent},
		{ref(contractsv1.ContextFabricSubjectProject, "project:p"), directread.SubjectOwnershipUnproven},
		{ref(contractsv1.ContextFabricSubjectProject, "project:q"), directread.SubjectAdmitted},
		{ref(contractsv1.ContextFabricSubjectTeam, "team:t"), directread.SubjectAdmitted},
		{ref(contractsv1.ContextFabricSubjectTeam, "team:u"), directread.SubjectDenied},
		{ref(contractsv1.ContextFabricSubjectOrganization, org), directread.SubjectAdmitted},
		{ref(contractsv1.ContextFabricSubjectRepository, "repository:x"), directread.SubjectAbsent},
	}
	requested := make([]contextfabric.SubjectRef, len(want))
	for index, entry := range want {
		requested[index] = entry.subject
	}
	authorized, decision := directread.NewSubjectGate(adapter, nil).Authorize(context.Background(), principal, requested)
	if decision.Decision != directread.DecisionPartial {
		t.Fatalf("decision %s/%s err %v", decision.Decision, decision.Reason, decision.Err)
	}
	for index, entry := range want {
		if got := decision.Outcomes[index].Outcome; got != entry.outcome {
			t.Errorf("%s %s: %s, want %s", entry.subject.Kind, entry.subject.CanonicalID, got, entry.outcome)
		}
	}
	if authorized.Len() != 4 || !authorized.IssuedTo(principal) {
		t.Fatalf("authorized %v", authorized.Subjects())
	}
	for _, cypher := range graph.queries {
		if !strings.Contains(cypher, "$org") {
			t.Errorf("query without the org predicate: %s", cypher)
		}
	}
}

// The ownership reach itself: a wildcard team is no reach, an ended edge is
// no reach, the query binds the organization and the current instant.
func TestChaos7071OwnershipReachedRepositories(t *testing.T) {
	const org = "org-a"
	ended := time.Now().Add(-time.Hour).UnixNano()
	future := time.Now().Add(time.Hour).UnixNano()
	graph := &chaos7071Graph{
		nodes: []*node{
			chaos7071Node(org, "team", "team:t", contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/b", "acme/a"}}),
			chaos7071Node(org, "team", "team:w", contextfabric.AuthorizationScope{}), // pre-4390 wildcard team
			chaos7071Node(org, "team", "team:v", contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/c"}}),
			chaos7071Node(org, "project", "project:p", contextfabric.AuthorizationScope{ProjectIDs: []string{"project:p"}}),
		},
		ownedBy: map[string][]chaos7071Edge{
			"project:p": {{teamID: "team:t", validToNs: ended}, {teamID: "team:w"}, {teamID: "team:v", validToNs: future}},
		},
	}
	adapter := newFakeAdapter(t, &fakeConn{queryFunc: graph.query})
	subjects := []contextfabric.SubjectRef{
		{Kind: contractsv1.ContextFabricSubjectTeam, CanonicalID: "team:t"},
		{Kind: contractsv1.ContextFabricSubjectTeam, CanonicalID: "team:w"},
		{Kind: contractsv1.ContextFabricSubjectProject, CanonicalID: "project:p"},
		{Kind: contractsv1.ContextFabricSubjectRepository, CanonicalID: "repository:a"},
	}
	reach, err := adapter.OwnershipReachedRepositories(context.Background(), storage.Principal{OrgID: org}, contextfabric.ResolvedGraphBinding{}, subjects)
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"acme/a", "acme/b"}, {}, {"acme/c"}, {}}
	if len(reach) != len(want) {
		t.Fatalf("reach %v", reach)
	}
	for index := range want {
		if strings.Join(reach[index], ",") != strings.Join(want[index], ",") {
			t.Errorf("%v: reach %v, want %v", subjects[index], reach[index], want[index])
		}
	}
	for _, cypher := range graph.queries {
		if strings.Contains(cypher, "-[r:") && (!strings.Contains(cypher, "r."+propValidToNs) || !strings.Contains(cypher, "t."+propValidToNs) || !strings.Contains(cypher, "r."+propRelationType+" = $owned")) {
			t.Errorf("project reach query lacks the current-edge, current-team or relation predicate: %s", cypher)
		}
	}
	if _, err := adapter.OwnershipReachedRepositories(context.Background(), storage.Principal{}, contextfabric.ResolvedGraphBinding{}, subjects); err == nil {
		t.Fatal("reach without an organization served")
	}
}
