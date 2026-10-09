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

// The stored span is per subject: two repositories whose earliest units differ
// get different spans from one read, and a team takes the earliest over the
// repositories it owns. Sentinel rows (window 255) carry each repository's
// earliest unit; they are never mix rows.
func TestInvestmentSpanIsPerSubject(t *testing.T) {
	t.Parallel()
	themes := map[string]float64{"feature_delivery": 1}
	spanA := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	spanB := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	mixA := themeMixRow("A", "", themes, 0)
	mixB := themeMixRow("B", "", themes, 0)
	sentinel := func(team string, from time.Time) []any {
		return []any{uint8(255), "repo-" + team, themes, 0.0, uint64(1), from}
	}
	tables := []fakeTable{
		{match: "FROM team_repo_ownership", rows: [][]any{{"T", "repo-A"}, {"T", "repo-B"}, {"U", "repo-B"}}},
		{match: "FROM work_unit_investments", rows: [][]any{mixA, mixB, sentinel("A", spanA), sentinel("B", spanB)}},
	}
	start := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	read := func(subjects ...contextfabric.SubjectRef) contextfabric.FactProviderResult {
		t.Helper()
		provider := findProvider(t, devhealthfacts.NewProviders(&fakeClient{tables: tables}), contextfabric.FactInvestment)
		result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &start, End: &end}, Kind: contextfabric.FactInvestment, Subjects: subjects,
		})
		if err != nil {
			t.Fatalf("ReadFacts: %v", err)
		}
		return result
	}
	repos := read(repoSubject("repo-A"), repoSubject("repo-B"))
	if !strings.Contains(repos.Reason, "repository:repo-B: the window starts") || !strings.Contains(repos.Reason, spanB.Format(time.RFC3339)) {
		t.Errorf("repo B (history starts after the window start) reason = %q", repos.Reason)
	}
	if strings.Contains(repos.Reason, "repository:repo-A") {
		t.Errorf("repo A (history starts before the window start) carries a span reason: %q", repos.Reason)
	}
	teams := read(teamSubject("T"), teamSubject("U"))
	if strings.Contains(teams.Reason, "team:T") {
		t.Errorf("team T owns repo A, whose history starts before the window: %q", teams.Reason)
	}
	if !strings.Contains(teams.Reason, "team:U: the window starts") || !strings.Contains(teams.Reason, spanB.Format(time.RFC3339)) {
		t.Errorf("team U (owns only repo B) reason = %q", teams.Reason)
	}
}

// A window with no overlapping unit serves no fact, and the span reason is
// still stated: missing is not silent. Only the sentinel rows come back.
func TestInvestmentSpanIsStatedForASubjectWithNoFact(t *testing.T) {
	t.Parallel()
	themes := map[string]float64{"feature_delivery": 1}
	tables := []fakeTable{
		{match: "FROM work_unit_investments", rows: [][]any{{uint8(255), "repo-A", themes, 0.0, uint64(1), time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)}}},
	}
	start := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	provider := findProvider(t, devhealthfacts.NewProviders(&fakeClient{tables: tables}), contextfabric.FactInvestment)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &start, End: &end}, Kind: contextfabric.FactInvestment,
		Subjects: []contextfabric.SubjectRef{repoSubject("repo-A")},
	})
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	if len(result.Facts) != 0 {
		t.Fatalf("facts = %d, want none for a window with no overlapping unit", len(result.Facts))
	}
	if !strings.Contains(result.Reason, "repository:repo-A: the window starts") || !strings.Contains(result.Reason, "2026-08-01T00:00:00Z") {
		t.Errorf("reason = %q, want the span reason for the subject with no fact", result.Reason)
	}
}
