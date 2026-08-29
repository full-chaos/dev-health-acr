package devhealthfacts_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestCHAOS4418RepositorySeriesAllTimeWindowReadsEveryDay is the provider
// half of codex R4 finding 2. Once the engine threads its canonical
// effective window into the fact read (contextfabric.factReadQuestion), the
// all_time sentinel arrives here as a RelativeID with NO bounds -- because
// no bounds exist for it by definition (window.go's relativeWindowBounds).
// Falling through to the 90-day default for it would answer a
// question about all of history with one quarter of it, which is exactly
// the silent-substitution defect this finding names. "All of history" is
// no day predicate at all; the series stays bounded by the per-repository
// LIMIT and the 64-row per-fact cap, never by an invented window.
func TestCHAOS4418RepositorySeriesAllTimeWindowReadsEveryDay(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM repo_metrics_daily", rows: [][]any{metricsRow("repo-1")}}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactMetrics)
	_, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{
			Axis:           contextfabric.TemporalCurrent,
			EvidenceWindow: &contractsv1.ContextFabricRequestedEvidenceWindow{RelativeID: contractsv1.ContextFabricRelativeWindowAllTime},
		},
		Kind: contextfabric.FactMetrics, Subjects: []contextfabric.SubjectRef{repoSubject("repo-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	last := client.queries[len(client.queries)-1]
	for _, binding := range last.bindings {
		if binding.Name == "series_window_start" || binding.Name == "series_window_end" {
			t.Fatalf("query bound %q = %v for an all_time window -- all of history must carry NO day bound, never a 90-day default standing in for it", binding.Name, binding.Value)
		}
	}
	if strings.Contains(last.statement, "day >=") || strings.Contains(last.statement, "day <=") {
		t.Fatalf("statement = %q, want no day predicate for an all_time window", last.statement)
	}
}

// TestCHAOS4418RepositorySeriesUsesCanonicalBoundsOverTheRelativeID pins
// the precedence the engine's own threading relies on: a bounded relative
// window arrives here carrying BOTH its RelativeID and the absolute bounds
// the engine already derived from it, and the bounds are what the query
// uses. Re-deriving the id here would duplicate relativeWindowBounds -- the
// only function allowed to do that -- against a different clock, so the
// read could span a different month than the answer advertises.
func TestCHAOS4418RepositorySeriesUsesCanonicalBoundsOverTheRelativeID(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM repo_metrics_daily", rows: [][]any{metricsRow("repo-1")}}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactMetrics)
	start := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	end := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	_, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{
			Axis: contextfabric.TemporalCurrent,
			EvidenceWindow: &contractsv1.ContextFabricRequestedEvidenceWindow{
				Start: &start, End: &end, RelativeID: contractsv1.ContextFabricRelativeWindowTrailing30D,
			},
		},
		Kind: contextfabric.FactMetrics, Subjects: []contextfabric.SubjectRef{repoSubject("repo-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if got := windowBinding(t, client, "series_window_start"); !got.Equal(start) {
		t.Fatalf("series_window_start = %v, want the engine's own canonical %v", got, start)
	}
	if got := windowBinding(t, client, "series_window_end"); !got.Equal(end) {
		t.Fatalf("series_window_end = %v, want the engine's own canonical %v", got, end)
	}
}
