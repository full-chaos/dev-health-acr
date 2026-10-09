package falkorgraph

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestFindSubjectsByExactNameTeamAdmitsActiveRowsOnly(t *testing.T) {
	var cypher string
	fake := &fakeConn{queryFunc: func(_ context.Context, _, q string, _ map[string]interface{}, _ bool) ([]row, error) {
		cypher = q
		return []row{
			lookupRow("org-1", "team", "team:platform", "Platform", map[string]interface{}{propPropertyPrefix + "is_active": false}),
			lookupRow("org-1", "team", "team:jira:platform", "Platform", map[string]interface{}{propPropertyPrefix + "is_active": true}),
		}, nil
	}}
	page, err := newFakeAdapter(t, fake).FindSubjectsByExactName(context.Background(), storage.Principal{OrgID: "org-1"}, lookupBinding, "Platform", "team", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Nodes) != 1 || page.Nodes[0].CanonicalID != "team:jira:platform" {
		t.Fatalf("nodes = %+v, want only the active keyed team", page.Nodes)
	}
	if !strings.Contains(cypher, teamActiveProperty) {
		t.Fatalf("team name query carries no active-team predicate: %s", cypher)
	}
}

func TestFindSubjectsByExactNameNonTeamKindCarriesNoActiveTeamPredicate(t *testing.T) {
	var cypher string
	fake := &fakeConn{queryFunc: func(_ context.Context, _, q string, _ map[string]interface{}, _ bool) ([]row, error) {
		cypher = q
		return nil, nil
	}}
	if _, err := newFakeAdapter(t, fake).FindSubjectsByExactName(context.Background(), storage.Principal{OrgID: "org-1"}, lookupBinding, "Platform", "project", "", 10); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(cypher, teamActiveProperty) {
		t.Fatalf("project name query carries the team predicate: %s", cypher)
	}
}

func inactiveBareAndActiveKeyedRows() []row {
	return []row{
		lookupRow("org-1", "team", "team:platform", "Platform", map[string]interface{}{propPropertyPrefix + "is_active": false}),
		lookupRow("org-1", "team", "team:jira:platform", "Platform", map[string]interface{}{propPropertyPrefix + "is_active": true}),
	}
}

func TestExactNameCensusAdmitsActiveTeamsOnly(t *testing.T) {
	fake := &fakeConn{queryFunc: func(context.Context, string, string, map[string]interface{}, bool) ([]row, error) {
		return inactiveBareAndActiveKeyedRows(), nil
	}}
	nodes, _, err := newFakeAdapter(t, fake).chaos4348ExactNameCandidates(context.Background(), "k", "org-1", temporalFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Fatalf("census returned %d nodes, want 1", len(nodes))
	}
}

func TestCohortKindCensusAdmitsActiveTeamsOnly(t *testing.T) {
	fake := &fakeConn{queryFunc: func(context.Context, string, string, map[string]interface{}, bool) ([]row, error) {
		return inactiveBareAndActiveKeyedRows(), nil
	}}
	nodes, _, err := newFakeAdapter(t, fake).cohortKindCensusCandidates(context.Background(), "k", "org-1", []string{"team"}, temporalFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Fatalf("census returned %d nodes, want 1", len(nodes))
	}
}

func TestFulltextSearchAdmitsActiveTeamsOnly(t *testing.T) {
	fake := &fakeConn{queryFunc: func(context.Context, string, string, map[string]interface{}, bool) ([]row, error) {
		var rows []row
		for _, r := range inactiveBareAndActiveKeyedRows() {
			rows = append(rows, row{"node": r["n"], "score": 1.0})
		}
		return rows, nil
	}}
	nodes, _, err := newFakeAdapter(t, fake).fulltextSearchNodes(context.Background(), "k", "org-1", "Platform", 10, temporalFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Fatalf("search returned %d nodes, want 1", len(nodes))
	}
}

func TestInactiveTeamNodeOnlyTeamsWithAnExplicitFalse(t *testing.T) {
	cases := []struct {
		name string
		n    *node
		want bool
	}{
		{"inactive team", &node{Properties: map[string]interface{}{propKind: "team", teamActiveProperty: false}}, true},
		{"active team", &node{Properties: map[string]interface{}{propKind: "team", teamActiveProperty: true}}, false},
		{"team without the property", &node{Properties: map[string]interface{}{propKind: "team"}}, false},
		{"inactive project", &node{Properties: map[string]interface{}{propKind: "project", teamActiveProperty: false}}, false},
		{"nil node", nil, false},
	}
	for _, c := range cases {
		if got := inactiveTeamNode(c.n); got != c.want {
			t.Errorf("%s: inactiveTeamNode = %t, want %t", c.name, got, c.want)
		}
	}
}
