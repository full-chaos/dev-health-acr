package devhealthfacts_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func rangeQuery(kind contextfabric.FactKind, subject contextfabric.SubjectRef, exclusive bool) contextfabric.FactQuery {
	start := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	return contextfabric.FactQuery{
		Kind: kind, Subjects: []contextfabric.SubjectRef{subject}, TimeEndExclusive: exclusive,
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &start, End: &end},
	}
}

func capturedStatements(client *fakeClient) string {
	var all []string
	for _, q := range client.queries {
		all = append(all, q.statement)
	}
	return strings.Join(all, "\n")
}

func TestEndExclusiveRangeBoundsDayGrainByLessThan(t *testing.T) {
	t.Parallel()
	for _, exclusive := range []bool{true, false} {
		client := &fakeClient{tables: []fakeTable{{match: "FROM compounding_risk_daily", rows: [][]any{healthRow("repo-1")}}}}
		provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactHealth)
		if _, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, rangeQuery(contextfabric.FactHealth, repoSubject("repo-1"), exclusive)); err != nil {
			t.Fatal(err)
		}
		statements := capturedStatements(client)
		hasLess := strings.Contains(statements, "AND day < toDate(")
		hasLessEqual := strings.Contains(statements, "AND day <= toDate(")
		if exclusive && (!hasLess || hasLessEqual) {
			t.Errorf("exclusive end: want `AND day < toDate(` and no `AND day <= toDate(`; statements:\n%s", statements)
		}
		if !exclusive && (hasLess || !hasLessEqual) {
			t.Errorf("explicit range: the inclusive `AND day <= toDate(` must stay; statements:\n%s", statements)
		}
		if !strings.Contains(statements, "day >= toDate(") {
			t.Errorf("start bound lost: %s", statements)
		}
	}
}

func TestEndExclusiveRangeBoundsTimestampGrainByLessThan(t *testing.T) {
	t.Parallel()
	for _, exclusive := range []bool{true, false} {
		client := &fakeClient{tables: []fakeTable{
			{match: workloadBaseQueryMatch, rows: [][]any{workloadRow("CHAOS", "scope-a")}},
			{match: workloadDailySeriesMatch, rows: [][]any{workloadDailySeriesRow("CHAOS", "2026-10-20", 5, 1.5, 0.5)}},
		}}
		provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactWorkload)
		if _, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, rangeQuery(contextfabric.FactWorkload, teamSubject("CHAOS"), exclusive)); err != nil {
			t.Fatal(err)
		}
		statements := capturedStatements(client)
		hasLess := strings.Contains(statements, "computed_at < {time_end")
		if exclusive != hasLess {
			t.Errorf("exclusive=%v: `computed_at < {time_end` present=%v; statements:\n%s", exclusive, hasLess, statements)
		}
		// The readers-module base query keeps its own inclusive predicate: it is bound one microsecond before an
		// exclusive end, and at the end itself for an explicit range.
		end := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
		wantEnd := end
		if exclusive {
			wantEnd = end.Add(-time.Microsecond)
		}
		found := false
		for _, q := range client.queries {
			if !strings.Contains(q.statement, workloadBaseQueryMatch) {
				continue
			}
			for _, b := range q.bindings {
				if b.Name == "time_end" {
					found = true
					if got, ok := b.Value.(time.Time); !ok || !got.Equal(wantEnd) {
						t.Errorf("exclusive=%v: base query time_end = %v, want %v", exclusive, b.Value, wantEnd)
					}
				}
			}
		}
		if !found {
			t.Errorf("exclusive=%v: no time_end binding on the workload base query", exclusive)
		}
	}
}
