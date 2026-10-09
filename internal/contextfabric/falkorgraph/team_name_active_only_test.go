package falkorgraph

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestFindSubjectsByExactNameTeamAdmitsActiveRowsOnly(t *testing.T) {
	fake := &fakeConn{queryFunc: func(_ context.Context, _, _ string, _ map[string]interface{}, _ bool) ([]row, error) {
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
