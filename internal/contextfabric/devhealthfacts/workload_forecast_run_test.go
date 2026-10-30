package devhealthfacts_test

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// workloadForecastRunMatch matches only the latest forecast run statement.
const workloadForecastRunMatch = "-- latest forecast run"

// readProjectWorkloadAgainstRun reads one project's workload fact whose
// freshest forecast day is freshestDay, with the organization's forecast run
// answered by run (nil: the store answers no row).
func readProjectWorkloadAgainstRun(t *testing.T, freshestDay string, run []any) (contextfabric.CanonicalFact, *fakeClient) {
	t.Helper()
	tables := []fakeTable{
		{match: workloadP50MaxMatch, rows: [][]any{workloadP50MaxRow("linear", "proj-1", 0, 0, 1, 0, "", "", false, false)}},
		{match: workloadBaseQueryMatch, rows: [][]any{
			workloadProjectRollupRow("linear", "proj-1", "team-1", "Team One", "proj-1", 3.2, 0.8, 1, 1),
		}},
		{match: workloadDailySeriesMatch, rows: [][]any{
			workloadDailySeriesRow("linear:proj-1", freshestDay, 1, 13.9, 19.8),
			workloadDailySeriesRow("linear:proj-1", "2026-09-14", 5, 6.1, 13.0),
		}},
	}
	if run != nil {
		tables = append(tables, fakeTable{match: workloadForecastRunMatch, rows: [][]any{run}})
	}
	client := &fakeClient{tables: tables}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactWorkload)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactWorkload, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %d, want 1", len(result.Facts))
	}
	return result.Facts[0], client
}

func workloadStringField(fact contextfabric.CanonicalFact, name string) string {
	if value := fact.Fields[name]; value.String != nil {
		return *value.String
	}
	return ""
}

// A project forecast that the organization's newest run did not refresh is
// stale: its backlog is the in-progress count of its own older day. The value
// is kept as read, never zeroed, and both days are served.
func TestWorkloadProjectForecastOlderThanTheLatestRunIsStale(t *testing.T) {
	t.Parallel()
	fact, client := readProjectWorkloadAgainstRun(t, "2026-10-02", []any{"2026-10-26", uint64(254)})
	if fact.SourceState != contextfabric.SourceStale {
		t.Errorf("source state = %q, want stale", fact.SourceState)
	}
	if got := workloadStringField(fact, "day"); got != "2026-10-02" {
		t.Errorf("day = %q, want the freshest forecast day 2026-10-02", got)
	}
	if got := workloadStringField(fact, "latest_run_day"); got != "2026-10-26" {
		t.Errorf("latest_run_day = %q, want 2026-10-26", got)
	}
	if backlog := fact.Fields["backlog_size"].Integer; backlog == nil || *backlog != 1 {
		t.Errorf("backlog_size = %#v, want the read value 1", fact.Fields["backlog_size"])
	}
	var run *capturedQuery
	for index := range client.queries {
		if strings.Contains(client.queries[index].statement, workloadForecastRunMatch) {
			run = &client.queries[index]
		}
	}
	if run == nil {
		t.Fatal("the latest forecast run was not read")
	}
	if !strings.Contains(run.statement, "FROM capacity_forecasts") || !strings.Contains(run.statement, "org_id = {org_id:String}") {
		t.Errorf("run statement = %q, want the organization's capacity_forecasts", run.statement)
	}
}

// A project forecast of the newest run's day is current.
func TestWorkloadProjectForecastOfTheLatestRunIsNotStale(t *testing.T) {
	t.Parallel()
	fact, _ := readProjectWorkloadAgainstRun(t, "2026-10-26", []any{"2026-10-26", uint64(254)})
	if fact.SourceState == contextfabric.SourceStale {
		t.Errorf("source state = stale, want the read's own state for a forecast of the latest run")
	}
	if got := workloadStringField(fact, "latest_run_day"); got != "2026-10-26" {
		t.Errorf("latest_run_day = %q, want 2026-10-26", got)
	}
}

// With no forecast run known, no day can be compared: the fact is not called
// stale and serves no latest_run_day.
func TestWorkloadProjectForecastWithNoKnownRunIsNotCalledStale(t *testing.T) {
	t.Parallel()
	for name, run := range map[string][]any{"no row": nil, "zero forecasts": {"1970-01-01", uint64(0)}} {
		fact, _ := readProjectWorkloadAgainstRun(t, "2026-10-02", run)
		if fact.SourceState == contextfabric.SourceStale {
			t.Errorf("%s: source state = stale, want not stale when no run is known", name)
		}
		if _, served := fact.Fields["latest_run_day"]; served {
			t.Errorf("%s: latest_run_day = %#v, want absent", name, fact.Fields["latest_run_day"])
		}
	}
}
