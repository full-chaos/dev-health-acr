package devhealthfacts_test

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const retiredCapabilityClause = "HAVING argMax(item_count, computed_at) > 0"

func assertCapabilityStatementsCarryRetirementClause(t *testing.T, client *fakeClient) {
	t.Helper()
	seen := 0
	for _, q := range client.queries {
		if !strings.Contains(q.statement, "work_item_measure_capability") {
			continue
		}
		seen++
		if got, want := strings.Count(q.statement, "work_item_measure_capability"), strings.Count(q.statement, retiredCapabilityClause); got != want {
			t.Errorf("statement reads work_item_measure_capability %d times but carries the retirement clause %d times:\n%s", got, want, q.statement)
		}
	}
	if seen == 0 {
		t.Fatal("no captured statement reads work_item_measure_capability")
	}
}

func TestFlowTeamCapabilityReadsCarryRetirementClause(t *testing.T) {
	t.Parallel()
	client, _ := readTeamFlowWith(t, workItemMetricsDailyRow("team-1", "scope-a", 10, 6, 4),
		[]any{"team-1", "2026-02-21", int64(10), int64(6), int64(4), 0.0, uint8(0), 0.0, uint8(0)})
	assertCapabilityStatementsCarryRetirementClause(t, client)
}

func TestFlowProjectCapabilityReadsCarryRetirementClause(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: "work_scope_id ORDER BY day DESC", rows: [][]any{
			projectWorkItemMetricsRow("linear", "proj-1", "team-1", "scope-a", 10, 6),
		}},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactFlow)
	if _, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactFlow, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-1")},
	}); err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	assertCapabilityStatementsCarryRetirementClause(t, client)
}
