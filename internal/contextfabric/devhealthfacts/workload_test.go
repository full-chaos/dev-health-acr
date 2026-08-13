package devhealthfacts_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func workloadRow(teamID string) []any {
	return []any{teamID, float64(3.2), float64(0.8), uint8(1), int64(14), uint8(0), uint8(1), int64(120), "2026-07-27 04:00:00"}
}

func TestWorkloadProviderHappyPath(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM capacity_forecasts", rows: [][]any{workloadRow("CHAOS")}}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactWorkload)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactWorkload, Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %#v, want 1", result.Facts)
	}
	fact := result.Facts[0]
	if fact.Fields["throughput_mean"].Number == nil || *fact.Fields["throughput_mean"].Number != 3.2 {
		t.Fatalf("fields = %#v", fact.Fields)
	}
	if fact.Fields["high_variance"].Boolean == nil || !*fact.Fields["high_variance"].Boolean {
		t.Fatalf("fields = %#v", fact.Fields)
	}
	if fact.Fields["forecast_p50_days"].Integer == nil || *fact.Fields["forecast_p50_days"].Integer != 14 {
		t.Fatalf("fields = %#v", fact.Fields)
	}
	// Semantic-honesty guard: a workload fact must state, in its own
	// structure, that it is a capacity forecast, never a current-load
	// reading (team-lead review requirement).
	if fact.Fields["basis"].String == nil || *fact.Fields["basis"].String != "capacity_forecast" {
		t.Fatalf("fields = %#v, want basis=capacity_forecast", fact.Fields)
	}
}

func TestWorkloadProviderZeroRowSubjectHasNoFactEntry(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM capacity_forecasts", rows: nil}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactWorkload)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactWorkload, Subjects: []contextfabric.SubjectRef{teamSubject("ghost-team")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 0 || result.State != contextfabric.SourceAvailable {
		t.Fatalf("result = %+v", result)
	}
}

func TestWorkloadProviderQueryErrorReturnsFactReadFailure(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM capacity_forecasts", err: errors.New("boom")}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactWorkload)
	_, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactWorkload, Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS")},
	})
	var failure *contextfabric.FactReadFailure
	if !errors.As(err, &failure) || failure.State != contextfabric.SourceUnavailable {
		t.Fatalf("err = %v", err)
	}
}

// TestWorkloadProviderNoPersonLevelFields is the "no person-level workload
// output" guard (§19.6.3). capacity_forecasts has no per-person column at
// all, so this proves that structurally: every field name on a workload
// fact is one of the known team-level aggregate fields, never anything
// person-shaped.
func TestWorkloadProviderNoPersonLevelFields(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM capacity_forecasts", rows: [][]any{workloadRow("CHAOS")}}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactWorkload)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactWorkload, Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	allowed := map[string]bool{
		"basis": true, "throughput_mean": true, "throughput_stddev": true, "insufficient_history": true,
		"high_variance": true, "backlog_size": true, "computed_at": true, "forecast_p50_days": true,
	}
	for _, fact := range result.Facts {
		for field := range fact.Fields {
			if !allowed[field] {
				t.Fatalf("unexpected field %q on a workload fact -- possible person-level leak", field)
			}
		}
	}
}

func TestWorkloadProviderScopedToOrgAndRequestedSubjects(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM capacity_forecasts", rows: nil}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactWorkload)
	_, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-8"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactWorkload, Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if got := client.orgIDBinding(); got != "org-8" {
		t.Fatalf("org_id binding = %q", got)
	}
	if got := client.idsBinding(); len(got) != 1 || got[0] != "CHAOS" {
		t.Fatalf("ids binding = %#v, want exactly the requested subject", got)
	}
	assertQueryScopedToOrgAndSubjects(t, client.queries[len(client.queries)-1].statement)
}

const maxWorkloadRowsPerQueryForTest = 200

func workloadRows(n int) [][]any {
	rows := make([][]any, n)
	for i := 0; i < n; i++ {
		rows[i] = workloadRow("team-" + strconv.Itoa(i))
	}
	return rows
}

func TestWorkloadProviderTruncatesWhenRowCountReachesLimit(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM capacity_forecasts", rows: workloadRows(maxWorkloadRowsPerQueryForTest)}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactWorkload)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactWorkload, Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if !result.Truncated {
		t.Fatalf("result.Truncated = false, want true when the row count reaches the limit")
	}
	if len(client.queries) == 0 || !strings.Contains(strings.ToUpper(client.queries[len(client.queries)-1].statement), "LIMIT") {
		t.Fatalf("query statement = %#v, want a LIMIT clause", client.queries)
	}
}
