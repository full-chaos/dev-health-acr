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

func TestEveryTeamCandidateQueryCarriesTheActiveTeamCypher(t *testing.T) {
	var captured []string
	fake := &fakeConn{queryFunc: func(_ context.Context, _, q string, _ map[string]interface{}, _ bool) ([]row, error) {
		captured = append(captured, q)
		return nil, nil
	}}
	adapter := newFakeAdapter(t, fake)
	ctx := context.Background()
	principal := storage.Principal{OrgID: "org-1"}
	reads := map[string]func() error{
		"list by kind": func() error {
			_, err := adapter.ListSubjectsByKind(ctx, principal, lookupBinding, "team", "", 10)
			return err
		},
		"exact name": func() error {
			_, err := adapter.FindSubjectsByExactName(ctx, principal, lookupBinding, "Platform", "team", "", 10)
			return err
		},
		"exact census": func() error {
			_, _, err := adapter.chaos4348ExactNameCandidates(ctx, "k", "org-1", temporalFilter{})
			return err
		},
		"cohort census": func() error {
			_, _, err := adapter.cohortKindCensusCandidates(ctx, "k", "org-1", []string{"team"}, temporalFilter{})
			return err
		},
		"fulltext": func() error {
			_, _, err := adapter.fulltextSearchNodes(ctx, "k", "org-1", "Platform", 10, temporalFilter{})
			return err
		},
	}
	for name, read := range reads {
		captured = nil
		if err := read(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(captured) == 0 {
			t.Fatalf("%s: no query issued", name)
		}
		for _, q := range captured {
			if !strings.Contains(q, teamActiveProperty) {
				t.Errorf("%s: query lacks the active-team clause: %s", name, q)
			}
		}
	}
}

func TestListSubjectsByKindOmitsInactiveTeams(t *testing.T) {
	fake := &fakeConn{queryFunc: func(context.Context, string, string, map[string]interface{}, bool) ([]row, error) {
		return inactiveBareAndActiveKeyedRows(), nil
	}}
	page, err := newFakeAdapter(t, fake).ListSubjectsByKind(context.Background(), storage.Principal{OrgID: "org-1"}, lookupBinding, "team", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Nodes) != 1 || page.Nodes[0].CanonicalID != "team:jira:platform" {
		t.Fatalf("nodes = %+v, want only the active keyed team", page.Nodes)
	}
}
