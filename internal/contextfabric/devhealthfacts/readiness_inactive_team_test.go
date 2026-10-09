package devhealthfacts_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The inactive-team lookup is bound to the team ids of the rows in hand, never
// the organization's whole inactive list: with 250 teams every id reaches the
// lookup, and a retired team is dropped from the breakdown.
func TestProjectReadinessDropsAnInactiveTeamAmongManyTeams(t *testing.T) {
	t.Parallel()
	var rows [][]any
	const teams = 250
	for i := 0; i < teams; i++ {
		rows = append(rows, readinessProjectRollupRow("linear", "proj-1", fmt.Sprintf("team-%03d", i), "Team", fmt.Sprintf("scope-%03d", i), "linear", 18, 2, 20, 0.9))
	}
	client := &fakeClient{tables: []fakeTable{
		{match: "is_active = 0 ORDER BY id", rows: [][]any{{"team-249"}}},
		{match: readinessOriginalQueryMatch, rows: rows},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactReadiness)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactReadiness, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	var lookup *capturedQuery
	for i := range client.queries {
		if strings.Contains(client.queries[i].statement, "is_active = 0 ORDER BY id") {
			lookup = &client.queries[i]
		}
	}
	if lookup == nil {
		t.Fatal("no inactive-team lookup was issued")
	}
	var bound []string
	for _, b := range lookup.bindings {
		if b.Name == "ids" {
			bound, _ = b.Value.([]string)
		}
	}
	if len(bound) != teams {
		t.Fatalf("lookup bound %d team ids, want all %d of the rows in hand", len(bound), teams)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %d, want 1", len(result.Facts))
	}
	if got := result.Facts[0].Fields["team_count"].Integer; got == nil || *got != teams-1 {
		t.Fatalf("team_count = %v, want %d (the inactive team-249 dropped)", got, teams-1)
	}
}
